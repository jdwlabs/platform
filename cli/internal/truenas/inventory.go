// Package truenas reads the objects the democratic-csi drivers leave on the
// NAS and decides which of them nothing references any more.
//
// Both TrueNAS storage classes use reclaimPolicy: Retain, so deleting a PVC
// deletes nothing on the NAS. One truenas-iscsi PVC leaves a zvol, an iSCSI
// extent, an iSCSI target and the target-to-extent mapping behind; one
// truenas-nfs PVC leaves a dataset and its NFS export.
//
// The classifier never trusts a NAS object's own name. Provisioned objects are
// named after the PVC UID they were created for, and that name survives the PV,
// the PVC and the workload — exactly like the Longhorn volume's recorded
// pvcName, it records history rather than liveness. Two things prove a zvol is
// still live, and both are read from the other side of the relationship:
//
//   - a PersistentVolume whose CSI volumeHandle, volumeAttributes, NFS share
//     path or iSCSI IQN names it, resolved by walking the cluster's PVs rather
//     than by matching the object's name against a convention, and
//   - an open iSCSI session on a target that exports it, which is the only
//     evidence that survives Kubernetes having no idea the volume exists.
//
// Only the first of those applies to both classes. The middleware keeps no
// client state for an NFS export, so for that class a PersistentVolume is the
// whole of the evidence and a dataset an outside client is reading looks
// exactly like an idle one. The classifier says so rather than implying a
// session rung it cannot supply, and refuses a dataset that an export above it
// still makes reachable.
//
// The iSCSI object graph adds a trap the NFS class does not have: the three
// objects are joined by numeric ID, not by name. An extent's `disk` field is
// the only statement of which zvol it actually exports, and a target reaches
// its zvol only through a targetextent mapping. A target whose name matches the
// convention for volume A can therefore be mapped to an extent that exports
// volume B, so every linkage here is resolved through IDs and a candidate whose
// target also exports something else is refused rather than deleted.
package truenas

import (
	"context"
	"fmt"
	"strings"
)

// Dataset is one ZFS dataset or zvol under a driver's dataset parent.
type Dataset struct {
	// ID is the full ZFS path, e.g. storage/k8s/iscsi/vols/pvc-<uid>.
	ID   string
	Name string
	// Type is FILESYSTEM for an NFS-backed dataset and VOLUME for a zvol.
	Type       string
	Mountpoint string
	Used       int64
}

// IsZvol reports whether the dataset is a block device rather than a filesystem.
func (d Dataset) IsZvol() bool { return strings.EqualFold(d.Type, "VOLUME") }

// Extent is an iSCSI extent. Disk is the authoritative statement of which zvol
// the extent exports; the extent's own name is only a label.
type Extent struct {
	ID   int
	Name string
	Disk string
	Path string
}

// DatasetID resolves the extent's backing store to a ZFS path, or "" when the
// extent is file-backed rather than zvol-backed.
func (e Extent) DatasetID() string {
	if rest, ok := strings.CutPrefix(e.Disk, "zvol/"); ok {
		return rest
	}
	return ""
}

// Target is an iSCSI target.
type Target struct {
	ID    int
	Name  string
	Alias string
}

// TargetExtent is the mapping row that joins a target to an extent. It is a
// first-class object because it is the third thing a deleted PVC leaks and
// because it is the only place the join is recorded.
type TargetExtent struct {
	ID       int
	TargetID int
	ExtentID int
}

// NFSShare is one NFS export.
type NFSShare struct {
	ID   int
	Path string
}

// Session is one open iSCSI connection. Its presence is proof that an initiator
// is using the target right now, whatever Kubernetes believes.
type Session struct {
	Target    string
	Initiator string
}

// Inventory is a single read of everything on the NAS a reclaim decision needs.
type Inventory struct {
	Datasets []Dataset
	Extents  []Extent
	Targets  []Target
	Mappings []TargetExtent
	Shares   []NFSShare
	Sessions []Session
	// SessionsKnown is false when the session list could not be read. Unknown
	// liveness is not idle: the classifier refuses every zvol in that case
	// rather than falling through to orphaned.
	SessionsKnown bool
	// SessionsError explains the refusal above.
	SessionsError string
}

// ReadInventory collects every NAS object relevant to one storage class.
func ReadInventory(ctx context.Context, c Caller, cfg DriverConfig) (Inventory, error) {
	var inv Inventory

	datasets, err := queryDatasets(ctx, c, cfg.DatasetParent)
	if err != nil {
		return Inventory{}, err
	}
	inv.Datasets = datasets

	if cfg.StorageClass == ClassNFS {
		shares, err := queryNFSShares(ctx, c)
		if err != nil {
			return Inventory{}, err
		}
		inv.Shares = shares
		inv.SessionsKnown = true
		return inv, nil
	}

	if inv.Extents, err = queryExtents(ctx, c); err != nil {
		return Inventory{}, err
	}
	if inv.Targets, err = queryTargets(ctx, c); err != nil {
		return Inventory{}, err
	}
	if inv.Mappings, err = queryTargetExtents(ctx, c); err != nil {
		return Inventory{}, err
	}
	// A failed session read degrades every zvol to "liveness unknown" instead
	// of failing the whole command, so the NFS half of a combined report still
	// arrives and the iSCSI half says why it has no verdict.
	sessions, err := querySessions(ctx, c)
	if err != nil {
		inv.SessionsError = err.Error()
	} else {
		inv.Sessions = sessions
		inv.SessionsKnown = true
	}
	return inv, nil
}

