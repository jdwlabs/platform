package heal

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestTerminateStuckSync(t *testing.T) {
	app := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "Application",
			"metadata": map[string]interface{}{
				"name":      "platform-nginx-gateway-fabric",
				"namespace": "argocd",
			},
			"operation": map[string]interface{}{
				"sync": map[string]interface{}{
					"revision": "abc123",
				},
			},
		},
	}
	scheme := runtime.NewScheme()
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}: "ApplicationList",
		},
		app,
	)

	if err := TerminateStuckSync(context.Background(), dc, "platform-nginx-gateway-fabric"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := dc.Resource(appGVR).Namespace("argocd").
		Get(context.Background(), "platform-nginx-gateway-fabric", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get after patch: %v", err)
	}
	op, found, err := unstructured.NestedMap(got.Object, "operation")
	if err != nil {
		t.Fatalf("nested lookup: %v", err)
	}
	if found && op != nil {
		t.Fatalf("expected operation cleared, got: %v", op)
	}
}

func TestTerminateStuckSync_Missing(t *testing.T) {
	scheme := runtime.NewScheme()
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}: "ApplicationList",
		},
	)
	if err := TerminateStuckSync(context.Background(), dc, "does-not-exist"); err == nil {
		t.Fatalf("expected error when application absent")
	}
}
