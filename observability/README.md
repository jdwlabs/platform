# observability/

Home for dashboards-as-code, managed by Grafana **Git Sync** (Grafana v13+).
See [docs/observability/DASHBOARDS-AND-MULTITENANCY.md](../docs/observability/DASHBOARDS-AND-MULTITENANCY.md)
for the full design, rationale, and migration path.

> Status: wired and syncing. Git Sync is the sole owner of every dashboard in
> this cluster. The seven dashboards that used to ship as ConfigMaps in each
> service's `postInstall/` directory live in `dashboards/platform/` as plain
> JSON, each keeping the `uid` it already had, and their ConfigMap manifests are
> gone. Nothing is owned by both paths, and every one of the seven is backed by
> metrics this cluster actually scrapes.
>
> The dashboard sidecar is still enabled in the Grafana values. It now has no
> ConfigMaps to pick up; removing it is a separate cleanup so that this change
> stays reversible.

## Layout

```
observability/
├── dashboards/
│   ├── platform/         # the ONLY synced path -> Grafana folder "Platform dashboards"
│   ├── jdwlabs/           # scaffolded; folder+team+RBAC exist, Git Sync not wired yet
│   └── dotablaze-tech/    # scaffolded; folder+team+RBAC exist, Git Sync not wired yet
└── jsonnet/              # uncompiled illustrative skeleton, not wired to anything
```

One Git Sync `Repository` resource exists, `platform-dashboards`. It tracks
`main` at `observability/dashboards/platform`, polls every 60s, and syncs with
`target: folder`, so everything under that path lands in a single Grafana
folder. Webhooks are disabled — this cluster guarantees no inbound path from
GitHub — and the only offered workflow is `branch`, because `main` is a merge
target only.

A per-tenant folder is therefore not just a new subdirectory here: it needs its
own `Repository` resource pointing at its own path, plus the folder RBAC and
Grafana team to go with it. The jdwlabs and dotablaze-tech folders + teams +
RBAC now render from each tenant's `observability` block in `tenant.yaml` (see
`helm-charts/tenant-envelope/templates/observability.yaml`), so the RBAC half
of that pair exists — but no `Repository` resource points at either tenant
path yet, so `dashboards/jdwlabs/` and `dashboards/dotablaze-tech/` are still
inert from Git Sync's perspective until that follow-up wiring lands (tracked
under the JDWLABS-67 parent). The dashboard JSON committed for each tenant is
still real — the queries match this cluster's live series — it just isn't
reachable through Grafana's UI yet.

Resource definitions live in
`tenants/platform/services/grafana/postInstall/gitsync-resources.yaml`. They are
Grafana app-platform objects, not Kubernetes ones, so `kubectl` and ArgoCD
cannot see them. Editing that file alone changes nothing in the cluster — the
apply Job creates but never updates, so a definition change has to go through
`platformctl gitsync recreate --dry-run` and then `--confirm`.

## Conventions

- **`uid`** inside each dashboard JSON is its durable identity — pin it, never
  let Grafana auto-assign on import.
- **Datasources are referenced via dashboard variables** (`${datasource}`,
  `${loki_ds}`), never hardcoded UIDs, so the same JSON works against a
  tenant-scoped datasource.
- **Per-tenant Loki/Tempo datasources** now exist for jdwlabs and
  dotablaze-tech, named `<tenant>-loki` / `<tenant>-tempo` (rendered by the
  same `templates/observability.yaml` Job as the folder/team/RBAC, off each
  tenant's `observability.tenantId`). Each carries an `X-Scope-OrgID` header
  pinned to that tenant, so a tenant dashboard should point its
  `${loki_ds}`/`${datasource}` variable default at its own pair rather than
  the platform ones — see
  [docs/observability/DASHBOARDS-AND-MULTITENANCY.md §5.3/§5.4](../docs/observability/DASHBOARDS-AND-MULTITENANCY.md)
  for the isolation mechanism and its known gaps.
- **Folder = tenant** is the intended end state, enforced by folder-level RBAC
  and a per-tenant Grafana team derived from the tenant's `observability` block
  in `tenants/<tenant>/tenant.yaml`. The platform folder predates this model
  and stays as-is; jdwlabs and dotablaze-tech now render their folder + team +
  RBAC from that block (Git Sync wiring is the remaining piece — see above).
- **One owner per dashboard.** A dashboard is provisioned by Git Sync *or* by a
  ConfigMap sidecar, never both — see the migration rule in the design doc.
- **Every committed dashboard is a real dashboard.** A panel query that matches
  nothing this cluster emits renders an empty view that still looks
  authoritative, which is worse than having no dashboard at all. Check the
  queries against live Prometheus before committing.

## jsonnet/

`jsonnet/` is an illustrative skeleton and nothing compiles it. There is no
`jsonnetfile.lock.json`, no `vendor/`, and no CI job that reads it, so the
dependency versions it names have never been resolved. Its only output was the
per-tenant RED example dashboard, which was deleted because no tenant service
exports the request metrics it charts, which leaves it with no live consumer.

It is kept as a starting point for a future generated-dashboard pipeline. Treat
it as a design sketch, not as the export mechanism — dashboards reach Git Sync
today by committing plain JSON under `dashboards/platform/`.
