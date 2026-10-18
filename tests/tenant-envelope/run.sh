#!/usr/bin/env bash
# Renders the tenant envelope for every tenant the way the governance
# ApplicationSet does, and asserts what the rendered network policies admit.
#
# helm-lint renders this chart with its default values, where `namespaces` is
# empty, so not one NetworkPolicy the cluster runs has ever been through CI.
# A policy that renders is not a policy that admits the right traffic, and the
# way a wrong one is found otherwise is a namespace going dark the moment it
# enforces.
#
# What is judged is the allow-set a namespace is left with once it enforces:
# the allow-all pair is set aside before a flow is resolved, so a namespace
# that is not enforcing yet is held to what it will have, not to the pair that
# currently hides it. Each case in flows.yaml is a flow somebody observed or
# a flow that must stay shut, and names its evidence.
#
# The resolver follows NetworkPolicy semantics as this cluster's engine applies
# them: an ipBlock matches addresses outside the cluster only, never a pod and
# never a node, and the API server is reachable only as an entity. It does not
# model the engine itself -- identity allocation, fragments, what a NodePort
# does to a source address. Those are checked against the live cluster, in the
# rollout the ADR beside each tenant's rules describes.
set -euo pipefail

if ! command -v helm >/dev/null 2>&1; then
  echo "helm not found: https://helm.sh/docs/intro/install/" >&2
  exit 1
fi

REPO_ROOT="$(git rev-parse --show-toplevel)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

python3 - "$REPO_ROOT" "$WORK" <<'PY'
import ipaddress
import pathlib
import subprocess
import sys

import yaml

root, work = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2])
CHART = root / "helm-charts/tenant-envelope"
FLOWS = root / "tests/tenant-envelope/flows.yaml"

# The pair a namespace carries until it enforces. Where it is only there for
# that reason it is set aside before resolving, so every case is judged
# against what enforcement leaves; `standing` holds the ones a tier keeps for
# good, which are part of that namespace's real allow-set.
ALLOW_ALL = {"allow-all-ingress", "allow-all-egress"}
standing: set[tuple[str, str]] = set()

# Everything the envelope renders by itself. A tenant's own entry reusing one
# of these names would be the same object declared twice in one Application.
ENVELOPE_POLICIES = {
    "default-deny-all",
    "allow-dns-egress",
    "allow-database-monitoring-egress",
    "allow-monitoring-ingress",
    "allow-external-egress",
    "allow-ingress-controller",
    "allow-kubelet-probes",
} | ALLOW_ALL

failures: list[str] = []


def fail(message: str) -> None:
    failures.append(message)
    print(f"  [FAIL] {message}")


def render(values: pathlib.Path, release: str) -> subprocess.CompletedProcess:
    return subprocess.run(
        ["helm", "template", release, str(CHART), "-f", str(values)],
        capture_output=True,
        text=True,
    )


def policies_of(rendered: str) -> list[dict]:
    docs = [d for d in yaml.safe_load_all(rendered) if isinstance(d, dict)]
    return [d for d in docs if d.get("kind") in ("NetworkPolicy", "CiliumNetworkPolicy")]


def selects(selector: dict | None, labels: dict) -> bool:
    selector = selector or {}
    for key, value in (selector.get("matchLabels") or {}).items():
        if labels.get(key) != value:
            return False
    for expr in selector.get("matchExpressions") or []:
        key, op, values = expr["key"], expr["operator"], expr.get("values") or []
        if op == "Exists" and key not in labels:
            return False
        if op == "DoesNotExist" and key in labels:
            return False
        if op == "In" and labels.get(key) not in values:
            return False
        if op == "NotIn" and labels.get(key) in values:
            return False
    return True


def port_matches(ports: list | None, port: int, protocol: str) -> bool:
    if not ports:
        return True
    for entry in ports:
        if entry.get("protocol", "TCP") != protocol:
            continue
        if "port" not in entry:
            return True
        low = int(entry["port"])
        high = int(entry.get("endPort", low))
        if low <= port <= high:
            return True
    return False


