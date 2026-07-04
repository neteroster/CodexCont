package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"codexcont/cpa-governor-plugin/internal/governor"
	_ "embed"
	"gopkg.in/yaml.v3"
)

const pluginID = "cpa-governor"

//go:embed assets/admin.html
var adminHTMLTemplate string

//go:embed assets/user.html
var userHTMLTemplate string

//go:embed assets/shared.css
var sharedCSSTemplate string

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type registration struct {
	SchemaVersion uint32       `json:"schema_version"`
	Metadata      metadata     `json:"metadata"`
	Capabilities  capabilities `json:"capabilities"`
}

type metadata struct {
	Name             string        `json:"Name"`
	Version          string        `json:"Version"`
	Author           string        `json:"Author"`
	GitHubRepository string        `json:"GitHubRepository"`
	ConfigFields     []configField `json:"ConfigFields"`
}

type capabilities struct {
	FrontendAuthProvider          bool     `json:"frontend_auth_provider"`
	FrontendAuthProviderExclusive bool     `json:"frontend_auth_provider_exclusive"`
	ModelRouter                   bool     `json:"model_router"`
	Executor                      bool     `json:"executor"`
	ExecutorModelScope            string   `json:"executor_model_scope"`
	ExecutorInputFormats          []string `json:"executor_input_formats"`
	ExecutorOutputFormats         []string `json:"executor_output_formats"`
	UsagePlugin                   bool     `json:"usage_plugin"`
	ManagementAPI                 bool     `json:"management_api"`
}

type runtimeState struct {
	mu                sync.RWMutex
	cfg               governor.Config
	store             *governor.Store
	keyState          governor.KeyPolicyState
	keyStatePath      string
	keyStateModTime   time.Time
	keyStateLastCheck time.Time
	rpmBuckets        map[string][]time.Time
}

var state = runtimeState{
	rpmBuckets: map[string][]time.Time{},
}

func main() { runPreviewIfRequested() }

