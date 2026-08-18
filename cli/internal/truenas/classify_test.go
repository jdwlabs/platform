package truenas

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/jdwlabs/platform/internal/k8s"
)

const iscsiParent = "storage/k8s/iscsi/vols"

func iscsiConfig() DriverConfig {
	cfg := NewDriverConfigForTest(ClassISCSI, "192.168.1.205", iscsiParent, "unused")
	cfg.TargetPrefix = "csi-"
	cfg.TargetSuffix = "-cluster"
	return cfg
}

func nfsConfig() DriverConfig {
	return NewDriverConfigForTest(ClassNFS, "192.168.1.205", "storage/k8s/vols", "unused")
}

// zvolFixture builds the three-object graph one truenas-iscsi PVC leaves
// behind, with the IDs deliberately not matching the names: the classifier must
// walk extent.disk and the mapping rows rather than pattern-match names.
func zvolFixture(name string, extentID, targetID, mappingID int, used int64) Inventory {
	return Inventory{
		Datasets: []Dataset{{
			ID: iscsiParent + "/" + name, Name: name, Type: "VOLUME", Used: used,
		}},
		Extents:       []Extent{{ID: extentID, Name: name, Disk: "zvol/" + iscsiParent + "/" + name}},
		Targets:       []Target{{ID: targetID, Name: "csi-" + name + "-cluster"}},
		Mappings:      []TargetExtent{{ID: mappingID, TargetID: targetID, ExtentID: extentID}},
		SessionsKnown: true,
	}
}

func findCandidate(t *testing.T, cands []Candidate, name string) Candidate {
	t.Helper()
	for _, c := range cands {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no candidate named %s in %v", name, candidateNames(cands))
	return Candidate{}
}

func candidateNames(cands []Candidate) []string {
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.Name)
	}
	return out
}

func TestClassify_UnreferencedZvolIsOrphanedWithTheWholeObjectGraphPlanned(t *testing.T) {
	inv := zvolFixture("pvc-dead", 7, 3, 11, 1288490188)
	cands := Classify(iscsiConfig(), inv, nil)

	c := findCandidate(t, cands, "pvc-dead")
	if c.Class != ClassOrphaned {
		t.Fatalf("class = %s (%s), want orphaned", c.Class, c.Reason)
	}
	if c.Kind != KindZvol {
		t.Errorf("kind = %s, want zvol", c.Kind)
	}
	// The count is the point of the ticket: one PVC leaks three NAS objects
	// plus the mapping that joins two of them.
	want := []string{"targetextent/target-3-extent-7", "extent/pvc-dead", "target/csi-pvc-dead-cluster",
		"zvol/" + iscsiParent + "/pvc-dead"}
	if got := planNames(c); !equalStrings(got, want) {
		t.Errorf("delete plan = %v, want %v", got, want)
	}
}

// The name a zvol carries is the PVC UID it was created for and outlives every
// object that named it, so it is exactly as untrustworthy as the Longhorn
// volume's recorded pvcName. Liveness has to come from the PV side.
func TestClassify_LiveClaimIsResolvedFromThePersistentVolumeNotTheZvolName(t *testing.T) {
	inv := zvolFixture("pvc-live", 7, 3, 11, 100)
	refs := []Reference{{
		PVName:    "pvc-live",
		PVPhase:   "Bound",
		ClaimedBy: "database/data-postgres-0",
		Tokens:    []string{"pvc-live", "iqn.2005-10.org.freenas.ctl:csi-pvc-live-cluster"},
	}}

	c := findCandidate(t, Classify(iscsiConfig(), inv, refs), "pvc-live")
	if c.Class != ClassClaimed {
		t.Fatalf("class = %s, want claimed", c.Class)
	}
	if c.ClaimedBy != "database/data-postgres-0" {
		t.Errorf("claimedBy = %q", c.ClaimedBy)
	}
}

