package cluster

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
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

func newStatefulSet(ns, name string, strategy appsv1.StatefulSetUpdateStrategyType, cur, upd string, replicas, updated int32) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: appsv1.StatefulSetSpec{
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{Type: strategy},
		},
		Status: appsv1.StatefulSetStatus{
			CurrentRevision: cur,
			UpdateRevision:  upd,
			Replicas:        replicas,
			UpdatedReplicas: updated,
		},
	}
}

// The condition that hid a Vault major-version bump for 47 minutes: OnDelete
// records a new updateRevision but never rolls the pod.
func TestCheckStatefulSetRevisions_OnDeleteSkewIsSurfaced(t *testing.T) {
	kube := fake.NewSimpleClientset(
		newStatefulSet("vault", "platform-vault", appsv1.OnDeleteStatefulSetStrategyType,
			"platform-vault-65d6fbffd6", "platform-vault-556744bc94", 1, 0),
		newStatefulSet("monitoring", "platform-loki", appsv1.RollingUpdateStatefulSetStrategyType,
			"platform-loki-797785bc86", "platform-loki-797785bc86", 1, 1),
	)
	r := checkStatefulSetRevisions(context.Background(), kube)
	if r.Status != StatusWarn {
		t.Fatalf("expected warn for pending OnDelete roll, got %s: %s", r.Status, r.Message)
	}
	for _, want := range []string{"vault/platform-vault", "OnDelete", "0/1", "556744bc94"} {
		if !strings.Contains(r.Message, want) {
			t.Fatalf("expected %q in message, got: %s", want, r.Message)
		}
	}
	if strings.Contains(r.Message, "platform-loki") {
		t.Fatalf("settled StatefulSet should not be reported, got: %s", r.Message)
	}
}

// Regression: under OnDelete the controller never advances currentRevision, even
// once every pod runs updateRevision. Comparing the two revisions therefore
// warns forever on an already-adopted StatefulSet — which is how the live Vault
// StatefulSet looked after its pod had picked up 2.0.3.
func TestCheckStatefulSetRevisions_OnDeleteAdoptedIsNotPending(t *testing.T) {
	kube := fake.NewSimpleClientset(
		newStatefulSet("vault", "platform-vault", appsv1.OnDeleteStatefulSetStrategyType,
			"platform-vault-65d6fbffd6", "platform-vault-556744bc94", 1, 1),
	)
	r := checkStatefulSetRevisions(context.Background(), kube)
	if r.Status != StatusPass {
		t.Fatalf("stale currentRevision with every pod updated must pass, got %s: %s", r.Status, r.Message)
	}
}

func TestCheckStatefulSetRevisions_NoSkewPasses(t *testing.T) {
	kube := fake.NewSimpleClientset(
		newStatefulSet("monitoring", "platform-loki", appsv1.RollingUpdateStatefulSetStrategyType,
			"platform-loki-797785bc86", "platform-loki-797785bc86", 1, 1),
	)
	r := checkStatefulSetRevisions(context.Background(), kube)
	if r.Status != StatusPass {
		t.Fatalf("expected pass, got %s: %s", r.Status, r.Message)
	}
}

// A StatefulSet mid-creation has no revision written yet; that is unsettled,
// not skewed, and must not be reported as a pending roll.
func TestCheckStatefulSetRevisions_UnsettledIsNotSkew(t *testing.T) {
	kube := fake.NewSimpleClientset(
		newStatefulSet("vault", "platform-vault", appsv1.OnDeleteStatefulSetStrategyType, "", "", 0, 0),
	)
	r := checkStatefulSetRevisions(context.Background(), kube)
	if r.Status != StatusPass {
		t.Fatalf("expected pass for unsettled StatefulSet, got %s: %s", r.Status, r.Message)
	}
}

// Scaled to zero: no pods exist to adopt anything.
func TestCheckStatefulSetRevisions_ScaledToZeroIsNotPending(t *testing.T) {
	kube := fake.NewSimpleClientset(
		newStatefulSet("monitoring", "platform-tempo", appsv1.RollingUpdateStatefulSetStrategyType,
			"platform-tempo-aaa", "platform-tempo-bbb", 0, 0),
	)
	r := checkStatefulSetRevisions(context.Background(), kube)
	if r.Status != StatusPass {
		t.Fatalf("expected pass for scaled-to-zero StatefulSet, got %s: %s", r.Status, r.Message)
	}
}

