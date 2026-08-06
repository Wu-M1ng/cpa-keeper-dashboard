package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const managementPrefix = "/v0/management/plugins/usage-keeper"

var managementNow = func() time.Time { return time.Now().UTC() }

type settingsResponse struct {
	Config  runtimeConfig `json:"config"`
	Runtime runtimeStatus `json:"runtime"`
}

type settingsPatch struct {
	RetentionDays *int `json:"retention_days"`
	ExportMax     *int `json:"export_max_records"`
}

func handleManagementEnvelope(raw []byte) []byte {
	var request managementRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return errorEnvelope("invalid_request", "invalid management request")
	}
	return okEnvelope(handleManagement(request))
}

func handleManagement(request managementRequest) managementResponse {
	if request.Method == http.MethodGet && isResourcePath(request.Path) {
		return serveDashboardAsset(request.Path)
	}
	runtime := currentRuntime()
	if runtime == nil {
		return errorResponse(http.StatusServiceUnavailable, "runtime_unavailable", errRuntimeUnavailable.Error())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	now := managementNow()

	switch request.Method + " " + request.Path {
	case http.MethodGet + " " + managementPrefix + "/summary":
		result, err := querySummary(ctx, runtime.store, request.Query, now)
		return queryJSON(result, err)
	case http.MethodGet + " " + managementPrefix + "/analysis":
		result, err := queryAnalysis(ctx, runtime.store, request.Query, now)
		return queryJSON(result, err)
	case http.MethodGet + " " + managementPrefix + "/interfaces":
		result, err := queryInterfaces(ctx, runtime.store, request.Query, now)
		return queryJSON(result, err)
	case http.MethodGet + " " + managementPrefix + "/upstream":
		result, err := queryUpstreamDetail(ctx, runtime.store, request.Query.Get("key"), request.Query, now)
		return queryJSON(result, err)
	case http.MethodGet + " " + managementPrefix + "/events":
		filter, err := parseEventFilter(request.Query, now)
		if err != nil {
			return errorResponse(http.StatusBadRequest, "invalid_filter", err.Error())
		}
		result, err := queryEvents(ctx, runtime.store, filter)
		return queryJSON(result, err)
	case http.MethodGet + " " + managementPrefix + "/events/export":
		filter, err := parseEventFilter(request.Query, now)
		if err != nil {
			return errorResponse(http.StatusBadRequest, "invalid_filter", err.Error())
		}
		runtime.configMu.RLock()
		maxRecords := runtime.config.ExportMax
		runtime.configMu.RUnlock()
		body, err := exportEventsCSV(ctx, runtime.store, filter, maxRecords)
		if err != nil {
			return internalError()
		}
		return downloadResponse("usage-events.csv", "text/csv; charset=utf-8", body)
	case http.MethodGet + " " + managementPrefix + "/settings":
		return jsonResponse(http.StatusOK, currentSettings(runtime))
	case http.MethodPut + " " + managementPrefix + "/settings":
		return updateSettings(ctx, runtime, request.Body)
	case http.MethodGet + " " + managementPrefix + "/prices":
		prices, err := listPrices(ctx, runtime.store)
		return queryJSON(map[string]any{"prices": prices}, err)
	case http.MethodPut + " " + managementPrefix + "/prices":
		return updatePrices(ctx, runtime.store, request.Body)
	case http.MethodGet + " " + managementPrefix + "/backup":
		runtime.configMu.RLock()
		maxRecords := runtime.config.ExportMax
		runtime.configMu.RUnlock()
		backup, err := exportBackup(ctx, runtime.store, maxRecords)
		if err != nil {
			return internalError()
		}
		body, _ := json.MarshalIndent(backup, "", "  ")
		return downloadResponse("usage-keeper-backup.json", "application/json; charset=utf-8", body)
	case http.MethodPost + " " + managementPrefix + "/restore":
		var payload backupPayload
		if len(request.Body) == 0 || json.Unmarshal(request.Body, &payload) != nil {
			return errorResponse(http.StatusBadRequest, "invalid_backup", "备份 JSON 无效")
		}
		result, err := importBackup(ctx, runtime.store, payload)
		return queryJSON(result, err)
	default:
		return errorResponse(http.StatusNotFound, "not_found", "route not found")
	}
}

func isResourcePath(path string) bool {
	return path == "/v0/resource/plugins/usage-keeper/dashboard" ||
		path == "/dashboard" || path == "/app.js" || path == "/style.css" ||
		path == "/v0/resource/plugins/usage-keeper/app.js" ||
		path == "/v0/resource/plugins/usage-keeper/style.css"
}

func parseEventFilter(query url.Values, now time.Time) (eventFilter, error) {
	rng, err := parseRange(query, now)
	if err != nil {
		return eventFilter{}, err
	}
	page, err := optionalPositiveInt(query.Get("page"), 1)
	if err != nil {
		return eventFilter{}, errors.New("page must be a positive integer")
	}
	pageSize, err := optionalPositiveInt(query.Get("page_size"), 25)
	if err != nil {
		return eventFilter{}, errors.New("page_size must be a positive integer")
	}
	status := query.Get("status")
	if status != "" && status != "success" && status != "failure" {
		return eventFilter{}, errors.New("status must be success or failure")
	}
	return eventFilter{
		FromMS: rng.FromMS, ToMS: rng.ToMS, Model: cleanQueryValue(query.Get("model")),
		Provider: cleanQueryValue(query.Get("provider")), APIKeyHash: cleanQueryValue(query.Get("api_key")),
		Upstream: cleanQueryValue(query.Get("upstream")), Status: status,
		Search: cleanQueryValue(query.Get("q")), Page: page, PageSize: pageSize,
	}, nil
}

func optionalPositiveInt(value string, fallback int) (int, error) {
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		return 0, errors.New("not a positive integer")
	}
	return parsed, nil
}

