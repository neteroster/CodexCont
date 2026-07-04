package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"codexcont/cpa-codexcont-executor-plugin/internal/executor"
	_ "embed"
	"gopkg.in/yaml.v3"
)

const pluginID = "cpa-codexcont-executor"
const executorModelScopeBoth = "both"
const executorFormatOpenAIResponse = "openai-response"

//go:embed assets/admin.html
var adminHTMLTemplate string

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
	mu    sync.RWMutex
	cfg   executor.Config
	store *executor.Store
}

var state = runtimeState{cfg: executor.DefaultConfig()}
var monitor = newSummaryMonitor(160)

var hostCall = func(method string, payload any) (json.RawMessage, error) {
	return nil, fmt.Errorf("host callback unavailable for %s", method)
}

func main() {
	if os.Getenv("CPA_CODEXCONT_EXECUTOR_NOOP") != "" {
		return
	}
}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case methodPluginRegister:
		return pluginRegister(request)
	case methodPluginReconfigure:
		return pluginReconfigure(request)
	case methodFrontendAuthIdentifier:
		return okEnvelope(map[string]string{"identifier": pluginID})
	case methodFrontendAuthAuthenticate:
		return frontendAuth(request)
	case methodModelRoute:
		return routeModel(request)
	case methodExecutorIdentifier:
		return okEnvelope(map[string]any{"identifier": pluginID, "type": "codexcont_executor"})
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
		return errorEnvelope("unknown_method", method), nil
	}
}

func pluginRegister(raw []byte) ([]byte, error) {
	if len(raw) > 0 {
		if err := applyLifecycleConfig(raw); err != nil {
			return errorEnvelope("config_error", err.Error()), nil
		}
	}
	return okEnvelope(pluginRegistration())
}

func pluginReconfigure(raw []byte) ([]byte, error) {
	if err := applyLifecycleConfig(raw); err != nil {
		return errorEnvelope("config_error", err.Error()), nil
	}
	return okEnvelope(pluginRegistration())
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: schemaVersion,
		Metadata: metadata{
			Name:             pluginID,
			Version:          "0.1.0",
			Author:           "konbakuyomu/CodexCont",
			GitHubRepository: "https://local/CodexCont",
			ConfigFields: []configField{
				{Name: "enabled", Type: configBoolean, Description: "Enable the plugin."},
				{Name: "route_enabled", Type: configBoolean, Description: "Route streaming Responses requests to the CodexCont executor."},
				{Name: "state_db_path", Type: configString, Description: "SQLite path for safe executor summaries."},
				{Name: "fail_mode", Type: configEnum, EnumValues: []string{"fallback", "fail_closed"}, Description: "Fallback behavior when the executor is disabled."},
				{Name: "upstream_model", Type: configString, Description: "Optional provider-registered model used for internal upstream rounds."},
				{Name: "upstream_model_aliases", Type: configString, Description: "Optional YAML map from client-visible model aliases to internal upstream models."},
				{Name: "truncation_step", Type: configInteger, Description: "Reasoning truncation step. Default 518."},
				{Name: "max_continue", Type: configInteger, Description: "Maximum hidden continuation rounds."},
				{Name: "marker_text", Type: configString, Description: "Hidden commentary continuation marker."},
			},
		},
		Capabilities: capabilities{
			FrontendAuthProvider:  true,
			ModelRouter:           true,
			Executor:              true,
			ExecutorModelScope:    executorModelScopeBoth,
			ExecutorInputFormats:  []string{executorFormatOpenAIResponse},
			ExecutorOutputFormats: []string{executorFormatOpenAIResponse},
			UsagePlugin:           true,
			ManagementAPI:         true,
		},
	}
}

func frontendAuth(_ []byte) ([]byte, error) {
	return okEnvelope(frontendAuthResponse{Authenticated: false, Metadata: map[string]string{"mode": "observability_only"}})
}

