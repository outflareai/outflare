// The tracker's Campaigns page (plans/ad-tracker-ui.md B2) against mocked
// API answers: the list with the last 7 days' numbers, a new campaign, and
// the editor: General (source, name, redirect mode), Destination (a path
// with a rule, landers and offers with weights and their share, Equalise,
// suggested weights applied by a click, a flow instead) and Tracking (the
// campaign URL and the source's parameters). Starts a local Vite server and
// mocks every API request; needs no backend.
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
const camp = '20000000-0000-0000-0000-000000000001';
const link = '30000000-0000-0000-0000-000000000001';
const src = '40000000-0000-0000-0000-000000000001';
const offerA = '50000000-0000-0000-0000-00000000000a', offerB = '50000000-0000-0000-0000-00000000000b';
const lander = '60000000-0000-0000-0000-000000000001';
const flow = '70000000-0000-0000-0000-000000000001';
const now = '2026-11-12T00:00:00Z';
const source = { id: src, brand_id: brand, name: 'PropellerAds', template: 'propellerads', currency: 'USD', click_id: { param: 'clickid', token: '${SUBID}' }, cost: { param: 'cost', token: '${cost}' },
  parameters: [{ name: 'Zone', param: 'zone', token: '${zoneid}', field: 1 }], postback: { url: 'https://ad.propellerads.example/conversion.php?visitor_id={external_id}&payout={payout}' },
  postback_rules: {}, status: 'active', campaigns: 1, created_at: now, updated_at: now };
let campaign = { id: camp, brand_id: brand, name: 'Propeller - US - Sweepstakes', slug: 'propeller-us', code: 'ofc_abc', channel: 'paid_ads', source: 'propellerads', landing_url: 'https://acme.example/',
  redirect_links: true, traffic_source_id: src, redirect_mode: '302', status: 'active', links: 1, clicks: 40, created_at: now, updated_at: now,
  cost: { model: 'token', amount: '0', share: 0, currency: '' } };
const dest = (id, name, url, extra = {}) => ({ id, name, url, weight: 100, daily_click_cap: null, clicks_today: 0, open: true, offer_id: null, offer_name: null, lander_id: null, lander_name: null, ...extra });
let destinations = [dest('d1', 'Offer A', 'https://offers.example/a', { offer_id: offerA, offer_name: 'Offer A' }), dest('d2', 'Offer B', 'https://offers.example/b', { offer_id: offerB, offer_name: 'Offer B', weight: 50 })];
let paths = [];
let flowId = null;

