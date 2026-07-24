# Vault Standalone → Raft HA Migration Runbook

Step-by-step, **human-executed** runbook for migrating Vault from standalone
file storage (1 replica) to Integrated Storage / raft HA (3 replicas).

> **The merge is the deployment.** ArgoCD auto-syncs `main` with
> `selfHeal: true`. The PR carrying the HA values must stay open until
> Phase 2 of this runbook is complete — merging it early starts a 3-replica
> raft rollout against a data directory that still holds file-backend data,
> and the cluster will not form.

Every `kubectl` / `vault` / `argocd` command below is executed by a human
operator, not an agent (see AGENTS.md binary contract — this runbook is the
documented exception path for a one-time migration).

---

## Why `vault operator migrate` (and not a snapshot restore)

`vault operator raft snapshot restore` requires an already-running raft
cluster as the source *and* destination — the file backend cannot produce a
raft snapshot. The only HashiCorp-supported path from `storage "file"` to
`storage "raft"` is an **offline** `vault operator migrate` run against the
data directory while no Vault server is running. That is what this runbook
does: migrate the data in place on the existing `data-platform-vault-0`
PVC, then bring up the 3-node cluster where pod 0 already owns the data and
pods 1/2 join empty and replicate.

---

## Preconditions (verify all before starting)

- [ ] Vault healthy and **unsealed**: `kubectl -n vault exec platform-vault-0 -- vault status`
      shows `Initialized true`, `Sealed false`, `Storage Type file`.
- [ ] Offline `vault-init.json` backup exists and is readable (root token +
      unseal key). Keys also live in the `vault-unseal-keys` Secret and at
      `secret/vault/vault-init`. **Do not start without the offline copy** —
      mid-migration the in-cluster copies are unreachable.
- [ ] The HA PR is approved and mergeable (CI green) but **not merged**.
- [ ] Stale PVCs confirmed dormant (see Phase 0).
- [ ] Maintenance window: expect **10–20 minutes** of Vault downtime.
      During the window ESO cannot sync new/changed secrets and cert-manager
      cannot issue new certificates; already-synced Secrets and issued certs
      keep working, so running workloads are unaffected.
- [ ] No node is in `MemoryPressure` and no memory alerts are firing
      (`kubectl top nodes`): the third replica must land on one of the
      tight 4 GB workers.

## Abort criteria

Stop and roll back (see Rollback) if any of these occur:

- The pre-migration backup cannot be taken or verified.
- `vault operator migrate` reports any error, or does not end with
  `Success! All of the keys have been migrated.`
- After merge, pods 1/2 stay `Pending` for more than 15 minutes
  (anti-affinity placement failure — see Placement notes).
- The raft cluster does not reach 3/3 voters within 30 minutes of unsealing.
- Unseal of any pod fails with the known-good key.

---

## Phase 0 — Pre-merge preparation (cluster mutations, no git changes)

### 0.1 Delete the stale `vault-1` / `vault-2` PVCs

These are leftovers from an earlier, failed HA attempt (~46 days old): the
Longhorn volumes are `detached` with robustness `unknown`, and the last
workload status for `platform-vault-1` is `Failed`. If they are left in
place, the new StatefulSet pods 1/2 will bind them and boot against stale
data. Their PVs are `Retain`, so PVC deletion alone does not remove the
volumes.

```bash
# Confirm they are still detached (STATE=detached) before deleting:
kubectl get volumes.longhorn.io -n longhorn-system \
  pvc-df6c5715-e351-4553-8e44-1a87a9519e3c \
  pvc-a0152d45-b0d2-4d14-b19a-7c5fe91e5ae5

kubectl -n vault delete pvc data-platform-vault-1 data-platform-vault-2

# Reclaim policy is Retain: delete the Released PVs, then the Longhorn volumes.
kubectl delete pv pvc-df6c5715-e351-4553-8e44-1a87a9519e3c \
                  pvc-a0152d45-b0d2-4d14-b19a-7c5fe91e5ae5
kubectl delete volumes.longhorn.io -n longhorn-system \
  pvc-df6c5715-e351-4553-8e44-1a87a9519e3c \
  pvc-a0152d45-b0d2-4d14-b19a-7c5fe91e5ae5 --ignore-not-found

# Verify only data-platform-vault-0 remains:
kubectl get pvc -n vault
```

### 0.2 Disable ArgoCD auto-sync for the vault Application

`selfHeal: true` would otherwise revert the manual scale-down within
minutes.

```bash
argocd app set platform-vault --sync-policy none
```

