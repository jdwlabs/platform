// EXAMPLE Grafonnet RED dashboard template (illustrative skeleton).
//
// Parameterised so one library produces a per-tenant RED dashboard from
// (tenant, folderUid). Datasources are referenced via a template variable, so
// the compiled JSON works against whichever (possibly tenant-scoped) datasource
// Grafana resolves at view time.
//
// Compile via main.jsonnet -> emits observability/dashboards/<tenant>/...json
local g = import 'github.com/grafana/grafonnet/main.libsonnet';

local dashboard = g.dashboard;
local var = g.dashboard.variable;
local ts = g.panel.timeSeries;
local prometheus = g.query.prometheus;

{
  // red(tenant): build a Services RED dashboard scoped to one tenant's namespaces.
  red(tenant):
    local nsSelector = 'namespace=~"$namespace"';
    dashboard.new('%s / Services RED' % tenant)
    + dashboard.withUid('%s-services-red' % tenant)
    + dashboard.withTags(['tenant:%s' % tenant, 'red', 'generated'])
    + dashboard.withVariables([
      var.datasource.new('datasource', 'prometheus'),
      var.query.new('namespace')
      + var.query.withDatasourceFromVariable(self.variables[0])
      + var.query.queryTypes.withLabelValues(
          'namespace',
          'kube_namespace_labels{label_platform_jdwlabs_io_tenant="%s"}' % tenant,
        )
      + var.query.selectionOptions.withIncludeAll(true)
      + var.query.selectionOptions.withMulti(true),
    ])
    + dashboard.withPanels([
      ts.new('Rate (req/s by service)')
      + ts.gridPos.withW(12) + ts.gridPos.withH(8) + ts.gridPos.withX(0) + ts.gridPos.withY(0)
      + ts.queryOptions.withTargets([
        prometheus.new('${datasource}',
          'sum by (service) (rate(http_requests_total{%s}[5m]))' % nsSelector)
        + prometheus.withLegendFormat('{{service}}'),
      ]),

      ts.new('Errors (5xx ratio)')
      + ts.gridPos.withW(12) + ts.gridPos.withH(8) + ts.gridPos.withX(12) + ts.gridPos.withY(0)
      + ts.queryOptions.withTargets([
        prometheus.new('${datasource}',
          'sum by (service) (rate(http_requests_total{%s,code=~"5.."}[5m])) / sum by (service) (rate(http_requests_total{%s}[5m]))' % [nsSelector, nsSelector])
        + prometheus.withLegendFormat('{{service}}'),
      ]),

      ts.new('Duration (p95 latency)')
      + ts.gridPos.withW(24) + ts.gridPos.withH(8) + ts.gridPos.withX(0) + ts.gridPos.withY(8)
      + ts.queryOptions.withTargets([
        prometheus.new('${datasource}',
          'histogram_quantile(0.95, sum by (service, le) (rate(http_request_duration_seconds_bucket{%s}[5m])))' % nsSelector)
        + prometheus.withLegendFormat('{{service}} p95'),
      ]),
    ]),
}
