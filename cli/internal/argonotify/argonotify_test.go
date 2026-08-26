package argonotify

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/jdwlabs/platform/internal/k8s"
)

func application(ns, name string, annotations map[string]string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata": map[string]interface{}{
			"name":        name,
			"namespace":   ns,
			"annotations": toStringMapInterface(annotations),
		},
	}}
}

func toStringMapInterface(m map[string]string) map[string]interface{} {
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func appClient(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{ApplicationGVR: "ApplicationList"},
		objs...,
	)
}

// An absent ConfigMap and Secret is not an error — it is the state a cluster
// that never configured notifications is in, and it must read as inactive
// rather than fail the read.
func TestLoad_AbsentConfigMapAndSecretReadsAsInactive(t *testing.T) {
	kube := k8s.NewFake()
	dyn := appClient()

	status, err := Load(context.Background(), kube, dyn, "argocd")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.ConfigMap.Exists {
		t.Error("ConfigMap.Exists = true, want false")
	}
	if status.Secret.Exists {
		t.Error("Secret.Exists = true, want false")
	}
	if status.Active() {
		t.Error("Active() = true, want false — nothing is configured")
	}
}

// Chart-installed defaults live in the controller binary, not the ConfigMap,
// so trigger./template./service. keys here reflect only what an operator
// added — and they are partitioned by prefix without cross-contamination.
func TestLoad_PartitionsConfigMapKeysByPrefix(t *testing.T) {
	kube := k8s.NewFake(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "argocd-notifications-cm", Namespace: "argocd"},
		Data: map[string]string{
			"trigger.on-sync-failed": "...",
			"template.app-sync-fail": "...",
			"service.slack":          "...",
			"context":                "not a trigger, template or service",
			"defaultTriggers":        "not one of the tracked prefixes either",
		},
	})
	dyn := appClient()

	status, err := Load(context.Background(), kube, dyn, "argocd")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !status.ConfigMap.Exists {
		t.Fatal("ConfigMap.Exists = false, want true")
	}
	if len(status.ConfigMap.Triggers) != 1 || status.ConfigMap.Triggers[0] != "trigger.on-sync-failed" {
		t.Errorf("Triggers = %v, want [trigger.on-sync-failed]", status.ConfigMap.Triggers)
	}
	if len(status.ConfigMap.Templates) != 1 || status.ConfigMap.Templates[0] != "template.app-sync-fail" {
		t.Errorf("Templates = %v, want [template.app-sync-fail]", status.ConfigMap.Templates)
	}
	if len(status.ConfigMap.Services) != 1 || status.ConfigMap.Services[0] != "service.slack" {
		t.Errorf("Services = %v, want [service.slack]", status.ConfigMap.Services)
	}
	if status.Active() {
		t.Error("Active() = true, want false — triggers/templates/services alone are not a subscription")
	}
}

func TestLoad_GlobalSubscriptionsKeyMakesItActive(t *testing.T) {
	kube := k8s.NewFake(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "argocd-notifications-cm", Namespace: "argocd"},
		Data: map[string]string{
			"subscriptions": "- recipients:\n  - slack:general\n  triggers:\n  - on-sync-failed\n",
		},
	})
	dyn := appClient()

	status, err := Load(context.Background(), kube, dyn, "argocd")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !status.Active() {
		t.Error("Active() = false, want true — the ConfigMap defines a global subscription")
	}
}

// An explicit empty list is not the same as a configured one, even though a
// bare whitespace check would call it non-empty.
func TestHasGlobalSubscriptions_EmptyListIsNotActive(t *testing.T) {
	cm := ConfigMap{Subscriptions: "[]\n"}
	if cm.HasGlobalSubscriptions() {
		t.Error("HasGlobalSubscriptions() = true, want false for an explicit empty list")
	}
}

func TestHasGlobalSubscriptions_UnparseableContentCountsAsConfigured(t *testing.T) {
	cm := ConfigMap{Subscriptions: "not: [valid, yaml list"}
	if !cm.HasGlobalSubscriptions() {
		t.Error("HasGlobalSubscriptions() = false, want true — unparseable content is not nothing")
	}
}

// The per-Application path is the other way notifications go live, and it has
// to work with zero global configuration: an Application can subscribe on its
// own with no "subscriptions" key in the ConfigMap at all.
func TestLoad_ApplicationSubscribeAnnotationMakesItActiveWithNoGlobalConfig(t *testing.T) {
	kube := k8s.NewFake()
	dyn := appClient(
		application("argocd", "web", map[string]string{
			"notifications.argoproj.io/subscribe.on-sync-failed.slack": "general",
			"unrelated.annotation/foo":                                 "bar",
		}),
		application("argocd", "quiet", map[string]string{
			"unrelated.annotation/foo": "bar",
		}),
	)

	status, err := Load(context.Background(), kube, dyn, "argocd")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !status.Active() {
		t.Error("Active() = false, want true — one Application carries a subscribe annotation")
	}
	if len(status.Subscriptions) != 1 {
		t.Fatalf("Subscriptions = %v, want exactly one entry", status.Subscriptions)
	}
	if got := status.Subscriptions[0].Ref(); got != "argocd/web" {
		t.Errorf("subscribed application = %s, want argocd/web", got)
	}
	if len(status.Subscriptions[0].Annotations) != 1 ||
		status.Subscriptions[0].Annotations[0] != "notifications.argoproj.io/subscribe.on-sync-failed.slack" {
		t.Errorf("Annotations = %v, want the one subscribe annotation only", status.Subscriptions[0].Annotations)
	}
}

// Secret values are never read — only key names, which are not themselves
// credentials.
func TestLoad_SecretKeysAreListedByNameOnly(t *testing.T) {
	kube := k8s.NewFake(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "argocd-notifications-secret", Namespace: "argocd"},
		Data: map[string][]byte{
			"slack-token": []byte("xoxb-should-never-appear-anywhere"),
		},
	})
	dyn := appClient()

	status, err := Load(context.Background(), kube, dyn, "argocd")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !status.Secret.Exists {
		t.Fatal("Secret.Exists = false, want true")
	}
	if len(status.Secret.Keys) != 1 || status.Secret.Keys[0] != "slack-token" {
		t.Errorf("Keys = %v, want [slack-token]", status.Secret.Keys)
	}
}
