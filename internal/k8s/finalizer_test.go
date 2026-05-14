package k8s

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestStripFinalizers(t *testing.T) {
	appsetGVR := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applicationsets"}

	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "ApplicationSet",
			"metadata": map[string]interface{}{
				"name":       "platform-services",
				"namespace":  "argocd",
				"finalizers": []interface{}{"resources-finalizer.argocd.argoproj.io"},
			},
		},
	}

	scheme := runtime.NewScheme()
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{appsetGVR: "ApplicationSetList"},
		obj,
	)

	if err := StripFinalizers(context.Background(), dc, appsetGVR, "argocd", "platform-services"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
