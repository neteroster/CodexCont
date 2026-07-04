# Codex Continuation and CPA Integration Contracts

## Scenario: Responses continuation middleware and CPA integration

### 1. Scope / Trigger
- Trigger this spec whenever work touches `/v1/responses` request handling, Codex encrypted reasoning replay, 516-style truncation detection, CPA integration, or proxy/egress deployment for Codex traffic.
- This is cross-layer work: request decoding, upstream SSE control flow, auth routing, Docker/Caddy networking, and downstream response compatibility all affect correctness.
- The SJC migration proved that replacing sub2api with CPA removes the old non-passthrough production path, but does not by itself solve the 516 continuation problem.

### 2. Signatures
- HTTP endpoint: `POST /v1/responses`
- Request headers:
  - `Content-Encoding: zstd` is accepted by the middleware and decoded before JSON parsing.
  - `Content-Encoding` must not be forwarded after decode because the forwarded body is plain JSON.
  - `Responses-API-Base` may override the upstream Responses base according to config mode.
- Continuation detector:
  - `reasoning_tokens == 518 * n - 2`
  - examples: `516`, `1034`, `1552`, `2070`, `2588`
- Production CPA route:
  - Public base URL: `https://cpa.konbakuyomu.us/`
  - CPA local bind: `127.0.0.1:8317`
  - Required production Codex egress: `socks5://172.19.0.1:1082`

### 3. Contracts
- A continuation round is allowed only when the terminal upstream response has the truncation-token fingerprint and the completed output includes replayable encrypted reasoning.
- Hidden continuation rounds must preserve the same selected OAuth account and proxy route. Do not rotate to another account inside one folded response; encrypted reasoning may be account-bound.
- Downstream clients must see one logical Responses stream, even if the middleware or CPA opens multiple upstream rounds.
- Reasoning items may be forwarded as reasoning, but tentative message/function-call output from a truncated round must stay buffered and must be discarded if a continuation round is opened.
- Final usage must distinguish agent-facing logical usage from billed upstream usage. Keep per-round metadata redacted; never log OAuth tokens, API keys, or encrypted reasoning payloads.
- CPA's ordinary stream chunk plugin surface is not enough for this feature because it can mutate/drop chunks after executor output, but cannot own upstream retry/continuation with the same auth context. A durable CPA-native implementation belongs in the Codex executor or in a new executor-level supervisor API.

### 4. Validation & Error Matrix
- Unsupported request `Content-Encoding` -> return `400` with an explicit decode error.
- Invalid zstd body -> return `400`; log content type, encoding, and byte length, but not body content.
- Truncation fingerprint without encrypted reasoning -> do not continue; flush the natural terminal response.
- Upstream EOF before terminal event -> emit an incomplete terminal response and do not leak buffered tentative text/tool calls.
- Continuation cap reached -> stop and report the cap reason in metadata.
- CPA egress route not reachable from the CPA container -> fail deployment validation; attach CPA to the required Docker network rather than falling back to direct egress.

### 5. Good/Base/Bad Cases
- Good: Round 1 ends with `reasoning_tokens=516`, has encrypted reasoning, and tentative text. The middleware replays reasoning plus a hidden marker into Round 2, discards Round 1 text, and downstream receives only the folded final answer.
- Base: A normal upstream response has no truncation fingerprint. The middleware acts as a transparent stream proxy.
- Bad: A downstream plugin sees `response.completed` and tries to start another upstream request after chunks have already been emitted. This leaks partial output and cannot guarantee same-auth replay.

### 6. Tests Required
- Unit: detector accepts `518 * n - 2` values and rejects adjacent values.
- Unit: zstd request bodies decode before JSON parsing, and `Content-Encoding` is dropped from forwarded headers.
- Stream fixture: truncated round followed by clean round produces one created event, one terminal event, monotonic sequence numbers, two reasoning items, and only the final clean answer.
- Stream fixture: truncated function call is discarded; clean function call is flushed.
- Stream fixture: EOF without terminal event emits `response.incomplete` and does not leak buffered text.
- Integration: CPA production smoke must verify `/healthz`, authenticated `/v1/models`, authenticated `/v1/responses`, and egress evidence through `172.19.0.1:1082`.

### 7. Wrong vs Correct

#### Wrong
```text
client -> CPA ordinary StreamChunkInterceptor plugin -> open ad hoc second request
```

This is too late in the pipeline: the plugin only sees downstream-bound chunks and cannot safely reuse the executor-selected Codex auth/proxy context.

#### Correct
```text
client -> continuation supervisor -> Codex executor same-auth upstream rounds -> folded downstream stream
```

The continuation owner must sit at executor level, where it can inspect raw upstream SSE, preserve auth/proxy identity, replay encrypted reasoning, and reconstruct the final logical response.

## Scenario: CodexCont admin diagnostics dashboard and request summaries

### 1. Scope / Trigger
- Trigger this spec whenever work touches CodexCont `/admin/*` routes, in-process diagnostics, SSE admin streams, dashboard UI, or production deployment of the CodexCont sidecar behind CPA.
- This is a cross-layer contract: `/v1/responses` lifecycle events feed `Diagnostics`, `Diagnostics` projects request summaries, Starlette exposes JSON/SSE admin APIs, and the static dashboard renders beginner-facing protection status.
- The admin dashboard is observability for the 516 continuation mitigation. It must never become a second control plane that mutates CPA, OAuth accounts, proxy routes, or request payloads.

### 2. Signatures
- Admin routes:
  - `GET /admin/healthz` -> service health and uptime.
  - `GET /admin/status` -> counters, active request metadata, upstream health, and safe config summary.
  - `GET /admin/requests?limit=N` -> recent request-level protection summaries.
  - `GET /admin/logs?limit=N` -> recent redacted diagnostic log events.
  - `GET /admin/logs/stream` -> SSE stream with `event: ready`, `event: request`, and `event: log`.
  - `GET /admin/` -> static dashboard HTML.
- Request summary projection fields:
  - `request_id`, `model`, `path`, `started_at`, `updated_at`, `ended_at`, `duration_ms`
  - `status`, `protection`, `folded`, `passthrough`, `passthrough_reason`
  - `rounds[]`, `latest_round`, `latest_reasoning_tokens`
  - `first_truncation_round`, `first_truncation_reasoning_tokens`, `first_truncation_n`, `first_truncation_decision`, `continuation_count`
  - `truncation_match`, `final_status`, `stopped_reason`, `failure_reason`, `failure_detail`
  - optional safe `key_identity`: `known`, `name`, `id`, `preview`, `source`,
    `enabled`
- Protection values:
  - `protected_clean`, `auto_continued`, `risk_uncontinued`, `passthrough`, `failed`, `incomplete`, `processing`

### 3. Contracts
- `Diagnostics` owns the request-summary projection. The frontend may format labels, but it must not re-derive protection status from raw log event names or ad hoc field parsing.
- Admin data is memory-only. Do not add persistent log files, databases, Redis, or CPA Manager dependencies for dashboard v1/v2 behavior.
- Request summaries and logs must not include request bodies, Authorization headers, API keys, OAuth tokens, encrypted reasoning content, or internal implementation-only fields such as `_started_perf`.
- If CodexCont is configured with a Key Policy state path, request summaries
  may include safe key identity. The resolver must hash a bearer credential
  only long enough to match Key Policy state, then discard the raw key and
  expose only safe name/preview/source fields.
- `event: log` behavior is backward-compatible with the original dashboard stream. Adding request updates must use a separate `event: request` SSE event.
- The beginner-facing dashboard must distinguish "entered CodexCont protection and no continuation was needed" from "516/518n-2 was detected and a hidden continuation round was opened".
- When a continued request ends with a clean final round, `latest_reasoning_tokens` may be below 516. The dashboard must label it as latest-round reasoning and separately display the first 516/518n-2 trigger round from the request summary.
- The dashboard frontend must treat the SSE connection as recoverable browser state, not as a durable data source. Manual refresh and foreground resume (`visibilitychange`, `pageshow`, or stale `focus`) must re-fetch the JSON snapshots with no-store/cache-bust semantics and force-create a new `EventSource`. Late responses from older fetches must not overwrite newer snapshots.
- The refresh action must provide visible busy/completion feedback, and the realtime connection chip must animate in all states (`connected`, `connecting/reconnecting`, and `disconnected`) so an operator can see that the page is alive after returning from an idle tab.
- The dashboard's top-right refresh button light must be driven by the same
  realtime state as the connection chip. During a refresh it may temporarily
  show `syncing`, but after the label falls back to "refresh" the light must
  keep the live state class (`live-ok`, `live-warn`, or `live-bad`) instead of
  returning to a neutral grey dot.
- Admin snapshot refresh must keep a short minimum visible `syncing` duration
  on manual/foreground refresh, just like the user page. Fast local admin
  snapshot responses must not collapse the operator feedback into a single
  imperceptible frame.
- When an SSE request update is still `processing`, the dashboard should
  immediately refresh status counters and follow up with short delayed
  `/admin/requests` snapshot reloads. Do not rely only on the next long polling
  interval to clear processing rows.
- Production admin access must remain behind `cpa-admin.konbakuyomu.us` plus Cloudflare Access. Public `cpa.konbakuyomu.us` must not expose `/admin/*`, `/codexcont/*`, `/management.html`, or CPA management APIs.
- SJC is a small-disk host. Deployment must prefer uploading changed files plus single-service rebuild/restart; do not use Docker prune or broad filesystem cleanup as part of dashboard rollout.

