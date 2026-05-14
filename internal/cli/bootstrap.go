package cli

import (
	"github.com/spf13/cobra"
)

func newBootstrapCmd(g *Globals) *cobra.Command {
	cmd := &cobra.Command{Use: "bootstrap", Short: "Cluster bootstrap operations"}
	cmd.AddCommand(newBootstrapVerifyCmd(g))
	cmd.AddCommand(newBootstrapHealCmd(g))
	return cmd
}

func newBootstrapVerifyCmd(g *Globals) *cobra.Command {
	return &cobra.Command{Use: "verify", Short: "Run verification gates against the cluster"}
}

func newBootstrapHealCmd(g *Globals) *cobra.Command {
	return &cobra.Command{Use: "heal", Short: "Recovery primitives"}
}
