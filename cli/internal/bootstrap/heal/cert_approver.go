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
