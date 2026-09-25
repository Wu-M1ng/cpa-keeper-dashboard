package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strconv"
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
	intervalMS := int64(15 * 60 * 1000)
	wantEnd := (now.UnixMilli() / intervalMS) * intervalMS
	wantStart := wantEnd - int64(5*24*4-1)*intervalMS
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

func TestQuerySummaryAllMatchesThirtyDaysAfterRetention(t *testing.T) {
	store := openTestStore(t)
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, chinaStandardTime)
	var events []usageEvent
	for day := 0; day < 40; day++ {
		events = append(events, fixtureEvent(now.AddDate(0, 0, -day).Add(-time.Hour), "gpt", false, int64(100+day), 10))
	}
	if err := store.writeBatch(events); err != nil {
		t.Fatal(err)
	}
	if err := store.prune(30, now); err != nil {
		t.Fatal(err)
	}
	monthly, err := querySummary(t.Context(), store, url.Values{"range": {"30d"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	all, err := querySummary(t.Context(), store, url.Values{"range": {"all"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if all.Range.IntervalMinutes != 1440 || monthly.Range.IntervalMinutes != 1440 {
		t.Fatalf("trend intervals: all=%d, 30d=%d; want daily buckets", all.Range.IntervalMinutes, monthly.Range.IntervalMinutes)
	}
	if all.KPI.Requests != 30 || monthly.KPI.Requests != 30 || len(all.Trend) != 30 {
		t.Fatalf("retained trend: all requests=%d, 30d requests=%d, points=%d", all.KPI.Requests, monthly.KPI.Requests, len(all.Trend))
	}
	if !reflect.DeepEqual(all.Trend, monthly.Trend) {
		t.Fatalf("all and 30d trends differ:\nall=%+v\n30d=%+v", all.Trend, monthly.Trend)
	}
	wantFirst := time.Date(2026, 8, 11, 0, 0, 0, 0, chinaStandardTime).UnixMilli()
	if all.Trend[0].TimestampMS != wantFirst {
		t.Fatalf("first chart date = %s, want August 11 at China midnight", time.UnixMilli(all.Trend[0].TimestampMS).In(chinaStandardTime))
	}
	if all.Range.FromMS != events[29].TimestampMS || all.Range.ToMS != now.UnixMilli() {
		t.Fatalf("all range does not reflect retained data: %+v", all.Range)
	}
}

func TestQuerySummaryBucketsUseChinaTime(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rangeKey string
		interval int64
		before   time.Time
		at       time.Time
		wantFrom time.Time
	}{
		{
			name: "daily", rangeKey: "30d", interval: 1440,
			before:   time.Date(2026, 9, 7, 23, 59, 0, 0, chinaStandardTime),
			at:       time.Date(2026, 9, 8, 0, 0, 0, 0, chinaStandardTime),
			wantFrom: time.Date(2026, 9, 7, 0, 0, 0, 0, chinaStandardTime),
		},
		{
			name: "six hours", rangeKey: "7d", interval: 360,
			before:   time.Date(2026, 9, 8, 5, 59, 0, 0, chinaStandardTime),
			at:       time.Date(2026, 9, 8, 6, 0, 0, 0, chinaStandardTime),
			wantFrom: time.Date(2026, 9, 8, 0, 0, 0, 0, chinaStandardTime),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := openTestStore(t)
			if err := store.writeBatch([]usageEvent{
				fixtureEvent(tc.before, "gpt", false, 100, 10),
				fixtureEvent(tc.at, "gpt", true, 200, 20),
			}); err != nil {
				t.Fatal(err)
			}
			result, err := querySummary(t.Context(), store, url.Values{"range": {tc.rangeKey}}, tc.at.Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			if result.Range.IntervalMinutes != tc.interval || len(result.Trend) != 2 {
				t.Fatalf("expected two China-time buckets, got range=%+v trend=%+v", result.Range, result.Trend)
			}
			if result.Trend[0].TimestampMS != tc.wantFrom.UnixMilli() || result.Trend[1].TimestampMS != tc.at.UnixMilli() {
				t.Fatalf("incorrect China-time boundaries: %+v", result.Trend)
			}
			if result.Trend[0].Input != 100 || result.Trend[1].Input != 200 {
				t.Fatalf("usage assigned to the wrong bucket: %+v", result.Trend)
			}
		})
	}
}

func TestQuerySummaryAllChoosesDailyOrWeeklyFromStoredData(t *testing.T) {
	for _, tc := range []struct {
		name     string
		age      time.Duration
		interval int64
	}{
		{"single event", 0, 1440},
		{"short history", 24 * time.Hour, 1440},
		{"45 days", 45 * 24 * time.Hour, 1440},
		{"over 45 days", 45*24*time.Hour + time.Minute, 10080},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := openTestStore(t)
			now := time.Date(2026, 9, 9, 12, 0, 0, 0, chinaStandardTime)
			first := now.Add(-tc.age)
			if err := store.writeBatch([]usageEvent{fixtureEvent(first, "gpt", false, 100, 10)}); err != nil {
				t.Fatal(err)
			}
			// Summary reads must continue to work entirely from minute rollups.
			if _, err := store.db.Exec("DROP TABLE usage_events"); err != nil {
				t.Fatal(err)
			}
			result, err := querySummary(t.Context(), store, url.Values{"range": {"all"}}, now)
			if err != nil {
				t.Fatal(err)
			}
			if result.Range.IntervalMinutes != tc.interval || result.Range.FromMS != first.UnixMilli() {
				t.Fatalf("incorrect all range for %s: %+v", tc.name, result.Range)
			}
			if len(result.Trend) != 1 || result.Trend[0].Requests != 1 {
				t.Fatalf("single stored event produced incorrect trend: %+v", result.Trend)
			}
			wantDate := time.Date(first.Year(), first.Month(), first.Day(), 0, 0, 0, 0, chinaStandardTime)
			if result.Trend[0].TimestampMS != wantDate.UnixMilli() {
				t.Fatalf("chart starts before first stored date: %s, want %s", time.UnixMilli(result.Trend[0].TimestampMS).In(chinaStandardTime), wantDate)
			}
		})
	}
}

func TestQuerySummaryAllWeeklyBucketsStartOnChinaMonday(t *testing.T) {
	store := openTestStore(t)
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, chinaStandardTime)
	if err := store.writeBatch([]usageEvent{
		fixtureEvent(time.Date(2026, 7, 1, 12, 0, 0, 0, chinaStandardTime), "gpt", false, 50, 5),
		fixtureEvent(time.Date(2026, 9, 6, 23, 59, 0, 0, chinaStandardTime), "gpt", false, 100, 10),
		fixtureEvent(time.Date(2026, 9, 7, 0, 0, 0, 0, chinaStandardTime), "gpt", false, 200, 20),
	}); err != nil {
		t.Fatal(err)
	}
	result, err := querySummary(t.Context(), store, url.Values{"range": {"all"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if result.Range.IntervalMinutes != 10080 || len(result.Trend) < 3 {
		t.Fatalf("expected a weekly trend: %+v", result.Range)
	}
	last := result.Trend[len(result.Trend)-2:]
	if last[0].Input != 100 || last[1].Input != 200 {
		t.Fatalf("Sunday and Monday usage merged: %+v", last)
	}
	for _, point := range result.Trend[1:] {
		at := time.UnixMilli(point.TimestampMS).In(chinaStandardTime)
		if at.Weekday() != time.Monday || at.Hour() != 0 || at.Minute() != 0 {
			t.Fatalf("weekly boundary is not China Monday midnight: %s", at)
		}
	}
}

func TestQuerySummaryAllEmptyAndFutureOnly(t *testing.T) {
	store := openTestStore(t)
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, chinaStandardTime)
	for _, futureOnly := range []bool{false, true} {
		if futureOnly {
			if err := store.writeBatch([]usageEvent{fixtureEvent(now.Add(time.Hour), "gpt", false, 100, 10)}); err != nil {
				t.Fatal(err)
			}
		}
		result, err := querySummary(t.Context(), store, url.Values{"range": {"all"}}, now)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Trend) != 0 || result.KPI.Requests != 0 || result.Range.IntervalMinutes != 1440 || result.Range.FromMS != result.Range.ToMS {
			t.Fatalf("empty all range, futureOnly=%t: range=%+v trend=%+v", futureOnly, result.Range, result.Trend)
		}
	}
}

func TestHealthRangeRollingFiveDays(t *testing.T) {
	now := time.Date(2026, 8, 26, 11, 30, 0, 0, time.UTC)
	rng := chinaHealthRange(now)
	intervalMS := int64(15 * 60 * 1000)
	wantEnd := (now.UnixMilli() / intervalMS) * intervalMS
	wantStart := wantEnd - int64(5*24*4-1)*intervalMS
	if rng.FromMS != wantStart || rng.ToMS != wantEnd+intervalMS-1 {
		t.Fatalf("health range = %d..%d, want rolling %d..%d", rng.FromMS, rng.ToMS, wantStart, wantEnd+intervalMS-1)
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

func TestParseCustomRangeAndPresets(t *testing.T) {
	now := time.Date(2026, 9, 25, 8, 30, 0, 0, time.UTC)
	from := now.Add(-90 * time.Minute)
	to := now.Add(-30 * time.Minute)
	for _, tc := range []struct {
		name  string
		query url.Values
	}{
		{
			name: "unix milliseconds",
			query: url.Values{"range": {"custom"}, "from": {strconv.FormatInt(from.UnixMilli(), 10)},
				"to": {strconv.FormatInt(to.UnixMilli(), 10)}},
		},
		{
			name: "RFC3339 China offsets",
			query: url.Values{"range": {"custom"}, "from": {from.In(time.FixedZone("CST", 8*60*60)).Format(time.RFC3339)},
				"to": {to.In(time.FixedZone("CST", 8*60*60)).Format(time.RFC3339)}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rng, err := parseRange(tc.query, now)
			if err != nil {
				t.Fatal(err)
			}
			if rng.FromMS != from.UnixMilli() || rng.ToMS != to.UnixMilli() || rng.Label != "custom" {
				t.Fatalf("custom range=%+v, want %d..%d", rng, from.UnixMilli(), to.UnixMilli())
			}
		})
	}

	for _, tc := range []struct {
		name  string
		query url.Values
	}{
		{name: "missing from", query: url.Values{"range": {"custom"}, "to": {strconv.FormatInt(to.UnixMilli(), 10)}}},
		{name: "missing to", query: url.Values{"range": {"custom"}, "from": {strconv.FormatInt(from.UnixMilli(), 10)}}},
		{name: "invalid timestamp", query: url.Values{"range": {"custom"}, "from": {"yesterday"}, "to": {strconv.FormatInt(to.UnixMilli(), 10)}}},
		{name: "equal bounds", query: url.Values{"range": {"custom"}, "from": {strconv.FormatInt(from.UnixMilli(), 10)}, "to": {strconv.FormatInt(from.UnixMilli(), 10)}}},
		{name: "reversed bounds", query: url.Values{"range": {"custom"}, "from": {strconv.FormatInt(to.UnixMilli(), 10)}, "to": {strconv.FormatInt(from.UnixMilli(), 10)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseRange(tc.query, now); err == nil {
				t.Fatal("invalid custom range was accepted")
			}
		})
	}

	preset, err := parseRange(url.Values{
		"range": {"7d"}, "from": {strconv.FormatInt(from.UnixMilli(), 10)},
		"to": {strconv.FormatInt(to.UnixMilli(), 10)},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if preset.FromMS != now.AddDate(0, 0, -7).UnixMilli() || preset.ToMS != now.UnixMilli() || preset.Label != "7d" {
		t.Fatalf("custom bounds changed the 7d preset: %+v", preset)
	}
}

func TestCustomRangeFiltersEventsAtInclusiveMillisecondBounds(t *testing.T) {
	store := openTestStore(t)
	now := time.Date(2026, 9, 25, 8, 30, 0, 0, time.UTC)
	from, to := now.Add(-2*time.Minute), now.Add(-time.Minute)
	events := []usageEvent{
		fixtureEvent(from.Add(-time.Millisecond), "before", false, 10, 1),
		fixtureEvent(from, "at-from", false, 20, 2),
		fixtureEvent(to, "at-to", false, 30, 3),
		fixtureEvent(to.Add(time.Millisecond), "after", false, 40, 4),
	}
	if err := store.writeBatch(events); err != nil {
		t.Fatal(err)
	}
	query := url.Values{
		"range": {"custom"}, "from": {strconv.FormatInt(from.UnixMilli(), 10)},
		"to": {strconv.FormatInt(to.UnixMilli(), 10)},
	}
	filter, err := parseEventFilter(query, now)
	if err != nil {
		t.Fatal(err)
	}
	page, err := queryEvents(t.Context(), store, filter)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Events) != 2 || page.Events[0].Model != "at-to" || page.Events[1].Model != "at-from" {
		t.Fatalf("custom event range included wrong boundaries: %+v", page)
	}
	summary, err := querySummary(t.Context(), store, query, now)
	if err != nil {
		t.Fatal(err)
	}
	if summary.KPI.Requests != page.Total {
		t.Fatalf("custom range summary=%d events=%d", summary.KPI.Requests, page.Total)
	}
	analysis, err := queryAnalysis(t.Context(), store, query, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(analysis.Models) != 2 || analysis.Models[0].Requests != 1 || analysis.Models[1].Requests != 1 {
		t.Fatalf("custom range analysis included wrong events: %+v", analysis.Models)
	}
}

func TestAggregateQueriesRespectExactMillisecondRange(t *testing.T) {
	store := openTestStore(t)
	now := time.Date(2026, 9, 9, 12, 0, 30, 0, chinaStandardTime)
	from := now.AddDate(0, 0, -30)
	outside := fixtureEvent(from.Add(-20*time.Second), "outside", false, 100, 10)
	outside.APIKeyHash = "1111111111111111"
	outside.UpstreamKey = "aaaaaaaaaaaaaaaa"
	inside := fixtureEvent(from.Add(20*time.Second), "inside", false, 200, 20)
	inside.APIKeyHash = "2222222222222222"
	inside.UpstreamKey = "bbbbbbbbbbbbbbbb"
	if err := store.writeBatch([]usageEvent{outside, inside}); err != nil {
		t.Fatal(err)
	}
	query := url.Values{"range": {"30d"}}
	summary, err := querySummary(t.Context(), store, query, now)
	if err != nil {
		t.Fatal(err)
	}
	analysis, err := queryAnalysis(t.Context(), store, query, now)
	if err != nil {
		t.Fatal(err)
	}
	interfaces, err := queryInterfaces(t.Context(), store, query, now)
	if err != nil {
		t.Fatal(err)
	}
	filter, err := parseEventFilter(query, now)
	if err != nil {
		t.Fatal(err)
	}
	page, err := queryEvents(t.Context(), store, filter)
	if err != nil {
		t.Fatal(err)
	}
	if summary.KPI.Requests != 1 || page.Total != 1 || summary.KPI.Requests != page.Total {
		t.Fatalf("summary requests=%d, events=%d; want matching exact-range count", summary.KPI.Requests, page.Total)
	}
	if len(analysis.Models) != 1 || analysis.Models[0].Name != "inside" || analysis.Models[0].Requests != 1 {
		t.Fatalf("analysis included an out-of-range boundary event: %+v", analysis.Models)
	}
	if len(interfaces.APIKeys) != 1 || len(interfaces.Upstreams) != 1 {
		t.Fatalf("interface statistics included an out-of-range boundary event: %+v", interfaces)
	}
}

func TestQueryEventsCountUsesRollupsAndExactBoundaryEvents(t *testing.T) {
	store := openTestStore(t)
	minute := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	from := minute.Add(-3*time.Minute + 10*time.Second)
	to := minute.Add(20 * time.Second)
	events := []usageEvent{
		fixtureEvent(from.Add(-time.Millisecond), "outside", false, 1, 1),
		fixtureEvent(from.Add(20*time.Second), "inside", false, 1, 1),
		fixtureEvent(minute.Add(-2*time.Minute+10*time.Second), "inside", false, 1, 1),
		fixtureEvent(minute.Add(-2*time.Minute+20*time.Second), "inside", false, 1, 1),
		fixtureEvent(minute.Add(-time.Minute+30*time.Second), "inside", false, 1, 1),
		fixtureEvent(minute.Add(10*time.Second), "inside", false, 1, 1),
		fixtureEvent(to.Add(time.Millisecond), "outside", false, 1, 1),
	}
	if err := store.writeBatch(events); err != nil {
		t.Fatal(err)
	}

	page, err := queryEvents(t.Context(), store, eventFilter{
		FromMS: from.UnixMilli(), ToMS: to.UnixMilli(), Page: 1, PageSize: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 5 || page.Pages != 3 || len(page.Events) != 2 {
		t.Fatalf("unfiltered page total=%d pages=%d events=%d, want total=5 pages=3 events=2", page.Total, page.Pages, len(page.Events))
	}

	filtered, err := queryEvents(t.Context(), store, eventFilter{
		FromMS: from.UnixMilli(), ToMS: to.UnixMilli(), Model: "inside", Page: 1, PageSize: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if filtered.Total != 5 {
		t.Fatalf("filtered event total=%d, want 5", filtered.Total)
	}

	all, err := queryEvents(t.Context(), store, eventFilter{
		FromMS: 0, ToMS: to.UnixMilli(), Page: 1, PageSize: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if all.Total != 6 {
		t.Fatalf("all-range event total=%d, want 6", all.Total)
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

func TestQueryAnalysisCombinedScanMatchesDimensionQueries(t *testing.T) {
	store, now := seededQueryStore(t)
	cases := []struct {
		name  string
		query url.Values
	}{
		{name: "24h", query: url.Values{"range": {"24h"}}},
		{name: "custom edges", query: url.Values{
			"range": {"custom"},
			"from":  {now.Add(-90 * time.Minute).Format(time.RFC3339)},
			"to":    {now.Add(-20 * time.Minute).Format(time.RFC3339)},
		}},
		{name: "all", query: url.Values{"range": {"all"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := queryAnalysis(t.Context(), store, tc.query, now)
			if err != nil {
				t.Fatal(err)
			}
			rng, err := parseRange(tc.query, now)
			if err != nil {
				t.Fatal(err)
			}
			prices, err := loadPriceMap(t.Context(), store)
			if err != nil {
				t.Fatal(err)
			}
			for _, dimension := range []struct {
				name, key, label string
			}{
				{"models", "model", "model"},
				{"providers", "provider", "provider"},
				{"api_keys", "api_key_hash", "api_key_mask"},
				{"sources", "source", "source"},
			} {
				want, err := queryDimension(t.Context(), store, rng, dimension.key, dimension.label, "", "", prices)
				if err != nil {
					t.Fatal(err)
				}
				switch dimension.name {
				case "api_keys":
					anonymizeDimensionStats(want, "key", false)
				case "sources":
					maskProviderCredentialStats(want, false)
					want = mergeDimensionStatsByName(want, "source")
				}
				wantJSON, err := json.Marshal(want)
				if err != nil {
					t.Fatal(err)
				}
				gotJSON, err := json.Marshal(got.Distributions[dimension.name])
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(gotJSON, wantJSON) {
					t.Errorf("%s distribution differs from per-dimension query:\ngot:  %s\nwant: %s", dimension.name, gotJSON, wantJSON)
				}
			}
		})
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
	if page.Events[0].CostUSD <= 0 || page.Events[0].InputPrice <= 0 {
		t.Fatalf("event cost or price missing: %+v", page.Events[0])
	}
}

func TestQueryEventsLoadsPricesBeforeOpeningRows(t *testing.T) {
	store, now := seededQueryStore(t)
	store.db.SetMaxOpenConns(1)
	store.db.SetMaxIdleConns(1)
	store.priceMu.Lock()
	store.priceLoaded = false
	store.priceList = nil
	store.priceMap = nil
	store.priceMu.Unlock()

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	page, err := queryEvents(ctx, store, eventFilter{
		FromMS: now.Add(-24 * time.Hour).UnixMilli(), ToMS: now.UnixMilli(), Page: 1, PageSize: 25,
	})
	if err != nil {
		t.Fatalf("single-connection event query deadlocked while loading prices: %v", err)
	}
	if page.Total != 3 || len(page.Events) != 3 {
		t.Fatalf("unexpected page after cold price load: %+v", page)
	}
}

func TestPublicAPIKeyIdentifierFiltersEventsAndCSV(t *testing.T) {
	store, now := seededQueryStore(t)
	analysis, err := queryAnalysis(t.Context(), store, url.Values{"range": {"all"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	stats := analysis.Distributions["api_keys"]
	if len(stats) == 0 {
		t.Fatal("API key distribution is empty")
	}
	selected := stats[0]
	filter := eventFilter{FromMS: 1, ToMS: now.UnixMilli(), APIKeyHash: selected.Key, Page: 1, PageSize: 25}
	page, err := queryEvents(t.Context(), store, filter)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != selected.Requests || int64(len(page.Events)) != selected.Requests {
		t.Fatalf("public key %q reports %d requests but filters %d events", selected.Key, selected.Requests, page.Total)
	}
	csvBody, err := exportEventsCSV(t.Context(), store, filter, 100)
	if err != nil {
		t.Fatal(err)
	}
	if rows := strings.Count(string(csvBody), "\n") - 1; int64(rows) != selected.Requests {
		t.Fatalf("CSV rows=%d, want %d for public key %q", rows, selected.Requests, selected.Key)
	}
}

func TestAnalysisReturnsActualTokenCategoryCosts(t *testing.T) {
	store := openTestStore(t)
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	event := fixtureEvent(now.Add(-time.Hour), "priced-model", false, 1_000_000, 1_000_000)
	event.CacheReadTokens = 0
	if err := store.writeBatch([]usageEvent{event}); err != nil {
		t.Fatal(err)
	}
	if err := replacePrices(t.Context(), store, []modelPrice{{
		Model: "priced-model", InputPerMillion: 1, OutputPerMillion: 10,
	}}); err != nil {
		t.Fatal(err)
	}
	result, err := queryAnalysis(t.Context(), store, url.Values{"range": {"24h"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Models) != 1 {
		t.Fatalf("models=%+v, want one model", result.Models)
	}
	model := result.Models[0]
	if model.Costs.Input != 1 || model.Costs.Output != 10 || model.CostUSD != 11 || model.Costs.total() != model.CostUSD {
		t.Fatalf("actual category costs were not preserved: %+v", model)
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

func TestStreamingBackupJSONRoundTrip(t *testing.T) {
	source, _ := seededQueryStore(t)
	raw, err := exportBackupJSON(t.Context(), source, 3)
	if err != nil {
		t.Fatal(err)
	}
	var payload backupPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("backup JSON is invalid: %v", err)
	}
	if payload.Version != 1 || payload.Events == nil || payload.Prices == nil || len(payload.Events) != 3 || len(payload.Prices) != 2 {
		t.Fatalf("backup payload is incomplete: %+v", payload)
	}
	target := openTestStore(t)
	if result, err := importBackup(t.Context(), target, payload); err != nil || result.Events != 3 {
		t.Fatalf("backup cannot be restored: result=%+v err=%v", result, err)
	}
	if _, err := exportBackupJSON(t.Context(), source, 2); !errors.Is(err, errBackupLimitExceeded) {
		t.Fatalf("oversized backup error=%v", err)
	}
}

func TestStreamingBackupJSONEmptyCollections(t *testing.T) {
	store := openTestStore(t)
	raw, err := exportBackupJSON(t.Context(), store, 1)
	if err != nil {
		t.Fatal(err)
	}
	var payload backupPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Events == nil || payload.Prices == nil || len(payload.Events) != 0 || len(payload.Prices) != 0 {
		t.Fatalf("empty backup collections are not restorable: %+v", payload)
	}
	if _, err := importBackup(t.Context(), store, payload); err != nil {
		t.Fatalf("empty backup cannot be restored: %v", err)
	}
}

func TestExportBackupLoadsPricesBeforeOpeningEventRows(t *testing.T) {
	store, _ := seededQueryStore(t)
	store.db.SetMaxOpenConns(1)
	store.db.SetMaxIdleConns(1)
	store.priceMu.Lock()
	store.priceLoaded = false
	store.priceList = nil
	store.priceMap = nil
	store.priceMu.Unlock()

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	payload, err := exportBackup(ctx, store, 100)
	if err != nil {
		t.Fatalf("single-connection backup export deadlocked while loading prices: %v", err)
	}
	if len(payload.Events) != 3 || len(payload.Prices) != 2 {
		t.Fatalf("unexpected backup after cold price load: %+v", payload)
	}
}

func TestImportBackupRejectsIncompletePayloadWithoutChangingData(t *testing.T) {
	store, _ := seededQueryStore(t)
	before := store.status()

	for _, payload := range []backupPayload{
		{Version: 1, Truncated: true, Events: []usageEvent{}, Prices: []modelPrice{}},
		{Version: 1},
		{Version: 1, Events: []usageEvent{}},
		{Version: 1, Prices: []modelPrice{}},
	} {
		if _, err := importBackup(t.Context(), store, payload); err == nil {
			t.Fatalf("incomplete backup was accepted: %+v", payload)
		}
		after := store.status()
		if after.EventCount != before.EventCount || after.RollupCount != before.RollupCount {
			t.Fatalf("rejected backup changed data: before=%+v after=%+v", before, after)
		}
	}
}

func TestImportBackupAcceptsExplicitEmptyCollections(t *testing.T) {
	store, _ := seededQueryStore(t)
	result, err := importBackup(t.Context(), store, backupPayload{
		Version: 1, Events: []usageEvent{}, Prices: []modelPrice{},
	})
	if err != nil {
		t.Fatal(err)
	}
	status := store.status()
	if result.Events != 0 || result.Prices != 0 || status.EventCount != 0 || status.RollupCount != 0 {
		t.Fatalf("explicit empty restore did not clear data: result=%+v status=%+v", result, status)
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

func TestAllCurrentMinuteUsesExactRange(t *testing.T) {
	store := openTestStore(t)
	now := time.Date(2026, 9, 9, 12, 0, 30, 0, chinaStandardTime)
	if err := store.writeBatch([]usageEvent{
		fixtureEvent(now.Add(-20*time.Second), "past", false, 1, 1),
		fixtureEvent(now.Add(20*time.Second), "future", false, 1, 1),
	}); err != nil {
		t.Fatal(err)
	}
	query := url.Values{"range": {"all"}}
	summary, err := querySummary(t.Context(), store, query, now)
	if err != nil {
		t.Fatal(err)
	}
	filter, err := parseEventFilter(query, now)
	if err != nil {
		t.Fatal(err)
	}
	page, err := queryEvents(t.Context(), store, filter)
	if err != nil {
		t.Fatal(err)
	}
	if summary.KPI.Requests != page.Total {
		t.Fatalf("summary/detail mismatch: summary=%d detail=%d", summary.KPI.Requests, page.Total)
	}
	analysis, err := queryAnalysis(t.Context(), store, query, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(analysis.Models) != 1 || analysis.Models[0].Requests != page.Total || analysis.Models[0].Name != "past" {
		t.Fatalf("analysis included future event: %+v", analysis.Models)
	}
	interfaces, err := queryInterfaces(t.Context(), store, query, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(interfaces.APIKeys) != 1 || interfaces.APIKeys[0].Requests != page.Total {
		t.Fatalf("interfaces included future event: %+v", interfaces.APIKeys)
	}
}
