package main

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	managementReadCacheTTL   = 4 * time.Second
	managementReadCacheItems = 32
	managementReadCacheBytes = 4 << 20
)

type managementReadCacheEntry struct {
	body       []byte
	expiresAt  time.Time
	lastAccess uint64
}

type managementReadCache struct {
	mu         sync.Mutex
	entries    map[string]managementReadCacheEntry
	inflight   map[string]managementReadCacheFlight
	bytes      int
	clock      uint64
	generation uint64
}

type managementReadCacheFlight struct {
	done       chan struct{}
	generation uint64
}

func newManagementReadCache() *managementReadCache {
	return &managementReadCache{
		entries:  make(map[string]managementReadCacheEntry),
		inflight: make(map[string]managementReadCacheFlight),
	}
}

func (c *managementReadCache) get(key string, now time.Time) ([]byte, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if !now.Before(entry.expiresAt) {
		c.removeLocked(key)
		return nil, false
	}
	c.clock++
	entry.lastAccess = c.clock
	c.entries[key] = entry
	return append([]byte(nil), entry.body...), true
}

func (c *managementReadCache) set(key string, body []byte, now time.Time) {
	if c == nil || len(body) == 0 || len(body) > managementReadCacheBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setLocked(key, body, now)
}

func (c *managementReadCache) setLocked(key string, body []byte, now time.Time) {
	if previous, ok := c.entries[key]; ok {
		c.bytes -= len(previous.body)
		delete(c.entries, key)
	}
	c.clock++
	c.entries[key] = managementReadCacheEntry{
		body:       append([]byte(nil), body...),
		expiresAt:  now.Add(managementReadCacheTTL),
		lastAccess: c.clock,
	}
	c.bytes += len(body)
	for len(c.entries) > managementReadCacheItems || c.bytes > managementReadCacheBytes {
		c.evictOldestLocked()
	}
}

func (c *managementReadCache) acquire(key string, now time.Time) ([]byte, bool, <-chan struct{}, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, ok := c.entries[key]; ok {
		if now.Before(entry.expiresAt) {
			c.clock++
			entry.lastAccess = c.clock
			c.entries[key] = entry
			return append([]byte(nil), entry.body...), true, nil, 0
		}
		c.removeLocked(key)
	}
	if flight, ok := c.inflight[key]; ok {
		return nil, false, flight.done, 0
	}
	generation := c.generation
	c.inflight[key] = managementReadCacheFlight{done: make(chan struct{}), generation: generation}
	return nil, false, nil, generation
}

func (c *managementReadCache) finish(key string, generation uint64, body []byte, cacheable bool, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	flight, ok := c.inflight[key]
	if !ok || flight.generation != generation {
		return
	}
	if cacheable && c.generation == generation && len(body) > 0 && len(body) <= managementReadCacheBytes {
		c.setLocked(key, body, now)
	}
	delete(c.inflight, key)
	close(flight.done)
}

func (c *managementReadCache) clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.entries = make(map[string]managementReadCacheEntry)
	c.bytes = 0
	c.generation++
	c.mu.Unlock()
}

func (c *managementReadCache) removeLocked(key string) {
	if entry, ok := c.entries[key]; ok {
		c.bytes -= len(entry.body)
		delete(c.entries, key)
	}
}

func (c *managementReadCache) evictOldestLocked() {
	var oldestKey string
	var oldest uint64
	for key, entry := range c.entries {
		if oldestKey == "" || entry.lastAccess < oldest {
			oldestKey, oldest = key, entry.lastAccess
		}
	}
	if oldestKey != "" {
		c.removeLocked(oldestKey)
	}
}

func (r *pluginRuntime) cachedRead(key string, reqHeaders http.Header, load func() managementResponse) managementResponse {
	if r == nil || r.readCache == nil {
		return load()
	}
	ifNoneMatch := ""
	if reqHeaders != nil {
		ifNoneMatch = strings.TrimSpace(reqHeaders.Get("If-None-Match"))
	}
	for {
		body, hit, wait, generation := r.readCache.acquire(key, time.Now())
		if hit {
			etag := computeETag(body)
			if ifNoneMatch != "" && (ifNoneMatch == etag || ifNoneMatch == "W/"+etag || "W/"+ifNoneMatch == etag) {
				return managementResponse{
					StatusCode: http.StatusNotModified,
					Headers:    etagHeaders(etag),
				}
			}
			return managementResponse{
				StatusCode: http.StatusOK,
				Headers:    etagHeaders(etag),
				Body:       body,
			}
		}
		if wait != nil {
			<-wait
			continue
		}
		response := load()
		cacheable := response.StatusCode == http.StatusOK && response.Headers.Get("Content-Type") == "application/json; charset=utf-8"
		if cacheable && len(response.Body) > 0 {
			etag := computeETag(response.Body)
			response.Headers.Set("ETag", etag)
			response.Headers.Set("Cache-Control", "no-cache")
		}
		r.readCache.finish(key, generation, response.Body, cacheable, time.Now())
		return response
	}
}

func computeETag(body []byte) string {
	sum := sha256.Sum256(body)
	return fmt.Sprintf(`"W/"%x""`, sum[:8])
}

func etagHeaders(etag string) http.Header {
	return http.Header{
		"Content-Type":           []string{"application/json; charset=utf-8"},
		"Cache-Control":          []string{"no-cache"},
		"ETag":                   []string{etag},
		"X-Content-Type-Options": []string{"nosniff"},
	}
}
