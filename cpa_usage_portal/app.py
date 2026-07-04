"""Starlette app for the self-service CPA usage portal."""
from __future__ import annotations

import asyncio
import contextlib
import json
import math
from pathlib import Path
from typing import Any

import httpx
from starlette.applications import Starlette
from starlette.requests import Request
from starlette.responses import HTMLResponse, JSONResponse, Response, StreamingResponse
from starlette.routing import Route

from .config import PortalConfig
from .cpamp import CPAMPClient, SUPPORTED_RANGES, range_window
from .key_policy import KeyPolicyState, KeyRecord
from .pricing import apply_event_pricing, apply_key_policy_pricing
from .quota_state import QuotaState, RESET_WINDOWS
from .redaction import safe_api_key_stats, safe_events
from .security import sha256_hex, sign_session, verify_session

_STATIC = Path(__file__).with_name("static")
_DASHBOARD = _STATIC / "dashboard.html"
_ADMIN_DASHBOARD = _STATIC / "admin.html"


class AuthError(Exception):
    pass


def _json_error(message: str, status_code: int) -> JSONResponse:
    return JSONResponse({"error": message}, status_code=status_code)


def _load_key_state(request: Request) -> KeyPolicyState:
    cfg: PortalConfig = request.app.state.cfg
    return KeyPolicyState.load(cfg.key_policy_state_path)


def _session_payload(request: Request) -> dict[str, Any]:
    cfg: PortalConfig = request.app.state.cfg
    token = request.cookies.get(cfg.session_cookie_name, "")
    payload = verify_session(token, cfg.session_secret)
    if payload is None:
        raise AuthError("not_authenticated")
    return payload


def _current_key(request: Request):
    payload = _session_payload(request)
    state = _load_key_state(request)
    record = state.get_by_raw_hash(str(payload.get("key_hash") or ""))
    if record is None or not record.enabled:
        raise AuthError("key_not_available")
    return record


def _quota(request: Request) -> QuotaState:
    return request.app.state.quota


def _record_id(record: KeyRecord) -> str:
    return record.policy_id or record.raw_key_hash


def _safe_record(record: KeyRecord, quota: QuotaState) -> dict[str, Any]:
    safe = record.safe_dict()
    local_limits = quota.get_limits(_record_id(record))
    limits = dict(safe.get("limits") or {})
    limits.update(local_limits.safe_dict())
    safe["limits"] = limits
    safe["reset_points"] = quota.get_reset_points(_record_id(record))
    return safe


def _safe_key_summary(record: KeyRecord) -> dict[str, Any]:
    safe = record.safe_dict()
    return {
        "id": safe.get("id") or _record_id(record),
        "name": safe.get("name") or "",
        "preview": safe.get("preview") or "",
        "enabled": bool(safe.get("enabled")),
    }


def _find_record(state: KeyPolicyState, key_id: str) -> KeyRecord | None:
    for record in state.keys:
        if _record_id(record) == key_id:
            return record
    return None


def _parse_before(request: Request) -> tuple[int | None, int | None]:
    before_ms = request.query_params.get("before_ms")
    before_id = request.query_params.get("before_id")
    if request.query_params.get("before") and (not before_ms and not before_id):
        parts = request.query_params["before"].split(":", 1)
        before_ms = parts[0] if parts else None
        before_id = parts[1] if len(parts) > 1 else None
    try:
        parsed_ms = int(before_ms) if before_ms else None
    except ValueError:
        parsed_ms = None
    try:
        parsed_id = int(before_id) if before_id else None
    except ValueError:
        parsed_id = None
    return parsed_ms, parsed_id


def _parse_range(request: Request, *, default: str) -> str:
    value = request.query_params.get("range", default)
    return value if value in SUPPORTED_RANGES else default


def _parse_float_limit(value: Any) -> float | None:
    if value is None or value == "":
        return None
    try:
        parsed = float(value)
    except (TypeError, ValueError):
        raise ValueError("invalid_limit")
    if parsed < 0 or not math.isfinite(parsed):
        raise ValueError("invalid_limit")
    return parsed


