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

from .codex import (
    build_round_payload,
    declares_continue_tool,
    reasoning_enabled,
    repair_followup_input,
)
from .config import Config
from .creds import build_upstream_headers, would_inject_authorization
from .proxy import fold_stream, open_passthrough, open_round
from .store import IdStore

log = logging.getLogger("middleware.app")
MAX_DECOMPRESSED_REQUEST_BYTES = 64 * 1024 * 1024


class RequestBodyError(ValueError):
    pass


def _decode_request_body(raw: bytes, content_encoding: str | None) -> tuple[bytes, bool]:
    encodings = [item.strip().lower() for item in (content_encoding or "").split(",") if item.strip()]
    if not encodings or encodings == ["identity"]:
        return raw, False
    if encodings != ["zstd"]:
        raise RequestBodyError(f"unsupported Content-Encoding: {content_encoding}")

    try:
        with zstd.ZstdDecompressor().stream_reader(io.BytesIO(raw)) as reader:
            decoded = reader.read(MAX_DECOMPRESSED_REQUEST_BYTES + 1)
    except zstd.ZstdError as exc:
        raise RequestBodyError("invalid zstd request body") from exc
    if len(decoded) > MAX_DECOMPRESSED_REQUEST_BYTES:
        raise RequestBodyError("decompressed request body is too large")
    return decoded, True


def _drop_header(headers: dict[str, str], name: str) -> None:
    for key in list(headers):
        if key.lower() == name.lower():
            del headers[key]


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


def _strip_responses_suffix(url: str) -> str:
    """Upstream API base: the responses URL with its trailing '/responses' endpoint
    removed. 'https://h/v1/responses' -> 'https://h/v1'. A URL not ending in
    '/responses' is returned unchanged (treated as an already-base URL)."""
    return url[: -len("/responses")] if url.endswith("/responses") else url


def _api_root(listen_path: str) -> str:
    """The directory containing a listen_path's endpoint: '/v1/responses' -> '/v1'."""
    return listen_path.rsplit("/", 1)[0] if "/" in listen_path else ""


def _resolve_passthrough_url(cfg: Config, request: Request) -> str | None:
    """Map an arbitrary incoming request to its upstream equivalent for transparent
    passthrough (everything that is NOT a fold endpoint, e.g. GET /v1/models).

    The proxy serves an OpenAI-style tree: each listen_path is '<root>/<endpoint>'
    (e.g. '/v1/responses') and the resolved upstream responses URL is '<base>/responses'.
    A non-fold request is forwarded to '<base>/<suffix>', where <suffix> is the incoming
    path with the matched listen-path root removed (GET /v1/models -> <base>/models),
    so an agent can still list models / hit other endpoints through the proxy. Query
    string is preserved. Returns None only under header_required without the header.
    """
    responses_url = _resolve_upstream_url(cfg, request)
    if responses_url is None:
        return None
    base = _strip_responses_suffix(responses_url)

    path = request.url.path
    # Longest matching listen-path root wins (handles multiple listen_paths).
    roots = sorted(
        {_api_root(p) for p in cfg.server.listen_paths if "/" in p},
        key=len,
        reverse=True,
    )
    suffix = path
    for root in roots:
        if root and (path == root or path.startswith(root + "/")):
            suffix = path[len(root):] or "/"
            break

    target = base.rstrip("/") + suffix
    if request.url.query:
        target += "?" + request.url.query
    return target


def _repair_legacy_tool_search_arguments(body: dict[str, Any]) -> tuple[dict[str, Any], int]:
    """Convert legacy tool_search_call.arguments JSON strings back to objects.

    Older Codex Desktop history can replay tool_search_call.arguments as a JSON
    string. Current upstream validation expects that specific item type to carry
    an object, while normal function_call.arguments must remain a string.
    """
    input_items = body.get("input")
    if not isinstance(input_items, list):
        return body, 0

    repaired_input: list[Any] | None = None
    repaired = 0
    for i, item in enumerate(input_items):
        if not (isinstance(item, dict) and item.get("type") == "tool_search_call"):
            continue
        args = item.get("arguments")
        if not isinstance(args, str):
            continue
        try:
            parsed = json.loads(args)
        except json.JSONDecodeError:
            continue
        if not isinstance(parsed, dict):
            continue
        if repaired_input is None:
            repaired_input = list(input_items)
        new_item = dict(item)
        new_item["arguments"] = parsed
        repaired_input[i] = new_item
        repaired += 1

    if repaired_input is None:
        return body, 0
    return {**body, "input": repaired_input}, repaired