// A RollingUpdate genuinely mid-roll is a real pending state, transient but real.
func TestCheckStatefulSetRevisions_RollingUpdateInProgressWarns(t *testing.T) {
	kube := fake.NewSimpleClientset(
		newStatefulSet("monitoring", "platform-loki", appsv1.RollingUpdateStatefulSetStrategyType,
			"platform-loki-old", "platform-loki-new", 3, 1),
	)
	r := checkStatefulSetRevisions(context.Background(), kube)
	if r.Status != StatusWarn || !strings.Contains(r.Message, "1/3") {
		t.Fatalf("expected warn naming 1/3 rolled, got %s: %s", r.Status, r.Message)
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

// --- checkArgoWorkloadImageDrift ---

func newApplication(name string, resources []map[string]any) *unstructured.Unstructured {
	resourceList := make([]interface{}, len(resources))
	for i, r := range resources {
		resourceList[i] = r
	}
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "Application",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": "argocd",
			},
			"status": map[string]interface{}{
				"resources": resourceList,
			},
		},
	}
}

func newDynamicApps(apps ...*unstructured.Unstructured) dynamic.Interface {
	objs := make([]runtime.Object, len(apps))
	for i, a := range apps {
		objs[i] = a
	}
	scheme := runtime.NewScheme()
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			gvrApplication: "ApplicationList",
		},
		objs...,
	)
}

func newStatefulSetWithImage(ns, name, image string, matchLabels map[string]string) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: appsv1.StatefulSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: matchLabels},
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: name, Image: image}},
				},
			},
		},
	}
}

func newDeploymentWithImage(ns, name, image string, matchLabels map[string]string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: matchLabels},
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: name, Image: image}},
				},
			},
		},
	}
}

func newRunningPod(ns, name string, podLabels map[string]string, runningImage string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: podLabels},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{Image: runningImage}},
		},
	}
}

// This is the OnDelete stall the ticket was written against, generalized past
// StatefulSets alone: the workload spec says one tag, ArgoCD would report
// this Synced, and the pod actually serving traffic has never been recreated.
func TestCheckArgoWorkloadImageDrift_StuckRolloutIsSurfaced(t *testing.T) {
	app := newApplication("platform-vault", []map[string]any{
		{"kind": "StatefulSet", "namespace": "vault", "name": "platform-vault", "group": "apps", "version": "v1"},
	})
	dyn := newDynamicApps(app)
	kube := fake.NewSimpleClientset(
		newStatefulSetWithImage("vault", "platform-vault", "hashicorp/vault:2.0.3", map[string]string{"app": "vault"}),
		newRunningPod("vault", "platform-vault-0", map[string]string{"app": "vault"}, "docker.io/hashicorp/vault:1.20.1"),
	)
	r := checkArgoWorkloadImageDrift(context.Background(), kube, dyn)
	if r.Status != StatusWarn {
		t.Fatalf("expected warn for stuck rollout, got %s: %s", r.Status, r.Message)
	}
	for _, want := range []string{"platform-vault", "declared 2.0.3", "running 1.20.1"} {
		if !strings.Contains(r.Message, want) {
			t.Fatalf("expected %q in message, got: %s", want, r.Message)
		}
	}
}

func TestCheckArgoWorkloadImageDrift_AdoptedPasses(t *testing.T) {
	app := newApplication("platform-vault", []map[string]any{
		{"kind": "StatefulSet", "namespace": "vault", "name": "platform-vault", "group": "apps", "version": "v1"},
	})
	dyn := newDynamicApps(app)
	kube := fake.NewSimpleClientset(
		newStatefulSetWithImage("vault", "platform-vault", "hashicorp/vault:2.0.3", map[string]string{"app": "vault"}),
		newRunningPod("vault", "platform-vault-0", map[string]string{"app": "vault"}, "docker.io/hashicorp/vault:2.0.3"),
	)
	r := checkArgoWorkloadImageDrift(context.Background(), kube, dyn)
	if r.Status != StatusPass {
		t.Fatalf("expected pass once the pod runs the declared tag, got %s: %s", r.Status, r.Message)
	}
}

