# ADR: Reloader adoption — SSA re-verification, annotation-surface rollout, and RBAC posture

Status: accepted, partially implemented. Supersedes condition 3 of ADR 0011.
Annotation-surface plumbing lands with this record for 7 of 9 target
workloads; Reloader itself is added via the platform GitOps path in the same
change set but is not synced until a human approves the RBAC grant below.

## Problem

ADR 0011 recommended adopting Stakater Reloader to close the out-of-band
secret rotation gap (ESO rewrites a Secret; nothing restarts the Deployment
reading it) but gated the recommendation on four conditions, three of which
are install mechanics. Condition 3 assumed ArgoCD's `prune` + `selfHeal`
would revert the pod-template annotation Reloader writes, and made an
`ignoreDifferences` entry in both `services-appset.yaml` and
`deployments-appset.yaml` a same-change prerequisite for install.

That assumption was never checked against a live Reloader-shaped write —
`kubectl.kubernetes.io/restartedAt` is architecturally the same case (an
out-of-band scalar annotation on `.spec.template.metadata.annotations`) and
had already been sitting on a running Deployment for months. This record
checks it.

ADR 0011 also found that 5 of the 9 target workloads expose only
`podAnnotations` in their charts, which cannot carry Reloader's opt-in
annotation — that has to land on the Deployment object's own `metadata`, not
the pod template. Installing the controller before that surface exists
strands the rollout partway while the controller already holds its RBAC
grant. This record lands the surface first.

## Re-verification: does ArgoCD revert an annotation it doesn't own?

Live state, checked directly against the cluster rather than trusted from
the ticket that raised it:

`dotablaze-tech-meowbot-prd` (`dotablaze-tech-prd` namespace) carries
`kubectl.kubernetes.io/restartedAt: "2026-05-23T00:05:41-05:00"` on
`.spec.template.metadata.annotations` today. `--show-managed-fields` on the
live object shows three field managers:

| Manager | Operation | Time | Fields |
|---|---|---|---|
| `argocd-controller` | Apply | 2026-05-21T06:27:09Z | Deployment spec, labels, container env/image/probes/resources — no annotation under `spec.template.metadata` |
| `kubectl-rollout` | Update | 2026-05-23T05:05:41Z | `f:spec.template.metadata.annotations.f:kubectl.kubernetes.io/restartedAt` |
| `kube-controller-manager` | Update | 2026-07-14T02:18:29Z | `status`, `deployment.kubernetes.io/revision` |

`argocd-controller`'s own SSA field set never claims
`f:kubectl.kubernetes.io/restartedAt` — Server-Side Apply only reverts a
field the applying manager asserts ownership of, and this manifest, rendered
from git, has never had that key in it. The Application
(`dotablaze-tech-meowbot-prd` in `argocd`) reports `sync.status: Synced`,
`health.status: Healthy`, with the last sync's `operationState.finishedAt`
at `2026-07-26T05:35:40Z` — 64 days after the annotation was written, and
still un-reverted as of this check (75 days after it was written). Its
`ignoreDifferences` for `kind: Deployment` lists exactly one path,
`.spec.template.metadata.annotations."checksum/secret"` — nothing scoped to
`restartedAt` is present or needed.

This is conclusive for the general case, not just this one field: ArgoCD's
prune/selfHeal loop cannot fight a field it never rendered and never claims.
Reloader's `secret.reloader.stakater.com/reload` and
`reloader.stakater.com/last-reloaded-from` annotations are equally never
present in any Helm render in this repo, so the same non-ownership applies
to them without adding anything to either ApplicationSet's
`ignoreDifferences`. **Condition 3 as written — a mandatory ignore-entry
change landing in the same PR as the install — is unnecessary for the
`annotations` reload strategy.** The two existing `checksum/secret` entries
stay; they cover a different, unrelated annotation and were never load-bearing
for this question.

