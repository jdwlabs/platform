package cluster

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func newDeployment(ns, name, workloadName string, ready int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels: map[string]string{
				"app.kubernetes.io/name":     workloadName,
				"app.kubernetes.io/instance": name,
			},
		},
		Status: appsv1.DeploymentStatus{
			Replicas:      ready,
			ReadyReplicas: ready,
			Conditions: []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
			},
		},
	}
}

func TestCheckDeploymentByWorkloadName_FindsHelmReleasePrefixed(t *testing.T) {
	kube := fake.NewSimpleClientset(
		newDeployment("cert-manager", "platform-cert-manager", "cert-manager", 1),
	)
	r := checkDeploymentByWorkloadName(context.Background(), kube, "cert-manager", "cert-manager")
	if r.Status != StatusPass {
		t.Fatalf("expected pass, got %s: %s", r.Status, r.Message)
	}
	if !strings.Contains(r.Message, "Available") {
		t.Fatalf("expected Available in message, got: %s", r.Message)
	}
}

func TestCheckDeploymentByWorkloadName_IgnoresSiblingComponents(t *testing.T) {
	// cert-manager release also ships a cainjector + webhook deployment.
	// Probe must match only the main controller (name=cert-manager).
	kube := fake.NewSimpleClientset(
		newDeployment("cert-manager", "platform-cert-manager", "cert-manager", 1),
		newDeployment("cert-manager", "platform-cert-manager-cainjector", "cainjector", 1),
		newDeployment("cert-manager", "platform-cert-manager-webhook", "webhook", 1),
	)
	r := checkDeploymentByWorkloadName(context.Background(), kube, "cert-manager", "cert-manager")
	if r.Status != StatusPass {
		t.Fatalf("expected pass, got %s: %s", r.Status, r.Message)
	}
}

func TestCheckDeploymentByWorkloadName_NoMatch(t *testing.T) {
	kube := fake.NewSimpleClientset(
		newDeployment("cert-manager", "platform-cert-manager-webhook", "webhook", 1),
	)
	r := checkDeploymentByWorkloadName(context.Background(), kube, "cert-manager", "cert-manager")
	if r.Status != StatusFail {
		t.Fatalf("expected fail when main controller absent, got: %v", r)
	}
}

func TestCheckDeploymentByWorkloadName_NotAvailable(t *testing.T) {
	d := newDeployment("external-secrets", "platform-external-secrets", "external-secrets", 0)
	d.Status.Conditions = []appsv1.DeploymentCondition{
		{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionFalse},
	}
	kube := fake.NewSimpleClientset(d)
	r := checkDeploymentByWorkloadName(context.Background(), kube, "external-secrets", "external-secrets")
	if r.Status != StatusFail {
		t.Fatalf("expected fail when Available=False, got: %v", r)
	}
}
