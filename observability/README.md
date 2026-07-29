# observability/

Home for dashboards-as-code, managed by Grafana **Git Sync** (Grafana v13+).
See [docs/observability/DASHBOARDS-AND-MULTITENANCY.md](../docs/observability/DASHBOARDS-AND-MULTITENANCY.md)
for the full design, rationale, and migration path.

> Status: wired and syncing. `dashboards/platform/` is the live Git Sync
> repository path — the connection and repository resources are healthy and
> polling this directory. The dashboards committed here are still examples
> showing the target layout, and they do not replace the existing
> ConfigMap-sidecar dashboards; those migrate one at a time, deleting each
> ConfigMap in the same change that adds its dashboard here, so the two paths
> never own the same dashboard.

## Layout

```
observability/
├── dashboards/          # Git Sync repository path. One subdir = one Grafana folder = one tenant boundary.
│   ├── platform/        # "Platform" folder (platform tenant, admin-managed)
│   ├── jdwlabs/         # tenant folder, RBAC-scoped to the jdwlabs team
│   └── dotablaze-tech/  # tenant folder, RBAC-scoped to the dotablaze-tech team
└── jsonnet/             # optional Grafonnet/mixin sources, compiled to dashboards/ in CI
```

## Conventions

- **`uid`** inside each dashboard JSON is its durable identity — pin it, never
  let Grafana auto-assign on import.
- **Datasources are referenced via dashboard variables** (`${datasource}`,
  `${loki_ds}`), never hardcoded UIDs, so the same JSON works against a
  tenant-scoped datasource.
- **Folder = tenant.** Folder-level RBAC + a per-tenant Grafana team enforce
  who can view/edit. Derived from the tenant's `observability` block in
  `tenants/<tenant>/tenant.yaml`.
- **One owner per dashboard.** A dashboard is provisioned by Git Sync *or* by a
  ConfigMap sidecar, never both — see the migration rule in the design doc.

## Generating dashboards (optional)

```sh
cd observability/jsonnet
jb install                                   # jsonnet-bundler: grafonnet, kubernetes-mixin
jsonnet -m ../dashboards main.jsonnet        # emit JSON into dashboards/
```

Compiled JSON is committed so Git Sync and reviewers only ever see plain JSON
and there is no Jsonnet toolchain dependency at runtime.
