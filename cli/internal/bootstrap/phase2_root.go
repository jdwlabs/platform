package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"

	"github.com/jdwlabs/platform/internal/k8s"
)

var (
	appGVR    = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	appSetGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applicationsets"}
)

// RootApplyPhase applies bootstrap/root-app.yaml and waits for the cascade.
type RootApplyPhase struct {
	kube     kubernetes.Interface
	dyn      dynamic.Interface
	branch   string
	manifest string
	lastMsg  string // set by Detect; read by ProgressMessage
}

func NewRootApplyPhase(kube kubernetes.Interface, dyn dynamic.Interface, branch, manifestPath string) *RootApplyPhase {
	return &RootApplyPhase{kube: kube, dyn: dyn, branch: branch, manifest: manifestPath}
}

func (p *RootApplyPhase) Name() string { return "root-apply" }
func (p *RootApplyPhase) Number() int  { return 2 }

// ProgressMessage implements ProgressMessenger.
func (p *RootApplyPhase) ProgressMessage(_ context.Context) string { return p.lastMsg }

func (p *RootApplyPhase) Detect(ctx context.Context) (State, error) {
	app, err := p.dyn.Resource(appGVR).Namespace("argocd").Get(ctx, "bootstrap", metav1.GetOptions{})
	if err != nil {
		return StateNotStarted, nil
	}
	// Stuck AppSet → broken
	if appset, err := p.dyn.Resource(appSetGVR).Namespace("argocd").Get(ctx, "platform-services", metav1.GetOptions{}); err == nil {
		if appset.GetDeletionTimestamp() != nil {
			return StateBroken, fmt.Errorf("applicationset/platform-services stuck terminating; run `platformctl bootstrap heal --stuck-finalizer`")
		}
	}
	sync, _, _ := unstructured.NestedString(app.Object, "status", "sync", "status")
	health, _, _ := unstructured.NestedString(app.Object, "status", "health", "status")
	// Healthy = all managed resources are operational; OutOfSync on its own
	// (e.g. Helm-chart CRD version drift) does not block platform readiness.
	if health == "Healthy" {
		return StateAlreadyDone, nil
	}
	p.lastMsg = fmt.Sprintf("bootstrap app: sync=%s health=%s", sync, health)
	return StateInProgress, nil
}

func (p *RootApplyPhase) Apply(ctx context.Context) error {
	raw, err := os.ReadFile(p.manifest)
	if err != nil {
		return fmt.Errorf("read %s: %w", p.manifest, err)
	}
	if p.branch != "" {
		raw, err = PatchRootAppBranch(raw, p.branch)
		if err != nil {
			return err
		}
	}
	asJSON, err := yaml.YAMLToJSON(raw)
	if err != nil {
		return fmt.Errorf("yaml→json: %w", err)
	}
	var obj unstructured.Unstructured
	if err := json.Unmarshal(asJSON, &obj); err != nil {
		return err
	}
	_, err = p.dyn.Resource(appGVR).Namespace("argocd").Patch(
		ctx, obj.GetName(), types.MergePatchType, asJSON, metav1.PatchOptions{FieldManager: "platformctl"},
	)
	if err != nil {
		// Resource may not exist yet — try create.
		_, err = p.dyn.Resource(appGVR).Namespace("argocd").Create(ctx, &obj, metav1.CreateOptions{})
	}
	return err
}

func (p *RootApplyPhase) Verify(ctx context.Context) error {
	deadline, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	// Wait for bootstrap App to be Synced+Healthy.
	if err := k8s.WaitFor(deadline, 10*time.Second, func(ctx context.Context) (bool, error) {
		st, _ := p.Detect(ctx)
		return st == StateAlreadyDone, nil
	}); err != nil {
		return err
	}
	// Then wait for repo-server after self-managed ArgoCD upgrade restarts pods.
	return VerifyRootApplied(deadline, p.kube, p.dyn)
}

// PatchRootAppBranch rewrites spec.source.targetRevision in the YAML manifest.
func PatchRootAppBranch(in []byte, branch string) ([]byte, error) {
	asJSON, err := yaml.YAMLToJSON(in)
	if err != nil {
		return nil, err
	}
	obj := &unstructured.Unstructured{}
	if err := obj.UnmarshalJSON(asJSON); err != nil {
		return nil, err
	}
	if err := unstructured.SetNestedField(obj.Object, branch, "spec", "source", "targetRevision"); err != nil {
		return nil, err
	}
	out, err := obj.MarshalJSON()
	if err != nil {
		return nil, err
	}
	return yaml.JSONToYAML(out)
}
