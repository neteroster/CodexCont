# Design

## User Page Contract

The Key Policy Plus user dashboard uses a fixed primary observation window:
`24h`. The top toolbar no longer has a time-range selector. The first-page
summary cards still show side-by-side quota windows from `/me`, while the live
usage summary and request table are backed by `/usage?range=24h` and
`/events?range=24h&limit=100`.

## Data Flow

- `/user/api/me` remains the source for key identity, configured limits, and
  four-window quota usage.
- `/user/api/usage?range=24h` remains the source for current live usage metrics.
- `/user/api/events?range=24h&limit=100` remains the source for the request table.
- Existing backend range handling stays intact for admin surfaces and future API
  consumers.

## Error Handling

The refresh code already cancels active fetches when a forced refresh supersedes
an in-flight one or when the page is hidden. Abort errors from that cancellation
path are control flow, not user-visible failures. Refresh jobs must treat aborts
as ignored stale work and avoid writing `state.errors.usage`,
`state.errors.protection`, or connection-failure UI.

## Compatibility

This is a user-page UX change only. It does not remove API range parameters,
database columns, quota windows, reset watermarks, or admin controls.
