import { test } from "node:test";
import assert from "node:assert/strict";
import { createClient, isHeld, ok, OutflareError, paginate } from "../dist/index.js";

// A fetch that records the request and answers with one canned response.
function fakeFetch(status, body) {
  const calls = [];
  const fetch = async (req) => {
    calls.push(req);
    return new Response(body === undefined ? null : JSON.stringify(body), {
      status,
      headers: { "Content-Type": "application/json", "X-Request-Id": "req-1" },
    });
  };
  return { fetch, calls };
}

test("sends the token and builds the path", async () => {
  const { fetch, calls } = fakeFetch(200, { data: [] });
  const gtm = createClient({ baseUrl: "https://outflare.example.com/", token: "oft_abc", fetch });
  const res = ok(await gtm.GET("/v1/workspaces/{ws}/mailboxes", { params: { path: { ws: "w1" }, query: { status: "active" } } }));
  assert.deepEqual(res, { data: [] });
  assert.equal(calls[0].url, "https://outflare.example.com/v1/workspaces/w1/mailboxes?status=active");
  assert.equal(calls[0].headers.get("Authorization"), "Bearer oft_abc");
});

test("ok throws the API error", async () => {
  const { fetch } = fakeFetch(409, { error: { code: "conflict", message: "remove the mailboxes first", request_id: "req-9" } });
  const gtm = createClient({ token: "oft_abc", baseUrl: "http://x", fetch });
  const res = await gtm.DELETE("/v1/workspaces/{ws}/oauth-connections/{id}", { params: { path: { ws: "w", id: "c" } } });
  assert.throws(() => ok(res), (e) => e instanceof OutflareError && e.status === 409 && e.code === "conflict" && e.requestId === "req-9" && e.message === "remove the mailboxes first");
});

test("ok passes an empty success through", async () => {
  const { fetch } = fakeFetch(204);
  const gtm = createClient({ token: "t", baseUrl: "http://x", fetch });
  assert.equal(ok(await gtm.DELETE("/v1/workspaces/{ws}/mailboxes/{id}", { params: { path: { ws: "w", id: "m" } } })), undefined);
});

test("isHeld spots a write a mission held", async () => {
  const held = { mission_action: { id: "a1" }, dry_run: false, approval_required: true, message: "Held for approval" };
  const { fetch, calls } = fakeFetch(202, held);
  const gtm = createClient({ token: "t", baseUrl: "http://x", fetch, mission: "m1" });
  const data = ok(await gtm.POST("/v1/workspaces/{ws}/leads", { params: { path: { ws: "w" } }, body: { email: "a@b-co.example" } }));
  assert.equal(calls[0].headers.get("Outflare-Mission"), "m1");
  assert.ok(isHeld(data));
  assert.ok(!isHeld({ id: "l1", email: "a@b-co.example" }));
  assert.ok(!isHeld(undefined));
});

test("verifyWebhook checks Standard Webhooks signatures", async () => {
  const { verifyWebhook, WebhookVerificationError } = await import("../dist/index.js");
  const { createHmac } = await import("node:crypto");
  const key = Buffer.from("0123456789abcdef0123456789abcdef");
  const secret = "whsec_" + key.toString("base64");
  const body = JSON.stringify({ id: "evt_1", type: "lead.replied", data: {} });
  const ts = String(Math.floor(Date.now() / 1000));
  const sig = createHmac("sha256", key).update(`evt_1.${ts}.${body}`).digest("base64");
  const headers = { "Webhook-Id": "evt_1", "webhook-timestamp": ts, "webhook-signature": `v1,bm90IGl0 v1,${sig}` };

  const event = await verifyWebhook(body, headers, secret);
  assert.equal(event.type, "lead.replied");
  assert.equal((await verifyWebhook(body, new Headers(headers), secret)).id, "evt_1");
  await assert.rejects(verifyWebhook(body + " ", headers, secret), WebhookVerificationError);
  await assert.rejects(verifyWebhook(body, headers, "whsec_" + Buffer.from("other").toString("base64")), WebhookVerificationError);
  await assert.rejects(verifyWebhook(body, headers, secret, { now: new Date(Date.now() + 600_000) }), /too far/);
});

