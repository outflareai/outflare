// The Errors list and an error's page against mocked API answers: status
// tabs with counts, search and filters, selecting rows and acting on them
// at once (one bulk request, with Undo), each row's actions, and on an
// error's page the fix prompt for an AI coding assistant, copying, and
// delete asking first. Starts a local Vite server and mocks every API
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

const brand = '10000000-0000-0000-0000-000000000001';
const shots = process.env.OUTFLARE_TEST_SHOTS;
const ago = (ms) => new Date(Date.now() - ms).toISOString();
const issue = (fp, type, message, extra = {}) => ({ fingerprint: fp, type, message, status: 'open', status_at: null, regressed: false, new: false, count: 10, visitors: 4,
  first_seen: ago(5 * 864e5), last_seen: ago(60_000), last_path: '/checkout', source: 'browser', handled: false, pages: 1, ...extra });
const issues = [
  issue('aaaa', 'TypeError', "Cannot read properties of undefined (reading 'price')", { count: 120, visitors: 48, pages: 3, new: true }),
  issue('bbbb', 'RangeError', 'Maximum call stack size exceeded', { count: 30, regressed: true, status_at: ago(2 * 864e5), last_path: '/pricing' }),
  issue('cccc', 'Error', 'Payment provider timed out', { count: 8, source: 'api', handled: true, last_path: '/api/pay' }),
];
const detail = {
  issue: issues[0], from: ago(7 * 864e5), to: ago(0),
  days: Array.from({ length: 8 }, (_, n) => ({ day: `2026-11-${String(10 + n).padStart(2, '0')}`, count: n * 3 })),
  pages: [{ path: '/checkout', count: 100 }, { path: '/pricing', count: 15 }, { path: '/', count: 5 }],
  occurrences: [{ id: '30000000-0000-0000-0000-000000000001', at: ago(60_000), type: 'TypeError', message: "Cannot read properties of undefined (reading 'price')",
    stack: "TypeError: Cannot read properties of undefined (reading 'price')\n    at t (https://acme.example/assets/index-Bx81kd2.js:1:2345)",
    stack_original: "TypeError: Cannot read properties of undefined (reading 'price')\n    at planTotal (src/checkout/total.ts:42:18)\n    at Checkout (src/checkout/Checkout.tsx:88:5)",
    handled: false, path: '/checkout', url: 'https://acme.example/checkout', source: 'browser', anonymous_id: 'anon-1', record_id: null,
    device: 'desktop', browser: 'Chrome', os: 'macOS', release: 'v2.4.1' }],
};

