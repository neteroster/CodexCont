# Governor UI route cleanup

## Goal

Clean up the CPA Governor UI and routing model so each entrypoint has one clear
job:

- CPAMP `配置面板` remains CPAMP-only configuration.
- CPA `插件管理 -> cpa-governor -> 编辑配置` remains low-level Governor plugin
  configuration.
- CPAMP sidebar `CPA Governor` becomes a read-only CodexCont protection status
  dashboard.
- `https://cpa-usage.konbakuyomu.us/` becomes the only daily user
  self-service entry. The Governor user resource remains addressable for the
  dedicated host, but it is no longer advertised as a CPAMP sidebar menu.

The user-facing result is less confusion, a smoother realtime status page, and
one consistent visual language across custom Governor/usage pages.

## Requirements

- Create/maintain this Trellis task with `prd.md`, `design.md`, and
  `implement.md` before implementation.
- Do not modify CPA, CPAMP, or CPA Key Policy upstream source or images.
- Replace the current Governor admin page content with a read-only CodexCont
  protection dashboard modeled after the existing CodexCont panel.
- Remove daily-use Key management, request-detail, and CodexCont setting forms
  from the Governor admin page. Persistent Governor settings belong in CPA's
  plugin configuration drawer.
- Return 404 for old `https://cpa-admin.konbakuyomu.us/codexcont/` entrypoint
  after migration.
- Add an admin-only data path under `/governor/codexcont/admin/*` that proxies
  to CodexCont `/admin/*` so the embedded Governor page can use the existing
  snapshot and SSE contracts.
- Redesign the user page into two tabs:
  - `额度与明细`: key quota summary plus recent requests with expandable
    request detail.
  - `思维链保护`: the current key's CodexCont protection summaries.
- User views must filter by the logged-in `cpa_` key and must not show other
  users' request or protection records.
- The user page should feel like a single-key, simplified version of CPAMP
  `请求监控 -> 实时监控`: keep total usage stats and per-call rows, but remove
  admin/global dimensions such as account summaries, client-key summaries,
  provider/account filters, and global key display controls.
- The user page's `额度与明细` tab must show quota, usage, recent requests, and
  rich expandable request details in one balanced layout.
- The user page's `思维链保护` tab must use the same table/detail visual system
  as `额度与明细`, scoped to the current key's CodexCont protection records.
- Remove the visible refresh spinner / diagonal animation style. Use CodexCont
  style realtime chips and dot pulse animation.
- User page manual refresh must provide a visible realtime rhythm: a short
  `同步中` state, a completion confirmation, a subtle live sweep, and changed
  row/card highlights. Fast local responses must not collapse the animation
  into an imperceptible instant update.
- Tab switching must be immediate and must not wait for network responses.
- Avoid full table re-render loops where possible. Admin status uses SSE; the
  user page uses cached tab state, lightweight visible-tab polling, data
  signatures, and foreground resume so the table does not visibly jitter every
  polling cycle.
- The admin `CPA Governor` CodexCont page and the user page must both survive
  idle/background tabs: no permanent `同步中`/`正在连接` state, stale requests are
  aborted or ignored, and foreground resume forces a fresh snapshot plus stream
  reconnect.
- Keep public `https://cpa.konbakuyomu.us` from exposing admin, plugin resource,
  or CodexCont routes.

## Acceptance Criteria

- [ ] `CPA Governor` sidebar menu renders a read-only CodexCont status page
      with protection result, active requests, hit round, latest reasoning,
      continuation count, failures, and advanced logs.
- [ ] The Governor page contains no Key management tab, request-detail tab, or
      server-side CodexCont save button.
- [ ] `/governor/codexcont/admin/status`, `/requests`, and `/logs/stream` work
      through the admin host, while old `/codexcont/` returns 404.
- [ ] `CPA Usage` no longer appears as a CPAMP sidebar plugin menu, while the
      dedicated `https://cpa-usage.konbakuyomu.us/` entry still serves the
      two-tab user UI.
- [ ] The user UI behaves like a single-key realtime monitor: quota/usage cards,
      per-call rows, rich expand details, and per-key CodexCont protection view
      without account/client-key summary tabs.
- [ ] User page rejects native `sk...` keys and shortened previews as before,
      and valid `cpa_` users only see their own data.
- [ ] Custom pages match the CodexCont dark compact style and do not show the
      old spinning refresh or diagonal badge animation.
- [ ] `CPA Usage` manual refresh visibly transitions through syncing and
      updated states, while realtime polling highlights changed rows/cards
      without a full-page flash.
- [ ] Switching between `额度与明细` and `思维链保护` is immediate, and two polling
      cycles do not cause periodic table reflow or short-field vertical text.
- [ ] After simulating a hung refresh or idle tab, both `CPA Governor` and
      `cpa-usage` recover with manual refresh or foreground resume without a
      full page reload.
- [ ] Local Go and Python tests pass.
- [ ] Playwright verifies desktop and mobile layouts for Governor admin and user
      pages.
- [ ] Production deployment updates only self-owned Governor/Caddy/CodexCont
      routing as needed, with no Docker prune and no official image source
      changes.
- [ ] Server smoke verifies public/admin route boundaries after deployment.

## Notes

- Existing production `/v1/responses` flow stays on the proven CodexCont path.
  This task does not cut over the Governor executor path.
