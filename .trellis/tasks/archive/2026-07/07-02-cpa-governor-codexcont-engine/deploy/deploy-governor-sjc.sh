#!/usr/bin/env bash
set -euo pipefail

TS="$(date +%Y%m%d-%H%M%S)"
BACKUP_DIR="/root/cpa-governor-backup-${TS}"
STAGING="/tmp/cpa-governor-deploy"
EXPECTED_SO_SHA="7f4e31cab8c214f8985c7a6a7fabbd90a559bd3b206e65bcac28f21a9a8a75d3"

log() { printf '[deploy] %s\n' "$*"; }

log "preflight disk"
df -h /
mkdir -p "$BACKUP_DIR"
chmod 700 "$BACKUP_DIR"

backup_one() {
  local src="$1"
  local rel="${src#/}"
  local dst="$BACKUP_DIR/$rel"
  if [ -e "$src" ]; then
    mkdir -p "$(dirname "$dst")"
    cp -a "$src" "$dst"
    log "backed up $src"
  else
    log "missing $src"
  fi
}

backup_one /opt/codex-stacks/cpa/config.yaml
backup_one /opt/codex-stacks/cpa/docker-compose.yaml
backup_one /opt/codex-stacks/cpa/plugin-state/cpa-key-policy-state.json
backup_one /opt/codex-stacks/codexcont/docker-compose.yaml
backup_one /opt/codex-stacks/codexcont/config.toml
backup_one /opt/codex-stacks/codexcont/app/middleware/app.py
backup_one /opt/codex-stacks/codexcont/app/middleware/engine.py
backup_one /opt/codex-stacks/caddy/Caddyfile
backup_one /opt/codex-stacks/cpa-admin-tunnel/Caddyfile

log "verify artifact checksum"
ACTUAL_SO_SHA="$(sha256sum "$STAGING/cpa-governor.so" | awk '{print $1}')"
if [ "$ACTUAL_SO_SHA" != "$EXPECTED_SO_SHA" ]; then
  echo "artifact sha mismatch: $ACTUAL_SO_SHA" >&2
  exit 20
fi

log "install codexcont engine files"
install -m 0644 "$STAGING/app.py" /opt/codex-stacks/codexcont/app/middleware/app.py
install -m 0644 "$STAGING/engine.py" /opt/codex-stacks/codexcont/app/middleware/engine.py

log "install governor plugin artifact"
mkdir -p /opt/codex-stacks/cpa/plugins/linux/amd64
install -m 0644 "$STAGING/cpa-governor.so" /opt/codex-stacks/cpa/plugins/linux/amd64/cpa-governor.so
mkdir -p /opt/codex-stacks/cpa/plugin-state/cpa-governor
chmod 700 /opt/codex-stacks/cpa/plugin-state/cpa-governor

log "patch CPA config safely"
python3 - <<'PY'
from pathlib import Path
import secrets
import yaml
p = Path('/opt/codex-stacks/cpa/config.yaml')
cfg = yaml.safe_load(p.read_text()) or {}
plugins = cfg.setdefault('plugins', {})
plugins['enabled'] = True
plugins['dir'] = '/CLIProxyAPI/plugins'
configs = plugins.setdefault('configs', {})
existing = configs.get('cpa-governor') or {}
secret = existing.get('session_secret') or secrets.token_urlsafe(48)
existing.update({
    'enabled': True,
    'priority': 20,
    'exclusive_auth': False,
    'state_db_path': '/CLIProxyAPI/plugin-state/cpa-governor/governor.sqlite',
    'key_policy_state_path': '/CLIProxyAPI/plugin-state/cpa-key-policy-state.json',
    'session_secret': secret,
    'codexcont_enabled': True,
    'codexcont_route': False,
    'codexcont_url': 'http://codexcont:8787',
    'fail_mode': 'fallback',
})
configs['cpa-governor'] = existing
text = yaml.safe_dump(cfg, allow_unicode=True, sort_keys=False, default_flow_style=False)
p.write_text(text)
p.chmod(0o600)
print('CPA_CONFIG_PATCHED cpa-governor enabled passive')
PY

