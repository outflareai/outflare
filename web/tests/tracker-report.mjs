// The tracker's Report page (plans/ad-tracker-ui.md B1) against mocked API
// answers: tiles and the tree table, expanding a row, drilling in (the row
// becomes a filter and the grouping moves down), sorting by a column, a
// custom column, saving a view, the CSV download, and phone cards. Starts
// a local Vite server and mocks every API request; needs no backend.
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
const m = (visits, conversions, revenue, cost, custom = {}) => ({
  visits, visitors: visits, conversions, revenue, cost, profit: cost === null ? null : revenue - cost, roi: cost ? (revenue - cost) / cost : null,
  roas: cost ? revenue / cost : null, cr: visits ? conversions / visits : null, epv: visits ? revenue / visits : null, cpa: cost && conversions ? cost / conversions : null, custom,
});
// report answers like the engine: rows for the levels asked, children nested.
const report = (q) => {
  const groups = (q.get('group_by') || 'channel').split(',');
  const cols = q.getAll('column').map(c => c.split(':')[0]);
  const custom = (mm) => Object.fromEntries(cols.map(c => [c, mm.visits ? Math.round(mm.revenue / mm.visits * 1e4) / 1e4 : null]));
  const leaf = (keys, mm) => ({ keys, metrics: { ...mm, custom: custom(mm) }, rows: [] });
  let rows;
  if (groups[0] === 'channel') {
    const us = leaf(['paid_ads', 'US'], m(200, 10, 500, null)), de = leaf(['paid_ads', 'DE'], m(100, 2, 80, null));
    const paid = { keys: ['paid_ads'], metrics: { ...m(300, 12, 580, null), custom: custom(m(300, 12, 580, null)) }, rows: groups.length > 1 ? [us, de] : [] };
    rows = [paid, leaf(['seo'], m(120, 3, 90, null))];
  } else {
    rows = [leaf(['US'], m(200, 10, 500, null))];
  }
  const t = m(420, 15, 670, null);
  return { from: q.get('from'), to: q.get('to'), timezone: 'Europe/Berlin', model: 'last_non_direct', conversion_time: q.get('conversion_time') || 'visit', currency: 'USD', group_by: groups,
    cost_available: false, cost_note: 'Spend can\'t be split by channel: group by day and one ad level to see cost.', totals: { ...t, custom: custom(t) }, rows, truncated: false, unconverted_currencies: [] };
};

