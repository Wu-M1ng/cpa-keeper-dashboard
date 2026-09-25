package main

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func withTestRuntime(t *testing.T) (*pluginRuntime, time.Time) {
	t.Helper()
	store, now := seededQueryStore(t)
	cfg := defaultConfig()
	runtime := &pluginRuntime{config: cfg, store: store, queue: make(chan writerQueueItem, 8), readCache: newManagementReadCache(), started: now}
	runtimeMu.Lock()
	previous := activeRuntime
	activeRuntime = runtime
	runtimeMu.Unlock()
	previousNow := managementNow
	managementNow = func() time.Time { return now }
	t.Cleanup(func() {
		managementNow = previousNow
		runtimeMu.Lock()
		activeRuntime = previous
		runtimeMu.Unlock()
	})
	return runtime, now
}

func TestManagementSummaryAndEvents(t *testing.T) {
	_, _ = withTestRuntime(t)
	summary := handleManagement(managementRequest{
		Method: http.MethodGet,
		Path:   managementPrefix + "/summary",
		Query:  url.Values{"range": {"all"}},
	})
	if summary.StatusCode != http.StatusOK || summary.Headers.Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected summary response: status=%d headers=%+v body=%s", summary.StatusCode, summary.Headers, summary.Body)
	}
	var decoded summaryResponse
	if err := json.Unmarshal(summary.Body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.KPI.Requests != 3 {
		t.Fatalf("summary requests = %d, want 3 (body=%s)", decoded.KPI.Requests, summary.Body)
	}

	events := handleManagement(managementRequest{
		Method: http.MethodGet,
		Path:   managementPrefix + "/events",
		Query:  url.Values{"range": {"all"}, "status": {"failure"}, "page_size": {"25"}},
	})
	if events.StatusCode != http.StatusOK {
		t.Fatalf("events status = %d body=%s", events.StatusCode, events.Body)
	}
	var page eventsPage
	_ = json.Unmarshal(events.Body, &page)
	if page.Total != 1 || len(page.Events) != 1 {
		t.Fatalf("unexpected events: %+v", page)
	}
}

func TestManagementPricesAndSettingsMutations(t *testing.T) {
	runtime, _ := withTestRuntime(t)
	pricesBody, _ := json.Marshal(map[string]any{"prices": []modelPrice{{Model: "new-model", InputPerMillion: 1.5}}})
	response := handleManagement(managementRequest{Method: http.MethodPut, Path: managementPrefix + "/prices", Body: pricesBody})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("prices status=%d body=%s", response.StatusCode, response.Body)
	}
	prices, err := listPrices(t.Context(), runtime.store)
	if err != nil || len(prices) != 1 || prices[0].Model != "new-model" {
		t.Fatalf("prices not replaced: %+v err=%v", prices, err)
	}

	settingsBody := []byte(`{"retention_days":14,"export_max_records":1000}`)
	response = handleManagement(managementRequest{Method: http.MethodPut, Path: managementPrefix + "/settings", Body: settingsBody})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("settings status=%d body=%s", response.StatusCode, response.Body)
	}
	runtime.configMu.RLock()
	defer runtime.configMu.RUnlock()
	if runtime.config.RetentionDays != 14 || runtime.config.ExportMax != 1000 {
		t.Fatalf("settings not applied: %+v", runtime.config)
	}
}

