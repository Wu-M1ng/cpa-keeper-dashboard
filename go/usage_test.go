package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestUsageHandleDecodesOfficialJSON(t *testing.T) {
	runtime := &pluginRuntime{
		config: runtimeConfig{APIKeyHashSalt: "salt"},
		queue:  make(chan usageEvent, 1),
	}
	runtimeMu.Lock()
	previous := activeRuntime
	activeRuntime = runtime
	runtimeMu.Unlock()
	t.Cleanup(func() {
		runtimeMu.Lock()
		activeRuntime = previous
		runtimeMu.Unlock()
	})

	raw := []byte(`{"provider":"codex","executor_type":"oauth","model":"gpt-5.6","alias":"fast","api_key":"sk-client-abcdefghijk","auth_id":"account-123","auth_index":"4","auth_type":"oauth","source":"openai","stream":true,"requested_at":"2023-11-14T22:13:20Z","latency":1250000000,"ttft":220000000,"failed":true,"failure":{"status_code":429,"body":"Bearer secret-token"},"detail":{"input_tokens":100,"output_tokens":25,"reasoning_tokens":5,"cache_read_input_tokens":40,"cache_creation_input_tokens":3}}`)
	response, err := handleMethod("usage.handle", raw)
	if err != nil {
		t.Fatalf("usage.handle returned error: %v", err)
	}
	var envelope envelope
	if err := json.Unmarshal(response, &envelope); err != nil || !envelope.OK {
		t.Fatalf("unexpected usage response: %s", response)
	}

	select {
	case event := <-runtime.queue:
		if event.Provider != "codex" || event.Model != "gpt-5.6" || event.InputTokens != 100 || event.OutputTokens != 25 {
			t.Fatalf("official usage fields were not decoded: %+v", event)
		}
		if event.TimestampMS != 1700000000000 || event.LatencyMS != 1250 || event.TTFTMS != 220 {
			t.Fatalf("official timing fields were not decoded: %+v", event)
		}
		if !event.Generate || event.StatusCode != 429 || event.CacheReadTokens != 40 || event.CacheCreationTokens != 3 {
			t.Fatalf("official aliases were not decoded: %+v", event)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("usage.handle did not enqueue a decoded event")
	}
}

func TestUsageHandleAcceptsLegacyPascalCaseJSON(t *testing.T) {
	var record usageRecord
	err := json.Unmarshal([]byte(`{"Provider":"legacy","Model":"model","RequestedAt":"2023-11-14T22:13:20Z","Latency":1000000,"Detail":{"InputTokens":4,"OutputTokens":6},"Failure":{"StatusCode":500,"Body":"failure"}}`), &record)
	if err != nil {
		t.Fatal(err)
	}
	if record.Provider != "legacy" || record.Model != "model" || record.Latency != time.Millisecond || record.Detail.TotalTokens != 0 {
		t.Fatalf("legacy usage fields were not decoded: %+v", record)
	}
	if record.Detail.InputTokens != 4 || record.Detail.OutputTokens != 6 || record.Failure.StatusCode != 500 {
		t.Fatalf("legacy nested fields were not decoded: %+v", record)
	}
}

func TestCompactUsageRecordMapsOfficialFieldsAndRedactsSecrets(t *testing.T) {
	record := usageRecord{
		Provider:     "codex",
		ExecutorType: "oauth",
		Model:        "gpt-5.6",
		Alias:        "fast",
		APIKey:       "sk-client-abcdefghijk",
		AuthID:       "account-123456789",
		AuthIndex:    "4",
		AuthType:     "oauth",
		Source:       "openai",
		RequestedAt:  time.Unix(1_700_000_000, 0).UTC(),
		Latency:      1250 * time.Millisecond,
		TTFT:         220 * time.Millisecond,
		Failed:       true,
		Failure: usageFailure{
			StatusCode: 429,
			Body:       "account@example.com api_key=plain-secret Authorization: Bearer secret-token sk-live-123456789 rate limited",
		},
		Detail: usageDetail{
			InputTokens:         100,
			OutputTokens:        25,
			ReasoningTokens:     5,
			CachedTokens:        40,
			CacheCreationTokens: 3,
			TotalTokens:         125,
		},
	}
	event := compactUsageRecord(record, "test-salt")
	if event.TimestampMS != record.RequestedAt.UnixMilli() || event.LatencyMS != 1250 || event.TTFTMS != 220 {
		t.Fatalf("time fields not mapped: %+v", event)
	}
	if event.CacheReadTokens != 40 || event.CacheCreationTokens != 3 || event.TotalTokens != 125 {
		t.Fatalf("token fields not mapped: %+v", event)
	}
	if event.APIKeyHash == "" || strings.Contains(event.APIKeyMask, "abcdefghijk") {
		t.Fatalf("API key was not masked: %+v", event)
	}
	if event.AuthID != "" || event.AuthIndex != "" || event.AuthType != "" {
		t.Fatalf("raw account identity entered the usage event: %+v", event)
	}
	if strings.Contains(event.UpstreamLabel, "123456789") || strings.Contains(event.Source, "openai") {
		t.Fatalf("account or source identity was not anonymized: %+v", event)
	}
	if strings.Contains(event.Failure, "secret-token") || strings.Contains(event.Failure, "sk-live") ||
		strings.Contains(event.Failure, "plain-secret") || strings.Contains(event.Failure, "account@example.com") {
		t.Fatalf("failure leaked a secret: %q", event.Failure)
	}
	if event.UpstreamKey == "" || event.UpstreamLabel == "" {
		t.Fatalf("upstream identity missing: %+v", event)
	}
}

func TestEnqueueDropsImmediatelyWhenQueueIsFull(t *testing.T) {
	r := &pluginRuntime{
		config: runtimeConfig{APIKeyHashSalt: "salt"},
		queue:  make(chan usageEvent, 1),
	}
	record := usageRecord{Model: "gpt", Provider: "codex", RequestedAt: time.Now()}
	if !r.enqueue(record) {
		t.Fatal("first enqueue should succeed")
	}
	started := time.Now()
	if r.enqueue(record) {
		t.Fatal("second enqueue should drop")
	}
	if elapsed := time.Since(started); elapsed > 20*time.Millisecond {
		t.Fatalf("full queue blocked for %v", elapsed)
	}
	if r.accepted.Load() != 1 || r.dropped.Load() != 1 {
		t.Fatalf("unexpected counters accepted=%d dropped=%d", r.accepted.Load(), r.dropped.Load())
	}
}

func BenchmarkEnqueue(b *testing.B) {
	r := &pluginRuntime{
		config: runtimeConfig{APIKeyHashSalt: "benchmark"},
		queue:  make(chan usageEvent, b.N+1),
	}
	record := usageRecord{Model: "gpt-5.6", Provider: "codex", APIKey: "client-key", RequestedAt: time.Now()}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.enqueue(record)
	}
}