func cleanQueryValue(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 160 {
		return value[:160]
	}
	return value
}

func currentSettings(runtime *pluginRuntime) settingsResponse {
	runtime.configMu.RLock()
	cfg := runtime.config
	runtime.configMu.RUnlock()
	cfg.APIKeyHashSalt = ""
	return settingsResponse{Config: cfg, Runtime: runtime.status()}
}

func updateSettings(ctx context.Context, runtime *pluginRuntime, body []byte) managementResponse {
	var patch settingsPatch
	if len(body) == 0 || json.Unmarshal(body, &patch) != nil {
		return errorResponse(http.StatusBadRequest, "invalid_settings", "设置 JSON 无效")
	}
	runtime.configMu.Lock()
	cfg := runtime.config
	if patch.RetentionDays != nil {
		if *patch.RetentionDays < 1 || *patch.RetentionDays > 3650 {
			runtime.configMu.Unlock()
			return errorResponse(http.StatusBadRequest, "invalid_settings", "retention_days 必须在 1 到 3650 之间")
		}
		cfg.RetentionDays = *patch.RetentionDays
	}
	if patch.ExportMax != nil {
		if *patch.ExportMax < 100 || *patch.ExportMax > 1_000_000 {
			runtime.configMu.Unlock()
			return errorResponse(http.StatusBadRequest, "invalid_settings", "export_max_records 必须在 100 到 1000000 之间")
		}
		cfg.ExportMax = *patch.ExportMax
	}
	runtime.config = cfg
	runtime.configMu.Unlock()
	values := map[string]int{"retention_days": cfg.RetentionDays, "export_max_records": cfg.ExportMax}
	tx, err := runtime.store.db.BeginTx(ctx, nil)
	if err != nil {
		return internalError()
	}
	for key, value := range values {
		if _, err := tx.ExecContext(ctx, `INSERT INTO plugin_settings(key, value, updated_at_ms) VALUES (?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at_ms=excluded.updated_at_ms`, key, strconv.Itoa(value), time.Now().UTC().UnixMilli()); err != nil {
			_ = tx.Rollback()
			return internalError()
		}
	}
	if err := tx.Commit(); err != nil {
		return internalError()
	}
	_ = runtime.store.prune(cfg.RetentionDays, time.Now().UTC())
	return jsonResponse(http.StatusOK, currentSettings(runtime))
}

func updatePrices(ctx context.Context, store *eventStore, body []byte) managementResponse {
	var request struct {
		Prices []modelPrice `json:"prices"`
	}
	if len(body) == 0 || json.Unmarshal(body, &request) != nil {
		return errorResponse(http.StatusBadRequest, "invalid_prices", "模型价格 JSON 无效")
	}
	if err := replacePrices(ctx, store, request.Prices); err != nil {
		return errorResponse(http.StatusBadRequest, "invalid_prices", err.Error())
	}
	prices, err := listPrices(ctx, store)
	return queryJSON(map[string]any{"prices": prices}, err)
}

func queryJSON(value any, err error) managementResponse {
	if err == nil {
		return jsonResponse(http.StatusOK, value)
	}
	message := err.Error()
	if strings.Contains(message, "invalid") || strings.Contains(message, "required") || strings.Contains(message, "must") || strings.Contains(message, "unsupported backup") {
		return errorResponse(http.StatusBadRequest, "invalid_request", message)
	}
	return internalError()
}

func jsonResponse(status int, value any) managementResponse {
	body, err := json.Marshal(value)
	if err != nil {
		return internalError()
	}
	return managementResponse{StatusCode: status, Headers: noStoreHeaders("application/json; charset=utf-8"), Body: body}
}

func errorResponse(status int, code, message string) managementResponse {
	return jsonResponse(status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func internalError() managementResponse {
	body := []byte(`{"error":{"code":"internal_error","message":"插件暂时无法完成该操作"}}`)
	return managementResponse{StatusCode: http.StatusInternalServerError, Headers: noStoreHeaders("application/json; charset=utf-8"), Body: body}
}

func downloadResponse(filename, contentType string, body []byte) managementResponse {
	headers := noStoreHeaders(contentType)
	headers.Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	return managementResponse{StatusCode: http.StatusOK, Headers: headers, Body: body}
}

func noStoreHeaders(contentType string) http.Header {
	return http.Header{
		"Content-Type":           []string{contentType},
		"Cache-Control":          []string{"no-store"},
		"X-Content-Type-Options": []string{"nosniff"},
	}
}
