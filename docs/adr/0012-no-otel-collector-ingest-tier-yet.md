# ADR: No OpenTelemetry Collector ingest tier at current scale

Status: accepted, no change deployed. Re-affirms the earlier design note on
fresh evidence, and replaces its prose triggers with measurable ones.

## Problem

`docs/OTEL-COLLECTOR-EVAL.md` recommended against a standalone OpenTelemetry
Collector and listed four triggers to revisit: tail-based sampling, central
PII scrubbing, a second backend, and non-OTLP producers. Those triggers are
qualitative. Nobody can look at the cluster and say whether one has fired,
so the decision has no gate — it only has a memory of a decision.

This record re-runs the question against live state and replaces those
triggers with thresholds that can be evaluated from Prometheus.

The revisit also has to answer something the original note did not. The
strongest argument for a collector in this cluster is no longer sampling or
fan-out; it is that a collector is the only place a tenant identity can be
stamped onto telemetry that the emitting workload cannot forge. That
argument is examined below and is the reason one of the new triggers exists.

## Current ingest topology, verified live 2026-08-04

Three planes, one owner per signal, exactly as `COLLECTION-OWNERSHIP.md`
decided. No OpenTelemetry Collector exists anywhere in the four repos.

| Signal | Producer | Path | Measured volume |
|---|---|---|---|
| Metrics | Prometheus Operator scrape | 30 jobs, no intermediary | 6,321 samples/s, 288,511 active series |
| Logs | `alloy-logs` DaemonSet (8) + `alloy-singleton` | Alloy to Loki push API | 33.5 lines/s, 13.2 KB/s |
| Traces | application SDK | OTLP gRPC direct to Tempo `:4317` | 0.204 spans/s |

`alloy-metrics` is disabled, so Alloy touches logs and cluster events only.
Tempo's metrics generator remote-writes span and service-graph metrics back
into Prometheus, which is why the remote-write receiver stays enabled.

Zero targets are down. No pod in `monitoring` is BestEffort — every one is
Burstable. The thirteen BestEffort pods cluster-wide are all `kube-proxy`
and Longhorn engine-image DaemonSets, neither of which this decision
touches.

### The trace plane has exactly one producer

A cluster-wide scan of every Deployment, StatefulSet and DaemonSet for
`OTEL_*` environment variables returns one workload:

```
jdwlabs-non/Deployment/jdwlabs-servicediscovery-non [servicediscovery]
  OTEL_EXPORTER_OTLP_ENDPOINT = http://platform-tempo.monitoring.svc.cluster.local:4317
  OTEL_EXPORTER_OTLP_PROTOCOL = grpc
  OTEL_SERVICE_NAME           = servicediscovery
total workloads with OTEL_* env: 1
```

Tempo agrees. Querying its own tag index for every service that has ever
sent it a span returns a single value, `servicediscovery`, and the span
metrics in Prometheus carry that one `service` label.

That workload runs in the **non-production** environment only. The
production values file for the same chart carries no OTEL keys at all. So
production emits no traces, and the entire trace plane is one non-prod
service producing about one span every five seconds.

A collector's whole value proposition is fan-in, fan-out, and central
processing. With one producer and one backend there is nothing to fan.

## Tempo is no longer dark — both halves of that finding are fixed

The 2026-07-26 state of this stack was that Tempo received zero spans and
that the condition was unalertable because a threshold rule over a
non-existent series never fires. Neither is true today, and the decision
had to be re-taken on that basis rather than on the stale finding.

Spans are flowing, and have been continuously:

```
tempo_distributor_spans_received_total{tenant="single-tenant"}   71460
tempo_distributor_bytes_received_total{tenant="single-tenant"}   51338799
tempo_receiver_accepted_spans{receiver="otlp/otlp_receiver"}     71460
tempo_receiver_refused_spans{receiver="otlp/otlp_receiver"}      0

sum(rate(tempo_distributor_spans_received_total[1h]))    = 0.2011
sum(rate(tempo_distributor_spans_received_total[24h]))   = 0.2042
sum(increase(tempo_distributor_spans_received_total[7d])) = 71461
```

