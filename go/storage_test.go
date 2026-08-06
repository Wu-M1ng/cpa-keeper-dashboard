package main

import (
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *eventStore {
	t.Helper()
	cfg := defaultConfig()
	cfg.StoragePath = filepath.Join(t.TempDir(), "usage.db")
	store, err := openEventStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.close() })
	return store
}

func TestStorageBatchWritesEventsAndMergesRollups(t *testing.T) {
	store := openTestStore(t)
	now := time.Now().UTC().Truncate(time.Minute)
	events := []usageEvent{
		fixtureEvent(now, "gpt-5.6", false, 100, 20),
		fixtureEvent(now.Add(10*time.Second), "gpt-5.6", true, 40, 5),
	}
	if err := store.writeBatch(events); err != nil {
		t.Fatal(err)
	}
	var eventCount int64
	if err := store.db.QueryRow("SELECT COUNT(*) FROM usage_events").Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 2 {
		t.Fatalf("events = %d, want 2", eventCount)
	}
	var requests, successes, failures, input, output int64
	err := store.db.QueryRow(`SELECT requests, successes, failures, input_tokens, output_tokens
		FROM usage_minute_rollups`).Scan(&requests, &successes, &failures, &input, &output)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || successes != 1 || failures != 1 || input != 140 || output != 25 {
		t.Fatalf("unexpected rollup requests=%d success=%d failure=%d input=%d output=%d", requests, successes, failures, input, output)
	}
	status := store.status()
	if status.JournalMode != "wal" || status.EventCount != 2 || status.RollupCount != 1 {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestStorageBatchIsAtomic(t *testing.T) {
	store := openTestStore(t)
	if _, err := store.db.Exec("DROP TABLE usage_minute_rollups"); err != nil {
		t.Fatal(err)
	}
	err := store.writeBatch([]usageEvent{fixtureEvent(time.Now(), "gpt", false, 1, 1)})
	if err == nil {
		t.Fatal("write should fail without rollup table")
	}
	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM usage_events").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("event transaction was not rolled back: count=%d", count)
	}
}

func TestStorageRetentionPrunesEventsAndRollups(t *testing.T) {
	store := openTestStore(t)
	now := time.Now().UTC()
	if err := store.writeBatch([]usageEvent{
		fixtureEvent(now.Add(-48*time.Hour), "old", false, 1, 1),
		fixtureEvent(now, "new", false, 1, 1),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.prune(1, now); err != nil {
		t.Fatal(err)
	}
	var eventCount, rollupCount int
	_ = store.db.QueryRow("SELECT COUNT(*) FROM usage_events").Scan(&eventCount)
	_ = store.db.QueryRow("SELECT COUNT(*) FROM usage_minute_rollups").Scan(&rollupCount)
	if eventCount != 1 || rollupCount != 1 {
		t.Fatalf("retention left events=%d rollups=%d, want 1/1", eventCount, rollupCount)
	}
}

func fixtureEvent(at time.Time, model string, failed bool, input, output int64) usageEvent {
	return usageEvent{
		TimestampMS:     at.UnixMilli(),
		Provider:        "codex",
		ExecutorType:    "oauth",
		Model:           model,
		APIKeyMask:      "c***ey",
		APIKeyHash:      "api-hash",
		AuthIndex:       "0",
		AuthType:        "oauth",
		UpstreamKey:     "upstream-hash",
		UpstreamLabel:   "codex / 0***",
		Source:          "openai",
		Generate:        true,
		LatencyMS:       100,
		TTFTMS:          30,
		Failed:          failed,
		InputTokens:     input,
		OutputTokens:    output,
		TotalTokens:     input + output,
		CacheReadTokens: input / 2,
	}
}