func runPreviewIfRequested() {
	addr := strings.TrimSpace(os.Getenv("CPA_GOVERNOR_PREVIEW_ADDR"))
	if addr == "" {
		return
	}
	cfg := governor.DefaultConfig()
	previewDir, err := os.MkdirTemp("", "cpa-governor-preview-*")
	if err != nil {
		panic(err)
	}
	cfg.StateDBPath = previewDir + "/governor.sqlite"
	cfg.SessionSecret = "preview-secret"
	cfg.CodexContEnabled = true
	cfg.CodexContURL = "http://" + addr
	store, err := governor.OpenStore(cfg.StateDBPath)
	if err != nil {
		panic(err)
	}
	previewRawKey := "cpa_preview_abcdefghijklmnopqrstuvwxyz0123456789AB"
	previewKey := governor.KeyRecord{
		ID:              "preview-key",
		Name:            "演示用户",
		KeyHash:         "sha256:" + governor.SHA256Hex(previewRawKey),
		Enabled:         true,
		Preview:         governor.HashPreview(governor.SHA256Hex(previewRawKey)),
		RPM:             60,
		Concurrency:     2,
		Models:          []string{"gpt-5.5", "gpt-5.4"},
		FiveHourUSD:     floatPtr(2),
		DailyLimitUSD:   floatPtr(8),
		WeeklyLimitUSD:  floatPtr(40),
		MonthlyLimitUSD: floatPtr(120),
		Prices: map[string]governor.ModelPrice{
			"gpt-5.5": {Model: "gpt-5.5", InputPerMillion: 5, OutputPerMillion: 30, CacheReadPerMillion: 0.5},
		},
	}
	_ = store.UpsertKey(context.Background(), previewKey)
	_ = store.InsertUsage(context.Background(), governor.UsageEvent{
		RequestID:       "req-preview-a",
		KeyID:           previewKey.ID,
		KeyPreview:      previewKey.Preview,
		Model:           "gpt-5.5",
		RequestedModel:  "gpt-5.5",
		ActualModel:     "gpt-5.5",
		Provider:        "openai",
		ExecutorType:    "codex",
		Endpoint:        "/v1/responses",
		RequestedAt:     time.Now().Add(-3 * time.Minute),
		LatencyMS:       14320,
		TTFTMS:          1180,
		ReasoningEffort: "high",
		ServiceTier:     "default",
		StatusCode:      200,
		Usage: governor.TokenUsage{
			InputTokens:     120000,
			CachedTokens:    103000,
			OutputTokens:    2100,
			ReasoningTokens: 516,
			TotalTokens:     122100,
		},
		Cost: 0.151,
		CostBreakdown: governor.CostForUsage(previewKey.Prices["gpt-5.5"], governor.TokenUsage{
			InputTokens:     120000,
			CachedTokens:    103000,
			OutputTokens:    2100,
			ReasoningTokens: 516,
			TotalTokens:     122100,
		}, "gpt-5.5"),
	})
	_ = store.SaveCodexSummary(context.Background(), "req-preview-a", previewKey.ID, "gpt-5.5", "auto_continued", map[string]any{
		"request_id":                        "req-preview-a",
		"model":                             "gpt-5.5",
		"path":                              "/v1/responses",
		"started_at":                        time.Now().Add(-2 * time.Minute).Format(time.RFC3339),
		"updated_at":                        time.Now().Add(-90 * time.Second).Format(time.RFC3339),
		"duration_ms":                       5570,
		"status":                            "completed",
		"protection":                        "auto_continued",
		"key_identity":                      previewKey.Safe(),
		"latest_round":                      2,
		"latest_reasoning_tokens":           181,
		"first_truncation_round":            1,
		"first_truncation_reasoning_tokens": 516,
		"first_truncation_decision":         "continue",
		"continuation_count":                1,
		"stopped_reason":                    "completed",
		"rounds":                            []map[string]any{{"round": 1, "reasoning_tokens": 516, "decision": "continue", "truncation_match": true}, {"round": 2, "reasoning_tokens": 181, "decision": "clean", "truncation_match": false}},
	})
	state.mu.Lock()
	state.cfg = cfg
	state.store = store
	state.keyState = governor.KeyPolicyState{Keys: []governor.KeyRecord{previewKey}}
	state.rpmBuckets = map[string][]time.Time{}
	state.mu.Unlock()
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/resource/plugins/cpa-governor/admin", func(w http.ResponseWriter, r *http.Request) {
		_ = r
		w.Header().Set("content-type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(adminHTML()))
	})
	mux.HandleFunc("/v0/resource/plugins/cpa-governor/user", func(w http.ResponseWriter, r *http.Request) {
		_ = r
		w.Header().Set("content-type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(userHTML()))
	})
	writePreviewStatus := func(w http.ResponseWriter) {
		w.Header().Set("content-type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"counters": map[string]any{
				"total_requests":  4,
				"active_requests": 17,
				"continuations":   1,
				"truncation_hits": 1,
				"failures":        0,
			},
			"last_error": nil,
		})
	}
	writePreviewRequests := func(w http.ResponseWriter) {
		w.Header().Set("content-type", "application/json; charset=utf-8")
		now := time.Now().UTC()
		identity := previewKey.Safe()
		_ = json.NewEncoder(w).Encode(map[string]any{"requests": []map[string]any{
			{
				"request_id":                        "req-preview-a",
				"model":                             "gpt-5.5",
				"path":                              "/v1/responses",
				"started_at":                        now.Add(-4 * time.Minute).Format(time.RFC3339),
				"updated_at":                        now.Add(-3 * time.Minute).Format(time.RFC3339),
				"duration_ms":                       5570,
				"status":                            "completed",
				"protection":                        "auto_continued",
				"key_identity":                      identity,
				"latest_round":                      2,
				"latest_reasoning_tokens":           181,
				"first_truncation_round":            1,
				"first_truncation_reasoning_tokens": 516,
				"continuation_count":                1,
				"rounds":                            []map[string]any{{"round": 1, "reasoning_tokens": 516, "decision": "continue", "truncation_match": true}, {"round": 2, "reasoning_tokens": 181, "decision": "clean", "truncation_match": false}},
			},
			{
				"request_id":              "req-preview-stale",
				"model":                   "gpt-5.5",
				"path":                    "/v1/responses",
				"started_at":              now.Add(-45 * time.Minute).Format(time.RFC3339),
				"updated_at":              now.Add(-44 * time.Minute).Format(time.RFC3339),
				"status":                  "processing",
				"protection":              "processing",
				"key_identity":            identity,
				"latest_round":            1,
				"latest_reasoning_tokens": 140,
				"continuation_count":      0,
				"rounds":                  []map[string]any{{"round": 1, "reasoning_tokens": 140, "decision": "clean", "truncation_match": false}},
			},
			{
				"request_id":              "req-preview-live",
				"model":                   "gpt-5.5",
				"path":                    "/v1/responses",
				"started_at":              now.Add(-30 * time.Second).Format(time.RFC3339),
				"updated_at":              now.Add(-5 * time.Second).Format(time.RFC3339),
				"status":                  "processing",
				"protection":              "processing",
				"key_identity":            identity,
				"latest_round":            1,
				"latest_reasoning_tokens": 140,
				"continuation_count":      0,
				"rounds":                  []map[string]any{{"round": 1, "reasoning_tokens": 140, "decision": "clean", "truncation_match": false}},
			},
		}})
	}
	mux.HandleFunc("/governor/codexcont/admin/status", func(w http.ResponseWriter, r *http.Request) {
		_ = r
		writePreviewStatus(w)
	})
	mux.HandleFunc("/admin/status", func(w http.ResponseWriter, r *http.Request) {
		_ = r
		writePreviewStatus(w)
	})
	mux.HandleFunc("/governor/codexcont/admin/requests", func(w http.ResponseWriter, r *http.Request) {
		_ = r
		writePreviewRequests(w)
	})
	mux.HandleFunc("/admin/requests", func(w http.ResponseWriter, r *http.Request) {
		_ = r
		writePreviewRequests(w)
	})
	mux.HandleFunc("/governor/codexcont/admin/logs/stream", func(w http.ResponseWriter, r *http.Request) {
		_ = r
		w.Header().Set("content-type", "text/event-stream")
		w.Header().Set("cache-control", "no-cache")
		_, _ = w.Write([]byte("event: ready\ndata: {\"ok\":true}\n\n"))
		_, _ = w.Write([]byte("event: log\ndata: {\"ts\":\"2026-07-02T02:09:59Z\",\"level\":\"info\",\"event\":\"round_decision\",\"message\":\"preview round decision\",\"fields\":{\"request_id\":\"req-preview-a\"}}\n\n"))
	})
	mux.HandleFunc("/engine/healthz", func(w http.ResponseWriter, r *http.Request) {
		_ = r
		w.Header().Set("content-type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"ok":true,"mode":"preview"}`))
	})
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		_ = r
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		rawReq, _ := json.Marshal(managementRequest{
			Method:  r.Method,
			Path:    r.URL.Path,
			Headers: r.Header,
			Query:   r.URL.Query(),
		})
		rawResp, _ := managementHandle(rawReq)
		var env envelope
		_ = json.Unmarshal(rawResp, &env)
		var resp managementResponse
		_ = json.Unmarshal(env.Result, &resp)
		for key, values := range resp.Headers {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		if resp.StatusCode != 0 {
			w.WriteHeader(resp.StatusCode)
		}
		_, _ = w.Write(resp.Body)
	})
	if err := http.ListenAndServe(addr, mux); err != nil {
		panic(err)
	}
}

func floatPtr(value float64) *float64 { return &value }

func shutdownPlugin() {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.store != nil {
		_ = state.store.Close()
		state.store = nil
	}
}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case methodPluginRegister, methodPluginReconfigure:
		if err := configure(request); err != nil {
			return nil, err
		}
		return okEnvelope(pluginRegistration())
	case methodFrontendAuthIdentifier:
		return okEnvelope(map[string]string{"identifier": pluginID})
	case methodFrontendAuthAuthenticate:
		return frontendAuth(request)
	case methodModelRoute:
		return routeModel(request)
	case methodExecutorIdentifier:
		return okEnvelope(map[string]string{"identifier": pluginID})
	case methodExecutorExecute:
		return executorExecute(request)
	case methodExecutorExecuteStream:
		return executorExecuteStream(request)
	case methodExecutorCountTokens:
		return okEnvelope(executorResponse{Payload: []byte(`{"input_tokens":0}`)})
	case methodUsageHandle:
		return usageHandle(request)
	case methodManagementRegister:
		return managementRegister()
	case methodManagementHandle:
		return managementHandle(request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func configure(raw []byte) error {
	cfg := governor.DefaultConfig()
	if len(raw) > 0 {
		var req lifecycleRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return err
		}
		if len(req.ConfigYAML) > 0 {
			if err := yaml.Unmarshal(req.ConfigYAML, &cfg); err != nil {
				return err
			}
		}
	}
	cfg = cfg.Normalize()
	store, err := governor.OpenStore(cfg.StateDBPath)
	if err != nil {
		return err
	}
	cfg = applyStoredSettings(cfg, store)
	var keyState governor.KeyPolicyState
	if strings.TrimSpace(cfg.KeyPolicyStatePath) != "" {
		loaded, err := governor.LoadKeyPolicyState(cfg.KeyPolicyStatePath)
		if err == nil {
			keyState = loaded
			_ = store.ImportKeys(context.Background(), loaded)
		}
	}
	state.mu.Lock()
	old := state.store
	state.cfg = cfg
	state.store = store
	state.keyState = keyState
	state.keyStatePath = strings.TrimSpace(cfg.KeyPolicyStatePath)
	state.keyStateModTime = time.Time{}
	state.keyStateLastCheck = time.Time{}
	state.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	_ = refreshKeyPolicyState(true)
	return nil
}

