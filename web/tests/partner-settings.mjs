// A partner program's settings page, in the real app on its real path
// (/partners/settings). Starts a local Vite server and mocks every API
// request; needs no backend. The page fills the main area (no side sheet),
// splits the settings into tabs with their own URLs, shows the portal's
// state, keeps edits across tabs, saves only when something changed and
// sends the program's settings as edited, asks before archiving, and
// doesn't scroll sideways on a phone. A portal without an address is offered
// one from the program's name, so its link reads /p/creators/, not an id.
// OUTFLARE_TEST_SHOTS=<dir> also saves screenshots at desktop and phone
// widths, light and dark.
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

const program = (id, name) => ({
  id, name, slug: name.toLowerCase().replace(/ /g, '-'), brand_id: brand, kind: 'affiliate', status: 'active', currency: 'USD', rules: [], groups: [],
  landing_url: 'https://example.com/pricing', credit_window_days: 30, credit_model: 'last', hold_days: 30, auto_approve: false, auto_accept: false,
  block_self_referrals: true, pay_browser_signups: false, min_payout_cents: 5000, payout_term_days: 15, payout_methods: ['paypal', 'wise'],
  payout_tokens: [], lead_expiry_days: null, url_params: ['aff'], block_ad_clicks: false, block_social_clicks: false, blocked_countries: [],
  sales_source: '', postback_hint: 'ofb_ahMu', portal_enabled: id === first,
  portal_subdomain: id === first ? 'northpeak' : null, portal_url: id === first ? 'https://partners.example.com/northpeak' : `${origin}/p/${id}/`,
  portal: { title: 'Northpeak partners', color: '#111827', lead_submissions: '' }, imported_from: '', switched_over_at: null,
});
const stats = { clicks: 0, referrals: 0, customers: 0, sales_cents: 0, pending_cents: 0, approved_cents: 0, paid_cents: 0, balance_cents: 0, pending_actions: 0, held_at_source: { pending_cents: 0, approved_cents: 0 }, partners: { total: 0, active: 0, pending: 0, suspended: 0, rejected: 0, missing_payout_details: 0 } };

const calls = [];
const domains = [];
function answer(method, u, body) {
  const p = u.pathname;
  if (p === '/v1/setup') return { needed: false };
  if (p === '/v1/me') return { user: { id: 'user-1', name: 'Owner', email: 'owner@example.com', email_verified: true }, workspaces: [{ id: ws, slug: 'acme', name: 'Acme', role: 'owner', scopes: ['workspace.manage', 'partners.read', 'partners.manage'] }] };
  if (p === '/v1/meta') return { name: 'Outflare', version: 'test', capabilities: [], login_methods: ['password'] };
  if (p === `/v1/workspaces/${ws}/brands`) return { data: [{ id: brand, name: 'Northpeak', handle: 'northpeak', status: 'active' }] };
  if (p === `/v1/workspaces/${ws}/stats`) return { unread_replies: 0, link_pitches_waiting: 0, actions_waiting: 0 };
  const b = `/v1/brands/${brand}`;
  if (p === `${b}/partner-programs`) return { data: [program(first, 'Main program'), program(second, 'Creators')] };
  if (p === `${b}/partner-programs/${first}` && method === 'PATCH') return { ...program(first, 'Main program'), ...body };
  if (p === `${b}/partner-programs/${second}/portal` && method === 'PUT') return { ...program(second, 'Creators'), portal_enabled: body.enabled, portal_subdomain: body.subdomain, portal_url: `${origin}/p/${body.subdomain}/` };
  if (p === `${b}/tracking-domains` && method === 'POST') { const d = { id: '40000000-0000-4000-8000-000000000001', domain: body.domain, status: 'pending', target: 'domains.example.net', partner_program_id: body.partner_program, help_centre_id: null }; domains.push(d); return d; }
  if (p === `${b}/tracking-domains`) return { data: domains };
  if (p.endsWith('/stats') && p.startsWith(`${b}/partner-programs/`)) return stats;
  if (p.endsWith('/imports') || p.endsWith('/assets') || p.endsWith('/site-keys') || p.endsWith('/import-sources')) return { data: [], suggested: '' };
  if (p === `${b}/partners`) return { data: [], next_cursor: null };
  return null;
}

