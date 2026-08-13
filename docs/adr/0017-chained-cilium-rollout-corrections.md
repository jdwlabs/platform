# ADR: Chained Cilium — corrections found when implementation started

Status: accepted. Extends
[networkpolicy-enforcement-via-chained-cilium](0012-networkpolicy-enforcement-via-chained-cilium.md)
and [chained-cilium-rollout-sizing-and-proof](0013-chained-cilium-rollout-sizing-and-proof.md).
The choice of chained Cilium is settled in the first and is not reopened. The
sizing, the `cni.exclusive` hazard and the proof method in the second all
stand. What this record replaces is that record's **rollout sequence**, steps
6 through 11, which describes a mechanism that does not exist.

## Problem

The sizing record's runbook was written on 2026-08-04 and implementation began
on 2026-08-13. Re-deriving its premises against live state before touching
anything found that three of them had moved and one of its mechanisms was
never real. Two of those are in the repo's own history, landed by later
commits than the record itself, which is how a nine-day-old runbook came to
describe a cluster that no longer exists.

## Finding 1 — there is no per-namespace audit mode

The sizing record's decision 2 reads "enforcement is enabled per namespace by
removing audit, so at every moment the blast radius is one namespace and the
rollback is one flag." Steps 8, 9, 10 and 11 each instruct the operator to
remove audit mode for a named set of namespaces, and each names re-enabling it
as the rollback.

Cilium has no such control. `policyAuditMode` renders a single
`policy-audit-mode` key into the `cilium-config` ConfigMap, read by every
agent on every node. The chained-Cilium record describes it correctly as a
"daemon-wide `policyAuditMode`"; the sizing record extends that record and
does not carry the word through. Removing audit is one cluster-wide flag, and
the eleven-step sequence built on staging it per namespace cannot be executed.

## Finding 2 — the per-namespace mechanism exists, but it is not audit

`3bc3e46 fix(tenant-envelope): make netpol enforce opt-in, not opt-out` landed
after the sizing record and supplies what that record assumed Cilium would.
`helm-charts/tenant-envelope/templates/network-policies.yaml` now gates
`allow-all-ingress` and `allow-all-egress` on a per-namespace `enforce` flag:

```
{{- $enforce := eq .enforce true }}
...
{{- if or (eq $policy "ci") (eq $policy "platform") (not $enforce) }}
```

A namespace isolates only once its entry in `tenants/<tenant>/tenant.yaml`
sets `enforce: true`. No namespace does, in any of the three tenants. The
staged rollout the sizing record wanted is therefore available — it just
operates on the policy set rather than on the engine, and it lives in this
repo rather than in Cilium's configuration.

That inverts the blast radius of the audit flag. Because every namespace
currently renders an allow-all pair, turning daemon-wide audit off enforces
140 policies that collectively permit everything. It is a no-op by
construction, on every namespace at once, which makes it the safest possible
way to prove the engine works rather than the most dangerous.

## Finding 3 — the breakage the sizing record proved is already fixed

That record's central static result was that enforcement would sever every
stateful tenant workload from Postgres, because the `default` tier allowed
egress on port 53 and nothing else. Its step 6 was to close that gap.

`530034b feat(tenant-envelope): close proven egress gaps in the default netpol
tier` did so. `allow-database-monitoring-egress` is live in all four workload
namespaces, permitting `database:5432` and `monitoring:4317`. Step 6 is
complete and needs no separate execution.

The rest of that record's breakage table is moot for a different reason: with
`enforce` defaulting off, the six namespaces it identified as genuinely
isolating no longer are. None of the four tenant workload namespaces is
isolated today, and the record's warning that the rollout must not start with
them describes a risk that only returns when someone sets `enforce: true`.

## Finding 4 — the numbers moved

Measured live 2026-08-13, against the same reads the sizing record used:

| Premise | 2026-08-04 | 2026-08-13 |
|---|---|---|
| NetworkPolicy objects / namespaces | 115 / 24 | 140 / 25 |
| `default-deny-all` objects | 24 | 25 |
| Allow policies (empty selector, with rules) | 84 | 108 |
| Free request budget, `talos-k3y-y3e` | 323Mi | 425Mi (1938Mi of 2363Mi committed) |
| Kubernetes | v1.35.1 | v1.36.3 |
| Talos | v1.13.4 | v1.13.4 |
| `LimitRange` in `kube-system` | absent | absent |
| Policy engine present | none | none |

The sizing conclusion survives its own premise moving: 192Mi still fits inside
the tightest node's budget, now with 233Mi of margin instead of 131Mi. The
`kube-system` `LimitRange` is still absent, so an unsized install would still
be BestEffort, and Talos would still kill it first.

## Finding 5 — the Kubernetes bump forces the Cilium version

Cilium's compatibility list is per-release and exhaustive: "older Kubernetes
versions not listed here do not have Cilium support." The v1.19 branch lists
1.31 through 1.34. The v1.20 branch lists 1.33 through 1.36.

At v1.36.3 the cluster is outside every Cilium release before 1.20. This is
not a preference for the newest chart — 1.20.0 is the only released line that
covers this cluster at all, and the Kubernetes upgrade that created that
constraint landed after the sizing record was written.

## Finding 6 — `cni.exclusive` cannot bite the configuration it was warned about

The sizing record devotes its first section to `cni.exclusive`, and states
that "it is not a tuning preference. It is the difference between a policy
engine and a cluster-wide outage." The mechanism it describes is real: at
`true` the agent renames every non-Cilium file in `/etc/cni/net.d` to
`*.cilium_bak` and re-asserts that through an fsnotify watcher, which against
Flannel deletes the conflist of the CNI being chained onto.

