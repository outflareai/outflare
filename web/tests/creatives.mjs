// The Creatives library and the Skills and prompts pages against mocked
// API answers: the list and gallery (remembered), filters and search,
// approving one and many, one creative's page (rename, sizes, words,
// what it is filed under, reject with a reason); the skills list with
// its On/Off switch, a new skill, a skill's prompts and settings saved
// as a new version, a split test, going back a version, making a
// creative, archive, and someone who can't manage. Starts a local Vite
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
const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==', 'base64');
const skillId = n => `70000000-0000-0000-0000-00000000000${n}`;
const fillIns = (...t) => [...new Set(t.join(' ').match(/\{[a-z ]+\}/g)?.map(s => s.slice(1, -1).replace(/ /g, '_')) ?? [])];
const prompt = (key, name, picture, copy, share) => ({ key, name, picture, copy, share, fill_ins: fillIns(picture, copy) });
const version = (n, note, variants, extra = {}) => ({ version: n, note, provider: 'openai', model: 'gpt-image-1', options: {}, sizes: ['1:1', '4:5'], rules: 'No extra text in the picture.', variants, created_by: null, created_at: '2026-11-12T09:00:00Z', creatives: 0, ...extra });
const skill = (n, name, kind, versions, extra = {}) => ({ id: skillId(n), brand_id: brand, name, description: '', kind, enabled: true, creatives: 0, archived_at: null, created_at: '2026-11-01T09:00:00Z', updated_at: '2026-11-12T09:00:00Z', versions, ...extra });
const skillOut = k => { const { versions, ...rest } = k; const current = versions.at(-1); return { ...rest, version: current.version, current, split_test: current.variants.filter(v => v.share > 0).length > 1 }; };
const creative = (n, name, status, extra = {}) => ({ id: `80000000-0000-0000-0000-00000000000${n}`, brand_id: brand, code: `CR-000${n}`, name, kind: 'image', status, skill_id: skillId(1), skill_name: 'Static ad', skill_version: 2, variant: 'A', persona: 'Scraping engineer', pain: 'Blocked requests', hook: 'A question', tags: ['q4'], sizes: ['1:1', '4:5'],
  headline: 'Blocked again?', subline: 'Get through every time', cta: 'Start free', prompt: 'Scene: a scraping engineer stuck on blocked requests.', generation_id: null, provider: 'openai', model: 'gpt-image-1', error_code: null, error: null, asset_id: status === 'making' || status === 'failed' ? null : `90000000-0000-0000-0000-00000000000${n}`,
  review_reason: '', reviewed_by: null, reviewed_at: null, rating: '', rating_reason: '', rated_at: null, version: 1, created_by: null, created_at: '2026-11-18T09:00:00Z', updated_at: '2026-11-18T09:00:00Z', ...extra });

