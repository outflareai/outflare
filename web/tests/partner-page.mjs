// A partner's page and the partner list's server search, in the real app
// on its real paths. Starts a local Vite server and mocks every API
// request; needs no backend. The list sends search and sort to the server
// and pages that query; a partner's name links to their page; the page
// opens by URL and picks the partner's program; its tabs read only that
// partner's rows; hostile names stay text. The Rewards and Conversions
// tabs search on the server, each reward opens its proof trail, bulk
// approval runs a dry run first, and proofs download as CSV.
// OUTFLARE_TEST_SHOTS=<dir> also saves screenshots at desktop and phone widths.
import { chromium } from 'playwright';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import { createServer } from 'node:net';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const web = fileURLToPath(new URL('../', import.meta.url));
const shots = process.env.OUTFLARE_TEST_SHOTS;
if (shots) mkdirSync(shots, { recursive: true });
const port = await new Promise(resolve => {
  const listener = createServer(); listener.listen(0, '127.0.0.1', () => { const port = listener.address().port; listener.close(() => resolve(port)); });
});
const origin = `http://127.0.0.1:${port}`;
const server = spawn(process.execPath, [fileURLToPath(new URL('../node_modules/vite/bin/vite.js', import.meta.url)), '--host', '127.0.0.1', '--port', String(port), '--strictPort'], { cwd: web, stdio: 'pipe' });
let output = ''; server.stdout.on('data', b => { output += b; }); server.stderr.on('data', b => { output += b; });

const ws = '10000000-0000-4000-8000-000000000001';
const brand = '20000000-0000-4000-8000-000000000001';
const first = '30000000-0000-4000-8000-000000000001';
const second = '30000000-0000-4000-8000-000000000002';
const group = '50000000-0000-4000-8000-000000000001';
const hostile = '40000000-0000-4000-8000-000000000001';
const customer = '60000000-0000-4000-8000-000000000001';
const payout = '70000000-0000-4000-8000-000000000001';
const mine = '40000000-0000-4000-8000-000000000007';
const rule = '80000000-0000-4000-8000-000000000001';
const due = '70000000-0000-4000-8000-000000000002';
const evilName = '"><img src=x onerror="window.__xss=1">';
const evilEmail = 'probe"><img src=y onerror="window.__xss=2">@example.com';
const t0 = '2026-03-02T10:00:00Z';

const program = (id, name, slug) => ({
  id, name, slug, kind: 'affiliate', status: 'active', currency: 'USD', rules: [], groups: [{ id: group, name: 'Creators' }],
  portal: { lead_submissions: false }, imported_from: id === second ? 'tolt' : '', switched_over_at: null, landing_url: 'https://example.com',
});
const stats = { clicks: 1234, referrals: 40, customers: 12, sales_cents: 4567800, pending_cents: 12000, approved_cents: 30000, paid_cents: 90000, balance_cents: 20000, pending_actions: 0, held_at_source: { pending_cents: 4000, approved_cents: 5000 } };
const partner = (id, n, extra = {}) => ({
  id, program_id: second, program: 'tolt-program', record_id: customer, name: `Partner ${n}`, email: `partner${n}@example.com`, code: `code${n}`,
  status: 'active', group_id: null, campaign_id: null, link: `https://go.example.com/r/code${n}`, campaign_link: '', promo_codes: [],
  payout_method: 'set', payout_type: 'paypal', payout_token: null, postback_url: '', note: '', source: 'manual', currency: 'USD',
  stats: { ...stats, clicks: n }, created_at: t0, updated_at: t0, ...extra,
});
const evil = partner(hostile, 0, {
  name: evilName, email: evilEmail, code: 'oldcode', status: 'pending', group_id: group, payout_method: '', source: 'import',
  promo_codes: [{ id: 'p1', code: 'SPRING', status: 'active' }], stats,
});

const page1 = Array.from({ length: 100 }, (_, n) => partner(`40000000-0000-4000-8000-${String(n + 100).padStart(12, '0')}`, n + 1));
const page2 = Array.from({ length: 20 }, (_, n) => partner(`40000000-0000-4000-8000-${String(n + 300).padStart(12, '0')}`, n + 101));

