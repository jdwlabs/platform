package bootstrap

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/jdwlabs/platform/internal/k8s"
	"github.com/jdwlabs/platform/internal/vault"
)

// mockVaultServer returns a mock Vault HTTP server that handles init/unseal/mounts.
func mockVaultServer(t *testing.T) *httptest.Server {
	t.Helper()
	initialized := false
	mux := http.NewServeMux()

	mux.HandleFunc("/v1/sys/init", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(map[string]any{"initialized": initialized})
			return
		}
		initialized = true
		json.NewEncoder(w).Encode(map[string]any{
			"keys":        []string{"key1"},
			"keys_base64": []string{"a2V5MQ=="},
			"root_token":  "root-tok",
		})
	})
	mux.HandleFunc("/v1/sys/unseal", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"sealed": false})
	})
	mux.HandleFunc("/v1/sys/mounts", func(w http.ResponseWriter, r *http.Request) {
		// Standard vault response envelope; empty data = no mounts yet.
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}})
	})
	mux.HandleFunc("/v1/sys/mounts/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestVaultInitPhase_Apply_PersistsSecrets(t *testing.T) {
	srv := mockVaultServer(t)

	kube := k8s.NewFake(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "vault"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "external-secrets"}},
	)
	p := NewVaultInitPhase(kube, vault.NewBuilder(srv.URL), true)
	if err := p.Apply(context.Background()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := kube.CoreV1().Secrets("vault").Get(context.Background(), "vault-init", metav1.GetOptions{}); err != nil {
		t.Fatalf("vault/vault-init missing: %v", err)
	}
	if _, err := kube.CoreV1().Secrets("external-secrets").Get(context.Background(), "vault-token", metav1.GetOptions{}); err != nil {
		t.Fatalf("external-secrets/vault-token missing: %v", err)
	}
}

func TestVaultInitPhase_Detect_NotStarted(t *testing.T) {
	srv := mockVaultServer(t)
	kube := k8s.NewFake()
	p := NewVaultInitPhase(kube, vault.NewBuilder(srv.URL), true)
	st, err := p.Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st != StateNotStarted {
		t.Fatalf("got %s", st)
	}
}
