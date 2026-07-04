# Governor UI route cleanup design

## Architecture

The task is a presentation and routing cleanup, not a provider-path migration.
CPA remains the public API entrypoint, CPAMP remains the admin shell, Key Policy
remains the current key source, and CodexCont remains the proven production
continuation service.

Governor keeps two browser resources:

- `/v0/resource/plugins/cpa-governor/admin` renders the admin CodexCont status
  dashboard.
- `/v0/resource/plugins/cpa-governor/user` renders the user self-service page.
  It remains a routable resource for the dedicated `cpa-usage` host, but it no
  longer registers a CPAMP sidebar menu.

Governor no longer exposes daily-use mutable controls inside the admin browser
resource. Low-level plugin configuration stays in CPA plugin metadata and the
CPA plugin configuration drawer.

## Data flow

Admin CodexCont status:

1. CPAMP loads the Governor admin resource.
2. The page fetches snapshots from `/governor/codexcont/admin/status` and
   `/governor/codexcont/admin/requests`.
3. The page opens `EventSource('/governor/codexcont/admin/logs/stream')`.
4. Caddy rewrites those admin-only paths to CodexCont `/admin/*`.
5. Public `cpa.konbakuyomu.us` never exposes these routes.

User page:

1. User enters a full Key Policy `cpa_...` key.
2. Browser sends it in `X-CPA-Governor-Key` to the GET-only session route.
3. Governor resolves the key against the Key Policy mirror and stores only safe
   session state.
4. User quota/details come from Governor's SQLite usage projection.
5. User CodexCont status is filtered by safe key identity where available.

The user page is a single-key monitor inspired by CPAMP realtime monitoring, not
a copy of CPAMP's global monitoring center. Admin/global dimensions such as
account summaries, client-key summaries, provider/account filters, and global
key display controls are intentionally omitted.

Usage-event projection owns the durable request-detail contract. CPA
`UsageRecord` fields such as requested model, provider, executor type,
reasoning effort, service tier, TTFT, and failure status code are stored as
optional columns so old rows remain readable. The browser renders these fields
when present and displays `-` for older records.

## UI contracts

- Use the existing CodexCont dashboard style as the source visual language:
  compact dark topbar, status chips, dot pulse, bounded metric cards, stable
  table widths, and expandable detail panels.
- No refresh-button rotation, diagonal badge, or full-screen marketing layout.
- Manual refresh is modeled as a small state machine (`syncing`, `updated`,
  `error`) instead of a decorative spinner. The syncing state keeps a brief
  minimum visible duration so fast cached/local API responses still communicate
  that a live refresh happened.
- Polling should feel alive without jank: use a thin live sweep, changed
  metric/request-row highlights, and content enter transitions; do not blank
  the table or rebuild the whole visual shell on each tick.
- Tab switches must be local and immediate. The page keeps cached state for
  both tabs, starts an async refresh after switching, and ignores stale fetches
  from older refresh sequences.
- Realtime polling compares stable per-row signatures. If no data changed, the
  existing DOM stays in place; if data changed, only the visible metrics/table
  content is updated and changed rows are highlighted.
- Admin Governor page is read-only. Controls may exist only for local UI state
  such as pause/autoscroll/filter, not for server configuration.
- User page has exactly two main tabs: quota/request details and protection
  status.

## Compatibility and rollback

- Keep existing Governor management APIs for compatibility unless removal is
  required by tests. The admin browser page simply stops exposing those controls.
- Keep `Cache-Control: no-store` for plugin HTML and JSON.
- If embedded SSE fails in CPAMP, the page falls back to snapshot refresh and
  reports reconnecting instead of breaking the page.
- Both custom pages use recoverable browser state. Snapshot fetches have an
  AbortController timeout, foreground resume aborts stale work and starts a new
  snapshot, and SSE connections are closed while hidden and recreated when the
  page becomes visible again.
- Rollback is replacing the previous Governor plugin binary and restoring the
  previous Caddy admin route block from backup.

## Security

- Do not include API keys, OAuth tokens, cookies, request bodies, response
  bodies, encrypted reasoning, or complete key hashes in HTML, JSON, logs, or
  Trellis docs.
- Public API domain blocks plugin resource and admin paths.
- User protection summaries are scoped to the logged-in key. Unknown key
  identity must degrade to "no data" for the user page rather than showing
  global data.
