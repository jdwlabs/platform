# Grafana

Chart `grafana` from `https://grafana-community.github.io/helm-charts` (the
community-maintained chart; the legacy `grafana.github.io/helm-charts` repo
froze at chart 10.5.15 / app 12.3.1 on 2026-01-30). Pinned in
`tenants/platform/tenant.yaml`.

## Git Sync (dashboards-as-code)

Git Sync is generally available in Grafana v13. It lets editors save dashboard
changes as a commit and open a pull request against this repository without
leaving the Grafana UI.

### One-time setup (manual)

1. Stage the GitHub App credential in Vault under `kv/grafana-gitsync` with
   fields `app-id`, `installation-id`, and `private-key` (the PEM contents).
   Create the GitHub App with `Contents: read/write` and `Pull requests:
   read/write`, install it on the target dashboards repo, and record the
   installation ID. The `grafana-gitsync-github-app` ExternalSecret syncs these
   into the `monitoring` namespace.
2. In Grafana: **Administration > General > Provisioning**, add a GitHub
   repository connection using the App ID / Installation ID / private key from
   the mounted secret (`/etc/secrets/gitsync`). Point it at the dashboards repo
   and target branch.

### Workflow

`edit dashboard in UI -> Save (commit) -> open PR -> review/merge -> Git Sync
pulls the merged state back`. Provisioned dashboards become read-through: the
source of truth is the Git file.

## Coexistence with ConfigMap/sidecar dashboards (drift avoidance)

Existing dashboards are provisioned as ConfigMaps (label `grafana_dashboard:
"1"`) discovered by the `kiwigrid/k8s-sidecar` sidecar across services
(`kube-prometheus-stack`, `longhorn`, `argo-cd`, `loki`, `cnpg-operator`).
**These two mechanisms must not manage the same dashboard**, or they will fight
and overwrite each other on every reconcile.

Keep them disjoint:

- ConfigMap/sidecar continues to own the platform-component dashboards shipped
  with the repo (the existing `dashboard-*.yaml` files). Leave these as-is.
- Git Sync owns net-new, hand-edited dashboards going forward. Place them in a
  distinct Git folder so the provisioned-folder boundary is unambiguous.
- Do not import a Git-Sync-managed dashboard as a ConfigMap (or vice versa).
  Migration of an existing ConfigMap dashboard to Git Sync means deleting the
  ConfigMap in the same change that adds it to the Git Sync repo.
