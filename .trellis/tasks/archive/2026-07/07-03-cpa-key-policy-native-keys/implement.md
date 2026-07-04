# Implementation Plan

1. Add native-key fields, schema migration, and safe projections.
2. Add native CPA config parser, CPAMP alias SQLite reader, and sync routine.
3. Wire config fields and configure-time sync.
4. Replace boolean RPM/quota checks with structured `PolicyDecision`.
5. Enable deny-only model routing/executor responses for explicit 429 payloads.
6. Change user login hints from `cpa_...` to native `sk-...`.
7. Simplify admin UI to policy editing only and retire create/delete endpoints.
8. Add/update tests for sync, policy decisions, UI strings, and denial bodies.
9. Validate with:
   - `/mnt/d/Dev/20_Software/_LocalRuntime/go/go1.22.6-linux-amd64/go/bin/go test ./...`
   - `git diff --check`
   - `python ./.trellis/scripts/task.py validate .trellis/tasks/07-03-cpa-key-policy-native-keys`

## Rollout Notes

- Deploy Plus first with native sync paths configured but review disabled new
  rows before enabling user traffic.
- After deployment, verify cpa-usage login/usage/protection and a deliberately
  over-limit `/v1/responses` call.
- No official CPA/CPAMP binary changes are part of this task.

## Implementation Evidence

- Implemented native CPA key sync in `cpa_key_policy_plus_plugin/go`:
  top-level CPA `api-keys` are read from `native_keys_config_path`, CPAMP
  aliases are read from `cpamp_alias_db_path`, and Plus stores only safe
  hash/preview/alias/source metadata.
- Reworked Plus admin UI from key lifecycle management to key strategy editing:
  no create/delete/rotate/full-key-copy controls remain in the page.
- Added structured policy denial decisions for missing policy, removed source,
  disabled key, model allowlist, RPM, and 5H/24H/7D/month quota windows.
- Added deny-only model routing/executor responses so over-limit model calls
  return OpenAI-compatible JSON/SSE payloads with explicit
  `rate_limit_exceeded` details. Production validation showed the official CPA
  executor ABI does not let a plugin set the final HTTP status/header on
  `/v1/responses`, so the public response body is the reliable client-facing
  contract unless CPA core is changed.
- Updated user login guidance from legacy `cpa_...` keys to CPA native
  `sk-...` keys while keeping `/user/api/*` usage portal ownership in Plus.

## Rollout Evidence

- Built the production Plus plugin with the existing WSL Go toolchain:
  `go version go1.22.6 linux/amd64`.
- Artifact check: `file` reported an ELF 64-bit x86-64 shared object and
  `sha256sum` reported
  `ea4c84348545826548fe1b449afde9803ae1b5eebcc2b1391389b07426055b87`.
- Deployed to SJC at
  `/opt/codex-stacks/cpa/plugins/linux/amd64/cpa-key-policy-plus.so`; container
  path `/CLIProxyAPI/plugins/linux/amd64/cpa-key-policy-plus.so` reports the
  same SHA after `docker restart cpa`.
- CPA logs after restart show both `cpa-codexcont-executor` and
  `cpa-key-policy-plus` loaded and registered.
- Production config keeps Plus as the user portal owner and disables the old
  sidecar route:
  `codexcont_enabled: false`, `codexcont_route: false`,
  `native_keys_config_path: /CLIProxyAPI/config.yaml`,
  `cpamp_alias_db_path: /CLIProxyAPI/plugin-state/cpamp-usage.sqlite`.
- CPAMP alias DB contains 3 aliases. Plus DB contains 3 enabled
  `native_cpa` rows (`QQ的官key`, `kuma的官key`, `阿伟的官key`) and 1 new
  native row that remains disabled by default.
- Public health/boundary checks:
  `https://cpa.konbakuyomu.us/healthz` -> 200,
  `https://cpa-usage.konbakuyomu.us/` -> 200,
  public plugin/admin paths on `cpa.konbakuyomu.us` -> 404.
- User portal acceptance with an enabled native CPA key:
  `/v0/resource/plugins/cpa-key-policy-plus/user/api/session`,
  `/me`, `/usage?range=24h`, `/events?range=24h&limit=3`, and
  `/codexcont?limit=3` all returned 200; `/me` reported `source=native_cpa`.
- Normal API smoke with the same key:
  `/v1/models` -> 200 with 7 models;
  `/v1/responses` -> 200 and response JSON reported `status=completed`.
- Quota denial smoke temporarily set that key's 5H limit to `$0.00`, called
  `/v1/responses`, then restored the original `$60.00` limit. The client saw
  an OpenAI-compatible JSON error body:
  `type=rate_limit_exceeded`, `code=five_hour_quota_exceeded`, `param=5h`, and
  Chinese message `CPA Key Policy+ 已拦截：QQ的官key 触发 5小时费用限额，已用
  $0.00 / 上限 $0.00。`.

## Known ABI Limitation

- The original target asked for HTTP `429` plus `X-CPA-Policy-*` headers.
  Current official CPA executor response types expose only
  `Payload`, `Headers`, and `Metadata`, and the public `/v1/responses` path
  does not let a plugin set the final HTTP status. Production therefore returns
  HTTP 200 with a clear OpenAI-compatible error body. Achieving true HTTP 429
  would require a CPA core/ABI change, which is outside this task's constraint
  of not modifying official CPA/CPAMP.

## Validation Evidence

- `wsl -e /mnt/d/Dev/20_Software/_LocalRuntime/go/go1.22.6-linux-amd64/go/bin/go version`
  -> `go version go1.22.6 linux/amd64`.
- `wsl -e bash -lc 'cd /mnt/d/Dev/20_Software/23_Reference/llm-gateway/CodexCont/cpa_key_policy_plus_plugin/go && /mnt/d/Dev/20_Software/_LocalRuntime/go/go1.22.6-linux-amd64/go/bin/go test ./...'`
  -> pass for `codexcont/cpa-key-policy-plus-plugin` and
  `codexcont/cpa-key-policy-plus-plugin/internal/policyplus`.
- `git diff --check` -> pass.
- `python ./.trellis/scripts/task.py validate .trellis/tasks/07-03-cpa-key-policy-native-keys`
  -> pass.
- Production acceptance after redeploy:
  user portal APIs -> pass, normal `/v1/responses` -> pass, quota denial error
  body -> pass, true HTTP 429/header -> blocked by current CPA executor ABI.