// containerd canonicalizes unqualified Docker Hub references on report-back;
// without normalizing that away, every unqualified image would look
// permanently drifted.
func TestCheckArgoWorkloadImageDrift_RegistryNormalizationAvoidsFalsePositive(t *testing.T) {
	app := newApplication("platform-litellm-redis", []map[string]any{
		{"kind": "Deployment", "namespace": "ai-sre", "name": "platform-litellm-redis", "group": "apps", "version": "v1"},
	})
	dyn := newDynamicApps(app)
	kube := fake.NewSimpleClientset(
		newDeploymentWithImage("ai-sre", "platform-litellm-redis", "redis:7-alpine", map[string]string{"app": "redis"}),
		newRunningPod("ai-sre", "platform-litellm-redis-0", map[string]string{"app": "redis"}, "docker.io/library/redis:7-alpine"),
	)
	r := checkArgoWorkloadImageDrift(context.Background(), kube, dyn)
	if r.Status != StatusPass {
		t.Fatalf("expected pass, registry-prefix normalization should treat these as the same image, got %s: %s", r.Status, r.Message)
	}
}

// A scaled-to-zero workload has no pods to observe; that is unverifiable, not
// drifted, and must not be reported as either failing or falsely confirmed.
func TestCheckArgoWorkloadImageDrift_NoPodsIsUnverifiedNotFailed(t *testing.T) {
	app := newApplication("platform-democratic-csi", []map[string]any{
		{"kind": "Deployment", "namespace": "democratic-csi", "name": "platform-democratic-csi", "group": "apps", "version": "v1"},
	})
	dyn := newDynamicApps(app)
	kube := fake.NewSimpleClientset(
		newDeploymentWithImage("democratic-csi", "platform-democratic-csi", "ghcr.io/democratic-csi/democratic-csi:latest", map[string]string{"app": "democratic-csi"}),
	)
	r := checkArgoWorkloadImageDrift(context.Background(), kube, dyn)
	if r.Status != StatusPass {
		t.Fatalf("expected pass when there is nothing running to verify, got %s: %s", r.Status, r.Message)
	}
}

func TestCheckArgoWorkloadImageDrift_NoApplicationsIsDefinitive(t *testing.T) {
	dyn := newDynamicApps()
	kube := fake.NewSimpleClientset()
	r := checkArgoWorkloadImageDrift(context.Background(), kube, dyn)
	if r.Status != StatusPass {
		t.Fatalf("expected pass, got %s: %s", r.Status, r.Message)
	}
	if r.Message == "" {
		t.Fatal("empty result must state the zero case explicitly")
	}
}

func TestSplitImageRef(t *testing.T) {
	cases := map[string][2]string{
		"hashicorp/vault:2.0.3":                                {"hashicorp/vault", "2.0.3"},
		"docker.io/hashicorp/vault:2.0.3":                      {"hashicorp/vault", "2.0.3"},
		"docker.io/library/redis:7-alpine":                     {"redis", "7-alpine"},
		"registry.k8s.io/metrics-server/metrics-server:v0.8.1": {"registry.k8s.io/metrics-server/metrics-server", "v0.8.1"},
		"blockloop/vault-unseal@sha256:41bcab66bb0759f8b3887b7b2f22a282d5ff3c6e2fc7829eeb2d1c98e3870c62": {"blockloop/vault-unseal", ""},
		"myregistry:5000/app:v1": {"myregistry:5000/app", "v1"},
	}
	for in, want := range cases {
		repo, tag := splitImageRef(in)
		if repo != want[0] || tag != want[1] {
			t.Fatalf("splitImageRef(%q) = (%q, %q), want (%q, %q)", in, repo, tag, want[0], want[1])
		}
	}
}

// --- checkLimitRangeAdoption ---

func newLimitRange(ns, name string, created time.Time, defaultRequestMemory string) *corev1.LimitRange {
	return &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, CreationTimestamp: metav1.NewTime(created)},
		Spec: corev1.LimitRangeSpec{
			Limits: []corev1.LimitRangeItem{
				{
					Type:           corev1.LimitTypeContainer,
					DefaultRequest: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(defaultRequestMemory)},
				},
			},
		},
	}
}

func newPodWithResources(ns, name string, created time.Time, containerName string, res corev1.ResourceRequirements) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, CreationTimestamp: metav1.NewTime(created)},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: containerName, Resources: res}},
		},
	}
}

