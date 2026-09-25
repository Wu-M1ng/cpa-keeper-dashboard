package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	runtimeMu           sync.RWMutex
	runtimeTransitionMu sync.Mutex
	activeRuntime       *pluginRuntime
)

type writerQueueItem struct {
	event   usageEvent
	barrier *writerBarrier
	counted bool
}

type writerBarrier struct {
	ready  chan struct{}
	resume chan struct{}
}

type pluginRuntime struct {
	configMu   sync.RWMutex
	settingsMu sync.Mutex
	config     runtimeConfig
	store      *eventStore
	queue      chan writerQueueItem
	readCache  *managementReadCache
	started    time.Time

	queueMu sync.RWMutex
	closed  bool
	done    chan struct{}

	accepted       atomic.Uint64
	queuedEvents   atomic.Int64
	dropped        atomic.Uint64
	written        atomic.Uint64
	writeFailures  atomic.Uint64
	writeDropped   atomic.Uint64
	writeUncertain atomic.Uint64
	lastBatchSize  atomic.Int64
	lastBatchNS    atomic.Int64
}

func configureRuntime(cfg runtimeConfig) error {
	runtimeTransitionMu.Lock()
	defer runtimeTransitionMu.Unlock()
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
		config: cfg,
		store:  store,
		// Reserve one slot for a restore barrier even when the event queue is full.
		queue:     make(chan writerQueueItem, cfg.QueueSize+1),
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
	runtimeTransitionMu.Lock()
	defer runtimeTransitionMu.Unlock()
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
		runtimeMu.RLock()
		if r := activeRuntime; r != nil {
			r.dropped.Add(1)
		}
		runtimeMu.RUnlock()
		return okEnvelope(struct{}{})
	}
	// Keep the selected runtime alive until the event is admitted or dropped.
	runtimeMu.RLock()
	if r := activeRuntime; r != nil {
		r.enqueue(record)
	}
	runtimeMu.RUnlock()
	return okEnvelope(struct{}{})
}

