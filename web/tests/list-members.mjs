// A list's page against mocked API answers: each member's name links to
// their record, the email, lifetime value and sign-up date columns, the
// Columns menu, and saving the columns picked as the viewer's default.
// Starts a local Vite server and mocks every API request; needs no backend.
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
const listId = '20000000-0000-0000-0000-000000000001';
const shots = process.env.OUTFLARE_TEST_SHOTS;
const list = { id: listId, brand_id: brand, name: 'Paying customers', description: '', kind: 'smart', record_type: 'person', rules: [], match: 'all', members: 3,
  refreshed_at: '2026-11-18T07:00:00Z', updated_at: '2026-11-18T07:00:00Z', created_at: '2026-11-01T00:00:00Z', error_code: null, query_id: null };
const members = [
  { id: '30000000-0000-0000-0000-000000000001', name: 'Ana Ruiz', email: 'ana@acme.example', entered_at: '2026-11-17T10:00:00Z', created_at: '2026-10-01T09:00:00Z', signed_up_at: '2026-10-02T09:00:00Z', lifetime_revenue: { currency: 'EUR', amount_minor: 6000 } },
  { id: '30000000-0000-0000-0000-000000000002', name: '', email: 'bo@acme.example', entered_at: '2026-11-16T10:00:00Z', created_at: '2026-09-01T09:00:00Z' },
  { id: '30000000-0000-0000-0000-000000000003', name: 'Cy Lin', email: 'cy@northpeak.example', entered_at: '2026-11-15T10:00:00Z', created_at: '2026-08-01T09:00:00Z', signed_up_at: '2026-08-03T09:00:00Z', lifetime_revenue: { currency: 'USD', amount_minor: 123456 } },
];