### 0.3 Suspend the auto-unseal CronJob

The server is about to go down; the cron would fail every 2 minutes and
pin `kube_job_failed`. (Sticks because auto-sync is now off.)

```bash
kubectl -n vault patch cronjob vault-auto-unseal -p '{"spec":{"suspend":true}}'
```

### 0.4 Back up the current data

Two independent backups; verify both before proceeding.

```bash
# a) Longhorn snapshot of the live volume (crash-consistent, instant
#    in-cluster rollback point). Longhorn UI -> volume
#    pvc-ae4ffd70-7f77-4e1d-8ae1-2d5dfd65e094 -> Take Snapshot,
#    name it pre-raft-migration.

# b) Off-cluster tar of /vault/data (portable copy, survives cluster loss).
#    Store it next to the offline vault-init.json.
kubectl -n vault exec platform-vault-0 -- tar czf - -C /vault data \
  > vault-file-backend-$(date +%Y%m%d-%H%M%S).tgz

# Verify the tar is non-trivial and lists the file-backend tree (core/, sys/, logical/):
tar tzf vault-file-backend-*.tgz | head
```

### 0.5 Stop Vault

```bash
kubectl -n vault scale statefulset platform-vault --replicas=0
kubectl -n vault get pods -w   # wait for platform-vault-0 to terminate
```

Downtime clock starts here.

---

## Phase 1 — Offline data migration (`vault operator migrate`)

### 1.1 Start a temporary migration pod on the existing PVC

Same image and security context as the chart-managed pod, so file
ownership stays correct.

```bash
kubectl -n vault apply -f - <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: vault-migrate
  namespace: vault
spec:
  restartPolicy: Never
  securityContext:
    runAsNonRoot: true
    runAsUser: 100
    runAsGroup: 1000
    fsGroup: 1000
  containers:
    - name: migrate
      image: hashicorp/vault:1.20.1
      command: ["sleep", "3600"]
      volumeMounts:
        - name: data
          mountPath: /vault/data
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: data-platform-vault-0
EOF
kubectl -n vault wait pod/vault-migrate --for=condition=Ready --timeout=120s
```

### 1.2 Run the migration

`node_id` **must** be `platform-vault-0`: the chart sets
`VAULT_RAFT_NODE_ID` to the pod name (`setNodeId: true`), and the migrated
raft state must belong to the same member id pod 0 will boot with.
The raft backend writes `vault.db` and `raft/` at the path root; the file
backend keeps its data in subdirectories (`core/`, `sys/`, `logical/`), so
source and destination can share `/vault/data` without collision.

```bash
kubectl -n vault exec -it vault-migrate -- sh -c 'cat > /tmp/migrate.hcl <<HCL
storage_source "file" {
  path = "/vault/data"
}

storage_destination "raft" {
  path    = "/vault/data"
  node_id = "platform-vault-0"
}

# https on purpose: the raft cluster port always runs Vault's own TLS
# (independent of listener tls_disable), and the chart sets
# VAULT_CLUSTER_ADDR to https://<pod>.platform-vault-internal:8201.
cluster_addr = "https://platform-vault-0.platform-vault-internal:8201"
HCL
vault operator migrate -config=/tmp/migrate.hcl'
```

Expected final line: `Success! All of the keys have been migrated.`
**Anything else → abort** (see Rollback; the file-backend data is read-only
during migration and remains intact).

### 1.3 Verify and tear down the migration pod

```bash
kubectl -n vault exec vault-migrate -- ls -la /vault/data /vault/data/raft
# Expect: vault.db and raft/raft.db present; core/ sys/ logical/ still present (source, kept for rollback).
kubectl -n vault delete pod vault-migrate
```

---

## Phase 2 — Merge (this is the deployment)

1. Merge the HA PR into `main`.
2. Re-enable auto-sync and let ArgoCD roll it out:

```bash
argocd app set platform-vault --sync-policy automated --auto-prune --self-heal
argocd app sync platform-vault
```

Expected rollout (StatefulSet uses `podManagementPolicy: Parallel`, so all
three pods start at once):

- `platform-vault-0` binds the existing (migrated) PVC.
- `platform-vault-1` / `-2` provision fresh Longhorn PVCs and start empty.
- The raw `platform-vault` PDB is pruned; the chart-native PDB
  (`maxUnavailable: 1`) is created.
- The updated 3-target auto-unseal CronJob is applied (still suspended).
- All pods run but stay **sealed** (`Running 0/1` is expected — readiness
  gates on unseal).