let browser;
try {
  for (let i = 0; i < 100 && !output.includes('ready in'); i++) await new Promise(r => setTimeout(r, 200));
  browser = await chromium.launch({ headless: true, args: ['--no-sandbox'], executablePath: process.env.OUTFLARE_TEST_CHROMIUM || undefined });
  const calls = [];
  const bodies = {};
  const mock = (page) => page.route('**/v1/**', async route => {
    const req = route.request(); const u = new URL(req.url()); const p = u.pathname; const method = req.method();
    calls.push(`${method} ${p}${u.search}`);
    const b = req.postData() ? JSON.parse(req.postData()) : null;
    if (b) bodies[`${method} ${p}`] = b;
    const L = `/v1/brands/${brand}/tracking-campaigns/${camp}/links/${link}`;
    let status = 200, body = {};
    if (p === '/v1/workspaces/workspace-a/brands') body = { data: [{ id: brand, name: 'Acme', handle: 'acme', status: 'active', timezone: 'Europe/Berlin' }] };
    else if (p === `/v1/brands/${brand}/tracking-campaigns` && method === 'POST') { status = 201; body = { ...campaign, id: '20000000-0000-0000-0000-000000000002', name: b.name }; }
    else if (p === `/v1/brands/${brand}/tracking-campaigns`) body = { data: [campaign, { ...campaign, id: 'x', name: 'A plain UTM campaign', traffic_source_id: null, redirect_links: false }] };
    else if (p === `/v1/brands/${brand}/tracking-campaigns/${camp}` && method === 'PATCH') { campaign = { ...campaign, name: b.name, redirect_mode: b.redirect_mode, traffic_source_id: b.traffic_source || null, cost: b.cost ? { ...campaign.cost, ...b.cost } : campaign.cost }; body = campaign; }
    else if (p === `/v1/brands/${brand}/tracking-campaigns/${camp}`) body = campaign;
    else if (p === `/v1/brands/${brand}/tracking-campaigns/${camp}/links`) body = { data: [{ id: link, campaign_id: camp, url: 'https://acme.example/', content: '', term: '', tagged_url: 'https://acme.example/?utm_source=propellerads',
      redirect_url: 'https://go.acme.example/l/abc', campaign_url: 'https://go.acme.example/l/abc?clickid=${SUBID}&cost=${cost}&zone=${zoneid}', clicks: 40, landed: 30, suspect_clicks: 0, created_at: now }] };
    else if (p === `${L}/destinations` && method === 'PUT') { destinations = b.destinations.map((d, i) => dest(d.id ?? `n${i}`, d.name, d.url ?? '', { weight: d.weight, offer_id: d.offer_id ?? null, lander_id: d.lander_id ?? null })); body = { link_id: link, fallback_url: 'https://acme.example/', destinations, needs_domain: false }; }
    else if (p === `${L}/destinations`) body = { link_id: link, fallback_url: 'https://acme.example/', destinations, needs_domain: false };
    else if (p === `${L}/paths` && method === 'PUT') { paths = b.paths.map((x, i) => ({ ...x, id: x.id ?? `p${i}`, destinations: x.destinations.map((d, j) => dest(`pd${j}`, d.name, d.url ?? '', { weight: d.weight, lander_id: d.lander_id ?? null, offer_id: d.offer_id ?? null })) })); body = { link_id: link, paths, needs_domain: false }; }
    else if (p === `${L}/paths`) body = { link_id: link, paths, needs_domain: false };
    else if (p === `${L}/flow` && method === 'PUT') { flowId = b.flow_id; body = { link_id: link, flow_id: flowId }; }
    else if (p === `${L}/flow`) body = { link_id: link, flow_id: flowId };
    else if (p === `${L}/weights`) body = { queued: true };
    else if (p === `${L}/weight-suggestions`) body = { link_id: link, flow_id: null, metric: 'revenue', days: 14, since: now, currency: 'USD', revenue_incomplete: false, min_clicks: 100, min_conversions: 10, lists: [
      { path_id: null, path_name: 'Default', kind: 'destinations', ready: true, reason: null, items: [
        { destination_id: 'd1', name: 'Offer A', url: 'https://offers.example/a', offer_id: offerA, lander_id: null, weight: 100, suggested_weight: 80, clicks: 900, excluded_clicks: 3, conversions: 40, revenue: '400.00', conversion_rate: 0.044, revenue_per_click: '0.44', probability_best: 0.91 },
        { destination_id: 'd2', name: 'Offer B', url: 'https://offers.example/b', offer_id: offerB, lander_id: null, weight: 50, suggested_weight: 20, clicks: 450, excluded_clicks: 0, conversions: 9, revenue: '90.00', conversion_rate: 0.02, revenue_per_click: '0.20', probability_best: 0.09 }] }] };
    else if (p === `/v1/brands/${brand}/traffic-sources`) body = { data: [source] };
    else if (p === `/v1/brands/${brand}/offers`) body = { data: [{ id: offerA, name: 'Offer A', url: 'https://offers.example/a' }, { id: offerB, name: 'Offer B', url: 'https://offers.example/b' }] };
    else if (p === `/v1/brands/${brand}/landers`) body = { data: [{ id: lander, name: 'Quiz lander', url: 'https://acme.example/quiz' }] };
    else if (p === `/v1/brands/${brand}/flows`) body = { data: [{ id: flow, brand_id: brand, name: 'Sweeps flow', status: 'active', links: 3, paths: [], destinations: [], needs_domain: false, created_at: now, updated_at: now }] };
    else if (p === `/v1/brands/${brand}/tracking-domains`) body = { data: [{ id: 'td', status: 'verified' }] };
    else if (p === `/v1/brands/${brand}/drilldown`) body = { rows: [{ keys: [camp], metrics: { visits: 1200, visitors: 1100, conversions: 49, revenue: 490, cost: 300, profit: 190, roi: 0.6333, roas: 1.63, cr: 0.04, epv: 0.41, cpa: 6.12, custom: {} }, rows: [] }] };
    else { status = 404; body = { error: { code: 'not_found', message: 'nothing here', request_id: 'r' } }; }
    await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  });

  // The list: tracker campaigns only, with the report's numbers.
  {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    await mock(page);
    await page.goto(`${origin}/tests/fixtures/tracker-campaigns.html`);
    await page.getByRole('heading', { name: 'Campaigns', exact: true }).waitFor();
    await page.getByRole('cell', { name: 'Propeller - US - Sweepstakes' }).waitFor();
    assert.equal(await page.getByText('A plain UTM campaign').count(), 0, 'a plain UTM campaign is not a tracker campaign');
    await page.getByRole('cell', { name: 'PropellerAds' }).waitFor();
    assert.match(calls.find(c => c.includes('/drilldown?')), /group_by=tracking_campaign/);
    const row = page.getByRole('row', { name: /Propeller - US/ });
    assert.match(await row.innerText(), /1,200/);
    assert.match(await row.innerText(), /63/);
    // Cancel closes the form without creating anything.
    await page.getByRole('button', { name: 'New campaign' }).click();
    await page.getByRole('button', { name: 'Cancel' }).click();
    assert.equal(calls.filter(c => c.startsWith('POST ')).length, 0, 'Cancel creates nothing');
    // New campaign: the source, a name, then the editor.
    await page.getByRole('button', { name: 'New campaign' }).click();
    await page.getByLabel('Name').fill('Propeller - DE - Sweeps');
    await page.getByLabel('Landing page').fill('https://acme.example/de');
    await page.getByRole('button', { name: 'Create and set up' }).click();
    await page.waitForFunction(() => window.gone.length > 0);
    const created = bodies[`POST /v1/brands/${brand}/tracking-campaigns`];
    assert.deepEqual([created.name, created.traffic_source, created.redirect_links, created.channel], ['Propeller - DE - Sweeps', src, true, 'paid_ads']);
    assert.deepEqual(await page.evaluate(() => window.gone[0]), ['tracker-campaigns', '20000000-0000-0000-0000-000000000002']);
    assert.deepEqual(errors, []);
    await page.close();
  }

  // The editor.
  {
    const page = await browser.newPage({ viewport: { width: 1280, height: 1000 } });
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    await mock(page);
    await page.goto(`${origin}/tests/fixtures/tracker-campaigns.html?item=${camp}`);
    await page.getByRole('heading', { name: 'Propeller - US - Sweepstakes' }).waitFor();
    assert.match(await page.getByTestId('campaign-url').innerText(), /clickid=\$\{SUBID\}/);
    // General: rename and pick a redirect mode.
    await page.getByLabel('Name').fill('Propeller - US - Sweeps v2');
    await page.getByLabel(/Double meta refresh/).check();
    await page.getByRole('button', { name: 'Save' }).click();
    await page.getByRole('button', { name: 'Saved' }).waitFor();
    assert.deepEqual(bodies[`PATCH /v1/brands/${brand}/tracking-campaigns/${camp}`], { name: 'Propeller - US - Sweeps v2', traffic_source: src, redirect_mode: 'double_meta' });
    // The cost model: CPC at 0.05 a click.
    await page.getByLabel('Cost model').selectOption('cpc');
    await page.getByLabel('Cost amount').fill('0.05');
    await page.getByRole('button', { name: 'Save' }).click();
    await page.getByRole('button', { name: 'Saved' }).waitFor();
    assert.deepEqual(bodies[`PATCH /v1/brands/${brand}/tracking-campaigns/${camp}`].cost, { model: 'cpc', currency: '', amount: '0.05' });

    // Destination: the default path's offers with their share of clicks.
    await page.getByRole('tab', { name: 'Destination' }).click();
    await page.getByText('Default path: everyone else').waitFor();
    assert.match(await page.getByTitle('67% of the clicks').innerText(), /67%/);
    // The suggestion is a diff, applied only by a click.
    const sugg = page.getByTestId('suggested-weights');
    assert.match(await sugg.innerText(), /100 → 80/);
    await sugg.getByRole('button', { name: 'Apply' }).click();
    await page.getByText('Suggested weights applied').waitFor();
    assert.deepEqual(bodies[`PATCH /v1/brands/${brand}/tracking-campaigns/${camp}/links/${link}/weights`], { source: 'suggestion', weights: [{ destination_id: 'd1', weight: 80 }, { destination_id: 'd2', weight: 20 }] });
    // Equalise, then a path for mobile visitors in the US with a lander.
    await page.getByRole('button', { name: 'Equalise' }).click();
    assert.equal(await page.getByTitle('50% of the clicks').count(), 2);
    await page.getByRole('button', { name: '+ Path' }).click();
    await page.getByLabel('Path name').fill('US mobile');
    await page.getByLabel('Rule values').fill('US, CA');
    await page.getByRole('button', { name: '+ Condition' }).click();
    await page.getByLabel('Rule values').nth(1).fill('mobile');
    await page.getByText('Country is US or CA, and Device is mobile').waitFor();
    await page.getByRole('button', { name: '+ Lander' }).first().click();
    await page.getByRole('button', { name: '+ Offer or page' }).first().click();
    await page.getByRole('button', { name: 'Save destinations' }).click();
    await page.getByText('Destinations saved').waitFor();
    const put = bodies[`PUT /v1/brands/${brand}/tracking-campaigns/${camp}/links/${link}/destinations`].destinations;
    assert.deepEqual(put.map(d => [d.offer_id, d.weight, d.url]), [[offerA, 100, undefined], [offerB, 100, undefined]]);
    const rules = bodies[`PUT /v1/brands/${brand}/tracking-campaigns/${camp}/links/${link}/paths`].paths;
    assert.equal(rules.length, 1);
    assert.deepEqual(rules[0].conditions, [{ field: 'country', op: 'is', values: ['US', 'CA'] }, { field: 'device', op: 'is', values: ['mobile'] }]);
    assert.deepEqual(rules[0].destinations.map(d => [d.lander_id ?? null, d.offer_id ?? null]), [[lander, null], [null, offerA]]);
    // A flow instead of the campaign's own paths.
    await page.getByLabel('Use a flow').click();
    await page.getByText('Clicks follow the flow "Sweeps flow", shared with 2 other links').waitFor();
    assert.deepEqual(bodies[`PUT /v1/brands/${brand}/tracking-campaigns/${camp}/links/${link}/flow`], { flow_id: flow });
    assert.equal(await page.getByRole('button', { name: 'Save destinations' }).count(), 0);

    // Tracking: the source's parameters and its postback.
    await page.getByRole('tab', { name: 'Tracking' }).click();
    await page.getByRole('cell', { name: 'Zone (field 1)' }).waitFor();
    await page.getByText('Postback to PropellerAds').waitFor();
    assert.deepEqual(errors, []);
    await page.close();
  }

  // Read-only, on a phone: nothing to edit, the URL still there.
  {
    const page = await browser.newPage({ viewport: { width: 390, height: 844 } });
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    await mock(page);
    await page.goto(`${origin}/tests/fixtures/tracker-campaigns.html?item=${camp}&readonly`);
    await page.getByTestId('campaign-url').waitFor();
    assert.equal(await page.getByRole('button', { name: 'Save' }).count(), 0);
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1), 'no sideways scroll on a phone');
    assert.deepEqual(errors, []);
    await page.close();
  }
  console.log('tracker campaigns: ok');
} catch (e) {
  console.error(output);
  throw e;
} finally {
  await browser?.close();
  server.kill();
}
