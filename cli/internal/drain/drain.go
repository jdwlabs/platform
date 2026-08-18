// Package drain answers one question about live cluster state: could each node
// be drained right now?
//
// A drain is only as good as the cluster's ability to re-place what it evicts.
// When nothing can host an evicted pod the eviction API keeps refusing it, the
// drain waits forever, and the operator discovers mid-upgrade that the cluster
// has been un-maintainable for weeks. Nothing in Kubernetes reports that state
// ahead of time — a node looks healthy right up to the moment it has to be
// emptied.
//
// So this package simulates the re-placement. For each node it takes the pods a
// drain would evict, removes that node from the set of targets, and packs them
// onto what is left under the scheduler predicates that actually bind here:
// node readiness and cordon state, taints and tolerations, nodeSelector and
// required node affinity, required pod affinity and anti-affinity, PersistentVolume
// node affinity, and memory/CPU/pod-count capacity.
//
// # What a verdict means
//
// The packing is greedy (largest request first, onto the target with the most
// free memory), so a "fits" verdict is a witness — an assignment that would
// work — and is therefore sound. A "blocked" verdict on capacity alone is not a
// proof, because some other packing order might have succeeded; it is a strong
// signal that wants a human's eye. A "blocked" verdict where some pod has no
// candidate node at all before capacity is even consulted IS a proof: no
// ordering can conjure a node that the pod's own constraints exclude. The two
// are reported distinctly, as reason class `hard` versus `capacity`.
//
// # What is deliberately not modelled
//
// Preferred (soft) affinity and topology spread constraints are ignored: they
// never make a placement impossible, only less even, so including them would
// turn feasible verdicts into false alarms. Hard topology spread constraints
// with whenUnsatisfiable=DoNotSchedule are likewise not modelled — see
// UnmodelledConstraints, which reports any workload that uses one so the gap is
// visible rather than silent. Scheduler plugins, priority preemption, and
// extended resources are out of scope; this is a feasibility check, not a
// scheduler.
package drain

import (
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
)

// HostnameTopologyKey is the only topology key this simulation resolves.
// Anti-affinity at a coarser topology (zone, region) cannot be evaluated on a
// single-site cluster where every node reports the same value, and treating an
// unresolvable key as satisfied would understate the constraint.
const HostnameTopologyKey = "kubernetes.io/hostname"

// PodClass is why a pod does or does not have to be re-placed by a drain.
type PodClass string

const (
	// ClassMovable is a pod a drain evicts and the cluster must re-place.
	ClassMovable PodClass = "movable"
	// ClassDaemonSet is skipped by `kubectl drain --ignore-daemonsets`; its
	// replacement is created by the DaemonSet controller once the node returns.
	ClassDaemonSet PodClass = "daemonset"
	// ClassMirror is a static pod owned by the kubelet. The eviction API cannot
	// touch it and it comes back with the node.
	ClassMirror PodClass = "mirror"
	// ClassFinished is a Succeeded or Failed pod: it holds no resources and is
	// deleted rather than re-placed.
	ClassFinished PodClass = "finished"
	// ClassTerminating already has a deletion timestamp, so the drain waits for
	// it rather than re-placing it.
	ClassTerminating PodClass = "terminating"
)

// Node is a scheduling target, reduced to what a placement decision needs.
type Node struct {
	Name         string
	Labels       map[string]string
	Taints       []corev1.Taint
	ControlPlane bool
	// Cordoned and NotReady are reported separately because they mean different
	// things to an operator: one is a deliberate act, the other is a fault.
	Cordoned bool
	NotReady bool

	AllocatableMemory int64 // bytes
	AllocatableCPU    int64 // millicores
	AllocatablePods   int64
}

// Schedulable reports whether the scheduler would consider this node a target
// at all. A cordoned or unready node is not capacity — counting it as capacity
// is how a drain plan that "fits" strands pods in practice.
func (n Node) Schedulable() bool { return !n.Cordoned && !n.NotReady }

