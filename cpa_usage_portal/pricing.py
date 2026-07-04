"""Per-key local cost projection using CPA Key Policy prices."""
from __future__ import annotations

from dataclasses import dataclass
from typing import Any

from .key_policy import ModelPrice

PER_MILLION = 1_000_000.0


@dataclass(frozen=True)
class ModelTokens:
    input_tokens: int = 0
    output_tokens: int = 0
    cached_tokens: int = 0
    cache_tokens: int = 0
    cache_read_tokens: int = 0
    cache_creation_tokens: int = 0


@dataclass(frozen=True)
class CacheProjection:
    compatible_cached_tokens: int = 0
    raw_cached_tokens: int = 0
    raw_cache_tokens: int = 0
    cache_read_tokens: int = 0
    cache_creation_tokens: int = 0
    cache_hit_tokens: int = 0
    cache_input_side_tokens: int = 0
    cache_hit_rate: float = 0.0
    semantics: str = "cpamp_compatible_cached_tokens"


def _number(value: Any) -> float:
    try:
        return float(value or 0)
    except (TypeError, ValueError):
        return 0.0


def _int(value: Any) -> int:
    try:
        return int(value or 0)
    except (TypeError, ValueError):
        return 0


def tokens_from_row(row: dict[str, Any]) -> ModelTokens:
    return ModelTokens(
        input_tokens=_int(row.get("input_tokens") or row.get("inputTokens")),
        output_tokens=_int(row.get("output_tokens") or row.get("outputTokens")),
        cached_tokens=_int(row.get("cached_tokens") or row.get("cachedTokens")),
        cache_tokens=_int(row.get("cache_tokens") or row.get("cacheTokens")),
        cache_read_tokens=_int(row.get("cache_read_tokens") or row.get("cacheReadTokens")),
        cache_creation_tokens=_int(row.get("cache_creation_tokens") or row.get("cacheCreationTokens")),
    )


def cache_projection_for_tokens(tokens: ModelTokens) -> CacheProjection:
    input_tokens = max(tokens.input_tokens, 0)
    raw_cached_tokens = max(tokens.cached_tokens, 0)
    raw_cache_tokens = max(tokens.cache_tokens, 0)
    cache_read_tokens = max(tokens.cache_read_tokens, 0)
    cache_creation_tokens = max(tokens.cache_creation_tokens, 0)
    if raw_cache_tokens > 0:
        cached_base = max(raw_cached_tokens, raw_cache_tokens)
        compatible_cached_tokens = max(cached_base - cache_read_tokens - cache_creation_tokens, 0)
        semantics = "raw_cache_tokens_normalized_to_cpamp"
    else:
        # CPAMP Management API already projects cached_tokens with its
        # compatibility expression, so do not subtract fine-grained fields again.
        compatible_cached_tokens = raw_cached_tokens
        semantics = "cpamp_compatible_cached_tokens"
    cache_hit_tokens = compatible_cached_tokens + cache_read_tokens
    cache_input_side_tokens = max(input_tokens, compatible_cached_tokens) + cache_read_tokens + cache_creation_tokens
    cache_hit_rate = 0.0
    if cache_input_side_tokens > 0:
        cache_hit_rate = min(max(cache_hit_tokens / cache_input_side_tokens, 0.0), 1.0)
    return CacheProjection(
        compatible_cached_tokens=compatible_cached_tokens,
        raw_cached_tokens=raw_cached_tokens,
        raw_cache_tokens=raw_cache_tokens,
        cache_read_tokens=cache_read_tokens,
        cache_creation_tokens=cache_creation_tokens,
        cache_hit_tokens=cache_hit_tokens,
        cache_input_side_tokens=cache_input_side_tokens,
        cache_hit_rate=cache_hit_rate,
        semantics=semantics,
    )


def price_for_model(
    price_book: dict[str, ModelPrice],
    *model_names: Any,
) -> tuple[str, ModelPrice] | None:
    candidates = [str(item or "").strip() for item in model_names if str(item or "").strip()]
    for candidate in candidates:
        if candidate in price_book:
            return candidate, price_book[candidate]
    lower_index = {name.lower(): (name, price) for name, price in price_book.items()}
    for candidate in candidates:
        found = lower_index.get(candidate.lower())
        if found is not None:
            return found
    return None


