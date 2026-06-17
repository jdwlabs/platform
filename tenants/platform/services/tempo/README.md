# Tempo (distributed tracing)

Single-binary [Grafana Tempo](https://grafana.com/docs/tempo/) deployment that
provides the **T** in the cluster's LGTM stack (metrics + logs were already in
place; this adds tracing).

## What is deployed

- **Chart:** `tempo` (single-binary mode) from `grafana-community/helm-charts`.
  The copy in `grafana/helm-charts` is deprecated as of early 2026.
- **Ingest:** OTLP only — gRPC `4317`, HTTP `4318`. Other receivers (Jaeger,
  Zipkin) are intentionally off.
- **Storage:** local filesystem on a 20Gi Longhorn PVC, 7-day retention.
- **Metrics generator:** enabled, remote-writing RED span metrics and
  service-graph metrics to Prometheus. This powers the Grafana service graph,
  trace-to-metrics, and exemplars.
- **Datasource:** provisioned via `postInstall/datasource.yaml` with
  trace-to-logs (Loki) and trace-to-metrics (Prometheus) correlation. See the
  coordination note in that file — the Grafana datasources sidecar must be
  enabled for it to load.

## OTLP endpoints

From any pod in the cluster:

| Protocol  | Endpoint                          |
|-----------|-----------------------------------|
| OTLP gRPC | `platform-tempo.monitoring:4317`  |
| OTLP HTTP | `http://platform-tempo.monitoring:4318` |

## How a service emits traces

Tracing is opt-in per workload. Two paths, pick one:

### 1. OTel SDK (preferred for app-owned spans)

Instrument the app with the OpenTelemetry SDK for its language and point the
exporter at the OTLP gRPC endpoint. The only required wiring is environment:

```yaml
env:
  - name: OTEL_EXPORTER_OTLP_ENDPOINT
    value: "http://platform-tempo.monitoring:4317"
  - name: OTEL_EXPORTER_OTLP_PROTOCOL
    value: "grpc"
  - name: OTEL_SERVICE_NAME
    value: "<service-name>"
  - name: OTEL_RESOURCE_ATTRIBUTES
    value: "k8s.namespace.name=<ns>,k8s.pod.name=$(POD_NAME)"
```

The resource attributes above feed the trace-to-logs tag mapping in the Tempo
datasource, so spans link cleanly to their Loki log lines.

### 2. Zero-code auto-instrumentation

For JVM/Node/Python services that can't be code-changed yet, attach the OTel
auto-instrumentation agent via an init container + `JAVA_TOOL_OPTIONS` /
`NODE_OPTIONS` / `PYTHONPATH`, exporting to the same OTLP endpoint. This is the
fastest way to get spans out of an existing service for the pilot.

## Pilot proposal

**Pilot service: `openclaw` (namespace `jdwlabs-ai`, `ai.jdwlabs.com`).**

Rationale:
- Org-owned application code (helm-charts/openclaw), so SDK instrumentation is
  fully under our control — no upstream dependency.
- It is an agent runtime that fans out to multiple LLM/API backends; latency and
  error attribution across those hops is exactly the problem traces solve.
- Public-facing with real traffic, so the trace-to-logs / trace-to-metrics
  correlation gets exercised end to end.

Pilot exit criteria: spans visible in Grafana Explore (Tempo), service graph
rendering openclaw -> downstream backends, and one click from a slow span to its
Loki logs and to the RED metrics panel.
