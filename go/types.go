package main

import (
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	abiVersion    uint32 = 1
	schemaVersion uint32 = 2
	pluginID             = "usage-keeper"
)

var pluginVersion = "0.1.0"

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Retryable  bool   `json:"retryable,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

type pluginRegisterRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type pluginRegisterResponse struct {
	SchemaVersion uint32             `json:"schema_version"`
	Metadata      pluginMetadata     `json:"metadata"`
	Capabilities  pluginCapabilities `json:"capabilities"`
}

type pluginMetadata struct {
	Name             string        `json:"Name"`
	Version          string        `json:"Version"`
	Author           string        `json:"Author"`
	GitHubRepository string        `json:"GitHubRepository"`
	Logo             string        `json:"Logo"`
	ConfigFields     []configField `json:"ConfigFields"`
}

type configField struct {
	Name        string   `json:"Name"`
	Type        string   `json:"Type"`
	Default     any      `json:"Default,omitempty"`
	EnumValues  []string `json:"EnumValues,omitempty"`
	Description string   `json:"Description"`
}

type pluginCapabilities struct {
	UsagePlugin   bool `json:"usage_plugin,omitempty"`
	ManagementAPI bool `json:"management_api,omitempty"`
}

type managementRegistration struct {
	Routes    []managementRoute `json:"routes"`
	Resources []resourceRoute   `json:"resources"`
}

type managementRoute struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	Description string `json:"description,omitempty"`
}

type resourceRoute struct {
	Path        string `json:"path"`
	Menu        string `json:"menu,omitempty"`
	Description string `json:"description,omitempty"`
}

type managementRequest struct {
	Method  string      `json:"method"`
	Path    string      `json:"path"`
	Headers http.Header `json:"headers"`
	Query   url.Values  `json:"query"`
	Body    []byte      `json:"body"`
}

type managementResponse struct {
	StatusCode int         `json:"status_code"`
	Headers    http.Header `json:"headers"`
	Body       []byte      `json:"body"`
}

type usageRecord struct {
	Provider        string        `json:"provider"`
	ExecutorType    string        `json:"executor_type"`
	Model           string        `json:"model"`
	Alias           string        `json:"alias"`
	APIKey          string        `json:"api_key"`
	AuthID          string        `json:"auth_id"`
	AuthIndex       string        `json:"auth_index"`
	AuthType        string        `json:"auth_type"`
	Endpoint        string        `json:"endpoint"`
	BaseURL         string        `json:"base_url"`
	Source          string        `json:"source"`
	ReasoningEffort string        `json:"reasoning_effort"`
	ServiceTier     string        `json:"service_tier"`
	Stream          bool          `json:"stream"`
	Generate        bool          `json:"generate"`
	RequestedAt     time.Time     `json:"requested_at"`
	Latency         time.Duration `json:"latency"`
	TTFT            time.Duration `json:"ttft"`
	Failed          bool          `json:"failed"`
	Failure         usageFailure  `json:"failure"`
	Detail          usageDetail   `json:"detail"`
}

type usageFailure struct {
	StatusCode int    `json:"status_code"`
	Body       string `json:"body"`
}

type usageDetail struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	ReasoningTokens     int64 `json:"reasoning_tokens"`
	CachedTokens        int64 `json:"cached_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
}

