// Pinned and Recents in the menu (web/src/shell.tsx, lib/menu.ts): "Pin to
// menu" in the top bar pins the page, pins and recents come from the
// person's account and show in the area they belong to, Recents can be
// cleared, and pins this browser kept before they were saved to the
// account are sent up once. Starts a local Vite server and mocks every API
// request; needs no backend.
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
  // The account's menu, and every write the app made to it.
  let account = { pins: [], recents: [] };
  const writes = [];
  const mock = (page, user = 'user-1') => page.route('**/v1/**', async route => {
    const u = new URL(route.request().url());
    const method = route.request().method();
    let status = 200, body = {};
    if (u.pathname.startsWith('/v1/me/menu')) {
      if (method !== 'GET') writes.push(`${method} ${u.pathname}`);
      if (method === 'GET') body = account;
      else if (method === 'PUT') { account.pins = route.request().postDataJSON().pins; body = { pins: account.pins }; }
      else if (method === 'POST') { account.recents = [{ ref: route.request().postDataJSON().ref, opened_at: new Date().toISOString() }, ...account.recents]; status = 204; }
      else { account.recents = []; status = 204; }
      await route.fulfill(status === 204 ? { status } : { status, contentType: 'application/json', body: JSON.stringify(body) });
      return;
    }
    if (u.pathname === '/v1/setup') body = { needed: false };
    else if (u.pathname === '/v1/me') body = me(user);
    else if (u.pathname === '/v1/meta') body = { name: 'Outflare', version: 'test', api: { rest: '/v1', openapi: '/v1/openapi.yaml' }, capabilities: [], login_methods: ['password'] };
    else { status = 404; body = { error: { code: 'not_found', message: 'nothing here', request_id: 'r' } }; }
    await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  });
  const menu = (page) => page.locator('[data-sidebar="content"]');
  const wrote = async (what) => { for (let n = 0; n < 50 && !writes.includes(what); n++) await new Promise(r => setTimeout(r, 100)); assert(writes.includes(what), `${what} was not sent: ${writes}`); };
  const thing = `/${ws}/-/sequences/20000000-0000-0000-0000-000000000002`;

  // 1. Pin the page you are on from the top bar: it is saved to the account
  // and shows under Pinned.
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const page = await context.newPage();
  const errors = []; page.on('pageerror', e => errors.push(e.message));
  await mock(page);
  await page.goto(`${origin}/${ws}/sequences`);
  await page.getByRole('button', { name: 'Pin to menu' }).click();
  await wrote('PUT /v1/me/menu/pins');
  assert.deepEqual(account.pins, ['sequences']);
  await page.getByRole('button', { name: 'Pinned', exact: true }).waitFor();
  await menu(page).getByText('Pinned', { exact: true }).waitFor();

  // 2. Pins and recents saved from another browser show after a reload: a
  // thing inside a page by the name this browser remembers, with how long
  // ago it was opened. Clear empties Recents and leaves the pins.
  account = { pins: ['sequences'], recents: [{ ref: thing, opened_at: new Date(Date.now() - 2 * 3600e3).toISOString() }, { ref: '/another-workspace/-/sequences/20000000-0000-0000-0000-000000000003', opened_at: new Date().toISOString() }] };
  await page.evaluate(([ref]) => localStorage.setItem('outflare:menu-labels', JSON.stringify({ [ref]: 'Q3 founders' })), [thing]);
  await page.reload();
  await menu(page).getByText('Recents', { exact: true }).waitFor({ timeout: 60000 });
  const recent = menu(page).getByRole('link', { name: 'Q3 founders' });
  await recent.waitFor();
  assert.equal(await menu(page).locator('li', { has: page.getByRole('link', { name: 'Q3 founders' }) }).getByText('2h').count(), 1, 'a recent says how long ago');
  assert.equal(await menu(page).getByText(/000003/).count(), 0, 'another workspace\'s things stay out');
  assert((await recent.getAttribute('href')).endsWith('/sequences/20000000-0000-0000-0000-000000000002'));
  await menu(page).getByRole('button', { name: 'Clear' }).click();
  await wrote('DELETE /v1/me/menu/recents');
  assert.equal(await menu(page).getByText('Recents', { exact: true }).count(), 0);
  await menu(page).getByText('Pinned', { exact: true }).waitFor();

  // 3. Unpin from the top bar.
  await page.getByRole('button', { name: 'Pinned', exact: true }).click();
  await page.getByRole('button', { name: 'Pin to menu' }).waitFor();
  assert.deepEqual(account.pins, []);
  assert.deepEqual(errors, []);
  await context.close();

  // 4. Pins this browser kept before they were saved to the account go up
  // once, when the account has none.
  {
    account = { pins: [], recents: [] };
    writes.length = 0;
    const c = await browser.newContext({ viewport: { width: 1440, height: 900 } });
    const pg = await c.newPage();
    await mock(pg);
    await pg.addInitScript(() => { if (!localStorage.getItem('nav')) localStorage.setItem('nav', JSON.stringify({ user: 'user-1', pins: ['sequences'], open: [], folded: [], version: 'v2' })); });
    await pg.goto(`${origin}/${ws}/overview`);
    await wrote('PUT /v1/me/menu/pins');
    assert.deepEqual(account.pins, ['sequences']);
    await menu(pg).getByRole('link', { name: 'Sequences' }).first().waitFor();
    if (shots) await pg.screenshot({ path: `${shots}/menu-pins.png` });
    await c.close();
  }
  await browser.close(); browser = undefined;
  console.log('ok');
} finally {
  if (browser) await browser.close();
  server.kill();
}