// A PV that names only the IQN — a statically written one, or a driver that
// changed its volume handle — still proves the zvol is live.
func TestClassify_IQNAloneProvesTheZvolIsReferenced(t *testing.T) {
	inv := zvolFixture("pvc-static", 7, 3, 11, 100)
	refs := []Reference{{
		PVName:  "legacy-block",
		PVPhase: "Bound",
		Tokens:  []string{"iqn.2005-10.org.freenas.ctl:csi-pvc-static-cluster"},
	}}

	c := findCandidate(t, Classify(iscsiConfig(), inv, refs), "pvc-static")
	if c.Class != ClassClaimed {
		t.Fatalf("class = %s (%s), want claimed", c.Class, c.Reason)
	}
}

// An open session is the only evidence that survives Kubernetes having no
// record of the volume at all, so it outranks every other signal.
func TestClassify_OpenSessionOutranksAnAbsentPersistentVolume(t *testing.T) {
	inv := zvolFixture("pvc-mounted", 7, 3, 11, 100)
	inv.Sessions = []Session{{
		Target: "iqn.2005-10.org.freenas.ctl:csi-pvc-mounted-cluster", Initiator: "iqn.1993-08.org.debian:node1",
	}}

	c := findCandidate(t, Classify(iscsiConfig(), inv, nil), "pvc-mounted")
	if c.Class != ClassAttached {
		t.Fatalf("class = %s, want attached", c.Class)
	}
	if !strings.Contains(c.Reason, "1 open iSCSI session") {
		t.Errorf("reason = %q", c.Reason)
	}
}

func TestClassify_UnreadableSessionListRefusesEveryZvol(t *testing.T) {
	inv := zvolFixture("pvc-dead", 7, 3, 11, 100)
	inv.SessionsKnown = false
	inv.SessionsError = "connection reset"

	c := findCandidate(t, Classify(iscsiConfig(), inv, nil), "pvc-dead")
	if c.Class != ClassOther {
		t.Fatalf("class = %s, want other — unknown liveness is not idle", c.Class)
	}
	if !strings.Contains(c.Reason, "connection reset") {
		t.Errorf("reason must carry the read failure, got %q", c.Reason)
	}
}

// The iSCSI objects are joined by numeric ID, so a target named for one volume
// can be mapped to an extent that exports another. Deleting it by name would
// take a live volume's export with it.
func TestClassify_TargetThatAlsoExportsAnotherZvolIsRefused(t *testing.T) {
	inv := zvolFixture("pvc-dead", 7, 3, 11, 100)
	inv.Datasets = append(inv.Datasets, Dataset{
		ID: iscsiParent + "/pvc-live", Name: "pvc-live", Type: "VOLUME", Used: 100,
	})
	inv.Extents = append(inv.Extents, Extent{ID: 8, Name: "pvc-live", Disk: "zvol/" + iscsiParent + "/pvc-live"})
	inv.Mappings = append(inv.Mappings, TargetExtent{ID: 12, TargetID: 3, ExtentID: 8})

	c := findCandidate(t, Classify(iscsiConfig(), inv, nil), "pvc-dead")
	if c.Class != ClassOther {
		t.Fatalf("class = %s, want other", c.Class)
	}
	if !strings.Contains(c.Reason, "also exports") {
		t.Errorf("reason = %q", c.Reason)
	}
}

