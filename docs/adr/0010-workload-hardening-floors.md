# ADR: Where workload hardening stops — CPU ceilings and the UID floor

Status: accepted, implemented.

## Problem

Two Trivy misconfiguration rules had accumulated open code-scanning alerts
with no owner and no disposition, because neither has an answer that is
obviously right:

- `KSV-0011` ("CPU not limited") — 17 alerts. Every workload in this repo
  sets CPU and memory *requests* and a memory *limit*, and none sets a CPU
  limit. The rule reads that consistent shape as 17 separate omissions.
- `KSV-0020` / `KSV-0021` ("runs with UID/GID <= 10000") — 24 alerts across
  10 workloads, including the two stateful ones.

Both were deliberately left open through an earlier triage pass rather than
dismissed, on the grounds that "the scanner is noisy" is not a disposition.
This record supplies the disposition. A third, narrower case —
`postgres-backup` declining a memory ceiling — is already documented in the
manifest itself and is restated here only because it belongs to the same
question.

## Decision 1: requests on everything, memory ceilings, no CPU ceiling

Every workload gets CPU and memory requests plus a memory limit. No workload
gets a CPU limit.

The requests are not optional. A container with no requests makes its whole
pod BestEffort, and pod QoS is computed across init containers too, so a
single unbounded init container drops the pod its neighbours live in. That
class is invisible to the scheduler and is evicted first under node memory
pressure — a failure this cluster has hit more than once.

The two ceilings are not symmetric, which is why only one of them is applied:

- A memory limit is a real stop. Memory is incompressible: a container that
  wants more than exists takes the node's other pods down with it, so
  OOM-killing the one container is the better outcome.
- A CPU limit is not a stop, it is a throttle. The CFS quota applies even
  when the node is otherwise idle, so a periodic job that briefly wants a
  burst is slowed for no one's benefit. CPU is compressible — contention
  degrades gracefully on its own, and requests already establish the
  scheduling floor and the contention share.

`KSV-0011` is therefore dismissed as "won't fix" wherever it fires. It is not
a false positive: the rule correctly observes there is no CPU limit. It is
disagreed with.

### The exception, restated

`postgres-backup` declines a memory ceiling as well, and keeps requests only.
`pg_dumpall`'s footprint scales with the largest table it streams, so a
ceiling converts an OOM kill into a lost backup rather than capping anything
worth capping. The reasoning lives in the manifest beside the field.

## Decision 2: the UID floor is not adopted

`runAsUser`/`runAsGroup` above 10000 is not required, and the alerts are
dismissed as "won't fix".

The rule guards against a container UID colliding with a meaningful host or
peer identity. That threat needs a shared UID namespace to land. These
workloads run in dedicated per-tenant namespaces under Pod Security
Standards, mount no `hostPath`, and share no UID namespace with the host, so
the numeric value buys nothing on top of the controls already in place.

Acting on it would also be least safe exactly where it matters most.
`redis:7.4-alpine` and `postgres:17` own their data directories at uid 999.
Overriding the UID on either risks a volume-ownership failure on the two
stateful workloads in the set — trading a theoretical exposure for a real
outage.

What *is* required, and is applied everywhere the image supports it:
`allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]`,
`seccompProfile: RuntimeDefault`, and `runAsNonRoot` with
`readOnlyRootFilesystem` where the image was verified to tolerate them.

## Consequences

- The dismissals are recorded against each alert with the reasoning, so a
  future reader finds an argument rather than an absence. They are
  reversible: reopening an alert restores it to the queue.
- These rules will fire again on any new workload. That is intended — the
  disposition is per-rule policy, not a suppression, so a new manifest still
  surfaces the question and gets answered against this record.
- If the cluster ever gains multi-tenant workloads that share a UID
  namespace, or hostPath mounts, Decision 2 stops holding and should be
  revisited rather than inherited.

## Non-goals

- Not suppressing either rule in `.trivyignore`. Suppression at source is
  reserved for checks that cannot produce signal against this repository at
  all; these two can, and the answer is a judgement rather than a defect in
  the check.
- Not revisiting the `securityContext` work already applied to the workloads
  that could take it — that shipped separately and is not in question here.
