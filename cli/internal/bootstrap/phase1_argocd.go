package bootstrap

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/jdwlabs/platform/internal/helm"
)

// ArgocdInstallPhase installs ArgoCD via helm upgrade --install.
type ArgocdInstallPhase struct {
	kube       kubernetes.Interface
	runner     helm.Runner
	valuesPath string
}

func NewArgocdInstallPhase(kube kubernetes.Interface, runner helm.Runner, valuesPath string) *ArgocdInstallPhase {
	if runner == nil {
		runner = helm.ExecRunner{}
	}
	return &ArgocdInstallPhase{kube: kube, runner: runner, valuesPath: valuesPath}
}

func (p *ArgocdInstallPhase) Name() string  { return "argocd-install" }
func (p *ArgocdInstallPhase) Number() int   { return 1 }

func (p *ArgocdInstallPhase) Detect(ctx context.Context) (State, error) {
	d, err := p.kube.AppsV1().Deployments("argocd").Get(ctx, "argocd-server", metav1.GetOptions{})
	if err != nil {
		return StateNotStarted, nil
	}
	for _, c := range d.Status.Conditions {
		if c.Type == appsv1.DeploymentAvailable && c.Status == corev1.ConditionTrue {
			return StateAlreadyDone, nil
		}
	}
	return StateInProgress, nil
}

func (p *ArgocdInstallPhase) Apply(ctx context.Context) error {
	opts := helm.InstallOpts{
		Namespace: "argocd",
		SetValues: map[string]string{"global.trackingMethod": "annotation"},
	}
	if p.valuesPath != "" {
		opts.ValuesFiles = []string{p.valuesPath}
	}
	if err := p.runner.UpgradeInstall(ctx, "platform-argo-cd", "argo/argo-cd", opts); err != nil {
		return fmt.Errorf("argocd helm install: %w", err)
	}
	return nil
}

func (p *ArgocdInstallPhase) Verify(ctx context.Context) error {
	deadline, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	return VerifyArgocdReady(deadline, p.kube)
}
