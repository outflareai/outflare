// The tracker's Setup page (plans/ad-tracker-ui.md B5) against mocked API
// answers: the checklist ticked from the brand's data with Skip remembered;
// the postback URL builder in the network's own tokens, its secret made by
// a rotation, its preview and the pixel, script and server call; and a test
// conversion whose outcome and reason show inline, refused and accepted.
// Starts a local Vite server and mocks every API request; needs no backend.
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
const net = '20000000-0000-0000-0000-000000000001';
const camp = '30000000-0000-0000-0000-000000000001';
const now = '2026-11-12T00:00:00Z';
const network = { id: net, brand_id: brand, name: 'Everflow', token_hint: 'ofp_ab', ip_allowlist: [], duplicate_policy: 'ignore', click_expiry_days: 30, currency: 'USD', status: 'active',
  network: 'everflow', click_id_param: 'sub1', postback_query: '', conversions: 0, created_at: now, updated_at: now, needs_domain: false };

let browser;
try {
  for (let i = 0; i < 100 && !output.includes('ready in'); i++) await new Promise(r => setTimeout(r, 200));
  browser = await chromium.launch({ headless: true, args: ['--no-sandbox'], executablePath: process.env.OUTFLARE_TEST_CHROMIUM || undefined });
  const bodies = [];
  let conversions = [];
  const filters = [];
  let lookups = { integration_id: null, enabled: false, flagged: true, converting: true, sample_percent: 5, monthly_cap: 5000, threshold: 75, updated_at: now, used_this_month: 0 };
  const mock = (page) => page.route('**/v1/**', async route => {
    const req = route.request(); const u = new URL(req.url()); const p = u.pathname; const method = req.method();
    const b = req.postData() ? JSON.parse(req.postData()) : null;
    if (b) bodies.push([`${method} ${p}`, b]);
    const B = `/v1/brands/${brand}`;
    let status = 200, body = {};
    if (p === '/v1/workspaces/workspace-a/brands') body = { data: [{ id: brand, name: 'Acme', handle: 'acme', status: 'active', timezone: 'Europe/Berlin' }] };
    else if (p === `${B}/tracking-domains`) body = { data: [{ id: 'td', domain: 'go.acme.example', status: 'verified' }] };
    else if (p === `${B}/traffic-sources`) body = { data: [{ id: 's' }] };
    else if (p === `${B}/offers`) body = { data: [{ id: 'o', missing_click_id: true }] };
    else if (p === `${B}/tracking-campaigns`) body = { data: [{ id: camp, name: 'Propeller - US', traffic_source_id: 's', redirect_links: true, clicks: 3 }] };
    else if (p === `${B}/postback-sources`) body = { data: [network] };
    else if (p === `${B}/affiliate-network-templates`) body = { data: [{ id: 'everflow', name: 'Everflow', kind: 'platform', click_id_param: 'sub1', macros: { cid: '{sub1}', payout: '{payout_amount}', txid: '{conversion_id}' } }] };
    else if (p === `${B}/postback-sources/${net}/rotate`) body = { ...network, postback_url: 'https://go.acme.example/t/ofp_ab99xyz?cid={sub1}' };
    else if (p === `${B}/tracker-conversions` && method === 'POST') {
      const row = b.conversions[0];
      if (row.click_id === 'old-click') body = { accepted: 0, updated: 0, duplicate: 0, failed: 1, rows: [{ row: 1, outcome: 'failed', error: 'the click expired: it is older than the network\'s 30 days' }] };
      else {
        conversions = [{ id: 'c1', source_id: net, click_key: row.click_id, campaign_id: camp, link_id: null, offer_id: null, fraud_reasons: [], clicked_at: now, transaction_id: row.transaction_id, type: 'test', status: 'declined', payout: '0', currency: 'USD', params: {}, record_id: null, postbacks: 1, converted_at: now, updated_at: now }];
        body = { accepted: 1, updated: 0, duplicate: 0, failed: 0, rows: [{ row: 1, outcome: 'accepted', conversion_id: 'c1' }] };
      }
    }
    else if (p === `${B}/tracker-conversions`) body = { data: conversions };
    else if (p === `${B}/tracker-clicks`) body = { data: [{ click_key: 'k7m2pq9x00000000' }], next_cursor: null };
    else if (p === `${B}/click-filters` && method === 'POST') { filters.push({ id: 'f' + filters.length, kind: b.kind, value: b.value, note: b.note, created_at: now }); status = 201; body = filters.at(-1); }
    else if (p === `${B}/click-filters`) body = { data: filters };
    else if (p === `${B}/ip-lookups` && method === 'PUT') { lookups = { ...lookups, ...b, used_this_month: 1234 }; body = lookups; }
    else if (p === `${B}/ip-lookups`) body = lookups;
    else if (p === '/v1/workspaces/workspace-a/integrations') body = { data: [{ id: 'iq', connector: 'ipqualityscore', name: 'IPQualityScore', capabilities: ['account.test', 'ip.score'], status: 'connected' }] };
    else { status = 404; body = { error: { code: 'not_found', message: 'nothing here', request_id: 'r' } }; }
    await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  });

  const page = await browser.newPage({ viewport: { width: 1280, height: 1100 } });
  const errors = []; page.on('pageerror', e => errors.push(e.message));
  await mock(page);
  await page.goto(`${origin}/tests/fixtures/tracker-setup.html`);
  // The checklist: domain, source, campaign and click are done; the offer
  // still lacks a click id; Skip is remembered.
  const list = page.getByTestId('checklist');
  await list.waitFor();
  await page.getByText('Setup 4/6').waitFor();
  assert.equal(await list.getByText('(done)').count(), 4);
  await page.getByRole('button', { name: 'Skip Add an offer that carries the click id' }).click();
  await page.getByText('Setup 5/6').waitFor();
  await page.reload();
  await page.getByText('Setup 5/6').waitFor();

  // The builder: the network's own tokens, then a secret from a rotation.
  const preview = page.getByTestId('postback-preview');
  assert.equal(await preview.innerText(), 'https://go.acme.example/t/<secret>?cid={sub1}&payout={payout_amount}&txid={conversion_id}&status={status}');
  await page.getByLabel('Send Currency').check();
  assert.match(await preview.innerText(), /&currency=\{currency\}&txid=/);
  await page.getByRole('button', { name: '+ Parameters p1 to p20' }).click();
  await page.getByLabel('Parameter 1 token').fill('{sub2}');
  assert.match(await preview.innerText(), /&p1=\{sub2\}$/);
  await page.getByRole('button', { name: 'Make a new one' }).click();
  await page.getByRole('alertdialog').getByRole('button', { name: 'Make a new URL' }).click();
  await page.getByText('New postback URL made').waitFor();
  assert.match(await preview.innerText(), /^https:\/\/go\.acme\.example\/t\/ofp_ab99xyz\?cid=/);
  assert.equal(await page.getByLabel('Secret').getAttribute('type'), 'password');
  assert.match(await page.getByTestId('snippet-pixel').innerText(), /^<img src="https:\/\/go\.acme\.example\/t\/ofp_ab99xyz\/pixel\?cid=\{sub1\}/);
  await page.getByRole('tab', { name: 'Server call' }).click();
  assert.match(await page.getByTestId('snippet-server').innerText(), /"p1":"\{sub2\}"/);

  // A test conversion: refused with its reason, then accepted on the click.
  await page.getByLabel('Click id', { exact: true }).fill('old-click');
  await page.getByRole('button', { name: 'Fire test conversion' }).click();
  await page.getByText('the click expired').waitFor();
  assert.match(await page.getByTestId('test-result').innerText(), /failed/);
  await page.getByRole('button', { name: 'Use my latest click' }).click();
  await page.waitForFunction(() => document.querySelector('[aria-label="Click id"]').value === 'k7m2pq9x00000000');
  await page.getByRole('button', { name: 'Fire test conversion' }).click();
  await page.getByTestId('test-result').getByText('Propeller - US').waitFor();
  const fired = bodies.filter(([k]) => k === `POST /v1/brands/${brand}/tracker-conversions`).at(-1)[1];
  assert.equal(fired.source, net);
  assert.deepEqual({ ...fired.conversions[0], transaction_id: undefined }, { click_id: 'k7m2pq9x00000000', transaction_id: undefined, payout: 0, type: 'test', status: 'declined' });
  // A test conversion ticks the last step but doesn't close the checklist.
  await page.getByText('Setup 6/6').waitFor();
  // A click filter: a user agent piece, then shown in the list.
  await page.getByLabel('Filter kind').selectOption('user_agent');
  await page.getByLabel('Filter value').fill('HeadlessChrome');
  await page.getByLabel('Filter note').fill('Scrapers');
  await page.getByRole('button', { name: 'Add filter' }).click();
  await page.getByTestId('click-filters').getByText('HeadlessChrome').waitFor();
  assert.deepEqual(bodies.filter(([k]) => k === `POST /v1/brands/${brand}/click-filters`).at(-1)[1], { kind: 'user_agent', value: 'HeadlessChrome', note: 'Scrapers' });
  // IP lookups: turned on with the one IPQualityScore connection, then a
  // lower cap saved; the card shows this month's use against it.
  const lookupCard = page.getByTestId('ip-lookups');
  await lookupCard.getByText('0 of 5,000 lookups used this month').waitFor();
  await lookupCard.getByRole('button', { name: 'Turn on' }).click();
  await lookupCard.getByText('On', { exact: true }).waitFor();
  assert.equal(bodies.filter(([k]) => k === `PUT /v1/brands/${brand}/ip-lookups`).at(-1)[1].integration_id, 'iq');
  await page.getByLabel('Monthly cap').fill('2000');
  await lookupCard.getByRole('button', { name: 'Save' }).click();
  await lookupCard.getByText('1,234 of 2,000 lookups used this month').waitFor();
  assert.equal(bodies.filter(([k]) => k === `PUT /v1/brands/${brand}/ip-lookups`).at(-1)[1].monthly_cap, 2000);
  assert.deepEqual(errors, []);
  await page.close();

  // On a phone: no sideways scroll.
  const phone = await browser.newPage({ viewport: { width: 390, height: 844 } });
  const perrors = []; phone.on('pageerror', e => perrors.push(e.message));
  await mock(phone);
  await phone.goto(`${origin}/tests/fixtures/tracker-setup.html?readonly`);
  await phone.getByTestId('postback-preview').waitFor();
  assert.equal(await phone.getByRole('button', { name: 'Fire test conversion' }).count(), 0);
  assert.ok(await phone.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1), 'no sideways scroll on a phone');
  assert.deepEqual(perrors, []);
  console.log('tracker setup: ok');
} catch (e) {
  console.error(output);
  throw e;
} finally {
  await browser?.close();
  server.kill();
}
