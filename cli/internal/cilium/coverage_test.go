package cilium

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/jdwlabs/platform/internal/k8s"
)

func pod(ns, name, node string, phase corev1.PodPhase, hostNetwork bool) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       corev1.PodSpec{NodeName: node, HostNetwork: hostNetwork},
		Status:     corev1.PodStatus{Phase: phase},
	}
}

func endpoint(ns, name string, identity interface{}) *unstructured.Unstructured {
	obj := map[string]interface{}{
		"apiVersion": "cilium.io/v2",
		"kind":       "CiliumEndpoint",
		"metadata":   map[string]interface{}{"name": name, "namespace": ns},
	}
	if identity != nil {
		obj["status"] = map[string]interface{}{
			"identity": map[string]interface{}{"id": identity},
		}
	}
	return &unstructured.Unstructured{Object: obj}
}

func endpointClient(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{EndpointGVR: "CiliumEndpointList"},
		objs...,
	)
}

// The fixture mirrors the state that makes this package necessary: a node
// whose pods all postdate its agent reads fully managed, while a node that was
// never drained reads mostly unmanaged, and the cluster-wide number hides both.
func fixture(t *testing.T) []Pod {
	t.Helper()
	kube := k8s.NewFake(
		pod("jdwlabs-prd", "api-old", "talos-lx0", corev1.PodRunning, false),
		pod("jdwlabs-prd", "api-new", "talos-k3y", corev1.PodRunning, false),
		pod("jdwlabs-prd", "worker-old", "talos-lx0", corev1.PodRunning, false),
		pod("kube-system", "flannel", "talos-lx0", corev1.PodRunning, true),
		pod("kube-system", "coredns-pending", "talos-k3y", corev1.PodPending, false),
	)
	dyn := endpointClient(
		endpoint("jdwlabs-prd", "api-new", int64(12345)),
	)
	pods, err := ListPolicyPods(context.Background(), kube, "")
	if err != nil {
		t.Fatalf("ListPolicyPods: %v", err)
	}
	eps, err := ListEndpoints(context.Background(), dyn, "")
	if err != nil {
		t.Fatalf("ListEndpoints: %v", err)
	}
	return Join(pods, eps)
}

func TestListPolicyPods_ExcludesPodsThatCanNeverCarryAnEndpoint(t *testing.T) {
	pods := fixture(t)
	if len(pods) != 3 {
		t.Fatalf("want 3 policy pods (host-network and non-Running excluded), got %d: %+v", len(pods), pods)
	}
	for _, p := range pods {
		if p.Name == "flannel" || p.Name == "coredns-pending" {
			t.Errorf("%s should not be counted as a policy pod", p.Key())
		}
	}
}

func TestJoin_MarksOnlyPodsWithAnEndpoint(t *testing.T) {
	pods := fixture(t)
	got := map[string]bool{}
	for _, p := range pods {
		got[p.Key()] = p.Managed
	}
	want := map[string]bool{
		"jdwlabs-prd/api-new":    true,
		"jdwlabs-prd/api-old":    false,
		"jdwlabs-prd/worker-old": false,
	}
	for key, wantManaged := range want {
		if got[key] != wantManaged {
			t.Errorf("%s managed=%v, want %v", key, got[key], wantManaged)
		}
	}
}

func TestJoin_CarriesTheIdentityThroughSoAnEndpointWithoutOneIsDistinguishable(t *testing.T) {
	kube := k8s.NewFake(pod("ns", "p", "n", corev1.PodRunning, false))
	dyn := endpointClient(endpoint("ns", "p", nil))
	pods, err := ListPolicyPods(context.Background(), kube, "")
	if err != nil {
		t.Fatalf("ListPolicyPods: %v", err)
	}
	eps, err := ListEndpoints(context.Background(), dyn, "")
	if err != nil {
		t.Fatalf("ListEndpoints: %v", err)
	}
	joined := Join(pods, eps)
	if !joined[0].Managed {
		t.Fatalf("an endpoint without an identity still proves the agent saw the pod")
	}
	if joined[0].Identity != "(no identity)" {
		t.Errorf("identity = %q, want the explicit no-identity marker", joined[0].Identity)
	}
}

func TestListEndpoints_RejectsAnIdentityItCannotRead(t *testing.T) {
	dyn := endpointClient(endpoint("ns", "p", []interface{}{"not-a-number"}))
	if _, err := ListEndpoints(context.Background(), dyn, ""); err == nil {
		t.Fatal("want an error rather than a silently identity-less endpoint")
	}
}

func TestGroupByNode_SeparatesADrainedNodeFromOneThatWasNot(t *testing.T) {
	groups := GroupByNode(fixture(t))
	byKey := map[string]Group{}
	for _, g := range groups {
		byKey[g.Key] = g
	}
	if got := byKey["talos-lx0"]; got.Managed != 0 || got.Total != 2 || got.Coverage() != 0 {
		t.Errorf("talos-lx0 = %+v, want 0/2 at 0%%", got)
	}
	if got := byKey["talos-k3y"]; got.Managed != 1 || got.Total != 1 || got.Coverage() != 100 {
		t.Errorf("talos-k3y = %+v, want 1/1 at 100%%", got)
	}
	// Worst-first ordering is the point of the report, not a cosmetic choice.
	if groups[0].Key != "talos-lx0" {
		t.Errorf("groups[0] = %s, want the node with the most unmanaged pods first", groups[0].Key)
	}
}

func TestTotal_RoundsCoverageDownAndSurvivesAnEmptyCluster(t *testing.T) {
	total := Total(fixture(t))
	if total.Total != 3 || total.Managed != 1 || total.Unmanaged != 2 {
		t.Fatalf("total = %+v, want 1 managed of 3", total)
	}
	if total.Coverage() != 33 {
		t.Errorf("coverage = %d%%, want 33%%", total.Coverage())
	}
	if got := Total(nil); got.Total != 0 || got.Coverage() != 100 {
		t.Errorf("empty cluster = %+v at %d%%, want a zero group reported as full coverage", got, got.Coverage())
	}
}

func TestUnmanaged_IsTheRestartWorklist(t *testing.T) {
	got := Unmanaged(fixture(t))
	if len(got) != 2 {
		t.Fatalf("want 2 unmanaged pods, got %d", len(got))
	}
	for _, p := range got {
		if p.Managed {
			t.Errorf("%s is managed and must not be on the worklist", p.Key())
		}
	}
}
