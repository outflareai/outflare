// Runs against the built server (pnpm build first) with a fake API behind
// the SDK's fetch, and a real MCP client over an in-memory transport.
import assert from "node:assert/strict";
import { test } from "node:test";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { InMemoryTransport } from "@modelcontextprotocol/sdk/inMemory.js";
import { createClient } from "@outflare/sdk";
import { createServer } from "../dist/server.js";

const WS = "11111111-1111-4111-8111-111111111111";
const SEQ = "22222222-2222-4222-8222-222222222222";

async function connect(api, opts = {}) {
  const calls = [];
  const fetch = async (req) => {
    const body = req.method === "GET" ? undefined : await req.text();
    calls.push({ method: req.method, url: new URL(req.url), body, headers: req.headers });
    const [status, json] = api(req, body);
    return new Response(json === undefined ? null : JSON.stringify(json), { status, headers: { "Content-Type": "application/json", "X-Request-Id": "req_1" } });
  };
  const server = createServer({ client: createClient({ baseUrl: "http://gtm.test", token: "oft_x", fetch }), workspace: WS, ...opts });
  const client = new Client({ name: "test", version: "0" });
  const [a, b] = InMemoryTransport.createLinkedPair();
  await Promise.all([server.connect(a), client.connect(b)]);
  return { client, calls };
}

test("lists read and write tools, and only read tools when read-only", async () => {
  const { client } = await connect(() => [200, {}]);
  const { tools } = await client.listTools();
  const names = tools.map((t) => t.name);
  assert.ok(names.includes("get_overview") && names.includes("enroll_leads") && names.includes("reply_to_inbox_message"));
  assert.ok(names.includes("list_supplier_mailboxes") && names.includes("import_supplier_mailboxes") && names.includes("update_supplier_sync"));
  assert.equal(tools.find((t) => t.name === "list_inbox").annotations.readOnlyHint, true);
  assert.equal(tools.find((t) => t.name === "reply_to_inbox_message").annotations.openWorldHint, true);

  const ro = await connect(() => [200, {}], { readOnly: true });
  const roTools = (await ro.client.listTools()).tools;
  assert.ok(roTools.length > 0);
  assert.ok(roTools.every((t) => t.annotations.readOnlyHint), roTools.map((t) => t.name).join());
});

test("calls the API with the workspace, token and arguments", async () => {
  const { client, calls } = await connect(() => [200, { enrolled: 2, already_enrolled: 0, suppressed: 1, active_elsewhere: 0 }]);
  const lead = "33333333-3333-4333-8333-333333333333";
  const res = await client.callTool({ name: "enroll_leads", arguments: { sequence_id: SEQ, lead_ids: [lead] } });
  assert.equal(res.isError, undefined);
  assert.equal(JSON.parse(res.content[0].text).enrolled, 2);
  assert.equal(calls[0].method, "POST");
  assert.equal(calls[0].url.pathname, `/v1/workspaces/${WS}/sequences/${SEQ}/enrollments`);
  assert.equal(calls[0].headers.get("authorization"), "Bearer oft_x");
  assert.deepEqual(JSON.parse(calls[0].body), { lead_ids: [lead] });

  await client.callTool({ name: "list_inbox", arguments: { unread: true, limit: 5 } });
  assert.equal(calls[1].url.search, "?unread=true&limit=5");
});

test("imports CSV as text/csv with the list name", async () => {
  const { client, calls } = await connect(() => [200, { created: 1, updated: 0, duplicates: 0, suppressed: 0, errors: [], list_id: null }]);
  const csv = "email,first_name\nana@acme-com.example,Ana\n";
  const res = await client.callTool({ name: "import_leads_csv", arguments: { csv, list: "Q4 founders" } });
  assert.equal(res.isError, undefined);
  assert.equal(calls[0].headers.get("content-type"), "text/csv");
  assert.equal(calls[0].url.searchParams.get("list"), "Q4 founders");
  assert.equal(calls[0].body, csv);
});

