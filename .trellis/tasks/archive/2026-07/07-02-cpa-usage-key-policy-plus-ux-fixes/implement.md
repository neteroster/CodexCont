# Implementation Plan

## Phase 0: Planning Gate

- [ ] Review this PRD/design/implementation plan with the user.
- [ ] Resolve the key lifecycle decision:
  - recommended: archive/restore in v1, hard delete only as constrained danger action.
- [ ] Do not run `task.py start` until the user approves implementation.

## Phase 1: Evidence And Reproduction

- [ ] Add or run local Plus preview with seeded admin/user data.
- [ ] Use Playwright to capture current admin/user screenshots:
  - desktop
  - 390px mobile
- [ ] Reproduce public `cpa-usage` stuck state locally by mocking:
  - `/codexcont` failure while `/usage` succeeds
  - `401 not_authenticated`
  - slow request followed by newer successful request
- [ ] Record findings in this task before changing code.

## Phase 2: Backend Support

- [ ] Add key lifecycle fields to `KeyRecord` and SQLite schema:
  - `archived`
  - `archived_at`
- [ ] Ensure `FrontendAuthProvider` rejects archived keys with a clear safe message.
- [ ] Add store methods:
  - `SetArchived(ctx, id, archived, at)`
  - optional `DeleteKeyIfUnused(ctx, id)` if hard delete is approved.
- [ ] Enrich admin key projection:
  - used values for 5H/24H/7D/month
  - limit values
  - remaining values when limit exists
  - active sessions
  - price coverage
  - archived state
- [ ] Add admin route(s):
  - `/plugins/cpa-key-policy-plus/keys/archive`
  - matching `/key-policy-plus/api/keys/archive` alias
  - optional delete route only if approved.
- [ ] Keep raw key/hash/body/cookie data out of all responses.

## Phase 3: Public User Page Stability

- [ ] Refactor refresh pipeline:
  - keep last good data
  - isolate auth errors from section errors
  - never let stale failed requests overwrite newer snapshots
  - manual refresh always clears stuck `syncing`
- [ ] Show clear state messages:
  - `实时已连接`
  - `部分数据同步失败`
  - `会话已过期`
  - `正在同步`
- [ ] Add section-level fallback cards for usage/protection failures.
- [ ] Preserve current sorting newest-first and stale processing filtering.

## Phase 4: Admin UI Redesign

- [ ] Replace the main inline edit table with master/detail layout.
- [ ] Key list shows safe summary fields only.
- [ ] Detail editor shows editable settings and quota progress.
- [ ] Reuse existing model/price editor with layout polish.
- [ ] Add archive/restore action and confirmation.
- [ ] Add optional hard delete danger action if approved.
- [ ] Remove meaningless overflow/ellipsis artifacts.
- [ ] Keep CPAMP-like dark style and avoid flashy effects.

## Phase 5: Local Preview Variants

- [ ] Produce preview Variant A: list + right detail drawer.
- [ ] Produce preview Variant B: cards + full-width detail panel.
- [ ] If needed, produce Variant C: expandable key rows.
- [ ] Capture desktop and 390px screenshots.
- [ ] Compare with user before final server deployment if the user wants to choose visually.

## Phase 6: Validation

Run from `cpa_key_policy_plus_plugin/go`:

```powershell
go test ./...
```

Run JS checks from repo root by extracting inline scripts or using existing helper pattern:

```powershell
node --check <extracted-admin-script.js>
node --check <extracted-user-script.js>
```

Run repo checks:

```powershell
git diff --check
```

Playwright checks:

- admin desktop layout: no horizontal body overflow, no short-field vertical text
- admin 390px layout: detail editor usable
- user page: login state, refresh recovery, session-expired message
- mocked delayed/failing endpoint: page does not stick in `同步中`
- newest-first event/protection ordering remains correct

## Phase 7: Server Deployment

- [ ] Check disk space.
- [ ] Back up:
  - current `cpa-key-policy-plus.so`
  - Plus SQLite DB
  - CPA config
  - Caddy/admin proxy config
- [ ] Build linux/amd64 `.so` and record SHA256.
- [ ] Upload only the new `.so` and proxy config if changed.
- [ ] Restart only necessary services.
- [ ] Verify:
  - `https://cpa-usage.konbakuyomu.us/` login with test key
  - user page realtime polling recovers after tab hidden/visible
  - `CPA Key Policy+` admin layout and lifecycle actions work
  - `cpa.konbakuyomu.us` still blocks management/plugin/resource paths
  - old Python `usage-admin` is not required by the public route

