# CPA Key Policy Plus design

## Architecture

`cpa-key-policy-plus` is a self-owned Go CPA plugin. It replaces the old
Key Policy plugin as the exclusive frontend auth provider for ordinary `cpa_`
keys and owns the SQLite state for key settings, usage events, reset watermarks,
active request slots, active Codex sessions, and audit records.

The intended final request chain is:

`Client -> CPA -> cpa-key-policy-plus auth checks -> cpa-governor/CodexCont Engine routing -> CPA upstream execution`.

Governor remains responsible for CodexCont Engine status and execution routing.
Plus owns key policy decisions and user/admin usage views.

Current implementation note: CodexCont's production 516/518n-2 folding still
lives in the Python sidecar `/v1/responses` path. The existing Engine API only
returns safe status summaries. Therefore Plus can be deployed now as the user
key and quota authority, but the public `/v1/responses` Caddy route must not be
cut from the known-good CodexCont sidecar path to CPA-first until Governor has
an executor-level continuation supervisor that is verified with a real test key.

## Data Model

Plus stores:

- `keys`: id, name, key hash, safe preview, enabled flag, RPM, request
  concurrency, max active sessions, model allowlist, model prices, and
  5H/24H/7D/month USD limits.
- `usage_events`: safe request metadata, usage counters, cost breakdown,
  failure summary, and key identity.
- `reset_watermarks`: per-key, per-window soft reset points.
- `active_requests`: per-key request-concurrency slots with stale TTL cleanup.
- `active_sessions`: per-key Codex window/session identifiers, source, first
  seen, last seen, and missing-signal warnings.
- `audit_logs`: admin actions, imports, resets, and policy rejections.

No table stores raw API keys, Authorization headers, request bodies, response
bodies, OAuth tokens, cookies, or encrypted reasoning content.

## Enforcement

Frontend auth checks run in this order:

1. Extract and verify Bearer `cpa_` key by hash.
2. Reject disabled key or disallowed requested model.
3. Enforce RPM.
4. Enforce quota windows using reset watermarks.
5. Acquire request-concurrency slot.
6. Extract Codex session/window identity and enforce max active sessions.

Request-concurrency slots are released by usage records when possible and have a
stale TTL fallback. Active sessions expire after 30 idle minutes. Missing session
identity is allowed in v1 and audited as `missing_session_identity`.

## Interfaces

Plugin registration exposes:

- Admin resource: `/v0/resource/plugins/cpa-key-policy-plus/admin`
- User resource: `/v0/resource/plugins/cpa-key-policy-plus/user`
- User host compatibility for `cpa-usage.konbakuyomu.us`
- Management/API routes for key listing, saves, resets, usage, active sessions,
  audits, and user session/login APIs.

Plus should keep user-facing API shapes close to the current Governor user
surface so `cpa-usage` can be migrated without surprising users.

## Migration And Rollback

Migration imports old Key Policy state into Plus by key hash. Existing
usage-admin and Governor 5H/month limits and reset watermarks may be imported
from their SQLite files if configured, but Plus becomes the source of truth after
cutover.

Deployment must back up CPA config, old Key Policy state, Governor SQLite, Plus
SQLite, and Caddy/admin proxy configs. Rollback means restoring the old plugin
binary/config, re-enabling old Key Policy, restoring previous routing, and
removing/ignoring Plus exclusive auth.

## Route Boundaries

`cpa.konbakuyomu.us` exposes only API paths needed by users. Admin/plugin/user
resources remain blocked there. `cpa-admin.konbakuyomu.us` exposes admin plugin
pages behind existing protection. `cpa-usage.konbakuyomu.us` exposes only the
ordinary user page and APIs.
