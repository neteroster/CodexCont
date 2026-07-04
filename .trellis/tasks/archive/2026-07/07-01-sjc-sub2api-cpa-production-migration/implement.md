# Implementation Plan

## Preflight

- Verify SJC disk, Docker state, Caddy route, DNS for `cpa.konbakuyomu.us`, `1081`/`1082` egress ASN, and sub2api DB counts.
- Abort before pulling CPA if free disk space is below the SJC clean threshold unless the existing capacity guard allowlist recovers space.

## Backup

- Create a root-only backup directory under `/root/cpa-migration-backups/<timestamp>/`.
- Back up:
  - sub2api compose and data config files
  - Caddyfile
  - egress router configs
  - SQL dump of sub2api database
  - redacted migration manifest with account/API key counts and routing decisions

## CPA Deployment

- Create `/opt/codex-stacks/cpa` with `config.yaml`, `docker-compose.yaml`, `auths/`, and `logs/`.
- Generate CPA auth JSON from sub2api DB:
  - account `3` enabled with `proxy_url` set to `socks5://172.19.0.1:1082`
  - account `2` disabled if exported
  - error accounts skipped or disabled
- Copy existing active sub2api API keys into CPA `api-keys`.
- Start CPA and validate `http://127.0.0.1:8317/healthz`.

## Cutover

- Add or update Caddy `cpa.konbakuyomu.us` site block to reverse proxy CPA.
- Connect Caddy to `cpa_net` if needed.
- Reload Caddy and verify `https://cpa.konbakuyomu.us/healthz`.
- Run authenticated `/v1/models` and real `/v1/responses` smoke tests.
- Confirm response smoke coincides with `sub2api-egress-att` logs or an equivalent `1082` egress probe.

## sub2api Retirement

- Stop and remove only these explicit containers after CPA validation:
  - `sub2api-canary`
  - `sub2api-canary-postgres`
  - `sub2api-canary-redis`
- Do not delete old data directories in this task.
- Verify the egress router containers remain running.

## Final Verification

- Check public CPA health and authenticated data-plane.
- Check `docker ps` confirms CPA and egress routers running, sub2api app/Postgres/Redis not running.
- Check disk free space after migration.
- Record verification evidence in the task and report remaining manual client base URL change.

## Execution Evidence - 2026-07-01

- Root-only backup created at `/root/cpa-migration-backups/20260701T034218Z`.
- Backups include sub2api DB dump, sub2api compose/config, Caddyfile, egress router configs, and redacted migration manifests.
- CPA deployed at `/opt/codex-stacks/cpa` with container `cpa`, image `eceasy/cli-proxy-api:latest`; startup log reported `CLIProxyAPI Version: v7.2.47`, commit `00114be`, built `2026-06-29T11:00:58Z`.
- CPA auth migration: one enabled Codex auth from sub2api account `3`, `proxy_url=socks5://172.19.0.1:1082`; one disabled backup auth from account `2`; four active production API keys copied from sub2api group `openai-cpa-poc`.
- CPA initially could not reach `172.19.0.1:1082` from isolated `cpa_net`; confirmed `sub2api-egress-att` listens only on `172.19.0.1:1082`, then attached CPA to `sub2api_canary_net` as well as `cpa_net`. Final `sub2api_canary_net` members: `caddy-edge cpa`.
- Private checks passed: `http://127.0.0.1:8317/healthz` returned `200`; authenticated `/v1/models` returned seven models; authenticated `/v1/responses` returned `200` on `gpt-5.5`.
- Caddy now routes `cpa.konbakuyomu.us -> cpa:8317`; `https://cpa.konbakuyomu.us/healthz` returned `200`.
- Public authenticated data-plane passed: `/v1/models` returned seven models; `/v1/responses` returned `200`, model `gpt-5.5`, response text `cpa-public-ok`.
- Egress evidence after public response: `sub2api-egress-att` logged `172.19.0.6 -> chatgpt.com:443` matching `sub2api-att-residential[sub2api-att-whitelisted-ss]`.
- Old route retired: `https://sub2api.konbakuyomu.us/health` returns `410` with `migrated to https://cpa.konbakuyomu.us/`; Caddyfile no longer contains `reverse_proxy sub2api-canary:8080`.
- Removed only explicit containers: `sub2api-canary`, `sub2api-canary-postgres`, `sub2api-canary-redis`. Preserved `sub2api-egress-att` and `sub2api-egress-direct`.
- Final running relevant containers: `cpa`, `sub2api-egress-att`, `sub2api-egress-direct`.
- Final root disk snapshot: `/dev/sda1 9.6G`, used `8.5G`, available `1.1G`, `89%`.
- DNS note: SJC resolvers `1.1.1.1` and `8.8.8.8` resolve `cpa.konbakuyomu.us -> 38.59.246.182`; this Windows client still returned NXDOMAIN during verification, but `curl --resolve cpa.konbakuyomu.us:443:38.59.246.182` returned `{"status":"ok"}`.

## Closeout Lessons

- The production migration is complete for the gateway stack: CPA is the public endpoint, sub2api app/Postgres/Redis are stopped/removed, and the AT&T egress router remains in service.
- This migration does not by itself prove the 516 continuation problem is solved. It removes the old sub2api non-passthrough path and gives a cleaner base for the fix.
- The current CodexCont middleware is the executable behavior reference for 516 continuation: detect the `518 * n - 2` reasoning-token fingerprint, require replayable encrypted reasoning, discard tentative output from truncated rounds, and fold hidden upstream rounds into one downstream Responses stream.
- CPA's ordinary stream chunk plugin surface is insufficient for the full fix because it cannot own same-auth upstream continuation. A durable CPA implementation should live in the Codex executor or a new executor-level supervisor extension point.
- If avoiding a CPA fork is more important than a single-container production shape, keep CPA close to upstream and run CodexCont as a sidecar in front of CPA. If minimizing moving parts is more important, patch CPA natively and keep the patch small, tested, and regularly rebased.
