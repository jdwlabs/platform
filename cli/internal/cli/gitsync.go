package cli

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
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
	grafanaNamespace   = "monitoring"
	grafanaPodSelector = "app.kubernetes.io/name=grafana"
	grafanaPort        = 3000
	grafanaDefaultAddr = "http://platform-grafana.monitoring.svc"
	grafanaAdminSecret = "grafana-admin-credentials"
	grafanaApplication = "platform-grafana"
	grafanaAddrEnv     = "PLATFORMCTL_GRAFANA_ADDR"
	// Where a tenant records the git sync folder its team is granted on, and
	// the Application whose PostSync hook is the only thing that re-applies
	// that grant. Both are rendered by helm-charts/tenant-envelope.
	tenantLabel          = "platform.jdwlabs.io/tenant"
	tenantGitSyncFolder  = "gitsync-folder"
	governanceAppPrefix  = "governance-"
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
	AcceptOpenFolder    bool
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
  * a connection still referenced by a repository is refused, because every
    repository on it must be deleted first, and the refusal names them
  * a repository whose folder carries a tenant's RBAC is refused, because the
    folder is deleted with it and comes back with Grafana's inherited grants —
    readable by every user — until that tenant's envelope syncs again

Recreation is the apply Job's job — it runs on the next sync of the Grafana
Application. Use "gitsync recreate" to delete in the enforced order, with
--with-connection when the connection definition itself is what changed.`,
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
	cmd.Flags().BoolVar(&opts.AcceptOpenFolder, "accept-open-folder", false,
		"proceed even though the repository's folder carries a tenant's RBAC and will come back without it")
	bindDryRun(cmd, g)
	return cmd
}

type gitSyncRecreateOptions struct {
	Repository          string
	WithConnection      bool
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
them from the definitions in git.

This is the only supported way to change a Git Sync resource definition: the
apply Job creates but never updates, so merging an edit alone changes nothing.

The connection is deleted only when no other repository still binds to it. One
connection serves many repositories, so recreating one of them leaves a shared
connection in place and the plan is a single delete.

--with-connection is how a connection definition itself is changed — a rotated
GitHub App key, an edited connection.json. It widens the plan to every
repository bound to that connection, then the connection last, because a
connection cannot be deleted underneath a repository that still references it.
All of them are recreated by the same apply Job run.

Destructive, so --confirm is required and --dry-run previews. The repository is
resolved automatically when exactly one exists; otherwise name it with
--repository.`,
		Example: `  platformctl gitsync recreate --repository platform-dashboards --dry-run
  platformctl gitsync recreate --repository platform-dashboards --confirm
  platformctl gitsync recreate --repository platform-dashboards --with-connection --confirm`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGitSyncRecreate(cmd, g, shared, opts)
		},
	}
	cmd.Flags().StringVar(&opts.Repository, "repository", "",
		"repository to recreate; required when more than one exists")
	cmd.Flags().BoolVar(&opts.WithConnection, "with-connection", false,
		"also recreate the connection, taking every repository bound to it along")
	cmd.Flags().BoolVar(&opts.Confirm, "confirm", false, "actually delete the planned resources")
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
				"Run `platformctl gitsync recreate --repository <name> --dry-run` if the fix is a definition change",
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
		return reportCLIError(out, err,
			gitSyncRefusalHelp(opts.Kind, repositoriesBlockingConnection(resources, opts.Name)))
	}

	// Refused rather than repaired, because this command deliberately requests
	// no resync: the folder is recreated whenever platform-grafana next syncs,
	// and only the tenant's own envelope puts the team-only permission list
	// back. `recreate` sequences both, so it is the way through rather than a
	// consolation. A cluster this cannot read is reported, never assumed empty.
	claims, claimErr := readTenantFolderClaims(cmd.Context())
	switch {
	case claimErr != nil:
		if err := display.ToonScalar(out, "warning",
			"tenant folder RBAC could not be checked: "+claimErr.Error()); err != nil {
			return err
		}
	case opts.Kind == gitsync.KindRepository && claims[opts.Name] != "" && !opts.AcceptOpenFolder:
		return reportCLIError(out,
			fmt.Errorf("folder %s carries RBAC for tenant %s, which only %s%s re-applies",
				opts.Name, claims[opts.Name], governanceAppPrefix, claims[opts.Name]),
			fmt.Sprintf("Run `platformctl gitsync recreate --repository %s --confirm`, which re-syncs %s%s too; "+
				"pass --accept-open-folder only if the folder being world-readable until that sync is intended",
				opts.Name, governanceAppPrefix, claims[opts.Name]))
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
	help := []string{
		"The apply Job recreates it on the next sync of the Grafana Application",
		"Run `platformctl gitsync status` afterwards to confirm it came back healthy",
	}
	if tenant := claims[opts.Name]; tenant != "" {
		help = append(help,
			fmt.Sprintf("Folder %s comes back readable by every grafana user until %s%s syncs — "+
				"re-sync it once the repository is healthy again",
				opts.Name, governanceAppPrefix, tenant))
	}
	return display.ToonList(out, "help", help)
}

