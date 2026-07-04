# Design

## Architecture

Production API data path:

`Codex client -> cpa.konbakuyomu.us -> caddy-edge -> codexcont:8787 -> cpa:8317 -> CPA Codex executor -> socks5://172.19.0.1:1082 -> OpenAI`

Direct CPA paths remain:

`client -> cpa.konbakuyomu.us -> caddy-edge -> cpa:8317`

Management path:

`browser -> Cloudflare Access -> cpa-admin.konbakuyomu.us -> Cloudflare Tunnel -> 127.0.0.1:8327 -> cpa-admin-proxy -> cpa:8317 -> CPA management panel`

## Runtime Layout

- CodexCont stack: `/opt/codex-stacks/codexcont`
- CodexCont config: `/opt/codex-stacks/codexcont/config.toml`
- CodexCont app source: copied from this repository into `/opt/codex-stacks/codexcont/app`
- CodexCont container: `codexcont`
- CodexCont Docker network: `cpa_net`
- CPA admin tunnel stack: `/opt/codex-stacks/cpa-admin-tunnel`
- CPA admin loopback proxy: `cpa-admin-proxy`, host bind `127.0.0.1:8327`
- CF token file: `/opt/codex-stacks/cpa-admin-tunnel/.env`, root-only, never committed
- Backup root: `/root/cpa-codexcont-admin-backups/<timestamp>/`

## Caddy Contract

- `cpa.konbakuyomu.us /v1/responses` reverse proxies to `codexcont:8787`.
- `cpa.konbakuyomu.us /management.html`, `/v0/management*`, and `/v0/resource/plugins/*` return a blocking status.
- All other CPA traffic reverse proxies to `cpa:8317`.
- Rollback is restoring the backed-up Caddyfile and reloading Caddy.

## CPA Management Contract

- `remote-management.secret-key` is set to a generated high-entropy secret.
- `remote-management.allow-remote` remains `false`.
- The `config.yaml` bind mount becomes writable because CPA hashes and persists plaintext management keys at startup.
- The plaintext management key is stored in a root-only credential file for the user to retrieve over SSH; task artifacts only record the path.
- Docker-published `127.0.0.1:8317` reaches CPA from a bridge address, so CPA does not treat it as a local client under `allow-remote: false`. The admin proxy keeps `allow-remote: false` by setting local-only forwarding headers before proxying to CPA.

## Cloudflare Tunnel Contract

- The user supplies a Cloudflare Dashboard Tunnel token out of band.
- The cloudflared container runs with host networking and targets `http://127.0.0.1:8327`.
- Cloudflare Access must protect `cpa-admin.konbakuyomu.us`; CPA management key remains the second layer.
- If the token is not available during implementation, leave the tunnel stack prepared but not running, and record the exact start command.

## Local Evaluation Contract

- Do not modify `C:\Users\dxt98\.codex`.
- Create a temporary local test `CODEX_HOME` with only the minimal `config.toml` and auth material needed for the eval.
- Point the test provider at `https://cpa.konbakuyomu.us/v1` with `wire_api = "responses"`.
- Run `D:\Dev\50_Scripts\52_Python\codex-candy-eval\codex_candy_eval.py` from its own directory.
