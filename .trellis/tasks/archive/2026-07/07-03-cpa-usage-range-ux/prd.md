# Fix CPA usage range UX

## Goal

Make the Key Policy Plus user usage dashboard less misleading by removing the
time-range dropdown and fixing refresh-cancel noise. The user page should present
one clear default live view for the last 24 hours, while still showing the
important quota windows side by side.

## Requirements

- The user page must no longer expose the `5h / 24h / 7d / month` range dropdown.
- The primary usage summary and request table must use the last 24 hours.
- The dashboard must keep the existing four-window quota visibility for `5H`,
  `24H`, `7D`, and `month` so users can compare quota risk without switching
  controls.
- Existing user/admin APIs and backend range calculations must remain available
  and compatible.
- Canceled in-flight refresh requests caused by a newer refresh, page focus, or
  visibility transition must not render as "usage sync failed" or "protection
  sync failed" notices.

## Acceptance Criteria

- [x] The Key Policy Plus user HTML contains no `rangeSelect` dropdown or range
      onchange handler.
- [x] The user page requests `/usage?range=24h` and `/events?range=24h&limit=100`
      for the live usage view.
- [x] Visible labels for the main usage card, request table heading, and request
      detail range read as `24 小时`.
- [x] The four-window quota cards remain visible, including `24H / 7D` and
      `5H / 本月`.
- [x] Abort/cancel errors are ignored by user-page refresh logic and do not set
      usage/protection sync error notices.
- [x] `go test ./...` passes in `cpa_key_policy_plus_plugin/go`.

## Notes

- Evidence from live HTML showed the production page is served by the Key Policy
  Plus user surface, not the older Python usage portal.
- Scope excludes the admin page range selector and backend quota/window logic.
