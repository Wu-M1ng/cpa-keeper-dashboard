package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
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

var errInvalidBackup = errors.New("invalid backup")
var errBackupLimitExceeded = errors.New("backup exceeds export_max_records")
var errCSVLimitExceeded = errors.New("CSV export exceeds export_max_records")

func invalidBackup(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errInvalidBackup, fmt.Sprintf(format, args...))
}

func exportBackup(ctx context.Context, store *eventStore, maxRecords int) (backupPayload, error) {
	if maxRecords < 1 {
		maxRecords = defaultExportMax
	}
	events := make([]usageEvent, 0, minInt(maxRecords, 1024))
	prices, err := readBackupEvents(ctx, store, maxRecords, func(event usageEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		return backupPayload{}, err
	}
	return backupPayload{Version: 1, GeneratedAt: time.Now().UTC().Format(time.RFC3339), Events: events, Prices: prices}, nil
}

func exportBackupJSON(ctx context.Context, store *eventStore, maxRecords int) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	output.WriteString(`{"version":1,"generated_at":`)
	if err := encoder.Encode(time.Now().UTC().Format(time.RFC3339)); err != nil {
		return nil, err
	}
	output.WriteString(`,"events":[`)
	first := true
	prices, err := readBackupEvents(ctx, store, maxRecords, func(event usageEvent) error {
		if !first {
			output.WriteByte(',')
		}
		first = false
		return encoder.Encode(event)
	})
	if err != nil {
		return nil, err
	}
	output.WriteString(`],"prices":`)
	if err := encoder.Encode(prices); err != nil {
		return nil, err
	}
	output.WriteByte('}')
	return output.Bytes(), nil
}

func readBackupEvents(ctx context.Context, store *eventStore, maxRecords int, visit func(usageEvent) error) ([]modelPrice, error) {
	if maxRecords < 1 {
		maxRecords = defaultExportMax
	}
	var total int64
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_events").Scan(&total); err != nil {
		return nil, err
	}
	if total > int64(maxRecords) {
		return nil, errBackupLimitExceeded
	}
	// Load prices before opening the event cursor. SQLite reserves the cursor's
	// connection until it is closed, so a cold price cache could otherwise
	// deadlock a single-connection pool.
	prices, err := listPrices(ctx, store)
	if err != nil {
		return nil, err
	}
	rows, err := store.db.QueryContext(ctx, "SELECT "+eventColumns+" FROM usage_events ORDER BY timestamp_ms, id LIMIT ?", maxRecords+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		if count == maxRecords {
			return nil, errBackupLimitExceeded
		}
		event, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		normalizeEventForStorage(&event, store.hashSalt)
		if err := visit(event); err != nil {
			return nil, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return prices, nil
}

func importBackup(ctx context.Context, store *eventStore, payload backupPayload) (importResult, error) {
	if payload.Version != 1 {
		return importResult{}, invalidBackup("unsupported backup version")
	}
	if payload.Truncated {
		return importResult{}, invalidBackup("truncated backup cannot be restored")
	}
	if len(payload.Events) > 1_000_000 {
		return importResult{}, invalidBackup("backup contains too many events")
	}
	for i := range payload.Events {
		event := &payload.Events[i]
		if event.TimestampMS <= 0 || strings.TrimSpace(event.Model) == "" || strings.TrimSpace(event.Provider) == "" {
			return importResult{}, invalidBackup("event %d is invalid", i+1)
		}
		measurements := []int64{
			event.LatencyMS, event.TTFTMS, event.InputTokens, event.OutputTokens,
			event.ReasoningTokens, event.CachedTokens, event.CacheReadTokens,
			event.CacheCreationTokens, event.TotalTokens,
		}
		for _, measurement := range measurements {
			if measurement < 0 {
				return importResult{}, invalidBackup("event %d contains a negative measurement", i+1)
			}
		}
		normalizeEventForStorage(event, store.hashSalt)
	}
	// A nil slice means the JSON field was omitted (or explicitly null). Empty
	// arrays are valid and intentionally clear that collection on restore.
	if payload.Events == nil || payload.Prices == nil {
		return importResult{}, invalidBackup("backup events and prices are required")
	}
	if err := validatePrices(payload.Prices); err != nil {
		return importResult{}, fmt.Errorf("%w: %v", errInvalidBackup, err)
	}
	store.priceMu.Lock()
	defer store.priceMu.Unlock()
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
	store.priceLoaded = false
	store.priceList = nil
	store.priceMap = nil
	store.invalidateStorageMetrics()
	return importResult{Events: len(payload.Events), Prices: len(payload.Prices)}, nil
}

func exportEventsCSV(ctx context.Context, store *eventStore, filter eventFilter, maxRecords int) ([]byte, error) {
	if maxRecords < 1 {
		maxRecords = defaultExportMax
	}
	filter.Page = 1
	filter.PageSize = maxRecords
	where, args, err := eventWhere(ctx, store, filter)
	if err != nil {
		return nil, err
	}
	args = append(args, maxRecords+1)
	rows, err := store.db.QueryContext(ctx, "SELECT "+eventColumns+" FROM usage_events "+where+" ORDER BY timestamp_ms DESC, id DESC LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var output bytes.Buffer
	output.WriteString("\xEF\xBB\xBF")
	writer := csv.NewWriter(&output)
	_ = writer.Write([]string{
		"time", "provider", "model", "endpoint", "api_key", "upstream", "source", "status",
		"status_code", "latency_ms", "ttft_ms", "input_tokens", "output_tokens",
		"cache_read_tokens", "cache_write_tokens", "reasoning_tokens", "total_tokens", "failure",
	})
	exported := 0
	for rows.Next() {
		if exported == maxRecords {
			return nil, errCSVLimitExceeded
		}
		event, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		redactEventForManagement(&event)
		status := "success"
		if event.Failed {
			status = "failure"
		}
		record := []string{
			time.UnixMilli(event.TimestampMS).UTC().Format(time.RFC3339), event.Provider,
			event.Model, event.Endpoint, event.APIKeyMask, event.UpstreamLabel, event.Source, status,
			strconv.Itoa(event.StatusCode), strconv.FormatInt(event.LatencyMS, 10),
			strconv.FormatInt(event.TTFTMS, 10), strconv.FormatInt(event.InputTokens, 10),
			strconv.FormatInt(event.OutputTokens, 10), strconv.FormatInt(event.CacheReadTokens, 10),
			strconv.FormatInt(event.CacheCreationTokens, 10), strconv.FormatInt(event.ReasoningTokens, 10),
			strconv.FormatInt(event.TotalTokens, 10), event.Failure,
		}
		for _, column := range []int{1, 2, 3, 4, 5, 6, 17} {
			record[column] = csvTextCell(record[column])
		}
		if err := writer.Write(record); err != nil {
			return nil, err
		}
		exported++
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, err
	}
	return output.Bytes(), rows.Err()
}

// CSV quoting only protects delimiters. A leading apostrophe makes untrusted
// formula-like text explicit text when opened by spreadsheet applications.
func csvTextCell(value string) string {
	trimmed := strings.TrimLeftFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
	})
	if trimmed != "" && strings.ContainsRune("=+-@", rune(trimmed[0])) {
		return "'" + value
	}
	return value
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
