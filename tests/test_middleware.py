#!/usr/bin/env python3
"""Offline tests for the continue_thinking middleware.

Run: .venv/Scripts/python.exe tests/test_middleware.py
No pytest dependency — a tiny runner prints PASS/FAIL per check.
"""
from __future__ import annotations

import asyncio
import json
import sys
from dataclasses import replace
from pathlib import Path

import zstandard as zstd

ROOT = Path(__file__).resolve().parent.parent
FIXTURES = Path(__file__).resolve().parent / "fixtures"
sys.path.insert(0, str(ROOT))

from starlette.datastructures import Headers

from middleware.app import (
    _decode_request_body,
    _drop_header,
    _make_client,
    _repair_legacy_tool_search_arguments,
    _resolve_upstream_url,
    _url_is_from_header,
)
from middleware.app import _canonicalize_function_call_outputs
from middleware.codex import (
    continue_call_id,
    is_truncation_pattern,
    reasoning_enabled,
    repair_followup_input,
    should_continue,
    tier_n,
)
from middleware.config import load_config
from middleware.creds import build_upstream_headers, would_inject_authorization
from middleware.proxy import fold_stream
from middleware.sse import DONE, incremental_sse
from middleware.store import IdStore


# --- helpers ----------------------------------------------------------------


def make_sse(events: list[dict]) -> bytes:
    out = b""
    for ev in events:
        out += f"event: {ev['type']}\r\n".encode()
        out += b"data: " + json.dumps(ev).encode() + b"\r\n\r\n"
    return out


async def _aiter_once(data: bytes):
    yield data


async def parse_events(data: bytes) -> list:
    evs = []
    async for e in incremental_sse(_aiter_once(data)):
        evs.append(e)
    return evs


class FakeResp:
    def __init__(
        self, data: bytes, status: int = 200, chunk: int = 4096, delay: float = 0
    ):
        self._data = data
        self.status_code = status
        self.headers: dict[str, str] = {}
        self._chunk = chunk
        self._delay = delay

    async def aiter_bytes(self):
        for i in range(0, len(self._data), self._chunk):
            if self._delay:
                await asyncio.sleep(self._delay)
            yield self._data[i : i + self._chunk]

    async def aread(self) -> bytes:
        return self._data

    async def aclose(self) -> None:
        pass


class FakeClient:
    """Returns the queued responses on successive send() calls; records the JSON
    body of each build_request (the per-continuation-round upstream payload)."""

    def __init__(self, responses: list[FakeResp]):
        self._responses = list(responses)
        self._i = 0
        self.payloads: list[dict] = []

    def build_request(self, *a, **k):
        content = k.get("content")
        if content is not None:
            try:
                self.payloads.append(json.loads(content))
            except (json.JSONDecodeError, TypeError):
                pass
        return ("req", a, k)

    async def send(self, req, stream=True):
        r = self._responses[self._i]
        self._i += 1
        return r


async def run_fold(cfg, base_body, first_resp, later_resps) -> list:
    client = FakeClient(later_resps)
    out = b""
    async for chunk in fold_stream(client, cfg, base_body, {}, first_resp):
        out += chunk
    return await parse_events(out)


async def run_fold_capture(cfg, base_body, first_resp, client) -> list:
    """Like run_fold but uses a caller-supplied client (to inspect client.payloads)."""
    out = b""
    async for chunk in fold_stream(client, cfg, base_body, {}, first_resp):
        out += chunk
    return await parse_events(out)


async def run_fold_raw(cfg, base_body, first_resp, later_resps) -> bytes:
    """Collect raw downstream bytes, including generated heartbeat events."""
    client = FakeClient(later_resps)
    out = b""
    async for chunk in fold_stream(client, cfg, base_body, {}, first_resp):
        out += chunk
    return out


# --- test registry ----------------------------------------------------------

_RESULTS: list[tuple[str, bool, str]] = []


def check(name: str, cond: bool, detail: str = "") -> None:
    _RESULTS.append((name, bool(cond), detail))


# --- 1. truncation math -----------------------------------------------------


def test_truncation_math():
    for n, tok in enumerate([516, 1034, 1552, 2070, 2588], start=1):
        check(f"is_truncation({tok})", is_truncation_pattern(tok))
        check(f"tier_n({tok})=={n}", tier_n(tok) == n, str(tier_n(tok)))
    for bad in (515, 517, 0, None):
        check(f"not is_truncation({bad})", not is_truncation_pattern(bad))
    # window
    check("should_continue 516 default", should_continue(516, min_n=1, max_n=0))
    check("should_continue 2588 max_n=3 blocked", not should_continue(2588, min_n=1, max_n=3))
    check("should_continue 516 min_n=2 blocked", not should_continue(516, min_n=2, max_n=0))
    check("should_continue None", not should_continue(None, min_n=1, max_n=0))


# --- 2. SSE framing robustness ---------------------------------------------


