# db-ui

Minimal in-house chart for [Adminer](https://github.com/vrana/adminer), the
single-container database admin UI served at `dbui.jdwlabs.com`.

## Why in-house

The service previously pulled the full upstream `adminer` chart
(`charts.ectobit.com`) with an empty values file. That rendered a BestEffort,
unbounded pod (observed spiking to ~600Mi) plus a ServiceAccount and ingress
machinery the platform does not use. Adminer needs exactly one Deployment and
one ClusterIP Service; the platform HTTPRoute already lives in the service's
`postInstall/` directory.

## What it renders

- `Deployment/db-ui` — `adminer:<tag>`, port 8080 (`http`), HTTP liveness and
  readiness probes, memory requests/limits (Burstable QoS by construction),
  hardened container securityContext.
- `Service/db-ui` — ClusterIP, port 80 → `http`.

No ServiceAccount is created: Adminer never talks to the Kubernetes API, so
the pod runs under the namespace default ServiceAccount.

## Values

See [values.yaml](values.yaml). Resource bounds and the securityContext
rationale are documented inline there. Platform overrides live in
`tenants/platform/services/db-ui/values.yaml`.
