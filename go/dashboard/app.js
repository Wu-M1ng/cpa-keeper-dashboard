(() => {
  'use strict';

  const API_ROOT = '/v0/management/plugins/usage-keeper';
  const CACHE_PREFIX = 'usage-keeper-cache:';
  const AUTO_REFRESH_MS = 60_000;
  const FRONTEND_CACHE_TTL_MS = 60_000;
  const FRONTEND_CACHE_MAX_ITEMS = 32;
  const CHINA_TIME_ZONE = 'Asia/Shanghai';
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
    managementKey: readManagementKey(),
    loading: false,
    pendingRequests: 0,
    refreshDeadline: 0,
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
  let resizeTimer = 0;

  const $ = (selector, root = document) => root.querySelector(selector);
  const $$ = (selector, root = document) => [...root.querySelectorAll(selector)];
  const icon = (name) => `<svg aria-hidden="true"><use href="#i-${name}"></use></svg>`;
  const esc = (value) => String(value ?? '').replace(/[&<>'"]/g, (char) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', "'": '&#39;', '"': '&quot;' })[char]);
  const number = new Intl.NumberFormat('zh-CN');
  const compact = new Intl.NumberFormat('en-US', { notation: 'compact', maximumFractionDigits: 1 });
  const money = new Intl.NumberFormat('zh-CN', { style: 'currency', currency: 'USD', minimumFractionDigits: 2, maximumFractionDigits: 4 });
  const dateTimeFormatter = new Intl.DateTimeFormat('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', timeZone: CHINA_TIME_ZONE });
  const healthDateFormatter = new Intl.DateTimeFormat('zh-CN', { month: 'numeric', day: 'numeric', timeZone: CHINA_TIME_ZONE });
  const healthDateTimeFormatter = new Intl.DateTimeFormat('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', timeZone: CHINA_TIME_ZONE });
  const time24hFormatter = new Intl.DateTimeFormat('zh-CN', { hour: '2-digit', minute: '2-digit', timeZone: CHINA_TIME_ZONE });
  const timeDateFormatter = new Intl.DateTimeFormat('zh-CN', { month: '2-digit', day: '2-digit', timeZone: CHINA_TIME_ZONE });

  document.addEventListener('DOMContentLoaded', init);

  async function init() {
    initCPAThemeObserver();
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
    window.addEventListener('resize', scheduleTrendResize, { passive: true });
    updateConnection(Boolean(state.managementKey));
    setFreshness(state.managementKey ? '正在连接' : '需要 Management Key');
    await loadActivePage(false);
    startAutoRefresh();
  }

  function scheduleTrendResize() {
    if (resizeTimer) window.clearTimeout(resizeTimer);
    resizeTimer = window.setTimeout(() => {
      resizeTimer = 0;
      if (state.page === 'overview' && state.lastTrendPoints.length) renderTrend(state.lastTrendPoints);
    }, 120);
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
    const delay = Number.isFinite(delayOverride)
      ? delayOverride
      : state.refreshDeadline ? Math.max(0, state.refreshDeadline - Date.now()) : AUTO_REFRESH_MS;
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

  function syncCPATheme() {
    try {
      const host = window.parent !== window ? window.parent.document.documentElement : document.documentElement;
      const theme = host.dataset.theme || host.dataset.cpaTheme || '';
      const normalized = theme === 'white' || theme === 'light' ? 'light' : theme === 'black' || theme === 'dark' ? 'dark' : '';
      if (normalized) document.documentElement.dataset.cpaTheme = normalized;
      else delete document.documentElement.dataset.cpaTheme;
    } catch (_) {
      delete document.documentElement.dataset.cpaTheme;
    }
  }

  function initCPAThemeObserver() {
    syncCPATheme();
    if (window.parent !== window) {
      try {
        const observer = new MutationObserver(syncCPATheme);
        observer.observe(window.parent.document.documentElement, { attributes: true, attributeFilter: ['data-theme', 'data-cpa-theme'] });
      } catch (_) {}
    }
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

  function trimDecimal(num) {
    return String(Number(num.toFixed(2)));
  }

  function formatTokenCompact(value) {
    const amount = Number(value || 0);
    if (Math.abs(amount) >= 1_000_000) return trimDecimal(amount / 1_000_000) + 'M';
    if (Math.abs(amount) >= 1_000) return trimDecimal(amount / 1_000) + 'K';
    return formatInt(amount);
  }

  function cacheHitRate(inputTokens, cacheReadTokens) {
    const input = Number(inputTokens || 0);
    const cacheRead = Number(cacheReadTokens || 0);
    return input > 0 ? Math.max(0, Math.min(1, cacheRead / input)) : 0;
  }

  function bindTrendInteractiveLegend() {
    $$('#trend-legend .legend-chip').forEach((chip) => chip.setAttribute('aria-pressed', String(state.trendActiveDims[chip.dataset.dim] ?? true)));
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
      clearFrontendCache();
      loadActivePage(true);
    });
  }

  async function loadActivePage(force) {
    state.loadController?.abort();
    const controller = new AbortController();
    const requestID = ++state.loadRequestID;
    const page = state.page;
    state.loadController = controller;
    state.refreshDeadline = 0;
    let completed = false;
    setLoading(true);
    setFreshness('正在刷新', 'stale');
    try {
      if (page === 'overview') await loadOverview(force, controller.signal, requestID);
      if (page === 'interfaces') await loadInterfaces(force, controller.signal, requestID);
      if (page === 'settings') await loadSettings(force, controller.signal, requestID);
      if (requestID !== state.loadRequestID) return;
      updateConnection(true);
      setFreshness(`更新于 ${new Intl.DateTimeFormat('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' }).format(new Date())}`, 'fresh');
      completed = true;
    } catch (error) {
      if (error.name !== 'AuthRequired' && error.name !== 'AbortError') {
        toast(error.message || '加载失败', true);
        updateConnection(false);
        setFreshness('加载失败', 'error');
      }
    } finally {
      if (requestID !== state.loadRequestID) return;
      state.loadController = null;
      setLoading(false);
      scheduleAutoRefresh(completed ? undefined : 5_000);
    }
  }

  async function cached(path, force = false, signal) {
    const requestURL = new URL(path, window.location.origin);
    if (!requestURL.searchParams.has('range')) requestURL.searchParams.set('range', state.range);
    const requestPath = requestURL.pathname + requestURL.search;
    const cacheKey = `${CACHE_PREFIX}${cacheScope()}:${requestPath}`;

    let entry = state.cache.get(cacheKey);
    if (!entry && !force) {
      try {
        const raw = sessionStorage.getItem(cacheKey);
        if (raw) {
          const parsed = JSON.parse(raw);
          if (parsed && parsed.expiresAt > Date.now()) {
            entry = parsed;
            state.cache.set(cacheKey, entry);
          } else {
            sessionStorage.removeItem(cacheKey);
          }
        }
      } catch (_) { /* SessionStorage disabled or unavailable. */ }
    }

    if (!force && entry && entry.expiresAt > Date.now()) {
      trackCacheExpiry(entry.expiresAt);
      return entry.data;
    }
    if (entry) {
      state.cache.delete(cacheKey);
      try { sessionStorage.removeItem(cacheKey); } catch (_) {}
    }

    const data = await api(requestPath, { signal });
    while (state.cache.size >= FRONTEND_CACHE_MAX_ITEMS) {
      const oldestKey = state.cache.keys().next().value;
      state.cache.delete(oldestKey);
      try { sessionStorage.removeItem(oldestKey); } catch (_) {}
    }
    const newEntry = { data, expiresAt: Date.now() + FRONTEND_CACHE_TTL_MS };
    state.cache.set(cacheKey, newEntry);
    try { sessionStorage.setItem(cacheKey, JSON.stringify(newEntry)); } catch (_) {}
    trackCacheExpiry(newEntry.expiresAt);
    return data;
  }

  function trackCacheExpiry(expiresAt) {
    if (!state.refreshDeadline || expiresAt < state.refreshDeadline) state.refreshDeadline = expiresAt;
  }

  function cacheScope() {
    let hash = 2166136261;
    for (let index = 0; index < state.managementKey.length; index += 1) {
      hash ^= state.managementKey.charCodeAt(index);
      hash = Math.imul(hash, 16777619);
    }
    return (hash >>> 0).toString(36);
  }

  function clearFrontendCache() {
    state.cache.clear();
    state.refreshDeadline = 0;
    try {
      for (let index = sessionStorage.length - 1; index >= 0; index -= 1) {
        const key = sessionStorage.key(index);
        if (key?.startsWith(CACHE_PREFIX)) sessionStorage.removeItem(key);
      }
    } catch (_) { /* SessionStorage disabled or unavailable. */ }
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
        clearFrontendCache();
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
      state.pendingRequests += 1;
      const response = await fetch(path, { headers: { Authorization: `Bearer ${state.managementKey}` } });
      if (response.status === 401) {
        sessionStorage.removeItem('usage-keeper-management-key');
        state.managementKey = '';
        clearFrontendCache();
        updateConnection(false);
        showAuthDialog();
        throw namedError('AuthRequired', 'Management Key 已失效');
      }
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
      if (error.name !== 'AuthRequired') toast(error.message || '导出失败', true);
    } finally {
      state.pendingRequests = Math.max(0, state.pendingRequests - 1);
    }
  }

  async function loadOverview(force, signal, requestID) {
    state.eventController?.abort();
    state.eventController = null;
    const eventRequestID = ++state.eventRequestID;
    const params = eventParams();
    const summary = await cached('/summary', force, signal);
    if (requestID !== state.loadRequestID || signal.aborted) return;
    renderKPIs(summary.kpi || {}, summary.trend || []);
    renderTrend(summary.trend || []);
    renderRuntime(summary.runtime || {});
    renderHealth(summary.health || []);

    await yieldToBrowser();
    if (requestID !== state.loadRequestID || signal.aborted) return;
    const analysis = await cached('/analysis', force, signal);
    if (requestID !== state.loadRequestID || signal.aborted) return;
    renderAnalysis(analysis);
    renderEventOptions(analysis);

    await yieldToBrowser();
    if (requestID !== state.loadRequestID || signal.aborted || eventRequestID !== state.eventRequestID) return;
    const events = await cached(`/events?${params}`, force, signal);
    if (requestID !== state.loadRequestID || signal.aborted || eventRequestID !== state.eventRequestID) return;
    state.eventPages = events.pages || 0;
    renderEvents(events);
  }

  function yieldToBrowser() {
    return new Promise((resolve) => requestAnimationFrame(resolve));
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
      <article class="kpi-panel theme-daily"><div class="kpi-header"><h3 class="kpi-title">日均用量</h3><span class="kpi-badge-pill">统计范围 ${esc(rangeLabel)}</span></div><div class="kpi-daily-list">
        <div class="kpi-daily-item" style="--metric-accent: var(--blue);">
          <span class="kpi-di-icon">${icon('activity')}</span>
          <span class="kpi-di-copy"><span class="kpi-di-label">日均请求</span></span>
          <strong class="kpi-di-val kpi-num-animate">${formatCompact(kpi.avg_requests_daily)}</strong>
        </div>
        <div class="kpi-daily-item" style="--metric-accent: var(--purple);">
          <span class="kpi-di-icon">${icon('diamond')}</span>
          <span class="kpi-di-copy"><span class="kpi-di-label">日均 Token</span></span>
          <strong class="kpi-di-val kpi-num-animate">${formatCompact(kpi.avg_tokens_daily)}</strong>
        </div>
        <div class="kpi-daily-item" style="--metric-accent: var(--yellow);">
          <span class="kpi-di-icon">${icon('dollar')}</span>
          <span class="kpi-di-copy"><span class="kpi-di-label">日均费用</span></span>
          <strong class="kpi-di-val kpi-num-animate">${formatMoney(kpi.avg_cost_daily)}</strong>
        </div>
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
    return formatTokenCompact(value);
  }

  function renderTrend(rawPoints) {
    const host = $('#trend-chart');
    const normalizedPoints = rawPoints.map((point) => ({
      ...point,
      timestamp_ms: Number(point.timestamp_ms || 0),
      requests: Number(point.requests || 0),
      tokens: Number(point.tokens || point.total_tokens || 0),
      reasoning: Number(point.reasoning || point.reasoning_tokens || 0),
      input: Number(point.input || 0), output: Number(point.output || 0),
      cache_write: Number(point.cache_write || 0), cache_read: Number(point.cache_read || 0),
      hit_rate: Number(point.hit_rate || 0), actual_cost: Number(point.actual_cost || point.cost_usd || 0),
      standard_cost: Number(point.standard_cost || 0),
    })).sort((leftPoint, rightPoint) => leftPoint.timestamp_ms - rightPoint.timestamp_ms);
    const points = normalizedPoints.filter((point) => point.requests > 0 || point.tokens > 0 ||
      point.input > 0 || point.output > 0 || point.cache_write > 0 || point.cache_read > 0 ||
      point.reasoning > 0 || point.actual_cost > 0 || point.standard_cost > 0);
    state.lastTrendPoints = points;
    if (!points.length) return empty(host, '当前范围没有用量');

    const dimensions = [
      { key: 'input', label: '输入', color: '#326ff5', axis: 'token' },
      { key: 'output', label: '输出', color: '#20b95a', axis: 'token' },
      { key: 'cache_write', label: '缓存创建', color: '#ff7a12', axis: 'token' },
      { key: 'cache_read', label: '缓存读取', color: '#18ad9d', axis: 'token', area: true },
      { key: 'hit_rate', label: '缓存命中率', color: '#7738ee', axis: 'rate', dashed: true },
    ];
    const active = state.trendActiveDims;
    const activeDimensions = dimensions.filter((dim) => active[dim.key]);

    if (!activeDimensions.length) {
      return empty(host, '已取消所有指标，点击上方图例恢复');
    }

    const styles = getComputedStyle(host);
    const horizontalPadding = parseFloat(styles.paddingLeft || '0') + parseFloat(styles.paddingRight || '0');
    const width = Math.max(520, Math.round(host.clientWidth - horizontalPadding)), height = 340, left = 65, right = 55, top = 25, bottom = 45;
    const plotWidth = width - left - right, plotHeight = height - top - bottom, zeroY = top + plotHeight;

    const tokenKeys = dimensions.filter((dim) => dim.axis === 'token' && active[dim.key]).map((dim) => dim.key);
    const tokenValues = tokenKeys.flatMap((key) => points.map((p) => p[key] || 0));
    const hasTokenActive = tokenKeys.length > 0;
    const maxToken = hasTokenActive ? Math.max(1, ...tokenValues) * 1.15 : 1;

    const firstTimestamp = points[0].timestamp_ms;
    const lastTimestamp = points.at(-1).timestamp_ms;
    const timestampSpan = Math.max(0, lastTimestamp - firstTimestamp);
    const x = (index) => {
      if (points.length === 1 || timestampSpan === 0) return left + plotWidth / 2;
      return left + Math.max(0, Math.min(1, (points[index].timestamp_ms - firstTimestamp) / timestampSpan)) * plotWidth;
    };
    const tokenY = (value) => Math.min(zeroY, Math.max(top, zeroY - value / maxToken * plotHeight));
    const rateY = (value) => Math.min(zeroY, Math.max(top, zeroY - Math.max(0, Math.min(1, value)) * plotHeight));

    const leftGrid = [0, .25, .5, .75, 1].map((ratio) => {
      const y = top + plotHeight * (1 - ratio);
      const labelText = hasTokenActive ? formatTokenCompact(maxToken * ratio) : (ratio === 0 ? '0' : '无 Token 系列');
      return `<line class="${ratio ? 'chart-grid-line' : 'chart-zero-line'}" x1="${left}" y1="${y}" x2="${width-right}" y2="${y}"/><text class="chart-left-label" x="${left-10}" y="${y+4}">${labelText}</text>`;
    }).join('');
    const rightGrid = [0, .2, .4, .6, .8, 1].map((ratio) => `<text class="chart-right-label" x="${width-right+10}" y="${top + plotHeight * (1-ratio) + 4}">${Math.round(ratio*100)}%</text>`).join('');
    const labelStep = Math.max(1, Math.floor(points.length / 7));
    const xLabels = points.map((point, index) => (index % labelStep === 0 || index === points.length - 1) ? `<text class="chart-x-label" x="${x(index)}" y="${zeroY+25}">${esc(formatTime(point.timestamp_ms, state.range))}</text>` : '').join('');

    let linePaths = '', areaPaths = '', pointDots = '';
    dimensions.forEach((dimension) => {
      if (!active[dimension.key]) return;
      const pathPoints = points.map((point, index) => ({ x: x(index), y: dimension.axis === 'rate' ? rateY(point[dimension.key]) : tokenY(point[dimension.key]) }));
      const path = buildClampedSmoothPath(pathPoints, top, zeroY);
      if (dimension.area && pathPoints.length > 1) {
        areaPaths += `<path d="${path} L ${pathPoints.at(-1).x} ${zeroY - 1} L ${pathPoints[0].x} ${zeroY - 1} Z" fill="url(#area-gradient-cache)" stroke="none" class="animated-area"/>`;
      }
      linePaths += `<path d="${path}" fill="none" stroke="${dimension.color}" stroke-width="2.5" ${dimension.dashed ? 'stroke-dasharray="6 6"' : ''} stroke-linecap="round" stroke-linejoin="round" class="animated-line"/>`;
      if (points.length <= 60) {
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
    let hoverFrame = 0;
    let lastPointer = null;
    const clearHover = () => { crosshair.hidden = true; dots.replaceChildren(); tooltip.hidden = true; };
    const updateHover = (clientX, clientY) => {
      const rect = svg.getBoundingClientRect();
      if (!rect.width) return;
      const viewX = (clientX - rect.left) / rect.width * width;
      if (viewX < left || viewX > width - right) { clearHover(); return; }
      const ratio = plotWidth ? Math.max(0, Math.min(1, (viewX - left) / plotWidth)) : 0;
      const targetTimestamp = timestampSpan ? firstTimestamp + ratio * timestampSpan : firstTimestamp;
      const index = points.length === 1 ? 0 : points.reduce((closestIndex, point, pointIndex) =>
        Math.abs(point.timestamp_ms - targetTimestamp) < Math.abs(points[closestIndex].timestamp_ms - targetTimestamp) ? pointIndex : closestIndex, 0);
      const point = points[index], pointX = x(index);
      crosshair.setAttribute('x1', pointX); crosshair.setAttribute('x2', pointX); crosshair.hidden = false;
      dots.innerHTML = dimensions.filter((dimension) => active[dimension.key]).map((dimension) => `<circle cx="${pointX}" cy="${dimension.axis === 'rate' ? rateY(point[dimension.key]) : tokenY(point[dimension.key])}" r="5.5" fill="${dimension.color}" stroke="var(--surface)" stroke-width="2.5" class="active-hover-dot"/>`).join('');
      const rows = dimensions.filter((dimension) => active[dimension.key]).map((dimension) => {
        const val = point[dimension.key] || 0;
        const formatted = dimension.axis === 'rate' ? formatPercent(val) : formatTokenCompact(val);
        return `<div class="tt-row"><span class="tt-row-left"><span class="tt-box ${dimension.dashed ? 'tt-box-dashed' : ''}" style="background:${dimension.color}"></span>${dimension.label}</span><strong>${formatted}</strong></div>`;
      }).join('');
      tooltip.innerHTML = `<div class="tt-header">${esc(formatDateTime(point.timestamp_ms))}</div><div class="tt-body">${rows}</div><div class="tt-footer">实际费用: <strong>${formatMoney(point.actual_cost)}</strong> · 标准费用: <strong>${formatMoney(point.standard_cost)}</strong></div>`;
      const halfWidth = 135;
      tooltip.style.left = `${Math.max(halfWidth + 12, Math.min(window.innerWidth - halfWidth - 12, clientX))}px`;
      tooltip.style.top = `${clientY}px`;
      tooltip.style.transform = clientY < 250 ? 'translate(-50%, 16px)' : 'translate(-50%, -100%) translateY(-16px)';
      tooltip.hidden = false;
    };
    host.onmousemove = (event) => {
      lastPointer = { clientX: event.clientX, clientY: event.clientY };
      if (hoverFrame) return;
      hoverFrame = requestAnimationFrame(() => {
        hoverFrame = 0;
        if (lastPointer) updateHover(lastPointer.clientX, lastPointer.clientY);
      });
    };
    host.onmouseleave = () => {
      if (hoverFrame) cancelAnimationFrame(hoverFrame);
      hoverFrame = 0;
      lastPointer = null;
      clearHover();
    };
  }

  function renderHealth(points) {
    const host = $('#health-grid');
    const slotMilliseconds = 15 * 60 * 1000;
    const normalized = points.map((point) => ({
      ...point,
      timestamp_ms: Number(point.timestamp_ms || 0),
      requests: Number(point.requests || 0),
      failures: Number(point.failures || 0),
    })).filter((point) => point.timestamp_ms > 0).sort((leftPoint, rightPoint) => leftPoint.timestamp_ms - rightPoint.timestamp_ms);
    const latestTimestamp = normalized.at(-1)?.timestamp_ms || 0;
    const dayMilliseconds = 24 * 60 * 60 * 1000;
    const chinaOffsetMilliseconds = 8 * 60 * 60 * 1000;
    const latestDayStart = latestTimestamp
      ? Math.floor((latestTimestamp + chinaOffsetMilliseconds) / dayMilliseconds) * dayMilliseconds - chinaOffsetMilliseconds
      : 0;
    const firstSlot = latestDayStart - (HEALTH_DAYS - 1) * dayMilliseconds;
    const pointsBySlot = new Map(normalized.map((point) => [Math.floor(point.timestamp_ms / slotMilliseconds) * slotMilliseconds, point]));
    const visible = latestTimestamp ? Array.from({ length: HEALTH_DAYS * HEALTH_SLOTS_PER_DAY }, (_, index) => {
      const timestamp = firstSlot + index * slotMilliseconds;
      return pointsBySlot.get(timestamp) || { timestamp_ms: timestamp, requests: 0, failures: 0 };
    }) : [];
    const requests = visible.reduce((sum, point) => sum + Number(point.requests || 0), 0);
    const failures = visible.reduce((sum, point) => sum + Number(point.failures || 0), 0);
    const successes = Math.max(0, requests - failures);
    $('#health-summary').textContent = requests ? formatPercent(successes / requests) : '--';
    $('#health-success-count').textContent = formatInt(successes);
    $('#health-failure-count').textContent = formatInt(failures);
    if (!visible.length) return empty(host, '暂无健康数据');

    let rows = $$('.health-row', host);
    let cells = $$('.health-cell', host);
    if (rows.length !== HEALTH_DAYS || cells.length !== HEALTH_DAYS * HEALTH_SLOTS_PER_DAY) {
      const fragment = document.createDocumentFragment();
      for (let dayIndex = 0; dayIndex < HEALTH_DAYS; dayIndex += 1) {
        const row = document.createElement('div');
        row.className = 'health-row';
        const label = document.createElement('span');
        label.className = 'health-date';
        const cellHost = document.createElement('div');
        cellHost.className = 'health-cells';
        for (let slot = 0; slot < HEALTH_SLOTS_PER_DAY; slot += 1) {
          const cell = document.createElement('span');
          cell.className = 'health-cell level-0';
          cellHost.appendChild(cell);
        }
        row.append(label, cellHost);
        fragment.appendChild(row);
      }
      host.replaceChildren(fragment);
      rows = $$('.health-row', host);
      cells = $$('.health-cell', host);
    }

    rows.forEach((row, dayIndex) => {
      const first = visible[dayIndex * HEALTH_SLOTS_PER_DAY];
      const label = first ? formatHealthDate(first.timestamp_ms) : '--';
      const labelNode = $('.health-date', row);
      if (labelNode.textContent !== label) labelNode.textContent = label;
    });

    cells.forEach((cell, index) => {
      const point = visible[index] || {};
      const signature = `${Number(point.timestamp_ms || 0)}:${Number(point.requests || 0)}:${Number(point.failures || 0)}`;
      if (cell.dataset.healthSignature === signature) return;
      cell.dataset.healthSignature = signature;
      const pointRequests = Number(point.requests || 0);
      const pointFailures = Number(point.failures || 0);
      const pointSuccesses = Math.max(0, pointRequests - pointFailures);
      const rate = pointRequests ? pointSuccesses / pointRequests : 0;
      const level = healthLevel(pointSuccesses, pointFailures);
      const formattedTime = point.timestamp_ms ? formatHealthDateTime(point.timestamp_ms) : '--';
      const title = pointRequests
        ? `${formattedTime} · 成功 ${formatInt(pointSuccesses)} · 失败 ${formatInt(pointFailures)} · ${formatPercent(rate)}`
        : `${formattedTime} · 无请求`;
      cell.className = `health-cell level-${level}`;
      cell.dataset.time = formattedTime;
      cell.dataset.reqs = String(pointRequests);
      cell.dataset.succs = String(pointSuccesses);
      cell.dataset.fails = String(pointFailures);
      cell.dataset.rate = pointRequests ? formatPercent(rate) : '--';
      cell.title = title;
      cell.setAttribute('aria-label', title);
    });
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
    $('#runtime-strip').setAttribute('aria-label', `队列 ${formatInt(runtime.queue_depth || 0)} / ${formatInt(queueCapacity)}，已接收 ${formatInt(runtime.accepted)}，已写入 ${formatInt(runtime.written)}，已丢弃 ${formatInt(runtime.dropped)}`);
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
    const ranked = values.filter((item) => Number(item.requests || 0) > 0).slice().sort((leftItem, rightItem) => Number(rightItem.requests || 0) - Number(leftItem.requests || 0));
    const total = ranked.reduce((sum, item) => sum + Number(item.requests || 0), 0) || 1;
    const top = ranked.slice(0, 5);
    const topTotal = top.reduce((sum, item) => sum + Number(item.requests || 0), 0);
    const remainder = Math.max(0, total - topTotal);
    const chartItems = remainder > 0 ? [...top, { name: '其他', requests: remainder, other: true }] : top;
    const circumference = 2 * Math.PI * 38;
    let offset = 0;
    const segments = chartItems.map((item, index) => {
      const percentage = ((item.requests || 0) / total * 100).toFixed(1);
      const calculatedLength = Number(item.requests || 0) / total * circumference;
      // The final segment closes the ring; the legacy top-five path used: index === top.length - 1 ? circumference - offset
      const length = index === chartItems.length - 1 ? Math.max(0, circumference - offset) : calculatedLength;
      const color = item.other ? '#9aa7b8' : COLORS[index % COLORS.length];
      const segment = `<circle class="donut-segment" data-idx="${index}" data-name="${esc(item.name || '未识别')}" data-reqs="${formatInt(item.requests)}" data-pct="${percentage}%" data-color="${color}" tabindex="0" role="img" aria-label="${esc(item.name || '未识别')} ${percentage}%" cx="52" cy="52" r="38" stroke="${color}" style="--final-dasharray: ${length} ${circumference-length}; stroke-dasharray: var(--final-dasharray);" stroke-dashoffset="${-offset}"/>`;
      offset += length;
      return segment;
    }).join('');
    const list = top.length ? [...top, ...(remainder > 0 ? [{ name: '其他', requests: remainder, other: true }] : [])].map((item, index) => {
      const percentage = ((item.requests || 0) / total * 100).toFixed(1);
      const color = item.other ? '#9aa7b8' : COLORS[index % COLORS.length];
      return `<div class="distribution-row" data-idx="${index}" data-name="${esc(item.name || '未识别')}" data-reqs="${formatInt(item.requests)}" data-pct="${percentage}%" data-color="${color}" tabindex="0" role="button" aria-label="${esc(item.name || '未识别')} ${percentage}%"><i style="background:${color}"></i><span title="${esc(item.name)}">${esc(item.name || '未识别')}</span><strong>${formatCompact(item.requests)}</strong></div>`;
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
      return `<span class="token-stack-segment" data-idx="${idx}" data-name="${label}" data-val="${formatInt(value)}" data-compact="${formatCompact(value)}" data-pct="${pct}%" data-color="${color}" tabindex="0" role="button" aria-label="${label} Token ${formatTokenCompact(value)}" style="width:${pct}%;background:${color}"></span>`;
    }).join('');

    const legendItems = parts.map(([label, value, color, idx]) => {
      const pct = (value / sum * 100).toFixed(1);
      return `<div class="token-legend-item" data-idx="${idx}" data-name="${label}" data-val="${formatInt(value)}" data-compact="${formatCompact(value)}" data-pct="${pct}%" data-color="${color}" tabindex="0" role="button" aria-label="${label} Token ${formatTokenCompact(value)}" style="border-color:${color}"><span>${label}</span><strong>${formatCompact(value)}</strong></div>`;
    }).join('');

    $('#token-composition').innerHTML = `<div class="token-stack">${stackItems}</div><div class="token-legend">${legendItems}</div>`;
  }

  function hitRateBadge(rate) {
    const val = Number(rate || 0);
    const percentage = (val * 100).toFixed(1);
    const cls = val >= 0.5 ? 'is-high' : val > 0 ? 'is-mid' : '';
    return `<span class="hit-rate-badge ${cls}">${percentage}%</span>`;
  }

  function bindDistributionInteractivity() {
    const grid = $('#distribution-grid');
    const tooltip = $('#floating-tooltip');
    if (!grid) return;

    const activate = (item, event) => {
      const card = item?.closest('.distribution-card');
      if (!card) return;
      const { idx, name, reqs, pct, color } = item.dataset;
      card.classList.add('has-hover');
      card.querySelectorAll('.donut-segment, .distribution-row').forEach((el) => {
        el.classList.toggle('is-active', el.dataset.idx === idx);
      });
      const nameEl = card.querySelector('.dct-name');
      const valEl = card.querySelector('.dct-val');
      if (nameEl && valEl) {
        nameEl.textContent = name || '未识别';
        nameEl.style.color = color || 'var(--text)';
        valEl.textContent = `${pct}`;
      }
      tooltip.innerHTML = `<div class="fgt-title" style="color:${esc(color || 'var(--text)')}">${esc(name || '未识别')}</div><div class="fgt-row"><span>请求占比</span><strong>${esc(pct || '0%')}</strong></div><div class="fgt-row"><span>累计请求数</span><strong>${esc(reqs || '0')} 次</strong></div>`;
      tooltip.classList.add('is-visible');
      if (event?.clientX != null) positionFloatingTooltip(tooltip, event);
      else {
        const rect = item.getBoundingClientRect();
        positionFloatingTooltip(tooltip, { clientX: rect.left + rect.width / 2, clientY: rect.top });
      }
    };
    const clear = (card) => {
      if (!card) return;
      card.classList.remove('has-hover');
      card.querySelectorAll('.donut-segment, .distribution-row').forEach((el) => el.classList.remove('is-active'));
      const nameEl = card.querySelector('.dct-name');
      const valEl = card.querySelector('.dct-val');
      if (nameEl && valEl) { nameEl.textContent = '占比率'; nameEl.style.color = 'var(--muted)'; valEl.textContent = 'TOP 5'; }
      tooltip.classList.remove('is-visible');
    };

    grid.addEventListener('mouseover', (event) => {
      const item = event.target.closest('.donut-segment, .distribution-row');
      if (item) activate(item, event);
    });
    grid.addEventListener('mousemove', (event) => {
      if (tooltip.classList.contains('is-visible')) positionFloatingTooltip(tooltip, event);
    });

    grid.addEventListener('mouseout', (event) => {
      const card = event.target.closest('.distribution-card');
      if (card && !card.contains(event.relatedTarget)) clear(card);
    });
    grid.addEventListener('focusin', (event) => {
      const item = event.target.closest('.donut-segment, .distribution-row');
      if (item) activate(item);
    });
    grid.addEventListener('focusout', (event) => {
      const card = event.target.closest('.distribution-card');
      if (card && !card.contains(event.relatedTarget)) clear(card);
    });
    grid.addEventListener('keydown', (event) => {
      const item = event.target.closest('.donut-segment, .distribution-row');
      if (!item) return;
      if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); activate(item); }
      if (event.key === 'Escape') clear(item.closest('.distribution-card'));
    });
  }

  function bindTokenInteractivity() {
    const container = $('#token-composition');
    const tooltip = $('#floating-tooltip');
    let pinnedTrigger = null;

    function populateTokenTooltip(trigger) {
      const { in: tokenIn, out: tokenOut, cacheRead, cacheWrite, reasoning, total } = trigger.dataset;
      tooltip.innerHTML = `<div class="fgt-title">Token 明细</div>
        <div class="fgt-row"><span>输入 Token</span><strong>${esc(tokenIn)}</strong></div>
        <div class="fgt-row"><span>输出 Token</span><strong>${esc(tokenOut)}</strong></div>
        ${cacheRead && cacheRead !== '0' ? `<div class="fgt-row"><span>缓存读取 Token</span><strong>${esc(cacheRead)}</strong></div>` : ''}
        ${cacheWrite && cacheWrite !== '0' ? `<div class="fgt-row"><span>缓存创建 Token</span><strong>${esc(cacheWrite)}</strong></div>` : ''}
        ${reasoning && reasoning !== '0' ? `<div class="fgt-row"><span>思考/推理 Token</span><strong>${esc(reasoning)}</strong></div>` : ''}
        <div class="fgt-footer">总 Token: <strong>${esc(total)}</strong></div>`;
    }

    if (container) {
      container.addEventListener('mouseover', (event) => {
        const item = event.target.closest('.token-stack-segment, .token-legend-item');
        if (!item || pinnedTrigger) return;
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
      container.addEventListener('mousemove', (event) => {
        if (!pinnedTrigger) positionFloatingTooltip(tooltip, event);
      });
      container.addEventListener('mouseout', (event) => {
        if (pinnedTrigger || container.contains(event.relatedTarget)) return;
        container.classList.remove('has-hover');
        container.querySelectorAll('.token-stack-segment, .token-legend-item').forEach((el) => el.classList.remove('is-active'));
        tooltip.classList.remove('is-visible');
      });
    }

    document.addEventListener('mouseover', (event) => {
      if (pinnedTrigger) return;
      const trigger = event.target.closest('[data-tooltip-type="token-detail"]');
      if (!trigger) return;
      populateTokenTooltip(trigger);
      tooltip.classList.add('is-visible');
    });

    document.addEventListener('mousemove', (event) => {
      if (pinnedTrigger || !tooltip.classList.contains('is-visible')) return;
      const trigger = event.target.closest('[data-tooltip-type="token-detail"]');
      if (trigger) positionFloatingTooltip(tooltip, event);
    });

    document.addEventListener('mouseout', (event) => {
      if (pinnedTrigger) return;
      const trigger = event.target.closest('[data-tooltip-type="token-detail"]');
      if (trigger && !trigger.contains(event.relatedTarget)) {
        tooltip.classList.remove('is-visible');
      }
    });

    document.addEventListener('click', (event) => {
      const btn = event.target.closest('.token-info-trigger');
      const trigger = event.target.closest('[data-tooltip-type="token-detail"]');
      if (btn || trigger) {
        const targetCell = btn ? btn.closest('[data-tooltip-type="token-detail"]') : trigger;
        if (pinnedTrigger === targetCell) {
          pinnedTrigger = null;
          $$('.token-info-trigger.is-pinned').forEach((el) => el.classList.remove('is-pinned'));
          tooltip.classList.remove('is-visible');
        } else {
          pinnedTrigger = targetCell;
          $$('.token-info-trigger.is-pinned').forEach((el) => el.classList.remove('is-pinned'));
          const btnEl = targetCell?.querySelector('.token-info-trigger');
          if (btnEl) btnEl.classList.add('is-pinned');
          populateTokenTooltip(targetCell);
          positionFloatingTooltip(tooltip, event);
          tooltip.classList.add('is-visible');
        }
      } else if (pinnedTrigger && !tooltip.contains(event.target)) {
        pinnedTrigger = null;
        $$('.token-info-trigger.is-pinned').forEach((el) => el.classList.remove('is-pinned'));
        tooltip.classList.remove('is-visible');
      }
    });
  }

  function renderTokenCell(inputTokens, outputTokens, cacheReadTokens, cacheCreationTokens = 0, reasoningTokens = 0, totalTokens = 0) {
    const input = Math.max(0, Number(inputTokens || 0));
    const output = Math.max(0, Number(outputTokens || 0));
    const cacheRead = Math.max(0, Number(cacheReadTokens || 0));
    const cacheWrite = Math.max(0, Number(cacheCreationTokens || 0));
    const reasoning = Math.max(0, Number(reasoningTokens || 0));
    const total = Math.max(0, Number(totalTokens || (input + output + cacheWrite + reasoning)));
    const hasCache = cacheRead > 0;

    const svgDown = `<svg class="token-icon-arrow" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2.2" d="M12 5v14M5 12l7 7 7-7"/></svg>`;
    const svgUp = `<svg class="token-icon-arrow" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2.2" d="M12 19V5M5 12l7-7 7 7"/></svg>`;
    const svgCache = `<svg class="token-icon-cache" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 8h14M5 8a2 2 0 110-4h14a2 2 0 110 4M5 8v10a2 2 0 002 2h10a2 2 0 002-2V8m-9 4h4"/></svg>`;

    return `<div class="token-compound-cell" data-tooltip-type="token-detail"
      data-in="${formatInt(input)}" data-out="${formatInt(output)}"
      data-cache-read="${formatInt(cacheRead)}" data-cache-write="${formatInt(cacheWrite)}"
      data-reasoning="${formatInt(reasoning)}" data-total="${formatInt(total)}">
      <div class="token-cell-lines">
        <div class="token-line-primary">
          <span class="token-badge-in" title="输入 Token">${svgDown} ${formatTokenCompact(input)}</span>
          <span class="token-badge-out" title="输出 Token">${svgUp} ${formatTokenCompact(output)}</span>
        </div>
        ${hasCache ? `<div class="token-line-sub"><span class="token-badge-cache" title="缓存读取">${svgCache} ${formatTokenCompact(cacheRead)}</span></div>` : ''}
      </div>
      <button type="button" class="token-info-trigger" title="点击展开/固定 Token 明细" aria-label="查看 Token 明细">
        <svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="10"/><path d="M12 16v-4M12 8h.01"/></svg>
      </button>
    </div>`;
  }

  function renderModelTable(models) {
    const body = $('#model-table');
    if (!models.length) return emptyRow(body, 7);
    body.innerHTML = models.map((item) => {
      const input = item.tokens?.input || 0;
      const cacheRead = item.tokens?.cache_read || 0;
      const rate = cacheHitRate(input, cacheRead);
      const percentage = (rate * 100).toFixed(1);
      return `<tr>
        <td><span class="cell-main" title="${esc(item.name)}">${esc(item.name)}</span></td>
        <td class="text-center">${formatInt(item.requests)}</td>
        <td class="text-center">${statusBadge(item.success_rate)}</td>
        <td class="text-center"><div class="progress-cell"><span>${percentage}%</span><div class="progress-bar-bg"><div class="progress-bar-fill" style="width:${percentage}%;background:var(--purple)"></div></div></div></td>
        <td class="text-center" title="${formatInt(item.total_tokens)}">${formatTokenCompact(item.total_tokens)}</td>
        <td class="text-center">${formatDuration(item.avg_latency_ms)}</td>
        <td class="text-center">${formatMoney(item.cost_usd)}</td>
      </tr>`;
    }).join('');
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
    if (!(data.events || []).length) return emptyRow(body, 8);
    body.innerHTML = data.events.map((event) => {
      const inputTokens = Math.max(0, Number(event.input_tokens || 0));
      const cacheReadTokens = Math.max(0, Number(event.cache_read_tokens || event.cached_tokens || 0));
      const cacheCreationTokens = Math.max(0, Number(event.cache_creation_tokens || 0));
      const uncachedInputTokens = Math.max(0, inputTokens - cacheReadTokens - cacheCreationTokens);
      const outputTokens = Math.max(0, Number(event.output_tokens || 0));
      const reasoningTokens = Math.max(0, Number(event.reasoning_tokens || 0));
      const totalTokens = Math.max(0, Number(event.total_tokens || 0));
      const rate = cacheHitRate(inputTokens, cacheReadTokens);
      const ttft = Number(event.ttft_ms || 0) > 0 ? formatDuration(event.ttft_ms) : '--';
      const status = event.failed ? `失败${event.status_code ? ` ${event.status_code}` : ''}` : '成功';
      const tokenCellHtml = renderTokenCell(uncachedInputTokens, outputTokens, cacheReadTokens, cacheCreationTokens, reasoningTokens, totalTokens);
      return `<tr>
        <td data-label="时间"><span class="cell-main">${esc(formatDateTime(event.timestamp_ms))}</span></td>
        <td data-label="模型 / 渠道"><span class="cell-main" title="${esc(event.model)}">${esc(event.model)}</span><span class="cell-sub" title="${esc(event.upstream_label)}">${esc(event.upstream_label)}</span></td>
        <td data-label="推理强度" class="text-center"><span class="cell-main">${esc(event.reasoning_effort || '--')}</span></td>
        <td data-label="状态" class="text-center"><span class="status-badge ${event.failed ? 'is-failure' : ''}">${status}</span>${event.failure ? `<span class="cell-sub" title="${esc(event.failure)}">${esc(event.failure)}</span>` : ''}</td>
        <td data-label="用时 / 首字" class="text-center"><span class="cell-main">${formatDuration(event.latency_ms)}</span><span class="cell-sub">首字 ${ttft}</span></td>
        <td data-label="Token 明细" class="text-center">${tokenCellHtml}</td>
        <td data-label="命中率" class="text-center">${hitRateBadge(rate)}</td>
        <td data-label="总 Token" class="text-center" title="${formatInt(totalTokens)}"><strong>${formatTokenCompact(totalTokens)}</strong></td>
      </tr>`;
    }).join('');
  }

  async function loadSettings(force, signal, requestID) {
    const [settings, prices] = await Promise.all([api('/settings', { signal }), api('/prices', { signal })]);
    if (requestID !== state.loadRequestID || signal.aborted) return;
    renderStorage(settings);
    renderPrices(prices.prices || []);
    $('#auth-state').textContent = state.managementKey ? '已连接 · 密钥仅用于当前页面会话' : '未连接';
  }

  function renderStorage(data) {
    const config = data.config || {}, runtime = data.runtime || {}, storage = runtime.storage || {};
    $('#storage-path').textContent = storage.path || '内存数据库';
    $('#storage-settings [name="retention_days"]').value = config.retention_days || 30;
    $('#storage-settings [name="export_max_records"]').value = config.export_max_records || 50000;
    const metrics = [
      ['数据库', formatBytes(storage.database_bytes)], ['事件', formatInt(storage.event_count)],
      ['Rollup', formatInt(storage.rollup_count)], ['队列丢弃', formatInt(runtime.dropped)],
      ['写入失败', formatInt(runtime.write_failures)], ['最近批写', `${formatNumber(runtime.last_batch_ms, 2)} ms`],
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
      clearFrontendCache();
      toast('模型价格已保存');
    } catch (error) { toast(error.message, true); }
  }

  async function saveStorageSettings(event) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    try {
      const data = await api('/settings', { method: 'PUT', body: JSON.stringify({ retention_days: Number(form.get('retention_days')), export_max_records: Number(form.get('export_max_records')) }) });
      renderStorage(data);
      clearFrontendCache();
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
      clearFrontendCache();
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

  function setFreshness(label, status = '') {
    const element = $('#freshness-state');
    if (!element) return;
    element.textContent = label;
    element.classList.toggle('is-fresh', status === 'fresh');
    element.classList.toggle('is-stale', status === 'stale');
    element.classList.toggle('is-error', status === 'error');
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
  function formatDateTime(value) { return dateTimeFormatter.format(new Date(Number(value))); }
  function formatHealthDate(value) { return healthDateFormatter.format(new Date(Number(value))); }
  function formatHealthDateTime(value) { return healthDateTimeFormatter.format(new Date(Number(value))); }
  function formatTime(value, range) { return (range === '24h' ? time24hFormatter : timeDateFormatter).format(new Date(Number(value))); }
  function namedError(name, message) { const error = new Error(message); error.name = name; return error; }
})();
