package heal

import (
	"context"
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

var appGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}

// appName is the ArgoCD Application name for kubelet-serving-cert-approver.
// The platform ApplicationSet prefixes all platform apps with "platform-".
const appName = "platform-kubelet-serving-cert-approver"

// RefreshApp patches an ArgoCD Application with a hard-refresh annotation,
// causing ArgoCD to re-sync it from live cluster state rather than its cache.
// Used to re-run a Sync-hook Job after the resources it creates were deleted.
func RefreshApp(ctx context.Context, dyn dynamic.Interface, name string) error {
	patch, err := json.Marshal(map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": map[string]interface{}{
				"argocd.argoproj.io/refresh": "hard",
			},
		},
	})
	if err != nil {
		return fmt.Errorf("marshal refresh patch: %w", err)
	}
	if _, err := dyn.Resource(appGVR).Namespace("argocd").Patch(
		ctx, name, types.MergePatchType, patch, metav1.PatchOptions{},
	); err != nil {
		return fmt.Errorf("patch application/%s: %w", name, err)
	}
	return nil
}

// RefreshCertApprover patches the argocd Application for
// kubelet-serving-cert-approver with a hard-refresh annotation, causing ArgoCD
// to re-sync from the live cluster state rather than its cache.
func RefreshCertApprover(ctx context.Context, dyn dynamic.Interface) error {
	patch, err := json.Marshal(map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": map[string]interface{}{
				"argocd.argoproj.io/refresh": "hard",
			},
		},
	})
	if err != nil {
		return fmt.Errorf("marshal refresh patch: %w", err)
	}
	_, err = dyn.Resource(appGVR).Namespace("argocd").Patch(
		ctx, appName, types.MergePatchType, patch, metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("patch application/%s: %w", appName, err)
	}
	return nil
}
