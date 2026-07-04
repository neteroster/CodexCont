"""Read-only admin routes for the CodexCont dashboard."""
from __future__ import annotations

import asyncio
import json
from pathlib import Path
from typing import Any
from urllib.parse import urlsplit

import httpx
from starlette.requests import Request
from starlette.responses import HTMLResponse, JSONResponse, RedirectResponse, StreamingResponse

from .config import Config

_DASHBOARD_HTML = Path(__file__).with_name("dashboard.html")


def _admin_diag(request: Request):
    return request.app.state.diagnostics


def _safe_config(cfg: Config, diagnostics_max_events: int) -> dict[str, Any]:
    parsed = urlsplit(cfg.upstream.url)
    return {
        "listen_paths": list(cfg.server.listen_paths),
        "upstream_mode": cfg.upstream.mode,
        "upstream_host": parsed.netloc,
        "upstream_path": parsed.path,
        "auth_mode": cfg.auth.mode,
        "continuation_enabled": cfg.cont.enabled,
        "continuation_method": cfg.cont.method,
        "max_continue": cfg.cont.max_continue,
        "truncation_step": cfg.cont.truncation_step,
        "log_retention": diagnostics_max_events,
        "key_identity_configured": bool(cfg.admin.key_policy_state_path),
    }


def _upstream_health_url(cfg: Config) -> str | None:
    parsed = urlsplit(cfg.upstream.url)
    if not parsed.scheme or not parsed.netloc:
        return None
    return f"{parsed.scheme}://{parsed.netloc}/healthz"


async def _probe_upstream(client: httpx.AsyncClient, cfg: Config) -> dict[str, Any]:
    url = _upstream_health_url(cfg)
    if not url:
        return {"ok": False, "status": "invalid_upstream_url"}
    try:
        resp = await client.get(url, timeout=3.0)
        return {
            "ok": 200 <= resp.status_code < 400,
            "status": "http",
            "status_code": resp.status_code,
            "url": url,
        }
    except Exception as exc:
        return {
            "ok": False,
            "status": "error",
            "error": type(exc).__name__,
            "url": url,
        }


async def admin_healthz(request: Request) -> JSONResponse:
    return JSONResponse(_admin_diag(request).health())


async def admin_status(request: Request) -> JSONResponse:
    cfg: Config = request.app.state.cfg
    upstream = await _probe_upstream(request.app.state.client, cfg)
    diag = _admin_diag(request)
    return JSONResponse(
        diag.snapshot(upstream=upstream, config=_safe_config(cfg, diag.max_events))
    )


async def admin_logs(request: Request) -> JSONResponse:
    raw_limit = request.query_params.get("limit", "200")
    try:
        limit = int(raw_limit)
    except ValueError:
        limit = 200
    diag = _admin_diag(request)
    return JSONResponse({"events": diag.recent(limit=limit), "max_events": diag.max_events})


async def admin_requests(request: Request) -> JSONResponse:
    raw_limit = request.query_params.get("limit", "100")
    try:
        limit = int(raw_limit)
    except ValueError:
        limit = 100
    diag = _admin_diag(request)
    return JSONResponse(
        {"requests": diag.recent_requests(limit=limit), "max_requests": diag.max_requests}
    )


def _sse_event(name: str, data: Any) -> bytes:
    body = json.dumps(data, ensure_ascii=False, separators=(",", ":"))
    return f"event: {name}\ndata: {body}\n\n".encode("utf-8")


async def admin_logs_stream(request: Request) -> StreamingResponse:
    diag = _admin_diag(request)
    log_queue = diag.subscribe()
    request_queue = diag.subscribe_requests()
    once = request.query_params.get("once") == "1"

    async def events():
        try:
            yield _sse_event("ready", {"ok": True})
            for item in diag.recent_requests(limit=50):
                yield _sse_event("request", item)
            for item in diag.recent(limit=50):
                yield _sse_event("log", item)
            if once:
                return
            while True:
                if await request.is_disconnected():
                    break
                log_task = asyncio.create_task(log_queue.get())
                req_task = asyncio.create_task(request_queue.get())
                done, pending = await asyncio.wait(
                    {log_task, req_task},
                    timeout=15.0,
                    return_when=asyncio.FIRST_COMPLETED,
                )
                for task in pending:
                    task.cancel()
                if pending:
                    await asyncio.gather(*pending, return_exceptions=True)
                if not done:
                    yield b": keepalive\n\n"
                    continue
                for task in done:
                    item = task.result()
                    event_name = "log" if task is log_task else "request"
                    yield _sse_event(event_name, item)
        finally:
            diag.unsubscribe(log_queue)
            diag.unsubscribe_requests(request_queue)

    return StreamingResponse(
        events(),
        media_type="text/event-stream",
        headers={"Cache-Control": "no-cache", "X-Accel-Buffering": "no"},
    )


async def admin_dashboard(_request: Request) -> HTMLResponse:
    return HTMLResponse(_DASHBOARD_HTML.read_text(encoding="utf-8"))


async def admin_redirect(_request: Request) -> RedirectResponse:
    return RedirectResponse(url="./admin/", status_code=307)
