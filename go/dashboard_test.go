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
	for _, page := range []string{"overview", "interfaces", "settings"} {
		if !bytes.Contains(root.Body, []byte(`data-page="`+page+`"`)) {
			t.Fatalf("dashboard is missing page %q", page)
		}
	}
	if bytes.Count(root.Body, []byte(`data-page="overview"`)) != 3 {
		t.Fatal("overview must contain summary, analysis, and events sections")
	}
	if bytes.Contains(root.Body, []byte(`data-page-target="analysis"`)) || bytes.Contains(root.Body, []byte(`data-page-target="events"`)) {
		t.Fatal("analysis and events must not remain as separate navigation pages")
	}
	for _, expected := range []string{
		"服务健康监测",
		"最近5天，15分钟一个网格",
		`id="health-success-count"`,
		`id="health-failure-count"`,
		"时间", "模型", "状态", "用时 / 首字", "非缓存输入",
		"输出", "思考", "缓存命中", "缓存创建", "总计",
	} {
		if !bytes.Contains(root.Body, []byte(expected)) {
			t.Fatalf("dashboard HTML is missing %q", expected)
		}
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
		"const AUTO_REFRESH_MS = 30_000;",
		"const HEALTH_DAYS = 5;",
		"const HEALTH_SLOTS_PER_DAY = 96;",
		"Math.max(0, inputTokens - cacheReadTokens - cacheCreationTokens)",
		"document.visibilityState !== 'visible'",
		"state.page === 'settings'",
		"document.addEventListener('visibilitychange', handleVisibilityChange)",
		"window.setTimeout(runAutoRefresh",
	} {
		if !bytes.Contains(js.Body, []byte(expected)) {
			t.Fatalf("JS asset is missing guarded auto-refresh behavior %q", expected)
		}
	}
	if bytes.Contains(js.Body, []byte("window.setInterval(refresh, AUTO_REFRESH_MS)")) {
		t.Fatal("auto-refresh must be scheduled from request completion, not initialization time")
	}
	css := serveDashboardAsset("/v0/resource/plugins/usage-keeper/style.css")
	if css.StatusCode != http.StatusOK || css.Headers.Get("Content-Type") != "text/css; charset=utf-8" {
		t.Fatalf("CSS asset response: %+v", css)
	}
	if relative := serveDashboardAsset("/app.js"); relative.StatusCode != http.StatusOK {
		t.Fatalf("relative asset route should be accepted: %+v", relative)
	}
}
