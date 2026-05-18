package k8s

import (
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

// NewClient returns a real Kubernetes client built from the user's kubeconfig.
// Honors $KUBECONFIG; falls back to ~/.kube/config.
func NewClient() (kubernetes.Interface, error) {
	cfg, err := buildConfig()
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(cfg)
}

// NewDynamic returns a dynamic client for working with custom resources.
func NewDynamic() (dynamic.Interface, error) {
	cfg, err := buildConfig()
	if err != nil {
		return nil, err
	}
	return dynamic.NewForConfig(cfg)
}

// NewRestConfig returns the REST config used to build k8s clients.
// Useful for operations that need raw REST access (e.g., port-forwarding).
func NewRestConfig() (*rest.Config, error) { return buildConfig() }

func buildConfig() (*rest.Config, error) {
	path := os.Getenv("KUBECONFIG")
	if path == "" {
		if home := homedir.HomeDir(); home != "" {
			path = filepath.Join(home, ".kube", "config")
		}
	}
	if path == "" {
		return nil, fmt.Errorf("no kubeconfig found (set KUBECONFIG or place at ~/.kube/config)")
	}
	return clientcmd.BuildConfigFromFlags("", path)
}
