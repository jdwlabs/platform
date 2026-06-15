package bootstrap

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/jdwlabs/platform/internal/vault"
)

func mockVaultKV(t *testing.T) (*httptest.Server, *vault.Client) {
	t.Helper()
	var mu sync.Mutex
	store := map[string]map[string]any{}

	mux := http.NewServeMux()
	// GET /v1/<mount>/data/<path>
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		key := r.URL.Path
		if r.Method == http.MethodGet {
			if v, ok := store[key]; ok {
				json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"data": v}})
				return
			}
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]any{"errors": []string{"not found"}})
			return
		}
		if r.Method == http.MethodPost || r.Method == http.MethodPut {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			data, _ := body["data"].(map[string]any)
			store[key] = data
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{})
			return
		}
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, err := vault.NewClient(srv.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	return srv, c
}

func TestVaultSeedPhase_PorkbunFromEnv(t *testing.T) {
	srv, c := mockVaultKV(t)
	t.Setenv("PLATFORMCTL_PORKBUN_API_KEY", "pk-api")
	t.Setenv("PLATFORMCTL_PORKBUN_SECRET_KEY", "pk-secret")

	p := NewVaultSeedPhase(NewVaultAddrResolver(srv.URL, nil, nil), true, "secret", nil, []string{"porkbun"})
	if err := p.Apply(context.Background()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got, err := c.GetKV(context.Background(), "secret", "porkbun")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got["api-key"] != "pk-api" {
		t.Fatalf("api-key: %v", got)
	}
}

func TestVaultSeedPhase_TenantSpec(t *testing.T) {
	srv, _ := mockVaultKV(t)
	t.Setenv("PLATFORMCTL_DEMO_GITHUB_APP_ID", "12345")
	t.Setenv("PLATFORMCTL_DEMO_GITHUB_INSTALLATION_ID", "67890")
	t.Setenv("PLATFORMCTL_DEMO_GITHUB_PRIVATE_KEY", "-----BEGIN RSA PRIVATE KEY-----")

	p := NewVaultSeedPhase(NewVaultAddrResolver(srv.URL, nil, nil), true, "secret", []string{"demo"}, []string{"demo-github-app"})
	if err := p.Apply(context.Background()); err != nil {
		t.Fatalf("apply: %v", err)
	}
}

func TestVaultSeedPhase_ArgoCDDexFromEnv(t *testing.T) {
	srv, c := mockVaultKV(t)
	t.Setenv("PLATFORMCTL_ARGOCD_DEX_ADMIN_PASSWORD_HASH", "$2a$10$testhash")
	t.Setenv("PLATFORMCTL_ARGOCD_DEX_HEADLAMP_CLIENT_SECRET", "super-secret-value")
	t.Setenv("PLATFORMCTL_ARGOCD_DEX_GITHUB_CLIENT_ID", "Ov23litestclientid")
	t.Setenv("PLATFORMCTL_ARGOCD_DEX_GITHUB_CLIENT_SECRET", "gh-client-secret-value")

	p := NewVaultSeedPhase(NewVaultAddrResolver(srv.URL, nil, nil), true, "secret", nil, []string{"argocd-dex"})
	if err := p.Apply(context.Background()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got, err := c.GetKV(context.Background(), "secret", "argocd-dex")
	if err != nil {
		t.Fatalf("get kv: %v", err)
	}
	if got["admin-password-hash"] != "$2a$10$testhash" {
		t.Fatalf("admin-password-hash: got %v", got["admin-password-hash"])
	}
	if got["headlamp-client-secret"] != "super-secret-value" {
		t.Fatalf("headlamp-client-secret: got %v", got["headlamp-client-secret"])
	}
	if got["github-client-id"] != "Ov23litestclientid" {
		t.Fatalf("github-client-id: got %v", got["github-client-id"])
	}
	if got["github-client-secret"] != "gh-client-secret-value" {
		t.Fatalf("github-client-secret: got %v", got["github-client-secret"])
	}
}
