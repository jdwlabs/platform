package vault

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// mockVault serves just enough of the Vault HTTP API for unit tests.
func mockVault(t *testing.T) (*httptest.Server, *Client) {
	t.Helper()
	mux := http.NewServeMux()

	// GET /v1/sys/init
	mux.HandleFunc("/v1/sys/init", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"initialized": false})
	})

	// PUT /v1/sys/init
	mux.HandleFunc("PUT /v1/sys/init", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"keys":         []string{"key1"},
			"keys_base64":  []string{"a2V5MQ=="},
			"root_token":   "test-root-token",
		})
	})

	// PUT /v1/sys/unseal
	mux.HandleFunc("PUT /v1/sys/unseal", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"sealed": false})
	})

	// GET /v1/sys/mounts
	mux.HandleFunc("/v1/sys/mounts", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{})
	})

	// POST /v1/sys/mounts/<mount>
	mux.HandleFunc("/v1/sys/mounts/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c, err := NewClient(srv.URL, "test-root-token")
	if err != nil {
		t.Fatal(err)
	}
	return srv, c
}

func TestNewClient_OK(t *testing.T) {
	_, c := mockVault(t)
	if c == nil {
		t.Fatal("nil client")
	}
}

func TestNewBuilder(t *testing.T) {
	srv, _ := mockVault(t)
	b := NewBuilder(srv.URL)
	c, err := b.New(context.Background(), "tok")
	if err != nil {
		t.Fatalf("builder: %v", err)
	}
	if c == nil {
		t.Fatal("nil client from builder")
	}
}

func TestInit(t *testing.T) {
	_, c := mockVault(t)
	res, err := c.Init(context.Background(), 1, 1)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if res.RootToken != "test-root-token" {
		t.Fatalf("token: %s", res.RootToken)
	}
}

func TestUnseal(t *testing.T) {
	_, c := mockVault(t)
	if err := c.Unseal(context.Background(), []string{"key1"}); err != nil {
		t.Fatalf("unseal: %v", err)
	}
}

func TestIsInitialized(t *testing.T) {
	_, c := mockVault(t)
	ok, err := c.IsInitialized(context.Background())
	if err != nil {
		t.Fatalf("init status: %v", err)
	}
	if ok {
		t.Fatal("expected not initialized")
	}
}
