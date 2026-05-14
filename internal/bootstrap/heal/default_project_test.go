package heal

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

const defaultProjectYAML = `apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: default
  namespace: argocd
spec:
  description: "Default project"
  sourceRepos:
    - '*'
  destinations:
    - namespace: '*'
      server: '*'
`

func newAppProjectFake() *dynamicfake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			{Group: "argoproj.io", Version: "v1alpha1", Resource: "appprojects"}: "AppProjectList",
		},
	)
}

func TestApplyDefaultProject(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "default.yaml")
	if err := os.WriteFile(path, []byte(defaultProjectYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	dc := newAppProjectFake()
	if err := ApplyDefaultProject(context.Background(), dc, path); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestApplyDefaultProject_BadPath(t *testing.T) {
	dc := newAppProjectFake()
	if err := ApplyDefaultProject(context.Background(), dc, "/no/such/file.yaml"); err == nil {
		t.Fatalf("expected error for missing file")
	}
}
