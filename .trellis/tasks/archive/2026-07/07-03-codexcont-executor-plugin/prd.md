# CodexCont executor plugin migration

## Goal

Replace the Docker-hosted Python CodexCont sidecar with an executor-only CPA
plugin while keeping Key Policy Plus as the owner of the ordinary user portal,
keys, quotas, RPM, and usage APIs.

## Requirements

- `cpa-key-policy-plus` continues to own `https://cpa-usage.konbakuyomu.us/`,
  user sessions, `/user/api/*`, quota windows, RPM, and usage details.
- The new executor plugin must not register or expose an ordinary user usage
  portal. It replaces only the old CodexCont Docker sidecar behavior for
  streaming `/v1/responses` continuation protection.
- The public execution chain after cutover is:
  `Codex -> Caddy -> CPA -> Key Policy Plus auth/quota -> CodexCont Executor
  plugin -> upstream model`.
- The executor plugin must implement the old CodexCont stream-folding behavior:
  `518*n-2` reasoning-token detection, encrypted reasoning replay, hidden
  commentary continuation marker, max continuation cap, terminal event
  reconstruction, and safe metadata.
- The executor plugin must have a single on/off config switch. When disabled,
  CPA should continue using the normal upstream path without continuation
  protection.
- The executor plugin should replace Governor's CPAMP-side CodexCont realtime
  monitoring role with a read-only admin monitor resource. This monitor may
  show safe rolling request summaries and executor health, but must not become
  a user portal or key/quota control plane.
- Safe protection summaries may be shown through Plus, but summary transport
  must not change ownership of `cpa-usage` and must not expose request bodies,
  response bodies, keys, cookies, OAuth tokens, or encrypted reasoning.

## Acceptance Criteria

- [x] A CPA plugin named for CodexCont executor behavior exists separately from
      `cpa-key-policy-plus` and does not register user/admin portal resources
      beyond internal/management observability needed for executor health.
- [x] The executor plugin exposes a CPAMP admin menu/resource for read-only
      realtime rolling CodexCont monitoring, replacing Governor's daily
      protection-monitoring role without exposing `/user` or `/user/api/*`.
- [x] With the executor switch disabled, model routing returns unhandled and
      existing CPA behavior remains available.
- [x] With the executor switch enabled, streaming Responses requests are routed
      to the plugin executor and folded into one downstream stream.
- [x] Unit tests cover clean passthrough, auto-continued two-round folding,
      `max_continue`, missing encrypted reasoning, upstream EOF, upstream
      error, sequence-number monotonicity, and reconstructed metadata.
- [x] Plus user page/API contracts remain unchanged:
      `/user/api/session`, `/user/api/me`, `/user/api/usage?range=24h`,
      `/user/api/events?range=24h&limit=100`, and `/user/api/codexcont`.
- [x] Protection summary failures degrade only the summary display; user login,
      quota, usage, and events still work.
- [x] `go test ./...` passes in both the executor plugin and Key Policy Plus
      plugin packages.
- [x] Public Caddy `/v1/responses` is cut over to CPA-first executor routing,
      then the old Docker CodexCont sidecar is stopped. This is intentionally
      a separate production approval gate.

## Notes

- Do not modify official CPA or CPAMP.
- Do not move `cpa-usage.konbakuyomu.us` to the executor plugin.
- Old Docker/Caddy sidecar removal is a production rollout step after local and
  server validation, not part of the first local code change.
