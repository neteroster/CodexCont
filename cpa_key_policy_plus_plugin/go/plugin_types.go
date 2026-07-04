package main

import (
	"net/http"
	"net/url"
	"time"
)

const (
	abiVersion    uint32 = 1
	schemaVersion uint32 = 1

	methodPluginRegister           = "plugin.register"
	methodPluginReconfigure        = "plugin.reconfigure"
	methodFrontendAuthIdentifier   = "frontend_auth.identifier"
	methodFrontendAuthAuthenticate = "frontend_auth.authenticate"
	methodModelRoute               = "model.route"
	methodExecutorIdentifier       = "executor.identifier"
	methodExecutorExecute          = "executor.execute"
	methodExecutorExecuteStream    = "executor.execute_stream"
	methodExecutorCountTokens      = "executor.count_tokens"
	methodUsageHandle              = "usage.handle"
	methodManagementRegister       = "management.register"
	methodManagementHandle         = "management.handle"
	methodHostModelExecute         = "host.model.execute"
	methodHostModelExecuteStream   = "host.model.execute_stream"
	methodHostModelStreamRead      = "host.model.stream_read"
	methodHostModelStreamClose     = "host.model.stream_close"
	methodHostStreamEmit           = "host.stream.emit"
	methodHostStreamClose          = "host.stream.close"
	methodHostAuthList             = "host.auth.list"
)

const (
	configString  = "string"
	configBoolean = "boolean"
	configEnum    = "enum"

	routeTargetSelf = "self"
)

type configField struct {
	Name        string   `json:"Name"`
	Type        string   `json:"Type"`
	EnumValues  []string `json:"EnumValues,omitempty"`
	Description string   `json:"Description"`
}

type frontendAuthRequest struct {
	Method  string      `json:"Method"`
	Path    string      `json:"Path"`
	Headers http.Header `json:"Headers"`
	Query   url.Values  `json:"Query"`
	Body    []byte      `json:"Body"`
}

type frontendAuthResponse struct {
	Authenticated bool              `json:"Authenticated"`
	Principal     string            `json:"Principal,omitempty"`
	Metadata      map[string]string `json:"Metadata,omitempty"`
}

type modelRouteRequest struct {
	PluginID           string         `json:"PluginID"`
	SourceFormat       string         `json:"SourceFormat"`
	RequestedModel     string         `json:"RequestedModel"`
	Stream             bool           `json:"Stream"`
	Headers            http.Header    `json:"Headers"`
	Query              url.Values     `json:"Query"`
	Body               []byte         `json:"Body"`
	Metadata           map[string]any `json:"Metadata"`
	AvailableProviders []string       `json:"AvailableProviders"`
}

type modelRouteResponse struct {
	Handled     bool   `json:"Handled"`
	TargetKind  string `json:"TargetKind,omitempty"`
	Target      string `json:"Target,omitempty"`
	TargetModel string `json:"TargetModel,omitempty"`
	Reason      string `json:"Reason,omitempty"`
}

type managementRegistrationResponse struct {
	Routes    []managementRoute `json:"routes,omitempty"`
	Resources []resourceRoute   `json:"resources,omitempty"`
}

type managementRoute struct {
	Method      string `json:"Method"`
	Path        string `json:"Path"`
	Menu        string `json:"Menu,omitempty"`
	Description string `json:"Description,omitempty"`
}

type resourceRoute struct {
	Path        string `json:"Path"`
	Menu        string `json:"Menu,omitempty"`
	Description string `json:"Description,omitempty"`
}

type managementRequest struct {
	Method         string      `json:"Method"`
	Path           string      `json:"Path"`
	Headers        http.Header `json:"Headers"`
	Query          url.Values  `json:"Query"`
	Body           []byte      `json:"Body"`
	HostCallbackID string      `json:"host_callback_id,omitempty"`
}

type managementResponse struct {
	StatusCode int         `json:"StatusCode"`
	Headers    http.Header `json:"Headers"`
	Body       []byte      `json:"Body"`
}

type executorResponse struct {
	Payload  []byte         `json:"Payload"`
	Headers  http.Header    `json:"Headers,omitempty"`
	Metadata map[string]any `json:"Metadata,omitempty"`
}

