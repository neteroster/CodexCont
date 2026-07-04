"""Starlette app: route the agent's Responses request through the fold logic.

Only ACTS when continuation is enabled and the agent did not itself declare a
`continue_thinking` tool (collision rule). Otherwise it is a pure passthrough,
so it is safe in front of all traffic.
"""
from __future__ import annotations

import contextlib
import io
import json
import logging
from typing import Any

import httpx
import zstandard as zstd
from starlette.applications import Starlette
from starlette.requests import Request
from starlette.responses import JSONResponse, Response, StreamingResponse
from starlette.routing import Route

from .admin import (
    admin_dashboard,
    admin_healthz,
    admin_logs,
    admin_logs_stream,
    admin_requests,
    admin_redirect,
    admin_status,
)
from .engine import engine_analyze, engine_healthz
from .codex import (
    build_round_payload,
    declares_continue_tool,
    reasoning_enabled,
    repair_followup_input,
)
from .config import Config
from .creds import build_upstream_headers, would_inject_authorization
from .diagnostics import Diagnostics
from .key_identity import KeyIdentityResolver
from .proxy import fold_stream, open_passthrough, open_round
from .store import IdStore

log = logging.getLogger("middleware.app")


class BodyDecodeError(ValueError):
    pass


def _decode_request_body(raw: bytes, encoding: str | None) -> bytes:
    enc = (encoding or "").strip().lower()
    if not enc or enc == "identity":
        return raw
    if enc != "zstd":
        raise BodyDecodeError(f"unsupported request content-encoding: {enc}")

    try:
        with zstd.ZstdDecompressor().stream_reader(io.BytesIO(raw)) as reader:
            return reader.read()
    except zstd.ZstdError as exc:
        raise BodyDecodeError("invalid zstd request body") from exc


def _header_base(request: Request) -> str | None:
    """The non-blank Responses-API-Base header value, or None (case-insensitive)."""
    v = request.headers.get("responses-api-base")
    v = v.strip() if v else ""
    return v or None


def _join_responses(base: str) -> str:
    """Build the Responses endpoint from a base URL (OpenAI base_url convention:
    `<base>/responses`). Lenient: if the value already ends in `/responses`
    (a full endpoint was passed), use it as-is."""
    base = base.rstrip("/")
    return base if base.endswith("/responses") else base + "/responses"


def _resolve_upstream_url(cfg: Config, request: Request) -> str | None:
    """Target URL for this request.

    - "fixed": always the configured URL (header ignored).
    - "header": the Responses-API-Base header (case-insensitive) is treated as a
      base URL and `/responses` is appended; overrides the configured URL when
      present, else the configured URL.
    - "header_required": the header MUST be present; returns None when it is
      absent/blank so the caller can reject the request (400).

    The header is stripped before forwarding upstream (build_upstream_headers).
    """
    if cfg.upstream.mode in ("header", "header_required"):
        base = _header_base(request)
        if base:
            return _join_responses(base)
        if cfg.upstream.mode == "header_required":
            return None
    return cfg.upstream.url


def _url_is_from_header(cfg: Config, request: Request) -> bool:
    return cfg.upstream.mode in ("header", "header_required") and _header_base(request) is not None


async def _passthrough(
    client: httpx.AsyncClient,
    cfg: Config,
    request: Request,
    raw: bytes,
    url: str,
    diagnostics: Diagnostics | None = None,
    request_id: str | None = None,
):
    """Pure proxy: forward the raw request and stream the raw response back."""
    headers = build_upstream_headers(request.headers.items(), cfg)
    try:
        resp = await open_passthrough(client, url, raw, headers)
    except Exception as exc:
        if diagnostics and request_id:
            diagnostics.request_failed(request_id, reason="passthrough_open_error", detail=repr(exc))
        raise

    async def body_iter():
        failed = False
        try:
            async for chunk in resp.aiter_bytes():
                yield chunk
        except Exception as exc:
            failed = True
            if diagnostics and request_id:
                diagnostics.request_failed(request_id, reason="passthrough_stream_error", detail=repr(exc))
            raise
        finally:
            if diagnostics and request_id and not failed:
                diagnostics.request_finished(request_id, status=f"passthrough:{resp.status_code}")
            await resp.aclose()

    return StreamingResponse(
        body_iter(),
        status_code=resp.status_code,
        media_type=resp.headers.get("content-type", "text/event-stream"),
    )


