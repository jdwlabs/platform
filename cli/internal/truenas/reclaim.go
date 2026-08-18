package truenas

import (
	"context"
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Reclaimer executes a candidate's delete plan.
//
// Every step re-reads its object from the NAS and compares the name against the
// plan before issuing the delete. Middleware row IDs are small integers that are
// reused, and the plan is built from an inventory read seconds earlier, so an ID
// alone is not enough to prove the delete lands on the object the operator was
// shown.
type Reclaimer struct {
	call Caller
	kube kubernetes.Interface
}

func NewReclaimer(call Caller, kube kubernetes.Interface) *Reclaimer {
	return &Reclaimer{call: call, kube: kube}
}

// Delete removes one planned object. An object that is already absent is the
// desired end state, so it reports success and a re-run stays idempotent.
func (r *Reclaimer) Delete(ctx context.Context, obj Object) error {
	switch obj.Kind {
	case KindMapping:
		return r.deleteRow(ctx, obj, "iscsi.targetextent", mappingRowName)
	case KindExtent:
		return r.deleteRow(ctx, obj, "iscsi.extent", rowFieldName("name"))
	case KindTarget:
		return r.deleteRow(ctx, obj, "iscsi.target", rowFieldName("name"))
	case KindShare:
		return r.deleteRow(ctx, obj, "sharing.nfs", rowFieldName("path"))
	case KindZvol, KindDataset:
		return r.deleteDataset(ctx, obj)
	case KindPV:
		return r.deletePV(ctx, obj)
	default:
		return fmt.Errorf("no delete is defined for %s", obj)
	}
}

// row is one middleware record, decoded loosely because each namespace names
// its identity field differently and the guard only needs that one field.
type row map[string]any

type nameOfRow func(row) string

func rowFieldName(field string) nameOfRow {
	return func(r row) string {
		s, _ := r[field].(string)
		return s
	}
}

func mappingRowName(r row) string {
	target, tok := numberField(r, "target")
	extent, eok := numberField(r, "extent")
	if !tok || !eok {
		return ""
	}
	return mappingName(TargetExtent{TargetID: target, ExtentID: extent})
}

func numberField(r row, field string) (int, bool) {
	switch v := r[field].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	default:
		return 0, false
	}
}

// deleteRow guards and deletes one middleware row addressed by numeric ID.
func (r *Reclaimer) deleteRow(ctx context.Context, obj Object, namespace string, nameOf nameOfRow) error {
	id, err := strconv.Atoi(obj.ID)
	if err != nil {
		return fmt.Errorf("%s has a non-numeric middleware id %q", obj, obj.ID)
	}

	var rows []row
	if err := r.call.Call(ctx, namespace+".query", []any{[]any{[]any{"id", "=", id}}}, &rows); err != nil {
		return fmt.Errorf("re-read %s before deleting it: %w", obj, err)
	}
	if len(rows) == 0 {
		return nil
	}
	if len(rows) > 1 {
		return fmt.Errorf("%s: middleware returned %d rows for id %d", obj, len(rows), id)
	}
	if got := nameOf(rows[0]); got != obj.Name {
		return fmt.Errorf("refusing to delete %s id %d: it is now named %q, not %q",
			obj.Kind, id, got, obj.Name)
	}
	if err := r.call.Call(ctx, namespace+".delete", []any{id}, nil); err != nil {
		return fmt.Errorf("delete %s: %w", obj, err)
	}
	return nil
}

func (r *Reclaimer) deleteDataset(ctx context.Context, obj Object) error {
	var rows []row
	if err := r.call.Call(ctx, "pool.dataset.query", []any{[]any{[]any{"id", "=", obj.ID}}}, &rows); err != nil {
		return fmt.Errorf("re-read %s before deleting it: %w", obj, err)
	}
	if len(rows) == 0 {
		return nil
	}
	if got, _ := rows[0]["id"].(string); got != obj.Name {
		return fmt.Errorf("refusing to delete dataset %q: the middleware returned %q", obj.Name, got)
	}
	// recursive:false is the whole guard against a child dataset going with it.
	// It is passed explicitly rather than left to the middleware default so the
	// intent survives a change to that default.
	params := []any{obj.ID, map[string]any{"recursive": false, "force": false}}
	if err := r.call.Call(ctx, "pool.dataset.delete", params, nil); err != nil {
		return fmt.Errorf("delete %s: %w", obj, err)
	}
	return nil
}

// deletePV removes the cluster's record of the volume. The phase is re-checked
// immediately before the delete because a PersistentVolume can be re-bound
// between the inventory read and this call, and a Bound PV is in use whatever
// the plan said.
func (r *Reclaimer) deletePV(ctx context.Context, obj Object) error {
	pv, err := r.kube.CoreV1().PersistentVolumes().Get(ctx, obj.Name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("re-read %s before deleting it: %w", obj, err)
	}
	phase := pv.Status.Phase
	if phase != corev1.VolumeReleased && phase != corev1.VolumeFailed {
		return fmt.Errorf("refusing to delete PersistentVolume %s: it is now %s", obj.Name, phase)
	}
	if err := r.kube.CoreV1().PersistentVolumes().Delete(ctx, obj.Name, metav1.DeleteOptions{}); err != nil &&
		!apierrors.IsNotFound(err) {
		return fmt.Errorf("delete %s: %w", obj, err)
	}
	return nil
}
