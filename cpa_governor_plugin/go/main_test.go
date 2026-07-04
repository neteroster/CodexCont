package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codexcont/cpa-governor-plugin/internal/governor"
)

func configureTestState(t *testing.T) governor.KeyRecord {
	t.Helper()
	store, err := governor.OpenStore(filepath.Join(t.TempDir(), "governor.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	key := governor.KeyRecord{
		ID:          "alice-key",
		Name:        "Alice",
		KeyHash:     "sha256:" + governor.SHA256Hex("cpa_live"),
		Enabled:     true,
		Preview:     governor.HashPreview(governor.SHA256Hex("cpa_live")),
		RPM:         60,
		Concurrency: 2,
		Models:      []string{"gpt-5.5"},
		Prices: map[string]governor.ModelPrice{
			"gpt-5.5": {
				Model:               "gpt-5.5",
				InputPerMillion:     5,
				OutputPerMillion:    30,
				CacheReadPerMillion: 0.5,
			},
		},
	}
	if err := store.UpsertKey(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	state.cfg = governor.DefaultConfig()
	state.cfg.SessionSecret = "secret"
	state.store = store
	state.keyState = governor.KeyPolicyState{}
	state.rpmBuckets = map[string][]time.Time{}
	state.mu.Unlock()
	t.Cleanup(func() {
		state.mu.Lock()
		if state.store != nil {
			_ = state.store.Close()
			state.store = nil
		}
		state.mu.Unlock()
	})
	return key
}

func unwrapEnvelope(t *testing.T, raw []byte, out any) {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if !env.OK {
		t.Fatalf("envelope error: %#v", env.Error)
	}
	if err := json.Unmarshal(env.Result, out); err != nil {
		t.Fatal(err)
	}
}

func unwrapManagementResponse(t *testing.T, raw []byte) managementResponse {
	t.Helper()
	var resp managementResponse
	unwrapEnvelope(t, raw, &resp)
	return resp
}

func TestPluginRegistrationUsesLocalABI(t *testing.T) {
	raw, err := handleMethod(methodPluginRegister, nil)
	if err != nil {
		t.Fatal(err)
	}
	var reg registration
	unwrapEnvelope(t, raw, &reg)
	if reg.SchemaVersion != schemaVersion || !reg.Capabilities.ManagementAPI || !reg.Capabilities.UsagePlugin {
		t.Fatalf("registration = %#v", reg)
	}
	if len(reg.Metadata.ConfigFields) == 0 {
		t.Fatal("config fields should be exposed")
	}

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(env.Result, &payload); err != nil {
		t.Fatal(err)
	}
	caps, _ := payload["capabilities"].(map[string]any)
	for _, key := range []string{"frontend_auth_provider", "model_router", "executor", "usage_plugin", "management_api"} {
		if caps[key] != true {
			t.Fatalf("capability %s missing or false in RPC registration: %s", key, string(env.Result))
		}
	}
	if _, ok := caps["FrontendAuthProvider"]; ok {
		t.Fatalf("unexpected Go-style capability field in RPC registration: %s", string(env.Result))
	}
}

func TestManagementRegisterUsesCPARPCSchema(t *testing.T) {
	raw, err := managementRegister()
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if !env.OK {
		t.Fatalf("envelope error: %#v", env.Error)
	}
	var payload map[string]any
	if err := json.Unmarshal(env.Result, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["routes"]; !ok {
		t.Fatalf("missing lowercase routes field: %s", string(env.Result))
	}
	if _, ok := payload["resources"]; !ok {
		t.Fatalf("missing lowercase resources field: %s", string(env.Result))
	}
	if _, ok := payload["Routes"]; ok {
		t.Fatalf("unexpected uppercase Routes field: %s", string(env.Result))
	}
	if _, ok := payload["Resources"]; ok {
		t.Fatalf("unexpected uppercase Resources field: %s", string(env.Result))
	}
	resources, ok := payload["resources"].([]any)
	if !ok {
		t.Fatalf("resources should be an array: %#v", payload["resources"])
	}
	userResourceSeen := false
	for _, item := range resources {
		resource, _ := item.(map[string]any)
		if resource["Path"] != "/user" {
			continue
		}
		userResourceSeen = true
		if menu, _ := resource["Menu"].(string); strings.TrimSpace(menu) != "" {
			t.Fatalf("user resource should stay routable but hidden from CPAMP sidebar menu: %#v", resource)
		}
	}
	if !userResourceSeen {
		t.Fatal("user resource should remain registered for the dedicated cpa-usage host")
	}
}

func TestFrontendAuthAcceptsManagedKeyAndRejectsDisallowedModel(t *testing.T) {
	configureTestState(t)
	authBody, _ := json.Marshal(frontendAuthRequest{
		Headers: http.Header{"Authorization": []string{"Bearer cpa_live"}},
		Body:    []byte(`{"model":"gpt-5.5","stream":true}`),
	})
	raw, err := frontendAuth(authBody)
	if err != nil {
		t.Fatal(err)
	}
	var resp frontendAuthResponse
	unwrapEnvelope(t, raw, &resp)
	if !resp.Authenticated || resp.Principal != "alice-key" {
		t.Fatalf("auth response = %#v", resp)
	}
	disallowed, _ := json.Marshal(frontendAuthRequest{
		Headers: http.Header{"Authorization": []string{"Bearer cpa_live"}},
		Body:    []byte(`{"model":"other-model","stream":true}`),
	})
	raw, err = frontendAuth(disallowed)
	if err != nil {
		t.Fatal(err)
	}
	unwrapEnvelope(t, raw, &resp)
	if resp.Authenticated {
		t.Fatalf("disallowed model authenticated: %#v", resp)
	}
}

func TestUserSessionNormalizesPastedBearerKey(t *testing.T) {
	configureTestState(t)
	raw, err := userSession(managementRequest{
		Headers: http.Header{"Authorization": []string{"Bearer Authorization: Bearer Bearer cpa_live "}},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp := unwrapManagementResponse(t, raw)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, string(resp.Body))
	}
	if got := resp.Headers.Get("set-cookie"); got == "" {
		t.Fatal("session cookie was not set")
	}
	if got := resp.Headers.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache-control = %q", got)
	}
	var body map[string]any
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["ok"] != true {
		t.Fatalf("body = %#v", body)
	}
}

func TestUserSessionPrefersDedicatedHeaderOverAuthorization(t *testing.T) {
	configureTestState(t)
	raw, err := userSession(managementRequest{
		Headers: http.Header{
			"Authorization":      []string{"Bearer cpamp-admin-token"},
			"X-CPA-Governor-Key": []string{"Authorization: Bearer cpa_live"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp := unwrapManagementResponse(t, raw)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, string(resp.Body))
	}
	if got := resp.Headers.Get("set-cookie"); got == "" {
		t.Fatal("session cookie was not set")
	}
	if got := resp.Headers.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache-control = %q", got)
	}
}

func TestUserSessionExplainsNativeAndPreviewKeys(t *testing.T) {
	configureTestState(t)
	cases := []struct {
		name string
		key  string
		code string
	}{
		{name: "native sk", key: "sk-test", code: "native_cpa_key_not_supported"},
		{name: "preview", key: "cpa_abcd...efgh", code: "key_preview_not_usable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := userSession(managementRequest{
				Headers: http.Header{"Authorization": []string{"Bearer " + tc.key}},
			})
			if err != nil {
				t.Fatal(err)
			}
			resp := unwrapManagementResponse(t, raw)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d body=%s", resp.StatusCode, string(resp.Body))
			}
			var body map[string]any
			if err := json.Unmarshal(resp.Body, &body); err != nil {
				t.Fatal(err)
			}
			if body["error"] != tc.code || body["message"] == "" {
				t.Fatalf("body = %#v", body)
			}
		})
	}
}