def cost_for_tokens(price: ModelPrice, tokens: ModelTokens, *, model: str = "", service_tier: str = "") -> float:
    return cost_breakdown_for_tokens(price, tokens, model=model, service_tier=service_tier)["costs"]["total"]


def cost_breakdown_for_tokens(
    price: ModelPrice,
    tokens: ModelTokens,
    *,
    model: str = "",
    service_tier: str = "",
    reasoning_tokens: Any = 0,
) -> dict[str, Any]:
    input_tokens = max(tokens.input_tokens, 0)
    output_tokens = max(tokens.output_tokens, 0)
    cache_projection = cache_projection_for_tokens(tokens)
    cached_tokens = cache_projection.compatible_cached_tokens
    cache_read_tokens = cache_projection.cache_read_tokens
    cache_creation_tokens = cache_projection.cache_creation_tokens
    reasoning = max(_int(reasoning_tokens), 0)
    prompt_tokens = max(input_tokens - cached_tokens, 0)
    cache_read_price = price.cache_read_per_million or price.input_per_million
    cache_creation_price = price.cache_creation_per_million or price.input_per_million
    input_cost = prompt_tokens * price.input_per_million / PER_MILLION
    cached_cost = cached_tokens * cache_read_price / PER_MILLION
    cache_read_cost = cache_read_tokens * cache_read_price / PER_MILLION
    cache_creation_cost = cache_creation_tokens * cache_creation_price / PER_MILLION
    output_cost = output_tokens * price.output_per_million / PER_MILLION
    subtotal = input_cost + cached_cost + cache_read_cost + cache_creation_cost + output_cost
    multiplier = service_tier_multiplier(model or price.model, service_tier)
    return {
        "source": "key_policy",
        "price_model": model or price.model,
        "unit": "usd_per_1m_tokens",
        "service_tier": service_tier or "",
        "service_tier_multiplier": multiplier,
        "prices": {
            "input_per_million": price.input_per_million,
            "output_per_million": price.output_per_million,
            "cache_read_per_million": cache_read_price,
            "cache_creation_per_million": cache_creation_price,
        },
        "tokens": {
            "input": input_tokens,
            "cached_input": cached_tokens,
            "cpamp_cached_input": cached_tokens,
            "raw_cached_input": cache_projection.raw_cached_tokens,
            "raw_cache_tokens": cache_projection.raw_cache_tokens,
            "billable_uncached_input": prompt_tokens,
            "cache_read": cache_read_tokens,
            "cache_creation": cache_creation_tokens,
            "fine_grained_cache_read": cache_read_tokens,
            "fine_grained_cache_creation": cache_creation_tokens,
            "cache_hit_input": cache_projection.cache_hit_tokens,
            "effective_cache_read_for_hit_rate": cache_projection.cache_hit_tokens,
            "cache_input_side": cache_projection.cache_input_side_tokens,
            "cache_hit_rate": cache_projection.cache_hit_rate,
            "cache_semantics": cache_projection.semantics,
            "total_cache_activity": cached_tokens + cache_read_tokens + cache_creation_tokens,
            "output": output_tokens,
            "reasoning": reasoning,
            "visible_output_estimate": max(output_tokens - reasoning, 0),
            "total": max(_int(input_tokens + output_tokens), 0),
        },
        "costs": {
            "input": input_cost * multiplier,
            "cached_input": cached_cost * multiplier,
            "cache_read": cache_read_cost * multiplier,
            "cache_creation": cache_creation_cost * multiplier,
            "cache_total": (cached_cost + cache_read_cost + cache_creation_cost) * multiplier,
            "output": output_cost * multiplier,
            "subtotal": subtotal,
            "total": subtotal * multiplier,
        },
    }


def cost_for_row(
    row: dict[str, Any],
    price_book: dict[str, ModelPrice],
    *,
    model_fields: tuple[str, ...] = ("model", "resolved_model", "requested_model"),
) -> tuple[float, str] | None:
    breakdown = cost_breakdown_for_row(row, price_book, model_fields=model_fields)
    if breakdown is None:
        return None
    return _number((breakdown.get("costs") or {}).get("total")), str(breakdown.get("price_model") or "")


