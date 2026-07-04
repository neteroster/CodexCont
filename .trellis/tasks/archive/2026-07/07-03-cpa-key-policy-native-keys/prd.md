# CPA Key Policy+ native key policy layer

## Goal

Make `CPA Key Policy+` a passive policy layer over CPA native `sk-...` API
keys. CPA/CPAMP owns key creation, deletion, full-key copy, and alias editing.
Plus owns policy, quota, usage projection, and the user usage portal.

## Requirements

- Plus must continue to own `https://cpa-usage.konbakuyomu.us/`,
  `/user/api/*`, usage windows, RPM, pricing, and protection summary display.
- CPA/CPAMP native API keys are the source of truth. Plus syncs them from CPA
  config and reads CPAMP aliases; it must not store raw keys.
- Plus stores only safe key identity: native hash, preview, source flags, and
  read-only alias/name.
- New native keys default to disabled.
- A new native key with the same alias as exactly one removed historical policy
  inherits that old policy and enabled state, but starts with a new ledger.
- Removed native keys are marked `source_present=false`, disabled, hidden by
  default, and retained for usage/protection history.
- Plus admin UI changes from "Key management" to "Key policy": no create,
  delete, rotate, full-key copy, or alias edit controls.
- User page login must use native `sk-...` keys; old `cpa_...` keys should
  fail with clear migrated/retired guidance.
- RPM and quota denials must produce explicit OpenAI-compatible `429` errors
  with Chinese window/key/used/limit details, not a generic CPA auth failure.
- Cost limits use post-accounting blocking: when current window usage is
  already `>= limit`, the next request is denied.
- Denial priority is: missing policy, removed/disabled, model allowlist, RPM,
  then quota windows `5h`, `24h`, `7d`, `month`.

## Acceptance Criteria

- [ ] Native key sync reads top-level CPA `api-keys` and CPAMP
      `api_key_aliases`.
- [ ] Store migration adds safe native-key fields and preserves existing rows.
- [ ] New native keys are disabled unless they inherit from one unique removed
      same-alias policy.
- [ ] Removed native keys are disabled and hidden by default but keep history.
- [ ] Plus frontend auth accepts native `sk-...` keys and rejects retired
      `cpa_...` self-service keys.
- [ ] Structured policy decisions cover missing, disabled, source-removed,
      model, RPM, and every fee window denial.
- [ ] Deny route/executor returns OpenAI-compatible JSON and SSE error payloads
      with `429` semantics and safe diagnostic headers.
- [ ] Admin HTML no longer contains create/delete/raw-key lifecycle controls.
- [ ] User HTML points users at native `sk-...` keys.
- [ ] `go test ./...` passes in `cpa_key_policy_plus_plugin/go`.

## Constraints

- Do not modify official CPA or CPAMP code.
- Do not store or expose raw keys, full hashes, Authorization headers, cookies,
  request bodies, response bodies, or encrypted reasoning.
- Keep executor plugin ownership separate; this task only changes Plus.
