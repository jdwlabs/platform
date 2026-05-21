package bootstrap

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"

	"github.com/jdwlabs/platform/internal/k8s"
)

func makeExternalSecret(ns, name string, ready bool) *unstructured.Unstructured {
	status := "False"
	if ready {
		status = "True"
	}
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "external-secrets.io/v1",
			"kind":       "ExternalSecret",
			"metadata":   map[string]any{"name": name, "namespace": ns},
			"status": map[string]any{
				"conditions": []any{
					map[string]any{"type": "Ready", "status": status, "message": ""},
				},
			},
		},
	}
}

func makeCertificate(ns, name string, ready bool) *unstructured.Unstructured {
	status := "False"
	if ready {
		status = "True"
	}
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "cert-manager.io/v1",
			"kind":       "Certificate",
			"metadata":   map[string]any{"name": name, "namespace": ns},
			"status": map[string]any{
				"conditions": []any{
					map[string]any{"type": "Ready", "status": status, "message": ""},
				},
			},
		},
	}
}

func makeApplication(name, health string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "Application",
			"metadata":   map[string]any{"name": name, "namespace": "argocd"},
			"status": map[string]any{
				"health": map[string]any{"status": health},
				"sync":   map[string]any{"status": "Synced"},
			},
		},
	}
}

func fakeConvergeDynamic(t *testing.T, objs ...runtime.Object) *fake.FakeDynamicClient {
	t.Helper()
	scheme := runtime.NewScheme()
	return fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		convergeGVRExternalSecret: "ExternalSecretList",
		convergeGVRCertificate:    "CertificateList",
		convergeGVRApplication:    "ApplicationList",
	}, objs...)
}

func TestConvergePhase_Detect_AlreadyDone(t *testing.T) {
	dc := fakeConvergeDynamic(t,
		makeExternalSecret("cert-manager", "porkbun", true),
		makeExternalSecret("longhorn-system", "longhorn", true),
		makeExternalSecret("monitoring", "grafana-admin-credentials", true),
		makeExternalSecret("monitoring", "alertmanager-config", true),
		makeExternalSecret("database", "rclone-gdrive", true),
		makeCertificate("nginx-gateway", "wildcard-jdwlabs", true),
		makeApplication("platform-vault", "Healthy"),
		makeApplication("platform-cert-manager", "Healthy"),
		makeApplication("jdwlabs-usersrole-non", "Degraded"), // tenant — not gated
	)
	p := NewConvergePhase(k8s.NewFake(), dc)
	st, err := p.Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st != StateAlreadyDone {
		t.Fatalf("expected StateAlreadyDone, got %s", st)
	}
}

func TestConvergePhase_Detect_ESONotReady(t *testing.T) {
	dc := fakeConvergeDynamic(t,
		makeExternalSecret("cert-manager", "porkbun", false), // not ready
		makeExternalSecret("longhorn-system", "longhorn", true),
		makeExternalSecret("monitoring", "grafana-admin-credentials", true),
		makeExternalSecret("monitoring", "alertmanager-config", true),
		makeExternalSecret("database", "rclone-gdrive", true),
		makeCertificate("nginx-gateway", "wildcard-jdwlabs", true),
	)
	p := NewConvergePhase(k8s.NewFake(), dc)
	st, _ := p.Detect(context.Background())
	if st != StateNotStarted {
		t.Fatalf("expected StateNotStarted, got %s", st)
	}
}

func TestConvergePhase_Detect_PlatformAppDegraded(t *testing.T) {
	dc := fakeConvergeDynamic(t,
		makeExternalSecret("cert-manager", "porkbun", true),
		makeExternalSecret("longhorn-system", "longhorn", true),
		makeExternalSecret("monitoring", "grafana-admin-credentials", true),
		makeExternalSecret("monitoring", "alertmanager-config", true),
		makeExternalSecret("database", "rclone-gdrive", true),
		makeCertificate("nginx-gateway", "wildcard-jdwlabs", true),
		makeApplication("platform-vault", "Degraded"),
	)
	p := NewConvergePhase(k8s.NewFake(), dc)
	st, _ := p.Detect(context.Background())
	if st != StateNotStarted {
		t.Fatalf("expected StateNotStarted, got %s", st)
	}
}

// TestConvergePhase_Apply_EmitsEvents verifies Apply emits progressing then ok
// events as gates pass. Uses a real HTTP mock so port-forward / k8s deps are
// avoided; the fake dynamic client provides the resource state.
func TestConvergePhase_Apply_EmitsEvents(t *testing.T) {
	_ = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{})
	}))

	dc := fakeConvergeDynamic(t,
		makeExternalSecret("cert-manager", "porkbun", true),
		makeExternalSecret("longhorn-system", "longhorn", true),
		makeExternalSecret("monitoring", "grafana-admin-credentials", true),
		makeExternalSecret("monitoring", "alertmanager-config", true),
		makeExternalSecret("database", "rclone-gdrive", true),
		makeCertificate("nginx-gateway", "wildcard-jdwlabs", true),
		makeApplication("platform-vault", "Healthy"),
	)

	var events []string
	p := NewConvergePhase(k8s.NewFake(), dc)
	p.SetOnEvent(func(status, msg string) {
		events = append(events, status+":"+msg)
	})

	if err := p.Apply(context.Background()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// Expect 3 ok events (one per gate)
	okCount := 0
	for _, e := range events {
		if len(e) >= 2 && e[:2] == "ok" {
			okCount++
		}
	}
	if okCount != 3 {
		t.Fatalf("expected 3 ok events, got %d: %v", okCount, events)
	}
}
