# ADR: Enforcing `jdwillmsen-prd` — the flow inventory, and why it lands in two steps

Status: accepted, step 1 only. Extends
[chained-cilium-rollout-corrections](0017-chained-cilium-rollout-corrections.md)
and [cilium-managed-endpoint-coverage](0028-cilium-managed-endpoint-coverage.md).
The engine, the `enforce` flag and the coverage precondition all stand. This
record adds the namespace those records did not cover, and the one thing their
rollouts did not have: a way to see what enforcement would drop before it
drops it.

## Problem

`jdwillmsen-prd` is the last tenant namespace not enforcing. It runs a
Minecraft Bedrock server people play on, and its tenant file said outright
that nobody knew whether the scoped egress rule covered the server's sign-in
path.

The four namespaces enforced so far each run one or two HTTP services with a
database behind them, and three of the four attempts on them still
crashlooped a workload before a root cause was found. This namespace is a
different shape: a UDP game port reached through a NodePort, six CronJobs and
a sync hook that drive the server through the cluster's API, long-lived
sessions between eight workloads, and a caller on the LAN. Enforcing it the
way the others were enforced, by setting the flag and watching, would find its
gaps by locking players out.

## What was read

Three sources, reconciled against each other.

**The chart.** `charts/minecraft-fwb` in the tenant's deployment repo at
`593e304`, rendered with the four value files its Application names. It ships
no NetworkPolicy today; the one it carries for the map's browser checks is
behind a switch that is off.

**The cluster**, read only: pods, Services, endpoints, policies,
CiliumEndpoints, the agent's own configuration.

**Traffic.** Hubble, from the `cilium-agent` pods on the two nodes the
namespace runs on. There is no relay, and each agent's ring holds 4095 flows —
about ninety seconds at the rate measured — so history does not exist and
everything below was captured by following the stream:

| Window (UTC) | Length | Covers |
|---|---|---|
| 2026-10-12 14:50–15:31 | 41 min | steady state, eight volume-recovery runs, one hourly version check |
| 2026-10-13 02:05–04:08 | 2 h 3 min | steady state, the map being served, two version checks, the nightly backup, one deploy with its sync hook, and the agent signing in again after Postgres restarted under it |

**Not observed:** the 16:40 scheduled restart, and with it the server
downloading a release and the bots signing in to Xbox Live again; the 05:40
census; the map fetching its icons, though it was replaced inside the second
window and made no connection out in the eighteen minutes left of it; a player asking
the agent a question; a player joining from a new address. The first capture
was meant to span the restart and was cut short an hour before it. Those rows
are marked *chart* below: they are what the chart says happens, not what was
seen.

## How the engine classifies what arrives

Four facts decide which rules can work at all. Each was read off live flows.

1. **A Service address is already a pod address when policy sees it.** The
   agent's connection to `…-rw.database:5432` is judged as a connection to the
   Postgres pod. Rules select pods, not Service addresses.
2. **The API server is a node, and an `ipBlock` does not match a node.** A Job
   dialling `kubernetes.default:443` is judged as TCP 6443 to a control-plane
   node carrying `reserved:kube-apiserver`. `PolicyCIDRMatchMode` is empty, so
   no CIDR rule selects it; only the `kube-apiserver` entity does, and vanilla
   NetworkPolicy cannot name an entity.
3. **A player is whichever node they came in through.** The game Services are
   `externalTrafficPolicy: Cluster`, so kube-proxy rewrites the source of
   everything entering a NodePort. Traffic that entered at a node other than
   the server's arrives as that node's tunnel address, `10.244.N.0`, with the
   identity `cidr:10.244.0.0/16, reserved:world`. That is what was seen
   arriving on the game port, once a minute, in every window. Traffic entering
   at the server's own node arrives as the bridge address `10.244.N.1`, which
   `allow-kubelet-probes` already admits on every port — by accident, not by
   design.
4. **Kubelet probes are still `world`**, arriving from each node's bridge
   address as the comment in `kubelet-probe-policy.yaml` records, and still
   admitted by `allow-kubelet-probes`. Nothing changes there.

## The inventory

*Today* is the rule that would admit the flow if the namespace enforced with
the tiers as they are. *Step 1* is the rule this change adds.

### Into the namespace

