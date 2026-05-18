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
	p := NewVaultInitPhase(kube, NewVaultAddrResolver(srv.URL, nil, nil), t.TempDir())
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
	// Detect() checks container Running state (not cs.Ready) — Vault's readiness
	// probe fails until after init, so we must not gate on Ready here.
	kube := k8s.NewFake(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "platform-vault-0",
				Namespace: "vault",
				Labels:    map[string]string{"app.kubernetes.io/name": "vault"},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{
					Name:  "vault",
					Ready: false, // readiness probe failing — expected before init
					State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
				}},
			},
		},
	)
	p := NewVaultInitPhase(kube, NewVaultAddrResolver(srv.URL, nil, nil), t.TempDir())
	st, err := p.Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st != StateNotStarted {
		t.Fatalf("got %s, want StateNotStarted", st)
	}
}

func TestVaultInitPhase_Detect_InProgress_PodNotReady(t *testing.T) {
	srv := mockVaultServer(t)
	// No vault pods → pod not ready → StateInProgress
	kube := k8s.NewFake()
	p := NewVaultInitPhase(kube, NewVaultAddrResolver(srv.URL, nil, nil), t.TempDir())
	st, err := p.Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st != StateInProgress {
		t.Fatalf("got %s, want StateInProgress", st)
	}
}

func TestVaultInitPhase_Detect_InProgress_NamespaceMissing(t *testing.T) {
	srv := mockVaultServer(t)
	// No vault namespace → InProgress with descriptive message
	kube := k8s.NewFake()
	p := NewVaultInitPhase(kube, NewVaultAddrResolver(srv.URL, nil, nil), t.TempDir())
	st, err := p.Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st != StateInProgress {
		t.Fatalf("got %s, want StateInProgress", st)
	}
	msg := p.ProgressMessage(context.Background())
	if msg == "" {
		t.Fatal("expected a progress message when vault namespace missing")
	}
}

func TestVaultInitPhase_Detect_InProgress_NamespaceExistsNoPods(t *testing.T) {
	srv := mockVaultServer(t)
	kube := k8s.NewFake(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "vault"}},
	)
	p := NewVaultInitPhase(kube, NewVaultAddrResolver(srv.URL, nil, nil), t.TempDir())
	st, err := p.Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st != StateInProgress {
		t.Fatalf("got %s, want StateInProgress", st)
	}
	msg := p.ProgressMessage(context.Background())
	if msg == "" {
		t.Fatal("expected a progress message when vault namespace exists but no pods")
	}
}

func TestVaultInitPhase_Detect_Unknown_CrashLoop(t *testing.T) {
	srv := mockVaultServer(t)
	kube := k8s.NewFake(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "platform-vault-0",
				Namespace: "vault",
				Labels:    map[string]string{"app.kubernetes.io/name": "vault"},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{
					Name:  "vault",
					Ready: false,
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
					},
				}},
			},
		},
	)
	p := NewVaultInitPhase(kube, NewVaultAddrResolver(srv.URL, nil, nil), t.TempDir())
	st, err := p.Detect(context.Background())
	if err == nil {
		t.Fatal("expected error for CrashLoopBackOff vault pod")
	}
	if st != StateUnknown {
		t.Fatalf("got %s, want StateUnknown", st)
	}
}
