package policyplus

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

type ModelPrice struct {
	Model                   string  `json:"model"`
	TargetModel             string  `json:"target_model,omitempty"`
	Provider                string  `json:"provider,omitempty"`
	InputPerMillion         float64 `json:"input_per_million"`
	OutputPerMillion        float64 `json:"output_per_million"`
	CacheReadPerMillion     float64 `json:"cache_read_per_million"`
	CacheCreationPerMillion float64 `json:"cache_creation_per_million"`
}

type ModelOption struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
	Type        string `json:"type,omitempty"`
	OwnedBy     string `json:"owned_by,omitempty"`
	Source      string `json:"source,omitempty"`
	Known       bool   `json:"known"`
}

type KeyRecord struct {
	ID                string                `json:"id"`
	Name              string                `json:"name"`
	KeyHash           string                `json:"key_hash"`
	Enabled           bool                  `json:"enabled"`
	Preview           string                `json:"preview"`
	RPM               int                   `json:"rpm,omitempty"`
	Concurrency       int                   `json:"concurrency,omitempty"`
	MaxActiveSessions int                   `json:"max_active_sessions,omitempty"`
	Models            []string              `json:"models"`
	Prices            map[string]ModelPrice `json:"prices,omitempty"`
	DailyLimitUSD     *float64              `json:"daily_limit_usd,omitempty"`
	WeeklyLimitUSD    *float64              `json:"weekly_limit_usd,omitempty"`
	FiveHourUSD       *float64              `json:"five_hour_usd,omitempty"`
	MonthlyLimitUSD   *float64              `json:"monthly_limit_usd,omitempty"`
	Archived          bool                  `json:"archived,omitempty"`
	ArchivedAt        int64                 `json:"archived_at,omitempty"`
	Source            string                `json:"source,omitempty"`
	SourcePresent     bool                  `json:"source_present"`
	Alias             string                `json:"alias,omitempty"`
	InheritedFrom     string                `json:"inherited_from,omitempty"`
	InheritConflict   bool                  `json:"inherit_conflict,omitempty"`
	Hidden            bool                  `json:"hidden,omitempty"`
	LastEnabled       bool                  `json:"last_enabled,omitempty"`
}

func (k KeyRecord) Safe() map[string]any {
	sourcePresent := k.SourcePresent
	if k.Source == "" {
		sourcePresent = true
	}
	return map[string]any{
		"id":                  k.ID,
		"name":                k.Name,
		"enabled":             k.Enabled,
		"preview":             k.Preview,
		"rpm":                 k.RPM,
		"concurrency":         0,
		"max_active_sessions": 0,
		"models":              append([]string(nil), k.Models...),
		"archived":            k.Archived,
		"archived_at":         k.ArchivedAt,
		"source":              k.Source,
		"source_present":      sourcePresent,
		"alias":               k.Alias,
		"inherited_from":      k.InheritedFrom,
		"inherit_conflict":    k.InheritConflict,
		"hidden":              k.Hidden,
		"last_enabled":        k.LastEnabled,
		"limits": map[string]any{
			"five_hour_usd": k.FiveHourUSD,
			"daily_usd":     k.DailyLimitUSD,
			"weekly_usd":    k.WeeklyLimitUSD,
			"monthly_usd":   k.MonthlyLimitUSD,
		},
		"pricing": map[string]any{
			"models": k.Prices,
		},
	}
}

type KeyPolicyState struct {
	Keys []KeyRecord
}

func ValidateKeyRecord(key KeyRecord) error {
	if strings.TrimSpace(key.ID) == "" {
		return fmt.Errorf("missing key id")
	}
	if key.RPM < 0 || key.Concurrency < 0 || key.MaxActiveSessions < 0 {
		return fmt.Errorf("limits must not be negative")
	}
	for _, limit := range []*float64{key.FiveHourUSD, key.DailyLimitUSD, key.WeeklyLimitUSD, key.MonthlyLimitUSD} {
		if limit != nil && *limit < 0 {
			return fmt.Errorf("usd limits must not be negative")
		}
	}
	for name, price := range key.Prices {
		if price.InputPerMillion < 0 || price.OutputPerMillion < 0 ||
			price.CacheReadPerMillion < 0 || price.CacheCreationPerMillion < 0 {
			return fmt.Errorf("model price must not be negative: %s", name)
		}
	}
	return nil
}