test("retries a busy server with the same idempotency key, then answers", async () => {
  const seen = [];
  const answers = [503, 429, 201];
  const fetch = async (req) => {
    seen.push({ key: req.headers.get("Idempotency-Key"), body: await req.text() });
    const status = answers.shift();
    return new Response(JSON.stringify(status === 201 ? { id: "l1" } : { error: { code: "busy", message: "busy" } }), {
      status,
      headers: { "Content-Type": "application/json", "Retry-After": "0" },
    });
  };
  const gtm = createClient({ token: "t", baseUrl: "http://x", fetch });
  const lead = ok(await gtm.POST("/v1/workspaces/{ws}/leads", { params: { path: { ws: "w" } }, body: { email: "ana@acme-com.example" } }));
  assert.equal(lead.id, "l1");
  assert.equal(seen.length, 3);
  assert.ok(seen[0].key && seen.every((s) => s.key === seen[0].key), "one key across retries");
  assert.ok(seen.every((s) => s.body.includes("ana@acme-com.example")), "the body is sent on every attempt");
});

test("gives up after maxRetries and returns the last answer", async () => {
  let calls = 0;
  const fetch = async () => {
    calls++;
    return new Response(JSON.stringify({ error: { code: "rate_limited", message: "slow down" } }), { status: 429, headers: { "Content-Type": "application/json", "Retry-After": "0" } });
  };
  const gtm = createClient({ token: "t", baseUrl: "http://x", fetch, maxRetries: 1 });
  const res = await gtm.GET("/v1/workspaces/{ws}/mailboxes", { params: { path: { ws: "w" } } });
  assert.equal(calls, 2);
  assert.throws(() => ok(res), (e) => e.status === 429 && e.code === "rate_limited");
});

test("a GET carries no idempotency key", async () => {
  const { fetch, calls } = fakeFetch(200, { data: [] });
  const gtm = createClient({ token: "t", baseUrl: "http://x", fetch });
  await gtm.GET("/v1/workspaces/{ws}/mailboxes", { params: { path: { ws: "w" } } });
  assert.equal(calls[0].headers.get("Idempotency-Key"), null);
});

test("paginate follows next_cursor", async () => {
  const pages = { "": { data: [{ id: "a" }, { id: "b" }], next_cursor: "c2" }, c2: { data: [{ id: "c" }], next_cursor: null } };
  const fetch = async (req) => {
    const cursor = new URL(req.url).searchParams.get("cursor") ?? "";
    return new Response(JSON.stringify(pages[cursor]), { status: 200, headers: { "Content-Type": "application/json" } });
  };
  const gtm = createClient({ token: "t", baseUrl: "http://x", fetch });
  const ids = [];
  for await (const lead of paginate((cursor) => gtm.GET("/v1/workspaces/{ws}/leads", { params: { path: { ws: "w" }, query: { cursor } } }))) {
    ids.push(lead.id);
  }
  assert.deepEqual(ids, ["a", "b", "c"]);
});

test("flagsBootstrapTag hands decisions to the page, escaped", async () => {
  const { flagsBootstrapTag } = await import("../dist/index.js");
  const tag = flagsBootstrapTag(
    { flags: { hero: "faster", chat: false }, payloads: { hero: { headline: "</script><b>Ship & go</b>" } } },
    "srv-4f1c2a9e",
  );
  assert.ok(tag.startsWith('<script type="application/json" id="outflare-flags">'));
  const inner = tag.slice(tag.indexOf(">") + 1, tag.lastIndexOf("</script>"));
  assert.ok(!/[<>&]/.test(inner), inner);
  assert.deepEqual(JSON.parse(inner), {
    anonymousId: "srv-4f1c2a9e",
    flags: { hero: "faster", chat: false },
    payloads: { hero: { headline: "</script><b>Ship & go</b>" } },
  });
});
