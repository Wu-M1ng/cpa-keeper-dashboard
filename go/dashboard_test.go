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
	for _, page := range []string{"overview", "analysis", "interfaces", "events", "settings"} {
		if !bytes.Contains(root.Body, []byte(`data-page="`+page+`"`)) {
			t.Fatalf("dashboard is missing page %q", page)
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
	css := serveDashboardAsset("/v0/resource/plugins/usage-keeper/style.css")
	if css.StatusCode != http.StatusOK || css.Headers.Get("Content-Type") != "text/css; charset=utf-8" {
		t.Fatalf("CSS asset response: %+v", css)
	}
	if relative := serveDashboardAsset("/app.js"); relative.StatusCode != http.StatusOK {
		t.Fatalf("relative asset route should be accepted: %+v", relative)
	}
}
