// Package longhorn reads Longhorn volume state and decides which volumes are
// genuinely reclaimable.
//
// The classifier deliberately ignores status.kubernetesStatus.pvcName. That
// field records the claim a volume was created for and is not cleared when the
// claim moves on, so several generations of one StatefulSet's volumes all carry
// the same name — live and stranded alike. Liveness is resolved the other way
// round instead: from the PVC's spec.volumeName, which can only ever point at
// one volume.
package longhorn

import (
	"context"
	"fmt"
	"strconv"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// SystemNamespace is where Longhorn keeps its custom resources.
const SystemNamespace = "longhorn-system"

// VolumeGVR is the Longhorn Volume custom resource.
var VolumeGVR = schema.GroupVersionResource{Group: "longhorn.io", Version: "v1beta2", Resource: "volumes"}

// Volume is the subset of a Longhorn Volume that a reclaim decision needs.
type Volume struct {
	Name       string
	State      string
	Robustness string
	// ActualSize is status.actualSize in bytes — what the volume really occupies.
	// The nominal spec size is thin-provisioned and overstates a reclaim by
	// orders of magnitude, so it is not read here at all.
	ActualSize   int64
	LastPodRefAt string
	// RecordedPVC/RecordedNS are status.kubernetesStatus values, reported for
	// the operator's benefit only. They never take part in classification.
	RecordedPVC string
	RecordedNS  string
	// StatusPresent is false when the volume carries no status block. Such a
	// volume is never reclaimable: its live state is unknown, not idle.
	StatusPresent bool
}

// Binding is the live claim state of a volume, resolved from the cluster rather
// than from anything the volume records about itself.
type Binding struct {
	PVCNamespace string
	PVCName      string
	PVPhase      string
}

// Class is the reclaim verdict for one volume.
type Class string

const (
	ClassOrphaned Class = "orphaned"
	ClassClaimed  Class = "claimed"
	ClassAttached Class = "attached"
	ClassOther    Class = "other"
)

// Candidate is a volume plus its verdict.
type Candidate struct {
	Volume
	Class Class
	// Reason explains why a volume is not reclaimable. Empty when orphaned.
	Reason    string
	ClaimedBy string
	PVPhase   string
}

// Classify assigns every volume a reclaim verdict from live cluster state.
// Order matters: attachment and claims are checked before state, so a volume
// that is in use can never fall through to orphaned on a state quirk.
func Classify(vols []Volume, bindings map[string]Binding) []Candidate {
	out := make([]Candidate, 0, len(vols))
	for _, v := range vols {
		b := bindings[v.Name]
		c := Candidate{Volume: v, PVPhase: b.PVPhase}
		if b.PVCName != "" {
			c.ClaimedBy = b.PVCNamespace + "/" + b.PVCName
		}
		switch {
		case !v.StatusPresent:
			c.Class = ClassOther
			c.Reason = "status.state absent — live state unknown"
		case v.State == "attached":
			c.Class = ClassAttached
			c.Reason = "volume is attached"
		case c.ClaimedBy != "":
			c.Class = ClassClaimed
			c.Reason = fmt.Sprintf("PersistentVolumeClaim %s points at this volume", c.ClaimedBy)
		case b.PVPhase == "Bound":
			c.Class = ClassClaimed
			c.Reason = "PersistentVolume is Bound"
		case v.State != "detached":
			c.Class = ClassOther
			c.Reason = fmt.Sprintf("state is %q, not detached", v.State)
		default:
			c.Class = ClassOrphaned
		}
		out = append(out, c)
	}
	return out
}

// ListVolumes reads every Longhorn Volume in the system namespace.
func ListVolumes(ctx context.Context, dyn dynamic.Interface) ([]Volume, error) {
	list, err := dyn.Resource(VolumeGVR).Namespace(SystemNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list volumes.longhorn.io in %s: %w", SystemNamespace, err)
	}
	out := make([]Volume, 0, len(list.Items))
	for i := range list.Items {
		item := list.Items[i]
		name := item.GetName()
		if name == "" {
			return nil, fmt.Errorf("volumes.longhorn.io item %d has no metadata.name", i)
		}
		v := Volume{Name: name}
		status, found, err := unstructuredMap(item.Object, "status")
		if err != nil {
			return nil, fmt.Errorf("volume %s: %w", name, err)
		}
		if !found {
			out = append(out, v)
			continue
		}
		v.StatusPresent = true
		v.State = stringField(status, "state")
		v.Robustness = stringField(status, "robustness")
		size, err := int64Field(status, "actualSize")
		if err != nil {
			return nil, fmt.Errorf("volume %s: status.%w", name, err)
		}
		v.ActualSize = size
		if ks, found, err := unstructuredMap(status, "kubernetesStatus"); err != nil {
			return nil, fmt.Errorf("volume %s: status.%w", name, err)
		} else if found {
			v.RecordedPVC = stringField(ks, "pvcName")
			v.RecordedNS = stringField(ks, "namespace")
			v.LastPodRefAt = stringField(ks, "lastPodRefAt")
		}
		out = append(out, v)
	}
	return out, nil
}

// ResolveBindings maps volume name to its live claim state: the PVC whose
// spec.volumeName names it, and the phase of the PersistentVolume of the same
// name. A volume absent from the result is claimed by nothing.
func ResolveBindings(ctx context.Context, kube kubernetes.Interface) (map[string]Binding, error) {
	out := map[string]Binding{}

	pvcs, err := kube.CoreV1().PersistentVolumeClaims("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list persistentvolumeclaims: %w", err)
	}
	for _, pvc := range pvcs.Items {
		if pvc.Spec.VolumeName == "" {
			continue
		}
		b := out[pvc.Spec.VolumeName]
		b.PVCName = pvc.Name
		b.PVCNamespace = pvc.Namespace
		out[pvc.Spec.VolumeName] = b
	}

	pvs, err := kube.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list persistentvolumes: %w", err)
	}
	for _, pv := range pvs.Items {
		b := out[pv.Name]
		b.PVPhase = string(pv.Status.Phase)
		out[pv.Name] = b
	}
	return out, nil
}