// UnmarshalJSON accepts the current CLIProxyAPI wire format and the older
// PascalCase examples that appeared in early plugin documentation. The
// compatibility path stays on the telemetry callback and performs no I/O.
func (r *usageRecord) UnmarshalJSON(data []byte) error {
	var wire struct {
		Provider        string          `json:"provider"`
		ProviderLegacy  string          `json:"Provider"`
		ExecutorType    string          `json:"executor_type"`
		ExecutorLegacy  string          `json:"ExecutorType"`
		Model           string          `json:"model"`
		ModelLegacy     string          `json:"Model"`
		Alias           string          `json:"alias"`
		AliasLegacy     string          `json:"Alias"`
		APIKey          string          `json:"api_key"`
		APIKeyLegacy    string          `json:"APIKey"`
		AuthID          string          `json:"auth_id"`
		AuthIDLegacy    string          `json:"AuthID"`
		AuthIndex       string          `json:"auth_index"`
		AuthIndexLegacy string          `json:"AuthIndex"`
		AuthType        string          `json:"auth_type"`
		AuthTypeLegacy  string          `json:"AuthType"`
		Endpoint        string          `json:"endpoint"`
		EndpointLegacy  string          `json:"Endpoint"`
		BaseURL         string          `json:"base_url"`
		BaseURLCamel    string          `json:"baseURL"`
		BaseURLLegacy   string          `json:"BaseURL"`
		Source          string          `json:"source"`
		SourceLegacy    string          `json:"Source"`
		Reasoning       string          `json:"reasoning_effort"`
		ReasoningOld    string          `json:"ReasoningEffort"`
		ServiceTier     string          `json:"service_tier"`
		ServiceTierOld  string          `json:"ServiceTier"`
		Stream          bool            `json:"stream"`
		StreamLegacy    bool            `json:"Stream"`
		Streaming       bool            `json:"streaming"`
		StreamingLegacy bool            `json:"Streaming"`
		Generate        bool            `json:"generate"`
		GenerateLegacy  bool            `json:"Generate"`
		RequestedAt     json.RawMessage `json:"requested_at"`
		RequestedAtMS   json.RawMessage `json:"requested_at_ms"`
		RequestedOld    json.RawMessage `json:"RequestedAt"`
		RequestedOldMS  json.RawMessage `json:"RequestedAtMs"`
		Latency         json.RawMessage `json:"latency"`
		LatencyMS       json.RawMessage `json:"latency_ms"`
		LatencyOld      json.RawMessage `json:"Latency"`
		LatencyOldMS    json.RawMessage `json:"LatencyMs"`
		TTFT            json.RawMessage `json:"ttft"`
		TTFTMS          json.RawMessage `json:"ttft_ms"`
		TTFTOld         json.RawMessage `json:"TTFT"`
		TTFTOldMS       json.RawMessage `json:"TTFTMs"`
		Failed          bool            `json:"failed"`
		FailedLegacy    bool            `json:"Failed"`
		Failure         usageFailure    `json:"failure"`
		FailureLegacy   usageFailure    `json:"Failure"`
		Detail          usageDetail     `json:"detail"`
		DetailLegacy    usageDetail     `json:"Detail"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}

	*r = usageRecord{
		Provider:        firstNonEmpty(wire.Provider, wire.ProviderLegacy),
		ExecutorType:    firstNonEmpty(wire.ExecutorType, wire.ExecutorLegacy),
		Model:           firstNonEmpty(wire.Model, wire.ModelLegacy),
		Alias:           firstNonEmpty(wire.Alias, wire.AliasLegacy),
		APIKey:          firstNonEmpty(wire.APIKey, wire.APIKeyLegacy),
		AuthID:          firstNonEmpty(wire.AuthID, wire.AuthIDLegacy),
		AuthIndex:       firstNonEmpty(wire.AuthIndex, wire.AuthIndexLegacy),
		AuthType:        firstNonEmpty(wire.AuthType, wire.AuthTypeLegacy),
		Endpoint:        firstNonEmpty(wire.Endpoint, wire.EndpointLegacy),
		BaseURL:         firstNonEmpty(wire.BaseURL, wire.BaseURLCamel, wire.BaseURLLegacy),
		Source:          firstNonEmpty(wire.Source, wire.SourceLegacy),
		ReasoningEffort: firstNonEmpty(wire.Reasoning, wire.ReasoningOld),
		ServiceTier:     firstNonEmpty(wire.ServiceTier, wire.ServiceTierOld),
		Stream:          wire.Stream || wire.StreamLegacy || wire.Streaming || wire.StreamingLegacy,
		Generate:        wire.Generate || wire.GenerateLegacy,
		RequestedAt:     parseUsageTime(firstUsageRaw(wire.RequestedAt, wire.RequestedAtMS, wire.RequestedOld, wire.RequestedOldMS)),
		Latency: firstUsageDuration(
			parseUsageDuration(wire.Latency, time.Nanosecond),
			parseUsageDuration(wire.LatencyMS, time.Millisecond),
			parseUsageDuration(wire.LatencyOld, time.Nanosecond),
			parseUsageDuration(wire.LatencyOldMS, time.Millisecond),
		),
		TTFT: firstUsageDuration(
			parseUsageDuration(wire.TTFT, time.Nanosecond),
			parseUsageDuration(wire.TTFTMS, time.Millisecond),
			parseUsageDuration(wire.TTFTOld, time.Nanosecond),
			parseUsageDuration(wire.TTFTOldMS, time.Millisecond),
		),
		Failed:  wire.Failed || wire.FailedLegacy,
		Failure: wire.Failure,
		Detail:  wire.Detail,
	}
	if r.Failure == (usageFailure{}) {
		r.Failure = wire.FailureLegacy
	}
	if r.Detail == (usageDetail{}) {
		r.Detail = wire.DetailLegacy
	}
	return nil
}

func (f *usageFailure) UnmarshalJSON(data []byte) error {
	var wire struct {
		StatusCode       int    `json:"status_code"`
		StatusCodeLegacy int    `json:"StatusCode"`
		Body             string `json:"body"`
		BodyLegacy       string `json:"Body"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	f.StatusCode = wire.StatusCode
	if f.StatusCode == 0 {
		f.StatusCode = wire.StatusCodeLegacy
	}
	f.Body = firstNonEmpty(wire.Body, wire.BodyLegacy)
	return nil
}

func (d *usageDetail) UnmarshalJSON(data []byte) error {
	var wire struct {
		InputTokens         int64 `json:"input_tokens"`
		InputLegacy         int64 `json:"InputTokens"`
		OutputTokens        int64 `json:"output_tokens"`
		OutputLegacy        int64 `json:"OutputTokens"`
		ReasoningTokens     int64 `json:"reasoning_tokens"`
		ReasoningLegacy     int64 `json:"ReasoningTokens"`
		CachedTokens        int64 `json:"cached_tokens"`
		CachedLegacy        int64 `json:"CachedTokens"`
		CacheReadTokens     int64 `json:"cache_read_tokens"`
		CacheReadLegacy     int64 `json:"CacheReadTokens"`
		CacheCreationTokens int64 `json:"cache_creation_tokens"`
		CacheCreateLegacy   int64 `json:"CacheCreationTokens"`
		TotalTokens         int64 `json:"total_tokens"`
		TotalLegacy         int64 `json:"TotalTokens"`
		PromptTokens        int64 `json:"prompt_tokens"`
		CompletionTokens    int64 `json:"completion_tokens"`
		CacheReadAlias      int64 `json:"cache_read_input_tokens"`
		CacheWriteAlias     int64 `json:"cache_creation_input_tokens"`
		PromptDetails       struct {
			Cached int64 `json:"cached_tokens"`
			Write  int64 `json:"cache_write_tokens"`
			Create int64 `json:"cache_creation_tokens"`
		} `json:"prompt_tokens_details"`
		InputDetails struct {
			Cached int64 `json:"cached_tokens"`
			Write  int64 `json:"cache_write_tokens"`
			Create int64 `json:"cache_creation_tokens"`
		} `json:"input_tokens_details"`
		CompletionDetails struct {
			Reasoning int64 `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	d.InputTokens = firstUsageInt64(wire.InputTokens, wire.PromptTokens, wire.InputLegacy)
	d.OutputTokens = firstUsageInt64(wire.OutputTokens, wire.CompletionTokens, wire.OutputLegacy)
	d.ReasoningTokens = firstUsageInt64(wire.ReasoningTokens, wire.CompletionDetails.Reasoning, wire.ReasoningLegacy)
	d.CachedTokens = firstUsageInt64(wire.CachedTokens, wire.PromptDetails.Cached, wire.InputDetails.Cached, wire.CachedLegacy)
	d.CacheReadTokens = firstUsageInt64(wire.CacheReadTokens, wire.CacheReadAlias, wire.PromptDetails.Cached, wire.InputDetails.Cached, wire.CacheReadLegacy)
	d.CacheCreationTokens = firstUsageInt64(wire.CacheCreationTokens, wire.CacheWriteAlias, wire.PromptDetails.Write, wire.PromptDetails.Create, wire.InputDetails.Write, wire.InputDetails.Create, wire.CacheCreateLegacy)
	d.TotalTokens = firstUsageInt64(wire.TotalTokens, wire.TotalLegacy)
	return nil
}

func firstUsageRaw(values ...json.RawMessage) json.RawMessage {
	for _, value := range values {
		trimmed := strings.TrimSpace(string(value))
		if trimmed != "" && trimmed != "null" {
			return value
		}
	}
	return nil
}

func parseUsageTime(raw json.RawMessage) time.Time {
	rawValue := firstUsageRaw(raw)
	if len(rawValue) == 0 {
		return time.Time{}
	}
	var text string
	if json.Unmarshal(rawValue, &text) == nil {
		text = strings.TrimSpace(text)
		if text == "" {
			return time.Time{}
		}
		if parsed, err := time.Parse(time.RFC3339Nano, text); err == nil {
			return parsed
		}
		if value, err := strconv.ParseFloat(text, 64); err == nil {
			return unixUsageTime(value)
		}
		return time.Time{}
	}
	var number float64
	if json.Unmarshal(rawValue, &number) != nil {
		return time.Time{}
	}
	return unixUsageTime(number)
}

func unixUsageTime(value float64) time.Time {
	if value == 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return time.Time{}
	}
	abs := math.Abs(value)
	switch {
	case abs >= 1e17:
		return time.Unix(int64(value/1e9), int64(math.Round(value-math.Trunc(value/1e9)*1e9))).UTC()
	case abs >= 1e12:
		return time.Unix(int64(value/1e3), int64(math.Round((value-math.Trunc(value/1e3)*1e3)*1e6))).UTC()
	default:
		return time.Unix(int64(value), int64(math.Round((value-math.Trunc(value))*1e9))).UTC()
	}
}

func parseUsageDuration(raw json.RawMessage, unit time.Duration) time.Duration {
	raw = firstUsageRaw(raw)
	if len(raw) == 0 {
		return 0
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		text = strings.TrimSpace(text)
		if duration, err := time.ParseDuration(text); err == nil {
			return duration
		}
		if value, err := strconv.ParseFloat(text, 64); err == nil {
			return time.Duration(math.Round(value * float64(unit)))
		}
		return 0
	}
	var value float64
	if json.Unmarshal(raw, &value) != nil {
		return 0
	}
	return time.Duration(math.Round(value * float64(unit)))
}

func firstUsageDuration(values ...time.Duration) time.Duration {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func firstUsageInt64(values ...int64) int64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

type usageEvent struct {
	ID                  int64  `json:"id"`
	TimestampMS         int64  `json:"timestamp_ms"`
	Provider            string `json:"provider"`
	ExecutorType        string `json:"executor_type,omitempty"`
	Model               string `json:"model"`
	Alias               string `json:"alias,omitempty"`
	APIKeyMask          string `json:"api_key"`
	APIKeyHash          string `json:"api_key_hash"`
	AuthID              string `json:"auth_id,omitempty"`
	AuthIndex           string `json:"auth_index,omitempty"`
	AuthType            string `json:"auth_type,omitempty"`
	UpstreamKey         string `json:"upstream_key"`
	UpstreamLabel       string `json:"upstream_label"`
	Source              string `json:"source"`
	ReasoningEffort     string `json:"reasoning_effort,omitempty"`
	ServiceTier         string `json:"service_tier,omitempty"`
	Generate            bool   `json:"generate"`
	LatencyMS           int64  `json:"latency_ms"`
	TTFTMS              int64  `json:"ttft_ms"`
	Failed              bool   `json:"failed"`
	StatusCode          int    `json:"status_code,omitempty"`
	Failure             string `json:"failure,omitempty"`
	InputTokens         int64  `json:"input_tokens"`
	OutputTokens        int64  `json:"output_tokens"`
	ReasoningTokens     int64  `json:"reasoning_tokens"`
	CachedTokens        int64  `json:"cached_tokens"`
	CacheReadTokens     int64  `json:"cache_read_tokens"`
	CacheCreationTokens int64  `json:"cache_creation_tokens"`
	TotalTokens         int64  `json:"total_tokens"`
}

type storageStatus struct {
	Enabled       bool   `json:"enabled"`
	Path          string `json:"path"`
	JournalMode   string `json:"journal_mode"`
	DatabaseBytes int64  `json:"database_bytes"`
	EventCount    int64  `json:"event_count"`
	RollupCount   int64  `json:"rollup_count"`
	LastWriteAt   string `json:"last_write_at,omitempty"`
	LastError     string `json:"last_error,omitempty"`
}

type runtimeStatus struct {
	Accepted      uint64        `json:"accepted"`
	Dropped       uint64        `json:"dropped"`
	Written       uint64        `json:"written"`
	WriteFailures uint64        `json:"write_failures"`
	QueueDepth    int           `json:"queue_depth"`
	QueueCapacity int           `json:"queue_capacity"`
	LastBatchSize int64         `json:"last_batch_size"`
	LastBatchMS   float64       `json:"last_batch_ms"`
	StartedAt     string        `json:"started_at"`
	Storage       storageStatus `json:"storage"`
	RetentionDays int           `json:"retention_days"`
	BatchSize     int           `json:"batch_size"`
	FlushInterval int           `json:"flush_interval_ms"`
}
