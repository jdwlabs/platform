package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"

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
	nonInteractive bool
}

func NewVaultInitPhase(kube kubernetes.Interface, b vault.Builder, nonInteractive bool) *VaultInitPhase {
	return &VaultInitPhase{kube: kube, builder: b, nonInteractive: nonInteractive}
}

func (p *VaultInitPhase) Name() string { return "vault-init" }
func (p *VaultInitPhase) Number() int  { return 3 }

func (p *VaultInitPhase) Detect(ctx context.Context) (State, error) {
	c, err := p.builder.New(ctx, "")
	if err != nil {
		return StateUnknown, fmt.Errorf("vault connect: %w", err)
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