func LoadKeyPolicyState(path string) (KeyPolicyState, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return KeyPolicyState{}, err
	}
	var data any
	if err := json.Unmarshal(raw, &data); err != nil {
		return KeyPolicyState{}, err
	}
	keys := extractKeys(data)
	out := make([]KeyRecord, 0, len(keys))
	for _, rawKey := range keys {
		if key, ok := parseKey(rawKey); ok {
			out = append(out, key)
		}
	}
	return KeyPolicyState{Keys: out}, nil
}

func (s KeyPolicyState) FindByRawKey(rawKey string) (KeyRecord, bool) {
	hash := SHA256Hex(rawKey)
	return s.FindByRawHash(hash)
}

func (s KeyPolicyState) FindByRawHash(hash string) (KeyRecord, bool) {
	normalized, err := NormalizeHash(hash)
	if err != nil {
		return KeyRecord{}, false
	}
	for _, key := range s.Keys {
		keyHash, err := NormalizeHash(key.KeyHash)
		if err == nil && keyHash == normalized {
			return key, true
		}
	}
	return KeyRecord{}, false
}

func extractKeys(data any) []map[string]any {
	if arr, ok := data.([]any); ok {
		return mapsFromArray(arr)
	}
	obj, ok := data.(map[string]any)
	if !ok {
		return nil
	}
	for _, path := range [][]string{{"keys"}, {"state", "keys"}, {"data", "keys"}, {"config", "keys"}} {
		var cur any = obj
		for _, part := range path {
			m, ok := cur.(map[string]any)
			if !ok {
				cur = nil
				break
			}
			cur = m[part]
		}
		if arr, ok := cur.([]any); ok {
			return mapsFromArray(arr)
		}
	}
	return nil
}

