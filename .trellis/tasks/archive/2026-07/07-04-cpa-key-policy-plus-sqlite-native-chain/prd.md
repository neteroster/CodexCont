# Fix CPA Key Policy Plus SQLite locks and native key mirror

## Goal

Make CPA Key Policy+ stable and operator-trustworthy again:

- The CPAMP Plus admin page must stop surfacing `database is locked (5) (SQLITE_BUSY)` during normal refresh/key-sync/usage activity.
- Plus must behave as a policy mirror for current official CPA native keys: the ordinary table shows only currently-present official keys with current aliases.
- Newly-created official native keys are enabled by default per user decision, while the UI makes missing RPM/quota limits obvious.
- Real Codex requests through the production CPA chain must succeed before handoff, not just internal admin APIs.

## Confirmed Evidence

- The previous task restored `/v1/responses` by replacing invalidated Codex OAuth auth, but did not harden Plus SQLite access.
- Plus `OpenStore`, CPAMP alias reads, executor-summary fallback reads, and legacy import reads currently use plain `sql.Open("sqlite", path)` without a busy timeout helper.
- Production `policyplus.sqlite` was observed with multiple file descriptors in one CPA process, consistent with SQLite lock contention risk under concurrent admin/API/usage work.
- Current Plus sync hides removed native keys from the default table but still keeps removed/history rows internally; ordinary UI must remain a current-official-key mirror.
- User selected `new official key = enabled immediately`.

## Requirements

- Add durable SQLite lock hardening for Plus-owned and Plus-read SQLite paths.
- Keep official CPA/CPAMP as the only raw-key and alias lifecycle owner.
- Re-sync native keys from CPA config and CPAMP aliases before Plus admin key listing and relevant strategy mutations.
- Default Plus admin key rows must be exactly current official native keys; deleted official keys must disappear from the ordinary table after refresh.
- Preserve historical usage, audit, and protection summaries internally without exposing stale rows in ordinary admin UX.
- Newly-created official native keys must be enabled immediately; if they have no RPM or quota limits, the admin UI must visibly indicate unlimited/missing limits.
- Do not modify official CPA/CPAMP source and do not expose raw `sk-...`, OAuth tokens, cookies, full hashes, request/response bodies, or encrypted reasoning.
- Production verification must include true `/v1/responses` with `gpt-5.5`, usage portal, CPAMP Plus admin APIs, and public-path blocking.

## Acceptance Criteria

- [x] Local Plus tests cover SQLite open discipline, busy-timeout behavior, native key mirror lifecycle, default-enabled new native keys, and admin UI missing-limit hints.
- [x] `go test ./...` passes in `cpa_key_policy_plus_plugin/go`; executor tests run if request-chain code/config is touched.
- [x] Linux/amd64 Plus plugin is built with the existing local Go runtime, deployed with a backup, and CPA is restarted once.
- [x] Production Plus admin key API returns 200 repeatedly without `SQLITE_BUSY` and mirrors the current official key count/aliases.
- [x] Official key add/delete/alias-change behavior is smoke-tested or directly verified against source-of-truth config/alias DB plus Plus API.
- [x] `https://cpa-usage.konbakuyomu.us/` still returns 200 and user APIs continue to work.
- [x] A real enabled native key request to `/v1/responses` with `model=gpt-5.5` succeeds; any auth failure is diagnosed from provider/auth availability without leaking secrets.
- [x] Public `cpa.konbakuyomu.us` admin/resource/plugin paths remain blocked.

## Notes

- SQLite fixes must be code-level durable fixes, not just a CPA restart.
- The ordinary UI mirrors current official keys; internal state can retain history for billing/debugging.

## Final Evidence

- Local toolchain: used existing WSL Go
  `/mnt/d/Dev/20_Software/_LocalRuntime/go/go1.22.6-linux-amd64/go/bin/go`,
  version `go1.22.6 linux/amd64`; no Go download was performed.
- Local validation passed:
  `go test ./... -count=1 -timeout=120s` in
  `cpa_key_policy_plus_plugin/go` and `cpa_codexcont_executor_plugin/go`,
  `git diff --check`, and
  `python ./.trellis/scripts/task.py validate .trellis/tasks/07-04-cpa-key-policy-plus-sqlite-native-chain`.
- Production artifact:
  `/opt/codex-stacks/cpa/plugins/linux/amd64/cpa-key-policy-plus.so`
  SHA256 `35c3179324883ebfcdbc7681e1304bb3bf3539fb62baa6579ba5e1cf49feab0e`.
- CPA logs after restart show both `cpa-codexcont-executor` and
  `cpa-key-policy-plus` loaded.
- Production Plus key API returned HTTP `200` repeatedly; CPA logs for the
  validation window contained no `SQLITE_BUSY` or `database is locked`.
- Native mirror verification:
  official CPA config count `4`, Plus ordinary key count `4`, preview sets
  matched exactly, no missing or extra Plus rows. Plus displayed names were
  `1bcd36fb...4180a3`, `kuma的官key`, `QQ的官key`, and `阿伟的官key`.
- Plus ordinary rows were all current native rows:
  enabled count `4`, `source_present` count `4`, no visible legacy or removed
  row.
- Public usage portal root returned HTTP `200`. User API smoke with a native
  key passed:
  `/session`, `/me`, `/usage?range=24h`,
  `/events?range=24h&limit=3`, and `/codexcont?limit=3` all returned HTTP
  `200` with `ok=true`.
- Real `/v1/responses` smoke through production returned HTTP `200`,
  `model=gpt-5.5`, no error code, and text `OK`.
- Over-limit smoke:
  temporarily set the unaliased smoke key `1bcd36fb...4180a3` 5H limit to
  `$0.00`; `/v1/responses` returned OpenAI-compatible error JSON with code
  `five_hour_quota_exceeded` and a Chinese message containing
  `5小时费用限额`; original policy was restored successfully.
- Public blocked paths all returned HTTP `404`:
  `/v0/resource/plugins/cpa-key-policy-plus/admin`,
  `/v0/resource/plugins/cpa-codexcont-executor/admin`,
  `/key-policy-plus/`, `/management.html`, and `/governor/`.
