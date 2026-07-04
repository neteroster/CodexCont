# Implementation Plan

## 1. Preflight

- Confirm Git status and active task.
- Confirm SJC disk, Docker state, CPA/Caddy containers, networks, and current management endpoint status.
- Confirm no secrets are printed in command output.

## 2. Backups

- Create `/root/cpa-codexcont-admin-backups/<timestamp>/`.
- Back up `/opt/codex-stacks/cpa/docker-compose.yaml`, `/opt/codex-stacks/cpa/config.yaml`, Caddyfile, and any new stack manifests.
- Record only backup path and redacted evidence.

## 3. CodexCont Sidecar

- Create `/opt/codex-stacks/codexcont/app` and copy only runtime files needed by CodexCont.
- Create `config.toml` with:
  - `server.host = "0.0.0.0"`
  - `server.port = 8787`
  - `server.listen_paths = ["/v1/responses"]`
  - `upstream.url = "http://cpa:8317/v1/responses"`
  - `upstream.mode = "fixed"`
  - `auth.mode = "passthrough"`
  - continuation enabled with existing defaults
- Create Dockerfile using Python 3.12 and install project dependencies.
- Start `codexcont` on `cpa_net`.
- Validate from container/network that CodexCont can reach CPA.

## 4. Caddy Cutover

- Update Caddy for `cpa.konbakuyomu.us`:
  - Block management paths.
  - Route `/v1/responses` to `codexcont:8787`.
  - Route all other traffic to `cpa:8317`.
- Reload Caddy.
- Verify health, models, real responses, and CodexCont logs.
- Roll back Caddy if responses fail.

## 5. CPA Management API

- Generate a strong CPA management key on the server.
- Store plaintext key in a root-only credentials file and write only the path in task evidence.
- Update CPA `config.yaml` with `remote-management.secret-key` and `allow-remote: false`.
- Change compose mount for config to writable.
- Restart CPA and verify local `/management.html` and authenticated `/v0/management/config`.
- Verify public API domain management paths remain blocked.

## 6. Cloudflare Tunnel

- Create `/opt/codex-stacks/cpa-admin-tunnel/docker-compose.yaml`.
- Run `cpa-admin-proxy` on `127.0.0.1:8327` to preserve CPA `allow-remote: false`.
- Create root-only `.env` placeholder or use the user-provided `TUNNEL_TOKEN`.
- Start `cloudflared` only after token is available.
- Verify `cpa-admin.konbakuyomu.us/management.html` reaches Cloudflare Access / management panel.

## 7. Eval

- Create an isolated local test `CODEX_HOME` under a temp directory.
- Configure a test provider for `https://cpa.konbakuyomu.us/v1`.
- Run `python codex_candy_eval.py -m gpt-5.5 -r high -n 5`.
- Keep or delete the temp test home only after reporting the path; do not touch the user's normal Codex config.

## 8. Closeout

- Update task evidence with validation results.
- Run local CodexCont tests if repository code changed.
- Commit Trellis task artifacts and any repo changes.
- Record remaining manual Cloudflare Access/token action if blocked.

## Execution Evidence

### Preflight And Backups

- SJC access: `ssh sjc-guard`.
- Backup path: `/root/cpa-codexcont-admin-backups/20260701T044651Z`.
- Disk before CodexCont build: `/dev/sda1` around `8.5G used / 845-846M free` after build, `92%`.
- No Docker prune or bulk filesystem deletion was used.

### CodexCont Sidecar

- Stack path: `/opt/codex-stacks/codexcont`.
- Container: `codexcont`.
- Network: `cpa_net`.
- Runtime image build used `python:3.12-slim`.
- Fixed a UTF-8 BOM in server `config.toml`; `tomllib` rejected the BOM at line 1 before the fix.
- Container health evidence:
  - `codexcont` can reach `http://cpa:8317/healthz` with HTTP `200`.
  - `caddy-edge` resolves `codexcont` on `cpa_net`.

### Caddy Cutover

- `cpa.konbakuyomu.us` Caddy block now:
  - blocks `/management.html`, `/v0/management*`, `/v0/resource/plugins/*` with `404`.
  - sends `/v1/responses` and `/v1/responses/*` to `codexcont:8787`.
  - sends other paths to `cpa:8317`.
