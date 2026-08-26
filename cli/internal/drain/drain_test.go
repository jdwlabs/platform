package drain

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const mi = 1024 * 1024

func node(name string, allocMi int64, opts ...func(*Node)) Node {
	n := Node{
		Name:              name,
		Labels:            map[string]string{HostnameTopologyKey: name},
		AllocatableMemory: allocMi * mi,
		AllocatableCPU:    4000,
		AllocatablePods:   110,
	}
	for _, o := range opts {
		o(&n)
	}
	return n
}

func pod(ns, name, onNode string, memMi int64, opts ...func(*Pod)) Pod {
	p := Pod{
		Namespace: ns,
		Name:      name,
		NodeName:  onNode,
		Memory:    memMi * mi,
		Class:     ClassMovable,
		Workload:  ns + "/" + name,
		Spec:      &corev1.PodSpec{},
		Labels:    map[string]string{},
	}
	for _, o := range opts {
		o(&p)
	}
	return p
}

func daemon(p *Pod) { p.Class = ClassDaemonSet }

// antiAffinity gives a pod the required hostname anti-affinity the three HA
// workloads in this cluster carry, keyed on an app label.
func antiAffinity(app string) func(*Pod) {
	return func(p *Pod) {
		p.Labels["app"] = app
		p.Spec.Affinity = &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
				LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": app}},
				TopologyKey:   HostnameTopologyKey,
			}},
		}}
	}
}

func softAntiAffinity(app string) func(*Pod) {
	return func(p *Pod) {
		p.Labels["app"] = app
		p.Spec.Affinity = &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{
			PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
				Weight: 100,
				PodAffinityTerm: corev1.PodAffinityTerm{
					LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": app}},
					TopologyKey:   HostnameTopologyKey,
				},
			}},
		}}
	}
}

// podAffinityOn gives a pod the required hostname pod affinity that pins it
// beside another workload.
func podAffinityOn(app string) func(*Pod) {
	return func(p *Pod) {
		p.Spec.Affinity = &corev1.Affinity{PodAffinity: &corev1.PodAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
				LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": app}},
				TopologyKey:   HostnameTopologyKey,
			}},
		}}
	}
}

func labelled(app string) func(*Pod) {
	return func(p *Pod) { p.Labels["app"] = app }
}

func verdictOf(t *testing.T, r Result, name string) NodeResult {
	t.Helper()
	for _, n := range r.Nodes {
		if n.Node.Name == name {
			return n
		}
	}
	t.Fatalf("no result for node %s", name)
	return NodeResult{}
}

func TestSimulateNode_PlacesEvictedPodsOnRoomElsewhere(t *testing.T) {
	c := Cluster{
		Nodes: []Node{node("a", 1000), node("b", 1000)},
		Pods:  []Pod{pod("ns", "one", "a", 200), pod("ns", "two", "a", 200)},
	}
	got := verdictOf(t, Simulate(c), "a")
	if got.Verdict != VerdictDrainable {
		t.Fatalf("verdict = %s, want drainable (blockers: %v)", got.Verdict, got.Blockers)
	}
	if len(got.Placements) != 2 {
		t.Fatalf("placements = %d, want 2", len(got.Placements))
	}
	for _, p := range got.Placements {
		if p.Target != "b" {
			t.Errorf("%s placed on %s, want b", p.Pod.Ref(), p.Target)
		}
	}
}

func TestSimulateNode_DaemonSetAndMirrorPodsAreNeverReplaced(t *testing.T) {
	c := Cluster{
		Nodes: []Node{node("a", 1000), node("b", 100)},
		Pods: []Pod{
			pod("ns", "ds", "a", 500, daemon),
			pod("ns", "static", "a", 500, func(p *Pod) { p.Class = ClassMirror }),
		},
	}
	got := verdictOf(t, Simulate(c), "a")
	if got.Verdict != VerdictEmpty {
		t.Fatalf("verdict = %s, want empty — a drain evicts neither class", got.Verdict)
	}
	if want := int64(1000 * mi); got.PinnedMemory != want {
		t.Errorf("pinned = %d, want %d", got.PinnedMemory, want)
	}
}