| From | To | Port | Seen | Today | Step 1 |
|---|---|---|---|---|---|
| Players, through a NodePort on another node | game server | UDP 31132 | hubble | **denied** | `allow-game-ingress` |
| Players, through the server's own node | game server | UDP 31132 | chart | `allow-kubelet-probes`, by accident | `allow-game-ingress` |
| Players, second transport | game server | TCP 31132, UDP 31160–31179 | chart — the server does not speak it today | **denied** | `allow-game-nethernet-ingress` |
| Prometheus | all seven scrape targets: 8080, 8766, 9090, 9101, 9102, 9103 | TCP | hubble | `allow-monitoring-ingress` | — |
| Gateway | map | TCP 8080 | hubble | `allow-ingress-controller` | — |
| Kubelet | every probed container | TCP | hubble | `allow-kubelet-probes` | — |

### Inside the namespace

Every row is **denied** today, at both ends: the `default` tier has no rule
for a namespace's own pods. All are admitted by `allow-same-namespace`.

| From | To | Port | Seen |
|---|---|---|---|
| agent, both bots, join probe | game server | UDP 31132 | hubble |
| agent, map | console bridge (in the server pod) | TCP 8766 | hubble |
| agent | server monitor (in the server pod) | TCP 8080 | hubble |
| agent | map internal | TCP 9090 | hubble |
| agent | backup exporter | TCP 9101 | hubble |
| both bots, map | agent | TCP 9090 | hubble |

The join probe's handshake is larger than the pod network's MTU and arrives
as two fragments. The second carries no port. An all-ports rule between the
namespace's pods admits it without depending on fragment tracking; that is one
of the reasons the rule is not written port by port.

### Out of the namespace

| From | To | Port | Seen | Today | Step 1 |
|---|---|---|---|---|---|
| every pod | CoreDNS | UDP 53 | hubble | `allow-dns-egress` | — |
| agent | Postgres, `database` | TCP 5432 | hubble | `allow-database-monitoring-egress` | — |
| backup, census, restore, scheduled restart, version check, volume recovery, sync hook | API server | TCP 6443 | hubble for volume recovery, version check, backup and the sync hook; chart for census, restore and the scheduled restart | **denied** | `allow-job-kube-apiserver-egress` |
| game server | Microsoft (`4.150.223.110`, `.112`, `.113`, `20.184.175.9`, `.12`) | TCP 443 | hubble | `allow-external-egress` | — |
| version check | GitHub API | TCP 443 | hubble | `allow-external-egress` | — |
| version check's candidate server | Microsoft and its CDN, to download the release | TCP 443 | hubble | `allow-external-egress` | — |
| agent | Xbox Live and Minecraft sign-in (six Microsoft addresses in `20.201.0.0/16`, `40.122.0.0/16`, `150.171.110.0/24`, `23.213.0.0/16`) | TCP 443 | hubble, once | `allow-external-egress` | — |
| both bots | Xbox Live and Minecraft sign-in | TCP 443 | chart | `allow-external-egress` | — |
| map | `api.github.com`, `raw.githubusercontent.com` for icons | TCP 443 | chart — a fresh map pod made no such call while watched | `allow-external-egress` | — |
| agent | inference host, `192.168.1.50` | TCP 8000 | chart | **denied** — the external allow excludes the LAN | `allow-agent-inference-egress` |

Nothing left the cluster on any port but 443, and nothing left it over UDP,
in either window. No NTP, no STUN, no plain HTTP. That answers the question
the tenant file left open for the steady state, for the candidate server's
download and for the agent's sign-in. It does not answer it for the bots'
sign-in or for the server's own start, neither of which was seen.

What goes to storage does not appear: the volumes are mounted by the kubelet,
on the node, and no pod opens a connection to TrueNAS. ExternalSecrets are
resolved by an operator in another namespace through the API; no pod here
talks to Vault.

## What enforcing as-is would have done

Three things, none of them subtle:

- **Every in-cluster session with the server would end.** The agent, both
  bots, the join probe and the map all lose the server and each other.
- **Every Job would fail at its first API call.** Volume recovery runs every
  five minutes. The backup would stop. The sync hook is a PreSync Job, so the
  tenant's Application could no longer sync at all — including the sync that
  would carry a fix.
- **Players entering through any node but the server's own would be
  dropped.** Those entering through the server's node would keep working,
  which is the kind of partial failure that reads as a client problem.

## Decision

