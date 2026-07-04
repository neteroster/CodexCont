# CPA Key Management And Usage Portal

## Goal

Give the CPA production stack real per-key governance without forking CPA:
administrators can monitor usage and set limits, while ordinary users can log
in with their own API key and see only their own usage and recent request
details.

The first version combines three layers:

- CPA Manager Plus (CPAMP) for administrator monitoring and request-level
  usage collection.
- CPA Key Policy for per-key model allowlists, RPM, and daily/weekly USD
  limits.
- A small independent usage portal for user self-service views.

## Requirements

- Keep CPA on the official image. Do not fork CPA or patch its runtime code.
- Keep CodexCont in the `/v1/responses` production path. The 516 continuation
  protection must not regress.
- Deploy CPAMP as an admin-only service behind `cpa-admin.konbakuyomu.us` and
  Cloudflare Access. CPAMP must consume CPA usage events and persist them in a
  bounded SQLite database.
- Enable CPA usage statistics and CPA plugins. Install the official
  `cpa-key-policy` plugin release and configure a persistent state file.
- New shared keys should be CPA Key Policy `cpa_...` keys. Existing native CPA
  `api-keys` remain as compatibility/admin escape hatch but are not the target
  mechanism for quota-managed users.
- Provide a Chinese user portal at `cpa-usage.konbakuyomu.us`.
- The user portal authenticates by accepting an API key once, validating it
  against Key Policy state, and storing only a signed HttpOnly session cookie
  containing safe hash identifiers and key metadata.
- The user portal must never store, log, render, or return raw API keys, OAuth
  tokens, CPA management keys, CPAMP admin keys, cookies, Authorization headers,
  request bodies, response bodies, or encrypted reasoning content.
- Users can only see their own key metadata, aggregate usage, and recent
  request summaries. Server-side filtering by `api_key_hash` is mandatory.
- Request details may include safe operational fields: time, model, status,
  latency, token usage, cost estimate, cache/reasoning counters if available,
  and redacted failure summaries.
- Budget allocation is semi-automatic in v1: the administrator supplies a
  trusted total budget, enabled keys are averaged, disabled keys are excluded,
  and missing model prices block automatic USD limit writes.
- CPAMP request history retention is 7 days. Retention must be batched and avoid
  `VACUUM`; WAL checkpoint is acceptable.
- SJC disk is small. Before server deployment, record `df -h /` and
  `docker system df`; do not use Docker prune or broad directory deletion.

## Acceptance Criteria

- [ ] CPAMP `/health` or equivalent status endpoint is reachable through the
      admin path and real CPA requests appear in monitoring data.
- [ ] CPA has `usage-statistics-enabled: true` and `plugins.enabled: true` with
      Key Policy loaded from a persistent plugin directory/state file.
- [ ] A Key Policy test key can call `/v1/responses`; a disallowed model is
      rejected; a low-limit/RPM test key is constrained.
- [ ] The CodexCont dashboard and `/v1/responses` folding path still work after
      CPA plugin/usage changes.
- [ ] `cpa-usage.konbakuyomu.us` lets a user log in with a Key Policy key and
      shows only that key's status, limits, aggregate usage, and recent events.
- [ ] User portal tests cover hash normalization, session safety, per-key
      filtering, redaction, budget suggestions, and retention SQL behavior.
- [ ] Public `cpa.konbakuyomu.us` still blocks CPA management/plugin/admin
      paths and does not expose CPAMP or the user portal internals.
- [ ] Deployment notes record final disk space, CPAMP SQLite/WAL size, and
      backup paths without leaking secrets.

## Non-Goals

- No CPA fork.
- No public CPAMP access for ordinary users.
- No attempt to infer OpenAI account balance as exact dollars in v1.
- No long-term billing ledger beyond 7-day request history.
- No request/response body inspection in the user portal.

## Notes

- Key Policy stores the raw `cpa_...` key hash as `sha256:<hex>` for login
  validation. For plugin keys, CPAMP records `api_key_hash` as
  `sha256(Key Policy id)` because CPA receives the plugin key id as the
  authenticated principal. The portal owns both normalizations.
- Cloudflare Access protects admin surfaces. The user portal is protected by
  API-key self-check plus strict server-side filtering.
