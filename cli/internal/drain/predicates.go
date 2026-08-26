package drain

import (
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"

	schedulingcorev1 "k8s.io/component-helpers/scheduling/corev1"
	"k8s.io/component-helpers/scheduling/corev1/nodeaffinity"
)

// admits reports why node n would refuse pod p, or "" when it would accept it.
// Only the non-resource predicates are evaluated here: capacity is checked
// separately because a capacity refusal depends on what else has been packed.
//
// The second return marks a refusal that rests on a pod this same evacuation
// placed on n. Such a refusal is undone by a different packing order, so it
// cannot support a `hard` verdict; placed carries the references of those pods.
func admits(p Pod, n Node, residents []Pod, placed map[string]bool) (string, bool) {
	node := asNode(n)

	if taint, untolerated := schedulingcorev1.FindMatchingUntoleratedTaint(
		klog.Background(), node.Spec.Taints, tolerations(p), schedulable, true); untolerated {
		return fmt.Sprintf("untolerated taint %s", taintRef(taint)), false
	}

	required := nodeaffinity.GetRequiredNodeAffinity(asPod(p))
	ok, err := required.Match(node)
	switch {
	case err != nil:
		// Refusing keeps the verdict off a rule nobody evaluated; which rule it
		// was is named through Cluster.UnmodelledConstraints.
		return "node selector/affinity is unparseable", false
	case !ok:
		return "node selector/affinity excludes it", false
	}

	for _, sel := range p.VolumeNodeSelectors {
		ns, err := nodeaffinity.NewNodeSelector(sel)
		if err != nil {
			return "volume node affinity is unparseable", false
		}
		if !ns.Match(node) {
			return "volume is node-local elsewhere", false
		}
	}

	return podAffinityAdmits(p, residents, placed)
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
//
// Both directions are checked, because the scheduler checks both: the incoming
// pod's anti-affinity against the residents, and every resident's anti-affinity
// against the incoming pod. A pod carrying no rule of its own is still refused
// by a node whose occupant forbids it, and a check that only looked one way
// would call such a placement a witness.
func podAffinityAdmits(p Pod, residents []Pod, placed map[string]bool) (string, bool) {
	if aff := affinity(p); aff != nil {
		if aff.PodAntiAffinity != nil {
			for _, term := range aff.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution {
				if term.TopologyKey != HostnameTopologyKey {
					continue
				}
				if match := firstMatchingResident(p, term, residents); match != "" {
					return "anti-affinity: " + match + " is already there", placed[match]
				}
			}
		}

		if aff.PodAffinity != nil {
			for _, term := range aff.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution {
				if term.TopologyKey != HostnameTopologyKey {
					continue
				}
				if firstMatchingResident(p, term, residents) == "" {
					// Order-dependent the other way round: the pod it must sit
					// beside may be one this evacuation has not placed yet.
					return "pod affinity: nothing it must sit beside is there", true
				}
			}
		}
	}

	for _, r := range residents {
		if why := residentRefuses(r, p); why != "" {
			return why, placed[r.Ref()]
		}
	}
	return "", false
}

// residentRefuses reports whether r's own required anti-affinity bars p from
// the node r sits on.
func residentRefuses(r, p Pod) string {
	aff := affinity(r)
	if aff == nil || aff.PodAntiAffinity == nil {
		return ""
	}
	for _, term := range aff.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution {
		if term.TopologyKey != HostnameTopologyKey {
			continue
		}
		if matchesTerm(r, term, p) {
			return "anti-affinity: " + r.Ref() + " is already there and its own rule excludes this pod"
		}
	}
	return ""
}

// firstMatchingResident returns the reference of the first resident pod that
// satisfies term, or "" when none does.
//
// Residents are held in arrival order — everything already on the node, then
// whatever this evacuation added — so where both would match, the pre-existing
// pod is the one named, and the refusal is correctly read as order-independent.
func firstMatchingResident(p Pod, term corev1.PodAffinityTerm, residents []Pod) string {
	for _, r := range residents {
		if matchesTerm(p, term, r) {
			return r.Ref()
		}
	}
	return ""
}

// matchesTerm reports whether candidate satisfies term as owner wrote it. Owner
// and candidate are separate arguments because a term's namespace scope
// defaults to the namespace of the pod carrying it, which is the resident's
// namespace when the term is read in reverse.
//
// A term's namespaceSelector is not evaluated — resolving it needs namespace
// labels this simulation does not load. Any pod using one is reported through
// Cluster.UnmodelledConstraints rather than silently mis-evaluated.
func matchesTerm(owner Pod, term corev1.PodAffinityTerm, candidate Pod) bool {
	sel, err := selectorFor(term.LabelSelector)
	if err != nil || sel == nil {
		return false
	}
	if candidate.Namespace != owner.Namespace && !slices.Contains(term.Namespaces, candidate.Namespace) {
		return false
	}
	return sel.Matches(labelSet(candidate.Labels))
}

// UsesUnmodelledConstraint reports the placement rules in spec that this
// simulation cannot evaluate — whether because it does not model them or
// because they do not parse — so the caller can name them instead of returning
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
	if na := spec.Affinity.NodeAffinity; na != nil && na.RequiredDuringSchedulingIgnoredDuringExecution != nil {
		if _, err := nodeaffinity.NewNodeSelector(na.RequiredDuringSchedulingIgnoredDuringExecution); err != nil {
			found = append(found, "required nodeAffinity that does not parse")
		}
	}
	terms := func(kind string, ts []corev1.PodAffinityTerm) {
		for _, t := range ts {
			if t.TopologyKey != HostnameTopologyKey {
				found = append(found, kind+" on topologyKey "+t.TopologyKey)
			}
			if t.NamespaceSelector != nil {
				found = append(found, kind+" with a namespaceSelector")
			}
			// An unparseable selector is a rule that exists and cannot be read,
			// which is the same gap as one this simulation declines to model:
			// left unsaid, it makes the verdict quietly more optimistic.
			if _, err := selectorFor(t.LabelSelector); err != nil {
				found = append(found, kind+" with a labelSelector that does not parse")
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
//
// Negatives are signed rather than blanked. A node whose requests exceed its
// allocatable has less than nothing free, and that is precisely the state this
// work is about; rendering it as the "-" the report uses for a figure nobody
// measured would hide an over-committed node among the unmeasured ones.
func FormatMemory(b int64) string {
	const mi = 1024 * 1024
	sign := ""
	if b < 0 {
		sign, b = "-", -b
	}
	if b < 1024*mi {
		return fmt.Sprintf("%s%dMi", sign, b/mi)
	}
	return fmt.Sprintf("%s%.1fGi", sign, float64(b)/float64(1024*mi))
}

// FormatCPU renders millicores the way this cluster's manifests already
// declare CPU (requests/limits are written as "250m"), so an observed figure
// can be compared against a spec without converting units in your head.
//
// Negatives are signed rather than blanked, for the same reason FormatMemory
// signs them: an over-committed node is exactly the state this figure exists
// to surface, and "-" already means "unmeasured" elsewhere in this report.
func FormatCPU(m int64) string {
	sign := ""
	if m < 0 {
		sign, m = "-", -m
	}
	return fmt.Sprintf("%s%dm", sign, m)
}
