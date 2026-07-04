"""Read-only Key Policy identity projection for CodexCont diagnostics."""
from __future__ import annotations

import hashlib
import json
from pathlib import Path
from typing import Any


def _sha256_hex(value: str) -> str:
    return hashlib.sha256(value.strip().encode("utf-8")).hexdigest()


def _normalize_hash(value: Any) -> str:
    text = str(value or "").strip()
    if text.startswith("sha256:"):
        text = text.split(":", 1)[1]
    if len(text) != 64 or any(ch not in "0123456789abcdefABCDEF" for ch in text):
        return ""
    return text.lower()


def _preview(value: str) -> str:
    normalized = _normalize_hash(value)
    if not normalized:
        return ""
    return f"{normalized[:8]}...{normalized[-6:]}"


def _first(raw: dict[str, Any], *names: str) -> Any:
    for name in names:
        if name in raw:
            return raw[name]
    return None


def _extract_keys(data: Any) -> list[dict[str, Any]]:
    if isinstance(data, list):
        return [item for item in data if isinstance(item, dict)]
    if not isinstance(data, dict):
        return []
    for path in (
        ("keys",),
        ("state", "keys"),
        ("data", "keys"),
        ("config", "keys"),
    ):
        current: Any = data
        for part in path:
            if not isinstance(current, dict):
                current = None
                break
            current = current.get(part)
        if isinstance(current, list):
            return [item for item in current if isinstance(item, dict)]
    return []


def _safe_record(raw: dict[str, Any], key_hash: str) -> dict[str, Any]:
    disabled = bool(_first(raw, "disabled", "is_disabled", "isDisabled") or False)
    enabled_raw = _first(raw, "enabled", "is_enabled", "isEnabled")
    enabled = bool(enabled_raw) if enabled_raw is not None else not disabled
    name = str(_first(raw, "name", "label", "alias", "description") or "").strip() or _preview(key_hash)
    raw_id = _first(raw, "id", "key_id", "keyId")
    return {
        "known": True,
        "source": "key_policy_state",
        "id": str(raw_id).strip() if raw_id is not None else _preview(key_hash),
        "name": name,
        "preview": str(_first(raw, "preview", "key_preview", "keyPreview") or "").strip() or _preview(key_hash),
        "enabled": enabled and not disabled,
    }


class KeyIdentityResolver:
    def __init__(self, state_path: str = "") -> None:
        self.state_path = str(state_path or "").strip()

    def identify_authorization(self, authorization: str | None) -> dict[str, Any]:
        bearer = _bearer_token(authorization)
        if not bearer:
            return {
                "known": False,
                "source": "authorization",
                "name": "未携带 Key",
                "preview": "",
            }
        key_hash = _sha256_hex(bearer)
        if not self.state_path:
            return {
                "known": False,
                "source": "unconfigured",
                "name": "未配置身份表",
                "preview": _preview(key_hash),
            }
        try:
            data = json.loads(Path(self.state_path).read_text(encoding="utf-8"))
        except Exception:
            return {
                "known": False,
                "source": "key_policy_state_unavailable",
                "name": "身份表不可读",
                "preview": _preview(key_hash),
            }
        for item in _extract_keys(data):
            stored = _normalize_hash(_first(item, "key_hash", "keyHash", "hash", "api_key_hash", "apiKeyHash"))
            if stored and stored == key_hash:
                return _safe_record(item, key_hash)
        return {
            "known": False,
            "source": "key_policy_state",
            "name": "未识别 Key",
            "preview": _preview(key_hash),
        }


def _bearer_token(authorization: str | None) -> str:
    text = str(authorization or "").strip()
    if not text:
        return ""
    parts = text.split(None, 1)
    if len(parts) != 2 or parts[0].lower() != "bearer":
        return ""
    return parts[1].strip()
