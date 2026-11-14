// A partner program's fraud review queue, the Partners page's Review tab,
// in the real app on its real path (/partners). Starts a local Vite server
// and mocks every API request; needs no backend. Open flags show their
// signal in words, the partner and the cash they hold; Clear sends the
// note and Confirm can suspend the partner; the status filter shows
// resolved flags; it doesn't scroll sideways on a phone.
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

const now = new Date().toISOString();
const partner = '50000000-0000-4000-8000-000000000001';
const flag = (id, signal, detail, status = 'open') => ({ id, program_id: first, partner_id: partner, partner_code: 'ana', conversion_id: '60000000-0000-4000-8000-000000000001',
  conversion_type: 'sale', amount_cents: 10000, currency: 'USD', signal, detail, status, note: '', held_cents: 2000, resolved_at: null, created_at: now });
let flags = [
  flag('70000000-0000-4000-8000-000000000001', 'fast_conversion', { seconds: 3, threshold: 10 }),
  flag('70000000-0000-4000-8000-000000000002', 'repeat_click', { clicks: 14, threshold: 10 }),
];
const calls = [];
function answer(method, u, body) {
  const p = u.pathname;
  if (p === '/v1/setup') return { needed: false };
  if (p === '/v1/me') return { user: { id: 'user-1', name: 'Owner', email: 'owner@example.com', email_verified: true }, workspaces: [{ id: ws, slug: 'acme', name: 'Acme', role: 'owner', scopes: ['workspace.manage', 'partners.read', 'partners.manage'] }] };
  if (p === '/v1/meta') return { name: 'Outflare', version: 'test', capabilities: [], login_methods: ['password'] };
  if (p === `/v1/workspaces/${ws}/brands`) return { data: [{ id: brand, name: 'Northpeak', handle: 'northpeak', status: 'active' }] };
  if (p === `/v1/workspaces/${ws}/stats`) return { unread_replies: 0, link_pitches_waiting: 0, actions_waiting: 0 };
  const b = `/v1/brands/${brand}`;
  if (p === `${b}/partner-programs`) return { data: [program(first, 'Main program')] };
  if (p === `${b}/partner-flags`) return { data: flags.filter(f => f.status === (u.searchParams.get('status') || 'open') && f.program_id === u.searchParams.get('program')), next_cursor: null };
  const m = p.match(/partner-flags\/([^/]+)\/(clear|confirm)$/);
  if (m && method === 'POST') {
    const f = flags.find(x => x.id === m[1]);
    f.status = m[2] === 'clear' ? 'cleared' : 'confirmed'; f.note = body.note || ''; f.resolved_at = now; f.held_cents = m[2] === 'confirm' ? 0 : f.held_cents;
    return f;
  }
  if (p.endsWith('/stats') && p.startsWith(`${b}/partner-programs/`)) return { clicks: 0, referrals: 0, customers: 0, sales_cents: 0, pending_cents: 0, approved_cents: 0, paid_cents: 0, balance_cents: 0, pending_actions: 0, held_at_source: { pending_cents: 0, approved_cents: 0 }, partners: { total: 0, active: 0, pending: 0, suspended: 0, rejected: 0, missing_payout_details: 0 } };
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

  {
    const { page, errors } = await open(1440, 900);
    await page.goto(`${origin}/acme/northpeak/partners`);
    await page.getByRole('tab', { name: 'Review' }).click();
    const table = page.getByTestId('partner-flags');
    await table.getByText('Instant conversion').waitFor();
    await table.getByText('3s after the click').waitFor();
    await table.getByText('14 clicks (flagged from 10)').waitFor();
    assert.equal(await table.getByText('$20.00').count(), 2, 'the held cash is shown');
    await shot(page, 'review-desktop-dark');

    // Clear sends the note; the flag leaves the open list.
    await table.getByRole('row').filter({ hasText: 'Instant conversion' }).getByRole('button', { name: 'Clear' }).click();
    await page.getByRole('alertdialog').getByRole('textbox').fill('a returning customer');
    await page.getByRole('alertdialog').getByRole('button', { name: 'Clear flag' }).click();
    await page.getByText('Flag cleared').waitFor();
    await table.getByText('Instant conversion').waitFor({ state: 'detached' });
    const cleared = calls.find(c => c.method === 'POST' && c.path.endsWith('/clear'));
    assert.deepEqual(cleared.body, { note: 'a returning customer' });

    // Confirm and suspend.
    await table.getByRole('button', { name: 'Confirm fraud' }).click();
    await page.getByRole('alertdialog').getByRole('button', { name: 'Confirm and suspend partner' }).click();
    await page.getByText('Fraud confirmed and partner suspended').waitFor();
    const confirmed = calls.find(c => c.method === 'POST' && c.path.endsWith('/confirm'));
    assert.equal(confirmed.body.suspend_partner, true);
    await page.getByText('Nothing to review').waitFor();

    // Resolved flags show under their status.
    await page.getByLabel('Flag status').selectOption('cleared');
    await table.getByText('“a returning customer”').waitFor();
    await table.getByText('Cleared', { exact: true }).waitFor();
    assert.deepEqual(errors, []);
    await page.close();
  }

  // On a phone: no sideways scroll.
  {
    flags.push(flag('70000000-0000-4000-8000-000000000003', 'payout_matches_customer', {}));
    const { page, errors } = await open(390, 844, 'light');
    await page.goto(`${origin}/acme/northpeak/partners`);
    await page.getByRole('tab', { name: 'Review' }).click();
    await page.getByTestId('partner-flags').getByText('Payout matches customer').waitFor();
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1), 'no sideways scroll on a phone');
    await shot(page, 'review-phone-light');
    assert.deepEqual(errors, []);
    await page.close();
  }
  console.log('partner review: ok');
} catch (e) {
  console.error(output);
  throw e;
} finally {
  await browser?.close();
  server.kill();
}
