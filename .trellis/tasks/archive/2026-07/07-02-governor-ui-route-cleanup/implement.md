# Governor UI route cleanup implementation plan

## Checklist

1. Load backend specs for Governor, CodexCont dashboard, and usage portal
   contracts.
2. Refactor Governor admin HTML into a CodexCont status dashboard:
   - read-only page,
   - snapshot fetches via `/governor/codexcont/admin/*`,
   - SSE with reconnect/foreground resume,
   - no Key/request/config tabs.
3. Refactor Governor user HTML into two tabs:
   - quota and recent request detail,
   - per-key protection status.
4. Hide the user resource from CPAMP sidebar menus while keeping the resource
   routable for `cpa-usage.konbakuyomu.us`.
5. Extend usage-event storage/projection with optional CPAMP realtime-style
   fields: requested/actual model, provider, executor type, reasoning effort,
   service tier, TTFT, and failure status code.
6. Extend user CodexCont API to return safe per-key protection summaries, or a
   safe empty projection when no matching summaries exist.
7. Adjust shared CSS to match CodexCont dashboard style and remove spinner /
   diagonal animation.
8. Rework user refresh behavior:
   - tab switches render cached state immediately,
   - manual refresh keeps a visible short sync state,
   - background polling pauses,
   - foreground resume fetches a fresh snapshot,
   - row signatures prevent no-op full table redraws.
9. Rework admin Governor refresh/SSE behavior:
   - snapshot fetches are abortable and time out,
   - hidden tabs close the SSE stream,
   - foreground resume forces a fresh snapshot and stream reconnect,
   - stale delayed processing follow-ups cannot keep the page stuck.
10. Update tests for menu hiding, schema migration, admin/user HTML shape,
   removed controls, user filtering, request-detail projection, and no-store
   behavior.
11. Run local checks:
   - `go test ./...` in `cpa_governor_plugin/go`
   - `.venv\\Scripts\\python.exe tests\\test_middleware.py`
   - `.venv\\Scripts\\python.exe tests\\test_cpa_usage_portal.py`
   - compileall for Python runtime files
   - `git diff --check`
12. Use Playwright CLI to validate local/static or test-served admin/user pages
   at desktop and 390px mobile widths, including immediate tab switching,
   two no-op polling cycles without table jitter, changed-row highlighting,
   and hung-refresh recovery for both admin and user pages.
13. Build linux/amd64 Governor plugin artifact and record SHA256.
14. Deploy to SJC:
    - check disk,
    - backup CPA plugin binary/config/Caddyfile/CodexCont route config,
    - upload self-owned artifact and Caddy route patch,
    - restart only necessary services.
15. Server smoke:
    - CPA health and authenticated model path,
    - CPAMP sidebar Governor page,
    - CPAMP sidebar no longer advertises `CPA Usage`,
    - dedicated user page login,
    - Kuma test key user page refresh remains recoverable after idle/manual
      refresh and current conversation traffic appears in realtime rows,
    - `/codexcont/` returns 404,
    - `/governor/codexcont/admin/*` works behind admin host,
    - public API domain blocks admin/plugin paths.
16. Update specs or task notes with learned contracts, commit, and archive.

## Risk points

- CPA plugin ResourceRoute is GET-only. Do not add POST-only user-resource
  behavior.
- CPAMP embedded pages may keep stale JS. Keep no-store and advise hard refresh
  only if validation shows old HTML still loaded.
- Admin SSE data path must remain admin-host-only; public exposure fails
  acceptance.
- The task must not switch `codexcont_route` or change the production execution
  path.

## Implementation Evidence

- Local Go tests passed in `cpa_governor_plugin/go`: `go test ./...`.
- Python regression passed: `.venv\Scripts\python.exe tests\test_middleware.py` with 163/163 checks and `.venv\Scripts\python.exe tests\test_cpa_usage_portal.py` with 84/84 checks.
- Python compile smoke passed: `.venv\Scripts\python.exe -m compileall middleware cpa_usage_portal run.py run_usage_portal.py`.
- `git diff --check` passed.
- Playwright CLI verified local preview pages:
  - `artifacts/governor-admin-desktop.png`
  - `artifacts/governor-admin-mobile.png`
  - `artifacts/governor-user-desktop.png`
  - `artifacts/governor-user-mobile.png`
- Linux plugin artifact built from WSL:
  - `cpa_governor_plugin/dist/linux/amd64/cpa-governor.so`
  - SHA256 `257228790455b9bc345bdf6d390a01b114482e8044e8c71251b3018e3a5e4538`
  - `file` reported ELF 64-bit x86-64 shared object.

