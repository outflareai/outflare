// The one Inbox against mocked API answers: support and sales views in one
// column, a count of new loop-ins on Watching, the row menu's Move to team
// with Undo, the line that says a teammate is writing in the open
// conversation, Online and Away, and the sound and notifications switch.
// Starts a local Vite server and mocks every API request; needs no backend.
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
const me = '50000000-0000-0000-0000-000000000001';
const leo = '50000000-0000-0000-0000-000000000002';
const sales = '60000000-0000-0000-0000-000000000001';
const success = '60000000-0000-0000-0000-000000000002';
const conv = (id, name, email, subject, extra = {}) => ({
  id, brand_id: brand, record_id: null, contact: { name, email }, mailbox_id: null, mailbox_email: 'help@northpeak.example', via: 'mailbox', inbound_address: null, reply_from: null,
  channel: 'email', identity_verified: false, visitor_seen_at: null, subject, status: 'open', assignee: null, team: null, topic: null, tags: [], unread: true, last_from: 'customer',
  preview: subject, messages: 1, sla: { reply_due_at: null, reply_breached_at: null, resolve_due_at: null, resolve_breached_at: null },
  first_message_at: '2026-11-10T09:00:00Z', first_reply_at: null, closed_at: null, last_message_at: '2026-11-10T09:00:00Z', created_at: '2026-11-10T09:00:00Z', ...extra,
});
const one = '70000000-0000-0000-0000-000000000001';
const two = '70000000-0000-0000-0000-000000000002';
const list = [conv(one, 'Tom Becker', 'tom@brightlane.example', 'Cannot find my invoice'), conv(two, 'Mei Tanaka', 'mei@acme.example', 'Upgrade to the team plan')];
const counts = { closed: 3, mine: 0, open: 2, overdue: 0, pending: 0, unassigned: 2 };
const person = (id, name) => ({ id, name });

