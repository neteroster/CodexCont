# Governor CPAMP style alignment

## Goal

Align the self-owned CPA Governor user/admin pages with the CPAMP admin
visual language and fix the user CodexCont protection table ordering.

The user-facing result should make `https://cpa-usage.konbakuyomu.us/` feel
like part of the same operations product as CPAMP: restrained dark theme,
compact cards, stable tables, modest status feedback, and newest requests at
the top in every request-like table.

## Requirements

- Only modify self-owned `cpa-governor` plugin page assets and tests.
- Do not modify CPA, CPAMP, or CPA Key Policy official source, images, or
  runtime data.
- Update the shared custom-page visual tokens to be closer to CPAMP:
  darker flat background, subdued borders, compact panels, muted table header,
  less saturated cyan, and simpler buttons/chips.
- Remove the current custom sweep/glow effects: page topbar sweep, refresh
  button sweep, metric-card bump, and broad row highlight.
- Keep subtle live feedback through small status dots only.
- Keep existing refresh safety behavior: abort stale requests, recover after
  background/idle, and keep latest refresh results authoritative.
- Make `额度与明细` and `思维链保护` use the same table, detail-card, chip, and
  button style.
- Make user `思维链保护` sort newest requests first. The visible time order
  should match `额度与明细` and CPAMP request monitoring.
- Make the top-right `活跃` count mean the number of currently visible,
  non-stale `processing` protection requests, not a backend counter that can be
  left high after abnormal communication.
- Add the same `活跃 N` status chip to the user self-service page so `CPA Usage`
  and `CPA Governor` expose the same realtime state vocabulary.
- Treat old `processing` rows as stale so they do not keep the active count high
  forever. Stale rows may remain in history, but they must not be counted as
  active work.
- Keep the user page without a CPAMP sidebar so ordinary users do not see an
  admin-shaped navigation surface.
- Preserve public/admin route boundaries after deployment.

## Acceptance Criteria

- [ ] `CPA Usage` and `CPA Governor` custom pages use one shared CPAMP-like
      dark style and no longer show the current bright cyan sweep/glow theme.
- [ ] Refresh buttons still show `同步中`, `刚刚更新`, and `同步失败`, but without
      sweep animation or broad glow.
- [ ] Small status dots still pulse lightly for connected/syncing/error states.
- [ ] `额度与明细` and `思维链保护` have matching table density, chip shape,
      detail panels, and button style.
- [ ] User `思维链保护` rows are newest-first by request time.
- [ ] Admin `活跃` count equals current non-stale `processing` rows instead of
      stale `status.counters.active_requests`.
- [ ] User page topbar shows `活跃 N` with the same chip style as the Governor
      admin page.
- [ ] Desktop and 390px mobile layouts have no overlapping text and no short
      fields forced vertical.
- [ ] Local Go tests and existing Python regressions pass.
- [ ] Playwright validates desktop/mobile visual shape and the protection row
      order.
- [ ] SJC deployment uploads only the new Governor `.so`, restarts only `cpa`,
      and keeps public `cpa.konbakuyomu.us` admin/plugin routes blocked.

## Out of Scope

- Pixel-perfect copying of CPAMP.
- Adding a CPAMP left sidebar to the ordinary user page.
- Changing CPAMP, CPA, or Key Policy official artifacts.
- Changing the production `/v1/responses` execution path.