**1. The envelope gains two per-namespace lists, `extraNetworkPolicies` and
`extraCiliumNetworkPolicies`, rendered as written.** No existing option could
express any of the four gaps, and the tenant cannot ship the policies itself:
its AppProject denies both kinds, which is what keeps isolation
governance-owned. Typed switches were considered — one for same-namespace
traffic, one for the API — and would have needed two more, shaped around one
chart's labels and ports, for the game port and the inference host. A list in
a code-owner-gated file is less template for the same review.

Every other tenant renders byte-for-byte what it rendered before; the lists
are absent there and the loops emit nothing.

**2. Same-namespace traffic is admitted whole, not pair by pair.** The pairs
are the deployment chart's, they change when it changes, and a pair missing
here is an outage only a platform change can end. The namespace is the unit
the tenant model isolates.

**3. The game port names no source.** After the NodePort rewrite the only
sources that could be named are node addresses, and anything that can reach a
node's port arrives as one. A list of them would restrict nothing and would
lock players out the day a node is added.

**4. API access is for Job pods only, on 6443 only.** Only their service
accounts are bound to anything. The entity without a port would open every
port on the control-plane nodes.

**5. It lands in two steps, and this change is the first.** Policies only add.
While the allow-all pair is rendered, every rule above can be applied, read
back and watched matching real traffic with nothing depending on it. Step 2 is
one line.

What makes step 1 more than a formality is that Hubble reports *how* a flow
was admitted. A verdict carries a match type, and a flow admitted only by an
allow-all rule reports `all`. Run today, the filter in the rollout below
returns exactly the two cross-boundary gaps the inventory predicts — Jobs to
the API server, and players through another node — and nothing else. After
step 1 it should return nothing. That is the per-namespace audit mode
[0017](0017-chained-cilium-rollout-corrections.md) found Cilium does not have.

It has a blind spot, and it is stated here so nobody trusts it past it: a rule
that names a peer but no port also reports `all` while the pair is present.
The filter therefore leaves out the peers such rules cover — the namespace
itself, `monitoring`, `nginx-gateway`, the kubelet addresses — and those are
verified by the test added with this change and by the drop query in step 2.

## Rollout

Commands are read-only. `$AGENT` is the `cilium-agent` pod on the node the
server runs on; repeat on the node running the rest of the namespace.

### Step 1 — the allows (this change)

Changes no verdict. After `governance-jdwillmsen` syncs:

1. Five new policies exist beside the old nine, and the Cilium one is valid:
   `kubectl -n jdwillmsen-prd get networkpolicy,ciliumnetworkpolicy`
2. The engine took the port rules, the range included. On the server's
   endpoint, ingress entries for `31132/UDP`, `31132/TCP` and
   `31160-31179/UDP`:
   `kubectl -n kube-system exec $AGENT -c cilium-agent -- cilium-dbg endpoint list`
   for the endpoint ID, then `… cilium-dbg bpf policy get <id>`.
3. Nothing is admitted by the allow-all pair alone. Follow for at least ten
   minutes, so that a volume-recovery run falls inside it:

   ```
   kubectl -n kube-system exec $AGENT -c cilium-agent -- \
     hubble observe --follow --namespace jdwillmsen-prd --type policy-verdict -o jsonpb \
   | jq -r '.flow
     | select(.policy_match_type == 4)
     | select((.traffic_direction == "EGRESS" and .source.namespace == "jdwillmsen-prd")
           or (.traffic_direction == "INGRESS" and .destination.namespace == "jdwillmsen-prd"))
     | (if .traffic_direction == "EGRESS" then .destination else .source end) as $peer
     | select($peer.namespace != "jdwillmsen-prd")
     | select(.traffic_direction == "EGRESS"
           or (($peer.namespace // "") | IN("monitoring", "nginx-gateway") | not))
     | select(.traffic_direction == "EGRESS"
           or (($peer.labels // []) | any(test("^cidr:10\\.244\\.[0-9]+\\.1/32$")) | not))
     | [.traffic_direction, (.source.pod_name // "outside"), (.destination.pod_name // "outside"),
        ((.l4 // {}) | to_entries | map("\(.key)/\(.value.destination_port // "-")") | join(","))]
     | @tsv'
   ```

   Any line is a flow step 2 would drop. It prints no address.
4. **Hold step 2 until this has run across a 16:40 restart** and stayed empty,
   and until `hubble observe --namespace jdwillmsen-prd --type trace` across
   the same restart has shown nothing leaving the cluster but TCP 443. The
   filter cannot see that second part: an external flow on another port is
   admitted by the pair like everything else and has no rule to be missing.
   The restart is the server's start and the bots' sign-in, which no window
   caught.

