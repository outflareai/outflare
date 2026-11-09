// The Outflare desktop app's sign-in, in the browser and in the app's own
// window (docs/desktop-app.md). Starts a local Vite server and mocks every
// API request; needs no backend. In a browser, /signin/app?state=... shows
// which computer asks, approves it and hands the code to outflare://. In
// the app's window (a marker and a fake invoke stand in for the shell),
// the sign-in screen sends the person to their browser, a failed
// completion says why, and links that leave Outflare go to the browser
// through the shell.
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
let browser;
try {
  let ready = false;
  for (let n = 0; n < 100; n++) { try { ready = (await fetch(origin)).ok; if (ready) break; } catch {} await new Promise(r => setTimeout(r, 100)); }
  assert(ready, `Vite did not start: ${output}`);
  // OUTFLARE_TEST_CHROMIUM points at a Chromium already on the machine; CI installs Playwright's own.
  browser = await chromium.launch({ headless: true, args: ['--no-sandbox'], executablePath: process.env.OUTFLARE_TEST_CHROMIUM || undefined });

  const me = { user: { id: 'user-1', name: 'Ana', email: 'ana@example.com', email_verified: true }, workspaces: [] };
  // A signed-in account with a workspace, so the app renders its sidebar.
  const meInApp = { ...me, workspaces: [{ id: '10000000-0000-0000-0000-000000000001', name: 'Acme', slug: 'acme', test: false, records_edit_owners_only: false, role: 'owner', scopes: [], brand_ids: [], features: [] }] };
  const signin = { name: "Ana's MacBook", platform: 'macos', app_version: '0.1.0', expires_at: '2026-01-01T00:10:00Z', approved: false };
  const calls = [];
  // mock answers the API for one page: signed in or not, with the sign-in
  // waiting or gone, and records every call.
  const mock = (page, { signedIn, lookup = 200, account = me }) => page.route('**/v1/**', async route => {
    const req = route.request(); const u = new URL(req.url());
    calls.push(`${req.method()} ${u.pathname}`);
    let status = 200, body = {};
    if (u.pathname === '/v1/setup') body = { needed: false };
    else if (u.pathname === '/v1/me') { if (signedIn) body = account; else { status = 401; body = { error: { code: 'unauthenticated', message: 'sign in', request_id: 'r' } }; } }
    else if (u.pathname === '/v1/meta') body = { name: 'Outflare', version: 'test', api: { rest: '/v1', openapi: '/v1/openapi.yaml' }, capabilities: [], login_methods: ['password'] };
    else if (u.pathname === '/v1/auth/app/signins/abc') { status = lookup; body = lookup === 200 ? signin : { error: { code: 'app_signin_expired', message: 'this sign-in expired; start again in the Outflare app', request_id: 'r' } }; }
    else if (u.pathname === '/v1/auth/app/signins/abc/approve') { status = 201; body = { code: 'ofac_test_not_a_real_code', url: 'outflare://signed-in?state=abc&code=ofac_test_not_a_real_code' }; }
    else { status = 404; body = { error: { code: 'not_found', message: 'nothing here', request_id: 'r' } }; }
    await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  });

  // 1. The browser: a signed-in person approves the app's sign-in.
  {
    const page = await browser.newPage();
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    await mock(page, { signedIn: true });
    // The deep link to outflare:// goes nowhere in this browser; the page
    // stays, and its fallback link carries the same URL.
    await page.goto(`${origin}/signin/app?state=abc`);
    await page.getByText("Sign in to Outflare on this computer?").waitFor();
    await page.getByText("Ana's MacBook (Mac)").waitFor();
    await page.getByText('ana@example.com').waitFor();
    await page.getByRole('button', { name: 'Sign in on this computer' }).click();
    await page.getByText("You're signed in on your computer").waitFor();
    assert.equal(await page.getByRole('link', { name: 'Open the Outflare app' }).getAttribute('href'), 'outflare://signed-in?state=abc&code=ofac_test_not_a_real_code');
    assert(calls.includes('GET /v1/auth/app/signins/abc') && calls.includes('POST /v1/auth/app/signins/abc/approve'), calls.join('\n'));
    assert.deepEqual(errors, []);
    await page.close();
  }

  // 2. The browser, signed out: the sign-in form first, and no approve
  // button for a sign-in that expired.
  {
    const page = await browser.newPage();
    await mock(page, { signedIn: false });
    await page.goto(`${origin}/signin/app?state=abc`);
    await page.getByRole('button', { name: 'Sign in' }).waitFor();
    assert.equal(await page.getByRole('button', { name: 'Sign in in your browser' }).count(), 0, 'a browser shows the form');
    await page.close();
    const expired = await browser.newPage();
    await mock(expired, { signedIn: true, lookup: 410 });
    await expired.goto(`${origin}/signin/app?state=abc`);
    await expired.getByText('this sign-in expired; start again in the Outflare app').waitFor();
    assert.equal(await expired.getByRole('button', { name: 'Sign in on this computer' }).count(), 0);
    await expired.close();
  }

  // 3. Inside the desktop app: the marker and a fake invoke play the shell.
  const shell = async (page) => {
    await page.addInitScript(() => {
      window.__OUTFLARE_APP__ = { version: '0.1.0', platform: 'macos' };
      window.__invoked = [];
      window.__TAURI__ = { core: { invoke: async (cmd, args) => { window.__invoked.push([cmd, args ?? null]); return null; } } };
    });
  };
  {
    const page = await browser.newPage();
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    await shell(page);
    await mock(page, { signedIn: false });
    await page.goto(`${origin}/`);
    await page.getByRole('button', { name: 'Sign in in your browser' }).click();
    await page.getByText('Waiting for your browser').waitFor();
    assert.deepEqual(await page.evaluate(() => window.__invoked), [['start_sign_in', null]]);
    assert.equal(await page.getByLabel('Password').count(), 0, 'the app shows no form');
    assert.deepEqual(errors, []);
    await page.close();
  }
  {
    const page = await browser.newPage();
    await shell(page);
    await mock(page, { signedIn: false });
    await page.goto(`${origin}/signin/app?error=app_signin_mismatch`);
    await page.getByText("That sign-in didn't come back to this app").waitFor();
    await page.getByRole('button', { name: 'Sign in in your browser' }).waitFor();
    await page.close();
  }
  // 4. Inside the desktop app with an update waiting, the sidebar shows the
  // Update button; one click asks the shell to install (which reopens the
  // app). The shell's update-ready event shows it too.
  {
    const page = await browser.newPage();
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    await page.addInitScript(() => {
      window.__OUTFLARE_APP__ = { version: '0.1.0', platform: 'macos' };
      window.__invoked = [];
      window.__listeners = {};
      window.__TAURI__ = {
        core: { invoke: async (cmd, args) => { window.__invoked.push([cmd, args ?? null]); if (cmd === 'update_state') return { enabled: true, available: true, version: '0.2.0' }; if (cmd === 'install_update') return new Promise(() => {}); return null; } },
        event: { listen: async (name, handler) => { window.__listeners[name] = handler; return () => {}; } },
      };
    });
    await mock(page, { signedIn: true, account: meInApp });
    await page.goto(`${origin}/acme`);
    const button = page.getByRole('button', { name: /Update available/ });
    await button.waitFor({ timeout: 20000 });
    await page.getByText('0.2.0').waitFor();
    await button.click();
    await page.getByText('Updating…').waitFor();
    assert((await page.evaluate(() => window.__invoked)).some(([cmd]) => cmd === 'install_update'), 'one click installs');
    assert.deepEqual(errors, []);
    await page.close();
  }
  {
    const page = await browser.newPage();
    await page.addInitScript(() => {
      window.__OUTFLARE_APP__ = { version: '0.1.0', platform: 'macos' };
      window.__listeners = {};
      window.__TAURI__ = {
        core: { invoke: async (cmd) => (cmd === 'update_state' ? { enabled: true, available: false } : null) },
        event: { listen: async (name, handler) => { window.__listeners[name] = handler; return () => {}; } },
      };
    });
    await mock(page, { signedIn: true, account: meInApp });
    await page.goto(`${origin}/acme`);
    await page.getByRole('button', { name: /Settings/ }).first().waitFor({ timeout: 20000 });
    assert.equal(await page.getByText('Update available').count(), 0, 'no update, no button');
    await page.evaluate(() => window.__listeners['update-ready']?.({ payload: { version: '0.3.0' } }));
    await page.getByText('Update available').waitFor();
    await page.close();
  }

  // 5. Inside the desktop app, a link that leaves Outflare goes to the
  // browser through the shell; one on this origin does not.
  {
    const page = await browser.newPage();
    await shell(page);
    await mock(page, { signedIn: false });
    await page.goto(`${origin}/`);
    await page.getByRole('button', { name: 'Sign in in your browser' }).waitFor();
    await page.evaluate((origin) => {
      const ext = document.createElement('a'); ext.href = 'https://outflare.ai/docs/'; ext.target = '_blank'; ext.textContent = 'docs'; ext.id = 'ext';
      const same = document.createElement('a'); same.href = origin + '/signin/app'; same.textContent = 'same'; same.id = 'same';
      document.body.append(ext, same);
      document.addEventListener('click', e => { if (e.target.id === 'same') e.preventDefault(); });
    }, origin);
    await page.click('#ext');
    await page.click('#same');
    await page.evaluate(() => window.open('mailto:hello@example.com'));
    assert.deepEqual(await page.evaluate(() => window.__invoked), [['open_external', { url: 'https://outflare.ai/docs/' }], ['open_external', { url: 'mailto:hello@example.com' }]]);
    await page.close();
  }
  console.log('app sign-in: ok');
} finally {
  await browser?.close();
  server.kill();
}
