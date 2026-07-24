# Vault Standalone → Raft HA Cutover Runbook

Step-by-step, **human-executed** runbook for cutting Vault over from
standalone file storage (1 replica, default Longhorn storage) to a 3-node
raft HA cluster on `longhorn-single` storage.

> **The merge is the deployment.** ArgoCD auto-syncs `main` with
> `selfHeal: true`. The PR carrying the HA values must stay open until the
> teardown phase below is complete. The first HA attempt (2026-06-08
> revert) failed exactly this way: the change merged outside its cutover
> window, pods 1/2 rolled to raft uninitialized while pod 0 kept the live
> file-storage data, and the rollout had to be reverted.

**Method: re-init + re-seed, not data migration.** Vault is re-initialized
empty on raft (`platformctl bootstrap phase 3`) and every kv path is
re-written from the codified, idempotent seed specs
(`platformctl bootstrap phase 4` / `bootstrap seed`). `vault operator
migrate` was considered and rejected as fragile; the seed specs are the
source of truth for everything Vault holds. **Consequence: re-init wipes
the current Vault contents permanently — the pre-flight below is
non-negotiable.**

Every `kubectl` / `vault` / `argocd` / `platformctl` command below is
executed by a human operator, not an agent.

---

## Pre-flight (non-negotiable — abort here costs nothing)

### A. Every seed value on hand

Phase 4 sources values from `PLATFORMCTL_*` env vars or interactive
prompts. Confirm you can produce **all** of them before touching the
cluster:

| Spec key | Fields |
|---|---|
| `porkbun` | api-key, secret-key |
| `grafana` | admin-user, admin-password |
| `longhorn` | htpasswd_string |
| `alertmanager` | discord_webhook_url |
| `usersrole` | jwt_key_non, jwt_key_prd |
| `argocd-dex` | admin-password-hash, headlamp-client-secret, github-client-id, github-client-secret |
| `grafana-gitsync` | app-id, installation-id, private-key |
| `truenas-csi` | api_key |
| `litellm` | master_key, anthropic/openrouter/nvidia api keys (optional-merge) |
| `holmes` | litellm_key, discord_webhook_url, jira url/email/token, github_token, talosconfig |
| per tenant | `<tenant>-github-app`, `<tenant>-ai-keys`, `<tenant>-discord-bot-token` |

If the argocd-dex Google SSO change (adds google-client-id /
google-client-secret to the `argocd-dex` spec) has merged by cutover time,
rebuild `platformctl` from current `main` and have those two values ready
as well.

### B. No un-seeded data in Vault

The seed specs are the recovery path; anything outside them is lost.
Diff live kv paths against the table above:

```bash
kubectl -n vault exec platform-vault-0 -- sh -c \
  'VAULT_TOKEN=<root-token> vault kv list kv/'
```

Every listed path must map to a seed spec key. If anything extra appears,
export it manually and decide: add a seed spec first, or accept the loss
in writing. **Abort until resolved.**

### C. Offline copies of the CURRENT credentials

Phase 3 **overwrites** `vault-unseal-keys` (vault ns) and `vault-token`
(external-secrets ns) with new values. Save the current unseal key, root
token, and `vault-init.json` offline first — they are required for
rollback into the preserved old data.

```bash
kubectl -n vault get secret vault-unseal-keys -o jsonpath='{.data.unseal_key_1}' | base64 -d
# Root token: offline vault-init.json / secret/vault/vault-init.
```

### D. Backstop backup (operator's call was "no snapshot required" — keep one anyway, it is nearly free)

```bash
# a) Longhorn snapshot of the live volume: Longhorn UI ->
#    pvc-ae4ffd70-7f77-4e1d-8ae1-2d5dfd65e094 -> Take Snapshot -> "pre-raft-cutover".
# b) Off-cluster tar of the file backend, stored next to vault-init.json:
kubectl -n vault exec platform-vault-0 -- tar czf - -C /vault data \
  > vault-file-backend-$(date +%Y%m%d-%H%M%S).tgz
tar tzf vault-file-backend-*.tgz | head   # sanity: core/, sys/, logical/
```

### E. Cluster state

- [ ] Vault healthy/unsealed: `kubectl -n vault exec platform-vault-0 -- vault status`
- [ ] `longhorn-single` StorageClass exists (`kubectl get sc longhorn-single`)
      — it is already on `main` (kept intentionally when the first attempt
      was reverted).
- [ ] HA PR approved, CI green, **not merged**.
- [ ] No node in `MemoryPressure`, no memory alerts (`kubectl top nodes`) —
      the third replica must land on one of the tight 4 GB workers.
