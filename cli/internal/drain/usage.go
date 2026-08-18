package drain

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// PodMetricsGVR is the metrics-server resource carrying live pod usage.
// It is read through the dynamic client rather than k8s.io/metrics so the
// binary does not take a dependency on a whole module for two fields.
var PodMetricsGVR = schema.GroupVersionResource{
	Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods",
}

// Usage is observed memory against what a pod declared.
//
// A feasibility check is only as good as the requests it reads: the scheduler
// places on declared requests, so a workload that declares a fraction of what
// it uses makes every node look emptier than it is and every drain verdict more
// optimistic than it should be. Reporting the two side by side is what turns a
// silently-wrong answer into a visible one.
type Usage struct {
	Memory int64 // bytes
}

// LoadUsage reads observed pod memory from metrics-server, keyed by
// namespace/name. A cluster without metrics-server is not an error here — the
// feasibility verdict does not depend on usage — so the caller is told the
// reason and continues without it.
func LoadUsage(ctx context.Context, dc dynamic.Interface) (map[string]Usage, error) {
	list, err := dc.Resource(PodMetricsGVR).Namespace(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("read metrics.k8s.io pod metrics: %w", err)
	}

	out := make(map[string]Usage, len(list.Items))
	for _, item := range list.Items {
		containers, found, err := unstructuredSlice(item.Object, "containers")
		if err != nil || !found {
			continue
		}
		var total int64
		for _, c := range containers {
			m, ok := c.(map[string]any)
			if !ok {
				continue
			}
			usage, ok := m["usage"].(map[string]any)
			if !ok {
				continue
			}
			raw, ok := usage["memory"].(string)
			if !ok {
				continue
			}
			q, err := resource.ParseQuantity(raw)
			if err != nil {
				continue
			}
			total += q.Value()
		}
		out[item.GetNamespace()+"/"+item.GetName()] = Usage{Memory: total}
	}
	return out, nil
}

func unstructuredSlice(obj map[string]any, key string) ([]any, bool, error) {
	v, ok := obj[key]
	if !ok {
		return nil, false, nil
	}
	s, ok := v.([]any)
	if !ok {
		return nil, false, fmt.Errorf("%s is %T, want a list", key, v)
	}
	return s, true, nil
}

// AttachUsage records observed memory on every pod that metrics-server knows
// about. Pods it does not know about keep UsageKnown false rather than a zero
// that would read as "uses nothing".
func AttachUsage(c *Cluster, usage map[string]Usage) {
	for i := range c.Pods {
		u, ok := usage[c.Pods[i].Ref()]
		if !ok {
			continue
		}
		c.Pods[i].UsedMemory = u.Memory
		c.Pods[i].UsageKnown = true
	}
}
