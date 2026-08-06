# CPA Usage Keeper Plugin Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a native CLIProxyAPI usage-observer plugin with Keeper-style low-overhead storage and a five-page embedded dashboard.

**Architecture:** The Go `c-shared` plugin implements CLIProxyAPI ABI v1/schema v2 and declares only `usage_plugin` plus `management_api`. `usage.handle` decodes one completed usage record, performs a non-blocking enqueue, and returns; a single background worker batches SQLite WAL writes and updates minute/dimension rollups. Browser assets are embedded into the dynamic library and call exact plugin-owned Management API routes for aggregated views, paginated events, prices, and backup operations.

**Tech Stack:** Go 1.24, CGO `c-shared`, `github.com/mattn/go-sqlite3`, embedded HTML/CSS/vanilla JavaScript, Go tests, Node syntax checks.

## Global Constraints

- Work only in `E:/Cursor/cpa-usage-dashboard`; do not modify `cpa-usage-plugin-main`.
- Use the failed plugin only to confirm C ABI exports and JSON envelope names; do not copy its storage, response interception, stream interception, frontend, or business implementation.
- Follow `https://help.router-for.me/cn/plugin/development`, `usage-plugin`, and `management-api` plus current `CLIProxyAPI/sdk/pluginabi` and `sdk/pluginapi` sources.
- Declare only `usage_plugin: true` and `management_api: true`; never declare response or stream interceptors.
- `usage.handle` must never wait for SQLite or network I/O; a full queue drops the record and increments an observable counter.
- Do not start a separate HTTP server, poll CLIProxyAPI, access its database, use Redis, or load third-party browser scripts.
- Mask client API keys and sanitize failure text before persistence and API responses.
- The UI has exactly five primary pages: 总览, 分析, 接口, 事件, 设置.

---

## File Map

- `go/main.go`: C ABI structs and exported `cliproxy_plugin_*` entry points only.
- `go/abi.go`: ABI method dispatch, envelopes, registration, reconfiguration, and exact management route registration.
- `go/config.go`: plugin YAML configuration parsing, validation, and resolved storage path.
- `go/types.go`: usage input, compact event, aggregate, API response, price, and storage-status types.
- `go/plugin.go`: plugin lifecycle, bounded queue, counters, worker startup, flush, and shutdown.
- `go/storage.go`: SQLite schema/migrations, WAL settings, batched event/rollup writes, retention, and health status.
- `go/query.go`: summary, analysis, interfaces, upstream detail, filtered events, and CSV query functions.
- `go/pricing.go`: default/custom model prices and per-record cost calculation.
- `go/management.go`: authenticated API/resource request routing, validation, JSON/CSV responses, import/export.
- `go/dashboard/index.html`: accessible five-page application shell.
- `go/dashboard/style.css`: responsive CPA-compatible visual system.
- `go/dashboard/app.js`: state, navigation, filters, SVG charts, tables, pagination, export, settings, and error/loading states.
- `go/*_test.go`: ABI, queue, storage, query, pricing, and management contract tests.
- `README.md`, `config.example.yaml`, `registry.json`, `scripts/build.ps1`, `.github/workflows/release.yml`: build, install, configuration, packaging, and release metadata.

### Task 1: ABI Skeleton And Registration

**Files:**
- Create: `go/go.mod`
- Create: `go/main.go`
- Create: `go/abi.go`
- Create: `go/types.go`
- Create: `go/abi_test.go`

**Interfaces:**
- Consumes: CLIProxyAPI ABI methods `plugin.register`, `plugin.reconfigure`, `usage.handle`, `management.register`, `management.handle`.
- Produces: `handleMethod(method string, request []byte) ([]byte, error)`, `okEnvelope(any) []byte`, and `pluginRegisterResponse` with schema version `2`.

- [ ] **Step 1: Write ABI contract tests**

  Assert registration metadata, schema version, the exact two capability flags, unknown-method errors, and management route/resource declarations.

