# Implementation Plan

1. Reuse the existing Governor plugin scaffolding as the base for a dedicated
   executor-only plugin, but remove user-portal ownership from the executor
   concept and naming.
2. Add a Go continuation package with the old Python sidecar behavior:
   truncation math, SSE parse/serialize helpers, request payload rebuild,
   usage aggregation, terminal reconstruction, and summary projection.
3. Wire `model.route` and `executor.execute_stream` so the route switch handles
   only streaming Responses requests and falls back cleanly when disabled.
4. Persist safe summaries in the executor store and add a read-only Plus
   summary bridge from `cpa-key-policy-plus` to the executor store path.
5. Add the executor CPAMP admin monitor resource for read-only rolling
   CodexCont protection summaries, replacing Governor's monitoring role
   without registering any user portal.
6. Update docs/specs to state that `cpa-usage.konbakuyomu.us` is Plus-owned and
   executor-only plugins replace only the Docker CodexCont sidecar.
7. Add focused Go tests for executor folding, executor monitor registration,
   and Plus summary degradation.
8. Run:
   - `go test ./...` in the executor plugin package.
   - `go test ./...` in `cpa_key_policy_plus_plugin/go`.
   - `git diff --check`.

## Risk Points

- Stream folding must emit exactly one terminal event and keep downstream
  sequence numbers monotonic.
- Tentative output from truncated rounds must never leak before a continuation
  decision.
- Summary bridging must fail soft so user quota and usage APIs are unaffected.
- The plugin must not accidentally register `cpa-usage` user resources.
- The CPAMP monitor must be read-only observability; key/quota management stays
  in Plus and ordinary user usage stays on `cpa-usage`.
- CPA/CPAMP currently requires resource-adjacent capabilities for plugin
  resource menu registration. Executor uses non-exclusive
  `frontend_auth_provider=true` and `usage_plugin=true` only as compatibility
  shims; `frontend_auth.authenticate` returns unauthenticated and
  `usage.handle` is no-op.

## Execution Evidence

- Added `cpa_codexcont_executor_plugin` as a separate executor-only CPA plugin.
- Added Go folding coverage for:
  - two-round auto continuation,
  - missing encrypted reasoning,
  - upstream EOF,
  - continuation open error,
  - `max_continue`,
  - monotonic sequence numbers and reconstructed metadata.
- Added plugin registration/route-switch tests confirming the executor plugin
  does not expose user resources or usage-portal ownership.
- Added a reconfigure regression: CPA calls `plugin.reconfigure` after the
  first load and still expects full plugin metadata/capabilities. Returning
  only `{"configured": true}` makes CPA mark the plugin unregistered and drops
  its CPAMP resource routes.
- Added upstream model alias support so a client-visible model can be mapped to
  a provider-registered internal model without changing downstream SSE or safe
  summaries. Current production alias:
  `gpt-5.4 -> gpt-5.3-codex-spark`.
- Added a host-stream parser regression for CPA line-sized SSE chunks without
  trailing newlines. This fixed the false `response.incomplete` result seen
  when the host callback returned chunks such as `event: response.created`.
- Added safe diagnostics to executor summaries: requested model, upstream
  model, rewritten body model, stream id presence, read counts, first/last read
  byte counts, and chunk status. Diagnostics do not include raw request bodies,
  raw response bodies, keys, or encrypted reasoning.
- Added Plus `codex_summary_db_path` read-only bridge and tests proving
  executor summaries are filtered by the current key.
- Validation:
  - `go test ./...` in `cpa_codexcont_executor_plugin/go`: passed.
  - `go test ./...` in `cpa_key_policy_plus_plugin/go`: passed.
  - `git diff --check`: passed with only CRLF conversion warnings.

## Local Build Toolchain Evidence

- Do not download Go again for WSL/Linux plugin builds unless both reusable
  toolchains are missing and the user explicitly approves a new install.
- Current WSL check:
  - plain `go` is not present in WSL `PATH`.
  - preferred reusable Go works:
    `/mnt/d/Dev/20_Software/_LocalRuntime/go/go1.22.6-linux-amd64/go/bin/go`
    -> `go version go1.22.6 linux/amd64`.
  - project-scoped fallback also works:
    `/mnt/d/Dev/20_Software/_LocalRuntime/CodexCont/go-sdk-1.22.6/bin/go`
    -> `go version go1.22.6 linux/amd64`.
  - cached tarball exists:
    `/mnt/d/Dev/20_Software/_LocalRuntime/go/downloads/go1.22.6.linux-amd64.tar.gz`.
  - `/tmp` currently has no `codex-go*` temporary Go directories.
