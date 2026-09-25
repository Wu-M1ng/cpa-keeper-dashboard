package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const chinaTimeOffsetMinutes = 8 * 60

var chinaStandardTime = time.FixedZone("CST", chinaTimeOffsetMinutes*60)

type timeRange struct {
	FromMS          int64  `json:"from_ms"`
	ToMS            int64  `json:"to_ms"`
	Label           string `json:"label"`
	IntervalMinutes int64  `json:"interval_minutes"`
}

type kpiStats struct {
	Requests         int64   `json:"requests"`
	Successes        int64   `json:"successes"`
	Failures         int64   `json:"failures"`
	SuccessRate      float64 `json:"success_rate"`
	TotalTokens      int64   `json:"total_tokens"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	ReasoningTokens  int64   `json:"reasoning_tokens"`
	CostUSD          float64 `json:"cost_usd"`
	SavedCostUSD     float64 `json:"saved_cost_usd"`
	AvgLatencyMS     float64 `json:"avg_latency_ms"`
	AvgTTFTMS        float64 `json:"avg_ttft_ms"`
	AvgRequestsDaily float64 `json:"avg_requests_daily"`
	AvgTokensDaily   float64 `json:"avg_tokens_daily"`
	AvgCostDaily     float64 `json:"avg_cost_daily"`
	RPM              float64 `json:"rpm"`
	TPM              float64 `json:"tpm"`
	CacheRate        float64 `json:"cache_rate"`
	RangeLabel       string  `json:"range_label"`
}

type trendPoint struct {
	TimestampMS  int64   `json:"timestamp_ms"`
	Requests     int64   `json:"requests"`
	Tokens       int64   `json:"tokens"`
	Input        int64   `json:"input"`
	Output       int64   `json:"output"`
	CacheWrite   int64   `json:"cache_write"`
	CacheRead    int64   `json:"cache_read"`
	Reasoning    int64   `json:"reasoning"`
	HitRate      float64 `json:"hit_rate"`
	CostUSD      float64 `json:"cost_usd"`
	ActualCost   float64 `json:"actual_cost"`
	StandardCost float64 `json:"standard_cost"`
}

type healthPoint struct {
	TimestampMS int64   `json:"timestamp_ms"`
	Requests    int64   `json:"requests"`
	Failures    int64   `json:"failures"`
	SuccessRate float64 `json:"success_rate"`
}

type summaryResponse struct {
	Range       timeRange     `json:"range"`
	KPI         kpiStats      `json:"kpi"`
	Health      []healthPoint `json:"health"`
	Trend       []trendPoint  `json:"trend"`
	Runtime     runtimeStatus `json:"runtime"`
	GeneratedAt string        `json:"generated_at"`
}

type dimensionStat struct {
	Key          string      `json:"key"`
	Name         string      `json:"name"`
	Requests     int64       `json:"requests"`
	Successes    int64       `json:"successes"`
	Failures     int64       `json:"failures"`
	SuccessRate  float64     `json:"success_rate"`
	TotalTokens  int64       `json:"total_tokens"`
	CostUSD      float64     `json:"cost_usd"`
	SavedCostUSD float64     `json:"saved_cost_usd"`
	AvgLatencyMS float64     `json:"avg_latency_ms"`
	Tokens       tokenTotals `json:"tokens"`
	Costs        costTotals  `json:"costs"`
	Models       int         `json:"models,omitempty"`

	latencySum int64
	modelSet   map[string]struct{}
	provider   string
}

type analysisResponse struct {
	Range         timeRange                  `json:"range"`
	Distributions map[string][]dimensionStat `json:"distributions"`
	Tokens        tokenTotals                `json:"tokens"`
	Models        []dimensionStat            `json:"models"`
	GeneratedAt   string                     `json:"generated_at"`
}

type interfacesResponse struct {
	Range       timeRange       `json:"range"`
	APIKeys     []dimensionStat `json:"api_keys"`
	Upstreams   []dimensionStat `json:"upstreams"`
	GeneratedAt string          `json:"generated_at"`
}

type upstreamDetailResponse struct {
	Key          string          `json:"key"`
	Name         string          `json:"name"`
	Provider     string          `json:"provider,omitempty"`
	Endpoint     string          `json:"endpoint,omitempty"`
	Range        timeRange       `json:"range"`
	Summary      dimensionStat   `json:"summary"`
	Models       []dimensionStat `json:"models"`
	RecentEvents []usageEvent    `json:"recent_events"`
	GeneratedAt  string          `json:"generated_at"`
}

type eventFilter struct {
	FromMS     int64
	ToMS       int64
	Model      string
	Provider   string
	APIKeyHash string
	Upstream   string
	Status     string
	Search     string
	Page       int
	PageSize   int
}

type eventsPage struct {
	Events          []usageEvent `json:"events"`
	Total           int64        `json:"total"`
	Page            int          `json:"page"`
	PageSize        int          `json:"page_size"`
	Pages           int          `json:"pages"`
	AccessiblePages int          `json:"accessible_pages"`
	GeneratedAt     string       `json:"generated_at"`
}

type aggregateRow struct {
	Bucket       int64
	Model        string
	Requests     int64
	Successes    int64
	Failures     int64
	Tokens       tokenTotals
	LatencySumMS int64
	TTFTSumMS    int64
	TTFTCount    int64
}

type aggregateKey struct {
	bucket int64
	model  string
}

type minuteSpan struct {
	from int64
	to   int64
}

const (
	summaryAggregateTrendOnly = iota
	summaryAggregateTrendAndHealth
	summaryAggregateHealthOnly
)

func parseRange(query url.Values, now time.Time) (timeRange, error) {
	now = now.UTC()
	label := strings.TrimSpace(query.Get("range"))
	if label == "" {
		label = "24h"
	}
	to := now
	var from time.Time
	switch label {
	case "24h":
		from = to.Add(-24 * time.Hour)
	case "7d":
		from = to.AddDate(0, 0, -7)
	case "30d":
		from = to.AddDate(0, 0, -30)
	case "all":
		from = time.Unix(0, 0).UTC()
	case "custom":
		parsedFrom, err := parseTimeValue(query.Get("from"))
		if err != nil {
			return timeRange{}, errors.New("invalid from time")
		}
		parsedTo, err := parseTimeValue(query.Get("to"))
		if err != nil {
			return timeRange{}, errors.New("invalid to time")
		}
		from, to = parsedFrom, parsedTo
	default:
		return timeRange{}, errors.New("range must be 24h, 7d, 30d, all, or custom")
	}
	if !from.Before(to) {
		return timeRange{}, errors.New("from must be before to")
	}
	duration := to.Sub(from)
	interval := int64(60)
	switch {
	case duration <= 48*time.Hour:
		interval = 60
	case duration <= 10*24*time.Hour:
		interval = 360
	case duration <= 45*24*time.Hour:
		interval = 1440
	default:
		interval = 10080
	}
	return timeRange{FromMS: from.UnixMilli(), ToMS: to.UnixMilli(), Label: label, IntervalMinutes: interval}, nil
}

func parseTimeValue(value string) (time.Time, error) {
	if millis, err := strconv.ParseInt(value, 10, 64); err == nil {
		return time.UnixMilli(millis).UTC(), nil
	}
	return time.Parse(time.RFC3339, value)
}

func chinaHealthRange(now time.Time) timeRange {
	intervalMS := int64(15 * 60 * 1000)
	totalSlots := int64(5 * 24 * 4) // 480 slots (120 hours)
	endBucket := (now.UnixMilli() / intervalMS) * intervalMS
	startBucket := endBucket - (totalSlots-1)*intervalMS
	return timeRange{
		FromMS:          startBucket,
		ToMS:            endBucket + intervalMS - 1,
		Label:           "5d",
		IntervalMinutes: 15,
	}
}

func querySummary(ctx context.Context, store *eventStore, query url.Values, now time.Time) (summaryResponse, error) {
	rng, err := parseRange(query, now)
	if err != nil {
		return summaryResponse{}, err
	}
	if rng.Label == "all" {
		firstTimestamp, err := firstStoredTimestamp(ctx, store, rng.ToMS)
		if err != nil {
			return summaryResponse{}, err
		}
		if firstTimestamp == 0 {
			rng.FromMS = rng.ToMS
		} else {
			rng.FromMS = firstTimestamp
		}
		// Keep short histories comparable with 30d; only long histories need weekly buckets.
		rng.IntervalMinutes = 1440
		if time.Duration(rng.ToMS-rng.FromMS)*time.Millisecond > 45*24*time.Hour {
			rng.IntervalMinutes = 10080
		}
	}
	prices, err := loadPriceMap(ctx, store)
	if err != nil {
		return summaryResponse{}, err
	}
	mainFrom, mainTo, boundaries := splitAggregateRange(rng)
	if rng.Label == "all" && len(boundaries) > 0 {
		available, err := usageEventsTableAvailable(ctx, store)
		if err != nil {
			return summaryResponse{}, err
		}
		if !available {
			mainTo = rng.ToMS / 60000
			boundaries = nil
		}
	}
	healthRange := chinaHealthRange(now)
	rows, healthByBucket, err := querySummaryAggregateRows(ctx, store, rng.IntervalMinutes,
		mainFrom, mainTo, boundaries, healthRange)
	if err != nil {
		return summaryResponse{}, err
	}
	result := summaryResponse{Range: rng, Trend: []trendPoint{}, Health: []healthPoint{}, GeneratedAt: now.UTC().Format(time.RFC3339)}
	result.KPI.RangeLabel = rng.Label
	if runtime := currentRuntime(); runtime != nil && runtime.store == store {
		result.Runtime = runtime.summaryStatus()
	} else {
		result.Runtime.Storage = store.statusSnapshot()
	}
	trendByBucket := make(map[int64]*trendPoint)
	var latencySum, ttftSum, ttftCount int64
	for _, row := range rows {
		result.KPI.Requests += row.Requests
		result.KPI.Successes += row.Successes
		result.KPI.Failures += row.Failures
		result.KPI.TotalTokens += row.Tokens.Total
		result.KPI.InputTokens += row.Tokens.Input
		result.KPI.OutputTokens += row.Tokens.Output
		result.KPI.CacheReadTokens += row.Tokens.CacheRead
		result.KPI.CacheWriteTokens += row.Tokens.CacheWrite
		result.KPI.ReasoningTokens += row.Tokens.Reasoning
		price := resolvePrice(row.Model, prices)
		actualCost := calculateCost(row.Tokens, price)
		standardCost := calculateStandardCost(row.Tokens, price)
		result.KPI.CostUSD += actualCost
		if saved := standardCost - actualCost; saved > 0 {
			result.KPI.SavedCostUSD += saved
		}
		latencySum += row.LatencySumMS
		ttftSum += row.TTFTSumMS
		ttftCount += row.TTFTCount
		point := trendByBucket[row.Bucket]
		if point == nil {
			point = &trendPoint{TimestampMS: row.Bucket * 60000}
			trendByBucket[row.Bucket] = point
		}
		point.Requests += row.Requests
		point.Tokens += row.Tokens.Total
		point.Input += row.Tokens.Input
		point.Output += row.Tokens.Output
		point.CacheRead += row.Tokens.CacheRead
		point.CacheWrite += row.Tokens.CacheWrite
		point.Reasoning += row.Tokens.Reasoning
		point.CostUSD += actualCost
		point.ActualCost += actualCost
		point.StandardCost += standardCost
	}
	result.KPI.SuccessRate = ratio(result.KPI.Successes, result.KPI.Requests)
	result.KPI.AvgLatencyMS = average(latencySum, result.KPI.Requests)
	result.KPI.AvgTTFTMS = average(ttftSum, ttftCount)
	result.KPI.CacheRate = ratio(result.KPI.CacheReadTokens, result.KPI.InputTokens)
	duration := effectiveSummaryDuration(rng, rows)
	days := duration.Hours() / 24
	if days < 1 {
		days = 1
	}
	minutes := duration.Minutes()
	if minutes < 1 {
		minutes = 1
	}
	result.KPI.AvgRequestsDaily = float64(result.KPI.Requests) / days
	result.KPI.AvgTokensDaily = float64(result.KPI.TotalTokens) / days
	result.KPI.AvgCostDaily = result.KPI.CostUSD / days
	result.KPI.RPM = float64(result.KPI.Requests) / minutes
	result.KPI.TPM = float64(result.KPI.TotalTokens) / minutes
	appendDenseTrend(&result.Trend, trendByBucket, rng, rng.IntervalMinutes)
	for i := range result.Trend {
		result.Trend[i].HitRate = ratio(result.Trend[i].CacheRead, result.Trend[i].Input)
	}
	sort.Slice(result.Trend, func(i, j int) bool { return result.Trend[i].TimestampMS < result.Trend[j].TimestampMS })
	if rng.Label == "all" && len(result.Trend) > 0 {
		// A partial first week must not imply usage before the first retained date.
		firstDayMS := timeBucketMinute(rng.FromMS/60000, 1440) * 60000
		if result.Trend[0].TimestampMS < firstDayMS {
			result.Trend[0].TimestampMS = firstDayMS
		}
	}

	appendDenseHealth(&result.Health, healthByBucket, healthRange, healthRange.IntervalMinutes)
	sort.Slice(result.Health, func(i, j int) bool { return result.Health[i].TimestampMS < result.Health[j].TimestampMS })
	return result, nil
}

// Return the earliest event visible at the requested end time.  The raw event
// table preserves millisecond precision; a rollup-only legacy database can
// only provide the start of its first minute.
func firstStoredTimestamp(ctx context.Context, store *eventStore, toMS int64) (int64, error) {
	available, err := usageEventsTableAvailable(ctx, store)
	if err != nil {
		return 0, err
	}
	if available {
		var timestamp sql.NullInt64
		if err := store.db.QueryRowContext(ctx,
			"SELECT MIN(timestamp_ms) FROM usage_events WHERE timestamp_ms <= ?", toMS).Scan(&timestamp); err != nil {
			return 0, err
		}
		if timestamp.Valid {
			return timestamp.Int64, nil
		}
		return 0, nil
	}
	var minute sql.NullInt64
	if err := store.db.QueryRowContext(ctx, `SELECT MIN(minute) FROM usage_minute_rollups
		WHERE minute <= ?`, toMS/60000).Scan(&minute); err != nil {
		return 0, err
	}
	if !minute.Valid {
		return 0, nil
	}
	return minute.Int64 * 60000, nil
}

func effectiveSummaryDuration(rng timeRange, rows []aggregateRow) time.Duration {
	if rng.Label != "all" || len(rows) == 0 {
		return time.Duration(rng.ToMS-rng.FromMS) * time.Millisecond
	}
	first, last := rows[0].Bucket, rows[0].Bucket
	for _, row := range rows[1:] {
		if row.Bucket < first {
			first = row.Bucket
		}
		if row.Bucket > last {
			last = row.Bucket
		}
	}
	minutes := last - first + rng.IntervalMinutes
	if minutes < rng.IntervalMinutes {
		minutes = rng.IntervalMinutes
	}
	return time.Duration(minutes) * time.Minute
}

func timeBucketOffset(interval int64) int64 {
	offset := int64(chinaTimeOffsetMinutes)
	if interval == 10080 {
		// The Unix epoch is a Thursday; align weeks to Monday in China time.
		offset += 3 * 1440
	}
	return offset
}

func timeBucketMinute(minute, interval int64) int64 {
	return minute - ((minute+timeBucketOffset(interval))%interval+interval)%interval
}

func appendDenseTrend(target *[]trendPoint, buckets map[int64]*trendPoint, rng timeRange, interval int64) {
	if len(buckets) == 0 {
		return
	}
	start := timeBucketMinute(rng.FromMS/60000, interval)
	end := timeBucketMinute(rng.ToMS/60000, interval)
	if end-start > 600 || rng.Label == "all" {
		keys := make([]int64, 0, len(buckets))
		for key := range buckets {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
		start, end = keys[0], keys[len(keys)-1]
	}
	for bucket := start; bucket <= end; bucket += interval {
		if point := buckets[bucket]; point != nil {
			*target = append(*target, *point)
		} else {
			*target = append(*target, trendPoint{TimestampMS: bucket * 60000})
		}
	}
}

func appendDenseHealth(target *[]healthPoint, buckets map[int64]*healthPoint, rng timeRange, interval int64) {
	start := timeBucketMinute(rng.FromMS/60000, interval)
	end := timeBucketMinute(rng.ToMS/60000, interval)
	for bucket := start; bucket <= end; bucket += interval {
		point := buckets[bucket]
		if point == nil {
			point = &healthPoint{TimestampMS: bucket * 60000}
		}
		point.SuccessRate = ratio(point.Requests-point.Failures, point.Requests)
		*target = append(*target, *point)
	}
}

func querySummaryAggregateRows(ctx context.Context, store *eventStore, trendInterval, mainFrom, mainTo int64, boundaries []exactRange, healthRange timeRange) ([]aggregateRow, map[int64]*healthPoint, error) {
	mainSpan, hasMain := minuteSpan{from: mainFrom, to: mainTo}, mainFrom <= mainTo
	healthFrom, healthTo, _ := splitAggregateRange(healthRange)
	healthSpan, hasHealth := minuteSpan{from: healthFrom, to: healthTo}, healthFrom <= healthTo
	var overlap minuteSpan
	hasOverlap := hasMain && hasHealth
	if hasOverlap {
		overlap.from = mainSpan.from
		if healthSpan.from > overlap.from {
			overlap.from = healthSpan.from
		}
		overlap.to = mainSpan.to
		if healthSpan.to < overlap.to {
			overlap.to = healthSpan.to
		}
		hasOverlap = overlap.from <= overlap.to
	}

	parts := make([]string, 0, 2)
	args := make([]any, 0, 24)
	spans := make([]minuteSpan, 0, 2)
	if hasMain {
		spans = append(spans, mainSpan)
	}
	if hasHealth {
		spans = append(spans, healthSpan)
	}
	spans = coalesceMinuteSpans(spans)
	if len(spans) > 0 {
		kindCases := make([]string, 0, 2)
		kindArgs := make([]any, 0, 4)
		if hasOverlap {
			kindCases = append(kindCases, "WHEN minute BETWEEN ? AND ? THEN 1")
			kindArgs = append(kindArgs, overlap.from, overlap.to)
		}
		if hasMain {
			kindCases = append(kindCases, "WHEN minute BETWEEN ? AND ? THEN 0")
			kindArgs = append(kindArgs, mainSpan.from, mainSpan.to)
		}
		kindExpression := "2"
		if len(kindCases) > 0 {
			kindExpression = "CASE " + strings.Join(kindCases, " ") + " ELSE 2 END"
		}

		bucketExpression := ""
		bucketArgs := make([]any, 0, 16)
		if hasOverlap {
			healthBucket, healthBucketArgs := minuteBucketExpression("minute", healthRange.IntervalMinutes)
			trendBucket, trendBucketArgs := minuteBucketExpression("minute", trendInterval)
			bucketExpression = "CASE WHEN minute BETWEEN ? AND ? THEN " + healthBucket +
				" WHEN minute BETWEEN ? AND ? THEN " + trendBucket + " ELSE " + healthBucket + " END"
			bucketArgs = append(bucketArgs, overlap.from, overlap.to)
			bucketArgs = append(bucketArgs, healthBucketArgs...)
			bucketArgs = append(bucketArgs, mainSpan.from, mainSpan.to)
			bucketArgs = append(bucketArgs, trendBucketArgs...)
			bucketArgs = append(bucketArgs, healthBucketArgs...)
		} else if hasMain {
			trendBucket, trendBucketArgs := minuteBucketExpression("minute", trendInterval)
			healthBucket, healthBucketArgs := minuteBucketExpression("minute", healthRange.IntervalMinutes)
			bucketExpression = "CASE WHEN minute BETWEEN ? AND ? THEN " + trendBucket + " ELSE " + healthBucket + " END"
			bucketArgs = append(bucketArgs, mainSpan.from, mainSpan.to)
			bucketArgs = append(bucketArgs, trendBucketArgs...)
			bucketArgs = append(bucketArgs, healthBucketArgs...)
		} else {
			var healthBucketArgs []any
			bucketExpression, healthBucketArgs = minuteBucketExpression("minute", healthRange.IntervalMinutes)
			bucketArgs = append(bucketArgs, healthBucketArgs...)
		}

		where, rangeArgs := minuteSpanPredicate(spans)
		parts = append(parts, `SELECT `+kindExpression+` AS kind, `+bucketExpression+` AS bucket, model,
			SUM(requests), SUM(successes), SUM(failures), SUM(input_tokens),
			SUM(output_tokens), SUM(reasoning_tokens), SUM(cached_tokens),
			SUM(cache_read_tokens), SUM(cache_creation_tokens), SUM(total_tokens),
			SUM(latency_sum_ms), SUM(ttft_sum_ms), SUM(ttft_count)
			FROM usage_minute_rollups WHERE `+where+` GROUP BY kind, bucket, model`)
		args = append(args, kindArgs...)
		args = append(args, bucketArgs...)
		args = append(args, rangeArgs...)
	}
	if len(boundaries) > 0 {
		where, rangeArgs := exactRangeWhere("timestamp_ms", boundaries)
		bucketExpression, bucketArgs := minuteBucketExpression("timestamp_ms / 60000", trendInterval)
		parts = append(parts, `SELECT 0 AS kind, `+bucketExpression+` AS bucket, model,
			COUNT(*), SUM(CASE WHEN failed = 0 THEN 1 ELSE 0 END),
			SUM(CASE WHEN failed != 0 THEN 1 ELSE 0 END), SUM(input_tokens),
			SUM(output_tokens), SUM(reasoning_tokens), SUM(cached_tokens),
			SUM(cache_read_tokens), SUM(cache_creation_tokens), SUM(total_tokens),
			SUM(latency_ms), SUM(ttft_ms), SUM(CASE WHEN ttft_ms > 0 THEN 1 ELSE 0 END)
			FROM usage_events WHERE `+where+` GROUP BY bucket, model`)
		args = append(args, bucketArgs...)
		args = append(args, rangeArgs...)
	}

	trendByBucket := make(map[aggregateKey]*aggregateRow)
	healthByBucket := make(map[int64]*healthPoint)
	if len(parts) == 0 {
		return nil, healthByBucket, nil
	}
	rows, err := store.db.QueryContext(ctx, strings.Join(parts, " UNION ALL "), args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind int
		var row aggregateRow
		if err := rows.Scan(&kind, &row.Bucket, &row.Model, &row.Requests, &row.Successes, &row.Failures,
			&row.Tokens.Input, &row.Tokens.Output, &row.Tokens.Reasoning, &row.Tokens.Cached,
			&row.Tokens.CacheRead, &row.Tokens.CacheWrite, &row.Tokens.Total,
			&row.LatencySumMS, &row.TTFTSumMS, &row.TTFTCount); err != nil {
			return nil, nil, err
		}
		switch kind {
		case summaryAggregateTrendOnly:
			addAggregateRow(trendByBucket, row)
		case summaryAggregateTrendAndHealth:
			trendRow := row
			trendRow.Bucket = timeBucketMinute(row.Bucket, trendInterval)
			addAggregateRow(trendByBucket, trendRow)
			addSummaryHealthPoint(healthByBucket, row)
		case summaryAggregateHealthOnly:
			addSummaryHealthPoint(healthByBucket, row)
		default:
			return nil, nil, fmt.Errorf("unexpected summary aggregate kind %d", kind)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	result := make([]aggregateRow, 0, len(trendByBucket))
	for _, row := range trendByBucket {
		result = append(result, *row)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Bucket == result[j].Bucket {
			return result[i].Model < result[j].Model
		}
		return result[i].Bucket < result[j].Bucket
	})
	return result, healthByBucket, nil
}

func minuteBucketExpression(column string, interval int64) (string, []any) {
	return column + " - ((" + column + " + ?) % ? + ?) % ?", []any{timeBucketOffset(interval), interval, interval, interval}
}

func minuteSpanPredicate(spans []minuteSpan) (string, []any) {
	conditions := make([]string, 0, len(spans))
	args := make([]any, 0, len(spans)*2)
	for _, span := range spans {
		conditions = append(conditions, "minute BETWEEN ? AND ?")
		args = append(args, span.from, span.to)
	}
	return "(" + strings.Join(conditions, " OR ") + ")", args
}

func coalesceMinuteSpans(spans []minuteSpan) []minuteSpan {
	valid := make([]minuteSpan, 0, len(spans))
	for _, span := range spans {
		if span.from <= span.to {
			valid = append(valid, span)
		}
	}
	sort.Slice(valid, func(i, j int) bool { return valid[i].from < valid[j].from })
	merged := make([]minuteSpan, 0, len(valid))
	for _, span := range valid {
		if len(merged) == 0 || span.from > merged[len(merged)-1].to+1 {
			merged = append(merged, span)
			continue
		}
		if span.to > merged[len(merged)-1].to {
			merged[len(merged)-1].to = span.to
		}
	}
	return merged
}

func addSummaryHealthPoint(points map[int64]*healthPoint, row aggregateRow) {
	point := points[row.Bucket]
	if point == nil {
		point = &healthPoint{TimestampMS: row.Bucket * 60000}
		points[row.Bucket] = point
	}
	point.Requests += row.Requests
	point.Failures += row.Failures
}

func addAggregateRow(rows map[aggregateKey]*aggregateRow, row aggregateRow) {
	key := aggregateKey{bucket: row.Bucket, model: row.Model}
	current := rows[key]
	if current == nil {
		copyOfRow := row
		rows[key] = &copyOfRow
		return
	}
	current.Requests += row.Requests
	current.Successes += row.Successes
	current.Failures += row.Failures
	addTokens(&current.Tokens, row.Tokens)
	current.LatencySumMS += row.LatencySumMS
	current.TTFTSumMS += row.TTFTSumMS
	current.TTFTCount += row.TTFTCount
}

func queryAnalysis(ctx context.Context, store *eventStore, query url.Values, now time.Time) (analysisResponse, error) {
	rng, err := parseRange(query, now)
	if err != nil {
		return analysisResponse{}, err
	}
	prices, err := loadPriceMap(ctx, store)
	if err != nil {
		return analysisResponse{}, err
	}
	result := analysisResponse{
		Range:         rng,
		Distributions: make(map[string][]dimensionStat, 4),
		GeneratedAt:   now.UTC().Format(time.RFC3339),
	}
	dimensions, err := queryAnalysisDimensions(ctx, store, rng, prices)
	if err != nil {
		return analysisResponse{}, err
	}
	result.Distributions["models"] = dimensions.models
	result.Distributions["providers"] = dimensions.providers
	result.Distributions["api_keys"] = dimensions.apiKeys
	result.Distributions["sources"] = dimensions.sources
	result.Models = dimensions.models
	for _, stat := range result.Models {
		addTokens(&result.Tokens, stat.Tokens)
	}
	return result, nil
}

func queryAnalysisDimensions(ctx context.Context, store *eventStore, rng timeRange, prices map[string]modelPrice) (analysisDimensionStats, error) {
	fullFrom, fullTo, boundaries := splitAggregateRange(rng)
	if rng.Label == "all" && len(boundaries) > 0 {
		available, err := usageEventsTableAvailable(ctx, store)
		if err != nil {
			return analysisDimensionStats{}, err
		}
		if !available {
			fullTo = rng.ToMS / 60000
			boundaries = nil
		}
	}

	parts := make([]string, 0, 2)
	args := make([]any, 0, 8)
	if fullFrom <= fullTo {
		parts = append(parts, `SELECT provider, model, source, api_key_hash, MAX(api_key_mask),
			SUM(requests), SUM(successes), SUM(failures), SUM(input_tokens), SUM(output_tokens),
			SUM(reasoning_tokens), SUM(cached_tokens), SUM(cache_read_tokens),
			SUM(cache_creation_tokens), SUM(total_tokens), SUM(latency_sum_ms)
			FROM usage_minute_rollups WHERE minute BETWEEN ? AND ?
			GROUP BY provider, model, source, api_key_hash`)
		args = append(args, fullFrom, fullTo)
	}
	if len(boundaries) > 0 {
		where, boundaryArgs := exactRangeWhere("timestamp_ms", boundaries)
		parts = append(parts, `SELECT provider, model, source, api_key_hash, MAX(api_key_mask),
			COUNT(*), SUM(CASE WHEN failed = 0 THEN 1 ELSE 0 END),
			SUM(CASE WHEN failed != 0 THEN 1 ELSE 0 END), SUM(input_tokens), SUM(output_tokens),
			SUM(reasoning_tokens), SUM(cached_tokens), SUM(cache_read_tokens),
			SUM(cache_creation_tokens), SUM(total_tokens), SUM(latency_ms)
			FROM usage_events WHERE `+where+`
			GROUP BY provider, model, source, api_key_hash`)
		args = append(args, boundaryArgs...)
	}
	groups := analysisDimensionStats{
		models:    make([]dimensionStat, 0),
		providers: make([]dimensionStat, 0),
		apiKeys:   make([]dimensionStat, 0),
		sources:   make([]dimensionStat, 0),
	}
	models := make(map[string]*dimensionStat)
	providers := make(map[string]*dimensionStat)
	apiKeys := make(map[string]*dimensionStat)
	sources := make(map[string]*dimensionStat)
	if len(parts) > 0 {
		rows, err := store.db.QueryContext(ctx, strings.Join(parts, " UNION ALL "), args...)
		if err != nil {
			return analysisDimensionStats{}, err
		}
		defer rows.Close()
		for rows.Next() {
			var provider, model, source, apiKeyHash, apiKeyMask string
			var row dimensionStat
			if err := rows.Scan(&provider, &model, &source, &apiKeyHash, &apiKeyMask,
				&row.Requests, &row.Successes, &row.Failures, &row.Tokens.Input, &row.Tokens.Output,
				&row.Tokens.Reasoning, &row.Tokens.Cached, &row.Tokens.CacheRead,
				&row.Tokens.CacheWrite, &row.Tokens.Total, &row.latencySum); err != nil {
				return analysisDimensionStats{}, err
			}
			price := resolvePrice(model, prices)
			accumulateAnalysisDimension(models, model, model, model, provider, model, false, row, price)
			accumulateAnalysisDimension(providers, provider, provider, provider, provider, model, true, row, price)
			accumulateAnalysisDimension(apiKeys, apiKeyHash, apiKeyHash, apiKeyMask, provider, model, true, row, price)
			accumulateAnalysisDimension(sources, provider+"\x00"+source, source, source, provider, model, true, row, price)
		}
		if err := rows.Err(); err != nil {
			return analysisDimensionStats{}, err
		}
	}

	groups.models = finalizeAnalysisDimensionMap(models)
	groups.providers = finalizeAnalysisDimensionMap(providers)
	groups.apiKeys = finalizeAnalysisDimensionMap(apiKeys)
	anonymizeDimensionStats(groups.apiKeys, "key", false)
	groups.sources = finalizeAnalysisDimensionMap(sources)
	maskProviderCredentialStats(groups.sources, false)
	groups.sources = mergeDimensionStatsByName(groups.sources, "source")
	return groups, nil
}

type analysisDimensionStats struct {
	models    []dimensionStat
	providers []dimensionStat
	apiKeys   []dimensionStat
	sources   []dimensionStat
}

func accumulateAnalysisDimension(groups map[string]*dimensionStat, groupKey, key, label, provider, model string, trackModels bool, row dimensionStat, price modelPrice) {
	stat := groups[groupKey]
	if stat == nil {
		stat = &dimensionStat{Key: key, Name: label, provider: provider}
		if trackModels {
			stat.modelSet = make(map[string]struct{})
		} else {
			stat.Models = 1
		}
		groups[groupKey] = stat
	} else if label > stat.Name {
		stat.Name = label
	}
	stat.Requests += row.Requests
	stat.Successes += row.Successes
	stat.Failures += row.Failures
	stat.latencySum += row.latencySum
	addTokens(&stat.Tokens, row.Tokens)
	stat.TotalTokens = stat.Tokens.Total
	costs := calculateCostTotals(row.Tokens, price)
	addCosts(&stat.Costs, costs)
	stat.CostUSD += costs.total()
	if saved := calculateStandardCost(row.Tokens, price) - costs.total(); saved > 0 {
		stat.SavedCostUSD += saved
	}
	if trackModels {
		stat.modelSet[model] = struct{}{}
	}
}

func finalizeAnalysisDimensionMap(byKey map[string]*dimensionStat) []dimensionStat {
	result := make([]dimensionStat, 0, len(byKey))
	for _, stat := range byKey {
		stat.SuccessRate = ratio(stat.Successes, stat.Requests)
		stat.AvgLatencyMS = average(stat.latencySum, stat.Requests)
		if stat.modelSet != nil {
			stat.Models = len(stat.modelSet)
		}
		result = append(result, *stat)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Requests == result[j].Requests {
			return result[i].Name < result[j].Name
		}
		return result[i].Requests > result[j].Requests
	})
	return result
}

func queryInterfaces(ctx context.Context, store *eventStore, query url.Values, now time.Time) (interfacesResponse, error) {
	rng, err := parseRange(query, now)
	if err != nil {
		return interfacesResponse{}, err
	}
	prices, err := loadPriceMap(ctx, store)
	if err != nil {
		return interfacesResponse{}, err
	}
	apiKeys, err := queryDimension(ctx, store, rng, "api_key_hash", "api_key_mask", "", "", prices)
	if err != nil {
		return interfacesResponse{}, err
	}
	upstreams, err := queryDimension(ctx, store, rng, "upstream_key", "source", "", "", prices)
	if err != nil {
		return interfacesResponse{}, err
	}
	anonymizeDimensionStats(apiKeys, "key", false)
	maskProviderCredentialStats(upstreams, true)
	return interfacesResponse{Range: rng, APIKeys: apiKeys, Upstreams: upstreams, GeneratedAt: now.UTC().Format(time.RFC3339)}, nil
}

func queryUpstreamDetail(ctx context.Context, store *eventStore, key string, query url.Values, now time.Time) (upstreamDetailResponse, error) {
	if strings.TrimSpace(key) == "" || len(key) > 160 {
		return upstreamDetailResponse{}, errors.New("upstream key is required")
	}
	rng, err := parseRange(query, now)
	if err != nil {
		return upstreamDetailResponse{}, err
	}
	prices, err := loadPriceMap(ctx, store)
	if err != nil {
		return upstreamDetailResponse{}, err
	}
	models, err := queryDimension(ctx, store, rng, "model", "model", "upstream_key", key, prices)
	if err != nil {
		return upstreamDetailResponse{}, err
	}
	result := upstreamDetailResponse{Key: key, Range: rng, Models: models, GeneratedAt: now.UTC().Format(time.RFC3339)}
	for _, model := range models {
		mergeDimension(&result.Summary, model)
	}
	var storedName, provider string
	_ = store.db.QueryRowContext(ctx, `SELECT MAX(source), MAX(provider) FROM usage_events
		WHERE upstream_key = ? AND timestamp_ms BETWEEN ? AND ?`, key, rng.FromMS, rng.ToMS).Scan(&storedName, &provider)
	result.Name = maskedProviderCredentialDisplay(provider, storedName, key)
	result.Provider = provider
	result.Summary.Key, result.Summary.Name = key, result.Name
	result.Summary.SuccessRate = ratio(result.Summary.Successes, result.Summary.Requests)
	result.Summary.AvgLatencyMS = average(result.Summary.latencySum, result.Summary.Requests)
	page, err := queryEvents(ctx, store, eventFilter{FromMS: rng.FromMS, ToMS: rng.ToMS, Upstream: key, Page: 1, PageSize: 20})
	if err != nil {
		return upstreamDetailResponse{}, err
	}
	result.RecentEvents = page.Events
	return result, nil
}

type exactRange struct {
	FromMS int64
	ToMS   int64
}

func splitAggregateRange(rng timeRange) (int64, int64, []exactRange) {
	fromMinute := rng.FromMS / 60000
	toMinute := rng.ToMS / 60000
	fullFrom := fromMinute
	if rng.FromMS%60000 != 0 {
		fullFrom++
	}
	fullTo := toMinute
	if rng.ToMS%60000 != 59999 {
		fullTo--
	}
	if fullFrom > fullTo {
		return fullFrom, fullTo, []exactRange{{FromMS: rng.FromMS, ToMS: rng.ToMS}}
	}
	boundaries := make([]exactRange, 0, 2)
	if rng.FromMS < fullFrom*60000 {
		boundaries = append(boundaries, exactRange{FromMS: rng.FromMS, ToMS: fullFrom*60000 - 1})
	}
	afterFull := (fullTo + 1) * 60000
	if afterFull <= rng.ToMS {
		boundaries = append(boundaries, exactRange{FromMS: afterFull, ToMS: rng.ToMS})
	}
	return fullFrom, fullTo, boundaries
}

// Older databases can be opened with only the minute rollup table available
// (for example, while a migration is in progress).  In that case an exact
// edge query cannot be run, so callers may deliberately fall back to the
// complete rollup minute.  Normal databases always have usage_events.
func usageEventsTableAvailable(ctx context.Context, store *eventStore) (bool, error) {
	var name string
	err := store.db.QueryRowContext(ctx,
		"SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'usage_events'").Scan(&name)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, err
	default:
		return name == "usage_events", nil
	}
}

func isMissingUsageEventsTable(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "no such table") && strings.Contains(message, "usage_events")
}

func exactRangeWhere(column string, ranges []exactRange) (string, []any) {
	conditions := make([]string, 0, len(ranges))
	args := make([]any, 0, len(ranges)*2)
	for _, rng := range ranges {
		conditions = append(conditions, column+" BETWEEN ? AND ?")
		args = append(args, rng.FromMS, rng.ToMS)
	}
	return "(" + strings.Join(conditions, " OR ") + ")", args
}

func queryAggregateRows(ctx context.Context, store *eventStore, rng timeRange, interval int64, extraWhere string, extraArgs []any) ([]aggregateRow, error) {
	fullFrom, fullTo, boundaries := splitAggregateRange(rng)
	if rng.Label == "all" && len(boundaries) > 0 {
		available, err := usageEventsTableAvailable(ctx, store)
		if err != nil {
			return nil, err
		}
		if !available {
			// Rollup-only legacy stores have no millisecond detail for the
			// current edge minute. Treat that minute as complete, matching the
			// historical all-range behavior.
			fullTo = rng.ToMS / 60000
			boundaries = nil
		}
	}
	merged := make(map[aggregateKey]*aggregateRow)
	consume := func(rows *sql.Rows) error {
		defer rows.Close()
		for rows.Next() {
			var row aggregateRow
			if err := rows.Scan(&row.Bucket, &row.Model, &row.Requests, &row.Successes, &row.Failures,
				&row.Tokens.Input, &row.Tokens.Output, &row.Tokens.Reasoning, &row.Tokens.Cached,
				&row.Tokens.CacheRead, &row.Tokens.CacheWrite, &row.Tokens.Total,
				&row.LatencySumMS, &row.TTFTSumMS, &row.TTFTCount); err != nil {
				return err
			}
			addAggregateRow(merged, row)
		}
		return rows.Err()
	}
	if fullFrom <= fullTo {
		where := "minute BETWEEN ? AND ?"
		args := []any{timeBucketOffset(interval), interval, interval, interval, fullFrom, fullTo}
		if extraWhere != "" {
			where += " AND " + extraWhere
			args = append(args, extraArgs...)
		}
		rows, err := store.db.QueryContext(ctx, `SELECT minute - ((minute + ?) % ? + ?) % ? AS bucket, model,
			SUM(requests), SUM(successes), SUM(failures), SUM(input_tokens),
			SUM(output_tokens), SUM(reasoning_tokens), SUM(cached_tokens),
			SUM(cache_read_tokens), SUM(cache_creation_tokens), SUM(total_tokens),
			SUM(latency_sum_ms), SUM(ttft_sum_ms), SUM(ttft_count)
			FROM usage_minute_rollups WHERE `+where+` GROUP BY bucket, model`, args...)
		if err != nil {
			return nil, err
		}
		if err := consume(rows); err != nil {
			return nil, err
		}
	}
	if len(boundaries) > 0 {
		where, rangeArgs := exactRangeWhere("timestamp_ms", boundaries)
		args := []any{timeBucketOffset(interval), interval, interval, interval}
		args = append(args, rangeArgs...)
		if extraWhere != "" {
			where += " AND " + extraWhere
			args = append(args, extraArgs...)
		}
		rows, err := store.db.QueryContext(ctx, `SELECT timestamp_ms / 60000 - (((timestamp_ms / 60000 + ?) % ? + ?) % ?) AS bucket, model,
			COUNT(*), SUM(CASE WHEN failed = 0 THEN 1 ELSE 0 END),
			SUM(CASE WHEN failed != 0 THEN 1 ELSE 0 END), SUM(input_tokens),
			SUM(output_tokens), SUM(reasoning_tokens), SUM(cached_tokens),
			SUM(cache_read_tokens), SUM(cache_creation_tokens), SUM(total_tokens),
			SUM(latency_ms), SUM(ttft_ms), SUM(CASE WHEN ttft_ms > 0 THEN 1 ELSE 0 END)
			FROM usage_events WHERE `+where+` GROUP BY bucket, model`, args...)
		if err != nil {
			if !(rng.Label == "all" && isMissingUsageEventsTable(err)) {
				return nil, err
			}
		} else {
			if err := consume(rows); err != nil {
				return nil, err
			}
		}
	}
	result := make([]aggregateRow, 0, len(merged))
	for _, row := range merged {
		result = append(result, *row)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Bucket == result[j].Bucket {
			return result[i].Model < result[j].Model
		}
		return result[i].Bucket < result[j].Bucket
	})
	return result, nil
}

func queryDimension(ctx context.Context, store *eventStore, rng timeRange, keyColumn, labelColumn, filterColumn, filterValue string, prices map[string]modelPrice) ([]dimensionStat, error) {
	allowed := map[string]bool{"model": true, "provider": true, "source": true, "api_key_hash": true, "api_key_mask": true, "upstream_key": true, "upstream_label": true}
	if !allowed[keyColumn] || !allowed[labelColumn] || (filterColumn != "" && !allowed[filterColumn]) {
		return nil, errors.New("unsupported statistics dimension")
	}
	byKey := make(map[string]*dimensionStat)
	consume := func(rows *sql.Rows) error {
		defer rows.Close()
		for rows.Next() {
			var key, label, provider, model string
			var row dimensionStat
			if err := rows.Scan(&key, &label, &provider, &model, &row.Requests, &row.Successes, &row.Failures,
				&row.Tokens.Input, &row.Tokens.Output, &row.Tokens.Reasoning, &row.Tokens.Cached,
				&row.Tokens.CacheRead, &row.Tokens.CacheWrite, &row.Tokens.Total, &row.latencySum); err != nil {
				return err
			}
			groupKey := key
			if keyColumn == "source" {
				groupKey = provider + "\x00" + key
			}
			stat := byKey[groupKey]
			if stat == nil {
				stat = &dimensionStat{Key: key, Name: label, provider: provider, modelSet: make(map[string]struct{})}
				byKey[groupKey] = stat
			}
			stat.Requests += row.Requests
			stat.Successes += row.Successes
			stat.Failures += row.Failures
			stat.latencySum += row.latencySum
			addTokens(&stat.Tokens, row.Tokens)
			stat.TotalTokens = stat.Tokens.Total
			price := resolvePrice(model, prices)
			costs := calculateCostTotals(row.Tokens, price)
			actualCost := costs.total()
			standardCost := calculateStandardCost(row.Tokens, price)
			addCosts(&stat.Costs, costs)
			stat.CostUSD += actualCost
			if saved := standardCost - actualCost; saved > 0 {
				stat.SavedCostUSD += saved
			}
			stat.modelSet[model] = struct{}{}
		}
		return rows.Err()
	}
	fullFrom, fullTo, boundaries := splitAggregateRange(rng)
	if rng.Label == "all" && len(boundaries) > 0 {
		available, err := usageEventsTableAvailable(ctx, store)
		if err != nil {
			return nil, err
		}
		if !available {
			// See queryAggregateRows: without event detail the edge rollup
			// is the only available representation of the all-time range.
			fullTo = rng.ToMS / 60000
			boundaries = nil
		}
	}
	if fullFrom <= fullTo {
		where := "minute BETWEEN ? AND ?"
		args := []any{fullFrom, fullTo}
		if filterColumn != "" {
			where += " AND " + filterColumn + " = ?"
			args = append(args, filterValue)
		}
		statement := fmt.Sprintf(`SELECT %s, MAX(%s), provider, model, SUM(requests), SUM(successes),
			SUM(failures), SUM(input_tokens), SUM(output_tokens), SUM(reasoning_tokens),
			SUM(cached_tokens), SUM(cache_read_tokens), SUM(cache_creation_tokens),
			SUM(total_tokens), SUM(latency_sum_ms)
			FROM usage_minute_rollups WHERE %s GROUP BY %s, provider, model`, keyColumn, labelColumn, where, keyColumn)
		rows, err := store.db.QueryContext(ctx, statement, args...)
		if err != nil {
			return nil, err
		}
		if err := consume(rows); err != nil {
			return nil, err
		}
	}
	if len(boundaries) > 0 {
		where, args := exactRangeWhere("timestamp_ms", boundaries)
		if filterColumn != "" {
			where += " AND " + filterColumn + " = ?"
			args = append(args, filterValue)
		}
		statement := fmt.Sprintf(`SELECT %s, MAX(%s), provider, model, COUNT(*),
			SUM(CASE WHEN failed = 0 THEN 1 ELSE 0 END), SUM(CASE WHEN failed != 0 THEN 1 ELSE 0 END),
			SUM(input_tokens), SUM(output_tokens), SUM(reasoning_tokens), SUM(cached_tokens),
			SUM(cache_read_tokens), SUM(cache_creation_tokens), SUM(total_tokens), SUM(latency_ms)
			FROM usage_events WHERE %s GROUP BY %s, provider, model`, keyColumn, labelColumn, where, keyColumn)
		rows, err := store.db.QueryContext(ctx, statement, args...)
		if err != nil {
			if !(rng.Label == "all" && isMissingUsageEventsTable(err)) {
				return nil, err
			}
		} else {
			if err := consume(rows); err != nil {
				return nil, err
			}
		}
	}
	result := make([]dimensionStat, 0, len(byKey))
	for _, stat := range byKey {
		stat.SuccessRate = ratio(stat.Successes, stat.Requests)
		stat.AvgLatencyMS = average(stat.latencySum, stat.Requests)
		stat.Models = len(stat.modelSet)
		result = append(result, *stat)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Requests == result[j].Requests {
			return result[i].Name < result[j].Name
		}
		return result[i].Requests > result[j].Requests
	})
	return result, nil
}

const eventColumns = `id, timestamp_ms, provider, executor_type, model, alias, endpoint,
	api_key_mask, api_key_hash, auth_id, auth_index, auth_type, upstream_key,
	upstream_label, source, reasoning_effort, service_tier, generate, latency_ms,
	ttft_ms, failed, status_code, failure, input_tokens, output_tokens,
	reasoning_tokens, cached_tokens, cache_read_tokens, cache_creation_tokens, total_tokens`

func queryEventDays(ctx context.Context, store *eventStore) ([]string, error) {
	rows, err := store.db.QueryContext(ctx, `WITH RECURSIVE days(first_ms) AS (
		SELECT MIN(timestamp_ms) FROM usage_events
		UNION ALL
		SELECT (SELECT MIN(timestamp_ms) FROM usage_events
			WHERE timestamp_ms >= ((days.first_ms + 28800000) / 86400000 + 1) * 86400000 - 28800000)
		FROM days WHERE first_ms IS NOT NULL
	)
	SELECT date(first_ms / 1000, 'unixepoch', '+8 hours') FROM days WHERE first_ms IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	days := []string{}
	for rows.Next() {
		var day string
		if err := rows.Scan(&day); err != nil {
			return nil, err
		}
		days = append(days, day)
	}
	return days, rows.Err()
}

