package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func testWriter(t *testing.T) *pluginRuntime {
	t.Helper()
	cfg := defaultConfig()
	cfg.BatchSize = 2
	cfg.FlushIntervalMS = 25
	r := &pluginRuntime{config: cfg, store: openTestStore(t), queue: make(chan writerQueueItem, 8), done: make(chan struct{}), readCache: newManagementReadCache()}
	return r
}

func testRestoringRuntime(t *testing.T) *pluginRuntime {
	t.Helper()
	cfg := defaultConfig()
	cfg.StoragePath = filepath.Join(t.TempDir(), "restore.db")
	cfg.QueueSize = 8
	cfg.BatchSize = 8
	cfg.FlushIntervalMS = 25
	r, err := newPluginRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	runtimeMu.Lock()
	previous := activeRuntime
	activeRuntime = r
	runtimeMu.Unlock()
	t.Cleanup(func() {
		r.close()
		runtimeMu.Lock()
		activeRuntime = previous
		runtimeMu.Unlock()
	})
	return r
}

func waitForWritten(t *testing.T, r *pluginRuntime, want uint64) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for r.written.Load() < want {
		select {
		case <-ticker.C:
		case <-deadline:
			t.Fatalf("written=%d, want at least %d", r.written.Load(), want)
		}
	}
}

