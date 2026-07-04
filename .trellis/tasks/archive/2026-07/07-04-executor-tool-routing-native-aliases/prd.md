# Fix executor tool routing and native key aliases

## Goal

Restore the post-migration user experience:

- Local Codex calls using the visible `gpt-5.5` model must not fail just because
  the executor internally routes to `gpt-5.3-codex-spark`.
- CPA Key Policy+ must display the same four official key aliases as the CPA /
  CPAMP API key panel, including `alicea`, and the selected row/detail panel
  must refer to the same safe preview/id.

## Requirements

- Keep the client-visible model name (`gpt-5.5`) in downstream streams,
  summaries, and Plus usage projections.
- Preserve the executor's internal upstream model aliasing when needed for the
  current CPA provider registration, but make it compatible with client tools
  that the upstream target does not support.
- The screenshot failure `Tool 'image_generation' is not supported with
  gpt-5.3-codex-spark` must be prevented before the upstream call.
- Do not expose raw API keys, OAuth files, cookies, Authorization headers, full
  hashes, request/response bodies, or encrypted reasoning in UI, logs, task
  files, or final output.
- Plus must read official CPA/CPAMP aliases from the actual source of truth used
  by the API key panel. If the configured `cpamp_alias_db_path` has no
  `api_key_aliases` table, Plus must fall back to another safe configured source
  rather than silently showing the hash preview.
- Plus ordinary key list must still show exactly current official native keys;
  removed or legacy rows stay hidden.
- The fix must be deployed and verified on SJC before handoff.

## Acceptance Criteria

- [x] Trellis planning artifacts explain why the `gpt-5.5` to
  `gpt-5.3-codex-spark` mapping exists and how tool compatibility is handled.
- [x] Executor unit tests cover a `gpt-5.5` request with
  `tools:[{type:"image_generation"}]` routed to Spark, proving the upstream
  request removes or neutralizes the unsupported tool while preserving the
  visible model downstream.
- [x] Executor diagnostics/summaries do not leak raw request bodies while still
  exposing safe evidence that tools were filtered.
- [x] Plus unit tests cover alias lookup from the real official alias source and
  the fallback path when `api_key_aliases` is absent.
- [x] Plus admin API on SJC shows four current native keys with names:
  `kuma的官key`, `QQ的官key`, `阿伟的官key`, and `alicea`.
- [x] Selecting the `alicea` row in Plus shows detail preview/id consistent with
  the row preview, not a mismatched alias/hash.
- [x] Real production `/v1/responses` smoke with an enabled native key and
  `model=gpt-5.5` plus `image_generation` no longer fails with unsupported tool.
- [x] Regression checks pass:
  `go test ./...` in both plugin packages, `git diff --check`, Linux plugin
  build(s), public blocked-path smoke, and `cpa-usage` user API smoke.

## Notes

- User asked why the mapping exists: it is a compatibility alias in the executor,
  not a CPA native key issue. It lets the client keep asking for `gpt-5.5` while
  the current provider-registered upstream model is `gpt-5.3-codex-spark`.
- The mapping became visible as a bug because the new request carried the
  `image_generation` tool and Spark rejects that tool.