// DeleteVolume removes one Longhorn Volume. An already-absent volume is the
// desired end state, so it reports success and the caller stays idempotent.
func DeleteVolume(ctx context.Context, dyn dynamic.Interface, name string) error {
	err := dyn.Resource(VolumeGVR).Namespace(SystemNamespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete volumes.longhorn.io/%s: %w", name, err)
	}
	return nil
}

// FormatBytes renders a byte count in binary units.
func FormatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	value := float64(b)
	suffixes := []string{"Ki", "Mi", "Gi", "Ti", "Pi"}
	var suffix string
	for _, s := range suffixes {
		value /= unit
		suffix = s
		if value < unit {
			break
		}
	}
	return fmt.Sprintf("%.1f%s", value, suffix)
}

func unstructuredMap(parent map[string]interface{}, key string) (map[string]interface{}, bool, error) {
	raw, ok := parent[key]
	if !ok || raw == nil {
		return nil, false, nil
	}
	m, ok := raw.(map[string]interface{})
	if !ok {
		return nil, false, fmt.Errorf("%s is %T, want an object", key, raw)
	}
	return m, true, nil
}

func stringField(m map[string]interface{}, key string) string {
	s, _ := m[key].(string)
	return s
}

// int64Field reads a numeric field that a CRD may serialise as a number or as a
// string. An unexpected type is returned as an error rather than defaulted to
// zero: a silent 0 would report a reclaim as freeing nothing, which reads as
// success.
func int64Field(m map[string]interface{}, key string) (int64, error) {
	raw, ok := m[key]
	if !ok || raw == nil {
		return 0, nil
	}
	switch v := raw.(type) {
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	case float64:
		return int64(v), nil
	case string:
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%s is %q, want an integer", key, v)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("%s is %T, want an integer", key, raw)
	}
}
