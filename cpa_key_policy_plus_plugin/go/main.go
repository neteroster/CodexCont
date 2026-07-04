package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"codexcont/cpa-key-policy-plus-plugin/internal/policyplus"
	_ "embed"
	"gopkg.in/yaml.v3"
)

const (
	pluginID                     = "cpa-key-policy-plus"
	plusSessionCookieName        = "cpa_key_policy_plus_session"
	executorModelScopeBoth       = "both"
	executorFormatOpenAIResponse = "openai-response"
	policyDenyMetadataPrefix     = "policy_deny_"
)

var executorUsageModelAliases = map[string]string{
	"gpt-5.3-codex-spark": "gpt-5.4",
}

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
	cfg               policyplus.Config
	store             *policyplus.Store
	keyState          policyplus.KeyPolicyState
	keyStatePath      string
	keyStateModTime   time.Time
	keyStateLastCheck time.Time
	rpmBuckets        map[string][]time.Time
}

type policyDecision struct {
	Allowed    bool
	StatusCode int
	Type       string
	Code       string
	Message    string
	Param      string
	Window     string
	UsedUSD    float64
	LimitUSD   float64
	UsedCount  int
	LimitCount int
	KeyID      string
	KeyName    string
}

var state = runtimeState{
	rpmBuckets: map[string][]time.Time{},
}

func main() { runPreviewIfRequested() }