const evilCustomer = '<script>window.__xss=3</script>';
const evilBuyer = 'buyer"><b>x</b>@example.com';
const sale = { conversion_id: 'c1', type: 'sale', external_id: 'inv_1001', amount_cents: 129900, currency: 'USD', occurred_at: t0 };
const rateRule = { id: rule, name: 'Launch rate', trigger: 'sale', kind: 'percent', percent_bp: 2000, amount_cents: null, recurrence: 'every', months: null, kept: false };
const row = (id, extra) => ({
  id, program_id: second, partner_id: hostile, side: 'partner', recipient_id: hostile, kind: 'cash', conversion_id: 'c1', rule_id: rule, reverses_id: null,
  amount_cents: 25980, currency: 'USD', action: '', action_data: {}, status: 'pending', hold_until: '2026-04-01T00:00:00Z', payout_id: null, note: '', approved_at: null, created_at: t0,
  partner_code: 'oldcode', partner_name: evilName, customer_record_id: customer, customer_name: evilCustomer, customer_email: evilBuyer, sale, rule: rateRule,
  source: 'native', imported_from: '', held_at_source: false, decision: null, reversed_by: [], net_cents: 25980, payout_status: null, ...extra,
});
const approvedBy = (by, actor_name, note = '') => ({ status: 'approved', by, actor_type: by === 'manual' ? 'human' : 'system', actor_name, at: '2026-03-05T12:00:00Z', note });
const rows = [
  row('w1', { source: 'imported', imported_from: 'tolt', held_at_source: true, note: 'Imported from Tolt' }),
  row('w2', { status: 'paid', payout_id: payout, payout_status: 'paid', note: 'March sales', approved_at: '2026-03-05T12:00:00Z', decision: approvedBy('manual', 'Owner', '<i>checked</i>'), reversed_by: ['w3'], net_cents: 0 }),
  row('w3', { kind: 'adjustment', amount_cents: -25980, net_cents: -25980, reverses_id: 'w2', status: 'approved', rule: null, rule_id: null, note: 'refund', decision: approvedBy('refund', '') }),
  row('w4', { kind: 'fee', amount_cents: 5000, net_cents: 5000, conversion_id: null, rule_id: null, customer_record_id: null, customer_name: '', customer_email: '', sale: null, rule: null, status: 'approved', payout_status: 'due', payout_id: due }),
];
const byId = Object.fromEntries(rows.map(r => [r.id, r]));
const proofPartner = { id: hostile, code: 'oldcode', name: evilName, email: evilEmail };
const referral = { id: 'r1', via: 'visit', status: 'customer', created_at: t0, first_sale_at: t0 };
const proofCustomer = { record_id: customer, name: evilCustomer, email: evilBuyer, customer_id: 'user_77' };
const conversion = { id: 'c1', type: 'sale', event_name: '', amount_cents: 129900, currency: 'USD', external_id: 'inv_1001', transaction_id: '90000000-0000-4000-8000-000000000001', payment_source: 'stripe', source: 'billing', product_id: 'pro', recurring: false, refund_of: null, occurred_at: t0 };
const w4Gaps = [
  ['credit', 'no_click', 'A fee has no click or visit behind it.'],
  ['referral', 'no_sale', 'A fee comes from no referral.'],
  ['customer', 'no_sale', 'A fee has no customer.'],
  ['conversion', 'no_sale', 'A fee was logged by hand, with no sale.'],
  ['rule', 'no_rule', 'No rule priced it: someone logged the amount.'],
  ['decision', 'decided_before_history', 'Approved before Outflare kept who approved rewards.'],
  ['payout', 'not_paid_yet', 'In a payout that is due, not paid yet.'],
];
const proofs = {
  w1: {
    reward: byId.w1, partner: proofPartner, credit: { via: 'promo_code', at: t0, ref: '', sub_id: '', promo_code: 'SPRING', click: null, visit: null }, referral: { ...referral, via: 'promo_code' }, customer: proofCustomer,
    conversion, rule: { ...rateRule, kept: true }, decision: null, payout: null,
    imported: { connector: 'tolt', partner_id: 'tolt-p-1', customer_id: 'tolt-c-1', commission_id: 'tolt-cm-1', transaction_id: 'tolt-t-1', payout_id: '' },
    gaps: [
      { step: 'credit', reason: 'credited_by_promo_code', message: 'No click or visit on record: the promo code SPRING credited this partner.' },
      { step: 'decision', reason: 'pending', message: 'Not approved yet: it is on hold until April 1.' },
      { step: 'payout', reason: 'not_in_payout', message: 'Tolt still pays this one, so it is in no payout here.' },
    ],
  },
  w2: {
    reward: byId.w2, partner: proofPartner, referral, customer: proofCustomer, conversion, rule: rateRule, decision: approvedBy('manual', 'Owner', '<i>checked</i>'),
    credit: { via: 'visit', at: t0, ref: 'oldcode', sub_id: 'sub-9', promo_code: '', click: null, visit: { id: 'v1', at: '2026-03-01T09:00:00Z', landing_url: 'https://example.com/pricing?ref=oldcode', ref: 'oldcode', referrer_host: 'blog.example.org', country: 'de', utm_source: 'blog', utm_medium: '', utm_campaign: 'spring', recorded: false } },
    payout: { id: payout, status: 'paid', amount_cents: 90000, currency: 'USD', created_at: '2026-04-02T00:00:00Z', paid_at: '2026-04-05T00:00:00Z', method: 'paypal', reference: 'PAYPAL-7781' },
    imported: null, gaps: [{ step: 'credit', reason: 'no_click', message: 'No click on record; a website visit credited them.' }],
  },
  w3: {
    reward: byId.w3, partner: proofPartner, credit: null, referral, customer: proofCustomer, conversion: { ...conversion, id: 'c2', type: 'refund', external_id: 're_1001', refund_of: 'c1' }, rule: null,
    decision: approvedBy('refund', ''), payout: null, imported: null,
    gaps: [{ step: 'credit', reason: 'no_click', message: 'A take-back has no click of its own.' }, { step: 'rule', reason: 'no_rule', message: 'A take-back for a refund; no rule priced it.' }, { step: 'payout', reason: 'not_paid_yet', message: 'Comes off the next payout.' }],
  },
  w4: {
    reward: byId.w4, partner: proofPartner, credit: null, referral: null, customer: null, conversion: null, rule: null, decision: null, payout: null, imported: null,
    gaps: w4Gaps.map(([step, reason, message]) => ({ step, reason, message })),
  },
};
const conversions = [{
  id: 'c1', program_id: second, partner_id: hostile, referral_id: 'r1', record_id: customer, customer_name: evilCustomer, customer_email: evilBuyer, partner_code: 'oldcode', partner_name: evilName,
  type: 'sale', event_name: '', recurring: false, external_id: 'inv_1001', refund_of: null, amount_cents: 129900, currency: 'USD', product_id: 'pro', source: 'billing', payment_source: 'stripe', interval: 'month', occurred_at: t0, created_at: t0,
}];
const csv = 'reward_id,partner_code,customer_email\nw2,oldcode,buyer@example.com\n';

