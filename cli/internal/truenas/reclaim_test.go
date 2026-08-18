package truenas

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

func reclaimFixture() *FakeMiddleware {
	return &FakeMiddleware{
		Datasets: []Dataset{{
			ID: "storage/k8s/iscsi/vols/pvc-orphan", Name: "pvc-orphan", Type: "VOLUME", Used: 1288490188,
		}},
		Extents:  []Extent{{ID: 7, Name: "pvc-orphan", Disk: "zvol/storage/k8s/iscsi/vols/pvc-orphan"}},
		Targets:  []Target{{ID: 3, Name: "csi-pvc-orphan-cluster"}},
		Mappings: []TargetExtent{{ID: 11, TargetID: 3, ExtentID: 7}},
	}
}

func orphanPlan(t *testing.T, nas *FakeMiddleware) []Object {
	t.Helper()
	cfg := NewDriverConfigForTest(ClassISCSI, "nas.test", "storage/k8s/iscsi/vols", "unused")
	inv, err := ReadInventory(context.Background(), nas, cfg)
	if err != nil {
		t.Fatalf("read inventory: %v", err)
	}
	cands := Classify(cfg, inv, nil)
	if len(cands) != 1 {
		t.Fatalf("want 1 candidate, got %d", len(cands))
	}
	if cands[0].Class != ClassOrphaned {
		t.Fatalf("want orphaned, got %s (%s)", cands[0].Class, cands[0].Reason)
	}
	nas.Calls = nil
	return cands[0].Objects
}

// The middleware refuses to delete an extent a mapping still joins, and refuses
// to delete a zvol an extent still exports, so the order is the difference
// between a reclaim and a half-deleted object graph.
func TestReclaimer_DeletesTheObjectGraphInDependencyOrder(t *testing.T) {
	nas := reclaimFixture()
	plan := orphanPlan(t, nas)

	r := NewReclaimer(nas, k8sfake.NewSimpleClientset())
	for _, obj := range plan {
		if err := r.Delete(context.Background(), obj); err != nil {
			t.Fatalf("delete %s: %v", obj, err)
		}
	}

	want := []string{
		"iscsi.targetextent.delete",
		"iscsi.extent.delete",
		"iscsi.target.delete",
		"pool.dataset.delete",
	}
	var got []string
	for _, c := range nas.Calls {
		if strings.HasSuffix(c, ".delete") {
			got = append(got, c)
		}
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("delete order:\n got %v\nwant %v", got, want)
	}
	if len(nas.Datasets) != 0 || len(nas.Extents) != 0 || len(nas.Targets) != 0 || len(nas.Mappings) != 0 {
		t.Errorf("fixture still holds objects: %+v", nas)
	}
}

// Middleware row IDs are small integers and get reused, so an ID that now
// addresses a different object must stop the delete rather than land on it.
func TestReclaimer_RefusesARowThatNoLongerCarriesThePlannedName(t *testing.T) {
	nas := reclaimFixture()
	plan := orphanPlan(t, nas)
	nas.Extents[0].Name = "someone-elses-volume"

	r := NewReclaimer(nas, k8sfake.NewSimpleClientset())
	var refused error
	for _, obj := range plan {
		if err := r.Delete(context.Background(), obj); err != nil {
			refused = err
			break
		}
	}
	if refused == nil {
		t.Fatalf("a renamed row must be refused")
	}
	if !strings.Contains(refused.Error(), "someone-elses-volume") {
		t.Errorf("refusal must name what it found: %v", refused)
	}
	if len(nas.Extents) != 1 {
		t.Errorf("the renamed extent was deleted anyway")
	}
}

// A re-run after a partial failure has to be safe, so an object that is already
// gone is the desired end state rather than an error.
func TestReclaimer_AbsentObjectIsIdempotentSuccess(t *testing.T) {
	nas := reclaimFixture()
	plan := orphanPlan(t, nas)
	nas.Extents = nil
	nas.Mappings = nil

	r := NewReclaimer(nas, k8sfake.NewSimpleClientset())
	for _, obj := range plan {
		if err := r.Delete(context.Background(), obj); err != nil {
			t.Fatalf("delete %s on an already-clean NAS: %v", obj, err)
		}
	}
	if len(nas.Datasets) != 0 {
		t.Errorf("the dataset should still have been deleted")
	}
}

// A PersistentVolume can be re-bound between the inventory read and the delete,
// and a Bound PV is in use whatever the plan concluded a moment earlier.
func TestReclaimer_RefusesAPersistentVolumeThatWentBound(t *testing.T) {
	kube := k8sfake.NewSimpleClientset(&corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pv-rebound"},
		Status:     corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound},
	})
	r := NewReclaimer(reclaimFixture(), kube)

	err := r.Delete(context.Background(), Object{Kind: KindPV, Name: "pv-rebound", ID: "pv-rebound"})
	if err == nil {
		t.Fatalf("a Bound PersistentVolume must be refused")
	}
	if !strings.Contains(err.Error(), "Bound") {
		t.Errorf("refusal must name the phase: %v", err)
	}
	if _, gerr := kube.CoreV1().PersistentVolumes().Get(
		context.Background(), "pv-rebound", metav1.GetOptions{}); gerr != nil {
		t.Errorf("the PersistentVolume was deleted anyway: %v", gerr)
	}
}

func TestReclaimer_ReleasedPersistentVolumeIsDeleted(t *testing.T) {
	kube := k8sfake.NewSimpleClientset(&corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pv-released"},
		Status:     corev1.PersistentVolumeStatus{Phase: corev1.VolumeReleased},
	})
	r := NewReclaimer(reclaimFixture(), kube)

	if err := r.Delete(context.Background(), Object{Kind: KindPV, Name: "pv-released", ID: "pv-released"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := kube.CoreV1().PersistentVolumes().Get(
		context.Background(), "pv-released", metav1.GetOptions{}); err == nil {
		t.Errorf("the Released PersistentVolume survived")
	}
}

// A middleware that refuses mid-plan must surface the refusal rather than carry
// on to the next object, which would delete a zvol whose extent still exports it.
func TestReclaimer_MiddlewareFailureStopsThePlan(t *testing.T) {
	nas := reclaimFixture()
	plan := orphanPlan(t, nas)
	boom := errors.New("middleware refused")
	nas.Fail = map[string]error{"iscsi.extent.delete": boom}

	r := NewReclaimer(nas, k8sfake.NewSimpleClientset())
	var got error
	for _, obj := range plan {
		if err := r.Delete(context.Background(), obj); err != nil {
			got = err
			break
		}
	}
	if !errors.Is(got, boom) {
		t.Fatalf("want the middleware error, got %v", got)
	}
	if len(nas.Datasets) != 1 {
		t.Errorf("the zvol was deleted despite its extent surviving")
	}
}