*Rollback:* revert the commit. Nothing depends on the policies.

### Step 2 — `enforce: true`

One line in `tenants/jdwillmsen/tenant.yaml`. Open its revert before merging
it: the path is code-owner gated, so a revert written during an outage waits
on a review, and the earlier attempts on other namespaces were each reverted
inside about ninety seconds.

Not during the 03:50–06:10 backup and census window, nor within the half hour
before 16:40. With someone in the game.

Immediately after sync:

1. `allow-all-ingress` and `allow-all-egress` are gone from the namespace.
2. Drops, on both nodes, for fifteen minutes:
   `kubectl -n kube-system exec $AGENT -c cilium-agent -- hubble observe --follow --namespace jdwillmsen-prd --verdict DROPPED`
   The ICMPv6 `ff02::16` lines from Job pods are there today and are not
   policy.
3. What a player sees: a join from a device that was not connected; the two
   bots still in the player list; the map at its public hostname showing live
   positions; an `@server` question answered in chat, which is the only test
   of the inference rule there is.
4. The next volume-recovery Job completes — at most five minutes away — and
   the next hourly version check.
5. No restart count in the namespace moved, and neither
   `JdwillmsenMinecraftUnjoinable` nor `JdwillmsenMinecraftAgentSessionStale`
   is pending.

Then, on the day: the 16:40 restart and the sign-ins after it, the 04:00
backup, the 05:40 census, and the first deploy, whose hook is the first Job
that can block a sync.

*Rollback:* merge the revert. The envelope renders the pair again, traffic is
admitted on the next packet, and nothing needs restarting. Sessions that were
dropped reconnect by themselves; the bots and the agent retry.

### What cannot be known before step 2

- **The restart.** The agent's sign-in was seen once and was HTTPS only; the
  bots' and the server's own start were not. If a drop appears from any of
  them to anything but TCP 443, that is the unverified path, and the revert
  comes first.
- **The inference rule.** Never seen carrying traffic.
- **A player's first packets through a NodePort under a port-only rule.** The
  handshake probes for the path MTU with datagrams that can fragment, and the
  later fragments carry no port. Fragment tracking is on
  (`EnableIPv4FragmentsTracking`) and is what makes that work; it has not been
  exercised by this path while it mattered.
- **A player from outside the LAN**, if there is such a route. None was
  identifiable: every arrival looks like a node.
- **The second transport.** Its ports are open inwards. A server that opens
  connections out to its players, as that transport's negotiation may, has no
  egress rule for it. Add one before switching the transport back, not after.

## Consequences

**The chart's labels are now an interface.** `allow-game-ingress` selects
`app: jdwillmsen-minecraft-fwb-prd-minecraft-bedrock` and the inference rule
selects the agent's. Renaming the release or the workloads in the deployment
repo silently unselects them, and under enforcement that is players locked
out. The flow table in `tests/tenant-envelope/flows.yaml` pins the labels on
this side; nothing pins them on the other.

**The map's browser checks cannot be turned on as designed.** The chart
refuses to render them until told the namespace enforces, because their own
NetworkPolicy is what confines the browser. After step 2 the namespace does
enforce, and that policy still cannot do its job, for two reasons that hold
whichever comes first. The tenant AppProject denies NetworkPolicy, so an
Application that renders one stops syncing — the whole game chart, not only
the checks. And policies only add: the namespace-wide allows here, the
same-namespace rule and the external allow among them, select the checks' pod
too, so it could reach the game server and the internet whatever its own
policy says. Setting `map.checks.networkPolicyEnforced` after step 2 would be
true and would not deliver what the switch promises. Confining one pod inside
an enforcing namespace needs the envelope to exempt it from the wide rules,
which is a separate decision.

**Every Job run mints a security identity**, because its `job-name` label is
part of it — one every five minutes from volume recovery alone. That is true
today and is why the API rule can select on the label at all. It is churn the
operator absorbs, not a fault, and it is recorded so the next person who sees
the identity count does not go looking for a leak.

**The envelope can now be widened per namespace by anyone who can change a
tenant file.** That file is code-owner gated, which is the control. The lists
can admit anything a policy can express, so the review of an entry is the
review of the flow it admits.
