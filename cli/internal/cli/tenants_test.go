package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdwlabs/platform/internal/vault"
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

// writeVerifyTree lays out a tenants tree with one enabled service holding two
// refs and one service absent from tenant.yaml holding a third.
func writeVerifyTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	tenantDir := filepath.Join(root, "platform")
	if err := os.MkdirAll(tenantDir, 0o755); err != nil {
		t.Fatal(err)
	}
	tenantDoc := "name: platform\nnamespaces:\n  - name: platform\nservices:\n" +
		"  - name: holmes\n    chart: x\n    repo: https://example.invalid\n    revision: 1.0.0\n" +
		"    namespace: platform\n    postInstall: true\n    syncWave: 1\n"
	if err := os.WriteFile(filepath.Join(tenantDir, "tenant.yaml"), []byte(tenantDoc), 0o644); err != nil {
		t.Fatal(err)
	}

	es := func(name string, props []string) string {
		doc := "apiVersion: external-secrets.io/v1\nkind: ExternalSecret\nmetadata:\n  name: " + name +
			"\n  namespace: platform\nspec:\n  secretStoreRef:\n    kind: ClusterSecretStore\n    name: vault\n  data:\n"
		for _, p := range props {
			doc += "    - secretKey: " + p + "\n      remoteRef:\n        key: holmes\n        property: " + p + "\n"
		}
		return doc
	}
	for svc, doc := range map[string]string{
		"holmes":         es("holmes", []string{"litellm_key", "webhook_token"}),
		"arc-runner-set": es("github-app", []string{"app_id"}),
	} {
		dir := filepath.Join(tenantDir, "services", svc, "postInstall")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "externalsecret.yaml"), []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// injectVault points the command at a stub KV-v2 backend serving fields.
func injectVault(t *testing.T, fields map[string]any) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/kv/data/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/holmes") {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": []string{"secret not found"}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"data": fields, "metadata": map[string]any{"version": 1}},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	vc, err := vault.NewClient(srv.URL, "root")
	if err != nil {
		t.Fatalf("vault client: %v", err)
	}
	testVaultClient = vc
	t.Cleanup(func() { testVaultClient = nil })
}

func runVerifySecrets(t *testing.T, tree string) (string, error) {
	t.Helper()
	root, _ := NewRoot("test")
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs([]string{"tenants", "verify-secrets", "--json", "--source", "repo", "--path", tree})
	err := root.Execute()
	return out.String(), err
}

func TestTenantsVerifySecrets_RepoSourceNamesUncheckedRefs(t *testing.T) {
	injectVault(t, map[string]any{"litellm_key": "x", "webhook_token": "y"})
	got, err := runVerifySecrets(t, writeVerifyTree(t))
	if err != nil {
		t.Fatalf("execute: %v\noutput: %s", err, got)
	}
	if !strings.Contains(got, "service-not-enabled: 1 ref(s) — arc-runner-set") {
		t.Errorf("output does not name the unchecked class:\n%s", got)
	}
	if !strings.Contains(got, "3 refs found; 2 checked; 1 not checked (service-not-enabled=1); 0 issues") {
		t.Errorf("summary does not separate found from checked:\n%s", got)
	}
}

// The regression: a ref present only in the tree must fail the gate.
func TestTenantsVerifySecrets_RepoSourceFailsOnUnresolvableRef(t *testing.T) {
	injectVault(t, map[string]any{"litellm_key": "x"}) // webhook_token absent
	got, err := runVerifySecrets(t, writeVerifyTree(t))
	if err == nil {
		t.Fatalf("expected non-zero exit, got nil\noutput: %s", got)
	}
	if code := ExitCode(err); code != ExitHardFail {
		t.Errorf("exit code = %d, want %d", code, ExitHardFail)
	}
	if !strings.Contains(got, "property-missing") || !strings.Contains(got, "webhook_token") {
		t.Errorf("output does not report the failing ref:\n%s", got)
	}
	if !strings.Contains(got, "1 issues") {
		t.Errorf("summary does not report the issue count:\n%s", got)
	}
}

func TestTenantsVerifySecrets_RejectsUnknownSource(t *testing.T) {
	root, _ := NewRoot("test")
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs([]string{"tenants", "verify-secrets", "--json", "--source", "both"})
	if err := root.Execute(); err == nil {
		t.Fatal("expected error for unknown --source, got nil")
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
