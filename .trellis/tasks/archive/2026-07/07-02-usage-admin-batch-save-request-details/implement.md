# Usage Admin Batch Save And Request Details Implementation Plan

## Evidence Already Collected

- `cpa_usage_portal/static/admin.html` currently renders `button data-save`
  per key and calls `saveLimits(keyId)`.
- `cpa_usage_portal/app.py` currently exposes only
  `PUT /admin/api/keys/{key_id}/limits` for limit updates.
- `cpa_usage_portal/static/dashboard.html` currently has a thin expanded
  request row without itemized pricing.
- `cpa_usage_portal/pricing.py` currently computes only total cost and the
  matched price model.
- Sub2API's usage types and billing service separate token and cost categories
  into input, output, cache creation, cache read, total, actual, service tier,
  reasoning effort, latency, and TTFT.

## Implementation Checklist

1. Add backend batch save route.
   - Add `admin_update_limits_batch`.
   - Route: `PUT /admin/api/keys/limits`.
   - Validate list shape, ids, numeric limits.
   - Save through `QuotaState.set_limits`.
   - Return refreshed safe key projections.

2. Refactor `usage-admin` frontend.
   - Remove per-row `保存`.
   - Add toolbar `保存全部`.
   - Track loaded snapshot and current input values.
   - Mark dirty state and disable save when clean.
   - Keep reset buttons per row.
   - Add saving/saved/error feedback.

3. Add admin all-key events.
   - Support `key_id=all` in `/admin/api/events`.
   - Fetch each enabled key's CPAMP events, attach safe key summary, merge,
     sort by timestamp descending, and cap by requested limit.
   - Update admin UI default select option to `全部 Key`.

4. Add pricing breakdown projection.
   - Add a function in `pricing.py` that returns both total cost and itemized
     breakdown from `ModelTokens` and `ModelPrice`.
   - Reuse the existing `service_tier_multiplier`.
   - Preserve existing `cost`, `cost_source`, and `price_model`.
   - Add `cost_breakdown` to priced events.

5. Extend safe event projection if needed.
   - Keep only safe scalar fields.
   - Include fields needed by breakdown that are already safe:
     `cached_tokens`, `cache_read_tokens`, `cache_creation_tokens`,
     `reasoning_tokens`, `total_tokens`, `service_tier`, `reasoning_effort`.
   - Do not expose raw CPAMP event payloads.

6. Add CodexCont key identity.
   - Add optional `AdminCfg.key_policy_state_path`.
   - Add a small read-only identity resolver that parses Key Policy state and
     hashes `Authorization` bearer values without retaining the raw key.
   - Pass safe identity into `Diagnostics.request_started`.
   - Render a `用户/Key` column in the CodexCont request table.

7. Redesign expanded details.
   - Update `dashboard.html` `detailRow`.
   - Add token composition, cost composition, and quota/accounting sections.
   - Keep failure detail compact and redacted.
   - Update `admin.html` event rendering to expose comparable detail or a
     compact operator version.

8. Unify visual style.
   - Remove `metric::after` crescent decorations from custom dashboard CSS.
   - Keep refresh/loading/live-state animations consistent.

9. Tests.
   - Batch save succeeds and persists all edited keys.
   - Batch save rejects malformed values/unknown ids.
   - Existing per-key route still works.
   - Admin all-key events include key summaries and never leak raw hashes.
   - CodexCont request summaries include safe key identity when configured.
   - Event pricing breakdown sums to displayed total.
   - Redaction still removes secret-like fields.
   - HTML smoke checks for `保存全部`, dirty state markers, and cost breakdown
     labels.

10. Validation.
   - `python -m compileall cpa_usage_portal run_usage_portal.py`
   - `python -m compileall middleware run.py`
   - `python tests/test_cpa_usage_portal.py`
   - `python tests/test_middleware.py`
   - `git status --short --branch`

## Files Likely To Change

- `cpa_usage_portal/app.py`
- `cpa_usage_portal/pricing.py`
- `cpa_usage_portal/redaction.py`
- `cpa_usage_portal/static/admin.html`
- `cpa_usage_portal/static/dashboard.html`
- `middleware/config.py`
- `middleware/diagnostics.py`
- `middleware/app.py`
- `middleware/dashboard.html`
- `tests/test_cpa_usage_portal.py`
- `tests/test_middleware.py`

## Risks And Rollback

