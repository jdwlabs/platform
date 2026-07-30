package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/jdwlabs/platform/internal/bootstrap"
	"github.com/jdwlabs/platform/internal/k8s"
	"github.com/jdwlabs/platform/internal/tenants"
)

func newTenantsCmd(g *Globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tenants",
		Short: "Tenant manifest operations",
	}
	cmd.AddCommand(newTenantsValidateCmd(g))
	cmd.AddCommand(newTenantsVerifySecretsCmd(g))
	return cmd
}

func newTenantsValidateCmd(g *Globals) *cobra.Command {
	return &cobra.Command{
		Use:   "validate [path]",
		Short: "Validate tenant.yaml files",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "tenants"
			if len(args) == 1 {
				path = args[0]
			}
			out := NewEmitter(cmd.OutOrStdout(), g.JSON)
			if g.Session != nil {
				out.SetSession(g.Session)
			}
			if err := tenants.ValidateDir(path); err != nil {
				out.Emit(Event{Phase: "tenants", Name: "validate", Status: "failed", Message: err.Error()})
				return err
			}
			esIssues, err := tenants.LintExternalSecrets(path)
			if err != nil {
				out.Emit(Event{Phase: "tenants", Name: "validate", Status: "failed", Message: err.Error()})
				return err
			}
			for _, iss := range esIssues {
				out.Emit(Event{Phase: "tenants", Name: "externalsecret-lint", Status: "broken", Message: iss.Error()})
			}
			if len(esIssues) > 0 {
				err := fmt.Errorf("%d ExternalSecret data entr(ies) omit required remoteRef fields", len(esIssues))
				out.Emit(Event{Phase: "tenants", Name: "validate", Status: "failed", Message: err.Error()})
				return err
			}
			out.Emit(Event{Phase: "tenants", Name: "validate", Status: "ok", Message: "all tenant.yaml files valid"})
			return nil
		},
	}
}

// newTenantsVerifySecretsCmd builds `platformctl tenants verify-secrets`.
// Resolves every ExternalSecret (key, property) reference against live Vault
// and surfaces missing Vault paths and missing fields — the common failure mode
// where tenant deploymentRepo manifests outpace Vault seed data.
//
// The default source is the tenants tree, not the cluster: applied state cannot
// contain a reference added on an unmerged branch, so a cluster scan silently
// passes the very change a pre-merge gate exists to catch.
func newTenantsVerifySecretsCmd(g *Globals) *cobra.Command {
	var (
		store   string
		kvMount string
		source  string
		path    string
	)
	cmd := &cobra.Command{
		Use:   "verify-secrets",
		Short: "Verify tenant ExternalSecret references resolve against live Vault",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := NewEmitter(cmd.OutOrStdout(), g.JSON)
			if g.Session != nil {
				out.SetSession(g.Session)
			}
			if source != sourceRepo && source != sourceCluster {
				return fmt.Errorf("--source must be %q or %q, got %q", sourceRepo, sourceCluster, source)
			}

			vc := testVaultClient
			if vc == nil {
				kc := testKubeClient
				if kc == nil {
					var err error
					kc, err = k8s.NewClient()
					if err != nil {
						return fmt.Errorf("kube client: %w", err)
					}
				}
				vaultAddr := os.Getenv("PLATFORMCTL_VAULT_ADDR")
				if vaultAddr == "" {
					vaultAddr = "http://vault.vault.svc:8200"
				}
				restCfg, err := k8s.NewRestConfig()
				if err != nil {
					return fmt.Errorf("rest config: %w", err)
				}
				resolver := bootstrap.NewVaultAddrResolver(vaultAddr, restCfg, kc)
				defer resolver.Stop()

				vc, err = resolver.NewClient(ctx, "")
				if err != nil {
					return fmt.Errorf("vault client: %w", err)
				}
			}

			var report tenants.VerifyReport
			var err error
			if source == sourceRepo {
				out.Emit(Event{Phase: "verify-secrets", Name: "scan", Status: "progressing",
					Message: fmt.Sprintf("scanning %s for ExternalSecrets backed by ClusterSecretStore/%s", path, store)})
				report, err = tenants.VerifyRepoSecrets(ctx, vc, path, kvMount, store)
			} else {
				dc := testDynamicClient
				if dc == nil {
					dc, err = k8s.NewDynamic()
					if err != nil {
						return fmt.Errorf("dynamic client: %w", err)
					}
				}
				out.Emit(Event{Phase: "verify-secrets", Name: "scan", Status: "progressing",
					Message: fmt.Sprintf("scanning applied ExternalSecrets backed by ClusterSecretStore/%s", store)})
				report, err = tenants.VerifySecrets(ctx, dc, vc, kvMount, store)
			}
			if err != nil {
				out.Emit(Event{Phase: "verify-secrets", Name: "scan", Status: "failed", Message: err.Error()})
				return err
			}

			for _, line := range report.SkipLines() {
				out.Emit(Event{Phase: "verify-secrets", Name: "not-checked", Status: "info", Message: line})
			}
			for _, iss := range report.Issues {
				out.Emit(Event{
					Phase:   "verify-secrets",
					Name:    issueName(iss),
					Status:  "broken",
					Message: fmt.Sprintf("%s: kv/%s property=%q — %s", iss.Kind, iss.VaultKey, iss.Property, iss.Detail),
				})
			}

			if report.HasIssues() {
				out.Emit(Event{Phase: "verify-secrets", Name: "summary", Status: "failed", Message: report.Summary()})
				return fmt.Errorf("%d secret reference(s) failed verification", len(report.Issues))
			}
			out.Emit(Event{Phase: "verify-secrets", Name: "summary", Status: "ok", Message: report.Summary()})
			return nil
		},
	}
	cmd.Flags().StringVar(&store, "store", "vault", "ClusterSecretStore name to filter by")
	cmd.Flags().StringVar(&kvMount, "kv-mount", "kv", "Vault KV-v2 mount path")
	cmd.Flags().StringVar(&source, "source", sourceRepo,
		`where to read ExternalSecrets from: "repo" (the tenants tree, gates unmerged refs) or "cluster" (applied state only)`)
	cmd.Flags().StringVar(&path, "path", "tenants", "tenants tree to scan when --source=repo")
	return cmd
}

const (
	sourceRepo    = "repo"
	sourceCluster = "cluster"
)

// issueName identifies the offending ExternalSecret by manifest path when the
// ref came from the tree, and by namespace when it came from the cluster.
func issueName(iss tenants.SecretIssue) string {
	if iss.File != "" {
		return iss.File
	}
	return iss.Namespace + "/" + iss.ESName
}