async def test_sse_framing():
    data = (FIXTURES / "codex_poc_r1.sse.txt").read_bytes()
    whole = await parse_events(data)

    # odd-sized chunks must produce identical events
    async def chunked(src: bytes, size: int):
        for i in range(0, len(src), size):
            yield src[i : i + size]

    pieces = []
    async for e in incremental_sse(chunked(data, 7)):
        pieces.append(e)

    check("sse whole-vs-chunked count", len(whole) == len(pieces), f"{len(whole)} vs {len(pieces)}")
    types_w = [e.get("type") for e in whole if isinstance(e, dict)]
    types_c = [e.get("type") for e in pieces if isinstance(e, dict)]
    check("sse whole-vs-chunked types", types_w == types_c)
    check("sse has completed", "response.completed" in types_w)
    check("sse no spurious DONE", DONE not in whole)  # Codex sends no [DONE]


# --- 3. fold rewrite on real r1 + r2 captures -------------------------------


async def test_fold_real_captures():
    cfg = load_config(ROOT / "config.toml")
    cfg = replace(cfg, cont=replace(cfg.cont, max_continue=1))  # r1 -> continue -> r2 -> stop

    r1 = FakeResp((FIXTURES / "codex_poc_r1.sse.txt").read_bytes())
    r2 = FakeResp((FIXTURES / "codex_poc_r2.sse.txt").read_bytes())
    base_body = {"model": "gpt-5.5", "input": [{"role": "user", "content": "q"}]}

    evs = await run_fold(cfg, base_body, r1, [r2])
    dict_evs = [e for e in evs if isinstance(e, dict)]
    types = [e.get("type") for e in dict_evs]

    check("fold one created", types.count("response.created") == 1)
    check("fold one in_progress", types.count("response.in_progress") == 1)
    check("fold one terminal", sum(types.count(t) for t in
          ("response.completed", "response.failed", "response.incomplete")) == 1)

    seqs = [e["sequence_number"] for e in dict_evs]
    check("fold seq monotonic 0..n", seqs == list(range(len(dict_evs))), str(seqs[:5]))

    # reasoning items forwarded at ds_oi 0 then 1
    rdone = [e for e in dict_evs if e.get("type") == "response.output_item.done"
             and (e.get("item") or {}).get("type") == "reasoning"]
    check("fold 2 reasoning items", len(rdone) == 2, str(len(rdone)))
    check("fold reasoning oi 0,1", [e["output_index"] for e in rdone] == [0, 1],
          str([e.get("output_index") for e in rdone]))

    # message flushed (r2) at ds_oi 2; r1 message discarded
    deltas = "".join(e.get("delta", "") for e in dict_evs
                     if e.get("type") == "response.output_text.delta")
    check("fold r2 answer present", "答案是" in deltas or "21" in deltas, deltas[:40])
    check("fold r1 message discarded", "最少需要取出" not in deltas)

    created = next(e for e in dict_evs if e.get("type") == "response.created")
    completed = dict_evs[-1]
    created_id = (created.get("response") or {}).get("id")
    completed_id = (completed.get("response") or {}).get("id")
    check("fold created/completed share id", created_id == completed_id,
          f"{created_id} vs {completed_id}")
    out_items = (completed.get("response") or {}).get("output") or []
    check("fold reconstructed output non-empty (3 items)", len(out_items) == 3, str(len(out_items)))
    # Agent-facing usage = single-response equivalent (NOT summed input).
    usage = (completed.get("response") or {}).get("usage") or {}
    check("fold input = round1 (4582, not summed)", usage.get("input_tokens") == 4582,
          str(usage.get("input_tokens")))
    check("fold cached = round1 (3840)",
          (usage.get("input_tokens_details") or {}).get("cached_tokens") == 3840)
    rt = (usage.get("output_tokens_details") or {}).get("reasoning_tokens")
    check("fold reasoning summed 3104", rt == 516 + 2588, str(rt))
    # output = summed reasoning + final round's non-reasoning (2947-2588=359)
    check("fold output = reasoning + final msg",
          usage.get("output_tokens") == 3104 + (2947 - 2588), str(usage.get("output_tokens")))
    check("fold total = input + output",
          usage.get("total_tokens") == 4582 + 3104 + (2947 - 2588), str(usage.get("total_tokens")))

    md = (completed.get("response") or {}).get("metadata") or {}
    check("fold proxy_rounds has 2 entries", len(md.get("proxy_rounds") or []) == 2,
          str(md.get("proxy_rounds")))
    check("fold stopped_reason max_continue", md.get("proxy_stopped_reason") == "max_continue",
          str(md.get("proxy_stopped_reason")))
    billed = md.get("proxy_billed_usage") or {}
    check("fold billed input summed 9722", billed.get("input_tokens") == 4582 + 5140,
          str(billed.get("input_tokens")))


# --- 3b. truncated tool call is discarded; clean tool call flushes ----------


