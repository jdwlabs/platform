package heal

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/yaml"
)

var appProjectGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "appprojects"}

// ApplyDefaultProject reads the AppProject manifest at path and server-side
// applies it to the cluster. Creates the resource if absent, patches if present.
func ApplyDefaultProject(ctx context.Context, dyn dynamic.Interface, path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	jsonBytes, err := yaml.YAMLToJSON(raw)
	if err != nil {
		return fmt.Errorf("yaml→json %s: %w", path, err)
	}

	var obj unstructured.Unstructured
	if err := json.Unmarshal(jsonBytes, &obj); err != nil {
		return fmt.Errorf("unmarshal %s: %w", path, err)
	}

	ns := obj.GetNamespace()
	name := obj.GetName()
	if ns == "" || name == "" {
		return fmt.Errorf("manifest %s missing namespace or name", path)
	}

	client := dyn.Resource(appProjectGVR).Namespace(ns)
	_, getErr := client.Get(ctx, name, metav1.GetOptions{})
	if k8serrors.IsNotFound(getErr) {
		if _, err = client.Create(ctx, &obj, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create appproject/%s: %w", name, err)
		}
		return nil
	}
	if getErr != nil {
		return fmt.Errorf("get appproject/%s: %w", name, getErr)
	}
	if _, err = client.Patch(ctx, name, types.MergePatchType, jsonBytes, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("patch appproject/%s: %w", name, err)
	}
	return nil
}
