# Headlamp Mobile OIDC via ArgoCD Dex

**Status:** Approved  
**Date:** 2026-05-27  
**Context:** Improve mobile login UX for Headlamp (dashboard.jdwlabs.com)

## Problem

Logging into `dashboard.jdwlabs.com` from mobile requires pasting a long-lived service account token — a 5+ step flow that is fragile on mobile. Target: ≤3 taps from a phone not on home network.

## Decision

Reuse ArgoCD's bundled Dex (`argocd-dex-server`) as a self-contained OIDC provider. No external IdP (no Google/GitHub dependency). No new service to deploy. Dex gets a static local user with a bcrypt-hashed password stored in Vault; Headlamp is registered as an OIDC client.

**Rejected options:**
- Google OAuth — external IdP dependency
- Standalone Dex — adds a new service; overkill for homelab
- Improved token flow — still 5+ steps on mobile, doesn't meet ≤3 tap goal
- Tailscale — out of scope (not deployed)

## Architecture

```
[Mobile browser]
     │ 1. GET dashboard.jdwlabs.com
     ▼
[Headlamp pod] ──redirect──► [argocd.jdwlabs.com/api/dex/auth]
                                         │
                               [Dex login form — 1Password autofills]
                                         │ 2. POST credentials
                                         ▼
                               [Dex verifies bcrypt hash from dex-secrets K8s secret]
                                         │ 3. redirect with auth code
                                         ▼
[dashboard.jdwlabs.com/oidc-callback]
     │ 4. token exchange: Headlamp → argocd-dex-server.argocd.svc (in-cluster)
     ▼
[Headlamp dashboard — all K8s API calls proxied via cluster-admin SA]
```

**Auth model:** All Dex-authenticated users receive full cluster-admin access via Headlamp's SA. Acceptable for single-user homelab. OIDC controls access to Headlamp; Kubernetes RBAC is not user-scoped.

**Side effect:** Adding `dex.config` to `argocd-cm` activates Dex-based SSO for ArgoCD's own login page ("Log in via Dex" button). Local admin account continues to work. Dex user can also log into ArgoCD.

## Components

### Vault Secret — `kv/argocd-dex`

| Field | Description |
|-------|-------------|
| `admin-password-hash` | bcrypt hash of the Dex admin password |
| `headlamp-client-secret` | shared secret between Headlamp and Dex |

### CLI — `cli/internal/bootstrap/phase4_vault_seed.go`

Add `argocd-dex` entry to `staticSeedSpecs`:

```go
"argocd-dex": {Path: "argocd-dex", Fields: []seedField{
    {"admin-password-hash",      "PLATFORMCTL_ARGOCD_DEX_ADMIN_PASSWORD_HASH",      true, false},
    {"headlamp-client-secret",   "PLATFORMCTL_ARGOCD_DEX_HEADLAMP_CLIENT_SECRET",   true, false},
}},
```

### New Files

**`tenants/platform/services/argo-cd/postInstall/dex-externalsecret.yaml`**

ExternalSecret in `argocd` namespace. Syncs both Dex secrets from Vault into K8s secret `dex-secrets`. ArgoCD's Dex substitutes `$dex-secrets:<key>` references in `dex.config` automatically.

**`tenants/platform/services/headlamp/postInstall/oidc-externalsecret.yaml`**

ExternalSecret in `headlamp` namespace. Syncs `headlamp-client-secret` from `kv/argocd-dex` into K8s secret `headlamp-oidc-secret` (key: `client-secret`).

### Modified Files

**`tenants/platform/services/argo-cd/values.yaml`**

Add to `configs.cm`:

```yaml
dex.config: |
  connectors: []
  enablePasswordDB: true
  oauth2:
    skipApprovalScreen: true
  staticPasswords:
    - email: admin@jdwlabs.com
      hash: $dex-secrets:admin-password-hash
      username: admin
      userID: "08a8684b-db88-4b73-90a9-3cd1661f5466"
  staticClients:
    - id: headlamp
      redirectURIs:
        - https://dashboard.jdwlabs.com/oidc-callback
      name: Headlamp
      secret: $dex-secrets:headlamp-client-secret
```

Note: The `argo-cd` OIDC client (ArgoCD → Dex) is auto-managed by ArgoCD; it does not appear in `staticClients`.

**`tenants/platform/services/headlamp/values.yaml`**

Add OIDC configuration. Client secret injected from `headlamp-oidc-secret` K8s secret via env var (`HEADLAMP_CONFIG_OIDC_CLIENT_SECRET` or equivalent per chart version 0.41.0).

```yaml
config:
  oidc:
    clientID: headlamp
    issuerURL: https://argocd.jdwlabs.com/api/dex
    scopes: "profile email"
```

Verify exact env var name and secret injection mechanism against Headlamp chart 0.41.0 values schema during implementation.

**`docs/OPERATIONS.md`**

Add mobile login section:
- Mobile flow: open dashboard → redirect to ArgoCD Dex login → credentials (1Password autofill) → dashboard. 2-3 taps.
- Rotating the Dex password: generate new bcrypt hash, update Vault, reseed with `platformctl bootstrap seed argocd-dex`.

## Secrets Flow

```
platformctl bootstrap seed argocd-dex
  (prompts for admin-password-hash and headlamp-client-secret)
  → writes kv/argocd-dex in Vault

ESO: argocd/dex-secrets  ←  kv/argocd-dex
  dex-secrets.admin-password-hash
  dex-secrets.headlamp-client-secret

ESO: headlamp/headlamp-oidc-secret  ←  kv/argocd-dex.headlamp-client-secret
  headlamp-oidc-secret.client-secret

ArgoCD dex.config at runtime:
  $dex-secrets:admin-password-hash      → resolved by ArgoCD controller
  $dex-secrets:headlamp-client-secret   → resolved by ArgoCD controller

Headlamp pod:
  env HEADLAMP_CONFIG_OIDC_CLIENT_SECRET from headlamp-oidc-secret
```

## Bootstrap Sequence (operator)

1. Generate bcrypt hash: `htpasswd -bnBC 10 "" <password> | tr -d ':\n'`
2. Generate client secret: `openssl rand -base64 32`
3. Seed Vault: `platformctl bootstrap seed argocd-dex`
4. ArgoCD syncs → Dex restarts with new config
5. Headlamp syncs → OIDC env var injected
6. Test: open `dashboard.jdwlabs.com` on mobile → verify redirect to Dex login

## Acceptance Criteria

- [ ] Current login friction points documented in OPERATIONS.md
- [ ] Dex OIDC configured and functional (no external IdP dependency)
- [ ] Mobile login works in ≤3 taps from phone off home network
- [ ] Password and client secret stored in Vault, not in Git
- [ ] `platformctl bootstrap seed argocd-dex` prompts for both secrets
- [ ] OPERATIONS.md updated with new mobile flow and password rotation procedure

## Files Changed

| File | Change |
|------|--------|
| `cli/internal/bootstrap/phase4_vault_seed.go` | Add `argocd-dex` seed spec |
| `tenants/platform/services/argo-cd/values.yaml` | Add `dex.config` |
| `tenants/platform/services/argo-cd/postInstall/dex-externalsecret.yaml` | New |
| `tenants/platform/services/headlamp/values.yaml` | Add OIDC config |
| `tenants/platform/services/headlamp/postInstall/oidc-externalsecret.yaml` | New |
| `docs/OPERATIONS.md` | Add mobile login section |
