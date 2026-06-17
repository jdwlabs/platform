# Observability: Dashboards-as-Code, Suite Design, and Multi-Tenancy

Status: Proposal with core decisions confirmed (2026-06-17, see §7). This
document describes a target architecture and a migration path. It does **not**
change any running provisioning today — the existing ConfigMap-sidecar
dashboards stay in place until the migration plan below is executed in follow-up
changes.

Audience: platform operators and tenant onboarders.

---

## 1. Where we are today

Dashboards are stored as raw Grafana JSON embedded in Kubernetes `ConfigMap`
manifests, scattered across each service's `postInstall/` directory under the
`platform` tenant:

| Dashboard | Location |
|-----------|----------|
| Node exporter (USE) | `tenants/platform/services/kube-prometheus-stack/postInstall/dashboard-node-exporter.yaml` |
| K8s overview | `tenants/platform/services/kube-prometheus-stack/postInstall/dashboard-k8s-overview.yaml` |
| ArgoCD | `tenants/platform/services/argo-cd/postInstall/dashboard-argocd.yaml` |
| Longhorn | `tenants/platform/services/longhorn/postInstall/dashboard-longhorn.yaml` |
| Loki | `tenants/platform/services/loki/postInstall/dashboard-loki.yaml` |
| CNPG | `tenants/platform/services/cnpg-operator/postInstall/dashboard-cnpg.yaml` |

Grafana ingests them through the **kube-prometheus-stack/Grafana sidecar**,
keyed on the label `grafana_dashboard: "1"` (see
`tenants/platform/services/grafana/values.yaml`):

```yaml
sidecar:
  dashboards:
    enabled: true
    label: grafana_dashboard
    labelValue: "1"
    searchNamespace: ALL
    provider:
      foldersFromFilesStructure: true
```

Datasources (`Prometheus`, `Loki`, `Alertmanager`) are declared inline in the
same Grafana values file with fixed UIDs. Grafana is on chart revision `9.4.3`
(a separate workstream upgrades it to v13 and enables Git Sync).

### Problems with the status quo

- **Discoverability:** a dashboard's source of truth is buried in an unrelated
  service's `postInstall/` dir. There is no single place to see "all our
  dashboards."
- **Editing friction:** editing a panel means hand-patching escaped JSON inside
  YAML. The round-trip from "edit in Grafana UI" to "commit to Git" is manual
  copy-paste, which guarantees drift.
- **No folder/RBAC structure:** `foldersFromFilesStructure` derives folders
  from the ConfigMap filesystem layout, not from intent. There is no per-tenant
  isolation of who can see/edit what.
- **Coupling to the sidecar:** the sidecar is a transitional mechanism. The
  future-default provisioning path in Grafana v13 is Git Sync.
- **Single tenant assumption:** all dashboards live under `tenants/platform/`.
  With `jdwlabs` and `dotablaze-tech` now real tenants, there is no model for
  tenant-scoped dashboards/datasources/alerts.

---

## 2. Dashboards-as-code: options and recommendation

### Options evaluated

| Option | What it is | Fit for this repo |
|--------|------------|-------------------|
| **ConfigMap + sidecar** (today) | Raw JSON in ConfigMaps, picked up by a label-watching sidecar. | Works, GitOps-native, but JSON-in-YAML is unmaintainable and has no folder/RBAC model. Transitional. |
| **Grafana Git Sync (v13 GA)** | Grafana natively syncs dashboards + folders to/from a Git repo path; resources modelled as Kubernetes-style kinds in Grafana's unified storage. GA since April 2026. Bidirectional: UI edits can open a PR back to Git. | **Recommended.** It is Grafana's own recommended provisioning path, keeps the "edit in UI → PR" loop closed, and aligns with the v13 upgrade already in flight. |
| **Grafonnet / Jsonnet + jsonnet-bundler** | Dashboards generated from Jsonnet libraries (`grafonnet`, `kubernetes-mixin`). Compiled to JSON in CI. | Strong for *generating* mixin-derived dashboards (DRY, parameterised). Best used as a **build step that emits JSON into the Git Sync repo**, not as the provisioning mechanism itself. |
| **Terraform `grafana` provider** | Dashboards/folders/datasources/orgs/teams managed as TF resources. | Good for the *control-plane* objects Git Sync does **not** manage (orgs, teams, RBAC, datasources, service accounts). Poor fit for dashboard JSON churn in a GitOps/ArgoCD shop — introduces a second reconciler (TF state) competing with ArgoCD. Use surgically, not for dashboards. |