- [ ] Maintenance window agreed: from teardown to end of re-seed, ESO
      cannot sync secret changes and cert-manager cannot issue new certs.
      Already-created Secrets and certs keep working, so running workloads
      are unaffected; budget ~30–45 minutes.

## Abort criteria

Stop (and roll back if past teardown) if any of these occur:

- Any pre-flight item cannot be satisfied — especially a kv path with no
  seed spec, or a seed value nobody can produce.
- After merge, any vault pod stays `Pending` >15 min (anti-affinity
  placement failure on the tight workers).
- `platformctl bootstrap phase 3` fails or `vault-init.json` is not
  produced.
- `raft list-peers` does not reach 3/3 voters within 30 min of unseal.
- Phase 4 seeding fails for a non-optional field.
- `ClusterSecretStore vault` not Ready or ExternalSecrets still failing
  30 min after re-seed.

---

## Phase 1 — Teardown (cluster mutations, no git changes)

The StatefulSet must be **deleted**, not patched: `volumeClaimTemplates`
(the storage class change) is immutable, so an in-place ArgoCD sync of the
new manifest would fail. All three PVCs go too — pod 0's data is
superseded by re-seed, and pods 1/2 hold stale data from the failed
2026-06 attempt (Longhorn volumes `detached`, robustness `unknown`, last
workload `Failed`).

```bash
# 1. Stop ArgoCD fighting the teardown (selfHeal would recreate everything):
argocd app set platform-vault --sync-policy none

# 2. Suspend the unseal cron (sticks now that auto-sync is off):
kubectl -n vault patch cronjob vault-auto-unseal -p '{"spec":{"suspend":true}}'

# 3. Delete the StatefulSet (downtime clock starts here):
kubectl -n vault delete statefulset platform-vault
kubectl -n vault get pods -w    # wait for platform-vault-0 to terminate

# 4. Delete all three PVCs. PVs are Retain, so delete the PV objects too:
kubectl -n vault delete pvc data-platform-vault-0 data-platform-vault-1 data-platform-vault-2
kubectl delete pv pvc-ae4ffd70-7f77-4e1d-8ae1-2d5dfd65e094 \
                  pvc-df6c5715-e351-4553-8e44-1a87a9519e3c \
                  pvc-a0152d45-b0d2-4d14-b19a-7c5fe91e5ae5

# 5. Longhorn volumes: DELETE the stale vault-1/2 volumes; KEEP pod 0's
#    volume (pvc-ae4ffd70-...) detached in Longhorn — it holds the old data
#    and its pre-raft-cutover snapshot; it is the rollback target. Delete it
#    only after the post-cutover soak.
kubectl delete volumes.longhorn.io -n longhorn-system \
  pvc-df6c5715-e351-4553-8e44-1a87a9519e3c \
  pvc-a0152d45-b0d2-4d14-b19a-7c5fe91e5ae5 --ignore-not-found

kubectl get pvc -n vault    # expect: none
```

---

## Phase 2 — Merge (this is the deployment)

1. Merge the HA PR into `main`.
2. Re-enable auto-sync and sync:

```bash
argocd app set platform-vault --sync-policy automated --auto-prune --self-heal
argocd app sync platform-vault
```

Expected rollout (`podManagementPolicy: Parallel` — all three pods at
once):

- Fresh StatefulSet, 3 replicas; three new PVCs on **longhorn-single**
  (verify: `kubectl get pvc -n vault -o wide` shows `longhorn-single`,
  and each Longhorn volume has 1 replica).
- Hard anti-affinity → three distinct nodes.
- Raw `platform-vault` PDB pruned; chart-native PDB (`maxUnavailable: 1`)
  created.
- Updated 3-target unseal CronJob applied (still suspended).
- All pods `Running 0/1` — sealed **and uninitialized**. That is correct.

---

## Phase 3 — Initialize (`platformctl bootstrap phase 3`)

```bash
platformctl bootstrap phase 3
```

This runs `vault operator init` (1 share / threshold 1), writes
`vault-init.json` to `.secrets/`, upserts the new `vault-unseal-keys`
(vault ns) and `vault-token` (external-secrets ns) Secrets, enables kv-v2,
and applies the ClusterSecretStore. It waits for a running vault pod and
inits whichever it reaches first — with symmetric `retry_join` any of the
three may become leader; the others join automatically.

Then resume the cron and let it unseal all three members (each container
targets one pod by DNS; the single Shamir key unseals every member):

