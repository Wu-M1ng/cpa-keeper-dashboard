package main

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestPrivacyFailureRedaction(t *testing.T) {
	tests := []struct {
		name, input   string
		secrets, keep []string
	}{
		{"email", "account@example.com: quota exceeded", []string{"account@example.com"}, []string{"acc***om", "quota exceeded"}},
		{"authorization", "Authorization: Bearer private-bearer; retry after 30s", []string{"private-bearer"}, []string{"retry after 30s"}},
		{"basic", "upstream rejected Basic dXNlcjpwYXNzd29yZA== (401)", []string{"dXNlcjpwYXNzd29yZA=="}, []string{"401"}},
		{"named fields", "api_key=plain-secret access_token=access-secret refreshToken=refresh-secret x-api-key: header-secret", []string{"plain-secret", "access-secret", "refresh-secret", "header-secret"}, nil},
		{"quoted fields", `password='secret with spaces' client_secret="quoted secret"; rate limited`, []string{"secret with spaces", "quoted secret"}, []string{"rate limited"}},
		{"json", `{"error":{"message":"account@example.com quota exceeded","api_key":"private-json","refresh_token":"refresh-json"},"status":429}`, []string{"account@example.com", "private-json", "refresh-json"}, []string{"quota exceeded", "429"}},
		{"json escapes", `{"error":{"access\u005ftoken":"escaped-secret","message":"account\u0040example.com"}}`, []string{"escaped-secret", "account@example.com", `account\u0040example.com`}, []string{"acc***om"}},
		{"escaped text", `error: {\"api_key\":\"escaped-text-secret\"} account\u0040example.com`, []string{"escaped-text-secret", "account@example.com"}, nil},
		{"encoded", "account%2540example.com api_key%3Dencoded-secret", []string{"account%2540example.com", "account@example.com", "encoded-secret"}, []string{"acc***om"}},
		{"html", "account&amp;#64;example.com Authorization: Bearer html-secret", []string{"account", "html-secret"}, []string{"acc***om"}},
		{"raw key", "incorrect API key sk-proj-private_123456: quota exceeded", []string{"sk-proj-private_123456"}, []string{"quota exceeded"}},
		{"jwt", "invalid token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.signature", []string{"eyJhbGciOiJIUzI1NiJ9", "signature"}, []string{"invalid token"}},
		{"url", "request https://user:pass@proxy.local/v1/account%40example.com?api_key=url-secret failed (502)", []string{"user:pass", "account@example.com", "url-secret", "api_key="}, []string{"https://proxy.local/v1/acc***om", "502"}},
		{"cookie", "Cookie: session=cookie-secret; other=other-secret\nupstream timeout", []string{"cookie-secret", "other-secret"}, []string{"upstream timeout"}},
		{"benign", "model gpt-5.6: invalid api key; token count exceeded (429)", nil, []string{"model gpt-5.6: invalid api key; token count exceeded (429)"}},
		{"boundary", strings.Repeat("x", 495) + " account@example.com Bearer boundary-secret", []string{"account@", "boundary-secret"}, nil},
		{"control characters", "account@exam\x00ple.com Bear\x00er control-secret", []string{"account@example.com", "control-secret"}, []string{"acc***om"}},
		{"zero width", "account@exam\u200bple.com Bear\u200ber invisible-secret", []string{"account", "invisible-secret"}, []string{"acc***om"}},
		{"line boundaries", "upstream timeout\nretry in 30s", nil, []string{"upstream timeout", "retry in 30s"}},
		{"split identifier", "account@exam\nple.com Bearer\nline-secret", []string{"account@example.com", "line-secret"}, nil},
		{"nested credentials", `{"error":{"credentials":{"access_token":"nested-secret"}},"code":"rate_limit"}`, []string{"nested-secret"}, []string{"rate_limit"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeFailure(tt.input)
			assertPrivacyText(t, got, tt.secrets, tt.keep)
			if len(got) > 512 || !utf8.ValidString(got) {
				t.Fatalf("invalid bounded failure text: %q", got)
			}
			if twice := sanitizeFailure(got); twice != got {
				t.Fatalf("redaction must be idempotent: %q -> %q", got, twice)
			}
			if json.Valid([]byte(tt.input)) && !json.Valid([]byte(got)) {
				t.Fatalf("short JSON diagnostic became invalid: %q", got)
			}
		})
	}
}

