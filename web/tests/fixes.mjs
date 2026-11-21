// The Fixes page against mocked API answers: a ticket opens as a page of
// its own (no sheet over the app), says what is wrong with examples before
// it is written up and explains its labels; once written, the write-up is
// formatted, and Copy and Give to agent ask for the issue and the agent
// instructions. Starts a local Vite server and mocks every API request;
// needs no backend.
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
const site = '20000000-0000-0000-0000-000000000001';
const plain = '30000000-0000-0000-0000-000000000001';
const written = '30000000-0000-0000-0000-000000000002';
const rule = (id, title, why, fix) => ({ id, severity: 'notice', category: 'links', title, why, fix });
const ticket = (id, number, r, extra = {}) => ({
  id, number, url: `https://outflare.example/acme/northpeak/fixes/${id}`, source: 'seo_audit', subject: { type: 'seo_site', id: site }, brand_id: brand, rule: r, title: r.title, status: 'open',
  count: 3, unit: 'pages', parts: 2, open_parts: 2, brief_status: 'none', brief: null, brief_error: '', brief_model: '', brief_feedback: '', brief_written_at: null,
  review: '', review_note: '', reviewed_by: null, reviewed_at: null, impact: { clicks: 0, impressions: 701, at: '2026-11-10T00:00:00Z' }, created_at: '2026-11-01T00:00:00Z', updated_at: '2026-11-10T00:00:00Z', ...extra,
});
const brief = {
  kind: 'code', title: 'Use hyphens in 3 integration page addresses', summary: 'Three integration pages have underscores in their addresses, such as `/integrations/cal_com/`.',
  why: 'Search engines read a hyphen as a space between words and an underscore as nothing. The gain is small.',
  change: '1. Search the code for `cal_com`.\n2. Make the public address use hyphens.\n\n| Now | After |\n| --- | --- |\n| `/integrations/cal_com/` | `/integrations/cal-com/` |',
  done_when: ['`https://northpeak.example/integrations/cal-com/` loads.'], dont: ['Don\'t rename the ids other code uses.'],
};
const tickets = [
  ticket(plain, 127, rule('link_no_text', 'Links without text', 'The page has links with nothing readable inside them.', 'Put words in each link that describe where it goes.')),
  ticket(written, 128, rule('url_underscores', 'Underscores in the URL', 'Underscores join words.', 'Use hyphens.'), { title: brief.title, brief, brief_status: 'ready', brief_model: 'test-model', brief_written_at: '2026-11-10T00:00:00Z' }),
];
const fix = (id, group) => ({ id, source: 'seo_audit', subject: { type: 'seo_site', id: site }, brand_id: brand, rule: tickets[0].rule, group, status: 'open', count: 2, unit: 'pages', assignee: null, last_found_by: null, created_at: '2026-11-01T00:00:00Z', updated_at: '2026-11-01T00:00:00Z' });
const detail = (t) => ({
  ...t, fixes: [fix('40000000-0000-0000-0000-000000000001', '/docs/*'), fix('40000000-0000-0000-0000-000000000002', '/blog/*')],
  pages: [{ fix_id: '40000000-0000-0000-0000-000000000001', url: 'https://northpeak.example/docs/start', detail: '2 links, to https://northpeak.example/, https://northpeak.example/pricing' }, { fix_id: '40000000-0000-0000-0000-000000000002', url: 'https://northpeak.example/blog/hello', detail: '1 link, to https://northpeak.example/' }],
  related: [{ id: written, title: 'Another ticket nobody asked to see', rule: 'url_underscores', status: 'open', review: '' }], guidelines: [],
});

