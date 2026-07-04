# CPA Key Policy Plus Admin Fixes

## Goal

Make `cpa-key-policy-plus` usable as the single admin surface for creating and maintaining `cpa_` user keys. The admin page must create keys reliably, save limits/prices/resets through the correct CPA management API path, and provide a model-selection experience comparable to the old Key Policy/CPAMP flow instead of hard-coded textarea editing.

## Confirmed Facts

- CPA plugin `ResourceRoute` is browser-navigable and GET-only in the current CPA host, so `POST`/`PUT` create/save/reset calls sent to `/v0/resource/plugins/...` cannot reach the plugin.
- `cpa-key-policy-plus` already registers management routes for key list/create/save/limits/reset and CodexCont config.
- The current Plus admin UI exposes model allowlists and prices through raw textarea/JSON fields, which makes model selection fragile and hides the registered CPA model list.
- CPA management exposes model-related sources through registered auth-file models and static model definitions; existing key configuration can provide a safe fallback union.

## Requirements

- Keep CPA, CPAMP, and the old Key Policy upstream source/images unchanged.
- Fix Plus admin writes by routing all mutating admin actions through a CPA management route or admin-proxy alias, not through GET-only resource routes.
- Preserve the HTML admin resource as a GET page, but make its API base choose the admin management alias first.
- Rework new-key creation so the admin can set name, enabled state, RPM, request concurrency, active Codex window limit, and initial allowed models.
- Show the generated full `cpa_` key exactly once after creation with clear copy guidance; do not store or display raw keys later.
- Replace raw model and price textareas with structured UI:
  - model count / price coverage in the main table,
  - searchable model selector,
  - select visible, clear, and selected-chip interactions,
  - per-model input/output/cache-read/cache-write prices in USD per 1M tokens.
- Discover models from CPA management data where possible and fall back to configured key models without deleting unknown existing entries.
- Never return or render OAuth tokens, management keys, raw API keys, full key hashes, Authorization headers, cookies, request bodies, response bodies, or encrypted reasoning content.

## Acceptance Criteria

- [ ] Creating a key from the Plus admin page succeeds and the new key appears after reload.
- [ ] Saving limits, model allowlists, prices, and reset watermarks succeeds through the management API path.
- [ ] The admin UI no longer depends on `POST`/`PUT` calls to `/v0/resource/plugins/cpa-key-policy-plus/...`.
- [ ] The model picker can search, select all visible, clear, preserve unknown configured models, and show price coverage.
- [ ] Existing keys with manually configured or no-longer-discovered models are not silently stripped on save.
- [ ] Go tests cover management writes, model normalization/fallback, and admin HTML transport expectations.
- [ ] JS syntax check, `go test ./...`, and `git diff --check` pass locally.

## Out Of Scope

- Switching the production `/v1/responses` execution chain.
- Changing CPA/CPAMP/old Key Policy upstream source or official images.
- Retiring `usage-admin` or redesigning the public `cpa-usage` user page in this task.
- Recomputing historical usage or changing CodexCont/Governor behavior.
