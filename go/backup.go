package main

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type backupPayload struct {
	Version     int          `json:"version"`
	GeneratedAt string       `json:"generated_at"`
	Events      []usageEvent `json:"events"`
	Prices      []modelPrice `json:"prices"`
	Truncated   bool         `json:"truncated,omitempty"`
}

type importResult struct {
	Events int `json:"events"`
	Prices int `json:"prices"`
}

func exportBackup(ctx context.Context, store *eventStore, maxRecords int) (backupPayload, error) {
	if maxRecords < 1 {
		maxRecords = defaultExportMax
	}
	rows, err := store.db.QueryContext(ctx, "SELECT "+eventColumns+" FROM usage_events ORDER BY timestamp_ms, id LIMIT ?", maxRecords+1)
	if err != nil {
		return backupPayload{}, err
	}
	defer rows.Close()
	events := make([]usageEvent, 0, minInt(maxRecords, 1024))
	truncated := false
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return backupPayload{}, err
		}
		if len(events) == maxRecords {
			truncated = true
			break
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return backupPayload{}, err
	}
	prices, err := listPrices(ctx, store)
	if err != nil {
		return backupPayload{}, err
	}
	return backupPayload{Version: 1, GeneratedAt: time.Now().UTC().Format(time.RFC3339), Events: events, Prices: prices, Truncated: truncated}, nil
}

func importBackup(ctx context.Context, store *eventStore, payload backupPayload) (importResult, error) {
	if payload.Version != 1 {
		return importResult{}, errors.New("unsupported backup version")
	}
	if len(payload.Events) > 1_000_000 {
		return importResult{}, errors.New("backup contains too many events")
	}
	if err := validatePrices(payload.Prices); err != nil {
		return importResult{}, err
	}
	for i := range payload.Events {
		event := &payload.Events[i]
		if event.TimestampMS <= 0 || strings.TrimSpace(event.Model) == "" || strings.TrimSpace(event.Provider) == "" {
			return importResult{}, fmt.Errorf("event %d is invalid", i+1)
		}
		event.Failure = sanitizeFailure(event.Failure)
		event.Model = cleanDimension(event.Model, "unknown")
		event.Provider = cleanDimension(event.Provider, "unknown")
		event.Source = cleanDimension(event.Source, "unknown")
		if event.UpstreamKey == "" {
			event.UpstreamKey = shortHMAC(strings.Join([]string{event.Provider, event.AuthID, event.AuthIndex, event.AuthType}, "\x00"), "restored")
		}
		if event.UpstreamLabel == "" {
			event.UpstreamLabel = event.Provider + " / " + maskIdentifier(firstNonEmpty(event.AuthIndex, event.AuthID, "default"))
		}
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return importResult{}, err
	}
	defer tx.Rollback()
	for _, statement := range []string{"DELETE FROM usage_events", "DELETE FROM usage_minute_rollups", "DELETE FROM model_prices"} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return importResult{}, err
		}
	}
	if err := writeEventsTx(ctx, tx, payload.Events); err != nil {
		return importResult{}, err
	}
	if err := insertPricesTx(ctx, tx, payload.Prices); err != nil {
		return importResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return importResult{}, err
	}
	store.mu.Lock()
	store.last = time.Now().UTC()
	store.lastErr = ""
	store.mu.Unlock()
	return importResult{Events: len(payload.Events), Prices: len(payload.Prices)}, nil
}

func exportEventsCSV(ctx context.Context, store *eventStore, filter eventFilter, maxRecords int) ([]byte, error) {
	if maxRecords < 1 {
		maxRecords = defaultExportMax
	}
	filter.Page = 1
	filter.PageSize = maxRecords
	where, args := eventWhere(filter)
	args = append(args, maxRecords)
	rows, err := store.db.QueryContext(ctx, "SELECT "+eventColumns+" FROM usage_events "+where+" ORDER BY timestamp_ms DESC, id DESC LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var output strings.Builder
	writer := csv.NewWriter(&output)
	_ = writer.Write([]string{
		"time", "provider", "model", "api_key", "upstream", "source", "status",
		"status_code", "latency_ms", "ttft_ms", "input_tokens", "output_tokens",
		"cache_read_tokens", "cache_write_tokens", "reasoning_tokens", "total_tokens", "failure",
	})
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		status := "success"
		if event.Failed {
			status = "failure"
		}
		_ = writer.Write([]string{
			time.UnixMilli(event.TimestampMS).UTC().Format(time.RFC3339), event.Provider,
			event.Model, event.APIKeyMask, event.UpstreamLabel, event.Source, status,
			strconv.Itoa(event.StatusCode), strconv.FormatInt(event.LatencyMS, 10),
			strconv.FormatInt(event.TTFTMS, 10), strconv.FormatInt(event.InputTokens, 10),
			strconv.FormatInt(event.OutputTokens, 10), strconv.FormatInt(event.CacheReadTokens, 10),
			strconv.FormatInt(event.CacheCreationTokens, 10), strconv.FormatInt(event.ReasoningTokens, 10),
			strconv.FormatInt(event.TotalTokens, 10), event.Failure,
		})
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, err
	}
	return []byte("\xEF\xBB\xBF" + output.String()), rows.Err()
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
