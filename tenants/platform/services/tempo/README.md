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
- **Datasource:** the `Tempo` datasource (with trace-to-logs / trace-to-metrics
  correlation to the Loki and Prometheus UIDs) is provisioned inline in
  `services/grafana/values.yaml`, matching how the other datasources are
  declared in this repo (the Grafana datasources sidecar is not used).

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

No pilot service is currently selected (the original candidate was
decommissioned before instrumentation started). When picking the next one,
prefer a service that is org-owned (SDK instrumentation under our control),
fans out to multiple backends (latency/error attribution across hops is the
trace use case), and carries real traffic — `ai-sre-relay` is the leading
candidate.

Pilot exit criteria: spans visible in Grafana Explore (Tempo), service graph
rendering the pilot -> downstream backends, and one click from a slow span to
its Loki logs and to the RED metrics panel.
