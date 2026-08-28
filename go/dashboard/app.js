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
    providerTokenActiveDims: { input: true, output: true, cache_read: true, cache_write: true, reasoning: true },
    lastTrendPoints: [],
    lastInterfacesData: null,
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
  const money = new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD', currencyDisplay: 'narrowSymbol', minimumFractionDigits: 2, maximumFractionDigits: 4 });
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
    bindProviderTokenInteractivity();
    initCustomSelects();
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

  let tooltipRafId = 0;
  function positionFloatingTooltip(tooltip, event) {
    if (!event) return;
    const clientX = event.clientX;
    const clientY = event.clientY;
    cancelAnimationFrame(tooltipRafId);
    tooltipRafId = requestAnimationFrame(() => {
      const halfWidth = 110;
      const left = Math.max(halfWidth + 12, Math.min(window.innerWidth - halfWidth - 12, clientX));
      tooltip.style.left = `${left}px`;
      tooltip.style.top = `${clientY}px`;
      tooltip.style.transform = clientY < 120
        ? 'translate(-50%, 18px)'
        : 'translate(-50%, -100%) translateY(-14px)';
    });
  }

  function bindSettings() {
    $('#add-price').addEventListener('click', () => appendPriceRow({}));
    $('#save-prices').addEventListener('click', savePrices);
    $('#export-prices-btn').addEventListener('click', exportPricesJSON);
    $('#autofill-prices-btn').addEventListener('click', autoFillUnpricedModels);

    // Search filter
    $('#price-search').addEventListener('input', (e) => applyPriceFilter(e.target.value));

    // Model Picker Dropdown
    let currentPickerRow = null;
    const pickerDropdown = $('#model-picker-dropdown');

    function closeModelPicker() {
      if (pickerDropdown) {
        pickerDropdown.classList.remove('is-open');
        currentPickerRow = null;
      }
    }

    // Table click actions (picker, clone, remove)
    $('#price-table').addEventListener('click', (event) => {
      const pickerBtn = event.target.closest('.model-picker-btn');
      if (pickerBtn) {
        event.stopPropagation();
        const row = pickerBtn.closest('tr');
        if (currentPickerRow === row && pickerDropdown.classList.contains('is-open')) {
          closeModelPicker();
          return;
        }
        currentPickerRow = row;
        const currentModel = (row.querySelector('[data-price-field="model"]')?.value || '').trim();
        const configuredElsewhere = new Set();
        $$('#price-table tr').forEach((r) => {
          if (r === row) return;
          const m = (r.querySelector('[data-price-field="model"]')?.value || '').trim();
          if (m) configuredElsewhere.add(m.toLowerCase());
        });

        const requested = getRequestedModelsList();
        
        let html = '';
        if (requested.length > 0) {
          html += `<div class="mpd-section-title"><span>⚡ 已产生调用的模型</span><span class="mpd-count-tag">${requested.length} 个</span></div>`;
          requested.forEach((item) => {
            const isCurrent = Boolean(currentModel && item.name.toLowerCase() === currentModel.toLowerCase());
            const isConfigured = configuredElsewhere.has(item.name.toLowerCase());

            if (isCurrent) {
              html += `
                <button type="button" class="mpd-item is-current" data-select-model="${esc(item.name)}" title="当前行已设定此模型">
                  <span class="mpd-item-left">
                    <svg class="mpd-check-icon" viewBox="0 0 24 24"><polyline points="20 6 9 17 4 12"/></svg>
                    <span class="mpd-item-name">${esc(item.name)}</span>
                  </span>
                  <span class="mpd-item-badge is-current-badge">当前已选 (${item.requests ? `${formatInt(item.requests)} 次` : '已调用'})</span>
                </button>
              `;
            } else if (isConfigured) {
              html += `
                <button type="button" class="mpd-item is-disabled" disabled title="该模型已在价格表中设定">
                  <span class="mpd-item-left">
                    <span class="mpd-item-name">${esc(item.name)}</span>
                  </span>
                  <span class="mpd-item-badge is-disabled-badge">已在价格表中</span>
                </button>
              `;
            } else {
              html += `
                <button type="button" class="mpd-item" data-select-model="${esc(item.name)}">
                  <span class="mpd-item-left">
                    <span class="mpd-item-name">${esc(item.name)}</span>
                  </span>
                  <span class="mpd-item-badge">${item.requests ? `${formatInt(item.requests)} 次请求` : '已调用'}</span>
                </button>
              `;
            }
          });
        } else {
          html += `<div class="mpd-section-title"><span>⚡ 已产生调用的模型</span></div><div class="cell-sub" style="padding:8px 10px">暂无请求记录，可直接在输入框中自定义输入</div>`;
        }

        pickerDropdown.innerHTML = html;
        
        const rect = pickerBtn.getBoundingClientRect();
        const dropdownWidth = Math.min(320, window.innerWidth - 24);
        let left = rect.left + rect.width - dropdownWidth;
        if (left < 12) left = 12;
        let top = rect.bottom + 6;
        if (top + 360 > window.innerHeight && rect.top > 360) {
          top = rect.top - 366;
        }
        
        pickerDropdown.style.left = `${left}px`;
        pickerDropdown.style.top = `${top}px`;
        pickerDropdown.style.width = `${dropdownWidth}px`;
        pickerDropdown.classList.add('is-open');
        return;
      }

      const removeBtn = event.target.closest('[data-remove-price]');
      if (removeBtn) {
        removeBtn.closest('tr').remove();
        return;
      }
      const cloneBtn = event.target.closest('[data-clone-price]');
      if (cloneBtn) {
        const row = cloneBtn.closest('tr');
        const price = {};
        $$('[data-price-field]', row).forEach((input) => {
          price[input.dataset.priceField] = input.dataset.priceField === 'model' ? (input.value ? `${input.value}-copy` : '') : input.value;
        });
        appendPriceRow(price);
        toast('已复制模型行');
      }
    });

    pickerDropdown.addEventListener('click', (event) => {
      const itemBtn = event.target.closest('[data-select-model]');
      if (itemBtn && currentPickerRow) {
        const modelName = itemBtn.dataset.selectModel;
        applyModelToRow(currentPickerRow, modelName);
        closeModelPicker();
        toast(`已选择模型 ${modelName}`);
      }
    });

    document.addEventListener('click', (event) => {
      if (pickerDropdown.classList.contains('is-open') && !pickerDropdown.contains(event.target) && !event.target.closest('.model-picker-btn')) {
        closeModelPicker();
      }
    });

    document.addEventListener('keydown', (event) => {
      if (event.key === 'Escape' && pickerDropdown.classList.contains('is-open')) {
        closeModelPicker();
      }
    });

    // Import Dialog
    const importDialog = $('#price-import-dialog');
    const importTextarea = $('#price-import-textarea');
    const importStatus = $('#price-import-status');
    const importFileInput = $('#price-file-input');
    const importFileLabel = $('#price-file-label');

    $('#import-prices-btn').addEventListener('click', () => {
      importTextarea.value = '';
      importStatus.hidden = true;
      importFileLabel.textContent = '未选择文件';
      importDialog.showModal();
    });
    $('#price-import-close').addEventListener('click', () => importDialog.close());
    $('#price-import-cancel').addEventListener('click', () => importDialog.close());
    $('#price-upload-btn').addEventListener('click', () => importFileInput.click());
    importFileInput.addEventListener('change', async (e) => {
      const file = e.target.files?.[0];
      if (!file) return;
      importFileLabel.textContent = file.name;
      const text = await file.text();
      importTextarea.value = text;
      e.target.value = '';
    });
    $('#price-import-submit').addEventListener('click', () => {
      const text = importTextarea.value.trim();
      if (!text) {
        importStatus.textContent = '请先粘贴价格数据或选择文件';
        importStatus.className = 'import-status-text is-error';
        importStatus.hidden = false;
        return;
      }
      const parsed = parseImportedPrices(text);
      if (!parsed.length) {
        importStatus.textContent = '未能解析出任何有效的模型价格，请检查数据格式';
        importStatus.className = 'import-status-text is-error';
        importStatus.hidden = false;
        return;
      }

      const mode = $('input[name="price-import-mode"]:checked')?.value || 'merge';
      if (mode === 'replace') {
        renderPrices(parsed);
      } else {
        const existing = getTablePrices();
        const map = new Map(existing.map((item) => [item.model.toLowerCase(), item]));
        parsed.forEach((item) => map.set(item.model.toLowerCase(), item));
        renderPrices([...map.values()]);
      }
      importDialog.close();
      toast(`成功导入 ${parsed.length} 个模型价格（请点击保存生效）`);
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

  function makeSparkline(trend, key, color, id) {
    const raw = (trend || []).map((point) => Number(point[key] || 0));
    const values = raw.length ? raw : [0, 0, 0, 0, 0, 0, 0, 0];
    const width = 240, height = 36;
    const min = Math.min(...values), max = Math.max(...values);
    const range = max - min || 1;
    const points = values.map((value, index) => {
      const x = values.length === 1 ? width / 2 : (index / (values.length - 1)) * width;
      const y = height - 4 - ((value - min) / range) * (height - 10);
      return { x, y };
    });
    const path = buildClampedSmoothPath(points, 3, height - 3);
    const gradient = `spark-gradient-${id}`;
    return `<svg class="sparkline-svg" viewBox="0 0 ${width} ${height}" preserveAspectRatio="none" aria-hidden="true"><defs><linearGradient id="${gradient}" x1="0" y1="0" x2="0" y2="1"><stop offset="0%" stop-color="${color}" stop-opacity=".26"/><stop offset="100%" stop-color="${color}" stop-opacity="0.02"/></linearGradient></defs><path d="${path} L ${width} ${height} L 0 ${height} Z" fill="url(#${gradient})" stroke="none"/><path d="${path}" fill="none" stroke="${color}" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/></svg>`;
  }

  function animateNumber(el, target, formatter, duration = 450) {
    if (!el) return;
    const end = Number(target || 0);
    const prev = el.dataset.prevVal !== undefined ? Number(el.dataset.prevVal) : 0;
    el.dataset.prevVal = String(end);
    if (isNaN(end) || Math.abs(end - prev) < 0.000001) {
      el.textContent = formatter(end);
      return;
    }
    const startTime = performance.now();
    function tick(now) {
      const p = Math.min(1, (now - startTime) / duration);
      const ease = 1 - Math.pow(1 - p, 3);
      const current = prev + (end - prev) * ease;
      el.textContent = formatter(current);
      if (p < 1) requestAnimationFrame(tick);
      else el.textContent = formatter(end);
    }
    requestAnimationFrame(tick);
  }

  function renderKPIs(kpi, trend) {
    const rangeLabels = { '24h': '24 小时', '7d': '7 天', '30d': '30 天', all: '全部' };
    const rangeLabel = rangeLabels[kpi.range_label] || kpi.range_label || rangeLabels[state.range];
    $('#overview-kpis').innerHTML = `<div class="kpi-row-top">
      <article class="kpi-panel theme-daily"><div class="kpi-header"><h3 class="kpi-title">日均用量</h3><span class="kpi-badge-pill">统计范围 ${esc(rangeLabel)}</span></div><div class="kpi-daily-list">
        <div class="kpi-daily-item" style="--metric-accent: var(--blue);">
          <span class="kpi-di-icon">${icon('activity')}</span>
          <span class="kpi-di-copy"><span class="kpi-di-label">日均请求</span></span>
          <strong class="kpi-di-val kpi-num-animate" id="kpi-val-daily-req">${formatCompact(kpi.avg_requests_daily)}</strong>
        </div>
        <div class="kpi-daily-item" style="--metric-accent: var(--purple);">
          <span class="kpi-di-icon">${icon('diamond')}</span>
          <span class="kpi-di-copy"><span class="kpi-di-label">日均 Token</span></span>
          <strong class="kpi-di-val kpi-num-animate" id="kpi-val-daily-tok">${formatCompact(kpi.avg_tokens_daily)}</strong>
        </div>
        <div class="kpi-daily-item" style="--metric-accent: var(--yellow);">
          <span class="kpi-di-icon">${icon('dollar')}</span>
          <span class="kpi-di-copy"><span class="kpi-di-label">日均费用</span></span>
          <strong class="kpi-di-val kpi-num-animate" id="kpi-val-daily-cost">${formatMoney(kpi.avg_cost_daily)}</strong>
        </div>
      </div></article>
      <article class="kpi-panel theme-blue"><div class="kpi-header"><h3 class="kpi-title">总请求数</h3><div class="kpi-icon-badge theme-blue">${icon('activity')}</div></div><strong class="kpi-main-val kpi-num-animate" id="kpi-val-req">${formatInt(kpi.requests)}</strong><div class="kpi-sub-info"><span class="dot-success">成功: ${formatInt(kpi.successes)}</span><span class="dot-failed">失败: ${formatInt(kpi.failures)}</span><span class="plain-item">成功率: ${formatPercent(kpi.success_rate)}</span></div><div class="sparkline-box" style="--card-theme:var(--blue)">${makeSparkline(trend, 'requests', '#326ff5', 'requests')}</div></article>
      <article class="kpi-panel theme-purple"><div class="kpi-header"><h3 class="kpi-title">总 Token 消耗</h3><div class="kpi-icon-badge theme-purple">${icon('diamond')}</div></div><strong class="kpi-main-val kpi-num-animate" id="kpi-val-tok">${formatCompact(kpi.total_tokens)}</strong><div class="kpi-sub-info"><span class="plain-item">缓存读取: ${formatCompact(kpi.cache_read_tokens)}</span><span class="plain-item">缓存写入: ${formatCompact(kpi.cache_write_tokens)}</span><span class="plain-item">推理: ${formatCompact(kpi.reasoning_tokens)}</span></div><div class="sparkline-box" style="--card-theme:var(--purple)">${makeSparkline(trend, 'tokens', '#a98bff', 'tokens')}</div></article>
    </div><div class="kpi-row-bottom">
      <article class="kpi-panel theme-green"><div class="kpi-header"><h3 class="kpi-title">RPM</h3><div class="kpi-icon-badge theme-green">${icon('clock')}</div></div><strong class="kpi-main-val kpi-num-animate" id="kpi-val-rpm">${formatNumber(kpi.rpm, 2)}</strong><div class="kpi-sub-info"><span class="plain-item">总请求数: ${formatInt(kpi.requests)}</span></div><div class="sparkline-box" style="--card-theme:var(--green)">${makeSparkline(trend, 'requests', '#42d982', 'rpm')}</div></article>
      <article class="kpi-panel theme-orange"><div class="kpi-header"><h3 class="kpi-title">TPM</h3><div class="kpi-icon-badge theme-orange">${icon('trend-up')}</div></div><strong class="kpi-main-val kpi-num-animate" id="kpi-val-tpm">${formatCompact(kpi.tpm)}</strong><div class="kpi-sub-info"><span class="plain-item">总 Token: ${formatCompact(kpi.total_tokens)}</span></div><div class="sparkline-box" style="--card-theme:var(--orange)">${makeSparkline(trend, 'tokens', '#ff7a12', 'tpm')}</div></article>
      <article class="kpi-panel theme-teal"><div class="kpi-header"><h3 class="kpi-title">缓存命中率</h3><div class="kpi-icon-badge theme-teal">${icon('percent')}</div></div><strong class="kpi-main-val kpi-num-animate" id="kpi-val-cache">${formatPercent(kpi.cache_rate)}</strong><div class="kpi-sub-info"><span class="plain-item">缓存读取: ${formatCompact(kpi.cache_read_tokens)}</span><span class="plain-item">输入: ${formatCompact(kpi.input_tokens)}</span></div><div class="sparkline-box" style="--card-theme:var(--teal)">${makeSparkline(trend, 'hit_rate', '#18ad9d', 'cache')}</div></article>
      <article class="kpi-panel theme-yellow"><div class="kpi-header"><h3 class="kpi-title">总费用</h3><div class="kpi-icon-badge theme-yellow">${icon('dollar')}</div></div><strong class="kpi-main-val kpi-num-animate" id="kpi-val-cost">${formatMoney(kpi.cost_usd)}</strong><div class="kpi-sub-info"><span class="plain-item">总 Token: ${formatCompact(kpi.total_tokens)}</span></div><div class="sparkline-box" style="--card-theme:var(--yellow)">${makeSparkline(trend, 'actual_cost', '#ffb35c', 'cost')}</div></article>
    </div>`;

    animateNumber($('#kpi-val-daily-req'), kpi.avg_requests_daily, formatCompact);
    animateNumber($('#kpi-val-daily-tok'), kpi.avg_tokens_daily, formatCompact);
    animateNumber($('#kpi-val-daily-cost'), kpi.avg_cost_daily, formatMoney);
    animateNumber($('#kpi-val-req'), kpi.requests, formatInt);
    animateNumber($('#kpi-val-tok'), kpi.total_tokens, formatCompact);
    animateNumber($('#kpi-val-rpm'), kpi.rpm, (v) => formatNumber(v, 2));
    animateNumber($('#kpi-val-tpm'), kpi.tpm, formatCompact);
    animateNumber($('#kpi-val-cache'), kpi.cache_rate, formatPercent);
    animateNumber($('#kpi-val-cost'), kpi.cost_usd, formatMoney);
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
    const normalizedPoints = (rawPoints || []).map((point) => ({
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

    state.lastTrendPoints = normalizedPoints;
    const hasAnyUsage = normalizedPoints.some((point) =>
      point.requests > 0 || point.tokens > 0 || point.input > 0 || point.output > 0 ||
      point.cache_write > 0 || point.cache_read > 0 || point.reasoning > 0 ||
      point.actual_cost > 0 || point.standard_cost > 0
    );
    if (!normalizedPoints.length || !hasAnyUsage) return empty(host, '当前范围没有用量');

    const points = normalizedPoints;

    const dimensions = [
      { key: 'input', label: '输入', color: '#2f6bf2', axis: 'token', strokeWidth: 3 },
      { key: 'output', label: '输出', color: '#16a34a', axis: 'token', strokeWidth: 3 },
      { key: 'cache_write', label: '缓存创建', color: '#ea580c', axis: 'token', strokeWidth: 3 },
      { key: 'cache_read', label: '缓存读取', color: '#00bcd4', axis: 'token', area: true, strokeWidth: 3.8 },
      { key: 'hit_rate', label: '缓存命中率', color: '#8b5cf6', axis: 'rate', dashed: true, strokeWidth: 3.2 },
    ];
    const active = state.trendActiveDims;
    const activeDimensions = dimensions.filter((dim) => active[dim.key]);

    if (!activeDimensions.length) {
      return empty(host, '已取消所有指标，点击上方图例恢复');
    }

    const width = Math.max(600, Math.round(host.clientWidth || 780));
    const height = 480;
    const left = 76, right = 58, top = 26, bottom = 44;
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
      const labelText = ratio === 0 ? '0' : (hasTokenActive ? formatTokenCompact(maxToken * ratio) : '');
      return `<line class="${ratio ? 'chart-grid-line' : 'chart-zero-line'}" x1="${left}" y1="${y}" x2="${width-right}" y2="${y}"/><text class="chart-left-label" x="${left-12}" y="${y+4}">${labelText}</text>`;
    }).join('');
    const rightGrid = [0, .2, .4, .6, .8, 1].map((ratio) => {
      const y = top + plotHeight * (1 - ratio);
      return `<text class="chart-right-label" x="${width-right+12}" y="${y+4}">${Math.round(ratio*100)}%</text>`;
    }).join('');
    const labelStep = Math.max(1, Math.floor(points.length / 7));
    const vGridLines = points.map((point, index) => (index % labelStep === 0 || index === points.length - 1) ? `<line class="chart-vgrid-line" x1="${x(index)}" y1="${top}" x2="${x(index)}" y2="${zeroY}"/>` : '').join('');
    const xLabels = points.map((point, index) => (index % labelStep === 0 || index === points.length - 1) ? `<text class="chart-x-label" x="${x(index)}" y="${zeroY+22}">${esc(formatTime(point.timestamp_ms, state.range))}</text>` : '').join('');

    let linePaths = '', areaPaths = '', pointDots = '';
    dimensions.forEach((dimension) => {
      if (!active[dimension.key]) return;

      if (dimension.axis === 'rate') {
        const validRatePoints = points
          .map((point, index) => ({
            x: x(index),
            y: rateY(point[dimension.key]),
            val: Number(point[dimension.key] || 0),
            hasData: (point.input > 0 || point.requests > 0),
          }))
          .filter((pt) => pt.hasData);

        if (validRatePoints.length > 1) {
          const path = buildClampedSmoothPath(validRatePoints, top, zeroY);
          linePaths += `<path d="${path}" fill="none" stroke="${dimension.color}" stroke-width="${dimension.strokeWidth || 3.2}" stroke-dasharray="6 5" class="animated-line is-dashed" stroke-linecap="round" stroke-linejoin="round"/>`;
        }

        if (validRatePoints.length <= 60) {
          validRatePoints.forEach((pt) => {
            pointDots += `<circle cx="${pt.x}" cy="${pt.y}" r="4.2" fill="var(--surface)" stroke="${dimension.color}" stroke-width="2.2" class="trend-point-dot"/>`;
          });
        }
      } else {
        const pathPoints = points.map((point, index) => ({
          x: x(index),
          y: tokenY(point[dimension.key]),
          val: Number(point[dimension.key] || 0),
        }));
        const path = buildClampedSmoothPath(pathPoints, top, zeroY);
        if (dimension.area && pathPoints.length > 1) {
          areaPaths += `<path d="${path} L ${pathPoints.at(-1).x} ${zeroY - 1} L ${pathPoints[0].x} ${zeroY - 1} Z" fill="url(#area-gradient-cache)" stroke="none" class="animated-area"/>`;
        }
        linePaths += `<path d="${path}" fill="none" stroke="${dimension.color}" stroke-width="${dimension.strokeWidth || 3}" class="animated-line" stroke-linecap="round" stroke-linejoin="round"/>`;
        if (points.length <= 60) {
          pathPoints.forEach((pt) => {
            if (pt.val > 0) {
              pointDots += `<circle cx="${pt.x}" cy="${pt.y}" r="4.2" fill="var(--surface)" stroke="${dimension.color}" stroke-width="2.2" class="trend-point-dot"/>`;
            }
          });
        }
      }
    });

    const defs = `<defs>
      <linearGradient id="area-gradient-cache" x1="0" y1="0" x2="0" y2="1">
        <stop offset="0%" stop-color="#00bcd4" stop-opacity=".32"/>
        <stop offset="65%" stop-color="#00bcd4" stop-opacity=".08"/>
        <stop offset="100%" stop-color="#00bcd4" stop-opacity="0"/>
      </linearGradient>
    </defs>`;

    host.innerHTML = `<svg class="trend-svg-large" viewBox="0 0 ${width} ${height}" id="trend-svg-element" role="img" aria-label="输入、输出、缓存和缓存命中率趋势">${defs}${vGridLines}${leftGrid}${rightGrid}<g>${areaPaths}${linePaths}${pointDots}</g>${xLabels}<line id="crosshair-line" class="chart-crosshair" x1="0" y1="${top}" x2="0" y2="${zeroY}" hidden/><g id="active-dots-group"></g></svg><div id="trend-tooltip" class="trend-tooltip-popup" hidden></div>`;
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
      dots.innerHTML = dimensions.filter((dimension) => active[dimension.key]).map((dimension) => `<circle cx="${pointX}" cy="${dimension.axis === 'rate' ? rateY(point[dimension.key]) : tokenY(point[dimension.key])}" r="6" fill="var(--surface)" stroke="${dimension.color}" stroke-width="3" class="active-hover-dot"/>`).join('');
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
    const totalSlots = HEALTH_DAYS * HEALTH_SLOTS_PER_DAY;
    const latestTimestamp = normalized.at(-1)?.timestamp_ms || 0;
    const endSlot = latestTimestamp
      ? Math.floor(latestTimestamp / slotMilliseconds) * slotMilliseconds
      : Math.floor(Date.now() / slotMilliseconds) * slotMilliseconds;
    const firstSlot = endSlot - (totalSlots - 1) * slotMilliseconds;
    const pointsBySlot = new Map(normalized.map((point) => [Math.floor(point.timestamp_ms / slotMilliseconds) * slotMilliseconds, point]));
    const visible = (latestTimestamp || normalized.length) ? Array.from({ length: totalSlots }, (_, index) => {
      const timestamp = firstSlot + index * slotMilliseconds;
      return pointsBySlot.get(timestamp) || { timestamp_ms: timestamp, requests: 0, failures: 0 };
    }) : [];
    const requests = visible.reduce((sum, point) => sum + Number(point.requests || 0), 0);
    const failures = visible.reduce((sum, point) => sum + Number(point.failures || 0), 0);
    const successes = Math.max(0, requests - failures);
    const rateEl = $('#health-summary');
    const rate = requests ? successes / requests : null;
    rateEl.textContent = rate !== null ? formatPercent(rate) : '--';
    if (rate !== null) {
      if (rate >= 0.98) rateEl.style.color = 'var(--green)';
      else if (rate >= 0.90) rateEl.style.color = 'var(--yellow)';
      else rateEl.style.color = 'var(--red)';
    } else {
      rateEl.style.color = 'var(--muted)';
    }
    $('#health-success-count').textContent = formatInt(successes);
    $('#health-failure-count').textContent = formatInt(failures);
    if (!visible.length) return empty(host, '暂无健康数据');

    const SLOTS_PER_ROW = 24;
    const ROWS_PER_DAY = 4;
    const TOTAL_ROWS = HEALTH_DAYS * ROWS_PER_DAY;
    let rows = $$('.health-row', host);
    let cells = $$('.health-cell', host);
    if (rows.length !== TOTAL_ROWS || cells.length !== HEALTH_DAYS * HEALTH_SLOTS_PER_DAY) {
      const fragment = document.createDocumentFragment();
      for (let dayIndex = 0; dayIndex < HEALTH_DAYS; dayIndex += 1) {
        const dayGroup = document.createElement('div');
        dayGroup.className = 'health-day-group';
        for (let r = 0; r < ROWS_PER_DAY; r += 1) {
          const row = document.createElement('div');
          row.className = 'health-row';
          for (let slot = 0; slot < SLOTS_PER_ROW; slot += 1) {
            const cell = document.createElement('span');
            cell.className = 'health-cell level-0';
            row.appendChild(cell);
          }
          dayGroup.appendChild(row);
        }
        fragment.appendChild(dayGroup);
      }
      host.replaceChildren(fragment);
      rows = $$('.health-row', host);
      cells = $$('.health-cell', host);
    }

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
    renderTokenComposition(data.tokens || {}, data.models || []);
    renderModelTable(data.models || []);
  }

  function renderDistributions(distributions) {
    const definitions = [
      ['models', '模型分布'], ['providers', '上游分布'], ['api_keys', '客户端 Key 分组'], ['sources', '渠道来源分布'],
    ];
    $('#distribution-grid').innerHTML = definitions.map(([key, title]) => distributionCard(title, distributions[key] || [], key)).join('');
  }

  function distributionCard(title, values, key) {
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
    return `<article class="distribution-card theme-${key || 'models'}"><h2>${title}</h2><div class="donut-layout"><div class="donut-wrapper"><svg class="donut" viewBox="0 0 104 104" aria-hidden="true"><circle class="donut-track" cx="52" cy="52" r="38"/>${segments}</svg><div class="donut-center-text"><span class="dct-name">占比率</span><span class="dct-val">TOP 5</span></div></div><div class="distribution-list">${list}</div></div></article>`;
  }

  function renderTokenComposition(tokens, models = []) {
    const regularInput = Math.max(0, (tokens.input || 0) - (tokens.cache_read || 0) - (tokens.cache_write || 0));
    const regularOutput = Math.max(0, (tokens.output || 0) - (tokens.reasoning || 0));
    const cacheRead = tokens.cache_read || 0;
    const cacheWrite = tokens.cache_write || 0;
    const reasoning = tokens.reasoning || 0;
    const totalTokens = tokens.total || (regularInput + regularOutput + cacheRead + cacheWrite + reasoning) || 0;
    const totalCost = (models || []).reduce((sum, m) => sum + Number(m.cost_usd || 0), 0) || state.summary?.kpi?.cost_usd || 0;

    let savedCost = Number(state.summary?.kpi?.saved_cost_usd || 0);
    if (!savedCost && (models || []).length) {
      savedCost = (models || []).reduce((sum, m) => sum + Number(m.saved_cost_usd || 0), 0);
    }
    if (!savedCost && state.summary?.trend?.length) {
      savedCost = state.summary.trend.reduce((sum, p) => sum + Math.max(0, Number(p.standard_cost || 0) - Number(p.actual_cost || 0)), 0);
    }

    const standardCost = totalCost + savedCost;
    const savingPct = standardCost > 0 ? ((savedCost / standardCost) * 100).toFixed(1) : '0.0';

    const sumTokens = (regularInput + regularOutput + cacheRead + cacheWrite + reasoning) || totalTokens || 1;
    const costPerMillion = totalTokens > 0 ? (totalCost / totalTokens * 1000000) : 0;

    const parts = [
      { key: 'input', label: '输入', tokens: regularInput, color: '#326ff5' },
      { key: 'cache_read', label: '缓存读取', tokens: cacheRead, color: '#ff7a12' },
      { key: 'cache_write', label: '缓存创建', tokens: cacheWrite, color: '#e44e3f' },
      { key: 'output', label: '输出', tokens: regularOutput, color: '#20b95a' },
    ];
    if (reasoning > 0) {
      parts.push({ key: 'reasoning', label: '推理 / 思考', tokens: reasoning, color: '#a98bff' });
    }

    parts.forEach((p, idx) => {
      p.idx = String(idx);
      p.pctNum = (p.tokens / sumTokens * 100);
      p.pct = p.pctNum.toFixed(2) + '%';
      p.cost = totalCost * (p.tokens / sumTokens);
    });

    const savingsBanner = `
      <div class="cache-saving-hero" title="基于模型输入单价与缓存读取单价差额计算得出的 Prompt 缓存直接经济节省">
        <div class="csh-left">
          <span class="csh-icon">💰</span>
          <div class="csh-text">
            <span class="csh-title">Prompt 缓存已节省成本</span>
            <span class="csh-desc">相比未经缓存的全量输入标准计费</span>
          </div>
        </div>
        <div class="csh-right">
          <strong class="csh-val">${formatMoney(savedCost)}</strong>
          <span class="csh-badge">已降低 ${savingPct}% 成本</span>
        </div>
      </div>`;

    const stackSegments = parts.map((part) => {
      if (part.pctNum <= 0) return '';
      const showText = part.pctNum >= 5;
      return `<div class="token-stack-segment" data-idx="${part.idx}" data-name="${part.label}" data-val="${formatInt(part.tokens)}" data-cost="${formatMoney(part.cost)}" data-compact="${formatCompact(part.tokens)}" data-pct="${part.pct}" data-color="${part.color}" tabindex="0" role="button" aria-label="${part.label} ${part.pct}" style="width:${part.pctNum.toFixed(3)}%;background:${part.color};">
        ${showText ? `<span class="tss-pct-text">${part.pct}</span>` : ''}
      </div>`;
    }).join('');

    const summaryStrip = `<div class="token-summary-strip">
      <div class="tss-col">
        <span class="tss-label">总 TOKEN</span>
        <strong class="tss-val" id="token-total">${formatCompact(totalTokens)}</strong>
      </div>
      <div class="tss-col">
        <span class="tss-label">实际总费用</span>
        <strong class="tss-val">${formatMoney(totalCost)}</strong>
      </div>
      <div class="tss-col">
        <span class="tss-label">缓存已节省</span>
        <strong class="tss-val tss-val-saved">${formatMoney(savedCost)}</strong>
        <span class="tss-badge tss-badge-saved">省 ${savingPct}%</span>
      </div>
      <div class="tss-col">
        <span class="tss-label">每百万 TOKEN 费率</span>
        <strong class="tss-val">$${costPerMillion.toFixed(4)}</strong>
        <span class="tss-badge">综合费率</span>
      </div>
    </div>`;

    const breakdownCards = `<div class="token-breakdown-grid" style="grid-template-columns: repeat(${parts.length}, minmax(0, 1fr));">
      ${parts.map((part) => `
        <div class="token-breakdown-card" data-idx="${part.idx}" data-name="${part.label}" data-val="${formatInt(part.tokens)}" data-cost="${formatMoney(part.cost)}" data-compact="${formatCompact(part.tokens)}" data-pct="${part.pct}" data-color="${part.color}" tabindex="0" role="button" aria-label="${part.label} ${formatMoney(part.cost)}">
          <div class="tbc-head">
            <i class="tbc-dot" style="background:${part.color}"></i>
            <span class="tbc-title">${part.label}</span>
          </div>
          <strong class="tbc-cost">${formatMoney(part.cost)}</strong>
          <span class="tbc-pct">${part.pct}</span>
        </div>
      `).join('')}
    </div>`;

    $('#token-composition').innerHTML = `
      ${savingsBanner}
      <div class="token-stack">${stackSegments}</div>
      ${summaryStrip}
      ${breakdownCards}
    `;
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

    let activeItem = null;

    const activate = (item, event) => {
      if (activeItem === item) {
        if (event?.clientX != null) positionFloatingTooltip(tooltip, event);
        return;
      }
      activeItem = item;
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
      activeItem = null;
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
      if (activeItem && tooltip.classList.contains('is-visible')) {
        positionFloatingTooltip(tooltip, event);
      }
    });

    grid.addEventListener('mouseout', (event) => {
      const item = event.target.closest('.donut-segment, .distribution-row');
      const card = event.target.closest('.distribution-card');
      if (item && !item.contains(event.relatedTarget)) {
        const nextItem = event.relatedTarget?.closest('.donut-segment, .distribution-row');
        if (!nextItem) {
          activeItem = null;
          tooltip.classList.remove('is-visible');
        }
      }
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
        <div class="fgt-body">
          <div class="fgt-row"><span>输入 Token</span><strong>${esc(tokenIn)}</strong></div>
          <div class="fgt-row"><span>输出 Token</span><strong>${esc(tokenOut)}</strong></div>
          ${cacheRead && cacheRead !== '0' ? `<div class="fgt-row"><span>缓存读取 Token</span><strong>${esc(cacheRead)}</strong></div>` : ''}
          ${cacheWrite && cacheWrite !== '0' ? `<div class="fgt-row"><span>缓存创建 Token</span><strong>${esc(cacheWrite)}</strong></div>` : ''}
          ${reasoning && reasoning !== '0' ? `<div class="fgt-row"><span>思考/推理 Token</span><strong>${esc(reasoning)}</strong></div>` : ''}
        </div>
        <div class="fgt-footer"><span>总 Token</span><strong class="fgt-total-val">${esc(total)}</strong></div>`;
    }

    function populateCostTooltip(trigger) {
      const {
        totalCost, inCost, outCost, inPrice, outPrice,
        cacheReadCost, cacheWriteCost, reasoningCost
      } = trigger.dataset;
      tooltip.innerHTML = `<div class="fgt-title">费用明细</div>
        <div class="fgt-body">
          <div class="fgt-row"><span>输入费用</span><strong>${esc(inCost || '$0.000000')}</strong></div>
          <div class="fgt-row"><span>输出费用</span><strong>${esc(outCost || '$0.000000')}</strong></div>
          <div class="fgt-row"><span>输入单价</span><strong class="cost-unit-price-in">${esc(inPrice || '$0.0000')} / 1M Token</strong></div>
          <div class="fgt-row"><span>输出单价</span><strong class="cost-unit-price-out">${esc(outPrice || '$0.0000')} / 1M Token</strong></div>
          ${cacheReadCost && cacheReadCost !== '$0.000000' && cacheReadCost !== '$0.00' ? `<div class="fgt-row"><span>缓存读取费用</span><strong>${esc(cacheReadCost)}</strong></div>` : ''}
          ${cacheWriteCost && cacheWriteCost !== '$0.000000' && cacheWriteCost !== '$0.00' ? `<div class="fgt-row"><span>缓存创建费用</span><strong>${esc(cacheWriteCost)}</strong></div>` : ''}
          ${reasoningCost && reasoningCost !== '$0.000000' && reasoningCost !== '$0.00' ? `<div class="fgt-row"><span>思考/推理费用</span><strong>${esc(reasoningCost)}</strong></div>` : ''}
        </div>
        <div class="fgt-footer"><span>总费用</span><strong class="fgt-total-cost">${esc(totalCost || '$0.000000')}</strong></div>`;
    }

    function renderTooltipForTrigger(trigger) {
      if (trigger.dataset.tooltipType === 'cost-detail') {
        populateCostTooltip(trigger);
      } else {
        populateTokenTooltip(trigger);
      }
    }

    let activeTokenItem = null;
    if (container) {
      container.addEventListener('mouseover', (event) => {
        const item = event.target.closest('.token-stack-segment, .token-breakdown-card');
        if (!item || pinnedTrigger) return;
        if (activeTokenItem === item) return;
        activeTokenItem = item;
        const { idx, name, val, compact, pct, color, cost } = item.dataset;
        container.classList.add('has-hover');
        container.querySelectorAll('.token-stack-segment, .token-breakdown-card').forEach((el) => {
          el.classList.toggle('is-active', el.dataset.idx === idx);
        });
        tooltip.innerHTML = `<div class="fgt-title" style="color:${color}">${esc(name)}</div>
          <div class="fgt-row"><span>占比率</span><strong>${esc(pct)}</strong></div>
          <div class="fgt-row"><span>折算费用</span><strong>${esc(cost || '$0.00')}</strong></div>
          <div class="fgt-row"><span>Token 消耗</span><strong>${esc(val)} (${esc(compact)})</strong></div>`;
        tooltip.classList.add('is-visible');
      });
      container.addEventListener('mousemove', (event) => {
        if (!pinnedTrigger && activeTokenItem) positionFloatingTooltip(tooltip, event);
      });
      container.addEventListener('mouseout', (event) => {
        if (pinnedTrigger || container.contains(event.relatedTarget)) return;
        activeTokenItem = null;
        container.classList.remove('has-hover');
        container.querySelectorAll('.token-stack-segment, .token-breakdown-card').forEach((el) => el.classList.remove('is-active'));
        tooltip.classList.remove('is-visible');
      });
    }

    document.addEventListener('mouseover', (event) => {
      if (pinnedTrigger) return;
      const trigger = event.target.closest('[data-tooltip-type="token-detail"], [data-tooltip-type="cost-detail"]');
      if (!trigger) return;
      renderTooltipForTrigger(trigger);
      tooltip.classList.add('is-visible');
    });

    document.addEventListener('mousemove', (event) => {
      if (pinnedTrigger || !tooltip.classList.contains('is-visible')) return;
      const trigger = event.target.closest('[data-tooltip-type="token-detail"], [data-tooltip-type="cost-detail"]');
      if (trigger) positionFloatingTooltip(tooltip, event);
    });

    document.addEventListener('mouseout', (event) => {
      if (pinnedTrigger) return;
      const trigger = event.target.closest('[data-tooltip-type="token-detail"], [data-tooltip-type="cost-detail"]');
      if (trigger && !trigger.contains(event.relatedTarget)) {
        tooltip.classList.remove('is-visible');
      }
    });

    document.addEventListener('click', (event) => {
      const btn = event.target.closest('.token-info-trigger, .cost-info-trigger');
      const trigger = event.target.closest('[data-tooltip-type="token-detail"], [data-tooltip-type="cost-detail"]');
      if (btn || trigger) {
        const targetCell = btn ? btn.closest('[data-tooltip-type="token-detail"], [data-tooltip-type="cost-detail"]') : trigger;
        if (pinnedTrigger === targetCell) {
          pinnedTrigger = null;
          $$('.token-info-trigger.is-pinned, .cost-info-trigger.is-pinned').forEach((el) => el.classList.remove('is-pinned'));
          tooltip.classList.remove('is-visible');
        } else {
          pinnedTrigger = targetCell;
          $$('.token-info-trigger.is-pinned, .cost-info-trigger.is-pinned').forEach((el) => el.classList.remove('is-pinned'));
          const btnEl = targetCell?.querySelector('.token-info-trigger, .cost-info-trigger');
          if (btnEl) btnEl.classList.add('is-pinned');
          renderTooltipForTrigger(targetCell);
          positionFloatingTooltip(tooltip, event);
          tooltip.classList.add('is-visible');
        }
      } else if (pinnedTrigger && !tooltip.contains(event.target)) {
        pinnedTrigger = null;
        $$('.token-info-trigger.is-pinned, .cost-info-trigger.is-pinned').forEach((el) => el.classList.remove('is-pinned'));
        tooltip.classList.remove('is-visible');
      }
    });
  }

  function formatUnitPrice(val) {
    const num = Number(val || 0);
    return `$${num.toFixed(4)}`;
  }

  function formatCostDetail(val) {
    const num = Number(val || 0);
    return `$${num.toFixed(6)}`;
  }

  function renderCostCell(event) {
    const costUSD = Number(event.cost_usd || 0);
    const inCost = Number(event.input_cost || 0);
    const outCost = Number(event.output_cost || 0);
    const inPrice = Number(event.input_price || 0);
    const outPrice = Number(event.output_price || 0);
    const cacheReadCost = Number(event.cache_read_cost || 0);
    const cacheWriteCost = Number(event.cache_write_cost || 0);
    const reasoningCost = Number(event.reasoning_cost || 0);
    const formattedTotal = formatCostDetail(costUSD);

    return `<div class="cost-compound-cell" data-tooltip-type="cost-detail"
      data-total-cost="${formattedTotal}"
      data-in-cost="${formatCostDetail(inCost)}"
      data-out-cost="${formatCostDetail(outCost)}"
      data-in-price="${formatUnitPrice(inPrice)}"
      data-out-price="${formatUnitPrice(outPrice)}"
      data-cache-read-cost="${formatCostDetail(cacheReadCost)}"
      data-cache-write-cost="${formatCostDetail(cacheWriteCost)}"
      data-reasoning-cost="${formatCostDetail(reasoningCost)}">
      <span class="cost-val-text">${formattedTotal}</span>
      <button type="button" class="cost-info-trigger" title="点击展开/固定费用明细" aria-label="查看费用明细">
        <svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="10"/><path d="M12 16v-4M12 8h.01"/></svg>
      </button>
    </div>`;
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
    state.lastInterfacesData = data;
    renderInterfaceSummary(data);
    renderProviderTokenLegend();
    renderProviderTokenChart(data.upstreams || []);
    renderUpstreamLatencyBoard(data.upstreams || []);
    renderAPIKeys(data.api_keys || []);
    renderUpstreams(data.upstreams || []);
  }

  function renderInterfaceSummary(data) {
    const apiKeys = data.api_keys || [], upstreams = data.upstreams || [];
    const requests = upstreams.reduce((sum, item) => sum + (item.requests || 0), 0);
    const weightedLatency = requests ? upstreams.reduce((sum, item) => sum + Number(item.avg_latency_ms || 0) * Number(item.requests || 0), 0) / requests : 0;
    const healthy = upstreams.filter((item) => Number(item.success_rate || 0) >= .9).length;
    const unhealthy = upstreams.length - healthy;
    const totalTokens = upstreams.reduce((sum, item) => sum + Number(item.total_tokens || 0), 0);
    const totalCost = upstreams.reduce((sum, item) => sum + Number(item.cost_usd || 0), 0);
    const overallSuccessRate = requests ? upstreams.reduce((sum, item) => sum + Number(item.successes || 0), 0) / requests : 1;

    $('#interface-summary').innerHTML = `
      <article class="kpi-panel interface-card theme-blue">
        <div class="kpi-header"><h3 class="kpi-title">客户端 Key 凭证</h3><div class="kpi-icon-badge theme-blue">${icon('key')}</div></div>
        <strong class="kpi-main-val">${formatInt(apiKeys.length)} <small style="font-size:12px;font-weight:600;color:var(--muted)">个活跃 Key</small></strong>
        <div class="kpi-sub-info"><span class="dot-success">全部分组正常</span><span class="plain-item">独立鉴权</span></div>
      </article>
      <article class="kpi-panel interface-card theme-purple">
        <div class="kpi-header"><h3 class="kpi-title">上游通道集群</h3><div class="kpi-icon-badge theme-purple">${icon('database')}</div></div>
        <strong class="kpi-main-val">${formatInt(upstreams.length)} <small style="font-size:12px;font-weight:600;color:var(--muted)">个 Provider</small></strong>
        <div class="kpi-sub-info"><span class="dot-success">在线: ${formatInt(healthy)}</span><span class="${unhealthy > 0 ? 'dot-failed' : 'plain-item'}">${unhealthy > 0 ? `异常: ${formatInt(unhealthy)}` : 'SLA 稳定'}</span></div>
      </article>
      <article class="kpi-panel interface-card theme-green">
        <div class="kpi-header"><h3 class="kpi-title">接口总请求量</h3><div class="kpi-icon-badge theme-green">${icon('activity')}</div></div>
        <strong class="kpi-main-val">${formatCompact(requests)} <small style="font-size:12px;font-weight:600;color:var(--muted)">次</small></strong>
        <div class="kpi-sub-info"><span class="plain-item">Token: ${formatCompact(totalTokens)}</span><span class="plain-item">成功率: ${formatPercent(overallSuccessRate)}</span></div>
      </article>
      <article class="kpi-panel interface-card theme-orange">
        <div class="kpi-header"><h3 class="kpi-title">上游平均响应延迟</h3><div class="kpi-icon-badge theme-orange">${icon('clock')}</div></div>
        <strong class="kpi-main-val">${formatDuration(weightedLatency)}</strong>
        <div class="kpi-sub-info"><span class="dot-success">${formatInt(healthy)} 正常通道</span><span class="plain-item">费用: ${formatMoney(totalCost)}</span></div>
      </article>`;
  }

  function renderProviderTokenLegend() {
    const host = $('#provider-token-legend');
    if (!host) return;
    const dims = [
      { key: 'input', label: '输入', color: '#326ff5' },
      { key: 'output', label: '输出', color: '#20b95a' },
      { key: 'cache_read', label: '缓存读取', color: '#ff7a12' },
      { key: 'cache_write', label: '缓存创建', color: '#e44e3f' },
      { key: 'reasoning', label: '推理 / 思考', color: '#a98bff' },
    ];
    const active = state.providerTokenActiveDims;
    host.innerHTML = dims.map((d) => `
      <button type="button" class="legend-chip ${active[d.key] ? 'is-active' : ''}" data-token-dim="${d.key}" aria-pressed="${Boolean(active[d.key])}">
        <i class="chip-circle" style="background:${d.color}"></i>
        <span>${d.label}</span>
      </button>
    `).join('');
  }

  function renderProviderTokenChart(upstreams = []) {
    const host = $('#provider-token-chart');
    if (!host) return;
    if (!upstreams.length) return empty(host, '暂无 Provider Token 消耗数据');

    const active = state.providerTokenActiveDims;
    const anyActive = Object.values(active).some(Boolean);
    if (!anyActive) {
      return empty(host, '已取消所有 Token 维度，点击上方图例恢复');
    }

    const items = upstreams.map((item) => {
      const regInput = Math.max(0, (item.tokens?.input || 0) - (item.tokens?.cache_read || 0) - (item.tokens?.cache_write || 0));
      const regOutput = Math.max(0, (item.tokens?.output || 0) - (item.tokens?.reasoning || 0));
      const cacheRead = item.tokens?.cache_read || 0;
      const cacheWrite = item.tokens?.cache_write || 0;
      const reasoning = item.tokens?.reasoning || 0;

      const segs = [
        { key: 'input', label: '输入', val: regInput, color: '#326ff5' },
        { key: 'output', label: '输出', val: regOutput, color: '#20b95a' },
        { key: 'cache_read', label: '缓存读取', val: cacheRead, color: '#ff7a12' },
        { key: 'cache_write', label: '缓存创建', val: cacheWrite, color: '#e44e3f' },
        { key: 'reasoning', label: '推理 / 思考', val: reasoning, color: '#a98bff' },
      ].filter((s) => active[s.key] && s.val > 0);

      const activeTotal = segs.reduce((sum, s) => sum + s.val, 0);
      return {
        ...item,
        segs,
        activeTotal,
      };
    }).filter((item) => item.activeTotal > 0).sort((a, b) => b.activeTotal - a.activeTotal);

    if (!items.length) {
      return empty(host, '当前选中的 Token 维度在各 Provider 均无消耗');
    }

    const maxTokens = Math.max(1, ...items.map((i) => i.activeTotal));

    host.innerHTML = `<div class="pt-bars-list">${items.map((item, index) => {
      const relativeWidth = Math.min(100, Math.max(12, item.activeTotal / maxTokens * 100));
      const segElements = item.segs.map((seg) => {
        const pctNum = item.activeTotal > 0 ? (seg.val / item.activeTotal * 100) : 0;
        const pctStr = pctNum.toFixed(1) + '%';
        const showLabel = pctNum >= 12;
        return `<div class="pt-bar-segment" data-provider="${esc(item.name)}" data-dim="${seg.label}" data-val="${formatInt(seg.val)}" data-compact="${formatCompact(seg.val)}" data-pct="${pctStr}" data-color="${seg.color}" style="width:${pctNum.toFixed(2)}%;background:${seg.color};" tabindex="0" role="button" aria-label="${seg.label}: ${formatInt(seg.val)} (${pctStr})">
          ${showLabel ? `<span class="pt-seg-text">${pctStr}</span>` : ''}
        </div>`;
      }).join('');

      return `
        <div class="pt-row" data-idx="${index}">
          <div class="pt-row-head">
            <div class="pt-name-wrap">
              <span class="pt-name" title="${esc(item.name)}">${esc(item.name || '未识别')}</span>
              <span class="pt-models-badge">${formatInt(item.models || 1)} 模型</span>
            </div>
            <div class="pt-total-wrap">
              <strong class="pt-total-val">${formatCompact(item.activeTotal)} <small>Token</small></strong>
              <span class="pt-cost-val">${formatMoney(item.cost_usd)}</span>
            </div>
          </div>
          <div class="pt-track-wrapper" style="width:${relativeWidth.toFixed(1)}%;">
            <div class="pt-track">${segElements || '<div class="pt-empty-bar"></div>'}</div>
          </div>
        </div>
      `;
    }).join('')}</div>`;
  }

  function renderUpstreamLatencyBoard(upstreams = []) {
    const host = $('#upstream-latency-board');
    if (!host) return;
    const validUpstreams = upstreams.filter((item) => Number(item.success_rate || 0) > 0);
    if (!validUpstreams.length) return empty(host, '暂无有效上游延迟与 SLA 数据');

    const ranked = validUpstreams.slice().sort((a, b) => (a.avg_latency_ms || 0) - (b.avg_latency_ms || 0));
    const maxLatency = Math.max(1000, ...ranked.map((u) => u.avg_latency_ms || 0));

    host.innerHTML = `<div class="ul-list">${ranked.map((item, idx) => {
      const latency = Number(item.avg_latency_ms || 0);
      const rate = Number(item.success_rate || 0);
      let tierCls = 'is-fast', tierText = '极速响应';
      if (latency >= 10000) { tierCls = 'is-slow'; tierText = '重载慢速'; }
      else if (latency >= 2000) { tierCls = 'is-mid'; tierText = '标准响应'; }
      const widthPct = Math.min(100, Math.max(8, latency / maxLatency * 100));

      return `
        <div class="ul-item">
          <div class="ul-head">
            <div class="ul-name-box">
              <span class="ul-rank">#${idx + 1}</span>
              <span class="ul-name" title="${esc(item.name)}">${esc(item.name || '未识别')}</span>
            </div>
            <div class="ul-stat-box">
              <span class="ul-tier ${tierCls}">${tierText}</span>
              <strong class="ul-lat-val">${formatDuration(latency)}</strong>
            </div>
          </div>
          <div class="ul-bar-bg">
            <div class="ul-bar-fill ${tierCls}" style="width:${widthPct.toFixed(1)}%;"></div>
          </div>
          <div class="ul-foot">
            <span>请求量: <strong>${formatInt(item.requests)} 次</strong></span>
            <span class="ul-succ ${rate < 0.9 ? 'is-warn' : ''}">成功率: <strong>${formatPercent(rate)}</strong></span>
          </div>
        </div>
      `;
    }).join('')}</div>`;
  }

  function bindProviderTokenInteractivity() {
    const legend = $('#provider-token-legend');
    const chart = $('#provider-token-chart');
    const tooltip = $('#floating-tooltip');

    if (legend) {
      legend.addEventListener('click', (event) => {
        const chip = event.target.closest('.legend-chip');
        if (!chip) return;
        const dim = chip.dataset.tokenDim;
        state.providerTokenActiveDims[dim] = !state.providerTokenActiveDims[dim];
        chip.classList.toggle('is-active', state.providerTokenActiveDims[dim]);
        chip.setAttribute('aria-pressed', String(state.providerTokenActiveDims[dim]));
        if (state.lastInterfacesData) {
          renderProviderTokenChart(state.lastInterfacesData.upstreams || []);
        }
      });
    }

    if (chart) {
      chart.addEventListener('mouseover', (event) => {
        const seg = event.target.closest('.pt-bar-segment');
        if (!seg) return;
        const { provider, dim, val, compact, pct, color } = seg.dataset;
        tooltip.innerHTML = `<div class="fgt-title" style="color:${color}">${esc(provider)}</div>
          <div class="fgt-row"><span>Token 类型</span><strong>${esc(dim)}</strong></div>
          <div class="fgt-row"><span>Token 消耗</span><strong>${esc(val)} (${esc(compact)})</strong></div>
          <div class="fgt-row"><span>当前占比</span><strong>${esc(pct)}</strong></div>`;
        tooltip.classList.add('is-visible');
      });
      chart.addEventListener('mousemove', (event) => {
        positionFloatingTooltip(tooltip, event);
      });
      chart.addEventListener('mouseout', (event) => {
        const seg = event.target.closest('.pt-bar-segment');
        if (seg && !chart.contains(event.relatedTarget)) {
          tooltip.classList.remove('is-visible');
        }
      });
      chart.addEventListener('mouseleave', () => {
        tooltip.classList.remove('is-visible');
      });
    }
  }

  function renderAPIKeys(items) {
    const body = $('#api-key-table');
    if (!items.length) return emptyRow(body, 6);
    const total = items.reduce((sum, item) => sum + Number(item.requests || 0), 0) || 1;
    body.innerHTML = items.map((item) => {
      const percentage = Math.min(100, Number(item.requests || 0) / total * 100);
      return `<tr>
        <td><span class="cell-main" title="${esc(item.name || '未识别')}">${esc(item.name || '未识别')}</span></td>
        <td class="text-center">${formatInt(item.models)} 个模型</td>
        <td class="text-center"><div class="progress-cell"><span>${formatInt(item.requests)} <small>(${percentage.toFixed(1)}%)</small></span><div class="progress-bar-bg"><div class="progress-bar-fill" style="width:${percentage.toFixed(2)}%;background:var(--blue)"></div></div></div></td>
        <td class="text-center">${statusBadge(item.success_rate)}</td>
        <td class="text-center" title="${formatInt(item.total_tokens)}">${formatTokenCompact(item.total_tokens)}</td>
        <td class="text-center">${formatMoney(item.cost_usd)}</td>
      </tr>`;
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
      const dotCls = rate < .9 ? 'is-err' : rate < .98 ? 'is-warn' : '';
      return `<tr>
        <td><span class="cell-main" title="${esc(item.name || '未识别')}">${esc(item.name || '未识别')}</span></td>
        <td class="text-center"><span class="status-badge ${rate < .9 ? 'is-failure' : ''}"><i class="status-pulse-dot ${dotCls}"></i>${healthLabel}</span></td>
        <td class="text-center">${formatInt(item.models)} 个模型</td>
        <td class="text-center"><div class="progress-cell"><span>${formatInt(item.requests)} <small>(${percentage.toFixed(1)}%)</small></span><div class="progress-bar-bg"><div class="progress-bar-fill" style="width:${percentage.toFixed(2)}%;background:var(--purple)"></div></div></div></td>
        <td class="text-center">${statusBadge(rate)}</td>
        <td class="text-center">${formatDuration(item.avg_latency_ms)}</td>
        <td class="text-center" title="${formatInt(item.total_tokens)}">${formatTokenCompact(item.total_tokens)}</td>
        <td class="text-center"><button class="secondary-button compact-btn" data-upstream="${esc(item.key)}">${icon('eye')}详情</button></td>
      </tr>`;
    }).join('');
  }

  async function openUpstream(key, trigger) {
    const drawer = $('#detail-drawer');
    state.drawerReturnFocus = trigger || document.activeElement;
    drawer.inert = false;
    drawer.classList.add('is-open');
    drawer.setAttribute('aria-hidden', 'false');
    $('#drawer-scrim').classList.add('is-open');
    $('#detail-title').textContent = '正在加载...';
    $('#detail-content').innerHTML = '<div class="skeleton" style="height:200px"></div>';
    $('#detail-close').focus();
    try {
      const data = await api(`/upstream?range=${encodeURIComponent(state.range)}&key=${encodeURIComponent(key)}`);
      $('#detail-title').textContent = data.name || key;
      const summary = data.summary || {};
      const models = data.models || [];
      const events = data.recent_events || [];
      const recentEventsHtml = events.map((event) => {
        const errText = event.failed ? formatErrorMessage(event.failure, event.status_code) : '';
        const titleText = errText ? `${event.model} (${errText})` : event.model;
        return `<div class="distribution-row" title="${esc(titleText)}"><i style="background:${event.failed ? 'var(--red)' : 'var(--green)'}"></i><span>${esc(event.model)} · ${esc(formatDateTime(event.timestamp_ms))}${errText ? ` <small style="color:var(--red)">(${esc(errText)})</small>` : ''}</span><strong>${formatDuration(event.latency_ms)}</strong></div>`;
      }).join('') || '<span class="cell-sub">暂无数据</span>';

      $('#detail-content').innerHTML = `
        <div class="detail-kpis">
          ${metric('请求', formatInt(summary.requests))}
          ${metric('成功率', formatPercent(summary.success_rate))}
          ${metric('Token', formatCompact(summary.total_tokens))}
          ${metric('平均延迟', formatDuration(summary.avg_latency_ms))}
        </div>
        <section class="detail-section">
          <h3>模型</h3>
          ${models.map((item) => `<div class="distribution-row"><i style="background:var(--accent)"></i><span>${esc(item.name)}</span><strong>${formatInt(item.requests)}</strong></div>`).join('') || '<span class="cell-sub">暂无数据</span>'}
        </section>
        <section class="detail-section">
          <h3>近期事件</h3>
          ${recentEventsHtml}
        </section>
      `;
    } catch (error) {
      $('#detail-title').textContent = '加载失败';
      $('#detail-content').textContent = error.message;
    }
  }

  function bindDrawer() {
    $('#detail-close')?.addEventListener('click', () => closeDrawer(true));
    $('#drawer-scrim')?.addEventListener('click', () => closeDrawer(true));
    document.addEventListener('keydown', (e) => {
      if (e.key === 'Escape') {
        const drawer = $('#detail-drawer');
        if (drawer && drawer.classList.contains('is-open')) {
          closeDrawer(true);
        }
      }
    });

    // Delegate click for upstream detail buttons
    document.addEventListener('click', (event) => {
      const btn = event.target.closest('[data-upstream]');
      if (btn) {
        event.preventDefault();
        const key = btn.dataset.upstream;
        if (key) {
          openUpstream(key, btn);
        }
      }
    });
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
    if (!select) return;
    const current = select.value;
    select.innerHTML = '<option value="">全部</option>' + items.map((item) => `<option value="${esc(item.key)}">${esc(item.name)}</option>`).join('');
    select.value = current;
    syncCustomSelectOptions(select);
  }

  function initCustomSelects() {
    $$('#event-filters select').forEach((select) => {
      if (select.closest('.custom-select-wrapper')) return;
      const wrapper = document.createElement('div');
      wrapper.className = 'custom-select-wrapper';
      select.parentNode.insertBefore(wrapper, select);
      wrapper.appendChild(select);
      select.classList.add('sr-only-select');

      const trigger = document.createElement('button');
      trigger.type = 'button';
      trigger.className = 'custom-select-trigger';
      trigger.setAttribute('aria-haspopup', 'listbox');
      trigger.setAttribute('aria-expanded', 'false');
      trigger.innerHTML = `<span class="cs-label">${esc(select.options[select.selectedIndex]?.text || '全部')}</span><svg class="cs-arrow" viewBox="0 0 24 24"><polyline points="6 9 12 15 18 9"/></svg>`;

      const dropdown = document.createElement('div');
      dropdown.className = 'custom-select-dropdown';
      dropdown.setAttribute('role', 'listbox');
      dropdown.hidden = true;

      wrapper.appendChild(trigger);
      wrapper.appendChild(dropdown);

      trigger.addEventListener('click', (e) => {
        e.stopPropagation();
        const isOpen = !dropdown.hidden;
        closeAllCustomSelects();
        if (!isOpen) {
          syncCustomSelectOptions(select);
          dropdown.hidden = false;
          wrapper.classList.add('is-open');
          trigger.setAttribute('aria-expanded', 'true');
        }
      });
    });

    document.addEventListener('click', (e) => {
      if (!e.target.closest('.custom-select-wrapper')) {
        closeAllCustomSelects();
      }
    });

    document.addEventListener('keydown', (e) => {
      if (e.key === 'Escape') {
        closeAllCustomSelects();
      }
    });

    $('#event-reset')?.addEventListener('click', () => {
      setTimeout(() => {
        $$('#event-filters select').forEach((select) => {
          select.value = '';
          const wrapper = select.closest('.custom-select-wrapper');
          if (wrapper) {
            const label = wrapper.querySelector('.cs-label');
            if (label) label.textContent = '全部';
            syncCustomSelectOptions(select);
          }
        });
      }, 0);
    });
  }

  function closeAllCustomSelects() {
    $$('.custom-select-wrapper').forEach((w) => {
      w.classList.remove('is-open');
      const dd = w.querySelector('.custom-select-dropdown');
      if (dd) dd.hidden = true;
      const trigger = w.querySelector('.custom-select-trigger');
      if (trigger) trigger.setAttribute('aria-expanded', 'false');
    });
  }

  function syncCustomSelectOptions(select) {
    const wrapper = select.closest('.custom-select-wrapper');
    if (!wrapper) return;
    const dropdown = wrapper.querySelector('.custom-select-dropdown');
    const label = wrapper.querySelector('.cs-label');
    if (!dropdown || !label) return;

    const currentVal = select.value;
    const options = [...select.options];
    const selectedOption = options.find((o) => o.value === currentVal) || options[0];
    label.textContent = selectedOption ? selectedOption.text : '全部';

    dropdown.innerHTML = options.map((opt) => `
      <button type="button" class="custom-select-option ${opt.value === currentVal ? 'is-selected' : ''}" data-value="${esc(opt.value)}">
        <span>${esc(opt.text)}</span>
        <svg class="cso-check" viewBox="0 0 24 24"><polyline points="20 6 9 17 4 12"/></svg>
      </button>
    `).join('');

    dropdown.querySelectorAll('.custom-select-option').forEach((optBtn) => {
      optBtn.addEventListener('click', (e) => {
        e.stopPropagation();
        const val = optBtn.dataset.value;
        select.value = val;
        label.textContent = optBtn.querySelector('span')?.textContent || '全部';
        closeAllCustomSelects();
        select.dispatchEvent(new Event('change', { bubbles: true }));
      });
    });
  }

  function eventParams() {
    return new URLSearchParams({ range: state.range, page: String(state.eventPage), page_size: '25', ...state.eventFilters });
  }

  function formatTPS(outputTokens, latencyMs, ttftMs) {
    const tokens = Math.max(0, Number(outputTokens || 0));
    const latency = Math.max(0, Number(latencyMs || 0));
    const ttft = Math.max(0, Number(ttftMs || 0));
    if (tokens <= 0 || latency <= 0) return '--';

    let durationSec = (ttft > 0 && latency > ttft) ? (latency - ttft) / 1000 : latency / 1000;
    if (durationSec < 0.02) durationSec = 0.02;

    const tps = tokens / durationSec;
    if (!isFinite(tps) || tps <= 0) return '--';
    return tps >= 100 ? `${tps.toFixed(0)} t/s` : `${tps.toFixed(1)} t/s`;
  }

  function unescapeHTML(str) {
    if (!str) return '';
    return String(str)
      .replace(/&#34;|&quot;/g, '"')
      .replace(/&#39;|&apos;/g, "'")
      .replace(/&#47;/g, '/')
      .replace(/&#92;/g, '\\')
      .replace(/&lt;/g, '<')
      .replace(/&gt;/g, '>')
      .replace(/&amp;/g, '&');
  }

  function formatErrorMessage(raw, statusCode) {
    let text = unescapeHTML(String(raw || '')).trim();
    if (!text) {
      if (!statusCode || statusCode === 200) return '';
      const codeMap = {
        400: '参数错误 (Bad Request)',
        401: '鉴权失败 (Unauthorized)',
        403: '拒绝访问 (Forbidden)',
        404: '模型不存在 (Not Found)',
        429: '上游限流 (Rate Limit)',
        500: '服务错误 (Internal Error)',
        502: '网关异常 (Bad Gateway)',
        503: '服务过载 (Service Unavailable)',
        504: '网关超时 (Gateway Timeout)',
      };
      return codeMap[statusCode] || `HTTP ${statusCode}`;
    }

    if ((text.startsWith('{') && text.endsWith('}')) || (text.startsWith('[') && text.endsWith(']'))) {
      try {
        const obj = JSON.parse(text);
        if (obj && typeof obj === 'object') {
          if (obj.error) {
            if (typeof obj.error === 'string') return obj.error;
            if (typeof obj.error === 'object') {
              const code = obj.error.code || obj.error.type || obj.error.status || '';
              const msg = obj.error.message || obj.error.msg || obj.error.detail || obj.error.error || '';
              if (code && msg) return `[${code}] ${msg}`;
              if (msg) return String(msg);
              if (code) return String(code);
            }
          }
          if (obj.message) return obj.code ? `[${obj.code}] ${obj.message}` : String(obj.message);
          if (obj.msg) return String(obj.msg);
          if (obj.detail) {
            if (typeof obj.detail === 'string') return obj.detail;
            if (Array.isArray(obj.detail)) return obj.detail.map((d) => d.msg || JSON.stringify(d)).join('; ');
          }
          if (obj.error_description) return String(obj.error_description);
        }
      } catch (e) {}
    }

    if (text.includes('<html') || text.includes('<!DOCTYPE') || text.includes('<title>')) {
      const titleMatch = text.match(/<title>(.*?)<\/title>/i);
      if (titleMatch && titleMatch[1]) return titleMatch[1].trim();
      const h1Match = text.match(/<h1>(.*?)<\/h1>/i);
      if (h1Match && h1Match[1]) return h1Match[1].trim();
      text = text.replace(/<[^>]+>/g, ' ').replace(/\s+/g, ' ').trim();
    }

    return text;
  }

  function renderEvents(data) {
    const body = $('#event-table');
    $('#event-count').textContent = `${formatInt(data.total)} 条记录`;
    $('#page-label').textContent = data.pages ? `第 ${data.page} / ${data.pages} 页` : '第 0 页';
    $('#page-prev').disabled = data.page <= 1;
    $('#page-next').disabled = !data.pages || data.page >= data.pages;
    if (!(data.events || []).length) return emptyRow(body, 9);
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
      const tps = formatTPS(outputTokens, event.latency_ms, event.ttft_ms);
      const status = event.failed ? `失败${event.status_code ? ` ${event.status_code}` : ''}` : '成功';
      const errorMsg = formatErrorMessage(event.failure, event.status_code);
      const errorSubHtml = (event.failed && errorMsg) ? `<span class="cell-sub error-detail-sub" title="${esc(errorMsg)}">${esc(errorMsg)}</span>` : '';
      const tokenCellHtml = renderTokenCell(uncachedInputTokens, outputTokens, cacheReadTokens, cacheCreationTokens, reasoningTokens, totalTokens);
      const costCellHtml = renderCostCell(event);
      return `<tr>
        <td data-label="时间"><span class="cell-main">${esc(formatDateTime(event.timestamp_ms))}</span></td>
        <td data-label="模型 / 渠道"><span class="cell-main" title="${esc(event.model)}">${esc(event.model)}</span><span class="cell-sub" title="${esc(event.upstream_label)}">${esc(event.upstream_label)}</span></td>
        <td data-label="推理强度" class="text-center"><span class="cell-main">${esc(event.reasoning_effort || '--')}</span></td>
        <td data-label="状态" class="text-center"><span class="status-badge ${event.failed ? 'is-failure' : ''}">${status}</span>${errorSubHtml}</td>
        <td data-label="用时 / 首字" class="text-center"><span class="cell-main">${formatDuration(event.latency_ms)}</span><span class="cell-sub">首字 ${ttft}</span></td>
        <td data-label="Token 明细" class="text-center">${tokenCellHtml}</td>
        <td data-label="命中率" class="text-center">${hitRateBadge(rate)}</td>
        <td data-label="总 Token / 速度" class="text-center"><span class="cell-main" title="${formatInt(totalTokens)} Token"><strong>${formatTokenCompact(totalTokens)}</strong></span><span class="cell-sub" title="输出生成速率 (TPS)">速度 ${tps}</span></td>
        <td data-label="费用" class="text-center">${costCellHtml}</td>
      </tr>`;
    }).join('');
  }

  async function loadSettings(force, signal, requestID) {
    const [settings, prices, analysis] = await Promise.all([
      api('/settings', { signal }),
      api('/prices', { signal }),
      api('/analysis?range=all', { signal }).catch(() => null),
    ]);
    if (requestID !== state.loadRequestID || signal.aborted) return;
    if (analysis?.models || analysis?.distributions?.models) {
      state.knownRequestedModels = analysis.models || analysis.distributions?.models || [];
    }
    updateActiveModelsDatalist();
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
      ['数据库大小', formatBytes(storage.database_bytes)],
      ['总事件记录', formatInt(storage.event_count)],
      ['分钟 Rollup', formatInt(storage.rollup_count)],
      ['最近批写耗时', `${formatNumber(runtime.last_batch_ms, 2)} ms`],
      ['写入失败数', formatInt(runtime.write_failures)],
      ['队列丢弃数', formatInt(runtime.dropped)],
    ];
    $('#storage-metrics').innerHTML = metrics.map(([label, value]) => metric(label, value)).join('');
  }

  function getRequestedModelsList() {
    const map = new Map();
    (state.knownRequestedModels || []).forEach((item) => {
      if (item.name && item.name !== '未识别') {
        map.set(item.name.toLowerCase(), { name: item.name, requests: Number(item.requests || 0) });
      }
    });
    $$('#model-table tr').forEach((row) => {
      const nameEl = row.querySelector('.cell-main');
      const reqEl = row.querySelector('td:nth-child(2)');
      if (nameEl) {
        const name = nameEl.textContent.trim();
        const reqStr = reqEl ? reqEl.textContent.replace(/[^0-9]/g, '') : '0';
        if (name && !map.has(name.toLowerCase())) {
          map.set(name.toLowerCase(), { name, requests: Number(reqStr || 0) });
        }
      }
    });
    $$('#event-table td[data-label="模型 / 渠道"] .cell-main').forEach((el) => {
      const name = el.textContent.trim();
      if (name && !map.has(name.toLowerCase())) {
        map.set(name.toLowerCase(), { name, requests: 1 });
      }
    });
    return [...map.values()].sort((a, b) => b.requests - a.requests);
  }

  function updateActiveModelsDatalist() {
    let datalist = $('#model-name-datalist');
    if (!datalist) {
      datalist = document.createElement('datalist');
      datalist.id = 'model-name-datalist';
      document.body.appendChild(datalist);
    }
    const requested = getRequestedModelsList();
    let html = '';
    if (requested.length > 0) {
      requested.forEach((item) => {
        html += `<option value="${esc(item.name)}">${esc(item.name)} (${item.requests ? `已产生 ${formatInt(item.requests)} 次请求` : '已产生请求'})</option>`;
      });
    }
    datalist.innerHTML = html;
  }

  function applyModelToRow(row, modelName) {
    const input = row.querySelector('[data-price-field="model"]');
    if (input) {
      input.value = modelName;
      input.dispatchEvent(new Event('input', { bubbles: true }));
    }
  }

  function parseImportedPrices(rawText) {
    const text = String(rawText || '').trim();
    if (!text) return [];

    if (text.startsWith('[') || text.startsWith('{')) {
      try {
        const parsed = JSON.parse(text);
        if (Array.isArray(parsed)) {
          return parsed.map((item) => {
            if (!item || typeof item !== 'object') return null;
            const model = String(item.model || item.id || item.name || '').trim();
            if (!model) return null;
            return {
              model,
              input_per_million: Number(item.input_per_million ?? item.input ?? item.in ?? item.prompt ?? 0),
              output_per_million: Number(item.output_per_million ?? item.output ?? item.out ?? item.completion ?? 0),
              cache_read_per_million: Number(item.cache_read_per_million ?? item.cache_read ?? item.cached ?? 0),
              cache_write_per_million: Number(item.cache_write_per_million ?? item.cache_write ?? item.cache_creation ?? 0),
              reasoning_per_million: Number(item.reasoning_per_million ?? item.reasoning ?? item.thinking ?? 0),
            };
          }).filter(Boolean);
        } else if (typeof parsed === 'object') {
          return Object.entries(parsed).map(([modelName, val]) => {
            const model = String(modelName || '').trim();
            if (!model) return null;
            if (typeof val === 'number') {
              return { model, input_per_million: val, output_per_million: val * 3, cache_read_per_million: val * 0.5, cache_write_per_million: val, reasoning_per_million: 0 };
            }
            if (Array.isArray(val)) {
              return {
                model,
                input_per_million: Number(val[0] || 0),
                output_per_million: Number(val[1] || 0),
                cache_read_per_million: Number(val[2] || 0),
                cache_write_per_million: Number(val[3] || 0),
                reasoning_per_million: Number(val[4] || 0),
              };
            }
            if (typeof val === 'object' && val !== null) {
              return {
                model,
                input_per_million: Number(val.input_per_million ?? val.input ?? val.in ?? val.prompt ?? 0),
                output_per_million: Number(val.output_per_million ?? val.output ?? val.out ?? val.completion ?? 0),
                cache_read_per_million: Number(val.cache_read_per_million ?? val.cache_read ?? val.cached ?? 0),
                cache_write_per_million: Number(val.cache_write_per_million ?? val.cache_write ?? val.cache_creation ?? 0),
                reasoning_per_million: Number(val.reasoning_per_million ?? val.reasoning ?? val.thinking ?? 0),
              };
            }
            return null;
          }).filter(Boolean);
        }
      } catch (e) {}
    }

    const lines = text.split(/\r?\n/).map((l) => l.trim()).filter(Boolean);
    const results = [];
    for (const line of lines) {
      if (line.startsWith('#') || line.startsWith('//')) continue;
      const parts = line.includes('\t') ? line.split('\t') : (line.includes(',') ? line.split(',') : line.split(/\s+/));
      const model = String(parts[0] || '').trim();
      if (!model || model.toLowerCase() === 'model' || model === '模型') continue;
      results.push({
        model,
        input_per_million: Number(parts[1] || 0),
        output_per_million: Number(parts[2] || 0),
        cache_read_per_million: Number(parts[3] || 0),
        cache_write_per_million: Number(parts[4] || 0),
        reasoning_per_million: Number(parts[5] || 0),
      });
    }
    return results;
  }

  function renderPrices(prices) {
    const body = $('#price-table');
    body.innerHTML = '';
    prices.forEach(appendPriceRow);
    if (!prices.length) appendPriceRow({});
    applyPriceFilter($('#price-search')?.value || '');
  }

  function appendPriceRow(price = {}) {
    const row = document.createElement('tr');
    const fields = ['input_per_million', 'output_per_million', 'cache_read_per_million', 'cache_write_per_million', 'reasoning_per_million'];
    
    const modelCellHtml = `
      <td>
        <div class="model-input-compound">
          <input data-price-field="model" type="text" placeholder="选择已有模型或输入自定义..." value="${esc(price.model ?? '')}" autocomplete="off">
          <button type="button" class="model-picker-btn" title="选择已产生请求的模型" aria-label="快捷选择模型">
            <svg viewBox="0 0 24 24"><polyline points="6 9 12 15 18 9"/></svg>
          </button>
        </div>
      </td>
    `;

    const priceCellsHtml = fields.map((field) =>
      `<td><input data-price-field="${field}" type="number" min="0" step="0.000001" value="${esc(price[field] ?? '')}"></td>`
    ).join('');

    const actionsCellHtml = `
      <td>
        <div class="price-row-actions">
          <button class="icon-button" data-clone-price title="复制行" aria-label="复制模型价格"><svg><use href="#i-copy"></use></svg></button>
          <button class="icon-button" data-remove-price title="删除" aria-label="删除模型价格"><svg><use href="#i-trash"></use></svg></button>
        </div>
      </td>
    `;

    row.innerHTML = modelCellHtml + priceCellsHtml + actionsCellHtml;
    $('#price-table').appendChild(row);
  }

  function getTablePrices() {
    return $$('#price-table tr').map((row) => {
      const value = {};
      $$('[data-price-field]', row).forEach((input) => {
        value[input.dataset.priceField] = input.dataset.priceField === 'model' ? input.value.trim() : Number(input.value || 0);
      });
      return value;
    }).filter((item) => item.model);
  }

  function applyPriceFilter(query) {
    const q = query.trim().toLowerCase();
    $$('#price-table tr').forEach((row) => {
      const modelInput = row.querySelector('[data-price-field="model"]');
      if (!modelInput) return;
      const model = (modelInput.value || '').toLowerCase();
      row.hidden = Boolean(q && !model.includes(q));
    });
  }

  async function savePrices() {
    const prices = getTablePrices();
    try {
      const data = await api('/prices', { method: 'PUT', body: JSON.stringify({ prices }) });
      renderPrices(data.prices || []);
      clearFrontendCache();
      toast(`已成功保存 ${prices.length} 个模型价格`);
    } catch (error) { toast(error.message, true); }
  }

  function exportPricesJSON() {
    const prices = getTablePrices();
    const blob = new Blob([JSON.stringify(prices, null, 2)], { type: 'application/json' });
    const url = URL.createObjectURL(blob);
    const link = document.createElement('a');
    link.href = url;
    link.download = `model-prices-${new Date().toISOString().slice(0, 10)}.json`;
    link.click();
    URL.revokeObjectURL(url);
    toast(`已导出 ${prices.length} 个模型价格配置`);
  }

  function autoFillUnpricedModels() {
    const existing = new Set(getTablePrices().map((p) => p.model.toLowerCase()));
    const discovered = new Set();

    // From model table in overview
    $$('#model-table .cell-main').forEach((el) => {
      const name = el.textContent.trim();
      if (name && !existing.has(name.toLowerCase())) discovered.add(name);
    });

    // From event rows
    $$('#event-table td[data-label="模型 / 渠道"] .cell-main').forEach((el) => {
      const name = el.textContent.trim();
      if (name && !existing.has(name.toLowerCase())) discovered.add(name);
    });

    if (!discovered.size) {
      return toast('所有活跃调用的模型已在价格表中设定');
    }

    discovered.forEach((model) => {
      appendPriceRow({ model, input_per_million: 0, output_per_million: 0, cache_read_per_million: 0, cache_write_per_million: 0, reasoning_per_million: 0 });
    });

    toast(`已自动补全 ${discovered.size} 个未定价模型（请填入单价并点击保存）`);
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
    if (dot) {
      dot.classList.toggle('is-online', online);
      dot.classList.toggle('is-error', !online);
      const statusBox = dot.closest('.top-nav-status');
      if (statusBox) {
        statusBox.setAttribute('title', online ? '采集服务在线' : '管理连接中断');
      }
    }
    const label = $('#connection-label');
    if (label) {
      label.textContent = online ? '采集服务在线' : '管理连接中断';
    }
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
  function formatTokenCompact(value) { return compact.format(Number(value || 0)); }
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
