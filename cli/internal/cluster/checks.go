package cluster

import (
	"context"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

var (
	gvrGatewayClass       = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gatewayclasses"}
	gvrGateway            = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"}
	gvrHTTPRoute          = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}
	gvrClusterSecretStore = schema.GroupVersionResource{Group: "external-secrets.io", Version: "v1", Resource: "clustersecretstores"}
	gvrExternalSecret     = schema.GroupVersionResource{Group: "external-secrets.io", Version: "v1", Resource: "externalsecrets"}
	gvrCertificate        = schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
	gvrApplication        = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
)

// AllChecks returns the full ordered health check list for the platform cluster.
func AllChecks(kube kubernetes.Interface, dyn dynamic.Interface) []Check {
	var checks []Check
	checks = append(checks, operatorChecks(kube, dyn)...)
	checks = append(checks, vaultChecks(kube, dyn)...)
	checks = append(checks, secretChecks(kube, dyn)...)
	checks = append(checks, tlsChecks(kube, dyn)...)
	checks = append(checks, argocdChecks(kube, dyn)...)
	return checks
}

// --- Layer 1: Operators ---

func operatorChecks(kube kubernetes.Interface, dyn dynamic.Interface) []Check {
	return []Check{
		{Layer: 1, Group: "Operators", Name: "argocd-server", Run: func(ctx context.Context) Result {
			return checkDeploymentByWorkloadName(ctx, kube, "argocd", "argocd-server")
		}},
		{Layer: 1, Group: "Operators", Name: "external-secrets", Run: func(ctx context.Context) Result {
			return checkDeploymentByWorkloadName(ctx, kube, "external-secrets", "external-secrets")
		}},
		{Layer: 1, Group: "Operators", Name: "cert-manager", Run: func(ctx context.Context) Result {
			return checkDeploymentByWorkloadName(ctx, kube, "cert-manager", "cert-manager")
		}},
		{Layer: 1, Group: "Operators", Name: "nginx-gateway-fabric", Run: func(ctx context.Context) Result {
			return checkGatewayClassAccepted(ctx, dyn, "nginx")
		}},
		{Layer: 1, Group: "Operators", Name: "longhorn", Run: func(ctx context.Context) Result {
			return checkStorageClassExists(ctx, kube, "longhorn")
		}},
		{Layer: 1, Group: "Operators", Name: "vault-pod", Run: func(ctx context.Context) Result {
			return checkVaultPodRunning(ctx, kube)
		}},
	}
}

// --- Layer 2: Vault init state ---

func vaultChecks(kube kubernetes.Interface, dyn dynamic.Interface) []Check {
	return []Check{
		{Layer: 2, Group: "Vault", Name: "vault-init-secret", Run: func(ctx context.Context) Result {
			return checkSecretExists(ctx, kube, "vault", "vault-init")
		}},
		{Layer: 2, Group: "Vault", Name: "vault-token-secret", Run: func(ctx context.Context) Result {
			return checkSecretExists(ctx, kube, "external-secrets", "vault-token")
		}},
		{Layer: 2, Group: "Vault", Name: "cluster-secret-store", Run: func(ctx context.Context) Result {
			return checkClusterSecretStoreReady(ctx, dyn, "vault")
		}},
	}
}

// --- Layer 3: ExternalSecrets ---

func secretChecks(kube kubernetes.Interface, dyn dynamic.Interface) []Check {
	type target struct{ name, ns string }
	targets := []target{
		{"porkbun", "cert-manager"},
		{"longhorn", "longhorn-system"},
		{"grafana-admin-credentials", "monitoring"},
		{"alertmanager-config", "monitoring"},
		{"rclone-gdrive", "database"},
	}
	checks := make([]Check, len(targets))
	for i, t := range targets {
		t := t
		checks[i] = Check{
			Layer: 3,
			Group: "Secrets",
			Name:  t.name + "/" + t.ns,
			Run: func(ctx context.Context) Result {
				return checkExternalSecretSynced(ctx, dyn, t.ns, t.name)
			},
		}
	}
	return checks
}

// --- Layer 4: TLS / Gateway routing ---

