package k8s

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestFakeClient_PreSeeded(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "argocd"}}
	c := NewFake(ns)
	got, err := c.CoreV1().Namespaces().Get(context.Background(), "argocd", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "argocd" {
		t.Fatalf("got %s", got.Name)
	}
}
