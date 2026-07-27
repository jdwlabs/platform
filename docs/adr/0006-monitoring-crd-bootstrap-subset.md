# ADR: Keep the wave -1 monitoring CRD subset in foundation-crds.yaml

Status: accepted, implemented.

## Problem

`bootstrap/crds/foundation-crds.yaml` vendors 11 CRDs at sync-wave -1: 8
Gateway API CRDs and 3 prometheus-operator CRDs (`ServiceMonitor`,
`PodMonitor`, `PrometheusRule`). The kube-prometheus-stack chart (wave 2)
installs its own copy of those same 3 CRDs as part of its normal chart
install. That raised the question of whether the hand-vendored subset is
still pulling its weight, or whether letting the chart own them outright —
removing the duplication instead of just re-scoping which ArgoCD Application
applies the vendored copy — would be simpler.

## Decision

Keep the 3-CRD subset. It is load-bearing, not legacy: `tenants/platform/tenant.yaml`
places `cert-manager`, `nginx-gateway-fabric`, and `longhorn` at `syncWave: 1`,
each shipping a `ServiceMonitor` or `PodMonitor` in its `postInstall/`
manifests (`tenants/platform/services/{cert-manager,nginx-gateway-fabric,longhorn}/postInstall/`).
`kube-prometheus-stack` itself does not sync until `syncWave: 2`. Without the
wave -1 CRDs, those wave-1 `postInstall` manifests would fail to apply on a
fresh bootstrap — the CRD they instantiate wouldn't exist yet.

This is the same design already documented inline at
`helm-charts/tenant-envelope/templates/services-appset.yaml:67-93`: the
`ignoreDifferences` block on these 3 CRDs exists precisely so the vendored
copy and the chart's copy can coexist without ArgoCD fighting itself over
which one wins.

What changed as a result of this review is not the subset's existence but
its ownership and freshness:

- Single owner: `bootstrap`'s recursive sweep now excludes `crds/**`
  (`bootstrap/root-app.yaml`), leaving `platform-crds` (`bootstrap/00-crds.yaml`)
  as the only Application that applies these CRDs. Previously both applied
  the same objects under the same field manager — harmless only because the
  content was byte-identical, which stopped being guaranteed the moment either
  side drifted.
- Freshness: `tools/sync-monitoring-crds.py` ties the vendored 3 CRDs to the
  `kube-prometheus-stack` chart revision pinned in `tenants/platform/tenant.yaml`,
  run in CI (`.github/workflows/validate.yml`, `foundation-crds-freshness` job)
  in check mode so a Renovate chart bump that isn't matched by a bundle
  regeneration fails the PR instead of silently aging out.

## Non-goals

- Not moving the Gateway API CRDs out of `foundation-crds.yaml` — they have
  no equivalent chart-owned duplicate today and are out of scope for this
  review.
- Not changing `ignoreDifferences` — it is what keeps the split working and
  removing it would reintroduce the SSA fight this subset already avoids.