### 4. Validation & Error Matrix
- Invalid `limit` query on `/admin/requests` or `/admin/logs` -> fall back to safe defaults.
- Request summary retention exceeds configured cap -> discard oldest non-active summaries first.
- Active request summary is returned -> internal monotonic timer fields must be stripped before JSON/SSE output.
- `GET /admin/logs/stream?once=1` -> emits `ready`, recent `request` events, then recent `log` events, then ends.
- Upstream CPA health probe fails -> dashboard reports upstream unhealthy but admin routes still return safely.
- Browser tab is idle/backgrounded and returns later -> dashboard reconnects SSE and reloads snapshots without requiring a full page reload.
- Manual refresh is clicked while a previous fetch is slow -> the latest refresh wins; older fetch results are ignored instead of overwriting the visible table.
- Request row appears as `processing` -> short follow-up reloads update it to a
  terminal state without requiring a full page refresh.
- Public API host exposes any admin path -> deployment validation fails; fix Caddy/admin proxy routing before accepting rollout.
- SJC free space is tight before rebuild -> verify `df -h /` and avoid pulls/prune; if rebuild needs new image layers and space is insufficient, pause rather than cleaning broad data.

### 5. Good/Base/Bad Cases
- Good: A real Codex request appears in `/admin/requests` with `protection=protected_clean` or `auto_continued`, and no raw reasoning content is present.
- Base: An invalid JSON request returns `400` from `/v1/responses` and appears as `protection=failed`, `failure_reason=invalid_json_body`.
- Bad: The dashboard scans log strings like `round_decision` in JavaScript and guesses whether the request was protected. This duplicates backend contract logic and will drift.
- Bad: A production rollout fixes the page but exposes `/admin/requests` on `https://cpa.konbakuyomu.us/`. This leaks operational metadata and violates the public/admin boundary.

### 6. Tests Required
- Unit: request summary projection covers `protected_clean`, `auto_continued`, `risk_uncontinued`, `passthrough`, `failed`, and retention behavior.
- Unit: redaction preserves numeric counters such as `reasoning_tokens` and `total_tokens`, while redacting bearer/API/OAuth/encrypted-content fields.
- Route smoke: `/admin/requests` returns summaries and `/admin/logs/stream?once=1` includes both `event: request` and `event: log`.
- Frontend smoke: desktop and mobile dashboard render without horizontal overflow, and simulated protection states are visibly distinct.
- Frontend smoke: dashboard HTML keeps the manual-refresh reconnect path, foreground-resume handler, and visible refresh/realtime animation hooks.
- Frontend smoke: dashboard HTML keeps processing-request follow-up reloads.
- Production smoke: `cpa-admin.konbakuyomu.us/codexcont/` reaches the dashboard through Cloudflare Access, while public `cpa.konbakuyomu.us/admin/*` and `/codexcont/*` return `404`.

### 7. Wrong vs Correct

#### Wrong
```text
raw log event -> frontend string matching -> protection label
```

This spreads the event contract into JavaScript and makes the beginner-facing status depend on incidental log wording.

#### Correct
```text
/v1/responses lifecycle -> Diagnostics request summary -> /admin/requests + event: request -> dashboard label
```

`Diagnostics` is the single projection owner. The UI renders the explicit `protection` value and keeps raw logs as an advanced troubleshooting view only.

## Scenario: CPA Key Policy, CPAMP, and user usage portal

### 1. Scope / Trigger
- Trigger this spec whenever work touches `cpa_usage_portal/`, CPAMP monitoring
  queries, CPA Key Policy state parsing, per-key quota display, user usage
  events, or production routes for `cpa-usage.konbakuyomu.us`.
- This is a cross-layer contract: Key Policy state authenticates a raw
  `cpa_...` key, CPA records the plugin principal into usage events, CPAMP
  hashes that principal, the portal filters CPAMP analytics, and the frontend
  renders only safe per-user summaries.

### 2. Signatures
- Portal routes:
  - `GET /healthz`
  - `POST /api/session`
  - `DELETE /api/session`
  - `GET /api/me`
  - `GET /api/usage?range=5h|24h|7d|month`
  - `GET /api/events?range=5h|24h|7d|month&limit=N&before=...`
  - `GET /api/events/stream`
  - `GET /admin/`
  - `GET /admin/api/keys`
  - `PUT /admin/api/keys/limits`
  - `PUT /admin/api/keys/{id}/limits`
  - `POST /admin/api/keys/{id}/reset`
  - `GET /admin/api/events?key_id=all|...&range=5h|24h|7d|month`
- Production user route: `https://cpa-usage.konbakuyomu.us/`
- Production local quota admin route:
  `https://cpa-admin.konbakuyomu.us/usage-admin/`
- Production admin route for CPAMP: `https://cpa-admin.konbakuyomu.us/`
- Key Policy state path on SJC:
  `/opt/codex-stacks/cpa/plugin-state/cpa-key-policy-state.json`
- Containers that read Key Policy state must use a mounted in-container path
  such as `/data/plugin-state/cpa-key-policy-state.json`; host paths are not
  valid from inside CodexCont or the usage portal unless explicitly mounted.

### 3. Contracts
- CPA stays on the official image. Do not fork CPA to implement per-key usage
  views.
- CPAMP stays on the official `seakee/cpa-manager-plus:latest` image. Do not
  patch CPAMP source for user self-service behavior; add sidecars or routes
  around it instead.
- CPA Key Policy stays as the official release plugin binary mounted into CPA.
  Updating Key Policy should mean replacing the plugin binary and preserving
  `plugin-state`, not editing CPA source.
- Keep CPA, CPAMP, CodexCont, and `cpa-usage-portal` as separate containers /
  stacks on the shared `cpa_net`. Do not bundle them into one image because
  independent updates are part of the maintenance contract.
- The user portal may mutate only its own local SQLite metadata: 5H/month
  limits, reset watermarks, and audit entries. It may read Key Policy state and
  query CPAMP monitoring, but it must not mutate CPA, OAuth accounts, proxy
  routing, CPAMP source events, or Key Policy records.
- Ordinary users should receive Key Policy `cpa_...` keys. CPA native `sk...`
  keys are compatibility/admin escape hatches and should not be treated as
  self-service user credentials.
- There is no automatic one-to-one binding between native CPA `sk...` keys and
  Key Policy `cpa_...` keys. If a future migration needs such a bridge, design
  an explicit mapping layer and prove it cannot bypass Key Policy limits.
- The CPAMP login key and the CPA management key are different secrets.
  `cpa-admin.konbakuyomu.us/management.html` currently points to CPAMP, so it
  requires the CPAMP admin key. CPA-native management calls use the CPA
  management key through the internal admin proxy path.
- Login validation uses the raw user key only once:
  `sha256(trimmed_raw_cpa_key)` must match Key Policy `key_hash`
  (`sha256:<hex>`). The raw key must not be stored, logged, or returned.
- CPAMP filtering for Key Policy keys must use `sha256(Key Policy id)`, not
  `sha256(raw cpa_... key)`. CPA Key Policy authenticates requests with
  `Principal = key.ID`, and CPAMP hashes CPA's usage-record principal.
- Keep raw-key hash and CPAMP usage hash as separate concepts in code and
  tests. Session cookies may contain safe hash identifiers, but every request
  must re-load Key Policy state and validate the raw-key hash still maps to an
  enabled record.
- User APIs must never return raw API keys, full raw-key hashes, full CPAMP
  usage hashes, OAuth tokens, CPA management keys, CPAMP admin keys, cookies,
  Authorization headers, request bodies, response bodies, or encrypted
  reasoning content.
- CPAMP `api_key_stats` must be projected before returning it to the browser.
  Do not pass CPAMP rows through directly because they may include full
  `api_key_hash` values.
- When Plus receives CPA usage records from executor/host-callback paths, it
  must resolve the user key by `AuthID` first, then `APIKey`, then `Source`.
  Executor records can carry the Key Policy id in `AuthID` while `Source` or
  provider fields name an upstream account file. Mapping only by raw API/source
  fields drops valid usage from `cpa-usage.konbakuyomu.us`.
- Plus usage projection must keep the user-visible model separate from the
  internal upstream model. If CPA usage callbacks report the executor's
  internal model instead of a client alias, Plus must project known executor
  aliases such as `gpt-5.3-codex-spark -> gpt-5.4` into `model` and
  `requested_model`, while preserving the internal value in `actual_model`.
- Key Policy model entries may be structured objects under `models[]`, not only
  strings. The portal must parse clean aliases from `alias` / `model` /
  `target_model` fields instead of rendering dicts as strings.
- Per-key prices are owned by Key Policy. The portal must parse
  `input_price_per_million`, `output_price_per_million`, and
  `cache_read_price_per_million` from each model entry, plus legacy
  top-level `model_prices` forms for compatibility.
- A model entry whose input, output, cache-read, and cache-creation prices are
  all zero is treated as unpriced for safety. Missing prices must not silently
  turn into "free" usage unless a future explicit free-model policy is added.
- CPAMP can legitimately return `cost: 0` when its own global price book lacks
  custom Codex aliases. For self-service user accounting, `/api/usage` and
  `/api/events` should overlay costs using the current key's Key Policy price
  book. Do not interpret CPAMP zero cost as "free" when Key Policy prices are
  configured.
- CPAMP Management API is the source of truth for usage token projection. Its
  public `cached_tokens` field is already the compatibility cached-input bucket
  used by the main CPA/CPAMP dashboard:
  `max(max(cached_tokens, cache_tokens) - cache_read_tokens -
  cache_creation_tokens, 0)`. The portal must treat that field as a real
  OpenAI/Codex cache hit even when fine-grained `cache_read_tokens` and
  `cache_creation_tokens` are both zero.
- Keep CPAMP-compatible cached input separate from fine-grained cache
  read/create fields in user-facing details. For OpenAI/Codex, a large
  `cached_tokens` value with `cache_read_tokens = 0` is normal and must not be
  displayed as "no cache read".
