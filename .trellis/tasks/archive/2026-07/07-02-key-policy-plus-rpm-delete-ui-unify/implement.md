# Implementation Plan

## Local Implementation

1. Update Plus backend:
   - Remove frontend-auth calls to active session and request concurrency checks.
   - Force create/save to persist `concurrency=0` and `max_active_sessions=0`.
   - Add `DeleteKey` store method and admin delete route.
   - Make archive route return 410 and remove it from management registration.
2. Update Plus admin UI:
   - Remove create/detail/list fields for request concurrency and Codex windows.
   - Remove show archived and archive/restore controls.
   - Add delete button with confirmation and `/keys/delete` call.
3. Update Governor backend:
   - Stop enforcing request concurrency in frontend auth.
   - Keep compatibility fields untouched unless required by tests.
4. Update UI shared style:
   - Align Plus and Governor `shared.css`.
   - Render protection result in user detail cards via `chip(req.protection)`.
5. Update tests:
   - Replace archive tests with delete tests.
   - Add create/save tests for forced-zero concurrency/session fields.
   - Add archive 410 compatibility test.
   - Add frontend HTML smoke assertions for removed labels and delete button.

## Validation

- `go test ./...` in `cpa_key_policy_plus_plugin/go`
- `go test ./...` in `cpa_governor_plugin/go`
- Extract `assets/admin.html` and `assets/user.html` scripts for `node --check`
- `git diff --check`
- Playwright:
  - Key Policy+ admin page desktop and 390px mobile
  - CPA Usage user page desktop and 390px mobile
  - CPA Governor admin page desktop
  - Confirm removed labels and unified protection chips

## Server Rollout

1. Check SJC disk free space.
2. Backup:
   - `/opt/codex-stacks/cpa/plugins/linux/amd64/cpa-key-policy-plus.so`
   - `/opt/codex-stacks/cpa/plugins/linux/amd64/cpa-governor.so` if changed
   - `/opt/codex-stacks/cpa/plugin-state/cpa-key-policy-plus/policyplus.sqlite`
   - CPA config and Caddy/admin proxy config.
3. Upload changed `.so` files, restart CPA only.
4. Verify plugin load logs and SHA256.
5. Delete production disabled/archived keys through the new delete API.
6. Verify Kuma test key login and a lightweight authenticated call.
7. Verify public blocked paths remain 404.

## Risks

- Hard deletion is intentionally irreversible without SQLite backup.
- Existing stale admin HTML may call archive route; 410 response prevents accidental stale archive behavior.

## Execution Notes

- Local tests passed:
  - `go test ./...` in `cpa_key_policy_plus_plugin/go`
  - `go test ./...` in `cpa_governor_plugin/go`
  - inline script syntax smoke for Plus/Governor `admin.html` and `user.html`
  - `git diff --check`
- Playwright preview checks passed for desktop and 390px mobile:
  - Key Policy+ admin has no retired concurrency/Codex-window/archive controls and has hard delete.
  - CPA Usage and CPA Governor protection details render protection result with the same `.chip` component.
  - Tables stay inside overflow panels; no page-level horizontal overflow.
- Linux amd64 artifacts deployed on SJC:
  - `cpa-key-policy-plus.so` SHA256 `a7a2b3cac09af1a019b37942f65a25e177b27f55e384c0a15bc93b2c62bfac37`
  - `cpa-governor.so` SHA256 `c0903c72d574fde4fa1f47185a0dbd9ec8ccc5579a533a8eeb8cf14c31d116dd`
- Production backup:
  - `/opt/codex-stacks/backups/key-policy-plus-rpm-delete-ui-unify-20260703-000536`
- Production cleanup:
  - Deleted disabled keys `key_5ce1c632...a19b5a` and `key_2bb8fd29...b016b1` through the new delete API.
  - Remaining Plus keys: 3 enabled, 0 disabled/archived.
- Production smoke:
  - CPA logs show both plugins loaded and registered after restart.
  - Kuma test key logs into `https://cpa-usage.konbakuyomu.us/`.
  - Kuma test key authenticates against `https://cpa.konbakuyomu.us/v1/models` and returns 7 models.
  - User usage/events/codexcont APIs return `ok`.
  - Public `https://cpa.konbakuyomu.us` returns 404 for plugin/admin/governor/codexcont paths.
