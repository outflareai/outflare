// Runs site.js on a tiny page to check the visual editor's changes: the
// version comes from the server's bootstrap tag, so nothing is fetched.
// Usage: node run_changes.mjs <site.js> <json options>. page is a tree of
// {tag, sel: [selectors it matches], text, attrs, children}; payload is the
// version's payload (flag "hero", version "b"); href the page's address;
// width the screen width; domains the tag's data-domains. It prints the
// page after the changes, the styles added to <head>, where the page was
// sent (redirect) and the track events.
import { readFileSync } from "node:fs";
import vm from "node:vm";

const [file, raw] = process.argv.slice(2);
const opt = JSON.parse(raw);
let href = opt.href || "https://northpeak.example/pricing";
let redirect = null;

class Node {
  constructor(tag, text) { this.tagName = tag ? tag.toUpperCase() : "#text"; this.childNodes = []; this.parentNode = null; this.attrs = {}; this.sel = []; this.styles = {}; this._text = text || ""; this.id = ""; this.className = "";
    this.style = { setProperty: (k, v) => { this.styles[k] = v; } };
    this.classList = { add() {}, remove() {} }; }
  get nodeType() { return this.tagName === "#text" ? 3 : 1; }
  get children() { return this.childNodes.filter((c) => c.nodeType === 1); }
  get firstChild() { return this.childNodes[0] || null; }
  get firstElementChild() { return this.children[0] || null; }
  get lastElementChild() { const c = this.children; return c[c.length - 1] || null; }
  get nextSibling() { const s = this.parentNode && this.parentNode.childNodes; return s ? s[s.indexOf(this) + 1] || null : null; }
  get nextElementSibling() { const s = this.parentNode && this.parentNode.children; return s ? s[s.indexOf(this) + 1] || null : null; }
  get textContent() { return this.tagName === "#text" ? this._text : this._text + this.childNodes.map((c) => c.textContent).join(""); }
  set textContent(v) {
    if (this.tagName === "#text") { this._text = String(v); return; }
    this.childNodes.forEach((c) => (c.parentNode = null));
    this.childNodes = [];
    if (String(v)) this.appendChild(new Node(null, String(v)));
  }
  set innerHTML(_) { throw new Error("innerHTML used"); }
  setAttribute(k, v) { this.attrs[k] = String(v); if (k === "id") this.id = String(v); }
  getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }
  appendChild(n) { return this.insertBefore(n, null); }
  insertBefore(n, ref) {
    if (n.parentNode) n.parentNode.removeChild(n);
    const i = ref ? this.childNodes.indexOf(ref) : -1;
    if (i < 0) this.childNodes.push(n); else this.childNodes.splice(i, 0, n);
    n.parentNode = this;
    return n;
  }
  removeChild(n) { this.childNodes = this.childNodes.filter((c) => c !== n); n.parentNode = null; return n; }
  contains(n) { for (let x = n; x; x = x.parentNode) if (x === this) return true; return false; }
  all() { return [this, ...this.childNodes.flatMap((c) => c.all())]; }
  dump() {
    if (this.nodeType === 3) return this._text;
    const o = { tag: this.tagName.toLowerCase() };
    if (this.sel.length) o.sel = this.sel[0];
    if (Object.keys(this.attrs).length) o.attrs = this.attrs;
    if (Object.keys(this.styles).length) o.style = this.styles;
    if (this.childNodes.length === 1 && this.childNodes[0].nodeType === 3) o.text = this.childNodes[0]._text;
    else if (this.childNodes.length) o.children = this.childNodes.map((c) => c.dump());
    return o;
  }
}
function build(spec) {
  const n = new Node(spec.tag || "div");
  n.sel = spec.sel || [];
  if (spec.text) n.appendChild(new Node(null, spec.text));
  for (const [k, v] of Object.entries(spec.attrs || {})) n.setAttribute(k, v);
  for (const c of spec.children || []) n.appendChild(build(c));
  return n;
}
const body = build({ tag: "body", children: opt.page || [] });
const head = new Node("head");
const boot = { anonymousId: "visitor-12345678", flags: { hero: "b" }, payloads: { hero: opt.payload } };
const document = {
  readyState: "complete",
  currentScript: { src: "https://app.outflare.example/site.js", getAttribute: (k) => ({ "data-key": "ofk_test_not_a_real_key", "data-consent": "granted", "data-domains": opt.domains || "", "data-banner": "off", "data-cookies": "off" })[k] ?? null },
  referrer: "", title: "", cookie: "", body, head,
  documentElement: { classList: { add() {}, remove() {} } },
  addEventListener() {},
  createElement: (t) => new Node(t),
  createTextNode: (t) => new Node(null, t),
  getElementById: (id) => (id === "outflare-flags" ? { textContent: JSON.stringify(boot) } : head.all().concat(body.all()).find((n) => n.id === id) || null),
  querySelectorAll: (q) => {
    const m = /^\[data-of-insert='(.+)'\]$/.exec(q);
    if (m) return body.all().filter((n) => n.getAttribute && n.getAttribute("data-of-insert") === m[1]);
    if (q.includes("[[")) throw new SyntaxError("bad selector");
    return body.all().filter((n) => n.sel.includes(q));
  },
  querySelector: (q) => document.querySelectorAll(q)[0] || null,
};
const loc = {
  get href() { return href; }, get host() { return new URL(href).host; }, get hostname() { return new URL(href).hostname; },
  get protocol() { return new URL(href).protocol; }, get pathname() { return new URL(href).pathname; }, get search() { return new URL(href).search; },
  replace(u) { redirect = u; },
};
const tracked = [];
const fetch = (url, init) => {
  if (url.endsWith("/v1/site/events")) for (const e of JSON.parse(init.body).batch) if (e.type === "track") tracked.push({ event: e.event, properties: e.properties });
  return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) });
};
const mem = () => { const m = new Map(); return { getItem: (k) => (m.has(k) ? m.get(k) : null), setItem: (k, v) => m.set(k, String(v)), removeItem: (k) => m.delete(k), key: () => null, length: 0 }; };
const timers = [];
const window = { addEventListener() {}, crypto: globalThis.crypto, fetch, matchMedia: (q) => ({ matches: /max-width: 767px/.test(q) && (opt.width || 1280) <= 767 }), setTimeout: (f) => { timers.push(f); return timers.length; } };
const ctx = vm.createContext({
  window, document, location: loc, history: { state: null, pushState() {}, replaceState() {} }, URL, URLSearchParams, Intl, Date, Math, JSON, Array,
  navigator: { language: "en", sendBeacon: () => true }, crypto: globalThis.crypto, fetch, setTimeout: window.setTimeout, sessionStorage: mem(), localStorage: mem(),
});
vm.runInContext(readFileSync(file, "utf8"), ctx);
for (let i = 0; i < 5; i++) {
  await new Promise((r) => setImmediate(r));
  while (timers.length) timers.shift()();
}
console.log(JSON.stringify({ page: body.childNodes.map((c) => c.dump()), styles: head.all().filter((n) => n.tagName === "STYLE").map((n) => ({ id: n.id, css: n.textContent })), redirect, tracked }));