func TestManagementExportAndRestoreValidation(t *testing.T) {
	runtime, _ := withTestRuntime(t)
	export := handleManagement(managementRequest{Method: http.MethodGet, Path: managementPrefix + "/events/export", Query: url.Values{"range": {"all"}}})
	if export.StatusCode != http.StatusOK || export.Headers.Get("Content-Disposition") == "" || len(export.Body) == 0 {
		t.Fatalf("unexpected export: %+v", export)
	}
	invalid := handleManagement(managementRequest{Method: http.MethodPost, Path: managementPrefix + "/restore", Body: []byte("not-json")})
	if invalid.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid restore status=%d", invalid.StatusCode)
	}
	before := runtime.store.status().EventCount
	for _, body := range [][]byte{
		[]byte(`{"version":1}`),
		[]byte(`{"version":1,"events":[],"prices":[],"truncated":true}`),
		[]byte(`{"version":1,"events":[{"timestamp_ms":1,"provider":"codex","model":"gpt","input_tokens":-1}],"prices":[]}`),
	} {
		response := handleManagement(managementRequest{Method: http.MethodPost, Path: managementPrefix + "/restore", Body: body})
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("incomplete restore status=%d body=%s", response.StatusCode, response.Body)
		}
		if after := runtime.store.status().EventCount; after != before {
			t.Fatalf("rejected restore changed events: before=%d after=%d", before, after)
		}
	}
}

func TestManagementBackupRejectsExportAboveLimit(t *testing.T) {
	runtime, _ := withTestRuntime(t)
	runtime.configMu.Lock()
	runtime.config.ExportMax = 2
	runtime.configMu.Unlock()
	request := managementRequest{Method: http.MethodGet, Path: managementPrefix + "/backup"}
	response := handleManagement(request)
	if response.StatusCode != http.StatusConflict || response.Headers.Get("Content-Disposition") != "" ||
		!strings.Contains(string(response.Body), "backup_limit_exceeded") {
		t.Fatalf("oversized backup was offered as a download: status=%d headers=%v body=%s", response.StatusCode, response.Headers, response.Body)
	}
	runtime.configMu.Lock()
	runtime.config.ExportMax = 3
	runtime.configMu.Unlock()
	response = handleManagement(request)
	if response.StatusCode != http.StatusOK || response.Headers.Get("Content-Disposition") == "" {
		t.Fatalf("complete backup failed: status=%d body=%s", response.StatusCode, response.Body)
	}
	var backup backupPayload
	if err := json.Unmarshal(response.Body, &backup); err != nil {
		t.Fatal(err)
	}
	if backup.Truncated || len(backup.Events) != 3 {
		t.Fatalf("backup is incomplete: events=%d truncated=%t", len(backup.Events), backup.Truncated)
	}
}

func TestManagementCSVRejectsExportAboveLimit(t *testing.T) {
	runtime, _ := withTestRuntime(t)
	runtime.configMu.Lock()
	runtime.config.ExportMax = 2
	runtime.configMu.Unlock()
	response := handleManagement(managementRequest{
		Method: http.MethodGet,
		Path:   managementPrefix + "/events/export",
		Query:  url.Values{"range": {"all"}},
	})
	if response.StatusCode != http.StatusConflict || response.Headers.Get("Content-Disposition") != "" ||
		!strings.Contains(string(response.Body), "csv_limit_exceeded") {
		t.Fatalf("truncated CSV was offered as a download: status=%d headers=%v body=%s", response.StatusCode, response.Headers, response.Body)
	}

	runtime.configMu.Lock()
	runtime.config.ExportMax = 3
	runtime.configMu.Unlock()
	response = handleManagement(managementRequest{
		Method: http.MethodGet,
		Path:   managementPrefix + "/events/export",
		Query:  url.Values{"range": {"all"}},
	})
	if response.StatusCode != http.StatusOK || response.Headers.Get("Content-Disposition") == "" {
		t.Fatalf("complete CSV export failed: status=%d body=%s", response.StatusCode, response.Body)
	}
	rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(response.Body), "\xEF\xBB\xBF"))).ReadAll()
	if err != nil || len(rows) != 4 {
		t.Fatalf("complete CSV rows=%d err=%v", len(rows), err)
	}
}

func TestManagementRejectsInvalidFiltersWithoutInternalDetails(t *testing.T) {
	_, _ = withTestRuntime(t)
	response := handleManagement(managementRequest{Method: http.MethodGet, Path: managementPrefix + "/events", Query: url.Values{"range": {"bad"}}})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.StatusCode, response.Body)
	}
	if string(response.Body) == "" || response.Headers.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("error response headers/body missing: %+v", response)
	}
}

