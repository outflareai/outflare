// The API panel (top right "API" button), in the real app on a real path.
// Starts a local Vite server and mocks every API request; needs no backend.
// Checks that the example is copy-paste ready: the real brand and item ids
// are in it, and "Create a token for this" makes a read-only token for this
// brand only, shows it once in the snippet, and never stores it.
// OUTFLARE_TEST_SHOTS=<dir> also saves screenshots.
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
const list = '60000000-0000-4000-8000-000000000001';
const fakeToken = 'oft_test_not_a_real_token';
const t0 = '2026-11-02T10:00:00Z';
const calls = [];

function answer(method, p, body) {
  if (p === '/v1/setup') return { needed: false };
  if (p === '/v1/me') return { user: { id: 'u1', name: 'Owner', email: 'owner@example.com', email_verified: true }, workspaces: [{ id: ws, slug: 'acme', name: 'Acme', role: 'owner', scopes: ['workspace.manage', 'tokens.manage', 'leads.read', 'leads.manage'] }] };
  if (p === '/v1/meta') return { name: 'Outflare', version: 'test', capabilities: [], login_methods: ['password'] };
  if (p === `/v1/workspaces/${ws}/brands`) return { data: [{ id: brand, name: 'Northpeak', handle: 'northpeak', status: 'active' }] };
  if (p === `/v1/workspaces/${ws}/stats`) return { unread_replies: 0, actions_waiting: 0 };
  if (p === `/v1/workspaces/${ws}/api-tokens` && method === 'POST') {
    return { token: fakeToken, api_token: { id: 't1', name: body.name, scopes: body.scopes, brand_id: body.brand, created_at: t0 } };
  }
  return null;
}

let browser;
try {
  let ready = false;
  for (let n = 0; n < 100; n++) { try { ready = (await fetch(origin)).ok; if (ready) break; } catch {} await new Promise(r => setTimeout(r, 100)); }
  assert(ready, `Vite did not start: ${output}`);
  browser = await chromium.launch({ headless: true, args: ['--no-sandbox'], executablePath: process.env.OUTFLARE_TEST_CHROMIUM || undefined });
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: shots ? 2 : 1 });
  const errors = []; page.on('pageerror', e => errors.push(e.message));
  await page.route('**/v1/**', async route => {
    const req = route.request(); const u = new URL(req.url());
    let sent = null; try { sent = req.postDataJSON(); } catch {}
    calls.push({ method: req.method(), path: u.pathname, body: sent });
    const body = answer(req.method(), u.pathname, sent);
    await route.fulfill(body === null
      ? { status: 404, contentType: 'application/json', body: JSON.stringify({ error: { code: 'not_found', message: 'nothing here', request_id: 'r' } }) }
      : { status: req.method() === 'POST' ? 201 : 200, contentType: 'application/json', body: JSON.stringify(body) });
  });

  await page.goto(`${origin}/acme/northpeak/lists/${list}`);
  await page.getByRole('button', { name: 'API', exact: true }).click();
  const panel = page.getByRole('complementary', { name: 'API for this page' });
  await panel.waitFor();
  const code = panel.locator('pre code').first();
  await code.waitFor();

  // 1. The real brand and list ids, no placeholders; the token stays an env var.
  let text = await code.innerText();
  assert.match(text, new RegExp(`/v1/brands/${brand}/lead-lists/${list}`), text);
  assert.doesNotMatch(text, /BRAND_ID|\{brand\}/, text);
  assert.match(text, /Bearer \$OUTFLARE_TOKEN/);
  if (shots) await page.screenshot({ path: join(shots, 'api-panel.png') });

  // 2. One click makes a read-only token for this brand only, shown in the snippet.
  await panel.getByRole('button', { name: 'Create a token for this' }).click();
  await panel.getByText("this token won't be shown again", { exact: false }).waitFor();
  const made = calls.find(c => c.method === 'POST' && c.path.endsWith('/api-tokens'));
  assert.deepEqual(made.body.scopes, ['leads.read']);
  assert.equal(made.body.brand, brand);
  assert.equal(made.body.expires_in_days, 30);
  text = await code.innerText();
  assert.match(text, new RegExp(`Bearer ${fakeToken}`));
  if (shots) await page.screenshot({ path: join(shots, 'api-panel-token.png') });

  // 3. Never stored in the browser.
  const stored = await page.evaluate(() => JSON.stringify({ ...localStorage }) + JSON.stringify({ ...sessionStorage }));
  assert.doesNotMatch(stored, /oft_/);

  // 4. Closing the panel forgets it.
  await panel.getByRole('button', { name: 'Close API panel' }).click();
  await page.getByRole('button', { name: 'API', exact: true }).click();
  text = await page.getByRole('complementary', { name: 'API for this page' }).locator('pre code').first().innerText();
  assert.match(text, /Bearer \$OUTFLARE_TOKEN/);

  assert.deepEqual(errors, []);
  console.log('api panel: ok');
} finally {
  await browser?.close();
  server.kill();
}
