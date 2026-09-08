# LiteLLM `max_tokens` does not clamp Holmes' 64k request to the local vLLM backend's 32768-token limit

Research note. Not an implementation plan. No code changed as part of this
investigation.

## Note on where this file lives

`docs/adr/` is for decisions and `docs/superpowers/plans/` is gitignored
scratch for implementation plans, so neither fits a primary-source research
artifact. This file opens `docs/investigations/` for that kind of note: a
finding worth keeping because it was expensive to establish, with no decision
attached yet.

## TL;DR

- **`litellm_params.max_tokens` in `model_list` is a default, not a clamp.**
  Confirmed directly in LiteLLM's current `main`-branch source
  (`litellm/router.py`, both the sync and async completion paths): the
  caller's kwargs are spread into the outgoing request **after** (and so on
  top of) `litellm_params`, so any `max_tokens` the caller sends always wins.
- **Holmes sends 64000 because it cannot discover the real limit — not
  because 64000 is hardcoded.** Holmes computes
  `max(64000, 12% of context window)`, then tries to shrink that against
  `litellm.model_cost[<model name>]["max_output_tokens"]`. Because
  `sre-investigator-local` / `hosted_vllm/qwen/qwen3-coder-30b-a3b` is a
  self-hosted model with no entry in LiteLLM's static pricing/context table,
  every lookup variant fails, the code falls through, and Holmes uses the
  64000 floor unmodified.
- **The deployed LiteLLM version is not pinned to a release at all.** The
  gateway image tag renders to `main-latest` (tracking LiteLLM's `main`
  branch), and the repo's own `tools/image-pin-allowlist.yaml` documents this
  as a deliberate, still-open exception — so "the exact version" is
  indeterminate from config; it is whatever `main-latest` build the node last
  pulled.
- **vLLM's OpenAI-compatible server rejects an over-limit `max_tokens`
  outright; it does not clamp it.** This is documented as current,
  intentional (if contested) vLLM behavior in an open upstream issue — it is
  why only the local backend errors while the cloud backends silently accept
  64000.
- **Recommended fix:** a LiteLLM-proxy-side pre-call guardrail
  (`async_pre_call_hook` / custom guardrail) that clamps `max_tokens` down to
  each model group's real ceiling before the request reaches the backend.
  This is the shared enforcement point for every caller, not just Holmes.
  Cheaper stopgap: set `OVERRIDE_MAX_OUTPUT_TOKEN` on the Holmes deployment,
  but that is a single global value across every model Holmes talks to
  (cloud and local), so it is not a substitute for a per-model server-side
  cap.

---

## 1. Does `litellm_params.max_tokens` on a `model_list` entry override a
caller-supplied `max_tokens`?

No. It is a default applied only when the caller omits the field.

**Source: `litellm/router.py` on `BerriAI/litellm` `main`
(fetched 2026-08-13, raw.githubusercontent.com/BerriAI/litellm/main/litellm/router.py)**

Both the sync and async request paths build the outgoing kwargs the same way
— a copy of the deployment's `litellm_params`, then the caller's `kwargs`
spread on top, so any key present in both is decided by the caller's value:

Sync path, `Router._completion` (`router.py:1871-1877`):
```python
input_kwargs: Final = {
    **litellm_params,
    "messages": messages,
    "caching": self.cache_responses,
    "client": model_client,
    **kwargs,
}
response: Final = litellm.completion(**input_kwargs)
```

Async path, `Router._acompletion` (`router.py:2891-2898`), the path the
LiteLLM proxy actually uses for every request:
```python
input_kwargs: Final = {
    **litellm_params,
    "messages": messages,
    "caching": self.cache_responses,
    "client": model_client,
    **kwargs,
}
_response: Final = litellm.acompletion(**input_kwargs)
```

`litellm_params` here is `deployment["litellm_params"].copy()` — the exact
dict from the `model_list` entry, which is where
`tenants/platform/services/litellm/values.yaml`'s `max_tokens: 16384` lives.
`kwargs` is the caller's own request body (Holmes' `max_tokens=64000`).
Python dict-unpacking gives later keys precedence, so `**kwargs` overwrites
`**litellm_params`'s `max_tokens` whenever the caller sets one. This is not
an edge case or a bug in one code path — it is the same merge order in both
the sync and async completion functions, and it is exactly what would
produce the observed 64000 reaching the vLLM backend despite the config's
16384.

The official docs (`docs.litellm.ai/docs/proxy/configs`,
`docs.litellm.ai/docs/completion/input`) do not state the override
relationship explicitly either way — they only show `max_tokens` as a valid
key inside `litellm_params` without describing precedence versus a
request-supplied value. The source is the authoritative answer here.

