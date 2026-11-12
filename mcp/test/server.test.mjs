// Runs against the built server (pnpm build first) with a fake API behind
// the SDK's fetch, and a real MCP client over an in-memory transport.
import assert from "node:assert/strict";
import { test } from "node:test";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { InMemoryTransport } from "@modelcontextprotocol/sdk/inMemory.js";
import { createClient } from "@outflare/sdk";
import { createServer } from "../dist/server.js";
import { CORE, MAX_CORE } from "../dist/toolbox.js";

const WS = "11111111-1111-4111-8111-111111111111";
const SEQ = "22222222-2222-4222-8222-222222222222";
const BRAND = "33333333-3333-4333-8333-333333333333";

async function connect(api, opts = {}) {
  const calls = [];
  const fetch = async (req) => {
    const body = req.method === "GET" ? undefined : await req.text();
    calls.push({ method: req.method, url: new URL(req.url), body, headers: req.headers });
    const [status, json] = api(req, body);
    return new Response(json === undefined ? null : JSON.stringify(json), { status, headers: { "Content-Type": "application/json", "X-Request-Id": "req_1" } });
  };
  const server = createServer({ client: createClient({ baseUrl: "http://gtm.test", token: "oft_x", fetch }), workspace: WS, tools: "all", ...opts });
  const client = new Client({ name: "test", version: "0" });
  const [a, b] = InMemoryTransport.createLinkedPair();
  await Promise.all([server.connect(a), client.connect(b)]);
  return { client, calls };
}

test("lists read and write tools, and only read tools when read-only", async () => {
  const { client } = await connect(() => [200, {}], { tools: "all" });
  const { tools } = await client.listTools();
  const names = tools.map((t) => t.name);
  assert.ok(names.includes("get_overview") && names.includes("enroll_leads") && names.includes("reply_to_inbox_message"));
  assert.ok(names.includes("list_supplier_mailboxes") && names.includes("import_supplier_mailboxes") && names.includes("update_supplier_sync"));
  assert.equal(tools.find((t) => t.name === "list_inbox").annotations.readOnlyHint, true);
  assert.equal(tools.find((t) => t.name === "reply_to_inbox_message").annotations.openWorldHint, true);

  const ro = await connect(() => [200, {}], { readOnly: true, tools: "all" });
  const roTools = (await ro.client.listTools()).tools;
  assert.ok(roTools.length > 0);
  assert.ok(roTools.every((t) => t.annotations.readOnlyHint), roTools.map((t) => t.name).join());
});

test("calls the API with the workspace, token and arguments", async () => {
  const { client, calls } = await connect(() => [200, { enrolled: 2, already_enrolled: 0, suppressed: 1, active_elsewhere: 0 }]);
  const lead = "33333333-3333-4333-8333-333333333333";
  const res = await client.callTool({ name: "enroll_leads", arguments: { brand: BRAND, sequence_id: SEQ, lead_ids: [lead] } });
  assert.equal(res.isError, undefined);
  assert.equal(JSON.parse(res.content[0].text).enrolled, 2);
  assert.equal(calls[0].method, "POST");
  assert.equal(calls[0].url.pathname, `/v1/brands/${BRAND}/sequences/${SEQ}/enrollments`);
  assert.equal(calls[0].headers.get("authorization"), "Bearer oft_x");
  assert.deepEqual(JSON.parse(calls[0].body), { lead_ids: [lead] });

  await client.callTool({ name: "list_inbox", arguments: { brand: BRAND, unread: true, limit: 5 } });
  assert.equal(calls[1].url.search, "?unread=true&limit=5");
});

test("imports CSV as text/csv with the list name", async () => {
  const { client, calls } = await connect(() => [200, { created: 1, updated: 0, duplicates: 0, suppressed: 0, errors: [], list_id: null }]);
  const csv = "email,first_name\nana@acme-com.example,Ana\n";
  const res = await client.callTool({ name: "import_leads_csv", arguments: { brand: BRAND, csv, list: "Q4 founders" } });
  assert.equal(res.isError, undefined);
  assert.equal(calls[0].headers.get("content-type"), "text/csv");
  assert.equal(calls[0].url.searchParams.get("list"), "Q4 founders");
  assert.equal(calls[0].body, csv);
});

test("returns API errors as tool errors the model can read", async () => {
  const { client } = await connect(() => [409, { error: { code: "conflict", message: "the recipient is suppressed", request_id: "req_9" } }]);
  const res = await client.callTool({ name: "reply_to_inbox_message", arguments: { brand: BRAND, message_id: SEQ, body: "Thanks!" } });
  assert.equal(res.isError, true);
  assert.equal(res.content[0].text, "409 conflict: the recipient is suppressed (request req_9)\nNext step: Read the item again and retry with its current state.");
  assert.deepEqual(res.structuredContent.error, {
    status: 409, code: "conflict", param: null, message: "the recipient is suppressed", request_id: "req_9", retry: false,
    next_step: "Read the item again and retry with its current state.",
  });

  const denied = await connect(() => [403, { error: { code: "forbidden", message: "this needs the sequences.manage scope", request_id: "req_2" } }]);
  const res2 = await denied.client.callTool({ name: "enroll_leads", arguments: { brand: BRAND, sequence_id: SEQ, lead_ids: [SEQ] } });
  assert.match(res2.content[0].text, /Next step: This app or token isn.t allowed to do this/);
  assert.equal(res2.structuredContent.error.retry, false);

  const busy = await connect(() => [429, { error: { code: "rate_limited", message: "slow down", request_id: "req_3" } }]);
  const res3 = await busy.client.callTool({ name: "list_inbox", arguments: { brand: BRAND } });
  assert.equal(res3.structuredContent.error.retry, true);
});

