# litellm-helm (vendored)

Vendored copy of upstream `oci://docker.litellm.ai/berriai/litellm-helm`
version **0.1.2** (chart contents identical except the patch below).

## Why vendored

The upstream chart hardcodes the `db-ready` init container image as
`docker.io/bitnami/postgresql:16.1.0-debian-11-r20` in
`templates/deployment.yaml`. Bitnami purged versioned tags from the
`bitnami` Docker Hub org, so that reference no longer resolves and the
pod is stuck in `Init:ErrImagePull`. The chart's own `db.dbReadyImage` /
`db.dbReadyTag` values exist but are never referenced by any template —
in every published version through 0.1.100 — so a values-only override
cannot fix it.

## Local patches

- `templates/deployment.yaml`: `db-ready` init container image is now
  templated from `db.dbReadyImage` + `db.dbReadyTag` (default preserves
  the original tag). Tenant values point these at `bitnamilegacy`.

## Upgrading

Diff a freshly pulled upstream chart against this directory, re-apply
the patch above, and check whether upstream has started honoring
`dbReadyImage` (in which case drop the vendoring and pin the chart
again in `tenants/platform/tenant.yaml`).
