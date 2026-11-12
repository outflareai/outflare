// Importing leads into a list, in the real app with every API request mocked.
// Checks that a list's own page imports a CSV in a dialog with that list fixed
// (no trip to Leads), and that the Leads import picks a list from a dropdown,
// starts on the list the table is filtered to, and can make a new list.
// OUTFLARE_TEST_SHOTS=<dir> also saves screenshots at desktop and phone width.
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
const user = '30000000-0000-4000-8000-000000000001';
const t0 = '2026-11-02T10:00:00Z';
const list = (id, name, extra = {}) => ({
  id, name, slug: name.toLowerCase().replace(/\W+/g, '-'), kind: 'manual', description: null, brand_id: brand, leads: 0,
  filters: {}, enrich_integration_ids: [], search_integration_id: null, min_score: 0, skip_contacted_days: 0,
  skip_in_sequences: false, verify: false, verified_emails_only: false, credit_cap: null, max_leads: null,
  refresh_every_days: null, next_refresh_at: null, last_run: null, profile_id: null, created_at: t0, updated_at: t0, ...extra,
});
const affiliates = list('40000000-0000-4000-8000-000000000001', 'Affiliates to move', { leads: 12 });
const founders = list('40000000-0000-4000-8000-000000000002', 'Q4 founders', { leads: 30 });
const searched = list('40000000-0000-4000-8000-000000000003', 'Apollo heads of sales', { kind: 'search', leads: 80 });
const lists = [affiliates, founders, searched];
const lead = { id: '50000000-0000-4000-8000-000000000001', email: 'ana@acme.example', first_name: 'Ana', last_name: 'Diaz', company: 'Acme', title: 'Founder', verification: 'deliverable', fields: {}, list_ids: [affiliates.id], created_at: t0, updated_at: t0 };
const imports = [];

function answer(method, u) {
  const p = u.pathname;
  if (p === '/v1/setup') return { needed: false };
  if (p === '/v1/me') return { user: { id: user, name: 'Owner', email: 'owner@example.com', email_verified: true }, workspaces: [{ id: ws, slug: 'acme', name: 'Acme', role: 'owner', scopes: ['workspace.manage', 'leads.read', 'leads.manage', 'outcomes.read', 'outcomes.manage'] }] };
  if (p === '/v1/meta') return { name: 'Outflare', version: 'test', capabilities: [], login_methods: ['password'] };
  if (p === `/v1/workspaces/${ws}/brands`) return { data: [{ id: brand, name: 'Northpeak', handle: 'northpeak', status: 'active' }] };
  if (p === `/v1/workspaces/${ws}/stats`) return { unread_replies: 0, actions_waiting: 0 };
  if (p === `/v1/workspaces/${ws}/data-providers`) return { data: [] };
  const b = `/v1/brands/${brand}`;
  if (p === `${b}/leads/import` && method === 'POST') {
    const name = u.searchParams.get('list');
    imports.push(name);
    const known = lists.find(l => l.name === name);
    return { created: 2, updated: 1, duplicates: 0, suppressed: 0, errors: [], list_id: name ? (known?.id ?? '40000000-0000-4000-8000-000000000099') : null };
  }
  if (p === `${b}/leads`) return { data: [lead], total: 1, next_cursor: null };
  if (p === `${b}/lead-lists`) return { data: lists, next_cursor: null };
  if (p === `${b}/smart-lists` || p === `${b}/ideal-customer-profiles`) return { data: [] };
  const one = p.match(new RegExp(`^${b}/lead-lists/([0-9a-f-]+)(/members|/runs)?$`));
  if (one) {
    const l = lists.find(x => x.id === one[1]);
    if (!l) return null;
    if (one[2] === '/runs') return { data: [] };
    if (one[2] === '/members') return { data: [{ ...lead, score: null, added_at: t0 }], total: 1, next_cursor: null };
    return l;
  }
  return { data: [] };
}

