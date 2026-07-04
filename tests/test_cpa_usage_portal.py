#!/usr/bin/env python3
"""Offline tests for the CPA usage self-service portal."""
from __future__ import annotations

import json
import sqlite3
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))

from starlette.testclient import TestClient

from cpa_usage_portal.app import create_app
from cpa_usage_portal.budget import suggest_equal_budget
from cpa_usage_portal.config import PortalConfig
from cpa_usage_portal.cpamp import range_window
from cpa_usage_portal.key_policy import KeyPolicyState
from cpa_usage_portal.pricing import apply_event_pricing
from cpa_usage_portal.quota_state import QuotaState
from cpa_usage_portal.redaction import redact, safe_event
from cpa_usage_portal.retention import enforce_usage_retention
from cpa_usage_portal.security import (
    hash_preview,
    key_policy_hash,
    normalize_key_hash,
    sha256_hex,
    sign_session,
    verify_session,
)


_RESULTS: list[tuple[str, bool, str]] = []


def check(name: str, cond: bool, detail: str = "") -> None:
    _RESULTS.append((name, bool(cond), detail))


class FakeCPAMP:
    def __init__(self, expected_hash: str, other_hash: str) -> None:
        self.expected_hash = expected_hash
        self.other_hash = other_hash
        self.seen_hashes: list[str] = []
        self.seen_windows: list[tuple[bool, int]] = []

    async def health(self):
        return {"ok": True}

    async def analytics(self, *, api_key_hash, window, include_events=False,
                        include_model_stats=False,
                        event_limit=100, before_ms=None, before_id=None):
        self.seen_hashes.append(api_key_hash)
        self.seen_windows.append((include_events, window.to_ms - window.from_ms))
        data = {
            "summary": {
                "total_calls": 2,
                "success_calls": 1,
                "failure_calls": 1,
                "success_rate": 0.5,
                "input_tokens": 100,
                "output_tokens": 50,
                "cached_tokens": 20,
                "cache_read_tokens": 0,
                "cache_creation_tokens": 0,
                "total_tokens": 300,
                "reasoning_tokens": 100,
                "total_cost": 0,
            },
            "timeline": [],
            "model_share": [{"model": "gpt-5.5", "calls": 2, "tokens": 300, "cost": 0}],
            "model_stats": [
                {
                    "model": "gpt-5.5",
                    "calls": 2,
                    "success_calls": 1,
                    "failure_calls": 1,
                    "success_rate": 0.5,
                    "input_tokens": 100,
                    "output_tokens": 50,
                    "cached_tokens": 20,
                    "cache_read_tokens": 0,
                    "cache_creation_tokens": 0,
                    "total_tokens": 300,
                    "cost": 0,
                }
            ] if include_model_stats else [],
            "api_key_stats": [
                {"api_key_hash": api_key_hash, "calls": 2, "total_tokens": 300},
                {"api_key_hash": self.other_hash, "calls": 99, "total_tokens": 999},
            ],
        }
        if include_events:
            data["events"] = {
                "items": [
                    {
                        "event_hash": "evt-a",
                        "request_id": "req-a",
                        "timestamp_ms": window.to_ms - 1000,
                        "api_key_hash": api_key_hash,
                        "model": "gpt-5.5",
                        "failed": False,
                        "input_tokens": 100,
                        "output_tokens": 50,
                        "cached_tokens": 20,
                        "total_tokens": 200,
                        "reasoning_tokens": 80,
                        "latency_ms": 1234,
                        "ttft_ms": 321,
                        "cost": 0,
                        "service_tier": "priority",
                        "reasoning_effort": "high",
                    },
                    {
                        "event_hash": "evt-b",
                        "request_id": "req-b",
                        "timestamp_ms": window.to_ms - 500,
                        "api_key_hash": self.other_hash,
                        "model": "gpt-5.5",
                        "failed": True,
                        "fail_summary": "Authorization: Bearer should-not-leak",
                    },
                ],
                "has_more": False,
                "total_count": 2,
            }
        return data


