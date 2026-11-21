// The connect dialog against a mocked API: a long key and a refusal whose
// message has no spaces to break at (a provider's raw JSON) stay inside the
// dialog, on a desktop and on a phone. Starts a local Vite server and mocks
// every API request; needs no backend.
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

const refusal = 'Acme refused the request (HTTP 400): {"error":{"message":"no","code":200,"type":"OAuthException","error_subcode":1234567,"trace_id":"' + 'A'.repeat(120) + '"}}';

let browser;
try {
  for (let i = 0; i < 100 && !output.includes('ready in'); i++) await new Promise(r => setTimeout(r, 200));
  browser = await chromium.launch({ headless: true, args: ['--no-sandbox'], executablePath: process.env.OUTFLARE_TEST_CHROMIUM || undefined });
  for (const viewport of [{ width: 1280, height: 900 }, { width: 390, height: 844 }]) {
    const page = await browser.newPage({ viewport });
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    await page.route('**/v1/**', async route => {
      const req = route.request(); const p = new URL(req.url()).pathname;
      if (p === '/v1/workspaces/workspace-a/integrations' && req.method() === 'POST')
        return route.fulfill({ status: 422, contentType: 'application/json', body: JSON.stringify({ error: { code: 'connector_invalid', message: refusal, request_id: 'r' } }) });
      await route.fulfill({ status: 200, contentType: 'application/json', body: '{}' });
    });
    await page.goto(`${origin}/tests/fixtures/connect-dialog.html`);
    await page.getByLabel('System user access token').fill('t'.repeat(400));
    await page.getByRole('button', { name: 'Test and connect' }).click();
    const alert = page.getByRole('alert').filter({ hasText: 'refused the request' });
    await alert.waitFor();
    const box = await page.getByRole('dialog').boundingBox();
    for (const [what, el] of [['the error', alert], ['the key field', page.getByLabel('System user access token')], ['the button', page.getByRole('button', { name: 'Test and connect' })]]) {
      const b = await el.boundingBox();
      assert.ok(b.x >= box.x - 1 && b.x + b.width <= box.x + box.width + 1, `${what} stays inside the dialog at ${viewport.width}px (${Math.round(b.x + b.width)} > ${Math.round(box.x + box.width)})`);
    }
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1), 'no sideways scroll');
    assert.deepEqual(errors, []);
    await page.close();
  }
  console.log('connect dialog: ok');
} catch (e) {
  console.error(output);
  throw e;
} finally {
  await browser?.close();
  server.kill();
}