func applyStoredSettings(cfg governor.Config, store *governor.Store) governor.Config {
	if store == nil {
		return cfg
	}
	settings, err := store.LoadSettings(context.Background())
	if err != nil {
		return cfg
	}
	if value, ok := settings["codexcont_enabled"]; ok {
		if parsed, err := strconv.ParseBool(value); err == nil {
			cfg.CodexContEnabled = parsed
		}
	}
	if value := strings.TrimSpace(settings["codexcont_url"]); value != "" {
		cfg.CodexContURL = strings.TrimRight(value, "/")
	}
	if value := strings.TrimSpace(settings["fail_mode"]); value != "" {
		cfg.FailMode = strings.ToLower(value)
	}
	return cfg.Normalize()
}

func pluginRegistration() registration {
	cfg := loadedConfig()
	return registration{
		SchemaVersion: schemaVersion,
		Metadata: metadata{
			Name:             "cpa-governor",
			Version:          "0.1.0",
			Author:           "konbakuyomu/CodexCont",
			GitHubRepository: "https://local/CodexCont",
			ConfigFields: []configField{
				{Name: "enabled", Type: configBoolean, Description: "Enable Governor key authentication and management surfaces."},
				{Name: "exclusive_auth", Type: configBoolean, Description: "When true, Governor frontend auth is exclusive."},
				{Name: "state_db_path", Type: configString, Description: "SQLite path for Governor state."},
				{Name: "key_policy_state_path", Type: configString, Description: "Optional CPA Key Policy state JSON path to import/mirror."},
				{Name: "session_secret", Type: configString, Description: "Secret used to sign user portal sessions."},
				{Name: "codexcont_enabled", Type: configBoolean, Description: "Enable CodexCont engine status integration."},
				{Name: "codexcont_route", Type: configBoolean, Description: "Route protected Responses requests to Governor executor. Keep false until cutover validation."},
				{Name: "codexcont_url", Type: configString, Description: "Internal CodexCont engine base URL."},
				{Name: "fail_mode", Type: configEnum, EnumValues: []string{"fallback", "fail_closed"}, Description: "Behavior when engine is unavailable."},
			},
		},
		Capabilities: capabilities{
			FrontendAuthProvider:          true,
			FrontendAuthProviderExclusive: cfg.ExclusiveAuth,
			ModelRouter:                   true,
			Executor:                      true,
			ExecutorModelScope:            "static",
			ExecutorInputFormats:          []string{"openai", "responses"},
			ExecutorOutputFormats:         []string{"openai", "responses"},
			UsagePlugin:                   true,
			ManagementAPI:                 true,
		},
	}
}

func loadedConfig() governor.Config {
	state.mu.RLock()
	defer state.mu.RUnlock()
	if state.cfg.StateDBPath == "" {
		return governor.DefaultConfig()
	}
	return state.cfg
}

func loadedStore() *governor.Store {
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.store
}

func frontendAuth(raw []byte) ([]byte, error) {
	var req frontendAuthRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return okEnvelope(frontendAuthResponse{Authenticated: false})
	}
	cfg := loadedConfig()
	if !cfg.Enabled {
		return okEnvelope(frontendAuthResponse{Authenticated: false})
	}
	key := bearer(req.Headers.Get("Authorization"))
	if key == "" {
		return okEnvelope(frontendAuthResponse{Authenticated: false})
	}
	record, ok := findKeyByRaw(key)
	if !ok || !record.Enabled {
		return okEnvelope(frontendAuthResponse{Authenticated: false})
	}
	model := requestedModelFromBody(req.Body)
	if model != "" && !governor.ModelAllowed(record.Models, model) {
		return okEnvelope(frontendAuthResponse{Authenticated: false})
	}
	if !allowRPM(record) {
		return okEnvelope(frontendAuthResponse{Authenticated: false})
	}
	if !allowQuota(record) {
		return okEnvelope(frontendAuthResponse{Authenticated: false})
	}
	return okEnvelope(frontendAuthResponse{
		Authenticated: true,
		Principal:     record.ID,
		Metadata: map[string]string{
			"provider": "cpa-governor",
			"key_id":   record.ID,
			"key_name": record.Name,
			"preview":  record.Preview,
		},
	})
}

func findKeyByRaw(rawKey string) (governor.KeyRecord, bool) {
	_ = refreshKeyPolicyState(false)
	if key, ok := lookupKeyByRaw(rawKey); ok {
		return key, true
	}
	if strings.HasPrefix(strings.ToLower(governor.NormalizeSubmittedKey(rawKey)), "cpa_") {
		_ = refreshKeyPolicyState(true)
		return lookupKeyByRaw(rawKey)
	}
	return governor.KeyRecord{}, false
}

func lookupKeyByRaw(rawKey string) (governor.KeyRecord, bool) {
	state.mu.RLock()
	keyState := state.keyState
	store := state.store
	state.mu.RUnlock()
	if key, ok := keyState.FindByRawKey(rawKey); ok {
		return key, true
	}
	if store != nil {
		key, ok, err := store.FindKeyByHash(context.Background(), governor.SHA256Hex(rawKey))
		if err == nil && ok {
			return key, true
		}
	}
	return governor.KeyRecord{}, false
}

func refreshKeyPolicyState(force bool) error {
	state.mu.RLock()
	path := strings.TrimSpace(state.keyStatePath)
	lastMod := state.keyStateModTime
	store := state.store
	state.mu.RUnlock()
	if path == "" || store == nil {
		return nil
	}
	now := time.Now()
	info, err := os.Stat(path)
	if err != nil {
		state.mu.Lock()
		state.keyStateLastCheck = now
		state.mu.Unlock()
		return err
	}
	if !force && !info.ModTime().After(lastMod) {
		state.mu.Lock()
		state.keyStateLastCheck = now
		state.mu.Unlock()
		return nil
	}
	loaded, err := governor.LoadKeyPolicyState(path)
	if err != nil {
		state.mu.Lock()
		state.keyStateLastCheck = now
		state.mu.Unlock()
		return err
	}
	if err := store.ImportKeys(context.Background(), loaded); err != nil {
		return err
	}
	state.mu.Lock()
	state.keyState = loaded
	state.keyStateModTime = info.ModTime()
	state.keyStateLastCheck = now
	state.mu.Unlock()
	return nil
}