func queryDatasets(ctx context.Context, c Caller, parent string) ([]Dataset, error) {
	// "^" is the middleware's starts-with operator. The trailing slash keeps
	// the parent itself, and any sibling sharing its name prefix, out of the
	// result — the parent dataset is not a reclaim candidate.
	filters := []any{[]any{"id", "^", parent + "/"}}
	options := map[string]any{"extra": map[string]any{"retrieve_children": false}}

	var rows []struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Type       string `json:"type"`
		Mountpoint string `json:"mountpoint"`
		Used       *struct {
			Parsed any `json:"parsed"`
		} `json:"used"`
	}
	if err := c.Call(ctx, "pool.dataset.query", []any{filters, options}, &rows); err != nil {
		return nil, fmt.Errorf("list datasets under %s: %w", parent, err)
	}

	out := make([]Dataset, 0, len(rows))
	for _, r := range rows {
		if r.ID == "" {
			return nil, fmt.Errorf("a dataset under %s has no id", parent)
		}
		// A grandchild is not a volume this driver provisions, and deleting a
		// candidate recursively would take it with it, so it is excluded here
		// and the non-recursive delete keeps the exclusion enforceable.
		if strings.Contains(strings.TrimPrefix(r.ID, parent+"/"), "/") {
			continue
		}
		d := Dataset{ID: r.ID, Name: leafOf(r.ID), Type: r.Type, Mountpoint: r.Mountpoint}
		if r.Used != nil {
			size, err := parseSize(r.Used.Parsed)
			if err != nil {
				return nil, fmt.Errorf("dataset %s: %w", r.ID, err)
			}
			d.Used = size
		}
		out = append(out, d)
	}
	return out, nil
}

func queryExtents(ctx context.Context, c Caller) ([]Extent, error) {
	var rows []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
		Disk string `json:"disk"`
		Path string `json:"path"`
	}
	if err := c.Call(ctx, "iscsi.extent.query", nil, &rows); err != nil {
		return nil, fmt.Errorf("list iscsi extents: %w", err)
	}
	out := make([]Extent, 0, len(rows))
	for _, r := range rows {
		out = append(out, Extent{ID: r.ID, Name: r.Name, Disk: r.Disk, Path: r.Path})
	}
	return out, nil
}

func queryTargets(ctx context.Context, c Caller) ([]Target, error) {
	var rows []struct {
		ID    int    `json:"id"`
		Name  string `json:"name"`
		Alias string `json:"alias"`
	}
	if err := c.Call(ctx, "iscsi.target.query", nil, &rows); err != nil {
		return nil, fmt.Errorf("list iscsi targets: %w", err)
	}
	out := make([]Target, 0, len(rows))
	for _, r := range rows {
		out = append(out, Target{ID: r.ID, Name: r.Name, Alias: r.Alias})
	}
	return out, nil
}

func queryTargetExtents(ctx context.Context, c Caller) ([]TargetExtent, error) {
	var rows []struct {
		ID     int `json:"id"`
		Target int `json:"target"`
		Extent int `json:"extent"`
	}
	if err := c.Call(ctx, "iscsi.targetextent.query", nil, &rows); err != nil {
		return nil, fmt.Errorf("list iscsi target-extent mappings: %w", err)
	}
	out := make([]TargetExtent, 0, len(rows))
	for _, r := range rows {
		out = append(out, TargetExtent{ID: r.ID, TargetID: r.Target, ExtentID: r.Extent})
	}
	return out, nil
}

func queryNFSShares(ctx context.Context, c Caller) ([]NFSShare, error) {
	var rows []struct {
		ID   int    `json:"id"`
		Path string `json:"path"`
	}
	if err := c.Call(ctx, "sharing.nfs.query", nil, &rows); err != nil {
		return nil, fmt.Errorf("list nfs shares: %w", err)
	}
	out := make([]NFSShare, 0, len(rows))
	for _, r := range rows {
		out = append(out, NFSShare{ID: r.ID, Path: r.Path})
	}
	return out, nil
}

func querySessions(ctx context.Context, c Caller) ([]Session, error) {
	var rows []struct {
		Target    string `json:"target"`
		Initiator string `json:"initiator"`
	}
	if err := c.Call(ctx, "iscsi.global.sessions", nil, &rows); err != nil {
		return nil, fmt.Errorf("list iscsi sessions: %w", err)
	}
	out := make([]Session, 0, len(rows))
	for _, r := range rows {
		out = append(out, Session{Target: r.Target, Initiator: r.Initiator})
	}
	return out, nil
}

func leafOf(zfsPath string) string {
	if i := strings.LastIndex(zfsPath, "/"); i >= 0 {
		return zfsPath[i+1:]
	}
	return zfsPath
}