// Pod is one scheduled pod plus the drain-relevant facts derived from it.
type Pod struct {
	Namespace string
	Name      string
	NodeName  string
	Labels    map[string]string

	Memory int64 // bytes
	CPU    int64 // millicores
	// UsedMemory is observed memory, populated only when metrics-server was
	// read. UsageKnown separates "measured zero" from "never measured".
	UsedMemory int64
	UsageKnown bool

	Class PodClass
	// Unmanaged marks a pod with no owner: `kubectl drain` refuses it without
	// --force, and with --force it is deleted and never recreated. Either way a
	// drain cannot proceed cleanly, so it is a blocker in its own right.
	Unmanaged bool
	// Workload is the owning controller's name, used to group blockers into a
	// message about a workload rather than about one replica.
	Workload string

	// Spec is retained because the fit predicates evaluate the pod's own
	// affinity, tolerations and node selector against each candidate node.
	Spec *corev1.PodSpec
	// VolumeNodeSelectors are the required node affinities of every
	// PersistentVolume the pod mounts. A node-local volume pins its pod outright.
	VolumeNodeSelectors []*corev1.NodeSelector

	// PDB names the PodDisruptionBudget governing this pod, and
	// DisruptionsAllowed is that budget's live allowance. A budget at zero does
	// not make a drain infeasible — it makes it wait — but it is what an
	// operator sees when a drain appears to hang, so it is reported.
	PDB                string
	DisruptionsAllowed int32
	HasPDB             bool
}

// Ref is the namespace/name an operator greps for.
func (p Pod) Ref() string { return p.Namespace + "/" + p.Name }

// Cluster is the live state a simulation runs against.
type Cluster struct {
	Nodes []Node
	Pods  []Pod
	// UnmodelledConstraints names workloads using a hard placement constraint
	// this simulation does not evaluate. A check that quietly ignores a
	// constraint reports feasibility it has not established, so the gap is
	// carried into the output instead.
	UnmodelledConstraints []string
}

// Verdict is the outcome for one node.
type Verdict string

const (
	// VerdictDrainable means a concrete assignment of every evicted pod to a
	// surviving node exists and was found.
	VerdictDrainable Verdict = "drainable"
	// VerdictBlocked means at least one evicted pod could not be placed.
	VerdictBlocked Verdict = "blocked"
	// VerdictEmpty means the drain evicts nothing, so it trivially completes.
	VerdictEmpty Verdict = "empty"
	// VerdictSkipped means the node is already cordoned or not ready, so
	// "could it be drained" is not the question to ask of it.
	VerdictSkipped Verdict = "skipped"
)

// ReasonClass distinguishes a proof from a signal.
type ReasonClass string

const (
	// ReasonHard means no surviving node satisfies the pod's own constraints,
	// independently of how much room any of them has. No packing order changes
	// this, so it is a proof of infeasibility.
	ReasonHard ReasonClass = "hard"
	// ReasonCapacity means candidate nodes exist but none had room left once
	// the rest of this node's evacuation was packed. Order-dependent, so it is
	// a strong signal rather than a proof.
	ReasonCapacity ReasonClass = "capacity"
	// ReasonUnmanaged means the pod has no controller to recreate it.
	ReasonUnmanaged ReasonClass = "unmanaged"
)

// Placement is one evicted pod and the node the simulation found for it.
type Placement struct {
	Pod    Pod
	Target string
}

// Blocker is one evicted pod the simulation could not place.
type Blocker struct {
	Pod    Pod
	Class  ReasonClass
	Reason string
}

