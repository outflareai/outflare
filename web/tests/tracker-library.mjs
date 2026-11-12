// The tracker's Offers, Landers and Flows pages (plans/ad-tracker-ui.md B3)
// against mocked API answers: an offer's token picker, missing click id
// warning, Preview with test values, payout by country, cap and fallback;
// a new lander showing its click URLs and script; and a flow edited with
// the campaign editor's paths, with the "used by N links" warning before a
// save. Starts a local Vite server and mocks every API request; needs no
// backend.
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
const offerA = '50000000-0000-0000-0000-00000000000a', offerB = '50000000-0000-0000-0000-00000000000b';
const lander = '60000000-0000-0000-0000-000000000001';
const flow = '70000000-0000-0000-0000-000000000001';
const now = '2026-11-12T00:00:00Z';
const offer = (id, name, extra = {}) => ({ id, brand_id: brand, name, url: `https://offers.example/${name.slice(-1).toLowerCase()}?sub1={click_id}`, source_id: net, payout_mode: 'postback', payout: '2.50', currency: 'USD',
  country_payouts: {}, conversion_type: '', daily_cap: null, cap_kind: 'conversions', fallback_offer_id: null, status: 'active', missing_click_id: false, tracking_url: `https://offers.example/${name.slice(-1).toLowerCase()}?sub1=CLICK`,
  clicks_today: 120, conversions_today: 4, cap_reached: false, destinations: 2, created_at: now, updated_at: now, ...extra });
const offers = [offer(offerA, 'Offer A'), offer(offerB, 'Offer B', { cap_reached: true, daily_cap: 10, conversions_today: 10 })];
const landerRow = (extra = {}) => ({ id: lander, brand_id: brand, name: 'Quiz lander', url: 'https://acme.example/quiz', ctas: 2, status: 'active', click_url: 'https://go.acme.example/click',
  cta_urls: ['https://go.acme.example/click/1', 'https://go.acme.example/click/2'], script: '<script src="https://go.acme.example/lander.js" async></script>', lander_clicks_today: 30, destinations: 1, needs_domain: false, created_at: now, updated_at: now, ...extra });
const dest = (id, name, url, extra = {}) => ({ id, name, url, weight: 100, daily_click_cap: null, clicks_today: 0, open: true, offer_id: null, offer_name: null, lander_id: null, lander_name: null, ...extra });
let flowRow = { id: flow, brand_id: brand, name: 'Sweeps flow', status: 'active', links: 3, needs_domain: false, created_at: now, updated_at: now,
  paths: [{ id: 'p1', name: 'Mobile', active: true, conditions: [{ field: 'device', op: 'is', values: ['mobile'] }], destinations: [dest('pd1', 'Quiz lander', 'https://acme.example/quiz', { lander_id: lander }), dest('pd2', 'Offer A', 'https://offers.example/a', { offer_id: offerA })] }],
  destinations: [dest('d1', 'Offer A', 'https://offers.example/a', { offer_id: offerA }), dest('d2', 'Offer B', 'https://offers.example/b', { offer_id: offerB })] };

