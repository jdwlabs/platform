package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestTenantsValidate_HappyPath(t *testing.T) {
	tmp := t.TempDir()
	tenantDir := filepath.Join(tmp, "demo")
	if err := os.MkdirAll(tenantDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte("name: demo\nnamespaces:\n  - name: demo\nservices: []\n")
	if err := os.WriteFile(filepath.Join(tenantDir, "tenant.yaml"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	root, _ := NewRoot("test")
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs([]string{"tenants", "validate", tmp})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v\nstderr: %s", err, errOut.String())
	}
}

func TestTenantsValidate_InvalidFails(t *testing.T) {
	tmp := t.TempDir()
	tenantDir := filepath.Join(tmp, "broken")
	if err := os.MkdirAll(tenantDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte("namespaces: []\nservices: []\n")
	if err := os.WriteFile(filepath.Join(tenantDir, "tenant.yaml"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	root, _ := NewRoot("test")
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs([]string{"tenants", "validate", tmp})
	err := root.Execute()
	if err == nil {
		t.Fatalf("expected validation error, got nil")
	}
}
