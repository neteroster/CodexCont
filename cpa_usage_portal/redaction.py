"""Safe projections for user-facing usage data."""
from __future__ import annotations

import json
import re
from typing import Any

from .security import hash_preview, normalize_key_hash

SECRET_KEYWORDS = (
    "authorization",
    "api_key",
    "apikey",
    "access_token",
    "refresh_token",
    "id_token",
    "cookie",
    "set-cookie",
    "oauth",
    "secret",
    "encrypted_content",
    "management_key",
    "cpamp",
)

_BEARER_RE = re.compile(r"(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+")
_KEY_RE = re.compile(
    r"(?i)\b(api[_-]?key|access[_-]?token|refresh[_-]?token|authorization|cookie|secret)"
    r"\s*[:=]\s*['\"]?[^'\"\s,;]+"
)
_SPACE_RE = re.compile(r"\s+")
_HEADER_BLOB_MARKERS = (
    "cf-cache-status",
    "set-cookie",
    "strict-transport-security",
    "cross-origin-opener-policy",
    "x-codex-",
    "x-openai-",
    "report-to",
)
MAX_FAILURE_BRIEF = 120
MAX_FAILURE_DETAIL = 600


def redact(value: Any, *, key: str = "") -> Any:
    key_l = key.lower()
    if any(part in key_l for part in SECRET_KEYWORDS):
        return "[REDACTED]"
    if isinstance(value, str):
        text = _BEARER_RE.sub("Bearer [REDACTED]", value)
        return _KEY_RE.sub(lambda m: f"{m.group(1)}=[REDACTED]", text)
    if isinstance(value, dict):
        return {str(k): redact(v, key=str(k)) for k, v in value.items()}
    if isinstance(value, list):
        return [redact(item) for item in value]
    if isinstance(value, (int, float, bool)) or value is None:
        return value
    return str(value)


def _as_text(value: Any) -> str:
    if value is None or value == "":
        return ""
    redacted = redact(value)
    if isinstance(redacted, str):
        return redacted
    try:
        return json.dumps(redacted, ensure_ascii=False, sort_keys=True)
    except TypeError:
        return str(redacted)


def _compact(value: str) -> str:
    return _SPACE_RE.sub(" ", value).strip()


def _truncate(value: str, limit: int) -> str:
    text = _compact(value)
    if len(text) <= limit:
        return text
    if limit <= 3:
        return "." * max(0, limit)
    return text[: max(0, limit - 3)].rstrip() + "..."


def _looks_like_response_headers(value: str) -> bool:
    lower = value.lower()
    return sum(1 for marker in _HEADER_BLOB_MARKERS if marker in lower) >= 2


def _failure_projection(raw: Any, *, failed: bool, status_code: Any) -> tuple[str, str]:
    text = _as_text(raw)
    if not text:
        if failed and status_code:
            return f"HTTP {status_code}", f"HTTP {status_code}"
        return "", ""
    if _looks_like_response_headers(text):
        if not failed:
            return "", ""
        text = "Upstream response headers omitted; inspect status code and quota fields."
    detail = _truncate(text, MAX_FAILURE_DETAIL)
    brief_source = detail.split("|", 1)[0].split("\n", 1)[0]
    brief = _truncate(brief_source, MAX_FAILURE_BRIEF)
    if not brief and failed and status_code:
        brief = f"HTTP {status_code}"
    return brief, detail


def safe_event(event: dict[str, Any], *, expected_hash: str) -> dict[str, Any] | None:
    api_key_hash = str(event.get("api_key_hash") or "").strip()
    try:
        normalized = normalize_key_hash(api_key_hash)
        expected = normalize_key_hash(expected_hash)
    except ValueError:
        return None
    if normalized != expected:
        return None

    status_code = event.get("fail_status_code")
    failed = bool(event.get("failed"))
    failure_brief, failure_detail = _failure_projection(
        event.get("fail_summary") or "",
        failed=failed,
        status_code=status_code,
    )
    return {
        "request_id": event.get("request_id") or "",
        "event_hash": event.get("event_hash") or "",
        "timestamp_ms": event.get("timestamp_ms") or 0,
        "model": event.get("resolved_model") or event.get("model") or "",
        "requested_model": event.get("model") or "",
        "endpoint": event.get("endpoint") or event.get("path") or "",
        "status": "failed" if failed else "success",
        "failed": failed,
        "status_code": status_code,
        "latency_ms": event.get("latency_ms"),
        "ttft_ms": event.get("ttft_ms"),
        "input_tokens": event.get("input_tokens") or 0,
        "output_tokens": event.get("output_tokens") or 0,
        "cached_tokens": event.get("cached_tokens") or 0,
        "cache_read_tokens": event.get("cache_read_tokens") or 0,
        "cache_creation_tokens": event.get("cache_creation_tokens") or 0,
        "reasoning_tokens": event.get("reasoning_tokens") or 0,
        "total_tokens": event.get("total_tokens") or 0,
        "cost": event.get("cost") or 0,
        "service_tier": event.get("service_tier") or "",
        "reasoning_effort": event.get("reasoning_effort") or "",
        "api_key_preview": hash_preview(expected),
        "failure_brief": failure_brief,
        "failure": failure_detail,
        "quota": {
            "used_percent": event.get("header_quota_used_percent"),
            "recover_at_ms": event.get("header_quota_recover_at_ms"),
            "plan": event.get("header_quota_plan_type") or "",
            "error_kind": event.get("header_error_kind") or "",
            "error_code": event.get("header_error_code") or "",
        },
    }


def safe_events(events: list[dict[str, Any]], *, expected_hash: str) -> list[dict[str, Any]]:
    projected: list[dict[str, Any]] = []
    for event in events:
        if isinstance(event, dict):
            safe = safe_event(event, expected_hash=expected_hash)
            if safe is not None:
                projected.append(safe)
    return projected


_SAFE_STAT_FIELDS = {
    "calls",
    "requests",
    "total_calls",
    "success_calls",
    "failure_calls",
    "success_rate",
    "input_tokens",
    "output_tokens",
    "cached_tokens",
    "cache_read_tokens",
    "cache_creation_tokens",
    "reasoning_tokens",
    "total_tokens",
    "cost",
    "total_cost",
    "latency_ms",
    "avg_latency_ms",
    "ttft_ms",
    "avg_ttft_ms",
}


def safe_api_key_stats(stats: list[dict[str, Any]], *, expected_hash: str) -> list[dict[str, Any]]:
    try:
        expected = normalize_key_hash(expected_hash)
    except ValueError:
        return []
    projected: list[dict[str, Any]] = []
    for stat in stats:
        if not isinstance(stat, dict):
            continue
        row_hash = str(stat.get("api_key_hash") or "").strip()
        if row_hash:
            try:
                if normalize_key_hash(row_hash) != expected:
                    continue
            except ValueError:
                continue
        safe = {key: stat.get(key) for key in _SAFE_STAT_FIELDS if key in stat}
        safe["api_key_preview"] = hash_preview(expected)
        projected.append(safe)
    return projected
