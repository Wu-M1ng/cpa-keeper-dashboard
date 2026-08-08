(() => {
  'use strict';

  const API_ROOT = '/v0/management/plugins/usage-keeper';
  const AUTO_REFRESH_MS = 30_000;
  const FRONTEND_CACHE_TTL_MS = 30_000;
  const FRONTEND_CACHE_MAX_ITEMS = 32;
  const HEALTH_DAYS = 5;
  const HEALTH_SLOTS_PER_DAY = 96;
  const COLORS = ['#326ff5', '#7738ee', '#20b95a', '#ff7a12', '#18ad9d', '#e44e3f'];
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
    pendingRequests: 0,
    lastRefreshAt: 0,
    cache: new Map(),
    eventPage: 1,
    eventPages: 0,
    eventFilters: {},
    trendActiveDims: { input: true, output: true, cache_write: true, cache_read: true, hit_rate: true },
    lastTrendPoints: [],
    loadRequestID: 0,
    eventRequestID: 0,
    loadController: null,
    eventController: null,
    drawerReturnFocus: null,
  };
  let autoRefreshTimer = 0;

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
    bindTrendInteractiveLegend();
    bindHealthInteractivity();
    bindDistributionInteractivity();
    bindTokenInteractivity();
    window.addEventListener('resize', () => {
      if (state.page === 'overview' && state.lastTrendPoints.length) renderTrend(state.lastTrendPoints);
    });
    updateConnection(Boolean(state.managementKey));
    loadActivePage(true);
    startAutoRefresh();
  }

  function startAutoRefresh() {
    document.addEventListener('visibilitychange', handleVisibilityChange);
    scheduleAutoRefresh();
  }

  function handleVisibilityChange() {
    if (document.visibilityState !== 'visible') {
      cancelAutoRefresh();
      state.loadController?.abort();
      state.eventController?.abort();
      return;
    }
    if (state.managementKey && state.page !== 'settings') loadActivePage(false);
    else scheduleAutoRefresh();
  }

  function cancelAutoRefresh() {
    if (!autoRefreshTimer) return;
    window.clearTimeout(autoRefreshTimer);
    autoRefreshTimer = 0;
  }

  function scheduleAutoRefresh(delayOverride) {
    cancelAutoRefresh();
    if (document.visibilityState !== 'visible') return;
    if (!state.managementKey || state.page === 'settings') return;
    const elapsed = state.lastRefreshAt ? Date.now() - state.lastRefreshAt : 0;
    const delay = Number.isFinite(delayOverride)
      ? delayOverride
      : state.lastRefreshAt ? Math.max(0, AUTO_REFRESH_MS - elapsed) : AUTO_REFRESH_MS;
    autoRefreshTimer = window.setTimeout(runAutoRefresh, delay);
  }

  async function runAutoRefresh() {
    autoRefreshTimer = 0;
    if (document.visibilityState !== 'visible' || !state.managementKey || state.page === 'settings') return;
    if (state.loading || state.pendingRequests > 0) {
      scheduleAutoRefresh(1_000);
      return;
    }
    await loadActivePage(false);
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
      closeDrawer(false);
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
      state.eventFilters = {};
      $('#event-filters').reset();
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
    $('#event-reset').addEventListener('click', (event) => {
      event.preventDefault();
      $('#event-filters').reset();
      state.eventFilters = {};
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

  function bindTrendInteractiveLegend() {
    $$('#trend-legend .legend-chip').forEach((chip) => chip.setAttribute('aria-pressed', 'true'));
    $('#trend-legend').addEventListener('click', (event) => {
      const chip = event.target.closest('.legend-chip');
      if (!chip) return;
      const dim = chip.dataset.dim;
      state.trendActiveDims[dim] = !state.trendActiveDims[dim];
      chip.classList.toggle('is-active', state.trendActiveDims[dim]);
      chip.setAttribute('aria-pressed', String(state.trendActiveDims[dim]));
      renderTrend(state.lastTrendPoints);
    });
  }

  function bindHealthInteractivity() {
    const grid = $('#health-grid');
    const tooltip = $('#floating-tooltip');
    grid.addEventListener('mouseover', (event) => {
      const cell = event.target.closest('.health-cell');
      if (!cell) return;
      tooltip.innerHTML = `<div class="fgt-title">${esc(cell.dataset.time)}</div>
        <div class="fgt-row"><span>总请求数</span><strong>${formatInt(cell.dataset.reqs)} 次</strong></div>
        <div class="fgt-row"><span>成功 / 失败</span><strong style="color:var(--green)">${formatInt(cell.dataset.succs)}</strong> / <strong style="color:var(--red)">${formatInt(cell.dataset.fails)}</strong></div>
        <div class="fgt-row"><span>成功率</span><strong>${esc(cell.dataset.rate)}</strong></div>`;
      tooltip.classList.add('is-visible');
    });
    grid.addEventListener('mousemove', (event) => positionFloatingTooltip(tooltip, event));
    grid.addEventListener('mouseleave', () => tooltip.classList.remove('is-visible'));
  }

  function bindDistributionInteractivity() {
    const container = $('#distribution-grid');
    const tooltip = $('#floating-tooltip');
    container.addEventListener('mouseover', (event) => {
      const item = event.target.closest('.donut-segment, .distribution-row');
      const card = event.target.closest('.distribution-card');
      if (!item || !card) return;
      const { idx, name, reqs, pct, color } = item.dataset;
      const layout = card.querySelector('.donut-layout');
      layout.classList.add('has-hover');
      layout.querySelectorAll('.donut-segment, .distribution-row').forEach((element) => {
        element.classList.toggle('is-active', element.dataset.idx === idx);
      });
      const nameElement = card.querySelector('.dct-name');
      const valueElement = card.querySelector('.dct-val');
      nameElement.textContent = name;
      nameElement.style.color = color;
      valueElement.textContent = pct;
      tooltip.innerHTML = `<div class="fgt-title" style="color:${color}">${esc(name)}</div>
        <div class="fgt-row"><span>请求占比</span><strong>${esc(pct)}</strong></div>
        <div class="fgt-row"><span>累计请求数</span><strong>${esc(reqs)} 次</strong></div>`;
      tooltip.classList.add('is-visible');
    });
    container.addEventListener('mousemove', (event) => positionFloatingTooltip(tooltip, event));
    container.addEventListener('mouseout', (event) => {
      const card = event.target.closest('.distribution-card');
      if (!card || card.contains(event.relatedTarget)) return;
      const layout = card.querySelector('.donut-layout');
      layout.classList.remove('has-hover');
      layout.querySelectorAll('.donut-segment, .distribution-row').forEach((element) => element.classList.remove('is-active'));
      const nameElement = card.querySelector('.dct-name');
      const valueElement = card.querySelector('.dct-val');
      nameElement.textContent = '占比率';
      nameElement.style.color = 'var(--muted)';
      valueElement.textContent = 'TOP 5';
      tooltip.classList.remove('is-visible');
    });
  }

  function positionFloatingTooltip(tooltip, event) {
    const halfWidth = Math.max(95, tooltip.offsetWidth / 2);
    const left = Math.max(halfWidth + 12, Math.min(window.innerWidth - halfWidth - 12, event.clientX));
    tooltip.style.left = `${left}px`;
    tooltip.style.top = `${event.clientY}px`;
    tooltip.style.transform = event.clientY < tooltip.offsetHeight + 28
      ? 'translate(-50%, 18px)'
      : 'translate(-50%, -100%) translateY(-14px)';
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
      if (button) openUpstream(button.dataset.upstream, button);
    });
    document.addEventListener('keydown', (event) => {
      if (event.key === 'Escape' && $('#detail-drawer').classList.contains('is-open')) closeDrawer();
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
    state.loadController?.abort();
    const controller = new AbortController();
    const requestID = ++state.loadRequestID;
    const page = state.page;
    state.loadController = controller;
    setLoading(true);
    try {
      if (page === 'overview') await loadOverview(force, controller.signal, requestID);
      if (page === 'interfaces') await loadInterfaces(force, controller.signal, requestID);
      if (page === 'settings') await loadSettings(force, controller.signal, requestID);
      if (requestID !== state.loadRequestID) return;
      updateConnection(true);
    } catch (error) {
      if (error.name !== 'AuthRequired' && error.name !== 'AbortError') {
        toast(error.message || '加载失败', true);
        updateConnection(false);
      }
    } finally {
      if (requestID !== state.loadRequestID) return;
      state.loadController = null;
      setLoading(false);
      state.lastRefreshAt = Date.now();
      scheduleAutoRefresh();
    }
  }

  function readSessionCache(key) {
    try {
      const raw = sessionStorage.getItem(`usage-keeper-cache:${key}`);
      if (!raw) return null;
      const parsed = JSON.parse(raw);
      if (parsed && parsed.expiresAt > Date.now()) return parsed.data;
      sessionStorage.removeItem(`usage-keeper-cache:${key}`);
    } catch (_) {}
    return null;
  }

  function writeSessionCache(key, data) {
    try {
      sessionStorage.setItem(`usage-keeper-cache:${key}`, JSON.stringify({
        data,
        expiresAt: Date.now() + FRONTEND_CACHE_TTL_MS
      }));
    } catch (_) {}
  }

  async function cached(path, force = false, signal) {
    const requestURL = new URL(path, window.location.origin);
    if (!requestURL.searchParams.has('range')) requestURL.searchParams.set('range', state.range);
    const requestPath = requestURL.pathname + requestURL.search;
    let entry = state.cache.get(requestPath);
    if (!entry) {
      const stored = readSessionCache(requestPath);
      if (stored) {
        entry = { data: stored, expiresAt: Date.now() + FRONTEND_CACHE_TTL_MS };
        state.cache.set(requestPath, entry);
      }
    }
    if (!force && entry && entry.expiresAt > Date.now()) return entry.data;
    if (entry) state.cache.delete(requestPath);
    const data = await api(requestPath, { signal });
    while (state.cache.size >= FRONTEND_CACHE_MAX_ITEMS) {
      state.cache.delete(state.cache.keys().next().value);
    }
    state.cache.set(requestPath, { data, expiresAt: Date.now() + FRONTEND_CACHE_TTL_MS });
    writeSessionCache(requestPath, data);
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
    state.pendingRequests += 1;
    try {
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
    } finally {
      state.pendingRequests -= 1;
    }
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

  async function loadOverview(force, signal, requestID) {
    state.eventController?.abort();
    state.eventController = null;
    const eventRequestID = ++state.eventRequestID;
    const params = eventParams();
    const [summary, analysis, events] = await Promise.all([
      cached('/summary', force, signal),
      cached('/analysis', force, signal),
      cached(`/events?${params}`, force, signal),
    ]);
    if (requestID !== state.loadRequestID || signal.aborted) return;
    renderKPIs(summary.kpi || {}, summary.trend || []);
    renderTrend(summary.trend || []);
    renderRuntime(summary.runtime || {});
    requestAnimationFrame(() => {
      if (requestID !== state.loadRequestID || signal.aborted) return;
      renderHealth(summary.health || []);
      renderAnalysis(analysis);
      renderEventOptions(analysis);
      requestAnimationFrame(() => {
        if (requestID !== state.loadRequestID || signal.aborted) return;
        if (eventRequestID === state.eventRequestID) {
          state.eventPages = events.pages || 0;
          renderEvents(events);
        }
      });
    });
  }

  function makeSparkline(points, key, color, id) {
    const source = points.length ? points : [{ [key]: 0 }, { [key]: 0 }];
    const count = Math.min(14, source.length);
    const values = Array.from({ length: count }, (_, index) => {
      const sourceIndex = count === 1 ? 0 : Math.round(index * (source.length - 1) / (count - 1));
      return Number(source[sourceIndex]?.[key] || 0);
    });
    if (values.length === 1) values.push(values[0]);
    const width = 240, height = 40;
    const min = Math.min(...values), max = Math.max(...values);
    const path = values.map((value, index) => {
      const x = index * width / (values.length - 1);
      const y = height - 4 - (value - min) / (max - min || 1) * (height - 10);
      return `${index ? 'L' : 'M'} ${x.toFixed(1)} ${y.toFixed(1)}`;
    }).join(' ');
    const gradient = `spark-gradient-${id}`;
    return `<svg class="sparkline-svg" viewBox="0 0 ${width} ${height}" preserveAspectRatio="none" aria-hidden="true"><defs><linearGradient id="${gradient}" x1="0" y1="0" x2="0" y2="1"><stop offset="0%" stop-color="${color}" stop-opacity=".38"/><stop offset="100%" stop-color="${color}" stop-opacity="0"/></linearGradient></defs><path d="${path} L ${width} ${height - 1} L 0 ${height - 1} Z" fill="url(#${gradient})" stroke="none"/><path d="${path}" fill="none" stroke="${color}" stroke-width="2.2" stroke-linecap="round"/></svg>`;
  }

  function renderKPIs(kpi, trend) {
    const rangeLabels = { '24h': '24 小时', '7d': '7 天', '30d': '30 天', all: '全部' };
    const rangeLabel = rangeLabels[kpi.range_label] || kpi.range_label || rangeLabels[state.range];
    $('#overview-kpis').innerHTML = `<div class="kpi-row-top">
      <article class="kpi-panel"><div class="kpi-header"><h3 class="kpi-title">日均用量</h3><span class="kpi-badge-pill">统计范围 ${esc(rangeLabel)}</span></div><div class="kpi-daily-list">
        <div class="kpi-daily-item"><span class="kpi-daily-label"><i class="icon-blue">${icon('activity')}</i>日均请求</span><strong class="kpi-daily-val kpi-num-animate">${formatCompact(kpi.avg_requests_daily)}</strong></div>
        <div class="kpi-daily-item"><span class="kpi-daily-label"><i class="icon-purple">${icon('diamond')}</i>日均 Token</span><strong class="kpi-daily-val kpi-num-animate">${formatCompact(kpi.avg_tokens_daily)}</strong></div>
        <div class="kpi-daily-item"><span class="kpi-daily-label"><i class="icon-orange">${icon('dollar')}</i>日均费用</span><strong class="kpi-daily-val kpi-num-animate">${formatMoney(kpi.avg_cost_daily)}</strong></div>
      </div></article>
      <article class="kpi-panel theme-blue"><div class="kpi-header"><h3 class="kpi-title">总请求数</h3><div class="kpi-icon-badge theme-blue">${icon('activity')}</div></div><strong class="kpi-main-val kpi-num-animate">${formatInt(kpi.requests)}</strong><div class="kpi-sub-info"><span class="dot-success">成功: ${formatInt(kpi.successes)}</span><span class="dot-failed">失败: ${formatInt(kpi.failures)}</span><span class="plain-item">成功率: ${formatPercent(kpi.success_rate)}</span></div><div class="sparkline-box" style="--card-theme:var(--blue)">${makeSparkline(trend, 'requests', '#326ff5', 'requests')}</div></article>
      <article class="kpi-panel theme-purple"><div class="kpi-header"><h3 class="kpi-title">总 Token 消耗</h3><div class="kpi-icon-badge theme-purple">${icon('diamond')}</div></div><strong class="kpi-main-val kpi-num-animate">${formatCompact(kpi.total_tokens)}</strong><div class="kpi-sub-info"><span class="plain-item">缓存读取: ${formatCompact(kpi.cache_read_tokens)}</span><span class="plain-item">缓存写入: ${formatCompact(kpi.cache_write_tokens)}</span><span class="plain-item">推理: ${formatCompact(kpi.reasoning_tokens)}</span></div><div class="sparkline-box" style="--card-theme:var(--purple)">${makeSparkline(trend, 'tokens', '#7738ee', 'tokens')}</div></article>
    </div><div class="kpi-row-bottom">
      <article class="kpi-panel theme-green"><div class="kpi-header"><h3 class="kpi-title">RPM（每分钟请求）</h3><div class="kpi-icon-badge theme-green">${icon('clock')}</div></div><strong class="kpi-main-val kpi-num-animate">${formatNumber(kpi.rpm, 2)}</strong><div class="kpi-sub-info"><span class="plain-item">总请求数: ${formatInt(kpi.requests)}</span></div><div class="sparkline-box" style="--card-theme:var(--green)">${makeSparkline(trend, 'requests', '#20b95a', 'rpm')}</div></article>
      <article class="kpi-panel theme-orange"><div class="kpi-header"><h3 class="kpi-title">TPM（每分钟 Token）</h3><div class="kpi-icon-badge theme-orange">${icon('trend-up')}</div></div><strong class="kpi-main-val kpi-num-animate">${formatCompact(kpi.tpm)}</strong><div class="kpi-sub-info"><span class="plain-item">总 Token: ${formatCompact(kpi.total_tokens)}</span></div><div class="sparkline-box" style="--card-theme:var(--orange)">${makeSparkline(trend, 'tokens', '#ff7a12', 'tpm')}</div></article>
      <article class="kpi-panel theme-teal"><div class="kpi-header"><h3 class="kpi-title">缓存命中率</h3><div class="kpi-icon-badge theme-teal">${icon('percent')}</div></div><strong class="kpi-main-val kpi-num-animate">${formatPercent(kpi.cache_rate)}</strong><div class="kpi-sub-info"><span class="plain-item">缓存读取: ${formatCompact(kpi.cache_read_tokens)}</span><span class="plain-item">输入: ${formatCompact(kpi.input_tokens)}</span></div><div class="sparkline-box" style="--card-theme:var(--teal)">${makeSparkline(trend, 'hit_rate', '#18ad9d', 'cache')}</div></article>
      <article class="kpi-panel theme-yellow"><div class="kpi-header"><h3 class="kpi-title">总费用</h3><div class="kpi-icon-badge theme-yellow">${icon('dollar')}</div></div><strong class="kpi-main-val kpi-num-animate">${formatMoney(kpi.cost_usd)}</strong><div class="kpi-sub-info"><span class="plain-item">总 Token: ${formatCompact(kpi.total_tokens)}</span></div><div class="sparkline-box" style="--card-theme:var(--yellow)">${makeSparkline(trend, 'actual_cost', '#dda918', 'cost')}</div></article>
    </div>`;
  }

  function buildClampedSmoothPath(points, minY, maxY) {
    if (!points.length) return '';
    let path = `M ${points[0].x.toFixed(1)} ${points[0].y.toFixed(1)}`;
    for (let index = 0; index < points.length - 1; index += 1) {
      const current = points[index], next = points[index + 1];
      const dx = next.x - current.x, dy = next.y - current.y;
      const firstY = Math.min(maxY, Math.max(minY, current.y + dy * .12));
      const secondY = Math.min(maxY, Math.max(minY, next.y - dy * .12));
      path += ` C ${(current.x + dx * .32).toFixed(1)} ${firstY.toFixed(1)}, ${(next.x - dx * .32).toFixed(1)} ${secondY.toFixed(1)}, ${next.x.toFixed(1)} ${next.y.toFixed(1)}`;
    }
    return path;
  }

  function formatAxisNumber(value) {
    if (value >= 1e8) return `${(value / 1e8).toFixed(1)}亿`;
    if (value >= 1e4) return `${(value / 1e4).toFixed(0)}万`;
    if (value >= 1e3) return `${(value / 1e3).toFixed(0)}k`;
    return String(Math.round(value));
  }

  function renderTrend(rawPoints) {
    const host = $('#trend-chart');
    const normalizedPoints = rawPoints.map((point) => ({
      ...point,
      input: Number(point.input || 0), output: Number(point.output || 0),
      cache_write: Number(point.cache_write || 0), cache_read: Number(point.cache_read || 0),
      hit_rate: Number(point.hit_rate || 0), actual_cost: Number(point.actual_cost || point.cost_usd || 0),
      standard_cost: Number(point.standard_cost || 0),
    }));
    const points = normalizedPoints.filter((point) => point.requests > 0 || point.tokens > 0 ||
      point.input > 0 || point.output > 0 || point.cache_write > 0 || point.cache_read > 0 ||
      point.reasoning > 0 || point.actual_cost > 0 || point.standard_cost > 0);
    state.lastTrendPoints = points;
    if (!points.length) return empty(host, '当前范围没有用量');
    const styles = getComputedStyle(host);
    const horizontalPadding = parseFloat(styles.paddingLeft || '0') + parseFloat(styles.paddingRight || '0');
    const width = Math.max(720, Math.round(host.clientWidth - horizontalPadding)), height = 340, left = 65, right = 55, top = 25, bottom = 45;
    const plotWidth = width - left - right, plotHeight = height - top - bottom, zeroY = top + plotHeight;
    const active = state.trendActiveDims;
    const maxToken = Math.max(1, ...points.map((point) => Math.max(
      active.input ? point.input : 0, active.output ? point.output : 0,
      active.cache_write ? point.cache_write : 0, active.cache_read ? point.cache_read : 0,
    ))) * 1.15;
    const x = (index) => left + (points.length === 1 ? plotWidth / 2 : index * plotWidth / (points.length - 1));
    const tokenY = (value) => Math.min(zeroY, Math.max(top, zeroY - value / maxToken * plotHeight));
    const rateY = (value) => Math.min(zeroY, Math.max(top, zeroY - value * plotHeight));
    const leftGrid = [0, .25, .5, .75, 1].map((ratio) => {
      const y = top + plotHeight * (1 - ratio);
      return `<line class="${ratio ? 'chart-grid-line' : 'chart-zero-line'}" x1="${left}" y1="${y}" x2="${width-right}" y2="${y}"/><text class="chart-left-label" x="${left-10}" y="${y+4}">${formatAxisNumber(maxToken * ratio)}</text>`;
    }).join('');
    const rightGrid = [0, .2, .4, .6, .8, 1].map((ratio) => `<text class="chart-right-label" x="${width-right+10}" y="${top + plotHeight * (1-ratio) + 4}">${Math.round(ratio*100)}%</text>`).join('');
    const labelStep = Math.max(1, Math.floor(points.length / 7));
    const xLabels = points.map((point, index) => (index % labelStep === 0 || index === points.length - 1) ? `<text class="chart-x-label" x="${x(index)}" y="${zeroY+25}">${esc(formatTime(point.timestamp_ms, state.range))}</text>` : '').join('');
    const dimensions = [
      { key: 'cache_read', color: '#18ad9d', area: true },
      { key: 'input', color: '#326ff5' }, { key: 'output', color: '#20b95a' },
      { key: 'cache_write', color: '#ff7a12' }, { key: 'hit_rate', color: '#7738ee', dashed: true, rate: true },
    ];
    let linePaths = '', areaPaths = '', pointDots = '';
    dimensions.forEach((dimension) => {
      if (!active[dimension.key]) return;
      const pathPoints = points.map((point, index) => ({ x: x(index), y: dimension.rate ? rateY(point[dimension.key]) : tokenY(point[dimension.key]) }));
      const path = buildClampedSmoothPath(pathPoints, top, zeroY);
      if (dimension.area && pathPoints.length > 1) {
        areaPaths += `<path d="${path} L ${pathPoints.at(-1).x} ${zeroY - 1} L ${pathPoints[0].x} ${zeroY - 1} Z" fill="url(#area-gradient-cache)" stroke="none" class="animated-area"/>`;
      }
      linePaths += `<path d="${path}" fill="none" stroke="${dimension.color}" stroke-width="2.5" ${dimension.dashed ? 'stroke-dasharray="6 6"' : ''} stroke-linecap="round" stroke-linejoin="round" class="animated-line"/>`;
      if (points.length <= 120) {
        pathPoints.forEach((pt) => {
          pointDots += `<circle cx="${pt.x}" cy="${pt.y}" r="3.5" fill="${dimension.color}" stroke="var(--surface)" stroke-width="1.8" class="trend-point-dot"/>`;
        });
      }
    });

    const defs = `<defs>
      <clipPath id="chart-reveal-clip">
        <rect x="0" y="0" width="${width}" height="${height}" class="chart-clip-rect"/>
      </clipPath>
      <linearGradient id="area-gradient-cache" x1="0" y1="0" x2="0" y2="1">
        <stop offset="0%" stop-color="#18ad9d" stop-opacity=".24"/>
        <stop offset="100%" stop-color="#18ad9d" stop-opacity="0"/>
      </linearGradient>
    </defs>`;

    host.innerHTML = `<svg class="trend-svg-large" viewBox="0 0 ${width} ${height}" id="trend-svg-element" role="img" aria-label="输入、输出、缓存和缓存命中率趋势">${defs}${leftGrid}${rightGrid}<g clip-path="url(#chart-reveal-clip)">${areaPaths}${linePaths}${pointDots}</g>${xLabels}<line id="crosshair-line" class="chart-crosshair" x1="0" y1="${top}" x2="0" y2="${zeroY}" hidden/><g id="active-dots-group"></g></svg><div id="trend-tooltip" class="trend-tooltip-popup" hidden></div>`;
    const svg = $('#trend-svg-element'), crosshair = $('#crosshair-line'), dots = $('#active-dots-group'), tooltip = $('#trend-tooltip');
    host.onmousemove = (event) => {
      const rect = svg.getBoundingClientRect();
      const viewX = (event.clientX - rect.left) / rect.width * width;
      if (viewX < left || viewX > width-right) {
        crosshair.hidden = true; dots.innerHTML = ''; tooltip.hidden = true; return;
      }
      const step = points.length === 1 ? plotWidth : plotWidth / (points.length - 1);
      const index = points.length === 1 ? 0 : Math.max(0, Math.min(points.length - 1, Math.round((viewX-left)/step)));
      const point = points[index], pointX = x(index);
      crosshair.setAttribute('x1', pointX); crosshair.setAttribute('x2', pointX); crosshair.hidden = false;
      dots.innerHTML = dimensions.filter((dimension) => active[dimension.key]).map((dimension) => `<circle cx="${pointX}" cy="${dimension.rate ? rateY(point[dimension.key]) : tokenY(point[dimension.key])}" r="5.5" fill="${dimension.color}" stroke="var(--surface)" stroke-width="2.5" class="active-hover-dot"/>`).join('');
      const rows = [
        ['input', '输入', '#326ff5', formatCompact], ['output', '输出', '#20b95a', formatCompact],
        ['cache_write', '缓存创建', '#ff7a12', formatCompact], ['cache_read', '缓存读取', '#18ad9d', formatCompact],
        ['hit_rate', '缓存命中率', '#7738ee', formatPercent],
      ].filter(([key]) => active[key]).map(([key, label, color, formatter]) => `<div class="tt-row"><span class="tt-row-left"><span class="tt-box ${key === 'hit_rate' ? 'tt-box-dashed' : ''}" style="background:${color}"></span>${label}</span><strong>${formatter(point[key])}</strong></div>`).join('');
      tooltip.innerHTML = `<div class="tt-header">${esc(formatDateTime(point.timestamp_ms))}</div><div class="tt-body">${rows}</div><div class="tt-footer">实际费用: <strong>${formatMoney(point.actual_cost)}</strong> · 标准费用: <strong>${formatMoney(point.standard_cost)}</strong></div>`;
      const halfWidth = 135;
      tooltip.style.left = `${Math.max(halfWidth+12, Math.min(window.innerWidth-halfWidth-12, event.clientX))}px`;
      tooltip.style.top = `${event.clientY}px`;
      tooltip.style.transform = event.clientY < 250 ? 'translate(-50%, 16px)' : 'translate(-50%, -100%) translateY(-16px)';
      tooltip.hidden = false;
    };
    host.onmouseleave = () => { crosshair.hidden = true; dots.innerHTML = ''; tooltip.hidden = true; };
  }

  function renderHealth(points) {
    const host = $('#health-grid');
    const visible = points.slice(-(HEALTH_DAYS * HEALTH_SLOTS_PER_DAY));
    const requests = visible.reduce((sum, point) => sum + Number(point.requests || 0), 0);
    const failures = visible.reduce((sum, point) => sum + Number(point.failures || 0), 0);
    const successes = Math.max(0, requests - failures);
    $('#health-summary').textContent = requests ? formatPercent(successes / requests) : '--';
    $('#health-success-count').textContent = formatInt(successes);
    $('#health-failure-count').textContent = formatInt(failures);
    if (!visible.length) return empty(host, '暂无健康数据');

    host.innerHTML = Array.from({ length: HEALTH_DAYS }, (_, dayIndex) => {
      const day = visible.slice(dayIndex * HEALTH_SLOTS_PER_DAY, (dayIndex + 1) * HEALTH_SLOTS_PER_DAY);
      const label = day.length ? formatHealthDate(day[0].timestamp_ms) : '--';
      const cells = day.map((point) => {
        const pointRequests = Number(point.requests || 0);
        const pointFailures = Number(point.failures || 0);
        const pointSuccesses = Math.max(0, pointRequests - pointFailures);
        const rate = pointRequests ? pointSuccesses / pointRequests : 0;
        const level = healthLevel(pointSuccesses, pointFailures);
        const title = pointRequests
          ? `${formatHealthDateTime(point.timestamp_ms)} · 成功 ${formatInt(pointSuccesses)} · 失败 ${formatInt(pointFailures)} · ${formatPercent(rate)}`
          : `${formatHealthDateTime(point.timestamp_ms)} · 无请求`;
        return `<span class="health-cell level-${level}" data-time="${esc(formatHealthDateTime(point.timestamp_ms))}" data-reqs="${pointRequests}" data-succs="${pointSuccesses}" data-fails="${pointFailures}" data-rate="${pointRequests ? formatPercent(rate) : '--'}" title="${esc(title)}" aria-label="${esc(title)}"></span>`;
      }).join('');
      return `<div class="health-row"><span class="health-date">${esc(label)}</span><div class="health-cells">${cells}</div></div>`;
    }).join('');
  }

  function healthLevel(successes, failures) {
    const total = successes + failures;
    if (!total) return 0;
    const rate = successes / total;
    const greenThreshold = Math.min(.99, .9 + .045 * Math.max(0, Math.log10(total / 10)));
    if (rate < .5) return 1;
    if (rate < .65) return 2;
    if (rate < .8) return 3;
    if (rate < greenThreshold) return 4;
    return 5;
  }

  function renderRuntime(runtime) {
    const storage = runtime.storage || {};
    const queueCapacity = runtime.queue_capacity || 256;
    const items = [
      ['队列', `${formatInt(runtime.queue_depth || 0)} / ${formatInt(queueCapacity)}`, '#7738ee'],
      ['已接收', formatInt(runtime.accepted), '#326ff5'],
      ['已写入', formatInt(runtime.written), '#20b95a'],
      ['已丢弃', formatInt(runtime.dropped), '#e44e3f'],
      ['最近批写', `${formatNumber(runtime.last_batch_ms, 2)} ms`, '#dda918'],
    ];
    $('#runtime-strip').innerHTML = items.map(([label, value, color]) => `<div class="runtime-item"><span><i class="runtime-item-dot" style="background:${color}"></i>${label}</span><strong>${value}</strong></div>`).join('');
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
    const total = top.reduce((sum, item) => sum + (item.requests || 0), 0) || 1;
    const circumference = 2 * Math.PI * 38;
    let offset = 0;
    const segments = top.map((item, index) => {
      const percentage = ((item.requests || 0) / total * 100).toFixed(1);
      const length = (item.requests || 0) / total * circumference;
      const color = COLORS[index % COLORS.length];
      const segment = `<circle class="donut-segment" data-idx="${index}" data-name="${esc(item.name || '未识别')}" data-reqs="${formatInt(item.requests)}" data-pct="${percentage}%" data-color="${color}" cx="52" cy="52" r="38" stroke="${color}" stroke-dasharray="${length} ${circumference-length}" stroke-dashoffset="${-offset}"/>`;
      offset += length;
      return segment;
    }).join('');
    const list = top.length ? top.map((item, index) => {
      const percentage = ((item.requests || 0) / total * 100).toFixed(1);
      const color = COLORS[index % COLORS.length];
      return `<div class="distribution-row" data-idx="${index}" data-name="${esc(item.name || '未识别')}" data-reqs="${formatInt(item.requests)}" data-pct="${percentage}%" data-color="${color}"><i style="background:${color}"></i><span title="${esc(item.name)}">${esc(item.name || '未识别')}</span><strong>${formatCompact(item.requests)}</strong></div>`;
    }).join('') : '<div class="cell-sub">暂无数据</div>';
    return `<article class="distribution-card"><h2>${title}</h2><div class="donut-layout"><div class="donut-wrapper"><svg class="donut" viewBox="0 0 104 104" aria-hidden="true"><circle class="donut-track" cx="52" cy="52" r="38"/>${segments}</svg><div class="donut-center-text"><span class="dct-name">占比率</span><span class="dct-val">TOP 5</span></div></div><div class="distribution-list">${list}</div></div></article>`;
  }

  function renderTokenComposition(tokens) {
    const regularInput = Math.max(0, (tokens.input || 0) - (tokens.cache_read || 0) - (tokens.cache_write || 0));
    const regularOutput = Math.max(0, (tokens.output || 0) - (tokens.reasoning || 0));
    const parts = [
      ['输入', regularInput, '#326ff5', '0'],
      ['输出', regularOutput, '#20b95a', '1'],
      ['缓存读取', tokens.cache_read || 0, '#7738ee', '2'],
      ['缓存写入', tokens.cache_write || 0, '#ff7a12', '3'],
      ['推理', tokens.reasoning || 0, '#e44e3f', '4'],
    ];
    const sum = parts.reduce((total, [, value]) => total + value, 0) || 1;
    $('#token-total').textContent = formatCompact(tokens.total || 0);

    const stackItems = parts.map(([label, value, color, idx]) => {
      const pct = (value / sum * 100).toFixed(1);
      return `<span class="token-stack-segment" data-idx="${idx}" data-name="${label}" data-val="${formatInt(value)}" data-compact="${formatCompact(value)}" data-pct="${pct}%" data-color="${color}" style="width:${pct}%;background:${color}"></span>`;
    }).join('');

    const legendItems = parts.map(([label, value, color, idx]) => {
      const pct = (value / sum * 100).toFixed(1);
      return `<div class="token-legend-item" data-idx="${idx}" data-name="${label}" data-val="${formatInt(value)}" data-compact="${formatCompact(value)}" data-pct="${pct}%" data-color="${color}" style="border-color:${color}"><span>${label}</span><strong>${formatCompact(value)}</strong></div>`;
    }).join('');

    $('#token-composition').innerHTML = `<div class="token-stack">${stackItems}</div><div class="token-legend">${legendItems}</div>`;
  }

  function bindTokenInteractivity() {
    const container = $('#token-composition');
    const tooltip = $('#floating-tooltip');
    container.addEventListener('mouseover', (event) => {
      const item = event.target.closest('.token-stack-segment, .token-legend-item');
      if (!item) return;
      const { idx, name, val, compact, pct, color } = item.dataset;
      container.classList.add('has-hover');
      container.querySelectorAll('.token-stack-segment, .token-legend-item').forEach((el) => {
        el.classList.toggle('is-active', el.dataset.idx === idx);
      });
      tooltip.innerHTML = `<div class="fgt-title" style="color:${color}">${esc(name)} Token</div>
        <div class="fgt-row"><span>用量占比</span><strong>${esc(pct)}</strong></div>
        <div class="fgt-row"><span>Token 消耗</span><strong>${esc(val)} (${esc(compact)})</strong></div>`;
      tooltip.classList.add('is-visible');
    });
    container.addEventListener('mousemove', (event) => positionFloatingTooltip(tooltip, event));
    container.addEventListener('mouseout', (event) => {
      if (container.contains(event.relatedTarget)) return;
      container.classList.remove('has-hover');
      container.querySelectorAll('.token-stack-segment, .token-legend-item').forEach((el) => el.classList.remove('is-active'));
      tooltip.classList.remove('is-visible');
    });
  }

  function renderModelTable(models) {
    const body = $('#model-table');
    if (!models.length) return emptyRow(body, 6);
    body.innerHTML = models.map((item) => `<tr><td><span class="cell-main" title="${esc(item.name)}">${esc(item.name)}</span></td><td class="text-center">${formatInt(item.requests)}</td><td class="text-center">${statusBadge(item.success_rate)}</td><td class="text-center">${formatCompact(item.total_tokens)}</td><td class="text-center">${formatDuration(item.avg_latency_ms)}</td><td class="text-center">${formatMoney(item.cost_usd)}</td></tr>`).join('');
  }

  async function loadInterfaces(force, signal, requestID) {
    const data = await cached('/interfaces', force, signal);
    if (requestID !== state.loadRequestID || signal.aborted) return;
    renderInterfaceSummary(data);
    renderAPIKeys(data.api_keys || []);
    renderUpstreams(data.upstreams || []);
  }

  function renderInterfaceSummary(data) {
    const apiKeys = data.api_keys || [], upstreams = data.upstreams || [];
    const requests = upstreams.reduce((sum, item) => sum + (item.requests || 0), 0);
    const weightedLatency = requests ? upstreams.reduce((sum, item) => sum + Number(item.avg_latency_ms || 0) * Number(item.requests || 0), 0) / requests : 0;
    const healthy = upstreams.filter((item) => Number(item.success_rate || 0) >= .9).length;
    $('#interface-summary').innerHTML = `
      <div class="interface-card theme-blue"><div class="ic-head"><span>客户端 Key 凭证</span><div class="ic-icon">${icon('key')}</div></div><strong class="ic-val">${formatInt(apiKeys.length)} <small>个活跃 Key</small></strong></div>
      <div class="interface-card theme-purple"><div class="ic-head"><span>上游通道集群</span><div class="ic-icon">${icon('database')}</div></div><strong class="ic-val">${formatInt(upstreams.length)} <small>个 Provider</small></strong></div>
      <div class="interface-card theme-green"><div class="ic-head"><span>接口总请求量</span><div class="ic-icon">${icon('activity')}</div></div><strong class="ic-val">${formatCompact(requests)} <small>次</small></strong></div>
      <div class="interface-card theme-orange"><div class="ic-head"><span>上游平均响应延迟</span><div class="ic-icon">${icon('clock')}</div></div><strong class="ic-val">${formatDuration(weightedLatency)} <small>${formatInt(healthy)} 个健康</small></strong></div>`;
  }

  function renderAPIKeys(items) {
    const body = $('#api-key-table');
    if (!items.length) return emptyRow(body, 6);
    const total = items.reduce((sum, item) => sum + Number(item.requests || 0), 0) || 1;
    body.innerHTML = items.map((item) => {
      const percentage = Math.min(100, Number(item.requests || 0) / total * 100);
      return `<tr><td><span class="cell-main" style="font-family:monospace">${esc(item.name || '未识别')}</span></td><td>${formatInt(item.models)} 个模型</td><td><div class="progress-cell"><span>${formatInt(item.requests)} <small>(${percentage.toFixed(1)}%)</small></span><div class="progress-bar-bg"><div class="progress-bar-fill" style="width:${percentage.toFixed(2)}%;background:var(--blue)"></div></div></div></td><td>${statusBadge(item.success_rate)}</td><td>${formatCompact(item.total_tokens)}</td><td><strong>${formatMoney(item.cost_usd)}</strong></td></tr>`;
    }).join('');
  }

  function renderUpstreams(items) {
    const body = $('#upstream-table');
    if (!items.length) return emptyRow(body, 8);
    const total = items.reduce((sum, item) => sum + Number(item.requests || 0), 0) || 1;
    body.innerHTML = items.map((item) => {
      const percentage = Math.min(100, Number(item.requests || 0) / total * 100);
      const rate = Number(item.success_rate || 0);
      const healthLabel = rate >= .98 ? '正常在线' : rate >= .9 ? '轻微波动' : '需要关注';
      return `<tr><td><span class="cell-main">${esc(item.name || '未识别')}</span></td><td><span class="status-badge ${rate < .9 ? 'is-failure' : ''}">${healthLabel}</span></td><td>${formatInt(item.models)} 个模型</td><td><div class="progress-cell"><span>${formatInt(item.requests)} <small>(${percentage.toFixed(1)}%)</small></span><div class="progress-bar-bg"><div class="progress-bar-fill" style="width:${percentage.toFixed(2)}%;background:var(--purple)"></div></div></div></td><td>${statusBadge(rate)}</td><td>${formatDuration(item.avg_latency_ms)}</td><td>${formatCompact(item.total_tokens)}</td><td><button class="secondary-button compact-btn" data-upstream="${esc(item.key)}">${icon('eye')}详情</button></td></tr>`;
    }).join('');
  }

  async function openUpstream(key, trigger) {
    const drawer = $('#detail-drawer');
    state.drawerReturnFocus = trigger || document.activeElement;
    drawer.inert = false;
    drawer.classList.add('is-open');
    drawer.setAttribute('aria-hidden', 'false');
    $('#drawer-scrim').classList.add('is-open');
    $('#detail-title').textContent = '正在加载';
    $('#detail-content').innerHTML = '<div class="skeleton" style="height:180px"></div>';
    $('#detail-close').focus();
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

  function closeDrawer(restoreFocus = true) {
    const drawer = $('#detail-drawer');
    drawer.classList.remove('is-open');
    drawer.setAttribute('aria-hidden', 'true');
    drawer.inert = true;
    $('#drawer-scrim').classList.remove('is-open');
    const returnFocus = state.drawerReturnFocus;
    state.drawerReturnFocus = null;
    if (restoreFocus && returnFocus?.isConnected) returnFocus.focus();
  }

  async function loadEvents(force = false) {
    state.eventController?.abort();
    const controller = new AbortController();
    const requestID = ++state.eventRequestID;
    state.eventController = controller;
    const params = eventParams();
    try {
      const data = await cached(`/events?${params}`, force, controller.signal);
      if (requestID !== state.eventRequestID || controller.signal.aborted) return;
      state.eventPages = data.pages || 0;
      renderEvents(data);
    } catch (error) {
      if (error.name !== 'AbortError' && error.name !== 'AuthRequired') toast(error.message || '请求明细加载失败', true);
    } finally {
      if (requestID === state.eventRequestID) state.eventController = null;
    }
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
    if (!(data.events || []).length) return emptyRow(body, 11);
    body.innerHTML = data.events.map((event) => {
      const inputTokens = Math.max(0, Number(event.input_tokens || 0));
      const cacheReadTokens = Math.max(0, Number(event.cache_read_tokens || event.cached_tokens || 0));
      const cacheCreationTokens = Math.max(0, Number(event.cache_creation_tokens || 0));
      const uncachedInputTokens = Math.max(0, inputTokens - cacheReadTokens - cacheCreationTokens);
      const ttft = Number(event.ttft_ms || 0) > 0 ? formatDuration(event.ttft_ms) : '--';
      const status = event.failed ? `失败${event.status_code ? ` ${event.status_code}` : ''}` : '成功';
      return `<tr>
        <td><span class="cell-main">${esc(formatDateTime(event.timestamp_ms))}</span></td>
        <td><span class="cell-main" title="${esc(event.model)}">${esc(event.model)}</span><span class="cell-sub" title="${esc(event.upstream_label)}">${esc(event.upstream_label)}</span></td>
        <td class="text-center"><span class="cell-main">${esc(event.reasoning_effort || '--')}</span></td>
        <td class="text-center"><span class="status-badge ${event.failed ? 'is-failure' : ''}">${status}</span>${event.failure ? `<span class="cell-sub" title="${esc(event.failure)}">${esc(event.failure)}</span>` : ''}</td>
        <td class="text-center"><span class="cell-main">${formatDuration(event.latency_ms)}</span><span class="cell-sub">首字 ${ttft}</span></td>
        <td class="text-center">${formatInt(uncachedInputTokens)}</td>
        <td class="text-center">${formatInt(event.output_tokens)}</td>
        <td class="text-center">${formatInt(event.reasoning_tokens)}</td>
        <td class="text-center">${formatInt(cacheReadTokens)}</td>
        <td class="text-center">${formatInt(cacheCreationTokens)}</td>
        <td class="text-center"><strong>${formatInt(event.total_tokens)}</strong></td>
      </tr>`;
    }).join('');
  }

  async function loadSettings(force, signal, requestID) {
    const [settings, prices] = await Promise.all([api('/settings', { signal }), api('/prices', { signal })]);
    if (requestID !== state.loadRequestID || signal.aborted) return;
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
    row.innerHTML = fields.map((field, index) => `<td><input data-price-field="${field}" type="${index ? 'number' : 'text'}" ${index ? 'min="0" step="0.000001"' : 'placeholder="model-name"'} value="${esc(price[field] ?? '')}"></td>`).join('') + `<td><button class="icon-button" data-remove-price title="删除" aria-label="删除模型价格"><svg><use href="#i-trash"></use></svg></button></td>`;
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
      await loadActivePage(true);
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
  function formatHealthDate(value) { return new Intl.DateTimeFormat('zh-CN', { month: 'numeric', day: 'numeric' }).format(new Date(Number(value))); }
  function formatHealthDateTime(value) { return new Intl.DateTimeFormat('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }).format(new Date(Number(value))); }
  function formatTime(value, range) { return new Intl.DateTimeFormat('zh-CN', range === '24h' ? { hour: '2-digit', minute: '2-digit' } : { month: '2-digit', day: '2-digit' }).format(new Date(Number(value))); }
  function namedError(name, message) { const error = new Error(message); error.name = name; return error; }
})();
