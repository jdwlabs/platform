package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/jdwlabs/platform/internal/bootstrap"
	"github.com/jdwlabs/platform/internal/bootstrap/heal"
	"github.com/jdwlabs/platform/internal/helm"
	"github.com/jdwlabs/platform/internal/k8s"
	"github.com/jdwlabs/platform/internal/tenants"
	"github.com/jdwlabs/platform/internal/vault"
)

func newBootstrapCmd(g *Globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Cluster bootstrap operations",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCascade(cmd.Context(), g, cmd.OutOrStdout(), 0)
		},
	}
	cmd.AddCommand(newBootstrapVerifyCmd(g))
	cmd.AddCommand(newBootstrapHealCmd(g))
	cmd.AddCommand(newBootstrapPhaseCmd(g))
	return cmd
}

func newBootstrapPhaseCmd(g *Globals) *cobra.Command {
	return &cobra.Command{
		Use:   "phase <number>",
		Short: "Run a single bootstrap phase by number (1-5)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			num, err := strconv.Atoi(args[0])
			if err != nil || num < 1 || num > 5 {
				return fmt.Errorf("phase must be 1..5")
			}
			return runCascade(cmd.Context(), g, cmd.OutOrStdout(), num)
		},
	}
}

// runCascade builds the phase list and runs the cascade. If phaseNum > 0, only
// that phase is run; if 0, all phases run in order.
func runCascade(ctx context.Context, g *Globals, w io.Writer, phaseNum int) error {
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
	vc, err := vault.NewClient(vaultAddr, os.Getenv("PLATFORMCTL_VAULT_TOKEN"))
	if err != nil {
		return fmt.Errorf("vault client: %w", err)
	}

	tenantNames, err := collectTenantNames("tenants")
	if err != nil {
		return fmt.Errorf("collect tenants: %w", err)
	}

	valuesPath := "tenants/platform/services/argo-cd/values.yaml"
	allPhases := []bootstrap.Phase{
		bootstrap.NewArgocdInstallPhase(kc, helm.ExecRunner{}, valuesPath),
		bootstrap.NewRootApplyPhase(kc, dc, g.Branch, "bootstrap/root-app.yaml"),
		bootstrap.NewVaultInitPhase(kc, vault.NewBuilder(vaultAddr), g.NonInteractive),
		bootstrap.NewVaultSeedPhase(vc, g.NonInteractive, "secret", tenantNames, nil),
		bootstrap.NewBackupsInitPhase(vc, g.NonInteractive, "secret"),
	}

	phases := allPhases
	if phaseNum > 0 {
		phases = []bootstrap.Phase{allPhases[phaseNum-1]}
	}

	em := NewEmitter(w, g.JSON)
	opts := bootstrap.CascadeOptions{
		OnEvent: func(phase, name, status, message string) {
			em.Emit(Event{Phase: phase, Name: name, Status: status, Message: message})
		},
	}
	return bootstrap.RunCascade(ctx, phases, opts)
}

func collectTenantNames(root string) ([]string, error) {
	matches, _ := filepath.Glob(filepath.Join(root, "*", "tenant.yaml"))
	var names []string
	for _, m := range matches {
		t, err := tenants.LoadFile(m)
		if err != nil {
			return nil, err
		}
		if t.Name == "platform" {
			continue
		}
		names = append(names, t.Name)
	}
	return names, nil
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
		stuckFinalizer       bool
		stuckKind            string
		stuckNamespace       string
		stuckName            string
		defaultProject       bool
		projectPath          string
		certApprover         bool
		tlsReissue           bool
		tlsNamespace         string
		orphanNamespaces     bool
		tenantsDir           string
		longhornFreshInstall bool
		all                  bool
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
			runLonghornFreshInstall := longhornFreshInstall || all

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

			if runLonghornFreshInstall {
				if err := heal.LonghornFreshInstall(ctx, kc, dc); err != nil {
					em.Emit(Event{Phase: "heal", Name: "longhorn-fresh-install", Status: "fail", Message: err.Error()})
					return err
				}
				em.Emit(Event{Phase: "heal", Name: "longhorn-fresh-install", Status: "ok",
					Message: "created longhorn-service-account + pre-upgrade CRB; triggered ArgoCD refresh"})
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
	cmd.Flags().BoolVar(&longhornFreshInstall, "longhorn-fresh-install", false, "create longhorn SA + RBAC so the pre-upgrade hook can run on a fresh cluster")
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
