package heal

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/jdwlabs/platform/internal/k8s"
)

// kindToGVR maps well-known ArgoCD resource kinds to their GVRs.
var kindToGVR = map[string]schema.GroupVersionResource{
	"applicationset": {Group: "argoproj.io", Version: "v1alpha1", Resource: "applicationsets"},
	"application":    {Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"},
	"appproject":     {Group: "argoproj.io", Version: "v1alpha1", Resource: "appprojects"},
}

// StuckOptions controls which resources get their finalizers stripped.
type StuckOptions struct {
	Namespace string
	Kind      string
	Name      string
}

// StripStuck removes finalizers from a stuck ArgoCD resource so Kubernetes
// can complete its deletion. Returns error if the kind is not recognized.
func StripStuck(ctx context.Context, dyn dynamic.Interface, opts StuckOptions) error {
	gvr, ok := kindToGVR[opts.Kind]
	if !ok {
		return fmt.Errorf("unknown kind %q; supported: applicationset, application, appproject", opts.Kind)
	}
	return k8s.StripFinalizers(ctx, dyn, gvr, opts.Namespace, opts.Name)
}
