package heal

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ReissueTLS deletes all TLS secrets in the given namespace that are owned by
// cert-manager (label cert-manager.io/certificate-name exists). cert-manager
// detects the deletion and reissues the certificate.
func ReissueTLS(ctx context.Context, c kubernetes.Interface, namespace string) ([]string, error) {
	secrets, err := c.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "cert-manager.io/certificate-name",
	})
	if err != nil {
		return nil, fmt.Errorf("list tls secrets in %s: %w", namespace, err)
	}

	var deleted []string
	for _, s := range secrets.Items {
		if err := c.CoreV1().Secrets(namespace).Delete(ctx, s.Name, metav1.DeleteOptions{}); err != nil {
			return deleted, fmt.Errorf("delete secret %s/%s: %w", namespace, s.Name, err)
		}
		deleted = append(deleted, s.Name)
	}
	return deleted, nil
}
