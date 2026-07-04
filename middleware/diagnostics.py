"""In-process diagnostics for the CodexCont admin dashboard."""
from __future__ import annotations

import asyncio
from copy import deepcopy
import re
import threading
import time
import uuid
from collections import deque
from datetime import UTC, datetime
from typing import Any

_SECRET_KEYWORDS = (
    "authorization",
    "api-key",
    "api_key",
    "apikey",
    "access_token",
    "refresh_token",
    "id_token",
    "tunnel_token",
    "bearer_token",
    "oauth",
    "secret",
    "encrypted_content",
)
_BEARER_RE = re.compile(r"(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+")
_KEY_VALUE_RE = re.compile(
    r"(?i)\b(api[_-]?key|access[_-]?token|refresh[_-]?token|authorization|secret)"
    r"\s*[:=]\s*['\"]?[^'\"\s,;]+"
)


def utc_now_iso() -> str:
    return datetime.now(UTC).isoformat().replace("+00:00", "Z")


def redact_value(value: Any, *, key: str = "") -> Any:
    """Return a JSON-safe value with obvious secrets removed."""
    key_l = key.lower()
    if key_l == "token" or key_l.endswith("_token") or any(part in key_l for part in _SECRET_KEYWORDS):
        return "[REDACTED]"
    if isinstance(value, str):
        text = _BEARER_RE.sub("Bearer [REDACTED]", value)
        return _KEY_VALUE_RE.sub(lambda m: f"{m.group(1)}=[REDACTED]", text)
    if isinstance(value, dict):
        return {str(k): redact_value(v, key=str(k)) for k, v in value.items()}
    if isinstance(value, (list, tuple)):
        return [redact_value(v) for v in value]
    if isinstance(value, (int, float, bool)) or value is None:
        return value
    return str(value)


_RISK_STOP_REASONS = {
    "no_encrypted_content",
    "max_continue",
    "max_total_output_tokens",
    "tier_out_of_window",
}


def _public_request_summary(summary: dict[str, Any]) -> dict[str, Any]:
    public = deepcopy(summary)
    public.pop("_started_perf", None)
    return redact_value(public)


