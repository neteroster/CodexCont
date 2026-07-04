# Design

## Architecture

The migration replaces the public application relay while keeping the existing SJC egress router layer:

`client -> cpa.konbakuyomu.us -> caddy-edge -> cpa:8317 -> CPA Codex executor -> socks5://172.19.0.1:1082 -> AT&T residential upstream -> OpenAI`

The old sub2api stack remains available until CPA passes data-plane verification:

`client -> sub2api.konbakuyomu.us -> caddy-edge -> sub2api-canary:8080 -> sub2api -> 1082`

## Server Layout

- CPA runtime directory: `/opt/codex-stacks/cpa`
- CPA config: `/opt/codex-stacks/cpa/config.yaml`
- CPA auth files: `/opt/codex-stacks/cpa/auths/*.json`
- CPA logs: `/opt/codex-stacks/cpa/logs`
- Compose service: `cpa`
- Docker network: `cpa_net`
- Published port: `127.0.0.1:8317:8317`
- Egress reachability: CPA also joins the existing `sub2api_canary_net` because the retained `sub2api-egress-att` host-network proxy listens specifically on `172.19.0.1:1082`; this keeps the migrated `proxy_url` literal and avoids reconfiguring the egress router.

## Auth and Access Contracts

- sub2api API keys are read from the existing database and copied into CPA `api-keys`.
- The active sub2api OAuth account is converted into CPA auth JSON with:
  - `type: "codex"`
  - token fields copied from sub2api credentials
  - `proxy_url: "socks5://172.19.0.1:1082"`
  - `disabled: false`
- Non-production or risky accounts are either skipped or written with `disabled: true`.
- Secrets stay on the server in root-only paths. Task artifacts record only counts, IDs, status, paths, and redacted key prefixes/suffixes.

## Caddy and DNS

The user has already created DNS for `cpa.konbakuyomu.us`. Caddy will add a new site block using the existing wildcard certificate:

`cpa.konbakuyomu.us -> cpa:8317`

The old `sub2api.konbakuyomu.us` route is not removed until CPA passes health, auth, model, and real response checks.

After CPA passes verification, `sub2api.konbakuyomu.us` returns an explicit `410` response pointing clients at `https://cpa.konbakuyomu.us/`; it must not reverse proxy to the removed sub2api containers.

## Disk Safety

The SJC host has limited free space, so the deployment intentionally avoids extra databases and large management components. Docker prune and recursive deletion are forbidden. If space drops below the configured SJC clean threshold, only the existing capacity guard allowlist may be used.

## Rollback

- Before Caddy cutover: stop CPA only; leave sub2api untouched.
- After Caddy cutover: restore the previous Caddyfile from the root-only backup and reload Caddy.
- If CPA auth refresh fails: re-login the affected CPA Codex auth; do not rely on the old sub2api non-passthrough reasoning path as the long-term fallback.