// NodeResult is the verdict for one node plus the evidence behind it.
type NodeResult struct {
	Node    Node
	Verdict Verdict

	Movable       int
	MovableMemory int64
	// RequestedMemory and FreeMemory describe the node as it stands, before any
	// eviction. They are the arithmetic behind the verdict rather than part of
	// it, and they are what an operator compares across nodes.
	RequestedMemory int64
	FreeMemory      int64
	// PinnedMemory is what stays on the node through a drain — DaemonSets,
	// static pods. It is not part of the feasibility question but it is the
	// number that explains why a small node has so little to give.
	PinnedMemory int64
	// TightestAfter is the free memory left on the most-committed surviving
	// node once the evacuation is packed, or -1 when the drain is blocked. It
	// is how close the cluster sits to its next refusal.
	TightestAfter int64

	Placements []Placement
	Blockers   []Blocker
	// UsedMemory is the node's observed memory across the pods metrics-server
	// reported, and UnderDeclared is how much observed usage exceeds declared
	// requests across those same pods. A large positive gap means the
	// feasibility arithmetic above is running on numbers the workloads do not
	// honour.
	UsedMemory    int64
	UnderDeclared int64
	UsageKnown    bool
	// Pods is every pod the node carries, classified. It is the inventory
	// behind the movable and pinned figures, reported so "why is this node
	// blocked" and "what is actually on it" are one question.
	Pods []Pod
	// PDBAtZero counts evicted pods whose disruption budget currently allows no
	// disruption. The drain stalls on these until the workload recovers.
	PDBAtZero int
}

// Drainable is the yes/no an operator is asking for.
func (r NodeResult) Drainable() bool {
	return r.Verdict == VerdictDrainable || r.Verdict == VerdictEmpty
}

// Result is the whole-cluster answer.
type Result struct {
	Nodes                 []NodeResult
	UnmodelledConstraints []string
	// Colocations records anti-affinity that live placement is not honouring.
	// It rides along with the feasibility verdict because the two trade against
	// each other: relaxing a rule is what makes a blocked drain possible, and
	// this is the cost of having done so.
	Colocations []Colocation
}

// Blocked returns the nodes that cannot be drained, in report order.
func (r Result) Blocked() []NodeResult {
	var out []NodeResult
	for _, n := range r.Nodes {
		if n.Verdict == VerdictBlocked {
			out = append(out, n)
		}
	}
	return out
}

// Simulate evaluates every node in the cluster and returns one result per node,
// ordered worst verdict first so the answer leads with the problem.
func Simulate(c Cluster) Result {
	res := Result{UnmodelledConstraints: c.UnmodelledConstraints, Colocations: Colocations(c)}
	for _, n := range c.Nodes {
		res.Nodes = append(res.Nodes, SimulateNode(c, n.Name))
	}
	sort.SliceStable(res.Nodes, func(i, j int) bool {
		ri, rj := res.Nodes[i], res.Nodes[j]
		if verdictRank(ri.Verdict) != verdictRank(rj.Verdict) {
			return verdictRank(ri.Verdict) < verdictRank(rj.Verdict)
		}
		return ri.Node.Name < rj.Node.Name
	})
	return res
}

func verdictRank(v Verdict) int {
	switch v {
	case VerdictBlocked:
		return 0
	case VerdictDrainable:
		return 1
	case VerdictEmpty:
		return 2
	default:
		return 3
	}
}

