# CPA CodexCont Executor Plugin

`cpa-codexcont-executor` is an executor-only CPA plugin that replaces the old
Docker-hosted CodexCont sidecar for streaming Responses continuation
protection.

It does not own the ordinary user portal. `cpa-key-policy-plus` remains the
owner of `https://cpa-usage.konbakuyomu.us/`, user login, quotas, RPM, and usage
APIs.

It does own the read-only CPAMP-side `CodexCont Executor` monitor. That page is
for rolling protection summaries and executor health only; it must not expose
user login, key editing, quota editing, raw request/response bodies, or
encrypted reasoning.

The plugin declares non-exclusive `frontend_auth_provider=true` and
`usage_plugin=true` only so CPA/CPAMP registers its resource menu.
`frontend_auth.authenticate` always returns unauthenticated and `usage.handle`
is intentionally a no-op; billing, quota, RPM, and user usage remain owned by
`cpa-key-policy-plus`.

## Local Test

```powershell
cd D:\Dev\20_Software\23_Reference\llm-gateway\CodexCont\cpa_codexcont_executor_plugin\go
go test ./...
```

## Linux Build

```bash
cd cpa_codexcont_executor_plugin/go
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -tags cliproxy_plugin -buildmode=c-shared -o cpa-codexcont-executor.so .
```

## Minimal CPA Config

```yaml
plugins:
  enabled: true
  dir: /CLIProxyAPI/plugins
  configs:
    cpa-codexcont-executor:
      enabled: true
      route_enabled: false
      state_db_path: /CLIProxyAPI/plugin-state/cpa-codexcont-executor/executor.sqlite
      fail_mode: fallback
```

`route_enabled: false` leaves CPA's normal upstream route untouched. Turn it on
only after local and server executor folding validation passes.

Both `plugin.register` and `plugin.reconfigure` must return the full plugin
registration object. CPA decodes reconfigure through the same metadata and
capability path; a lightweight acknowledgement causes CPA to drop the plugin
from the active resource snapshot.

## CPAMP Monitor

The plugin registers a read-only admin resource:

```text
/v0/resource/plugins/cpa-codexcont-executor/admin
```

The page polls the plugin's safe status and summary endpoints and is intended
to replace Governor's CodexCont realtime monitoring role during cleanup.