The 1h and 24h rates agree to three significant figures and the 7d increase
accounts for the entire lifetime counter, so this is a steady trickle rather
than a burst followed by silence. The Tempo pod has been up 14.5 days.
Nothing is being refused.

The alerting gap is closed too. `TempoNoSpansReceived` now exists in
`kube-prometheus-stack/postInstall/rules-tempo.yaml` and is one of only
three rules in the entire 199-rule set built on `absent()`:

```
tempo | TempoNoSpansReceived | inactive |
  sum(rate(tempo_distributor_traces_per_batch_count[5m])) == 0
    or absent(tempo_distributor_traces_per_batch_count)
```

Its `inactive` state is itself evidence: were the series missing, the
`absent()` arm would evaluate true and the rule would be pending or firing.

This matters to the decision rather than being background. A collector was
never a fix for a dark pipeline, and proposing one while the pipeline was
dark would have been treating an ingest topology as a substitute for an
alert. The pipeline is lit and watched; the question is now purely one of
whether the topology earns its keep.

## Memory is not the constraint it was — the failure domain is

The premise this ticket inherited was that control-plane nodes run about
3.98Gi at 90-110% memory on a fragile quorum, and that anything added must
be weighed against that. That is no longer the shape of the cluster:

```
talos-6iz-oey  control-plane  cap=6038408Ki  alloc=4887432Ki  requests 978Mi (20%)
talos-fow-vbk  control-plane  cap=6038416Ki  alloc=4887440Ki  requests 978Mi (20%)
talos-oam-s4g  control-plane  cap=6038416Ki  alloc=4887440Ki  requests 1746Mi (36%)
talos-lx0-6a4  worker         cap=65815440Ki alloc=64254864Ki
talos-2qd-v0u  worker         cap=3980168Ki  alloc=2419592Ki
talos-g1i-e3h  worker         cap=3980168Ki  alloc=2419592Ki
talos-k3y-y3e  worker         cap=3980168Ki  alloc=2419592Ki
```

Control planes were resized and now sit at 20-36% of allocatable requested
and 48-57% live usage. The 3.98Gi figure now describes three small
*workers*, not the control planes. The monitoring stack is drained onto
`talos-lx0-6a4`, which has 64Gi allocatable and is 15% used.

So the honest statement is that a gateway collector would fit. A single
replica sized to survive `memory_limiter` — say 128Mi requested against a
512Mi ceiling with `limit_mib: 400` and `spike_limit_mib: 100` — is noise
against 64Gi of headroom. Two replicas for availability is still noise.
Even the tail-sampling case is small: at the volume that would justify it,
a 30s decision window holds well under a megabyte of spans.

**Rejecting this on memory would be rejecting it on a stale number.** The
cost that actually decides it is structural, not resource:

- **A new failure domain in the only path that carries traces.** Today an
  app talks to Tempo. With a gateway there are two hops and two processes
  that can silently drop. This cluster has already lost a signal to exactly
  that shape of fault.
- **A single-replica gateway is a single point of failure**; a
  multi-replica one is a load-balancing problem the moment any stateful
  processor is added, per the scaling constraint below.
- **Upgrade and configuration surface** for a component whose only current
  job would be to forward one service's spans unmodified.

The placement caveat is real if this is ever built: the collector must
carry explicit requests and limits in Helm values — never a live patch,
which ArgoCD's selfHeal reverts — and must be pinned alongside the
monitoring stack. Landing it by default on one of the three 2.42Gi workers
would put a memory-hungry buffering process on the tightest nodes in the
cluster.

## The scaling constraint that makes "adopt for tail sampling" misleading

Tail-based sampling is the headline trigger in the original note, stated as
though it were a matter of turning on a processor. Upstream's own scaling
guidance is that the tail-sampling processor holds traces in memory and is
therefore stateful: if replicas are scaled out naively, different replicas
receive different spans of the same trace, each makes an independent
sampling decision, and the result is traces missing spans. The prescribed
fix is a *second* tier — collectors running the load-balancing exporter,
hashing by `traceID`, in front of the collectors running the processor.

