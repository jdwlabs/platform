package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/jdwlabs/platform/internal/drain"
	"github.com/jdwlabs/platform/internal/k8s"
)

// The fixture is this cluster in miniature: two roomy workers and one small
// one, and a three-replica workload with required hostname anti-affinity whose
// third replica therefore has nowhere to go when the small worker is drained.
func drainNode(name string, allocMi int64) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{"kubernetes.io/hostname": name},
		},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceMemory: *resource.NewQuantity(allocMi*1024*1024, resource.BinarySI),
				corev1.ResourceCPU:    *resource.NewMilliQuantity(4000, resource.DecimalSI),
				corev1.ResourcePods:   *resource.NewQuantity(110, resource.DecimalSI),
			},
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
}

func taintedNode(name string, allocMi int64) *corev1.Node {
	n := drainNode(name, allocMi)
	n.Spec.Taints = []corev1.Taint{{
		Key: "node-role.kubernetes.io/control-plane", Effect: corev1.TaintEffectNoSchedule,
	}}
	return n
}

// plainPod carries no placement constraint, so it can go wherever there is room.
func plainPod(name, node string, memMi int64, tolerateControlPlane bool) *corev1.Pod {
	p := drainPod(name, node, memMi, false)
	p.Namespace = "default"
	p.Labels = map[string]string{"app": "plain"}
	if tolerateControlPlane {
		p.Spec.Tolerations = []corev1.Toleration{{
			Key: "node-role.kubernetes.io/control-plane", Operator: corev1.TolerationOpExists,
		}}
	}
	return p
}

func drainPod(name, node string, memMi int64, hard bool) *corev1.Pod {
	controller := true
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "database",
			Labels:    map[string]string{"app": "pg"},
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "StatefulSet", Name: "pg", Controller: &controller},
			},
		},
		Spec: corev1.PodSpec{
			NodeName: node,
			Containers: []corev1.Container{{
				Name: "pg",
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceMemory: *resource.NewQuantity(memMi*1024*1024, resource.BinarySI),
				}},
			}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	if hard {
		p.Spec.Affinity = &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
				LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "pg"}},
				TopologyKey:   "kubernetes.io/hostname",
			}},
		}}
	}
	return p
}

// seedPodMetrics creates through the resource client rather than seeding the
// tracker, because the fake derives a GVR from an object's kind and would file
// a PodMetrics under "podmetrics" — while metrics-server really serves it at
// metrics.k8s.io/v1beta1 "pods", which is what the loader reads.
func seedPodMetrics(t *testing.T, dc *dynamicfake.FakeDynamicClient, ns, name, memory string) {
	t.Helper()
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "metrics.k8s.io/v1beta1",
		"kind":       "PodMetrics",
		"metadata":   map[string]interface{}{"name": name, "namespace": ns},
		"containers": []interface{}{
			map[string]interface{}{"name": "pg", "usage": map[string]interface{}{"memory": memory}},
		},
	}}
	if _, err := dc.Resource(drain.PodMetricsGVR).Namespace(ns).
		Create(context.Background(), obj, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed pod metrics: %v", err)
	}
}

func runDrain(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return runDrainWithMetrics(t, true, args...)
}