func routeModel(raw []byte) ([]byte, error) {
	var req modelRouteRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	cfg := loadedConfig()
	if !cfg.Enabled || !cfg.CodexContRoute {
		return okEnvelope(modelRouteResponse{Handled: false})
	}
	if !isResponsesRequest(req.SourceFormat, req.Body) {
		return okEnvelope(modelRouteResponse{Handled: false})
	}
	return okEnvelope(modelRouteResponse{
		Handled:    true,
		TargetKind: routeTargetSelf,
		Reason:     "cpa_governor_codexcont_engine_enabled",
	})
}

func isResponsesRequest(source string, body []byte) bool {
	source = strings.ToLower(strings.TrimSpace(source))
	if strings.Contains(source, "response") || source == "openai" {
		return true
	}
	return strings.Contains(string(body), `"stream"`) && strings.Contains(string(body), `"model"`)
}

func requestedModelFromBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return ""
	}
	if text, ok := raw["model"].(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

func allowRPM(key governor.KeyRecord) bool {
	if key.RPM <= 0 {
		return true
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-time.Minute)
	bucket := state.rpmBuckets[key.ID]
	kept := bucket[:0]
	for _, ts := range bucket {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	if len(kept) >= key.RPM {
		state.rpmBuckets[key.ID] = kept
		return false
	}
	state.rpmBuckets[key.ID] = append(kept, now)
	return true
}

func allowQuota(key governor.KeyRecord) bool {
	store := loadedStore()
	if store == nil {
		return true
	}
	ctx := context.Background()
	now := time.Now()
	limits := []struct {
		name  string
		limit *float64
	}{
		{governor.Range5H, key.FiveHourUSD},
		{governor.Range24H, key.DailyLimitUSD},
		{governor.Range7D, key.WeeklyLimitUSD},
		{governor.RangeMonth, key.MonthlyLimitUSD},
	}
	for _, item := range limits {
		used, err := store.UsageSum(ctx, key.ID, governor.WindowFor(item.name, now))
		if err != nil {
			continue
		}
		if !governor.CheckLimit(used, item.limit).Allowed {
			return false
		}
	}
	return true
}

func executorUnavailable() ([]byte, error) {
	cfg := loadedConfig()
	if cfg.CodexContRoute && cfg.FailMode == "fail_closed" {
		return errorEnvelope("codexcont_executor_pending", "CPA Governor protected executor is not enabled in this build"), nil
	}
	return errorEnvelope("codexcont_executor_pending", "CPA Governor protected executor is in passive mode; disable codexcont_enabled to route through CPA provider path"), nil
}

func executorExecute(raw []byte) ([]byte, error) {
	var req executorCallRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	cfg := loadedConfig()
	if !cfg.CodexContRoute {
		return executorUnavailable()
	}
	payload := req.ExecutorRequest.Payload
	if len(payload) == 0 {
		payload = req.ExecutorRequest.OriginalRequest
	}
	result, err := callHost(methodHostModelExecute, hostModelExecutionRequest{
		EntryProtocol:  firstNonEmpty(req.ExecutorRequest.SourceFormat, "openai"),
		ExitProtocol:   firstNonEmpty(req.ExecutorRequest.Format, "openai"),
		Model:          req.ExecutorRequest.Model,
		Stream:         false,
		Body:           payload,
		Headers:        cloneHeader(req.ExecutorRequest.Headers),
		Query:          cloneValues(req.ExecutorRequest.Query),
		Alt:            req.ExecutorRequest.Alt,
		HostCallbackID: req.HostCallbackID,
	})
	if err != nil {
		return errorEnvelope("host_model_execute_error", err.Error()), nil
	}
	var resp hostModelExecutionResponse
	if err := json.Unmarshal(result, &resp); err != nil {
		return errorEnvelope("host_model_execute_decode_error", err.Error()), nil
	}
	if resp.StatusCode >= 400 {
		return errorEnvelope("host_model_execute_http_error", fmt.Sprintf("upstream returned %d", resp.StatusCode)), nil
	}
	return okEnvelope(executorResponse{Payload: resp.Body, Headers: resp.Headers})
}

func executorExecuteStream(raw []byte) ([]byte, error) {
	var req executorCallRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	cfg := loadedConfig()
	if !cfg.CodexContRoute {
		return executorUnavailable()
	}
	if strings.TrimSpace(req.StreamID) == "" {
		return errorEnvelope("stream_id_required", "stream_id is required for executor.execute_stream"), nil
	}
	payload := req.ExecutorRequest.Payload
	if len(payload) == 0 {
		payload = req.ExecutorRequest.OriginalRequest
	}
	result, err := callHost(methodHostModelExecuteStream, hostModelExecutionRequest{
		EntryProtocol:  firstNonEmpty(req.ExecutorRequest.SourceFormat, "openai"),
		ExitProtocol:   firstNonEmpty(req.ExecutorRequest.Format, "openai"),
		Model:          req.ExecutorRequest.Model,
		Stream:         true,
		Body:           payload,
		Headers:        cloneHeader(req.ExecutorRequest.Headers),
		Query:          cloneValues(req.ExecutorRequest.Query),
		Alt:            req.ExecutorRequest.Alt,
		HostCallbackID: req.HostCallbackID,
	})
	if err != nil {
		return errorEnvelope("host_model_stream_error", err.Error()), nil
	}
	var resp hostModelStreamResponse
	if err := json.Unmarshal(result, &resp); err != nil {
		return errorEnvelope("host_model_stream_decode_error", err.Error()), nil
	}
	if resp.StatusCode >= 400 {
		return errorEnvelope("host_model_stream_http_error", fmt.Sprintf("upstream returned %d", resp.StatusCode)), nil
	}
	if resp.StreamID == "" {
		return errorEnvelope("host_model_stream_empty", "host returned empty stream id"), nil
	}
	go forwardHostStream(req.StreamID, resp.StreamID)
	return okEnvelope(executorStreamResponse{Headers: resp.Headers})
}

func usageHandle(raw []byte) ([]byte, error) {
	var rec usageRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, err
	}
	_ = refreshKeyPolicyState(false)
	store := loadedStore()
	if store == nil {
		return okEnvelope(map[string]any{})
	}
	keys, _ := store.ListKeys(context.Background())
	keyByID := map[string]governor.KeyRecord{}
	for _, key := range keys {
		keyByID[key.ID] = key
	}
	key := keyByID[rec.APIKey]
	if key.ID == "" {
		key = keyByID[rec.Source]
	}
	usage := governor.TokenUsage{
		InputTokens:         rec.Detail.InputTokens,
		OutputTokens:        rec.Detail.OutputTokens,
		CachedTokens:        rec.Detail.CachedTokens,
		CacheReadTokens:     rec.Detail.CacheReadTokens,
		CacheCreationTokens: rec.Detail.CacheCreationTokens,
		ReasoningTokens:     rec.Detail.ReasoningTokens,
		TotalTokens:         rec.Detail.TotalTokens,
	}
	var cost float64
	var breakdown governor.CostBreakdown
	if key.ID != "" {
		if price, ok := governor.PriceForModel(key.Prices, firstNonEmpty(rec.Alias, rec.Model)); ok {
			breakdown = governor.CostForUsage(price, usage, firstNonEmpty(rec.Alias, rec.Model))
			cost = breakdown.Costs["total"]
		}
	}
	event := governor.UsageEvent{
		RequestID:       firstNonEmpty(rec.ResponseHeaders.Get("x-request-id"), rec.ResponseHeaders.Get("x-openai-request-id")),
		KeyID:           key.ID,
		KeyPreview:      key.Preview,
		Model:           firstNonEmpty(rec.Alias, rec.Model),
		RequestedModel:  firstNonEmpty(rec.Alias, rec.Model),
		ActualModel:     rec.Model,
		Provider:        rec.Provider,
		ExecutorType:    rec.ExecutorType,
		Endpoint:        rec.Source,
		RequestedAt:     rec.RequestedAt,
		LatencyMS:       rec.Latency.Milliseconds(),
		TTFTMS:          rec.TTFT.Milliseconds(),
		ReasoningEffort: rec.ReasoningEffort,
		ServiceTier:     rec.ServiceTier,
		StatusCode:      rec.Failure.StatusCode,
		Failed:          rec.Failed,
		Failure:         governor.Brief(rec.Failure.Body, 600),
		Usage:           usage,
		Cost:            cost,
		CostBreakdown:   breakdown,
	}
	_ = store.InsertUsage(context.Background(), event)
	return okEnvelope(map[string]any{})
}

func managementRegister() ([]byte, error) {
	resp := managementRegistrationResponse{
		Routes: []managementRoute{
			{Method: http.MethodGet, Path: "/plugins/cpa-governor/keys"},
			{Method: http.MethodPut, Path: "/plugins/cpa-governor/keys/limits"},
			{Method: http.MethodPost, Path: "/plugins/cpa-governor/keys/reset"},
			{Method: http.MethodGet, Path: "/plugins/cpa-governor/events"},
			{Method: http.MethodGet, Path: "/plugins/cpa-governor/codexcont"},
			{Method: http.MethodPut, Path: "/plugins/cpa-governor/codexcont"},
		},
		Resources: []resourceRoute{
			{Path: "/admin", Menu: "CPA Governor", Description: "Governor admin dashboard"},
			{Path: "/admin/api/keys"},
			{Path: "/admin/api/keys/limits"},
			{Path: "/admin/api/keys/reset"},
			{Path: "/admin/api/events"},
			{Path: "/admin/api/codexcont"},
			{Path: "/user", Description: "Self-service usage dashboard"},
			{Path: "/user/api/session"},
			{Path: "/user/api/me"},
			{Path: "/user/api/usage"},
			{Path: "/user/api/events"},
			{Path: "/user/api/codexcont"},
		},
	}
	return okEnvelope(resp)
}

func managementHandle(raw []byte) ([]byte, error) {
	var req managementRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	path := strings.TrimSpace(req.Path)
	switch {
	case path == "/v0/resource/plugins/cpa-governor/admin" || path == "/admin":
		return managementHTML(adminHTML())
	case path == "/v0/resource/plugins/cpa-governor/user" || path == "/user":
		return managementHTML(userHTML())
	case strings.HasSuffix(path, "/admin/api/keys"):
		return adminKeys(req)
	case strings.HasSuffix(path, "/admin/api/keys/limits"):
		return adminSetLimits(req)
	case strings.HasSuffix(path, "/admin/api/keys/reset"):
		return adminReset(req)
	case strings.HasSuffix(path, "/admin/api/events"):
		return adminEvents(req)
	case strings.HasSuffix(path, "/admin/api/codexcont"):
		return adminCodexCont(req)
	case strings.Contains(path, "/user/api/session"):
		return userSession(req)
	case strings.Contains(path, "/user/api/me"):
		return userMe(req)
	case strings.Contains(path, "/user/api/usage"):
		return userUsage(req)
	case strings.Contains(path, "/user/api/events"):
		return userEvents(req)
	case strings.Contains(path, "/user/api/codexcont"):
		return userCodexCont(req)
	case strings.HasSuffix(path, "/plugins/cpa-governor/keys"):
		return adminKeys(req)
	case strings.HasSuffix(path, "/plugins/cpa-governor/keys/limits"):
		return adminSetLimits(req)
	case strings.HasSuffix(path, "/plugins/cpa-governor/keys/reset"):
		return adminReset(req)
	case strings.HasSuffix(path, "/plugins/cpa-governor/events"):
		return adminEvents(req)
	case strings.HasSuffix(path, "/plugins/cpa-governor/codexcont"):
		return adminCodexCont(req)
	default:
		return jsonResponse(http.StatusNotFound, map[string]any{"ok": false, "error": "not_found"})
	}
}

func adminKeys(_ managementRequest) ([]byte, error) {
	_ = refreshKeyPolicyState(false)
	store := loadedStore()
	if store == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "store_unavailable"})
	}
	keys, err := store.ListKeys(context.Background())
	if err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
	}
	safe := make([]map[string]any, 0, len(keys))
	now := time.Now()
	for _, key := range keys {
		row := key.Safe()
		row["usage"] = usageWindows(context.Background(), store, key.ID, now)
		safe = append(safe, row)
	}
	return jsonResponse(http.StatusOK, map[string]any{"ok": true, "keys": safe, "codexcont": codexcontStatus()})
}

