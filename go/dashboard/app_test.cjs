const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const os = require('node:os');
const { execFileSync } = require('node:child_process');

const nodes = new Map();
const animationFrames = [];
const requestedPaths = [];
const toastItems = [];
const documentListeners = new Map();
const rangeButtons = ['24h', '7d', '30d', 'all', 'custom'].map((range) => {
  const button = node(`#range-${range}`);
  button.dataset = { range };
  return button;
});
function node(selector) {
  if (!nodes.has(selector)) {
    const classes = new Set();
    nodes.set(selector, {
      id: selector.startsWith('#') ? selector.slice(1) : '',
      innerHTML: '', textContent: '', inert: true, isConnected: true,
      hidden: false, value: '', listeners: {},
      classList: {
        add: (value) => classes.add(value),
        remove: (value) => classes.delete(value),
        contains: (value) => classes.has(value),
        toggle(value, force) {
          if (force === undefined ? !classes.has(value) : force) classes.add(value);
          else classes.delete(value);
        },
      },
      setAttribute() {},
      addEventListener(type, callback) { this.listeners[type] = callback; },
      trigger(type, event = {}) { this.listeners[type]?.(event); },
      click() { this.trigger('click'); },
      closest() { return null; },
      contains(target) { return target === this; },
      querySelector(selector) { return this.querySelectorAll(selector)[0] || null; },
      querySelectorAll(selector) {
        const matched = [];
        const visit = (child) => {
          if (child.matches?.(selector)) matched.push(child);
          (child.children || []).forEach(visit);
        };
        (this.children || []).forEach(visit);
        return matched;
      },
      reset() {},
      focus() {},
      scrollIntoView() {},
    });
  }
  return nodes.get(selector);
}

const pending = new Map();
const context = vm.createContext({
  AbortController,
  Headers,
  Intl,
  URL,
  URLSearchParams,
  console,
  window: { location: { origin: 'http://test.local' } },
  performance: { now: () => 1_000 },
  requestAnimationFrame: (callback) => {
    animationFrames.push(callback);
    return animationFrames.length;
  },
  document: {
    addEventListener(type, callback) { documentListeners.set(type, callback); },
    createElement: () => ({ textContent: '', remove() {} }),
    querySelector(selector) {
      return selector === '#range-control [data-range="custom"]' ? rangeButtons[4] : node(selector);
    },
    querySelectorAll(selector) { return selector === '#range-control button' ? rangeButtons : []; },
    activeElement: null,
  },
  setTimeout: () => 0,
  sessionStorage: { getItem: () => 'test-key' },
  localStorage: { getItem: () => null },
  fetch: (url, options = {}) => new Promise((resolve, reject) => {
    requestedPaths.push(url);
    const key = new URL(url, 'http://test.local').searchParams.get('key');
    options.signal?.addEventListener('abort', () => {
      const error = new Error('aborted');
      error.name = 'AbortError';
      reject(error);
    }, { once: true });
    pending.set(key, resolve);
  }),
});

const source = fs.readFileSync(path.join(__dirname, 'app.js'), 'utf8');
assert.match(source, /\}\)\(\);\s*$/);
vm.runInContext(source.replace(
  /\}\)\(\);\s*$/,
  'globalThis.testAPI = { state, animateNumber, renderTokenComposition, openUpstream, closeDrawer, renderStorage, renderEvents, renderRuntime, renderRangeSummary, renderEventFilterChips, setEventFilterForm, openEventDetail, applyHealthDrilldown, clearHealthDrilldown, drilldownUpstream, chinaInputToMillis, activeRangeParams, eventParams, cached, bindRange, bindEvents, download }; })();',
), context);
const dashboardHTML = fs.readFileSync(path.join(__dirname, 'index.html'), 'utf8');
assert.ok(dashboardHTML.indexOf('id="overview-kpis"') < dashboardHTML.indexOf('id="runtime-strip"'));
assert.ok(dashboardHTML.indexOf('id="runtime-strip"') < dashboardHTML.indexOf('data-region="operational-pulse"'));