def _round(rs_id, enc, reasoning_tokens_val, *, extra_items=None, msg=None):
    evs = [
        {"type": "response.created", "response": {"id": "resp_x", "status": "in_progress",
         "model": "gpt-5.5", "metadata": {}}},
        {"type": "response.in_progress", "response": {"id": "resp_x"}},
        {"type": "response.output_item.added", "output_index": 0,
         "item": {"id": rs_id, "type": "reasoning"}},
        {"type": "response.output_item.done", "output_index": 0,
         "item": {"id": rs_id, "type": "reasoning", "encrypted_content": enc}},
    ]
    oi = 1
    for it in (extra_items or []):
        evs.append({"type": "response.output_item.added", "output_index": oi, "item": it})
        if it["type"] == "function_call":
            evs.append({"type": "response.function_call_arguments.delta", "output_index": oi,
                        "item_id": it["id"], "delta": it.get("arguments", "{}")})
        evs.append({"type": "response.output_item.done", "output_index": oi, "item": it})
        oi += 1
    if msg is not None:
        evs += [
            {"type": "response.output_item.added", "output_index": oi,
             "item": {"id": "msg_x", "type": "message"}},
            {"type": "response.content_part.added", "output_index": oi, "item_id": "msg_x",
             "content_index": 0, "part": {"type": "output_text"}},
            {"type": "response.output_text.delta", "output_index": oi, "item_id": "msg_x",
             "content_index": 0, "delta": msg},
            {"type": "response.output_text.done", "output_index": oi, "item_id": "msg_x",
             "content_index": 0, "text": msg},
            {"type": "response.content_part.done", "output_index": oi, "item_id": "msg_x",
             "content_index": 0, "part": {"type": "output_text", "text": msg}},
            {"type": "response.output_item.done", "output_index": oi,
             "item": {"id": "msg_x", "type": "message",
                      "content": [{"type": "output_text", "text": msg}]}},
        ]
    evs.append({"type": "response.completed", "response": {"id": "resp_x", "status": "completed",
                "usage": {"input_tokens": 100, "output_tokens": 50, "total_tokens": 150,
                          "output_tokens_details": {"reasoning_tokens": reasoning_tokens_val}}}})
    return make_sse(evs)


async def test_sse_heartbeat_while_buffering():
    base = load_config(ROOT / "config.toml")
    check("heartbeat: disabled by default", base.stream.heartbeat_interval_seconds == 0)
    cfg = replace(
        base,
        stream=replace(base.stream, heartbeat_interval_seconds=0.005),
    )
    base_body = {"model": "gpt-5.5", "input": [{"role": "user", "content": "q"}]}

    # The clean final message is tentative and therefore produces no downstream
    # Responses events until the terminal event. Feed it slowly enough for the
    # heartbeat timer to fire while fold_stream is buffering it.
    response = FakeResp(
        _round("rs_hb", "ENC_HB", 999, msg="buffered" * 100),
        chunk=128,
        delay=0.001,
    )
    raw = await run_fold_raw(cfg, base_body, response, [])
    heartbeat_bytes = (
        b"event: response.keep_alive\n"
        b'data: {"type":"response.keep_alive"}\n\n'
    )
    check("heartbeat: SSE event emitted while output buffered", heartbeat_bytes in raw)

    # The heartbeat is a valid parsed SSE event so Codex's event-level idle timer
    # resets, but it is an unknown no-op Responses type with no sequence number.
    parsed = [e for e in await parse_events(raw) if isinstance(e, dict)]
    heartbeats = [e for e in parsed if e.get("type") == "response.keep_alive"]
    evs = [e for e in parsed if e.get("type") != "response.keep_alive"]
    seqs = [e["sequence_number"] for e in evs]
    check("heartbeat: event is parseable by EventSource clients", bool(heartbeats))
    check("heartbeat: events consume no sequence number", seqs == list(range(len(evs))))

    disabled = replace(cfg, stream=replace(cfg.stream, heartbeat_interval_seconds=0))
    raw_disabled = await run_fold_raw(
        disabled,
        base_body,
        FakeResp(_round("rs_no_hb", "ENC", 999, msg="done"), chunk=128, delay=0.001),
        [],
    )
    check("heartbeat: zero interval disables events", heartbeat_bytes not in raw_disabled)


async def test_truncated_tool_call_discarded():
    cfg = load_config(ROOT / "config.toml")
    base_body = {"model": "gpt-5.5", "input": [{"role": "user", "content": "q"}]}

    # Round A: truncated (516) + a real tool call. Round B: clean message.
    tool = {"id": "fc_a", "type": "function_call", "name": "shell", "call_id": "call_a",
            "arguments": "{\"cmd\":\"ls\"}"}
    rA = FakeResp(_round("rs_a", "ENC_A", 516, extra_items=[tool]))
    rB = FakeResp(_round("rs_b", "ENC_B", 999, msg="done"))

    evs = [e for e in await run_fold(cfg, base_body, rA, [rB]) if isinstance(e, dict)]
    has_fc = any((e.get("item") or {}).get("type") == "function_call" for e in evs)
    fc_args = any(e.get("type") == "response.function_call_arguments.delta" for e in evs)
    check("truncated tool call discarded (no fc item)", not has_fc)
    check("truncated tool call discarded (no fc args)", not fc_args)
    deltas = "".join(e.get("delta", "") for e in evs
                     if e.get("type") == "response.output_text.delta")
    check("clean round message flushed", deltas == "done", deltas)

    # Clean round ending in a tool call → must flush it through.
    rOnly = FakeResp(_round("rs_c", "ENC_C", 999, extra_items=[tool]))
    evs2 = [e for e in await run_fold(cfg, base_body, rOnly, []) if isinstance(e, dict)]
    has_fc2 = any((e.get("item") or {}).get("type") == "function_call" for e in evs2)
    check("clean round tool call flushed", has_fc2)


