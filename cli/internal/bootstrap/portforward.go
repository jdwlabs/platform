package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"

	"github.com/jdwlabs/platform/internal/vault"
)

// VaultAddrResolver resolves the vault HTTP address, automatically starting a
// port-forward tunnel when the configured address is an in-cluster DNS name
// (contains ".svc"). The tunnel is started once and reused across phases 3-5.
type VaultAddrResolver struct {
	defaultAddr string
	restCfg     *rest.Config
	kube        kubernetes.Interface

	mu           sync.Mutex
	resolvedAddr string
	pfStop       func()
}

func NewVaultAddrResolver(defaultAddr string, restCfg *rest.Config, kube kubernetes.Interface) *VaultAddrResolver {
	return &VaultAddrResolver{defaultAddr: defaultAddr, restCfg: restCfg, kube: kube}
}

// Resolve returns the vault address to use. On first call with an in-cluster
// DNS address, it starts an automatic port-forward and returns the local addr.
func (r *VaultAddrResolver) Resolve(ctx context.Context) (string, error) {
	if r.restCfg == nil || !strings.Contains(r.defaultAddr, ".svc") {
		return r.defaultAddr, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.resolvedAddr != "" {
		return r.resolvedAddr, nil
	}
	addr, stop, err := startPodPortForward(ctx, r.restCfg, r.kube, "vault", "app.kubernetes.io/name=vault", 8200)
	if err != nil {
		return "", fmt.Errorf("auto port-forward vault: %w", err)
	}
	r.resolvedAddr = addr
	r.pfStop = stop
	return addr, nil
}

// NewClient creates a vault client using the resolved address. If token is
// empty, it tries to read the root token from the vault-init k8s secret
// (written by phase 3), falling back to PLATFORMCTL_VAULT_TOKEN env var.
func (r *VaultAddrResolver) NewClient(ctx context.Context, token string) (*vault.Client, error) {
	addr, err := r.Resolve(ctx)
	if err != nil {
		return nil, err
	}
	if token == "" {
		token = r.resolveToken(ctx)
	}
	return vault.NewClient(addr, token)
}

// Stop closes the port-forward tunnel if one was started.
func (r *VaultAddrResolver) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pfStop != nil {
		r.pfStop()
		r.pfStop = nil
	}
}

func (r *VaultAddrResolver) resolveToken(ctx context.Context) string {
	if r.kube != nil {
		if tok, err := r.vaultTokenFromSecret(ctx); err == nil && tok != "" {
			return tok
		}
	}
	return os.Getenv("PLATFORMCTL_VAULT_TOKEN")
}

func (r *VaultAddrResolver) vaultTokenFromSecret(ctx context.Context) (string, error) {
	s, err := r.kube.CoreV1().Secrets("vault").Get(ctx, "vault-init", metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	var result struct {
		RootToken string `json:"root_token"`
	}
	if err := json.Unmarshal(s.Data["vault-init.json"], &result); err != nil {
		return "", err
	}
	return result.RootToken, nil
}

// startPodPortForward finds a running pod in namespace matching labelSelector,
// opens a 0→remotePort tunnel via SPDY, and returns the bound local
// http://localhost:PORT address plus a cleanup func.
func startPodPortForward(ctx context.Context, restCfg *rest.Config, kube kubernetes.Interface, namespace, labelSelector string, remotePort int) (string, func(), error) {
	pods, err := kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		return "", nil, fmt.Errorf("list pods %s/%s: %w", namespace, labelSelector, err)
	}
	var podName string
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning {
			podName = pod.Name
			break
		}
	}
	if podName == "" {
		return "", nil, fmt.Errorf("no running pod in namespace %s matching %q", namespace, labelSelector)
	}

	transport, upgrader, err := spdy.RoundTripperFor(restCfg)
	if err != nil {
		return "", nil, fmt.Errorf("spdy roundtripper: %w", err)
	}

	hostURL, err := url.Parse(restCfg.Host)
	if err != nil {
		return "", nil, fmt.Errorf("parse kube host: %w", err)
	}
	hostURL.Path = fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/portforward", namespace, podName)

	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, hostURL)

	stopChan := make(chan struct{})
	readyChan := make(chan struct{})
	pf, err := portforward.New(dialer, []string{fmt.Sprintf("0:%d", remotePort)}, stopChan, readyChan, nil, nil)
	if err != nil {
		return "", nil, fmt.Errorf("create portforward: %w", err)
	}

	errChan := make(chan error, 1)
	go func() { errChan <- pf.ForwardPorts() }()

	select {
	case <-readyChan:
	case fwErr := <-errChan:
		return "", nil, fmt.Errorf("portforward: %w", fwErr)
	case <-ctx.Done():
		close(stopChan)
		return "", nil, ctx.Err()
	}

	ports, err := pf.GetPorts()
	if err != nil {
		close(stopChan)
		return "", nil, fmt.Errorf("get bound ports: %w", err)
	}

	var once sync.Once
	stop := func() { once.Do(func() { close(stopChan) }) }
	return fmt.Sprintf("http://localhost:%d", ports[0].Local), stop, nil
}
