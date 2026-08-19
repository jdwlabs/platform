# Observability Collection Ownership

Status: **decided** (2026-07). This document records which release owns each
observability signal in the `monitoring` namespace, why, and what was removed
to get to a single owner per signal.

## The problem

Three releases overlapped in the `monitoring` namespace:

| Release | Chart | What it did before |
|---|---|---|
| `platform-kube-prometheus-stack` | `kube-prometheus-stack` | Prometheus + Alertmanager + operator, node-exporter, kube-state-metrics, ServiceMonitor/PodMonitor/PrometheusRule discovery, bundled Grafana (disabled) |
| `platform-monitoring` | `k8s-monitoring` (Grafana Alloy) | Pod logs + cluster events to Loki, **and** its own scrapes of kubelet, cAdvisor and kubelet `/metrics/resource`, remote-written into the same Prometheus; deployed a windows-exporter DaemonSet |
| `platform-grafana` | `grafana` (standalone) | Dashboards (sidecar + Git Sync), datasources, UI |

Consequences of the double collection path:

- kubelet and cAdvisor series were ingested twice (once via the
  kube-prometheus-stack kubelet ServiceMonitor, once via Alloy remote-write
  under `job="integrations/kubernetes/*"`), inflating Prometheus TSDB series
  count and RSS — the dominant memory pressure on this cluster.
- Alloy's copies bypassed the cAdvisor cardinality drop relabelings maintained
  in the kube-prometheus-stack values, so the "dropped" high-cardinality
  series came back in through the side door.
- A prior vault double-scrape incident was a symptom of the same ambiguity:
  with two collectors running, every new target risks being picked up twice.
- The windows-exporter DaemonSet (a `clusterMetrics` subchart default) can
  never schedule on this Linux-only Talos cluster — dead objects.

## Decision

One owner per signal:

| Signal | Owner | Mechanism |
|---|---|---|
| Metrics scraping | `kube-prometheus-stack` | Prometheus Operator; ServiceMonitors/PodMonitors discovered cluster-wide (`*SelectorNilUsesHelmValues: false`) |
| Recording/alerting rules | `kube-prometheus-stack` | Chart `defaultRules` + `PrometheusRule` manifests in `tenants/platform/services/kube-prometheus-stack/postInstall/` |
| Alerting delivery | `kube-prometheus-stack` | Alertmanager (config via ExternalSecret) |
| Pod logs | `k8s-monitoring` (Alloy) | `alloy-logs` DaemonSet → Loki |
| Node/kernel logs | Talos (push) → `k8s-monitoring` (Alloy) | Talos streams its kernel ring buffer to a hostPort listener on the `alloy-logs` DaemonSet → Loki (`job="integrations/talos/kernel"`) |
| Log-derived alerting rules | `loki` | Rule ConfigMaps in `tenants/platform/services/loki/postInstall/`, delivered by the chart's rules sidecar to the Loki ruler → Alertmanager |
| Cluster events | `k8s-monitoring` (Alloy) | `alloy-singleton` → Loki (`job="integrations/kubernetes/eventhandler"`, referenced by the Loki retention/selector config) |
| Traces | apps → Tempo directly (OTLP); Tempo metrics-generator remote-writes RED metrics to Prometheus | `tempo` release |
| Dashboards / UI | standalone `grafana` | Sidecar ConfigMap discovery + Git Sync |

### Why kube-prometheus-stack for metrics (not Alloy)

- All scrape intent in this repo is already expressed as
  ServiceMonitor/PodMonitor manifests (cert-manager, vault, longhorn,
  cnpg-operator, nginx-gateway-fabric) and chart-managed monitors (loki,
  tempo, kube-state-metrics, node-exporter). Alloy's operator-objects feature
  was never enabled — it only duplicated kubelet/cAdvisor.
- Rules and Alertmanager routing are Prometheus-Operator-native; moving
  collection to Alloy would strand ~9 PrometheusRule manifests and the
  alert pipeline behind a second translation layer.
- Cardinality controls (cAdvisor/kube-state-metrics drop relabelings) live in
  the kube-prometheus-stack values and only apply on its scrape path.

### Why node logs arrive by push, not by scrape

