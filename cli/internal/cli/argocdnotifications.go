package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jdwlabs/platform/internal/argonotify"
	"github.com/jdwlabs/platform/internal/display"
)

type argoCDNotificationsOptions struct {
	Namespace string
}

func newClusterArgoCDNotificationsCmd(g *Globals) *cobra.Command {
	opts := &argoCDNotificationsOptions{}
	cmd := &cobra.Command{
		Use:   "argocd-notifications",
		Short: "Report whether ArgoCD's notifications controller has anything actually subscribed",
		Long: `Read argocd-notifications-cm and argocd-notifications-secret, and scan every
Application for a subscribe annotation, to answer one question raw YAML makes
tedious: is this controller wired up to send anything, or is it running on
chart defaults nobody has subscribed to?

The notifications controller ships a catalog of triggers and templates
compiled into the binary. The ConfigMap only ever carries what an operator
layered on top of that — and none of it fires, defaults or custom, until
something subscribes a trigger to a receiver. That happens exactly two ways:
the ConfigMap's own "subscriptions" key, which applies to every Application,
or a notifications.argoproj.io/subscribe.<trigger>.<service> annotation on one
Application. This command reports both, alongside what the ConfigMap and
Secret carry as corroborating evidence, and states the yes/no verdict
directly rather than leaving it to be inferred from raw YAML.

Read-only.`,
		Example: `  platformctl cluster argocd-notifications
  platformctl cluster argocd-notifications --json
  platformctl cluster argocd-notifications --namespace argocd`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runArgoCDNotifications(cmd, g, opts)
		},
	}
	cmd.Flags().StringVarP(&opts.Namespace, "namespace", "n", "argocd", "namespace ArgoCD is installed in")
	return cmd
}

func runArgoCDNotifications(cmd *cobra.Command, g *Globals, opts *argoCDNotificationsOptions) error {
	out := cmd.OutOrStdout()

	kc, err := volumeKubeClient()
	if err != nil {
		return reportCLIError(out, err, "Check KUBECONFIG points at the cluster")
	}
	dc, err := volumeDynamicClient()
	if err != nil {
		return reportCLIError(out, err, "Check KUBECONFIG points at the cluster")
	}

	status, err := argonotify.Load(cmd.Context(), kc, dc, opts.Namespace)
	if err != nil {
		return reportCLIError(out, err, "Run `platformctl cluster status` to check the cluster is reachable")
	}

	if g.JSON {
		return emitArgoCDNotificationsEvents(out, g, status)
	}
	return writeArgoCDNotificationsReport(out, status)
}

func writeArgoCDNotificationsReport(out io.Writer, status argonotify.Status) error {
	if err := display.ToonScalar(out, "namespace", status.Namespace); err != nil {
		return err
	}
	if err := display.ToonScalar(out, "configmap", configMapSummary(status.ConfigMap)); err != nil {
		return err
	}
	if err := display.ToonScalar(out, "secret", secretSummary(status.Secret)); err != nil {
		return err
	}
	subFields := []string{"application", "annotations"}
	if err := display.ToonTable(out, "subscribed", subFields, subscriptionRows(status.Subscriptions)); err != nil {
		return err
	}
	if err := display.ToonScalar(out, "active", activeLine(status.Active())); err != nil {
		return err
	}
	if err := display.ToonScalar(out, "result", argoCDNotificationsResult(status)); err != nil {
		return err
	}
	return display.ToonList(out, "help", argoCDNotificationsHelp(status))
}

func configMapSummary(cm argonotify.ConfigMap) string {
	if !cm.Exists {
		return "argocd-notifications-cm: not found"
	}
	subs := "empty"
	if cm.HasGlobalSubscriptions() {
		subs = "configured"
	}
	// Slash-separated rather than comma-separated: a comma is the TOON
	// delimiter, so a scalar containing one has to be quoted and the line
	// gets noisier.
	return fmt.Sprintf("argocd-notifications-cm: exists / %d trigger(s) / %d template(s) / %d service(s) / global subscriptions: %s",
		len(cm.Triggers), len(cm.Templates), len(cm.Services), subs)
}

