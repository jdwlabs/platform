package longhorn

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func TestClassify(t *testing.T) {
	// Every volume in this table carries the same recorded pvcName, which is the
	// trap this classifier exists to avoid: the field is historical and survives
	// on volumes the PVC no longer points at.
	const samePVC = "data-platform-vault-0"

	vols := []Volume{
		{Name: "pvc-live", State: "attached", RecordedPVC: samePVC, RecordedNS: "vault", StatusPresent: true},
		{Name: "pvc-claimed-detached", State: "detached", RecordedPVC: samePVC, RecordedNS: "vault", StatusPresent: true},
		{Name: "pvc-released-pv", State: "detached", RecordedPVC: samePVC, RecordedNS: "vault", StatusPresent: true},
		{Name: "pvc-orphan", State: "detached", RecordedPVC: samePVC, RecordedNS: "vault", StatusPresent: true},
		{Name: "pvc-faulted", State: "faulted", RecordedPVC: samePVC, RecordedNS: "vault", StatusPresent: true},
		{Name: "pvc-no-status", StatusPresent: false},
	}
	bindings := map[string]Binding{
		"pvc-live":             {PVCNamespace: "vault", PVCName: samePVC, PVPhase: "Bound"},
		"pvc-claimed-detached": {PVCNamespace: "vault", PVCName: samePVC, PVPhase: "Bound"},
		"pvc-released-pv":      {PVPhase: "Bound"},
		"pvc-orphan":           {PVPhase: "Released"},
	}

	got := Classify(vols, bindings)
	want := map[string]Class{
		"pvc-live":             ClassAttached,
		"pvc-claimed-detached": ClassClaimed,
		"pvc-released-pv":      ClassClaimed,
		"pvc-orphan":           ClassOrphaned,
		"pvc-faulted":          ClassOther,
		"pvc-no-status":        ClassOther,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d candidates, want %d", len(got), len(want))
	}
	for _, c := range got {
		if c.Class != want[c.Name] {
			t.Errorf("%s: class = %q, want %q (reason %q)", c.Name, c.Class, want[c.Name], c.Reason)
		}
		if c.Class == ClassOrphaned && c.Reason != "" {
			t.Errorf("%s: orphaned candidate should carry no refusal reason, got %q", c.Name, c.Reason)
		}
		if c.Class != ClassOrphaned && c.Reason == "" {
			t.Errorf("%s: non-orphan must explain itself", c.Name)
		}
	}
}