func applyLifecycleConfig(raw []byte) error {
	var req lifecycleRequest
	if err := json.Unmarshal(raw, &req); err != nil && len(raw) > 0 {
		return err
	}
	cfg := executor.DefaultConfig()
	if len(req.ConfigYAML) > 0 {
		if err := yaml.Unmarshal(req.ConfigYAML, &cfg); err != nil {
			return err
		}
	}
	cfg = cfg.Normalize()
	store, err := executor.OpenStore(cfg.StateDBPath)
	if err != nil {
		return err
	}
	state.mu.Lock()
	old := state.store
	state.cfg = cfg
	state.store = store
	state.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	return nil
}

func loadedConfig() executor.Config {
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.cfg.Normalize()
}

func loadedStore() *executor.Store {
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.store
}

func shutdownPlugin() {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.store != nil {
		_ = state.store.Close()
		state.store = nil
	}
}

func routeModel(raw []byte) ([]byte, error) {
	var req modelRouteRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	cfg := loadedConfig()
	if !cfg.Enabled || !cfg.RouteEnabled || !req.Stream || !isResponsesRequest(req.SourceFormat, req.Body) {
		return okEnvelope(modelRouteResponse{Handled: false})
	}
	return okEnvelope(modelRouteResponse{
		Handled:    true,
		TargetKind: routeTargetSelf,
		Reason:     "cpa_codexcont_executor_enabled",
	})
}

func isResponsesRequest(source string, body []byte) bool {
	source = strings.ToLower(strings.TrimSpace(source))
	if strings.Contains(source, "response") || source == "responses" {
		return true
	}
	text := string(body)
	return strings.Contains(text, `"model"`) &&
		(strings.Contains(text, `"input"`) ||
			strings.Contains(text, `"previous_response_id"`) ||
			strings.Contains(text, `"reasoning.encrypted_content"`))
}

func executorUnavailable() ([]byte, error) {
	cfg := loadedConfig()
	if cfg.RouteEnabled && cfg.FailMode == "fail_closed" {
		return errorEnvelope("codexcont_executor_disabled", "CodexCont executor route is disabled"), nil
	}
	return errorEnvelope("codexcont_executor_disabled", "CodexCont executor is not handling this request"), nil
}

