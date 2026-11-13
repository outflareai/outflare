// The tracker's Logs page (plans/ad-tracker-ui.md B6) against mocked API
// answers: clicks with their campaign and offer by name, filters and the
// time window in the request, likely bots on request, Load more, a click's
// detail with its id to copy; lander clicks, conversions, postbacks in and
// out; and errors grouped by why with their fix. Starts a local Vite server
// and mocks every API request; needs no backend.
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
const camp = '30000000-0000-0000-0000-000000000001';
const offer = '50000000-0000-0000-0000-00000000000a';
const net = '20000000-0000-0000-0000-000000000001';
const src = '40000000-0000-0000-0000-000000000001';
const now = new Date();
const ago = (m) => new Date(now.getTime() - m * 60_000).toISOString();
const click = (key, extra = {}) => ({ click_key: key, campaign_id: camp, link_id: 'l', path_id: null, destination_id: null, lander_id: null, offer_id: offer, direct: false, country: 'US', region: '', city: '',
  isp: 'Acme Mobile', asn: 64500, device: 'mobile', os: 'Android', browser: 'Chrome', language: 'en', fields: { 1: 'zone7' }, source_click_id: 'net-1', cost: '0.02', likely_bot: false, fraud_reasons: [], landed_at: null, clicked_at: ago(5), ...extra });
const call = (outcome, reason, extra = {}) => ({ id: `c-${outcome}-${Math.random()}`, source_id: net, conversion_id: null, via: 'postback', method: 'GET', request: { cid: 'aaaaaaaaaaaaaaa1', payout: '2.5' }, click_key: 'aaaaaaaaaaaaaaa1',
  status: 200, outcome, reason, duration_ms: 4, request_id: 'r', created_at: ago(10), ...extra });