The `nodeLogs` feature of `k8s-monitoring` reads systemd journal files off the
node. Talos runs no journald, so that feature has nothing to read here and
stays disabled — enabling it would mount an always-empty path and report
nothing wrong while doing it. Talos keeps kernel output in the ring buffer and
exposes it two ways: over its own API (`talosctl dmesg`, which needs a
talosconfig and so cannot be a collector's data source) and as a push to a
remote endpoint named on the kernel command line. The push is the only one a
collector can consume unattended, which inverts the usual direction: the node
dials the collector.

Because those lines carry no hostname of their own, the listener has to be on
the node that produced them for a `node` label to be truthful. That is why the
receiver is a hostPort on the per-node `alloy-logs` DaemonSet rather than a
Service in front of all of them, and why the Talos side points at loopback.

### Why log rules live with `loki`, not with `kube-prometheus-stack`

Alerting rules are otherwise owned by `kube-prometheus-stack` (see the table
above), and that stays true for everything expressed in PromQL. A LogQL rule
cannot be: `PrometheusRule` objects are read by the Prometheus Operator, which
has no way to hand a LogQL expression to the Loki ruler. The Loki chart's own
rules sidecar is the only delivery path, so those rule files are owned by the
`loki` release. Delivery of the resulting alert is still shared — the Loki
ruler posts to the same Alertmanager, so a log-derived alert routes by
`severity` exactly like a metric-derived one.

### Why the standalone Grafana (not the bundled one)

The bundled Grafana was already `enabled: false` in the kube-prometheus-stack
values. The standalone release carries all real state: admin credentials via
ExternalSecret, datasources (Prometheus/Loki/Alertmanager/Tempo with
trace-to-logs/metrics correlation), the dashboard sidecar, persistence, the
HTTPRoute, and the Git Sync (dashboards-as-code) setup. Nothing references the
bundled instance. The standalone release stays; the bundled one stays off.

## What changed to get here

In `tenants/platform/services/monitoring/values.yaml`:

- `clusterMetrics.enabled: false` — removes Alloy's kubelet, cAdvisor and
  kubelet-resource scrapes and the windows-exporter DaemonSet.
- `alloy-metrics.enabled: false` — the metrics collector StatefulSet has
  nothing left to collect. (Its pve5 `workload.jdwlabs.io/monitoring` node
  affinity went with it; Prometheus and Grafana keep theirs.)
- The `Prometheus` remote-write destination was removed; `selfReporting` is
  disabled because it requires a metrics destination.
- `storage-capacity-rules.yaml` moved to
  `tenants/platform/services/kube-prometheus-stack/postInstall/rules-storage-capacity.yaml`.
  It sat at the top level of the `monitoring` service directory, which is not
  a source path for any ArgoCD Application (`postInstall: false` and not in a
  `postInstall/` dir), so the `PVCUsageHigh` alert was silently never
  installed. It now lands with the other kube-prometheus-stack rules.

Prometheus keeps `enableRemoteWriteReceiver: true`: the Tempo
metrics-generator still remote-writes span/service-graph metrics.

## What deliberately did NOT change

- **Log shipping** (`alloy-logs`, pod logs → Loki on TrueNAS-NFS) and
  **cluster events** are untouched.
- **CRD ownership**: the Prometheus Operator CRDs are still applied both by
  `bootstrap/crds/foundation-crds.yaml` (operator v0.91.0, wave -1) and by the
  kube-prometheus-stack chart's bundled `crds/` (older schema). The three
  CRD `ignoreDifferences` entries in
  `helm-charts/tenant-envelope/templates/services-appset.yaml` are therefore
  **still required**. Removing them needs kube-prometheus-stack
  `crds.enabled: false` — but with `automated.prune: true` on the service
  Applications, dropping the CRDs from the chart's desired state risks ArgoCD
  pruning the CRDs themselves (which cascades to every
  ServiceMonitor/PodMonitor/PrometheusRule/Prometheus/Alertmanager in the
  cluster) depending on which Application currently holds the tracking
  annotation. That migration must be its own change with explicit prune
  protection, ideally folded into the monitoring stack uplift, which should
  target only the surviving stack described here.
