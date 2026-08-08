package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	runtimeMu     sync.RWMutex
	activeRuntime *pluginRuntime
)

type pluginRuntime struct {
	configMu   sync.RWMutex
	settingsMu sync.Mutex
	config     runtimeConfig
	store      *eventStore
	queue      chan usageEvent
	readCache  *managementReadCache
	started    time.Time

	queueMu sync.RWMutex
	closed  bool
	done    chan struct{}

	accepted      atomic.Uint64
	dropped       atomic.Uint64
	written       atomic.Uint64
	writeFailures atomic.Uint64
	lastBatchSize atomic.Int64
	lastBatchNS   atomic.Int64
}

func configureRuntime(cfg runtimeConfig) error {
	next, err := newPluginRuntime(cfg)
	if err != nil {
		return err
	}
	runtimeMu.Lock()
	previous := activeRuntime
	activeRuntime = next
	runtimeMu.Unlock()
	if previous != nil {
		previous.close()
	}
	return nil
}

func newPluginRuntime(cfg runtimeConfig) (*pluginRuntime, error) {
	store, err := openEventStore(cfg)
	if err != nil {
		return nil, err
	}
	cfg.RetentionDays = store.loadIntSetting("retention_days", cfg.RetentionDays)
	cfg.ExportMax = store.loadIntSetting("export_max_records", cfg.ExportMax)
	r := &pluginRuntime{
		config:    cfg,
		store:     store,
		queue:     make(chan usageEvent, cfg.QueueSize),
		readCache: newManagementReadCache(),
		started:   time.Now().UTC(),
		done:      make(chan struct{}),
	}
	go r.runWriter()
	return r, nil
}

func currentRuntime() *pluginRuntime {
	runtimeMu.RLock()
	r := activeRuntime
	runtimeMu.RUnlock()
	return r
}

func shutdownRuntime() {
	runtimeMu.Lock()
	r := activeRuntime
	activeRuntime = nil
	runtimeMu.Unlock()
	if r != nil {
		r.close()
	}
}

func handleUsage(raw []byte) []byte {
	var record usageRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		// Usage observation is fail-open: malformed telemetry must not affect CPA.
		if r := currentRuntime(); r != nil {
			r.dropped.Add(1)
		}
		return okEnvelope(struct{}{})
	}
	if r := currentRuntime(); r != nil {
		r.enqueue(record)
	}
	return okEnvelope(struct{}{})
}

func (r *pluginRuntime) enqueue(record usageRecord) bool {
	if len(r.queue) >= cap(r.queue) {
		r.dropped.Add(1)
		return false
	}
	r.configMu.RLock()
	salt := r.config.APIKeyHashSalt
	r.configMu.RUnlock()
	event := compactUsageRecord(record, salt)

	r.queueMu.RLock()
	defer r.queueMu.RUnlock()
	if r.closed {
		r.dropped.Add(1)
		return false
	}
	select {
	case r.queue <- event:
		r.accepted.Add(1)
		return true
	default:
		r.dropped.Add(1)
		return false
	}
}

func (r *pluginRuntime) runWriter() {
	defer close(r.done)
	r.configMu.RLock()
	batchSize := r.config.BatchSize
	flushEvery := r.config.flushInterval()
	r.configMu.RUnlock()

	ticker := time.NewTicker(flushEvery)
	defer ticker.Stop()
	retentionTicker := time.NewTicker(24 * time.Hour)
	defer retentionTicker.Stop()
	_ = r.pruneExpired(time.Now().UTC())

	batch := make([]usageEvent, 0, batchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		started := time.Now()
		count := len(batch)
		if err := r.store.writeBatch(batch); err != nil {
			r.writeFailures.Add(1)
		} else {
			r.written.Add(uint64(count))
		}
		r.lastBatchSize.Store(int64(count))
		r.lastBatchNS.Store(time.Since(started).Nanoseconds())
		batch = batch[:0]
	}

	for {
		select {
		case event, ok := <-r.queue:
			if !ok {
				flush()
				return
			}
			batch = append(batch, event)
			if len(batch) >= batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case now := <-retentionTicker.C:
			flush()
			if err := r.pruneExpired(now.UTC()); err == nil {
				r.readCache.clear()
			}
		}
	}
}

func (r *pluginRuntime) pruneExpired(now time.Time) error {
	r.configMu.RLock()
	retentionDays := r.config.RetentionDays
	r.configMu.RUnlock()
	return r.store.prune(retentionDays, now)
}

func (r *pluginRuntime) close() {
	r.queueMu.Lock()
	if r.closed {
		r.queueMu.Unlock()
		return
	}
	r.closed = true
	close(r.queue)
	r.queueMu.Unlock()
	<-r.done
	_ = r.store.close()
}

