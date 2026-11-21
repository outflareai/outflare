// Error capture is one switch: on the Errors page and on the script under
// Website tracking, against mocked API answers. Off says what turning it on
// does; flipping it saves capture_errors on the brand's script at once; on
// says it is capturing on the site, or that the script has gone quiet; a
// brand with no script gets one button to add it. Starts a local Vite
// server and mocks every API request; needs no backend.
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
const keyID = '20000000-0000-0000-0000-000000000001';
const snippet = '<script async src="https://app.example.test/site.js" data-key="ofk_test_not_a_real_key"></script>';
const shots = process.env.OUTFLARE_TEST_SHOTS;

const siteKey = (lastVisit, on, pixelID = '') => ({
  id: keyID, brand_id: brand, name: 'Acme site', origins: ['https://*.acme.example'], excluded_params: [], tests: true, capture_clicks: false, capture_forms: false, capture_errors: on,
  chatgpt_pixel: false, chatgpt_pixel_id: pixelID,
  flicker_guard: '', snippet, events_today: 222, bots_today: 0, last_used_at: lastVisit === null ? null : new Date(Date.now() - lastVisit).toISOString(),
  created_at: '2026-01-01T00:00:00Z', rotated_at: null, previous_expires_at: null, proxy_secret_at: null, identify_secret_at: null,
  own_domain: { status: 'none', events: 0, through_own_domain: 0, safari_direct: 0, snippet },
});
const noErrors = { data: [], counts: { open: 0, resolved: 0, ignored: 0 }, errors: 0 };

