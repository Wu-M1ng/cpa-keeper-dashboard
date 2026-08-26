package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestDashboardContentContract protects the embedded UI from accidentally losing
// a user-facing section while the presentation layer is being refactored.
func TestDashboardContentContract(t *testing.T) {
	html := serveDashboardAsset("/v0/resource/plugins/usage-keeper/dashboard")
	if html.StatusCode != 200 {
		t.Fatalf("dashboard asset status: %d", html.StatusCode)
	}
	body := string(html.Body)
	requiredHTML := []string{
		`class="top-nav"`, `data-region="global-nav"`,
		`data-page-target="overview"`, `data-page-target="interfaces"`, `data-page-target="settings"`,
		`id="page-title"`, `id="page-subtitle"`, `id="range-control"`, `id="refresh-button"`,
		`id="overview-kpis"`, `id="trend-legend"`, `data-dim="input"`, `data-dim="output"`,
		`data-dim="cache_write"`, `data-dim="cache_read"`, `data-dim="hit_rate"`,
		`id="trend-chart"`, `id="health-grid"`, `id="distribution-grid"`, `id="token-total"`,
		`id="token-composition"`, `id="model-table"`, `id="runtime-strip"`, `id="event-filters"`,
		`id="event-table"`, `id="event-export"`, `id="page-prev"`, `id="page-next"`,
		`id="interface-summary"`, `id="api-key-table"`, `id="upstream-table"`,
		`id="detail-drawer"`, `id="detail-content"`, `id="storage-settings"`, `id="price-table"`,
		`id="backup-export"`, `id="backup-import"`, `id="change-key"`, `id="auth-dialog"`,
		`id="floating-tooltip"`,
		"Token 构成", "模型统计", "上游统计", "请求明细记录", "上游详情",
	}
	for _, token := range requiredHTML {
		if !strings.Contains(body, token) {
			t.Fatalf("dashboard HTML is missing %q", token)
		}
	}
	if strings.Count(body, `data-page="overview"`) != 1 {
		t.Fatal("overview must remain a single combined page")
	}
	if strings.Contains(body, `data-page-target="analysis"`) || strings.Contains(body, `data-page-target="events"`) {
		t.Fatal("analysis and events must remain combined in overview")
	}
	if strings.Contains(body, ">端点<") || strings.Contains(body, "输入模型、渠道、端点") {
		t.Fatal("request details must not expose an endpoint column")
	}

	js := serveDashboardAsset("/v0/resource/plugins/usage-keeper/app.js")
	if js.StatusCode != 200 {
		t.Fatalf("app.js asset status: %d", js.StatusCode)
	}
	for _, token := range []string{
		"trendActiveDims", "cache_write", "cache_read", "hit_rate", "formatTokenCompact", "cacheHitRate",
		"MutationObserver", "document.visibilityState", "FRONTEND_CACHE_TTL_MS", "new AbortController()",
		"loadRequestID", "eventRequestID", "requestAnimationFrame", "Asia/Shanghai", "日均用量", "总请求数",
		"总 Token 消耗", "RPM", "TPM", "总费用",
	} {
		if !bytes.Contains(js.Body, []byte(token)) {
			t.Fatalf("dashboard JS is missing %q", token)
		}
	}
	if bytes.Count(js.Body, []byte("function bindDistributionInteractivity(")) != 1 {
		t.Fatal("distribution interaction must have one binding implementation")
	}

	css := serveDashboardAsset("/v0/resource/plugins/usage-keeper/style.css")
	if css.StatusCode != 200 {
		t.Fatalf("style.css asset status: %d", css.StatusCode)
	}
	cssText := string(css.Body)
	if strings.Count(cssText, "{") != strings.Count(cssText, "}") {
		t.Fatalf("CSS braces are unbalanced: %d open / %d close", strings.Count(cssText, "{"), strings.Count(cssText, "}"))
	}
	for _, token := range []string{".interface-card", ".progress-cell", ".chart-host-interactive", ".floating-glass-tooltip"} {
		if !strings.Contains(cssText, token) {
			t.Fatalf("dashboard CSS is missing %q", token)
		}
	}
	for _, stale := range []string{".sidebar", ".sidebar-status", ".nav-heading", ".theme-switcher"} {
		if strings.Contains(cssText, stale) {
			t.Fatalf("stale selector remains: %s", stale)
		}
	}
}
