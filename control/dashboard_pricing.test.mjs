import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';

// Execute the actual embedded dashboard, not a duplicate cost implementation.
const html = fs.readFileSync(new URL('./dashboard.html', import.meta.url), 'utf8');
const script = html.slice(html.indexOf('<script>') + 8, html.lastIndexOf('</script>')).replace(/showDashboard\(\);\s*$/, '');
function dashboard(fetch, baseHref = '/') {
  const elements = new Map();
  const element = id => {
    if (!elements.has(id)) elements.set(id, { value: id === 'apiToken' ? 'test-admin' : '', innerHTML: '', textContent: '', dataset: {} });
    return elements.get(id);
  };
  const baseURI = new URL(baseHref, 'https://unit.test/dashboard').href;
  const ctx = vm.createContext({
    document: { getElementById: element, baseURI },
    localStorage: { getItem: () => null, setItem: () => {} },
    URL, URLSearchParams,
    fetch: (input, init) => { const url = new URL(input, baseURI); return fetch(url.pathname + url.search, init); },
    console: { warn: () => {}, error: () => {} },
    setInterval: () => 1, clearInterval: () => {},
  });
  vm.runInContext(script, ctx);
  return { ctx, element, run: code => vm.runInContext(code, ctx) };
}
const response = (data, status = 200) => ({ ok: status >= 200 && status < 300, status, json: async () => data });
const price = { input_cost_per_token: 5e-6, output_cost_per_token: 25e-6, cache_read_input_token_cost: .5e-6, cache_creation_input_token_cost: 6.25e-6 };
const row = (model = 'retired-claude', overrides = {}) => ({ date: '2026-09-01', model, request_count: 1, tokens_in: 3e6, tokens_cached: 1e6, tokens_new_cache: 1e6, tokens_out: 1e6, ...overrides });
async function setPrices(d, rows, prices) {
  d.ctx.rows = rows;
  d.ctx.fetch = async () => response(prices);
  await d.run(`fetchPricing(rows.map(d => d.model), 'test-admin').then(p => { PRICING = p; pricingLoaded = true; })`);
}

test('fetchData requests historical models and renders corrected estimate after pricing resolves', async () => {
  const calls = [];
  const d = dashboard(async url => {
    calls.push(url);
    if (url.startsWith('/usage?')) return response([row()]);
    const params = new URL(url, 'https://unit.test').searchParams;
    assert.equal(params.get('models'), 'retired-claude');
    return response({ 'retired-claude': price });
  });
  d.run('populateFilters = () => {}; renderChart = () => {};');
  await d.run('fetchData()');
  assert.equal(calls.length, 2);
  assert.match(d.element('cards').innerHTML, /\$36\.75/); // 5 + .5 + 6.25 +25, no extra5
  assert.match(d.element('pricingStatus').textContent, /Priced 1 \/ 1 requests/);
  assert.doesNotMatch(d.element('cards').innerHTML, /partial/);
});

test('prefixed dashboard fetches both usage and pricing under its base path', async () => {
  const calls = [];
  const d = dashboard(async url => {
    calls.push(url);
    if (url.startsWith('/copilot/usage?')) return response([row()]);
    if (url.startsWith('/copilot/usage/pricing?')) return response({ 'retired-claude': price });
    throw new Error(`unexpected URL ${url}`);
  }, '/copilot/');
  d.run('populateFilters = () => {}; renderChart = () => {};');
  await d.run('fetchData()');
  assert.equal(calls.length, 2);
  assert.ok(calls[0].startsWith('/copilot/usage?'));
  assert.ok(calls[1].startsWith('/copilot/usage/pricing?'));
  assert.match(d.element('cards').innerHTML, /\$36\.75/);
});

test('missing models/required rates are explicit, zero rates valid, no fictional cache rate', async () => {
  const d = dashboard();
  const rows = [row(), row('<img src=x onerror=bad()>'), row('missing-cache'), row('free')];
  await setPrices(d, rows, { 'retired-claude': price, 'missing-cache': { input_cost_per_token: 1e-6, output_cost_per_token: 1e-6 }, free: { input_cost_per_token: 0, output_cost_per_token: 0, cache_read_input_token_cost: 0, cache_creation_input_token_cost: 0 } });
  const e = d.run('estimateCost(rows)');
  assert.equal(e.cost, 36.75); assert.equal(e.pricedRequests, 2); assert.equal(e.missingModels.length, 2);
  d.run('renderCards(rows)');
  assert.match(d.element('cards').innerHTML, /Est. Cost \(partial\)/);
  assert.match(d.element('pricingStatus').textContent, /Priced 2 \/ 4 requests/);
  assert.match(d.element('pricingStatus').textContent, /<img src=x onerror=bad\(\)>/); // only textContent
  assert.doesNotMatch(d.element('cards').innerHTML, /onerror/);
  d.run("renderCards(rows.filter(r => r.model === 'free'))");
  assert.match(d.element('cards').innerHTML, /\$0\.0000/);
  d.run("renderCards(rows.filter(r => r.model === 'missing-cache'))");
  assert.match(d.element('cards').innerHTML, /Unavailable/);
  assert.doesNotMatch(d.element('cards').innerHTML, /\$0\.0000/);
});

