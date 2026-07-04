# Design

## Boundaries

This task modifies only the self-owned `cpa_key_policy_plus_plugin` and any deployment/admin-proxy route notes required for `/key-policy-plus/api/*`. CPA, CPAMP, and old Key Policy upstream code remain untouched.

The Plus admin HTML remains a CPA plugin resource because CPAMP can render plugin resources from the left menu. Resource routes are safe for HTML and read-only GETs only. Mutations use CPA plugin management routes.

## Admin API Transport

The browser uses a single API base resolver:

1. Prefer `/key-policy-plus/api` on `cpa-admin.konbakuyomu.us`.
2. In local preview/test, support an explicit `window.CPA_KEY_POLICY_PLUS_API_BASE` override.
3. Do not fall back to mutating `/v0/resource/plugins/cpa-key-policy-plus/admin/api`.

The admin proxy must map:

```text
/key-policy-plus/api/* -> /v0/management/plugins/cpa-key-policy-plus/*
```

The plugin already owns management routes such as `GET /plugins/cpa-key-policy-plus/keys`, `POST /plugins/cpa-key-policy-plus/keys/create`, `PUT /plugins/cpa-key-policy-plus/keys/save`, and `POST /plugins/cpa-key-policy-plus/keys/reset`.

## Model Discovery

Add a management endpoint:

```text
GET /plugins/cpa-key-policy-plus/models
```

The endpoint returns a safe normalized shape:

```json
{
  "models": [
    {"id": "gpt-5.5", "source": "configured", "known": true}
  ],
  "warnings": []
}
```

Model sources are merged with stable de-duplication:

- optional configured static/default model catalog from plugin config,
- current key allowlist union from Plus SQLite,
- preserved unknown models from existing keys.

If CPA management model discovery is wired through the admin proxy in a future step, the frontend can merge those returned models with this endpoint without changing persistence. For this task, Plus must at least stop hard-coding a tiny model list and must not delete configured unknowns.

## Admin UI Data Flow

```text
Plus store -> /keys -> admin state
Plus model projection -> /models -> model selector state
admin edits -> normalized payload -> /keys/save
create dialog -> /keys/create -> one-time raw key modal -> reload keys
reset button -> /keys/reset -> reload keys
```

The table owns a draft copy of keys. Rendering formats the draft only; validation and normalization happen before sending to the API.

## Model And Price Editing

Main table columns show summary fields instead of long text:

- model count and unknown count,
- price coverage `priced/selected`,
- `编辑模型/价格` action.

The modal edits one selected key at a time. It contains:

- search box,
- all visible/clear buttons,
- selected model chips,
- discovered model list with checkboxes,
- unknown configured model notice,
- structured price rows for selected models.

Price units are fixed to USD per 1M tokens:

- input,
- output,
- cache read,
- cache write/creation.

Unknown selected models stay selected and writable. Saving never removes a model unless the admin explicitly unchecks/removes it.

## Security

Raw key material appears only in the successful-create response modal. The UI does not persist it in local storage or write it into URLs. API responses must keep existing safe key previews and must not add full hashes or secrets.

## Compatibility

Existing API payload fields are preserved where possible (`models`, `prices`, `limits`, `rpm`, `concurrency`, `max_active_sessions`). The UI may send both structured `model_prices` and compatibility `prices` shapes if the backend already expects one of them.

If `/models` fails, the UI falls back to the union of models already present in loaded keys and displays a warning instead of blocking all edits.

## Rollout And Rollback

Rollout is plugin-only plus a small admin-proxy alias. Back up the old `.so`, Plus SQLite DB, and admin proxy route file before replacing the plugin. Roll back by restoring the previous `.so` and proxy route, then restarting/reloading only the affected services.