- Future build/test commands should call the preferred Go path explicitly, for
  example:
  `/mnt/d/Dev/20_Software/_LocalRuntime/go/go1.22.6-linux-amd64/go/bin/go test ./...`.
- Follow-up validation used the preferred reusable Go path, not a WSL download:
  - `go test ./...` in `cpa_codexcont_executor_plugin/go`: passed.
  - `go test ./...` in `cpa_key_policy_plus_plugin/go`: passed.
  - `git diff --check`: passed with CRLF conversion warnings only.
  - `python ./.trellis/scripts/task.py validate .trellis/tasks/07-03-codexcont-executor-plugin`:
    passed.

## Rollout Checklist

1. Build linux/amd64 plugin artifacts for `cpa-codexcont-executor` and, when
   deploying the summary bridge, `cpa-key-policy-plus`; record SHA256 hashes.
2. Install the executor plugin into CPA with `route_enabled: false` first.
   This verifies plugin loading without changing live `/v1/responses` routing.
3. Confirm existing public contracts still work:
   - `https://cpa-usage.konbakuyomu.us/` login and usage APIs.
   - authenticated `https://cpa.konbakuyomu.us/v1/models`.
   - existing Codex request path still succeeds through the known-good route.
4. Enable executor routing only for a controlled smoke window, then test a real
   streaming `/v1/responses` request and inspect safe executor summaries.
5. After the CPA-first executor path is stable, remove the old Caddy special
   route to `codexcont:8787` and stop the Docker sidecar.

## Server Staging Evidence

- Built linux/amd64 artifacts with Go 1.22.6 in WSL and verified both are ELF
  x86-64 shared objects.
- Artifact hashes:
  - `cpa-codexcont-executor.so`:
    `041f06bc0c7c5ea7edc3082b86fd484364f7ee83e6411a8993663ab67b417951`
  - `cpa-key-policy-plus.so`:
    `9fc597a67b852e3ec212e6d3f6e4dc08d0ef6ee728df25fb4ac4b99e5aac75e8`
- SJC backup before plugin replacement:
  `/opt/codex-stacks/backups/cpa-codexcont-executor-20260703-105924`.
- Installed both artifacts into
  `/opt/codex-stacks/cpa/plugins/linux/amd64/`.
- Added executor config with `route_enabled: false` and Plus
  `codex_summary_db_path` bridge to the executor SQLite store.
- Restarted only the `cpa` container. `caddy-edge`, `codexcont`, `cpamp`,
  `cpa-admin-proxy`, and `cpa-usage-portal` kept running.
- Server validation:
  - local CPA `http://127.0.0.1:8317/healthz`: HTTP `200`.
  - public `https://cpa.konbakuyomu.us/healthz`: HTTP `200`.
  - CPA logs show `cpa-codexcont-executor` and `cpa-key-policy-plus` loaded
    and registered.
  - public executor resource path:
    `https://cpa.konbakuyomu.us/v0/resource/plugins/cpa-codexcont-executor/status`
    returned HTTP `404`.
  - public Plus admin resource path:
    `https://cpa.konbakuyomu.us/v0/resource/plugins/cpa-key-policy-plus/admin`
    returned HTTP `404`.
  - public old CodexCont dashboard path:
    `https://cpa.konbakuyomu.us/codexcont/` returned HTTP `404`.
  - public `https://cpa-usage.konbakuyomu.us/` returned HTTP `200`; HTML no
    longer contains `rangeSelect` or `<select>`.

## Server Monitor Staging Evidence

- User clarified executor should also replace Governor's CPAMP-side realtime
  rolling CodexCont monitor. Plan/docs/spec were updated so executor owns only
  the read-only monitor; Plus still owns `cpa-usage`, keys, quota, RPM, and
  user usage APIs.
- Added executor admin resource:
  `/v0/resource/plugins/cpa-codexcont-executor/admin`, with safe status and
  summaries APIs under `/admin/api/status` and `/admin/api/summaries`.
- Root cause found during staging: executor `plugin.reconfigure` returned only
  `{"configured": true}`. CPA v7.2.48 reuses the registration decoder for
  `plugin.reconfigure`, so later config refreshes logged
  `returned invalid metadata or no capabilities` and removed executor from the
  active plugin snapshot. Fix: both `plugin.register` and
  `plugin.reconfigure` now return the same full registration object.