func adminSetLimits(req managementRequest) ([]byte, error) {
	var body struct {
		Limits []struct {
			ID          string   `json:"id"`
			FiveHourUSD *float64 `json:"five_hour_usd"`
			MonthlyUSD  *float64 `json:"monthly_usd"`
		} `json:"limits"`
	}
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_json"})
	}
	store := loadedStore()
	if store == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "store_unavailable"})
	}
	for _, item := range body.Limits {
		if strings.TrimSpace(item.ID) == "" {
			return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": "missing_key_id"})
		}
		if err := store.SetLimits(context.Background(), item.ID, item.FiveHourUSD, item.MonthlyUSD); err != nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		}
	}
	return adminKeys(req)
}

func adminReset(req managementRequest) ([]byte, error) {
	var body struct {
		ID     string `json:"id"`
		Window string `json:"window"`
	}
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_json"})
	}
	if body.Window == "" {
		body.Window = "all"
	}
	windows := []string{body.Window}
	if body.Window == "all" {
		windows = []string{governor.Range5H, governor.Range24H, governor.Range7D, governor.RangeMonth}
	}
	store := loadedStore()
	if store == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "store_unavailable"})
	}
	for _, window := range windows {
		if err := store.Reset(context.Background(), body.ID, window, time.Now()); err != nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		}
	}
	return jsonResponse(http.StatusOK, map[string]any{"ok": true})
}