```bash
kubectl -n vault patch cronjob vault-auto-unseal -p '{"spec":{"suspend":false}}'
# Within ~2 minutes:
kubectl -n vault get pods -l component=server   # 3x 1/1 Running
```

Verify the raft cluster before seeding:

```bash
kubectl -n vault exec platform-vault-0 -- sh -c \
  'VAULT_TOKEN=<new-root-token> vault operator raft list-peers'
# Expect exactly 3 voters: platform-vault-0/1/2, one leader.
```

**Immediately store the new `vault-init.json` offline** (alongside the
old one — do not overwrite it until rollback is off the table).

---

## Phase 4 — Re-seed (`platformctl bootstrap phase 4`)

```bash
# Export every PLATFORMCTL_* value from pre-flight A (or answer prompts):
platformctl bootstrap phase 4
# Targeted re-runs, if a spec needs correcting:
#   platformctl bootstrap seed <spec-key>
```

Seeding is idempotent and merge-based; re-running a spec never wipes
fields owned by other services.

---

## Phase 5 — Verification

```bash
# ESO end-to-end:
kubectl get clustersecretstore vault                  # READY True
kubectl get externalsecrets -A | grep -v Synced       # expect no rows
platformctl tenants verify-secrets                    # 0 issues
platformctl bootstrap verify                          # phase gates green

# Force a fast re-sync if any ExternalSecret lags its refreshInterval:
#   kubectl annotate externalsecret <name> -n <ns> force-sync=$(date +%s)

# Quorum resilience (acceptance criterion — one pod loss keeps serving):
kubectl -n vault delete pod platform-vault-1
#   ClusterSecretStore stays Ready; pod rejoins and cron re-unseals it;
#   raft list-peers returns to 3/3.

# Guardrails and telemetry:
kubectl -n vault get pdb            # chart PDB, maxUnavailable 1; raw PDB gone
# Prometheus: vault targets UP (listener telemetry stanza preserved).
# UI: https://vault.jdwlabs.com reachable.
```

Spot-check consumers whose secrets were re-seeded with **new** values
only if you rotated anything during seeding; otherwise values are
identical and pods need no restart.

### Post-cutover soak and cleanup (≥24 h healthy)

```bash
# First raft snapshot (raft can snapshot; file storage never could):
kubectl -n vault exec platform-vault-0 -- sh -c \
  'VAULT_TOKEN=<root-token> vault operator raft snapshot save /tmp/post-cutover.snap'
kubectl -n vault cp platform-vault-0:/tmp/post-cutover.snap ./post-cutover.snap
```

After the soak: delete the preserved old Longhorn volume
(`pvc-ae4ffd70-...`) via the Longhorn UI, and retire the old
`vault-init.json` / unseal key. Follow-ups: schedule periodic
`raft snapshot save` backups; consider pointing the ClusterSecretStore at
`platform-vault-active` so ESO never lands on a sealed member during
rolling restarts.

---

## Rollback

### Before teardown

Nothing has changed. Do not merge; re-enable auto-sync if you disabled it:

```bash
argocd app set platform-vault --sync-policy automated --auto-prune --self-heal
```

### After teardown / after merge

1. **Revert the PR** via a new PR (`main` is PR-only). ArgoCD syncs back
   to the standalone file-storage config, 1 replica.
2. Delete the raft StatefulSet and its three `longhorn-single` PVCs/PVs
   (same immutability constraint applies in reverse).
3. Reattach the preserved old data: in the Longhorn UI, take volume
   `pvc-ae4ffd70-...` (revert to snapshot `pre-raft-cutover` if it was
   written to) and use **Create PV/PVC** to expose it as
   `data-platform-vault-0` in namespace `vault`. ArgoCD's standalone
   StatefulSet then binds it. If the volume is gone, restore from the
   off-cluster tar onto a fresh PVC.
4. Restore the **old** `vault-unseal-keys` and `vault-token` Secrets from
   the offline copies (phase 3 overwrote both):

```bash
kubectl -n vault create secret generic vault-unseal-keys \
  --from-literal=unseal_key_1=<old-key> --dry-run=client -o yaml | kubectl apply -f -
kubectl -n external-secrets create secret generic vault-token \
  --from-literal=token=<old-root-token> --dry-run=client -o yaml | kubectl apply -f -
```

5. Resume the cron (it unseals pod 0 within 2 minutes), verify
   `vault status` shows `Storage Type file` / unsealed, a known secret
   resolves, and `ClusterSecretStore vault` is Ready.
6. Post-mortem before any retry.