func TestUserHTMLSessionUsesGETResourceRoute(t *testing.T) {
	html := userHTML()
	if !strings.Contains(html, `api("/session"`) || !strings.Contains(html, `"X-CPA-Governor-Key": raw`) {
		t.Fatal("user login should call the GET-only resource session route with the dedicated user-key header")
	}
	if strings.Contains(html, "Authorization:'Bearer '+raw") || strings.Contains(html, `Authorization:"Bearer "+raw`) {
		t.Fatal("CPAMP embeds plugin pages behind its own auth; user login must use a dedicated key header")
	}
	if strings.Contains(strings.ToLower(html), "method:'post'") || strings.Contains(strings.ToLower(html), `method:"post"`) {
		t.Fatal("CPA resource routes are GET-only; user login must not use POST")
	}
}

func TestAdminHTMLIsReadOnlyCodexContDashboard(t *testing.T) {
	html := adminHTML()
	for _, want := range []string{
		`/governor/codexcont/admin`,
		`EventSource(BASE + "/logs/stream")`,
		`最近请求`,
		`高级日志`,
		`命中轮`,
		`末轮 reasoning`,
		`AbortController`,
		`cancelSnapshot`,
		`reconnectAll`,
		`sync-button`,
		`applySyncLight`,
		`refreshMinimumDelay`,
		`activeProcessingCount`,
		`PROCESSING_STALE_MS`,
		`live-bad`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("admin dashboard missing %q", want)
		}
	}
	for _, forbidden := range []string{
		`Key 管理`,
		`>请求明细</button>`,
		`保存 CodexCont`,
		`action:'save'`,
		`action:"save"`,
		`ccEnabled`,
		`ccUrl`,
		`ccFail`,
		`c.active_requests`,
	} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("admin dashboard should be read-only and not contain %q", forbidden)
		}
	}
}

