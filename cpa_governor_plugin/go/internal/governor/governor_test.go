package governor

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func ptr(v float64) *float64 { return &v }

func writePolicyState(t *testing.T, dir string, rawKey string) string {
	t.Helper()
	path := filepath.Join(dir, "key-policy.json")
	body := map[string]any{
		"keys": []map[string]any{{
			"id":       "alice-key",
			"name":     "Alice",
			"key_hash": "sha256:" + SHA256Hex(rawKey),
			"enabled":  true,
			"rpm":      12,
			"models": []map[string]any{{
				"alias":                        "gpt-5.5",
				"target_model":                 "gpt-5.5",
				"input_price_per_million":      5,
				"output_price_per_million":     30,
				"cache_read_price_per_million": 0.5,
			}},
			"daily_limit_usd":  5,
			"weekly_limit_usd": 30,
		}},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestKeyPolicyStateParsesSafeRecords(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyState(t, dir, "cpa_live")
	state, err := LoadKeyPolicyState(path)
	if err != nil {
		t.Fatal(err)
	}
	key, ok := state.FindByRawKey(" cpa_live ")
	if !ok {
		t.Fatal("raw key did not match policy state")
	}
	if key.ID != "alice-key" || key.Name != "Alice" || !key.Enabled {
		t.Fatalf("unexpected key: %#v", key)
	}
	if len(key.Models) != 1 || key.Models[0] != "gpt-5.5" {
		t.Fatalf("models = %#v", key.Models)
	}
	price, ok := PriceForModel(key.Prices, "GPT-5.5")
	if !ok || price.InputPerMillion != 5 || price.OutputPerMillion != 30 || price.CacheReadPerMillion != 0.5 {
		t.Fatalf("price = %#v ok=%v", price, ok)
	}
	safe := key.Safe()
	encoded, _ := json.Marshal(safe)
	if strings.Contains(string(encoded), SHA256Hex("cpa_live")) || strings.Contains(string(encoded), "cpa_live") {
		t.Fatalf("safe projection leaked key material: %s", encoded)
	}
}

func TestPricingBreakdownUsesPerMillionAndCachedInput(t *testing.T) {
	price := ModelPrice{
		Model:               "gpt-5.5",
		InputPerMillion:     5,
		OutputPerMillion:    30,
		CacheReadPerMillion: 0.5,
	}
	breakdown := CostForUsage(price, TokenUsage{
		InputTokens:     100,
		CachedTokens:    20,
		OutputTokens:    50,
		ReasoningTokens: 30,
		TotalTokens:     150,
	}, "gpt-5.5")
	if got := breakdown.Tokens["billable_uncached_input"]; got != 80 {
		t.Fatalf("billable input = %d", got)
	}
	if got := breakdown.Tokens["visible_output_estimate"]; got != 20 {
		t.Fatalf("visible output estimate = %d", got)
	}
	if breakdown.Costs["total"] <= 0 {
		t.Fatalf("total cost should be positive: %#v", breakdown.Costs)
	}
	if breakdown.Costs["cached_input"] <= 0 {
		t.Fatalf("cached input should be charged with cache read price: %#v", breakdown.Costs)
	}
}

func TestStoreUsageWindowsAndSoftReset(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "governor.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := KeyRecord{
		ID:              "alice-key",
		Name:            "Alice",
		KeyHash:         "sha256:" + SHA256Hex("cpa_live"),
		Enabled:         true,
		Preview:         HashPreview(SHA256Hex("cpa_live")),
		FiveHourUSD:     ptr(1),
		MonthlyLimitUSD: ptr(10),
	}
	if err := store.UpsertKey(ctx, key); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := store.InsertUsage(ctx, UsageEvent{
		RequestID:   "req-old",
		KeyID:       "alice-key",
		RequestedAt: now.Add(-2 * time.Hour),
		Cost:        0.5,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertUsage(ctx, UsageEvent{
		RequestID:   "req-new",
		KeyID:       "alice-key",
		RequestedAt: now.Add(-30 * time.Minute),
		Cost:        0.25,
	}); err != nil {
		t.Fatal(err)
	}
	used, err := store.UsageSum(ctx, "alice-key", WindowFor(Range5H, now))
	if err != nil {
		t.Fatal(err)
	}
	if used != 0.75 {
		t.Fatalf("used before reset = %v", used)
	}
	if err := store.Reset(ctx, "alice-key", Range5H, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	used, err = store.UsageSum(ctx, "alice-key", WindowFor(Range5H, now))
	if err != nil {
		t.Fatal(err)
	}
	if used != 0.25 {
		t.Fatalf("used after reset = %v", used)
	}
	summary, err := store.UsageSummary(ctx, "alice-key", WindowFor(Range5H, now))
	if err != nil {
		t.Fatal(err)
	}
	if summary.Calls != 1 || summary.TotalCost != 0.25 {
		t.Fatalf("summary after reset = %#v", summary)
	}
}

func TestStoreMigratesOldUsageEventsSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "governor.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`create table usage_events (
		id integer primary key autoincrement,
		request_id text,
		key_id text,
		key_preview text,
		model text,
		endpoint text,
		requested_at integer not null,
		latency_ms integer,
		failed integer not null,
		failure text,
		input_tokens integer,
		output_tokens integer,
		cached_tokens integer,
		cache_read_tokens integer,
		cache_creation_tokens integer,
		reasoning_tokens integer,
		total_tokens integer,
		cost real,
		cost_breakdown_json text
	)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into usage_events(request_id, key_id, model, requested_at, failed, cost, cost_breakdown_json) values('old-1', 'alice-key', 'gpt-5.5', ?, 0, 0.1, '{}')`, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events, err := store.RecentEvents(context.Background(), "alice-key", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].RequestID != "old-1" {
		t.Fatalf("events = %#v", events)
	}
	if events[0].RequestedModel != "" || events[0].TTFTMS != 0 || events[0].StatusCode != 0 {
		t.Fatalf("old row should read safe zero values: %#v", events[0])
	}
}

func TestStoreRecentCodexSummariesFiltersByKey(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "governor.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveCodexSummary(ctx, "req-a", "alice-key", "gpt-5.5", "auto_continued", map[string]any{
		"request_id": "req-a",
		"protection": "auto_continued",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCodexSummary(ctx, "req-b", "bob-key", "gpt-5.5", "protected_clean", map[string]any{
		"request_id": "req-b",
		"protection": "protected_clean",
	}); err != nil {
		t.Fatal(err)
	}
	alice, err := store.RecentCodexSummaries(ctx, "alice-key", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(alice) != 1 || alice[0].RequestID != "req-a" || alice[0].Protection != "auto_continued" {
		t.Fatalf("alice summaries = %#v", alice)
	}
	all, err := store.RecentCodexSummaries(ctx, "all", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("all summaries = %#v", all)
	}
}

func TestSecurityAndRedaction(t *testing.T) {
	raw := " cpa_live "
	hash := SHA256Hex(raw)
	if hash != SHA256Hex(strings.TrimSpace(raw)) {
		t.Fatal("SHA256Hex should trim raw keys")
	}
	if hash != SHA256Hex("Bearer cpa_live") || hash != SHA256Hex("Authorization: Bearer cpa_live") {
		t.Fatal("SHA256Hex should normalize pasted bearer prefixes")
	}
	token, err := SignSession(SessionPayload{KeyID: "alice", KeyHash: "sha256:" + hash, ExpiresAt: time.Now().Add(time.Hour).Unix()}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	payload, ok := VerifySession(token, "secret", time.Now())
	if !ok || payload.KeyID != "alice" {
		t.Fatalf("session verify failed: %#v ok=%v", payload, ok)
	}
	if _, ok := VerifySession(token, "wrong", time.Now()); ok {
		t.Fatal("session verified with wrong secret")
	}
	brief := Brief("Authorization: Bearer secret and api_key=abc", 200)
	if strings.Contains(brief, "secret") || strings.Contains(brief, "abc") {
		t.Fatalf("secret leaked in brief: %s", brief)
	}
}

func TestSubmittedKeyHints(t *testing.T) {
	cases := []struct {
		name string
		in   string
		code string
	}{
		{name: "missing", in: " ", code: "missing_api_key"},
		{name: "native", in: "sk-abc", code: "native_cpa_key_not_supported"},
		{name: "preview", in: "cpa_abcd...efgh", code: "key_preview_not_usable"},
		{name: "unsupported", in: "abc", code: "unsupported_key_format"},
		{name: "short cpa", in: "Bearer cpa_live", code: "key_preview_not_usable"},
		{name: "full cpa", in: "Bearer cpa_abcdefghijklmnopqrstuvwxyz0123456789", code: "invalid_api_key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hint := ExplainUnmatchedSubmittedKey(tc.in)
			if hint.Error != tc.code || hint.Message == "" {
				t.Fatalf("hint = %#v", hint)
			}
		})
	}
	if got := NormalizeSubmittedKey("Authorization: Bearer Bearer cpa_live "); got != "cpa_live" {
		t.Fatalf("normalized key = %q", got)
	}
	if got := NormalizeSubmittedKey("\ufeff“Bearer cpa_live\u200b”"); got != "cpa_live" {
		t.Fatalf("normalized decorated key = %q", got)
	}
}
