import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createClient, decideFlag, decideFlags, flagBucket, flagDevice, LocalFlags } from "../dist/index.js";

const data = (name) => JSON.parse(readFileSync(new URL("../../../engine/internal/flags/testdata/" + name, import.meta.url), "utf8"));

test("decides every vector as the engine does", () => {
  const vectors = data("vectors.json");
  assert.ok(vectors.length > 200);
  for (const v of vectors) {
    const got = decideFlag(v.flag, v.visitor);
    assert.equal(got.variant, v.variant, v.name);
    assert.equal(got.reason, v.reason, v.name);
  }
});

test("reads a device from a user agent as the engine does", () => {
  for (const d of data("devices.json")) assert.equal(flagDevice(d.user_agent), d.device, d.user_agent);
});

test("buckets are sha256's first 8 bytes mod 10,000", () => {
  // sha256("") starts e3b0c44298fc1c14: 0xe3b0c44298fc1c14 mod 10000.
  assert.equal(flagBucket(""), Number(0xe3b0c44298fc1c14n % 10000n));
  assert.equal(flagBucket("abc"), Number(0xba7816bf8f01cfean % 10000n));
  const long = "x".repeat(200);
  assert.ok(flagBucket(long) >= 0 && flagBucket(long) < 10000);
});

const defs = {
  refresh_seconds: 30,
  flags: [
    { key: "hero", variants: [{ key: "control", weight: 50 }, { key: "faster", weight: 50, payload: { headline: "Ship faster" } }], rollout: 100, rules: [], salt: "s1", group: "", group_from: 0, group_to: 0 },
    { key: "beta", variants: [{ key: "on", weight: 100 }], rollout: 100, rules: [{ attribute: "property.plan", op: "is", values: ["pro"] }], salt: "s2", group: "", group_from: 0, group_to: 0 },
    { key: "mobile-nav", variants: [{ key: "on", weight: 100 }], rollout: 100, rules: [{ attribute: "device", op: "is", values: ["mobile"] }, { attribute: "signed_in", op: "is", values: ["true"] }], salt: "s3", group: "", group_from: 0, group_to: 0 },
  ],
};

test("decideFlags gives the shape of evaluate", () => {
  const iphone = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) Mobile/15E148";
  const out = decideFlags(defs, { anonymousId: " visitor-1 ", userId: "u1", userAgent: iphone, properties: { plan: "Pro", seats: 3, none: null } });
  assert.ok(out.flags.hero === "control" || out.flags.hero === "faster");
  if (out.flags.hero === "faster") assert.deepEqual(out.payloads.hero, { headline: "Ship faster" });
  assert.equal(out.flags.beta, "on");
  assert.equal(out.flags["mobile-nav"], "on");
  assert.deepEqual(out.reasons, { hero: "match", beta: "match", "mobile-nav": "match" });
  const anon = decideFlags(defs, { anonymousId: "visitor-1", keys: ["beta", "mobile-nav"] });
  assert.deepEqual(anon.flags, { beta: false, "mobile-nav": false });
  assert.deepEqual(anon.reasons, { beta: "rules", "mobile-nav": "rules" });
  // The same visitor always gets the same version.
  assert.equal(decideFlags(defs, { anonymousId: "visitor-1" }).flags.hero, out.flags.hero);
  assert.throws(() => decideFlags(defs, {}), /anonymousId/);
});

test("LocalFlags loads once, answers from memory and refreshes with the ETag", async () => {
  const calls = [];
  let fail = false;
  let body = defs;
  const fetch = async (req) => {
    calls.push(req);
    if (fail) return new Response(JSON.stringify({ error: { code: "internal", message: "down", request_id: "r" } }), { status: 500, headers: { "Content-Type": "application/json" } });
    if (req.headers.get("If-None-Match") === '"v1"' && body === defs) return new Response(null, { status: 304, headers: { ETag: '"v1"' } });
    return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json", ETag: body === defs ? '"v1"' : '"v2"' } });
  };
  const errors = [];
  const client = createClient({ baseUrl: "https://outflare.example.com", token: "oft_test_not_a_real_token", fetch, maxRetries: 0 });
  const flags = new LocalFlags(client, { brand: "b1", refreshSeconds: 5, onError: (e) => errors.push(e) });
  assert.equal(flags.current(), null);
  const first = await flags.decide({ anonymousId: "visitor-1" });
  assert.equal(calls.length, 1);
  assert.equal(calls[0].url, "https://outflare.example.com/v1/brands/b1/flags/definitions");
  assert.equal(calls[0].headers.get("Authorization"), "Bearer oft_test_not_a_real_token");
  await flags.decide({ anonymousId: "visitor-2" });
  assert.equal(calls.length, 1, "fresh definitions cost no request");
  // One flag at a time, from the same memory.
  const who = { anonymousId: "visitor-1" };
  assert.equal(await flags.variant("hero", who), first.flags.hero);
  assert.equal(await flags.isEnabled("hero", who), true);
  assert.deepEqual(await flags.payload("hero", who), first.payloads.hero);
  assert.equal(await flags.variant("no-such-flag", who), false);
  assert.equal(await flags.isEnabled("no-such-flag", who), false);
  assert.equal(await flags.payload("no-such-flag", who), undefined);
  assert.equal(calls.length, 1);

  // Unchanged: 304 keeps what is in memory.
  await flags.refresh();
  assert.equal(calls[1].headers.get("If-None-Match"), '"v1"');
  assert.equal((await flags.decide({ anonymousId: "visitor-1" })).flags.hero, first.flags.hero);

  // A failed refresh keeps deciding with the last definitions.
  fail = true;
  await assert.rejects(flags.refresh());
  assert.equal((await flags.decide({ anonymousId: "visitor-1" })).flags.hero, first.flags.hero);
  fail = false;

  // A changed flag arrives with the next refresh.
  body = { refresh_seconds: 30, flags: [] };
  await flags.refresh();
  assert.deepEqual((await flags.decide({ anonymousId: "visitor-1" })).flags, {});
});

test("LocalFlags fails the first decide when the definitions can't be read", async () => {
  const fetch = async () => new Response("{}", { status: 403, headers: { "Content-Type": "application/json" } });
  const flags = new LocalFlags(createClient({ baseUrl: "https://outflare.example.com", token: "oft_test_not_a_real_token", fetch, maxRetries: 0 }), { brand: "b1" });
  await assert.rejects(flags.decide({ anonymousId: "v" }), /403/);
});
