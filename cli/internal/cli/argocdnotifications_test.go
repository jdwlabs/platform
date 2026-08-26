package cli

import (
	"bytes"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/jdwlabs/platform/internal/argonotify"
	"github.com/jdwlabs/platform/internal/k8s"
)

func notifyApplication(ns, name string, annotations map[string]string) *unstructured.Unstructured {
	ann := make(map[string]interface{}, len(annotations))
	for k, v := range annotations {
		ann[k] = v
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata": map[string]interface{}{
			"name":        name,
			"namespace":   ns,
			"annotations": ann,
		},
	}}
}

func runNotifyCmd(t *testing.T, kubeObjs []runtime.Object, appObjs []runtime.Object, args ...string) (string, error) {
	t.Helper()
	kc := k8s.NewFake(kubeObjs...)
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{argonotify.ApplicationGVR: "ApplicationList"},
		appObjs...,
	)
	root := NewRootForTest(kc, dc)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"cluster", "argocd-notifications"}, args...))
	err := root.Execute()
	return out.String(), err
}

// The state that motivates this command: chart defaults installed, nothing
// subscribed, so the verdict has to say "no" plainly rather than leave it to
// be inferred from raw YAML.
func TestArgoCDNotifications_NothingConfiguredReadsAsInactive(t *testing.T) {
	out, err := runNotifyCmd(t, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "active: no") {
		t.Errorf("missing the inactive verdict:\n%s", out)
	}
	if !strings.Contains(out, "argocd-notifications-cm: not found") {
		t.Errorf("missing the configmap line:\n%s", out)
	}
	if !strings.Contains(out, "argocd-notifications-secret: not found") {
		t.Errorf("missing the secret line:\n%s", out)
	}
	if !strings.Contains(out, "safe to consider disabling") {
		t.Errorf("missing the actionable help line:\n%s", out)
	}
}

func TestArgoCDNotifications_GlobalSubscriptionReadsAsActive(t *testing.T) {
	cm := []runtime.Object{&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "argocd-notifications-cm", Namespace: "argocd"},
		Data: map[string]string{
			"subscriptions":          "- recipients:\n  - slack:general\n  triggers:\n  - on-sync-failed\n",
			"trigger.on-sync-failed": "...",
		},
	}}
	out, err := runNotifyCmd(t, cm, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "active: yes") {
		t.Errorf("missing the active verdict:\n%s", out)
	}
	if !strings.Contains(out, "global subscriptions: configured") {
		t.Errorf("configmap line should say subscriptions are configured:\n%s", out)
	}
}

func TestArgoCDNotifications_SubscribedApplicationIsListed(t *testing.T) {
	apps := []runtime.Object{
		notifyApplication("argocd", "web", map[string]string{
			"notifications.argoproj.io/subscribe.on-sync-failed.slack": "general",
		}),
		notifyApplication("argocd", "quiet", map[string]string{"unrelated/foo": "bar"}),
	}
	out, err := runNotifyCmd(t, nil, apps)
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "active: yes") {
		t.Errorf("missing the active verdict:\n%s", out)
	}
	if !strings.Contains(out, "subscribed[1]{application,annotations}:") {
		t.Errorf("missing the subscribed table:\n%s", out)
	}
	if !strings.Contains(out, "argocd/web,notifications.argoproj.io/subscribe.on-sync-failed.slack") {
		t.Errorf("subscribed row should name the application and its annotation:\n%s", out)
	}
	if strings.Contains(out, "argocd/quiet") {
		t.Errorf("an application with no subscribe annotation should not be listed:\n%s", out)
	}
}

func TestArgoCDNotifications_SecretKeysAreCountedNotValued(t *testing.T) {
	secret := []runtime.Object{&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "argocd-notifications-secret", Namespace: "argocd"},
		Data:       map[string][]byte{"slack-token": []byte("xoxb-super-secret")},
	}}
	out, err := runNotifyCmd(t, secret, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "argocd-notifications-secret: exists / 1 credential key(s)") {
		t.Errorf("missing the secret summary line:\n%s", out)
	}
	if strings.Contains(out, "xoxb-super-secret") {
		t.Fatalf("secret value leaked into output:\n%s", out)
	}
}

func TestArgoCDNotifications_JSONEmitsConfigMapSecretAndSummaryEvents(t *testing.T) {
	apps := []runtime.Object{
		notifyApplication("argocd", "web", map[string]string{
			"notifications.argoproj.io/subscribe.on-sync-failed.slack": "general",
		}),
	}
	out, err := runNotifyCmd(t, nil, apps, "--json")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 {
		t.Fatalf("want configmap + secret + subscription + summary events, got %d:\n%s", len(lines), out)
	}
	for _, l := range lines {
		if !strings.Contains(l, `"phase":"argocd-notifications"`) {
			t.Errorf("event is missing the phase field: %s", l)
		}
	}
	summary := jsonEventNamed(t, out, "summary")
	if !strings.Contains(summary, `"active":"true"`) {
		t.Errorf("summary event should carry active=true:\n%s", summary)
	}
}

func TestArgoCDNotifications_NamespaceFlagIsHonoured(t *testing.T) {
	cm := []runtime.Object{&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "argocd-notifications-cm", Namespace: "argocd-system"},
		Data:       map[string]string{"subscriptions": "- recipients: [slack:general]\n  triggers: [on-sync-failed]\n"},
	}}
	out, err := runNotifyCmd(t, cm, nil, "--namespace", "argocd-system")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "namespace: argocd-system") {
		t.Errorf("missing the namespace line:\n%s", out)
	}
	if !strings.Contains(out, "active: yes") {
		t.Errorf("configmap in the flagged namespace should be read:\n%s", out)
	}
}

// This command is read-only and never fails on the verdict itself — "inactive"
// is a normal, successful answer, not an error.
func TestArgoCDNotifications_InactiveDoesNotExitNonZero(t *testing.T) {
	_, err := runNotifyCmd(t, nil, nil)
	if err != nil {
		t.Errorf("inactive notifications should not be an error: %v", err)
	}
}
