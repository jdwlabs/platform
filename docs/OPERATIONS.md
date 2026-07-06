# Platform Operations

Day-2 operations, troubleshooting, and CI-mode reference. See
[BOOTSTRAP.md](BOOTSTRAP.md) for first-time bring-up, and
[ARCHITECTURE.md](ARCHITECTURE.md) for the structural model.

## 1. Day-2 access

| Service   | URL                            | Get credentials                                                                                                       |
|-----------|--------------------------------|-----------------------------------------------------------------------------------------------------------------------|
| ArgoCD UI | `https://argocd.jdwlabs.com`  | `kubectl -n argocd get secret argocd-initial-admin-secret -o jsonpath='{.data.password}' \| base64 -d`               |
| Headlamp  | `https://dashboard.jdwlabs.com` | Log in via Dex: credentials stored in `kv/argocd-dex` (see §1.2 for password rotation)                             |
| Grafana   | `https://grafana.jdwlabs.com` | `admin` / value at `kv/grafana` field `admin_password`                                                                |
| db-ui     | `https://db.jdwlabs.com`      | Cluster-side OAuth via gitops-managed config                                                                          |
| Vault     | `https://vault.jdwlabs.com`   | Root token in `secret/vault/vault-init` (offline copy required for break-glass)                                       |

> `platformctl` does not currently expose URL/credential lookup commands.
> Adding `platformctl access <service>` is a tracked v2 feature.

### 1.1 ArgoCD initial login (fresh cluster)

On a fresh bootstrap the HTTPS HTTPRoute may not be fully up yet (wildcard
cert still issuing). Use a port-forward to reach ArgoCD before the ingress is
ready:

```bash
kubectl -n argocd port-forward svc/argocd-server 8080:80
# then open http://localhost:8080 in a browser
```

Get the auto-generated admin password:

```bash
# Linux / macOS
kubectl -n argocd get secret argocd-initial-admin-secret \
  -o jsonpath='{.data.password}' | base64 -d && echo

# Windows PowerShell
kubectl -n argocd get secret argocd-initial-admin-secret `
  -o jsonpath='{.data.password}' | ForEach-Object { [System.Text.Encoding]::UTF8.GetString([System.Convert]::FromBase64String($_)) }
```

Log in with username `admin` and the password above. Change the password in
**User Info → Update Password** immediately; ArgoCD automatically deletes the
`argocd-initial-admin-secret` Secret once you do.

> **Secret gone already?** If the initial-admin-secret was deleted (password
> already changed or manually removed), reset the password via the `argocd`
> CLI:
> ```bash
> argocd account update-password --account admin --new-password <new-password>
> ```
> or patch the bcrypt hash directly:
> ```bash
> # generate bcrypt hash
> htpasswd -nbBC 10 "" <new-password> | tr -d ':\n' | sed 's/$2y/$2a/'
> # patch the argocd-cm ConfigMap
> kubectl -n argocd patch cm argocd-cm \
>   -p '{"data":{"accounts.admin":"apiKey,login"}}'
> kubectl -n argocd patch secret argocd-secret \
>   -p "{\"stringData\":{\"admin.password\":\"<bcrypt-hash>\",\"admin.passwordMtime\":\"$(date +%FT%T%Z)\"}}"
> kubectl -n argocd rollout restart deploy/argocd-server
> ```


### 1.2 Headlamp mobile login (OIDC via Dex)

Open `https://dashboard.jdwlabs.com` on any device. You are redirected to the
Dex login form at `https://argocd.jdwlabs.com/api/dex/auth`. Enter the
credentials you seeded in `kv/argocd-dex`. 1Password autofill works on mobile.
For the full phone workflow — home-screen setup, session/refresh-token
lifetimes, and the auth options evaluated — see
[MOBILE-ACCESS.md](MOBILE-ACCESS.md).

**Rotate the Dex admin password:**

1. Generate a new bcrypt hash (cost 10):
   ```bash
   # Linux / macOS
   htpasswd -bnBC 10 "" <new-password> | tr -d ':\n'
   ```
