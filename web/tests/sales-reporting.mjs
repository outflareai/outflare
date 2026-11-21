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
const replyAt = '2026-11-14T15:42:00Z';
const sentAt = '2026-11-15T09:12:00Z';
const replyBody = 'Hi Maya,\n\n\nCould you share pricing for our team? We would like to discuss a trial.';
const attribution = { id: 'worker:example', name: 'Example discovery agent', kind: 'worker', status: 'known' };
const member = { user_id: 'u1', name: 'Maya Example', email: 'maya@example.com', role: 'manager', connected: false };
const lead = {
  id: '30000000-0000-4000-8000-000000000001', record_id: 'r1',
  name: 'Ana Example', company: 'Northpeak Example', email: 'ana@northpeak.example',
  state: 'new', reply_state: 'reply_received', version: 1,
  owner_id: null, unread: true, viewed_by: [], feedback_count: 0,
  source: 'lead_portal', source_id: 'source-example', created_at: t0,
  updated_at: t0, last_activity_at: t0, attribution,
  website: 'https://northpeak.example',
  latest_message: { body: 'Here are the plan details.', occurred_at: sentAt, direction: 'outgoing' },
  latest_reply: { body: replyBody, occurred_at: replyAt, direction: 'incoming' },
};
let mode = 'missing';
// Outflare's own discovery lanes (the lead finder, an Outflare Cloud feature).
const finder = `/v1/brands/${brand}/cloud/lead-finder`;
const finderStages = ['checking', 'not_a_fit', 'to_review', 'adding', 'finding_people', 'ready', 'in_crm', 'in_campaign', 'stopped'];
function finderLanes(query) {
  const stages = { checking: 3, not_a_fit: 4, to_review: 2, adding: 0, finding_people: 1, ready: 1, in_crm: 0, in_campaign: 2, stopped: 1 };
  const lane = {
    run_id: '81000000-0000-4000-8000-000000000001', profile_id: '82000000-0000-4000-8000-000000000001', profile_name: 'Finance teams',
    run_status: 'running', standing: true, run_error_code: null, source: 'proposal:api_token:example', source_name: 'Proposed by an API token',
    lane: 'Example payroll review sites', agents: ['scout-1'], proposed: 18, already_known: 4, companies: 14, stages, fit: 7,
    people: 5, emails: 4, in_campaign: 2, stops: { rejected: 4, no_email: 1 }, waits: { finder_busy: 1 }, first_at: t0, last_at: t0,
  };
  return { days: Number(query.get('days')), generated_at: t0, stages: finderStages, lanes: [lane],
    totals: { proposed: 18, already_known: 4, companies: 14, stages, fit: 7, people: 5, emails: 4, in_campaign: 2, stops: lane.stops } };
}
// The lane registry of one profile (the Lane leaderboard).
const profileId = '82000000-0000-4000-8000-000000000001';
const boardPath = `${finder}/profiles/${profileId}/lanes`;
const cost = (agent, outflare, passed) => ({ agent_cents: agent, agent_requests: 120, agent_unpriced: 0, outflare_cents: outflare, total_cents: agent + outflare, per_pass_cents: passed ? (agent + outflare) / passed : null });
function registryLane(n, status, extra = {}) {
  return {
    id: `83000000-0000-4000-8000-00000000000${n}`, profile_id: profileId, name: `Example lane ${n}`, title: `Example lane ${n}`, hypothesis: '', kind: 'signal', signal: 'hiring',
    segment: 'EU', recipe: { queries: [`example query ${n}`] }, origin: 'initial', parent_id: null, status, priority: 0, why: null, learned: null, est_size: null,
    claimed_by: null, lease_expires_at: null, probed_at: null, low_batches: 0, next_revisit_at: null, score_pending: false, waiting_for: 0, created_by: 'scout-1',
    created_at: t0, updated_at: t0, last_activity_at: t0, found: 0, new: 0, known: 0, repeat: 0, known_by: {}, repeat_by: {}, companies: 0, checking: 0,
    screened: 0, passed: 0, errors: 0, known_later: 0, stopped_at_intake: {}, grades: {}, new_rate: null, pass_rate: null, cost: cost(0, 0, 0), ...extra,
  };
}
const registry = [
  registryLane(1, 'productive', { title: 'Example payroll review sites', kind: 'list', signal: null, recipe: { urls: ['https://reviews.example/payroll'], queries: ['payroll software reviews'] },
    why: 'Probe: 30 of 50 new, 9 of 28 passed (32%). Scaling.', learned: 'Example lesson: review sites list vendors that sell to finance teams.', est_size: 400,
    claimed_by: 'scout-1', lease_expires_at: '2099-01-01T00:00:00Z', found: 50, new: 30, known: 15, repeat: 5, known_by: { in_people_list: 10, seen_by_lead_finder: 5 },
    repeat_by: { other_lane: 5 }, companies: 30, checking: 2, screened: 28, passed: 9, errors: 1, known_later: 2, stopped_at_intake: { customer: 2 },
    grades: { A: 3, B: 6, C: 4, D: 2 }, new_rate: 0.56, pass_rate: 0.32, cost: cost(38.5, 21, 9) }),
  registryLane(2, 'proposed', { title: 'Example data teams hiring scrapers', origin: 'learned', parent_id: '83000000-0000-4000-8000-000000000001' }),
  registryLane(3, 'low_yield', { title: 'Example coupon directories', why: 'Only 4 of 50 companies were new; the rest Outflare already had.', found: 50, new: 4, known: 46, new_rate: 0.08 }),
];
function laneBoard() {
  const by = st => registry.filter(l => l.status === st).length;
  const add = k => registry.reduce((n, l) => n + l[k], 0);
  return {
    profile_id: profileId, generated_at: t0,
    settings: { probe_size: 50, batch_size: 200, precheck_size: 20, min_pass_rate: 0.15, min_new_rate: 0.3, exploration_share: 0.3, review_mode: 'human_review', lease_seconds: 900 },
    totals: { lanes: registry.length, proposed: by('proposed'), probing: by('probing'), scaling: by('productive'), dead: by('low_yield'), exhausted: by('cooling_down'), paused: by('paused'), rejected: by('rejected'),
      found: add('found'), new: add('new'), known: add('known'), repeat: add('repeat'), companies: add('companies'), screened: add('screened'), passed: add('passed'), errors: add('errors'),
      known_later: add('known_later'), agent_cents: 38.5, agent_requests: 120, outflare_cents: 21, total_cents: 59.5 },
    lanes: registry,
    notes: [{ id: '84000000-0000-4000-8000-000000000001', lane_id: registry[2].id, lane: registry[2].title, author: 'system: score', kind: 'dead_end', text: 'Example note: coupon sites are already known.', created_at: t0 }],
    notes_text: 'RICH (scaling now; build on these, do not duplicate)\n- [list, EU] Example payroll review sites: 9/28 passed, 30/50 new.',
    covered_text: 'The workspace already holds 1200 companies.',
  };
}
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
    discovery: mode === 'connected' ? {
      missions: [{ id: '40000000-0000-4000-8000-000000000001', name: 'Example mission', state: 'running', budget_cents: 5000, spent_cents: 1250 }],
      totals: { found: 400, new: 250, passed: 80, graded_ab: 50, sent: 40 },
      buckets: [{ at: from, counts: { found: 400, new: 250, passed: 80, graded_ab: 50, sent: 40 } }],
      lanes: { total: 12, by_status: { productive: 3, low_yield: 9 }, rows: [{ title: 'Example price-tracking tools', method: 'keyword', status: 'productive', found: 200, new: 100, passed: 40 }] },
      spend: { window_cents: 800, rows: [{ tool: 'Example search', units: 30, cents: 800 }, { tool: 'Own scraper', units: 90, cents: null }] },
    } : null,
    results: { amounts: [{ currency: 'USD', amount_minor: 120000, valued: 1, unvalued: 1 }] },
    agents: [{ attribution, counts, intake: { submitted: 150, accepted: 120, duplicates: 20, blocked: 8, held: 2 } }],
    attribution_coverage: { native_milestones: 7, known: 7, unavailable: 0 },
  };
}
function answer(method, u, body) {
  const p = u.pathname;
  if (p === '/v1/setup') return { needed: false };
  if (p === '/v1/meta') return { name: 'Outflare', version: 'test', capabilities: ['lead_finder'], login_methods: ['password'] };
  if (p === `${finder}/lanes`) return finderLanes(u.searchParams);
  if (p === `/v1/brands/${brand}/ideal-customer-profiles`) return { data: [{ id: profileId, name: 'Finance teams', status: 'active' }] };
  if (p === `${boardPath}/board`) return laneBoard();
  if (method === 'PATCH' && p.startsWith(`${boardPath}/`)) {
    const l = registry.find(x => x.id === p.split('/').at(-1));
    if (body.action === 'approve') l.status = 'probing';
    if (body.priority !== undefined) l.priority = body.priority;
    return l;
  }
  if (p === `${finder}/lanes/companies` && u.searchParams.get('lane_id')) return {
    total: 1, next_cursor: null,
    data: [{ domain: 'payrollco.example', name: 'Payroll Example', source_url: 'https://reviews.example/payroll', evidence: 'Listed among payroll tools.', agent: 'scout-1',
      stage: 'stopped', code: 'customer', lane_id: u.searchParams.get('lane_id'), candidate_id: null, verdict: 'accept', score: 88, grade: 'B', grade_detail: null,
      reasons: ['Example reason: sells to finance teams'], record_id: null, arrived_at: t0, people: [] }],
  };
  if (p === `${finder}/lanes/companies`) return {
    total: 1, next_cursor: null,
    data: [{ domain: 'payrollco.example', name: 'Payroll Example', source_url: 'https://reviews.example/payroll', evidence: 'Listed among payroll tools.',
      agent: 'scout-1', stage: 'to_review', code: null, candidate_id: '80000000-0000-4000-8000-000000000001', verdict: 'accept', score: 88,
      record_id: null, arrived_at: t0, people: [{ id: '90000000-0000-4000-8000-000000000001', first_name: 'Dana', last_name: 'Example', title: 'CFO',
        has_email: true, status: 'added', stop_code: null, record_id: '90000000-0000-4000-8000-000000000002' }] }],
  };
  if (p === '/v1/me') return {
    user: { id: member.user_id, name: member.name, email: member.email, email_verified: true },
    workspaces: [{ id: ws, slug: 'example', name: 'Example Sales', role: 'owner', scopes: ['workspace.manage', 'customers.read', 'customers.manage', 'members.manage'] }],
  };
  if (p === `/v1/workspaces/${ws}/brands`) return { data: [{ id: brand, name: 'Northpeak', handle: 'northpeak', status: 'active' }] };
  if (p === `/v1/workspaces/${ws}/stats`) return { unread_replies: 0, actions_waiting: 0 };
  if (p === `${root}/reporting/team`) {
    const people = [
      { user_id: '10000000-0000-4000-8000-000000000011', name: 'Alex Example', role: 'sdr', active: true, counts: { lead_seen: 7, lead_assigned: 2, lead_taken: 3, lead_archived: 1, handoff_completed: 2 } },
      { user_id: '10000000-0000-4000-8000-000000000012', name: 'Sam Example', role: 'ae', active: true, counts: { lead_seen: 4, send_accepted: 3, customer_confirmed: 1 } },
    ];
    const events = people.map((m, i) => ({ id: `activity-${i}`, lead_id: lead.id, record_id: lead.record_id, name: lead.name, company: lead.company, actor_id: m.user_id, actor_name: m.name, actor_role: m.role, action: i ? 'customer_confirmed' : 'lead_seen', occurred_at: u.searchParams.get('from') }));
    const data = events.filter(e => (!u.searchParams.get('user_id') || e.actor_id === u.searchParams.get('user_id')) && (!u.searchParams.get('action') || e.action === u.searchParams.get('action')) && (!u.searchParams.get('search') || e.company.toLowerCase().includes(u.searchParams.get('search').toLowerCase())));
    return { from: u.searchParams.get('from'), to: u.searchParams.get('to'), members: people, data: u.searchParams.has('cursor') ? [] : data, next_cursor: u.searchParams.has('cursor') || !data.length ? null : 'fixture-next' };
  }
  if (p === `${root}/reporting/lanes`) {
    if (u.searchParams.get('trace') === 'true') return { source: {status:'connected'}, lanes:null, trace: {
      scope:'latest_20_runs', total:1,offset:0,
      summary:{calls:2,errors:1,recorded_cents:2,unknown_cost_calls:1},
      runs:[{id:'60000000-0000-4000-8000-000000000001',mission_id:'40000000-0000-4000-8000-000000000001',status:'completed',started_at:t0,finished_at:t0,last_event_at:t0}],
      tools:[{provider:'example',tool:'search',unit_kind:'request',units:2,calls:2,errors:1,empty:0,duration_ms:500,recorded_cents:2,unknown_cost_calls:1}],
      lanes:[{lane_id:'50000000-0000-4000-8000-000000000001',title:'Example price-tracking tools',calls:2,errors:1,recorded_cents:2,unknown_cost_calls:1,companies:1,screened:1,passed:1,graded_ab:1,sent:1}],
      rows:[{id:'70000000-0000-4000-8000-000000000001',run_id:'60000000-0000-4000-8000-000000000001',lane_id:'50000000-0000-4000-8000-000000000001',lane_title:'Example price-tracking tools',company:null,grade:null,qualification:null,phase:'collect',agent:'example-agent',kind:'tool',provider:'example',tool:'search',status:'ok',started_at:t0,duration_ms:500,attempt:2,cost_cents:null,input:{query:'retail stock intelligence'},output:{http_status:200,results:3}}],
    }};
    const status = u.searchParams.get('status'), offset = Number(u.searchParams.get('offset') ?? 0);
    const lane = (n, state) => ({
      id: `50000000-0000-4000-8000-${String(n).padStart(12, '0')}`, title: n === 1 ? 'Example price-tracking tools' : `Example lane ${n}`,
      method: 'keyword', origin: 'initial', status: state, segment: 'Example price tools', idea: 'Example trackers poll shops every day',
      why: null, verdict: 'Example verdict: scaling', learned: n === 1 ? 'Example lesson: trackers pass, coupon sites fail' : null,
      recipe: { queries: ['example price tracker', 'example stock monitor'], urls: [], seeds: ['northpeak.example'], describe: [], jobs: [] },
      found: 200, new: 100, known: 100, screened: 90, passed: 40, ai_cents: 12.5, expected_size: 300, last_activity_at: state === 'proposed' ? null : t0,
    });
    const all = status === 'proposed' ? [] : Array.from({ length: status ? 3 : 60 }, (_, i) => lane(i + 1, i < 3 ? 'productive' : 'low_yield'));
    return { source: { status: 'connected', connections: [] }, lanes: { total: all.length, offset, rows: all.slice(offset, offset + 50) } };
  }
  if (p === `${root}/me`) return member;
  if (p === `${root}/team`) return { data: [member] };
  if (p === `${root}/templates`) return { data: [] };
  if (p === `${root}/leads`) return { data: [lead], new_unseen: true, unread: true, next_cursor: null };
  if (p === `${root}/leads/${lead.id}`) return { lead, messages: [], events: [], handoffs: [], draft: null };
  if (p === `${root}/leads/${lead.id}/actions` && body?.action === 'read') {
    lead.unread = false;
    lead.viewed_by = [{ user_id: member.user_id, seen_at: t0 }];
    return lead;
  }
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
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 }, colorScheme: 'dark', locale: 'en-GB', timezoneId: 'Europe/Paris' });
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
  // The compact queue keeps workflow controls; original uploader remains in full details.
  await page.goto(`${origin}/example/northpeak/crm`);
  await page.getByRole('heading', { name: 'All conversations', exact: true }).waitFor();
  await page.getByText('Here are the plan details.', { exact: true }).waitFor();
  const queueRow = page.getByRole('row').filter({ has: page.getByRole('button', { name: lead.company }) });
  assert.equal(await queueRow.locator('time').getAttribute('datetime'), sentAt, 'latest message time must not be the lead update time');
  assert.match(await queueRow.locator('time').innerText(), /^Sent .*10:12/, 'message time uses the viewer timezone');
  await page.getByRole('button', { name: /New conversations/ }).click();
  await page.getByRole('heading', { name: 'New conversations', exact: true }).waitFor();
  const reply = queueRow.locator('blockquote');
  await page.getByText(replyBody.replace(/\s+/g, ' '), { exact: true }).waitFor();
  assert.equal(await queueRow.locator('time').getAttribute('datetime'), replyAt, 'new conversations show when the prospect replied, not when the lead was imported');
  assert.match(await queueRow.locator('time').innerText(), /^Replied .*16:42/);
  assert.equal(await reply.getAttribute('title'), replyBody, 'the preview retains the original message on hover');
  assert(await reply.evaluate(el => {
    const text = el.firstChild;
    const start = text.textContent.indexOf('Could you share pricing');
    const range = document.createRange();
    range.setStart(text, start);
    range.setEnd(text, start + 'Could you share pricing'.length);
    const box = el.getBoundingClientRect();
    return [...range.getClientRects()].every(rect => rect.top >= box.top && rect.bottom <= box.bottom + 1);
  }), 'the actual reply must be visible inside the clamped preview after a greeting and blank lines');
  await page.getByRole('button', { name: 'All conversations', exact: true }).click();
  await page.getByRole('heading', { name: 'All conversations', exact: true }).waitFor();
  await queueRow.getByRole('combobox', { name: `Assigned to for ${lead.name}`, exact: true }).waitFor();
  await queueRow.getByRole('button', { name: 'Feedback', exact: true }).waitFor();
  await queueRow.getByRole('button', { name: 'Archive', exact: true }).waitFor();
  assert.equal(await queueRow.getByRole('button', { name: 'Open conversation', exact: true }).count(), 0);
  await queueRow.getByRole('button', { name: 'Mark seen', exact: true }).click();
  await queueRow.getByRole('button', { name: 'Seen by you', exact: true }).waitFor();
  await queueRow.getByText(member.name, { exact: true }).waitFor();
  assert.equal(lead.owner_id, null, 'marking seen must not assign the conversation');
  await queueRow.getByRole('button', { name: lead.company }).click();
  await page.getByRole('heading', { name: lead.company, exact: true }).waitFor();
  await page.getByText('Lead details', { exact: true }).click();
  await page.getByText(attribution.name, { exact: true }).waitFor();
  await page.getByRole('button', { name: 'Back to all conversations', exact: true }).click();
  await page.getByRole('heading', { name: 'All conversations', exact: true }).waitFor();
  await page.getByRole('button', { name: 'Reporting', exact: true }).click();
  await page.waitForURL('**/sales-reporting');
  await page.getByText('Outflare data only', { exact: true }).waitFor();
  const milestones = page.locator('.sr-stages');
  assert(await milestones.getByRole('button', { name: /Discovered/ }).isDisabled());
  assert.match(await milestones.getByRole('button', { name: /Discovered/ }).innerText(), /—/);
  assert.match(await milestones.getByRole('button', { name: /SDR picked up/ }).innerText(), /5/);
  assert.match(await milestones.getByRole('button', { name: /Confirmed customers/ }).innerText(), /2/);
  if (shots) await page.screenshot({ path: join(shots, 'reporting-live-missing.png'), fullPage: true });

  // Native team reporting is available independently of the portal, and filters never write.
  await page.clock.setFixedTime(new Date('2026-11-17T12:00:00Z'));
  await page.getByRole('button', { name: 'Team activity', exact: true }).click();
  await page.getByRole('heading', { name: 'Team activity', exact: true }).waitFor();
  const sdrActivity = page.getByRole('region', { name: 'SDR activity', exact: true });
  await sdrActivity.getByRole('button', { name: 'Alex Example', exact: true }).waitFor();
  assert.match(await sdrActivity.innerText(), /7/);
  assert.match(await page.getByRole('region', { name: 'Account executive activity', exact: true }).innerText(), /Sam Example/);
  const teamCall = () => calls.filter(c => c.path === `${root}/reporting/team`).at(-1);
  assert.equal(teamCall().query.get('from'), '2026-11-16T23:00:00.000Z', 'Today starts at viewer local midnight');
  assert.equal(teamCall().query.get('to'), '2026-11-17T12:00:00.000Z', 'Today includes the current partial hour');
  const log = page.getByRole('region', { name: 'Activity log', exact: true });
  await sdrActivity.getByRole('button', { name: 'Alex Example', exact: true }).click();
  await page.waitForFunction(() => document.querySelector('[aria-label="Activity teammate"]').value.endsWith('11'));
  await log.getByRole('combobox', { name: 'Activity action' }).selectOption('lead_seen');
  await log.getByRole('searchbox', { name: 'Activity person or company' }).fill('Northpeak');
  await page.waitForResponse(r => r.url().includes('/reporting/team?') && new URL(r.url()).searchParams.get('search') === 'Northpeak');
  assert.equal(teamCall().query.get('user_id'), '10000000-0000-4000-8000-000000000011');
  assert.equal(teamCall().query.get('action'), 'lead_seen');
  assert.equal(await log.getByRole('listitem').count(), 1);
  await log.getByRole('button', { name: 'Older actions', exact: true }).click();
  await log.getByText('No matching activity', { exact: true }).waitFor();
  await log.getByRole('button', { name: 'Newest actions', exact: true }).click();
  await log.getByRole('button', { name: `${lead.company} · ${lead.name}`, exact: true }).waitFor();
  await page.getByRole('combobox', { name: 'Team activity period' }).selectOption('yesterday');
  await log.getByRole('button', { name: `${lead.company} · ${lead.name}`, exact: true }).waitFor();
  assert.equal(teamCall().query.get('from'), '2026-11-15T23:00:00.000Z');
  assert.equal(teamCall().query.get('to'), '2026-11-16T23:00:00.000Z');
  await page.setViewportSize({ width: 390, height: 844 });
  assert(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1), 'team report must not overflow the page on mobile');
  if (shots) await page.screenshot({ path: join(shots, 'sales-team-activity-mobile.png'), fullPage: true });
  await page.setViewportSize({ width: 1440, height: 1000 });
  if (shots) await page.screenshot({ path: join(shots, 'sales-team-activity.png'), fullPage: true });
  await log.getByRole('button', { name: `${lead.company} · ${lead.name}`, exact: true }).click();
  await page.getByRole('heading', { name: lead.company, exact: true }).waitFor();
  await page.getByRole('button', { name: 'Back to team activity', exact: true }).click();
  await page.getByRole('heading', { name: 'Team activity', exact: true }).waitFor();
  await page.getByRole('button', { name: 'Reporting', exact: true }).click();
  await page.waitForURL('**/sales-reporting');
  await page.getByText('Outflare data only', { exact: true }).waitFor();

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
  // Discovery: the portal's counts, lanes and spend; an uncosted tool stays unknown, never zero.
  await page.getByRole('button', { name: 'Discovery', exact: true }).click();
  // The Lane leaderboard of the profile comes first: yield, lanes by state, what each taught, and the shared record.
  await page.getByRole('heading', { name: 'Lane leaderboard', exact: true }).waitFor();
  // The heading shows before the profiles and the board load: wait for a lane before reading the tiles.
  await page.getByRole('button', { name: 'Example payroll review sites', exact: true }).first().waitFor();
  // The same lane is also a row of the live lane flow, which can show first: wait for the board itself.
  const leaderboardTiles = page.locator('section', { has: page.getByRole('heading', { name: 'Lane leaderboard', exact: true }) }).locator('.sr-summary-grid');
  const boardTiles = /Found[\s\S]*100[\s\S]*Passed[\s\S]*9[\s\S]*32% of screened[\s\S]*Cost per passed company[\s\S]*\$0\.066/;
  for (let i = 0; i < 300 && !(calls.some(c => c.path === `${boardPath}/board`) && boardTiles.test(await leaderboardTiles.innerText().catch(() => ''))); i++) await new Promise(r => setTimeout(r, 100));
  assert.ok(calls.some(c => c.path === `${boardPath}/board`));
  assert.match(await leaderboardTiles.innerText(), boardTiles);
  const scaling = page.getByRole('row').filter({ has: page.getByRole('button', { name: 'Example payroll review sites', exact: true }) }).first();
  assert.match(await scaling.innerText(), /List · EU[\s\S]*Scaling[\s\S]*scout-1 working[\s\S]*50[\s\S]*30 \(56%\)[\s\S]*9 \/ 28 \(32%\)[\s\S]*~400[\s\S]*\$0\.066[\s\S]*Scaling\.[\s\S]*Example lesson/);
  await scaling.getByRole('button', { name: 'Show recipe', exact: true }).click();
  await scaling.getByText('Lists: https://reviews.example/payroll', { exact: true }).waitFor();
  const laneStates = page.getByRole('group', { name: 'Leaderboard lane state', exact: true });
  const lanesTable = page.locator('section', { has: page.getByRole('heading', { name: 'Lanes', exact: true }) });
  await laneStates.getByRole('button', { name: 'Dead · 1', exact: true }).click();
  await lanesTable.getByRole('button', { name: 'Example coupon directories', exact: true }).waitFor();
  assert.equal(await lanesTable.getByRole('button', { name: 'Example data teams hiring scrapers', exact: true }).count(), 0);
  await laneStates.getByRole('button', { name: 'All · 3', exact: true }).click();
  const record = page.locator('section', { has: page.getByRole('heading', { name: 'Shared record', exact: true }) });
  await record.getByText('Example note: coupon sites are already known.', { exact: true }).waitFor();
  await page.getByRole('heading', { name: 'By way of finding', exact: true }).waitFor();
  // A lane opens as its own page, with its controls, counters and companies; it fits a phone.
  await page.getByRole('button', { name: 'Example data teams hiring scrapers', exact: true }).click();
  await page.getByRole('heading', { name: 'Example data teams hiring scrapers', exact: true }).waitFor();
  assert.equal(await page.getByRole('heading', { name: 'Lane flow, live', exact: true }).count(), 0, 'a lane page replaces the board');
  await page.getByRole('button', { name: 'Example payroll review sites', exact: true }).waitFor();
  await page.setViewportSize({ width: 390, height: 844 });
  if (shots) await page.screenshot({ path: join(shots, 'reporting-board-lane-mobile.png'), fullPage: true });
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth), false, 'a leaderboard lane page must fit mobile');
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.getByRole('button', { name: 'Approve', exact: true }).click();
  await page.getByText('Probing', { exact: true }).first().waitFor();
  assert.deepEqual(calls.filter(c => c.method === 'PATCH').at(-1).body, { action: 'approve' });
  await page.getByRole('button', { name: 'Raise priority', exact: true }).click();
  await page.getByText('Priority 1', { exact: true }).waitFor();
  assert.deepEqual(calls.filter(c => c.method === 'PATCH').at(-1).body, { priority: 1 });
  const laneCompanies = page.locator('section', { has: page.getByRole('heading', { name: 'Companies this lane sent', exact: true }) });
  assert.match(await laneCompanies.getByRole('row', { name: /Payroll Example/ }).innerText(), /Stopped[\s\S]*B[\s\S]*Example reason[\s\S]*Already a customer/);
  assert.equal(calls.filter(c => c.path === `${finder}/lanes/companies`).at(-1).query.get('lane_id'), registry[1].id);
  await page.getByRole('button', { name: 'Example payroll review sites', exact: true }).click();
  await page.getByRole('heading', { name: 'Example payroll review sites', exact: true }).waitFor();
  await page.getByText('Already in People · 10', { exact: true }).waitFor();
  await page.getByText('Another lane found it first · 5', { exact: true }).waitFor();
  if (shots) await page.screenshot({ path: join(shots, 'reporting-board-lane.png'), fullPage: true });
  await page.getByRole('button', { name: 'All lanes', exact: true }).click();
  await page.getByRole('heading', { name: 'Lane leaderboard', exact: true }).waitFor();
  if (shots) await page.screenshot({ path: join(shots, 'reporting-lane-leaderboard.png'), fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth), false, 'the leaderboard must fit mobile');
  await page.setViewportSize({ width: 1440, height: 1000 });
  // Outflare's own lanes, live: what each lane sent and where its companies are.
  await page.getByRole('heading', { name: 'Lane flow, live', exact: true }).waitFor();
  const laneRow = page.locator('section', { has: page.getByRole('heading', { name: 'Lane flow, live', exact: true }) }).getByRole('row').filter({ has: page.getByRole('button', { name: 'Example payroll review sites', exact: true }) });
  assert.match(await laneRow.innerText(), /scout-1[\s\S]*Finance teams[\s\S]*open[\s\S]*18[\s\S]*14[\s\S]*3[\s\S]*7/);
  assert.ok(calls.some(c => c.path === `${finder}/lanes` && c.query.get('days')));
  await page.getByText('Not a fit for the profile · 4', { exact: true }).waitFor();
  // A lane opens as its own page: its stages, why companies stopped, and its companies with their people.
  await laneRow.getByRole('button', { name: 'Example payroll review sites', exact: true }).click();
  await page.getByRole('button', { name: 'All lanes', exact: true }).waitFor();
  await page.getByRole('heading', { name: 'Every company in this lane', exact: true }).waitFor();
  await page.getByRole('button', { name: 'Dana Example', exact: true }).waitFor();
  assert.equal(await page.getByRole('heading', { name: 'Company discovery', exact: true }).count(), 0, 'a lane page replaces the board');
  await page.getByRole('button', { name: /Waiting for review/ }).click();
  await page.getByRole('heading', { name: 'Waiting for review', exact: true }).waitFor();
  const listed = calls.filter(c => c.path === `${finder}/lanes/companies`).at(-1);
  assert.deepEqual([listed.query.get('lane'), listed.query.get('source'), listed.query.get('stage')], ['Example payroll review sites', 'proposal:api_token:example', 'to_review']);
  if (shots) await page.screenshot({ path: join(shots, 'reporting-finder-lane.png'), fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth), false, 'a lane page must fit mobile');
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.getByRole('button', { name: 'All lanes', exact: true }).click();
  await page.getByRole('heading', { name: 'Company discovery', exact: true }).waitFor();
  const discovery = page.locator('section', { has: page.getByRole('heading', { name: 'Company discovery', exact: true }) }).locator('.sr-summary-grid');
  assert.match(await discovery.innerText(), /400[\s\S]*250 not already held \(63%\)[\s\S]*80[\s\S]*50[\s\S]*40[\s\S]*\$0\.20 recorded spend each[\s\S]*\$8\.00/);
  await page.getByRole('cell', { name: 'Example mission', exact: true }).waitFor();
  await page.getByRole('cell', { name: '$37.50', exact: true }).waitFor();
  const history = page.getByRole('region', { name: 'Discovery execution history' }).first();
  await history.getByRole('button', { name: 'collect · example / search' }).click();
  await history.getByText('retail stock intelligence', { exact: false }).waitFor();
  assert.match(await history.innerText(), /2 calls.*1 failed attempts.*1 calls without a price/s);
  assert.match(await history.innerText(), /Unknown/);
  await history.getByRole('combobox').selectOption('60000000-0000-4000-8000-000000000001');
  await history.getByRole('button', { name: 'collect · example / search' }).waitFor();
  assert.equal(calls.filter(c=>c.query.get('trace')==='true').at(-1).query.get('run'),'60000000-0000-4000-8000-000000000001');
  if (shots) await page.screenshot({ path: join(shots,'reporting-execution-history.png'), fullPage:true });
  await page.setViewportSize({width:390,height:844});
  if(shots)await page.screenshot({path:join(shots,'reporting-execution-mobile.png'),fullPage:true});
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth > window.innerWidth),false,'execution history must fit mobile');
  await page.setViewportSize({width:1440,height:1000});
  // The lane board: open a lane for its recipe and lessons, filter by state, page through.
  await page.getByRole('heading', { name: 'Search lanes', exact: true }).waitFor();
  await page.getByText('12 ways of finding companies', { exact: false }).waitFor();
  await page.getByRole('button', { name: 'Example price-tracking tools', exact: true }).click();
  await page.getByText('Example lesson: trackers pass, coupon sites fail', { exact: false }).waitFor();
  await page.getByText('example stock monitor', { exact: true }).waitFor();
  await page.getByText('northpeak.example', { exact: true }).waitFor();
  await page.getByText('100 already held', { exact: false }).waitFor();
  if (shots) await page.screenshot({ path: join(shots, 'reporting-live-lanes.png'), fullPage: true });
  await page.getByText('1–50 of 60', { exact: true }).waitFor();
  await page.getByRole('button', { name: 'Next', exact: true }).click();
  await page.getByText('51–60 of 60', { exact: true }).waitFor();
  assert.equal(calls.filter(c => c.path === `${root}/reporting/lanes`).at(-1).query.get('offset'), '50');
  const states = page.getByRole('group', { name: 'Lane state', exact: true });
  await states.getByRole('button', { name: /Productive · 3/ }).click();
  await page.getByRole('button', { name: 'Example lane 3', exact: true }).waitFor();
  const filtered = calls.filter(c => c.path === `${root}/reporting/lanes`).at(-1);
  assert.deepEqual([filtered.query.get('status'), filtered.query.get('offset')], ['productive', '0']);
  assert.equal(await page.getByRole('button', { name: 'Next', exact: true }).count(), 0);
  const ownTool = page.getByRole('row', { name: /Own scraper/ });
  assert.match(await ownTool.innerText(), /90[\s\S]*—/);
  if (shots) await page.screenshot({ path: join(shots, 'reporting-live-discovery.png'), fullPage: true });
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
  // Queue navigation and opening a conversation only record views; nothing sends or enrols.
  assert(calls.filter(c => c.method !== 'GET').every(c => c.body?.action === 'read' || (c.method === 'POST' && c.path === `${root}/queue-seen`) || (c.method === 'PATCH' && c.path.startsWith(`${boardPath}/`))));
  assert.deepEqual(errors, []);
  console.log('sales reporting: queue, live data, explicit demo, comparisons, intervals, mobile and conversation navigation passed');
} finally {
  await browser?.close();
  server.kill();
}
