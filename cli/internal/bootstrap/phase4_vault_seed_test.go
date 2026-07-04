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

func TestStaticSeedSpecs_TruenasCSI(t *testing.T) {
	spec, ok := staticSeedSpecs["truenas-csi"]
	if !ok {
		t.Fatal("truenas-csi seed spec missing")
	}
	if spec.Path != "truenas-csi" {
		t.Fatalf("path = %q, want truenas-csi", spec.Path)
	}
	if len(spec.Fields) != 1 || spec.Fields[0].Name != "api_key" {
		t.Fatalf("fields = %+v, want single api_key field", spec.Fields)
	}
	if spec.Fields[0].EnvVar != "PLATFORMCTL_TRUENAS_CSI_API_KEY" {
		t.Fatalf("env = %q", spec.Fields[0].EnvVar)
	}
}

func TestStaticSeedSpecs_Holmes(t *testing.T) {
	spec, ok := staticSeedSpecs["holmes"]
	if !ok {
		t.Fatal("holmes seed spec missing")
	}
	if spec.Path != "holmes" {
		t.Fatalf("path = %q, want holmes", spec.Path)
	}
	want := map[string]string{
		"litellm_key":         "PLATFORMCTL_HOLMES_LITELLM_KEY",
		"discord_webhook_url": "PLATFORMCTL_HOLMES_DISCORD_WEBHOOK",
		"jira_url":            "PLATFORMCTL_HOLMES_JIRA_URL",
		"jira_email":          "PLATFORMCTL_HOLMES_JIRA_EMAIL",
		"jira_api_token":      "PLATFORMCTL_HOLMES_JIRA_API_TOKEN",
		"github_token":        "PLATFORMCTL_HOLMES_GITHUB_TOKEN",
	}
	got := map[string]string{}
	for _, f := range spec.Fields {
		got[f.Name] = f.EnvVar
	}
	for name, env := range want {
		if got[name] != env {
			t.Fatalf("field %s env = %q, want %q (all: %v)", name, got[name], env, got)
		}
	}
}

func TestStaticSeedSpecs_Litellm(t *testing.T) {
	spec, ok := staticSeedSpecs["litellm"]
	if !ok {
		t.Fatal("litellm seed spec missing")
	}
	if spec.Path != "litellm" {
		t.Fatalf("path = %q, want litellm", spec.Path)
	}
	want := map[string]string{
		"master_key":         "PLATFORMCTL_LITELLM_MASTER_KEY",
		"anthropic_api_key":  "PLATFORMCTL_LITELLM_ANTHROPIC_API_KEY",
		"openrouter_api_key": "PLATFORMCTL_LITELLM_OPENROUTER_API_KEY",
	}
	got := map[string]string{}
	for _, f := range spec.Fields {
		got[f.Name] = f.EnvVar
		if !f.Secret {
			t.Errorf("field %s must be Secret", f.Name)
		}
		if !f.Optional {
			t.Errorf("field %s must be Optional: partial re-seeds must not prompt for keys that are already in Vault", f.Name)
		}
	}
	for name, env := range want {
		if got[name] != env {
			t.Fatalf("field %s env = %q, want %q (all: %v)", name, got[name], env, got)
		}
	}
}

func TestVaultSeedPhase_MergePreservesExistingFields(t *testing.T) {
	srv, c := mockVaultKV(t)
	// Pre-existing field seeded outside this spec run must survive a re-seed.
	if err := c.PutKV(context.Background(), "secret", "holmes", map[string]any{
		"litellm_key": "sk-existing",
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PLATFORMCTL_HOLMES_DISCORD_WEBHOOK", "https://discord.example/hook")
	t.Setenv("PLATFORMCTL_HOLMES_JIRA_URL", "https://jdwlabs.atlassian.net")
	t.Setenv("PLATFORMCTL_HOLMES_JIRA_EMAIL", "ops@example.com")
	t.Setenv("PLATFORMCTL_HOLMES_JIRA_API_TOKEN", "jira-tok")
	t.Setenv("PLATFORMCTL_HOLMES_GITHUB_TOKEN", "gh-tok")

	p := NewVaultSeedPhase(NewVaultAddrResolver(srv.URL, nil, nil), true, "secret", nil, []string{"holmes"})
	if err := p.Apply(context.Background()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got, err := c.GetKV(context.Background(), "secret", "holmes")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got["litellm_key"] != "sk-existing" {
		t.Fatalf("litellm_key clobbered: %v", got)
	}
	if got["discord_webhook_url"] != "https://discord.example/hook" {
		t.Fatalf("discord_webhook_url: %v", got)
	}
	if got["github_token"] != "gh-tok" {
		t.Fatalf("github_token: %v", got)
	}
}
