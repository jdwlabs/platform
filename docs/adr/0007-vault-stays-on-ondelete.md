# ADR: Vault stays on `OnDelete`; make the skew visible instead

Status: accepted, implemented.

## Problem

On 2026-07-26 the Vault chart went `0.30.1 → 0.34.0`, carrying image
`1.20.1 → 2.0.3` — a major version bump. ArgoCD applied it and immediately
reported Synced + Healthy. The pod kept serving `1.20.1` for the next
47 minutes, until something deleted it. During the restart Vault was sealed
and not Ready for roughly 70 seconds, and a health sample taken inside that
window still reported 49/49 apps Synced+Healthy.

Nothing was broken — Vault self-recovered via the 2-minute auto-unseal
CronJob. What was broken is that no tool could tell the difference between a
Vault serving the version we shipped and a Vault serving the previous one,
or between an unsealed Vault and a sealed one.

The `vault` StatefulSet uses `updateStrategy: OnDelete`, which is the
`hashicorp/vault` chart's own default — it is not something this repo set.
Under `OnDelete` the controller records a new `updateRevision` but never
rolls pods; an operator deletes them deliberately. That raised the question
of whether the strategy itself was the mistake, given Vault is currently a
single replica whose unseal is automated anyway.

## Options considered

**Move to `RollingUpdate`.** Removes the applied-but-not-running window
outright: ArgoCD applies, the controller rolls, and the auto-unseal CronJob
picks the pod back up within two minutes. Attractive precisely because the
single-replica topology makes the roll trivial — there is no quorum to lose.

Rejected on the grounds that the single-replica topology is the temporary
state, not the steady one. The raft HA migration restores three replicas,
and at three replicas an automatic roll is materially worse: each pod comes
back sealed and must be unsealed and rejoined before the next is taken down.
Handing that sequencing to the StatefulSet controller, whose only ordering
guarantee is ordinal and whose only readiness signal is a probe that cannot
distinguish "sealed" from "starting", is how a rolling upgrade turns into a
lost quorum. Choosing `RollingUpdate` now would mean choosing it back at the
exact moment it becomes dangerous, which is the worst possible time to
revisit a rollout strategy.

**Keep `OnDelete`, fix the blindness.** Chosen. `OnDelete` is the correct
strategy for an unseal-gated stateful service and is what upstream ships.
The failure here was never the rollout mechanism — it was that three
independent blind spots lined up so an un-adopted revision was invisible:

1. ArgoCD's StatefulSet health check treats `OnDelete` as healthy
   unconditionally, so revision skew never surfaces there.
2. `.spec.updateStrategy` sits in the tenant `ignoreDifferences`, so a git
   diff does not show it either.
3. `platformctl cluster status`'s Vault check was phase-based — it asserted
   the pod was `Running`, which a sealed, not-Ready Vault satisfies.

## Decision

Keep `updateStrategy: OnDelete` on the Vault StatefulSet, at the chart
default, and close the detection gap instead:

1. The `vault-pod` check asserts readiness and unsealed state via
   `/v1/sys/health`, which distinguishes unsealed-active (200) from sealed
   (503), rather than asserting pod phase.
2. A generic `statefulset-revisions` check flags any StatefulSet whose pods
   have not adopted the applied revision, so a pending `OnDelete` roll is
   visible across the whole cluster rather than only where someone thought
   to look.

Adopting a pending revision stays a deliberate operator action, documented
in `docs/OPERATIONS.md` §5: delete pods one at a time, verify health between
each, and expect a seal after each delete until auto-unseal catches up.

## Implementation note worth keeping

The obvious way to detect a pending roll is to compare
`.status.currentRevision` against `.status.updateRevision`. That is wrong for
`OnDelete` specifically — the controller never advances `currentRevision`
under this strategy, even once every pod is running `updateRevision`. A check
built that way reports a permanent, unclearable warning on exactly the
StatefulSets this decision is about, which trains readers to ignore the
signal and leaves the cluster worse off than the original blindness.

The check therefore compares each pod's `controller-revision-hash` label
against the StatefulSet's `updateRevision`. This was caught by verifying the
check's output against the live cluster rather than trusting that it passed.

## Consequences

Revision skew is now a first-class, cluster-wide signal rather than a
Vault-specific anecdote — it covers all StatefulSets, so the next OnDelete
service inherits the detection for free.

The applied-but-not-running window still exists by design. What changed is
that it is now reported. An operator seeing `N/M pending roll` is being told
a shipped change is not yet running, not that something is broken.

The raft HA migration should re-check this decision only if it changes the
unseal mechanism — the reasoning above depends on pods coming back sealed,
not on the replica count as such.
