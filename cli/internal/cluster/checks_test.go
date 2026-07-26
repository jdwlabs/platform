package cluster

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func newDeployment(ns, name, workloadName string, ready int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels: map[string]string{
				"app.kubernetes.io/name":     workloadName,
				"app.kubernetes.io/instance": name,
			},
		},
		Status: appsv1.DeploymentStatus{
			Replicas:      ready,
			ReadyReplicas: ready,
			Conditions: []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
			},
		},
	}
}

func TestCheckDeploymentByWorkloadName_FindsHelmReleasePrefixed(t *testing.T) {
	kube := fake.NewSimpleClientset(
		newDeployment("cert-manager", "platform-cert-manager", "cert-manager", 1),
	)
	r := checkDeploymentByWorkloadName(context.Background(), kube, "cert-manager", "cert-manager")
	if r.Status != StatusPass {
		t.Fatalf("expected pass, got %s: %s", r.Status, r.Message)
	}
	if !strings.Contains(r.Message, "Available") {
		t.Fatalf("expected Available in message, got: %s", r.Message)
	}
}

func TestCheckDeploymentByWorkloadName_IgnoresSiblingComponents(t *testing.T) {
	// cert-manager release also ships a cainjector + webhook deployment.
	// Probe must match only the main controller (name=cert-manager).
	kube := fake.NewSimpleClientset(
		newDeployment("cert-manager", "platform-cert-manager", "cert-manager", 1),
		newDeployment("cert-manager", "platform-cert-manager-cainjector", "cainjector", 1),
		newDeployment("cert-manager", "platform-cert-manager-webhook", "webhook", 1),
	)
	r := checkDeploymentByWorkloadName(context.Background(), kube, "cert-manager", "cert-manager")
	if r.Status != StatusPass {
		t.Fatalf("expected pass, got %s: %s", r.Status, r.Message)
	}
}

func TestCheckDeploymentByWorkloadName_NoMatch(t *testing.T) {
	kube := fake.NewSimpleClientset(
		newDeployment("cert-manager", "platform-cert-manager-webhook", "webhook", 1),
	)
	r := checkDeploymentByWorkloadName(context.Background(), kube, "cert-manager", "cert-manager")
	if r.Status != StatusFail {
		t.Fatalf("expected fail when main controller absent, got: %v", r)
	}
}

func newVaultPod(name string, phase corev1.PodPhase, ready bool) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "vault",
			Labels:    map[string]string{"app.kubernetes.io/name": "vault"},
		},
		Status: corev1.PodStatus{
			Phase: phase,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: boolCondition(ready)},
			},
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "vault", Ready: ready, State: corev1.ContainerState{
					Running: &corev1.ContainerStateRunning{},
				}},
			},
		},
	}
}

func boolCondition(b bool) corev1.ConditionStatus {
	if b {
		return corev1.ConditionTrue
	}
	return corev1.ConditionFalse
}

// A sealed Vault is Running but not Ready — its readiness probe runs
// `vault status`, which exits non-zero while sealed. The phase-based check this
// replaced reported exactly this state as healthy.
func TestCheckVaultPodReady_SealedPodIsNotHealthy(t *testing.T) {
	kube := fake.NewSimpleClientset(newVaultPod("platform-vault-0", corev1.PodRunning, false))
	r := checkVaultPodReady(context.Background(), kube)
	if r.Status != StatusFail {
		t.Fatalf("expected fail for Running-but-not-Ready vault, got %s: %s", r.Status, r.Message)
	}
	if !strings.Contains(r.Message, "sealed") {
		t.Fatalf("expected message to name the sealed case, got: %s", r.Message)
	}
}

func TestCheckVaultPodReady_UnsealedPasses(t *testing.T) {
	kube := fake.NewSimpleClientset(newVaultPod("platform-vault-0", corev1.PodRunning, true))
	r := checkVaultPodReady(context.Background(), kube)
	if r.Status != StatusPass {
		t.Fatalf("expected pass, got %s: %s", r.Status, r.Message)
	}
}

// With raft HA a partially-unsealed set still serves reads on quorum, so it is
// degraded rather than down.
func TestCheckVaultPodReady_PartiallyReadyWarns(t *testing.T) {
	kube := fake.NewSimpleClientset(
		newVaultPod("platform-vault-0", corev1.PodRunning, true),
		newVaultPod("platform-vault-1", corev1.PodRunning, true),
		newVaultPod("platform-vault-2", corev1.PodRunning, false),
	)
	r := checkVaultPodReady(context.Background(), kube)
	if r.Status != StatusWarn {
		t.Fatalf("expected warn for 2/3 ready, got %s: %s", r.Status, r.Message)
	}
	if !strings.Contains(r.Message, "platform-vault-2") {
		t.Fatalf("expected the unready member named, got: %s", r.Message)
	}
}

