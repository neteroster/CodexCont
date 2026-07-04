package policyplus

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func ptr(v float64) *float64 { return &v }

func TestSQLiteOpenStoreUsesSingleConnectionAndBusyTimeout(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "policyplus.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if got := store.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("main store should use one DB connection, got %d", got)
	}
	var timeout int
	if err := store.db.QueryRowContext(ctx, `pragma busy_timeout`).Scan(&timeout); err != nil {
		t.Fatal(err)
	}
	if timeout != sqliteBusyTimeoutMS {
		t.Fatalf("busy_timeout=%d want %d", timeout, sqliteBusyTimeoutMS)
	}
}

func TestSQLiteBusyTimeoutWaitsForTransientWriteLock(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "policyplus.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	locker, err := openSQLite(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Close()
	tx, err := locker.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `insert into keys(id, name, key_hash, enabled, updated_at) values('lock-row', 'Lock', 'sha256:lock', 1, ?)`, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- store.UpsertKey(ctx, KeyRecord{
			ID:            "native-wait",
			Name:          "Wait",
			KeyHash:       "sha256:" + SHA256Hex("sk-wait"),
			Enabled:       true,
			Preview:       HashPreview(SHA256Hex("sk-wait")),
			Source:        NativeCPASource,
			SourcePresent: true,
		})
	}()
	time.Sleep(150 * time.Millisecond)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			if strings.Contains(err.Error(), "SQLITE_BUSY") || strings.Contains(err.Error(), "database is locked") {
				t.Fatalf("operation should wait for transient lock, got %v", err)
			}
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("operation did not complete after lock release")
	}
}

func TestReadOnlySQLiteHelperRejectsWrites(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "aliases.sqlite")
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.ExecContext(ctx, `create table api_key_aliases(api_key_hash text primary key, alias text)`); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := openSQLite(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `insert into api_key_aliases(api_key_hash, alias) values('sha256:x', 'x')`); err == nil {
		t.Fatal("read-only helper should reject writes")
	} else if !errors.Is(err, sql.ErrNoRows) && !strings.Contains(strings.ToLower(err.Error()), "readonly") && !strings.Contains(strings.ToLower(err.Error()), "read-only") {
		t.Fatalf("write rejected with unexpected error: %v", err)
	}
}

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

func TestNormalizeModelOptionsAcceptsCommonPayloadShapes(t *testing.T) {
	payload := map[string]any{
		"models": []any{
			map[string]any{"id": "gpt-5.5", "display_name": "GPT 5.5", "owned_by": "openai"},
			map[string]any{"model": "gpt-5.4"},
			"gpt-5.5",
			map[string]any{"alias": "codex-auto-review", "target_model": "gpt-5.5"},
		},
	}
	options := NormalizeModelOptions(payload, "online")
	if len(options) != 3 {
		t.Fatalf("options = %#v", options)
	}
	if options[0].ID != "gpt-5.5" || options[0].DisplayName != "GPT 5.5" || options[0].OwnedBy != "openai" {
		t.Fatalf("first option = %#v", options[0])
	}
	if options[2].ID != "codex-auto-review" {
		t.Fatalf("alias model was not parsed: %#v", options)
	}
}

func TestMergeModelOptionsPreservesUnknownConfiguredModels(t *testing.T) {
	known := NormalizeModelOptions([]any{map[string]any{"id": "gpt-5.5", "display_name": "GPT 5.5"}}, "online")
	configured := ModelOptionsFromIDs([]string{"gpt-5.5", "legacy-custom"}, "plus_configured", false)
	merged := MergeModelOptions(known, configured)
	if len(merged) != 2 {
		t.Fatalf("merged = %#v", merged)
	}
	if !merged[0].Known || merged[0].DisplayName != "GPT 5.5" {
		t.Fatalf("known metadata should win: %#v", merged[0])
	}
	if merged[1].ID != "legacy-custom" || merged[1].Known {
		t.Fatalf("unknown configured model should be preserved: %#v", merged[1])
	}
}

