import { chromium } from 'playwright';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createServer } from 'node:net';
import { fileURLToPath } from 'node:url';

const ws = '30000000-0000-0000-0000-000000000001';
const brands = [
  { id: '10000000-0000-0000-0000-000000000001', name: 'Acme', handle: 'acme', status: 'active' },
  { id: '10000000-0000-0000-0000-000000000002', name: 'Northpeak', handle: 'northpeak', status: 'active' },
];
const unassigned = Array.from({ length: 5 }, (_, n) => ({ integration_id: `40000000-0000-0000-0000-00000000000${n}`, connector: 'emailbison', name: `Connection ${n}` }));
const me = {
  user: { id: 'user-a', email: 'owner@acme.example', name: 'Owner', email_verified: true },
  workspaces: [{ id: ws, slug: 'acme', name: 'Acme', role: 'owner', scopes: ['workspace.manage', 'mailboxes.manage'] }],
  menu_links: [],
};
const headings = { mailboxes: 'Mailboxes', domains: 'Domains', warmup: 'Warm-up' };
const tabs = ['domains', 'warmup', 'mailboxes', 'domains', 'warmup', 'mailboxes'];

const web = fileURLToPath(new URL('../', import.meta.url));
const port = await new Promise((resolve) => {
  const listener = createServer();
  listener.listen(0, '127.0.0.1', () => {
    const port = listener.address().port;
    listener.close(() => resolve(port));
  });
});
const origin = `http://127.0.0.1:${port}`;
const server = spawn(process.execPath, [fileURLToPath(new URL('../node_modules/vite/bin/vite.js', import.meta.url)), '--host', '127.0.0.1', '--port', String(port), '--strictPort'], { cwd: web, stdio: 'pipe' });
let output = '';
server.stdout.on('data', (b) => { output += b; });
server.stderr.on('data', (b) => { output += b; });
let browser;
try {
  let ready = false;
  for (let n = 0; n < 100; n++) {
    try { ready = (await fetch(origin)).ok; if (ready) break; } catch {}
    await new Promise((r) => setTimeout(r, 100));
  }
  assert(ready, `Vite did not start: ${output}`);
  browser = await chromium.launch({ headless: true, args: ['--no-sandbox'] });
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  const duplicateKeys = [];
  page.on('console', (m) => { if (m.type() === 'error' && m.text().includes('two children with the same key')) duplicateKeys.push(m.text()); });
  await page.route('**/v1/**', async (route) => {
    const u = new URL(route.request().url());
    let body = { data: [] };
    if (u.pathname === '/v1/setup') body = { needed: false };
    else if (u.pathname === '/v1/me') body = me;
    else if (u.pathname === '/v1/meta') body = { capabilities: [] };
    else if (u.pathname.endsWith('/brands')) body = { data: brands };
    else if (u.pathname.endsWith('/connections/unassigned')) body = { data: unassigned };
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
  });
  await page.route('**/ingest/**', (route) => route.fulfill({ status: 200, body: '{}' }));
  const notice = page.getByText('5 connections or accounts have no brand, so their data shows in no brand.', { exact: true });
  await page.goto(`${origin}/acme/all/mailboxes`);
  await page.getByRole('heading', { name: headings.mailboxes, exact: true }).waitFor();
  await notice.first().waitFor();
  const counts = [await notice.count()];
  for (const tab of tabs) {
    await page.evaluate((tab) => {
      history.pushState(null, '', `/acme/all/${tab}`);
      dispatchEvent(new PopStateEvent('popstate'));
    }, tab);
    await page.getByRole('heading', { name: headings[tab], exact: true }).waitFor();
    counts.push(await notice.count());
  }
  assert.deepEqual(counts, counts.map(() => 1), `the unassigned notice should show once on every page, counted ${counts.join(' ')} across ${['mailboxes', ...tabs].join(', ')}`);
  assert.deepEqual(duplicateKeys, [], 'React found siblings with the same key in the page frame');
  assert.deepEqual(errors, []);
  console.log('unassigned notice: shown once across', counts.length, 'pages');
} finally {
  await browser?.close();
  server.kill();
}
