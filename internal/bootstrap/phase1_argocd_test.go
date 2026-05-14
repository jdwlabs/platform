package bootstrap

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/jdwlabs/platform/internal/helm"
	"github.com/jdwlabs/platform/internal/k8s"
)

func TestArgocdInstallPhase_DetectAlreadyDone(t *testing.T) {
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "argocd-server", Namespace: "argocd"},
		Status: appsv1.DeploymentStatus{
			Conditions: []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
			},
		},
	}
	c := k8s.NewFake(d)
	p := NewArgocdInstallPhase(c, &helm.FakeRunner{}, "")
	st, err := p.Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st != StateAlreadyDone {
		t.Fatalf("got %s", st)
	}
}

func TestArgocdInstallPhase_DetectNotStarted(t *testing.T) {
	c := k8s.NewFake()
	p := NewArgocdInstallPhase(c, &helm.FakeRunner{}, "")
	st, err := p.Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st != StateNotStarted {
		t.Fatalf("got %s", st)
	}
}

func TestArgocdInstallPhase_DetectInProgress(t *testing.T) {
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "argocd-server", Namespace: "argocd"},
		// No Available condition set → InProgress
	}
	c := k8s.NewFake(d)
	p := NewArgocdInstallPhase(c, &helm.FakeRunner{}, "")
	st, err := p.Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st != StateInProgress {
		t.Fatalf("got %s", st)
	}
}

func TestArgocdInstallPhase_Apply_CallsHelm(t *testing.T) {
	fake := &helm.FakeRunner{}
	p := NewArgocdInstallPhase(k8s.NewFake(), fake, "values.yaml")
	if err := p.Apply(context.Background()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(fake.Calls) != 1 || fake.Calls[0] != "platform-argo-cd/argo/argo-cd" {
		t.Fatalf("unexpected calls: %v", fake.Calls)
	}
}
