# CodexCont Status Dashboard

## Goal

Add a lightweight, server-side CodexCont dashboard that shows current service health, request/continuation metrics, and real-time logs for the production CodexCont sidecar. The page must help verify the live CPA chain used by this Codex conversation without modifying CPA or storing persistent logs on the small SJC disk.

## Confirmed Facts

- CodexCont is a Python Starlette proxy currently serving `/v1/responses`.
- Production traffic is routed as `cpa.konbakuyomu.us/v1/responses -> CodexCont -> CPA -> OpenAI`.
- CPA remains the official image; CodexCont owns the 516/518n-2 continuation mitigation.
- Admin access already works through `cpa-admin.konbakuyomu.us` via Cloudflare Tunnel + Cloudflare Access + CPA management key.
- SJC disk is small, so first version must avoid persistent logs, databases, Docker prune, and broad cleanup.
- This Codex conversation is expected to use the same production path, so the dashboard should be able to show live logs from real ongoing chat traffic.
- Current dashboard v1 is English-first and log-first. It exposes raw events such as `fold_start`,
  `round_decision`, `continuation_opened`, and `request_finished`, but a beginner cannot quickly tell
  whether a request was protected, clean, automatically continued, risky, or failed.

## UX Refinement Request

- Convert the dashboard to Chinese-first copy.
- Make the first screen explain operational state in beginner-readable terms, without requiring the user
  to understand internal event names or raw fields.
- Promote request-level protection status above raw logs:
  - protected and clean: the request passed through CodexCont folding/protection and did not hit the
    516/518n-2 truncation fingerprint.
  - auto-continued: CodexCont detected the 516/518n-2 fingerprint and opened a hidden continuation round.
  - risky/unhandled: the truncation fingerprint was seen but continuation could not be opened because a
    guard stopped it.
  - failed: request or upstream handling failed.
- Keep raw logs available as an advanced detail view for debugging.

## Requirements

- R1: Add read-only admin routes inside CodexCont:
  - `GET /admin/healthz`
  - `GET /admin/status`
  - `GET /admin/logs`
  - `GET /admin/logs/stream`
  - `GET /admin/`
- R2: Track in-process metrics: uptime, active requests, total requests, continuation count, 516/518n-2 truncation hits, failure count, last request/continuation/error timestamps, and upstream CPA health.
- R3: Add an in-memory ring buffer for structured, redacted operational events. Do not record request bodies, authorization headers, API keys, OAuth tokens, or encrypted reasoning content.
- R4: Stream logs to the browser with SSE so the page updates without manual refresh.
- R5: Provide a compact operational frontend with status cards, metrics, live log table, filters, pause/autoscroll controls, and mobile-safe layout.
- R6: Expose the page only through `https://cpa-admin.konbakuyomu.us/codexcont/`; keep public `https://cpa.konbakuyomu.us` from exposing `/admin` or `/codexcont`.
- R7: Keep logs memory-only by default, with a bounded retention size around 500-1000 entries.
- R8: Preserve existing `/v1/responses` behavior and current continuation semantics.
- R9: Add Chinese dashboard labels and beginner-readable status explanations.
- R10: Add a request-centric view that groups events by request id and surfaces protection state, model,
  rounds, reasoning token counts, continuation count, and final status.
- R11: Clearly distinguish "经过 CodexCont 保护但无需续写" from "检测到 516/518n-2 并已自动续写".

## Acceptance Criteria

- [x] Trellis artifacts record PRD, design, implementation steps, deployment evidence, and residual risks.
- [x] Local tests pass for ring buffer retention, subscriber broadcast, redaction, admin route smoke, and existing middleware behavior.
- [x] Local frontend check confirms the dashboard renders without overlapping text on desktop and mobile viewports.
- [x] `GET /admin/status` reports live metrics and redacted config summary.
- [x] `GET /admin/logs/stream` emits live SSE events when requests flow through CodexCont.
- [x] On SJC, `https://cpa-admin.konbakuyomu.us/codexcont/` opens through the existing Cloudflare Access path.
- [x] This active Codex conversation or a real `/v1/responses` request appears in the dashboard live logs.
- [x] `https://cpa.konbakuyomu.us/admin/` and `https://cpa.konbakuyomu.us/codexcont/` are not publicly exposed.
- [x] No secrets are printed or committed, no persistent log store is added, and no Docker prune or bulk deletion is used.
- [x] Dashboard first screen is Chinese-first and readable for non-technical users.
- [x] Recent requests show beginner-readable protection status without opening raw logs.
- [x] Requests that triggered automatic continuation are visually distinct from clean protected requests.

## Out Of Scope

- CPA plugin implementation for v1.
- Long-term historical analytics, log database, login system, or multi-user RBAC.
- Changing CPA auth/account scheduling, OpenAI OAuth tokens, or 516 continuation logic beyond instrumentation hooks.
- Deleting old stack data or cleaning server disk outside explicitly named files.