def peer_matches(peers: list | None, policy_namespace: str, other: dict) -> bool:
    """One `from`/`to` list of a NetworkPolicy rule against the far end."""
    if not peers:
        return True
    for peer in peers:
        if "ipBlock" in peer:
            if "ip" not in other:
                continue
            address = ipaddress.ip_address(other["ip"])
            block = peer["ipBlock"]
            if address not in ipaddress.ip_network(block["cidr"]):
                continue
            if any(address in ipaddress.ip_network(e) for e in block.get("except") or []):
                continue
            return True
        elif "pod" in other:
            pod = other["pod"]
            if "namespaceSelector" in peer:
                namespace_labels = {"kubernetes.io/metadata.name": pod["namespace"]}
                if not selects(peer["namespaceSelector"], namespace_labels):
                    continue
            elif pod["namespace"] != policy_namespace:
                continue
            if selects(peer.get("podSelector"), pod.get("labels") or {}):
                return True
    return False


def cilium_rule_matches(rule: dict, direction: str, other: dict, port: int, protocol: str) -> bool:
    prefix = "from" if direction == "ingress" else "to"
    matched = False
    entities = rule.get(f"{prefix}Entities") or []
    if "entity" in other and other["entity"] in entities:
        matched = True
    if "ip" in other:
        address = ipaddress.ip_address(other["ip"])
        if any(address in ipaddress.ip_network(c) for c in rule.get(f"{prefix}CIDR") or []):
            matched = True
    if not matched:
        return False
    to_ports = rule.get("toPorts")
    if not to_ports:
        return True
    return any(port_matches(p.get("ports"), port, protocol) for p in to_ports)


def allowed(policies: list[dict], direction: str, pod: dict, other: dict, port: int, protocol: str) -> str | None:
    """Name of the first policy admitting this flow at `pod`, or None."""
    labels = pod.get("labels") or {}
    for policy in policies:
        meta, spec = policy["metadata"], policy.get("spec") or {}
        if meta["namespace"] != pod["namespace"]:
            continue
        if meta["name"] in ALLOW_ALL and (meta["namespace"], meta["name"]) not in standing:
            continue
        if policy["kind"] == "CiliumNetworkPolicy":
            if not selects(spec.get("endpointSelector"), labels):
                continue
            for rule in spec.get(direction) or []:
                if cilium_rule_matches(rule, direction, other, port, protocol):
                    return meta["name"]
            continue
        if not selects(spec.get("podSelector"), labels):
            continue
        for rule in spec.get(direction) or []:
            peers = rule.get("from" if direction == "ingress" else "to")
            if peer_matches(peers, meta["namespace"], other) and port_matches(rule.get("ports"), port, protocol):
                return meta["name"]
    return None


def governed(policies: list[dict], namespace: str) -> bool:
    return any(p["metadata"]["namespace"] == namespace for p in policies)


# --- every tenant renders, and renders nothing twice -------------------------

