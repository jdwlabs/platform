package heal

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

var longhornAppGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}

const (
	longhornNS      = "longhorn-system"
	longhornSA      = "longhorn-service-account"
	longhornCRB     = "longhorn-pre-upgrade-admin"
	longhornAppName = "platform-longhorn"
)

// LonghornFreshInstall creates the namespace, ServiceAccount, and temporary
// ClusterRoleBinding that Longhorn's pre-upgrade hook requires on a fresh install.
// On subsequent syncs ArgoCD replaces chart-managed equivalents.
//
// Root cause: Longhorn's Helm chart defines a pre-upgrade PreSync hook that runs
// the longhorn-manager image. ArgoCD fires PreSync hooks before applying any chart
// resources, so the hook pod can't find the SA or its RBAC on a green-field cluster.
func LonghornFreshInstall(ctx context.Context, kube kubernetes.Interface, dyn dynamic.Interface) error {
	if err := ensureLonghornNamespace(ctx, kube); err != nil {
		return err
	}
	if err := ensureLonghornSA(ctx, kube); err != nil {
		return err
	}
	if err := ensureLonghornCRB(ctx, kube); err != nil {
		return err
	}
	// Trigger ArgoCD refresh only if the app already exists; non-fatal if not.
	_ = triggerArgoRefresh(ctx, dyn, "argocd", longhornAppName)
	return nil
}

func ensureLonghornNamespace(ctx context.Context, kube kubernetes.Interface) error {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: longhornNS},
	}
	_, err := kube.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create longhorn-system namespace: %w", err)
	}
	return nil
}

func ensureLonghornSA(ctx context.Context, kube kubernetes.Interface) error {
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: longhornSA, Namespace: longhornNS},
	}
	_, err := kube.CoreV1().ServiceAccounts(longhornNS).Create(ctx, sa, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create longhorn SA: %w", err)
	}
	return nil
}

func ensureLonghornCRB(ctx context.Context, kube kubernetes.Interface) error {
	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: longhornCRB},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "cluster-admin",
		},
		Subjects: []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      longhornSA,
			Namespace: longhornNS,
		}},
	}
	_, err := kube.RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create longhorn pre-upgrade CRB: %w", err)
	}
	return nil
}

func triggerArgoRefresh(ctx context.Context, dyn dynamic.Interface, namespace, appName string) error {
	patch := []byte(`{"metadata":{"annotations":{"argocd.argoproj.io/refresh":"hard"}}}`)
	_, err := dyn.Resource(longhornAppGVR).Namespace(namespace).Patch(
		ctx, appName, types.MergePatchType, patch, metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("refresh argocd app %s: %w", appName, err)
	}
	return nil
}