func TestSimulateNode_FinishedPodsHoldNoCapacity(t *testing.T) {
	c := Cluster{
		Nodes: []Node{node("a", 1000), node("b", 300)},
		Pods: []Pod{
			pod("ns", "job", "b", 900, func(p *Pod) { p.Class = ClassFinished }),
			pod("ns", "live", "a", 250),
		},
	}
	got := verdictOf(t, Simulate(c), "a")
	if got.Verdict != VerdictDrainable {
		t.Fatalf("verdict = %s, want drainable — a Succeeded pod on b is not occupancy", got.Verdict)
	}
}

// The cluster's actual failure: three replicas, required hostname
// anti-affinity, and only two nodes big enough to hold them.
func TestSimulateNode_RequiredAntiAffinityStrandsTheThirdReplica(t *testing.T) {
	c := Cluster{
		Nodes: []Node{node("big-1", 8000), node("big-2", 8000), node("small", 300)},
		Pods: []Pod{
			pod("db", "pg-0", "big-1", 192, antiAffinity("pg")),
			pod("db", "pg-1", "big-2", 192, antiAffinity("pg")),
			pod("db", "pg-2", "small", 192, antiAffinity("pg")),
		},
	}
	got := verdictOf(t, Simulate(c), "small")
	if got.Verdict != VerdictBlocked {
		t.Fatalf("verdict = %s, want blocked", got.Verdict)
	}
	if len(got.Blockers) != 1 {
		t.Fatalf("blockers = %d, want 1", len(got.Blockers))
	}
	b := got.Blockers[0]
	if b.Class != ReasonHard {
		t.Errorf("class = %s, want hard — both large nodes are excluded by the rule itself, "+
			"which no packing order can change", b.Class)
	}
	if !strings.Contains(b.Reason, "anti-affinity") {
		t.Errorf("reason = %q, want it to name anti-affinity", b.Reason)
	}
}

// The same cluster with the rule relaxed: this is the change the ticket weighs.
func TestSimulateNode_PreferredAntiAffinityLetsTheReplicaMove(t *testing.T) {
	c := Cluster{
		Nodes: []Node{node("big-1", 8000), node("big-2", 8000), node("small", 300)},
		Pods: []Pod{
			pod("db", "pg-0", "big-1", 192, softAntiAffinity("pg")),
			pod("db", "pg-1", "big-2", 192, softAntiAffinity("pg")),
			pod("db", "pg-2", "small", 192, softAntiAffinity("pg")),
		},
	}
	got := verdictOf(t, Simulate(c), "small")
	if got.Verdict != VerdictDrainable {
		t.Fatalf("verdict = %s, want drainable (blockers: %v)", got.Verdict, got.Blockers)
	}
}

// Required pod affinity is the mirror of the anti-affinity case: the pod must
// land beside something, and a node that does not hold it is no candidate.
func TestSimulateNode_RequiredPodAffinityNeedsItsNeighbourOnTheTargetNode(t *testing.T) {
	nodes := []Node{node("a", 1000), node("b", 8000)}
	sidecar := pod("ns", "app-0", "a", 64, podAffinityOn("cache"))

	blocked := verdictOf(t, Simulate(Cluster{Nodes: nodes, Pods: []Pod{sidecar}}), "a")
	if blocked.Verdict != VerdictBlocked {
		t.Fatalf("verdict = %s, want blocked — nothing it must sit beside exists", blocked.Verdict)
	}
	if !strings.Contains(blocked.Blockers[0].Reason, "nothing it must sit beside") {
		t.Errorf("reason = %q, want it to name the missing neighbour", blocked.Blockers[0].Reason)
	}

	withCache := Cluster{Nodes: nodes, Pods: []Pod{sidecar, pod("ns", "cache-0", "b", 64, labelled("cache"))}}
	if got := verdictOf(t, Simulate(withCache), "a"); got.Verdict != VerdictDrainable {
		t.Fatalf("verdict = %s, want drainable once the neighbour is on b (blockers: %v)",
			got.Verdict, got.Blockers)
	}
}

