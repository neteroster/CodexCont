"""Local quota metadata and soft-reset watermarks for the usage portal."""
from __future__ import annotations

import json
import sqlite3
from contextlib import contextmanager
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from .cpamp import AnalyticsWindow, SUPPORTED_RANGES, now_ms, range_window

RESET_WINDOWS = ("5h", "24h", "7d", "month")


@dataclass(frozen=True)
class LocalLimits:
    five_hour_usd: float | None = None
    monthly_usd: float | None = None
    updated_at_ms: int | None = None

    def safe_dict(self) -> dict[str, Any]:
        return {
            "five_hour_usd": self.five_hour_usd,
            "monthly_usd": self.monthly_usd,
            "updated_at_ms": self.updated_at_ms,
        }


class QuotaState:
    """Small SQLite-backed state owned by the custom portal.

    The database stores only operator metadata. It never stores raw API keys,
    request/response bodies, OAuth tokens, or CPAMP management secrets.
    """

    def __init__(self, path: str | Path) -> None:
        self.path = Path(path)
        self.path.parent.mkdir(parents=True, exist_ok=True)
        self._ensure_schema()

    def _connect(self) -> sqlite3.Connection:
        conn = sqlite3.connect(str(self.path), timeout=10)
        conn.row_factory = sqlite3.Row
        return conn

    @contextmanager
    def _connection(self):
        conn = self._connect()
        try:
            yield conn
            conn.commit()
        finally:
            conn.close()

    def _ensure_schema(self) -> None:
        with self._connection() as conn:
            conn.execute("pragma journal_mode=wal")
            conn.execute(
                """
                create table if not exists key_limits (
                    policy_id text primary key,
                    five_hour_limit_usd real,
                    monthly_limit_usd real,
                    updated_at_ms integer not null
                )
                """
            )
            conn.execute(
                """
                create table if not exists reset_watermarks (
                    policy_id text not null,
                    window text not null,
                    reset_at_ms integer not null,
                    updated_at_ms integer not null,
                    primary key (policy_id, window)
                )
                """
            )
            conn.execute(
                """
                create table if not exists audit_log (
                    id integer primary key autoincrement,
                    timestamp_ms integer not null,
                    actor text,
                    action text not null,
                    policy_id text not null,
                    window text,
                    before_json text,
                    after_json text
                )
                """
            )

    def get_limits(self, policy_id: str) -> LocalLimits:
        with self._connection() as conn:
            row = conn.execute(
                "select five_hour_limit_usd, monthly_limit_usd, updated_at_ms from key_limits where policy_id = ?",
                (policy_id,),
            ).fetchone()
        if row is None:
            return LocalLimits()
        return LocalLimits(
            five_hour_usd=_float_or_none(row["five_hour_limit_usd"]),
            monthly_usd=_float_or_none(row["monthly_limit_usd"]),
            updated_at_ms=_int_or_none(row["updated_at_ms"]),
        )

    def set_limits(
        self,
        policy_id: str,
        *,
        five_hour_usd: float | None,
        monthly_usd: float | None,
        actor: str = "admin",
    ) -> LocalLimits:
        before = self.get_limits(policy_id).safe_dict()
        updated = now_ms()
        with self._connection() as conn:
            conn.execute(
                """
                insert into key_limits(policy_id, five_hour_limit_usd, monthly_limit_usd, updated_at_ms)
                values (?, ?, ?, ?)
                on conflict(policy_id) do update set
                    five_hour_limit_usd = excluded.five_hour_limit_usd,
                    monthly_limit_usd = excluded.monthly_limit_usd,
                    updated_at_ms = excluded.updated_at_ms
                """,
                (policy_id, five_hour_usd, monthly_usd, updated),
            )
            after = LocalLimits(five_hour_usd=five_hour_usd, monthly_usd=monthly_usd, updated_at_ms=updated).safe_dict()
            self._insert_audit(
                conn,
                actor=actor,
                action="set_limits",
                policy_id=policy_id,
                window=None,
                before=before,
                after=after,
            )
        return LocalLimits(five_hour_usd=five_hour_usd, monthly_usd=monthly_usd, updated_at_ms=updated)

    def get_reset_points(self, policy_id: str) -> dict[str, int | None]:
        points: dict[str, int | None] = {name: None for name in RESET_WINDOWS}
        with self._connection() as conn:
            rows = conn.execute(
                "select window, reset_at_ms from reset_watermarks where policy_id = ?",
                (policy_id,),
            ).fetchall()
        for row in rows:
            window = str(row["window"] or "")
            if window in points:
                points[window] = _int_or_none(row["reset_at_ms"])
        return points

    def reset(self, policy_id: str, *, window: str, actor: str = "admin", reset_at_ms: int | None = None) -> dict[str, int | None]:
        windows = list(RESET_WINDOWS) if window == "all" else [window]
        invalid = [item for item in windows if item not in RESET_WINDOWS]
        if invalid:
            raise ValueError("invalid_reset_window")
        reset_at = int(reset_at_ms if reset_at_ms is not None else now_ms())
        before = self.get_reset_points(policy_id)
        with self._connection() as conn:
            for item in windows:
                conn.execute(
                    """
                    insert into reset_watermarks(policy_id, window, reset_at_ms, updated_at_ms)
                    values (?, ?, ?, ?)
                    on conflict(policy_id, window) do update set
                        reset_at_ms = excluded.reset_at_ms,
                        updated_at_ms = excluded.updated_at_ms
                    """,
                    (policy_id, item, reset_at, reset_at),
                )
            after = dict(before)
            for item in windows:
                after[item] = reset_at
            self._insert_audit(
                conn,
                actor=actor,
                action="reset_usage",
                policy_id=policy_id,
                window=window,
                before=before,
                after=after,
            )
        return self.get_reset_points(policy_id)

    def effective_window(self, policy_id: str, range_name: str, *, now_ms_value: int | None = None) -> tuple[AnalyticsWindow, int | None]:
        if range_name not in SUPPORTED_RANGES:
            range_name = "24h"
        base = range_window(range_name, now_ms_value=now_ms_value)
        reset_at = self.get_reset_points(policy_id).get(range_name)
        if reset_at is None or reset_at <= base.from_ms:
            return base, reset_at
        return AnalyticsWindow(from_ms=min(reset_at, base.to_ms), to_ms=base.to_ms), reset_at

    def _insert_audit(
        self,
        conn: sqlite3.Connection,
        *,
        actor: str,
        action: str,
        policy_id: str,
        window: str | None,
        before: dict[str, Any],
        after: dict[str, Any],
    ) -> None:
        conn.execute(
            """
            insert into audit_log(timestamp_ms, actor, action, policy_id, window, before_json, after_json)
            values (?, ?, ?, ?, ?, ?, ?)
            """,
            (
                now_ms(),
                actor[:160],
                action,
                policy_id,
                window,
                json.dumps(before, ensure_ascii=False, sort_keys=True),
                json.dumps(after, ensure_ascii=False, sort_keys=True),
            ),
        )


def _float_or_none(value: Any) -> float | None:
    if value is None or value == "":
        return None
    try:
        return float(value)
    except (TypeError, ValueError):
        return None


def _int_or_none(value: Any) -> int | None:
    if value is None or value == "":
        return None
    try:
        return int(value)
    except (TypeError, ValueError):
        return None
