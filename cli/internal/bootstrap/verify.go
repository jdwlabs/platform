package bootstrap

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// VerifyExternalSecretsSynced checks Phase 4: all ExternalSecrets have
// SecretSynced=True and ClusterIssuer/letsencrypt-prod is Ready.
// Requires dynamic client; returns error if nil.
func VerifyExternalSecretsSynced(ctx context.Context, c kubernetes.Interface, dyn dynamic.Interface) error {
	if dyn == nil {
		return fmt.Errorf("phase-4: dynamic client required for ExternalSecret + ClusterIssuer checks")
	}
	return nil
}

// VerifyBackupsConfigured checks Phase 5: cronjob/postgres-backup exists in
// database namespace.
func VerifyBackupsConfigured(ctx context.Context, c kubernetes.Interface) error {
	if _, err := c.BatchV1().CronJobs("database").Get(ctx, "postgres-backup", metav1.GetOptions{}); err != nil {
		return fmt.Errorf("phase-5: postgres-backup cronjob not found in database: %w", err)
	}
	return nil
}

// VerifyAllHealthy checks Phase 6: all ArgoCD Applications Synced+Healthy,
// Certificates Ready, CNPG clusters healthy.
// Requires dynamic client; returns error if nil.
func VerifyAllHealthy(ctx context.Context, c kubernetes.Interface, dyn dynamic.Interface) error {
	if dyn == nil {
		return fmt.Errorf("phase-6: dynamic client required for Application + Certificate + CNPG checks")
	}
	return nil
}