def _dedupe_tool_namespaces(body: dict[str, Any]) -> int:
    """Make tool namespace names unique across top-level and input-item tools.

    Codex attaches `tool_search_output` items whose `tools` re-declare
    namespaces already present in the top-level tool list. OpenAI tolerates
    that; DeepSeek rejects it with "Duplicate namespace name ..." (HTTP 400).
    Keep the first declaration, fold later duplicates' tools into it.
    """
    kept: dict[str, dict[str, Any]] = {}
    dropped = 0

    def normalize(container: dict[str, Any]) -> None:
        nonlocal dropped
        tools = container.get("tools")
        if not isinstance(tools, list):
            return
        out: list[Any] = []
        for entry in tools:
            if not (isinstance(entry, dict) and entry.get("type") == "namespace"):
                out.append(entry)
                continue
            name = entry.get("name")
            if not isinstance(name, str):
                out.append(entry)
                continue
            first = kept.get(name)
            if first is None:
                kept[name] = entry
                out.append(entry)
                continue
            target = first.get("tools")
            if not isinstance(target, list):
                target = []
                first["tools"] = target
            inner = entry.get("tools")
            names = {t.get("name") for t in target if isinstance(t, dict)}
            for tool in inner if isinstance(inner, list) else []:
                if isinstance(tool, dict) and tool.get("name") not in names:
                    target.append(tool)
                    names.add(tool.get("name"))
            dropped += 1
        container["tools"] = out

    normalize(body)
    input_items = body.get("input")
    if isinstance(input_items, list):
        for item in input_items:
            if isinstance(item, dict):
                normalize(item)
    return dropped



def _canonicalize_function_call_outputs(body: dict[str, Any]) -> tuple[dict[str, Any], int]:
    """Place matching function_call_output items after their call batch.

    DeepSeek's Responses API rejects a request when a developer/hook message
    sits between a function_call and its matching function_call_output. Codex
    can emit parallel calls followed by outputs interleaved with notices, so
    move only the matching outputs and leave every other item in order.
    """
    input_items = body.get("input")
    if not isinstance(input_items, list):
        return body, 0

    items = list(input_items)
    repaired = 0
    i = 0
    while i < len(items):
        if not (isinstance(items[i], dict) and items[i].get("type") == "function_call"):
            i += 1
            continue

        call_ids: list[str] = []
        while i < len(items):
            item = items[i]
            if not (isinstance(item, dict) and item.get("type") == "function_call"):
                break
            call_id = item.get("call_id")
            if isinstance(call_id, str) and call_id:
                call_ids.append(call_id)
            i += 1
        run_end = i
        if not call_ids:
            continue

        wanted = set(call_ids)
        positions: list[int] = []
        outputs: list[Any] = []
        for j in range(run_end, len(items)):
            item = items[j]
            if not (
                isinstance(item, dict)
                and item.get("type") == "function_call_output"
                and item.get("call_id") in wanted
            ):
                continue
            positions.append(j)
            outputs.append(item)

        if not positions:
            continue
        if positions == list(range(run_end, run_end + len(positions))):
            continue

        for pos in reversed(positions):
            del items[pos]
        items[run_end:run_end] = outputs
        repaired += len(outputs)
        i = run_end + len(outputs)

    if not repaired:
        return body, 0
    return {**body, "input": items}, repaired


async def _passthrough(
    client: httpx.AsyncClient, cfg: Config, request: Request, raw: bytes, url: str,
    method: str = "POST", decoded_request_body: bool = False,
):
    """Pure proxy: forward the raw request and stream the raw response back.
    `method` defaults to POST (the Responses endpoint); other endpoints pass theirs."""
    headers = build_upstream_headers(request.headers.items(), cfg)
    if decoded_request_body:
        _drop_header(headers, "content-encoding")
    resp = await open_passthrough(client, url, raw, headers, method=method)

    async def body_iter():
        try:
            async for chunk in resp.aiter_bytes():
                yield chunk
        finally:
            await resp.aclose()

    return StreamingResponse(
        body_iter(),
        status_code=resp.status_code,
        media_type=resp.headers.get("content-type", "text/event-stream"),
    )


