// The Alerts page's "Where they go" card against mocked API answers: each
// kind of alert can post to a chat of the workspace's own Telegram bot,
// beside Slack and email. The chats the bot heard from are listed, a
// saved chat that isn't keeps its name, Another chat takes a chat id, Not
// in Telegram stops it; without a bot the page links to the connector, and
// someone who can't manage sees no chat pickers. Starts a local Vite
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
const slack = '20000000-0000-0000-0000-000000000001';
const bot = '20000000-0000-0000-0000-000000000002';
const now = '2026-11-12T00:00:00Z';
const kinds = [
  { id: 'reply.hot', title: 'Hot replies', description: 'A reply that sounds interested.' },
  { id: 'visitor.hot', title: 'Hot website visitors', description: 'A company looking at pricing.' },
];
const setting = (kind, extra = {}) => ({ brand_id: brand, kind, email: null, slack_integration_id: null, slack_channel: null, slack_channel_name: null,
  telegram_integration_id: null, telegram_chat: null, telegram_chat_name: null, updated_at: now, ...extra });

let browser;
try {
  for (let i = 0; i < 100 && !output.includes('ready in'); i++) await new Promise(r => setTimeout(r, 200));
  browser = await chromium.launch({ headless: true, args: ['--no-sandbox'], executablePath: process.env.OUTFLARE_TEST_CHROMIUM || undefined });
  const open = async ({ integrations, chats = [], query = '' }) => {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    const puts = []; const mutes = [];
    let muted = ['visitor.hot'];
    // The saved chat for visitor.hot is one the bot no longer lists.
    let settings = [setting('visitor.hot', { telegram_integration_id: bot, telegram_chat: '-1005550001', telegram_chat_name: 'Old sales room' })];
    await page.route('**/v1/**', async route => {
      const req = route.request(); const u = new URL(req.url()); const p = u.pathname; const method = req.method();
      const b = req.postData() ? JSON.parse(req.postData()) : null;
      let status = 200, body = {};
      if (p === '/v1/workspaces/workspace-a/brands') body = { data: [{ id: brand, name: 'Acme', handle: 'acme', status: 'active' }] };
      else if (p === '/v1/workspaces/workspace-a/integrations') body = { data: integrations };
      else if (p === '/v1/workspaces/workspace-a/alerts/channels') body = { data: u.searchParams.get('integration_id') === bot ? chats : [{ id: 'C1', name: 'general', private: false }] };
      else if (p === `/v1/brands/${brand}/alerts`) body = { data: [], next_cursor: null };
      else if (p === `/v1/brands/${brand}/alerts/settings` && method === 'PUT') {
        puts.push(b);
        if (b.telegram_chat === '-100404') { status = 400; body = { error: { code: 'invalid_request', message: 'telegram_chat is not a chat this bot can see', request_id: 'r' } }; }
        else {
          const old = settings.find(s => s.kind === b.kind) ?? setting(b.kind);
          const next = { ...old };
          if ('telegram_chat' in b) Object.assign(next, { telegram_integration_id: b.telegram_integration_id, telegram_chat: b.telegram_chat,
            telegram_chat_name: b.telegram_chat === null ? null : (chats.find(c => c.id === b.telegram_chat)?.name ?? 'Acme alerts') });
          settings = [...settings.filter(s => s.kind !== b.kind), next];
          body = next;
        }
      }
      else if (p === `/v1/brands/${brand}/alerts/mutes` && method === 'PUT') {
        mutes.push(b);
        const ks = b.kinds ?? [b.kind];
        const before = muted.length;
        muted = b.muted ? [...new Set([...muted, ...ks])] : muted.filter(k => !ks.includes(k));
        body = { ...(b.kinds ? { kinds: ks } : { kind: b.kind }), muted: b.muted, changed: Math.abs(muted.length - before) };
      }
      else if (p === `/v1/brands/${brand}/alerts/settings`) body = { kinds, default_email: 'owner', daily_email_limit: 50, settings, muted };
      else { status = 404; body = { error: { code: 'not_found', message: 'nothing here', request_id: 'r' } }; }
      await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
    });
    await page.goto(`${origin}/tests/fixtures/alerts.html${query}`);
    await page.getByText('Where they go').waitFor();
    return { page, errors, puts, mutes };
  };
  const connected = (id, connector) => ({ id, connector, status: 'connected' });

  // A bot and Slack connected: both pickers show for each kind.
  {
    const chats = [{ id: '-1001234567890', name: 'Growth team', private: false }, { id: '4242', name: 'Sam Lee', private: true }];
    const { page, errors, puts } = await open({ integrations: [connected(slack, 'slack'), connected(bot, 'telegram_bot')], chats });
    const hot = page.getByLabel('Telegram chat for Hot replies');
    await hot.waitFor();
    await page.getByLabel('Slack channel for Hot replies').waitFor();
    assert.deepEqual(await hot.locator('option').allInnerTexts(), ['Not in Telegram', 'Growth team', 'Sam Lee', 'Another chat…']);
    // A saved chat the bot no longer lists keeps its name.
    const visitors = page.getByLabel('Telegram chat for Hot website visitors');
    assert.equal(await visitors.inputValue(), '-1005550001');
    assert.match((await visitors.locator('option').allInnerTexts()).join('|'), /Old sales room/);
    // Picking a listed chat saves it with the bot's connection.
    await hot.selectOption('-1001234567890');
    await page.waitForFunction(() => document.querySelector('[aria-label="Telegram chat for Hot replies"]').value === '-1001234567890');
    assert.deepEqual(puts.at(-1), { kind: 'reply.hot', telegram_integration_id: bot, telegram_chat: '-1001234567890' });
    // Another chat asks for its id; one the bot can't see says so.
    await hot.selectOption('other');
    const dialog = page.getByRole('alertdialog');
    await dialog.getByText('Add your bot to the group or channel first').waitFor();
    await dialog.getByLabel('Chat id or @name').fill('-100404');
    await dialog.getByRole('button', { name: 'Post here' }).click();
    await page.getByText('telegram_chat is not a chat this bot can see').waitFor();
    assert.equal(await hot.inputValue(), '-1001234567890');
    await hot.selectOption('other');
    await page.getByRole('alertdialog').getByLabel('Chat id or @name').fill(' @acme_alerts ');
    await page.getByRole('alertdialog').getByRole('button', { name: 'Post here' }).click();
    await page.waitForFunction(() => document.querySelector('[aria-label="Telegram chat for Hot replies"]').value === '@acme_alerts');
    assert.deepEqual(puts.at(-1), { kind: 'reply.hot', telegram_integration_id: bot, telegram_chat: '@acme_alerts' });
    assert.match((await hot.locator('option').allInnerTexts()).join('|'), /Acme alerts/);
    // Not in Telegram stops it.
    await hot.selectOption('');
    await page.waitForFunction(() => document.querySelector('[aria-label="Telegram chat for Hot replies"]').value === '');
    assert.deepEqual(puts.at(-1), { kind: 'reply.hot', telegram_integration_id: null, telegram_chat: null });
    assert.equal(await page.getByText('Connect your Telegram bot').count(), 0);
    assert.deepEqual(errors, []);
    await page.close();
  }

  // Bulk mute: one click mutes every kind shown, Undo puts back only what
  // it changed, Show narrows the kinds and ticks pick some of them.
  {
    const { page, errors, mutes } = await open({ integrations: [] });
    await page.getByText('All 2 kinds shown').waitFor();
    await page.getByRole('button', { name: 'Mute all 2 for me', exact: true }).click();
    await page.getByText('Muted 1 kind for you').waitFor();
    assert.deepEqual(mutes.at(-1), { kinds: ['reply.hot', 'visitor.hot'], muted: true });
    // The list re-renders after the toast, so wait for the button to go.
    await page.getByRole('button', { name: 'Mute all 2 for me', exact: true }).waitFor({ state: 'detached' });
    await page.getByRole('button', { name: 'Undo' }).click();
    await page.getByRole('button', { name: 'Mute all 2 for me', exact: true }).waitFor();
    assert.deepEqual(mutes.at(-1), { kinds: ['reply.hot'], muted: false });
    await page.getByLabel('Show kinds of alert').selectOption('muted');
    await page.getByText('All 1 kind shown').waitFor();
    await page.getByRole('button', { name: 'Unmute all 1 for me', exact: true }).click();
    await page.getByText('No kinds match').waitFor();
    assert.deepEqual(mutes.at(-1), { kinds: ['visitor.hot'], muted: false });
    await page.getByLabel('Show kinds of alert').selectOption('all');
    await page.getByLabel('Tick Hot replies').click();
    await page.getByText('1 ticked').waitFor();
    await page.getByRole('button', { name: 'Mute 1 for me', exact: true }).click();
    await page.getByText('All 2 kinds shown').waitFor();
    assert.deepEqual(mutes.at(-1), { kinds: ['reply.hot'], muted: true });
    await page.getByLabel('Find a kind of alert').fill('website');
    await page.getByText('All 1 kind shown').waitFor();
    assert.deepEqual(errors, []);
    await page.close();
  }

  // A bot that heard from no chat: how to get one listed.
  {
    const { page, errors } = await open({ integrations: [connected(bot, 'telegram_bot')] });
    await page.getByText("Your Telegram bot hasn't heard from a chat in the last day.").waitFor();
    assert.deepEqual(await page.getByLabel('Telegram chat for Hot replies').locator('option').allInnerTexts(), ['Not in Telegram', 'Another chat…']);
    assert.deepEqual(errors, []);
    await page.close();
  }

  // No bot: a link to connect one, and no chat picker.
  {
    const { page, errors } = await open({ integrations: [] });
    const link = page.getByRole('link', { name: 'Connect your Telegram bot' });
    await link.waitFor();
    assert.match(await link.getAttribute('href'), /connectors\/telegram_bot$/);
    assert.equal(await page.getByLabel('Telegram chat for Hot replies').count(), 0);
    assert.deepEqual(errors, []);
    await page.close();
  }

  // Someone who can't manage: no pickers, on a phone without sideways scroll.
  {
    const { page, errors } = await open({ integrations: [connected(bot, 'telegram_bot')], query: '?readonly' });
    await page.setViewportSize({ width: 390, height: 844 });
    await page.getByText('Only owners and admins can change who gets them.').waitFor();
    assert.equal(await page.getByLabel('Telegram chat for Hot replies').count(), 0);
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1), 'no sideways scroll on a phone');
    assert.deepEqual(errors, []);
    await page.close();
  }
  console.log('alerts: ok');
} catch (e) {
  console.error(output);
  throw e;
} finally {
  await browser?.close();
  server.kill();
}