func queryEvents(ctx context.Context, store *eventStore, filter eventFilter) (eventsPage, error) {
	normalizeEventFilter(&filter)
	offset, err := eventPageOffset(filter)
	if err != nil {
		return eventsPage{}, err
	}
	priceMap, err := loadPriceMap(ctx, store)
	if err != nil {
		return eventsPage{}, err
	}
	where, args, err := eventWhere(ctx, store, filter)
	if err != nil {
		return eventsPage{}, err
	}
	var total int64
	if canCountEventsFromRollups(filter) {
		total, err = countEventsFromRollups(ctx, store, filter)
	} else {
		err = store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_events "+where, args...).Scan(&total)
	}
	if err != nil {
		return eventsPage{}, err
	}
	pages := 0
	if total > 0 {
		pages = int((total-1)/int64(filter.PageSize) + 1)
	}
	accessiblePages := minInt(pages, minInt(maxEventPage, maxEventOffset/filter.PageSize+1))
	result := eventsPage{Events: []usageEvent{}, Total: total, Page: filter.Page, PageSize: filter.PageSize, Pages: pages, AccessiblePages: accessiblePages, GeneratedAt: time.Now().UTC().Format(time.RFC3339)}
	if offset >= total {
		return result, nil
	}
	queryArgs := append(append([]any{}, args...), filter.PageSize, offset)
	rows, err := store.db.QueryContext(ctx, "SELECT "+eventColumns+" FROM usage_events "+where+" ORDER BY timestamp_ms DESC, id DESC LIMIT ? OFFSET ?", queryArgs...)
	if err != nil {
		return eventsPage{}, err
	}
	defer rows.Close()
	events := make([]usageEvent, 0, filter.PageSize)
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return eventsPage{}, err
		}
		redactEventForManagement(&event)
		price := resolvePrice(event.Model, priceMap)
		costs := calculateCostTotals(tokenTotals{
			Input: event.InputTokens, Output: event.OutputTokens,
			CacheRead: event.CacheReadTokens, CacheWrite: event.CacheCreationTokens,
			Reasoning: event.ReasoningTokens, Total: event.TotalTokens,
		}, price)
		cacheWriteRate := price.CacheWritePerMillion
		if cacheWriteRate == 0 {
			cacheWriteRate = price.InputPerMillion
		}
		reasoningRate := price.ReasoningPerMillion
		if reasoningRate == 0 {
			reasoningRate = price.OutputPerMillion
		}
		event.InputPrice, event.OutputPrice = price.InputPerMillion, price.OutputPerMillion
		event.CacheReadPrice, event.CacheWritePrice = price.CacheReadPerMillion, cacheWriteRate
		event.ReasoningPrice = reasoningRate
		event.InputCost, event.OutputCost = costs.Input, costs.Output
		event.CacheReadCost, event.CacheWriteCost = costs.CacheRead, costs.CacheWrite
		event.ReasoningCost, event.CostUSD = costs.Reasoning, costs.total()
		events = append(events, event)
	}
	result.Events = events
	return result, rows.Err()
}

