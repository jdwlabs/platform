package cli

import (
	"bytes"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/jdwlabs/platform/internal/cilium"
	"github.com/jdwlabs/platform/internal/k8s"
)

// The fixture is the shape the ticket describes in miniature: one node whose
// pods were all recreated after its agent started, one node whose pods predate
// it, and a host-network pod that can never carry an endpoint.
func netpolPod(ns, name, node string, phase corev1.PodPhase, hostNetwork bool) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       corev1.PodSpec{NodeName: node, HostNetwork: hostNetwork},
		Status:     corev1.PodStatus{Phase: phase},
	}
}

func netpolEndpoint(ns, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "cilium.io/v2",
		"kind":       "CiliumEndpoint",
		"metadata":   map[string]interface{}{"name": name, "namespace": ns},
		"status":     map[string]interface{}{"identity": map[string]interface{}{"id": int64(4242)}},
	}}
}

func runNetpol(t *testing.T, args ...string) (string, error) {
	t.Helper()
	kc := k8s.NewFake(
		netpolPod("jdwlabs-prd", "api-old", "talos-lx0", corev1.PodRunning, false),
		netpolPod("jdwlabs-prd", "worker-old", "talos-lx0", corev1.PodRunning, false),
		netpolPod("monitoring", "grafana", "talos-k3y", corev1.PodRunning, false),
		netpolPod("kube-system", "flannel", "talos-lx0", corev1.PodRunning, true),
	)
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{cilium.EndpointGVR: "CiliumEndpointList"},
		netpolEndpoint("monitoring", "grafana"),
	)
	root := NewRootForTest(kc, dc)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestNetpolCoverage_ReportsTheSplitAndFailsBelowFullCoverage(t *testing.T) {
	out, err := runNetpol(t, "cluster", "netpol", "coverage")
	if err == nil {
		t.Fatalf("want a non-zero exit while coverage is incomplete:\n%s", out)
	}
	if !strings.Contains(out, "below --min-coverage 100") {
		t.Errorf("the refusal must name the threshold it failed:\n%s", out)
	}
	if !strings.Contains(out, `coverage: "1/3 pods managed (33%) / 2 unmanaged, cluster-wide"`) {
		t.Errorf("missing aggregate coverage line:\n%s", out)
	}
	if !strings.Contains(out, "by-namespace[2]{namespace,total,managed,unmanaged,coverage}:") {
		t.Errorf("missing TOON table header:\n%s", out)
	}
	if !strings.Contains(out, `jdwlabs-prd,"2","0","2",0%`) {
		t.Errorf("missing the fully unmanaged namespace row:\n%s", out)
	}
	if strings.Contains(out, "flannel") {
		t.Errorf("host-network pods must not be counted:\n%s", out)
	}
}

func TestNetpolCoverage_MinCoverageZeroReportsWithoutFailing(t *testing.T) {
	out, err := runNetpol(t, "cluster", "netpol", "coverage", "--min-coverage", "0")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "1/3 pods managed (33%)") {
		t.Errorf("missing coverage line:\n%s", out)
	}
}

func TestNetpolCoverage_UnmanagedListsTheRestartWorklist(t *testing.T) {
	out, _ := runNetpol(t, "cluster", "netpol", "coverage", "--unmanaged", "--min-coverage", "0")
	if !strings.Contains(out, "unmanaged[2]{namespace,pod,node}:") {
		t.Errorf("missing unmanaged table:\n%s", out)
	}
	if !strings.Contains(out, "jdwlabs-prd,api-old,talos-lx0") {
		t.Errorf("missing unmanaged pod row:\n%s", out)
	}
}

func TestNetpolCoverage_ByNodeSeparatesTheReconciledNode(t *testing.T) {
	out, _ := runNetpol(t, "cluster", "netpol", "coverage", "--by", "node", "--min-coverage", "0")
	if !strings.Contains(out, "by-node[2]{node,total,managed,unmanaged,coverage}:") {
		t.Errorf("missing per-node table:\n%s", out)
	}
	if !strings.Contains(out, `talos-k3y,"1","1","0",100%`) {
		t.Errorf("missing the fully managed node row:\n%s", out)
	}
}