func mapsFromArray(arr []any) []map[string]any {
	out := make([]map[string]any, 0, len(arr))
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

const (
	NativeCPASource  = "native_cpa"
	LegacyPlusSource = "legacy_plus"
)

type NativeKeySyncInput struct {
	RawKey string
	Alias  string
}

func NativeKeyIDFromHash(hash string) string {
	return "native_" + sanitizeIDPart(HashPreview(hash))
}

func NativeKeyPreviewFromHash(hash string) string {
	return HashPreview(hash)
}

func NativeKeyRecord(rawKey, alias string) (KeyRecord, bool) {
	rawKey = NormalizeSubmittedKey(rawKey)
	if rawKey == "" {
		return KeyRecord{}, false
	}
	hash := SHA256Hex(rawKey)
	normalized, err := NormalizeHash(hash)
	if err != nil {
		return KeyRecord{}, false
	}
	alias = strings.TrimSpace(alias)
	preview := NativeKeyPreviewFromHash(normalized)
	name := alias
	if name == "" {
		name = preview
	}
	return KeyRecord{
		ID:            NativeKeyIDFromHash(normalized),
		Name:          name,
		KeyHash:       "sha256:" + normalized,
		Enabled:       true,
		Preview:       preview,
		Source:        NativeCPASource,
		SourcePresent: true,
		Alias:         alias,
		Hidden:        false,
	}, true
}

func CopyPolicyFields(dst, src KeyRecord) KeyRecord {
	dst.Enabled = src.Enabled
	if (src.Source == NativeCPASource || src.Source == LegacyPlusSource) && !src.SourcePresent {
		dst.Enabled = src.LastEnabled
	}
	dst.RPM = src.RPM
	dst.Concurrency = 0
	dst.MaxActiveSessions = 0
	dst.Models = append([]string(nil), src.Models...)
	dst.Prices = clonePrices(src.Prices)
	dst.FiveHourUSD = cloneFloatPtr(src.FiveHourUSD)
	dst.DailyLimitUSD = cloneFloatPtr(src.DailyLimitUSD)
	dst.WeeklyLimitUSD = cloneFloatPtr(src.WeeklyLimitUSD)
	dst.MonthlyLimitUSD = cloneFloatPtr(src.MonthlyLimitUSD)
	return dst
}

func clonePrices(in map[string]ModelPrice) map[string]ModelPrice {
	if in == nil {
		return nil
	}
	out := make(map[string]ModelPrice, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneFloatPtr(in *float64) *float64 {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

var idPartCleanup = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

func sanitizeIDPart(value string) string {
	value = strings.ReplaceAll(value, "...", "_")
	value = strings.ReplaceAll(value, "…", "_")
	value = idPartCleanup.ReplaceAllString(value, "_")
	value = strings.Trim(value, "_")
	if value == "" {
		return "unknown"
	}
	return value
}

func parseKey(raw map[string]any) (KeyRecord, bool) {
	rawHash := firstString(raw, "key_hash", "keyHash", "hash", "api_key_hash", "apiKeyHash")
	if rawHash == "" {
		return KeyRecord{}, false
	}
	normalized, err := NormalizeHash(rawHash)
	if err != nil {
		return KeyRecord{}, false
	}
	id := firstString(raw, "id", "key_id", "keyId")
	if id == "" {
		id = HashPreview(normalized)
	}
	name := firstString(raw, "name", "label", "alias", "description")
	if name == "" {
		name = id
	}
	enabled, hasEnabled := firstBool(raw, "enabled", "is_enabled", "isEnabled")
	disabled, _ := firstBool(raw, "disabled", "is_disabled", "isDisabled")
	if !hasEnabled {
		enabled = !disabled
	}
	archived, _ := firstBool(raw, "archived", "is_archived", "isArchived")
	modelItems := asList(firstAny(raw, "models", "allowed_models", "allowedModels", "model_allowlist", "modelAllowlist", "aliases"))
	models := parseModels(modelItems)
	prices := parsePrices(raw, modelItems)
	return KeyRecord{
		ID:                strings.TrimSpace(id),
		Name:              strings.TrimSpace(name),
		KeyHash:           "sha256:" + normalized,
		Enabled:           enabled && !disabled,
		Preview:           firstNonEmpty(firstString(raw, "preview", "key_preview", "keyPreview"), HashPreview(normalized)),
		RPM:               firstInt(raw, "rpm", "rpm_limit", "rpmLimit", "rpm_per_minute", "rpmPerMinute"),
		Concurrency:       firstInt(raw, "concurrency", "concurrency_limit", "concurrencyLimit", "max_concurrent", "maxConcurrent", "request_concurrency", "requestConcurrency"),
		MaxActiveSessions: firstInt(raw, "max_active_sessions", "maxActiveSessions", "max_sessions", "maxSessions", "session_limit", "sessionLimit", "max_codex_windows", "maxCodexWindows"),
		Models:            models,
		Prices:            prices,
		DailyLimitUSD:     firstFloatPtr(raw, "daily_limit_usd", "dailyLimitUsd", "daily_limit", "dailyLimit", "daily_usd", "dailyUsd"),
		WeeklyLimitUSD:    firstFloatPtr(raw, "weekly_limit_usd", "weeklyLimitUsd", "weekly_limit", "weeklyLimit", "weekly_usd", "weeklyUsd"),
		FiveHourUSD:       firstFloatPtr(raw, "five_hour_limit_usd", "fiveHourLimitUsd", "five_hour_usd", "fiveHourUsd", "5h_limit_usd"),
		MonthlyLimitUSD:   firstFloatPtr(raw, "monthly_limit_usd", "monthlyLimitUsd", "monthly_usd", "monthlyUsd", "month_limit_usd"),
		Archived:          archived,
	}, true
}

func parseModels(items []any) []string {
	seen := map[string]bool{}
	var out []string
	for _, item := range items {
		name := ""
		if m, ok := item.(map[string]any); ok {
			name = modelName(m)
		} else {
			name = strings.TrimSpace(toString(item))
		}
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

func ModelOptionsFromIDs(ids []string, source string, known bool) []ModelOption {
	out := make([]ModelOption, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		key := strings.ToLower(id)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, ModelOption{ID: id, DisplayName: id, Source: source, Known: known})
	}
	return out
}

func NormalizeModelOptions(data any, source string) []ModelOption {
	items := modelOptionItems(data)
	out := make([]ModelOption, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		option, ok := parseModelOption(item, source)
		if !ok {
			continue
		}
		key := strings.ToLower(option.ID)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, option)
	}
	return out
}

func MergeModelOptions(groups ...[]ModelOption) []ModelOption {
	out := []ModelOption{}
	byID := map[string]int{}
	for _, group := range groups {
		for _, item := range group {
			item.ID = strings.TrimSpace(item.ID)
			if item.ID == "" {
				continue
			}
			if strings.TrimSpace(item.DisplayName) == "" {
				item.DisplayName = item.ID
			}
			key := strings.ToLower(item.ID)
			if idx, ok := byID[key]; ok {
				existing := out[idx]
				if existing.DisplayName == existing.ID && item.DisplayName != "" {
					existing.DisplayName = item.DisplayName
				}
				if existing.Type == "" {
					existing.Type = item.Type
				}
				if existing.OwnedBy == "" {
					existing.OwnedBy = item.OwnedBy
				}
				if existing.Source == "" {
					existing.Source = item.Source
				}
				existing.Known = existing.Known || item.Known
				out[idx] = existing
				continue
			}
			byID[key] = len(out)
			out = append(out, item)
		}
	}
	return out
}

func modelOptionItems(data any) []any {
	switch v := data.(type) {
	case nil:
		return nil
	case []any:
		return v
	case []string:
		out := make([]any, len(v))
		for i := range v {
			out[i] = v[i]
		}
		return out
	case []ModelOption:
		out := make([]any, len(v))
		for i := range v {
			out[i] = v[i]
		}
		return out
	case map[string]any:
		for _, name := range []string{"models", "data", "items", "result"} {
			if raw, ok := v[name]; ok {
				if items := modelOptionItems(raw); len(items) > 0 {
					return items
				}
			}
		}
		out := make([]any, 0, len(v))
		for id, raw := range v {
			if m, ok := raw.(map[string]any); ok {
				if _, hasID := m["id"]; !hasID {
					m["id"] = id
				}
				out = append(out, m)
				continue
			}
			out = append(out, id)
		}
		return out
	default:
		text := strings.TrimSpace(toString(v))
		if text == "" {
			return nil
		}
		return []any{text}
	}
}

func parseModelOption(item any, source string) (ModelOption, bool) {
	switch v := item.(type) {
	case ModelOption:
		v.ID = strings.TrimSpace(v.ID)
		if v.ID == "" {
			return ModelOption{}, false
		}
		if strings.TrimSpace(v.DisplayName) == "" {
			v.DisplayName = v.ID
		}
		if v.Source == "" {
			v.Source = source
		}
		return v, true
	case string:
		id := strings.TrimSpace(v)
		if id == "" {
			return ModelOption{}, false
		}
		return ModelOption{ID: id, DisplayName: id, Source: source, Known: true}, true
	case map[string]any:
		id := modelName(v)
		if id == "" {
			return ModelOption{}, false
		}
		display := firstString(v, "display_name", "displayName", "label", "name")
		if display == "" {
			display = id
		}
		known, hasKnown := firstBool(v, "known")
		if !hasKnown {
			known = true
		}
		src := firstString(v, "source")
		if src == "" {
			src = source
		}
		return ModelOption{
			ID:          id,
			DisplayName: display,
			Type:        firstString(v, "type", "object"),
			OwnedBy:     firstString(v, "owned_by", "ownedBy", "provider"),
			Source:      src,
			Known:       known,
		}, true
	default:
		id := strings.TrimSpace(toString(v))
		if id == "" {
			return ModelOption{}, false
		}
		return ModelOption{ID: id, DisplayName: id, Source: source, Known: true}, true
	}
}

func parsePrices(raw map[string]any, modelItems []any) map[string]ModelPrice {
	out := map[string]ModelPrice{}
	for _, item := range modelItems {
		if m, ok := item.(map[string]any); ok {
			if p, ok := parsePriceEntry(m, ""); ok {
				out[p.Model] = p
			}
		}
	}
	priceRaw := firstAny(raw, "model_prices", "modelPrices", "prices")
	switch v := priceRaw.(type) {
	case map[string]any:
		for name, item := range v {
			if m, ok := item.(map[string]any); ok {
				if p, ok := parsePriceEntry(m, name); ok {
					out[p.Model] = p
				}
			}
		}
	case []any:
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				if p, ok := parsePriceEntry(m, ""); ok {
					out[p.Model] = p
				}
			}
		}
	}
	return out
}

func parsePriceEntry(raw map[string]any, defaultModel string) (ModelPrice, bool) {
	model := modelName(raw)
	if model == "" {
		model = strings.TrimSpace(defaultModel)
	}
	if model == "" {
		return ModelPrice{}, false
	}
	price := ModelPrice{
		Model:                   model,
		TargetModel:             firstString(raw, "target_model", "targetModel", "upstream_model", "upstreamModel"),
		Provider:                firstString(raw, "provider", "type"),
		InputPerMillion:         firstFloat(raw, "input_price_per_million", "inputPricePerMillion", "input", "prompt", "prompt_price_per_million"),
		OutputPerMillion:        firstFloat(raw, "output_price_per_million", "outputPricePerMillion", "output", "completion", "completion_price_per_million"),
		CacheReadPerMillion:     firstFloat(raw, "cache_read_price_per_million", "cacheReadPricePerMillion", "cache_price_per_million", "cachePricePerMillion", "cache_read", "cacheRead", "cache"),
		CacheCreationPerMillion: firstFloat(raw, "cache_creation_price_per_million", "cacheCreationPricePerMillion", "cache_write_price_per_million", "cacheWritePricePerMillion", "cache_creation", "cacheCreation", "cache_write", "cacheWrite"),
	}
	if price.InputPerMillion <= 0 && price.OutputPerMillion <= 0 && price.CacheReadPerMillion <= 0 && price.CacheCreationPerMillion <= 0 {
		return ModelPrice{}, false
	}
	return price, true
}

func modelName(raw map[string]any) string {
	return firstString(raw, "alias", "model", "name", "id", "target_model", "targetModel", "upstream_model", "upstreamModel")
}

func firstAny(raw map[string]any, names ...string) any {
	for _, name := range names {
		if value, ok := raw[name]; ok {
			return value
		}
	}
	return nil
}

func firstString(raw map[string]any, names ...string) string {
	for _, name := range names {
		if value, ok := raw[name]; ok {
			text := strings.TrimSpace(toString(value))
			if text != "" {
				return text
			}
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstBool(raw map[string]any, names ...string) (bool, bool) {
	for _, name := range names {
		if value, ok := raw[name]; ok {
			switch v := value.(type) {
			case bool:
				return v, true
			case string:
				switch strings.ToLower(strings.TrimSpace(v)) {
				case "true", "1", "yes", "enabled":
					return true, true
				case "false", "0", "no", "disabled":
					return false, true
				}
			}
		}
	}
	return false, false
}

func firstInt(raw map[string]any, names ...string) int {
	for _, name := range names {
		if value, ok := raw[name]; ok {
			switch v := value.(type) {
			case float64:
				return int(v)
			case int:
				return v
			case string:
				var n int
				if err := json.NewDecoder(strings.NewReader(v)).Decode(&n); err == nil {
					return n
				}
			}
		}
	}
	return 0
}

func firstFloat(raw map[string]any, names ...string) float64 {
	ptr := firstFloatPtr(raw, names...)
	if ptr == nil {
		return 0
	}
	return *ptr
}

func firstFloatPtr(raw map[string]any, names ...string) *float64 {
	for _, name := range names {
		if value, ok := raw[name]; ok {
			switch v := value.(type) {
			case float64:
				return &v
			case int:
				f := float64(v)
				return &f
			case string:
				var f float64
				if err := json.NewDecoder(strings.NewReader(v)).Decode(&f); err == nil {
					return &f
				}
			}
		}
	}
	return nil
}

func asList(value any) []any {
	switch v := value.(type) {
	case []any:
		return v
	case []string:
		out := make([]any, len(v))
		for i := range v {
			out[i] = v[i]
		}
		return out
	case string:
		parts := strings.Split(v, ",")
		out := make([]any, 0, len(parts))
		for _, part := range parts {
			if text := strings.TrimSpace(part); text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func toString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	default:
		if value == nil {
			return ""
		}
		return strings.TrimSpace(fmt.Sprint(value))
	}
}