### Recommendation

**Adopt Git Sync as the dashboard provisioning path, with Grafonnet/mixins as
an optional upstream code-generation step. Keep ArgoCD as the deployment
reconciler; keep Terraform out of the dashboard loop.**

Rationale:

1. The Grafana v13 upgrade is already happening — Git Sync is the native,
   GA, recommended path on that version. Building on the sidecar would be
   building on the deprecated-in-spirit mechanism.
2. Git Sync closes the "edit in UI, version in Git" loop that the sidecar
   cannot. Operators can iterate in the UI and open a PR; reviewers see a JSON
   diff in the platform repo.
3. JSON files in a flat, discoverable directory are far easier to review and
   own than JSON escaped inside ConfigMap YAML.
4. Grafonnet/mixins remain available where parameterised generation pays off
   (the Kubernetes mixin, per-tenant RED dashboards) — they compile to JSON
   that lands in the same Git Sync directory, so there is exactly one runtime
   format Grafana sees.

### Schema v1 → v2

- Grafana dynamic dashboards (schema **v2**) are GA and on by default as of
  Grafana v13. The v2 schema models dashboard elements as Kubernetes kinds.
- **v1 dashboards are auto-migrated to v2 on load** — no manual migration step
  is required to bring today's ConfigMap JSON across.
- Caveat to record in the runbook: once a self-managed dashboard is migrated to
  v2 and saved, it cannot be migrated back to v1. Migrate deliberately
  (one dashboard at a time, commit the v2 JSON) rather than mass-opening every
  dashboard in the UI.

### Avoiding sidecar ↔ Git Sync drift (the critical migration rule)

A dashboard must be owned by **exactly one** provisioner at a time. If the same
dashboard exists both as a sidecar ConfigMap and in the Git Sync repo, the two
reconcilers fight and the UI shows nondeterministic state.

Migration is therefore **per-dashboard, atomic**:

1. Export the dashboard's current JSON (from the ConfigMap or the live UI).
2. Commit it to the Git Sync dashboards directory (Section 3 layout).
3. In the **same** change, delete the corresponding ConfigMap from the
   service's `postInstall/`.
4. ArgoCD prunes the ConfigMap; Git Sync provisions the file. No window where
   both own it.

Do **not** flip the sidecar off globally first — that would orphan every
not-yet-migrated dashboard. Leave the sidecar enabled until the last ConfigMap
dashboard is migrated, then remove `sidecar.dashboards` from the Grafana values
in a final cleanup change.

---

## 3. Proposed repository / folder layout

Git Sync points at a single repo path. Because this is a GitOps monorepo, the
natural home is a top-level `observability/` tree (a peer of `tenants/`,
distinct from per-service `postInstall/`). This makes "all our dashboards" one
discoverable directory and lets folder structure encode Grafana folder + RBAC
intent.

```
observability/
└── dashboards/                      # <- Git Sync repository path
    ├── platform/                    # Grafana folder "Platform" (platform tenant)
    │   ├── cluster-overview.json
    │   ├── node-use.json
    │   ├── namespace-overview.json
    │   ├── gateway-red.json
    │   ├── logs-explore.json
    │   ├── traces-explore.json
    │   ├── slo-error-budget.json
    │   ├── capacity-cost.json
    │   ├── argocd.json
    │   ├── longhorn.json
    │   ├── loki.json
    │   └── cnpg.json
    ├── jdwlabs/                      # Grafana folder "jdwlabs" (tenant-scoped)
    │   └── jdwlabs-services-red.json
    └── dotablaze-tech/               # Grafana folder "dotablaze-tech"
        └── dotablaze-services-red.json
```

