package heal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

// ErrSyncInProgress reports that an Application already had an operation in
// flight, so the requested one would never have run. Sentinel rather than a
// plain error because the caller says something different about it: this one
// settles on its own and the sync only has to be re-requested afterwards.
var ErrSyncInProgress = errors.New("a sync is already in progress")

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

// SyncApp starts a sync operation on an ArgoCD Application by setting
// .operation, which is the only thing that makes its hooks run.
//
// RefreshApp is not a weaker version of this, it is a different verb: a hard
// refresh re-renders the manifests and re-runs the comparison, and nothing
// else. An Application whose git and live state are identical then compares
// Synced, no sync operation is created, and its Sync/PostSync hooks do not run
// — hook Jobs are excluded from the diff, so their absence is never the drift
// that would trigger one. Reach for RefreshApp when the Application really is
// out of sync and the only question is how soon ArgoCD notices; reach for this
// when a hook has to run again regardless of drift.
//
// Refuses while an operation is already in flight, because the patch would be
// accepted and then discarded: the controller resumes the existing
// Status.OperationState instead of the .operation just written, and clears
// .operation when that older operation finishes. `argocd app sync` refuses
// this server-side; a raw patch is answered with a plain success.
func SyncApp(ctx context.Context, dyn dynamic.Interface, appName string) error {
	app, err := dyn.Resource(appGVR).Namespace("argocd").Get(ctx, appName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("read application/%s: %w", appName, err)
	}
	if inProgress(app) {
		return fmt.Errorf("%w on application/%s", ErrSyncInProgress, appName)
	}
	patch, err := json.Marshal(map[string]interface{}{
		"operation": map[string]interface{}{
			"initiatedBy": map[string]interface{}{"username": "platformctl"},
			"sync": map[string]interface{}{
				// The hook strategy is the point of the operation: the apply
				// alternative skips hooks, which is exactly what has to run.
				"syncStrategy": map[string]interface{}{"hook": map[string]interface{}{}},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("marshal sync operation: %w", err)
	}
	if _, err := dyn.Resource(appGVR).Namespace("argocd").Patch(
		ctx, appName, types.MergePatchType, patch, metav1.PatchOptions{},
	); err != nil {
		return fmt.Errorf("request sync of application/%s: %w", appName, err)
	}
	return nil
}

// inProgress covers both halves of the controller's own check: a pending
// .operation it has not picked up yet, and a Status.OperationState it is still
// running. Either one swallows a newly requested sync.
func inProgress(app *unstructured.Unstructured) bool {
	if _, found, _ := unstructured.NestedMap(app.Object, "operation"); found {
		return true
	}
	phase, _, _ := unstructured.NestedString(app.Object, "status", "operationState", "phase")
	return phase == "Running"
}