def _actor(request: Request) -> str:
    return (
        request.headers.get("cf-access-authenticated-user-email")
        or request.headers.get("x-usage-admin-actor")
        or "admin"
    )


def _admin_allowed(request: Request) -> bool:
    cfg: PortalConfig = request.app.state.cfg
    return request.headers.get(cfg.admin_header_name) == cfg.admin_header_value


def _admin_guard(request: Request) -> JSONResponse | None:
    if _admin_allowed(request):
        return None
    return _json_error("not_found", 404)


def _limit_for(record: KeyRecord, quota: QuotaState, range_name: str) -> float | None:
    local = quota.get_limits(_record_id(record))
    if range_name == "5h":
        return local.five_hour_usd
    if range_name == "24h":
        return record.daily_limit_usd
    if range_name == "7d":
        return record.weekly_limit_usd
    if range_name == "month":
        return local.monthly_usd
    return None


def _quota_projection(
    record: KeyRecord,
    quota: QuotaState,
    *,
    range_name: str,
    window_from_ms: int,
    window_to_ms: int,
    reset_at_ms: int | None,
    used_usd: Any,
) -> dict[str, Any]:
    limit = _limit_for(record, quota, range_name)
    used = _float(used_usd)
    remaining = None
    percent = None
    if limit is not None and limit > 0:
        remaining = max(limit - used, 0.0)
        percent = used / limit
    return {
        "range": range_name,
        "from_ms": window_from_ms,
        "to_ms": window_to_ms,
        "reset_at_ms": reset_at_ms,
        "limit_usd": limit,
        "used_usd": used,
        "remaining_usd": remaining,
        "used_percent": percent,
    }


def _float(value: Any) -> float:
    try:
        return float(value or 0)
    except (TypeError, ValueError):
        return 0.0


def _attach_event_accounting(
    events: list[dict[str, Any]],
    *,
    record: KeyRecord,
    quota: QuotaState,
    selected_range: str,
    selected_quota: dict[str, Any],
    now_ms_value: int,
) -> list[dict[str, Any]]:
    effective_windows: dict[str, tuple[int, int, int | None]] = {}
    key_id = _record_id(record)
    for name in RESET_WINDOWS:
        window, reset_at = quota.effective_window(key_id, name, now_ms_value=now_ms_value)
        effective_windows[name] = (window.from_ms, window.to_ms, reset_at)

    projected: list[dict[str, Any]] = []
    for event in events:
        row = dict(event)
        ts = int(row.get("timestamp_ms") or 0)
        included = [
            name
            for name, (from_ms, to_ms, _reset_at) in effective_windows.items()
            if ts and from_ms <= ts <= to_ms
        ]
        window_from, window_to, reset_at = effective_windows.get(selected_range, (0, 0, None))
        row["accounting"] = {
            "selected_range": selected_range,
            "included_windows": included,
            "window_from_ms": window_from,
            "window_to_ms": window_to,
            "reset_at_ms": reset_at,
            "current_window_remaining_usd": selected_quota.get("remaining_usd"),
            "current_window_limit_usd": selected_quota.get("limit_usd"),
        }
        projected.append(row)
    return projected


