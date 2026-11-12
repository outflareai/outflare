// The Link orders page, in the real app on its real path (/link-orders).
// Starts a local Vite server and mocks every API request; needs no backend.
// Checks the SEO nav item, tab views against fixture data, Approve/Paid
// gating (brand approver only, Paid only after a passing live-link check),
// candidate traffic as an em dash, no causality claim on impact, and that
// the page never starts a paid SEO read.
// OUTFLARE_TEST_SHOTS=<dir> also saves labeled screenshots at desktop and
// phone widths, light and dark.
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
const approver = '30000000-0000-4000-8000-000000000001';
const operator = '30000000-0000-4000-8000-000000000002';
const t0 = '2026-11-02T10:00:00Z';

const donor = (id, domain, extra = {}) => ({
  id, publisher_id: id.replace('50000000', '51000000'), domain,
  pbn: false, country: 'US', languages: ['EN'], niches: ['proxies'],
  casino_allowed: false, rel_offered: ['dofollow'], slot_types: ['guest_post'],
  duration: 'permanent', duration_days: null, tat_days: 14, guarantee_days: 90,
  vendor_id: '', source: 'manual', domain_rating: 50, traffic: 12000, spam_score: null,
  metrics_at: '2026-11-03T00:00:00Z', metrics_source: 'ahrefs', notes: '',
  archived_at: null, prices: [{ currency: 'USD', amount_minor: 85000 }],
  created_at: t0, updated_at: t0, ...extra,
});

const donors = [
  donor('50000000-0000-4000-8000-000000000001', 'infrachronicle.example', { domain_rating: 62, prices: [{ currency: 'USD', amount_minor: 85000 }] }),
  donor('50000000-0000-4000-8000-000000000002', 'securityledger.example', { domain_rating: 58, country: 'DE', languages: ['EN', 'DE'], prices: [{ currency: 'EUR', amount_minor: 35000 }] }),
  donor('50000000-0000-4000-8000-000000000003', 'saasstack.example', { domain_rating: 41, niches: ['saas'], prices: [{ currency: 'EUR', amount_minor: 8000 }] }),
  donor('50000000-0000-4000-8000-000000000004', 'growthopsblog.example', { domain_rating: 36, pbn: true, prices: [{ currency: 'USD', amount_minor: 12000 }] }),
  donor('50000000-0000-4000-8000-000000000005', 'devtoolsweekly.example', { domain_rating: 55, prices: [{ currency: 'USD', amount_minor: 40000 }] }),
];

const order = (id, donorId, status, extra = {}) => {
  const d = donors.find(x => x.id === donorId);
  const quoted = extra.quoted_amount_minor ?? (status === 'requested' ? null : d.prices[0].amount_minor);
  const currency = extra.quoted_currency ?? (status === 'requested' ? null : d.prices[0].currency);
  const frozen = ['approved', 'published', 'paid', 'verified', 'lost'].includes(status);
  return {
    id, publisher_id: d.publisher_id, donor_profile_id: donorId, domain: d.domain, status,
    target_url: extra.target_url ?? '/blog', slot_type: 'guest_post', rel_requested: 'dofollow',
    quoted_amount_minor: quoted, quoted_currency: currency,
    donor_amount_minor: frozen ? quoted : null, currency: frozen ? currency : null,
    guarantee_days: 90, guarantee_until: frozen ? '2027-01-16T00:00:00Z' : null,
    published_url: extra.published_url ?? '', proof_url: extra.proof_url ?? '',
    published_at: extra.published_at ?? null, placement_id: extra.placement_id ?? null,
    agreement_id: null, invoice_id: extra.invoice_id ?? null, payment_id: extra.payment_id ?? null,
    credit_payment_id: extra.credit_payment_id ?? null, replaces_order_id: extra.replaces_order_id ?? null,
    owner_user_id: operator, last_check_at: extra.last_check_at ?? null,
    last_check_live: extra.last_check_live ?? null, last_check_target_match: extra.last_check_target_match ?? null,
    last_check_rel: extra.last_check_rel ?? '', last_check_anchor: extra.last_check_anchor ?? '',
    approved_by: frozen ? approver : null, approved_at: frozen ? '2026-10-20T12:00:00Z' : null,
    paid_by: extra.paid_at ? approver : null, paid_at: extra.paid_at ?? null,
    lost_at: extra.lost_at ?? null, lost_reason: extra.lost_reason ?? '', notes: extra.notes ?? '',
    created_at: extra.created_at ?? t0, updated_at: extra.updated_at ?? t0,
  };
};

