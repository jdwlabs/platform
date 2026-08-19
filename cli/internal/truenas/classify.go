package truenas

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Class is the reclaim verdict for one candidate. The vocabulary is the same
// as the Longhorn backend's on purpose: an operator filtering `--class
// orphaned` should not have to learn a second set of words per storage tier.
type Class string

const (
	ClassOrphaned Class = "orphaned"
	ClassClaimed  Class = "claimed"
	ClassAttached Class = "attached"
	ClassOther    Class = "other"
)

// Verdicts lists every class this backend can assign, in report order.
func Verdicts() []string {
	return []string{string(ClassOrphaned), string(ClassClaimed), string(ClassAttached), string(ClassOther)}
}

// ObjectKind names one deletable thing.
type ObjectKind string

const (
	KindZvol    ObjectKind = "zvol"
	KindDataset ObjectKind = "dataset"
	KindTarget  ObjectKind = "target"
	KindExtent  ObjectKind = "extent"
	KindMapping ObjectKind = "targetextent"
	KindShare   ObjectKind = "share"
	KindPV      ObjectKind = "pv"
)

// Object is one entry in a candidate's delete plan. Name is the exact name the
// reclaim re-reads from the NAS immediately before deleting the row, so a plan
// built against a stale inventory cannot delete a different object that has
// since taken the same ID.
type Object struct {
	Kind ObjectKind
	Name string
	ID   string
}

func (o Object) String() string { return string(o.Kind) + "/" + o.Name }

// CandidateTokens are the identifiers a PersistentVolume could use to name one
// candidate. They are recorded on the candidate so the reclaim can re-run the
// reference check just before deleting — a candidate that no PV names has no PV
// to re-read by name, and that is the majority of orphans.
//
// Targets are separate from Objects because they match differently: an iSCSI
// IQN carries the target name after a colon, so a target is also matched by
// that suffix, while everything else is matched by equality.
type CandidateTokens struct {
	Objects []string
	Targets []string
}

// Candidate is one reclaimable unit: the backing dataset plus every object that
// exists only to export it, with the verdict and the ordered delete plan.
type Candidate struct {
	Name         string
	Kind         ObjectKind
	StorageClass string
	Class        Class
	// Reason explains a verdict that is not orphaned, and is empty otherwise.
	Reason    string
	Used      int64
	DatasetID string
	Target    string
	Extent    string
	Share     string
	Sessions  int
	ClaimedBy string
	PV        string
	PVPhase   string
	// Tokens is what a PersistentVolume would have to say to name this
	// candidate. It is the whole input to the reclaim's cluster-side re-check.
	Tokens CandidateTokens
	// Objects is the delete plan in execution order: target-extent mappings,
	// then extents, then targets, then NFS shares, then the dataset, then the
	// Released PersistentVolume. Reversing any pair of those leaves a dangling
	// row the middleware then refuses to clean up.
	Objects []Object
}