// metricsUp toggles whether metrics-server answers, because "the cluster has no
// metrics-server" and "it has one" are different reports and both must work.
func runDrainWithMetrics(t *testing.T, metricsUp bool, args ...string) (string, error) {
	t.Helper()
	kc := k8s.NewFake(
		drainNode("big-1", 8000),
		drainNode("big-2", 8000),
		drainNode("small", 300),
		taintedNode("cp", 8000),
		drainPod("pg-0", "big-1", 192, true),
		drainPod("pg-1", "big-2", 192, true),
		drainPod("pg-2", "small", 192, true),
		plainPod("plain-1", "big-1", 64, false),
		plainPod("plain-2", "cp", 64, true),
		&policyv1.PodDisruptionBudget{
			ObjectMeta: metav1.ObjectMeta{Name: "pg", Namespace: "database"},
			Spec: policyv1.PodDisruptionBudgetSpec{
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "pg"}},
			},
			Status: policyv1.PodDisruptionBudgetStatus{DisruptionsAllowed: 0},
		},
	)
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			drain.PodMetricsGVR: "PodMetricsList",
			{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}: "ApplicationList",
		},
	)
	seedPodMetrics(t, dc, "database", "pg-2", "300Mi")
	if !metricsUp {
		dc.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewServiceUnavailable("no metrics")
		})
	}
	root := NewRootForTest(kc, dc)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestDrainCheck_DefaultShapeIsToonWithFiveFields(t *testing.T) {
	out, _ := runDrain(t, "cluster", "drain-check")
	if !strings.Contains(out, "count: 4 nodes (3 blocked / 1 drainable / 0 empty / 0 skipped)") {
		t.Errorf("missing aggregate count line:\n%s", out)
	}
	if !strings.Contains(out, "nodes[4]{node,verdict,movable,movableMem,blockers}:") {
		t.Errorf("missing TOON table header:\n%s", out)
	}
	if !strings.Contains(out, "small,blocked,") {
		t.Errorf("small worker should be blocked:\n%s", out)
	}
}

func TestDrainCheck_ExitsNonZeroWhenANodeCannotBeDrained(t *testing.T) {
	out, err := runDrain(t, "cluster", "drain-check")
	if err == nil {
		t.Fatalf("expected a non-zero exit when a drain is infeasible:\n%s", out)
	}
	if code := ExitCode(err); code != ExitHardFail {
		t.Errorf("exit code = %d, want %d", code, ExitHardFail)
	}
}

func TestDrainCheck_BlockedNodeReportsThePodAndWhyNoNodeTakesIt(t *testing.T) {
	out, _ := runDrain(t, "cluster", "drain-check")
	if !strings.Contains(out, "blockers[3]{node,pod,request,class,reason}:") {
		t.Errorf("missing blockers table:\n%s", out)
	}
	if !strings.Contains(out, "database/pg-2,192Mi,hard,") {
		t.Errorf("blocker row should name the pod, its request and a hard reason class:\n%s", out)
	}
	if !strings.Contains(out, "anti-affinity") {
		t.Errorf("reason should name the constraint that excludes every node:\n%s", out)
	}
}

func TestDrainCheck_LeadsWithTheBlockedNode(t *testing.T) {
	out, _ := runDrain(t, "cluster", "drain-check")
	rows := strings.SplitN(out, "nodes[4]", 2)[1]
	if !strings.Contains(strings.SplitN(rows, "\n", 3)[1], "big-1,blocked") {
		t.Errorf("first data row should be the blocked node:\n%s", out)
	}
}

func TestDrainCheck_PlanShowsWhereEachEvictedPodWouldGo(t *testing.T) {
	out, err := runDrain(t, "cluster", "drain-check", "--node", "cp", "--plan")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "plan[1]{node,pod,request,target}:") {
		t.Errorf("missing plan table:\n%s", out)
	}
	if !strings.Contains(out, "cp,default/plain-2,64Mi,big-2") {
		t.Errorf("plain-2 should land on the roomiest node that will take it:\n%s", out)
	}
}

func TestDrainCheck_PodsListsClassificationAndDisruptionBudget(t *testing.T) {
	out, _ := runDrain(t, "cluster", "drain-check", "--node", "small", "--pods")
	if !strings.Contains(out, "pods[1]{node,pod,class,request,pdb,allowed}:") {
		t.Errorf("missing pods table:\n%s", out)
	}
	if !strings.Contains(out, "small,database/pg-2,movable,192Mi,database/pg,\"0\"") {
		t.Errorf("pod row should carry its budget and current allowance:\n%s", out)
	}
}

func TestDrainCheck_UnknownNodeIsRefusedWithTheKnownSet(t *testing.T) {
	out, err := runDrain(t, "cluster", "drain-check", "--node", "nope")
	if err == nil {
		t.Fatalf("expected an error for an unknown node:\n%s", out)
	}
	if !strings.Contains(out, "big-1") || !strings.Contains(out, "small") {
		t.Errorf("refusal should name the nodes that do exist:\n%s", out)
	}
}