def write_state(path: Path, raw_key: str, disabled_key: str) -> tuple[str, str, str]:
    key_hash = sha256_hex(raw_key)
    cpamp_hash = sha256_hex("alice-key")
    disabled_hash = sha256_hex(disabled_key)
    path.write_text(
        json.dumps(
            {
                "keys": [
                    {
                        "id": "alice-key",
                        "key_hash": key_policy_hash(key_hash),
                        "name": "Alice",
                        "enabled": True,
                        "rpm": 12,
                        "models": [
                            {
                                "alias": "gpt-5.5",
                                "provider": "codex",
                                "target_model": "gpt-5.5",
                                "input_price_per_million": 5,
                                "output_price_per_million": 30,
                                "cache_read_price_per_million": 0.5,
                            }
                        ],
                        "daily_limit_usd": 5,
                        "weekly_limit_usd": 30,
                        "daily_usage_usd": 0.5,
                    },
                    {
                        "id": "disabled-key",
                        "key_hash": key_policy_hash(disabled_hash),
                        "name": "Disabled",
                        "enabled": False,
                        "models": [
                            {
                                "alias": "gpt-5.5",
                                "input_price_per_million": 5,
                                "output_price_per_million": 30,
                                "cache_read_price_per_million": 0.5,
                            }
                        ],
                    },
                ]
            },
            ensure_ascii=False,
        ),
        encoding="utf-8",
    )
    return key_hash, cpamp_hash, disabled_hash


def test_hash_and_session() -> None:
    raw = " cpa_test_key "
    hex_hash = sha256_hex(raw)
    check("sha256 trims raw key", hex_hash == sha256_hex(raw.strip()))
    check("normalize strips sha256 prefix", normalize_key_hash(key_policy_hash(hex_hash)) == hex_hash)
    check("hash preview hides middle", hash_preview(hex_hash).startswith(hex_hash[:8]))

    token = sign_session({"key_hash": hex_hash, "key_name": "Alice"}, "secret", ttl_seconds=60)
    payload = verify_session(token, "secret")
    check("session verifies", payload is not None and payload.get("key_hash") == hex_hash)
    check("session rejects wrong secret", verify_session(token, "other") is None)


def test_key_policy_and_budget(tmp: Path) -> None:
    key_hash, cpamp_hash, _ = write_state(tmp / "state.json", "cpa_live", "cpa_disabled")
    state = KeyPolicyState.load(tmp / "state.json")
    record = state.get(key_hash)
    check("key policy finds key", record is not None and record.name == "Alice")
    check("key policy validates raw key hash", state.get_by_raw_hash(key_hash) is record)
    check("key policy cpamp hash uses policy id",
          record is not None and record.cpamp_hash == cpamp_hash)
    check("key policy finds cpamp hash", state.get_by_cpamp_hash(cpamp_hash) is record)
    check("key policy safe dict hides full hash",
          record is not None and key_hash not in json.dumps(record.safe_dict()))
    check("key policy keeps clean model aliases",
          record is not None and record.models == ["gpt-5.5"],
          str(record.models if record else None))
    check("key policy parses per-model prices",
          record is not None
          and record.model_prices["gpt-5.5"].input_per_million == 5
          and record.model_prices["gpt-5.5"].output_per_million == 30
          and record.model_prices["gpt-5.5"].cache_read_per_million == 0.5,
          str(record.model_prices if record else None))
    check("key policy safe dict exposes limits",
          record is not None
          and record.safe_dict()["limits"]["daily_usd"] == 5
          and record.safe_dict()["limits"]["weekly_usd"] == 30,
          str(record.safe_dict() if record else None))

    suggestion = suggest_equal_budget(state.enabled_keys(), total_daily_usd=10, total_weekly_usd=70)
    check("budget enabled count", suggestion.enabled_key_count == 1)
    check("budget daily assigned", suggestion.per_key_daily_usd == 10)
    check("budget patch uses policy hash", suggestion.patches[0]["key_hash"].startswith("sha256:"))

    bad_state = KeyPolicyState([record.__class__(**{**record.__dict__, "raw": {}, "model_prices": {}})])
    try:
        suggest_equal_budget(bad_state.enabled_keys(), total_daily_usd=1)
        blocked = False
    except ValueError:
        blocked = True
    check("budget blocks missing prices", blocked)


