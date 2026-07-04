# Design

## Boundaries

- `cpa-key-policy-plus` 是普通用户 Key 的权威配置和额度统计入口。
- `cpa-governor` 仍只负责管理员 CodexCont 实时状态展示。
- 官方 CPA/CPAMP 镜像保持不变；生产只替换自有插件 `.so`。

## Key Control Changes

- Frontend auth 删除并发和 active session 判定，只保留 enabled、archived/deleted absence、model allowlist、RPM、quota windows。
- Store 层保留旧字段和表以兼容已有 SQLite schema，但保存 Key 时强制 `Concurrency=0`、`MaxActiveSessions=0`。
- Governor 侧也停止执行 request concurrency，避免两套插件里仍有隐藏拦截点。

## Hard Delete Contract

- 新管理路由：
  - `POST /v0/management/plugins/cpa-key-policy-plus/keys/delete`
  - admin proxy alias: `POST /key-policy-plus/api/keys/delete`
- Request: `{ "id": "<key id>", "confirm": "delete" }`
- Effect:
  - delete from `keys`
  - delete from `reset_watermarks`
  - delete from `active_sessions`
  - keep `usage_events`
  - keep `codexcont_summaries`
  - append `audit_log` action `delete_key`
- Old archive route is removed from registration and returns HTTP 410 if stale HTML calls it.

## UI Design

- Key Policy+ admin list/detail:
  - Show Key name/preview, enabled state, RPM, quota summaries, model/pricing summary.
  - Remove concurrency/session UI.
  - Add destructive delete action in detail panel with explicit confirm dialog.
- Shared visual system:
  - Keep a single canonical `shared.css` shape for Plus and Governor.
  - Use the same `.chip` output for protection values everywhere.
  - Replace large custom protection result text in user detail cards with the same chip used by Governor rows.

## Deployment Shape

- Build Linux amd64 Plus plugin; build Governor only if its source changes.
- Backup Plus `.so`, Plus SQLite, CPA config, Caddy/admin proxy config.
- Upload changed plugin artifacts and restart only required services.
- After plugin deployment, call delete API for all keys where `enabled=false` or `archived=true`.

## Rollback

- Restore previous `.so` and Plus SQLite backup, restart CPA.
- Since hard delete removes active config rows, rollback for deleted keys requires SQLite backup restoration.
