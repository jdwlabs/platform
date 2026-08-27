# truenas-csi

Thin GitOps wrapper around [truenas/truenas-csi](https://github.com/truenas/truenas-csi) —
the official iXsystems CSI driver, which speaks TrueNAS's WebSocket API
(`wss://<host>/api/current`), the one that survives the REST removal in
TrueNAS 26. Upstream ships only a raw manifest (`deploy/truenas-csi-driver.yaml`)
or an OpenShift operator; this chart re-templates that manifest (at the
`Chart.yaml` `appVersion` tag) so it fits this repo's Helm + ArgoCD model.

Deployed via `chartPath` from `tenants/platform/tenant.yaml`, in parallel with
democratic-csi — disjoint CSIDriver name (`csi.truenas.io`), namespace,
credential path and dataset parents. Decision record:
`docs/adr/0029-truenas-csi-evaluated-as-democratic-csi-replacement.md`.
PoC/migration procedure: `docs/truenas-csi-migration-poc.md`.

Deliberate departures from the upstream manifest, each annotated where it
lives:

- **No Namespace, no Secret.** The namespace comes from the tenant envelope;
  the API key Secret is rendered by ESO from Vault path `kv/truenas-csi-driver`
  (`tenants/platform/services/truenas-csi/postInstall/external-secret.yaml`).
- **Images digest-pinned** (upstream deploys `:latest`).
- **iSCSI config hostPath redirected** off `/etc/iscsi` (type `Directory`),
  which fails Talos's hostPath type check — see `node.iscsiDirHostPath`.
- **Snapshotter sidecar off by default** — the cluster has no
  snapshot-controller or `snapshot.storage.k8s.io` CRDs yet.
- **Controller liveness probe toggleable** — same probe shape that caused
  restart churn on democratic-csi during transient NAS outages.

Pool and dataset parents are per-StorageClass (`pool`/`datasetPath`
parameters), not chart config; the PoC classes live under
`tenants/platform/services/truenas-csi/postInstall/`.
