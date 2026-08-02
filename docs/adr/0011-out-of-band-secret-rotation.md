# ADR: Propagating out-of-band secret rotation to running workloads

Status: proposed. Recommends adopting Stakater Reloader, gated on a human
decision about the cluster-wide RBAC grant it requires. Nothing installed.

## Problem

A workload that reads a secret through `env` or `envFrom` copies the value
into the process at exec and never looks again. When External Secrets
Operator rewrites that Secret from Vault, the Secret is correct within its
refresh interval and the running process is not. Nothing in the control
plane reports a difference: the ExternalSecret is `SecretSynced`, the
Deployment is unchanged, ArgoCD is Synced and Healthy, and the pod is Ready.

The render-time `checksum/config` annotation added to the `common` library
chart closes the git-sourced half of this problem, and it works — both
`servicediscovery` Deployments carry the annotation live. It cannot close
the other half by construction. A checksum computed from chart content is
constant across a rotation, because the rotated value never appears in the
chart.

This is not a single-workload wrinkle. A live sweep of every ExternalSecret
against every Deployment, StatefulSet and DaemonSet in the cluster found 21
ExternalSecrets, of which 13 land in a Secret consumed as environment by
nine distinct Deployments:

| Deployment | Namespace | How | Refresh |
|---|---|---|---|
| `ai-sre-relay` | `ai-sre` | `envFrom` | 1h |
| `platform-litellm` | `ai-sre` | `envFrom` | 1h |
| `platform-headlamp` | `headlamp` | `envFrom` | 1m |
| `platform-holmes-holmes` | `ai-sre` | `secretKeyRef` | 1h |
| `platform-grafana` | `monitoring` | `secretKeyRef` ×4 | 1m |
| `jdwlabs-usersrole-non` | `jdwlabs-non` | `secretKeyRef` ×3 | 1m |
| `jdwlabs-usersrole-prd` | `jdwlabs-prd` | `secretKeyRef` ×3 | 1m |
| `dotablaze-tech-meowbot-non` | `dotablaze-tech-non` | `secretKeyRef` ×3 | 1m |
| `dotablaze-tech-meowbot-prd` | `dotablaze-tech-prd` | `secretKeyRef` ×3 | 1m |

Three further ExternalSecrets are consumed as volumes, where the kubelet
refreshes the file in place and the application's own reload behaviour
decides the outcome. Those are out of scope here. The remaining five have no
pod-level reference at all — they are read through the API by a controller,
so a rotation reaches them without a restart.

The sweep covered Deployments, StatefulSets and DaemonSets. It did not cover
the six CronJobs, deliberately: a CronJob creates a fresh pod on every run
and therefore reads the current Secret by construction.

The failure has already been observed. Adding a key to the `ai-sre-relay`
ExternalSecret wrote it to the Secret in seconds while the 62-minute-old pod
kept running without it; the value only took effect when the pod was
replaced by hand. That credential is shared with the Alertmanager receiver
specifically so the two can never present different tokens — they read the
same Vault property. Alertmanager reloads it from a mounted file and the
relay does not, so they cannot drift in configuration but can drift in
effect, and every alert 401s in the gap.

## Options considered

**Application-level file watch (fsnotify on a mounted secret).** Rejected as
a general answer. It does not apply to any of the nine workloads as they
stand, because there is no file to watch — the value is an environment
variable. Reaching it requires first converting each workload from `env` to
a volume-mounted Secret, then adding reload logic to each application's
source, in whichever repository that source lives. For the JVM service the
rotated values include a JDBC password, so a file re-read is not sufficient:
the connection pool has to be re-initialised. The cost is per-workload, in
someone else's repository, and recurs for every workload added later. It is
the right answer only for an application you already own and are already
changing.

**Hashed or immutable ConfigMap names.** Rejected because it solves a
different problem. Naming a ConfigMap after a hash of its contents forces a
new pod spec when git-sourced content changes — which is what the
`checksum/config` annotation already does, verified live. It has no purchase
on out-of-band rotation: the ESO target Secret's name is fixed by
`target.name`, its contents change outside git, and no render changes. The
tempting variant — hash the ExternalSecret's `remoteRef` keys instead — is
the known template-input trap. It hashes what we asked for rather than what
came back, so it is constant across every rotation.

