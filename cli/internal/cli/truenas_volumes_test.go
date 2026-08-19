package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/jdwlabs/platform/internal/k8s"
	"github.com/jdwlabs/platform/internal/truenas"
)

// The driver configs are the real rendered shape, not a reduction of it: the
// dataset parents and the iSCSI naming affixes this command scopes itself with
// come from these documents, so a test that invented its own would not notice
// the parser drifting from what External Secrets actually writes.
const iscsiDriverConfig = `
driver: freenas-api-iscsi
httpConnection:
  protocol: https
  host: 192.168.1.205
  port: 443
  allowInsecure: true
  apiKey: not-a-real-key
zfs:
  datasetParentName: storage/k8s/iscsi/vols
  detachedSnapshotsDatasetParentName: storage/k8s/iscsi/snaps
iscsi:
  namePrefix: csi-
  nameSuffix: "-cluster"
`

const nfsDriverConfig = `
driver: freenas-api-nfs
httpConnection:
  protocol: https
  host: 192.168.1.205
  port: 443
  allowInsecure: true
  apiKey: not-a-real-key
zfs:
  datasetParentName: storage/k8s/vols
  detachedSnapshotsDatasetParentName: storage/k8s/snaps
nfs:
  shareHost: 192.168.1.205
`

func driverConfigSecret(name, body string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: truenas.DriverNamespace},
		Data:       map[string][]byte{"driver-config-file.yaml": []byte(body)},
	}
}

// The fixture is the state the ticket describes: one iSCSI volume still in use,
// one whose PVC was deleted and left the whole object graph behind, and one NFS
// dataset with its export still present.
func truenasFixture() *truenas.FakeMiddleware {
	return &truenas.FakeMiddleware{
		Datasets: []truenas.Dataset{
			{ID: "storage/k8s/iscsi/vols/pvc-live", Name: "pvc-live", Type: "VOLUME", Used: 4509715660},
			{ID: "storage/k8s/iscsi/vols/pvc-orphan", Name: "pvc-orphan", Type: "VOLUME", Used: 1288490188},
			{
				ID: "storage/k8s/vols/pvc-nfs-orphan", Name: "pvc-nfs-orphan", Type: "FILESYSTEM",
				Mountpoint: "/mnt/storage/k8s/vols/pvc-nfs-orphan", Used: 536870912,
			},
		},
		Extents: []truenas.Extent{
			{ID: 1, Name: "pvc-live", Disk: "zvol/storage/k8s/iscsi/vols/pvc-live"},
			{ID: 2, Name: "pvc-orphan", Disk: "zvol/storage/k8s/iscsi/vols/pvc-orphan"},
		},
		Targets: []truenas.Target{
			{ID: 1, Name: "csi-pvc-live-cluster"},
			{ID: 2, Name: "csi-pvc-orphan-cluster"},
		},
		Mappings: []truenas.TargetExtent{
			{ID: 1, TargetID: 1, ExtentID: 1},
			{ID: 2, TargetID: 2, ExtentID: 2},
		},
		Shares: []truenas.NFSShare{
			{ID: 1, Path: "/mnt/storage/k8s/vols/pvc-nfs-orphan"},
		},
	}
}

func runTrueNAS(t *testing.T, nas *truenas.FakeMiddleware, args ...string) (string, error) {
	t.Helper()

	kc := k8s.NewFake(
		driverConfigSecret("democratic-csi-iscsi-driver-config", iscsiDriverConfig),
		driverConfigSecret("democratic-csi-driver-config", nfsDriverConfig),
		// Only pvc-live is live, and it is proved so from the claim side: the
		// PVC names the PV, and the PV's volume handle names the zvol.
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "data-app-0", Namespace: "apps"},
			Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: "pv-live"},
		},
		&corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: "pv-live"},
			Spec: corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       truenas.ProvisionerISCSI,
					VolumeHandle: "pvc-live",
				},
			}},
			Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound},
		},
	)

	testTrueNASDialer = func(context.Context, truenas.DriverConfig, truenas.TLSOptions) (truenas.Caller, error) {
		return nas, nil
	}
	t.Cleanup(func() { testTrueNASDialer = nil })

	root := NewRootForTest(kc, nil)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestTrueNASList_DefaultShapeIsToonWithFourFields(t *testing.T) {
	out, err := runTrueNAS(t, truenasFixture(), "cluster", "volumes", "truenas", "list")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "count: 3 total (2 orphaned / 1 claimed / 0 attached / 0 other)") {
		t.Errorf("missing aggregate count line:\n%s", out)
	}
	if !strings.Contains(out, "volumes[3]{name,kind,class,size}:") {
		t.Errorf("missing TOON table header:\n%s", out)
	}
	if !strings.Contains(out, "pvc-live,zvol,claimed,4.2Gi") {
		t.Errorf("missing pvc-live row:\n%s", out)
	}
	if !strings.Contains(out, "pvc-orphan,zvol,orphaned,1.2Gi") {
		t.Errorf("missing pvc-orphan row:\n%s", out)
	}
	if !strings.Contains(out, "pvc-nfs-orphan,dataset,orphaned,512.0Mi") {
		t.Errorf("missing NFS row:\n%s", out)
	}
}