- Final deployed executor artifact:
  `edd8dad11a3843672f802aee8412ae02a21421e7807dc569c509a7529875255a`.
- SJC backups before monitor replacements:
  `/opt/codex-stacks/backups/cpa-codexcont-executor-monitor-20260703-125630`,
  `/opt/codex-stacks/backups/cpa-codexcont-executor-monitor-20260703-131110`,
  and
  `/opt/codex-stacks/backups/cpa-codexcont-executor-monitor-20260703-132133`.
- Final staging validation:
  - local CPA `http://127.0.0.1:8317/healthz`: HTTP `200`.
  - local executor resource `/v0/resource/plugins/cpa-codexcont-executor/admin`:
    HTTP `200`, `Cache-Control: no-store`, HTML contains `实时滚动监控`.
  - local executor status API:
    `/v0/resource/plugins/cpa-codexcont-executor/admin/api/status`: HTTP `200`
    with `route_enabled:false`.
  - local executor summaries API:
    `/v0/resource/plugins/cpa-codexcont-executor/admin/api/summaries?limit=5`:
    HTTP `200`, currently empty summaries.
  - public executor resource:
    `https://cpa.konbakuyomu.us/v0/resource/plugins/cpa-codexcont-executor/admin`:
    HTTP `404`.
  - public CPA health: `https://cpa.konbakuyomu.us/healthz`: HTTP `200`.
  - public Plus usage page: `https://cpa-usage.konbakuyomu.us/`: HTTP `200`;
    HTML contains no `rangeSelect` and no `<select>`.

## Server Executor Smoke Evidence

- Root cause found during controlled executor smoke: the public/client model
  `gpt-5.4` was not the provider-registered upstream model on SJC. The executor
  now owns a plugin-only alias from `gpt-5.4` to `gpt-5.3-codex-spark`; official
  CPA/CPAMP code and Plus ownership were not changed.
- Root cause found after the first alias test: CPA host callbacks can deliver
  SSE as line-sized chunks without trailing newlines. The parser now accepts
  standalone `event:`, `data:`, comment, and blank line chunks, so a valid
  upstream stream no longer collapses into `response.incomplete`.
- Final deployed executor artifact on SJC:
  `/opt/codex-stacks/cpa/plugins/linux/amd64/cpa-codexcont-executor.so`
  with SHA256
  `499538733a37c77ff11b39bbb1818eaa1b748b87110988a4c97b1ed8997c1b81`.
- Current SJC executor config after smoke:
  - `enabled: true`.
  - `route_enabled: false`.
  - `upstream_model_aliases.gpt-5.4: gpt-5.3-codex-spark`.
  - status API reported `alias_count: 1`.
- Controlled internal smoke temporarily set `route_enabled: true` and sent a
  local streaming request to `http://127.0.0.1:8317/v1/responses`.
  Result:
  - HTTP `200`.
  - terminal event `response.completed`.
  - summary `protection=protected_clean`.
  - downstream stream and summary preserved `model=gpt-5.4`.
  - diagnostics showed upstream callback `model/body_model=gpt-5.3-codex-spark`.
- After smoke, `route_enabled` was restored to `false`. Public Caddy
  `/v1/responses` has not been switched away from the old Docker sidecar yet.
- Temporary Plus smoke key was deleted after validation; follow-up check showed
  the raw key temp file was absent.
- Final public-safety checks:
  - local CPA health: HTTP `200`.
  - public CPA health: HTTP `200`.
  - `https://cpa-usage.konbakuyomu.us/`: HTTP `200`.
  - usage page has no `rangeSelect`, no `<select>`, and contains `24 小时`.
  - public executor admin path:
    `https://cpa.konbakuyomu.us/v0/resource/plugins/cpa-codexcont-executor/admin`
    returned HTTP `404`.
  - old Docker `codexcont` sidecar is still running until public cutover is
    explicitly approved and verified.

## Current Remote State Recheck

- Read-only SJC check used SSH alias `sjc-snap`; the plain `sjc` alias is not
  configured in this Windows SSH profile.
- Current deployed artifact hashes:
  - `cpa-key-policy-plus.so`:
    `e261cfbe8777c43ec2d45ce76f4a094ae5656bdbb3ad33513c9ba2c0322b2369`.
    This is the AuthID usage-mapping fix artifact.
  - `cpa-codexcont-executor.so`:
    `499538733a37c77ff11b39bbb1818eaa1b748b87110988a4c97b1ed8997c1b81`.