func runPreviewIfRequested() {
	addr := strings.TrimSpace(os.Getenv("CPA_KEY_POLICY_PLUS_PREVIEW_ADDR"))
	if addr == "" {
		return
	}
	cfg := policyplus.DefaultConfig()
	previewDir, err := os.MkdirTemp("", "cpa-key-policy-plus-preview-*")
	if err != nil {
		panic(err)
	}
	cfg.StateDBPath = previewDir + "/policyplus.sqlite"
	cfg.SessionSecret = "preview-secret"
	cfg.CodexContEnabled = true
	cfg.CodexContURL = "http://" + addr
	store, err := policyplus.OpenStore(cfg.StateDBPath)
	if err != nil {
		panic(err)
	}
	previewRawKey := "cpa_preview_abcdefghijklmnopqrstuvwxyz0123456789AB"
	previewKey := policyplus.KeyRecord{
		ID:              "preview-key",
		Name:            "演示用户",
		KeyHash:         "sha256:" + policyplus.SHA256Hex(previewRawKey),
		Enabled:         true,
		Preview:         policyplus.HashPreview(policyplus.SHA256Hex(previewRawKey)),
		RPM:             60,
		Concurrency:     0,
		Models:          []string{"gpt-5.5", "gpt-5.4"},
		FiveHourUSD:     floatPtr(2),
		DailyLimitUSD:   floatPtr(8),
		WeeklyLimitUSD:  floatPtr(40),
		MonthlyLimitUSD: floatPtr(120),
		Prices: map[string]policyplus.ModelPrice{
			"gpt-5.5": {Model: "gpt-5.5", InputPerMillion: 5, OutputPerMillion: 30, CacheReadPerMillion: 0.5},
		},
	}
	disabledKey := policyplus.KeyRecord{
		ID:                "preview-disabled",
		Name:              "禁用演示",
		KeyHash:           "sha256:" + policyplus.SHA256Hex("cpa_preview_disabled_abcdefghijklmnopqrstuvwxyz0123"),
		Enabled:           false,
		Preview:           policyplus.HashPreview(policyplus.SHA256Hex("cpa_preview_disabled_abcdefghijklmnopqrstuvwxyz0123")),
		RPM:               30,
		Concurrency:       0,
		MaxActiveSessions: 0,
		Models:            []string{"gpt-5.4-mini"},
		DailyLimitUSD:     floatPtr(3),
	}
	noLimitKey := policyplus.KeyRecord{
		ID:                "preview-no-limit",
		Name:              "无限额演示",
		KeyHash:           "sha256:" + policyplus.SHA256Hex("cpa_preview_nolimit_abcdefghijklmnopqrstuvwxyz0123"),
		Enabled:           true,
		Preview:           policyplus.HashPreview(policyplus.SHA256Hex("cpa_preview_nolimit_abcdefghijklmnopqrstuvwxyz0123")),
		RPM:               0,
		Concurrency:       0,
		MaxActiveSessions: 0,
	}
	_ = store.UpsertKey(context.Background(), previewKey)
	_ = store.UpsertKey(context.Background(), disabledKey)
	_ = store.UpsertKey(context.Background(), noLimitKey)
	_ = store.InsertUsage(context.Background(), policyplus.UsageEvent{
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
		Usage: policyplus.TokenUsage{
			InputTokens:     120000,
			CachedTokens:    103000,
			OutputTokens:    2100,
			ReasoningTokens: 516,
			TotalTokens:     122100,
		},
		Cost: 0.151,
		CostBreakdown: policyplus.CostForUsage(previewKey.Prices["gpt-5.5"], policyplus.TokenUsage{
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
	_ = store.InsertUsage(context.Background(), policyplus.UsageEvent{
		RequestID:   "req-preview-disabled",
		KeyID:       disabledKey.ID,
		KeyPreview:  disabledKey.Preview,
		Model:       "gpt-5.4-mini",
		RequestedAt: time.Now().Add(-25 * time.Minute),
		Cost:        0.18,
	})
	state.mu.Lock()
	state.cfg = cfg
	state.store = store
	state.keyState = policyplus.KeyPolicyState{Keys: []policyplus.KeyRecord{previewKey, disabledKey, noLimitKey}}
	state.rpmBuckets = map[string][]time.Time{}
	state.mu.Unlock()
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/resource/plugins/cpa-key-policy-plus/admin", func(w http.ResponseWriter, r *http.Request) {
		_ = r
		w.Header().Set("content-type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(adminHTML()))
	})
	mux.HandleFunc("/v0/resource/plugins/cpa-key-policy-plus/user", func(w http.ResponseWriter, r *http.Request) {
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
		body, _ := io.ReadAll(r.Body)
		rawReq, _ := json.Marshal(managementRequest{
			Method:  r.Method,
			Path:    r.URL.Path,
			Headers: r.Header,
			Query:   r.URL.Query(),
			Body:    body,
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
	cfg := policyplus.DefaultConfig()
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
	store, err := policyplus.OpenStore(cfg.StateDBPath)
	if err != nil {
		return err
	}
	cfg = applyStoredSettings(cfg, store)
	var keyState policyplus.KeyPolicyState
	if strings.TrimSpace(cfg.KeyPolicyStatePath) != "" {
		loaded, err := policyplus.LoadKeyPolicyState(cfg.KeyPolicyStatePath)
		if err == nil {
			keyState = loaded
			_ = store.ImportKeys(context.Background(), loaded)
		}
	}
	importLegacySQLite(store, cfg.LegacyQuotaDBPath, "usage-admin")
	importLegacySQLite(store, cfg.GovernorStateDBPath, "governor")
	syncNativeKeysFromConfig(store, cfg)
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
	_ = syncNativeKeysFromLoadedConfig()
	return nil
}

func syncNativeKeysFromLoadedConfig() error {
	state.mu.RLock()
	cfg := state.cfg
	store := state.store
	state.mu.RUnlock()
	return syncNativeKeysFromConfig(store, cfg)
}

func syncNativeKeysFromConfig(store *policyplus.Store, cfg policyplus.Config) error {
	if store == nil || strings.TrimSpace(cfg.NativeKeysConfigPath) == "" {
		return nil
	}
	rawKeys, err := policyplus.LoadNativeKeysFromCPAConfig(cfg.NativeKeysConfigPath)
	if err != nil {
		_ = store.Audit(context.Background(), "system", "native_key_sync_failed", "cpa_config", map[string]any{"error": policyplus.Brief(err.Error(), 240)})
		return err
	}
	aliasPaths := cfg.AliasDBPaths()
	aliases, aliasErrors, err := policyplus.LoadAPIKeyAliasesFromSQLitePaths(context.Background(), aliasPaths)
	if len(aliasErrors) > 0 {
		_ = store.Audit(context.Background(), "system", "native_alias_read_partial", "cpamp", map[string]any{"errors": aliasErrors})
	}
	if err != nil && len(aliasPaths) > 0 {
		_ = store.Audit(context.Background(), "system", "native_alias_read_failed", "cpamp", map[string]any{"error": policyplus.Brief(err.Error(), 240)})
	}
	inputs := make([]policyplus.NativeKeySyncInput, 0, len(rawKeys))
	for _, rawKey := range rawKeys {
		hash := policyplus.SHA256Hex(rawKey)
		alias := aliases[hash]
		inputs = append(inputs, policyplus.NativeKeySyncInput{RawKey: rawKey, Alias: alias})
	}
	if err := store.SyncNativeKeys(context.Background(), inputs); err != nil {
		_ = store.Audit(context.Background(), "system", "native_key_sync_failed", "store", map[string]any{"error": policyplus.Brief(err.Error(), 240)})
		return err
	}
	return nil
}

func applyStoredSettings(cfg policyplus.Config, store *policyplus.Store) policyplus.Config {
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

func importLegacySQLite(store *policyplus.Store, path, source string) {
	path = strings.TrimSpace(path)
	if store == nil || path == "" {
		return
	}
	if _, err := os.Stat(path); err != nil {
		_ = store.Audit(context.Background(), "system", "legacy_import_skipped", source, map[string]any{
			"path":  path,
			"error": policyplus.Brief(err.Error(), 240),
		})
		return
	}
	result, err := store.ImportLegacyQuotaSQLite(context.Background(), path, source)
	if err != nil {
		_ = store.Audit(context.Background(), "system", "legacy_import_failed", source, map[string]any{
			"path":  path,
			"error": policyplus.Brief(err.Error(), 240),
		})
		return
	}
	if result.Limits > 0 || result.Resets > 0 {
		_ = store.Audit(context.Background(), "system", "legacy_import_completed", source, map[string]any{
			"path":   path,
			"limits": result.Limits,
			"resets": result.Resets,
		})
	}
}

func pluginRegistration() registration {
	cfg := loadedConfig()
	return registration{
		SchemaVersion: schemaVersion,
		Metadata: metadata{
			Name:             "cpa-key-policy-plus",
			Version:          "0.1.0",
			Author:           "konbakuyomu/CodexCont",
			GitHubRepository: "https://local/CodexCont",
			ConfigFields: []configField{
				{Name: "enabled", Type: configBoolean, Description: "Enable Key Policy Plus user-key authentication and management surfaces."},
				{Name: "exclusive_auth", Type: configBoolean, Description: "When true, Key Policy Plus is the exclusive frontend auth provider for user keys."},
				{Name: "state_db_path", Type: configString, Description: "SQLite path for Key Policy Plus state."},
				{Name: "key_policy_state_path", Type: configString, Description: "Optional old CPA Key Policy state JSON path to import once or mirror during cutover."},
				{Name: "legacy_quota_db_path", Type: configString, Description: "Optional old usage-admin SQLite path for one-way 5H/month limit and reset import."},
				{Name: "governor_state_db_path", Type: configString, Description: "Optional old Governor SQLite path for one-way limit and reset import."},
				{Name: "codex_summary_db_path", Type: configString, Description: "Optional read-only CodexCont executor SQLite path for safe protection summaries."},
				{Name: "native_keys_config_path", Type: configString, Description: "Optional CPA config YAML path whose top-level api-keys are synced as native policy keys."},
				{Name: "cpamp_alias_db_path", Type: configString, Description: "Optional CPAMP manager SQLite path for read-only api_key_aliases lookup."},
				{Name: "cpamp_alias_db_paths", Type: configString, Description: "Optional comma/semicolon-separated fallback CPAMP SQLite paths for read-only api_key_aliases lookup."},
				{Name: "session_secret", Type: configString, Description: "Secret used to sign user portal sessions."},
				{Name: "codexcont_enabled", Type: configBoolean, Description: "Enable CodexCont status lookup for user summaries."},
				{Name: "codexcont_route", Type: configBoolean, Description: "Deprecated in Key Policy Plus; keep false and let Governor own CodexCont routing."},
				{Name: "codexcont_url", Type: configString, Description: "Internal CodexCont engine base URL."},
				{Name: "fail_mode", Type: configEnum, EnumValues: []string{"fallback", "fail_closed"}, Description: "Behavior when engine is unavailable."},
			},
		},
		Capabilities: capabilities{
			FrontendAuthProvider:          true,
			FrontendAuthProviderExclusive: cfg.ExclusiveAuth,
			ModelRouter:                   true,
			Executor:                      true,
			ExecutorModelScope:            executorModelScopeBoth,
			ExecutorInputFormats:          []string{executorFormatOpenAIResponse},
			ExecutorOutputFormats:         []string{executorFormatOpenAIResponse},
			UsagePlugin:                   true,
			ManagementAPI:                 true,
		},
	}
}

func loadedConfig() policyplus.Config {
	state.mu.RLock()
	defer state.mu.RUnlock()
	if state.cfg.StateDBPath == "" {
		return policyplus.DefaultConfig()
	}
	return state.cfg
}

func loadedStore() *policyplus.Store {
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
	model := requestedModelFromBody(req.Body)
	record, decision, ok := policyDecisionForRawKey(key, model, true)
	if !ok {
		return okEnvelope(frontendAuthResponse{Authenticated: false})
	}
	metadata := authMetadata(record)
	if !decision.Allowed {
		if !shouldSurfacePolicyDeny(req) {
			return okEnvelope(frontendAuthResponse{Authenticated: false})
		}
		metadata = decisionMetadata(metadata, decision)
	}
	return okEnvelope(frontendAuthResponse{
		Authenticated: true,
		Principal:     record.ID,
		Metadata:      metadata,
	})
}

func shouldSurfacePolicyDeny(req frontendAuthRequest) bool {
	path := strings.ToLower(strings.TrimSpace(req.Path))
	if strings.Contains(path, "/v1/responses") || strings.Contains(path, "/responses") {
		return true
	}
	return requestedModelFromBody(req.Body) != ""
}

func policyDecisionForRawKey(rawKey, model string, consumeRPM bool) (policyplus.KeyRecord, policyDecision, bool) {
	submitted := policyplus.NormalizeSubmittedKey(rawKey)
	if strings.HasPrefix(strings.ToLower(submitted), "cpa_") {
		return policyplus.KeyRecord{}, policyDecision{}, false
	}
	record, ok := findKeyByRaw(rawKey)
	if ok {
		return record, evaluatePolicy(record, model, consumeRPM), true
	}
	if !isNativeSubmittedKey(submitted) {
		return policyplus.KeyRecord{}, policyDecision{}, false
	}
	hash := policyplus.SHA256Hex(submitted)
	preview := policyplus.HashPreview(hash)
	record = policyplus.KeyRecord{
		ID:            policyplus.NativeKeyIDFromHash(hash),
		Name:          preview,
		KeyHash:       "sha256:" + hash,
		Preview:       preview,
		Source:        policyplus.NativeCPASource,
		SourcePresent: false,
	}
	base := policyDecision{
		Allowed:    true,
		StatusCode: http.StatusOK,
		KeyID:      record.ID,
		KeyName:    preview,
	}
	decision := denyDecision(base, "invalid_request_error", "policy_missing", "policy_missing", "", fmt.Sprintf("CPA Key Policy+ 已拦截：%s 没有对应的 Plus 策略，请先在管理页同步并启用策略。", preview))
	return record, decision, true
}

func isNativeSubmittedKey(key string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	return strings.HasPrefix(lower, "sk-") || strings.HasPrefix(lower, "sk_")
}

func authMetadata(record policyplus.KeyRecord) map[string]string {
	return map[string]string{
		"provider":       "cpa-key-policy-plus",
		"key_id":         record.ID,
		"key_name":       record.Name,
		"preview":        record.Preview,
		"source":         record.Source,
		"source_present": strconv.FormatBool(record.SourcePresent),
	}
}

func decisionMetadata(metadata map[string]string, decision policyDecision) map[string]string {
	out := map[string]string{}
	for k, v := range metadata {
		out[k] = v
	}
	out[policyDenyMetadataPrefix+"code"] = decision.Code
	out[policyDenyMetadataPrefix+"message"] = decision.Message
	out[policyDenyMetadataPrefix+"window"] = decision.Window
	out[policyDenyMetadataPrefix+"param"] = decision.Param
	return out
}

func findKeyByRaw(rawKey string) (policyplus.KeyRecord, bool) {
	_ = syncNativeKeysFromLoadedConfig()
	_ = refreshKeyPolicyState(false)
	if key, ok := lookupKeyByRaw(rawKey); ok {
		return key, true
	}
	if strings.HasPrefix(strings.ToLower(policyplus.NormalizeSubmittedKey(rawKey)), "cpa_") {
		_ = refreshKeyPolicyState(true)
		return lookupKeyByRaw(rawKey)
	}
	return policyplus.KeyRecord{}, false
}

func lookupKeyByRaw(rawKey string) (policyplus.KeyRecord, bool) {
	state.mu.RLock()
	keyState := state.keyState
	store := state.store
	state.mu.RUnlock()
	if store != nil {
		key, ok, err := store.FindKeyByHash(context.Background(), policyplus.SHA256Hex(rawKey))
		if err == nil && ok {
			return key, true
		}
	}
	if key, ok := keyState.FindByRawKey(rawKey); ok {
		return key, true
	}
	return policyplus.KeyRecord{}, false
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
	loaded, err := policyplus.LoadKeyPolicyState(path)
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
	if !cfg.Enabled {
		return okEnvelope(modelRouteResponse{Handled: false})
	}
	decision, ok := policyDecisionForHeaders(req.Headers, firstNonEmpty(req.RequestedModel, requestedModelFromBody(req.Body)), false)
	if !ok || decision.Allowed {
		return okEnvelope(modelRouteResponse{Handled: false})
	}
	return okEnvelope(modelRouteResponse{
		Handled:    true,
		TargetKind: routeTargetSelf,
		Reason:     "cpa_key_policy_plus_policy_denied",
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

func policyDecisionForHeaders(headers http.Header, model string, consumeRPM bool) (policyDecision, bool) {
	rawKey := bearer(headers.Get("Authorization"))
	if rawKey == "" {
		return policyDecision{}, false
	}
	_, decision, ok := policyDecisionForRawKey(rawKey, model, consumeRPM)
	if !ok {
		return policyDecision{}, false
	}
	return decision, true
}

func evaluatePolicy(key policyplus.KeyRecord, model string, consumeRPM bool) policyDecision {
	base := policyDecision{
		Allowed:    true,
		StatusCode: http.StatusOK,
		KeyID:      key.ID,
		KeyName:    safeKeyDisplayName(key),
	}
	if key.ID == "" {
		return denyDecision(base, "invalid_request_error", "missing_policy", "policy_missing", "", "CPA Key Policy+ 已拦截：当前 Key 没有对应的 Plus 策略。")
	}
	if key.Source == policyplus.NativeCPASource && !key.SourcePresent {
		return denyDecision(base, "invalid_request_error", "api_key_source_removed", "source_removed", "", fmt.Sprintf("CPA Key Policy+ 已拦截：%s 已从 CPA 官方 Key 列表移除。", safeKeyDisplayName(key)))
	}
	if !key.Enabled || key.Archived {
		return denyDecision(base, "invalid_request_error", "api_key_disabled", "disabled", "", fmt.Sprintf("CPA Key Policy+ 已拦截：%s 当前已禁用。", safeKeyDisplayName(key)))
	}
	if model != "" && !policyplus.ModelAllowed(key.Models, model) {
		return denyDecision(base, "invalid_request_error", "model_not_allowed", "model", "", fmt.Sprintf("CPA Key Policy+ 已拦截：%s 不允许使用模型 %s。", safeKeyDisplayName(key), model))
	}
	if rpm := checkRPM(key, consumeRPM); !rpm.Allowed {
		base.UsedCount = rpm.Used
		base.LimitCount = rpm.Limit
		base.Window = "rpm"
		return denyDecision(base, "rate_limit_exceeded", "rpm_rate_limit_exceeded", "rpm", "", fmt.Sprintf("CPA Key Policy+ 已拦截：%s 触发 RPM 限制，最近 1 分钟请求 %d / 上限 %d。", safeKeyDisplayName(key), rpm.Used, rpm.Limit))
	}
	if quota := checkQuota(key); !quota.Allowed {
		base.Window = quota.Window
		base.Param = quota.Window
		base.UsedUSD = quota.Used
		base.LimitUSD = quota.Limit
		return denyDecision(base, "rate_limit_exceeded", quota.Code, quota.Window, quota.Window, fmt.Sprintf("CPA Key Policy+ 已拦截：%s 触发 %s费用限额，已用 $%.2f / 上限 $%.2f。", safeKeyDisplayName(key), windowDisplayName(quota.Window), quota.Used, quota.Limit))
	}
	return base
}

func evaluateIdentityPolicy(key policyplus.KeyRecord) policyDecision {
	base := policyDecision{
		Allowed:    true,
		StatusCode: http.StatusOK,
		KeyID:      key.ID,
		KeyName:    safeKeyDisplayName(key),
	}
	if key.ID == "" {
		return denyDecision(base, "invalid_request_error", "missing_policy", "policy_missing", "", "CPA Key Policy+ 已拦截：当前 Key 没有对应的 Plus 策略。")
	}
	if key.Source == policyplus.NativeCPASource && !key.SourcePresent {
		return denyDecision(base, "invalid_request_error", "api_key_source_removed", "source_removed", "", fmt.Sprintf("CPA Key Policy+ 已拦截：%s 已从 CPA 官方 Key 列表移除。", safeKeyDisplayName(key)))
	}
	if !key.Enabled || key.Archived {
		return denyDecision(base, "invalid_request_error", "api_key_disabled", "disabled", "", fmt.Sprintf("CPA Key Policy+ 已拦截：%s 当前已禁用。", safeKeyDisplayName(key)))
	}
	return base
}

func denyDecision(base policyDecision, typ, code, reason, param, message string) policyDecision {
	base.Allowed = false
	base.StatusCode = http.StatusTooManyRequests
	base.Type = typ
	base.Code = code
	base.Param = param
	if base.Param == "" {
		base.Param = reason
	}
	base.Message = message
	return base
}

func safeKeyDisplayName(key policyplus.KeyRecord) string {
	for _, value := range []string{key.Name, key.Alias, key.Preview, key.ID} {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return "当前 Key"
}

type rpmDecision struct {
	Allowed bool
	Used    int
	Limit   int
}

func checkRPM(key policyplus.KeyRecord, consume bool) rpmDecision {
	if key.RPM <= 0 {
		return rpmDecision{Allowed: true}
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
		return rpmDecision{Allowed: false, Used: len(kept), Limit: key.RPM}
	}
	if consume {
		state.rpmBuckets[key.ID] = append(kept, now)
	} else {
		state.rpmBuckets[key.ID] = kept
	}
	return rpmDecision{Allowed: true, Used: len(kept), Limit: key.RPM}
}

type quotaDecision struct {
	Allowed bool
	Window  string
	Used    float64
	Limit   float64
	Code    string
}

func checkQuota(key policyplus.KeyRecord) quotaDecision {
	store := loadedStore()
	if store == nil {
		return quotaDecision{Allowed: true}
	}
	ctx := context.Background()
	now := time.Now()
	limits := []struct {
		name  string
		limit *float64
		code  string
	}{
		{policyplus.Range5H, key.FiveHourUSD, "five_hour_quota_exceeded"},
		{policyplus.Range24H, key.DailyLimitUSD, "daily_quota_exceeded"},
		{policyplus.Range7D, key.WeeklyLimitUSD, "weekly_quota_exceeded"},
		{policyplus.RangeMonth, key.MonthlyLimitUSD, "monthly_quota_exceeded"},
	}
	for _, item := range limits {
		if item.limit == nil {
			continue
		}
		used, err := store.UsageSum(ctx, key.ID, policyplus.WindowFor(item.name, now))
		if err != nil {
			continue
		}
		if !policyplus.CheckLimit(used, item.limit).Allowed {
			return quotaDecision{Allowed: false, Window: item.name, Used: used, Limit: *item.limit, Code: item.code}
		}
	}
	return quotaDecision{Allowed: true}
}

func windowDisplayName(window string) string {
	switch window {
	case policyplus.Range5H:
		return "5小时"
	case policyplus.Range24H:
		return "24小时"
	case policyplus.Range7D:
		return "7天"
	case policyplus.RangeMonth:
		return "本月"
	default:
		return window
	}
}

func executorUnavailable() ([]byte, error) {
	cfg := loadedConfig()
	if cfg.CodexContRoute && cfg.FailMode == "fail_closed" {
		return errorEnvelope("codexcont_executor_pending", "CPA Key Policy+ protected executor is not enabled in this build"), nil
	}
	return errorEnvelope("codexcont_executor_pending", "CPA Key Policy+ protected executor is in passive mode; disable codexcont_enabled to route through CPA provider path"), nil
}

func executorExecute(raw []byte) ([]byte, error) {
	var req executorCallRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	execReq := normalizedExecutorRequest(req)
	if decision, ok := policyDenyForExecutor(execReq); ok {
		return okEnvelope(policyDenyExecutorResponse(decision))
	}
	cfg := loadedConfig()
	if !cfg.CodexContRoute {
		return executorUnavailable()
	}
	payload := execReq.Payload
	if len(payload) == 0 {
		payload = execReq.OriginalRequest
	}
	result, err := callHost(methodHostModelExecute, hostModelExecutionRequest{
		EntryProtocol:  firstNonEmpty(execReq.SourceFormat, "openai"),
		ExitProtocol:   firstNonEmpty(execReq.Format, "openai"),
		Model:          execReq.Model,
		Stream:         false,
		Body:           payload,
		Headers:        cloneHeader(execReq.Headers),
		Query:          cloneValues(execReq.Query),
		Alt:            execReq.Alt,
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
	execReq := normalizedExecutorRequest(req)
	if decision, ok := policyDenyForExecutor(execReq); ok {
		if strings.TrimSpace(req.StreamID) == "" {
			return okEnvelope(executorStreamResponse{Headers: policyDenyHeaders(decision)})
		}
		go emitPolicyDenyStream(req.StreamID, decision)
		return okEnvelope(executorStreamResponse{Headers: policyDenyHeaders(decision)})
	}
	cfg := loadedConfig()
	if !cfg.CodexContRoute {
		return executorUnavailable()
	}
	if strings.TrimSpace(req.StreamID) == "" {
		return errorEnvelope("stream_id_required", "stream_id is required for executor.execute_stream"), nil
	}
	payload := execReq.Payload
	if len(payload) == 0 {
		payload = execReq.OriginalRequest
	}
	result, err := callHost(methodHostModelExecuteStream, hostModelExecutionRequest{
		EntryProtocol:  firstNonEmpty(execReq.SourceFormat, "openai"),
		ExitProtocol:   firstNonEmpty(execReq.Format, "openai"),
		Model:          execReq.Model,
		Stream:         true,
		Body:           payload,
		Headers:        cloneHeader(execReq.Headers),
		Query:          cloneValues(execReq.Query),
		Alt:            execReq.Alt,
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

func policyDenyForExecutor(req executorRequest) (policyDecision, bool) {
	model := firstNonEmpty(req.Model, requestedModelFromBody(req.OriginalRequest), requestedModelFromBody(req.Payload))
	decision, ok := policyDecisionForHeaders(req.Headers, model, false)
	if !ok || decision.Allowed {
		return policyDecision{}, false
	}
	return decision, true
}

func normalizedExecutorRequest(req executorCallRequest) executorRequest {
	if !isZeroExecutorRequest(req.executorRequest) {
		return req.executorRequest
	}
	return req.NestedExecutorRequest
}

func isZeroExecutorRequest(req executorRequest) bool {
	return strings.TrimSpace(req.AuthID) == "" &&
		strings.TrimSpace(req.AuthProvider) == "" &&
		strings.TrimSpace(req.Model) == "" &&
		strings.TrimSpace(req.Format) == "" &&
		!req.Stream &&
		strings.TrimSpace(req.Alt) == "" &&
		len(req.Headers) == 0 &&
		len(req.Query) == 0 &&
		len(req.OriginalRequest) == 0 &&
		strings.TrimSpace(req.SourceFormat) == "" &&
		len(req.Payload) == 0 &&
		len(req.Metadata) == 0 &&
		len(req.StorageJSON) == 0 &&
		len(req.AuthMetadata) == 0 &&
		len(req.AuthAttributes) == 0
}

func policyDenyExecutorResponse(decision policyDecision) executorResponse {
	return executorResponse{
		Payload: policyDenyBody(decision),
		Headers: policyDenyHeaders(decision),
		Metadata: map[string]any{
			"policy_denied": true,
			"code":          decision.Code,
			"window":        decision.Window,
		},
	}
}

func policyDenyHeaders(decision policyDecision) http.Header {
	headers := http.Header{}
	headers.Set("Content-Type", "application/json; charset=utf-8")
	headers.Set("X-CPA-Policy-Reason", decision.Code)
	if window := firstNonEmpty(decision.Window, decision.Param); window != "" {
		headers.Set("X-CPA-Policy-Window", window)
	}
	headers.Set("Retry-After", "60")
	return headers
}

func policyDenyBody(decision policyDecision) []byte {
	body, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": decision.Message,
			"type":    firstNonEmpty(decision.Type, "rate_limit_exceeded"),
			"code":    decision.Code,
			"param":   decision.Param,
		},
	})
	return body
}

func emitPolicyDenyStream(streamID string, decision policyDecision) {
	defer func() {
		_, _ = callHost(methodHostStreamClose, hostStreamCloseRequest{StreamID: streamID})
	}()
	payload := append([]byte("event: error\ndata: "), policyDenyBody(decision)...)
	payload = append(payload, []byte("\n\n")...)
	_, _ = callHost(methodHostStreamEmit, hostStreamEmitRequest{
		StreamID: streamID,
		Payload:  payload,
	})
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
	keyByID := map[string]policyplus.KeyRecord{}
	for _, key := range keys {
		keyByID[key.ID] = key
	}
	key := keyByID[rec.AuthID]
	if key.ID == "" {
		key = keyByID[rec.APIKey]
	}
	if key.ID == "" {
		key = keyByID[rec.Source]
	}
	visibleModel := visibleUsageModel(key, rec)
	usage := policyplus.TokenUsage{
		InputTokens:         rec.Detail.InputTokens,
		OutputTokens:        rec.Detail.OutputTokens,
		CachedTokens:        rec.Detail.CachedTokens,
		CacheReadTokens:     rec.Detail.CacheReadTokens,
		CacheCreationTokens: rec.Detail.CacheCreationTokens,
		ReasoningTokens:     rec.Detail.ReasoningTokens,
		TotalTokens:         rec.Detail.TotalTokens,
	}
	var cost float64
	var breakdown policyplus.CostBreakdown
	if key.ID != "" {
		if price, ok := policyplus.PriceForModel(key.Prices, visibleModel); ok {
			breakdown = policyplus.CostForUsage(price, usage, visibleModel)
			cost = breakdown.Costs["total"]
		}
	}
	event := policyplus.UsageEvent{
		RequestID:       firstNonEmpty(rec.ResponseHeaders.Get("x-request-id"), rec.ResponseHeaders.Get("x-openai-request-id")),
		KeyID:           key.ID,
		KeyPreview:      key.Preview,
		Model:           visibleModel,
		RequestedModel:  visibleModel,
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
		Failure:         policyplus.Brief(rec.Failure.Body, 600),
		Usage:           usage,
		Cost:            cost,
		CostBreakdown:   breakdown,
	}
	_ = store.InsertUsage(context.Background(), event)
	return okEnvelope(map[string]any{})
}

func visibleUsageModel(key policyplus.KeyRecord, rec usageRecord) string {
	if model := strings.TrimSpace(rec.Alias); model != "" {
		if alias := visibleExecutorAliasForReportedModel(model); alias != "" {
			return alias
		}
		return model
	}
	reported := strings.TrimSpace(rec.Model)
	if reported == "" || key.ID == "" {
		return reported
	}
	allowed := cleanStrings(key.Models)
	for _, model := range allowed {
		if strings.EqualFold(model, reported) {
			return reported
		}
	}
	if alias := visibleExecutorAliasForReportedModel(reported); alias != "" {
		return alias
	}
	if len(allowed) == 1 {
		return allowed[0]
	}
	priceModels := make([]string, 0, len(key.Prices))
	for name, price := range key.Prices {
		model := strings.TrimSpace(price.Model)
		if model == "" {
			model = strings.TrimSpace(name)
		}
		if model != "" {
			priceModels = append(priceModels, model)
		}
	}
	priceModels = cleanStrings(priceModels)
	for _, model := range priceModels {
		if strings.EqualFold(model, reported) {
			return reported
		}
	}
	if len(priceModels) == 1 {
		return priceModels[0]
	}
	return reported
}

func visibleExecutorAliasForReportedModel(reported string) string {
	reported = strings.TrimSpace(reported)
	if reported == "" {
		return ""
	}
	for actual, visible := range executorUsageModelAliases {
		if strings.EqualFold(strings.TrimSpace(actual), reported) {
			return strings.TrimSpace(visible)
		}
	}
	return ""
}

func managementRegister() ([]byte, error) {
	resp := managementRegistrationResponse{
		Routes: []managementRoute{
			{Method: http.MethodGet, Path: "/plugins/cpa-key-policy-plus/keys"},
			{Method: http.MethodGet, Path: "/plugins/cpa-key-policy-plus/models"},
			{Method: http.MethodPut, Path: "/plugins/cpa-key-policy-plus/keys/save"},
			{Method: http.MethodPut, Path: "/plugins/cpa-key-policy-plus/keys/limits"},
			{Method: http.MethodPost, Path: "/plugins/cpa-key-policy-plus/keys/reset"},
			{Method: http.MethodGet, Path: "/plugins/cpa-key-policy-plus/events"},
			{Method: http.MethodGet, Path: "/plugins/cpa-key-policy-plus/codexcont"},
			{Method: http.MethodPut, Path: "/plugins/cpa-key-policy-plus/codexcont"},
		},
		Resources: []resourceRoute{
			{Path: "/admin", Menu: "CPA Key Policy+", Description: "Unified user key policy dashboard"},
			{Path: "/admin/api/keys"},
			{Path: "/admin/api/models"},
			{Path: "/admin/api/keys/save"},
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
	case path == "/v0/resource/plugins/cpa-key-policy-plus/admin" || path == "/admin":
		return managementHTML(adminHTML())
	case path == "/v0/resource/plugins/cpa-key-policy-plus/user" || path == "/user":
		return managementHTML(userHTML())
	case strings.HasSuffix(path, "/admin/api/keys"):
		return adminKeys(req)
	case strings.HasSuffix(path, "/admin/api/models"):
		return adminModels(req)
	case strings.HasSuffix(path, "/admin/api/keys/create"):
		return adminCreateKey(req)
	case strings.HasSuffix(path, "/admin/api/keys/save"):
		return adminSaveKeys(req)
	case strings.HasSuffix(path, "/admin/api/keys/limits"):
		return adminSetLimits(req)
	case strings.HasSuffix(path, "/admin/api/keys/reset"):
		return adminReset(req)
	case strings.HasSuffix(path, "/admin/api/keys/archive"):
		return adminArchiveKey(req)
	case strings.HasSuffix(path, "/admin/api/keys/delete"):
		return adminDeleteKey(req)
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
	case strings.HasSuffix(path, "/plugins/cpa-key-policy-plus/keys"):
		return adminKeys(req)
	case strings.HasSuffix(path, "/plugins/cpa-key-policy-plus/models"):
		return adminModels(req)
	case strings.HasSuffix(path, "/plugins/cpa-key-policy-plus/keys/create"):
		return adminCreateKey(req)
	case strings.HasSuffix(path, "/plugins/cpa-key-policy-plus/keys/save"):
		return adminSaveKeys(req)
	case strings.HasSuffix(path, "/plugins/cpa-key-policy-plus/keys/limits"):
		return adminSetLimits(req)
	case strings.HasSuffix(path, "/plugins/cpa-key-policy-plus/keys/reset"):
		return adminReset(req)
	case strings.HasSuffix(path, "/plugins/cpa-key-policy-plus/keys/archive"):
		return adminArchiveKey(req)
	case strings.HasSuffix(path, "/plugins/cpa-key-policy-plus/keys/delete"):
		return adminDeleteKey(req)
	case strings.HasSuffix(path, "/plugins/cpa-key-policy-plus/events"):
		return adminEvents(req)
	case strings.HasSuffix(path, "/plugins/cpa-key-policy-plus/codexcont"):
		return adminCodexCont(req)
	case strings.HasSuffix(path, "/key-policy-plus/api/keys"):
		return adminKeys(req)
	case strings.HasSuffix(path, "/key-policy-plus/api/models"):
		return adminModels(req)
	case strings.HasSuffix(path, "/key-policy-plus/api/keys/create"):
		return adminCreateKey(req)
	case strings.HasSuffix(path, "/key-policy-plus/api/keys/save"):
		return adminSaveKeys(req)
	case strings.HasSuffix(path, "/key-policy-plus/api/keys/limits"):
		return adminSetLimits(req)
	case strings.HasSuffix(path, "/key-policy-plus/api/keys/reset"):
		return adminReset(req)
	case strings.HasSuffix(path, "/key-policy-plus/api/keys/archive"):
		return adminArchiveKey(req)
	case strings.HasSuffix(path, "/key-policy-plus/api/keys/delete"):
		return adminDeleteKey(req)
	case strings.HasSuffix(path, "/key-policy-plus/api/events"):
		return adminEvents(req)
	case strings.HasSuffix(path, "/key-policy-plus/api/codexcont"):
		return adminCodexCont(req)
	default:
		return jsonResponse(http.StatusNotFound, map[string]any{"ok": false, "error": "not_found"})
	}
}

func adminKeys(req managementRequest) ([]byte, error) {
	if err := syncNativeKeysFromLoadedConfig(); err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]any{"ok": false, "error": "native_key_sync_failed", "message": policyplus.Brief(err.Error(), 240)})
	}
	_ = refreshKeyPolicyState(false)
	store := loadedStore()
	if store == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "store_unavailable"})
	}
	keys, err := store.ListKeys(context.Background())
	if err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
	}
	includeRemoved := truthyQuery(req.Query.Get("include_removed")) || truthyQuery(req.Query.Get("show_removed"))
	keys = currentAdminKeyRows(keys, includeRemoved)
	safe := make([]map[string]any, 0, len(keys))
	now := time.Now()
	for _, key := range keys {
		row := key.Safe()
		usage := usageWindows(context.Background(), store, key.ID, now)
		row["usage"] = usage
		row["quota"] = quotaWindows(key, usage)
		safe = append(safe, row)
	}
	return jsonResponse(http.StatusOK, map[string]any{"ok": true, "keys": safe, "codexcont": codexcontStatus()})
}

func truthyQuery(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func currentAdminKeyRows(keys []policyplus.KeyRecord, includeRemoved bool) []policyplus.KeyRecord {
	out := make([]policyplus.KeyRecord, 0, len(keys))
	for _, key := range keys {
		if key.Source != policyplus.NativeCPASource {
			continue
		}
		if !includeRemoved && (!key.SourcePresent || key.Hidden) {
			continue
		}
		out = append(out, key)
	}
	return out
}

func adminModels(_ managementRequest) ([]byte, error) {
	models, warnings := adminModelCatalog()
	return jsonResponse(http.StatusOK, map[string]any{"ok": true, "models": models, "warnings": warnings})
}

func adminModelCatalog() ([]policyplus.ModelOption, []string) {
	warnings := []string{}
	hostModels, hostWarnings := hostAuthModelHints()
	warnings = append(warnings, hostWarnings...)
	configured := configuredModelOptions()
	models := policyplus.MergeModelOptions(hostModels, configured)
	if len(models) == 0 {
		warnings = append(warnings, "当前没有从 CPA 或 Plus 配置中发现模型；可以先创建允许全部模型的 Key，或在编辑模型时手动输入模型名。")
	}
	sort.SliceStable(models, func(i, j int) bool {
		if models[i].Known != models[j].Known {
			return models[i].Known
		}
		return strings.ToLower(models[i].ID) < strings.ToLower(models[j].ID)
	})
	return models, warnings
}

func configuredModelOptions() []policyplus.ModelOption {
	_ = syncNativeKeysFromLoadedConfig()
	store := loadedStore()
	if store == nil {
		return nil
	}
	keys, err := store.ListKeys(context.Background())
	if err != nil {
		return nil
	}
	ids := []string{}
	for _, key := range keys {
		ids = append(ids, key.Models...)
		for model := range key.Prices {
			ids = append(ids, model)
		}
	}
	return policyplus.ModelOptionsFromIDs(cleanStrings(ids), "plus_configured", false)
}

func hostAuthModelHints() ([]policyplus.ModelOption, []string) {
	result, err := callHost(methodHostAuthList, map[string]any{})
	if err != nil {
		return nil, []string{"宿主 auth 列表不可用，已使用 Plus 当前配置模型兜底。"}
	}
	var body struct {
		Files []map[string]any `json:"files"`
	}
	if err := json.Unmarshal(result, &body); err != nil {
		return nil, []string{"宿主 auth 列表格式无法解析，已使用 Plus 当前配置模型兜底。"}
	}
	ids := []string{}
	for _, file := range body.Files {
		for _, field := range []string{"models", "available_models", "model_aliases"} {
			ids = append(ids, modelIDsFromAny(file[field])...)
		}
	}
	return policyplus.ModelOptionsFromIDs(cleanStrings(ids), "host_auth", true), nil
}

func modelIDsFromAny(raw any) []string {
	options := policyplus.NormalizeModelOptions(raw, "host_auth")
	out := make([]string, 0, len(options))
	for _, option := range options {
		out = append(out, option.ID)
	}
	return out
}

func adminCreateKey(req managementRequest) ([]byte, error) {
	_ = req
	return jsonResponse(http.StatusGone, map[string]any{
		"ok":      false,
		"error":   "native_key_lifecycle_owned_by_cpa",
		"message": "Key 新增、删除、复制和别名已交给 CPA/CPAMP 管理；Plus 只编辑已同步 Key 的策略。",
	})
}

func adminSaveKeys(req managementRequest) ([]byte, error) {
	var body struct {
		Keys []struct {
			ID                string                           `json:"id"`
			Name              string                           `json:"name"`
			Enabled           *bool                            `json:"enabled"`
			RPM               int                              `json:"rpm"`
			Concurrency       int                              `json:"concurrency"`
			MaxActiveSessions int                              `json:"max_active_sessions"`
			Models            []string                         `json:"models"`
			Prices            map[string]policyplus.ModelPrice `json:"prices"`
			FiveHourUSD       *float64                         `json:"five_hour_usd"`
			DailyUSD          *float64                         `json:"daily_usd"`
			WeeklyUSD         *float64                         `json:"weekly_usd"`
			MonthlyUSD        *float64                         `json:"monthly_usd"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_json"})
	}
	store := loadedStore()
	if store == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "store_unavailable"})
	}
	if err := syncNativeKeysFromLoadedConfig(); err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]any{"ok": false, "error": "native_key_sync_failed", "message": policyplus.Brief(err.Error(), 240)})
	}
	existing, err := store.ListKeys(context.Background())
	if err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
	}
	byID := map[string]policyplus.KeyRecord{}
	for _, key := range existing {
		byID[key.ID] = key
	}
	for _, item := range body.Keys {
		key, ok := byID[strings.TrimSpace(item.ID)]
		if !ok {
			return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": "unknown_key"})
		}
		if key.Source == policyplus.NativeCPASource && !key.SourcePresent {
			return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": "source_removed_key_read_only"})
		}
		if item.Enabled != nil {
			key.Enabled = *item.Enabled
		}
		key.RPM = item.RPM
		key.Concurrency = 0
		key.MaxActiveSessions = 0
		key.Models = cleanStrings(item.Models)
		if item.Prices != nil {
			key.Prices = cleanPrices(item.Prices)
		}
		key.FiveHourUSD = item.FiveHourUSD
		key.DailyLimitUSD = item.DailyUSD
		key.WeeklyLimitUSD = item.WeeklyUSD
		key.MonthlyLimitUSD = item.MonthlyUSD
		if err := store.SaveKeySettings(context.Background(), key); err != nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		}
	}
	return adminKeys(req)
}

func cleanPrices(items map[string]policyplus.ModelPrice) map[string]policyplus.ModelPrice {
	if items == nil {
		return nil
	}
	out := map[string]policyplus.ModelPrice{}
	for name, price := range items {
		model := strings.TrimSpace(price.Model)
		if model == "" {
			model = strings.TrimSpace(name)
		}
		if model == "" {
			continue
		}
		price.Model = model
		out[model] = price
	}
	return out
}

func cleanStrings(items []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
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
	if err := syncNativeKeysFromLoadedConfig(); err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]any{"ok": false, "error": "native_key_sync_failed", "message": policyplus.Brief(err.Error(), 240)})
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
		windows = []string{policyplus.Range5H, policyplus.Range24H, policyplus.Range7D, policyplus.RangeMonth}
	}
	store := loadedStore()
	if store == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "store_unavailable"})
	}
	if err := syncNativeKeysFromLoadedConfig(); err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]any{"ok": false, "error": "native_key_sync_failed", "message": policyplus.Brief(err.Error(), 240)})
	}
	for _, window := range windows {
		if err := store.Reset(context.Background(), body.ID, window, time.Now()); err != nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		}
	}
	return jsonResponse(http.StatusOK, map[string]any{"ok": true})
}

func adminArchiveKey(req managementRequest) ([]byte, error) {
	_ = req
	return jsonResponse(http.StatusGone, map[string]any{
		"ok":      false,
		"error":   "native_key_lifecycle_owned_by_cpa",
		"message": "Key 生命周期已交给 CPA/CPAMP 管理；Plus 只保留策略和历史。",
	})
}

func adminDeleteKey(req managementRequest) ([]byte, error) {
	_ = req
	return jsonResponse(http.StatusGone, map[string]any{
		"ok":      false,
		"error":   "native_key_lifecycle_owned_by_cpa",
		"message": "Key 删除请在 CPA/CPAMP 中完成；Plus 会在同步后自动标记官方已移除并保留历史。",
	})
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
		hint := policyplus.ExplainUnmatchedSubmittedKey(key)
		return jsonResponse(http.StatusUnauthorized, map[string]any{"ok": false, "error": hint.Error, "message": hint.Message})
	}
	record, decision, ok := policyDecisionForRawKey(key, "", false)
	if !ok {
		hint := policyplus.ExplainUnmatchedSubmittedKey(key)
		return jsonResponse(http.StatusUnauthorized, map[string]any{"ok": false, "error": hint.Error, "message": hint.Message})
	}
	if record.ID != "" && decision.Code != "policy_missing" {
		decision = evaluateIdentityPolicy(record)
	}
	if !decision.Allowed {
		return jsonResponse(http.StatusForbidden, map[string]any{"ok": false, "error": decision.Code, "category": "auth", "message": decision.Message})
	}
	cfg := loadedConfig()
	token, err := policyplus.SignSession(policyplus.SessionPayload{
		KeyID:     record.ID,
		KeyHash:   record.KeyHash,
		ExpiresAt: time.Now().Add(policyplus.SessionTTL()).Unix(),
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
			"Set-Cookie":    userSessionSetCookies(token),
		},
		Body: body,
	}
	return okEnvelope(resp)
}

func userSessionSetCookies(token string) []string {
	paths := []string{
		"/",
		"/v0/resource/plugins/cpa-key-policy-plus/user",
		"/key-policy-plus-user",
	}
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		out = append(out, (&http.Cookie{
			Name:     plusSessionCookieName,
			Value:    token,
			Path:     path,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   int(policyplus.SessionTTL().Seconds()),
		}).String())
	}
	return out
}

func userSubmittedKey(req managementRequest) string {
	raw := firstNonEmpty(
		headerFirst(req.Headers, "X-CPA-Key-Policy-Plus-Key"),
		headerFirst(req.Headers, "X-CPA-Governor-Key"),
		headerFirst(req.Headers, "X-CPA-User-Key"),
	)
	if raw == "" {
		raw = bearer(headerFirst(req.Headers, "Authorization"))
	}
	return policyplus.NormalizeSubmittedKey(raw)
}

func userMe(req managementRequest) ([]byte, error) {
	key, ok := keyFromSession(req)
	if !ok {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"ok": false, "error": "not_authenticated", "category": "auth", "message": "会话已过期，请重新登录。"})
	}
	row := key.Safe()
	if store := loadedStore(); store != nil {
		usage := usageWindows(context.Background(), store, key.ID, time.Now())
		row["usage"] = usage
		row["quota"] = quotaWindows(key, usage)
	}
	return jsonResponse(http.StatusOK, map[string]any{"ok": true, "me": row})
}

func userUsage(req managementRequest) ([]byte, error) {
	key, ok := keyFromSession(req)
	if !ok {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"ok": false, "error": "not_authenticated", "category": "auth", "message": "会话已过期，请重新登录。"})
	}
	store := loadedStore()
	if store == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "store_unavailable"})
	}
	rangeName := req.Query.Get("range")
	if rangeName == "" {
		rangeName = policyplus.Range24H
	}
	summary, err := store.UsageSummary(context.Background(), key.ID, policyplus.WindowFor(rangeName, time.Now()))
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
		return jsonResponse(http.StatusUnauthorized, map[string]any{"ok": false, "error": "not_authenticated", "category": "auth", "message": "会话已过期，请重新登录。"})
	}
	return eventsResponseFromRequest(req, key.ID)
}

func userCodexCont(req managementRequest) ([]byte, error) {
	key, ok := keyFromSession(req)
	if !ok {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"ok": false, "error": "not_authenticated", "category": "auth", "message": "会话已过期，请重新登录。"})
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
	var events []policyplus.UsageEvent
	var err error
	if strings.TrimSpace(rangeName) == "" {
		events, err = store.RecentEvents(context.Background(), keyID, limit)
	} else {
		events, err = store.RecentEventsWindow(context.Background(), keyID, policyplus.WindowFor(rangeName, time.Now()), limit)
	}
	if err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
	}
	return jsonResponse(http.StatusOK, map[string]any{"ok": true, "events": events})
}

func usageWindows(ctx context.Context, store *policyplus.Store, keyID string, now time.Time) map[string]float64 {
	out := map[string]float64{}
	for _, name := range []string{policyplus.Range5H, policyplus.Range24H, policyplus.Range7D, policyplus.RangeMonth} {
		value, _ := store.UsageSum(ctx, keyID, policyplus.WindowFor(name, now))
		out[name] = value
	}
	return out
}

func quotaWindows(key policyplus.KeyRecord, usage map[string]float64) map[string]map[string]any {
	limits := map[string]*float64{
		policyplus.Range5H:    key.FiveHourUSD,
		policyplus.Range24H:   key.DailyLimitUSD,
		policyplus.Range7D:    key.WeeklyLimitUSD,
		policyplus.RangeMonth: key.MonthlyLimitUSD,
	}
	out := map[string]map[string]any{}
	for _, name := range []string{policyplus.Range5H, policyplus.Range24H, policyplus.Range7D, policyplus.RangeMonth} {
		used := usage[name]
		row := map[string]any{
			"used_usd":      used,
			"limit_usd":     nil,
			"remaining_usd": nil,
			"percent":       nil,
		}
		if limit := limits[name]; limit != nil {
			remaining := *limit - used
			if remaining < 0 {
				remaining = 0
			}
			percent := 0.0
			if *limit > 0 {
				percent = used / *limit
			}
			row["limit_usd"] = *limit
			row["remaining_usd"] = remaining
			row["percent"] = percent
		}
		out[name] = row
	}
	return out
}

func codexRequestsForKey(key policyplus.KeyRecord, limit int) ([]map[string]any, string) {
	if requests, ok := fetchExecutorCodexSummaries(key, limit); ok {
		return requests, "codexcont_executor_store"
	}
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

func fetchExecutorCodexSummaries(key policyplus.KeyRecord, limit int) ([]map[string]any, bool) {
	cfg := loadedConfig()
	path := strings.TrimSpace(cfg.CodexSummaryDBPath)
	if path == "" {
		return nil, false
	}
	items, err := policyplus.RecentCodexSummariesFromSQLite(context.Background(), path, key.ID, limit)
	if err != nil {
		return nil, false
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
	return out, true
}

func fetchCodexContRequests(key policyplus.KeyRecord, limit int) ([]map[string]any, bool) {
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

func safeCodexSummary(req map[string]any, key policyplus.KeyRecord) map[string]any {
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

func codexIdentityMatches(identity map[string]any, key policyplus.KeyRecord) bool {
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
				Error:    policyplus.Brief(err.Error(), 400),
			})
			return
		}
		var chunk hostModelStreamReadResponse
		if err := json.Unmarshal(result, &chunk); err != nil {
			_, _ = callHost(methodHostStreamEmit, hostStreamEmitRequest{
				StreamID: targetStreamID,
				Error:    "decode host stream chunk: " + policyplus.Brief(err.Error(), 300),
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
				Error:    policyplus.Brief(chunk.Error, 400),
			})
			return
		}
		if chunk.Done {
			return
		}
	}
}

func keyFromSession(req managementRequest) (policyplus.KeyRecord, bool) {
	_ = syncNativeKeysFromLoadedConfig()
	_ = refreshKeyPolicyState(false)
	tokens := sessionTokensFromCookie(headerFirst(req.Headers, "Cookie"))
	cfg := loadedConfig()
	for _, token := range tokens {
		if key, ok := keyFromSessionToken(token, cfg.SessionSecret); ok {
			return key, true
		}
	}
	return policyplus.KeyRecord{}, false
}

func sessionTokensFromCookie(cookie string) []string {
	var tokens []string
	for _, part := range strings.Split(cookie, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, plusSessionCookieName+"=") {
			tokens = append(tokens, strings.TrimPrefix(part, plusSessionCookieName+"="))
			continue
		}
		if strings.HasPrefix(part, "cpa_governor_session=") {
			tokens = append(tokens, strings.TrimPrefix(part, "cpa_governor_session="))
		}
	}
	return tokens
}

func keyFromSessionToken(token string, secret string) (policyplus.KeyRecord, bool) {
	payload, ok := policyplus.VerifySession(token, secret, time.Now())
	if !ok {
		return policyplus.KeyRecord{}, false
	}
	store := loadedStore()
	if store == nil {
		return policyplus.KeyRecord{}, false
	}
	keys, err := store.ListKeys(context.Background())
	if err != nil {
		return policyplus.KeyRecord{}, false
	}
	for _, key := range keys {
		currentHash, errCurrent := policyplus.NormalizeHash(key.KeyHash)
		sessionHash, errSession := policyplus.NormalizeHash(payload.KeyHash)
		if key.ID == payload.KeyID && key.Enabled && !key.Archived && errCurrent == nil && errSession == nil && currentHash == sessionHash {
			return key, true
		}
	}
	return policyplus.KeyRecord{}, false
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
		out["health_error"] = policyplus.Brief(err.Error(), 200)
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
	tpl = strings.ReplaceAll(tpl, "{{SHARED_CSS}}", sharedCSS())
	return strings.ReplaceAll(tpl, "{{CSS}}", sharedCSS())
}