async def _events_for_record(
    request: Request,
    record: KeyRecord,
    *,
    range_name: str,
    limit: int,
) -> tuple[list[dict[str, Any]], dict[str, Any]]:
    quota_state = _quota(request)
    window, reset_at = quota_state.effective_window(_record_id(record), range_name)
    data = await request.app.state.cpamp.analytics(
        api_key_hash=record.cpamp_hash,
        window=window,
        include_events=True,
        include_model_stats=True,
        event_limit=limit,
    )
    data = apply_key_policy_pricing(data, record.model_prices)
    quota_summary = _quota_projection(
        record,
        quota_state,
        range_name=range_name,
        window_from_ms=window.from_ms,
        window_to_ms=window.to_ms,
        reset_at_ms=reset_at,
        used_usd=(data.get("summary") or {}).get("total_cost"),
    )
    page = data.get("events") or {}
    items = apply_event_pricing(
        safe_events(page.get("items") or [], expected_hash=record.cpamp_hash),
        record.model_prices,
    )
    items = _attach_event_accounting(
        items,
        record=record,
        quota=quota_state,
        selected_range=range_name,
        selected_quota=quota_summary,
        now_ms_value=window.to_ms,
    )
    key_summary = _safe_key_summary(record)
    for item in items:
        item["key"] = key_summary
    return items, {
        "range": range_name,
        "from_ms": window.from_ms,
        "to_ms": window.to_ms,
        "reset_at_ms": reset_at,
        "quota": quota_summary,
    }


async def dashboard(_request: Request) -> HTMLResponse:
    return HTMLResponse(_DASHBOARD.read_text(encoding="utf-8"))


async def healthz(request: Request) -> JSONResponse:
    cfg: PortalConfig = request.app.state.cfg
    state_ok = Path(cfg.key_policy_state_path).exists()
    cpamp_ok: bool | None = None
    try:
        await request.app.state.cpamp.health()
        cpamp_ok = True
    except Exception:
        cpamp_ok = False
    return JSONResponse({"ok": state_ok and bool(cpamp_ok), "key_policy_state": state_ok, "cpamp": cpamp_ok})


async def create_session(request: Request) -> JSONResponse:
    try:
        body = await request.json()
    except json.JSONDecodeError:
        return _json_error("invalid_json", 400)
    api_key = str((body or {}).get("api_key") or "").strip()
    if not api_key:
        return _json_error("api_key_required", 400)

    key_hash = sha256_hex(api_key)
    state = _load_key_state(request)
    record = state.get_by_raw_hash(key_hash)
    if record is None:
        return _json_error("invalid_api_key", 401)
    if not record.enabled:
        return _json_error("api_key_disabled", 403)

    cfg: PortalConfig = request.app.state.cfg
    token = sign_session(
        {"key_hash": record.raw_key_hash, "cpamp_hash": record.cpamp_hash, "key_name": record.name},
        cfg.session_secret,
        ttl_seconds=cfg.session_ttl_seconds,
    )
    response = JSONResponse({"me": _safe_record(record, _quota(request))})
    response.set_cookie(
        cfg.session_cookie_name,
        token,
        max_age=cfg.session_ttl_seconds,
        httponly=True,
        secure=cfg.cookie_secure,
        samesite="lax",
        path="/",
    )
    return response


async def delete_session(request: Request) -> JSONResponse:
    cfg: PortalConfig = request.app.state.cfg
    response = JSONResponse({"ok": True})
    response.delete_cookie(cfg.session_cookie_name, path="/")
    return response


async def me(request: Request) -> JSONResponse:
    try:
        record = _current_key(request)
    except AuthError as exc:
        return _json_error(str(exc), 401)
    return JSONResponse({"me": _safe_record(record, _quota(request))})


async def usage(request: Request) -> JSONResponse:
    try:
        record = _current_key(request)
    except AuthError as exc:
        return _json_error(str(exc), 401)
    range_name = _parse_range(request, default="24h")
    quota_state = _quota(request)
    window, reset_at = quota_state.effective_window(_record_id(record), range_name)
    data = await request.app.state.cpamp.analytics(
        api_key_hash=record.cpamp_hash,
        window=window,
        include_events=False,
        include_model_stats=True,
    )
    data = apply_key_policy_pricing(data, record.model_prices)
    quota_summary = _quota_projection(
        record,
        quota_state,
        range_name=range_name,
        window_from_ms=window.from_ms,
        window_to_ms=window.to_ms,
        reset_at_ms=reset_at,
        used_usd=(data.get("summary") or {}).get("total_cost"),
    )
    return JSONResponse({
        "range": range_name,
        "from_ms": window.from_ms,
        "to_ms": window.to_ms,
        "reset_at_ms": reset_at,
        "limits": _safe_record(record, quota_state).get("limits") or {},
        "reset_points": quota_state.get_reset_points(_record_id(record)),
        "quota": quota_summary,
        "summary": data.get("summary") or {},
        "timeline": data.get("timeline") or [],
        "model_share": data.get("model_share") or [],
        "model_stats": data.get("model_stats") or [],
        "api_key_stats": safe_api_key_stats(data.get("api_key_stats") or [], expected_hash=record.cpamp_hash),
    })