async def handle_responses(request: Request) -> Response:
    cfg: Config = request.app.state.cfg
    client: httpx.AsyncClient = request.app.state.client
    diagnostics: Diagnostics = request.app.state.diagnostics
    key_identity = request.app.state.key_identity.identify_authorization(
        request.headers.get("authorization")
    )
    request_id = diagnostics.request_started(path=request.url.path, key_identity=key_identity)

    wire_raw = await request.body()
    try:
        raw = _decode_request_body(wire_raw, request.headers.get("content-encoding"))
    except BodyDecodeError as exc:
        log.warning(
            "request body decode failed: content-type=%s content-encoding=%s len=%d error=%s",
            request.headers.get("content-type"),
            request.headers.get("content-encoding"),
            len(wire_raw),
            exc,
        )
        diagnostics.request_failed(request_id, reason="body_decode_error", detail=str(exc))
        return JSONResponse({"error": str(exc)}, status_code=400)

    try:
        body: dict[str, Any] = json.loads(raw)
    except (json.JSONDecodeError, UnicodeDecodeError):
        log.warning(
            "invalid JSON body: content-type=%s content-encoding=%s len=%d",
            request.headers.get("content-type"),
            request.headers.get("content-encoding"),
            len(raw),
        )
        diagnostics.request_failed(request_id, reason="invalid_json_body")
        return JSONResponse({"error": "invalid JSON body"}, status_code=400)
    if not isinstance(body, dict):
        diagnostics.request_failed(request_id, reason="non_object_body")
        return JSONResponse({"error": "body must be a JSON object"}, status_code=400)

    url = _resolve_upstream_url(cfg, request)
    if url is None:
        diagnostics.request_update(request_id, model=body.get("model"))
        diagnostics.request_failed(request_id, reason="missing_responses_api_base")
        return JSONResponse(
            {"error": "Responses-API-Base header is required (upstream mode=header_required)"},
            status_code=400,
        )
    diagnostics.request_update(request_id, model=body.get("model"), upstream_url=url)

    # Safety: never send the proxy's configured credentials to a URL the request
    # itself supplied. If the base came from the header, the request must carry
    # its own Authorization (we won't inject ours toward an external URL).
    if _url_is_from_header(cfg, request) and would_inject_authorization(
        cfg, agent_has_authorization=request.headers.get("authorization") is not None
    ):
        log.warning("blocked: Responses-API-Base override without own auth (model=%s)",
                    body.get("model"))
        diagnostics.request_failed(request_id, reason="blocked_header_override_without_own_auth")
        return JSONResponse(
            {"error": "When overriding the upstream base (Responses-API-Base), the request must "
                      "provide its own Authorization; the proxy will not send its configured "
                      "credentials to an externally supplied URL."},
            status_code=400,
        )

    # Fold only a streaming, reasoning-enabled request that isn't a collision.
    # Everything else (non-reasoning, non-streaming, continuation disabled, or
    # the agent declaring its own continue_thinking) is a pure passthrough.
    # The collision rule only matters for the tool_pair method (we inject a tool);
    # commentary injects no tool, so a declared continue_thinking is irrelevant.
    collision = (
        cfg.cont.method == "tool_pair"
        and declares_continue_tool(body, cfg.cont.continue_tool_name)
    )
    should_fold = (
        cfg.cont.enabled
        and bool(body.get("stream"))
        and reasoning_enabled(body)
        and not collision
    )
    if not should_fold:
        why = ("disabled" if not cfg.cont.enabled
               else "non-stream" if not body.get("stream")
               else "non-reasoning" if not reasoning_enabled(body)
               else "declares-continue_thinking")
        log.info("passthrough (%s): model=%s path=%s url=%s",
                 why, body.get("model"), request.url.path, url)
        diagnostics.mark_passthrough(request_id, reason=why, model=body.get("model"))
        return await _passthrough(client, cfg, request, raw, url, diagnostics, request_id)

    log.info("fold start: model=%s path=%s url=%s input_items=%d",
             body.get("model"), request.url.path, url, len(body.get("input") or []))
    diagnostics.mark_fold_start(
        request_id, model=body.get("model"), path=request.url.path, upstream_url=url
    )

    # repair_followup="stateful": re-insert tool_pair continue pairs after recorded
    # ids (tool_pair only — commentary preserves cross-turn structure via forward_marker).
    if cfg.cont.repair_followup == "stateful" and cfg.cont.method == "tool_pair":
        body = {
            **body,
            "input": repair_followup_input(
                list(body.get("input") or []),
                request.app.state.id_store,
                tool_name=cfg.cont.continue_tool_name,
                output_text=cfg.cont.continue_output_text,
            ),
        }

    headers = build_upstream_headers(request.headers.items(), cfg)
    payload = build_round_payload(
        body,
        input_items=list(body.get("input") or []),
        force_include_encrypted=cfg.stream.force_include_encrypted,
        drop_previous_response_id=False,  # round 1 passes it through
    )

    # Open round 1 here so a non-2xx (e.g. bad auth) is mirrored with its real
    # status code rather than buried inside a 200 SSE stream.
    resp = await open_round(client, url, payload, headers)
    if resp.status_code >= 400:
        err = await resp.aread()
        await resp.aclose()
        diagnostics.request_failed(
            request_id, reason="upstream_http_error", detail={"status_code": resp.status_code}
        )
        return Response(
            err, status_code=resp.status_code, media_type=resp.headers.get("content-type")
        )

    return StreamingResponse(
        fold_stream(
            client,
            cfg,
            body,
            headers,
            resp,
            request.app.state.id_store,
            url=url,
            diagnostics=diagnostics,
            request_id=request_id,
        ),
        media_type="text/event-stream",
    )


