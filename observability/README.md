# observability/

Home for dashboards-as-code, managed by Grafana **Git Sync** (Grafana v13+).
See [docs/observability/DASHBOARDS-AND-MULTITENANCY.md](../docs/observability/DASHBOARDS-AND-MULTITENANCY.md)
for the full design, rationale, and migration path.

> Status: wired and syncing, and Git Sync now owns every platform dashboard.
> `dashboards/platform/` is the live Git Sync repository path — the connection
> and repository resources are healthy and polling this directory. The seven
> dashboards that used to ship as ConfigMaps in each service's `postInstall/`
> directory live here as plain JSON, each keeping the `uid` it already had, and
> their ConfigMap manifests are gone. No dashboard is owned by both paths.
>
> `slo-error-budget.json` here and `jdwlabs-services-red.json` under
> `jdwlabs/` remain illustrative examples, tagged `generated-example`. Their
> panel queries do not match any recording rule this cluster actually emits, so
> they render empty and are not yet real dashboards.
>
> The dashboard sidecar is still enabled in the Grafana values. It now has no
> ConfigMaps to pick up; removing it is a separate cleanup so that this change
> stays reversible.

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