// An extent's name is a label; its disk field is the statement of what it
// exports. A convention-matching name over a different zvol must not decide it.
func TestClassify_ExtentDiskDecidesWhichZvolIsExported(t *testing.T) {
	inv := Inventory{
		Datasets: []Dataset{
			{ID: iscsiParent + "/pvc-a", Name: "pvc-a", Type: "VOLUME"},
			{ID: iscsiParent + "/pvc-b", Name: "pvc-b", Type: "VOLUME"},
		},
		// The extent is named for pvc-a but exports pvc-b.
		Extents:       []Extent{{ID: 7, Name: "pvc-a", Disk: "zvol/" + iscsiParent + "/pvc-b"}},
		Targets:       []Target{{ID: 3, Name: "csi-pvc-a-cluster"}},
		Mappings:      []TargetExtent{{ID: 11, TargetID: 3, ExtentID: 7}},
		SessionsKnown: true,
	}
	refs := []Reference{{PVName: "pvc-b", PVPhase: "Bound", Tokens: []string{"pvc-b"}}}

	cands := Classify(iscsiConfig(), inv, refs)
	if got := findCandidate(t, cands, "pvc-b").Class; got != ClassClaimed {
		t.Errorf("pvc-b class = %s, want claimed", got)
	}
	a := findCandidate(t, cands, "pvc-a")
	if a.Class != ClassOrphaned {
		t.Fatalf("pvc-a class = %s (%s), want orphaned", a.Class, a.Reason)
	}
	// Nothing exports pvc-a, so its plan must not reach for the target or the
	// extent that merely carry its name.
	if got := planNames(a); !equalStrings(got, []string{"zvol/" + iscsiParent + "/pvc-a"}) {
		t.Errorf("pvc-a plan = %v, want the zvol alone", got)
	}
}

func TestClassify_ReleasedPersistentVolumeIsPartOfThePlan(t *testing.T) {
	inv := zvolFixture("pvc-released", 7, 3, 11, 100)
	refs := []Reference{{PVName: "pvc-released", PVPhase: "Released", Tokens: []string{"pvc-released"}}}

	c := findCandidate(t, Classify(iscsiConfig(), inv, refs), "pvc-released")
	if c.Class != ClassOrphaned {
		t.Fatalf("class = %s (%s), want orphaned", c.Class, c.Reason)
	}
	plan := planNames(c)
	if last := plan[len(plan)-1]; last != "pv/pvc-released" {
		t.Errorf("the PersistentVolume must be deleted last, plan = %v", plan)
	}
}

func TestClassify_AvailablePersistentVolumeIsNotReclaimable(t *testing.T) {
	inv := zvolFixture("pvc-available", 7, 3, 11, 100)
	refs := []Reference{{PVName: "pvc-available", PVPhase: "Available", Tokens: []string{"pvc-available"}}}

	c := findCandidate(t, Classify(iscsiConfig(), inv, refs), "pvc-available")
	if c.Class != ClassOther {
		t.Fatalf("class = %s, want other — an Available PV can still be bound", c.Class)
	}
}

func TestClassify_StrayExtentAndTargetAreReported(t *testing.T) {
	inv := Inventory{
		// The zvol is already gone; its extent and mapping are not.
		Extents:       []Extent{{ID: 7, Name: "pvc-gone", Disk: "zvol/" + iscsiParent + "/pvc-gone"}},
		Targets:       []Target{{ID: 3, Name: "csi-pvc-gone-cluster"}, {ID: 4, Name: "csi-pvc-lonely-cluster"}},
		Mappings:      []TargetExtent{{ID: 11, TargetID: 3, ExtentID: 7}},
		SessionsKnown: true,
	}
	cands := Classify(iscsiConfig(), inv, nil)

	stray := findCandidate(t, cands, "pvc-gone")
	if stray.Class != ClassOrphaned || stray.Kind != KindExtent {
		t.Fatalf("stray extent = %s/%s (%s)", stray.Kind, stray.Class, stray.Reason)
	}
	lonely := findCandidate(t, cands, "csi-pvc-lonely-cluster")
	if lonely.Class != ClassOrphaned || lonely.Kind != KindTarget {
		t.Fatalf("stray target = %s/%s (%s)", lonely.Kind, lonely.Class, lonely.Reason)
	}
}

func TestClassify_TargetOutsideTheDriverNamingConventionIsIgnored(t *testing.T) {
	inv := Inventory{
		Targets:       []Target{{ID: 9, Name: "someones-manual-target"}},
		SessionsKnown: true,
	}
	if cands := Classify(iscsiConfig(), inv, nil); len(cands) != 0 {
		t.Fatalf("reported %v, want nothing outside the driver's own naming", candidateNames(cands))
	}
}

