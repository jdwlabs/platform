# OpenTelemetry Collector evaluation

**Status:** design note / not implemented
**Scope:** evaluate the OpenTelemetry Collector as the *standard ingest path*
feeding Prometheus, Loki, and Tempo, given the current observability stack.

## Context

The cluster runs an LGTM-shaped stack:

- **Metrics** — kube-prometheus-stack (Prometheus), plus `alloy-metrics` from the
  k8s-monitoring chart remote-writing to it.
- **Logs** — Loki, fed by `alloy-logs` (DaemonSet) and `alloy-singleton` from the
  k8s-monitoring chart.
- **Traces** — Tempo (new), ingesting OTLP directly from instrumented apps; its
  metrics generator remote-writes span/service-graph metrics to Prometheus.

So today there are effectively **two** collection planes already: Grafana Alloy
(metrics + logs, via k8s-monitoring) and direct OTLP-to-Tempo (traces). There is
no standalone OpenTelemetry Collector.

## The question

Should we introduce the OpenTelemetry Collector as a single, vendor-neutral
ingest tier that all three signals flow through, i.e.

```
apps / kubelet / kube-state ──► OTel Collector ──► Prometheus
                                              ├──► Loki
                                              └──► Tempo
```

## Analysis

### What the Collector would buy us

- **One ingest contract.** Workloads emit OTLP for all three signals; the
  Collector owns fan-out, batching, retry, and backpressure. Apps stop caring
  about backend topology.
- **Central processing.** Tail-based sampling for traces, attribute
  scrubbing/PII redaction, k8s metadata enrichment, and cost-control filtering
  all live in one config instead of being smeared across SDKs and Alloy.
- **Vendor neutrality.** Swapping or adding a backend becomes an exporter change,
  not an app or agent change.

### What it costs us right now

- **Alloy is already the collector.** Grafana Alloy *is* an OTel-compatible
  collector distribution. The k8s-monitoring chart already does k8s metadata
  enrichment, batching, and remote-write for metrics and logs. Dropping a second
  collector tier in front duplicates capability we already pay for.
- **A third moving part.** A Collector tier (gateway Deployment + optional
  agent DaemonSet) is more pods, more memory, more upgrade surface, and another
  failure domain in the path between every app and every backend. The recent
  CI/Vault cascade is a reminder that added links in critical chains have a real
  blast radius.
- **Marginal trace benefit today.** Tempo already accepts OTLP directly and runs
  its own metrics generator. The strongest Collector-only feature —
  tail-based sampling — is not justified until trace volume is high enough to
  need it. The single pilot service does not need it.
- **Overlap with the v4 direction.** The k8s-monitoring v3 chart is in
  maintenance; v4 is the future and leans further into Alloy as the unified
  collector. Standing up a separate OTel Collector now risks building toward an
  architecture the upstream chart is moving away from.

## Recommendation

**Do not adopt a standalone OpenTelemetry Collector now. Keep it as the target
architecture, contingent on scale.**

Near term (this stack, current scale):

1. Keep Alloy (k8s-monitoring) as the collector for metrics and logs.
2. Let instrumented apps emit OTLP **directly to Tempo** for traces, as wired in
   the Tempo workstream. This keeps the trace path short and the pilot simple.
3. Standardize all app instrumentation on **OTLP** regardless of backend. This is
   the one decision that makes a future Collector tier a drop-in: point the SDK
   `OTEL_EXPORTER_OTLP_ENDPOINT` at a Collector service instead of Tempo, and
   nothing else in the app changes.

Adopt a Collector gateway later **when at least one** of these is true:

- Trace volume justifies **tail-based sampling** (cost or Tempo ingest pressure).
- We need **central PII/attribute scrubbing** before data hits any backend.
- We add a **second tracing/metrics backend** (e.g. a managed Grafana Cloud
  export) and want fan-out decoupled from apps.
- A workload can only export **non-OTLP** formats and we want to normalize at the
  edge rather than per-app.

When that trigger fires, deploy the Collector as a **gateway Deployment** (not a
per-node DaemonSet — Alloy already owns node-local log/metric collection) sitting
between OTLP producers and the three backends, and repoint app SDKs at it. The
Alloy metrics/logs planes can remain as-is or be folded in incrementally.

### Dependency on B/D/E outcomes

- **D (logs uplift):** done here — Alloy stays the log collector with a tighter
  memory cap; nothing about that pushes toward a separate Collector.
- **E (tracing):** Tempo's direct-OTLP ingest is sufficient for launch and the
  pilot, which is what removes the urgency for a Collector tier.
- **B (metrics, separate workstream):** if B converges on a second metrics
  backend or remote-write fan-out, that is the most likely trigger to revisit
  this and stand up the gateway.
