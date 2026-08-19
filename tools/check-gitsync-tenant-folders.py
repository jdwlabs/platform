#!/usr/bin/env python3
"""Keep the three lists that define a tenant's Git Sync folder in agreement.

A tenant dashboard folder is described in three places that nothing linked
until this check existed:

  1. TENANT_REPOSITORIES in the grafana service's gitsync-apply-job.yaml —
     which repositories the apply Job creates, and from which ConfigMap key.
  2. The Repository definitions in gitsync-resources.yaml — the name Grafana
     gives the folder (metadata.name IS the folder UID under sync.target
     folder) and the path it syncs.
  3. observability.grafana.gitSyncFolder in tenants/<t>/tenant.yaml — the
     folder tenant-envelope grants that tenant's team on.

Any one of them missing is silent and unsafe rather than loud. A repository
created without a matching gitSyncFolder produces a folder carrying Grafana's
inherited Admin/Editor/Viewer grants, readable by every user in the instance;
the apply Job exits 0, ArgoCD is green, and nothing in the cluster reports it.
A gitSyncFolder without a repository is the mirror image: a tenant-envelope
hook waiting on a folder that will never appear, failing that tenant's
observability sync every time.

Usage:
    python3 tools/check-gitsync-tenant-folders.py

Exit codes: 0 = all three agree; 1 = a mismatch was found.
"""
import json
import re
import sys
from pathlib import Path

import yaml

REPO_ROOT = Path(__file__).resolve().parent.parent
GRAFANA_SERVICE = REPO_ROOT / "tenants/platform/services/grafana"
APPLY_JOB = GRAFANA_SERVICE / "postInstall/gitsync-apply-job.yaml"
RESOURCES = GRAFANA_SERVICE / "postInstall/gitsync-resources.yaml"
TENANTS_DIR = REPO_ROOT / "tenants"
MOUNT_PATH = "/etc/gitsync/"

# The Job assigns both lists as shell heredoc-free string literals; the tenant
# one may span lines because each entry is "name:file".
LIST_RE = {
    "required": re.compile(r'^\s*REQUIRED_REPOSITORY="([^"]*)"', re.MULTILINE),
    "tenant": re.compile(r'^\s*TENANT_REPOSITORIES="([^"]*)"', re.MULTILINE),
}


def job_script(path: Path) -> str:
    doc = yaml.safe_load(path.read_text(encoding="utf-8"))
    return doc["spec"]["template"]["spec"]["containers"][0]["command"][-1]


def parse_entries(script: str, which: str) -> dict:
    """name -> ConfigMap key, from one of the Job's repository lists."""
    match = LIST_RE[which].search(script)
    if not match:
        raise SystemExit(f"{APPLY_JOB}: no {which.upper()} list found — has the Job been restructured?")
    entries = {}
    for token in match.group(1).split():
        name, _, mount = token.partition(":")
        if not name or not mount.startswith(MOUNT_PATH):
            raise SystemExit(f"{APPLY_JOB}: entry {token!r} is not name:{MOUNT_PATH}<key>")
        entries[name] = mount[len(MOUNT_PATH):]
    return entries


def repository_definitions(path: Path) -> dict:
    """ConfigMap key -> parsed Repository JSON."""
    doc = yaml.safe_load(path.read_text(encoding="utf-8"))
    out = {}
    for key, body in doc["data"].items():
        parsed = json.loads(body)
        if parsed.get("kind") == "Repository":
            out[key] = parsed
    return out


def tenant_claims() -> dict:
    """gitSyncFolder -> [tenant, ...], across every tenant.yaml."""
    claims = {}
    for path in sorted(TENANTS_DIR.glob("*/tenant.yaml")):
        doc = yaml.safe_load(path.read_text(encoding="utf-8")) or {}
        folder = (doc.get("observability") or {}).get("grafana", {}).get("gitSyncFolder")
        if folder:
            claims.setdefault(folder, []).append(doc.get("name", path.parent.name))
    return claims


def main() -> int:
    script = job_script(APPLY_JOB)
    required = parse_entries(script, "required")
    tenant_repos = parse_entries(script, "tenant")
    definitions = repository_definitions(RESOURCES)
    claims = tenant_claims()

    problems = []

    # Every listed repository must have a definition whose name matches, since
    # the Job probes by name and POSTs the body: a pair that disagrees creates
    # one resource and health-gates another.
    for name, key in {**required, **tenant_repos}.items():
        definition = definitions.get(key)
        if definition is None:
            problems.append(f"{name}: the Job mounts {MOUNT_PATH}{key}, which {RESOURCES.name} does not define")
            continue
        declared = definition.get("metadata", {}).get("name")
        if declared != name:
            problems.append(f"{name}: {key} declares metadata.name {declared!r} — the Job would create it and gate on a different resource")

    for key in definitions:
        if key not in {**required, **tenant_repos}.values():
            problems.append(f"{key}: defined in {RESOURCES.name} but no list in the Job creates it")

    # The boundary check: repository and grant are declared in different files
    # and only both together make a tenant folder private.
    for name in tenant_repos:
        owners = claims.get(name, [])
        if not owners:
            problems.append(
                f"{name}: no tenant sets observability.grafana.gitSyncFolder to it — "
                "its folder would be readable by every grafana user"
            )
        elif len(owners) > 1:
            problems.append(f"{name}: claimed by more than one tenant ({', '.join(owners)})")

    for folder, owners in sorted(claims.items()):
        if folder not in tenant_repos:
            problems.append(
                f"{folder}: claimed by tenant {', '.join(owners)} but no repository creates it — "
                "that tenant's folder-rbac hook would wait for a folder that never appears"
            )
            continue
        # One tenant, one path: the grant is what makes the folder private, so
        # it has to be the folder holding that tenant's dashboards.
        tenant = owners[0]
        definition = definitions.get(tenant_repos[folder])
        if definition is None:
            continue  # already reported above as a missing definition
        want = f"observability/dashboards/{tenant}"
        got = definition.get("spec", {}).get("github", {}).get("path")
        if got != want:
            problems.append(f"{folder}: tenant {tenant} grants it, but it syncs {got!r} rather than {want!r}")
        elif not (REPO_ROOT / want).is_dir():
            problems.append(f"{folder}: syncs {want}, which does not exist in this tree")

    if problems:
        print("GIT SYNC TENANT FOLDER MISMATCH:")
        for problem in problems:
            print(f"  {problem}")
        print(
            "\nA tenant folder needs all three: a TENANT_REPOSITORIES entry, a Repository "
            "definition of the same name, and a tenant.yaml gitSyncFolder claiming it."
        )
        return 1

    print(
        f"gitsync-tenant-folders: {len(tenant_repos)} tenant folder(s) "
        f"({', '.join(sorted(tenant_repos))}) each created, defined and granted"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
