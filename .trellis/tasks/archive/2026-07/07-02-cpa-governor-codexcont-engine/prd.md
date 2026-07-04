# CPA Governor plugin and CodexCont engine

## Goal

Move the production CPA/CodexCont integration from the current Caddy-fronted sidecar shape into a cleaner CPA-owned architecture:

```text
Client -> CPA -> CPA Governor plugin -> optional CodexCont Engine -> CPA upstream execution path
```

The user-facing outcome is one coherent system: CPA stays official, CodexCont becomes a pure 516/518n-2 protection engine, and CPA Governor owns normal user keys, quotas, usage, request detail, CodexCont status, and both admin/user panels.

## Requirements

- Create and ship a self-owned CPA Governor plugin without modifying CPA, CPAMP, or CPA Key Policy official source/images.
- Keep existing production service usable during migration; do not remove the current Caddy/CodexCont sidecar path until the Governor path passes validation.
- Import or mirror existing Key Policy `cpa_...` users into Governor-managed identity records so ordinary users continue using CPA-style keys.
- Governor must be able to enforce enabled/disabled state, model allowlist, RPM, concurrency, and 5H/24H/7D/month USD quota before any upstream request is sent.
- Governor must persist authoritative key metadata, limits, reset watermarks, usage events, CodexCont protection summaries, and audit entries in its own SQLite database.
- Governor admin UI must live inside the CPA Admin plugin surface and include key management, limit/save/reset, request detail, CodexCont configuration/status, and audit information.
- Governor user UI must replace the independent usage portal as the target design, with tabs for quota/usage, CodexCont protection, and request details. Users must only see their own key's data.
- CodexCont Docker must gain an engine mode/API that can return safe protection summaries and never persist raw requests, responses, Authorization, OAuth tokens, encrypted reasoning, or true CoT.
- Realtime UI v1 can use 1-2 second polling rather than SSE.
- Public `cpa.konbakuyomu.us` must not expose management, plugin resource, CodexCont admin, CPAMP, or usage-admin internals.
- SJC deployment must respect small-disk constraints: no Docker prune, no broad deletion, backup before changes, and restart only required services.

## Acceptance Criteria

- [ ] Trellis `prd.md`, `design.md`, and `implement.md` record the architecture, safety boundaries, and rollout plan.
- [x] Local tests cover Governor key hashing, quota windows, price calculation, reset watermarks, safe event projection, and CodexCont engine summaries.
- [x] A Linux-compatible CPA Governor plugin artifact or build path exists and is documented.
- [x] CodexCont exposes `GET /engine/healthz` and a safe engine endpoint for protection summaries.
- [x] Admin UI shows all keys, limits, usage, request details, CodexCont config/status, and can save/reset through authenticated management APIs.
- [x] User UI can show own quota/usage, own request details, and own CodexCont status without exposing other users or secrets.
- [x] Server deployment keeps CPA official image and CPAMP/Key Policy official artifacts unchanged.
- [x] Production smoke verifies `/healthz`, authenticated model/request path, Governor user/admin pages, CodexCont engine health, and public admin-path blocking.
- [ ] If full executor cutover is not safe in one pass, the deployed state must still improve operations safely and leave a documented toggle/rollback to the current working path.

## Out of Scope

- Forking CPA, CPAMP, or CPA Key Policy.
- Claiming 516 is mathematically impossible after the change.
- Deleting old data directories or pruning Docker images.
- Exposing true reasoning content or encrypted reasoning payloads in any UI.