let browser;
try {
  for (let i = 0; i < 100 && !output.includes('ready in'); i++) await new Promise(r => setTimeout(r, 200));
  browser = await chromium.launch({ headless: true, args: ['--no-sandbox'], executablePath: process.env.OUTFLARE_TEST_CHROMIUM || undefined });
  const calls = [];
  const shot = async (page, name) => { if (process.env.OUTFLARE_TEST_SHOTS) await page.screenshot({ path: `${process.env.OUTFLARE_TEST_SHOTS}/${name}.png`, fullPage: true }); };
  let presence = 'away';
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const page = await context.newPage();
  const errors = []; page.on('pageerror', e => errors.push(e.message));
  await page.route('**/v1/**', async route => {
    const req = route.request(); const u = new URL(req.url()); const p = u.pathname; const m = req.method();
    calls.push(`${m} ${p}${u.search} ${req.postData() ?? ''}`.trim());
    const B = `/v1/brands/${brand}`; const W = '/v1/workspaces/workspace-a';
    let body = { data: [] };
    if (p === `${W}/brands`) body = { data: [{ id: brand, name: 'Northpeak', handle: 'northpeak', status: 'active', timezone: 'UTC' }] };
    else if (p === `${W}/support/mailboxes`) body = { data: [{ id: '80000000-0000-0000-0000-000000000001', email: 'help@northpeak.example', display_name: 'Northpeak help', brand_id: brand, status: 'active', last_synced_at: null, sync_error: null }] };
    else if (p === `${W}/teams`) body = { data: [{ id: sales, name: 'Sales', member_ids: [] }, { id: success, name: 'Customer success', member_ids: [] }] };
    else if (p === `${W}/members`) body = { data: [{ user_id: me, email: 'maya@northpeak.example', name: 'Maya Lindqvist', role: 'owner' }, { user_id: leo, email: 'leo@northpeak.example', name: 'Leo Marsh', role: 'admin' }] };
    else if (p === `${B}/support/stats`) body = { closed: 0, conversations: 2, counts, days: 30, median_first_reply_seconds: null, median_resolution_seconds: null, replied: 0, sla_missed: 0 };
    else if (p === `${B}/knowledge/suggestions`) body = { counts: { answers: 0, conflicts: 0 }, answers: [], conflicts: [] };
    else if (p === `${B}/inbox-presence`) { if (m === 'PUT') presence = JSON.parse(req.postData()).status; body = { status: presence, online: presence === 'online' ? [person(me, 'Maya Lindqvist')] : [] }; }
    else if (p === `${B}/inbox-loop-ins`) body = { loop_ins: [{ kind: 'support', id: two, name: 'Mei Tanaka', subject: 'Upgrade to the team plan', by: person(leo, 'Leo Marsh'), at: '2026-11-10T09:05:00Z' }] };
    else if (p === `${B}/inbox-viewing`) body = { viewers: [{ id: leo, name: 'Leo Marsh', typing: true }] };
    else if (p === `${B}/support/conversations`) body = { data: list, counts, next_cursor: null, total: list.length };
    else if (p === `${B}/support/conversations/${one}` && m === 'PATCH') { Object.assign(list[0], JSON.parse(req.postData()).team_id ? { team: person(success, 'Customer success') } : { team: null }); body = list[0]; }
    else if (p === `${B}/support/conversations/${one}`) body = { conversation: { ...list[0], unread: false }, followers: [], other_conversations: [], messages: [{ id: '90000000-0000-0000-0000-000000000001', kind: 'in', author: null, from_email: 'tom@brightlane.example', from_name: 'Tom Becker', to_email: 'help@northpeak.example', subject: 'Cannot find my invoice', body: 'Where do I download last month\'s invoice?', auto_reply: false, status: 'received', error: null, created_at: '2026-11-10T09:00:00Z', sent_at: null }] };
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
  });
  const last = (re) => [...calls].reverse().find(c => re.test(c)) ?? '';

  await page.goto(`${origin}/tests/fixtures/inbox.html`);
  await page.getByText('Cannot find my invoice').first().waitFor();

  // One column of views: support's folders and sales' replies.
  const views = page.getByRole('navigation', { name: 'Support folders' });
  let text = await views.innerText();
  for (const want of ['Your inbox', 'Watching', 'Unassigned', 'New replies', 'From support', 'Keyboard shortcuts', 'Sound and notifications']) assert.match(text, new RegExp(want), `the views column has ${want}`);
  // A teammate looped this person in: Watching says so.
  await views.getByText('1 new').waitFor();
  await shot(page, 'inbox-views');

  // Online and Away.
  await views.getByRole('button', { name: 'Online' }).click();
  await page.getByText(/You're online/).waitFor();
  assert.match(last(/PUT .*inbox-presence/), /"status":"online"/);

  // Sound and notifications starts off and turns on.
  const alerts = views.getByRole('button', { name: /Sound and notifications/ });
  assert.equal(await alerts.getAttribute('aria-pressed'), 'false');
  await alerts.click();
  assert.equal(await alerts.getAttribute('aria-pressed'), 'true');

  // The row menu moves a conversation to a team, and Undo takes it back.
  await page.getByText('Cannot find my invoice').first().click({ button: 'right' });
  const menu = page.getByRole('menu');
  for (const want of ['Assign to', 'Move to team', 'Loop in', 'Copy link']) await menu.getByRole('menuitem', { name: new RegExp(want) }).first().waitFor();
  await menu.getByRole('menuitem', { name: /Move to team/ }).click();
  await page.getByRole('menuitem', { name: /Customer success/ }).click();
  await page.getByRole('button', { name: 'Undo' }).waitFor();
  assert.match(last(/PATCH .*support\/conversations/), new RegExp(`"team_id":"${success}"`));
  await page.getByRole('button', { name: 'Undo' }).click();
  await page.getByText('Undone.').waitFor();
  assert.match(last(/PATCH .*support\/conversations/), /"team_id":null/);

  // Open it: a teammate is writing there, and the page said this person is there too.
  await page.getByText('Cannot find my invoice').first().click();
  await page.getByText('Leo Marsh is writing here right now. Check with them before you answer.').waitFor();
  assert.match(last(/GET .*inbox-viewing/), new RegExp(`kind=support&id=${one}`));
  await shot(page, 'inbox-conversation');
  assert.deepEqual(errors, []);
  console.log('inbox: ok');
} catch (e) {
  console.error(output);
  throw e;
} finally {
  await browser?.close();
  server.kill();
}
