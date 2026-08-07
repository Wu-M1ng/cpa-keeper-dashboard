# Strict Sample Dashboard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the production plugin dashboard visually and interactively match `go/dashboard/参考样例1.html` while retaining real CLIProxyAPI data, authentication, auto-refresh, privacy guarantees, and low overhead.

**Architecture:** Use the sample DOM and CSS as the desktop source of truth, but port only its presentation and interaction code. Extend the existing summary read model with real rollup-derived KPI and trend fields, persist a sanitized endpoint path for the sample's event table, and keep every management request under `/v0/management/plugins/usage-keeper`. No mock data, random values, external assets, frontend framework, or new request-path I/O will be introduced.

**Tech Stack:** Go 1.24, SQLite WAL rollups, embedded HTML/CSS/vanilla JavaScript, Go tests.

## Global Constraints

- Preserve the non-blocking bounded usage queue and existing SQLite batch writer.
- Preserve Management Key authentication and the 30-second visibility-aware auto-refresh.
- Preserve Provider credential, client key, account, failure-text, and endpoint privacy controls.
- Preserve backup/restore, storage settings, model pricing, CSV export, pagination, and upstream detail behavior even where the sample omits those controls.
- Use the sample's desktop layout, typography, spacing, colors, card hierarchy, charts, tooltips, progress bars, and table presentation as the visual source of truth.
- Correct the sample's `padding: 99px` typo to `9px` and add the missing 980px, 680px, and 430px responsive behavior.
- Target release version `1.0.10`.

---

### Task 1: Lock the Production Data Contract

**Files:**
- Modify: `go/query_test.go`
- Modify: `go/storage_test.go`
- Modify: `go/usage_test.go`
- Modify: `go/dashboard_test.go`

**Interfaces:**
- Consumes: current `summaryResponse`, `usageEvent`, and embedded dashboard assets.
- Produces: failing tests for the exact real-data fields and sample structure required by later tasks.

- [ ] Extend `TestQuerySummaryUsesRollups` to require KPI fields `avg_requests_daily`, `avg_tokens_daily`, `avg_cost_daily`, `rpm`, `tpm`, `cache_rate`, `cache_read_tokens`, `cache_write_tokens`, `reasoning_tokens`, and `input_tokens`.
- [ ] Require every `trendPoint` to expose `input`, `output`, `cache_write`, `cache_read`, `hit_rate`, `actual_cost`, and `standard_cost` in addition to the existing fields.
- [ ] Add a storage migration test which opens a database whose `usage_events` table lacks `endpoint`, initializes the store, and verifies that the column is added without losing rows.
- [ ] Add a usage normalization test requiring `/v1/chat/completions?api_key=secret#fragment` to be persisted and returned only as `/v1/chat/completions`.
- [ ] Extend `TestDashboardAssetsAreEmbeddedAndSelfContained` to require sample classes and controls: `kpi-section`, `trend-legend`, `chart-host-interactive`, `event-reset`, `floating-tooltip`, `interface-card`, and `progress-cell`.
- [ ] Assert that production assets contain no `MOCK_DATA`, `MOCK_TREND_POINTS`, `Math.random`, `示例演示中`, or `示例环境`.
- [ ] Run `go test ./... -count=1` and record the expected failures before implementation.

### Task 2: Extend Rollup-Derived Summary Data

**Files:**
- Modify: `go/query.go`
- Modify: `go/pricing.go`
- Test: `go/query_test.go`
- Test: `go/pricing_test.go`

**Interfaces:**
- Consumes: the existing `queryAggregateRows` result and model price map.
- Produces: the real KPI and five-dimensional trend JSON used by the sample renderer.

