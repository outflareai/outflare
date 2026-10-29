// Runs site.js in a minimal browser stand-in and prints the visitor id, the
// address it left and how many times it posted the first page view.
// Usage: node run_site.mjs <site.js> <json options>. statuses lists the
// answers to each post in turn (a number, or "error" for a network error).
// With flags (the answer to /v1/site/flags) it also asks for flags, as with
// data-flags, and prints what onFlags gave and the classes on <html>;
// stored is what sessionStorage held from an earlier page.
// banner is the answer to GET /v1/site/consent (the consent banner's
// settings); without it the site has none. cookie is document.cookie from
// an earlier page, gpc the browser's Global Privacy Control, calls are
// outflare methods to call once the page loaded ([name, ...args]), and
// held are scripts kept back for consent (data-category). It also prints
// the choices sent, Consent Mode's dataLayer, Microsoft's uetq, the calls
// Meta's fbq got, the scripts let through and the cookies left. With
// loaded the page has finished loading; local is localStorage, tags the
// page's scripts and iframes ({tag, src}) and policySpots how many
// data-outflare-cookie-policy elements it has. It prints the cookie
// report sent and what the policy elements were filled with.
// With quiet, no onFlags callback reads the flags; read lists flags to read
// with outflare.flag once they arrive. tracked lists the track events sent.
// cookies (an object) adds cookies to cookie, consent is the data-consent
// mode (granted by default), track an event to send after the page view,
// and bodies lists what each post sent. bootstrap is the JSON (an object,
// or a string as written) of a <script id="outflare-flags"> the site's
// server put in the page.
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
const attrs = { "data-key": "ofk_test_not_a_real_key", "data-consent": opt.consent || "granted", "data-domains": opt.domains || "" };
if (opt.flags && !opt.noTag) attrs["data-flags"] = "";
if (opt.bannerOff) attrs["data-banner"] = "off";
if (opt.cookiesOff) attrs["data-cookies"] = "off";
const classes = new Set(opt.htmlClass ? [opt.htmlClass] : []);
const storage = new Map(Object.entries(opt.stored || {}));
const sessionStorage = {
  getItem: (k) => (storage.has(k) ? storage.get(k) : null),
  setItem: (k, v) => storage.set(k, String(v)),
  removeItem: (k) => storage.delete(k),
};
// A cookie jar that behaves like document.cookie: setting one name keeps
// the others, and max-age=0 removes it. Domains are ignored.
const jar = new Map((opt.cookie || "").split(/;\s*/).filter(Boolean).map((c) => c.split("=")));
for (const [k, v] of Object.entries(opt.cookies || {})) jar.set(k, v);
const ran = [];
const held = (opt.held || []).map((h) => {
  const a = { type: "text/plain", "data-category": h.category, ...(h.src ? { src: h.src } : {}) };
  const el = {
    tagName: "SCRIPT", text: h.text || "", src: h.src || "",
    get attributes() { return Object.entries(a).map(([name, value]) => ({ name, value })); },
    getAttribute: (k) => a[k] ?? null,
    setAttribute: (k, v) => { a[k] = v; },
    parentNode: { replaceChild: (n) => ran.push(n.src || n.text) },
  };
  return el;
});
const local = new Map(Object.entries(opt.local || {}));
const localStorage = {
  getItem: (k) => (local.has(k) ? local.get(k) : null),
  setItem: (k, v) => local.set(k, String(v)),
  removeItem: (k) => local.delete(k),
  key: (i) => [...local.keys()][i] ?? null,
  get length() { return local.size; },
};
const tags = (opt.tags || []).map((t) => ({ tagName: t.tag.toUpperCase(), getAttribute: (k) => (k === "src" ? t.src : null) }));
// Elements the visual editor's changes find, by selector.
const elements = Object.fromEntries(Object.entries(opt.elements || {}).map(([sel, n]) => [sel, Array.from({ length: n }, () => {
  const e = { textContent: "old", attrs: {}, display: "", setAttribute(k, v) { e.attrs[k] = v; }, style: { setProperty(k, v) { e.display = v; } } };
  return e;
})]));
const spots = Array.from({ length: opt.policySpots || 0 }, () => ({ innerHTML: "", querySelector: () => null }));
const document = {
  readyState: opt.loaded ? "complete" : "loading",
  currentScript: { src: "https://app.outflare.example/site.js", getAttribute: (k) => attrs[k] ?? null },
  referrer: opt.referrer || "",
  title: "",
  get cookie() { return [...jar].map(([k, v]) => k + "=" + v).join("; "); },
  set cookie(c) {
    const [pair, ...rest] = c.split(";");
    const [k, v] = pair.split("=");
    if (rest.some((r) => r.trim() === "max-age=0")) jar.delete(k.trim());
    else jar.set(k.trim(), v);
  },
  addEventListener() {},
  getElementById: (id) =>
    id === "outflare-flags" && opt.bootstrap !== undefined
      ? { textContent: typeof opt.bootstrap === "string" ? opt.bootstrap : JSON.stringify(opt.bootstrap) }
      : null,
  querySelectorAll: (q) =>
    q.includes("data-category") ? held.filter((h) => h.getAttribute("type") === "text/plain" && h.getAttribute("data-of-loaded") === null)
    : q.includes("cookie-policy") ? spots
    : q.startsWith("script[src]") ? tags
    : elements[q] ? elements[q]
    : [],
  createElement: () => {
    const n = { attrs: {}, setAttribute(k, v) { n.attrs[k] = v; } };
    return n;
  },
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
const choices = [];
let bannerAsks = 0;
const cookieReports = [];
const tracked = [];
const bodies = [];
const fetch = (url, init) => {
  if (url.includes("/v1/site/cookie-policy")) {
    return Promise.resolve({ ok: true, status: 200, text: () => Promise.resolve("<div>policy</div>") });
  }
  if (url.endsWith("/v1/site/cookies")) {
    cookieReports.push(JSON.parse(init.body));
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ added: 0 }) });
  }
  if (url.includes("/v1/site/consent")) {
    if (init && init.method === "POST") {
      choices.push(JSON.parse(init.body));
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) });
    }
    bannerAsks++;
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(opt.banner || { enabled: false }) });
  }
  if (url.endsWith("/v1/site/events")) {
    const body = JSON.parse(init.body);
    bodies.push(body);
    for (const e of body.batch) if (e.type === "track") tracked.push({ event: e.event, properties: e.properties });
  }
  if (url.endsWith("/v1/site/flags")) {
    flagAsks++;
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(opt.flags) });
  }
  const answer = statuses[Math.min(posts++, statuses.length - 1)];
  return answer === "error" ? Promise.reject(new TypeError("network")) : Promise.resolve({ status: answer });
};
const fbq = [];
const window = { addEventListener() {}, crypto: globalThis.crypto, fetch };
if (opt.fbq) window.fbq = (...a) => fbq.push(a);
const ctx = vm.createContext({
  window, document, history, location: loc, URL, URLSearchParams, Intl, Date, Math, JSON, Array,
  navigator: { language: opt.language || "en", sendBeacon: () => true, globalPrivacyControl: !!opt.gpc },
  crypto: globalThis.crypto, fetch, setTimeout: (f) => timers.push(f), sessionStorage, localStorage,
});
vm.runInContext(readFileSync(file, "utf8"), ctx);
// Let the banner's settings arrive before acting like the visitor.
for (let i = 0; i < 5; i++) await new Promise((r) => setImmediate(r));
let state = null;
for (const [name, ...args] of opt.calls || []) state = window.outflare[name](...args);
if (opt.track) window.outflare.track(opt.track, { plan: "pro" });
let got = null;
if (opt.flags && !opt.quiet) window.outflare.onFlags((f) => { got = { flags: { ...f }, payload: window.outflare.flagPayload("hero") }; });
for (const k of opt.read || []) window.outflare.onFlags(() => window.outflare.flag(k));
// Let answers arrive and retries run, without waiting for real time.
for (let i = 0; i < 10; i++) {
  await new Promise((r) => setImmediate(r));
  while (timers.length) timers.shift()();
}
console.log(JSON.stringify({ anon: window.outflare.anonymousId(), href, posts, flagAsks, got, classes: [...classes].sort(), tracked, stored: Object.fromEntries(storage),
  choices, bannerAsks, cookieReports, policy: spots.map((s) => s.innerHTML), local: Object.fromEntries(local), state, consent: window.outflare.consent(), dataLayer: (window.dataLayer || []).map((a) => [...a]), uetq: window.uetq || null, fbq, ran,
  cookies: Object.fromEntries(jar), bodies,
  elements: Object.fromEntries(Object.entries(elements).map(([k, els]) => [k, els.map((e) => ({ text: e.textContent, attrs: e.attrs, display: e.display }))])) }));