func executorExecute(raw []byte) ([]byte, error) {
	var req executorCallRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	execReq := normalizedExecutorRequest(req)
	cfg := loadedConfig()
	if !cfg.Enabled || !cfg.RouteEnabled {
		return executorUnavailable()
	}
	payload := firstBytes(execReq.Payload, execReq.OriginalRequest)
	upstreamModel := resolveUpstreamModel(cfg, execReq, payload)
	upstreamPayload, _ := rewriteUpstreamPayload(payload, upstreamModel)
	result, err := hostCall(methodHostModelExecute, hostModelExecutionRequest{
		EntryProtocol:  executorProtocol(execReq.SourceFormat),
		ExitProtocol:   executorProtocol(execReq.Format),
		Model:          upstreamModel,
		Stream:         false,
		Body:           upstreamPayload,
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
	cfg := loadedConfig()
	if !cfg.Enabled || !cfg.RouteEnabled {
		return executorUnavailable()
	}
	if strings.TrimSpace(req.StreamID) == "" {
		return errorEnvelope("stream_id_required", "stream_id is required for executor.execute_stream"), nil
	}
	payload := firstBytes(execReq.Payload, execReq.OriginalRequest)
	var base map[string]any
	if err := json.Unmarshal(payload, &base); err != nil {
		return errorEnvelope("invalid_responses_payload", err.Error()), nil
	}
	go foldHostStream(req, execReq, cfg, base)
	return okEnvelope(executorStreamResponse{Headers: http.Header{"Content-Type": []string{"text/event-stream"}}})
}

func foldHostStream(req executorCallRequest, execReq executorRequest, cfg executor.Config, base map[string]any) {
	ctx := context.Background()
	targetStreamID := req.StreamID
	monitorID := monitor.Start(execReq, base)
	diagnostics := newStreamDiagnostics()
	var streamErr string
	defer func() {
		_, _ = hostCall(methodHostStreamClose, hostStreamCloseRequest{StreamID: targetStreamID, Error: streamErr})
	}()
	opener := func(ctx context.Context, body []byte, round int) (executor.StreamReader, error) {
		return openHostModelStream(ctx, req, execReq, cfg, body, round, diagnostics)
	}
	emitter := func(ctx context.Context, payload []byte) error {
		_, err := hostCall(methodHostStreamEmit, hostStreamEmitRequest{StreamID: targetStreamID, Payload: payload})
		return err
	}
	result, err := executor.FoldStream(ctx, cfg, base, opener, emitter)
	if err != nil {
		streamErr = brief(err.Error(), 400)
		monitor.Fail(monitorID, streamErr)
		_, _ = hostCall(methodHostStreamEmit, hostStreamEmitRequest{StreamID: targetStreamID, Error: streamErr})
		return
	}
	if result != nil {
		monitor.Finish(monitorID, saveFoldSummary(execReq, result, diagnostics.Snapshot()))
	}
}

type hostStreamReader struct {
	streamID    string
	round       int
	diagnostics *streamDiagnostics
	firstRead   bool
	readCount   int
}

func openHostModelStream(_ context.Context, req executorCallRequest, execReq executorRequest, cfg executor.Config, body []byte, round int, diagnostics *streamDiagnostics) (executor.StreamReader, error) {
	requestedModel := firstNonEmpty(modelFromBody(body), execReq.Model)
	upstreamModel := resolveUpstreamModel(cfg, execReq, body)
	upstreamBody, filteredTools := rewriteUpstreamPayload(body, upstreamModel)
	hostReq := hostModelExecutionRequest{
		EntryProtocol:  executorProtocol(execReq.SourceFormat),
		ExitProtocol:   executorProtocol(execReq.Format),
		Model:          upstreamModel,
		Stream:         true,
		Body:           upstreamBody,
		Headers:        cloneHeader(execReq.Headers),
		Query:          cloneValues(execReq.Query),
		Alt:            execReq.Alt,
		HostCallbackID: req.HostCallbackID,
	}
	if diagnostics != nil {
		diagnostics.RecordOpen(round, hostReq, requestedModel, modelFromBody(upstreamBody), len(upstreamBody), filteredTools)
	}
	result, err := hostCall(methodHostModelExecuteStream, hostReq)
	if err != nil {
		if diagnostics != nil {
			diagnostics.RecordOpenError(round, err)
		}
		return nil, err
	}
	var resp hostModelStreamResponse
	if err := json.Unmarshal(result, &resp); err != nil {
		if diagnostics != nil {
			diagnostics.RecordOpenError(round, err)
		}
		return nil, err
	}
	if diagnostics != nil {
		diagnostics.RecordOpenResponse(round, resp)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("upstream returned %d", resp.StatusCode)
	}
	if resp.StreamID == "" {
		return nil, fmt.Errorf("host returned empty stream id")
	}
	return &hostStreamReader{streamID: resp.StreamID, round: round, diagnostics: diagnostics}, nil
}

func (r *hostStreamReader) Read(context.Context) ([]byte, bool, error) {
	result, err := hostCall(methodHostModelStreamRead, hostModelStreamReadRequest{StreamID: r.streamID})
	if err != nil {
		r.recordRead(nil, false, err)
		return nil, false, err
	}
	var chunk hostModelStreamReadResponse
	if err := json.Unmarshal(result, &chunk); err != nil {
		r.recordRead(nil, false, err)
		return nil, false, err
	}
	if chunk.Error != "" {
		err := fmt.Errorf("%s", chunk.Error)
		r.recordRead(chunk.Payload, chunk.Done, err)
		return chunk.Payload, chunk.Done, err
	}
	r.recordRead(chunk.Payload, chunk.Done, nil)
	return chunk.Payload, chunk.Done, nil
}

func (r *hostStreamReader) recordRead(payload []byte, done bool, err error) {
	if r == nil {
		return
	}
	r.readCount++
	if r.diagnostics != nil {
		r.diagnostics.RecordRead(r.round, r.readCount, payload, done, err)
	}
	if !r.firstRead {
		r.firstRead = true
	}
}

func (r *hostStreamReader) Close() error {
	_, err := hostCall(methodHostModelStreamClose, hostModelStreamCloseRequest{StreamID: r.streamID})
	return err
}

func saveFoldSummary(req executorRequest, result *executor.FoldResult, diagnostics map[string]any) map[string]any {
	if result == nil {
		return nil
	}
	summary := cloneSummary(result.Summary)
	keyID := strings.TrimSpace(req.AuthID)
	if keyID != "" {
		summary["key_identity"] = map[string]any{"known": true, "id": keyID}
	}
	model := firstNonEmpty(req.Model, fmt.Sprint(summary["model"]))
	if model != "" {
		summary["model"] = model
	}
	if diagnostics != nil {
		summary["diagnostics"] = diagnostics
	}
	requestID := firstNonEmpty(result.RequestID, fmt.Sprint(summary["request_id"]))
	store := loadedStore()
	if store == nil {
		return summary
	}
	_ = store.SaveCodexSummary(context.Background(), requestID, keyID, model, result.Protection, summary)
	return summary
}

func usageHandle(_ []byte) ([]byte, error) {
	return okEnvelope(map[string]any{"handled": false, "observability_only": true})
}

func managementRegister() ([]byte, error) {
	return okEnvelope(managementRegistrationResponse{
		Routes: []managementRoute{
			{Method: http.MethodGet, Path: "/plugins/cpa-codexcont-executor/status", Description: "CodexCont executor status."},
			{Method: http.MethodGet, Path: "/plugins/cpa-codexcont-executor/summaries", Description: "Safe CodexCont executor summaries."},
		},
		Resources: []resourceRoute{
			{Path: "/admin", Menu: "CodexCont Executor", Description: "Realtime CodexCont executor monitor"},
			{Path: "/admin/api/status"},
			{Path: "/admin/api/summaries"},
		},
	})
}

func managementHandle(raw []byte) ([]byte, error) {
	var req managementRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	path := strings.TrimSpace(req.Path)
	switch {
	case path == "/v0/resource/plugins/cpa-codexcont-executor/admin" || path == "/admin":
		return managementHTML(adminHTML())
	case strings.Contains(path, "/admin/api/status"):
		return jsonResponse(http.StatusOK, map[string]any{"ok": true, "executor": statusPayload()})
	case strings.Contains(path, "/admin/api/summaries"):
		return summariesResponse(req)
	case strings.HasSuffix(path, "/status"):
		return jsonResponse(http.StatusOK, map[string]any{"ok": true, "executor": statusPayload()})
	case strings.HasSuffix(path, "/summaries"):
		return summariesResponse(req)
	default:
		return jsonResponse(http.StatusNotFound, map[string]any{"ok": false, "error": "not_found"})
	}
}

func summariesResponse(req managementRequest) ([]byte, error) {
	store := loadedStore()
	if store == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "store_unavailable"})
	}
	limit := limitFromQuery(req.Query.Get("limit"), 100, 200)
	items, err := store.RecentCodexSummaries(context.Background(), req.Query.Get("key_id"), limit)
	if err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
	}
	out := make([]map[string]any, 0, len(items))
	seen := map[string]bool{}
	for _, summary := range monitor.Recent(limit) {
		id := fmt.Sprint(summary["request_id"])
		if id != "" {
			seen[id] = true
		}
		out = append(out, summary)
		if len(out) >= limit {
			return jsonResponse(http.StatusOK, map[string]any{"ok": true, "summaries": out})
		}
	}
	for _, item := range items {
		if seen[item.RequestID] {
			continue
		}
		out = append(out, item.Summary)
		if len(out) >= limit {
			break
		}
	}
	return jsonResponse(http.StatusOK, map[string]any{"ok": true, "summaries": out})
}