## Server Deployment Evidence

- Pre-deploy SJC root disk was tight but usable: `9.6G` total, about `507M` free. No Docker prune, no image pull, and no container rebuild were used.
- Backup path: `/root/cpa-governor-ui-route-cleanup-backups/20260702-governor-ui-route-cleanup-130447`.
- Uploaded only the Governor `.so`; installed server SHA256 matches local artifact.
- Restarted only `cpa` and `cpa-admin-proxy`; reloaded `caddy-edge` config. Existing `codexcont`, `cpamp`, and `cpa-usage-portal` containers were not rebuilt.
- Admin proxy Caddy now returns `404` for `/codexcont/` and exposes admin-only `/governor/codexcont/admin/*` to CodexCont `/admin/*`.
- Public `cpa.konbakuyomu.us` blocker now includes `/governor*` alongside `/codexcont*` and plugin/admin paths.
- Server smoke results:
  - `http://127.0.0.1:8317/healthz`: `200`.
  - `http://127.0.0.1:8327/governor/`: `200`, contains `CodexCont 实时保护状态` and no old Key management controls.
  - `http://127.0.0.1:8327/governor-user/`: `200`, contains only `额度与明细` and `思维链保护` tabs.
  - `http://127.0.0.1:8327/codexcont/`: `404`.
  - `http://127.0.0.1:8327/governor/codexcont/admin/status`: `200`.
  - `http://127.0.0.1:8327/governor/codexcont/admin/requests?limit=2`: `200`.
  - `http://127.0.0.1:8327/governor/codexcont/admin/logs/stream?once=1`: `200`, emitted `ready` and `request` events.
  - Public `https://cpa.konbakuyomu.us/v0/resource/plugins/cpa-governor/admin`: `404`.
  - Public `https://cpa.konbakuyomu.us/codexcont/`: `404`.
  - Public `https://cpa.konbakuyomu.us/governor/`: `404`.
  - Public `https://cpa-usage.konbakuyomu.us/`: `200`, contains the two-tab user page.
- CPA logs after restart showed `plugin loaded plugin_id=cpa-governor` and `plugin registered plugin_id=cpa-governor`.
- User API smoke used a server-signed short-lived session without printing raw keys or secrets:
  - `/governor-user/api/me`: `200`, safe name/preview only.
  - `/governor-user/api/codexcont?limit=80` for `QQ专用`: `200`, returned only that key's CodexCont summaries.
  - `/governor-user/api/events?range=24h&limit=3`: `200`.
- Post-deploy disk remained about `495M` free.

## Animation Refinement Evidence

- User feedback after the first rollout: `CPA Usage` still felt stiff, and the
  top-right refresh button lacked clear realtime feedback.
- Added a refresh state model to the user page: `同步中` with `aria-busy`, then
  `刚刚更新` or `同步失败`; hand-triggered refresh keeps a short minimum visible
  syncing state so fast responses do not appear as no-op clicks.
- Added shared CSS hooks for smooth realtime behavior without reintroducing
  the old spinner or diagonal metric decoration:
  `syncSweep`, live topbar sweep, metric bump, changed row highlight, and
  content enter transition.
- Local validation after the refinement:
  - `go test ./...` in `cpa_governor_plugin/go`.
  - `.venv\Scripts\python.exe tests\test_middleware.py` with 163/163 checks.
  - `.venv\Scripts\python.exe tests\test_cpa_usage_portal.py` with 84/84 checks.
  - `.venv\Scripts\python.exe -m compileall middleware cpa_usage_portal run.py run_usage_portal.py`.
  - `git diff --check`.
- Playwright preview validation:
  - `artifacts/governor-user-animation-desktop.png`
  - `artifacts/governor-user-animation-mobile.png`
  - `artifacts/governor-user-refresh-syncing.png`
  - Refresh state sample confirmed `syncing` at 0 ms and 180 ms, then
    `just-updated` at 600 ms.
- Linux plugin artifact rebuilt after the animation refinement:
  - SHA256 `bd980ad3ea82e224ed6becfc1e1e9729eb9a7e7f539c3853494fe005362c1813`.
