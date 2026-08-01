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
cluster-admin-equivalent escalation path. `watchGlobally=false` does not
help — the affected workloads span seven namespaces. The mitigation is to
replace the ClusterRole and ClusterRoleBinding with namespaced Roles and
RoleBindings in exactly those namespaces, accepting that new namespaces then
need an explicit grant. This is the one part of the decision that is a
judgement call rather than a measurement, and it should be made
deliberately.

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

Nine workloads gain rotation that takes effect. One of them — the relay —
has no other mechanism available at all: it is deployed as raw manifests
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
