// Settings → Website tracking against mocked API answers: a script that
// sent a visit in the last day reads Live with its last visit and today's
// events, and the setup steps are gone; one whose visits stopped reads
// Quiet and points at the script below; one that has sent nothing shows
// the steps and waits. The script is on the page once in every state. Starts a local Vite server and mocks every
// API request; needs no backend.
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

// lastVisit: how long ago the script last sent a visit, in ms; null for never.
const answers = (lastVisit) => {
  const last = lastVisit === null ? null : new Date(Date.now() - lastVisit).toISOString();
  const key = { id: keyID, brand_id: brand, name: 'acme.example', origins: [], excluded_params: [], tests: false, flicker_guard: '', snippet,
    events_today: last && lastVisit < 864e5 ? 222 : 0, bots_today: 0, last_used_at: last, created_at: '2026-01-01T00:00:00Z', proxy_secret_at: null,
    own_domain: { status: 'none', events: 0, through_own_domain: 0, safari_direct: 0, snippet } };
  const step = { id: 'site_script', title: 'Add the website script', detail: '', status: last ? 'done' : 'waiting', done: !!last, skipped: false,
    install: { site_key_id: keyID, methods: ['manual'], snippet, developer: 'Please add this script.', ai_prompt: 'Add this script.', connector: 'google_tag_manager' } };
  return { key, setup: { brand_id: brand, website: 'https://acme.example', done: last ? 1 : 0, total: 1, steps: [step] } };
};

let browser;
try {
  for (let i = 0; i < 100 && !output.includes('ready in'); i++) await new Promise(r => setTimeout(r, 200));
  browser = await chromium.launch({ headless: true, args: ['--no-sandbox'], executablePath: process.env.OUTFLARE_TEST_CHROMIUM || undefined });

  const open = async (lastVisit) => {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    const { key, setup } = answers(lastVisit);
    await page.route('**/v1/**', async route => {
      const p = new URL(route.request().url()).pathname;
      let status = 200, body = {};
      if (p === '/v1/workspaces/workspace-a/brands') body = { data: [{ id: brand, name: 'Acme', handle: 'acme', status: 'active', timezone: 'Europe/Berlin' }] };
      else if (p === '/v1/workspaces/workspace-a/brands/acme/setup') body = setup;
      else if (p === `/v1/brands/${brand}/site-keys`) body = { data: [key] };
      else if (p === '/v1/workspaces/workspace-a/connectors') body = { data: [] };
      else { status = 404; body = { error: { code: 'not_found', message: 'nothing here', request_id: 'r' } }; }
      await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
    });
    await page.goto(`${origin}/tests/fixtures/website-tracking.html`);
    return { page, errors };
  };

  // Live: one line says so, with the proof; no setup steps left to read.
  const live = await open(60_000);
  await live.page.getByText('Tracking is live on acme.example').waitFor();
  await live.page.getByText(/Last visit .* · 222 events today\./).waitFor();
  assert.equal(await live.page.getByText('Get your script').count(), 0);
  assert.equal(await live.page.getByText('data-key="ofk_test_not_a_real_key"').count(), 1, 'the script shows once');
  assert.equal(await live.page.getByText('Live', { exact: true }).count(), 2, 'Live on the status line and on the script');
  await live.page.getByRole('button', { name: "See who's visiting" }).click();
  assert.deepEqual(await live.page.evaluate(() => window.gone), ['visitors']);
  if (shots) await live.page.screenshot({ path: `${shots}/website-tracking-live.png`, fullPage: true });
  assert.deepEqual(live.errors, []);

  // Quiet: it worked, then visits stopped; the script to check is below.
  const quiet = await open(3 * 864e5);
  await quiet.page.getByText('No visits from acme.example in the last 24 hours').waitFor();
  assert.equal(await quiet.page.getByText('Quiet', { exact: true }).count(), 2);
  assert.equal(await quiet.page.getByText('Tracking is live').count(), 0);
  assert.equal(await quiet.page.getByText('data-key="ofk_test_not_a_real_key"').count(), 1, 'the script shows once');
  if (shots) await quiet.page.screenshot({ path: `${shots}/website-tracking-quiet.png`, fullPage: true });
  assert.deepEqual(quiet.errors, []);

  // Never sent a visit: the steps, waiting on the first one.
  const waiting = await open(null);
  await waiting.page.getByText('See your first visit').waitFor();
  await waiting.page.getByText('Add it to acme.example').waitFor();
  await waiting.page.getByText('Waiting for first visit').waitFor();
  await waiting.page.getByText('data-key="ofk_test_not_a_real_key"').first().waitFor();
  await waiting.page.waitForFunction(() => document.body.innerText.split('data-key="ofk_test_not_a_real_key"').length === 2);
  assert.equal(await waiting.page.getByText('Tracking is live').count(), 0);
  if (shots) await waiting.page.screenshot({ path: `${shots}/website-tracking-waiting.png`, fullPage: true });
  assert.deepEqual(waiting.errors, []);

  console.log('website tracking: ok');
} catch (e) {
  console.error(output);
  throw e;
} finally {
  await browser?.close();
  server.kill();
}
