# CPA Governor Plugin

CPA Governor is a self-owned CLIProxyAPI plugin for this repository. It keeps
official CPA, CPAMP, and CPA Key Policy binaries untouched while moving user
quota, usage projection, and CodexCont status into a CPA-native plugin surface.

## Safety Mode

The first production rollout is intentionally conservative:

- `codexcont_enabled: true` means Governor can read CodexCont Engine health and
  show status in the plugin UI.
- `codexcont_route: false` keeps real `/v1/responses` execution on the current
  known-good production path.
- Turn `codexcont_route` on only after executor-level continuation has been
  validated with a test key. Until then, Caddy's current CodexCont sidecar route
  remains the fallback.

## Local Test

```powershell
cd D:\Dev\20_Software\23_Reference\llm-gateway\CodexCont\cpa_governor_plugin\go
go test ./...
```

## Linux Build

The CPA production host is Linux amd64. Build a shared object with cgo:

```bash
cd cpa_governor_plugin/go
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -tags cliproxy_plugin -buildmode=c-shared -o cpa-governor.so .
```

The host discovers plugin files from `plugins.dir` and the platform subdirectory,
for example:

```text
/CLIProxyAPI/plugins/linux/amd64/cpa-governor.so
```

If the Windows host only has a non-linux Go toolchain, build from WSL with a
temporary linux/amd64 Go toolchain instead. Keep the resulting `dist/` artifact
out of git and record its SHA256 in the Trellis task.

## Minimal CPA Config

```yaml
plugins:
  enabled: true
  dir: /CLIProxyAPI/plugins
  configs:
    cpa-governor:
      enabled: true
      priority: 20
      exclusive_auth: true
      state_db_path: /CLIProxyAPI/plugin-state/cpa-governor/governor.sqlite
      key_policy_state_path: /CLIProxyAPI/plugin-state/cpa-key-policy-state.json
      session_secret: ${CPA_GOVERNOR_SESSION_SECRET}
      codexcont_enabled: true
      codexcont_route: false
      codexcont_url: http://codexcont:8787
      fail_mode: fallback
```

Use `exclusive_auth: true` only when Governor is expected to participate in
frontend auth. Current production key policy has moved to `cpa-key-policy-plus`,
which hard-blocks disabled, disallowed-model, over-quota, and over-RPM user
keys. Governor no longer enforces request concurrency.

## Production Routes

The first SJC rollout uses the plugin as the unified UI and usage surface:

- Admin page: `https://cpa-admin.konbakuyomu.us/governor/`
- User page: `https://cpa-usage.konbakuyomu.us/`

The public API host must keep plugin/admin paths blocked. The user host should
only expose the Governor user resource and user APIs; the admin resource must
return 404 there.

## User Key Login

The user page accepts the full Key Policy `cpa_...` key. Native CPA `sk...`
keys and shortened previews are rejected with explanatory messages because they
are not the quota-managed user identity in this deployment.

CPA plugin resource routes are GET-only in the current host. The user login
request therefore calls `/user/api/session` with `GET` and passes the key only
through the `X-CPA-Governor-Key` header so embedded CPAMP pages do not confuse
it with the admin shell's own `Authorization` header. Do not put user keys in
query strings.

The CPAMP sidebar entry named `CPA Governor` and the direct
`https://cpa-admin.konbakuyomu.us/governor/` route are the same admin page.
Prefer the CPAMP sidebar for normal administration; the direct route is a
convenience/debug entrypoint, not a second system.
