package bootstrap

import (
	"strings"
	"testing"
)

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
