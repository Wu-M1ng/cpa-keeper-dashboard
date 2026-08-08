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
	AvgLatencyMS float64     `json:"avg_latency_ms"`
	Tokens       tokenTotals `json:"tokens"`
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
	Events      []usageEvent `json:"events"`
	Total       int64        `json:"total"`
	Page        int          `json:"page"`
	PageSize    int          `json:"page_size"`
	Pages       int          `json:"pages"`
	GeneratedAt string       `json:"generated_at"`
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

func querySummary(ctx context.Context, store *eventStore, query url.Values, now time.Time) (summaryResponse, error) {
	rng, err := parseRange(query, now)
	if err != nil {
		return summaryResponse{}, err
	}
	prices, err := loadPriceMap(ctx, store)
	if err != nil {
		return summaryResponse{}, err
	}
	rows, err := queryAggregateRows(ctx, store, rng, rng.IntervalMinutes, "", nil)
	if err != nil {
		return summaryResponse{}, err
	}
	result := summaryResponse{Range: rng, Trend: []trendPoint{}, Health: []healthPoint{}, GeneratedAt: now.UTC().Format(time.RFC3339)}
	result.KPI.RangeLabel = rng.Label
	if runtime := currentRuntime(); runtime != nil && runtime.store == store {
		result.Runtime = runtime.status()
	} else {
		result.Runtime.Storage = store.status()
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

	const healthInterval int64 = 15
	healthStart := now.Truncate(24 * time.Hour).Add(-4 * 24 * time.Hour)
	healthRange := timeRange{
		FromMS:          healthStart.UnixMilli(),
		ToMS:            healthStart.Add(5 * 24 * time.Hour).Add(-time.Millisecond).UnixMilli(),
		Label:           "5d",
		IntervalMinutes: healthInterval,
	}
	healthRows, err := queryAggregateRows(ctx, store, healthRange, healthInterval, "", nil)
	if err != nil {
		return summaryResponse{}, err
	}
	healthByBucket := make(map[int64]*healthPoint)
	for _, row := range healthRows {
		point := healthByBucket[row.Bucket]
		if point == nil {
			point = &healthPoint{TimestampMS: row.Bucket * 60000}
			healthByBucket[row.Bucket] = point
		}
		point.Requests += row.Requests
		point.Failures += row.Failures
	}
	appendDenseHealth(&result.Health, healthByBucket, healthRange, healthInterval)
	sort.Slice(result.Health, func(i, j int) bool { return result.Health[i].TimestampMS < result.Health[j].TimestampMS })
	return result, nil
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

func appendDenseTrend(target *[]trendPoint, buckets map[int64]*trendPoint, rng timeRange, interval int64) {
	if len(buckets) == 0 {
		return
	}
	start, end := rng.FromMS/60000, rng.ToMS/60000
	start = (start / interval) * interval
	end = (end / interval) * interval
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
	start, end := rng.FromMS/60000, rng.ToMS/60000
	start = (start / interval) * interval
	end = (end / interval) * interval
	for bucket := start; bucket <= end; bucket += interval {
		point := buckets[bucket]
		if point == nil {
			point = &healthPoint{TimestampMS: bucket * 60000}
		}
		point.SuccessRate = ratio(point.Requests-point.Failures, point.Requests)
		*target = append(*target, *point)
	}
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
	dimensions := []struct{ response, key, label string }{
		{"models", "model", "model"},
		{"providers", "provider", "provider"},
		{"api_keys", "api_key_hash", "api_key_mask"},
		{"sources", "source", "source"},
	}
	for _, dimension := range dimensions {
		stats, err := queryDimension(ctx, store, rng, dimension.key, dimension.label, "", "", prices)
		if err != nil {
			return analysisResponse{}, err
		}
		switch dimension.response {
		case "api_keys":
			anonymizeDimensionStats(stats, "key", false)
		case "sources":
			maskProviderCredentialStats(stats, false)
			stats = mergeDimensionStatsByName(stats, "source")
		}
		result.Distributions[dimension.response] = stats
		if dimension.response == "models" {
			result.Models = stats
			for _, stat := range stats {
				addTokens(&result.Tokens, stat.Tokens)
			}
		}
	}
	return result, nil
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
	_ = store.db.QueryRowContext(ctx, `SELECT MAX(source), MAX(provider) FROM usage_minute_rollups
		WHERE upstream_key = ? AND minute BETWEEN ? AND ?`, key, rng.FromMS/60000, rng.ToMS/60000).Scan(&storedName, &provider)
	result.Name = maskedProviderCredentialDisplay(provider, storedName, key)
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

func queryAggregateRows(ctx context.Context, store *eventStore, rng timeRange, interval int64, extraWhere string, extraArgs []any) ([]aggregateRow, error) {
	where := "minute BETWEEN ? AND ?"
	args := []any{interval, interval, rng.FromMS / 60000, rng.ToMS / 60000}
	if extraWhere != "" {
		where += " AND " + extraWhere
		args = append(args, extraArgs...)
	}
	query := `SELECT (minute / ?) * ? AS bucket, model,
		SUM(requests), SUM(successes), SUM(failures), SUM(input_tokens),
		SUM(output_tokens), SUM(reasoning_tokens), SUM(cached_tokens),
		SUM(cache_read_tokens), SUM(cache_creation_tokens), SUM(total_tokens),
		SUM(latency_sum_ms), SUM(ttft_sum_ms), SUM(ttft_count)
		FROM usage_minute_rollups WHERE ` + where + ` GROUP BY bucket, model ORDER BY bucket`
	rows, err := store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]aggregateRow, 0)
	for rows.Next() {
		var row aggregateRow
		if err := rows.Scan(&row.Bucket, &row.Model, &row.Requests, &row.Successes, &row.Failures,
			&row.Tokens.Input, &row.Tokens.Output, &row.Tokens.Reasoning, &row.Tokens.Cached,
			&row.Tokens.CacheRead, &row.Tokens.CacheWrite, &row.Tokens.Total,
			&row.LatencySumMS, &row.TTFTSumMS, &row.TTFTCount); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func queryDimension(ctx context.Context, store *eventStore, rng timeRange, keyColumn, labelColumn, filterColumn, filterValue string, prices map[string]modelPrice) ([]dimensionStat, error) {
	allowed := map[string]bool{"model": true, "provider": true, "source": true, "api_key_hash": true, "api_key_mask": true, "upstream_key": true, "upstream_label": true}
	if !allowed[keyColumn] || !allowed[labelColumn] || (filterColumn != "" && !allowed[filterColumn]) {
		return nil, errors.New("unsupported statistics dimension")
	}
	where := "minute BETWEEN ? AND ?"
	args := []any{rng.FromMS / 60000, rng.ToMS / 60000}
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
	defer rows.Close()
	byKey := make(map[string]*dimensionStat)
	for rows.Next() {
		var key, label, provider, model string
		var row dimensionStat
		if err := rows.Scan(&key, &label, &provider, &model, &row.Requests, &row.Successes, &row.Failures,
			&row.Tokens.Input, &row.Tokens.Output, &row.Tokens.Reasoning, &row.Tokens.Cached,
			&row.Tokens.CacheRead, &row.Tokens.CacheWrite, &row.Tokens.Total, &row.latencySum); err != nil {
			return nil, err
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
		stat.CostUSD += calculateCost(row.Tokens, resolvePrice(model, prices))
		stat.modelSet[model] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
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

func queryEvents(ctx context.Context, store *eventStore, filter eventFilter) (eventsPage, error) {
	normalizeEventFilter(&filter)
	where, args := eventWhere(filter)
	var total int64
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_events "+where, args...).Scan(&total); err != nil {
		return eventsPage{}, err
	}
	queryArgs := append(append([]any{}, args...), filter.PageSize, (filter.Page-1)*filter.PageSize)
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
		events = append(events, event)
	}
	pages := 0
	if total > 0 {
		pages = int((total + int64(filter.PageSize) - 1) / int64(filter.PageSize))
	}
	return eventsPage{Events: events, Total: total, Page: filter.Page, PageSize: filter.PageSize, Pages: pages, GeneratedAt: time.Now().UTC().Format(time.RFC3339)}, rows.Err()
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
		if strings.Contains(credential, "***") {
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

func eventWhere(filter eventFilter) (string, []any) {
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
		{filter.APIKeyHash, "api_key_hash"}, {filter.Upstream, "upstream_key"},
	}
	for _, item := range filters {
		if item.value != "" {
			conditions = append(conditions, item.column+" = ?")
			args = append(args, item.value)
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
	return "WHERE " + strings.Join(conditions, " AND "), args
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

func mergeDimension(target *dimensionStat, value dimensionStat) {
	target.Requests += value.Requests
	target.Successes += value.Successes
	target.Failures += value.Failures
	target.CostUSD += value.CostUSD
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