---

## Phase 3 — Unseal and verify

Unseal **pod 0 first** — it holds the data and must become leader before
the empty pods can complete their `retry_join`.

```bash
UNSEAL_KEY=$(kubectl -n vault get secret vault-unseal-keys \
  -o jsonpath='{.data.unseal_key_1}' | base64 -d)

kubectl -n vault exec platform-vault-0 -- vault operator unseal "$UNSEAL_KEY"
kubectl -n vault exec platform-vault-0 -- vault status
# Expect: Sealed false, Storage Type raft, HA Enabled true, HA Mode active
```

Then pods 1 and 2 (with a Shamir seal, a joiner attaches to the leader
while sealed and completes the join when unsealed with the **cluster's**
key — the same key works for all members):

```bash
kubectl -n vault exec platform-vault-1 -- vault operator unseal "$UNSEAL_KEY"
kubectl -n vault exec platform-vault-2 -- vault operator unseal "$UNSEAL_KEY"
```

### Verification checklist

```bash
# 3/3 raft voters (root token from vault-init.json / secret/vault/vault-init):
kubectl -n vault exec platform-vault-0 -- sh -c \
  'VAULT_TOKEN=<root-token> vault operator raft list-peers'
# Expect exactly: platform-vault-0 (leader), platform-vault-1, platform-vault-2 — all voters.

# All pods ready:
kubectl -n vault get pods -l component=server   # 3x 1/1 Running

# Data survived: a known secret resolves.
# ESO healthy:
kubectl get clustersecretstore vault          # READY True
kubectl get externalsecrets -A | grep -v SecretSynced

# Metrics: Prometheus vault targets UP (telemetry stanza preserved).

# Chart PDB in place, raw PDB gone:
kubectl -n vault get pdb
```

### Resume the auto-unseal CronJob

Only after 3/3 voters are confirmed:

```bash
kubectl -n vault patch cronjob vault-auto-unseal -p '{"spec":{"suspend":false}}'
# Next run should be a clean success (all three targets already unsealed).
```

### Post-migration cleanup (optional, after ≥24h healthy)

Take a first raft snapshot, then remove the now-dead file-backend
directories from pod 0's volume:

```bash
kubectl -n vault exec platform-vault-0 -- sh -c \
  'VAULT_TOKEN=<root-token> vault operator raft snapshot save /tmp/post-migration.snap'
kubectl -n vault cp platform-vault-0:/tmp/post-migration.snap ./post-migration.snap

kubectl -n vault exec platform-vault-0 -- rm -rf \
  /vault/data/core /vault/data/sys /vault/data/logical /vault/data/auth
```

Do **not** do this before the 24h soak — the file tree is the fast local
rollback path. Follow-ups after soak: schedule periodic
`raft snapshot save` backups; consider pointing the ClusterSecretStore at
the `platform-vault-active` Service so ESO never lands on a sealed pod
during future rolling restarts.

---

## Rollback

### Before the merge (Phases 0–1 failed)

The migration is additive — the file-backend tree is never modified. Do not
merge the PR.

```bash
kubectl -n vault delete pod vault-migrate --ignore-not-found
# Remove raft artifacts if the migrate run partially wrote them:
#   kubectl exec into a fresh vault-migrate pod and: rm -rf /vault/data/vault.db /vault/data/raft
kubectl -n vault scale statefulset platform-vault --replicas=1
argocd app set platform-vault --sync-policy automated --auto-prune --self-heal
kubectl -n vault patch cronjob vault-auto-unseal -p '{"spec":{"suspend":false}}'
# Cron unseals pod 0 within 2 minutes; verify vault status + ESO.
```

### After the merge (raft cluster unhealthy / data wrong)

1. **Revert the PR** on `main` (`git revert` via a new PR — main is
   PR-only). ArgoCD syncs back to standalone file config, 1 replica.
2. Delete the raft pods' fresh PVCs so a future retry starts clean
   (`data-platform-vault-1/2`, plus their PVs / Longhorn volumes if the
   reclaim policy is again Retain).
3. Pod 0 boots with `storage "file"` against the untouched `core/ sys/
   logical/` tree. Unseal (cron or manual) and verify a known secret.
4. If the file tree was damaged: restore the Longhorn snapshot
   `pre-raft-migration` onto the volume (Longhorn UI → revert requires the
   volume detached, i.e. scale to 0 first), or recreate the PVC from the
   off-cluster tar.
5. Confirm ESO / cert-manager recover, then post-mortem before retrying.
