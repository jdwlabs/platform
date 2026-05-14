package heal

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func dynFakeWithAppSet() *dynamicfake.FakeDynamicClient {
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
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{appsetGVR: "ApplicationSetList"},
		obj,
	)
}

func TestStripStuck_KnownKind(t *testing.T) {
	dc := dynFakeWithAppSet()
	opts := StuckOptions{Namespace: "argocd", Kind: "applicationset", Name: "platform-services"}
	if err := StripStuck(context.Background(), dc, opts); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestStripStuck_UnknownKind(t *testing.T) {
	dc := dynFakeWithAppSet()
	opts := StuckOptions{Namespace: "argocd", Kind: "widget", Name: "foo"}
	if err := StripStuck(context.Background(), dc, opts); err == nil {
		t.Fatalf("expected error for unknown kind")
	}
}