- [ ] **Step 2: Run the ABI tests and verify the empty project fails**

  Run: `go test ./...` from `go/`.
  Expected: FAIL because ABI handlers are not defined.

- [ ] **Step 3: Implement the C ABI and JSON envelope dispatcher**

  Export `cliproxy_plugin_init`, `cliproxyPluginCall`, `cliproxyPluginFree`, and `cliproxyPluginShutdown`; allocate response bytes with `C.CBytes` and release them with `C.free`.

- [ ] **Step 4: Implement minimal plugin registration**

  Return `schema_version: 2`, metadata/config fields, and only `usage_plugin` plus `management_api` capabilities.

- [ ] **Step 5: Run focused tests**

  Run: `go test ./... -run 'Test(Register|Envelope|ManagementRegistration)'`.
  Expected: PASS.

### Task 2: Non-Blocking Ingestion And SQLite Batch Storage

**Files:**
- Create: `go/config.go`
- Create: `go/plugin.go`
- Create: `go/storage.go`
- Create: `go/usage_test.go`
- Create: `go/storage_test.go`

**Interfaces:**
- Consumes: ABI-decoded `usageRecord` and resolved `runtimeConfig`.
- Produces: `pluginRuntime.enqueue(usageRecord)`, `eventStore.writeBatch([]usageEvent)`, `eventStore.status() storageStatus`, and bounded `flush`/`close` operations.

- [ ] **Step 1: Write queue and storage tests**

  Cover immediate enqueue, full-queue drop counting, token field mapping, API-key masking/hashing, SQLite event persistence, atomic rollup updates, WAL mode, retention, flush, and clean shutdown.

- [ ] **Step 2: Run focused tests and confirm failures**

  Run: `go test ./... -run 'Test(Enqueue|Usage|Storage|Retention)'`.
  Expected: FAIL because runtime and store are not defined.

- [ ] **Step 3: Implement compact usage decoding and bounded ingestion**

  Decode only official `UsageRecord` fields, normalize nanosecond durations, sanitize failure text, mask/hash API keys, enqueue with `select { case queue <- event: default: }`, and return the ABI response immediately.

- [ ] **Step 4: Implement SQLite schema and one writer**

  Create `usage_events`, `usage_minute_rollups`, `usage_dimension_rollups`, `model_prices`, and `plugin_settings`; enable WAL, `busy_timeout`, `synchronous=NORMAL`, and batch event plus rollup updates in short transactions.

- [ ] **Step 5: Implement periodic flush and retention**

  Flush at `batch_size` or `flush_interval_ms`, run retention at startup and once daily, and drain the queue within a bounded shutdown window.

- [ ] **Step 6: Run queue/storage tests and race checks**

  Run: `go test -race ./... -run 'Test(Enqueue|Usage|Storage|Retention)'`.
  Expected: PASS with no race report.

### Task 3: Aggregated Queries, Events, Prices, And Backup

**Files:**
- Create: `go/query.go`
- Create: `go/pricing.go`
- Create: `go/query_test.go`
- Create: `go/pricing_test.go`

**Interfaces:**
- Consumes: rollup/event/model price tables.
- Produces: `querySummary`, `queryAnalysis`, `queryInterfaces`, `queryUpstreamDetail`, `queryEvents`, `exportEventsCSV`, `listPrices`, `replacePrices`, `exportBackup`, and `importBackup`.

- [ ] **Step 1: Write deterministic query fixtures**

  Insert mixed providers, models, API keys, successes/failures, latency, TTFT, token parts, and price overrides across several minute buckets.

- [ ] **Step 2: Run query tests and confirm failures**

  Run: `go test ./... -run 'Test(Query|Price|Export|Import)'`.
  Expected: FAIL because query functions are not defined.

- [ ] **Step 3: Implement rollup-backed overview and analysis**

  Return KPIs, health buckets, usage/cost trend, provider/model/source/API-key distributions, token composition, and model statistics without scanning event payloads.

- [ ] **Step 4: Implement interfaces and paginated events**

  Support range, provider, model, API-key hash, status, keyword, page, and page-size filters; cap page size at 200 and return total/page metadata.

