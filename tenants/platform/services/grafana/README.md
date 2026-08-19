# Grafana

Chart `grafana` from `https://grafana-community.github.io/helm-charts` (the
community-maintained chart; the legacy `grafana.github.io/helm-charts` repo
froze at chart 10.5.15 / app 12.3.1 on 2026-01-30). Pinned in
`tenants/platform/tenant.yaml`.

## Dashboards (current model)

Dashboards are provisioned as ConfigMaps (label `grafana_dashboard: "1"`)
discovered by the `kiwigrid/k8s-sidecar` sidecar across services
(`kube-prometheus-stack`, `longhorn`, `argo-cd`, `loki`, `cnpg-operator`).

## Datasources

Datasources (Prometheus, Loki, Alertmanager, Tempo) are provisioned inline in
`values.yaml` under `datasources`. The Tempo datasource carries trace-to-logs /
trace-to-metrics correlation to the Loki and Prometheus UIDs.

## Git Sync (dashboards-as-code)

Git Sync is GA in Grafana v13: editors save a dashboard change as a commit and
open a pull request against this repo without leaving the UI. Design + rollout
plan: `docs/observability/DASHBOARDS-AND-MULTITENANCY.md`.

### Credential setup (human, one-time — already done)

1. Create a dedicated **Grafana GitHub App** — repo access limited to
   `jdwlabs/platform`; permissions **Contents: read/write** and **Pull requests:
   read/write**. Install it on the repo; record the **App ID** and
   **Installation ID**; generate a **private key** (PEM).

   **Webhooks: read/write is not needed.** Grafana derives a connection's
   required permissions from the workflows of the repositories bound to it, not
   from the connection alone. The `write` workflow pushes directly and pulls in
   a webhooks requirement; `branch` does not. While the repository offered
   `write`, the connection failed with:

   ```text
   GitHub App lacks required 'webhooks' permission: requires 'write', has ''
   ```

   which reads like a connection-level demand and is not one. Recreating the
   repository with `workflows: ["branch"]` cleared it against an App still
   holding only `contents:write`, `metadata:read` and `pull_requests:write` —
   the grant was never changed. `spec.webhook.disabled: true` remains set on
   both resources so no inbound hook is registered, which is what this cluster
   needs regardless.
2. Seed the credential into Vault (mirrors the ARC `<tenant>-github-app` flow):

   ```sh
   export PLATFORMCTL_GRAFANA_GITSYNC_APP_ID=<app-id>
   export PLATFORMCTL_GRAFANA_GITSYNC_INSTALLATION_ID=<installation-id>
   export PLATFORMCTL_GRAFANA_GITSYNC_PRIVATE_KEY="$(cat app.private-key.pem)"
   platformctl bootstrap seed grafana-gitsync
   ```

   This writes `kv/grafana-gitsync` (fields `app-id`, `installation-id`,
   `private-key`); the `grafana-gitsync-github-app` ExternalSecret syncs it into
   the `monitoring` namespace and Grafana mounts it at `/etc/secrets/gitsync`.

   > Seed Vault **before** this merges — `extraSecretMounts` makes the Grafana
   > pod depend on the `grafana-gitsync-github-app` secret existing.

### Connecting the repository (automated)

The connection itself is **not** a manual step. Grafana's provisioning resources
(`Connection`, `Repository`) live in Grafana's own API server — they look like
Kubernetes objects but ArgoCD cannot see or reconcile them, so a connection made
by hand in the UI disappears the moment Grafana is rebuilt, with no drift signal
anywhere.

Instead:

- `postInstall/gitsync-resources.yaml` holds every resource definition as the
  reviewable source of truth. The private key is deliberately absent.
- `postInstall/gitsync-apply-job.yaml` runs on every ArgoCD sync, injects the key
  from the `grafana-gitsync-github-app` secret, and creates whichever resource is
  missing.

There is one `Connection` and one `Repository` per synced path —
`platform-dashboards`, `jdwlabs-dashboards`, `dotablaze-tech-dashboards` — all
bound to the same connection, because `spec.connection.name` is a many-to-one
reference and a repository syncs exactly one path. Adding a folder is a new
repository definition plus a line in the Job's `REPOSITORIES` list; the name in
the two must match, since the Job probes by name before it POSTs.

