package cli

import (
	"bytes"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/jdwlabs/platform/internal/k8s"
	"github.com/jdwlabs/platform/internal/longhorn"
)

// The fixture mirrors the shape that makes this command necessary: three
// generations of one StatefulSet's volumes, all recording the same pvcName,
// only one of them actually claimed.
func volumeFixture(name, state string, size int64, lastPodRefAt string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "longhorn.io/v1beta2",
		"kind":       "Volume",
		"metadata":   map[string]interface{}{"name": name, "namespace": longhorn.SystemNamespace},
		"status": map[string]interface{}{
			"state":      state,
			"robustness": "healthy",
			"actualSize": size,
			"kubernetesStatus": map[string]interface{}{
				"pvcName":      "data-platform-vault-0",
				"namespace":    "vault",
				"lastPodRefAt": lastPodRefAt,
			},
		},
	}}
}

func volumeClients() (*dynamicfake.FakeDynamicClient, *corev1.PersistentVolumeClaim) {
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			longhorn.VolumeGVR: "VolumeList",
			{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}: "ApplicationList",
		},
		volumeFixture("pvc-live", "attached", 4509715660, "2026-07-30T00:00:00Z"),
		volumeFixture("pvc-orphan", "detached", 1288490188, "2026-07-20T00:00:00Z"),
		volumeFixture("pvc-orphan-2", "detached", 0, ""),
	)
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "data-platform-vault-0", Namespace: "vault"},
		Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: "pvc-live"},
	}
	return dc, pvc
}

func runVolumes(t *testing.T, args ...string) (string, error) {
	t.Helper()
	dc, pvc := volumeClients()
	kc := k8s.NewFake(
		pvc,
		&corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: "pvc-live"},
			Status:     corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound},
		},
		&corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: "pvc-orphan"},
			Status:     corev1.PersistentVolumeStatus{Phase: corev1.VolumeReleased},
		},
	)
	root := NewRootForTest(kc, dc)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestVolumesList_DefaultShapeIsToonWithFourFields(t *testing.T) {
	out, err := runVolumes(t, "cluster", "volumes", "list")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "count: 3 total (2 orphaned / 0 claimed / 1 attached / 0 other)") {
		t.Errorf("missing aggregate count line:\n%s", out)
	}
	if !strings.Contains(out, "volumes[3]{name,state,class,size}:") {
		t.Errorf("missing TOON table header:\n%s", out)
	}
	// pvc-live is attached AND claimed; attachment is reported first because it
	// is the stronger statement about liveness.
	if !strings.Contains(out, "pvc-live,attached,attached,4.2Gi") {
		t.Errorf("missing pvc-live row:\n%s", out)
	}
	if !strings.Contains(out, "pvc-orphan,detached,orphaned,1.2Gi") {
		t.Errorf("missing pvc-orphan row:\n%s", out)
	}
	if !strings.Contains(out, "help[") {
		t.Errorf("list output should suggest next steps:\n%s", out)
	}
}

func TestVolumesList_BareNounListsToo(t *testing.T) {
	out, err := runVolumes(t, "cluster", "volumes")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "volumes[3]{name,state,class,size}:") {
		t.Fatalf("bare noun should print content, got:\n%s", out)
	}
}

func TestVolumesList_ClassFilterAndDefinitiveZero(t *testing.T) {
	out, err := runVolumes(t, "cluster", "volumes", "list", "--class", "attached")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "volumes[1]{name,state,class,size}:") {
		t.Errorf("filter should narrow the table:\n%s", out)
	}

	out, err = runVolumes(t, "cluster", "volumes", "list", "--class", "other")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "volumes[0]{name,state,class,size}:") {
		t.Errorf("empty result still needs a counted header:\n%s", out)
	}
	if !strings.Contains(out, "result: 0 volumes classed other of 3 total") {
		t.Errorf("empty result must be stated definitively:\n%s", out)
	}
}

func TestVolumesList_UnknownClassFailsLoudly(t *testing.T) {
	out, err := runVolumes(t, "cluster", "volumes", "list", "--class", "detached")
	if err == nil {
		t.Fatalf("expected an error for an unknown class\n%s", out)
	}
	if !strings.Contains(out, "orphaned") {
		t.Errorf("error should list the valid classes:\n%s", out)
	}
}