def cost_breakdown_for_row(
    row: dict[str, Any],
    price_book: dict[str, ModelPrice],
    *,
    model_fields: tuple[str, ...] = ("model", "resolved_model", "requested_model"),
) -> dict[str, Any] | None:
    matched = price_for_model(price_book, *(row.get(field) for field in model_fields))
    if matched is None:
        return None
    model, price = matched
    breakdown = cost_breakdown_for_tokens(
        price,
        tokens_from_row(row),
        model=model,
        service_tier=str(row.get("service_tier") or ""),
        reasoning_tokens=row.get("reasoning_tokens"),
    )
    explicit_total = _int(row.get("total_tokens") or row.get("totalTokens"))
    if explicit_total:
        breakdown = {
            **breakdown,
            "tokens": {**breakdown["tokens"], "total": explicit_total},
        }
    return breakdown


def service_tier_multiplier(model_name: str, service_tier: str) -> float:
    tier = str(service_tier or "").strip().lower()
    if tier not in {"priority", "fast"}:
        return 1.0
    model = str(model_name or "").strip().lower()
    if _is_family(model, "gpt-5.5"):
        return 2.5
    if _is_family(model, "gpt-5.4-mini"):
        return 2.0
    if _is_family(model, "gpt-5.4"):
        return 2.0
    if _is_family(model, "gpt-5.3-codex"):
        return 2.0
    return 1.0


def _is_family(model: str, family: str) -> bool:
    return model == family or model.startswith(family + "-")


def apply_key_policy_pricing(data: dict[str, Any], price_book: dict[str, ModelPrice]) -> dict[str, Any]:
    if not price_book:
        return data
    projected = dict(data)
    model_stats = [dict(row) for row in projected.get("model_stats") or [] if isinstance(row, dict)]
    cost_by_model: dict[str, float] = {}
    unpriced: set[str] = set()
    for row in model_stats:
        model = str(row.get("model") or "").strip()
        priced = cost_for_row(row, price_book, model_fields=("model",))
        if priced is None:
            if model:
                unpriced.add(model)
            continue
        cost, matched_model = priced
        row["cost"] = cost
        row["cost_source"] = "key_policy"
        cost_by_model[model or matched_model] = cost_by_model.get(model or matched_model, 0.0) + cost

    model_share = [dict(row) for row in projected.get("model_share") or [] if isinstance(row, dict)]
    for row in model_share:
        model = str(row.get("model") or "").strip()
        if model in cost_by_model:
            row["cost"] = cost_by_model[model]
            row["cost_source"] = "key_policy"
            continue
        priced = cost_for_row(row, price_book, model_fields=("model",))
        if priced is None:
            if model:
                unpriced.add(model)
            continue
        cost, _ = priced
        row["cost"] = cost
        row["cost_source"] = "key_policy"
        cost_by_model[model] = cost_by_model.get(model, 0.0) + cost

    if model_stats:
        total_cost = sum(cost_by_model.values())
    else:
        total_cost = sum(_number(row.get("cost")) for row in model_share)
    summary = dict(projected.get("summary") or {})
    summary["total_cost"] = total_cost
    summary["cost_source"] = "key_policy"
    summary["unpriced_models"] = sorted(unpriced)

    projected["summary"] = summary
    projected["model_share"] = model_share
    projected["model_stats"] = model_stats
    return projected


def apply_event_pricing(events: list[dict[str, Any]], price_book: dict[str, ModelPrice]) -> list[dict[str, Any]]:
    if not price_book:
        return events
    projected: list[dict[str, Any]] = []
    for event in events:
        row = dict(event)
        breakdown = cost_breakdown_for_row(row, price_book, model_fields=("model", "requested_model"))
        if breakdown is not None:
            row["cost"] = _number((breakdown.get("costs") or {}).get("total"))
            row["cost_source"] = "key_policy"
            row["price_model"] = breakdown.get("price_model") or ""
            row["cost_breakdown"] = breakdown
        projected.append(row)
    return projected