test("domain tools put the domain in the path", async () => {
  const { client, calls } = await connect(() => [200, { domain: "acme-com.example", status: "paused" }]);
  const res = await client.callTool({ name: "update_domain", arguments: { domain: "acme-com.example", status: "paused", daily_limit: null } });
  assert.equal(res.isError, undefined);
  assert.equal(calls[0].method, "PATCH");
  assert.equal(calls[0].url.pathname, `/v1/workspaces/${WS}/sending-domains/acme-com.example`);
  assert.deepEqual(JSON.parse(calls[0].body), { status: "paused", daily_limit: null });

  await client.callTool({ name: "get_domain", arguments: { domain: "acme-com.example" } });
  assert.equal(calls[1].method, "GET");
  assert.equal(calls[1].url.pathname, `/v1/workspaces/${WS}/sending-domains/acme-com.example`);
});

test("analytics tools put the integration and report in the path", async () => {
  const { client, calls } = await connect(() => [200, { report: "pages", rows: [] }]);
  const res = await client.callTool({ name: "get_analytics_report", arguments: { integration_id: SEQ, report: "pages", from: "2026-10-01", to: "2026-10-24", limit: 5 } });
  assert.equal(res.isError, undefined);
  assert.equal(calls[0].url.pathname, `/v1/workspaces/${WS}/analytics/${SEQ}/reports/pages`);
  assert.equal(calls[0].url.searchParams.get("from"), "2026-10-01");
  assert.equal(calls[0].url.searchParams.get("limit"), "5");

  await client.callTool({ name: "set_analytics_site", arguments: { integration_id: SEQ, site: "123" } });
  assert.equal(calls[1].method, "PUT");
  assert.deepEqual(JSON.parse(calls[1].body), { site: "123" });

  const ro = await connect(() => [200, {}], { readOnly: true });
  const names = (await ro.client.listTools()).tools.map((t) => t.name);
  assert.ok(names.includes("get_analytics_summary") && !names.includes("set_analytics_site"));
});

test("stored analytics tools read history and sync", async () => {
  const BRAND = "33333333-3333-4333-8333-333333333333";
  const { client, calls } = await connect(() => [200, { sites: [], rows: [] }]);
  let res = await client.callTool({ name: "get_analytics_overview", arguments: { brand: BRAND, from: "2026-10-01" } });
  assert.equal(res.isError, undefined);
  assert.equal(calls[0].url.pathname, `/v1/brands/${BRAND}/analytics/overview`);
  assert.equal(calls[0].url.searchParams.get("from"), "2026-10-01");

  await client.callTool({ name: "query_analytics_history", arguments: { brand: BRAND, report: "pages", group: "key", host: "example.com" } });
  assert.equal(calls[1].url.pathname, `/v1/brands/${BRAND}/analytics/history`);
  assert.equal(calls[1].url.searchParams.get("group"), "key");

  await client.callTool({ name: "compare_analytics_tools", arguments: { brand: BRAND, host: "example.com" } });
  assert.equal(calls[2].url.searchParams.get("host"), "example.com");

  await client.callTool({ name: "sync_analytics", arguments: { integration_id: SEQ } });
  assert.equal(calls[3].method, "POST");
  assert.equal(calls[3].url.pathname, `/v1/workspaces/${WS}/analytics/${SEQ}/sync`);
  await client.callTool({ name: "sync_analytics", arguments: {} });
  assert.equal(calls[4].url.pathname, `/v1/workspaces/${WS}/analytics/sync`);

  const ro = await connect(() => [200, {}], { readOnly: true });
  const names = (await ro.client.listTools()).tools.map((t) => t.name);
  assert.ok(names.includes("query_analytics_history") && !names.includes("sync_analytics"));
});

test("SEO tools read history free and keep paid reports out of read-only mode", async () => {
  const { client, calls } = await connect(() => [200, { data: [] }]);
  await client.callTool({ name: "get_seo_history", arguments: { report: "keywords", host: "example.com" } });
  assert.equal(calls[0].url.pathname, `/v1/workspaces/${WS}/seo/history`);
  assert.equal(calls[0].url.searchParams.get("report"), "keywords");

  await client.callTool({ name: "get_seo_report", arguments: { integration_id: SEQ, report: "backlinks", target: "rival-com.example", limit: 20 } });
  assert.equal(calls[1].url.pathname, `/v1/workspaces/${WS}/seo/${SEQ}/reports/backlinks`);
  assert.equal(calls[1].url.searchParams.get("target"), "rival-com.example");

  await client.callTool({ name: "update_seo_settings", arguments: { integration_id: SEQ, domains: ["example.com"], monthly_budget: null } });
  assert.equal(calls[2].method, "PUT");
  assert.deepEqual(JSON.parse(calls[2].body), { domains: ["example.com"], monthly_budget: null });

  const ro = await connect(() => [200, {}], { readOnly: true });
  const names = (await ro.client.listTools()).tools.map((t) => t.name);
  assert.ok(names.includes("list_seo") && names.includes("get_seo_history"));
  assert.ok(!names.includes("get_seo_report") && !names.includes("sync_seo"));
});

test("sending server tools put the server id in the path", async () => {
  const { client, calls } = await connect(() => [200, { id: SEQ, status: "paused" }]);
  const res = await client.callTool({ name: "update_sending_server", arguments: { server_id: SEQ, status: "paused", daily_limit: null, ips: ["203.0.113.9"] } });
  assert.equal(res.isError, undefined);
  assert.equal(calls[0].method, "PATCH");
  assert.equal(calls[0].url.pathname, `/v1/workspaces/${WS}/sending-servers/${SEQ}`);
  assert.deepEqual(JSON.parse(calls[0].body), { status: "paused", daily_limit: null, ips: ["203.0.113.9"] });

  await client.callTool({ name: "check_sending_server", arguments: { server_id: SEQ } });
  assert.equal(calls[1].method, "POST");
  assert.equal(calls[1].url.pathname, `/v1/workspaces/${WS}/sending-servers/${SEQ}/check`);
});

