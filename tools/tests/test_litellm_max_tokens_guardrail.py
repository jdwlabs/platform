#!/usr/bin/env python3
"""Regression tests for the LiteLLM max_tokens ceiling guardrail.

The guardrail's actual source lives as a literal Python string inside
tenants/platform/services/litellm/postInstall/custom-callbacks-configmap.yaml
(LiteLLM requires it as a real file next to config.yaml, not an importable
module, so it ships via a projected ConfigMap key rather than a .py file
under this repo's normal Python tooling). This suite extracts that string
and executes it against lightweight stand-ins for the `litellm`/`fastapi`
symbols it imports, so the guardrail's actual token-budget arithmetic is
exercised without needing those packages installed.

Every test here corresponds to a way the guardrail could let a request
through whose real prompt-plus-output can exceed the backend's hard context
window. Run with:

    python3 -m unittest discover -s tools/tests -t tools/tests
"""
import sys
import types
import unittest
from pathlib import Path

import yaml

REPO_ROOT = Path(__file__).resolve().parent.parent.parent
CONFIGMAP_PATH = (
    REPO_ROOT
    / "tenants/platform/services/litellm/postInstall/custom-callbacks-configmap.yaml"
)


def _load_guardrail_source() -> str:
    with open(CONFIGMAP_PATH, encoding="utf-8") as f:
        manifest = yaml.safe_load(f)
    return manifest["data"]["custom_callbacks.py"]


def _install_fake_modules():
    """Registers minimal stand-ins for litellm/fastapi in sys.modules.

    Returns (fake_litellm, HTTPException) so tests can monkeypatch
    token_counter per-case and catch the guardrail's rejection type.
    """
    fake_litellm = types.ModuleType("litellm")
    fake_litellm.token_counter = lambda **kwargs: 0

    integrations_pkg = types.ModuleType("litellm.integrations")
    custom_logger_mod = types.ModuleType("litellm.integrations.custom_logger")

    class CustomLogger:
        """Stand-in for litellm's real base class -- no behavior needed here."""

    custom_logger_mod.CustomLogger = CustomLogger

    proxy_pkg = types.ModuleType("litellm.proxy")
    proxy_server_mod = types.ModuleType("litellm.proxy.proxy_server")
    proxy_server_mod.UserAPIKeyAuth = object
    proxy_server_mod.DualCache = object

    fake_fastapi = types.ModuleType("fastapi")

    class HTTPException(Exception):
        def __init__(self, status_code, detail):
            super().__init__(detail)
            self.status_code = status_code
            self.detail = detail

    fake_fastapi.HTTPException = HTTPException

    sys.modules["litellm"] = fake_litellm
    sys.modules["litellm.integrations"] = integrations_pkg
    sys.modules["litellm.integrations.custom_logger"] = custom_logger_mod
    sys.modules["litellm.proxy"] = proxy_pkg
    sys.modules["litellm.proxy.proxy_server"] = proxy_server_mod
    sys.modules["fastapi"] = fake_fastapi

    return fake_litellm, HTTPException


class GuardrailHarness(unittest.IsolatedAsyncioTestCase):
    """Execs the real guardrail source against the fakes above."""

    @classmethod
    def setUpClass(cls):
        cls._removed_modules = {
            name: sys.modules.get(name)
            for name in (
                "litellm",
                "litellm.integrations",
                "litellm.integrations.custom_logger",
                "litellm.proxy",
                "litellm.proxy.proxy_server",
                "fastapi",
            )
        }
        cls.fake_litellm, cls.HTTPException = _install_fake_modules()

        source = _load_guardrail_source()
        namespace = {"__name__": "custom_callbacks_under_test"}
        exec(compile(source, str(CONFIGMAP_PATH), "exec"), namespace)
        cls.module = namespace

    @classmethod
    def tearDownClass(cls):
        for name, mod in cls._removed_modules.items():
            if mod is None:
                sys.modules.pop(name, None)
            else:
                sys.modules[name] = mod

    def setUp(self):
        # Each test controls the estimated prompt size directly; the real
        # guardrail's own undercount-vs-backend behavior is exactly what the
        # SAFETY_MARGIN_TOKENS constant (asserted on below) exists to cover,
        # not something this harness needs to reproduce.
        self.fake_litellm.token_counter = lambda **kwargs: self._prompt_tokens
        self._prompt_tokens = 0

    def set_prompt_tokens(self, n: int):
        self._prompt_tokens = n


class TestNoRequestedMaxTokens(GuardrailHarness):
    async def test_passthrough_when_max_tokens_absent(self):
        data = {"model": "sre-investigator-local", "messages": []}
        result = await self.module["guardrail_instance"].async_pre_call_hook(
            user_api_key_dict=None, cache=None, data=data, call_type="completion"
        )
        self.assertEqual(result, data)
        self.assertNotIn("max_tokens", result)