let browser;
try {
  for (let i = 0; i < 100 && !output.includes('ready in'); i++) await new Promise(r => setTimeout(r, 200));
  browser = await chromium.launch({ headless: true, args: ['--no-sandbox'], executablePath: process.env.OUTFLARE_TEST_CHROMIUM || undefined });
  const calls = [];
  const bodies = {};
  const mock = (page) => page.route('**/v1/**', async route => {
    const req = route.request(); const u = new URL(req.url()); const p = u.pathname; const method = req.method();
    calls.push(`${method} ${p}`);
    const b = req.postData() ? JSON.parse(req.postData()) : null;
    if (b) bodies[`${method} ${p}`] = b;
    const B = `/v1/brands/${brand}`;
    let status = 200, body = {};
    if (p === '/v1/workspaces/workspace-a/brands') body = { data: [{ id: brand, name: 'Acme', handle: 'acme', status: 'active', timezone: 'Europe/Berlin' }] };
    else if (p === `${B}/offers` && method === 'POST') { status = 201; body = offer('50000000-0000-0000-0000-00000000000c', b.name); }
    else if (p === `${B}/offers`) body = { data: offers };
    else if (p.startsWith(`${B}/offers/`)) body = offers[0];
    else if (p === `${B}/postback-sources`) body = { data: [{ id: net, name: 'MaxBounty', network: 'maxbounty', currency: 'USD', status: 'active' }] };
    else if (p === `${B}/landers` && method === 'POST') { status = 201; body = landerRow({ name: b.name, url: b.url, ctas: b.ctas }); }
    else if (p === `${B}/landers`) body = { data: calls.includes(`POST ${B}/landers`) ? [landerRow()] : [] };
    else if (p === `${B}/flows` && method === 'POST') { status = 201; body = { ...flowRow, id: '70000000-0000-0000-0000-000000000002', name: b.name, links: 0, paths: [], destinations: [] }; }
    else if (p === `${B}/flows`) body = { data: [flowRow] };
    else if (p === `${B}/flows/${flow}` && method === 'PATCH') { flowRow = { ...flowRow, ...b }; body = flowRow; }
    else if (p === `${B}/flows/${flow}`) body = flowRow;
    else if (p === `${B}/flows/${flow}/destinations` || p === `${B}/flows/${flow}/paths`) body = flowRow;
    else if (p === `${B}/flows/${flow}/weight-suggestions`) body = { link_id: null, flow_id: flow, metric: 'revenue', days: 14, since: now, currency: 'USD', revenue_incomplete: false, min_clicks: 100, min_conversions: 10,
      lists: [{ path_id: null, path_name: 'Default', kind: 'destinations', ready: false, reason: 'not_enough_conversions', items: [] }] };
    else { status = 404; body = { error: { code: 'not_found', message: 'nothing here', request_id: 'r' } }; }
    await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  });

  // Offers.
  {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    await mock(page);
    await page.goto(`${origin}/tests/fixtures/tracker-library.html?page=offers`);
    await page.getByRole('heading', { name: 'Offers', exact: true }).waitFor();
    const rowB = page.getByRole('row', { name: /Offer B/ });
    assert.match(await rowB.innerText(), /Capped/);
    assert.match(await rowB.innerText(), /MaxBounty/);
    // New: the token picker inserts at the cursor; without {click_id} and a
    // network, the warning shows.
    await page.getByRole('button', { name: 'New offer' }).click();
    await page.getByLabel('Name').fill('Offer C');
    await page.getByLabel('Offer URL').fill('https://offers.example/c?sub1=');
    await page.getByLabel('Affiliate network').selectOption('');
    await page.getByText('Missing click id').waitFor();
    await page.getByRole('button', { name: 'Click id', exact: true }).click();
    assert.equal(await page.getByLabel('Offer URL').inputValue(), 'https://offers.example/c?sub1={click_id}');
    assert.equal(await page.getByText('Missing click id').count(), 0);
    await page.getByRole('button', { name: 'Country', exact: true }).click();
    await page.getByRole('button', { name: /Preview with test values/ }).click();
    assert.deepEqual(await page.evaluate(() => window.opened), ['https://offers.example/c?sub1=test-click-idUS']);
    await page.getByLabel('Affiliate network').selectOption(net);
    await page.getByLabel('Fixed').check();
    await page.getByLabel('Payout', { exact: true }).fill('3');
    await page.getByRole('button', { name: '+ Payout by country' }).click();
    await page.getByLabel('Country', { exact: true }).fill('de');
    await page.getByLabel('Country payout').fill('4.5');
    await page.getByLabel('Daily cap').fill('50');
    await page.getByLabel('Fallback offer').selectOption(offerA);
    await page.getByRole('button', { name: 'Add offer' }).click();
    await page.getByText('Offer added').waitFor();
    const posted = bodies[`POST /v1/brands/${brand}/offers`];
    assert.deepEqual(posted, { name: 'Offer C', url: 'https://offers.example/c?sub1={click_id}{country}', source_id: net, payout_mode: 'fixed', payout: '3', currency: 'USD',
      country_payouts: { DE: '4.5' }, conversion_type: '', daily_cap: 50, cap_kind: 'conversions', fallback_offer_id: offerA });
    // A saved offer shows its tracking URL; Archive asks first.
    await page.getByRole('row', { name: /Offer A/ }).click();
    await page.getByText('Tracking URL').waitFor();
    await page.getByRole('button', { name: 'Archive' }).click();
    await page.getByRole('alertdialog').getByRole('button', { name: 'Archive' }).click();
    await page.getByText('Offer archived').waitFor();
    assert.deepEqual(bodies[`PATCH /v1/brands/${brand}/offers/${offerA}`], { status: 'archived' });
    assert.deepEqual(errors, []);
    await page.close();
  }

  // Landers: a new one stays open with its click URLs and script.
  {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    await mock(page);
    await page.goto(`${origin}/tests/fixtures/tracker-library.html?page=landers`);
    await page.getByText('No landers yet').waitFor();
    await page.getByRole('button', { name: 'New lander' }).click();
    await page.getByLabel('Name').fill('Quiz lander');
    await page.getByLabel('Lander URL').fill('https://acme.example/quiz');
    await page.getByLabel('Calls to action').fill('2');
    await page.getByRole('button', { name: 'Add lander' }).click();
    const urls = page.getByTestId('click-urls');
    await urls.waitFor();
    assert.deepEqual(bodies[`POST /v1/brands/${brand}/landers`], { name: 'Quiz lander', url: 'https://acme.example/quiz', ctas: 2 });
    assert.match(await urls.innerText(), /Call to action 2/);
    assert.match(await urls.innerText(), /click\/2/);
    await page.getByRole('button', { name: 'Copy script' }).waitFor();
    assert.equal(calls.filter(c => c === `GET /v1/brands/${brand}/landers`).length, 2, 'the list reloads behind the sheet');
    assert.deepEqual(errors, []);
    await page.close();
  }

  // Flows: the list, a new one, and a flow's paths with the warning.
  {
    const page = await browser.newPage({ viewport: { width: 1280, height: 1000 } });
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    await mock(page);
    await page.goto(`${origin}/tests/fixtures/tracker-library.html?page=flows`);
    const row = page.getByRole('row', { name: /Sweeps flow/ });
    await row.waitFor();
    assert.match(await row.innerText(), /Mobile/);
    assert.match(await row.innerText(), /3 links/);
    await page.getByRole('button', { name: 'New flow' }).click();
    await page.getByRole('alertdialog').getByLabel('Name').fill('Push - DE');
    await page.getByRole('alertdialog').getByRole('button', { name: 'Create' }).click();
    await page.waitForFunction(() => window.gone.length > 0);
    assert.deepEqual(await page.evaluate(() => window.gone[0]), ['tracker-flows', '70000000-0000-0000-0000-000000000002']);
    await page.close();
  }
  {
    const page = await browser.newPage({ viewport: { width: 1280, height: 1000 } });
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    await mock(page);
    await page.goto(`${origin}/tests/fixtures/tracker-library.html?page=flows&item=${flow}`);
    await page.getByText('Used by 3 links: a change here changes them all.').waitFor();
    await page.getByText('Device is mobile').waitFor();
    await page.getByText('Suggested weights for the offers and pages: needs more conversions.').waitFor();
    await page.getByRole('button', { name: 'Equalise' }).first().click();
    await page.getByRole('button', { name: 'Save destinations' }).click();
    // The warning first; Cancel saves nothing.
    const dialog = page.getByRole('alertdialog');
    await dialog.getByText('Change 3 links?').waitFor();
    await dialog.getByRole('button', { name: 'Cancel' }).click();
    assert.equal(calls.filter(c => c === `PUT /v1/brands/${brand}/flows/${flow}/destinations`).length, 0);
    await page.getByRole('button', { name: 'Save destinations' }).click();
    await dialog.getByRole('button', { name: 'Save for all' }).click();
    await page.getByText('Destinations saved').waitFor();
    assert.deepEqual(bodies[`PUT /v1/brands/${brand}/flows/${flow}/destinations`].destinations.map(d => [d.offer_id, d.weight]), [[offerA, 100], [offerB, 100]]);
    const paths = bodies[`PUT /v1/brands/${brand}/flows/${flow}/paths`].paths;
    assert.deepEqual(paths.map(x => [x.id, x.conditions[0].values[0], x.destinations.length]), [['p1', 'mobile', 2]]);
    // Rename.
    await page.getByLabel('Flow name').fill('Sweeps flow v2');
    await page.getByRole('button', { name: 'Rename' }).click();
    await page.getByText('Flow renamed').waitFor();
    assert.deepEqual(bodies[`PATCH /v1/brands/${brand}/flows/${flow}`], { name: 'Sweeps flow v2' });
    assert.deepEqual(errors, []);
    await page.close();
  }

  // Read-only, on a phone.
  {
    const page = await browser.newPage({ viewport: { width: 390, height: 844 } });
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    await mock(page);
    await page.goto(`${origin}/tests/fixtures/tracker-library.html?page=offers&readonly`);
    await page.getByRole('row', { name: /Offer A/ }).waitFor();
    assert.equal(await page.getByRole('button', { name: 'New offer' }).count(), 0);
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1), 'no sideways scroll on a phone');
    assert.deepEqual(errors, []);
    await page.close();
  }
  console.log('tracker library: ok');
} catch (e) {
  console.error(output);
  throw e;
} finally {
  await browser?.close();
  server.kill();
}
