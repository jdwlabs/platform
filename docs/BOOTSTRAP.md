# Platform Bootstrap

Step-by-step guide for bringing the platform up on a fresh Kubernetes cluster.
This document covers the happy path. For day-2 operations and troubleshooting
see [OPERATIONS.md](OPERATIONS.md). For architecture see
[ARCHITECTURE.md](ARCHITECTURE.md). For adding tenants after the platform is
running, see [ONBOARDING.md](ONBOARDING.md).

## 1. Prerequisites

- [ ] Kubernetes cluster provisioned via `jdwlabs/infrastructure`
      (Terraform + `talops`); all nodes Ready
- [ ] `kubeconfig` context selected: `kubectl config current-context`
- [ ] Vault accessible from the operator workstation
      (default `http://vault.vault.svc:8200`; override via
      `PLATFORMCTL_VAULT_ADDR`)
- [ ] Platform repo cloned:
      `git clone https://github.com/jdwlabs/platform.git`
- [ ] `rclone` installed locally (only needed to authorize the Google Drive
      remote for Phase 5)

## 2. Install `platformctl`

`platformctl` is a single static binary. Pick a release from
[Releases](https://github.com/jdwlabs/platform/releases) and drop it in
your PATH.

**Linux/macOS:**

```bash
curl -fsSL \
  "https://github.com/jdwlabs/platform/releases/latest/download/platformctl-$(uname -s | tr A-Z a-z)-$(uname -m)" \
  -o /usr/local/bin/platformctl
chmod +x /usr/local/bin/platformctl
```

**Windows (PowerShell):**

```powershell
Invoke-WebRequest -Uri https://github.com/jdwlabs/platform/releases/latest/download/platformctl-windows-amd64.exe `
  -OutFile $env:USERPROFILE\bin\platformctl.exe
```

**From source:**

```bash
go install github.com/jdwlabs/platform/cmd/platformctl@latest
```

## 3. One-shot bootstrap

The fast path. Run from a checkout of this repo:

```bash
platformctl bootstrap
```

`platformctl` walks the five phases (see §4), pausing for the manual touches
each phase requires. To skip prompts, set every required `PLATFORMCTL_*`
env var and add `--non-interactive`. See [OPERATIONS.md §6](OPERATIONS.md#6-non-interactive--ci-mode) for the full env
contract.

To bootstrap from a non-`main` branch (e.g. testing a PR):

```bash
platformctl bootstrap --branch feature/my-change
```

## 4. Phase summary

| # | Phase            | What it does                                                      | Manual touch                  |
|---|------------------|-------------------------------------------------------------------|-------------------------------|
| 1 | `argocd-install` | `helm upgrade --install platform-argo-cd` from tenant values      | —                             |
| 2 | `root-apply`     | Apply `bootstrap/root-app.yaml`; wait AppProjects + governance AppSet | —                          |
| 3 | `vault-init`     | `vault operator init`, unseal, store keys/token, enable kv-v2     | Save `vault-init.json` offline (interactive prompt) |
| 4 | `vault-seed`     | Write all platform + tenant kv paths                              | Provide secret values via prompts or `PLATFORMCTL_*` env |
| 5 | `backups-init`   | Capture rclone Google Drive OAuth token                           | Run `rclone authorize "drive"` out-of-band, paste block |

After all five phases, run:

```bash
platformctl bootstrap verify
```

`verify` asserts every readiness gate (ArgoCD ready, root applied, Vault
initialized, ExternalSecrets synced, backups configured, all healthy).

## 5. Re-running phases

Every phase is idempotent. Re-run a single phase:

```bash
platformctl bootstrap phase 3       # vault-init only
platformctl bootstrap verify        # verify gates
```

If a phase reports `state=already_done` on Detect, only Verify runs.

## 6. DNS records

After `verify` reports green, configure DNS at the registrar:

| Record              | Type   | Target                                |
|---------------------|--------|---------------------------------------|
| `*.jdwlabs.com`     | A/AAAA | Cluster gateway external IP           |
| `_acme-challenge.*` | (auto) | Managed by porkbun-webhook for DNS-01 |

Apex records and per-service hostnames are documented per-tenant in
`tenants/<tenant>/services/<svc>/`.

## 7. Non-interactive bootstrap

For CI or scripted re-installs, see [OPERATIONS.md §6](OPERATIONS.md#6-non-interactive--ci-mode).

## 8. Heal commands index

If a fresh bootstrap leaves the cluster in a known-bad state:

| Symptom                                              | Command                                                                     |
|------------------------------------------------------|-----------------------------------------------------------------------------|
| `applicationset/platform-services` stuck terminating | `platformctl bootstrap heal --stuck-finalizer --kind ApplicationSet --name platform-services` |
| `default` AppProject missing                         | `platformctl bootstrap heal --default-project`                              |
| `kubelet-serving-cert-approver` Application stale    | `platformctl bootstrap heal --cert-approver`                                |
| TLS certs not reissuing                              | `platformctl bootstrap heal --tls-reissue`                                  |
| Tenant ns left over after removing a tenant          | `platformctl bootstrap heal --orphan-namespaces`                            |
| Longhorn pre-upgrade hook fails on fresh cluster     | `platformctl bootstrap heal --longhorn-fresh-install`                       |
| Run every safe recovery in order                     | `platformctl bootstrap heal --all`                                          |

All heal subcommands are idempotent and emit `--json` events when that flag is set.

## 9. Dependency chain reference

```
Kubernetes cluster ready
  |
  +-- argocd-install (Phase 1: Helm)
       |
       +-- root-apply (Phase 2)
            |
            +-- Wave -1: platform-crds (Gateway API, Prometheus, Cert-Manager)
            +-- Wave 0:  bootstrap (AppProjects, argo-cd self-management)
            +-- Wave 1:  governance cascade (Namespaces, RBAC, NetworkPolicies)
                          + cert-manager, nginx-gateway-fabric, Longhorn
            +-- Wave 2:  Vault + ESO (+ ClusterSecretStores)
                          |
                          +-- vault-init (Phase 3) initializes Vault
                          +-- vault-seed (Phase 4) populates kv paths
                                |
                                +-- ExternalSecrets resolve (all tenants)
                                +-- cert-manager ClusterIssuers + TLS certs
                                +-- Longhorn ingress auth
                                +-- ARC runners register with GitHub
            +-- Wave 3:  CNPG operator, ARC controller
            +-- Wave 4:  PostgreSQL clusters, db-ui, litellm-db, litellm-redis
            +-- Wave 5:  postgres-backup, litellm, holmes,
                          Tenant ARC runner sets, Atlas schema migrations
            +-- Wave 6:  ai-sre-relay (alert webhook -> holmes/litellm)
```

Per-service wave assignments are authoritative in `tenants/<name>/tenant.yaml`
(`syncWave` field).
