// The Brand kit page against mocked API answers: the list with what can't
// be used yet, the gallery (remembered), adding a file with what it is,
// one file's page (rename, note, tags, confirming and taking back consent,
// archive, delete), the brand's colours, a server without file storage,
// and someone who can't manage. Starts a local Vite server and mocks every
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
const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==', 'base64');
const file = (n, kind, name, extra = {}) => {
  const needed = kind === 'person' || kind === 'voice';
  const a = { id: `90000000-0000-0000-0000-00000000000${n}`, brand_id: brand, kind, name, note: '', tags: [], filename: `${name}.png`, content_type: 'image/png', family: 'image', bytes: 38_000, sha256: String(n).repeat(64), width: 1600, height: 400, resizable: true, source: 'upload',
    consent: { needed, confirmed: false, at: null, by: null, note: '' }, usable: !needed, archived_at: null, version: 1, created_by: null, created_at: '2026-11-12T09:00:00Z', updated_at: '2026-11-12T09:00:00Z', ...extra };
  a.file_path = `/v1/brands/${brand}/assets/${a.id}/file`;
  return a;
};

let browser;
try {
  let ready = false;
  for (let n = 0; n < 100; n++) { try { ready = (await fetch(origin)).ok; if (ready) break; } catch {} await new Promise(r => setTimeout(r, 100)); }
  assert(ready, `Vite did not start: ${output}`);
  browser = await chromium.launch({ headless: true, args: ['--no-sandbox'], executablePath: process.env.OUTFLARE_TEST_CHROMIUM || undefined });
  const shot = async (page, name) => { if (process.env.OUTFLARE_TEST_SHOTS) await page.screenshot({ path: `${process.env.OUTFLARE_TEST_SHOTS}/${name}.png`, fullPage: true }); };

  const start = async ({ storage = true, query = '' } = {}) => {
    const state = { files: [
      file(1, 'logo', 'Main logo', { tags: ['dark backgrounds'], note: 'Use on dark backgrounds only.' }),
      file(2, 'font', 'Headline font', { content_type: 'font/woff2', family: 'font', width: null, height: null, resizable: false, filename: 'headline.woff2' }),
      file(3, 'person', 'Ana, presenter'),
      file(4, 'voice', 'Ana, voice', { content_type: 'audio/mpeg', family: 'audio', width: null, height: null, resizable: false, filename: 'ana.mp3', consent: { needed: true, confirmed: true, at: '2026-11-03T10:00:00Z', by: null, note: 'Signed release, 3 Nov' }, usable: true }),
    ], calls: [], bodies: [] };
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 }, colorScheme: 'light' });
    page.on('pageerror', e => { throw e; });
    await page.route('**/v1/**', async route => {
      const req = route.request(); const u = new URL(req.url()); const p = u.pathname; const m = req.method();
      state.calls.push(`${m} ${p}${u.search}`);
      const body = m === 'GET' || m === 'DELETE' ? null : JSON.parse(req.postData() || 'null');
      if (body) state.bodies.push([`${m} ${p}`, body]);
      const base = `/v1/brands/${brand}/assets`;
      const one = state.files.find(f => p.startsWith(`${base}/${f.id}`));
      const json = (status, b) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(b) });
      if (p === base && m === 'GET') return json(200, { data: state.files.filter(f => !!f.archived_at === (u.searchParams.get('archived') === 'true')), storage });
      if (p === base && m === 'POST') {
        if (body.filename === 'script.png') return json(400, { error: { code: 'invalid_request', message: 'That file is not a picture, video, sound file or font.', request_id: 'r' } });
        const a = file(state.files.length + 1, body.kind, body.filename.replace(/\.[^.]+$/, ''), { filename: body.filename });
        state.files.unshift(a); return json(201, a);
      }
      if (one && p.endsWith('/file')) return route.fulfill({ status: 200, contentType: 'image/png', body: png });
      if (one && p.endsWith('/archive')) { one.archived_at = '2026-11-19T00:00:00Z'; one.usable = false; return json(200, one); }
      if (one && p.endsWith('/restore')) { one.archived_at = null; return json(200, one); }
      if (one && m === 'PATCH') {
        if (body.version !== one.version) return json(409, { error: { code: 'conflict', message: 'The file changed since you opened it.', request_id: 'r' } });
        for (const k of ['name', 'note', 'tags', 'kind']) if (body[k] !== undefined) one[k] = body[k];
        if (body.consent) { one.consent = { ...one.consent, confirmed: !!body.consent.confirmed, note: body.consent.note ?? '', at: body.consent.confirmed ? '2026-11-19T00:00:00Z' : null }; }
        one.usable = !one.archived_at && (!one.consent.needed || one.consent.confirmed); one.version++;
        return json(200, one);
      }
      if (one && m === 'DELETE') { state.files = state.files.filter(f => f !== one); return route.fulfill({ status: 204 }); }
      if (p === `/v1/workspaces/workspace-a/brands/${brand}` && m === 'PATCH') { await page.evaluate(c => { window.savedColors = c; }, body.colors); return json(200, {}); }
      return json(404, { error: { code: 'not_found', message: 'nothing here', request_id: 'r' } });
    });
    await page.goto(`${origin}/tests/fixtures/brand-kit.html${query}`);
    await page.getByRole('heading', { name: "Acme's brand kit" }).waitFor();
    return { page, state };
  };

  {
    const { page, state } = await start();
    // The list: every file, what it is, its consent and whether it can be used.
    const rows = page.locator('tbody tr');
    await rows.first().waitFor();
    assert.equal(await rows.count(), 4);
    assert.match(await rows.nth(0).innerText(), /Main logo[\s\S]*dark backgrounds[\s\S]*Logo[\s\S]*PNG · 1600 × 400[\s\S]*Not needed[\s\S]*Ready to use/);
    assert.match(await rows.nth(2).innerText(), /Ana, presenter[\s\S]*Person[\s\S]*Needs consent[\s\S]*Can't be used yet/);
    assert.match(await rows.nth(3).innerText(), /Ana, voice[\s\S]*Consent on file[\s\S]*Ready to use/);
    await page.getByText("1 file can't be used yet").waitFor();
    // Pictures come as small copies.
    assert(state.calls.some(c => c.endsWith('/file?width=240')), state.calls.join('\n'));
    await shot(page, 'brand-kit-list');

    // Filters and search.
    await page.getByRole('button', { name: /^Fonts 1$/ }).click();
    assert.equal(await rows.count(), 1);
    await page.getByRole('button', { name: /^All 4$/ }).click();
    await page.getByRole('searchbox', { name: 'Search files' }).fill('dark');
    assert.equal(await rows.count(), 1);
    await page.getByRole('searchbox', { name: 'Search files' }).fill('nothing like this');
    await page.getByText('No file matches').waitFor();
    await page.getByRole('button', { name: 'Show all files' }).click();
    assert.equal(await rows.count(), 4);

    // The gallery, remembered for next time.
    await page.getByRole('button', { name: /Gallery/ }).click();
    await page.getByRole('button', { name: 'Main logo' }).waitFor();
    assert.equal(await page.locator('table').count(), 0);
    assert.equal(await page.evaluate(() => localStorage.getItem('outflare.brandkit.view')), 'gallery');
    await shot(page, 'brand-kit-gallery');
    await page.getByRole('button', { name: /List/ }).click();

    // Adding files: each says what it is before anything is sent; a refusal
    // keeps the file on the list with the reason.
    await page.getByLabel('Files to add').setInputFiles([{ name: 'new-logo.png', mimeType: 'image/png', buffer: png }, { name: 'script.png', mimeType: 'image/png', buffer: png }, { name: 'intro.mp3', mimeType: 'audio/mpeg', buffer: Buffer.from('ID3') }]);
    await page.getByText('Add these 3 files').waitFor();
    assert.equal(await page.getByLabel('What new-logo.png is').inputValue(), 'logo');
    assert.equal(await page.getByLabel('What intro.mp3 is').inputValue(), 'voice');
    await page.getByText("can't be used until you confirm that person agreed").waitFor();
    await page.getByLabel('What script.png is').selectOption('screenshot');
    await shot(page, 'brand-kit-adding');
    await page.getByRole('button', { name: 'Add 3 files' }).click();
    await page.getByText('2 files added.').waitFor();
    await page.getByText('script.png: That file is not a picture').waitFor();
    await page.getByText('Add this file').waitFor();
    await page.getByRole('button', { name: 'Cancel' }).click();
    const posts = state.bodies.filter(([k]) => k.startsWith('POST'));
    assert.deepEqual(posts.map(([, b]) => [b.kind, b.filename]), [['logo', 'new-logo.png'], ['screenshot', 'script.png'], ['voice', 'intro.mp3']]);
    assert.equal(posts[0][1].data, png.toString('base64'));
    assert.equal(await rows.count(), 6);
    await page.getByText("2 files can't be used yet").waitFor();
    await page.getByRole('button', { name: 'Show these 2' }).click();
    assert.equal(await rows.count(), 2);

    // One file: confirming consent needs how they agreed and the tick.
    await page.getByRole('button', { name: 'Ana, presenter' }).click();
    await page.getByText("This shows a real person, so it can't be used until they agree").waitFor();
    await page.getByRole('button', { name: 'Confirm consent' }).click();
    const save = page.getByRole('button', { name: 'Save' });
    assert(await save.isDisabled());
    await page.getByLabel('How did they agree?').fill('Signed release, 18 Nov');
    assert(await save.isDisabled());
    await page.getByLabel("The person shown agreed to be in Acme's ads").check();
    await shot(page, 'brand-kit-file');
    await save.click();
    await page.getByText('Consent confirmed. The file is ready to use.').waitFor();
    assert.deepEqual(state.bodies.at(-1)[1], { consent: { confirmed: true, note: 'Signed release, 18 Nov' }, version: 1 });
    await page.getByText('Signed release, 18 Nov').waitFor();
    // Taking it back asks first.
    await page.getByRole('button', { name: 'Take back' }).click();
    await page.getByRole('button', { name: 'Take back consent' }).click();
    await page.getByText("Consent taken back. The file can't be used.").waitFor();
    assert.deepEqual(state.bodies.at(-1)[1], { consent: { confirmed: false }, version: 2 });

    // Rename, note and tags, each with Save and Cancel.
    await page.getByRole('button', { name: '✎ Rename' }).click();
    await page.getByLabel('Name').fill('Ana on stage');
    await page.getByRole('button', { name: 'Save' }).click();
    await page.getByRole('heading', { name: 'Ana on stage' }).waitFor();
    await page.getByRole('button', { name: 'Edit', exact: true }).click();
    await page.getByLabel('When to use it').fill('Talking videos only.');
    await page.getByRole('button', { name: 'Cancel' }).click();
    await page.getByText('No note yet').waitFor();
    await page.getByRole('button', { name: '+ Add' }).click();
    await page.getByLabel('New tag').fill('Talking Video');
    await page.getByRole('button', { name: 'Save' }).click();
    await page.getByRole('button', { name: 'Remove tag talking video' }).waitFor();
    assert.deepEqual(state.bodies.at(-1)[1], { tags: ['talking video'], version: 4 });

    // Archive keeps the file out of the list; Show archived finds it; delete asks first.
    await page.getByRole('button', { name: 'Archive' }).click();
    await page.getByText('Ana on stage is archived.').waitFor();
    await page.getByRole('heading', { name: "Acme's brand kit" }).waitFor();
    await page.getByRole('button', { name: /^All 5$/ }).waitFor();
    await page.getByLabel('Show archived').check();
    await page.getByRole('button', { name: 'Ana on stage' }).click();
    await page.getByRole('button', { name: 'Delete for good' }).click();
    await page.getByText("This can't be undone").waitFor();
    await page.getByRole('button', { name: 'Delete for good' }).last().click();
    await page.getByText('Ana on stage is deleted.').waitFor();
    assert(state.calls.some(c => c.startsWith('DELETE ')), state.calls.join('\n'));
    await page.getByText('Nothing is archived').waitFor();
    await page.getByLabel('Show archived').uncheck();

    // Colours: shown, edited with Save, refused when not a colour.
    await page.getByText('#16a34a').waitFor();
    await page.getByRole('button', { name: 'Edit colours' }).click();
    await page.getByLabel('Background colour', { exact: true }).fill('white');
    await page.getByText('A colour is a # and three or six letters and numbers').waitFor();
    await page.getByLabel('Background colour', { exact: true }).fill('#ffffff');
    await page.getByRole('button', { name: 'Save' }).click();
    await page.getByText('Colours saved.').waitFor();
    assert.deepEqual(state.bodies.at(-1)[1], { colors: { primary: '#16a34a', background: '#ffffff', text: '#0a0a0a' } });
    await page.getByText('#ffffff').waitFor();
    await page.close();
  }

  {
    // No file storage: the page says so and offers no way to add files.
    const { page } = await start({ storage: false });
    await page.getByText("Files can't be added on this server yet").waitFor();
    assert.equal(await page.getByRole('button', { name: '+ Add files' }).count(), 0);
    await page.close();
  }

  {
    // Someone who can't manage reads everything and changes nothing.
    const { page } = await start({ query: '?readonly' });
    assert.equal(await page.getByRole('button', { name: '+ Add files' }).count(), 0);
    assert.equal(await page.getByRole('button', { name: 'Edit colours' }).count(), 0);
    await page.getByRole('button', { name: 'Ana, presenter' }).click();
    await page.getByText("This shows a real person").waitFor();
    for (const name of ['Confirm consent', '✎ Rename', 'Archive', 'Delete for good', '+ Add']) assert.equal(await page.getByRole('button', { name }).count(), 0, name);
    await page.getByRole('button', { name: 'Download' }).waitFor();
    await page.close();
  }
  console.log('ok');
} finally { if (browser) await browser.close(); server.kill(); }
