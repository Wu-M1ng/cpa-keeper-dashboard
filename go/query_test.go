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
	if len(result.Trend) == 0 || len(result.Health) == 0 {
		t.Fatalf("trend or health missing: %+v", result)
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
