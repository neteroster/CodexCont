# Governor CPAMP style alignment implementation plan

## Checklist

1. Load applicable Trellis specs before editing.
2. Start this task with `task.py start`.
3. Update shared Governor CSS:
   - flatten page background,
   - tune CPAMP-like colors/borders/shadows,
   - remove topbar sweep, refresh sweep, metric bump, and row broad highlight,
   - keep small status-dot pulse.
4. Review `admin.html` and `user.html` class usage and adjust only if the
   shared CSS cannot express the desired CPAMP-like layout.
5. Add a backend ordering helper for user CodexCont summaries and apply it to
   both live and fallback sources.
6. Add shared frontend helpers for protection ordering and non-stale
   `processing` active counts; use them in both admin and user pages.
7. Add/adjust Go tests:
   - visual CSS no longer contains sweep/bump hooks,
   - status-dot animation remains,
   - user CodexCont response is newest-first.
   - admin/user HTML derives active count from non-stale `processing` rows.
8. Run local validation:
   - `go test ./...` in `cpa_governor_plugin/go`,
   - extracted inline JS syntax check for `assets/user.html` and
     `assets/admin.html`,
   - `.venv\Scripts\python.exe tests\test_middleware.py`,
   - `.venv\Scripts\python.exe tests\test_cpa_usage_portal.py`,
   - `git diff --check`.
9. Playwright local preview:
   - desktop and 390px mobile for user/admin pages,
   - assert no sweep/bump classes produce visible animation hooks,
   - assert protection table first visible row is newer than the second row.
10. Build linux/amd64 `cpa-governor.so` and record SHA256.
11. Deploy to SJC:
    - check disk,
    - backup old plugin,
    - upload only `.so`,
    - restart only `cpa`,
    - remove temp upload.
12. Production smoke:
    - `cpa-usage` login with the known test key,
    - verify visual hook presence/absence via HTML and Playwright,
    - verify protection newest-first,
    - verify `活跃` equals current non-stale `processing` rows on admin and user
      pages,
    - verify CPAMP sidebar `CPA Governor`,
    - verify public API route boundaries.
13. Update task evidence/specs as needed, commit, archive.

## Risk Points

- CPAMP visual parity is subjective. Use the provided screenshots as the target:
  restrained dark panels, muted table headers, and no bright custom sweep.
- CodexCont live API may already order rows differently than local fallback.
  The Governor user API should normalize order after filtering.
- SJC disk is tight. Do not pull images or rebuild containers for this plugin
  asset-only change.

## Validation Evidence

- Started task with Trellis and loaded backend/guides specs before editing.
- Local tests:
  - `go test ./...` in `cpa_governor_plugin/go` passed.
  - Extracted `assets/user.html` and `assets/admin.html` scripts and ran
    `node --check --input-type=commonjs -` for both; both passed.
  - `.venv\Scripts\python.exe tests\test_middleware.py` passed `163/163`.
  - `.venv\Scripts\python.exe tests\test_cpa_usage_portal.py` passed `84/84`.
  - `.venv\Scripts\python.exe -m compileall middleware cpa_usage_portal run.py run_usage_portal.py` passed.
  - `git diff --check` passed, with only expected CRLF warnings.
- Playwright local preview:
  - Saved screenshots under ignored `artifacts/`:
    `governor-admin-desktop.png`, `governor-admin-mobile.png`,
    `governor-user-protection-desktop.png`,
    `governor-user-protection-mobile.png`.
  - Preview deliberately reported backend `active_requests=17` while the
    visible request list had one fresh `processing` row and one stale
    `processing` row; admin and user pages both displayed `活跃 1`.
  - DOM checks found no `syncSweep`, `liveSweep`, `metricBump`, `rowFresh`,
    `.sync-button::after`, `.topbar::after`, or `cards-updated` hooks.
  - DOM checks confirmed status dots still animate with `statusBlink`.
  - User `思维链保护` order was newest-first:
    `req-preview-live`, `req-preview-a`, `req-preview-stale`.
- Build:
  - Built `cpa-governor.so` on WSL with Go `1.22.6` for linux/amd64.
  - SHA256: `57a353a5f45cf22cb3d80bf666c1f39f5557f1cdf8c3cf28f0e0f4705d7a7887`.
  - `file` reported ELF 64-bit x86-64 shared object.
- Server deployment:
  - SJC root disk before deploy: `450M` free; after deploy: `433M` free.
  - Backed up old plugin to
    `/root/cpa-governor-cpamp-style-backups/20260702-165855/cpa-governor.so`
    with SHA256 `6d8970fd0168efbb691bb8e322fcea380d4d0e311025a6d50b96c3a6c4bc82f1`.
  - Uploaded only the new `.so`, restarted only `cpa`, and removed only the
    single temporary upload file `/tmp/cpa-governor-cpamp-style.so`.
  - CPA `/healthz` returned `{"status":"ok"}`.
  - CPA logs showed `plugin loaded` and `plugin registered` for
    `cpa-governor` from `/CLIProxyAPI/plugins/linux/amd64/cpa-governor.so`.
  - Public `https://cpa.konbakuyomu.us/governor/`,
    `/v0/resource/plugins/cpa-governor/admin`,
    `/v0/resource/plugins/cpa-governor/user`, and `/codexcont/` all returned
    `404`.
  - `https://cpa-usage.konbakuyomu.us/` API login with Kuma test key returned
    `200`, `/me` returned the expected safe preview, and `/codexcont?limit=20`
    returned 20 protection records with active count 0 at the time of smoke.
  - Server-local plugin HTML checks found no removed animation hooks and found
    `statusBlink`, `activeProcessingCount`, and `PROCESSING_STALE_MS` in both
    admin and user resources.