let browser;
try {
  let ready = false;
  for (let n = 0; n < 100; n++) { try { ready = (await fetch(origin)).ok; if (ready) break; } catch {} await new Promise(r => setTimeout(r, 100)); }
  assert(ready, `Vite did not start: ${output}`);
  browser = await chromium.launch({ headless: true, args: ['--no-sandbox'], executablePath: process.env.OUTFLARE_TEST_CHROMIUM || undefined });
  const shot = async (page, name) => { if (process.env.OUTFLARE_TEST_SHOTS) await page.screenshot({ path: `${process.env.OUTFLARE_TEST_SHOTS}/${name}.png`, fullPage: true }); };

  const start = async (query = '') => {
    const state = {
      creatives: [
        creative(1, 'Blocked again', 'needs_review'),
        creative(2, 'One number', 'needs_review', { variant: 'B', persona: 'Agency owner', pain: 'Slow reports', tags: [] }),
        creative(3, 'Proof first', 'approved', { reviewed_at: '2026-11-18T10:00:00Z' }),
        creative(4, 'Too glossy', 'rejected', { review_reason: 'Looks made by AI', reviewed_at: '2026-11-18T10:00:00Z' }),
        creative(5, 'No picture', 'failed', { error: 'The AI account refused the prompt.', error_code: 'provider_refused' }),
      ],
      skills: [
        skill(1, 'Static ad', 'image', [
          version(1, '', [prompt('A', 'Problem first', 'Scene: a {persona} stuck on {pain}.', 'Headline: name {pain} as a question.', 100)], { creatives: 3 }),
          version(2, 'Added the rule about text', [prompt('A', 'Problem first', 'Scene: a {persona} stuck on {pain}, showing {proof}.', 'Headline: name {pain} as a question.', 100)], { creatives: 2 }),
        ], { description: 'One headline over one picture', creatives: 5 }),
        skill(2, 'Animated static', 'video', [version(1, '', [prompt('A', 'Slow zoom', 'Bring the picture to life.', '', 50), prompt('B', 'Parallax', 'Move the layers apart.', '', 50)], { provider: 'fal', model: 'fal-ai/kling-video/v2/master/image-to-video', sizes: ['9:16'] })], { enabled: false }),
      ],
      calls: [], bodies: [], history: {},
    };
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 }, colorScheme: 'light' });
    page.on('pageerror', e => { throw e; });
    await page.route('**/v1/**', async route => {
      const req = route.request(); const u = new URL(req.url()); const p = u.pathname; const m = req.method();
      state.calls.push(`${m} ${p}${u.search}`);
      const body = m === 'GET' || m === 'DELETE' ? null : JSON.parse(req.postData() || 'null');
      if (m !== 'GET') state.bodies.push([`${m} ${p}`, body]);
      const json = (status, b) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(b) });
      const refuse = (status, code, message) => json(status, { error: { code, message, request_id: 'r' } });
      const cBase = `/v1/brands/${brand}/creatives`; const sBase = `/v1/brands/${brand}/creative-skills`;
      const c = state.creatives.find(x => p.startsWith(`${cBase}/${x.id}`));
      const k = state.skills.find(x => p.startsWith(`${sBase}/${x.id}`));
      if (p === cBase && m === 'GET') return json(200, { data: state.creatives });
      if (p === cBase && m === 'POST') {
        const made = state.skills.find(x => x.id === body.skill_id);
        if (body.persona === 'Nobody') return refuse(409, 'connector_needed', 'Connect an OpenAI account to make pictures.');
        const n = state.creatives.length + 1;
        const a = creative(n, `${body.persona}, ${body.pain}`, 'making', { skill_id: made.id, skill_name: made.name, skill_version: made.versions.at(-1).version, variant: body.variant || 'A', persona: body.persona, pain: body.pain, hook: body.hook, headline: body.headline, subline: body.subline, cta: body.cta });
        state.creatives.unshift(a); made.creatives++; return json(201, a);
      }
      if (c && p.endsWith('/ad')) return route.fulfill({ status: 200, contentType: 'image/png', body: png });
      if (c && p.endsWith('/history')) return json(200, { data: (state.history[c.id] ?? []).concat([{ id: `50000000-0000-0000-0000-00000000000${c.code.at(-1)}`, kind: 'made', said: '', data: {}, actor_id: '40000000-0000-0000-0000-000000000001', actor_name: 'Ana', created_at: '2026-11-18T09:00:00Z' }]) });
      if (c && p.endsWith('/rating')) { Object.assign(c, { rating: body.rating, rating_reason: body.reason ?? '' }); (state.history[c.id] ??= []).unshift({ id: `51000000-0000-0000-0000-0000000000${String(state.bodies.length).padStart(2, '0')}`, kind: body.rating || 'rating_cleared', said: body.reason ?? '', data: {}, actor_id: '40000000-0000-0000-0000-000000000001', actor_name: 'Ana', created_at: '2026-11-19T09:00:00Z' }); return json(200, c); }
      if (p === `/v1/brands/${brand}/creative-lessons` && m === 'POST') return json(201, { id: '30000000-0000-0000-0000-000000000001', word: '', say: '', created_by: null, created_at: '2026-11-19T09:00:00Z', ...body });
      if (c && p.endsWith('/approve')) { c.status = 'approved'; c.reviewed_at = '2026-11-19T00:00:00Z'; c.version++; return json(200, c); }
      if (c && p.endsWith('/reject')) { c.status = 'rejected'; c.review_reason = body?.reason ?? ''; c.reviewed_at = '2026-11-19T00:00:00Z'; c.version++; return json(200, c); }
      if (c && m === 'PATCH') {
        if (body.version !== c.version) return refuse(409, 'conflict', 'The creative changed since you opened it.');
        for (const f of ['name', 'persona', 'pain', 'hook', 'tags', 'headline', 'subline', 'cta']) if (body[f] !== undefined) c[f] = body[f];
        c.version++; return json(200, c);
      }
      if (p === sBase && m === 'GET') return json(200, { data: state.skills.filter(x => !!x.archived_at === (u.searchParams.get('archived') === 'true')).map(skillOut) });
      if (p === sBase && m === 'POST') {
        if (state.skills.some(x => x.name.toLowerCase() === body.name.toLowerCase())) return refuse(409, 'conflict', 'A skill with that name already exists.');
        const made = skill(state.skills.length + 1, body.name, body.kind, [version(1, '', body.variants.map((v, i) => prompt('ABCD'[i], v.name, v.picture, v.copy, v.share)), { provider: body.provider, model: body.model, sizes: body.sizes, rules: body.rules })], { description: body.description });
        state.skills.push(made); return json(201, skillOut(made));
      }
      if (k && p.endsWith('/versions')) return json(200, { data: [...k.versions].reverse() });
      if (k && m === 'PATCH') {
        const cur = k.versions.at(-1);
        if (body.version !== undefined && body.version !== cur.version) return refuse(409, 'conflict', 'The skill changed since you opened it.');
        for (const f of ['name', 'description', 'enabled']) if (body[f] !== undefined) k[f] = body[f];
        if (body.archived !== undefined) k.archived_at = body.archived ? '2026-11-19T00:00:00Z' : null;
        if (body.restore_version) { const old = k.versions.find(v => v.version === body.restore_version); k.versions.push({ ...old, version: cur.version + 1, note: `Went back to v${old.version}`, creatives: 0 }); }
        else if (body.variants) k.versions.push(version(cur.version + 1, body.note ?? '', body.variants.map((v, i) => prompt('ABCD'[i], v.name, v.picture, v.copy, v.share)), { provider: body.provider, model: body.model, sizes: body.sizes, rules: body.rules }));
        return json(200, skillOut(k));
      }
      return refuse(404, 'not_found', 'nothing here');
    });
    await page.goto(`${origin}/tests/fixtures/creatives.html${query}`);
    return { page, state };
  };

  {
    // The library: every creative with who it is for, what made it and where it stands.
    const { page, state } = await start();
    await page.getByRole('heading', { name: '5 creatives, 2 waiting for you' }).waitFor();
    const rows = page.locator('tbody tr');
    assert.equal(await rows.count(), 5);
    assert.match(await rows.nth(0).innerText(), /Blocked again[\s\S]*For Scraping engineer · Blocked requests[\s\S]*Made by Static ad · v2 · prompt A · gpt-image-1[\s\S]*CR-0001[\s\S]*1:1[\s\S]*4:5[\s\S]*Waiting for approval[\s\S]*Approve[\s\S]*Reject/);
    assert.match(await rows.nth(3).innerText(), /Too glossy[\s\S]*Rejected[\s\S]*Looks made by AI/);
    assert.match(await rows.nth(4).innerText(), /No picture was made[\s\S]*Not made[\s\S]*The AI account refused the prompt\./);
    // The picture is the finished ad, small.
    assert(state.calls.some(c => c.includes('/ad?width=240&size=1%3A1&v=1')), state.calls.join('\n'));
    await shot(page, 'creatives-list');

    // Filters and search.
    await page.getByRole('button', { name: /^Approved 1$/ }).click();
    assert.equal(await rows.count(), 1);
    await page.getByRole('button', { name: /^All 5$/ }).click();
    await page.getByRole('searchbox', { name: 'Search creatives' }).fill('agency');
    assert.equal(await rows.count(), 1);
    await page.getByRole('searchbox', { name: 'Search creatives' }).fill('nothing like this');
    await page.getByRole('button', { name: 'Show all creatives' }).click();
    assert.equal(await rows.count(), 5);

    // The gallery, remembered for next time.
    await page.getByRole('button', { name: /Gallery/ }).click();
    await page.locator('article').first().waitFor();
    assert.equal(await page.locator('article').count(), 5);
    assert.equal(await page.evaluate(() => localStorage.getItem('outflare.creatives.view')), 'gallery');
    await shot(page, 'creatives-gallery');
    await page.getByRole('button', { name: /List/ }).click();

    // One creative: rename with Save and Cancel.
    await page.getByRole('button', { name: 'Blocked again', exact: true }).click();
    await page.getByRole('heading', { name: /Blocked again/ }).waitFor();
    await page.getByRole('button', { name: '✎ Rename' }).click();
    await page.getByLabel('Creative name').fill('Thrown away');
    await page.getByRole('button', { name: 'Cancel' }).click();
    await page.getByRole('button', { name: '✎ Rename' }).click();
    await page.getByLabel('Creative name').fill('Blocked, as a question');
    await page.getByRole('button', { name: 'Save' }).click();
    await page.getByText('Renamed.').waitFor();
    assert.deepEqual(state.bodies.at(-1)[1], { name: 'Blocked, as a question', version: 1 });
    await page.getByRole('heading', { name: /Blocked, as a question/ }).waitFor();

    // Another size is the same picture laid out again.
    await page.getByRole('group', { name: 'Size' }).getByRole('button', { name: '4:5' }).click();
    await page.getByRole('button', { name: 'Download 4:5' }).waitFor();
    assert(state.calls.some(c => c.includes('/ad?width=1080&size=4%3A5&v=2')), state.calls.join('\n'));

    // The words change without a new picture.
    await page.getByRole('button', { name: 'Edit' }).first().click();
    await page.getByLabel('Headline').fill('Blocked again? Not here.');
    await page.getByLabel('Button').fill('Try it');
    await shot(page, 'creative-editing-words');
    await page.getByRole('button', { name: 'Save' }).click();
    await page.getByText('Words saved. No new picture was made.').waitFor();
    assert.deepEqual(state.bodies.at(-1)[1], { headline: 'Blocked again? Not here.', subline: 'Get through every time', cta: 'Try it', version: 2 });
    assert(!state.calls.some(c => c === `POST /v1/brands/${brand}/creatives`));

    // What it is filed under: tags are tidied.
    await page.getByRole('button', { name: 'Edit' }).last().click();
    await page.getByLabel('Tags').fill('Q4, Launch, q4');
    await page.getByRole('button', { name: 'Save' }).click();
    await page.getByText('Saved.', { exact: true }).waitFor();
    assert.deepEqual(state.bodies.at(-1)[1].tags, ['q4', 'launch']);
    await page.getByText('Scene: a scraping engineer stuck on blocked requests.').waitFor();

    // Reject asks why; the reason stays with the creative.
    await page.getByRole('button', { name: 'Reject' }).click();
    await page.getByLabel('Why are you rejecting it?').fill('Too dark');
    await page.getByRole('button', { name: 'Reject' }).last().click();
    await page.getByText('Rejected.', { exact: true }).waitFor();
    assert.deepEqual(state.bodies.at(-1), [`POST /v1/brands/${brand}/creatives/80000000-0000-0000-0000-000000000001/reject`, { reason: 'Too dark' }]);
    await page.getByText('Too dark').waitFor();
    // A rejected one can still be approved.
    await page.getByRole('button', { name: 'Approve' }).click();
    await page.getByText('Approved.', { exact: true }).waitFor();
    await shot(page, 'creative-approved');

    // Like it, change your mind, then say not this with why, kept as a rule for every new picture.
    const id = '80000000-0000-0000-0000-000000000001';
    await page.getByRole('button', { name: 'Like', exact: true }).click();
    await page.getByText('Liked.', { exact: true }).waitFor();
    assert.deepEqual(state.bodies.at(-1), [`POST /v1/brands/${brand}/creatives/${id}/rating`, { rating: 'liked', reason: '' }]);
    await page.getByText('Liked it').waitFor();
    await page.getByRole('button', { name: 'Change my mind' }).click();
    await page.getByText('Taken back.', { exact: true }).waitFor();
    await page.getByRole('button', { name: 'Not this' }).click();
    await page.getByLabel('Why not').fill('The hands look wrong');
    await page.getByLabel('Remember this as a rule for every new picture').check();
    await page.getByRole('button', { name: 'Save' }).click();
    await page.getByText('Marked not this, and remembered as a rule for every new one.', { exact: true }).waitFor();
    assert(state.bodies.some(([c, b]) => c === `POST /v1/brands/${brand}/creative-lessons` && b.kind === 'rule' && b.rule === 'The hands look wrong' && b.applies_to === 'image' && b.source_id === id));
    assert(state.bodies.some(([c, b]) => c === `POST /v1/brands/${brand}/creatives/${id}/rating` && b.rating === 'not_this' && b.reason === 'The hands look wrong'));
    await page.getByText('Said not this: "The hands look wrong"').waitFor();
    await page.getByText('Made it').waitFor();
    await shot(page, 'creative-not-this');
    await page.getByRole('button', { name: '← Creatives' }).click();
    await page.getByRole('heading', { name: '5 creatives, 1 waiting for you' }).waitFor();
    await page.close();
  }

  {
    // Many at once: pick the ones waiting and answer them together.
    const { page, state } = await start();
    await page.getByLabel('Select all waiting for approval').check();
    await page.getByRole('button', { name: 'Approve 2' }).click();
    await page.getByText('2 creatives approved.').waitFor();
    assert.equal(state.calls.filter(c => c.endsWith('/approve')).length, 2);
    await page.getByRole('heading', { name: '5 creatives', exact: true }).waitFor();
    await page.close();
  }

  {
    // Someone who can't manage reads everything and changes nothing.
    const { page } = await start('?readonly');
    await page.getByRole('heading', { name: /5 creatives/ }).waitFor();
    for (const name of ['Approve', 'Reject']) assert.equal(await page.getByRole('button', { name, exact: true }).count(), 0, name);
    await page.getByRole('button', { name: 'Blocked again', exact: true }).click();
    for (const name of ['Approve', 'Reject', '✎ Rename', 'Edit']) assert.equal(await page.getByRole('button', { name, exact: true }).count(), 0, name);
    await page.getByRole('button', { name: 'Download 1:1' }).waitFor();
    await page.close();
  }

  {
    // Skills and prompts: the list with its model, split test and switch.
    const { page, state } = await start('?page=skills');
    await page.getByRole('heading', { name: '2 skills, 1 split test running' }).waitFor();
    const rows = page.locator('tbody tr');
    assert.match(await rows.nth(0).innerText(), /Static ad v2[\s\S]*One headline over one picture[\s\S]*Image[\s\S]*gpt-image-1[\s\S]*OpenAI[\s\S]*No test running[\s\S]*5/);
    assert.match(await rows.nth(1).innerText(), /Animated static v1[\s\S]*Video[\s\S]*fal\.ai[\s\S]*A against B/);
    await shot(page, 'skills-list');
    await page.getByRole('switch', { name: 'Animated static on or off' }).click();
    await page.getByText('Animated static is on.').waitFor();
    assert.deepEqual(state.bodies.at(-1)[1], { enabled: true });

    // A new skill needs a name, a model and a prompt; saving makes nothing.
    await page.getByRole('button', { name: 'New skill' }).click();
    const save = page.getByRole('button', { name: 'Save skill' });
    assert(await save.isDisabled());
    await page.getByLabel('Skill name').fill('Comparison table');
    await page.getByLabel('AI model').fill('gpt-image-1');
    await page.getByText('Every prompt needs its Picture filled in.').waitFor();
    await page.getByLabel('Prompt A picture').fill('A table: {brand} against {competitor}.');
    await page.getByText('{competitor}', { exact: true }).waitFor();
    await page.getByRole('button', { name: '✓ 4:5' }).click();
    await page.getByRole('button', { name: '16:9' }).click();
    await shot(page, 'skill-new');
    await save.click();
    await page.getByText('Comparison table is saved. Nothing was made or paid for.').waitFor();
    assert.deepEqual(state.bodies.at(-1)[1], { name: 'Comparison table', description: '', kind: 'image', provider: '', model: 'gpt-image-1', sizes: ['1:1', '16:9'], rules: '', variants: [{ name: '', picture: 'A table: {brand} against {competitor}.', copy: '', share: 100 }] });
    await page.getByRole('heading', { name: /Comparison table/ }).waitFor();
    await page.getByRole('button', { name: '← Skills and prompts' }).click();
    await page.getByRole('heading', { name: '3 skills, 1 split test running' }).waitFor();

    // One skill: nothing to save until something changes.
    await page.getByRole('button', { name: 'Static ad', exact: true }).click();
    await page.getByRole('heading', { name: /Static ad/ }).waitFor();
    assert.equal(await page.getByRole('button', { name: 'Save as v3' }).count(), 0);
    const versions = page.locator('section', { hasText: 'Every saved change' }).locator('tbody tr');
    await versions.first().waitFor();
    assert.equal(await versions.count(), 2);
    assert.match(await versions.nth(0).innerText(), /v2\s*current[\s\S]*Added the rule about text/);
    await page.getByRole('button', { name: 'Connect an account' }).click();
    assert(await page.evaluate(() => window.wentToConnectors));

    // Cancel throws a change away.
    await page.getByLabel('Rules').fill('Never show a person.');
    await page.getByRole('button', { name: 'Save as v3' }).waitFor();
    await page.getByRole('button', { name: 'Cancel' }).click();
    assert.equal(await page.getByLabel('Rules').inputValue(), 'No extra text in the picture.');

    // A second prompt starts a split test; saving makes a new version.
    await page.getByRole('button', { name: 'Add prompt B' }).click();
    await page.getByLabel('Prompt B name').fill('One number');
    await page.getByLabel('Prompt B picture').fill('One picture that shows {proof} at a glance.');
    assert.equal(await page.getByLabel('Prompt B share').inputValue(), '50');
    await page.getByText('Prompts: a split test is running').waitFor();
    // A creative can't be made from changes that aren't saved.
    assert(await page.getByRole('button', { name: 'Make a creative' }).isDisabled());
    await page.getByLabel('What changed and why').fill('Testing a number against the problem');
    await shot(page, 'skill-editing');
    await page.getByRole('button', { name: 'Save as v3' }).click();
    await page.getByText('Saved as v3.').waitFor();
    const saved = state.bodies.at(-1)[1];
    assert.equal(saved.version, 2); assert.equal(saved.note, 'Testing a number against the problem');
    assert.deepEqual(saved.variants.map(v => [v.name, v.share]), [['Problem first', 50], ['One number', 50]]);
    await versions.nth(2).waitFor();
    assert.equal(await page.getByRole('button', { name: 'Save as v4' }).count(), 0);

    // Make a creative: who it is for, the words and the prompt's own fill-ins.
    await page.getByRole('button', { name: 'Make a creative' }).click();
    const make = page.getByRole('button', { name: 'Make 1 creative' });
    assert(await make.isDisabled());
    await page.getByLabel('Persona').fill('Nobody');
    await page.getByLabel('Pain').fill('Blocked requests');
    await page.getByLabel('Fill-in proof').fill('99.9% of requests succeed');
    await page.getByLabel('Headline', { exact: true }).fill('Blocked again?');
    await make.click();
    // No account connected: the reason is shown and the form stays.
    await page.getByText('Connect an OpenAI account to make pictures.').waitFor();
    await page.getByLabel('Persona').fill('Scraping engineer');
    await shot(page, 'skill-make-creative');
    await make.click();
    await page.getByText('CR-0006 is being made. It will wait in Creatives for your approval.').waitFor();
    assert.deepEqual(state.bodies.at(-1)[1], { skill_id: skillId(1), persona: 'Scraping engineer', pain: 'Blocked requests', hook: '', headline: 'Blocked again?', subline: '', cta: '', fill_ins: { proof: '99.9% of requests succeed' } });
    // It opens in the library, still being made.
    await page.getByRole('heading', { name: /Scraping engineer, Blocked requests/ }).waitFor();
    await page.getByText('Being made…').waitFor();
    await page.close();
  }

  {
    // Going back a version saves it again as a new one; archive asks first.
    const { page, state } = await start('?page=skills');
    await page.getByRole('button', { name: 'Static ad', exact: true }).click();
    await page.getByRole('button', { name: 'Go back to v1' }).click();
    await page.getByText('Its prompts and settings are saved again as v3').waitFor();
    await page.getByRole('button', { name: 'Go back to v1' }).last().click();
    await page.getByText('v1 is saved again as v3.').waitFor();
    assert.deepEqual(state.bodies.at(-1)[1], { restore_version: 1, version: 2 });
    await page.getByRole('button', { name: '✎ Rename' }).click();
    await page.getByLabel('Skill name').fill('Static picture ad');
    await page.getByRole('button', { name: 'Save', exact: true }).click();
    await page.getByRole('heading', { name: /Static picture ad/ }).waitFor();
    await page.getByRole('button', { name: 'Archive' }).click();
    await page.getByRole('button', { name: 'Archive' }).last().click();
    await page.getByText('Static picture ad is archived.').waitFor();
    await page.getByRole('heading', { name: /1 skill, 1 split test running/ }).waitFor();
    await page.getByLabel('Show archived').check();
    await page.getByRole('button', { name: 'Restore' }).click();
    await page.getByText('Static picture ad is back.').waitFor();
    await page.close();
  }

  {
    // Someone who can't manage reads the skills and changes nothing.
    const { page } = await start('?page=skills&readonly');
    await page.getByRole('heading', { name: /2 skills/ }).waitFor();
    assert.equal(await page.getByRole('button', { name: 'New skill' }).count(), 0);
    assert(await page.getByRole('switch', { name: 'Static ad on or off' }).isDisabled());
    await page.getByRole('button', { name: 'Open' }).first().click();
    assert(await page.getByLabel('Rules').isDisabled());
    for (const name of ['Archive', 'Make a creative', 'Add prompt B', 'Go back to v1', '✎ Rename']) assert.equal(await page.getByRole('button', { name }).count(), 0, name);
    await page.close();
  }
  console.log('ok');
} finally { if (browser) await browser.close(); server.kill(); }