// SimulateNode drains one node on paper and reports whether the eviction could
// complete.
func SimulateNode(c Cluster, nodeName string) NodeResult {
	var node Node
	for _, n := range c.Nodes {
		if n.Name == nodeName {
			node = n
		}
	}
	result := NodeResult{Node: node, TightestAfter: -1}

	if !node.Schedulable() {
		result.Verdict = VerdictSkipped
		return result
	}

	targets := newTargetSet(c, nodeName)

	var movable []Pod
	for _, p := range c.Pods {
		if p.NodeName != nodeName || p.Class == ClassFinished {
			continue
		}
		result.RequestedMemory += p.Memory
		result.Pods = append(result.Pods, p)
		if p.UsageKnown {
			result.UsageKnown = true
			result.UsedMemory += p.UsedMemory
			if gap := p.UsedMemory - p.Memory; gap > 0 {
				result.UnderDeclared += gap
			}
		}
		switch p.Class {
		case ClassMovable:
			movable = append(movable, p)
		case ClassDaemonSet, ClassMirror:
			result.PinnedMemory += p.Memory
		}
	}
	result.FreeMemory = node.AllocatableMemory - result.RequestedMemory

	result.Movable = len(movable)
	for _, p := range movable {
		result.MovableMemory += p.Memory
		if p.HasPDB && p.DisruptionsAllowed == 0 {
			result.PDBAtZero++
		}
	}

	if len(movable) == 0 {
		result.Verdict = VerdictEmpty
		return result
	}

	// Largest first: a first-fit-decreasing order finds a witness far more often
	// than arrival order does, and the whole point is to find one if it exists.
	sort.SliceStable(movable, func(i, j int) bool {
		if movable[i].Memory != movable[j].Memory {
			return movable[i].Memory > movable[j].Memory
		}
		return movable[i].Ref() < movable[j].Ref()
	})

	for _, p := range movable {
		if p.Unmanaged {
			result.Blockers = append(result.Blockers, Blocker{
				Pod:    p,
				Class:  ReasonUnmanaged,
				Reason: "no owning controller — drain refuses it without --force and nothing recreates it",
			})
			continue
		}
		target, blocker := targets.place(p)
		if blocker != nil {
			result.Blockers = append(result.Blockers, *blocker)
			continue
		}
		result.Placements = append(result.Placements, Placement{Pod: p, Target: target})
	}

	if len(result.Blockers) > 0 {
		result.Verdict = VerdictBlocked
		return result
	}
	result.Verdict = VerdictDrainable
	result.TightestAfter = targets.tightestFreeMemory()
	return result
}

// targetSet is the mutable packing state: the surviving nodes, their remaining
// room, and who lives on them once earlier pods in this evacuation are placed.
type targetSet struct {
	nodes     []Node
	freeMem   map[string]int64
	freeCPU   map[string]int64
	freePods  map[string]int64
	residents map[string][]Pod
}

func newTargetSet(c Cluster, drained string) *targetSet {
	ts := &targetSet{
		freeMem:   map[string]int64{},
		freeCPU:   map[string]int64{},
		freePods:  map[string]int64{},
		residents: map[string][]Pod{},
	}
	for _, n := range c.Nodes {
		if n.Name == drained || !n.Schedulable() {
			continue
		}
		ts.nodes = append(ts.nodes, n)
		ts.freeMem[n.Name] = n.AllocatableMemory
		ts.freeCPU[n.Name] = n.AllocatableCPU
		ts.freePods[n.Name] = n.AllocatablePods
	}
	for _, p := range c.Pods {
		if p.NodeName == drained || p.Class == ClassFinished {
			continue
		}
		if _, ok := ts.freeMem[p.NodeName]; !ok {
			continue
		}
		ts.freeMem[p.NodeName] -= p.Memory
		ts.freeCPU[p.NodeName] -= p.CPU
		ts.freePods[p.NodeName]--
		ts.residents[p.NodeName] = append(ts.residents[p.NodeName], p)
	}
	sort.Slice(ts.nodes, func(i, j int) bool { return ts.nodes[i].Name < ts.nodes[j].Name })
	return ts
}

