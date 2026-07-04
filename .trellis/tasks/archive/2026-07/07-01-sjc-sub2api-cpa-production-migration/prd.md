# SJC sub2api to CPA production migration

## Goal

Replace the SJC production sub2api endpoint with lightweight CPA while preserving Codex OAuth auth, AT&T residential egress, and rollback evidence.

## Confirmed Facts

- SJC is reachable with `ssh sjc-guard`; the root disk is small (`9.6G`) and had about `1.3-1.4G` free during planning.
- Existing public production endpoint `https://sub2api.konbakuyomu.us/health` returned `200`.
- The user has created the DNS record for `cpa.konbakuyomu.us`.
- Caddy currently reverse proxies `sub2api.konbakuyomu.us` to `sub2api-canary:8080`; `cli-proxy.konbakuyomu.us` is currently `410 gone`.
- Current sub2api image was created on `2026-06-06`, before the later encrypted reasoning preservation fix, and account-level `openai_passthrough` is not enabled.
- The active production OpenAI OAuth account in sub2api is account `3`, assigned to group `openai-cpa-poc`, bound to proxy `SJC-ATT-RESIDENTIAL -> socks5://172.19.0.1:1082`.
- The `1082` egress route currently exits as `AS7018 AT&T Enterprises, LLC`; `1081` remains the direct SJC route.

## Requirements

- R1: Deploy a lightweight CPA stack on SJC at `/opt/codex-stacks/cpa` without adding Postgres, Redis, CPA Manager, Usage Keeper, or other persistent services.
- R2: Preserve current usable client API keys so callers can migrate by changing only the base URL from `https://sub2api.konbakuyomu.us/` to `https://cpa.konbakuyomu.us/`.
- R3: Convert usable sub2api OpenAI OAuth credentials into CPA `type: codex` auth JSON files locally on the server without printing or committing tokens.
- R4: Ensure the default CPA production account uses `proxy_url: socks5://172.19.0.1:1082`; do not silently fall back to `1081` or direct SJC egress.
- R5: Add `cpa.konbakuyomu.us` to Caddy using the existing wildcard TLS material and reverse proxy it to the CPA container.
- R6: Keep rollback available until CPA is verified with health, model listing, and a real `/v1/responses` request.
- R7: After CPA verification, stop and remove only the explicitly named sub2api app/Postgres/Redis containers; keep the egress router containers because CPA depends on `1082`.
- R8: Respect the small-disk constraint: do not run `docker system prune`, `docker image prune`, recursive deletes, or bulk directory deletion.

## Acceptance Criteria

- [ ] Trellis artifacts exist and record the migration requirements, design, implementation order, evidence, and rollback points.
- [ ] `/opt/codex-stacks/cpa` contains root-only CPA config/auth material and a Docker Compose deployment.
- [ ] `http://127.0.0.1:8317/healthz` and `https://cpa.konbakuyomu.us/healthz` return healthy responses.
- [ ] An authenticated `/v1/models` request succeeds through CPA using a migrated API key.
- [ ] A real authenticated `/v1/responses` request succeeds through CPA and produces evidence that `1082`/AT&T egress was used.
- [ ] `sub2api-canary`, `sub2api-canary-postgres`, and `sub2api-canary-redis` are not running after cutover.
- [ ] `sub2api-egress-att` and `sub2api-egress-direct` remain running.
- [ ] No secret-bearing `.env`, auth JSON, API key, OAuth token, or database dump is written into Git or Obsidian.
- [ ] No Docker prune, recursive deletion, or bulk deletion is performed.

## Out of Scope

- Migrating sub2api usage history or dashboard state into CPA.
- Installing a long-term CPA management dashboard.
- Deleting old sub2api data directories without a separate explicit user confirmation.
- Retiring the egress routers or changing the upstream residential subscription architecture.
