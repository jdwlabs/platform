package k8s

import (
	"context"
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

// StripFinalizers removes all finalizers from the named resource via MergePatch.
func StripFinalizers(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, namespace, name string) error {
	patch, err := json.Marshal(map[string]interface{}{
		"metadata": map[string]interface{}{"finalizers": nil},
	})
	if err != nil {
		return fmt.Errorf("marshal patch: %w", err)
	}
	_, err = dyn.Resource(gvr).Namespace(namespace).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("strip finalizers %s/%s: %w", namespace, name, err)
	}
	return nil
}