The same hazard applies to span-to-metrics aggregation, which this cluster
already gets from Tempo's metrics generator for free.

So the tail-sampling trigger does not buy one Deployment. It buys a
two-tier topology in front of a backend that currently ingests a fifth of a
span per second. That is the reason the volume threshold below is set where
it is rather than at the first sign of trace growth.

## Options considered

**Adopt a gateway Collector now.** Rejected. There is one producer, one
backend, no non-OTLP source, and no identified redaction requirement. Every
capability a collector would add is either already provided (batching,
retry and k8s enrichment for logs by Alloy; RED metrics by Tempo's
generator) or has nothing to act on. It would add a hop to the only signal
path that has just been recovered from a dark state.

**Adopt an agent DaemonSet.** Rejected, and more firmly than the gateway.
Alloy already owns node-local collection on all eight nodes at roughly
1.95Gi of aggregate working set. A second per-node agent duplicates that
footprint to serve one non-prod workload.

**Adopt a minimal gateway now purely to make later adoption a no-op.**
Rejected. The drop-in property the original note wanted is already secured
by the app emitting OTLP, and the change when a trigger fires is one
environment variable per workload. Standing up an idle forwarding tier to
preserve an ability that costs one string edit is paying maintenance now
for no benefit.

**Keep direct OTLP to Tempo and gate on measurable triggers.** Chosen.

## Decision

Do not deploy an OpenTelemetry Collector. Keep application SDKs exporting
OTLP directly to Tempo, keep Alloy as the owner of logs and cluster events,
and keep Prometheus scraping metrics with no intermediary.

Revisit when **any one** of the following is true, each stated so it can be
checked rather than argued:

**T1 — trace retention stops being affordable.** Tempo holds 168h on a 20Gi
volume. Seven days of ingest is currently 51.3 MB, which is 0.25% of that
volume. The trigger is a sustained span rate whose 7-day footprint exceeds
roughly 70% of the volume — about 275x today's rate, or **≈55 spans/s
sustained over 7 days**. Below that line the cheaper answers are head
sampling, which the SDK already honours through `OTEL_TRACES_SAMPLER`, and
raising the volume. Above it, keeping everything is no longer an option and
tail sampling starts to pay for its two-tier topology.

**T2 — a second telemetry backend exists.** Any second trace or metric sink
— a managed Grafana Cloud export, an external APM — at which point fan-out
belongs in one config rather than in every workload's environment.

**T3 — a producer that cannot emit OTLP.** A workload speaking only Jaeger,
Zipkin, or a vendor protocol. Tempo's Jaeger and Zipkin receivers are
deliberately switched off today; enabling them is the cheaper first move,
and only a format Tempo cannot receive at all fires this trigger.

**T4 — telemetry must be redacted before it is stored.** Any span or log
attribute that must not reach Tempo or Loki. Scrubbing in each SDK is a
per-workload promise; scrubbing in a gateway is a chokepoint.

**T5 — tenant identity must be enforced rather than trusted.** This is the
trigger the original note did not have, and it is the one most likely to
fire first, because per-tenant observability is already scoped work.

That work wires Loki and Tempo native multi-tenancy with `X-Scope-OrgID`
keyed off the namespace tenant label. Neither backend has it on today:
Tempo has no `multitenancyEnabled` key, and Loki runs `auth_enabled:
false`. With one trace producer, the header can simply be set in that
workload's values, and a collector is not needed to deliver per-tenant
observability. Nothing in this record blocks that rollout or requires a
collector for it.

But a header set by the workload is a header the workload chooses. A
tenant that can set `X-Scope-OrgID` can set another tenant's value, and the
isolation becomes cooperative rather than enforced. If the requirement
hardens from "each tenant sees its own data" to "a tenant cannot write into
another tenant's stream", that needs a boundary the workload cannot cross:
a gateway that derives tenancy from Kubernetes metadata via `k8sattributes`
and overwrites any client-supplied header. That is a genuine collector-only
capability, and it should be recorded as the trigger it is rather than
discovered late.

The distinction to hold onto: **per-tenant routing does not need a
collector; non-forgeable per-tenant routing does.**

## How adoption would be proven to carry data