test('no cache rates needed for zero cache tokens; missing writes not silently zero', async () => {
  const d = dashboard();
  await setPrices(d, [row('plain')], { plain: { input_cost_per_token: 0, output_cost_per_token: 1e-6 } });
  d.ctx.rows = [row('plain', { tokens_cached: 0, tokens_new_cache: 0 })];
  assert.equal(d.run('estimateCost(rows).cost'), 1);
  assert.equal(d.run('estimateCost(rows).missingModels.length'), 0);
  d.ctx.rows = [row('plain', { tokens_cached: 0 })];
  assert.equal(d.run('estimateCost(rows).missingModels.length'), 1);
});

test('pricing fetch failure is unavailable, retried on refresh, never stale cost', async () => {
  let fail = false;
  const d = dashboard(async url => url.startsWith('/usage?') ? response([row()]) : fail ? response({}, 503) : response({ 'retired-claude': price }));
  d.run('populateFilters = () => {}; renderChart = () => {};');
  await d.run('fetchData()');
  assert.match(d.element('cards').innerHTML, /\$36\.75/);
  fail = true; await d.run('fetchData()');
  assert.match(d.element('cards').innerHTML, /Unavailable/);
  assert.doesNotMatch(d.element('cards').innerHTML, /36\.75/);
  fail = false; await d.run('fetchData()');
  assert.match(d.element('cards').innerHTML, /\$36\.75/);
});

test('empty usage avoids default catalog request; large model lists are batched/deduped', async () => {
  const calls = [];
  const d = dashboard(async url => { calls.push(url); return response({}); });
  await d.run("fetchPricing([], 'token')"); assert.equal(calls.length, 0);
  d.ctx.names = [...Array.from({length: 121}, (_,i) => `model-${i}`), 'model-0'];
  await d.run("fetchPricing(names, 'token')");
  assert.equal(calls.length, 3);
  assert.equal(calls.flatMap(url => new URL(url, 'https://unit.test').searchParams.get('models').split(',')).length, 121);
  for (const url of calls) assert.ok(new URL(url, 'https://unit.test').searchParams.get('models').split(',').length <= 50);
});

test('out-of-order price responses cannot overwrite new range/token results', async () => {
  let releaseOld, markStarted;
  const oldStarted = new Promise(resolve => { markStarted = resolve; });
  let count = 0;
  const d = dashboard(async url => {
    if (url.startsWith('/usage?')) return response([row(++count === 1 ? 'old' : 'new')]);
    const model = new URL(url, 'https://unit.test').searchParams.get('models');
    if (model === 'old') return new Promise(resolve => { releaseOld = () => resolve(response({ old: price })); markStarted(); });
    return response({ new: { ...price, input_cost_per_token: 1e-6 } });
  });
  d.run('populateFilters = () => {}; renderChart = () => {};');
  const old = d.run('fetchData()');
  await oldStarted;
  assert.equal(typeof releaseOld, 'function');
  await d.run('fetchData()');
  assert.match(d.element('cards').innerHTML, /\$32\.75/);
  releaseOld(); await old;
  assert.match(d.element('cards').innerHTML, /\$32\.75/);
  assert.equal(d.run("rawData[0].model"), 'new');
});

test('chart label uses the same cost buckets and labels partial coverage', async () => {
  const d = dashboard();
  await setPrices(d, [row()], { 'retired-claude': price });
  d.ctx.Chart = class { constructor(_canvas, config) { this.data = config.data; d.ctx.chartConfig = config; } destroy() {} };
  d.run('renderChart(rows)');
  function labels() {
    const cfg = d.ctx.chartConfig, drawn = [];
    const ctx = { save(){}, restore(){}, fillText(t){drawn.push(t);}, measureText(t){return {width:t.length};} };
    cfg.plugins[0].afterDatasetsDraw({ ctx, data: cfg.data, getDatasetMeta: () => ({ hidden: false, data: [{x:10,y:50}] }) });
    return drawn.join(' ');
  }
  assert.match(labels(), /\$36\.75/);
  d.ctx.rows = [row(), row('unknown')]; d.run('renderChart(rows)');
  assert.match(labels(), /\$36\.75 \(partial\)/);
});