- [ ] Add the required JSON fields to `kpiStats` and `trendPoint` without removing the existing `requests`, `tokens`, or `cost_usd` fields.
- [ ] Accumulate input, output, cache-read, cache-write, and reasoning totals from the already-loaded aggregate rows; do not issue an event-table query.
- [ ] Calculate `cache_rate` as `cache_read_tokens / input_tokens`, returning `0` when input is zero.
- [ ] Calculate RPM and TPM from the selected range duration; for `all`, use the first and last non-empty aggregate buckets, with a minimum duration of one interval.
- [ ] Calculate daily averages from the same effective duration, with a minimum divisor of one day, and expose the active range label so the KPI badge is not hard-coded to seven days.
- [ ] Add `calculateStandardCost(tokens, price)` which prices cache-read input at the normal input rate while retaining output and reasoning pricing; use it only in management-query calculations.
- [ ] Fill each trend bucket's token dimensions, hit rate, actual cost, and standard cost while preserving dense buckets.
- [ ] Run `go test ./... -run 'TestQuerySummary|TestCalculate' -count=1`.

### Task 3: Persist a Sanitized Endpoint for the Sample Event Table

**Files:**
- Modify: `go/types.go`
- Modify: `go/plugin.go`
- Modify: `go/storage.go`
- Modify: `go/query.go`
- Modify: `go/backup.go`
- Test: `go/storage_test.go`
- Test: `go/query_test.go`
- Test: `go/usage_test.go`

**Interfaces:**
- Consumes: `usageRecord.Endpoint` from the plugin ABI callback.
- Produces: `usageEvent.Endpoint string` serialized as `endpoint`, containing only a bounded URL path.

- [ ] Add `Endpoint string \`json:"endpoint,omitempty"\`` to `usageEvent` and populate it in `compactUsageRecord`.
- [ ] Implement `sanitizeEndpoint` using `net/url`: retain only the path, discard query and fragment, normalize empty values to `""`, and cap the stored path at 256 bytes.
- [ ] Add `endpoint TEXT NOT NULL DEFAULT ''` to new `usage_events` tables.
- [ ] During store initialization, inspect `PRAGMA table_info(usage_events)` and run `ALTER TABLE usage_events ADD COLUMN endpoint TEXT NOT NULL DEFAULT ''` only when needed.
- [ ] Update `insertEventSQL`, `eventColumns`, `writeEventsTx`, and `scanEvent` in the same column order.
- [ ] Include `endpoint` in CSV export; backup JSON inherits it from `usageEvent` and remains backward compatible because the field is optional.
- [ ] Keep internal SQLite schema migration independent from the plugin ABI `schemaVersion`.
- [ ] Run storage, usage, query, backup, and privacy tests.

### Task 4: Replace the Production DOM with the Sample Layout

**Files:**
- Modify: `go/dashboard/index.html`
- Test: `go/dashboard_test.go`

**Interfaces:**
- Consumes: existing production element IDs used by `app.js`.
- Produces: the sample's three-page layout with all production controls retained.

- [ ] Add the sample icon symbols `i-diamond`, `i-dollar`, `i-percent`, `i-clock`, `i-trend-up`, and `i-eye`.
- [ ] Replace `kpi-grid` with `kpi-section` and use the sample's trend legend buttons and `chart-host-interactive` host.
- [ ] Reorder the overview exactly as the sample: KPI, trend, health, distributions, token composition, model statistics, runtime strip, filters, request details.
- [ ] Add the reset button and floating tooltip host.
- [ ] Use the sample's 12-column event header: time, model/channel, reasoning effort, endpoint, status, latency/TTFT, non-cached input, output, reasoning, cache hit, cache creation, total token.
- [ ] Use the sample's interface summary, API-key table, and upstream table headings, including progress and detail columns.
- [ ] Keep the production auth dialog, backup/import band, management connection band, file input, and all current IDs after the sample's settings sections.
- [ ] Add `scope="col"`, dialog semantics, complete icon `aria-label` values, and no external URLs.

### Task 5: Port the Sample Styles Exactly and Complete Responsive States

**Files:**
- Modify: `go/dashboard/style.css`
- Test: `go/dashboard_test.go`

**Interfaces:**
- Consumes: the Task 4 DOM classes.
- Produces: the sample visual system in light, dark, tablet, and mobile layouts.