let browser;
try {
  for (let i = 0; i < 100 && !output.includes('ready in'); i++) await new Promise(r => setTimeout(r, 200));
  browser = await chromium.launch({ headless: true, args: ['--no-sandbox'], executablePath: process.env.OUTFLARE_TEST_CHROMIUM || undefined });
  const calls = [];
  const posted = [];
  const views = [];
  const mock = (page) => page.route('**/v1/**', async route => {
    const req = route.request(); const u = new URL(req.url()); const q = u.searchParams; const p = u.pathname;
    calls.push(`${req.method()} ${p}${u.search}`);
    let status = 200, body = {}, contentType = 'application/json';
    if (p === '/v1/workspaces/workspace-a/brands') body = { data: [{ id: brand, name: 'Acme', handle: 'acme', status: 'active', timezone: 'Europe/Berlin' }] };
    else if (p === `/v1/brands/${brand}/drilldown` && q.get('format') === 'csv') { contentType = 'text/csv'; body = 'channel,visits\npaid_ads,300\n'; }
    else if (p === `/v1/brands/${brand}/drilldown` && q.get('source') === 'tracker') {
      const r = report(q);
      const groups = (q.get('group_by') || 'tracking_campaign').split(',');
      // A column the server can't read fails the whole report, as the real one does.
      if (q.getAll('column').some(c => c.includes('clicks'))) { status = 400; body = { error: { code: 'invalid_request', message: "formula: clicks isn't a metric", request_id: 'r' } }; }
      else if (q.getAll('filter').includes('country:ZZ')) body = { ...r, source: 'tracker', group_by: groups, cost_available: true, cost_note: null, unconverted_currencies: [], names: {}, rows: [] };
      else body = { ...r, source: 'tracker', group_by: groups, cost_available: true, cost_note: null, unconverted_currencies: ['EUR'],
        // The report names its ids itself: a path has no list to look it up in.
        names: { '30000000-0000-0000-0000-000000000001': 'Push US', '90000000-0000-0000-0000-000000000009': 'Tier-1 geos' },
        rows: [{ keys: ['30000000-0000-0000-0000-000000000001'], metrics: { ...m(900, 30, 75, 40), lander_views: 600, lander_clicks: 150, lander_ctr: 0.25, custom: {} }, rows: [] },
          ...(groups[0] === 'path' ? [{ keys: ['90000000-0000-0000-0000-000000000009'], metrics: { ...m(10, 1, 5, 4), lander_views: 0, lander_clicks: 0, lander_ctr: null, custom: {} }, rows: [] }] : [])] };
    }
    else if (p === `/v1/brands/${brand}/drilldown`) body = report(q);
    else if (p === `/v1/brands/${brand}/tracking-campaigns`) body = { data: [{ id: '30000000-0000-0000-0000-000000000001', name: 'Push US' }] };
    else if (p === `/v1/brands/${brand}/traffic-sources`) body = { data: [{ id: 's1', name: 'Acme Push', parameters: [{ name: 'Zone', field: 1 }] }, { id: 's2', name: 'Acme Native', parameters: [{ name: 'Widget', field: 1 }, { name: 'Placement', field: 4 }] }] };
    else if (p === `/v1/brands/${brand}/offers` || p === `/v1/brands/${brand}/landers`) body = { data: [] };
    else if (p.startsWith(`/v1/brands/${brand}/report-views/`) && req.method() === 'DELETE') { views.length = 0; status = 204; body = ''; }
    else if (p === `/v1/brands/${brand}/report-views` && req.method() === 'POST') {
      const b = JSON.parse(req.postData()); posted.push(b);
      const v = { id: '50000000-0000-0000-0000-000000000001', brand_id: brand, name: b.name, spec: b.spec, shared: false, mine: true, created_at: '2026-11-12T00:00:00Z', updated_at: '2026-11-12T00:00:00Z' };
      views.push(v); status = 201; body = v;
    }
    else if (p === `/v1/brands/${brand}/report-views`) body = { data: views };
    else { status = 404; body = { error: { code: 'not_found', message: 'nothing here', request_id: 'r' } }; }
    await route.fulfill({ status, contentType, body: typeof body === 'string' ? body : JSON.stringify(body) });
  });
  const last = (re) => [...calls].reverse().find(c => re.test(c)) ?? '';

  {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    await mock(page);
    await page.goto(`${origin}/tests/fixtures/tracker-report.html?group_by=channel,country`);
    await page.getByRole('heading', { name: 'Report' }).waitFor();
    // Dates default to the last 7 days in the brand's time zone.
    assert.match(last(/drilldown\?/), /group_by=channel%2Ccountry/);
    assert.match(last(/drilldown\?/), /from=\d{4}-\d{2}-\d{2}&to=\d{4}-\d{2}-\d{2}/);
    await page.getByText('Spend can\'t be split by channel').waitFor();
    await page.getByRole('cell', { name: 'paid_ads' }).waitFor();
    // Expanding a row shows its children.
    await page.getByRole('button', { name: 'Expand' }).first().click();
    await page.getByRole('cell', { name: 'DE' }).waitFor();
    // Sorting: the first click sorts largest first.
    await page.getByRole('columnheader', { name: 'Revenue' }).click();
    await page.waitForFunction(() => location.search.includes('sort=-revenue'));
    assert.match(last(/drilldown\?/), /sort=-revenue/);
    // Drilling in: the row becomes a filter and the grouping moves down.
    await page.getByRole('cell', { name: 'US' }).dblclick();
    await page.waitForFunction(() => location.search.includes('filter=channel'));
    const drilled = last(/drilldown\?/);
    assert.match(drilled, /filter=channel%3Apaid_ads/);
    assert.match(drilled, /filter=country%3AUS/);
    // A custom column.
    await page.getByRole('button', { name: 'Columns' }).click();
    await page.getByLabel('Column name').fill('rpv');
    await page.getByLabel('Formula').fill('revenue / visits');
    await page.getByRole('button', { name: '+ Custom column' }).click();
    // The editor sits above the table, so the new column shows as it is added.
    await page.getByRole('columnheader', { name: 'rpv' }).waitFor();
    await page.getByRole('button', { name: 'Done' }).click();
    assert.equal(await page.getByLabel('Column name').count(), 0);
    assert.match(last(/drilldown\?/), /column=rpv%3Arevenue\+%2F\+visits|column=rpv%3Arevenue%20%2F%20visits/);
    // Saving a view keeps the settings.
    await page.getByRole('button', { name: 'Save as…' }).click();
    await page.getByRole('alertdialog').getByLabel('Name').fill('US paid');
    await page.getByRole('alertdialog').getByRole('button', { name: 'Save' }).click();
    await page.getByText('View saved').waitFor();
    assert.equal(posted.length, 1);
    assert.deepEqual(posted[0].spec.columns, [{ name: 'rpv', formula: 'revenue / visits' }]);
    assert.ok(posted[0].spec.filters.includes('channel:paid_ads'));
    assert.deepEqual(posted[0].spec.date_range, { preset: 'last_7_days' });
    // The CSV.
    const download = page.waitForEvent('download');
    await page.getByRole('button', { name: 'Download CSV' }).click();
    await download;
    assert.match(last(/format=csv/), /format=csv/);
    // The tracker as the source: its own levels, campaigns by name, and
    // the source in the link.
    assert.equal(await page.getByRole('columnheader', { name: 'Lander CTR' }).count(), 0, 'no lander columns on site visits');
    await page.getByLabel('Report on').selectOption('tracker');
    await page.getByRole('cell', { name: 'Push US' }).waitFor();
    // The lander columns come with tracker clicks, and sort like the rest.
    await page.getByRole('columnheader', { name: 'Lander views' }).waitFor();
    assert.match(await page.getByRole('row', { name: /Push US/ }).innerText(), /600[\s\S]*150[\s\S]*25/);
    await page.getByRole('columnheader', { name: 'Lander CTR' }).click();
    await page.waitForFunction(() => location.search.includes('lander_ctr'));
    assert.match(last(/drilldown\?/), /sort=-?lander_ctr/);
    assert.match(last(/drilldown\?/), /source=tracker/);
    // Money the report couldn't convert is named, so a low number isn't taken for the truth.
    assert.match(await page.getByTestId('unconverted').innerText(), /Revenue and cost in EUR are not counted here: there is no exchange rate from EUR to USD yet/);
    assert.match(last(/drilldown\?/), /group_by=tracking_campaign/);
    assert.doesNotMatch(last(/drilldown\?/), /filter=/, 'switching source clears the filters');
    assert.equal(await page.getByLabel('Level 1').inputValue(), 'tracking_campaign');
    assert.ok(await page.getByLabel('Level 1').locator('option[value="offer"]').count() === 1, 'tracker levels offered');
    assert.equal(await page.getByLabel('Level 1').locator('option[value="utm_source"]').count(), 0, 'site levels not offered');
    assert.match(await page.evaluate(() => location.search), /source=tracker/);
    // Custom fields carry what the traffic sources call them.
    assert.equal(await page.getByLabel('Level 1').locator('option[value="field1"]').innerText(), 'Field 1 · Zone / Widget');
    assert.equal(await page.getByLabel('Level 1').locator('option[value="field4"]').innerText(), 'Field 4 · Placement');
    assert.equal(await page.getByLabel('Level 1').locator('option[value="field2"]').innerText(), 'Field 2');
    // Paths are shown by the name the report gives, not their id.
    await page.getByLabel('Level 1').selectOption('path');
    await page.getByRole('cell', { name: 'Tier-1 geos' }).waitFor();
    assert.equal(await page.getByText('90000000-0000-0000-0000-000000000009').count(), 0);
    await page.getByLabel('Level 1').selectOption('tracking_campaign');
    // A filter on a campaign is picked by name, and its chip shows the name.
    await page.getByLabel('Filter dimension').selectOption('tracking_campaign');
    assert.equal(await page.getByLabel('Filter value').evaluate(el => el.tagName), 'SELECT');
    await page.getByLabel('Filter value').selectOption({ label: 'Push US' });
    await page.getByRole('button', { name: '+ Filter' }).click();
    await page.getByText('Tracker campaign is Push US').waitFor();
    assert.match(last(/drilldown\?/), /filter=tracking_campaign%3A30000000-0000-0000-0000-000000000001/);
    await page.getByRole('button', { name: 'Remove filter' }).click();
    // A country code is read in capitals; filters that match nothing say so and can be cleared.
    await page.getByLabel('Filter dimension').selectOption('country');
    await page.getByLabel('Filter value').fill('zz');
    await page.getByRole('button', { name: '+ Filter' }).click();
    await page.getByText('Nothing matches these filters').waitFor();
    assert.match(last(/drilldown\?/), /filter=country%3AZZ/);
    await page.getByRole('button', { name: 'Clear filters' }).click();
    await page.getByRole('cell', { name: 'Push US' }).waitFor();
    // A formula with a word that isn't a metric is refused where it is typed, and never sent.
    await page.getByRole('button', { name: 'Columns' }).click();
    const before = calls.filter(c => c.includes('/drilldown?')).length;
    await page.getByLabel('Column name').fill('bad');
    await page.getByLabel('Formula').fill('revenue / clicks');
    await page.getByRole('button', { name: '+ Custom column' }).click();
    await page.getByRole('alert').getByText('"clicks" isn\'t a metric').waitFor();
    // So is one that doesn't read as a formula at all, whatever is wrong with it.
    for (const [formula, why] of [['revenue /', 'The formula ends early'], ['revenue ** 2', '"*" can\'t be used there'], ['(revenue + cost', 'A bracket isn\'t closed'], ['revenue cost', '"cost" isn\'t expected there'], ['1..2 * visits', '"1..2" isn\'t a number']]) {
      await page.getByLabel('Formula').fill(formula);
      await page.getByRole('button', { name: '+ Custom column' }).click();
      await page.getByRole('alert').getByText(why).waitFor();
    }
    assert.equal(calls.filter(c => c.includes('/drilldown?')).length, before, 'a bad formula is not sent');
    await page.keyboard.press('Escape');
    // Deleting a saved view asks first.
    await page.getByLabel('View').selectOption({ label: 'US paid' });
    await page.getByRole('button', { name: 'More' }).click();
    await page.getByText('Delete view').click();
    await page.getByRole('alertdialog').getByRole('button', { name: 'Cancel' }).click();
    assert.equal(calls.filter(c => c.startsWith('DELETE ')).length, 0, 'Cancel deletes nothing');
    await page.getByRole('button', { name: 'More' }).click();
    await page.getByText('Delete view').click();
    await page.getByRole('alertdialog').getByRole('button', { name: 'Delete' }).click();
    await page.getByText('View deleted').waitFor();
    assert.equal(calls.filter(c => c.startsWith('DELETE ')).length, 1);
    assert.deepEqual(errors, []);
    await page.close();
  }
  // A link that carries a broken column shows the error and no old numbers.
  {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    await mock(page);
    await page.goto(`${origin}/tests/fixtures/tracker-report.html?source=tracker&column=bad:revenue/clicks`);
    await page.getByText("formula: clicks isn't a metric").waitFor();
    assert.equal(await page.getByRole('cell', { name: 'Push US' }).count(), 0, 'no stale table under an error');
    // And a way out: the column can be removed from the error itself.
    await page.getByText('This report has a custom column (bad).').waitFor();
    await page.getByRole('button', { name: 'Remove it' }).click();
    await page.getByRole('cell', { name: 'Push US' }).waitFor();
    assert.doesNotMatch(await page.evaluate(() => new Promise(r => setTimeout(() => r(location.search), 50))), /column=/);
    await page.close();
  }
  // Phones get one card per row.
  {
    const page = await browser.newPage({ viewport: { width: 390, height: 800 } });
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    await mock(page);
    // A fresh visit opens on the tracker's own clicks.
    await page.goto(`${origin}/tests/fixtures/tracker-report.html`);
    await page.getByRole('button', { name: /Push US/ }).waitFor();
    assert.match(last(/drilldown\?/), /source=tracker/);
    assert.match(await page.evaluate(() => location.search), /source=tracker/);
    assert.deepEqual(errors, []);
    await page.close();
  }
  console.log('tracker report: ok');
} finally {
  await browser?.close();
  server.kill();
}
