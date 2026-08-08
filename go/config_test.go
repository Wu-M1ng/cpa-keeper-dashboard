package main

import (
	"path/filepath"
	"testing"
)

func TestParseConfigAcceptsCPAPluginConfigFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "keeper.db")
	cfg, err := parseConfig([]byte("enabled: true\npriority: 10\nstorage_path: " + filepath.ToSlash(path) + "\nqueue_size: 4096\nretention_days: 90\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.QueueSize != 4096 || cfg.RetentionDays != 90 || cfg.StoragePath != path {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestDefaultQueueSizeIsBounded(t *testing.T) {
	if cfg := defaultConfig(); cfg.QueueSize != 2048 {
		t.Fatalf("default queue size = %d, want 2048", cfg.QueueSize)
	}
}

func TestParseConfigRejectsInvalidBounds(t *testing.T) {
	if _, err := parseConfig([]byte("queue_size: 8\n")); err == nil {
		t.Fatal("small queue should be rejected")
	}
	if _, err := parseConfig([]byte("storage_path: data/keeper.sqlite\n")); err == nil {
		t.Fatal("non-db storage path should be rejected")
	}
}