- `/api/me` must expose safe daily/weekly USD limits and a safe pricing
  summary. It must also expose local 5H/month USD limits and reset points when
  the portal SQLite has them. The user dashboard must show 5H, daily, weekly,
  and monthly limits directly, not only as a selected-range hint.
- `usage-admin` bulk limit saves must validate every submitted key id and
  numeric 5H/month value before reporting success. The UI should expose one
  global save action for local limits and keep per-key soft reset actions
  separate, because reset changes the local watermark rather than the limit
  configuration.
- `usage-admin` all-key event mode (`key_id=all`) must merge enabled Key Policy
  keys, attach only a safe key summary (`id`, `name`, `preview`, `enabled`),
  sort newest first, and cap the merged result by the requested limit.
- The usage portal's selected time range controls both `/api/usage` aggregates
  and the visible `/api/events` recent-request table. The page must also render
  the active range label, because 24h and 7d can legitimately return identical
  numbers when all retained usage happened in the last day.
- Supported portal ranges are `5h`, `24h`, `7d`, and `month`. `month` is the
  current Asia/Shanghai calendar month. `5h`, `24h`, and `7d` are rolling
  windows.
- A portal soft reset writes a reset watermark and narrows future CPAMP query
  windows to `max(base_window_start, reset_at_ms)`. It must not delete CPAMP
  rows or rewrite Key Policy's own historical usage display.
- All `/admin/*` portal routes require a proxy-injected admin header from
  `cpa-admin.konbakuyomu.us/usage-admin/`. Public `cpa-usage.konbakuyomu.us`
  must not be able to call these routes successfully.
- Public `cpa.konbakuyomu.us` must continue to block management, plugin,
  admin, CodexCont dashboard, CPAMP, and usage-portal internals.
- The usage portal frontend must not rely on an old `/api/events/stream`
  connection after tab idle. Manual refresh and foreground resume must rebuild
  the `EventSource`, reload `/api/me`, `/api/usage`, and `/api/events` with
  no-store/cache-bust semantics, and keep late fetch responses from replacing
  newer data.
- The refresh button and realtime chip must expose visible state changes:
  refresh shows busy/completion animation, while the realtime chip pulses for
  connected, reconnecting, and disconnected states.
- The visible refresh state must not disappear just because the local or
  cached API responds quickly. Keep a short minimum `syncing` state before the
  completion confirmation, then use row/card highlights to show what changed
  without flashing or blanking the table.
- The refresh button's small status light and the realtime chip must share the
  same state transition. After the completion label returns to "refresh", the
  light must still pulse as connected/reconnecting/error rather than reverting
  to a grey idle light.
- Custom Governor/CodexCont dashboards must derive the visible `活跃` chip from
  the current request list's non-stale `processing` rows rather than directly
  rendering a backend `active_requests` counter. Backend counters can remain
  high after abnormal communication; stale processing rows may stay in history
  but must not keep the active chip inflated.
- CPAMP-aligned custom dashboards should use restrained status-dot animation
  only. Do not reintroduce page sweep bars, refresh-button sweep lights,
  metric-card bump animations, or broad row flash effects as the primary
  realtime feedback.
- A realtime usage event should update the visible recent-request table
  immediately and then schedule delayed snapshot refreshes, because CPAMP
  aggregate views may update slightly after the event row appears.
- SJC is disk-constrained. Portal rollouts should upload changed files and
  rebuild only `cpa-usage-portal`; do not use Docker prune or broad deletion.

### 4. Validation & Error Matrix
- Unknown raw API key -> `401 invalid_api_key`.
- Native CPA `sk...` key submitted to the user portal -> `401 invalid_api_key`
  unless it has been explicitly migrated into Key Policy; this is expected.
- Disabled Key Policy record -> `403 api_key_disabled` at login or
  `401 key_not_available` for an existing session.
- Missing or invalid session -> `401 not_authenticated`.
- CPA management key submitted to CPAMP UI -> reject as an invalid admin key;
  use the CPAMP admin key for CPAMP.
- CPAMP rows whose `api_key_hash` does not match the current key's CPAMP usage
  hash -> drop them server-side.
- Browser tab is left idle and reopened -> usage portal reconnects SSE and
  reloads own-key usage/events without a full page reload.
- Manual refresh happens during a slow previous refresh -> the latest refresh
  owns the visible state; stale responses are ignored.
- Key Policy prices exist but CPAMP returns zero cost -> user portal shows
  nonzero estimated cost from Key Policy prices and marks the source as
  `key_policy`.
- `GET /api/events?range=24h` -> recent events are fetched from the 24h window;
  omitting `range` keeps the compatibility default.
- Key Policy daily/weekly USD limits exist -> `/api/me` and the dashboard show
  both values safely.
- Portal local 5H/month limits exist -> `/api/me` and the dashboard show both
  values safely.
- `POST /admin/api/keys/{id}/reset` -> updates only portal reset watermarks;
  CPAMP original rows remain visible in CPAMP itself.
- `GET /admin/api/keys` without the proxy-injected admin header -> `404`.
- `GET /api/usage` must not include the full raw-key hash or full policy-id
  hash anywhere in the JSON response.
- CPA usage record with `AuthID=<key.id>`, `Source=<provider file>`, and
  `Alias=<client-visible model>` -> Plus stores a `usage_events` row for
  `<key.id>`, keeps the client-visible alias as `Model`/`RequestedModel`, and
  keeps the provider/internal model as `ActualModel`.
- CPA usage record with `Model=<executor internal model>` and no usable visible
  alias -> Plus applies the known executor usage alias table before pricing or
  user-event projection.
- DNS for `cpa-usage.konbakuyomu.us` may be absent while the sidecar and Caddy
  route are ready; verify with explicit host resolution before declaring the
  route broken.

### 5. Good/Base/Bad Cases
- Good: A user logs in with a `cpa_...` key; the portal validates
  `sha256(raw key)` against Key Policy state, then queries CPAMP with
  `sha256(key.id)` and shows only that key's events.
- Good: CPA and CPAMP are updated by pulling their official images while the
  custom user portal is rebuilt separately from this repository.
- Base: A Key Policy key has no events yet. The portal still shows safe key
  metadata and empty usage tables.
- Bad: The portal filters CPAMP with `sha256(raw key)` and shows zero events
  even though CPAMP has usage records for the key id.
- Bad: `/api/usage` returns CPAMP `api_key_stats` unchanged and leaks a full
  `api_key_hash` to the browser.
- Bad: Ordinary users log into CPAMP or create native `sk...` keys for
  themselves. That expands the admin trust boundary and bypasses the intended
  Key Policy user model.

### 6. Tests Required
- Unit: raw key hash validates login while `record.cpamp_hash` equals
  `sha256(policy id)` when `id` is present.
- Unit: CPAMP analytics calls use the policy-id hash, not the raw-key hash.
- Unit: usage/event projections reject another key's hash and do not return
  full hash values.
- Unit: structured Key Policy `models[]` entries produce clean model aliases,
  safe daily/weekly limits, and per-model prices.
- Unit: `/api/usage` and `/api/events` recompute nonzero costs from Key Policy
  prices when CPAMP cost fields are zero.
- Unit: `usage.handle` maps executor/host-callback records by `AuthID` before
  falling back to `APIKey` or `Source`, and preserves alias/internal-model
  projection in the stored event.
- Unit: `usage.handle` maps known executor internal models to visible aliases
  even when CPA fills both `Model` and `Alias` with the internal model.
- Unit: portal local SQLite stores 5H/month limits, applies reset watermarks,
  and closes connections cleanly on Windows.
- Unit: `/admin/*` routes reject requests without the proxy-injected admin
  header and expose safe quota projections when the header is present.
- Unit: redaction covers Authorization, cookies, API keys, tokens, management
  keys, and encrypted reasoning fields while preserving numeric token counters.
- Unit: retention deletes old CPAMP `usage_events` in batches and does not run
  `VACUUM`.
- Frontend smoke: user portal HTML keeps the forced stream reconnect,
  foreground-resume handler, and refresh/realtime animation hooks.
- Production smoke: allowed Key Policy model succeeds, disallowed model is
  rejected, CPAMP records real usage, the portal login succeeds, and
  `/api/events` returns only own events.

### 7. Wrong vs Correct

#### Wrong
```text
user -> native sk... key -> user portal / CPAMP admin
```

Native CPA keys are not the quota-managed user identity in this deployment.

#### Correct
```text
admin -> Key Policy creates cpa_... key -> user uses cpa_... for Codex and usage portal
```

The `cpa_...` key is both the request credential and the self-service usage
credential, while CPAMP remains admin-only.

## Scenario: CPA Key Policy Plus native-key policy layer

### 1. Scope / Trigger
- Trigger this spec whenever work touches `cpa_key_policy_plus_plugin/`, CPA
  native `api-keys`, CPAMP alias integration, Plus policy/quota decisions,
  user usage portal login, or over-limit `/v1/responses` behavior.
- This is cross-layer work: CPA config and CPAMP aliases feed Plus SQLite,
  Plus frontend-auth metadata feeds model routing/executor behavior, usage
  callbacks feed quota windows, and `cpa-usage.konbakuyomu.us` renders the
  user-facing view.

### 2. Signatures
- CPA config source: top-level `api-keys` in `/CLIProxyAPI/config.yaml`.
- CPAMP alias source:
  `/CLIProxyAPI/plugin-state/cpamp-usage.sqlite`, table
  `api_key_aliases(api_key_hash, alias, updated_at_ms)`.
- Plus config fields:
  `native_keys_config_path`, `cpamp_alias_db_path`,
  `codex_summary_db_path`, `codexcont_enabled`, and `codexcont_route`.
