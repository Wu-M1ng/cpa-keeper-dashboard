package main

import (
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegisterDeclaresOnlyLowOverheadCapabilities(t *testing.T) {
	shutdownRuntime()
	tempDir := t.TempDir()
	t.Cleanup(shutdownRuntime)

	configYAML := []byte("storage_path: " + filepath.ToSlash(filepath.Join(tempDir, "usage.db")) + "\n")
	request, err := json.Marshal(map[string]string{
		"config_yaml": base64.StdEncoding.EncodeToString(configYAML),
	})
	if err != nil {
		t.Fatal(err)
	}

	raw, err := handleMethod("plugin.register", request)
	if err != nil {
		t.Fatalf("register returned error: %v", err)
	}

	var outer envelope
	if err := json.Unmarshal(raw, &outer); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if !outer.OK {
		t.Fatalf("register envelope was not OK: %s", raw)
	}

	var registration pluginRegisterResponse
	if err := json.Unmarshal(outer.Result, &registration); err != nil {
		t.Fatalf("decode registration: %v", err)
	}
	if registration.SchemaVersion != schemaVersion {
		t.Fatalf("schema version = %d, want %d", registration.SchemaVersion, schemaVersion)
	}
	if registration.Metadata.GitHubRepository != "https://github.com/Wu-M1ng/cpa-keeper-dashboard" {
		t.Fatalf("repository = %q", registration.Metadata.GitHubRepository)
	}
	if !registration.Capabilities.UsagePlugin || !registration.Capabilities.ManagementAPI {
		t.Fatalf("required capabilities missing: %+v", registration.Capabilities)
	}

	encoded, err := json.Marshal(registration.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	var capabilityKeys map[string]bool
	if err := json.Unmarshal(encoded, &capabilityKeys); err != nil {
		t.Fatal(err)
	}
	if len(capabilityKeys) != 2 {
		t.Fatalf("capabilities = %s, want only usage_plugin and management_api", encoded)
	}
	if capabilityKeys["response_interceptor"] || capabilityKeys["response_stream_interceptor"] {
		t.Fatalf("hot-path interceptors must not be registered: %s", encoded)
	}
}

func TestManagementRegistrationUsesExactRoutes(t *testing.T) {
	raw, err := handleMethod("management.register", nil)
	if err != nil {
		t.Fatal(err)
	}
	var outer envelope
	if err := json.Unmarshal(raw, &outer); err != nil {
		t.Fatal(err)
	}
	var registration managementRegistration
	if err := json.Unmarshal(outer.Result, &registration); err != nil {
		t.Fatal(err)
	}
	if len(registration.Resources) != 3 {
		t.Fatalf("resources = %d, want 3", len(registration.Resources))
	}
	if registration.Resources[0].Path != "/" || registration.Resources[0].Menu != "用量 Keeper" {
		t.Fatalf("unexpected menu resource: %+v", registration.Resources[0])
	}
	if len(registration.Routes) < 10 {
		t.Fatalf("management routes = %d, want complete dashboard API", len(registration.Routes))
	}
	for _, route := range registration.Routes {
		if route.Path == "" || route.Method == "" {
			t.Fatalf("route must be exact: %+v", route)
		}
	}
}

func TestUnknownMethodReturnsErrorEnvelope(t *testing.T) {
	raw, err := handleMethod("response.intercept_after", nil)
	if err != nil {
		t.Fatalf("dispatcher should encode method errors: %v", err)
	}
	var outer envelope
	if err := json.Unmarshal(raw, &outer); err != nil {
		t.Fatal(err)
	}
	if outer.OK || outer.Error == nil || outer.Error.Code != "unknown_method" {
		t.Fatalf("unexpected envelope: %s", raw)
	}
}

func TestEnvelopeMarshalsResultOnce(t *testing.T) {
	raw := okEnvelope(map[string]int{"value": 7})
	var decoded struct {
		OK     bool `json:"ok"`
		Result struct {
			Value int `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.OK || decoded.Result.Value != 7 {
		t.Fatalf("unexpected envelope: %s", raw)
	}
}

func TestManagementWireUsesOfficialFieldNames(t *testing.T) {
	requestJSON := []byte(`{"method":"GET","path":"/v0/management/plugins/usage-keeper/summary","query":{"range":["24h"]},"body":""}`)
	var request managementRequest
	if err := json.Unmarshal(requestJSON, &request); err != nil {
		t.Fatal(err)
	}
	if request.Method != "GET" || request.Path == "" || request.Query.Get("range") != "24h" {
		t.Fatalf("official management request was not decoded: %+v", request)
	}

	raw, err := json.Marshal(managementResponse{StatusCode: 200, Body: []byte(`{"ok":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(raw)
	if !strings.Contains(encoded, `"status_code":200`) || !strings.Contains(encoded, `"body":"eyJvayI6dHJ1ZX0="`) {
		t.Fatalf("management response did not use ABI field names: %s", encoded)
	}
	if strings.Contains(encoded, `"StatusCode"`) || strings.Contains(encoded, `"Body"`) {
		t.Fatalf("management response still uses legacy field names: %s", encoded)
	}

	raw, err = handleMethod("management.register", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"routes"`) || !strings.Contains(string(raw), `"resources"`) {
		t.Fatalf("management registration did not use official field names: %s", raw)
	}
}
