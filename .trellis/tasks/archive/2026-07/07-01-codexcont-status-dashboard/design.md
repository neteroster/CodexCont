# Design

## Architecture

Dashboard functionality lives inside the existing CodexCont Starlette app. CPA remains an official upstream service and is not forked or extended for this first version.

Production data path remains unchanged:

`Codex -> cpa.konbakuyomu.us/v1/responses -> Caddy -> CodexCont -> CPA -> OpenAI`

Admin view path:

`Browser -> Cloudflare Access -> cpa-admin.konbakuyomu.us/codexcont/ -> cpa-admin-proxy -> codexcont:8787/admin/`

## Backend Contracts

- `GET /admin/healthz` returns `{ "ok": true }` plus process uptime.
- `GET /admin/status` returns process metrics, recent counters, upstream health, and a safe config summary.
- `GET /admin/requests?limit=N` returns recent request-level protection summaries. Each summary is a redacted,
  memory-only projection of internal diagnostics events keyed by the generated request id.
- `GET /admin/logs?limit=N` returns the newest redacted in-memory log events.
- `GET /admin/logs/stream` uses `text/event-stream`; new log events are emitted as `event: log`, and request
  summary updates are emitted as `event: request`.
- `GET /admin/` serves the static dashboard. Static assets can be embedded or served under `/admin/static/...`; no Node build is required.

## Metrics And Logs

- Add a small diagnostics module responsible for:
  - monotonic service start time and uptime
  - bounded ring buffer
  - subscriber queues for SSE
  - request counters and active request tracking
  - continuation/truncation/failure counters
  - log redaction for sensitive-looking strings and headers
- Instrument these points:
  - request accepted / passthrough / fold start
  - round decision with reasoning token count and continuation decision
  - continuation opened
  - request finished cleanly or failed
  - upstream health probe result
- Use generated request IDs for dashboard correlation only; do not expose upstream tokens or reasoning payloads.
- Add a request summary projection inside `Diagnostics` so frontend code does not re-derive protection meaning from raw log fields.
- Request protection result values:
  - `protected_clean`: folded request completed without a truncation fingerprint.
  - `auto_continued`: a truncation fingerprint was detected and a hidden continuation round opened.
  - `risk_uncontinued`: a truncation fingerprint was detected but continuation was blocked by a guard.
  - `passthrough`: request did not enter folding protection.
  - `failed`, `incomplete`, `processing`: failure, incomplete upstream ending, or still active.

## Frontend

- Single static operational dashboard, not a landing page.
- Chinese-first operational copy.
- Compact top band: CodexCont health, CPA health, continuation config, active requests, SSE connection status.
- Metrics grid: total requests, folded/protected requests, continuations, truncation hits, failures.
- Primary table: recent requests with protection result chips, model, rounds, reasoning token count, continuation count, final result, and expandable round details.
- Advanced log table remains available below the primary request view, with filters, pause/autoscroll, clear local view, and Chinese event labels.
- Styling is plain CSS with restrained colors, max 8px card radius, stable dimensions, responsive grid, and no decorative gradient/orb background.

## Deployment Contract

- Public API host `cpa.konbakuyomu.us` must not expose `/admin` or `/codexcont`.
- Existing `cpa-admin-proxy` should route `/codexcont/` to `codexcont:8787/admin/` with path stripping; all other paths continue to CPA management.
- Cloudflare Access remains the outer auth layer. CodexCont admin routes do not implement their own login for v1.
- Server rollout must back up current CodexCont stack and `cpa-admin-proxy` config before changes.