- SJC deployment evidence for the animation refinement:
  - Pre-deploy root disk remained tight: about `489M` free; no Docker prune,
    image pull, or container rebuild was used.
  - Backup path:
    `/root/cpa-governor-ui-route-cleanup-backups/20260702-governor-usage-animation-140408`.
  - Uploaded only `cpa-governor.so`, installed server SHA256 matched the local
    artifact, and restarted only `cpa`.
  - CPA logs showed `plugin loaded plugin_id=cpa-governor` and
    `plugin registered plugin_id=cpa-governor` after restart.
  - Server smoke: `http://127.0.0.1:8317/healthz` returned `200`;
    `http://127.0.0.1:8327/governor-user/` contained `sync-button`,
    `content-refreshing`, and `row-fresh`; public
    `https://cpa.konbakuyomu.us/governor/` returned `404`;
    public `https://cpa-usage.konbakuyomu.us/` contained the new animation
    hooks.
  - Post-deploy disk was about `477M` free, and `/tmp/cpa-governor.so` was
    removed explicitly.

## Sync Light Follow-up Evidence

- User feedback after the animation refinement: the right-top realtime/sync
  status light also needs to follow the same state as the refresh label and
  realtime chip.
- Fixed both custom Governor pages so the refresh button light keeps a live
  state class (`live-ok`, `live-info`, `live-warn`, or `live-bad`) driven by
  the same status function as the connection chip. When the temporary
  `刚刚更新` label resets to `刷新`, the light remains in the current live
  state instead of falling back to grey.
- Admin Governor page now also keeps a minimum visible `同步中` duration for
  manual/foreground snapshot refreshes, matching the user page and preventing
  fast local snapshots from making the click feel like a no-op.
- Local validation after the sync-light follow-up:
  - `go test ./...` in `cpa_governor_plugin/go`.
  - JavaScript syntax check by extracting `<script>` from `assets/user.html`
    and `assets/admin.html` and running `node --check --input-type=commonjs -`.
  - `.venv\Scripts\python.exe tests\test_middleware.py` with 163/163 checks.
  - `.venv\Scripts\python.exe tests\test_cpa_usage_portal.py` with 84/84
    checks.
  - `.venv\Scripts\python.exe -m compileall middleware cpa_usage_portal run.py
    run_usage_portal.py`.
  - `git diff --check` passed with only CRLF warnings.
- Playwright CLI validation:
  - Local preview user page showed `syncing live-info`, then
    `just-updated live-ok`, then settled on `刷新` with `live-ok`.
  - Local preview admin page showed `syncing live-info`, then
    `just-updated live-warn` while the preview SSE was reconnecting, then
    settled on `刷新` with the realtime error/reconnect light still active.
  - Mobile screenshots were captured for both user and admin pages:
    `artifacts/governor-user-sync-light-mobile.png` and
    `artifacts/governor-admin-sync-light-mobile.png`.
- Linux plugin artifact rebuilt after the sync-light follow-up:
  - SHA256 `6d8970fd0168efbb691bb8e322fcea380d4d0e311025a6d50b96c3a6c4bc82f1`.
  - `file` reported an ELF 64-bit x86-64 shared object.
- SJC deployment evidence for the sync-light follow-up:
  - Pre-deploy root disk remained tight: about `470M` free; no Docker prune,
    image pull, or container rebuild was used.
  - Backup path:
    `/root/cpa-governor-ui-route-cleanup-backups/20260702-sync-light-154445`.
  - Uploaded only `cpa-governor.so`, installed server SHA256 matched the local
    artifact, restarted only `cpa`, and removed the temporary upload file.
  - CPA logs showed `plugin loaded plugin_id=cpa-governor` and
    `plugin registered plugin_id=cpa-governor` after restart.
  - Server smoke: `http://127.0.0.1:8317/healthz` returned `200`;
    `http://127.0.0.1:8327/governor/` and `/governor-user/` contained the
    sync-light hooks; admin data channel `/governor/codexcont/admin/status`
    and `/requests?limit=2` returned `200`.
  - Public boundary stayed closed:
    `https://cpa.konbakuyomu.us/governor/`,
    `/v0/resource/plugins/cpa-governor/admin`, and `/codexcont/` all returned
    `404`; `https://cpa-usage.konbakuyomu.us/` returned `200`.
  - User API smoke with the Kuma test key succeeded without printing raw
    secrets: session ok, key name `kuma专用`, 24h usage returned calls and cost,
    and CodexCont summary returned 20 own-key records.
  - Production Playwright smoke on `https://cpa-usage.konbakuyomu.us/` logged
    in with the test key and verified 100 request rows plus the same
    `syncing live-info -> just-updated live-ok -> 刷新 live-ok` transition.
  - Post-deploy root disk was about `458M` free.