## 2. Is there ANY LiteLLM mechanism that hard-caps a client's requested
`max_tokens`, regardless of what the client sent?

**Not automatically, out of the box. There is a real, documented mechanism,
but it requires writing custom server-side logic — it is not a config key
you flip.**

### `model_info.max_tokens` / `model_info.max_output_tokens` — informational, not enforced

This is a distinct field from `litellm_params.max_tokens`, confirmed via
`docs.litellm.ai/docs/proxy/config_settings` and via the open GitHub issue
below. It is metadata LiteLLM (and its callers, including Holmes — see §5)
can *read* to answer "what's this model's real limit," used for cost
tracking, `/v1/model/info` responses, and client-side heuristics like
Holmes' own. It is **not** consulted by `Router._completion` /
`Router._acompletion` to reject or shrink an outgoing request — the merge
shown in §1 happens unconditionally regardless of what `model_info` says.

Confirming that this field is not even populated automatically for
self-hosted backends like `hosted_vllm`:
[BerriAI/litellm#27830](https://github.com/BerriAI/litellm/issues/27830)
(open, labels `llm translation`, `proxy`, `stale`), which states:

> hosted vLLM/OpenAI-like models frequently show: `max_input_tokens: null`
> and `max_output_tokens: null` unless admins manually add `model_info` in
> config

So even manually setting `model_info.max_output_tokens: 32768` on the
`sre-investigator-local` entry (which the repo does not currently do — the
entry only sets `litellm_params.max_tokens: 16384`) would make LiteLLM
*report* the correct ceiling to a well-behaved client, but would still not
make LiteLLM itself refuse or shrink an over-limit request server-side.

Directly on point, a prior feature request asking for exactly the enforcement
this ticket wants —
[BerriAI/litellm#1663 "Have max tokens check model limit and adjust"](https://github.com/BerriAI/litellm/issues/1663)
(opened 2024-01-29) — was **closed as not planned**. Upstream has
explicitly declined to build automatic max_tokens-vs-model-limit
reconciliation.

### The real mechanism: proxy call hooks / custom guardrails

LiteLLM's proxy supports an `async_pre_call_hook` that runs "just before a
litellm completion call is made" and can mutate the outgoing request body
(`docs.litellm.ai/docs/proxy/call_hooks`, "Modify / Reject Incoming
Requests"). A custom `CustomGuardrail`/`CustomLogger` subclass registered via
`litellm_settings.callbacks` in `config.yaml` receives the request `data`
dict — which includes `max_tokens` — before it is dispatched, and can clamp
or reject it. This is a real, current, code-level enforcement point; nothing
comparable exists as a declarative config key.

No such hook exists today in
`tenants/platform/services/litellm/values.yaml`'s `proxy_config` — the
`general_settings` / `router_settings` / `litellm_settings` blocks there
(values.yaml:96-109) contain no `callbacks` entry. Confirmed by grepping
LiteLLM's own default pre-call path
(`litellm/proxy/litellm_pre_call_utils.py` on `main`, fetched 2026-08-13) —
it contains no `max_tokens` handling at all, so there is no default,
built-in guardrail doing this either.

### Confirmed at the provider-adapter layer too

`litellm/llms/hosted_vllm/chat/transformation.py` (the adapter LiteLLM uses
for exactly this deployment's `hosted_vllm/qwen/qwen3-coder-30b-a3b` model)
was read in full: it transforms tool schemas, message content, and thinking
blocks, but does nothing with `max_tokens` — no clamping logic exists in the
hosted_vllm-specific code path either. The request's `max_tokens` reaches
the backend unmodified.

## 3. Is this a known upstream LiteLLM issue?

Not as a single issue titled around this exact symptom, but as a cluster of
open/closed issues describing the same structural gap:

- [#1663](https://github.com/BerriAI/litellm/issues/1663) — "Have max tokens
  check model limit and adjust." **Closed, not planned** (2024). Directly
  asked for automatic max_tokens capping against model limits; upstream
  declined.
- [#27830](https://github.com/BerriAI/litellm/issues/27830) — "Auto-populate
  max_input_tokens/max_output_tokens for hosted vLLM/OpenAI-like models."
  **Open.** Confirms `model_info` stays `null` for self-hosted backends
  unless manually configured — the root reason Holmes' own client-side
  fallback in §5 can't self-correct either.
- [#8985](https://github.com/BerriAI/litellm/issues/8985) — "Inconsistent use
  of max tokens." **Closed, not planned.** Different angle (ambiguity in
  `model_prices_and_context_window.json` between input/output/combined
  limits for some cloud models), not directly this bug, but same theme of
  `max_tokens` semantics being under-specified.

No maintainer-recommended workaround exists in any of these threads beyond
"configure `model_info` yourself" (which, per §2, still doesn't enforce
anything server-side) — none of them mention a guardrail/pre-call-hook
workaround explicitly, that is inferred from LiteLLM's general hook docs
(§2), not from these issues.

**This is also a known, currently-unresolved vLLM-side behavior, not just a
LiteLLM gap.** [vllm-project/vllm#42474](https://github.com/vllm-project/vllm/issues/42474)
— "vLLM rejects requests when max_tokens exceeds available context instead
of clamping." **Open.** Its description matches this ticket's error almost
exactly (a `VLLMValidationError` when `max_tokens` + prompt tokens exceed the
model's context, citing `vllm/renderers/params.py:418`), and explicitly notes
other OpenAI-API-compatible client tools (Zed, Factory.ai, Opencode) hitting
the same class of failure because they assume `max_tokens` is a soft ceiling
vLLM will clamp to, when in fact vLLM currently treats it as a hard,
rejected-if-exceeded value. This is why only the local vLLM backend in this
config throws — the three cloud backends (OpenRouter, NVIDIA NIM) evidently
tolerate or silently reduce an over-limit `max_tokens` rather than hard-reject
it.

## 4. What LiteLLM version is actually deployed in this cluster?

**Indeterminate from config — the deployment deliberately does not pin a
version.** This is confirmed by the repo's own documentation of the
exception, not an inference:

- `tenants/platform/tenant.yaml:368-375` — the `litellm` service entry
  points `chartPath: helm-charts/litellm-helm`, `revision: main`. That
  `revision` is a **git ref for this repo's own vendored Helm chart
  directory**, not a LiteLLM release version.
- `helm-charts/litellm-helm/Chart.yaml` — vendored chart `version: 0.1.2`
  (from upstream `oci://docker.litellm.ai/berriai/litellm-helm`, per
  `helm-charts/litellm-helm/README.md`), `appVersion: latest`.
- `helm-charts/litellm-helm/values.yaml:9-15` — `image.repository:
  ghcr.io/berriai/litellm-database`, `image.tag: ""`.
- `helm-charts/litellm-helm/templates/deployment.yaml:113` —
  `image: "{{ .Values.image.repository }}:{{ .Values.image.tag | default
  (printf "main-%s" .Chart.AppVersion) }}"`. With an empty tag and
  `appVersion: latest`, this renders to `ghcr.io/berriai/litellm-database:main-latest`.
- `tools/image-pin-allowlist.yaml:42-51` explicitly documents this as a
  known, deliberate gap:

  > deliberate: the empty tag makes the deployment template render
  > `main-<appVersion>`, and this chart's appVersion is literally `latest`,
  > so the gateway runs the upstream `main-latest` build. Combined with
  > pullPolicy IfNotPresent, the nodes hold whatever `main-latest` was when
  > they first pulled it — pinning to the digest `main-latest` resolves to
  > today would therefore not freeze the running build, it would UPGRADE
  > it. Choosing a specific LiteLLM build is a version decision for the
  > AI-SRE gateway and is deliberately not made by a CI coverage change.

So the true answer is: **the gateway runs whatever build of LiteLLM's `main`
branch `ghcr.io/berriai/litellm-database:main-latest` resolved to at the
moment each node last pulled the image** (`pullPolicy: IfNotPresent`, so it
is frozen at pull time, not continuously tracking `main`). There is no
Kubernetes-manifest or Helm-values source of truth in this repo for exactly
which LiteLLM commit that is; determining it precisely would require
inspecting the running pod/image digest, which this research task
deliberately did not do (`AGENTS.md`'s binary contract restricts cluster
inspection to `platformctl`, which has no image/version query today, and raw
`kubectl` is out of scope for this investigation).

**Consequence for §1/§2's findings:** because the deployment tracks `main`
directly rather than a tagged release, LiteLLM's current `main`-branch source
(fetched 2026-08-13, used throughout §1–§2) *is* the most accurate available
description of deployed behavior — there is no older pinned version whose
CHANGELOG needs reconciling against it. The caveat is the inverse of the
usual one: it's not "is this stale for an old pinned version," it's "the
exact build pulled could differ from today's `main` HEAD by however long it's
been since the node last pulled" (unknown from config).

## 5. Holmes' own `max_tokens` behavior — hardcoded or configurable?

**Neither purely hardcoded nor a simple flag — it's a computed value with a
64000 floor, overridable by one environment variable, and it does try (and,
here, fails) to discover the real per-model limit on its own.**

Source: `holmes/core/llm.py` on `HolmesGPT/holmesgpt` `master` (the current
upstream org/repo — `robusta-dev/holmesgpt` now points here; fetched
2026-08-13 via `raw.githubusercontent.com/HolmesGPT/holmesgpt/master/holmes/core/llm.py`),
`DefaultLLM.get_maximum_output_token()`:

```python
def get_maximum_output_token(self) -> int:
    # Reserve output budget = max(64k, 12% of the context window). The 64k
    # floor keeps small and unknown models usable (the 200k fallback window
    # gives 12% = 24k, so they stay at 64k), while large windows scale up:
    # a 1M-context model reserves 120k. The crossover is ~533k. This value
    # is still capped below to the model's real max_output_tokens when the
    # model is known to litellm.
    max_output_tokens = max(64000, self.get_context_window_size() * 12 // 100)

    if OVERRIDE_MAX_OUTPUT_TOKEN:
        return OVERRIDE_MAX_OUTPUT_TOKEN

    for name in self._get_model_name_variants_for_lookup():
        try:
            litellm_max_output_tokens = litellm.model_cost[name]["max_output_tokens"]
            if litellm_max_output_tokens < max_output_tokens:
                max_output_tokens = litellm_max_output_tokens
            return max_output_tokens
        except Exception:
            continue

    # ...logs a warning once per model and returns the 64000-floor value
    return max_output_tokens
```

`OVERRIDE_MAX_OUTPUT_TOKEN` (`holmes/core/llm.py:54`,
`OVERRIDE_MAX_OUTPUT_TOKEN = environ_get_safe_int("OVERRIDE_MAX_OUTPUT_TOKEN")`)
is a plain environment variable Holmes reads at import time — this is the
configurable knob, and it is documented on Holmes' own docs site
(`docs/reference/context-management.md` on the same repo): a non-null
`max_tokens`/`max_completion_tokens` on the model's own args takes first
precedence, then `OVERRIDE_MAX_OUTPUT_TOKEN`, then the computed
`max(64000, 12%-of-context)` default further capped by litellm's model_cost
table when the model is recognized.

**Why the fallback lookup fails for this deployment specifically:** Holmes'
own `modelList` entry for the local tier
(`tenants/platform/services/holmes/values.yaml:42-45`) is:
```yaml
sre-investigator-local:
  model: openai/sre-investigator-local
  api_base: http://platform-litellm.ai-sre.svc.cluster.local:4000/v1
```
`self.model` / the names tried in `_get_model_name_variants_for_lookup()`
are derived from `openai/sre-investigator-local` (an alias meaningful only to
the LiteLLM proxy, not a real model LiteLLM's SDK ships pricing/context data
for) — this name (and its variants) will never be a key in
`litellm.model_cost`, the same static table §2's issue #27830 says stays
empty for self-hosted/`hosted_vllm` models generally. Every lookup attempt
throws, `except Exception: continue` runs out, and Holmes falls through to
the `max(64000, ...)` floor unmodified — hence exactly 64000, matching the
observed error's `max_tokens=64000`.

So: **64000 is not a hardcoded constant Holmes always sends; it's a computed
default that only survives unmodified because Holmes cannot resolve
`sre-investigator-local`'s real limit through any of its own lookup paths.**
It is configurable three ways, cited above: per-model `max_tokens` in
Holmes' own `modelList` args, the global `OVERRIDE_MAX_OUTPUT_TOKEN` env var,
or (if it worked, which per §2/§5 it currently can't for this model)
LiteLLM's `model_info.max_output_tokens` being populated and visible to
Holmes.

## 6. Recommendation

### Preferred: LiteLLM-proxy-side hard cap via a pre-call guardrail

Add a custom guardrail / `async_pre_call_hook` (per
`docs.litellm.ai/docs/proxy/call_hooks`, §2 above) to the LiteLLM proxy
config that, for the `sre-investigator-local` model group (and ideally every
group, keyed off a per-model ceiling), clamps `data["max_tokens"]` down to a
value safely under the backend's real limit before the request is
dispatched — e.g. cap to 16000 for `sre-investigator-local` (vLLM
`max_model_len=32768`, leaving headroom for prompt tokens, matching the
16384 figure the config comment already intends). This is the shared
enforcement point: it protects every current and future caller of this
LiteLLM instance (Holmes today, anything else added later), not just Holmes,
and it doesn't depend on the caller's SDK correctly resolving model limits —
which, per §5, Holmes' own client-side heuristic demonstrably cannot do for
proxy-aliased self-hosted models.

Trade-offs: requires shipping custom Python (a `CustomGuardrail` class) and
wiring it into `litellm_settings.callbacks` in
`tenants/platform/services/litellm/values.yaml`'s `proxy_config` — more
surface area than a values.yaml key, and it's logic this repo now owns and
maintains rather than upstream config. Given §2/§3 confirm no declarative
LiteLLM config key does this job, and #1663 shows upstream has declined to
build it in, a custom hook is the only proxy-side option available today.

### Complementary / interim: Holmes-side `OVERRIDE_MAX_OUTPUT_TOKEN`

Set `OVERRIDE_MAX_OUTPUT_TOKEN` (an int, e.g. `16000`) as an
`additionalEnvVars` entry in `tenants/platform/services/holmes/values.yaml`
(alongside the existing `OPENAI_API_KEY` / `OPENAI_API_BASE` /
`TOOL_MEMORY_LIMIT_MB` entries at lines 17-27). This is a one-line config
change with a confirmed, cited code path (§5) and would immediately stop the
64000 request at the source.

Trade-offs: it is a single global value across **every** model Holmes talks
to — `sre-investigator` (OpenRouter gpt-oss-120b), `sre-investigator-fb1`/`fb2`
(NVIDIA-hosted), and `sre-investigator-local` all currently share the same
"16384 is ample for RCA prose" intent per the config comments in
`tenants/platform/services/litellm/values.yaml:63-64`, so one shared override
happens to fit today — but it can't express a different ceiling per model
group the way a proxy-side per-model-group guardrail can, and it does
nothing for any other OpenAI-compatible client that might call this LiteLLM
instance in the future without going through Holmes.

### Both together, or proxy-side alone, but not Holmes-side alone

Because Holmes is upstream OSS this cluster doesn't control the release
cadence of, and because the proxy is the one chokepoint every caller shares,
the proxy-side guardrail is the fix that actually "guarantees no request to
`sre-investigator-local` (or any model group) ever exceeds that model's real
context limit, regardless of what Holmes requests" per the ticket's own
framing. The `OVERRIDE_MAX_OUTPUT_TOKEN` env var is reasonable as a fast,
low-risk mitigation to land first (or as defense in depth alongside the
guardrail), but should not be treated as the complete fix on its own — it
only closes the gap for Holmes specifically, and only as long as every model
Holmes talks to happens to share one safe ceiling.

### Not a fix by itself: raising `litellm_params.max_tokens`

Changing the `max_tokens: 16384` value already in
`tenants/platform/services/litellm/values.yaml` (e.g. to something under
32768) would not help — §1 shows this field is only a default, and Holmes
always sends an explicit `max_tokens` (per `holmes/core/llm.py:704-709`,
Holmes only omits it if the model's own args explicitly set it to `null`),
so any value here is unconditionally overwritten by the caller's own request
today, regardless of what it's set to.

---

## Sources consulted

- This repo: `tenants/platform/services/litellm/values.yaml`,
  `tenants/platform/services/holmes/values.yaml`,
  `tenants/platform/tenant.yaml`, `helm-charts/litellm-helm/Chart.yaml`,
  `helm-charts/litellm-helm/values.yaml`,
  `helm-charts/litellm-helm/templates/deployment.yaml`,
  `helm-charts/litellm-helm/README.md`, `tools/image-pin-allowlist.yaml`,
  `AGENTS.md`
- LiteLLM source (`BerriAI/litellm`, `main`, fetched 2026-08-13):
  `litellm/router.py`, `litellm/utils.py`,
  `litellm/proxy/litellm_pre_call_utils.py`,
  `litellm/llms/hosted_vllm/chat/transformation.py`
- LiteLLM docs: [docs.litellm.ai/docs/proxy/configs](https://docs.litellm.ai/docs/proxy/configs),
  [docs.litellm.ai/docs/completion/input](https://docs.litellm.ai/docs/completion/input),
  [docs.litellm.ai/docs/proxy/config_settings](https://docs.litellm.ai/docs/proxy/config_settings),
  [docs.litellm.ai/docs/proxy/call_hooks](https://docs.litellm.ai/docs/proxy/call_hooks)
- LiteLLM GitHub issues:
  [#1663](https://github.com/BerriAI/litellm/issues/1663),
  [#27830](https://github.com/BerriAI/litellm/issues/27830),
  [#8985](https://github.com/BerriAI/litellm/issues/8985)
- HolmesGPT source (`HolmesGPT/holmesgpt`, `master`, fetched 2026-08-13):
  `holmes/core/llm.py`, `docs/reference/context-management.md`
- vLLM GitHub issue: [#42474](https://github.com/vllm-project/vllm/issues/42474)
