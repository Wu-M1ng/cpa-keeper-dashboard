package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestUsageHandleKeepsRuntimeOpenThroughEnqueue(t *testing.T) {
	shutdownRuntime()
	cfg := defaultConfig()
	cfg.StoragePath = filepath.Join(t.TempDir(), "usage.db")
	t.Cleanup(shutdownRuntime)
	if err := configureRuntime(cfg); err != nil {
		t.Fatal(err)
	}
	previous := currentRuntime()
	previous.queueMu.Lock()
	queueLocked := true
	defer func() {
		if queueLocked {
			previous.queueMu.Unlock()
		}
	}()

	handled := make(chan []byte, 1)
	go func() {
		handled <- handleUsage([]byte(`{"provider":"codex","model":"queued-before-reconfigure"}`))
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if !runtimeMu.TryLock() {
			break
		}
		runtimeMu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("usage handler released the runtime before enqueue completed")
		}
		time.Sleep(time.Millisecond)
	}

	reconfigured := make(chan error, 1)
	go func() { reconfigured <- configureRuntime(cfg) }()
	previous.queueMu.Unlock()
	queueLocked = false
	select {
	case raw := <-handled:
		var response envelope
		if err := json.Unmarshal(raw, &response); err != nil || !response.OK {
			t.Fatalf("usage response=%s err=%v", raw, err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("usage handler did not finish")
	}
	select {
	case err := <-reconfigured:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("reconfiguration did not finish")
	}
	if previous.dropped.Load() != 0 {
		t.Fatalf("in-flight usage was dropped during reconfiguration: %d", previous.dropped.Load())
	}
	var count int
	if err := currentRuntime().store.db.QueryRow("SELECT COUNT(*) FROM usage_events").Scan(&count); err != nil || count != 1 {
		t.Fatalf("event count after reconfiguration=%d err=%v", count, err)
	}
}

func TestUsageHandleDecodesOfficialJSON(t *testing.T) {
	runtime := &pluginRuntime{
		config: runtimeConfig{APIKeyHashSalt: "salt"},
		queue:  make(chan writerQueueItem, 1),
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
	case item := <-runtime.queue:
		event := item.event
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

func TestCompactUsageRecordMapsOfficialFieldsAndMasksIdentifiers(t *testing.T) {
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
		Endpoint:     "/v1/chat/completions?api_key=plain-secret#fragment",
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
	if event.Endpoint != "/v1/chat/completions" {
		t.Fatalf("endpoint = %q, want sanitized path", event.Endpoint)
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
	for _, secret := range []string{"account@example.com", "plain-secret", "secret-token", "sk-live-123456789"} {
		if strings.Contains(event.Failure, secret) {
			t.Fatalf("failure text exposed %q: %q", secret, event.Failure)
		}
	}
	if !strings.Contains(event.Failure, "rate limited") {
		t.Fatalf("failure text lost its diagnostic message: %q", event.Failure)
	}
	if event.UpstreamKey == "" || event.UpstreamLabel == "" {
		t.Fatalf("upstream identity missing: %+v", event)
	}
	if event.UpstreamLabel != "codex / acc***89" || event.Source != event.UpstreamLabel {
		t.Fatalf("provider credential label = %q/%q, want first-three/last-two mask", event.UpstreamLabel, event.Source)
	}
}

func TestMaskProviderCredentialKeepsFirstThreeAndLastTwoCharacters(t *testing.T) {
	tests := map[string]string{
		"5312415661d8a481":         "531***81",
		"account@example.com":      "acc***om",
		"sk-channel-secret-123456": "sk-***56",
		"public":                   "pub***ic",
		"0":                        "0***",
	}
	for input, want := range tests {
		if got := maskProviderCredential(input); got != want {
			t.Errorf("maskProviderCredential(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestEnqueueDropsImmediatelyWhenQueueIsFull(t *testing.T) {
	r := &pluginRuntime{
		config: runtimeConfig{APIKeyHashSalt: "salt"},
		queue:  make(chan writerQueueItem, 1),
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

func TestConcurrentEnqueuePreservesRestoreBarrierSlot(t *testing.T) {
	r := &pluginRuntime{
		config: runtimeConfig{APIKeyHashSalt: "salt", QueueSize: 2},
		queue:  make(chan writerQueueItem, 3),
	}
	record := usageRecord{Model: "gpt", Provider: "codex", RequestedAt: time.Now()}
	var workers sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 32; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			r.enqueue(record)
		}()
	}
	close(start)
	workers.Wait()
	if got := len(r.queue); got != 2 {
		t.Fatalf("queued events=%d, want 2 with one barrier slot reserved", got)
	}
	if got := r.queuedEvents.Load(); got != 2 {
		t.Fatalf("admission counter=%d, want 2", got)
	}
	select {
	case r.queue <- writerQueueItem{barrier: &writerBarrier{}}:
	default:
		t.Fatal("concurrent enqueue consumed the restore barrier slot")
	}
}

func TestPruneExpiredUsesCurrentRetentionDays(t *testing.T) {
	store := openTestStore(t)
	now := time.Now().UTC()
	if err := store.writeBatch([]usageEvent{fixtureEvent(now.Add(-48*time.Hour), "gpt", false, 1, 1)}); err != nil {
		t.Fatal(err)
	}
	runtime := &pluginRuntime{config: runtimeConfig{RetentionDays: 1}, store: store}
	runtime.configMu.Lock()
	runtime.config.RetentionDays = 10
	runtime.configMu.Unlock()
	if err := runtime.pruneExpired(now); err != nil {
		t.Fatal(err)
	}
	if count := store.status().EventCount; count != 1 {
		t.Fatalf("dynamic retention pruned current data: count=%d", count)
	}
}

func TestPruneExpiredSerializesWithRetentionUpdates(t *testing.T) {
	store := openTestStore(t)
	now := time.Now().UTC()
	if err := store.writeBatch([]usageEvent{fixtureEvent(now.AddDate(0, 0, -40), "keep-for-sixty-days", false, 1, 1)}); err != nil {
		t.Fatal(err)
	}
	runtime := &pluginRuntime{config: runtimeConfig{RetentionDays: 30}, store: store}
	runtime.settingsMu.Lock()
	done := make(chan error, 1)
	go func() { done <- runtime.pruneExpired(now) }()

	select {
	case err := <-done:
		runtime.settingsMu.Unlock()
		t.Fatalf("background prune bypassed settings serialization: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	runtime.configMu.Lock()
	runtime.config.RetentionDays = 60
	runtime.configMu.Unlock()
	runtime.settingsMu.Unlock()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("background prune did not resume after settings update")
	}
	if count := store.status().EventCount; count != 1 {
		t.Fatalf("background prune used stale retention and removed data: count=%d", count)
	}
}

func TestSanitizeEndpointKeepsNonSensitivePathVisible(t *testing.T) {
	input := "https://proxy.local/v1/account%40example.com/sk-live-123456789/chat?api_key=secret#fragment"
	want := "https://proxy.local/v1/acc***om/***/chat"
	if got := sanitizeEndpoint(input); got != want {
		t.Fatalf("sanitizeEndpoint() = %q, want %q", got, want)
	}
}

func TestSanitizeFailureRedactsBeforeBoundingAndCleaningControls(t *testing.T) {
	input := "  account@example.com Bearer secret-token\n" + strings.Repeat("x", 600)
	got := sanitizeFailure(input)
	if strings.Contains(got, "\n") || strings.Contains(got, "account@example.com") || strings.Contains(got, "secret-token") {
		t.Fatalf("failure text exposed an identifier or retained controls: %q", got)
	}
	if len(got) > 512 {
		t.Fatalf("failure text length = %d, want at most 512", len(got))
	}
}

func TestSanitizersKeepOversizedEventFieldsBounded(t *testing.T) {
	dimension := "model-" + strings.Repeat("x", 4096)
	if got := cleanDimension(dimension, "unknown"); len(got) != 160 || got != dimension[:160] {
		t.Fatalf("cleanDimension length/value = %d/%q, want 160-byte prefix", len(got), got)
	}

	endpoint := "https://proxy.local/" + strings.Repeat("x", 4096)
	if got := sanitizeEndpoint(endpoint); len(got) != 256 || got != endpoint[:256] {
		t.Fatalf("sanitizeEndpoint length/value = %d/%q, want 256-byte prefix", len(got), got)
	}

	failure := strings.Repeat("x", 4096)
	if got := sanitizeFailure(failure); len(got) != 512 || got != failure[:512] {
		t.Fatalf("sanitizeFailure length/value = %d/%q, want 512-byte prefix", len(got), got)
	}
}

func BenchmarkEnqueue(b *testing.B) {
	r := &pluginRuntime{
		config: runtimeConfig{APIKeyHashSalt: "benchmark"},
		queue:  make(chan writerQueueItem, b.N+1),
	}
	record := usageRecord{Model: "gpt-5.6", Provider: "codex", APIKey: "client-key", RequestedAt: time.Now()}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.enqueue(record)
	}
}
