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

- `postInstall/gitsync-resources.yaml` holds both resource definitions as the
  reviewable source of truth. The private key is deliberately absent.
- `postInstall/gitsync-apply-job.yaml` runs on every ArgoCD sync, injects the key
  from the `grafana-gitsync-github-app` secret, and creates whichever resource is
  missing.

The Job **creates but never updates**. An existing connection is left untouched,
because the stored private key is write-once and re-applying it would disturb a
live sync path. The consequence is that editing `gitsync-resources.yaml` does not
propagate to a running Grafana — to change a resource, delete it first, then let
the next sync recreate it.

#### Changing a resource definition (manual, attended)

Merging an edit to `gitsync-resources.yaml` on its own changes nothing: the next
Job run finds both resources present and skips them. ArgoCD cannot help here —
these live in Grafana's API server, so `prune`/`selfHeal` on `platform-grafana`
neither manages nor removes them. Delete them, then let the sync recreate them:

```sh
platformctl gitsync recreate --dry-run   # prints the delete order, mutates nothing
platformctl gitsync recreate --confirm
platformctl gitsync status               # after the sync: both healthy again?
```

`recreate` deletes the repository **before** the connection, because the
repository references it, and then asks ArgoCD to refresh `platform-grafana` so
the apply Job re-runs. To reset one resource on its own use
`platformctl gitsync delete --kind repository --name platform-dashboards --confirm`.

Both paths refuse while a dashboard is owned by `platform-dashboards`, because
the repository's remove-orphan-resources finalizer collects whatever it owns;
the refusal names the dashboards at risk. `--allow-owned-dashboards` proceeds
anyway and is only correct when losing them is intended. Dashboards annotated
`grafana.app/managedBy: classic-file-provisioning` belong to the ConfigMap
sidecar, are never owned by the repository, and are unaffected.

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