- Plus user API resource path on production:
  `/v0/resource/plugins/cpa-key-policy-plus/user/api/session`,
  `/me`, `/usage?range=24h`, `/events?range=24h&limit=N`, and
  `/codexcont?limit=N`.
- Plus user page public host:
  `https://cpa-usage.konbakuyomu.us/`.
- Executor denial body:
  ```json
  {
    "error": {
      "message": "CPA Key Policy+ 已拦截：<key name> 触发 <window>费用限额，已用 $<used> / 上限 $<limit>。",
      "type": "rate_limit_exceeded",
      "code": "five_hour_quota_exceeded",
      "param": "5h"
    }
  }
  ```

### 3. Contracts
- CPA/CPAMP owns native `sk-...` key lifecycle: create, delete, copy, and alias.
  Plus is a passive policy layer and must not expose raw-key creation,
  deletion, rotation, full-key copy, or alias editing controls.
- Plus stores only safe identity: `sha256:<hex>` hash, safe preview, source
  flags, read-only alias/name, and strategy fields. It must not store raw
  `sk-...` keys.
- Plus policy IDs for native keys use the native hash-derived
  `native_<preview>` form. Alias is display/template metadata and must not be
  the ledger primary key.
- New native keys default to enabled by current product decision. If exactly
  one removed historical row has the same alias, Plus may inherit policy fields
  and enabled state into the new native row, but usage history remains under
  the old row. If the new row has no RPM or fee-window limits, the admin UI
  must label it as missing/unlimited limits instead of hiding the risk.
- Removed native keys are marked `source_present=false`, disabled, hidden by
  default, and retained for historical usage/protection summaries.
- Plus admin key reads must trigger native-key sync before listing, then default
  to only current official native rows:
  `source == native_cpa`, `source_present == true`, and `hidden == false`.
  Legacy Plus rows and removed native rows may remain in SQLite for history or
  diagnostics, but must not appear in the ordinary strategy table. The ordinary
  admin page must request `/keys` without `include_removed`/`show_removed` and
  must not expose a visible removed-row toggle.
- Plus SQLite access must use a shared opener with `busy_timeout` for both the
  writable Plus DB and read-only auxiliary DBs. The writable Plus DB must limit
  the Go `database/sql` pool to one open connection; CPAMP alias DB, executor
  summary DB, and legacy import DB reads must use read-only opens where
  possible. When using one writable connection, do not keep `Rows` open while
  issuing writes on the same DB; collect IDs, close rows, then update.
- CPAMP alias sync must normalize `api_key_aliases.api_key_hash` whether it is
  stored as bare SHA256 hex or with a case-insensitive `sha256:` prefix.
- Plus user login now accepts CPA native `sk-...` keys. Retired Plus
  `cpa_...` keys should fail with migrated/retired guidance.
- Plus user APIs and HTML must never return raw keys, full hashes, bearer
  headers, cookies, request/response bodies, or encrypted reasoning.
- `codexcont_enabled` and `codexcont_route` remain false for Plus. Plus may
  read executor summaries through `codex_summary_db_path`, but the executor
  plugin owns streaming continuation.
- Over-limit model calls must return an OpenAI-compatible error body with
  Chinese key/window/used/limit details. Under the current official CPA
  executor ABI, plugins cannot guarantee the final public HTTP status or
  response headers on `/v1/responses`; treat the JSON body as the reliable
  client-facing contract unless CPA core is changed.

### 4. Validation & Error Matrix
- Native key appears in CPA config without a Plus policy -> insert enabled
  strategy row and mark missing RPM/fee-window limits in the admin UI.
- Native key appears with one same-alias removed template -> copy policy fields
  and enabled state; do not copy ledger usage.
- Native key appears with multiple same-alias removed templates -> insert
  disabled row with conflict state for manual review.
- Existing native row that is only the old empty/default-disabled placeholder
  -> upgrade to enabled on sync. A manually disabled row with actual strategy
  settings must stay disabled.
- Native key disappears from CPA config -> set `source_present=false`,
  `enabled=false`, `hidden=true`; do not delete history.
- Official key alias changes or deletions in CPAMP -> refreshing the Plus admin
  key API updates the displayed alias and default key count without requiring a
  CPA restart.
- Missing policy, removed source, disabled key, disallowed model, RPM limit, or
  5H/24H/7D/month fee limit -> structured policy denial with safe key name and
  stable error code.
- Fee quota uses post-accounting blocking: when current window usage is already
  `>= limit`, the next request is denied. Do not pre-charge or predict the
  current request cost.
- A `$0.00` limit is explicit and must deny immediately; `nil` means unlimited.
- Public `cpa.konbakuyomu.us` exposes plugin/admin/resource paths -> deployment
  is not accepted.
- Expecting true HTTP 429 from Plus executor without modifying CPA core ->
  invalid assumption; production acceptance should check the error JSON body.

### 5. Good/Base/Bad Cases
- Good: CPAMP has aliases `QQ的官key`, `kuma的官key`, and `阿伟的官key`; Plus
  syncs the corresponding native rows as enabled policy records and
  `cpa-usage.konbakuyomu.us` logs in with a native `sk-...` key.
- Good: CPAMP shows four official native keys; the Plus admin default key API
  returns exactly those four current native rows with matching aliases, while
  old `legacy_plus` / `cpa_...` rows remain hidden from the ordinary table.
- Good: Temporarily setting a key's 5H limit to `$0.00` makes the next
  `/v1/responses` return an OpenAI-compatible error body naming the key and
  `5小时费用限额`, then restoring the previous limit re-enables normal calls.
- Base: A newly created CPA native key has no alias/history; Plus shows it
  enabled and clearly warns that RPM or fee-window limits are not configured.
- Bad: Plus stores raw `sk-...` keys or exposes a full-key copy button. CPAMP
  already owns raw key lifecycle.
- Bad: Tests assert HTTP 429/header propagation from executor output under the
  current CPA ABI. That can pass only with a CPA core change.

### 6. Tests Required
- Go unit: native CPA config parsing and CPAMP `api_key_aliases` loading.
- Go unit: Plus SQLite opener applies busy timeout, writable DB uses one open
  connection, read-only auxiliary opens reject writes, and transient writer
  locks wait instead of immediately returning `SQLITE_BUSY`.
- Go unit: admin key listing triggers native sync, reflects alias/deletion
  changes, hides legacy rows by default, and keeps removed-native diagnostics
  separate from the ordinary table.
- Go unit: sync lifecycle for new, removed, inherited, and ambiguous native
  keys; historical usage does not move across native hash IDs.
- Go unit: policy denials cover missing policy, source removed, disabled,
  model allowlist, RPM, 5H, 24H, 7D, month, and explicit zero limits.
- Go unit: admin HTML has no create/delete/rotate/raw-key-copy lifecycle
  controls, no removed-row toggle, and visible missing-limit hints; user HTML
  points to native `sk-...` keys.
- Integration: production user API session/me/usage/events/codexcont works with
  an enabled native key.
- Integration: normal `/v1/models` and `/v1/responses` succeed with an enabled
  native key; over-limit `/v1/responses` returns the structured error body.

### 7. Wrong vs Correct

#### Wrong
```text
CPA native key -> Plus raw-key store -> Plus alias/key lifecycle controls
```

This duplicates CPAMP's job and increases secret exposure.

#### Correct
```text
CPA config api-keys + CPAMP aliases -> Plus native-key sync
  -> Plus strategy/quota rows keyed by native hash -> cpa-usage user portal
```

Plus owns policy and accounting, not the raw key lifecycle.

#### Wrong
```go
db.SetMaxOpenConns(1)
rows, _ := db.QueryContext(ctx, "select id from keys")
for rows.Next() {
    db.ExecContext(ctx, "update keys set hidden=1 where id=?", id)
}
```

This can self-deadlock when the single writable SQLite connection is still
owned by the active query.

#### Correct
```go
rows, _ := db.QueryContext(ctx, "select id from keys")
ids := collectIDsAndClose(rows)
for _, id := range ids {
    db.ExecContext(ctx, "update keys set hidden=1 where id=?", id)
}
```

Close read cursors before writes on the same single-connection Plus DB.

## Scenario: CPA Key Policy Plus historical key authority migration

### 1. Scope / Trigger
- Trigger this spec whenever work touches `cpa_key_policy_plus_plugin/`, the
  `cpa-key-policy-plus` CPA plugin config, `cpa-usage.konbakuyomu.us`, per-key
  quota windows, retired Plus-owned `cpa_...` key compatibility, or migration
  cleanup from the old `cpa-key-policy` plugin.
- This is cross-layer work: CPA dynamic plugin loading, old Key Policy JSON
  import, Plus SQLite state, Caddy public/admin routing, user cookies, and
  CodexCont/Governor deployment boundaries must agree.

### 2. Signatures
- CPA plugin artifact:
  `/CLIProxyAPI/plugins/linux/amd64/cpa-key-policy-plus.so`.
- CPA plugin config:
  `plugins.configs.cpa-key-policy-plus` with `enabled`, `priority`,
  `exclusive_auth`, `state_db_path`, `key_policy_state_path`,
  `legacy_quota_db_path`, `governor_state_db_path`, `session_secret`,
  `codexcont_enabled`, `codexcont_route`, `codexcont_url`, and `fail_mode`.
- SQLite tables owned by Plus: `keys`, `usage_events`, `reset_watermarks`,
  `active_requests`, `active_sessions`, `audit_log`, `codexcont_summaries`,
  and `settings`. `active_sessions` is retained for schema compatibility and
  delete cleanup, but is not an enforcement source after the RPM-only cutover.