test("a 204 answer reads as ok", async () => {
  const { client } = await connect(() => [204, undefined]);
  const res = await client.callTool({ name: "stop_enrollment", arguments: { brand: BRAND, sequence_id: SEQ, enrollment_id: SEQ } });
  assert.equal(res.isError, undefined);
  assert.equal(res.content[0].text, '{"ok":true}');
});

test("rejects bad arguments before calling the API", async () => {
  const { client, calls } = await connect(() => [200, {}]);
  const res = await client.callTool({ name: "get_sequence", arguments: { sequence_id: "not-a-uuid" } });
  assert.equal(res.isError, true);
  assert.equal(calls.length, 0);
});

test("turns warm-up on for a pool", async () => {
  const { client, calls } = await connect(() => [200, { updated: 12 }]);
  const pool = "44444444-4444-4444-8444-444444444444";
  const res = await client.callTool({ name: "set_warmup", arguments: { enabled: true, pool_id: pool } });
  assert.equal(JSON.parse(res.content[0].text).updated, 12);
  assert.equal(calls[0].url.pathname, `/v1/workspaces/${WS}/mailboxes/warmup`);
  assert.deepEqual(JSON.parse(calls[0].body), { enabled: true, pool_id: pool });
});

test("lead and mailbox tools: read, change, delete, list membership", async () => {
  const { client, calls } = await connect(() => [200, {}]);
  const LEAD = "44444444-4444-4444-8444-444444444444";
  const LIST = "55555555-5555-4555-8555-555555555555";
  await client.callTool({ name: "get_lead", arguments: { brand: BRAND, lead_id: LEAD } });
  assert.equal(calls.at(-1).url.pathname, `/v1/brands/${BRAND}/leads/${LEAD}`);
  await client.callTool({ name: "update_lead", arguments: { brand: BRAND, lead_id: LEAD, title: "CFO", custom: { tier: null } } });
  assert.equal(calls.at(-1).method, "PATCH");
  assert.deepEqual(JSON.parse(calls.at(-1).body), { title: "CFO", custom: { tier: null } });
  await client.callTool({ name: "delete_lead", arguments: { brand: BRAND, lead_id: LEAD, suppress: true } });
  assert.equal(calls.at(-1).method, "DELETE");
  assert.equal(calls.at(-1).url.search, "?suppress=true");
  await client.callTool({ name: "add_leads_to_list", arguments: { brand: BRAND, list_id: LIST, lead_ids: [LEAD] } });
  assert.equal(calls.at(-1).url.pathname, `/v1/brands/${BRAND}/lead-lists/${LIST}/leads`);
  await client.callTool({ name: "remove_lead_from_list", arguments: { brand: BRAND, list_id: LIST, lead_id: LEAD } });
  assert.equal(calls.at(-1).url.pathname, `/v1/brands/${BRAND}/lead-lists/${LIST}/leads/${LEAD}`);
  await client.callTool({ name: "get_mailbox", arguments: { mailbox_id: LEAD } });
  assert.equal(calls.at(-1).url.pathname, `/v1/workspaces/${WS}/mailboxes/${LEAD}`);
  await client.callTool({ name: "update_mailbox", arguments: { mailbox_id: LEAD, status: "paused" } });
  assert.deepEqual(JSON.parse(calls.at(-1).body), { status: "paused" });

  const ro = await connect(() => [200, {}], { readOnly: true });
  const names = (await ro.client.listTools()).tools.map((t) => t.name);
  assert.ok(names.includes("get_lead") && names.includes("get_mailbox"));
  assert.ok(!names.includes("delete_lead") && !names.includes("update_mailbox"));
});

test("allocation tools call the allocation API", async () => {
  const { client, calls } = await connect(() => [200, { data: [] }]);
  const box = "44444444-4444-4444-8444-444444444444";
  await client.callTool({ name: "assign_mailboxes", arguments: { sequence_id: SEQ, mailbox_ids: [box] } });
  assert.equal(calls[0].url.pathname, `/v1/workspaces/${WS}/mailboxes/assign`);
  assert.deepEqual(JSON.parse(calls[0].body), { sequence_id: SEQ, mailbox_ids: [box] });

  await client.callTool({ name: "decide_allocation_suggestion", arguments: { suggestion_id: box, decision: "decline" } });
  assert.equal(calls[1].url.pathname, `/v1/workspaces/${WS}/allocation/suggestions/${box}/decline`);

  await client.callTool({ name: "get_sequence_mailboxes", arguments: { sequence_id: SEQ } });
  assert.equal(calls[2].url.pathname, `/v1/workspaces/${WS}/sequences/${SEQ}/allocation`);

  const bad = await client.callTool({ name: "lock_mailboxes", arguments: { mailbox_ids: [], kind: null } });
  assert.equal(bad.isError, true);
});

test("infrastructure tools: read-only mode leaves out approving and moving", async () => {
  const PLAN = "44444444-4444-4444-8444-444444444444";
  const { client, calls } = await connect(() => [200, { id: PLAN, status: "building" }]);
  await client.callTool({ name: "approve_infra_plan", arguments: { plan_id: PLAN, once_cents: 2400, monthly_cents: 900 } });
  assert.equal(calls[0].url.pathname, `/v1/workspaces/${WS}/infra-plans/${PLAN}/approve`);
  assert.deepEqual(JSON.parse(calls[0].body), { once_cents: 2400, monthly_cents: 900 });

  const ro = await connect(() => [200, {}], { readOnly: true });
  const names = (await ro.client.listTools()).tools.map((t) => t.name);
  assert.ok(names.includes("get_infrastructure") && names.includes("preview_infra_plan"));
  assert.ok(!names.includes("approve_infra_plan") && !names.includes("move_mailboxes"));
});