This also means the earlier framing (`AGENTS.md`-documented risk: "a field
inside a list, under `RespectIgnoreDifferences=true` + `ServerSideApply=true`,
gets silently dropped by ArgoCD's ignore-stripping") never applied to the
`annotations` strategy in the first place — that risk is specific to the
default `env-vars` strategy, which mutates a *list* (`containers[].env[]`).
ADR 0011's choice of `--reload-strategy=annotations` over the default
sidesteps it structurally, independent of this SSA finding.

## Annotation-surface rollout

Reloader's annotation must land on the Deployment's own `metadata`, per
ADR 0011's own render-verified survey. Status after this change:

| Workload | Namespace | Repo | Surface | Status |
|---|---|---|---|---|
| `ai-sre-relay` | `ai-sre` | `platform` (raw manifest) | `metadata.annotations` directly | Done — annotation set |
| `platform-litellm` | `ai-sre` | `platform` (`helm-charts/litellm-helm`) | new `deploymentAnnotations` value | Done — chart + value |
| `platform-headlamp` | `headlamp` | `platform` (upstream chart) | `deploymentAnnotations` (render-confirmed) | Done — value set |
| `platform-holmes-holmes` | `ai-sre` | `platform` (upstream chart) | `commonAnnotations` (render-confirmed; lands on every chart-rendered object, harmless — Reloader only acts on Deployment/StatefulSet/DaemonSet) | Done — value set |
| `platform-grafana` | `monitoring` | `platform` (upstream chart) | `annotations` (render-confirmed) | Done — value set |
| `jdwlabs-usersrole-non` | `jdwlabs-non` | `jdwlabs/deployments` (`charts/common` library) | new `deploymentAnnotations` value, library bumped 0.3.0 → 0.4.0, propagated to all 6 dependent charts | Done — chart + value |
| `jdwlabs-usersrole-prd` | `jdwlabs-prd` | `jdwlabs/deployments` (`charts/common` library) | same library bump | Done — chart + value |
| `dotablaze-tech-meowbot-non` | `dotablaze-tech-non` | `dotablaze-tech/deployments` | `charts/meowbot/templates/deployment.yaml` has no object-level annotation hook | **Not done** — repository not reachable from this session; follow-up subtask filed against JDWLABS-291 |
| `dotablaze-tech-meowbot-prd` | `dotablaze-tech-prd` | `dotablaze-tech/deployments` | same | **Not done** — same follow-up |

7 of 9 are ready for Reloader to act on the moment it is installed and
synced. The `jdwlabs/deployments` change bumps the shared `common` library
chart, so it reaches `authui`, `container`, `rolesui`, `servicediscovery`,
`usersui` too (dependency-lock regeneration only for those five; the
`deploymentAnnotations` value itself is set only where a Reloader target is
in scope, i.e. `usersrole`). Each Reloader annotation value lists the actual
Secret name(s) the workload's `env`/`envFrom` reads, not a
tenant-envelope-scale wildcard.

The two `dotablaze-tech` workloads live in a chart this session has no
checkout of and no ability to open a PR against. Installing Reloader now
still reaches 7 of 9 immediately; the remaining 2 catch up once the
follow-up subtask lands a `deploymentAnnotations`-equivalent hook in that
chart's `deployment.yaml`.

## RBAC posture — decision

ADR 0011 narrowed this to three real options and left the choice as a
judgement call. Deciding it now: **accept the cluster-wide ClusterRole**
(the chart's default, `watchGlobally: true`), one release,
`--reload-strategy=annotations`.

- Seven per-namespace releases (`watchGlobally=false` × 7) were rejected in
  ADR 0011 for multiplying the standing footprint and upgrade surface
  sevenfold, and that reasoning doesn't change here.
- Tracking unreleased `master` for a security-relevant controller to get a
  namespaced-Role feature is rejected for the same reason ADR 0011 gave:
  running unreleased code for a security control, with a chart version
  number that doesn't distinguish it from the release lacking the feature.
- The read half of the ClusterRole (`get`/`list`/`watch` on every Secret and
  ConfigMap) is not a qualitatively new grant in this cluster — External
  Secrets Operator already holds equivalent breadth via the
  `ClusterSecretStore`, and Vault, not the Kubernetes Secret objects, is the
  system of record. The genuinely new capability is the write half
  (`update`/`patch` on every Deployment/DaemonSet/StatefulSet,
  `create`/`delete` on Jobs) — a compromised or buggy Reloader can repoint
  any workload's image cluster-wide. That is registered here as a real,
  standing cost, not waved away.
- Mitigations already in place rather than proposed: GitOps-only deployment
  (this repo's binary contract keeps agents and operators off raw
  `kubectl`/`helm`), the `annotations` reload strategy (Reloader mutates one
  scalar annotation per roll, not env vars, and every roll is attributable
  via `reloader.stakater.com/last-reloaded-from`), and opt-in-per-workload
  adoption (no `--auto-reload-all`).

This grant is the reason the install PR is opened but not merged or synced
without an explicit human go-ahead — the RBAC decision is recorded here;
the merge decision is not this document's to make.

## Consequences

Once installed and synced, 7 of 9 workloads roll automatically on the next
rotation of the Secret they read; the 2 `dotablaze-tech` workloads do not
until the follow-up subtask lands their annotation surface. No
`ignoreDifferences` change is required for any of the 9 — condition 3's
premise doesn't hold for the `annotations` strategy, as re-verified above.

Verifying the adoption end to end (a workload actually rolls, and the new
value is live in the running process) requires an actual Vault rotation
after the install PR merges and ArgoCD syncs it — a human-gated action this
session cannot perform. That check is the explicit post-merge step recorded
on the install PR, not claimed as done here.

This record does not reopen ADR 0011's Decision, Options, or conditions 1,
2, and 4 — only condition 3 is superseded, on the evidence above. ADR 0011
remains the record of the original evaluation and is unedited.