# --- commentary continuation (default) vs tool_pair --------------------------


async def test_commentary_continuation_payload():
    cfg = load_config(ROOT / "config.toml")  # method = "commentary" by default
    base_body = {"model": "gpt-5.5", "input": [{"role": "user", "content": "q"}]}
    rA = FakeResp(_round("rs_a", "ENC_A", 516, msg="trunc"))  # truncated → continue
    rB = FakeResp(_round("rs_b", "ENC_B", 999, msg="done"))   # clean → stop
    client = FakeClient([rB])
    evs = [e for e in await run_fold_capture(cfg, base_body, rA, client) if isinstance(e, dict)]

    check("commentary: one continuation round opened", len(client.payloads) == 1,
          str(len(client.payloads)))
    inp = (client.payloads[0].get("input") if client.payloads else []) or []
    last = inp[-1] if inp else {}
    check("commentary: marker is a phase:commentary assistant message",
          last.get("type") == "message" and last.get("role") == "assistant"
          and last.get("phase") == "commentary", str(last))
    check("commentary: marker text from config",
          (last.get("content") or [{}])[0].get("text") == cfg.cont.marker_text)
    check("commentary: no function_call injected in replay",
          not any(isinstance(x, dict) and x.get("type") == "function_call" for x in inp))
    check("commentary: prior reasoning replayed (encrypted)",
          any(isinstance(x, dict) and x.get("type") == "reasoning"
              and x.get("encrypted_content") for x in inp))
    # forward_marker defaults false → marker stays hidden from the downstream stream
    check("commentary: marker hidden downstream by default",
          not any((e.get("item") or {}).get("phase") == "commentary" for e in evs))


async def test_tool_pair_continuation_payload():
    base = load_config(ROOT / "config.toml")
    cfg = replace(base, cont=replace(base.cont, method="tool_pair"))
    base_body = {"model": "gpt-5.5", "input": [{"role": "user", "content": "q"}]}
    rA = FakeResp(_round("rs_a", "ENC_A", 516, msg="trunc"))
    rB = FakeResp(_round("rs_b", "ENC_B", 999, msg="done"))
    client = FakeClient([rB])
    await run_fold_capture(cfg, base_body, rA, client)

    inp = (client.payloads[0].get("input") if client.payloads else []) or []
    types = [x.get("type") for x in inp if isinstance(x, dict)]
    check("tool_pair: function_call + output injected",
          "function_call" in types and "function_call_output" in types, str(types))
    check("tool_pair: no commentary message in replay",
          not any(isinstance(x, dict) and x.get("phase") == "commentary" for x in inp))


async def test_forward_marker_emits_downstream():
    base = load_config(ROOT / "config.toml")
    cfg = replace(base, cont=replace(base.cont, method="commentary", forward_marker=True))
    base_body = {"model": "gpt-5.5", "input": [{"role": "user", "content": "q"}]}
    rA = FakeResp(_round("rs_a", "ENC_A", 516, msg="trunc"))
    rB = FakeResp(_round("rs_b", "ENC_B", 999, msg="done"))
    evs = [e for e in await run_fold(cfg, base_body, rA, [rB]) if isinstance(e, dict)]

    done = [e for e in evs if e.get("type") == "response.output_item.done"
            and (e.get("item") or {}).get("phase") == "commentary"]
    check("forward_marker: one commentary item emitted downstream", len(done) == 1,
          str(len(done)))
    delta = "".join(e.get("delta", "") for e in evs
                    if e.get("type") == "response.output_text.delta"
                    and e.get("item_id", "").startswith("msg_continue_"))
    check("forward_marker: commentary delta carries marker text",
          delta == cfg.cont.marker_text, delta)
    # reconstructed output carries the commentary item (so the agent echoes it)
    completed = evs[-1]
    out_items = (completed.get("response") or {}).get("output") or []
    phases = [it.get("phase") for it in out_items if isinstance(it, dict)]
    check("forward_marker: commentary in reconstructed output", "commentary" in phases,
          str(phases))
    # sequence numbers stay monotonic 0..n despite the injected item
    seqs = [e["sequence_number"] for e in evs]
    check("forward_marker: seq monotonic with injected marker",
          seqs == list(range(len(evs))), str(seqs[:6]))


# --- 2-fix. header transparency (#2) ----------------------------------------


def test_header_transparency():
    cfg = load_config(ROOT / "config.toml")
    client = _make_client()
    check("client invents no user-agent", "user-agent" not in client.headers)
    check("client invents no accept", "accept" not in client.headers)

    agent = [
        ("Authorization", "Bearer agent"),
        ("Content-Type", "application/json"),
        ("User-Agent", "codex_cli_rs/1.0"),
        ("Host", "drop.me"),
        ("Content-Length", "123"),
        ("Accept-Encoding", "gzip"),
        ("Responses-API-Base", "https://override/responses"),
        ("X-Custom", "keep"),
    ]
    out = build_upstream_headers(agent, cfg)
    low = {k.lower(): v for k, v in out.items()}
    check("hdr keeps content-type", low.get("content-type") == "application/json")
    check("hdr keeps user-agent", low.get("user-agent") == "codex_cli_rs/1.0")
    check("hdr keeps custom", low.get("x-custom") == "keep")
    check("hdr keeps authorization", low.get("authorization") == "Bearer agent")
    for dropped in ("host", "content-length", "accept-encoding", "responses-api-base"):
        check(f"hdr drops {dropped}", dropped not in low)


