package truenas

import (
	"context"
	"strings"
	"testing"
)

func readInventory(t *testing.T, nas *FakeMiddleware, cfg DriverConfig) (Inventory, error) {
	t.Helper()
	return ReadInventory(context.Background(), nas, cfg)
}

// The "unknown liveness is not idle" guard only ever fired when the session
// call failed. A call that succeeds and returns rows the decoder cannot read
// leaves every Session.Target empty, so countSessions returns 0 for every zvol
// while SessionsKnown stays true — the rung disappears and the verdicts that
// depend on it do not. That is the shape a renamed field in a future middleware
// release produces.
func TestReadInventory_SessionRowsWithNoReadableTargetAreUnknownLiveness(t *testing.T) {
	nas := &FakeMiddleware{
		Datasets: []Dataset{{
			ID: iscsiParent + "/pvc-attached", Name: "pvc-attached", Type: "VOLUME",
		}},
		Extents:  []Extent{{ID: 7, Name: "pvc-attached", Disk: "zvol/" + iscsiParent + "/pvc-attached"}},
		Targets:  []Target{{ID: 3, Name: "csi-pvc-attached-cluster"}},
		Mappings: []TargetExtent{{ID: 11, TargetID: 3, ExtentID: 7}},
		Sessions: []Session{{Initiator: "iqn.1993-08.org.debian:01:node"}},
	}

	inv, err := readInventory(t, nas, iscsiConfig())
	if err != nil {
		t.Fatalf("the session list degrades the run, it does not fail it: %v", err)
	}
	if inv.SessionsKnown {
		t.Fatalf("session rows with no readable target must not count as a readable session list")
	}
	if !strings.Contains(inv.SessionsError, "no readable target") {
		t.Errorf("the reason must say what was unreadable, got %q", inv.SessionsError)
	}

	c := findCandidate(t, Classify(iscsiConfig(), inv, nil), "pvc-attached")
	if c.Class != ClassOther {
		t.Errorf("class = %s (%s), want other — the rung is gone, so no zvol is idle", c.Class, c.Reason)
	}
}

func TestReadInventory_EmptySessionListIsStillAReadableOne(t *testing.T) {
	inv, err := readInventory(t, &FakeMiddleware{}, iscsiConfig())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !inv.SessionsKnown {
		t.Errorf("no sessions is a readable answer, not an unreadable one: %q", inv.SessionsError)
	}
}

// An extent's disk field is the only statement of which zvol it exports. If it
// cannot be read, the zvol→extent→target join collapses and takes sharedTargets
// and the session rung with it, so every attached zvol would classify orphaned
// with nothing in the output saying why.
func TestReadInventory_ExtentWithNoReadableDiskFailsTheRead(t *testing.T) {
	nas := &FakeMiddleware{
		Extents: []Extent{{ID: 7, Name: "pvc-orphan", Type: "DISK"}},
	}

	_, err := readInventory(t, nas, iscsiConfig())
	if err == nil {
		t.Fatalf("an extent whose backing store cannot be read must fail the read")
	}
	if !strings.Contains(err.Error(), "pvc-orphan") {
		t.Errorf("the error must name the row it could not read: %v", err)
	}
}

// A file-backed extent is the one row that legitimately carries no disk, and it
// says so in its type.
func TestReadInventory_FileBackedExtentIsNotADecoderFailure(t *testing.T) {
	nas := &FakeMiddleware{
		Extents: []Extent{{ID: 7, Name: "iso-library", Type: "FILE", Path: "/mnt/storage/iso.img"}},
	}

	inv, err := readInventory(t, nas, iscsiConfig())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(inv.Extents) != 1 {
		t.Errorf("the file extent should have been kept, got %v", inv.Extents)
	}
}