func statusPayload() map[string]any {
	cfg := loadedConfig()
	return map[string]any{
		"plugin_id":       pluginID,
		"enabled":         cfg.Enabled,
		"route_enabled":   cfg.RouteEnabled,
		"state_db_path":   cfg.StateDBPath,
		"truncation_step": cfg.TruncationStep,
		"max_continue":    cfg.MaxContinue,
		"mode":            "executor_only",
		"monitor":         "cpamp_admin_resource",
		"upstream_model":  cfg.UpstreamModel,
		"alias_count":     len(cfg.UpstreamModelAliases),
	}
}

func okEnvelope(result any) ([]byte, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

func jsonResponse(status int, body any) ([]byte, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return okEnvelope(managementResponse{
		StatusCode: status,
		Headers: http.Header{
			"Content-Type":  []string{"application/json; charset=utf-8"},
			"Cache-Control": []string{"no-store"},
		},
		Body: raw,
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

func adminHTML() string {
	return adminHTMLTemplate
}

func limitFromQuery(raw string, fallback, maxValue int) int {
	limit, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || limit <= 0 {
		return fallback
	}
	if limit > maxValue {
		return maxValue
	}
	return limit
}

func firstBytes(values ...[]byte) []byte {
	for _, value := range values {
		if len(value) > 0 {
			return value
		}
	}
	return nil
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func executorProtocol(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "", "responses", "openai-responses", "openai_responses":
		return executorFormatOpenAIResponse
	default:
		return value
	}
}

func resolveUpstreamModel(cfg executor.Config, req executorRequest, body []byte) string {
	cfg = cfg.Normalize()
	requested := firstNonEmpty(modelFromBody(body), req.Model)
	if cfg.UpstreamModel != "" {
		return cfg.UpstreamModel
	}
	if len(cfg.UpstreamModelAliases) > 0 {
		if target := strings.TrimSpace(cfg.UpstreamModelAliases[requested]); target != "" {
			return target
		}
		requestedLower := strings.ToLower(strings.TrimSpace(requested))
		for alias, target := range cfg.UpstreamModelAliases {
			if strings.ToLower(strings.TrimSpace(alias)) == requestedLower {
				if target = strings.TrimSpace(target); target != "" {
					return target
				}
			}
		}
	}
	return requested
}

func rewritePayloadModel(body []byte, model string) []byte {
	out, _ := rewriteUpstreamPayload(body, model)
	return out
}

func rewriteUpstreamPayload(body []byte, model string) ([]byte, []string) {
	model = strings.TrimSpace(model)
	if model == "" || len(body) == 0 {
		return append([]byte(nil), body...), nil
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return append([]byte(nil), body...), nil
	}
	filtered := filterUnsupportedTools(raw, model)
	current, _ := raw["model"].(string)
	if strings.TrimSpace(current) == model && len(filtered) == 0 {
		return append([]byte(nil), body...), nil
	}
	raw["model"] = model
	out, err := json.Marshal(raw)
	if err != nil {
		return append([]byte(nil), body...), nil
	}
	return out, filtered
}

func filterUnsupportedTools(raw map[string]any, upstreamModel string) []string {
	if raw == nil || !strings.EqualFold(strings.TrimSpace(upstreamModel), executor.DefaultCodexUpstreamModel) {
		return nil
	}
	tools, ok := raw["tools"].([]any)
	if !ok || len(tools) == 0 {
		return nil
	}
	kept := make([]any, 0, len(tools))
	filteredSet := map[string]bool{}
	for _, tool := range tools {
		toolMap, ok := tool.(map[string]any)
		if !ok {
			kept = append(kept, tool)
			continue
		}
		toolType := strings.TrimSpace(fmt.Sprint(toolMap["type"]))
		if strings.EqualFold(toolType, "image_generation") {
			filteredSet["image_generation"] = true
			continue
		}
		kept = append(kept, tool)
	}
	if len(filteredSet) == 0 {
		return nil
	}
	if len(kept) == 0 {
		delete(raw, "tools")
	} else {
		raw["tools"] = kept
	}
	if isImageGenerationToolChoice(raw["tool_choice"]) {
		delete(raw, "tool_choice")
	}
	filtered := make([]string, 0, len(filteredSet))
	for toolType := range filteredSet {
		filtered = append(filtered, toolType)
	}
	sort.Strings(filtered)
	return filtered
}

func isImageGenerationToolChoice(value any) bool {
	switch choice := value.(type) {
	case string:
		return strings.EqualFold(strings.TrimSpace(choice), "image_generation")
	case map[string]any:
		if strings.EqualFold(strings.TrimSpace(fmt.Sprint(choice["type"])), "image_generation") {
			return true
		}
		if strings.EqualFold(strings.TrimSpace(fmt.Sprint(choice["name"])), "image_generation") {
			return true
		}
	}
	return false
}

func cloneHeader(in http.Header) http.Header {
	out := http.Header{}
	for key, values := range in {
		out[key] = append([]string(nil), values...)
	}
	return out
}

func cloneValues(in map[string][]string) map[string][]string {
	out := map[string][]string{}
	for key, values := range in {
		out[key] = append([]string(nil), values...)
	}
	return out
}

func cloneSummary(in map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range in {
		out[key] = value
	}
	return out
}

type streamDiagnostics struct {
	mu     sync.Mutex
	rounds map[int]map[string]any
	order  []int
}

func newStreamDiagnostics() *streamDiagnostics {
	return &streamDiagnostics{rounds: map[int]map[string]any{}}
}

func (d *streamDiagnostics) RecordOpen(round int, req hostModelExecutionRequest, requestedModel, bodyModel string, bodyBytes int, filteredTools []string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	item := d.ensureRoundLocked(round)
	item["entry_protocol"] = req.EntryProtocol
	item["exit_protocol"] = req.ExitProtocol
	item["model"] = req.Model
	item["requested_model"] = requestedModel
	item["body_model"] = bodyModel
	item["body_bytes"] = bodyBytes
	item["stream"] = req.Stream
	item["host_callback"] = strings.TrimSpace(req.HostCallbackID) != ""
	if len(filteredTools) > 0 {
		item["filtered_tool_types"] = append([]string(nil), filteredTools...)
	}
}

func (d *streamDiagnostics) RecordOpenResponse(round int, resp hostModelStreamResponse) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	item := d.ensureRoundLocked(round)
	item["open_status_code"] = resp.StatusCode
	item["stream_id_present"] = strings.TrimSpace(resp.StreamID) != ""
	if contentType := resp.Headers.Get("Content-Type"); contentType != "" {
		item["response_content_type"] = contentType
	}
}

func (d *streamDiagnostics) RecordOpenError(round int, err error) {
	if d == nil || err == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	item := d.ensureRoundLocked(round)
	item["open_error"] = brief(err.Error(), 160)
}

func (d *streamDiagnostics) RecordRead(round int, readNo int, payload []byte, done bool, err error) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	item := d.ensureRoundLocked(round)
	if readNo <= 0 {
		readNo = 1
	}
	item["read_count"] = readNo
	item["last_read_payload_bytes"] = len(payload)
	item["last_read_done"] = done
	item["last_read_kind"] = payloadKind(payload)
	if readNo == 1 {
		item["first_read_payload_bytes"] = len(payload)
		item["first_read_done"] = done
		item["first_read_kind"] = payloadKind(payload)
	}
	if err != nil {
		item["last_read_error"] = brief(err.Error(), 160)
		if readNo == 1 {
			item["first_read_error"] = brief(err.Error(), 160)
		}
	}
}

func (d *streamDiagnostics) Snapshot() map[string]any {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.order) == 0 {
		return nil
	}
	rounds := make([]map[string]any, 0, len(d.order))
	for _, round := range d.order {
		rounds = append(rounds, cloneSummary(d.rounds[round]))
	}
	return map[string]any{"rounds": rounds}
}

func (d *streamDiagnostics) ensureRoundLocked(round int) map[string]any {
	if round <= 0 {
		round = 1
	}
	item := d.rounds[round]
	if item == nil {
		item = map[string]any{"round": round}
		d.rounds[round] = item
		d.order = append(d.order, round)
	}
	return item
}

func modelFromBody(body []byte) string {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return ""
	}
	return fmt.Sprint(raw["model"])
}

