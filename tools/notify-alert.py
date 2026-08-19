#!/usr/bin/env python3
"""Post one alert to Alertmanager so a scheduled workflow can reach a person.

A scheduled workflow has no audience. Nothing watches the Actions tab, no
review blocks on it, and a cron that goes red stays red without anybody being
told — the failure and the silence look identical from outside. Alertmanager is
the one bus in this system that already fans out to both a human and the AI-SRE
relay, so a workflow that wants to be heard posts there rather than inventing a
second notification path nobody would maintain.

Routing is decided entirely by the labels on the alert, by the route tree in
the platform repo's Alertmanager config:

  * `severity: critical` with no tenant reaches the relay *and* Discord.
  * `severity: warning` with no tenant reaches the relay only — deliberately,
    so warnings never page.
  * `tenant: jdwlabs` reaches Discord at any severity.

That is why severity is a required argument with no default. Picking it is the
whole decision about who finds out, and a default would make that choice
silently, once, for every caller that came later.

Alerts posted here carry no `endsAt`, so Alertmanager expires them on its own
`resolve_timeout`. That is intended: each run is a discrete statement about the
state at that moment, and a caller that stops posting stops alerting. It also
means this cannot detect a schedule that has stopped firing altogether — an
alert nobody sends is indistinguishable from one nobody needed. That gap
belongs to an `absent()` rule next to the other Prometheus rules, not here.

Delivery failures are loud. A notification that quietly does not arrive is the
exact condition this exists to end, so a non-2xx response or an exhausted retry
budget exits non-zero and prints what came back.
"""

import argparse
import json
import os
import sys
import time
import urllib.error
import urllib.request
from datetime import datetime, timezone

DEFAULT_ENDPOINT = "https://alertmanager.jdwlabs.com"

# Posting is a single small JSON body against an endpoint on the same
# continent; anything past a few seconds is a failure worth reporting rather
# than one worth waiting out.
DEFAULT_TIMEOUT_SECONDS = 10

# Retries exist for a dropped connection, not for a rejected payload: a 4xx
# means the alert was understood and refused and will be refused identically
# next time, so only transport errors and 5xx are retried.
DEFAULT_RETRIES = 3
RETRY_BACKOFF_SECONDS = 2

SEVERITIES = ("critical", "warning", "info")


class NotifyError(RuntimeError):
    """A delivery attempt that will not succeed by being repeated."""


def parse_pairs(values: list[str], what: str) -> dict[str, str]:
    """Turn repeated `key=value` arguments into a dict.

    Split on the first `=` only, so an annotation may contain one — descriptions
    routinely carry URLs with query strings.
    """
    pairs: dict[str, str] = {}
    for raw in values:
        key, sep, value = raw.partition("=")
        if not sep or not key:
            raise NotifyError(f"{what} must be KEY=VALUE, got {raw!r}")
        pairs[key] = value
    return pairs


def build_alert(
    alertname: str,
    severity: str,
    summary: str,
    description: str | None = None,
    labels: dict[str, str] | None = None,
    annotations: dict[str, str] | None = None,
    now: datetime | None = None,
) -> dict:
    """Build one Alertmanager v2 alert.

    `alertname` and `severity` are set last so an explicit label cannot
    overwrite them: they are the two fields the route tree matches on, and a
    caller silently redirecting its own alert by passing `--label severity=info`
    would be a routing bug with no symptom.
    """
    alert_labels = dict(labels or {})
    alert_labels["alertname"] = alertname
    alert_labels["severity"] = severity

    alert_annotations = dict(annotations or {})
    alert_annotations["summary"] = summary
    if description:
        alert_annotations["description"] = description

    started = (now or datetime.now(timezone.utc)).astimezone(timezone.utc)
    return {
        "labels": alert_labels,
        "annotations": alert_annotations,
        "startsAt": started.isoformat().replace("+00:00", "Z"),
    }