func TestUserHTMLHasTwoTabsAndNoSpinner(t *testing.T) {
	html := userHTML()
	if !strings.Contains(html, `data-tab="usage"`) || !strings.Contains(html, `额度与明细`) {
		t.Fatal("user page should expose the quota/details tab")
	}
	if !strings.Contains(html, `data-tab="codex"`) || !strings.Contains(html, `思维链保护`) {
		t.Fatal("user page should expose the protection tab")
	}
	if strings.Contains(html, `data-tab="requests"`) || strings.Contains(html, `>请求明细</button>`) {
		t.Fatal("request details should be merged into the quota/details tab, not a third tab")
	}
	for _, want := range []string{
		`setRefreshState`,
		`markRefreshStart`,
		`just-updated`,
		`refreshActive`,
		`switchTab`,
		`AbortController`,
		`cancelRefresh`,
		`visibilitychange`,
		`applySyncLight`,
		`live-bad`,
		`sortProtectionItems`,
		`activeProcessingCount`,
		`PROCESSING_STALE_MS`,
		`id="active-chip"`,
		`setInterval(() => refreshActive(false), 5000)`,
		`单 Key 实时监控`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("user page should keep realtime refresh animation hook %q", want)
		}
	}
	if strings.Contains(html, `load(true)`) || strings.Contains(html, `setInterval(() => load(false), 3000)`) {
		t.Fatal("user page should not use the old blocking tab switch or 3s full reload loop")
	}
	css := sharedCSS()
	for _, want := range []string{
		`.sync-button.syncing`,
		`.sync-button.just-updated`,
		`.sync-button.live-ok .sync-light`,
		`.sync-button.live-bad .sync-light`,
		`@keyframes statusBlink`,
		`@keyframes statusPing`,
	} {
		if !strings.Contains(css, want) {
			t.Fatalf("shared css should keep restrained realtime status style %q", want)
		}
	}
	for _, forbidden := range []string{
		".spin", "spin ", "rotate(", ".metric::after",
		`@keyframes syncSweep`,
		`@keyframes liveSweep`,
		`@keyframes metricBump`,
		`@keyframes rowFresh`,
		`.sync-button::after`,
		`.topbar::after`,
		`.metrics.cards-updated .metric`,
	} {
		if strings.Contains(css, forbidden) {
			t.Fatalf("custom pages should not use old spinner/sweep/bump effects: %q", forbidden)
		}
	}
}