func TestDrainCheck_UnknownFieldIsRefusedWithTheValidSet(t *testing.T) {
	out, err := runDrain(t, "cluster", "drain-check", "--fields", "node,bogus")
	if err == nil {
		t.Fatalf("expected an error for an unknown field:\n%s", out)
	}
	if !strings.Contains(out, "unknown field bogus") {
		t.Errorf("refusal should name the field:\n%s", out)
	}
	if !strings.Contains(out, "tightestAfter") {
		t.Errorf("refusal should list the valid fields:\n%s", out)
	}
}

func TestDrainCheck_FieldsAndFullAreMutuallyExclusive(t *testing.T) {
	out, err := runDrain(t, "cluster", "drain-check", "--fields", "node", "--full")
	if err == nil {
		t.Fatalf("expected an error:\n%s", out)
	}
	if !strings.Contains(out, "mutually exclusive") {
		t.Errorf("refusal should say why:\n%s", out)
	}
}

func TestDrainCheck_JSONEmitsOneEventPerNodeAndASummary(t *testing.T) {
	out, _ := runDrain(t, "--json", "cluster", "drain-check")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 5 {
		t.Fatalf("want 4 node events plus a summary, got %d:\n%s", len(lines), out)
	}
	for _, l := range lines {
		if !strings.Contains(l, `"phase":"drain-check"`) {
			t.Errorf("event is missing the phase field: %s", l)
		}
	}
	if !strings.Contains(out, `"status":"broken"`) {
		t.Errorf("the blocked node should be reported broken:\n%s", out)
	}
	if !strings.Contains(out, `"name":"summary"`) {
		t.Errorf("missing the summary event:\n%s", out)
	}
}

// Usage never decides a verdict, so a cluster without metrics-server must still
// answer the feasibility question — while saying the column is missing rather
// than printing an empty one that reads as "nothing is oversized".
func TestDrainCheck_MissingMetricsServerDowngradesRatherThanFails(t *testing.T) {
	out, err := runDrainWithMetrics(t, false, "cluster", "drain-check", "--node", "cp", "--usage")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "warning: ") || !strings.Contains(out, "observed memory unavailable") {
		t.Errorf("missing the warning about absent usage data:\n%s", out)
	}
	if !strings.Contains(out, "nodes[1]") {
		t.Errorf("the verdict should still be reported:\n%s", out)
	}
}

func TestDrainCheck_ReadOnlyPromiseIsStated(t *testing.T) {
	out, _ := runDrain(t, "cluster", "drain-check")
	if !strings.Contains(out, "Nothing was cordoned, evicted or applied") {
		t.Errorf("the report should say it mutated nothing:\n%s", out)
	}
}

// The correctness bug this command exists alongside: a pod declaring a fraction
// of what it uses makes every node look emptier than it is.
func TestDrainCheck_UsageReportsObservedMemoryBesideTheDeclaredRequest(t *testing.T) {
	out, _ := runDrain(t, "cluster", "drain-check", "--node", "small", "--pods", "--usage")
	if !strings.Contains(out, "pods[1]{node,pod,class,request,used,pdb,allowed}:") {
		t.Errorf("pods table should gain a used column:\n%s", out)
	}
	if !strings.Contains(out, "small,database/pg-2,movable,192Mi,300Mi,") {
		t.Errorf("row should show 192Mi declared against 300Mi observed:\n%s", out)
	}
}

func TestDrainCheck_UsageSummarisesTheShortfallPerNode(t *testing.T) {
	out, _ := runDrain(t, "cluster", "drain-check", "--usage", "--fields", "node,used,underDeclared")
	if !strings.Contains(out, "small,300Mi,108Mi") {
		t.Errorf("node row should report observed memory and the 108Mi it exceeds its request by:\n%s", out)
	}
	if !strings.Contains(out, "big-2,\"-\",\"-\"") {
		t.Errorf("a node metrics-server said nothing about should read as unmeasured, not zero:\n%s", out)
	}
}
