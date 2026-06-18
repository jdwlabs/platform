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

### One-time setup (manual, before this lands)

1. Create a dedicated **Grafana GitHub App** — repo access limited to
   `jdwlabs/platform`; permissions **Contents: read/write** and **Pull requests:
   read/write**. Install it on the repo; record the **App ID** and
   **Installation ID**; generate a **private key** (PEM).
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

3. In Grafana: **Administration > General > Provisioning** → add a GitHub
   repository connection using the App ID / Installation ID / private key, point
   it at this repo + target branch, and choose the PR workflow.

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