2. Update Vault:
   ```bash
   platformctl bootstrap seed argocd-dex
   # PLATFORMCTL_ARGOCD_DEX_ADMIN_PASSWORD_HASH=<new-hash>
   # PLATFORMCTL_ARGOCD_DEX_HEADLAMP_CLIENT_SECRET=<keep-existing-or-rotate>
   ```
3. Wait ~1 min for the `dex-secrets` ExternalSecret to refresh, then restart Dex:
   ```bash
   kubectl rollout restart deploy/argocd-dex-server -n argocd
   ```

> Note: Headlamp forwards your Dex id_token as the bearer token on every
> Kubernetes API call — it does NOT proxy through a service account. The
> kube-apiserver must therefore trust the Dex issuer (Talos
> `cluster.apiServer.extraArgs` `oidc-*` flags, managed in the
> infrastructure repo), and access is granted by the `headlamp-oidc-admin`
> ClusterRoleBinding mapping `oidc:admin@jdwlabs.com` to cluster-admin.
> Full cluster-admin for the single Dex user is intentional for a
> single-operator homelab.

## 2. Vault lifecycle

**Unseal after pod restart:**

```bash
kubectl -n vault exec -it vault-0 -- vault operator unseal <key-1>
kubectl -n vault exec -it vault-0 -- vault operator unseal <key-2>
kubectl -n vault exec -it vault-0 -- vault operator unseal <key-3>
```

Keys live at `secret/vault/vault-init` (Kubernetes Secret) and in your
offline `vault-init.json` backup. If both are gone, you have lost the
keys — restore Vault from a snapshot or reinstall.

**Root token rotation, re-key:** see upstream Vault docs. `platformctl`
does not orchestrate these yet.

## 3. PostgreSQL operations

**Manual backup trigger:**

```bash
kubectl -n database create job --from=cronjob/postgres-backup postgres-backup-manual-$(date +%s)
```

**Restore from SQL dump (`.sql.gz`):**

See **[RESTORE.md](RESTORE.md)** for the full step-by-step guide, including
Windows-specific `kubectl cp` quirks, ownership fixes, and the mandatory
`app` role password reset after every restore.

**Restore from CNPG snapshot (WAL/declarative):**

Edit the `Cluster` CR's `spec.bootstrap.recovery.backup.name` to the target
snapshot and re-sync the Application. The Atlas migration job will replay on top.

**Failover:** CNPG promotes a healthy replica automatically when the
primary fails. Force a manual switchover with:

```bash
kubectl -n database cnpg promote <cluster-name> <replica-pod-name>
```

(requires the `cnpg` kubectl plugin)

## 4. TLS certs

**Force re-issue:**

```bash
platformctl bootstrap heal --tls-reissue
```

Deletes every cert-manager-managed TLS Secret cluster-wide; cert-manager
re-issues on the next reconcile.

**ClusterIssuer health:**

```bash
kubectl get clusterissuer letsencrypt-prod -o yaml | yq '.status.conditions'
```

**DNS-01 troubleshooting:** Check the porkbun-webhook pod logs in
`cert-manager`:

```bash
kubectl -n cert-manager logs deploy/porkbun-webhook
```

## 5. Troubleshooting symptom → fix