const orders = [
  order('40000000-0000-4000-8000-000000000010', '50000000-0000-4000-8000-000000000005', 'requested', { target_url: '/architecture' }),
  order('40000000-0000-4000-8000-000000000001', '50000000-0000-4000-8000-000000000003', 'quoted', { target_url: '/architecture' }),
  order('40000000-0000-4000-8000-000000000011', '50000000-0000-4000-8000-000000000005', 'negotiating', { target_url: '/free-proxy-list' }),
  order('40000000-0000-4000-8000-000000000012', '50000000-0000-4000-8000-000000000001', 'approved', { target_url: '/proxies-for/tiktok' }),
  order('40000000-0000-4000-8000-000000000002', '50000000-0000-4000-8000-000000000005', 'published', {
    target_url: '/blog', published_url: 'https://devtoolsweekly.example/posts/outflare', published_at: '2026-11-04T00:00:00Z',
    placement_id: '60000000-0000-4000-8000-000000000002',
  }),
  order('40000000-0000-4000-8000-000000000003', '50000000-0000-4000-8000-000000000001', 'published', {
    target_url: '/blog', published_url: 'https://infrachronicle.example/guest/geonode', published_at: '2026-11-02T00:00:00Z',
    placement_id: '60000000-0000-4000-8000-000000000003',
    last_check_at: '2026-11-08T09:00:00Z', last_check_live: true, last_check_target_match: true,
    last_check_rel: 'dofollow', last_check_anchor: 'residential proxies',
  }),
  order('40000000-0000-4000-8000-000000000013', '50000000-0000-4000-8000-000000000003', 'published', {
    target_url: '/architecture', published_url: 'https://saasstack.example/list', published_at: '2026-11-01T00:00:00Z',
    placement_id: '60000000-0000-4000-8000-000000000013',
    last_check_at: '2026-11-08T09:00:00Z', last_check_live: false, last_check_target_match: false,
  }),
  order('40000000-0000-4000-8000-000000000014', '50000000-0000-4000-8000-000000000005', 'paid', {
    target_url: '/proxies-for/roblox', published_url: 'https://devtoolsweekly.example/roblox', published_at: '2026-10-15T00:00:00Z',
    paid_at: '2026-10-18T00:00:00Z', payment_id: '70000000-0000-4000-8000-000000000014', invoice_id: '71000000-0000-4000-8000-000000000014',
    last_check_at: '2026-11-08T09:00:00Z', last_check_live: true, last_check_target_match: true, last_check_rel: 'dofollow',
  }),
  order('40000000-0000-4000-8000-000000000005', '50000000-0000-4000-8000-000000000001', 'verified', {
    target_url: '/free-proxy-list', published_url: 'https://infrachronicle.example/proxy-list', published_at: '2026-10-01T00:00:00Z',
    paid_at: '2026-10-05T00:00:00Z', payment_id: '70000000-0000-4000-8000-000000000005', invoice_id: '71000000-0000-4000-8000-000000000005',
    last_check_at: '2026-11-08T09:00:00Z', last_check_live: true, last_check_target_match: true, last_check_rel: 'dofollow', last_check_anchor: 'free proxy list',
  }),
  order('40000000-0000-4000-8000-000000000004', '50000000-0000-4000-8000-000000000002', 'lost', {
    target_url: '/blog', published_url: 'https://securityledger.example/inserts/geonode', published_at: '2026-10-12T00:00:00Z',
    paid_at: '2026-10-21T00:00:00Z', payment_id: '70000000-0000-4000-8000-000000000004', invoice_id: '71000000-0000-4000-8000-000000000004',
    lost_at: '2026-11-06T00:00:00Z', last_check_at: '2026-11-06T00:00:00Z', last_check_live: false, last_check_target_match: false,
    last_check_rel: 'dofollow', last_check_anchor: 'geonode',
  }),
];

const candidates = [
  { domain: 'serpfacts.example', rating: 44, traffic: null, competitors: ['Oxylabs'], status: 'not_in_catalog', donor_profile_id: null },
  { domain: 'proxyinsider.example', rating: 41, traffic: null, competitors: ['Oxylabs', 'Smartproxy'], status: 'in_catalog', donor_profile_id: '50000000-0000-4000-8000-000000000003' },
  { domain: 'infrachronicle.example', rating: 62, traffic: 999999, competitors: ['Smartproxy'], status: 'ordered', donor_profile_id: '50000000-0000-4000-8000-000000000001' },
];

