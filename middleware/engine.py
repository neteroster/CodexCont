"""Internal CodexCont engine API.

The engine endpoints are deliberately narrower than the public proxy path. They
return only safe protection summaries that a CPA plugin can persist or display.
"""
from __future__ import annotations

import json
import time
from dataclasses import asdict, dataclass
from typing import Any

from starlette.requests import Request
from starlette.responses import JSONResponse

from .codex import is_truncation_pattern, tier_n
from .diagnostics import redact_value


STARTED_AT = time.time()


@dataclass(frozen=True)
class EngineRoundSummary:
    round: int
    reasoning_tokens: int | None = None
    truncation_match: bool = False
    truncation_n: int | None = None
    decision: str = "unknown"


def _int_or_none(value: Any) -> int | None:
    try:
        if value is None or value == "":
            return None
        return int(value)
    except (TypeError, ValueError):
        return None


def _string(value: Any, default: str = "") -> str:
    text = str(value or "").strip()
    return text or default


def _extract_rounds(payload: dict[str, Any]) -> list[EngineRoundSummary]:
    raw_rounds = payload.get("rounds")
    rounds: list[EngineRoundSummary] = []
    if isinstance(raw_rounds, list):
        for idx, item in enumerate(raw_rounds, start=1):
            if not isinstance(item, dict):
                continue
            round_no = _int_or_none(item.get("round") or item.get("round_no")) or idx
            tokens = _int_or_none(
                item.get("reasoning_tokens")
                or item.get("reasoningTokens")
                or item.get("output_tokens_details", {}).get("reasoning_tokens")
            )
            trunc = bool(item.get("truncation_match") or is_truncation_pattern(tokens))
            rounds.append(
                EngineRoundSummary(
                    round=round_no,
                    reasoning_tokens=tokens,
                    truncation_match=trunc,
                    truncation_n=tier_n(tokens) if trunc else None,
                    decision=_string(item.get("decision"), "continue" if trunc else "clean"),
                )
            )
    if not rounds:
        tokens = _int_or_none(
            payload.get("reasoning_tokens")
            or payload.get("reasoningTokens")
            or payload.get("usage", {})
            .get("output_tokens_details", {})
            .get("reasoning_tokens")
        )
        trunc = bool(payload.get("truncation_match") or is_truncation_pattern(tokens))
        rounds.append(
            EngineRoundSummary(
                round=1,
                reasoning_tokens=tokens,
                truncation_match=trunc,
                truncation_n=tier_n(tokens) if trunc else None,
                decision=_string(payload.get("decision"), "continue" if trunc else "clean"),
            )
        )
    return rounds


def summarize_engine_payload(payload: dict[str, Any]) -> dict[str, Any]:
    """Project a safe CodexCont protection summary from engine input.

    This is intentionally tolerant. The CPA plugin can send already-known round
    summaries, while tests and smoke probes can send only a usage object.
    """
    rounds = _extract_rounds(payload)
    latest = rounds[-1] if rounds else EngineRoundSummary(round=1)
    first_hit = next((item for item in rounds if item.truncation_match), None)
    continuation_count = max(0, int(payload.get("continuation_count") or 0))
    if continuation_count == 0 and len(rounds) > 1:
        continuation_count = len(rounds) - 1

    failure = _string(payload.get("failure_reason") or payload.get("failure"))
    stopped_reason = _string(payload.get("stopped_reason") or payload.get("stop_reason"))
    folded = bool(payload.get("folded") or continuation_count > 0 or len(rounds) > 1)
    passthrough = bool(payload.get("passthrough"))

    if failure:
        protection = "failed"
    elif passthrough:
        protection = "passthrough"
    elif continuation_count > 0:
        protection = "auto_continued"
    elif first_hit is not None:
        protection = "risk_uncontinued"
    else:
        protection = "protected_clean"

    summary = {
        "ok": True,
        "request_id": _string(payload.get("request_id")),
        "model": _string(payload.get("model")),
        "protection": protection,
        "folded": folded,
        "passthrough": passthrough,
        "rounds": [asdict(item) for item in rounds],
        "latest_round": latest.round,
        "latest_reasoning_tokens": latest.reasoning_tokens,
        "first_truncation_round": first_hit.round if first_hit else None,
        "first_truncation_reasoning_tokens": first_hit.reasoning_tokens if first_hit else None,
        "first_truncation_n": first_hit.truncation_n if first_hit else None,
        "continuation_count": continuation_count,
        "stopped_reason": stopped_reason or None,
        "failure_reason": failure or None,
        "safe": True,
    }
    return redact_value(summary)


async def engine_healthz(request: Request) -> JSONResponse:
    _ = request
    return JSONResponse(
        {
            "ok": True,
            "mode": "codexcont-engine",
            "uptime_seconds": round(time.time() - STARTED_AT, 3),
        }
    )


async def engine_analyze(request: Request) -> JSONResponse:
    raw = await request.body()
    try:
        body = json.loads(raw or b"{}")
    except (json.JSONDecodeError, UnicodeDecodeError):
        return JSONResponse({"ok": False, "error": "invalid_json_body"}, status_code=400)
    if not isinstance(body, dict):
        return JSONResponse({"ok": False, "error": "body_must_be_object"}, status_code=400)
    return JSONResponse(summarize_engine_payload(body))