func TestVolumesList_UnknownFlagIsRejected(t *testing.T) {
	out, err := runVolumes(t, "cluster", "volumes", "list", "--stat", "orphaned")
	if err == nil {
		t.Fatalf("expected an unknown-flag error\n%s", out)
	}
	// An unknown flag must not read as an empty result set: the error and the
	// valid flag list both have to reach stdout.
	if !strings.Contains(out, "error:") || !strings.Contains(out, "unknown flag") {
		t.Errorf("unknown flag must be reported on stdout:\n%s", out)
	}
	if !strings.Contains(out, "--class") || !strings.Contains(out, "--fields") {
		t.Errorf("error should inline the valid flags:\n%s", out)
	}
}

func TestVolumesList_FieldsEscapeHatches(t *testing.T) {
	out, err := runVolumes(t, "cluster", "volumes", "list", "--fields", "name,claimedBy,lastPodRefAt")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "volumes[3]{name,claimedBy,lastPodRefAt}:") {
		t.Errorf("--fields should drive the header:\n%s", out)
	}
	if !strings.Contains(out, `pvc-live,vault/data-platform-vault-0,"2026-07-30T00:00:00Z"`) {
		t.Errorf("claim should be reported from the PVC, and a timestamp quoted:\n%s", out)
	}

	out, err = runVolumes(t, "cluster", "volumes", "list", "--full")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "recordedPvc") || !strings.Contains(out, "pvPhase") {
		t.Errorf("--full should include every field:\n%s", out)
	}
}

func TestVolumesList_UnknownFieldNamesTheValidSet(t *testing.T) {
	out, err := runVolumes(t, "cluster", "volumes", "list", "--fields", "name,bogus")
	if err == nil {
		t.Fatalf("expected an error for an unknown field\n%s", out)
	}
	if !strings.Contains(out, "lastPodRefAt") {
		t.Errorf("error should name the valid fields:\n%s", out)
	}
}

func TestVolumesList_FieldsAndFullConflict(t *testing.T) {
	if _, err := runVolumes(t, "cluster", "volumes", "list", "--fields", "name", "--full"); err == nil {
		t.Fatal("expected an error when --fields and --full are combined")
	}
}

func TestVolumesReclaim_MissingConfirmationRefuses(t *testing.T) {
	out, err := runVolumes(t, "cluster", "volumes", "reclaim", "--all-orphaned")
	if err == nil {
		t.Fatalf("reclaim must refuse without --confirm or --dry-run\n%s", out)
	}
	if !strings.Contains(out, "--confirm") || !strings.Contains(out, "--dry-run") {
		t.Errorf("refusal should name both escape hatches:\n%s", out)
	}
	// The refusal must land before any cluster read, so no data is printed.
	if strings.Contains(out, "reclaim[") {
		t.Errorf("nothing should have been listed:\n%s", out)
	}
}

func TestVolumesReclaim_NoSelectionRefuses(t *testing.T) {
	out, err := runVolumes(t, "cluster", "volumes", "reclaim", "--confirm")
	if err == nil {
		t.Fatalf("reclaim must refuse with no selection\n%s", out)
	}
	if !strings.Contains(out, "--all-orphaned") || !strings.Contains(out, "--name") {
		t.Errorf("refusal should name both selection modes:\n%s", out)
	}
}

func TestVolumesReclaim_NameAndAllOrphanedConflict(t *testing.T) {
	if _, err := runVolumes(t, "cluster", "volumes", "reclaim",
		"--all-orphaned", "--name", "pvc-orphan", "--confirm"); err == nil {
		t.Fatal("expected an error when both selection modes are given")
	}
}

func TestVolumesReclaim_DryRunMutatesNothing(t *testing.T) {
	dc, pvc := volumeClients()
	kc := k8s.NewFake(pvc)
	root := NewRootForTest(kc, dc)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"cluster", "volumes", "reclaim", "--all-orphaned", "--min-age", "0", "--dry-run"})
	if err := root.Execute(); err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out.String())
	}

	got := out.String()
	if !strings.Contains(got, "mode: dry-run") {
		t.Errorf("dry-run must announce itself:\n%s", got)
	}
	if !strings.Contains(got, "reclaim[2]{name,class,size,lastPodRefAt}:") {
		t.Errorf("dry-run must list exactly what it would delete:\n%s", got)
	}
	if !strings.Contains(got, "nothing was mutated") {
		t.Errorf("dry-run must state that nothing changed:\n%s", got)
	}

	remaining, err := longhorn.ListVolumes(root.Context(), dc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(remaining) != 3 {
		t.Fatalf("dry-run deleted volumes: %d remain, want 3", len(remaining))
	}
}

