// Website analytics, Search, against mocked API answers: a page in the
// Pages table opens the searches that showed it, a search opens the pages
// it showed, each with its clicks, impressions, click-through and position,
// the period kept, the source named and a way back. Starts a local Vite
// server and mocks every API request; needs no backend.
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
const gsc = '20000000-0000-0000-0000-000000000001';
const site = 'sc-domain:northpeak.example';
const pricing = 'https://northpeak.example/pricing';
const home = 'https://northpeak.example/';
const m = (clicks, impressions, position) => ({ clicks, impressions, ctr: impressions ? clicks / impressions : 0, position });
// Search Console's page × query numbers for the period.
const cells = [
  [pricing, 'northpeak pricing', m(120, 900, 1.4)],
  [pricing, 'trail planner cost', m(30, 1400, 6.2)],
  [pricing, 'hiking app plans', m(8, 700, 11.8)],
  [home, 'northpeak', m(410, 1600, 1.1)],
  [home, 'trail planner cost', m(4, 380, 14.5)],
  ['https://northpeak.example/blog/best-trails', 'best trails near me', m(55, 5200, 8.9)],
];
const sum = (rows) => {
  const t = rows.reduce((a, [, , x]) => ({ clicks: a.clicks + x.clicks, impressions: a.impressions + x.impressions, pw: a.pw + x.position * x.impressions }), { clicks: 0, impressions: 0, pw: 0 });
  return { clicks: t.clicks, impressions: t.impressions, ctr: t.impressions ? t.clicks / t.impressions : 0, position: t.impressions ? t.pw / t.impressions : 0 };
};
const days = (from, to, total) => {
  const out = []; const n = Math.round((Date.parse(to) - Date.parse(from)) / 864e5) + 1;
  for (let i = 0; i < n; i++) {
    const w = 0.6 + 0.4 * Math.sin(i / 3) ** 2;
    out.push({ date: new Date(Date.parse(from) + i * 864e5).toISOString().slice(0, 10), clicks: Math.round(total.clicks / n * w * 1.2), impressions: Math.round(total.impressions / n * w * 1.2) });
  }
  return out;
};
const report = (name, q) => {
  const rows = cells.filter(([p, k]) => (!q.get('page') || p === q.get('page')) && (!q.get('keyword') || k === q.get('keyword')));
  const base = { report: name, site, from: q.get('from'), to: q.get('to'), integration_id: gsc, connector: 'google_search_console', cached: false, fetched_at: '2026-11-16T10:00:00Z',
    totals: sum(rows), notes: ['Search Console data is usually two to three days behind; the latest days may still change.'] };
  if (name === 'traffic') return { ...base, series: days(q.get('from'), q.get('to'), sum(rows)) };
  const at = name === 'pages' ? 0 : 1;
  const keys = [...new Set(rows.map(r => r[at]))];
  const out = keys.map(k => ({ key: k, ...sum(rows.filter(r => r[at] === k)) })).sort((a, b) => b.clicks - a.clicks);
  if (name === 'search_queries' && !q.get('keyword')) base.notes.push('Google hides rare queries for privacy, so the queries add up to less than the total.');
  return { ...base, rows: out };
};