- Pricing math risk: table total and breakdown must use the same backend
  calculation. Do not let the frontend recalculate totals independently.
- Partial save risk: batch route should fail visibly on invalid input before
  the UI claims success.
- Security risk: expanded details should not become a raw CPAMP event viewer.
- Rollback is simple because this affects only `cpa-usage-portal`; revert these
  files or redeploy the previous portal container.

## Implementation Notes 2026-07-02

- Added `PUT /admin/api/keys/limits` for all-key local 5H/month limit saves.
  Validation runs for the full payload before any `QuotaState` write.
- Added `key_id=all` to `/admin/api/events`; it queries enabled Key Policy
  records, attaches safe `key` summaries, merges newest-first, and keeps each
  row's own accounting/reset context.
- Added backend-owned `cost_breakdown` on priced events. The table `cost` is
  the same value as `cost_breakdown.costs.total`; frontends only render it.
- Added CodexCont `middleware/key_identity.py` plus optional
  `[admin] key_policy_state_path`. It hashes the bearer key for matching and
  stores only `known/name/id/preview/source/enabled`.
- Reworked `cpa_usage_portal/static/admin.html` to use a single global
  `保存全部`, all-key default request view, safe `用户/Key` column, and detailed
  token/cost/reasoning/accounting expansion.
- Reworked `cpa_usage_portal/static/dashboard.html` expansion to show the same
  useful breakdown sections for ordinary users.
- Added a CodexCont request table `用户/Key` column and removed the metric-card
  crescent decoration from both custom dashboards.
- Documented the optional `key_policy_state_path` in `config.toml`,
  `README.md`, and `README_zh.md`.

## Verification 2026-07-02

- `.venv/Scripts/python.exe -m compileall cpa_usage_portal run_usage_portal.py`
  passed.
- `.venv/Scripts/python.exe -m compileall middleware run.py` passed.
- `.venv/Scripts/python.exe tests/test_cpa_usage_portal.py` passed:
  79/79 checks.
- `.venv/Scripts/python.exe tests/test_middleware.py` passed:
  152/152 checks.
- Node inline-script syntax check passed for:
  `cpa_usage_portal/static/admin.html`,
  `cpa_usage_portal/static/dashboard.html`, and `middleware/dashboard.html`.
- `playwright-cli --version` confirmed the global Mise-managed CLI is available
  (`0.1.14`).
- `playwright-cli -s=codex run-code --filename=.../research/playwright-layout-check.js`
  passed for desktop and 390px mobile views of `usage-admin`,
  `usage-dashboard`, and `codexcont-dashboard`.
- Playwright initially caught `usage-admin/mobile` horizontal body overflow.
  The fix was to make the topbar brand flex child shrinkable (`min-width: 0`)
  and full-width on narrow screens; the same guard now covers all three custom
  pages.

## Deployment Notes 2026-07-02

- Server backup created at
  `/root/codex-backups/20260702-usage-admin-batch-save/self-owned-services-before-deploy.tgz`.
- Uploaded and rebuilt only `codexcont` and `cpa-usage-portal`; CPA, CPAMP, and
  CPA Key Policy official images/source were not modified.
- Server validation caught one deployment-only issue: CodexCont's
  `key_policy_state_path` must point to a container-visible mount, not the host
  `/opt/...` path. Fixed by mounting
  `/opt/codex-stacks/cpa/plugin-state:/data/plugin-state:ro` into `codexcont`
  and setting
  `key_policy_state_path = "/data/plugin-state/cpa-key-policy-state.json"`.
- Post-deploy validation:
  - `codexcont` and `cpa-usage-portal` containers are running.
  - `cpa-usage-portal /healthz` reports `key_policy_state=true` and
    `cpamp=true`.
  - CodexCont `/admin/requests` shows current requests with known
    Key Policy identity.
  - `cpa-admin` internal proxy renders `usage-admin` with `保存全部`,
    `全部 Key`, and `用户/Key`, and renders the CodexCont dashboard with
    `用户/Key`.
  - Public `cpa.konbakuyomu.us/admin/requests` and `/codexcont/` return `404`.
  - Public `cpa-usage.konbakuyomu.us/admin/api/keys` and `/usage-admin/`
    return `404`.
  - Root filesystem remained at about `667M` free after rebuild; no Docker
    prune or broad deletion was used.

## Cache Semantics Follow-up 2026-07-02

- User reported that the custom usage detail displayed `Cache Read = 0` and
  `Cache Write = 0` even though the main panel showed high cache hit behavior.