func runGitSyncRecreate(cmd *cobra.Command, g *Globals, shared *gitSyncGlobals, opts *gitSyncRecreateOptions) error {
	out := cmd.OutOrStdout()

	if err := requireGitSyncIntent(opts.Confirm, g.DryRun); err != nil {
		return reportCLIError(out, err,
			"Run `platformctl gitsync recreate --repository <name> --dry-run` first, then re-run with --confirm")
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

	if opts.WithConnection && repo.ConnectionRef == "" {
		return reportCLIError(out,
			fmt.Errorf("repository %s binds to no connection, so --with-connection has nothing to recreate", repo.Name),
			"Drop --with-connection, or run `platformctl gitsync status --full` to see what each repository binds to")
	}

	// A connection serves every repository bound to it, so tearing it down for
	// one of them strands the rest — and it strands them silently, because the
	// damage shows up on the siblings rather than on the resource that was
	// named. Only the last repository on a connection may take it with it,
	// unless --with-connection takes them all deliberately.
	var stillBound []string
	if repo.ConnectionRef != "" {
		stillBound = siblingRepositories(repos, repo.ConnectionRef, repo.Name)
	}
	deleting := []string{repo.Name}
	if opts.WithConnection {
		deleting = append(deleting, stillBound...)
	}

	// Every repository in the plan is checked, not just the named one:
	// --with-connection deletes siblings the operator did not name, and the
	// finalizer does not care which of them was typed.
	for _, name := range deleting {
		owned, err := client.DashboardsOwnedBy(cmd.Context(), name)
		if err != nil {
			return reportCLIError(out, err,
				"Ownership could not be verified, so nothing was deleted; retry once Grafana answers")
		}
		if len(owned) > 0 && !opts.AllowOwnedDashboard {
			return reportCLIError(out,
				fmt.Errorf("repository %s owns %d dashboard(s): %s", name, len(owned), strings.Join(owned, " ")),
				"Deleting it lets the remove-orphan-resources finalizer collect them; pass --allow-owned-dashboards only if that is intended")
		}
	}

	// Read before the plan is printed, so a dry-run says which tenant envelopes
	// a confirmed run would re-sync rather than leaving that to be discovered
	// afterwards.
	claims, claimErr := readTenantFolderClaims(cmd.Context())
	if claimErr != nil {
		if err := display.ToonScalar(out, "warning",
			"tenant folder RBAC could not be checked: "+claimErr.Error()); err != nil {
			return err
		}
	}

	// Ordering is the whole reason this command exists: the repository
	// references the connection, so deleting the connection first strands it.
	var steps [][]string
	for _, name := range deleting {
		steps = append(steps, []string{strconv.Itoa(len(steps) + 1), gitsync.KindRepository, name})
	}
	if repo.ConnectionRef != "" && (opts.WithConnection || len(stillBound) == 0) {
		steps = append(steps, []string{strconv.Itoa(len(steps) + 1), gitsync.KindConnection, repo.ConnectionRef})
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
	// Said out loud rather than left to be inferred from a one-row order table:
	// an operator who expected two deletes needs to know which resource was held
	// back and why, or the short plan reads as the command having missed a step.
	if len(stillBound) > 0 && !opts.WithConnection {
		if err := display.ToonScalar(out, "retained",
			fmt.Sprintf("connection %s stays: still used by repository %s",
				repo.ConnectionRef, strings.Join(stillBound, " "))); err != nil {
			return err
		}
	}
	// The mirror image: --with-connection deletes repositories nobody named, so
	// the widened plan says whose sync it interrupts before it is confirmed.
	if len(stillBound) > 0 && opts.WithConnection {
		if err := display.ToonScalar(out, "cascade",
			fmt.Sprintf("connection %s is shared, so repository %s goes with it and is recreated too",
				repo.ConnectionRef, strings.Join(stillBound, " "))); err != nil {
			return err
		}
	}
	if g.DryRun {
		if err := display.ToonScalar(out, "result",
			fmt.Sprintf("would delete %d resource(s) in this order — nothing was mutated", len(steps))); err != nil {
			return err
		}
		plan := []string{"Re-run with --confirm to delete them and request a resync"}
		for _, name := range deleting {
			if tenant := claims[name]; tenant != "" {
				plan = append(plan, fmt.Sprintf(
					"Folder %s carries tenant %s's RBAC, so %s%s is re-synced too — without that the folder comes back readable by every grafana user",
					name, tenant, governanceAppPrefix, tenant))
			}
		}
		return display.ToonList(out, "help", append(plan,
			gitSyncConnectionChangeHelp(repo.Name, stillBound, opts.WithConnection)...))
	}

	for _, step := range steps {
		if err := client.Delete(cmd.Context(), step[1], step[2]); err != nil {
			return reportCLIError(out, err, "Run `platformctl gitsync status` to see what is left")
		}
	}

	resync := "skipped (--no-sync)"
	// A deleted repository takes its folder with it, and the folder Grafana
	// recreates carries the inherited Admin/Editor/Viewer grants until the
	// tenant's own PostSync hook replaces them. Refreshing only the Grafana
	// Application recreates the folder and stops there, which is a reopened
	// tenant boundary with nothing reporting it. Both Applications, or neither.
	envelopes := "skipped (--no-sync)"
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
		if claimErr != nil {
			envelopes = "not checked: " + claimErr.Error()
		} else {
			envelopes = refreshTenantEnvelopes(cmd.Context(), deleting, claims)
		}
	}
	if err := display.ToonScalar(out, "result",
		fmt.Sprintf("deleted %d resource(s) in order; ArgoCD refresh %s", len(steps), resync)); err != nil {
		return err
	}
	if err := display.ToonScalar(out, "envelopes", envelopes); err != nil {
		return err
	}
	return display.ToonList(out, "help", append([]string{
		"Run `platformctl gitsync status` after the sync to confirm everything is healthy again",
		"A definition change only takes effect if it is merged before the apply Job re-runs",
	}, gitSyncConnectionChangeHelp(repo.Name, stillBound, opts.WithConnection)...))
}

// gitSyncConnectionChangeHelp surfaces --with-connection exactly where the
// operator learns the connection was left alone. Without it the flag is only
// discoverable from --help, and the plan that retained a connection reads as
// the one place the CLI cannot go — which is how an operator concludes a
// rotated App key has no path through this command at all.
func gitSyncConnectionChangeHelp(repository string, stillBound []string, withConnection bool) []string {
	if withConnection || len(stillBound) == 0 {
		return nil
	}
	return []string{
		fmt.Sprintf("Changing the connection itself (rotated key, edited connection.json) needs "+
			"`platformctl gitsync recreate --repository %s --with-connection --confirm`, which takes repository %s along",
			repository, strings.Join(stillBound, " ")),
	}
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
		if bound := repositoriesBlockingConnection(resources, name); len(bound) > 0 {
			return fmt.Errorf("connection %s is still referenced by repository %s", name, strings.Join(bound, " "))
		}
	}
	return nil
}

