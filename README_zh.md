# CodexCont

用于 Codex / OpenAI Responses 兼容 API 的“继续思考”中间件。

本项目是一个轻量 Starlette 代理，部署在编码代理和上游 Responses 接口之间。它会检测一种已知的推理截断指纹：`usage.output_tokens_details.reasoning_tokens == 518 * n - 2`。检测到后，中间件会在后台让模型继续思考，并把多轮上游流式响应折叠成一个连贯的下游 SSE 响应。

```text
编码代理  ->  CodexCont  ->  Codex / Responses API
```

> **用 AI Agent 安装？** 把 [`INSTALL-GUIDE-AGENT/AGENT.md`](INSTALL-GUIDE-AGENT/AGENT.md) 交给你的 Agent —— 这是一份专为 AI Agent 在你机器上逐步执行而写的安装手册。

## 免责声明

本项目是对已观察到的 OpenAI Codex 推理截断机制的明确绕过。若使用本中间件的行为被视为滥用、违反服务条款、导致费用异常增加，或造成其他不良后果，均由使用者自行承担责任。

## 功能概览

- 实时向下游转发 reasoning 项，保留“正在思考”的体验。
- 在上游 terminal event 出现前，缓存暂定的最终输出（`message` 和 `function_call`）。
- 如果本轮被判定为截断，丢弃暂定输出，并携带已产生的 reasoning 打开下一轮续写请求。
- 如果本轮自然完成或触发安全上限，冲刷最终一轮的输出，并发出一个重构后的 terminal response。
- 对不符合条件的请求透明透传。

默认续写方式是隐藏的 `phase: "commentary"` assistant 消息（`"Continue thinking..."`）。也支持旧版的合成工具调用对（`tool_pair`）模式。

## 环境要求

