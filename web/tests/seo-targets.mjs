// The SEO target report against mocked API answers: page and keyword are
// columns of their own, each sorts, and the row under the headers filters
// by page, keyword, status, intent, page type and band. Starts a local Vite
// server and mocks every API request; needs no backend. Set
// OUTFLARE_TEST_SHOTS to a folder to save a screenshot.
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
const site = '20000000-0000-0000-0000-000000000001';
const w = (position, clicks = 0, impressions = 0) => ({ clicks, impressions, position });
const kw = (keyword, position, clicks, impressions) => ({ keyword, current: w(position, clicks, impressions), previous: w(position === null ? null : position + 1.5, clicks, impressions), week: w(position, 0, 0), last_week: w(position, 0, 0), weekly: Array.from({ length: 12 }, (_, i) => position === null ? null : position + (11 - i) * 0.3) });
let n = 0;
const target = (path, keyword, position, impressions, { status = 'on_target', intent = '', page_type = '', band = 'top10', secondary = [] } = {}) => ({
  id: `30000000-0000-0000-0000-00000000000${++n}`, site_id: site, host: 'northpeak.example', url: `https://northpeak.example${path}`,
  primary: kw(keyword, position, Math.round(impressions / 20), impressions), secondary: secondary.map(s => kw(s, 14, 3, 90)), tracked: [], competing: [],
  checks: { in_title: true, in_h1: true, in_description: false }, intent, page_type, summary_length: 120, status, owner: null, suggestions: [], band,
  trend: 'improved', updated_at: '2026-11-20T00:00:00Z',
});
const targets = [
  target('/pricing', 'trail gear pricing', 2.1, 5400, { intent: 'commercial', page_type: 'pricing', band: 'top3' }),
  target('/blog/how-to-pack', 'how to pack a backpack', 8.4, 12000, { intent: 'informational', page_type: 'blog', status: 'missing_content', secondary: ['backpack packing list'] }),
  target('/tents', 'buy tents online', 15.2, 3100, { intent: 'transactional', page_type: 'product', band: 'striking', status: 'cannibalized' }),
  target('/compare/summitly', 'northpeak vs summitly', 24, 800, { intent: 'commercial', page_type: 'comparison', band: 'beyond', status: 'wrong_intent' }),
  target('/about', 'about northpeak', null, 0, { band: 'none', status: 'no_data' }),
];
const count = (f) => targets.filter(f).length;
const report = {
  sites: [{ id: site, host: 'northpeak.example', source: null }], targets, notes: ['Positions are Search Console averages over 28 days.'],
  summary: { targets: targets.length, top3: count(t => t.band === 'top3'), top10: count(t => t.band === 'top10'), striking: count(t => t.band === 'striking'), beyond: count(t => t.band === 'beyond'), none: count(t => t.band === 'none'),
    improved: targets.length, declined: 0, competing: 0, missing_on_page: 0, untracked: targets.length, on_target: count(t => t.status === 'on_target'), cannibalized: count(t => t.status === 'cannibalized'),
    wrong_intent: count(t => t.status === 'wrong_intent'), missing_content: count(t => t.status === 'missing_content'), no_data: count(t => t.status === 'no_data'), no_summary: 0 },
};

