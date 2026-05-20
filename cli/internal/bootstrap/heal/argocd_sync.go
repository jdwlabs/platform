package heal

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

// TerminateStuckSync nulls out the in-progress operation on an ArgoCD
// Application, unblocking syncs that are stuck waiting for a Helm hook Job
// that has already been deleted (e.g. cert-generator with ttlSecondsAfterFinished=30).
func TerminateStuckSync(ctx context.Context, dyn dynamic.Interface, appName string) error {
	_, err := dyn.Resource(appGVR).Namespace("argocd").Patch(
		ctx, appName, types.MergePatchType,
		[]byte(`{"operation":null}`),
		metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("terminate operation on application/%s: %w", appName, err)
	}
	return nil
}