class Diagnostics:
    """Small memory-only metrics and event hub.

    This intentionally does not write to disk. It is safe for the small SJC VPS
    and simple enough to keep CodexCont independent from CPA internals.
    """

    def __init__(self, *, max_events: int = 800, max_requests: int = 200) -> None:
        self.max_events = max(1, int(max_events))
        self.max_requests = max(1, int(max_requests))
        self._events: deque[dict[str, Any]] = deque(maxlen=self.max_events)
        self._subscribers: set[asyncio.Queue[dict[str, Any]]] = set()
        self._request_subscribers: set[asyncio.Queue[dict[str, Any]]] = set()
        self._lock = threading.RLock()
        self._seq = 0
        self._started_wall = utc_now_iso()
        self._started_perf = time.monotonic()
        self._active_ids: set[str] = set()
        self._counters: dict[str, int] = {
            "total_requests": 0,
            "active_requests": 0,
            "folded_requests": 0,
            "passthrough_requests": 0,
            "continuations": 0,
            "truncation_hits": 0,
            "failures": 0,
        }
        self._last_request_at: str | None = None
        self._last_continuation_at: str | None = None
        self._last_error_at: str | None = None
        self._last_error: dict[str, Any] | None = None
        self._request_meta: dict[str, dict[str, Any]] = {}
        self._request_summaries: dict[str, dict[str, Any]] = {}

    def recent(self, *, limit: int | None = None) -> list[dict[str, Any]]:
        with self._lock:
            events = list(self._events)
        if limit is None:
            return events
        limit = max(0, min(int(limit), self.max_events))
        return events[-limit:]

    def subscribe(self) -> asyncio.Queue[dict[str, Any]]:
        queue: asyncio.Queue[dict[str, Any]] = asyncio.Queue(maxsize=200)
        with self._lock:
            self._subscribers.add(queue)
        return queue

    def unsubscribe(self, queue: asyncio.Queue[dict[str, Any]]) -> None:
        with self._lock:
            self._subscribers.discard(queue)

    def subscribe_requests(self) -> asyncio.Queue[dict[str, Any]]:
        queue: asyncio.Queue[dict[str, Any]] = asyncio.Queue(maxsize=200)
        with self._lock:
            self._request_subscribers.add(queue)
        return queue

    def unsubscribe_requests(self, queue: asyncio.Queue[dict[str, Any]]) -> None:
        with self._lock:
            self._request_subscribers.discard(queue)

    def recent_requests(self, *, limit: int | None = None) -> list[dict[str, Any]]:
        with self._lock:
            summaries = [_public_request_summary(item) for item in self._request_summaries.values()]
        if limit is None:
            return summaries
        limit = max(0, min(int(limit), self.max_requests))
        return summaries[-limit:]

    def record(self, level: str, event: str, message: str = "", **fields: Any) -> dict[str, Any]:
        item = {
            "seq": 0,
            "ts": utc_now_iso(),
            "level": (level or "info").lower(),
            "event": event,
            "message": redact_value(message),
            "fields": redact_value(fields),
        }
        with self._lock:
            self._seq += 1
            item["seq"] = self._seq
            self._events.append(item)
            subscribers = list(self._subscribers)

        for queue in subscribers:
            try:
                queue.put_nowait(item)
            except asyncio.QueueFull:
                try:
                    queue.get_nowait()
                except asyncio.QueueEmpty:
                    pass
                try:
                    queue.put_nowait(item)
                except asyncio.QueueFull:
                    pass
        return item

    def _trim_requests_locked(self) -> None:
        while len(self._request_summaries) > self.max_requests:
            removed = False
            for request_id in list(self._request_summaries.keys()):
                if request_id not in self._active_ids:
                    self._request_summaries.pop(request_id, None)
                    removed = True
                    break
            if not removed:
                break

    def _request_copy_locked(self, request_id: str) -> dict[str, Any] | None:
        summary = self._request_summaries.get(request_id)
        if summary is None:
            return None
        return _public_request_summary(summary)

    def _publish_request(self, summary: dict[str, Any] | None) -> None:
        if summary is None:
            return
        with self._lock:
            subscribers = list(self._request_subscribers)
        for queue in subscribers:
            try:
                queue.put_nowait(summary)
            except asyncio.QueueFull:
                try:
                    queue.get_nowait()
                except asyncio.QueueEmpty:
                    pass
                try:
                    queue.put_nowait(summary)
                except asyncio.QueueFull:
                    pass

    def _set_request_result_locked(self, summary: dict[str, Any]) -> None:
        if summary.get("status") == "failed":
            summary["protection"] = "failed"
            return
        if summary.get("status") == "incomplete":
            summary["protection"] = "incomplete"
            return
        if summary.get("passthrough"):
            summary["protection"] = "passthrough"
            return
        if summary.get("continuation_count", 0) > 0:
            summary["protection"] = "auto_continued"
            return
        if summary.get("truncation_match") or summary.get("stopped_reason") in _RISK_STOP_REASONS:
            summary["protection"] = "risk_uncontinued"
            return
        if summary.get("folded"):
            summary["protection"] = "protected_clean"
            return
        summary["protection"] = "processing"

    def _finish_request_locked(
        self,
        request_id: str,
        *,
        status: str,
        stopped_reason: str | None = None,
        failure_reason: str | None = None,
        failure_detail: Any = None,
    ) -> dict[str, Any] | None:
        now = utc_now_iso()
        summary = self._request_summaries.get(request_id)
        if summary is None:
            return None
        summary["updated_at"] = now
        summary["ended_at"] = now
        started_perf = summary.pop("_started_perf", None)
        if isinstance(started_perf, (int, float)):
            summary["duration_ms"] = round((time.monotonic() - started_perf) * 1000)
        summary["final_status"] = status
        summary["stopped_reason"] = stopped_reason
        if failure_reason is not None:
            summary["status"] = "failed"
            summary["failure_reason"] = failure_reason
            summary["failure_detail"] = redact_value(failure_detail)
        elif status == "incomplete" or status == "closed":
            summary["status"] = "incomplete"
        else:
            summary["status"] = "completed"
        self._set_request_result_locked(summary)
        return _public_request_summary(summary)

    def request_started(
        self,
        *,
        path: str,
        model: str | None = None,
        key_identity: dict[str, Any] | None = None,
    ) -> str:
        request_id = uuid.uuid4().hex[:12]
        now = utc_now_iso()
        perf = time.monotonic()
        safe_identity = redact_value(key_identity or {
            "known": False,
            "source": "unset",
            "name": "未识别 Key",
            "preview": "",
        })
        with self._lock:
            self._active_ids.add(request_id)
            self._counters["total_requests"] += 1
            self._counters["active_requests"] = len(self._active_ids)
            self._last_request_at = now
            self._request_meta[request_id] = {
                "request_id": request_id,
                "path": path,
                "model": model,
                "key_identity": safe_identity,
                "started_at": now,
            }
            self._request_summaries[request_id] = {
                "request_id": request_id,
                "model": model,
                "path": path,
                "key_identity": safe_identity,
                "started_at": now,
                "updated_at": now,
                "ended_at": None,
                "duration_ms": None,
                "status": "processing",
                "protection": "processing",
                "folded": False,
                "passthrough": False,
                "passthrough_reason": None,
                "rounds": [],
                "latest_round": None,
                "latest_reasoning_tokens": None,
                "first_truncation_round": None,
                "first_truncation_reasoning_tokens": None,
                "first_truncation_n": None,
                "first_truncation_decision": None,
                "continuation_count": 0,
                "truncation_match": False,
                "final_status": None,
                "stopped_reason": None,
                "failure_reason": None,
                "failure_detail": None,
                "_started_perf": perf,
            }
            self._trim_requests_locked()
            summary = self._request_copy_locked(request_id)
        self._publish_request(summary)
        self.record("info", "request_started", "Responses request received",
                    request_id=request_id, path=path, model=model, key_identity=safe_identity)
        return request_id

    def request_update(self, request_id: str, **fields: Any) -> None:
        summary = None
        with self._lock:
            meta = self._request_meta.get(request_id)
            if meta is not None:
                meta.update({k: v for k, v in fields.items() if v is not None})
            req = self._request_summaries.get(request_id)
            if req is not None:
                safe_updates = {k: v for k, v in fields.items() if k in {"model", "path"} and v is not None}
                if safe_updates:
                    req.update(safe_updates)
                    req["updated_at"] = utc_now_iso()
                    summary = self._request_copy_locked(request_id)
        self._publish_request(summary)

    def mark_fold_start(self, request_id: str, *, model: Any, path: str, upstream_url: str) -> None:
        summary = None
        with self._lock:
            self._counters["folded_requests"] += 1
            req = self._request_summaries.get(request_id)
            if req is not None:
                req["folded"] = True
                req["model"] = model
                req["path"] = path
                req["updated_at"] = utc_now_iso()
                req["protection"] = "processing"
                summary = self._request_copy_locked(request_id)
        self._publish_request(summary)
        self.record("info", "fold_start", "Folded Responses stream started",
                    request_id=request_id, model=model, path=path, upstream_url=upstream_url)

    def mark_passthrough(self, request_id: str, *, reason: str, model: Any) -> None:
        summary = None
        with self._lock:
            self._counters["passthrough_requests"] += 1
            req = self._request_summaries.get(request_id)
            if req is not None:
                req["passthrough"] = True
                req["passthrough_reason"] = reason
                req["model"] = model
                req["updated_at"] = utc_now_iso()
                req["protection"] = "passthrough"
                summary = self._request_copy_locked(request_id)
        self._publish_request(summary)
        self.record("info", "passthrough", "Request passed through without folding",
                    request_id=request_id, reason=reason, model=model)

    def round_decision(
        self,
        request_id: str,
        *,
        round_no: int,
        reasoning_tokens: int | None,
        n: int | None,
        decision: str,
        buffered: list[str],
        truncation_match: bool,
    ) -> None:
        summary = None
        if truncation_match:
            with self._lock:
                self._counters["truncation_hits"] += 1
        with self._lock:
            req = self._request_summaries.get(request_id)
            if req is not None:
                round_summary = {
                    "round": round_no,
                    "reasoning_tokens": reasoning_tokens,
                    "n": n,
                    "decision": decision,
                    "buffered": list(buffered),
                    "truncation_match": truncation_match,
                }
                req["rounds"].append(round_summary)
                req["latest_round"] = round_no
                req["latest_reasoning_tokens"] = reasoning_tokens
                if truncation_match and req.get("first_truncation_round") is None:
                    req["first_truncation_round"] = round_no
                    req["first_truncation_reasoning_tokens"] = reasoning_tokens
                    req["first_truncation_n"] = n
                    req["first_truncation_decision"] = decision
                req["truncation_match"] = bool(req.get("truncation_match") or truncation_match)
                req["updated_at"] = utc_now_iso()
                if truncation_match and decision != "continue" and req.get("continuation_count", 0) == 0:
                    req["protection"] = "risk_uncontinued"
                    req["stopped_reason"] = decision
                summary = self._request_copy_locked(request_id)
        self._publish_request(summary)
        self.record(
            "info",
            "round_decision",
            "Round finished and continuation decision was made",
            request_id=request_id,
            round=round_no,
            reasoning_tokens=reasoning_tokens,
            n=n,
            decision=decision,
            buffered=buffered,
            truncation_match=truncation_match,
        )

    def continuation_opened(self, request_id: str, *, from_round: int, next_round: int, method: str) -> None:
        now = utc_now_iso()
        summary = None
        with self._lock:
            self._counters["continuations"] += 1
            self._last_continuation_at = now
            req = self._request_summaries.get(request_id)
            if req is not None:
                req["continuation_count"] = int(req.get("continuation_count") or 0) + 1
                req["updated_at"] = now
                req["protection"] = "auto_continued"
                summary = self._request_copy_locked(request_id)
        self._publish_request(summary)
        self.record("info", "continuation_opened", "Opened hidden continuation round",
                    request_id=request_id, from_round=from_round, next_round=next_round, method=method)

    def request_finished(self, request_id: str, *, status: str, stopped_reason: str | None = None) -> None:
        summary = None
        with self._lock:
            self._active_ids.discard(request_id)
            self._counters["active_requests"] = len(self._active_ids)
            self._request_meta.pop(request_id, None)
            summary = self._finish_request_locked(
                request_id, status=status, stopped_reason=stopped_reason
            )
            self._trim_requests_locked()
        self._publish_request(summary)
        self.record("info", "request_finished", "Responses request finished",
                    request_id=request_id, status=status, stopped_reason=stopped_reason)

    def request_failed(self, request_id: str, *, reason: str, detail: Any = None) -> None:
        now = utc_now_iso()
        error = {"request_id": request_id, "reason": reason, "detail": redact_value(detail)}
        summary = None
        with self._lock:
            self._active_ids.discard(request_id)
            self._counters["active_requests"] = len(self._active_ids)
            self._counters["failures"] += 1
            self._last_error_at = now
            self._last_error = error
            self._request_meta.pop(request_id, None)
            summary = self._finish_request_locked(
                request_id,
                status="failed",
                failure_reason=reason,
                failure_detail=detail,
            )
            self._trim_requests_locked()
        self._publish_request(summary)
        self.record("warning", "request_failed", "Responses request failed", **error)

    def health(self) -> dict[str, Any]:
        return {
            "ok": True,
            "started_at": self._started_wall,
            "uptime_seconds": round(time.monotonic() - self._started_perf, 3),
        }

    def snapshot(
        self,
        *,
        upstream: dict[str, Any] | None = None,
        config: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        with self._lock:
            counters = dict(self._counters)
            active = list(self._request_meta.values())
            last_error = dict(self._last_error) if self._last_error else None
            last_request_at = self._last_request_at
            last_continuation_at = self._last_continuation_at
            last_error_at = self._last_error_at
        return {
            **self.health(),
            "counters": counters,
            "active_requests": active,
            "recent_requests": self.recent_requests(limit=10),
            "last_request_at": last_request_at,
            "last_continuation_at": last_continuation_at,
            "last_error_at": last_error_at,
            "last_error": last_error,
            "upstream": upstream or {"ok": None, "status": "not_checked"},
            "config": config or {},
        }
