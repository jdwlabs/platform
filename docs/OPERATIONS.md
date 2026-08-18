# Platform Operations

Day-2 operations, troubleshooting, and CI-mode reference. See
[BOOTSTRAP.md](BOOTSTRAP.md) for first-time bring-up, and
[ARCHITECTURE.md](ARCHITECTURE.md) for the structural model.

## 0. Installing / upgrading `platformctl`

`platformctl` has no auto-update path: a merge to `main` only reaches an
operator's workstation once someone rebuilds and reinstalls it there. Always
check the build identity after installing — a stale binary silently keeps
running old behavior with no error.

```bash
cd cli
make build              # writes ./bin/platformctl (or bin/platformctl.exe on Windows)
make install            # installs to /usr/local/bin, override with PREFIX
make install PREFIX=~/bin
```

`PREFIX` defaults to `/usr/local/bin`; set it to wherever `platformctl` is
actually on your `PATH` (e.g. `PREFIX=~/bin` for a per-user install with no
`sudo`).

Verify the install picked up the change you expect:

```bash
platformctl --version
# platformctl version <git-describe> (commit <short-sha>, built <UTC timestamp>)
```

If the commit shown is older than `main`, the install did not take — rerun
`make install` and re-check, rather than assuming the new behavior is live.

**Windows:** `make`/`install` work if you have both on `PATH` (e.g. via Git
Bash, which ships `/usr/bin/install`); otherwise build directly and copy the
binary by hand:

```powershell
cd cli
go build -buildvcs=false -o bin\platformctl.exe .\cmd\platformctl
Copy-Item bin\platformctl.exe C:\Users\<you>\bin\platformctl.exe -Force
```

Always give the output an explicit `.exe` name. A build written to an
extensionless path can sit next to an existing `platformctl.exe` on the same
`PATH` directory — `Get-Command`/`which` will resolve the new extensionless
file, but Windows still launches the old `.exe` when you type `platformctl`,
so the stale binary keeps running invisibly. Overwriting `platformctl.exe`
in place removes the ambiguity.

## 1. Day-2 access

| Service   | URL                            | Get credentials                                                                                                       |
|-----------|--------------------------------|-----------------------------------------------------------------------------------------------------------------------|
| ArgoCD UI | `https://argocd.jdwlabs.com`  | `kubectl -n argocd get secret argocd-initial-admin-secret -o jsonpath='{.data.password}' \| base64 -d`               |
| Headlamp  | `https://dashboard.jdwlabs.com` | Log in via Dex: credentials stored in `kv/argocd-dex` (see §1.2 for password rotation)                             |
| Grafana   | `https://grafana.jdwlabs.com` | `admin` / value at `kv/grafana` field `admin_password`                                                                |
| db-ui     | `https://db.jdwlabs.com`      | Cluster-side OAuth via gitops-managed config                                                                          |
| Vault     | `https://vault.jdwlabs.com`   | Root token in `secret/vault/vault-init` (offline copy required for break-glass)                                       |
| Proxmox   | `https://pve<1-5>.attlocal.net:8006` (LAN); Tailscale subnet router off-LAN — route live, off-LAN access not yet demonstrated (§1.3) | Proxmox's own local auth — not gitops-managed |

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

### 1.3 Proxmox cluster UI (hypervisor management)

Proxmox (`:8006` on each of the five `pve*` hosts) is **not** a platform
service — it is the hypervisor layer this cluster runs on, owned by the
`infrastructure` repo, not by any Helm chart or `tenant.yaml` entry here.
It is documented in this table because it is still part of day-2 admin
access, and because the access-model decision below was made while working
this ticket.

**Access model: VPN-only via the Tailscale subnet router, not a public
`nginx-gateway-fabric` reverse-proxy subdomain.**

Reasoning:

- Proxmox is a privileged management surface (VM console access, storage,
  backups, cluster settings) — the same category of exposure risk as the
  Kubernetes (`6443`) and Talos (`50000`) APIs, which were deliberately
  pulled off the public internet, with a Tailscale subnet router on the
  HAProxy VM as the replacement path. Reversing that call for
  Proxmox — a surface with *more*
  blast radius than either API — would be inconsistent with that posture.
- The subnet router advertises the whole `192.168.1.0/24` LAN, not just the
  HAProxy VM. Every `pve*` host's `:8006` is therefore covered by the same
  approved route — a second, separate access mechanism
  (reverse proxy + its own auth layer, a second TLS cert, a second set of
  credentials to rotate) would duplicate a path that standing up the subnet
  router already provides for free.
- A reverse-proxy subdomain would need its own auth layer in front of
  Proxmox's own login (Proxmox has no OIDC/Dex-style delegation this repo
  could reuse today, unlike Headlamp/ArgoCD), which is meaningfully more to
  build and maintain than documenting the VPN path.

**Current state (verified 2026-08-05):** Proxmox UI is LAN-only. Port `8006`
on the WAN IP refuses the connection — the same signature as the already
closed `6443`/`50000` — while `443` on the same IP answers normally. No
router change is required by this decision; there is nothing to close.