test("returns API errors as tool errors the model can read", async () => {
  const { client } = await connect(() => [409, { error: { code: "conflict", message: "the recipient is suppressed", request_id: "req_9" } }]);
  const res = await client.callTool({ name: "reply_to_inbox_message", arguments: { message_id: SEQ, body: "Thanks!" } });
  assert.equal(res.isError, true);
  assert.equal(res.content[0].text, "409 conflict: the recipient is suppressed (request req_9)\nNext step: Read the item again and retry with its current state.");
  assert.deepEqual(res.structuredContent.error, {
    status: 409, code: "conflict", param: null, message: "the recipient is suppressed", request_id: "req_9", retry: false,
    next_step: "Read the item again and retry with its current state.",
  });

  const denied = await connect(() => [403, { error: { code: "forbidden", message: "this needs the sequences.manage scope", request_id: "req_2" } }]);
  const res2 = await denied.client.callTool({ name: "enroll_leads", arguments: { sequence_id: SEQ, lead_ids: [SEQ] } });
  assert.match(res2.content[0].text, /Next step: This app or token isn.t allowed to do this/);
  assert.equal(res2.structuredContent.error.retry, false);

  const busy = await connect(() => [429, { error: { code: "rate_limited", message: "slow down", request_id: "req_3" } }]);
  const res3 = await busy.client.callTool({ name: "list_inbox", arguments: {} });
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
  const { client, calls } = await connect(() => [200, { sites: [], rows: [] }]);
  let res = await client.callTool({ name: "get_analytics_overview", arguments: { from: "2026-10-01" } });
  assert.equal(res.isError, undefined);
  assert.equal(calls[0].url.pathname, `/v1/workspaces/${WS}/analytics/overview`);
  assert.equal(calls[0].url.searchParams.get("from"), "2026-10-01");

  await client.callTool({ name: "query_analytics_history", arguments: { report: "pages", group: "key", host: "example.com" } });
  assert.equal(calls[1].url.pathname, `/v1/workspaces/${WS}/analytics/history`);
  assert.equal(calls[1].url.searchParams.get("group"), "key");

  await client.callTool({ name: "compare_analytics_tools", arguments: { host: "example.com" } });
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
  const res = await client.callTool({ name: "stop_enrollment", arguments: { sequence_id: SEQ, enrollment_id: SEQ } });
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

test("agency mode switches between client workspaces", async () => {
  const AG = "44444444-4444-4444-8444-444444444444";
  const C2 = "55555555-5555-4555-8555-555555555555";
  const { client, calls } = await connect(
    (req) => (new URL(req.url).pathname === `/v1/workspaces/${C2}` ? [200, { id: C2, name: "Contoso", slug: "contoso" }] : [200, { data: [] }]),
    { agency: AG, workspace: undefined },
  );
  const names = (await client.listTools()).tools.map((t) => t.name);
  assert.ok(names.includes("list_clients") && names.includes("use_client") && names.includes("create_client"));
  await client.callTool({ name: "list_clients", arguments: {} });
  assert.equal(calls[0].url.pathname, `/v1/agencies/${AG}/clients`);
  const res = await client.callTool({ name: "use_client", arguments: { workspace_id: C2 } });
  assert.match(res.content[0].text, /Contoso/);
  await client.callTool({ name: "list_leads", arguments: {} });
  assert.equal(calls.at(-1).url.pathname, `/v1/workspaces/${C2}/leads`);
  // Supplier tools follow the switch too.
  const SUP = "66666666-6666-4666-8666-666666666666";
  await client.callTool({ name: "list_supplier_mailboxes", arguments: { integration_id: SUP } });
  assert.equal(calls.at(-1).url.pathname, `/v1/workspaces/${C2}/integrations/${SUP}/mailboxes`);
});

test("lead and mailbox tools: read, change, delete, list membership", async () => {
  const { client, calls } = await connect(() => [200, {}]);
  const LEAD = "44444444-4444-4444-8444-444444444444";
  const LIST = "55555555-5555-4555-8555-555555555555";
  await client.callTool({ name: "get_lead", arguments: { lead_id: LEAD } });
  assert.equal(calls.at(-1).url.pathname, `/v1/workspaces/${WS}/leads/${LEAD}`);
  await client.callTool({ name: "update_lead", arguments: { lead_id: LEAD, title: "CFO", custom: { tier: null } } });
  assert.equal(calls.at(-1).method, "PATCH");
  assert.deepEqual(JSON.parse(calls.at(-1).body), { title: "CFO", custom: { tier: null } });
  await client.callTool({ name: "delete_lead", arguments: { lead_id: LEAD, suppress: true } });
  assert.equal(calls.at(-1).method, "DELETE");
  assert.equal(calls.at(-1).url.search, "?suppress=true");
  await client.callTool({ name: "add_leads_to_list", arguments: { list_id: LIST, lead_ids: [LEAD] } });
  assert.equal(calls.at(-1).url.pathname, `/v1/workspaces/${WS}/lead-lists/${LIST}/leads`);
  await client.callTool({ name: "remove_lead_from_list", arguments: { list_id: LIST, lead_id: LEAD } });
  assert.equal(calls.at(-1).url.pathname, `/v1/workspaces/${WS}/lead-lists/${LIST}/leads/${LEAD}`);
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

test("acts inside a mission once use_mission is called", async () => {
  const MISSION = "44444444-4444-4444-8444-444444444444";
  const held = { mission_action: { id: "55555555-5555-4555-8555-555555555555", status: "pending" }, approval_required: true, dry_run: false, message: "Held for approval" };
  const { client, calls } = await connect((req) => (req.method === "POST" ? [202, held] : [200, { data: [] }]));

  // Without a mission, mission tools ask for one and calls carry no header.
  const none = await client.callTool({ name: "get_mission", arguments: {} });
  assert.equal(none.isError, true);
  assert.match(none.content[0].text, /use_mission/);
  await client.callTool({ name: "list_mailboxes", arguments: {} });
  assert.equal(calls.at(-1).headers.get("outflare-mission"), null);

  await client.callTool({ name: "use_mission", arguments: { mission_id: MISSION } });
  await client.callTool({ name: "get_mission", arguments: {} });
  assert.equal(calls.at(-1).url.pathname, `/v1/workspaces/${WS}/missions/${MISSION}`);

  // A held write comes back as a normal result with the action to poll.
  const res = await client.callTool({ name: "create_sequence", arguments: { name: "In mission" } });
  assert.equal(res.isError, undefined);
  assert.equal(JSON.parse(res.content[0].text).approval_required, true);
  assert.equal(calls.at(-1).headers.get("outflare-mission"), MISSION);

  await client.callTool({ name: "request_connector", arguments: { connector: "zerobounce", why: "Verify the list before sending" } });
  assert.deepEqual(JSON.parse(calls.at(-1).body), { kind: "connect_request", connector: "zerobounce", body: "Verify the list before sending" });

  await client.callTool({ name: "use_mission", arguments: { mission_id: null } });
  await client.callTool({ name: "list_mailboxes", arguments: {} });
  assert.equal(calls.at(-1).headers.get("outflare-mission"), null);
});

test("starts in a mission from the option", async () => {
  const MISSION = "44444444-4444-4444-8444-444444444444";
  const { client, calls } = await connect(() => [200, { data: [] }], { mission: MISSION });
  await client.callTool({ name: "list_mailboxes", arguments: {} });
  assert.equal(calls[0].headers.get("outflare-mission"), MISSION);
});

test("reads a project's brief and edits it with the version it read", async () => {
  const PROJ = "44444444-4444-4444-8444-444444444444";
  const { client, calls } = await connect(() => [200, { project_id: PROJ, version: 3, markdown: "# Fintech" }]);
  const res = await client.callTool({ name: "get_project_brief", arguments: { project_id: PROJ } });
  assert.equal(JSON.parse(res.content[0].text).version, 3);
  assert.equal(calls[0].url.pathname, `/v1/workspaces/${WS}/projects/${PROJ}/brief`);

  await client.callTool({ name: "update_project", arguments: { project_id: PROJ, brief: "Lead with the audit.", brief_version: 3, icp: { titles: ["CFO"] } } });
  assert.equal(calls[1].method, "PATCH");
  assert.equal(calls[1].url.pathname, `/v1/workspaces/${WS}/projects/${PROJ}`);
  assert.deepEqual(JSON.parse(calls[1].body), { brief: "Lead with the audit.", brief_version: 3, icp: { titles: ["CFO"] } });

  await client.callTool({ name: "add_to_project", arguments: { project_id: PROJ, domains: ["acme-mail.com"] } });
  assert.equal(calls[2].url.pathname, `/v1/workspaces/${WS}/projects/${PROJ}/add`);
});

test("analytics for agents: changes, catalog resource, prompts and compact answers", async () => {
  const { client, calls } = await connect(() => [200, { websites: [{ host: "example.com", tools: [{ metrics: [{ metric: "ctr", now: 0.0345678, before: null }] }] }], units: {} }]);
  const res = await client.callTool({ name: "get_analytics_changes", arguments: { host: "example.com", days: 28 } });
  assert.equal(calls[0].url.pathname, `/v1/workspaces/${WS}/analytics/changes`);
  assert.equal(calls[0].url.searchParams.get("days"), "28");
  const text = res.content[0].text;
  assert.ok(!text.includes("\n") && !text.includes("null") && text.includes('"now":0.0346'), text);

  const resources = (await client.listResources()).resources;
  assert.ok(resources.some((r) => r.uri === "outflare://analytics/catalog"));
  const read = await client.readResource({ uri: "outflare://analytics/catalog" });
  assert.equal(calls[1].url.pathname, `/v1/workspaces/${WS}/analytics/catalog`);
  assert.equal(read.contents[0].mimeType, "application/json");

  const prompts = (await client.listPrompts()).prompts.map((p) => p.name);
  assert.deepEqual(prompts.sort(), ["explain_traffic_change", "seo_check", "set_up_outflare", "weekly_website_review", "whats_blocked"]);
  const p = await client.getPrompt({ name: "weekly_website_review", arguments: { host: "example.com" } });
  assert.ok(p.messages[0].content.text.includes("get_analytics_changes with host example.com"));

  const ro = await connect(() => [200, {}], { readOnly: true });
  const names = (await ro.client.listTools()).tools.map((t) => t.name);
  assert.ok(names.includes("get_analytics_changes") && names.includes("get_analytics_catalog"));
});

test("webhook tools: create, test and redeliver call the webhook endpoints", async () => {
  const HOOK = "44444444-4444-4444-8444-444444444444";
  const { client, calls } = await connect(() => [200, { id: HOOK, status: "delivered" }]);
  await client.callTool({ name: "create_webhook", arguments: { url: "https://example.com/h", events: ["lead.replied"] } });
  assert.equal(calls[0].url.pathname, `/v1/workspaces/${WS}/webhooks`);
  assert.deepEqual(JSON.parse(calls[0].body), { url: "https://example.com/h", events: ["lead.replied"] });
  await client.callTool({ name: "test_webhook", arguments: { webhook_id: HOOK } });
  assert.equal(calls[1].url.pathname, `/v1/workspaces/${WS}/webhooks/${HOOK}/test`);
  await client.callTool({ name: "redeliver_webhook", arguments: { webhook_id: HOOK, delivery_id: 7 } });
  assert.equal(calls[2].url.pathname, `/v1/workspaces/${WS}/webhooks/${HOOK}/deliveries/7/redeliver`);
  await client.callTool({ name: "list_webhook_deliveries", arguments: { webhook_id: HOOK, status: "failed" } });
  assert.equal(calls[3].url.search, "?status=failed");

  const ro = await connect(() => [200, {}], { readOnly: true });
  const names = (await ro.client.listTools()).tools.map((t) => t.name);
  assert.ok(names.includes("list_webhooks") && !names.includes("create_webhook"));
});

test("answers are compact JSON with structuredContent, and list_events joins types", async () => {
  const { client, calls } = await connect(() => [200, { data: [{ id: "evt_2", type: "lead.replied" }], next_after: "evt_2", has_more: false }]);
  const res = await client.callTool({ name: "list_events", arguments: { after: "evt_1", types: ["lead.replied", "mailbox.*"] } });
  assert.equal(res.content[0].text, JSON.stringify({ data: [{ id: "evt_2", type: "lead.replied" }], next_after: "evt_2", has_more: false }));
  assert.equal(res.structuredContent.next_after, "evt_2");
  assert.equal(calls[0].url.pathname, `/v1/workspaces/${WS}/events`);
  assert.equal(calls[0].url.searchParams.get("types"), "lead.replied,mailbox.*");
});

test("names the tool, the agent's app and the client on every call", async () => {
  const { client, calls } = await connect(() => [200, { data: [] }]);
  await client.callTool({ name: "list_inbox", arguments: {} });
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
    projects: [],
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
  await client.callTool({ name: "remember", arguments: { text: "Only Series A and later", kind: "decision" } });
  assert.equal(calls[0].method, "POST");
  assert.equal(calls[0].url.pathname, `/v1/workspaces/${WS}/memories`);
  assert.deepEqual(JSON.parse(calls[0].body), { text: "Only Series A and later", kind: "decision" });

  await client.callTool({ name: "update_context", arguments: { goals: "20 demos a month", version: 2 } });
  assert.equal(calls[1].method, "PATCH");
  assert.equal(calls[1].url.pathname, `/v1/workspaces/${WS}/context`);

  await client.callTool({ name: "forget", arguments: { memory_id: SEQ } });
  assert.equal(calls[2].method, "DELETE");
  assert.equal(calls[2].url.pathname, `/v1/workspaces/${WS}/memories/${SEQ}`);

  const ro = await connect(() => [200, {}], { readOnly: true });
  const names = (await ro.client.listTools()).tools.map((t) => t.name);
  assert.ok(names.includes("get_context") && names.includes("list_memories") && names.includes("get_status"));
  assert.ok(!names.includes("remember") && !names.includes("update_context") && !names.includes("forget"));
});

test("brand tools reach the brand routes, and OUTFLARE_BRAND is the list default", async () => {
  const { client, calls } = await connect(() => [200, { data: [] }], { brand: "repocket" });
  await client.callTool({ name: "list_sequences", arguments: {} });
  assert.equal(calls[0].url.search, "?brand=repocket");
  await client.callTool({ name: "list_sequences", arguments: { brand: "all" } });
  assert.equal(calls[1].url.search, "?brand=all");
  await client.callTool({ name: "update_brand_context", arguments: { brand: "geonode", voice: "Plain." } });
  assert.equal(calls[2].method, "PATCH");
  assert.equal(calls[2].url.pathname, `/v1/workspaces/${WS}/brands/geonode/context`);
  assert.deepEqual(JSON.parse(calls[2].body), { voice: "Plain." });
  await client.callTool({ name: "get_context", arguments: {} });
  assert.ok(calls.some((c) => c.url.pathname.endsWith("/context") && c.url.search === "?brand=repocket"));
});
