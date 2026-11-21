// The browser extension in a real Chromium, against a small stand-in for
// the Outflare API: the side panel finds a known person and a known
// company, adds a new person with their company, adds a note, refuses to
// add a do-not-contact website, and the toolbar icon is marked for known
// and do-not-contact sites from the list kept in the browser. Needs no
// backend. Run: pnpm --filter @outflare/web test:extension
import { chromium } from 'playwright';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { createServer } from 'node:http';
import { cpSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const brand = '10000000-0000-0000-0000-000000000001';
const ws = '20000000-0000-0000-0000-000000000001';
const ana = '30000000-0000-0000-0000-000000000001';
const acme = '30000000-0000-0000-0000-000000000002';
const salt = 'testsalt';
const digest = d => createHash('sha256').update(`${salt}:${d}`).digest('hex').slice(0, 16);
const record = (id, type, name, ns, value, extra = {}) => ({
  id, type, name, brand_id: brand, origin: 'manual', attributes: {}, attribute_sources: {}, identities: [{ namespace: ns, value }], needs_review: false, version: 1,
  merged_into: null, archived: false, created_at: '2026-11-01T09:00:00Z', updated_at: '2026-11-01T09:00:00Z', ...extra,
});
const records = [
  record(ana, 'person', 'Ana Ruiz', 'email', 'ana@acme.example', { contactability: { basis: 'unknown', acquired_via: '', allowed: { transactional: true, cold_outreach: true, marketing: false }, suppressed: false, why: '' } }),
  record(acme, 'account', 'Acme', 'domain', 'acme.example'),
];
const summary = id => ({
  record_id: id, type: id === ana ? 'person' : 'account', name: id === ana ? 'Ana Ruiz' : 'Acme', company: id === ana ? { id: acme, name: 'Acme' } : null, owner: { id: 'u1', name: 'Maya Lindqvist' },
  stage: { stage: 'Customer' }, people_total: id === ana ? 0 : 1, deals: { open: 1, won: 0, lost: 0, latest: [{ id: 'd1', name: 'Team plan', stage: 'Proposal', state: 'open', amount_cents: 480000, currency: 'USD' }] },
  conversations: { open: 1, waiting: 0, latest: [{ id: 'c1', subject: 'Cannot find my invoice', last_message_at: '2026-11-10T09:00:00Z' }] },
  contact: { last_at: '2026-11-10T09:00:00Z' }, tasks: { open: 0, overdue: 0, next: [] },
});

const calls = [];
const headers = [];
const api = createServer((req, res) => {
  let raw = '';
  req.on('data', b => { raw += b; });
  req.on('end', () => {
    const u = new URL(req.url, 'http://x');
    const p = u.pathname;
    calls.push(`${req.method} ${p}${u.search} ${raw}`.trim());
    if (p.startsWith('/v1/')) headers.push({ site: req.headers['sec-fetch-site'], auth: req.headers.authorization, cookie: req.headers.cookie });
    const send = (status, body) => { res.writeHead(status, { 'Content-Type': 'application/json' }); res.end(JSON.stringify(body)); };
    const B = `/v1/brands/${brand}`;
    if (!p.startsWith('/v1/')) return res.writeHead(200, { 'Content-Type': 'text/html' }).end('<title>Site</title><p>A website</p>');
    if (req.headers.authorization !== 'Bearer oft_test_not_a_real_token') return send(401, { error: { code: 'unauthenticated', message: 'sign in' } });
    if (p === '/v1/whoami') return send(200, { actor: { id: 'a1', type: 'api_token' }, token: { id: 't1', name: 'Outflare for Chrome', workspace_id: ws, workspace: { id: ws, slug: 'northpeak', name: 'Northpeak' }, scopes: ['workspace.read', 'customers.read', 'customers.manage'] } });
    if (p === `/v1/workspaces/${ws}/brands`) return send(200, { data: [{ id: brand, name: 'Northpeak', handle: 'northpeak', status: 'active' }] });
    if (p === `${B}/records/known-domains`) return send(200, { salt, companies: records.filter(r => r.type === 'account').map(r => digest(r.identities[0].value)), do_not_contact: [digest('blocked.example')], truncated: false });
    if (p === `${B}/records/lookup`) {
      const hit = records.filter(r => r.identities.some(i => i.namespace === u.searchParams.get('namespace') && i.value === u.searchParams.get('value')));
      return send(200, { data: hit, owner: null });
    }
    if (p === `${B}/records` && req.method === 'POST') {
      const body = JSON.parse(raw);
      const made = record(`30000000-0000-0000-0000-0000000000${10 + records.length}`, body.type, body.name, body.identities[0].namespace, body.identities[0].value, { origin: body.origin });
      records.push(made);
      return send(201, made);
    }
    const m = p.match(new RegExp(`^${B}/records/([^/]+)/(summary|relationships)$`));
    if (m && m[2] === 'summary') return send(200, summary(m[1]));
    if (m) return send(201, { id: 'rel1' });
    if (p === `${B}/crm/activities`) return send(201, { id: 'act1' });
    if (p === `${B}/sub-sources` && req.method === 'GET') return send(200, { data: [{ key: 'web_summit_2026', label: 'Web Summit 2026', source: 'event', archived: false, people: 4 }] });
    if (p === `${B}/sub-sources`) return send(200, { key: 'web_summit_2026', label: 'Web Summit 2026', source: 'event', archived: false, people: 0 });
    send(404, { error: { code: 'not_found', message: 'not found' } });
  });
});
await new Promise(r => api.listen(0, '127.0.0.1', r));
const port = api.address().port;
const origin = `http://127.0.0.1:${port}`;

// A copy of the extension that may reach the stand-in without asking, as
// it may reach app.outflare.ai.
const dir = mkdtempSync(join(tmpdir(), 'outflare-ext-'));
cpSync(fileURLToPath(new URL('../../extension/', import.meta.url)), dir, { recursive: true });
const manifest = JSON.parse(readFileSync(join(dir, 'manifest.json'), 'utf8'));
manifest.host_permissions.push('http://127.0.0.1/*');
writeFileSync(join(dir, 'manifest.json'), JSON.stringify(manifest));

const context = await chromium.launchPersistentContext(mkdtempSync(join(tmpdir(), 'outflare-ext-profile-')), {
  channel: 'chromium', headless: true, executablePath: process.env.OUTFLARE_TEST_CHROMIUM || undefined,
  args: ['--no-sandbox', `--disable-extensions-except=${dir}`, `--load-extension=${dir}`, `--host-resolver-rules=MAP *.example 127.0.0.1`],
});
try {
  const sw = context.serviceWorkers()[0] || await context.waitForEvent('serviceworker');
  const id = new URL(sw.url()).host;
  // The key in the manifest fixes the id the server recognises.
  assert.equal(id, 'kolppcifpdmbigbfddkchjdgbcnlobcg');
  const shot = async (page, name) => { if (process.env.OUTFLARE_TEST_SHOTS) await page.screenshot({ path: `${process.env.OUTFLARE_TEST_SHOTS}/${name}.png`, fullPage: true }); };

  const page = await context.newPage();
  await page.setViewportSize({ width: 380, height: 760 });
  const errors = []; page.on('pageerror', e => errors.push(e.message));
  await page.goto(`chrome-extension://${id}/panel.html`);
  await page.getByRole('button', { name: 'Connect Outflare' }).waitFor();
  await shot(page, 'connect');

  // Signed in (the sign-in itself is the server's flow, tested there).
  await sw.evaluate(async ({ origin }) => {
    await chrome.storage.local.set({ server: origin, clients: { [origin]: 'client' }, auth: { access: 'oft_test_not_a_real_token', refresh: 'ofo_test_not_a_real_token', expires: Date.now() + 3600e3 } });
  }, { origin });
  await page.reload();
  const search = page.getByRole('searchbox');
  await search.waitFor();

  // A person Outflare knows.
  await search.fill('ana@acme.example');
  await page.getByRole('button', { name: 'Look up' }).click();
  await page.getByRole('heading', { name: 'Ana Ruiz' }).waitFor();
  for (const text of ['A person you know', 'Maya Lindqvist', 'Customer', 'Marketing email', 'Deals (1 open)', 'Team plan', 'Support (1 open)', 'Cannot find my invoice'])
    await page.getByText(text, { exact: false }).first().waitFor();
  assert.equal(await page.getByRole('link', { name: 'Open in Outflare' }).getAttribute('href'), `${origin}/northpeak/northpeak/people/${ana}`);
  await shot(page, 'person');

  // A note on them.
  await page.getByRole('button', { name: 'Add note' }).click();
  await page.getByRole('textbox', { name: 'Note' }).fill('Asked about the team plan.\nCall back Friday.');
  await page.locator('form.card').getByRole('button', { name: 'Add note' }).click();
  await page.getByRole('textbox', { name: 'Note' }).waitFor({ state: 'detached' });
  await page.getByRole('heading', { name: 'Ana Ruiz' }).waitFor();
  const note = calls.find(c => c.startsWith(`POST /v1/brands/${brand}/crm/activities`));
  assert.ok(note && note.includes('"kind":"note"') && note.includes('"title":"Asked about the team plan."') && note.includes(`"record_id":"${ana}"`), note);

  // A company, found by its website with or without a subdomain.
  await page.getByRole('searchbox').fill('https://www.docs.acme.example/pricing');
  await page.getByRole('button', { name: 'Look up' }).click();
  await page.getByText('A company you know').waitFor();
  await shot(page, 'company');

  // Someone new, added with their company.
  await page.getByRole('searchbox').fill('tom@brightlane.example');
  await page.getByRole('button', { name: 'Look up' }).click();
  await page.getByText('Not in Outflare yet').waitFor();
  await page.getByLabel('Name').fill('Tom Becker');
  // Met at a conference: Source Events and conferences, and which one.
  await page.getByLabel('Where they came from').selectOption('event');
  await page.getByLabel('Event or conference', { exact: true }).fill('Web Summit 2026');
  await shot(page, 'new-person');
  await page.getByRole('button', { name: 'Add with company' }).click();
  await page.getByRole('heading', { name: 'Tom Becker' }).waitFor();
  const made = calls.filter(c => c.startsWith(`POST /v1/brands/${brand}/records `));
  assert.equal(made.length, 2, made.join('\n'));
  assert.ok(made[0].includes('"type":"person"') && made[0].includes('"origin":"manual"') && made[0].includes('"source":"event"') && made[0].includes('"sub_source":"web_summit_2026"') && made[0].includes('tom@brightlane.example') && made[0].includes('"acquired_via":"extension"'));
  assert.ok(made[1].includes('"type":"account"') && made[1].includes('"value":"brightlane.example"'));
  assert.ok(calls.some(c => c.includes('/relationships') && c.includes('"kind":"works_at"')));

  // A free mailbox is no company.
  await page.getByRole('searchbox').fill('sam@gmail.com');
  await page.getByRole('button', { name: 'Look up' }).click();
  await page.getByRole('button', { name: 'Add person' }).waitFor();
  assert.equal(await page.getByRole('button', { name: 'Add with company' }).count(), 0);

  // A do-not-contact website can't be added.
  await page.getByRole('searchbox').fill('blocked.example');
  await page.getByRole('button', { name: 'Look up' }).click();
  await page.getByRole('alert').filter({ hasText: 'Do not contact' }).waitFor();
  assert.equal(await page.getByRole('button', { name: 'Add company' }).count(), 0);
  await shot(page, 'blocked');

  // The toolbar icon, from the list in the browser. With the panel open,
  // Outflare is asked only about sites it already holds, never the unknown one.
  const before = calls.length;
  const badge = async host => {
    const tab = await context.newPage();
    await tab.goto(`http://${host}:${port}/`);
    let text = '';
    for (let i = 0; i < 40; i++) {
      text = await sw.evaluate(async url => { const [t] = await chrome.tabs.query({ url }); return t ? await chrome.action.getBadgeText({ tabId: t.id }) : ''; }, `http://${host}/*`);
      if (text) break;
      await new Promise(r => setTimeout(r, 100));
    }
    await tab.close();
    return text;
  };
  assert.equal(await badge('www.acme.example'), ' ');
  assert.equal(await badge('blocked.example'), '!');
  assert.equal(await badge('unknown.example'), '');
  const after = calls.slice(before).filter(c => c.includes('/v1/'));
  assert.ok(!after.some(c => /unknown\.example/.test(c)), after.join('\n'));

  // Every call carries the token and no cookie, and none is cross-site.
  assert.ok(headers.length > 5 && headers.every(x => x.auth === 'Bearer oft_test_not_a_real_token' && !x.cookie));
  assert.ok(headers.every(x => x.site !== 'cross-site'), JSON.stringify([...new Set(headers.map(x => x.site))]));

  // Disconnect goes back to the start.
  await page.bringToFront();
  await page.getByRole('button', { name: 'Disconnect' }).click();
  await page.getByRole('button', { name: 'Connect Outflare' }).waitFor();
  assert.deepEqual(errors, []);
  console.log('extension: ok', JSON.stringify([...new Set(headers.map(x => x.site))]));
} finally {
  await context.close();
  api.close();
}
