# Runbook: Gateway API CRD bundle v1.1.0 → v1.6.1

Status: PLANNED — nothing in this runbook has been applied. Every cluster
mutation below is executed by a human. Agents may run the read-only
inspection commands (`kubectl get`, `--raw` discovery, `--dry-run=server`)
but not the applies or patches.

Scope: taking the vendored Gateway API CRDs in
`bootstrap/crds/foundation-crds.yaml` from **v1.1.0, experimental channel**
to **v1.6.1**, and — as part of the same decision — moving off the
experimental channel onto **standard**. Out of scope: the NGINX Gateway
Fabric chart itself (already at 2.7.0), the 3 prometheus-operator CRDs that
share the same bundle file, and any new Gateway API feature adoption.

Two claim types are kept separate throughout. **Verified** means measured
against this cluster or parsed out of the upstream release artifact on
2026-09-08. **Upstream** means read from a sigs.k8s.io/gateway-api changelog
and not independently reproduced here.

## Why

The cluster runs Gateway API CRDs from bundle v1.1.0. NGINX Gateway Fabric
2.7.0 recommends v1.6.1 — five minor versions ahead — and says so out loud:

```
$ kubectl get gatewayclass nginx -o yaml   # verified 2026-09-08
  Accepted          True   Accepted            The GatewayClass is accepted
  SupportedVersion  False  UnsupportedVersion  The Gateway API CRD versions are not
                                               recommended. Recommended version is v1.6.1
  ResolvedRefs      True   ResolvedRefs        The ParametersRef resource is resolved
```

Nothing is down. NGF compares only the major version before deciding to
proceed, so a minor skew is best-effort rather than fatal, and all 24
HTTPRoutes are Accepted with the single Gateway Programmed. This is a
return-to-supported-configuration change, not an outage fix.

It is not cosmetic either. The skew has two measurable effects today
(**verified**, from the NGF control-plane log):

- Six NGF controllers are switched off at startup because the GVK they probe
  for is not being served:

  ```
  "ReferenceGrant v1 CRD not found, falling back to v1beta1"
  "CRD not found, controller disabled" kind=TLSRoute
  "CRD not found, controller disabled" kind=ListenerSet
  "CRD not found, controller disabled" kind=ReferenceGrant
  "CRD not found, controller disabled" kind=TCPRoute
  "CRD not found, controller disabled" kind=UDPRoute
  "CRD not found, controller disabled" kind=BackendTLSPolicy
  ```

  BackendTLSPolicy and ListenerSet have no CRD in the cluster at all. The
  other four have a CRD, but at a version older than the one NGF 2.7.0 probes
  for.

- NGF fails, repeatedly, to write Gateway status: `unknown field
  "status.attachedListenerSets"` — 7 occurrences in the last 24h, one per
  status write. The v1.1.0 Gateway schema has no such field, so the API
  server prunes it. The v1.6.1 standard schema does have it (Gateway `v1`
  `status` properties are `addresses`, `attachedListenerSets`, `conditions`,
  `listeners`).

