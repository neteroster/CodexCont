# Implementation Plan

1. Read the applicable Trellis backend/spec guidance before editing.
2. Update `cpa_key_policy_plus_plugin/go/assets/user.html`:
   - remove the `rangeSelect` select element and all visibility/onchange logic,
   - replace mutable `state.range` usage with a fixed `PRIMARY_RANGE = "24h"`,
   - keep four-window quota cards from `/me`,
   - add abort detection and ignore aborts inside refresh job error handling.
3. Add regression coverage in `cpa_key_policy_plus_plugin/go/main_test.go` for:
   - no user range dropdown,
   - fixed `range=24h` usage/events requests,
   - visible fixed-24h labels,
   - abort ignore helper presence.
4. Run `go test ./...` from `cpa_key_policy_plus_plugin/go`.
5. Run a final diff review focused on unrelated churn and the user-facing copy.

## Execution Evidence

- `go test ./...` in `cpa_key_policy_plus_plugin/go`: passed.
- `git diff --check`: passed with only CRLF conversion warnings.
- Before deployment, public `https://cpa-usage.konbakuyomu.us/` still served the
  old user HTML:
  - `rangeSelect`: present.
  - `PRIMARY_RANGE`: absent.
- Built linux/amd64 plugin artifact:
  - `cpa_key_policy_plus_plugin/dist/linux/amd64/cpa-key-policy-plus.so`
  - SHA256 `4cfb5cdc0633bb428b8526c9146e6705457d753c240e7417e50a875e2fca4adc`
  - `file`: ELF 64-bit x86-64 shared object.
- SJC deployment:
  - uploaded only the new `cpa-key-policy-plus.so`;
  - backed up prior plugin, Plus SQLite DB, CPA config, and CPA compose file to
    `/opt/codex-stacks/backups/cpa-usage-range-ux-20260703-052620`;
  - replaced `/opt/codex-stacks/cpa/plugins/linux/amd64/cpa-key-policy-plus.so`;
  - restarted only the `cpa` container.
- Production verification after restart:
  - remote plugin SHA256 matches local artifact:
    `4cfb5cdc0633bb428b8526c9146e6705457d753c240e7417e50a875e2fca4adc`;
  - CPA logs show `plugin_id=cpa-key-policy-plus` loaded and registered;
  - public `https://cpa-usage.konbakuyomu.us/` returns `200`;
  - public user HTML now has no `rangeSelect`, has `PRIMARY_RANGE`, has fixed
    `range=${encodeURIComponent(PRIMARY_RANGE)}` requests, and includes
    `refresh_cancelled`;
  - `https://cpa.konbakuyomu.us/healthz` returns `200`;
  - public blocked paths on `cpa.konbakuyomu.us` for Plus resource,
    `/key-policy-plus/`, `/admin/`, and `/codexcont/` return `404`.
