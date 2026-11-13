// The tracker's Traffic sources and Affiliate networks pages
// (plans/ad-tracker-ui.md B4) against mocked API answers: the template
// gallery (search, category chips, Custom first, Connected), a source made
// from a template whose postback needs the brand's own value (REPLACE blocks
// the save), the parameters table and the live campaign URL preview, the
// send rules and recent postbacks; and an affiliate network whose postback
// URL shows once after it is made and again after a rotation. Starts a local
// Vite server and mocks every API request; needs no backend.
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
const src = '40000000-0000-0000-0000-000000000001';
const net = '20000000-0000-0000-0000-000000000001';
const now = '2026-11-12T00:00:00Z';
const templates = [
  { id: 'google', name: 'Google Ads', category: 'search_social', connector: 'google_ads', currency: 'USD', click_id: { param: 'gclid', token: '' }, cost: { param: '', token: '' }, parameters: [{ name: 'Campaign', param: 'campaignid', token: '{campaignid}', field: 1 }], postback: null },
  { id: 'propellerads', name: 'PropellerAds', category: 'push_pop', currency: 'USD', click_id: { param: 'clickid', token: '${SUBID}' }, cost: { param: 'cost', token: '${cost}' },
    parameters: [{ name: 'Zone', param: 'zone', token: '${zoneid}', field: 1 }], postback: { url: 'http://ad.propellerads.example/conversion.php?aid=REPLACE&visitor_id={source_click_id}&payout={payout}', method: 'GET' }, note: 'Find your aid in the Tracking section.' },
  { id: 'taboola', name: 'Taboola', category: 'native', currency: 'USD', click_id: { param: 'click_id', token: '{click_id}' }, cost: { param: '', token: '' }, parameters: [], postback: null },
];
const rules = { share: 100, decimals: 2, min_payout: '0', types: [], statuses: ['approved'], resend: false };
const source = { id: src, brand_id: brand, name: 'Zeropark', template: '', currency: 'USD', click_id: { param: 'cid', token: '{cid}' }, cost: { param: '', token: '' }, parameters: [],
  postback: { url: 'https://postback.zeropark.example/?cid={source_click_id}', method: 'GET' }, postback_rules: rules, status: 'active', campaigns: 2, created_at: now, updated_at: now };
const network = (extra = {}) => ({ id: net, brand_id: brand, name: 'Everflow', token_hint: 'ofp_ab', ip_allowlist: [], duplicate_policy: 'ignore', click_expiry_days: 30, currency: 'USD', status: 'active',
  network: 'everflow', click_id_param: 'sub1', postback_query: 'cid={sub1}&payout={payout}', conversions: 12, created_at: now, updated_at: now, needs_domain: false, ...extra });