// The scheduler checks anti-affinity in both directions, so the simulation must
// too: a pod declaring nothing is still refused by a node whose occupant
// forbids it, and a one-way check would call that placement a witness.
func TestSimulateNode_ResidentAntiAffinityRefusesAPodCarryingNoRule(t *testing.T) {
	c := Cluster{
		Nodes: []Node{node("a", 1000), node("b", 8000)},
		Pods: []Pod{
			pod("ns", "picky-0", "b", 64, antiAffinity("web")),
			pod("ns", "web-0", "a", 64, labelled("web")),
		},
	}
	got := verdictOf(t, Simulate(c), "a")
	if got.Verdict != VerdictBlocked {
		t.Fatalf("verdict = %s, want blocked — b's occupant excludes web-0", got.Verdict)
	}
	if got.Blockers[0].Class != ReasonHard {
		t.Errorf("class = %s, want hard — picky-0 was there before the evacuation", got.Blockers[0].Class)
	}
	if !strings.Contains(got.Blockers[0].Reason, "its own rule excludes this pod") {
		t.Errorf("reason = %q, want it to say whose rule refused the pod", got.Blockers[0].Reason)
	}
}

// `hard` claims no packing order could have helped. A node excluded only by a
// pod this same evacuation put there does not support that claim.
func TestSimulateNode_ARefusalThisEvacuationCausedIsNotCalledAProof(t *testing.T) {
	c := Cluster{
		Nodes: []Node{node("a", 1000), node("b", 8000)},
		Pods: []Pod{
			pod("ns", "web-0", "a", 192, antiAffinity("web")),
			pod("ns", "web-1", "a", 64, antiAffinity("web")),
		},
	}
	got := verdictOf(t, Simulate(c), "a")
	if got.Verdict != VerdictBlocked {
		t.Fatalf("verdict = %s, want blocked — one node cannot hold two mutually exclusive pods", got.Verdict)
	}
	if got.Blockers[0].Class != ReasonCapacity {
		t.Errorf("class = %s, want capacity — b refused web-1 only because web-0 had just "+
			"been placed there, which is order-dependent", got.Blockers[0].Class)
	}
}

func TestSimulateNode_CapacityShortfallNamesTheResourceThatRanOut(t *testing.T) {
	cases := []struct {
		name     string
		nodeB    Node
		podCPU   int64
		wantWord string
	}{
		{"memory", node("b", 100), 0, "memory"},
		{"cpu", node("b", 8000, func(n *Node) { n.AllocatableCPU = 100 }), 500, "cpu"},
		{"pod cap", node("b", 8000, func(n *Node) { n.AllocatablePods = 0 }), 0, "the pod cap"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Cluster{
				Nodes: []Node{node("a", 1000), tc.nodeB},
				Pods:  []Pod{pod("ns", "one", "a", 200, func(p *Pod) { p.CPU = tc.podCPU })},
			}
			got := verdictOf(t, Simulate(c), "a")
			if got.Verdict != VerdictBlocked {
				t.Fatalf("verdict = %s, want blocked", got.Verdict)
			}
			if got.Blockers[0].Class != ReasonCapacity {
				t.Fatalf("class = %s, want capacity", got.Blockers[0].Class)
			}
			if !strings.Contains(got.Blockers[0].Reason, tc.wantWord) {
				t.Errorf("reason = %q, want it to name %q — sending an operator to hunt for "+
					"memory on a node that ran out of something else is the failure this avoids",
					got.Blockers[0].Reason, tc.wantWord)
			}
		})
	}
}

func TestSimulateNode_UnmanagedPodBlocksTheDrainEvenWithRoom(t *testing.T) {
	c := Cluster{
		Nodes: []Node{node("a", 1000), node("b", 8000)},
		Pods:  []Pod{pod("ns", "bare", "a", 64, func(p *Pod) { p.Unmanaged = true })},
	}
	got := verdictOf(t, Simulate(c), "a")
	if got.Verdict != VerdictBlocked {
		t.Fatalf("verdict = %s, want blocked", got.Verdict)
	}
	if got.Blockers[0].Class != ReasonUnmanaged {
		t.Errorf("class = %s, want unmanaged", got.Blockers[0].Class)
	}
}

func TestSimulateNode_CordonedAndUnreadyNodesAreNotCapacity(t *testing.T) {
	c := Cluster{
		Nodes: []Node{
			node("a", 1000),
			node("cordoned", 8000, func(n *Node) { n.Cordoned = true }),
			node("broken", 8000, func(n *Node) { n.NotReady = true }),
		},
		Pods: []Pod{pod("ns", "one", "a", 200)},
	}
	r := Simulate(c)
	if got := verdictOf(t, r, "a"); got.Verdict != VerdictBlocked {
		t.Errorf("verdict = %s, want blocked — neither other node can accept work", got.Verdict)
	}
	if got := verdictOf(t, r, "cordoned"); got.Verdict != VerdictSkipped {
		t.Errorf("cordoned verdict = %s, want skipped", got.Verdict)
	}
}