func TestTrueNASList_BareNounListsToo(t *testing.T) {
	out, err := runTrueNAS(t, truenasFixture(), "cluster", "volumes", "truenas")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "volumes[3]{name,kind,class,size}:") {
		t.Errorf("bare noun should list:\n%s", out)
	}
}

func TestTrueNASList_StorageClassNarrowsToOneDriver(t *testing.T) {
	out, err := runTrueNAS(t, truenasFixture(),
		"cluster", "volumes", "truenas", "list", "--storage-class", "truenas-nfs")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if strings.Contains(out, "pvc-orphan,") {
		t.Errorf("iSCSI volumes leaked into an --storage-class truenas-nfs report:\n%s", out)
	}
	if !strings.Contains(out, "pvc-nfs-orphan") {
		t.Errorf("missing the NFS dataset:\n%s", out)
	}
}

func TestTrueNASList_ClassFilterAndDefinitiveZero(t *testing.T) {
	out, err := runTrueNAS(t, truenasFixture(),
		"cluster", "volumes", "truenas", "list", "--class", "attached")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "0 candidates classed attached of 3 total") {
		t.Errorf("an empty filter must be definitive, not blank:\n%s", out)
	}
}

func TestTrueNASList_UnknownClassFailsLoudly(t *testing.T) {
	out, err := runTrueNAS(t, truenasFixture(),
		"cluster", "volumes", "truenas", "list", "--class", "dangling")
	if err == nil {
		t.Fatalf("an unknown class must fail\n%s", out)
	}
	if !strings.Contains(out, "orphaned") {
		t.Errorf("the error must name the valid set:\n%s", out)
	}
}

func TestTrueNASList_FieldsEscapeHatches(t *testing.T) {
	out, err := runTrueNAS(t, truenasFixture(),
		"cluster", "volumes", "truenas", "list", "--fields", "name,target,extent,dataset")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "volumes[3]{name,target,extent,dataset}:") {
		t.Errorf("--fields did not reshape the table:\n%s", out)
	}
	if !strings.Contains(out, "csi-pvc-orphan-cluster") {
		t.Errorf("the target name should be reportable:\n%s", out)
	}
}

func TestTrueNASList_UnknownFieldNamesTheValidSet(t *testing.T) {
	out, err := runTrueNAS(t, truenasFixture(),
		"cluster", "volumes", "truenas", "list", "--fields", "zvolname")
	if err == nil {
		t.Fatalf("an unknown field must fail\n%s", out)
	}
	if !strings.Contains(out, "storageClass") {
		t.Errorf("the error must list the valid fields:\n%s", out)
	}
}

func TestTrueNASReclaim_MissingConfirmationRefuses(t *testing.T) {
	nas := truenasFixture()
	out, err := runTrueNAS(t, nas, "cluster", "volumes", "truenas", "reclaim", "--all-orphaned")
	if err == nil {
		t.Fatalf("reclaim without --confirm or --dry-run must refuse\n%s", out)
	}
	if !strings.Contains(out, "--confirm") || !strings.Contains(out, "--dry-run") {
		t.Errorf("the refusal must name both escapes:\n%s", out)
	}
	if len(nas.Datasets) != 3 {
		t.Errorf("a refused invocation deleted something")
	}
}

func TestTrueNASReclaim_NoSelectionRefuses(t *testing.T) {
	out, err := runTrueNAS(t, truenasFixture(), "cluster", "volumes", "truenas", "reclaim", "--confirm")
	if err == nil {
		t.Fatalf("reclaim with no selection must refuse\n%s", out)
	}
	if !strings.Contains(out, "--all-orphaned") {
		t.Errorf("the refusal must name the selectors:\n%s", out)
	}
}