class TestNormalClamping(GuardrailHarness):
    async def _run(self, model, prompt_tokens, requested):
        self.set_prompt_tokens(prompt_tokens)
        data = {"model": model, "messages": [], "max_tokens": requested}
        return await self.module["guardrail_instance"].async_pre_call_hook(
            user_api_key_dict=None, cache=None, data=data, call_type="completion"
        )

    async def test_clamps_to_available_room_when_below_absolute_cap(self):
        m = self.module
        context_window = m["CONTEXT_WINDOW_BY_MODEL"]["sre-investigator-local"]
        prompt_tokens = 20_000
        expected_available = (
            context_window - prompt_tokens - m["SAFETY_MARGIN_TOKENS"]
        )
        self.assertLess(
            expected_available,
            m["MAX_OUTPUT_TOKENS_ABSOLUTE"],
            "test fixture must exercise the available-room branch, not the absolute cap",
        )
        result = await self._run(
            "sre-investigator-local", prompt_tokens, requested=64_000
        )
        self.assertEqual(result["max_tokens"], expected_available)

    async def test_clamps_to_absolute_cap_when_plenty_of_room(self):
        m = self.module
        result = await self._run(
            "sre-investigator", prompt_tokens=100, requested=64_000
        )
        self.assertEqual(result["max_tokens"], m["MAX_OUTPUT_TOKENS_ABSOLUTE"])

    async def test_leaves_a_requested_value_under_the_ceiling_untouched(self):
        result = await self._run(
            "sre-investigator-local", prompt_tokens=100, requested=256
        )
        self.assertEqual(result["max_tokens"], 256)

    async def test_unknown_model_uses_default_context_window(self):
        m = self.module
        result = await self._run(
            "some-new-model-not-in-the-table", prompt_tokens=100, requested=64_000
        )
        expected = (
            m["DEFAULT_CONTEXT_WINDOW"] - 100 - m["SAFETY_MARGIN_TOKENS"]
        )
        self.assertEqual(result["max_tokens"], min(expected, m["MAX_OUTPUT_TOKENS_ABSOLUTE"]))


class TestTokenCounterFailureFallback(GuardrailHarness):
    async def test_falls_back_to_absolute_cap_without_a_context_check(self):
        def boom(**kwargs):
            raise RuntimeError("tokenizer blew up")

        self.fake_litellm.token_counter = boom
        m = self.module
        data = {
            "model": "sre-investigator-local",
            "messages": [],
            "max_tokens": 64_000,
        }
        result = await self.module["guardrail_instance"].async_pre_call_hook(
            user_api_key_dict=None, cache=None, data=data, call_type="completion"
        )
        self.assertEqual(result["max_tokens"], m["MAX_OUTPUT_TOKENS_ABSOLUTE"])


class TestRefusesRatherThanOverflowing(GuardrailHarness):
    """The actual regression: a large-enough prompt used to still get a
    forced MIN_OUTPUT_TOKENS response, and prompt_tokens + MIN_OUTPUT_TOKENS
    could exceed context_window. The fix must refuse instead."""

    async def _run(self, prompt_tokens, requested=512):
        self.set_prompt_tokens(prompt_tokens)
        data = {
            "model": "sre-investigator-local",
            "messages": [],
            "max_tokens": requested,
        }
        return await self.module["guardrail_instance"].async_pre_call_hook(
            user_api_key_dict=None, cache=None, data=data, call_type="completion"
        )

    async def test_refuses_when_prompt_leaves_no_room_for_the_floor(self):
        m = self.module
        context_window = m["CONTEXT_WINDOW_BY_MODEL"]["sre-investigator-local"]
        # One token past the point where available == MIN_OUTPUT_TOKENS - 1.
        prompt_tokens = (
            context_window - m["SAFETY_MARGIN_TOKENS"] - m["MIN_OUTPUT_TOKENS"] + 1
        )
        with self.assertRaises(self.HTTPException) as ctx:
            await self._run(prompt_tokens)
        self.assertEqual(ctx.exception.status_code, 400)

    async def test_reproduces_the_incident_shape_without_silently_overflowing(self):
        # The observed failure: a 512-output request against a prompt whose
        # real count left the total one token over a 32768 window. Whatever
        # this guardrail's own estimate is for a prompt that large, it must
        # never hand back max_tokens=512 for it -- it must refuse.
        with self.assertRaises(self.HTTPException):
            await self._run(prompt_tokens=32_257, requested=512)

    async def test_allows_exactly_at_the_floor_boundary(self):
        m = self.module
        context_window = m["CONTEXT_WINDOW_BY_MODEL"]["sre-investigator-local"]
        # available == MIN_OUTPUT_TOKENS exactly -- must be allowed, not refused.
        prompt_tokens = (
            context_window - m["SAFETY_MARGIN_TOKENS"] - m["MIN_OUTPUT_TOKENS"]
        )
        result = await self._run(prompt_tokens, requested=64_000)
        self.assertEqual(result["max_tokens"], m["MIN_OUTPUT_TOKENS"])


class TestNeverAuthorizesOverflow(GuardrailHarness):
    """Property check: whenever the hook returns normally (doesn't raise),
    prompt_tokens + the max_tokens it authorizes must never exceed the
    model's context window -- the invariant the whole guardrail exists to
    hold, across a spread of prompt sizes rather than one fixed sample."""

    async def test_invariant_holds_across_a_range_of_prompt_sizes(self):
        m = self.module
        context_window = m["CONTEXT_WINDOW_BY_MODEL"]["sre-investigator-local"]
        for prompt_tokens in range(0, context_window + 2000, 137):
            self.set_prompt_tokens(prompt_tokens)
            data = {
                "model": "sre-investigator-local",
                "messages": [],
                "max_tokens": 64_000,
            }
            try:
                result = await self.module["guardrail_instance"].async_pre_call_hook(
                    user_api_key_dict=None,
                    cache=None,
                    data=data,
                    call_type="completion",
                )
            except self.HTTPException:
                continue
            authorized = result["max_tokens"]
            self.assertLessEqual(
                prompt_tokens + authorized,
                context_window,
                msg=(
                    f"prompt_tokens={prompt_tokens} authorized={authorized} "
                    f"together exceed context_window={context_window}"
                ),
            )


class TestConfigMapSanity(unittest.TestCase):
    """Guards against a future edit breaking the YAML/Python embedding
    itself, independent of the guardrail's logic."""

    def test_yaml_parses_and_embedded_python_compiles(self):
        source = _load_guardrail_source()
        compile(source, str(CONFIGMAP_PATH), "exec")


if __name__ == "__main__":
    unittest.main()