func TestSimulateNode_UntoleratedTaintExcludesANode(t *testing.T) {
	tainted := node("cp", 8000, func(n *Node) {
		n.Taints = []corev1.Taint{{
			Key: "node-role.kubernetes.io/control-plane", Effect: corev1.TaintEffectNoSchedule,
		}}
	})
	c := Cluster{
		Nodes: []Node{node("a", 1000), tainted},
		Pods:  []Pod{pod("ns", "one", "a", 200)},
	}
	got := verdictOf(t, Simulate(c), "a")
	if got.Verdict != VerdictBlocked {
		t.Fatalf("verdict = %s, want blocked", got.Verdict)
	}
	if !strings.Contains(got.Blockers[0].Reason, "untolerated taint") {
		t.Errorf("reason = %q, want it to name the taint", got.Blockers[0].Reason)
	}
}

func TestSimulateNode_NodeLocalVolumePinsItsPod(t *testing.T) {
	pinned := func(p *Pod) {
		p.VolumeNodeSelectors = []*corev1.NodeSelector{{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{{
				MatchExpressions: []corev1.NodeSelectorRequirement{{
					Key:      HostnameTopologyKey,
					Operator: corev1.NodeSelectorOpIn,
					Values:   []string{"a"},
				}},
			}},
		}}
	}
	c := Cluster{
		Nodes: []Node{node("a", 1000), node("b", 8000)},
		Pods:  []Pod{pod("ns", "stateful", "a", 64, pinned)},
	}
	got := verdictOf(t, Simulate(c), "a")
	if got.Verdict != VerdictBlocked {
		t.Fatalf("verdict = %s, want blocked", got.Verdict)
	}
	if got.Blockers[0].Class != ReasonHard {
		t.Errorf("class = %s, want hard", got.Blockers[0].Class)
	}
}

// Packing is largest-first so that a witness is found when one exists: arrival
// order would wedge the 300Mi pod behind two 200Mi ones on a 500Mi node.
func TestSimulateNode_PacksLargestFirstSoAFeasibleAssignmentIsFound(t *testing.T) {
	c := Cluster{
		Nodes: []Node{node("a", 1000), node("b", 500), node("c", 500)},
		Pods: []Pod{
			pod("ns", "small-1", "a", 200),
			pod("ns", "small-2", "a", 200),
			pod("ns", "large", "a", 300),
		},
	}
	got := verdictOf(t, Simulate(c), "a")
	if got.Verdict != VerdictDrainable {
		t.Fatalf("verdict = %s, want drainable (blockers: %v)", got.Verdict, got.Blockers)
	}
}

func TestSimulateNode_ReportsCommitmentAndDisruptionBudgetState(t *testing.T) {
	c := Cluster{
		Nodes: []Node{node("a", 1000), node("b", 8000)},
		Pods: []Pod{
			pod("ns", "ds", "a", 400, daemon),
			pod("ns", "app", "a", 200, func(p *Pod) { p.HasPDB = true; p.PDB = "ns/app" }),
		},
	}
	got := verdictOf(t, Simulate(c), "a")
	if want := int64(600 * mi); got.RequestedMemory != want {
		t.Errorf("requested = %d, want %d", got.RequestedMemory, want)
	}
	if want := int64(400 * mi); got.FreeMemory != want {
		t.Errorf("free = %d, want %d", got.FreeMemory, want)
	}
	if got.PDBAtZero != 1 {
		t.Errorf("pdbAtZero = %d, want 1 — a budget allowing no disruption is why a "+
			"feasible drain still hangs", got.PDBAtZero)
	}
}

func TestSimulate_OrdersBlockedNodesFirst(t *testing.T) {
	c := Cluster{
		Nodes: []Node{node("aaa-fine", 1000), node("zzz-stuck", 1000)},
		Pods: []Pod{
			pod("ns", "one", "aaa-fine", 100),
			pod("ns", "bare", "zzz-stuck", 100, func(p *Pod) { p.Unmanaged = true }),
		},
	}
	r := Simulate(c)
	if r.Nodes[0].Node.Name != "zzz-stuck" {
		t.Errorf("first node = %s, want zzz-stuck — the answer should lead with the problem",
			r.Nodes[0].Node.Name)
	}
}

