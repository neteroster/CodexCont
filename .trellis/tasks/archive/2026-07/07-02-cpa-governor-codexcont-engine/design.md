# CPA Governor plugin and CodexCont engine design

## Architecture

Governor is a self-owned CPA plugin plus a small state database. CPA remains the public API entrypoint. Governor authenticates frontend keys, enforces policy, records usage, serves admin/user pages, and optionally invokes CodexCont Engine before upstream execution.

CodexCont remains a separate Python container. Its new engine API is internal-only and focused on protection decisions and safe summaries. The current middleware/admin dashboard stays available during migration as rollback and evidence tooling.

## Plugin capabilities

- `FrontendAuthProvider`: validates Governor/Key Policy-compatible keys and returns a stable principal. It rejects disabled keys and unknown keys before CPA routing.
- `ModelRouter + Executor`: routes protected Responses requests to Governor execution. The executor can call CodexCont Engine and CPA host model callbacks. If engine is disabled or unavailable, behavior follows configured fail mode.
- `UsagePlugin`: consumes CPA `UsageRecord`, normalizes token/cache/reasoning/cost data, and stores a safe event projection.
- `ManagementAPI`: exposes authenticated admin APIs and browser resources for the CPA Admin plugin menu.
- Resource routes: expose user self-service pages and GET-only user APIs; sensitive mutations stay in Management APIs.

## State model

Governor SQLite stores:

- `keys`: id, name, safe preview, key hash, enabled flag, model allowlist, rpm, concurrency, created/updated timestamps.
- `limits`: 5H/day/week/month USD limits per key.
- `prices`: per-key/per-model input, output, cache-read, cache-create prices per million tokens.
- `reset_watermarks`: per-key per-window soft reset timestamp.
- `usage_events`: safe request events, token counters, cache buckets, reasoning tokens, estimated cost, latency, status, failure summary.
- `codexcont_summaries`: request id, key id, protection result, hit round, latest/final reasoning token counters, continuation count, stop/failure reason.
- `audit_log`: admin operations, target key/config, timestamp, safe actor/source.

No raw key, OAuth token, Authorization header, raw request/response body, encrypted reasoning content, or true CoT is stored.

## CodexCont Engine contract

- `GET /engine/healthz` returns service health, uptime, mode, and version.
- `POST /engine/v1/responses/analyze` accepts a sanitized request envelope plus optional chunk/usage summaries and returns a protection decision/summary.
- Full folded execution is enabled behind a feature flag once plugin host-model callback execution is validated in production. Until then, the current sidecar continuation path remains the production fallback.

The engine returns only safe fields: request id, model, protection value, first truncation round, first truncation reasoning tokens, latest/final reasoning tokens, continuation count, folded flag, stopped reason, failure summary.

## UI design

Admin page inside CPA Admin:

- Key management and bulk save/reset.
- Global usage and request detail.
- CodexCont config and health.
- Global protection status and audit log.

User page:

- Login with CPA/Governor key via Authorization header, not URL.
- Tabs: quota/usage, thought-chain protection, request details.
- 1-2 second polling with visible refresh/reconnect state.
- Own-key filtering enforced server-side.

## Rollout and rollback

1. Deploy CodexCont engine API without removing current middleware path.
2. Deploy Governor plugin disabled/passive: admin/user pages and usage ingestion first.
3. Import/mirror existing Key Policy keys and verify user/admin visibility.
4. Enable hard quota enforcement for test keys, then normal keys.
5. Enable protected executor path for test key/model only.
6. Remove Caddy `/v1/responses -> codexcont` front split only after real production smoke passes.

Rollback keeps current working Caddy/CodexCont/CPA path. Governor can be disabled from CPA plugin config without deleting state.

## Implementation notes

- Governor refreshes the Key Policy state file opportunistically. UI reads,
  user login, and usage ingestion re-check file mtime with a short debounce so
  newly created Key Policy keys appear without restarting CPA.
- CPA frontend auth providers do not have a separate hard-deny return shape:
  an unauthenticated Governor response means "not handled". Therefore hard
  quota/RPM/concurrency blocking requires Governor to run as the exclusive
  frontend auth provider, or the old user auth plugin must not accept the same
  user keys.
- `codexcont_enabled` and `codexcont_route` are intentionally separate.
  Production can show CodexCont engine health and status while keeping the
  current Caddy-fronted continuation path until executor continuation is proven.
