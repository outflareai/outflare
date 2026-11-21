// The Start here page of the Creatives tab against mocked API answers:
// the four steps and what is next for a brand with nothing, a starter
// skill added with one click once the picture model is typed, ideas from
// the brand's personas, an ad described in words and drafted by the
// brand's own AI model, a brand that is done, and someone who can't
// manage. Starts a local Vite server and mocks every API request; needs
// no backend.
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

const provider = (provider, name, connected) => ({ provider, name, connected, integration_id: null });
const providers = (openai, fal, eleven) => ({ storage: true, data: [
  { kind: 'image', ready: openai || fal, providers: [provider('openai', 'OpenAI', openai), provider('fal', 'fal.ai', fal)] },
  { kind: 'video', ready: fal, providers: [provider('fal', 'fal.ai', fal)] },
  { kind: 'voice', ready: eleven, providers: [provider('elevenlabs', 'ElevenLabs', eleven)] },
] });
const persona = (n, name, pains) => ({ id: `90000000-0000-0000-0000-00000000000${n}`, name, pains });

let browser;
try {
  let ready = false;
  for (let n = 0; n < 100; n++) { try { ready = (await fetch(origin)).ok; if (ready) break; } catch {} await new Promise(r => setTimeout(r, 100)); }
  assert(ready, `Vite did not start: ${output}`);
  browser = await chromium.launch({ headless: true, args: ['--no-sandbox'], executablePath: process.env.OUTFLARE_TEST_CHROMIUM || undefined });
  const shot = async (page, name) => { if (process.env.OUTFLARE_TEST_SHOTS) await page.screenshot({ path: `${process.env.OUTFLARE_TEST_SHOTS}/${name}.png`, fullPage: true }); };

  const start = async (state, query = '') => {
    state.posts = [];
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 }, colorScheme: 'light' });
    page.on('pageerror', e => { throw e; });
    await page.addInitScript(() => { window.outflareTestPollMs = 20; });
    await page.route('**/v1/**', async route => {
      const url = new URL(route.request().url()), method = route.request().method();
      const json = (body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
      if (url.pathname.endsWith('/generations/providers')) return json(state.providers);
      if (url.pathname.endsWith('/assets')) return json({ data: state.files });
      if (url.pathname.endsWith('/personas')) return json({ data: state.personas });
      if (url.pathname.endsWith('/creatives')) return json({ data: state.creatives });
      if (url.pathname.endsWith('/creative-skill-drafts')) {
        state.said = route.request().postDataJSON().description; state.reads = 0;
        if (state.noModel) return json({ error: { code: 'connector_needed', message: 'Connect an AI model under Connectors first.', request_id: 'r' } }, 409);
        return json({ id: '60000000-0000-0000-0000-000000000001', description: state.said, status: 'queued', error: '', name: '', summary: '', picture: '', copy: '', created_at: '2026-01-01T00:00:00Z', finished_at: null }, 202);
      }
      if (url.pathname.includes('/creative-skill-drafts/')) {
        const base = { id: '60000000-0000-0000-0000-000000000001', description: state.said, error: '', name: '', summary: '', picture: '', copy: '', created_at: '2026-01-01T00:00:00Z', finished_at: null };
        if (++state.reads < 2) return json({ ...base, status: 'running' });
        if (state.draftFails) return json({ ...base, status: 'failed', error: 'Claude took too long to answer. Try again.' });
        return json({ ...base, status: 'ready', name: 'Slow checkout', summary: 'A shop owner watching a slow checkout.', picture: 'A shop owner watching a spinner on a till. No text in the picture.', copy: 'Lead with {Proof point}.' });
      }
      if (url.pathname.endsWith('/creative-skills') && method === 'POST') {
        const body = route.request().postDataJSON(); state.posts.push(body);
        if (state.refuse) return json({ error: { code: 'invalid', message: 'That model is not one your account has.', request_id: 'r' } }, 400);
        const made = { id: '70000000-0000-0000-0000-000000000009', name: body.name, kind: 'image', current: { model: body.model } };
        state.skills.push(made);
        return json(made, 201);
      }
      if (url.pathname.endsWith('/creative-skills')) return json({ data: state.skills });
      return json({ error: { code: 'not_found', message: 'nothing here', request_id: 'r' } }, 404);
    });
    await page.goto(`${origin}/tests/fixtures/creative-start.html${query}`);
    await page.getByRole('heading', { name: /first ad/ }).waitFor();
    return page;
  };
  const steps = page => page.getByRole('region', { name: 'Steps' }).locator('[data-step]');
  const dones = async page => Promise.all([0, 1, 2, 3].map(i => steps(page).nth(i).getAttribute('data-done')));

  {
    // A brand with nothing: every step is open and the first is next.
    const state = { files: [], providers: providers(false, false, false), skills: [], creatives: [], personas: [] };
    const page = await start(state);
    assert.deepEqual(await dones(page), ['false', 'false', 'false', 'false']);
    assert.match(await page.getByRole('group', { name: 'Progress' }).innerText(), /0 of 4 done[\s\S]*Next: add your brand files/);
    assert.match(await steps(page).nth(0).innerText(), /Do this next[\s\S]*Start with your logo/);
    assert.match(await steps(page).nth(1).innerText(), /Not connected\s*OpenAI · pictures[\s\S]*Not connected\s*fal\.ai · pictures, video[\s\S]*Not connected\s*ElevenLabs · voice/);
    await steps(page).nth(0).getByRole('button', { name: 'Add files' }).click();
    assert.equal(await page.evaluate(() => window.opened), 'brand-kit');
    await steps(page).nth(1).getByRole('button', { name: 'Connect' }).first().click();
    assert.equal(await page.evaluate(() => window.wentToConnectors), true);
    // No personas yet: the page says where ideas come from.
    await steps(page).nth(3).getByRole('button', { name: 'Brand profile' }).click();
    assert.equal(await page.evaluate(() => window.wentToProfile), true);
    // A starter skill can't be added until the model is typed: there is no default.
    const add = page.getByRole('region', { name: 'Starter skills' }).getByRole('button', { name: 'Add this skill' });
    assert.equal(await add.count(), 3);
    assert.equal(await add.first().isDisabled(), true);
    // Its prompts can be read first.
    await page.getByRole('button', { name: 'See the prompts' }).first().click();
    assert.match(await page.getByLabel('Prompts of Problem first').innerText(), /Prompt A: At the desk[\s\S]*\{persona\}[\s\S]*Prompt B[\s\S]*Rules:/);
    // The talking video waits for video and voice.
    await page.getByRole('button', { name: 'Connect video and voice first' }).waitFor();
    await shot(page, 'creative-start-empty');
    await page.close();
  }

  {
    // Files and a picture account are there: picking a skill is next, and one click adds it.
    const state = { files: [{ id: 'a' }, { id: 'b' }], providers: providers(true, false, false), skills: [], creatives: [], personas: [persona(1, 'Site owner', ['slow checkout']), persona(2, 'Agency owner', [])] };
    const page = await start(state);
    assert.deepEqual(await dones(page), ['true', 'true', 'false', 'false']);
    assert.match(await page.getByRole('group', { name: 'Progress' }).innerText(), /2 of 4 done[\s\S]*Next: pick a starter skill/);
    assert.match(await steps(page).nth(0).innerText(), /2 files are in the Brand kit/);
    // Ideas come from personas that have a pain.
    assert.match(await steps(page).nth(3).innerText(), /Ideas from your Brand profile:\s*Site owner · slow checkout/);
    assert.doesNotMatch(await steps(page).nth(3).innerText(), /Agency owner/);
    await page.getByLabel('Picture model').fill('test-image-model');
    // A refusal is shown and nothing is opened.
    state.refuse = true;
    await page.getByRole('button', { name: 'Add this skill' }).nth(1).click();
    await page.getByText('That model is not one your account has.').waitFor();
    assert.equal(await page.evaluate(() => window.opened), undefined);
    state.refuse = false;
    await page.getByRole('button', { name: 'Add this skill' }).nth(1).click();
    await page.getByText('One number is added. Nothing was made or paid for.').waitFor();
    const body = state.posts.at(-1);
    assert.equal(body.name, 'One number'); assert.equal(body.kind, 'image'); assert.equal(body.model, 'test-image-model');
    assert.deepEqual(body.sizes, ['1:1', '4:5']); assert.equal(body.variants.length, 2);
    assert.deepEqual(body.variants.map(v => v.share), [50, 50]);
    assert.match(body.rules, /never like stock or AI art/);
    // It opens the new skill, where a creative is made.
    assert.equal(await page.evaluate(() => window.opened), 'creative-skills/70000000-0000-0000-0000-000000000009');
    await shot(page, 'creative-start-skill');
    await page.close();
  }

  {
    // Say what you want: the brand's own AI model drafts the skill, the
    // person reads and changes it, and only Save makes a skill.
    const state = { files: [{ id: 'a' }], providers: providers(true, false, false), skills: [], creatives: [], personas: [persona(1, 'Site owner', ['slow checkout'])], noModel: true };
    const page = await start(state);
    const say = page.getByRole('region', { name: 'Say what you want' });
    const write = say.getByRole('button', { name: 'Write the prompt' });
    assert.equal(await write.isDisabled(), true);
    // An idea from the Brand profile fills the box.
    await say.getByRole('button', { name: 'Site owner · slow checkout' }).click();
    assert.match(await say.getByLabel('The ad you want').inputValue(), /Site owner.*slow checkout/);
    // No AI model connected: it says so and points at Connectors.
    await write.click();
    await say.getByRole('alert').getByText('Connect an AI model under Connectors first.').waitFor();
    await say.getByRole('button', { name: 'Open Connectors' }).click();
    assert.equal(await page.evaluate(() => window.wentToConnectors), true);
    // A draft that fails says why.
    state.noModel = false; state.draftFails = true;
    await write.click();
    await say.getByRole('alert').getByText('Claude took too long to answer. Try again.').waitFor();
    state.draftFails = false;
    await write.click();
    const draft = say.getByLabel('Draft skill');
    await draft.waitFor();
    assert.match(state.said, /slow checkout/);
    assert.equal(await draft.getByLabel('Skill name').inputValue(), 'Slow checkout');
    assert.match(await draft.getByLabel('Picture prompt').inputValue(), /spinner on a till/);
    // Nothing is saved without the picture model, and nothing was posted yet.
    assert.equal(await draft.getByRole('button', { name: 'Save as a skill' }).isDisabled(), true);
    assert.equal(state.posts.length, 0);
    await page.getByLabel('Picture model').fill('test-image-model');
    await draft.getByLabel('Words over the picture').fill('Lead with {Proof point}. Button: Try it.');
    await shot(page, 'creative-start-say');
    await draft.getByRole('button', { name: 'Save as a skill' }).click();
    await page.getByText('Slow checkout is saved as a skill. Nothing was made or paid for.').waitFor();
    const body = state.posts.at(-1);
    assert.equal(body.name, 'Slow checkout'); assert.equal(body.model, 'test-image-model'); assert.equal(body.variants.length, 1);
    assert.equal(body.variants[0].copy, 'Lead with {Proof point}. Button: Try it.');
    assert.equal(await page.evaluate(() => window.opened), 'creative-skills/70000000-0000-0000-0000-000000000009');
    await page.close();
  }

  {
    // A brand that has made and approved one: all done, the model it uses is offered, and a skill it has is marked.
    const state = { files: [{ id: 'a' }], providers: providers(true, true, true), skills: [{ id: 's1', name: 'Problem first', kind: 'image', current: { model: 'their-image-model' } }], creatives: [{ id: 'c1', status: 'approved' }, { id: 'c2', status: 'needs_review' }], personas: [] };
    const page = await start(state);
    await page.getByRole('heading', { name: 'You have made your first ad' }).waitFor();
    assert.deepEqual(await dones(page), ['true', 'true', 'true', 'true']);
    assert.match(await page.getByRole('group', { name: 'Progress' }).innerText(), /^4 of 4 done$/);
    assert.match(await steps(page).nth(3).innerText(), /2 made, 1 approved/);
    assert.equal(await page.getByLabel('Picture model').inputValue(), 'their-image-model');
    const starters = page.getByRole('region', { name: 'Starter skills' });
    assert.equal(await starters.getByRole('button', { name: 'Add this skill' }).count(), 2);
    await starters.getByText('Added', { exact: true }).waitFor();
    await starters.getByRole('button', { name: 'New talking video' }).click();
    assert.equal(await page.evaluate(() => window.opened), 'talking-videos/new');
    await page.close();
  }

  {
    // Someone who can't manage sees where things stand and changes nothing.
    const state = { files: [], providers: providers(false, false, false), skills: [], creatives: [], personas: [] };
    const page = await start(state, '?readonly');
    for (const name of ['Add this skill', 'Connect', 'Start from a blank skill']) assert.equal(await page.getByRole('button', { name, exact: true }).count(), 0, name);
    assert.equal(await page.getByLabel('Picture model').count(), 0);
    await page.close();
  }
  console.log('ok');
} finally {
  if (browser) await browser.close();
  server.kill();
}
