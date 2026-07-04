# Usage Admin Batch Save And Request Details Design

## Boundary

This task modifies only the custom `cpa-usage-portal` service:

- backend: `cpa_usage_portal/app.py`, `pricing.py`, `redaction.py`, and local
  quota state helpers if needed
- frontend: `cpa_usage_portal/static/admin.html` and
  `cpa_usage_portal/static/dashboard.html`
- tests: `tests/test_cpa_usage_portal.py`

CPA, CPAMP, CPA Key Policy, and their images/source trees remain untouched.

## Admin Batch Save

Current flow:

- `GET /admin/api/keys` returns safe key projections plus local 5H/month limits.
- Each row renders a per-key `保存` button.
- Clicking it sends `PUT /admin/api/keys/{key_id}/limits`.

New flow:

- The table renders editable 5H/month inputs only.
- A toolbar-level `保存全部` button compares current input values with the last
  loaded snapshot.
- The button is disabled when there are no dirty edits.
- Dirty state is visible in the toolbar and, optionally, on changed rows.
- Submit one payload:

```json
{
  "limits": [
    {
      "id": "alice-key",
      "five_hour_usd": 1.25,
      "monthly_usd": 20
    }
  ]
}
```

Backend adds `PUT /admin/api/keys/limits`:

- requires the existing admin guard
- validates that `limits` is a list
- validates each id resolves to a Key Policy record
- parses each numeric limit with the existing `_parse_float_limit`
- writes all valid limits to local `QuotaState`
- returns refreshed safe records, or an explicit error with the bad id/index

The existing per-key route can remain for backwards compatibility and tests.

## Admin Recent Requests

`usage-admin` changes from "one selected Key only" to "all Keys by default":

- `key_id=all` merges recent events from enabled Key Policy records.
- Each event receives a safe `key` projection with `id`, `name`, `preview`, and
  `enabled`.
- The UI renders an `全部 Key` select option first, then one option per key.
- The recent-request table adds a `用户/Key` column.
- Single-key filtering remains available by passing the concrete policy id.

## Pricing Breakdown

Current `pricing.py` recomputes only `cost` and `cost_source`.

New backend projection:

- keep the current total cost fields for compatibility
- add `cost_breakdown` to event rows after `safe_events(...)` and
  `apply_event_pricing(...)`
- compute breakdown in Python from the same Key Policy `ModelPrice` used for
  table totals, so frontend math does not duplicate pricing rules
- align cache semantics with CPAMP's own management panel. CPAMP API
  `cached_tokens` is already the compatibility cached-input bucket. The portal
  displays it as `CPAMP 缓存命中`, while fine-grained cache read/create remain
  separate. If a raw row includes `cache_tokens`, normalize it with CPAMP's
  formula before pricing to avoid double counting.

Suggested `cost_breakdown` shape:

```json
{
  "source": "key_policy",
  "price_model": "gpt-5.5",
  "unit": "usd_per_1m_tokens",
  "service_tier": "priority",
  "service_tier_multiplier": 2.5,
  "prices": {
    "input_per_million": 5,
    "output_per_million": 30,
    "cache_read_per_million": 0.5,
    "cache_creation_per_million": 5
  },
  "tokens": {
    "input": 955,
    "cached_input": 86016,
    "cpamp_cached_input": 86016,
    "billable_uncached_input": 955,
    "cache_read": 0,
    "cache_creation": 0,
    "fine_grained_cache_read": 0,
    "fine_grained_cache_creation": 0,
    "effective_cache_read_for_hit_rate": 86016,
    "cache_hit_rate": 0.989,
    "cache_semantics": "cpamp_compatible_cached_tokens",
    "output": 2455,
    "reasoning": 2270,
    "visible_output_estimate": 185,
    "total": 89426
  },
  "costs": {
    "input": 0.004775,
    "cached_input": 0.043008,
    "cache_read": 0,
    "cache_creation": 0,
    "output": 0.07365,
    "subtotal": 0.121433,
    "total": 0.3035825
  }
}
```

`visible_output_estimate` is a safe derived number:

```text
max(output_tokens - reasoning_tokens, 0)
```

It must be clearly labeled as an estimate because upstream accounting can vary.

## Detail UI

The user page keeps the compact main table:

- time/request id
- status
- model
- latency/TTFT
- total tokens with input/output hint
- reasoning tokens
- cost
- expand action

Expanded detail should be reorganized into sections:

- `请求信息`: request id, endpoint, status code, model/requested model,
  service tier, reasoning effort
- `Token 组成`: input, cached input, cache read, cache creation, output,
  reasoning, visible output estimate, total
- `费用组成`: price model, source, per-million prices, service tier multiplier,
  individual cost parts, total
- `限额窗口`: included windows, current range, remaining, reset/start point
- `失败详情`: short redacted failure reason, expanded redacted detail only when
  failed

The admin events area can reuse the same rendering helpers or a simplified
variant, but it should expose the same cost/token composition for operators.

## Sub2API Lessons Applied

Sub2API stores and exposes separate token and cost categories instead of one
opaque amount:

- input tokens/cost
- output tokens/cost
- cache creation tokens/cost
- cache read tokens/cost
- total cost and actual cost
- service tier and reasoning effort
- request type, stream/openai-ws mode, latency, TTFT

Our portal cannot recover fields CPAMP never records, and should not invent
secret/raw payload fields. The useful adaptation is to present all safe fields
CPAMP already provides, plus a deterministic pricing projection using our Key
Policy prices.

## CodexCont Protection Correlation And Key Identity

This task does not join usage events to CodexCont request summaries. It does add
safe Key identity to CodexCont's own request summaries:

- Add optional `[admin] key_policy_state_path`.
- On request start, parse `Authorization: Bearer ...` only long enough to hash
  the key and match the Key Policy state.
- Store only a safe `key_identity` projection on diagnostics request summaries.
- If the key is missing, display `未携带 Key`; if unmatched, display
  `未识别 Key` plus a safe hash preview.

This gives the CodexCont table the same operator-friendly "who made this
request" context as `usage-admin` without introducing a cross-service join.

## Visual Consistency

Both custom dashboards use the same dark operations-console style:

- remove the current `metric::after` crescent decoration
- keep 8px cards, restrained gradients, compact chips, fixed-width tables, and
  dense but readable detail panels
- avoid decorative shapes that can be mistaken for broken chart widgets

## Compatibility And Security

- Existing `/api/events` and `/admin/api/events` fields stay compatible.
- New fields are additive.
- Redaction remains server-side.
- The frontend must not reconstruct costs from hidden raw event data.
- No raw request body, response body, authorization header, cookies, OAuth
  token, full hash, management key, or encrypted reasoning content is exposed.

## Rollout

Local first:

- run unit/smoke tests
- inspect the pages with desktop and narrow mobile widths if implementation
  changes layout materially

Server rollout later:

- back up `/opt/codex-stacks/cpa-usage-portal`
- upload/rebuild/restart only `cpa-usage-portal`
- do not touch CPA, CPAMP, or Key Policy images/source
- do not use Docker prune or batch deletion