| Symptom                                                           | Fix                                                                                                        |
|-------------------------------------------------------------------|------------------------------------------------------------------------------------------------------------|
| `applicationset/platform-services` stuck terminating             | `platformctl bootstrap heal --stuck-finalizer --kind ApplicationSet --name platform-services`              |
| Pods CrashLoop with `Error: secret "<name>" not found`           | `platformctl tenants verify-secrets` — reports every ExternalSecret ref that fails to resolve against live Vault (missing kv path or missing field) |
| ArgoCD App stuck `OutOfSync` after manual edit                   | `kubectl annotate app <name> -n argocd argocd.argoproj.io/refresh=hard`                                    |
| Cert is `Pending` for >10 minutes                                | `kubectl describe certificate <name> -n <ns>` → look at events; usually DNS-01 propagation                 |
| ARC runners offline in GitHub                                    | Check `kv/<tenant>-github-app` field `installation_id`; check ARC controller logs in `arc-systems`         |
| New tenant ns won't reconcile                                    | Re-run `platformctl tenants validate tenants/`                                                             |
| "Immutable field" errors during GitOps takeover                  | Delete the conflicting Deployments/StatefulSets/Pods so ArgoCD re-creates them                             |
| Orphan tenant namespaces after removing a tenant from `tenants/` | `platformctl bootstrap heal --orphan-namespaces`                                                           |
| Vault kubernetes auth backend / `vault-admin` policy missing     | Re-run `platformctl bootstrap phase 3` on a fresh cluster (admin bootstrap is now part of phase 3, not a postInstall Job) |
| CNPG clusters not healthy                                        | Check Longhorn pods in `longhorn-system`; check PVC binding                                                |
| Pod stuck in `CrashLoopBackOff` 100+ restarts, no logs          | Liveness restarts reuse same overlay fs; stale state survives. Full pod delete breaks the cycle: `kubectl delete pod <name> -n <ns>`. If pod is a CNPG standby with I/O errors, also delete its PVC — CNPG will pg_basebackup from primary. |
| CNPG standby `input/output error` on pgdata chmod               | Longhorn remount stuck. Delete pod + PVC: `kubectl delete pod <name> -n database && kubectl delete pvc <name> -n database`. CNPG creates replacement via pg_basebackup automatically. |
| `platformctl bootstrap heal --cert-approver` fails "not found"  | ArgoCD app is named `platform-kubelet-serving-cert-approver`, not `kubelet-serving-cert-approver`. Refresh directly: `kubectl annotate application platform-kubelet-serving-cert-approver -n argocd argocd.argoproj.io/refresh=normal --overwrite` |
| `platform-nginx-gateway-fabric` stuck `OutOfSync` / `Running`   | Helm cert-generator Job TTL race; run `platformctl bootstrap heal --stuck-sync --sync-app platform-nginx-gateway-fabric` |
| Gateway HTTPS listener `InvalidListener` / all HTTPS routes failing | `wildcard-jdwlabs-tls` secret missing; `kubectl apply -f tenants/platform/services/nginx-gateway-fabric/postInstall/certificate.yaml` then wait 5–15 min for DNS-01 |

## 6. Non-interactive / CI mode

When `--non-interactive` is set, `platformctl` reads every prompt value
from environment variables. The contract:

| Phase / prompt                                    | Env var                                          |
|---------------------------------------------------|--------------------------------------------------|
| Vault addr override                               | `PLATFORMCTL_VAULT_ADDR`                         |
| Vault token (post-init)                           | `PLATFORMCTL_VAULT_TOKEN`                        |
| `kv/porkbun` `api-key`                            | `PLATFORMCTL_PORKBUN_API_KEY`                    |
| `kv/porkbun` `secret-key`                         | `PLATFORMCTL_PORKBUN_SECRET_KEY`                 |
| `kv/grafana` `admin-user`                         | `PLATFORMCTL_GRAFANA_ADMIN_USER`                 |
| `kv/grafana` `admin-password`                     | `PLATFORMCTL_GRAFANA_ADMIN_PASSWORD`             |
| `kv/longhorn` `htpasswd_string`                   | `PLATFORMCTL_LONGHORN_HTPASSWD`                  |
| `kv/alertmanager` `discord_webhook_url`           | `PLATFORMCTL_ALERTMANAGER_DISCORD_WEBHOOK`       |
| `kv/usersrole` `jwt_secret`                       | `PLATFORMCTL_USERSROLE_JWT_SECRET`               |
| `kv/argocd-dex` `admin-password-hash`             | `PLATFORMCTL_ARGOCD_DEX_ADMIN_PASSWORD_HASH`     |
| `kv/argocd-dex` `headlamp-client-secret`          | `PLATFORMCTL_ARGOCD_DEX_HEADLAMP_CLIENT_SECRET`  |
| `kv/<tenant>-github-app` `github_app_id`          | `PLATFORMCTL_<TENANT>_GITHUB_APP_ID`             |
| `kv/<tenant>-github-app` `github_app_installation_id` | `PLATFORMCTL_<TENANT>_GITHUB_INSTALLATION_ID` |
| `kv/<tenant>-github-app` `github_app_private_key` | `PLATFORMCTL_<TENANT>_GITHUB_PRIVATE_KEY`        |
| `kv/<tenant>-ai-keys` `openai_api_key` (optional) | `PLATFORMCTL_<TENANT>_OPENAI_API_KEY`            |
| `kv/<tenant>-ai-keys` `anthropic_api_key` (optional) | `PLATFORMCTL_<TENANT>_ANTHROPIC_API_KEY`      |
| `kv/<tenant>-ai-keys` `openrouter_api_key` (optional) | `PLATFORMCTL_<TENANT>_OPENROUTER_API_KEY`   |
| `kv/<tenant>-discord-bot-token` `token` (optional) | `PLATFORMCTL_<TENANT>_DISCORD_BOT_TOKEN`       |
| `kv/rclone-gdrive` `rclone_conf` (Phase 5)        | `PLATFORMCTL_RCLONE_CONF`                        |

