package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/jdwlabs/platform/internal/cluster"
	"github.com/jdwlabs/platform/internal/k8s"
)

func newClusterCmd(g *Globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cluster",
		Short: "Cluster health and status commands",
	}
	cmd.AddCommand(newClusterStatusCmd(g))
	return cmd
}

func newClusterStatusCmd(g *Globals) *cobra.Command {
	var (
		watchMode bool
		interval  time.Duration
	)
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Check cluster health across all layers (operators, vault, secrets, TLS, ArgoCD apps)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

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

			checks := cluster.AllChecks(kc, dc)

			if watchMode {
				for {
					// Clear terminal and print timestamp header.
					fmt.Fprint(cmd.OutOrStdout(), "\033[H\033[2J")
					fmt.Fprintf(cmd.OutOrStdout(), "platformctl cluster status  (%s)  — Ctrl+C to stop\n\n",
						time.Now().Format("15:04:05"))

					layers := cluster.RunChecks(ctx, checks)
					if g.JSON {
						if err := cluster.PrintJSON(cmd.OutOrStdout(), layers); err != nil {
							return err
						}
					} else {
						cluster.PrintResults(cmd.OutOrStdout(), layers, g.NoColor)
					}

					select {
					case <-ctx.Done():
						return nil
					case <-time.After(interval):
					}
				}
			}

			layers := cluster.RunChecks(ctx, checks)
			if g.JSON {
				return cluster.PrintJSON(cmd.OutOrStdout(), layers)
			}
			cluster.PrintResults(cmd.OutOrStdout(), layers, g.NoColor)

			if cluster.OverallStatus(layers) == cluster.StatusFail {
				return fmt.Errorf("cluster unhealthy")
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&watchMode, "watch", "w", false, "continuously re-check at --interval")
	cmd.Flags().DurationVar(&interval, "interval", 30*time.Second, "poll interval in watch mode")
	return cmd
}