- Current public Caddy route is still the safety rollback route:
  `reverse_proxy @responses codexcont:8787`, then ordinary fallback
  `reverse_proxy cpa:8317`. Therefore public `/v1/responses` still goes
  through the old Docker CodexCont sidecar first.
- Current CPA executor config has `route_enabled: true`, but public traffic is
  still bypassing it because Caddy special-cases `/v1/responses` to the
  sidecar.
- Current container state:
  - `cpa` running.
  - `caddy-edge` running.
  - `cpamp` healthy.
  - `codexcont` old sidecar still running.
  - `cpa-usage-portal` running.
- Current health/portal checks:
  - SJC root disk remains tight: about `1.1G` free, `90%` used.
  - local CPA health `http://127.0.0.1:8317/healthz`: HTTP `200`.
  - public `https://cpa-usage.konbakuyomu.us/`: HTTP `200`.
  - usage page HTML has no `rangeSelect`, no `<select>`, and contains
    `24 小时`.

## Production Cutover Evidence

- Fixed the duplicate CPAMP executor menu:
  - Root cause: the executor management route
    `/plugins/cpa-codexcont-executor/status` set
    `Menu: CodexCont Executor`, so CPAMP rendered it as a second sidebar page
    that displayed raw JSON.
  - Fix: only `/admin` resource sets the menu label; status/summaries remain
    internal management routes with no `Menu`.
  - Deployed executor artifact:
    `a4469e59dc2c6255ef7891711e8410d257615c9f5dae17e057c8c688c6096b86`.
  - Validation: local CPA
    `/v0/resource/plugins/cpa-codexcont-executor/admin` returned HTTP `200`
    and contained `实时滚动监控`; local resource
    `/v0/resource/plugins/cpa-codexcont-executor/status` returned HTTP `404`.
- Disabled the old `cpa-governor` plugin in
  `/opt/codex-stacks/cpa/config.yaml`:
  - `cpa-governor.enabled: false`.
  - After CPA restart, logs showed `cpa-codexcont-executor` and
    `cpa-key-policy-plus` registered, with no `cpa-governor` registration.
  - Validation: local
    `/v0/resource/plugins/cpa-governor/admin` returned HTTP `404`; public
    `https://cpa.konbakuyomu.us/v0/resource/plugins/cpa-governor/admin`
    returned HTTP `404`.
- Retired the old Governor plugin from the CPAMP installed-plugin surface:
  - Moved the single old artifact
    `/opt/codex-stacks/cpa/plugins/linux/amd64/cpa-governor.so` into
    `/opt/codex-stacks/backups/cpa-governor-retired-20260703-193559/`.
  - Removed only the `plugins.configs.cpa-governor` block from
    `/opt/codex-stacks/cpa/config.yaml`; the legacy Governor state DB remains
    available for Plus read-only import/audit paths.
  - Restarted `cpa` and `cpamp`.
  - Validation: CPAMP management plugin list now returns only
    `cpa-codexcont-executor` with `menus=['CodexCont Executor']` and
    `cpa-key-policy-plus` with `menus=['CPA Key Policy+']`; `cpa-governor`
    is absent from the installed list.
  - Validation: local executor admin resource stayed HTTP `200`, executor
    status resource stayed HTTP `404`, Governor admin stayed HTTP `404`,
    public CPA health stayed HTTP `200`, public usage portal stayed HTTP
    `200`, and public executor/Governor admin resources stayed HTTP `404`.
- Cut public Caddy `/v1/responses` to CPA-first:
  - Removed the special route
    `reverse_proxy @responses codexcont:8787`.
  - Current `cpa.konbakuyomu.us` site block falls through to
    `reverse_proxy cpa:8317`.
  - `docker exec caddy-edge caddy validate --config /etc/caddy/Caddyfile`:
    passed.
  - `docker exec caddy-edge caddy reload --config /etc/caddy/Caddyfile`:
    succeeded.
- Fixed Plus usage projection for executor aliases:
  - First CPA-first smoke proved usage rows were recorded, but model projected
    as internal `gpt-5.3-codex-spark`.
  - Plus now resolves records by `AuthID` first and maps known executor usage
    alias `gpt-5.3-codex-spark -> gpt-5.4` into `model` and
    `requested_model`, while preserving `actual_model`.
  - Final deployed Plus artifact:
    `fefa58c4a8bc6c99de55ba590807cbb2489af3b5e1b15cbfeb5c9df94883d381`.