// This mirrors the live longhorn-system state that motivated the check:
// engine-image pods created weeks before longhorn-defaults existed still run
// with no memory request at all, and nothing about their current state flags
// that on its own.
func TestCheckLimitRangeAdoption_PredatingPodMissingDefaultWarns(t *testing.T) {
	lrCreated := time.Date(2026, 7, 26, 0, 19, 44, 0, time.UTC)
	podCreated := time.Date(2026, 6, 29, 2, 7, 2, 0, time.UTC) // predates the LimitRange
	kube := fake.NewSimpleClientset(
		newLimitRange("longhorn-system", "longhorn-defaults", lrCreated, "64Mi"),
		newPodWithResources("longhorn-system", "engine-image-ei-75a03ec3-jb28n", podCreated, "engine-image", corev1.ResourceRequirements{}),
	)
	r := checkLimitRangeAdoption(context.Background(), kube)
	if r.Status != StatusWarn {
		t.Fatalf("expected warn for pod predating the LimitRange with no memory request, got %s: %s", r.Status, r.Message)
	}
	for _, want := range []string{"engine-image-ei-75a03ec3-jb28n", "requests.memory", "longhorn-defaults"} {
		if !strings.Contains(r.Message, want) {
			t.Fatalf("expected %q in message, got: %s", want, r.Message)
		}
	}
}

func TestCheckLimitRangeAdoption_PostLimitRangePodPasses(t *testing.T) {
	lrCreated := time.Date(2026, 7, 26, 0, 19, 44, 0, time.UTC)
	podCreated := time.Date(2026, 7, 26, 5, 33, 10, 0, time.UTC) // after the LimitRange, correctly defaulted
	kube := fake.NewSimpleClientset(
		newLimitRange("longhorn-system", "longhorn-defaults", lrCreated, "64Mi"),
		newPodWithResources("longhorn-system", "engine-image-ei-a4d05f02-c47qp", podCreated, "engine-image", corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")},
		}),
	)
	r := checkLimitRangeAdoption(context.Background(), kube)
	if r.Status != StatusPass {
		t.Fatalf("expected pass, got %s: %s", r.Status, r.Message)
	}
}

// A pod that predates the LimitRange but had an explicit request set anyway
// is not drift — the operator asked for that value on purpose.
func TestCheckLimitRangeAdoption_PredatingPodWithExplicitValuePasses(t *testing.T) {
	lrCreated := time.Date(2026, 7, 26, 0, 19, 44, 0, time.UTC)
	podCreated := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	kube := fake.NewSimpleClientset(
		newLimitRange("longhorn-system", "longhorn-defaults", lrCreated, "64Mi"),
		newPodWithResources("longhorn-system", "manual-pod", podCreated, "app", corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi")},
		}),
	)
	r := checkLimitRangeAdoption(context.Background(), kube)
	if r.Status != StatusPass {
		t.Fatalf("expected pass, explicit request predates but satisfies the default, got %s: %s", r.Status, r.Message)
	}
}

func TestCheckLimitRangeAdoption_NoLimitRangesPasses(t *testing.T) {
	r := checkLimitRangeAdoption(context.Background(), fake.NewSimpleClientset())
	if r.Status != StatusPass {
		t.Fatalf("expected pass, got %s: %s", r.Status, r.Message)
	}
	if r.Message == "" {
		t.Fatal("empty result must state the zero case explicitly")
	}
}

// A pod-scoped LimitRange caps sums across a pod, not any one container's
// spec, so it defaults nothing at the container level and must not be
// misread as satisfied or violated.
func TestCheckLimitRangeAdoption_PodScopedLimitRangeIsNotContainerDefault(t *testing.T) {
	lr := &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{Name: "pod-caps", Namespace: "team-a", CreationTimestamp: metav1.NewTime(time.Now())},
		Spec: corev1.LimitRangeSpec{
			Limits: []corev1.LimitRangeItem{
				{Type: corev1.LimitTypePod, Max: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("4Gi")}},
			},
		},
	}
	kube := fake.NewSimpleClientset(
		lr,
		newPodWithResources("team-a", "old-pod", time.Now().Add(-time.Hour), "app", corev1.ResourceRequirements{}),
	)
	r := checkLimitRangeAdoption(context.Background(), kube)
	if r.Status != StatusPass {
		t.Fatalf("expected pass, pod-scoped LimitRange has no container defaults to check, got %s: %s", r.Status, r.Message)
	}
}