let browser;
try {
  let ready = false;
  for (let n = 0; n < 100; n++) { try { ready = (await fetch(origin)).ok; if (ready) break; } catch {} await new Promise(r => setTimeout(r, 100)); }
  assert(ready, `Vite did not start: ${output}`);
  browser = await chromium.launch({ headless: true, args: ['--no-sandbox'], executablePath: process.env.OUTFLARE_TEST_CHROMIUM || undefined });
  const shot = async (page, name) => { if (process.env.OUTFLARE_TEST_SHOTS) await page.screenshot({ path: `${process.env.OUTFLARE_TEST_SHOTS}/${name}.png`, fullPage: true }); };
  const calls = [];
  const mock = (page) => page.route('**/v1/**', async route => {
    const req = route.request(); const u = new URL(req.url()); const q = u.searchParams; const p = u.pathname;
    calls.push(`${req.method()} ${p}${u.search}`);
    let status = 200, body = {};
    if (p === '/v1/workspaces/workspace-a/brands') body = { data: [{ id: brand, name: 'Northpeak', handle: 'northpeak', status: 'active' }] };
    else if (p === `/v1/brands/${brand}/analytics`) body = { data: [{ id: gsc, name: 'Google Search Console', connector: 'google_search_console', connector_name: 'Google Search Console', status: 'connected', last_error: null, account: { name: 'ana@northpeak.example', details: {} }, site, reports: ['traffic', 'pages', 'search_queries'] }] };
    else if (p === `/v1/brands/${brand}/seo`) body = { data: [] };
    else if (p === `/v1/brands/${brand}/site-keys`) body = { data: [] };
    else if (p === `/v1/workspaces/workspace-a/analytics/${gsc}/sites`) body = { data: [{ id: site, name: 'northpeak.example (whole domain)', url: 'northpeak.example', host: 'northpeak.example', domain: '', sync: true, conversion_events: [], filter_test_accounts: false }] };
    else if (p === '/v1/workspaces/workspace-a/analytics/site-brands') body = { data: [{ integration_id: gsc, site, brand_id: brand }] };
    else if (p.startsWith(`/v1/workspaces/workspace-a/analytics/${gsc}/reports/`)) body = report(p.split('/').at(-1), q);
    else { status = 404; body = { error: { code: 'not_found', message: 'nothing here', request_id: 'r' } }; }
    await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  });

  const page = await browser.newPage({ viewport: { width: 1280, height: 900 }, deviceScaleFactor: 2 });
  const errors = []; page.on('pageerror', e => errors.push(e.message));
  await mock(page);
  await page.goto(`${origin}/tests/fixtures/search-drilldown.html?item=search`);
  await page.getByLabel('Period').selectOption('90');
  await page.getByRole('tab', { name: 'Pages' }).last().click();
  const row = page.getByRole('row', { name: new RegExp(pricing) });
  await row.waitFor();
  await page.getByText('Click a page to see the searches that showed it.').waitFor();
  await shot(page, 'search-pages');
  // Both tables load together, so switching to the other shows it at once.
  assert(calls.some(c => c.includes('/reports/search_queries?') && !c.includes('page=') && !c.includes('keyword=')), 'search queries load with pages: ' + calls.join('\n'));

  // A page: the searches that showed it, over the same 90 days.
  await row.click();
  const gone = await page.evaluate(() => window.gone.at(-1));
  assert.deepEqual(gone, ['analytics', `search/page/${encodeURIComponent(pricing)}`]);
  await page.getByRole('heading', { name: pricing }).waitFor();
  await page.getByRole('heading', { name: 'Searches that showed this page' }).waitFor();
  await page.getByTestId('data-source').filter({ hasText: 'the searches that showed this page in the last 90 days' }).waitFor();
  await page.getByRole('row', { name: /trail planner cost/ }).waitFor();
  assert.equal(await page.getByRole('row', { name: /northpeak pricing/ }).count(), 1);
  assert.equal(await page.getByRole('row', { name: /best trails near me/ }).count(), 0, 'only this page\'s searches');
  for (const label of ['Search clicks', 'Impressions', 'Click-through', 'Avg. position']) await page.getByText(label).first().waitFor();
  assert(calls.some(c => c.includes('/reports/search_queries?') && c.includes(`page=${encodeURIComponent(pricing)}`) && c.includes(`site=${encodeURIComponent(site)}`)), calls.join('\n'));
  assert(calls.some(c => c.includes('/reports/traffic?') && c.includes(`page=${encodeURIComponent(pricing)}`)), calls.join('\n'));
  const from = calls.filter(c => c.includes('/reports/search_queries?') && c.includes('page=')).map(c => new URL('http://x' + c.split(' ')[1]).searchParams).at(-1);
  assert.equal((Date.parse(from.get('to')) - Date.parse(from.get('from'))) / 864e5, 89, 'the period is kept');
  await shot(page, 'search-page-drill');

  // A search: the pages it showed, and back to the page from there.
  await page.getByRole('row', { name: /trail planner cost/ }).click();
  await page.getByRole('heading', { name: '“trail planner cost”' }).waitFor();
  await page.getByRole('heading', { name: 'Pages this search showed' }).waitFor();
  await page.getByRole('row', { name: new RegExp('^' + home + ' ') }).waitFor();
  assert.equal(await page.getByRole('row', { name: /best-trails/ }).count(), 0, 'only the pages this search showed');
  assert(calls.some(c => c.includes('/reports/pages?') && c.includes('keyword=trail+planner+cost')), calls.join('\n'));
  await shot(page, 'search-query-drill');

  // Back to Search, with the Pages table still picked.
  await page.getByRole('button', { name: '← Search' }).click();
  await page.getByRole('row', { name: new RegExp(pricing) }).waitFor();
  assert.equal(await page.getByRole('tab', { name: 'Pages' }).last().getAttribute('aria-selected'), 'true');
  assert.equal(await page.getByLabel('Period').inputValue(), '90');

  // A search query from the Search queries table, then a phone.
  await page.getByRole('tab', { name: 'Search queries' }).click();
  await page.getByRole('row', { name: /^northpeak pricing/ }).click();
  await page.getByRole('heading', { name: 'Pages this search showed' }).waitFor();
  await page.setViewportSize({ width: 390, height: 844 });
  await shot(page, 'search-query-drill-phone');
  assert.deepEqual(errors, []);
  console.log('search drill-down: ok');
} finally {
  await browser?.close();
  server.kill();
}