func TestClassify_NFSDatasetAndItsExport(t *testing.T) {
	inv := Inventory{
		Datasets: []Dataset{{
			ID: "storage/k8s/vols/pvc-nfs", Name: "pvc-nfs", Type: "FILESYSTEM",
			Mountpoint: "/mnt/storage/k8s/vols/pvc-nfs", Used: 4096,
		}},
		Shares:        []NFSShare{{ID: 5, Path: "/mnt/storage/k8s/vols/pvc-nfs"}},
		SessionsKnown: true,
	}

	c := findCandidate(t, Classify(nfsConfig(), inv, nil), "pvc-nfs")
	if c.Class != ClassOrphaned || c.Kind != KindDataset {
		t.Fatalf("class/kind = %s/%s (%s)", c.Class, c.Kind, c.Reason)
	}
	want := []string{"share//mnt/storage/k8s/vols/pvc-nfs", "dataset/storage/k8s/vols/pvc-nfs"}
	if got := planNames(c); !equalStrings(got, want) {
		t.Errorf("plan = %v, want %v", got, want)
	}
}

func TestClassify_NFSSharePathProvesTheDatasetIsReferenced(t *testing.T) {
	inv := Inventory{
		Datasets: []Dataset{{
			ID: "storage/k8s/vols/pvc-nfs", Name: "pvc-nfs", Type: "FILESYSTEM",
			Mountpoint: "/mnt/storage/k8s/vols/pvc-nfs",
		}},
		Shares:        []NFSShare{{ID: 5, Path: "/mnt/storage/k8s/vols/pvc-nfs"}},
		SessionsKnown: true,
	}
	// Only the share path is asserted, as an in-tree NFS PV would.
	refs := []Reference{{PVName: "bulk", PVPhase: "Bound", Tokens: []string{"/mnt/storage/k8s/vols/pvc-nfs"}}}

	if c := findCandidate(t, Classify(nfsConfig(), inv, refs), "pvc-nfs"); c.Class != ClassClaimed {
		t.Fatalf("class = %s (%s), want claimed", c.Class, c.Reason)
	}
}

func TestResolveReferences_ClaimComesFromTheClaimNotTheClaimRef(t *testing.T) {
	// The Released PV still carries a claimRef to a PVC that no longer exists;
	// reading it would report the volume as live forever.
	released := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pvc-released"},
		Spec: corev1.PersistentVolumeSpec{
			ClaimRef: &corev1.ObjectReference{Namespace: "database", Name: "data-postgres-0"},
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       ProvisionerISCSI,
					VolumeHandle: "pvc-released",
					VolumeAttributes: map[string]string{
						"iqn": "iqn.2005-10.org.freenas.ctl:csi-pvc-released-cluster",
					},
				},
			},
		},
		Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeReleased},
	}
	kube := k8s.NewFake(released)

	refs, err := ResolveReferences(context.Background(), kube)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("got %d references, want 1", len(refs))
	}
	if refs[0].ClaimedBy != "" {
		t.Errorf("claimedBy = %q, want empty: no PersistentVolumeClaim exists", refs[0].ClaimedBy)
	}
	if !refs[0].Released() {
		t.Errorf("phase %q must count as released", refs[0].PVPhase)
	}
	if !refs[0].namesTarget("csi-pvc-released-cluster") {
		t.Errorf("the IQN attribute must name the target, tokens = %v", refs[0].Tokens)
	}
}

// The NAS address and the pool name appear in a PV's attributes, and matching
// on those would make every object on the box look live.
func TestReference_DoesNotMatchOnIncidentalAttributeValues(t *testing.T) {
	r := Reference{Tokens: []string{"192.168.1.205", "nfs", "storage"}}
	for _, identifier := range []string{"storage/k8s/vols/pvc-a", "pvc-a", "/mnt/storage/k8s/vols/pvc-a"} {
		if r.namesObject(identifier) {
			t.Errorf("%q must not be matched by an incidental attribute value", identifier)
		}
	}
}

func planNames(c Candidate) []string {
	out := make([]string, 0, len(c.Objects))
	for _, o := range c.Objects {
		out = append(out, o.String())
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