- Production smoke before sidecar stop:
  - Public `https://cpa.konbakuyomu.us/v1/responses`: HTTP `200`.
  - Stream contained `response.completed`.
  - Stream did not contain `response.incomplete`.
  - Plus `usage_events` recorded the request for the temporary key.
  - Final corrected usage projection:
    `model=gpt-5.4`, `requested_model=gpt-5.4`,
    `actual_model=gpt-5.3-codex-spark`.
- User portal validation:
  - `https://cpa-usage.konbakuyomu.us/`: HTTP `200`.
  - User API with temporary key:
    `/session`, `/me`, `/usage?range=24h`, and
    `/events?range=24h&limit=100` all returned HTTP `200`.
  - `/events` showed the latest smoke row as `gpt-5.4` with internal model
    preserved separately.
- Old Docker sidecar stopped:
  - Ran `docker compose stop codexcont` in `/opt/codex-stacks/codexcont`.
  - `docker ps` no longer showed a running `codexcont` container.
  - `cpa`, `caddy-edge`, `cpamp`, and `cpa-usage-portal` stayed running.
- Old chain cleanup after executor cutover:
  - Created backup directory
    `/opt/codex-stacks/backups/old-codexcont-chain-cleanup-20260703-195045/`
    with the prior CPA config, admin Caddyfile, and old CodexCont compose
    file.
  - Removed the stopped Docker container `codexcont` with explicit
    `docker rm codexcont`.
  - Removed the old sidecar image `codexcont-codexcont:latest` with explicit
    `docker image rm codexcont-codexcont`; no Docker prune or bulk cleanup was
    used.
  - Renamed
    `/opt/codex-stacks/codexcont/docker-compose.yaml` to
    `docker-compose.yaml.retired-20260703-195045`, so a default
    `docker compose up` in that directory can no longer recreate the old
    sidecar.
  - Updated `/opt/codex-stacks/cpa/config.yaml` so Plus has
    `codexcont_enabled: false` and no longer carries the old
    `codexcont_url: http://codexcont:8787` line. The executor SQLite bridge
    remains the source for safe protection summaries.
  - Updated `/opt/codex-stacks/cpa-admin-tunnel/Caddyfile` so retired
    `/governor*`, `/governor-user*`, and `/codexcont*` admin paths return
    `404`; no route now reverse-proxies to `codexcont:8787` or
    `cpa-governor`.
  - `docker exec cpa-admin-proxy caddy validate --config
    /etc/caddy/Caddyfile`: passed.
  - Restarted `cpa`, `cpa-admin-proxy`, and `cpamp`.
  - Validation:
    - `docker ps -a` and `docker image ls` no longer show `codexcont`.
    - Live CPA config contains `codexcont_enabled: false` and no
      `codexcont_url`.
    - Live admin Caddyfile contains no `codexcont:8787`, `cpa-governor`, or
      `/governor/codexcont/admin` references.
    - Local/public CPA health and `https://cpa-usage.konbakuyomu.us/` all
      returned HTTP `200`.
    - Admin proxy `/governor/` and `/governor/codexcont/admin/status` returned
      HTTP `404`.
    - Executor admin resource remained HTTP `200`.
    - Public `/codexcont/`, `/governor/`, executor resource, and Governor
      resource all returned HTTP `404`.
    - CPAMP plugin list still shows only `cpa-codexcont-executor` with
      `menus=['CodexCont Executor']` and `cpa-key-policy-plus` with
      `menus=['CPA Key Policy+']`.
- Production smoke after sidecar stop:
  - Public `https://cpa.konbakuyomu.us/v1/responses`: HTTP `200`.
  - Stream contained `response.completed`.
  - Stream did not contain `response.incomplete`.
  - New Plus usage row:
    `model=gpt-5.4`, `requested_model=gpt-5.4`,
    `actual_model=gpt-5.3-codex-spark`.
- Temporary cutover key cleanup:
  - Deleted the temporary key row from Plus `keys`, plus related
    `reset_watermarks` and `active_sessions`.
  - Preserved `usage_events` history rows for audit evidence.
  - Removed explicit sensitive temp files:
    `/tmp/cpa-codexcont-cutover-key.txt` and
    `/tmp/cpa-codexcont-cutover-key-id.txt`.