func TestVolumesReclaim_ConfirmDeletesOnlyOrphans(t *testing.T) {
	dc, pvc := volumeClients()
	kc := k8s.NewFake(pvc, &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pvc-live"},
		Status:     corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound},
	})
	root := NewRootForTest(kc, dc)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"cluster", "volumes", "reclaim", "--all-orphaned", "--min-age", "0", "--confirm"})
	if err := root.Execute(); err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out.String())
	}

	got := out.String()
	if !strings.Contains(got, "deleted[2]{name,class,size,lastPodRefAt}:") {
		t.Errorf("expected a deleted table:\n%s", got)
	}
	if !strings.Contains(got, "result: deleted 2 volume(s) reclaiming 1.2Gi with 0 refused") {
		t.Errorf("expected an aggregate result line:\n%s", got)
	}

	remaining, err := longhorn.ListVolumes(root.Context(), dc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(remaining) != 1 || remaining[0].Name != "pvc-live" {
		t.Fatalf("the claimed volume must survive, got %+v", remaining)
	}
}

func TestVolumesReclaim_RefusesNamedBoundVolume(t *testing.T) {
	out, err := runVolumes(t, "cluster", "volumes", "reclaim", "--name", "pvc-live", "--confirm")
	if err == nil {
		t.Fatalf("expected a non-zero exit when a named volume is refused\n%s", out)
	}
	if !strings.Contains(out, "refused[1]{name,class,reason}:") {
		t.Errorf("refusal must be reported as data, not swallowed:\n%s", out)
	}
	if !strings.Contains(out, "result: 0 deleted and 1 refused") {
		t.Errorf("refusal must be counted in the result line:\n%s", out)
	}
}

func TestVolumesReclaim_RefusesUnknownName(t *testing.T) {
	out, err := runVolumes(t, "cluster", "volumes", "reclaim", "--name", "pvc-does-not-exist", "--confirm")
	if err == nil {
		t.Fatalf("a name that matches no volume must not report success\n%s", out)
	}
	if !strings.Contains(out, "pvc-does-not-exist") {
		t.Errorf("refusal should name the volume:\n%s", out)
	}
}

func TestVolumesReclaim_MinAgeSkipsRecentOrphans(t *testing.T) {
	out, err := runVolumes(t, "cluster", "volumes", "reclaim", "--all-orphaned", "--min-age", "8760h", "--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "skipped[2]{name,reason}:") {
		t.Errorf("age-excluded orphans must be reported, not silently dropped:\n%s", out)
	}
	if !strings.Contains(out, "reclaim[0]{name,class,size,lastPodRefAt}:") {
		t.Errorf("expected an empty candidate table:\n%s", out)
	}
	if !strings.Contains(out, "result: 0 candidates") {
		t.Errorf("expected a definitive zero:\n%s", out)
	}
}

func TestVolumesReclaim_ZeroOrphansIsDefinitive(t *testing.T) {
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{longhorn.VolumeGVR: "VolumeList"},
		volumeFixture("pvc-live", "attached", 1024, "2026-07-30T00:00:00Z"),
	)
	kc := k8s.NewFake(&corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "data-platform-vault-0", Namespace: "vault"},
		Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: "pvc-live"},
	})
	root := NewRootForTest(kc, dc)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"cluster", "volumes", "reclaim", "--all-orphaned", "--dry-run"})
	if err := root.Execute(); err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "result: 0 candidates — 0 orphaned volumes of 1 total") {
		t.Fatalf("zero orphans must be stated definitively:\n%s", out.String())
	}
}

func TestVolumesJSON_EmitsOneEventPerVolumePlusSummary(t *testing.T) {
	out, err := runVolumes(t, "--json", "cluster", "volumes", "list")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected 3 volume events plus a summary, got %d:\n%s", len(lines), out)
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, `{"ts":`) || !strings.Contains(l, `"phase":"volumes"`) {
			t.Fatalf("not a platformctl event: %s", l)
		}
	}
}
