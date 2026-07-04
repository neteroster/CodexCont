"""Semi-automatic budget suggestion helpers."""
from __future__ import annotations

from dataclasses import dataclass

from .key_policy import KeyRecord, has_model_prices


@dataclass(frozen=True)
class BudgetSuggestion:
    enabled_key_count: int
    per_key_daily_usd: float | None
    per_key_weekly_usd: float | None
    patches: list[dict]


def suggest_equal_budget(
    keys: list[KeyRecord],
    *,
    total_daily_usd: float | None = None,
    total_weekly_usd: float | None = None,
    require_model_prices: bool = True,
) -> BudgetSuggestion:
    enabled = [key for key in keys if key.enabled]
    if not enabled:
        return BudgetSuggestion(0, None, None, [])
    if require_model_prices:
        missing = [key.name for key in enabled if not has_model_prices(key)]
        if missing:
            raise ValueError("missing model prices for: " + ", ".join(missing))

    daily = None if total_daily_usd is None else round(float(total_daily_usd) / len(enabled), 4)
    weekly = None if total_weekly_usd is None else round(float(total_weekly_usd) / len(enabled), 4)
    patches = []
    for key in enabled:
        patch = {"key_hash": key.key_hash, "name": key.name}
        if daily is not None:
            patch["daily_limit_usd"] = daily
        if weekly is not None:
            patch["weekly_limit_usd"] = weekly
        patches.append(patch)
    return BudgetSuggestion(len(enabled), daily, weekly, patches)