# --- upstream URL resolution via Responses-API-Base header ------------------


class _Req:
    def __init__(self, headers: dict):
        self.headers = Headers(headers)


def test_upstream_url_resolution():
    base = load_config(ROOT / "config.toml")
    fixed = replace(base, upstream=replace(base.upstream, mode="fixed", url="https://cfg/responses"))
    header = replace(base, upstream=replace(base.upstream, mode="header", url="https://cfg/responses"))
    with_hdr = _Req({"Responses-API-Base": "https://override/v1"})
    no_hdr = _Req({})

    check("fixed ignores header", _resolve_upstream_url(fixed, with_hdr) == "https://cfg/responses")
    check("header appends /responses to base",
          _resolve_upstream_url(header, with_hdr) == "https://override/v1/responses")
    check("header falls back to url",
          _resolve_upstream_url(header, no_hdr) == "https://cfg/responses")
    check("header trims trailing slash + case-insensitive",
          _resolve_upstream_url(header, _Req({"responses-api-base": "https://low/v1/"})) == "https://low/v1/responses")
    check("header full endpoint left as-is",
          _resolve_upstream_url(header, _Req({"Responses-API-Base": "https://x/v1/responses"})) == "https://x/v1/responses")
    check("header blank → fallback",
          _resolve_upstream_url(header, _Req({"Responses-API-Base": "   "})) == "https://cfg/responses")

    # header_required: present → use it; absent/blank → None (caller returns 400)
    req = replace(base, upstream=replace(base.upstream, mode="header_required", url="https://cfg/responses"))
    check("required appends /responses",
          _resolve_upstream_url(req, with_hdr) == "https://override/v1/responses")
    check("required missing → None", _resolve_upstream_url(req, no_hdr) is None)
    check("required blank → None",
          _resolve_upstream_url(req, _Req({"Responses-API-Base": " "})) is None)


# --- transparent passthrough URL mapping (models + other endpoints) ----------


class _PURL:
    def __init__(self, path: str, query: str = ""):
        self.path = path
        self.query = query


class _PReq:
    """Minimal request fake for passthrough URL resolution (path + query + headers)."""

    def __init__(self, path: str, headers: dict | None = None, query: str = ""):
        self.url = _PURL(path, query)
        self.headers = Headers(headers or {})


def test_passthrough_url_resolution():
    from middleware.app import _resolve_passthrough_url

    base = load_config(ROOT / "config.toml")
    base = replace(base, server=replace(base.server, listen_paths=("/v1/responses",)))

    fixed = replace(base, upstream=replace(base.upstream, mode="fixed",
                    url="https://h/v1/responses"))
    check("pt /v1/models -> base/models",
          _resolve_passthrough_url(fixed, _PReq("/v1/models")) == "https://h/v1/models")
    check("pt /v1/embeddings -> base/embeddings",
          _resolve_passthrough_url(fixed, _PReq("/v1/embeddings")) == "https://h/v1/embeddings")
    check("pt /v1/responses maps back to responses endpoint",
          _resolve_passthrough_url(fixed, _PReq("/v1/responses")) == "https://h/v1/responses")
    check("pt preserves query string",
          _resolve_passthrough_url(fixed, _PReq("/v1/models", query="limit=10"))
          == "https://h/v1/models?limit=10")

    # ChatGPT-style URL: strip '/responses' -> base path is /backend-api/codex.
    cg = replace(base, upstream=replace(base.upstream, mode="fixed",
                 url="https://chatgpt.com/backend-api/codex/responses"))
    check("pt chatgpt base mapping",
          _resolve_passthrough_url(cg, _PReq("/v1/models"))
          == "https://chatgpt.com/backend-api/codex/models")

    # header mode: base derived from the Responses-API-Base header.
    header = replace(base, upstream=replace(base.upstream, mode="header",
                     url="https://cfg/responses"))
    check("pt header base mapping",
          _resolve_passthrough_url(header, _PReq("/v1/models",
               headers={"Responses-API-Base": "https://ov/v1"})) == "https://ov/v1/models")

    # header_required without header -> None (caller returns 400).
    req = replace(base, upstream=replace(base.upstream, mode="header_required",
                  url="https://cfg/responses"))
    check("pt header_required no header -> None",
          _resolve_passthrough_url(req, _PReq("/v1/models")) is None)


async def test_passthrough_preserves_method():
    from middleware.proxy import open_passthrough
    seen: dict = {}

    class _CapClient:
        def build_request(self, *a, **k):
            seen["method"] = a[0] if a else k.get("method")
            return ("req", a, k)

        async def send(self, req, stream=True):
            return FakeResp(b'{"data": []}', status=200)

    await open_passthrough(_CapClient(), "https://h/v1/models", b"", {}, method="GET")
    check("passthrough forwards GET method", seen.get("method") == "GET", str(seen))
    await open_passthrough(_CapClient(), "https://h/v1/responses", b"{}", {}, method="POST")
    check("passthrough forwards POST method", seen.get("method") == "POST")
    seen.clear()
    await open_passthrough(_CapClient(), "https://h/v1/models", b"", {})  # default
    check("passthrough defaults to POST", seen.get("method") == "POST", str(seen))