async def handle_responses(request: Request) -> Response:
    cfg: Config = request.app.state.cfg
    client: httpx.AsyncClient = request.app.state.client

    raw = await request.body()
    try:
        decoded_raw, decoded_request_body = _decode_request_body(
            raw, request.headers.get("content-encoding")
        )
    except RequestBodyError as exc:
        return JSONResponse({"error": str(exc)}, status_code=400)
    try:
        body: dict[str, Any] = json.loads(decoded_raw)
    except (json.JSONDecodeError, UnicodeDecodeError):
        return JSONResponse({"error": "invalid JSON body"}, status_code=400)
    if not isinstance(body, dict):
        return JSONResponse({"error": "body must be a JSON object"}, status_code=400)

    url = _resolve_upstream_url(cfg, request)
    if url is None:
        return JSONResponse(
            {"error": "Responses-API-Base header is required (upstream mode=header_required)"},
            status_code=400,
        )

    # Safety: never send the proxy's configured credentials to a URL the request
    # itself supplied. If the base came from the header, the request must carry
    # its own Authorization (we won't inject ours toward an external URL).
    if _url_is_from_header(cfg, request) and would_inject_authorization(
        cfg, agent_has_authorization=request.headers.get("authorization") is not None
    ):
        log.warning("blocked: Responses-API-Base override without own auth (model=%s)",
                    body.get("model"))
        return JSONResponse(
            {"error": "When overriding the upstream base (Responses-API-Base), the request must "
                      "provide its own Authorization; the proxy will not send its configured "
                      "credentials to an externally supplied URL."},
            status_code=400,
        )

    body, legacy_repairs = _repair_legacy_tool_search_arguments(body)
    model_name = body.get("model")
    is_deepseek = isinstance(model_name, str) and "deepseek" in model_name.lower()
    tool_order_repairs = 0
    if is_deepseek:
        body, tool_order_repairs = _canonicalize_function_call_outputs(body)
    namespace_dedup = _dedupe_tool_namespaces(body) if is_deepseek else 0

    forward_raw = decoded_raw
    if legacy_repairs or tool_order_repairs or namespace_dedup:
        if legacy_repairs:
            log.info("repaired legacy tool_search_call.arguments: count=%d", legacy_repairs)
        if tool_order_repairs:
            log.info("reordered function_call_output items: count=%d", tool_order_repairs)
        if namespace_dedup:
            log.info("dropped duplicate tool namespaces: count=%d", namespace_dedup)
        forward_raw = json.dumps(body, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    if legacy_repairs or namespace_dedup:
        if legacy_repairs:
            log.info("repaired legacy tool_search_call.arguments: count=%d", legacy_repairs)
        if namespace_dedup:
            log.info("dropped duplicate tool namespaces: count=%d", namespace_dedup)
        forward_raw = json.dumps(body, ensure_ascii=False, separators=(",", ":")).encode("utf-8")

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
        return await _passthrough(
            client,
            cfg,
            request,
            forward_raw,
            url,
            decoded_request_body=decoded_request_body,
        )

    log.info("fold start: model=%s path=%s url=%s input_items=%d",
             body.get("model"), request.url.path, url, len(body.get("input") or []))

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
    if decoded_request_body:
        _drop_header(headers, "content-encoding")
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
        return Response(
            err, status_code=resp.status_code, media_type=resp.headers.get("content-type")
        )

    return StreamingResponse(
        fold_stream(client, cfg, body, headers, resp, request.app.state.id_store, url=url),
        media_type="text/event-stream",
    )


async def handle_passthrough(request: Request) -> Response:
    """Transparent passthrough for any request that is NOT a fold endpoint (e.g.
    GET /v1/models, POST /v1/embeddings), so an agent can still list models and hit
    other endpoints through the proxy instead of getting 404. The raw request is
    forwarded to the upstream equivalent and the response streamed back unchanged.
    The same credential-leak guard as the fold path applies (never inject configured
    credentials toward a request-supplied Responses-API-Base URL)."""
    cfg: Config = request.app.state.cfg
    client: httpx.AsyncClient = request.app.state.client

    url = _resolve_passthrough_url(cfg, request)
    if url is None:
        return JSONResponse(
            {"error": "Responses-API-Base header is required (upstream mode=header_required)"},
            status_code=400,
        )

    if _url_is_from_header(cfg, request) and would_inject_authorization(
        cfg, agent_has_authorization=request.headers.get("authorization") is not None
    ):
        log.warning("blocked passthrough: Responses-API-Base override without own auth (path=%s)",
                    request.url.path)
        return JSONResponse(
            {"error": "When overriding the upstream base (Responses-API-Base), the request must "
                      "provide its own Authorization; the proxy will not send its configured "
                      "credentials to an externally supplied URL."},
            status_code=400,
        )

    raw = await request.body()
    log.info("passthrough: %s %s -> %s", request.method, request.url.path, url)
    return await _passthrough(client, cfg, request, raw, url, method=request.method)


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
        app.state.client = _make_client()
        app.state.id_store = IdStore()
        try:
            yield
        finally:
            await app.state.client.aclose()

    routes = [
        Route(path, handle_responses, methods=["POST"]) for path in cfg.server.listen_paths
    ]
    # Catch-all transparent passthrough for everything else (GET /v1/models, etc.).
    # Fold routes are POST-only and listed first, so they win on full match; any other
    # path/method falls through here and is forwarded to the upstream unchanged.
    routes.append(Route(
        "/{path:path}",
        handle_passthrough,
        methods=["GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"],
    ))
    return Starlette(routes=routes, lifespan=lifespan)