let browser;
try {
  let ready = false;
  for (let n = 0; n < 100; n++) { try { ready = (await fetch(origin)).ok; if (ready) break; } catch {} await new Promise(r => setTimeout(r, 100)); }
  assert(ready, `Vite did not start: ${output}`);
  browser = await chromium.launch({ headless: true, args: ['--no-sandbox'], executablePath: process.env.OUTFLARE_TEST_CHROMIUM || undefined });
  const open = async (width, height, theme = 'dark') => {
    const page = await browser.newPage({ viewport: { width, height }, deviceScaleFactor: shots ? 2 : 1 });
    await page.addInitScript(t => { try { localStorage.setItem('theme', t); } catch {} }, theme);
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    await page.route('**/v1/**', async route => {
      const req = route.request(); const u = new URL(req.url());
      const body = answer(req.method(), u);
      await route.fulfill(body === null
        ? { status: 404, contentType: 'application/json', body: JSON.stringify({ error: { code: 'not_found', message: 'nothing here', request_id: 'r' } }) }
        : { status: 200, contentType: 'application/json', body: JSON.stringify(body) });
    });
    return { page, errors };
  };
  const shot = async (page, name) => { if (shots) await page.screenshot({ path: join(shots, `${name}.png`), fullPage: true }); };
  const csv = { name: 'leads.csv', mimeType: 'text/csv', buffer: Buffer.from('email\nana@acme.example\nbo@acme.example\n') };

  // 1. A list's page imports into that list, without leaving it.
  for (const [w, h, label] of [[1440, 900, 'desktop'], [390, 844, 'phone']]) {
    imports.length = 0;
    const { page, errors } = await open(w, h);
    await page.goto(`${origin}/acme/northpeak/lists/${affiliates.id}`);
    await page.getByRole('heading', { name: /Affiliates to move/ }).first().waitFor();
    await page.getByRole('button', { name: 'Import leads', exact: true }).click();
    const dialog = page.getByRole('dialog');
    await dialog.getByText('Import leads into Affiliates to move').waitFor();
    assert.equal(await dialog.locator('select').count(), 0, 'the list is fixed: no list picker');
    await dialog.locator('input[type="file"]').setInputFiles(csv);
    await shot(page, `list-dialog-${label}`);
    await dialog.getByRole('button', { name: 'Import leads' }).click();
    await dialog.getByText('2 added, 1 updated in Affiliates to move.').waitFor();
    assert.deepEqual(imports, ['Affiliates to move']);
    assert.ok(new URL(page.url()).pathname.includes('/lists/'), 'stays on the list');
    await shot(page, `list-dialog-done-${label}`);
    assert.deepEqual(errors, []);
    await page.close();
  }

  // 2. Leads: a dropdown of the brand's lists, preselected from the filter.
  {
    imports.length = 0;
    const { page, errors } = await open(1440, 900);
    await page.goto(`${origin}/acme/northpeak/leads`);
    await page.getByRole('heading', { name: 'Leads', exact: true }).waitFor();
    await page.getByLabel('List', { exact: true }).waitFor({ timeout: 15000 });
    await page.getByLabel('List', { exact: true }).selectOption(founders.id);
    await page.getByRole('button', { name: 'Import leads' }).click();
    const picker = page.getByLabel('Add them to a list');
    assert.equal(await picker.inputValue(), founders.id, 'starts on the filtered list');
    const options = await picker.locator('option').allTextContents();
    assert.deepEqual(options, ['No list', 'Affiliates to move (12)', 'Q4 founders (30)', 'New list…'], 'search lists are left out');
    await page.locator('input[type="file"]').setInputFiles(csv);
    await shot(page, 'leads-dropdown');
    await page.getByRole('button', { name: 'Import leads' }).last().click();
    await page.getByText('2 added, 1 updated in Q4 founders.').waitFor();

    // A new list needs a name before importing.
    await picker.selectOption('__new');
    await page.locator('input[type="file"]').setInputFiles(csv);
    const go = page.getByRole('button', { name: 'Import leads' }).last();
    assert.ok(await go.isDisabled(), 'no name, no import');
    await page.getByLabel('New list name').fill('Webinar sign-ups');
    await shot(page, 'leads-new-list');
    await go.click();
    await page.getByText('2 added, 1 updated in Webinar sign-ups.').waitFor();

    // No list sends no list.
    await picker.selectOption('');
    await page.locator('input[type="file"]').setInputFiles(csv);
    await go.click();
    await page.waitForFunction(() => document.body.innerText.includes('2 added, 1 updated.'));
    assert.deepEqual(imports, ['Q4 founders', 'Webinar sign-ups', null]);
    assert.deepEqual(errors, []);
    await page.close();
  }
  console.log('lead import: ok');
} finally {
  await browser?.close();
  server.kill();
}