func tlsChecks(kube kubernetes.Interface, dyn dynamic.Interface) []Check {
	return []Check{
		{Layer: 4, Group: "TLS/Routing", Name: "cert/wildcard-jdwlabs", Run: func(ctx context.Context) Result {
			return checkCertificateReady(ctx, dyn, "nginx-gateway", "wildcard-jdwlabs")
		}},
		{Layer: 4, Group: "TLS/Routing", Name: "gateway/platform-gateway", Run: func(ctx context.Context) Result {
			return checkGatewayProgrammed(ctx, dyn, "nginx-gateway", "platform-gateway")
		}},
		{Layer: 4, Group: "TLS/Routing", Name: "httproute/vault", Run: func(ctx context.Context) Result {
			return checkHTTPRouteAccepted(ctx, dyn, "vault", "vault")
		}},
		{Layer: 4, Group: "TLS/Routing", Name: "httproute/argocd", Run: func(ctx context.Context) Result {
			return checkHTTPRouteAccepted(ctx, dyn, "argocd", "argocd")
		}},
		{Layer: 4, Group: "TLS/Routing", Name: "httproute/http-redirect", Run: func(ctx context.Context) Result {
			return checkHTTPRouteAccepted(ctx, dyn, "nginx-gateway", "http-to-https-redirect")
		}},
	}
}

// --- Layer 5: ArgoCD applications ---

func argocdChecks(kube kubernetes.Interface, dyn dynamic.Interface) []Check {
	return []Check{
		{Layer: 5, Group: "ArgoCD", Name: "applications", Run: func(ctx context.Context) Result {
			return checkAllApplicationsHealthy(ctx, dyn)
		}},
	}
}

// --- Check implementations ---

// checkDeploymentByWorkloadName finds the Deployment in ns with the
// app.kubernetes.io/name=<workloadName> label and reports its Available
// condition. This decouples the probe from Helm release naming, so charts
// installed with arbitrary release prefixes (e.g. "platform-cert-manager")
// still match.
func checkDeploymentByWorkloadName(ctx context.Context, kube kubernetes.Interface, ns, workloadName string) Result {
	list, err := kube.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/name=" + workloadName,
	})
	if k8serrors.IsNotFound(err) {
		return Failf("namespace %s not found", ns)
	}
	if err != nil {
		return Failf("list error: %v", err)
	}
	if len(list.Items) == 0 {
		return Failf("no Deployment with app.kubernetes.io/name=%s in ns %s", workloadName, ns)
	}
	d := list.Items[0]
	for _, cond := range d.Status.Conditions {
		if cond.Type == appsv1.DeploymentAvailable && cond.Status == corev1.ConditionTrue {
			return Passf("Available (%d/%d ready)", d.Status.ReadyReplicas, d.Status.Replicas)
		}
	}
	return Failf("not Available (%d/%d ready)", d.Status.ReadyReplicas, d.Status.Replicas)
}

func checkSecretExists(ctx context.Context, kube kubernetes.Interface, ns, name string) Result {
	_, err := kube.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return Fail("secret not found")
	}
	if err != nil {
		return Failf("get error: %v", err)
	}
	return Pass("present")
}

func checkStorageClassExists(ctx context.Context, kube kubernetes.Interface, name string) Result {
	_, err := kube.StorageV1().StorageClasses().Get(ctx, name, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return Fail("StorageClass not found")
	}
	if err != nil {
		return Failf("get error: %v", err)
	}
	return Pass("StorageClass present")
}

func checkVaultPodRunning(ctx context.Context, kube kubernetes.Interface) Result {
	pods, err := kube.CoreV1().Pods("vault").List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/name=vault",
	})
	if k8serrors.IsNotFound(err) {
		return Fail("vault namespace not found")
	}
	if err != nil {
		return Failf("list pods: %v", err)
	}
	if len(pods.Items) == 0 {
		return Fail("no vault pods found")
	}
	for _, pod := range pods.Items {
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.State.Waiting != nil && cs.State.Waiting.Reason == "CrashLoopBackOff" {
				return Failf("pod %s: CrashLoopBackOff", pod.Name)
			}
		}
		if pod.Status.Phase == corev1.PodRunning {
			for _, cs := range pod.Status.ContainerStatuses {
				if cs.State.Running != nil {
					return Passf("pod %s: Running", pod.Name)
				}
			}
		}
	}
	pod := pods.Items[0]
	return Warnf("pod %s: phase=%s (initializing)", pod.Name, pod.Status.Phase)
}

func checkGatewayClassAccepted(ctx context.Context, dyn dynamic.Interface, name string) Result {
	obj, err := dyn.Resource(gvrGatewayClass).Get(ctx, name, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return Fail("GatewayClass not found")
	}
	if err != nil {
		return Failf("get error: %v", err)
	}
	ok, msg := conditionStatus(obj, "Accepted")
	if ok {
		return Pass("Accepted")
	}
	return Failf("not Accepted: %s", msg)
}