func secretSummary(s argonotify.Secret) string {
	if !s.Exists {
		return "argocd-notifications-secret: not found"
	}
	return fmt.Sprintf("argocd-notifications-secret: exists / %d credential key(s)", len(s.Keys))
}

func subscriptionRows(subs []argonotify.Subscription) [][]string {
	rows := make([][]string, 0, len(subs))
	for _, s := range subs {
		rows = append(rows, []string{s.Ref(), strings.Join(s.Annotations, "; ")})
	}
	return rows
}

func activeLine(active bool) string {
	if active {
		return "yes"
	}
	return "no"
}

func argoCDNotificationsResult(status argonotify.Status) string {
	if !status.Active() {
		return "inactive: no global subscriptions and no Application carries a subscribe annotation " +
			"— whatever is in the ConfigMap is chart defaults nobody has wired up"
	}
	if status.ConfigMap.HasGlobalSubscriptions() && len(status.Subscriptions) > 0 {
		return fmt.Sprintf("active: a global subscription is configured and %d Application(s) also carry a subscribe annotation",
			len(status.Subscriptions))
	}
	if status.ConfigMap.HasGlobalSubscriptions() {
		return "active: the ConfigMap's global subscriptions key is configured, applying to every Application"
	}
	return fmt.Sprintf("active: %d Application(s) carry a notifications.argoproj.io/subscribe annotation",
		len(status.Subscriptions))
}

func argoCDNotificationsHelp(status argonotify.Status) []string {
	var help []string
	if !status.Active() {
		help = append(help, "Nothing found here would fire a notification — "+
			"safe to consider disabling the controller if nothing else depends on it")
	}
	if status.Active() && len(status.ConfigMap.Services) == 0 {
		help = append(help, "No service.* keys are configured in the ConfigMap, "+
			"so a subscription here still has nowhere to deliver to")
	}
	return append(help, "Read-only — nothing was changed")
}

// emitArgoCDNotificationsEvents mirrors the TOON report onto the newline-
// delimited event stream the repo's --json contract defines.
func emitArgoCDNotificationsEvents(out io.Writer, g *Globals, status argonotify.Status) error {
	em := NewEmitter(out, g.JSON)
	if g.Session != nil {
		em.SetSession(g.Session)
	}
	em.Emit(Event{
		Phase: "argocd-notifications", Name: "configmap", Status: "info",
		Message: configMapSummary(status.ConfigMap),
		Detail: map[string]string{
			"exists":           fmt.Sprintf("%t", status.ConfigMap.Exists),
			"triggers":         fmt.Sprintf("%d", len(status.ConfigMap.Triggers)),
			"templates":        fmt.Sprintf("%d", len(status.ConfigMap.Templates)),
			"services":         fmt.Sprintf("%d", len(status.ConfigMap.Services)),
			"globalSubscribed": fmt.Sprintf("%t", status.ConfigMap.HasGlobalSubscriptions()),
		},
	})
	em.Emit(Event{
		Phase: "argocd-notifications", Name: "secret", Status: "info",
		Message: secretSummary(status.Secret),
		Detail: map[string]string{
			"exists": fmt.Sprintf("%t", status.Secret.Exists),
			"keys":   fmt.Sprintf("%d", len(status.Secret.Keys)),
		},
	})
	for _, s := range status.Subscriptions {
		em.Emit(Event{
			Phase: "argocd-notifications", Name: "subscription", Status: "info",
			Message: fmt.Sprintf("%s subscribed via %s", s.Ref(), strings.Join(s.Annotations, ", ")),
			Detail: map[string]string{
				"application": s.Ref(),
				"annotations": strings.Join(s.Annotations, ","),
			},
		})
	}
	em.Emit(Event{
		Phase: "argocd-notifications", Name: "summary", Status: "ok",
		Message: argoCDNotificationsResult(status),
		Detail:  map[string]string{"active": fmt.Sprintf("%t", status.Active())},
	})
	return nil
}
