# Design

## Problem Split

This task has two independent but user-visible failures:

1. Executor model aliasing is correct for provider compatibility but currently
   forwards all client tools unchanged. When the visible model `gpt-5.5` maps
   to upstream `gpt-5.3-codex-spark`, upstream rejects unsupported built-in
   tools such as `image_generation`.
2. Plus mirrors the correct native key count, but one official alias (`alicea`)
   is missing because the production alias source is a WAL-mode CPAMP SQLite DB
   and the old CPA file bind saw only a stale `usage.sqlite` view without the
   live `usage.sqlite-wal` / `usage.sqlite-shm` files.

## Executor Tool Compatibility

The executor must preserve the visible model contract while adapting the
upstream request body for the actual target model.

Data flow:

```text
client body model=gpt-5.5, tools=[image_generation]
  -> executor resolves upstream_model=gpt-5.3-codex-spark
  -> executor rewrites body.model to Spark
  -> executor filters unsupported tools for Spark
  -> upstream receives compatible body
  -> downstream stream and summaries still say gpt-5.5
```

For the first fix, define a small compatibility layer keyed by upstream model.
For `gpt-5.3-codex-spark`, drop built-in tool entries whose `type` is
`image_generation`. Do not drop custom function tools. If the tools array
becomes empty, omit it from the upstream body. Record safe diagnostics such as
`filtered_tool_types:["image_generation"]`, not the request body.

## Alias Source

Plus already treats CPA config `api-keys` as native key source of truth. Alias
must come from the same source the official API key panel uses.

Investigation should identify the real production storage shape. The code
should support:

- existing `api_key_aliases(api_key_hash, alias)` table when present;
- a safe file/config/table fallback discovered in production;
- a directory-level read-only CPAMP data mount for WAL-mode SQLite, because a
  single-file bind can hide newly written aliases from the reader;
- no fallback should ever store or return raw keys.

Alias matching remains by SHA256 of the native raw key. The ledger key remains
the native hash-derived `native_<preview>` id; alias is display and inheritance
metadata only.

## Rollout

Build only changed plugin artifacts with the existing WSL Go runtime and
`-tags cliproxy_plugin -buildmode=c-shared`. Deploy to SJC with a backup and
restart only `cpa`.

Production note: applying the new CPAMP data directory mount requires recreating
the `cpa` container. The current compose file has `pull_policy: always`; avoid
that on future plugin/config-only changes or expect an official CPA image pull
as part of recreate.

Rollback is replacing the new `.so` with the timestamped backup and restarting
`cpa`.
