package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type eventStore struct {
	db       *sql.DB
	enabled  bool
	path     string
	hashSalt string
	mu       sync.RWMutex
	last     time.Time
	lastErr  string
}

func openEventStore(cfg runtimeConfig) (*eventStore, error) {
	path := cfg.StoragePath
	dsn := "file:usage-keeper-memory?mode=memory&cache=shared"
	if cfg.StorageEnabled {
		if err := ensureStorageDirectory(path); err != nil {
			return nil, fmt.Errorf("create storage directory: %w", err)
		}
		dsn = "file:" + filepath.ToSlash(path)
	}
	dsn = withConnectionPragmas(dsn)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	store := &eventStore{db: db, enabled: cfg.StorageEnabled, path: path, hashSalt: cfg.APIKeyHashSalt}
	if err := store.initialize(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func withConnectionPragmas(dsn string) string {
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	query := url.Values{}
	query.Add("_pragma", "busy_timeout(2000)")
	query.Add("_pragma", "synchronous(NORMAL)")
	query.Add("_pragma", "temp_store(MEMORY)")
	return dsn + separator + query.Encode()
}

func (s *eventStore) initialize() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, statement := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA busy_timeout=2000",
		"PRAGMA temp_store=MEMORY",
		`CREATE TABLE IF NOT EXISTS usage_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp_ms INTEGER NOT NULL,
			provider TEXT NOT NULL,
			executor_type TEXT NOT NULL,
			model TEXT NOT NULL,
			alias TEXT NOT NULL,
			endpoint TEXT NOT NULL DEFAULT '',
			api_key_mask TEXT NOT NULL,
			api_key_hash TEXT NOT NULL,
			auth_id TEXT NOT NULL,
			auth_index TEXT NOT NULL,
			auth_type TEXT NOT NULL,
			upstream_key TEXT NOT NULL,
			upstream_label TEXT NOT NULL,
			source TEXT NOT NULL,
			reasoning_effort TEXT NOT NULL,
			service_tier TEXT NOT NULL,
			generate INTEGER NOT NULL,
			latency_ms INTEGER NOT NULL,
			ttft_ms INTEGER NOT NULL,
			failed INTEGER NOT NULL,
			status_code INTEGER NOT NULL,
			failure TEXT NOT NULL,
			input_tokens INTEGER NOT NULL,
			output_tokens INTEGER NOT NULL,
			reasoning_tokens INTEGER NOT NULL,
			cached_tokens INTEGER NOT NULL,
			cache_read_tokens INTEGER NOT NULL,
			cache_creation_tokens INTEGER NOT NULL,
			total_tokens INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_events_timestamp ON usage_events(timestamp_ms DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_events_model_time ON usage_events(model, timestamp_ms DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_events_provider_time ON usage_events(provider, timestamp_ms DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_events_api_time ON usage_events(api_key_hash, timestamp_ms DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_events_upstream_time ON usage_events(upstream_key, timestamp_ms DESC)`,
		`CREATE TABLE IF NOT EXISTS usage_minute_rollups (
			minute INTEGER NOT NULL,
			provider TEXT NOT NULL,
			model TEXT NOT NULL,
			source TEXT NOT NULL,
			api_key_hash TEXT NOT NULL,
			api_key_mask TEXT NOT NULL,
			upstream_key TEXT NOT NULL,
			upstream_label TEXT NOT NULL,
			requests INTEGER NOT NULL,
			successes INTEGER NOT NULL,
			failures INTEGER NOT NULL,
			input_tokens INTEGER NOT NULL,
			output_tokens INTEGER NOT NULL,
			reasoning_tokens INTEGER NOT NULL,
			cached_tokens INTEGER NOT NULL,
			cache_read_tokens INTEGER NOT NULL,
			cache_creation_tokens INTEGER NOT NULL,
			total_tokens INTEGER NOT NULL,
			latency_sum_ms INTEGER NOT NULL,
			ttft_sum_ms INTEGER NOT NULL,
			ttft_count INTEGER NOT NULL,
			PRIMARY KEY(minute, provider, model, source, api_key_hash, upstream_key)
		) WITHOUT ROWID`,
		`CREATE INDEX IF NOT EXISTS idx_rollups_model_time ON usage_minute_rollups(model, minute)`,
		`CREATE INDEX IF NOT EXISTS idx_rollups_provider_time ON usage_minute_rollups(provider, minute)`,
		`CREATE INDEX IF NOT EXISTS idx_rollups_upstream_time ON usage_minute_rollups(upstream_key, minute)`,
		`CREATE TABLE IF NOT EXISTS model_prices (
			model TEXT PRIMARY KEY,
			input_per_million REAL NOT NULL DEFAULT 0,
			output_per_million REAL NOT NULL DEFAULT 0,
			cache_read_per_million REAL NOT NULL DEFAULT 0,
			cache_write_per_million REAL NOT NULL DEFAULT 0,
			reasoning_per_million REAL NOT NULL DEFAULT 0,
			updated_at_ms INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS plugin_settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at_ms INTEGER NOT NULL
		)`,
	} {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize sqlite: %w", err)
		}
	}
	if err := s.ensureEndpointColumn(ctx); err != nil {
		return fmt.Errorf("migrate sqlite: %w", err)
	}
	return nil
}

