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
const source = { id: src, brand_id: brand, name: 'Zeropark', template: 'zeropark', currency: 'USD', click_id: { param: 'cid', token: '{cid}' }, cost: { param: '', token: '' }, parameters: [],
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
  const adCampaign = '50000000-0000-0000-0000-000000000001';
  const target = (t, extra = {}) => ({ source_id: src, network: 'zeropark', campaign_ref: '11111111-1111-4111-8111-111111111111', ad_campaign_id: adCampaign, campaign_name: 'Push US',
    target: t, clicks: 1200, conversions: 0, currency: 'USD', cost: '8.4', revenue: '0', profit: '-8.4', roi: -1, state: 'active', state_detail: '',
    bid: '0.0125', auto_bid: false, bid_queued: null, min_bid: '0.0005', seen_at: now, ...extra });
  let targets = [target('pub-17'), target('pub-22', { clicks: 300, conversions: 4, cost: '2.1', revenue: '6', profit: '3.9', roi: 1.857, bid: null, auto_bid: true }),
    target('pub-31', { clicks: 40, cost: '0.3', profit: '-0.3', state: 'stopped', state_detail: 'over_budget', bid: null, min_bid: null, seen_at: null })];
  const mock = (page) => page.route('**/v1/**', async route => {
    const req = route.request(); const u = new URL(req.url()); const p = u.pathname; const method = req.method();
    calls.push(`${method} ${p}`);
    const b = req.postData() ? JSON.parse(req.postData()) : null;
    if (b) bodies[`${method} ${p}`] = b;
    const B = `/v1/brands/${brand}`;
    let status = 200, body = {};
    if (p === '/v1/workspaces/workspace-a/brands') body = { data: [{ id: brand, name: 'Acme', handle: 'acme', status: 'active', timezone: 'Europe/Berlin' }] };
    else if (p === '/v1/workspaces/workspace-a/integrations') body = { data: [{ id: 'i1', connector: 'google_ads', status: 'connected' }, { id: 'i2', connector: 'zeropark_ads', status: 'connected' }] };
    else if (p === `${B}/traffic-source-templates`) body = { data: templates };
    else if (p === `${B}/traffic-sources` && method === 'POST') { status = 201; body = { ...source, id: '40000000-0000-0000-0000-000000000002', name: b.name, template: b.template }; }
    else if (p === `${B}/traffic-sources`) body = { data: [source] };
    else if (p === `${B}/traffic-sources/${src}/postbacks`) body = { data: [{ id: 's1', conversion_id: 'c1', status: 'sent', payout: '1.20', url: 'https://postback.zeropark.example/?cid=abc', http_status: 200, error: '', attempts: 1, sent_at: now, created_at: now },
      { id: 's2', conversion_id: 'c2', status: 'failed', payout: '0.80', url: 'https://postback.zeropark.example/?cid=def', http_status: 500, error: 'the network answered 500', attempts: 5, sent_at: null, created_at: now }] };
    else if (p === `${B}/traffic-sources/${src}`) body = { ...source, ...b };
    else if (p === `${B}/network-targets`) body = { data: targets, from: '2026-11-09', to: '2026-11-15' };
    else if (p === `${B}/ads/campaigns/${adCampaign}/exclusions` && method === 'POST') {
      targets = targets.map((t) => (t.target === b.value ? { ...t, state: b.action === 'add' ? 'paused' : 'active' } : t));
      status = 202; body = { queued: true, change: { id: 'ch1', kind: b.action === 'add' ? 'exclusion_add' : 'exclusion_remove', status: 'queued' } };
    }
    else if (p === `${B}/ads/campaigns/${adCampaign}/bid` && method === 'POST') {
      targets = targets.map((t) => (t.target === b.placement ? { ...t, bid_queued: String(b.bid_micros / 1e6) } : t));
      status = 202; body = { queued: true, change: { id: 'ch2', kind: 'bid', status: 'queued' } };
    }
    else if (p === `${B}/network-costs`) body = { data: u.searchParams.get('source_id') === src ? [
      { source_id: src, network: 'zeropark', campaign_ref: '11111111-1111-4111-8111-111111111111', day: '2026-11-14', currency: 'USD', spend: '12.5', matched: '12', spread: '0.4', unattributed: '0.1', clicks: 4000, targets: 31, synced_at: now },
    ] : [] };
    else if (p === `${B}/affiliate-network-templates`) body = { data: [{ id: 'everflow', name: 'Everflow', kind: 'platform', click_id_param: 'sub1', macros: { cid: '{sub1}' } }, { id: 'maxbounty', name: 'MaxBounty', kind: 'network', click_id_param: 's2', macros: {} }] };
    else if (p === `${B}/postback-sources` && method === 'POST') { status = 201; networks = [network({ name: b.name })]; body = network({ name: b.name, postback_url: 'https://go.acme.example/postback/ofp_ab12?cid={sub1}&payout={payout}' }); }
    else if (p === `${B}/postback-sources`) body = { data: networks };
    else if (p === `${B}/postback-sources/old` && method === 'PATCH') { networks = networks.map(n => n.id === 'old' ? { ...n, status: b.status } : n); body = networks.find(n => n.id === 'old'); }
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
    // A Zeropark source with its connection: cost is exact, and its form
    // shows how the last days' spend was shared out.
    assert.match(await page.getByRole('row', { name: /Zeropark/ }).innerText(), /Exact from Zeropark/);
    // Targets: results per target, the losing one in red, and Pause asks
    // first, then sends the target as a placement exclusion.
    const tt = page.getByTestId('network-targets');
    await tt.waitFor();
    const losing = tt.getByRole('row', { name: /pub-17/ });
    assert.match(await losing.innerText(), /1,200[\s\S]*8\.40[\s\S]*-8\.40[\s\S]*-100%/);
    assert.match(await tt.getByRole('row', { name: /pub-22/ }).innerText(), /186%/);
    await losing.getByRole('button', { name: 'Pause pub-17' }).click();
    await page.getByRole('alertdialog').getByRole('button', { name: 'Pause target' }).click();
    await page.getByText('Pausing pub-17 on Zeropark').waitFor();
    assert.deepEqual(bodies[`POST /v1/brands/${brand}/ads/campaigns/${adCampaign}/exclusions`], { kind: 'placement', value: 'pub-17', action: 'add' });
    await tt.getByRole('row', { name: /pub-17/ }).getByText('Paused').waitFor();
    await tt.getByRole('row', { name: /pub-17/ }).getByRole('button', { name: 'Resume pub-17' }).waitFor();
    // Bids: the network's figure, Auto, and none for a target it never
    // listed, which shows what holds it back. A bid under the network's
    // floor can't be sent; a good one goes as a placement bid and shows
    // as waiting.
    assert.match(await tt.getByRole('row', { name: /pub-22/ }).innerText(), /Auto/);
    assert.match(await tt.getByRole('row', { name: /pub-31/ }).innerText(), /—[\s\S]*Over budget/);
    await tt.getByRole('button', { name: 'Change bid for pub-17' }).click();
    const bidDialog = page.getByRole('dialog');
    await bidDialog.getByText('It is 0.0125 now.').waitFor();
    await bidDialog.getByLabel('New bid').fill('0.0001');
    await bidDialog.getByText('Zeropark takes 0.0005 or more for this target.').waitFor();
    assert.equal(await bidDialog.getByRole('button', { name: 'Change bid' }).isDisabled(), true);
    await bidDialog.getByLabel('New bid').fill('0.009');
    await bidDialog.getByRole('button', { name: 'Change bid' }).click();
    await page.getByText('Changing the bid for pub-17 on Zeropark').waitFor();
    assert.deepEqual(bodies[`POST /v1/brands/${brand}/ads/campaigns/${adCampaign}/bid`], { bid_micros: 9000, placement: 'pub-17' });
    assert.match(await tt.getByRole('button', { name: 'Change bid for pub-17' }).innerText(), /0\.0125\s+0\.009/);
    // An existing source: its recent postbacks, with the failure's reason.
    await page.getByRole('row', { name: /Zeropark/ }).first().click();
    const costDays = page.getByTestId('network-cost-days');
    await costDays.waitFor();
    assert.match(await costDays.innerText(), /12\.50 USD[\s\S]*12\.00 USD[\s\S]*0\.40 USD[\s\S]*0\.10 USD/);
    await page.getByText('Recent postbacks').waitFor();
    await page.getByText('the network answered 500').waitFor();
    // It opened in the page, not over it, and Back returns to the list.
    await page.getByTestId('details').waitFor();
    assert.equal(await page.getByRole('dialog').count(), 0, 'no panel over the page');
    await page.getByRole('button', { name: 'Back to traffic sources' }).click();
    await page.getByRole('heading', { name: 'Traffic sources', exact: true }).waitFor();
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
    await page.getByRole('button', { name: 'New affiliate network' }).first().click();
    const dialog = page.getByRole('dialog');
    await dialog.getByRole('button', { name: 'Platforms', exact: true }).click();
    assert.equal(await dialog.getByRole('button', { name: /MaxBounty/ }).count(), 0);
    await dialog.getByRole('button', { name: /Everflow/ }).click();
    assert.equal(await page.getByLabel('Click id parameter').inputValue(), 'sub1');
    await page.getByLabel('Update').check();
    await page.getByLabel('IP allowlist').fill('203.0.113.0/24\n198.51.100.7');
    // A number can be typed over: the field waits for the whole number
    // (it used to snap to 1 when cleared, so 45 became 145), and settles
    // on the nearest allowed value when left out of range.
    const expiry = page.getByLabel('Click expiry days');
    await expiry.press('ControlOrMeta+a');
    await expiry.press('Backspace');
    await page.keyboard.type('45');
    assert.equal(await expiry.inputValue(), '45');
    await expiry.fill('900');
    await page.getByLabel('IP allowlist').focus();
    assert.equal(await expiry.inputValue(), '365');
    await expiry.fill('45');
    // The currency is picked from a list, so a typo can't be saved.
    assert.equal(await page.getByLabel('Currency').evaluate(el => el.tagName), 'SELECT');
    await page.getByLabel('Currency').selectOption('EUR');
    await page.getByRole('button', { name: 'Add network' }).click();
    const box = page.getByTestId('postback-url');
    await box.waitFor();
    assert.match(await box.innerText(), /ofp_ab12/);
    assert.deepEqual(bodies[`POST /v1/brands/${brand}/postback-sources`], { name: 'Everflow', click_id_param: 'sub1', duplicate_policy: 'update', ip_allowlist: ['203.0.113.0/24', '198.51.100.7'], currency: 'EUR', click_expiry_days: 45, network: 'everflow' });
    await page.getByRole('button', { name: 'Make a new postback URL' }).click();
    await page.getByRole('alertdialog').getByRole('button', { name: 'Make a new URL' }).click();
    await page.getByText('New postback URL made').waitFor();
    assert.match(await box.innerText(), /ofp_cd34/);
    assert.deepEqual(errors, []);
    await page.close();
  }

  // An archived network leaves the list, waits behind a switch, and can be
  // restored from its form.
  {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    await mock(page);
    networks = [network(), network({ id: 'old', name: 'Old network', status: 'archived' })];
    await page.goto(`${origin}/tests/fixtures/tracker-sources.html?page=networks`);
    await page.getByRole('button', { name: 'Everflow', exact: true }).waitFor();
    assert.equal(await page.getByText('Old network').count(), 0, 'an archived network is not in the list');
    await page.getByLabel('Show archived (1)').check();
    await page.getByRole('button', { name: 'Old network' }).click();
    assert.equal(await page.getByRole('button', { name: 'Archive', exact: true }).count(), 0);
    await page.getByRole('button', { name: 'Restore' }).click();
    await page.getByText('Network restored').waitFor();
    assert.deepEqual(bodies[`PATCH /v1/brands/${brand}/postback-sources/old`], { status: 'active' });
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
