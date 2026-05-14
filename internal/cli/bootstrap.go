package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/jdwlabs/platform/internal/bootstrap"
	"github.com/jdwlabs/platform/internal/bootstrap/heal"
	"github.com/jdwlabs/platform/internal/k8s"
	"github.com/jdwlabs/platform/internal/tenants"
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
	var (
		stuckFinalizer  bool
		stuckKind       string
		stuckNamespace  string
		stuckName       string
		defaultProject  bool
		projectPath     string
		certApprover    bool
		tlsReissue      bool
		tlsNamespace    string
		orphanNamespaces bool
		tenantsDir      string
		all             bool
	)

	cmd := &cobra.Command{
		Use:   "heal",
		Short: "Recovery primitives",
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

			runDefaultProject := defaultProject || all
			runCertApprover := certApprover || all
			runTLSReissue := tlsReissue || all
			runOrphanNamespaces := orphanNamespaces || all

			if stuckFinalizer {
				opts := heal.StuckOptions{Namespace: stuckNamespace, Kind: stuckKind, Name: stuckName}
				if err := heal.StripStuck(ctx, dc, opts); err != nil {
					em.Emit(Event{Phase: "heal", Name: "stuck-finalizer", Status: "fail", Message: err.Error()})
					return err
				}
				em.Emit(Event{Phase: "heal", Name: "stuck-finalizer", Status: "ok",
					Message: fmt.Sprintf("stripped finalizers from %s/%s (%s)", stuckNamespace, stuckName, stuckKind)})
			}

			if runDefaultProject {
				if err := heal.ApplyDefaultProject(ctx, dc, projectPath); err != nil {
					em.Emit(Event{Phase: "heal", Name: "default-project", Status: "fail", Message: err.Error()})
					return err
				}
				em.Emit(Event{Phase: "heal", Name: "default-project", Status: "ok",
					Message: fmt.Sprintf("applied appproject/default from %s", projectPath)})
			}

			if runCertApprover {
				if err := heal.RefreshCertApprover(ctx, dc); err != nil {
					em.Emit(Event{Phase: "heal", Name: "cert-approver", Status: "fail", Message: err.Error()})
					return err
				}
				em.Emit(Event{Phase: "heal", Name: "cert-approver", Status: "ok",
					Message: "triggered hard refresh on application/kubelet-serving-cert-approver"})
			}

			if runTLSReissue {
				deleted, err := heal.ReissueTLS(ctx, kc, tlsNamespace)
				if err != nil {
					em.Emit(Event{Phase: "heal", Name: "tls-reissue", Status: "fail", Message: err.Error()})
					return err
				}
				em.Emit(Event{Phase: "heal", Name: "tls-reissue", Status: "ok",
					Message: fmt.Sprintf("deleted %d cert-manager secrets in %s", len(deleted), tlsNamespace)})
			}

			if runOrphanNamespaces {
				allowed, err := buildAllowedNamespaces(tenantsDir)
				if err != nil {
					em.Emit(Event{Phase: "heal", Name: "orphan-namespaces", Status: "fail", Message: err.Error()})
					return err
				}
				deleted, err := heal.DeleteOrphanNamespaces(ctx, kc, allowed)
				if err != nil {
					em.Emit(Event{Phase: "heal", Name: "orphan-namespaces", Status: "fail", Message: err.Error()})
					return err
				}
				em.Emit(Event{Phase: "heal", Name: "orphan-namespaces", Status: "ok",
					Message: fmt.Sprintf("deleted %d orphan namespaces", len(deleted))})
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&stuckFinalizer, "stuck-finalizer", false, "strip finalizers from a stuck ArgoCD resource")
	cmd.Flags().StringVar(&stuckKind, "kind", "applicationset", "resource kind (applicationset|application|appproject)")
	cmd.Flags().StringVar(&stuckNamespace, "namespace", "argocd", "namespace of the stuck resource")
	cmd.Flags().StringVar(&stuckName, "name", "", "name of the stuck resource")
	cmd.Flags().BoolVar(&defaultProject, "default-project", false, "create-or-update the ArgoCD default AppProject")
	cmd.Flags().StringVar(&projectPath, "project-path", "bootstrap/argocd/projects/default.yaml", "path to the AppProject manifest")
	cmd.Flags().BoolVar(&certApprover, "cert-approver", false, "trigger hard refresh on application/kubelet-serving-cert-approver")
	cmd.Flags().BoolVar(&tlsReissue, "tls-reissue", false, "delete cert-manager TLS secrets to trigger reissuance")
	cmd.Flags().StringVar(&tlsNamespace, "tls-namespace", "cert-manager", "namespace to scan for cert-manager TLS secrets")
	cmd.Flags().BoolVar(&orphanNamespaces, "orphan-namespaces", false, "delete tenant-labeled namespaces not found in any tenant.yaml")
	cmd.Flags().StringVar(&tenantsDir, "tenants-dir", "tenants", "directory containing per-tenant subdirectories")
	cmd.Flags().BoolVar(&all, "all", false, "run all heal operations (except --stuck-finalizer which requires a target)")
	return cmd
}

// buildAllowedNamespaces reads all tenant.yaml files under dir and returns
// every declared namespace name as an allowed set.
func buildAllowedNamespaces(dir string) (map[string]bool, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*", "tenant.yaml"))
	if err != nil {
		return nil, fmt.Errorf("glob tenants: %w", err)
	}
	allowed := make(map[string]bool)
	for _, m := range matches {
		t, err := tenants.LoadFile(m)
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", m, err)
		}
		for _, ns := range t.Namespaces {
			allowed[ns.Name] = true
		}
	}
	return allowed, nil
}
