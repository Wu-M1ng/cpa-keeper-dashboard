# Channel Distribution, Health Grid, and Event Table Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Merge duplicate displayed channel sources, render a five-day 15-minute service-health grid, and expose the requested ten event-detail columns.

**Architecture:** Keep aggregation and privacy normalization in Go so every client receives one canonical channel entry. Keep the health query bounded to exactly 480 rollup buckets and render those buckets as five labeled rows in the embedded HTML/CSS/JS dashboard. Event data already contains every required token field, so only the presentation calculation and table structure change.

**Tech Stack:** Go 1.24, SQLite rollups, embedded HTML/CSS/vanilla JavaScript.

## Global Constraints

- Keep the usage callback non-blocking and free of new I/O.
- Do not add Redis, frontend frameworks, or external assets.
- Never expose raw client keys or Provider credentials.
- Preserve the 30-second guarded auto-refresh behavior.

---

### Task 1: Merge Duplicate Channel Source Segments

**Files:**
- Modify: `go/query.go`
- Test: `go/query_test.go`

**Interfaces:**
- Consumes: `[]dimensionStat` after `maskProviderCredentialStats`.
- Produces: `mergeDimensionStatsByName([]dimensionStat) []dimensionStat` with one row per displayed channel label.

- [x] Add a query test that writes two legacy rollup `source` values which both mask to `codex / sk-***e0`, then assert the source distribution has one item with two requests.
- [x] Run `go test ./...` and verify the new test fails with two source items.
- [x] Merge source statistics after masking, combining requests, successes, failures, latency, cost, token totals, and model sets.
- [x] Sort the merged result by request count and rerun `go test ./...`.

### Task 2: Build the Five-Day Health Grid

**Files:**
- Modify: `go/query.go`
- Modify: `go/dashboard/index.html`
- Modify: `go/dashboard/app.js`
- Modify: `go/dashboard/style.css`
- Test: `go/query_test.go`
- Test: `go/dashboard_test.go`

**Interfaces:**
- Consumes: `usage_minute_rollups` for the five UTC calendar days ending today.
- Produces: exactly 480 `healthPoint` values, ordered as five groups of 96 15-minute buckets.

- [x] Add tests asserting both populated and empty stores return 480 health points, with the first point at midnight four days before `now`.
- [x] Replace range-derived health intervals with a fixed 15-minute health range and make dense filling include no-request buckets.
- [x] Update the health panel heading and add success/failure summary targets.
- [x] Render five labeled rows, calculate success/failure totals from the health points, and assign five health levels using the Keeper thresholds.
- [x] Add wide-grid, horizontal-scroll, dark-theme, and mobile CSS matching the supplied reference.
- [x] Extend embedded dashboard tests to assert the five-day copy and health summary targets.

### Task 3: Expand Request Details to Ten Columns

**Files:**
- Modify: `go/dashboard/index.html`
- Modify: `go/dashboard/app.js`
- Modify: `go/dashboard/style.css`
- Test: `go/dashboard_test.go`

**Interfaces:**
- Consumes: existing event JSON fields `latency_ms`, `ttft_ms`, `input_tokens`, `output_tokens`, `reasoning_tokens`, `cache_read_tokens`, `cached_tokens`, `cache_creation_tokens`, and `total_tokens`.
- Produces: columns `时间`, `模型`, `状态`, `用时 / 首字`, `非缓存输入`, `输出`, `思考`, `缓存命中`, `缓存创建`, `总计`.

- [x] Add dashboard asset assertions for all ten column labels and a ten-column empty state.
- [x] Replace the six-column HTML header with the requested ten-column header.
- [x] Render non-cached input as `max(0, input - cacheRead - cacheCreation)` and keep latency/TTFT in one cell.
- [x] Increase the event table minimum width so desktop columns remain readable and mobile uses horizontal scrolling.

### Task 4: Version and Verification

**Files:**
- Modify: `go/types.go`
- Modify: `registry.json`
- Modify: `scripts/build.ps1`
- Modify: `README.md`

**Interfaces:**
- Produces: plugin version `1.0.9` and release tag examples `v1.0.9`.

- [x] Replace every previous release reference with `1.0.9`.
- [x] Run `gofmt` on changed Go files.
- [x] Run `go test ./...`, `go vet ./...`, and `node --check dashboard/app.js`.
- [x] Search for stale previous release references and verify CSS brace balance.

## Execution Note

This project is untracked inside the parent workspace repository, so the plan deliberately omits Git commit steps and preserves all existing workspace changes.
