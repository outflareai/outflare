// Runs site.js in a minimal browser stand-in and prints the visitor id, the
// address it left and how many times it posted the first page view.
// Usage: node run_site.mjs <site.js> <json options>. statuses lists the
// answers to each post in turn (a number, or "error" for a network error).
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
const document = {
  currentScript: { src: "https://app.outflare.example/site.js", getAttribute: (k) => attrs[k] ?? null },
  referrer: opt.referrer || "",
  title: "",
  cookie: "",
  addEventListener() {},
};
const history = {
  state: null,
  pushState(_, __, u) { href = new URL(u, href).toString(); },
  replaceState(_, __, u) { href = new URL(u, href).toString(); },
};
let posts = 0;
const statuses = opt.statuses || [200];
const timers = [];
const fetch = () => {
  const answer = statuses[Math.min(posts++, statuses.length - 1)];
  return answer === "error" ? Promise.reject(new TypeError("network")) : Promise.resolve({ status: answer });
};
const window = { addEventListener() {}, crypto: globalThis.crypto, fetch };
const ctx = vm.createContext({
  window, document, history, location: loc, URL, URLSearchParams, Intl, Date, Math, JSON, Array,
  navigator: { language: "en", sendBeacon: () => true },
  crypto: globalThis.crypto, fetch, setTimeout: (f) => timers.push(f),
});
vm.runInContext(readFileSync(file, "utf8"), ctx);
// Let answers arrive and retries run, without waiting for real time.
for (let i = 0; i < 10; i++) {
  await new Promise((r) => setImmediate(r));
  while (timers.length) timers.shift()();
}
console.log(JSON.stringify({ anon: window.outflare.anonymousId(), href, posts }));
