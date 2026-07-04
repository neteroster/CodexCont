"""Read-only projection of CPA Key Policy state."""
from __future__ import annotations

import hashlib
import json
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from .security import hash_preview, key_policy_hash, normalize_key_hash


def _as_list(value: Any) -> list[Any]:
    if isinstance(value, list):
        return value
    if isinstance(value, tuple):
        return list(value)
    if isinstance(value, str) and value.strip():
        return [item.strip() for item in value.split(",") if item.strip()]
    return []


def _first(raw: dict[str, Any], *names: str) -> Any:
    for name in names:
        if name in raw:
            return raw[name]
    return None


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


@dataclass(frozen=True)
class ModelPrice:
    model: str
    input_per_million: float = 0.0
    output_per_million: float = 0.0
    cache_read_per_million: float = 0.0
    cache_creation_per_million: float = 0.0
    target_model: str | None = None
    provider: str | None = None

    def safe_dict(self) -> dict[str, Any]:
        return {
            "model": self.model,
            "target_model": self.target_model,
            "provider": self.provider,
            "input_per_million": self.input_per_million,
            "output_per_million": self.output_per_million,
            "cache_read_per_million": self.cache_read_per_million,
            "cache_creation_per_million": self.cache_creation_per_million,
        }


@dataclass(frozen=True)
class KeyRecord:
    key_hash: str
    name: str
    enabled: bool
    policy_id: str | None = None
    rpm: int | None = None
    models: list[str] = field(default_factory=list)
    model_prices: dict[str, ModelPrice] = field(default_factory=dict)
    daily_limit_usd: float | None = None
    weekly_limit_usd: float | None = None
    daily_usage_usd: float | None = None
    weekly_usage_usd: float | None = None
    preview: str | None = None
    raw: dict[str, Any] = field(default_factory=dict)

    @property
    def raw_key_hash(self) -> str:
        return normalize_key_hash(self.key_hash)

    @property
    def cpamp_hash(self) -> str:
        if self.policy_id:
            # CPA Key Policy authenticates requests as key.ID. CPA Manager Plus
            # then hashes that principal, not the original cpa_... key.
            return hashlib.sha256(self.policy_id.strip().encode("utf-8")).hexdigest()
        return self.raw_key_hash

    def safe_dict(self) -> dict[str, Any]:
        return {
            "id": self.policy_id or self.name or hash_preview(self.raw_key_hash),
            "name": self.name,
            "enabled": self.enabled,
            "preview": self.preview or hash_preview(self.raw_key_hash),
            "rpm": self.rpm,
            "models": list(self.models),
            "limits": {
                "daily_usd": self.daily_limit_usd,
                "weekly_usd": self.weekly_limit_usd,
            },
            "usage": {
                "daily_usd": self.daily_usage_usd,
                "weekly_usd": self.weekly_usage_usd,
            },
            "pricing": {
                "priced_model_count": len(self.model_prices),
                "models": [price.safe_dict() for price in self.model_prices.values()],
            },
        }


class KeyPolicyState:
    def __init__(self, keys: list[KeyRecord], *, source: Path | None = None) -> None:
        self.keys = keys
        self.source = source
        self._by_raw_hash = {item.raw_key_hash: item for item in keys}
        self._by_cpamp_hash = {item.cpamp_hash: item for item in keys}

    @classmethod
    def load(cls, path: str | Path) -> "KeyPolicyState":
        source = Path(path)
        data = json.loads(source.read_text(encoding="utf-8"))
        keys = [_parse_key(item) for item in _extract_keys(data)]
        return cls([key for key in keys if key is not None], source=source)

    def get(self, key_hash: str) -> KeyRecord | None:
        normalized = normalize_key_hash(key_hash)
        return self._by_raw_hash.get(normalized) or self._by_cpamp_hash.get(normalized)

    def get_by_raw_hash(self, key_hash: str) -> KeyRecord | None:
        return self._by_raw_hash.get(normalize_key_hash(key_hash))

    def get_by_cpamp_hash(self, key_hash: str) -> KeyRecord | None:
        return self._by_cpamp_hash.get(normalize_key_hash(key_hash))

    def enabled_keys(self) -> list[KeyRecord]:
        return [key for key in self.keys if key.enabled]


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