- [ ] **Step 5: Implement pricing and portable backup**

  Store prices per million tokens for input/output/cache-read/cache-write/reasoning, calculate costs consistently, export versioned JSON, and import in one validated transaction.

- [ ] **Step 6: Run query and pricing tests**

  Run: `go test ./... -run 'Test(Query|Price|Export|Import)'`.
  Expected: PASS.

### Task 4: Management API And Embedded Five-Page UI

**Files:**
- Create: `go/management.go`
- Create: `go/management_test.go`
- Create: `go/dashboard/index.html`
- Create: `go/dashboard/style.css`
- Create: `go/dashboard/app.js`

**Interfaces:**
- Consumes: query/pricing/backup functions and CLIProxyAPI `ManagementRequest`.
- Produces: exact resource routes for `/`, `/app.js`, `/style.css`; exact authenticated API routes under `/plugins/usage-keeper/*`; complete five-page browser UI.

- [ ] **Step 1: Write management handler tests**

  Verify content types, no-store headers, route/method validation, query parsing, JSON bodies, CSV attachment, price mutation, backup import/export, and errors that never expose internal paths or SQL.

- [ ] **Step 2: Run management tests and confirm failures**

  Run: `go test ./... -run TestManagement`.
  Expected: FAIL because handlers/assets are not defined.

- [ ] **Step 3: Implement exact resource and API routes**

  Serve only embedded same-origin assets from resource routes and keep settings/import mutations behind plugin Management API authentication.

- [ ] **Step 4: Build the five-page shell**

  Add 总览 with KPI/health/trend, 分析 with four distributions/token composition/model table, 接口 with API-key/upstream/detail views, 事件 with filters/pagination/export, and 设置 with prices/storage/import/export.

- [ ] **Step 5: Implement lightweight charts and interaction**

  Use vanilla JavaScript and inline SVG primitives, debounce filters, abort stale fetches, cache unchanged reference data, respect reduced motion, and render empty/loading/error states without layout shifts.

- [ ] **Step 6: Run backend and JavaScript validation**

  Run: `go test ./...` and `node --check dashboard/app.js` from `go/`.
  Expected: PASS.

### Task 5: Packaging, Documentation, And End-To-End Verification

**Files:**
- Create: `.gitignore`
- Create: `README.md`
- Create: `config.example.yaml`
- Create: `registry.json`
- Create: `scripts/build.ps1`
- Create: `.github/workflows/release.yml`

**Interfaces:**
- Consumes: completed plugin source and CLIProxyAPI plugin discovery/release conventions.
- Produces: reproducible local build, platform zip assets, `checksums.txt`, installation/configuration guide, and verification checklist.

- [ ] **Step 1: Document build and installation**

  Include CGO compiler prerequisites, `plugins/<GOOS>/<GOARCH>` placement, plugin ID derived from filename, `plugins.enabled`, per-plugin configuration, resource URL, management status checks, retention, and backup behavior.

- [ ] **Step 2: Add build/release automation**

  Build `usage-keeper.dll`/`.so`/`.dylib`, package `<pluginID>_<version>_<goos>_<goarch>.zip` with the dynamic library at zip root, and generate sha256 checksums.

- [ ] **Step 3: Format and run the full verification suite**

  Run: `gofmt -w *.go`, `go test -race ./...`, `go vet ./...`, `node --check dashboard/app.js`, and `go build -buildmode=c-shared -o usage-keeper.dll .` on Windows.
  Expected: all commands succeed.

- [ ] **Step 4: Inspect exports and artifact contents**

  Confirm the dynamic library exports `cliproxy_plugin_init`, the package zip contains the library at its root, and the plugin registers only the intended capabilities.

- [ ] **Step 5: Final scope review**

  Confirm all five pages and requested contents exist, every data query is aggregated or paginated, callback overflow is observable, no response interceptor is registered, and `cpa-usage-plugin-main` remains untouched.