type usageRecord struct {
	Provider        string        `json:"Provider"`
	ExecutorType    string        `json:"ExecutorType"`
	Model           string        `json:"Model"`
	Alias           string        `json:"Alias"`
	APIKey          string        `json:"APIKey"`
	AuthID          string        `json:"AuthID"`
	AuthIndex       string        `json:"AuthIndex"`
	AuthType        string        `json:"AuthType"`
	Source          string        `json:"Source"`
	ReasoningEffort string        `json:"ReasoningEffort"`
	ServiceTier     string        `json:"ServiceTier"`
	RequestedAt     time.Time     `json:"RequestedAt"`
	Latency         time.Duration `json:"Latency"`
	TTFT            time.Duration `json:"TTFT"`
	Failed          bool          `json:"Failed"`
	Failure         usageFailure  `json:"Failure"`
	Detail          usageDetail   `json:"Detail"`
	ResponseHeaders http.Header   `json:"ResponseHeaders"`
}

type executorRequest struct {
	AuthID          string            `json:"AuthID"`
	AuthProvider    string            `json:"AuthProvider"`
	Model           string            `json:"Model"`
	Format          string            `json:"Format"`
	Stream          bool              `json:"Stream"`
	Alt             string            `json:"Alt"`
	Headers         http.Header       `json:"Headers"`
	Query           url.Values        `json:"Query"`
	OriginalRequest []byte            `json:"OriginalRequest"`
	SourceFormat    string            `json:"SourceFormat"`
	Payload         []byte            `json:"Payload"`
	Metadata        map[string]any    `json:"Metadata"`
	StorageJSON     []byte            `json:"StorageJSON"`
	AuthMetadata    map[string]any    `json:"AuthMetadata"`
	AuthAttributes  map[string]string `json:"AuthAttributes"`
}

type executorCallRequest struct {
	executorRequest
	NestedExecutorRequest executorRequest `json:"ExecutorRequest,omitempty"`
	StreamID              string          `json:"stream_id,omitempty"`
	HostCallbackID        string          `json:"host_callback_id,omitempty"`
}

type executorStreamResponse struct {
	Headers http.Header `json:"headers,omitempty"`
}

type hostModelExecutionRequest struct {
	EntryProtocol  string      `json:"entry_protocol"`
	ExitProtocol   string      `json:"exit_protocol"`
	Model          string      `json:"model"`
	Stream         bool        `json:"stream"`
	Body           []byte      `json:"body"`
	Headers        http.Header `json:"headers"`
	Query          url.Values  `json:"query"`
	Alt            string      `json:"alt,omitempty"`
	HostCallbackID string      `json:"host_callback_id,omitempty"`
}

type hostModelExecutionResponse struct {
	StatusCode int         `json:"status_code"`
	Headers    http.Header `json:"headers"`
	Body       []byte      `json:"body"`
}

type hostModelStreamResponse struct {
	StatusCode int         `json:"status_code"`
	Headers    http.Header `json:"headers"`
	StreamID   string      `json:"stream_id"`
}

type hostModelStreamReadRequest struct {
	StreamID string `json:"stream_id"`
}

type hostModelStreamReadResponse struct {
	Payload []byte `json:"payload"`
	Error   string `json:"error"`
	Done    bool   `json:"done"`
}

type hostModelStreamCloseRequest struct {
	StreamID string `json:"stream_id"`
}

type hostStreamEmitRequest struct {
	StreamID string `json:"stream_id"`
	Payload  []byte `json:"payload,omitempty"`
	Error    string `json:"error,omitempty"`
}

type hostStreamCloseRequest struct {
	StreamID string `json:"stream_id"`
	Error    string `json:"error,omitempty"`
}

type usageFailure struct {
	StatusCode int    `json:"StatusCode"`
	Body       string `json:"Body"`
}

type usageDetail struct {
	InputTokens         int64 `json:"InputTokens"`
	OutputTokens        int64 `json:"OutputTokens"`
	ReasoningTokens     int64 `json:"ReasoningTokens"`
	CachedTokens        int64 `json:"CachedTokens"`
	CacheReadTokens     int64 `json:"CacheReadTokens"`
	CacheCreationTokens int64 `json:"CacheCreationTokens"`
	TotalTokens         int64 `json:"TotalTokens"`
}
