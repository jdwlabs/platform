# ADR: Chained Cilium — sizing, the breakage set, and how enforcement gets proven

Status: accepted, not yet implemented. Extends
[networkpolicy-enforcement-via-chained-cilium](0011-networkpolicy-enforcement-via-chained-cilium.md);
supersedes neither. That record is referenced by slug rather than by number
throughout, because a renumber of it is in flight — see the numbering note
under Decision. The choice of chained Cilium is settled there and is not
reopened here.

## Problem

The chained-Cilium record chose `generic-veth` chaining mode and sketched a
five-step rollout. Re-deriving its premises against live state on 2026-08-04
confirmed every one of them — 115 `NetworkPolicy` objects across 24
namespaces, `kube-flannel` 8/8 as the only CNI, no policy engine of any kind
present. That decision stands.

But it stops at the point where the rollout becomes dangerous. Three
things it leaves open each independently determine whether the rollout is
safe to run, and none of them can be answered by reading the decision:

1. **Whether the agent fits.** That record does not mention memory. Cilium's
   chart ships `resources: {}`, and there is no `LimitRange` in `kube-system`
   to backfill it.
2. **What enforcement actually breaks.** It correctly says the 91
   allow-policies are untested and that the audit log is the specification.
   It does not say that part of the answer is already knowable statically —
   and the part that is knowable is severe.
3. **How isolation gets proven.** Its step 5 flips audit mode off. It
   states no test that distinguishes "enforcement is working" from "the
   engine is loaded and still enforcing nothing", which is the failure this
   whole ticket exists to correct.

This record supplies those three, and the runbook that follows from them.

## The install has one setting that can take the cluster down

Cilium's `cni.exclusive` defaults to **true**. At that setting the agent
renames every non-Cilium file in `/etc/cni/net.d` — `.conf`, `.conflist` and
`.json` alike — to a `.cilium_bak` suffix, and starts an `fsnotify` watcher
to re-assert that if anything rewrites them. From `daemon/cmd/cni/config.go`:

```go
// cleanupOtherCNI renames any existing CNI configuration files with the suffix
// ".cilium_bak", excepting files in keep
```

Flannel's datapath on this cluster is `10-flannel.conflist`, written by the
`install-config` init container of the `kube-flannel` DaemonSet. A chained
install that accepts the default therefore deletes the CNI configuration of
the CNI it is supposed to be chaining onto, on every node at once, and holds
it deleted. Running pods keep their existing veth and survive; every
subsequent pod creation fails to get networking.

`cni.exclusive=false` is not a tuning preference. It is the difference
between a policy engine and a cluster-wide outage, and it is the single
value most likely to be dropped when someone adapts an install command from
the upstream quickstart, because the quickstart is written for Cilium as the
only CNI.

## Sizing — the binding constraint is a worker, not the control plane

The received wisdom is that the control plane is the fragile part of this
cluster. For this change it is not. Measured live 2026-08-04:

| Node | Role | Allocatable | Requested | Free for requests |
|---|---|---|---|---|
| talos-k3y-y3e | worker | 2363Mi | 2040Mi (86%) | **323Mi** |
| talos-g1i-e3h | worker | 2363Mi | 1650Mi (69%) | 713Mi |
| talos-2qd-v0u | worker | 2363Mi | 1554Mi (65%) | 809Mi |
| talos-oam-s4g | control-plane | 4772Mi | 1746Mi (36%) | 3026Mi |
| talos-6iz-oey | control-plane | 4772Mi | 978Mi (20%) | 3794Mi |
| talos-fow-vbk | control-plane | 4772Mi | 978Mi (20%) | 3794Mi |
| talos-4h8-zy6 | worker | 14430Mi | 5970Mi (41%) | 8460Mi |
| talos-lx0-6a4 | worker | 62749Mi | 12323Mi (19%) | 50426Mi |

The three control-plane nodes have between 3.0Gi and 3.7Gi of unreserved
request budget each. The tightest node in the cluster is `talos-k3y-y3e`, a
worker, with 323Mi — and its limits are already committed to 214% of
allocatable.

Two consequences follow, and both are load-bearing:

**The upstream example request does not fit.** Cilium's `values.yaml` carries
a commented example of `100m`/`512Mi` requests. A 512Mi request cannot be
scheduled on `talos-k3y-y3e`. A DaemonSet pod that cannot be scheduled is
`Pending`, and a node whose Cilium agent is `Pending` enforces no policy on
any pod it hosts — while `kubectl get netpol` on those namespaces looks
exactly the same as on every other node. That is the same class of silent
false assurance this ADR chain exists to eliminate, reintroduced by a
resource number.