func TestNetpolCoverage_NamespaceScopeNarrowsBothSidesOfTheJoin(t *testing.T) {
	out, err := runNetpol(t, "cluster", "netpol", "coverage", "-n", "monitoring")
	if err != nil {
		t.Fatalf("a fully managed namespace must exit zero: %v\n%s", err, out)
	}
	if !strings.Contains(out, "1/1 pods managed (100%) / 0 unmanaged, namespace monitoring") {
		t.Errorf("missing scoped coverage line:\n%s", out)
	}
	if !strings.Contains(out, "result: every pod-network pod has a CiliumEndpoint") {
		t.Errorf("missing the clean result line:\n%s", out)
	}
}

func TestNetpolCoverage_RejectsAnUnknownAxisWithTheValidSet(t *testing.T) {
	out, err := runNetpol(t, "cluster", "netpol", "coverage", "--by", "tenant")
	if err == nil {
		t.Fatal("want an error for an unknown --by")
	}
	if !strings.Contains(out, "unknown --by tenant; valid: namespace, node") {
		t.Errorf("refusal must name the valid set:\n%s", out)
	}
}

func TestNetpolCoverage_RejectsAnOutOfRangeThreshold(t *testing.T) {
	out, err := runNetpol(t, "cluster", "netpol", "coverage", "--min-coverage", "140")
	if err == nil {
		t.Fatal("want an error for a threshold outside 0-100")
	}
	if !strings.Contains(out, "--min-coverage 140 is outside 0-100") {
		t.Errorf("refusal must name the bound:\n%s", out)
	}
}

func TestNetpolCoverage_JSONEmitsOneEventPerGroupPlusASummary(t *testing.T) {
	out, err := runNetpol(t, "cluster", "netpol", "coverage", "--json", "--min-coverage", "0")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"phase":"netpol-coverage"`) {
		t.Errorf("missing phase on the event stream:\n%s", out)
	}
	if !strings.Contains(out, `"status":"broken"`) {
		t.Errorf("a namespace with unmanaged pods must be emitted as broken:\n%s", out)
	}
	if !strings.Contains(out, `"name":"summary","status":"broken"`) {
		t.Errorf("summary must carry the incomplete-coverage verdict:\n%s", out)
	}
}

// The per-namespace step gate in OPERATIONS.md advances on this command's exit
// code, so an empty scope must not be reported as complete: 0/0 would round to
// 100% and exit 0, and a typo mid-rollout would read as enrolled.
func TestNetpolCoverage_RefusesANamespaceWithNoPodsInsteadOfReportingFullCoverage(t *testing.T) {
	out, err := runNetpol(t, "cluster", "netpol", "coverage", "-n", "does-not-exist")
	if err == nil {
		t.Fatalf("an empty namespace scope must not exit zero:\n%s", out)
	}
	if !strings.Contains(out, "namespace does-not-exist has no pod-network pods to cover") {
		t.Errorf("refusal must name the empty scope:\n%s", out)
	}
	if strings.Contains(out, "100%") {
		t.Errorf("an undefined coverage must never be printed as 100%%:\n%s", out)
	}
}

// --min-coverage 0 waives the threshold, not the empty-scope refusal: the gate
// is that the namespace was measured at all.
func TestNetpolCoverage_EmptyNamespaceRefusalSurvivesMinCoverageZero(t *testing.T) {
	out, err := runNetpol(t, "cluster", "netpol", "coverage", "-n", "does-not-exist", "--min-coverage", "0")
	if err == nil {
		t.Fatalf("--min-coverage 0 must not turn an unmeasurable scope into a pass:\n%s", out)
	}
	if !strings.Contains(out, "has no pod-network pods to cover") {
		t.Errorf("missing refusal:\n%s", out)
	}
}
