package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
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

func TestStorageDoesNotPersistRawClientIdentifiers(t *testing.T) {
	store := openTestStore(t)
	event := fixtureEvent(time.Now().UTC(), "gpt-5.6", false, 10, 5)
	event.AuthID = "account@example.com"
	event.AuthIndex = "account-index"
	event.AuthType = "oauth"
	event.APIKeyMask = "sk-...secret"
	event.Source = "account@example.com"
	event.UpstreamLabel = "codex / account@example.com"
	if err := store.writeBatch([]usageEvent{event}); err != nil {
		t.Fatal(err)
	}
	var apiLabel, authID, authIndex, authType, upstreamLabel, source string
	err := store.db.QueryRow(`SELECT api_key_mask, auth_id, auth_index, auth_type, upstream_label, source
		FROM usage_events LIMIT 1`).Scan(&apiLabel, &authID, &authIndex, &authType, &upstreamLabel, &source)
	if err != nil {
		t.Fatal(err)
	}
	if authID != "" || authIndex != "" || authType != "" {
		t.Fatalf("stored raw auth identity: id=%q index=%q type=%q", authID, authIndex, authType)
	}
	for name, value := range map[string]string{"api label": apiLabel, "upstream label": upstreamLabel, "source": source} {
		if strings.Contains(value, "secret") || strings.Contains(value, "account") || strings.Contains(value, "example.com") {
			t.Fatalf("stored %s exposed an identifier: %q", name, value)
		}
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

func TestStorageRetentionRecordsTransactionFailure(t *testing.T) {
	store := openTestStore(t)
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	if err := store.prune(30, time.Now()); err == nil {
		t.Fatal("retention on a closed database must fail")
	}
	if status := store.statusSnapshot(); status.LastError == "" {
		t.Fatal("retention transaction failure is missing from storage status")
	}
}

func TestStorageRetentionErrorSurvivesWritesUntilRetry(t *testing.T) {
	store := openTestStore(t)
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	if err := store.writeBatch([]usageEvent{fixtureEvent(now.AddDate(0, 0, -40), "expired", false, 100, 10)}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER reject_retention BEFORE DELETE ON usage_minute_rollups
		BEGIN SELECT RAISE(ABORT, 'forced retention failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.prune(30, now); err == nil {
		t.Fatal("retention must report the failing delete")
	}
	if err := store.writeBatch([]usageEvent{fixtureEvent(now, "current", false, 200, 20)}); err != nil {
		t.Fatal(err)
	}
	if status := store.status(); status.EventCount != 2 || status.RollupCount != 2 || status.LastError == "" {
		t.Fatalf("normal usage erased the retention failure: %+v", status)
	}
	if _, err := store.db.Exec("DROP TRIGGER reject_retention"); err != nil {
		t.Fatal(err)
	}
	if err := store.prune(30, now); err != nil {
		t.Fatal(err)
	}
	if status := store.status(); status.EventCount != 1 || status.RollupCount != 1 || status.LastError != "" {
		t.Fatalf("retention retry did not recover: %+v", status)
	}
}

func TestStorageRetentionRebuildsPartialCutoffMinute(t *testing.T) {
	for _, keepBoundaryEvent := range []bool{false, true} {
		t.Run(map[bool]string{false: "fully expired minute", true: "partially expired minute"}[keepBoundaryEvent], func(t *testing.T) {
			store := openTestStore(t)
			now := time.Date(2026, 9, 9, 12, 0, 30, 0, chinaStandardTime)
			cutoff := now.AddDate(0, 0, -30)
			expired := fixtureEvent(cutoff.Add(-10*time.Second), "gpt", false, 100, 10)
			current := fixtureEvent(now, "gpt", false, 300, 30)
			retained := fixtureEvent(cutoff, "gpt", true, 200, 20)
			retained.CacheCreationTokens = 7
			retained.ReasoningTokens = 9
			retained.TTFTMS = 0
			events := []usageEvent{expired, current}
			wantEvents, wantTokens := int64(1), current.TotalTokens
			if keepBoundaryEvent {
				events = append(events, retained)
				wantEvents++
				wantTokens += retained.TotalTokens
			}
			if err := store.writeBatch(events); err != nil {
				t.Fatal(err)
			}
			if err := store.prune(30, now); err != nil {
				t.Fatal(err)
			}
			status := store.status()
			if status.EventCount != wantEvents || status.RollupCount != wantEvents {
				t.Fatalf("expired minute remains in storage: %+v, want %d", status, wantEvents)
			}
			var requests, tokens, cacheWrite, reasoning, failures, ttftCount int64
			if err := store.db.QueryRow(`SELECT SUM(requests), SUM(total_tokens), SUM(cache_creation_tokens),
				SUM(reasoning_tokens), SUM(failures), SUM(ttft_count) FROM usage_minute_rollups`).Scan(
				&requests, &tokens, &cacheWrite, &reasoning, &failures, &ttftCount); err != nil {
				t.Fatal(err)
			}
			if requests != wantEvents || tokens != wantTokens || ttftCount != 1 {
				t.Fatalf("rollups disagree with retained events: requests=%d tokens=%d ttft_count=%d", requests, tokens, ttftCount)
			}
			if keepBoundaryEvent && (cacheWrite != 7 || reasoning != 9 || failures != 1) {
				t.Fatalf("cutoff minute lost retained metrics: cache_write=%d reasoning=%d failures=%d", cacheWrite, reasoning, failures)
			}
		})
	}
}

func TestStorageMigratesEndpointColumnWithoutLosingEvents(t *testing.T) {
	cfg := defaultConfig()
	cfg.StoragePath = filepath.Join(t.TempDir(), "legacy.db")
	store, err := openEventStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.writeBatch([]usageEvent{fixtureEvent(time.Now().UTC(), "gpt-5.6", false, 10, 5)}); err != nil {
		t.Fatal(err)
	}
	if err := store.close(); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(cfg.StoragePath))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query("PRAGMA table_info(usage_events)")
	if err != nil {
		t.Fatal(err)
	}
	hasEndpoint := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		hasEndpoint = hasEndpoint || name == "endpoint"
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if hasEndpoint {
		if _, err := db.Exec("ALTER TABLE usage_events DROP COLUMN endpoint"); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := openEventStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.close() })
	var count int
	var endpoint string
	if err := reopened.db.QueryRow("SELECT COUNT(*), COALESCE(MAX(endpoint), '') FROM usage_events").Scan(&count, &endpoint); err != nil {
		t.Fatal(err)
	}
	if count != 1 || endpoint != "" {
		t.Fatalf("migration retained count=%d endpoint=%q, want 1 and empty path", count, endpoint)
	}
}

func TestStorageAppliesConnectionPragmasToEntirePool(t *testing.T) {
	store := openTestStore(t)
	connections := make([]*sql.Conn, 0, 4)
	for i := 0; i < sqliteMaxOpenConnections; i++ {
		connection, err := store.db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, connection)
	}
	defer func() {
		for _, connection := range connections {
			_ = connection.Close()
		}
	}()

	for index, connection := range connections {
		var busyTimeout, synchronous, tempStore, cacheSize, pageSize int
		if err := connection.QueryRowContext(context.Background(), "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
			t.Fatal(err)
		}
		if err := connection.QueryRowContext(context.Background(), "PRAGMA synchronous").Scan(&synchronous); err != nil {
			t.Fatal(err)
		}
		if err := connection.QueryRowContext(context.Background(), "PRAGMA temp_store").Scan(&tempStore); err != nil {
			t.Fatal(err)
		}
		if err := connection.QueryRowContext(context.Background(), "PRAGMA cache_size").Scan(&cacheSize); err != nil {
			t.Fatal(err)
		}
		if err := connection.QueryRowContext(context.Background(), "PRAGMA page_size").Scan(&pageSize); err != nil {
			t.Fatal(err)
		}
		if busyTimeout != 2000 || synchronous != 1 || tempStore != 1 || cacheSize != -sqliteCacheKiBPerConnection || pageSize != sqlitePageSizeKiB*1024 {
			t.Fatalf("connection %d pragmas busy=%d synchronous=%d temp_store=%d cache_size=%d page_size=%d", index, busyTimeout, synchronous, tempStore, cacheSize, pageSize)
		}
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