func TestManagementCacheKeyIncludesCustomRange(t *testing.T) {
	base := managementRequest{Method: http.MethodGet, Path: managementPrefix + "/summary"}
	base.Query = url.Values{"range": {"custom"}, "from": {"1000"}, "to": {"2000"}}
	other := base
	other.Query = url.Values{"range": {"custom"}, "from": {"2000"}, "to": {"3000"}}
	if managementCacheKey(base) == managementCacheKey(other) {
		t.Fatal("different custom ranges must not share a management cache key")
	}
}

func TestUpdateSettingsPublishesConfigOnlyAfterCommit(t *testing.T) {
	runtime, _ := withTestRuntime(t)
	runtime.configMu.RLock()
	before := runtime.config
	runtime.configMu.RUnlock()
	if err := runtime.store.db.Close(); err != nil {
		t.Fatal(err)
	}

	response := updateSettings(t.Context(), runtime, []byte(`{"retention_days":14,"export_max_records":1000}`))
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusInternalServerError)
	}
	runtime.configMu.RLock()
	after := runtime.config
	runtime.configMu.RUnlock()
	if after != before {
		t.Fatalf("failed commit published runtime config: before=%+v after=%+v", before, after)
	}
}

func TestUpdateSettingsRollsBackWhenRetentionFails(t *testing.T) {
	runtime, now := withTestRuntime(t)
	if err := runtime.store.writeBatch([]usageEvent{fixtureEvent(now.AddDate(0, 0, -40), "expired", false, 100, 10)}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.store.db.Exec(`CREATE TRIGGER reject_retention BEFORE DELETE ON usage_minute_rollups
		BEGIN SELECT RAISE(ABORT, 'forced retention failure'); END`); err != nil {
		t.Fatal(err)
	}
	before := runtime.config
	response := updateSettings(t.Context(), runtime, []byte(`{"retention_days":14,"export_max_records":1000}`))
	if response.StatusCode != http.StatusInternalServerError || !strings.Contains(string(response.Body), "retention_cleanup_failed") {
		t.Fatalf("failed retention reported success: status=%d body=%s", response.StatusCode, response.Body)
	}
	if runtime.config != before {
		t.Fatalf("failed retention changed runtime settings: %+v", runtime.config)
	}
	if saved := runtime.store.loadIntSetting("retention_days", before.RetentionDays); saved != before.RetentionDays {
		t.Fatalf("failed retention persisted new settings: %d", saved)
	}
	status := runtime.store.status()
	if status.EventCount != 4 || status.RollupCount != 4 || status.LastError == "" {
		t.Fatalf("failed retention did not preserve data and report error: %+v", status)
	}
}

func TestUpdateSettingsPrunesAndInvalidatesSummaryCache(t *testing.T) {
	runtime, now := withTestRuntime(t)
	if err := runtime.store.writeBatch([]usageEvent{fixtureEvent(now.AddDate(0, 0, -40), "expired", false, 100, 10)}); err != nil {
		t.Fatal(err)
	}
	request := managementRequest{Method: http.MethodGet, Path: managementPrefix + "/summary", Query: url.Values{"range": {"all"}}}
	before := handleManagement(request)
	var summary summaryResponse
	if err := json.Unmarshal(before.Body, &summary); err != nil || summary.KPI.Requests != 4 {
		t.Fatalf("failed to prime summary cache: %s, err=%v", before.Body, err)
	}
	response := updateSettings(t.Context(), runtime, []byte(`{"retention_days":30}`))
	if response.StatusCode != http.StatusOK {
		t.Fatalf("settings status=%d body=%s", response.StatusCode, response.Body)
	}
	status := runtime.store.status()
	if status.EventCount != 3 || status.RollupCount != 3 {
		t.Fatalf("settings did not prune at the management request time: %+v", status)
	}
	after := handleManagement(request)
	if err := json.Unmarshal(after.Body, &summary); err != nil || summary.KPI.Requests != 3 {
		t.Fatalf("summary cache still contains expired data: %s, err=%v", after.Body, err)
	}
}
