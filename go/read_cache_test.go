package main

import (
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestManagementReadCacheIsBoundedAndExpires(t *testing.T) {
	cache := newManagementReadCache()
	now := time.Unix(1_700_000_000, 0)
	cache.set("copy", []byte(`{"value":1}`), now)
	first, ok := cache.get("copy", now)
	if !ok {
		t.Fatal("fresh cache entry missed")
	}
	first[0] = 'x'
	second, ok := cache.get("copy", now)
	if !ok || string(second) != `{"value":1}` {
		t.Fatalf("cached body was mutated: %q", second)
	}
	if _, ok := cache.get("copy", now.Add(managementReadCacheTTL)); ok {
		t.Fatal("expired cache entry was returned")
	}
	for index := 0; index < managementReadCacheItems+8; index++ {
		cache.set(fmt.Sprintf("key-%d", index), []byte(`{"ok":true}`), now)
	}
	cache.mu.Lock()
	entries, bytes := len(cache.entries), cache.bytes
	cache.mu.Unlock()
	if entries > managementReadCacheItems || bytes > managementReadCacheBytes {
		t.Fatalf("cache exceeded bounds: entries=%d bytes=%d", entries, bytes)
	}
}

func TestCachedReadCoalescesConcurrentMisses(t *testing.T) {
	runtime := &pluginRuntime{readCache: newManagementReadCache()}
	var loads atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	loader := func() managementResponse {
		loads.Add(1)
		once.Do(func() { close(started) })
		<-release
		return jsonResponse(http.StatusOK, map[string]bool{"ok": true})
	}
	const readers = 8
	var wait sync.WaitGroup
	wait.Add(readers)
	for index := 0; index < readers; index++ {
		go func() {
			defer wait.Done()
			if response := runtime.cachedRead("summary", nil, loader); response.StatusCode != http.StatusOK {
				t.Errorf("status = %d", response.StatusCode)
			}
		}()
	}
	<-started
	close(release)
	wait.Wait()
	if loads.Load() != 1 {
		t.Fatalf("loader ran %d times, want 1", loads.Load())
	}
}

func TestCacheClearRejectsInflightStaleResult(t *testing.T) {
	runtime := &pluginRuntime{readCache: newManagementReadCache()}
	var loads atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		runtime.cachedRead("summary", nil, func() managementResponse {
			loads.Add(1)
			close(started)
			<-release
			return jsonResponse(http.StatusOK, map[string]int{"version": 1})
		})
	}()
	<-started
	runtime.readCache.clear()
	close(release)
	<-firstDone
	runtime.cachedRead("summary", nil, func() managementResponse {
		loads.Add(1)
		return jsonResponse(http.StatusOK, map[string]int{"version": 2})
	})
	if loads.Load() != 2 {
		t.Fatalf("stale inflight response repopulated cache: loads=%d", loads.Load())
	}
}

func TestCachedReadReturns304NotModified(t *testing.T) {
	runtime := &pluginRuntime{readCache: newManagementReadCache()}
	loader := func() managementResponse {
		return jsonResponse(http.StatusOK, map[string]string{"status": "ok"})
	}
	first := runtime.cachedRead("summary", nil, loader)
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first status = %d, want 200", first.StatusCode)
	}
	etag := first.Headers.Get("ETag")
	if etag == "" {
		t.Fatal("first response missing ETag header")
	}

	headers := http.Header{"If-None-Match": []string{etag}}
	second := runtime.cachedRead("summary", headers, loader)
	if second.StatusCode != http.StatusNotModified {
		t.Fatalf("second status = %d, want 304 Not Modified", second.StatusCode)
	}
	if len(second.Body) != 0 {
		t.Fatalf("304 response contains body: %s", second.Body)
	}
}

func TestWriterCommitInvalidatesManagementCache(t *testing.T) {
	cfg := defaultConfig()
	cfg.StoragePath = filepath.Join(t.TempDir(), "usage.db")
	runtime, err := newPluginRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.close)
	now := time.Now()
	runtime.readCache.set("summary", []byte(`{"stale":true}`), now)
	if !runtime.enqueue(usageRecord{Provider: "codex", Model: "gpt", RequestedAt: now}) {
		t.Fatal("usage event was not enqueued")
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.written.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if runtime.written.Load() == 0 {
		t.Fatal("writer did not commit before deadline")
	}
	if _, ok := runtime.readCache.get("summary", time.Now()); ok {
		t.Fatal("writer commit did not invalidate management cache")
	}
}