func TestCheckVaultPodReady_CrashLoopFails(t *testing.T) {
	pod := newVaultPod("platform-vault-0", corev1.PodRunning, false)
	pod.Status.ContainerStatuses[0].State = corev1.ContainerState{
		Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
	}
	kube := fake.NewSimpleClientset(pod)
	r := checkVaultPodReady(context.Background(), kube)
	if r.Status != StatusFail || !strings.Contains(r.Message, "CrashLoopBackOff") {
		t.Fatalf("expected CrashLoopBackOff fail, got %s: %s", r.Status, r.Message)
	}
}

func newStatefulSet(ns, name string, strategy appsv1.StatefulSetUpdateStrategyType, cur, upd string) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: appsv1.StatefulSetSpec{
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{Type: strategy},
		},
		Status: appsv1.StatefulSetStatus{CurrentRevision: cur, UpdateRevision: upd},
	}
}

// The condition that hid a Vault major-version bump for 47 minutes: OnDelete
// records a new updateRevision but never rolls the pod.
func TestCheckStatefulSetRevisions_OnDeleteSkewIsSurfaced(t *testing.T) {
	kube := fake.NewSimpleClientset(
		newStatefulSet("vault", "platform-vault", appsv1.OnDeleteStatefulSetStrategyType,
			"platform-vault-65d6fbffd6", "platform-vault-556744bc94"),
		newStatefulSet("monitoring", "platform-loki", appsv1.RollingUpdateStatefulSetStrategyType,
			"platform-loki-797785bc86", "platform-loki-797785bc86"),
	)
	r := checkStatefulSetRevisions(context.Background(), kube)
	if r.Status != StatusWarn {
		t.Fatalf("expected warn for pending OnDelete roll, got %s: %s", r.Status, r.Message)
	}
	for _, want := range []string{"vault/platform-vault", "OnDelete", "65d6fbffd6", "556744bc94"} {
		if !strings.Contains(r.Message, want) {
			t.Fatalf("expected %q in message, got: %s", want, r.Message)
		}
	}
	if strings.Contains(r.Message, "platform-loki") {
		t.Fatalf("settled StatefulSet should not be reported, got: %s", r.Message)
	}
}

func TestCheckStatefulSetRevisions_NoSkewPasses(t *testing.T) {
	kube := fake.NewSimpleClientset(
		newStatefulSet("monitoring", "platform-loki", appsv1.RollingUpdateStatefulSetStrategyType,
			"platform-loki-797785bc86", "platform-loki-797785bc86"),
	)
	r := checkStatefulSetRevisions(context.Background(), kube)
	if r.Status != StatusPass {
		t.Fatalf("expected pass, got %s: %s", r.Status, r.Message)
	}
}

// A StatefulSet mid-creation has neither revision written yet; that is unsettled,
// not skewed, and must not be reported as a pending roll.
func TestCheckStatefulSetRevisions_UnsettledIsNotSkew(t *testing.T) {
	kube := fake.NewSimpleClientset(
		newStatefulSet("vault", "platform-vault", appsv1.OnDeleteStatefulSetStrategyType, "", ""),
	)
	r := checkStatefulSetRevisions(context.Background(), kube)
	if r.Status != StatusPass {
		t.Fatalf("expected pass for unsettled StatefulSet, got %s: %s", r.Status, r.Message)
	}
}

func TestCheckStatefulSetRevisions_EmptyClusterIsDefinitive(t *testing.T) {
	r := checkStatefulSetRevisions(context.Background(), fake.NewSimpleClientset())
	if r.Status != StatusPass {
		t.Fatalf("expected pass, got %s: %s", r.Status, r.Message)
	}
	if r.Message == "" {
		t.Fatal("empty result must state the zero case explicitly, not return a blank message")
	}
}

func TestRevisionHash(t *testing.T) {
	for in, want := range map[string]string{
		"platform-vault-556744bc94": "556744bc94",
		"nodashes":                  "nodashes",
		"trailing-":                 "trailing-",
	} {
		if got := revisionHash(in); got != want {
			t.Fatalf("revisionHash(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCheckDeploymentByWorkloadName_NotAvailable(t *testing.T) {
	d := newDeployment("external-secrets", "platform-external-secrets", "external-secrets", 0)
	d.Status.Conditions = []appsv1.DeploymentCondition{
		{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionFalse},
	}
	kube := fake.NewSimpleClientset(d)
	r := checkDeploymentByWorkloadName(context.Background(), kube, "external-secrets", "external-secrets")
	if r.Status != StatusFail {
		t.Fatalf("expected fail when Available=False, got: %v", r)
	}
}