def test_redaction_and_safe_event() -> None:
    expected = sha256_hex("cpa_live")
    other = sha256_hex("cpa_other")
    redacted = redact("Authorization: Bearer abc123 and api_key=secret")
    check("redact bearer", "abc123" not in redacted and "[REDACTED]" in redacted, redacted)
    safe = safe_event(
        {
            "api_key_hash": expected,
            "event_hash": "evt",
            "fail_summary": "access_token=secret",
            "failed": True,
        },
        expected_hash=expected,
    )
    check("safe event accepts own hash", safe is not None)
    check("safe event redacts failure", safe is not None and "secret" not in safe.get("failure", ""))
    check("safe event has short failure brief",
          safe is not None and safe.get("failure_brief") == "access_token=[REDACTED]",
          str(safe))
    header_blob = json.dumps({
        "Cf-Cache-Status": "DYNAMIC",
        "Set-Cookie": "secret-cookie",
        "Strict-Transport-Security": "max-age=31536000",
        "X-Codex-Plan-Type": "pro",
    })
    clean = safe_event(
        {
            "api_key_hash": expected,
            "failed": False,
            "fail_summary": header_blob,
        },
        expected_hash=expected,
    )
    check("safe event drops success header blob",
          clean is not None and clean.get("failure") == "" and clean.get("failure_brief") == "",
          str(clean))
    failed = safe_event(
        {
            "api_key_hash": expected,
            "failed": True,
            "fail_status_code": 502,
            "fail_summary": header_blob,
        },
        expected_hash=expected,
    )
    check("safe event summarizes failed header blob",
          failed is not None and failed.get("failure_brief") == "Upstream response headers omitted; inspect status code and quota fields.",
          str(failed))
    long_failure = safe_event(
        {
            "api_key_hash": expected,
            "failed": True,
            "fail_summary": "x" * 1000,
        },
        expected_hash=expected,
    )
    check("safe event bounds failure detail",
          long_failure is not None and len(long_failure.get("failure", "")) <= 600,
          str(long_failure))
    check("safe event rejects other hash",
          safe_event({"api_key_hash": other}, expected_hash=expected) is None)


def test_pricing_breakdown(tmp: Path) -> None:
    write_state(tmp / "state.json", "cpa_live", "cpa_disabled")
    record = KeyPolicyState.load(tmp / "state.json").enabled_keys()[0]
    events = apply_event_pricing([
        {
            "model": "gpt-5.5",
            "input_tokens": 100,
            "cached_tokens": 20,
            "output_tokens": 50,
            "reasoning_tokens": 30,
            "total_tokens": 150,
            "service_tier": "priority",
            "cost": 0,
        }
    ], record.model_prices)
    event = events[0]
    breakdown = event.get("cost_breakdown") or {}
    costs = breakdown.get("costs") or {}
    parts = sum(float(costs.get(name) or 0) for name in ("input", "cached_input", "cache_read", "cache_creation", "output"))
    check("pricing breakdown emitted", breakdown.get("price_model") == "gpt-5.5", str(breakdown))
    check("pricing breakdown total matches cost",
          abs(float(event.get("cost") or 0) - float(costs.get("total") or 0)) < 1e-12,
          str(event))
    check("pricing breakdown parts sum to total",
          abs(parts - float(costs.get("total") or 0)) < 1e-12,
          str(costs))
    check("pricing breakdown keeps reasoning metric",
          (breakdown.get("tokens") or {}).get("reasoning") == 30
          and (breakdown.get("tokens") or {}).get("visible_output_estimate") == 20,
          str(breakdown.get("tokens")))
    token_breakdown = breakdown.get("tokens") or {}
    check("pricing treats cpamp cached tokens as cache hits",
          token_breakdown.get("cpamp_cached_input") == 20
          and token_breakdown.get("fine_grained_cache_read") == 0
          and token_breakdown.get("effective_cache_read_for_hit_rate") == 20
          and token_breakdown.get("cache_semantics") == "cpamp_compatible_cached_tokens",
          str(token_breakdown))
    check("pricing charges cached input even when fine-grained read is zero",
          float(costs.get("cached_input") or 0) > 0 and float(costs.get("cache_read") or 0) == 0,
          str(costs))

    raw_events = apply_event_pricing([
        {
            "model": "gpt-5.5",
            "input_tokens": 100,
            "cached_tokens": 80,
            "cache_tokens": 80,
            "cache_read_tokens": 30,
            "cache_creation_tokens": 10,
            "output_tokens": 0,
            "cost": 0,
        }
    ], record.model_prices)
    raw_tokens = (raw_events[0].get("cost_breakdown") or {}).get("tokens") or {}
    check("pricing normalizes raw cache bucket like CPAMP",
          raw_tokens.get("cpamp_cached_input") == 40
          and raw_tokens.get("fine_grained_cache_read") == 30
          and raw_tokens.get("fine_grained_cache_creation") == 10
          and raw_tokens.get("effective_cache_read_for_hit_rate") == 70
          and raw_tokens.get("cache_semantics") == "raw_cache_tokens_normalized_to_cpamp",
          str(raw_tokens))


