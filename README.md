# CodexCont

[English](README.md) · [中文](README_zh.md)

Continue-thinking middleware for Codex / OpenAI Responses-compatible APIs.

This project is a small Starlette proxy that sits between a coding agent and an upstream Responses endpoint. It detects a known reasoning-truncation fingerprint (`usage.output_tokens_details.reasoning_tokens == 518 * n - 2`), silently asks the model to continue thinking, and folds multiple upstream streaming responses into one coherent downstream SSE response.

```text
Coding agent  ->  CodexCont  ->  Codex / Responses API
```

> **Installing via an AI agent?** Hand it [`INSTALL-GUIDE-AGENT/AGENT.md`](INSTALL-GUIDE-AGENT/AGENT.md) — a step-by-step runbook written for an AI agent to execute on your machine.

## Disclaimer

This project explicitly bypasses the observed OpenAI Codex reasoning-truncation behavior. If your use of this middleware is considered abusive, violates service terms, increases costs unexpectedly, or causes any other adverse consequences, you are solely responsible for those consequences.

## What it does

- Streams reasoning items to the agent live.
- Buffers tentative final output (`message` and `function_call`) until the upstream terminal event reveals whether the round was truncated.
- If the round is truncated, discards the tentative output and opens a continuation round with the prior reasoning replayed.
- If the round finishes cleanly or a safety cap is reached, flushes the final round output and emits one reconstructed terminal response.
- Leaves non-matching traffic as a transparent passthrough.

The default continuation method is a hidden `phase: "commentary"` assistant message (`"Continue thinking..."`). A legacy synthetic tool-pair mode is also available.

## Requirements