func (r *pluginRuntime) status() runtimeStatus {
	return r.runtimeStatus(r.store.status())
}

func (r *pluginRuntime) summaryStatus() runtimeStatus {
	return r.runtimeStatus(r.store.statusSnapshot())
}

func (r *pluginRuntime) runtimeStatus(storage storageStatus) runtimeStatus {
	r.configMu.RLock()
	cfg := r.config
	r.configMu.RUnlock()
	return runtimeStatus{
		Accepted:      r.accepted.Load(),
		Dropped:       r.dropped.Load(),
		Written:       r.written.Load(),
		WriteFailures: r.writeFailures.Load(),
		QueueDepth:    len(r.queue),
		QueueCapacity: cap(r.queue),
		LastBatchSize: r.lastBatchSize.Load(),
		LastBatchMS:   float64(r.lastBatchNS.Load()) / float64(time.Millisecond),
		StartedAt:     r.started.Format(time.RFC3339),
		Storage:       storage,
		RetentionDays: cfg.RetentionDays,
		BatchSize:     cfg.BatchSize,
		FlushInterval: cfg.FlushIntervalMS,
	}
}

func compactUsageRecord(record usageRecord, salt string) usageEvent {
	requestedAt := record.RequestedAt.UTC()
	if requestedAt.IsZero() {
		requestedAt = time.Now().UTC()
	}
	provider := cleanDimension(record.Provider, "unknown")
	model := cleanDimension(record.Model, "unknown")
	upstreamMaterial := strings.Join([]string{provider, record.AuthID, record.AuthIndex, record.AuthType}, "\x00")
	upstreamKey := shortHMAC(upstreamMaterial, salt)
	credential := providerCredentialFromSource(provider, record.Source)
	if credential == "" {
		credential = preferredProviderCredential(record.AuthType, record.AuthID, record.AuthIndex)
	}
	upstreamLabel := providerCredentialLabel(provider, credential, upstreamKey)
	source := upstreamLabel
	apiHash := ""
	apiMask := anonymousLabel("key", "")
	if record.APIKey != "" {
		apiHash = shortHMAC(record.APIKey, salt)
		apiMask = anonymousLabel("key", apiHash)
	}
	cacheRead := record.Detail.CacheReadTokens
	if cacheRead == 0 {
		cacheRead = record.Detail.CachedTokens
	}
	total := record.Detail.TotalTokens
	if total == 0 {
		total = record.Detail.InputTokens + record.Detail.OutputTokens
	}
	return usageEvent{
		TimestampMS:  requestedAt.UnixMilli(),
		Provider:     provider,
		ExecutorType: cleanDimension(record.ExecutorType, ""),
		Model:        model,
		Alias:        cleanDimension(record.Alias, ""),
		Endpoint:     sanitizeEndpoint(record.Endpoint),
		APIKeyMask:   apiMask,
		APIKeyHash:   apiHash,
		// Raw account identifiers are only used above to derive the upstream key.
		// They must never enter the queue, SQLite database, or management API.
		AuthID:              "",
		AuthIndex:           "",
		AuthType:            "",
		UpstreamKey:         upstreamKey,
		UpstreamLabel:       upstreamLabel,
		Source:              source,
		ReasoningEffort:     cleanDimension(record.ReasoningEffort, ""),
		ServiceTier:         cleanDimension(record.ServiceTier, ""),
		Generate:            record.Generate || record.Stream,
		LatencyMS:           max64(0, record.Latency.Milliseconds()),
		TTFTMS:              max64(0, record.TTFT.Milliseconds()),
		Failed:              record.Failed,
		StatusCode:          record.Failure.StatusCode,
		Failure:             sanitizeFailure(record.Failure.Body),
		InputTokens:         max64(0, record.Detail.InputTokens),
		OutputTokens:        max64(0, record.Detail.OutputTokens),
		ReasoningTokens:     max64(0, record.Detail.ReasoningTokens),
		CachedTokens:        max64(0, record.Detail.CachedTokens),
		CacheReadTokens:     max64(0, cacheRead),
		CacheCreationTokens: max64(0, record.Detail.CacheCreationTokens),
		TotalTokens:         max64(0, total),
	}
}

func shortHMAC(value, salt string) string {
	mac := hmac.New(sha256.New, []byte(salt))
	_, _ = mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil)[:8])
}

func anonymousLabel(prefix, digest string) string {
	digest = strings.TrimSpace(digest)
	if digest == "" {
		return prefix + "-unknown"
	}
	if len(digest) > 8 {
		digest = digest[:8]
	}
	return prefix + "-" + digest
}