func TestPrivacyEndpointRedaction(t *testing.T) {
	tests := []struct{ input, want string }{
		{"", ""},
		{"/v1/chat/completions?api_key=secret#fragment", "/v1/chat/completions"},
		{"https://proxy.local/v1/chat/completions", "https://proxy.local/v1/chat/completions"},
		{"https://user:password@proxy.local/v1/account%40example.com/sk-proj-private/chat?key=secret", "https://proxy.local/v1/acc***om/***/chat"},
		{"/v1/account%2540example.com/chat", "/v1/acc***om/chat"},
		{"/v1/api_key/path-secret/chat", "/v1/api_key/***/chat"},
		{"/v1/access-token/path-secret/chat", "/v1/access-token/***/chat"},
		{"/v1/account@example.com/chat", "/v1/acc***om/chat"},
		{"/v1/%ZZ?api_key=secret", "***"},
		{"https://[broken-host/account@example.com?api_key=secret", "***"},
		{"mailto:account@example.com", "***"},
		{"?api_key=secret", ""},
		{"/v1/acc***om/***/chat", "/v1/acc***om/***/chat"},
		{"/v1/account@exam%00ple.com/chat", "/v1/acc***om/chat"},
		{"/v1/%73%6b-private-value/chat", "/v1/***/chat"},
		{"/v1/account@example.com/" + strings.Repeat("x", 500), ("/v1/acc***om/" + strings.Repeat("x", 500))[:256]},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeEndpoint(tt.input)
			if got != tt.want {
				t.Fatalf("endpoint = %q, want %q", got, tt.want)
			}
			if twice := sanitizeEndpoint(got); twice != got {
				t.Fatalf("endpoint redaction must be idempotent: %q -> %q", got, twice)
			}
		})
	}
}

func TestPrivacyBoundsOversizedInputs(t *testing.T) {
	input := strings.Repeat("x", 17*1024) + " account@example.com Bearer private-secret"
	for name, sanitize := range map[string]func(string) string{"failure": sanitizeFailure, "endpoint": sanitizeEndpoint} {
		t.Run(name, func(t *testing.T) {
			got := sanitize(input)
			if len(got) > 512 || !utf8.ValidString(got) {
				t.Fatalf("oversized input not bounded: %q", got)
			}
			assertPrivacyText(t, got, []string{"account@example.com", "private-secret"}, nil)
		})
	}
}

const privacyFixtureEndpoint = "https://user:password@proxy.local/v1/account%40example.com/sk-live-pathsecret/chat?api_key=query-secret"
const privacyFixtureFailure = "account@example.com api_key=body-secret Authorization: Bearer bearer-secret quota exceeded (429)"

func assertPrivacyText(t *testing.T, got string, secrets, keep []string) {
	t.Helper()
	for _, secret := range secrets {
		if strings.Contains(got, secret) {
			t.Errorf("sensitive value %q remains in %q", secret, got)
		}
	}
	for _, expected := range keep {
		if !strings.Contains(got, expected) {
			t.Errorf("diagnostic value %q missing from %q", expected, got)
		}
	}
}

func assertPrivateEvent(t *testing.T, event usageEvent) {
	t.Helper()
	assertPrivacyText(t, event.Endpoint+" "+event.Failure,
		[]string{"account@example.com", "account%40example.com", "pathsecret", "query-secret", "body-secret", "bearer-secret", "user:password"},
		[]string{"https://proxy.local/v1/acc***om/***/chat", "quota exceeded", "429"})
}