func canCountEventsFromRollups(filter eventFilter) bool {
	return filter.FromMS >= 0 && filter.ToMS > 0 && filter.ToMS >= filter.FromMS && filter.Model == "" &&
		filter.Provider == "" && filter.APIKeyHash == "" && filter.Upstream == "" &&
		filter.Status == "" && filter.Search == ""
}

func countEventsFromRollups(ctx context.Context, store *eventStore, filter eventFilter) (int64, error) {
	fullFrom, fullTo, boundaries := splitAggregateRange(timeRange{FromMS: filter.FromMS, ToMS: filter.ToMS})
	parts := make([]string, 0, 1+len(boundaries))
	args := make([]any, 0, 2+len(boundaries)*2)
	if fullFrom <= fullTo {
		parts = append(parts, `SELECT COALESCE(SUM(requests), 0) AS request_count
			FROM usage_minute_rollups WHERE minute BETWEEN ? AND ?`)
		args = append(args, fullFrom, fullTo)
	}
	for _, boundary := range boundaries {
		parts = append(parts, `SELECT COUNT(*) AS request_count FROM usage_events
			WHERE timestamp_ms BETWEEN ? AND ?`)
		args = append(args, boundary.FromMS, boundary.ToMS)
	}
	statement := `SELECT COALESCE(SUM(request_count), 0) FROM (` + strings.Join(parts, " UNION ALL ") + `) AS event_counts`
	var total int64
	if err := store.db.QueryRowContext(ctx, statement, args...).Scan(&total); err != nil {
		return 0, err
	}
	return total, nil
}