func adminEvents(req managementRequest) ([]byte, error) {
	keyID := req.Query.Get("key_id")
	if keyID == "" {
		keyID = "all"
	}
	return eventsResponseFromRequest(req, keyID)
}

func adminCodexCont(req managementRequest) ([]byte, error) {
	if req.Method == http.MethodPut || (req.Method == http.MethodGet && req.Query.Get("action") == "save") {
		var body struct {
			Enabled  *bool  `json:"enabled"`
			URL      string `json:"url"`
			FailMode string `json:"fail_mode"`
		}
		if req.Method == http.MethodGet {
			if rawEnabled := strings.TrimSpace(req.Query.Get("enabled")); rawEnabled != "" {
				if parsed, err := strconv.ParseBool(rawEnabled); err == nil {
					body.Enabled = &parsed
				}
			}
			body.URL = req.Query.Get("url")
			body.FailMode = req.Query.Get("fail_mode")
		} else {
			_ = json.Unmarshal(req.Body, &body)
		}
		store := loadedStore()
		state.mu.Lock()
		if body.Enabled != nil {
			state.cfg.CodexContEnabled = *body.Enabled
		}
		if strings.TrimSpace(body.URL) != "" {
			state.cfg.CodexContURL = strings.TrimRight(strings.TrimSpace(body.URL), "/")
		}
		if strings.TrimSpace(body.FailMode) != "" {
			state.cfg.FailMode = strings.ToLower(strings.TrimSpace(body.FailMode))
		}
		state.cfg = state.cfg.Normalize()
		cfg := state.cfg
		state.mu.Unlock()
		if store != nil {
			settings := map[string]string{
				"codexcont_enabled": strconv.FormatBool(cfg.CodexContEnabled),
				"codexcont_url":     cfg.CodexContURL,
				"fail_mode":         cfg.FailMode,
			}
			if err := store.SaveSettings(context.Background(), settings); err != nil {
				return jsonResponse(http.StatusInternalServerError, map[string]any{"ok": false, "error": "save_settings_failed"})
			}
		}
		return jsonResponse(http.StatusOK, map[string]any{"ok": true, "codexcont": codexcontStatus()})
	}
	return jsonResponse(http.StatusOK, map[string]any{"ok": true, "codexcont": codexcontStatus()})
}

func userSession(req managementRequest) ([]byte, error) {
	key := userSubmittedKey(req)
	if key == "" {
		hint := governor.ExplainUnmatchedSubmittedKey(key)
		return jsonResponse(http.StatusUnauthorized, map[string]any{"ok": false, "error": hint.Error, "message": hint.Message})
	}
	record, ok := findKeyByRaw(key)
	if !ok {
		hint := governor.ExplainUnmatchedSubmittedKey(key)
		return jsonResponse(http.StatusUnauthorized, map[string]any{"ok": false, "error": hint.Error, "message": hint.Message})
	}
	if !record.Enabled {
		return jsonResponse(http.StatusForbidden, map[string]any{"ok": false, "error": "api_key_disabled", "message": "这个 Key 当前已禁用，请联系管理员。"})
	}
	cfg := loadedConfig()
	token, err := governor.SignSession(governor.SessionPayload{
		KeyID:     record.ID,
		KeyHash:   record.KeyHash,
		ExpiresAt: time.Now().Add(governor.SessionTTL()).Unix(),
	}, cfg.SessionSecret)
	if err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]any{"ok": false, "error": "session_error"})
	}
	body, err := json.Marshal(map[string]any{"ok": true, "me": record.Safe()})
	if err != nil {
		return nil, err
	}
	resp := managementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":  []string{"application/json; charset=utf-8"},
			"Cache-Control": []string{"no-store"},
			"Set-Cookie":    []string{fmt.Sprintf("cpa_governor_session=%s; Path=/; HttpOnly; SameSite=Lax; Max-Age=86400", token)},
		},
		Body: body,
	}
	return okEnvelope(resp)
}

func userSubmittedKey(req managementRequest) string {
	raw := firstNonEmpty(
		headerFirst(req.Headers, "X-CPA-Governor-Key"),
		headerFirst(req.Headers, "X-CPA-User-Key"),
	)
	if raw == "" {
		raw = bearer(headerFirst(req.Headers, "Authorization"))
	}
	return governor.NormalizeSubmittedKey(raw)
}

func userMe(req managementRequest) ([]byte, error) {
	key, ok := keyFromSession(req)
	if !ok {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"ok": false, "error": "not_authenticated"})
	}
	row := key.Safe()
	if store := loadedStore(); store != nil {
		row["usage"] = usageWindows(context.Background(), store, key.ID, time.Now())
	}
	return jsonResponse(http.StatusOK, map[string]any{"ok": true, "me": row})
}

func userUsage(req managementRequest) ([]byte, error) {
	key, ok := keyFromSession(req)
	if !ok {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"ok": false, "error": "not_authenticated"})
	}
	store := loadedStore()
	if store == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "store_unavailable"})
	}
	rangeName := req.Query.Get("range")
	if rangeName == "" {
		rangeName = governor.Range24H
	}
	summary, err := store.UsageSummary(context.Background(), key.ID, governor.WindowFor(rangeName, time.Now()))
	if err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
	}
	success := summary.Calls - summary.Failed
	successRate := 0.0
	if summary.Calls > 0 {
		successRate = float64(success) / float64(summary.Calls)
	}
	return jsonResponse(http.StatusOK, map[string]any{
		"ok":     true,
		"range":  rangeName,
		"limits": key.Safe()["limits"],
		"summary": map[string]any{
			"calls":        summary.Calls,
			"success":      success,
			"failed":       summary.Failed,
			"success_rate": successRate,
			"total_cost":   summary.TotalCost,
			"usage":        summary.Usage,
		},
	})
}