func (r *pluginRuntime) enqueue(record usageRecord) bool {
	r.queueMu.RLock()
	defer r.queueMu.RUnlock()
	if r.closed {
		r.dropped.Add(1)
		return false
	}
	r.configMu.RLock()
	salt := r.config.APIKeyHashSalt
	queueLimit := r.config.QueueSize
	r.configMu.RUnlock()
	if queueLimit < 1 || queueLimit > cap(r.queue) {
		queueLimit = cap(r.queue)
	}
	for {
		queued := r.queuedEvents.Load()
		if queued >= int64(queueLimit) {
			r.dropped.Add(1)
			return false
		}
		if r.queuedEvents.CompareAndSwap(queued, queued+1) {
			break
		}
	}
	event := compactUsageRecord(record, salt)

	select {
	case r.queue <- writerQueueItem{event: event, counted: true}:
		r.accepted.Add(1)
		return true
	default:
		r.queuedEvents.Add(-1)
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
		r.writeBatchWithRetry(batch)
		r.lastBatchSize.Store(int64(count))
		r.lastBatchNS.Store(time.Since(started).Nanoseconds())
		clear(batch[:cap(batch)])
		batch = batch[:0]
	}

	for {
		select {
		case item, ok := <-r.queue:
			if !ok {
				flush()
				return
			}
			if item.counted {
				r.queuedEvents.Add(-1)
			}
			if item.barrier != nil {
				flush()
				close(item.barrier.ready)
				<-item.barrier.resume
				continue
			}
			batch = append(batch, item.event)
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

func (r *pluginRuntime) pauseWriter(ctx context.Context) (func(), error) {
	barrier := &writerBarrier{ready: make(chan struct{}), resume: make(chan struct{})}
	r.queueMu.Lock()
	if r.closed {
		r.queueMu.Unlock()
		return nil, errRuntimeUnavailable
	}
	select {
	case r.queue <- writerQueueItem{barrier: barrier}:
		r.queueMu.Unlock()
	default:
		r.queueMu.Unlock()
		return nil, errors.New("writer queue cannot accept restore barrier")
	}
	select {
	case <-barrier.ready:
		return sync.OnceFunc(func() { close(barrier.resume) }), nil
	case <-ctx.Done():
		close(barrier.resume)
		return nil, ctx.Err()
	case <-r.done:
		close(barrier.resume)
		return nil, errRuntimeUnavailable
	}
}

func (r *pluginRuntime) restoreBackup(ctx context.Context, payload backupPayload) (importResult, error) {
	runtimeTransitionMu.Lock()
	defer runtimeTransitionMu.Unlock()
	if currentRuntime() != r {
		return importResult{}, errRuntimeUnavailable
	}
	if r.done != nil {
		resume, err := r.pauseWriter(ctx)
		if err != nil {
			return importResult{}, err
		}
		defer resume()
	}
	r.settingsMu.Lock()
	defer r.settingsMu.Unlock()
	result, err := importBackup(ctx, r.store, payload)
	if err == nil {
		r.readCache.clear()
	}
	return result, err
}

func (r *pluginRuntime) exportBackup(ctx context.Context, maxRecords int) (backupPayload, error) {
	unlock, err := r.prepareBackupExport(ctx)
	if err != nil {
		return backupPayload{}, err
	}
	defer unlock()
	return exportBackup(ctx, r.store, maxRecords)
}

func (r *pluginRuntime) exportBackupJSON(ctx context.Context, maxRecords int) ([]byte, error) {
	unlock, err := r.prepareBackupExport(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return exportBackupJSON(ctx, r.store, maxRecords)
}

func (r *pluginRuntime) prepareBackupExport(ctx context.Context) (func(), error) {
	runtimeTransitionMu.Lock()
	if currentRuntime() != r {
		runtimeTransitionMu.Unlock()
		return nil, errRuntimeUnavailable
	}
	if r.done != nil {
		resume, err := r.pauseWriter(ctx)
		if err != nil {
			runtimeTransitionMu.Unlock()
			return nil, err
		}
		resume()
	}
	return runtimeTransitionMu.Unlock, nil
}

// The batch remains owned by the writer until it succeeds or the bounded
// retry budget is exhausted. Enqueue remains non-blocking during backoff.
func (r *pluginRuntime) writeBatchWithRetry(batch []usageEvent) {
	const maxAttempts = 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		err := r.store.writeBatch(batch)
		if err == nil {
			r.written.Add(uint64(len(batch)))
			return
		}
		r.writeFailures.Add(1)
		var uncertain *uncertainBatchWriteError
		if errors.As(err, &uncertain) {
			// A failed COMMIT may already have persisted the batch. Do not
			// replay it or label it as a confirmed loss.
			r.writeUncertain.Add(uint64(len(batch)))
			return
		}
		if attempt == maxAttempts-1 {
			break
		}
		time.Sleep((250 * time.Millisecond) << attempt)
	}
	r.writeDropped.Add(uint64(len(batch)))
	r.dropped.Add(uint64(len(batch)))
}

func (r *pluginRuntime) pruneExpired(now time.Time) error {
	// Serialize the complete read-config/prune operation with settings updates.
	// Otherwise a background task can capture the old retention value and run
	// after a newer value has been committed.
	r.settingsMu.Lock()
	defer r.settingsMu.Unlock()
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
	queueCapacity := cap(r.queue)
	if cfg.QueueSize > 0 && cfg.QueueSize < queueCapacity {
		queueCapacity = cfg.QueueSize
	}
	return runtimeStatus{
		Accepted:       r.accepted.Load(),
		Dropped:        r.dropped.Load(),
		Written:        r.written.Load(),
		WriteFailures:  r.writeFailures.Load(),
		WriteDropped:   r.writeDropped.Load(),
		WriteUncertain: r.writeUncertain.Load(),
		QueueDepth:     len(r.queue),
		QueueCapacity:  queueCapacity,
		LastBatchSize:  r.lastBatchSize.Load(),
		LastBatchMS:    float64(r.lastBatchNS.Load()) / float64(time.Millisecond),
		StartedAt:      r.started.Format(time.RFC3339),
		Storage:        storage,
		RetentionDays:  cfg.RetentionDays,
		BatchSize:      cfg.BatchSize,
		FlushInterval:  cfg.FlushIntervalMS,
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
		Endpoint:     sanitizeEndpoint(resolveEndpoint(record.BaseURL, record.Endpoint)),
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

func resolveEndpoint(baseURL, endpoint string) string {
	baseURL = strings.TrimSpace(baseURL)
	endpoint = strings.TrimSpace(endpoint)
	if baseURL != "" && endpoint != "" {
		if strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
			return endpoint
		}
		if strings.HasPrefix(baseURL, "http://") || strings.HasPrefix(baseURL, "https://") {
			baseParsed, err := url.Parse(baseURL)
			if err == nil && baseParsed.Host != "" {
				if strings.HasPrefix(endpoint, baseParsed.Path) {
					return baseParsed.Scheme + "://" + baseParsed.Host + endpoint
				}
				return strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(endpoint, "/")
			}
		}
		return baseURL
	}
	if baseURL != "" {
		return baseURL
	}
	return endpoint
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

func cleanControlChars(s string, maxLen int) string {
	cleaned := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	if len(cleaned) <= maxLen {
		return cleaned
	}
	cleaned = cleaned[:maxLen]
	for !utf8.ValidString(cleaned) {
		cleaned = cleaned[:len(cleaned)-1]
	}
	return cleaned
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
		if isMaskedProviderCredential(storedCredential) {
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
	oversized := len(value) > 160
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	if len(value) > 160 {
		value = value[:160]
	}
	cleaned := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
	if oversized {
		return strings.Clone(cleaned)
	}
	return cleaned
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
