# CPA CodexCont Sidecar and Admin Tunnel

## Goal

Run CodexCont as a server-side continuation middleware in front of CPA for `/v1/responses`, keep CPA on the upstream official image, and expose CPA's management panel only through a Cloudflare Tunnel protected by Cloudflare Access and a CPA management key.

## Confirmed Facts

- Production CPA currently runs on SJC at `/opt/codex-stacks/cpa`, container `cpa`, image `eceasy/cli-proxy-api:latest`, bound as `127.0.0.1:8317:8317`.
- `cpa` is on `cpa_net` and `sub2api_canary_net`; the `sub2api-egress-att` route remains required for `socks5://172.19.0.1:1082`.
- CPA management endpoints and `/management.html` currently return `404`, because `remote-management.secret-key` is not enabled.
- SJC root disk is small: about `9.6G` total, `8.5G` used, `1.1G` available during planning.
- The host Python is `3.10`, while CodexCont requires Python `>=3.12`; the production sidecar should therefore run in a Python 3.12 container.
- No existing `cloudflared` binary or tunnel container was found on SJC.
- Local `codex-candy-eval` uses the `codex` CLI. To avoid touching the user's daily local Codex config, evaluation must use an isolated `CODEX_HOME` or command-line config overrides.

## Requirements

- R1: Create `/opt/codex-stacks/codexcont` and run CodexCont as a restartable service/container on SJC.
- R2: Route only `cpa.konbakuyomu.us/v1/responses` through CodexCont; keep other CPA API routes direct to CPA.
- R3: Configure CodexCont upstream as `http://cpa:8317/v1/responses`, auth mode `passthrough`, continuation enabled.
- R4: Explicitly block public access on `cpa.konbakuyomu.us` to `/management.html`, `/v0/management*`, and `/v0/resource/plugins/*`.
- R5: Enable CPA management with a strong generated management key while keeping `remote-management.allow-remote: false`.
- R6: Prepare a Cloudflare Tunnel stack for `cpa-admin.konbakuyomu.us` using a Dashboard Token, stored only in a root-only server path.
- R7: The Cloudflare Access policy for `cpa-admin.konbakuyomu.us` must restrict access to the user's Cloudflare identity before the endpoint is considered production-ready.
- R8: Do not print OAuth tokens, API keys, Cloudflare Tunnel tokens, or management keys into terminal output, Git, Obsidian, or task artifacts.
- R9: Do not run Docker prune or recursive/bulk deletion; if disk space becomes insufficient, pause before cleanup.
- R10: Record candy-eval results honestly; do not claim the 516 class is completely eliminated unless live evidence supports it.

## Acceptance Criteria

- [x] Trellis artifacts record requirements, design, implementation steps, backups, evidence, and residual risks.
- [x] CodexCont is running on SJC and can reach CPA through `cpa_net`.
- [x] `https://cpa.konbakuyomu.us/healthz` succeeds.
- [x] Authenticated `https://cpa.konbakuyomu.us/v1/models` succeeds through CPA.
- [x] Authenticated `https://cpa.konbakuyomu.us/v1/responses` succeeds and CodexCont logs show it handled the request.
- [x] Public `https://cpa.konbakuyomu.us/management.html` and `/v0/management/config` are blocked.
- [x] `https://cpa-admin.konbakuyomu.us/management.html` is reachable only through Cloudflare Access and then CPA management key authentication.
- [x] A `codex-candy-eval` run using isolated local test config points at `https://cpa.konbakuyomu.us/v1` without changing `C:\Users\dxt98\.codex`.
- [x] Candy-eval result and CodexCont fold logs are recorded, including any residual 516-class failure.

## Out of Scope

- Forking or patching CPA itself for 516 continuation.
- Enabling a heavy CPA management stack, Postgres, Redis, or usage keeper.
- Deleting old sub2api data directories or running Docker prune.
- Storing Cloudflare or CPA management secrets in the repository.
- Claiming benchmark-level 516 correctness from route smoke tests alone.