log "patch admin proxy Caddy routes"
python3 - <<'PY'
from pathlib import Path
p = Path('/opt/codex-stacks/cpa-admin-tunnel/Caddyfile')
text = p.read_text()
block = '''
	# CPA Governor plugin surfaces.
	handle /governor {
		redir /governor/ 307
	}

	handle /governor/ {
		rewrite * /v0/resource/plugins/cpa-governor/admin
		reverse_proxy cpa:8317 {
			import cpa_local_headers
		}
	}

	handle_path /governor/* {
		rewrite * /v0/resource/plugins/cpa-governor/admin{uri}
		reverse_proxy cpa:8317 {
			import cpa_local_headers
		}
	}

	handle /governor-user {
		redir /governor-user/ 307
	}

	handle /governor-user/ {
		rewrite * /v0/resource/plugins/cpa-governor/user
		reverse_proxy cpa:8317 {
			import cpa_local_headers
		}
	}

	handle_path /governor-user/* {
		rewrite * /v0/resource/plugins/cpa-governor/user{uri}
		reverse_proxy cpa:8317 {
			import cpa_local_headers
		}
	}
'''
if '/governor/*' not in text:
    markers = ['\n\thandle /codexcont {', '\n    handle /codexcont {']
    for marker in markers:
        if marker in text:
            text = text.replace(marker, '\n' + block + marker, 1)
            break
    else:
        raise SystemExit('admin proxy insertion marker not found')
p.write_text(text)
print('ADMIN_PROXY_PATCHED governor routes present')
PY

log "patch public cpa-usage host to Governor user surface"
python3 - <<'PY'
from pathlib import Path

p = Path('/opt/codex-stacks/caddy/Caddyfile')
text = p.read_text()
start = text.find('cpa-usage.konbakuyomu.us {')
if start < 0:
    raise SystemExit('cpa-usage block not found')
brace = 0
end = None
for idx in range(start, len(text)):
    ch = text[idx]
    if ch == '{':
        brace += 1
    elif ch == '}':
        brace -= 1
        if brace == 0:
            end = idx + 1
            break
if end is None:
    raise SystemExit('cpa-usage block end not found')
old = text[start:end]
inner_lines = old.splitlines()[1:-1]
preserved = []
for line in inner_lines:
    stripped = line.strip()
    if not stripped or stripped.startswith('#'):
        continue
    if stripped.startswith(('encode', '@', 'respond', 'reverse_proxy', 'handle')):
        continue
    preserved.append(line)
new_lines = ['cpa-usage.konbakuyomu.us {']
new_lines.extend(preserved[:2])
new_lines.extend([
    '    encode zstd gzip',
    '',
    '    handle /favicon.ico {',
    '        respond 204',
    '    }',
    '',
    '    handle / {',
    '        rewrite * /v0/resource/plugins/cpa-governor/user',
    '        reverse_proxy cpa:8317',
    '    }',
    '',
    '    handle /v0/resource/plugins/cpa-governor/user* {',
    '        reverse_proxy cpa:8317',
    '    }',
    '',
    '    handle {',
    '        respond 404',
    '    }',
    '}',
])
text = text[:start] + '\n'.join(new_lines) + text[end:]
p.write_text(text)
print('PUBLIC_USAGE_PATCHED governor user surface')
PY

log "rebuild CodexCont image"
docker compose -f /opt/codex-stacks/codexcont/docker-compose.yaml build codexcont
docker compose -f /opt/codex-stacks/codexcont/docker-compose.yaml up -d --no-deps codexcont

log "restart CPA for plugin load"
docker restart cpa >/dev/null

log "restart admin proxy for routes"
docker restart cpa-admin-proxy >/dev/null

log "reload public Caddy edge"
docker exec caddy-edge caddy validate --config /etc/caddy/Caddyfile
docker exec caddy-edge caddy reload --config /etc/caddy/Caddyfile

log "post status"
docker ps --format 'table {{.Names}}\t{{.Status}}'
sha256sum /opt/codex-stacks/cpa/plugins/linux/amd64/cpa-governor.so
printf 'BACKUP_DIR=%s\n' "$BACKUP_DIR"
