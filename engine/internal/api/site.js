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
// Consent banner: turned on for the site key in Outflare (Website tracking),
// this script shows the cookie banner itself, with the rule for the
// visitor's region (ask first in the EEA, the UK and Switzerland; opt-out
// in the US), Global Privacy Control, the categories (analytics, marketing,
// preferences) and the look chosen there. It sets Google Consent Mode v2
// and the Meta, TikTok and Microsoft consent signals, and loads scripts and
// iframes kept back until their category is allowed:
//
//   <script type="text/plain" data-category="marketing" src="https://..."></script>
//   <iframe data-category="marketing" data-src="https://..."></iframe>
//
// outflare.consent() gives the visitor's choices; outflare.consent(true),
// (false) or ({analytics: true, marketing: false}) sets them, from your own
// banner too. outflare.showConsent() (or any element with
// data-outflare-consent-open) opens the banner again, and
// outflare.onConsent(fn) runs fn(choices) now and on every change. Each
// choice is kept in Outflare as the visitor's consent, and joins their
// record when they sign up. data-banner="off" keeps the banner off.
//
// Cookie policy: an element with data-outflare-cookie-policy shows the
// site's cookie policy, which Outflare keeps up to date. Once a day a
// browser reports the names (never the values) of the page's cookies,
// storage keys, scripts and embeds for it; data-cookies="off" stops that.
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
// script after a second or two at most. Better still, decide the flags on
// your own server (POST .../flags/evaluate) and hand them to the page in
// <script type="application/json" id="outflare-flags">: the script uses
// them at once, with no request. ?of_flag=key:variant on any page
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
  var VERSION = "8";
  // Events go where the script came from: a path the site forwards keeps
  // its prefix.
  var src = new URL(script.src, location.href);
  var api = (script.getAttribute("data-api") || (src.origin + src.pathname.replace(/\/site\.js$/, ""))).replace(/\/$/, "");
  // Browsers driven by automation are not visitors.
  if (navigator.webdriver) return;
  var domains = (script.getAttribute("data-domains") || "").split(/[\s,]+/).filter(Boolean).map(function (d) { return d.toLowerCase().replace(/^www\./, ""); });
  var mode = script.getAttribute("data-consent") || "auto";
  var COOKIE = "of_aid", CHOICE = "of_consent", YEAR = 31536000;
  // Click ids ad networks add to landing pages, the ad ids an Outflare
  // tracking template adds (of_campaign and the rest), and the cookies ad
  // pixels set, read only with marketing consent.
  var clickIDs = ["gclid", "gbraid", "wbraid", "dclid", "fbclid", "msclkid", "li_fat_id", "rdt_cid", "twclid", "ttclid", "ScCid", "epik", "tblci", "ob_click_id", "qclid", "irclickid"];
  var adParams = ["campaign", "adset", "ad", "placement"];
  var adCookies = { _fbp: "fbp", _fbc: "fbc", _gcl_aw: "gcl_aw", _ttp: "ttp", _scid: "scid" };

  function needsConsent() {
    if (mode === "required") return true;
    if (mode === "granted" || mode === "none") return false;
    if (navigator.globalPrivacyControl) return true;
    var tz = "";
    try { tz = Intl.DateTimeFormat().resolvedOptions().timeZone || ""; } catch (e) {}
    return /^Europe\//.test(tz) || /^Atlantic\/(Reykjavik|Canary|Madeira|Azores|Faroe)$/.test(tz) ||
      /^(Africa\/Ceuta|Asia\/Nicosia|Asia\/Famagusta|Arctic\/Longyearbyen)$/.test(tz);
  }
  // The visitor's choices by category; analytics is what keeps their id in
  // a cookie. Worked out below, once the banner's settings are known.
  var CATS = ["analytics", "marketing", "preferences"];
  var cats = {}, consented = false;

  function uuid() {
    if (w.crypto && crypto.randomUUID) return crypto.randomUUID();
    return "xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx".replace(/[xy]/g, function (c) {
      var r = (Math.random() * 16) | 0;
      return (c === "x" ? r : (r & 3) | 8).toString(16);
    });
  }
  function readCookie(name, re) {
    var m = d.cookie.match(new RegExp("(?:^|;\\s*)" + (name || COOKIE) + "=(" + (re || "[A-Za-z0-9-]{8,64}") + ")"));
    return m ? m[1] : "";
  }
  // A cookie goes on the widest domain that takes it, so www and app
  // subdomains share it. Without consent there is no id cookie at all: the
  // id lives only in this page, and in-app navigation keeps it.
  function setCookie(name, value, keep) {
    var parts = location.hostname.split("."), attrs = "; path=/; SameSite=Lax" + (location.protocol === "https:" ? "; Secure" : "");
    attrs += "; max-age=" + (keep ? YEAR : 0);
    for (var i = Math.max(parts.length - 2, 0); i >= 0; i--) {
      var domain = parts.slice(i).join(".");
      d.cookie = name + "=" + value + attrs + (i < parts.length - 1 ? "; domain=" + domain : "");
      if (keep ? readCookie(name, "[^;]*") === value : !readCookie(name, "[^;]*")) return;
    }
  }
  function writeCookie(id) { setCookie(COOKIE, id, consented); }
  // An id cookie already there was set with consent.
  var stored = readCookie();
  var anon = stored || uuid(), carriedID = false, started = Date.now();
  // A link from one of the site's other domains carries the visitor's id,
  // valid for two minutes so a shared link doesn't merge its readers. It
  // counts only when the visitor came from another listed domain, so a
  // link someone pasted elsewhere can't give its readers their id.
  try {
    var here = new URL(location.href), carried = here.searchParams.get("_ofa");
    if (carried !== null) {
      var parts = carried.split("."), age = Date.now() / 1000 - Number(parts[1]), from = "";
      try { from = registrable(new URL(d.referrer).hostname); } catch (e) {}
      if (from && from !== registrable(location.hostname) && /^[A-Za-z0-9-]{8,64}$/.test(parts[0]) && age >= -60 && age < 120) { anon = parts[0]; carriedID = true; }
      here.searchParams.delete("_ofa");
      history.replaceState(history.state, "", here.pathname + here.search + here.hash);
    }
  } catch (e) {}
  // Flags the site's own server decided for this page, handed over in
  // <script type="application/json" id="outflare-flags"> as
  // {"anonymousId", "flags", "payloads"}: the page shows the visitor's
  // version at once, with no request and no flash. They count only for the
  // visitor they were decided for: this page's id, or the server's own when
  // the browser has none yet.
  var boot = null;
  try {
    var bootEl = d.getElementById && d.getElementById("outflare-flags");
    var b = bootEl && JSON.parse(bootEl.textContent || "null");
    if (b && typeof b.anonymousId === "string" && /^[A-Za-z0-9-]{8,64}$/.test(b.anonymousId) && b.flags && typeof b.flags === "object" &&
      (b.anonymousId === anon || (!stored && !carriedID))) {
      anon = b.anonymousId;
      boot = { flags: {}, payloads: {} };
      for (var bk in b.flags) {
        var bv = b.flags[bk];
        if (/^[a-z0-9][a-z0-9_-]{0,63}$/.test(bk) && (bv === false || (typeof bv === "string" && /^[a-z0-9][a-z0-9_-]{0,63}$/.test(bv)))) boot.flags[bk] = bv;
      }
      if (b.payloads && typeof b.payloads === "object") for (bk in b.payloads) if (boot.flags[bk]) boot.payloads[bk] = b.payloads[bk];
    }
  } catch (e) { boot = null; }
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
  // ---- Consent ----
  // The visitor's last choice, from the of_consent cookie: the banner's
  // consent version, a digit per category (1 allowed, 0 not, 2 not asked)
  // and when, in Unix seconds.
  function readChoice() {
    var m = readCookie(CHOICE, "\\d+\\.[012]{3}\\.\\d+");
    if (!m) return null;
    var p = m.split("."), c = { v: Number(p[0]), at: Number(p[2]) };
    CATS.forEach(function (k, i) { if (p[1].charAt(i) !== "2") c[k] = p[1].charAt(i) === "1"; });
    return c;
  }
  var choice = readChoice(), gpc = !!navigator.globalPrivacyControl, lang = (navigator.language || "en").toLowerCase();
  var bannerOff = script.getAttribute("data-banner") === "off", cfg = null, CFG = "of_cb";
  try {
    var cached = JSON.parse(localStorage.getItem(CFG) || "null");
    if (cached && cached.key === key && cached.lang === lang && cached.cfg) cfg = cached;
  } catch (e) {}
  function on() { return !bannerOff && cfg && cfg.cfg.enabled; }
  var EEA = "AT BE BG HR CY CZ DK EE FI FR DE GR HU IE IT LV LT LU MT NL PL PT RO SK SI ES SE IS LI NO GB CH GF GP MQ RE YT MF";
  function region() {
    var c = on() ? cfg.cfg.country : "", tz = "";
    if (c) return EEA.indexOf(c) >= 0 && c.length === 2 ? "eea" : c === "US" ? "us" : "other";
    try { tz = Intl.DateTimeFormat().resolvedOptions().timeZone || ""; } catch (e) {}
    if (/^Europe\//.test(tz) || /^Atlantic\/(Reykjavik|Canary|Madeira|Azores|Faroe)$/.test(tz) ||
      /^(Africa\/Ceuta|Asia\/Nicosia|Asia\/Famagusta|Arctic\/Longyearbyen)$/.test(tz)) return "eea";
    if (/^(America\/(New_York|Detroit|Kentucky|Indiana|Chicago|Menominee|North_Dakota|Denver|Boise|Phoenix|Los_Angeles|Anchorage|Juneau|Sitka|Metlakatla|Yakutat|Nome|Adak)|Pacific\/Honolulu|US\/)/.test(tz)) return "us";
    return "other";
  }
  // The rule for this visitor: opt_in (ask first), opt_out, notice or
  // none. Without the banner, data-consent decides as it always has.
  function rule() {
    if (on()) return cfg.cfg.regions[region()] || "opt_in";
    return needsConsent() ? "opt_in" : "none";
  }
  // A choice counts unless the banner asks again: a newer consent version
  // or an older choice than reask_after_days, where the rule asks first.
  function decided() {
    if (!choice) return false;
    if (!on() || rule() !== "opt_in") return true;
    var c = cfg.cfg, days = c.reask_after_days;
    return choice.v >= c.consent_version && !(days > 0 && Date.now() / 1000 - choice.at > days * 86400);
  }
  function honoursGPC() { return gpc && (!on() || cfg.cfg.frameworks.gpc); }
  function recompute() {
    var r = rule(), mine = decided();
    CATS.forEach(function (k) {
      var asked = !on() || cfg.cfg.categories[k];
      if (mine && choice[k] !== undefined) cats[k] = choice[k];
      else if (!asked) cats[k] = r !== "opt_in";
      else cats[k] = r !== "opt_in" && !(k === "marketing" && honoursGPC());
    });
    // An id cookie from before the banner was set with consent.
    if (stored && !mine && !on()) CATS.forEach(function (k) { cats[k] = true; });
    if (!on() && !mine) cats.marketing = cats.preferences = cats.analytics;
    consented = cats.analytics;
  }
  recompute();
  if (consented) writeCookie(anon);
  else if (stored && choice) setCookie(COOKIE, anon, false);

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
  // The visual editor: a link from Outflare carries ?of_edit=<token>, kept
  // for the visit, and the editor loads from Outflare over the page. The
  // token lets it change one version of one flag, for a short while.
  var editToken = "";
  try {
    editToken = sessionStorage.getItem("of_edit") || "";
    var editURL = new URL(location.href), et = editURL.searchParams.get("of_edit");
    if (et && /^[A-Za-z0-9_.-]{20,2000}$/.test(et)) {
      editToken = et;
      try { sessionStorage.setItem("of_edit", et); } catch (e) {}
      editURL.searchParams.delete("of_edit");
      history.replaceState(history.state, "", editURL.pathname + editURL.search + editURL.hash);
    }
  } catch (e) {}
  if (editToken && d.createElement) {
    var ed = d.createElement("script");
    ed.src = api + "/v1/site/editor.js";
    ed.async = true;
    ed.setAttribute("data-token", editToken);
    ed.setAttribute("data-api", api);
    (d.head || d.documentElement).appendChild && (d.head || d.documentElement).appendChild(ed);
  }

  function campaign(q) {
    var c = {}, any = false;
    ["source", "medium", "campaign", "term", "content", "id"].forEach(function (k) {
      var v = q.get("utm_" + k);
      if (v) { c[k === "campaign" ? "name" : k] = v; any = true; }
    });
    return any ? c : null;
  }
  // The ad pixels' cookies, once the visitor allows marketing.
  function cookieIDs(ids) {
    if (!cats.marketing) return false;
    var any = false;
    for (var name in adCookies) {
      var m = d.cookie.match(new RegExp("(?:^|;\\s*)" + name + "=([^;]{1,500})"));
      if (m) { ids[adCookies[name]] = m[1]; any = true; }
    }
    return any;
  }
  function landing(q) {
    var p = {}, ids = {}, ad = {}, any = cookieIDs(ids), anyAd = false;
    clickIDs.forEach(function (k) {
      var v = q.get(k);
      if (v) { ids[k] = v; any = true; }
    });
    if (any) p.click_ids = ids;
    adParams.forEach(function (k) {
      var v = q.get("of_" + k);
      if (v) { ad[k] = v; anyAd = true; }
    });
    if (anyAd) p.ad = ad;
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
    var q = new URLSearchParams(location.search), id = uuid(), props = {}, ids = {};
    for (var k in properties || {}) props[k] = properties[k];
    // A conversion carries the ad pixels' cookies, for sending it back.
    if (!props.click_ids && cookieIDs(ids)) props.click_ids = ids;
    send([{ type: "track", event: String(event), anonymousId: anon, messageId: id, properties: props, context: context(q) }]);
    return id;
  }

  // Flags: asked for once per page (and again on in-app navigation, since
  // rules can depend on the page), and kept for the visit so the next page
  // starts with the last answer.
  var FLAGS = "of_flags", SEEN = "of_flags_seen", classFlags = script.getAttribute("data-flags") !== null, flagsWanted = classFlags, flagState = null, flagWaiters = [], flagClasses = [], seen = {};
  // Where the flags in use came from (bootstrap: the server's, kept: the
  // last page's, fetched: asked for) and how long after the script started
  // they were first shown, sent with Experiment Viewed so a test can tell
  // whether visitors waited long enough to see the original first.
  var flagSource = "", flagWait = -1, bootURL = boot ? location.href : "";
  try {
    var kept = JSON.parse(sessionStorage.getItem(FLAGS) || "null");
    if (kept && kept.anon === anon) flagState = kept, flagSource = "kept";
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
    track("Experiment Viewed", { flag: k, variant: v, source: flagSource, wait_ms: Math.max(flagWait, 0) });
  }
  function applyFlags(state, source) {
    flagState = state;
    // The first answer shown is the one the visitor saw first.
    if (flagWait < 0) {
      flagWait = Date.now() - started;
      if (source) flagSource = source;
    }
    var root = d.documentElement, cls = [];
    for (var k in state.flags) if (variant(k)) cls.push("of-flag-" + k + "-" + variant(k));
    flagClasses.forEach(function (c) { root.classList.remove(c); });
    cls.forEach(function (c) { root.classList.add(c); });
    flagClasses = cls;
    if (classFlags) for (k in state.flags) exposed(k);
    applyChanges();
    root.classList.remove("of-wait");
    var waiting = flagWaiters;
    flagWaiters = [];
    waiting.forEach(function (fn) { try { fn(flagMap()); } catch (e) {} });
  }
  // Visual changes: a version's payload can list edits to make to the page
  // ({"changes": [{"selector", "text" | "hide" | "href" | "src" and "alt"}]},
  // from the visual editor). They apply when the flags arrive, again once
  // the page has loaded and a moment later for content that comes late,
  // and making them counts as showing the version. Text goes in as text,
  // never HTML, and links and images only to http(s) or a path.
  function safeURL(u) {
    return typeof u === "string" && u.length <= 2000 && !/\s/.test(u) && (/^\/(?!\/)/.test(u) || /^https?:\/\/[^/]/i.test(u));
  }
  function applyChanges() {
    if (!flagState || !flagState.payloads) return;
    for (var k in flagState.payloads) {
      var p = flagState.payloads[k], cs = p && p.changes;
      if (!variant(k) || !cs || !cs.length) continue;
      var applied = false;
      for (var i = 0; i < cs.length && i < 50; i++) {
        var c = cs[i], els = [];
        if (!c || typeof c.selector !== "string") continue;
        try { els = d.querySelectorAll(c.selector); } catch (e) { continue; }
        for (var j = 0; j < els.length; j++) {
          var el = els[j];
          if (typeof c.text === "string") el.textContent = c.text;
          else if (c.hide) el.style.setProperty("display", "none", "important");
          else if (safeURL(c.href)) el.setAttribute("href", c.href);
          else if (c.src !== undefined || c.alt !== undefined) {
            if (safeURL(c.src)) el.setAttribute("src", c.src);
            if (typeof c.alt === "string") el.setAttribute("alt", c.alt);
          }
          applied = true;
        }
      }
      if (applied) exposed(k);
    }
  }
  if (d.readyState === "loading") d.addEventListener("DOMContentLoaded", function () { applyChanges(); });
  w.setTimeout && w.setTimeout(function () { applyChanges(); }, 1500);

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
    // The server already decided this page.
    if (bootURL && bootURL === location.href) return applyFlags(flagState, "bootstrap");
    if (!w.fetch) return applyFlags({ anon: anon, flags: {}, payloads: {} });
    var body = JSON.stringify({ key: key, anonymousId: anon, url: location.href, referrer: lastURL || d.referrer, language: navigator.language || "", preview: previewed() });
    fetch(api + "/v1/site/flags", { method: "POST", body: body, headers: { "Content-Type": "text/plain" } })
      .then(function (res) { return res.ok ? res.json() : null; })
      .then(function (out) {
        var state = { anon: anon, flags: (out && out.flags) || (flagState && flagState.flags) || {}, payloads: (out && out.payloads) || (flagState && flagState.payloads) || {} };
        if (out) try { sessionStorage.setItem(FLAGS, JSON.stringify(state)); } catch (e) {}
        applyFlags(state, out ? "fetched" : "");
      }, function () { applyFlags(flagState || { anon: anon, flags: {}, payloads: {} }); });
  }
  // The server's answer, or else the last one, shows at once; a fresh one
  // follows unless the server's covers this page.
  if (boot) {
    flagState = { anon: anon, flags: boot.flags, payloads: boot.payloads };
    try { sessionStorage.setItem(FLAGS, JSON.stringify(flagState)); } catch (e) {}
    applyFlags(flagState, "bootstrap");
  } else if (flagState) applyFlags(flagState);

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

  // ---- Consent banner ----
  // The banner's settings come from Outflare, kept in this browser for a
  // few minutes so later pages show it at once.
  // Kept-back scripts wait for the settings on a first page, since the
  // banner may ask first where data-consent wouldn't.
  var ready = bannerOff || !!cfg || !w.fetch;
  function loadBanner() {
    if (bannerOff || !w.fetch || (cfg && Date.now() - cfg.at < (cfg.cfg.enabled ? 300000 : 1800000))) return;
    var fail = function () { if (!ready) { ready = true; unblock(); } };
    fetch(api + "/v1/site/consent?key=" + encodeURIComponent(key) + "&lang=" + encodeURIComponent(lang))
      .then(function (res) { return res.ok ? res.json() : null; })
      .then(function (out) {
        if (!out) return fail();
        var was = on();
        ready = true;
        cfg = { key: key, lang: lang, at: Date.now(), cfg: out };
        try { localStorage.setItem(CFG, JSON.stringify(cfg)); } catch (e) {}
        settle(!was);
      }, fail);
  }
  // settle applies the current choices everywhere: the id cookie, the ad
  // platforms' consent signals, the scripts kept back, and the banner.
  var signalled = false, listeners = [];
  function settle(first) {
    recompute();
    writeCookie(anon);
    signals(first || !signalled);
    unblock();
    if (on() && !decided() && rule() !== "none") showBanner(false);
    else if (on() && rule() !== "none") showReopen();
    else hideBanner();
    listeners.forEach(function (fn) { try { fn(choices()); } catch (e) {} });
  }
  function choices() {
    return { necessary: true, analytics: cats.analytics, marketing: cats.marketing, preferences: cats.preferences,
      decided: decided(), region: region(), rule: rule(), gpc: gpc };
  }
  // choose keeps the visitor's choice: in the of_consent cookie (the server
  // sets it too, for a full year), and in Outflare when it changed.
  function choose(next, action) {
    var c = { v: on() ? cfg.cfg.consent_version : 1, at: Math.floor(Date.now() / 1000) }, digits = "", body = { key: key, anonymousId: anon, action: action };
    CATS.forEach(function (k) {
      var asked = !on() || cfg.cfg.categories[k], v = next[k] === undefined ? cats[k] : !!next[k];
      digits += asked ? (v ? "1" : "0") : "2";
      if (asked) { c[k] = v; body[k] = v; }
    });
    var before = readCookie(CHOICE, "[^;]*").split(".");
    choice = c;
    setCookie(CHOICE, c.v + "." + digits + "." + c.at, true);
    settle(false);
    if (before[0] === String(c.v) && before[1] === digits) return;
    body.region = region();
    body.gpc = gpc;
    body.lang = lang;
    body.path = location.pathname;
    if (on()) { body.version = cfg.cfg.version; body.consent_version = cfg.cfg.consent_version; }
    if (w.fetch) {
      try { fetch(api + "/v1/site/consent", { method: "POST", body: JSON.stringify(body), keepalive: true, headers: { "Content-Type": "text/plain" } }).then(null, function () {}); } catch (e) {}
    }
  }

  // Google Consent Mode v2, Meta, TikTok and Microsoft, each when the
  // banner is on and the workspace left it on. The first call sets the
  // defaults; later ones update them.
  function signals(first) {
    if (!on()) return;
    var fw = cfg.cfg.frameworks, m = cats.marketing, a = cats.analytics, p = cats.preferences, waiting = !decided() && rule() === "opt_in";
    var g = function (v) { return v ? "granted" : "denied"; };
    if (fw.google_consent_mode) {
      var gtag = function () { (w.dataLayer = w.dataLayer || []).push(arguments); };
      var st = { ad_storage: g(m), ad_user_data: g(m), ad_personalization: g(m), analytics_storage: g(a),
        functionality_storage: g(p), personalization_storage: g(p), security_storage: "granted" };
      if (first) {
        st.wait_for_update = 500;
        gtag("consent", "default", st);
        gtag("set", "ads_data_redaction", !m);
      } else gtag("consent", "update", st);
    }
    if (fw.microsoft) (w.uetq = w.uetq || []).push("consent", first ? "default" : "update", { ad_storage: g(m) });
    signalled = true;
    vendorSignals(waiting);
  }
  // Meta's and TikTok's pixels take consent only once they have loaded,
  // so this runs again when the page has.
  function vendorSignals(waiting) {
    if (!on()) return;
    var fw = cfg.cfg.frameworks, m = cats.marketing;
    try { if (fw.meta && typeof w.fbq === "function") w.fbq("consent", m ? "grant" : "revoke"); } catch (e) {}
    try {
      if (fw.tiktok && w.ttq) {
        if (waiting && w.ttq.holdConsent) w.ttq.holdConsent();
        else if (m && w.ttq.grantConsent) w.ttq.grantConsent();
        else if (!m && w.ttq.revokeConsent) w.ttq.revokeConsent();
      }
    } catch (e) {}
  }

  // Scripts and iframes kept back until their categories are allowed:
  // type="text/plain" data-category="marketing", or data-src on an iframe.
  function allowed(el) {
    var list = (el.getAttribute("data-category") || "").split(/[\s,]+/).filter(Boolean);
    for (var i = 0; i < list.length; i++) if (list[i] !== "necessary" && !cats[list[i]]) return false;
    return list.length > 0;
  }
  function unblock() {
    if (!ready || !d.querySelectorAll) return;
    var held = d.querySelectorAll('script[type="text/plain"][data-category],iframe[data-category][data-src]');
    for (var i = 0; i < held.length; i++) {
      var el = held[i];
      if (el.getAttribute("data-of-loaded") !== null || !allowed(el)) continue;
      el.setAttribute("data-of-loaded", "");
      if (el.tagName === "IFRAME") { el.src = el.getAttribute("data-src"); continue; }
      var run = d.createElement("script");
      for (var j = 0; j < el.attributes.length; j++) {
        var at = el.attributes[j];
        if (at.name !== "type" && at.name !== "data-type") run.setAttribute(at.name, at.value);
      }
      if (el.getAttribute("data-type")) run.type = el.getAttribute("data-type");
      if (!el.src) run.text = el.text;
      run.async = false;
      el.parentNode.replaceChild(run, el);
    }
  }

  // The banner, in its own shadow root so the site's styles leave it alone.
  var host = null, root = null, prefsOpen = false;
  var esc = function (t) { return String(t == null ? "" : t).replace(/[&<>"']/g, function (c) { return "&#" + c.charCodeAt(0) + ";"; }); };
  function contrast(hex) {
    var h = (hex || "").replace("#", "");
    if (h.length === 3) h = h.replace(/./g, "$&$&");
    var n = parseInt(h, 16);
    if (h.length !== 6 || isNaN(n)) return "";
    return (0.299 * (n >> 16) + 0.587 * ((n >> 8) & 255) + 0.114 * (n & 255)) > 150 ? "#111827" : "#ffffff";
  }
  function css(c) {
    var dark = c.theme === "dark", col = c.colors || {};
    var bg = col.background || (dark ? "#111827" : "#ffffff"), fg = col.text || (dark ? "#f9fafb" : "#1f2937");
    var pri = col.primary || (dark ? "#f9fafb" : "#111827");
    return ":host{all:initial}" +
      ".of-cb,.of-cb-reopen{--bg:" + bg + ";--fg:" + fg + ";--pri:" + pri + ";--pri-fg:" + (contrast(pri) || "#fff") + ";--line:" + (dark ? "rgba(255,255,255,.2)" : "rgba(0,0,0,.14)") + "}" +
      ".of-cb{position:fixed;z-index:2147483646;box-sizing:border-box;font:14px/1.45 system-ui,-apple-system,'Segoe UI',Roboto,sans-serif;color:var(--fg);background:var(--bg);box-shadow:0 6px 30px rgba(0,0,0,.2);padding:16px 20px;display:flex;flex-wrap:wrap;gap:12px 20px;align-items:center}" +
      ".of-cb *{box-sizing:border-box}" +
      ".of-cb--bar{left:0;right:0;bottom:0}.of-cb--bar.of-cb--top{top:0;bottom:auto}" +
      ".of-cb--box{width:min(420px,calc(100vw - 32px));border-radius:12px;flex-direction:column;align-items:stretch;bottom:16px;right:16px}" +
      ".of-cb--box.of-cb--bottom_left{right:auto;left:16px}.of-cb--box.of-cb--top{bottom:auto;top:16px}.of-cb--box.of-cb--bottom{right:auto;left:50%;transform:translateX(-50%)}" +
      ".of-cb--modal,.of-cb--box.of-cb--center{top:50%;left:50%;right:auto;bottom:auto;transform:translate(-50%,-50%);width:min(520px,calc(100vw - 32px));max-height:calc(100vh - 32px);overflow:auto;border-radius:14px;flex-direction:column;align-items:stretch}" +
      ".of-cb--box,.of-cb--modal{flex-wrap:nowrap}.of-cb--box .of-cb-text,.of-cb--modal .of-cb-text{flex:none}" +
      ".of-cb-overlay{position:fixed;inset:0;background:rgba(0,0,0,.45);z-index:2147483645}" +
      ".of-cb-text{flex:1 1 320px;min-width:0}.of-cb-logo{display:block;max-height:28px;max-width:140px;margin-bottom:8px}" +
      ".of-cb-title{margin:0 0 4px;font-size:15px;font-weight:600}.of-cb-body,.of-cb-note{margin:0;opacity:.85}.of-cb-note{margin-top:6px;font-size:13px}" +
      ".of-cb a{color:inherit;text-decoration:underline}" +
      ".of-cb-btns{display:flex;flex-wrap:wrap;gap:8px}" +
      ".of-cb-btn{font:inherit;font-weight:600;cursor:pointer;border-radius:8px;padding:9px 16px;min-height:40px;border:1px solid var(--pri);background:var(--pri);color:var(--pri-fg)}" +
      ".of-cb-btn--plain{background:transparent;color:var(--fg);border-color:var(--line)}" +
      ".of-cb-btn:focus-visible,.of-cb-reopen:focus-visible,.of-cb input:focus-visible{outline:2px solid var(--pri);outline-offset:2px}" +
      ".of-cb-prefs{width:100%;display:grid;gap:8px}" +
      ".of-cb-cat{display:flex;gap:12px;align-items:flex-start;justify-content:space-between;border-top:1px solid var(--line);padding-top:8px}" +
      ".of-cb-cat p{margin:2px 0 0;font-size:13px;opacity:.8}.of-cb-cat input{width:18px;height:18px;margin:2px 0 0;accent-color:var(--pri)}.of-cb-on{font-size:12px;opacity:.75;white-space:nowrap}" +
      ".of-cb-reopen{position:fixed;bottom:16px;left:16px;z-index:2147483646;width:40px;height:40px;border-radius:50%;border:1px solid var(--line);background:var(--bg);color:var(--fg);cursor:pointer;box-shadow:0 2px 10px rgba(0,0,0,.18);display:flex;align-items:center;justify-content:center;padding:0}" +
      "@media (max-width:600px){.of-cb--bar{flex-direction:column;align-items:stretch}.of-cb-btns>.of-cb-btn{flex:1 1 auto}}" +
      (c.custom_css || "");
  }
  function mount(html) {
    if (!d.body) { d.addEventListener("DOMContentLoaded", function () { mount(html); }); return; }
    if (!host) {
      host = d.createElement("div");
      host.setAttribute("data-outflare-consent", "");
      root = host.attachShadow ? host.attachShadow({ mode: "open" }) : host;
      root.addEventListener("click", clicked);
      d.body.appendChild(host);
    }
    root.innerHTML = "<style>" + css(cfg.cfg) + "</style>" + html;
    var first = root.querySelector && root.querySelector(".of-cb--modal [data-of-consent]");
    if (first && first.focus) first.focus();
  }
  function hideBanner() { if (host) { host.parentNode && host.parentNode.removeChild(host); host = root = null; } }
  function showReopen() {
    if (!cfg.cfg.reopen_button) return hideBanner();
    mount('<button type="button" class="of-cb-reopen" data-of-consent="settings" aria-label="' + esc(cfg.cfg.texts.settings) + '" title="' + esc(cfg.cfg.texts.settings) +
      '"><svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M12 2a10 10 0 1 0 10 10 4 4 0 0 1-5-5 4 4 0 0 1-5-5"/><circle cx="8.5" cy="8.5" r="1"/><circle cx="16" cy="15.5" r="1"/><circle cx="11" cy="16" r="1"/></svg></button>');
  }
  function showBanner(prefs) {
    if (!on()) return;
    prefsOpen = prefs;
    var c = cfg.cfg, t = c.texts, r = rule(), html = "";
    if (c.custom_html) return mount('<div class="of-cb-custom">' + c.custom_html + "</div>");
    var layout = c.layout === "modal" ? "modal" : c.layout === "box" ? "box" : "bar";
    var btn = function (act, label, plain) { return '<button type="button" class="of-cb-btn' + (plain ? " of-cb-btn--plain" : "") + '" data-of-consent="' + act + '">' + esc(label) + "</button>"; };
    if (layout === "modal") html += '<div class="of-cb-overlay"></div>';
    html += '<div class="of-cb of-cb--' + layout + " of-cb--" + esc(c.position) + '" role="dialog" aria-labelledby="of-cb-title"' + (layout === "modal" ? ' aria-modal="true"' : "") + ">";
    html += '<div class="of-cb-text">' + (c.logo_url ? '<img class="of-cb-logo" alt="" src="' + esc(c.logo_url) + '">' : "") +
      '<p class="of-cb-title" id="of-cb-title">' + esc(t.title) + "</p>" +
      '<p class="of-cb-body">' + esc(t.body) + (c.policy_url ? ' <a href="' + esc(c.policy_url) + '">' + esc(t.policy) + "</a>" : "") + "</p>" +
      (honoursGPC() && r !== "opt_in" ? '<p class="of-cb-note">' + esc(t.gpc_honoured) + "</p>" : "") + "</div>";
    if (prefs) {
      html += '<div class="of-cb-prefs"><div class="of-cb-cat"><div><strong>' + esc(t.necessary) + "</strong><p>" + esc(t.necessary_desc) + '</p></div><span class="of-cb-on">' + esc(t.always_on) + "</span></div>";
      CATS.forEach(function (k) {
        if (!c.categories[k]) return;
        html += '<label class="of-cb-cat"><div><strong>' + esc(t[k]) + "</strong><p>" + esc(t[k + "_desc"]) + '</p></div><input type="checkbox" data-of-category="' + k + '"' + (cats[k] ? " checked" : "") + "></label>";
      });
      html += "</div>";
    }
    html += '<div class="of-cb-btns">';
    if (r === "opt_in") html += btn("reject", t.reject) + btn(prefs ? "save" : "customize", prefs ? t.save : t.customize, true) + btn("accept", t.accept);
    else if (r === "opt_out") html += btn("opt_out", t.opt_out) + btn(prefs ? "save" : "customize", prefs ? t.save : t.customize, true) + btn("accept", t.ok);
    else html += btn(prefs ? "save" : "customize", prefs ? t.save : t.customize, true) + btn("accept", t.ok);
    mount(html + "</div></div>");
  }
  function clicked(e) {
    var b = e.target && e.target.closest ? e.target.closest("[data-of-consent]") : null;
    if (!b) return;
    var act = b.getAttribute("data-of-consent"), all = {};
    if (act === "settings" || act === "customize") return showBanner(true);
    if (act === "accept" || act === "reject") {
      CATS.forEach(function (k) { all[k] = act === "accept"; });
      return choose(all, act + "_all");
    }
    if (act === "opt_out") return choose({ marketing: false }, "opt_out");
    if (act === "save") {
      var boxes = root.querySelectorAll("[data-of-category]");
      for (var i = 0; i < boxes.length; i++) all[boxes[i].getAttribute("data-of-category")] = !!boxes[i].checked;
      return choose(all, "custom");
    }
  }
  // Links such as "Cookie settings" in the site's footer open the banner.
  d.addEventListener("click", function (e) {
    var a = e.target && e.target.closest ? e.target.closest("[data-outflare-consent-open]") : null;
    if (a && on()) { if (e.preventDefault) e.preventDefault(); showBanner(true); }
  }, true);
  // Scripts added later, by the site or a tag manager, are checked too.
  if (w.MutationObserver) {
    try { new MutationObserver(function () { unblock(); }).observe(d.documentElement, { childList: true, subtree: true }); } catch (e) {}
  }
  w.addEventListener("load", function () { vendorSignals(!decided() && rule() === "opt_in"); unblock(); });

  // ---- Cookie policy ----
  // Elements with data-outflare-cookie-policy show the site's cookie
  // policy, which Outflare keeps up to date from what the site uses.
  function showPolicy() {
    var spots = d.querySelectorAll ? d.querySelectorAll("[data-outflare-cookie-policy]") : [];
    if (!spots.length || !w.fetch) return;
    fetch(api + "/v1/site/cookie-policy?format=html&key=" + encodeURIComponent(key))
      .then(function (res) { return res.ok ? res.text() : ""; })
      .then(function (htmlText) {
        if (!htmlText) return;
        for (var i = 0; i < spots.length; i++) {
          spots[i].innerHTML = htmlText;
          // Without the banner there is nothing for the button to open.
          if (!on()) {
            var b = spots[i].querySelector(".of-cookie-policy-settings");
            if (b && b.parentNode) b.parentNode.removeChild(b);
          }
        }
      }, function () {});
  }
  // Once a day, a browser tells Outflare which cookies, storage keys,
  // scripts and embeds the page has, so the policy lists what the site
  // really uses, including what scripts set after the page loaded. Only
  // names go, never a value, a query string or who the visitor is.
  // data-cookies="off" stops it.
  var REPORTED = "of_cs";
  function nameOf(src) {
    try {
      var u = new URL(src, location.href);
      if (!/^https?:$/.test(u.protocol)) return "";
      return u.hostname.toLowerCase() + "/" + u.pathname.split("/")[1];
    } catch (e) { return ""; }
  }
  function reportCookies() {
    if (script.getAttribute("data-cookies") === "off" || !w.fetch) return;
    try {
      var last = Number(localStorage.getItem(REPORTED) || 0);
      if (Date.now() - last < 86400000) return;
      localStorage.setItem(REPORTED, String(Date.now()));
    } catch (e) { return; }
    var body = { key: key, path: location.pathname, cookies: [], local: [], session: [], scripts: [], iframes: [] }, i, n;
    d.cookie.split(";").forEach(function (c) {
      n = c.split("=")[0].replace(/^\s+|\s+$/g, "");
      if (n && body.cookies.indexOf(n) < 0) body.cookies.push(n);
    });
    try { for (i = 0; i < localStorage.length; i++) body.local.push(localStorage.key(i)); } catch (e) {}
    try { for (i = 0; i < sessionStorage.length; i++) body.session.push(sessionStorage.key(i)); } catch (e) {}
    var tags = d.querySelectorAll ? d.querySelectorAll("script[src],iframe[src]") : [];
    for (i = 0; i < tags.length; i++) {
      n = nameOf(tags[i].getAttribute("src"));
      var list = tags[i].tagName === "IFRAME" ? body.iframes : body.scripts;
      if (n && list.indexOf(n) < 0) list.push(n);
    }
    ["cookies", "local", "session", "scripts"].forEach(function (k) { body[k] = body[k].slice(0, 200); });
    body.iframes = body.iframes.slice(0, 100);
    if (on()) body.granted = { analytics: cats.analytics, marketing: cats.marketing, preferences: cats.preferences };
    try { fetch(api + "/v1/site/cookies", { method: "POST", body: JSON.stringify(body), keepalive: true, headers: { "Content-Type": "text/plain" } }).then(null, function () {}); } catch (e) {}
  }
  function whenLoaded(fn) {
    if (d.readyState === "complete") fn();
    else w.addEventListener("load", fn);
  }
  whenLoaded(function () { showPolicy(); setTimeout(reportCookies, 3000); });

  var queued = Array.isArray(w.outflare) ? w.outflare : [];
  w.outflare = {
    loaded: true,
    page: page,
    track: track,
    // The visitor's id, to send with identify from your server when they
    // sign up or sign in, so their earlier visits join their record.
    anonymousId: function () { return anon; },
    // The visitor's choices: {necessary, analytics, marketing, preferences,
    // decided, region, rule, gpc}. With true they accept every category
    // (analytics keeps their id in a cookie for a year), with false they
    // refuse or withdraw them all (the cookie is removed), and with
    // {marketing: false} they set some. Each change is kept in Outflare.
    consent: function (yes) {
      if (arguments.length) {
        var next = {};
        if (yes && typeof yes === "object") next = yes;
        else CATS.forEach(function (k) { next[k] = !!yes; });
        choose(next, "api");
      }
      return choices();
    },
    // Opens the banner on its categories, such as from "Cookie settings".
    showConsent: function () { if (on()) showBanner(true); },
    // Runs fn(choices) now and whenever they change.
    onConsent: function (fn) {
      if (typeof fn !== "function") return;
      listeners.push(fn);
      try { fn(choices()); } catch (e) {}
    },
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
  if (on()) settle(true);
  else unblock();
  loadBanner();
  page();
  queued.forEach(function (c) {
    if (c && w.outflare[c[0]]) w.outflare[c[0]].apply(null, c.slice(1));
  });
})();