func TestTrueNASReclaim_DryRunMutatesNothing(t *testing.T) {
	nas := truenasFixture()
	out, err := runTrueNAS(t, nas, "cluster", "volumes", "truenas", "reclaim", "--all-orphaned", "--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "mode: dry-run") {
		t.Errorf("missing mode line:\n%s", out)
	}
	// Six objects: the orphaned zvol's mapping, extent, target and dataset,
	// plus the NFS export and its dataset.
	if !strings.Contains(out, "would delete 6 object(s) across 2 candidate(s)") {
		t.Errorf("missing the object-level preview:\n%s", out)
	}
	if !strings.Contains(out, "nothing was mutated") {
		t.Errorf("a dry run must say so:\n%s", out)
	}
	if !strings.Contains(out, "objects[6]{volume,step,kind,name}:") {
		t.Errorf("the per-object plan is the reviewable part:\n%s", out)
	}
	for _, call := range nas.Calls {
		if strings.HasSuffix(call, ".delete") {
			t.Fatalf("--dry-run issued %s", call)
		}
	}
	if len(nas.Datasets) != 3 || len(nas.Shares) != 1 {
		t.Errorf("--dry-run mutated the fixture")
	}
}

func TestTrueNASReclaim_ConfirmDeletesOnlyOrphans(t *testing.T) {
	nas := truenasFixture()
	out, err := runTrueNAS(t, nas, "cluster", "volumes", "truenas", "reclaim", "--all-orphaned", "--confirm")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "deleted 6 object(s) across 2 candidate(s)") {
		t.Errorf("missing the delete summary:\n%s", out)
	}
	if len(nas.Datasets) != 1 || nas.Datasets[0].Name != "pvc-live" {
		t.Fatalf("the claimed volume must survive, got %+v", nas.Datasets)
	}
	if len(nas.Extents) != 1 || nas.Extents[0].Name != "pvc-live" {
		t.Errorf("the live extent was deleted: %+v", nas.Extents)
	}
	if len(nas.Targets) != 1 || nas.Targets[0].Name != "csi-pvc-live-cluster" {
		t.Errorf("the live target was deleted: %+v", nas.Targets)
	}
	if len(nas.Shares) != 0 {
		t.Errorf("the orphaned export survived: %+v", nas.Shares)
	}
}

// The whole point of the command: a claim is resolved from the PersistentVolume
// side, so naming a live volume explicitly is refused rather than obeyed.
func TestTrueNASReclaim_RefusesNamedClaimedVolume(t *testing.T) {
	nas := truenasFixture()
	out, err := runTrueNAS(t, nas,
		"cluster", "volumes", "truenas", "reclaim", "--name", "pvc-live", "--confirm")
	if err == nil {
		t.Fatalf("a claimed volume must be refused\n%s", out)
	}
	if !strings.Contains(out, "refused[1]") {
		t.Errorf("a refusal must be reported as a row, not skipped:\n%s", out)
	}
	if !strings.Contains(out, "apps/data-app-0") {
		t.Errorf("the refusal must name the claim that holds it:\n%s", out)
	}
	if len(nas.Datasets) != 3 {
		t.Errorf("a refused reclaim deleted something")
	}
}

func TestTrueNASReclaim_RefusesUnknownName(t *testing.T) {
	out, err := runTrueNAS(t, truenasFixture(),
		"cluster", "volumes", "truenas", "reclaim", "--name", "pvc-nonexistent", "--dry-run")
	if err == nil {
		t.Fatalf("an unknown name must fail\n%s", out)
	}
	if !strings.Contains(out, "no such object") {
		t.Errorf("the refusal must say the name matched nothing:\n%s", out)
	}
}

func TestTrueNASReclaim_NameAndAllOrphanedConflict(t *testing.T) {
	out, err := runTrueNAS(t, truenasFixture(), "cluster", "volumes", "truenas",
		"reclaim", "--all-orphaned", "--name", "pvc-orphan", "--confirm")
	if err == nil {
		t.Fatalf("the two selectors must conflict\n%s", out)
	}
	if !strings.Contains(out, "mutually exclusive") {
		t.Errorf("the refusal must say why:\n%s", out)
	}
}

