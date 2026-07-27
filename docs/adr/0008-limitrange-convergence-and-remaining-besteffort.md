# ADR: Correcting the BestEffort claim in 0005, and closing the rest

Status: accepted, implemented. Corrects
[0005](0005-bounding-containers-charts-cannot-reach.md); does not supersede it.

## Problem

ADR 0005's Consequences section states:

> After this change the BestEffort half is fully met.

The paragraph immediately below it says the opposite, correctly:

> A LimitRange defaults containers only at admission, so the effect on
> longhorn-system and nginx-gateway arrives as pods are recreated, not at
> merge time.

Both cannot be true at merge time, and the optimistic one is the sentence a
reader reaches first. A live scan confirms the second is the accurate one,
and also turns up two BestEffort workloads that 0005 never accounted for at
all — neither in its three-class table nor in its accepted-risk register.

The mechanism 0005 chose is not in question. It works, and the live cluster
proves it on exactly the workload class the ADR could not reach through Helm
values: the `ei-a4d05f02` Longhorn engine-image pods, created after the
`longhorn-defaults` LimitRange existed, are Burstable. The five older
`ei-75a03ec3` pods, all predating it, are still BestEffort. That is the
LimitRange behaving as documented, not failing.

## What the live scan actually found

Every BestEffort pod in the cluster, by cause:

| Workload | Count | Cause | Reachable? |
|---|---|---|---|
| `kube-proxy` | 8 | Talos-managed, deliberately unbounded | register in 0005 |
| Longhorn `engine-image-ei-75a03ec3-*` | 5 | pods predate the LimitRange | converges (below) |
| `vault-auto-unseal-*` | 3 | this repo's own manifest, never sized | **yes** |
| `postgres-backup-*` | 3 | this repo's own manifest, never sized | **yes** |

The last two rows are the finding. Both are raw manifests under
`tenants/platform/services/*/postInstall/` — no chart gap, no LimitRange
needed, nothing out of reach. They were simply missed, because the scans that
produced 0005 looked for containers Helm values could not reach and these
were never Helm-rendered to begin with.

`postgres-backup` is the more instructive of the two: its unbounded container
is the `dump` **init** container, the same class of omission 0005 was written
to correct.

## Decision

**1. The two reachable workloads get requests, and only requests.**
`vault-auto-unseal` and both `postgres-backup` containers now carry
`resources.requests` with no limits, following 0005's own reasoning: a
missing request makes a container BestEffort and evicted first; a missing
limit lets it starve neighbours. These two workloads are ones where a
ceiling is the wrong lever — an OOM kill of the unseal job leaves the
platform without secrets, and an OOM kill of `pg_dumpall` silently costs a
backup.

The requests deliberately carry more headroom than 0005's "roughly 1.5×
observed" convention, and it is worth recording why rather than leaving the
deviation silent. Seven days of `container_memory_working_set_bytes` put
`vault-auto-unseal` at a 6.1Mi peak over ~230 samples and `postgres-backup`'s
`upload` at 18Mi over ~120, both tight distributions. The `dump` init
container has no usable measurement at all: it completes in about a second,
which is shorter than the scrape interval, so the 13 samples that exist
across a week of runs are arbitrary snapshots rather than peaks. Its number
is therefore a headroom judgement, not a measured fit — sized against a 46MB
cluster dump that `pg_dumpall` streams rather than buffers, with room for the
databases to grow. Since these are requests on jobs that live seconds, the
cost of over-reserving is a rounding error against the cost of guessing low
on the one container nothing can observe.

**2. The five `ei-75a03ec3` pods are recorded as converging, not fixed.**
They are not an accepted risk in the sense 0005 uses the term — no decision
was made to leave them unbounded. They are pods a LimitRange cannot
retroactively touch, and they disappear when the v1.11.1 engine image is
retired. That retirement is not free and does not belong to this work:
`refCount` is 68, all 14 volumes still run v1.11.1, and clearing them
requires a live per-volume engine migration followed by an explicit delete of
the `EngineImage` CR, which Longhorn does not garbage-collect on its own. Two
of those volumes are the orphaned `data-platform-vault-1`/`-2` PVCs, whose
removal belongs to the Vault raft migration runbook.

Sequencing, preconditions and abort criteria are in
`docs/longhorn-engine-upgrade.md`.

**3. The hygiene metric is stated per half, and one half gets longer.** The
epic's "zero BestEffort or memory-unbounded workloads" is two claims, not
one. Collapsing them into a single verdict is precisely how 0005's summary
came to contradict its own next paragraph, so they are read separately here:

*BestEffort.* `kube-proxy` ×8 is an accepted risk recorded in 0005 and will
not change. Longhorn `ei-75a03ec3` ×5 converges when the engine image is
retired. Everything else is met once this change lands — 19 BestEffort pods
in the live scan, 6 of them the two workloads above.

*Memory-unbounded.* Met only under 0005's "explicitly recorded accepted-risk
list" clause, and Decision 1 lengthens that list rather than shortening it.
Shipping requests without limits puts `vault-auto-unseal` and both
`postgres-backup` containers alongside `kube-flannel`, `kube-proxy`,
`install-config` and Longhorn `instance-manager` as unbounded by choice. The
register entry is Decision 1's own reasoning, and the trigger to revisit is
the same one 0005 set for `instance-manager`: a node observed under memory
pressure attributable to these workloads growing, rather than to the volume
of data they happen to be moving.

## Consequences

Forcing the five stragglers now would mean rolling the storage data plane to
apply a scheduling hint, on volumes serving live workloads, ahead of a
migration that has its own runbook. That trade is worse than the exposure:
these pods hold no state, and their eviction under memory pressure costs a
restart, not data.

The general lesson is narrower than 0005's and worth stating separately: a
scan scoped by *mechanism* ("containers Helm values cannot reach") will miss
workloads that never went through that mechanism. The scan that found these
two was scoped by *symptom* — every pod whose `status.qosClass` is
BestEffort — and that is the scope any future check should use.
