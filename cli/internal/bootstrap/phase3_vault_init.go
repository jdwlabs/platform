package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/jdwlabs/platform/internal/prompt"
	"github.com/jdwlabs/platform/internal/vault"
)

// VaultInitPhase runs vault operator init, persists keys to k8s secrets, and
// enables the kv-v2 secrets engine.
type VaultInitPhase struct {
	kube           kubernetes.Interface
	builder        vault.Builder
	vaultAddr      string
	nonInteractive bool
	lastMsg        string // set by vaultPodReady; read by ProgressMessage
}

func NewVaultInitPhase(kube kubernetes.Interface, b vault.Builder, vaultAddr string, nonInteractive bool) *VaultInitPhase {
	return &VaultInitPhase{kube: kube, builder: b, vaultAddr: vaultAddr, nonInteractive: nonInteractive}
}

func (p *VaultInitPhase) Name() string { return "vault-init" }
func (p *VaultInitPhase) Number() int  { return 3 }

// ProgressMessage implements ProgressMessenger, surfacing live pod status in the
// cascade's "waiting (X elapsed)" log lines.
func (p *VaultInitPhase) ProgressMessage(_ context.Context) string { return p.lastMsg }

func (p *VaultInitPhase) Detect(ctx context.Context) (State, error) {
	// Check pod readiness before attempting HTTP — avoids misleading network errors
	// while vault is still starting up.
	ready, err := p.vaultPodReady(ctx)
	if err != nil {
		return StateUnknown, err
	}
	if !ready {
		return StateInProgress, nil
	}

	c, err := p.builder.New(ctx, "")
	if err != nil {
		return StateUnknown, p.vaultConnectError(err)
	}
	initialized, err := c.IsInitialized(ctx)
	if err != nil {
		return StateUnknown, err
	}
	if !initialized {
		return StateNotStarted, nil
	}
	if _, err := p.kube.CoreV1().Secrets("external-secrets").Get(ctx, "vault-token", metav1.GetOptions{}); err != nil {
		return StateBroken, fmt.Errorf("vault initialized but vault-token secret missing — re-run to recover or restore from backup")
	}
	return StateAlreadyDone, nil
}

// vaultPodReady returns true when at least one vault pod (label app.kubernetes.io/name=vault
// in namespace vault) is Running and all its containers are Ready.
// It sets p.lastMsg on every call so ProgressMessage can surface live status.
// Returns a non-nil error only for unrecoverable states (CrashLoopBackOff, API errors).
func (p *VaultInitPhase) vaultPodReady(ctx context.Context) (bool, error) {
	pods, err := p.kube.CoreV1().Pods("vault").List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/name=vault",
	})
	if k8serrors.IsNotFound(err) {
		p.lastMsg = "vault namespace not yet created (ArgoCD deploying wave-1 services)"
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("list vault pods: %w", err)
	}
	if len(pods.Items) == 0 {
		if exists, _ := p.namespaceExists(ctx, "vault"); !exists {
			p.lastMsg = "vault namespace not yet created (ArgoCD deploying wave-1 services)"
		} else {
			p.lastMsg = "vault pod not yet created (ArgoCD deploying wave-2 services)"
		}
		return false, nil
	}
	for _, pod := range pods.Items {
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.State.Waiting != nil && cs.State.Waiting.Reason == "CrashLoopBackOff" {
				return false, fmt.Errorf("vault pod %s in CrashLoopBackOff — check logs: kubectl logs -n vault %s", pod.Name, pod.Name)
			}
		}
		if pod.Status.Phase == corev1.PodRunning {
			allReady := true
			for _, cs := range pod.Status.ContainerStatuses {
				if !cs.Ready {
					allReady = false
					if cs.State.Waiting != nil {
						p.lastMsg = fmt.Sprintf("pod %s: container %s waiting (%s)", pod.Name, cs.Name, cs.State.Waiting.Reason)
					} else {
						p.lastMsg = fmt.Sprintf("pod %s: container %s not yet ready", pod.Name, cs.Name)
					}
				}
			}
			if allReady {
				p.lastMsg = ""
				return true, nil
			}
		} else {
			p.lastMsg = fmt.Sprintf("pod %s: phase=%s (waiting for Longhorn PVC?)", pod.Name, pod.Status.Phase)
		}
	}
	return false, nil
}

func (p *VaultInitPhase) namespaceExists(ctx context.Context, name string) (bool, error) {
	_, err := p.kube.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (p *VaultInitPhase) Apply(ctx context.Context) error {
	c, err := p.builder.New(ctx, "")
	if err != nil {
		return err
	}
	res, err := c.Init(ctx, 5, 3)
	if err != nil {
		return fmt.Errorf("init: %w", err)
	}
	if err := c.Unseal(ctx, res.Keys); err != nil {
		return fmt.Errorf("unseal: %w", err)
	}
	c.SetToken(res.RootToken)
	if err := c.EnableKVv2(ctx, "secret"); err != nil {
		return fmt.Errorf("enable kv: %w", err)
	}

	initJSON, _ := json.Marshal(res)
	if err := upsertSecret(ctx, p.kube, "vault", "vault-init", map[string][]byte{
		"vault-init.json": initJSON,
	}); err != nil {
		return err
	}
	if err := upsertSecret(ctx, p.kube, "external-secrets", "vault-token", map[string][]byte{
		"token": []byte(res.RootToken),
	}); err != nil {
		return err
	}
	if !p.nonInteractive {
		_, _ = prompt.Confirm("Vault keys + root token written to cluster. Have you copied vault-init.json offline?", false, false)
	}
	return nil
}

func (p *VaultInitPhase) Verify(ctx context.Context) error {
	st, err := p.Detect(ctx)
	if err != nil {
		return err
	}
	if st != StateAlreadyDone {
		return fmt.Errorf("vault-init verify: state %s", st)
	}
	return nil
}

// vaultConnectError wraps vault connection errors with actionable guidance when
// the address is an in-cluster DNS name that won't resolve from outside the cluster.
func (p *VaultInitPhase) vaultConnectError(err error) error {
	msg := err.Error()
	if strings.Contains(p.vaultAddr, ".svc") &&
		(strings.Contains(msg, "no such host") || strings.Contains(msg, "connection refused")) {
		return fmt.Errorf(
			"vault not reachable at %s (in-cluster DNS — running outside the cluster?)\n"+
				"  Fix: kubectl port-forward svc/platform-vault 8200:8200 -n vault\n"+
				"  Then: export PLATFORMCTL_VAULT_ADDR=http://localhost:8200\n"+
				"  original: %w",
			p.vaultAddr, err,
		)
	}
	return fmt.Errorf("vault connect: %w", err)
}

// upsertSecret creates or updates a k8s Secret.
func upsertSecret(ctx context.Context, c kubernetes.Interface, ns, name string, data map[string][]byte) error {
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Type:       corev1.SecretTypeOpaque,
		Data:       data,
	}
	_, err := c.CoreV1().Secrets(ns).Create(ctx, s, metav1.CreateOptions{})
	if k8serrors.IsAlreadyExists(err) {
		_, err = c.CoreV1().Secrets(ns).Update(ctx, s, metav1.UpdateOptions{})
	}
	return err
}
