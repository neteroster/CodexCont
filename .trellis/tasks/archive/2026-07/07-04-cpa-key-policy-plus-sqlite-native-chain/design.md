# Design

## SQLite Discipline

Plus uses one durable store for policy/usage and reads several auxiliary SQLite
databases. All of those connections must use a shared helper instead of raw
`sql.Open("sqlite", path)`.

- Main Plus store: open with `_pragma=busy_timeout(5000)` and WAL, then set
  `MaxOpenConns(1)` and `MaxIdleConns(1)` so one CPA plugin instance cannot
  create competing write connections to the same SQLite file.
- Read-only auxiliary paths: open with `file:<path>?mode=ro&_pragma=busy_timeout(5000)`
  where possible. Set a small connection pool and never write to these DBs.
- Existing schema creation can keep explicit PRAGMA statements, but the busy
  timeout must be attached at connection-open time so every pooled connection
  inherits it.

## Native Key Mirror

CPA/CPAMP remains source of truth for native `sk-...` keys and aliases. Plus
stores only safe hash/preview and policy fields.

- Sync source: top-level CPA `api-keys` plus CPAMP `api_key_aliases`.
- Identity: native hash-derived ID remains the ledger key; alias is display and
  inheritance signal only.
- Default admin projection: only `source=native_cpa`, `source_present=true`,
  and `hidden=false`.
- Deleted official keys: mark removed/hidden internally and exclude from ordinary
  table. Do not delete usage/audit rows.
- New official keys: enabled by default. If a unique same-alias removed/legacy
  template exists, copy strategy fields. If no template exists, enabled with
  empty limits. If inheritance is ambiguous, keep conflict diagnostics but do
  not expose raw secrets.

## UI Contract

The Plus admin page is a strategy editor for current official keys, not a key
lifecycle page.

- Remove or keep retired lifecycle controls disabled/hidden as already intended.
- Rename counts so they mean current official keys, not total historical rows.
- Render an explicit missing-limit hint when RPM is zero/empty and all quota
  windows are unlimited.

## Production Validation Boundary

Internal API success is not enough. Acceptance requires:

- CPAMP Plus admin API and UI refresh are stable.
- The public usage portal works.
- A real `/v1/responses` request reaches upstream and completes with `gpt-5.5`.
- Public admin/resource paths are still blocked.
