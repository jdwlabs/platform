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
			out.Emit(Event{Phase: "tenants", Name: "validate", Status: "ok", Message: "all tenant.yaml files valid"})
			return nil
		},
	}
}

// newTenantsVerifySecretsCmd builds `platformctl tenants verify-secrets`.
// Lists every ExternalSecret in the cluster that references the named
// ClusterSecretStore (default "vault") and checks each (key, property) ref
// against live Vault state. Surfaces missing Vault paths and missing fields
// — the common failure mode where tenant deploymentRepo manifests outpace
// Vault seed data.
func newTenantsVerifySecretsCmd(g *Globals) *cobra.Command {
	var (
		store   string
		kvMount string
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

			kc := testKubeClient
			dc := testDynamicClient
			if kc == nil {
				var err error
				kc, err = k8s.NewClient()
				if err != nil {
					return fmt.Errorf("kube client: %w", err)
				}
			}
			if dc == nil {
				var err error
				dc, err = k8s.NewDynamic()
				if err != nil {
					return fmt.Errorf("dynamic client: %w", err)
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

			vc, err := resolver.NewClient(ctx, "")
			if err != nil {
				return fmt.Errorf("vault client: %w", err)
			}

			out.Emit(Event{Phase: "verify-secrets", Name: "scan", Status: "progressing",
				Message: fmt.Sprintf("scanning ExternalSecrets backed by ClusterSecretStore/%s", store)})

			report, err := tenants.VerifySecrets(ctx, dc, vc, kvMount, store)
			if err != nil {
				out.Emit(Event{Phase: "verify-secrets", Name: "scan", Status: "failed", Message: err.Error()})
				return err
			}

			for _, iss := range report.Issues {
				out.Emit(Event{
					Phase:   "verify-secrets",
					Name:    iss.Namespace + "/" + iss.ESName,
					Status:  "broken",
					Message: fmt.Sprintf("%s: kv/%s property=%q — %s", iss.Kind, iss.VaultKey, iss.Property, iss.Detail),
				})
			}

			if report.HasIssues() {
				out.Emit(Event{Phase: "verify-secrets", Name: "summary", Status: "failed",
					Message: fmt.Sprintf("%d refs checked; %d issues", report.Checked, len(report.Issues))})
				return fmt.Errorf("%d secret reference(s) failed verification", len(report.Issues))
			}
			out.Emit(Event{Phase: "verify-secrets", Name: "summary", Status: "ok",
				Message: fmt.Sprintf("%d refs checked; all resolve", report.Checked)})
			return nil
		},
	}
	cmd.Flags().StringVar(&store, "store", "vault", "ClusterSecretStore name to filter by")
	cmd.Flags().StringVar(&kvMount, "kv-mount", "kv", "Vault KV-v2 mount path")
	return cmd
}
