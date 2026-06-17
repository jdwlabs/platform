// EXAMPLE Grafonnet entrypoint (illustrative skeleton).
//
// Emits one file per dashboard into observability/dashboards/<tenant>/.
// Run with:  jsonnet -m ../dashboards main.jsonnet
//
// The kubernetes-mixin's grafanaDashboards would be merged in here the same way
// to generate the platform-folder cluster/node/namespace/workload dashboards.
local red = import 'lib/red.libsonnet';

local tenants = ['jdwlabs', 'dotablaze-tech'];

{
  // -m mode keys are output file paths relative to the output dir.
  ['%s/%s-services-red.json' % [tenant, tenant]]: red.red(tenant)
  for tenant in tenants
}
