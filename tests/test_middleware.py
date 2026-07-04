#!/usr/bin/env python3
"""Offline tests for the continue_thinking middleware.

Run: .venv/Scripts/python.exe tests/test_middleware.py
No pytest dependency — a tiny runner prints PASS/FAIL per check.
"""
from __future__ import annotations

import asyncio
import hashlib
import json
import sys
import tempfile
from dataclasses import replace
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
FIXTURES = Path(__file__).resolve().parent / "fixtures"
sys.path.insert(0, str(ROOT))

import zstandard as zstd
from starlette.datastructures import Headers
from starlette.testclient import TestClient

from middleware.app import (
    create_app,
    _decode_request_body,
    _make_client,
    _resolve_upstream_url,
    _url_is_from_header,
)
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
from middleware.diagnostics import Diagnostics, redact_value
from middleware.engine import summarize_engine_payload
from middleware.key_identity import KeyIdentityResolver
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
    def __init__(self, data: bytes, status: int = 200, chunk: int = 4096):
        self._data = data
        self.status_code = status
        self.headers: dict[str, str] = {}
        self._chunk = chunk

    async def aiter_bytes(self):
        for i in range(0, len(self._data), self._chunk):
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
        ("Content-Encoding", "zstd"),
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
    for dropped in (
        "host",
        "content-length",
        "content-encoding",
        "accept-encoding",
        "responses-api-base",
    ):
        check(f"hdr drops {dropped}", dropped not in low)


def test_zstd_request_body_decode():
    raw = b'{"model":"gpt-5.5","stream":true}'
    encoded = zstd.ZstdCompressor().compress(raw)
    check("zstd body decodes", _decode_request_body(encoded, "zstd") == raw)
    check("identity body unchanged", _decode_request_body(raw, None) == raw)


# --- dashboard diagnostics --------------------------------------------------


def test_diagnostics_ring_and_redaction():
    diag = Diagnostics(max_events=2)
    diag.record("info", "first", "Authorization: Bearer abc123",
                authorization="Bearer abc123", nested={"api_key": "secret"})
    diag.record("info", "second", "ok")
    diag.record("warning", "third", "access_token=abc")
    recent = diag.recent()
    check("diagnostics ring keeps max events", [e["event"] for e in recent] == ["second", "third"],
          str([e["event"] for e in recent]))
    bearer_redacted = redact_value("Authorization: Bearer abc123")
    check("redact bearer text",
          "abc123" not in bearer_redacted and "[REDACTED]" in bearer_redacted,
          bearer_redacted)
    redacted = redact_value({"api_key": "secret", "safe": "value"})
    check("redact sensitive dict key", redacted.get("api_key") == "[REDACTED]")
    check("keep safe dict key", redacted.get("safe") == "value")
    token_counts = redact_value({"reasoning_tokens": 516, "total_tokens": 1024})
    check("keep token counters", token_counts.get("reasoning_tokens") == 516)


