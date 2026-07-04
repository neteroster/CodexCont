# CPA Usage and Key Policy Plus UX fixes

## Goal

Make `CPA Key Policy+` and the public `CPA Usage` page understandable, stable, and pleasant enough for day-to-day operation.

This task fixes the current rough edges without changing CPA, CPAMP, or any third-party plugin source/image. The source of truth stays our self-owned `cpa-key-policy-plus` plugin plus the existing admin/public proxy routing.

## User Value

- Admin can manage each `cpa_` key from one clear page without guessing what a cramped table cell means.
- Admin can see configured limits together with actual used/remaining quota before saving or resetting.
- Admin can remove unwanted keys through a safe lifecycle action.
- Ordinary users can open `https://cpa-usage.konbakuyomu.us/`, log in with their key, and see a stable realtime view instead of a stuck `连接异常 / 同步失败` state.
- Future UI changes can be previewed locally before production deploy.

## Confirmed Facts

- The current task exists at `.trellis/tasks/07-02-cpa-usage-key-policy-plus-ux-fixes/` and is still in `planning`.
- `cpa-usage.konbakuyomu.us` is routed by Caddy to CPA, not the old Python portal: `/` rewrites to `/v0/resource/plugins/cpa-key-policy-plus/user`, and `/v0/resource/plugins/cpa-key-policy-plus/user*` proxies to `cpa:8317`.
- The public user resource is reachable: `/v0/resource/plugins/cpa-key-policy-plus/user/api/me` returns `401 not_authenticated` without a session, and `/user/api/session` returns a structured invalid-key error when probed with a fake key. That points to a frontend/session/retry/data-state bug, not a dead domain.
- The old `cpa-usage-portal` container is still running on the server, but the current public Caddy route does not use it. This is a source of operational confusion.
- `CPA Key Policy+` admin UI is currently one wide inline-edit table. It mixes name, enabled state, RPM, request concurrency, Codex windows, four quota inputs, model/price summary, 24H usage, and reset buttons into each row.
- The `...` visible in the current admin screenshot is not a meaningful field; it is an overflow/truncation symptom from cramped columns.
- Admin key rows already receive backend usage windows from `usageWindows(...)`, but the UI only exposes a small `24H 用量` cell and does not clearly show 5H/24H/7D/month used/limit/remaining together.
- The store has hard deletion behavior only for stale legacy sync (`delete from keys where id=?`) but there is no user-facing admin route or UI for key deletion/archive.
- The user page already has stale `处理中` filtering logic and refresh abort logic, but the screenshot still shows a stuck `连接异常 / 同步失败` state; error handling and state recovery need to be made explicit and testable.

## Requirements

- Keep changes limited to self-owned code:
  - `cpa_key_policy_plus_plugin/go/**`
  - necessary admin/public proxy routing
  - Trellis/task docs and tests
- Do not modify CPA, CPAMP, or old `cpa-key-policy` official source/images.
- Diagnose and fix the public `cpa-usage` realtime failure mode:
  - user page must distinguish unauthenticated/session-expired from network/API failure
  - a failed poll must not leave the page permanently stuck in `同步中` or `连接异常`
  - manual refresh and page visibility restore must recover without full browser reload when the session is still valid
- Redesign `CPA Key Policy+` admin page around clear hierarchy rather than one huge inline table:
  - overview cards
  - key list
  - selected-key detail editor/drawer/panel
  - actual used/limit/remaining quota display for 5H/24H/7D/month
  - active session/window count
  - model/price coverage
- Preserve or improve existing create/save/model selection behavior:
  - creating a key still shows the full `cpa_` key exactly once
  - model list stays searchable and supports unknown model preservation
  - price editing remains structured by model
- Add safe key lifecycle management:
  - default should be `停用 + 归档/隐藏` so history and audit remain intact
  - hard delete, if provided, must be clearly dangerous and constrained
- Keep user-facing data safe:
  - never expose raw keys, full hashes, Authorization/cookie values, request/response bodies, or encrypted reasoning content
- Provide local preview variants before finalizing the UI:
  - at minimum, preview the recommended layout and one alternative layout with seeded data
  - screenshots should cover desktop and narrow/mobile widths

## Acceptance Criteria

- [ ] `CPA Key Policy+` admin no longer renders the single overcrowded inline table as the main editing surface.
- [ ] Admin can see for each key: name/preview/status, RPM, request concurrency, Codex window limit, active sessions, 5H/24H/7D/month used/limit/remaining, model count, and price coverage.
- [ ] Admin can edit a selected key without horizontal overflow or meaningless `...` cells on desktop.
- [ ] Admin can safely hide/archive or remove an unwanted key according to the chosen lifecycle decision.
- [ ] Public `cpa-usage` login and refresh states are testable and recover from failed polls without requiring a full page reload when the session remains valid.
- [ ] `cpa-usage` shows useful failure text for session-expired vs API/network failure.
- [ ] Existing model discovery, model selection, price editing, key creation, save, reset, usage events, and CodexCont summaries continue to work.
- [ ] Local preview(s) are runnable and screenshots are produced for desktop and 390px mobile.
- [ ] `go test ./...` passes in `cpa_key_policy_plus_plugin/go`.
- [ ] Inline JS syntax checks pass for changed HTML assets.
- [ ] Playwright verifies admin and user pages for layout, refresh recovery, and no short-field vertical text.
- [ ] Server deploy preserves public/admin boundaries: `cpa.konbakuyomu.us` still blocks plugin/resource/management paths; `cpa-usage.konbakuyomu.us` exposes only the user page/API.

## Out Of Scope

- Rewriting CPA, CPAMP, or official Key Policy.
- Reintroducing the old Python `usage-admin` as the source of truth.
- Changing the production `/v1/responses` execution architecture.
- Deleting production data or CPAMP/CPA history.
- Bulk filesystem cleanup or Docker prune.

## Open Questions

- Key lifecycle policy: should the first implementation expose only `停用 + 归档隐藏`, or also expose hard delete in an advanced/danger zone?
- UI shape: should the admin page use a right-side detail drawer, a full-width detail panel under the selected key, or a card/list layout? Recommendation is right-side detail drawer on desktop and stacked detail panel on mobile.
