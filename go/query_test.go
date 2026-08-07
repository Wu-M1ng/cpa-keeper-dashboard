package main

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"
)

func seededQueryStore(t *testing.T) (*eventStore, time.Time) {
	t.Helper()
	store := openTestStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	events := []usageEvent{
		fixtureEvent(now.Add(-2*time.Hour), "gpt-5.6", false, 1000, 100),
		fixtureEvent(now.Add(-time.Hour), "gpt-5.6", true, 500, 50),
		fixtureEvent(now.Add(-30*time.Minute), "claude-sonnet", false, 200, 80),
	}
	events[1].StatusCode = 429
	events[1].Failure = "rate limited"
	events[2].Provider = "anthropic"
	events[2].Source = "claude"
	events[2].APIKeyHash = "other-api"
	events[2].APIKeyMask = "o***ey"
	events[2].UpstreamKey = "other-upstream"
	events[2].UpstreamLabel = "anthropic / 1***"
	if err := store.writeBatch(events); err != nil {
		t.Fatal(err)
	}
	if err := replacePrices(context.Background(), store, []modelPrice{
		{Model: "gpt-5.6", InputPerMillion: 2, OutputPerMillion: 8},
		{Model: "claude-sonnet", InputPerMillion: 3, OutputPerMillion: 15},
	}); err != nil {
		t.Fatal(err)
	}
	return store, now
}