const settings = { approver_user_id: approver, equivalent_dr_band: 10, updated_at: t0, updated_by: approver };
const summary = {
  counts: { requested: 1, quoted: 1, negotiating: 1, approved: 1, published: 3, paid: 1, verified: 1, lost: 1 },
  committed: [{ currency: 'USD', amount_minor: 125000 }, { currency: 'EUR', amount_minor: 8000 }],
  spent: [{ currency: 'USD', amount_minor: 125000 }, { currency: 'EUR', amount_minor: 35000 }],
  lost: [{ currency: 'EUR', amount_minor: 35000 }],
};
const budget = {
  budgets: [
    { budget_id: '80000000-0000-4000-8000-000000000001', title: 'November USD', currency: 'USD', amount_minor: 1000000, starts_on: '2026-11-01', ends_on: '2026-11-30', committed: 125000, spent: 125000, remaining: 750000 },
    { budget_id: '80000000-0000-4000-8000-000000000002', title: 'November EUR', currency: 'EUR', amount_minor: 200000, starts_on: '2026-11-01', ends_on: '2026-11-30', committed: 8000, spent: 35000, remaining: 157000 },
  ],
};
const impact = {
  published_at: '2026-10-01T00:00:00Z',
  target_url: '/free-proxy-list',
  causality: false,
  note: 'These figures do not show that the link caused a change. They are already-saved Search Console and ranking points around publication, not a fresh paid read.',
  search_console: {
    available: true,
    baseline: { from: '2026-09-03', to: '2026-09-30', clicks: 1200, impressions: 18000, position: 8.2 },
    after: { from: '2026-10-02', to: '2026-10-29', clicks: 1380, impressions: 19100, position: 7.6 },
    series: [
      { day: '2026-09-15', clicks: 40, impressions: 600, position: 8.2 },
      { day: '2026-10-15', clicks: 50, impressions: 680, position: 7.6 },
    ],
  },
  rankings: {
    available: true,
    series: [
      { day: '2026-09-15', keyword: 'free proxy list', position: 6, url: 'https://geonode.com/free-proxy-list' },
      { day: '2026-10-15', keyword: 'free proxy list', position: 4, url: 'https://geonode.com/free-proxy-list' },
    ],
  },
};
const members = [
  { user_id: approver, email: 'approver@example.com', name: 'Brand approver', role: 'owner', role_name: 'Owner', created_at: t0, brand_ids: [] },
  { user_id: operator, email: 'operator@example.com', name: 'Operator', role: 'member', role_name: 'Member', created_at: t0, brand_ids: [] },
];
const scopesFor = user => user === approver
  ? ['workspace.manage', 'coverage.read', 'coverage.manage', 'coverage.approve', 'coverage.finance.read', 'coverage.finance.manage', 'members.read']
  : ['coverage.read', 'coverage.manage', 'coverage.finance.read', 'members.read'];

const calls = [];
let actingAs = approver;
let catalog = [...donors];

