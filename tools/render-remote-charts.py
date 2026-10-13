#!/usr/bin/env python3
"""Render every third-party Helm release this repo deploys, so a chart bump
that cannot template fails the PR instead of ArgoCD.

`helm-lint` only walks helm-charts/, this repo's own charts. A remote chart is
never templated by any gate, so a `revision` bump whose chart no longer
renders, or a values key the chart now rejects, merges and is applied by an
ArgoCD Application that runs with prune and selfHeal: there is no manual step
between a broken render and the cluster. Charts that ship no
values.schema.json do not even error on a removed key; they ignore it, which
is why the render is the check and not just `helm lint`.

The release list is not restated here. It is read through
check-remote-chart-image-pins.py's discover_releases(), i.e. from the
`chart`/`repo`/`revision` pins in tenants/*/tenant.yaml and the values file at
tenants/<tenant>/services/<service>/values.yaml, so bumping a pin cannot leave
a second list behind. That also means a new remote chart is covered the
moment it is added to a tenant.

Each release is rendered the way the ArgoCD Application does it: release name
is the service name, namespace is the service namespace, CRDs are included,
and the repo's values file is the only override.

Usage:
    python3 tools/render-remote-charts.py [--jobs N] [--kube-version vX.Y.Z]

Exit codes: 0 = every release rendered; 1 = at least one release failed to
render or could not be fetched. A fetch failure is reported as a failure, not
skipped: a chart that was never rendered has not been checked.
"""
import argparse
import subprocess
import sys
import time
from concurrent.futures import ThreadPoolExecutor
from importlib.util import module_from_spec, spec_from_file_location
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent

# Reuse the sibling checker's release discovery rather than parsing
# tenant.yaml a second time. Loaded by path because the filename is not a
# legal module name, and not registered in sys.modules so it cannot shadow the
# copy its own tests load.
_spec = spec_from_file_location(
    "check_remote_chart_image_pins", REPO_ROOT / "tools/check-remote-chart-image-pins.py"
)
_remote = module_from_spec(_spec)
_spec.loader.exec_module(_remote)

# Helm's built-in default targets an old Kubernetes, which makes charts with a
# kubeVersion constraint (kube-prometheus-stack requires >=1.25) fail here for
# a reason that cannot occur on the cluster. Overridable so the render tracks
# the cluster's minor.
DEFAULT_KUBE_VERSION = "v1.34.0"

# A cold chart fetch plus render of the largest chart takes tens of seconds; a
# hung museum must not hold the job until the runner's own timeout.
RENDER_TIMEOUT = 300
TAIL_LINES = 25


def chart_args(release: dict) -> list[str]:
    """Arguments naming the chart and its pinned version.

    OCI registries take the chart in the reference; classic repositories take
    `--repo`, which avoids mutating the runner's repo list with `repo add`.
    """
    repo = release["repo"].rstrip("/")
    if repo.startswith("oci://") or not repo.startswith(("http://", "https://")):
        base = repo.removeprefix("oci://")
        return [f"oci://{base}/{release['chart']}", "--version", release["revision"]]
    return [release["chart"], "--repo", repo, "--version", release["revision"]]


def render(release: dict, kube_version: str) -> dict:
    overlay = REPO_ROOT / release["overlay"]
    command = [
        "helm", "template", release["name"], *chart_args(release),
        "--namespace", release["namespace"],
        "--kube-version", kube_version,
        "--include-crds",
    ]
    if overlay.exists():
        command += ["-f", str(overlay)]
    started = time.monotonic()
    try:
        done = subprocess.run(
            command, capture_output=True, text=True, timeout=RENDER_TIMEOUT, check=False
        )
        ok, detail = done.returncode == 0, done.stderr
        if ok and not done.stdout.strip():
            # An empty render exits 0 but deploys nothing, which is never what
            # a release in tenant.yaml intends.
            ok, detail = False, "helm template produced no manifests"
    except subprocess.TimeoutExpired:
        ok, detail = False, f"timed out after {RENDER_TIMEOUT}s"
    return {
        "release": release,
        "ok": ok,
        "seconds": time.monotonic() - started,
        "detail": detail,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--jobs", type=int, default=6, help="parallel renders (default 6)")
    parser.add_argument("--kube-version", default=DEFAULT_KUBE_VERSION)
    parser.add_argument(
        "--only", action="append", default=[], metavar="TENANT/SERVICE",
        help="render just this service (repeatable)",
    )
    args = parser.parse_args()

    releases = _remote.discover_releases()
    if args.only:
        releases = [r for r in releases if f"{r['tenant']}/{r['name']}" in args.only]
    if not releases:
        print("no remote chart releases found", file=sys.stderr)
        return 1

    started = time.monotonic()
    with ThreadPoolExecutor(max_workers=max(1, args.jobs)) as pool:
        results = list(pool.map(lambda r: render(r, args.kube_version), releases))

    failed = [r for r in results if not r["ok"]]
    for result in results:
        release = result["release"]
        print(
            f"{'ok  ' if result['ok'] else 'FAIL'} {release['tenant']}/{release['name']} "
            f"({release['chart']} {release['revision']}) {result['seconds']:.1f}s"
        )
    print(
        f"\n{len(results) - len(failed)}/{len(results)} remote releases rendered "
        f"in {time.monotonic() - started:.1f}s"
    )

    for result in failed:
        release = result["release"]
        lines = result["detail"].strip().splitlines()[-TAIL_LINES:]
        print(f"\n--- {release['tenant']}/{release['name']} ({release['chart']} {release['revision']})")
        print("\n".join(lines))
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