func userEvents(req managementRequest) ([]byte, error) {
	key, ok := keyFromSession(req)
	if !ok {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"ok": false, "error": "not_authenticated"})
	}
	return eventsResponseFromRequest(req, key.ID)
}

func userCodexCont(req managementRequest) ([]byte, error) {
	key, ok := keyFromSession(req)
	if !ok {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"ok": false, "error": "not_authenticated"})
	}
	limit := 80
	if rawLimit := strings.TrimSpace(req.Query.Get("limit")); rawLimit != "" {
		if parsed, err := strconv.Atoi(rawLimit); err == nil {
			limit = parsed
		}
	}
	if limit <= 0 || limit > 200 {
		limit = 80
	}
	requests, source := codexRequestsForKey(key, limit)
	sortCodexSummariesNewestFirst(requests)
	return jsonResponse(http.StatusOK, map[string]any{
		"ok":        true,
		"codexcont": codexcontStatus(),
		"requests":  requests,
		"source":    source,
	})
}

func eventsResponse(keyID string, limit int) ([]byte, error) {
	return eventsResponseWithRange(keyID, "", limit)
}

func eventsResponseFromRequest(req managementRequest, keyID string) ([]byte, error) {
	limit := 100
	if rawLimit := strings.TrimSpace(req.Query.Get("limit")); rawLimit != "" {
		if parsed, err := strconv.Atoi(rawLimit); err == nil {
			limit = parsed
		}
	}
	return eventsResponseWithRange(keyID, req.Query.Get("range"), limit)
}

func eventsResponseWithRange(keyID string, rangeName string, limit int) ([]byte, error) {
	store := loadedStore()
	if store == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "store_unavailable"})
	}
	var events []governor.UsageEvent
	var err error
	if strings.TrimSpace(rangeName) == "" {
		events, err = store.RecentEvents(context.Background(), keyID, limit)
	} else {
		events, err = store.RecentEventsWindow(context.Background(), keyID, governor.WindowFor(rangeName, time.Now()), limit)
	}
	if err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
	}
	return jsonResponse(http.StatusOK, map[string]any{"ok": true, "events": events})
}

func usageWindows(ctx context.Context, store *governor.Store, keyID string, now time.Time) map[string]float64 {
	out := map[string]float64{}
	for _, name := range []string{governor.Range5H, governor.Range24H, governor.Range7D, governor.RangeMonth} {
		value, _ := store.UsageSum(ctx, keyID, governor.WindowFor(name, now))
		out[name] = value
	}
	return out
}

func codexRequestsForKey(key governor.KeyRecord, limit int) ([]map[string]any, string) {
	if requests, ok := fetchCodexContRequests(key, limit); ok {
		return requests, "codexcont_admin"
	}
	store := loadedStore()
	if store == nil {
		return []map[string]any{}, "unavailable"
	}
	items, err := store.RecentCodexSummaries(context.Background(), key.ID, limit)
	if err != nil {
		return []map[string]any{}, "store_error"
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		safe := safeCodexSummary(item.Summary, key)
		if safe == nil {
			continue
		}
		out = append(out, safe)
	}
	sortCodexSummariesNewestFirst(out)
	return out, "governor_store"
}