async def test_passthrough_routing():
    """Exercise Starlette's router, not just the passthrough helper functions."""
    import httpx
    import middleware.app as app_module
    from starlette.responses import PlainTextResponse

    async def fold_marker(request):
        return PlainTextResponse(f"FOLD {request.method}")

    async def passthrough_marker(request):
        return PlainTextResponse(f"PASS {request.method}")

    # create_app captures the handlers in Route objects, so restoring the module
    # globals immediately afterward keeps this test isolated from the rest.
    original_fold = app_module.handle_responses
    original_passthrough = app_module.handle_passthrough
    try:
        app_module.handle_responses = fold_marker
        app_module.handle_passthrough = passthrough_marker
        app = app_module.create_app(load_config(ROOT / "config.toml"))
    finally:
        app_module.handle_responses = original_fold
        app_module.handle_passthrough = original_passthrough

    transport = httpx.ASGITransport(app=app)
    async with httpx.AsyncClient(transport=transport, base_url="http://test") as client:
        for method in ("GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"):
            response = await client.request(method, "/v1/models")
            check(f"route passes {method}", response.status_code == 200,
                  f"status={response.status_code}")
            if method != "HEAD":
                check(f"route dispatches {method} to passthrough",
                      response.text == f"PASS {method}", response.text)

        response = await client.post("/v1/responses")
        check("route keeps POST /v1/responses on fold handler",
              response.text == "FOLD POST", response.text)
        response = await client.get("/v1/responses")
        check("route sends GET /v1/responses to passthrough",
              response.text == "PASS GET", response.text)


# --- security guard: never send config creds to a header-supplied URL --------


def test_auth_safety_guard():
    base = load_config(ROOT / "config.toml")

    def blocked(url_mode, auth_mode, token, has_hdr, has_auth):
        cfg = replace(
            base,
            upstream=replace(base.upstream, mode=url_mode),
            auth=replace(base.auth, mode=auth_mode, access_token=token),
        )
        h = {}
        if has_hdr:
            h["Responses-API-Base"] = "https://external/responses"
        if has_auth:
            h["Authorization"] = "Bearer agent"
        rq = _Req(h)
        from_hdr = _url_is_from_header(cfg, rq)
        inj = would_inject_authorization(
            cfg, agent_has_authorization=rq.headers.get("authorization") is not None
        )
        return from_hdr and inj  # the exact condition handle_responses rejects on

    # fixed url → always safe
    check("guard: fixed+inject allow", not blocked("fixed", "inject", "TOK", True, False))
    # header + passthrough → never injects → allow
    check("guard: header+passthrough allow",
          not blocked("header", "passthrough", "TOK", True, False))
    # header + inject + header present → block (even if agent has its own auth)
    check("guard: header+inject+hdr block (noauth)",
          blocked("header", "inject", "TOK", True, False))
    check("guard: header+inject+hdr block (auth)",
          blocked("header", "inject", "TOK", True, True))
    # header + inject, no header → config url → allow
    check("guard: header+inject no-hdr allow",
          not blocked("header", "inject", "TOK", False, False))
    # header + PtI + header + agent has own auth → allow (uses agent's)
    check("guard: header+PtI+hdr+auth allow",
          not blocked("header", "passthrough_then_inject", "TOK", True, True))
    # header + PtI + header + no agent auth → block (would inject config)
    check("guard: header+PtI+hdr+noauth block",
          blocked("header", "passthrough_then_inject", "TOK", True, False))
    # header_required + inject + header → block
    check("guard: required+inject+hdr block",
          blocked("header_required", "inject", "TOK", True, False))
    # empty configured token → nothing to leak → allow
    check("guard: empty token allow", not blocked("header", "inject", "", True, False))


# --- auth injection from config (#2 follow-up) ------------------------------


def test_auth_injection():
    base = load_config(ROOT / "config.toml")

    def hdrs(cfg, agent):
        return {k.lower(): v for k, v in build_upstream_headers(agent, cfg).items()}

    # passthrough_then_inject: inject token when agent sends none; empty account → no header
    cfg = replace(base, auth=replace(base.auth, mode="passthrough_then_inject",
                                     access_token="TOK", chatgpt_account_id=""))
    out = hdrs(cfg, [("x", "1")])
    check("inject token when missing", out.get("authorization") == "Bearer TOK")
    check("no account header when empty", "chatgpt-account-id" not in out)

    # passthrough_then_inject: agent's auth wins (not overridden)
    out2 = hdrs(cfg, [("Authorization", "Bearer AGENT")])
    check("fallback keeps agent auth", out2.get("authorization") == "Bearer AGENT")

    # inject: config overrides agent + adds account
    cfg2 = replace(base, auth=replace(base.auth, mode="inject",
                                      access_token="TOK", chatgpt_account_id="acct1"))
    out3 = hdrs(cfg2, [("Authorization", "Bearer AGENT")])
    check("inject overrides agent auth", out3.get("authorization") == "Bearer TOK")
    check("inject adds account", out3.get("chatgpt-account-id") == "acct1")

    # passthrough: never inject anything
    cfg3 = replace(base, auth=replace(base.auth, mode="passthrough",
                                      access_token="TOK", chatgpt_account_id="acct1"))
    out4 = hdrs(cfg3, [("x", "1")])
    check("passthrough never injects", "authorization" not in out4 and "chatgpt-account-id" not in out4)


