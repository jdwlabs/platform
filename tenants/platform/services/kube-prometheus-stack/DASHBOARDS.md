# Grafana Dashboard Reference

Dashboards are provisioned as ConfigMaps with label `grafana_dashboard: "1"` and auto-discovered by the Grafana sidecar.
Each ConfigMap contains the full JSON exported from Grafana.com.

## Dashboards

| Dashboard                 | ID    | Location                                                         | Source                                       |
|---------------------------|-------|------------------------------------------------------------------|----------------------------------------------|
| Kubernetes / Views / Pods | 15760 | `kube-prometheus-stack/postInstall/dashboard-k8s-overview.yaml`  | https://grafana.com/grafana/dashboards/15760 |
| Node Exporter Full        | 1860  | `kube-prometheus-stack/postInstall/dashboard-node-exporter.yaml` | https://grafana.com/grafana/dashboards/1860  |
| ArgoCD                    | 14584 | `argo-cd/postInstall/dashboard-argocd.yaml`                      | https://grafana.com/grafana/dashboards/14584 |
| ingress-nginx             | 9614  | `ingress-nginx/postInstall/dashboard-ingress-nginx.yaml`         | https://grafana.com/grafana/dashboards/9614  |
| Longhorn                  | 16888 | `longhron/postInstall/dashboard-longhorn.yaml`                   | https://grafana.com/grafana/dashboards/16888 |
| CNPG                      | 20417 | `cnpg-operator/postInstall/dashboard-cnpg.yaml`                  | https://grafana.com/grafana/dashboards/20417 |
| Loki                      | 13407 | `loki/postInstall/dashboard-loki.yaml`                           | https://grafana.com/grafana/dashboards/13407 |

## Updating a Dashboard

1. Visit the Grafana.com link above and check fro newer revisions
2. Download the latest JSON:
   `curl -sL "https://grafana.com/api/dashboards/{ID}/revisions/latest/download" > /tmp/dashboard.json`
3. Replace the JSON content in the corresponding ConfigMap YAML (indented under `data.<filename>: |`)
4. Commit and push - ArgoCD will reconcile the ConfigMap and Grafana's sidecar will reload it

## Adding a New Dashboard

1. Find the dashboard on https://grafana.com/grafana/dashboards/
2. Download the JSON:
   `curl -sL "https://grafana.com/api/dashboards/{ID}/revisions/latest/download" > /tmp/dashboard.json`
3. Create a ConfigMap in the relevant service's `postInstall/` directory: 
   ```yaml 
   apiVersion: v1 
   kind: ConfigMap
   metadata:
      name: dashboard-<name>
      namespace: <service-namespace>
      labels:
         grafana_dashboard: "1"
   data:
      <name>.json: |
         <paste JSON here, indented 4 spaces>
   ```
4. Ensure the service has `postInstall: true` in `tenant.yaml`
5. Add the dashboard to this table