func (s *eventStore) ensureEndpointColumn(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, "PRAGMA table_info(usage_events)")
	if err != nil {
		return err
	}
	hasEndpoint := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		hasEndpoint = hasEndpoint || name == "endpoint"
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if hasEndpoint {
		return nil
	}
	_, err = s.db.ExecContext(ctx, "ALTER TABLE usage_events ADD COLUMN endpoint TEXT NOT NULL DEFAULT ''")
	return err
}

const insertEventSQL = `INSERT INTO usage_events (
	timestamp_ms, provider, executor_type, model, alias, endpoint, api_key_mask, api_key_hash,
	auth_id, auth_index, auth_type, upstream_key, upstream_label, source,
	reasoning_effort, service_tier, generate, latency_ms, ttft_ms, failed,
	status_code, failure, input_tokens, output_tokens, reasoning_tokens,
	cached_tokens, cache_read_tokens, cache_creation_tokens, total_tokens
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

const upsertRollupSQL = `INSERT INTO usage_minute_rollups (
	minute, provider, model, source, api_key_hash, api_key_mask, upstream_key,
	upstream_label, requests, successes, failures, input_tokens, output_tokens,
	reasoning_tokens, cached_tokens, cache_read_tokens, cache_creation_tokens,
	total_tokens, latency_sum_ms, ttft_sum_ms, ttft_count
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(minute, provider, model, source, api_key_hash, upstream_key) DO UPDATE SET
	requests = requests + 1,
	successes = successes + excluded.successes,
	failures = failures + excluded.failures,
	input_tokens = input_tokens + excluded.input_tokens,
	output_tokens = output_tokens + excluded.output_tokens,
	reasoning_tokens = reasoning_tokens + excluded.reasoning_tokens,
	cached_tokens = cached_tokens + excluded.cached_tokens,
	cache_read_tokens = cache_read_tokens + excluded.cache_read_tokens,
	cache_creation_tokens = cache_creation_tokens + excluded.cache_creation_tokens,
	total_tokens = total_tokens + excluded.total_tokens,
	latency_sum_ms = latency_sum_ms + excluded.latency_sum_ms,
	ttft_sum_ms = ttft_sum_ms + excluded.ttft_sum_ms,
	ttft_count = ttft_count + excluded.ttft_count,
	api_key_mask = excluded.api_key_mask,
	upstream_label = excluded.upstream_label`

func (s *eventStore) writeBatch(events []usageEvent) error {
	if len(events) == 0 {
		return nil
	}
	for i := range events {
		normalizeEventForStorage(&events[i], s.hashSalt)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.setError(err)
		return err
	}
	rollback := true
	defer func() {
		if rollback {
			_ = tx.Rollback()
		}
	}()
	if err := writeEventsTx(ctx, tx, events); err != nil {
		s.setError(err)
		return err
	}
	if err := tx.Commit(); err != nil {
		s.setError(err)
		return err
	}
	rollback = false
	s.mu.Lock()
	s.last = time.Now().UTC()
	s.lastErr = ""
	s.mu.Unlock()
	return nil
}

func writeEventsTx(ctx context.Context, tx *sql.Tx, events []usageEvent) error {
	eventStmt, err := tx.PrepareContext(ctx, insertEventSQL)
	if err != nil {
		return err
	}
	defer eventStmt.Close()
	rollupStmt, err := tx.PrepareContext(ctx, upsertRollupSQL)
	if err != nil {
		return err
	}
	defer rollupStmt.Close()

	for _, event := range events {
		if _, err := eventStmt.ExecContext(ctx,
			event.TimestampMS, event.Provider, event.ExecutorType, event.Model, event.Alias,
			event.Endpoint, event.APIKeyMask, event.APIKeyHash, event.AuthID, event.AuthIndex, event.AuthType,
			event.UpstreamKey, event.UpstreamLabel, event.Source, event.ReasoningEffort,
			event.ServiceTier, boolInt(event.Generate), event.LatencyMS, event.TTFTMS,
			boolInt(event.Failed), event.StatusCode, event.Failure, event.InputTokens,
			event.OutputTokens, event.ReasoningTokens, event.CachedTokens,
			event.CacheReadTokens, event.CacheCreationTokens, event.TotalTokens,
		); err != nil {
			return err
		}
		success, failure := int64(1), int64(0)
		if event.Failed {
			success, failure = 0, 1
		}
		ttftCount := int64(0)
		if event.TTFTMS > 0 {
			ttftCount = 1
		}
		if _, err := rollupStmt.ExecContext(ctx,
			event.TimestampMS/60000, event.Provider, event.Model, event.Source,
			event.APIKeyHash, event.APIKeyMask, event.UpstreamKey, event.UpstreamLabel,
			success, failure, event.InputTokens, event.OutputTokens, event.ReasoningTokens,
			event.CachedTokens, event.CacheReadTokens, event.CacheCreationTokens,
			event.TotalTokens, event.LatencyMS, event.TTFTMS, ttftCount,
		); err != nil {
			return err
		}
	}
	return nil
}

func (s *eventStore) prune(retentionDays int, now time.Time) error {
	if retentionDays <= 0 {
		return nil
	}
	cutoffMS := now.AddDate(0, 0, -retentionDays).UnixMilli()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM usage_events WHERE timestamp_ms < ?", cutoffMS); err == nil {
		_, err = tx.ExecContext(ctx, "DELETE FROM usage_minute_rollups WHERE minute < ?", cutoffMS/60000)
	}
	if err != nil {
		_ = tx.Rollback()
		s.setError(err)
		return err
	}
	return tx.Commit()
}

func (s *eventStore) status() storageStatus {
	status := storageStatus{Enabled: s.enabled, Path: s.path, JournalMode: "wal"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&status.JournalMode)
	_ = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_events").Scan(&status.EventCount)
	_ = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_minute_rollups").Scan(&status.RollupCount)
	if s.enabled {
		if info, err := os.Stat(s.path); err == nil {
			status.DatabaseBytes = info.Size()
		}
	}
	s.mu.RLock()
	if !s.last.IsZero() {
		status.LastWriteAt = s.last.Format(time.RFC3339)
	}
	status.LastError = s.lastErr
	s.mu.RUnlock()
	return status
}

func (s *eventStore) loadIntSetting(key string, fallback int) int {
	var raw string
	if err := s.db.QueryRow("SELECT value FROM plugin_settings WHERE key = ?", key).Scan(&raw); err != nil {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return fallback
	}
	return value
}

func (s *eventStore) setError(err error) {
	if err == nil {
		return
	}
	message := err.Error()
	if strings.Contains(strings.ToLower(message), s.path) {
		message = strings.ReplaceAll(message, s.path, filepath.Base(s.path))
	}
	s.mu.Lock()
	s.lastErr = message
	s.mu.Unlock()
}

func (s *eventStore) close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func boolInt(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

var errRuntimeUnavailable = errors.New("usage runtime is not available")
