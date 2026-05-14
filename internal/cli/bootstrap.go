package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/jdwlabs/platform/internal/bootstrap"
	"github.com/jdwlabs/platform/internal/k8s"
)

func newBootstrapCmd(g *Globals) *cobra.Command {
	cmd := &cobra.Command{Use: "bootstrap", Short: "Cluster bootstrap operations"}
	cmd.AddCommand(newBootstrapVerifyCmd(g))
	cmd.AddCommand(newBootstrapHealCmd(g))
	return cmd
}

type verifyGate struct {
	phase int
	name  string
	run   func(ctx context.Context) error
}

func newBootstrapVerifyCmd(g *Globals) *cobra.Command {
	return &cobra.Command{
		Use:   "verify",
		Short: "Run verification gates against the cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			em := NewEmitter(os.Stdout, g.JSON)

			kc := testKubeClient
			dc := testDynamicClient
			if kc == nil {
				var err error
				kc, err = k8s.NewClient()
				if err != nil {
					return fmt.Errorf("build kube client: %w", err)
				}
			}
			if dc == nil {
				var err error
				dc, err = k8s.NewDynamic()
				if err != nil {
					return fmt.Errorf("build dynamic client: %w", err)
				}
			}

			gates := []verifyGate{
				{1, "argocd-ready", func(ctx context.Context) error {
					return bootstrap.VerifyArgocdReady(ctx, kc)
				}},
				{2, "root-applied", func(ctx context.Context) error {
					return bootstrap.VerifyRootApplied(ctx, kc, dc)
				}},
				{3, "vault-initialized", func(ctx context.Context) error {
					return bootstrap.VerifyVaultInitialized(ctx, kc, dc)
				}},
				{4, "external-secrets-synced", func(ctx context.Context) error {
					return bootstrap.VerifyExternalSecretsSynced(ctx, kc, dc)
				}},
				{5, "backups-configured", func(ctx context.Context) error {
					return bootstrap.VerifyBackupsConfigured(ctx, kc)
				}},
				{6, "all-healthy", func(ctx context.Context) error {
					return bootstrap.VerifyAllHealthy(ctx, kc, dc)
				}},
			}

			var firstErr error
			for _, gate := range gates {
				phase := fmt.Sprintf("phase-%d", gate.phase)
				if err := gate.run(ctx); err != nil {
					em.Emit(Event{Phase: phase, Name: gate.name, Status: "fail", Message: err.Error()})
					if firstErr == nil {
						firstErr = err
					}
				} else {
					em.Emit(Event{Phase: phase, Name: gate.name, Status: "ok", Message: "verified"})
				}
			}
			return firstErr
		},
	}
}

func newBootstrapHealCmd(g *Globals) *cobra.Command {
	return &cobra.Command{Use: "heal", Short: "Recovery primitives"}
}
