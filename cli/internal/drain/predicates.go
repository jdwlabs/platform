package drain

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"

	schedulingcorev1 "k8s.io/component-helpers/scheduling/corev1"
	"k8s.io/component-helpers/scheduling/corev1/nodeaffinity"
)

// admits reports why node n would refuse pod p, or "" when it would accept it.
// Only the non-resource predicates are evaluated here: capacity is checked
// separately because a capacity refusal depends on what else has been packed,
// while these refusals do not.
func admits(p Pod, n Node, residents []Pod) string {
	node := asNode(n)

	if taint, untolerated := schedulingcorev1.FindMatchingUntoleratedTaint(
		klog.Background(), node.Spec.Taints, tolerations(p), schedulable, true); untolerated {
		return fmt.Sprintf("untolerated taint %s", taintRef(taint))
	}

	required := nodeaffinity.GetRequiredNodeAffinity(asPod(p))
	if ok, err := required.Match(node); err != nil || !ok {
		return "node selector/affinity excludes it"
	}

	for _, sel := range p.VolumeNodeSelectors {
		ns, err := nodeaffinity.NewNodeSelector(sel)
		if err != nil {
			return "volume node affinity is unparseable"
		}
		if !ns.Match(node) {
			return "volume is node-local elsewhere"
		}
	}

	if why := podAffinityAdmits(p, residents); why != "" {
		return why
	}
	return ""
}

// schedulable selects the taint effects that keep a new pod off a node.
// NoExecute also evicts pods already running, but for a placement decision it
// bars entry exactly as NoSchedule does; PreferNoSchedule never bars entry.
func schedulable(t *corev1.Taint) bool {
	return t.Effect == corev1.TaintEffectNoSchedule || t.Effect == corev1.TaintEffectNoExecute
}

func taintRef(t corev1.Taint) string {
	if t.Value == "" {
		return fmt.Sprintf("%s:%s", t.Key, t.Effect)
	}
	return fmt.Sprintf("%s=%s:%s", t.Key, t.Value, t.Effect)
}

// podAffinityAdmits evaluates the hard inter-pod rules against the pods already
// resident on the candidate node.
func podAffinityAdmits(p Pod, residents []Pod) string {
	aff := affinity(p)
	if aff == nil {
		return ""
	}

	if aff.PodAntiAffinity != nil {
		for _, term := range aff.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution {
			if term.TopologyKey != HostnameTopologyKey {
				continue
			}
			if match := firstMatchingResident(p, term, residents); match != "" {
				return "anti-affinity: " + match + " is already there"
			}
		}
	}

	if aff.PodAffinity != nil {
		for _, term := range aff.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution {
			if term.TopologyKey != HostnameTopologyKey {
				continue
			}
			if firstMatchingResident(p, term, residents) == "" {
				return "pod affinity: nothing it must sit beside is there"
			}
		}
	}
	return ""
}

// firstMatchingResident returns the reference of the first resident pod that
// satisfies term, or "" when none does.
//
// A term's namespaceSelector is not evaluated — resolving it needs namespace
// labels this simulation does not load. Any pod using one is reported through
// Cluster.UnmodelledConstraints rather than silently mis-evaluated.
func firstMatchingResident(p Pod, term corev1.PodAffinityTerm, residents []Pod) string {
	sel, err := selectorFor(term.LabelSelector)
	if err != nil || sel == nil {
		return ""
	}
	scope := map[string]bool{p.Namespace: true}
	for _, ns := range term.Namespaces {
		scope[ns] = true
	}
	for _, r := range residents {
		if !scope[r.Namespace] {
			continue
		}
		if sel.Matches(labelSet(r.Labels)) {
			return r.Ref()
		}
	}
	return ""
}

// UsesUnmodelledConstraint reports the placement rules in spec that this
// simulation cannot evaluate, so the caller can name them instead of returning
// a verdict that quietly assumed they were satisfied.
func UsesUnmodelledConstraint(spec *corev1.PodSpec) []string {
	if spec == nil {
		return nil
	}
	var found []string
	for _, c := range spec.TopologySpreadConstraints {
		if c.WhenUnsatisfiable == corev1.DoNotSchedule {
			found = append(found, "topologySpreadConstraints/DoNotSchedule on "+c.TopologyKey)
		}
	}
	if spec.Affinity == nil {
		return found
	}
	terms := func(kind string, ts []corev1.PodAffinityTerm) {
		for _, t := range ts {
			if t.TopologyKey != HostnameTopologyKey {
				found = append(found, kind+" on topologyKey "+t.TopologyKey)
			}
			if t.NamespaceSelector != nil {
				found = append(found, kind+" with a namespaceSelector")
			}
		}
	}
	if spec.Affinity.PodAntiAffinity != nil {
		terms("required podAntiAffinity", spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution)
	}
	if spec.Affinity.PodAffinity != nil {
		terms("required podAffinity", spec.Affinity.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution)
	}
	return found
}

func affinity(p Pod) *corev1.Affinity {
	if p.Spec == nil {
		return nil
	}
	return p.Spec.Affinity
}

func tolerations(p Pod) []corev1.Toleration {
	if p.Spec == nil {
		return nil
	}
	return p.Spec.Tolerations
}

// asNode and asPod rebuild the minimal API objects the upstream scheduling
// helpers expect. Reusing those helpers rather than reimplementing selector,
// affinity and toleration matching is the whole reason the reduced types carry
// labels, taints and the raw spec.
func asNode(n Node) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: n.Name, Labels: n.Labels},
		Spec:       corev1.NodeSpec{Taints: n.Taints},
	}
}

func asPod(p Pod) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: p.Namespace, Name: p.Name, Labels: p.Labels},
	}
	if p.Spec != nil {
		pod.Spec = *p.Spec
	}
	return pod
}

func selectorFor(ls *metav1.LabelSelector) (labels.Selector, error) {
	if ls == nil {
		return labels.Nothing(), nil
	}
	return metav1.LabelSelectorAsSelector(ls)
}

func labelSet(m map[string]string) labels.Set { return labels.Set(m) }

// FormatMemory renders bytes in the unit this cluster is discussed in. Mebibytes
// up to a gibibyte, then gibibytes with one decimal — an operator comparing a
// 192Mi request against a node's free memory should not have to convert.
func FormatMemory(b int64) string {
	const mi = 1024 * 1024
	if b < 0 {
		return "-"
	}
	if b < 1024*mi {
		return fmt.Sprintf("%dMi", b/mi)
	}
	return fmt.Sprintf("%.1fGi", float64(b)/float64(1024*mi))
}
