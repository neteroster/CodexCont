# Governor CPAMP style alignment design

## Architecture

This is a presentation and ordering fix inside the self-owned Governor plugin.
The runtime architecture stays unchanged:

- `cpa-governor` serves admin/user HTML through CPA plugin resources.
- `cpa-usage.konbakuyomu.us` exposes only the user resource.
- CPAMP remains the admin shell and visual reference.
- CodexCont remains the production protection data source.

The shared visual system lives in the plugin's embedded assets. Both
`admin.html` and `user.html` consume the same `shared.css`, so style changes
should be made once through shared tokens/classes rather than one-off per page
overrides.

## Visual Contract

- Use a flatter CPAMP-like dark background and panel stack:
  - no radial page glow,
  - restrained panel shadow,
  - dark slate panels,
  - muted grey table headers.
- Keep the product-specific `U` / `G` marks, but make the rest of the surface
  feel like CPAMP's operations UI.
- Remove high-motion effects:
  - `.topbar::after` sweep and `liveSweep`,
  - `.sync-button::after` sweep and `syncSweep`,
  - `.metrics.cards-updated .metric` bump,
  - `rowFresh` broad row animation.
- Preserve only lightweight status-dot pulse (`statusBlink` / `statusPing`) so
  operators still see live state without a strong light strip.
- Keep table layout fixed with horizontal overflow on mobile. Do not shrink
  dense tables until short fields wrap vertically.

## Ordering Contract

User CodexCont summaries can come from two sources:

- live CodexCont `/admin/requests`, filtered by key identity;
- Governor local `codexcont_summaries` fallback.

The user API should return a deterministic newest-first list regardless of
source. Sort by the displayed request time:

1. `started_at`
2. `updated_at`
3. `ended_at`

If parsing fails, keep that row behind rows with valid timestamps while
preserving stable fallback order. The frontend may also apply the same sort as
a defensive display guard, but the backend should own the API contract.

## Active Request Contract

The `活跃` chip is an operator-facing "how many requests are still processing
right now" indicator. It must not directly render CodexCont
`status.counters.active_requests`, because a broken SSE/admin communication
period can leave that counter high long after those rows stop being visible.

Both admin and user pages derive active count from the current request list:

- include only rows whose explicit status or protection is `processing`;
- exclude rows whose latest visible timestamp is older than a short stale
  threshold;
- keep stale processing rows in the history table if returned by the API, but
  do not count them as active.

The timestamp priority for stale detection mirrors sorting: `updated_at`,
`started_at`, then `ended_at`. This keeps "currently being updated" rows alive
while preventing old abnormal rows from pinning `活跃` forever.

## Compatibility

- Keep HTML response cache behavior and refresh recovery logic unchanged.
- Keep existing class names where tests or JS rely on them, but change their
  visual effect to CPAMP-like styling.
- Existing screenshot artifacts are not tracked; new Playwright artifacts stay
  under ignored `artifacts/`.
- Deployment rollback is replacing the previous
  `/opt/codex-stacks/cpa/plugins/linux/amd64/cpa-governor.so` from backup and
  restarting `cpa`.

## Security

- Do not add or print raw keys, cookies, Authorization headers, OAuth tokens,
  or encrypted reasoning content.
- User protection rows remain scoped to the logged-in Key Policy key.
- Public API host must keep blocking plugin/admin/governor/codexcont paths.