func TestQuerySummaryUsesRollups(t *testing.T) {
	store, now := seededQueryStore(t)
	result, err := querySummary(context.Background(), store, url.Values{"range": {"24h"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if result.KPI.Requests != 3 || result.KPI.Successes != 2 || result.KPI.Failures != 1 {
		t.Fatalf("unexpected KPI: %+v", result.KPI)
	}
	if result.KPI.TotalTokens != 1930 || result.KPI.CostUSD <= 0 {
		t.Fatalf("tokens/cost missing: %+v", result.KPI)
	}
	if result.KPI.InputTokens != 1700 || result.KPI.OutputTokens != 230 ||
		result.KPI.CacheReadTokens != 850 || result.KPI.CacheWriteTokens != 0 ||
		result.KPI.ReasoningTokens != 0 {
		t.Fatalf("token dimensions missing from KPI: %+v", result.KPI)
	}
	if result.KPI.CacheRate != 0.5 || result.KPI.AvgRequestsDaily != 3 ||
		result.KPI.AvgTokensDaily != 1930 || result.KPI.AvgCostDaily <= 0 {
		t.Fatalf("daily/cache KPI missing: %+v", result.KPI)
	}
	if result.KPI.RPM <= 0 || result.KPI.TPM <= 0 || result.KPI.RangeLabel != "24h" {
		t.Fatalf("rate/range KPI missing: %+v", result.KPI)
	}
	if len(result.Trend) == 0 || len(result.Health) != 5*24*4 {
		t.Fatalf("trend or health missing: %+v", result)
	}
	var trendTokens tokenTotals
	var actualCost, standardCost float64
	for _, point := range result.Trend {
		trendTokens.Input += point.Input
		trendTokens.Output += point.Output
		trendTokens.CacheRead += point.CacheRead
		trendTokens.CacheWrite += point.CacheWrite
		trendTokens.Reasoning += point.Reasoning
		actualCost += point.ActualCost
		standardCost += point.StandardCost
	}
	if trendTokens.Input != 1700 || trendTokens.Output != 230 || trendTokens.CacheRead != 850 {
		t.Fatalf("trend token dimensions = %+v", trendTokens)
	}
	if actualCost <= 0 || standardCost < actualCost {
		t.Fatalf("trend costs actual=%f standard=%f", actualCost, standardCost)
	}
	wantStart := now.UTC().Truncate(24 * time.Hour).Add(-4 * 24 * time.Hour).UnixMilli()
	wantEnd := wantStart + int64((5*24*4-1)*15*time.Minute/time.Millisecond)
	if result.Health[0].TimestampMS != wantStart || result.Health[len(result.Health)-1].TimestampMS != wantEnd {
		t.Fatalf("health range = %d..%d, want %d..%d", result.Health[0].TimestampMS, result.Health[len(result.Health)-1].TimestampMS, wantStart, wantEnd)
	}
	var healthRequests int64
	for _, point := range result.Health {
		healthRequests += point.Requests
	}
	if healthRequests != 3 {
		t.Fatalf("health requests = %d, want 3", healthRequests)
	}
}

func TestQuerySummaryReturnsEmptyFiveDayHealthGrid(t *testing.T) {
	store := openTestStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	result, err := querySummary(context.Background(), store, url.Values{"range": {"24h"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Health) != 5*24*4 {
		t.Fatalf("empty health grid has %d points, want 480", len(result.Health))
	}
	for _, point := range result.Health {
		if point.Requests != 0 || point.Failures != 0 || point.SuccessRate != 0 {
			t.Fatalf("empty health point contains activity: %+v", point)
		}
	}
}

func TestQueryAnalysisReturnsFourDistributionsAndModels(t *testing.T) {
	store, now := seededQueryStore(t)
	result, err := queryAnalysis(context.Background(), store, url.Values{"range": {"24h"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Distributions) != 4 {
		t.Fatalf("distributions = %d, want 4", len(result.Distributions))
	}
	for _, key := range []string{"models", "providers", "api_keys", "sources"} {
		if len(result.Distributions[key]) == 0 {
			t.Fatalf("distribution %q is empty", key)
		}
	}
	if len(result.Models) != 2 || result.Tokens.Total != 1930 {
		t.Fatalf("model/token analysis incomplete: %+v", result)
	}
}

func TestQueryAnalysisMergesSourcesWithTheSameMaskedChannel(t *testing.T) {
	store := openTestStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	events := []usageEvent{
		fixtureEvent(now.Add(-time.Hour), "gpt-5.6", false, 100, 20),
		fixtureEvent(now.Add(-30*time.Minute), "gpt-5.6", false, 80, 10),
	}
	if err := store.writeBatch(events); err != nil {
		t.Fatal(err)
	}

	firstMinute := events[0].TimestampMS / 60000
	secondMinute := events[1].TimestampMS / 60000
	if _, err := store.db.Exec(`UPDATE usage_minute_rollups SET source = CASE minute
		WHEN ? THEN 'sk-channel-alpha-e0'
		WHEN ? THEN 'sk-channel-bravo-e0'
		ELSE source END`, firstMinute, secondMinute); err != nil {
		t.Fatal(err)
	}

	result, err := queryAnalysis(context.Background(), store, url.Values{"range": {"24h"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	sources := result.Distributions["sources"]
	if len(sources) != 1 {
		t.Fatalf("source distribution = %+v, want one merged channel", sources)
	}
	if sources[0].Name != "codex / sk-***e0" || sources[0].Requests != 2 {
		t.Fatalf("merged source = %+v, want codex / sk-***e0 with 2 requests", sources[0])
	}
}

func TestQueryInterfacesAndUpstreamDetail(t *testing.T) {
	store, now := seededQueryStore(t)
	interfaces, err := queryInterfaces(context.Background(), store, url.Values{"range": {"24h"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(interfaces.APIKeys) != 2 || len(interfaces.Upstreams) != 2 {
		t.Fatalf("unexpected interfaces: %+v", interfaces)
	}
	detail, err := queryUpstreamDetail(context.Background(), store, interfaces.Upstreams[0].Key, url.Values{"range": {"24h"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Summary.Requests != 2 || len(detail.Models) != 1 || len(detail.RecentEvents) != 2 {
		t.Fatalf("unexpected upstream detail: %+v", detail)
	}
}

func TestProviderCredentialLabelsCombineProviderWithSource(t *testing.T) {
	store := openTestStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	codex := fixtureEvent(now.Add(-time.Hour), "gpt-5.6", false, 100, 20)
	codex.Provider = "codex"
	codex.Source = "sk-channel-secret-e0"
	codex.UpstreamKey = "1111111111111111"
	codex.UpstreamLabel = "codex / upstream-deadbeef0f"
	antigravity := fixtureEvent(now.Add(-30*time.Minute), "gemini", false, 80, 10)
	antigravity.Provider = "antigravity"
	antigravity.Source = "baduser@example.com"
	antigravity.UpstreamKey = "2222222222222222"
	antigravity.UpstreamLabel = "antigravity / aaaaaaaaaaaaaa78"
	if err := store.writeBatch([]usageEvent{codex, antigravity}); err != nil {
		t.Fatal(err)
	}

	// Reproduce rows written by an older version: useful source, hashed upstream label.
	_, err := store.db.Exec(`UPDATE usage_events SET source = CASE provider
		WHEN 'codex' THEN 'sk-channel-secret-e0' ELSE 'baduser@example.com' END,
		upstream_label = CASE provider WHEN 'codex' THEN 'codex / upstream-deadbeef0f'
		ELSE 'antigravity / aaaaaaaaaaaaaa78' END`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.db.Exec(`UPDATE usage_minute_rollups SET source = CASE provider
		WHEN 'codex' THEN 'sk-channel-secret-e0' ELSE 'baduser@example.com' END,
		upstream_label = CASE provider WHEN 'codex' THEN 'codex / upstream-deadbeef0f'
		ELSE 'antigravity / aaaaaaaaaaaaaa78' END`)
	if err != nil {
		t.Fatal(err)
	}

	analysis, err := queryAnalysis(context.Background(), store, url.Values{"range": {"24h"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	interfaces, err := queryInterfaces(context.Background(), store, url.Values{"range": {"24h"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	page, err := queryEvents(context.Background(), store, eventFilter{
		FromMS: now.Add(-24 * time.Hour).UnixMilli(), ToMS: now.UnixMilli(), Page: 1, PageSize: 25,
	})
	if err != nil {
		t.Fatal(err)
	}

	for name, items := range map[string][]dimensionStat{
		"source distribution": analysis.Distributions["sources"],
		"upstream stats":      interfaces.Upstreams,
	} {
		labels := make(map[string]bool, len(items))
		for _, item := range items {
			labels[item.Name] = true
		}
		if !labels["codex / sk-***e0"] || !labels["antigravity / bad***om"] {
			t.Fatalf("%s labels = %+v", name, labels)
		}
	}
	for _, event := range page.Events {
		want := "codex / sk-***e0"
		if event.Provider == "antigravity" {
			want = "antigravity / bad***om"
		}
		if event.Source != want || event.UpstreamLabel != want {
			t.Fatalf("event labels = source %q upstream %q, want %q", event.Source, event.UpstreamLabel, want)
		}
	}
}

func TestQueryEventsFiltersAndPaginates(t *testing.T) {
	store, now := seededQueryStore(t)
	page, err := queryEvents(context.Background(), store, eventFilter{
		FromMS:   now.Add(-24 * time.Hour).UnixMilli(),
		ToMS:     now.UnixMilli(),
		Provider: "codex",
		Status:   "failure",
		Page:     1,
		PageSize: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Events) != 1 || page.Events[0].StatusCode != 429 {
		t.Fatalf("unexpected events page: %+v", page)
	}
}

func TestManagementResponsesDoNotExposeStoredIdentifiers(t *testing.T) {
	store, now := seededQueryStore(t)
	_, err := store.db.Exec(`UPDATE usage_events SET
		auth_id = 'channel-account@example.com',
		auth_index = 'sk-channel-secret-123456',
		upstream_label = 'codex / sk-channel-secret-123456',
		source = 'codex / apikey / sk-channel-secret-123456'
		WHERE provider = 'codex'`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.db.Exec(`UPDATE usage_minute_rollups SET
		upstream_label = 'codex / sk-channel-secret-123456',
		source = 'codex / apikey / sk-channel-secret-123456'
		WHERE provider = 'codex'`)
	if err != nil {
		t.Fatal(err)
	}

	analysis, err := queryAnalysis(context.Background(), store, url.Values{"range": {"24h"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	interfaces, err := queryInterfaces(context.Background(), store, url.Values{"range": {"24h"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	page, err := queryEvents(context.Background(), store, eventFilter{
		FromMS: now.Add(-24 * time.Hour).UnixMilli(), ToMS: now.UnixMilli(), Page: 1, PageSize: 25,
	})
	if err != nil {
		t.Fatal(err)
	}

	raw, err := json.Marshal([]any{analysis, interfaces, page})
	if err != nil {
		t.Fatal(err)
	}
	for _, sensitive := range []string{"c***ey", "o***ey", "api-hash", "other-api", "channel-account@example.com", "sk-channel-secret-123456", `"source":"openai"`, `"source":"claude"`, `"auth_index":"0"`} {
		if strings.Contains(string(raw), sensitive) {
			t.Fatalf("management response exposed %q: %s", sensitive, raw)
		}
	}
	if !strings.Contains(string(raw), "sk-***56") {
		t.Fatalf("management response is missing the masked provider credential: %s", raw)
	}
	if len(page.Events) == 0 || page.Events[0].APIKeyMask == "" || page.Events[0].Source == "" {
		t.Fatalf("anonymous event labels are missing: %+v", page.Events)
	}
}

func TestEventCSVDoesNotExposeStoredIdentifiers(t *testing.T) {
	store, now := seededQueryStore(t)
	csv, err := exportEventsCSV(context.Background(), store, eventFilter{
		FromMS: now.Add(-24 * time.Hour).UnixMilli(), ToMS: now.UnixMilli(),
	}, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, sensitive := range []string{"c***ey", "o***ey", "api-hash", "other-api", ",openai,", ",claude,"} {
		if strings.Contains(string(csv), sensitive) {
			t.Fatalf("CSV exposed %q: %s", sensitive, csv)
		}
	}
}

func TestBackupRoundTrip(t *testing.T) {
	source, _ := seededQueryStore(t)
	payload, err := exportBackup(context.Background(), source, 100)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Version != 1 || len(payload.Events) != 3 || len(payload.Prices) != 2 {
		t.Fatalf("unexpected backup: %+v", payload)
	}
	target := openTestStore(t)
	result, err := importBackup(context.Background(), target, payload)
	if err != nil {
		t.Fatal(err)
	}
	if result.Events != 3 || target.status().EventCount != 3 {
		t.Fatalf("restore failed: result=%+v status=%+v", result, target.status())
	}
}

func TestImportBackupRejectsNegativeMeasurements(t *testing.T) {
	store := openTestStore(t)
	event := fixtureEvent(time.Now().UTC(), "gpt-5.6", false, 10, 5)
	event.InputTokens = -1
	_, err := importBackup(context.Background(), store, backupPayload{Version: 1, Events: []usageEvent{event}})
	if err == nil || !strings.Contains(err.Error(), "negative measurement") {
		t.Fatalf("import error = %v, want negative measurement rejection", err)
	}
}
