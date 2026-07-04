"""Small, explicit retention helper for CPAMP SQLite history."""
from __future__ import annotations

import sqlite3
import time
from dataclasses import dataclass
from pathlib import Path


@dataclass(frozen=True)
class RetentionResult:
    deleted: int
    cutoff_ms: int
    batches: int


def _has_required_usage_table(conn: sqlite3.Connection) -> bool:
    row = conn.execute(
        "select name from sqlite_schema where type = 'table' and name = 'usage_events'"
    ).fetchone()
    if row is None:
        return False
    columns = {item[1] for item in conn.execute("pragma table_info(usage_events)").fetchall()}
    return {"id", "timestamp_ms"}.issubset(columns)


def enforce_usage_retention(
    db_path: str | Path,
    *,
    keep_days: int = 7,
    batch_size: int = 500,
    now_ms_value: int | None = None,
) -> RetentionResult:
    keep_ms = max(1, int(keep_days)) * 24 * 60 * 60 * 1000
    cutoff_ms = (now_ms_value if now_ms_value is not None else int(time.time() * 1000)) - keep_ms
    deleted = 0
    batches = 0
    conn = sqlite3.connect(str(db_path))
    try:
        conn.execute("pragma busy_timeout = 5000")
        if not _has_required_usage_table(conn):
            return RetentionResult(0, cutoff_ms, 0)
        while True:
            rows = conn.execute(
                "select id from usage_events where timestamp_ms < ? order by timestamp_ms limit ?",
                (cutoff_ms, max(1, int(batch_size))),
            ).fetchall()
            if not rows:
                break
            ids = [row[0] for row in rows]
            placeholders = ",".join("?" for _ in ids)
            conn.execute(f"delete from usage_events where id in ({placeholders})", ids)
            conn.commit()
            deleted += len(ids)
            batches += 1
            if len(ids) < batch_size:
                break
        conn.execute("pragma wal_checkpoint(TRUNCATE)").fetchall()
    finally:
        conn.close()
    return RetentionResult(deleted, cutoff_ms, batches)
