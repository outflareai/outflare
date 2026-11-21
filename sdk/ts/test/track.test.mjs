import assert from "node:assert/strict";
import { test } from "node:test";
import { Tracker, errorHash, looksLikeCrawler } from "../dist/index.js";

function fake(answers = []) {
  const calls = [];
  const fetch = async (url, init) => {
    calls.push({ url, auth: init.headers.Authorization, key: init.headers["Idempotency-Key"], body: JSON.parse(init.body) });
    const status = answers.shift() ?? 202;
    if (status === 0) throw new Error("network down");
    return new Response("{}", { status });
  };
  return { calls, fetch };
}

test("events are queued, batched and sent with the token", async () => {
  const f = fake();
  const t = new Tracker({ token: "oft_test_not_a_real_token", brand: "northpeak", baseUrl: "https://outflare.example/", fetch: f.fetch, flushInterval: 60_000 });
  const id = t.track({ userId: "u1", event: "Order Completed", properties: { total: 42 } });
  t.identify({ userId: "u1", traits: { plan: "pro" } });
  t.group({ userId: "u1", groupId: "org_7" });
  t.page({ anonymousId: "a1", name: "Pricing" });
  t.screen({ anonymousId: "a1", name: "Home" });
  t.alias({ userId: "u1", previousId: "a1" });
  assert.equal(f.calls.length, 0);
  await t.flush();
  assert.equal(f.calls.length, 1);
  const { url, auth, body } = f.calls[0];
  assert.equal(url, "https://outflare.example/v1/brands/northpeak/events");
  assert.equal(auth, "Bearer oft_test_not_a_real_token");
  assert.equal(body.source, "node");
  assert.deepEqual(body.batch.map((m) => m.type), ["track", "identify", "group", "page", "screen", "alias"]);
  assert.equal(body.batch[0].messageId, id);
  assert.equal(body.batch[0].event, "Order Completed");
  assert.ok(body.batch[0].timestamp && body.batch[0].context.library.name === "outflare-node");
});

test("flushAt sends without waiting", async () => {
  const f = fake();
  const t = new Tracker({ token: "oft_test_not_a_real_token", brand: "b", fetch: f.fetch, flushAt: 2, flushInterval: 60_000 });
  t.track({ userId: "u1", event: "A" });
  t.track({ userId: "u1", event: "B" });
  await t.close();
  assert.equal(f.calls.length, 1);
  assert.equal(f.calls[0].body.batch.length, 2);
  assert.throws(() => t.track({ userId: "u1", event: "C" }), /closed/);
});

test("a busy server is retried; a refusal is not", async () => {
  const f = fake([503, 0, 202, 401]);
  const lost = [];
  const t = new Tracker({ token: "oft_test_not_a_real_token", brand: "b", fetch: f.fetch, flushInterval: 60_000, onError: (e, n) => lost.push([e.message, n]) });
  t.track({ userId: "u1", event: "A" });
  await t.flush();
  assert.equal(f.calls.length, 3);
  assert.equal(lost.length, 0);
  t.track({ userId: "u1", event: "B" });
  await t.flush();
  assert.equal(f.calls.length, 4);
  assert.equal(lost.length, 1);
  assert.match(lost[0][0], /refused the batch \(401\)/);
  assert.equal(lost[0][1], 1);
});

test("calls say what is missing", () => {
  const t = new Tracker({ token: "oft_test_not_a_real_token", brand: "b", fetch: fake().fetch });
  assert.throws(() => t.track({ event: "A" }), /userId or an anonymousId/);
  assert.throws(() => t.track({ userId: "u1" }), /event name/);
  assert.throws(() => t.identify({ anonymousId: "a1" }), /userId/);
  assert.throws(() => new Tracker({ brand: "b" }), /token/);
});