## Risk And Rollback

- Risk: DB migration adds columns incorrectly.
  - Rollback: restore Plus SQLite DB backup and previous `.so`.
- Risk: frontend auth accidentally allows archived keys.
  - Mitigation: backend tests for archived key rejection.
- Risk: public page session handling regresses.
  - Mitigation: tests for valid session, expired session, and stale request race.
- Risk: hard delete breaks historical usage joins.
  - Mitigation: prefer archive-first; reject hard delete when usage exists if implemented.

## Execution Evidence

- Implemented the confirmed lifecycle policy: Key Policy+ now supports
  archive/restore instead of hard delete. Archived keys are hidden by default,
  cannot log in to the user page, and cannot authenticate frontend requests.
- Migrated Plus SQLite schema with `keys.archived` and `keys.archived_at`, and
  preserved archive state across legacy state imports so old Key Policy sync
  cannot accidentally unarchive a Plus-managed key.
- Rebuilt the admin UI as a master/detail page:
  - key list shows safe preview, status, active sessions, RPM, request
    concurrency, Codex window limit, four quota windows, model count, and price
    coverage;
  - right detail panel edits basic policy, four quotas, model/price modal,
    soft reset, and archive/restore;
  - mobile layout keeps the dense table inside its scroll container and shows
    the detail editor as a full-width section.
- Stabilized the user self-service page refresh behavior:
  - auth errors return the user to login with `会话已过期`;
  - usage/CodexCont partial failures keep last good data and show section-level
    notices;
  - old refreshes are aborted/ignored, so manual refresh and foreground resume
    no longer leave the page stuck in `同步中`.
- Built linux/amd64 plugin artifact with WSL Go 1.22.6:
  - `cpa_key_policy_plus_plugin/dist/linux/amd64/cpa-key-policy-plus.so`
  - SHA256 `980d61ee7d2753bd9caa48036e804c02060e3a4515ed4b7af2d66ddfaa203b7c`
  - `file`: ELF 64-bit x86-64 shared object.
- SJC deployment:
  - backup path:
    `/opt/codex-stacks/backups/cpa-key-policy-plus-ux-fixes-20260702-225009`
  - uploaded only the new `.so`;
  - restarted only `cpa`;
  - did not pull images and did not run Docker prune.
- Production verification:
  - CPA logs show `plugin_id=cpa-key-policy-plus` loaded and registered from
    `/CLIProxyAPI/plugins/linux/amd64/cpa-key-policy-plus.so`;
  - remote plugin SHA256 matches local artifact;
  - admin API `/key-policy-plus/api/keys` returns `200`, `5` keys, and every
    key projection includes `archived` and `quota`;
  - admin API `/key-policy-plus/api/models` returns `200` with `7` models and
    no warnings;
  - archive/restore smoke on existing disabled smoke key succeeded and restored
    the key to its prior unarchived state;
  - Kuma test key login on `https://cpa-usage.konbakuyomu.us/` returns `200`;
    `/me`, `/usage`, `/events`, and `/codexcont` all return `200`;
  - public `https://cpa.konbakuyomu.us` still returns `404` for Plus resource,
    Plus API alias, management, and `usage-admin` paths;
  - root disk ended at about `344M` free after removing the single uploaded
    `/tmp` artifact.

## Validation Results

- `go test ./...` in `cpa_key_policy_plus_plugin/go`: passed.
- Inline JS syntax checks for `assets/admin.html` and `assets/user.html`:
  passed.
- `git diff --check`: passed, with only existing CRLF conversion warnings.
- Local Playwright preview:
  - admin desktop `1600x1000`: no page overflow, master/detail usable, archive
    toggle shows hidden rows, no raw `...` truncation;
  - admin mobile `390x900`: no page overflow, detail editor usable, dense table
    scroll contained;
  - user desktop and mobile: login works, partial `/codexcont` failure keeps
    app visible, recovery clears notice, mocked `401` returns to login.
- Production Playwright:
  - `https://cpa-usage.konbakuyomu.us/` desktop and `390px` mobile both log in
    with the Kuma test key;
  - metrics render, `思维链保护` tab switches, no page-level overflow;
  - manual refresh returns to `实时已连接` and does not remain stuck in
    `同步中`.
