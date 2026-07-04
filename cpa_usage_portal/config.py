"""Environment configuration for the CPA usage portal."""
from __future__ import annotations

import os
from dataclasses import dataclass
from pathlib import Path


@dataclass(frozen=True)
class PortalConfig:
    host: str = "0.0.0.0"
    port: int = 8797
    key_policy_state_path: str = "/data/cpa-key-policy-state.json"
    local_state_db_path: str = "/data/portal/usage_portal.sqlite"
    cpamp_base_url: str = "http://cpamp:18317"
    cpamp_admin_key: str = ""
    session_secret: str = ""
    session_cookie_name: str = "cpa_usage_session"
    session_ttl_seconds: int = 24 * 60 * 60
    cookie_secure: bool = True
    poll_seconds: float = 3.0
    admin_header_name: str = "x-usage-admin"
    admin_header_value: str = "1"


def _read_secret(value: str, file_path: str) -> str:
    if value.strip():
        return value.strip()
    if file_path.strip():
        return Path(file_path).read_text(encoding="utf-8").strip()
    return ""


def _bool_env(name: str, default: bool) -> bool:
    value = os.environ.get(name)
    if value is None or value == "":
        return default
    return value.strip().lower() in {"1", "true", "yes", "on"}


def load_config_from_env() -> PortalConfig:
    return PortalConfig(
        host=os.environ.get("CPA_USAGE_PORTAL_HOST", "0.0.0.0"),
        port=int(os.environ.get("CPA_USAGE_PORTAL_PORT", "8797")),
        key_policy_state_path=os.environ.get(
            "CPA_USAGE_PORTAL_KEY_POLICY_STATE",
            "/data/cpa-key-policy-state.json",
        ),
        local_state_db_path=os.environ.get("CPA_USAGE_PORTAL_LOCAL_STATE_DB", "/data/portal/usage_portal.sqlite"),
        cpamp_base_url=os.environ.get("CPA_USAGE_PORTAL_CPAMP_URL", "http://cpamp:18317"),
        cpamp_admin_key=_read_secret(
            os.environ.get("CPA_USAGE_PORTAL_CPAMP_ADMIN_KEY", ""),
            os.environ.get("CPA_USAGE_PORTAL_CPAMP_ADMIN_KEY_FILE", ""),
        ),
        session_secret=_read_secret(
            os.environ.get("CPA_USAGE_PORTAL_SESSION_SECRET", ""),
            os.environ.get("CPA_USAGE_PORTAL_SESSION_SECRET_FILE", ""),
        ),
        session_cookie_name=os.environ.get("CPA_USAGE_PORTAL_SESSION_COOKIE", "cpa_usage_session"),
        session_ttl_seconds=int(os.environ.get("CPA_USAGE_PORTAL_SESSION_TTL_SECONDS", str(24 * 60 * 60))),
        cookie_secure=_bool_env("CPA_USAGE_PORTAL_COOKIE_SECURE", True),
        poll_seconds=float(os.environ.get("CPA_USAGE_PORTAL_POLL_SECONDS", "3")),
        admin_header_name=os.environ.get("CPA_USAGE_PORTAL_ADMIN_HEADER_NAME", "x-usage-admin"),
        admin_header_value=os.environ.get("CPA_USAGE_PORTAL_ADMIN_HEADER_VALUE", "1"),
    )
