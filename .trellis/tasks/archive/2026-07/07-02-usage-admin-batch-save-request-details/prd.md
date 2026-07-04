# Usage admin batch save and request details

## Goal

Improve the custom CPA usage portal so it is easier to operate multiple keys
and easier to understand what each request actually cost.

The task stays inside our own `cpa-usage-portal` service. It must not fork or
modify CPA, CPAMP, or CPA Key Policy official source/images.

## Requirements

- On `usage-admin`, replace per-row limit save buttons with one global
  `保存全部` action.
- Keep per-key soft reset actions (`清零当前` / `清零全部`) as explicit,
  destructive actions with confirmation.
- Track unsaved 5H/month limit edits in the admin UI so the operator can see
  whether the page is clean, dirty, saving, saved, or failed.
- Add a batch limits API in `cpa-usage-portal` so all edited key limits are
  validated and saved in one request.
- Improve request expansion details in the user usage page and the admin
  request view by learning from Sub2API's usage display model:
  - token breakdown: input, output, cached input, cache read, cache creation,
    total, reasoning tokens
  - cost breakdown: input cost, cached input cost, cache read cost, cache
    creation cost, output cost, subtotal/total, price model, pricing source,
    service tier multiplier
  - request metadata: request id, endpoint, status code, latency, TTFT,
    requested/resolved model, service tier, reasoning effort
  - quota/accounting context: which windows the request counts into, selected
    window remaining amount, reset watermark/start point
  - failure details: short visible reason by default, longer redacted detail
    only inside the expanded panel
- Preserve the existing security boundary:
  - never return request body, response body, raw API key, full key hash,
    Authorization, cookies, OAuth token, CPA/CPAMP management key, or encrypted
    reasoning content
  - never display real chain-of-thought; only display safe metrics such as
    reasoning token counts and reasoning effort
- Cache display must follow CPAMP main-panel semantics: `cached_tokens` is the
  CPAMP-compatible cache-hit bucket for OpenAI/Codex, while
  `cache_read_tokens` / `cache_creation_tokens` are fine-grained fields that
  may legitimately be zero.
- Keep the existing dark, compact dashboard style. The new details should feel
  like an operational breakdown, not a log dump.
- `usage-admin` 最近请求默认显示全部 Key 的请求，表格必须显示
  用户/Key 摘要，并保留单 Key 筛选。
- CodexCont 保护状态面板也要显示安全的用户/Key 归属；来源为可选
  Key Policy state，只做哈希匹配和安全预览，不保存原始 key。
- 两个自定义页面必须统一视觉语言，并移除指标卡中的月牙形装饰。

## Acceptance Criteria

- [x] `usage-admin` shows one global `保存全部` button and no per-row `保存`
      buttons.
- [x] Editing any 5H/month limit marks the admin page dirty; saving persists
      all edited keys and then refreshes the displayed quota windows.
- [x] Batch save validates malformed numeric limits and unknown key ids without
      partially hiding errors.
- [x] Per-row soft reset actions still work and still warn that they only write
      our local reset watermark.
- [x] `usage-admin` 最近请求默认是全部 Key；表格能分清每条请求属于哪个
      用户/Key，并可筛选到单个 Key。
- [x] `/api/events` and `/admin/api/events` continue to return existing
      compatible fields and additionally include a safe `cost_breakdown`
      projection when Key Policy pricing is available.
- [x] `/admin/api/events?key_id=all` returns merged events across enabled keys,
      sorted newest first and capped by `limit`.
- [x] CodexCont `/admin/requests` and request SSE include safe `key_identity`
      when Authorization can be identified.
- [x] Expanded request detail shows useful cost/token/reasoning/accounting
      sections and no longer spends most of the space on low-value raw failure
      blobs.
- [x] The cost shown in the table equals the sum of the cost breakdown parts
      after service-tier multiplier.
- [x] The metric cards in both custom pages no longer show the crescent-shaped
      decorative arc.
- [x] Tests cover batch save, pricing breakdown, redaction safety, and request
      detail HTML markers.
- [x] `.venv/Scripts/python.exe -m compileall cpa_usage_portal run_usage_portal.py` passes.
- [x] `.venv/Scripts/python.exe tests/test_cpa_usage_portal.py` passes.

## Notes

- Confirmed from current code: `cpa_usage_portal/static/admin.html` has
  per-row `data-save` buttons calling `saveLimits(keyId)`, and
  `cpa_usage_portal/app.py` only has a per-key
  `PUT /admin/api/keys/{key_id}/limits` route.
- Confirmed from current code: `cpa_usage_portal/static/dashboard.html`
  expansion currently shows request id, endpoint, status code, service tier,
  reasoning effort, provider quota, accounting windows, and redacted failure
  text, but does not show itemized pricing.
- Confirmed from Sub2API reference:
  `frontend/src/types/index.ts` models usage with input/output/cache creation/
  cache read token and cost fields, plus `total_cost`, `actual_cost`,
  `rate_multiplier`, `service_tier`, `reasoning_effort`, request type, stream,
  latency, and TTFT. Its backend `CostBreakdown` separates input/output/image/
  cache creation/cache read costs before summing total/actual cost.
- Product decision resolved on 2026-07-02: `usage-admin` 最近请求采用
  "全部 Key 默认，单 Key 可筛选"。
