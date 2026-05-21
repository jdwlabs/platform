package bootstrap

import (
	"context"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/jdwlabs/platform/internal/k8s"
)

var (
	convergeGVRExternalSecret = schema.GroupVersionResource{Group: "external-secrets.io", Version: "v1", Resource: "externalsecrets"}
	convergeGVRCertificate    = schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
	convergeGVRApplication    = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
)

// esoTargets lists the platform ExternalSecrets that must sync before
// cert-manager can issue TLS certificates and platform apps become Healthy.
var esoTargets = []struct{ name, ns string }{
	{"porkbun", "cert-manager"},
	{"longhorn", "longhorn-system"},
	{"grafana-admin-credentials", "monitoring"},
	{"alertmanager-config", "monitoring"},
	{"rclone-gdrive", "database"},
}

// ConvergePhase waits for the cluster to reach a usable state after the
// bootstrap phases complete. It gates sequentially:
//
//  1. ESO secrets synced     (5 min)  — porkbun needed for DNS-01
//  2. Wildcard TLS cert Ready (15 min) — cert-manager DNS-01 validation
//  3. Platform apps Healthy  (10 min) — platform-* ArgoCD apps
type ConvergePhase struct {
	kube    kubernetes.Interface
	dc      dynamic.Interface
	onEvent func(status, msg string)
}

func NewConvergePhase(kube kubernetes.Interface, dc dynamic.Interface) *ConvergePhase {
	return &ConvergePhase{kube: kube, dc: dc}
}

func (p *ConvergePhase) SetOnEvent(f func(status, msg string)) { p.onEvent = f }
func (p *ConvergePhase) Name() string                          { return "converge" }
func (p *ConvergePhase) Number() int                           { return 6 }

func (p *ConvergePhase) emit(status, msg string) {
	if p.onEvent != nil {
		p.onEvent(status, msg)
	}
}

func (p *ConvergePhase) Detect(ctx context.Context) (State, error) {
	if pending := p.pendingESO(ctx); len(pending) > 0 {
		return StateNotStarted, nil
	}
	if !p.certReady(ctx) {
		return StateNotStarted, nil
	}
	if degraded := p.degradedPlatformApps(ctx); len(degraded) > 0 {
		return StateNotStarted, nil
	}
	return StateAlreadyDone, nil
}

func (p *ConvergePhase) Apply(ctx context.Context) error {
	gates := []struct {
		name     string
		timeout  time.Duration
		interval time.Duration
		poll     func(context.Context) (bool, string)
	}{
		{
			name: "ESO secrets", timeout: 5 * time.Minute, interval: 15 * time.Second,
			poll: func(ctx context.Context) (bool, string) {
				pending := p.pendingESO(ctx)
				if len(pending) == 0 {
					return true, "all ESO secrets synced"
				}
				return false, "pending: " + strings.Join(pending, ", ")
			},
		},
		{
			name: "TLS cert", timeout: 15 * time.Minute, interval: 30 * time.Second,
			poll: func(ctx context.Context) (bool, string) {
				if p.certReady(ctx) {
					return true, "wildcard-jdwlabs Ready"
				}
				return false, p.certStatus(ctx)
			},
		},
		{
			name: "platform apps", timeout: 10 * time.Minute, interval: 20 * time.Second,
			poll: func(ctx context.Context) (bool, string) {
				degraded := p.degradedPlatformApps(ctx)
				if len(degraded) == 0 {
					return true, "all platform apps Healthy"
				}
				return false, "degraded: " + strings.Join(degraded, ", ")
			},
		},
	}

	for _, g := range gates {
		deadline, cancel := context.WithTimeout(ctx, g.timeout)
		err := k8s.WaitFor(deadline, g.interval, func(ctx context.Context) (bool, error) {
			done, msg := g.poll(ctx)
			if done {
				p.emit("ok", g.name+": "+msg)
				return true, nil
			}
			p.emit("progressing", g.name+": "+msg)
			return false, nil
		})
		cancel()
		if err != nil {
			return fmt.Errorf("converge gate %q timed out: %w", g.name, err)
		}
	}
	return nil
}

func (p *ConvergePhase) Verify(ctx context.Context) error {
	st, err := p.Detect(ctx)
	if err != nil {
		return err
	}
	if st != StateAlreadyDone {
		return fmt.Errorf("converge: cluster not in healthy state after apply")
	}
	return nil
}

func (p *ConvergePhase) pendingESO(ctx context.Context) []string {
	var pending []string
	for _, t := range esoTargets {
		obj, err := p.dc.Resource(convergeGVRExternalSecret).Namespace(t.ns).Get(ctx, t.name, metav1.GetOptions{})
		if err != nil || !convergeConditionTrue(obj, "Ready") {
			pending = append(pending, t.name+"/"+t.ns)
		}
	}
	return pending
}

func (p *ConvergePhase) certReady(ctx context.Context) bool {
	obj, err := p.dc.Resource(convergeGVRCertificate).Namespace("nginx-gateway").Get(ctx, "wildcard-jdwlabs", metav1.GetOptions{})
	return err == nil && convergeConditionTrue(obj, "Ready")
}

func (p *ConvergePhase) certStatus(ctx context.Context) string {
	obj, err := p.dc.Resource(convergeGVRCertificate).Namespace("nginx-gateway").Get(ctx, "wildcard-jdwlabs", metav1.GetOptions{})
	if err != nil {
		return "cert not found"
	}
	conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, c := range conditions {
		cond, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if cond["type"] == "Ready" {
			reason, _ := cond["reason"].(string)
			msg, _ := cond["message"].(string)
			if reason != "" {
				return reason + ": " + msg
			}
			return msg
		}
	}
	return "condition not yet set"
}

func (p *ConvergePhase) degradedPlatformApps(ctx context.Context) []string {
	list, err := p.dc.Resource(convergeGVRApplication).Namespace("argocd").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil
	}
	var degraded []string
	for _, item := range list.Items {
		name := item.GetName()
		if !strings.HasPrefix(name, "platform-") {
			continue
		}
		health, _, _ := unstructured.NestedString(item.Object, "status", "health", "status")
		if health == "Degraded" || health == "Missing" || health == "" {
			degraded = append(degraded, name)
		}
	}
	return degraded
}

func convergeConditionTrue(obj *unstructured.Unstructured, condType string) bool {
	conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, c := range conditions {
		cond, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if cond["type"] == condType {
			return cond["status"] == "True"
		}
	}
	return false
}