let browser;
try {
  for (let i = 0; i < 100 && !output.includes('ready in'); i++) await new Promise(r => setTimeout(r, 200));
  browser = await chromium.launch({ headless: true, args: ['--no-sandbox'], executablePath: process.env.OUTFLARE_TEST_CHROMIUM || undefined });
  const page = await browser.newPage({ viewport: { width: 1600, height: 900 } });
  const errors = []; page.on('pageerror', e => errors.push(e.message));
  await page.route('**/v1/**', async route => {
    const p = new URL(route.request().url()).pathname;
    let status = 200, body;
    if (p === '/v1/workspaces/workspace-a/brands') body = { data: [{ id: brand, name: 'Northpeak', handle: 'northpeak', status: 'active', timezone: 'Europe/Berlin' }] };
    else if (p === `/v1/brands/${brand}/seo/targets`) body = report;
    else { status = 404; body = { error: { code: 'not_found', message: 'nothing here', request_id: 'r' } }; }
    await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  });
  await page.goto(`${origin}/tests/fixtures/seo-targets.html`);
  const table = page.locator('table').first();
  await table.getByRole('columnheader', { name: 'Page', exact: true }).waitFor();
  await table.getByRole('columnheader', { name: 'Keyword', exact: true }).waitFor();
  const firstCol = async (i) => (await table.locator('tbody > tr').evaluateAll((rows, i) => rows.map(r => r.children[i]?.textContent?.trim() ?? ''), i));

  // Default order is impressions, most first.
  assert.equal((await firstCol(1))[0], 'how to pack a backpack+1');

  // Sorting by keyword starts A to Z, a second click Z to A.
  await table.getByRole('button', { name: 'Keyword' }).click();
  assert.deepEqual(await firstCol(1), ['about northpeak', 'buy tents online', 'how to pack a backpack+1', 'northpeak vs summitly', 'trail gear pricing']);
  await table.getByRole('button', { name: /^Keyword/ }).click();
  assert.equal((await firstCol(1))[0], 'trail gear pricing');

  // Sorting by page goes by path.
  await table.getByRole('button', { name: 'Page', exact: true }).click();
  assert.deepEqual(await firstCol(0), ['/about', '/blog/how-to-pack', '/compare/summitly', '/pricing', '/tents']);

  // Column filters: page text, keyword text (secondary keywords too), and picks.
  await page.getByLabel('Filter by page', { exact: true }).fill('blog');
  assert.deepEqual(await firstCol(0), ['/blog/how-to-pack']);
  await page.getByLabel('Filter by page', { exact: true }).fill('');
  await page.getByLabel('Filter by keyword').fill('packing list');
  assert.deepEqual(await firstCol(0), ['/blog/how-to-pack']);
  await page.getByLabel('Filter by keyword').fill('northpeak');
  assert.deepEqual(await firstCol(0), ['/about', '/compare/summitly']);
  await page.getByLabel('Filter by intent').selectOption('commercial');
  assert.deepEqual(await firstCol(0), ['/compare/summitly']);
  await page.getByRole('button', { name: 'Clear 2 column filters' }).click();
  assert.equal((await firstCol(0)).length, 5);
  await page.getByLabel('Filter by status').selectOption('cannibalized');
  assert.deepEqual(await firstCol(0), ['/tents']);
  await page.getByLabel('Filter by status').selectOption('');
  await page.getByLabel('Filter by page type').selectOption('(not set)');
  assert.deepEqual(await firstCol(0), ['/about']);
  await page.getByLabel('Filter by page type').selectOption('');
  await page.getByLabel('Filter by band').selectOption('striking');
  assert.deepEqual(await firstCol(0), ['/tents']);
  await page.getByLabel('Filter by band').selectOption('');

  // Clicking a row still opens its details.
  await table.locator('tbody > tr').first().click();
  await page.getByText('Keywords on this page (Search Console, 28 days)').waitFor();
  await table.locator('tbody > tr').first().click();

  if (process.env.OUTFLARE_TEST_SHOTS) {
    await page.getByLabel('Filter by keyword').fill('northpeak');
    await page.screenshot({ path: `${process.env.OUTFLARE_TEST_SHOTS}/seo-targets-filtered.png`, fullPage: true });
    await page.getByLabel('Filter by keyword').fill('');
    await table.getByRole('button', { name: /^Page( [↑↓])?$/ }).click();
    await page.screenshot({ path: `${process.env.OUTFLARE_TEST_SHOTS}/seo-targets.png`, fullPage: true });
  }
  assert.deepEqual(errors, []);
  console.log('seo-targets: ok');
} catch (e) {
  console.error(output);
  throw e;
} finally {
  await browser?.close();
  server.kill();
}