**The default is BestEffort.** The chart ships `resources: {}` and there is
no `LimitRange` in `kube-system` to supply a default, so an unmodified
install produces a BestEffort DaemonSet on all eight nodes. Talos's OOM
controller kills BestEffort cgroups first; that is precisely how ESO and
cert-manager were killed once already. A BestEffort policy engine is a
policy engine that disappears under memory pressure, which is the moment
isolation matters most.

Sized against the measured ~180Mi per node for agent plus Envoy, and with
Envoy disabled because chaining forgoes L7 anyway:

```yaml
resources:
  requests:
    cpu: 100m
    memory: 192Mi
  limits:
    memory: 512Mi
```

192Mi fits inside 323Mi with margin on the worst node. The limit is set and
the request is set, so the QoS class is Burstable, not BestEffort.

These belong in the tenant values file rather than a live patch, and it is
worth being exact about why, because the usual shorthand — "ArgoCD reverts
anything not in Git" — is not true and leads people to the right action for
the wrong reason. Applications sync with `ServerSideApply=true` plus
`selfHeal`, so reversion is decided by server-side-apply **field ownership**,
not by whether a value appears in Git. A field absent from the rendered
manifest is owned by nobody and survives indefinitely: on this cluster
`kubectl.kubernetes.io/restartedAt` has persisted on a tenant Deployment
since 2026-05-23 under prune and selfHeal, because `argocd-controller` holds
`Apply` while `kubectl-rollout` holds `Update` over a disjoint field set, and
the two never contend.

`resources` is the opposite case. The chart renders it, so it is inside
ArgoCD's applied field set, ArgoCD owns it, and a live patch to it is
genuinely reverted on the next sync. The conclusion holds — set it in values
— but only for fields the chart actually emits. Anyone reasoning about
whether some other out-of-band change will stick should check field ownership
in `metadata.managedFields` rather than assume selfHeal settles it.

## What enforcement would actually break

The chained-Cilium record groups the 115 objects by shape and concludes the
allow-set is untested. That is right, but it understates what is already
provable without the audit log. The 18 `platform`-tier namespaces pair their
`default-deny-all` with both `allow-all-ingress` and `allow-all-egress`;
because policies are additive, enforcement there changes nothing. The
exposure is entirely in the six namespaces with no allow-all companion —
the ones the tenant model claims are isolated.

For those six, the allow-set is knowable statically, because the tenant
envelope generates it. `helm-charts/tenant-envelope/templates/network-policies.yaml`
renders exactly one egress allowance for the `default` tier:

```yaml
  egress:
    - ports:
        - port: 53
          protocol: UDP
        - port: 53
          protocol: TCP
```

Port 53 and nothing else. Every other egress destination is denied. Matching
that against what the workloads in those namespaces actually dial, read from
their live pod specs:

| Namespace | Workload | Destination | Verdict on enforcement |
|---|---|---|---|
| jdwlabs-prd | usersrole-prd | `platform-postgresql-cluster-prd-rw.database:5432` | **denied** |
| jdwlabs-non | usersrole-non | `platform-postgresql-cluster-non-rw.database:5432` | **denied** |
| dotablaze-tech-prd | meowbot-prd | `platform-postgresql-cluster-prd-rw.database:5432` | **denied** |
| dotablaze-tech-non | meowbot-non | `platform-postgresql-cluster-non-rw.database:5432` | **denied** |
| jdwlabs-non | servicediscovery-non | `platform-tempo.monitoring:4317` | **denied** |
| jdwlabs-runners | — | `ci` tier carries `allow-all-egress` | permitted |
| dotablaze-tech-runners | — | `ci` tier carries `allow-all-egress` | permitted |

So enforcing the policies as written severs every stateful tenant workload
from its database, in both environments, for both tenants, simultaneously.
Not a subtle gap in an edge path — the primary data dependency of every
application the cluster exists to run.

This is worth stating plainly because it inverts the intuition the ticket
invites. The six "genuinely isolating" namespaces are the ones that look
most carefully configured, and they are the ones where enforcement is most
destructive. The 18 permissive platform namespaces are where enforcement is
free. Any rollout that starts with the tenant namespaces on the grounds that
they are the security-relevant ones has it exactly backwards.

