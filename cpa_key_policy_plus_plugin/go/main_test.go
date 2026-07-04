package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codexcont/cpa-key-policy-plus-plugin/internal/policyplus"
	_ "modernc.org/sqlite"
)

func setupTestState(t *testing.T) policyplus.KeyRecord {
	t.Helper()
	state.mu.Lock()
	state.cfg = policyplus.DefaultConfig()
	state.cfg.StateDBPath = filepath.Join(t.TempDir(), "policyplus.sqlite")
	state.cfg.SessionSecret = "test-secret"
	state.cfg.ExclusiveAuth = true
	state.keyState = policyplus.KeyPolicyState{}
	state.keyStatePath = ""
	state.rpmBuckets = map[string][]time.Time{}
	old := state.store
	state.store = nil
	state.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	store, err := policyplus.OpenStore(state.cfg.StateDBPath)
	if err != nil {
		t.Fatal(err)
	}
	key := policyplus.KeyRecord{
		ID:                "alice-key",
		Name:              "Alice",
		KeyHash:           "sha256:" + policyplus.SHA256Hex("sk-alice-secret"),
		Enabled:           true,
		Preview:           policyplus.HashPreview(policyplus.SHA256Hex("sk-alice-secret")),
		Source:            policyplus.NativeCPASource,
		SourcePresent:     true,
		RPM:               10,
		Concurrency:       2,
		MaxActiveSessions: 1,
		Models:            []string{"gpt-5.5"},
		DailyLimitUSD:     floatPtr(10),
		WeeklyLimitUSD:    floatPtr(50),
	}
	if err := store.UpsertKey(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	state.store = store
	state.mu.Unlock()
	t.Cleanup(func() {
		state.mu.Lock()
		if state.store != nil {
			_ = state.store.Close()
		}
		state.store = nil
		state.mu.Unlock()
	})
	return key
}

func TestPluginRegistrationIsPolicyPlusExclusiveAuth(t *testing.T) {
	setupTestState(t)
	raw, err := okEnvelope(pluginRegistration())
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var reg registration
	if err := json.Unmarshal(env.Result, &reg); err != nil {
		t.Fatal(err)
	}
	if reg.Metadata.Name != pluginID {
		t.Fatalf("plugin name = %s", reg.Metadata.Name)
	}
	if !reg.Capabilities.FrontendAuthProvider || !reg.Capabilities.FrontendAuthProviderExclusive {
		t.Fatalf("frontend auth capabilities = %#v", reg.Capabilities)
	}
	if !reg.Capabilities.ModelRouter || !reg.Capabilities.Executor {
		t.Fatalf("plus must expose deny-only model route/executor for explicit policy 429s: %#v", reg.Capabilities)
	}
	if reg.Capabilities.ExecutorModelScope != executorModelScopeBoth {
		t.Fatalf("executor model scope = %q", reg.Capabilities.ExecutorModelScope)
	}
	if len(reg.Capabilities.ExecutorInputFormats) != 1 || reg.Capabilities.ExecutorInputFormats[0] != executorFormatOpenAIResponse {
		t.Fatalf("executor input formats = %#v", reg.Capabilities.ExecutorInputFormats)
	}
	if len(reg.Capabilities.ExecutorOutputFormats) != 1 || reg.Capabilities.ExecutorOutputFormats[0] != executorFormatOpenAIResponse {
		t.Fatalf("executor output formats = %#v", reg.Capabilities.ExecutorOutputFormats)
	}
}

func TestFrontendAuthIgnoresRetiredActiveSessionLimit(t *testing.T) {
	setupTestState(t)
	req := frontendAuthRequest{
		Headers: http.Header{"Authorization": []string{"Bearer sk-alice-secret"}},
		Body:    []byte(`{"model":"gpt-5.5","prompt_cache_key":"window-a"}`),
	}
	if !authOK(t, req) {
		t.Fatal("first session should authenticate")
	}
	req.Body = []byte(`{"model":"gpt-5.5","prompt_cache_key":"window-a"}`)
	if !authOK(t, req) {
		t.Fatal("same session should refresh and authenticate")
	}
	req.Body = []byte(`{"model":"gpt-5.5","prompt_cache_key":"window-b"}`)
	if !authOK(t, req) {
		t.Fatal("retired Codex window limit should not reject a second session")
	}
}

func TestFrontendAuthAllowsMissingSessionAndAudits(t *testing.T) {
	setupTestState(t)
	req := frontendAuthRequest{
		Headers: http.Header{"Authorization": []string{"Bearer sk-alice-secret"}},
		Body:    []byte(`{"model":"gpt-5.5"}`),
	}
	if !authOK(t, req) {
		t.Fatal("missing session identity should not be rejected in v1")
	}
}

func TestAdminSaveKeysPersistsUnifiedLimits(t *testing.T) {
	setupTestState(t)
	enabled := false
	body := map[string]any{"keys": []map[string]any{{
		"id":                  "alice-key",
		"name":                "Alice Plus",
		"enabled":             enabled,
		"rpm":                 5,
		"concurrency":         1,
		"max_active_sessions": 3,
		"models":              []string{"gpt-5.4", "custom-unknown"},
		"prices": map[string]any{
			"custom-unknown": map[string]any{"input_per_million": 1.2},
		},
		"five_hour_usd": 1.25,
		"daily_usd":     2.5,
		"weekly_usd":    7.5,
		"monthly_usd":   20.0,
	}}}
	rawBody, _ := json.Marshal(body)
	raw, err := adminSaveKeys(managementRequest{Body: rawBody})
	if err != nil {
		t.Fatal(err)
	}
	bodyBytes := decodeManagementBody(t, raw)
	if strings.Contains(string(bodyBytes), "Alice Plus") || !strings.Contains(string(bodyBytes), "Alice") {
		t.Fatalf("save response should keep CPA/CPAMP readonly alias: %s", bodyBytes)
	}
	store := loadedStore()
	keys, err := store.ListKeys(context.Background())
	if err != nil || len(keys) != 1 {
		t.Fatalf("ListKeys err=%v keys=%#v", err, keys)
	}
	got := keys[0]
	if got.Enabled || got.RPM != 5 || got.Concurrency != 0 || got.MaxActiveSessions != 0 {
		t.Fatalf("updated key = %#v", got)
	}
	if got.Name != "Alice" {
		t.Fatalf("Plus save must not rename native key alias, got %q", got.Name)
	}
	if got.FiveHourUSD == nil || *got.FiveHourUSD != 1.25 || got.MonthlyLimitUSD == nil || *got.MonthlyLimitUSD != 20 {
		t.Fatalf("limits not persisted: %#v", got)
	}
	if len(got.Models) != 2 || got.Models[1] != "custom-unknown" {
		t.Fatalf("models should preserve unknown selected model: %#v", got.Models)
	}
	if price, ok := got.Prices["custom-unknown"]; !ok || price.Model != "custom-unknown" || price.InputPerMillion != 1.2 {
		t.Fatalf("custom price not normalized: %#v", got.Prices)
	}
}

func TestAdminCreateKeyRetiredToCPAMP(t *testing.T) {
	setupTestState(t)
	raw, err := adminCreateKey(managementRequest{Body: []byte(`{"name":"Bob","enabled":false,"rpm":9,"concurrency":4,"max_active_sessions":3,"models":["gpt-5.4-mini"]}`)})
	if err != nil {
		t.Fatal(err)
	}
	resp := decodeManagementResponse(t, raw)
	bodyBytes := resp.Body
	keys, err := loadedStore().ListKeys(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusGone || !strings.Contains(string(bodyBytes), "native_key_lifecycle_owned_by_cpa") {
		t.Fatalf("create should be retired to CPA/CPAMP: status=%d body=%s", resp.StatusCode, bodyBytes)
	}
	if strings.Contains(string(bodyBytes), "raw_key") || len(keys) != 1 {
		t.Fatalf("retired create must not return raw keys or persist Bob: keys=%#v body=%s", keys, bodyBytes)
	}
}

func TestManagementAliasCreateSaveAndReset(t *testing.T) {
	setupTestState(t)
	createRaw, _ := json.Marshal(map[string]any{"name": "Alias", "models": []string{"gpt-5.4"}})
	raw, err := managementHandle(mustJSON(t, managementRequest{
		Method: http.MethodPost,
		Path:   "/key-policy-plus/api/keys/create",
		Body:   createRaw,
	}))
	if err != nil {
		t.Fatal(err)
	}
	createResp := decodeManagementResponse(t, raw)
	createBody := createResp.Body
	if createResp.StatusCode != http.StatusGone || !strings.Contains(string(createBody), "native_key_lifecycle_owned_by_cpa") || strings.Contains(string(createBody), `"raw_key"`) {
		t.Fatalf("create alias should be retired: status=%d body=%s", createResp.StatusCode, createBody)
	}
	saveRaw, _ := json.Marshal(map[string]any{"keys": []map[string]any{{
		"id":                  "alice-key",
		"name":                "Alias Saved",
		"enabled":             true,
		"rpm":                 11,
		"concurrency":         2,
		"max_active_sessions": 1,
		"models":              []string{"gpt-5.5"},
	}}})
	raw, err = managementHandle(mustJSON(t, managementRequest{
		Method: http.MethodPut,
		Path:   "/key-policy-plus/api/keys/save",
		Body:   saveRaw,
	}))
	if err != nil {
		t.Fatal(err)
	}
	saveBody := decodeManagementBody(t, raw)
	if strings.Contains(string(saveBody), "Alias Saved") || !strings.Contains(string(saveBody), "Alice") {
		t.Fatalf("save via alias should update strategy but not readonly alias: %s", saveBody)
	}
	keys, err := loadedStore().ListKeys(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		if key.ID == "alice-key" && (key.Concurrency != 0 || key.MaxActiveSessions != 0) {
			t.Fatalf("alias save should force retired limits to zero: %#v", key)
		}
	}
	resetRaw, _ := json.Marshal(map[string]any{"id": "alice-key", "window": "5h"})
	raw, err = managementHandle(mustJSON(t, managementRequest{
		Method: http.MethodPost,
		Path:   "/key-policy-plus/api/keys/reset",
		Body:   resetRaw,
	}))
	if err != nil {
		t.Fatal(err)
	}
	resetBody := decodeManagementBody(t, raw)
	if !strings.Contains(string(resetBody), `"ok":true`) {
		t.Fatalf("reset via alias failed: %s", resetBody)
	}
}

func TestArchiveRouteReturnsGone(t *testing.T) {
	key := setupTestState(t)
	archiveRaw, _ := json.Marshal(map[string]any{"id": key.ID, "archived": true})
	raw, err := managementHandle(mustJSON(t, managementRequest{
		Method: http.MethodPost,
		Path:   "/key-policy-plus/api/keys/archive",
		Body:   archiveRaw,
	}))
	if err != nil {
		t.Fatal(err)
	}
	body := decodeManagementBody(t, raw)
	if !strings.Contains(string(body), `"native_key_lifecycle_owned_by_cpa"`) {
		t.Fatalf("archive route should return CPA/CPAMP lifecycle guidance: %s", body)
	}
}

func TestDeleteKeyRetiredAndDoesNotDisableNativePolicy(t *testing.T) {
	key := setupTestState(t)
	deleteRaw, _ := json.Marshal(map[string]any{"id": key.ID, "confirm": "delete"})
	raw, err := managementHandle(mustJSON(t, managementRequest{
		Method: http.MethodPost,
		Path:   "/key-policy-plus/api/keys/delete",
		Body:   deleteRaw,
	}))
	if err != nil {
		t.Fatal(err)
	}
	resp := decodeManagementResponse(t, raw)
	body := resp.Body
	if resp.StatusCode != http.StatusGone || !strings.Contains(string(body), `"native_key_lifecycle_owned_by_cpa"`) {
		t.Fatalf("delete should be retired to CPA/CPAMP: status=%d body=%s", resp.StatusCode, body)
	}
	if !authOK(t, frontendAuthRequest{
		Headers: http.Header{"Authorization": []string{"Bearer sk-alice-secret"}},
		Body:    []byte(`{"model":"gpt-5.5","prompt_cache_key":"window-a"}`),
	}) {
		t.Fatal("retired Plus delete should not disable native policy")
	}
	raw, err = userSession(managementRequest{Headers: http.Header{"X-CPA-Key-Policy-Plus-Key": []string{"sk-alice-secret"}}})
	if err != nil {
		t.Fatal(err)
	}
	body = decodeManagementBody(t, raw)
	if !strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("retired Plus delete should not break user session: %s", body)
	}
}

func TestAdminKeysMirrorsCurrentNativeCPAKeys(t *testing.T) {
	setupTestState(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	aliasPath := filepath.Join(dir, "cpamp.sqlite")
	writeNativeConfig := func(keys ...string) {
		t.Helper()
		body := "api-keys:\n"
		for _, key := range keys {
			body += "  - " + key + "\n"
		}
		if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	db, err := sql.Open("sqlite", aliasPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`create table api_key_aliases(api_key_hash text primary key, alias text, updated_at_ms integer)`); err != nil {
		t.Fatal(err)
	}
	upsertAlias := func(rawKey, alias string) {
		t.Helper()
		if _, err := db.Exec(`insert into api_key_aliases(api_key_hash, alias, updated_at_ms) values(?, ?, ?)
			on conflict(api_key_hash) do update set alias=excluded.alias, updated_at_ms=excluded.updated_at_ms`,
			"sha256:"+policyplus.SHA256Hex(rawKey), alias, time.Now().UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	upsertAlias("sk-native-qq", "QQ专用")
	upsertAlias("sk-native-wei", "阿伟专用")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	writeNativeConfig("sk-native-qq", "sk-native-wei")

	legacy := policyplus.KeyRecord{
		ID:            "cpa_legacy_policy",
		Name:          "旧 CPI 下划线 Key",
		KeyHash:       "sha256:" + policyplus.SHA256Hex("cpa_legacy_policy"),
		Enabled:       true,
		Preview:       policyplus.HashPreview(policyplus.SHA256Hex("cpa_legacy_policy")),
		Source:        policyplus.LegacyPlusSource,
		SourcePresent: false,
		Hidden:        true,
	}
	if err := loadedStore().UpsertKey(context.Background(), legacy); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	state.cfg.NativeKeysConfigPath = configPath
	state.cfg.CPAMPAliasDBPath = aliasPath
	state.mu.Unlock()

	raw, err := adminKeys(managementRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var first struct {
		OK   bool                       `json:"ok"`
		Keys []map[string]any           `json:"keys"`
		Meta map[string]json.RawMessage `json:"codexcont"`
	}
	if err := json.Unmarshal(decodeManagementBody(t, raw), &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Keys) != 2 {
		t.Fatalf("default admin list should mirror only current native keys: %#v", first.Keys)
	}
	names := map[string]bool{}
	for _, key := range first.Keys {
		names[fmt.Sprint(key["name"])] = true
		if key["source"] != policyplus.NativeCPASource || key["source_present"] != true || key["hidden"] == true {
			t.Fatalf("default row should be active native key only: %#v", key)
		}
	}
	if !names["QQ专用"] || !names["阿伟专用"] || names["旧 CPI 下划线 Key"] {
		t.Fatalf("unexpected admin key names: %#v rows=%#v", names, first.Keys)
	}
	for _, key := range first.Keys {
		if key["enabled"] != true {
			t.Fatalf("new official native keys should be enabled by default: %#v", key)
		}
	}

	db, err = sql.Open("sqlite", aliasPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`update api_key_aliases set alias=? where api_key_hash=?`, "QQ官Key改名", "sha256:"+policyplus.SHA256Hex("sk-native-qq")); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	writeNativeConfig("sk-native-qq")
	raw, err = adminKeys(managementRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var second struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(decodeManagementBody(t, raw), &second); err != nil {
		t.Fatal(err)
	}
	if len(second.Keys) != 1 || second.Keys[0]["name"] != "QQ官Key改名" {
		t.Fatalf("admin list should refresh alias changes and official deletions: %#v", second.Keys)
	}

	raw, err = adminKeys(managementRequest{Query: map[string][]string{"include_removed": {"1"}}})
	if err != nil {
		t.Fatal(err)
	}
	var withRemoved struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(decodeManagementBody(t, raw), &withRemoved); err != nil {
		t.Fatal(err)
	}
	if len(withRemoved.Keys) < 2 {
		t.Fatalf("include_removed should expose removed native diagnostics: %#v", withRemoved.Keys)
	}
	seenRemovedQQ := false
	for _, key := range withRemoved.Keys {
		if key["source"] != policyplus.NativeCPASource || key["name"] == "旧 CPI 下划线 Key" {
			t.Fatalf("include_removed leaked non-native row: %#v", key)
		}
		if key["name"] == "阿伟专用" && key["source_present"] == false {
			seenRemovedQQ = true
		}
	}
	if !seenRemovedQQ {
		t.Fatalf("include_removed did not expose the official key removed by sync: %#v", withRemoved.Keys)
	}
}

func TestAdminKeysUsesAliasFallbackPath(t *testing.T) {
	setupTestState(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	emptyAliasPath := filepath.Join(dir, "empty-cpamp.sqlite")
	realAliasPath := filepath.Join(dir, "real-cpamp.sqlite")
	if err := os.WriteFile(configPath, []byte("api-keys:\n  - sk-alice\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	emptyDB, err := sql.Open("sqlite", emptyAliasPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := emptyDB.Exec(`create table unrelated(id text)`); err != nil {
		t.Fatal(err)
	}
	if err := emptyDB.Close(); err != nil {
		t.Fatal(err)
	}
	realDB, err := sql.Open("sqlite", realAliasPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := realDB.Exec(`create table api_key_aliases(api_key_hash text primary key, alias text, updated_at_ms integer)`); err != nil {
		t.Fatal(err)
	}
	if _, err := realDB.Exec(`insert into api_key_aliases(api_key_hash, alias, updated_at_ms) values(?, ?, ?)`, "sha256:"+policyplus.SHA256Hex("sk-alice"), "alicea", time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err := realDB.Close(); err != nil {
		t.Fatal(err)
	}

	state.mu.Lock()
	state.cfg.NativeKeysConfigPath = configPath
	state.cfg.CPAMPAliasDBPath = emptyAliasPath
	state.cfg.CPAMPAliasDBPaths = realAliasPath
	state.mu.Unlock()

	raw, err := adminKeys(managementRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(decodeManagementBody(t, raw), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Keys) != 1 || resp.Keys[0]["name"] != "alicea" || resp.Keys[0]["alias"] != "alicea" {
		t.Fatalf("admin list should use fallback CPAMP alias source: %#v", resp.Keys)
	}
	if preview := fmt.Sprint(resp.Keys[0]["preview"]); preview == "" || strings.Contains(preview, "sk-alice") {
		t.Fatalf("preview should stay safe and derived from the same native key: %#v", resp.Keys[0])
	}
}

func TestPolicyDecisionDenialsUseExplicitCodes(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *policyplus.KeyRecord)
		model  string
		code   string
		param  string
	}{
		{
			name: "disabled",
			mutate: func(t *testing.T, key *policyplus.KeyRecord) {
				key.Enabled = false
			},
			model: "gpt-5.5",
			code:  "api_key_disabled",
			param: "disabled",
		},
		{
			name: "source removed",
			mutate: func(t *testing.T, key *policyplus.KeyRecord) {
				key.SourcePresent = false
			},
			model: "gpt-5.5",
			code:  "api_key_source_removed",
			param: "source_removed",
		},
		{
			name:  "model not allowed",
			model: "gpt-5.4",
			code:  "model_not_allowed",
			param: "model",
		},
		{
			name: "rpm",
			mutate: func(t *testing.T, key *policyplus.KeyRecord) {
				key.RPM = 1
			},
			model: "gpt-5.5",
			code:  "rpm_rate_limit_exceeded",
			param: "rpm",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			key := setupTestState(t)
			if tc.mutate != nil {
				tc.mutate(t, &key)
			}
			if key.SourcePresent {
				if err := loadedStore().SaveKeySettings(context.Background(), key); err != nil {
					t.Fatal(err)
				}
			}
			if tc.name == "source removed" {
				if err := loadedStore().SyncNativeKeys(context.Background(), nil); err != nil {
					t.Fatal(err)
				}
			}
			if tc.name == "rpm" {
				first := evaluatePolicy(key, tc.model, true)
				if !first.Allowed {
					t.Fatalf("first RPM request should pass: %#v", first)
				}
			}
			decision := evaluatePolicy(key, tc.model, tc.name == "rpm")
			if decision.Allowed || decision.StatusCode != http.StatusTooManyRequests || decision.Code != tc.code || decision.Param != tc.param || !strings.Contains(decision.Message, "CPA Key Policy+") {
				t.Fatalf("decision = %#v", decision)
			}
		})
	}
}

func TestPolicyDecisionQuotaWindowsUsePostAccountingOrder(t *testing.T) {
	windows := []struct {
		name  string
		set   func(*policyplus.KeyRecord)
		code  string
		param string
	}{
		{policyplus.Range5H, func(k *policyplus.KeyRecord) { k.FiveHourUSD = floatPtr(1) }, "five_hour_quota_exceeded", policyplus.Range5H},
		{policyplus.Range24H, func(k *policyplus.KeyRecord) { k.DailyLimitUSD = floatPtr(1) }, "daily_quota_exceeded", policyplus.Range24H},
		{policyplus.Range7D, func(k *policyplus.KeyRecord) { k.WeeklyLimitUSD = floatPtr(1) }, "weekly_quota_exceeded", policyplus.Range7D},
		{policyplus.RangeMonth, func(k *policyplus.KeyRecord) { k.MonthlyLimitUSD = floatPtr(1) }, "monthly_quota_exceeded", policyplus.RangeMonth},
	}
	for _, item := range windows {
		t.Run(item.name, func(t *testing.T) {
			key := setupTestState(t)
			key.FiveHourUSD = nil
			key.DailyLimitUSD = nil
			key.WeeklyLimitUSD = nil
			key.MonthlyLimitUSD = nil
			item.set(&key)
			if err := loadedStore().SaveKeySettings(context.Background(), key); err != nil {
				t.Fatal(err)
			}
			if err := loadedStore().InsertUsage(context.Background(), policyplus.UsageEvent{
				RequestID:   "quota-" + item.name,
				KeyID:       key.ID,
				RequestedAt: time.Now(),
				Cost:        1,
			}); err != nil {
				t.Fatal(err)
			}
			decision := evaluatePolicy(key, "gpt-5.5", false)
			if decision.Allowed || decision.Code != item.code || decision.Param != item.param || decision.Window != item.name || decision.UsedUSD != 1 || decision.LimitUSD != 1 {
				t.Fatalf("quota decision = %#v", decision)
			}
			if !strings.Contains(decision.Message, "已用 $1.00 / 上限 $1.00") {
				t.Fatalf("quota message should expose used/limit: %s", decision.Message)
			}
		})
	}
}

func TestRouteAndExecutorReturnPolicyDenied429(t *testing.T) {
	key := setupTestState(t)
	key.Enabled = false
	if err := loadedStore().SaveKeySettings(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	routeRaw, err := routeModel(mustJSON(t, modelRouteRequest{
		RequestedModel: "gpt-5.5",
		Headers:        http.Header{"Authorization": []string{"Bearer sk-alice-secret"}},
		Body:           []byte(`{"model":"gpt-5.5"}`),
	}))
	if err != nil {
		t.Fatal(err)
	}
	var routeEnv envelope
	if err := json.Unmarshal(routeRaw, &routeEnv); err != nil {
		t.Fatal(err)
	}
	var routeResp modelRouteResponse
	if err := json.Unmarshal(routeEnv.Result, &routeResp); err != nil {
		t.Fatal(err)
	}
	if !routeResp.Handled || routeResp.TargetKind != routeTargetSelf || routeResp.Reason != "cpa_key_policy_plus_policy_denied" {
		t.Fatalf("route response = %#v", routeResp)
	}

	execRaw, err := executorExecute(mustJSON(t, executorCallRequest{
		executorRequest: executorRequest{
			Model:   "gpt-5.5",
			Headers: http.Header{"Authorization": []string{"Bearer sk-alice-secret"}},
			Payload: []byte(`{"model":"gpt-5.5"}`),
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	var execEnv envelope
	if err := json.Unmarshal(execRaw, &execEnv); err != nil {
		t.Fatal(err)
	}
	var execResp executorResponse
	if err := json.Unmarshal(execEnv.Result, &execResp); err != nil {
		t.Fatal(err)
	}
	if execResp.Headers.Get("X-CPA-Policy-Reason") != "api_key_disabled" || execResp.Headers.Get("Retry-After") == "" {
		t.Fatalf("executor deny response = %#v", execResp)
	}
	if execResp.Headers.Get("X-CPA-Policy-Window") != "disabled" {
		t.Fatalf("executor deny headers should include safe policy window/param: %#v", execResp.Headers)
	}
	var body struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
			Param   string `json:"param"`
		} `json:"error"`
	}
	if err := json.Unmarshal(execResp.Payload, &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "api_key_disabled" || body.Error.Message == "" || body.Error.Param != "disabled" {
		t.Fatalf("deny body = %s", execResp.Payload)
	}
}

func TestFrontendAuthOnlySurfacesDeniedNativeKeysForModelRequests(t *testing.T) {
	key := setupTestState(t)
	key.Enabled = false
	if err := loadedStore().SaveKeySettings(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	raw, err := frontendAuth(mustJSON(t, frontendAuthRequest{
		Path:    "/v1/models",
		Headers: http.Header{"Authorization": []string{"Bearer sk-alice-secret"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var resp frontendAuthResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Authenticated {
		t.Fatalf("non-model denied request should fail closed through normal auth path: %#v", resp)
	}

	raw, err = frontendAuth(mustJSON(t, frontendAuthRequest{
		Path:    "/v1/responses",
		Headers: http.Header{"Authorization": []string{"Bearer sk-alice-secret"}},
		Body:    []byte(`{"model":"gpt-5.5"}`),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Authenticated || resp.Metadata[policyDenyMetadataPrefix+"code"] != "api_key_disabled" {
		t.Fatalf("responses denied request should route to explicit policy body: %#v", resp)
	}
}

func TestExecutorRequestNormalizationKeepsNestedPayload(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","stream":true}`)
	req := normalizedExecutorRequest(executorCallRequest{
		NestedExecutorRequest: executorRequest{
			Model:   "gpt-5.5",
			Payload: body,
		},
	})
	if req.Model != "gpt-5.5" || string(req.Payload) != string(body) {
		t.Fatalf("normalized nested request = %#v", req)
	}
}

func TestAdminKeysIncludesQuotaProjection(t *testing.T) {
	setupTestState(t)
	store := loadedStore()
	if err := store.InsertUsage(context.Background(), policyplus.UsageEvent{
		RequestID:   "usage-a",
		KeyID:       "alice-key",
		RequestedAt: time.Now().Add(-time.Hour),
		Cost:        2.5,
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := adminKeys(managementRequest{})
	if err != nil {
		t.Fatal(err)
	}
	body := decodeManagementBody(t, raw)
	text := string(body)
	for _, want := range []string{`"usage"`, `"quota"`, `"24h"`, `"remaining_usd"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("admin key projection missing %s: %s", want, body)
		}
	}
}

func TestUsageHandleMapsExecutorRecordsByAuthID(t *testing.T) {
	key := setupTestState(t)
	key.Models = []string{"gpt-5.4"}
	key.Prices = map[string]policyplus.ModelPrice{
		"gpt-5.4": {Model: "gpt-5.4", InputPerMillion: 10, OutputPerMillion: 20},
	}
	if err := loadedStore().SaveKeySettings(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(usageRecord{
		Provider:     "codex-account-3.json",
		ExecutorType: "codex",
		Model:        "gpt-5.3-codex-spark",
		AuthID:       key.ID,
		Source:       "codex-account-3.json",
		RequestedAt:  time.Now(),
		Detail: usageDetail{
			InputTokens:     10,
			OutputTokens:    5,
			ReasoningTokens: 2,
			TotalTokens:     15,
		},
	})
	raw, err := usageHandle(body)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if !env.OK {
		t.Fatalf("usage response = %s", raw)
	}
	events, err := loadedStore().RecentEvents(context.Background(), key.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %#v", events)
	}
	got := events[0]
	if got.KeyID != key.ID || got.Model != "gpt-5.4" || got.RequestedModel != "gpt-5.4" || got.ActualModel != "gpt-5.3-codex-spark" {
		t.Fatalf("usage event did not preserve auth/alias mapping: %#v", got)
	}
	if got.Cost <= 0 {
		t.Fatalf("usage event should use visible model price book, got cost=%f event=%#v", got.Cost, got)
	}
}

func TestVisibleUsageModelDoesNotGuessForMultiModelKeys(t *testing.T) {
	key := policyplus.KeyRecord{ID: "key-1", Models: []string{"gpt-5.4", "gpt-5.5"}}
	got := visibleUsageModel(key, usageRecord{Model: "provider-internal-unknown"})
	if got != "provider-internal-unknown" {
		t.Fatalf("multi-model key should not guess visible alias, got %q", got)
	}
	got = visibleUsageModel(key, usageRecord{Model: "gpt-5.3-codex-spark"})
	if got != "gpt-5.4" {
		t.Fatalf("known executor alias should be projected to visible model, got %q", got)
	}
	got = visibleUsageModel(key, usageRecord{Model: "gpt-5.3-codex-spark", Alias: "gpt-5.3-codex-spark"})
	if got != "gpt-5.4" {
		t.Fatalf("internal alias field should be projected to visible model, got %q", got)
	}
	got = visibleUsageModel(key, usageRecord{Model: "gpt-5.3-codex-spark", Alias: "gpt-5.4"})
	if got != "gpt-5.4" {
		t.Fatalf("explicit alias should win, got %q", got)
	}
}

func TestAdminModelsFallsBackToConfiguredModels(t *testing.T) {
	key := setupTestState(t)
	key.Models = []string{"gpt-5.5", "legacy-custom"}
	key.Prices = map[string]policyplus.ModelPrice{
		"price-only": {Model: "price-only", InputPerMillion: 1},
	}
	if err := loadedStore().SaveKeySettings(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	raw, err := adminModels(managementRequest{})
	if err != nil {
		t.Fatal(err)
	}
	body := decodeManagementBody(t, raw)
	text := string(body)
	for _, want := range []string{"gpt-5.5", "legacy-custom", "price-only"} {
		if !strings.Contains(text, want) {
			t.Fatalf("model catalog missing %s: %s", want, text)
		}
	}
}

func TestUserHTMLUsesPolicyPlusHeader(t *testing.T) {
	html := userHTML()
	if !strings.Contains(html, "X-CPA-Key-Policy-Plus-Key") {
		t.Fatal("user page must send the Plus login header")
	}
	if strings.Contains(html, "X-CPA-Governor-Key") {
		t.Fatal("user page should not keep the Governor login header")
	}
	for _, want := range []string{"完整原生 sk- Key", "旧的 cpa_ Key 已迁移下线", "只接受 CPA 原生 sk- Key"} {
		if !strings.Contains(html, want) {
			t.Fatalf("user page missing native-key guidance %q", want)
		}
	}
}

func TestUserHTMLFixedRangeUX(t *testing.T) {
	html := userHTML()
	for _, removed := range []string{`id="rangeSelect"`, "rangeSelect", "state.range"} {
		if strings.Contains(html, removed) {
			t.Fatalf("user page should not keep mutable range control %q", removed)
		}
	}
	for _, want := range []string{
		`const PRIMARY_RANGE = "24h";`,
		"${rangeLabel(PRIMARY_RANGE)}费用",
		"${rangeLabel(PRIMARY_RANGE)} · 最近",
		"24H / 7D",
		"5H / 本月",
		"api(`/usage?range=${encodeURIComponent(PRIMARY_RANGE)}`",
		"api(`/events?range=${encodeURIComponent(PRIMARY_RANGE)}&limit=100`",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("user fixed-range page missing %q", want)
		}
	}
}

func TestUserHTMLIgnoresRefreshCancelNoise(t *testing.T) {
	html := userHTML()
	for _, want := range []string{
		"function isRefreshCancel",
		`abort("refresh_cancelled")`,
		`controller.abort("refresh_timeout")`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("user refresh cancel handling missing %q", want)
		}
	}
	usageCancel := strings.Index(html, "if (isRefreshCancel(controller.signal)) return;")
	usageError := strings.Index(html, `state.errors.usage = "用量接口同步失败："`)
	if usageCancel < 0 || usageError < 0 || usageCancel > usageError {
		t.Fatal("usage refresh cancel must be ignored before setting a sync error")
	}
	protectionError := strings.Index(html, `state.errors.protection = "思维链保护同步失败："`)
	if protectionError < 0 {
		t.Fatal("protection sync error assignment missing")
	}
	protectionCancel := strings.LastIndex(html[:protectionError], "if (isRefreshCancel(controller.signal)) return;")
	if protectionCancel < 0 {
		t.Fatal("protection refresh cancel must be ignored before setting a sync error")
	}
}

func TestCodexRequestsPreferExecutorSummaryBridge(t *testing.T) {
	key := setupTestState(t)
	execPath := filepath.Join(t.TempDir(), "executor.sqlite")
	execStore, err := policyplus.OpenStore(execPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := execStore.SaveCodexSummary(context.Background(), "exec-a", key.ID, "gpt-5.5", "auto_continued", map[string]any{
		"request_id":         "exec-a",
		"model":              "gpt-5.5",
		"protection":         "auto_continued",
		"key_identity":       map[string]any{"known": true, "id": key.ID, "preview": key.Preview},
		"continuation_count": 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := execStore.SaveCodexSummary(context.Background(), "exec-b", "bob-key", "gpt-5.5", "protected_clean", map[string]any{
		"request_id":   "exec-b",
		"model":        "gpt-5.5",
		"protection":   "protected_clean",
		"key_identity": map[string]any{"known": true, "id": "bob-key"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := execStore.Close(); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	state.cfg.CodexSummaryDBPath = execPath
	state.cfg.CodexContEnabled = false
	state.mu.Unlock()
	requests, source := codexRequestsForKey(key, 10)
	if source != "codexcont_executor_store" || len(requests) != 1 || requests[0]["request_id"] != "exec-a" {
		t.Fatalf("source=%s requests=%#v", source, requests)
	}
}

func TestAdminHTMLHasRenderedSharedCSS(t *testing.T) {
	html := adminHTML()
	if strings.Contains(html, "{{CSS}}") || strings.Contains(html, "{{SHARED_CSS}}") {
		t.Fatalf("admin html still has css placeholder")
	}
	if !strings.Contains(html, "--panel") || !strings.Contains(html, "CPA Key Policy+") {
		t.Fatal("admin html should embed shared style and key policy UI")
	}
	if strings.Contains(html, "/v0/resource/plugins/cpa-key-policy-plus/admin/api") {
		t.Fatal("admin html must not send mutating requests through GET-only resource routes")
	}
	if !strings.Contains(html, "/key-policy-plus/api") || !strings.Contains(html, "编辑模型/价格") {
		t.Fatal("admin html should use the management alias and structured model editor")
	}
	for _, removed := range []string{"请求并发", "Codex窗口", "显示归档", "归档隐藏", "恢复 Key"} {
		if strings.Contains(html, removed) {
			t.Fatalf("admin html should not contain retired control %q", removed)
		}
	}
	for _, removed := range []string{"新建 Key", "删除 Key", "复制完整 Key", "raw-key", "/keys/create", "/keys/delete"} {
		if strings.Contains(html, removed) {
			t.Fatalf("admin html should not expose Plus-side key lifecycle control %q", removed)
		}
	}
	for _, want := range []string{"Key 策略", "保存策略", "当前官方 Key", "新原生 Key 默认启用", "未配置限额"} {
		if !strings.Contains(html, want) {
			t.Fatalf("admin html missing native policy UI marker %q", want)
		}
	}
	if strings.Contains(html, "新原生 Key 默认禁用") || strings.Contains(html, "策略总数") || strings.Contains(html, "显示官方已移除") || strings.Contains(html, "include_removed") {
		t.Fatal("admin html should not advertise old disabled/default, historical count, or removed-row controls")
	}
}

func TestSessionInvalidatedWhenKeyHashChanges(t *testing.T) {
	key := setupTestState(t)
	req := managementRequest{Headers: http.Header{"X-CPA-Key-Policy-Plus-Key": []string{"sk-alice-secret"}}}
	raw, err := userSession(req)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var resp managementResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	cookie := strings.Join(resp.Headers.Values("Set-Cookie"), "; ")
	if cookie == "" {
		t.Fatal("session did not set cookie")
	}
	if _, ok := keyFromSession(managementRequest{Headers: http.Header{"Cookie": []string{cookie}}}); !ok {
		t.Fatal("fresh session should resolve")
	}
	key.KeyHash = "sha256:" + policyplus.SHA256Hex("sk-rotated-secret")
	if err := loadedStore().UpsertKey(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if _, ok := keyFromSession(managementRequest{Headers: http.Header{"Cookie": []string{cookie}}}); ok {
		t.Fatal("old session should not survive key rotation")
	}
}

func TestSessionCookiePathCompatibility(t *testing.T) {
	setupTestState(t)
	raw, err := userSession(managementRequest{Headers: http.Header{"X-CPA-Key-Policy-Plus-Key": []string{"sk-alice-secret"}}})
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var resp managementResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	cookies := resp.Headers.Values("Set-Cookie")
	for _, want := range []string{
		"Path=/;",
		"Path=/v0/resource/plugins/cpa-key-policy-plus/user;",
		"Path=/key-policy-plus-user;",
	} {
		if !strings.Contains(strings.Join(cookies, "\n"), want) {
			t.Fatalf("session response should set compatible cookie path %s: %#v", want, cookies)
		}
	}
	valid := ""
	for _, rawCookie := range cookies {
		if strings.Contains(rawCookie, "Path=/;") {
			valid = strings.SplitN(rawCookie, ";", 2)[0]
			break
		}
	}
	if valid == "" {
		t.Fatalf("root session cookie not found: %#v", cookies)
	}
	staleFirst := "cpa_key_policy_plus_session=stale-invalid-token; " + valid
	if _, ok := keyFromSession(managementRequest{Headers: http.Header{"Cookie": []string{staleFirst}}}); !ok {
		t.Fatal("fresh session should resolve even when a stale path-specific cookie is sent first")
	}
}

func authOK(t *testing.T, req frontendAuthRequest) bool {
	t.Helper()
	rawReq, _ := json.Marshal(req)
	raw, err := frontendAuth(rawReq)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var resp frontendAuthResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Authenticated
}

func decodeManagementBody(t *testing.T, raw []byte) []byte {
	t.Helper()
	return decodeManagementResponse(t, raw).Body
}

func decodeManagementResponse(t *testing.T, raw []byte) managementResponse {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var resp managementResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
