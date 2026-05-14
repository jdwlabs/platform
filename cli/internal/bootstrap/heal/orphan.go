package heal

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// systemNamespaces are never deleted regardless of tenant ownership.
var systemNamespaces = map[string]bool{
	"argocd":           true,
	"cert-manager":     true,
	"database":         true,
	"default":          true,
	"external-secrets": true,
	"kube-node-lease":  true,
	"kube-public":      true,
	"kube-system":      true,
	"longhorn-system":  true,
	"monitoring":       true,
}

// DeleteOrphanNamespaces deletes namespaces that carry the platform tenant
// label (jdwlabs.io/tenant) but whose name is not in the allowed set.
// Returns the list of deleted namespace names.
func DeleteOrphanNamespaces(ctx context.Context, c kubernetes.Interface, allowed map[string]bool) ([]string, error) {
	nsList, err := c.CoreV1().Namespaces().List(ctx, metav1.ListOptions{
		LabelSelector: "jdwlabs.io/tenant",
	})
	if err != nil {
		return nil, fmt.Errorf("list namespaces: %w", err)
	}

	var deleted []string
	for _, ns := range nsList.Items {
		name := ns.Name
		if systemNamespaces[name] || allowed[name] {
			continue
		}
		if err := c.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
			return deleted, fmt.Errorf("delete namespace %s: %w", name, err)
		}
		deleted = append(deleted, name)
	}
	return deleted, nil
}
