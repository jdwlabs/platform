package cli

import (
	"github.com/jdwlabs/platform/internal/tenants"
	"github.com/spf13/cobra"
)

func newTenantsCmd(g *Globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tenants",
		Short: "Tenant manifest operations",
	}
	cmd.AddCommand(newTenantsValidateCmd(g))
	return cmd
}

func newTenantsValidateCmd(g *Globals) *cobra.Command {
	return &cobra.Command{
		Use:   "validate [path]",
		Short: "Validate tenant.yaml files (replaces tools/validate-tenants.py)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "tenants"
			if len(args) == 1 {
				path = args[0]
			}
			out := NewEmitter(cmd.OutOrStdout(), g.JSON)
			if err := tenants.ValidateDir(path); err != nil {
				out.Emit(Event{Phase: "tenants", Name: "validate", Status: "failed", Message: err.Error()})
				return err
			}
			out.Emit(Event{Phase: "tenants", Name: "validate", Status: "ok", Message: "all tenant.yaml files valid"})
			return nil
		},
	}
}