func TestPrivacyRedactsBeforeQueueAndStorage(t *testing.T) {
	now := time.Now().UTC()
	event := compactUsageRecord(usageRecord{
		Provider: "codex", Model: "gpt-5.6", RequestedAt: now,
		Source: "account@example.com", Endpoint: privacyFixtureEndpoint,
		Failure: usageFailure{StatusCode: 429, Body: privacyFixtureFailure},
	}, "test-salt")
	assertPrivateEvent(t, event)
	if event.Source != "codex / acc***om" {
		t.Fatalf("existing account label changed: %q", event.Source)
	}
	store := openTestStore(t)
	raw := fixtureEvent(now, "gpt-5.6", true, 10, 5)
	raw.Endpoint, raw.Failure = privacyFixtureEndpoint, privacyFixtureFailure
	if err := store.writeBatch([]usageEvent{raw}); err != nil {
		t.Fatal(err)
	}
	stored, err := scanEvent(store.db.QueryRow("SELECT " + eventColumns + " FROM usage_events LIMIT 1"))
	if err != nil {
		t.Fatal(err)
	}
	assertPrivateEvent(t, stored)
}

func TestPrivacyProtectsLegacyEventsAcrossOutputs(t *testing.T) {
	store, now := seededQueryStore(t)
	// 模拟旧版本已经写入的原文，避免测试被入库清洗提前掩盖。
	if _, err := store.db.Exec("UPDATE usage_events SET endpoint = ?, failure = ?", privacyFixtureEndpoint, privacyFixtureFailure); err != nil {
		t.Fatal(err)
	}
	filter := eventFilter{FromMS: now.Add(-24 * time.Hour).UnixMilli(), ToMS: now.UnixMilli(), Page: 1, PageSize: 25}
	page, err := queryEvents(t.Context(), store, filter)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 3 {
		t.Fatalf("unexpected event count: %d", len(page.Events))
	}
	for _, event := range page.Events {
		assertPrivateEvent(t, event)
	}
	var upstreamKey string
	if err := store.db.QueryRow("SELECT upstream_key FROM usage_events LIMIT 1").Scan(&upstreamKey); err != nil {
		t.Fatal(err)
	}
	detail, err := queryUpstreamDetail(t.Context(), store, upstreamKey, url.Values{"range": {"24h"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.RecentEvents) == 0 {
		t.Fatal("upstream fixture has no events")
	}
	for _, event := range detail.RecentEvents {
		assertPrivateEvent(t, event)
	}
	csv, err := exportEventsCSV(t.Context(), store, filter, 100)
	if err != nil {
		t.Fatal(err)
	}
	assertPrivacyText(t, string(csv), []string{"account@example.com", "account%40example.com", "pathsecret", "query-secret", "body-secret", "bearer-secret", "user:password"}, []string{"quota exceeded", "acc***om"})
	payload, err := exportBackup(t.Context(), store, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range payload.Events {
		assertPrivateEvent(t, event)
	}
	// 导入旧备份也必须在存储之前脱敏。
	for i := range payload.Events {
		payload.Events[i].Endpoint, payload.Events[i].Failure = privacyFixtureEndpoint, privacyFixtureFailure
	}
	target := openTestStore(t)
	if _, err := importBackup(t.Context(), target, payload); err != nil {
		t.Fatal(err)
	}
	rows, err := target.db.Query("SELECT " + eventColumns + " FROM usage_events")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			t.Fatal(err)
		}
		assertPrivateEvent(t, event)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestPrivacyRejectsSpoofedMaskedLabels(t *testing.T) {
	for _, label := range []string{"codex / account@example.com***", "codex / ***sk-live-private-value"} {
		got := maskedProviderCredentialDisplay("codex", label, "fallback")
		assertPrivacyText(t, got, []string{"account@example.com", "sk-live-private-value"}, nil)
		event := usageEvent{Provider: "codex", Source: label, UpstreamLabel: label}
		normalizeEventForStorage(&event, "test-salt")
		assertPrivacyText(t, event.Source, []string{"account@example.com", "sk-live-private-value"}, nil)
	}
}
