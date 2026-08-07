package main

import (
	"embed"
	"net/http"
	"strings"
)

//go:embed dashboard/index.html dashboard/style.css dashboard/app.js
var dashboardAssets embed.FS

func serveDashboardAsset(path string) managementResponse {
	asset := "dashboard/index.html"
	contentType := "text/html; charset=utf-8"
	switch {
	case path == "/app.js" || strings.HasSuffix(path, "/app.js"):
		asset = "dashboard/app.js"
		contentType = "text/javascript; charset=utf-8"
	case path == "/style.css" || strings.HasSuffix(path, "/style.css"):
		asset = "dashboard/style.css"
		contentType = "text/css; charset=utf-8"
	case path == "/dashboard" || path == "/v0/resource/plugins/usage-keeper/dashboard":
	default:
		return errorResponse(http.StatusNotFound, "asset_not_found", "asset not found")
	}
	body, err := dashboardAssets.ReadFile(asset)
	if err != nil {
		return internalError()
	}
	headers := noStoreHeaders(contentType)
	if asset == "dashboard/index.html" {
		headers.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'self'")
	}
	return managementResponse{StatusCode: http.StatusOK, Headers: headers, Body: body}
}
