# GEMINI.md

This file provides guidance to Gemini CLI when working in this repository.
For the canonical reference, see [CLAUDE.md](CLAUDE.md) — this file mirrors that content.

## Repository Overview

GitOps platform configuration for the jdwlabs Kubernetes cluster. Managed via `platformctl` — the custom CLI. ArgoCD watches this repo; merging to `main` triggers automatic sync.

### Key Directories

- `bootstrap/` — ArgoCD ApplicationSets and AppProjects
- `platform/` — Shared platform applications (Vault, cert-manager, nginx, etc.)
- `tenants/` — Per-tenant configuration (tenant.yaml)
- `helm-charts/` — Custom Helm charts
- `cli/` — Go source for `platformctl`

## Development Commands

```bash
platformctl tenants validate          # Validate tenant.yaml files
platformctl tenants verify-secrets    # Verify ExternalSecret refs in Vault
yamllint tenants/ bootstrap/          # YAML lint
kubeconform                           # Validate Kubernetes manifests
cd cli && go build ./... && go test ./...  # Build/test platformctl CLI
```

## Agent Contract

- ALL cluster operations go through `platformctl` — never raw kubectl/vault/helm
- Use `--json` flag when parsing output programmatically
- Exit codes: 0=done, 1=hard fail, 2=progressing/timeout, 3=broken, 4=user abort
- Design specs in `docs/superpowers/specs/`, plans in `docs/superpowers/plans/`
