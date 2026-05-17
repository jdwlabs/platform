package bootstrap

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/jdwlabs/platform/internal/k8s"
)

func bootstrapApp(sync, health string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]interface{}{"name": "bootstrap", "namespace": "argocd"},
		"status": map[string]interface{}{
			"sync":   map[string]interface{}{"status": sync},
			"health": map[string]interface{}{"status": health},
		},
	}}
}

func newFakeDynamic(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}:    "ApplicationList",
			{Group: "argoproj.io", Version: "v1alpha1", Resource: "applicationsets"}: "ApplicationSetList",
		},
		objs...,
	)
}

func TestRootApplyDetect_HealthyOutOfSync_AlreadyDone(t *testing.T) {
	// OutOfSync+Healthy (e.g. Helm-chart CRD version drift) must be treated as done.
	dc := newFakeDynamic(bootstrapApp("OutOfSync", "Healthy"))
	kc := k8s.NewFake()
	p := NewRootApplyPhase(kc, dc, "", "bootstrap/root-app.yaml")
	st, err := p.Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st != StateAlreadyDone {
		t.Fatalf("got %s, want StateAlreadyDone (Healthy is sufficient)", st)
	}
}

func TestRootApplyDetect_SyncedHealthy_AlreadyDone(t *testing.T) {
	dc := newFakeDynamic(bootstrapApp("Synced", "Healthy"))
	kc := k8s.NewFake()
	p := NewRootApplyPhase(kc, dc, "", "bootstrap/root-app.yaml")
	st, err := p.Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st != StateAlreadyDone {
		t.Fatalf("got %s, want StateAlreadyDone", st)
	}
}

func TestRootApplyDetect_Degraded_InProgress(t *testing.T) {
	dc := newFakeDynamic(bootstrapApp("OutOfSync", "Degraded"))
	kc := k8s.NewFake()
	p := NewRootApplyPhase(kc, dc, "", "bootstrap/root-app.yaml")
	st, err := p.Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st != StateInProgress {
		t.Fatalf("got %s, want StateInProgress", st)
	}
	if msg := p.ProgressMessage(context.Background()); msg == "" {
		t.Fatal("expected ProgressMessage when degraded")
	}
}

func TestRootApplyDetect_StuckAppSet_Broken(t *testing.T) {
	app := bootstrapApp("Synced", "Healthy")
	now := metav1.Now()
	appset := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "ApplicationSet",
		"metadata": map[string]interface{}{
			"name":              "platform-services",
			"namespace":         "argocd",
			"deletionTimestamp": now.UTC().Format("2006-01-02T15:04:05Z"),
			"finalizers":        []interface{}{"resources-finalizer.argocd.argoproj.io"},
		},
	}}
	dc := newFakeDynamic(app, appset)
	kc := k8s.NewFake()
	p := NewRootApplyPhase(kc, dc, "", "bootstrap/root-app.yaml")
	st, err := p.Detect(context.Background())
	if err == nil {
		t.Fatal("expected error for stuck AppSet")
	}
	if st != StateBroken {
		t.Fatalf("got %s, want StateBroken", st)
	}
}

func TestPatchRootAppBranch(t *testing.T) {
	in := []byte(`apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: bootstrap
  namespace: argocd
spec:
  source:
    targetRevision: main
`)
	out, err := PatchRootAppBranch(in, "feature/x")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "feature/x") {
		t.Fatalf("branch not patched:\n%s", out)
	}
	if strings.Contains(string(out), "targetRevision: main") {
		t.Fatalf("old branch still present:\n%s", out)
	}
}

func TestPatchRootAppBranch_PreservesOtherFields(t *testing.T) {
	in := []byte(`apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: bootstrap
  namespace: argocd
spec:
  destination:
    server: https://kubernetes.default.svc
  source:
    repoURL: https://github.com/jdwlabs/platform.git
    targetRevision: main
`)
	out, err := PatchRootAppBranch(in, "mybranch")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "repoURL") {
		t.Fatalf("repoURL was lost:\n%s", out)
	}
	if !strings.Contains(string(out), "kubernetes.default.svc") {
		t.Fatalf("destination was lost:\n%s", out)
	}
}
