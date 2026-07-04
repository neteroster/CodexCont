# Implementation Plan

1. Read applicable backend specs before editing.
2. Add/adjust Plus sync tests:
   - CPAMP alias loader accepts bare SHA256 and `sha256:<hex>`.
   - admin key listing triggers native sync and reflects alias changes.
   - default admin list excludes `legacy_plus` and removed/hidden native rows.
3. Patch Plus implementation:
   - normalize CPAMP alias hashes robustly.
   - sync before admin key list and relevant strategy operations.
   - filter default admin projection to current native rows.
4. Add/adjust executor test for `gpt-5.5` upstream alias preserving visible
   model.
5. Patch executor alias/config behavior if needed.
6. Run local checks:
   - `go test ./...` in `cpa_key_policy_plus_plugin/go`.
   - `go test ./...` in `cpa_codexcont_executor_plugin/go`.
   - `git diff --check`.
   - `python ./.trellis/scripts/task.py validate .trellis/tasks/07-03-07-03-native-key-sync-auth-availability`.
7. Build linux/amd64 Plus and executor plugins with existing WSL Go; record
   SHA256.
8. Deploy only changed `.so` files and minimal CPA config alias change.
9. Restart only `cpa`, then verify:
   - plugin logs loaded/registered.
   - Plus admin/default API has official active native key count and aliases.
   - public `/v1/responses` with `model=gpt-5.5` succeeds.
   - public usage portal and public admin blocks remain correct.
10. Commit, push branch, update PR.