# --- 4-fix. reasoning/stream gating (#4) ------------------------------------


def test_reasoning_gate():
    check("reasoning_enabled dict", reasoning_enabled({"reasoning": {"effort": "high"}}))
    check("reasoning_enabled absent → true", reasoning_enabled({"input": []}))
    check("reasoning_enabled null → true", reasoning_enabled({"reasoning": None}))
    check("reasoning_enabled empty dict → true", reasoning_enabled({"reasoning": {}}))
    check("reasoning_enabled explicit false → false", not reasoning_enabled({"reasoning": False}))


# --- 4b-fix. legacy tool-search replay repair --------------------------------


def test_legacy_tool_search_argument_repair():
    body = {
        "input": [
            {
                "type": "function_call",
                "name": "shell_command",
                "arguments": "{\"command\":\"pwd\"}",
            },
            {
                "type": "tool_search_call",
                "call_id": "call_old",
                "arguments": "{\"query\":\"read_thread\",\"limit\":5}",
            },
            {
                "type": "tool_search_call",
                "call_id": "call_new",
                "arguments": {"query": "already new", "limit": 10},
            },
        ]
    }
    repaired, count = _repair_legacy_tool_search_arguments(body)
    items = repaired["input"]
    check("legacy tool_search repair count", count == 1, str(count))
    check("legacy tool_search args now object", isinstance(items[1]["arguments"], dict))
    check("legacy tool_search parsed limit", items[1]["arguments"].get("limit") == 5)
    check("function_call args remain string", isinstance(items[0]["arguments"], str))
    check("already-object tool_search unchanged", items[2]["arguments"]["limit"] == 10)
    check("repair does not mutate original", isinstance(body["input"][1]["arguments"], str))


def test_zstd_request_body_decode():
    raw = json.dumps({"model": "gpt-5.6-sol", "input": ["fixture"]}).encode()
    compressed = zstd.ZstdCompressor().compress(raw)
    decoded, was_decoded = _decode_request_body(compressed, "zstd")
    check("zstd request body decoded", decoded == raw)
    check("zstd request body marked decoded", was_decoded)

    identity, identity_decoded = _decode_request_body(raw, None)
    check("identity request body unchanged", identity == raw and not identity_decoded)

    headers = {"Content-Encoding": "zstd", "Content-Type": "application/json"}
    _drop_header(headers, "content-encoding")
    check("decoded request drops content-encoding", "Content-Encoding" not in headers)


# --- 3-fix. stateful follow-up repair (#3) ----------------------------------


def test_stateful_repair():
    store = IdStore()
    store.add("rs_keep")
    inp = [
        {"role": "user", "content": "q"},
        {"type": "reasoning", "id": "rs_keep", "encrypted_content": "E1"},
        {"type": "reasoning", "id": "rs_natural", "encrypted_content": "E2"},  # not recorded
        {"type": "message", "id": "msg"},
    ]
    out = repair_followup_input(inp, store, tool_name="continue_thinking", output_text="go")

    # pair inserted right after rs_keep only
    idx = next(i for i, x in enumerate(out)
               if isinstance(x, dict) and x.get("id") == "rs_keep")
    nxt = out[idx + 1]
    nxt2 = out[idx + 2]
    cid = continue_call_id("rs_keep")
    check("stateful inserts call after recorded id",
          nxt.get("type") == "function_call" and nxt.get("call_id") == cid, str(nxt))
    check("stateful inserts output after call",
          nxt2.get("type") == "function_call_output" and nxt2.get("call_id") == cid)

    # natural-consecutive reasoning (unrecorded) gets NO splice
    nidx = next(i for i, x in enumerate(out)
                if isinstance(x, dict) and x.get("id") == "rs_natural")
    check("stateful no splice for unrecorded id",
          out[nidx + 1].get("type") == "message", str(out[nidx + 1]))

    # idempotent: re-running adds nothing
    out2 = repair_followup_input(out, store, tool_name="continue_thinking", output_text="go")
    check("stateful idempotent", len(out2) == len(out), f"{len(out)} -> {len(out2)}")


# --- 7-fix. graceful EOF → incomplete (#7) ----------------------------------