// repositoriesBlockingConnection names every repository that references a
// connection, from a mixed-kind resource list.
func repositoriesBlockingConnection(resources []gitsync.Resource, connection string) []string {
	var repos []gitsync.Resource
	for _, r := range resources {
		if r.Kind == gitsync.KindRepository {
			repos = append(repos, r)
		}
	}
	return sortedNames(gitsync.RepositoriesUsingConnection(repos, connection))
}

// siblingRepositories names the other repositories bound to a connection. It is
// what separates "this connection has no user left" from "this connection is
// shared", which the single-repository era never had to distinguish.
func siblingRepositories(repos []gitsync.Resource, connection, excluding string) []string {
	var out []string
	for _, name := range gitsync.RepositoriesUsingConnection(repos, connection) {
		if name != excluding {
			out = append(out, name)
		}
	}
	return sortedNames(out)
}

// sortedNames keeps the delete plan, the refusal and the command it suggests
// listing the same repositories in the same order — Grafana's list order is
// not promised to be stable, and an order that shifts between runs reads as
// the set having changed.
func sortedNames(names []string) []string {
	sort.Strings(names)
	return names
}

// gitSyncRefusalHelp names the repositories in the way out, because for a
// shared connection there is no generic one: `recreate` on its own now retains
// a connection with siblings, so pointing at it without --with-connection sends
// an operator round a loop that never deletes the thing they asked to delete.
func gitSyncRefusalHelp(kind string, blocking []string) string {
	if kind != gitsync.KindConnection {
		return "Pass --allow-owned-dashboards only if losing those dashboards is intended"
	}
	if len(blocking) == 0 {
		return "Delete the repositories that reference it first"
	}
	return fmt.Sprintf("Delete repository %s first — `platformctl gitsync recreate --repository %s "+
		"--with-connection --confirm` does all of it in order",
		strings.Join(blocking, " "), blocking[0])
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

// tenantFolderClaims maps a git sync repository name to the tenant whose team
// is granted on the folder that repository creates. Deleting such a repository
// takes the folder with it (the remove-orphan-resources finalizer owns it), and
// the folder Grafana recreates comes back with the inherited Admin/Editor/Viewer
// grants — readable by every user in the instance — until the tenant's PostSync
// hook runs again. Nothing else in the cluster can answer this: the hook Job
// carrying the UID is garbage collected minutes after it runs, so the claim is
// read from the ConfigMap tenant-envelope leaves behind.
func tenantFolderClaims(ctx context.Context, kc kubernetes.Interface) (map[string]string, error) {
	list, err := kc.CoreV1().ConfigMaps(grafanaNamespace).List(ctx, metav1.ListOptions{LabelSelector: tenantLabel})
	if err != nil {
		return nil, err
	}
	claims := make(map[string]string, len(list.Items))
	for _, cm := range list.Items {
		if folder := cm.Data[tenantGitSyncFolder]; folder != "" {
			claims[folder] = cm.Labels[tenantLabel]
		}
	}
	return claims, nil
}

// readTenantFolderClaims answers with the claims and, separately, with why it
// could not. A cluster it cannot read is reported rather than treated as "no
// tenant claims anything" — that mistake is how a missing grant looks exactly
// like an absent one.
func readTenantFolderClaims(ctx context.Context) (map[string]string, error) {
	kc, err := volumeKubeClient()
	if err != nil {
		return nil, err
	}
	return tenantFolderClaims(ctx, kc)
}

// refreshTenantEnvelopes asks ArgoCD to re-run the hook that re-grants each
// deleted repository's folder to its tenant team. Returned as a message rather
// than an error: the deletes already happened, so an unrefreshed envelope is a
// follow-up the operator must see, not a reason to report the command failed.
func refreshTenantEnvelopes(ctx context.Context, deleted []string, claims map[string]string) string {
	var apps, missed []string
	for _, name := range deleted {
		tenant, ok := claims[name]
		if !ok {
			continue
		}
		app := governanceAppPrefix + tenant
		dc, err := volumeDynamicClient()
		if err == nil {
			err = heal.RefreshApp(ctx, dc, app)
		}
		if err != nil {
			missed = append(missed, app)
			continue
		}
		apps = append(apps, app)
	}
	switch {
	case len(missed) > 0:
		return fmt.Sprintf("not requested for %s — re-sync it or its folder stays readable by every grafana user",
			strings.Join(sortedNames(missed), " "))
	case len(apps) > 0:
		return "requested for " + strings.Join(sortedNames(apps), " ")
	default:
		return "none needed: no deleted repository carries tenant folder RBAC"
	}
}