const publicIdentifierSalt = "usage-keeper-public-label"

func anonymizeDimensionStats(stats []dimensionStat, prefix string, preserveKey bool) {
	for i := range stats {
		key := stats[i].Key
		label := publicIdentifier(prefix, key)
		stats[i].Name = label
		if !preserveKey {
			stats[i].Key = label
		}
	}
}

func maskProviderCredentialStats(stats []dimensionStat, preserveKey bool) {
	for i := range stats {
		key := stats[i].Key
		stats[i].Name = maskedProviderCredentialDisplay(stats[i].provider, stats[i].Name, key)
		if !preserveKey {
			stats[i].Key = publicIdentifier("source", key)
		}
	}
}

func mergeDimensionStatsByName(stats []dimensionStat, keyPrefix string) []dimensionStat {
	byName := make(map[string]*dimensionStat, len(stats))
	for _, stat := range stats {
		groupKey := stat.provider + "\x00" + stat.Name
		current := byName[groupKey]
		if current == nil {
			copyOfStat := stat
			copyOfStat.Key = publicIdentifier(keyPrefix, groupKey)
			copyOfStat.modelSet = make(map[string]struct{}, len(stat.modelSet))
			for model := range stat.modelSet {
				copyOfStat.modelSet[model] = struct{}{}
			}
			byName[groupKey] = &copyOfStat
			continue
		}
		current.Requests += stat.Requests
		current.Successes += stat.Successes
		current.Failures += stat.Failures
		current.CostUSD += stat.CostUSD
		current.SavedCostUSD += stat.SavedCostUSD
		addCosts(&current.Costs, stat.Costs)
		current.latencySum += stat.latencySum
		addTokens(&current.Tokens, stat.Tokens)
		for model := range stat.modelSet {
			current.modelSet[model] = struct{}{}
		}
	}

	merged := make([]dimensionStat, 0, len(byName))
	for _, stat := range byName {
		stat.TotalTokens = stat.Tokens.Total
		stat.SuccessRate = ratio(stat.Successes, stat.Requests)
		stat.AvgLatencyMS = average(stat.latencySum, stat.Requests)
		stat.Models = len(stat.modelSet)
		merged = append(merged, *stat)
	}
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].Requests == merged[j].Requests {
			return merged[i].Name < merged[j].Name
		}
		return merged[i].Requests > merged[j].Requests
	})
	return merged
}