def post(
    alert: dict,
    endpoint: str,
    token: str | None = None,
    timeout: float = DEFAULT_TIMEOUT_SECONDS,
    retries: int = DEFAULT_RETRIES,
    opener=urllib.request.urlopen,
    sleep=time.sleep,
) -> None:
    """POST the alert, retrying transport failures and 5xx."""
    url = endpoint.rstrip("/") + "/api/v2/alerts"
    body = json.dumps([alert]).encode()
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = f"Bearer {token}"

    last: str = "no attempt was made"
    for attempt in range(1, max(1, retries) + 1):
        request = urllib.request.Request(url, data=body, headers=headers, method="POST")
        try:
            with opener(request, timeout=timeout) as response:
                if 200 <= response.status < 300:
                    return
                last = f"HTTP {response.status}: {response.read().decode(errors='replace')[:500]}"
        except urllib.error.HTTPError as exc:
            detail = exc.read().decode(errors="replace")[:500]
            if exc.code < 500:
                raise NotifyError(f"HTTP {exc.code} from {url}: {detail}") from exc
            last = f"HTTP {exc.code}: {detail}"
        except (urllib.error.URLError, OSError, TimeoutError) as exc:
            last = f"{type(exc).__name__}: {exc}"

        if attempt < max(1, retries):
            sleep(RETRY_BACKOFF_SECONDS * attempt)

    raise NotifyError(f"giving up on {url} after {max(1, retries)} attempts — {last}")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Post one alert to Alertmanager."
    )
    parser.add_argument("--alertname", required=True, help="alert name, matched by the route tree")
    parser.add_argument(
        "--severity", required=True, choices=SEVERITIES,
        help="who finds out: critical reaches Discord and the relay, warning the relay alone",
    )
    parser.add_argument("--summary", required=True, help="one-line summary annotation")
    parser.add_argument("--description", help="longer description annotation")
    parser.add_argument(
        "--label", action="append", default=[], metavar="KEY=VALUE",
        help="extra label; repeatable (tenant=jdwlabs also reaches Discord)",
    )
    parser.add_argument(
        "--annotation", action="append", default=[], metavar="KEY=VALUE",
        help="extra annotation; repeatable",
    )
    parser.add_argument(
        "--endpoint", default=os.environ.get("ALERTMANAGER_URL", DEFAULT_ENDPOINT),
        help=f"Alertmanager base URL (ALERTMANAGER_URL, default {DEFAULT_ENDPOINT})",
    )
    parser.add_argument("--timeout", type=float, default=DEFAULT_TIMEOUT_SECONDS)
    parser.add_argument("--retries", type=int, default=DEFAULT_RETRIES)
    parser.add_argument(
        "--dry-run", action="store_true",
        help="print the alert that would be posted and send nothing",
    )
    args = parser.parse_args(argv)

    try:
        alert = build_alert(
            alertname=args.alertname,
            severity=args.severity,
            summary=args.summary,
            description=args.description,
            labels=parse_pairs(args.label, "--label"),
            annotations=parse_pairs(args.annotation, "--annotation"),
        )
    except NotifyError as exc:
        print(f"notify-alert: {exc}", file=sys.stderr)
        return 2

    if args.dry_run:
        print(json.dumps([alert], indent=2))
        return 0

    try:
        post(
            alert,
            endpoint=args.endpoint,
            # Alertmanager is unauthenticated on its public route today. Reading
            # a token when one is present means adding auth later is a secret to
            # set rather than a change to every caller.
            token=os.environ.get("ALERTMANAGER_TOKEN") or None,
            timeout=args.timeout,
            retries=args.retries,
        )
    except NotifyError as exc:
        print(f"notify-alert: {exc}", file=sys.stderr)
        return 1

    print(f"notify-alert: posted {args.alertname} ({args.severity})")
    return 0


if __name__ == "__main__":
    sys.exit(main())
