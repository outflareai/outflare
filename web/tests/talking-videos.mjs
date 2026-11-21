// The talking videos pages against mocked API answers: the list that
// opens on what needs a person, a new video, one video's page (a fix to
// check: keep it and remember, or go back saying why and try again; fix
// one part by its words; like and not this; versions and going back to
// one; the history), variations with one thing changed, lessons, and
// someone who can't manage. Starts a local Vite server and mocks every
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
const vid = n => `60000000-0000-0000-0000-00000000000${n}`;
const file = n => `90000000-0000-0000-0000-0000000000${String(n).padStart(2, '0')}`;
const settings = { voice: { voice: 'voice_test_1', model: 'voice-model-test' }, words: { model: 'words-model-test' }, video: { model: 'acme/talk/reference-to-video', options: { resolution: '480p' } }, upscale: { model: 'acme/upscale/video' }, finish: { tighten: true, phone_look: true, captions: true } };
const wordsOf = text => text.split(' ').map((t, i) => ({ text: t, start: i * 0.5, end: i * 0.5 + 0.4 }));
const version = (number, state, final, text, extra = {}) => ({ number, state, base_asset_id: file(final), final_asset_id: file(final), words: wordsOf(text), seconds: 4, notes: [], edit: null, reason: '', note: '', decided_by: null, decided_at: null, created_at: '2026-11-20T09:00:00Z', ...extra });
const script = 'Your scraper is blocked again. Northpeak picks a clean address.';
const video = (n, name, extra = {}) => ({ id: vid(n), brand_id: brand, name, script, direction: 'A man in a grey hoodie talks to his phone camera.', scene_asset_id: file(50), settings, status: 'ready', stage: 'done', round: 0, edit: null, error_code: null, error: null,
  voice_asset_id: file(1), render_asset_id: file(2), base_asset_id: file(3), final_asset_id: file(10 + n), words: wordsOf(script.replace(/[.]/g, '')), seconds: 4.9, notes: [], waiting_version: null, variation_of: null, variation_change: {}, variations: 0,
  rating: '', rating_reason: '', rated_at: null, versions: [version(1, 'current', 10 + n, script)], steps: [], created_by: null, created_at: '2026-11-20T09:00:00Z', updated_at: '2026-11-20T09:00:00Z', finished_at: '2026-11-20T09:05:00Z', ...extra });
const line = (n, kind, said = '', data = {}) => ({ id: `50000000-0000-0000-0000-0000000000${String(n).padStart(2, '0')}`, kind, said, data, actor_id: kind === 'fix_ready' ? null : '40000000-0000-0000-0000-000000000001', actor_name: kind === 'fix_ready' ? null : 'Ana', created_at: '2026-11-20T09:10:00Z' });