def _make_client() -> httpx.AsyncClient:
    """A client that does NOT invent a User-Agent or Accept of its own; those
    are forwarded from the agent or omitted. httpx still manages Host /
    Content-Length / Accept-Encoding / Connection (plan-allowed)."""
    client = httpx.AsyncClient(timeout=None)
    for h in ("user-agent", "accept"):
        if h in client.headers:
            del client.headers[h]
    return client


def create_app(cfg: Config) -> Starlette:
    @contextlib.asynccontextmanager
    async def lifespan(app: Starlette):
        app.state.cfg = cfg
        app.state.diagnostics = Diagnostics(max_events=cfg.admin.max_log_events)
        app.state.key_identity = KeyIdentityResolver(cfg.admin.key_policy_state_path)
        app.state.client = _make_client()
        app.state.id_store = IdStore()
        try:
            yield
        finally:
            await app.state.client.aclose()

    routes = [
        Route("/engine/healthz", engine_healthz, methods=["GET"]),
        Route("/engine/v1/responses/analyze", engine_analyze, methods=["POST"]),
        Route("/admin", admin_redirect, methods=["GET"]),
        Route("/admin/", admin_dashboard, methods=["GET"]),
        Route("/admin/healthz", admin_healthz, methods=["GET"]),
        Route("/admin/status", admin_status, methods=["GET"]),
        Route("/admin/requests", admin_requests, methods=["GET"]),
        Route("/admin/logs", admin_logs, methods=["GET"]),
        Route("/admin/logs/stream", admin_logs_stream, methods=["GET"]),
    ] + [
        Route(path, handle_responses, methods=["POST"]) for path in cfg.server.listen_paths
    ]
    return Starlette(routes=routes, lifespan=lifespan)