func TestRestoreBarrierSeparatesQueuedAndNewEvents(t *testing.T) {
	r := testRestoringRuntime(t)
	now := time.Now().UTC()
	if !r.enqueue(usageRecord{Provider: "codex", Model: "before", RequestedAt: now}) {
		t.Fatal("could not queue event before restore barrier")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	resume, err := r.pauseWriter(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer resume()
	if got := r.written.Load(); got != 1 {
		t.Fatalf("barrier did not flush preceding event: written=%d", got)
	}
	if r.status().QueueCapacity != 8 {
		t.Fatalf("control slot leaked into public queue capacity: %d", r.status().QueueCapacity)
	}
	if !r.enqueue(usageRecord{Provider: "codex", Model: "after", RequestedAt: now}) {
		t.Fatal("new event was not buffered while writer paused")
	}
	time.Sleep(60 * time.Millisecond)
	if got := r.written.Load(); got != 1 {
		t.Fatalf("event after barrier was written before resume: written=%d", got)
	}
	resume()
	waitForWritten(t, r, 2)
}

func TestRestoreReplacesDrainedHistoryAndResumesAfterFailure(t *testing.T) {
	r := testRestoringRuntime(t)
	now := time.Now().UTC()
	if !r.enqueue(usageRecord{Provider: "codex", Model: "old", RequestedAt: now}) {
		t.Fatal("could not queue event before restore")
	}
	payload := backupPayload{Version: 1, Events: []usageEvent{fixtureEvent(now, "restored", false, 10, 2)}, Prices: []modelPrice{}}
	result, err := r.restoreBackup(t.Context(), payload)
	if err != nil || result.Events != 1 {
		t.Fatalf("restore failed: result=%+v err=%v", result, err)
	}
	var count int
	var model string
	if err := r.store.db.QueryRow("SELECT COUNT(*), MAX(model) FROM usage_events").Scan(&count, &model); err != nil {
		t.Fatal(err)
	}
	if count != 1 || model != "restored" {
		t.Fatalf("pre-restore queued event survived replacement: count=%d model=%q", count, model)
	}
	if _, err := r.restoreBackup(t.Context(), backupPayload{Version: 1, Truncated: true}); err == nil {
		t.Fatal("invalid backup was accepted")
	}
	if !r.enqueue(usageRecord{Provider: "codex", Model: "new", RequestedAt: now}) {
		t.Fatal("writer remained paused after failed restore")
	}
	waitForWritten(t, r, 2)
	if err := r.store.db.QueryRow("SELECT COUNT(*) FROM usage_events").Scan(&count); err != nil || count != 2 {
		t.Fatalf("post-restore event missing: count=%d err=%v", count, err)
	}
}

func TestBackupFlushesPreviouslyQueuedEvents(t *testing.T) {
	r := testRestoringRuntime(t)
	if !r.enqueue(usageRecord{Provider: "codex", Model: "queued", RequestedAt: time.Now().UTC()}) {
		t.Fatal("could not queue event before backup")
	}
	backup, err := r.exportBackup(t.Context(), 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(backup.Events) != 1 || backup.Events[0].Model != "queued" {
		t.Fatalf("backup omitted event queued before request: %+v", backup.Events)
	}
}

func TestWriterRetriesFailedBatchWithoutDuplicateRollups(t *testing.T) {
	r := testWriter(t)
	if _, err := r.store.db.Exec(`CREATE TRIGGER reject_fixture BEFORE INSERT ON usage_events WHEN NEW.input_tokens=20
		BEGIN SELECT RAISE(ABORT, 'temporary fixture failure'); END`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		r.queue <- writerQueueItem{event: fixtureEvent(time.Now().UTC(), "gpt", false, int64(10*(i+1)), 2)}
		r.accepted.Add(1)
	}
	close(r.queue)
	go r.runWriter()
	t.Cleanup(func() { <-r.done })
	deadline := time.After(3 * time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if r.writeFailures.Load() > 0 {
			break
		}
		select {
		case <-ticker.C:
		case <-deadline:
			t.Fatal("writer did not attempt the batch")
		}
	}
	if _, err := r.store.db.Exec("DROP TRIGGER reject_fixture"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.done:
	case <-time.After(3 * time.Second):
		t.Fatal("writer did not finish retries")
	}
	if r.written.Load() != 2 || r.dropped.Load() != 0 {
		t.Fatalf("written=%d dropped=%d", r.written.Load(), r.dropped.Load())
	}
	var count, requests int
	if err := r.store.db.QueryRow("SELECT COUNT(*) FROM usage_events").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := r.store.db.QueryRow("SELECT COALESCE(SUM(requests),0) FROM usage_minute_rollups").Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if count != 2 || requests != 2 {
		t.Fatalf("retry duplicated/lost data: events=%d requests=%d", count, requests)
	}
}

func TestWriterAccountsForExhaustedRetries(t *testing.T) {
	r := testWriter(t)
	if _, err := r.store.db.Exec(`CREATE TRIGGER reject_fixture BEFORE INSERT ON usage_events
		BEGIN SELECT RAISE(ABORT, 'persistent fixture failure'); END`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		r.queue <- writerQueueItem{event: fixtureEvent(time.Now().UTC(), "gpt", false, 10, 2)}
		r.accepted.Add(1)
	}
	close(r.queue)
	r.runWriter()
	if r.writeFailures.Load() != 3 || r.dropped.Load() != 2 || r.written.Load() != 0 {
		t.Fatalf("failures=%d dropped=%d written=%d", r.writeFailures.Load(), r.dropped.Load(), r.written.Load())
	}
	raw, err := json.Marshal(r.status())
	if err != nil {
		t.Fatal(err)
	}
	var status map[string]any
	if err = json.Unmarshal(raw, &status); err != nil {
		t.Fatal(err)
	}
	if status["write_dropped"] != float64(2) {
		t.Fatalf("missing write-loss counter: %s", raw)
	}
}

func TestWriterDoesNotReplayUncertainCommit(t *testing.T) {
	r := testWriter(t)
	r.store.db.SetMaxOpenConns(1)
	r.store.db.SetMaxIdleConns(1)
	// Inserts succeed, but the deferred constraint makes COMMIT fail. The
	// connection must then be discarded with its unfinished transaction.
	for _, statement := range []string{
		"PRAGMA foreign_keys=ON",
		"CREATE TABLE fixture_parent(id INTEGER PRIMARY KEY)",
		"CREATE TABLE fixture_child(parent_id INTEGER REFERENCES fixture_parent(id) DEFERRABLE INITIALLY DEFERRED)",
		`CREATE TRIGGER reject_commit AFTER INSERT ON usage_events
		 BEGIN INSERT INTO fixture_child(parent_id) VALUES(1); END`,
	} {
		if _, err := r.store.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	r.writeBatchWithRetry([]usageEvent{fixtureEvent(time.Now().UTC(), "gpt", false, 10, 2)})
	if r.writeFailures.Load() != 1 || r.dropped.Load() != 0 || r.written.Load() != 0 {
		t.Fatalf("uncertain commit must not be retried or reported as lost: %+v", r.status())
	}
	raw, err := json.Marshal(r.status())
	if err != nil {
		t.Fatal(err)
	}
	var status map[string]any
	if err := json.Unmarshal(raw, &status); err != nil {
		t.Fatal(err)
	}
	if status["write_uncertain"] != float64(1) || status["write_dropped"] != float64(0) {
		t.Fatalf("incorrect outcome counters: %s", raw)
	}
	var count, rollups int
	if err := r.store.db.QueryRow("SELECT (SELECT COUNT(*) FROM usage_events), (SELECT COUNT(*) FROM usage_minute_rollups)").Scan(&count, &rollups); err != nil {
		t.Fatal(err)
	}
	if count != 0 || rollups != 0 {
		t.Fatalf("discarded transaction survived: events=%d rollups=%d", count, rollups)
	}
	if _, err := r.store.db.Exec("DROP TRIGGER reject_commit"); err != nil {
		t.Fatal(err)
	}
	r.writeBatchWithRetry([]usageEvent{fixtureEvent(time.Now().UTC(), "gpt", false, 10, 2)})
	if r.written.Load() != 1 || r.writeFailures.Load() != 1 {
		t.Fatalf("writer did not recover on a clean connection: %+v", r.status())
	}
}

func TestWriterFlushesPartialBatchOnClose(t *testing.T) {
	r := testWriter(t)
	r.config.FlushIntervalMS = 60000
	r.queue <- writerQueueItem{event: fixtureEvent(time.Now().UTC(), "gpt", false, 10, 2)}
	r.accepted.Add(1)
	close(r.queue)
	r.runWriter()
	if r.written.Load() != 1 || r.dropped.Load() != 0 || r.lastBatchSize.Load() != 1 {
		t.Fatalf("partial batch was not flushed: %+v", r.status())
	}
}

func TestMemoryStoreSurvivesDiscardedWriteConnection(t *testing.T) {
	cfg := defaultConfig()
	cfg.StorageEnabled = false
	store, err := openEventStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.close() })
	store.db.SetMaxOpenConns(2)
	store.db.SetMaxIdleConns(1)
	if err := store.writeBatch([]usageEvent{fixtureEvent(time.Now().UTC(), "history", false, 10, 2)}); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"PRAGMA foreign_keys=ON",
		"CREATE TABLE fixture_parent(id INTEGER PRIMARY KEY)",
		"CREATE TABLE fixture_child(parent_id INTEGER REFERENCES fixture_parent(id) DEFERRABLE INITIALLY DEFERRED)",
		`CREATE TRIGGER reject_commit AFTER INSERT ON usage_events
		 BEGIN INSERT INTO fixture_child(parent_id) VALUES(1); END`,
	} {
		if _, err := store.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	r := &pluginRuntime{config: cfg, store: store}
	r.writeBatchWithRetry([]usageEvent{fixtureEvent(time.Now().UTC(), "failed", false, 10, 2)})
	if r.writeUncertain.Load() != 1 || r.writeFailures.Load() != 1 || r.dropped.Load() != 0 {
		t.Fatalf("unexpected uncertain outcome: %+v", r.status())
	}
	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM usage_events").Scan(&count); err != nil || count != 1 {
		t.Fatalf("discarding a write connection destroyed memory history: count=%d err=%v", count, err)
	}
	if _, err := store.db.Exec("DROP TRIGGER reject_commit"); err != nil {
		t.Fatal(err)
	}
	r.writeBatchWithRetry([]usageEvent{fixtureEvent(time.Now().UTC(), "recovered", false, 10, 2)})
	if r.written.Load() != 1 {
		t.Fatalf("memory writer did not recover: %+v", r.status())
	}
	if err := store.db.QueryRow("SELECT COUNT(*) FROM usage_events").Scan(&count); err != nil || count != 2 {
		t.Fatalf("history not preserved after recovery: count=%d err=%v", count, err)
	}
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openEventStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.close()
	if got := reopened.status().EventCount; got != 0 {
		t.Fatalf("store close leaked the memory keeper connection: events=%d", got)
	}
}

func TestPaginationRejectsOverflowAndExcessiveOffsets(t *testing.T) {
	_, now := withTestRuntime(t)
	for _, query := range []url.Values{
		{"page": {strconv.Itoa(int(^uint(0) >> 1))}, "page_size": {"200"}},
		{"page": {"40002"}, "page_size": {"25"}},
		{"page": {"5002"}, "page_size": {"200"}},
	} {
		query.Set("range", "all")
		response := handleManagement(managementRequest{Method: http.MethodGet, Path: managementPrefix + "/events", Query: query})
		if response.StatusCode != http.StatusBadRequest {
			t.Errorf("query=%v status=%d", query, response.StatusCode)
		}
	}
	store := currentRuntime().store
	_, err := queryEvents(t.Context(), store, eventFilter{FromMS: now.Add(-time.Hour).UnixMilli(), ToMS: now.UnixMilli(), Page: int(^uint(0) >> 1), PageSize: 200})
	if err == nil {
		t.Fatal("query layer accepted overflowing offset")
	}
	page, err := queryEvents(t.Context(), store, eventFilter{Page: 2, PageSize: 25})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 || page.Pages != 1 || len(page.Events) != 0 {
		t.Fatalf("out-of-range page=%+v", page)
	}
	page, err = queryEvents(t.Context(), store, eventFilter{Page: 2, PageSize: 2})
	if err != nil || page.Total != 3 || len(page.Events) != 1 {
		t.Fatalf("ordinary page=%+v err=%v", page, err)
	}
	for _, boundary := range []struct{ page, pageSize int }{{40001, 25}, {5001, 200}, {40001, 1}} {
		filter, err := parseEventFilter(url.Values{
			"page": {strconv.Itoa(boundary.page)}, "page_size": {strconv.Itoa(boundary.pageSize)},
		}, now)
		if err != nil {
			t.Fatalf("valid pagination boundary %+v: %v", boundary, err)
		}
		result, err := queryEvents(t.Context(), store, filter)
		if err != nil || len(result.Events) != 0 {
			t.Fatalf("valid out-of-range page %+v: %+v %v", boundary, result, err)
		}
	}
	filter, err := parseEventFilter(url.Values{"page_size": {"99999"}}, now)
	if err != nil || filter.PageSize != 200 {
		t.Fatalf("page size must still be clamped: %+v %v", filter, err)
	}
	empty, err := queryEvents(t.Context(), store, eventFilter{Model: "absent-model"})
	if err != nil || empty.Events == nil || len(empty.Events) != 0 || empty.Pages != 0 || empty.AccessiblePages != 0 {
		t.Fatalf("empty result=%+v err=%v", empty, err)
	}
}

func TestSummaryContainsMeasuredStorageStatus(t *testing.T) {
	_, _ = withTestRuntime(t)
	response := handleManagement(managementRequest{Method: http.MethodGet, Path: managementPrefix + "/summary", Query: url.Values{"range": {"all"}}})
	var result summaryResponse
	if err := json.Unmarshal(response.Body, &result); err != nil {
		t.Fatal(err)
	}
	if result.Runtime.Storage.EventCount != 3 || result.Runtime.Storage.RollupCount != 3 || result.Runtime.Storage.DatabaseBytes <= 0 {
		t.Fatalf("summary storage metrics=%+v", result.Runtime.Storage)
	}
	var wire map[string]any
	_ = json.Unmarshal(response.Body, &wire)
	storage := wire["runtime"].(map[string]any)["storage"].(map[string]any)
	if storage["metrics_available"] != true || storage["sampled_at"] == nil {
		t.Fatalf("missing sampling metadata: %+v", storage)
	}
}

func TestStorageStatusUnknownAndMemoryJournal(t *testing.T) {
	store := openTestStore(t)
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(store.statusSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	var status map[string]any
	_ = json.Unmarshal(raw, &status)
	if status["event_count"] != nil || status["database_bytes"] != nil || status["metrics_available"] != false || status["metrics_error"] == nil {
		t.Fatalf("unknown metrics must not look like zeros: %s", raw)
	}
	cfg := defaultConfig()
	cfg.StorageEnabled = false
	memory, err := openEventStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer memory.close()
	if got := memory.statusSnapshot().JournalMode; got != "memory" {
		t.Fatalf("memory journal=%q", got)
	}
}

func TestStorageStatusCachesAndRecoversFromFailedSampling(t *testing.T) {
	store := openTestStore(t)
	event := fixtureEvent(time.Now().UTC(), "gpt", false, 10, 2)
	if err := store.writeBatch([]usageEvent{event}); err != nil {
		t.Fatal(err)
	}
	first := store.statusSnapshot()
	if !first.MetricsAvailable || first.MetricsStale || first.EventCount != 1 {
		t.Fatalf("first snapshot=%+v", first)
	}
	var bytes int64
	for _, path := range []string{store.path, store.path + "-wal"} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		bytes += info.Size()
	}
	if first.DatabaseBytes != bytes {
		t.Fatalf("database size=%d, want db+WAL=%d", first.DatabaseBytes, bytes)
	}
	if err := store.writeBatch([]usageEvent{event}); err != nil {
		t.Fatal(err)
	}
	cached := store.statusSnapshot()
	if cached.EventCount != 1 || cached.SampledAt != first.SampledAt {
		t.Fatalf("sampling ignored TTL: %+v", cached)
	}
	store.metricsMu.Lock()
	store.metricsAttempt = time.Now().Add(-6 * time.Second)
	store.metricsMu.Unlock()
	fresh := store.statusSnapshot()
	if fresh.EventCount != 2 || fresh.MetricsStale {
		t.Fatalf("expired snapshot did not refresh: %+v", fresh)
	}
	if _, err := store.db.Exec("ALTER TABLE usage_events RENAME TO fixture_missing_events"); err != nil {
		t.Fatal(err)
	}
	stale := store.status()
	if !stale.MetricsAvailable || !stale.MetricsStale || stale.MetricsError == "" || stale.EventCount != 2 || stale.SampledAt != fresh.SampledAt {
		t.Fatalf("failed sample must retain and label the last snapshot: %+v", stale)
	}
	if _, err := store.db.Exec("ALTER TABLE fixture_missing_events RENAME TO usage_events"); err != nil {
		t.Fatal(err)
	}
	recovered := store.status()
	if !recovered.MetricsAvailable || recovered.MetricsStale || recovered.MetricsError != "" || recovered.EventCount != 2 {
		t.Fatalf("sampling did not recover: %+v", recovered)
	}
}

func TestStorageStatusRefreshesAfterPruneAndRestore(t *testing.T) {
	store := openTestStore(t)
	now := time.Now().UTC()
	if err := store.writeBatch([]usageEvent{fixtureEvent(now.Add(-48*time.Hour), "gpt", false, 10, 2)}); err != nil {
		t.Fatal(err)
	}
	if got := store.statusSnapshot().EventCount; got != 1 {
		t.Fatalf("before prune count=%d", got)
	}
	if err := store.prune(1, now); err != nil {
		t.Fatal(err)
	}
	if got := store.statusSnapshot().EventCount; got != 0 {
		t.Fatalf("pruned count stayed cached: %d", got)
	}
	_, err := importBackup(t.Context(), store, backupPayload{
		Version: 1, Events: []usageEvent{fixtureEvent(now, "restored", false, 10, 2)}, Prices: []modelPrice{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := store.statusSnapshot().EventCount; got != 1 {
		t.Fatalf("restored count stayed cached: %d", got)
	}
}

func TestCSVTextCellPreservesOrdinaryText(t *testing.T) {
	for _, value := range []string{"", "普通文本", "123", "line,with,commas", "line\nnext", `quoted "name"`, "'=1+1"} {
		if got := csvTextCell(value); got != value {
			t.Errorf("ordinary text changed: %q -> %q", value, got)
		}
	}
	for _, value := range []string{"=1+1", " +1", "\t-2", "\ufeff@SUM(1,2)", "\u200b=1+1", "\x00=1+1", "\r\n=1+1"} {
		if got := csvTextCell(value); got != "'"+value {
			t.Errorf("formula prefix unprotected: %q -> %q", value, got)
		}
	}
}

func TestCSVProtectsFormulaPrefixes(t *testing.T) {
	store := openTestStore(t)
	payloads := []string{"=1+1", "+SUM(1,2)", "-1+2", "@SUM(1,2)", "  =1+1", "\t=1+1", "\r=1+1", "\ufeff=1+1", "normal model", "123", "line,with,commas"}
	now := time.Now().UTC()
	for i, payload := range payloads {
		event := fixtureEvent(now.Add(time.Duration(i)*time.Millisecond), "temporary", true, 10, 2)
		if err := store.writeBatch([]usageEvent{event}); err != nil {
			t.Fatal(err)
		}
		// Legacy raw values also need protection at the export boundary.
		if _, err := store.db.Exec("UPDATE usage_events SET model=?, provider=?, failure=? WHERE id=(SELECT MAX(id) FROM usage_events)", payload, payload, payload); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := exportEventsCSV(t.Context(), store, eventFilter{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(raw), "\xEF\xBB\xBF"))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	columns := map[string]int{}
	for i, k := range rows[0] {
		columns[k] = i
	}
	for _, row := range rows[1:] {
		for _, column := range []string{"model", "provider", "failure"} {
			cell := row[columns[column]]
			trimmed := strings.TrimLeft(cell, " \t\r\n\ufeff")
			if len(trimmed) > 0 && strings.ContainsRune("=+-@", rune(trimmed[0])) {
				t.Errorf("formula exported in %s: %q", column, cell)
			}
		}
		if row[columns["input_tokens"]] != "10" {
			t.Errorf("numeric column changed: %+v", row)
		}
	}
}

func TestDashboardWireFixture(t *testing.T) {
	path := os.Getenv("CPA_DASHBOARD_FIXTURE")
	if path == "" {
		t.Skip("fixture output is opt-in")
	}
	store, now := seededQueryStore(t)
	if _, err := store.db.Exec("UPDATE usage_events SET ttft_ms=1500, latency_ms=3000, output_tokens=100"); err != nil {
		t.Fatal(err)
	}
	var key string
	if err := store.db.QueryRow("SELECT upstream_key FROM usage_events LIMIT 1").Scan(&key); err != nil {
		t.Fatal(err)
	}
	result, err := queryUpstreamDetail(t.Context(), store, key, url.Values{"range": {"all"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}
