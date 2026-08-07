# Phase 3 — HPA / VPA / Scale-to-Min Verdicts

Evaluates the four candidates named in the Phase 3 scope against the values now in the repo after Phase 2 (JDWLABS-297 through 301, merged as platform#211-215). This is a **repo-only evaluation** — there is no live cluster access in this session, so nothing below substitutes for a `kubectl top` re-baseline. Every number is labeled by source:

- **trustworthy** — read directly from a merged Phase 2 PR or the current `values.yaml`.
- **unverified** — would need a live re-measurement (`kubectl top`, HPA scaling-event history, sustained CPU utilization over time); flagged rather than guessed at.

The "before/after usage evidence once applied" half of the ticket's deliverable is entirely unverified for the same reason and is called out per-workload below, not skipped silently.

## Finding: VPA is not installed in this cluster

A full-repo search (`tenants/`, `bootstrap/`, `helm-charts/`, everywhere else) for `VerticalPodAutoscaler` / `autoscaling.k8s.io` returns zero matches. No VPA CRDs, no VPA controller Deployment, no chart wiring one in. This is a **finding**, not an implementation detail to route around: any verdict of "VPA" below is a directional recommendation blocked on a cluster-wide controller install (platform-repo infrastructure work, out of scope for a per-app values change), not something implementable this session. Running VPA and HPA against the same object's CPU/memory metric is also a known anti-pattern if both ever land, which is a reason to make that install a deliberate decision rather than a byproduct of this ticket.

## Verdict summary

| Workload | Baseline (2026-07-20) | Post-Phase-2 request | Verdict | Floor / config | Source |
|---|---|---|---|---|---|
| ai-sre/platform-litellm | 512Mi req, 1988Mi used, 1 replica | 2Gi req = 2Gi limit (Guaranteed), cpu 100m req, no cpu limit | **keep-as-is** | n/a | request: trustworthy (platform#214); CPU utilization pattern: unverified |
| ai-sre/platform-holmes-holmes | 2048Mi req, 853Mi used, 1 replica | 1536Mi req = 1536Mi limit (Guaranteed), cpu 100m req | **keep-as-is** | n/a | request: trustworthy (platform#214, itself confirming the Phase 1 hit-list trim); CPU utilization pattern: unverified |
| vault-config-operator/platform-vault-config-operator | 250Mi req, 26Mi used, 1 replica | 128Mi req, 512Mi limit | **scale-to-min-floor** | `replicaCount: 1` (now explicit) | trustworthy (platform#212) |
| jdwlabs-prd/jdwlabs-usersrole-prd | 1024Mi req, 331Mi used, 1 replica | 512Mi req, 2Gi limit | **keep-as-is (already HPA, premise disproved — see below)** | existing HPA: min 1 / max 3, CPU 80% / Mem 80% | trustworthy (deployments repo `charts/usersrole/values.yaml`) |
| jdwlabs-non/jdwlabs-usersrole-non | 1024Mi req, 224Mi used, 1 replica | 512Mi req, 2Gi limit | **keep-as-is (already HPA, premise disproved — see below)** | same HPA (base chart values, no per-env override) | trustworthy (deployments repo) |

No code changes were warranted for litellm, holmes, or usersrole — see reasoning per workload. One change was made: an explicit `replicaCount: 1` on vault-config-operator (see that section).

## ai-sre/platform-litellm — keep-as-is

The ticket flagged this as "worth checking CPU-based HPA given it's an LLM proxy with bursty load." Checked, and the answer is keep-as-is for three reasons, none of which require a fresh measurement to state:

1. **No CPU usage has ever been captured for this workload.** Phase 1's baseline (`01-baseline.md`) and Phase 2's chart inventory are both memory-only — `grep -i cpu` across every file in this directory turns up nothing but an unrelated Longhorn footnote. Setting a `targetCPUUtilizationPercentage` threshold with zero observed CPU data isn't a policy decision, it's a guess dressed as one.
2. **The documented bottleneck isn't compute.** `tenants/platform/services/litellm/values.yaml` documents the whole model list running against zero-cost provider tiers (OpenRouter `:free` at 50 req/day, a separate NVIDIA NIM quota, a local vLLM backstop) — the request volume this proxy will ever see is capped by external free-tier quotas, not by CPU throughput. More replicas do nothing about a quota ceiling shared across all of them via the same provider API keys.
3. **A second replica costs real memory for no proven benefit.** litellm runs Guaranteed QoS (`requests.memory == limits.memory == 2Gi`, deliberately, per the comment already in that file). Scaling out doubles that reservation cluster-wide — directly against this epic's own memory-conservation objective — while doing nothing for the actual constraint above.

Revisit if: the proxy moves off free-tier quotas (paid API credits, meaningfully higher request volume) *and* a live `kubectl top`/Prometheus capture shows sustained CPU pressure. Until then the chart's built-in `autoscaling.enabled` gate (`helm-charts/litellm-helm/templates/hpa.yaml`, off by default) is the mechanism to flip, not something to build first.

## ai-sre/platform-holmes-holmes — keep-as-is

Same absence of CPU data applies here — the upstream chart (pulled and inspected: `robusta-charts/holmes` 0.38.0) ships its own `hpa.yaml` gated by `autoscaling.enabled` (default `false`, `targetCPU: "60"`), so the mechanism exists but has never been fed a real number.

Beyond the missing data, the workload's documented nature doesn't call for horizontal scale-out:

- Holmes is invoked two ways — synchronously by the alert relay's webhook (one investigation at a time, in this single-tenant home cluster) and by an hourly drift-scan CronJob. Neither is a concurrent-request-volume pattern that more replicas would help.
- The one documented "burst" for this workload is a **memory** burst inside a single investigation (tool-heavy runs approaching ~1.5Gi), which Phase 2 already right-sized as a request==limit Guaranteed pod specifically so it isn't the first thing evicted under node pressure. That reasoning is already recorded in `tenants/platform/services/holmes/values.yaml` and doesn't change here.

Revisit only alongside real concurrency evidence (e.g., overlapping investigations queuing or timing out), which nothing in this repo's baseline or Phase 2 findings shows.

## vault-config-operator/platform-vault-config-operator — scale-to-min-floor

This is the one workload where "reconciliation-only, low utilization" from the ticket matches the repo exactly: Phase 2 (platform#212) trimmed the request to 128Mi against ~25Mi measured usage (the most disproportionate ratio recorded cluster-wide), left the 512Mi limit alone, and the chart renders no HPA template at all (confirmed by pulling `redhat-cop/vault-config-operator` v0.8.51 and checking `templates/`) — horizontal scaling was never on the table for this chart.

The floor is the replica count, and the verdict is to confirm it rather than silently rely on the chart default. `replicaCount` was previously unset in this repo's `values.yaml` (falling through to the chart's own default of `1`). Implemented: an explicit `replicaCount: 1`, so the floor is a stated decision in this repo instead of an inherited chart default that a future chart bump could silently change. No HPA/VPA — a reconciler against Vault's API holds no state of its own and gains nothing from more copies of itself.

## jdwlabs-*/usersrole (prd + non) — premise disproved, keep-as-is

**The ticket's premise is wrong and this is worth recording rather than working around.** The ticket (and this repo's own Phase 1 baseline) describe usersrole as effectively single-replica and pitch it as a VPA candidate specifically *because* it looked un-autoscaled. It isn't. `deployments/charts/usersrole/values.yaml` has carried this since the chart's very first commit (`f46c231`, predating this epic entirely — confirmed via `git log -p` on the file, which shows no later commit ever touching the `autoscaling:` block):

```
autoscaling:
  enabled: true
  minReplicas: 1
  maxReplicas: 3
  targetCPUUtilizationPercentage: 80
  targetMemoryUtilizationPercentage: 80
```

Neither `values-non.yaml` nor `values-prd.yaml` overrides this, so both environments already run a live HorizontalPodAutoscaler (rendered by the shared `common` library chart's `_hpa.yaml`) scaling 1→3 replicas on 80% CPU or 80% memory utilization against the request. Phase 1's baseline captured both environments at 1 running replica not because replicas are pinned, but because live usage (224-331Mi against a 1024Mi-then-512Mi request) never got close to the 80% trigger.

Given that:

- **VPA is not additionally warranted** — the workload already has a utilization-based scaling mechanism, and layering VPA's request-rewriting on top of an active HPA targeting the same CPU/memory metrics is a documented anti-pattern (double-adjusting the same signal), not a low-risk addition. It would also hit the same missing-controller blocker as every other VPA verdict in this doc.
- **No new HPA is needed** — one already exists.
- **Keep-as-is** is the correct verdict, but the actionable output of this evaluation is the corrected premise itself: Phase 3 planning for usersrole should stop describing it as unscaled.

What's still genuinely unverified: whether the existing HPA has ever actually fired in production (scaling event history), and whether 80%/80% are sane targets for a JVM app whose slow boot already required a dedicated startupProbe fix (a scale-out event mid-traffic-spike pays the same ~5-minute JVM warmup as a manual restart). Neither is answerable without live cluster access — flagged, not guessed at.

## What remains unverified pending live access

- Before/after usage evidence for every verdict above (the ticket's own deliverable text asks for this "once applied" — nothing here was applied to a live workload, only to repo values, so there is no "after" to measure yet).
- Actual CPU utilization over time for litellm and holmes — the specific gap that keeps both at keep-as-is rather than a CPU-based HPA.
- Whether the usersrole HPA has ever triggered a scale-out event, and how a JVM cold-start behaves under HPA-triggered scale-out versus the startupProbe-covered case Phase 2 already validated.
- Whether metrics-server is still duplicated (Phase 1 hit-list row 9 flagged a platform-chart release alongside a possible Talos-bundled copy, deferred to "decide in 2e"); `tenants/platform/tenant.yaml` still carries one `metrics-server` service entry as of this session, but confirming whether a second live copy exists needs cluster access this session doesn't have. Any future CPU-based HPA on litellm/holmes depends on metrics-server reporting correctly, so this is worth closing out before revisiting either keep-as-is verdict above.
