// The Paid ads page (plans/paid-ads/plan.md, "Web experience") for a brand
// with a Google Ads account, against mocked API answers: the number against
// the target and its sentence, the decision inbox's Approve flow, the
// header's customers and window switches changing the performance query,
// the set-up path with the offer sheet, and what Google's Required Minimum
// Functionality asks: the date ranges, a report's CSV download and removing
// a campaign after a confirmation. Starts a local Vite server and mocks
// every API request; needs no backend.
import { readFile } from 'node:fs/promises';
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

// The brand, its Google Ads account and one campaign.
const brand = '10000000-0000-0000-0000-000000000001';
const account = '20000000-0000-0000-0000-000000000001';
const campaign = '30000000-0000-0000-0000-000000000001';
const campaign2 = '30000000-0000-0000-0000-000000000002';
const today = new Date().toISOString().slice(0, 10);
const totals = { spend_micros: 456_000_000, impressions: 12000, clicks: 900, conversions: 15, conversion_value_micros: null, cpc_micros: 506_667, ctr: 0.075, cost_per_conversion_micros: 30_400_000, platform_roas: null, daily_reach: null, frequency: null, link_clicks: null, landing_page_views: null, video_views: null, all_conversions: 21, all_conversion_value_micros: null };
const campaigns = [
  { id: campaign, external_id: '111', name: 'Brand search', connector: 'google_ads', account_id: account, account_name: 'Acme Ads', brand_id: brand, status: 'active', provider_status: 'ENABLED', objective: 'search', bidding_strategy: 'maximize_conversions', currency: 'USD', daily_budget_micros: 20_000_000, target_cost_per_conversion_micros: null, target_status: 'no_target', target_from: 'none', platform_target_cpa_micros: null, platform_target_roas: null, trend: 'steady', spend_change: 0.02, totals, previous_totals: totals },
  { id: campaign2, external_id: '222', name: 'Generic search', connector: 'google_ads', account_id: account, account_name: 'Acme Ads', brand_id: brand, status: 'active', provider_status: 'ENABLED', objective: 'search', bidding_strategy: 'maximize_conversions', currency: 'USD', daily_budget_micros: 40_000_000, target_cost_per_conversion_micros: null, target_status: 'no_target', target_from: 'none', platform_target_cpa_micros: null, platform_target_roas: null, trend: 'scaling_up', spend_change: 0.3, totals, previous_totals: totals },
];
const row = (c, spend, signups, payers, status) => ({
  id: c.id, level: 'campaign', name: c.name, external_id: c.external_id, status: 'active', connector: 'google_ads', ad_account_id: account, account_name: 'Acme Ads', campaign_id: c.id, currency: 'USD',
  spend_micros: spend, clicks: 400, impressions: 6000, platform_conversions: 7, signups, new_payers: payers,
  cost_per_signup_micros: signups ? Math.round(spend / signups) : null, cost_per_new_payer_micros: payers ? Math.round(spend / payers) : null, target_status: status,
  new_revenue: [{ currency: 'USD', amount_minor: 48_000 }], returning_revenue: [{ currency: 'USD', amount_minor: 12_000 }], lifetime_revenue: [{ currency: 'USD', amount_minor: 90_000 }],
  payback_months: 2.5, previous_spend_micros: spend, spend_change: 0.01, trend: 'steady', window_days: 30,
});
// 12 new customers for $456 is $38 each, under the $45 target.
const performance = (q) => {
  const previous = q.get('to') !== today;
  const data = previous
    ? [row(campaigns[0], 200_000_000, 20, 5, 'under'), row(campaigns[1], 220_000_000, 10, 5, 'on_target')]
    : [row(campaigns[0], 200_000_000, 30, 8, 'under'), row(campaigns[1], 256_000_000, 12, 4, 'over')];
  return { from: q.get('from'), to: q.get('to'), level: q.get('level') ?? 'campaign', customers: q.get('customers') ?? 'all', window_days: Number(q.get('window_days') ?? 30), target_cost_per_payer_micros: 45_000_000, target_cost_per_signup_micros: null, currency: 'USD', data, notes: [] };
};
const finding = (id, kind, extra) => ({
  id: `40000000-0000-0000-0000-00000000000${id}`, campaign_id: campaign2, campaign_name: 'Generic search', account_id: account, account_name: 'Acme Ads', connector: 'google_ads', currency: 'USD', brand_id: brand,
  kind, status: 'open', reason: '', evidence: { days: 14, spend_micros: 256_000_000, new_payers: 4, cost_per_new_payer_micros: 64_000_000, target_cost_per_payer_micros: 45_000_000, daily_budget_micros: 40_000_000 }, proposed: {},
  change_id: null, test_id: null, decided_by: null, decided_at: null, measure_from: null, measure_until: null, window_note: '', verdict: '', verdict_note: '', result: {}, judged_at: null,
  created_at: '2026-11-08T09:00:00Z', updated_at: '2026-11-08T09:00:00Z', autonomy: 'approve', impact_micros: null, unit: 'payers', shadow: false, applied_auto: false, reverted_at: null, rule: 'cost_over_target', ...extra,
});
const findings = [
  finding(1, 'budget_down', { reason: "Cut Generic search's budget by 20%?", proposed: { daily_budget_micros: 32_000_000 }, impact_micros: -9_000_000 }),
  finding(2, 'budget_up', { reason: "Raise Brand search's budget?", campaign_id: campaign, campaign_name: 'Brand search', proposed: { daily_budget_micros: 25_000_000 }, impact_micros: 0, evidence: { days: 14, spend_micros: 200_000_000, new_payers: 8, cost_per_new_payer_micros: 25_000_000, target_cost_per_payer_micros: 45_000_000, daily_budget_micros: 20_000_000, days_budget_spent: 9 } }),
  finding(3, 'negative_add', { reason: 'Paused a keyword that spent without a sign-up', status: 'applied', autonomy: 'auto', applied_auto: true, decided_at: '2026-11-09T06:00:00Z', proposed: { keyword: 'free template' } }),
  finding(4, 'alert.tracking_broken', { reason: 'Clicks arrive without their ids', autonomy: 'alert', evidence: {} }),
  finding(5, 'pause', { reason: 'Would have paused Generic search', status: 'shadow', autonomy: 'auto', shadow: true }),
  finding(6, 'budget_down', { reason: 'Cut the budget', status: 'judged', verdict: 'worked', verdict_note: '', measure_from: '2026-10-01', measure_until: '2026-10-21', judged_at: '2026-10-22T00:00:00Z', result: { before: { new_payers: 9, spend_micros: 369_000_000, days: 21 }, after: { new_payers: 14, spend_micros: 532_000_000, days: 21 } } }),
];
const acquisitions = {
  data: [{ id: '50000000-0000-0000-0000-000000000001', record_id: '60000000-0000-0000-0000-000000000001', record_name: 'Ana Example', connector: 'google_ads', ad_account_id: account, campaign: { id: campaign, name: 'Brand search', external_id: '111' }, ad_group: { id: null, name: 'Core', external_id: '9' }, ad: { id: null, name: '', external_id: '77' }, keyword: 'acme', click_id_kind: 'gclid', matched_by: 'template', clicked_at: '2026-11-01T10:00:00Z', signed_up_at: '2026-11-01T10:20:00Z', first_paid_at: '2026-11-03T08:00:00Z', payments: 2, revenue_to_date: [{ currency: 'USD', amount_minor: 9800 }], bucket: 'payer', ad_consent: true, window_days: 30, created_at: '2026-11-01T10:20:00Z' }],
  next_cursor: null,
};
const step = (id, title, done, detail, page) => ({ step: id, title, done, detail, fix: { tool: 'get_ads_setup', page, who: 'person' } });
const setup = {
  brand_id: brand, done: 7, total: 9, ready: false, checked_at: '2026-11-09T08:00:00Z',
  steps: [
    step('site_script', 'Put the website script on the site', true, 'The website script sends visits.', '/acme/acme/setup'),
    step('events', 'Send sign-ups and payments as events', true, 'Sign-ups and payments arrive.', '/acme/acme/setup'),
    step('payments', 'Connect where payments happen', true, 'Stripe is connected.', '/acme/acme/connectors'),
    step('ad_account', 'Connect an ad account to the brand', true, 'One Google Ads account.', '/acme/acme/ads/accounts'),
    step('conversion_feed', 'Send conversions back to the network', false, 'No conversion rule yet.', '/acme/acme/ads/conversions'),
    step('tracking_template', 'Add the tracking template to Google ads', true, 'Every live Google ad carries the template.', '/acme/acme/ads'),
    step('economics', 'Set what a new customer may cost', true, 'Target set.', '/acme/acme/ads'),
    step('knowledge_sheet', 'Review what Outflare thinks you sell', false, 'The sheet is read but nobody has said it looks right.', '/acme/acme/company'),
    step('consent', 'Choose who conversions are sent for', true, 'Consented people only.', '/acme/acme/ads/conversions'),
  ],
};
const sheet = (reviewed) => ({
  brand_id: brand, read_at: '2026-11-08T07:00:00Z', pages: 6, read_error: '', reviewed_at: reviewed ? '2026-11-09T09:00:00Z' : null, reviewed_by: reviewed ? 'user-a' : null, changed_at: '2026-11-08T07:00:00Z', refresh_daily: true, changed_since_review: false, trusted: true,
  knowledge: { summary: 'Acme sells project planning software to small construction firms.', industry: 'Software', pages: ['https://acme.example/'], lists: { products: [{ text: 'Acme Planner', url: 'https://acme.example/planner' }], services: [{ text: 'Onboarding for teams of ten or more', url: '' }], audiences: [{ text: 'Small construction firms', url: '' }] } },
});

