# CPA Key Policy Plus implementation plan

1. Read backend specs for CodexCont/Governor/user portal and current plugin
   patterns.
2. Create `cpa_key_policy_plus_plugin/go` using the current Governor plugin
   patterns but with independent metadata, config, store, policy engine, and UI.
3. Implement key import from old Key Policy-compatible JSON and optional
   Governor local limits/resets.
4. Implement policy enforcement: key auth, model allowlist, RPM, quota windows,
   request concurrency, active session count, and missing-session audit.
5. Implement usage recording and cost breakdown with safe event projection.
6. Implement admin and user resources with compact CPAMP-like dark UI.
7. Wire user host compatibility so `cpa-usage.konbakuyomu.us` serves Plus user
   APIs instead of the old Governor/usage-admin source.
8. Keep the existing public `/v1/responses -> CodexCont sidecar -> CPA` route
   until Governor has a verified executor-level continuation supervisor. Do not
   switch Caddy to CPA-first in this task if that would bypass folding.
9. Adjust Governor only as needed so it no longer acts as the long-term key and
   quota authority.
10. Add tests for import, auth, limits, reset watermarks, session extraction,
   concurrency, user isolation, and UI resources.
11. Build linux/amd64 plugin artifact and prepare deployment notes/scripts with
    backups and rollback steps.

## Validation

- `go test ./...` in the new plugin module.
- Existing `go test ./...` in `cpa_governor_plugin/go`.
- `python tests/test_middleware.py`
- `python tests/test_cpa_usage_portal.py`
- `python -m compileall middleware cpa_usage_portal run.py run_usage_portal.py`
- `git diff --check`
- Playwright desktop/mobile smoke for Plus admin, Plus user, and Governor status.

## Production Checks

- Back up CPA config, old Key Policy state, Governor DB, Caddy/admin proxy, and
  old plugin binaries before cutover.
- Verify valid current `cpa_` keys import and authenticate.
- Verify an over-limit test key is rejected before CodexCont/upstream.
- Verify `usage-admin` no longer serves stale limit controls.
- Verify public API host still blocks admin/plugin/user paths.
- Verify the known-good CodexCont folding path still handles real
  `/v1/responses` traffic after Plus is enabled.

## Execution Evidence

- Local checks passed on 2026-07-02:
  - `go test ./...` in `cpa_key_policy_plus_plugin/go`.
  - `go test ./...` in `cpa_governor_plugin/go`.
  - `.venv\Scripts\python.exe tests\test_middleware.py` -> `163/163`.
  - `.venv\Scripts\python.exe tests\test_cpa_usage_portal.py` -> `84/84`.
  - `.venv\Scripts\python.exe -m compileall middleware cpa_usage_portal run.py run_usage_portal.py`.
  - JS syntax check for embedded `admin.html` and `user.html` script blocks.
  - `git diff --check`.
- Built artifact:
  - `cpa_key_policy_plus_plugin/dist/linux/amd64/cpa-key-policy-plus.so`
  - SHA256 `B46D205C9803A8A59CAC40B92AC89385D41AF1814E55CF05F76820CE14CBA730`.
- SJC backup before cutover:
  - `/opt/codex-stacks/backups/cpa-key-policy-plus-20260702-192608`.
- Production deployment evidence:
  - Remote plugin SHA256 matches the local artifact.
  - Root disk before/after verification remained tight but usable: `/dev/sda1`
    about `9.6G`, `9.2G` used, about `384M` available. No Docker prune or
    image pull was used.
  - `cpa` was restarted once; `caddy-edge` was reloaded; `cpa-admin-proxy`
    was restarted because its Caddy admin API was not listening for reload.
  - CPA logs show `plugin_id=cpa-key-policy-plus` loaded from
    `/CLIProxyAPI/plugins/linux/amd64/cpa-key-policy-plus.so` at the cutover
    time, along with Governor. Old `cpa-key-policy` remains on disk but is
    disabled in config and was not loaded in the post-cutover log block.
  - Plus SQLite exists at
    `/opt/codex-stacks/cpa/plugin-state/cpa-key-policy-plus/policyplus.sqlite`
    with imported rows: `keys=3`, `usage_events=4`,
    `reset_watermarks=8`, `audit_log=28`.
  - `https://cpa-usage.konbakuyomu.us/` returns `200` and `Cache-Control:
    no-store`.
  - Public blocked paths return `404`:
    `https://cpa.konbakuyomu.us/v0/resource/plugins/cpa-key-policy-plus/user`,
    `/usage-admin/`, and `/codexcont/`.
  - Admin-proxy backend routes: `/key-policy-plus/` returns `200`,
    `/key-policy-plus/api/keys` returns `3` keys, and legacy
    `/usage-admin/` plus `/codexcont/` return `404`.
  - Kuma test key login on `cpa-usage` returns `200`, identifies the safe
    preview/name, and `/user/api/usage`, `/user/api/events`, and
    `/user/api/codexcont` all return `200`.
  - Authenticated `/v1/models` returns `200` and `7` models.
  - A tiny authenticated `/v1/responses` request with `gpt-5.4-mini` returns
    `200`, `status=completed`, proving the existing public sidecar route still
    works after Plus is enabled.
  - Direct CPA fake-model rejection currently surfaces as CPA's generic
    `401 Missing API key` because Plus `frontendAuth` returns
    unauthenticated for policy denial. This is accepted as the current CPA
    wrapper, not as a user-facing ideal error shape.
- Playwright production smoke:
  - `cpa-usage` opens as `CPA 用量自助页`.
  - Kuma test key login renders the dashboard with realtime chip, active
    count, limits, usage, tokens, cache, and reasoning metrics.
  - `思维链保护` tab switches successfully and shows newest-first rows
    (`2026/7/2 19:43:56` above `2026/7/2 19:43:47` in the smoke run).
  - At `390px` viewport, page width remains `390`; dense table overflow is
    contained inside `.table-wrap`.

## Cutover Note

This task intentionally did not switch public `/v1/responses` to CPA-first.
The verified production folding owner is still the Python CodexCont sidecar.
Governor/Plus can become the front-of-CodexCont hard gate only after an
executor-level continuation supervisor is implemented and tested with real
folding traffic.