test("missions are retired: history only, and no mission header", async () => {
  const MISSION = "44444444-4444-4444-8444-444444444444";
  const held = { action: { id: "55555555-5555-4555-8555-555555555555", status: "held" }, approval_required: true, message: "Held for approval" };
  const { client, calls } = await connect((req) => (req.method === "POST" ? [202, held] : [200, { data: [] }]));
  const names = (await client.listTools()).tools.map((t) => t.name);
  for (const gone of ["use_mission", "update_mission", "submit_mission_plan", "post_mission_update", "pause_mission", "stop_mission", "complete_mission", "approve_mission", "resume_mission", "decide_mission_action", "request_connector"]) {
    assert.ok(!names.includes(gone), gone);
  }

  // A mission's history needs its id.
  const none = await client.callTool({ name: "get_mission", arguments: {} });
  assert.equal(none.isError, true);
  await client.callTool({ name: "get_mission", arguments: { mission_id: MISSION } });
  assert.equal(calls.at(-1).url.pathname, `/v1/workspaces/${WS}/missions/${MISSION}`);

  // A held change comes back as a normal result with the action to poll,
  // and no call carries the mission header.
  const res = await client.callTool({ name: "create_sequence", arguments: { brand: BRAND, name: "Held" } });
  assert.equal(res.isError, undefined);
  assert.equal(JSON.parse(res.content[0].text).approval_required, true);
  assert.ok(calls.every((c) => c.headers.get("outflare-mission") === null));
});

test("projects were removed: no project tools", async () => {
  const { client } = await connect(() => [200, {}]);
  const names = (await client.listTools()).tools.map((t) => t.name);
  for (const gone of ["list_projects", "get_project_brief", "create_project", "update_project", "add_to_project", "remove_from_project"]) {
    assert.ok(!names.includes(gone), gone);
  }
});

test("analytics for agents: changes, catalog resource, prompts and compact answers", async () => {
  const BRAND = "33333333-3333-4333-8333-333333333333";
  const { client, calls } = await connect((req) =>
    new URL(req.url).pathname.endsWith("/brands")
      ? [200, { data: [{ id: BRAND, handle: "acme" }] }]
      : [200, { websites: [{ host: "example.com", tools: [{ metrics: [{ metric: "ctr", now: 0.0345678, before: null }] }] }], units: {} }],
  );
  const res = await client.callTool({ name: "get_analytics_changes", arguments: { brand: BRAND, host: "example.com", days: 28 } });
  assert.equal(calls[0].url.pathname, `/v1/brands/${BRAND}/analytics/changes`);
  assert.equal(calls[0].url.searchParams.get("days"), "28");
  const text = res.content[0].text;
  assert.ok(!text.includes("\n") && !text.includes("null") && text.includes('"now":0.0346'), text);

  const resources = (await client.listResources()).resources;
  assert.ok(resources.some((r) => r.uri === "outflare://analytics/catalog"));
  const read = await client.readResource({ uri: "outflare://analytics/catalog" });
  // Each brand has its own catalog: the first brand's without OUTFLARE_BRAND.
  assert.equal(calls[1].url.pathname, `/v1/workspaces/${WS}/brands`);
  assert.equal(calls[2].url.pathname, `/v1/brands/${BRAND}/analytics/catalog`);
  assert.equal(read.contents[0].mimeType, "application/json");

  const prompts = (await client.listPrompts()).prompts.map((p) => p.name);
  assert.deepEqual(prompts.sort(), ["explain_traffic_change", "run_reddit_campaign", "seo_check", "set_up_outflare", "weekly_website_review", "whats_blocked"]);
  const p = await client.getPrompt({ name: "weekly_website_review", arguments: { host: "example.com" } });
  assert.ok(p.messages[0].content.text.includes("get_analytics_changes with host example.com"));

  const ro = await connect(() => [200, {}], { readOnly: true });
  const names = (await ro.client.listTools()).tools.map((t) => t.name);
  assert.ok(names.includes("get_analytics_changes") && names.includes("get_analytics_catalog"));
});

test("webhook tools: create, test and redeliver call the brand's webhook endpoints", async () => {
  const HOOK = "44444444-4444-4444-8444-444444444444";

  const { client, calls } = await connect(() => [200, { id: HOOK, status: "delivered" }]);
  await client.callTool({ name: "create_webhook", arguments: { brand: BRAND, url: "https://example.com/h", events: ["lead.replied"] } });
  assert.equal(calls[0].url.pathname, `/v1/brands/${BRAND}/webhooks`);
  assert.deepEqual(JSON.parse(calls[0].body), { url: "https://example.com/h", events: ["lead.replied"] });
  await client.callTool({ name: "test_webhook", arguments: { brand: BRAND, webhook_id: HOOK } });
  assert.equal(calls[1].url.pathname, `/v1/brands/${BRAND}/webhooks/${HOOK}/test`);
  await client.callTool({ name: "redeliver_webhook", arguments: { brand: BRAND, webhook_id: HOOK, delivery_id: 7 } });
  assert.equal(calls[2].url.pathname, `/v1/brands/${BRAND}/webhooks/${HOOK}/deliveries/7/redeliver`);
  await client.callTool({ name: "list_webhook_deliveries", arguments: { brand: BRAND, webhook_id: HOOK, status: "failed" } });
  assert.equal(calls[3].url.search, "?status=failed");

  const ro = await connect(() => [200, {}], { readOnly: true });
  const names = (await ro.client.listTools()).tools.map((t) => t.name);
  assert.ok(names.includes("list_webhooks") && !names.includes("create_webhook"));
});