async def events(request: Request) -> JSONResponse:
    try:
        record = _current_key(request)
    except AuthError as exc:
        return _json_error(str(exc), 401)
    limit_raw = request.query_params.get("limit", "100")
    try:
        limit = max(1, min(int(limit_raw), 200))
    except ValueError:
        limit = 100
    before_ms, before_id = _parse_before(request)
    range_name = _parse_range(request, default="7d")
    quota_state = _quota(request)
    window, reset_at = quota_state.effective_window(_record_id(record), range_name)
    data = await request.app.state.cpamp.analytics(
        api_key_hash=record.cpamp_hash,
        window=window,
        include_events=True,
        include_model_stats=True,
        event_limit=limit,
        before_ms=before_ms,
        before_id=before_id,
    )
    data = apply_key_policy_pricing(data, record.model_prices)
    quota_summary = _quota_projection(
        record,
        quota_state,
        range_name=range_name,
        window_from_ms=window.from_ms,
        window_to_ms=window.to_ms,
        reset_at_ms=reset_at,
        used_usd=(data.get("summary") or {}).get("total_cost"),
    )
    page = data.get("events") or {}
    items = apply_event_pricing(
        safe_events(page.get("items") or [], expected_hash=record.cpamp_hash),
        record.model_prices,
    )
    items = _attach_event_accounting(
        items,
        record=record,
        quota=quota_state,
        selected_range=range_name,
        selected_quota=quota_summary,
        now_ms_value=window.to_ms,
    )
    return JSONResponse({
        "range": range_name,
        "from_ms": window.from_ms,
        "to_ms": window.to_ms,
        "reset_at_ms": reset_at,
        "quota": quota_summary,
        "events": items,
        "next_before_ms": page.get("next_before_ms") or 0,
        "next_before_id": page.get("next_before_id") or 0,
        "has_more": bool(page.get("has_more")),
        "total_count": page.get("total_count") or len(items),
    })


def _sse(name: str, payload: Any) -> bytes:
    data = json.dumps(payload, ensure_ascii=False, separators=(",", ":"))
    return f"event: {name}\ndata: {data}\n\n".encode("utf-8")


async def events_stream(request: Request) -> StreamingResponse:
    try:
        record = _current_key(request)
    except AuthError as exc:
        return StreamingResponse(iter([_sse("error", {"error": str(exc)})]), media_type="text/event-stream")

    cfg: PortalConfig = request.app.state.cfg

    async def stream():
        seen: set[str] = set()
        yield _sse("ready", {"ok": True})
        while True:
            if await request.is_disconnected():
                break
            try:
                data = await request.app.state.cpamp.analytics(
                    api_key_hash=record.cpamp_hash,
                    window=_quota(request).effective_window(_record_id(record), "24h")[0],
                    include_events=True,
                    event_limit=20,
                )
                page = data.get("events") or {}
                items = apply_event_pricing(
                    safe_events(page.get("items") or [], expected_hash=record.cpamp_hash),
                    record.model_prices,
                )
                for item in reversed(items):
                    event_hash = str(item.get("event_hash") or "")
                    if event_hash and event_hash not in seen:
                        seen.add(event_hash)
                        yield _sse("event", item)
            except Exception as exc:
                yield _sse("error", {"error": type(exc).__name__})
            await asyncio.sleep(max(1.0, cfg.poll_seconds))

    return StreamingResponse(
        stream(),
        media_type="text/event-stream",
        headers={"Cache-Control": "no-cache", "X-Accel-Buffering": "no"},
    )


