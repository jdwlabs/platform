package cli

import (
	"bytes"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/jdwlabs/platform/internal/k8s"
)

func staticObjects() []runtime.Object {
	return []runtime.Object{
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "argocd-server", Namespace: "argocd"},
			Status: appsv1.DeploymentStatus{
				Conditions: []appsv1.DeploymentCondition{
					{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
				},
			},
		},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "argocd-repo-server", Namespace: "argocd"},
			Status: appsv1.DeploymentStatus{
				Conditions: []appsv1.DeploymentCondition{
					{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
				},
			},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "vault-token", Namespace: "external-secrets"},
		},
		&batchv1.CronJob{
			ObjectMeta: metav1.ObjectMeta{Name: "postgres-backup", Namespace: "database"},
		},
	}
}

func TestBootstrapVerify_AllGatesPass(t *testing.T) {
	kc := k8s.NewFake(staticObjects()...)

	appset := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "ApplicationSet",
			"metadata": map[string]interface{}{
				"name":      "platform-services",
				"namespace": "argocd",
			},
		},
	}
	scheme := runtime.NewScheme()
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			{Group: "argoproj.io", Version: "v1alpha1", Resource: "applicationsets"}: "ApplicationSetList",
		},
		appset,
	)

	root := NewRootForTest(kc, dc)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"bootstrap", "verify"})

	if err := root.Execute(); err != nil {
		t.Fatalf("expected all gates to pass: %v\noutput: %s", err, out.String())
	}
}

func TestBootstrapVerify_EmptyDynamic_Errors(t *testing.T) {
	kc := k8s.NewFake(staticObjects()...)

	// Empty fake: dynamic client that can't find any resources.
	// Verifies that missing ArgoCD resources cause gate failures.
	scheme := runtime.NewScheme()
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			{Group: "argoproj.io", Version: "v1alpha1", Resource: "applicationsets"}: "ApplicationSetList",
		},
		// no objects → ApplicationSet not found → gate 2 fails
	)

	root := NewRootForTest(kc, dc)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"bootstrap", "verify"})

	if err := root.Execute(); err == nil {
		t.Fatalf("expected error when ArgoCD resources missing, got nil\noutput: %s", out.String())
	}
}
