# ADR: Enforce NetworkPolicy with Cilium chained over Flannel

Status: accepted, not yet implemented.

## Problem

The cluster carries 115 `NetworkPolicy` objects across 24 of its 27
namespaces, and not one of them does anything. Verified live on 2026-08-03:
the only CNI running is `kube-flannel`, eight pods, one per node. Flannel
implements no policy engine at all — it programs a VXLAN datapath and
ignores `NetworkPolicy` entirely. The API server accepts every object,
`kubectl get networkpolicy` lists them, and nothing on the data path has
ever read one.

This is worse than having no policies. An unenforced policy is
indistinguishable from an enforced one at every point where anyone would
look: the manifests are in Git, the objects are in the cluster, ArgoCD
reports them Synced, and a reviewer asking "is this namespace isolated?"
finds a `default-deny-all` and concludes that it is. The policies are not a
partial control, they are a false one.

## What is actually in those 115 objects

Grouped by shape, because the shape is what determines the risk of turning
enforcement on:

| Count | Shape |
|---|---|
| 44 | Egress, empty pod selector, with rules — namespace-wide egress allowances |
| 40 | Ingress, empty pod selector, with rules — namespace-wide ingress allowances |
| 24 | Ingress **and** Egress, empty pod selector, **no rules** — `default-deny-all` |
| 7 | Ingress, specific pod selector, with rules — targeted allowances |

The 24 in the third row are the whole problem. An empty pod selector with
both policy types and no rules denies all traffic in that namespace. There
is one in every namespace that has policies at all, `kube-system` included.

They have never been tested against traffic, because nothing has ever
enforced them. The 91 allow-policies are equally untested: they were written
against an idea of what talks to what, and no observation has ever
contradicted them, because no observation was possible.

So the failure mode of "install a policy engine" is not that one namespace
loses some connectivity. It is that 24 namespaces simultaneously go
default-deny, including the one running CoreDNS and Flannel itself, gated
only on whether 91 never-exercised allow rules happen to be complete. On a
cluster of 181 running pods.

## Options considered

**Calico in policy-only mode over Flannel.** The classic `canal` shape:
Flannel keeps the datapath, Calico's controller programs iptables from the
`NetworkPolicy` objects. Conventional, well-trodden, and the smallest
component to add.

Rejected on the one property that matters here. Calico OSS has no audit or
dry-run mode — staged network policies are a Calico Enterprise feature. The
moment the controller starts, all 24 default-deny policies are live. There
is no way to learn what they would break before they break it, so the only
available rollout is to enforce and watch what falls over, on a cluster
where one of the affected namespaces is `kube-system`. That is not a
rollout plan, it is an incident with a scheduled start time.

**Replace Flannel with Cilium outright.** Full eBPF datapath, kube-proxy
replacement, and it would retire the recurring VXLAN cross-node blackhole
that has needed a node reboot several times, since that fault is a property
of Flannel's VXLAN link and would cease to exist.

Rejected for this decision, not on merit but on coupling. It requires a
machine-config change on all eight Talos nodes (`cni: none`, and a decision
about `proxy.disabled`) plus a CNI swap underneath 181 running pods. That is
a datapath migration, and this is a policy problem. Tying them together
means a policy rollback also rolls back the datapath, and it puts the
cluster's packet forwarding at risk to fix something that is currently only
a missing control. Worth doing on its own terms, and the chaining decision
below does not preclude it — Cilium moves from chained to native by changing
its own configuration, not by being replaced.

**Delete the 115 policies.** Honest, free, and immediate: it removes the
false assurance without pretending to add isolation. Rejected because the
policies are broadly correct in intent and represent real work; deleting
them discards the intent along with the falsehood, and the next person to
want isolation starts from nothing.

**Cilium in `generic-veth` CNI chaining mode.** Chosen. Flannel remains the
CNI and keeps the datapath untouched; Cilium is installed alongside it as a
policy engine only, chained after Flannel in the CNI configuration.
Upstream supports this over any CNI using a veth device model, which Flannel
does.

## Why chaining wins here

Cilium has a daemon-wide `policyAuditMode`. With it enabled, the policy
engine evaluates every packet against the loaded policies and **logs the
verdict it would have reached without acting on it** — `cilium-dbg monitor
-t policy-verdict` reports `action audit` for traffic that would have been
dropped.

That converts the unanswerable question into a measurable one. Instead of
enforcing 24 untested default-deny policies and finding out, the cluster
runs with the engine loaded and enforcing nothing, and every gap in the 91
allow-policies announces itself as an audit verdict naming the source, the
destination, and the port. The allow-set gets corrected against observed
traffic rather than against recollection, and enforcement is switched on
only once the audit log is quiet.

No other option on the list offers that, and given what the 24 policies do,
it is the difference between a staged rollout and an outage.

## Consequences

**Accepted losses.** Chaining forgoes Cilium's L7 policy enforcement and
IPsec transparent encryption. Neither is in use: all 115 policies are L3/L4,
and none expresses an L7 rule. If an L7 policy is ever wanted, that is the
point at which replacing Flannel outright earns its risk.

**Every existing pod must be restarted.** A new CNI chaining configuration
does not apply to pods that are already running. They stay reachable and
keep receiving traffic, but they get no policy enforcement — so until all
181 are restarted, enforcement coverage is partial and silently so. The
rollout has to treat the restart as a required step, and completeness has to
be asserted rather than assumed, because the failure mode is a pod that
looks protected and is not.

**A second networking component to maintain.** Cilium's version must be kept
compatible with the Kubernetes version alongside Flannel's, and both now
appear in the upgrade path.

**The VXLAN fault is not addressed.** It belongs to Flannel's datapath,
which this deliberately does not touch.

## Rollout

Ordered so that no step can drop traffic before the preceding step has shown
what it would drop:

1. Install Cilium with `chaining-mode: generic-veth` and
   `policyAuditMode=true`. Nothing is enforced.
2. Rolling-restart all workloads so every pod is managed by the chained
   configuration. Assert the count rather than assuming it — an unrestarted
   pod is an unprotected one that reads as protected.
3. Collect policy verdicts until the sample covers the slow paths as well as
   the hot ones — backup jobs, cert renewals, the unseal CronJob, scrape
   intervals. A day of traffic does not exercise a monthly job.
4. Correct the 91 allow-policies against the observed verdicts. This is the
   real work; the audit log is the specification.
5. Disable audit mode per namespace, lowest-blast-radius first.
   `kube-system` last, and only once its audit log has been silent across a
   full cycle of the slow paths.

Step 4 is where the schedule will actually go. Steps 1 and 2 are mechanical;
step 5 is a flag. The unknown is how wrong 91 untested policies are, and the
audit log is the only thing that can answer it.

## Revisit

If step 3 shows the allow-set is close to correct, replacing Flannel with
native Cilium becomes materially cheaper — the policies would already be
proven, leaving only the datapath migration, which can then be judged on the
VXLAN fault alone.
