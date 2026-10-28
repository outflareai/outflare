// Runs site.js in a minimal browser stand-in and prints the visitor id, the
// address it left and how many times it posted the first page view.
// Usage: node run_site.mjs <site.js> <json options>. statuses lists the
// answers to each post in turn (a number, or "error" for a network error).
// With flags (the answer to /v1/site/flags) it also asks for flags, as with
// data-flags, and prints what onFlags gave and the classes on <html>;
// stored is what sessionStorage held from an earlier page. With quiet, no
// onFlags callback reads the flags; read lists flags to read with
// outflare.flag once they arrive. tracked lists the track events sent.
import { readFileSync } from "node:fs";
import vm from "node:vm";

const [file, raw] = process.argv.slice(2);
const opt = JSON.parse(raw);
let href = opt.href;
const loc = {
  get href() { return href; },
  get hostname() { return new URL(href).hostname; },
  get protocol() { return new URL(href).protocol; },
  get pathname() { return new URL(href).pathname; },
  get search() { return new URL(href).search; },
};
const attrs = { "data-key": "ofk_test_not_a_real_key", "data-consent": "granted", "data-domains": opt.domains || "" };
if (opt.flags && !opt.noTag) attrs["data-flags"] = "";
const classes = new Set(opt.htmlClass ? [opt.htmlClass] : []);
const storage = new Map(Object.entries(opt.stored || {}));
const sessionStorage = {
  getItem: (k) => (storage.has(k) ? storage.get(k) : null),
  setItem: (k, v) => storage.set(k, String(v)),
  removeItem: (k) => storage.delete(k),
};
const document = {
  currentScript: { src: "https://app.outflare.example/site.js", getAttribute: (k) => attrs[k] ?? null },
  referrer: opt.referrer || "",
  title: "",
  cookie: opt.cookie || "",
  addEventListener() {},
  documentElement: { classList: { add: (c) => classes.add(c), remove: (c) => classes.delete(c) } },
};
const history = {
  state: null,
  pushState(_, __, u) { href = new URL(u, href).toString(); },
  replaceState(_, __, u) { href = new URL(u, href).toString(); },
};
let posts = 0;
const statuses = opt.statuses || [200];
const timers = [];
let flagAsks = 0;
const tracked = [];
const fetch = (url, init) => {
  if (url.endsWith("/v1/site/events")) {
    for (const e of JSON.parse(init.body).batch) if (e.type === "track") tracked.push({ event: e.event, properties: e.properties });
  }
  if (url.endsWith("/v1/site/flags")) {
    flagAsks++;
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(opt.flags) });
  }
  const answer = statuses[Math.min(posts++, statuses.length - 1)];
  return answer === "error" ? Promise.reject(new TypeError("network")) : Promise.resolve({ status: answer });
};
const window = { addEventListener() {}, crypto: globalThis.crypto, fetch };
const ctx = vm.createContext({
  window, document, history, location: loc, URL, URLSearchParams, Intl, Date, Math, JSON, Array,
  navigator: { language: "en", sendBeacon: () => true },
  crypto: globalThis.crypto, fetch, setTimeout: (f) => timers.push(f), sessionStorage,
});
vm.runInContext(readFileSync(file, "utf8"), ctx);
let got = null;
if (opt.flags && !opt.quiet) window.outflare.onFlags((f) => { got = { flags: { ...f }, payload: window.outflare.flagPayload("hero") }; });
for (const k of opt.read || []) window.outflare.onFlags(() => window.outflare.flag(k));
// Let answers arrive and retries run, without waiting for real time.
for (let i = 0; i < 10; i++) {
  await new Promise((r) => setImmediate(r));
  while (timers.length) timers.shift()();
}
console.log(JSON.stringify({ anon: window.outflare.anonymousId(), href, posts, flagAsks, got, classes: [...classes].sort(), tracked, stored: Object.fromEntries(storage) }));