async def admin_dashboard(request: Request) -> Response:
    blocked = _admin_guard(request)
    if blocked is not None:
        return blocked
    return HTMLResponse(_ADMIN_DASHBOARD.read_text(encoding="utf-8"))


async def _analytics_for_quota(request: Request, record: KeyRecord, range_name: str) -> dict[str, Any]:
    quota_state = _quota(request)
    window, reset_at = quota_state.effective_window(_record_id(record), range_name)
    data = await request.app.state.cpamp.analytics(
        api_key_hash=record.cpamp_hash,
        window=window,
        include_events=False,
        include_model_stats=True,
    )
    data = apply_key_policy_pricing(data, record.model_prices)
    summary = data.get("summary") or {}
    return _quota_projection(
        record,
        quota_state,
        range_name=range_name,
        window_from_ms=window.from_ms,
        window_to_ms=window.to_ms,
        reset_at_ms=reset_at,
        used_usd=summary.get("total_cost"),
    )


async def admin_keys(request: Request) -> JSONResponse:
    blocked = _admin_guard(request)
    if blocked is not None:
        return blocked
    state = _load_key_state(request)
    quota_state = _quota(request)
    keys = []
    for record in state.keys:
        usage_windows: dict[str, Any] = {}
        for name in RESET_WINDOWS:
            try:
                usage_windows[name] = await _analytics_for_quota(request, record, name)
            except Exception as exc:
                usage_windows[name] = {"range": name, "error": type(exc).__name__}
        row = _safe_record(record, quota_state)
        row["usage_windows"] = usage_windows
        keys.append(row)
    return JSONResponse({"keys": keys})


async def admin_update_limits(request: Request) -> JSONResponse:
    blocked = _admin_guard(request)
    if blocked is not None:
        return blocked
    key_id = request.path_params["key_id"]
    state = _load_key_state(request)
    record = _find_record(state, key_id)
    if record is None:
        return _json_error("key_not_found", 404)
    try:
        body = await request.json()
        five_hour = _parse_float_limit((body or {}).get("five_hour_usd"))
        monthly = _parse_float_limit((body or {}).get("monthly_usd"))
    except (json.JSONDecodeError, ValueError) as exc:
        return _json_error(str(exc) or "invalid_json", 400)
    quota_state = _quota(request)
    quota_state.set_limits(_record_id(record), five_hour_usd=five_hour, monthly_usd=monthly, actor=_actor(request))
    return JSONResponse({"me": _safe_record(record, quota_state)})


async def admin_update_limits_batch(request: Request) -> JSONResponse:
    blocked = _admin_guard(request)
    if blocked is not None:
        return blocked
    try:
        body = await request.json()
    except json.JSONDecodeError:
        return _json_error("invalid_json", 400)
    items = (body or {}).get("limits")
    if not isinstance(items, list):
        return _json_error("invalid_limits", 400)

    state = _load_key_state(request)
    validated: list[tuple[KeyRecord, float | None, float | None]] = []
    for index, item in enumerate(items):
        if not isinstance(item, dict):
            return _json_error(f"invalid_limit_item:{index}", 400)
        key_id = str(item.get("id") or "").strip()
        record = _find_record(state, key_id)
        if record is None:
            return _json_error(f"key_not_found:{key_id or index}", 404)
        try:
            five_hour = _parse_float_limit(item.get("five_hour_usd"))
            monthly = _parse_float_limit(item.get("monthly_usd"))
        except ValueError as exc:
            return _json_error(f"{str(exc)}:{key_id}", 400)
        validated.append((record, five_hour, monthly))

    quota_state = _quota(request)
    actor = _actor(request)
    for record, five_hour, monthly in validated:
        quota_state.set_limits(
            _record_id(record),
            five_hour_usd=five_hour,
            monthly_usd=monthly,
            actor=actor,
        )
    return JSONResponse({"ok": True, "keys": [_safe_record(record, quota_state) for record, _, _ in validated]})