func maskedProviderCredentialDisplay(provider, label, fallbackKey string) string {
	label = strings.TrimSpace(strings.ReplaceAll(label, " · ", " / "))
	parts := strings.Split(label, " / ")
	if len(parts) >= 2 {
		if strings.TrimSpace(provider) == "" {
			provider = parts[0]
		}
		credential := strings.TrimSpace(parts[len(parts)-1])
		if isMaskedProviderCredential(credential) {
			return cleanDimension(provider, "unknown") + " / " + credential
		}
		return providerCredentialLabel(provider, credential, fallbackKey)
	}
	return providerCredentialLabel(provider, label, fallbackKey)
}

func publicIdentifier(prefix, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return anonymousLabel(prefix, "")
	}
	return anonymousLabel(prefix, shortHMAC(value, publicIdentifierSalt))
}

func redactEventForManagement(event *usageEvent) {
	event.Endpoint = sanitizeEndpoint(event.Endpoint)
	event.APIKeyMask = publicIdentifier("key", event.APIKeyHash)
	event.APIKeyHash = ""
	event.AuthID = ""
	event.AuthIndex = ""
	event.AuthType = ""
	event.UpstreamLabel = maskedProviderCredentialDisplay(event.Provider, event.Source, event.UpstreamKey)
	event.UpstreamKey = ""
	event.Source = event.UpstreamLabel
	event.Failure = sanitizeFailure(event.Failure)
}