func TestImportKeysDoesNotDeletePlusNativeKeys(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "policyplus.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	native := KeyRecord{
		ID:      "native-plus-key",
		Name:    "Native",
		KeyHash: "sha256:" + SHA256Hex("cpa_native_plus"),
		Enabled: true,
		Preview: HashPreview(SHA256Hex("cpa_native_plus")),
	}
	if err := store.UpsertKey(ctx, native); err != nil {
		t.Fatal(err)
	}
	imported := KeyRecord{
		ID:      "imported-key",
		Name:    "Imported",
		KeyHash: "sha256:" + SHA256Hex("cpa_imported"),
		Enabled: true,
		Preview: HashPreview(SHA256Hex("cpa_imported")),
	}
	if err := store.ImportKeys(ctx, KeyPolicyState{Keys: []KeyRecord{imported}}); err != nil {
		t.Fatal(err)
	}
	keys, err := store.ListKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, key := range keys {
		seen[key.ID] = true
	}
	if !seen[native.ID] || !seen[imported.ID] {
		t.Fatalf("native/imported keys should both remain: %#v", seen)
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
	store, err := OpenStore(filepath.Join(t.TempDir(), "policyplus.sqlite"))
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

func TestStoreDeleteKeyRemovesConfigAndKeepsHistory(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "policyplus.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := KeyRecord{
		ID:      "alice-key",
		Name:    "Alice",
		KeyHash: "sha256:" + SHA256Hex("cpa_live"),
		Enabled: true,
		Preview: HashPreview(SHA256Hex("cpa_live")),
	}
	if err := store.UpsertKey(ctx, key); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := store.Reset(ctx, key.ID, Range5H, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegisterActiveSession(ctx, key.ID, newSessionIdentity("test", "window-a"), 3, DefaultSessionIdle, now); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertUsage(ctx, UsageEvent{
		RequestID:   "req-delete-kept",
		KeyID:       key.ID,
		RequestedAt: now,
		Cost:        0.25,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCodexSummary(ctx, "codex-delete-kept", key.ID, "gpt-5.5", "protected_clean", map[string]any{
		"request_id": "codex-delete-kept",
		"started_at": now.Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}

	if err := store.DeleteKey(ctx, key.ID); err != nil {
		t.Fatal(err)
	}
	keys, err := store.ListKeys(ctx)
	if err != nil || len(keys) != 0 {
		t.Fatalf("keys err=%v keys=%#v", err, keys)
	}
	usage, err := store.UsageSummary(ctx, key.ID, WindowFor(Range24H, now))
	if err != nil {
		t.Fatal(err)
	}
	if usage.Calls != 1 || usage.TotalCost != 0.25 {
		t.Fatalf("usage history should be retained after delete: %#v", usage)
	}
	codex, err := store.RecentCodexSummaries(ctx, key.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(codex) != 1 || codex[0].RequestID != "codex-delete-kept" {
		t.Fatalf("codex history should be retained after delete: %#v", codex)
	}
	var resetCount, activeCount, auditCount int
	if err := store.db.QueryRowContext(ctx, `select count(1) from reset_watermarks where key_id=?`, key.ID).Scan(&resetCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `select count(1) from active_sessions where key_id=?`, key.ID).Scan(&activeCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `select count(1) from audit_log where target=? and action='delete_key'`, key.ID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if resetCount != 0 || activeCount != 0 || auditCount != 1 {
		t.Fatalf("delete cleanup reset=%d active=%d audit=%d", resetCount, activeCount, auditCount)
	}
}

func TestStoreImportsLegacyQuotaSQLite(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	plus, err := OpenStore(filepath.Join(dir, "policyplus.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer plus.Close()
	key := KeyRecord{
		ID:      "alice-key",
		Name:    "Alice",
		KeyHash: "sha256:" + SHA256Hex("cpa_live"),
		Enabled: true,
		Preview: HashPreview(SHA256Hex("cpa_live")),
	}
	if err := plus.UpsertKey(ctx, key); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(dir, "usage-admin.sqlite")
	legacy, err := sql.Open("sqlite", legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`create table key_limits(policy_id text primary key, five_hour_limit_usd real, monthly_limit_usd real, updated_at_ms integer)`); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`create table reset_watermarks(policy_id text, window text, reset_at_ms integer, updated_at_ms integer, primary key(policy_id, window))`); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`insert into key_limits(policy_id, five_hour_limit_usd, monthly_limit_usd, updated_at_ms) values('alice-key', 1.5, 20, 1000)`); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`insert into reset_watermarks(policy_id, window, reset_at_ms, updated_at_ms) values('alice-key', '5h', 2000000000000, 2000000000000)`); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := plus.ImportLegacyQuotaSQLite(ctx, legacyPath, "usage-admin")
	if err != nil {
		t.Fatal(err)
	}
	if result.Limits != 1 || result.Resets != 1 {
		t.Fatalf("import result = %#v", result)
	}
	keys, err := plus.ListKeys(ctx)
	if err != nil || len(keys) != 1 {
		t.Fatalf("keys err=%v keys=%#v", err, keys)
	}
	if keys[0].FiveHourUSD == nil || *keys[0].FiveHourUSD != 1.5 || keys[0].MonthlyLimitUSD == nil || *keys[0].MonthlyLimitUSD != 20 {
		t.Fatalf("legacy limits not imported: %#v", keys[0])
	}
	resetAt, ok := plus.ResetAt(ctx, "alice-key", Range5H)
	if !ok || resetAt != 2000000000 {
		t.Fatalf("legacy reset not imported: resetAt=%d ok=%v", resetAt, ok)
	}
}

func TestNativeKeyLoadersReadCPAConfigAndCPAMPAliases(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("api-keys:\n  - sk-native-one\n  - '  sk-native-two  '\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := LoadNativeKeysFromCPAConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] != "sk-native-one" || keys[1] != "sk-native-two" {
		t.Fatalf("native keys = %#v", keys)
	}

	dbPath := filepath.Join(dir, "cpamp.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`create table api_key_aliases(api_key_hash text primary key, alias text, updated_at_ms integer)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into api_key_aliases(api_key_hash, alias, updated_at_ms) values(?, ?, ?)`, "sha256:"+SHA256Hex("sk-native-one"), "QQ专用", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into api_key_aliases(api_key_hash, alias, updated_at_ms) values(?, ?, ?)`, SHA256Hex("sk-native-two"), "阿伟专用", 2); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into api_key_aliases(api_key_hash, alias, updated_at_ms) values(?, ?, ?)`, "SHA256:"+SHA256Hex("sk-native-three"), "Kuma专用", 3); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	aliases, err := LoadAPIKeyAliasesFromSQLite(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if aliases[SHA256Hex("sk-native-one")] != "QQ专用" {
		t.Fatalf("aliases = %#v", aliases)
	}
	if aliases[SHA256Hex("sk-native-two")] != "阿伟专用" {
		t.Fatalf("bare-hash aliases = %#v", aliases)
	}
	if aliases[SHA256Hex("sk-native-three")] != "Kuma专用" {
		t.Fatalf("case-insensitive prefix aliases = %#v", aliases)
	}
}

func TestNativeAliasLoaderFallsBackAcrossSQLitePaths(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	emptyPath := filepath.Join(dir, "empty.sqlite")
	emptyDB, err := sql.Open("sqlite", emptyPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := emptyDB.Exec(`create table unrelated(id text)`); err != nil {
		t.Fatal(err)
	}
	if err := emptyDB.Close(); err != nil {
		t.Fatal(err)
	}

	aliasPath := filepath.Join(dir, "cpamp-real.sqlite")
	aliasDB, err := sql.Open("sqlite", aliasPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aliasDB.Exec(`create table api_key_aliases(api_key_hash text primary key, alias text, updated_at_ms integer)`); err != nil {
		t.Fatal(err)
	}
	if _, err := aliasDB.Exec(`insert into api_key_aliases(api_key_hash, alias, updated_at_ms) values(?, ?, ?)`, "sha256:"+SHA256Hex("sk-alice"), "alicea", 10); err != nil {
		t.Fatal(err)
	}
	if err := aliasDB.Close(); err != nil {
		t.Fatal(err)
	}

	emptyAliases, err := LoadAPIKeyAliasesFromSQLite(ctx, emptyPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(emptyAliases) != 0 {
		t.Fatalf("missing alias table should be an empty source, got %#v", emptyAliases)
	}
	aliases, errs, err := LoadAPIKeyAliasesFromSQLitePaths(ctx, []string{emptyPath, aliasPath})
	if err != nil || len(errs) != 0 {
		t.Fatalf("fallback alias load err=%v errs=%#v", err, errs)
	}
	if aliases[SHA256Hex("sk-alice")] != "alicea" {
		t.Fatalf("fallback alias missing: %#v", aliases)
	}
}

func TestCheckLimitTreatsZeroAsExplicitLimit(t *testing.T) {
	if decision := CheckLimit(0, nil); !decision.Allowed {
		t.Fatalf("nil limit should mean unlimited: %#v", decision)
	}
	zero := 0.0
	if decision := CheckLimit(0, &zero); decision.Allowed || decision.LimitUSD == nil || *decision.LimitUSD != 0 {
		t.Fatalf("zero limit should deny post-accounting requests: %#v", decision)
	}
	positive := 1.0
	if decision := CheckLimit(0.5, &positive); !decision.Allowed {
		t.Fatalf("below positive limit should pass: %#v", decision)
	}
	if decision := CheckLimit(1, &positive); decision.Allowed {
		t.Fatalf("used >= positive limit should deny: %#v", decision)
	}
}

func TestSyncNativeKeysLifecycleInheritanceAndHistory(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "policyplus.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if err := store.SyncNativeKeys(ctx, []NativeKeySyncInput{{RawKey: "sk-native-old", Alias: "QQ专用"}}); err != nil {
		t.Fatal(err)
	}
	oldID := NativeKeyIDFromHash(SHA256Hex("sk-native-old"))
	old, ok, err := store.FindKeyByHash(ctx, SHA256Hex("sk-native-old"))
	if err != nil || !ok {
		t.Fatalf("old native key err=%v ok=%v", err, ok)
	}
	if old.ID != oldID || !old.Enabled || old.Name != "QQ专用" || old.Alias != "QQ专用" || old.Source != NativeCPASource || !old.SourcePresent || old.Hidden {
		t.Fatalf("new native key should be enabled current official policy row: %#v", old)
	}
	old.Enabled = true
	old.RPM = 7
	old.Models = []string{"gpt-5.5"}
	old.Prices = map[string]ModelPrice{"gpt-5.5": {Model: "gpt-5.5", InputPerMillion: 1}}
	old.FiveHourUSD = ptr(2)
	old.WeeklyLimitUSD = ptr(12)
	if err := store.SaveKeySettings(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertUsage(ctx, UsageEvent{RequestID: "old-usage", KeyID: old.ID, RequestedAt: time.Now(), Cost: 1.25}); err != nil {
		t.Fatal(err)
	}

	if err := store.SyncNativeKeys(ctx, nil); err != nil {
		t.Fatal(err)
	}
	removed, ok, err := store.FindKeyByHash(ctx, SHA256Hex("sk-native-old"))
	if err != nil || !ok {
		t.Fatalf("removed native key err=%v ok=%v", err, ok)
	}
	if removed.Enabled || removed.SourcePresent || !removed.Hidden {
		t.Fatalf("removed native key should be disabled and hidden: %#v", removed)
	}
	usage, err := store.UsageSummary(ctx, old.ID, WindowFor(Range24H, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if usage.Calls != 1 || usage.TotalCost != 1.25 {
		t.Fatalf("removed key history should remain: %#v", usage)
	}

	if err := store.SyncNativeKeys(ctx, []NativeKeySyncInput{{RawKey: "sk-native-new", Alias: "QQ专用"}}); err != nil {
		t.Fatal(err)
	}
	newKey, ok, err := store.FindKeyByHash(ctx, SHA256Hex("sk-native-new"))
	if err != nil || !ok {
		t.Fatalf("new inherited native key err=%v ok=%v", err, ok)
	}
	if !newKey.Enabled || newKey.RPM != 7 || newKey.InheritedFrom != old.ID || len(newKey.Models) != 1 || newKey.Models[0] != "gpt-5.5" || newKey.FiveHourUSD == nil || *newKey.FiveHourUSD != 2 {
		t.Fatalf("new same-alias key should inherit policy only: %#v", newKey)
	}
	newUsage, err := store.UsageSummary(ctx, newKey.ID, WindowFor(Range24H, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if newUsage.Calls != 0 || newUsage.TotalCost != 0 {
		t.Fatalf("new inherited key must start fresh ledger: %#v", newUsage)
	}
}

func TestSyncNativeKeysDisablesAmbiguousSameAliasInheritance(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "policyplus.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, raw := range []string{"sk-old-a", "sk-old-b"} {
		rec, ok := NativeKeyRecord(raw, "阿伟专用")
		if !ok {
			t.Fatalf("failed to build native record for %s", raw)
		}
		rec.SourcePresent = false
		rec.Hidden = true
		rec.Enabled = false
		if err := store.UpsertKey(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SyncNativeKeys(ctx, []NativeKeySyncInput{{RawKey: "sk-new-c", Alias: "阿伟专用"}}); err != nil {
		t.Fatal(err)
	}
	key, ok, err := store.FindKeyByHash(ctx, SHA256Hex("sk-new-c"))
	if err != nil || !ok {
		t.Fatalf("new key err=%v ok=%v", err, ok)
	}
	if key.Enabled || !key.InheritConflict || key.InheritedFrom != "" {
		t.Fatalf("ambiguous same-alias inheritance should require manual template: %#v", key)
	}
}

func TestSyncNativeKeysUpgradesExistingDefaultEmptyNativePolicy(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "policyplus.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rec, ok := NativeKeyRecord("sk-empty-existing", "")
	if !ok {
		t.Fatal("failed to build native record")
	}
	rec.Enabled = false
	rec.LastEnabled = false
	if err := store.UpsertKey(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `update keys set enabled=0, last_enabled=0 where id=?`, rec.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.SyncNativeKeys(ctx, []NativeKeySyncInput{{RawKey: "sk-empty-existing"}}); err != nil {
		t.Fatal(err)
	}
	upgraded, ok, err := store.FindKeyByHash(ctx, SHA256Hex("sk-empty-existing"))
	if err != nil || !ok {
		t.Fatalf("upgraded key err=%v ok=%v", err, ok)
	}
	if !upgraded.Enabled {
		t.Fatalf("default empty native policy should be upgraded to enabled: %#v", upgraded)
	}

	upgraded.Enabled = false
	upgraded.LastEnabled = false
	upgraded.RPM = 30
	if err := store.SaveKeySettings(ctx, upgraded); err != nil {
		t.Fatal(err)
	}
	if err := store.SyncNativeKeys(ctx, []NativeKeySyncInput{{RawKey: "sk-empty-existing"}}); err != nil {
		t.Fatal(err)
	}
	manual, ok, err := store.FindKeyByHash(ctx, SHA256Hex("sk-empty-existing"))
	if err != nil || !ok {
		t.Fatalf("manual key err=%v ok=%v", err, ok)
	}
	if manual.Enabled {
		t.Fatalf("manual disabled policy with settings should not be auto-enabled: %#v", manual)
	}
}

func TestSyncNativeKeysInheritsFromLegacyPlusPolicyByAlias(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "policyplus.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	legacy := KeyRecord{
		ID:              "legacy-qq",
		Name:            "QQ专用",
		KeyHash:         "sha256:" + SHA256Hex("cpa-old-qq"),
		Enabled:         true,
		Preview:         HashPreview(SHA256Hex("cpa-old-qq")),
		RPM:             9,
		Models:          []string{"gpt-5.5"},
		WeeklyLimitUSD:  ptr(30),
		MonthlyLimitUSD: ptr(100),
	}
	if err := store.UpsertKey(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertUsage(ctx, UsageEvent{RequestID: "legacy-usage", KeyID: legacy.ID, RequestedAt: time.Now(), Cost: 2}); err != nil {
		t.Fatal(err)
	}
	if err := store.SyncNativeKeys(ctx, []NativeKeySyncInput{{RawKey: "sk-official-qq", Alias: "QQ专用"}}); err != nil {
		t.Fatal(err)
	}
	native, ok, err := store.FindKeyByHash(ctx, SHA256Hex("sk-official-qq"))
	if err != nil || !ok {
		t.Fatalf("native key err=%v ok=%v", err, ok)
	}
	if native.InheritedFrom != legacy.ID || !native.Enabled || native.RPM != 9 || native.WeeklyLimitUSD == nil || *native.WeeklyLimitUSD != 30 {
		t.Fatalf("native key should inherit legacy Plus policy: %#v", native)
	}
	keys, err := store.ListKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var retired KeyRecord
	for _, key := range keys {
		if key.ID == legacy.ID {
			retired = key
			break
		}
	}
	if retired.ID == "" || retired.Source != LegacyPlusSource || retired.Enabled || retired.SourcePresent || !retired.Hidden || !retired.LastEnabled {
		t.Fatalf("legacy Plus template should retire after inheritance: %#v", retired)
	}
	legacyUsage, err := store.UsageSummary(ctx, legacy.ID, WindowFor(Range24H, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	nativeUsage, err := store.UsageSummary(ctx, native.ID, WindowFor(Range24H, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if legacyUsage.Calls != 1 || nativeUsage.Calls != 0 {
		t.Fatalf("usage should stay on legacy ledger only: legacy=%#v native=%#v", legacyUsage, nativeUsage)
	}
}

func TestStoreMigratesOldUsageEventsSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policyplus.sqlite")
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

func TestSessionIdentityPriorityAndExpiry(t *testing.T) {
	headers := http.Header{
		"X-Codex-Turn-Metadata": []string{`{"window_id":"turn-window","prompt_cache_key":"turn-cache"}`},
		"X-Session-ID":          []string{"session-header"},
	}
	body := []byte(`{"client_metadata":{"x-codex-window-id":"client-window"},"prompt_cache_key":"body-cache","conversation_id":"conv"}`)
	identity := ExtractSessionIdentity(headers, body)
	if identity.Source != "client_metadata.x-codex-window-id" {
		t.Fatalf("identity priority = %#v", identity)
	}
	headers.Set("X-Codex-Window-Id", "header-window")
	identity = ExtractSessionIdentity(headers, body)
	if identity.Source != "x-codex-window-id" {
		t.Fatalf("header window should win: %#v", identity)
	}
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "policyplus.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Unix(1000, 0)
	first, err := store.RegisterActiveSession(ctx, "alice", newSessionIdentity("test", "a"), 1, DefaultSessionIdle, now)
	if err != nil || !first.Allowed || first.Active != 1 {
		t.Fatalf("first session = %#v err=%v", first, err)
	}
	second, err := store.RegisterActiveSession(ctx, "alice", newSessionIdentity("test", "b"), 1, DefaultSessionIdle, now.Add(31*time.Minute))
	if err != nil || !second.Allowed || second.Active != 1 {
		t.Fatalf("expired slot should be reusable: %#v err=%v", second, err)
	}
	missing, err := store.RegisterActiveSession(ctx, "alice", SessionIdentity{}, 1, DefaultSessionIdle, now)
	if err != nil || !missing.Allowed || !missing.Missing {
		t.Fatalf("missing identity should be allowed: %#v err=%v", missing, err)
	}
	count, err := store.AuditCount(ctx, "missing_session_identity")
	if err != nil || count != 1 {
		t.Fatalf("missing identity audit count=%d err=%v", count, err)
	}
}

func TestStoreRecentCodexSummariesFiltersByKey(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "policyplus.sqlite"))
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

func TestRecentCodexSummariesFromSQLiteReadsExternalExecutorStore(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "executor.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCodexSummary(ctx, "exec-a", "alice-key", "gpt-5.5", "auto_continued", map[string]any{
		"request_id":   "exec-a",
		"key_identity": map[string]any{"known": true, "id": "alice-key"},
		"protection":   "auto_continued",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCodexSummary(ctx, "exec-b", "bob-key", "gpt-5.5", "protected_clean", map[string]any{
		"request_id":   "exec-b",
		"key_identity": map[string]any{"known": true, "id": "bob-key"},
		"protection":   "protected_clean",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	alice, err := RecentCodexSummariesFromSQLite(ctx, path, "alice-key", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(alice) != 1 || alice[0].RequestID != "exec-a" || alice[0].Protection != "auto_continued" {
		t.Fatalf("alice executor summaries = %#v", alice)
	}
	missing, err := RecentCodexSummariesFromSQLite(ctx, filepath.Join(t.TempDir(), "missing.sqlite"), "alice-key", 10)
	if err == nil || missing != nil {
		t.Fatalf("missing executor db should fail soft for caller: items=%#v err=%v", missing, err)
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
		{name: "native", in: "sk-abc", code: "invalid_api_key"},
		{name: "preview", in: "cpa_abcd...efgh", code: "key_preview_not_usable"},
		{name: "unsupported", in: "abc", code: "unsupported_key_format"},
		{name: "short cpa", in: "Bearer cpa_live", code: "legacy_cpa_key_retired"},
		{name: "full cpa", in: "Bearer cpa_abcdefghijklmnopqrstuvwxyz0123456789", code: "legacy_cpa_key_retired"},
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