async def admin_reset_usage(request: Request) -> JSONResponse:
    blocked = _admin_guard(request)
    if blocked is not None:
        return blocked
    key_id = request.path_params["key_id"]
    state = _load_key_state(request)
    record = _find_record(state, key_id)
    if record is None:
        return _json_error("key_not_found", 404)
    try:
        body = await request.json()
    except json.JSONDecodeError:
        return _json_error("invalid_json", 400)
    window = str((body or {}).get("window") or "all")
    try:
        points = _quota(request).reset(_record_id(record), window=window, actor=_actor(request))
    except ValueError as exc:
        return _json_error(str(exc), 400)
    return JSONResponse({"ok": True, "reset_points": points})


async def admin_events(request: Request) -> JSONResponse:
    blocked = _admin_guard(request)
    if blocked is not None:
        return blocked
    key_id = request.query_params.get("key_id", "all") or "all"
    state = _load_key_state(request)
    limit_raw = request.query_params.get("limit", "100")
    try:
        limit = max(1, min(int(limit_raw), 200))
    except ValueError:
        limit = 100
    range_name = _parse_range(request, default="24h")
    if key_id == "all":
        merged: list[dict[str, Any]] = []
        for record in state.enabled_keys():
            try:
                items, _item_meta = await _events_for_record(request, record, range_name=range_name, limit=limit)
            except Exception:
                continue
            merged.extend(items)
        merged.sort(key=lambda item: int(item.get("timestamp_ms") or 0), reverse=True)
        window = range_window(range_name)
        meta = {"range": range_name, "from_ms": window.from_ms, "to_ms": window.to_ms, "reset_at_ms": None}
        return JSONResponse({
            **meta,
            "quota": None,
            "key_id": "all",
            "events": merged[:limit],
        })

    record = _find_record(state, key_id)
    if record is None:
        return _json_error("key_not_found", 404)
    items, meta = await _events_for_record(request, record, range_name=range_name, limit=limit)
    return JSONResponse({
        **meta,
        "key_id": _record_id(record),
        "events": items,
    })


def create_app(cfg: PortalConfig) -> Starlette:
    if not cfg.session_secret:
        raise ValueError("CPA_USAGE_PORTAL_SESSION_SECRET or file is required")
    if not cfg.cpamp_admin_key:
        raise ValueError("CPA_USAGE_PORTAL_CPAMP_ADMIN_KEY or file is required")

    @contextlib.asynccontextmanager
    async def lifespan(app: Starlette):
        client = httpx.AsyncClient()
        app.state.cfg = cfg
        app.state.client = client
        app.state.cpamp = CPAMPClient(cfg.cpamp_base_url, cfg.cpamp_admin_key, client)
        app.state.quota = QuotaState(cfg.local_state_db_path)
        try:
            yield
        finally:
            await client.aclose()

    routes = [
        Route("/", dashboard, methods=["GET"]),
        Route("/healthz", healthz, methods=["GET"]),
        Route("/api/session", create_session, methods=["POST"]),
        Route("/api/session", delete_session, methods=["DELETE"]),
        Route("/api/me", me, methods=["GET"]),
        Route("/api/usage", usage, methods=["GET"]),
        Route("/api/events", events, methods=["GET"]),
        Route("/api/events/stream", events_stream, methods=["GET"]),
        Route("/admin/", admin_dashboard, methods=["GET"]),
        Route("/admin/api/keys", admin_keys, methods=["GET"]),
        Route("/admin/api/keys/limits", admin_update_limits_batch, methods=["PUT"]),
        Route("/admin/api/keys/{key_id:str}/limits", admin_update_limits, methods=["PUT"]),
        Route("/admin/api/keys/{key_id:str}/reset", admin_reset_usage, methods=["POST"]),
        Route("/admin/api/events", admin_events, methods=["GET"]),
    ]
    return Starlette(routes=routes, lifespan=lifespan)