This is a precondition of adoption, not a follow-up task, and it exists
because of a fault this stack has already suffered. A bare `host:port` in
`OTEL_EXPORTER_OTLP_ENDPOINT` parses the host as the URL scheme, yielding
an empty endpoint with TLS implicitly on. Spans stop. Every liveness and
readiness probe stays green, because the exporter is misconfigured, not
unhealthy. Health is not evidence of flow, and a collector inserts a second
process capable of failing in precisely that silent way.

The application side is already hardened: `servicediscovery`'s tracing
setup validates an explicit `http`/`https` scheme and logs
`Tracing disabled` rather than starting a dead exporter. The collector side
would need equivalent guarantees, established in this order:

1. **Conservation across the hop, not health of the hop.** The decisive
   check is that what leaves the collector arrives at Tempo:
   `rate(otelcol_exporter_sent_spans)` must track
   `rate(tempo_distributor_spans_received_total)`. A collector that accepts
   spans and drops them on export is Running, Ready, and useless.
2. **Read the refusal and failure counters, never the totals alone.**
   `otelcol_receiver_refused_spans` and
   `otelcol_exporter_send_failed_spans` must be zero;
   `otelcol_processor_dropped_spans` must be zero. `memory_limiter`
   refusing data is the documented signal that the tier is undersized, and
   it is invisible if only accepted-span counts are watched.
3. **`absent()`-shaped alerts on the new hop, merged before the collector
   is put in the path.** A threshold rule over a series that stops existing
   never fires — that is the same gap that left Tempo dark and unalertable.
   `TempoNoSpansReceived` is the correct template and should be cloned onto
   the collector's receive and export counters. Ordering matters: ArgoCD
   runs prune and selfHeal on all applications, so merging the collector is
   deploying it. The alerts must land first, or the tier goes live
   unwatched.
4. **Cut over one workload and diff the counters.** With a single producer
   this is cheap: repoint it, then confirm Tempo's received-span rate is
   unchanged across the switch. A drop to zero that no probe reports is the
   exact failure being guarded against.
5. **A synthetic trace canary is what distinguishes a broken pipeline from
   a quiet one.** `sum(rate(...)) == 0` cannot tell "the collector is
   dropping everything" from "nobody sent anything", and with one non-prod
   producer the trace plane goes quiet whenever that one service is idle.
   Loki already has `loki-canary` running on five nodes doing exactly this
   job for logs. There is no trace equivalent.

Point 5 generalises past this decision. At the current scale the
highest-value change to the trace pipeline is not an ingest tier — it is a
canary that continuously proves the path end to end and gives the existing
`absent()` alert a signal that means something when the application is
idle. That is cheaper than a collector, addresses the failure mode this
cluster has actually experienced, and stays useful whichever way T1 through
T5 eventually resolve.

## Consequences

**The trigger list is now falsifiable.** T1 is a Prometheus query, T2
through T4 are yes/no facts about what exists, and T5 is a stated security
property. This record can be checked rather than re-argued from memory.

**One capability is knowingly deferred.** Until a collector exists, tenant
isolation on telemetry is cooperative: it depends on each workload sending
an honest `X-Scope-OrgID`. That is acceptable while every producer is
first-party and there is one of them, and it is written down here so that
it is a decision rather than an oversight.

**No change is deployed by this record.** Nothing is added to any tenant's
service list, no values file changes, and no ArgoCD Application is created
or modified. The cluster after this ADR is byte-identical to the cluster
before it.

**The earlier design note is superseded on two points, not replaced.** Its
recommendation stands. Its qualitative triggers are replaced by T1-T5, and
its topology description is stale where it says `alloy-metrics`
remote-writes to Prometheus — that was disabled by the collection-ownership
decision.

## Non-goals

This record does not decide whether to enable Tempo or Loki multi-tenancy,
which is scoped elsewhere and does not require a collector. It does not
address control-plane metrics scraping, which is a Prometheus scrape-target
question with no ingest-tier component and is blocked on a machine-config
apply. It does not propose a trace canary — it identifies one as higher
value than the subject of this record, which is a finding for the backlog
rather than a decision taken here.