rendered: dict[str, list[dict]] = {}
for tenant_file in sorted(root.glob("tenants/*/tenant.yaml")):
    tenant = tenant_file.parent.name
    result = render(tenant_file, f"governance-{tenant}")
    if result.returncode != 0:
        fail(f"{tenant}: helm template failed: {result.stderr.strip()}")
        continue
    policies = policies_of(result.stdout)
    rendered[tenant] = policies

    seen: set[tuple] = set()
    for policy in policies:
        key = (policy["kind"], policy["metadata"]["namespace"], policy["metadata"]["name"])
        if key in seen:
            fail(f"{tenant}: {key[0]} {key[1]}/{key[2]} is rendered twice")
        seen.add(key)

    values = yaml.safe_load(tenant_file.read_text())
    for namespace in values.get("namespaces") or []:
        name = namespace["name"]
        tier = namespace.get("networkPolicy", "default")
        names = {p["metadata"]["name"] for p in policies if p["metadata"]["namespace"] == name}
        extras = {
            e.get("name")
            for key in ("extraNetworkPolicies", "extraCiliumNetworkPolicies")
            for e in namespace.get(key) or []
        }
        reused = extras & ENVELOPE_POLICIES
        if reused:
            fail(f"{tenant}/{name}: extra policy reuses an envelope name: {sorted(reused)}")
        if names - ENVELOPE_POLICIES != extras:
            fail(f"{tenant}/{name}: rendered {sorted(names - ENVELOPE_POLICIES)} beyond the envelope, declared {sorted(extras)}")
        # The property the whole staged rollout rests on: the pair is there
        # until a namespace says enforce, and gone from a default-tier
        # namespace the moment it does.
        isolating = namespace.get("enforce") is True and tier == "default"
        if tier in ("platform", "ci"):
            standing.add((name, "allow-all-egress"))
        if tier == "platform":
            standing.add((name, "allow-all-ingress"))
        if isolating and names & ALLOW_ALL:
            fail(f"{tenant}/{name}: enforcing, but still renders {sorted(names & ALLOW_ALL)}")
        if not namespace.get("enforce") and not ALLOW_ALL <= names:
            fail(f"{tenant}/{name}: not enforcing, but the allow-all pair is incomplete")
    print(f"  [ok] {tenant}: {len(policies)} policies across {len(values.get('namespaces') or [])} namespace(s)")

# --- a malformed entry stops the render instead of shipping half a policy ----

for label, namespace in (
    ("an extra NetworkPolicy with no spec", {"extraNetworkPolicies": [{"name": "x"}]}),
    ("an extra NetworkPolicy with no name", {"extraNetworkPolicies": [{"spec": {"podSelector": {}}}]}),
    ("an extra CiliumNetworkPolicy with no spec", {"extraCiliumNetworkPolicies": [{"name": "x"}]}),
    ("an extra CiliumNetworkPolicy with no name", {"extraCiliumNetworkPolicies": [{"spec": {"endpointSelector": {}}}]}),
):
    fixture = work / "malformed.yaml"
    fixture.write_text(yaml.safe_dump({"name": "fixture", "namespaces": [{"name": "fixture", "quotaTier": "small", **namespace}]}))
    result = render(fixture, "governance-fixture")
    if result.returncode == 0:
        fail(f"{label} rendered; it has to fail the render")
    else:
        print(f"  [ok] {label} is refused at render")

# --- the flows ---------------------------------------------------------------

cases = (yaml.safe_load(FLOWS.read_text()) or {}).get("flows") or []
if not cases:
    sys.exit(f"{FLOWS}: no flows; an empty file is a gate that asserts nothing")

everything = [p for policies in rendered.values() for p in policies]
for case in cases:
    name, expect = case["name"], case["expect"]
    port, protocol = int(case["port"]), case.get("protocol", "TCP")
    source, destination = case["from"], case["to"]
    verdicts: list[str] = []
    reasons: list[str] = []
    judged = False
    # A flow crosses up to two policy points: egress where it leaves a pod and
    # ingress where it reaches one. Only an end inside a namespace the
    # envelope governs is judged; the rest of the cluster is not this test's.
    for direction, end, other in (("egress", source, destination), ("ingress", destination, source)):
        if "pod" not in end or not governed(everything, end["pod"]["namespace"]):
            continue
        judged = True
        by = allowed(everything, direction, end["pod"], other, port, protocol)
        verdicts.append("allow" if by else "deny")
        reasons.append(f"{direction} at {end['pod']['namespace']}: {by or 'no rule'}")
    if not judged:
        fail(f"{name}: neither end is in a governed namespace, so nothing was judged")
        continue
    actual = "allow" if all(v == "allow" for v in verdicts) else "deny"
    if actual == expect:
        print(f"  [ok] {name}: {actual} ({'; '.join(reasons)})")
    else:
        fail(f"{name}: expected {expect}, resolved {actual} ({'; '.join(reasons)})")

print(f"tenant envelope: {len(rendered)} tenant(s), {len(cases)} flow(s), {len(failures)} failure(s)")
if failures:
    sys.exit(1)
PY