The fix is a template change, not a per-namespace patch — one egress
allowance for the `database` namespace on 5432 and one for `monitoring` on
4317, added to the `default` tier, reaching all four workload namespaces at
once. That change should land and be observed in audit mode **before**
enforcement, not discovered from the audit log after.

## Decision

**1. The rollout is sequenced permissive-first.** The 18 platform-tier
namespaces enforce first because their allow-all pair makes enforcement a
no-op there, which proves the engine works without risking anything. The
four tenant workload namespaces enforce last, after the template gap above
is closed and their audit logs are silent. `kube-system` enforces on its own,
after everything else, as the chained-Cilium record already requires.

**2. Audit mode is the default state, not a phase.** Cilium is installed with
`policyAuditMode=true` and stays there. Enforcement is enabled per namespace
by removing audit, so at every moment the blast radius is one namespace and
the rollback is one flag.

**3. The agent is Burstable and sized to the tightest node.** 192Mi request,
512Mi limit, set in `tenants/platform/services/cilium/values.yaml`. Envoy
disabled. Not patched live.

**4. This ADR takes 0013, and how that number was arrived at is itself the
finding.** `main` currently carries two records numbered 0011 — the
chained-Cilium one this extends, and the out-of-band-secret-rotation one —
both landed from concurrent branches. A separate branch is renaming the
chained-Cilium record to 0012, which puts this record immediately after the
decision it extends. That adjacency is deliberate and worth preserving.

Getting here took three attempts, because the number was contested twice
while this record was being written: 0012 first, then 0014, each time by a
branch that had claimed it in the interval. That is not carelessness by
anyone involved. It is what happens when ADR numbers are allocated by
whoever writes the file and nothing checks uniqueness at commit or in CI, so
allocation races whenever more than one record is in flight — which is now
the normal condition rather than the exception.

A gap in the sequence is harmless; two records at one number is the actual
defect, and it is the one already on `main`. Renumbering by hand resolves an
instance and not the mechanism — a CI check rejecting a duplicate numeric
prefix under `docs/adr/` would, and is worth more than any individual
renumber.

The record this extends is referenced by slug rather than number throughout,
so those references survive its pending renumber without further edits.

## Runbook

Eleven steps. Estimated elapsed time is dominated by observation, not by
work: steps 1–3 are roughly two hours, step 4 is a minimum of seven days,
steps 5–11 are under an hour of action spread across as many days as the
audit logs stay noisy. There is **no expected downtime** at any step; every
step that could drop traffic is gated behind a preceding step that shows
what it would drop.

Cluster operations go through `platformctl` per the repo's binary contract;
raw `kubectl` below is read-only verification only.

---

**Step 1 — land the chart, do not sync it.**
Add the `cilium` service entry to `tenants/platform/tenant.yaml` (namespace
`kube-system`, wave 1), the Helm repo to `project.extraSourceRepos`, and
`tenants/platform/services/cilium/values.yaml` with the values below. Merge.

```yaml
cni:
  chainingMode: generic-veth
  customConf: true
  exclusive: false          # Flannel owns the CNI slot; true renames its conflist
  configMap: cni-configuration
routingMode: native         # Flannel forwards; Cilium must not
enableIPv4Masquerade: false # Flannel masquerades
enableIPv6Masquerade: false
endpointRoutes:
  enabled: true
policyAuditMode: true
envoy:
  enabled: false            # no L7 in chaining mode
ipam:
  mode: kubernetes
k8sServiceHost: localhost   # KubePrism
k8sServicePort: 7445
cgroup:
  autoMount:
    enabled: false          # Talos provides cgroupv2 and bpffs
  hostRoot: /sys/fs/cgroup
securityContext:
  capabilities:
    ciliumAgent:
      [CHOWN, KILL, NET_ADMIN, NET_RAW, IPC_LOCK, SYS_ADMIN, SYS_RESOURCE,
       DAC_OVERRIDE, FOWNER, SETGID, SETUID]
    cleanCiliumState: [NET_ADMIN, SYS_ADMIN, SYS_RESOURCE]
resources:
  requests:
    cpu: 100m
    memory: 192Mi
  limits:
    memory: 512Mi
```

The `cni-configuration` ConfigMap must reproduce the live Flannel conflist
verbatim — `name: cbr0`, the `flannel` and `portmap` plugins exactly as
`kube-flannel-cfg` has them — with `cilium-cni` appended. A mismatched `name`
field produces a conflist that CNI will not match.

*Blast radius:* none. Nothing is applied.
*Rollback:* revert the commit.

