# CPA Key Policy+ RPM-only、硬删除 Key、Usage/Governor 风格统一

## Goal

将普通用户 Key 控制收敛到 `RPM + 模型白名单 + 5H/24H/7D/月额度`，移除不好用的「请求并发」和「Codex窗口」限制；将 Key 生命周期从「禁用/归档」改为管理员可执行的硬删除；统一 `cpa-usage.konbakuyomu.us` 用户页和 CPAMP 左下角 `CPA Governor` 面板的视觉组件。

## Requirements

- `cpa-key-policy-plus` 不再执行 `concurrency` 和 `max_active_sessions` 限制。
- 新建/保存 Key 时兼容旧 payload，但 `concurrency` 和 `max_active_sessions` 统一保存/返回为 `0`。
- 管理页移除「请求并发」「Codex窗口」「显示归档」「归档隐藏」「恢复 Key」。
- 管理页新增硬删除按钮和确认流程，删除 Key 配置、reset watermarks、active sessions；保留脱敏历史 usage/codex summary。
- 旧 archive API 不再注册，兼容入口返回 `410 archive_removed_use_delete`。
- 部署后删除生产中当前所有禁用或归档 Key。
- `CPA Usage` 和 `CPA Governor` 统一状态 chip、详情卡、表格、按钮、工具栏状态灯风格，特别是保护结果在详情中也使用同款气泡 chip。
- 不改 CPA、CPAMP 官方源码或镜像；只改自有 Plus/Governor 插件和必要部署。

## Acceptance Criteria

- [x] Key Policy+ 管理页不再出现「请求并发」「Codex窗口」「显示归档」或归档/恢复按钮。
- [x] 新建/保存 Key 后返回的 `concurrency` 与 `max_active_sessions` 为 `0`。
- [x] 删除按钮能删除 Key；删除后该完整 `cpa_` key 无法登录用户页或通过 CPA frontend auth。
- [x] 删除不会删除该 Key 既有 usage/codex summary 历史记录。
- [x] 当前生产禁用/归档 Key 被删除，启用 Key 继续可登录和调用。
- [x] CPA Usage 与 CPA Governor 中保护状态 chip、详情卡、表格密度和按钮视觉一致。
- [x] 本地 Go 测试、HTML 脚本语法检查、Playwright 关键页面检查通过。
- [x] 生产公网 `cpa.konbakuyomu.us` 仍阻断管理和插件路径。

## Out of Scope

- 不实现新的 Codex 窗口识别机制。
- 不删除历史 usage/codex summary 账本。
- 不切换 `/v1/responses` 执行链路。