func checkGatewayProgrammed(ctx context.Context, dyn dynamic.Interface, ns, name string) Result {
	obj, err := dyn.Resource(gvrGateway).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return Fail("Gateway not found")
	}
	if err != nil {
		return Failf("get error: %v", err)
	}
	ok, msg := conditionStatus(obj, "Programmed")
	if ok {
		return Pass("Programmed")
	}
	return Failf("not Programmed: %s", msg)
}

func checkHTTPRouteAccepted(ctx context.Context, dyn dynamic.Interface, ns, name string) Result {
	obj, err := dyn.Resource(gvrHTTPRoute).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return Fail("HTTPRoute not found")
	}
	if err != nil {
		return Failf("get error: %v", err)
	}
	parents, _, _ := unstructured.NestedSlice(obj.Object, "status", "parents")
	for _, p := range parents {
		parent, ok := p.(map[string]any)
		if !ok {
			continue
		}
		conditions, ok := parent["conditions"].([]any)
		if !ok {
			continue
		}
		for _, c := range conditions {
			cond, ok := c.(map[string]any)
			if !ok {
				continue
			}
			if cond["type"] == "Accepted" {
				if cond["status"] == "True" {
					return Pass("Accepted")
				}
				msg, _ := cond["message"].(string)
				reason, _ := cond["reason"].(string)
				return Failf("not Accepted (%s): %s", reason, msg)
			}
		}
	}
	return Warn("no parent status yet")
}

func checkClusterSecretStoreReady(ctx context.Context, dyn dynamic.Interface, name string) Result {
	obj, err := dyn.Resource(gvrClusterSecretStore).Get(ctx, name, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return Fail("ClusterSecretStore not found")
	}
	if err != nil {
		return Failf("get error: %v", err)
	}
	ok, msg := conditionStatus(obj, "Ready")
	if ok {
		return Pass("Ready")
	}
	return Failf("not Ready: %s", msg)
}

func checkExternalSecretSynced(ctx context.Context, dyn dynamic.Interface, ns, name string) Result {
	obj, err := dyn.Resource(gvrExternalSecret).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return Fail("ExternalSecret not found")
	}
	if err != nil {
		return Failf("get error: %v", err)
	}
	ok, msg := conditionStatus(obj, "Ready")
	if ok {
		return Pass("Synced")
	}
	return Failf("not synced: %s", msg)
}

func checkCertificateReady(ctx context.Context, dyn dynamic.Interface, ns, name string) Result {
	obj, err := dyn.Resource(gvrCertificate).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return Fail("Certificate not found")
	}
	if err != nil {
		return Failf("get error: %v", err)
	}
	ok, msg := conditionStatus(obj, "Ready")
	if ok {
		return Pass("Ready")
	}
	return Warnf("not Ready: %s", msg)
}

func checkAllApplicationsHealthy(ctx context.Context, dyn dynamic.Interface) Result {
	list, err := dyn.Resource(gvrApplication).Namespace("argocd").List(ctx, metav1.ListOptions{})
	if err != nil {
		return Failf("list applications: %v", err)
	}
	var degraded, unsynced []string
	for _, item := range list.Items {
		name := item.GetName()
		syncStatus, _, _ := unstructured.NestedString(item.Object, "status", "sync", "status")
		healthStatus, _, _ := unstructured.NestedString(item.Object, "status", "health", "status")
		if healthStatus == "Degraded" || healthStatus == "Missing" {
			degraded = append(degraded, name+"("+healthStatus+")")
		} else if syncStatus == "OutOfSync" {
			unsynced = append(unsynced, name)
		}
	}
	total := len(list.Items)
	if len(degraded) > 0 {
		return Failf("%d/%d degraded: %s", len(degraded), total, strings.Join(degraded, ", "))
	}
	if len(unsynced) > 0 {
		return Warnf("%d/%d out-of-sync: %s", len(unsynced), total, strings.Join(unsynced, ", "))
	}
	return Passf("all %d apps Synced+Healthy", total)
}

// conditionStatus reads status.conditions[type=condType] from an unstructured object.
// Returns (true, "") when status==True, or (false, message) otherwise.
func conditionStatus(obj *unstructured.Unstructured, condType string) (bool, string) {
	conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, c := range conditions {
		cond, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if cond["type"] == condType {
			msg, _ := cond["message"].(string)
			return cond["status"] == "True", msg
		}
	}
	return false, "condition not found"
}
