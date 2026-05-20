package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/jdwlabs/platform/internal/vault"
)

// VaultInitPhase runs vault operator init, persists keys to k8s secrets,
// enables the kv-v2 secrets engine, and applies the ESO ClusterSecretStore
// so cert-manager can resolve DNS-01 challenges without waiting for ArgoCD.
type VaultInitPhase struct {
	kube      kubernetes.Interface
	dc        dynamic.Interface // nil in tests; skips ClusterSecretStore apply
	resolver  *VaultAddrResolver
	backupDir string                  // directory for vault-init.json backup; defaults to .secrets/
	lastMsg   string                  // set by vaultPodReady; read by ProgressMessage
	onEvent   func(status, msg string) // wired by bootstrap.go so non-fatal warns appear in the event stream
}

func NewVaultInitPhase(kube kubernetes.Interface, dc dynamic.Interface, resolver *VaultAddrResolver, backupDir string) *VaultInitPhase {
	if backupDir == "" {
		backupDir = ".secrets"
	}
	return &VaultInitPhase{kube: kube, dc: dc, resolver: resolver, backupDir: backupDir}
}

// SetOnEvent wires a callback so Apply() can emit non-fatal warnings into the
// caller's event stream rather than falling back to fmt.Printf.
func (p *VaultInitPhase) SetOnEvent(f func(status, msg string)) { p.onEvent = f }

// warn emits a non-fatal warning via onEvent when wired, otherwise stderr.
func (p *VaultInitPhase) warn(msg string) {
	if p.onEvent != nil {
		p.onEvent("progressing", "warn: "+msg)
	} else {
		fmt.Println("warn:", msg)
	}
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

	c, err := p.resolver.NewClient(ctx, "")
	if err != nil {
		return StateUnknown, err
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
			// Check container is started (Running state), not cs.Ready — Vault's
			// readiness probe fails until after initialization, creating a deadlock
			// if we wait for Ready before calling Init.
			allStarted := true
			for _, cs := range pod.Status.ContainerStatuses {
				if cs.State.Running == nil {
					allStarted = false
					if cs.State.Waiting != nil {
						p.lastMsg = fmt.Sprintf("pod %s: container %s waiting (%s)", pod.Name, cs.Name, cs.State.Waiting.Reason)
					} else {
						p.lastMsg = fmt.Sprintf("pod %s: container %s not yet started", pod.Name, cs.Name)
					}
				}
			}
			if allStarted {
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
	c, err := p.resolver.NewClient(ctx, "")
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
	if err := c.EnableKVv2(ctx, "kv"); err != nil {
		return fmt.Errorf("enable kv: %w", err)
	}
	if err := p.applyVaultAdminBootstrap(ctx, c); err != nil {
		return fmt.Errorf("admin bootstrap: %w", err)
	}

	initJSON, err := json.Marshal(res)
	if err != nil {
		return fmt.Errorf("marshal vault init result: %w", err)
	}
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
	if err := p.backupInitJSON(initJSON); err != nil {
		// Non-fatal: cluster secret is the authoritative copy.
		p.warn(fmt.Sprintf("could not write local backup: %v", err))
	}
	// Apply the ESO ClusterSecretStore immediately so cert-manager can resolve
	// DNS-01 challenges without waiting for the ArgoCD vault app to sync.
	// (ArgoCD vault sync fails until the wildcard cert exists, which requires
	// the ClusterSecretStore to exist — circular dependency broken here.)
	if err := p.applyVaultClusterSecretStore(ctx); err != nil {
		p.warn(fmt.Sprintf("ClusterSecretStore/vault not applied (ESO CRD may not be ready yet — will self-heal once ArgoCD syncs vault app): %v", err))
	}
	return nil
}

// vaultAdminPolicyHCL grants full access to Vault. Bound to the
// auth/kubernetes/role/vault-admin role used by in-cluster operators (mainly
// db-ui and ARC runners) that need to seed or rotate tenant secrets.
const vaultAdminPolicyHCL = `path "/*" {
  capabilities = ["create", "read", "update", "delete", "list", "sudo"]
}
`

// applyVaultAdminBootstrap configures Vault's kubernetes auth backend, writes
// the vault-admin policy + role, and enables the database secrets engine. This
// runs from platformctl with the root token (held in memory from Init), so we
// avoid the cross-namespace secret coupling and sync-wave fragility of the
// old postInstall vault-admin-initializer Job.
//
// Vault auto-discovers its in-cluster k8s CA + reviewer JWT from its own pod's
// SA mount when token_reviewer_jwt and kubernetes_ca_cert are omitted (Vault
// 1.9+ default behavior), so we only need kubernetes_host.
func (p *VaultInitPhase) applyVaultAdminBootstrap(ctx context.Context, c *vault.Client) error {
	if err := c.EnableAuthMethod(ctx, "kubernetes", "kubernetes"); err != nil {
		return fmt.Errorf("enable kubernetes auth: %w", err)
	}
	if err := c.Write(ctx, "auth/kubernetes/config", map[string]any{
		"kubernetes_host": "https://kubernetes.default.svc:443",
	}); err != nil {
		return fmt.Errorf("write auth/kubernetes/config: %w", err)
	}
	if err := c.PutPolicy(ctx, "vault-admin", vaultAdminPolicyHCL); err != nil {
		return fmt.Errorf("put policy vault-admin: %w", err)
	}
	if err := c.Write(ctx, "auth/kubernetes/role/vault-admin", map[string]any{
		"bound_service_account_names":      "default",
		"bound_service_account_namespaces": "default",
		"policies":                         "vault-admin",
		"ttl":                              "1h",
	}); err != nil {
		return fmt.Errorf("write auth/kubernetes/role/vault-admin: %w", err)
	}
	if err := c.EnableSecretsEngine(ctx, "database", "database"); err != nil {
		return fmt.Errorf("enable database secrets engine: %w", err)
	}
	return nil
}

// applyVaultClusterSecretStore applies the ESO ClusterSecretStore that points
// at Vault. This must happen before ArgoCD can successfully sync the vault app
// (which fails until the wildcard TLS cert exists, which requires this CSS to
// exist so ESO can sync the porkbun webhook secret to cert-manager).
func (p *VaultInitPhase) applyVaultClusterSecretStore(ctx context.Context) error {
	if p.dc == nil {
		return nil // test environment
	}
	css := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "external-secrets.io/v1",
			"kind":       "ClusterSecretStore",
			"metadata":   map[string]any{"name": "vault"},
			"spec": map[string]any{
				"provider": map[string]any{
					"vault": map[string]any{
						"server":  "http://platform-vault.vault.svc.cluster.local:8200",
						"path":    "kv",
						"version": "v2",
						"auth": map[string]any{
							"tokenSecretRef": map[string]any{
								"name":      "vault-token",
								"namespace": "external-secrets",
								"key":       "token",
							},
						},
					},
				},
			},
		},
	}
	gvr := schema.GroupVersionResource{Group: "external-secrets.io", Version: "v1", Resource: "clustersecretstores"}
	_, err := p.dc.Resource(gvr).Apply(ctx, "vault", css, metav1.ApplyOptions{FieldManager: "platformctl", Force: true})
	return err
}

// backupInitJSON writes vault-init.json to p.backupDir so the operator has an
// offline copy of unseal keys and root token. The directory is gitignored.
func (p *VaultInitPhase) backupInitJSON(data []byte) error {
	if err := os.MkdirAll(p.backupDir, 0700); err != nil {
		return err
	}
	dest := filepath.Join(p.backupDir, "vault-init.json")
	return os.WriteFile(dest, data, 0600)
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
