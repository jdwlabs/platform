package heal

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestRefreshCertApprover(t *testing.T) {
	app := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "Application",
			"metadata": map[string]interface{}{
				"name":      "kubelet-serving-cert-approver",
				"namespace": "argocd",
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

	if err := RefreshCertApprover(context.Background(), dc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRefreshCertApprover_Missing(t *testing.T) {
	scheme := runtime.NewScheme()
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}: "ApplicationList",
		},
	)
	if err := RefreshCertApprover(context.Background(), dc); err == nil {
		t.Fatalf("expected error when application absent")
	}
}