type rowScanner interface{ Scan(...any) error }

func scanEvent(scanner rowScanner) (usageEvent, error) {
	var event usageEvent
	var generate, failed int64
	err := scanner.Scan(&event.ID, &event.TimestampMS, &event.Provider, &event.ExecutorType,
		&event.Model, &event.Alias, &event.Endpoint, &event.APIKeyMask, &event.APIKeyHash, &event.AuthID,
		&event.AuthIndex, &event.AuthType, &event.UpstreamKey, &event.UpstreamLabel,
		&event.Source, &event.ReasoningEffort, &event.ServiceTier, &generate,
		&event.LatencyMS, &event.TTFTMS, &failed, &event.StatusCode, &event.Failure,
		&event.InputTokens, &event.OutputTokens, &event.ReasoningTokens,
		&event.CachedTokens, &event.CacheReadTokens, &event.CacheCreationTokens,
		&event.TotalTokens)
	event.Generate = generate != 0
	event.Failed = failed != 0
	return event, err
}

func normalizeEventFilter(filter *eventFilter) {
	if filter.Page < 1 {
		filter.Page = 1
	}
	if filter.PageSize < 1 {
		filter.PageSize = 25
	}
	if filter.PageSize > 200 {
		filter.PageSize = 200
	}
	if len(filter.Search) > 120 {
		filter.Search = filter.Search[:120]
	}
}