- Admin resource: `GET /v0/resource/plugins/cpa-key-policy-plus/admin`.
- Admin management routes:
  - `GET /v0/management/plugins/cpa-key-policy-plus/keys`
  - `GET /v0/management/plugins/cpa-key-policy-plus/models`
  - `POST /v0/management/plugins/cpa-key-policy-plus/keys/create`
  - `PUT /v0/management/plugins/cpa-key-policy-plus/keys/save`
  - `PUT /v0/management/plugins/cpa-key-policy-plus/keys/limits`
  - `POST /v0/management/plugins/cpa-key-policy-plus/keys/reset`
  - `POST /v0/management/plugins/cpa-key-policy-plus/keys/delete`
- User resource: `GET /v0/resource/plugins/cpa-key-policy-plus/user`.
- Admin convenience route:
  `https://cpa-admin.konbakuyomu.us/key-policy-plus/`.
- Admin API alias:
  `https://cpa-admin.konbakuyomu.us/key-policy-plus/api/*` rewrites to the
  corresponding Plus management route. This alias is admin-host only and must
  not exist on `cpa.konbakuyomu.us`.
- User route: `https://cpa-usage.konbakuyomu.us/`.
- User API session creation is GET-only on CPA resource routes and sends the
  raw user key in `X-CPA-Key-Policy-Plus-Key`; do not put the key in the URL.
- User session cookie name: `cpa_key_policy_plus_session`. Session creation
  must refresh compatible cookies for `/`,
  `/v0/resource/plugins/cpa-key-policy-plus/user`, and
  `/key-policy-plus-user` so stale path-specific cookies from earlier routes
  do not survive a successful login.

### 3. Contracts
- This scenario is historical-migration context. In the current native-key
  design, CPA/CPAMP owns raw native `sk-...` keys and aliases; Plus owns only
  policy, quota, prices, resets, user sessions, and user usage projections.
  The old `cpa-key-policy` plugin must stay disabled in config.
- Plus may load old Key Policy JSON and legacy Governor/usage-admin SQLite
  watermarks as import/audit material, but ordinary admin/user surfaces must not
  present retired Plus-owned `cpa_...` rows as current keys.
- Plus must never store or return raw API keys, Authorization headers, full key
  hashes, cookies, request bodies, response bodies, OAuth tokens, or encrypted
  reasoning content.
- User session validation must tolerate multiple cookies with the same
  `cpa_key_policy_plus_session` name. Browsers may send both a stale
  path-specific cookie and a fresh root cookie for plugin resource API paths;
  the backend must try every candidate session token and accept the first valid
  one instead of failing on the first invalid token.
- CPA plugin `ResourceRoute` is GET-only in the current host. The Plus admin
  HTML may be served from a resource route, but create/save/reset mutations
  must go through `/key-policy-plus/api/*` -> CPA management routes. Do not
  send `POST` or `PUT` to `/v0/resource/plugins/cpa-key-policy-plus/admin/api/*`.
- The admin proxy route for `/key-policy-plus/api/*` must inject the CPA
  management key from a mounted secret or equivalent process environment; do
  not commit the raw key to Caddyfile, Trellis docs, or git. CPAMP iframe
  context must not be relied on to add an Authorization header for embedded
  plugin HTML, because the plugin page owns its own `fetch()` calls.
- The model catalog endpoint returns safe `ModelOption` projections only:
  `id`, optional display metadata, `source`, and `known`. It may merge CPA host
  model hints with already configured Plus models, but it must preserve unknown
  configured models instead of deleting them when online discovery is empty or
  stale.
- `5h`, `24h`, and `7d` are rolling USD windows. `month` is the current
  Asia/Shanghai calendar month. Reset writes a soft watermark and does not
  delete historical `usage_events`.
- The Plus user dashboard primary live view is fixed to `24h`. Do not expose a
  top-level `5h/24h/7d/month` selector on the ordinary user page; keep
  four-window quota visibility in side-by-side cards instead. The user APIs may
  continue accepting range parameters for compatibility and future callers.
- Refresh cancellation is browser control flow, not a user-visible sync
  failure. When a manual refresh, focus/pageshow refresh, or visibility change
  aborts an older in-flight user-page fetch, the page must ignore that aborted
  work instead of writing usage/protection error notices.
- Ordinary user throttling is RPM-only plus model allowlist and quota windows.
  `concurrency` and `max_active_sessions` payload fields are compatibility
  fields only: create/save handlers must accept stale payloads but persist and
  return both values as `0`, and frontend auth must not read them.
- Plus no longer owns native key deletion. Deleting a current key happens in
  CPA/CPAMP; the next Plus sync marks the native row removed/hidden for
  ordinary views while preserving `usage_events` plus `codexcont_summaries` for
  billing and troubleshooting history. Any legacy Plus-owned hard-delete route
  is compatibility-only and must not be used for native key lifecycle.
- CPAMP native key aliases are stored in `api_key_aliases` inside CPAMP's
  `usage.sqlite`, which runs in WAL mode in production. CPA must mount the
  containing CPAMP data directory read-only, not only the bare `usage.sqlite`
  file, so Plus can see `usage.sqlite-wal` / `usage.sqlite-shm` and newly
  written aliases. A file-only bind can look healthy but show stale aliases such
  as a hash preview instead of a newly renamed key.
- Plus alias loading may try multiple read-only SQLite paths and must treat a
  missing `api_key_aliases` table as an empty source, not as a fatal key sync
  failure. The CPA config `api-keys` list remains the current key source of
  truth; aliases are display metadata matched by native key SHA256.
- Archive/restore has been retired. Stale archive routes may remain as
  compatibility guards, but must return `410 archive_removed_use_delete`
  instead of mutating key state.
- `exclusive_auth: true` lets Plus participate in CPA frontend auth. In the
  current CPA host, policy rejection may be surfaced as CPA's generic `401`
  `Missing API key` response because `frontendAuth` returns unauthenticated.
  Treat that as an expected wrapper unless CPA adds typed auth-denial payloads.
- Current production request chain is `Codex -> Caddy -> CPA -> Plus policy ->
  cpa-codexcont-executor -> upstream`. Do not reintroduce the Docker
  CodexCont sidecar or Governor into the public `/v1/responses` path after the
  executor plugin has passed production smoke.
- `cpa-admin.konbakuyomu.us/usage-admin/` must no longer serve stale local
  limit controls after Plus cutover; return `404` or redirect to the Plus admin
  page.

### 4. Validation & Error Matrix
- Plus plugin missing/wrong architecture -> CPA logs do not show
  `plugin loaded plugin_id=cpa-key-policy-plus`; deployment is not accepted.
- Old Key Policy enabled alongside Plus exclusive auth -> ordinary key
  authority is ambiguous; disable old Key Policy before accepting cutover.
- Current valid native `sk-...` key -> `/v1/models`, `/v1/responses`, and user
  session login succeed. Retired `cpa_...` keys should fail with migrated or
  retired guidance.
- Valid full `cpa_...` key plus a stale same-name path-specific session cookie
  -> user session login and `/user/api/me` still succeed; stale cookies must
  not create a persistent `not_authenticated` loop after a successful login.
- Shortened key preview or rotated full key -> login fails; only the full key
  shown at create/rotation can match the stored hash.
- Disabled/removed/disallowed/over-RPM/over-quota request -> CPA/Plus rejects
  before upstream execution; the executor must not call the upstream provider
  for a denied key.
- Stale create/save payload includes `concurrency` or `max_active_sessions` ->
  Plus ignores the requested values and persists `0`.
- Stale archive route call -> `410 archive_removed_use_delete`.
- Native key removal in CPA/CPAMP -> Plus default admin key list no longer
  returns that key after sync; history remains internal.
- `POST/PUT /v0/resource/plugins/cpa-key-policy-plus/admin/api/*` -> fails
  before reaching plugin logic; this is a deployment/config bug if the admin
  page depends on it.
- `/key-policy-plus/api/*` without a working CPA management-key injection ->
  `401 missing management key` or `invalid_admin_key`; deployment is not
  accepted until the alias returns safe Plus JSON and create/save/reset work.
- CPA/host model discovery unavailable -> `/models` still returns the union of
  currently configured Plus model names and prices, with a warning instead of
  stripping key allowlists.
- `cpa-usage.konbakuyomu.us` exposes only the user resource and user APIs.
- Public `cpa.konbakuyomu.us/v0/resource/plugins/*`, `/usage-admin*`,
  `/codexcont*`, `/governor*`, `/management*`, `/key-policy-plus*`, and
  `/admin*` -> `404`.

### 5. Good/Base/Bad Cases
- Good: CPA logs show `cpa-key-policy-plus` loaded, Plus default admin key list
  mirrors current CPA native keys, `cpa-usage` login works with a current native
  key, and `/v1/responses` succeeds through the CPA -> Plus -> executor chain.
- Good: A browser with an old
  `Path=/v0/resource/plugins/cpa-key-policy-plus/user` session cookie can log
  in again; the new response refreshes all compatible paths and `/api/me`
  accepts the fresh token even if the stale token is sent first.
- Base: A key has no usage yet. User login still shows key metadata, configured
  models, prices, and empty usage tables.
- Good: The admin page is a resource HTML page, while its mutations use
  `/key-policy-plus/api/*` and reach Plus management handlers with the CPA
  management key injected by the admin proxy.
- Good: An unwanted key is removed in CPA/CPAMP; Plus ordinary key list drops it
  after sync, the key can no longer log in or authenticate requests, while
  historical usage and CodexCont summaries still exist by safe `key_id`.
- Bad: `cpa-usage` still reads `cpa_usage_portal` SQLite as the authority after
  Plus is enabled. That preserves the split-brain limit problem.
- Bad: A retired key is only hidden or archived. That keeps a confusing second
  lifecycle path and can make admins think a key was fully removed when the
  permission row still exists.
- Bad: The Plus admin page tries to create keys through
  `/v0/resource/plugins/cpa-key-policy-plus/admin/api/keys/create`. The current
  CPA host treats ResourceRoute as GET-only, so writes fail before plugin code.