- `caddy validate --config /etc/caddy/Caddyfile`: valid.
- `caddy reload --config /etc/caddy/Caddyfile`: succeeded.

### Public API Validation

- `https://cpa.konbakuyomu.us/healthz`: HTTP `200`.
- `https://cpa.konbakuyomu.us/v1/models` with existing API key: HTTP `200`.
- `https://cpa.konbakuyomu.us/v1/responses` streaming smoke with `gpt-5.5`: HTTP `200`, expected marker text returned.
- Public management blocking:
  - `https://cpa.konbakuyomu.us/management.html`: HTTP `404`.
  - `https://cpa.konbakuyomu.us/v0/management/config`: HTTP `404`.
- CodexCont log evidence for live response:
  - `fold start: model=gpt-5.5 path=/v1/responses url=http://cpa:8317/v1/responses`.
  - continuation decisions were logged for eval rounds.
- Egress evidence:
  - `sub2api-egress-att` logs show CPA traffic to `chatgpt.com:443` through `sub2api-att-residential`.

### CPA Management

- CPA management key generated on server only.
- Plaintext management key path: `/root/cpa-codexcont-admin-backups/20260701T044651Z/cpa-management-key.txt`.
- CPA `config.yaml` bind mount changed from read-only to writable.
- CPA restarted with local image and `--pull never`.
- CPA hashed and persisted `remote-management.secret-key`; plaintext was not left in `config.yaml`.
- `allow-remote` remains `false`.
- Direct host request to `127.0.0.1:8317/v0/management/config` returned `403 remote management disabled` because Docker publish reaches CPA as a bridge peer, not loopback.
- Admin proxy added to preserve `allow-remote: false`:
  - stack path: `/opt/codex-stacks/cpa-admin-tunnel`.
  - container: `cpa-admin-proxy`.
  - host bind: `127.0.0.1:8327`.
  - proxy sets `X-Forwarded-For`, `X-Real-IP`, and `CF-Connecting-IP` to `127.0.0.1`.
- Admin proxy validation:
  - `http://127.0.0.1:8327/management.html`: HTTP `200`.
  - `http://127.0.0.1:8327/v0/management/config` without key: HTTP `401`.
  - `http://127.0.0.1:8327/v0/management/config` with management key: HTTP `200`.

### Cloudflare Tunnel Status

- `/opt/codex-stacks/cpa-admin-tunnel/docker-compose.yaml` prepared with:
  - `cpa-admin-proxy` running now.
  - `cpa-admin-tunnel` using `cloudflare/cloudflared:latest`, host networking, and `.env`.
- `/opt/codex-stacks/cpa-admin-tunnel/.env` is root-only and secrets were not recorded in Git, Obsidian, or Trellis artifacts.
- User provided/configured the Cloudflare Tunnel token out of band and reported the Docker Cloudflare connector is connected.
- Cloudflare Dashboard public hostname targets `http://127.0.0.1:8327`, not `8317`, because of the CPA local-client behavior above.
- Final user acceptance: `https://cpa-admin.konbakuyomu.us/management.html` reaches the CPA management panel through Cloudflare Tunnel + Cloudflare Access + CPA management key.

### Isolated Codex Candy Eval

- Normal `C:\Users\dxt98\.codex` was not modified.
- Temporary `CODEX_HOME`: `C:\Users\dxt98\AppData\Local\Temp\codex-cpa-eval-home-20260701130319`.
- Test provider: `https://cpa.konbakuyomu.us/v1`, `wire_api = "responses"`, API key supplied via process environment only.
- Command: `python D:\Dev\50_Scripts\52_Python\codex-candy-eval\codex_candy_eval.py -m gpt-5.5 -r high -n 5`.
- Result: `4/5` correct, `80.0%`.
- Important caveat: CodexCont did catch and continue multiple `518n-2` rounds, but one eval run still ended wrong after a first-round `reasoning_tokens=516`. Current sidecar is a mitigation and traffic-path fix, not yet a proof of complete 516-class elimination.