let browser;
try {
  let ready = false;
  for (let n = 0; n < 100; n++) { try { ready = (await fetch(origin)).ok; if (ready) break; } catch {} await new Promise(r => setTimeout(r, 100)); }
  assert(ready, `Vite did not start: ${output}`);
  browser = await chromium.launch({ headless: true, args: ['--no-sandbox'], executablePath: process.env.OUTFLARE_TEST_CHROMIUM || undefined });

  const open = async (width, height, theme = 'dark') => {
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
        : { status: 200, contentType: 'application/json', body: JSON.stringify(body) });
    });
    return { page, errors };
  };
  const shot = async (page, name) => { if (shots) await page.screenshot({ path: join(shots, `${name}.png`), fullPage: true }); };
  const tabs = { General: ['Links and credit', 'New partners', 'Pause or archive'], Payouts: ['Rewards and payouts'], Tracking: ['Where sales come from', 'Install on your website'], Fraud: ['Fraud protection'], Portal: ['Partner portal'], Import: ['Import from Tolt'] };
  const nav = page => page.getByRole('navigation', { name: 'Settings' });
  const tab = async (page, name) => { await nav(page).getByRole('button', { name: new RegExp(`^${name}`) }).click(); await page.waitForURL(`**/partners/settings/${name.toLowerCase()}`); };

  // 1. The Partners page opens settings as a page of its own, by URL.
  {
    const { page, errors } = await open(1440, 900);
    await page.goto(`${origin}/acme/northpeak/partners`);
    await page.getByText('Portal open').waitFor();
    await page.getByRole('button', { name: 'Program settings' }).click();
    await page.waitForURL('**/acme/northpeak/partners/settings');
    await page.getByRole('heading', { name: /Main program settings/ }).waitFor();
    assert.equal(await page.locator('[data-slot="sheet-content"]').count(), 0, 'settings still open in a side sheet');
    // Each tab shows only its own sections.
    for (const [name, own] of Object.entries(tabs)) {
      await tab(page, name);
      for (const [other, theirs] of Object.entries(tabs)) {
        for (const s of theirs) assert.equal(await page.getByRole('heading', { name: s, exact: true }).isVisible(), other === name, `${s} on the ${name} tab`);
      }
      assert.equal(await page.getByRole('region', { name: 'Unsaved changes' }).count(), 0, 'the save bar shows before any change');
      await shot(page, `settings-${name.toLowerCase()}-desktop-dark`);
    }
    await tab(page, 'Portal');
    await page.getByRole('link', { name: 'https://partners.example.com/northpeak' }).first().waitFor();
    await tab(page, 'Fraud');

    // A change shows the save bar; Discard puts it back.
    await page.getByRole('switch', { name: 'Ignore visits from paid ads' }).click();
    await page.getByRole('region', { name: 'Unsaved changes' }).waitFor();
    await page.getByRole('button', { name: 'Discard' }).click();
    assert.equal(await page.getByRole('switch', { name: 'Ignore visits from paid ads' }).getAttribute('aria-checked'), 'false');
    assert.equal(await page.getByRole('region', { name: 'Unsaved changes' }).count(), 0);

    // Save sends what was edited.
    await page.getByRole('switch', { name: 'Ignore visits from paid ads' }).click();
    await page.getByLabel('Blocked countries').fill('br, in');
    await tab(page, 'Tracking');
    await page.getByRole('switch', { name: 'Stripe' }).click();
    await tab(page, 'General');
    await page.getByLabel('Credit window (days)').fill('60');
    assert.equal(await page.getByRole('switch', { name: 'Review each application' }).getAttribute('aria-checked'), 'true', 'new programs should review applications');
    await page.getByRole('switch', { name: 'Review each application' }).click();
    await tab(page, 'Fraud');
    assert.equal(await page.getByLabel('Blocked countries').inputValue(), 'br, in', 'an edit was lost when switching tabs');
    assert.equal(await nav(page).getByTitle('Unsaved changes').count(), 3, 'tabs with changes are not marked');
    await shot(page, 'settings-unsaved-desktop-dark');
    await page.getByRole('button', { name: 'Save changes' }).click();
    await page.getByText('Settings saved.').waitFor();
    const patch = calls.find(c => c.method === 'PATCH' && c.path.endsWith(`/partner-programs/${first}`));
    assert(patch, 'nothing was saved');
    assert.equal(patch.body.block_ad_clicks, true);
    assert.equal(patch.body.sales_source, 'stripe');
    assert.deepEqual(patch.body.blocked_countries, ['BR', 'IN']);
    assert.equal(patch.body.credit_window_days, 60);
    assert.equal(patch.body.auto_accept, true);
    assert.equal(patch.body.min_payout_cents, 5000);
    assert.deepEqual(patch.body.url_params, ['aff']);
    assert.equal(patch.body.payout_term_days, 15);
    assert.equal(patch.body.status, undefined, 'saving settings changed the status');

    // Archive asks first; Cancel changes nothing.
    calls.length = 0;
    await tab(page, 'General');
    await page.getByRole('button', { name: 'Archive', exact: true }).click();
    await page.getByRole('alertdialog').getByText('Archive Main program?').waitFor();
    await page.getByRole('button', { name: 'Cancel' }).click();
    assert(!calls.some(c => c.method === 'PATCH'), 'Archive went through without asking');

    // Open Flows goes back to the Flows tab.
    await tab(page, 'Payouts');
    await page.getByRole('button', { name: 'Open Flows' }).click();
    await page.waitForURL('**/acme/northpeak/partners');
    assert.equal(await page.getByRole('tab', { name: 'Flows' }).getAttribute('aria-selected'), 'true');
    assert.deepEqual(errors, []);
    await page.close();
  }

  // 2. Light theme and phone width; nothing scrolls sideways.
  for (const [width, height, theme] of [[1440, 900, 'light'], [375, 812, 'dark'], [375, 812, 'light']]) {
    const { page, errors } = await open(width, height, theme);
    await page.goto(`${origin}/acme/northpeak/partners/settings/payouts`);
    await page.getByRole('heading', { name: /Main program settings/ }).waitFor();
    await page.getByRole('heading', { name: 'Rewards and payouts', exact: true }).waitFor();
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, `settings scroll sideways at ${width}px`);
    await shot(page, `settings-${width < 600 ? 'phone' : 'desktop'}-${theme}`);
    assert.deepEqual(errors, []);
    await page.close();
  }
  // 3. A portal without an address is offered one from the program's name.
  {
    const { page, errors } = await open(1440, 900);
    await page.addInitScript(id => { try { localStorage.setItem('outflare.partners.program', id); } catch {} }, second);
    await page.goto(`${origin}/acme/northpeak/partners/settings/portal`);
    await page.getByRole('heading', { name: /Creators settings/ }).waitFor();
    const address = page.getByRole('textbox', { name: /^Portal address/ });
    assert.equal(await address.inputValue(), 'creators');
    await page.getByText(`The portal's link: 127.0.0.1:${port}/p/creators/`).waitFor();
    await shot(page, 'settings-portal-address-desktop-dark');
    calls.length = 0;
    await page.getByRole('switch', { name: 'Open the portal' }).click();
    await page.getByRole('button', { name: 'Save portal' }).click();
    await page.getByText('Portal saved.').waitFor();
    const put = calls.find(c => c.method === 'PUT' && c.path.endsWith(`/partner-programs/${second}/portal`));
    assert.equal(put?.body.subdomain, 'creators');
    // Its own domain: one DNS record, shown until it's connected.
    await page.getByRole('button', { name: 'Use your own domain' }).click();
    await page.getByRole('textbox', { name: 'Your own domain' }).fill('https://partners.northpeak.example/');
    await page.getByRole('button', { name: 'Add', exact: true }).click();
    await page.getByText('Waiting for DNS').waitFor();
    await page.getByText('domains.example.net').waitFor();
    const post = calls.find(c => c.method === 'POST' && c.path.endsWith('/tracking-domains'));
    assert.deepEqual(post?.body, { domain: 'partners.northpeak.example', partner_program: second });
    assert(!calls.some(c => c.method === 'PUT' && c !== put), 'adding a domain also saved the portal');
    await shot(page, 'settings-portal-domain-desktop-dark');
    assert.deepEqual(errors, []);
    await page.close();
  }
  console.log('partner settings: ok');
} finally {
  await browser?.close();
  server.kill();
}
