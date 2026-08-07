package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	defaultQueueSize       = 256
	defaultBatchSize       = 64
	defaultFlushIntervalMS = 250
	defaultRetentionDays   = 30
	defaultExportMax       = 50000
)

type runtimeConfig struct {
	StorageEnabled  bool   `yaml:"storage_enabled" json:"storage_enabled"`
	StoragePath     string `yaml:"storage_path" json:"storage_path"`
	QueueSize       int    `yaml:"queue_size" json:"queue_size"`
	BatchSize       int    `yaml:"batch_size" json:"batch_size"`
	FlushIntervalMS int    `yaml:"flush_interval_ms" json:"flush_interval_ms"`
	RetentionDays   int    `yaml:"retention_days" json:"retention_days"`
	ExportMax       int    `yaml:"export_max_records" json:"export_max_records"`
	APIKeyHashSalt  string `yaml:"api_key_hash_salt" json:"-"`
}

func defaultConfig() runtimeConfig {
	return runtimeConfig{
		StorageEnabled:  true,
		StoragePath:     filepath.Join("data", "usage-keeper.db"),
		QueueSize:       defaultQueueSize,
		BatchSize:       defaultBatchSize,
		FlushIntervalMS: defaultFlushIntervalMS,
		RetentionDays:   defaultRetentionDays,
		ExportMax:       defaultExportMax,
		APIKeyHashSalt:  "cpa-usage-keeper",
	}
}

func parseConfig(raw []byte) (runtimeConfig, error) {
	cfg := defaultConfig()
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return runtimeConfig{}, fmt.Errorf("invalid plugin configuration: %w", err)
		}
	}
	if cfg.QueueSize < 64 || cfg.QueueSize > 65536 {
		return runtimeConfig{}, errors.New("queue_size must be between 64 and 65536")
	}
	if cfg.BatchSize < 1 || cfg.BatchSize > 1024 {
		return runtimeConfig{}, errors.New("batch_size must be between 1 and 1024")
	}
	if cfg.BatchSize > cfg.QueueSize {
		return runtimeConfig{}, errors.New("batch_size must not exceed queue_size")
	}
	if cfg.FlushIntervalMS < 25 || cfg.FlushIntervalMS > 10000 {
		return runtimeConfig{}, errors.New("flush_interval_ms must be between 25 and 10000")
	}
	if cfg.RetentionDays < 1 || cfg.RetentionDays > 3650 {
		return runtimeConfig{}, errors.New("retention_days must be between 1 and 3650")
	}
	if cfg.ExportMax < 100 || cfg.ExportMax > 1000000 {
		return runtimeConfig{}, errors.New("export_max_records must be between 100 and 1000000")
	}
	if strings.TrimSpace(cfg.APIKeyHashSalt) == "" {
		cfg.APIKeyHashSalt = defaultConfig().APIKeyHashSalt
	}
	if cfg.StorageEnabled {
		if strings.TrimSpace(cfg.StoragePath) == "" {
			return runtimeConfig{}, errors.New("storage_path is required when storage is enabled")
		}
		if !strings.EqualFold(filepath.Ext(cfg.StoragePath), ".db") {
			return runtimeConfig{}, errors.New("storage_path must reference a .db file")
		}
		absolute, err := filepath.Abs(filepath.Clean(cfg.StoragePath))
		if err != nil {
			return runtimeConfig{}, fmt.Errorf("resolve storage_path: %w", err)
		}
		cfg.StoragePath = absolute
	}
	return cfg, nil
}

func (c runtimeConfig) flushInterval() time.Duration {
	return time.Duration(c.FlushIntervalMS) * time.Millisecond
}

func ensureStorageDirectory(path string) error {
	return os.MkdirAll(filepath.Dir(path), 0o750)
}
