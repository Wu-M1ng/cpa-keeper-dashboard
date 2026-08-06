package main

import (
	"encoding/json"
	"fmt"
)

func okEnvelope(result any) []byte {
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return errorEnvelope("marshal_error", "failed to encode plugin response")
	}
	raw, _ := json.Marshal(envelope{OK: true, Result: resultJSON})
	return raw
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case "plugin.register", "plugin.reconfigure":
		return handleRegister(request)
	case "usage.handle":
		return handleUsage(request), nil
	case "management.register":
		return okEnvelope(managementRoutes()), nil
	case "management.handle":
		return handleManagementEnvelope(request), nil
	case "plugin.shutdown":
		shutdownRuntime()
		return okEnvelope(struct{}{}), nil
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func handleRegister(raw []byte) ([]byte, error) {
	var request pluginRegisterRequest
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &request); err != nil {
			return errorEnvelope("invalid_request", "invalid plugin registration request"), nil
		}
	}
	cfg, err := parseConfig(request.ConfigYAML)
	if err != nil {
		return errorEnvelope("invalid_config", err.Error()), nil
	}
	if err := configureRuntime(cfg); err != nil {
		return errorEnvelope("storage_error", fmt.Sprintf("start usage storage: %v", err)), nil
	}

	return okEnvelope(pluginRegisterResponse{
		SchemaVersion: schemaVersion,
		Metadata: pluginMetadata{
			Name:             "用量 Keeper",
			Version:          pluginVersion,
			Author:           "local",
			GitHubRepository: "https://github.com/Wu-M1ng/cpa-keeper-dashboard",
			ConfigFields: []configField{
				{Name: "storage_enabled", Type: "boolean", Default: true, Description: "启用 SQLite 持久化。"},
				{Name: "storage_path", Type: "string", Default: "data/usage-keeper.db", Description: "SQLite 数据库路径。"},
				{Name: "queue_size", Type: "integer", Default: defaultQueueSize, Description: "非阻塞用量队列容量。"},
				{Name: "batch_size", Type: "integer", Default: defaultBatchSize, Description: "单次 SQLite 批写记录数。"},
				{Name: "flush_interval_ms", Type: "integer", Default: defaultFlushIntervalMS, Description: "后台批写最长等待时间。"},
				{Name: "retention_days", Type: "integer", Default: defaultRetentionDays, Description: "事件与聚合数据保留天数。"},
				{Name: "export_max_records", Type: "integer", Default: defaultExportMax, Description: "单次明细导出上限。"},
				{Name: "api_key_hash_salt", Type: "string", Default: "", Description: "API Key 分组哈希盐。"},
			},
		},
		Capabilities: pluginCapabilities{UsagePlugin: true, ManagementAPI: true},
	}), nil
}

func managementRoutes() managementRegistration {
	return managementRegistration{
		Resources: []resourceRoute{
			{Path: "/dashboard", Menu: "用量 Keeper", Description: "轻量用量统计与健康监测。"},
			{Path: "/app.js"},
			{Path: "/style.css"},
		},
		Routes: []managementRoute{
			{Method: "GET", Path: "/plugins/usage-keeper/summary"},
			{Method: "GET", Path: "/plugins/usage-keeper/analysis"},
			{Method: "GET", Path: "/plugins/usage-keeper/interfaces"},
			{Method: "GET", Path: "/plugins/usage-keeper/upstream"},
			{Method: "GET", Path: "/plugins/usage-keeper/events"},
			{Method: "GET", Path: "/plugins/usage-keeper/events/export"},
			{Method: "GET", Path: "/plugins/usage-keeper/settings"},
			{Method: "PUT", Path: "/plugins/usage-keeper/settings"},
			{Method: "GET", Path: "/plugins/usage-keeper/prices"},
			{Method: "PUT", Path: "/plugins/usage-keeper/prices"},
			{Method: "GET", Path: "/plugins/usage-keeper/backup"},
			{Method: "POST", Path: "/plugins/usage-keeper/restore"},
		},
	}
}