- Each **top-level directory under `dashboards/` becomes a Grafana folder.**
  Folder = tenant boundary (Section 5). Folder-level RBAC scopes who sees/edits.
- Filenames are stable dashboard slugs; the `uid` inside the JSON is the durable
  identity (pin it, don't let Grafana auto-assign).
- Datasource references inside dashboard JSON use **datasource variables**
  (`${datasource}` / `${loki_ds}`), never hardcoded UIDs, so the same JSON works
  when a tenant is pointed at a tenant-scoped datasource.

### Optional Grafonnet generation tree

Where dashboards are generated (mixins, repeated per-tenant RED), keep the
source under `observability/jsonnet/` and compile into `observability/dashboards/`
in CI. The compiled JSON is committed (so Git Sync and reviewers see plain JSON,
and there is no Jsonnet toolchain dependency at runtime).

```
observability/
├── jsonnet/
│   ├── jsonnetfile.json            # jsonnet-bundler: grafonnet, kubernetes-mixin
│   ├── lib/red.libsonnet           # parameterised RED dashboard (per service/tenant)
│   └── main.jsonnet                # entrypoint: emits files into ../dashboards/
└── dashboards/                     # generated + hand-authored JSON (committed)
```

---

## 4. State-of-the-art dashboard suite

The suite is organised around the three standard methods:

- **RED** (Rate, Errors, Duration) — for *request-driven services* (ingress,
  gateway, apps).
- **USE** (Utilization, Saturation, Errors) — for *resources* (nodes, disks).
- **Four Golden Signals** (latency, traffic, errors, saturation) — RED + the
  saturation dimension; used for SLO views.

### Platform folder (the platform tenant)

| Dashboard | Method | Source / mixin | Notes |
|-----------|--------|----------------|-------|
| Cluster overview | mixed | kubernetes-mixin (`k8s-resources-cluster`) | Replaces today's hand-rolled k8s-overview. |
| Node / USE | USE | kubernetes-mixin (`node-rsrc-use`) + node_exporter mixin | Replaces today's node-exporter dashboard. |
| Namespace overview | mixed | kubernetes-mixin (`k8s-resources-namespace`) | Per-namespace CPU/mem/network; namespace template var feeds tenant scoping. |
| Workload / pod | mixed | kubernetes-mixin (`k8s-resources-workload`) | Deployment/StatefulSet drill-down. |
| Gateway / Ingress RED | RED | nginx-gateway-fabric / Gateway API metrics | Rate, 5xx error ratio, p50/p95/p99 latency per route. |
| Logs explore | — | Loki datasource (built-in Explore-style) | Keep the existing Loki **operational** dashboard (`loki.json`); add a logs-explore view scoped by namespace/tenant label. |
| Traces explore | — | Tempo datasource | Service graph + trace search; trace-to-logs and trace-to-metrics correlation (added with the Tempo workstream). |
| SLO / error budget | Golden signals | Custom (or Sloth-generated recording rules) | Per-SLO availability + latency, burn-rate, remaining error budget. |
| Capacity / cost | USE-adjacent | kube-state-metrics + node_exporter | Requests-vs-capacity, PVC growth (Longhorn), retention headroom. |
| ArgoCD | RED-ish | existing `dashboard-argocd` | Sync health, app status, reconciliation rate. |
| Longhorn | USE | existing `dashboard-longhorn` | Volume health, capacity, rebuild status. |
| Loki (operational) | USE | existing `dashboard-loki` | Loki's own ingestion/query health. |
| CNPG | mixed | existing `dashboard-cnpg` | Postgres cluster health, replication, backups. |

### Per-tenant folders (jdwlabs, dotablaze-tech)

| Dashboard | Method | Source | Notes |
|-----------|--------|--------|-------|
| Tenant services RED | RED | Grafonnet `red.libsonnet` template | One row per app in the tenant's namespaces; rate/error/duration from app metrics + gateway routes filtered to the tenant. |
| Tenant logs | — | Loki, label/tenant-scoped | Namespace-restricted log view. |
| Tenant resource usage | USE | kubernetes-mixin namespace, scoped | CPU/mem/quota consumption vs ResourceQuota. |

The kubernetes-mixin and node_exporter mixin are the authoritative upstreams for
the cluster/node/namespace/workload dashboards; pin their versions in
`jsonnet/jsonnetfile.json` and regenerate on bump. The bespoke dashboards (SLO,
capacity, gateway RED, tenant RED) are maintained as Grafonnet templates so they
parameterise cleanly per tenant.

---

## 5. Multi-tenancy architecture

Today there are three tenants (`platform`, `jdwlabs`, `dotablaze-tech`) but a
single shared observability stack with `auth_enabled: false` on Loki and a
single-org Grafana. The recommendation below is **incremental**: it keeps the
single shared stack and layers tenancy on top, rather than standing up
per-tenant stacks (overkill for a homelab-scale platform).

### 5.1 Grafana: folders + RBAC + teams (NOT organizations)

**Decision: use folders + folder-level RBAC + teams. Do not use Grafana
organizations.**

Why not organizations:

- **Git Sync does not provision organizations.** Git Sync manages dashboards
  and folders (alerts on the roadmap). Orgs, teams, datasources, and RBAC are
  control-plane objects outside Git Sync. Choosing orgs would force a second
  provisioning tool (Terraform/API) for the tenant boundary and split the model.
- Organizations are hard-isolated silos: no cross-org dashboards, duplicated
  datasources, clumsy shared "platform" views. For a small platform with shared
  infra this is friction without payoff.
- Folders map cleanly onto the Git Sync directory layout in Section 3
  (one top-level dir = one folder = one tenant boundary), and folder-level RBAC
  + teams give per-tenant view/edit isolation while still allowing shared
  platform dashboards.

Model:

- One Grafana **team per tenant** (`jdwlabs`, `dotablaze-tech`), plus platform
  admins.
- One **folder per tenant**, with RBAC granting the tenant team `View` (or
  `Edit`) on its own folder and no access to other tenants' folders. The
  `Platform` folder is admin-managed; tenants get read-only or no access as
  policy dictates.
- Teams and folder-permission bindings are control-plane objects. Manage them
  via the **Terraform `grafana` provider** *or* Grafana provisioning files —
  used **only** for these control-plane objects, never for dashboard JSON.
  (This is the one narrow place Terraform earns its keep.)

### 5.2 Prometheus / metrics: label-based tenancy now, Mimir later

**Decision (now): single Prometheus, label-based tenancy via the existing
`platform.jdwlabs.io/tenant` namespace label + Grafana folder/datasource
scoping. Defer Mimir until scale or hard-isolation demands it.**

- The platform already labels namespaces with `platform.jdwlabs.io/tenant`.
  kube-state-metrics surfaces this, so every series is attributable to a tenant
  via `namespace` → tenant join. Tenant dashboards filter on the tenant's
  namespaces.
- This is *soft* multi-tenancy (a determined user with broad Grafana access can
  query across tenants). For a trust-internal homelab that is acceptable.
- **Mimir (multi-tenant, `X-Scope-OrgID`)** is the upgrade path when hard
  metric isolation, per-tenant retention/limits, or long-term storage is
  required. The current `monitoring` (k8s-monitoring/Alloy) setup already
  remote-writes to Prometheus; swapping the remote-write target to Mimir and
  injecting `X-Scope-OrgID` per tenant is the migration. Per-tenant Prometheus
  instances are explicitly **not** recommended (operational multiplication for
  little gain at this scale).

### 5.3 Loki: enable native multi-tenancy (`X-Scope-OrgID`)

**Decision: turn on Loki multi-tenancy (`auth_enabled: true`) and assign each
tenant an `X-Scope-OrgID`, when tenant log isolation is needed.**

- Loki is `auth_enabled: false` today (single tenant `fake`). Native tenancy is
  the `X-Scope-OrgID` header: writers (Alloy) stamp the tenant id, readers
  (Grafana per-tenant Loki datasource) send the matching header.
- Mechanics:
  - Alloy log pipeline sets `X-Scope-OrgID` per tenant — derive it from the
    `platform.jdwlabs.io/tenant` namespace label so routing follows the
    existing tenant model.
  - One Grafana Loki datasource **per tenant**, each pinned to that tenant's
    `X-Scope-OrgID` (via `httpHeaderName`/`httpHeaderValue` custom headers),
    scoped to the tenant's Grafana folder.
- This gives true per-tenant log isolation at the store, not just at the UI.

### 5.4 Tempo: native multi-tenancy (`X-Scope-OrgID`), same pattern as Loki

**Decision: enable Tempo multi-tenancy with the same `X-Scope-OrgID` tenant id
scheme as Loki, as part of the tracing workstream.**

- Tempo uses the identical `X-Scope-OrgID` mechanism. Reusing the same tenant
  id across Loki + Tempo (+ Mimir later) gives one consistent tenant identifier
  across all three signals and makes trace↔log↔metric correlation tenant-aware.

### 5.5 How this extends the `tenant-envelope` model

Tenancy must ride the **existing** `tenant.yaml` / `tenant-envelope`
ApplicationSet model so adding a tenant stays a single-file operation.

Proposed extension to the tenant schema (illustrative — not yet implemented):

```yaml
# tenants/<tenant>/tenant.yaml
observability:
  tenantId: jdwlabs                 # X-Scope-OrgID for Loki/Tempo/(Mimir)
  grafana:
    folder: jdwlabs                 # Grafana folder == dashboards/<tenant>/ dir
    team: jdwlabs                   # Grafana team granted access to the folder
    access: edit                    # view | edit
```

Flow when a tenant is added:

1. `tenant.yaml` gains the `observability` block above.
2. `tenant-envelope` renders (new templates): the tenant's Grafana team +
   folder-RBAC binding, and the tenant-scoped Loki/Tempo datasources carrying
   the tenant's `X-Scope-OrgID`.
3. Alloy log/trace pipelines route that tenant's namespaces to the matching
   `X-Scope-OrgID`.
4. Dashboards for the tenant live in `observability/dashboards/<tenant>/` and
   land in the matching Grafana folder via Git Sync.

This keeps the "one tenant = one `tenants/<tenant>/` dir" promise: dashboards,
datasources, folder/RBAC, and signal-store tenant ids all derive from a single
tenant definition.

---

## 6. Migration path (summary)

Each step is an independent, reviewable change. Nothing here is executed by
this proposal PR.

1. **Land Grafana v13 + Git Sync** (separate in-flight workstream). Point Git
   Sync at `observability/dashboards/`. Sidecar stays enabled in parallel.
2. **Seed the new tree** with the platform-folder dashboards: prefer
   mixin/Grafonnet-generated JSON for cluster/node/namespace; copy the existing
   ArgoCD/Longhorn/Loki/CNPG JSON across as-is.
3. **Per-dashboard cutover** (atomic): commit JSON to `observability/dashboards/`
   *and* delete the source ConfigMap in the same change (Section 2 rule). Repeat
   until all six existing dashboards are migrated.
4. **Add the SLO, gateway-RED, capacity, traces dashboards** as new files (no
   ConfigMap predecessor).
5. **Remove the sidecar** (`sidecar.dashboards`) from the Grafana values once
   no ConfigMap dashboards remain.
6. **Layer tenancy**: extend `tenant.yaml` + `tenant-envelope` per Section 5.5;
   enable Loki/Tempo `X-Scope-OrgID`; create Grafana teams/folders/RBAC.

---

## 7. Decisions (confirmed 2026-06-17)

Confirmed by Jake:

1. **Dashboards-as-code = Git Sync** (not Terraform, not staying on the
   sidecar), with Grafonnet/mixins as an optional CI generation step. ✅
2. **Grafana tenancy = folders + folder-RBAC + teams, not organizations** —
   folders are the tenant boundary (Git Sync cannot provision orgs). ✅
3. **Metrics tenancy = label-based on shared Prometheus now; Mimir later.** Soft
   metric isolation is accepted for internal tenants; revisit Mimir + hard
   isolation when an external/untrusted tenant or noisy-neighbor pressure
   appears. ✅
4. **Loki + Tempo = native `X-Scope-OrgID` multi-tenancy**, tenant id derived
   from the existing namespace tenant label (store-level isolation, not UI-only).
5. **Tenancy rides `tenant.yaml`/`tenant-envelope`** via a new `observability`
   block, keeping tenant onboarding single-file.

### Git Sync operational defaults (confirmed)

These govern how Git Sync is wired against this **monorepo** (the chosen backing
repo — not a separate dashboards repo):

- **Backing repo = this monorepo.** Dashboards live under `observability/`
  (outside `tenants/`) so the ArgoCD ApplicationSets — which watch `tenants/` —
  never try to apply the dashboard JSON. Git Sync, not ArgoCD, reconciles that
  tree; the two never own the same object.
- **Write path = PR workflow, human review, no auto-merge.** Editors save in the
  Grafana UI → the Grafana GitHub App opens a branch + PR against `main` → a
  human reviews/merges → Git Sync pulls merged `main` back. Branch protection on
  `main` is unchanged; the App is just another PR author. Auto-merge for trivial
  dashboard diffs is deferred until the round-trip is proven.
- **Rollout scope = platform folder first.** Scaffold all three tenant folders
  (`platform`, `jdwlabs`, `dotablaze-tech`) but only wire `platform/` to Git Sync
  now; add the tenant folders as each onboards.
- **Migration pace = trickle, not big-bang.** New and hand-edited dashboards go
  to Git Sync; existing ConfigMap/sidecar dashboards stay as-is until individually
  touched, at which point each is moved per the §2 one-owner rule (add to Git
  Sync + delete the ConfigMap in the same change). The sidecar stays enabled
  until the last dashboard migrates.
- **Auth = dedicated Grafana GitHub App**, scoped to this repo (Contents +
  Pull requests: read/write), distinct from ArgoCD's repo credentials; private
  key staged in Vault `kv/grafana-gitsync` (see the grafana service PR).

---

## References

- [Grafana dashboards as code: manage with Git (Git Sync)](https://grafana.com/blog/git-sync-grafana/)
- [Set up Git Sync](https://grafana.com/docs/grafana/latest/as-code/observability-as-code/git-sync/git-sync-setup/)
- [Work with provisioned dashboards in Git Sync](https://grafana.com/docs/grafana/latest/as-code/observability-as-code/git-sync/provisioned-dashboards/)
- [Dashboard v2 schema and dynamic dashboards](https://grafana.com/whats-new/2025-05-05-dashboard-v2-schema-and-dynamic-dashboards/)
- [Dynamic dashboards GA](https://grafana.com/whats-new/2026-04-08-dynamic-dashboards-is-now-generally-available/)
- [Manage multi-team access in a single Grafana instance](https://grafana.com/docs/grafana/latest/setup-grafana/configure-access/multi-team-access/)
- [Loki multi-tenancy](https://grafana.com/docs/loki/latest/operations/multi-tenancy/)
- [Tempo multi-tenancy](https://grafana.com/docs/tempo/latest/operations/manage-advanced-systems/multitenancy/)
- [Mimir authentication and authorization](https://grafana.com/docs/mimir/latest/manage/secure/authentication-and-authorization/)
- [kubernetes-mixin](https://github.com/kubernetes-monitoring/kubernetes-mixin)
- [The RED Method](https://grafana.com/blog/the-red-method-how-to-instrument-your-services/)
- [dotdc/grafana-dashboards-kubernetes (modern K8s dashboards)](https://github.com/dotdc/grafana-dashboards-kubernetes)