let browser;
try {
  for (let i = 0; i < 100 && !output.includes('ready in'); i++) await new Promise(r => setTimeout(r, 200));
  browser = await chromium.launch({ headless: true, args: ['--no-sandbox'], executablePath: process.env.OUTFLARE_TEST_CHROMIUM || undefined });
  const calls = [];
  // OUTFLARE_TEST_SHOTS names a folder to save a screenshot of each screen in.
  const shot = async (page, name) => { if (process.env.OUTFLARE_TEST_SHOTS) await page.screenshot({ path: `${process.env.OUTFLARE_TEST_SHOTS}/${name}.png`, fullPage: true }); };
  const mock = (page) => page.route('**/v1/**', async route => {
    const req = route.request(); const u = new URL(req.url()); const p = u.pathname; const q = u.searchParams;
    calls.push(`${req.method()} ${p}${u.search}`);
    const B = `/v1/brands/${brand}`;
    let status = 200, body = {};
    const one = tickets.find(t => p.startsWith(`${B}/fix-tickets/${t.id}`));
    if (p === '/v1/workspaces/workspace-a/brands') body = { data: [{ id: brand, name: 'Northpeak', handle: 'northpeak', status: 'active', timezone: 'UTC' }] };
    else if (p === `${B}/seo-sites`) body = { data: [{ id: site, host: 'northpeak.example' }] };
    else if (p === `${B}/fix-tickets`) body = { data: tickets };
    else if (one && p.endsWith('/prompt')) body = { prompt: `${q.get('for')} prompt for #${one.number}`, pages: [] };
    else if (one) body = detail(one);
    else { status = 404; body = { error: { code: 'not_found', message: 'nothing here', request_id: 'r' } }; }
    await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  });
  const last = (re) => [...calls].reverse().find(c => re.test(c)) ?? '';

  const context = await browser.newContext({ viewport: { width: 1280, height: 1000 } });
  await context.grantPermissions(['clipboard-read', 'clipboard-write'], { origin });
  const page = await context.newPage();
  const errors = []; page.on('pageerror', e => errors.push(e.message));
  await mock(page);
  await page.goto(`${origin}/tests/fixtures/fixes.html`);

  // A ticket that isn't written up yet opens as a page, not a sheet.
  await page.getByText('Links without text').locator('visible=true').first().click();
  await page.getByRole('heading', { name: /Links without text/ }).waitFor();
  assert.equal(await page.getByRole('dialog').count(), 0, 'a ticket is a page, not a sheet over the app');
  await page.getByText('2 links, to https://northpeak.example/, https://northpeak.example/pricing').first().waitFor();
  let text = await page.locator('#root').innerText();
  assert.match(text, /#127/);
  assert.match(text, /WHAT'S WRONG/i);
  assert.match(text, /FOR EXAMPLE/i);
  assert.match(text, /Minor\. Worth tidying, not urgent\./);
  assert.match(text, /Nobody has picked it up yet\./);
  assert.match(text, /showed these pages in search 701 times/);
  assert.match(text, /Outflare fix #127/);
  assert.doesNotMatch(text, /Other fixes on this site|Another ticket nobody asked to see/);
  await shot(page, 'fixes-ticket-plain');

  // Back to the list, then the written ticket: formatted, with both copies.
  await page.getByRole('button', { name: '← Fixes' }).click();
  await page.getByText(brief.title).locator('visible=true').first().click();
  await page.getByRole('heading', { name: new RegExp(brief.title) }).waitFor();
  await page.getByRole('table').waitFor();
  text = await page.locator('#root').innerText();
  assert.doesNotMatch(text, /`/, 'the write-up is formatted, not raw Markdown');
  assert.match(text, /Needs review/);
  await shot(page, 'fixes-ticket-written');
  await page.getByRole('button', { name: 'Give to agent', exact: true }).click();
  await page.getByText(/Paste it into Claude Code/).waitFor();
  assert.match(last(/\/prompt/), /for=agent/);
  assert.equal(await page.evaluate(() => navigator.clipboard.readText()), 'agent prompt for #128');
  await page.getByRole('button', { name: 'Copy', exact: true }).first().click();
  await page.getByText(/Paste it into GitHub/).waitFor();
  assert.match(last(/\/prompt/), /for=issue/);
  await page.getByRole('button', { name: 'Copy the link to this ticket' }).click();
  assert.equal(await page.evaluate(() => navigator.clipboard.readText()), tickets[1].url);
  assert.deepEqual(errors, []);
  await page.close();

  // On a phone: no sideways scroll on the page itself.
  const phone = await browser.newPage({ viewport: { width: 390, height: 844 } });
  await mock(phone);
  await phone.goto(`${origin}/tests/fixtures/fixes.html`);
  await phone.getByText(brief.title).locator('visible=true').first().click();
  await phone.getByRole('table').waitFor();
  await shot(phone, 'fixes-ticket-phone');
  assert.ok(await phone.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1), 'no sideways scroll on a phone');
  console.log('fixes: ok');
} catch (e) {
  console.error(output);
  throw e;
} finally {
  await browser?.close();
  server.kill();
}