func TestAdminCodexContGETSavePersistsSettings(t *testing.T) {
	configureTestState(t)
	raw, err := adminCodexCont(managementRequest{
		Method: http.MethodGet,
		Query: url.Values{
			"action":    []string{"save"},
			"enabled":   []string{"true"},
			"url":       []string{"http://codexcont:8787/"},
			"fail_mode": []string{"fail_closed"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp := unwrapManagementResponse(t, raw)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, string(resp.Body))
	}
	cfg := loadedConfig()
	if !cfg.CodexContEnabled || cfg.CodexContURL != "http://codexcont:8787" || cfg.FailMode != "fail_closed" {
		t.Fatalf("cfg = %#v", cfg)
	}
	settings, err := loadedStore().LoadSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if settings["codexcont_enabled"] != "true" || settings["codexcont_url"] != "http://codexcont:8787" || settings["fail_mode"] != "fail_closed" {
		t.Fatalf("settings = %#v", settings)
	}
}

func TestUserCodexContFiltersToCurrentKey(t *testing.T) {
	key := configureTestState(t)
	otherPreview := governor.HashPreview(governor.SHA256Hex("cpa_bob"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/engine/healthz" {
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		if r.URL.Path != "/admin/requests" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"requests":[
			{"request_id":"alice-old","model":"gpt-5.5","protection":"protected_clean","started_at":"2026-07-02T10:00:00Z","key_identity":{"known":true,"id":"alice-key","name":"Alice","preview":"` + key.Preview + `"},"latest_reasoning_tokens":120,"continuation_count":0},
			{"request_id":"bob-1","model":"gpt-5.5","protection":"protected_clean","key_identity":{"known":true,"id":"bob-key","name":"Bob","preview":"` + otherPreview + `"},"latest_reasoning_tokens":120,"continuation_count":0},
			{"request_id":"unknown-1","model":"gpt-5.5","protection":"protected_clean","key_identity":{"known":false,"preview":"nope"}},
			{"request_id":"alice-new","model":"gpt-5.5","protection":"auto_continued","started_at":"2026-07-02T11:00:00Z","key_identity":{"known":true,"id":"alice-key","name":"Alice","preview":"` + key.Preview + `"},"latest_reasoning_tokens":181,"continuation_count":1}
		]}`))
	}))
	defer srv.Close()

	cfg := loadedConfig()
	cfg.CodexContEnabled = true
	cfg.CodexContURL = srv.URL
	state.mu.Lock()
	state.cfg = cfg
	state.mu.Unlock()

	token, err := governor.SignSession(governor.SessionPayload{
		KeyID:     key.ID,
		KeyHash:   key.KeyHash,
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}, cfg.SessionSecret)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := userCodexCont(managementRequest{
		Headers: http.Header{"Cookie": []string{"cpa_governor_session=" + token}},
		Query:   url.Values{"limit": []string{"20"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp := unwrapManagementResponse(t, raw)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, string(resp.Body))
	}
	var body struct {
		OK       bool             `json:"ok"`
		Source   string           `json:"source"`
		Requests []map[string]any `json:"requests"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatal(err)
	}
	if !body.OK || body.Source != "codexcont_admin" {
		t.Fatalf("body = %#v", body)
	}
	if len(body.Requests) != 2 || body.Requests[0]["request_id"] != "alice-new" || body.Requests[1]["request_id"] != "alice-old" {
		t.Fatalf("requests were not filtered to current key: %#v", body.Requests)
	}
	encoded, _ := json.Marshal(body.Requests)
	if strings.Contains(string(encoded), "bob-1") || strings.Contains(string(encoded), "unknown-1") {
		t.Fatalf("other users leaked into codexcont response: %s", encoded)
	}
}

func TestUsageHandleStoresCost(t *testing.T) {
	configureTestState(t)
	rec := usageRecord{
		Provider:        "openai",
		ExecutorType:    "codex",
		Model:           "gpt-5.5-real",
		Alias:           "gpt-5.5",
		APIKey:          "alice-key",
		Source:          "/v1/responses",
		ReasoningEffort: "high",
		ServiceTier:     "default",
		RequestedAt:     time.Now(),
		Latency:         1500 * time.Millisecond,
		TTFT:            220 * time.Millisecond,
		Failure: usageFailure{
			StatusCode: 200,
		},
		ResponseHeaders: http.Header{"X-Request-Id": []string{"req-usage-1"}},
		Detail: usageDetail{
			InputTokens:     100,
			CachedTokens:    20,
			OutputTokens:    50,
			ReasoningTokens: 30,
			TotalTokens:     150,
		},
	}
	rawRec, _ := json.Marshal(rec)
	if _, err := usageHandle(rawRec); err != nil {
		t.Fatal(err)
	}
	events, err := loadedStore().RecentEvents(context.Background(), "alice-key", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Cost <= 0 || events[0].Usage.ReasoningTokens != 30 {
		t.Fatalf("events = %#v", events)
	}
	got := events[0]
	if got.RequestID != "req-usage-1" || got.Model != "gpt-5.5" || got.ActualModel != "gpt-5.5-real" {
		t.Fatalf("model/request fields not projected: %#v", got)
	}
	if got.Provider != "openai" || got.ExecutorType != "codex" || got.Endpoint != "/v1/responses" {
		t.Fatalf("source fields not projected: %#v", got)
	}
	if got.ReasoningEffort != "high" || got.ServiceTier != "default" || got.TTFTMS != 220 || got.StatusCode != 200 {
		t.Fatalf("realtime detail fields not projected: %#v", got)
	}
}

func TestRefreshKeyPolicyStateImportsNewKeys(t *testing.T) {
	dir := t.TempDir()
	store, err := governor.OpenStore(filepath.Join(dir, "governor.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "policy.json")
	writePolicy := func(id, rawKey string) {
		t.Helper()
		body := map[string]any{"keys": []map[string]any{{
			"id":       id,
			"name":     id,
			"key_hash": "sha256:" + governor.SHA256Hex(rawKey),
			"enabled":  true,
		}}}
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writePolicy("alice", "cpa_alice")

	state.mu.Lock()
	state.cfg = governor.DefaultConfig()
	state.store = store
	state.keyState = governor.KeyPolicyState{}
	state.keyStatePath = path
	state.keyStateModTime = time.Time{}
	state.keyStateLastCheck = time.Time{}
	state.rpmBuckets = map[string][]time.Time{}
	state.mu.Unlock()
	t.Cleanup(func() {
		state.mu.Lock()
		if state.store != nil {
			_ = state.store.Close()
			state.store = nil
		}
		state.mu.Unlock()
	})

	if err := refreshKeyPolicyState(true); err != nil {
		t.Fatal(err)
	}
	if key, ok := findKeyByRaw("cpa_alice"); !ok || key.ID != "alice" {
		t.Fatalf("alice not imported: %#v ok=%v", key, ok)
	}

	writePolicy("bob", "cpa_bob")
	if err := refreshKeyPolicyState(true); err != nil {
		t.Fatal(err)
	}
	if key, ok := findKeyByRaw("cpa_bob"); !ok || key.ID != "bob" {
		t.Fatalf("bob not imported after refresh: %#v ok=%v", key, ok)
	}
	if key, ok := findKeyByRaw("cpa_alice"); ok {
		t.Fatalf("deleted alice should not remain in Governor mirror: %#v", key)
	}
}
