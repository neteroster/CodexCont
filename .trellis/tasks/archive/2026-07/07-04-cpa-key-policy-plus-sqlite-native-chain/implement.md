# Implementation Plan

1. Load backend specs and relevant archived task evidence.
2. Add SQLite open helpers in Plus store code:
   - main writable DB with busy timeout, WAL, and single-connection pool;
   - read-only DB helper for CPAMP aliases and executor summaries;
   - legacy import helper with busy timeout.
3. Adjust native sync defaults so brand-new official native keys are enabled
   immediately unless an ambiguous inheritance conflict requires attention.
4. Update admin projection/UI text so current-key counts and missing-limit hints
   are clear.
5. Add/adjust tests for SQLite discipline, native lifecycle, UI strings, and
   admin API mirror behavior.
6. Run local validation:
   - `go test ./...` in `cpa_key_policy_plus_plugin/go`
   - executor tests if touched
   - `git diff --check`
   - Trellis task validation
7. Build linux/amd64 plugin with the existing local Go runtime, not a new Go
   download.
8. Deploy cautiously:
   - check SJC disk;
   - backup current Plus `.so`;
   - upload replacement;
   - restart only CPA.
9. Production acceptance:
   - repeat Plus admin keys/models API calls and check logs for no `SQLITE_BUSY`;
   - verify key count/aliases match current official config;
   - verify usage portal 200 and user APIs;
   - smoke `/v1/responses` with enabled native key and `gpt-5.5`;
   - verify public admin/resource paths remain 404.
10. Update spec with durable SQLite/native-key contract, commit, push, archive
    task, and record journal evidence.