def test_retention(tmp: Path) -> None:
    db = tmp / "usage.sqlite"
    conn = sqlite3.connect(db)
    conn.execute("create table usage_events (id integer primary key autoincrement, timestamp_ms integer not null)")
    conn.execute("insert into usage_events(timestamp_ms) values (?)", (1_000,))
    conn.execute("insert into usage_events(timestamp_ms) values (?)", (10_000_000_000,))
    conn.commit()
    conn.close()
    result = enforce_usage_retention(db, keep_days=7, batch_size=1, now_ms_value=10_000_000_000)
    verify = sqlite3.connect(db)
    try:
        count = verify.execute("select count(*) from usage_events").fetchone()[0]
    finally:
        verify.close()
    check("retention deletes old rows", result.deleted == 1 and count == 1, str(result))


def test_quota_state(tmp: Path) -> None:
    quota = QuotaState(tmp / "quota.sqlite")
    limits = quota.get_limits("alice-key")
    check("quota state defaults empty", limits.five_hour_usd is None and limits.monthly_usd is None)
    updated = quota.set_limits("alice-key", five_hour_usd=1.5, monthly_usd=20, actor="tester")
    check("quota state stores local limits", updated.five_hour_usd == 1.5 and updated.monthly_usd == 20)
    points = quota.reset("alice-key", window="5h", actor="tester", reset_at_ms=1_800_000_000_000)
    check("quota state stores reset watermark", points["5h"] == 1_800_000_000_000, str(points))
    window, reset_at = quota.effective_window("alice-key", "5h", now_ms_value=1_800_000_100_000)
    check("quota state applies reset watermark",
          reset_at == 1_800_000_000_000 and window.from_ms == 1_800_000_000_000,
          str((window, reset_at)))
    month = range_window("month", now_ms_value=1_783_108_800_000)
    check("range window supports month", month.from_ms < month.to_ms)