let browser;
try {
  for (let i = 0; i < 100 && !output.includes('ready in'); i++) await new Promise(r => setTimeout(r, 200));
  browser = await chromium.launch({ headless: true, args: ['--no-sandbox'], executablePath: process.env.OUTFLARE_TEST_CHROMIUM || undefined });
  const calls = [];
  const mock = (page) => page.route('**/v1/**', async route => {
    const req = route.request(); const u = new URL(req.url()); const p = u.pathname; const q = u.searchParams;
    calls.push(`${req.method()} ${p}${u.search}`);
    const B = `/v1/brands/${brand}`;
    let status = 200, body = {};
    if (p === '/v1/workspaces/workspace-a/brands') body = { data: [{ id: brand, name: 'Acme', handle: 'acme', status: 'active', timezone: 'UTC' }] };
    else if (p === `${B}/tracking-campaigns`) body = { data: [{ id: camp, name: 'Push US', traffic_source_id: src, redirect_links: true }] };
    else if (p === `${B}/offers`) body = { data: [{ id: offer, name: 'Sweeps offer' }] };
    else if (p === `${B}/landers`) body = { data: [{ id: 'ld', name: 'Quiz lander' }] };
    else if (p === `${B}/postback-sources`) body = { data: [{ id: net, name: 'Everflow' }] };
    else if (p === `${B}/traffic-sources`) body = { data: [{ id: src, name: 'PropellerAds', postback: { url: 'https://ad.example/pb', method: 'GET' } }] };
    else if (p === `${B}/tracker-clicks`) {
      if (q.get('cursor')) body = { data: [click('aaaaaaaaaaaaaaa9', { clicked_at: ago(50) })], next_cursor: null };
      else body = { data: [click('aaaaaaaaaaaaaaa1'), ...(q.get('bots') === 'true' ? [click('aaaaaaaaaaaaaaa2', { likely_bot: true })] : []), click('aaaaaaaaaaaaaaa3', { fraud_reasons: ['datacenter_network'], country: 'DE' })], next_cursor: 'next-1' };
    }
    else if (p === `${B}/tracker-lander-clicks`) body = { data: [{ click_key: 'aaaaaaaaaaaaaaa1', lander_id: 'ld', cta: 2, destination_id: null, offer_id: offer, likely_bot: false, clicked_at: ago(4) }], next_cursor: null };
    else if (p === `${B}/tracker-conversions`) body = { data: [{ id: 'cv', source_id: net, click_key: 'aaaaaaaaaaaaaaa1', campaign_id: camp, link_id: null, offer_id: offer, fraud_reasons: [], clicked_at: ago(5), transaction_id: 't', type: 'sale', status: 'approved', payout: '2.5', currency: 'USD', params: {}, record_id: null, postbacks: 1, converted_at: ago(3), updated_at: ago(3) },
      { id: 'old', source_id: net, click_key: 'zz', campaign_id: camp, link_id: null, offer_id: offer, fraud_reasons: [], clicked_at: ago(9000), transaction_id: 't2', type: 'sale', status: 'approved', payout: '1', currency: 'USD', params: {}, record_id: null, postbacks: 1, converted_at: ago(9000), updated_at: ago(9000) }] };
    else if (p === `${B}/tracker-conversion-log`) body = { data: [call('accepted', ''), call('unknown_click', 'no click has this key'), call('unknown_click', 'no click has this key'), call('expired_click', 'the click is older than 30 days')], next_cursor: null };
    else if (p === `${B}/traffic-sources/${src}/postbacks`) body = { data: [{ id: 's1', conversion_id: 'cv', status: 'failed', payout: '2.00', url: 'https://ad.example/pb?x=1', http_status: 500, error: 'the network answered 500', attempts: 5, sent_at: null, created_at: ago(2) }] };
    else { status = 404; body = { error: { code: 'not_found', message: 'nothing here', request_id: 'r' } }; }
    await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  });
  const last = (re) => [...calls].reverse().find(c => re.test(c)) ?? '';

  const page = await browser.newPage({ viewport: { width: 1280, height: 1000 } });
  const errors = []; page.on('pageerror', e => errors.push(e.message));
  await mock(page);
  await page.goto(`${origin}/tests/fixtures/tracker-logs.html`);
  // Clicks: names for ids, the suspect badge, the window in the request.
  const table = page.getByTestId('log-table');
  await table.getByText('aaaaaaaaaaaaaaa1').waitFor();
  assert.match(await table.innerText(), /Push US/);
  assert.match(await table.innerText(), /Sweeps offer/);
  assert.match(await table.innerText(), /Suspect/);
  assert.match(last(/tracker-clicks\?/), /since=/);
  assert.match(last(/tracker-clicks\?/), /bots=false/);
  await page.getByLabel('Time window').selectOption('15m');
  await page.getByLabel('Campaign').selectOption(camp);
  await page.getByLabel('Show likely bots').check();
  await table.getByText('aaaaaaaaaaaaaaa2').waitFor();
  assert.match(last(/tracker-clicks\?/), new RegExp(`campaign=${camp}`));
  assert.match(last(/tracker-clicks\?/), /bots=true/);
  const since = new Date(decodeURIComponent(/since=([^&]+)/.exec(last(/tracker-clicks\?/))[1]));
  assert.ok(now - since < 16 * 60_000 && now - since > 14 * 60_000, `since ${since}`);
  // Load more follows the cursor.
  await page.getByRole('button', { name: 'Load more' }).click();
  await table.getByText('aaaaaaaaaaaaaaa9').waitFor();
  assert.match(last(/tracker-clicks\?/), /cursor=next-1/);
  // A click's detail, with its id to copy.
  await table.getByText('aaaaaaaaaaaaaaa3').click();
  assert.equal(await page.getByTestId('detail-click').innerText(), 'aaaaaaaaaaaaaaa3');
  await page.getByRole('dialog').getByText('datacenter_network').waitFor();
  await page.keyboard.press('Escape');

  // The other tabs.
  await page.getByRole('tab', { name: 'Lander clicks' }).click();
  await table.getByText('CTA 2 → Sweeps offer').waitFor();
  await page.getByRole('tab', { name: 'Conversions' }).click();
  await table.getByText('2.5 USD').waitFor();
  assert.equal(await table.getByText('zz', { exact: true }).count(), 0, 'a conversion outside the window is left out');
  await page.getByRole('tab', { name: 'Postbacks in' }).click();
  await table.getByText('unknown_click').first().waitFor();
  assert.match(await table.innerText(), /Everflow/);
  await page.getByRole('tab', { name: 'Postbacks out' }).click();
  await table.getByText('the network answered 500').waitFor();
  assert.match(await table.innerText(), /5 tries/);
  // Errors: grouped by why, most first, each with its fix.
  await page.getByRole('tab', { name: 'Errors' }).click();
  const groups = page.getByTestId('error-groups');
  await groups.waitFor();
  const text = await groups.innerText();
  assert.ok(text.indexOf('Unknown click id · 2') < text.indexOf('Click expired · 1'), text);
  assert.doesNotMatch(text, /accepted/);
  await groups.getByRole('button', { name: 'Fix it' }).first().click();
  assert.deepEqual(await page.evaluate(() => window.gone[0]), ['tracker-offers', null]);
  // Live refreshes until paused.
  const before = calls.filter(c => c.includes('tracker-conversion-log')).length;
  await page.getByRole('button', { name: 'Live' }).click();
  await page.waitForTimeout(5600);
  assert.ok(calls.filter(c => c.includes('tracker-conversion-log')).length > before, 'Live refreshes');
  await page.getByRole('button', { name: 'Live · Pause' }).click();
  assert.deepEqual(errors, []);
  await page.close();

  // On a phone: no sideways scroll on the page itself.
  const phone = await browser.newPage({ viewport: { width: 390, height: 844 } });
  await mock(phone);
  await phone.goto(`${origin}/tests/fixtures/tracker-logs.html`);
  await phone.getByTestId('log-table').waitFor();
  assert.ok(await phone.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1), 'no sideways scroll on a phone');
  console.log('tracker logs: ok');
} catch (e) {
  console.error(output);
  throw e;
} finally {
  await browser?.close();
  server.kill();
}