function response(name) {
  return {
    ok: true,
    status: 200,
    headers: new Headers({ 'Content-Type': 'application/json' }),
    json: async () => ({ name, summary: {}, models: [], recent_events: [] }),
  };
}

function calendarClick(value, kind = 'day', disabled = false) {
  node('#range-calendars').trigger('click', { target: {
    closest(selector) {
      if (selector !== (kind === 'day' ? '[data-calendar-day]' : '[data-calendar-shift]')) return null;
      return { disabled, dataset: kind === 'day' ? { calendarDay: value } : { calendarShift: value } };
    },
  } });
}

async function main() {
  const { state, animateNumber, renderTokenComposition, openUpstream } = context.testAPI;
  node('#toast-region').appendChild = (item) => toastItems.push(item);

  const firstKpiRender = { id: 'kpi-val-req', textContent: '42' };
  animateNumber(firstKpiRender, 42, String);
  assert.equal(animationFrames.length, 0, 'initial KPI values should not schedule count-up frames');
  const refreshedKpi = { id: 'kpi-val-req', textContent: '50' };
  animateNumber(refreshedKpi, 50, String);
  assert.equal(animationFrames.length, 1, 'updated KPI values should still animate');
  animationFrames.shift()(1_450);
  assert.equal(refreshedKpi.textContent, '50');

  renderTokenComposition(
    { input: 1_000_000, output: 1_000_000, total: 2_000_000 },
    [{ cost_usd: 11, costs: { input: 1, output: 10 } }],
  );
  const cards = [...node('#token-composition').innerHTML.matchAll(
    /class="token-breakdown-card"[^>]*data-idx="([^"]+)"[^>]*data-cost="([^"]+)"/g,
  )];
  const costs = Object.fromEntries(cards.map((match) => [match[1], match[2]]));
  assert.equal(costs['0'], '$1.00');
  assert.equal(costs['3'], '$10.00');

  const first = openUpstream('A');
  const second = openUpstream('B');
  pending.get('B')(response('B'));
  await second;
  assert.equal(node('#detail-title').textContent, 'B');
  pending.get('A')?.(response('A'));
  await first;
  assert.equal(node('#detail-title').textContent, 'B');
  assert.equal(state.drawerRequestID, 2);

  // Use the actual Go JSON encoder and query response to catch field drift.
  const fixtureDir = fs.mkdtempSync(path.join(os.tmpdir(), 'cpa-dashboard-contract-'));
  try {
    const fixturePath = path.join(fixtureDir, 'upstream.json');
    execFileSync('go', ['test', '-count=1', '-run', '^TestDashboardWireFixture$', '.'], {
      cwd: path.join(__dirname, '..'),
      env: { ...process.env, CPA_DASHBOARD_FIXTURE: fixturePath },
      stdio: 'pipe',
    });
    const wire = JSON.parse(fs.readFileSync(fixturePath, 'utf8'));
    assert.equal(wire.recent_events[0].ttft_ms, 1500);
    const request = openUpstream('wire');
    pending.get('wire')({ ...response('wire'), json: async () => wire });
    await request;
    const detailHTML = node('#detail-content').innerHTML;
    assert.match(detailHTML, /首字延迟 \(TTFT\):<\/span> <strong>1\.50 s<\/strong>/);
    assert.match(detailHTML, /生成速度 \(TPS\):<\/span> <strong>66\.7 t\/s<\/strong>/);
  } finally {
    fs.rmSync(fixtureDir, { recursive: true, force: true });
  }

  context.testAPI.renderStorage({ config: {}, runtime: { storage: { metrics_available: false } } });
  assert.match(node('#storage-metrics').innerHTML, /未知/);
  context.testAPI.renderStorage({ config: {}, runtime: { storage: {
    metrics_available: true, metrics_stale: true, sampled_at: '2026-09-25T08:30:00.123456789Z',
    database_bytes: 2048, event_count: 3, rollup_count: 2,
  } } });
  assert.match(node('#storage-metrics').innerHTML, /2\.0 KB/);
  assert.match(node('#storage-metrics').innerHTML, /采集失败，显示上次快照/);
  assert.match(node('#storage-metrics').innerHTML, /09\/25 16:30:00/);
  context.testAPI.renderEvents({ events: [], page: 40001, pages: 50000, accessible_pages: 40001, total: 1250000 });
  assert.equal(node('#page-next').disabled, true, 'next page must respect the server pagination limit');
  assert.equal(state.eventPages, 40001, 'click handlers must use the same accessible page limit');

  const { chinaInputToMillis, activeRangeParams, eventParams, cached } = context.testAPI;
  assert.equal(chinaInputToMillis('2026-09-25T16:30'), Date.parse('2026-09-25T08:30:00Z'));
  assert.equal(chinaInputToMillis('2026-09-25T00:00'), Date.parse('2026-09-24T16:00:00Z'));
  assert.equal(chinaInputToMillis('2026-09-25T23:59:59'), Date.parse('2026-09-25T15:59:59Z'));
  assert.equal(chinaInputToMillis('2026-02-30T16:30'), null);
  assert.equal(chinaInputToMillis('invalid'), null);
  state.range = '7d';
  assert.equal(activeRangeParams().range, '7d');
  assert.equal(activeRangeParams().from, undefined);
  state.range = 'custom';
  state.customRange = { from: 1_000, to: 2_000 };
  state.eventFilters = { q: 'gpt' };
  const normalEvents = eventParams();
  assert.equal(normalEvents.get('range'), 'custom');
  assert.equal(normalEvents.get('from'), '1000');
  assert.equal(normalEvents.get('to'), '2000');
  assert.equal(normalEvents.get('q'), 'gpt');
  state.healthFilter = { from: 1_200, to: 1_300 };
  const lockedEvents = eventParams();
  assert.equal(lockedEvents.get('from'), '1200');
  assert.equal(lockedEvents.get('to'), '1300');
  state.healthFilter = null;
  assert.equal(eventParams().get('from'), '1000', 'clearing the health lock restores the custom range');

  const firstSummary = cached('/summary');
  const requestsBeforeSummaries = requestedPaths.length;
  assert.match(requestedPaths.at(-1), /range=custom&from=1000&to=2000/);
  pending.get(null)(response('first'));
  await firstSummary;
  state.customRange = { from: 3_000, to: 4_000 };
  const secondSummary = cached('/summary');
  assert.match(requestedPaths.at(-1), /range=custom&from=3000&to=4000/);
  pending.get(null)(response('second'));
  await secondSummary;
  assert.equal(requestedPaths.length, requestsBeforeSummaries + 1, 'different custom ranges need separate cache keys');

  const customUpstream = openUpstream('custom-key');
  assert.match(requestedPaths.at(-1), /range=custom&from=3000&to=4000&key=custom-key/);
  pending.get('custom-key')(response('custom-key'));
  await customUpstream;

  const failedBackup = context.testAPI.download('/v0/management/plugins/usage-keeper/backup', 'backup.json');
  pending.get(null)({
    ok: false, status: 409,
    headers: new Headers({ 'Content-Type': 'application/json' }),
    json: async () => ({ error: { message: '请求明细超过备份上限，请提高 export_max_records 后重试' } }),
  });
  await failedBackup;
  assert.equal(toastItems.at(-1).textContent, '请求明细超过备份上限，请提高 export_max_records 后重试');

  state.page = 'settings';
  state.range = '24h';
  const form = node('#custom-range-form');
  form.hidden = true;
  context.testAPI.bindRange();
  assert.equal(node('#custom-range-summary').hidden, true, 'settings should not show the overview range summary');
  rangeButtons[4].trigger('click');
  assert.equal(form.hidden, false);
  assert.equal(state.range, '24h', 'opening the editor must not change the applied preset');
  assert.match(requestedPaths.at(-1), /\/events\/dates$/);
  assert.equal(node('#custom-range-apply').disabled, true, 'dates must remain unavailable while loading');
  pending.get(null)({ ...response('days'), json: async () => ({ days: ['2026-09-24', '2026-09-25', '2026-10-03'] }) });
  await new Promise(setImmediate);
  const calendarHTML = node('#range-calendars').innerHTML;
  assert.match(calendarHTML, /2026 年 9 月/);
  assert.match(calendarHTML, /2026 年 10 月/);
  assert.match(calendarHTML, /data-calendar-day="2026-09-26"[^>]* disabled/);
  assert.match(calendarHTML, /data-calendar-day="2026-09-25"[^>]*>25<\/button>/);
  calendarClick('2026-09-26');
  assert.equal(node('#custom-range-from').textContent, '选择日期', 'a date absent from the API cannot be selected');
  calendarClick('2026-09-25');
  assert.equal(node('#custom-range-from').textContent, '2026-09-25');
  calendarClick('2026-10-03');
  assert.equal(node('#custom-range-to').textContent, '2026-10-03', 'cross-month end date can be selected');
  calendarClick(1, 'shift');
  assert.match(node('#range-calendars').innerHTML, /2026 年 11 月/);
  calendarClick(-1, 'shift');
  assert.match(node('#range-calendars').innerHTML, /2026 年 9 月/);
  node('#custom-range-to').trigger('click');
  calendarClick('2026-09-25');
  node('#custom-range-from-time').value = '16:30:00';
  node('#custom-range-from-time').trigger('input');
  node('#custom-range-to-time').value = '15:30:00';
  node('#custom-range-to-time').trigger('input');
  assert.equal(node('#custom-range-apply').disabled, true, 'end time before start time cannot be applied');
  form.trigger('submit', { preventDefault() {} });
  assert.equal(state.range, '24h', 'invalid bounds must not change the active range');
  assert.equal(node('#custom-range-error').hidden, false);
  node('#custom-range-to-time').value = '17:30:00';
  node('#custom-range-to-time').trigger('input');
  assert.equal(node('#custom-range-apply').disabled, false);
  form.trigger('submit', { preventDefault() {} });
  assert.equal(state.range, 'custom');
  assert.equal(state.customRange.from, Date.parse('2026-09-25T08:30:00Z'));
  assert.equal(state.customRange.to, Date.parse('2026-09-25T09:30:00.999Z'));
  assert.equal(form.hidden, true);
  assert.equal(rangeButtons[4].classList.contains('is-active'), true);
  state.page = 'overview';
  context.testAPI.renderRangeSummary();
  assert.equal(node('#custom-range-summary').hidden, false);
  assert.equal(node('#custom-range-summary-value').textContent, '2026-09-25 16:30:00 至 2026-09-25 17:30:00');
  node('#custom-range-summary').trigger('click');
  assert.equal(form.hidden, false, 'the applied range summary should reopen the editor');
  assert.equal(state.range, 'custom', 'editing an applied custom range must keep it active');
  assert.equal(node('#custom-range-from-time').value, '16:30:00');
  assert.equal(node('#custom-range-to-time').value, '17:30:00');
  pending.get(null)({ ...response('days'), json: async () => ({ days: ['2026-09-25'] }) });
  await new Promise(setImmediate);
  documentListeners.get('pointerdown')({ target: { closest: () => null } });
  assert.equal(form.hidden, true, 'clicking outside the floating picker should close it');
  rangeButtons[4].trigger('click');
  pending.get(null)({ ...response('empty'), json: async () => ({ days: [] }) });
  await new Promise(setImmediate);
  assert.match(node('#range-calendars').innerHTML, /暂无请求明细记录/);
  assert.equal(node('#custom-range-apply').disabled, true);
  documentListeners.get('keydown')({ key: 'Escape' });
  assert.equal(form.hidden, true, 'Escape should close the floating picker');
  rangeButtons[4].trigger('click');
  pending.get(null)({ ...response('empty'), json: async () => ({ days: [] }) });
  await new Promise(setImmediate);
  node('#custom-range-cancel').trigger('click');
  assert.equal(form.hidden, true, 'the cancel button should close the floating picker');
  rangeButtons[1].trigger('click');
  assert.equal(state.range, '7d');
  assert.equal(node('#custom-range-summary').hidden, true, 'preset ranges should not show the custom range summary');
  assert.equal(rangeButtons[1].classList.contains('is-active'), true);
  assert.equal(rangeButtons[4].classList.contains('is-active'), false);
  assert.equal(eventParams().get('from'), null);
  const presetSummary = cached('/summary');
  assert.match(requestedPaths.at(-1), /range=7d$/);
  pending.get(null)(response('preset'));
  await presetSummary;

  const { renderRuntime, openEventDetail, applyHealthDrilldown, clearHealthDrilldown, drilldownUpstream } = context.testAPI;
  function settleEvents() {
    pending.get(null)?.({
      ...response('events'),
      json: async () => ({ events: [], total: 0, page: 1, pages: 0, accessible_pages: 0 }),
    });
  }

  const runtime = { queue_depth: 0, queue_capacity: 256, accepted: 1, written: 1, dropped: 0, last_batch_ms: 1 };
  context.testAPI.bindEvents();
  renderRuntime({ ...runtime, storage: { last_error: 'disk full' } });
  assert.equal(toastItems.at(-1).textContent, 'disk full');
  assert.match(node('#runtime-strip').innerHTML, /存储异常/);
  assert.equal(node('#runtime-strip').classList.contains('has-runtime-warning'), true);
  const shownErrors = toastItems.length;
  renderRuntime({ ...runtime, storage: { last_error: 'disk full' } });
  assert.equal(toastItems.length, shownErrors, 'refreshing the same storage error must not toast again');
  renderRuntime({ ...runtime, storage: {} });
  assert.equal(toastItems.length, shownErrors, 'clearing a storage error must not toast');
  assert.doesNotMatch(node('#runtime-strip').innerHTML, /存储异常/);
  renderRuntime({ ...runtime, storage: { last_error: 'disk full' } });
  assert.equal(toastItems.length, shownErrors, 'the same storage error must stay suppressed after recovery');
  renderRuntime({ ...runtime, storage: { last_error: 'database is locked' } });
  assert.equal(toastItems.at(-1).textContent, 'database is locked', 'a new storage error should still be reported');
  const distinctErrorCount = toastItems.length;
  renderRuntime({ ...runtime, storage: { last_error: 'database is locked' } });
  assert.equal(toastItems.length, distinctErrorCount, 'the same new error must only be reported once');
  renderRuntime({ ...runtime, write_dropped: 2, write_uncertain: 1 });
  assert.match(node('#runtime-strip').innerHTML, /写入丢失/);
  assert.match(node('#runtime-strip').innerHTML, /写入不确定/);
  assert.equal(node('#runtime-strip').classList.contains('has-runtime-warning'), true);
  renderRuntime(runtime);
  assert.equal(node('#runtime-strip').classList.contains('has-runtime-warning'), false, 'the warning style should clear after counters and storage errors recover');

  const provider = node('#event-filters [name="provider"]');
  provider.options = [{ value: 'provider-1', textContent: 'Provider <One>' }];
  provider.selectedIndex = 0;
  const model = node('#event-filters [name="model"]');
  model.options = [{ value: 'model-1', textContent: 'model-1' }];
  model.selectedIndex = 0;
  context.testAPI.setEventFilterForm({ q: '<img src=x>', provider: 'provider-1', model: 'model-1', status: 'success', upstream: 'private-upstream-key' });
  const filterChips = node('#event-filter-chips');
  assert.equal(filterChips.hidden, false);
  assert.match(filterChips.innerHTML, /搜索：&lt;img src=x&gt;/);
  assert.match(filterChips.innerHTML, /Provider：Provider &lt;One&gt;/);
  assert.match(filterChips.innerHTML, /模型：model-1/);
  assert.match(filterChips.innerHTML, /状态：成功/);
  assert.match(filterChips.innerHTML, /上游筛选：已选/);
  assert.doesNotMatch(filterChips.innerHTML, /private-upstream-key/);

  openEventDetail({ api_key: 'key-abc12345', api_key_hash: '', model: 'gpt', failed: false, timestamp_ms: 1_000 });
  assert.match(node('#detail-content').innerHTML, /客户端标识<\/span><strong class="kv-value">key-abc12345<\/strong>/);

  state.page = 'overview';
  state.eventFilters = { q: 'old', provider: 'openai', model: 'gpt', status: 'success' };
  const slotMs = 15 * 60 * 1000;
  applyHealthDrilldown(1_000, 1_000 + slotMs - 1, '09/25 16:00', true, 2);
  assert.equal(state.healthFilter.to, 1_000 + slotMs - 1, 'an inclusive query must stop before the next cell');
  assert.equal(state.eventFilters.status, 'failure');
  assert.equal(state.eventFilters.q, undefined, 'a health cell must not keep the previous search');
  assert.equal(node('#event-filters [name="status"]').value, 'failure');
  assert.equal(node('#event-filters [name="q"]').value, '');
  assert.match(requestedPaths.at(-1), /from=1000&to=900999/);
  assert.match(requestedPaths.at(-1), /status=failure/);
  settleEvents();
  await new Promise(setImmediate);

  applyHealthDrilldown(2_000, 2_000 + slotMs - 1, '09/25 16:15', false, 0);
  assert.equal(state.eventFilters.status, undefined, 'a healthy cell must clear the failure filter');
  assert.equal(node('#event-filters [name="status"]').value, '');
  assert.doesNotMatch(requestedPaths.at(-1), /status=/);
  settleEvents();
  await new Promise(setImmediate);

  drilldownUpstream('upstream-1', 'openai / acc***om');
  assert.equal(state.healthFilter, null, 'an upstream drilldown replaces the health-cell lock');
  assert.deepEqual(JSON.parse(JSON.stringify(state.eventFilters)), { upstream: 'upstream-1' });
  assert.equal(node('#event-filters [name="q"]').value, '', 'the masked upstream name must not become a search');
  assert.equal(node('#event-filters [name="upstream"]').value, 'upstream-1');
  assert.match(requestedPaths.at(-1), /upstream=upstream-1/);
  assert.doesNotMatch(requestedPaths.at(-1), /[?&]q=/);
  assert.match(node('#event-filter-chips').innerHTML, /上游筛选：已选/);
  node('#event-filter-chips').trigger('click', { target: {
    closest(selector) {
      return selector === '[data-remove-event-filter]' ? { dataset: { removeEventFilter: 'upstream' } } : null;
    },
  } });
  assert.deepEqual(JSON.parse(JSON.stringify(state.eventFilters)), {});
  assert.equal(node('#filter-upstream').value, '');
  assert.equal(node('#drilldown-banner').hidden, true);
  assert.equal(node('#event-filter-chips').hidden, true);
  settleEvents();
  await new Promise(setImmediate);

  clearHealthDrilldown();
  assert.equal(state.healthFilter, null);
  assert.deepEqual(JSON.parse(JSON.stringify(state.eventFilters)), {});
  assert.equal(node('#event-filters [name="upstream"]').value, '');
  settleEvents();
  await new Promise(setImmediate);
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