let browser;
const patches = [];
const views = new Map();
try {
  for (let i = 0; i < 100 && !output.includes('ready in'); i++) await new Promise(r => setTimeout(r, 200));
  browser = await chromium.launch({ headless: true, args: ['--no-sandbox'], executablePath: process.env.OUTFLARE_TEST_CHROMIUM || undefined });
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, locale: 'en-US' });
  const open = async (width = 1280) => {
    const p = await ctx.newPage();
    await p.setViewportSize({ width, height: 900 });
    const errors = []; p.on('pageerror', e => errors.push(e.message));
    await p.route('**/v1/**', async route => {
      const path = new URL(route.request().url()).pathname;
      let status = 200, body = {};
      if (path === `/v1/brands/${brand}/smart-lists/${listId}` && route.request().method() === 'PATCH') {
        const sent = route.request().postDataJSON();
        patches.push(sent);
        if (sent.name === 'Taken') { status = 409; body = { error: { code: 'conflict', message: 'a list with this name already exists', request_id: 'r' } }; }
        else body = Object.assign(list, sent);
      } else if (path === `/v1/brands/${brand}/smart-lists/${listId}`) body = list;
      else if (path === `/v1/brands/${brand}/smart-lists/${listId}/members`) body = { data: members, next_cursor: null };
      else if (path === '/v1/me/table-views') body = { data: [...views].map(([view, hidden]) => ({ view, hidden })) };
      else if (path.startsWith('/v1/me/table-views/')) {
        // Saved columns live with the account.
        const view = path.slice('/v1/me/table-views/'.length);
        if (route.request().method() === 'DELETE') { views.delete(view); status = 204; }
        else { views.set(view, route.request().postDataJSON().hidden); body = { view, hidden: views.get(view) }; }
      }
      else if (path === `/v1/brands/${brand}/catalog`) body = { types: [], events: [] };
      else if (path.endsWith('/lead-lists') || path.endsWith('/news/searches')) body = { data: [] };
      else { status = 404; body = { error: { code: 'not_found', message: 'nothing here', request_id: 'r' } }; }
      await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
    });
    await p.goto(`${origin}/tests/fixtures/list-members.html?list=${encodeURIComponent(JSON.stringify(list))}`);
    return { page: p, errors };
  };
  const headers = (p) => p.locator('table thead th').allTextContents().then((h) => h.map((x) => x.trim()).filter(Boolean));

  // Names link to the person; the new columns show.
  const a = await open();
  const ana = a.page.locator('table').getByRole('link', { name: 'Ana Ruiz' });
  await ana.waitFor();
  assert.match(await ana.getAttribute('href'), /people\/30000000-0000-0000-0000-000000000001$/);
  await a.page.locator('table').getByRole('link', { name: 'bo@acme.example' }).waitFor(); // no name: the email is the link
  assert.deepEqual(await headers(a.page), ['Name', 'Email', 'Lifetime value', 'Signed up', 'Joined list']);
  await a.page.locator('table').getByText('€60.00').waitFor();
  await a.page.locator('table').getByText('$1,234.56').waitFor();
  await a.page.locator('table').getByText('Oct 2, 2026').waitFor();
  await ana.click();
  assert.deepEqual(await a.page.evaluate(() => window.gone), [['people', '30000000-0000-0000-0000-000000000001']]);
  if (shots) await a.page.screenshot({ path: `${shots}/list-members.png`, fullPage: true });

  // Pick columns, save them as the default.
  await a.page.getByRole('button', { name: 'Columns' }).click();
  assert.equal(await a.page.getByRole('menuitemcheckbox', { name: 'Name' }).count(), 0, 'the name always shows');
  await a.page.getByRole('menuitemcheckbox', { name: 'Joined list' }).click();
  await a.page.getByRole('menuitemcheckbox', { name: 'First seen' }).click();
  if (shots) await a.page.screenshot({ path: `${shots}/list-members-columns.png`, fullPage: true });
  await a.page.getByRole('menuitem', { name: 'Save as my default' }).click();
  await a.page.getByText('These columns are your default').waitFor();
  assert.deepEqual(await headers(a.page), ['Name', 'Email', 'Lifetime value', 'Signed up', 'First seen']);
  assert.deepEqual(a.errors, []);

  assert.deepEqual(views.get('list-people'), ['joined'], 'the view is saved with the account');
  // The next visit, to any list of people, starts from the saved columns,
  // in another browser too: this one's own copy is thrown away first.
  await a.page.evaluate(() => localStorage.clear());
  const b = await open();
  await b.page.locator('table').getByRole('link', { name: 'Ana Ruiz' }).waitFor();
  assert.deepEqual(await headers(b.page), ['Name', 'Email', 'Lifetime value', 'Signed up', 'First seen']);
  await b.page.getByRole('button', { name: 'Columns' }).click();
  await b.page.getByRole('menuitem', { name: 'Back to standard columns' }).click();
  await b.page.getByText('Back to the standard columns').waitFor();
  assert.deepEqual(await headers(b.page), ['Name', 'Email', 'Lifetime value', 'Signed up', 'Joined list']);
  assert.equal(views.size, 0, 'the saved view is dropped from the account');
  const c = await open();
  await c.page.locator('table').getByRole('link', { name: 'Ana Ruiz' }).waitFor();
  assert.deepEqual(await headers(c.page), ['Name', 'Email', 'Lifetime value', 'Signed up', 'Joined list']);
  assert.deepEqual(b.errors, []);

  // Renaming it and describing it: a form in place, with Save and Cancel.
  const d = await open();
  await d.page.getByRole('button', { name: 'Edit name and description' }).click();
  await d.page.getByRole('textbox', { name: 'Name', exact: true }).fill('Something else');
  await d.page.getByRole('button', { name: 'Cancel' }).click();
  await d.page.getByRole('heading', { name: /Paying customers/ }).waitFor();
  assert.deepEqual(patches, [], 'Cancel saves nothing');
  await d.page.getByRole('button', { name: 'Edit name and description' }).click();
  await d.page.getByRole('textbox', { name: 'Name', exact: true }).fill('Taken');
  await d.page.getByRole('button', { name: 'Save' }).click();
  await d.page.getByText('a list with this name already exists').waitFor();
  await d.page.getByRole('textbox', { name: 'Name', exact: true }).fill('  All paying customers ');
  await d.page.getByRole('textbox', { name: /Description/ }).fill('Everyone who has ever paid.');
  if (shots) await d.page.screenshot({ path: `${shots}/list-edit.png`, fullPage: true });
  await d.page.getByRole('button', { name: 'Save' }).click();
  await d.page.getByRole('heading', { name: /All paying customers/ }).waitFor();
  await d.page.getByText('Everyone who has ever paid.').waitFor();
  assert.deepEqual(patches.at(-1), { name: 'All paying customers', description: 'Everyone who has ever paid.' });
  assert.deepEqual(d.errors, []);

  // Phone width: cards, the name still a link, nothing scrolls sideways.
  const phone = await open(390);
  await phone.page.locator('ul').getByRole('link', { name: 'Ana Ruiz' }).waitFor();
  assert.equal(await phone.page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, 'no sideways scroll at phone width');
  if (shots) await phone.page.screenshot({ path: `${shots}/list-members-phone.png`, fullPage: true });

  console.log('list members: ok');
} catch (e) {
  console.error(output);
  throw e;
} finally {
  await browser?.close();
  server.kill();
}
