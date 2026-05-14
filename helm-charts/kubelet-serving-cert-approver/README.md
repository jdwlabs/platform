# kubelet-serving-cert-approver

Self-contained Helm chart that deploys [`alex1989hu/kubelet-serving-cert-approver`](https://github.com/alex1989hu/kubelet-serving-cert-approver) with two platform-specific patches baked in:

1. **Container security context** — `runAsUser`/`runAsGroup` pinned to `65534` so pods do not require root to satisfy the namespace PodSecurity admission.
2. **Probe `initialDelaySeconds: 30`** — prevents CrashLoopBackOff during node-cold-start when the certificate manager is racing to come up alongside kubelet.

Previously these were applied as manual `kubectl patch` commands documented in
`docs/BOOTSTRAP.md` Phase 8. They are now expressed declaratively in the
chart's Deployment template.

## Source

Templates derive from `deploy/standalone-install.yaml` at the upstream tag
listed in `Chart.yaml`'s `appVersion`. To bump the upstream version:

```bash
# 1. Update Chart.yaml appVersion and values.yaml image.tag
# 2. Re-vendor templates from the new tag:
curl -fsSL https://raw.githubusercontent.com/alex1989hu/kubelet-serving-cert-approver/<NEW_TAG>/deploy/standalone-install.yaml
# 3. Diff against existing templates/, port only the upstream-driven changes
# 4. Run: helm lint helm-charts/kubelet-serving-cert-approver
# 5. Run: helm template helm-charts/kubelet-serving-cert-approver | kubeconform -ignore-missing-schemas
```

## Healer

`platformctl bootstrap heal --cert-approver` (introduced in Plan 3) re-applies
this chart in case the cluster drifts.
