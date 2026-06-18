# Grafana

Chart `grafana` from `https://grafana-community.github.io/helm-charts` (the
community-maintained chart; the legacy `grafana.github.io/helm-charts` repo
froze at chart 10.5.15 / app 12.3.1 on 2026-01-30). Pinned in
`tenants/platform/tenant.yaml`.

## Dashboards (current model)

Dashboards are provisioned as ConfigMaps (label `grafana_dashboard: "1"`)
discovered by the `kiwigrid/k8s-sidecar` sidecar across services
(`kube-prometheus-stack`, `longhorn`, `argo-cd`, `loki`, `cnpg-operator`).

## Datasources

Datasources (Prometheus, Loki, Alertmanager, Tempo) are provisioned inline in
`values.yaml` under `datasources`. The Tempo datasource carries trace-to-logs /
trace-to-metrics correlation to the Loki and Prometheus UIDs.

## Dashboards-as-code (planned, not in this change)

Migration to Git Sync (dashboards-as-code, GA in Grafana v13) is tracked
separately — see `docs/observability/DASHBOARDS-AND-MULTITENANCY.md`. It needs a
dedicated Grafana GitHub App whose key is staged in Vault `kv/grafana-gitsync`
and surfaced via ExternalSecret; that wiring lands in a follow-up PR once the
App exists. The ConfigMap/sidecar model above remains in effect until then.
