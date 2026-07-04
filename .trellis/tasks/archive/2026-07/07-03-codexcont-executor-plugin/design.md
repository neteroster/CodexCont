# Design

## Architecture Boundary

`cpa-key-policy-plus` remains the Key Policy and user-portal authority. The new
executor plugin owns only CodexCont continuation execution for streaming
Responses requests. This avoids conflating `cpa-usage.konbakuyomu.us` with the
sidecar replacement.

The intended cutover path is:

```text
Codex -> Caddy -> CPA
  -> cpa-key-policy-plus frontend auth / RPM / quota
  -> cpa-codexcont-executor model route + executor
  -> CPA host model stream
  -> upstream Responses API
```

## Executor Flow

- `model.route` handles only enabled, streaming Responses-style requests.
- `executor.execute_stream` opens the first upstream stream through the CPA host
  callback, reads upstream SSE chunks, and emits one folded downstream SSE
  stream through `host.stream.emit`.
- The folding state machine mirrors the Python sidecar: forward first
  lifecycle events, stream reasoning items, buffer tentative message/function
  output, inspect terminal usage, continue on `518*n-2` when encrypted reasoning
  is replayable, and emit one reconstructed terminal response.
- Continuation rounds rebuild the request body from the original input plus
  replayed reasoning and a hidden commentary marker. `previous_response_id` is
  dropped because state is carried explicitly.

## Upstream Model Alias

The executor may rewrite the internal upstream model while preserving the
client-visible model downstream. This is plugin-owned routing state, not a CPA
or CPAMP fork. The first production alias is:

```yaml
upstream_model_aliases:
  gpt-5.4: gpt-5.3-codex-spark
```

When a request enters as `gpt-5.4`, the host model callback receives
`gpt-5.3-codex-spark` in both the callback metadata and request body. The
folded downstream SSE and stored safe summary keep `gpt-5.4`, so the client and
Plus usage view do not learn or depend on the internal provider alias.

## Host Stream Compatibility

CPA host callbacks may return SSE as logical line chunks without trailing
newlines, for example one read containing only `event: response.created`. The
executor parser treats `event:`, `data:`, comment, and blank line chunks as
complete SSE lines. This prevents false `response.incomplete` results when the
host stream transport splits events differently than the old Python sidecar.

## Summary Bridge

The executor plugin writes safe request summaries to its own SQLite store.
Plus may optionally read that store for `/user/api/codexcont`, filtered by the
current Key Policy key id. Plus never writes executor state, and executor never
owns Plus user sessions or quota state.

If the summary bridge is unavailable, Plus returns an empty/degraded protection
summary while keeping `/user/api/me`, `/user/api/usage`, and `/user/api/events`
healthy.

## CPAMP Monitor

The executor plugin also owns the read-only CPAMP-side CodexCont monitor that
lets operators replace Governor's protection dashboard. It registers an admin
resource/menu for executor observability only, backed by safe status and
summary endpoints. The monitor polls executor summaries for rolling updates; it
does not expose `/user`, `/user/api/*`, key editing, quota controls, request
bodies, response bodies, or encrypted reasoning.

The current CPA/CPAMP resource-menu path expects plugin resources to come from
plugins that declare the same resource-adjacent capabilities used by Plus and
Governor. The executor therefore declares `frontend_auth_provider=true` and
`usage_plugin=true` only as resource-registration compatibility shims.
`frontend_auth.authenticate` always returns unauthenticated, and `usage.handle`
is an explicit no-op. These shims must not authenticate requests, store usage,
price costs, mutate quotas, or become a billing source.

## Safety

The executor plugin must not store or return request bodies, response bodies,
Authorization headers, raw keys, cookies, OAuth tokens, or encrypted reasoning.
Only safe counters and status fields are persisted: request id, key id if known,
model, protection status, rounds, reasoning counters, continuation count,
stopped/failure reason, timestamps, duration, and safe diagnostics such as
upstream model alias evidence, stream id presence, and byte counts.

## Compatibility

`cpa-key-policy-plus` keeps `codexcont_route` deprecated/off. The executor
plugin owns the new route switch, so disabling continuation protection is a
one-setting operation without changing the user portal.