test("exposed sends Experiment Viewed only for a version", async () => {
  const f = fake();
  const t = new Tracker({ token: "oft_test_not_a_real_token", brand: "b", fetch: f.fetch, flushInterval: 60_000 });
  assert.equal(t.exposed("pricing-headline", false, { anonymousId: "a1" }), undefined);
  assert.equal(t.exposed("pricing-headline", "", { anonymousId: "a1" }), undefined);
  assert.ok(t.exposed("pricing-headline", "faster", { anonymousId: "a1" }));
  assert.throws(() => t.exposed("", "faster", { anonymousId: "a1" }), /flag/);
  await t.close();
  const [m] = f.calls[0].body.batch;
  assert.equal(f.calls[0].body.batch.length, 1);
  assert.equal(m.event, "Experiment Viewed");
  assert.deepEqual(m.properties, { flag: "pricing-headline", variant: "faster", source: "server" });
  assert.equal(m.anonymousId, "a1");
});

test("captureException sends Error Caught without addresses or keys", async () => {
  const f = fake();
  const t = new Tracker({ token: "oft_test_not_a_real_token", brand: "b", fetch: f.fetch, flushInterval: 60_000 });
  const boom = (n) => new TypeError(`order ${n} for ana@acme.example failed with sk_test_not_a_real_key_0000000000000000`);
  t.captureException(boom(41), { userId: "u1", properties: { route: "/checkout", type: "mine" } });
  t.captureException(boom(42));
  t.captureException("plain text", { anonymousId: "a1", handled: false });
  await t.close();
  const [a, b, c] = f.calls[0].body.batch;
  assert.equal(a.event, "Error Caught");
  assert.equal(a.userId, "u1");
  assert.equal(a.properties.type, "TypeError");
  assert.equal(a.properties.message, "order 41 for [email] failed with [key]");
  assert.equal(a.properties.route, "/checkout");
  assert.equal(a.properties.handled, true);
  assert.ok(a.properties.stack.includes("track.test.mjs") && !a.properties.stack.includes("acme.example"));
  // The same bug with another number is the same issue; no one is "server".
  assert.equal(a.properties.fingerprint, b.properties.fingerprint);
  assert.match(a.properties.fingerprint, /^[0-9a-f]{16}$/);
  assert.equal(b.anonymousId, "server");
  assert.deepEqual([c.properties.type, c.properties.message, c.properties.handled, c.properties.stack], ["Error", "plain text", false, undefined]);
  // The hash the website script and the Python library use.
  assert.equal(errorHash("TypeError|order 0 failed|"), "7cb8aa38c34b101a");
});

test("crawlerVisit counts crawlers per host and sends counts, never browsers", async () => {
  const f = fake();
  const t = new Tracker({ token: "oft_test_not_a_real_token", brand: "b", fetch: f.fetch, flushInterval: 60_000 });
  const gpt = "Mozilla/5.0 (compatible; GPTBot/1.2; +https://openai.com/gptbot)";
  assert.equal(t.crawlerVisit({ userAgent: gpt, host: "WWW.example.com" }), true);
  assert.equal(t.crawlerVisit({ userAgent: gpt, host: "www.example.com" }), true);
  assert.equal(t.crawlerVisit({ userAgent: "Mozilla/5.0 ChatGPT-User/1.0", host: "docs.example.com" }), true);
  assert.equal(t.crawlerVisit({ userAgent: "Mozilla/5.0 (Macintosh) Chrome/130 Safari/537.36", host: "www.example.com" }), false);
  assert.equal(t.crawlerVisit({ userAgent: gpt, host: "" }), false);
  assert.equal(t.crawlerVisit({ userAgent: null, host: "www.example.com" }), false);
  assert.equal(f.calls.length, 0);
  await t.close();
  assert.equal(f.calls.length, 1);
  assert.match(f.calls[0].url, /\/v1\/brands\/b\/ai-bot-hits$/);
  assert.equal(f.calls[0].auth, "Bearer oft_test_not_a_real_token");
  assert.ok(f.calls[0].key);
  assert.deepEqual(f.calls[0].body.hits, [
    { host: "www.example.com", user_agent: gpt, requests: 2 },
    { host: "docs.example.com", user_agent: "Mozilla/5.0 ChatGPT-User/1.0", requests: 1 },
  ]);
  assert.equal(t.crawlerVisit({ userAgent: gpt, host: "www.example.com" }), false);
  for (const ua of ["Amzn-User/1.0", "Google-Extended", "meta-webindexer/1.1", "Claude-User/1.0", "PerplexityBot/1.0", "CCBot/2.0", "Applebot-Extended"]) {
    assert.equal(looksLikeCrawler(ua), true, ua);
  }
});