It cannot happen in the install that record specifies. Chart 1.20.0 emits the
key only for a non-chained install:

```
{{- if and (not .Values.cni.customConf) .Values.cni.install }}
  write-cni-conf-when-ready: {{ .Values.cni.hostConfDirMountPath }}/05-cilium.conflist
  cni-exclusive: {{ .Values.cni.exclusive | quote }}
```

A chained install sets `cni.customConf: true`, so `cni-exclusive` never
reaches `cilium-config` whatever the value in the values file. The agent's own
default is `false` (`daemon/cmd/cni/config/config.go`), so the absent key
resolves safely rather than dangerously:

```go
CNIChainingMode:       "none",
CNILogFile:            "/var/run/cilium/cilium-cni.log",
CNIExclusive:          false,
```

Rendering this repo's values against chart 1.20.0 confirms it: `custom-cni-conf`,
`cni-chaining-mode`, `read-cni-conf` and `write-cni-conf-when-ready` are all
present and `cni-exclusive` is absent.

The value stays set in `tenants/platform/services/cilium/values.yaml`. It
costs nothing, it states the intent, and it is already correct if someone
later flips `customConf`. What changes is the weight the sizing record puts on
it: the hazard belongs to the quickstart install that record was warning
people away from, not to the chained install it prescribes. Treating it as the
single most dangerous line makes it the thing that gets checked, which is a
poor use of the attention — `cni.customConf` and the ConfigMap's `name` field
are the values that actually decide whether this works.

## Decision

**1. The rollout is five steps, not eleven.** Steps 1 through 5 of the sizing
record — land the chart unsynced, verify the rendered manifest, sync and check
scheduling before health, restart every pod and assert the count, observe for
a minimum of seven days — are unchanged and remain the substance of the work.
Its steps 6 through 11 are replaced by:

- **6. Correct the allow-set against the audit log.** Unchanged in intent from
  the sizing record's step 7. Its step 6 is already merged.
- **7. Turn daemon-wide audit off, once.** A no-op while every namespace
  carries an allow-all pair, and the first end-to-end proof that the
  enforcement path works. Rollback is the same single flag.
- **8. Isolate one namespace at a time with `enforce: true`.** `jdwlabs-non`
  first, then `dotablaze-tech-non`, then the two `prd` namespaces, then the
  two `ci` runner namespaces. Rollback is reverting one line of
  `tenant.yaml`. `kube-system` is a platform-tier namespace and carries
  `allow-all` unconditionally, so it is never opted in by this sequence — the
  sizing record's "kube-system last" warning is satisfied by never doing it
  at all.

The proof method is unchanged and still binding: the negative test in the
sizing record's "Proving isolation" section, run before and after, with the
permitted paths demonstrated still working in the same run.

**2. Cilium 1.20.0**, being the only release line that supports Kubernetes
v1.36.

**3. The chained configuration ships as a hand-written ConfigMap**, per the
sizing record and upstream's generic-veth documentation, rather than the
chart's `cni.chainingTarget`. See below.

## The chainingTarget alternative, and why not yet

Chart 1.20.0 offers `cni.chainingTarget`, which the sizing record does not
mention and which addresses the specific hazard that record flags — that a
hand-written ConfigMap must reproduce Flannel's conflist verbatim, and a
mismatched `name` field produces a conflist CNI will not match. Set to the
Flannel network name, the agent watches for that network and derives its
chained configuration from whatever Flannel is actually serving, so the
mismatch cannot happen and the config cannot go stale. It implies
`generic-veth` on its own:

```
{{- else if (not (kindIs "invalid" .Values.cni.chainingTarget)) -}}
  {{- $cniChainingMode = "generic-veth" -}}
```

It is not taken here because upstream's generic-veth page still documents the
ConfigMap, this cluster's Flannel conflist is short, static and readable
directly from `kube-flannel-cfg`, and the first install of a policy engine is
the wrong place to also be the first user of a less-trodden path. The
staleness risk it removes is real but slow: the conflist changes only when
Flannel's version does.

It becomes the better option the moment the ConfigMap is observed to have
drifted from `kube-flannel-cfg`, and that comparison is worth making at every
Flannel bump.

## Consequences

**`kube-system` is now out of the rollout entirely**, where the sizing record
had it as the final and sharpest step. It carries the `platform` tier, which
renders `allow-all` unconditionally, so no `enforce` flag reaches it. The
namespace holding CoreDNS and Flannel is protected by the tier assignment
rather than by rollout ordering — a stronger guarantee than a step at the end
of a list, and one that is easy to lose by editing a tier.

**Two records now describe the same rollout differently.** The sizing record
is not amended, because records here are append-only. Anyone reading it for
the rollout gets a sequence that cannot be executed, and only reaches this
correction by following the reference forward. That is the cost of
append-only, and it is paid every time a record is extended rather than
edited.

**The ADR numbering defect the sizing record raised is closed.** That record
found two records at 0011 on `main` and observed that a CI check rejecting a
duplicate numeric prefix under `docs/adr/` would fix the mechanism where
renumbering fixes only an instance. `tools/check-adr-numbering.py` now does
exactly that, gated by the `adr-numbering` job in `validate.yml`, and it
reports 17 records with no duplicate prefixes with this one added. Allocation
is still by reading the directory — the check does not hand out numbers, it
rejects collisions — so two records written concurrently still race, but the
race now fails a required check instead of landing.

**Nothing alerts on a DaemonSet whose desired and ready counts diverge**, as
that record already recorded. A `Pending` agent on one node still means that
node silently enforces nothing, and the increased headroom on
`talos-k3y-y3e` reduces the odds without changing the failure mode.
