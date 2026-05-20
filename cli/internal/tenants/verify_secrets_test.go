package tenants

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/jdwlabs/platform/internal/vault"
)

func newES(ns, name, vaultKey, property string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "external-secrets.io/v1",
			"kind":       "ExternalSecret",
			"metadata": map[string]any{
				"name":      name,
				"namespace": ns,
			},
			"spec": map[string]any{
				"secretStoreRef": map[string]any{
					"kind": "ClusterSecretStore",
					"name": "vault",
				},
				"data": []any{
					map[string]any{
						"secretKey": property,
						"remoteRef": map[string]any{
							"key":      vaultKey,
							"property": property,
						},
					},
				},
			},
		},
	}
}

// mockVaultKV serves a KV-v2 backend with the given data map. Paths map to
// /v1/<mount>/data/<path> per Vault KVv2 layout.
func mockVaultKV(t *testing.T, mount string, data map[string]map[string]any) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	prefix := "/v1/" + mount + "/data/"
	mux.HandleFunc(prefix, func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, prefix)
		d, ok := data[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]any{"errors": []string{"secret not found"}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"data": d,
				"metadata": map[string]any{
					"version":      1,
					"created_time": "2026-01-01T00:00:00Z",
				},
			},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newFakeDyn(t *testing.T, objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	t.Helper()
	scheme := runtime.NewScheme()
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			externalSecretGVR: "ExternalSecretList",
		},
		objs...,
	)
}

func TestVerifySecrets_AllResolve(t *testing.T) {
	srv := mockVaultKV(t, "kv", map[string]map[string]any{
		"porkbun": {"api-key": "x", "secret-key": "y"},
	})
	vc, err := vault.NewClient(srv.URL, "root")
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	dc := newFakeDyn(t,
		newES("cert-manager", "porkbun", "porkbun", "api-key"),
	)
	r, err := VerifySecrets(context.Background(), dc, vc, "kv", "vault")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if r.HasIssues() {
		t.Fatalf("expected no issues, got: %+v", r.Issues)
	}
	if r.Checked != 1 {
		t.Fatalf("expected 1 checked, got %d", r.Checked)
	}
}

func TestVerifySecrets_VaultKeyMissing(t *testing.T) {
	srv := mockVaultKV(t, "kv", map[string]map[string]any{})
	vc, err := vault.NewClient(srv.URL, "root")
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	dc := newFakeDyn(t,
		newES("dotablaze-tech", "meowbot-non", "dotablaze-tech-discord-bot-token", "token"),
	)
	r, err := VerifySecrets(context.Background(), dc, vc, "kv", "vault")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(r.Issues) != 1 {
		t.Fatalf("expected 1 issue, got %d: %+v", len(r.Issues), r.Issues)
	}
	if r.Issues[0].Kind != "vault-key-missing" {
		t.Errorf("expected vault-key-missing, got %s", r.Issues[0].Kind)
	}
}

func TestVerifySecrets_PropertyMissing(t *testing.T) {
	srv := mockVaultKV(t, "kv", map[string]map[string]any{
		"usersrole": {"jwt_key_prd": "ok"}, // jwt_key_non absent
	})
	vc, err := vault.NewClient(srv.URL, "root")
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	dc := newFakeDyn(t,
		newES("jdwlabs", "usersrole-non", "usersrole", "jwt_key_non"),
		newES("jdwlabs", "usersrole-prd", "usersrole", "jwt_key_prd"),
	)
	r, err := VerifySecrets(context.Background(), dc, vc, "kv", "vault")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if r.Checked != 2 {
		t.Errorf("expected 2 checked, got %d", r.Checked)
	}
	if len(r.Issues) != 1 {
		t.Fatalf("expected 1 issue (jwt_key_non), got %d: %+v", len(r.Issues), r.Issues)
	}
	if r.Issues[0].Kind != "property-missing" {
		t.Errorf("expected property-missing, got %s", r.Issues[0].Kind)
	}
	if r.Issues[0].ESName != "usersrole-non" {
		t.Errorf("expected usersrole-non, got %s", r.Issues[0].ESName)
	}
}

func TestVerifySecrets_IgnoresOtherStores(t *testing.T) {
	srv := mockVaultKV(t, "kv", map[string]map[string]any{})
	vc, err := vault.NewClient(srv.URL, "root")
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	otherStore := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "external-secrets.io/v1",
			"kind":       "ExternalSecret",
			"metadata": map[string]any{
				"name":      "github-app",
				"namespace": "jdwlabs",
			},
			"spec": map[string]any{
				"secretStoreRef": map[string]any{
					"kind": "ClusterSecretStore",
					"name": "github-app-token",
				},
				"data": []any{},
			},
		},
	}
	dc := newFakeDyn(t, otherStore)
	r, err := VerifySecrets(context.Background(), dc, vc, "kv", "vault")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if r.Checked != 0 {
		t.Errorf("expected 0 checked (different store), got %d", r.Checked)
	}
	if r.HasIssues() {
		t.Errorf("expected no issues, got: %+v", r.Issues)
	}
}
