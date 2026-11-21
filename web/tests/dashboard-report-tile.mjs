// A saved report as a dashboard tile (plans/ad-tracker-ui.md B7) against
// mocked API answers: the tile shows the report's first level by name with
// its totals, Add a tile lists the saved reports, and a private one is
// shared before it is added. Starts a local Vite server and mocks every API
// request; needs no backend.
import { chromium } from 'playwright';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createServer } from 'node:net';
import { fileURLToPath } from 'node:url';

const web = fileURLToPath(new URL('../', import.meta.url));
const port = await new Promise(resolve => {
  const listener = createServer(); listener.listen(0, '127.0.0.1', () => { const port = listener.address().port; listener.close(() => resolve(port)); });
});
const origin = `http://127.0.0.1:${port}`;
const server = spawn(process.execPath, [fileURLToPath(new URL('../node_modules/vite/bin/vite.js', import.meta.url)), '--host', '127.0.0.1', '--port', String(port), '--strictPort'], { cwd: web, stdio: 'pipe' });
let output = ''; server.stdout.on('data', b => { output += b; }); server.stderr.on('data', b => { output += b; });

const brand = '10000000-0000-0000-0000-000000000001';
const dash = 'd0000000-0000-0000-0000-000000000001';
const camp = '30000000-0000-0000-0000-000000000001';
const shared = 'a0000000-0000-0000-0000-000000000001';
const mine = 'a0000000-0000-0000-0000-000000000002';
const metrics = (visits, conversions, revenue, cost) => ({ visits, visitors: visits, conversions, revenue, cost, profit: revenue - cost, roi: (revenue - cost) / cost, roas: revenue / cost,
  cr: conversions / visits, epv: revenue / visits, cpa: cost / conversions, lander_views: 0, lander_clicks: 0, lander_ctr: null, custom: {} });
const tile = (id, view, name) => ({ id, insight_id: null, insight: null, report_view_id: view, report_view: { id: view, name, source: 'tracker', group_by: ['tracking_campaign'] }, note: '', x: 0, y: 0, w: 6, h: 5 });

let browser;
try {
  for (let i = 0; i < 100 && !output.includes('ready in'); i++) await new Promise(r => setTimeout(r, 200));
  browser = await chromium.launch({ headless: true, args: ['--no-sandbox'], executablePath: process.env.OUTFLARE_TEST_CHROMIUM || undefined });
  const calls = [];
  const tiles = [tile('t1', shared, 'Profit by campaign')];
  const page = await browser.newPage({ viewport: { width: 1280, height: 1000 } });
  const errors = []; page.on('pageerror', e => errors.push(e.message));
  await page.route('**/v1/**', async route => {
    const req = route.request(); const u = new URL(req.url()); const p = u.pathname;
    calls.push(`${req.method()} ${p}${u.search} ${req.postData() ?? ''}`.trim());
    const B = `/v1/brands/${brand}`;
    let status = 200, body = {};
    const detail = () => ({ id: dash, brand_id: brand, name: 'Tracker', description: '', settings: {}, tiles: tiles.length, share: null, created_by: null, created_at: '2026-10-01T00:00:00Z', updated_at: '2026-10-01T00:00:00Z', tile_list: tiles });
    if (p === '/v1/workspaces/workspace-a/brands') body = { data: [{ id: brand, name: 'Acme', handle: 'acme', status: 'active', timezone: 'UTC' }] };
    else if (p === `${B}/dashboards/${dash}` && req.method() === 'GET') body = detail();
    else if (p === `${B}/dashboards/${dash}/tiles` && req.method() === 'POST') { tiles.push(tile('t2', req.postDataJSON().report_view_id, 'By country')); status = 201; body = detail(); }
    else if (p === `${B}/drilldown`) body = { from: '2026-10-01', to: '2026-10-07', timezone: 'UTC', model: 'last_touch', conversion_time: 'visit', source: 'tracker', currency: 'USD', group_by: ['tracking_campaign'],
      cost_available: true, cost_note: null, truncated: false, totals: metrics(1200, 40, 100, 60),
      rows: [{ keys: [camp], metrics: metrics(900, 30, 75, 40), rows: [] }, { keys: [''], metrics: metrics(300, 10, 25, 20), rows: [] }] };
    else if (p === `${B}/tracking-campaigns`) body = { data: [{ id: camp, name: 'Push US' }] };
    else if (p === `${B}/report-views` && req.method() === 'GET') body = { data: [
      { id: shared, brand_id: brand, name: 'Profit by campaign', spec: { source: 'tracker', group_by: ['tracking_campaign'] }, shared: true, mine: true, created_at: '2026-10-01T00:00:00Z', updated_at: '2026-10-01T00:00:00Z' },
      { id: mine, brand_id: brand, name: 'By country', spec: { source: 'tracker', group_by: ['country'] }, shared: false, mine: true, created_at: '2026-10-01T00:00:00Z', updated_at: '2026-10-01T00:00:00Z' }] };
    else if (p === `${B}/report-views/${mine}` && req.method() === 'PATCH') body = { id: mine, brand_id: brand, name: 'By country', spec: {}, shared: true, mine: true, created_at: '2026-10-01T00:00:00Z', updated_at: '2026-10-01T00:00:00Z' };
    else if (p === `${B}/insights`) body = { data: [], next_cursor: null };
    else if (p === `${B}/annotations`) body = { data: [] };
    else { status = 404; body = { error: { code: 'not_found', message: 'nothing here', request_id: 'r' } }; }
    await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  });
  await page.goto(`${origin}/tests/fixtures/dashboard-report-tile.html`);

  // The tile: the report's name, what it reports on and its dates, the
  // campaign by name, a row with no value, and the totals.
  const card = page.getByTestId('report-tile').first();
  await card.getByText('Push US').waitFor();
  const text = await card.innerText();
  assert.match(text, /Profit by campaign/);
  assert.match(text, /Tracker clicks · 2026-10-01 to 2026-10-07/);
  assert.match(text, /\(none\)/);
  assert.match(text, /Total[\s\S]*1,200[\s\S]*40[\s\S]*\$100[\s\S]*\$60[\s\S]*\$40/);
  assert.ok(calls.some(c => c.includes(`/drilldown?view=${shared}`)), 'the tile runs the saved report');

  // Add a tile lists the saved reports; a private one is shared first.
  await page.getByRole('button', { name: 'Edit dashboard' }).click();
  await page.getByRole('button', { name: 'Add tile' }).click();
  await page.getByRole('heading', { name: 'Saved reports' }).waitFor();
  const row = page.getByRole('listitem').filter({ hasText: 'By country' });
  assert.match(await row.innerText(), /only you see it/);
  await row.getByRole('button', { name: 'Share and add' }).click();
  await page.getByRole('button', { name: 'Share and add' }).last().click();
  await page.getByTestId('report-tile').nth(1).waitFor();
  const patch = calls.findIndex(c => c.startsWith(`PATCH /v1/brands/${brand}/report-views/${mine}`) && c.includes('"shared":true'));
  const add = calls.findIndex(c => c.startsWith(`POST /v1/brands/${brand}/dashboards/${dash}/tiles`) && c.includes(mine));
  assert.ok(patch >= 0 && add > patch, 'the report is shared, then added');
  assert.deepEqual(errors, []);
  console.log('dashboard report tile: ok');
} catch (e) {
  console.error(output);
  throw e;
} finally {
  await browser?.close();
  server.kill();
}
