# Design

## Ownership Boundary

CPA/CPAMP remains the source of truth for native `sk-...` keys and aliases.
Plus should not represent old self-managed `cpa_...` rows as active strategy
rows. Plus can retain old rows in SQLite for audit/history, but UI/API default
projections must be native-present rows.

## Native Key Sync

The sync routine reads:

```text
/CLIProxyAPI/config.yaml top-level api-keys
/CLIProxyAPI/plugin-state/cpamp-usage.sqlite api_key_aliases
```

Admin key list and save/reset endpoints that operate on visible keys must call
sync first, not only plugin configure. This makes the Plus page refresh reflect
official CPAMP changes.

Alias lookup must normalize both CPAMP shapes:

```text
<sha256 hex>
sha256:<sha256 hex>
```

The normalized map key is always bare lowercase SHA256 hex.

## Admin Projection

Store may retain historical rows, but admin default response should expose only
active native policy rows:

- `source == native_cpa`
- `source_present == true`
- `hidden == false`

Removed native rows remain available only behind an explicit diagnostic/show
removed mode if needed. Legacy Plus rows are not part of the default current-key
view.

## Auth Availability

The executor owns model aliasing before host callbacks. Current production only
mapped `gpt-5.4`; user traffic now asks for `gpt-5.5`. Add `gpt-5.5` to the
upstream alias map in code/tests and production config, mapping to the same
provider-registered Codex upstream model already proven to work:
`gpt-5.3-codex-spark`.

Downstream SSE and Plus usage projection should keep client-visible
`gpt-5.5`; host callback metadata/body uses the upstream model.

## Rollback

- Plugin rollback: restore prior `.so` backups from `/opt/codex-stacks/cpa/plugins/linux/amd64/` backups if new tests fail.
- Config rollback: remove the added `gpt-5.5` alias if it causes unexpected upstream behavior.
- DB rollback should not be required; rows are hidden/projected rather than deleted.
