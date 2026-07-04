# Implementation Plan

## Checklist

1. Start the Trellis task after these artifacts are written.
2. Inspect the existing Plus store/API payloads and tests.
3. Add or refine backend management handlers:
   - `/plugins/cpa-key-policy-plus/models`,
   - safe model normalization and key-union fallback,
   - transport tests for create/save/reset through management routes.
4. Rework `assets/admin.html`:
   - remove raw model/price textarea workflow,
   - add create dialog,
   - add model/price modal,
   - resolve API base to `/key-policy-plus/api`,
   - keep no-store and secret-safe behavior.
5. Add tests:
   - Go backend tests for model projection and management writes,
   - HTML static tests proving no mutating ResourceRoute fallback,
   - JS syntax check helper command.
6. Run local validation.
7. Build linux/amd64 plugin artifact only if local validation passes.
8. Prepare server rollout notes:
   - backup old `.so`, Plus SQLite, CPA config, admin proxy config,
   - add `/key-policy-plus/api/*` admin-proxy alias,
   - restart/reload only CPA/admin proxy as needed,
   - verify new key creation and public/admin route boundaries.

## Validation Commands

```powershell
go test ./...
node --check <extracted-admin-script.js>
git diff --check
```

If the repo-level Python tests are affected or touched, also run:

```powershell
.venv\Scripts\python.exe tests\test_middleware.py
.venv\Scripts\python.exe tests\test_cpa_usage_portal.py
.venv\Scripts\python.exe -m compileall middleware cpa_usage_portal run.py run_usage_portal.py
```

## Risk Points

- ResourceRoute remains GET-only; any accidental `POST`/`PUT` fallback to `/v0/resource/plugins/...` will recreate the original failure.
- Existing configured models must be preserved when the current discovery source returns an empty or stale list.
- The full generated key is unrecoverable after the create modal closes; do not imply it can be re-read.
- Do not leak management keys, raw user keys, full hashes, or auth-file details while adding model discovery.

## Rollback Points

- Before deployment: keep the previous plugin `.so` and admin proxy route file.
- If create/save fails after deploy: restore old `.so`, remove or bypass `/key-policy-plus/api/*` alias, restart CPA/admin proxy.
- If public routes expose admin/plugin resources: roll back Caddy/admin proxy change first, then investigate plugin behavior.

## Execution Notes

- Implemented Plus admin model catalog, safe model option normalization, structured create modal, and structured model/price editor.
- Fixed local preview body forwarding so Playwright can exercise real create/save requests.
- Added `/key-policy-plus/api/*` management alias support and server admin-proxy route. Production route injects the CPA management key from the existing mounted secret, not from a committed Caddyfile literal.
- Built linux/amd64 plugin with WSL Go 1.22.6. Deployed SHA256:
  `0cc73b598afd9ce109aa27d8bc0522c257dfe93d2f60b34aee481b5906c55b1d`.
- Server backup path:
  `/opt/codex-stacks/backups/cpa-key-policy-plus-admin-fixes-20260702-211634`.

## Validation Results

- `go test ./...` passed in `cpa_key_policy_plus_plugin/go`.
- Admin HTML inline script `node --check` passed.
- `tests/test_middleware.py`: 163/163 passed.
- `tests/test_cpa_usage_portal.py`: 84/84 passed.
- `python -m compileall middleware cpa_usage_portal run.py run_usage_portal.py` passed.
- Playwright local preview verified create, model selection, price save, and 390px layout without page-level horizontal overflow.
- Server verified:
  - CPA logs show `cpa-key-policy-plus` loaded and registered.
  - `/key-policy-plus/api/keys` and `/key-policy-plus/api/models` return `200`.
  - Disabled smoke key creation through `/key-policy-plus/api/keys/create` returns `200` and persists safe settings.
  - Public `cpa.konbakuyomu.us` returns `404` for Plus resource, management, API alias, and `usage-admin` paths.
  - Root disk ended at 368 MB free; no Docker prune or official image pull was used.
