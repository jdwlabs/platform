package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/jdwlabs/platform/internal/bootstrap"
	"github.com/jdwlabs/platform/internal/bootstrap/heal"
	"github.com/jdwlabs/platform/internal/display"
	"github.com/jdwlabs/platform/internal/gitsync"
	"github.com/jdwlabs/platform/internal/k8s"
)

const (
	grafanaNamespace     = "monitoring"
	grafanaPodSelector   = "app.kubernetes.io/name=grafana"
	grafanaPort          = 3000
	grafanaDefaultAddr   = "http://platform-grafana.monitoring.svc"
	grafanaAdminSecret   = "grafana-admin-credentials"
	grafanaApplication   = "platform-grafana"
	grafanaAddrEnv       = "PLATFORMCTL_GRAFANA_ADDR"
	grafanaUserEnv       = "PLATFORMCTL_GRAFANA_ADMIN_USER"
	grafanaPasswordEnv   = "PLATFORMCTL_GRAFANA_ADMIN_PASSWORD"
	gitSyncDefaultFields = "kind,name,healthy,syncState"
)

var gitSyncFields = []string{"kind", "name", "healthy", "syncState", "health", "connection"}

type gitSyncGlobals struct {
	Addr      string
	Namespace string
}

func newGitSyncCmd(g *Globals) *cobra.Command {
	shared := &gitSyncGlobals{}

	cmd := &cobra.Command{
		Use:   "gitsync",
		Short: "Inspect and reset Grafana Git Sync provisioning resources",
		Long: `Report and reset Grafana's Git Sync Connection and Repository.

These resources live in Grafana's own API server, not Kubernetes: kubectl cannot
see them and ArgoCD cannot reconcile them, so their health blocks are the only
place a failing sync surfaces. Neither health message names its own cause — a
connection reporting "GitHub App lacks required 'webhooks' permission" is
describing a requirement derived from a bound repository's workflows, not a
missing grant on the App.

The apply Job that creates these resources never updates them, so changing a
definition means deleting the resource and letting the next sync recreate it.

Run with no subcommand to report status.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGitSyncStatus(cmd, g, shared, &gitSyncStatusOptions{})
		},
	}
	cmd.PersistentFlags().StringVar(&shared.Addr, "grafana-addr", "",
		"Grafana base URL; an in-cluster .svc address is reached by an automatic port-forward (env "+grafanaAddrEnv+")")
	cmd.PersistentFlags().StringVar(&shared.Namespace, "namespace", gitsync.DefaultNamespace,
		"Grafana provisioning namespace (not the Kubernetes namespace Grafana runs in)")

	cmd.AddCommand(newGitSyncStatusCmd(g, shared))
	cmd.AddCommand(newGitSyncDeleteCmd(g, shared))
	cmd.AddCommand(newGitSyncRecreateCmd(g, shared))
	return cmd
}

type gitSyncStatusOptions struct {
	Fields string
	Full   bool
}

func newGitSyncStatusCmd(g *Globals, shared *gitSyncGlobals) *cobra.Command {
	opts := &gitSyncStatusOptions{}
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Report health and sync state for every Git Sync resource",
		Long: `Report each Connection and Repository with its health and sync state.

Exits non-zero when any resource is unhealthy, when a resource reports no health
at all, or when no resources exist — an empty collection means Git Sync is
credentialed but not connected, which is a finding rather than a clean result.`,
		Example: `  platformctl gitsync status
  platformctl gitsync status --full`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGitSyncStatus(cmd, g, shared, opts)
		},
	}
	cmd.Flags().StringVar(&opts.Fields, "fields", "",
		"comma-separated fields to report instead of "+gitSyncDefaultFields)
	cmd.Flags().BoolVar(&opts.Full, "full", false, "report every field, including the full health message")
	return cmd
}

type gitSyncDeleteOptions struct {
	Kind                string
	Name                string
	Confirm             bool
	AllowOwnedDashboard bool
}

func newGitSyncDeleteCmd(g *Globals, shared *gitSyncGlobals) *cobra.Command {
	opts := &gitSyncDeleteOptions{}
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete one Git Sync resource so the next sync recreates it (requires --confirm)",
		Long: `Delete a single Connection or Repository. Destructive, so --confirm is
required and --dry-run previews; neither is assumed and stdin is never read.

Two preconditions are enforced rather than documented:

  * a repository still owning dashboards is refused, because its
    remove-orphan-resources finalizer collects whatever it owns on delete
  * a connection still referenced by a repository is refused, because the
    repository must be deleted first

Recreation is the apply Job's job — it runs on the next sync of the Grafana
Application. Use "gitsync recreate" to do both in the enforced order.`,
		Example: `  platformctl gitsync delete --kind repository --name platform-dashboards --dry-run
  platformctl gitsync delete --kind repository --name platform-dashboards --confirm`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGitSyncDelete(cmd, g, shared, opts)
		},
	}
	cmd.Flags().StringVar(&opts.Kind, "kind", "", "resource kind: "+strings.Join(gitsync.Kinds(), " or "))
	cmd.Flags().StringVar(&opts.Name, "name", "", "resource name")
	cmd.Flags().BoolVar(&opts.Confirm, "confirm", false, "actually delete the resource")
	cmd.Flags().BoolVar(&opts.AllowOwnedDashboard, "allow-owned-dashboards", false,
		"proceed even though the repository owns dashboards the finalizer will collect")
	bindDryRun(cmd, g)
	return cmd
}

type gitSyncRecreateOptions struct {
	Repository          string
	Confirm             bool
	AllowOwnedDashboard bool
	NoSync              bool
}

func newGitSyncRecreateCmd(g *Globals, shared *gitSyncGlobals) *cobra.Command {
	opts := &gitSyncRecreateOptions{}
	cmd := &cobra.Command{
		Use:   "recreate",
		Short: "Delete repository then connection in order and ask ArgoCD to re-run the apply Job",
		Long: `Delete a repository and the connection it binds to, in that order, then
request an ArgoCD refresh of the Grafana Application so its apply Job recreates
both from the definitions in git.

This is the only supported way to change a Git Sync resource definition: the
apply Job creates but never updates, so merging an edit alone changes nothing.

Destructive, so --confirm is required and --dry-run previews. The repository is
resolved automatically when exactly one exists; otherwise name it with
--repository.`,
		Example: `  platformctl gitsync recreate --dry-run
  platformctl gitsync recreate --confirm
  platformctl gitsync recreate --repository platform-dashboards --confirm`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGitSyncRecreate(cmd, g, shared, opts)
		},
	}
	cmd.Flags().StringVar(&opts.Repository, "repository", "",
		"repository to recreate; required when more than one exists")
	cmd.Flags().BoolVar(&opts.Confirm, "confirm", false, "actually delete both resources")
	cmd.Flags().BoolVar(&opts.AllowOwnedDashboard, "allow-owned-dashboards", false,
		"proceed even though the repository owns dashboards the finalizer will collect")
	cmd.Flags().BoolVar(&opts.NoSync, "no-sync", false,
		"skip the ArgoCD refresh; the resources stay absent until the next sync")
	bindDryRun(cmd, g)
	return cmd
}

func runGitSyncStatus(cmd *cobra.Command, g *Globals, shared *gitSyncGlobals, opts *gitSyncStatusOptions) error {
	out := cmd.OutOrStdout()

	fields, err := resolveGitSyncFields(opts.Fields, opts.Full)
	if err != nil {
		return reportCLIError(out, err, "Run `platformctl gitsync status --help` for the field list")
	}

	client, stop, err := newGitSyncClient(cmd.Context(), shared)
	if err != nil {
		return reportCLIError(out, err, "Set "+grafanaAddrEnv+" to reach Grafana directly, or check KUBECONFIG")
	}
	defer stop()

	resources, err := readAllGitSyncResources(cmd.Context(), client)
	if err != nil {
		return reportCLIError(out, err, "Run `platformctl cluster status` to check Grafana itself")
	}

	healthy, unhealthy, unknown := countGitSyncHealth(resources)
	summary := fmt.Sprintf("%d total (%d healthy / %d unhealthy / %d health unknown)",
		len(resources), healthy, unhealthy, unknown)

	if g.JSON {
		em := NewEmitter(out, g.JSON)
		if g.Session != nil {
			em.SetSession(g.Session)
		}
		for _, r := range resources {
			status := "ok"
			if !r.Healthy {
				status = "broken"
			}
			em.Emit(Event{
				Phase:   "gitsync",
				Name:    r.Kind + "/" + r.Name,
				Status:  status,
				Message: r.HealthMessage,
				Detail: map[string]string{
					"kind": r.Kind, "name": r.Name,
					"healthy": fmt.Sprintf("%t", r.Healthy), "syncState": r.SyncState,
				},
			})
		}
		em.Emit(Event{Phase: "gitsync", Name: "summary", Status: gitSyncSummaryStatus(resources), Message: summary})
	} else {
		if err := display.ToonScalar(out, "count", summary); err != nil {
			return err
		}
		if err := display.ToonTable(out, "gitsync", fields, gitSyncRows(resources, fields)); err != nil {
			return err
		}
		// The default field set keeps the table narrow, so the health message —
		// the only place the real failure is written down — is printed in full
		// for anything that is not healthy.
		if bad := unhealthyResources(resources); len(bad) > 0 {
			if err := display.ToonTable(out, "unhealthy", []string{"kind", "name", "health"},
				gitSyncRows(bad, []string{"kind", "name", "health"})); err != nil {
				return err
			}
		}
		if err := display.ToonScalar(out, "result", gitSyncResultLine(resources, healthy, unhealthy, unknown)); err != nil {
			return err
		}
		if unhealthy+unknown > 0 {
			if err := display.ToonList(out, "help", []string{
				"Run `platformctl gitsync status --full` for the whole health and sync payload",
				"Run `platformctl gitsync recreate --dry-run` if the fix is a definition change",
			}); err != nil {
				return err
			}
		}
	}

	if len(resources) == 0 {
		return fmt.Errorf("git sync is credentialed but not connected: no connections or repositories exist")
	}
	if unhealthy+unknown > 0 {
		return fmt.Errorf("%d of %d git sync resource(s) unhealthy", unhealthy+unknown, len(resources))
	}
	return nil
}

func runGitSyncDelete(cmd *cobra.Command, g *Globals, shared *gitSyncGlobals, opts *gitSyncDeleteOptions) error {
	out := cmd.OutOrStdout()

	if err := requireGitSyncIntent(opts.Confirm, g.DryRun); err != nil {
		return reportCLIError(out, err,
			"Run `platformctl gitsync delete --kind <kind> --name <name> --dry-run` first, then re-run with --confirm")
	}
	if opts.Kind == "" || opts.Name == "" {
		return reportCLIError(out,
			fmt.Errorf("--kind and --name are both required"),
			"Run `platformctl gitsync status` to see the kinds and names that exist")
	}
	if !containsString(gitsync.Kinds(), opts.Kind) {
		return reportCLIError(out,
			fmt.Errorf("unknown --kind %s; valid: %s", opts.Kind, strings.Join(gitsync.Kinds(), ", ")),
			"Run `platformctl gitsync status` to see the kinds that exist")
	}

	client, stop, err := newGitSyncClient(cmd.Context(), shared)
	if err != nil {
		return reportCLIError(out, err, "Set "+grafanaAddrEnv+" to reach Grafana directly, or check KUBECONFIG")
	}
	defer stop()

	resources, err := readAllGitSyncResources(cmd.Context(), client)
	if err != nil {
		return reportCLIError(out, err, "Run `platformctl cluster status` to check Grafana itself")
	}
	if !gitSyncExists(resources, opts.Kind, opts.Name) {
		if err := display.ToonScalar(out, "result",
			fmt.Sprintf("%s/%s is already absent (no-op)", opts.Kind, opts.Name)); err != nil {
			return err
		}
		return nil
	}

	if err := checkGitSyncDeletable(cmd.Context(), client, resources, opts.Kind, opts.Name, opts.AllowOwnedDashboard); err != nil {
		return reportCLIError(out, err, gitSyncRefusalHelp(opts.Kind))
	}

	mode := "delete"
	if g.DryRun {
		mode = "dry-run"
	}
	if err := display.ToonScalar(out, "mode", mode); err != nil {
		return err
	}
	if err := display.ToonTable(out, "target", []string{"kind", "name"}, [][]string{{opts.Kind, opts.Name}}); err != nil {
		return err
	}
	if g.DryRun {
		if err := display.ToonScalar(out, "result",
			fmt.Sprintf("would delete %s/%s — nothing was mutated", opts.Kind, opts.Name)); err != nil {
			return err
		}
		return display.ToonList(out, "help", []string{"Re-run with --confirm to delete it"})
	}
	if err := client.Delete(cmd.Context(), opts.Kind, opts.Name); err != nil {
		return reportCLIError(out, err, "Run `platformctl gitsync status` to see whether it survived")
	}
	if err := display.ToonScalar(out, "result", fmt.Sprintf("deleted %s/%s", opts.Kind, opts.Name)); err != nil {
		return err
	}
	return display.ToonList(out, "help", []string{
		"The apply Job recreates it on the next sync of the Grafana Application",
		"Run `platformctl gitsync status` afterwards to confirm it came back healthy",
	})
}

func runGitSyncRecreate(cmd *cobra.Command, g *Globals, shared *gitSyncGlobals, opts *gitSyncRecreateOptions) error {
	out := cmd.OutOrStdout()

	if err := requireGitSyncIntent(opts.Confirm, g.DryRun); err != nil {
		return reportCLIError(out, err,
			"Run `platformctl gitsync recreate --dry-run` first, then re-run with --confirm")
	}

	client, stop, err := newGitSyncClient(cmd.Context(), shared)
	if err != nil {
		return reportCLIError(out, err, "Set "+grafanaAddrEnv+" to reach Grafana directly, or check KUBECONFIG")
	}
	defer stop()

	repos, err := client.List(cmd.Context(), gitsync.KindRepository)
	if err != nil {
		return reportCLIError(out, err, "Run `platformctl cluster status` to check Grafana itself")
	}
	repo, err := pickRepository(repos, opts.Repository)
	if err != nil {
		return reportCLIError(out, err, "Run `platformctl gitsync status` to see which repositories exist")
	}

	owned, err := client.DashboardsOwnedBy(cmd.Context(), repo.Name)
	if err != nil {
		return reportCLIError(out, err,
			"Ownership could not be verified, so nothing was deleted; retry once Grafana answers")
	}
	if len(owned) > 0 && !opts.AllowOwnedDashboard {
		return reportCLIError(out,
			fmt.Errorf("repository %s owns %d dashboard(s): %s", repo.Name, len(owned), strings.Join(owned, " ")),
			"Deleting it lets the remove-orphan-resources finalizer collect them; pass --allow-owned-dashboards only if that is intended")
	}

	// Ordering is the whole reason this command exists: the repository
	// references the connection, so deleting the connection first strands it.
	steps := [][]string{{"1", gitsync.KindRepository, repo.Name}}
	if repo.ConnectionRef != "" {
		steps = append(steps, []string{"2", gitsync.KindConnection, repo.ConnectionRef})
	}

	mode := "delete"
	if g.DryRun {
		mode = "dry-run"
	}
	if err := display.ToonScalar(out, "mode", mode); err != nil {
		return err
	}
	if err := display.ToonTable(out, "order", []string{"step", "kind", "name"}, steps); err != nil {
		return err
	}
	if g.DryRun {
		if err := display.ToonScalar(out, "result",
			fmt.Sprintf("would delete %d resource(s) in this order — nothing was mutated", len(steps))); err != nil {
			return err
		}
		return display.ToonList(out, "help", []string{"Re-run with --confirm to delete them and request a resync"})
	}

	for _, step := range steps {
		if err := client.Delete(cmd.Context(), step[1], step[2]); err != nil {
			return reportCLIError(out, err, "Run `platformctl gitsync status` to see what is left")
		}
	}

	resync := "skipped (--no-sync)"
	if !opts.NoSync {
		dc, derr := volumeDynamicClient()
		if derr == nil {
			derr = heal.RefreshApp(cmd.Context(), dc, grafanaApplication)
		}
		if derr != nil {
			// The deletes already happened, so a failed refresh is a warning with
			// a manual follow-up, not a reason to report the whole command failed.
			resync = "not requested: " + derr.Error()
		} else {
			resync = "requested for " + grafanaApplication
		}
	}
	if err := display.ToonScalar(out, "result",
		fmt.Sprintf("deleted %d resource(s) in order; ArgoCD refresh %s", len(steps), resync)); err != nil {
		return err
	}
	return display.ToonList(out, "help", []string{
		"Run `platformctl gitsync status` after the sync to confirm both are healthy again",
		"A definition change only takes effect if it is merged before the apply Job re-runs",
	})
}

// requireGitSyncIntent refuses the ambiguous invocations: neither flag given, or
// both. Nothing is inferred, and nothing is read from stdin.
func requireGitSyncIntent(confirm, dryRun bool) error {
	if confirm && dryRun {
		return fmt.Errorf("--confirm and --dry-run are mutually exclusive")
	}
	if !confirm && !dryRun {
		return fmt.Errorf("this command needs --confirm to delete or --dry-run to preview")
	}
	return nil
}

func checkGitSyncDeletable(ctx context.Context, client *gitsync.Client, resources []gitsync.Resource,
	kind, name string, allowOwned bool) error {
	switch kind {
	case gitsync.KindRepository:
		owned, err := client.DashboardsOwnedBy(ctx, name)
		if err != nil {
			return err
		}
		if len(owned) > 0 && !allowOwned {
			return fmt.Errorf("repository %s owns %d dashboard(s): %s", name, len(owned), strings.Join(owned, " "))
		}
	case gitsync.KindConnection:
		var repos []gitsync.Resource
		for _, r := range resources {
			if r.Kind == gitsync.KindRepository {
				repos = append(repos, r)
			}
		}
		if bound := gitsync.RepositoriesUsingConnection(repos, name); len(bound) > 0 {
			return fmt.Errorf("connection %s is still referenced by repository %s", name, strings.Join(bound, " "))
		}
	}
	return nil
}

func gitSyncRefusalHelp(kind string) string {
	if kind == gitsync.KindConnection {
		return "Delete the repository first — `platformctl gitsync recreate --confirm` does both in order"
	}
	return "Pass --allow-owned-dashboards only if losing those dashboards is intended"
}

func pickRepository(repos []gitsync.Resource, wanted string) (gitsync.Resource, error) {
	if wanted != "" {
		for _, r := range repos {
			if r.Name == wanted {
				return r, nil
			}
		}
		return gitsync.Resource{}, fmt.Errorf("no repository named %s exists", wanted)
	}
	switch len(repos) {
	case 0:
		return gitsync.Resource{}, fmt.Errorf("no repositories exist, so there is nothing to recreate")
	case 1:
		return repos[0], nil
	default:
		names := make([]string, 0, len(repos))
		for _, r := range repos {
			names = append(names, r.Name)
		}
		return gitsync.Resource{}, fmt.Errorf("%d repositories exist, so --repository is required: %s",
			len(repos), strings.Join(names, " "))
	}
}

func readAllGitSyncResources(ctx context.Context, client *gitsync.Client) ([]gitsync.Resource, error) {
	var out []gitsync.Resource
	for _, kind := range gitsync.Kinds() {
		res, err := client.List(ctx, kind)
		if err != nil {
			return nil, err
		}
		out = append(out, res...)
	}
	return out, nil
}

func gitSyncExists(resources []gitsync.Resource, kind, name string) bool {
	for _, r := range resources {
		if r.Kind == kind && r.Name == name {
			return true
		}
	}
	return false
}

func countGitSyncHealth(resources []gitsync.Resource) (healthy, unhealthy, unknown int) {
	for _, r := range resources {
		switch {
		case !r.HealthKnown:
			unknown++
		case r.Healthy:
			healthy++
		default:
			unhealthy++
		}
	}
	return healthy, unhealthy, unknown
}

func unhealthyResources(resources []gitsync.Resource) []gitsync.Resource {
	var out []gitsync.Resource
	for _, r := range resources {
		if !r.Healthy {
			out = append(out, r)
		}
	}
	return out
}

func gitSyncResultLine(resources []gitsync.Resource, healthy, unhealthy, unknown int) string {
	if len(resources) == 0 {
		return "0 resources — git sync is credentialed but not connected"
	}
	if unhealthy+unknown == 0 {
		return fmt.Sprintf("0 issues — all %d resource(s) healthy", healthy)
	}
	return fmt.Sprintf("%d of %d resource(s) unhealthy", unhealthy+unknown, len(resources))
}

func gitSyncSummaryStatus(resources []gitsync.Resource) string {
	_, unhealthy, unknown := countGitSyncHealth(resources)
	if len(resources) == 0 || unhealthy+unknown > 0 {
		return "broken"
	}
	return "ok"
}

func resolveGitSyncFields(csv string, full bool) ([]string, error) {
	if full && csv != "" {
		return nil, fmt.Errorf("--fields and --full are mutually exclusive")
	}
	if full {
		return gitSyncFields, nil
	}
	if csv == "" {
		return strings.Split(gitSyncDefaultFields, ","), nil
	}
	var fields []string
	for _, part := range strings.Split(csv, ",") {
		f := strings.TrimSpace(part)
		if f == "" {
			continue
		}
		if !containsString(gitSyncFields, f) {
			return nil, fmt.Errorf("unknown field %s; valid: %s", f, strings.Join(gitSyncFields, ", "))
		}
		fields = append(fields, f)
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("--fields is empty; valid: %s", strings.Join(gitSyncFields, ", "))
	}
	return fields, nil
}

func gitSyncRows(resources []gitsync.Resource, fields []string) [][]string {
	rows := make([][]string, 0, len(resources))
	for _, r := range resources {
		row := make([]string, 0, len(fields))
		for _, f := range fields {
			row = append(row, gitSyncFieldValue(r, f))
		}
		rows = append(rows, row)
	}
	return rows
}

func gitSyncFieldValue(r gitsync.Resource, field string) string {
	switch field {
	case "kind":
		return r.Kind
	case "name":
		return r.Name
	case "healthy":
		if !r.HealthKnown {
			return "unknown"
		}
		return fmt.Sprintf("%t", r.Healthy)
	case "syncState":
		if r.SyncState == "" {
			return "none"
		}
		return r.SyncState
	case "health":
		return r.HealthMessage
	case "connection":
		return r.ConnectionRef
	default:
		return ""
	}
}

// newGitSyncClient resolves the Grafana address and admin credentials. An
// in-cluster .svc address does not resolve from a workstation, so it is reached
// through an automatic port-forward, mirroring how the Vault commands work.
func newGitSyncClient(ctx context.Context, shared *gitSyncGlobals) (*gitsync.Client, func(), error) {
	noop := func() {}

	addr := shared.Addr
	if addr == "" {
		addr = os.Getenv(grafanaAddrEnv)
	}
	if addr == "" {
		addr = grafanaDefaultAddr
	}

	kc, err := volumeKubeClient()
	if err != nil {
		return nil, noop, err
	}

	user, pass, err := grafanaCredentials(ctx, kc)
	if err != nil {
		return nil, noop, err
	}

	stop := noop
	if strings.Contains(addr, ".svc") {
		restCfg, err := k8s.NewRestConfig()
		if err != nil {
			return nil, noop, fmt.Errorf("rest config: %w", err)
		}
		local, cancel, err := bootstrap.StartPortForward(ctx, restCfg, kc,
			grafanaNamespace, grafanaPodSelector, grafanaPort)
		if err != nil {
			return nil, noop, fmt.Errorf("auto port-forward grafana: %w", err)
		}
		addr, stop = local, cancel
	}
	return gitsync.NewClient(addr, user, pass, shared.Namespace), stop, nil
}

// grafanaCredentials prefers the environment (so a CI run needs no cluster read)
// and otherwise reads the same secret the apply Job uses.
func grafanaCredentials(ctx context.Context, kc kubernetes.Interface) (string, string, error) {
	if user, pass := os.Getenv(grafanaUserEnv), os.Getenv(grafanaPasswordEnv); user != "" && pass != "" {
		return user, pass, nil
	}
	sec, err := kc.CoreV1().Secrets(grafanaNamespace).Get(ctx, grafanaAdminSecret, metav1.GetOptions{})
	if err != nil {
		return "", "", fmt.Errorf("read secret %s/%s: %w", grafanaNamespace, grafanaAdminSecret, err)
	}
	user := string(sec.Data["admin-user"])
	pass := string(sec.Data["admin-password"])
	if user == "" || pass == "" {
		return "", "", fmt.Errorf("secret %s/%s carries no admin-user/admin-password",
			grafanaNamespace, grafanaAdminSecret)
	}
	return user, pass, nil
}

func containsString(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