Tenant name in env-var keys is uppercased, with `-` → `_`. So tenant
`dotablaze-tech` maps to `PLATFORMCTL_DOTABLAZE_TECH_GITHUB_APP_ID`.

**`--json` event stream:** every state transition emits one
newline-delimited JSON line. Schema:

```json
{"ts":"2026-05-12T18:00:00Z","phase":"bootstrap","name":"vault-init","status":"ok","message":"applied"}
```

`status` is one of `info | progressing | ok | broken | failed`. Exit codes:

| Code | Meaning                       |
|------|-------------------------------|
| 0    | Done                          |
| 1    | Hard failure                  |
| 2    | Still progressing (timed out) |
| 3    | Broken state                  |
| 4    | User aborted                  |

**Example GHA workflow:**

```yaml
jobs:
  bootstrap-staging:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Install platformctl
        run: |
          curl -fsSL https://github.com/jdwlabs/platform/releases/latest/download/platformctl-linux-amd64 \
            -o /usr/local/bin/platformctl
          chmod +x /usr/local/bin/platformctl
      - name: Bootstrap
        env:
          KUBECONFIG: ${{ secrets.STAGING_KUBECONFIG }}
          PLATFORMCTL_VAULT_ADDR: ${{ secrets.STAGING_VAULT_ADDR }}
          PLATFORMCTL_PORKBUN_API_KEY: ${{ secrets.PORKBUN_API_KEY }}
          # ... every PLATFORMCTL_* var the seed specs need
        run: platformctl bootstrap --non-interactive --branch ${{ github.sha }} --json
```

## 7. Cluster lifecycle

**Drain a node:** `kubectl drain <node> --ignore-daemonsets --delete-emptydir-data`

**Rolling Talos upgrade:** see `jdwlabs/infrastructure` README. After
the upgrade completes, `platformctl bootstrap verify` should report all
gates green.

**Disaster recovery (rebuild from this repo):**

1. Restore the Vault snapshot (if available) into a fresh cluster.
2. `platformctl bootstrap --non-interactive` with the env vars supplied
   from the operator's offline backup.
3. PostgreSQL clusters auto-restore from their CNPG backups (configured
   to use the rclone-gdrive remote).

## 8. Observability quick-refs

**Loki queries:**

```
{namespace="argocd"} |= "ERROR"                               # ArgoCD errors
{namespace="vault"} | json | __error__=""                     # Structured Vault logs
{namespace="cert-manager", container="cert-manager"} |= "DNS" # DNS-01 detail
```

**Prometheus alert routes:** alerts route via `kv/alertmanager`
`slack_webhook`. Verify by inspecting the alertmanager-config ConfigMap.

**Where to look first when X is broken:**

| Subsystem        | Start here                                           |
|------------------|------------------------------------------------------|
| GitOps reconcile | `kubectl get app -n argocd`, then `argocd app get`   |
| Secrets          | `kubectl get clustersecretstore`, then ExternalSecret |
| Certs            | `kubectl get clusterissuer,certificate -A`            |
| Postgres         | `kubectl get cluster -n database -o wide` (CNPG plugin) |
| ARC runners      | `kubectl get pods -n arc-systems`                    |
| Gateway (NGF)    | `kubectl get pods -n nginx-gateway`, then check `NginxGatewayFabricDown`/`NginxGatewayFabricReconcileErrorsHigh` alerts (control-plane health only, not request-level) |