**Step 2 — verify what ArgoCD is actually about to run.**
Hard-refresh the Application and read the rendered manifest, not the sync
status. An Application can report `Synced` with a fresh `reconciledAt` while
still serving a pre-merge manifest, so `Synced` is not evidence the new
values are live.

Confirm in the rendered output: `cni.exclusive: false` present, `resources`
present and non-empty, `policyAuditMode: true`.

*Blast radius:* none, read-only.
*Rollback:* n/a.

**Step 3 — sync, then check scheduling before checking health.**
The first question is not whether the agents are `Running`, it is whether
all eight were schedulable:

```
kubectl get ds cilium -n kube-system \
  -o custom-columns='DESIRED:.status.desiredNumberScheduled,READY:.status.numberReady'
kubectl get pods -n kube-system -l k8s-app=cilium -o wide
```

`DESIRED` must be 8 and `READY` must be 8. A `Pending` agent on
`talos-k3y-y3e` means the request did not fit and that node silently has no
policy engine. Then confirm chaining actually engaged:

```
kubectl exec -n kube-system ds/cilium -- cilium status | grep -i chaining
```

Expected: `CNI Chaining: generic-veth`. If this reads anything else, stop —
Cilium is not chained and may be contending for the CNI slot.

*Blast radius:* new pod scheduling cluster-wide, if `cni.exclusive` was
wrong. Existing pods keep their networking either way.
*Rollback:* delete the Cilium Application; if `/etc/cni/net.d` shows
`.cilium_bak` files, restore them on each node and restart `kube-flannel`.

**Step 4 — restart every pod, then assert the count.**
Pods that predate the chaining config are not managed by Cilium. They keep
running and keep receiving traffic, and they get no enforcement — a pod that
reads as protected and is not. Roll every workload, then verify Cilium's
managed endpoint count reconciles against the running pod count rather than
assuming the rollout was complete. Any host-network pod is legitimately
excluded and should be subtracted explicitly, not hand-waved.

*Blast radius:* a rolling restart of all workloads. Standard rollout risk,
no policy risk — audit mode is on.
*Rollback:* none needed; restarting is not reversible and does not need to be.

**Step 5 — observe for a minimum of seven days.**
Collect `cilium-dbg monitor -t policy-verdict` and read `action audit`
entries. Seven days is a floor, not a target: it must span at least one full
cycle of the slow paths — Postgres backups, cert-manager renewals, the Vault
unseal CronJob, and the longest scrape interval. A day of traffic exercises
the hot paths and none of the ones that will break in week three.

*Blast radius:* none. Audit mode acts on nothing.
*Rollback:* n/a.

**Step 6 — close the known template gap.**
Add egress to `database` on 5432 and to `monitoring` on 4317 to the `default`
tier in `helm-charts/tenant-envelope/templates/network-policies.yaml`. This
is the gap proven statically above; it does not need to wait for the audit
log to rediscover it. Merge and let the four workload namespaces pick it up.

*Blast radius:* none — still audit mode. The policies change, nothing enforces
them.
*Rollback:* revert the commit.

**Step 7 — close the remaining gaps the audit log found.**
Everything step 5 surfaced that step 6 did not already cover. This is the
step whose duration cannot be estimated in advance, because its size is
exactly the quantity nobody knows: how wrong 91 never-exercised policies are.

*Blast radius:* none.
*Rollback:* revert per commit.

**Step 8 — enforce the 18 platform-tier namespaces.**
Remove audit mode for these first. Their `allow-all-ingress` plus
`allow-all-egress` pair means enforcement is semantically a no-op, so this
step proves the enforcement path works end to end while risking nothing. If
anything breaks here, the problem is Cilium's datapath, not the policies —
a diagnostically clean signal that is unavailable in any other ordering.

*Blast radius:* nominally nil; realistically it is the first real test of the
engine.
*Rollback:* re-enable audit mode on the affected namespace.

**Step 9 — enforce the two `ci` runner namespaces.**
They carry `allow-all-egress`, so only ingress is genuinely denied, which is
the intent for runners. Low risk and it exercises a real deny for the first
time.

*Blast radius:* CI runners lose inbound reachability, which is the point.
*Rollback:* re-enable audit mode.

**Step 10 — enforce the four tenant workload namespaces, one at a time.**
`jdwlabs-non` first, then `dotablaze-tech-non`, then the two `prd`
namespaces. Only proceed to the next once the previous namespace's audit log
has been silent and its workloads are verifiably still reaching Postgres.
Non-production before production, per tenant, with no batching.