func brief(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit]
}

func payloadKind(payload []byte) string {
	trimmed := bytes.TrimSpace(payload)
	switch {
	case len(trimmed) == 0:
		return "empty"
	case bytes.HasPrefix(trimmed, []byte("event:")):
		return "sse_event"
	case bytes.HasPrefix(trimmed, []byte("data:")):
		return "sse_data"
	case bytes.Equal(trimmed, []byte("[DONE]")):
		return "done"
	case json.Valid(trimmed):
		return "json"
	default:
		return "other"
	}
}

type summaryMonitor struct {
	mu    sync.Mutex
	limit int
	items map[string]map[string]any
	order []string
}

func newSummaryMonitor(limit int) *summaryMonitor {
	return &summaryMonitor{limit: limit, items: map[string]map[string]any{}}
}

func (m *summaryMonitor) Start(req executorRequest, base map[string]any) string {
	if m == nil {
		return ""
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	id := fmt.Sprintf("processing-%d", time.Now().UnixNano())
	model := firstNonEmpty(req.Model, fmt.Sprint(base["model"]))
	item := map[string]any{
		"request_id":         id,
		"model":              model,
		"started_at":         now,
		"updated_at":         now,
		"status":             "processing",
		"final_status":       "processing",
		"protection":         "processing",
		"latest_round":       0,
		"continuation_count": 0,
		"rounds":             []map[string]any{},
	}
	if keyID := strings.TrimSpace(req.AuthID); keyID != "" {
		item["key_identity"] = map[string]any{"known": true, "id": keyID}
	}
	m.upsert(id, item)
	return id
}

func (m *summaryMonitor) Finish(processingID string, summary map[string]any) {
	if m == nil || len(summary) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if processingID != "" {
		m.removeLocked(processingID)
	}
	id := processingID
	if requestID := strings.TrimSpace(fmt.Sprint(summary["request_id"])); requestID != "" && requestID != "<nil>" {
		id = requestID
	}
	m.upsertLocked(id, cloneSummary(summary))
}

func (m *summaryMonitor) Fail(processingID, reason string) {
	if m == nil || processingID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	item := m.items[processingID]
	if item == nil {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	item["updated_at"] = now
	item["ended_at"] = now
	item["status"] = "failed"
	item["final_status"] = "failed"
	item["protection"] = "failed"
	item["failure_reason"] = reason
}

func (m *summaryMonitor) Recent(limit int) []map[string]any {
	if m == nil {
		return nil
	}
	if limit <= 0 || limit > m.limit {
		limit = m.limit
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]map[string]any, 0, limit)
	for i := len(m.order) - 1; i >= 0 && len(out) < limit; i-- {
		if item := m.items[m.order[i]]; item != nil {
			out = append(out, cloneSummary(item))
		}
	}
	return out
}

func (m *summaryMonitor) upsert(id string, item map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.upsertLocked(id, item)
}

func (m *summaryMonitor) upsertLocked(id string, item map[string]any) {
	if strings.TrimSpace(id) == "" {
		id = fmt.Sprintf("summary-%d", time.Now().UnixNano())
		item["request_id"] = id
	}
	if _, exists := m.items[id]; !exists {
		m.order = append(m.order, id)
	}
	m.items[id] = item
	m.trimLocked()
}

func (m *summaryMonitor) removeLocked(id string) {
	delete(m.items, id)
	for i, value := range m.order {
		if value == id {
			m.order = append(m.order[:i], m.order[i+1:]...)
			return
		}
	}
}

func (m *summaryMonitor) trimLocked() {
	if m.limit <= 0 {
		m.limit = 160
	}
	for len(m.order) > m.limit {
		oldest := m.order[0]
		m.order = m.order[1:]
		delete(m.items, oldest)
	}
}