**How to reach it today (LAN):** `https://pve<1-5>.attlocal.net:8006`,
resolved by the gateway's own DNS at `192.168.1.254` — see
[`infrastructure/docs/host-addressing.md`](https://github.com/jdwlabs/infrastructure/blob/main/docs/host-addressing.md)
for the full host/address table. The `pve5` record currently answers with
two addresses; only `192.168.1.204` is live — retry if a `pve5` connection
is refused.

**How to reach it off-LAN:** over the same Tailscale subnet router stood up for
cluster admin — see
[`infrastructure/docs/tailscale-subnet-router.md`](https://github.com/jdwlabs/infrastructure/blob/main/docs/tailscale-subnet-router.md).
As of 2026-08-13 the HAProxy VM (`192.168.1.199`) is on the tailnet as
`haproxy-1` and its `192.168.1.0/24` route is approved, so the path exists on
paper. It has **not** been exercised from off-LAN yet — no tailnet device on a
different network has used the route — so treat off-LAN Proxmox access as
unproven rather than working, and expect to debug it on first use. This is a
documented dependency, not a gap in this repo.

**What "reachable via a stable domain name" still needs:** the
`pve*.attlocal.net` names only resolve when a client
queries the gateway (`192.168.1.254`) directly; Tailscale does not forward
DNS queries to it by default (the subnet router install intentionally sets
`--accept-dns=false`). An off-LAN tailnet client therefore needs either a
[Tailscale split-DNS nameserver](https://tailscale.com/kb/1054/dns#nameservers-and-split-dns)
pointed at `192.168.1.254` for the `attlocal.net` domain, or a small set of
MagicDNS/internal-DNS records for the `pve*` hosts — both are Tailscale
admin-console / DNS-server changes with no Terraform or platformctl
surface, so they belong to the infrastructure repo alongside the rest of
the subnet-router runbook, not to this repo. No public `jdwlabs.com` DNS
record is needed or planned for Proxmox under this decision.

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

**Reading the `vault-pod` health check:** it asserts pods are *Ready*, not
merely Running. Readiness is the seal signal — the chart's readiness probe
runs `vault status`, whose exit code encodes seal state — so a sealed Vault
shows as `Running, not Ready` and fails the check. A partially-ready set
warns rather than fails, because a raft quorum of unsealed members still
serves reads.

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

### 3.1 Moving the backup Drive remote off rclone's shared client_id

The `[gdrive]` remote that uploads the dumps carries no `client_id`, so rclone
authenticates with the OAuth credentials bundled in its binary. Google is
retiring those during 2026 and every backup run says so:

```text
NOTICE: gdrive: This remote uses rclone's shared Google Drive client_id, which
is being retired and will stop working during 2026.
```

Uploads still succeed today. When the retirement lands they stop, with no
warning beyond that NOTICE — and job logs are garbage-collected within a day,
so `platformctl cluster status` asserts the state instead (Secrets →
`rclone-gdrive-client-id`, ⚠ until this procedure is done).

**This is one atomic credential swap, not two changes.** A Google refresh token
is only valid for the OAuth client that issued it, so pasting a new `client_id`
next to the existing `token` breaks the remote at its first token refresh. The
client, the secret and a freshly authorized token are written together or not
at all — `platformctl` rejects a block carrying only half a credential.

Steps 1-6 need a browser and the Google account that owns the shared drive; the
rest is CLI.

1. **Google Cloud Console**, signed in as the Google account with at least
   *Content manager* on the shared drive holding `postgres-backups`. Create a
   project dedicated to this remote (e.g. `jdwlabs-backups`) — nothing else on
   the platform uses Google Cloud, and a project of its own keeps the consent
   screen scoped to this one job.
2. **APIs & Services → Library → Google Drive API → Enable.**
3. **APIs & Services → OAuth consent screen.** User type *External* (*Internal*
   if the account is Google Workspace); fill in app name and support email.
   Then set **Publishing status to "In production"**. Leaving it in *Testing*
   caps refresh tokens at 7 days, which would stop the backups a week after the
   rotation looked successful.
4. **Data access → Add scopes:** `https://www.googleapis.com/auth/drive`
   (matches the remote's `scope = drive`). rclone's guide also lists
   `.../auth/docs` and `.../auth/drive.metadata.readonly`; adding all three
   matches upstream and costs nothing.
5. **Credentials → Create credentials → OAuth client ID → Application type
   "Desktop app".** Desktop clients accept rclone's loopback redirect with
   nothing to type in. If you pick *Web application* instead, you must add the
   redirect URI `http://127.0.0.1:53682/` exactly. Copy the client ID and
   client secret.
6. On a workstation with `rclone` and a browser, authorize against the new
   client and keep the token JSON it prints:

   ```bash
   rclone authorize "drive" "<client-id>" "<client-secret>"
   ```

7. Assemble the replacement block. Read the current one for the `team_drive`
   id — it identifies the shared drive and must be carried over verbatim:

   ```bash
   kubectl -n database get secret rclone-gdrive \
     -o jsonpath='{.data.rclone\.conf}' | base64 -d
   ```

   ```ini
   [gdrive]
   type = drive
   scope = drive
   team_drive = <existing id>
   client_id = <new client id>
   client_secret = <new client secret>
   token = <JSON printed by rclone authorize>
   ```

8. Write it to `kv/rclone-gdrive` property `rclone_conf` — the only place this
   credential lives. The seed validates the block before it reaches Vault and
   merges over the path, so nothing else there is disturbed:

   ```bash
   PLATFORMCTL_RCLONE_CONF="$(cat gdrive.conf)" \
     platformctl bootstrap seed rclone-gdrive --field rclone_conf --non-interactive
   rm gdrive.conf
   ```

9. The ExternalSecret refreshes hourly; force it rather than wait:

   ```bash
   kubectl -n database annotate externalsecret rclone-gdrive \
     force-sync="$(date +%s)" --overwrite
   ```

10. Confirm, in this order — the check reads the synced Secret, the job run
    proves Google accepts the credential:

    ```bash
    platformctl cluster status          # Secrets → rclone-gdrive-client-id: ✓
    job=postgres-backup-manual-$(date +%s)
    kubectl -n database create job --from=cronjob/postgres-backup "$job"
    kubectl -n database wait --for=condition=complete job/"$job" --timeout=10m
    kubectl -n database logs job/"$job" -c upload
    ```

    A completed job with no `shared Google Drive client_id` NOTICE is the
    finished state.

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
| Pods CrashLoop with `Error: secret "<name>" not found`           | `platformctl tenants verify-secrets` — reports every ExternalSecret ref that fails to resolve against live Vault (missing kv path or missing field). Scans the `tenants/` tree by default; `--source cluster` scans applied state. The summary names any ref class it did not check |
| ArgoCD App stuck `OutOfSync` after manual edit                   | `kubectl annotate app <name> -n argocd argocd.argoproj.io/refresh=hard`                                    |
| Cert is `Pending` for >10 minutes                                | `kubectl describe certificate <name> -n <ns>` → look at events; usually DNS-01 propagation                 |
| ARC runners offline in GitHub                                    | ARC is dormant by default (CI runs on GitHub-hosted runners — see "Self-hosted CI runners (ARC)" below). If re-enabled: check `kv/<tenant>-github-app` field `installation_id`; check ARC controller logs in `arc-systems` |
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
| `statefulset-revisions` warns `N/M pending roll` | A StatefulSet has an applied revision its pods have not adopted — expected under `updateStrategy: OnDelete`, where the controller records a new `updateRevision` but never rolls pods. Nothing is broken; the change is simply not running yet. Adopt it by deleting the pods deliberately, one at a time, verifying health between each. For Vault, expect a seal after each delete until the auto-unseal CronJob catches up. |
| `image-drift` warns `declared X, running Y` for a workload         | An ArgoCD Application's Deployment/StatefulSet/DaemonSet spec asks for one image tag, but a pod matched by that workload's own selector is still running another. ArgoCD reports the Application Synced regardless — sync only confirms the spec was applied, not that any pod picked it up. Same root cause class as `statefulset-revisions` (stuck rollout, `OnDelete`, or an evicted-but-not-recreated DaemonSet pod), generalized to every workload kind. Fix by recreating the stale pod(s) (`kubectl delete pod <name> -n <ns>`) and re-running `cluster status` to confirm the running tag now matches. |
| `limitrange-adoption` warns `N container(s) predate their namespace LimitRange` | A namespace's `LimitRange` only defaults `resources.requests`/`limits` at pod admission — a one-time mutation on create, not a continuously enforced rule. Pods that existed before the `LimitRange` was applied keep running without the default forever; nothing later reconciles them, and ArgoCD reports the `LimitRange` resource itself Synced regardless. The listed pods are naked (often `BestEffort`) despite the namespace looking policy-governed. Fix by recreating the named pod(s) so they re-admit under the current `LimitRange` — for a Deployment/StatefulSet-owned pod, `kubectl delete pod <name> -n <ns>` is enough; for Longhorn `engine-image-*`/`instance-manager-*` pods this happens naturally on the next node drain or Longhorn manager restart. |
| Detached Longhorn volumes accumulate after a StatefulSet is rebuilt | `longhorn-single` uses `Retain`, so a volume outlives the claim it was created for and never ages out. `platformctl cluster volumes list --class orphaned` reports which are genuinely unclaimed; `platformctl cluster volumes reclaim --all-orphaned --dry-run` shows exactly what a reclaim would delete and mutates nothing; re-run with `--confirm` to delete. Do **not** identify candidates by name or by the volume's own `status.kubernetesStatus.pvcName` — that field records the claim the volume was created for and repeats across every generation of the same StatefulSet, so several volumes carry the identical name and only one is live. The command resolves the claim the other way round, from each PVC's `spec.volumeName`, and refuses any volume a PVC or a `Bound` PersistentVolume still points at. Deleting the `volumes.longhorn.io` object cascades to its replicas and its `Released` PV. |
| TrueNAS zvols/datasets accumulate after PVCs are deleted | Both TrueNAS classes use `Retain`, so deleting a PVC deletes nothing on the NAS: one `truenas-iscsi` PVC leaks a zvol, an extent, a target and the target-extent mapping; one `truenas-nfs` PVC leaks a dataset and its export. None of it is visible to `kubectl`. `platformctl cluster volumes truenas list --class orphaned` reports what is genuinely unreferenced, `... reclaim --all-orphaned --dry-run` prints the exact per-object delete plan and mutates nothing, and `--confirm` deletes. Do **not** identify candidates by name — a provisioned object is named for the PVC UID it was created for and that name outlives the PV, the PVC and the workload. Liveness is proved only from a PersistentVolume that names the object or an open iSCSI session on a target that exports it; if the session list is unreadable, every zvol is refused. See "Reclaiming leaked TrueNAS volumes" above. |
| `Released` PVs on `local-path` linger after a node is lost | The provisioner reclaims by scheduling a busybox helper pod **onto the volume's own node** to `rm -rf` the directory. The helper tolerates everything, so a cordoned or tainted node still reclaims normally, but a node that is `NotReady` or removed from the cluster can never run it — the PV stays `Released` and the on-disk `_work` stays on that node's `/var` forever. Longhorn's `Delete` needed no node scheduling, so this failure mode is new since CI `_work` moved to `local-path`. See "Self-hosted CI runners (ARC)" below for the post-incident check. |
| Grafana dashboards stop syncing from git, or `platform-grafana` goes red with `never became healthy` in the hook log | `platformctl gitsync status` reports each Connection and Repository with `health` and `sync.state`; it exits non-zero when any is unhealthy and prints the full health message. These resources live in Grafana's own API server, so `kubectl` and ArgoCD cannot see them and a red `platform-grafana` sync here means Git Sync is reporting itself broken, not that the deploy failed. Read the message on the **repository**, not the connection: a connection saying `GitHub App lacks required 'webhooks' permission` is describing a requirement derived from a bound repository's `write` workflow, and the App needs no webhooks grant. An empty result means Git Sync is credentialed but not connected. |
| An edit to `gitsync-resources.yaml` merged but changed nothing | The apply Job creates and never updates, so the next run finds both resources present and skips them. `platformctl gitsync recreate --dry-run`, then `--confirm`, deletes the repository **before** the connection (the repository references it) and requests an ArgoCD refresh of `platform-grafana` so the Job re-runs. Both delete paths refuse while the repository still owns dashboards, because its remove-orphan-resources finalizer collects whatever it owns — `--allow-owned-dashboards` overrides that only when losing them is intended. |
| A new field must be added to an already-seeded Vault path | `platformctl bootstrap seed <spec> --field <name>` writes that property alone, merging over what is there, so the other credentials are neither re-supplied nor prompted for. An unknown spec key or field name is refused with the valid set listed — a mistyped key used to select an empty spec, write nothing and still report success. |

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
| `kv/holmes` `webhook_token`                       | `PLATFORMCTL_HOLMES_WEBHOOK_TOKEN`               |
| `kv/usersrole` `jwt_secret`                       | `PLATFORMCTL_USERSROLE_JWT_SECRET`               |
| `kv/argocd-dex` `admin-password-hash`             | `PLATFORMCTL_ARGOCD_DEX_ADMIN_PASSWORD_HASH`     |
| `kv/argocd-dex` `headlamp-client-secret`          | `PLATFORMCTL_ARGOCD_DEX_HEADLAMP_CLIENT_SECRET`  |
| `kv/argocd-dex` `github-client-id`                | `PLATFORMCTL_ARGOCD_DEX_GITHUB_CLIENT_ID`        |
| `kv/argocd-dex` `github-client-secret`            | `PLATFORMCTL_ARGOCD_DEX_GITHUB_CLIENT_SECRET`    |
| `kv/argocd-dex` `google-client-id` (optional)     | `PLATFORMCTL_ARGOCD_DEX_GOOGLE_CLIENT_ID`        |
| `kv/argocd-dex` `google-client-secret` (optional) | `PLATFORMCTL_ARGOCD_DEX_GOOGLE_CLIENT_SECRET`    |
| `kv/<tenant>-github-app` `github_app_id` (optional) | `PLATFORMCTL_<TENANT>_GITHUB_APP_ID`           |
| `kv/<tenant>-github-app` `github_app_installation_id` (optional) | `PLATFORMCTL_<TENANT>_GITHUB_INSTALLATION_ID` |
| `kv/<tenant>-github-app` `github_app_private_key` (optional) | `PLATFORMCTL_<TENANT>_GITHUB_PRIVATE_KEY`  |
| `kv/<tenant>-ai-keys` `openai_api_key` (optional) | `PLATFORMCTL_<TENANT>_OPENAI_API_KEY`            |
| `kv/<tenant>-ai-keys` `anthropic_api_key` (optional) | `PLATFORMCTL_<TENANT>_ANTHROPIC_API_KEY`      |
| `kv/<tenant>-ai-keys` `openrouter_api_key` (optional) | `PLATFORMCTL_<TENANT>_OPENROUTER_API_KEY`   |
| `kv/<tenant>-ai-keys` `nvidia_api_key` (optional) | `PLATFORMCTL_<TENANT>_NVIDIA_API_KEY`            |
| `kv/<tenant>-discord-bot-token` `token` (optional) | `PLATFORMCTL_<TENANT>_DISCORD_BOT_TOKEN`       |
| `kv/rclone-gdrive` `rclone_conf` (Phase 5; re-seedable, §3.1) | `PLATFORMCTL_RCLONE_CONF`            |

Tenant name in env-var keys is uppercased, with `-` → `_`. So tenant
`dotablaze-tech` maps to `PLATFORMCTL_DOTABLAZE_TECH_GITHUB_APP_ID`.

Every `kv/<tenant>-*` path is optional: a tenant is not obliged to deploy the
service that consumes one, and the ARC runner sets that consume `-github-app`
are dormant. Seeding skips an unsupplied tenant path and says so rather than
prompting. Naming a field with `--field` writes it regardless.

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

**Tenant alert routing:** a `PrometheusRule` alert only reaches a tenant's
route if the rule's `labels` block sets `tenant: jdwlabs` or
`tenant: dotablaze-tech` — the namespace-level `platform.jdwlabs.io/tenant`
label used elsewhere in the observability stack is not visible to
Alertmanager. Both tenant routes currently resolve to the shared `discord`
receiver (see `alertmanager-config-externalsecret.yaml`); an alert with no
`tenant` label falls through unchanged to that same receiver. To verify a
tenant's route live, fire a test alert carrying the label (e.g. `amtool alert
add tenant=jdwlabs alertname=Test` against the Alertmanager API) and confirm
it lands under the `tenant = "jdwlabs"` route in the Alertmanager UI's Status
→ routing tree before checking it reached Discord.

**Tracing (Tempo) — `TempoNoSpansReceived`:**

Tempo ingesting nothing is invisible by default: a counter that never
increments publishes no series, so an ordinary threshold alert cannot fire on
it. That is why the rule pairs a rate check with `absent()`, and why an empty
query result below is a finding rather than a tooling failure.

The metric to use is `tempo_distributor_traces_per_batch_count`. Do not reach
for a spans-received counter — this Tempo publishes no metric containing
"received" or "span", so querying one returns empty no matter how many spans
are flowing, and any alert keyed on it can never clear. Confirm the name
against the running version before trusting it; it has changed across Tempo
releases:

```
kubectl -n monitoring exec deploy/platform-grafana -c grafana -- curl -sG \
  "http://platform-kube-prometheus-s-prometheus.monitoring.svc:9090/api/v1/label/__name__/values" \
  | tr ',' '\n' | grep tempo_distributor
```

1. Distinguish the two failure modes:

   ```
   sum(tempo_distributor_traces_per_batch_count)
   ```

   A **value of 0** means Tempo is up and scraped but nothing is emitting — an
   emitter problem, which is the common case. An **empty vector** means the
   metric itself is gone: either Tempo is not being scraped, or the name moved
   in an upgrade. That is a Tempo/monitoring problem, not an emitter one, so
   check step 2 before hunting for services.

2. Rule out a scrape problem before chasing emitters — `up{job="platform-tempo"}`
   should be `1`. If Tempo itself is down, the alert is a symptom, not the cause.

3. Identify what is supposed to be emitting. Tracing is opt-in per service; the
   set of instrumented services is small and deliberate, so "nothing is sending"
   is usually the removal of the one service that was.

4. If the emitting service was decommissioned on purpose, instrument a
   replacement — do not silence the alert. An empty tracing backend that nobody
   is told about is the exact condition this rule exists to surface.

**AI-SRE relay (`AiSreRelay*` alerts):**

The relay receives every critical/warning alert from Alertmanager and decides
whether it becomes an investigation, a Jira ticket, and possibly a remediation
PR. It exposes hand-written counters on `:8080/metrics` — no `go_*` or
`process_*` series, so `up` is the only health signal about the target itself.

Every `Handle()` increments exactly one of two counters, so their sum is
"alerts the relay processed" and the pair partitions every delivery:

| Metric | Meaning |
|---|---|
| `ai_sre_relay_investigations_run_total` | Alerts investigated |
| `ai_sre_relay_repeats_skipped_total` | Refires deduplicated against an open ticket |
| `ai_sre_relay_repo_rejections_total` | Remediations discarded at the repository allowlist |

Diagnosing a quiet relay — the three states look identical from Discord, and
these queries separate them:

```
up{job="ai-sre-relay", namespace="ai-sre"}                          # 1 = scraped
sum(increase(ai_sre_relay_investigations_run_total[6h]))            # work done
sum(increase(ai_sre_relay_repeats_skipped_total[6h]))               # work deduplicated
```

- Both counters flat while alerts fire → the webhook path is broken. Check the
  Alertmanager `ai-sre` receiver and the relay's bearer token, not the relay's
  own logic.
- Skips climbing, investigations flat → dedupe is swallowing everything,
  usually a Jira ticket left open past the end of its firing episode. Close it.
- `up` empty rather than 0 → the ServiceMonitor has stopped selecting the
  Service; every other relay alert is blind until it is restored.

A dark relay costs automated remediation, not visibility: the Alertmanager
route mirrors alerts to it with `continue: true`, so Discord notification is
unaffected either way. That is why these alerts are `warning` and not
`critical`.

**Where to look first when X is broken:**

| Subsystem        | Start here                                           |
|------------------|------------------------------------------------------|
| GitOps reconcile | `kubectl get app -n argocd`, then `argocd app get`   |
| Secrets          | `kubectl get clustersecretstore`, then ExternalSecret |
| Certs            | `kubectl get clusterissuer,certificate -A`            |
| Postgres         | `kubectl get cluster -n database -o wide` (CNPG plugin) |
| ARC runners      | Dormant by default — `arc-systems` should be empty; see "Self-hosted CI runners (ARC)" |
| Gateway (NGF)    | `kubectl get pods -n nginx-gateway`, then check `NginxGatewayFabricDown`/`NginxGatewayFabricReconcileErrorsHigh` alerts (control-plane health only, not request-level) |
| Tracing (Tempo)  | `sum(tempo_distributor_traces_per_batch_count)` — 0 means nothing is emitting, empty vector means the metric itself is gone; see the `TempoNoSpansReceived` steps above |
| AI-SRE relay     | `up{job="ai-sre-relay", namespace="ai-sre"}` first, then the two processing counters; see the `AiSreRelay*` steps above |

**Control-plane metrics — etcd, kube-scheduler, kube-controller-manager:**

These three ship with no telemetry by default: etcd's metrics live only
behind its mTLS client port, and both scheduler and controller-manager bind
their metrics port to loopback. The Talos machine config widens all three
(etcd gains a dedicated metrics-only listener; the other two move off
`127.0.0.1`), and this chart's `kubeEtcd` / `kubeScheduler` /
`kubeControllerManager` blocks scrape them. This is a two-repo, ordered
rollout — the machine-config side is human-applied, and the scrape config
here must not merge until it has, on all control-plane nodes, or Prometheus
records `up == 0` and the newly-enabled alert rules fire on a fault that does
not exist. Talos does not restart etcd when `cluster.etcd.extraArgs` changes,
so etcd needs a manual `talosctl service etcd restart` per node (one at a
time, confirming quorum and `HEALTH OK` between each) even after the machine
config has already rolled out to all three.

Endpoints — reachable from the node network only, not from outside the
cluster:

```
http://<control-plane-ip>:2381/metrics   # etcd — plain HTTP, no client API
https://<control-plane-ip>:10259/metrics # kube-scheduler — self-signed cert
https://<control-plane-ip>:10257/metrics # kube-controller-manager — self-signed cert
```

Prometheus discovers all three via Kubernetes service/node discovery — no
addresses are committed to git. Job names: `kube-etcd`, `kube-scheduler`,
`kube-controller-manager`.

Verification queries, run via the Prometheus API (e.g.
`kubectl port-forward -n monitoring svc/platform-kube-prometheus-s-prometheus 9090:9090`):

```
count by (job) (up)
# expect kube-etcd, kube-scheduler, kube-controller-manager present, 3 targets each

etcd_server_leader_changes_seen_total
histogram_quantile(0.99, rate(etcd_disk_wal_fsync_duration_seconds_bucket[5m]))
# both should return data, not an empty vector
```

Then confirm the alert rules loaded rather than merely being absent:
`/api/v1/rules`, filter for `etcd`, `KubeScheduler`, `KubeControllerManager`
— every one should read `inactive`, never `firing` on a healthy cluster.

**TrueNAS metrics (`truenas-*` alerts):**

TrueNAS SCALE 25.10.4 exposes no Prometheus endpoint. Its API offers exactly
one metrics export type — `GRAPHITE`, a push — so the cluster runs a
`graphite-exporter` in `monitoring` that receives the push and is scraped
normally. Metrics flow NAS → node:30203 → `truenas-graphite-exporter` →
Prometheus. This is the opposite direction to every other target here, which
has two consequences worth remembering when reading an alert:

- The NAS being healthy and the NAS *reporting* are independent. Host
  reachability for 192.168.1.205 is owned by blackbox-exporter
  (`HardwareHostUnreachable`, TCP :22); `TrueNASMetricsStale` means only that
  metrics stopped arriving.
- **A TrueNAS update resets the reporting exporter configuration.** If NAS
  alerting goes quiet after an upgrade, re-add it under Reporting → Exporters
  before looking anywhere else. Required settings, as carried by exporter
  `id=1` (`prometheus-graphite-ingest`):

  | Field | Value | Notes |
  |---|---|---|
  | `exporter_type` | `GRAPHITE` | the only type this version offers |
  | `destination_ip` | any cluster node IP | currently `192.168.1.87` |
  | `destination_port` | `30203` | pinned NodePort, see `service.yaml` |
  | `prefix` | `truenas` | first path segment |
  | `namespace` | `truenas` | second path segment; **the field is `namespace`, not "hostname"** — `GraphiteExporter` has no `hostname` field. This is what the mapping captures as `host=`, which is why it matches blackbox-exporter's `host="truenas"`. |
  | `update_every` | `30` | seconds between pushes — **not yet applied, still `1` on the NAS**, see below |
  | `buffer_on_failures` | `10` | pushes buffered while the sink is unreachable |
  | `send_names_instead_of_ids` | `true` | yields the `_serial_lunid_…` chart ids the mapping parses |
  | `matching_charts` | `*` | filtering happens in the mapping, not here |

  `update_every` should be `30`, not the default `1`. At 1s the NAS pushes
  ~476 samples every second for data that changes on the order of minutes; the
  mapping keeps 75 of those and Prometheus scrapes the result once a minute,
  so everything pushed in between is overwritten unread. 30s keeps two samples
  per scrape — enough that one dropped push does not open a gap — at a
  thirtieth of the traffic. It is a NAS-side field, not a repo one, and it is
  still at `1` because the API key needed to change it is currently invalid
  (see `docs/adr/0025-…`). Change it alone, and read it back with
  `GET /api/v2.0/reporting/exporters`; revert the whole exporter with
  `DELETE /api/v2.0/reporting/exporters/id/1`.

What the push carries is **not** what the alert names suggest it used to.
TrueNAS SCALE 25.10 replaced the stock netdata collectors with
TrueNAS-specific ones, and the charts the original mapping matched
(`zfspool`, `disk_space`, `smart_log_smart`) are not emitted at all — 100% of
samples were being dropped. The mapping now matches the chart names captured
off the live stream and keeps 75 series: per-disk I/O and busy percent, ARC
size and hit rates, per-core CPU, CPU package temperature, memory, load,
uptime, nfsd, and per-interface throughput and drops. See
`docs/adr/0025-truenas-metrics-what-the-graphite-push-can-and-cannot-carry.md`
for the capture and the full reasoning.

Two consequences of that, which are the reason NAS alerting is thinner than
it looks:

- **Pool health, pool/dataset capacity and SMART are not in the push on this
  version, in any form.** They exist on the TrueNAS API (`pool.query`,
  `pool.dataset.query`, `disk.temperatures` all return them) but nothing polls
  it yet — that needs a read-only API key this cluster does not hold. Tracked
  as JDWLABS-367. Until then a degrading pool reaches nobody through
  Prometheus; TrueNAS's own alert mail is the only notification.
- `graphite_dropped_samples_total` climbing is **normal**. The mapping ignores
  401 of the 476 pushed paths on purpose, so that counter rises continuously
  on a healthy pipeline and is not a health signal.

Units are whatever the NAS sends, confirmed against `/reporting/graphs`
`vertical_label`: memory and ARC are bytes, disk and NFS throughput are
**Kibibytes/s**, interface throughput is **Kilobits/s**. netdata signs
outbound dimensions negative, so `truenas_network_transmit_*`,
`truenas_nfsd_write_*` and `truenas_network_transmit_drops_*` arrive as
negative numbers — take `abs()` at query time.

Verification queries:

```
time() - graphite_last_processed_timestamp_seconds{job="truenas-graphite-exporter"}
# seconds since the last sample the mapping ACCEPTED. This is what
# TrueNASMetricsStale reads. A value of time() itself means the exporter has
# accepted nothing since it started — the mapping is matching nothing.

count by (__name__) ({__name__=~"truenas_.+"})     # expect 28 metric names, 75 series
truenas_memory_available_bytes / truenas_memory_total_bytes
count by (serial) (truenas_disk_busy_percent)      # expect one series per physical disk
```

If the first query returns a large number while
`graphite_dropped_samples_total` is still climbing, samples are arriving and
no longer matching — a TrueNAS upgrade renamed the charts again. Re-capture
the stream before editing the mapping; every pattern in it was written
against real captured path strings and a replacement should be too:

```
# temporary sink on any host the NAS can reach, then point exporter id=1 at it
python3 -c "import socketserver
class H(socketserver.StreamRequestHandler):
    def handle(self):
        for l in self.rfile: print(l.decode().rstrip())
socketserver.ThreadingTCPServer(('0.0.0.0',2003),H).serve_forever()"
```

### Reclaiming leaked TrueNAS volumes

Both TrueNAS classes use `reclaimPolicy: Retain`. That is the right call for
data safety and it means **nothing is cleaned up when a PVC is deleted**.
Deleting one `truenas-iscsi` PVC leaves four objects on the NAS — a zvol, an
iSCSI extent, an iSCSI target and the target-extent mapping — plus a `Released`
PV in the cluster. One `truenas-nfs` PVC leaves a dataset and its NFS export.
None of it is visible to `kubectl`, and none of it ages out.

This supersedes the manual `midclt` procedure for NFS, which had no notion of an
extent, a target or the mapping between them and so never transferred to iSCSI.

```
# what is out there, and what the tool thinks of it
platformctl cluster volumes truenas list
platformctl cluster volumes truenas list --class orphaned --full

# preview — mutates nothing, prints the exact per-object delete plan
platformctl cluster volumes truenas reclaim --all-orphaned --dry-run

# delete
platformctl cluster volumes truenas reclaim --all-orphaned --confirm

# one volume at a time; still checked against the same rules
platformctl cluster volumes truenas reclaim --name pvc-<uid> --confirm
```

`--storage-class truenas-iscsi|truenas-nfs` narrows the report to one driver.
While the NAS presents its stock self-signed certificate, add
`--truenas-ca-file <pem>` or `--truenas-insecure-skip-tls-verify`.

**Do not identify candidates by name.** Provisioned objects are named after the
PVC UID they were created for, and that name outlives the PV, the PVC and the
workload — the same trap as Longhorn's `status.kubernetesStatus.pvcName` above.
The command resolves liveness from the other side, and only two things count as
proof that storage is still in use:

- a PersistentVolume whose CSI volume handle, volume attributes, NFS path or
  iSCSI IQN names the object, with claims resolved from each PVC's
  `spec.volumeName`, and
- an open iSCSI session on a target that exports it — the only evidence that
  survives the cluster having no record of the volume at all.

If the session list cannot be read, every zvol is refused rather than reported
orphaned: unknown liveness is not idle. Anything still claimed, still `Bound`,
or reachable through a target that also exports another volume is reported as a
`refused` row and exits non-zero — never skipped quietly.

The iSCSI objects are joined by **numeric ID, not by name**. An extent's `disk`
field is the only statement of which zvol it actually exports, and a target
reaches its zvol only through a mapping row, so a target named for one volume
can be mapped to an extent exporting another. Deletes run in dependency order
(mapping → extent → target → export → dataset → `Released` PV) and every object
is re-read and matched on its exact name immediately before it is deleted.

Reclaim stays **operator-initiated**. It is not wired to a CronJob: the refusals
above depend on live session state and on PVs that a partially-synced cluster
may not have recreated yet, and an unattended run that guesses wrong deletes
data no backup of the leaked object exists for.

**Upgrade gate — the NAS does not go past 25.10.x:**

TrueNAS removes the REST API in 26. Both democratic-csi releases reach the
NAS over it (`freenas-api-nfs` and `freenas-api-iscsi`, `protocol: http` to
192.168.1.205:80), and **no released or unreleased build of democratic-csi
can speak the replacement JSON-RPC-over-WebSocket API** — verified against
driver source, not release notes, in
`docs/adr/0024-truenas-rest-removal-blocks-democratic-csi.md`.

Upgrading the NAS to 26.x therefore takes out the CSI control plane for both
`truenas-nfs` and `truenas-iscsi`. Already-bound, already-mounted volumes
keep serving — the NFS and iSCSI data paths never touch the API — but
provisioning, expansion, snapshotting and deletion all fail, and the only
remedy is downgrading the NAS.

Before any TrueNAS major upgrade, confirm the gate has lifted:

```
kubectl -n democratic-csi get secret democratic-csi-driver-config \
  -o jsonpath='{.data.driver-config-file\.yaml}' | base64 -d | head -6
# a `protocol: http` httpConnection block means the gate still applies
```

The standing REST-deprecation alert on the NAS is the live signal that the
dependency is still there. Leave it alone — it self-clears 24h after the
last REST call, and dismissing it destroys the only evidence available.

`platformctl cluster volumes truenas` is deliberately **not** part of that
dependency: it speaks JSON-RPC 2.0 over `wss://<host>/api/current`, the API
that replaces REST in 26, behind a single `Caller` interface. So the reclaim
tooling already works on a 26.x NAS, it does not reset the deprecation alert's
24h timer, and the alert continues to measure only the CSI drivers. The gate
above is about the drivers alone.

The ADR's R1-R4 triggers say what has to become true before the gate lifts,
and record a second, opposite hazard: the driver image runs from an unpinned
`latest` tag, and upstream's announced next version drops REST support and
requires 26.x. That one breaks provisioning with no commit in this repo.

## Self-hosted CI runners (ARC) — dormant

All CI runs exclusively on GitHub-hosted runners (`ubuntu-latest`). The ARC
stack (controller + per-tenant runner scale sets) is retained in-repo but
**disabled by default**: the service entries are commented out in
`tenants/platform/tenant.yaml` (controller) and
`tenants/{jdwlabs,dotablaze-tech}/tenant.yaml` (runner sets). Values,
postInstall manifests, runner namespaces, ARC RBAC, and the
`kv/<tenant>-github-app` Vault secrets are all kept intact so nothing needs
rebuilding to come back.

**Steady state while dormant:**

- `arc-systems`, `jdwlabs-runners`, `dotablaze-tech-runners` namespaces exist
  but run zero pods
- No workflow may target `ubuntu-jdwlabs` / `ubuntu-dotablaze-tech` on an
  automatic trigger — such jobs would queue forever (the deployments-repo E2E
  workflow is manual-dispatch only for this reason)

**Re-enable procedure:**

1. Check free space on `/var` for every worker node a runner could land on
   (`talosctl -n <node> df`). Runner `_work` comes from `local-path`, which
   provisions a plain hostPath directory and enforces no capacity — the `4Gi`
   request in the runner-set values is a scheduling hint, so a build is
   bounded only by node disk, on the same partition as kubelet and
   containerd. A runaway job fills the node, not just its own volume.
   `NodeFilesystemAlmostOutOfSpace` does cover this
   (`nodeExporterAlerting: true` in `kube-prometheus-stack/values.yaml`), but
   it fires at 5% / 3% free — late enough that a fast CI fill reaches
   DiskPressure and evicts co-tenants first, so this pre-flight is not
   redundant with the alert.
2. Uncomment the `arc-systems` service in `tenants/platform/tenant.yaml`
3. Uncomment the `arc-runner-set-<tenant>` service(s) in the tenant file(s)
4. Verify `kv/<tenant>-github-app` still resolves:
   `platformctl tenants verify-secrets`
5. Merge; ArgoCD deploys controller (wave 3) then runner sets (wave 5)
6. Smoke-test with the apps repo `ARC Test kubernetes Workflow`
   (workflow_dispatch, input `arc_name`), and confirm runners appear in the
   GitHub org under Settings > Actions > Runners

**Post-incident: orphaned `local-path` PVs after node loss**

Run this after any incident that took a worker node `NotReady`, replaced it, or
removed it from the cluster — node loss is exactly the scenario runner `_work`
was moved onto `local-path` to survive, and it is also the one case
`local-path` cannot clean up after itself.

`local-path` reclaims a `Delete` volume by scheduling a helper pod onto the
node that holds the directory. The helper tolerates every taint, so a cordon or
a drain is fine, but an unreachable or deleted node leaves nothing to schedule
onto: the PV sits `Released` indefinitely and the checked-out source stays on
that node's disk.

1. List volumes the provisioner could not reclaim:

   ```bash
   kubectl get pv | grep Released
   ```

   Cross-check `STORAGECLASS` — `Released` on `longhorn`/`longhorn-single` is
   the expected steady state for `Retain` classes and is handled separately
   (see "Detached Longhorn volumes accumulate…" above). Only `local-path` rows
   are orphans.

2. For each, read `spec.nodeAffinity` to find the node that holds the data:

   ```bash
   kubectl get pv <name> -o jsonpath='{.spec.nodeAffinity}{"\n"}'
   ```

3. If the node is back and healthy, delete the PV and let the provisioner run
   its helper pod. If the node is gone for good, delete the PV directly — the
   directory went with the node:

   ```bash
   kubectl delete pv <name>
   ```

4. If the node came back with its disk intact, the directories the provisioner
   never got to reclaim are still there. Confirm and clear them on the node:

   ```bash
   talosctl -n <node> ls /var/local-path-provisioner
   ```

   Anything under that path with no matching PV is dead CI scratch. Reclaiming
   it matters beyond tidiness: it is what the re-enable pre-flight in step 1
   above measures free space against.
