# CPA Key Policy Plus unified control

## Goal

Create a self-owned CPA plugin, `cpa-key-policy-plus`, that replaces the old
`cpa-key-policy` as the single authority for user `cpa_` keys, per-key limits,
quota resets, usage visibility, and pre-upstream request enforcement. The final
operator experience should remove the legacy `usage-admin` split and keep
ordinary user self-service only at `https://cpa-usage.konbakuyomu.us/`.

## Requirements

- Provide one administrator plugin page for every per-key setting: enabled
  state, name, model allowlist, model prices, RPM, request concurrency, Codex
  active window/session count, 5H/24H/7D/month USD limits, and soft reset.
- Preserve current `cpa_` keys by importing the existing Key Policy state by
  hash/name/preview/RPM/model/price/daily/weekly data; users must not need a
  new key solely because of this migration.
- Enforce limits before upstream model execution: disabled key, disallowed
  model, RPM, request concurrency, active Codex window count, and quota
  exhaustion must be rejected before model execution. During the migration
  window, the current public CodexCont sidecar may still receive the request
  first so the verified 516/518n-2 protection path is not lost; full
  before-CodexCont rejection waits for Governor executor-level folding.
- Implement Codex window limiting as active session tracking, not RPM:
  same key plus same extracted window/session identifier counts as one active
  window, refreshes last-seen time, and expires after 30 idle minutes.
- If a request lacks a usable window/session identifier, do not reject in v1;
  allow it under normal request-concurrency/quota checks and record a visible
  warning/audit signal.
- Keep 5H/24H/7D rolling windows and Asia/Shanghai natural-month limits.
  Reset must be soft watermarks and must not delete historical usage events.
- Keep CPA, CPAMP, and existing official/third-party images/source untouched.
  Own code may add a new plugin and adjust own Governor/Caddy deployment.
- `cpa-usage.konbakuyomu.us` remains the only ordinary-user self-service
  surface and must read the new plugin authority, not the legacy usage-admin
  sidecar.
- Old `cpa-admin.konbakuyomu.us/usage-admin/` must be retired after migration
  by returning 404 or redirecting to the new administrator plugin page.

## Acceptance Criteria

- [ ] `cpa-key-policy-plus` registers in CPA with admin and user resource
      routes, exclusive frontend auth, model routing/execution integration, and
      usage recording.
- [ ] Existing Key Policy keys import into Plus and can authenticate without
      re-issuing keys.
- [ ] Administrator UI can view/edit/save all per-key settings and reset any
      supported quota window for a selected key.
- [ ] User UI at `https://cpa-usage.konbakuyomu.us/` can log in with a valid
      `cpa_` key and only see that key's limits, usage, events, and CodexCont
      summaries.
- [ ] Disabled/disallowed/over-RPM/over-concurrency/over-window/over-quota
      requests are rejected before upstream execution in the deployed-safe
      migration mode.
- [ ] Session extraction follows the agreed priority:
      `X-Codex-Window-Id`, `client_metadata.x-codex-window-id`,
      `X-Codex-Turn-Metadata.window_id/prompt_cache_key`, body
      `prompt_cache_key`, `Session_id`/`X-Session-ID`, then
      `conversation_id`.
- [ ] Active Codex window counts expire after 30 idle minutes and repeated
      requests in the same session do not consume extra window slots.
- [ ] Missing window/session signals are allowed but audited and visible.
- [ ] Legacy `usage-admin` is no longer the place to set 5H/month limits.
- [ ] Public `cpa.konbakuyomu.us` does not expose plugin/admin/user resources.
- [ ] Public `/v1/responses` is not cut to CPA-first until Governor owns a
      verified executor-level continuation path; otherwise the current
      CodexCont sidecar route remains in place.