**A documented manual step in the rotation runbook.** This is the status
quo, and it is what deferring again means. `kubectl rollout restart` after a
rotation, or bumping an explicit generation annotation in git, costs no
memory, no new RBAC and no interaction with ArgoCD, and the git-annotation
variant is entirely GitOps-native. It is rejected because its correctness
depends on a human remembering, at the one moment when forgetting is most
expensive, and because forgetting is silent — every control-plane signal
stays green while the credential is stale. That is the same silent-failure
shape as the config-checksum work that preceded this, and the reason that
work was done rather than documented.

**Stakater Reloader, `annotations` strategy, opt-in per workload.**
Recommended. It watches the API server rather than git, which is the only
place the changed value exists, and it reaches all nine workloads with one
annotation each and no application change.

## Decision

Adopt Reloader, subject to four conditions that are part of the install and
not follow-up work, and subject to a human decision on the first of them.

**1. The RBAC grant is the real cost, not the memory.** The chart's
ClusterRole grants `get`/`list`/`watch` on every Secret and ConfigMap in the
cluster and `update`/`patch` on every Deployment, DaemonSet and StatefulSet,
plus `create`/`delete` on Jobs. A component holding both halves can read any
ServiceAccount token and repoint any workload's image, which makes it a
cluster-admin-equivalent escalation path.

The obvious mitigation — keep one global release and hand-substitute
namespaced Roles in the seven namespaces — does not work, and is worth
stating explicitly because it is what a reader reaches for first. Watch
scope is set by `KUBERNETES_NAMESPACE`, not by what RBAC permits.
`watchGlobally=false` is what sets it, from the pod's own namespace via
`fieldRef`, and it swaps the ClusterRole and ClusterRoleBinding for a Role
and RoleBinding carrying the same rules in that one namespace. There is no
way to hand a single release a *set* of namespaces: the released chart has
no `namespaces` value and the `v1.4.19` binary has no `--namespaces` flag.
A globally-watching Reloader therefore issues a cluster-scoped LIST/WATCH
that seven namespaced Roles can never satisfy, so its informer fails its
initial list. The predictable end of that is someone restoring the
ClusterRole to make the controller work, with this ADR's stated safeguard
now silently absent.

The real options are narrower and each carries a cost:

- **Seven releases, one per namespace, each `watchGlobally=false`.** The
  only way to get namespaced RBAC out of the released chart. Multiplies the
  standing footprint and the upgrade surface by seven.
- **Track unreleased `master` for both chart and image.** The per-namespace
  Role loop is a master-only feature added 2026-06-26, and confusingly
  carries the same chart version number as the release that lacks it — so
  "chart 2.2.14" is not a sufficient description of what you installed.
  Means running unreleased code for a security control.
- **Accept the ClusterRole**, with cluster-wide Secret read plus workload
  patch as a stated, registered cost.

This is the one part of the decision that is a judgement call rather than a
measurement. It is also narrower than it first appears, and it should be
made deliberately with these three options in view rather than by reaching
for a fourth that does not exist.

**2. `--reload-strategy=annotations`, never the default.** The default
`env-vars` strategy injects a hash-bearing environment variable into every
container, which writes into `.spec.template.spec.containers[].env[]`. This
repository has already recorded, in the tenant-envelope ignore rules, what
happens when a field inside a list is ignored under
`RespectIgnoreDifferences=true` together with `ServerSideApply=true`: ArgoCD
strips the ignored subfields before applying, the list merge silently drops
entries, and the diff never converges. The `annotations` strategy writes one
scalar annotation, `reloader.stakater.com/last-reloaded-from`, into the pod
template, and upstream documents it as the strategy to use with GitOps
tooling for this reason.

**3. Both ApplicationSets need the ignore entry, in the same change.** With
prune and selfHeal on every Application, ArgoCD reverts the annotation
Reloader writes and the two controllers fight indefinitely. The Deployment
ignore list currently covers only
`.spec.template.metadata.annotations."checksum/secret"`, and it appears
twice — in `services-appset.yaml` for platform services and in
`deployments-appset.yaml` for the application charts. Both `usersrole`
Deployments are generated by the second one, so an entry added only to the
first covers seven of the nine workloads and leaves two quietly broken.
StatefulSets need nothing: they already ignore the whole pod annotation map.

**4. Explicit requests and limits.** The chart ships `resources: {}`. The
`tenant-limits` LimitRange present in every tenant namespace supplies a
64Mi memory `defaultRequest` but no default limit, so the pod would be
Burstable and memory-unbounded — which fails the memory epic's own
"zero BestEffort or memory-unbounded workloads" metric. It also degrades
Reloader specifically, because the chart derives `GOMEMLIMIT` from
`limits.memory`; with no limit the Go runtime is handed a ceiling the size
of the node and never collects under pressure.

