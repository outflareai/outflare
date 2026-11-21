// The sidebar menu (web/src/shell.tsx): several modules stay open at once,
// opening a page never closes another, a heading folds its modules away,
// and what is open is remembered for the person who opened it. Starts a
// local Vite server and mocks every API request; needs no backend. With
// SHOTS=<dir> it also saves screenshots of the menu. This is the old menu,
// which people reach with "Old menu" on the new one, so the test starts
// from stored preferences that chose it.
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
const shots = process.env.SHOTS;
const ws = '10000000-0000-0000-0000-000000000001';
let browser;
try {
  let ready = false;
  for (let n = 0; n < 100; n++) { try { ready = (await fetch(origin)).ok; if (ready) break; } catch {} await new Promise(r => setTimeout(r, 100)); }
  assert(ready, `Vite did not start: ${output}`);
  browser = await chromium.launch({ headless: true, args: ['--no-sandbox'], executablePath: process.env.OUTFLARE_TEST_CHROMIUM || undefined });

  const me = (id) => ({ user: { id, name: 'Ana', email: 'ana@example.com', email_verified: true }, workspaces: [{ id: ws, name: 'Acme', slug: 'acme', test: false, records_edit_owners_only: false, role: 'owner', scopes: [], brand_ids: [], features: [] }] });
  const mock = (page, user = 'user-1') => page.route('**/v1/**', async route => {
    const u = new URL(route.request().url());
    let status = 200, body = {};
    if (u.pathname === '/v1/setup') body = { needed: false };
    else if (u.pathname === '/v1/me') body = me(user);
    else if (u.pathname === '/v1/meta') body = { name: 'Outflare', version: 'test', api: { rest: '/v1', openapi: '/v1/openapi.yaml' }, capabilities: [], login_methods: ['password'] };
    else { status = 404; body = { error: { code: 'not_found', message: 'nothing here', request_id: 'r' } }; }
    await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  });
  const menu = (page) => page.locator('[data-sidebar="content"]');
  const module = (page, name) => menu(page).getByRole('button', { name, exact: true });

  const open = async (page, name) => (await module(page, name).getAttribute('aria-expanded')) === 'true';
  const stored = (page) => page.evaluate(() => JSON.parse(localStorage.getItem('nav') ?? 'null'));

  // 1. Desktop: the page's module opens, opening others leaves it open, and
  // a reload keeps what was open.
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const page = await context.newPage();
  const errors = []; page.on('pageerror', e => errors.push(e.message));
  await mock(page);
  // Only on the first load, so the reloads below read what the app stored.
  await page.addInitScript(() => { if (!localStorage.getItem('nav')) localStorage.setItem('nav', JSON.stringify({ user: 'user-1', pins: [], open: [], folded: [], version: 'v1', chose: true })); });
  await page.goto(`${origin}/${ws}/sequences`);
  await module(page, 'Outreach').waitFor({ timeout: 60000 });
  assert(await open(page, 'Outreach'), 'the page\'s module opens');
  await module(page, 'CRM').click();
  await module(page, 'SEO').click();
  assert(await open(page, 'Outreach') && await open(page, 'CRM') && await open(page, 'SEO'), 'several modules stay open');
  // Going to a page in another module leaves the rest open.
  // CRM's Deals.
  await menu(page).getByRole('link', { name: 'Deals', exact: true }).first().click();
  assert(await open(page, 'Outreach') && await open(page, 'SEO'), 'a page in another module closes nothing');
  await module(page, 'SEO').click();
  assert(!(await open(page, 'SEO')) && await open(page, 'Outreach'), 'a module closes on its own');
  assert.deepEqual((await stored(page)).open.sort(), ['crm', 'outreach']);
  await page.reload();
  await module(page, 'Outreach').waitFor();
  assert(await open(page, 'Outreach') && await open(page, 'CRM') && !(await open(page, 'SEO')), 'open modules survive a reload');

  // 2. A heading folds its modules away and stays folded; the module of the
  // page you are on stays in sight.
  const heading = (name) => menu(page).getByRole('button', { name: new RegExp(`^${name}`) });
  await heading('Ads').click();
  assert.equal(await module(page, 'Paid ads').count(), 0, 'a folded heading hides its modules');
  await heading('Customers').click();
  assert.equal(await menu(page).getByRole('link', { name: 'Needs attention', exact: true }).count(), 0);
  assert(await module(page, 'CRM').isVisible(), 'the module you are in stays in a folded heading');
  if (shots) {
    await heading('Customers').click();
    await heading('Ads').click();
  }
  await page.reload();
  await module(page, 'CRM').waitFor();
  if (!shots) assert.equal(await module(page, 'Paid ads').count(), 0, 'folds survive a reload');

  // 3. Close every group: one button closes them all.
  await page.getByRole('button', { name: 'Close every open menu group' }).click();
  assert(!(await open(page, 'Outreach')) && !(await open(page, 'CRM')), 'every module closes');
  assert.deepEqual((await stored(page)).open, []);
  assert.deepEqual(errors, []);

  // 4. Someone else signing in on this browser starts from the defaults:
  // the new menu, and nothing open once they switch to the old one.
  {
    const other = await context.newPage();
    await mock(other, 'user-2');
    await other.goto(`${origin}/${ws}/overview`);
    await other.getByRole('button', { name: 'Old menu' }).click();
    await module(other, 'Outreach').waitFor();
    assert(!(await open(other, 'Outreach')) && !(await open(other, 'CRM')));
    await other.close();
  }

  if (shots) {
    // The menu on a page, with its group open and two headings folded.
    for (const theme of ['light', 'dark']) {
      const c = await browser.newContext({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2, colorScheme: theme });
      const pg = await c.newPage();
      await mock(pg);
      await pg.addInitScript(([t]) => { localStorage.setItem('theme', t); localStorage.setItem('nav', JSON.stringify({ user: 'user-1', pins: [], open: ['outreach'], folded: ['social', 'ads'], version: 'v1', chose: true })); }, [theme]);
      await pg.goto(`${origin}/${ws}/sequences`);
      await module(pg, 'Outreach').waitFor({ timeout: 60000 });
      await pg.waitForTimeout(500);
      await pg.screenshot({ path: `${shots}/desktop-${theme}.png` });
      await menu(pg).evaluate(el => { el.scrollTop = el.scrollHeight; });
      await pg.screenshot({ path: `${shots}/desktop-scrolled-${theme}.png` });
      await pg.setViewportSize({ width: 1280, height: 720 });
      await pg.waitForTimeout(400);
      await pg.screenshot({ path: `${shots}/laptop-${theme}.png` });
      await c.close();
      const ph = await browser.newContext({ ...(await import('playwright')).devices['iPhone 15'], colorScheme: theme });
      const pp = await ph.newPage();
      await mock(pp);
      await pp.addInitScript(([t]) => { localStorage.setItem('theme', t); localStorage.setItem('nav', JSON.stringify({ user: 'user-1', pins: [], open: ['outreach'], folded: ['social', 'ads'], version: 'v1', chose: true })); }, [theme]);
      await pp.goto(`${origin}/${ws}/sequences`);
      await pp.getByRole('button', { name: /toggle sidebar/i }).first().click();
      await module(pp, 'Outreach').waitFor({ timeout: 60000 });
      await pp.waitForTimeout(500);
      await pp.screenshot({ path: `${shots}/phone-${theme}.png` });
      await ph.close();
    }
  }
  await browser.close(); browser = undefined;
  console.log('ok');
} finally {
  if (browser) await browser.close();
  server.kill();
}
