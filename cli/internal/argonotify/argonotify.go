// Package argonotify answers one question raw YAML leaves ambiguous: is
// ArgoCD's notifications controller actually wired up to send anything, or is
// it running on chart defaults nobody has subscribed to?
//
// The notifications controller ships a catalog of triggers and templates
// compiled into the binary; the argocd-notifications-cm ConfigMap only ever
// carries what an operator layered on top of that default catalog. None of
// it fires, defaults or custom, until something subscribes a trigger to a
// receiver — either the ConfigMap's own "subscriptions" key, which applies to
// every Application, or a notifications.argoproj.io/subscribe.<trigger>.
// <service> annotation on one Application. So the decisive signal for "is
// this in use" is a subscription, not the presence of triggers, templates or
// service credentials — this package reads all of it, but reports the
// subscription state as the verdict.
package argonotify

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"
)

// ApplicationGVR is the ArgoCD Application custom resource.
var ApplicationGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}

// SubscribeAnnotationPrefix marks an Application as subscribed to a
// notification trigger. The suffix is "<trigger>.<service>", neither of which
// this package needs to parse to know a subscription exists.
const SubscribeAnnotationPrefix = "notifications.argoproj.io/subscribe."

const (
	configMapName = "argocd-notifications-cm"
	secretName    = "argocd-notifications-secret"
)

// ConfigMap is what argocd-notifications-cm carries beyond the controller's
// built-in catalog.
type ConfigMap struct {
	Exists bool
	// Triggers and Templates are the trigger.<name>/template.<name> keys the
	// ConfigMap itself defines. Their presence shows customization, but
	// without a subscription none of it ever fires — see Active.
	Triggers  []string
	Templates []string
	// Services are the service.<type>.<name> keys: the delivery channels a
	// subscription would need in order to reach anywhere.
	Services []string
	// Subscriptions is the ConfigMap's own "subscriptions" key, applied to
	// every Application. Non-empty means notifications are active
	// cluster-wide regardless of any Application's own annotations.
	Subscriptions string
}

// HasGlobalSubscriptions reports whether the ConfigMap's own "subscriptions"
// key defines at least one subscription. The key holds a YAML list, so an
// explicit empty list ("[]", or the key set to nothing) has to read the same
// as an absent key — a bare whitespace check would call an explicitly
// emptied list "configured".
func (c ConfigMap) HasGlobalSubscriptions() bool {
	trimmed := strings.TrimSpace(c.Subscriptions)
	if trimmed == "" {
		return false
	}
	var list []any
	if err := yaml.Unmarshal([]byte(c.Subscriptions), &list); err == nil {
		return len(list) > 0
	}
	// Content present but not a parseable list is not nothing — reporting it
	// as unconfigured would be the same silent-optimism this package exists
	// to avoid, so it counts as configured instead.
	return true
}

// Secret is what argocd-notifications-secret carries. Only key names are
// read, never values — platformctl has no business handling delivery
// credentials, and a key name (e.g. "slack-token") is not itself a secret.
type Secret struct {
	Exists bool
	Keys   []string
}

// Subscription is one Application carrying at least one subscribe annotation.
type Subscription struct {
	Namespace   string
	Name        string
	Annotations []string
}

// Ref is the namespace/name an operator greps for.
func (s Subscription) Ref() string { return s.Namespace + "/" + s.Name }

// Status is the whole answer: what the ConfigMap and Secret carry, plus every
// Application that has actually subscribed to a trigger.
type Status struct {
	Namespace     string
	ConfigMap     ConfigMap
	Secret        Secret
	Subscriptions []Subscription
}

// Active reports whether anything would actually cause the controller to send
// a notification: a cluster-wide subscription in the ConfigMap, or at least
// one Application annotated to receive one. Triggers, templates and service
// credentials being present is not sufficient on its own — none of them
// fires without a subscription binding a trigger to a receiver.
func (s Status) Active() bool {
	return s.ConfigMap.HasGlobalSubscriptions() || len(s.Subscriptions) > 0
}

// Load reads the notifications ConfigMap and Secret from namespace, and scans
// every Application in it for a subscribe annotation. Read-only: two Get
// calls and one List.
func Load(ctx context.Context, kube kubernetes.Interface, dyn dynamic.Interface, namespace string) (Status, error) {
	status := Status{Namespace: namespace}

	cm, err := kube.CoreV1().ConfigMaps(namespace).Get(ctx, configMapName, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		// Absent entirely reads as inactive by construction: nothing can
		// override chart defaults that were never even written out.
	case err != nil:
		return status, fmt.Errorf("get configmap %s/%s: %w", namespace, configMapName, err)
	default:
		status.ConfigMap = convertConfigMap(cm)
	}

	secret, err := kube.CoreV1().Secrets(namespace).Get(ctx, secretName, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return status, fmt.Errorf("get secret %s/%s: %w", namespace, secretName, err)
	default:
		status.Secret = convertSecret(secret)
	}

	apps, err := dyn.Resource(ApplicationGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return status, fmt.Errorf("list applications.argoproj.io: %w", err)
	}
	for i := range apps.Items {
		item := apps.Items[i]
		var matched []string
		for k := range item.GetAnnotations() {
			if strings.HasPrefix(k, SubscribeAnnotationPrefix) {
				matched = append(matched, k)
			}
		}
		if len(matched) == 0 {
			continue
		}
		sort.Strings(matched)
		status.Subscriptions = append(status.Subscriptions, Subscription{
			Namespace: item.GetNamespace(), Name: item.GetName(), Annotations: matched,
		})
	}
	sort.Slice(status.Subscriptions, func(i, j int) bool {
		return status.Subscriptions[i].Ref() < status.Subscriptions[j].Ref()
	})

	return status, nil
}

func convertConfigMap(cm *corev1.ConfigMap) ConfigMap {
	out := ConfigMap{Exists: true}
	for k, v := range cm.Data {
		switch {
		case k == "subscriptions":
			out.Subscriptions = v
		case strings.HasPrefix(k, "trigger."):
			out.Triggers = append(out.Triggers, k)
		case strings.HasPrefix(k, "template."):
			out.Templates = append(out.Templates, k)
		case strings.HasPrefix(k, "service."):
			out.Services = append(out.Services, k)
		}
	}
	sort.Strings(out.Triggers)
	sort.Strings(out.Templates)
	sort.Strings(out.Services)
	return out
}

func convertSecret(s *corev1.Secret) Secret {
	out := Secret{Exists: true}
	for k := range s.Data {
		out.Keys = append(out.Keys, k)
	}
	sort.Strings(out.Keys)
	return out
}