let browser;
try {
  for (let i = 0; i < 100 && !output.includes('ready in'); i++) await new Promise(r => setTimeout(r, 200));
  browser = await chromium.launch({ headless: true, args: ['--no-sandbox'], executablePath: process.env.OUTFLARE_TEST_CHROMIUM || undefined });

  // key: the brand's script, or null for none. Returns the page and the
  // bodies of every PATCH it sent.
  const open = async (key, page = 'errors', width = 1280) => {
    const p = await browser.newPage({ viewport: { width, height: 900 } });
    const errors = []; p.on('pageerror', e => errors.push(e.message));
    const patches = [];
    let current = key;
    await p.route('**/v1/**', async route => {
      const path = new URL(route.request().url()).pathname;
      let status = 200, body = {};
      if (path === '/v1/workspaces/workspace-a/brands') body = { data: [{ id: brand, name: 'Acme', handle: 'acme', status: 'active', timezone: 'Europe/Berlin' }] };
      else if (path === `/v1/brands/${brand}/site-keys`) body = { data: current ? [current] : [] };
      else if (path === `/v1/brands/${brand}/site-keys/${keyID}` && route.request().method() === 'PATCH') {
        const b = route.request().postDataJSON(); patches.push(b); current = { ...current, ...b }; body = current;
      } else if (path === `/v1/brands/${brand}/errors`) body = noErrors;
      else if (path === '/v1/workspaces/workspace-a/brands/acme/setup') body = { brand_id: brand, website: 'https://acme.example', done: 1, total: 1, steps: [{ id: 'site_script', title: 'Add the website script', detail: '', status: 'done', done: true, skipped: false }] };
      else if (path === '/v1/workspaces/workspace-a/connectors') body = { data: [] };
      else { status = 404; body = { error: { code: 'not_found', message: 'nothing here', request_id: 'r' } }; }
      await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
    });
    await p.goto(`${origin}/tests/fixtures/error-capture.html?page=${page}`);
    return { page: p, errors, patches };
  };

  // Off: says so and what turning it on does; the switch turns it on here.
  const off = await open(siteKey(60_000, false));
  await off.page.getByText(/Switch it on to send errors on/).waitFor();
  await off.page.getByText('acme.example', { exact: true }).first().waitFor();
  if (shots) await off.page.screenshot({ path: `${shots}/errors-capture-off.png`, fullPage: true });
  await off.page.getByRole('switch', { name: 'Capture errors' }).click();
  await off.page.getByText(/Capturing errors on/).waitFor();
  await off.page.getByText(/no errors so far/).waitFor();
  assert.deepEqual(off.patches, [{ capture_errors: true }]);
  await off.page.getByText('Error capture is on, so new errors show here').waitFor();
  if (shots) await off.page.screenshot({ path: `${shots}/errors-capture-on.png`, fullPage: true });
  assert.deepEqual(off.errors, []);

  // On, but the script has sent nothing for days: says so, links to check it.
  const quiet = await open(siteKey(3 * 864e5, true));
  await quiet.page.getByText('On, script quiet').waitFor();
  await quiet.page.getByRole('button', { name: 'Open Website tracking' }).click();
  assert.deepEqual(await quiet.page.evaluate(() => window.gone), ['tracking']);
  assert.deepEqual(quiet.errors, []);

  // No script: one button to add it.
  const none = await open(null);
  await none.page.getByRole('button', { name: 'Add the website script' }).click();
  assert.deepEqual(await none.page.evaluate(() => window.gone), ['tracking']);
  if (shots) await none.page.screenshot({ path: `${shots}/errors-capture-no-script.png`, fullPage: true });
  assert.deepEqual(none.errors, []);

  // Someone who can't change the script sees the state and who can.
  const viewer = await open(siteKey(60_000, false), 'viewer');
  await viewer.page.getByText('An owner or admin can switch this.').waitFor();
  assert.equal(await viewer.page.getByRole('switch', { name: 'Capture errors' }).isDisabled(), true);

  // Website tracking: the same switch, saved at once, without Edit.
  const tracking = await open(siteKey(60_000, false), 'tracking');
  await tracking.page.getByText('What the script does on its own').waitFor();
  await tracking.page.getByRole('switch', { name: 'Capture errors' }).click();
  await tracking.page.getByRole('link', { name: 'Open Errors' }).waitFor();
  assert.deepEqual(tracking.patches, [{ capture_errors: true }]);
  if (shots) await tracking.page.screenshot({ path: `${shots}/tracking-capture-switches.png`, fullPage: true });
  assert.deepEqual(tracking.errors, []);

  // The ChatGPT Ads pixel: without a Pixel ID on the brand's connection the
  // switch waits and says where to add it; with one it saves at once.
  assert.equal(await tracking.page.getByRole('switch', { name: 'Load the ChatGPT Ads pixel' }).isDisabled(), true);
  await tracking.page.getByText('Add your Pixel ID first').waitFor();
  assert.match(await tracking.page.getByRole('link', { name: 'Paid ads, Accounts' }).getAttribute('href'), /\/ads\/accounts$/);
  const pixel = await open(siteKey(60_000, false, 'px_test_1'), 'tracking');
  await pixel.page.getByRole('switch', { name: 'Load the ChatGPT Ads pixel' }).click();
  await pixel.page.getByText('Your developer can take the pixel\'s own snippet off the site.').waitFor();
  await pixel.page.getByText('px_test_1').waitFor();
  assert.deepEqual(pixel.patches, [{ chatgpt_pixel: true }]);
  if (shots) await pixel.page.screenshot({ path: `${shots}/tracking-chatgpt-pixel.png`, fullPage: true });
  assert.deepEqual(pixel.errors, []);

  // Phone width: nothing scrolls sideways.
  const phone = await open(siteKey(60_000, true), 'errors', 390);
  await phone.page.getByText(/Capturing errors on/).waitFor();
  assert.equal(await phone.page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, 'no sideways scroll at phone width');
  if (shots) await phone.page.screenshot({ path: `${shots}/errors-capture-phone.png`, fullPage: true });

  console.log('error capture: ok');
} catch (e) {
  console.error(output);
  throw e;
} finally {
  await browser?.close();
  server.kill();
}