## Prerequisite the install cannot supply

Reloader's opt-in annotation goes on the **Deployment object**, not the pod
template — upstream's README and the chart's own `NOTES.txt` both place
`secret.reloader.stakater.com/reload` in the Deployment's `metadata`. The
controller does also look at the pod template, but upstream documents that
dual path as a precedence edge case with unpredictable ordering, which is
not something to build an adoption on.

Five of the nine workloads render from a template with no object-level
annotation hook at all. Their charts expose `podAnnotations`, which lands in
`.spec.template.metadata.annotations`, and nothing that reaches the
Deployment's own metadata. They cannot be annotated until that changes, and
three of the five sit outside this repository:

| Workloads | Template | Repository |
|---|---|---|
| `jdwlabs-usersrole-non`, `jdwlabs-usersrole-prd` | `charts/common/templates/_deployment.yaml` | `jdwlabs/deployments` |
| `dotablaze-tech-meowbot-non`, `-prd` | `charts/meowbot/templates/deployment.yaml` | `dotablaze-tech/deployments` |
| `platform-litellm` | `helm-charts/litellm-helm/templates/deployment.yaml` | this repository |

The two are not the same size of change. The `jdwlabs/deployments` one is a
library chart, so a single edit reaches both `usersrole` environments and
every future chart built on it — but it needs a `common` version bump and a
`Chart.lock` regeneration across all six app charts that depend on it. The
`dotablaze-tech/deployments` one is a standalone chart template; that
repository has no `common` library to fix centrally.

The remaining four need nothing: `ai-sre-relay` is raw manifests and is
edited directly, and `platform-headlamp`, `platform-holmes-holmes` and
`platform-grafana` come from upstream charts whose `deploymentAnnotations`,
`commonAnnotations` and `annotations` values respectively were each
confirmed by render to reach the Deployment object.

An adoption planned without this stalls at five of nine workloads after the
controller is already installed and holding its RBAC.

## The memory objection no longer decides this

This evaluation was originally gated in part on an always-on Deployment
being too expensive against the active memory-reduction work, estimated at
50-100Mi. Measured rather than estimated, the objection does not survive.

Cluster allocatable memory is 96.3 GiB across eight nodes, against 26.6 GiB
of requests — 27.6% committed. The pve5 worker alone carries 61.3 GiB
allocatable at 20% requested, roughly 49 GiB unrequested, and is where a
new low-priority singleton would land. At the namespace default request of
64Mi, Reloader is 0.065% of cluster allocatable.

The informer cache is bounded by data that was also measured, not guessed:
948 KB of Secret data and 341 KB of ConfigMap data cluster-wide, across 74
Secrets and 75 ConfigMaps. A cluster-wide watch on both therefore caches
about 1.3 MB. Reloader's footprint here is Go runtime baseline, not cache,
and it does not scale with anything this cluster is about to do. If it ever
needs to, `resourceLabelSelector` narrows the watch to labelled resources
without touching the RBAC.

## Consequences

A second controller mutates workloads outside git. That is a genuine change
to how this cluster works, and the annotation strategy plus the two ignore
entries are what keep it from being a drift source. The mutation is visible:
`reloader.stakater.com/last-reloaded-from` records which resource caused
each roll, so a restart is attributable rather than mysterious.

Adoption is opt-in per workload via
`secret.reloader.stakater.com/reload: "<name>"`, not `--auto-reload-all`.
A blanket auto-reload would take the RBAC concern above and make every
workload in the cluster a target of it, and would roll workloads on Secret
changes that do not need a roll.

Nine workloads gain rotation that takes effect, but not at the same time:
four on the day the controller lands, and the other five only once the chart
work in the prerequisite above is done. One of the nine — the relay — has
no other mechanism available at all: it is deployed as raw manifests
with no Helm or kustomize render step, so there is no render in which a
checksum could be computed, and every one of its values originates in Vault.
That workload is the reason this is a decision and not a preference.

Verifying the adoption requires an actual Vault rotation, which is a
human-gated action. The check is that the workload rolls and the new value
is in effect in the running process — reading the Secret back proves only
that ESO did its half.

The chart installs no CRDs, so removing it later is deleting a Deployment
and its RBAC. Nothing else in the cluster gains a dependency on it beyond
the annotations, which become inert rather than harmful.
