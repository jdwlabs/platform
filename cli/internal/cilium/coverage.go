// Package cilium answers the one question the cilium-agent's own metrics
// cannot: which pods the policy engine holds no endpoint for.
//
// Cilium only ever sees a pod whose sandbox it created. A pod that already
// existed when the agent started never received a `cilium-cni` ADD, so it has
// no CiliumEndpoint, no identity, and every NetworkPolicy selecting it
// resolves against nothing. The agent exports counts of the endpoints it
// manages; it cannot export a count of the pods it never learned about,
// because those pods are outside its world. The gap is therefore only
// measurable by joining the two sources from outside — the pod list from the
// API server, the endpoint list from Cilium — which is what this package does.
package cilium

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// EndpointGVR is the CiliumEndpoint custom resource the agent creates per
// managed pod.
var EndpointGVR = schema.GroupVersionResource{Group: "cilium.io", Version: "v2", Resource: "ciliumendpoints"}

// Pod is one pod that policy is expected to resolve against, plus whether
// Cilium actually holds an endpoint for it.
type Pod struct {
	Namespace string
	Name      string
	Node      string
	Managed   bool
	// Identity is the numeric security identity from the endpoint, empty when
	// unmanaged. Reported rather than used: an endpoint that exists but carries
	// no identity is a different failure from having no endpoint at all.
	Identity string
}

// Key is the namespace/name pair a CiliumEndpoint shares with its pod.
func (p Pod) Key() string { return p.Namespace + "/" + p.Name }

// Group is the managed/unmanaged split for one namespace or one node.
type Group struct {
	Key       string
	Total     int
	Managed   int
	Unmanaged int
}

// Coverage is the ratio of managed pods to pods policy should cover.
func (g Group) Coverage() int {
	if g.Total == 0 {
		return 100
	}
	return g.Managed * 100 / g.Total
}

// ListPolicyPods returns the pods a NetworkPolicy can meaningfully select.
//
// Two exclusions keep the denominator honest. Host-network pods share the
// node's namespace and never get a CiliumEndpoint at all, so counting them
// would report a permanent gap that no remediation can close. Pods that are
// not Running have no sandbox yet or no longer have one, so a missing endpoint
// there is a race rather than a coverage hole.
func ListPolicyPods(ctx context.Context, kube kubernetes.Interface, namespace string) ([]Pod, error) {
	list, err := kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	out := make([]Pod, 0, len(list.Items))
	for i := range list.Items {
		p := list.Items[i]
		if p.Spec.HostNetwork || p.Status.Phase != corev1.PodRunning {
			continue
		}
		node := p.Spec.NodeName
		if node == "" {
			node = "(unscheduled)"
		}
		out = append(out, Pod{Namespace: p.Namespace, Name: p.Name, Node: node})
	}
	return out, nil
}

// ListEndpoints maps namespace/name to the security identity Cilium resolved
// for it. A pod absent from the result is unmanaged.
func ListEndpoints(ctx context.Context, dyn dynamic.Interface, namespace string) (map[string]string, error) {
	list, err := dyn.Resource(EndpointGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list ciliumendpoints.cilium.io: %w", err)
	}
	out := make(map[string]string, len(list.Items))
	for i := range list.Items {
		item := list.Items[i]
		key := item.GetNamespace() + "/" + item.GetName()
		id, found, err := identityID(item.Object)
		if err != nil {
			return nil, fmt.Errorf("ciliumendpoint %s: %w", key, err)
		}
		if !found {
			// An endpoint that exists without an identity still proves the
			// agent saw the pod, so it counts as managed and says so.
			out[key] = "(no identity)"
			continue
		}
		out[key] = id
	}
	return out, nil
}

// Join marks every pod for which an endpoint exists as managed.
func Join(pods []Pod, endpoints map[string]string) []Pod {
	out := make([]Pod, 0, len(pods))
	for _, p := range pods {
		if id, ok := endpoints[p.Key()]; ok {
			p.Managed = true
			p.Identity = id
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out
}

// GroupByNamespace aggregates the join by namespace. The rollout is sequenced
// per namespace and verified per node, so both axes are first-class here
// rather than one being derived by the caller.
func GroupByNamespace(pods []Pod) []Group {
	return groupBy(pods, func(p Pod) string { return p.Namespace })
}

// GroupByNode aggregates the join by the node each pod runs on.
func GroupByNode(pods []Pod) []Group {
	return groupBy(pods, func(p Pod) string { return p.Node })
}

func groupBy(pods []Pod, key func(Pod) string) []Group {
	index := map[string]*Group{}
	for _, p := range pods {
		k := key(p)
		g, ok := index[k]
		if !ok {
			g = &Group{Key: k}
			index[k] = g
		}
		g.Total++
		if p.Managed {
			g.Managed++
		} else {
			g.Unmanaged++
		}
	}
	out := make([]Group, 0, len(index))
	for _, g := range index {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		// Worst coverage first: the report exists to name what still needs
		// restarting, so the actionable rows must not sort below the clean ones.
		if out[i].Unmanaged != out[j].Unmanaged {
			return out[i].Unmanaged > out[j].Unmanaged
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// Total collapses every pod into a single group, so the aggregate line and the
// per-group rows are computed the same way.
func Total(pods []Pod) Group {
	groups := groupBy(pods, func(Pod) string { return "" })
	if len(groups) == 0 {
		return Group{}
	}
	return groups[0]
}

// Unmanaged returns only the pods with no endpoint, in the input order.
func Unmanaged(pods []Pod) []Pod {
	out := make([]Pod, 0, len(pods))
	for _, p := range pods {
		if !p.Managed {
			out = append(out, p)
		}
	}
	return out
}

// identityID reads status.identity.id, which a CRD may serialise as a number
// or a string. An unexpected type is an error rather than a default: silently
// reading it as absent would report a live endpoint as identity-less.
func identityID(obj map[string]interface{}) (string, bool, error) {
	status, ok := obj["status"].(map[string]interface{})
	if !ok {
		return "", false, nil
	}
	identity, ok := status["identity"].(map[string]interface{})
	if !ok {
		return "", false, nil
	}
	raw, ok := identity["id"]
	if !ok || raw == nil {
		return "", false, nil
	}
	switch v := raw.(type) {
	case int64:
		return fmt.Sprintf("%d", v), true, nil
	case int:
		return fmt.Sprintf("%d", v), true, nil
	case float64:
		return fmt.Sprintf("%d", int64(v)), true, nil
	case string:
		return v, true, nil
	default:
		return "", false, fmt.Errorf("status.identity.id is %T, want a number or string", raw)
	}
}
