package main

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

func TestDashboardAssetsAreEmbeddedAndSelfContained(t *testing.T) {
	root := serveDashboardAsset("/v0/resource/plugins/usage-keeper/dashboard")
	if root.StatusCode != http.StatusOK || root.Headers.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("root asset response: %+v", root)
	}
	csp := root.Headers.Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self'") || !strings.Contains(csp, "style-src 'self' 'unsafe-inline'") {
		t.Fatalf("dashboard CSP does not allow its trusted dynamic styles: %q", csp)
	}
	for _, page := range []string{"overview", "interfaces", "settings"} {
		if !bytes.Contains(root.Body, []byte(`data-page="`+page+`"`)) {
			t.Fatalf("dashboard is missing page %q", page)
		}
	}
	if bytes.Count(root.Body, []byte(`data-page="overview"`)) != 1 {
		t.Fatal("overview must combine summary, analysis, and events in one page")
	}
	if bytes.Contains(root.Body, []byte(`data-page-target="analysis"`)) || bytes.Contains(root.Body, []byte(`data-page-target="events"`)) {
		t.Fatal("analysis and events must not remain as separate navigation pages")
	}
	for _, expected := range []string{
		`class="top-nav"`,
		`id="overview-kpis"`,
		`data-page-target="overview"`,
		`data-page-target="interfaces"`,
		`data-page-target="settings"`,
		`data-dim="input"`,
		`data-dim="output"`,
		`data-dim="cache_write"`,
		`data-dim="cache_read"`,
		`data-dim="hit_rate"`,
		"缓存命中率",
		"缓存创建",
		"缓存读取",
		`id="trend-legend"`,
		"trend-legend-interactive",
		"chart-host-interactive",
		`id="event-reset"`,
		`id="floating-tooltip"`,
		"服务健康监测",
		"最近5天，15分钟一个网格",
		`id="health-success-count"`,
		`id="health-failure-count"`,
		"时间", "模型 / 渠道", "推理强度", "状态", "用时 / 首字", "Token 明细",
		"命中率", "总 Token",
	} {
		if !bytes.Contains(root.Body, []byte(expected)) {
			t.Fatalf("dashboard HTML is missing %q", expected)
		}
	}
	bodyStr := string(root.Body)
	if strings.Index(bodyStr, `id="overview-kpis"`) > strings.Index(bodyStr, `id="trend-chart"`) {
		t.Fatal("overview KPI cards must remain before the trend chart")
	}
	if strings.Contains(bodyStr, `class="sidebar"`) {
		t.Fatal("dashboard navigation must use the top navigation")
	}
	if bytes.Contains(root.Body, []byte(">端点<")) || bytes.Contains(root.Body, []byte("输入模型、渠道、端点")) {
		t.Fatal("event table must not expose an endpoint column")
	}
	if strings.Contains(string(root.Body), "https://") || strings.Contains(string(root.Body), "http://") {
		t.Fatal("dashboard HTML must not load external resources")
	}
	js := serveDashboardAsset("/v0/resource/plugins/usage-keeper/app.js")
	if js.StatusCode != http.StatusOK || js.Headers.Get("Content-Type") != "text/javascript; charset=utf-8" {
		t.Fatalf("JS asset response: %+v", js)
	}
	if !bytes.Contains(js.Body, []byte("usage-keeper-management-key")) {
		t.Fatal("JS asset does not include session auth handling")
	}
	if !bytes.Contains(js.Body, []byte("const endpoint = path.startsWith('/') ? API_ROOT + path : API_ROOT + '/' + path;")) {
		t.Fatal("JS asset must prefix dashboard API requests with the plugin management route")
	}
	if bytes.Contains(js.Body, []byte("fetch(path.startsWith('/') ? path : API_ROOT + path")) {
		t.Fatal("JS asset still sends dashboard API requests to the site root")
	}
	for _, expected := range []string{
		"kpi-row-top",
		"kpi-row-bottom",
		"trendActiveDims",
		"cache_write",
		"cache_read",
		"hit_rate",
		"aria-pressed",
		"formatTokenCompact",
		"cacheHitRate",
		"MutationObserver",
	} {
		if !bytes.Contains(js.Body, []byte(expected)) {
			t.Fatalf("dashboard JS is missing visual contract %q", expected)
		}
	}
	for _, expected := range []string{
		"const AUTO_REFRESH_MS = 60_000;",
		"const FRONTEND_CACHE_TTL_MS = 60_000;",
		"const FRONTEND_CACHE_MAX_ITEMS = 32;",
		"const CACHE_PREFIX = 'usage-keeper-cache:';",
		"const CHINA_TIME_ZONE = 'Asia/Shanghai';",
		"const HEALTH_DAYS = 5;",
		"const HEALTH_SLOTS_PER_DAY = 96;",
		"Math.max(0, inputTokens - cacheReadTokens - cacheCreationTokens)",
		"document.visibilityState !== 'visible'",
		"state.page === 'settings'",
		"document.addEventListener('visibilitychange', handleVisibilityChange)",
		"state.loadController?.abort();",
		"await loadActivePage(false);",
		"window.setTimeout(runAutoRefresh",
		"loadActivePage(false);",
		"await yieldToBrowser();",
		"index === top.length - 1 ? circumference - offset",
		"function clearFrontendCache()",
		"sessionStorage.removeItem(oldestKey)",
		"Math.max(0, state.refreshDeadline - Date.now())",
	} {
		if !bytes.Contains(js.Body, []byte(expected)) {
			t.Fatalf("JS asset is missing guarded auto-refresh behavior %q", expected)
		}
	}
	if bytes.Contains(js.Body, []byte("window.setInterval(refresh, AUTO_REFRESH_MS)")) {
		t.Fatal("auto-refresh must be scheduled from request completion, not initialization time")
	}
	if bytes.Contains(js.Body, []byte("loadActivePage(true);\n    startAutoRefresh();")) {
		t.Fatal("initial dashboard load must reuse a fresh session cache")
	}
	if bytes.Contains(js.Body, []byte("Promise.all([\n      cached('/summary'")) {
		t.Fatal("overview refresh must not run all SQLite-backed reads concurrently")
	}
	for _, forbidden := range []string{"if (state.loading) return;", "await loadEventOptions(force);"} {
		if bytes.Contains(js.Body, []byte(forbidden)) {
			t.Fatalf("dashboard JS retains stale-request or repeated-analysis behavior %q", forbidden)
		}
	}
	for _, expected := range []string{"new AbortController()", "loadRequestID", "eventRequestID"} {
		if !bytes.Contains(js.Body, []byte(expected)) {
			t.Fatalf("dashboard JS is missing request race guard %q", expected)
		}
	}
	for _, expected := range []string{"interface-card", "progress-cell", "buildClampedSmoothPath", "trendActiveDims"} {
		if !bytes.Contains(js.Body, []byte(expected)) {
			t.Fatalf("dashboard JS is missing sample interaction %q", expected)
		}
	}
	css := serveDashboardAsset("/v0/resource/plugins/usage-keeper/style.css")
	if css.StatusCode != http.StatusOK || css.Headers.Get("Content-Type") != "text/css; charset=utf-8" {
		t.Fatalf("CSS asset response: %+v", css)
	}
	for _, expected := range []string{".interface-card", ".progress-cell", ".chart-host-interactive", ".floating-glass-tooltip"} {
		if !bytes.Contains(css.Body, []byte(expected)) {
			t.Fatalf("dashboard CSS is missing sample selector %q", expected)
		}
	}
	if relative := serveDashboardAsset("/app.js"); relative.StatusCode != http.StatusOK {
		t.Fatalf("relative asset route should be accepted: %+v", relative)
	}
	for name, body := range map[string][]byte{"HTML": root.Body, "JavaScript": js.Body, "CSS": css.Body} {
		for _, forbidden := range []string{"MOCK_DATA", "MOCK_TREND_POINTS", "Math.random", "示例演示中", "示例环境"} {
			if bytes.Contains(body, []byte(forbidden)) {
				t.Fatalf("production %s contains demo marker %q", name, forbidden)
			}
		}
	}
}