function answer(method, u, body) {
  const p = u.pathname;
  if (p === '/v1/setup') return { needed: false };
  if (p === '/v1/me') return { user: { id: actingAs, name: actingAs === approver ? 'Brand approver' : 'Operator', email: actingAs === approver ? 'approver@example.com' : 'operator@example.com', email_verified: true }, workspaces: [{ id: ws, slug: 'acme', name: 'Acme', role: actingAs === approver ? 'owner' : 'member', scopes: scopesFor(actingAs) }] };
  if (p === '/v1/meta') return { name: 'Outflare', version: 'test', capabilities: [], login_methods: ['password'] };
  if (p === `/v1/workspaces/${ws}/brands`) return { data: [{ id: brand, name: 'Northpeak', handle: 'northpeak', status: 'active' }] };
  if (p === `/v1/workspaces/${ws}/stats`) return { unread_replies: 0, link_pitches_waiting: 0, actions_waiting: 0 };
  if (p === `/v1/workspaces/${ws}/members`) return { data: members };
  const b = `/v1/brands/${brand}`;
  if (p === `${b}/link-orders/settings`) {
    if (method === 'PATCH') return { ...settings, ...body, updated_at: '2026-11-10T12:00:00Z' };
    return settings;
  }
  if (p === `${b}/link-orders/catalog` && method === 'GET') return { data: catalog, next_cursor: null };
  if (p === `${b}/link-orders/catalog` && method === 'POST') {
    const row = donor('50000000-0000-4000-8000-000000000099', body.domain, { pbn: !!body.pbn, source: 'manual', prices: body.prices ?? [] });
    catalog = [...catalog, row];
    return row;
  }
  if (p === `${b}/link-orders/catalog/import` && method === 'POST') {
    return { created: 1, updated: 0, errors: [] };
  }
  if (p === `${b}/link-orders/candidates`) return { data: candidates, next_cursor: null };
  if (p.endsWith('/add-to-catalog') && method === 'POST') {
    const domain = p.split('/candidates/')[1].split('/')[0];
    const row = donor('50000000-0000-4000-8000-000000000098', domain, { source: 'competitor_gap', prices: [] });
    catalog = [...catalog, row];
    return row;
  }
  if (p === `${b}/link-orders/summary`) return summary;
  if (p === `${b}/link-orders/budget`) return budget;
  if (p === `${b}/link-orders` && method === 'GET') return { data: orders, next_cursor: null };
  if (p === `${b}/link-orders` && method === 'POST') {
    return order('40000000-0000-4000-8000-000000000099', body.donor_profile_id, 'requested', { target_url: body.target_url });
  }
  const one = p.match(new RegExp(`^${b}/link-orders/([0-9a-f-]+)(?:/(quote|negotiate|approve|publish|check|pay|verify|lost|replace|credit|equivalents|impact|history))?$`));
  if (one) {
    const id = one[1];
    const action = one[2];
    const row = orders.find(o => o.id === id);
    if (!row && !p.includes('/catalog/items/')) return null;
    if (!action) return row;
    if (action === 'impact') return impact;
    if (action === 'equivalents') return { data: [{ donor: donors[0], same_donor: false, dr_band_start: 50, dr_band_end: 59 }, { donor: donors[4], same_donor: false, dr_band_start: 50, dr_band_end: 59 }] };
    if (action === 'history') return { data: [], next_cursor: null };
    if (action === 'check') return { queued: true, placement_id: row.placement_id };
    return { ...row, ...(action === 'approve' ? { status: 'approved' } : {}), ...(action === 'pay' ? { status: 'paid', paid_at: '2026-11-10T12:00:00Z', paid_by: actingAs } : {}) };
  }
  return null;
}