let browser;
try {
  for (let i = 0; i < 100 && !output.includes('ready in'); i++) await new Promise(r => setTimeout(r, 200));
  browser = await chromium.launch({ headless: true, args: ['--no-sandbox'], executablePath: process.env.OUTFLARE_TEST_CHROMIUM || undefined });
  const calls = [];
  const bodies = {};
  let networks = [];
  const mock = (page) => page.route('**/v1/**', async route => {
    const req = route.request(); const u = new URL(req.url()); const p = u.pathname; const method = req.method();
    calls.push(`${method} ${p}`);
    const b = req.postData() ? JSON.parse(req.postData()) : null;
    if (b) bodies[`${method} ${p}`] = b;
    const B = `/v1/brands/${brand}`;
    let status = 200, body = {};
    if (p === '/v1/workspaces/workspace-a/brands') body = { data: [{ id: brand, name: 'Acme', handle: 'acme', status: 'active', timezone: 'Europe/Berlin' }] };
    else if (p === '/v1/workspaces/workspace-a/integrations') body = { data: [{ id: 'i1', connector: 'google_ads', status: 'connected' }] };
    else if (p === `${B}/traffic-source-templates`) body = { data: templates };
    else if (p === `${B}/traffic-sources` && method === 'POST') { status = 201; body = { ...source, id: '40000000-0000-0000-0000-000000000002', name: b.name, template: b.template }; }
    else if (p === `${B}/traffic-sources`) body = { data: [source] };
    else if (p === `${B}/traffic-sources/${src}/postbacks`) body = { data: [{ id: 's1', conversion_id: 'c1', status: 'sent', payout: '1.20', url: 'https://postback.zeropark.example/?cid=abc', http_status: 200, error: '', attempts: 1, sent_at: now, created_at: now },
      { id: 's2', conversion_id: 'c2', status: 'failed', payout: '0.80', url: 'https://postback.zeropark.example/?cid=def', http_status: 500, error: 'the network answered 500', attempts: 5, sent_at: null, created_at: now }] };
    else if (p === `${B}/traffic-sources/${src}`) body = { ...source, ...b };
    else if (p === `${B}/affiliate-network-templates`) body = { data: [{ id: 'everflow', name: 'Everflow', kind: 'platform', click_id_param: 'sub1', macros: { cid: '{sub1}' } }, { id: 'maxbounty', name: 'MaxBounty', kind: 'network', click_id_param: 's2', macros: {} }] };
    else if (p === `${B}/postback-sources` && method === 'POST') { status = 201; networks = [network({ name: b.name })]; body = network({ name: b.name, postback_url: 'https://go.acme.example/postback/ofp_ab12?cid={sub1}&payout={payout}' }); }
    else if (p === `${B}/postback-sources`) body = { data: networks };
    else if (p === `${B}/postback-sources/${net}/rotate`) body = network({ token_hint: 'ofp_cd', postback_url: 'https://go.acme.example/postback/ofp_cd34?cid={sub1}&payout={payout}' });
    else { status = 404; body = { error: { code: 'not_found', message: 'nothing here', request_id: 'r' } }; }
    await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  });

  // Traffic sources.
  {
    const page = await browser.newPage({ viewport: { width: 1280, height: 1000 } });
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    await mock(page);
    await page.goto(`${origin}/tests/fixtures/tracker-sources.html?page=sources`);
    await page.getByRole('row', { name: /Zeropark/ }).waitFor();
    // The gallery: Custom first, Connected on the network Outflare syncs,
    // category chips and search.
    await page.getByRole('button', { name: 'New traffic source' }).click();
    const dialog = page.getByRole('dialog');
    const tiles = await dialog.locator('.grid > button').allInnerTexts();
    assert.match(tiles[0], /Custom/, 'Custom comes first');
    assert.match(await dialog.getByRole('button', { name: /Google Ads/ }).innerText(), /Connected/);
    await dialog.getByRole('button', { name: 'Native', exact: true }).click();
    assert.equal(await dialog.getByRole('button', { name: /PropellerAds/ }).count(), 0);
    await dialog.getByRole('button', { name: 'All', exact: true }).click();
    await dialog.getByLabel('Search').fill('prop');
    assert.equal(await dialog.getByRole('button', { name: /Taboola/ }).count(), 0);
    await dialog.getByRole('button', { name: /PropellerAds/ }).click();
    // Prefilled from the template, with the live URL preview.
    await page.getByText('Find your aid in the Tracking section.').waitFor();
    assert.equal(await page.getByLabel('Click id parameter').inputValue(), 'clickid');
    assert.match(await page.getByTestId('url-preview').innerText(), /\?clickid=\$\{SUBID\}&cost=\$\{cost\}&zone=\$\{zoneid\}$/);
    // REPLACE blocks the save until it is filled.
    await page.getByText('Fill in REPLACE in the postback URL').waitFor();
    assert.ok(await page.getByRole('button', { name: 'Add traffic source' }).isDisabled());
    const pb = page.getByLabel('Postback URL');
    await pb.fill((await pb.inputValue()).replace('REPLACE', '4242'));
    assert.ok(await page.getByRole('button', { name: 'Add traffic source' }).isEnabled());
    // A parameter: the next free field, and the preview follows.
    await page.getByRole('button', { name: '+ Add parameter' }).click();
    await page.getByLabel('Parameter name').nth(1).fill('Campaign');
    await page.getByLabel('Parameter', { exact: true }).nth(1).fill('camp');
    await page.getByLabel('Token', { exact: true }).nth(1).fill('${campaignid}');
    assert.match(await page.getByTestId('url-preview').innerText(), /&camp=\$\{campaignid\}$/);
    await page.getByLabel('Payout share').fill('80');
    await page.getByLabel('pending').check();
    await page.getByRole('button', { name: 'Add traffic source' }).click();
    await page.getByText('Traffic source added').waitFor();
    const posted = bodies[`POST /v1/brands/${brand}/traffic-sources`];
    assert.equal(posted.template, 'propellerads');
    assert.deepEqual(posted.parameters, [{ name: 'Zone', param: 'zone', token: '${zoneid}', field: 1 }, { name: 'Campaign', param: 'camp', token: '${campaignid}', field: 2 }]);
    assert.match(posted.postback.url, /aid=4242&/);
    assert.deepEqual([posted.postback_rules.share, posted.postback_rules.statuses], [80, ['approved', 'pending']]);
    // An existing source: its recent postbacks, with the failure's reason.
    await page.getByRole('row', { name: /Zeropark/ }).click();
    await page.getByText('Recent postbacks').waitFor();
    await page.getByText('the network answered 500').waitFor();
    assert.deepEqual(errors, []);
    await page.close();
  }

  // Affiliate networks: the URL shows once, and again after a rotation.
  {
    const page = await browser.newPage({ viewport: { width: 1280, height: 1000 } });
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    await mock(page);
    await page.goto(`${origin}/tests/fixtures/tracker-sources.html?page=networks`);
    await page.getByText('No affiliate networks yet').waitFor();
    await page.getByRole('button', { name: 'New affiliate network' }).click();
    const dialog = page.getByRole('dialog');
    await dialog.getByRole('button', { name: 'Platforms', exact: true }).click();
    assert.equal(await dialog.getByRole('button', { name: /MaxBounty/ }).count(), 0);
    await dialog.getByRole('button', { name: /Everflow/ }).click();
    assert.equal(await page.getByLabel('Click id parameter').inputValue(), 'sub1');
    await page.getByLabel('Update').check();
    await page.getByLabel('IP allowlist').fill('203.0.113.0/24\n198.51.100.7');
    await page.getByRole('button', { name: 'Add network' }).click();
    const box = page.getByTestId('postback-url');
    await box.waitFor();
    assert.match(await box.innerText(), /ofp_ab12/);
    assert.deepEqual(bodies[`POST /v1/brands/${brand}/postback-sources`], { name: 'Everflow', click_id_param: 'sub1', duplicate_policy: 'update', ip_allowlist: ['203.0.113.0/24', '198.51.100.7'], currency: 'USD', click_expiry_days: 30, network: 'everflow' });
    await page.getByRole('button', { name: 'Make a new postback URL' }).click();
    await page.getByRole('alertdialog').getByRole('button', { name: 'Make a new URL' }).click();
    await page.getByText('New postback URL made').waitFor();
    assert.match(await box.innerText(), /ofp_cd34/);
    assert.deepEqual(errors, []);
    await page.close();
  }

  // Read-only, on a phone.
  {
    const page = await browser.newPage({ viewport: { width: 390, height: 844 } });
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    await mock(page);
    await page.goto(`${origin}/tests/fixtures/tracker-sources.html?page=sources&readonly`);
    await page.getByRole('row', { name: /Zeropark/ }).waitFor();
    assert.equal(await page.getByRole('button', { name: 'New traffic source' }).count(), 0);
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1), 'no sideways scroll on a phone');
    assert.deepEqual(errors, []);
    await page.close();
  }
  console.log('tracker sources: ok');
} catch (e) {
  console.error(output);
  throw e;
} finally {
  await browser?.close();
  server.kill();
}