- Bad: Caddy is changed to route public `/v1/responses` directly to CPA before
  executor-level folding exists. That bypasses the current 516/518n-2
  mitigation.

### 6. Tests Required
- Go unit: old state import remains history-only, native key preservation,
  raw-key hash login, rotation/deletion invalidation through native sync,
  negative value validation, model allowlist, RPM, rolling/natural-month quota
  windows, soft reset, retired concurrency/session fields forced to zero, and
  cost projection.
- Go unit: admin/user HTML resources are `no-store`, user login uses
  `X-CPA-Key-Policy-Plus-Key`, and responses do not leak raw keys/full hashes.
- Go unit: user session creation sets `cpa_key_policy_plus_session` cookies on
  the root path and known user-resource aliases, and session lookup succeeds
  when a stale same-name cookie appears before a fresh valid cookie in the
  `Cookie` header.
- Go unit: user events and CodexCont summaries are filtered to the current key.
- Go unit: Plus user HTML has no range dropdown, fixes usage/events requests to
  `range=24h`, keeps `24H / 7D` and `5H / 本月` quota cards, and ignores
  refresh-cancel aborts before rendering sync errors.
- Go unit: admin HTML points mutations at `/key-policy-plus/api`, model
  normalization preserves unknown configured models, and create/save/reset
  through the admin alias persist settings.
- Go unit: native deletion from CPA config hides the key from ordinary Plus
  admin responses, preserves usage and Codex summaries, and stale archive routes
  return `410 archive_removed_use_delete`.
- Frontend/Playwright: Plus admin can create a key, select discovered models,
  edit per-model prices, save, hard-delete a key, reload, and keep dense tables
  horizontally scrollable on 390px without page-level overflow.
- Production smoke: plugin SHA256 matches the built artifact, CPA logs show
  Plus loaded, Plus default admin key count matches current CPA native keys,
  `cpa-usage` login works, admin backend `/key-policy-plus/` and
  `/key-policy-plus/api/models` work, `usage-admin` backend returns `404`, and
  public blocked paths return `404`.
- Production smoke: a tiny authenticated `/v1/responses` request succeeds
  through the current CPA -> Plus -> executor route before handoff.

### 7. Wrong vs Correct

#### Wrong
```text
Plus enabled -> public /v1/responses -> Governor or Docker sidecar
```

This reintroduces a retired control plane and makes the request path harder to
reason about.

#### Correct
```text
Codex -> Caddy -> CPA -> Plus policy -> cpa-codexcont-executor -> upstream
```

#### Wrong
```text
admin HTML -> POST /v0/resource/plugins/cpa-key-policy-plus/admin/api/keys/create
```

This depends on a mutating ResourceRoute, but the current CPA host dispatches
resource plugin routes as GET-only browser resources.

#### Correct
```text
admin HTML -> /key-policy-plus/api/keys/create
admin proxy -> /v0/management/plugins/cpa-key-policy-plus/keys/create
```

The admin proxy injects the CPA management key from a mounted secret or process
environment, and the public API host still blocks `/key-policy-plus*`.

#### Wrong
```text
admin "deletes" a key by setting enabled=false or archived=true
```

This leaves a permission row behind and keeps the old archive lifecycle alive.

#### Correct
```text
admin HTML -> /key-policy-plus/api/keys/delete {"id":"...","confirm":"delete"}
Plus -> delete keys/reset_watermarks/active_sessions, keep safe history
```

Hard deletion removes the authority entry while preserving billing and
diagnostic records.

#### Wrong
```text
Codex window count -> reject requests via max_active_sessions
```

Normal Codex conversations can reuse or fan out window/session metadata in ways
that make this limit noisy and hard to explain.

#### Correct
```text
frontend auth -> enabled/deleted check -> model allowlist -> RPM -> quota windows
```

After the RPM-only cutover, concurrency/session fields are accepted only for
backward-compatible payload decoding and must be stored as zero.

#### Wrong
```text
Cookie: cpa_key_policy_plus_session=stale-path-token; cpa_key_policy_plus_session=fresh-root-token
backend -> verify only the first same-name cookie -> 401 not_authenticated
```

Browser cookie path precedence can put an older path-specific cookie before the
fresh root cookie on plugin resource API requests, causing a login loop even
after session creation succeeds.

#### Correct
```text
session creation -> Set-Cookie for root and known plugin/user aliases
session lookup -> collect all same-name session cookies -> accept first valid token
```

The backend must treat same-name cookies as a compatibility set, not as a
single trusted value.

Separate the key authority migration from the continuation-owner migration.

## Scenario: CodexCont executor-only CPA plugin

### 1. Scope / Trigger
- Trigger this spec whenever work touches `cpa_codexcont_executor_plugin/`,
  CPA executor routing for `/v1/responses`, or Plus protection-summary reads
  from an executor store.
- This plugin replaces the Docker CodexCont sidecar only. It must not own
  `cpa-usage.konbakuyomu.us`, ordinary user sessions, quota windows, RPM, or
  `/user/api/*`.

### 2. Signatures
- CPA plugin id: `cpa-codexcont-executor`.
- Artifact: `/CLIProxyAPI/plugins/linux/amd64/cpa-codexcont-executor.so`.
- Config keys: `enabled`, `route_enabled`, `state_db_path`, `fail_mode`,
  `upstream_model`, `upstream_model_aliases`, `truncation_step`,
  `max_continue`, and `marker_text`.
- Plus optional read-only bridge:
  `plugins.configs.cpa-key-policy-plus.codex_summary_db_path`.
- Management routes are internal observability only:
  `GET /plugins/cpa-codexcont-executor/status` and
  `GET /plugins/cpa-codexcont-executor/summaries`.
- CPAMP/admin resource:
  `GET /v0/resource/plugins/cpa-codexcont-executor/admin` renders a read-only
  realtime rolling monitor for executor health and safe summaries.
- Resource API aliases for that monitor:
  `GET /v0/resource/plugins/cpa-codexcont-executor/admin/api/status` and
  `GET /v0/resource/plugins/cpa-codexcont-executor/admin/api/summaries`.

### 3. Contracts
- `route_enabled=false` -> `model.route` returns unhandled. CPA keeps the
  normal upstream path and the executor plugin does not provide continuation
  protection.
- `route_enabled=true` -> only streaming Responses-style requests are routed to
  the executor. Non-stream requests remain unhandled by this plugin.
- The executor stream owner opens upstream rounds through CPA host callbacks,
  folds them into one downstream SSE stream, and preserves one logical terminal
  event for the client.
- Upstream model aliasing belongs inside the executor plugin. If
  `upstream_model` or `upstream_model_aliases` maps a client-visible model to a
  provider-registered internal model, the host callback model and request body
  model must be rewritten for upstream, while downstream SSE and safe summaries
  keep the client-visible model.
- CPA host callbacks may return SSE as line-sized chunks without trailing
  newlines. The executor parser must accept standalone `event:`, `data:`,
  comment, and blank line chunks as complete SSE lines.
- The executor may persist only safe summaries: request id, key id, model,
  protection state, round counters, reasoning counters, continuation count,
  stopped/failure reason, timestamps, and safe diagnostics such as model alias
  evidence and read byte counts. It must not persist or return request bodies,
  response bodies, raw keys, Authorization headers, OAuth tokens, cookies, or
  encrypted reasoning.
- The executor may register a CPAMP admin menu/resource for read-only
  monitoring. This is the replacement for Governor's CodexCont protection
  monitor only; it must not expose `/user`, `/user/api/*`, key editing, quota
  editing, or ordinary user self-service.
- CPAMP must show exactly one executor sidebar entry. Internal management
  routes such as `/plugins/cpa-codexcont-executor/status` and `/summaries`
  must not set `Menu`; only the `/admin` resource may set
  `Menu: CodexCont Executor`.
- If CPA/CPAMP requires resource-adjacent capabilities for resource menu
  registration, the executor may declare non-exclusive
  `frontend_auth_provider=true` and `usage_plugin=true` only as compatibility
  shims. In that case `frontend_auth.authenticate` must always return
  unauthenticated and `usage.handle` must be a no-op response. These shims must
  not authenticate requests, persist usage events, calculate costs, mutate
  quota windows, or become billing sources.
- CPA calls `plugin.reconfigure` after the initial `plugin.register` and
  decodes the response through the same registration path. Executor plugins
  must return full metadata and capabilities from both methods; returning only a
  lightweight configured acknowledgement makes CPA mark the plugin
  unregistered and drops CPAMP resource routes.
- Plus may read the executor SQLite store through `codex_summary_db_path` for
  `/user/api/codexcont`; read failures degrade only protection summaries and
  must not affect login, quota, `/user/api/usage`, or `/user/api/events`.
- After production traffic is verified on the executor plugin, the old Docker
  sidecar chain must be retired all the way through operational entry points:
  remove the stopped `codexcont` container and image explicitly, disable or
  rename the default CodexCont compose file so it cannot be recreated by a
  plain `docker compose up`, remove admin proxy routes that target
  `codexcont:8787` or `cpa-governor`, and set Plus `codexcont_enabled: false`
  so user-summary reads use the executor SQLite bridge instead of the old
  sidecar admin API. Keep legacy Governor state only as read-only import/audit
  material when Plus still needs it.

### 4. Validation & Error Matrix
- Plugin registers frontend auth or user resources -> reject the change; Plus
  owns the user portal.
- Plugin registers key/quota mutations or ordinary user resource APIs -> reject
  the change; the CPAMP resource is observability-only.
- Any non-admin executor management route sets a CPAMP `Menu` label -> reject;
  this creates duplicate sidebar entries and can expose raw JSON as a page.