func TestColocations_ReportsReplicasSharingANodeAndWhichRuleTheyBreak(t *testing.T) {
	c := Cluster{
		Nodes: []Node{node("a", 8000), node("b", 8000)},
		Pods: []Pod{
			pod("db", "pg-0", "a", 192, softAntiAffinity("pg"), sameWorkload("db/pg")),
			pod("db", "pg-1", "a", 192, softAntiAffinity("pg"), sameWorkload("db/pg")),
			pod("db", "pg-2", "b", 192, softAntiAffinity("pg"), sameWorkload("db/pg")),
		},
	}
	got := Colocations(c)
	if len(got) != 1 {
		t.Fatalf("colocations = %d, want 1: %+v", len(got), got)
	}
	if got[0].Node != "a" || got[0].Replicas != 2 || got[0].Rule != "preferred" {
		t.Errorf("got %+v, want 2 replicas of db/pg on a under a preferred rule", got[0])
	}
}

func TestColocations_IgnoresWorkloadsWithNoAntiAffinity(t *testing.T) {
	c := Cluster{
		Nodes: []Node{node("a", 8000)},
		Pods: []Pod{
			pod("ns", "web-0", "a", 64, sameWorkload("ns/web")),
			pod("ns", "web-1", "a", 64, sameWorkload("ns/web")),
		},
	}
	if got := Colocations(c); len(got) != 0 {
		t.Errorf("colocations = %+v, want none — co-location is only notable against a rule", got)
	}
}

func sameWorkload(name string) func(*Pod) {
	return func(p *Pod) { p.Workload = name }
}

func TestUsesUnmodelledConstraint_NamesWhatTheSimulationCannotEvaluate(t *testing.T) {
	spec := &corev1.PodSpec{
		TopologySpreadConstraints: []corev1.TopologySpreadConstraint{{
			TopologyKey: "topology.kubernetes.io/zone", WhenUnsatisfiable: corev1.DoNotSchedule,
		}},
		Affinity: &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
				TopologyKey: "topology.kubernetes.io/zone",
			}},
		}},
	}
	got := UsesUnmodelledConstraint(spec)
	if len(got) != 2 {
		t.Fatalf("got %v, want both the spread constraint and the zone anti-affinity named", got)
	}
}

func TestUsesUnmodelledConstraint_StaysQuietOnWhatItDoesEvaluate(t *testing.T) {
	spec := &corev1.PodSpec{Affinity: &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
			TopologyKey: HostnameTopologyKey,
		}},
	}}}
	if got := UsesUnmodelledConstraint(spec); len(got) != 0 {
		t.Errorf("got %v, want nothing — a hostname rule is evaluated, not skipped", got)
	}
}

func TestUsesUnmodelledConstraint_NamesASelectorItCannotParse(t *testing.T) {
	spec := &corev1.PodSpec{Affinity: &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
			TopologyKey: HostnameTopologyKey,
			LabelSelector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{
				Key: "app", Operator: metav1.LabelSelectorOpIn,
			}}},
		}},
	}}}
	got := UsesUnmodelledConstraint(spec)
	if len(got) != 1 || !strings.Contains(got[0], "does not parse") {
		t.Fatalf("got %v, want the unreadable selector named — an anti-affinity term that "+
			"cannot be parsed matches nothing, which makes the verdict quietly optimistic", got)
	}
}

// An over-committed node has less than nothing free, and that figure has to be
// distinguishable from the "-" the report prints for what nobody measured.
func TestFormatMemory(t *testing.T) {
	cases := map[int64]string{
		0:          "0Mi",
		192 * mi:   "192Mi",
		2363 * mi:  "2.3Gi",
		-192 * mi:  "-192Mi",
		-1536 * mi: "-1.5Gi",
		1023 * mi:  "1023Mi",
		1024 * mi:  "1.0Gi",
	}
	for in, want := range cases {
		if got := FormatMemory(in); got != want {
			t.Errorf("FormatMemory(%d) = %s, want %s", in, got, want)
		}
	}
}

func TestFormatCPU(t *testing.T) {
	cases := map[int64]string{
		0:    "0m",
		150:  "150m",
		1500: "1500m",
		-250: "-250m",
	}
	for in, want := range cases {
		if got := FormatCPU(in); got != want {
			t.Errorf("FormatCPU(%d) = %s, want %s", in, got, want)
		}
	}
}
