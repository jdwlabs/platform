package drain

import (
	"sort"
)

// Colocation is a workload with two or more replicas on one node despite an
// anti-affinity rule asking for them to be apart.
//
// Under a required rule this should be impossible and means the rule was added
// after the pods were placed. Under a preferred rule it is the expected cost of
// that choice: the scheduler co-locates when it cannot spread, and nothing ever
// moves the replica back. Either way it is invisible until someone looks, which
// is why a drain feasibility report carries it — relaxing a rule to make drains
// possible is only safe if the resulting concentration is observable.
type Colocation struct {
	Workload string
	Node     string
	Replicas int
	// Rule is "required" or "preferred", naming which anti-affinity the pods
	// carry so a reader knows whether this is a surprise or an accepted cost.
	Rule string
}

// Colocations reports every workload with replicas sharing a node against a
// hostname anti-affinity rule, worst concentration first.
func Colocations(c Cluster) []Colocation {
	type key struct{ workload, node string }
	counts := map[key]int{}
	rules := map[string]string{}

	for _, p := range c.Pods {
		if p.Class == ClassFinished || p.Class == ClassDaemonSet || p.Class == ClassMirror {
			continue
		}
		rule := antiAffinityRule(p)
		if rule == "" {
			continue
		}
		// A required rule wins the label when a workload carries both, because
		// that is the stronger promise being broken.
		if rules[p.Workload] != "required" {
			rules[p.Workload] = rule
		}
		counts[key{p.Workload, p.NodeName}]++
	}

	var out []Colocation
	for k, n := range counts {
		if n < 2 {
			continue
		}
		out = append(out, Colocation{Workload: k.workload, Node: k.node, Replicas: n, Rule: rules[k.workload]})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Replicas != out[j].Replicas {
			return out[i].Replicas > out[j].Replicas
		}
		if out[i].Workload != out[j].Workload {
			return out[i].Workload < out[j].Workload
		}
		return out[i].Node < out[j].Node
	})
	return out
}

// antiAffinityRule returns the strongest hostname anti-affinity a pod carries,
// or "" when it carries none.
func antiAffinityRule(p Pod) string {
	if p.Spec == nil || p.Spec.Affinity == nil || p.Spec.Affinity.PodAntiAffinity == nil {
		return ""
	}
	pa := p.Spec.Affinity.PodAntiAffinity
	for _, t := range pa.RequiredDuringSchedulingIgnoredDuringExecution {
		if t.TopologyKey == HostnameTopologyKey {
			return "required"
		}
	}
	for _, w := range pa.PreferredDuringSchedulingIgnoredDuringExecution {
		if w.PodAffinityTerm.TopologyKey == HostnameTopologyKey {
			return "preferred"
		}
	}
	return ""
}
