// Real app, fictional API fixtures. No production data or provider calls.
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
  const listener = createServer();
  listener.listen(0, '127.0.0.1', () => {
    const port = listener.address().port;
    listener.close(() => resolve(port));
  });
});
const origin = `http://127.0.0.1:${port}`;
const server = spawn(process.execPath, [fileURLToPath(new URL('../node_modules/vite/bin/vite.js', import.meta.url)), '--host', '127.0.0.1', '--port', String(port), '--strictPort'], { cwd: web, stdio: 'pipe' });
let output = '';
server.stdout.on('data', b => { output += b; });
server.stderr.on('data', b => { output += b; });
const ws = '10000000-0000-4000-8000-000000000001';
const brand = '20000000-0000-4000-8000-000000000001';
const root = `/v1/brands/${brand}/sales`;
const t0 = '2026-11-15T10:00:00Z';
const attribution = { id: 'worker:example', name: 'Example discovery agent', kind: 'worker', status: 'known' };
const member = { user_id: 'u1', name: 'Maya Example', email: 'maya@example.com', role: 'manager', connected: false };
const lead = {
  id: '30000000-0000-4000-8000-000000000001', record_id: 'r1',
  name: 'Ana Example', company: 'Northpeak Example', email: 'ana@northpeak.example',
  state: 'new', reply_state: 'reply_received', version: 1,
  source: 'lead_portal', source_id: 'source-example', created_at: t0,
  updated_at: t0, last_activity_at: t0, attribution,
  website: 'https://northpeak.example',
  latest_message: { body: 'Could you share pricing for our team?', occurred_at: t0, direction: 'incoming' },
  latest_reply: { body: 'Could you share pricing for our team?', occurred_at: t0, direction: 'incoming' },
};
let mode = 'missing';
let failPrevious = false;
const calls = [];
function report(query) {
  const hours = { '24h': 24, '48h': 48, '7d': 168, '30d': 720, '365d': 8760 }[query.get('period') || '24h'];
  const to = query.get('to') || t0;
  const from = query.get('from') || new Date(Date.parse(to) - hours * 3600000).toISOString();
  const counts = mode === 'connected'
    ? { discovered: 120, researched: 90, verified: 70, emailed: 60, replied: 12, sdr: 5, closed: 2 }
    : { sdr: 5, closed: 2 };
  return {
    source: { status: mode === 'connected' ? 'connected' : 'missing', connections: [] },
    generated_at: t0, from, to, bucket: hours <= 48 ? 'hour' : 'day',
    buckets: [{ at: from, counts }], totals: counts, cohort: null,
    tools: null, capacity: null,
    results: { amounts: [{ currency: 'USD', amount_minor: 120000, valued: 1, unvalued: 1 }] },
    agents: [{ attribution, counts, intake: { submitted: 150, accepted: 120, duplicates: 20, blocked: 8, held: 2 } }],
    attribution_coverage: { native_milestones: 7, known: 7, unavailable: 0 },
  };
}
function answer(method, u, body) {
  const p = u.pathname;
  if (p === '/v1/setup') return { needed: false };
  if (p === '/v1/meta') return { name: 'Outflare', version: 'test', capabilities: [], login_methods: ['password'] };
  if (p === '/v1/me') return {
    user: { id: member.user_id, name: member.name, email: member.email, email_verified: true },
    workspaces: [{ id: ws, slug: 'example', name: 'Example Sales', role: 'owner', scopes: ['workspace.manage', 'customers.read', 'customers.manage', 'members.manage'] }],
  };
  if (p === `/v1/workspaces/${ws}/brands`) return { data: [{ id: brand, name: 'Northpeak', handle: 'northpeak', status: 'active' }] };
  if (p === `/v1/workspaces/${ws}/stats`) return { unread_replies: 0, actions_waiting: 0 };
  if (p === `${root}/me`) return member;
  if (p === `${root}/team`) return { data: [member] };
  if (p === `${root}/templates`) return { data: [] };
  if (p === `${root}/leads`) return { data: [lead], new_unseen: true, unread: true, next_cursor: null };
  if (p === `${root}/leads/${lead.id}`) return { lead, messages: [], events: [], handoffs: [], draft: null };
  if (p === `${root}/leads/${lead.id}/actions` && body?.action === 'read') return { lead };
  if (p === `${root}/reporting`) return report(u.searchParams);
  if (p === `${root}/reporting/leads`) return { groups: (u.searchParams.get('stages') || 'closed').split(',').map(stage => ({
    stage, total: 1, offset: 0,
    leads: [{ id: lead.id, company: lead.company, email: lead.email, status: 'customer', at: t0, origin: 'outflare', attribution, amount_minor: null, currency: 'USD' }],
  })) };
  return { data: [] };
}
let browser;
try {
  let ready = false;
  for (let n = 0; n < 100; n++) {
    try { ready = (await fetch(origin)).ok; if (ready) break; } catch {}
    await new Promise(r => setTimeout(r, 100));
  }
  assert(ready, `Vite did not start: ${output}`);
  browser = await chromium.launch({ headless: true, executablePath: process.env.OUTFLARE_TEST_CHROMIUM || undefined });
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 }, colorScheme: 'dark' });
  page.setDefaultTimeout(30000);
  const errors = [];
  page.on('pageerror', e => errors.push(e.message));
  await page.route('**/v1/**', async route => {
    const req = route.request(), u = new URL(req.url());
    let sent = null; try { sent = req.postDataJSON(); } catch {}
    calls.push({ method: req.method(), path: u.pathname, query: u.searchParams, body: sent });
    const failed = u.pathname === `${root}/reporting` && (mode === 'error' || (failPrevious && u.searchParams.has('from')));
    await route.fulfill({ status: failed ? 503 : 200, contentType: 'application/json', body: JSON.stringify(failed
      ? { error: { code: 'upstream_failed', message: 'Example source unavailable', request_id: 'fixture' } }
      : answer(req.method(), u, sent)) });
  });
  // Preserve the conversation queue, including the original uploader.
  await page.goto(`${origin}/example/northpeak/crm`);
  await page.getByRole('heading', { name: 'All leads', exact: true }).waitFor();
  await page.getByText('Could you share pricing for our team?', { exact: true }).waitFor();
  await page.getByText(attribution.name, { exact: true }).waitFor();
  await page.getByRole('button', { name: 'Reporting', exact: true }).click();
  await page.waitForURL('**/sales-reporting');
  await page.getByText('Outflare data only', { exact: true }).waitFor();
  const milestones = page.locator('.sr-stages');
  assert(await milestones.getByRole('button', { name: /Discovered/ }).isDisabled());
  assert.match(await milestones.getByRole('button', { name: /Discovered/ }).innerText(), /—/);
  assert.match(await milestones.getByRole('button', { name: /SDR picked up/ }).innerText(), /5/);
  assert.match(await milestones.getByRole('button', { name: /Confirmed customers/ }).innerText(), /2/);
  if (shots) await page.screenshot({ path: join(shots, 'reporting-live-missing.png'), fullPage: true });

  // Explicit demo survives a source/API failure; never silently replaces live numbers.
  mode = 'error';
  await page.getByRole('button', { name: 'Refresh', exact: true }).click();
  await page.getByText('Example source unavailable', { exact: true }).waitFor();
  assert.equal(await page.getByText('Fictional example data', { exact: false }).count(), 0);
  await page.getByRole('button', { name: 'View UI demo', exact: true }).click();
  await page.getByText('Fictional example data', { exact: false }).waitFor();
  for (const name of ['48 hours', 'Week', 'Month', 'Year', '24 hours']) {
    await page.getByRole('group', { name: 'Reporting period', exact: true }).getByRole('button', { name, exact: true }).click();
    assert.equal(await page.getByRole('group', { name: 'Reporting period', exact: true }).getByRole('button', { name, exact: true }).getAttribute('aria-pressed'), 'true');
  }
  if (shots) await page.screenshot({ path: join(shots, 'reporting-demo-flow.png'), fullPage: true });
  await page.getByRole('tab', { name: 'Results', exact: true }).click();
  assert.equal(await page.getByRole('region', { name: 'Throughput', exact: true }).count(), 0);
  assert.equal(await page.getByRole('option', { name: /Won|Lost/ }).count(), 0);
  await page.getByRole('heading', { name: 'Confirmed customers', exact: true }).waitFor();
  await page.getByRole('columnheader', { name: 'Expected monthly revenue', exact: true }).waitFor();
  assert.equal(await page.getByRole('columnheader', { name: 'Payments', exact: true }).count(), 0);
  await page.getByText('Not entered', { exact: true }).first().waitFor();
  await page.locator('.sr-lead-link').first().click();
  await page.getByRole('dialog').getByText('Customer confirmed', { exact: true }).waitFor();
  await page.keyboard.press('Escape');
  await page.getByRole('dialog').waitFor({ state: 'hidden' });
  await page.evaluate(() => window.scrollTo(0, 0));
  if (shots) await page.screenshot({ path: join(shots, 'reporting-demo-results.png'), fullPage: true, animations: 'disabled' });
  await page.getByRole('tab', { name: 'Costs & capacity', exact: true }).click();
  await page.getByRole('heading', { name: 'Tools & spend', exact: true }).waitFor();
  await page.setViewportSize({ width: 390, height: 844 });
  assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), 'mobile page overflows');
  if (shots) await page.screenshot({ path: join(shots, 'reporting-demo-mobile.png'), fullPage: true });
  await page.setViewportSize({ width: 1440, height: 1000 });

  // Connected fixtures: inspect all selected milestones and follow native IDs.
  mode = 'connected';
  await page.getByRole('button', { name: /Return to live reporting/ }).click();
  await page.getByText('Connected', { exact: true }).waitFor();
  const compare = page.getByRole('group', { name: 'Compare milestones', exact: true });
  await compare.getByRole('button', { name: /SDR picked up/ }).click();
  await page.getByRole('button', { name: /Inspect hour/ }).click();
  await page.getByRole('dialog').getByText(attribution.name, { exact: true }).first().waitFor();
  const interval = calls.filter(c => c.path === `${root}/reporting/leads`).at(-1);
  assert.deepEqual(interval.query.get('stages').split(','), ['discovered', 'researched', 'verified', 'sdr']);
  await page.keyboard.press('Escape');
  await page.getByRole('dialog').waitFor({ state: 'hidden' });
  await page.evaluate(() => window.scrollTo(0, 0));
  failPrevious = true;
  await page.getByRole('button', { name: 'Results', exact: true }).click();
  await page.getByRole('button', { name: 'Retry comparison', exact: true }).waitFor();
  failPrevious = false;
  await page.getByRole('button', { name: 'Retry comparison', exact: true }).click();
  await page.getByRole('heading', { name: 'Agent performance', exact: true }).waitFor();
  await page.getByRole('button', { name: 'Submission quality', exact: true }).click();
  await page.getByRole('columnheader', { name: 'Duplicates', exact: true }).waitFor();
  await page.getByRole('button', { name: 'Northpeak Example →', exact: true }).click();
  await page.waitForURL(`**/crm/sales%3Alead%3A${lead.id}`);
  await page.getByRole('heading', { name: 'Northpeak Example', exact: true }).waitFor();
  // Opening a conversation only marks it read; reporting never sends or enrols.
  assert(calls.filter(c => c.method !== 'GET').every(c => c.body?.action === 'read'));
  assert.deepEqual(errors, []);
  console.log('sales reporting: queue, live data, explicit demo, comparisons, intervals, mobile and conversation navigation passed');
} finally {
  await browser?.close();
  server.kill();
}
