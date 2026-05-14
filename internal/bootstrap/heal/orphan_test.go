package heal

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func ns(name string, labels map[string]string) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
	}
}

func TestDeleteOrphanNamespaces_DeletesOrphans(t *testing.T) {
	tenantLabel := map[string]string{"jdwlabs.io/tenant": "demo"}
	c := fake.NewSimpleClientset([]runtime.Object{
		ns("demo-app", tenantLabel),   // orphan: in cluster, not in allowed set
		ns("demo-keep", tenantLabel),  // allowed
		ns("kube-system", tenantLabel), // system: protected
	}...)

	allowed := map[string]bool{"demo-keep": true}
	deleted, err := DeleteOrphanNamespaces(context.Background(), c, allowed)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(deleted) != 1 || deleted[0] != "demo-app" {
		t.Fatalf("expected [demo-app], got %v", deleted)
	}
}

func TestDeleteOrphanNamespaces_NoTenantLabel_Skipped(t *testing.T) {
	c := fake.NewSimpleClientset([]runtime.Object{
		ns("unlabeled", nil),
	}...)
	deleted, err := DeleteOrphanNamespaces(context.Background(), c, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(deleted) != 0 {
		t.Fatalf("expected empty, got %v", deleted)
	}
}