- Python `>= 3.12`
- 推荐使用 [`uv`](https://docs.astral.sh/uv/)

运行依赖在 `pyproject.toml` 中声明：

- `httpx`
- `starlette`
- `uvicorn`

## 快速开始

```bash
uv sync
cp config.example.toml config.toml
uv run python run.py
```

`run.py` 会读取本地 `config.toml`；请先从 `config.example.toml` 复制一份，再按需调整。

示例默认服务监听 `127.0.0.1:8787`，接受以下路径的 POST 请求：

- `/v1/responses`

也可以直接使用当前虚拟环境运行：

```bash
# Windows / 本工作区 Git Bash
.venv/Scripts/python.exe run.py
```

## 将客户端指向代理

把原本的上游接口地址替换为本代理地址即可。

示例：

```text
http://127.0.0.1:8787/v1/responses
```

示例默认配置（`config.example.toml`，复制为 `config.toml` 后使用）为：

```toml
[upstream]
url = "https://chatgpt.com/backend-api/codex/responses"
mode = "header"
```

当 `mode = "header"` 时，请求头 `Responses-API-Base` 会覆盖配置中的 `url`；如果没有该请求头，则回退到配置的 Codex URL。

例如，要指向通用 Responses 兼容端点，可以发送：

```text
Responses-API-Base: https://api.openai.com/v1
```

中间件会自动追加 `/responses`；如果传入值已经以 `/responses` 结尾，则保持不变。该控制头不会被继续转发到上游。

## 鉴权

`config.toml` 支持三种鉴权模式。示例默认值是 `passthrough`：

```toml
[auth]
mode = "passthrough"               # passthrough | inject | passthrough_then_inject
access_token = ""                  # 作为 Authorization: Bearer <access_token> 发送
chatgpt_account_id = ""            # 非空时作为 chatgpt-account-id 发送
```

模式说明：

- `passthrough`：只转发调用方提供的鉴权头，不注入配置中的凭据。
- `inject`：使用配置中的凭据设置/覆盖鉴权头。
- `passthrough_then_inject`：调用方已有鉴权头则保留；没有时才用配置中的凭据补上。

安全保护：如果请求使用 `Responses-API-Base` 指定了上游地址，中间件不会把配置中的凭据泄露到这个由请求方指定的 URL。如果当前鉴权模式会为该请求注入配置中的凭据，请求会被 `400` 拒绝。若要对每个请求动态指定上游并使用凭据，请让调用方自己携带 `Authorization`，并使用 `mode = "passthrough"` 或 `mode = "passthrough_then_inject"`。

不要提交密钥。`.gitignore` 已忽略 `rt.json` 和 `free_rt.json`；如果把 token 写入 `config.toml`，也请谨慎管理。

## 状态面板

CodexCont 内置一个只读状态面板：

```text
http://127.0.0.1:8787/admin/
```

它可以查看服务状态、上游健康、内存中的请求指标，以及通过 SSE 实时推送的脱敏日志。当前面板优先展示“最近请求”的中文保护结果，能直接区分：

- `已保护 / 无需续写`：请求进入 CodexCont 保护链，未命中 516/518n-2 指纹。
- `已自动续写`：检测到 516/518n-2，并已打开隐藏续写轮。
- `疑似截断 / 未续写`：检测到截断指纹，但保护条件或上限阻止了续写。
- `透传` / `失败` / `不完整`：分别表示未进入折叠保护、请求失败、上游未完整结束。

面板里的“末轮思考量”只表示最后一轮上游响应的 reasoning token；如果某个请求被自动续写，真正触发续写的 516/518n-2 会显示在“命中轮”列里。

日志和最近请求都只保存在进程内存中，默认日志上限为：

```toml
[admin]
max_log_events = 800
key_policy_state_path = "" # 可选：填 CPA Key Policy state JSON 路径后显示安全的 Key 名称
```

Docker 部署时这里必须填容器内部可读到的路径。例如把 CPA plugin state
只读挂载到容器内，然后设置
`key_policy_state_path = "/data/plugin-state/cpa-key-policy-state.json"`。

不要把 `/admin/` 直接暴露在公网 API 域名上；生产环境应放在 Cloudflare Access 这类外层访问控制之后。

## CPA 用量自助页

本仓库还包含一个独立的 CPA 用量自助页 sidecar：

```bash
.venv/Scripts/python.exe run_usage_portal.py
```

它不是 CPA fork，也不是 CPAMP 的替代品。推荐生产链路是：

```text
CPA Key Policy 负责 key 和限额
CPAMP 负责请求级用量采集
cpa_usage_portal 只给普通用户看自己的用量
```

关键环境变量：

```text
CPA_USAGE_PORTAL_KEY_POLICY_STATE=/data/cpa-key-policy-state.json
CPA_USAGE_PORTAL_CPAMP_URL=http://cpamp:18317
CPA_USAGE_PORTAL_CPAMP_ADMIN_KEY_FILE=/run/secrets/cpamp_admin_key
CPA_USAGE_PORTAL_SESSION_SECRET_FILE=/run/secrets/session_secret
```

用户只在登录时提交自己的 `cpa_...` API Key。服务端会用 `sha256` 校验 Key Policy state，并在 HttpOnly cookie 中只保存用于会话校验的哈希和安全元数据；页面和接口不会返回原始 key、完整 key hash、OAuth token、CPA/CPAMP 管理密钥、请求正文、响应正文或 encrypted reasoning 内容。

Docker 部署模板在：

```text
deploy/cpa-usage-portal/
```

## CPAMP 与 Key Policy 配合

计划中的生产组合是：

- CPAMP：管理员面板和请求级监控，放在 `cpa-admin.konbakuyomu.us` + Cloudflare Access 后面。
- CPA Key Policy：生成 `cpa_...` key，配置每个 key 的模型权限、RPM、每日/每周 USD 限额。
- CPA 用量自助页：普通用户用自己的 key 登录，只能看自己的用量和最近请求。

Key Policy 有两个容易混淆的标识：

- `key_hash`：原始 `cpa_...` key 的 `sha256:<hex>`，只用于登录校验。
- `id`：Key Policy 交给 CPA 的 Principal；CPAMP monitoring 里的 `api_key_hash` 实际是 `sha256(id)`。

因此门户登录时用原始 key hash 找到 Key Policy 记录，查询 CPAMP 时改用该记录 `id` 的 hash。所有 CPAMP 查询都会强制带当前登录 key 对应的 `api_key_hash` 过滤。

### Key 体系

生产上推荐把普通用户统一迁移到 Key Policy 生成的 `cpa_...` key：

```text
Codex Base URL: https://cpa.konbakuyomu.us/v1
Codex API Key:  cpa_...
用量自助页:     https://cpa-usage.konbakuyomu.us/
```

CPA 原生 `api-keys` 里的 `sk...` key 只建议保留为管理员兼容/救急入口，不作为普通用户分发体系。它不天然带 Key Policy 的用户身份、模型 allowlist、RPM、每日/每周限额，也不作为用量自助页的登录凭据。

管理面板也有两类密钥：

- CPAMP 管理面板登录使用 CPAMP admin key。
- CPA 原生 management API 使用 CPA management key。

当前 `https://cpa-admin.konbakuyomu.us/management.html` 指向 CPAMP，所以登录它需要 CPAMP admin key，而不是 CPA management key。

### 维护边界

CPA、CPAMP、CPA Key Policy 都保持官方来源，不在本仓库二开：

- CPA：官方镜像 `eceasy/cli-proxy-api:latest`。
- CPAMP：官方镜像 `seakee/cpa-manager-plus:latest`。
- CPA Key Policy：官方 release 的 `cpa-key-policy.so`，挂载到 CPA 插件目录。

本仓库自定义维护的只有：

- `CodexCont`：负责 516/518n-2 续写保护。
- `cpa_usage_portal`：普通用户用量自助页。

服务器上它们是独立 stack / 独立容器，后续可以分别更新 CPA、CPAMP、Key Policy、CodexCont 和用户自助页。

## 什么时候会执行续写折叠

只有同时满足以下条件时，中间件才会执行折叠逻辑：

- `[continue].enabled = true`
- 请求体是 JSON 对象
- `stream` 为真
- reasoning 没有被显式禁用（`"reasoning": false` 会关闭折叠）
- 使用 `method = "tool_pair"` 时，请求没有声明与 `[continue].continue_tool_name` 同名的真实工具

其他请求会作为普通流式请求透明透传。

## 续写逻辑

每个上游 round 的处理流程：

1. reasoning 相关事件实时转发，并重写 `sequence_number` 和 `output_index`。
2. message 和 function-call 事件作为暂定输出缓存起来。
3. 收到 terminal event 后读取 `usage.output_tokens_details.reasoning_tokens`。
4. 如果 token 数匹配 `518 * n - 2`，位于配置的 tier 窗口内，存在 encrypted reasoning content，且安全上限允许，则中间件会：
   - 丢弃本轮暂定输出；
   - 把本轮 reasoning 和续写标记追加到下一轮请求 input；
   - 打开新的上游流式 round。
5. 否则，冲刷最终缓存的输出，并发出重构后的 terminal event。

下游编码代理只会看到一个 response；隐藏轮次的细节会写入最终响应的 metadata。

## 响应 metadata

最终重构响应会包含代理相关 metadata，例如：

- `metadata.proxy_rounds`：每轮 reasoning token 数和检测出的 tier `n`。
- `metadata.proxy_billed_usage`：隐藏上游轮次的真实累计 token 用量。
- `metadata.proxy_stopped_reason`：当续写因上限或错误停止时出现。

下游可见的 `usage` 会被重构得像单个 response：输入/缓存 token 取第一轮，reasoning token 累加，最终一轮的非 reasoning 输出计入 output。

## 测试

测试套件是自包含的，不依赖 `pytest`：

```bash
uv run python tests/test_middleware.py
# 或
.venv/Scripts/python.exe tests/test_middleware.py
```

当前离线测试覆盖：

- 截断数学判断
- 增量 SSE 解析
- 基于抓包 fixture 的折叠/重写行为
- commentary 和 tool-pair 两种续写 payload
- header 透明转发
- 上游 URL 解析
- 鉴权安全保护
- dashboard diagnostics 和 admin route smoke
- CPA 用量自助页的 key hash/session/过滤/脱敏/retention 行为
- EOF / 上游错误处理

## 项目结构

```text
middleware/
  admin.py     # 只读状态面板 / admin 路由
  app.py       # Starlette 应用和路由处理
  codex.py     # 截断数学和续写 payload 构造
  config.py    # config.toml 加载和 dataclass 配置
  creds.py     # 上游 header / auth 构造
  dashboard.html # 静态状态面板页面
  diagnostics.py # 内存指标、环形日志和 SSE 订阅
  proxy.py     # fold_stream 状态机
  sse.py       # 增量 SSE 解析和序列化
  store.py     # 可选 stateful repair 使用的内存 ID 存储

cpa_usage_portal/
  app.py       # 用户用量自助页 Starlette 应用
  key_policy.py # CPA Key Policy state 只读解析
  cpamp.py     # CPAMP monitoring API client
  redaction.py # 用户可见事件脱敏投影
  retention.py # CPAMP SQLite 7 天保留辅助
  static/dashboard.html # 中文自助页

tests/
  test_middleware.py
  test_cpa_usage_portal.py
  fixtures/

run.py         # uvicorn 入口
run_usage_portal.py # CPA 用量自助页入口
config.example.toml # 示例运行配置；复制为 config.toml 后本地使用
```

## 限制

- 最终答案文本会被缓存到 terminal round 证明未截断之后才发出，因此最终答案首 token 延迟可能高于普通流式请求。
- 非流式请求当前会透传，不进行折叠。
- 截断检测器是针对已观察到的 `518 * n - 2` 指纹设计的。
- 可选的 `repair_followup = "stateful"` 使用进程内内存状态；多代理实例之间不会共享。

## 致谢

感谢 [LINUX DO](https://linux.do) 社区的相关讨论，没有这些讨论，也就没有本项目。特别感谢 LINUX DO 社区的 @shinorochi 和 @dskdkj 一同明确截断机制和 GPT 的思考模型，感谢 @shinorochi 提出的基于 commentary 输入而非工具调用伪造的更好方案。
