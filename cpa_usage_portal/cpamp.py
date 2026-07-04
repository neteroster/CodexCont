"""CPAMP monitoring API client."""
from __future__ import annotations

import time
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from typing import Any

import httpx


@dataclass(frozen=True)
class AnalyticsWindow:
    from_ms: int
    to_ms: int


SUPPORTED_RANGES = {"5h", "24h", "7d", "month"}
_SHANGHAI_TZ = timezone(timedelta(hours=8))


def now_ms() -> int:
    return int(time.time() * 1000)


def range_window(range_name: str, *, now_ms_value: int | None = None) -> AnalyticsWindow:
    current = int(now_ms_value if now_ms_value is not None else now_ms())
    if range_name == "5h":
        delta = 5 * 60 * 60 * 1000
        return AnalyticsWindow(from_ms=current - delta, to_ms=current)
    if range_name == "7d":
        delta = 7 * 24 * 60 * 60 * 1000
        return AnalyticsWindow(from_ms=current - delta, to_ms=current)
    if range_name == "month":
        local_now = datetime.fromtimestamp(current / 1000, _SHANGHAI_TZ)
        start = local_now.replace(day=1, hour=0, minute=0, second=0, microsecond=0)
        return AnalyticsWindow(from_ms=int(start.timestamp() * 1000), to_ms=current)
    else:
        delta = 24 * 60 * 60 * 1000
        return AnalyticsWindow(from_ms=current - delta, to_ms=current)


class CPAMPClient:
    def __init__(self, base_url: str, admin_key: str, client: httpx.AsyncClient) -> None:
        self.base_url = base_url.rstrip("/")
        self.admin_key = admin_key.strip()
        self.client = client

    def _headers(self) -> dict[str, str]:
        return {"Authorization": f"Bearer {self.admin_key}"}

    async def health(self) -> dict[str, Any]:
        resp = await self.client.get(f"{self.base_url}/health", headers=self._headers(), timeout=5.0)
        resp.raise_for_status()
        data = resp.json()
        return data if isinstance(data, dict) else {"ok": True}

    async def analytics(
        self,
        *,
        api_key_hash: str,
        window: AnalyticsWindow,
        include_events: bool = False,
        include_model_stats: bool = False,
        event_limit: int = 100,
        before_ms: int | None = None,
        before_id: int | None = None,
    ) -> dict[str, Any]:
        include: dict[str, Any] = {
            "summary": True,
            "timeline": True,
            "model_share": True,
            "api_key_stats": True,
            "granularity": "hour",
        }
        if include_model_stats:
            include["model_stats"] = True
        if include_events:
            page: dict[str, Any] = {"limit": max(1, min(int(event_limit), 200))}
            if before_ms is not None:
                page["before_ms"] = int(before_ms)
            if before_id is not None:
                page["before_id"] = int(before_id)
            include["events_page"] = page

        body = {
            "from_ms": window.from_ms,
            "to_ms": window.to_ms,
            "now_ms": now_ms(),
            "time_zone": "Asia/Shanghai",
            "filters": {
                "api_key_hashes": [api_key_hash],
                "include_failed": True,
            },
            "include": include,
        }
        resp = await self.client.post(
            f"{self.base_url}/v0/management/monitoring/analytics",
            headers=self._headers(),
            json=body,
            timeout=15.0,
        )
        resp.raise_for_status()
        data = resp.json()
        return data if isinstance(data, dict) else {}