async def test_eof_incomplete():
    cfg = load_config(ROOT / "config.toml")
    base_body = {"model": "gpt-5.5", "input": [{"role": "user", "content": "q"}]}
    # A round that streams reasoning + message but NO terminal event.
    events = [
        {"type": "response.created", "response": {"id": "resp_e", "status": "in_progress"}},
        {"type": "response.in_progress", "response": {"id": "resp_e"}},
        {"type": "response.output_item.added", "output_index": 0,
         "item": {"id": "rs_e", "type": "reasoning"}},
        {"type": "response.output_item.done", "output_index": 0,
         "item": {"id": "rs_e", "type": "reasoning", "encrypted_content": "E"}},
        {"type": "response.output_item.added", "output_index": 1,
         "item": {"id": "msg_e", "type": "message"}},
        {"type": "response.output_text.delta", "output_index": 1, "item_id": "msg_e",
         "content_index": 0, "delta": "partial"},
        {"type": "response.output_item.done", "output_index": 1,
         "item": {"id": "msg_e", "type": "message"}},
        # <-- no response.completed
    ]
    evs = [e for e in await run_fold(cfg, base_body, FakeResp(make_sse(events)), [])
           if isinstance(e, dict)]
    term = evs[-1]
    check("eof terminal is incomplete", term.get("type") == "response.incomplete",
          term.get("type"))
    reason = ((term.get("response") or {}).get("incomplete_details") or {}).get("reason")
    check("eof reason upstream_eof", reason == "upstream_eof", str(reason))

    # buffered tentative output must NOT leak on EOF (only reasoning survives)
    leaked = any(e.get("type") == "response.output_text.delta" for e in evs)
    check("eof does not leak buffered message", not leaked)
    out_items = (term.get("response") or {}).get("output") or []
    check("eof output is reasoning only",
          all(it.get("type") == "reasoning" for it in out_items) and len(out_items) == 1,
          str([it.get("type") for it in out_items]))


# --- 8-fix. failed terminal preserves upstream error -------------------------


async def test_failed_error_preserved():
    cfg = load_config(ROOT / "config.toml")
    base_body = {"model": "gpt-5.5", "input": [{"role": "user", "content": "q"}]}
    upstream_error = {
        "type": "server_error",
        "code": "upstream_overloaded",
        "message": "The upstream service is temporarily overloaded.",
    }
    events = [
        {"type": "response.created", "response": {
            "id": "resp_f", "status": "in_progress", "error": None,
        }},
        {"type": "response.in_progress", "response": {"id": "resp_f"}},
        {"type": "response.failed", "response": {
            "id": "resp_f", "status": "failed", "error": upstream_error,
        }},
    ]
    evs = [e for e in await run_fold(cfg, base_body, FakeResp(make_sse(events)), [])
           if isinstance(e, dict)]
    term = evs[-1]
    response = term.get("response") or {}
    check("failed terminal type preserved", term.get("type") == "response.failed",
          str(term.get("type")))
    check("failed status preserved", response.get("status") == "failed",
          str(response.get("status")))
    check("failed error preserved", response.get("error") == upstream_error,
          str(response.get("error")))


# --- runner -----------------------------------------------------------------


def test_function_call_output_order_repair():
    body = {
        "input": [
            {"type": "function_call", "call_id": "call_a"},
            {"type": "function_call", "call_id": "call_b"},
            {"type": "function_call", "call_id": "call_c"},
            {"type": "function_call_output", "call_id": "call_a"},
            {"type": "message", "role": "developer", "content": "notice"},
            {"type": "function_call_output", "call_id": "call_b"},
            {"type": "function_call_output", "call_id": "call_c"},
        ]
    }
    repaired, count = _canonicalize_function_call_outputs(body)
    items = repaired["input"]
    order = [(item.get("type"), item.get("call_id")) for item in items]
    check("tool output order repair count", count == 3, str(count))
    check(
        "tool outputs follow call batch",
        order
        == [
            ("function_call", "call_a"),
            ("function_call", "call_b"),
            ("function_call", "call_c"),
            ("function_call_output", "call_a"),
            ("function_call_output", "call_b"),
            ("function_call_output", "call_c"),
            ("message", None),
        ],
        str(order),
    )
    check("developer notice moved after outputs", items[-1].get("role") == "developer")
    check(
        "tool output repair does not mutate original",
        body["input"][4].get("type") == "message",
        str(body["input"]),
    )

    repaired_again, count_again = _canonicalize_function_call_outputs(repaired)
    check(
        "tool output order repair idempotent",
        count_again == 0 and repaired_again is repaired,
        f"count={count_again}",
    )


async def _main():
    test_truncation_math()
    await test_sse_framing()
    await test_fold_real_captures()
    await test_sse_heartbeat_while_buffering()
    await test_truncated_tool_call_discarded()
    await test_commentary_continuation_payload()
    await test_tool_pair_continuation_payload()
    await test_forward_marker_emits_downstream()
    test_header_transparency()
    test_upstream_url_resolution()
    test_passthrough_url_resolution()
    await test_passthrough_preserves_method()
    await test_passthrough_routing()
    test_auth_safety_guard()
    test_auth_injection()
    test_reasoning_gate()
    test_legacy_tool_search_argument_repair()
    test_zstd_request_body_decode()
    test_stateful_repair()
    await test_eof_incomplete()
    await test_failed_error_preserved()
    test_function_call_output_order_repair()


def main():
    asyncio.run(_main())
    passed = sum(1 for _, ok, _ in _RESULTS if ok)
    for name, ok, detail in _RESULTS:
        mark = "PASS" if ok else "FAIL"
        line = f"[{mark}] {name}"
        if not ok and detail:
            line += f"  -- {detail}"
        print(line)
    print(f"\n{passed}/{len(_RESULTS)} checks passed")
    sys.exit(0 if passed == len(_RESULTS) else 1)


if __name__ == "__main__":
    main()