def test_diagnostics_request_summaries():
    with tempfile.TemporaryDirectory() as d:
        raw = "cpa_live_key"
        key_hash = hashlib.sha256(raw.encode("utf-8")).hexdigest()
        state_path = Path(d) / "key-policy.json"
        state_path.write_text(json.dumps({
            "keys": [{
                "id": "alice-key",
                "key_hash": f"sha256:{key_hash}",
                "name": "Alice",
                "preview": "cpa_...live",
                "enabled": True,
            }]
        }), encoding="utf-8")
        identity = KeyIdentityResolver(str(state_path)).identify_authorization(f"Bearer {raw}")
        check("key identity resolves bearer safely",
              identity.get("known") is True
              and identity.get("name") == "Alice"
              and raw not in json.dumps(identity),
              str(identity))
        unknown = KeyIdentityResolver(str(state_path)).identify_authorization("Bearer other")
        check("key identity unknown uses hash preview",
              unknown.get("known") is False
              and unknown.get("name") == "未识别 Key"
              and "other" not in json.dumps(unknown),
              str(unknown))

    clean = Diagnostics(max_events=10, max_requests=5)
    rid = clean.request_started(
        path="/v1/responses",
        model="gpt-5.5",
        key_identity={"known": True, "name": "Alice", "preview": "cpa_...live", "source": "test"},
    )
    clean.mark_fold_start(rid, model="gpt-5.5", path="/v1/responses",
                          upstream_url="http://cpa:8317/v1/responses")
    clean.round_decision(rid, round_no=1, reasoning_tokens=140, n=None,
                         decision="clean", buffered=["message"], truncation_match=False)
    clean.request_finished(rid, status="completed", stopped_reason="natural")
    summary = clean.recent_requests(limit=1)[0]
    check("request summary protected clean", summary.get("protection") == "protected_clean",
          str(summary))
    check("request summary keeps reasoning tokens",
          summary.get("latest_reasoning_tokens") == 140, str(summary))
    check("request summary clean has no truncation round",
          summary.get("first_truncation_round") is None, str(summary))
    check("request summary exposes safe key identity",
          (summary.get("key_identity") or {}).get("name") == "Alice"
          and "cpa_live_key" not in json.dumps(summary),
          str(summary))

    cont = Diagnostics(max_events=10, max_requests=5)
    rid = cont.request_started(path="/v1/responses", model="gpt-5.5")
    cont.mark_fold_start(rid, model="gpt-5.5", path="/v1/responses",
                         upstream_url="http://cpa:8317/v1/responses")
    cont.round_decision(rid, round_no=1, reasoning_tokens=516, n=1,
                        decision="continue", buffered=["message"], truncation_match=True)
    cont.continuation_opened(rid, from_round=1, next_round=2, method="commentary")
    cont.round_decision(rid, round_no=2, reasoning_tokens=181, n=None,
                        decision="clean", buffered=["message"], truncation_match=False)
    cont.request_finished(rid, status="completed", stopped_reason="natural")
    summary = cont.recent_requests(limit=1)[0]
    check("request summary auto continued", summary.get("protection") == "auto_continued",
          str(summary))
    check("request summary continuation count", summary.get("continuation_count") == 1,
          str(summary))
    check("request summary records first truncation round",
          summary.get("first_truncation_round") == 1, str(summary))
    check("request summary records first truncation tokens",
          summary.get("first_truncation_reasoning_tokens") == 516, str(summary))
    check("request summary latest reasoning can be clean round",
          summary.get("latest_reasoning_tokens") == 181, str(summary))

    risk = Diagnostics(max_events=10, max_requests=5)
    rid = risk.request_started(path="/v1/responses", model="gpt-5.5")
    risk.mark_fold_start(rid, model="gpt-5.5", path="/v1/responses",
                         upstream_url="http://cpa:8317/v1/responses")
    risk.round_decision(rid, round_no=1, reasoning_tokens=516, n=1,
                        decision="no_encrypted_content", buffered=["message"], truncation_match=True)
    risk.request_finished(rid, status="completed", stopped_reason="no_encrypted_content")
    summary = risk.recent_requests(limit=1)[0]
    check("request summary risk uncontinued", summary.get("protection") == "risk_uncontinued",
          str(summary))
    check("request summary risk decision captured",
          summary.get("first_truncation_decision") == "no_encrypted_content", str(summary))

    passthrough = Diagnostics(max_events=10, max_requests=5)
    rid = passthrough.request_started(path="/v1/responses", model="gpt-5.5")
    passthrough.mark_passthrough(rid, reason="non-stream", model="gpt-5.5")
    passthrough.request_finished(rid, status="passthrough:200")
    summary = passthrough.recent_requests(limit=1)[0]
    check("request summary passthrough", summary.get("protection") == "passthrough",
          str(summary))

    failed = Diagnostics(max_events=10, max_requests=5)
    rid = failed.request_started(path="/v1/responses", model="gpt-5.5")
    failed.request_failed(rid, reason="invalid_json_body")
    summary = failed.recent_requests(limit=1)[0]
    check("request summary failed", summary.get("protection") == "failed", str(summary))

    retained = Diagnostics(max_events=10, max_requests=2)
    for idx in range(3):
        rid = retained.request_started(path="/v1/responses", model=f"m{idx}")
        retained.request_finished(rid, status="completed")
    summaries = retained.recent_requests()
    check("request summary retention bounded", len(summaries) == 2, str(summaries))
    check("request summary retention newest", [s["model"] for s in summaries] == ["m1", "m2"],
          str(summaries))


async def test_diagnostics_subscriber_broadcast():
    diag = Diagnostics(max_events=5)
    queue = diag.subscribe()
    diag.record("info", "broadcast", "hello", request_id="req1")
    item = await asyncio.wait_for(queue.get(), timeout=1.0)
    diag.unsubscribe(queue)
    check("diagnostics subscriber receives event", item.get("event") == "broadcast", str(item))
    check("diagnostics subscriber receives fields",
          (item.get("fields") or {}).get("request_id") == "req1", str(item))