// place assigns p to the surviving node with the most free memory that accepts
// it, and returns a Blocker when none does.
func (ts *targetSet) place(p Pod) (string, *Blocker) {
	var eligible []Node
	rejections := map[string]string{}
	for _, n := range ts.nodes {
		if why := admits(p, n, ts.residents[n.Name]); why != "" {
			rejections[n.Name] = why
			continue
		}
		eligible = append(eligible, n)
	}

	if len(eligible) == 0 {
		return "", &Blocker{
			Pod:    p,
			Class:  ReasonHard,
			Reason: summariseRejections(ts.nodes, rejections),
		}
	}

	best := ""
	var bestFree int64
	var closest int64 = -1
	short := map[string]int{}
	for _, n := range eligible {
		if lack := ts.lacks(n.Name, p); lack != "" {
			short[lack]++
			if ts.freeMem[n.Name] > closest {
				closest = ts.freeMem[n.Name]
			}
			continue
		}
		if best == "" || ts.freeMem[n.Name] > bestFree {
			best, bestFree = n.Name, ts.freeMem[n.Name]
		}
	}

	if best == "" {
		return "", &Blocker{
			Pod:   p,
			Class: ReasonCapacity,
			Reason: fmt.Sprintf("needs %s; %d eligible node(s) all short on %s — roomiest has %s free",
				FormatMemory(p.Memory), len(eligible), joinShortfalls(short), FormatMemory(maxInt64(closest, 0))),
		}
	}

	ts.freeMem[best] -= p.Memory
	ts.freeCPU[best] -= p.CPU
	ts.freePods[best]--
	placed := p
	placed.NodeName = best
	ts.residents[best] = append(ts.residents[best], placed)
	return best, nil
}

// lacks names the resource that stops n taking p, or "" when n has room.
// Naming it matters: "no room" sends an operator hunting for memory on a node
// that ran out of CPU or hit the kubelet pod cap.
func (ts *targetSet) lacks(node string, p Pod) string {
	switch {
	case ts.freeMem[node] < p.Memory:
		return "memory"
	case ts.freeCPU[node] < p.CPU:
		return "cpu"
	case ts.freePods[node] < 1:
		return "the pod cap"
	default:
		return ""
	}
}

// joinShortfalls renders the shortfall tally in a stable order, so two runs
// against the same cluster produce the same line.
func joinShortfalls(short map[string]int) string {
	order := []string{"memory", "cpu", "the pod cap"}
	var parts []string
	for _, k := range order {
		if n, ok := short[k]; ok {
			parts = append(parts, fmt.Sprintf("%s (%d)", k, n))
		}
	}
	if len(parts) == 0 {
		return "capacity"
	}
	return strings.Join(parts, " / ")
}

func (ts *targetSet) tightestFreeMemory() int64 {
	var tightest int64 = -1
	for _, n := range ts.nodes {
		if tightest < 0 || ts.freeMem[n.Name] < tightest {
			tightest = ts.freeMem[n.Name]
		}
	}
	return tightest
}

// summariseRejections turns per-node rejection reasons into one line, grouping
// identical reasons so a five-node cluster does not print five near-identical
// clauses.
func summariseRejections(nodes []Node, rejections map[string]string) string {
	if len(rejections) == 0 {
		return "no surviving node is schedulable"
	}
	byReason := map[string][]string{}
	var order []string
	for _, n := range nodes {
		why, ok := rejections[n.Name]
		if !ok {
			continue
		}
		if _, seen := byReason[why]; !seen {
			order = append(order, why)
		}
		byReason[why] = append(byReason[why], n.Name)
	}
	parts := make([]string, 0, len(order))
	for _, why := range order {
		parts = append(parts, fmt.Sprintf("%s (%s)", why, strings.Join(byReason[why], " ")))
	}
	return "no eligible node: " + strings.Join(parts, "; ")
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// MatchPDBs attaches disruption budget state to each pod. It is exported so the
// loader and tests share one definition of which budget governs a pod.
func MatchPDBs(pods []Pod, pdbs []policyv1.PodDisruptionBudget) {
	for i := range pods {
		for _, pdb := range pdbs {
			if pdb.Namespace != pods[i].Namespace {
				continue
			}
			sel, err := selectorFor(pdb.Spec.Selector)
			if err != nil || sel == nil || !sel.Matches(labelSet(pods[i].Labels)) {
				continue
			}
			pods[i].PDB = pdb.Namespace + "/" + pdb.Name
			pods[i].HasPDB = true
			pods[i].DisruptionsAllowed = pdb.Status.DisruptionsAllowed
			break
		}
	}
}