let browser;
try {
  let ready = false;
  for (let n = 0; n < 100; n++) { try { ready = (await fetch(origin)).ok; if (ready) break; } catch {} await new Promise(r => setTimeout(r, 100)); }
  assert(ready, `Vite did not start: ${output}`);
  browser = await chromium.launch({ headless: true, args: ['--no-sandbox'], executablePath: process.env.OUTFLARE_TEST_CHROMIUM || undefined });

  const calls = [];
  // OUTFLARE_TEST_SHOTS names a folder to save a screenshot of each screen in.
  const shot = async (page, name) => { if (process.env.OUTFLARE_TEST_SHOTS) await page.screenshot({ path: `${process.env.OUTFLARE_TEST_SHOTS}/${name}.png`, fullPage: true }); };
  let reviewed = false;
  let decided = [];
  const removeBodies = [];
  const convBodies = [];
  const webAction = (id, name) => ({ id, name, type: 'webpage', category: 'signup', status: 'enabled', primary: true, counting: 'one', click_window_days: 30, default_value_micros: null, default_currency: '', always_use_default: false,
    snippets: [{ type: 'webpage', page_format: 'html', global_site_tag: `<script>gtag('config', 'AW-1')</script>`, event_snippet: `<script>gtag('event', 'conversion', {'send_to': 'AW-1/${id}'})</script>` }] });
  const convActions = [{ ...webAction('77', 'Demo booked') }, { id: '78', name: 'Offline sale', type: 'upload_clicks', category: 'purchase', status: 'enabled', primary: false, counting: 'many', click_window_days: 90, default_value_micros: null, default_currency: '', always_use_default: false, snippets: [] }];
  // mock answers every API call the page makes for the brand.
  const mock = (page) => page.route('**/v1/**', async route => {
    const req = route.request(); const u = new URL(req.url()); const q = u.searchParams; const p = u.pathname;
    calls.push(`${req.method()} ${p}${u.search}`);
    let status = 200, body = {};
    if (p === '/v1/workspaces/workspace-a/brands') body = { data: [{ id: brand, name: 'Acme', handle: 'acme', status: 'active' }] };
    else if (p === '/v1/workspaces/workspace-a/ads/connections') body = { data: [{ id: '70000000-0000-0000-0000-000000000001', connector: 'google_ads', name: 'Google Ads', accounts: 1, setup_needed: [], last_ok_at: '2026-11-09T08:00:00Z', last_error: null }] };
    else if (p === '/v1/workspaces/workspace-a/ads/accounts' || p === `/v1/brands/${brand}/ads/accounts`) body = { data: [{ id: account, connector: 'google_ads', external_id: '1234567890', name: 'Acme Ads', currency: 'USD', time_zone: 'UTC', status: 'active', brand_id: brand, integration_id: '70000000-0000-0000-0000-000000000001', sync_on: true, last_synced_at: '2026-11-09T08:00:00Z', last_error: null, target_cost_per_conversion_micros: null }] };
    else if (p === `/v1/brands/${brand}/ads/campaigns`) body = { data: campaigns };
    else if (p === `/v1/brands/${brand}/ads/summary`) body = { data: [], totals: [] };
    else if (p === `/v1/brands/${brand}/ads/economics`) body = { brand_id: brand, currency: 'USD', target_cost_per_payer_micros: 45_000_000, target_cost_per_signup_micros: null, acquisition_window_days: 30, payback_months: 12, gross_margin_percent: null, shadow_mode: true, set: true, updated_by: null, created_at: '2026-11-01T00:00:00Z', updated_at: '2026-11-01T00:00:00Z' };
    else if (p === `/v1/brands/${brand}/ads/setup`) body = setup;
    else if (p === `/v1/brands/${brand}/ads/performance`) body = performance(q);
    else if (p === `/v1/brands/${brand}/ads/acquisitions`) body = acquisitions;
    else if (p === `/v1/brands/${brand}/knowledge`) body = sheet(reviewed);
    else if (p === `/v1/brands/${brand}/knowledge/review` && req.method() === 'POST') { reviewed = true; body = sheet(true); }
    else if (p === `/v1/brands/${brand}/ads/drafts`) body = { data: [] };
    else if (p === `/v1/brands/${brand}/ads/accounts/${account}/conversion-actions` && req.method() === 'POST') { status = 202; convBodies.push(JSON.parse(req.postData())); convActions.push(webAction('79', 'Trial started')); body = { queued: true, change: { id: '81000000-0000-0000-0000-000000000001', kind: 'conversion_action_create', status: 'queued' } }; }
    else if (p === `/v1/brands/${brand}/ads/accounts/${account}/conversion-actions`) body = { data: convActions, categories: ['purchase', 'sign_up', 'lead', 'page_view', 'contact', 'other'] };
    else if (p === `/v1/brands/${brand}/ads/accounts/${account}/changes/81000000-0000-0000-0000-000000000001`) body = { id: '81000000-0000-0000-0000-000000000001', kind: 'conversion_action_create', status: 'applied', result: { conversion_action_id: '79' }, error: '' };
    else if (p === '/v1/workspaces/workspace-a/ads/conversion-rules' || p === '/v1/workspaces/workspace-a/ads/conversion-sends') body = { data: [] };
    else if (p === `/v1/brands/${brand}/ads/settings`) body = { consent_mode: 'opt_out' };
    else if (p === `/v1/brands/${brand}/ads/bidding-strategies`) body = { from: q.get('from'), to: q.get('to'), errors: [], notes: [], data: [{ id: '900', name: 'Leads tCPA', type: 'target_cpa', status: 'enabled', target_cpa_micros: 40_000_000, target_roas: null, campaign_count: 2, currency: 'USD', ad_account_id: account, account_name: 'Acme Ads', connector: 'google_ads', spend_micros: 80_000_000, impressions: 900, clicks: 40, cpc_micros: 2_000_000, conversions: 4, all_conversions: 6, cost_per_conversion_micros: 20_000_000 }] };
    else if (p === `/v1/brands/${brand}/ads/campaigns/${campaign2}/remove` && req.method() === 'POST') { status = 202; body = { queued: true, change: { id: '80000000-0000-0000-0000-000000000001', kind: 'campaign_remove', status: 'queued' } }; removeBodies.push(req.postData()); }
    else if (p === `/v1/brands/${brand}/attribution/ads`) body = { data: [] };
    else if (p === `/v1/brands/${brand}/ads/findings`) body = { data: findings.filter(f => !decided.includes(f.id) && (!q.get('status') || f.status === q.get('status'))) };
    else if (p.startsWith(`/v1/brands/${brand}/ads/findings/`) && /\/(approve|dismiss|acknowledge|revert)$/.test(p) && req.method() === 'POST') { decided.push(p.split('/').at(-2)); body = {}; }
    // The ads routes that moved under the brand answer 410, as the server does.
    else if (p.startsWith('/v1/workspaces/workspace-a/ads/') && !/\/ads\/(connections|accounts|conversion-rules|conversion-sends)$/.test(p)) { status = 410; body = { error: { code: 'route_moved', message: 'This route moved', request_id: 'r' } }; }
    else { status = 404; body = { error: { code: 'not_found', message: 'nothing here', request_id: 'r' } }; }
    await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  });

  // 1. The number: cost per new paying customer against the target, with
  // its sentence, and the header's switches changing the performance query.
  {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    await mock(page);
    await page.goto(`${origin}/tests/fixtures/ads.html`);
    await page.getByRole('heading', { name: 'Paid ads' }).waitFor();
    await page.getByTestId('cost-per-payer').waitFor();
    assert.equal(await page.getByTestId('cost-per-payer').textContent(), '$38');
    assert.equal(await page.getByTestId('hero-sentence').textContent(), '12 new customers at $38 each, under your $45 target.');
    await page.getByText('Under target').first().waitFor();
    await page.getByText('vs the 28 days before').waitFor();
    await shot(page, 'number');
    assert(calls.some(c => c.includes('/ads/performance?') && c.includes('window_days=30') && c.includes('customers=all')), calls.join('\n'));
    const before = calls.length;
    await page.getByRole('group', { name: 'Customers' }).getByRole('button', { name: 'New' }).click();
    await page.getByTestId('cost-per-payer').waitFor();
    assert(calls.slice(before).some(c => c.includes('/ads/performance?') && c.includes('customers=new')), calls.slice(before).join('\n'));
    const beforeWindow = calls.length;
    await page.getByRole('group', { name: 'Attribution window' }).getByRole('button', { name: '7-day window' }).click();
    await page.getByTestId('cost-per-payer').waitFor();
    assert(calls.slice(beforeWindow).some(c => c.includes('/ads/performance?') && c.includes('window_days=7')), calls.slice(beforeWindow).join('\n'));
    assert.deepEqual(errors, []);
    await page.close();
  }

  // 2. Needs you: the cards of each autonomy, and Approve on a saving
  // takes the card away and posts the approval.
  {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    await mock(page);
    await page.goto(`${origin}/tests/fixtures/ads.html?item=inbox`);
    const cut = page.getByText("Cut Generic search's budget by 20%?");
    await cut.waitFor();
    await page.getByText('Expected: about $9.00 less per new customer').waitFor();
    await page.getByText('Last 14 days, $256 spent, 4 new customers at $64 each, target $45').first().waitFor();
    await page.getByRole('button', { name: 'Undo' }).waitFor();
    await page.getByRole('button', { name: 'Acknowledge' }).waitFor();
    await page.getByText('What the rules would have done').waitFor();
    assert.equal(await page.getByRole('button', { name: 'Turn rules on' }).count(), 0, 'the shadow section starts collapsed');
    await page.getByText('What the rules would have done').click();
    await page.getByRole('button', { name: 'Turn rules on' }).waitFor();
    await page.getByText('Would have paused Generic search').waitFor();
    await shot(page, 'inbox');
    // The saving's Approve needs no confirmation.
    const card = page.locator('li', { hasText: "Cut Generic search's budget by 20%?" });
    await card.getByRole('button', { name: 'Approve', exact: true }).click();
    await cut.waitFor({ state: 'hidden' });
    assert(calls.some(c => c === `POST /v1/brands/${brand}/ads/findings/${findings[0].id}/approve`), calls.join('\n'));
    // A raise can spend more, so it asks first.
    const raise = page.locator('li', { hasText: "Raise Brand search's budget?" });
    await raise.getByRole('button', { name: 'Approve', exact: true }).click();
    await page.getByRole('alertdialog').getByText('This change can spend more').waitFor();
    await page.getByRole('alertdialog').getByRole('button', { name: 'Approve' }).click();
    await page.getByText("Raise Brand search's budget?").waitFor({ state: 'hidden' });
    assert(calls.some(c => c === `POST /v1/brands/${brand}/ads/findings/${findings[1].id}/approve`), calls.join('\n'));
    assert.deepEqual(errors, []);
    await page.close();
  }

  // 3. Set up: the two steps left and the offer sheet, reviewed in place.
  {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    await mock(page);
    await page.goto(`${origin}/tests/fixtures/ads.html?item=setup`);
    await page.getByText('7 of 9 done').waitFor();
    assert.equal(await page.getByText('To do', { exact: true }).count(), 2);
    await page.getByRole('heading', { name: 'What Outflare thinks you sell' }).waitFor();
    await page.getByText('Acme sells project planning software to small construction firms.').waitFor();
    await page.getByText('Not reviewed').waitFor();
    await shot(page, 'setup');
    await page.getByRole('button', { name: 'Looks right' }).click();
    await page.getByText(/^Reviewed /).waitFor();
    assert(calls.some(c => c === `POST /v1/brands/${brand}/knowledge/review`), calls.join('\n'));
    // A step's fix opens the page that fixes it.
    await page.getByRole('button', { name: 'Fix it' }).first().click();
    assert.deepEqual(await page.evaluate(() => window.gone.at(-1)), ['ads', 'conversions']);
    assert.deepEqual(errors, []);
    await page.close();
  }

  // 4. Results, customers and verdicts render their rows; nothing scrolls
  // sideways on a phone.
  {
    const page = await browser.newPage({ viewport: { width: 390, height: 844 } });
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    await mock(page);
    await page.goto(`${origin}/tests/fixtures/ads.html?item=results`);
    await page.getByRole('heading', { name: 'Results' }).waitFor();
    await page.getByText('Generic search').first().waitFor();
    const wide = await page.evaluate(() => {
      const w = document.documentElement.clientWidth;
      return [...document.querySelectorAll('*')].filter(e => e.getBoundingClientRect().right > w + 1).slice(0, 8).map(e => `${e.tagName.toLowerCase()}.${String(e.className).slice(0, 60)}`);
    });
    assert.deepEqual(wide, [], 'the results screen scrolls sideways on a phone');
    await shot(page, 'results-phone');
    await page.getByRole('tab', { name: 'Customers' }).click();
    await page.getByText('Ana Example').first().waitFor();
    await page.getByText('Brand search › Core › 77 › acme').first().waitFor();
    await shot(page, 'customers-phone');
    await page.getByRole('tab', { name: 'Verdicts' }).click();
    await page.getByText('This cut worked: 14 new customers at $38 each, before 9 at $41.').waitFor();
    assert.deepEqual(errors, []);
    await page.close();
  }
  // 5. Google's Required Minimum Functionality: the 30-day and custom
  // ranges, the campaign report with status and all conversions shown, its
  // CSV download, the bidding strategy report, and removing a campaign only
  // after a confirmation that says it can't be undone.
  {
    const page = await browser.newPage({ viewport: { width: 1400, height: 1000 }, acceptDownloads: true });
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    await mock(page);
    await page.goto(`${origin}/tests/fixtures/ads.html?item=results`);
    const report = page.locator('[data-slot=card]', { hasText: 'Campaigns as each network reports them' });
    await report.locator('table').getByText('Generic search').first().waitFor();
    for (const h of ['Status', 'Clicks', 'Impressions', 'Conv.', 'All conv.']) await report.getByRole('columnheader', { name: h, exact: true }).waitFor();
    await page.locator('[data-slot=card]', { hasText: 'Bidding strategies' }).locator('table').getByText('Leads tCPA').waitFor();
    // 30 days and a custom range change what is asked for.
    const before = calls.length;
    await page.getByRole('group', { name: 'Date range' }).getByRole('button', { name: '30 days' }).click();
    const from30 = new Date(Date.now() - 29 * 86_400_000).toISOString().slice(0, 10);
    await report.locator('table').getByText('Generic search').first().waitFor();
    for (let i = 0; i < 50 && !calls.slice(before).some(c => c.includes('/ads/campaigns?') && c.includes(`from=${from30}`)); i++) await page.waitForTimeout(100);
    assert(calls.slice(before).some(c => c.includes('/ads/campaigns?') && c.includes(`from=${from30}`)), calls.slice(before).join('\n'));
    const beforeCustom = calls.length;
    await page.getByRole('group', { name: 'Date range' }).getByRole('button', { name: 'Custom' }).click();
    await page.getByLabel('From', { exact: true }).fill('2026-09-01');
    await page.getByLabel('To', { exact: true }).fill('2026-09-30');
    await page.getByRole('form', { name: 'Custom date range' }).getByRole('button', { name: 'Apply' }).click();
    for (let i = 0; i < 50 && !calls.slice(beforeCustom).some(c => c.includes('/ads/campaigns?') && c.includes('from=2026-09-01') && c.includes('to=2026-09-30')); i++) await page.waitForTimeout(100);
    assert(calls.slice(beforeCustom).some(c => c.includes('/ads/campaigns?') && c.includes('from=2026-09-01') && c.includes('to=2026-09-30')), calls.slice(beforeCustom).join('\n'));
    assert(calls.slice(beforeCustom).some(c => c.includes('/ads/performance?') && c.includes('from=2026-09-01') && c.includes('to=2026-09-30')), 'the results follow the custom range');
    await shot(page, 'rmf-results');
    // The campaign report's download: the columns shown, money with its currency.
    const [download] = await Promise.all([page.waitForEvent('download'), report.getByRole('button', { name: 'Download CSV' }).click()]);
    assert.equal(download.suggestedFilename(), 'outflare-ads-campaigns-2026-09-01-2026-09-30.csv');
    const csv = (await readFile(await download.path(), 'utf8')).replace(/^\ufeff/, '').trim().split('\r\n');
    assert.equal(csv[0], 'Campaign,Network,Status,Spend,Clicks,Impressions,CPC,Conv.,All conv.,Cost / conv.');
    assert.equal(csv.length, 3, csv.join('\n'));
    assert(csv.some(l => l.startsWith('Brand search,Google Ads · Acme Ads,Active,456.00 USD,900,12000,0.51 USD,15,21,30.40 USD')), csv.join('\n'));
    // Remove asks first, saying it can't be undone, then posts confirm: true.
    await report.getByRole('button', { name: 'Manage Generic search' }).click();
    await page.getByRole('menuitem', { name: 'Remove' }).click();
    const dialog = page.getByRole('alertdialog');
    await dialog.getByText("Google Ads can't undo a removal").waitFor();
    await dialog.getByRole('button', { name: 'Cancel' }).click();
    assert.equal(removeBodies.length, 0, 'cancel removes nothing');
    await report.getByRole('button', { name: 'Manage Generic search' }).click();
    await page.getByRole('menuitem', { name: 'Remove' }).click();
    await page.getByRole('alertdialog').getByRole('button', { name: 'Remove campaign' }).click();
    for (let i = 0; i < 50 && removeBodies.length === 0; i++) await page.waitForTimeout(100);
    assert.deepEqual(removeBodies.map(b => JSON.parse(b)), [{ confirm: true }]);
    assert.deepEqual(errors, []);
    await page.close();
  }
  // 6. C.65: a Google account's conversion actions with a download, the
  // website code of one, and a new website conversion whose code shows
  // once Google has made it.
  {
    const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    await mock(page);
    await page.goto(`${origin}/tests/fixtures/ads.html?item=conversions`);
    const card = page.locator('[data-slot=card]', { hasText: 'Conversion actions · Acme Ads' });
    await card.locator('table').getByText('Demo booked').waitFor();
    await card.getByRole('button', { name: 'Download CSV' }).waitFor();
    await card.locator('table').getByRole('button', { name: 'Show the code' }).click();
    await page.getByRole('dialog').getByTestId('event-snippet').getByText("AW-1/77").waitFor();
    await page.keyboard.press('Escape');
    await card.getByRole('button', { name: 'New website conversion' }).click();
    const dialog = page.getByRole('dialog');
    await dialog.getByLabel('Name').fill('Trial started');
    await dialog.getByRole('button', { name: 'Create and show the code' }).click();
    await page.getByRole('dialog', { name: 'Put Trial started on your site' }).getByTestId('event-snippet').getByText('AW-1/79').waitFor();
    assert.deepEqual(convBodies, [{ name: 'Trial started', category: 'sign_up', counting: 'one', click_window_days: 30 }]);
    await shot(page, 'rmf-conversion');
    assert.deepEqual(errors, []);
    await page.close();
  }
  // Ads are a brand's: no workspace ads route but those main keeps on the
  // workspace (connections, every brand's accounts for assigning one, and
  // the conversion rules and their sends).
  const workspaceAds = calls.filter(c => c.includes('/v1/workspaces/workspace-a/ads/') && !/ \/v1\/workspaces\/workspace-a\/ads\/(connections|accounts|conversion-rules|conversion-sends)(\?|$)/.test(c));
  assert.deepEqual(workspaceAds, []);
  console.log('paid ads: ok');
} finally {
  await browser?.close();
  server.kill();
}