// Classify assigns a verdict to every object under one driver's dataset parent.
func Classify(cfg DriverConfig, inv Inventory, refs []Reference) []Candidate {
	g := newGraph(inv)
	out := make([]Candidate, 0, len(inv.Datasets))
	for _, d := range inv.Datasets {
		out = append(out, classifyDataset(cfg, inv, g, refs, d))
	}
	out = append(out, classifyStrays(cfg, inv, g, refs)...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

type graph struct {
	extentByID       map[int]Extent
	targetByID       map[int]Target
	extentsByDataset map[string][]Extent
	mappingsByExtent map[int][]TargetExtent
	mappingsByTarget map[int][]TargetExtent
}

func newGraph(inv Inventory) *graph {
	g := &graph{
		extentByID:       map[int]Extent{},
		targetByID:       map[int]Target{},
		extentsByDataset: map[string][]Extent{},
		mappingsByExtent: map[int][]TargetExtent{},
		mappingsByTarget: map[int][]TargetExtent{},
	}
	for _, e := range inv.Extents {
		g.extentByID[e.ID] = e
		if id := e.DatasetID(); id != "" {
			g.extentsByDataset[id] = append(g.extentsByDataset[id], e)
		}
	}
	for _, t := range inv.Targets {
		g.targetByID[t.ID] = t
	}
	for _, m := range inv.Mappings {
		g.mappingsByExtent[m.ExtentID] = append(g.mappingsByExtent[m.ExtentID], m)
		g.mappingsByTarget[m.TargetID] = append(g.mappingsByTarget[m.TargetID], m)
	}
	return g
}

func classifyDataset(cfg DriverConfig, inv Inventory, g *graph, refs []Reference, d Dataset) Candidate {
	c := Candidate{
		Name:         d.Name,
		Kind:         KindDataset,
		StorageClass: cfg.StorageClass,
		DatasetID:    d.ID,
		Used:         d.Used,
	}
	if d.IsZvol() {
		c.Kind = KindZvol
	}

	extents, mappings, targets := g.exportsOf(d.ID)
	shares := sharesOn(inv, d)
	c.Target = joinNames(targetNames(targets))
	c.Extent = joinNames(extentNames(extents))
	c.Share = joinNames(sharePaths(shares))

	sessions, sessionTarget := countSessions(inv, targets)
	c.Sessions = sessions

	c.Tokens = CandidateTokens{
		Objects: append([]string{d.Name, d.ID, d.Mountpoint}, sharePaths(shares)...),
		Targets: targetNames(targets),
	}
	matches := matchReferences(refs, c.Tokens)
	if len(matches) == 1 {
		c.PV = matches[0].PVName
		c.PVPhase = matches[0].PVPhase
		c.ClaimedBy = matches[0].ClaimedBy
	}

	c.Objects = deletePlan(mappings, extents, targets, shares, d, c.PV)
	c.Class, c.Reason = verdict(cfg, inv, g, c, d, matches, sessionTarget, targets)
	return c
}

// verdict applies the safety ladder. Order is the safety property: every rung
// that could mean "in use" is tested before the one that concludes "orphaned",
// so a state this code does not understand can never fall through to a delete.
//
// The ladder is not symmetric across the two classes, and pretending otherwise
// is the trap. iSCSI has a liveness rung the NAS itself can answer — an open
// session — while the middleware records no client state at all for an NFS
// export, so a dataset an outside initiator is reading right now is
// indistinguishable from an idle one. For that class the PersistentVolume side
// is the only proof of use, and the single NAS-side signal that does survive is
// an export above the dataset, which makes everything under it reachable by a
// client this cluster never created.
func verdict(cfg DriverConfig, inv Inventory, g *graph, c Candidate, d Dataset,
	matches []Reference, sessionTarget string, targets []Target) (Class, string) {

	if underSnapshotParent(cfg, c.DatasetID) {
		return ClassOther, "it is the driver's detached-snapshot parent " + cfg.SnapshotParent +
			" or lives inside it, and this command has no delete plan for a snapshot tree"
	}
	if cfg.StorageClass == ClassISCSI && !inv.SessionsKnown {
		return ClassOther, "iSCSI session list unreadable, so liveness is unknown: " + inv.SessionsError
	}
	if c.Sessions > 0 {
		return ClassAttached, fmt.Sprintf("%d open iSCSI session(s) on target %s", c.Sessions, sessionTarget)
	}
	if cfg.StorageClass == ClassNFS {
		if path := exportCovering(inv, d.Mountpoint); path != "" {
			return ClassOther, "NFS export " + path + " covers this dataset, so a client outside the cluster can reach it"
		}
	}
	if shared := sharedTargets(g, targets, c.DatasetID); len(shared) > 0 {
		return ClassOther, fmt.Sprintf("target %s also exports %s, so deleting it would break another volume",
			joinNames(shared[0:1]), joinNames(shared[1:]))
	}
	if len(matches) > 1 {
		names := make([]string, 0, len(matches))
		for _, m := range matches {
			names = append(names, m.PVName)
		}
		return ClassOther, "more than one PersistentVolume names this storage: " + joinNames(names)
	}
	if len(matches) == 1 {
		ref := matches[0]
		if ref.ClaimedBy != "" {
			return ClassClaimed, fmt.Sprintf("PersistentVolumeClaim %s points at PersistentVolume %s", ref.ClaimedBy, ref.PVName)
		}
		if ref.PVPhase == "Bound" {
			return ClassClaimed, "PersistentVolume " + ref.PVName + " is Bound"
		}
		if !ref.Released() {
			return ClassOther, fmt.Sprintf("PersistentVolume %s is %s, so it can still be bound", ref.PVName, ref.PVPhase)
		}
	}
	return ClassOrphaned, ""
}

// exportsOf resolves the iSCSI objects that reach one dataset. Every hop is by
// ID: the extent names the zvol in its disk field, and the target reaches the
// extent only through a mapping row.
func (g *graph) exportsOf(datasetID string) ([]Extent, []TargetExtent, []Target) {
	extents := g.extentsByDataset[datasetID]
	var mappings []TargetExtent
	var targets []Target
	seenTarget := map[int]bool{}
	for _, e := range extents {
		for _, m := range g.mappingsByExtent[e.ID] {
			mappings = append(mappings, m)
			if t, ok := g.targetByID[m.TargetID]; ok && !seenTarget[t.ID] {
				seenTarget[t.ID] = true
				targets = append(targets, t)
			}
		}
	}
	return extents, mappings, targets
}

// sharedTargets returns [target, otherDataset...] when a target reached from
// this dataset also exports a different one. Deleting such a target would break
// a volume the operator never named, so it is a refusal.
func sharedTargets(g *graph, targets []Target, datasetID string) []string {
	for _, t := range targets {
		var others []string
		for _, m := range g.mappingsByTarget[t.ID] {
			e, ok := g.extentByID[m.ExtentID]
			if !ok {
				others = append(others, "extent "+strconv.Itoa(m.ExtentID))
				continue
			}
			if id := e.DatasetID(); id != "" && id != datasetID {
				others = append(others, id)
			}
		}
		if len(others) > 0 {
			return append([]string{t.Name}, others...)
		}
	}
	return nil
}

// underSnapshotParent reports whether a candidate is the driver's detached-
// snapshot tree or something inside it. When that parent is nested under the
// volume parent its dataset arrives here as an ordinary child that no
// PersistentVolume names, which is the shape of an orphan; the snapshots it
// holds are not this command's to delete.
func underSnapshotParent(cfg DriverConfig, datasetID string) bool {
	if cfg.SnapshotParent == "" || datasetID == "" {
		return false
	}
	return datasetID == cfg.SnapshotParent || strings.HasPrefix(datasetID, cfg.SnapshotParent+"/")
}

// exportCovering returns the path of an NFS export that reaches the dataset
// from above. Such an export is not the dataset's own, so it is not in the
// delete plan and would survive the reclaim while the data under it disappeared.
func exportCovering(inv Inventory, mountpoint string) string {
	if mountpoint == "" {
		return ""
	}
	for _, s := range inv.Shares {
		if s.Path != mountpoint && strings.HasPrefix(mountpoint, s.Path+"/") {
			return s.Path
		}
	}
	return ""
}

func sharesOn(inv Inventory, d Dataset) []NFSShare {
	if d.Mountpoint == "" {
		return nil
	}
	var out []NFSShare
	for _, s := range inv.Shares {
		if s.Path == d.Mountpoint {
			out = append(out, s)
		}
	}
	return out
}

func countSessions(inv Inventory, targets []Target) (int, string) {
	var count int
	var named string
	for _, t := range targets {
		for _, s := range inv.Sessions {
			if s.Target == t.Name || strings.HasSuffix(s.Target, ":"+t.Name) {
				count++
				named = t.Name
			}
		}
	}
	return count, named
}

func matchReferences(refs []Reference, tokens CandidateTokens) []Reference {
	var out []Reference
	for _, r := range refs {
		if _, ok := r.namesCandidate(tokens); ok {
			out = append(out, r)
		}
	}
	return out
}

// deletePlan orders the objects so that no step leaves a row the middleware
// would then refuse to delete: the mapping joins target to extent, the extent
// holds the zvol open, and the PersistentVolume is the cluster's record of all
// of it.
func deletePlan(mappings []TargetExtent, extents []Extent, targets []Target,
	shares []NFSShare, d Dataset, pvName string) []Object {
	var plan []Object
	for _, m := range mappings {
		plan = append(plan, Object{Kind: KindMapping, Name: mappingName(m), ID: strconv.Itoa(m.ID)})
	}
	for _, e := range extents {
		plan = append(plan, Object{Kind: KindExtent, Name: e.Name, ID: strconv.Itoa(e.ID)})
	}
	for _, t := range targets {
		plan = append(plan, Object{Kind: KindTarget, Name: t.Name, ID: strconv.Itoa(t.ID)})
	}
	for _, s := range shares {
		plan = append(plan, Object{Kind: KindShare, Name: s.Path, ID: strconv.Itoa(s.ID)})
	}
	if d.ID != "" {
		kind := KindDataset
		if d.IsZvol() {
			kind = KindZvol
		}
		plan = append(plan, Object{Kind: kind, Name: d.ID, ID: d.ID})
	}
	if pvName != "" {
		plan = append(plan, Object{Kind: KindPV, Name: pvName, ID: pvName})
	}
	return plan
}

func mappingName(m TargetExtent) string {
	return fmt.Sprintf("target-%d-extent-%d", m.TargetID, m.ExtentID)
}

// classifyStrays reports the objects that outlived the dataset they served, or
// that were never joined to one. These are what a partially completed manual
// cleanup leaves behind, and they are invisible to a dataset-first scan.
func classifyStrays(cfg DriverConfig, inv Inventory, g *graph, refs []Reference) []Candidate {
	if cfg.StorageClass != ClassISCSI {
		return strayShares(cfg, inv, refs)
	}

	known := map[string]bool{}
	for _, d := range inv.Datasets {
		known[d.ID] = true
	}
	prefix := cfg.DatasetParent + "/"

	var out []Candidate
	claimedTargets := map[int]bool{}
	for _, e := range inv.Extents {
		id := e.DatasetID()
		if id == "" || !strings.HasPrefix(id, prefix) || known[id] {
			continue
		}
		mappings := g.mappingsByExtent[e.ID]
		var targets []Target
		for _, m := range mappings {
			// A target still mapped to something else is not this extent's to
			// delete, so it stays out of the plan and out of the refusal.
			if t, ok := g.targetByID[m.TargetID]; ok && len(g.mappingsByTarget[t.ID]) == 1 {
				targets = append(targets, t)
				claimedTargets[t.ID] = true
			}
		}
		c := Candidate{
			Name:         e.Name,
			Kind:         KindExtent,
			StorageClass: cfg.StorageClass,
			DatasetID:    id,
			Target:       joinNames(targetNames(targets)),
			Extent:       e.Name,
			Objects:      deletePlan(mappings, []Extent{e}, targets, nil, Dataset{}, ""),
			Tokens:       CandidateTokens{Objects: []string{e.Name}, Targets: targetNames(targets)},
		}
		c.Sessions, _ = countSessions(inv, targets)
		c.Class, c.Reason = strayVerdict(cfg, inv, refs, c)
		c.Reason = withDetail(c.Reason, "extent still exports "+id+", which no longer exists")
		out = append(out, c)
	}

	for _, t := range inv.Targets {
		if len(g.mappingsByTarget[t.ID]) > 0 || claimedTargets[t.ID] {
			continue
		}
		// DetectsStrayTargets is the single statement of when this scan can
		// run; the caller reports the skip, because a silently disabled check
		// reads as "nothing was left behind".
		if !cfg.DetectsStrayTargets() {
			continue
		}
		if !strings.HasPrefix(t.Name, cfg.TargetPrefix) || !strings.HasSuffix(t.Name, cfg.TargetSuffix) {
			continue
		}
		c := Candidate{
			Name:         t.Name,
			Kind:         KindTarget,
			StorageClass: cfg.StorageClass,
			Target:       t.Name,
			Objects:      []Object{{Kind: KindTarget, Name: t.Name, ID: strconv.Itoa(t.ID)}},
			Tokens:       CandidateTokens{Targets: []string{t.Name}},
		}
		c.Sessions, _ = countSessions(inv, []Target{t})
		c.Class, c.Reason = strayVerdict(cfg, inv, refs, c)
		c.Reason = withDetail(c.Reason, "target is mapped to no extent, so it exports nothing")
		out = append(out, c)
	}
	return out
}

func strayShares(cfg DriverConfig, inv Inventory, refs []Reference) []Candidate {
	mountRoot := "/mnt/" + cfg.DatasetParent + "/"
	known := map[string]bool{}
	for _, d := range inv.Datasets {
		if d.Mountpoint != "" {
			known[d.Mountpoint] = true
		}
	}
	var out []Candidate
	for _, s := range inv.Shares {
		if !strings.HasPrefix(s.Path, mountRoot) || known[s.Path] {
			continue
		}
		c := Candidate{
			Name:         leafOf(s.Path),
			Kind:         KindShare,
			StorageClass: cfg.StorageClass,
			Share:        s.Path,
			Objects:      []Object{{Kind: KindShare, Name: s.Path, ID: strconv.Itoa(s.ID)}},
			Tokens:       CandidateTokens{Objects: []string{s.Path}},
		}
		c.Class, c.Reason = strayVerdict(cfg, inv, refs, c)
		c.Reason = withDetail(c.Reason, "export has no dataset behind it")
		out = append(out, c)
	}
	return out
}

// strayVerdict applies the same ladder to an object with no dataset. It cannot
// consult a claim, because there is no volume handle to match, so a live
// session or any PersistentVolume naming the object is the only stop.
//
// The unknown-session refusal keys on the storage class rather than on whether
// a target resolved: an extent hanging off a target that is mapped to something
// else contributes no target here, and a resolved-target test would let exactly
// that case through with the session list unread.
func strayVerdict(cfg DriverConfig, inv Inventory, refs []Reference, c Candidate) (Class, string) {
	if cfg.StorageClass == ClassISCSI && !inv.SessionsKnown {
		return ClassOther, "iSCSI session list unreadable, so liveness is unknown: " + inv.SessionsError
	}
	if c.Sessions > 0 {
		return ClassAttached, fmt.Sprintf("%d open iSCSI session(s)", c.Sessions)
	}
	for _, r := range refs {
		if named, ok := r.namesCandidate(c.Tokens); ok {
			return ClassClaimed, "PersistentVolume " + r.PVName + " names " + named
		}
	}
	return ClassOrphaned, ""
}

// withDetail appends the finding that produced a stray candidate. An orphaned
// row has no refusal reason of its own, so the detail becomes the reason and
// the report still says why the object was listed.
func withDetail(reason, detail string) string {
	if reason == "" {
		return detail
	}
	return reason + "; " + detail
}

func targetNames(ts []Target) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.Name)
	}
	return out
}

func extentNames(es []Extent) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.Name)
	}
	return out
}

func sharePaths(ss []NFSShare) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, s.Path)
	}
	return out
}

// joinNames uses a space because a comma is the TOON field delimiter, and a
// cell containing one has to be quoted for every row that has more than one
// value in it.
func joinNames(names []string) string { return strings.Join(names, " ") }