const calls = [];
let heldBatches = 0;
function answer(method, u, body) {
  const p = u.pathname, q = u.searchParams;
  if (p === '/v1/setup') return { needed: false };
  if (p === '/v1/me') return { user: { id: 'user-1', name: 'Owner', email: 'owner@example.com', email_verified: true }, workspaces: [{ id: ws, slug: 'acme', name: 'Acme', role: 'owner', scopes: ['workspace.manage', 'partners.read', 'partners.manage'] }] };
  if (p === '/v1/meta') return { name: 'Outflare', version: 'test', capabilities: [], login_methods: ['password'] };
  if (p === `/v1/workspaces/${ws}/brands`) return { data: [{ id: brand, name: 'Northpeak', handle: 'northpeak', status: 'active' }] };
  if (p === `/v1/workspaces/${ws}/stats`) return { unread_replies: 0, link_pitches_waiting: 0, actions_waiting: 0 };
  const b = `/v1/brands/${brand}`;
  if (p === `${b}/partner-programs`) return { data: [program(first, 'Main program', 'main'), program(second, 'Tolt program', 'tolt-program')] };
  if (p === `${b}/partner-programs/${first}` || p === `${b}/partner-programs/${second}`) return program(p.endsWith(first) ? first : second, p.endsWith(first) ? 'Main program' : 'Tolt program', 'x');
  if (p.endsWith('/stats') && p.startsWith(`${b}/partner-programs/`)) return { ...stats, held_at_source: p.includes(second) ? stats.held_at_source : { pending_cents: 0, approved_cents: 0 }, partners: { total: 121, active: 120, pending: 1, suspended: 0, rejected: 0, missing_payout_details: 1 } };
  if (p.endsWith('/partners') && q.get('record_id')) return { data: [partner(mine, 7)], next_cursor: null };
  if (p.endsWith('/partner-referrals') && q.get('record_id')) return { data: [{ id: 'r9', program_id: second, partner_id: hostile, record_id: customer, customer_name: '', customer_email: '', via: 'click', promo_code: '', sub_id: '', status: 'customer', first_sale_at: t0, sales_cents: 0, created_at: t0 }], next_cursor: null };
  if (p.endsWith(`/partners/${hostile}`)) return evil;
  if (p === `${b}/partners`) {
    if (q.get('q')) return { data: q.get('q').toLowerCase() === 'oldcode' ? [evil] : [], next_cursor: null };
    if (q.get('cursor') === 'page2') return { data: page2, next_cursor: null };
    return { data: page1, next_cursor: 'page2' };
  }
  if (p === `${b}/partners/${hostile}`) return evil;
  if (p === `${b}/partner-referrals`) {
    assert.equal(q.get('partner_id'), hostile, 'referrals are read for this partner only');
    return { data: [{ id: 'r1', program_id: second, partner_id: hostile, record_id: customer, customer_name: '<script>window.__xss=3</script>', customer_email: 'buyer"><b>x</b>@example.com', via: 'promo_code', promo_code: 'SPRING', sub_id: '', status: 'customer', first_sale_at: t0, sales_cents: 129900, created_at: t0 }], next_cursor: null };
  }
  if (p === `${b}/partner-conversions`) {
    assert(q.get('partner_id') === hostile || q.get('program') === second, 'conversions are read for this partner or program only');
    if (q.get('q') && q.get('q') !== 'inv_1001') return { data: [], next_cursor: null };
    return { data: q.get('type') === 'refund' ? [] : conversions, next_cursor: null };
  }
  if (p === `${b}/partner-rewards`) {
    assert(q.get('partner_id') === hostile || q.get('program') === second, 'rewards are read for this partner or program only');
    if (q.get('q') && q.get('q') !== 'inv_1001') return { data: [], next_cursor: null };
    return { data: q.get('source') === 'held_at_source' ? [byId.w1] : rows, next_cursor: null };
  }
  const proof = p.match(new RegExp(`^${b}/partner-rewards/(w\\d)/proof$`));
  if (proof) return proofs[proof[1]] ?? null;
  if (p === `${b}/partner-rewards/bulk` && method === 'POST') {
    const counts = { native: 30, imported: 12, held_at_source: 18 };
    if (body.dry_run) return { action: body.action, done: 0, skipped: [], more: false, counts };
    if (body.include_held_at_source) return { action: body.action, done: 60, skipped: [], more: false, counts };
    return heldBatches++ % 2 === 0
      ? { action: body.action, done: 0, skipped: [{ id: 'h1', reason: 'held_at_source' }, { id: 'h2', reason: 'held_at_source' }], more: true, counts: { ...counts, held_at_source: 0 } }
      : { action: body.action, done: 42, skipped: [], more: false, counts: { ...counts, held_at_source: 0 } };
  }
  if (p === `${b}/partner-proofs/export`) return { raw: csv, type: 'text/csv' };
  if (p === `${b}/partner-payouts`) {
    assert(q.get('partner_id') === hostile || q.get('program') === second, 'payouts are read for this partner or program only');
    return { data: [{ id: payout, partner_id: hostile, partner_name: evilName, program_id: second, amount_cents: 90000, currency: 'USD', status: 'paid', note: '', reference: 'PAYPAL-7781', rewards: 3, paid_at: '2026-04-05T00:00:00Z', created_at: '2026-04-02T00:00:00Z' }], next_cursor: null };
  }
  if (p === '/v1/workspaces/' + ws + '/partner-settings') return { erase_payout_notes: false };
  if (p === `${b}/partners/${hostile}/activity`) {
    const a = (id, action, data, actor_type = 'human', actor_name = 'Owner') => ({ id, action, actor_type, actor_name, target_type: 'partner', target_id: hostile, data, created_at: t0 });
    return { data: [
      a('a1', 'partner_payout.paid', { amount_cents: 90000, currency: 'USD', reference: 'PAYPAL-7781' }),
      a('a2', 'partner_reward.approved', { amount_cents: 25980, currency: 'USD', by: 'auto' }, 'system', ''),
      a('a3', 'partner.updated', { status: 'active', previous_status: 'pending' }),
      a('a4', 'partner.updated', { status: 'active', previous_status: 'active', changed: ['payout_method'] }),
      a('a5', 'partner.something_new', {}, 'agent', 'Helper'),
    ], next_cursor: null };
  }
  return null;
}

