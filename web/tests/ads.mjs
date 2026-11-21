// The Paid ads page (plans/paid-ads/plan.md, "Web experience") for a brand
// with a Google Ads account, against mocked API answers: the number against
// the target and its sentence, revenue and ROAS beside the spend in the
// headline and the Results table, the decision inbox's Approve flow, the
// header's customers and window switches changing the performance query,
// the set-up path with the offer sheet, and what Google's Required Minimum
// Functionality asks: the date ranges, a report's CSV download and removing
// a campaign after a confirmation, and a campaign's own page, where an ad
// set and an ad are paused, resumed and budgeted from Manage. Starts a local Vite server and mocks
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
  { id: campaign, external_id: '111', name: 'Brand search', connector: 'google_ads', ad_account_id: account, account_id: account, account_name: 'Acme Ads', brand_id: brand, status: 'active', provider_status: 'ENABLED', objective: 'search', bidding_strategy: 'maximize_conversions', currency: 'USD', report_currency: 'USD', exchange_rate: 1, daily_budget_micros: 20_000_000, target_cost_per_conversion_micros: null, target_status: 'no_target', target_from: 'none', platform_target_cpa_micros: null, platform_target_roas: null, trend: 'steady', spend_change: 0.02, totals, previous_totals: totals, primary_status: 'eligible', primary_status_reasons: [], optimization_score: 0.8214, budget_lost_share: 0.04, rank_lost_share: 0.31 },
  { id: campaign2, external_id: '222', name: 'Generic search', connector: 'google_ads', ad_account_id: account, account_id: account, account_name: 'Acme Ads', brand_id: brand, status: 'active', provider_status: 'ENABLED', objective: 'search', bidding_strategy: 'maximize_conversions', currency: 'USD', report_currency: 'USD', exchange_rate: 1, daily_budget_micros: 40_000_000, target_cost_per_conversion_micros: null, target_status: 'no_target', target_from: 'none', platform_target_cpa_micros: null, platform_target_roas: null, trend: 'scaling_up', spend_change: 0.3, totals, previous_totals: totals, primary_status: 'limited', primary_status_reasons: ['budget_constrained'], optimization_score: null, budget_lost_share: 0.23, rank_lost_share: 0.4 },
];
const row = (c, spend, signups, payers, status) => ({
  id: c.id, level: 'campaign', name: c.name, external_id: c.external_id, status: 'active', connector: 'google_ads', ad_account_id: account, account_name: 'Acme Ads', campaign_id: c.id, currency: 'USD', account_currency: 'USD',
  spend_micros: spend, clicks: 400, impressions: 6000, platform_conversions: 7, signups, new_payers: payers,
  cost_per_signup_micros: signups ? Math.round(spend / signups) : null, cost_per_new_payer_micros: payers ? Math.round(spend / payers) : null, target_status: status,
  new_revenue: [{ currency: 'USD', amount_minor: 48_000 }], returning_revenue: [{ currency: 'USD', amount_minor: 12_000 }], lifetime_revenue: [{ currency: 'USD', amount_minor: 90_000 }],
  // $480 new and $120 returning revenue in the range, over the spend.
  roas: (60_000 * 10_000) / spend,
  payback_months: 2.5, previous_spend_micros: spend, spend_change: 0.01, trend: 'steady', window_days: 30,
});
// 12 new customers for $456 is $38 each, under the $45 target.
// One row per day of the range: a customer every other day.
const daily = (from, to) => {
  const out = [];
  for (let t = Date.parse(`${from}T00:00:00Z`), i = 0; t <= Date.parse(`${to}T00:00:00Z`); t += 86400000, i++) out.push({ day: new Date(t).toISOString().slice(0, 10), currency: 'USD', spend_micros: 16_000_000, signups: 1, new_payers: i % 2, clicks: 30, impressions: 400, platform_conversions: 0.5, revenue_micros: 40_000_000 });
  return out;
};
const performance = (q) => {
  const previous = q.get('to') !== today;
  const data = previous
    ? [row(campaigns[0], 200_000_000, 20, 5, 'under'), row(campaigns[1], 220_000_000, 10, 5, 'on_target')]
    : [row(campaigns[0], 200_000_000, 30, 8, 'under'), row(campaigns[1], 256_000_000, 12, 4, 'over')];
  // Under a model, each campaign gets half of the sign-ups the model credits.
  const model = q.get('model');
  if (model) for (const r of data) Object.assign(r, { model_signups: r.signups / 2 + 0.5, model_revenue: [{ currency: 'USD', amount_minor: 24_000 }], model_cost_per_signup_micros: Math.round(r.spend_micros / (r.signups / 2 + 0.5)), model_roas: 1.2 });
  else for (const r of data) Object.assign(r, { model_signups: null, model_revenue: [], model_cost_per_signup_micros: null, model_roas: null });
  return { model: model ?? 'last_paid_click', from: q.get('from'), to: q.get('to'), level: q.get('level') ?? 'campaign', customers: q.get('customers') ?? 'all', window_days: Number(q.get('window_days') ?? 30), target_cost_per_payer_micros: 45_000_000, target_cost_per_signup_micros: null, currency: 'USD', data, daily: previous ? [] : daily(q.get('from'), q.get('to')), notes: [] };
};
const finding = (id, kind, extra) => ({
  id: `40000000-0000-0000-0000-00000000000${id}`, campaign_id: campaign2, campaign_name: 'Generic search', account_id: account, account_name: 'Acme Ads', connector: 'google_ads', currency: 'USD', brand_id: brand,
  kind, status: 'open', reason: '', evidence: { days: 14, spend_micros: 256_000_000, new_payers: 4, cost_per_new_payer_micros: 64_000_000, target_cost_per_payer_micros: 45_000_000, daily_budget_micros: 40_000_000 }, proposed: {},
  change_id: null, test_id: null, decided_by: null, decided_at: null, measure_from: null, measure_until: null, window_note: '', verdict: '', verdict_note: '', result: {}, judged_at: null,
  created_at: '2026-11-08T09:00:00Z', updated_at: '2026-11-08T09:00:00Z', autonomy: 'approve', impact_micros: null, unit: 'payers', shadow: false, applied_auto: false, reverted_at: null, rule: 'cost_over_target', feedback: '', feedback_note: '', feedback_at: null, ...extra,
});
const findings = [
  finding(1, 'budget_down', { reason: "Cut Generic search's budget by 20%?", proposed: { daily_budget_micros: 32_000_000 }, impact_micros: -9_000_000 }),
  finding(2, 'budget_up', { reason: "Raise Brand search's budget?", campaign_id: campaign, campaign_name: 'Brand search', proposed: { daily_budget_micros: 25_000_000 }, impact_micros: 0, evidence: { days: 14, spend_micros: 200_000_000, new_payers: 8, cost_per_new_payer_micros: 25_000_000, target_cost_per_payer_micros: 45_000_000, daily_budget_micros: 20_000_000, days_budget_spent: 9 } }),
  finding(3, 'negative_add', { reason: 'Paused a keyword that spent without a sign-up', status: 'applied', autonomy: 'auto', applied_auto: true, decided_at: '2026-11-09T06:00:00Z', proposed: { keyword: 'free template' } }),
  finding(4, 'alert.sends_failing', { reason: 'Conversions are failing to send', autonomy: 'alert', evidence: {} }),
  finding(5, 'pause', { reason: 'Would have paused Generic search', status: 'shadow', autonomy: 'auto', shadow: true }),
  finding(6, 'budget_down', { reason: 'Cut the budget', status: 'judged', verdict: 'worked', verdict_note: '', measure_from: '2026-10-01', measure_until: '2026-10-21', judged_at: '2026-10-22T00:00:00Z', result: { before: { new_payers: 9, spend_micros: 369_000_000, days: 21 }, after: { new_payers: 14, spend_micros: 532_000_000, days: 21 } } }),
];
const acquisitions = {
  data: [{ id: '50000000-0000-0000-0000-000000000001', record_id: '60000000-0000-0000-0000-000000000001', record_name: 'Ana Example', connector: 'google_ads', ad_account_id: account, campaign: { id: campaign, name: 'Brand search', external_id: '111' }, ad_group: { id: null, name: 'Core', external_id: '9' }, ad: { id: null, name: '', external_id: '77' }, keyword: 'acme', click_id_kind: 'gclid', matched_by: 'template', clicked_at: '2026-11-01T10:00:00Z', signed_up_at: '2026-11-01T10:20:00Z', first_paid_at: '2026-11-03T08:00:00Z', payments: 2, revenue_to_date: [{ currency: 'USD', amount_minor: 9800 }], bucket: 'payer', ad_consent: true, window_days: 30, created_at: '2026-11-01T10:20:00Z' },
    { id: '50000000-0000-0000-0000-000000000002', record_id: '60000000-0000-0000-0000-000000000002', record_name: 'Ben Example', connector: 'google_ads', ad_account_id: account, campaign: { id: null, name: '', external_id: '999' }, ad_group: { id: null, name: '', external_id: '' }, ad: { id: null, name: '', external_id: '' }, keyword: '', click_id_kind: 'gclid', matched_by: 'campaign_param', clicked_at: '2026-10-20T10:00:00Z', signed_up_at: '2026-10-20T10:20:00Z', first_paid_at: null, payments: 0, revenue_to_date: [], bucket: 'signup', ad_consent: true, window_days: 30, created_at: '2026-10-20T10:20:00Z' },
    { id: '50000000-0000-0000-0000-000000000003', record_id: '60000000-0000-0000-0000-000000000003', record_name: 'Cy Example', connector: 'google_ads', ad_account_id: account, campaign: { id: null, name: '', external_id: '' }, ad_group: { id: null, name: '', external_id: '' }, ad: { id: null, name: '', external_id: '' }, keyword: '', click_id_kind: 'gclid', matched_by: 'click_id', clicked_at: '2026-10-21T10:00:00Z', signed_up_at: '2026-10-21T10:20:00Z', first_paid_at: null, payments: 0, revenue_to_date: [], bucket: 'signup', ad_consent: true, window_days: 30, created_at: '2026-10-21T10:20:00Z' }],
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
  // A new search ad waiting for approval: four headlines, three descriptions.
  const draftBodies = [];
  const feedbackBodies = [];
  const newAd = { id: '82000000-0000-0000-0000-000000000001', brand_id: '10000000-0000-0000-0000-000000000001', account_id: '82000000-0000-0000-0000-0000000000aa', account_name: 'Acme Ads', connector: 'google_ads', currency: 'USD', kind: 'ad',
    campaign_id: null, campaign_name: 'Brand search', ad_group_id: null, ad_group_name: 'Acme', reason: 'The one ad there says nothing about agents.', status: 'draft', made: {}, error: '', created_by: null, action_id: null,
    approved_by: null, approved_at: null, made_at: null, live_by: null, live_at: null, created_at: '2026-11-17T09:00:00Z', updated_at: '2026-11-17T09:00:00Z', checks: [],
    content: { kind: 'ad', final_url: 'https://www.acme.example/agents', path1: 'agents', headlines: ['Acme official site', 'Web data for your agents', 'Try it free', 'No charge for failed requests'],
      descriptions: ['Give your agents live web data.', 'Works with every framework.', 'Pay only for what works.'] } };
  let decided = [];
  const removeBodies = [];
  const convBodies = [];
  const webAction = (id, name) => ({ id, name, type: 'webpage', category: 'signup', status: 'enabled', primary: true, counting: 'one', click_window_days: 30, default_value_micros: null, default_currency: '', always_use_default: false,
    snippets: [{ type: 'webpage', page_format: 'html', global_site_tag: `<script>gtag('config', 'AW-1')</script>`, event_snippet: `<script>gtag('event', 'conversion', {'send_to': 'AW-1/${id}'})</script>` }] });
  const convActions = [{ ...webAction('77', 'Demo booked') }, { id: '78', name: 'Offline sale', type: 'upload_clicks', category: 'purchase', status: 'enabled', primary: false, counting: 'many', click_window_days: 90, default_value_micros: null, default_currency: '', always_use_default: false, snippets: [] }];
  // mock answers every API call the page makes for the brand.
  const economics = { brand_id: brand, currency: 'USD', target_cost_per_payer_micros: 45_000_000, target_cost_per_signup_micros: null, acquisition_window_days: 30, payback_months: 12, gross_margin_percent: null, shadow_mode: true, daily_budget_ceiling_micros: 150_000_000,
    target_new_payers_monthly: null, monthly_budget_micros: 600_000_000, target_revenue_monthly_micros: null, target_roas: 2.5, overview_numbers: [], set: true, updated_by: null, created_at: '2026-11-01T00:00:00Z', updated_at: '2026-11-01T00:00:00Z' };
  const economicsBodies = [];
  const holdoutBodies = [];
  const pausedAds = [];
  const mock = (page) => page.route('**/v1/**', async route => {
    const req = route.request(); const u = new URL(req.url()); const q = u.searchParams; const p = u.pathname;
    calls.push(`${req.method()} ${p}${u.search}`);
    let status = 200, body = {};
    if (p === '/v1/workspaces/workspace-a/brands') body = { data: [{ id: brand, name: 'Acme', handle: 'acme', status: 'active' }] };
    else if (p === '/v1/workspaces/workspace-a/ads/connections') body = { data: [{ id: '70000000-0000-0000-0000-000000000001', connector: 'google_ads', name: 'Google Ads', accounts: 1, setup_needed: [], conversion_fields: [], conversion_fields_set: [], last_ok_at: '2026-11-09T08:00:00Z', last_error: null }] };
    else if (p === '/v1/workspaces/workspace-a/ads/accounts' || p === `/v1/brands/${brand}/ads/accounts`) body = { data: [{ id: account, connector: 'google_ads', external_id: '1234567890', name: 'Acme Ads', currency: 'USD', time_zone: 'UTC', status: 'active', brand_id: brand, integration_id: '70000000-0000-0000-0000-000000000001', sync_on: true, last_synced_at: '2026-11-09T08:00:00Z', last_error: null, target_cost_per_conversion_micros: null }] };
    else if (p === `/v1/brands/${brand}/ads/campaigns`) body = { data: campaigns };
    else if (p === `/v1/brands/${brand}/ads/summary`) body = { data: [], totals: [] };
    else if (p === `/v1/brands/${brand}/ads/economics`) { if (req.method() === 'PUT') { economicsBodies.push(JSON.parse(req.postData())); Object.assign(economics, economicsBodies.at(-1)); } body = economics; }
    else if (p === `/v1/brands/${brand}/ads/setup`) body = setup;
    else if (p === `/v1/brands/${brand}/ads/holdouts`) {
      const side = (s, a, v) => ({ signups: s, ad_signups: a, paid_visits: v });
      if (req.method() === 'POST') { status = 201; holdoutBodies.push(JSON.parse(req.postData())); body = { id: '90000000-0000-0000-0000-000000000002', ...holdoutBodies.at(-1), status: 'scheduled', created_at: '2026-11-19T00:00:00Z', result: { days: 0, before_from: '', through: '', held_before: side(0, 0, 0), held_during: side(0, 0, 0), rest_before: side(0, 0, 0), rest_during: side(0, 0, 0), expected_signups: null, lift_signups: null, incrementality: null, leaked: false, verdict: 'scheduled' } }; }
      else body = { data: [{ id: '90000000-0000-0000-0000-000000000001', name: 'Portugal without ads', connector: '', countries: ['PT'], starts_on: '2026-10-20', ends_on: '2026-11-16', status: 'finished', created_at: '2026-10-19T00:00:00Z', result: { days: 28, before_from: '2026-09-22', through: '2026-11-16', held_before: side(100, 50, 400), held_during: side(60, 0, 3), rest_before: side(200, 30, 900), rest_during: side(200, 30, 910), expected_signups: 100, lift_signups: 40, incrementality: 0.8, leaked: false, verdict: 'ads_add' } }] };
    }
    else if (p === `/v1/brands/${brand}/ads/business`) {
      const week = (w, revenue, meta, google) => ({ week: w, revenue_micros: revenue * 1e6, spend_micros: (meta + google) * 1e6, spend: [{ connector: 'meta_ads', spend_micros: meta * 1e6 }, { connector: 'google_ads', spend_micros: google * 1e6 }] });
      const cohort = (month, people, cost, each, days, estimated) => ({ month, new_customers: people, spend_micros: people * cost * 1e6, cost_each_micros: cost * 1e6, revenue_each_micros: each.map((v) => (v === null ? null : v * 1e6)), payback_days: days, payback_estimated: estimated });
      body = { from: q.get('from'), to: q.get('to'), currency: 'USD', target_roas: 3.5, cohort_days: [30, 60, 90, 120],
        totals: { revenue_micros: 41280000000, spend_micros: 10584000000, revenue_per_spend: 3.9, new_customers: 96, new_customers_from_ads: 58, cost_per_new_customer_micros: 110250000 },
        previous: { revenue_micros: 34920000000, spend_micros: 9948000000, revenue_per_spend: 3.51, new_customers: 84, new_customers_from_ads: 50, cost_per_new_customer_micros: 118430000 },
        weeks: [week('2026-10-19', 8900, 1800, 600), week('2026-10-26', 9700, 1900, 700), week('2026-11-02', 10900, 2000, 800), week('2026-11-09', 11780, 2000, 784)],
        networks: [
          { connector: 'meta_ads', spend_micros: 7700000000, share: 0.727, platform_conversions: 57, new_payers: 37, cost_per_new_payer_micros: 208108108 },
          { connector: 'google_ads', spend_micros: 2884000000, share: 0.273, platform_conversions: 30, new_payers: 21, cost_per_new_payer_micros: 137333333 },
        ],
        cohorts: [cohort('2026-07', 61, 131, [96, 148, 201, 262], 49, false), cohort('2026-08', 70, 126, [102, 160, 214, null], 45, false), cohort('2026-09', 78, 122, [108, 171, null, null], 37, false), cohort('2026-10', 84, 118, [112, null, null, null], 32, true), cohort('2026-11', 96, 110, [null, null, null, null], null, false)],
        left_out_currencies: [] };
    }
    else if (p === `/v1/brands/${brand}/ads/creatives/results`) {
      const step = (id, count, base, usual, grade) => ({ id, count, base, rate: base ? count / base : null, usual, grade });
      const row = (ext, name, connector, more) => ({ id: `91000000-0000-0000-0000-00000000000${ext}`, level: 'ad', name, external_id: `a${ext}`, status: 'active', connector, ad_account_id: account, account_name: 'Acme Ads', campaign_id: campaigns[0].id, campaign_name: 'Brand search', currency: 'USD', account_currency: 'USD', clicks: 0, impressions: 20000, platform_conversions: null, platform_all_conversions: null, signups: 0, new_payers: 0, cost_per_signup_micros: null, cost_per_new_payer_micros: null, target_status: '', new_revenue: [], returning_revenue: [], lifetime_revenue: [], roas: null, payback_months: null, previous_spend_micros: 0, spend_change: null, trend: '', window_days: 30, model_signups: null, model_revenue: [], model_cost_per_signup_micros: null, model_roas: null, type: 'image', headline: '', text: '', url: '', thumbnail_url: '', video: false, weakest_step: '', ...more });
      body = { from: q.get('from'), to: q.get('to'), currency: 'USD', target_cost_per_payer_micros: 45000000, summary: { total: 4, scale: 1, keep: 0, fix: 1, stop: 1, too_early: 1 }, data: [
        row(1, 'Countries map', 'meta_ads', { spend_micros: 212000000, new_payers: 11, signups: 60, cost_per_new_payer_micros: 18400000, roas: 4.6, headline: '190+ countries. One API.', verdict: 'scale', steps: [step('click', 600, 20000, 0.02, 'A'), step('sign_up', 60, 600, 0.07, 'A'), step('pay', 11, 60, 0.1, 'A')] }),
        row(2, 'First request video', 'meta_ads', { spend_micros: 98000000, new_payers: 3, signups: 20, cost_per_new_payer_micros: 32700000, roas: 2.1, video: true, type: 'video', verdict: 'fix', weakest_step: 'hook', steps: [step('hook', 2800, 20000, 0.3, 'D'), step('hold', 1150, 2800, 0.4, 'B'), step('click', 300, 20000, 0.02, 'C'), step('sign_up', 20, 300, 0.07, 'B'), step('pay', 3, 20, 0.1, 'A')] }),
        row(3, 'Price table', 'google_ads', { spend_micros: 120000000, new_payers: 0, signups: 2, roas: 0, verdict: 'stop', weakest_step: 'sign_up', steps: [step('click', 400, 20000, 0.02, 'B'), step('sign_up', 2, 400, 0.07, 'D'), step('pay', 0, 2, 0.1, '')] }),
        row(4, 'Just launched', 'meta_ads', { spend_micros: 5000000, impressions: 300, verdict: 'too_early', steps: [step('click', 4, 300, 0.02, ''), step('sign_up', 0, 4, 0.07, ''), step('pay', 0, 0, 0.1, '')] }),
      ] };
    }
    else if (p === `/v1/brands/${brand}/ads/items/91000000-0000-0000-0000-000000000003/pause` && req.method() === 'POST') { status = 202; pausedAds.push(p); body = { queued: true }; }
    else if (p === `/v1/brands/${brand}/ads/match-check`) body = { days: 30, window_days: 30, since: '2026-10-20T00:00:00Z', visits: 4200, visits_usable: 3990, browsers: 3100, browsers_unique: 2480, people: 260, people_credited: 60, would_credit: 24, ambiguous: 9, no_match: 167, known_pairs: 50, known_pairs_right: 47, accuracy: 0.94, verdict: 'worth_testing' };
    else if (p === `/v1/brands/${brand}/ads/performance`) body = performance(q);
    else if (p === `/v1/brands/${brand}/ads/acquisitions`) body = acquisitions;
    else if (p === `/v1/brands/${brand}/knowledge`) body = sheet(reviewed);
    else if (p === `/v1/brands/${brand}/knowledge/review` && req.method() === 'POST') { reviewed = true; body = sheet(true); }
    else if (p === `/v1/brands/${brand}/ads/drafts`) body = { data: [newAd] };
    else if (p === `/v1/brands/${brand}/ads/drafts/${newAd.id}` && req.method() === 'PATCH') { draftBodies.push(JSON.parse(req.postData())); body = newAd; }
    else if (p === `/v1/brands/${brand}/ads/accounts/${account}/conversion-actions` && req.method() === 'POST') { status = 202; convBodies.push(JSON.parse(req.postData())); convActions.push(webAction('79', 'Trial started')); body = { queued: true, change: { id: '81000000-0000-0000-0000-000000000001', kind: 'conversion_action_create', status: 'queued' } }; }
    else if (p === `/v1/brands/${brand}/ads/accounts/${account}/conversion-actions`) body = { data: convActions, categories: ['purchase', 'sign_up', 'lead', 'page_view', 'contact', 'other'] };
    else if (p === `/v1/brands/${brand}/ads/accounts/${account}/changes/81000000-0000-0000-0000-000000000001`) body = { id: '81000000-0000-0000-0000-000000000001', kind: 'conversion_action_create', status: 'applied', result: { conversion_action_id: '79' }, error: '' };
    else if (p === '/v1/workspaces/workspace-a/ads/conversion-rules' || p === '/v1/workspaces/workspace-a/ads/conversion-sends') body = { data: [] };
    else if (p === `/v1/brands/${brand}/ads/settings`) body = { consent_mode: 'opt_out' };
    else if (p === `/v1/brands/${brand}/ads/bidding-strategies`) body = { from: q.get('from'), to: q.get('to'), errors: [], notes: [], data: [{ id: '900', name: 'Leads tCPA', type: 'target_cpa', status: 'enabled', target_cpa_micros: 40_000_000, target_roas: null, campaign_count: 2, currency: 'USD', ad_account_id: account, account_name: 'Acme Ads', connector: 'google_ads', spend_micros: 80_000_000, impressions: 900, clicks: 40, cpc_micros: 2_000_000, conversions: 4, all_conversions: 6, cost_per_conversion_micros: 20_000_000 }] };
    else if (/\/ads\/campaigns\/[^/]+\/pause$/.test(p) && req.method() === 'POST') { status = 202; body = { queued: true, change: { id: '80000000-0000-0000-0000-000000000002', kind: 'campaign_pause', status: 'queued' } }; }
    else if (p === `/v1/brands/${brand}/ads/campaigns/${campaign2}/remove` && req.method() === 'POST') { status = 202; body = { queued: true, change: { id: '80000000-0000-0000-0000-000000000001', kind: 'campaign_remove', status: 'queued' } }; removeBodies.push(req.postData()); }
    else if (p === `/v1/brands/${brand}/attribution/ads`) body = { data: [] };
    else if (p === `/v1/brands/${brand}/attribution/models`) body = { data: [] };
    else if (p === `/v1/brands/${brand}/ads/findings`) body = { data: findings.filter(f => !decided.includes(f.id) && (!q.get('status') || f.status === q.get('status'))) };
    else if (p.startsWith(`/v1/brands/${brand}/ads/findings/`) && p.endsWith('/feedback') && req.method() === 'POST') { const f = findings.find(x => x.id === p.split('/').at(-2)); feedbackBodies.push(JSON.parse(req.postData())); Object.assign(f, { feedback: feedbackBodies.at(-1).rating, feedback_note: feedbackBodies.at(-1).note ?? '', feedback_at: '2026-11-10T09:00:00Z' }); body = f; }
    else if (findings.some(f => p === `/v1/brands/${brand}/ads/findings/${f.id}`)) body = findings.find(f => p.endsWith(f.id));
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
    const tile = (id, part) => page.getByTestId(`number-${id}${part ? `-${part}` : ''}`);
    await tile('cost_per_payer', 'value').waitFor();
    assert.equal(await tile('cost_per_payer', 'value').textContent(), '$38.00');
    assert.equal(await page.getByTestId('hero-sentence').textContent(), '12 new customers at $38.00 each, under your $45.00 target.');
    // Every number against the days before, with the old number: $456 over
    // 12 customers against $420 over 10 is $38 against $42, down 9.5%.
    assert.equal(await tile('cost_per_payer', 'change').textContent(), 'down9.5%was $42.00');
    assert.match(await tile('cost_per_payer', 'target').textContent(), /^Under target · \$45\.00$/);
    // The six usual numbers, in order.
    assert.deepEqual(await page.locator('[data-testid^="number-"][title]').evaluateAll(els => els.filter(e => /^number-[a-z_]+$/.test(e.dataset.testid)).map(e => e.dataset.testid.slice(7))),
      ['cost_per_payer', 'cost_per_signup', 'new_payers', 'spend', 'revenue', 'roas']);
    // Revenue and its return beside the spend: $1,200 over $456 is 2.6×,
    // against $1,200 over $420 (2.9×) the days before, on a 2.5× target.
    assert.equal(await tile('revenue', 'value').textContent(), '$1,200.00');
    assert.equal(await tile('revenue', 'change').textContent(), 'No change');
    assert.equal(await tile('roas', 'value').textContent(), '2.6×');
    assert.equal(await tile('roas', 'change').textContent(), 'down7.9%was 2.9×');
    assert.match(await tile('roas', 'target').textContent(), /^On target · 2\.5×$/);
    // A monthly budget of $600 is $560 for 28 days: $456 is inside it.
    assert.match(await tile('spend', 'target').textContent(), /^Inside budget · \$560\.00$/);
    // A number without a target offers to set one.
    assert.match(await tile('new_payers', 'target').textContent(), /^No target set · Set one$/);
    // Compared with the same days a year before asks for those days.
    const beforeCompare = calls.length;
    await page.getByLabel('Compared with').selectOption('year');
    await tile('cost_per_payer', 'value').waitFor();
    const lastYear = new Date(`${today}T12:00:00Z`); lastYear.setUTCFullYear(lastYear.getUTCFullYear() - 1);
    for (let i = 0; i < 50 && !calls.slice(beforeCompare).some(c => c.includes('/ads/performance?') && c.includes(`to=${lastYear.toISOString().slice(0, 10)}`)); i++) await page.waitForTimeout(100);
    assert(calls.slice(beforeCompare).some(c => c.includes('/ads/performance?') && c.includes(`to=${lastYear.toISOString().slice(0, 10)}`)), calls.slice(beforeCompare).join('\n'));
    await page.getByLabel('Compared with').selectOption('previous');
    await tile('cost_per_payer', 'change').getByText('was $42.00').waitFor();
    // Choose numbers: hide one, show another, set a target, and Save sends
    // exactly that; Cancel sends nothing.
    await page.getByRole('button', { name: 'Choose numbers' }).click();
    const panel = page.getByRole('dialog', { name: 'Choose your numbers' });
    await panel.getByText('6 of 8 shown').waitFor();
    await panel.getByRole('checkbox', { name: 'Show Spend' }).uncheck();
    await panel.getByRole('button', { name: 'Cancel' }).click();
    await panel.waitFor({ state: 'hidden' });
    assert.equal(economicsBodies.length, 0);
    await page.getByRole('button', { name: 'Choose numbers' }).click();
    await panel.getByRole('checkbox', { name: 'Show Spend' }).uncheck();
    await panel.getByRole('checkbox', { name: 'Show Clicks' }).check();
    await panel.getByRole('button', { name: 'Move Clicks up' }).click();
    await panel.getByLabel('New customers a month').fill('15');
    await panel.getByRole('button', { name: 'Save' }).click();
    await panel.waitFor({ state: 'hidden' });
    assert.deepEqual(economicsBodies.at(-1), { currency: 'USD', overview_numbers: ['cost_per_payer', 'cost_per_signup', 'new_payers', 'revenue', 'clicks', 'roas'], target_new_payers_monthly: 15 }, JSON.stringify(economicsBodies));
    await tile('clicks', 'value').waitFor();
    assert.equal(await tile('clicks', 'value').textContent(), '800');
    assert.equal(await tile('spend').count(), 0);
    // 15 a month is 14 for 28 days: 12 is within a tenth under it.
    assert.match(await tile('new_payers', 'target').textContent(), /^Under target · 14$/);
    // Back to the usual six for the rest of the run.
    Object.assign(economics, { overview_numbers: [], target_new_payers_monthly: null });
    await page.reload();
    await tile('spend', 'value').waitFor();
    await shot(page, 'number');
    assert(calls.some(c => c.includes('/ads/performance?') && c.includes('window_days=30') && c.includes('customers=all')), calls.join('\n'));
    // Seven tabs, with what waits for a decision counted on Actions: two
    // proposals and one alert.
    assert.deepEqual(await page.getByRole('tablist', { name: 'Paid ads screens' }).getByRole('tab').allTextContents(), ['Overview', 'Whole business', 'Actions3', 'Campaigns', 'Creatives', 'Customers', 'Audiences', 'Experiments', 'Settings']);
    await page.getByRole('button', { name: 'See the 3 recommended actions' }).waitFor();
    // The one account is plain text, not a picker.
    await page.getByText(/^Google Ads · Acme Ads · USD · synced /).waitFor();
    // The brand's own name is judged apart from everything else, on the
    // same rows as the number: $200 for 8 customers, $256 for 4.
    const split = page.locator('[data-slot=card]', { hasText: 'Searches for Acme against everything else' }).getByRole('table');
    assert.match(await split.locator('tr', { hasText: 'Searches for Acme' }).textContent(), /1 campaign\$200\.00308\$25\.00Under/);
    assert.match(await split.locator('tr', { hasText: 'Everything else' }).textContent(), /1 campaign\$256\.00124\$64\.00Too few to judge/);
    // Do this next: the open actions, biggest effect first.
    const next = page.locator('[data-slot=card]', { hasText: 'Do this next' });
    assert.equal(await next.getByRole('table').locator('tbody tr').count(), 3);
    await next.getByRole('table').getByText("Cut Generic search's budget by 20%?").waitFor();
    await next.getByRole('table').getByText('about $9.00 less per new customer').waitFor();
    // The funnel: 800 clicks, 42 sign-ups, 12 first payments.
    // Cost per new customer against the target, from the same read.
    const chart = page.locator('[data-slot=card]', { hasText: 'Cost per new customer against your target' });
    await chart.getByTestId('ads-cost-chart').locator('svg').first().waitFor();
    await chart.getByText('Your target').first().waitFor();
    const funnel = await page.getByTestId('ads-funnel').locator('li').allTextContents();
    assert.match(funnel[0], /^Ad clicks\$456\.00 spent800$/);
    assert.match(funnel[1], /^Sign-ups5\.3% of clicks · \$10\.86 each42$/);
    assert.match(funnel[2], /^First payment28\.6% of sign-ups · \$38\.00 each12$/);
    await shot(page, 'overview');
    // Creatives holds its own pages, each one click from any other, and
    // opens on Results; Start here leads the row for a brand's first ad.
    await page.getByRole('tab', { name: 'Creatives' }).click();
    const creativePages = page.getByRole('navigation', { name: 'Creatives pages' }).getByRole('button');
    await creativePages.first().waitFor();
    assert.deepEqual(await creativePages.allTextContents(), ['Start here', 'Results', 'Library', 'Talking videos', 'Skills and prompts', 'Brand kit']);
    assert.equal(await creativePages.nth(1).getAttribute('aria-current'), 'page');
    await creativePages.nth(3).click();
    await page.locator('nav[aria-label="Creatives pages"] button[aria-current="page"]', { hasText: 'Talking videos' }).waitFor();
    assert.equal(await page.getByRole('tab', { selected: true }).first().textContent(), 'Creatives');
    await page.getByRole('tab', { name: 'Overview' }).click();
    await page.getByTestId('number-cost_per_payer-value').waitFor();
    // The counting window and which customers' revenue counts are choices
    // on Setup; the number follows them.
    await page.getByRole('tab', { name: 'Settings' }).click();
    const before = calls.length;
    await page.getByLabel('Revenue counted').selectOption('new');
    await page.getByLabel('Counting window').selectOption('7');
    await page.getByRole('tab', { name: 'Overview' }).click();
    await page.getByTestId('number-cost_per_payer-value').waitFor();
    assert(calls.slice(before).some(c => c.includes('/ads/performance?') && c.includes('customers=new') && c.includes('window_days=7')), calls.slice(before).join('\n'));
    // One date menu, with today in it.
    const beforeToday = calls.length;
    await page.getByRole('button', { name: 'Date range' }).click();
    assert.deepEqual(await page.getByRole('menuitem').allTextContents(), ['Today', 'Yesterday', 'Last 7 days', 'Last 28 days', 'Last 30 days', 'Last 90 days', 'This month', 'Last month', 'Custom range']);
    await page.getByRole('menuitem', { name: 'Today' }).click();
    for (let i = 0; i < 50 && !calls.slice(beforeToday).some(c => c.includes('/ads/performance?') && c.includes(`from=${today}`)); i++) await page.waitForTimeout(100);
    assert(calls.slice(beforeToday).some(c => c.includes('/ads/performance?') && c.includes(`from=${today}`) && c.includes(`to=${today}`)), calls.slice(beforeToday).join('\n'));
    // The number at the top follows the network picked at the top of the
    // page, like every table under it.
    const scopeAt = await page.evaluate(() => { const k = Object.keys(sessionStorage).find((x) => x.startsWith('outflare.ads.scope.')) ?? 'outflare.ads.scope.workspace-a'; sessionStorage.setItem(k, JSON.stringify({ network: 'google_ads' })); return k; });
    const beforeScope = calls.length;
    await page.goto(`${origin}/tests/fixtures/ads.html`);
    for (let i = 0; i < 50 && !calls.slice(beforeScope).some(c => c.includes('/ads/performance?')); i++) await page.waitForTimeout(100);
    assert(calls.slice(beforeScope).filter(c => c.includes('/ads/performance?')).every(c => c.includes('connector=google_ads')), calls.slice(beforeScope).join('\n'));
    await page.evaluate((k) => sessionStorage.removeItem(k), scopeAt);
    // Older links land on the new tabs.
    for (const [old, now] of [['inbox', 'Actions3'], ['results', 'Campaigns'], ['verdicts', 'Experiments'], ['tests', 'Experiments'], ['sends', 'Settings']]) {
      await page.goto(`${origin}/tests/fixtures/ads.html?item=${old}`);
      await page.getByRole('tablist', { name: 'Paid ads screens' }).waitFor();
      assert.equal(await page.getByRole('tab', { selected: true }).first().textContent(), now, old);
    }
    // Settings' pages sit in a row under the tabs: from the send log, any
    // other is one click away, and so is the way back.
    const pagesNav = page.getByRole('navigation', { name: 'Settings pages' });
    assert.deepEqual(await pagesNav.getByRole('button').allTextContents(), ['General', 'Tracking', 'Send conversions', 'Send log', 'Ad accounts', 'Change history']);
    assert.equal(await pagesNav.locator('[aria-current=page]').textContent(), 'Send log');
    await pagesNav.getByRole('button', { name: 'General' }).click();
    await page.getByLabel('Counting window').waitFor();
    assert.equal(await pagesNav.locator('[aria-current=page]').textContent(), 'General');
    // Tracking has the dry run of matching visits by network and device:
    // nothing is read until it is run, then the verdict and its numbers.
    await pagesNav.getByRole('button', { name: 'Tracking' }).click();
    await page.getByTestId('match-check-run').waitFor();
    assert.ok(!calls.some(c => c.includes('/ads/match-check')));
    await page.getByTestId('match-check-run').click();
    await page.getByTestId('match-check').getByText('Worth testing').waitFor();
    assert.match(await page.getByTestId('match-check-would-credit').textContent(), /^24more people.*12% of the 200 known people/);
    assert.match(await page.getByTestId('match-check-accuracy').textContent(), /^94%.*47 of 50 pairs/);
    assert.match(await page.getByTestId('match-check-ambiguous').textContent(), /^9people/);
    assert.ok(calls.includes(`GET /v1/brands/${brand}/ads/match-check?days=30`));
    await pagesNav.getByRole('button', { name: 'Ad accounts' }).click();
    await page.getByPlaceholder('Search accounts').waitFor();
    await page.getByRole('tab', { name: 'Settings' }).click();
    await page.getByLabel('Counting window').waitFor();
    assert.deepEqual(errors, []);
    await page.close();
  }

  // 2. Actions: one table. Approve on a saving takes the row away and
  // posts the approval; a raise asks first; an alert opens its fix.
  {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    await mock(page);
    await page.goto(`${origin}/tests/fixtures/ads.html?item=actions`);
    const table = page.getByRole('table').filter({ hasText: 'Why' }).first();
    const cut = table.getByText("Cut Generic search's budget by 20%?");
    await cut.waitFor();
    await table.getByText('About $9.00 less per new customer').waitFor();
    await table.getByText('Last 14 days, $256.00 spent, 4 new customers at $64.00 each, target $45.00').first().waitFor();
    await page.getByRole('tab', { name: 'To do · 3' }).waitFor();
    await page.getByRole('tab', { name: 'Done · 2' }).waitFor();
    await page.getByText('Approving every raise adds at most $152.00 a month.').waitFor();
    assert.equal(await page.getByRole('button', { name: 'Acknowledge' }).count(), 0, 'no bare Acknowledge');
    // A new ad reads as it will in Google's results: three headlines, the
    // site and its path, two descriptions, and how many more there are.
    const newAds = page.locator('[data-slot=card]', { hasText: 'New ads' });
    const preview = newAds.locator('[data-ad-preview]').first();
    await preview.getByText('Acme official site | Web data for your agents | Try it free').waitFor();
    await preview.getByText('www.acme.example/agents').waitFor();
    await preview.getByText('Give your agents live web data. Works with every framework.').waitFor();
    assert.equal(await preview.getByText('+1 more').count(), 2);
    // The whole ad is listed under it from the start, each line with its length.
    await preview.getByText('No charge for failed requests').waitFor();
    await preview.getByText('18/30').waitFor();
    await shot(page, 'actions');
    // Edit opens the text beside its preview; a line that is too long
    // can't be saved, and Save sends the whole ad.
    await preview.getByRole('button', { name: 'Edit' }).click();
    const edit = page.locator('[data-slot=card]', { hasText: 'Edit this ad' });
    const heads = edit.getByLabel('Headlines');
    await heads.fill('Acme official site\nWeb data for your AI agents, all of them');
    await edit.getByText(/is \d+ characters; cut \d+\./).waitFor();
    await edit.getByText('1 line is too long for Google').waitFor();
    assert.equal(await edit.getByRole('button', { name: 'Save' }).isDisabled(), true);
    await heads.fill('Acme official site\nWeb data for AI agents');
    await edit.locator('[data-ad-preview]').getByText('Acme official site | Web data for AI agents').waitFor();
    await shot(page, 'edit-ad');
    await edit.getByRole('button', { name: 'Save' }).click();
    await newAds.waitFor();
    assert.deepEqual(draftBodies.map(b => [b.headlines, b.descriptions.length, b.final_url, b.path1, b.reason]), [[['Acme official site', 'Web data for AI agents'], 3, 'https://www.acme.example/agents', 'agents', 'The one ad there says nothing about agents.']]);
    // The saving's Approve needs no confirmation.
    await table.getByRole('row', { name: /Cut Generic search's budget by 20%\?/ }).getByRole('button', { name: 'Approve', exact: true }).click();
    await cut.waitFor({ state: 'hidden' });
    assert(calls.some(c => c === `POST /v1/brands/${brand}/ads/findings/${findings[0].id}/approve`), calls.join('\n'));
    // A raise can spend more, so it asks first, with the budget and the most it can add.
    await table.getByRole('row', { name: /Raise Brand search's budget\?/ }).getByRole('button', { name: 'Approve', exact: true }).click();
    const ask = page.getByRole('alertdialog');
    await ask.getByText('This change can spend more').waitFor();
    await ask.getByText('Daily budget: $20.00 → $25.00.').waitFor();
    await ask.getByText('The most it can add: $152.00 a month.').waitFor();
    await ask.getByRole('button', { name: 'Approve' }).click();
    await table.getByText("Raise Brand search's budget?").waitFor({ state: 'hidden' });
    assert(calls.some(c => c === `POST /v1/brands/${brand}/ads/findings/${findings[1].id}/approve`), calls.join('\n'));
    // Done lists what was applied, with Undo; Preview what was kept back.
    await page.getByRole('tab', { name: /^Done/ }).click();
    await table.getByRole('row', { name: /Paused a keyword/ }).getByRole('button', { name: 'Undo' }).waitFor();
    await page.getByRole('tab', { name: /^Preview/ }).click();
    await table.getByText('Would have paused Generic search').waitFor();
    // A row opens the action's own page: the numbers behind it, and Good
    // advice or Bad advice with a reason, which is saved for the brand.
    await page.getByRole('tab', { name: /^To do/ }).click();
    await table.getByRole('button', { name: 'Conversions are failing to send', exact: true }).waitFor();
    await page.getByRole('tab', { name: /^Done/ }).click();
    await table.getByRole('button', { name: 'Cut the budget', exact: true }).click();
    assert.deepEqual(await page.evaluate(() => window.gone.at(-1)), ['ads', `actions/${findings[5].id}`]);
    const action = page.locator('section[aria-label=Action]');
    await action.getByRole('heading', { name: 'Cut the budget' }).waitFor();
    await action.getByText('Result: worked').waitFor();
    await page.locator('[data-slot=card]', { hasText: 'Why Outflare recommends this' }).waitFor();
    const advice = page.locator('[data-slot=card]', { hasText: 'Was this good advice?' });
    assert.equal(await advice.getByRole('button', { name: 'Save feedback' }).isDisabled(), true);
    await advice.getByLabel(/Why\?/).fill('Never cut brand campaigns.');
    await advice.getByRole('button', { name: 'Bad advice' }).click();
    await advice.getByRole('button', { name: 'Saved', exact: true }).waitFor();
    assert.deepEqual(feedbackBodies, [{ rating: 'bad', note: 'Never cut brand campaigns.' }]);
    assert.equal(await advice.getByRole('button', { name: 'Bad advice' }).getAttribute('aria-pressed'), 'true');
    await shot(page, 'action');
    await page.getByRole('button', { name: '← Actions' }).click();
    await table.waitFor();
    // An alert's button opens the page that fixes it.
    await page.getByRole('tab', { name: /^To do/ }).click();
    const alertRow = table.getByRole('row', { name: /Conversions are failing to send/ });
    await alertRow.getByRole('button', { name: 'Open the send log' }).click();
    assert.deepEqual(await page.evaluate(() => window.gone.at(-1)), ['ads', 'sends']);
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
    // The status table: a real status on each line, problems first.
    const lines = page.locator('[data-slot=card]', { hasText: 'Everything that has to be right' }).getByRole('table');
    const first = lines.getByRole('row').nth(1);
    await first.getByText('Fix', { exact: true }).waitFor();
    await first.getByText('Conversions sent to the ad networks').waitFor();
    await first.getByText('No conversion rule yet.').waitFor();
    await lines.getByRole('row', { name: /Target cost per new customer/ }).getByText('$45.00').waitFor();
    await lines.getByRole('row', { name: /Limits/ }).getByText('Total daily budget never above $150.00 without asking you twice', { exact: false }).waitFor();
    await lines.getByRole('row', { name: /Send log/ }).getByText('No conversion was refused in the last 7 days.').waitFor();
    await lines.getByRole('row', { name: /Tracking/ }).getByText('Working', { exact: true }).waitFor();
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
    await page.getByRole('heading', { name: 'Campaigns', exact: true }).waitFor();
    await page.getByText('Generic search').first().waitFor();
    await page.getByText('$200.00 spent · 3.0× ROAS').first().waitFor();
    const wide = await page.evaluate(() => {
      const w = document.documentElement.clientWidth;
      return [...document.querySelectorAll('*')].filter(e => e.getBoundingClientRect().right > w + 1).slice(0, 8).map(e => `${e.tagName.toLowerCase()}.${String(e.className).slice(0, 60)}`);
    });
    assert.deepEqual(wide, [], 'the results screen scrolls sideways on a phone');
    await shot(page, 'results-phone');
    await page.getByRole('tab', { name: 'Customers' }).click();
    await page.getByText('Ana Example').first().waitFor();
    await page.getByText('Brand search › Core › [acme]').first().waitFor();
    await page.getByText('A campaign not in the connected ad account').first().waitFor();
    await page.getByText('Not matched to a campaign').first().waitFor();
    await shot(page, 'customers-phone');
    // Whole business: all revenue against all ad spend, what the networks
    // claim beside what was counted, and payback by the month of first payment.
    await page.getByRole('tab', { name: 'Whole business' }).click();
    assert.equal(await page.getByTestId('business-headline').textContent(), 'Every $1 of ads brought back $3.90 in revenue, up from $3.51.');
    assert.match(await page.getByTestId('business-revenue').textContent(), /Total revenue\$41,280\.00.*18\.2%.*was \$34,920\.00/s);
    assert.match(await page.getByTestId('business-ratio').textContent(), /\$3\.90.*Over target · \$3\.50/s);
    assert.match(await page.getByTestId('business-new').textContent(), /96.*58 from ads, 38 from elsewhere/s);
    assert.match(await page.getByTestId('business-cost').textContent(), /\$110\.25.*6\.9%/s);
    assert.match(await page.getByTestId('business-networks').textContent(), /Meta.*\$7,700\.00.*73%.*57.*37.*All networks.*87.*58.*\$182\.48/s);
    assert.equal(await page.getByTestId('business-claim').textContent(), 'The networks claim 87 between them. Outflare traced 58 new customers to ads.');
    assert.match(await page.getByTestId('business-cohort-2026-07').textContent(), /July.*61.*\$131\.00.*\$96\.00.*\$148\.00.*\$201\.00.*\$262\.00.*49 days/s);
    assert.match(await page.getByTestId('business-cohort-2026-10').textContent(), /October.*not yet.*about 32 days/s);
    assert.match(await page.getByTestId('business-cohort-2026-11').textContent(), /too early/);
    assert.equal(await page.getByTestId('business-weeks').locator('rect').count(), 8);
    await shot(page, 'business-phone');
    await page.setViewportSize({ width: 1400, height: 900 });
    await shot(page, 'business-wide');
    await page.setViewportSize({ width: 390, height: 844 });
    // Creatives, Results: every ad with its verdict, a grade per step and why,
    // filtered by verdict, as a gallery or a list (remembered), and paused
    // from its card after a confirmation.
    await page.getByRole('tab', { name: 'Creatives' }).click();
    assert.deepEqual(await page.getByRole('navigation', { name: 'Creatives pages' }).getByRole('button').allTextContents(), ['Start here', 'Results', 'Library', 'Talking videos', 'Skills and prompts', 'Brand kit']);
    assert.equal(await page.getByTestId('creative-headline').textContent(), '1 to scale, 1 to fix and 1 to stop, out of 4 running.');
    assert.match(await page.getByTestId('creative-a1').textContent(), /★ Scale it.*Countries map.*Click\s*A\s*3\.0%.*11 new customers at \$18\.40 each, under your \$45\.00 target/s);
    assert.match(await page.getByTestId('creative-a2').textContent(), /Fix the opening.*Hook\s*D\s*14%.*Only 14% watch past the opening; your ads usually get 30%/s);
    assert.match(await page.getByTestId('creative-a3').textContent(), /Stop it.*\$120\.00 spent and no new customer, against your \$45\.00 target/s);
    assert.match(await page.getByTestId('creative-a4').textContent(), /Too early.*Click\s*–\s*4/s);
    await shot(page, 'creatives-phone');
    await page.getByRole('button', { name: /^Stop it 1$/ }).click();
    assert.equal(await page.locator('[data-testid^="creative-a"]').count(), 1);
    await page.getByTestId('creative-a3').getByRole('button', { name: 'Pause' }).click();
    await page.getByRole('button', { name: 'Pause the ad' }).click();
    await page.getByText('Pausing the ad.').first().waitFor();
    assert.equal(pausedAds.length, 1);
    await page.getByRole('button', { name: /^All 4$/ }).click();
    await page.getByRole('group', { name: 'View' }).getByRole('button', { name: 'List' }).click();
    await page.getByRole('columnheader', { name: 'Verdict' }).waitFor();
    assert.equal(await page.evaluate(() => Object.entries(localStorage).find(([k]) => k.startsWith('outflare.ads.creatives.view.'))?.[1]), 'list');
    await page.getByRole('group', { name: 'View' }).getByRole('button', { name: 'Gallery' }).click();
    await page.getByRole('tab', { name: 'Experiments' }).click();
    await page.getByText('This cut worked: 14 new customers at $38.00 each, before 9 at $41.00.').first().waitFor();
    await page.getByText('Results of past changes').first().waitFor();
    await page.getByRole('heading', { name: 'Experiments' }).first().waitFor();
    // Holdout tests: a finished one says what the ads were adding, and a new
    // one is saved with its countries picked by name.
    await page.getByText('The ads add sign-ups').first().waitFor();
    await page.getByText('With the ads off, these countries had 60 sign-ups against 100 expected: the ads were adding about 40. That is 80% of the 50 that ads were credited with there before.').first().waitFor();
    await page.getByTestId('holdout-new').click();
    await page.getByTestId('holdout-form').waitFor();
    assert.ok(await page.getByTestId('holdout-form').getByRole('button', { name: 'Save' }).isDisabled());
    await page.getByTestId('holdout-countries').click();
    await page.getByPlaceholder('Search countries').fill('spai');
    await page.getByRole('option', { name: 'Spain' }).click();
    await page.keyboard.press('Escape');
    await page.getByTestId('holdout-form').getByRole('button', { name: 'Save' }).click();
    await page.getByTestId('holdout-form').waitFor({ state: 'detached' });
    assert.equal(holdoutBodies.length, 1);
    assert.deepEqual([holdoutBodies[0].countries, holdoutBodies[0].name, holdoutBodies[0].connector], [['ES'], 'Spain without ads', '']);
    assert.match(holdoutBodies[0].starts_on, /^\d{4}-\d\d-\d\d$/);
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
    // Campaigns: budget, spend, sign-ups, customers and cost per customer
    // against the target by default, with a total that adds up.
    const results = page.locator('[data-slot=card]').filter({ has: page.getByRole('heading', { name: 'Campaigns', exact: true }) });
    await results.locator('table').getByText('Generic search').first().waitFor();
    const shownHeads = (await results.getByRole('columnheader').allTextContents()).filter(Boolean);
    assert.deepEqual(shownHeads, ['Campaign', 'Status', 'Optimization score', 'Room to grow', 'Budget', 'Spend', 'Sign-ups', 'New customers', 'Cost per customer', 'Against target', 'Network says'], shownHeads.join(' | '));
    // An enabled campaign says whether it is really serving, in Google's
    // words, and shows Google's Optimization score.
    await results.locator('tr', { hasText: 'Generic search' }).getByText('Limited by budget').waitFor();
    await results.locator('tr', { hasText: 'Brand search' }).getByText('Eligible', { exact: true }).waitFor();
    await results.locator('tr', { hasText: 'Brand search' }).getByText('82.1%').waitFor();
    // Room to grow: the searches each campaign missed to its budget and to rank.
    await results.locator('tr', { hasText: 'Generic search' }).getByText('23% budget').waitFor();
    await results.locator('tr', { hasText: 'Brand search' }).getByText('· 31% rank').waitFor();
    await results.getByTestId('campaigns-total').getByText('Total: filtered campaigns').waitFor();
    // Both campaigns are enabled, so both totals are the same: $60.00 a day
    // of budget and $456.00 spent, every amount with its cents.
    assert.match(await results.getByTestId('account-total').textContent(), /^Total: account\$60\.00\/day\$456\.00/);
    assert.equal(await results.getByLabel('Status').inputValue(), 'enabled');
    // One visible button on the row, the rest under More, with a link to the network.
    await results.getByRole('button', { name: 'Pause Brand search' }).waitFor();
    await results.getByRole('button', { name: 'More for Brand search' }).click();
    for (const item of ['Change budget', 'Set a target', 'Open in Google Ads', 'Remove']) await page.getByRole('menuitem', { name: item }).waitFor();
    await page.keyboard.press('Escape');
    await page.getByRole('menu').waitFor({ state: 'hidden' });
    // Ticking campaigns shows one bar; one confirmation pauses them all.
    await results.getByRole('checkbox', { name: 'Select Brand search' }).check();
    await results.getByRole('checkbox', { name: 'Select Generic search' }).check();
    const bar = results.getByRole('group', { name: 'Change the ticked campaigns' });
    await bar.getByText('2 selected').waitFor();
    const beforeBulk = calls.length;
    await bar.getByRole('button', { name: 'Pause' }).click();
    await page.getByRole('alertdialog').getByRole('button', { name: 'Pause' }).click();
    await bar.waitFor({ state: 'hidden' });
    await page.getByText('Pause sent for 2 campaigns.').waitFor();
    // The page reads the campaigns again a moment later.
    const reread = calls.length;
    for (let i = 0; i < 80 && !calls.slice(reread).some(c => c.includes('/ads/accounts')); i++) await page.waitForTimeout(100);
    await page.waitForTimeout(500);
    await results.locator('table').getByText('Generic search').first().waitFor();
    assert.equal(calls.slice(beforeBulk).filter(c => /^POST .*\/ads\/campaigns\/[^/]+\/pause$/.test(c)).length, 2, calls.slice(beforeBulk).join('\n'));
    // Counting by another attribution model asks for it and adds its columns
    // beside the usual ones; the choice goes back to the last paid click.
    await results.getByRole('columnheader', { name: 'Network says', exact: true }).waitFor();
    assert.equal(await results.getByTestId('model-signups').count(), 0);
    const beforeModel = calls.length;
    await results.getByLabel('Attribution model').selectOption('linear');
    await results.locator('tr', { hasText: 'Brand search' }).getByTestId('model-signups').waitFor();
    assert.ok(calls.slice(beforeModel).some(c => c.includes('/ads/performance') && c.includes('model=linear')), calls.slice(beforeModel).join('\n'));
    assert.equal(await results.locator('tr', { hasText: 'Brand search' }).getByTestId('model-signups').textContent(), '15.5');
    await results.getByLabel('Attribution model').selectOption('last_paid_click');
    await results.getByTestId('model-signups').first().waitFor({ state: 'detached' });
    // Revenue and ROAS are columns to turn on, and then in the download.
    await results.getByRole('button', { name: 'Columns' }).click();
    for (const c of ['New revenue', 'Returning revenue', 'ROAS']) await page.getByRole('menuitemcheckbox', { name: c, exact: true }).click();
    await page.keyboard.press('Escape');
    const heads = await results.getByRole('columnheader').allTextContents();
    assert.deepEqual(heads.slice(heads.indexOf('Spend'), heads.indexOf('Spend') + 4), ['Spend', 'New revenue', 'Returning revenue', 'ROAS'], heads.join(' | '));
    const brandRow = results.locator('tr', { hasText: 'Brand search' });
    assert.equal(await brandRow.getByTestId('roas').textContent(), '3.0×');
    assert.match(await brandRow.getByTestId('roas').getAttribute('title'), /first and later payments, divided by spend, in the account's currency/);
    const [resultsCsv] = await Promise.all([page.waitForEvent('download'), results.getByRole('button', { name: 'Download CSV' }).click()]);
    const rcsv = (await readFile(await resultsCsv.path(), 'utf8')).replace(/^﻿/, '').trim().split('\r\n');
    assert(rcsv[0].includes('Spend,New revenue,Returning revenue,ROAS'), rcsv[0]);
    assert(rcsv.some(l => l.startsWith('Brand search,') && l.includes('200.00 USD,480.00 USD,120.00 USD,3,')), rcsv.join('\n'));
    // 30 days and a custom range change what is asked for.
    const before = calls.length;
    await page.getByRole('button', { name: 'Date range' }).click();
    await page.getByRole('menuitem', { name: 'Last 30 days' }).click();
    const from30 = new Date(Date.now() - 29 * 86_400_000).toISOString().slice(0, 10);
    await report.locator('table').getByText('Generic search').first().waitFor();
    for (let i = 0; i < 50 && !calls.slice(before).some(c => c.includes('/ads/campaigns?') && c.includes(`from=${from30}`)); i++) await page.waitForTimeout(100);
    assert(calls.slice(before).some(c => c.includes('/ads/campaigns?') && c.includes(`from=${from30}`)), calls.slice(before).join('\n'));
    const beforeCustom = calls.length;
    await page.getByRole('menu').waitFor({ state: 'hidden' });
    await page.getByRole('button', { name: 'Date range' }).click();
    await page.getByRole('menuitem', { name: 'Custom range' }).click();
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
    assert(csv.some(l => l.startsWith('Brand search,Google Ads · Acme Ads,Eligible,456.00 USD,900,12000,0.51 USD,15,21,30.40 USD')), csv.join('\n'));
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
  // 7. Below the campaign: an ad set and an ad are paused and resumed from
  // their Manage menus, and an ad set that holds its own budget has it set.
  {
    const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    await mock(page);
    const meta = '30000000-0000-0000-0000-000000000003';
    const setA = '31000000-0000-0000-0000-000000000001', setB = '31000000-0000-0000-0000-000000000002', adA = '32000000-0000-0000-0000-000000000001';
    const item = (id, level, name, extra) => ({ id, level, parent_id: level === 'ad' ? setA : meta, parent_name: level === 'ad' ? 'Lookalike' : 'Prospecting', campaign_id: meta, campaign_name: 'Prospecting',
      ad_account_id: account, connector: 'meta_ads', account_name: 'Acme Ads', external_id: id.slice(-3), name, status: 'active', provider_status: 'ACTIVE', daily_budget_micros: null, details: {}, brand_id: brand, currency: 'USD', report_currency: 'USD', exchange_rate: 1, totals, ...extra });
    const sent = [];
    await page.route('**/v1/brands/*/ads/**', async route => {
      const req = route.request(); const u = new URL(req.url()); const p = u.pathname;
      const json = (status, body) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
      if (p === `/v1/brands/${brand}/ads/campaigns`) return json(200, { data: [...campaigns, { ...campaigns[0], id: meta, external_id: '9', name: 'Prospecting', connector: 'meta_ads', objective: 'leads', daily_budget_micros: null }] });
      if (p === `/v1/brands/${brand}/ads/items`) {
        return json(200, { data: u.searchParams.get('level') === 'ad_group'
          ? [item(setA, 'ad_group', 'Lookalike', { daily_budget_micros: 40_000_000 }), item(setB, 'ad_group', 'Retargeting', { status: 'paused', provider_status: 'PAUSED' })]
          : [item(adA, 'ad', 'Demo video', { details: { policy_status: 'disapproved', policy_reasons: 'Destination not working', url: 'https://www.acme.example/demo', headlines: 'Watch the demo\nBook a call', descriptions: 'See it in two minutes.' } })] });
      }
      const m = p.match(/\/ads\/items\/([0-9a-f-]{36})(\/pause|\/resume)?$/);
      if (m && req.method() !== 'GET') {
        sent.push(`${req.method()} ${m[1]}${m[2] ?? ''} ${req.postData() ?? ''}`.trim());
        return json(202, { queued: true, change: { id: '80000000-0000-0000-0000-000000000009', kind: 'ad_group_pause', status: 'queued' } });
      }
      return route.fallback();
    });
    await page.goto(`${origin}/tests/fixtures/ads.html?item=${meta}`);
    const sets = page.locator('[data-slot=card]', { hasText: 'Open one for its ads' });
    await sets.getByText('Lookalike').first().waitFor();
    // An active ad set with its own budget: Pause and the budget, no Resume.
    await sets.getByRole('button', { name: 'Pause Lookalike' }).waitFor();
    assert.equal(await sets.getByRole('button', { name: 'Resume Lookalike' }).count(), 0);
    await sets.getByRole('button', { name: 'More for Lookalike' }).click();
    await page.getByRole('menuitem', { name: 'Set daily budget' }).click();
    const dialog = page.getByRole('alertdialog');
    assert.equal(await dialog.getByLabel('Daily budget').inputValue(), '40.00');
    await dialog.getByLabel('Daily budget').fill('25');
    await dialog.getByRole('button', { name: 'Set the budget' }).click();
    for (let i = 0; i < 50 && sent.length < 1; i++) await page.waitForTimeout(100);
    assert.deepEqual(sent, [`PATCH ${setA} {"daily_budget_micros":25000000}`]);
    // The menu didn't open the ad set's own page.
    assert.equal(await page.getByRole('heading', { name: 'Lookalike' }).count(), 0);
    await sets.getByRole('button', { name: 'Pause Lookalike' }).click();
    // Paused ad sets are left out to start with, as in Google Ads; the
    // Status filter brings them back, and the totals rows cover both.
    assert.equal(await sets.getByRole('button', { name: 'Resume Retargeting' }).count(), 0);
    await sets.getByText('1 not shown').waitFor();
    await sets.getByTestId('total-filtered').getByText('Total: filtered ad sets').waitFor();
    await sets.getByTestId('total-all').getByText('Total: campaign').waitFor();
    await sets.getByLabel('Status').selectOption('all');
    // A paused ad set whose campaign holds the budget: Resume only.
    assert.equal(await sets.getByRole('button', { name: 'Pause Retargeting' }).count(), 0);
    assert.equal(await sets.getByRole('button', { name: 'More for Retargeting' }).count(), 0, 'nothing but Resume');
    await sets.getByRole('button', { name: 'Resume Retargeting' }).click();
    // An ad: Pause, and no Remove on Meta.
    const adsCard = page.locator('[data-slot=card]', { hasText: 'Every ad in this campaign' });
    assert.equal(await adsCard.getByRole('button', { name: 'More for Demo video' }).count(), 0, 'no Remove on Meta');
    await adsCard.getByText('Disapproved: Destination not working').first().waitFor();
    // An ad with its text shows as it reads in search results.
    await adsCard.locator('[data-ad-preview]').getByText('Watch the demo | Book a call').first().waitFor();
    await adsCard.getByRole('button', { name: 'Pause Demo video' }).click();
    for (let i = 0; i < 50 && sent.length < 4; i++) await page.waitForTimeout(100);
    assert.deepEqual(sent.slice(1), [`POST ${setA}/pause`, `POST ${setB}/resume`, `POST ${adA}/pause`]);
    await page.getByText('Pause sent to Meta').first().waitFor();
    await shot(page, 'ad-set-manage');
    assert.deepEqual(errors, []);
    await page.close();
  }
  // A bid by hand: Set bid shows on an ad group only where the campaign
  // bids by hand, and posts the bid in micros.
  {
    const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    await mock(page);
    const manual = '30000000-0000-0000-0000-000000000004', group = '31000000-0000-0000-0000-000000000004';
    const bids = [];
    await page.route('**/v1/brands/*/ads/**', async route => {
      const req = route.request(); const u = new URL(req.url()); const p = u.pathname;
      const json = (status, body) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
      if (p === `/v1/brands/${brand}/ads/campaigns`) return json(200, { data: [...campaigns, { ...campaigns[0], id: manual, external_id: '333', name: 'Manual search', bidding_strategy: 'manual_cpc' }] });
      if (p === `/v1/brands/${brand}/ads/items`) {
        const id = u.searchParams.get('campaign_id');
        return json(200, { data: u.searchParams.get('level') === 'ad_group' ? [{ id: group, level: 'ad_group', parent_id: id, parent_name: 'x', campaign_id: id, campaign_name: 'x', ad_account_id: account, connector: 'google_ads',
          account_name: 'Acme Ads', external_id: '71', name: 'Pricing', status: 'active', provider_status: 'ENABLED', daily_budget_micros: null, details: {}, brand_id: brand, currency: 'USD', report_currency: 'USD', exchange_rate: 1, totals }] : [] });
      }
      if (p === `/v1/brands/${brand}/ads/items/${group}/bid` && req.method() === 'POST') { bids.push(req.postData()); return json(202, { queued: true, change: { id: '80000000-0000-0000-0000-000000000010', kind: 'bid', status: 'queued' } }); }
      return route.fallback();
    });
    await page.goto(`${origin}/tests/fixtures/ads.html?item=${manual}`);
    const groups = page.locator('[data-slot=card]', { hasText: 'Open one for its ads' });
    await groups.getByText('Pricing').first().waitFor();
    await groups.getByRole('button', { name: 'More for Pricing' }).click();
    await page.getByRole('menuitem', { name: 'Set bid' }).click();
    const dialog = page.getByRole('alertdialog');
    await dialog.getByLabel('Bid per click').fill('1.25');
    await dialog.getByRole('button', { name: 'Set the bid' }).click();
    for (let i = 0; i < 50 && bids.length === 0; i++) await page.waitForTimeout(100);
    assert.deepEqual(bids, ['{"bid_micros":1250000}']);
    await page.getByText('Bid change sent to Google Ads').first().waitFor();
    // On a campaign that bids automatically there is no Set bid.
    await page.goto(`${origin}/tests/fixtures/ads.html?item=${campaign}`);
    await page.locator('[data-slot=card]', { hasText: 'Open one for its ads' }).getByRole('button', { name: 'Pause Pricing' }).waitFor();
    assert.equal(await page.getByRole('button', { name: 'More for Pricing' }).count(), 0, 'no Set bid');
    assert.deepEqual(errors, []);
    await page.close();
  }
  // Someone who can't manage has no Manage menu on an ad set.
  {
    const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
    await mock(page);
    await page.route(`**/v1/brands/${brand}/ads/items*`, route => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: new URL(route.request().url()).searchParams.get('level') === 'ad_group'
      ? [{ id: '31000000-0000-0000-0000-000000000009', level: 'ad_group', parent_id: campaign, parent_name: 'Brand search', campaign_id: campaign, campaign_name: 'Brand search', ad_account_id: account, connector: 'google_ads', account_name: 'Acme Ads',
          external_id: '71', name: 'Pricing', status: 'active', provider_status: 'ENABLED', daily_budget_micros: null, details: {}, brand_id: brand, currency: 'USD', report_currency: 'USD', exchange_rate: 1, totals }] : [] }) }));
    await page.goto(`${origin}/tests/fixtures/ads.html?item=${campaign}&readonly`);
    await page.locator('[data-slot=card]', { hasText: 'Open one for its ads' }).getByText('Pricing').first().waitFor();
    assert.equal(await page.getByRole('button', { name: 'Pause Pricing' }).count(), 0);
    assert.equal(await page.getByRole('button', { name: 'More for Pricing' }).count(), 0);
    await page.close();
  }
  // Excluding a search: the button is on the row, it asks first and warns
  // when the search brought conversions, then posts the negative keyword.
  {
    const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    await mock(page);
    const group = '31000000-0000-0000-0000-000000000021';
    const negatives = [];
    await page.route('**/v1/brands/*/ads/**', async route => {
      const req = route.request(); const p = new URL(req.url()).pathname;
      const json = (status, body) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
      if (p === `/v1/brands/${brand}/ads/search-terms`) return json(200, { from: '2026-10-01', to: '2026-10-28', data: [
        { term: 'free template', keyword_text: 'template', keyword_match_type: 'phrase', match_type: 'phrase', ad_group_id: group, ad_group_name: 'Core', campaign_id: campaign, campaign_name: 'Brand search', currency: 'USD', report_currency: 'USD', exchange_rate: 1, totals: { ...totals, conversions: 2 }, keyword_signups: 8, keyword_signups_paid: 0, keyword_paid_share: 0 },
      ] });
      if (p === `/v1/brands/${brand}/ads/keywords`) return json(200, { from: '2026-10-01', to: '2026-10-28', data: [
        { id: '32000000-0000-0000-0000-000000000001', external_id: '91', text: 'template', match_type: 'phrase', status: 'active', ad_group_id: group, ad_group_name: 'Core', campaign_id: campaign, campaign_name: 'Brand search', currency: 'USD', report_currency: 'USD', exchange_rate: 1, totals,
          first_page_cpc_micros: null, top_of_page_cpc_micros: null, first_position_cpc_micros: null, quality_score: 7, signups: 8, signups_paid: 2, new_payers: 3, paid_share: 0.25, cost_per_new_payer_micros: 152_000_000 },
      ] });
      if (p === `/v1/brands/${brand}/ads/campaigns/${campaign}/signals`) return json(200, { supported: true, from: '2026-10-01', to: '2026-10-28', currency: 'USD', budget_lost_impression_share: 0.32, rank_lost_impression_share: 0.05, search_partners: false, display_expansion: false, segments: [
        { kind: 'device', value: 'mobile', label: 'mobile', impressions: 9000, clicks: 600, spend_micros: 300_000_000, conversions: 4 },
        { kind: 'device', value: 'desktop', label: 'desktop', impressions: 3000, clicks: 300, spend_micros: 156_000_000, conversions: 11 },
        { kind: 'location', value: 'geoTargetConstants/2840', label: 'US', impressions: 12000, clicks: 900, spend_micros: 456_000_000, conversions: 15 },
      ] });
      if (p === `/v1/brands/${brand}/ads/campaigns/${campaign}/keywords` && req.method() === 'POST') { negatives.push('add ' + req.postData()); return json(202, { queued: true, change: { id: '80000000-0000-0000-0000-000000000022', kind: 'keyword_add', status: 'queued' } }); }
      if (p === `/v1/brands/${brand}/ads/campaigns/${campaign}/negative-keywords` && req.method() === 'POST') { negatives.push(req.postData()); return json(202, { queued: true, change: { id: '80000000-0000-0000-0000-000000000021', kind: 'negative_add', status: 'queued' } }); }
      return route.fallback();
    });
    await page.goto(`${origin}/tests/fixtures/ads.html?item=${campaign}`);
    // Room to grow and where the money goes, read from the network.
    const grow = page.locator('[data-slot=card]', { hasText: 'Room to grow, and where the money goes' });
    await grow.getByTestId('room-to-grow').getByText('It missed 32% of the searches it could have shown for because the budget ran out.').waitFor();
    await grow.getByRole('table').getByText('Mobile').waitFor();
    await grow.getByLabel('View by').selectOption('location');
    await grow.getByRole('table').getByText('United States').waitFor();
    // A keyword carries Outflare's own count of who it brought.
    const kw = page.getByRole('row', { name: /"template"/ });
    await kw.getByText('25%').waitFor();
    await kw.getByText('$152.00').waitFor();
    await page.getByRole('group', { name: 'Search' }).getByRole('button', { name: 'Search terms' }).click();
    await page.getByRole('row', { name: /free template/ }).getByText('0% of 8').waitFor();
    await page.getByRole('button', { name: 'Exclude free template' }).click();
    await page.getByRole('menuitem', { name: 'Every search that contains it, in this campaign' }).click();
    const ask = page.getByRole('alertdialog');
    await ask.getByText('Every search that contains these words is excluded, in the campaign Brand search.', { exact: false }).waitFor();
    await ask.getByText('This search brought 2 conversions in this range.').waitFor();
    await shot(page, 'exclude-search');
    await ask.getByRole('button', { name: 'Exclude' }).click();
    for (let i = 0; i < 50 && negatives.length < 1; i++) await page.waitForTimeout(100);
    assert.deepEqual(negatives, ['{"keyword":"free template","match_type":"phrase"}']);
    await page.getByText('Sent to Google Ads.', { exact: false }).first().waitFor();
    // A search that isn't a keyword yet can be added as one, after a
    // confirmation that says it can spend more.
    await page.getByRole('button', { name: 'Add free template as a keyword' }).click();
    await page.getByRole('menuitem', { name: 'This exact search' }).click();
    await page.getByRole('alertdialog').getByText('A new keyword can spend more, within the campaign', { exact: false }).waitFor();
    await page.getByRole('alertdialog').getByRole('button', { name: 'Add keyword' }).click();
    for (let i = 0; i < 50 && negatives.length < 2; i++) await page.waitForTimeout(100);
    assert.match(negatives[1], /^add \{"keyword":"free template","match_type":"exact","ad_group_id":"/);
    assert.deepEqual(errors, []);
    await page.close();
  }
  // Ads are a brand's: no workspace ads route but those main keeps on the
  // workspace (connections, every brand's accounts for assigning one, the
  // conversion rules and their sends, and the Tracking page's reads).
  const workspaceAds = calls.filter(c => c.includes('/v1/workspaces/workspace-a/ads/') && !/ \/v1\/workspaces\/workspace-a\/ads\/(connections|accounts|conversion-rules|conversion-sends|tracking-health|tracking-templates|accounts\/[^/]+\/tracking\/run)(\?|$)/.test(c));
  assert.deepEqual(workspaceAds, []);
  // Send conversions back: adding a rule searches the brand's own events,
  // a payment takes its own amount with nothing to fill in, another event
  // offers the number properties it carried and reads cents from the name,
  // and a payment rule saved without a value is fixed in one click or
  // through Edit.
  {
    const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    await mock(page);
    const rule = (id, extra) => ({ id, ad_account_id: account, connector: 'google_ads', event_name: 'payment.succeeded', conversion: '', conversion_name: '', target_id: '78', target_name: 'Offline sale', event_type: '', value_property: '', value_unit: 'major', fixed_value_micros: null, currency: '', enabled: true, sent_30d: 0, skipped_30d: 0, failed_30d: 0, queued: 0, last_sent_at: null, created_at: '2026-11-16T10:00:00Z', updated_at: '2026-11-16T10:00:00Z', feed_role: 'custom', customer_type: '', ...extra });
    let rules = [rule('r1', {})];
    const posted = [], patched = [];
    const ev = (name, label, seen) => ({ name, label, description: '', properties: [], status: 'approved', sensitivity: 'internal', owner_id: null, retention_days: null, destinations: [], first_seen_at: '2026-10-01T00:00:00Z', last_seen_at: '2026-11-16T09:00:00Z', seen_count: seen, name_check: 'ok', looks_like: null, check_reason: '', same_as: null, created_at: '2026-10-01T00:00:00Z', updated_at: '2026-10-01T00:00:00Z' });
    const json = (route, body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
    await page.route(/\/ads\/conversion-targets(\?|$)/, route => json(route, { integration_id: '70000000-0000-0000-0000-000000000001', setup_needed: [], data: [{ id: '78', name: 'Offline sale', event_types: [] }] }));
    await page.route(/\/ads\/conversion-rules(\?|$)/, route => {
      if (route.request().method() === 'POST') { const b = JSON.parse(route.request().postData()); posted.push(b); rules = [...rules, rule('r' + (rules.length + 1), { event_name: b.event_name, value_property: b.value_property ?? '', value_unit: b.value_unit })]; return json(route, rules.at(-1), 201); }
      return json(route, { data: rules });
    });
    await page.route(/\/ads\/conversion-rules\/[^/?]+$/, route => {
      const b = JSON.parse(route.request().postData()); const id = route.request().url().split('/').at(-1); patched.push([id, b]);
      rules = rules.map(r => r.id === id ? { ...r, ...b } : r);
      return json(route, rules.find(r => r.id === id));
    });
    await page.route(/\/conversion-definitions(\?|$)/, route => json(route, { data: [] }));
    await page.route(/\/catalog\/events(\?|$)/, route => json(route, { data: [ev('signed_up', 'Signed up', 4920), ev('payment.succeeded', '', 2156), ev('order_completed', '', 2157)] }));
    await page.route(/\/catalog\/events\/[^/]+\/usage/, route => json(route, { name: 'order_completed', count: 10, people: 9, days: [], sampled: 10, used_in: [], properties: [{ key: 'total_cents', types: ['number'], share: 1, values: [] }, { key: 'plan', types: ['string'], share: 1, values: [] }] }));
    await page.goto(`${origin}/tests/fixtures/ads.html?item=conversions`);
    const card = page.locator('[data-slot=card]', { hasText: 'Send conversions back' });
    // The payment rule with no value says so and takes the amount in one click.
    await card.getByText('Every payment would be sent worth 0.').waitFor();
    await card.getByRole('button', { name: "Send the payment's amount" }).click();
    await card.getByText('Value from amount_minor').waitFor();
    assert.deepEqual(patched[0], ['r1', { value_property: 'amount_minor', value_unit: 'minor' }]);
    // Saying the site already collects consent from everyone asks once, then saves.
    const modes = [];
    await page.route(/\/ads\/settings$/, route => {
      if (route.request().method() !== 'PUT') return json(route, { consent_mode: modes.at(-1) ?? 'opt_out', updated_at: null });
      modes.push(JSON.parse(route.request().postData()).consent_mode);
      return json(route, { consent_mode: modes.at(-1), updated_at: '2026-11-16T10:00:00Z' });
    });
    await card.getByLabel('Who to send conversions for').selectOption('collected');
    await page.getByRole('button', { name: 'Yes, everyone has consented' }).click();
    await card.getByText('You confirmed your site collects consent').waitFor();
    assert.deepEqual(modes, ['collected']);
    // Edit changes the event in place.
    await card.getByRole('button', { name: 'Edit', exact: true }).click();
    await card.getByText('Each payment is sent with its own amount and currency.').waitFor();
    await card.getByRole('button', { name: 'Cancel' }).click();
    // Add a rule: search, pick a payment, nothing else to fill in.
    await card.getByRole('button', { name: 'Add a rule' }).click();
    await card.getByLabel('When this event happens').fill('pay');
    await card.getByRole('option', { name: /payment\.succeeded/ }).click();
    await card.getByText('Each payment is sent with its own amount and currency.').waitFor();
    assert.equal(await card.getByLabel(/^Worth/).count(), 0);
    await card.getByRole('button', { name: 'Add rule' }).click();
    await card.getByRole('button', { name: 'Add a rule' }).waitFor();
    assert.deepEqual({ e: posted[0].event_name, v: posted[0].value_property, u: posted[0].value_unit }, { e: 'payment.succeeded', v: 'amount_minor', u: 'minor' });
    // Another event: its number property is offered and read as cents.
    await card.getByRole('button', { name: 'Add a rule' }).click();
    await card.getByLabel('When this event happens').fill('order');
    await card.getByRole('option', { name: /order_completed/ }).click();
    await card.getByLabel(/^Worth/).selectOption('total_cents');
    assert.equal(await card.getByLabel('Read as').inputValue(), 'minor');
    await card.getByRole('button', { name: 'Add rule' }).click();
    await card.getByRole('button', { name: 'Add a rule' }).waitFor();
    assert.deepEqual({ e: posted[1].event_name, v: posted[1].value_property, u: posted[1].value_unit }, { e: 'order_completed', v: 'total_cents', u: 'minor' });
    // A sign-up: the brand's own history of who pays, and the two switches that use it.
    await page.route(/\/ads\/signup-quality(\?|$)/, route => json(route, { event: 'signed_up', from: '2026-08-18T00:00:00Z', to: '2026-11-16T00:00:00Z', signups: 4920, paid: 492, paid_share: 0.1, value_per_payer_micros: 50000000, currency: 'USD', enough: true, min_signups: 100, min_payers: 10, pay_days: 30,
      segments: [{ country: 'US', email_kind: 'work', signups: 900, paid: 180, paid_share: 0.2, predicted_share: 0.197, predicted_value_micros: 9850000, qualified: true }, { country: 'BR', email_kind: 'throwaway', signups: 300, paid: 1, paid_share: 0.0033, predicted_share: 0.011, predicted_value_micros: 550000, qualified: false }] }));
    await card.getByRole('button', { name: 'Add a rule' }).click();
    await card.getByLabel('When this event happens').fill('signed');
    await card.getByRole('option', { name: /signed_up/ }).click();
    const pays = card.getByTestId('who-pays');
    await pays.getByText('Of 4,920 people who did this in the last 90 days, 10% paid within 30 days').waitFor();
    await pays.getByRole('button', { name: 'See it by country and email' }).click();
    await pays.getByRole('cell', { name: 'Throwaway email' }).waitFor();
    await pays.getByLabel(/Send a predicted value/).check();
    await pays.getByLabel(/Send qualified ones only/).check();
    if (process.env.OUTFLARE_TEST_SHOTS) await page.screenshot({ path: `${process.env.OUTFLARE_TEST_SHOTS}/who-pays.png` });
    await card.getByRole('button', { name: 'Add rule' }).click();
    await card.getByRole('button', { name: 'Add a rule' }).waitFor();
    assert.deepEqual({ e: posted[2].event_name, p: posted[2].predicted_value, q: posted[2].qualified_only }, { e: 'signed_up', p: true, q: true });
    assert.deepEqual(errors, []);
    await page.close();
  }
  // The send log is its own screen: each send with what happened, where
  // the country came from and a link to the person.
  {
    const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    await mock(page);
    const person = '90000000-0000-0000-0000-000000000001';
    const send = (id, extra) => ({ id, rule_id: 'r1', connector: 'google_ads', event_name: 'signed_up', record_id: person, record_name: 'Ana Reyes', event_id: 'msg-' + id, occurred_at: '2026-11-16T10:00:00Z', status: 'sent', reason: '', match_keys: ['gclid', 'email'], value_micros: null, currency: '', attempts: 1, sent_at: '2026-11-16T10:02:00Z', created_at: '2026-11-16T10:01:00Z', transport: 'data_manager', request_id: 'req-1', country: 'US', country_source: 'earlier_event', target_name: 'Northpeak sign-up', ad_account_id: account, ...extra });
    const asked = [];
    const json = (route, body) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
    await page.route(/\/ads\/conversion-rules(\?|$)/, route => json(route, { data: [] }));
    await page.route(/\/ads\/conversion-sends(\?|$)/, route => {
      const q = new URL(route.request().url()).searchParams; asked.push(q.get('status') ?? '');
      const all = [send('s1', {}), send('s2', { status: 'skipped', reason: 'no consent for ad data, and their country is unknown', match_keys: [], country: '', country_source: '', sent_at: null, attempts: 0, record_id: null, record_name: '' }),
        send('s3', { status: 'failed', reason: 'NOT_FOUND: Resource not found.', attempts: 5, sent_at: null })];
      return json(route, { data: all.filter(s => !q.get('status') || s.status === q.get('status')), next_cursor: null });
    });
    await page.route(/\/ads\/conversion-sends\/s1\/status$/, route => json(route, { state: 'partly_processed', detail: 'x', request_id: 'req-1', records: 3, errors: [{ reason: 'EVENT_TIME_INVALID', label: 'Event time invalid', count: 1 }], warnings: [], checked_at: '2026-11-16T11:00:00Z' }));
    await page.route(/\/ads\/conversion-check(\?|$)/, route => json(route, { from: '2026-11-03', to: '2026-11-16', data: [
      { ad_account_id: account, account_name: 'Northpeak', connector: 'google_ads', action: 'Northpeak sign-up', sent: 40, sent_with_click_id: 30, counted: 27, share: 0.9, status: 'ok', days: [] },
      { ad_account_id: account, account_name: 'Northpeak', connector: 'google_ads', action: 'Northpeak purchase', sent: 25, sent_with_click_id: 20, counted: 0, share: null, status: 'none', days: [] },
    ] }));
    await page.goto(`${origin}/tests/fixtures/ads.html?item=sends`);
    const card = page.locator('[data-slot=card]', { hasText: 'Send log' }).getByRole('table');
    const filters = page.locator('[data-slot=card]', { hasText: 'Send log' });
    await card.getByText('Google Ads accepted it').first().waitFor();
    await card.getByText('from an earlier event of theirs').first().waitFor();
    await card.getByText('Google click id, Email (hashed)').first().waitFor();
    await card.getByText('Not sent, and not counted as an error.').waitFor();
    await card.getByText('Google Ads could not find the conversion.').waitFor();
    assert.equal(await card.getByRole('link', { name: 'Ana Reyes' }).first().getAttribute('href'), `../people/${person}`);
    // Above the log: what was sent against what the network counts.
    const check = page.locator('[data-slot=card]', { hasText: 'Sent against counted' }).getByRole('table');
    const ok = check.getByRole('row', { name: /Northpeak sign-up/ });
    await ok.getByText('90%').waitFor();
    await ok.getByText('Counted', { exact: true }).waitFor();
    await check.getByRole('row', { name: /Northpeak purchase/ }).getByText('None counted').waitFor();
    await check.getByText('Google Ads counts none of them.').waitFor();
    // What the platform did after accepting it is asked from the row.
    await card.getByRole('button', { name: 'Ask Google Ads what it did with this signed_up' }).click();
    await card.getByText('Partly refused').waitFor();
    await card.getByText('Refused 1: Event time invalid.').waitFor();
    await filters.getByLabel('Result').selectOption('failed');
    await card.getByText('Google Ads accepted it').first().waitFor({ state: 'detached' });
    assert.equal(asked.at(-1), 'failed');
    assert.deepEqual(errors, []);
    await page.close();
  }
  // The chart of the ads over time: four number tiles, each a metric from
  // its menu, drawn or hidden by a click, and remembered after a reload.
  {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    await mock(page);
    const json = (route, body) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
    await page.route(/\/insights\/catalog$/, route => json(route, { conversions: [], events: [], networks: [{ network: 'google', connected: true }] }));
    const series = (label, values, currency) => ({ label, total: values.reduce((a, b) => a + b, 0), points: values.map((value, i) => ({ start: `2026-11-0${i + 1}T00:00:00Z`, value })), ...(currency ? { currency } : {}) });
    await page.route(/\/insights\/(trend|funnel)$/, route => json(route, route.request().url().endsWith('funnel') ? { steps: [], breakdown: [], from: '2026-11-01T00:00:00Z', to: '2026-11-04T00:00:00Z' }
      : { series: [series('Ad spend', [10, 20, 30, 40], 'USD'), series('Clicks', [20, 30, 40, 110]), series('Impressions', [1000, 1000, 1000, 1000]), series('Conversions', [1, 1, 1, 2]), series('Visits from ads', [10, 20, 30, 40])], formula: null, breakdown: [], from: '2026-11-01T00:00:00Z', to: '2026-11-04T00:00:00Z' }));
    await page.goto(`${origin}/tests/fixtures/ads.html`);
    const tile = (name) => page.getByRole('button', { name: `${name}: choose another metric` });
    await tile('Clicks').waitFor();
    // Clicks and Cost are drawn to start with; $100 over 200 clicks is $0.50.
    const pressed = () => page.locator('button[aria-pressed=true]').allTextContents();
    assert.deepEqual(await pressed(), ['200', '$100.00']);
    await page.getByRole('button', { pressed: false }).filter({ hasText: '$0.50' }).waitFor();
    await page.getByRole('img', { name: 'Clicks, Cost over time' }).waitFor();
    // A click on Impressions draws it too; a click on Clicks hides it.
    await page.getByRole('button', { pressed: false }).filter({ hasText: '4,000' }).click();
    await page.getByRole('button', { pressed: true }).filter({ hasText: /^200$/ }).click();
    await page.getByRole('img', { name: 'Cost, Impressions over time' }).waitFor();
    // Avg. CPC's menu swaps it for the click rate: 200 of 4,000 is 5%.
    await tile('Avg. CPC').click();
    await page.getByRole('menuitemradio', { name: 'Click rate' }).click();
    await tile('Click rate').waitFor();
    await page.getByRole('button', { pressed: false }).filter({ hasText: '5.00%' }).waitFor();
    await shot(page, 'chart');
    // The choice is still there after a reload.
    await page.reload();
    await tile('Click rate').waitFor();
    await page.getByRole('img', { name: 'Cost, Impressions over time' }).waitFor();
    assert.deepEqual(errors, []);
    await page.close();
  }
  // Audiences: one list sent to several ad networks, each with its own
  // status; a new audience picks its list and networks on its own page.
  {
    const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    await mock(page);
    const json = (route, body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
    const dest = (id, connector, name, extra) => ({ id, ad_account_id: id, account_name: 'Northpeak', connector, connector_name: name, external_id: '801', status: 'synced', members: 1840, skipped: 12, size: { search: 1500, display: 1700, match_rate: 81 }, error: '', can_exclude: connector !== 'reddit_ads', excluded_campaigns: 0, synced_at: '2026-11-17T08:00:00Z', created_at: '2026-11-10T08:00:00Z', ...extra });
    const list = { id: '81000000-0000-0000-0000-000000000001', name: 'Paid in the last 90 days', members: 1852 };
    const audiences = [{ id: '82000000-0000-0000-0000-000000000001', name: 'Paying customers', enabled: true, exclude: false, list, created_at: '2026-11-10T08:00:00Z', updated_at: '2026-11-10T08:00:00Z',
      destinations: [dest(account, 'google_ads', 'Google Ads'), dest('83000000-0000-0000-0000-000000000002', 'meta_ads', 'Meta Ads', { status: 'error', error: 'Custom audience terms not accepted', size: {} })] }];
    const accounts = [{ id: account, name: 'Northpeak', connector: 'google_ads', connector_name: 'Google Ads', can_sync: true, can_exclude: true }, { id: '83000000-0000-0000-0000-000000000002', name: 'Northpeak', connector: 'meta_ads', connector_name: 'Meta Ads', can_sync: true, can_exclude: true },
      { id: '83000000-0000-0000-0000-000000000003', name: 'Northpeak', connector: 'chatgpt_ads', connector_name: 'ChatGPT Ads', can_sync: false, can_exclude: false },
      { id: '83000000-0000-0000-0000-000000000004', name: 'Northpeak', connector: 'reddit_ads', connector_name: 'Reddit Ads', can_sync: true, can_exclude: false }];
    const sent = [];
    await page.route(/\/ads\/audiences(\/[^?]*)?(\?|$)/, route => {
      const req = route.request(); const p = new URL(req.url()).pathname;
      if (req.method() !== 'GET') sent.push([req.method(), p.split('/ads/audiences')[1], req.postData() ? JSON.parse(req.postData()) : null]);
      if (req.method() === 'POST' && p.endsWith('/sync')) return json(route, { queued: true }, 202);
      if (req.method() === 'POST') { const a = { id: '82000000-0000-0000-0000-000000000002', enabled: true, list, destinations: [], created_at: '2026-11-17T09:00:00Z', updated_at: '2026-11-17T09:00:00Z', ...JSON.parse(req.postData()) }; audiences.push(a); return json(route, a, 201); }
      return json(route, { data: audiences, accounts });
    });
    await page.route(/\/smart-lists(\?|$)/, route => json(route, { data: [{ ...list, kind: 'smart', record_type: 'person' }, { id: '81000000-0000-0000-0000-000000000002', name: 'Accounts', kind: 'static', record_type: 'account', members: 4 }] }));
    await page.goto(`${origin}/tests/fixtures/ads.html?item=audiences`);
    const table = page.locator('[data-slot=card]', { hasText: 'Pick a list of people once' }).getByRole('table');
    const row = table.getByRole('row', { name: /Paying customers/ });
    await row.getByText('Paid in the last 90 days').waitFor();
    await row.getByText('1,852').waitFor();
    await row.getByText('Google Ads').waitFor();
    await row.getByText('Meta Ads').waitFor();
    await row.getByRole('button', { name: 'Sync now' }).click();
    await page.getByText('Syncing Paying customers.').waitFor();
    assert.deepEqual(sent.at(-1), ['POST', '/82000000-0000-0000-0000-000000000001/sync', null]);
    await shot(page, 'audiences');
    // Its own page: a row per network, with what the network matched.
    await row.getByRole('button', { name: 'Paying customers', exact: true }).click();
    assert.deepEqual(await page.evaluate(() => window.gone.at(-1)), ['ads', 'audiences/82000000-0000-0000-0000-000000000001']);
    const networks = page.locator('[data-slot=card]', { hasText: 'One row per ad account' }).getByRole('table');
    await networks.getByRole('row', { name: /Google Ads.*Synced.*1,840.*12.*1,700.*81%/ }).waitFor();
    await networks.getByRole('row', { name: /Meta Ads.*Failed.*Custom audience terms not accepted.*Not reported/ }).waitFor();
    await page.getByRole('button', { name: 'Download file' }).waitFor();
    await shot(page, 'audience');
    // A new audience: the one list of people is picked already, and a
    // network that only takes a file says so.
    await page.goto(`${origin}/tests/fixtures/ads.html?item=audiences/new`);
    await page.getByRole('heading', { name: 'New audience' }).waitFor();
    await page.getByText('ChatGPT Ads only takes an uploaded file.').waitFor();
    assert.equal(await page.getByRole('combobox').inputValue(), list.id);
    assert.equal(await page.getByRole('button', { name: 'Create audience' }).isDisabled(), true);
    await page.getByLabel('Name').fill('Big spenders');
    await page.getByRole('checkbox', { name: /Google Ads/ }).check();
    await page.getByRole('checkbox', { name: /Meta Ads/ }).check();
    await page.getByRole('button', { name: 'Create audience' }).click();
    await page.getByText('Big spenders is made. The first sync starts now.').waitFor();
    assert.deepEqual(sent.at(-1), ['POST', '', { name: 'Big spenders', list_id: list.id, ad_account_ids: [account, '83000000-0000-0000-0000-000000000002'], exclude: false }]);
    // People who should never see ads: the choice is on the form, a
    // network that can't be told from here says so, and the audience's
    // page counts the campaigns the list is excluded on.
    await page.goto(`${origin}/tests/fixtures/ads.html?item=audiences/new`);
    await page.getByLabel('Name').fill('Refunded');
    await page.getByRole('checkbox', { name: /Google Ads/ }).check();
    await page.getByRole('checkbox', { name: /Reddit Ads/ }).check();
    await page.getByRole('radio', { name: /Never show them ads/ }).check();
    await page.getByText("Reddit Ads can't be told to exclude a list from here.").waitFor();
    await shot(page, 'audience-exclude');
    await page.getByRole('button', { name: 'Create audience' }).click();
    await page.getByText('Refunded is made. The first sync starts now.').waitFor();
    assert.deepEqual(sent.at(-1), ['POST', '', { name: 'Refunded', list_id: list.id, ad_account_ids: [account, '83000000-0000-0000-0000-000000000004'], exclude: true }]);
    audiences[0].exclude = true; audiences[0].destinations[0].excluded_campaigns = 3;
    await page.goto(`${origin}/tests/fixtures/ads.html?item=audiences/82000000-0000-0000-0000-000000000001`);
    await page.getByText('Never shown ads').waitFor();
    await page.locator('[data-slot=card]', { hasText: 'One row per ad account' }).getByRole('row', { name: /Google Ads.*3 campaigns/ }).waitFor();
    assert.deepEqual(errors, []);
    await page.close();
  }
  // Ad accounts: a connection that takes extra fields to send conversions
  // says whether they are set, and has the form to add them.
  {
    const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    await mock(page);
    const fields = [{ name: 'pixel_id', label: 'Pixel ID', help: 'Your data source\'s Pixel ID.', secret: false, optional: true }, { name: 'conversions_key', label: 'Conversion key', help: 'Create a key.', secret: true, optional: true }, { name: 'website', label: 'Website address', help: 'The site.', secret: false, optional: true }];
    const conn = { id: '70000000-0000-0000-0000-000000000009', connector: 'chatgpt_ads', name: 'ChatGPT Ads', status: 'connected', accounts: 1, setup_needed: [], conversion_fields: fields, conversion_fields_set: ['website'], last_run_at: null, last_ok_at: '2026-11-09T08:00:00Z', last_error: null };
    const puts = [];
    await page.route(/\/ads\/connections(\/[^?]*)?(\?|$)/, route => {
      const req = route.request();
      if (req.method() === 'PUT') { puts.push(JSON.parse(req.postData())); conn.conversion_fields_set = ['website', 'pixel_id', 'conversions_key']; return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(conn) }); }
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: [conn] }) });
    });
    await page.goto(`${origin}/tests/fixtures/ads.html?item=accounts`);
    const card = page.locator('[data-slot=card]', { hasText: 'Your ad platform sign-ins' });
    await card.getByText('Not set up').waitFor();
    await card.getByText('Needs Pixel ID, Conversion key').waitFor();
    await card.getByRole('button', { name: 'Set up' }).click();
    await card.getByLabel('Pixel ID').fill('px_test_1');
    await card.getByLabel('Conversion key').fill('not-a-real-key');
    assert.equal(await card.getByLabel('Website address').getAttribute('placeholder'), 'Saved');
    await shot(page, 'conversion-setup');
    await card.getByRole('button', { name: 'Save' }).click();
    await card.getByText('Set up', { exact: true }).waitFor();
    await card.getByRole('button', { name: 'Edit' }).waitFor();
    assert.deepEqual(puts, [{ values: { pixel_id: 'px_test_1', conversions_key: 'not-a-real-key' } }]);
    assert.deepEqual(errors, []);
    await page.close();
  }
  console.log('paid ads: ok');
} finally {
  await browser?.close();
  server.kill();
}
