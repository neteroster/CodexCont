# CPA Key Management And Usage Portal Design

## Architecture

```text
Codex / clients
  -> https://cpa.konbakuyomu.us/v1/responses
  -> public Caddy
  -> CodexCont sidecar
  -> CPA official container
  -> CPA Key Policy plugin
  -> OpenAI OAuth account

CPA official container
  -> usage event queue
  -> CPAMP collector
  -> /data/usage.sqlite

User browser
  -> https://cpa-usage.konbakuyomu.us/
  -> cpa-usage-portal sidecar
  -> Key Policy state file (read-only)
  -> CPAMP monitoring API (filtered by api_key_hash)
```

CPA remains the source of API behavior. CodexCont remains the source of 516
continuation protection. CPAMP and the user portal observe and project safe
usage information; they do not own request forwarding.

## Server Components

### CPAMP

- Stack path: `/opt/codex-stacks/cpamp`.
- Network: `cpa_net`.
- Persistent data: `/opt/codex-stacks/cpamp/data/usage.sqlite` and
  `/opt/codex-stacks/cpamp/data/data.key`.
- Collector mode: `auto`.
- CPA management endpoint: `http://cpa-admin-proxy:8327`, so CPA can keep
  `remote-management.allow-remote: false`.
- Admin route: `https://cpa-admin.konbakuyomu.us/cpamp/` via the existing admin
  proxy and Cloudflare Access.

### CPA Key Policy

- Install the official Linux amd64 release for `cpa-key-policy v0.2.1`.
- Mount a persistent CPA plugin directory and a persistent plugin state
  directory into the CPA container.
- CPA config shape:

```yaml
usage-statistics-enabled: true
plugins:
  enabled: true
  dir: "/CLIProxyAPI/plugins"
  configs:
    cpa-key-policy:
      enabled: true
      priority: 10
      state_file: "/CLIProxyAPI/plugin-state/cpa-key-policy-state.json"
      keys: []
```

When the state file exists, it is the source of truth. New user keys are created
through Key Policy management APIs or UI. Existing native CPA keys remain for
compatibility/admin access and should be migrated later.

### User Usage Portal

- Stack path: `/opt/codex-stacks/cpa-usage-portal`.
- Domain: `cpa-usage.konbakuyomu.us`.
- Network: `cpa_net`.
- Runtime: Python 3.12 with Starlette/httpx, matching CodexCont's lightweight
  stack.
- Static frontend: one Chinese HTML/CSS/JS page; no npm build.
- Portal reads Key Policy state read-only and talks to CPAMP through an internal
  admin URL with a CPAMP admin key from a root-only secret file or environment.

## Portal Data Contracts

### Hash Normalization

- Raw key: accepted only in `POST /api/session`.
- Raw-key hex: `sha256(trimmed_raw_key).hexdigest()`.
- Key Policy hash: `sha256:<raw-key-hex>`; this validates login.
- CPAMP usage hash for Key Policy keys: `sha256(Key Policy id)`.
- Fallback CPAMP hash for non-id records: raw-key hex.

The portal stores only safe hash identifiers and key metadata in a signed
HttpOnly cookie. It must not store the raw `cpa_...` key.

### Safe Key Metadata

The portal may return:

- key id/name/preview if present.
- enabled/disabled status.
- model allowlist or aliases.
- RPM.
- daily/weekly USD limits.
- usage counters exposed by Key Policy state.

It must not return Key Policy internal `key_hash`, raw key material, or plugin
management credentials.

### Safe Usage Event

The portal projects CPAMP events into safe fields:

- timestamp, model, provider/account labels if already non-secret.
- status, HTTP status, latency.
- input/output/reasoning/cache tokens when available.
- cost estimate when available.
- redacted error summary.

It must not return prompt, response text, headers, cookies, raw request payload,
raw response payload, OAuth token data, encrypted reasoning content, or any
unrecognized secret-like field.

## Portal API

- `POST /api/session`: body `{ "api_key": "..." }`; validates against Key
  Policy state and returns safe key metadata.
- `DELETE /api/session`: clears the session cookie.
- `GET /api/me`: returns safe key metadata for the current session.
- `GET /api/usage?range=24h|7d`: returns aggregated usage for the current key.
- `GET /api/events?limit=100&before=...`: returns recent safe request events.
- `GET /api/events/stream`: SSE stream for new safe request events.

All read APIs require a valid session. Every CPAMP query includes the current
session's CPAMP-format `api_key_hash`.

## Budget Suggestion

The portal package includes an admin utility module/script for budget
suggestions:

- Inputs: total daily/weekly budget, enabled Key Policy keys, model price table.
- Disabled keys are excluded.
- Missing model price data blocks USD limit suggestions.
- Output: per-key suggested daily/weekly limits and a safe patch payload that an
  admin can apply through Key Policy management APIs.

Applying limits remains an administrator action in v1.

## Retention

The retention job deletes CPAMP rows older than 7 days in small batches. It must
only target known CPAMP usage tables after introspection and run
`PRAGMA wal_checkpoint(TRUNCATE)` after deletion. It must not run `VACUUM`.

## Security Boundaries

- Public API host must continue to block `/management.html`,
  `/v0/management*`, `/v0/resource/plugins/*`, `/admin/*`, `/codexcont/*`,
  `/cpamp/*`, and portal-internal admin routes.
- CPAMP remains behind Cloudflare Access and CPA management key.
- User portal does not use Cloudflare Access in v1; the API key itself is the
  self-service credential. This makes strict redaction and per-key filtering the
  primary boundary.

## Rollback

- If CPAMP fails before CPA config changes, stop only CPAMP and leave production
  traffic unchanged.
- If CPA plugin startup fails, restore the backed-up CPA config/compose and
  restart only `cpa`.
- If the user portal fails, remove only the portal route/container; CPA,
  CodexCont, and CPAMP can continue running.
- If any public admin exposure is detected, revert Caddy/admin proxy routing
  before continuing functional tests.
