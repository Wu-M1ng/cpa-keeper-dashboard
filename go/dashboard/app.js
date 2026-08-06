(() => {
  'use strict';

  const API_ROOT = '/v0/management/plugins/usage-keeper';
  const AUTO_REFRESH_MS = 30_000;
  const COLORS = ['var(--blue)', 'var(--accent)', 'var(--orange)', 'var(--yellow)', 'var(--red)', '#7589b5'];
  const pageMeta = {
    overview: ['总览', '运行状态与用量脉搏'],
    interfaces: ['接口', '客户端与上游调用结构'],
    settings: ['设置', '价格、存储与数据迁移'],
  };
  const state = {
    page: 'overview',
    range: '24h',
    theme: readTheme(),
    managementKey: readManagementKey(),
    loading: false,
    lastRefreshAt: 0,
    cache: new Map(),
    eventPage: 1,
    eventPages: 0,
    eventFilters: {},
  };

  const $ = (selector, root = document) => root.querySelector(selector);
  const $$ = (selector, root = document) => [...root.querySelectorAll(selector)];
  const icon = (name) => `<svg aria-hidden="true"><use href="#i-${name}"></use></svg>`;
  const esc = (value) => String(value ?? '').replace(/[&<>'"]/g, (char) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', "'": '&#39;', '"': '&quot;' })[char]);
  const number = new Intl.NumberFormat('zh-CN');
  const compact = new Intl.NumberFormat('zh-CN', { notation: 'compact', maximumFractionDigits: 1 });
  const money = new Intl.NumberFormat('zh-CN', { style: 'currency', currency: 'USD', minimumFractionDigits: 2, maximumFractionDigits: 4 });

  document.addEventListener('DOMContentLoaded', init);

  function init() {
    bindTheme();
    bindNavigation();
    bindRange();
    bindEvents();
    bindSettings();
    bindDrawer();
    bindAuth();
    updateConnection(Boolean(state.managementKey));
    loadActivePage(true);
    startAutoRefresh();
  }

  function startAutoRefresh() {
    const refresh = () => {
      if (document.visibilityState !== 'visible') return;
      if (!state.managementKey || state.loading || state.page === 'settings') return;
      if (Date.now() - state.lastRefreshAt < AUTO_REFRESH_MS) return;
      loadActivePage(true);
    };
    window.setInterval(refresh, AUTO_REFRESH_MS);
    document.addEventListener('visibilitychange', refresh);
  }

  function bindTheme() {
    applyTheme(state.theme);
    $$('#theme-control button').forEach((button) => button.addEventListener('click', () => {
      state.theme = button.dataset.theme || 'auto';
      try { localStorage.setItem('usage-keeper-theme', state.theme); } catch (_) { /* Storage may be disabled. */ }
      applyTheme(state.theme);
    }));
  }

  function applyTheme(theme) {
    const explicit = theme === 'light' || theme === 'dark' ? theme : '';
    if (explicit) document.documentElement.dataset.theme = explicit;
    else delete document.documentElement.dataset.theme;
    $$('#theme-control button').forEach((button) => {
      const active = button.dataset.theme === theme;
      button.classList.toggle('is-active', active);
      button.setAttribute('aria-pressed', String(active));
    });
  }

  function bindNavigation() {
    $$('.nav-item').forEach((button) => button.addEventListener('click', () => {
      const page = button.dataset.pageTarget;
      if (!pageMeta[page] || page === state.page) return;
      state.page = page;
      $$('.nav-item').forEach((item) => item.classList.toggle('is-active', item === button));
      $$('.page').forEach((item) => item.classList.toggle('is-active', item.dataset.page === page));
      $('#page-title').textContent = pageMeta[page][0];
      $('#page-subtitle').textContent = pageMeta[page][1];
      $('#range-control').hidden = page === 'settings';
      closeDrawer();
      loadActivePage(false);
    }));
    $('#refresh-button').addEventListener('click', () => loadActivePage(true));
  }

  function bindRange() {
    $$('#range-control button').forEach((button) => button.addEventListener('click', () => {
      state.range = button.dataset.range;
      state.cache.clear();
      $$('#range-control button').forEach((item) => item.classList.toggle('is-active', item === button));
      state.eventPage = 1;
      loadActivePage(true);
    }));
  }

  function bindEvents() {
    $('#event-filters').addEventListener('submit', (event) => {
      event.preventDefault();
      const form = new FormData(event.currentTarget);
      state.eventFilters = Object.fromEntries([...form.entries()].filter(([, value]) => value));
      state.eventPage = 1;
      loadEvents(true);
    });
    $('#page-prev').addEventListener('click', () => {
      if (state.eventPage > 1) { state.eventPage -= 1; loadEvents(true); }
    });
    $('#page-next').addEventListener('click', () => {
      if (state.eventPage < state.eventPages) { state.eventPage += 1; loadEvents(true); }
    });
    $('#event-export').addEventListener('click', () => {
      const params = eventParams();
      params.delete('page');
      params.delete('page_size');
      download(`${API_ROOT}/events/export?${params}`, 'usage-events.csv');
    });
  }

  function bindSettings() {
    $('#add-price').addEventListener('click', () => appendPriceRow({}));
    $('#save-prices').addEventListener('click', savePrices);
    $('#price-table').addEventListener('click', (event) => {
      const button = event.target.closest('[data-remove-price]');
      if (button) button.closest('tr').remove();
    });
    $('#storage-settings').addEventListener('submit', saveStorageSettings);
    $('#backup-export').addEventListener('click', () => download(`${API_ROOT}/backup`, 'usage-keeper-backup.json'));
    $('#backup-import').addEventListener('click', () => $('#backup-file').click());
    $('#backup-file').addEventListener('change', restoreBackup);
    $('#change-key').addEventListener('click', showAuthDialog);
  }

  function bindDrawer() {
    $('#detail-close').addEventListener('click', closeDrawer);
    $('#drawer-scrim').addEventListener('click', closeDrawer);
    $('#upstream-table').addEventListener('click', (event) => {
      const button = event.target.closest('[data-upstream]');
      if (button) openUpstream(button.dataset.upstream);
    });
  }

  function bindAuth() {
    $('#auth-save').addEventListener('click', (event) => {
      event.preventDefault();
      const key = $('#auth-key').value.trim();
      if (!key) return;
      state.managementKey = key;
      sessionStorage.setItem('usage-keeper-management-key', key);
      $('#auth-dialog').close();
      updateConnection(true);
      state.cache.clear();
      loadActivePage(true);
    });
  }

  async function loadActivePage(force) {
    if (state.loading) return;
    setLoading(true);
    try {
      if (state.page === 'overview') await loadOverview(force);
      if (state.page === 'interfaces') await loadInterfaces(force);
      if (state.page === 'settings') await loadSettings(force);
      updateConnection(true);
      state.lastRefreshAt = Date.now();
    } catch (error) {
      if (error.name !== 'AuthRequired') {
        toast(error.message || '加载失败', true);
        updateConnection(false);
      }
    } finally {
      setLoading(false);
    }
  }

  async function cached(path, force = false) {
    const key = `${path}|${state.range}`;
    if (!force && state.cache.has(key)) return state.cache.get(key);
    const separator = path.includes('?') ? '&' : '?';
    const data = await api(`${path}${separator}range=${encodeURIComponent(state.range)}`);
    state.cache.set(key, data);
    return data;
  }

  async function api(path, options = {}) {
    if (!state.managementKey) {
      showAuthDialog();
      throw namedError('AuthRequired', '需要 Management Key');
    }
    const headers = new Headers(options.headers || {});
    headers.set('Authorization', `Bearer ${state.managementKey}`);
    if (options.body && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json');
    const endpoint = path.startsWith('/') ? API_ROOT + path : API_ROOT + '/' + path;
    const response = await fetch(endpoint, { ...options, headers });
    if (response.status === 401) {
      sessionStorage.removeItem('usage-keeper-management-key');
      state.managementKey = '';
      updateConnection(false);
      showAuthDialog();
      throw namedError('AuthRequired', 'Management Key 已失效');
    }
    const contentType = response.headers.get('content-type') || '';
    const body = contentType.includes('json') ? await response.json() : await response.text();
    if (!response.ok) {
      throw new Error(body?.error?.message || body?.error || `请求失败 (${response.status})`);
    }
    return body;
  }

  async function download(path, filename) {
    try {
      if (!state.managementKey) return showAuthDialog();
      const response = await fetch(path, { headers: { Authorization: `Bearer ${state.managementKey}` } });
      if (!response.ok) throw new Error(`导出失败 (${response.status})`);
      const blob = await response.blob();
      const href = URL.createObjectURL(blob);
      const link = document.createElement('a');
      link.href = href;
      link.download = filename;
      document.body.appendChild(link);
      link.click();
      link.remove();
      URL.revokeObjectURL(href);
      toast('导出完成');
    } catch (error) {
      toast(error.message || '导出失败', true);
    }
  }

  async function loadOverview(force) {
    const params = eventParams();
    const [summary, analysis, events] = await Promise.all([
      cached('/summary', force),
      cached('/analysis', force),
      api(`/events?${params}`),
    ]);
    renderKPIs(summary.kpi || {});
    renderTrend(summary.trend || []);
    renderHealth(summary.health || [], summary.kpi || {});
    renderRuntime(summary.runtime || {});
    renderAnalysis(analysis);
    renderEventOptions(analysis);
    state.eventPages = events.pages || 0;
    renderEvents(events);
  }

  function renderKPIs(kpi) {
    const cards = [
      ['请求总量', formatInt(kpi.requests), `${formatInt(kpi.successes)} 次成功`],
      ['成功率', formatPercent(kpi.success_rate), `${formatInt(kpi.failures)} 次失败`],
      ['Token', formatCompact(kpi.total_tokens), formatInt(kpi.total_tokens)],
      ['估算费用', formatMoney(kpi.cost_usd), '当前模型价格'],
      ['平均延迟', formatDuration(kpi.avg_latency_ms), `TTFT ${formatDuration(kpi.avg_ttft_ms)}`],
    ];
    $('#overview-kpis').innerHTML = cards.map(([label, value, sub]) => `<article class="kpi-card"><span>${label}</span><strong>${value}</strong><small>${sub}</small></article>`).join('');
  }

  function renderTrend(points) {
    const host = $('#trend-chart');
    if (!points.length) return empty(host, '当前范围没有用量');
    const width = 760, height = 275, left = 44, right = 22, top = 20, bottom = 34;
    const plotW = width - left - right, plotH = height - top - bottom;
    const maxRequests = Math.max(1, ...points.map((item) => item.requests || 0));
    const maxTokens = Math.max(1, ...points.map((item) => item.tokens || 0));
    const x = (index) => left + (points.length === 1 ? plotW / 2 : index * plotW / (points.length - 1));
    const requestY = (value) => top + plotH - value / maxRequests * plotH;
    const tokenY = (value) => top + plotH - value / maxTokens * plotH;
    const requestPath = points.map((item, index) => `${index ? 'L' : 'M'}${x(index).toFixed(1)},${requestY(item.requests).toFixed(1)}`).join(' ');
    const tokenPath = points.map((item, index) => `${index ? 'L' : 'M'}${x(index).toFixed(1)},${tokenY(item.tokens).toFixed(1)}`).join(' ');
    const grid = [0, .25, .5, .75, 1].map((ratio) => {
      const y = top + plotH * ratio;
      return `<line class="chart-grid-line" x1="${left}" y1="${y}" x2="${width-right}" y2="${y}"/><text class="chart-label" x="4" y="${y+3}">${formatCompact(Math.round(maxRequests * (1-ratio)))}</text>`;
    }).join('');
    const labelIndexes = [...new Set([0, Math.floor((points.length - 1) / 3), Math.floor((points.length - 1) * 2 / 3), points.length - 1])];
    const labels = labelIndexes.map((index) => `<text class="chart-label" text-anchor="middle" x="${x(index)}" y="${height-8}">${formatTime(points[index].timestamp_ms, state.range)}</text>`).join('');
    host.innerHTML = `<svg class="trend-svg" viewBox="0 0 ${width} ${height}" role="img" aria-label="用量趋势">${grid}<path class="chart-request-line" d="${requestPath}"/><path class="chart-token-line" d="${tokenPath}"/>${labels}</svg>`;
  }

  function renderHealth(points, kpi) {
    const host = $('#health-grid');
    $('#health-summary').textContent = formatPercent(kpi.success_rate);
    if (!points.length) return empty(host, '暂无健康数据');
    host.innerHTML = points.slice(-480).map((point) => {
      const rate = point.success_rate || 0;
      const level = point.requests === 0 ? 0 : rate >= .995 ? 5 : rate >= .98 ? 4 : rate >= .9 ? 3 : rate >= .7 ? 2 : 1;
      return `<span class="health-cell level-${level}" title="${esc(formatDateTime(point.timestamp_ms))} · ${formatPercent(rate)} · ${formatInt(point.requests)} 次"></span>`;
    }).join('');
  }

  function renderRuntime(runtime) {
    const storage = runtime.storage || {};
    const items = [
      ['队列', `${formatInt(runtime.queue_depth)} / ${formatInt(runtime.queue_capacity)}`],
      ['已接收', formatInt(runtime.accepted)],
      ['已写入', formatInt(runtime.written)],
      ['已丢弃', formatInt(runtime.dropped)],
      ['最近批写', `${formatNumber(runtime.last_batch_ms, 2)} ms`],
    ];
    $('#runtime-strip').innerHTML = items.map(([label, value]) => `<div class="runtime-item"><span>${label}</span><strong>${value}</strong></div>`).join('');
    if (storage.last_error) toast(storage.last_error, true);
  }

  async function loadAnalysis(force) {
    const data = await cached('/analysis', force);
    renderAnalysis(data);
  }

  function renderAnalysis(data) {
    renderDistributions(data.distributions || {});
    renderTokenComposition(data.tokens || {});
    renderModelTable(data.models || []);
  }

  function renderDistributions(distributions) {
    const definitions = [
      ['models', '模型分布'], ['providers', '上游分布'], ['api_keys', '客户端 Key 分组'], ['sources', '渠道来源分布'],
    ];
    $('#distribution-grid').innerHTML = definitions.map(([key, title]) => distributionCard(title, distributions[key] || [])).join('');
  }

  function distributionCard(title, values) {
    const top = values.slice(0, 5);
    const total = values.reduce((sum, item) => sum + (item.requests || 0), 0) || 1;
    const circumference = 2 * Math.PI * 38;
    let offset = 0;
    const segments = top.map((item, index) => {
      const length = (item.requests || 0) / total * circumference;
      const segment = `<circle class="donut-segment" cx="52" cy="52" r="38" stroke="${COLORS[index % COLORS.length]}" stroke-dasharray="${length} ${circumference-length}" stroke-dashoffset="${-offset}"/>`;
      offset += length;
      return segment;
    }).join('');
    const list = top.length ? top.map((item, index) => `<div class="distribution-row"><i style="background:${COLORS[index % COLORS.length]}"></i><span title="${esc(item.name)}">${esc(item.name || '未识别')}</span><strong>${formatCompact(item.requests)}</strong></div>`).join('') : '<div class="cell-sub">暂无数据</div>';
    return `<article class="distribution-card"><h2>${title}</h2><div class="donut-layout"><svg class="donut" viewBox="0 0 104 104" aria-hidden="true"><circle class="donut-track" cx="52" cy="52" r="38"/>${segments}</svg><div class="distribution-list">${list}</div></div></article>`;
  }

  function renderTokenComposition(tokens) {
    const regularInput = Math.max(0, (tokens.input || 0) - (tokens.cache_read || 0) - (tokens.cache_write || 0));
    const regularOutput = Math.max(0, (tokens.output || 0) - (tokens.reasoning || 0));
    const parts = [
      ['输入', regularInput, 'var(--blue)'], ['输出', regularOutput, 'var(--orange)'],
      ['缓存读取', tokens.cache_read || 0, 'var(--accent)'], ['缓存写入', tokens.cache_write || 0, 'var(--yellow)'],
      ['推理', tokens.reasoning || 0, 'var(--red)'],
    ];
    const sum = parts.reduce((total, [, value]) => total + value, 0) || 1;
    $('#token-total').textContent = formatCompact(tokens.total || 0);
    $('#token-composition').innerHTML = `<div class="token-stack">${parts.map(([, value, color]) => `<span style="width:${value/sum*100}%;background:${color}"></span>`).join('')}</div><div class="token-legend">${parts.map(([label, value, color]) => `<div class="token-legend-item" style="border-color:${color}"><span>${label}</span><strong>${formatCompact(value)}</strong></div>`).join('')}</div>`;
  }

  function renderModelTable(models) {
    const body = $('#model-table');
    if (!models.length) return emptyRow(body, 6);
    body.innerHTML = models.map((item) => `<tr><td><span class="cell-main">${esc(item.name)}</span></td><td class="numeric">${formatInt(item.requests)}</td><td>${statusBadge(item.success_rate)}</td><td class="numeric">${formatCompact(item.total_tokens)}</td><td class="numeric">${formatDuration(item.avg_latency_ms)}</td><td class="numeric">${formatMoney(item.cost_usd)}</td></tr>`).join('');
  }

  async function loadInterfaces(force) {
    const data = await cached('/interfaces', force);
    renderInterfaceSummary(data);
    renderAPIKeys(data.api_keys || []);
    renderUpstreams(data.upstreams || []);
  }

  function renderInterfaceSummary(data) {
    const apiKeys = data.api_keys || [], upstreams = data.upstreams || [];
    const requests = upstreams.reduce((sum, item) => sum + (item.requests || 0), 0);
    $('#interface-summary').innerHTML = metric('客户端 Key', formatInt(apiKeys.length)) + metric('上游', formatInt(upstreams.length)) + metric('请求', formatInt(requests));
  }

  function renderAPIKeys(items) {
    const body = $('#api-key-table');
    if (!items.length) return emptyRow(body, 6);
    body.innerHTML = items.map((item) => `<tr><td><span class="cell-main">${esc(item.name || '未识别')}</span></td><td class="numeric">${formatInt(item.models)}</td><td class="numeric">${formatInt(item.requests)}</td><td>${statusBadge(item.success_rate)}</td><td class="numeric">${formatCompact(item.total_tokens)}</td><td class="numeric">${formatMoney(item.cost_usd)}</td></tr>`).join('');
  }

  function renderUpstreams(items) {
    const body = $('#upstream-table');
    if (!items.length) return emptyRow(body, 6);
    body.innerHTML = items.map((item) => `<tr><td><button class="row-button" data-upstream="${esc(item.key)}">${esc(item.name)}</button></td><td class="numeric">${formatInt(item.models)}</td><td class="numeric">${formatInt(item.requests)}</td><td>${statusBadge(item.success_rate)}</td><td class="numeric">${formatDuration(item.avg_latency_ms)}</td><td class="numeric">${formatCompact(item.total_tokens)}</td></tr>`).join('');
  }

  async function openUpstream(key) {
    const drawer = $('#detail-drawer');
    drawer.classList.add('is-open');
    drawer.setAttribute('aria-hidden', 'false');
    $('#drawer-scrim').classList.add('is-open');
    $('#detail-title').textContent = '正在加载';
    $('#detail-content').innerHTML = '<div class="skeleton" style="height:180px"></div>';
    try {
      const data = await api(`/upstream?range=${encodeURIComponent(state.range)}&key=${encodeURIComponent(key)}`);
      $('#detail-title').textContent = data.name || key;
      const summary = data.summary || {};
      const models = data.models || [];
      const events = data.recent_events || [];
      $('#detail-content').innerHTML = `<div class="detail-kpis">${metric('请求', formatInt(summary.requests))}${metric('成功率', formatPercent(summary.success_rate))}${metric('Token', formatCompact(summary.total_tokens))}${metric('平均延迟', formatDuration(summary.avg_latency_ms))}</div><section class="detail-section"><h3>模型</h3>${models.map((item) => `<div class="distribution-row"><i style="background:var(--accent)"></i><span>${esc(item.name)}</span><strong>${formatInt(item.requests)}</strong></div>`).join('') || '<span class="cell-sub">暂无数据</span>'}</section><section class="detail-section"><h3>近期事件</h3>${events.map((event) => `<div class="distribution-row"><i style="background:${event.failed ? 'var(--red)' : 'var(--green)'}"></i><span>${esc(event.model)} · ${esc(formatDateTime(event.timestamp_ms))}</span><strong>${formatDuration(event.latency_ms)}</strong></div>`).join('') || '<span class="cell-sub">暂无数据</span>'}</section>`;
    } catch (error) {
      $('#detail-title').textContent = '加载失败';
      $('#detail-content').textContent = error.message;
    }
  }

  function closeDrawer() {
    $('#detail-drawer').classList.remove('is-open');
    $('#detail-drawer').setAttribute('aria-hidden', 'true');
    $('#drawer-scrim').classList.remove('is-open');
  }

  async function loadEvents(force) {
    await loadEventOptions(force);
    const params = eventParams();
    const data = await api(`/events?${params}`);
    state.eventPages = data.pages || 0;
    renderEvents(data);
  }

  async function loadEventOptions(force) {
    const analysis = await cached('/analysis', force);
    renderEventOptions(analysis);
  }

  function renderEventOptions(analysis) {
    const distributions = analysis.distributions || {};
    fillSelect($('#event-filters [name="provider"]'), distributions.providers || []);
    fillSelect($('#event-filters [name="model"]'), distributions.models || []);
  }

  function fillSelect(select, items) {
    const current = select.value;
    select.innerHTML = '<option value="">全部</option>' + items.map((item) => `<option value="${esc(item.key)}">${esc(item.name)}</option>`).join('');
    select.value = current;
  }

  function eventParams() {
    return new URLSearchParams({ range: state.range, page: String(state.eventPage), page_size: '25', ...state.eventFilters });
  }

  function renderEvents(data) {
    const body = $('#event-table');
    $('#event-count').textContent = `${formatInt(data.total)} 条记录`;
    $('#page-label').textContent = data.pages ? `第 ${data.page} / ${data.pages} 页` : '第 0 页';
    $('#page-prev').disabled = data.page <= 1;
    $('#page-next').disabled = !data.pages || data.page >= data.pages;
    if (!(data.events || []).length) return emptyRow(body, 6);
    body.innerHTML = data.events.map((event) => `<tr><td><span class="cell-main">${esc(formatDateTime(event.timestamp_ms))}</span><span class="cell-sub">${esc(event.source)}</span></td><td><span class="cell-main">${esc(event.model)}</span><span class="cell-sub">${esc(event.upstream_label)}</span></td><td><span class="cell-main">${esc(event.api_key)}</span></td><td><span class="cell-main numeric">${formatCompact(event.total_tokens)}</span><span class="cell-sub">入 ${formatCompact(event.input_tokens)} · 出 ${formatCompact(event.output_tokens)}</span></td><td><span class="cell-main numeric">${formatDuration(event.latency_ms)}</span><span class="cell-sub">TTFT ${formatDuration(event.ttft_ms)}</span></td><td><span class="status-badge ${event.failed ? 'is-failure' : ''}">${event.failed ? `失败 ${event.status_code || ''}` : '成功'}</span>${event.failure ? `<span class="cell-sub" title="${esc(event.failure)}">${esc(event.failure)}</span>` : ''}</td></tr>`).join('');
  }

  async function loadSettings(force) {
    const [settings, prices] = await Promise.all([api('/settings'), api('/prices')]);
    renderStorage(settings);
    renderPrices(prices.prices || []);
    $('#auth-state').textContent = state.managementKey ? '已连接 · 密钥仅用于当前页面会话' : '未连接';
    if (force) state.cache.clear();
  }

  function renderStorage(data) {
    const config = data.config || {}, runtime = data.runtime || {}, storage = runtime.storage || {};
    $('#storage-path').textContent = storage.path || '内存数据库';
    $('#storage-settings [name="retention_days"]').value = config.retention_days || 30;
    $('#storage-settings [name="export_max_records"]').value = config.export_max_records || 50000;
    const metrics = [
      ['数据库', formatBytes(storage.database_bytes)], ['事件', formatInt(storage.event_count)],
      ['Rollup', formatInt(storage.rollup_count)], ['队列丢弃', formatInt(runtime.dropped)],
    ];
    $('#storage-metrics').innerHTML = metrics.map(([label, value]) => metric(label, value)).join('');
  }

  function renderPrices(prices) {
    const body = $('#price-table');
    body.innerHTML = '';
    prices.forEach(appendPriceRow);
    if (!prices.length) appendPriceRow({});
  }

  function appendPriceRow(price = {}) {
    const row = document.createElement('tr');
    const fields = ['model', 'input_per_million', 'output_per_million', 'cache_read_per_million', 'cache_write_per_million', 'reasoning_per_million'];
    row.innerHTML = fields.map((field, index) => `<td><input data-price-field="${field}" type="${index ? 'number' : 'text'}" ${index ? 'min="0" step="0.000001"' : 'placeholder="model-name"'} value="${esc(price[field] ?? '')}"></td>`).join('') + `<td><button class="icon-button" data-remove-price title="删除"><svg><use href="#i-trash"></use></svg></button></td>`;
    $('#price-table').appendChild(row);
  }

  async function savePrices() {
    const prices = $$('#price-table tr').map((row) => {
      const value = {};
      $$('[data-price-field]', row).forEach((input) => { value[input.dataset.priceField] = input.dataset.priceField === 'model' ? input.value.trim() : Number(input.value || 0); });
      return value;
    }).filter((item) => item.model);
    try {
      const data = await api('/prices', { method: 'PUT', body: JSON.stringify({ prices }) });
      renderPrices(data.prices || []);
      state.cache.clear();
      toast('模型价格已保存');
    } catch (error) { toast(error.message, true); }
  }

  async function saveStorageSettings(event) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    try {
      const data = await api('/settings', { method: 'PUT', body: JSON.stringify({ retention_days: Number(form.get('retention_days')), export_max_records: Number(form.get('export_max_records')) }) });
      renderStorage(data);
      toast('存储设置已保存');
    } catch (error) { toast(error.message, true); }
  }

  async function restoreBackup(event) {
    const file = event.target.files?.[0];
    event.target.value = '';
    if (!file) return;
    try {
      const text = await file.text();
      JSON.parse(text);
      const result = await api('/restore', { method: 'POST', body: text });
      state.cache.clear();
      toast(`已恢复 ${formatInt(result.events)} 条事件`);
      await loadSettings(true);
    } catch (error) { toast(error.message || '导入失败', true); }
  }

  function readManagementKey() {
    try {
      const sessionKey = sessionStorage.getItem('usage-keeper-management-key');
      if (sessionKey) return sessionKey;
      const candidates = ['cli-proxy-auth', 'managementKey'];
      for (const storageKey of candidates) {
        const raw = localStorage.getItem(storageKey);
        if (!raw) continue;
        const decoded = decodeObfuscated(raw);
        const parsed = parseJSON(decoded);
        const found = findManagementKey(parsed);
        if (found) return found;
      }
    } catch (_) { /* Storage access can be disabled by browser policy. */ }
    return '';
  }

  function readTheme() {
    try {
      const saved = localStorage.getItem('usage-keeper-theme');
      if (saved === 'light' || saved === 'dark' || saved === 'auto') return saved;
    } catch (_) { /* Storage may be disabled. */ }
    return 'auto';
  }

  function decodeObfuscated(value) {
    const prefix = 'enc::v1::';
    if (!value.startsWith(prefix)) return value;
    try {
      const binary = atob(value.slice(prefix.length));
      const encrypted = Uint8Array.from(binary, (char) => char.charCodeAt(0));
      const key = new TextEncoder().encode(`cli-proxy-api-webui::secure-storage|${location.host}|${navigator.userAgent}`);
      const output = encrypted.map((byte, index) => byte ^ key[index % key.length]);
      return new TextDecoder().decode(output);
    } catch (_) { return value; }
  }

  function parseJSON(value) {
    try {
      const parsed = JSON.parse(value);
      return typeof parsed === 'string' && parsed !== value ? parseJSON(parsed) : parsed;
    } catch (_) { return value; }
  }

  function findManagementKey(value, depth = 0) {
    if (depth > 5 || value == null) return '';
    if (typeof value === 'object') {
      if (typeof value.managementKey === 'string' && value.managementKey.trim()) return value.managementKey.trim();
      for (const nested of Object.values(value)) {
        const found = findManagementKey(nested, depth + 1);
        if (found) return found;
      }
    }
    if (typeof value === 'string' && depth === 0) return value.trim();
    return '';
  }

  function showAuthDialog() {
    const dialog = $('#auth-dialog');
    $('#auth-key').value = state.managementKey || '';
    if (!dialog.open) dialog.showModal();
    setTimeout(() => $('#auth-key').focus(), 0);
  }

  function updateConnection(online) {
    const dot = $('#connection-dot');
    dot.classList.toggle('is-online', online);
    dot.classList.toggle('is-error', !online);
    $('#connection-label').textContent = online ? '采集服务在线' : '管理连接中断';
    const topbarDot = $('#topbar-state-dot');
    const topbarLabel = $('#topbar-state-label');
    if (topbarDot && topbarLabel) {
      topbarDot.classList.toggle('is-online', online);
      topbarDot.classList.toggle('is-error', !online);
      topbarLabel.textContent = online ? '已连接' : '未连接';
    }
  }

  function setLoading(value) {
    state.loading = value;
    $('#refresh-button').classList.toggle('is-spinning', value);
    $('#refresh-button').disabled = value;
    $('#main-content').setAttribute('aria-busy', String(value));
  }

  function toast(message, isError = false) {
    const item = document.createElement('div');
    item.className = `toast${isError ? ' is-error' : ''}`;
    item.textContent = message;
    $('#toast-region').appendChild(item);
    setTimeout(() => item.remove(), 3600);
  }

  function empty(element, label) { element.innerHTML = `<div class="empty-state" style="display:grid;place-items:center">${esc(label)}</div>`; }
  function emptyRow(element, columns) { element.innerHTML = `<tr><td class="empty-state" colspan="${columns}">当前范围没有数据</td></tr>`; }
  function metric(label, value) { return `<div class="metric"><span>${label}</span><strong>${value}</strong></div>`; }
  function statusBadge(rate) { return `<span class="status-badge ${rate < .9 ? 'is-failure' : ''}">${formatPercent(rate)}</span>`; }
  function formatInt(value) { return number.format(Number(value || 0)); }
  function formatCompact(value) { return compact.format(Number(value || 0)); }
  function formatNumber(value, digits = 1) { return Number(value || 0).toFixed(digits); }
  function formatPercent(value) { return `${(Number(value || 0) * 100).toFixed(value >= .999 ? 2 : 1)}%`; }
  function formatMoney(value) { return money.format(Number(value || 0)); }
  function formatDuration(value) { const ms = Number(value || 0); return ms >= 1000 ? `${(ms / 1000).toFixed(ms >= 10000 ? 1 : 2)} s` : `${Math.round(ms)} ms`; }
  function formatBytes(value) { const bytes = Number(value || 0); if (bytes < 1024) return `${bytes} B`; if (bytes < 1048576) return `${(bytes/1024).toFixed(1)} KB`; return `${(bytes/1048576).toFixed(1)} MB`; }
  function formatDateTime(value) { return new Intl.DateTimeFormat('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit' }).format(new Date(Number(value))); }
  function formatTime(value, range) { return new Intl.DateTimeFormat('zh-CN', range === '24h' ? { hour: '2-digit', minute: '2-digit' } : { month: '2-digit', day: '2-digit' }).format(new Date(Number(value))); }
  function namedError(name, message) { const error = new Error(message); error.name = name; return error; }
})();