// An open session is the only evidence of liveness that survives Kubernetes
// having no record of the volume at all, so it has to outrank an absent PV.
func TestTrueNASReclaim_OpenSessionRefusesAnOtherwiseOrphanedZvol(t *testing.T) {
	nas := truenasFixture()
	nas.Sessions = []truenas.Session{{
		Target: "iqn.2005-10.org.freenas.ctl:csi-pvc-orphan-cluster", Initiator: "iqn.1993-08.org.debian:01:node1",
	}}

	out, err := runTrueNAS(t, nas, "cluster", "volumes", "truenas", "reclaim", "--all-orphaned", "--confirm")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	for _, d := range nas.Datasets {
		if d.Name == "pvc-orphan" {
			return
		}
	}
	t.Fatalf("a zvol with an open iSCSI session was deleted:\n%s", out)
}

// A session list that cannot be read is unknown liveness, not idle liveness —
// and dropping the zvols it covers out of the report is how that turns into an
// exit 0 that reports nothing was wrong. --all-orphaned selects by verdict, so
// a claimed or attached candidate was never asked for, but a candidate classed
// `other` is the classifier declining to conclude and is a refusal.
func TestTrueNASReclaim_UnreadableSessionListRefusesEveryZvol(t *testing.T) {
	nas := truenasFixture()
	nas.Fail = map[string]error{"iscsi.global.sessions": context.DeadlineExceeded}

	out, err := runTrueNAS(t, nas, "cluster", "volumes", "truenas", "reclaim", "--all-orphaned", "--confirm")
	if err == nil {
		t.Fatalf("an unreadable session list must not exit clean:\n%s", out)
	}
	if !strings.Contains(out, "warnings[1]") || !strings.Contains(out, "iSCSI session list could not be read") {
		t.Errorf("the failed read must be reported at the top of the output:\n%s", out)
	}
	if !strings.Contains(out, "refused[2]") {
		t.Errorf("every zvol of unknown liveness must be a refused row:\n%s", out)
	}
	for _, d := range nas.Datasets {
		if d.Name == "pvc-orphan" {
			// The NFS half still reclaims: its liveness never depended on the
			// session list.
			if len(nas.Shares) != 0 {
				t.Errorf("the NFS export should still have been reclaimed")
			}
			return
		}
	}
	t.Fatalf("a zvol of unknown liveness was deleted:\n%s", out)
}

// The same read failure on a read-only listing is a caveat, not a failure: the
// report is still the truth about what the NAS holds.
func TestTrueNASList_UnreadableSessionListIsWarnedAboutAndStillLists(t *testing.T) {
	nas := truenasFixture()
	nas.Fail = map[string]error{"iscsi.global.sessions": context.DeadlineExceeded}

	out, err := runTrueNAS(t, nas, "cluster", "volumes", "truenas", "list")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "warnings[1]") {
		t.Errorf("the failed read must be reported:\n%s", out)
	}
	if !strings.Contains(out, "count: 3 total (1 orphaned / 0 claimed / 0 attached / 2 other)") {
		t.Errorf("both zvols must fall to other:\n%s", out)
	}
}

func TestTrueNASJSON_EmitsOneEventPerCandidatePlusSummary(t *testing.T) {
	out, err := runTrueNAS(t, truenasFixture(), "--json", "cluster", "volumes", "truenas", "list")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 {
		t.Fatalf("want 3 candidate events plus a summary, got %d:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[len(lines)-1], `"status":"ok"`) {
		t.Errorf("the last event must be the summary:\n%s", lines[len(lines)-1])
	}
	if !strings.Contains(out, `"phase":"truenas-volumes"`) {
		t.Errorf("events must carry the phase:\n%s", out)
	}
}

// The credential reaches the middleware and nothing else. A driver config is
// the only place it exists in this process, so no report may echo it.
func TestTrueNAS_APIKeyNeverReachesOutput(t *testing.T) {
	for _, args := range [][]string{
		{"cluster", "volumes", "truenas", "list", "--full"},
		{"--json", "cluster", "volumes", "truenas", "list", "--full"},
		{"cluster", "volumes", "truenas", "reclaim", "--all-orphaned", "--dry-run"},
	} {
		out, err := runTrueNAS(t, truenasFixture(), args...)
		if err != nil {
			t.Fatalf("%v: unexpected error: %v\n%s", args, err, out)
		}
		if strings.Contains(out, "not-a-real-key") {
			t.Errorf("%v leaked the API key:\n%s", args, out)
		}
	}
}