- [ ] Use the sample variables, shell, header, navigation, KPI panels, sparkline containers, interactive trend chart, health grid, distribution, tables, interface cards, progress bars, drawer, and tooltip styles as the baseline.
- [ ] Correct `.settings-title > svg` padding from `99px` to `9px`.
- [ ] Preserve production styles absent from the sample: auth dialog, skeleton and empty states, backup/connection bands, detailed upstream sections, toasts, and reduced-motion behavior.
- [ ] At 1320px, keep the sample's one-column top KPI row, two-column bottom KPI row, two-column distributions, four-column filters, and two-column interface summary.
- [ ] At 980px, stack header controls and use three-column runtime/token summaries.
- [ ] At 680px, switch KPI and interface cards to one column, retain horizontal table and health-grid scrolling, and prevent tooltip overflow.
- [ ] At 430px, use one-column filter controls and keep all text, buttons, and navigation inside the viewport.
- [ ] Add asset tests for the key selectors and verify CSS brace balance.

### Task 6: Port Sample Interactions onto Real API Data

**Files:**
- Modify: `go/dashboard/app.js`
- Test: `go/dashboard_test.go`

**Interfaces:**
- Consumes: real `/summary`, `/analysis`, `/events`, `/interfaces`, `/upstream`, `/settings`, and `/prices` responses.
- Produces: sample-equivalent interactions without mock data.

- [ ] Add `state.trendActiveDims` and bind delegated legend toggles without replacing the existing management-key, cache, page, loading, or auto-refresh state.
- [ ] Render the sample's seven KPI panels; generate each sparkline from real trend points instead of static presets.
- [ ] Port `buildClampedSmoothPath` and the five-dimensional dual-axis trend chart, using real cost fields and retaining safe behavior for zero or one point.
- [ ] Port the crosshair, active dots, clamped tooltip positioning, health tooltip, and linked donut/list hover behavior with event delegation so 30-second refreshes do not duplicate listeners.
- [ ] Keep the existing five-day UTC health-grid contract and sample color thresholds.
- [ ] Render donut center text and percentage/request tooltips from real merged distributions.
- [ ] Implement actual event reset: clear the form and `state.eventFilters`, reset `eventPage` to 1, then reload the overview.
- [ ] Render the 12 event fields from real data; compute non-cached input as `max(0, input - cacheRead - cacheCreation)`.
- [ ] Render four interface cards, weighted upstream latency, request-share progress bars, health-aware upstream status, and the sample detail button.
- [ ] Preserve production save, restore, export, authentication, visibility handling, request cancellation guards, and error toasts.
- [ ] Run `node --check dashboard/app.js` and embedded asset tests.

### Task 7: Visual Verification and Release

**Files:**
- Modify: `go/types.go`
- Modify: `registry.json`
- Modify: `scripts/build.ps1`
- Modify: `README.md`

**Interfaces:**
- Produces: verified release `1.0.10`.

- [ ] Verify light and dark themes at 1440x900 and 1920x1080 against `参考样例1.html`, comparing header, KPI geometry, trend chart, health grid, distributions, events, interfaces, and settings.
- [ ] Verify 980x900 and 390x844 for overflow, navigation wrapping, table scrolling, tooltip clamping, and fixed-format component dimensions.
- [ ] Confirm that refreshing, range switching, legend toggles, filters/reset, pagination, export, drawer, price editing, backup/restore, and Management Key flow remain functional.
- [ ] Run `gofmt` on all changed Go files.
- [ ] Run `go test ./... -count=1`, `go vet ./...`, and `node --check dashboard/app.js`.
- [ ] Search production files for mock/demo strings, raw secret fixtures, stale `1.0.9` release references, and root-relative API requests outside `API_ROOT`.
- [ ] Update the plugin, registry, build-script, README, and release examples to `1.0.10` only after every check passes.

## Self-Review

- The plan covers every visible sample component and preserves all production-only functionality.
- Sample-only fake data and random visuals are replaced by existing rollups or explicitly added sanitized fields.
- Summary enhancements stay on management reads; the request callback adds only one bounded string assignment and no I/O.
- The sample's known CSS typo and missing mobile breakpoints are explicitly corrected.
- No external dependency, Redis service, frontend framework, route, or unauthenticated resource is added.

## Execution Note

The target directory is an untracked project inside the parent workspace repository. Implementation should remain in `E:\Cursor\cpa-usage-dashboard` without deleting, moving, or resetting unrelated workspace files.
