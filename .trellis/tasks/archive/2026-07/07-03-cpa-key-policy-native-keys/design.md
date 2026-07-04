# Design

## Ownership Boundary

CPA/CPAMP remains the key lifecycle authority. Plus becomes an overlay policy
database keyed by the native API key hash. Alias is display/template metadata,
not a ledger identity.

```text
CPA config api-keys + CPAMP aliases
  -> Plus native key sync
  -> Plus policy rows keyed by native_<hash preview>
  -> frontend auth / usage / user portal
```

## Native Key Identity

- Normalize native key with the existing submitted-key normalizer.
- Compute SHA256 and store it as `sha256:<hex>`.
- Generate ID as `native_<preview>`, where preview is the existing safe
  `HashPreview` without raw key material.
- Read alias from CPAMP `api_key_aliases.api_key_hash`; fallback display is the
  safe preview.
- `Name` follows the alias for read-only display. Admin edits do not change it.

## Sync Rules

When a native key exists in CPA config:

- Existing row: update hash, preview, alias/name, `source=native_cpa`,
  `source_present=true`, `hidden=false`; preserve policy fields.
- New row with no unique same-alias removed template: insert disabled with
  empty/default policy.
- New row with exactly one removed same-alias template: copy policy fields and
  enabled state into the new ID, set `inherited_from`, and leave usage/reset
  rows under the old ID.
- Multiple removed same-alias templates: insert disabled with
  `inherit_conflict=true` so UI can ask for manual policy selection later.

When a stored native row is absent from CPA config:

- Set `source_present=false`, `enabled=false`, `hidden=true`.
- Keep usage events, reset watermarks, and Codex protection summaries.

## Policy Decision

A single `PolicyDecision` projection owns auth allow/deny state:

- `Allowed`, `StatusCode`, `Type`, `Code`, `Message`, `Window`, `UsedUSD`,
  `LimitUSD`, `KeyID`, and safe `KeyName`.
- Missing/disabled/source-removed/model denials are deterministic before rate
  counters.
- RPM consumes one in-memory bucket entry only when allowed by earlier checks.
- Fee quota checks read current usage sums and deny when `used >= limit`.

Frontend auth still returns `Authenticated=false` for denied requests because
the CPA frontend-auth ABI has no custom response body. To make the user see the
real reason, Plus also exposes a deny-only model route/executor. The router
handles only requests whose bearer key maps to a denied Plus policy decision;
the executor returns an OpenAI-compatible 429 body or streaming SSE error.

## UI Contract

Admin page is renamed to "Key 策略":

- Shows native alias/name, preview, source status, inherited/conflict state,
  enabled, RPM, models, prices, and quota windows.
- Removed rows are hidden by default and can be shown with a toggle.
- No Plus-side create/delete/rotate/copy-full-key/rename lifecycle controls.

User page:

- Accepts the existing header transport but text/hints refer to native
  `sk-...` keys.
- `cpa_...` inputs return migrated/retired guidance.

## Compatibility

Old `key_policy_state_path` import remains as legacy migration support, but
native sync is the preferred source. Existing usage APIs and executor summary
bridge stay unchanged.
