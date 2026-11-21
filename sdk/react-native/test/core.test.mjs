import assert from "node:assert/strict";
import { test } from "node:test";
import { Outflare, userAgent, version } from "../dist/core.js";

function fake(answers = []) {
  const calls = [];
  const fetch = async (url, init) => {
    calls.push({ url, headers: init.headers, body: JSON.parse(init.body) });
    const status = answers.shift() ?? 200;
    if (status === 0) throw new Error("network down");
    return new Response("{}", { status });
  };
  return { calls, fetch };
}

function memory() {
  const m = new Map();
  return { m, getItem: async (k) => m.get(k) ?? null, setItem: async (k, v) => void m.set(k, v), removeItem: async (k) => void m.delete(k) };
}

const base = { key: "ofk_test_not_a_real_key", host: "https://outflare.example/", flushInterval: 60_000, os: { name: "iOS", version: "18.1" } };

test("screens and events go to the site route with the key and one visitor id", async () => {
  const f = fake();
  const o = new Outflare({ ...base, fetch: f.fetch, app: { name: "Northpeak", version: "2.4.0" } });
  o.screen("Checkout");
  o.track("Order Started", { items: 2 });
  assert.equal(f.calls.length, 0);
  await o.flush();
  assert.equal(f.calls.length, 1);
  const { url, headers, body } = f.calls[0];
  assert.equal(url, "https://outflare.example/v1/site/events");
  assert.equal(headers["User-Agent"], `outflare-react-native/${version} (iPhone; CPU iPhone OS 18_1 like Mac OS X)`);
  assert.equal(body.key, "ofk_test_not_a_real_key");
  assert.deepEqual(body.batch.map((e) => e.type), ["screen", "track"]);
  assert.equal(body.batch[0].name, "Checkout");
  assert.equal(body.batch[1].event, "Order Started");
  assert.equal(body.batch[0].anonymousId, await o.anonymousId());
  assert.equal(body.batch[0].anonymousId, body.batch[1].anonymousId);
  assert.notEqual(body.batch[0].messageId, body.batch[1].messageId);
  assert.equal(body.batch[0].context.app.version, "2.4.0");
  assert.equal(body.batch[0].context.library.name, "outflare-react-native");
});

test("at most 20 events go in one request", async () => {
  const f = fake();
  const o = new Outflare({ ...base, fetch: f.fetch, flushAt: 20 });
  for (let i = 0; i < 45; i++) o.track("Tapped");
  await o.flush();
  assert.deepEqual(f.calls.map((c) => c.body.batch.length), [20, 20, 5]);
});

test("events wait through no answer and a busy server, and are dropped on a refusal", async () => {
  const f = fake([0, 503, 200, 401]);
  const lost = [];
  const o = new Outflare({ ...base, fetch: f.fetch, onError: (_e, n) => lost.push(n) });
  o.track("Kept");
  await o.flush();
  await o.flush();
  assert.equal(f.calls.length, 2);
  await o.flush();
  assert.equal(f.calls.length, 3);
  // The same message id each time, so a retry is stored once.
  assert.equal(f.calls[0].body.batch[0].messageId, f.calls[2].body.batch[0].messageId);
  await o.flush();
  assert.equal(f.calls.length, 3);
  o.track("Refused");
  await o.flush();
  assert.deepEqual(lost, [1]);
  await o.flush();
  assert.equal(f.calls.length, 4);
});

test("the visitor id and waiting events survive a restart; reset starts a new visitor", async () => {
  const storage = memory();
  const down = fake([0]);
  const first = new Outflare({ ...base, storage, fetch: down.fetch });
  const id = await first.anonymousId();
  first.screen("Home");
  await first.flush();
  assert.equal(JSON.parse(storage.m.get("outflare.queue")).length, 1);

  const up = fake();
  const second = new Outflare({ ...base, storage, fetch: up.fetch });
  assert.equal(await second.anonymousId(), id);
  await second.flush();
  assert.equal(up.calls[0].body.batch[0].name, "Home");
  assert.equal(storage.m.has("outflare.queue"), false);

  await second.reset();
  assert.notEqual(await second.anonymousId(), id);
  assert.equal(storage.m.get("outflare.anonymousId"), await second.anonymousId());
});

test("the user agent names the phone the way Outflare reads it", () => {
  assert.equal(userAgent("outflare-react-native", { name: "Android", version: "15" }, "Pixel 9"), `outflare-react-native/${version} (Linux; Android 15; Pixel 9) Mobile`);
  assert.equal(userAgent("outflare-react-native", { name: "iPadOS", version: "18.1" }), `outflare-react-native/${version} (iPad; CPU iPhone OS 18_1 like Mac OS X)`);
});
