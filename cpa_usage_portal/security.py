"""Hashing and signed-session helpers for the CPA usage portal."""
from __future__ import annotations

import base64
import hashlib
import hmac
import json
import time
from typing import Any

HASH_PREFIX = "sha256:"


def sha256_hex(value: str) -> str:
    return hashlib.sha256(value.strip().encode("utf-8")).hexdigest()


def normalize_key_hash(value: str) -> str:
    text = (value or "").strip().lower()
    if text.startswith(HASH_PREFIX):
        text = text[len(HASH_PREFIX):]
    if len(text) != 64 or any(ch not in "0123456789abcdef" for ch in text):
        raise ValueError("invalid sha256 key hash")
    return text


def key_policy_hash(hex_hash: str) -> str:
    return f"{HASH_PREFIX}{normalize_key_hash(hex_hash)}"


def hash_preview(hex_hash: str) -> str:
    normalized = normalize_key_hash(hex_hash)
    return f"{normalized[:8]}...{normalized[-4:]}"


def _b64(data: bytes) -> str:
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode("ascii")


def _unb64(data: str) -> bytes:
    pad = "=" * (-len(data) % 4)
    return base64.urlsafe_b64decode(data + pad)


def sign_session(payload: dict[str, Any], secret: str, *, ttl_seconds: int) -> str:
    now = int(time.time())
    safe_payload = {
        **payload,
        "iat": now,
        "exp": now + max(1, int(ttl_seconds)),
    }
    encoded = _b64(json.dumps(safe_payload, sort_keys=True, separators=(",", ":")).encode("utf-8"))
    signature = hmac.new(secret.encode("utf-8"), encoded.encode("ascii"), hashlib.sha256).digest()
    return f"{encoded}.{_b64(signature)}"


def verify_session(token: str, secret: str) -> dict[str, Any] | None:
    try:
        encoded, signature = token.split(".", 1)
    except ValueError:
        return None
    want = hmac.new(secret.encode("utf-8"), encoded.encode("ascii"), hashlib.sha256).digest()
    try:
        got = _unb64(signature)
    except Exception:
        return None
    if not hmac.compare_digest(got, want):
        return None
    try:
        payload = json.loads(_unb64(encoded))
    except Exception:
        return None
    if not isinstance(payload, dict):
        return None
    if int(payload.get("exp") or 0) < int(time.time()):
        return None
    try:
        payload["key_hash"] = normalize_key_hash(str(payload.get("key_hash") or ""))
    except ValueError:
        return None
    return payload
