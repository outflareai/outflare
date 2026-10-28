/*! Outflare website tracking. https://outflare.ai/docs/ */
// Sends a page view on every page (and on in-app navigation), with the
// landing page's campaign tags, ad click ids and referral code exactly as
// they came. Nothing is classified here: Outflare works out channels and
// attribution on the server. Visitors are known only by a random id; your
// own product links it to a person by sending identify with userId and this
// anonymousId (outflare.anonymousId()) when they sign up or sign in.
//
//   <script async src="https://app.outflare.ai/site.js" data-key="ofk_..."></script>
//
// Served from your own domain (a path your site forwards to Outflare, such
// as /ofx/site.js), events go to that same path, so blockers leave them
// alone and the visitor's cookie is set by the server and kept for a year.
//
// Sites on more than one domain list them in data-domains, such as
// data-domains="northpeak.example,northpeak-app.example": links between them
// carry the visitor's id for two minutes, so one visit spans both.
//
// Consent: where consent is needed (data-consent="auto", the default, treats
// visitors in Europe and the UK that way, and anyone with Global Privacy
// Control on), no cookie is set and the id lasts only for the page until
// you call outflare.consent(true). data-consent="required" asks it of everyone;
// "granted" if your own banner already asked before loading this.
//
// Feature flags and A/B tests: with data-flags on the tag (or the first
// call to outflare.flag), the script asks for the visitor's variants.
// outflare.onFlags(fn) runs fn once they are known; outflare.flag("key")
// then gives the variant or false, and outflare.flagPayload("key") its
// payload. <html> gets a class per variant (of-flag-pricing-headline-faster)
// for tests done in CSS alone. To keep visitors from seeing the original
// flash before a variant, give <html> the class of-wait from an inline
// script in <head>, with a style that hides the page while it is there:
// the script takes it off as soon as the flags arrive, and your inline
// script after a second or two at most. ?of_flag=key:variant on any page
// shows that variant to you alone for the rest of the visit, to preview it.
//
// A visitor joins a flag's A/B test when the page shows them its version:
// when the page reads it (outflare.flag, outflare.flagPayload, or a flag
// in what onFlags gives), or, with data-flags, when its class goes on
// <html>. The script then sends Experiment Viewed with the flag and the
// variant, once per visit. Previews send nothing.
(function () {
  "use strict";
  var w = window, d = document;
  if (w.outflare && w.outflare.loaded) return;
  var script = d.currentScript || d.querySelector("script[data-key][src*='site.js']");
  if (!script) return;
  var key = script.getAttribute("data-key") || "";
  // Sent with every batch, so Outflare can tell a site running an old copy.
  var VERSION = "3";
  // Events go where the script came from: a path the site forwards keeps
  // its prefix.
  var src = new URL(script.src, location.href);
  var api = (script.getAttribute("data-api") || (src.origin + src.pathname.replace(/\/site\.js$/, ""))).replace(/\/$/, "");
  // Browsers driven by automation are not visitors.
  if (navigator.webdriver) return;
  var domains = (script.getAttribute("data-domains") || "").split(/[\s,]+/).filter(Boolean).map(function (d) { return d.toLowerCase().replace(/^www\./, ""); });
  var mode = script.getAttribute("data-consent") || "auto";
  var COOKIE = "of_aid", YEAR = 31536000;
  var clickIDs = ["gclid", "gbraid", "wbraid", "fbclid", "msclkid", "li_fat_id", "rdt_cid", "twclid"];

  function needsConsent() {
    if (mode === "required") return true;
    if (mode === "granted" || mode === "none") return false;
    if (navigator.globalPrivacyControl) return true;
    var tz = "";
    try { tz = Intl.DateTimeFormat().resolvedOptions().timeZone || ""; } catch (e) {}
    return /^Europe\//.test(tz) || /^Atlantic\/(Reykjavik|Canary|Madeira|Azores|Faroe)$/.test(tz) ||
      /^(Africa\/Ceuta|Asia\/Nicosia|Asia\/Famagusta|Arctic\/Longyearbyen)$/.test(tz);
  }
  var consented = !needsConsent();

  function uuid() {
    if (w.crypto && crypto.randomUUID) return crypto.randomUUID();
    return "xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx".replace(/[xy]/g, function (c) {
      var r = (Math.random() * 16) | 0;
      return (c === "x" ? r : (r & 3) | 8).toString(16);
    });
  }
  function readCookie() {
    var m = d.cookie.match(/(?:^|;\s*)of_aid=([A-Za-z0-9-]{8,64})/);
    return m ? m[1] : "";
  }
  // The cookie goes on the widest domain that takes it, so www and app
  // subdomains share one id. Without consent there is no cookie at all: the
  // id lives only in this page, and in-app navigation keeps it.
  function writeCookie(id) {
    var parts = location.hostname.split("."), attrs = "; path=/; SameSite=Lax" + (location.protocol === "https:" ? "; Secure" : "");
    attrs += "; max-age=" + (consented ? YEAR : 0);
    for (var i = Math.max(parts.length - 2, 0); i >= 0; i--) {
      var domain = parts.slice(i).join(".");
      d.cookie = COOKIE + "=" + id + attrs + (i < parts.length - 1 ? "; domain=" + domain : "");
      if (consented ? readCookie() === id : !readCookie()) return;
    }
  }
  // A cookie already there was set with consent.
  var stored = readCookie();
  if (stored) consented = true;
  var anon = stored || uuid();
  // A link from one of the site's other domains carries the visitor's id,
  // valid for two minutes so a shared link doesn't merge its readers. It
  // counts only when the visitor came from another listed domain, so a
  // link someone pasted elsewhere can't give its readers their id.
  try {
    var here = new URL(location.href), carried = here.searchParams.get("_ofa");
    if (carried !== null) {
      var parts = carried.split("."), age = Date.now() / 1000 - Number(parts[1]), from = "";
      try { from = registrable(new URL(d.referrer).hostname); } catch (e) {}
      if (from && from !== registrable(location.hostname) && /^[A-Za-z0-9-]{8,64}$/.test(parts[0]) && age >= -60 && age < 120) anon = parts[0];
      here.searchParams.delete("_ofa");
      history.replaceState(history.state, "", here.pathname + here.search + here.hash);
    }
  } catch (e) {}
  // A click on a tracked link in an email the workspace sent carries a
  // token (ofvisit) signed by Outflare. It goes with the first page view, so
  // Outflare can tell whose visit it is, and leaves the address at once.
  var visitToken = "";
  try {
    var there = new URL(location.href);
    visitToken = there.searchParams.get("ofvisit") || "";
    if (visitToken) {
      there.searchParams.delete("ofvisit");
      history.replaceState(history.state, "", there.pathname + there.search + there.hash);
    }
  } catch (e) {}
  if (consented) writeCookie(anon);

  // Variants to preview, from ?of_flag=key:variant, kept for the visit.
  var preview = {};
  try { preview = JSON.parse(sessionStorage.getItem("of_flag_preview") || "{}") || {}; } catch (e) {}
  try {
    var asked = new URL(location.href), pv = asked.searchParams.getAll("of_flag");
    if (pv.length) {
      pv.join(",").split(",").forEach(function (p) {
        var kv = p.split(":");
        if (/^[a-z0-9][a-z0-9_-]{0,63}$/.test(kv[0])) preview[kv[0]] = kv[1] && kv[1] !== "off" ? kv[1] : false;
      });
      try { sessionStorage.setItem("of_flag_preview", JSON.stringify(preview)); } catch (e) {}
      asked.searchParams.delete("of_flag");
      history.replaceState(history.state, "", asked.pathname + asked.search + asked.hash);
    }
  } catch (e) {}

  function campaign(q) {
    var c = {}, any = false;
    ["source", "medium", "campaign", "term", "content", "id"].forEach(function (k) {
      var v = q.get("utm_" + k);
      if (v) { c[k === "campaign" ? "name" : k] = v; any = true; }
    });
    return any ? c : null;
  }
  function landing(q) {
    var p = {}, ids = {}, any = false;
    clickIDs.forEach(function (k) {
      var v = q.get(k);
      if (v) { ids[k] = v; any = true; }
    });
    if (any) p.click_ids = ids;
    if (q.get("ofc")) p.ofc = q.get("ofc");
    var ref = q.get("ref") || q.get("via");
    if (ref) p.ref = ref;
    return p;
  }

  var lastURL = "";
  function send(events) {
    post(JSON.stringify({ key: key, batch: events, consent: consented, v: VERSION }), 0);
  }
  // A network error or a server error is tried again after 1, 5 and 25
  // seconds. Each event has its own messageId, so a retry never counts twice.
  function post(body, attempt) {
    var url = api + "/v1/site/events";
    function retry() {
      if (attempt < 3) setTimeout(function () { post(body, attempt + 1); }, 1000 * Math.pow(5, attempt));
    }
    if (!w.fetch) {
      try { if (navigator.sendBeacon) navigator.sendBeacon(url, body); } catch (e) {}
      return;
    }
    try {
      fetch(url, { method: "POST", body: body, keepalive: true, headers: { "Content-Type": "text/plain" } })
        .then(function (res) { if (res.status >= 500) retry(); }, retry);
    } catch (e) { retry(); }
  }
  function context(q) {
    var c = {
      page: { url: location.href, path: location.pathname, search: location.search, referrer: lastURL || d.referrer, title: d.title },
      locale: navigator.language || ""
    };
    try { c.timezone = Intl.DateTimeFormat().resolvedOptions().timeZone; } catch (e) {}
    var camp = campaign(q);
    if (camp) c.campaign = camp;
    return c;
  }
  function page(name, properties) {
    var q = new URLSearchParams(location.search), props = landing(q);
    for (var k in properties || {}) props[k] = properties[k];
    if (visitToken) { props.ofvisit = visitToken; visitToken = ""; }
    if (flagsWanted && lastURL && lastURL !== location.href) loadFlags();
    var e = { type: "page", anonymousId: anon, messageId: uuid(), properties: props, context: context(q) };
    if (name) e.name = String(name);
    send([e]);
    lastURL = location.href;
  }
  // track returns the event's messageId: pass it to an ad pixel as its
  // event id (Meta's eventID), so a conversion Outflare also sends back
  // server to server is counted once.
  function track(event, properties) {
    if (!event) return;
    var q = new URLSearchParams(location.search), id = uuid();
    send([{ type: "track", event: String(event), anonymousId: anon, messageId: id, properties: properties || {}, context: context(q) }]);
    return id;
  }

  // Flags: asked for once per page (and again on in-app navigation, since
  // rules can depend on the page), and kept for the visit so the next page
  // starts with the last answer.
  var FLAGS = "of_flags", SEEN = "of_flags_seen", classFlags = script.getAttribute("data-flags") !== null, flagsWanted = classFlags, flagState = null, flagWaiters = [], flagClasses = [], seen = {};
  try {
    var kept = JSON.parse(sessionStorage.getItem(FLAGS) || "null");
    if (kept && kept.anon === anon) flagState = kept;
    var keptSeen = JSON.parse(sessionStorage.getItem(SEEN) || "null");
    if (keptSeen && keptSeen.anon === anon) seen = keptSeen.flags || {};
  } catch (e) {}
  // The visitor has been shown flag k's version: tell Outflare once per
  // visit (and again only if their version changes), so the flag's A/B
  // test counts them. Not for a preview, nor before the flags arrive.
  function exposed(k) {
    if (Object.prototype.hasOwnProperty.call(preview, k)) return;
    var v = flagState && flagState.flags[k];
    if (!v || seen[k] === v) return;
    seen[k] = v;
    try { sessionStorage.setItem(SEEN, JSON.stringify({ anon: anon, flags: seen })); } catch (e) {}
    track("Experiment Viewed", { flag: k, variant: v });
  }
  function applyFlags(state) {
    flagState = state;
    var root = d.documentElement, cls = [];
    for (var k in state.flags) if (variant(k)) cls.push("of-flag-" + k + "-" + variant(k));
    flagClasses.forEach(function (c) { root.classList.remove(c); });
    cls.forEach(function (c) { root.classList.add(c); });
    flagClasses = cls;
    if (classFlags) for (k in state.flags) exposed(k);
    root.classList.remove("of-wait");
    var waiting = flagWaiters;
    flagWaiters = [];
    waiting.forEach(function (fn) { try { fn(flagMap()); } catch (e) {} });
  }
  function variant(k) {
    if (Object.prototype.hasOwnProperty.call(preview, k)) return preview[k];
    return flagState && flagState.flags[k] ? flagState.flags[k] : false;
  }
  // The versions being previewed, for the server to send their content.
  function previewed() {
    var out = {};
    for (var k in preview) if (preview[k]) out[k] = preview[k];
    return out;
  }
  // Every flag that is on, with its variant or false. Reading one counts
  // as showing it.
  function flagMap() {
    var out = {}, k;
    function get(k) { return function () { exposed(k); return variant(k); }; }
    for (k in (flagState && flagState.flags) || {}) {
      if (!Object.prototype.hasOwnProperty.call(preview, k)) Object.defineProperty(out, k, { get: get(k), enumerable: true });
    }
    for (k in preview) out[k] = preview[k];
    return out;
  }
  function loadFlags() {
    flagsWanted = true;
    if (!w.fetch) return applyFlags({ anon: anon, flags: {}, payloads: {} });
    var body = JSON.stringify({ key: key, anonymousId: anon, url: location.href, referrer: lastURL || d.referrer, language: navigator.language || "", preview: previewed() });
    fetch(api + "/v1/site/flags", { method: "POST", body: body, headers: { "Content-Type": "text/plain" } })
      .then(function (res) { return res.ok ? res.json() : null; })
      .then(function (out) {
        var state = { anon: anon, flags: (out && out.flags) || (flagState && flagState.flags) || {}, payloads: (out && out.payloads) || (flagState && flagState.payloads) || {} };
        if (out) try { sessionStorage.setItem(FLAGS, JSON.stringify(state)); } catch (e) {}
        applyFlags(state);
      }, function () { applyFlags(flagState || { anon: anon, flags: {}, payloads: {} }); });
  }
  // The last answer shows at once; a fresh one follows.
  if (flagState) applyFlags(flagState);

  // Links to the site's other domains carry the visitor's id.
  function registrable(host) {
    host = host.toLowerCase().replace(/^www\./, "");
    for (var i = 0; i < domains.length; i++) {
      if (host === domains[i] || host.slice(-domains[i].length - 1) === "." + domains[i]) return domains[i];
    }
    return "";
  }
  if (domains.length) {
    var decorate = function (e) {
      var a = e.target && e.target.closest ? e.target.closest("a[href]") : null;
      if (!a) return;
      try {
        var to = new URL(a.href, location.href), mine = registrable(location.hostname), theirs = registrable(to.hostname);
        if (!theirs || theirs === mine || !/^https?:$/.test(to.protocol)) return;
        to.searchParams.set("_ofa", anon + "." + Math.floor(Date.now() / 1000));
        a.href = to.toString();
      } catch (err) {}
    };
    d.addEventListener("mousedown", decorate, true);
    d.addEventListener("keydown", function (e) { if (e.key === "Enter") decorate(e); }, true);
    d.addEventListener("touchstart", decorate, { capture: true, passive: true });
  }

  // In-app navigation counts as a page view.
  function watch(method) {
    var orig = history[method];
    history[method] = function () {
      var out = orig.apply(this, arguments);
      if (location.href !== lastURL) setTimeout(function () { page(); }, 0);
      return out;
    };
  }
  watch("pushState");
  watch("replaceState");
  w.addEventListener("popstate", function () { if (location.href !== lastURL) page(); });

  var queued = Array.isArray(w.outflare) ? w.outflare : [];
  w.outflare = {
    loaded: true,
    page: page,
    track: track,
    // The visitor's id, to send with identify from your server when they
    // sign up or sign in, so their earlier visits join their record.
    anonymousId: function () { return anon; },
    // Call with true once the visitor accepts analytics cookies (the id is
    // then kept in a cookie for a year), false if they withdraw (the cookie
    // is removed).
    consent: function (yes) { consented = !!yes; writeCookie(anon); },
    // Forget the visitor, such as on sign-out on a shared computer.
    reset: function () {
      anon = uuid();
      if (consented) writeCookie(anon);
      seen = {};
      try { sessionStorage.removeItem(FLAGS); sessionStorage.removeItem(SEEN); } catch (e) {}
      if (flagsWanted) loadFlags();
    },
    // The visitor's variant of a flag, or false: false too until the
    // flags arrive, so wait with onFlags when it matters.
    flag: function (k) {
      if (!flagsWanted) loadFlags();
      exposed(String(k));
      return variant(String(k));
    },
    flagPayload: function (k) {
      exposed(String(k));
      var v = variant(String(k));
      return v && flagState && flagState.payloads ? flagState.payloads[k] : undefined;
    },
    // Runs fn(flags) once the visitor's flags are known (at once when
    // they already are), with every flag that is on: its variant or false.
    onFlags: function (fn) {
      if (typeof fn !== "function") return;
      if (!flagsWanted) loadFlags();
      if (flagState) { try { fn(flagMap()); } catch (e) {} } else flagWaiters.push(fn);
    }
  };
  if (flagsWanted) loadFlags();
  page();
  queued.forEach(function (c) {
    if (c && w.outflare[c[0]]) w.outflare[c[0]].apply(null, c.slice(1));
  });
})();
