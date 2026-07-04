# Implementation Plan

1. Confirm production evidence without exposing secrets:
   - executor alias config for `gpt-5.5`;
   - current upstream failure body;
   - official key alias storage schema and Plus admin projection.
2. Implement executor upstream body compatibility:
   - add helper to rewrite model and filter unsupported built-in tools for the
     resolved upstream model;
   - use it in streaming and non-streaming executor paths;
   - add safe diagnostics for filtered tools.
3. Implement Plus alias-source fallback:
   - inspect the production alias source;
   - extend alias loader/tests to read that source while preserving existing
     `api_key_aliases` behavior.
4. Add tests:
   - executor request with `image_generation` and `gpt-5.5` alias to Spark;
   - no filtering for custom/function tools;
   - Plus alias fallback returns `alicea` for the corresponding native hash.
5. Validate locally:
   - `go test ./... -count=1 -timeout=120s` in
     `cpa_codexcont_executor_plugin/go`;
   - `go test ./... -count=1 -timeout=120s` in
     `cpa_key_policy_plus_plugin/go`;
   - `git diff --check`;
   - Trellis task validation.
6. Build Linux plugin artifacts with existing local WSL Go:
   - no Go download;
   - build only the changed `.so` files using `-tags cliproxy_plugin
     -buildmode=c-shared`;
   - record SHA256.
7. Deploy to SJC:
   - backup current plugin `.so`;
   - upload replacement(s);
   - restart only `cpa`;
   - verify plugins loaded.
8. Production acceptance:
   - Plus admin API shows exactly four native keys and includes `alicea`;
   - real `/v1/responses` with `model=gpt-5.5` and `image_generation` no longer
     fails with unsupported-tool error;
   - normal `gpt-5.5` still returns `OK`;
   - `cpa-usage` user APIs still return 200/ok;
   - public admin/resource/plugin paths remain 404.
9. Update spec with the tool-compatibility and alias-source contracts, commit,
   push, archive, and record journal.

## Implementation Evidence

- Local Go tests passed:
  - `go test ./... -count=1 -timeout=120s` in
    `cpa_codexcont_executor_plugin/go`.
  - `go test ./... -count=1 -timeout=120s` in
    `cpa_key_policy_plus_plugin/go`.
- WSL Go validation used the existing toolchain:
  `/mnt/d/Dev/20_Software/_LocalRuntime/go/go1.22.6-linux-amd64/go/bin/go`
  (`go1.22.6 linux/amd64`); no Go download was performed.
- Linux plugin artifacts were built with
  `CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -tags cliproxy_plugin
  -buildmode=c-shared`.
- Built artifact SHA256:
  - `cpa-codexcont-executor.so`:
    `af17e38f6b4a6fe8912c0f32cbc9f747d49db99545ce642bc31f08f783b07cd4`.
  - `cpa-key-policy-plus.so`:
    `f1e898032fdd7f0d3ed5f0f0d0ad14fe8a6fca79ead6561437ad7216904e2ec8`.

## Production Evidence

- Deployed both plugin artifacts to SJC and backed up previous binaries at
  `/opt/codex-stacks/backups/executor-alias-tool-fix-20260704-061152`.
- Fixed the alias source root cause by adding a read-only directory mount:
  `/opt/codex-stacks/cpamp/data:/CLIProxyAPI/cpamp-data:ro`, then changed Plus
  `cpamp_alias_db_path` to `/CLIProxyAPI/cpamp-data/usage.sqlite`.
- Config backup before the mount change:
  `/opt/codex-stacks/backups/cpamp-alias-wal-mount-20260704-061939`.
- Evidence for the alias bug:
  - CPAMP host DB had `usage.sqlite`, `usage.sqlite-shm`, and
    `usage.sqlite-wal`.
  - The old CPA file-level mount exposed `cpamp-usage.sqlite` with a stale /
    empty WAL view, so Plus saw three aliases but not the new `alicea`.
  - After the directory mount, Plus admin API returned exactly four current
    native keys and the `native_1bcd36fb_4180a3` row showed
    `name=alicea`, `alias=alicea`, and `preview=1bcd36fb...4180a3`.
- `/v1/responses` production smoke:
  - Internal non-stream `model=gpt-5.5` with `tools:[image_generation]`:
    HTTP `200`, visible model `gpt-5.5`, no unsupported-tool error.
  - Internal complete streaming `model=gpt-5.5` with
    `tools:[image_generation]`: HTTP `200`, `response.completed` observed,
    visible model `gpt-5.5`, no upstream model leak.
  - Public `https://cpa.konbakuyomu.us/v1/responses` non-stream smoke:
    HTTP `200`, visible model `gpt-5.5`, no unsupported-tool error.
- `cpa-usage` user API smoke with the `alicea` key succeeded through the
  GET-only resource API:
  `/session`, `/me`, `/usage?range=24h`, `/events?range=24h&limit=3`, and
  `/codexcont?limit=3` all returned `200/ok`; `/me` matched
  `native_1bcd36fb_4180a3`.
- Public boundary smoke:
  - `https://cpa.konbakuyomu.us/healthz`: `200`.
  - Public Plus/executor resource admin paths, `/codexcont/`, and
    `/management.html`: `404`.
  - `https://cpa-usage.konbakuyomu.us/`: `200`.

## Caution

The CPA compose file currently contains `pull_policy: always`. Recreating `cpa`
to apply the new mount pulled the official CPA image from `v7.2.49` to
`v7.2.50`. Smoke tests above passed after the pull, but future plugin-only or
mount-only deploys should avoid unintended image drift.