With `sync.target: folder` a repository's `metadata.name` becomes the created
folder's UID, and a repository cannot adopt a folder that already exists
outside provisioning. The tenant repositories therefore carry a
`-dashboards` suffix — the bare tenant name collides with the folder
tenant-envelope creates through the classic folders API, and that collision
stops the sync outright. The folder that appears here is granted to the tenant
team by tenant-envelope's `PostSync` hook, off
`observability.grafana.gitSyncFolder` in the tenant's `tenant.yaml` — a synced
tenant folder left at Grafana's inherited permissions is readable by every user
in the instance. That grant is also why `grafana.ini`'s `feature_toggles` pins
`provisioningFolderMetadata`: Grafana refuses a permissions write on a
provisioned folder without it. Adding a folder means the repository definition,
the `TENANT_REPOSITORIES` line **and** the tenant's key — `python3
tools/check-gitsync-tenant-folders.py` fails CI on any subset. See
`observability/README.md` for the full consequence.

Only `platform-dashboards` is gated here, for creation and health alike. This
Job is an ArgoCD `Sync` hook, so anything it fails on fails the whole
`platform-grafana` sync: a tenant repository that cannot be created or cannot
become healthy is reported as a warning and left to `platformctl gitsync
status`, which still exits non-zero on it, rather than taking Grafana's own
dashboards down with it. Creation is not exempt from that, because the
realistic creation failure is not a malformed definition — CI parses those —
but an unmanaged-folder collision, which is live state and is rejected at
admission with a 422.

A repository that failed to be created is skipped by the health wait rather
than proved unhealthy over 90 seconds it cannot pass.

The Job **creates but never updates**. An existing connection is left untouched,
because the stored private key is write-once and re-applying it would disturb a
live sync path. The consequence is that editing `gitsync-resources.yaml` does not
propagate to a running Grafana — to change a resource, delete it first, then let
the next sync recreate it.

#### Changing a resource definition (manual, attended)

Merging an edit to `gitsync-resources.yaml` on its own changes nothing: the next
Job run finds the resources present and skips them. ArgoCD cannot help here —
these live in Grafana's API server, so `prune`/`selfHeal` on `platform-grafana`
neither manages nor removes them. Delete them, then let the sync recreate them:

```sh
platformctl gitsync recreate --repository <name> --dry-run   # prints the delete order, mutates nothing
platformctl gitsync recreate --repository <name> --confirm
platformctl gitsync status                                   # after the sync: everything healthy again?
```

`recreate` deletes the repository **before** the connection, because the
repository references it, and then asks ArgoCD to refresh `platform-grafana` so
the apply Job re-runs. `--repository` is required now that more than one
exists. The connection is deleted only when no other repository still binds to
it, so recreating one folder leaves the other two syncing and the plan is a
single delete; the retained connection is reported rather than left to be
inferred from the short plan. To reset one resource on its own use
`platformctl gitsync delete --kind repository --name platform-dashboards --confirm`.

Changing the **connection** itself — rotating the GitHub App key, editing
`connection.json` — is the one thing that shared connection blocks, and
`--with-connection` is the way through it:

```sh
platformctl gitsync recreate --repository platform-dashboards --with-connection --dry-run
platformctl gitsync recreate --repository platform-dashboards --with-connection --confirm
```

It widens the plan to every repository bound to that connection and then the
connection last, because a connection cannot be deleted underneath a repository
that still references it. All of them come back from the same apply Job run, so
the interruption is one sync interval, not one per folder. `gitsync delete
--kind connection` stays refused while any repository references it and now
names them all in its refusal.

Both paths refuse while a dashboard is owned by the repository being deleted,
because its remove-orphan-resources finalizer collects whatever it owns; the
refusal names the dashboards at risk. `--allow-owned-dashboards` proceeds
anyway and is only correct when losing them is intended. Dashboards annotated
`grafana.app/managedBy: classic-file-provisioning` belong to the ConfigMap
sidecar, are never owned by a repository, and are unaffected.

**Adding** a folder is not this procedure. A repository that does not exist yet
is created by the ordinary apply Job on the next sync, so a new definition
needs no delete and no `recreate` — merge it and let ArgoCD sync.

#### Inspecting current state

```sh
platformctl gitsync status         # health + sync state per resource
platformctl gitsync status --full  # plus the whole health message
```

`0 resources` means Git Sync is credentialed but not connected. Present is not
the same as working, so the command reads `status.health.healthy` and
`status.sync.state` rather than existence, and exits non-zero when anything is
unhealthy — a resource that fails every sync still lists fine. That is also what
the Job's final gate asserts, so a red `platform-grafana` sync with
`never became healthy` in the hook log is Git Sync reporting itself broken
rather than the deploy being broken.

### Workflow

`edit in UI -> Save (commit) -> App opens PR -> human review/merge -> Git Sync
pulls merged state back`. PRs are authored by the App (a bot identity), so they
can be approved under the repo's required-review ruleset.

## Coexistence with ConfigMap/sidecar dashboards (drift avoidance)

The two mechanisms must not manage the same dashboard, or they fight on every
reconcile. ConfigMap/sidecar keeps the existing platform-component dashboards;
Git Sync owns net-new, hand-edited dashboards in a distinct folder. Migrating an
existing ConfigMap dashboard means deleting the ConfigMap in the same change
that adds it to Git Sync.