func normalizeEventForStorage(event *usageEvent, salt string) {
	event.Provider = cleanDimension(event.Provider, "unknown")
	event.Model = cleanDimension(event.Model, "unknown")
	event.Endpoint = sanitizeEndpoint(event.Endpoint)
	if event.APIKeyHash != "" && !isHexDigest(event.APIKeyHash, 16) {
		event.APIKeyHash = shortHMAC(event.APIKeyHash, salt)
	}
	if event.UpstreamKey == "" {
		material := strings.Join([]string{event.Provider, event.AuthID, event.AuthIndex, event.AuthType}, "\x00")
		event.UpstreamKey = shortHMAC(material, salt)
	} else if !isHexDigest(event.UpstreamKey, 16) {
		event.UpstreamKey = shortHMAC(event.UpstreamKey, salt)
	}
	event.APIKeyMask = anonymousLabel("key", event.APIKeyHash)
	credential := providerCredentialFromSource(event.Provider, event.Source)
	if credential == "" {
		credential = preferredProviderCredential(event.AuthType, event.AuthID, event.AuthIndex)
	}
	event.UpstreamLabel = providerCredentialLabelFromStored(event.Provider, credential, event.UpstreamLabel, event.UpstreamKey)
	event.Source = event.UpstreamLabel
	event.AuthID = ""
	event.AuthIndex = ""
	event.AuthType = ""
	event.Failure = sanitizeFailure(event.Failure)
}

func sanitizeEndpoint(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	path := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, parsed.Path)
	if len(path) <= 256 {
		return path
	}
	path = path[:256]
	for !utf8.ValidString(path) {
		path = path[:len(path)-1]
	}
	return path
}

func preferredProviderCredential(authType, authID, authIndex string) string {
	authType = strings.ToLower(strings.TrimSpace(authType))
	if strings.Contains(authType, "api") || strings.Contains(authType, "key") {
		return firstNonEmpty(authIndex, authID, authType)
	}
	return firstNonEmpty(authID, authIndex, authType)
}

func providerCredentialFromSource(provider, source string) string {
	source = strings.TrimSpace(strings.ReplaceAll(source, " · ", " / "))
	if source == "" {
		return ""
	}
	parts := strings.Split(source, " / ")
	candidate := strings.TrimSpace(parts[len(parts)-1])
	lower := strings.ToLower(candidate)
	if candidate == "" || strings.EqualFold(candidate, strings.TrimSpace(provider)) {
		return ""
	}
	switch lower {
	case "unknown", "apikey", "api-key", "key", "credential", "auth", "oauth":
		return ""
	}
	if len(parts) > 1 || strings.Contains(candidate, "***") || strings.Contains(candidate, "@") || len([]rune(candidate)) >= 10 {
		return candidate
	}
	return ""
}

func providerCredentialLabel(provider, credential, fallbackKey string) string {
	credential = cleanProviderCredential(credential)
	if credential == "" {
		credential = fallbackKey
	}
	return cleanDimension(provider, "unknown") + " / " + maskProviderCredential(credential)
}

func providerCredentialLabelFromStored(provider, credential, storedLabel, fallbackKey string) string {
	if credential == "" {
		storedCredential := strings.TrimSpace(storedLabel)
		if prefix := cleanDimension(provider, "unknown") + " / "; strings.HasPrefix(storedCredential, prefix) {
			storedCredential = strings.TrimSpace(strings.TrimPrefix(storedCredential, prefix))
		}
		if strings.Contains(storedCredential, "***") {
			return cleanDimension(provider, "unknown") + " / " + storedCredential
		}
		credential = storedCredential
	}
	return providerCredentialLabel(provider, credential, fallbackKey)
}

func cleanProviderCredential(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimSuffix(value, ".json")
	value = strings.TrimSuffix(value, ".yaml")
	value = strings.TrimSuffix(value, ".yml")
	return strings.TrimSpace(value)
}

func maskProviderCredential(value string) string {
	value = cleanProviderCredential(value)
	if value == "" {
		return "unknown"
	}
	runes := []rune(value)
	if len(runes) >= 6 {
		return string(runes[:3]) + "***" + string(runes[len(runes)-2:])
	}
	if len(runes) >= 3 {
		return string(runes[:1]) + "***" + string(runes[len(runes)-1:])
	}
	return string(runes[:1]) + "***"
}

func isHexDigest(value string, length int) bool {
	if len(value) != length {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func cleanDimension(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	if len(value) > 160 {
		value = value[:160]
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
}

func sanitizeFailure(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 512 {
		value = value[:512]
	}
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func (e usageEvent) String() string {
	return fmt.Sprintf("%s/%s@%d", e.Provider, e.Model, e.TimestampMS)
}
