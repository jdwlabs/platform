package bootstrap

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/jdwlabs/platform/internal/k8s"
)

// --- Phase 1: VerifyArgocdReady ---

func TestVerifyArgocdReady_OK(t *testing.T) {
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "argocd-server", Namespace: "argocd"},
		Status: appsv1.DeploymentStatus{
			Conditions: []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
			},
		},
	}
	c := k8s.NewFake(deploy)
	if err := VerifyArgocdReady(context.Background(), c); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestVerifyArgocdReady_NotAvailable(t *testing.T) {
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "argocd-server", Namespace: "argocd"},
	}
	c := k8s.NewFake(deploy)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := VerifyArgocdReady(ctx, c); err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestVerifyArgocdReady_Missing(t *testing.T) {
	c := k8s.NewFake()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := VerifyArgocdReady(ctx, c); err == nil {
		t.Fatalf("expected error, got nil")
	}
}

// --- Phase 2: VerifyRootApplied ---

func TestVerifyRootApplied_NilDynamic_Errors(t *testing.T) {
	c := k8s.NewFake()
	if err := VerifyRootApplied(context.Background(), c, nil); err == nil {
		t.Fatalf("expected error with nil dynamic client")
	}
}

// --- Phase 3: VerifyVaultInitialized ---

func TestVerifyVaultInitialized_SecretPresent(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "vault-token", Namespace: "external-secrets"},
	}
	c := k8s.NewFake(secret)
	// nil dynamic → skips ClusterSecretStore check; static secret check passes
	if err := VerifyVaultInitialized(context.Background(), c, nil); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestVerifyVaultInitialized_SecretMissing(t *testing.T) {
	c := k8s.NewFake()
	if err := VerifyVaultInitialized(context.Background(), c, nil); err == nil {
		t.Fatalf("expected error")
	}
}

// --- Phase 4: VerifyExternalSecretsSynced ---

func TestVerifyExternalSecretsSynced_NilDynamic(t *testing.T) {
	c := k8s.NewFake()
	if err := VerifyExternalSecretsSynced(context.Background(), c, nil); err == nil {
		t.Fatalf("expected error with nil dynamic client")
	}
}

// --- Phase 5: VerifyBackupsConfigured ---

func TestVerifyBackupsConfigured_CronJobPresent(t *testing.T) {
	cj := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: "postgres-backup", Namespace: "database"},
	}
	c := k8s.NewFake(cj)
	if err := VerifyBackupsConfigured(context.Background(), c); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestVerifyBackupsConfigured_Missing(t *testing.T) {
	c := k8s.NewFake()
	if err := VerifyBackupsConfigured(context.Background(), c); err == nil {
		t.Fatalf("expected error")
	}
}

// --- Phase 6: VerifyAllHealthy ---

func TestVerifyAllHealthy_NilDynamic(t *testing.T) {
	c := k8s.NewFake()
	if err := VerifyAllHealthy(context.Background(), c, nil); err == nil {
		t.Fatalf("expected error with nil dynamic")
	}
}