The deeper reason this drifted at all: for five upstream minors there was no
freshness check on the Gateway API half of this bundle. `tools/sync-monitoring-crds.py`
ties the 3 prometheus-operator CRDs in the same file to a pinned chart
revision and CI enforces it (`.github/workflows/validate.yml`,
`foundation-crds-freshness`); the 8 Gateway API CRDs had no equivalent, no
pinned version anywhere in the repo, and no Renovate manager, so nothing moved
them forward. That gap is now closed — see
[Follow-up](#follow-up-close-the-drift-gap) — which is what makes this
upgrade a version bump in `tools/gateway-api-crd-pin.yaml` rather than a
hand-splice of a 17,000-line file.

## Live state (verified 2026-09-08)

Kubernetes server `v1.36.3`. NGF control plane
`ghcr.io/nginx/nginx-gateway-fabric:2.7.0`.

All 8 CRDs are annotated `bundle-version: v1.1.0` / `channel: experimental`,
`conversion.strategy: None`, and are owned solely by the `platform-crds`
Application (`bootstrap/00-crds.yaml`, sync-wave -1, `prune: true`,
`selfHeal: true`, `ServerSideApply=true`).

| CRD | served now (**S** = storage) | `status.storedVersions` now | objects |
|---|---|---|---|
| `gatewayclasses` | `v1` **S**, `v1beta1` | `[v1]` | 1 |
| `gateways` | `v1` **S**, `v1beta1` | `[v1]` | 1 |
| `httproutes` | `v1` **S**, `v1beta1` | `[v1]` | 24 |
| `grpcroutes` | `v1` **S**, `v1alpha2` | `[v1]` | 0 |
| `referencegrants` | `v1alpha2`, `v1beta1` **S** | `[v1beta1]` | 25 |
| `tcproutes` | `v1alpha2` **S** | `[v1alpha2]` | 0 |
| `tlsroutes` | `v1alpha2` **S** | `[v1alpha2]` | 0 |
| `udproutes` | `v1alpha2` **S** | `[v1alpha2]` | 0 |

The 25 ReferenceGrants are one `allow-gateway-tls` per namespace, all stored
at `v1beta1`, all rendered by
`helm-charts/tenant-envelope/templates/network-policies.yaml`. They are what
lets the Gateway in `nginx-gateway` read the wildcard TLS Secret out of each
tenant namespace — 23 of the Gateway's 24 attached routes are on the HTTPS
listener, so these are load-bearing for every TLS route in the cluster.

Consumers of these CRDs, from ClusterRole rules (**verified**): NGF
(`gatewayclasses`, `gateways`, `httproutes`, `grpcroutes`, `referencegrants`,
`tcproutes`, `tlsroutes`, `udproutes`, `backendtlspolicies`, `listenersets`);
cert-manager, which runs with `--enable-gateway-api` and watches
`gateways`/`httproutes`/`listenersets` plus creates solver HTTPRoutes; and
Holmes, read-only across all 8.

`bootstrap/crds/foundation-crds.yaml` is 20,227 lines. Gateway API occupies
lines 1–17,122; the 3 prometheus-operator CRDs occupy the remainder and are
regenerated by their own tool. Any regeneration must leave that tail alone.

## Target state (verified by parsing the v1.6.1 release artifacts)

Both channel bundles were downloaded from the `v1.6.1` release and their
version tables read directly, rather than inferred from changelog prose.

**standard/v1.6.1** — 10 CRDs, plus a `ValidatingAdmissionPolicy` and its
binding:

| CRD | served (**S** = storage) |
|---|---|
| `gatewayclasses` | `v1` **S**, `v1beta1` |
| `gateways` | `v1` **S**, `v1beta1` |
| `httproutes` | `v1` **S**, `v1beta1` |
| `grpcroutes` | `v1` **S** |
| `referencegrants` | `v1`, `v1beta1` **S** |
| `tcproutes` | `v1` **S**, `v1alpha2` (unserved) |
| `tlsroutes` | `v1` **S**, `v1alpha2` + `v1alpha3` (unserved) |
| `udproutes` | `v1` **S**, `v1alpha2` (unserved) |
| `listenersets` | `v1` **S** — new |
| `backendtlspolicies` | `v1` **S**, `v1alpha3` (unserved) — new |

**Why v1.6.1 and not the newest release.** v1.6.2 exists (**upstream**,
released 2026-09-03) and its CRDs are functionally identical to v1.6.1 — the
differences are the `bundle-version` annotation and a regex tweak in the
admission policy. v1.6.1 is nevertheless the target because it is the exact
version NGF 2.7.0 names in its `SupportedVersion` condition, and flipping
that condition is the entire point of this change. Whether NGF would also
accept v1.6.2 depends on how strictly it compares, which this plan has not
established — so take the version it asked for and let the freshness tooling
in [Follow-up](#follow-up-close-the-drift-gap) raise the next bump on its own
evidence.

**experimental/v1.6.1** is the same 10 with the unserved stanzas re-enabled,
plus three `gateway.networking.x-k8s.io` CRDs — `xbackends`,
`xbackendtrafficpolicies`, `xmeshes` — and a handful of extra fields.

### The storage-version picture is not what it looks like

The obvious hazard going in was ReferenceGrant: it is the one CRD in this
cluster whose storage version is not `v1` and it holds 25 objects. **That
hazard does not exist.** Upstream kept `v1beta1` as ReferenceGrant's storage
version in v1.6.1 and added `v1` as served-but-not-storage. Storage version
before: `v1beta1`. Storage version after: `v1beta1`. No migration, no
`storedVersions` change, no read-write pass for those 25 objects.

The storage version *does* move — for TCPRoute, TLSRoute and UDPRoute, all of
which graduated to `v1` (**upstream**: TLSRoute in v1.5.0, TCPRoute and
UDPRoute in v1.6.0). Each of those has **zero objects in this cluster**. So
the data-migration half of this upgrade is empty: there is no object anywhere
in the cluster that needs rewriting at a new storage version.

What is left is a bookkeeping problem, not a data problem — see
[Stage 2](#stage-2--stale-storedversions-cleanup).

### Version removals

- `grpcroutes` loses `v1alpha2` (**upstream**: unserved since v1.2.0; absent
  from the v1.6.1 CRD entirely — **verified** in the artifact).
- `referencegrants` loses `v1alpha2` (same v1.2.0 change).

Both are safe here because neither version appears in the respective
`storedVersions` (`[v1]` and `[v1beta1]` — **verified**). The API server
rejects an update that drops a `spec.versions` entry still listed in
`status.storedVersions`; that rejection is precisely the failure mode
upstream documents for v1.2.0, and this cluster is not exposed to it.

### Do we need intermediate hops?

**No. v1.1.0 → v1.6.1 is a single apply.** The reasoning, not just the
conclusion:

- CRD versions are a set, not a sequence. Applying the v1.6.1 CRD replaces
  the whole `spec.versions` list in one write; there is no accumulated state
  that an intermediate bundle would have set up.
- The only thing that makes a skip unsafe is a version being dropped from
  `spec.versions` while still in `storedVersions`. Checked above for every
  one of the 8 CRDs: no exposure.
- The only thing that makes it unsafe *for controllers* is a controller still
  speaking a version that stops being served. Checked: NGF 2.7.0 probes for
  versions **newer** than what is served today, not older; cert-manager
  watches `gateways`/`httproutes` at `v1`, which is served before and after.
- **Upstream** permits skipping minors explicitly. From the CRD management
  guide at `release-1.6`: "Although it is usually safe to upgrade across
  multiple Gateway API minor versions at once, the safest and most widely
  tested path will involve upgrading one minor version at a time." That is a
  preference, not a rule — and the reason to take the single hop here is that
  the per-hop checks upstream would have you do (`storedVersions`, served
  versions, controller compatibility) have all been done above and all come
  back clean.
- Upstream also documents a four-release removal process for API versions:
  new version becomes storage, old version deprecated, old version stops
  being served but stays in the CRD "for the sake of automatic translation",
  and only then is it dropped. That staging is what makes a five-minor jump
  survivable: the only version *dropped* from a CRD in this range was
  `v1alpha2` on GRPCRoute and ReferenceGrant in v1.2.0, and neither appears in
  this cluster's `storedVersions`.

Two CRDs were removed from upstream bundles in this range — `BackendLBPolicy`
(gone in v1.3.0) and `XListenerSet` (gone in v1.5.0, superseded by the
standard `ListenerSet`). Neither was ever vendored here and neither exists in
the cluster (**verified** — the full CRD list contains no such names), so
there are no orphans to clean up.

### Should this cluster stay on the experimental channel?

**No — move to standard.** Evidence:

- Every live object was validated offline against the v1.6.1 **standard**
  schemas: all 51 Gateway API objects (24 HTTPRoutes, 25 ReferenceGrants, 1
  Gateway, 1 GatewayClass). Result: **no experimental-only field is used by
  any of them, and no field in any of them is absent from the standard
  schema.** Nothing gets pruned by the channel move.
- The experimental-only surface at v1.6.1 is `spec.useDefaultGateways` on
  every route kind, `Gateway.spec.defaultScope`, and on HTTPRoute
  `rules[].retry`, `rules[].sessionPersistence`, percentage mirroring, and
  the extra filter/backendRef options. None appear in any manifest in this
  repo.
- Every GVK NGF 2.7.0 asks for — including `listenersets` and
  `backendtlspolicies` — is in the standard channel at v1.6.1. TCPRoute,
  TLSRoute, UDPRoute and ListenerSet all graduated to standard by v1.5/v1.6
  (**upstream**), which is what removes the original reason to be on
  experimental at all.
- The three `x-k8s.io` CRDs that experimental would add have zero consumers
  here and would be three more objects for `prune: true` to own.

The trade is that the move is effectively **one-way** if the VAP is installed
(see [Stage 4](#stage-4-separate-pr--the-safe-upgrades-vap)): that policy
denies applying experimental CRDs over standard ones. Given nothing uses
experimental fields, that is a gate on a direction we do not want to travel.

## Preconditions — hard gates

1. **Re-derive the live-state table above immediately before Stage 1.** The
   object counts are the whole argument for "no migration needed". If any of
   `tcproutes`/`tlsroutes`/`udproutes` is non-zero by then, stop and add a
   read-write pass ([Stage 2](#stage-2--stale-storedversions-cleanup) covers
   what that would look like).
2. **`platform-crds` is Synced/Healthy** before starting. An in-flight sync
   racing a CRD schema swap is avoidable noise.
3. **The regenerated bundle contains every CRD name the current one does.**
   This is the sharpest hazard in the change and is spelled out in
   [The prune hazard](#the-prune-hazard) below. Non-negotiable gate.
4. **Server-side dry run passes** before merge (below).
5. A window where a Gateway status blip is acceptable. There is no data-plane
   restart here — the nginx DaemonSet is untouched — but the NGF control
   plane gets restarted in Stage 3 and will re-resolve all 24 routes.

### The prune hazard

`platform-crds` runs `prune: true` and `selfHeal: true`, and `bootstrap/root-app.yaml`
excludes `crds/**` from its recursive sweep so this Application is the *only*
owner of these objects. **A CRD name that disappears from
`bootstrap/crds/foundation-crds.yaml` gets pruned from the cluster, and
pruning a CRD deletes every object of that kind.**

For `referencegrants` that is 25 objects whose loss immediately breaks the
Gateway's cross-namespace read of the wildcard TLS Secret, taking down TLS on
23 of 24 routes. For `httproutes` it is all 24 routes. A regeneration script
that silently emits 9 CRDs instead of 10, or misnames one, is a cluster-wide
ingress outage delivered by a green-looking sync.

Gate, run on the branch before merge:

```bash
python3 - <<'PY'
import yaml
old = {d["metadata"]["name"] for d in yaml.safe_load_all(open("bootstrap/crds/foundation-crds.yaml")) if d and d["kind"]=="CustomResourceDefinition"}
PY
# then diff that set against the same computation on the branch;
# every name present before MUST still be present after.
```

Expected: the set only ever grows — 11 CRDs before (8 Gateway API + 3
prometheus-operator), 13 after (10 + 3).

## Sequence

### Stage 1 — swap the bundle to standard v1.6.1

The merge is the deployment: `platform-crds` auto-syncs `main` with
`selfHeal: true`, so merging the PR is what applies the CRDs.

Regeneration, on the branch. Set `version: v1.6.1` and `channel: standard` in
`tools/gateway-api-crd-pin.yaml`, then:

```bash
python3 tools/sync-gateway-api-crds.py --write
```

The tool replaces only the documents it pins and leaves the 3
prometheus-operator CRDs that follow them alone. It ignores the
`ValidatingAdmissionPolicy` and its binding, which is Stage 4's business.

It will refuse to run until `listenersets` — which the v1.6.1 standard channel
ships and the pin has never heard of — is classified, and `backendtlspolicies`
has to be moved out of `notVendored` by hand to be picked up. Both refusals
are the decision this stage is supposed to take deliberately; see
[What this change enables as a side effect](#what-this-change-enables-as-a-side-effect).

Pre-merge verification (read-only; `--dry-run=server` runs admission but
persists nothing):

```bash
kubectl apply --server-side --field-manager=argocd-controller \
  --dry-run=server -f bootstrap/crds/foundation-crds.yaml
```

Expected: `customresourcedefinition.apiextensions.k8s.io/<name> serverside-applied
(server dry run)` for all 13, no `Invalid value` on `status.storedVersions`,
no error mentioning `spec.versions`. A `storedVersions` error here means
precondition 1 was wrong and the change must not merge.

After merge, verification:

```bash
kubectl get crd -o json | jq -r '
  .items[] | select(.metadata.name|test("gateway.networking")) |
  [.metadata.name,
   .metadata.annotations["gateway.networking.k8s.io/bundle-version"],
   .metadata.annotations["gateway.networking.k8s.io/channel"],
   ([.spec.versions[]|select(.served)|.name]|join(",")),
   ([.spec.versions[]|select(.storage)|.name]|join(",")),
   (.status.storedVersions|join(","))] | @tsv' | column -t
```

Expected: 10 rows, all `v1.6.1` / `standard`; storage column `v1` everywhere
except `referencegrants` which stays `v1beta1`; `storedVersions` reads `v1`
for gatewayclasses/gateways/httproutes/grpcroutes, `v1beta1` for
referencegrants, and **`v1alpha2,v1`** for tcproutes/tlsroutes/udproutes —
that last one is the expected, not-yet-cleaned state that Stage 2 fixes.

And that nothing was lost:

```bash
kubectl get httproutes -A --no-headers | wc -l    # expect 24
kubectl get referencegrants -A --no-headers | wc -l  # expect 25
kubectl get gateways,gatewayclasses -A --no-headers | wc -l  # expect 2
kubectl get httproutes -A -o json | jq -r '
  [.items[].status.parents[].conditions[] | select(.type=="Accepted") | .status] | unique'
# expect: ["True"]
```

**Rollback for Stage 1:** revert the PR. The reverted bundle reinstates the
v1.1.0 CRDs, including the `v1alpha2` stanzas for grpcroutes and
referencegrants; re-adding a CRD version is permitted, so this direction
works. Two caveats. First, it only works cleanly *before* Stage 4 — once the
VAP exists, admission denies any bundle annotated below v1.5.0 and the revert
sync will fail with `Installing CRDs with version before v1.5.0 is
prohibited`. Second, the revert removes `listenersets` and
`backendtlspolicies` from the desired state, and `prune: true` will delete
those CRDs — harmless only while they hold zero objects, which is true on day
one and stops being true the moment anyone creates a BackendTLSPolicy. After
that point the revert is destructive and the rollback is "fix forward", not
"revert".

### Stage 2 — stale `storedVersions` cleanup

Three CRDs, zero objects each, one patch apiece. Confirm the counts again
first — the patch is only correct because nothing was ever persisted at
`v1alpha2`:

```bash
for k in tcproutes tlsroutes udproutes; do
  printf '%s ' "$k"; kubectl get "$k" -A --no-headers 2>/dev/null | wc -l
done
# All three MUST print 0. If any is non-zero, do not patch - see below.
```

Then (human-executed):

```bash
for k in tcproutes tlsroutes udproutes; do
  kubectl patch customresourcedefinitions "$k.gateway.networking.k8s.io" \
    --subresource=status --type=merge \
    -p '{"status":{"storedVersions":["v1"]}}'
done
```

Verify:

```bash
kubectl get crd tcproutes.gateway.networking.k8s.io \
  tlsroutes.gateway.networking.k8s.io udproutes.gateway.networking.k8s.io \
  -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.status.storedVersions}{"\n"}{end}'
# expect each: ["v1"]
```

**If any count is non-zero**, the patch is unsafe on its own and the objects
must be rewritten at the new storage version first. Two mechanisms exist:

- The **kube-storage-version-migrator** (`StorageVersionMigration` CRs). It is
  **not available on this cluster** — `kubectl get --raw /apis` shows no
  `storagemigration.k8s.io` group (**verified**), so using it would mean
  installing the controller first. Not worth it for a handful of objects.
- A **read-write-all pass**: `kubectl get <kind> -A -o json | kubectl replace
  -f -`, or the annotation-touch loop upstream documents for the v1.2.0
  equivalent of this problem. Every write lands at the new storage version.
  This is the right tool at this scale.

Neither is needed as things stand. The 25 ReferenceGrants in particular need
no pass at all, because their storage version does not move.

**Why not just leave `storedVersions` stale.** It is inert today — nothing
reads it at request time. It bites on the *next* upgrade: the API server
refuses to remove a version from `spec.versions` while it is still listed in
`storedVersions`. Upstream currently keeps the `v1alpha2` stanzas present but
unserved, which is exactly the accommodation that lets a stale entry sit
harmlessly. The release that finally drops those stanzas — v1.7 or later —
will be rejected on apply, and because this bundle is GitOps-applied the
symptom will be `platform-crds` stuck in a sync error loop with a message
about `status.storedVersions`, discovered by whoever merges the next Renovate
bump rather than by whoever caused it. Cleaning up now, while the count is
provably zero and the patch is a one-liner, is far cheaper than cleaning up
under a red Application.

**Rollback for Stage 2:** patch the value back
(`{"status":{"storedVersions":["v1alpha2","v1"]}}`). This is genuinely
reversible while the `v1alpha2` stanza is still in `spec.versions`, which it
is in v1.6.1. It stops being reversible once a future bundle removes that
stanza — but at that point re-adding `v1alpha2` is not something anyone
should want.

### Stage 3 — restart the NGF control plane

**Verified**: NGF 2.7.0 probes for CRD presence **once, at controller start**
— every `controller disabled` line in the log carries the same timestamp as
`Starting the NGINX Gateway Fabric control plane`. The new CRDs will not be
picked up by the running process. This is why the ordering is
CRDs-then-restart and not the reverse: restarting first would just re-disable
the same six controllers.

```bash
kubectl -n nginx-gateway rollout restart deploy/platform-nginx-gateway-fabric
kubectl -n nginx-gateway rollout status deploy/platform-nginx-gateway-fabric
```

Verify — the whole point of the exercise:

```bash
kubectl get gatewayclass nginx -o jsonpath='{range .status.conditions[*]}{.type}={.status} {.reason}{"\n"}{end}'
# expect: Accepted=True Accepted
#         SupportedVersion=True SupportedVersion
#         ResolvedRefs=True ResolvedRefs

kubectl -n nginx-gateway logs deploy/platform-nginx-gateway-fabric --tail=200 \
  | grep -E 'controller disabled|falling back'
# expect: no output

kubectl -n nginx-gateway logs deploy/platform-nginx-gateway-fabric --since=15m \
  | grep -c attachedListenerSets
# expect: 0

kubectl get httproutes -A -o json | jq -r '
  [.items[].status.parents[].conditions[] | select(.type=="Accepted") | .status] | unique'
# expect: ["True"]  (all 24, re-resolved by the restarted control plane)

kubectl -n nginx-gateway get gateway platform-gateway -o jsonpath='{.status.listeners[*].attachedRoutes}'
# expect: 1 23   (http, https - unchanged from before the upgrade)
```

Also confirm the data plane never moved:

```bash
kubectl -n nginx-gateway get ds platform-gateway-nginx \
  -o jsonpath='{.status.numberReady}/{.status.desiredNumberScheduled}{"\n"}'
```

**Rollback for Stage 3:** none required — a restart is not a state change. If
routes fail to re-attach, that is a Stage 1 problem surfacing late, and the
Stage 1 rollback applies.

### Stage 4 (separate PR) — the `safe-upgrades` VAP

v1.6.1 ships a `ValidatingAdmissionPolicy` +
`ValidatingAdmissionPolicyBinding` named
`safe-upgrades.gateway.networking.k8s.io` (**upstream**: introduced in
v1.5.0). It denies, cluster-wide, on CREATE/UPDATE of any CRD in the
`gateway.networking.k8s.io` group:

- installing experimental CRDs over standard ones, and
- installing any bundle annotated `v1.[0-5].x` or `v0`.

`failurePolicy: Fail`, `validationActions: [Deny]`.

Two sharp edges in it. The deny message says "Installing CRDs with version
before v1.5.0 is prohibited", but the CEL regex it ships with at v1.6.x is
`v1.[0-5].\d+` — which also blocks **v1.5.x itself**. The message and the
rule disagree; trust the rule. And the policy sits *ahead of* the CRDs in
upstream's install file, so a single `kubectl apply -f` of the full release
artifact makes the policy live before its own CRDs are applied. Staging it
separately, as recommended below, sidesteps that ordering question entirely.

**Recommendation: do not vendor it in Stage 1. Add it in a separate PR after
the bundle has soaked**, and only with the rollback consequence written down.
The reason is specific to how this repo rolls back: the documented path for
every other change here is "revert the PR and let Argo sync". This policy
denies exactly that revert — an Argo sync of the v1.1.0 bundle would be
rejected at admission and `platform-crds` would sit red until a human deletes
the VAP by hand. Coupling the upgrade and the thing that blocks its own
rollback into one merge means a bad Stage 1 has to be untangled through an
admission gate.

It is still worth having afterwards: it is a real guard against someone
re-vendoring an experimental bundle or an older one by accident, which is the
same class of mistake that produced this drift.

If adopted, document alongside it: **to roll back the Gateway API bundle
below v1.5.0, delete
`validatingadmissionpolicybinding safe-upgrades.gateway.networking.k8s.io`
first, then revert.** Whether Argo's prune ordering deletes the binding before
it applies the older CRDs in a single revert sync is not something this plan
has established — assume it does not, and delete it manually.

## What this change enables as a side effect

Adding `listenersets` and `backendtlspolicies` turns on two NGF controllers
that are currently disabled. Both kinds have zero objects and neither the
repo nor any chart creates one, so the behavioural change on day one is nil —
but it is a genuine widening of what the control plane reconciles, delivered
by a change whose stated purpose is a version bump. It is called out here
rather than discovered later. The alternative — vendoring only the 8 CRDs
that exist today — would leave NGF permanently reporting two disabled
controllers and would not actually reduce the blast radius, since the CRD
schemas being applied are the same either way.

## Abort criteria

- The pre-merge `--dry-run=server` reports anything about
  `status.storedVersions` or `spec.versions` → the storage-version analysis
  is wrong for the current cluster state; stop and redo it.
- The CRD-name set shrinks between the old and new bundle → stop, do not
  merge. See [The prune hazard](#the-prune-hazard).
- Any HTTPRoute leaves `Accepted=True` at any point → stop; do not proceed to
  the next stage.
- `platform-crds` goes to `SyncFailed` or `Degraded` → stop and read the
  message before retrying; `selfHeal` will keep retrying on its own and
  should not be left doing so unattended.
- cert-manager begins failing HTTP-01 challenges (it creates solver
  HTTPRoutes) → stop; certificate issuance is a cross-cutting dependency and
  a broken HTTPRoute schema would show up here first.
- Gateway `Programmed` goes False → stop, and check the data-plane DaemonSet
  before anything else.

## Rollback summary — what is and is not one-way

| Stage | Reversible? | How |
|---|---|---|
| 1 — bundle swap | Yes, until Stage 4 | Revert the PR; Argo re-applies v1.1.0 |
| 1 — new CRDs (`listenersets`, `backendtlspolicies`) | Yes while empty | Revert prunes them; **destructive once objects exist** |
| 2 — `storedVersions` patch | Yes | Patch back to `["v1alpha2","v1"]` |
| 3 — NGF restart | N/A | Not a state change |
| 4 — VAP | Yes, but blocks other rollbacks | Delete the binding, then revert |
| experimental → standard channel | One-way once Stage 4 lands | Admission denies experimental-over-standard |
| removing a served CRD version | Reversible at the CRD level | Re-adding a version is permitted; what is not recoverable is any object written at the removed version in the interim — none exist here |

The genuinely irreversible actions in this plan are: pruning a CRD that has
objects (avoided by precondition 3), and — after Stage 4 — going back to
experimental or to a pre-v1.5.0 bundle without first deleting the policy.

## Follow-up: close the drift gap

The Gateway API CRDs drifted five minors because nothing watched them. The
prometheus-operator CRDs in the *same file* do not drift, because
`tools/sync-monitoring-crds.py` pins them to a chart revision and CI fails a
PR that lets them age. The mirror image of that is now in place:

- `tools/gateway-api-crd-pin.yaml` holds the version, the channel, and the
  explicit list of which CRDs from that release are vendored. Unlike the
  monitoring CRDs there is no chart to hang the version off, and a constant
  buried in a script is not something Renovate can read, so the pin is its
  own file.
- `tools/sync-gateway-api-crds.py` compares each pinned document against the
  release artifact byte-for-byte and regenerates it under `--write`, with the
  same check-mode contract and exit codes as the monitoring script. It shares
  the bundle file with that script by owning documents by CRD name rather
  than by line range, so neither can walk over the other's half.
- `.github/workflows/validate.yml` runs it in check mode as a second step of
  the existing `foundation-crds-freshness` job — one file, one red check,
  whichever half aged.
- A Renovate `customManager` in `renovate.json` watches the pinned version
  against `github-releases` for `kubernetes-sigs/gateway-api`, so the bump
  arrives as a PR rather than as a controller condition nobody reads. It
  bumps the version only; the regeneration is a human running `--write`, and
  the failing freshness check in between is the handoff.

What is deliberately *not* built: the pin does not assert anything about what
is installed in the cluster, only about what the repo vendors. A
`platformctl cluster status` check asserting `GatewayClass nginx` has
`SupportedVersion=True` — the same shape as the Longhorn engine-version check
proposed in `docs/longhorn-engine-upgrade.md` — would close the other half,
and is the signal that would have surfaced this drift years before a chart
bump did.

## Out of scope

- The NGF chart — already at 2.7.0 and unchanged by this plan.
- The 3 prometheus-operator CRDs sharing the bundle file — different owner,
  different freshness tool, must survive the regeneration untouched.
- Adopting any new Gateway API feature (ListenerSet, BackendTLSPolicy, CORS
  filter, retries). The CRDs arrive; nothing uses them.
- The freshness tooling described under [Follow-up](#follow-up-close-the-drift-gap).
  It landed separately and pins the bundle at where it is today; this runbook
  is what moves the pin.

## Unverified — confirm before relying on these

- **CEL validation against existing objects.** All 51 live objects were
  checked against the v1.6.1 standard *structural* schemas and pass. They
  were **not** evaluated against the new CEL rules as a set, because CEL
  cannot easily be run offline. CEL runs on write, not on stored data, so
  nothing breaks at apply time — but an object that violates a new rule
  becomes un-updatable, and the failure would surface the next time Argo or a
  controller writes it. The three rules added in this range that apply to
  fields which already existed were each checked by hand (**verified**):

  | Rule (**upstream**) | Exposure here |
  |---|---|
  | v1.5.0 — a listener with `protocol: TLS` must set `tls` | None. The Gateway has one `HTTP` and one `HTTPS` listener; no `TLS` listener exists. The HTTPS listener sets `tls.mode: Terminate` regardless. |
  | v1.2.0 — total `matches` summed across all HTTPRoute rules is capped | None. The busiest route has 1 rule and 1 match; the cap is well over 100. |
  | v1.6.0 — `spec` becomes required on ReferenceGrant | None. All 25 have a `spec`. |

  A post-Stage-1 `kubectl apply --dry-run=server` over the rendered tenant
  manifests is still the right way to close the residual gap, since it
  exercises every rule rather than the three anticipated ones.

  Note that a rule this plan initially expected to matter does **not** exist:
  "`tls` must be configured for `protocol: HTTPS`" appears in the v1.6.0-rc.1
  breaking changes but was reverted before v1.6.0 final and is not in the
  shipped bundle.
- **Whether NGF 2.7.0 requires any experimental-channel field for a feature
  in use here.** The GVKs it watches are all standard at v1.6.1 (verified
  from its ClusterRole and the bundle), and no live object uses an
  experimental field — but NGF's own documentation was not audited
  feature-by-feature against the standard channel. If a used NGF feature
  turns out to need an experimental field, the fix is re-vendoring the
  experimental bundle, which the Stage 4 VAP would block.
- **Argo's prune/apply ordering within a single sync** for the revert path
  described in Stage 4. Assumed unfavourable; delete the VAP binding manually
  rather than testing it during an incident.
- **`v1alpha2` TLSRoute in the experimental channel.** The v1.5 changelog
  states TLSRoute `v1alpha2` was removed from the experimental channel, but
  the v1.6.1 experimental artifact still carries it as a served version. The
  artifact is what gets applied, so this discrepancy does not affect the plan
  — but it means the v1.5 changelog text should not be treated as
  authoritative about the current bundle contents.
- **How strictly NGF compares versions.** The `SupportedVersion` condition
  names v1.6.1 exactly. Whether v1.6.2 (or a later patch) would also satisfy
  it was not determined from NGF's source. This only matters for the *next*
  bump, not this one.
- **Timing.** No estimate is offered for how long Argo takes to apply a
  1 MB, 13-CRD bundle, or whether the API server briefly rejects requests
  while re-validating schemas of that size. Watch it rather than assume it.
