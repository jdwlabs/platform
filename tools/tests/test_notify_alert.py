#!/usr/bin/env python3
"""Unit tests for tools/notify-alert.py.

Every test below pins one way a notification could fail to arrive while the
step that sent it still reported success — a label that quietly rerouted the
alert away from its audience, a 5xx swallowed as delivery, an exhausted retry
budget returning 0. A notifier that lies about delivery is worse than none,
because it is the thing everyone stops checking. Run with:

    python3 -m unittest discover -s tools/tests -t tools/tests
"""

import importlib.util
import unittest
import unittest.mock
import urllib.error
from datetime import datetime, timezone
from io import BytesIO
from pathlib import Path

MODULE_PATH = Path(__file__).resolve().parent.parent / "notify-alert.py"
_spec = importlib.util.spec_from_file_location("notify_alert", MODULE_PATH)
na = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(na)


class FakeResponse:
    def __init__(self, status: int, body: bytes = b""):
        self.status = status
        self._body = body

    def read(self) -> bytes:
        return self._body

    def __enter__(self):
        return self

    def __exit__(self, *exc):
        return False


def http_error(code: int, body: bytes = b"nope") -> urllib.error.HTTPError:
    return urllib.error.HTTPError(
        "https://alertmanager.example/api/v2/alerts", code, "err", {}, BytesIO(body)
    )


class BuildAlertTests(unittest.TestCase):
    def test_carries_alertname_and_severity_as_labels(self):
        alert = na.build_alert("Foo", "critical", "something happened")
        self.assertEqual(alert["labels"]["alertname"], "Foo")
        self.assertEqual(alert["labels"]["severity"], "critical")
        self.assertEqual(alert["annotations"]["summary"], "something happened")

    def test_extra_labels_cannot_override_the_routing_labels(self):
        # A caller that could set severity through --label would silently
        # redirect its own alert past the receiver it was written to reach.
        alert = na.build_alert(
            "Foo", "critical", "s", labels={"severity": "info", "alertname": "Bar"}
        )
        self.assertEqual(alert["labels"]["severity"], "critical")
        self.assertEqual(alert["labels"]["alertname"], "Foo")

    def test_extra_labels_and_annotations_are_kept(self):
        alert = na.build_alert(
            "Foo", "warning", "s", labels={"repo": "deployments"},
            annotations={"runbook_url": "https://example/run"},
        )
        self.assertEqual(alert["labels"]["repo"], "deployments")
        self.assertEqual(alert["annotations"]["runbook_url"], "https://example/run")

    def test_description_is_omitted_when_absent(self):
        self.assertNotIn("description", na.build_alert("Foo", "info", "s")["annotations"])

    def test_starts_at_is_utc_and_zulu(self):
        alert = na.build_alert(
            "Foo", "info", "s", now=datetime(2026, 8, 19, 7, 11, tzinfo=timezone.utc)
        )
        self.assertEqual(alert["startsAt"], "2026-08-19T07:11:00Z")

    def test_no_end_time_is_set(self):
        # Expiry is Alertmanager's resolve_timeout on purpose: a caller that
        # stops posting stops alerting, with no stale alert left firing.
        self.assertNotIn("endsAt", na.build_alert("Foo", "info", "s"))


class ParsePairsTests(unittest.TestCase):
    def test_splits_on_the_first_equals_only(self):
        pairs = na.parse_pairs(["url=https://x/y?a=b"], "--annotation")
        self.assertEqual(pairs, {"url": "https://x/y?a=b"})

    def test_rejects_a_value_with_no_key(self):
        with self.assertRaises(na.NotifyError):
            na.parse_pairs(["=value"], "--label")

    def test_rejects_a_bare_token(self):
        with self.assertRaises(na.NotifyError):
            na.parse_pairs(["nokey"], "--label")