def _parse_key(raw: dict[str, Any]) -> KeyRecord | None:
    raw_hash = _first(raw, "key_hash", "keyHash", "hash", "api_key_hash", "apiKeyHash")
    if not isinstance(raw_hash, str) or not raw_hash.strip():
        return None
    try:
        normalized = normalize_key_hash(raw_hash)
    except ValueError:
        return None

    disabled = bool(_first(raw, "disabled", "is_disabled", "isDisabled") or False)
    enabled_raw = _first(raw, "enabled", "is_enabled", "isEnabled")
    enabled = bool(enabled_raw) if enabled_raw is not None else not disabled
    model_items = _as_list(_first(raw, "models", "allowed_models", "allowedModels", "model_allowlist", "modelAllowlist", "aliases"))
    models = _parse_models(model_items)
    model_prices = _parse_model_prices(raw, model_items)
    name = str(_first(raw, "name", "label", "alias", "description") or "").strip()
    if not name:
        name = hash_preview(normalized)
    raw_id = _first(raw, "id", "key_id", "keyId")
    policy_id = str(raw_id).strip() if raw_id is not None else None

    return KeyRecord(
        key_hash=key_policy_hash(normalized),
        name=name,
        enabled=enabled and not disabled,
        policy_id=policy_id or None,
        rpm=_int_or_none(_first(raw, "rpm", "rpm_limit", "rpmLimit", "rpm_per_minute", "rpmPerMinute")),
        models=models,
        model_prices=model_prices,
        daily_limit_usd=_float_or_none(_first(
            raw,
            "daily_limit_usd",
            "dailyLimitUsd",
            "daily_limit",
            "dailyLimit",
            "daily_usd",
            "dailyUsd",
            "daily_quota_usd",
            "dailyQuotaUsd",
            "daily_spend_limit_usd",
            "dailySpendLimitUsd",
        )),
        weekly_limit_usd=_float_or_none(_first(
            raw,
            "weekly_limit_usd",
            "weeklyLimitUsd",
            "weekly_limit",
            "weeklyLimit",
            "weekly_usd",
            "weeklyUsd",
            "weekly_quota_usd",
            "weeklyQuotaUsd",
            "weekly_spend_limit_usd",
            "weeklySpendLimitUsd",
        )),
        daily_usage_usd=_float_or_none(_first(raw, "daily_usage_usd", "dailyUsageUsd", "daily_usage", "dailyUsage")),
        weekly_usage_usd=_float_or_none(_first(raw, "weekly_usage_usd", "weeklyUsageUsd", "weekly_usage", "weeklyUsage")),
        preview=str(_first(raw, "preview", "key_preview", "keyPreview") or "").strip() or None,
        raw=dict(raw),
    )


def has_model_prices(record: KeyRecord) -> bool:
    return bool(record.model_prices)


def _parse_models(items: list[Any]) -> list[str]:
    values: list[str] = []
    for item in items:
        if isinstance(item, dict):
            name = _model_name(item)
        else:
            name = str(item or "").strip()
        if name and name not in values:
            values.append(name)
    return values


def _model_name(raw: dict[str, Any]) -> str:
    return str(_first(
        raw,
        "alias",
        "model",
        "name",
        "id",
        "target_model",
        "targetModel",
        "upstream_model",
        "upstreamModel",
    ) or "").strip()


def _parse_model_prices(raw: dict[str, Any], model_items: list[Any]) -> dict[str, ModelPrice]:
    prices: dict[str, ModelPrice] = {}

    for item in model_items:
        if isinstance(item, dict):
            price = _parse_price_entry(item)
            if price is not None:
                prices[price.model] = price

    raw_prices = _first(raw, "model_prices", "modelPrices", "prices")
    if isinstance(raw_prices, dict):
        for name, item in raw_prices.items():
            if isinstance(item, dict):
                price = _parse_price_entry(item, default_model=str(name))
            else:
                price = _parse_price_entry({"input": item}, default_model=str(name))
            if price is not None:
                prices[price.model] = price
    elif isinstance(raw_prices, list):
        for item in raw_prices:
            if isinstance(item, dict):
                price = _parse_price_entry(item)
                if price is not None:
                    prices[price.model] = price

    return prices


def _parse_price_entry(raw: dict[str, Any], *, default_model: str | None = None) -> ModelPrice | None:
    model = _model_name(raw) or str(default_model or "").strip()
    if not model:
        return None
    input_price = _price(raw, "input_price_per_million", "inputPricePerMillion", "input", "prompt", "prompt_price_per_million")
    output_price = _price(raw, "output_price_per_million", "outputPricePerMillion", "output", "completion", "completion_price_per_million")
    cache_read_price = _price(
        raw,
        "cache_read_price_per_million",
        "cacheReadPricePerMillion",
        "cache_price_per_million",
        "cachePricePerMillion",
        "cache_read",
        "cacheRead",
        "cache",
        "input_cache_read",
        "inputCacheRead",
    )
    cache_creation_price = _price(
        raw,
        "cache_creation_price_per_million",
        "cacheCreationPricePerMillion",
        "cache_write_price_per_million",
        "cacheWritePricePerMillion",
        "cache_creation",
        "cacheCreation",
        "cache_write",
        "cacheWrite",
        "input_cache_write",
        "inputCacheWrite",
    )
    if not any((value or 0) > 0 for value in (input_price, output_price, cache_read_price, cache_creation_price)):
        return None
    target_model = str(_first(raw, "target_model", "targetModel", "upstream_model", "upstreamModel") or "").strip() or None
    provider = str(_first(raw, "provider", "type") or "").strip() or None
    return ModelPrice(
        model=model,
        input_per_million=input_price or 0.0,
        output_per_million=output_price or 0.0,
        cache_read_per_million=cache_read_price or 0.0,
        cache_creation_per_million=cache_creation_price or 0.0,
        target_model=target_model,
        provider=provider,
    )


def _price(raw: dict[str, Any], *names: str) -> float | None:
    return _float_or_none(_first(raw, *names))
