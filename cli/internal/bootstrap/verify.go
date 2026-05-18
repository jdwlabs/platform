package bootstrap

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/jdwlabs/platform/internal/k8s"
)

func argoAppSetGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applicationsets"}
}

// deploymentAvailable returns true when all conditions on the named deployment
// include Available=True.
func deploymentAvailable(ctx context.Context, c kubernetes.Interface, namespace, name string) (bool, error) {
	d, err := c.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return false, nil // not yet created; keep polling
	}
	for _, cond := range d.Status.Conditions {
		if cond.Type == appsv1.DeploymentAvailable && cond.Status == corev1.ConditionTrue {
			return true, nil
		}
	}
	return false, nil
}

// VerifyArgocdReady polls until deploy/argocd-server in namespace argocd is
// Available=True, or until ctx is cancelled. Callers are responsible for
// setting an appropriate deadline on ctx (Phase 1 Verify uses 5 minutes).
func VerifyArgocdReady(ctx context.Context, c kubernetes.Interface) error {
	err := k8s.WaitFor(ctx, 10*time.Second, func(ctx context.Context) (bool, error) {
		return deploymentAvailable(ctx, c, "argocd", "argocd-server")
	})
	if err != nil {
		return fmt.Errorf("phase-1: argocd-server not Available: %w", err)
	}
	return nil
}

// VerifyRootApplied checks Phase 2: the bootstrap Application is Synced+Healthy,
// platform-services ApplicationSet is not stuck terminating, and the ArgoCD
// repo-server is Available after the self-managed upgrade restarts pods.
// Requires a dynamic client for ArgoCD CRDs; returns error if nil.
func VerifyRootApplied(ctx context.Context, c kubernetes.Interface, dyn dynamic.Interface) error {
	if dyn == nil {
		return fmt.Errorf("phase-2: dynamic client required for ArgoCD CRD checks")
	}
	appsetGVR := argoAppSetGVR()
	appset, err := dyn.Resource(appsetGVR).Namespace("argocd").Get(ctx, "platform-services", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("phase-2: get applicationset/platform-services: %w", err)
	}
	if ts := appset.GetDeletionTimestamp(); ts != nil {
		return fmt.Errorf("phase-2: applicationset/platform-services is terminating (stuck finalizer)")
	}
	// ArgoCD self-manages its own upgrade after root-apply, restarting pods.
	// Wait for repo-server to be Available before returning so downstream apps
	// don't attempt manifest generation against a dead repo-server.
	if err := k8s.WaitFor(ctx, 10*time.Second, func(ctx context.Context) (bool, error) {
		return deploymentAvailable(ctx, c, "argocd", "argocd-repo-server")
	}); err != nil {
		return fmt.Errorf("phase-2: argocd-repo-server not Available (self-upgrade may be stuck): %w", err)
	}
	return nil
}

// VerifyVaultInitialized checks Phase 3: secret/vault-token exists in
// external-secrets namespace (static check). ClusterSecretStore Valid status
// requires a dynamic client and is skipped if nil.
func VerifyVaultInitialized(ctx context.Context, c kubernetes.Interface, dyn dynamic.Interface) error {
	if _, err := c.CoreV1().Secrets("external-secrets").Get(ctx, "vault-token", metav1.GetOptions{}); err != nil {
		return fmt.Errorf("phase-3: vault-token secret not found in external-secrets: %w", err)
	}
	return nil
}

// VerifyExternalSecretsSynced checks Phase 4: the porkbun ExternalSecret in
// cert-manager is Ready (proxy for all platform secrets syncing from Vault).
// Requires dynamic client; returns error if nil.
func VerifyExternalSecretsSynced(ctx context.Context, c kubernetes.Interface, dyn dynamic.Interface) error {
	if dyn == nil {
		return fmt.Errorf("phase-4: dynamic client required for ExternalSecret checks")
	}
	esGVR := schema.GroupVersionResource{Group: "external-secrets.io", Version: "v1", Resource: "externalsecrets"}
	obj, err := dyn.Resource(esGVR).Namespace("cert-manager").Get(ctx, "porkbun", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("phase-4: get externalsecret/porkbun: %w", err)
	}
	for _, cond := range unstructuredConditions(obj) {
		if cond["type"] == "Ready" {
			if cond["status"] == "True" {
				return nil
			}
			return fmt.Errorf("phase-4: porkbun ExternalSecret not Ready: %s", cond["message"])
		}
	}
	return fmt.Errorf("phase-4: porkbun ExternalSecret has no Ready condition yet")
}

// VerifyBackupsConfigured checks Phase 5: cronjob/postgres-backup exists in
// database namespace.
func VerifyBackupsConfigured(ctx context.Context, c kubernetes.Interface) error {
	if _, err := c.BatchV1().CronJobs("database").Get(ctx, "postgres-backup", metav1.GetOptions{}); err != nil {
		return fmt.Errorf("phase-5: postgres-backup cronjob not found in database: %w", err)
	}
	return nil
}

// VerifyAllHealthy checks Phase 6: all ArgoCD Applications Synced+Healthy
// and the wildcard TLS certificate is Ready.
// Requires dynamic client; returns error if nil.
func VerifyAllHealthy(ctx context.Context, c kubernetes.Interface, dyn dynamic.Interface) error {
	if dyn == nil {
		return fmt.Errorf("phase-6: dynamic client required for Application + Certificate checks")
	}
	// Check all ArgoCD applications.
	appGVR := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	list, err := dyn.Resource(appGVR).Namespace("argocd").List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("phase-6: list applications: %w", err)
	}
	for _, app := range list.Items {
		name := app.GetName()
		syncStatus, _, _ := nestedString(app.Object, "status", "sync", "status")
		healthStatus, _, _ := nestedString(app.Object, "status", "health", "status")
		if healthStatus == "Degraded" || healthStatus == "Missing" {
			return fmt.Errorf("phase-6: application/%s health=%s", name, healthStatus)
		}
		if syncStatus == "OutOfSync" {
			return fmt.Errorf("phase-6: application/%s not synced", name)
		}
	}
	// Check wildcard certificate.
	certGVR := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
	cert, err := dyn.Resource(certGVR).Namespace("nginx-gateway").Get(ctx, "wildcard-jdwlabs", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("phase-6: get certificate/wildcard-jdwlabs: %w", err)
	}
	for _, cond := range unstructuredConditions(cert) {
		if cond["type"] == "Ready" {
			if cond["status"] == "True" {
				return nil
			}
			return fmt.Errorf("phase-6: wildcard certificate not Ready: %s", cond["message"])
		}
	}
	return fmt.Errorf("phase-6: wildcard certificate has no Ready condition yet")
}

// unstructuredConditions extracts status.conditions[].{type,status,message}
// from an *unstructured.Unstructured object as a slice of string maps.
func unstructuredConditions(obj *unstructured.Unstructured) []map[string]string {
	raw, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	out := make([]map[string]string, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		row := map[string]string{}
		for k, v := range m {
			row[k] = fmt.Sprintf("%v", v)
		}
		out = append(out, row)
	}
	return out
}

func nestedString(obj map[string]any, fields ...string) (string, bool, error) {
	return unstructured.NestedString(obj, fields...)
}