- `frontend_auth.authenticate` authenticates a request, or `usage.handle` stores
  records / changes quota or cost state -> reject; these capabilities are only
  menu-registration shims.
- `route_enabled=false` but `model.route` handles a request -> reject; the
  switch is not one-click safe.
- Missing executor summary DB -> Plus falls back to sidecar/local summaries or
  returns an empty protection list, while usage APIs continue to pass.
- Upstream EOF before terminal event -> executor emits `response.incomplete`
  and must not leak buffered tentative message/function-call output.
- Host stream returns line-sized SSE chunks -> executor must still emit the
  terminal event; treating this as EOF/incomplete is a regression.
- Upstream alias configured -> upstream host callback uses the internal model
  in callback metadata and body; downstream stream and summaries keep the
  client-visible model.
- Executor default config must include stable Codex visible-model aliases for
  currently exposed Codex client models. In this deployment, `gpt-5.4` and
  `gpt-5.5` both default to the provider-registered upstream
  `gpt-5.3-codex-spark`; production YAML may repeat or override those mappings,
  but missing YAML entries must not make `gpt-5.5` fall through to an
  unregistered upstream model.
- When routing to `gpt-5.3-codex-spark`, the executor must filter upstream-only
  incompatible built-in tools such as `image_generation` before the host
  callback. It must preserve custom/function tools, remove an emptied `tools`
  array, clear any `tool_choice` that points to the filtered built-in, and keep
  safe diagnostics such as `filtered_tool_types` without storing the request
  body.
- A public `/v1/responses` failure that reaches the executor/host callback and
  returns `authentication_error` with `auth_unavailable` plus wording such as
  `Encountered invalidated oauth token for user` is a CPA Codex OAuth account
  credential problem, not a Plus native-key sync problem. Verify
  `/CLIProxyAPI/config.yaml` native key hashes, Plus admin key projection, and
  `auth-dir` Codex account files separately before blaming API keys.
- Truncation fingerprint without encrypted reasoning -> executor must not open
  a continuation round and must report `no_encrypted_content` metadata.
- After final sidecar cleanup, any live production config still containing
  `reverse_proxy codexcont:8787`, a default
  `/opt/codex-stacks/codexcont/docker-compose.yaml`, a `codexcont` Docker
  container/image, or Plus `codexcont_enabled: true` is a rollback hazard and
  must be fixed before calling the migration closed.

### 5. Good/Base/Bad Cases
- Good: `cpa-usage.konbakuyomu.us` still serves Plus, while public
  `/v1/responses` can later route through CPA and the executor plugin for
  continuation protection.
- Good: The CPAMP plugin menu has `CodexCont Executor`, and it shows a
  polling realtime monitor backed by executor status/summaries without key or
  quota controls.
- Good: `/v0/resource/plugins/cpa-codexcont-executor/admin` is routable inside
  the admin boundary, while `/v0/resource/plugins/cpa-codexcont-executor/status`
  is not a resource page and returns `404` through resource dispatch.
- Good: A `gpt-5.5` streaming request opens the host callback with
  `gpt-5.3-codex-spark`, while downstream events, summaries, and diagnostics
  preserve the client-visible `gpt-5.5`.
- Base: executor plugin loaded with `route_enabled=false`; no request is
  handled by the executor, and CPA remains usable without continuation folding.
- Base: All official native keys fail with the same `invalidated oauth token`
  message even though Plus lists the right keys and executor aliasing is active.
  Refresh or replace the CPA Codex OAuth auth JSON; do not rotate CPA native
  keys to fix an upstream account-token failure.
- Bad: executor plugin registers `/user` or `/user/api/session`. That creates a
  second user portal and conflicts with Key Policy Plus ownership.

### 6. Tests Required
- Go unit: registration has executor/model-router capability but no frontend
  auth, usage plugin, or user resources.
- Go unit: CPAMP admin monitor resource exists, returns `no-store` HTML, polls
  status/summaries, and does not contain user/key/quota control endpoints.
- Go unit: executor management registration exposes exactly one CPAMP menu
  entry, and non-admin management routes have empty `Menu` fields.
- Go unit: `plugin.reconfigure` returns full registration metadata and
  capabilities, not only `{"configured": true}`.
- Go unit: executor `usage.handle` returns an observability-only no-op and does
  not store billing or quota data.
- Go unit: executor `frontend_auth.authenticate` returns unauthenticated and is
  non-exclusive.
- Go unit: route switch disabled/enabled behavior and non-stream fallback.
- Go unit: upstream model aliasing rewrites host callback metadata/body while
  preserving downstream client-visible model and safe diagnostics.
- Go unit: default executor config aliases `gpt-5.5` to the configured Codex
  upstream model and keeps `gpt-5.5` visible in emitted streams and summaries.
- Go unit: SSE parser accepts host callback line-chunked streams without
  trailing newlines.
- Go unit: stream folding covers auto continuation, max continuation, missing
  encrypted reasoning, upstream EOF, upstream error, monotonic sequence
  numbers, and reconstructed proxy metadata.
- Go unit: Plus reads executor summaries by key id through
  `codex_summary_db_path`, filters other users, and fails soft when the DB is
  missing.

### 7. Wrong vs Correct

#### Wrong
```text
cpa-codexcont-executor -> registers user page -> cpa-usage host points there
```

This recreates the ownership confusion between usage portal and continuation
engine.

#### Correct
```text
cpa-key-policy-plus -> owns cpa-usage and /user/api/*
cpa-codexcont-executor -> owns streaming Responses continuation and read-only CPAMP monitor only
Plus -> optional read-only summary bridge for display
```

The executor replaces the Docker sidecar and Governor's CodexCont monitor, not
the Plus user portal or key/quota controls.

## Scenario: Local linux/amd64 Go plugin build toolchain

### 1. Scope / Trigger
- Trigger this spec whenever building or rebuilding the Linux CPA plugin
  artifacts for `cpa-codexcont-executor` or `cpa-key-policy-plus` from this
  Windows/WSL workspace.
- This is an infra contract because build reproducibility, WSL placement, disk
  usage, and production artifact SHA evidence all affect rollout safety.

### 2. Signatures
- Preferred WSL Go binary:
  `/mnt/d/Dev/20_Software/_LocalRuntime/go/go1.22.6-linux-amd64/go/bin/go`.
- Project-scoped fallback WSL Go binary:
  `/mnt/d/Dev/20_Software/_LocalRuntime/CodexCont/go-sdk-1.22.6/bin/go`.
- Cached Go tarball, if re-extraction is ever needed:
  `/mnt/d/Dev/20_Software/_LocalRuntime/go/downloads/go1.22.6.linux-amd64.tar.gz`.
- Expected version for the current CPA plugin builds:
  `go version go1.22.6 linux/amd64`.
- CPA plugin artifact build command shape:
  `CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -tags cliproxy_plugin -buildmode=c-shared -o <plugin>.so .`

### 3. Contracts
- Do not repeatedly download Go into WSL or `/tmp` for plugin builds when the
  `_LocalRuntime` toolchain exists.
- Build/test commands must either call the preferred Go binary explicitly or
  prepend its `bin` directory to `PATH` for that one command/session.
- If plain `go` is not in WSL `PATH`, that is not a blocker and must not start
  a new download. Use the explicit `_LocalRuntime` path instead.
- If the extracted toolchain is missing but the cached tarball exists, ask
  before re-extracting and place it under `_LocalRuntime`, not a throwaway
  `/tmp/codex-go*` directory.
- Record plugin artifact SHA256 hashes after every production-bound rebuild.
- CLIProxyAPI CPA plugins are C ABI shared objects built with
  `-tags cliproxy_plugin -buildmode=c-shared`. Do not use Go
  `-buildmode=plugin`; that produces the wrong plugin ABI for this host.
- Do not commit local build directories or generated `.so` / `.h` files from
  plugin builds. Keep them ignored and record only the production SHA evidence
  in the Trellis task.

### 4. Validation & Error Matrix
- Preferred Go path exists and reports `go1.22.6 linux/amd64` -> use it for
  `go test ./...` and plugin builds.
- Plain WSL `go` is absent -> continue with the explicit `_LocalRuntime` Go
  path; do not download.
- Preferred path missing but fallback path exists and reports the expected
  version -> use the fallback and record that choice in task evidence.
- Both extracted toolchains missing -> pause before network download; check the
  cached tarball and confirm the intended `_LocalRuntime` extraction target.
- Build artifact SHA not recorded -> rollout evidence is incomplete.
- Build uses `-buildmode=plugin` instead of `-buildmode=c-shared` -> artifact
  is invalid for CPA deployment even if `go build` exits successfully.

### 5. Good/Base/Bad Cases
- Good: A Linux plugin build uses
  `/mnt/d/Dev/20_Software/_LocalRuntime/go/go1.22.6-linux-amd64/go/bin/go`,
  tests pass, `file` reports an ELF x86-64 shared object, and SHA256 is logged.
- Base: WSL has no global `go`; explicit `_LocalRuntime` Go still works.
- Bad: A helper script silently downloads Go again into `/tmp` because
  `command -v go` returned empty.
- Bad: A local `build/` directory or generated plugin `.so` / `.h` appears in
  `git status` as an untracked production artifact.

### 6. Tests Required
- Shell smoke: `command -v go || true` plus the preferred explicit Go path
  `version` check before any WSL plugin build.
- Go unit: run `go test ./...` in both plugin packages with the selected Go
  binary.
- Artifact check: run `file` and `sha256sum` on production-bound `.so` files.
- Git check: `git status --short` must not include plugin build artifacts.

### 7. Wrong vs Correct

#### Wrong
```bash
command -v go || curl -fsSL https://go.dev/dl/go1.22.6.linux-amd64.tar.gz | tar -xz -C /tmp
```