let browser;
try {
  for (let i = 0; i < 100 && !output.includes('ready in'); i++) await new Promise(r => setTimeout(r, 200));
  browser = await chromium.launch({ headless: true, args: ['--no-sandbox'], executablePath: process.env.OUTFLARE_TEST_CHROMIUM || undefined });

  const open = async (page = 'errors', width = 1280) => {
    const ctx = await browser.newContext({ viewport: { width, height: 900 } });
    await ctx.grantPermissions(['clipboard-read', 'clipboard-write'], { origin });
    const p = await ctx.newPage();
    const errors = []; p.on('pageerror', e => errors.push(e.message));
    const sent = [];
    await p.route('**/v1/**', async route => {
      const req = route.request();
      const path = new URL(req.url()).pathname;
      let status = 200, body = {};
      if (req.method() !== 'GET') sent.push({ method: req.method(), path, body: req.postDataJSON?.() ?? null });
      if (path === '/v1/workspaces/workspace-a/brands') body = { data: [{ id: brand, name: 'Acme', handle: 'acme', status: 'active', timezone: 'Europe/Berlin' }] };
      else if (path === `/v1/brands/${brand}/site-keys`) body = { data: [] };
      else if (path === `/v1/brands/${brand}/errors`) body = { from: ago(7 * 864e5), to: ago(0), days: 7, errors: 158, counts: { open: 3, resolved: 2, ignored: 1, archived: 4 }, data: issues };
      else if (path === `/v1/brands/${brand}/errors/bulk`) body = { action: req.postDataJSON().action, changed: req.postDataJSON().fingerprints.length, skipped: 0, fingerprints: req.postDataJSON().fingerprints };
      else if (path.startsWith(`/v1/brands/${brand}/errors/`) && req.method() === 'GET') body = { ...detail, issue: issues.find((i) => path.endsWith(i.fingerprint)) ?? issues[0] };
      else if (path.startsWith(`/v1/brands/${brand}/errors/`) && req.method() === 'PATCH') body = { fingerprint: path.split('/').pop(), status: req.postDataJSON().status, status_at: ago(0) };
      else if (path.startsWith(`/v1/brands/${brand}/errors/`) && req.method() === 'DELETE') body = { deleted: 1, errors_deleted: 120 };
      else { status = 404; body = { error: { code: 'not_found', message: 'nothing here', request_id: 'r' } }; }
      await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
    });
    await p.goto(`${origin}/tests/fixtures/error-capture.html?page=${page}`);
    return { page: p, errors, sent };
  };

  // The list: tabs with counts, filters, the selection bar.
  const list = await open();
  await list.page.getByRole('tab', { name: /Archived\s*4/ }).waitFor();
  await list.page.getByRole('tab', { name: /Fixed\s*2/ }).waitFor();
  await list.page.getByText('+2 more pages').waitFor();
  await list.page.getByLabel('Search errors').fill('stack size');
  await list.page.getByText('1 of 3').waitFor();
  await list.page.getByRole('button', { name: 'Clear filters' }).click();
  await list.page.getByLabel('Which errors').selectOption('back');
  await list.page.getByText('1 of 3').waitFor();
  await list.page.getByLabel('Which errors').selectOption('all');
  await list.page.getByLabel('Where').selectOption('server');
  await list.page.locator('table').getByText('Payment provider timed out').waitFor();
  assert.equal(await list.page.locator('table').getByText('Maximum call stack size exceeded').count(), 0);
  await list.page.getByLabel('Where').selectOption('all');

  // Select two, ignore them at once: one bulk request, with Undo.
  await list.page.getByRole('checkbox', { name: /Select TypeError/ }).click();
  await list.page.getByRole('checkbox', { name: /Select RangeError/ }).click();
  await list.page.getByText('2 selected').waitFor();
  if (shots) await list.page.screenshot({ path: `${shots}/errors-list-selected.png`, fullPage: true });
  await list.page.getByRole('toolbar', { name: 'Selected errors' }).getByRole('button', { name: 'Ignore' }).click();
  await list.page.getByText('2 errors ignored').waitFor();
  assert.deepEqual(list.sent.at(-1), { method: 'POST', path: `/v1/brands/${brand}/errors/bulk`, body: { fingerprints: ['aaaa', 'bbbb'], action: 'ignored' } });
  await list.page.getByRole('button', { name: 'Undo' }).click();
  await list.page.waitForFunction(() => true);
  for (let i = 0; i < 50 && list.sent.length < 2; i++) await new Promise(r => setTimeout(r, 100));
  assert.deepEqual(list.sent.at(-1), { method: 'POST', path: `/v1/brands/${brand}/errors/bulk`, body: { fingerprints: ['aaaa', 'bbbb'], action: 'open' } });

  // Select every row shown, then copy one prompt for them all.
  await list.page.getByRole('checkbox', { name: 'Select every error shown' }).click();
  await list.page.getByText('3 selected').waitFor();
  await list.page.getByRole('toolbar', { name: 'Selected errors' }).getByRole('button', { name: 'Copy fix prompt' }).click();
  await list.page.getByText(/Fix prompt copied for 3 errors/).waitFor();
  const many = await list.page.evaluate(() => navigator.clipboard.readText());
  assert.match(many, /These 3 errors are happening in production/);
  assert.match(many, /## Error 3/);

  // A row's own actions: mark one fixed.
  await list.page.getByRole('button', { name: /Actions for RangeError/ }).click();
  await list.page.getByRole('menuitem', { name: 'Mark fixed' }).click();
  await list.page.getByText('Error marked fixed').waitFor();
  assert.deepEqual(list.sent.at(-1), { method: 'PATCH', path: `/v1/brands/${brand}/errors/bbbb`, body: { status: 'resolved' } });
  assert.deepEqual(list.errors, []);

  // An error's page: the prompt names the mapped stack, browser and release.
  const one = await open('errors&item=aaaa');
  await one.page.getByRole('heading', { name: /Cannot read properties of undefined/ }).waitFor();
  await one.page.getByText('Chrome on macOS (desktop)').first().waitFor();
  await one.page.getByRole('button', { name: 'Show prompt' }).click();
  const shown = await one.page.locator('section[aria-label="Fix it with AI"] pre').innerText();
  assert.match(shown, /src\/checkout\/total\.ts:42:18/);
  assert.match(shown, /Release: v2\.4\.1/);
  assert.match(shown, /Pages: \/checkout \(100\)/);
  assert.doesNotMatch(shown, /anon-1/, 'no visitor ids in the prompt');
  if (shots) await one.page.screenshot({ path: `${shots}/error-page.png`, fullPage: true });
  await one.page.getByRole('button', { name: 'Copy fix prompt for AI' }).click();
  assert.match(await one.page.evaluate(() => navigator.clipboard.readText()), /Fix the cause, not the symptom/);
  // Delete asks first, then goes back to the list.
  await one.page.getByRole('button', { name: 'More actions' }).click();
  await one.page.getByRole('menuitem', { name: 'Delete' }).click();
  await one.page.getByText("This can't be undone").waitFor();
  await one.page.getByRole('button', { name: 'Delete error' }).click();
  for (let i = 0; i < 50 && !one.sent.some((s) => s.method === 'DELETE'); i++) await new Promise(r => setTimeout(r, 100));
  assert.equal(one.sent.at(-1).method, 'DELETE');
  await one.page.waitForFunction(() => window.gone.length > 0);
  assert.deepEqual(await one.page.evaluate(() => window.gone), ['errors']);
  assert.deepEqual(one.errors, []);

  // Phone width: rows are cards and nothing scrolls sideways.
  const phone = await open('errors', 390);
  await phone.page.locator('ul').getByText('Payment provider timed out').waitFor();
  assert.equal(await phone.page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, 'no sideways scroll at phone width');
  if (shots) await phone.page.screenshot({ path: `${shots}/errors-list-phone.png`, fullPage: true });

  console.log('errors actions: ok');
} catch (e) {
  console.error(output);
  throw e;
} finally {
  await browser?.close();
  server.kill();
}