def test_app_routes(tmp: Path) -> None:
    key_hash, cpamp_hash, other_raw_hash = write_state(tmp / "state.json", "cpa_live", "cpa_disabled")
    other_hash = sha256_hex("other-policy-id")
    cfg = PortalConfig(
        key_policy_state_path=str(tmp / "state.json"),
        local_state_db_path=str(tmp / "quota.sqlite"),
        cpamp_admin_key="cpamp_test",
        session_secret="session_secret",
        cookie_secure=False,
    )
    with TestClient(create_app(cfg)) as client:
        fake = FakeCPAMP(cpamp_hash, other_hash)
        client.app.state.cpamp = fake

        health = client.get("/healthz")
        check("portal healthz ok", health.status_code == 200 and health.json().get("ok") is True)
        html = client.get("/")
        check("portal dashboard html ok", html.status_code == 200 and "CPA 用量自助页" in html.text)
        admin_public = client.get("/admin/")
        check("portal admin hidden without proxy header", admin_public.status_code == 404)
        admin_html = client.get("/admin/", headers={"x-usage-admin": "1"})
        check("portal admin dashboard html ok", admin_html.status_code == 200 and "CPA 用量管理" in admin_html.text)
        check("portal admin supports usage-admin mount", "API_BASE" in admin_html.text and "/usage-admin" in admin_html.text)
        check("portal admin has one save all action",
              "保存全部" in admin_html.text and "data-save" not in admin_html.text,
              admin_html.text[:200])
        check("portal admin defaults all-key request view",
              "全部 Key" in admin_html.text and "用户/Key" in admin_html.text,
              admin_html.text[:200])
        check("portal admin detail shows useful breakdown",
              "Token 组成" in admin_html.text and "费用组成" in admin_html.text and "CPAMP 缓存命中" in admin_html.text,
              admin_html.text[:200])
        check("portal admin has no metric crescent", "metric::after" not in admin_html.text)
        check("portal dashboard refresh reconnects stream", "startStream({ force: true })" in html.text)
        check("portal dashboard revives after background", "visibilitychange" in html.text)
        check("portal dashboard shows refresh animation", "is-loading" in html.text and "stream warn" in html.text)
        check("portal dashboard shows key limits", "用量限额" in html.text and "日限" in html.text and "周限" in html.text)
        check("portal dashboard follows delayed usage updates",
              "mergeEvent(JSON.parse(ev.data))" in html.text and "scheduleFollowUpRefreshes" in html.text)
        check("portal dashboard applies selected range to events",
              "api(`/api/events?range=${encodeURIComponent(range)}&limit=100`)" in html.text
              and "当前显示" in html.text)
        check("portal dashboard supports 5h and month ranges",
              '<option value="5h">5 小时</option>' in html.text
              and '<option value="month">本月</option>' in html.text)
        check("portal dashboard has no metric crescent", "metric::after" not in html.text)
        check("portal dashboard explains cache semantics",
              "CPAMP 缓存命中" in html.text and "细粒度 Cache Read" in html.text,
              html.text[:200])

        admin_keys = client.get("/admin/api/keys", headers={"x-usage-admin": "1"})
        admin_body = admin_keys.json()
        check("portal admin lists quota windows",
              admin_keys.status_code == 200
              and {"5h", "24h", "7d", "month"}.issubset(set(admin_body["keys"][0]["usage_windows"].keys())),
              str(admin_body))
        limits_update = client.put(
            "/admin/api/keys/alice-key/limits",
            headers={"x-usage-admin": "1"},
            json={"five_hour_usd": 1.25, "monthly_usd": 20},
        )
        check("portal admin updates local limits",
              limits_update.status_code == 200
              and limits_update.json()["me"]["limits"]["five_hour_usd"] == 1.25
              and limits_update.json()["me"]["limits"]["monthly_usd"] == 20,
              limits_update.text)
        batch_update = client.put(
            "/admin/api/keys/limits",
            headers={"x-usage-admin": "1"},
            json={"limits": [{"id": "alice-key", "five_hour_usd": "2.5", "monthly_usd": "25"}]},
        )
        check("portal admin batch updates local limits",
              batch_update.status_code == 200
              and batch_update.json()["keys"][0]["limits"]["five_hour_usd"] == 2.5
              and batch_update.json()["keys"][0]["limits"]["monthly_usd"] == 25,
              batch_update.text)
        batch_bad = client.put(
            "/admin/api/keys/limits",
            headers={"x-usage-admin": "1"},
            json={"limits": [{"id": "alice-key", "five_hour_usd": "not-a-number", "monthly_usd": "25"}]},
        )
        check("portal admin batch rejects invalid limits",
              batch_bad.status_code == 400 and "invalid_limit:alice-key" in batch_bad.text,
              batch_bad.text)
        reset = client.post(
            "/admin/api/keys/alice-key/reset",
            headers={"x-usage-admin": "1"},
            json={"window": "5h"},
        )
        check("portal admin soft resets window", reset.status_code == 200 and reset.json()["reset_points"]["5h"], reset.text)
        fake.seen_hashes.clear()
        fake.seen_windows.clear()

        bad = client.post("/api/session", json={"api_key": "nope"})
        check("portal rejects unknown key", bad.status_code == 401)

        login = client.post("/api/session", json={"api_key": "cpa_live"})
        check("portal login ok", login.status_code == 200, login.text)
        cookie = login.headers.get("set-cookie", "")
        check("portal cookie httponly", "HttpOnly" in cookie, cookie)
        check("portal cookie does not contain raw key", "cpa_live" not in cookie, cookie)

        me = client.get("/api/me")
        check("portal me ok", me.status_code == 200 and me.json()["me"]["name"] == "Alice", me.text)
        check("portal me exposes local limits and reset points",
              me.json()["me"]["limits"]["five_hour_usd"] == 2.5
              and me.json()["me"]["limits"]["monthly_usd"] == 25
              and me.json()["me"]["reset_points"]["5h"],
              me.text)
        usage = client.get("/api/usage?range=24h")
        usage_body = usage.json()
        check("portal usage ok", usage.status_code == 200 and usage_body["summary"]["total_calls"] == 2)
        check("portal usage includes daily and weekly limits",
              me.json()["me"]["limits"]["daily_usd"] == 5 and me.json()["me"]["limits"]["weekly_usd"] == 30,
              me.text)
        check("portal usage recomputes cost from key policy prices",
              usage_body["summary"]["total_cost"] > 0
              and usage_body["model_share"][0]["cost"] > 0
              and usage_body["summary"]["cost_source"] == "key_policy",
              str(usage_body))
        check("portal usage stat hides cpamp hash", cpamp_hash not in json.dumps(usage_body), str(usage_body))
        check("portal usage stats reject other hash",
              len(usage_body["api_key_stats"]) == 1 and usage_body["api_key_stats"][0]["calls"] == 2,
              str(usage_body))
        events = client.get("/api/events?range=24h&limit=100")
        body = events.json()
        check("portal events return selected range",
              body.get("range") == "24h"
              and any(include_events and delta <= 25 * 60 * 60 * 1000 for include_events, delta in fake.seen_windows),
              str(body))
        check("portal filters events to own key", len(body["events"]) == 1, str(body))
        check("portal events recompute cost from key policy prices",
              body["events"][0]["cost"] > 0 and body["events"][0]["cost_source"] == "key_policy",
              str(body))
        check("portal events include accounting windows",
              "accounting" in body["events"][0]
              and "24h" in body["events"][0]["accounting"]["included_windows"],
              str(body))
        check("portal events include pricing breakdown",
              "cost_breakdown" in body["events"][0]
              and body["events"][0]["cost_breakdown"]["costs"]["total"] == body["events"][0]["cost"],
              str(body["events"][0]))
        check("portal events expose cpamp cache hit semantics",
              body["events"][0]["cost_breakdown"]["tokens"]["cpamp_cached_input"] == 20
              and body["events"][0]["cost_breakdown"]["tokens"]["effective_cache_read_for_hit_rate"] == 20,
              str(body["events"][0]["cost_breakdown"]["tokens"]))
        admin_events = client.get("/admin/api/events?key_id=all&range=24h&limit=100", headers={"x-usage-admin": "1"})
        admin_events_body = admin_events.json()
        check("portal admin all-key events ok",
              admin_events.status_code == 200
              and admin_events_body.get("key_id") == "all"
              and admin_events_body["events"][0]["key"]["name"] == "Alice",
              str(admin_events_body))
        check("portal admin all-key event hides full hashes",
              cpamp_hash not in json.dumps(admin_events_body) and key_hash not in json.dumps(admin_events_body),
              str(admin_events_body))
        month_usage = client.get("/api/usage?range=month")
        check("portal usage supports month range",
              month_usage.status_code == 200 and month_usage.json()["range"] == "month",
              month_usage.text)
        check("portal never returns full raw api hash", key_hash not in json.dumps(body), str(body))
        check("portal does not use disabled raw hash", other_raw_hash not in json.dumps(body), str(body))
        check("portal cpamp filter used policy-id hash",
              fake.seen_hashes and all(h == cpamp_hash for h in fake.seen_hashes))


def main() -> None:
    test_hash_and_session()
    with tempfile.TemporaryDirectory() as d:
        test_key_policy_and_budget(Path(d))
    test_redaction_and_safe_event()
    with tempfile.TemporaryDirectory() as d:
        test_pricing_breakdown(Path(d))
    with tempfile.TemporaryDirectory() as d:
        test_retention(Path(d))
    with tempfile.TemporaryDirectory() as d:
        test_quota_state(Path(d))
    with tempfile.TemporaryDirectory() as d:
        test_app_routes(Path(d))

    passed = sum(1 for _, ok, _ in _RESULTS if ok)
    for name, ok, detail in _RESULTS:
        mark = "PASS" if ok else "FAIL"
        line = f"[{mark}] {name}"
        if not ok and detail:
            line += f"  -- {detail}"
        print(line)
    print(f"\n{passed}/{len(_RESULTS)} checks passed")
    sys.exit(0 if passed == len(_RESULTS) else 1)


if __name__ == "__main__":
    main()