test("answers are compact JSON with structuredContent, and list_events joins types", async () => {
  const { client, calls } = await connect(() => [200, { data: [{ id: "evt_2", type: "lead.replied" }], next_cursor: "c2", has_more: false }]);
  const res = await client.callTool({ name: "list_events", arguments: { cursor: "c1", types: ["lead.replied", "mailbox.*"] } });
  assert.equal(res.content[0].text, JSON.stringify({ data: [{ id: "evt_2", type: "lead.replied" }], next_cursor: "c2", has_more: false }));
  assert.equal(res.structuredContent.next_cursor, "c2");
  assert.equal(calls[0].url.pathname, `/v1/workspaces/${WS}/events`);
  assert.equal(calls[0].url.searchParams.get("types"), "lead.replied,mailbox.*");
  assert.equal(calls[0].url.searchParams.get("cursor"), "c1");
});

test("names the tool, the agent's app and the client on every call", async () => {
  const { client, calls } = await connect(() => [200, { data: [] }]);
  await client.callTool({ name: "list_inbox", arguments: { brand: BRAND } });
  const h = calls.at(-1).headers;
  assert.equal(h.get("outflare-tool"), "list_inbox");
  assert.equal(h.get("outflare-agent"), "test");
  assert.match(h.get("outflare-client"), /^mcp\//);
});

test("get_context comes first and joins the context with the status and onboarding", async () => {
  const context = {
    context: { company: "Acme", missing: ["goals", "voice"], ask_for_missing: true, version: 2 },
    memories: [{ id: SEQ, kind: "preference", text: "No pricing in email one", pinned: true }],
    memory_count: 1,
    markdown: "# Acme\n\n## Remembered\n\n- (preference) No pricing in email one\n",
  };
  const status = { summary: "1 problem is stopping sending: no mailboxes connected.", can_send: false, setup: [], blockers: [{ code: "no_mailboxes" }] };
  const onboarding = { steps: [], done: 1, total: 6, next_step: "goal", complete: false, goal: null, dismissed: false, dismissed_at: null, show: false };
  const { client, calls } = await connect((req) => {
    const p = new URL(req.url).pathname;
    return p.endsWith("/status") ? [200, status] : p.endsWith("/onboarding") ? [200, onboarding] : [200, context];
  });
  const { tools } = await client.listTools();
  assert.equal(tools[0].name, "get_context");
  assert.match(tools[0].description, /first in every conversation/);

  const res = await client.callTool({ name: "get_context", arguments: {} });
  assert.equal(res.structuredContent.context.company, "Acme");
  assert.equal(res.structuredContent.status.blockers[0].code, "no_mailboxes");
  assert.equal(res.structuredContent.onboarding.next_step, "goal");
  assert.deepEqual(calls.map((c) => c.url.pathname).sort(), [`/v1/workspaces/${WS}/context`, `/v1/workspaces/${WS}/onboarding`, `/v1/workspaces/${WS}/status`]);

  const md = await client.readResource({ uri: "outflare://context" });
  assert.match(md.contents[0].text, /No pricing in email one/);
  const guide = await client.readResource({ uri: "outflare://guide" });
  assert.match(guide.contents[0].text, /How things connect/);
  const { instructions } = client.getInstructions ? { instructions: client.getInstructions() } : { instructions: "" };
  assert.match(instructions, /Start of every conversation: call get_context/);
});

test("memory tools save, correct and forget, and stay out of read-only mode", async () => {
  const { client, calls } = await connect(() => [201, { id: SEQ, kind: "decision", text: "Only Series A and later", pinned: false }]);
  await client.callTool({ name: "remember", arguments: { brand: BRAND, text: "Only Series A and later", kind: "decision" } });
  assert.equal(calls[0].method, "POST");
  assert.equal(calls[0].url.pathname, `/v1/brands/${BRAND}/memories`);
  assert.deepEqual(JSON.parse(calls[0].body), { text: "Only Series A and later", kind: "decision" });

  await client.callTool({ name: "update_context", arguments: { goals: "20 demos a month", version: 2 } });
  assert.equal(calls[1].method, "PATCH");
  assert.equal(calls[1].url.pathname, `/v1/workspaces/${WS}/context`);

  await client.callTool({ name: "forget", arguments: { brand: BRAND, memory_id: SEQ } });
  assert.equal(calls[2].method, "DELETE");
  assert.equal(calls[2].url.pathname, `/v1/brands/${BRAND}/memories/${SEQ}`);

  const ro = await connect(() => [200, {}], { readOnly: true });
  const names = (await ro.client.listTools()).tools.map((t) => t.name);
  assert.ok(names.includes("get_context") && names.includes("list_memories") && names.includes("get_status"));
  assert.ok(!names.includes("remember") && !names.includes("update_context") && !names.includes("forget"));
});

test("brand tools reach the brand routes, and OUTFLARE_BRAND is the list default", async () => {
  const { client, calls } = await connect(() => [200, { data: [] }], { brand: "repocket" });
  await client.callTool({ name: "list_mailboxes", arguments: {} });
  assert.equal(calls[0].url.search, "?brand=repocket");
  await client.callTool({ name: "list_mailboxes", arguments: { brand: "all" } });
  assert.equal(calls[1].url.search, "?brand=all");
  await client.callTool({ name: "update_brand_context", arguments: { brand: "geonode", voice: "Plain." } });
  assert.equal(calls[2].method, "PATCH");
  assert.equal(calls[2].url.pathname, `/v1/workspaces/${WS}/brands/geonode/context`);
  assert.deepEqual(JSON.parse(calls[2].body), { voice: "Plain." });
  await client.callTool({ name: "get_context", arguments: {} });
  assert.ok(calls.some((c) => c.url.pathname.endsWith("/context") && c.url.search === "?brand=repocket"));
});

test("customer tools and run_query use the brand routes, looking a handle up first", async () => {

  const { client, calls } = await connect((req) => {
    const u = new URL(req.url);
    if (u.pathname.endsWith("/brands/repocket")) return [200, { id: BRAND, handle: "repocket" }];
    return [200, { data: [], columns: [], rows: [] }];
  });
  await client.callTool({ name: "list_records", arguments: { brand: "repocket", type: "person" } });
  assert.equal(calls[0].url.pathname, `/v1/workspaces/${WS}/brands/repocket`);
  assert.equal(calls[1].url.pathname, `/v1/brands/${BRAND}/records`);
  assert.equal(calls[1].url.search, "?type=person");
  await client.callTool({ name: "get_record_timeline", arguments: { brand: BRAND, record_id: SEQ } });
  assert.equal(calls[2].url.pathname, `/v1/brands/${BRAND}/records/${SEQ}/timeline`);
  await client.callTool({ name: "run_query", arguments: { brand: BRAND, sql: "select count(*) from people" } });
  assert.equal(calls[3].method, "POST");
  assert.equal(calls[3].url.pathname, `/v1/brands/${BRAND}/query`);
  assert.deepEqual(JSON.parse(calls[3].body), { sql: "select count(*) from people" });
  await client.callTool({ name: "add_to_static_list", arguments: { brand: BRAND, list_id: SEQ, record_ids: [WS] } });
  assert.equal(calls[4].url.pathname, `/v1/brands/${BRAND}/smart-lists/${SEQ}/members`);
  const missing = await client.callTool({ name: "list_records", arguments: {} });
  assert.equal(missing.isError, true);
});

test("activation tools use the brand routes: rules, journeys, lifecycle email and alerts", async () => {

  const { client, calls } = await connect((req) => {
    const u = new URL(req.url);
    if (u.pathname.endsWith("/brands/repocket")) return [200, { id: BRAND, handle: "repocket" }];
    return [200, { data: [] }];
  });
  await client.callTool({ name: "list_rules", arguments: { brand: "repocket" } });
  assert.equal(calls[0].url.pathname, `/v1/workspaces/${WS}/brands/repocket`);
  assert.equal(calls[1].url.pathname, `/v1/brands/${BRAND}/rules`);
  await client.callTool({ name: "add_to_journey", arguments: { brand: BRAND, journey: SEQ, record_id: WS } });
  assert.equal(calls[2].url.pathname, `/v1/brands/${BRAND}/journeys/${SEQ}/runs`);
  await client.callTool({ name: "edit_journey_draft", arguments: { brand: BRAND, journey: SEQ, entry: "again" } });
  assert.equal(calls[3].url.pathname, `/v1/brands/${BRAND}/journeys/${SEQ}/draft`);
  assert.equal(calls[4].method, "PATCH");
  assert.equal(calls[4].url.pathname, `/v1/brands/${BRAND}/journeys/${SEQ}/draft`);
  await client.callTool({ name: "send_email", arguments: { brand: BRAND, to: ["ana@example.com"], from: "hi@example.com", subject: "Hi", text: "Hi" } });
  assert.equal(calls[5].url.pathname, `/v1/brands/${BRAND}/emails`);
  await client.callTool({ name: "list_email_topics", arguments: { brand: BRAND } });
  assert.equal(calls[6].url.pathname, `/v1/brands/${BRAND}/email-topics`);
  await client.callTool({ name: "list_alerts", arguments: { brand: BRAND } });
  assert.equal(calls[7].url.pathname, `/v1/brands/${BRAND}/alerts`);
  const missing = await client.callTool({ name: "list_journeys", arguments: {} });
  assert.equal(missing.isError, true);
});

test("reddit tools: guide, campaign prompt, actions and the report; read-only leaves out placing", async () => {
  const { client, calls } = await connect(() => [200, { data: [] }]);
  const guide = await client.readResource({ uri: "outflare://reddit-guide" });
  assert.match(guide.contents[0].text, /place_reddit_action/);
  const p = await client.getPrompt({ name: "run_reddit_campaign", arguments: { brand: "geonode", budget: "300" } });
  assert.match(p.messages[0].content.text, /geonode.*300 Engain credits/);

  const thread = "https://www.reddit.com/r/proxies/comments/1abc2d/best_residential_proxies/";
  await client.callTool({ name: "place_reddit_action", arguments: { brand: BRAND, kind: "comment", target_url: thread, content: "Geonode held up for me", brand_mentioned: true } });
  assert.equal(calls[0].method, "POST");
  assert.equal(calls[0].url.pathname, `/v1/brands/${BRAND}/reddit/actions`);
  assert.equal(JSON.parse(calls[0].body).kind, "comment");
  await client.callTool({ name: "get_reddit_report", arguments: { brand: BRAND, group_by: "strategy" } });
  assert.equal(calls[1].url.pathname, `/v1/brands/${BRAND}/reddit/report`);
  assert.equal(calls[1].url.search, "?group_by=strategy");
  await client.callTool({ name: "get_reddit_subreddit", arguments: { brand: BRAND, subreddit: "r/proxies" } });
  assert.equal(calls[2].url.pathname, `/v1/brands/${BRAND}/reddit/subreddits/proxies`);
  await client.callTool({ name: "close_reddit_experiment", arguments: { brand: BRAND, experiment: SEQ, conclusion: "Plain recommendations last a week." } });
  assert.equal(calls[3].method, "PATCH");
  assert.equal(calls[3].url.pathname, `/v1/brands/${BRAND}/reddit/experiments/${SEQ}`);
  assert.deepEqual(JSON.parse(calls[3].body), { status: "closed", conclusion: "Plain recommendations last a week." });

  const ro = await connect(() => [200, {}], { readOnly: true });
  const names = (await ro.client.listTools()).tools.map((t) => t.name);
  assert.ok(names.includes("get_reddit_report") && names.includes("preflight_reddit_drafts"));
  assert.ok(!names.includes("place_reddit_action") && !names.includes("approve_reddit_action"));
});

test("saved query tools use the brand routes; save_query makes or changes one; read-only leaves it out", async () => {

  const { client, calls } = await connect(() => [200, { data: [], columns: [], rows: [] }]);
  await client.callTool({ name: "list_saved_queries", arguments: { brand: BRAND, q: "plan" } });
  assert.equal(calls[0].url.pathname, `/v1/brands/${BRAND}/queries`);
  assert.equal(calls[0].url.search, "?q=plan");
  await client.callTool({ name: "get_saved_query", arguments: { brand: BRAND, query_id: SEQ, versions: true } });
  assert.equal(calls[1].url.pathname, `/v1/brands/${BRAND}/queries/${SEQ}/versions`);
  await client.callTool({ name: "run_saved_query", arguments: { brand: BRAND, query_id: SEQ, version: 2 } });
  assert.equal(calls[2].method, "POST");
  assert.equal(calls[2].url.pathname, `/v1/brands/${BRAND}/queries/${SEQ}/run`);
  assert.deepEqual(JSON.parse(calls[2].body), { version: 2 });
  await client.callTool({ name: "save_query", arguments: { brand: BRAND, name: "By plan", sql: "select 1", visibility: "brand" } });
  assert.equal(calls[3].method, "POST");
  assert.equal(calls[3].url.pathname, `/v1/brands/${BRAND}/queries`);
  assert.deepEqual(JSON.parse(calls[3].body), { name: "By plan", sql: "select 1", visibility: "brand" });
  await client.callTool({ name: "save_query", arguments: { brand: BRAND, query_id: SEQ, sql: "select 2", note: "two" } });
  assert.equal(calls[4].method, "PATCH");
  assert.equal(calls[4].url.pathname, `/v1/brands/${BRAND}/queries/${SEQ}`);
  const missing = await client.callTool({ name: "save_query", arguments: { brand: BRAND, sql: "select 1" } });
  assert.equal(missing.isError, true);
  assert.equal(calls.length, 5);
  const ro = await connect(() => [200, {}], { readOnly: true });
  const roTools = (await ro.client.listTools()).tools.map((t) => t.name);
  assert.ok(roTools.includes("run_saved_query") && !roTools.includes("save_query"));
});

test("search insights retain brand routing, model assumptions and write permissions", async () => {
  const { client, calls } = await connect(() => [200, { report: "forecast", status: "ready", data: [], notes: [], summary: {} }]);
  await client.callTool({ name: "get_search_insights", arguments: { brand: WS, site: SEQ, report: "forecast", weeks: 12, growth: .2, currency: "EUR", order_value: 25 } });
  assert.equal(calls[0].url.pathname, `/v1/brands/${WS}/seo-sites/${SEQ}/insights`);
  assert.equal(calls[0].url.searchParams.get("currency"), "EUR");
  assert.equal(calls[0].url.searchParams.get("growth"), "0.2");
  await client.callTool({ name: "refresh_search_insights", arguments: { brand: WS, site: SEQ } });
  assert.equal(calls[1].method, "POST");
  assert.deepEqual(JSON.parse(calls[1].body), {}); // public update feed needs no API key
  const readOnly = await connect(() => [200, {}], { readOnly: true });
  const names = (await readOnly.client.listTools()).tools.map((t) => t.name);
  assert.ok(names.includes("get_search_insights") && names.includes("list_search_groups") && names.includes("get_search_workspace") && names.includes("get_search_report_run"));
  assert.ok(!names.includes("save_search_work") && !names.includes("save_search_schedule") && !names.includes("run_search_report"));
  assert.ok(!names.includes("save_search_group") && !names.includes("refresh_search_insights"));
});

test("partner proof tools: the proof, its json export, and the new list and bulk params", async () => {
  const { client, calls } = await connect(() => [200, { data: [] }]);
  await client.callTool({ name: "get_partner_reward_proof", arguments: { brand: BRAND, reward_id: SEQ } });
  assert.equal(calls[0].url.pathname, `/v1/brands/${BRAND}/partner-rewards/${SEQ}/proof`);
  await client.callTool({ name: "export_partner_proofs", arguments: { brand: BRAND, partner_id: SEQ, from: "2026-01-01T00:00:00Z" } });
  assert.equal(calls[1].url.pathname, `/v1/brands/${BRAND}/partner-proofs/export`);
  assert.deepEqual(Object.fromEntries(calls[1].url.searchParams), { partner_id: SEQ, from: "2026-01-01T00:00:00Z", format: "json" });
  await client.callTool({ name: "list_partner_rewards", arguments: { brand: BRAND, q: "ana@", source: "held_at_source" } });
  assert.deepEqual(Object.fromEntries(calls[2].url.searchParams), { q: "ana@", source: "held_at_source" });
  await client.callTool({ name: "list_partner_conversions", arguments: { brand: BRAND, q: "ch_1" } });
  assert.equal(calls[3].url.search, "?q=ch_1");
  await client.callTool({ name: "bulk_partner_rewards", arguments: { brand: BRAND, action: "approve", program: "affiliates", dry_run: true } });
  assert.deepEqual(JSON.parse(calls[4].body), { action: "approve", program: "affiliates", dry_run: true });
});

// The toolbox: apps see a short core list, and find_tools and call_tool reach
// every other tool, so no app's cap (ChatGPT keeps 200) ever hides one.
test("lists the core set by default, under the cap, and every tool with tools=all", async () => {
  const all = (await (await connect(() => [200, {}], { tools: "all" })).client.listTools()).tools.map((t) => t.name);
  const core = (await (await connect(() => [200, {}], { tools: "core" })).client.listTools()).tools.map((t) => t.name);
  assert.ok(CORE.size <= MAX_CORE, `the core set has ${CORE.size} tools; keep it at ${MAX_CORE} or fewer`);
  assert.ok(core.length <= MAX_CORE && core.length < 100, `${core.length} tools listed by default`);
  for (const name of CORE) assert.ok(all.includes(name), `core tool ${name} doesn't exist`);
  assert.deepEqual([...core].sort(), [...CORE].sort());
  assert.ok(all.length > 500 && all.includes("find_tools") && all.includes("call_tool"));
});

test("find_tools finds every tool and what people ask for", async () => {
  const { client } = await connect(() => [200, {}], { tools: "all" });
  const all = (await client.listTools()).tools;
  const find = async (query, limit = 5) => (await client.callTool({ name: "find_tools", arguments: { query, limit } })).structuredContent.tools.map((t) => t.name);
  const asks = {
    "create an email template": "create_email_template",
    "send an email": "send_email",
    "send a newsletter broadcast": "send_email_broadcast",
    "set up an email provider": "update_email_provider",
    "add an email sending domain": "add_email_domain",
    "create a journey": "create_journey",
    "email timeline for a person": "get_record_timeline",
    "check keyword rankings": "check_rankings",
    "pause an ad campaign": "pause_ad_campaign",
    "add leads to a list": "add_leads_to_list",
    "create a dashboard": "create_dashboard",
    "list partner payouts": "list_partner_payouts",
  };
  for (const [q, want] of Object.entries(asks)) assert.ok((await find(q)).includes(want), `"${q}" should find ${want}, got ${await find(q)}`);
  // Every tool can be found by the words of its own name.
  const lost = [];
  for (const t of all) if (!(await find(t.name.replace(/_/g, " "), 3)).includes(t.name)) lost.push(t.name);
  assert.deepEqual(lost, []);
  // And nearly every one by its title, as a person would put it.
  let byTitle = 0;
  for (const t of all) if ((await find(t.title ?? t.name, 10)).includes(t.name)) byTitle++;
  assert.ok(byTitle / all.length >= 0.99, `${all.length - byTitle} tools can't be found by their title`);
  const hit = (await client.callTool({ name: "find_tools", arguments: { query: "create email template", limit: 1 } })).structuredContent.tools[0];
  assert.equal(hit.name, "create_email_template");
  assert.equal(hit.input_schema.type, "object");
  assert.ok(hit.input_schema.properties);
});

test("call_tool runs any tool through the API, and explains mistakes", async () => {
  const { client, calls } = await connect(() => [200, { data: [] }], { tools: "core" });
  const listed = (await client.listTools()).tools.map((t) => t.name);
  assert.ok(!listed.includes("get_journey"));
  const res = await client.callTool({ name: "call_tool", arguments: { name: "get_journey", arguments: { brand: BRAND, journey: SEQ } } });
  assert.equal(res.isError, undefined, res.content?.[0]?.text);
  assert.equal(calls.length, 1);
  assert.match(calls[0].url.pathname, new RegExp(SEQ));
  assert.equal(calls[0].headers.get("outflare-tool"), "get_journey");

  const bad = await client.callTool({ name: "call_tool", arguments: { name: "get_journey", arguments: {} } });
  assert.equal(bad.isError, true);
  assert.match(bad.content[0].text, /don't fit.*journey/s);

  const typo = await client.callTool({ name: "call_tool", arguments: { name: "create_email_templates" } });
  assert.equal(typo.isError, true);
  assert.match(typo.content[0].text, /Did you mean: .*create_email_template/);
});

test("read-only connections can't reach write tools through call_tool", async () => {
  const { client, calls } = await connect(() => [200, {}], { readOnly: true, tools: "core" });
  const tools = (await client.listTools()).tools;
  assert.ok(tools.every((t) => t.annotations.readOnlyHint), tools.map((t) => t.name).join());
  const res = await client.callTool({ name: "call_tool", arguments: { name: "send_email", arguments: {} } });
  assert.equal(res.isError, true);
  assert.match(res.content[0].text, /can only read/);
  assert.equal(calls.length, 0);
});

test("get_context and the instructions say how to find every tool", async () => {
  const { client } = await connect(() => [200, {}], { tools: "core" });
  assert.match(client.getInstructions(), /find_tools/);
  const res = await client.callTool({ name: "get_context", arguments: {} });
  assert.match(res.structuredContent.tools, /find_tools.*call_tool/s);
});

test("the hosted server lists the core set, every tool with ?tools=all, and reports the server's version", async () => {
  const { createServer: createHttp } = await import("node:http");
  const { serveHttp } = await import("../dist/http.js");
  const { StreamableHTTPClientTransport } = await import("@modelcontextprotocol/sdk/client/streamableHttp.js");
  const api = createHttp((req, res) => {
    res.setHeader("Content-Type", "application/json");
    if (req.url === "/v1/meta") return res.end(JSON.stringify({ version: "1.2.3+abc" }));
    if (req.url === "/v1/whoami") return res.end(JSON.stringify({ token: { id: "tok", workspace_id: WS, scopes: ["leads.manage"] } }));
    res.statusCode = 404;
    res.end("{}");
  });
  await new Promise((r) => api.listen(0, "127.0.0.1", r));
  const port = await new Promise((r) => {
    const probe = createHttp();
    probe.listen(0, "127.0.0.1", () => {
      const p = probe.address().port;
      probe.close(() => r(p));
    });
  });
  const mcp = serveHttp({ listen: `127.0.0.1:${port}`, baseUrl: `http://127.0.0.1:${api.address().port}`, secret: "s" });
  await new Promise((r) => (mcp.http.listening ? r() : mcp.http.once("listening", r)));
  const open = async (query) => {
    const client = new Client({ name: "test", version: "0" });
    await client.connect(new StreamableHTTPClientTransport(new URL(`http://127.0.0.1:${port}/mcp${query}`), {
      requestInit: { headers: { Authorization: "Bearer oft_test_not_a_real_token", "X-Outflare-Proxy-Secret": "s" } },
    }));
    return client;
  };
  try {
    const core = await open("");
    assert.equal(core.getServerVersion().version, "1.2.3+abc");
    const n = (await core.listTools()).tools.length;
    assert.ok(n <= MAX_CORE, `${n} tools`);
    const all = await open("?tools=all");
    assert.ok((await all.listTools()).tools.length > 500);
    await core.close();
    await all.close();
  } finally {
    mcp.close();
    api.close();
  }
});
