const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const nodes = new Map();
const animationFrames = [];
function node(selector) {
  if (!nodes.has(selector)) {
    const classes = new Set();
    nodes.set(selector, {
      id: selector.startsWith('#') ? selector.slice(1) : '',
      innerHTML: '', textContent: '', inert: true, isConnected: true,
      classList: {
        add: (value) => classes.add(value),
        remove: (value) => classes.delete(value),
        contains: (value) => classes.has(value),
      },
      setAttribute() {},
      focus() {},
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
  console,
  performance: { now: () => 1_000 },
  requestAnimationFrame: (callback) => {
    animationFrames.push(callback);
    return animationFrames.length;
  },
  document: { addEventListener() {}, querySelector: node, activeElement: null },
  sessionStorage: { getItem: () => 'test-key' },
  localStorage: { getItem: () => null },
  fetch: (url, options = {}) => new Promise((resolve, reject) => {
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
  'globalThis.testAPI = { state, animateNumber, renderTokenComposition, openUpstream, closeDrawer }; })();',
), context);

function response(name) {
  return {
    ok: true,
    status: 200,
    headers: new Headers({ 'Content-Type': 'application/json' }),
    json: async () => ({ name, summary: {}, models: [], recent_events: [] }),
  };
}

async function main() {
  const { state, animateNumber, renderTokenComposition, openUpstream } = context.testAPI;

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
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