let browser;
try {
  let ready = false;
  for (let n = 0; n < 100; n++) { try { ready = (await fetch(origin)).ok; if (ready) break; } catch {} await new Promise(r => setTimeout(r, 100)); }
  assert(ready, `Vite did not start: ${output}`);
  browser = await chromium.launch({ headless: true, args: ['--no-sandbox'], executablePath: process.env.OUTFLARE_TEST_CHROMIUM || undefined });

  const open = async (width, height) => {
    const page = await browser.newPage({ viewport: { width, height } });
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    await page.route('**/v1/**', async route => {
      const req = route.request(); const u = new URL(req.url());
      const sent = req.method() === 'GET' ? undefined : req.postDataJSON();
      calls.push({ method: req.method(), path: u.pathname, query: Object.fromEntries(u.searchParams), body: sent });
      let body;
      try { body = answer(req.method(), u, sent); } catch (e) { errors.push(e.message); body = null; }
      await route.fulfill(body === null
        ? { status: 404, contentType: 'application/json', body: JSON.stringify({ error: { code: 'not_found', message: 'nothing here', request_id: 'r' } }) }
        : body.raw !== undefined ? { status: 200, contentType: body.type, body: body.raw }
        : { status: 200, contentType: 'application/json', body: JSON.stringify(body) });
    });
    return { page, errors };
  };
  const shot = async (page, name, fullPage = true) => { if (shots) await page.screenshot({ path: join(shots, `${name}.png`), fullPage }); };
  const noXSS = async page => {
    assert.equal(await page.evaluate(() => window.__xss), undefined, 'a hostile name ran as HTML');
    assert.equal(await page.locator('img[src="x"], img[src="y"], b:text("x"), i:text("checked")').count(), 0, 'a hostile name became an element');
  };
  const notWide = async (page, width) => assert.equal(await page.evaluate(() => Math.max(document.body.scrollWidth, document.documentElement.scrollWidth) <= window.innerWidth), true, `the page scrolls sideways at ${width}px`);
  const until = async (ok, what) => {
    for (let n = 0; n < 50; n++) { if (ok()) return; await new Promise(r => setTimeout(r, 100)); }
    assert.fail(what);
  };
  const seen = l => l.filter({ visible: true }).first();
  const settled = page => page.evaluate(() => Promise.allSettled(document.getAnimations().map(a => a.finished)));
  const programTab = async (page, tab) => {
    await page.goto(`${origin}/acme/northpeak/partners`);
    await page.getByRole('link', { name: 'Partner 1', exact: true }).filter({ visible: true }).waitFor();
    await page.getByRole('combobox', { name: 'Program' }).selectOption(second);
    await page.getByText("What Outflare pays; Tolt pays $50.00 more").waitFor();
    await page.getByText("What Outflare pays; Tolt pays $40.00 more").waitFor();
    await shot(page, `program-tiles-${page.viewportSize().width}`);
    await page.getByRole('tab', { name: tab }).click();
  };
  const download = async (page, click) => {
    const [file] = await Promise.all([page.waitForEvent('download'), click()]);
    const { readFile } = await import('node:fs/promises');
    assert.equal(await readFile(await file.path(), 'utf8'), csv, 'the download is the CSV the server sent');
    const name = file.suggestedFilename();
    assert.match(name, /^[a-z0-9-]+\.csv$/, `a file name carries more than slugs and ids: ${name}`);
    return name;
  };

  // 1. The list searches and sorts on the server and pages that query.
  {
    const { page, errors } = await open(1280, 900);
    await page.goto(`${origin}/acme/northpeak/partners`);
    await page.getByRole('link', { name: 'Partner 1', exact: true }).waitFor();
    assert.equal(await page.getByText(/Search and sorting cover/).count(), 0, 'the partner list still says search covers only loaded rows');
    await page.getByRole('button', { name: 'Load more' }).click();
    await page.getByRole('link', { name: 'Partner 120', exact: true }).waitFor();
    await page.getByRole('combobox', { name: 'Sort partners' }).selectOption('sales');
    await page.getByRole('searchbox', { name: /Search by name/ }).fill('oldcode');
    await page.getByRole('link', { name: evilName, exact: true }).waitFor();
    const searched = calls.filter(c => c.path.endsWith('/partners') && c.query.q);
    assert(searched.length >= 1, 'search went to the server');
    assert(searched.every(c => c.query.q === 'oldcode' && c.query.sort === 'sales'), `search carried the sort: ${JSON.stringify(searched)}`);
    assert(calls.some(c => c.path.endsWith('/partners') && c.query.sort === 'sales' && !c.query.q), 'sorting went to the server');
    assert.equal(await page.getByRole('link', { name: 'Partner 1', exact: true }).count(), 0, 'old rows stayed after a search');
    await noXSS(page);
    await shot(page, 'list-search-desktop');
    await page.getByRole('link', { name: evilName, exact: true }).click();
    await page.waitForURL(`**/acme/northpeak/partners/${hostile}`);
    await page.getByRole('heading', { name: new RegExp('img src=x') }).waitFor();
    await page.getByRole('button', { name: '← Partners' }).click();
    await page.waitForURL('**/acme/northpeak/partners');
    assert.equal(await page.getByRole('searchbox', { name: /Search by name/ }).inputValue(), 'oldcode', 'Back lost the search');
    assert.equal(await page.getByRole('combobox', { name: 'Program' }).inputValue(), second, 'Back did not land on the partner\'s program');
    await page.getByRole('searchbox', { name: /Search by name/ }).fill(hostile);
    await page.waitForURL(`**/acme/northpeak/partners/${hostile}`);
    await page.getByRole('heading', { name: new RegExp('img src=x') }).waitFor();
    assert(!calls.some(c => c.path.endsWith('/partners') && c.query.q === hostile), 'a whole partner id was searched instead of opened');
    assert.deepEqual(errors, []);
    await page.close();
  }

  // 1b. The list at phone width: cards link to the page, nothing scrolls sideways.
  {
    const { page, errors } = await open(375, 812);
    await page.goto(`${origin}/acme/northpeak/partners`);
    await page.getByRole('link', { name: 'Partner 1', exact: true }).filter({ visible: true }).waitFor();
    await page.getByRole('searchbox', { name: /Search by name/ }).fill('oldcode');
    await page.getByRole('link', { name: evilName, exact: true }).filter({ visible: true }).waitFor();
    assert.equal(await page.evaluate(() => document.body.scrollWidth <= window.innerWidth), true, 'the list scrolls sideways at 375px');
    await shot(page, 'list-search-phone');
    await page.getByRole('link', { name: evilName, exact: true }).filter({ visible: true }).click();
    await page.waitForURL(`**/acme/northpeak/partners/${hostile}`);
    await noXSS(page);
    assert.deepEqual(errors, []);
    await page.close();
  }

  // 2. The page opens by URL, shows the header, numbers and each tab.
  for (const [width, height, tag] of [[1280, 900, 'desktop'], [375, 812, 'phone']]) {
    const { page, errors } = await open(width, height);
    await page.goto(`${origin}/acme/northpeak/partners/${hostile}`);
    await page.getByRole('heading', { name: new RegExp('img src=x') }).waitFor();
    await page.getByText(evilEmail, { exact: false }).filter({ visible: true }).first().waitFor();
    await page.getByText('Partner in Tolt program', { exact: false }).waitFor();
    for (const label of ['Clicks', 'Referrals', 'Customers', 'Sales', 'Commission earned', 'On hold', 'Approved', 'Paid', 'Balance due']) {
      await page.getByText(label, { exact: true }).filter({ visible: true }).first().waitFor();
    }
    await page.getByText('$1,320', { exact: true }).waitFor();
    await page.getByText('Imported from Tolt').waitFor();
    await page.getByText('no payout details').waitFor();
    await page.getByText('Creators').waitFor();
    await page.getByRole('button', { name: 'Approve', exact: true }).waitFor();
    await page.getByText("What Outflare pays; Tolt pays $40.00 more").waitFor();
    await page.getByText("What Outflare pays; Tolt pays $50.00 more").waitFor();
    await page.getByText('$250.00', { exact: true }).waitFor();
    await page.getByText('$50.00 more in due payouts').waitFor();
    assert.equal(await page.evaluate(() => document.body.scrollWidth <= window.innerWidth), true, `the page scrolls sideways at ${width}px`);
    await shot(page, `page-referrals-${tag}`);
    const customerLink = page.getByRole('link', { name: '<script>window.__xss=3</script>' }).filter({ visible: true }).first();
    assert.equal(await customerLink.getAttribute('href'), `/acme/northpeak/people/${customer}`);

    await page.getByRole('tab', { name: 'Conversions' }).click();
    await page.getByText('inv_1001').filter({ visible: true }).first().waitFor();
    await shot(page, `page-conversions-${tag}`);
    await page.getByRole('tab', { name: 'Rewards' }).click();
    await page.getByText(/Paid (Apr 5, 2026|5 Apr 2026)/).filter({ visible: true }).first().waitFor();
    await shot(page, `page-rewards-${tag}`);
    await page.getByRole('tab', { name: 'Payouts' }).click();
    await page.getByText('PAYPAL-7781').filter({ visible: true }).first().waitFor();
    await shot(page, `page-payouts-${tag}`);
    await page.getByRole('tab', { name: 'Activity' }).click();
    await page.getByText('Payout of $900.00 marked paid, reference PAYPAL-7781').filter({ visible: true }).first().waitFor();
    await page.getByText('Reward of $259.80 approved after the hold').filter({ visible: true }).first().waitFor();
    await page.getByText('Status changed from pending to active').filter({ visible: true }).first().waitFor();
    await page.getByText('Changed the payout details').filter({ visible: true }).first().waitFor();
    await page.getByText('partner.something_new').filter({ visible: true }).first().waitFor();
    await page.getByText('Outflare').filter({ visible: true }).first().waitFor();
    await shot(page, `page-activity-${tag}`);
    await noXSS(page);
    assert.equal(await page.evaluate(() => document.body.scrollWidth <= window.innerWidth), true, `the page scrolls sideways at ${width}px`);
    assert.deepEqual(errors, []);
    await page.close();
  }
  // 4. The program's Rewards say whose they are, and bulk approval dry-runs first.
  for (const [width, height, tag] of [[1280, 900, 'desktop'], [375, 812, 'phone']]) {
    const { page, errors } = await open(width, height);
    await programTab(page, 'Rewards');
    await seen(page.getByText('Imported from Tolt')).waitFor();
    await seen(page.getByText('paid by Tolt')).waitFor();
    await seen(page.getByText('Paid, then taken back, net $0.00')).waitFor();
    await seen(page.getByRole('button', { name: 'Reversed by' })).waitFor();
    await seen(page.getByRole('button', { name: 'Reverses a reward' })).waitFor();
    await notWide(page, width);
    await noXSS(page);
    await shot(page, `rewards-${tag}`);
    if (tag === 'desktop') {
      await page.getByRole('link', { name: evilName, exact: true }).first().waitFor();
      assert.equal(await page.getByRole('link', { name: evilCustomer }).first().getAttribute('href'), `/acme/northpeak/people/${customer}`);
      await page.getByText('Approved by Owner').first().waitFor();
      await page.getByText('Approved by Outflare').waitFor();
      await page.getByText('No customer').waitFor();
      await page.getByText('No sale').waitFor();
      await page.getByRole('button', { name: 'Columns' }).click();
      const picker = ['Partner', 'Customer', 'Sale', 'Reward', 'Status', 'Rule', 'Source', 'Decided', 'Payout', 'Note', 'Made'];
      for (const c of picker) await page.getByRole('menuitemcheckbox', { name: c, exact: true }).waitFor();
      assert.equal(await page.getByRole('menuitemcheckbox').count(), picker.length, 'the Columns picker lists a column with no name');
      await page.getByRole('menuitemcheckbox', { name: 'Rule', exact: true }).click();
      await page.keyboard.press('Escape');
      await page.getByRole('menu').waitFor({ state: 'detached' });
      await page.getByText('Launch rate: 20%, recurring, every sale').first().waitFor();
      await shot(page, 'rewards-rule-desktop');
    }

    const bulk = () => calls.filter(c => c.path.endsWith('/partner-rewards/bulk'));
    let before = bulk().length;
    await seen(page.getByRole('button', { name: 'Approve all pending' })).click();
    const dialog = page.getByRole('alertdialog');
    await settled(page);
    await dialog.getByText('Approve 42 rewards: 30 made by Outflare, 12 imported that Outflare pays. 18 imported rewards Tolt still pays are left out.').waitFor();
    assert.equal(bulk().length - before, 1, 'the confirmation opened before its dry run');
    assert.deepEqual(bulk().at(-1).body, { action: 'approve', program: second, dry_run: true, include_held_at_source: true });
    await shot(page, `bulk-confirm-${tag}`, false);
    await dialog.getByRole('button', { name: 'Approve 42' }).click();
    await page.getByText('Approved 42 rewards; left out 2 Tolt still pays.').waitFor();
    assert.deepEqual(bulk().slice(before + 1).map(c => c.body), [{ action: 'approve', program: second }, { action: 'approve', program: second }], 'a batch of only held rewards stopped the run, or held rewards were sent without being chosen');

    before = bulk().length;
    await seen(page.getByRole('button', { name: 'Approve all pending' })).click();
    await dialog.getByRole('button', { name: "Approve all 60, with Tolt's" }).click();
    await page.getByText('Approved 60 rewards.').waitFor();
    assert.deepEqual(bulk().slice(before).map(c => c.body), [
      { action: 'approve', program: second, dry_run: true, include_held_at_source: true },
      { action: 'approve', program: second, include_held_at_source: true },
    ]);

    await page.getByRole('searchbox', { name: /Search by partner code/ }).fill('inv_1001');
    await until(() => calls.some(c => c.path.endsWith('/partner-rewards') && c.query.q === 'inv_1001' && c.query.program === second), 'the rewards search never reached the server');
    await page.getByRole('button', { name: 'Approve all pending' }).filter({ visible: true }).waitFor({ state: 'detached' });
    await page.getByRole('combobox', { name: 'Source' }).selectOption('held_at_source');
    await until(() => calls.some(c => c.path.endsWith('/partner-rewards') && c.query.source === 'held_at_source' && c.query.q === 'inv_1001'), 'the source filter never reached the server');
    await seen(page.getByText('paid by Tolt')).waitFor();
    await page.getByText('Paid, then taken back, net $0.00').first().waitFor({ state: 'detached' });
    assert.deepEqual(errors, []);
    await page.close();
  }

  // 5. Each reward opens its proof trail: every step, every gap, reversals both ways, the payout.
  for (const [width, height, tag] of [[1280, 900, 'desktop'], [375, 812, 'phone']]) {
    const { page, errors } = await open(width, height);
    await programTab(page, 'Rewards');
    const proofOf = n => seen(page.getByRole('button', { name: 'Proof', exact: true }).filter({ visible: true }).nth(n));
    const drawer = page.getByRole('dialog');
    const trail = drawer.getByRole('list', { name: 'Proof trail' });
    const steps = () => trail.locator('h3').allTextContents();
    const proofCalls = () => calls.filter(c => c.path.endsWith('/proof')).map(c => c.path.split('/').at(-2));

    await proofOf(1).click();
    await drawer.getByText('Paid, then taken back, net $0.00').waitFor();
    assert.deepEqual(await steps(), ['Website visit', 'Referral', 'Customer', 'Sale', 'Rule', 'Approved', 'Payout']);
    for (const text of ['matched later', 'https://example.com/pricing?ref=oldcode', 'blog.example.org', 'DE', 'blog / spring', 'inv_1001', 'stripe', 'Launch rate', '20%', 'Recurring, every sale', 'as it is now', 'Owner', 'by hand', '<i>checked</i>', 'PayPal', 'PAYPAL-7781', 'user_77', 'No click on record; a website visit credited them.']) {
      await drawer.getByText(text, { exact: true }).first().waitFor();
    }
    assert.equal(await drawer.getByRole('link', { name: evilCustomer }).getAttribute('href'), `/acme/northpeak/people/${customer}`);
    await notWide(page, width);
    await noXSS(page);
    await settled(page);
    await shot(page, `proof-drawer-${tag}`, false);
    const name = await download(page, () => drawer.getByRole('button', { name: 'Download CSV' }).click());
    assert.equal(name, `tolt-program-payout-${payout}-proof.csv`);
    assert.deepEqual(calls.filter(c => c.path.endsWith('/partner-proofs/export')).at(-1).query, { payout_id: payout, format: 'csv' });

    await drawer.getByRole('button', { name: 'Reversed by' }).click();
    await drawer.getByText('A take-back for a refund; no rule priced it.').waitFor();
    assert.deepEqual(await steps(), ['Click or visit', 'Referral', 'Customer', 'Refund', 'Rule', 'Approved', 'Payout']);
    await drawer.getByText('by a refund').waitFor();
    await drawer.getByRole('button', { name: 'Reverses a reward' }).click();
    await drawer.getByText('Paid, then taken back, net $0.00').waitFor();
    await drawer.getByRole('button', { name: '← Back to the earlier reward' }).click();
    await drawer.getByText('A take-back for a refund; no rule priced it.').waitFor();
    assert.deepEqual(proofCalls().slice(-4), ['w2', 'w3', 'w2', 'w3']);
    await page.keyboard.press('Escape');

    await proofOf(3).click();
    for (const [, , message] of w4Gaps) await drawer.getByText(message, { exact: true }).waitFor();
    assert.deepEqual(await steps(), ['Click or visit', 'Referral', 'Customer', 'Sale', 'Rule', 'Approval', 'Payout']);
    await settled(page);
    await shot(page, `proof-gaps-${tag}`, false);
    await page.keyboard.press('Escape');

    await proofOf(0).click();
    await drawer.getByText('Imported from Tolt', { exact: true }).first().waitFor();
    for (const text of ['tolt-cm-1', 'tolt-t-1', 'SPRING', 'paid by Tolt', 'as it was then', 'No click or visit on record: the promo code SPRING credited this partner.', 'Not approved yet: it is on hold until April 1.', 'Tolt still pays this one, so it is in no payout here.']) {
      await drawer.getByText(text, { exact: true }).first().waitFor();
    }
    await page.keyboard.press('Escape');

    await proofOf(1).click();
    await drawer.getByRole('link', { name: 'Open payouts' }).click();
    await page.waitForURL(`**/acme/northpeak/partners/${hostile}`);
    assert.equal(await page.getByRole('tab', { name: 'Payouts' }).getAttribute('aria-selected'), 'true', 'Open payouts did not land on the Payouts tab');
    await seen(page.getByText('PAYPAL-7781')).waitFor();
    await noXSS(page);
    assert.deepEqual(errors, []);
    await page.close();
  }

  // 6. The program's Conversions: partner and customer, type, server search, a partner chip.
  for (const [width, height, tag] of [[1280, 900, 'desktop'], [375, 812, 'phone']]) {
    const { page, errors } = await open(width, height);
    await programTab(page, 'Conversions');
    await seen(page.getByText('inv_1001')).waitFor();
    if (tag === 'desktop') {
      assert.equal(await page.getByRole('link', { name: evilName, exact: true }).getAttribute('href'), `/acme/northpeak/partners/${hostile}`);
      assert.equal(await page.getByRole('link', { name: evilCustomer }).getAttribute('href'), `/acme/northpeak/people/${customer}`);
    }
    await notWide(page, width);
    await noXSS(page);
    await shot(page, `conversions-${tag}`);
    const search = page.getByRole('searchbox', { name: /Search by partner code/ });
    const conv = () => calls.filter(c => c.path.endsWith('/partner-conversions'));
    await search.fill('buyer@example.com');
    await until(() => conv().some(c => c.query.q === 'buyer@example.com' && c.query.program === second), 'the conversions search never reached the server');
    await seen(page.getByText('Nothing matches.')).waitFor();
    await search.fill('OLDCODE');
    await page.getByText('Partner: oldcode').waitFor();
    await until(() => conv().some(c => c.query.partner_id === hostile && c.query.program === second && !c.query.q), 'the partner chip never filtered on the server');
    assert.equal(await search.inputValue(), '', 'the code stayed in the search box next to its chip');
    await seen(page.getByText('inv_1001')).waitFor();
    await shot(page, `conversions-partner-${tag}`);
    await page.getByRole('button', { name: 'Show every partner' }).click();
    assert.equal(await page.getByText('Partner: oldcode').count(), 0);
    await page.getByRole('combobox', { name: 'Type' }).selectOption('refund');
    await until(() => conv().some(c => c.query.type === 'refund'), 'the type filter never reached the server');
    await seen(page.getByText('No conversions match.')).waitFor();
    if (tag === 'desktop') {
      await page.getByRole('combobox', { name: 'Type' }).selectOption('');
      await page.getByRole('button', { name: 'oldcode', exact: true }).click();
      await page.getByText('Partner: oldcode').waitFor();
    }
    assert.deepEqual(errors, []);
    await page.close();
  }

  // 7. Proof downloads: a partner over a date range, and each payout.
  {
    const { page, errors } = await open(1280, 900);
    await page.goto(`${origin}/acme/northpeak/partners/${hostile}`);
    await page.getByRole('heading', { name: new RegExp('img src=x') }).waitFor();
    await page.getByRole('button', { name: 'Download proof' }).click();
    const dialog = page.getByRole('dialog', { name: 'Download proof' });
    await dialog.getByLabel('From').fill('2026-03-01');
    await dialog.getByLabel('To').fill('2026-03-31');
    await settled(page);
    await shot(page, 'export-dialog-desktop', false);
    const name = await download(page, () => dialog.getByRole('button', { name: 'Download CSV' }).click());
    assert.equal(name, `tolt-program-partner-${hostile}-proof-from-2026-03-01-to-2026-03-31.csv`);
    const [from, to] = await page.evaluate(() => [new Date('2026-03-01T00:00:00').toISOString(), new Date('2026-04-01T00:00:00').toISOString()]);
    assert.deepEqual(calls.filter(c => c.path.endsWith('/partner-proofs/export')).at(-1).query, { partner_id: hostile, from, to, format: 'csv' });
    await page.getByRole('tab', { name: 'Payouts' }).click();
    assert.equal(await download(page, () => page.getByRole('button', { name: 'Download proof' }).nth(1).click()), `tolt-program-payout-${payout}-proof.csv`);
    await programTab(page, 'Payouts');
    assert.equal(await download(page, () => page.getByRole('button', { name: 'Download proof' }).click()), `tolt-program-payout-${payout}-proof.csv`);
    assert.equal(calls.filter(c => c.path.endsWith('/partner-proofs/export')).at(-1).query.payout_id, payout);
    await noXSS(page);
    assert.deepEqual(errors, []);
    await page.close();
  }

  // 3. A person's Partners section links to each partner's page.
  {
    const { page, errors } = await open(800, 600);
    await page.goto(`${origin}/tests/fixtures/record-partners.html`);
    const own = page.getByRole('link', { name: /Partner in tolt-program as code7/ });
    const by = page.getByRole('link', { name: /Referred by oldcode/ });
    await own.waitFor();
    await by.waitFor();
    assert.match(await own.getAttribute('href'), new RegExp(`/partners/${mine}$`));
    assert.match(await by.getAttribute('href'), new RegExp(`/partners/${hostile}$`));
    assert.equal(await page.getByRole('button', { name: 'Open partners' }).count(), 0);
    await by.click();
    assert.deepEqual(await page.evaluate(() => window.opened), [['partners', hostile]]);
    assert(page.url().endsWith('/tests/fixtures/record-partners.html'), 'a plain click left the page');
    await noXSS(page);
    await shot(page, 'record-partners');
    assert.deepEqual(errors, []);
    await page.close();
  }
  console.log('partner page: ok');
} finally {
  await browser?.close();
  server.kill();
}