const (
	maxEventPage   = 40001
	maxEventOffset = 1_000_000
)

func eventPageOffset(filter eventFilter) (int64, error) {
	// Check before multiplying so this is safe on 32-bit hosts as well.
	if filter.Page < 1 || filter.PageSize < 1 || filter.Page > maxEventPage ||
		filter.Page-1 > maxEventOffset/filter.PageSize {
		return 0, fmt.Errorf("page must be between 1 and %d and offset must not exceed %d; narrow the time range", maxEventPage, maxEventOffset)
	}
	return int64(filter.Page-1) * int64(filter.PageSize), nil
}

func eventWhere(ctx context.Context, store *eventStore, filter eventFilter) (string, []any, error) {
	conditions := []string{"1=1"}
	args := make([]any, 0, 10)
	if filter.FromMS > 0 {
		conditions = append(conditions, "timestamp_ms >= ?")
		args = append(args, filter.FromMS)
	}
	if filter.ToMS > 0 {
		conditions = append(conditions, "timestamp_ms <= ?")
		args = append(args, filter.ToMS)
	}
	filters := []struct{ value, column string }{
		{filter.Model, "model"}, {filter.Provider, "provider"},
		{filter.Upstream, "upstream_key"},
	}
	for _, item := range filters {
		if item.value != "" {
			conditions = append(conditions, item.column+" = ?")
			args = append(args, item.value)
		}
	}
	if filter.APIKeyHash != "" {
		apiKeyHashes, err := resolveAPIKeyFilter(ctx, store, filter.APIKeyHash)
		if err != nil {
			return "", nil, err
		}
		if len(apiKeyHashes) == 0 {
			conditions = append(conditions, "0=1")
		} else {
			placeholders := make([]string, len(apiKeyHashes))
			for i, hash := range apiKeyHashes {
				placeholders[i] = "?"
				args = append(args, hash)
			}
			conditions = append(conditions, "api_key_hash IN ("+strings.Join(placeholders, ",")+")")
		}
	}
	if filter.Status == "success" {
		conditions = append(conditions, "failed = 0")
	} else if filter.Status == "failure" {
		conditions = append(conditions, "failed = 1")
	}
	if strings.TrimSpace(filter.Search) != "" {
		conditions = append(conditions, "(model LIKE ? OR provider LIKE ? OR endpoint LIKE ? OR upstream_label LIKE ? OR source LIKE ? OR failure LIKE ?)")
		needle := "%" + strings.TrimSpace(filter.Search) + "%"
		for i := 0; i < 6; i++ {
			args = append(args, needle)
		}
	}
	return "WHERE " + strings.Join(conditions, " AND "), args, nil
}

