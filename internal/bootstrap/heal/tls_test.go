package heal

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func TestReissueTLS_DeletesOwned(t *testing.T) {
	owned := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-tls",
			Namespace: "default",
			Labels:    map[string]string{"cert-manager.io/certificate-name": "my-cert"},
		},
	}
	unowned := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "default"},
	}

	c := fake.NewSimpleClientset([]runtime.Object{owned, unowned}...)
	deleted, err := ReissueTLS(context.Background(), c, "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(deleted) != 1 || deleted[0] != "my-tls" {
		t.Fatalf("expected [my-tls], got %v", deleted)
	}
}

func TestReissueTLS_NoneOwned(t *testing.T) {
	c := fake.NewSimpleClientset()
	deleted, err := ReissueTLS(context.Background(), c, "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(deleted) != 0 {
		t.Fatalf("expected empty, got %v", deleted)
	}
}