class PostTests(unittest.TestCase):
    def test_posts_a_json_array_to_the_v2_alerts_path(self):
        seen = {}

        def opener(request, timeout=None):
            seen["url"] = request.full_url
            seen["body"] = request.data
            seen["ctype"] = request.headers.get("Content-type")
            return FakeResponse(200)

        na.post({"labels": {}}, endpoint="https://am.example/", opener=opener)
        self.assertEqual(seen["url"], "https://am.example/api/v2/alerts")
        self.assertEqual(seen["ctype"], "application/json")
        self.assertTrue(seen["body"].startswith(b"["))

    def test_sends_a_bearer_header_only_when_a_token_is_given(self):
        seen = {}

        def opener(request, timeout=None):
            seen["auth"] = request.headers.get("Authorization")
            return FakeResponse(200)

        na.post({}, endpoint="https://am.example", opener=opener)
        self.assertIsNone(seen["auth"])
        na.post({}, endpoint="https://am.example", token="t0ken", opener=opener)
        self.assertEqual(seen["auth"], "Bearer t0ken")

    def test_sends_a_basic_auth_header_when_credentials_are_given(self):
        seen = {}

        def opener(request, timeout=None):
            seen["auth"] = request.headers.get("Authorization")
            return FakeResponse(200)

        na.post({}, endpoint="https://am.example", basic_auth=("admin", "hunter2"), opener=opener)
        self.assertEqual(seen["auth"], "Basic YWRtaW46aHVudGVyMg==")

    def test_basic_auth_takes_precedence_over_a_token(self):
        seen = {}

        def opener(request, timeout=None):
            seen["auth"] = request.headers.get("Authorization")
            return FakeResponse(200)

        na.post({}, endpoint="https://am.example", token="t0ken",
                 basic_auth=("admin", "hunter2"), opener=opener)
        self.assertEqual(seen["auth"], "Basic YWRtaW46aHVudGVyMg==")

    def test_a_4xx_is_not_retried(self):
        calls = []

        def opener(request, timeout=None):
            calls.append(1)
            raise http_error(400)

        with self.assertRaises(na.NotifyError):
            na.post({}, endpoint="https://am.example", opener=opener, sleep=lambda _: None)
        self.assertEqual(len(calls), 1)

    def test_a_5xx_is_retried_then_reported(self):
        calls = []

        def opener(request, timeout=None):
            calls.append(1)
            raise http_error(503)

        with self.assertRaises(na.NotifyError):
            na.post({}, endpoint="https://am.example", retries=3, opener=opener,
                    sleep=lambda _: None)
        self.assertEqual(len(calls), 3)

    def test_a_transport_error_is_retried_then_succeeds(self):
        calls = []

        def opener(request, timeout=None):
            calls.append(1)
            if len(calls) < 3:
                raise urllib.error.URLError("connection reset")
            return FakeResponse(200)

        na.post({}, endpoint="https://am.example", retries=3, opener=opener,
                sleep=lambda _: None)
        self.assertEqual(len(calls), 3)

    def test_a_non_2xx_status_without_an_exception_is_a_failure(self):
        # A response object carrying 302 is not delivery, and returning quietly
        # here is exactly the silent non-notification this tool exists to avoid.
        with self.assertRaises(na.NotifyError):
            na.post({}, endpoint="https://am.example", retries=1,
                    opener=lambda r, timeout=None: FakeResponse(302),
                    sleep=lambda _: None)


class MainTests(unittest.TestCase):
    ARGS = ["--alertname", "Foo", "--severity", "warning", "--summary", "s"]

    def test_dry_run_sends_nothing_and_succeeds(self):
        with unittest.mock.patch.object(na, "post") as posted:
            self.assertEqual(na.main(self.ARGS + ["--dry-run"]), 0)
        posted.assert_not_called()

    def test_delivery_failure_exits_non_zero(self):
        with unittest.mock.patch.object(na, "post", side_effect=na.NotifyError("down")):
            self.assertEqual(na.main(self.ARGS), 1)

    def test_successful_delivery_exits_zero(self):
        with unittest.mock.patch.object(na, "post"):
            self.assertEqual(na.main(self.ARGS), 0)

    def test_an_unknown_severity_is_rejected(self):
        with self.assertRaises(SystemExit):
            na.main(["--alertname", "F", "--severity", "page-me", "--summary", "s"])

    def test_a_malformed_label_exits_without_posting(self):
        with unittest.mock.patch.object(na, "post") as posted:
            self.assertEqual(na.main(self.ARGS + ["--label", "bare"]), 2)
        posted.assert_not_called()

    def test_the_token_is_read_from_the_environment(self):
        with unittest.mock.patch.object(na, "post") as posted, \
                unittest.mock.patch.dict(na.os.environ, {"ALERTMANAGER_TOKEN": "abc"}):
            na.main(self.ARGS)
        self.assertEqual(posted.call_args.kwargs["token"], "abc")

    def test_basic_auth_credentials_are_read_from_the_environment(self):
        env = {"ALERTMANAGER_USER": "admin", "ALERTMANAGER_PASSWORD": "hunter2"}
        with unittest.mock.patch.object(na, "post") as posted, \
                unittest.mock.patch.dict(na.os.environ, env):
            na.main(self.ARGS)
        self.assertEqual(posted.call_args.kwargs["basic_auth"], ("admin", "hunter2"))

    def test_basic_auth_is_not_set_when_only_one_credential_is_present(self):
        with unittest.mock.patch.object(na, "post") as posted, \
                unittest.mock.patch.dict(na.os.environ, {"ALERTMANAGER_USER": "admin"}):
            na.main(self.ARGS)
        self.assertIsNone(posted.call_args.kwargs["basic_auth"])


if __name__ == "__main__":
    unittest.main()