func resolveAPIKeyFilter(ctx context.Context, store *eventStore, value string) ([]string, error) {
	if !strings.HasPrefix(value, "key-") {
		return []string{value}, nil
	}
	rows, err := store.db.QueryContext(ctx, "SELECT DISTINCT api_key_hash FROM usage_events")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hashes []string
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			return nil, err
		}
		if publicIdentifier("key", hash) == value {
			hashes = append(hashes, hash)
		}
	}
	return hashes, rows.Err()
}

func addTokens(target *tokenTotals, value tokenTotals) {
	target.Input += value.Input
	target.Output += value.Output
	target.CacheRead += value.CacheRead
	target.CacheWrite += value.CacheWrite
	target.Reasoning += value.Reasoning
	target.Cached += value.Cached
	target.Total += value.Total
}

func addCosts(target *costTotals, value costTotals) {
	target.Input += value.Input
	target.Output += value.Output
	target.CacheRead += value.CacheRead
	target.CacheWrite += value.CacheWrite
	target.Reasoning += value.Reasoning
}

func mergeDimension(target *dimensionStat, value dimensionStat) {
	target.Requests += value.Requests
	target.Successes += value.Successes
	target.Failures += value.Failures
	target.CostUSD += value.CostUSD
	target.SavedCostUSD += value.SavedCostUSD
	addCosts(&target.Costs, value.Costs)
	target.latencySum += int64(value.AvgLatencyMS * float64(value.Requests))
	addTokens(&target.Tokens, value.Tokens)
	target.TotalTokens = target.Tokens.Total
}

func ratio(numerator, denominator int64) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func average(total, count int64) float64 {
	if count == 0 {
		return 0
	}
	return float64(total) / float64(count)
}

var _ = sql.ErrNoRows
