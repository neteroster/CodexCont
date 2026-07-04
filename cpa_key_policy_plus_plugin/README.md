# CPA Key Policy Plus Plugin

`cpa-key-policy-plus` is the self-owned replacement for the old Key Policy
plugin. It owns ordinary user `cpa_` keys, per-key limits, usage projection,
soft resets, model allowlists, RPM policy, and rolling quota windows.

It does not modify CPA, CPAMP, or the old Key Policy source/image.

Concurrency and Codex active-window limits were intentionally retired because
normal Codex conversations can trip them too easily. Old database fields remain
for schema compatibility, but new saves force them to `0` and frontend auth does
not enforce them.

## Safety Boundary

Plus is safe to deploy as the exclusive user-key auth and quota authority. The
public `/v1/responses` route should stay on the current known-good CodexCont
sidecar path until the separate `cpa-codexcont-executor` plugin has passed a
controlled server validation. The current CodexCont Engine API is
status/summary only.

## Local Test

```powershell
cd D:\Dev\20_Software\23_Reference\llm-gateway\CodexCont\cpa_key_policy_plus_plugin\go
go test ./...
```

## Linux Build

```bash
cd cpa_key_policy_plus_plugin/go
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -tags cliproxy_plugin -buildmode=c-shared -o cpa-key-policy-plus.so .
```

The production CPA host loads platform artifacts from the configured plugin
directory, for example:

```text
/CLIProxyAPI/plugins/linux/amd64/cpa-key-policy-plus.so
```

## Minimal CPA Config

```yaml
plugins:
  enabled: true
  dir: /CLIProxyAPI/plugins
  configs:
    cpa-key-policy-plus:
      enabled: true
      priority: 10
      exclusive_auth: true
      state_db_path: /CLIProxyAPI/plugin-state/cpa-key-policy-plus/policyplus.sqlite
      key_policy_state_path: /CLIProxyAPI/plugin-state/cpa-key-policy-state.json
      legacy_quota_db_path: /CLIProxyAPI/plugin-state/cpa-usage-portal/usage-portal.sqlite
      governor_state_db_path: /CLIProxyAPI/plugin-state/cpa-governor/governor.sqlite
      codex_summary_db_path: /CLIProxyAPI/plugin-state/cpa-codexcont-executor/executor.sqlite
      session_secret: ${CPA_KEY_POLICY_PLUS_SESSION_SECRET}
      codexcont_enabled: true
      codexcont_route: false
      codexcont_url: http://codexcont:8787
      fail_mode: fallback
```

Disable the old `cpa-key-policy` only after Plus loads and current `cpa_` keys
authenticate successfully.

## User Page

`cpa-usage.konbakuyomu.us` should expose only the Plus user resource and
`/user/api/*` compatibility routes. User login uses the full `cpa_` key in the
`X-CPA-Key-Policy-Plus-Key` header. Native `sk...` keys and shortened previews
are rejected.