let browser;
try {
  let ready = false;
  for (let n = 0; n < 100; n++) { try { ready = (await fetch(origin)).ok; if (ready) break; } catch {} await new Promise(r => setTimeout(r, 100)); }
  assert(ready, `Vite did not start: ${output}`);
  browser = await chromium.launch({ headless: true, args: ['--no-sandbox'], executablePath: process.env.OUTFLARE_TEST_CHROMIUM || undefined });

  const open = async (width, height, theme = 'dark', user = approver) => {
    actingAs = user;
    catalog = [...donors];
    const page = await browser.newPage({ viewport: { width, height }, deviceScaleFactor: shots ? 2 : 1 });
    await page.addInitScript(t => { try { localStorage.setItem('theme', t); } catch {} }, theme);
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    await page.route('**/v1/**', async route => {
      const req = route.request(); const u = new URL(req.url());
      let sent = null; try { sent = req.postDataJSON(); } catch {}
      calls.push({ method: req.method(), path: u.pathname, body: sent });
      let body;
      try { body = answer(req.method(), u, sent); } catch (e) { errors.push(e.message); body = null; }
      await route.fulfill(body === null
        ? { status: 404, contentType: 'application/json', body: JSON.stringify({ error: { code: 'not_found', message: 'nothing here', request_id: 'r' } }) }
        : { status: methodStatus(req.method(), u.pathname, body), contentType: 'application/json', body: JSON.stringify(body) });
    });
    return { page, errors };
  };
  const shot = async (page, name) => { if (shots) await page.screenshot({ path: join(shots, `${name}.png`), fullPage: true }); };
  const openPage = async (page) => {
    await page.goto(`${origin}/acme/northpeak/link-orders`);
    await page.getByRole('heading', { name: 'Link orders', exact: true }).waitFor();
  };
  const closeSheet = async (page) => {
    const sheet = page.locator('[data-slot="sheet-content"]');
    if (await sheet.count() && await sheet.first().isVisible()) {
      await page.getByRole('button', { name: 'Close' }).first().click();
      await sheet.first().waitFor({ state: 'hidden' });
    }
  };
  const tab = async (page, name) => {
    await closeSheet(page);
    await page.locator('[role="tablist"]').filter({ hasText: 'Overview' }).getByRole('tab', { name, exact: true }).click();
    const slug = name.toLowerCase();
    const expected = slug === 'overview' ? /\/link-orders\/?(\?|$)/ : new RegExp('/link-orders/' + slug + '(?:\\?|$)');
    try {
      await page.waitForURL(expected, { timeout: 5000 });
    } catch (e) {
      throw new Error(`${e.message} (url=${page.url()} after clicking ${name})`);
    }
  };

  // 1. Approver: overview, SEO nav, KPIs keep currencies apart, screenshots of every tab.
  {
    calls.length = 0;
    const { page, errors } = await open(1440, 900, 'dark', approver);
    await openPage(page);
    await page.getByText('Per brand, currencies kept apart').waitFor();
    const kpi = await page.locator('body').innerText();
    assert.match(kpi, /\$10,000/);
    assert.match(kpi, /€2,000/);
    assert.doesNotMatch(kpi, /\$12,000|€12,000|12,000 USD\+EUR|total of USD and EUR/i);
    const seoBtn = page.getByRole('button', { name: 'SEO', exact: true });
    const loBtn = page.getByRole('button', { name: 'Link orders', exact: true });
    const backBtn = page.getByRole('button', { name: 'Backlinks', exact: true });
    if (!(await loBtn.isVisible().catch(() => false))) await seoBtn.click();
    await loBtn.waitFor();
    await backBtn.waitFor();
    const order = await page.evaluate(() => {
      const names = [...document.querySelectorAll('button')].map(b => (b.textContent || '').trim()).filter(Boolean);
      return { names, back: names.indexOf('Backlinks'), lo: names.indexOf('Link orders') };
    });
    assert(order.back !== -1 && order.lo !== -1 && order.lo === order.back + 1, `Link orders should sit immediately after Backlinks in the SEO module, got ${order.names.slice(Math.max(0, order.back - 2), order.lo + 3).join(' | ')}`);
    assert(!calls.some(c => c.path.includes('/seo-competitors/gaps') && c.method !== 'GET'), 'the page started a paid gaps read');
    assert(!calls.some(c => c.path.includes('/seo-competitors/gaps/links') && c.method === 'POST'), 'the page posted a paid link-gap pull');
    await shot(page, 'overview-desktop-dark');

    await tab(page, 'Catalog');
    await page.getByText(/Donor catalog ·/).waitFor();
    await page.getByText('infrachronicle.example').waitFor();
    assert.equal(await page.getByText('growthopsblog.example').count(), 0, 'PBN donor shown in the default exclude-PBN filter');
    await page.getByLabel('PBN').selectOption('any');
    await page.getByText('growthopsblog.example').waitFor();
    assert.equal(await page.getByRole('button', { name: /google sheet/i }).count(), 0, 'Google Sheet import is offered');
    await page.getByRole('button', { name: 'Import CSV' }).waitFor();
    await shot(page, 'catalog-desktop-dark');

    await tab(page, 'Candidates');
    await page.getByRole('heading', { name: 'Donor candidates' }).waitFor();
    const traffic = await page.locator('[data-testid="candidate-traffic"]').allTextContents();
    assert(traffic.length >= 3, 'candidate rows missing');
    for (const t of traffic) assert.equal(t.trim(), '—', `candidate traffic was ${JSON.stringify(t)} instead of an em dash`);
    await page.getByRole('button', { name: 'Add to catalog' }).click();
    const added = calls.find(c => c.method === 'POST' && c.path.includes('/link-orders/candidates/') && c.path.endsWith('/add-to-catalog'));
    assert(added, 'add-to-catalog was not called');
    assert.match(added.path, /serpfacts\.example/);
    assert(!calls.some(c => c.method === 'POST' && c.path.includes('/seo-competitors/')), 'adding a candidate started a paid SEO read');
    await shot(page, 'candidates-desktop-dark');

    await tab(page, 'Orders');
    await page.getByRole('tab', { name: 'Kanban' }).waitFor();
    await page.getByText('saasstack.example').first().waitFor();
    await shot(page, 'orders-kanban-desktop-dark');
    await page.getByRole('tab', { name: 'Table' }).click();
    await page.waitForURL(/\borders=table\b/);
    await shot(page, 'orders-table-desktop-dark');

    // Quoted order: Approve enabled for the brand approver.
    await page.locator('tr', { hasText: 'saasstack.example' }).filter({ hasText: 'Quoted' }).click();
    const approve = page.getByRole('button', { name: 'Approve', exact: true });
    await approve.waitFor();
    assert.equal(await approve.isDisabled(), false, 'Approve is disabled for the brand approver on a quoted order');
    await shot(page, 'order-approve-desktop-dark');
    await closeSheet(page);

    // Published, no live check yet: Paid disabled.
    await page.locator('tr', { hasText: 'devtoolsweekly.example' }).filter({ hasText: '/blog' }).click();
    const paidBlocked = page.getByRole('button', { name: 'Mark Paid' });
    await paidBlocked.waitFor();
    assert.equal(await paidBlocked.isDisabled(), true, 'Paid is enabled before a passing live-link check');
    await shot(page, 'order-paid-blocked-desktop-dark');
    await closeSheet(page);

    // Published, passing check: Paid enabled.
    await page.locator('tr', { hasText: 'infrachronicle.example' }).filter({ hasText: '/blog' }).click();
    const paidReady = page.getByRole('button', { name: 'Mark Paid' });
    await paidReady.waitFor();
    assert.equal(await paidReady.isDisabled(), false, 'Paid is disabled for the approver after a passing live-link check');
    await shot(page, 'order-paid-ready-desktop-dark');
    await closeSheet(page);

    await tab(page, 'Checks');
    await page.getByRole('heading', { name: 'Link checks' }).waitFor();
    await shot(page, 'checks-desktop-dark');

    await tab(page, 'Impact');
    await page.getByText('These figures do not show that the link caused a change').first().waitFor();
    const impactText = await page.locator('body').innerText();
    assert.doesNotMatch(impactText, /caused by this (link|order)|because of this (link|order)|this link caused/i);
    await shot(page, 'impact-desktop-dark');

    await tab(page, 'Payments');
    await page.getByRole('heading', { name: 'Payments ledger' }).waitFor();
    await shot(page, 'payments-desktop-dark');
    assert.deepEqual(errors, []);
    await page.close();
  }

  // 2. Light theme (desktop) and phone width.
  {
    const { page, errors } = await open(1440, 900, 'light', approver);
    await openPage(page);
    await shot(page, 'overview-desktop-light');
    await tab(page, 'Catalog');
    await page.getByText(/Donor catalog ·/).waitFor();
    await shot(page, 'catalog-desktop-light');
    await tab(page, 'Orders');
    await shot(page, 'orders-kanban-desktop-light');
    await tab(page, 'Impact');
    await page.getByText('These figures do not show that the link caused a change').first().waitFor();
    await shot(page, 'impact-desktop-light');
    assert.deepEqual(errors, []);
    await page.close();
  }
  {
    const { page, errors } = await open(375, 812, 'dark', approver);
    await openPage(page);
    await shot(page, 'overview-phone-dark');
    assert.deepEqual(errors, []);
    await page.close();
  }
  {
    const { page, errors } = await open(375, 812, 'light', approver);
    await openPage(page);
    await shot(page, 'overview-phone-light');
    assert.deepEqual(errors, []);
    await page.close();
  }

  // 3. Operator: Approve and Paid stay disabled; the client never posts them.
  {
    calls.length = 0;
    const { page, errors } = await open(1440, 900, 'dark', operator);
    await openPage(page);
    await tab(page, 'Orders');
    await page.getByRole('tab', { name: 'Table' }).click();
    await page.locator('tr', { hasText: 'saasstack.example' }).filter({ hasText: 'Quoted' }).click();
    const approve = page.getByRole('button', { name: 'Approve', exact: true });
    await approve.waitFor();
    assert.equal(await approve.isDisabled(), true, 'Approve is enabled for an operator who is not the brand approver');
    await closeSheet(page);
    await page.locator('tr', { hasText: 'infrachronicle.example' }).filter({ hasText: '/blog' }).click();
    const paid = page.getByRole('button', { name: 'Mark Paid' });
    await paid.waitFor();
    assert.equal(await paid.isDisabled(), true, 'Paid is enabled for an operator who is not the brand approver');
    await shot(page, 'operator-approve-disabled-desktop-dark');
    assert(!calls.some(c => c.method === 'POST' && /\/(approve|pay)$/.test(c.path)), 'operator posted Approve or Paid');
    assert.deepEqual(errors, []);
    await page.close();
  }

  console.log('ok');
} finally {
  await browser?.close();
  server.kill('SIGTERM');
}

function methodStatus(method, path, body) {
  if (method === 'POST' && path.endsWith('/check')) return 202;
  if (method === 'POST' && path.endsWith('/link-orders') && body?.id) return 201;
  if (method === 'POST' && path.endsWith('/catalog') && body?.id) return 201;
  return 200;
}
