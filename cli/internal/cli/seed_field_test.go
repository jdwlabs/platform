package cli

import (
	"bytes"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/jdwlabs/platform/internal/k8s"
)

// runSeed drives `bootstrap seed` with no reachable Vault. Every case here must
// fail during validation, which happens before any client is built — if one ever
// reached the Vault resolver instead, this test would hang rather than fail
// quietly, which is the outcome worth noticing.
func runSeed(t *testing.T, args ...string) (string, error) {
	t.Helper()
	kc := k8s.NewFake()
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}: "ApplicationList",
		},
	)
	root := NewRootForTest(kc, dc)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestSeed_HasFieldFlag(t *testing.T) {
	root, _ := NewRoot("test")
	for _, c := range root.Commands() {
		if c.Name() != "bootstrap" {
			continue
		}
		for _, sub := range c.Commands() {
			if sub.Name() == "seed" {
				if sub.Flags().Lookup("field") == nil {
					t.Fatal("bootstrap seed is missing --field")
				}
				return
			}
		}
	}
	t.Fatal("bootstrap seed command not found")
}

func TestSeed_UnknownSpecKeyIsRejectedBeforeAnyVaultCall(t *testing.T) {
	_, err := runSeed(t, "bootstrap", "seed", "grafana-git-sync")
	if err == nil {
		t.Fatal("a mistyped spec key must fail rather than write nothing and succeed")
	}
	if !strings.Contains(err.Error(), "unknown seed spec") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "grafana-gitsync") {
		t.Fatalf("error should list the valid keys: %v", err)
	}
}

func TestSeed_UnknownFieldIsRejected(t *testing.T) {
	_, err := runSeed(t, "bootstrap", "seed", "holmes", "--field", "webhook-token")
	if err == nil {
		t.Fatal("a misspelled field must be rejected")
	}
	if !strings.Contains(err.Error(), "webhook_token") {
		t.Fatalf("error should list the spec's fields: %v", err)
	}
}

func TestSeed_FieldWithoutASingleSpecIsRejected(t *testing.T) {
	_, err := runSeed(t, "bootstrap", "seed", "--field", "webhook_token")
	if err == nil {
		t.Fatal("--field with no spec must be rejected")
	}
	if !strings.Contains(err.Error(), "exactly one spec") {
		t.Fatalf("unexpected error: %v", err)
	}
}
