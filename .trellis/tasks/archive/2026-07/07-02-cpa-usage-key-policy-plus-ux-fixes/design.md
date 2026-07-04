# Design

## Boundaries

This task changes only the self-owned `cpa-key-policy-plus` plugin and narrow proxy routing when needed.

- Plugin backend: `cpa_key_policy_plus_plugin/go/main.go`
- Plugin store/models: `cpa_key_policy_plus_plugin/go/internal/policyplus/**`
- Plugin UI: `cpa_key_policy_plus_plugin/go/assets/admin.html`, `user.html`, `shared.css`
- Tests: `cpa_key_policy_plus_plugin/go/**/*_test.go`
- Optional admin/public proxy routing if a path bug is proven

Do not modify CPA, CPAMP, old Key Policy, or their images/source.

## Current Problems By Layer

### Public `cpa-usage`

The public route itself is alive. Caddy rewrites `/` to the Plus user resource and the resource API returns valid JSON status responses. The observed `连接异常 / 同步失败` state is therefore more likely a UI/session/retry issue than a dead proxy route.

Current frontend behavior couples several fetches into one refresh:

- `/me`
- `/usage`
- `/events`
- `/codexcont`

Any one failure puts the topbar into `连接异常`. The UI does not visibly classify `401 not_authenticated` as session expiry, and a bad poll can leave the user with an empty table and generic sync failure.

Design response:

- Treat `401 not_authenticated` as `会话已过期，请重新登录`, not generic network failure.
- Keep last known good data when a non-auth poll fails.
- Split refresh state into per-domain health:
  - session/account
  - usage/events
  - CodexCont protection
- The topbar can show an aggregate state, but the content area must show which section failed.
- Manual refresh must cancel older requests, start a new sequence, and ensure stale failed responses cannot overwrite a newer successful snapshot.
- Page visibility restore should always trigger a fresh snapshot if logged in.

### Admin `CPA Key Policy+`

The current admin table is doing too much in one row. It has no visual difference between "configured value" and "actual usage", and the row overflows into ellipsis artifacts.

Design response:

Use a master/detail layout.

Desktop:

- Left/main list: one row per key, compact and mostly read-only.
- Right detail drawer/panel: editable settings for the selected key.

Mobile:

- Key list becomes stacked rows.
- Detail editor opens as a full-width panel/modal.

Key list columns:

- Key: name, safe preview, status chip.
- Health: enabled/disabled/archived, active sessions.
- Limits: 5H/24H/7D/month mini progress summaries.
- Controls: RPM, request concurrency, Codex windows as compact text, not inline inputs.
- Models: count and price coverage.
- Usage: selected range cost and 24H quick hint.
- Actions: edit, reset, archive.

Detail editor sections:

- Basic settings: name, enabled, RPM, request concurrency, Codex active windows.
- Quota windows: 5H/24H/7D/month each shows `used / limit / remaining` and an editable limit field.
- Models and prices: reuse the existing model editor modal and structured price table.
- Usage reset: soft reset per window.
- Lifecycle: archive/restore; optional hard delete in danger zone depending on product decision.

### Key Lifecycle

Hard deleting a key row is simple but risky because usage events, Codex summaries, reset watermarks, active sessions, and audit logs may reference the key ID. The better default is to add soft archival.

Recommended store extension:

- Add nullable-ish columns to `keys`:
  - `archived integer default 0`
  - `archived_at integer default 0`
- `ListKeys` returns all keys for admin by default, with UI filter `显示归档`.
- Frontend auth treats `archived` like disabled: cannot authenticate/use.
- User portal cannot log in with archived keys.
- Usage/history remains visible to admin.

Optional hard delete:

- Implement as advanced route only after archive exists.
- Either reject if usage exists, or require deleting only the key row while preserving historical events with safe preview. The safer v1 is "reject if usage exists".

## API Changes

Admin management routes:

- `GET /plugins/cpa-key-policy-plus/keys`
  - return keys with `usage`, `limits`, `active_sessions`, `archived`, and `remaining` projections.
- `PUT /plugins/cpa-key-policy-plus/keys/save`
  - accept `archived` if lifecycle is included.
- `POST /plugins/cpa-key-policy-plus/keys/archive`
  - `{ id, archived: true|false }`
- Optional:
  - `DELETE /plugins/cpa-key-policy-plus/keys`
  - `{ id, confirm }`, reject when usage/history exists unless explicitly allowed by later decision.

User resource API:

- Existing paths stay stable:
  - `/user/api/session`
  - `/user/api/me`
  - `/user/api/usage`
  - `/user/api/events`
  - `/user/api/codexcont`
- Error responses should include safe `error`, `message`, and a category usable by frontend:
  - `auth`
  - `network`
  - `usage`
  - `codexcont`
  - `unknown`

## UI Preview Plan

Create local previews with seeded data before production deploy.

Variant A, recommended:

- CPAMP-like key list plus right detail drawer.
- Best for many keys and dense operator work.

Variant B:

- Key cards plus full-width detail panel below selected card.
- More readable for small key counts, less dense for admin use.

Variant C:

- Two-level table: collapsed key rows with expandable editor rows.
- Closest to current table, but still risks cramped layouts and row height jumps.

Recommendation: implement Variant A, with mobile fallback behaving like Variant B.

Preview mechanics:

- Extend existing preview mode or add a small dev-only preview fixture endpoint.
- Seed several keys:
  - enabled normal key with meaningful usage
  - disabled key
  - archived key
  - key with no limit
  - key with incomplete prices
- Seed usage events and CodexCont summaries so progress bars and details are visible.

## Compatibility

- Existing keys must remain usable.
- Existing SQLite DB must migrate forward with `alter table` column checks only.
- Existing `cpa_` sessions remain valid unless the session secret changed or the key hash/ID changed.
- Existing old records without new lifecycle fields display as active/non-archived.
- Current model discovery behavior remains:
  - CPA/host model hints first
  - Plus configured models as fallback
  - unknown selected models preserved

## Operational Notes

- The old `cpa-usage-portal` container is still running but not on the current public route. After this task verifies Plus user page fully replaces it, we can plan a separate stop/retire action. Do not delete its files in this task.
- Server deploy should back up the current `.so`, Plus SQLite DB, CPA config, and proxy config.
- Do not run Docker prune.
- Do not batch-delete files/directories.

