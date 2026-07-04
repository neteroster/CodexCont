# CPA Key Management And Usage Portal Implementation Plan

## Phase 1: Local Implementation

1. Add a `cpa_usage_portal` Python package with:
   - Key Policy state loader and hash normalization.
   - Signed session cookie helpers.
   - CPAMP client abstraction.
   - Redaction and safe event projection.
   - Budget suggestion helper.
   - Retention helper for CPAMP SQLite.
   - Starlette app and static Chinese dashboard.
2. Add focused tests for:
   - `sha256:` vs bare-hex normalization.
   - API key validation and unknown key rejection.
   - HttpOnly session cookie behavior.
   - CPAMP requests always include the current key hash.
   - Secret redaction and safe event projection.
   - Budget suggestion edge cases.
   - Retention deleting old rows in batches without `VACUUM`.
3. Update README files with short operational notes.

## Phase 2: Server Preflight

1. Record `df -h /`, `docker system df`, and current container list.
2. Back up root-only copies of:
   - `/opt/codex-stacks/cpa/docker-compose.yaml`
   - `/opt/codex-stacks/cpa/config.yaml`
   - `/opt/codex-stacks/cpa-admin-tunnel/Caddyfile`
   - `/opt/codex-stacks/caddy/Caddyfile`
   - Key Policy state and CPAMP data paths if already present.
3. Do not use Docker prune or broad deletion. If free space is insufficient for
   image pulls, pause for an explicit per-image cleanup decision.

## Phase 3: CPAMP And Key Policy

1. Create `/opt/codex-stacks/cpamp` and start CPAMP on `cpa_net`.
2. Enable CPA usage statistics and persistent plugin mounts.
3. Install `cpa-key-policy v0.2.1` from the official release and verify checksum.
4. Restart only `cpa`; verify `/healthz`, `/v1/models`, and CodexCont
   `/v1/responses`.
5. Route `cpa-admin.konbakuyomu.us/cpamp/` through the admin proxy.

## Phase 4: User Portal

1. Create `/opt/codex-stacks/cpa-usage-portal`.
2. Deploy the portal container on `cpa_net`.
3. Add `cpa-usage.konbakuyomu.us` routing.
4. Verify login with a test Key Policy key and confirm only that key's events
   are visible.

## Phase 5: Acceptance And Closeout

1. Verify Key Policy:
   - Allowed model succeeds.
   - Disallowed model fails.
   - Low-limit/RPM test key is constrained.
2. Verify CPAMP receives real requests.
3. Verify CodexCont dashboard and 516 protection still show current requests.
4. Verify public API domain still blocks management/plugin/admin paths.
5. Record disk/database/log sizes.
6. Update this task with deployment evidence, then commit.

## Current Evidence

- Existing production CPA public base: `https://cpa.konbakuyomu.us/`.
- Existing admin base: `https://cpa-admin.konbakuyomu.us/`.
- Existing CodexCont admin path: `/codexcont/`.
- Existing SJC disk is known to be tight; live preflight is required before any
  server-side image pull.

## Deployment Evidence 2026-07-01

- Root-only backup path: `/root/cpa-key-management-backups/20260701-193204`.
- Server disk after rollout: `/dev/sda1` size `9.6G`, used `8.8G`, available
  `771M`, use `93%`.
- `docker system df` before the final portal rebuild showed images `2.227GB`,
  local volumes `53.64MB`, build cache `234.8MB`; no Docker prune was used.
- CPAMP is deployed at `/opt/codex-stacks/cpamp`, container `cpamp`, and
  `http://127.0.0.1:8327/health` returned `200`.
- CPA config has `usage-statistics-enabled: true`, `plugins.enabled: true`, and
  `cpa-key-policy.enabled: true` with state file
  `/CLIProxyAPI/plugin-state/cpa-key-policy-state.json`.
- Key Policy state path on the host:
  `/opt/codex-stacks/cpa/plugin-state/cpa-key-policy-state.json`; current safe
  status is `key_count=1`.
- A Key Policy smoke key successfully called
  `https://cpa.konbakuyomu.us/v1/responses`: HTTP `200`, status `completed`.
- A disallowed model using the smoke key was rejected with HTTP `401`.
- Public `https://cpa.konbakuyomu.us` returned `404` for `/management.html`,
  `/v0/management/config`, `/v0/resource/plugins/cpa-key-policy`,
  `/admin/requests`, `/codexcont/`, and `/cpamp/`.
- CodexCont admin proxy route remains healthy:
  `http://127.0.0.1:8327/codexcont/healthz` and `/codexcont/requests?limit=1`
  returned `200`.
- CPAMP SQLite sizes after rollout: `usage.sqlite` `236K`, WAL `3.3M`, SHM
  `32K`.
- User portal is deployed at `/opt/codex-stacks/cpa-usage-portal`, container
  `cpa-usage-portal`, image `cpa-usage-portal:latest`.
- Portal route was verified through local host resolution for
  `https://cpa-usage.konbakuyomu.us`: `/healthz` `200`, login `200`,
  `/api/events` returned `2` own events, `/api/usage` returned `2` calls and
  `0` failures.
- `/api/usage` no longer exposes either the raw-key hash or the Key Policy id
  hash; it returns only safe stat fields plus a preview.
- Live CPAMP comparison confirmed the important hash contract:
  `sha256("sjc-smoke-20260701")` returned `calls=1 events=1`, while
  `sha256(raw smoke key)` returned `calls=0 events=0`. The portal now validates
  login with raw-key hash but filters CPAMP with the Key Policy id hash.
- Local validation after the hash correction:
  `.venv\Scripts\python.exe tests\test_cpa_usage_portal.py` -> `32/32`;
  `.venv\Scripts\python.exe tests\test_middleware.py` -> `143/143`;
  `.venv\Scripts\python.exe -m compileall cpa_usage_portal run_usage_portal.py`
  completed.

## Remaining Operational Note

- `cpa-usage.konbakuyomu.us` DNS was later added by the user. Server-side
  public health verification returned `200`.
- RPM/daily-limit enforcement was not stress-tested with many live requests to
  avoid unnecessary spend. Model allowlist rejection and normal Key Policy
  authentication were verified.

## Closeout Decisions

- Ordinary users should use Key Policy `cpa_...` keys for both Codex requests
  and the user usage portal. Native CPA `sk...` keys are compatibility/admin
  escape hatches, not the self-service user identity.
- The user portal intentionally rejects native `sk...` keys with
  `invalid_api_key` unless they are explicitly migrated into Key Policy.
- `https://cpa-admin.konbakuyomu.us/management.html` points to CPAMP, so its
  login key is the CPAMP admin key. CPA management key is a separate secret for
  CPA-native management calls.
- There is no one-to-one binding between native `sk...` keys and Key Policy
  `cpa_...` keys in the current design. Adding such a bridge would need a
  separate mapping layer and bypass-risk review.
- CPA, CPAMP, and CPA Key Policy were not source-modified. They are deployed as
  official image/plugin artifacts plus config, volumes, and Caddy routing.
- `cpa-usage-portal` and CodexCont are the custom-maintained sidecars. All
  production components are separate containers/stacks so CPA/CPAMP/Key Policy
  can be updated independently from custom code.
