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
│   ├── platform/         # synced path -> Grafana folder "Platform dashboards"
│   ├── jdwlabs/           # synced path -> Grafana folder "Jdwlabs dashboards"
│   └── dotablaze-tech/    # synced path -> Grafana folder "Dotablaze Tech dashboards"
└── jsonnet/              # uncompiled illustrative skeleton, not wired to anything
```

Each top-level directory under `dashboards/` has its own Git Sync `Repository`
resource — `platform-dashboards`, `jdwlabs-dashboards`,
`dotablaze-tech-dashboards`. A repository syncs exactly one path
(`spec.github.path` is a single string), so one repository per folder is the
only available shape; there is no multi-path form. All three track `main`, poll
every 60s, and sync with `target: folder`, so everything under a path lands in
that repository's own Grafana folder. Webhooks are disabled — this cluster
guarantees no inbound path from GitHub — and the only offered workflow is
`branch`, because `main` is a merge target only.

All three share the single `Connection`, `jdwlabs-platform-github`:
`spec.connection.name` is a many-to-one reference, so one GitHub App serves
every path rather than each repository carrying its own credential. Keeping
every repository on `branch` is what makes adding one harmless to the others —
the connection's required permissions are derived from the union of its bound
repositories' workflows, so a set that does not change cannot change what the
App is asked for. Grafana allows several connections onto the same GitHub repo
as long as their paths are **siblings**; nested or overlapping paths are
rejected, which is why the tree is flat under `dashboards/`.

### Folder identity is the repository's name

With `target: folder`, the created folder's **UID is the repository's
`metadata.name`** and its title is `spec.title`. There is no field to aim a
repository at a folder that already exists, and a repository whose name
collides with a folder created outside provisioning cannot take that folder
over — it stops with an unmanaged-collision error and syncs nothing.

That is the reason the tenant repositories are named `<tenant>-dashboards`
rather than `<tenant>`: each tenant already has a folder whose UID is the bare
tenant name, created through the classic folders API by
`helm-charts/tenant-envelope/templates/observability.yaml`, and that folder is
exactly the kind Git Sync may not adopt. The bare name is the one choice
guaranteed to fail.

So a tenant holds **two** folders: `<tenant>`, which tenant-envelope creates,
and `<tenant>-dashboards`, which its Git Sync repository creates. Both are
permissioned the same way, from the same `observability.grafana` block. A
folder created by provisioning carries Grafana's inherited Admin/Editor/Viewer
grants — readable by every user in the instance, the other tenants' teams
included — until a full-replace permission write drops them, and that write is
what makes a folder tenant-private rather than merely tenant-named.

Which hook does it matters, so both templates live in
`helm-charts/tenant-envelope/templates/observability.yaml`:

| Hook | Job | Does |
| --- | --- | --- |
| `Sync` | `tenant-<t>-grafana-observability-apply` | Folder, team, folder-RBAC on `<tenant>`, Loki/Tempo datasources |
| `PostSync` | `tenant-<t>-grafana-gitsync-folder-rbac` | Waits for `<tenant>-dashboards`, grants the tenant team on it |

The split is deliberate. The git sync folder is created by the separate
`platform-grafana` Application, so waiting for it means waiting on something
this Application does not control — and fail-closed is the only correct posture
for a permission step, because "the folder is not there" and "the folder is
there and open" are the same observation without asking. Inside the `Sync` hook
that failure would take the tenant's namespaces, quotas, NetworkPolicies and
AppProject with it. `PostSync` fails the operation without withdrawing what the
`Sync` phase already applied, so the blast radius is this tenant's
observability rather than this tenant's governance.

Neither Job ever *creates* the git sync folder: the repository owns that UID,
and a folder Git Sync did not create is one it refuses to adopt.

### Adding a tenant folder

Three files, and all three or none — `tools/check-gitsync-tenant-folders.py`
fails CI on any subset, because each missing piece is silent in the cluster:

1. `tenants/<t>/tenant.yaml` — `observability.grafana.gitSyncFolder:
   <t>-dashboards`. Missing: the folder is created and stays readable by every
   Grafana user, with the apply Job exiting 0 and ArgoCD green.
2. `tenants/platform/services/grafana/postInstall/gitsync-resources.yaml` — a
   `Repository` whose `metadata.name` is that folder UID and whose
   `spec.github.path` is `observability/dashboards/<t>`.
3. The same service's `gitsync-apply-job.yaml` — one line in
   `TENANT_REPOSITORIES` pairing that name with its ConfigMap key. Missing: the
   tenant's PostSync hook waits five minutes for a folder nothing creates, then
   fails that tenant's sync every time.

The permission write itself depends on the `provisioningFolderMetadata` feature
toggle, which Grafana requires before it will accept a permissions write on any
provisioned folder. It is pinned in the grafana service's `values.yaml` rather
than left at its default for exactly that reason.

**Known gap.** Two folders is one more than the design calls for — the
dashboards do not land *inside* the tenant folder, they sit beside it. The
boundary holds either way (both folders carry the same team grant), but the
navigation is split. Unifying them needs a folder Git Sync owns from creation,
which means tenant-envelope no longer creating one and the existing unmanaged
folders being removed first. That is a deliberate follow-up, not part of the
wiring.

Resource definitions live in
`tenants/platform/services/grafana/postInstall/gitsync-resources.yaml`. They are
Grafana app-platform objects, not Kubernetes ones, so `kubectl` and ArgoCD
cannot see them. Editing that file alone changes nothing in the cluster — the
apply Job creates but never updates, so a definition change has to go through
`platformctl gitsync recreate --repository <name> --dry-run` and then
`--confirm`.

Create-if-absent is per resource, so *adding* a folder is not a definition
change: a new repository is absent, the next sync creates it, and no `recreate`
is involved. Only editing a repository that already exists needs the
delete-first dance — and with several repositories on one connection,
`recreate` takes `--repository` and leaves the shared connection alone.
Changing the **connection** definition (a rotated GitHub App key, an edited
`connection.json`) is the one case that cannot leave it alone: add
`--with-connection`, which widens the plan to every repository bound to it and
then the connection, since a connection cannot be deleted underneath a
repository that still references it.

Deleting a tenant repository is not a neutral act, because the folder is a
resource the repository owns: the remove-orphan-resources finalizer takes the
folder with it, and the one Grafana recreates comes back with the inherited
grants. `recreate` therefore re-syncs the claiming tenant's
`governance-<tenant>` alongside `platform-grafana`, and reports which. `gitsync
delete` requests no sync at all, so it *refuses* a repository a tenant claims —
`--accept-open-folder` overrides it and prints what is then owed.

## Conventions

- **`uid`** inside each dashboard JSON is its durable identity — pin it, never
  let Grafana auto-assign on import.
- **Datasources are referenced via dashboard variables** (`${datasource}`,
  `${loki_ds}`), never hardcoded UIDs, so the same JSON works against a
  tenant-scoped datasource. A tenant dashboard **pins** those variables to its
  own datasources (`"current"` filled in, `"hide": 2`): an empty `current`
  resolves to whatever Grafana calls default, which is the platform pair, and
  the header on the tenant datasource is the only thing scoping the answer.
- **Per-tenant Loki/Tempo datasources** now exist for jdwlabs and
  dotablaze-tech, named `<tenant>-loki` / `<tenant>-tempo` (rendered by the
  same `templates/observability.yaml` Job as the folder/team/RBAC, off each
  tenant's `observability.tenantId`). Each carries an `X-Scope-OrgID` header
  pinned to that tenant, and that header *is* the isolation — a tenant
  dashboard that leaves its `${loki_ds}` variable unpinned answers out of the
  platform store, so the tenant dashboards pin theirs; see
  [docs/observability/DASHBOARDS-AND-MULTITENANCY.md §5.3/§5.4](../docs/observability/DASHBOARDS-AND-MULTITENANCY.md)
  for the isolation mechanism and its known gaps.
- **Folder = tenant** is the intended end state, enforced by folder-level RBAC
  and a per-tenant Grafana team derived from the tenant's `observability` block
  in `tenants/<tenant>/tenant.yaml`. The platform folder predates this model
  and stays as-is. A tenant reaches it across two folders rather than one:
  their dashboards sync into a separate Git Sync folder because a repository
  cannot adopt a folder it did not create, and that folder is granted to the
  same team from the same block — see "Folder identity is the repository's
  name" above.
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
