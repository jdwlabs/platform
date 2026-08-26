package drain

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func container(name, memory, cpu string) any {
	return map[string]any{"name": name, "usage": map[string]any{"memory": memory, "cpu": cpu}}
}

func metricsClient(t *testing.T, pods map[string][]any) *dynamicfake.FakeDynamicClient {
	t.Helper()
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{PodMetricsGVR: "PodMetricsList"},
	)
	for name, containers := range pods {
		obj := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "metrics.k8s.io/v1beta1",
			"kind":       "PodMetrics",
			"metadata":   map[string]any{"name": name, "namespace": "ns"},
			"containers": containers,
		}}
		if _, err := dc.Resource(PodMetricsGVR).Namespace("ns").
			Create(context.Background(), obj, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	return dc
}

func TestLoadUsage_SumsEveryContainerOfAPod(t *testing.T) {
	dc := metricsClient(t, map[string][]any{
		"whole": {container("app", "100Mi", "100m"), container("sidecar", "50Mi", "50m")},
	})
	usage, err := LoadUsage(context.Background(), dc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := int64(150 * mi); usage["ns/whole"].Memory != want {
		t.Errorf("memory = %d, want %d", usage["ns/whole"].Memory, want)
	}
	if want := int64(150); usage["ns/whole"].CPU != want {
		t.Errorf("cpu = %d, want %d", usage["ns/whole"].CPU, want)
	}
}

// A pod totalled from only the containers that parsed reports less than it
// uses, which reads as a workload living within its request — the exact
// conclusion the usage column exists to challenge.
func TestLoadUsage_APodItCannotTotalIsLeftUnmeasuredRatherThanUnderstated(t *testing.T) {
	dc := metricsClient(t, map[string][]any{
		"partial": {container("app", "100Mi", "100m"), container("sidecar", "not-a-quantity", "50m")},
	})
	usage, err := LoadUsage(context.Background(), dc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := usage["ns/partial"]; ok {
		t.Fatalf("ns/partial was recorded as %v, want no entry at all", usage["ns/partial"])
	}

	c := &Cluster{Pods: []Pod{pod("ns", "partial", "a", 192)}}
	AttachUsage(c, usage)
	if c.Pods[0].UsageKnown {
		t.Error("UsageKnown = true, want false — an unreadable pod is unmeasured, not thrifty")
	}
}

// A container reporting memory but no CPU figure (or vice versa) is the same
// failure mode as an unparseable quantity: the pod is left unmeasured rather
// than totalled from half its containers' worth of data.
func TestLoadUsage_ACPUFigureMissingLeavesThePodUnmeasured(t *testing.T) {
	dc := metricsClient(t, map[string][]any{
		"nocpu": {map[string]any{"name": "app", "usage": map[string]any{"memory": "100Mi"}}},
	})
	usage, err := LoadUsage(context.Background(), dc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := usage["ns/nocpu"]; ok {
		t.Fatalf("ns/nocpu was recorded as %v, want no entry at all", usage["ns/nocpu"])
	}
}