func fetchCodexContRequests(key governor.KeyRecord, limit int) ([]map[string]any, bool) {
	cfg := loadedConfig()
	if !cfg.CodexContEnabled {
		return nil, false
	}
	base := strings.TrimRight(strings.TrimSpace(cfg.CodexContURL), "/")
	if base == "" {
		return nil, false
	}
	parsed, err := url.Parse(base + "/admin/requests?limit=" + strconv.Itoa(limit))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, false
	}
	client := http.Client{Timeout: 1200 * time.Millisecond}
	resp, err := client.Get(parsed.String())
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false
	}
	var body struct {
		Requests []map[string]any `json:"requests"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, false
	}
	out := make([]map[string]any, 0, len(body.Requests))
	for _, req := range body.Requests {
		safe := safeCodexSummary(req, key)
		if safe == nil {
			continue
		}
		out = append(out, safe)
	}
	sortCodexSummariesNewestFirst(out)
	return out, true
}

func sortCodexSummariesNewestFirst(items []map[string]any) {
	sort.SliceStable(items, func(i, j int) bool {
		left, leftOK := codexSummaryDisplayTime(items[i])
		right, rightOK := codexSummaryDisplayTime(items[j])
		if leftOK != rightOK {
			return leftOK
		}
		if !leftOK {
			return false
		}
		return left.After(right)
	})
}

func codexSummaryDisplayTime(req map[string]any) (time.Time, bool) {
	for _, field := range []string{"started_at", "updated_at", "ended_at"} {
		if parsed, ok := parseCodexSummaryTime(req[field]); ok {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func parseCodexSummaryTime(value any) (time.Time, bool) {
	switch v := value.(type) {
	case time.Time:
		if v.IsZero() {
			return time.Time{}, false
		}
		return v, true
	case string:
		raw := strings.TrimSpace(v)
		if raw == "" {
			return time.Time{}, false
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
			if parsed, err := time.Parse(layout, raw); err == nil {
				return parsed, true
			}
		}
		return time.Time{}, false
	case json.Number:
		if asInt, err := v.Int64(); err == nil {
			return unixLikeTime(asInt)
		}
		if asFloat, err := v.Float64(); err == nil {
			return unixLikeTime(int64(asFloat))
		}
	case float64:
		return unixLikeTime(int64(v))
	case int64:
		return unixLikeTime(v)
	case int:
		return unixLikeTime(int64(v))
	}
	return time.Time{}, false
}

func unixLikeTime(raw int64) (time.Time, bool) {
	if raw <= 0 {
		return time.Time{}, false
	}
	if raw > 1_000_000_000_000 {
		return time.UnixMilli(raw), true
	}
	return time.Unix(raw, 0), true
}

func safeCodexSummary(req map[string]any, key governor.KeyRecord) map[string]any {
	if req == nil {
		return nil
	}
	identity, _ := req["key_identity"].(map[string]any)
	if !codexIdentityMatches(identity, key) {
		return nil
	}
	fields := []string{
		"request_id", "model", "path", "started_at", "updated_at", "ended_at",
		"duration_ms", "status", "protection", "latest_round",
		"latest_reasoning_tokens", "first_truncation_round",
		"first_truncation_reasoning_tokens", "first_truncation_decision",
		"continuation_count", "stopped_reason", "failure_reason",
		"passthrough_reason", "rounds",
	}
	out := map[string]any{}
	for _, field := range fields {
		if value, ok := req[field]; ok {
			out[field] = value
		}
	}
	out["key_identity"] = key.Safe()
	return out
}

func codexIdentityMatches(identity map[string]any, key governor.KeyRecord) bool {
	if strings.TrimSpace(key.ID) == "" || identity == nil {
		return false
	}
	id := strings.TrimSpace(fmt.Sprint(identity["id"]))
	if id != "" && id == key.ID {
		return true
	}
	preview := strings.TrimSpace(fmt.Sprint(identity["preview"]))
	return preview != "" && preview == key.Preview
}

func forwardHostStream(targetStreamID string, sourceStreamID string) {
	defer func() {
		_, _ = callHost(methodHostModelStreamClose, hostModelStreamCloseRequest{StreamID: sourceStreamID})
		_, _ = callHost(methodHostStreamClose, hostStreamCloseRequest{StreamID: targetStreamID})
	}()
	for {
		result, err := callHost(methodHostModelStreamRead, hostModelStreamReadRequest{StreamID: sourceStreamID})
		if err != nil {
			_, _ = callHost(methodHostStreamEmit, hostStreamEmitRequest{
				StreamID: targetStreamID,
				Error:    governor.Brief(err.Error(), 400),
			})
			return
		}
		var chunk hostModelStreamReadResponse
		if err := json.Unmarshal(result, &chunk); err != nil {
			_, _ = callHost(methodHostStreamEmit, hostStreamEmitRequest{
				StreamID: targetStreamID,
				Error:    "decode host stream chunk: " + governor.Brief(err.Error(), 300),
			})
			return
		}
		if len(chunk.Payload) > 0 {
			_, _ = callHost(methodHostStreamEmit, hostStreamEmitRequest{
				StreamID: targetStreamID,
				Payload:  chunk.Payload,
			})
		}
		if chunk.Error != "" {
			_, _ = callHost(methodHostStreamEmit, hostStreamEmitRequest{
				StreamID: targetStreamID,
				Error:    governor.Brief(chunk.Error, 400),
			})
			return
		}
		if chunk.Done {
			return
		}
	}
}

func keyFromSession(req managementRequest) (governor.KeyRecord, bool) {
	_ = refreshKeyPolicyState(false)
	cookie := headerFirst(req.Headers, "Cookie")
	token := ""
	for _, part := range strings.Split(cookie, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "cpa_governor_session=") {
			token = strings.TrimPrefix(part, "cpa_governor_session=")
			break
		}
	}
	cfg := loadedConfig()
	payload, ok := governor.VerifySession(token, cfg.SessionSecret, time.Now())
	if !ok {
		return governor.KeyRecord{}, false
	}
	store := loadedStore()
	if store == nil {
		return governor.KeyRecord{}, false
	}
	keys, err := store.ListKeys(context.Background())
	if err != nil {
		return governor.KeyRecord{}, false
	}
	for _, key := range keys {
		if key.ID == payload.KeyID && key.Enabled {
			return key, true
		}
	}
	return governor.KeyRecord{}, false
}

func codexcontStatus() map[string]any {
	cfg := loadedConfig()
	status := map[string]any{
		"enabled":   cfg.CodexContEnabled,
		"route":     cfg.CodexContRoute,
		"url":       cfg.CodexContURL,
		"fail_mode": cfg.FailMode,
		"mode":      "passive_until_executor_cutover",
	}
	if cfg.CodexContEnabled {
		health := probeCodexContHealth(cfg.CodexContURL)
		for key, value := range health {
			status[key] = value
		}
	}
	return status
}

func probeCodexContHealth(baseURL string) map[string]any {
	out := map[string]any{
		"health_ok": false,
	}
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/") + "/engine/healthz")
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		out["health_error"] = "invalid_engine_url"
		return out
	}
	client := http.Client{Timeout: 800 * time.Millisecond}
	resp, err := client.Get(parsed.String())
	if err != nil {
		out["health_error"] = governor.Brief(err.Error(), 200)
		return out
	}
	defer resp.Body.Close()
	out["health_status"] = resp.StatusCode
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		out["health_ok"] = true
	}
	return out
}

func bearer(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(strings.ToLower(value), "bearer ") {
		return strings.TrimSpace(value[7:])
	}
	return ""
}

func headerFirst(headers http.Header, name string) string {
	if headers == nil {
		return ""
	}
	if value := strings.TrimSpace(headers.Get(name)); value != "" {
		return value
	}
	for key, values := range headers {
		if !strings.EqualFold(key, name) {
			continue
		}
		for _, value := range values {
			if strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
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

func cloneHeader(headers http.Header) http.Header {
	if headers == nil {
		return nil
	}
	cloned := make(http.Header, len(headers))
	for key, values := range headers {
		cloned[key] = append([]string(nil), values...)
	}
	return cloned
}

func cloneValues(values map[string][]string) map[string][]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string][]string, len(values))
	for key, items := range values {
		cloned[key] = append([]string(nil), items...)
	}
	return cloned
}

func okEnvelope(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

func jsonResponse(status int, v any) ([]byte, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return okEnvelope(managementResponse{
		StatusCode: status,
		Headers: http.Header{
			"Content-Type":  []string{"application/json; charset=utf-8"},
			"Cache-Control": []string{"no-store"},
		},
		Body: body,
	})
}

func managementHTML(html string) ([]byte, error) {
	return okEnvelope(managementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":  []string{"text/html; charset=utf-8"},
			"Cache-Control": []string{"no-store"},
		},
		Body: []byte(html),
	})
}

var hostCall = func(method string, payload any) (json.RawMessage, error) {
	_ = payload
	return nil, fmt.Errorf("host callback %s is unavailable", method)
}

func callHost(method string, payload any) (json.RawMessage, error) {
	return hostCall(method, payload)
}

func adminHTML() string {
	return renderHTML(adminHTMLTemplate)
}

func userHTML() string {
	return renderHTML(userHTMLTemplate)
}

func sharedCSS() string {
	return sharedCSSTemplate
}

func renderHTML(tpl string) string {
	return strings.ReplaceAll(tpl, "{{CSS}}", sharedCSS())
}