*Blast radius:* one tenant namespace per step. On the `prd` steps this is
user-visible.
*Rollback:* re-enable audit mode for that namespace — one flag, seconds.

**Step 11 — enforce `kube-system` last.**
Only after every other namespace has been enforced and stable, and only once
its own audit log has been silent across a full slow-path cycle. It holds
CoreDNS and Flannel itself.

*Blast radius:* the highest in the cluster. A mistake here takes DNS and
potentially the datapath.
*Rollback:* re-enable audit mode.

## Proving isolation

The Definition of Done asks for evidence that traffic is blocked, not that
the engine is installed. "The pods are Running" proves nothing here — that
was true throughout the entire period in which 115 policies enforced nothing.

The test must be a **negative** one, and it must be run twice: once before
enforcement, where it is expected to succeed, and once after, where it is
expected to fail. A test that is only run after enforcement cannot
distinguish a working deny from a broken test.

In a scratch namespace carrying the `default` tier, deleted afterwards:

1. **Baseline, audit mode on.** From a pod in the scratch namespace, open a
   TCP connection to a pod in another tenant's namespace and to
   `platform-postgresql-cluster-prd-rw.database:5432`. Both must succeed, and
   `cilium-dbg monitor -t policy-verdict` must show `action audit` for both.
   The audit verdict is what proves the policy is loaded and evaluated —
   without it, a later failure could just as easily be a routing fault.
2. **Enforce that namespace only.**
3. **Re-run the identical connections.** Both must now fail, and the monitor
   must show `action deny` naming source, destination and port. Record the
   verdict lines, not a description of them.
4. **Confirm the permitted path still works.** DNS resolution must still
   succeed and ingress from `nginx-gateway` must still reach the pod. A deny-
   everything result is not isolation, it is an outage that happens to
   satisfy the deny test.

Step 4 is the one most likely to be skipped and the one that distinguishes
this from a broken CNI. The pass condition is that the denied paths deny
**and** the allowed paths allow, demonstrated in the same run.

Cross-namespace deny between the two tenants is the specific control this
ticket is about, so that pair is the one to record on the ticket.

## Consequences

**A `Pending` agent is now a tracked failure mode.** Sizing the request to
fit `talos-k3y-y3e` means any future workload that consumes that node's
remaining ~130Mi of headroom can render the policy engine unschedulable
there. Nothing currently alerts on a DaemonSet whose desired and ready counts
diverge, and until something does, the guarantee is one scheduling decision
from silently regressing on one node.

**Tenants can create `CiliumNetworkPolicy`.** The tenant AppProject
blacklists `networking.k8s.io/NetworkPolicy`, which is what makes policy
governance-owned. That blacklist names the kind explicitly, so it does not
cover `cilium.io/CiliumNetworkPolicy`. Installing Cilium therefore opens a
path for a tenant to define its own policy, outside the envelope that is
supposed to be the only source. This does not block the rollout, but the
blacklist should be extended before tenants have any reason to use it.

**`docs/TENANT-MODEL.md` is currently false and stays false until step 10.**
It lists NetworkPolicy among the isolation boundaries without qualification.
That claim is wrong today and remains wrong throughout the audit period,
which is the longest phase of this plan. It should be corrected to state that
enforcement is in progress, rather than left to become true eventually.

**The VXLAN fault is untouched**, as already recorded there. Chaining leaves
Flannel's datapath exactly as it is, so the recurring cross-node blackhole
and its reboot workaround remain. Anything that fixes it is a separate
datapath decision.

## Non-goals

- **Re-deciding the engine.** That record chose chained Cilium; this one
  implements it. The Calico rejection there is additionally supported by two
  facts it did not need: kube-proxy runs in `nftables` mode on this cluster,
  which Calico's OSS iptables dataplane conflicts with, and the `canal`
  manifest that would be the vehicle was archived upstream in October 2025.
- **Replacing Flannel.** Native Cilium remains the better end state and gets
  materially cheaper once the allow-set is proven, but it is a datapath
  migration and belongs with the Talos work, not here.
- **Deleting the 115 policies.** They become real at step 10.
- **Any change to the `infrastructure` repo.** Chaining needs no Talos
  machine-config change — Flannel keeps the CNI slot and the chaining config
  ships as a ConfigMap. `cluster.extraManifests` stays empty, so the
  `--manifests-no-prune` hazard on `talosctl upgrade-k8s` is not in scope for
  this rollout.