let browser;
try {
  let ready = false;
  for (let n = 0; n < 100; n++) { try { ready = (await fetch(origin)).ok; if (ready) break; } catch {} await new Promise(r => setTimeout(r, 100)); }
  assert(ready, `Vite did not start: ${output}`);
  browser = await chromium.launch({ headless: true, args: ['--no-sandbox'], executablePath: process.env.OUTFLARE_TEST_CHROMIUM || undefined });
  const shot = async (page, name) => { if (process.env.OUTFLARE_TEST_SHOTS) await page.screenshot({ path: `${process.env.OUTFLARE_TEST_SHOTS}/${name}.png`, fullPage: true }); };

  const start = async (query = '', empty = false) => {
    const fixed = 'Your scraper is blocked again. Northpeak picks a clean address.';
    const state = {
      finishing: true, calls: [], bodies: [], history: {}, voicesOff: false,
      lessons: [{ id: '30000000-0000-0000-0000-000000000001', kind: 'rule', word: '', say: '', rule: 'No logos on clothing', applies_to: 'video', source_type: '', source_id: null, created_by: null, created_at: '2026-11-19T09:00:00Z' }],
      videos: empty ? [] : [
        video(1, 'Blocked at 2am', { waiting_version: 2, variations: 1, round: 1, versions: [
          version(2, 'waiting', 21, fixed, { edit: { action: 'redo', from: 5, to: 5, text: 'Northpeak', say: 'north-peek', note: 'She says North-pack' } }), version(1, 'current', 11, script)] }),
        video(2, 'Blocked at 2am (variation 1)', { variation_of: vid(1), variation_change: { change: 'first_line', value: 'Still rotating proxies by hand?', from: vid(1) }, script: 'Still rotating proxies by hand? Northpeak picks a clean address.' }),
        video(3, 'One line of code', { status: 'working', stage: 'render', final_asset_id: null, base_asset_id: null, words: [], seconds: 0, versions: [] }),
        video(4, 'Agency owner, price', { rating: 'liked', rating_reason: 'The hook is strong', rated_at: '2026-11-20T10:00:00Z' }),
        video(5, '190 countries', { status: 'failed', stage: 'render', error_code: 'connector_invalid', error: 'The video model refused the scene picture.', final_asset_id: null, words: [], seconds: 0, versions: [] }),
      ],
    };
    state.history[vid(1)] = [line(3, 'fix_ready', '', { version: 2 }), line(2, 'fix_asked', 'She says North-pack', { action: 'redo', from: 5, to: 5, words: 'Northpeak', text: 'Northpeak', say: 'north-peek' }), line(1, 'made', '', { version: 1 })];
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 }, colorScheme: 'light' });
    page.on('pageerror', e => { throw e; });
    await page.route('**/v1/**', async route => {
      const req = route.request(); const u = new URL(req.url()); const p = u.pathname; const m = req.method();
      state.calls.push(`${m} ${p}${u.search}`);
      const body = m === 'GET' || m === 'DELETE' ? null : JSON.parse(req.postData() || 'null');
      if (m !== 'GET') state.bodies.push([`${m} ${p.replace(`/v1/brands/${brand}`, '')}`, body]);
      const json = (status, b) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(b) });
      const refuse = (status, code, message) => json(status, { error: { code, message, request_id: 'r' } });
      const base = `/v1/brands/${brand}/videos`; const lBase = `/v1/brands/${brand}/creative-lessons`;
      const v = state.videos.find(x => p.startsWith(`${base}/${x.id}`));
      const past = (kind, said = '', data = {}) => { (state.history[v.id] ??= []).unshift(line(10 + state.bodies.length, kind, said, data)); };
      if (p.endsWith('/file')) return route.fulfill({ status: 200, contentType: 'image/png', body: png });
      if (p === `/v1/brands/${brand}/generations/voices`) return state.voicesOff ? refuse(409, 'connector_needed', 'Connect your ElevenLabs account to list its voices.') : json(200, { provider: 'elevenlabs', data: [{ id: 'voice_test_1', name: 'Maya', category: 'premade' }, { id: 'voice_test_2', name: 'Jonas', category: 'premade' }] });
      if (p === `/v1/brands/${brand}/assets`) return json(200, { data: [50, 51].map(n => ({ id: file(n), brand_id: brand, kind: 'other', name: `Desk frame ${n}`, family: 'image', file_path: `/v1/brands/${brand}/assets/${file(n)}/file`, usable: true })) });
      if (p === lBase && m === 'GET') return json(200, { data: state.lessons });
      if (p === lBase && m === 'POST') { const l = { id: `30000000-0000-0000-0000-00000000000${state.lessons.length + 1}`, word: '', say: '', rule: '', applies_to: 'video', source_id: null, created_by: null, created_at: '2026-11-20T11:00:00Z', ...body }; state.lessons.unshift(l); return json(201, l); }
      if (p.startsWith(lBase) && m === 'DELETE') { state.lessons = state.lessons.filter(l => !p.endsWith(l.id)); return route.fulfill({ status: 204 }); }
      if (p === base && m === 'GET') { const fam = u.searchParams.get('family'); return json(200, { data: state.videos.filter(x => !fam || x.id === fam || x.variation_of === fam), storage: true, finishing: state.finishing }); }
      if (p === base && m === 'POST') {
        if (body.name === 'No account') return refuse(409, 'connector_needed', 'A talking video needs your own ElevenLabs account and your own fal.ai account.');
        const made = video(state.videos.length + 1, body.name, { script: body.script, direction: body.direction, settings: body.settings, status: 'working', stage: 'voice', final_asset_id: null, words: [], seconds: 0, versions: [] });
        state.videos.unshift(made); return json(201, made);
      }
      if (!v) return refuse(404, 'not_found', 'nothing here');
      if (p.endsWith('/history')) return json(200, { data: state.history[v.id] ?? [] });
      if (p.endsWith('/fix/keep')) {
        const w = v.versions.find(x => x.state === 'waiting'); v.versions.find(x => x.state === 'current').state = 'before'; w.state = 'current';
        v.final_asset_id = w.final_asset_id; v.words = w.words; v.waiting_version = null;
        if (body?.remember) state.lessons.unshift({ id: '30000000-0000-0000-0000-000000000009', kind: 'say', word: w.edit.text, say: w.edit.say, rule: '', applies_to: 'video', source_type: 'video', source_id: v.id, created_by: null, created_at: '2026-11-20T11:00:00Z' });
        past('fix_kept', '', { version: w.number, remembered: !!body?.remember }); return json(200, v);
      }
      if (p.endsWith('/fix/go-back')) {
        const w = v.versions.find(x => x.state === 'waiting'); Object.assign(w, { state: 'gone_back', reason: body.reason, note: body.note }); v.waiting_version = null;
        past('gone_back', body.note, { version: w.number, reason: body.reason, try_again: !!body.try_again });
        if (body.try_again) { Object.assign(v, { status: 'working', stage: 'edit_voice', round: v.round + 1 }); past('fix_asked', body.note, { action: 'redo', words: 'Northpeak', text: 'Northpeak' }); }
        return json(200, v);
      }
      if (p.endsWith('/restore')) {
        const n = Number(p.split('/').at(-2)); v.versions.find(x => x.state === 'current').state = 'before'; const to = v.versions.find(x => x.number === n); to.state = 'current';
        v.final_asset_id = to.final_asset_id; v.words = to.words; past('version_restored', '', { version: n }); return json(200, v);
      }
      if (p.endsWith('/rating')) { Object.assign(v, { rating: body.rating, rating_reason: body.reason ?? '', rated_at: body.rating ? '2026-11-20T11:00:00Z' : null }); past(body.rating || 'rating_cleared', body.reason ?? ''); return json(200, v); }
      if (p.endsWith('/edits')) {
        if (body.note === 'refuse') return refuse(409, 'conflict', 'A fix to this video waits for your answer: keep it or go back first');
        Object.assign(v, { status: 'working', stage: body.action === 'remove' ? 'edit_cut' : 'edit_voice', round: v.round + 1, edit: body });
        past('fix_asked', body.note, { action: body.action, words: v.words.slice(body.from, body.to + 1).map(w => w.text).join(' '), text: body.text }); return json(200, v);
      }
      if (p.endsWith('/variations')) {
        const made = body.values.map((value, i) => video(state.videos.length + 1 + i, `${v.name} (variation ${v.variations + i + 1})`, { variation_of: v.variation_of ?? v.id, variation_change: { change: body.change, value, from: v.id }, status: 'working', stage: 'voice', final_asset_id: null, words: [], versions: [] }));
        state.videos.unshift(...made); v.variations += made.length; past('variation_made', '', { change: body.change, count: made.length }); return json(201, { data: made });
      }
      if (p.endsWith('/retry')) { Object.assign(v, { status: 'working', error: null, error_code: null }); return json(200, v); }
      if (m === 'PATCH') { v.name = body.name; return json(200, v); }
      if (m === 'DELETE') { state.videos = state.videos.filter(x => x !== v); return route.fulfill({ status: 204 }); }
      return json(200, v);
    });
    await page.goto(`${origin}/tests/fixtures/talking-videos.html${query}`);
    return { page, state };
  };
  const sent = (state, call) => state.bodies.filter(([c]) => c === call).map(([, b]) => b);

  {
    // The list opens on what needs a person, and says what.
    const { page } = await start();
    await page.getByRole('heading', { name: 'Talking videos' }).waitFor();
    await page.getByText('2 things need you.').waitFor();
    await page.getByText('1 fix to check, and 1 new variation to rate.').waitFor();
    assert.equal(await page.getByRole('button', { name: 'Needs you 2' }).getAttribute('aria-pressed'), 'true');
    assert.equal(await page.locator('tbody tr').count(), 2);
    await page.getByText('Variation of Blocked at 2am').waitFor();
    await shot(page, 'talking-videos-list');
    await page.getByRole('button', { name: 'All 5' }).click();
    assert.equal(await page.locator('tbody tr').count(), 5);
    await page.getByText('Step 3 of 5: making the video').waitFor();
    await page.getByText('The video model refused the scene picture.').waitFor();
    await page.getByText('Liked: "The hook is strong"').waitFor();
    await page.getByRole('button', { name: 'Failed 1' }).click();
    assert.equal(await page.locator('tbody tr').count(), 1);
    await page.getByRole('button', { name: 'See why' }).click();
    await page.getByText('It was not made.').waitFor();
    await page.getByRole('button', { name: 'Try again' }).click();
    await page.getByText('Trying again from the step that failed.').waitFor();
    await page.close();
  }

  {
    // A fix to check: no, with why, and try again; then yes, remembered.
    const { page, state } = await start();
    await page.getByRole('button', { name: /Check the fix: Blocked at 2am/ }).click();
    await page.getByRole('heading', { name: 'Your fix is ready. Do you like it?' }).waitFor();
    await page.getByText('You said: "She says North-pack".').waitFor();
    assert.equal(await page.getByLabel('Before the fix').count(), 1);
    assert.equal(await page.getByLabel('After the fix').count(), 1);
    // While it waits nothing else is fixed.
    await page.getByText('Answer the fix above first: keep it or go back.').waitFor();
    assert(await page.getByRole('button', { name: 'Northpeak', exact: true }).isDisabled());
    await shot(page, 'talking-video-fix-to-check');
    await page.getByRole('button', { name: 'No, go back to before' }).click();
    await page.getByText('The fix is kept in the history, not deleted.').waitFor();
    await page.getByRole('button', { name: 'Lips do not match' }).click();
    await page.getByLabel('What is wrong').fill('His lips are a beat late');
    await shot(page, 'talking-video-go-back');
    await page.getByRole('button', { name: 'Go back and try again' }).click();
    await page.getByText('Gone back. Making the fix again with your note.').waitFor();
    assert.deepEqual(sent(state, `POST /videos/${vid(1)}/fix/go-back`), [{ reason: 'Lips do not match', note: 'His lips are a beat late', try_again: true }]);
    await page.getByText('Went back to before the fix: Lips do not match: "His lips are a beat late"').waitFor();
    await page.getByText('Fix you turned down').waitFor();
    await page.close();
  }

  {
    const { page, state } = await start(`?item=${vid(1)}`);
    await page.getByRole('switch', { name: 'Remember this for every new video' }).waitFor();
    await page.getByText('says "Northpeak" as "north-peek"').waitFor();
    await page.getByRole('button', { name: 'Yes, keep the fix' }).click();
    await page.getByText('Kept. The version before is still in the history.').waitFor();
    assert.deepEqual(sent(state, `POST /videos/${vid(1)}/fix/keep`), [{ remember: true }]);
    await page.getByText('Kept the fix (version 2) and remembered how to say it').waitFor();
    assert.equal(await page.getByRole('heading', { name: 'Your fix is ready. Do you like it?' }).count(), 0);

    // Versions: go back to the first, which keeps the second.
    await page.getByRole('button', { name: 'Go back to this version' }).click();
    await page.getByText('The video is version 1 again.').waitFor();
    assert.deepEqual(state.calls.filter(c => c.endsWith('/versions/1/restore')).length, 1);

    // Rename with Save and Cancel.
    await page.getByRole('button', { name: '✎ Rename' }).click();
    await page.getByLabel('Name').fill('Blocked at 2am, desk');
    await page.getByRole('button', { name: 'Save', exact: true }).click();
    await page.getByRole('heading', { name: /Blocked at 2am, desk/ }).waitFor();

    // Not this asks why; the answer can be taken back; then liked.
    await page.getByRole('button', { name: 'Not this' }).click();
    await page.getByLabel('Why not').fill('Too slow to start');
    await page.getByRole('button', { name: 'Save', exact: true }).click();
    await page.getByText('"Too slow to start"').first().waitFor();
    await page.getByRole('button', { name: 'Change my mind' }).click();
    await page.getByRole('button', { name: 'Like', exact: true }).click();
    await page.getByText('Liked.', { exact: true }).waitFor();
    assert.deepEqual(sent(state, `POST /videos/${vid(1)}/rating`), [{ rating: 'not_this', reason: 'Too slow to start' }, { rating: '', reason: '' }, { rating: 'liked', reason: '' }]);

    // Fix one part: pick words, say what is wrong and how it should sound.
    await page.getByRole('button', { name: 'Northpeak', exact: true }).click();
    await page.getByRole('button', { name: 'clean', exact: true }).click();
    await page.getByText('You picked "Northpeak picks a clean"').waitFor();
    await page.getByLabel('How it should sound').fill('north-peek');
    await page.getByLabel('What is wrong, in your own words').fill('The name is said wrong');
    await shot(page, 'talking-video-fix-one-part');
    await page.getByRole('button', { name: 'Fix this part' }).click();
    await page.getByText('Making your fix. You check it before it replaces anything.').waitFor();
    assert.deepEqual(sent(state, `POST /videos/${vid(1)}/edits`), [{ action: 'redo', from: 5, to: 8, note: 'The name is said wrong', say: 'north-peek' }]);
    await page.getByText('Only the part you picked is made again.').waitFor();
    await page.close();
  }

  {
    // Remove words, and change them; a refusal is said.
    const { page, state } = await start(`?item=${vid(4)}`);
    await page.getByRole('button', { name: 'again', exact: true }).click();
    await page.getByRole('button', { name: 'Remove it' }).click();
    await page.getByText('Cutting words out costs nothing.').waitFor();
    await page.getByLabel('What is wrong, in your own words').fill('refuse');
    await page.getByRole('button', { name: 'Fix this part' }).click();
    await page.getByText('A fix to this video waits for your answer').waitFor();
    await page.getByRole('button', { name: 'Change the words' }).click();
    assert(await page.getByRole('button', { name: 'Fix this part' }).isDisabled());
    await page.getByLabel('New words').fill('once more');
    await page.getByLabel('What is wrong, in your own words').fill('');
    await page.getByRole('button', { name: 'Fix this part' }).click();
    assert.deepEqual(sent(state, `POST /videos/${vid(4)}/edits`).at(-1), { action: 'redo', from: 4, to: 4, note: '', text: 'once more' });

    // Delete asks first.
    await page.getByRole('button', { name: 'Delete', exact: true }).click();
    await page.getByRole('button', { name: 'Delete video' }).click();
    await page.getByText('Deleted.').waitFor();
    await page.getByRole('heading', { name: 'Talking videos' }).waitFor();
    assert.equal(state.videos.length, 4);
    await page.close();
  }

  {
    // Variations: rate the family, change one thing, teach a lesson.
    const { page, state } = await start(`?item=${vid(4)}`);
    await page.getByRole('button', { name: 'Make variations' }).click();
    await page.getByRole('heading', { name: 'Variations of "Agency owner, price"' }).waitFor();
    assert(await page.getByRole('button', { name: 'Make variations' }).isDisabled());
    await page.getByLabel('First line 1').fill('Still rotating proxies by hand?');
    await page.getByLabel('First line 2').fill('Your scraper died at 2am. Again.');
    await page.getByText('No logos on clothing').waitFor();
    await shot(page, 'talking-video-variations');
    await page.getByRole('button', { name: 'Make 2 variations' }).click();
    await page.getByText('Making 2 variations.').waitFor();
    assert.deepEqual(sent(state, `POST /videos/${vid(4)}/variations`), [{ change: 'first_line', values: ['Still rotating proxies by hand?', 'Your scraper died at 2am. Again.'] }]);
    await page.getByText('First line: "Your scraper died at 2am. Again."').waitFor();
    // Another thing to change: a voice, picked from the brand's own.
    await page.getByRole('switch', { name: 'Voice' }).click();
    await page.getByRole('button', { name: 'Jonas' }).click();
    await page.getByRole('button', { name: 'Make 1 variation' }).click();
    assert.deepEqual(sent(state, `POST /videos/${vid(4)}/variations`).at(-1), { change: 'voice', values: ['voice_test_2'] });
    // A lesson, added and removed.
    await page.getByLabel('Word', { exact: true }).fill('Northpeak');
    await page.getByLabel('Said as').fill('north-peek');
    await page.getByRole('button', { name: 'Remember this' }).click();
    await page.getByText('"Northpeak" is said "north-peek"').waitFor();
    assert.deepEqual(sent(state, 'POST /creative-lessons'), [{ kind: 'say', word: 'Northpeak', say: 'north-peek', source_type: 'video', source_id: vid(4) }]);
    await page.getByRole('button', { name: 'Remove No logos on clothing' }).click();
    await page.getByText('No logos on clothing').waitFor({ state: 'detached' });
    await page.close();
  }

  {
    // Rating a variation from the family.
    const { page, state } = await start(`?item=${vid(1)}`);
    await page.getByRole('button', { name: 'Yes, keep the fix' }).click();
    await page.getByRole('button', { name: 'Make variations' }).click();
    await page.getByText('First line: "Still rotating proxies by hand?"').waitFor();
    await page.locator('div.rounded-lg', { hasText: 'Still rotating proxies by hand?' }).getByRole('button', { name: 'Like', exact: true }).click();
    await page.waitForFunction(() => !document.body.innerText.includes('Rate this'));
    assert.deepEqual(sent(state, `POST /videos/${vid(2)}/rating`), [{ rating: 'liked', reason: '' }]);
    await page.close();
  }

  {
    // A new video: the models of the last one are filled in.
    const { page, state } = await start('?item=new');
    await page.getByRole('heading', { name: 'New talking video' }).waitFor();
    await page.getByText('Write what is said').waitFor();
    assert.equal(await page.getByLabel('Video model').inputValue(), 'acme/talk/reference-to-video');
    await page.getByLabel('Name').fill('No account');
    await page.getByLabel('Script').fill('Meet Northpeak, the fastest way to ship.');
    await page.getByText('7 words, about 3 seconds.').waitFor();
    await page.getByLabel('Voice', { exact: true }).selectOption('voice_test_2');
    await page.getByRole('button', { name: 'Desk frame 51' }).click();
    await page.getByRole('switch', { name: 'Captions' }).click();
    await shot(page, 'talking-video-new');
    await page.getByRole('button', { name: 'Make video' }).click();
    await page.getByText('A talking video needs your own ElevenLabs account').waitFor();
    await page.getByLabel('Name').fill('Fastest way');
    await page.getByRole('button', { name: 'Make video' }).click();
    await page.getByRole('heading', { name: /Fastest way/ }).waitFor();
    const made = sent(state, 'POST /videos').at(-1);
    assert.equal(made.scene_asset_id, file(51));
    assert.deepEqual(made.settings, { voice: { voice: 'voice_test_2', model: 'voice-model-test' }, words: { model: 'words-model-test' }, video: { model: 'acme/talk/reference-to-video', options: { resolution: '480p' } }, upscale: { model: 'acme/upscale/video' }, finish: { tighten: true, phone_look: true, captions: false } });
    await page.close();
  }

  {
    // Nothing made yet: no models are filled in, and none is a default.
    const { page } = await start('', true);
    await page.getByText('No talking videos yet').waitFor();
    await page.getByRole('button', { name: 'New talking video' }).first().click();
    assert.equal(await page.getByLabel('Video model').inputValue(), '');
    await page.getByText('Outflare has no default models').waitFor();
    await page.close();
  }

  {
    // Someone who can't manage reads everything and changes nothing.
    const { page } = await start(`?readonly&item=${vid(1)}`);
    await page.getByText('Someone who can manage content answers this.').waitFor();
    for (const name of ['Yes, keep the fix', 'Delete', '✎ Rename', 'Like', 'Make variations']) assert.equal(await page.getByRole('button', { name, exact: true }).count(), 0, name);
    await page.getByText('Everything that happened to this video').waitFor();
    await page.close();
  }
  console.log('ok');
} finally { if (browser) await browser.close(); server.kill(); }