- Python `>= 3.12`
- [`uv`](https://docs.astral.sh/uv/) recommended

Runtime dependencies are declared in `pyproject.toml`:

- `httpx`
- `starlette`
- `uvicorn`

## Quick start

```bash
uv sync
cp config.example.toml config.toml
uv run python run.py
```

`run.py` reads the local `config.toml`; start by copying `config.example.toml` and then adjust it as needed.

The example default server listens on `127.0.0.1:8787` and accepts POST requests at:

- `/v1/responses`

You can also run with the already-created virtual environment directly:

```bash
# Windows / Git Bash in this workspace
.venv/Scripts/python.exe run.py
```

## Point your client at the proxy

Use the proxy URL instead of the real upstream URL.

Example:

```text
http://127.0.0.1:8787/v1/responses
```

The example default configuration (`config.example.toml`, copied to `config.toml`) uses:

```toml
[upstream]
url = "https://chatgpt.com/backend-api/codex/responses"
mode = "header"
```

With `mode = "header"`, a `Responses-API-Base` request header overrides the configured `url`; when the header is absent, requests fall back to the configured Codex URL.

For example, to target a generic Responses-compatible endpoint, send:

```text
Responses-API-Base: https://api.openai.com/v1
```

The middleware appends `/responses` unless the supplied value already ends with `/responses`. This control header is stripped before forwarding upstream.

## Authentication

`config.toml` supports three auth modes. The example default is `passthrough`:

```toml
[auth]
mode = "passthrough"               # passthrough | inject | passthrough_then_inject
access_token = ""                  # sent as Authorization: Bearer <access_token>
chatgpt_account_id = ""            # sent as chatgpt-account-id when non-empty
```

Modes:

- `passthrough`: forward the caller's auth headers only; inject nothing.
- `inject`: override/set auth headers from config.
- `passthrough_then_inject`: keep caller auth when present, otherwise inject from config.

Security guard: if a request supplies `Responses-API-Base`, the middleware will not leak configured credentials to that request-supplied URL. If the current auth mode would inject configured credentials for that request, it is rejected with `400`. To use per-request upstream overrides with credentials, pass the caller's own `Authorization` and use `mode = "passthrough"` or `mode = "passthrough_then_inject"`.

Do not commit secrets. `rt.json` and `free_rt.json` are ignored by `.gitignore`, and tokens in `config.toml` should be handled carefully.

## Dashboard

CodexCont serves a lightweight read-only dashboard at:

```text
http://127.0.0.1:8787/admin/
```

It exposes service status, upstream health, in-memory request metrics, recent request protection summaries, and live redacted logs through SSE. The request view distinguishes protected-clean, auto-continued, risky-uncontinued, passthrough, failed, and incomplete requests. Log history is memory-only and bounded by:

```toml
[admin]
max_log_events = 800
key_policy_state_path = "" # optional; set to the CPA Key Policy state JSON to show safe key names
```

When running in Docker, this must be a path visible inside the CodexCont
container. For example, mount the CPA plugin state read-only and set
`key_policy_state_path = "/data/plugin-state/cpa-key-policy-state.json"`.

Do not expose `/admin/` on a public API hostname unless it is protected by an external access layer such as Cloudflare Access.

## CPA Usage Portal

This repository also includes a small self-service sidecar in `cpa_usage_portal/`.
It is designed to sit next to CPA Manager Plus and CPA Key Policy: users log in
with their own `cpa_...` key and can only see usage filtered to that key. The
portal validates login with the raw key's Key Policy hash, but filters CPAMP
usage with `sha256(Key Policy id)` because the plugin authenticates to CPA as
the key id. Raw API keys, OAuth tokens, management keys, request bodies,
response bodies, and encrypted reasoning content are never returned.

Run locally:

```bash
.venv/Scripts/python.exe run_usage_portal.py
```

Docker deployment templates live in `deploy/cpa-usage-portal/`.

## When continuation is applied

The middleware folds only when all of the following are true:

- `[continue].enabled = true`
- request body is a JSON object
- `stream` is truthy
- reasoning is not explicitly disabled (`"reasoning": false` disables folding)
- when using `method = "tool_pair"`, the request does not declare a real tool with the same name as `[continue].continue_tool_name`

All other requests are proxied unchanged as passthrough streams.

## Continuation logic

For each upstream round:

1. Reasoning item events are forwarded live with rewritten `sequence_number` and `output_index`.
2. Message and function-call events are buffered as tentative output.
3. On the terminal event, the middleware reads `usage.output_tokens_details.reasoning_tokens`.
4. If the token count matches `518 * n - 2`, is within the configured tier window, has encrypted reasoning content, and safety caps allow it, the middleware:
   - drops the buffered tentative output,
   - appends the round's reasoning plus a continuation marker to the next request input,
   - opens another upstream streaming round.
5. Otherwise it flushes the final buffered output and emits a reconstructed terminal event.

The downstream agent sees one response, while metadata includes details about the hidden rounds.

## Response metadata

The final reconstructed response includes proxy metadata such as:

- `metadata.proxy_rounds`: per-round reasoning token counts and detected tier `n`.
- `metadata.proxy_billed_usage`: summed upstream token usage across hidden rounds.
- `metadata.proxy_stopped_reason`: present when a guard or error stops continuation.

Agent-facing `usage` is reconstructed to look like one response: round-1 input/cached tokens, summed reasoning tokens, and the final round's non-reasoning output.

## Tests

The test suite is self-contained and does not require `pytest`:

```bash
uv run python tests/test_middleware.py
# or
.venv/Scripts/python.exe tests/test_middleware.py
```

Current offline coverage includes:

- truncation math
- incremental SSE parsing
- fold/rewrite behavior with captured SSE fixtures
- commentary and tool-pair continuation payloads
- header transparency
- upstream URL resolution
- auth safety guard
- dashboard diagnostics and admin route smoke
- CPA usage portal hash/session/filtering/redaction/retention behavior
- EOF/upstream-error behavior

## Project layout

```text
middleware/
  admin.py     # read-only dashboard/admin routes
  app.py       # Starlette app and route handler
  codex.py     # truncation math and continuation payload builders
  config.py    # config.toml loader and dataclasses
  creds.py     # upstream header/auth construction
  dashboard.html # static dashboard page
  diagnostics.py # in-memory metrics, ring buffer, and SSE subscribers
  proxy.py     # fold_stream state machine
  sse.py       # incremental SSE parser/serializer
  store.py     # in-memory ID store for optional stateful repair

tests/
  test_middleware.py
  test_cpa_usage_portal.py
  fixtures/

run.py         # uvicorn entrypoint
run_usage_portal.py # CPA usage portal entrypoint
config.example.toml # example runtime configuration; copy to config.toml for local use
```

## Limitations

- Final answer text is buffered until the terminal round proves it is not truncated, so final-answer first-token latency can be higher than a normal stream.
- Non-streaming requests are currently passed through rather than folded.
- The truncation detector is intentionally specific to the observed `518 * n - 2` fingerprint.
- Optional `repair_followup = "stateful"` uses in-memory process-local state; it is not shared across multiple proxy instances.

## Acknowledgements

This project would not exist without the discussions in the [LINUX DO](https://linux.do) community. Special thanks to **@shinorochi** and **@dskdkj** of the LINUX DO community for jointly pinning down the truncation mechanism and GPT's thinking model, and to **@shinorochi** for proposing the better approach based on `commentary` input rather than faked tool calls.
