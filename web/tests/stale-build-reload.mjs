// After a deploy, a page of the previous build asks for scripts that are gone.
// Builds the app, serves the build, and checks that the page reloads once when
// its entry script or a lazy chunk fails to load, never twice in a row, and
// that the reloaded page reports it. Mocks every API request; needs no backend.
import { chromium } from 'playwright';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { createServer } from 'node:http';
import { tmpdir } from 'node:os';
import { extname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const web = fileURLToPath(new URL('../', import.meta.url));
const dist = mkdtempSync(join(tmpdir(), 'outflare-stale-build-'));
execFileSync(process.execPath, [join(web, 'node_modules/vite/bin/vite.js'), 'build', '--outDir', dist, '--emptyOutDir', '--logLevel', 'error'], { cwd: web, stdio: 'inherit' });
for (const entry of ['index.html', 'portal.html']) {
  assert(readFileSync(join(dist, entry), 'utf8').includes('outflare:stale-build-reload'), `${entry} carries the reload guard`);
}

const types = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.woff2': 'font/woff2', '.svg': 'image/svg+xml' };
const server = createServer((req, res) => {
  const path = new URL(req.url, 'http://x').pathname;
  let file = join(dist, path);
  if (!path.startsWith('/assets/') && !extname(path)) file = join(dist, 'index.html');
  try {
    const body = readFileSync(file);
    res.writeHead(200, { 'content-type': types[extname(file)] ?? 'application/octet-stream', 'cache-control': 'no-store' }).end(body);
  } catch {
    res.writeHead(404).end();
  }
});
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
const origin = `http://127.0.0.1:${server.address().port}`;

let browser;
try {
  // OUTFLARE_TEST_CHROMIUM points at a Chromium already on the machine; CI installs Playwright's own.
  browser = await chromium.launch({ headless: true, args: ['--no-sandbox'], executablePath: process.env.OUTFLARE_TEST_CHROMIUM || undefined });

  const open = async (missing) => {
    const page = await browser.newPage();
    const loads = [];
    page.on('framenavigated', f => { if (f === page.mainFrame()) loads.push(f.url()); });
    await page.route('**/v1/**', route => {
      const u = new URL(route.request().url());
      if (u.pathname === '/v1/me') return route.fulfill({ status: 401, contentType: 'application/json', body: JSON.stringify({ error: { code: 'unauthenticated', message: 'sign in', request_id: 'r' } }) });
      const body = u.pathname === '/v1/setup' ? { needed: false } : u.pathname === '/v1/meta' ? { name: 'Outflare', version: 'test', api: { rest: '/v1', openapi: '/v1/openapi.yaml' }, capabilities: [], login_methods: ['password'] } : {};
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
    });
    let gone = 0;
    await page.route(/\/assets\/app-[^/]+\.js$/, route => (gone++ < missing ? route.fulfill({ status: 404, body: '' }) : route.continue()));
    return { page, loads };
  };
  const note = page => page.evaluate(() => JSON.parse(sessionStorage.getItem('outflare:stale-build-reload') || 'null'));

  // 1. The entry script of the previous build is gone once: one reload, and the app runs.
  {
    const { page, loads } = await open(1);
    await page.goto(origin + '/');
    await page.getByRole('button', { name: 'Sign in' }).waitFor();
    assert.equal(loads.length, 2, `one reload: ${loads.join(', ')}`);
    const n = await note(page);
    assert.equal(n?.reason, 'entry');
    assert.equal(n?.tracked, true, 'the reloaded page reported the reload');
    await page.close();
  }

  // 2. The entry script stays gone: one reload, then the page stops trying.
  {
    const { page, loads } = await open(Infinity);
    await page.goto(origin + '/');
    await page.waitForTimeout(3000);
    assert.equal(loads.length, 2, `no loop: ${loads.join(', ')}`);
    assert.equal((await note(page))?.tracked, undefined, 'nothing ran to report it');
    await page.close();
  }

  // 3. A lazy chunk fails in an open tab: one reload; a second failure soon after does not reload.
  {
    const { page, loads } = await open(0);
    await page.goto(origin + '/');
    await page.getByRole('button', { name: 'Sign in' }).waitFor();
    const preloadError = () => page.evaluate(() => { const e = new Event('vite:preloadError', { cancelable: true }); e.payload = new Error('chunk gone'); dispatchEvent(e); });
    await Promise.all([page.waitForEvent('framenavigated'), preloadError()]);
    await page.getByRole('button', { name: 'Sign in' }).waitFor();
    assert.equal((await note(page))?.reason, 'chunk');
    await preloadError();
    await page.waitForTimeout(1500);
    assert.equal(loads.length, 2, `one reload: ${loads.join(', ')}`);
    await page.close();
  }
  console.log('stale build reload: ok');
} finally {
  await browser?.close();
  server.close();
  rmSync(dist, { recursive: true, force: true });
}