def test_admin_routes_smoke():
    base = load_config(ROOT / "config.toml")
    cfg = replace(
        base,
        upstream=replace(base.upstream, url="http://127.0.0.1:9/v1/responses"),
    )
    with TestClient(create_app(cfg)) as client:
        health = client.get("/admin/healthz")
        check("admin healthz 200", health.status_code == 200, str(health.status_code))
        check("admin healthz ok", health.json().get("ok") is True, str(health.text))

        client.app.state.diagnostics.record("info", "manual_event", "hello")
        rid = client.app.state.diagnostics.request_started(path="/v1/responses", model="gpt-5.5")
        client.app.state.diagnostics.mark_fold_start(
            rid, model="gpt-5.5", path="/v1/responses",
            upstream_url="http://127.0.0.1:9/v1/responses"
        )
        client.app.state.diagnostics.request_finished(rid, status="completed")
        logs = client.get("/admin/logs?limit=1")
        body = logs.json()
        check("admin logs 200", logs.status_code == 200, str(logs.status_code))
        check("admin logs returns recent event",
              (body.get("events") or [{}])[-1].get("event") == "request_finished", str(body))

        requests = client.get("/admin/requests?limit=1")
        requests_body = requests.json()
        check("admin requests 200", requests.status_code == 200, str(requests.status_code))
        check("admin requests returns summary",
              (requests_body.get("requests") or [{}])[-1].get("protection") == "protected_clean",
              str(requests_body))

        status = client.get("/admin/status")
        status_body = status.json()
        check("admin status 200", status.status_code == 200, str(status.status_code))
        check("admin status has counters", "counters" in status_body, str(status_body))
        check("admin status redacted config host",
              (status_body.get("config") or {}).get("upstream_host") == "127.0.0.1:9",
              str(status_body.get("config")))

        html = client.get("/admin/")
        check("admin dashboard html 200", html.status_code == 200, str(html.status_code))
        check("admin dashboard contains EventSource", "new EventSource" in html.text)
        check("admin dashboard Chinese first screen", "最近请求" in html.text)
        check("admin dashboard has trigger round column", "命中轮" in html.text)
        check("admin dashboard has latest reasoning column", "末轮思考量" in html.text)
        check("admin dashboard has key identity column", "用户/Key" in html.text)
        check("admin dashboard has no metric crescent", "metric::after" not in html.text)
        check("admin dashboard refresh reconnects stream", "connectStream({ force: true })" in html.text)
        check("admin dashboard revives after background", "visibilitychange" in html.text)
        check("admin dashboard shows refresh animation", "is-loading" in html.text and "stream warn" in html.text)
        check("admin dashboard follows processing requests",
              "scheduleRequestFollowUp" in html.text and "setInterval(loadRequests, 5000)" in html.text)

        stream = client.get("/admin/logs/stream?once=1")
        check("admin logs stream ready", "event: ready" in stream.text, stream.text[:80])
        check("admin logs stream request event", "event: request" in stream.text, stream.text[:200])

        engine_health = client.get("/engine/healthz")
        check("engine healthz 200", engine_health.status_code == 200, str(engine_health.status_code))
        check("engine healthz mode", engine_health.json().get("mode") == "codexcont-engine",
              engine_health.text)
        engine_summary = client.post("/engine/v1/responses/analyze", json={
            "model": "gpt-5.5",
            "rounds": [
                {"round": 1, "reasoning_tokens": 516, "decision": "continue"},
                {"round": 2, "reasoning_tokens": 181, "decision": "clean"},
            ],
        })
        body = engine_summary.json()
        check("engine analyze 200", engine_summary.status_code == 200, engine_summary.text)
        check("engine analyze auto continued", body.get("protection") == "auto_continued", str(body))
        check("engine analyze first hit", body.get("first_truncation_round") == 1, str(body))


def test_engine_summary_projection():
    clean = summarize_engine_payload({
        "model": "gpt-5.5",
        "usage": {"output_tokens_details": {"reasoning_tokens": 140}},
    })
    check("engine summary clean", clean.get("protection") == "protected_clean", str(clean))
    check("engine summary latest tokens", clean.get("latest_reasoning_tokens") == 140, str(clean))

    risk = summarize_engine_payload({
        "model": "gpt-5.5",
        "reasoning_tokens": 516,
        "stopped_reason": "no_encrypted_content",
    })
    check("engine summary risk", risk.get("protection") == "risk_uncontinued", str(risk))
    check("engine summary truncation n", risk.get("first_truncation_n") == 1, str(risk))

    failed = summarize_engine_payload({
        "model": "gpt-5.5",
        "failure_reason": "Authorization: Bearer secret-token",
    })
    check("engine summary failed", failed.get("protection") == "failed", str(failed))
    check("engine summary redacts failure",
          "secret-token" not in json.dumps(failed), str(failed))


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


# --- runner -----------------------------------------------------------------


async def _main():
    test_truncation_math()
    await test_sse_framing()
    await test_fold_real_captures()
    await test_truncated_tool_call_discarded()
    await test_commentary_continuation_payload()
    await test_tool_pair_continuation_payload()
    await test_forward_marker_emits_downstream()
    test_header_transparency()
    test_zstd_request_body_decode()
    test_diagnostics_ring_and_redaction()
    test_diagnostics_request_summaries()
    await test_diagnostics_subscriber_broadcast()
    test_admin_routes_smoke()
    test_engine_summary_projection()
    test_upstream_url_resolution()
    test_auth_safety_guard()
    test_auth_injection()
    test_reasoning_gate()
    test_stateful_repair()
    await test_eof_incomplete()


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