- CPAMP source confirms that its analytics API projects `cached_tokens` with a
  compatibility expression:
  `max(max(cached_tokens, cache_tokens) - cache_read_tokens -
  cache_creation_tokens, 0)`.
- CPAMP's monitoring UI uses `cached_tokens + cache_read_tokens` as cache-hit
  tokens for hit-rate display. Therefore OpenAI/Codex rows can correctly have
  large `cached_tokens` and zero fine-grained cache read/write fields.
- The portal fix keeps CPA, CPAMP, and CPA Key Policy untouched. It updates only
  our `cpa-usage-portal` pricing projection and static pages:
  - backend `cost_breakdown.tokens` now exposes `cpamp_cached_input`,
    `fine_grained_cache_read`, `fine_grained_cache_creation`,
    `effective_cache_read_for_hit_rate`, `cache_hit_rate`, and
    `cache_semantics`;
  - frontend labels now show `CPAMP 缓存命中` separately from
    `细粒度 Cache Read/Write`, with an inline note explaining the OpenAI/Codex
    zero-read case;
  - regression tests cover CPAMP-compatible cached rows and raw
    `cache_tokens` normalization without double counting.

## Verification Follow-up 2026-07-02

- `.venv/Scripts/python.exe -m compileall cpa_usage_portal run_usage_portal.py`
  passed.
- `.venv/Scripts/python.exe tests/test_cpa_usage_portal.py` passed:
  84/84 checks.
- `.venv/Scripts/python.exe -m compileall middleware run.py` passed.
- `.venv/Scripts/python.exe tests/test_middleware.py` passed:
  152/152 checks.
- HTML script extraction syntax check passed for `cpa_usage_portal` admin/user
  pages and `middleware/dashboard.html`.
- Playwright layout check passed for `usage-admin`, `usage-dashboard`, and
  `codexcont-dashboard` on desktop and 390px mobile. Playwright first caught a
  mobile clipped-chip risk in the CodexCont realtime status chip; it was fixed
  by giving chips a stable 32px min-height and explicit line-height.

## Deployment Follow-up 2026-07-02

- Pre-deploy SJC root disk: `9.6G` total, `8.9G` used, about `661M` free.
  `docker system df` showed build cache available, but no Docker prune or broad
  deletion was used.
- Backup created at
  `/root/codex-backups/20260702-cache-semantics-fix/self-owned-files-before-cache-fix.tgz`.
- Uploaded only self-owned files:
  - `/opt/codex-stacks/cpa-usage-portal/app/cpa_usage_portal/pricing.py`
  - `/opt/codex-stacks/cpa-usage-portal/app/cpa_usage_portal/static/admin.html`
  - `/opt/codex-stacks/cpa-usage-portal/app/cpa_usage_portal/static/dashboard.html`
  - `/opt/codex-stacks/codexcont/app/middleware/dashboard.html`
- Rebuilt/restarted only `cpa-usage-portal` and `codexcont`; CPA, CPAMP, and
  CPA Key Policy official images/source were not modified.
- Post-deploy validation:
  - `cpa-usage-portal` container health returned
    `{"ok": true, "key_policy_state": true, "cpamp": true}`.
  - `http://127.0.0.1:8327/usage-admin/` contained `CPAMP 缓存命中` and
    `细粒度 Cache Read`.
  - `http://127.0.0.1:8327/usage-admin/api/events?key_id=all&range=24h&limit=5`
    returned 5 events; one recent event projected
    `cpamp_cached_input=201216`, `fine_grained_cache_read=0`,
    `effective_cache_read_for_hit_rate=201216`,
    `cache_hit_rate≈0.9935`, and
    `cache_semantics=cpamp_compatible_cached_tokens`.
  - `codexcont` `/admin/healthz` returned `200`, and
    `/codexcont/requests?limit=5` returned request summaries with
    `key_identity`.
  - `https://cpa-usage.konbakuyomu.us/` returned `200` and contains the new
    cache labels; `https://cpa-admin.konbakuyomu.us/usage-admin/` returned
    Cloudflare Access `302`.
  - Public `https://cpa.konbakuyomu.us/admin/requests`,
    `/codexcont/`, and `/usage-admin/` stayed `404`; public
    `https://cpa-usage.konbakuyomu.us/admin/api/keys` and `/usage-admin/`
    stayed `404`.
  - Post-deploy root disk remained about `659M` free.