This redownloads a large toolchain, hides the chosen compiler path, and leaves
throwaway state outside the project runtime convention.

#### Correct
```bash
/mnt/d/Dev/20_Software/_LocalRuntime/go/go1.22.6-linux-amd64/go/bin/go test ./...
```

The build uses the already-provisioned local runtime toolchain and produces
repeatable evidence.

## Scenario: CPA Governor plugin and CodexCont Engine rollout

### 1. Scope / Trigger
- Trigger this spec whenever work touches `cpa_governor_plugin/`, Governor
  plugin deployment, CPA plugin routing, `cpa-usage.konbakuyomu.us` user
  routing, or CodexCont Engine routes.
- This is cross-layer work: CPA dynamic plugin loading, Key Policy state
  import, SQLite usage state, Caddy public/admin routing, and CodexCont Engine
  health all have to agree.
- Governor is owned by this repository. CPA, CPAMP, and CPA Key Policy remain
  official upstream artifacts and must not be forked for this integration.

### 2. Signatures
- CPA plugin artifact:
  `/CLIProxyAPI/plugins/linux/amd64/cpa-governor.so`.
- CPA plugin config:
  `plugins.configs.cpa-governor` with `enabled`, `priority`,
  `exclusive_auth`, `state_db_path`, `key_policy_state_path`,
  `session_secret`, `codexcont_enabled`, `codexcont_route`,
  `codexcont_url`, and `fail_mode`.
- Admin resource:
  `GET /v0/resource/plugins/cpa-governor/admin`.
- User resource:
  `GET /v0/resource/plugins/cpa-governor/user`.
- Admin proxy convenience routes:
  `https://cpa-admin.konbakuyomu.us/governor/` and
  `https://cpa-admin.konbakuyomu.us/governor-user/`.
- Embedded admin CodexCont data channel:
  `GET /governor/codexcont/admin/status`,
  `GET /governor/codexcont/admin/requests?limit=N`, and
  `GET /governor/codexcont/admin/logs/stream?once=1`. These are admin-host
  only proxy paths to CodexCont `/admin/*`; the retired standalone
  `/codexcont/` dashboard must return `404`.
- User portal route:
  `https://cpa-usage.konbakuyomu.us/`.
- CodexCont Engine:
  `GET /engine/healthz` and `POST /engine/v1/responses/analyze`.

### 3. Contracts
- Deploy Governor first in passive mode unless a test-key executor cutover has
  already passed:
  `codexcont_enabled: true`, `codexcont_route: false`,
  `exclusive_auth: false`.
- In passive mode, Governor may provide admin/user UI, key visibility, usage
  storage, CodexCont health, and safe request projections, but it must not be
  described as the exclusive quota enforcer or the executor-level 516 owner.
- Public `cpa.konbakuyomu.us` must block `/v0/resource/plugins/*`,
  `/v0/management*`, `/admin*`, `/codexcont*`, `/governor*`, and related
  management paths.
- `cpa-usage.konbakuyomu.us` may expose only the Governor user page and
  `.../user/api/*`; it must return 404 for Governor admin resources and other
  management paths.
- CPA plugin `ResourceRoute` dispatch is GET-only in the current CPA host.
  User self-service APIs under `/v0/resource/plugins/cpa-governor/user/api/*`
  must therefore use GET requests. Session creation passes the raw user key in
  `X-CPA-Governor-Key` (or `X-CPA-User-Key`) so it does not collide with
  CPAMP/management `Authorization` headers on embedded plugin pages. A direct
  route may still accept `Authorization: Bearer <cpa_...>` as a fallback. Do
  not put the key in the URL, and do not implement user-resource mutations as
  POST unless they move behind a management route or another authenticated
  proxy surface.
- User resource responses and plugin HTML must send `Cache-Control: no-store`.
  CPAMP can keep a tab alive across plugin upgrades, so stale HTML/JS must not
  be cached by the browser or an intermediate admin proxy.
- `cpa-admin.konbakuyomu.us/governor/` is protected by Cloudflare Access and
  may route through the local admin proxy to CPA's plugin resource endpoint.
  The Governor admin resource is read-only daily observability: it may show
  CodexCont status, request summaries, hit rounds, latest reasoning counters,
  continuation counts, failures, and advanced logs, but it must not expose Key
  management, request-management tabs, or server-side CodexCont save controls.
  Persistent Governor settings live in CPA's plugin configuration drawer.
- Do not put CPA management keys, API keys, OAuth tokens, cookies, or
  encrypted reasoning into Caddy rewrites, browser URLs, Trellis docs, or git.
- The Governor user portal must explain key identity clearly: Key Policy
  `cpa_...` full keys are accepted, native CPA `sk...` keys and shortened
  previews are rejected with human-readable messages.
- When updating the plugin binary, record the SHA256 and verify CPA logs show
  the plugin loaded and registered from the platform directory.
- Dense admin/user tables on mobile must keep a stable minimum table width
  inside an overflowed panel. Do not let tables shrink until short fields turn
  vertical.
- On SJC, rebuild/restart only the necessary self-owned service or plugin.
  Do not use Docker prune and do not pull official images as a side effect of
  a Governor-only rollout.

### 4. Validation & Error Matrix
- CPA plugin file missing or wrong architecture -> CPA logs do not show
  `plugin loaded plugin_id=cpa-governor`; deployment is not accepted.
- Governor admin API returns no Key Policy keys -> verify
  `key_policy_state_path` and plugin-state mount before accepting the UI.
- `codexcont_route=false` -> production `/v1/responses` must still pass
  through the existing working route and return a real successful response.
- Public `cpa.konbakuyomu.us/v0/resource/plugins/cpa-governor/admin` returns
  200 -> rollback Caddy public block before accepting the rollout.
- Public `cpa.konbakuyomu.us/governor/` or any
  `/governor/codexcont/admin/*` path returns 200 -> rollback Caddy public
  block before accepting the rollout.
- Public `cpa-usage.konbakuyomu.us/v0/resource/plugins/cpa-governor/admin`
  returns 200 -> rollback user-host route before accepting the rollout.
- User API without session -> `401`; invalid CPA user key -> `401
  invalid_api_key`.
- User session with a native `sk...` key -> `401
  native_cpa_key_not_supported` and a message telling the user to use the full
  Key Policy `cpa_...` key.
- User session with a shortened `cpa_...` preview -> `401
  key_preview_not_usable` and a message telling the user to use the full key
  shown at create/rotation time.
- User session with a full but rotated/stale `cpa_...` key -> `401
  invalid_api_key`; validate by hashing the pasted key and comparing it with
  the current Key Policy state before blaming Governor sync.
- Embedded CPAMP plugin page sends a CPAMP management bearer token in
  `Authorization` plus user key in `X-CPA-Governor-Key` -> Governor must use
  the dedicated user-key header and return `200` for a valid current key.
- `POST` to a user resource API -> CPA returns `404` before the plugin; the
  browser UI must call these resource APIs with GET.
- Mobile Playwright snapshot shows table columns narrower than practical text
  width or vertical labels -> add panel overflow/min-width and revalidate.

### 5. Good/Base/Bad Cases
- Good: Governor is loaded by CPA, the sidebar `CPA Governor` page shows the
  read-only CodexCont protection dashboard, user page opens on `cpa-usage`, the
  embedded admin data channel works, retired `/codexcont/` returns 404, public
  API admin paths return 404, and real `/v1/responses` still succeeds.
- Base: Governor user page opens but no user is logged in. `/user/api/me`
  returns `401 not_authenticated`, and the page waits for a raw `cpa_...` key.
- Base: A Key Policy key is rotated. The old full key is unrecoverable and
  should fail login; only the newly generated full key shown in the rotation
  dialog can match the current `key_hash`.
- Bad: The user portal uses `Authorization` as its only login transport inside
  CPAMP. The admin shell may already use that header, causing valid user keys
  to be interpreted as invalid.
- Bad: Caddy sends public `/v1/responses` to Governor before the executor-level
  continuation path is validated. This can bypass the known-good CodexCont
  fold path.
- Bad: A deployment runs `docker compose up` against the CPA stack with
  `pull_policy: always` during a plugin-only change on the small SJC disk.
  This may pull new layers and introduce unrelated official-image drift.

### 6. Tests Required
- Go unit: key hashing, Key Policy import, quota windows, pricing, redaction,
  store usage events, admin/user handler responses.
- Go unit: user login must prefer `X-CPA-Governor-Key` over `Authorization`,
  read headers case-insensitively, and mark JSON/HTML responses `no-store`.
- Go unit: admin HTML must be read-only and must not contain Key management,
  request-detail tabs, CodexCont save actions, or spinner/diagonal animation
  hooks.
- Go unit: user CodexCont summaries must be filtered to the current session key
  by safe identity and must not leak other users' request ids.
- Python unit: CodexCont Engine summary projection and route smoke.
- Build verification: linux/amd64 `.so` SHA256 recorded and `file` reports an
  ELF x86-64 shared object compatible with the Debian/glibc CPA image.
- Server smoke: `/healthz`, authenticated `/v1/models`, authenticated
  `/v1/responses`, Governor admin/user pages, user-host 401/404 boundaries,
  public API 404 boundaries, disk free space.
- Playwright: desktop and 390px mobile snapshots for Governor admin and user
  pages; dense tables must be horizontally scrollable instead of vertically
  compressed.

### 7. Wrong vs Correct

#### Wrong
```text
plugin UI added -> route every public path to /v0/resource/plugins/*
```

This exposes internal management resources and bypasses the public/admin host
boundary.

#### Correct
```text
cpa-admin host -> Governor admin resource
cpa-usage host -> Governor user resource only
cpa API host -> official API plus explicit admin/plugin blocks
```

Each hostname exposes only the surface that matches its trust boundary.