func TestClassify_RecordedPVCNameNeverProvesLiveness(t *testing.T) {
	// A volume whose recorded pvcName matches a PVC that exists but points at a
	// different volume is an orphan. Trusting the recorded name here is exactly
	// the bug that would delete a live Vault volume.
	vols := []Volume{{Name: "pvc-stale", State: "detached", RecordedPVC: "data-platform-vault-0", StatusPresent: true}}
	bindings := map[string]Binding{
		"pvc-current": {PVCNamespace: "vault", PVCName: "data-platform-vault-0", PVPhase: "Bound"},
	}
	got := Classify(vols, bindings)
	if got[0].Class != ClassOrphaned {
		t.Fatalf("class = %q, want %q", got[0].Class, ClassOrphaned)
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{0, "0B"},
		{512, "512B"},
		{1024, "1.0Ki"},
		{1536, "1.5Ki"},
		{10 * 1024 * 1024, "10.0Mi"},
		{4509715660, "4.2Gi"},
	}
	for _, tt := range tests {
		if got := FormatBytes(tt.in); got != tt.want {
			t.Errorf("FormatBytes(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func newFakeDynamic(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{VolumeGVR: "VolumeList"},
		objs...,
	)
}

func volumeObject(name string, status map[string]interface{}) *unstructured.Unstructured {
	obj := map[string]interface{}{
		"apiVersion": "longhorn.io/v1beta2",
		"kind":       "Volume",
		"metadata":   map[string]interface{}{"name": name, "namespace": SystemNamespace},
	}
	if status != nil {
		obj["status"] = status
	}
	return &unstructured.Unstructured{Object: obj}
}

func TestListVolumes(t *testing.T) {
	dc := newFakeDynamic(
		volumeObject("pvc-a", map[string]interface{}{
			"state":      "detached",
			"robustness": "unknown",
			"actualSize": int64(1024),
			"kubernetesStatus": map[string]interface{}{
				"pvcName":      "data-platform-vault-0",
				"namespace":    "vault",
				"lastPodRefAt": "2026-07-28T01:02:03Z",
			},
		}),
		volumeObject("pvc-b", nil),
	)

	vols, err := ListVolumes(context.Background(), dc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vols) != 2 {
		t.Fatalf("got %d volumes, want 2", len(vols))
	}
	byName := map[string]Volume{}
	for _, v := range vols {
		byName[v.Name] = v
	}
	a := byName["pvc-a"]
	if a.State != "detached" || a.ActualSize != 1024 || a.RecordedPVC != "data-platform-vault-0" ||
		a.LastPodRefAt != "2026-07-28T01:02:03Z" || !a.StatusPresent {
		t.Fatalf("pvc-a parsed wrong: %+v", a)
	}
	if byName["pvc-b"].StatusPresent {
		t.Fatalf("a volume with no status block must not report StatusPresent")
	}
}

func TestListVolumes_UnexpectedActualSizeTypeFailsLoudly(t *testing.T) {
	dc := newFakeDynamic(volumeObject("pvc-a", map[string]interface{}{
		"state":      "detached",
		"actualSize": true,
	}))
	if _, err := ListVolumes(context.Background(), dc); err == nil {
		t.Fatal("expected an error, got nil")
	} else if !strings.Contains(err.Error(), "actualSize") {
		t.Fatalf("error should name the offending field, got: %v", err)
	}
}

func TestListVolumes_StringActualSizeIsAccepted(t *testing.T) {
	dc := newFakeDynamic(volumeObject("pvc-a", map[string]interface{}{
		"state":      "detached",
		"actualSize": "2048",
	}))
	vols, err := ListVolumes(context.Background(), dc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vols[0].ActualSize != 2048 {
		t.Fatalf("actualSize = %d, want 2048", vols[0].ActualSize)
	}
}

func TestResolveBindings(t *testing.T) {
	kc := fake.NewSimpleClientset(
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "data-platform-vault-0", Namespace: "vault"},
			Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: "pvc-live"},
		},
		// A PVC with no bound volume yet must not claim anything.
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "pending", Namespace: "vault"},
		},
		&corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: "pvc-live"},
			Status:     corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound},
		},
		&corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: "pvc-orphan"},
			Status:     corev1.PersistentVolumeStatus{Phase: corev1.VolumeReleased},
		},
	)

	got, err := ResolveBindings(context.Background(), kc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b := got["pvc-live"]; b.PVCName != "data-platform-vault-0" || b.PVCNamespace != "vault" || b.PVPhase != "Bound" {
		t.Fatalf("pvc-live binding wrong: %+v", b)
	}
	if b := got["pvc-orphan"]; b.PVCName != "" || b.PVPhase != "Released" {
		t.Fatalf("pvc-orphan binding wrong: %+v", b)
	}
}

func TestDeleteVolume(t *testing.T) {
	dc := newFakeDynamic(volumeObject("pvc-a", map[string]interface{}{"state": "detached"}))
	if err := DeleteVolume(context.Background(), dc, "pvc-a"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	vols, err := ListVolumes(context.Background(), dc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vols) != 0 {
		t.Fatalf("volume was not deleted: %+v", vols)
	}
}

func TestDeleteVolume_MissingIsNotAnError(t *testing.T) {
	// Reclaim is safe to re-run: a volume already gone is the desired state.
	dc := newFakeDynamic()
	if err := DeleteVolume(context.Background(), dc, "pvc-gone"); err != nil {
		t.Fatalf("deleting an absent volume must be a no-op, got: %v", err)
	}
}
