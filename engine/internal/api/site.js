/*! Outflare website tracking. https://outflare.ai/docs/ */
// Sends a page view on every page (and on in-app navigation), with the
// landing page's campaign tags, ad click ids and referral code exactly as
// they came. Nothing is classified here: Outflare works out channels and
// attribution on the server. Visitors are known only by a random id; your
// own product links it to a person by sending identify with userId and this
// anonymousId (outflare.anonymousId()) when they sign up or sign in.
// Sites whose backend reports nothing can call outflare.signup(email)
// instead, so a partner's referral shows as a lead at once.
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
// data-reopen="off" leaves out the corner button that reopens it, for a
// page with its own "Cookie settings" link.
//
// Page speed: each page load reports how long it took to load, respond to
// clicks and settle (Core Web Vitals), with no visitor id, for Page speed
// in Outflare. data-vitals="off" stops it.
//
// Cookie policy: an element with data-outflare-cookie-policy shows the
// site's cookie policy, which Outflare keeps up to date. Once a day a
// browser reports the names (never the values) of the page's cookies,
// storage keys, scripts and embeds for it; data-cookies="off" stops that.
//
// Feature flags and A/B tests: tests made in Outflare's visual editor run
// with nothing on the tag; the script asks for the visitor's variants by
// itself while the site has one (data-flags="off" stops it). For the
// page's own code, with data-flags on the tag (or the first call to
// outflare.flag), the script asks for the visitor's variants.
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
// Session recordings and heatmaps: turned on for the brand in Outflare
// (Recordings, Heatmaps), with nothing on the tag. Recordings load a
// second script only for the visits that are recorded, and only with the
// visitor's consent to analytics where consent is needed. What a visitor
// types is never recorded: every field is masked in the page. Give an
// element data-of-mask to hide its text too, or data-of-block to leave it
// out altogether. Heatmaps count where a page is clicked and how far it
// is scrolled, with no visitor id. data-replay="off" stops both.
//
// Autocapture: turned on for the site key in Outflare (Website tracking,
// "Capture clicks" and "Capture form submits"), with nothing on the tag.
// A click on a link or button is sent as Element Clicked with the
// element's tag, its label, id, classes and where a link goes (no query
// string); a form submit as Form Submitted with the form's id, name and
// address. What a visitor typed is never read: no field values, and a
// label that looks like an email address, a number or a key is left out.
// data-outflare-event="Plan Chosen" on an element names its click
// yourself, with or without the switch. data-outflare-ignore leaves an
// element and everything in it out; data-autocapture="off" on the tag
// stops it all.
//
// Ad pixels: with the site key's "Load the ChatGPT Ads pixel" switch on
// (Website tracking), the script loads OpenAI's Measurement Pixel for the
// Pixel ID on the brand's ChatGPT Ads connection, counts each page view
// with it, and gives it the signed-in visitor's hashed email, phone and
// id, so the site needs no pixel snippet and no code that passes user
// data. The hashes come from Outflare's server, for a browser your product
// identified (outflare.identify, or identify from your server with the
// visitor's anonymousId); the script never sees an email address. Nothing
// loads without the visitor's consent to marketing, and withdrawing it
// tells the pixel. A page that still carries OpenAI's own snippet keeps
// it: the script then only adds the user data. Sign-ups and purchases
// still go from Outflare's server, not from the page. data-pixels="off"
// on the tag stops it.
//
// With "Load the Meta pixel" or "Load the TikTok pixel" on, the script
// loads the pixel the brand's conversions for that platform already go
// to, counts each page view, and gives it the signed-in visitor's hashes
// the same way. When a track call is one of those conversions, the script
// also sends it to the pixel with the id Outflare's server sends it with,
// so the platform counts it once and can match it on the browser's own
// cookies. A page that carries the platform's own snippet keeps it, and
// the script then does nothing for that platform.
//
// Errors: with the site key's "Capture errors" switch on, uncaught errors
// and rejected promises are sent as Error Caught (type, message, stack,
// where it was thrown), and Outflare's Errors page groups them into
// issues. outflare.captureException(err, {step: "checkout"}) sends one the
// page caught itself, with or without the switch. Email addresses and long
// keys are taken out of the message and stack before anything is sent, and
// query strings out of the stack's addresses. data-release="1.4.2" (or
// outflare.setRelease) sends your version or commit with each error, so
// the Errors page shows the release an error first appeared in. Errors
// thrown in browser extensions and on pages opened from a file aren't
// sent. Each error also carries the window's size and the visitor's
// light or dark and reduced-motion settings; never anything typed.
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
  var VERSION = "25";
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
  var clickIDs = ["gclid", "gbraid", "wbraid", "dclid", "fbclid", "msclkid", "li_fat_id", "rdt_cid", "twclid", "oppref", "ttclid", "ScCid", "epik", "tblci", "ob_click_id", "qclid", "irclickid"];
  var adParams = ["campaign", "adset", "ad", "placement"];
  var adCookies = { _fbp: "fbp", _fbc: "fbc", _gcl_aw: "gcl_aw", _ttp: "ttp", _scid: "scid", __obref: "obref" };

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
  // id lives in this tab's sessionStorage for the visit (of_vid), so every
  // page of the visit is one visitor and sees the same versions, and it is
  // gone when the tab closes.
  function setCookie(name, value, keep) {
    var parts = location.hostname.split("."), attrs = "; path=/; SameSite=Lax" + (location.protocol === "https:" ? "; Secure" : "");
    attrs += "; max-age=" + (keep ? YEAR : 0);
    for (var i = Math.max(parts.length - 2, 0); i >= 0; i--) {
      var domain = parts.slice(i).join(".");
      d.cookie = name + "=" + value + attrs + (i < parts.length - 1 ? "; domain=" + domain : "");
      if (keep ? readCookie(name, "[^;]*") === value : !readCookie(name, "[^;]*")) return;
    }
  }
  // The signed-in hint: your app calls outflare.signedIn(true) after
  // sign-in and outflare.signedIn(false) on sign-out. It is kept as a
  // cookie that only ever holds "1", on the widest domain, so the
  // marketing site knows a visitor has an account without knowing who
  // they are. It needs no consent: it names nobody and is set by the
  // site's own sign-in. It is a hint for what to show, never proof:
  // anyone can set it in their own browser, so the server must still
  // check access.
  var SIGNED = "of_signed_in";
  function isSignedIn() { return readCookie(SIGNED, "1(?=;|$)") === "1"; }
  // Elements marked data-outflare-signed-in="show" are shown only to
  // signed-in visitors, and "hide" only to the others.
  function showSignedIn() {
    var inn = isSignedIn();
    [["show", inn], ["hide", !inn]].forEach(function (p) {
      try {
        var els = d.querySelectorAll('[data-outflare-signed-in="' + p[0] + '"]');
        for (var i = 0; i < els.length; i++) els[i].style.setProperty("display", p[1] ? "" : "none");
      } catch (e) {}
    });
  }
  var VISIT = "of_vid";
  function keepVisit(id) { try { sessionStorage.setItem(VISIT, id); } catch (e) {} }
  function writeCookie(id) { setCookie(COOKIE, id, consented); keepVisit(id); }
  // An id cookie already there was set with consent.
  var stored = readCookie(), visitID = "";
  try { visitID = sessionStorage.getItem(VISIT) || ""; } catch (e) {}
  if (!/^[A-Za-z0-9-]{8,64}$/.test(visitID)) visitID = "";
  var anon = stored || visitID || uuid(), carriedID = false, started = Date.now();
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
      (b.anonymousId === anon || (!stored && !visitID && !carriedID))) {
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
  var bannerOff = script.getAttribute("data-banner") === "off", reopenOff = script.getAttribute("data-reopen") === "off", cfg = null, CFG = "of_cb";
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
  keepVisit(anon);

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
  // The editor's phone preview (a window named of_phone) shows the page
  // without the editor.
  if (window.name === "of_phone") editToken = "";
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
  var clickKey = "";
  // directKey is this tab's click key for a direct-mode campaign: 16
  // random letters and digits, as a redirect's.
  function directKey(ofc) {
    var name = "of_dclk_" + ofc, v = "";
    try { v = sessionStorage.getItem(name) || ""; } catch (e) {}
    if (/^[a-z0-9]{16}$/.test(v)) return v;
    var c = w.crypto, abc = "abcdefghijklmnopqrstuvwxyz0123456789", out = "";
    if (!c || !c.getRandomValues) return "";
    var a = new Uint8Array(48);
    c.getRandomValues(a);
    for (var i = 0; i < a.length && out.length < 16; i++) if (a[i] < 252) out += abc.charAt(a[i] % 36);
    if (out.length < 16) return "";
    try { sessionStorage.setItem(name, out); } catch (e) {}
    return out;
  }
  // fillClickKey puts the click key in the page's links that ask for it
  // with {ofclk}, such as an offer's sub id.
  function fillClickKey() {
    if (!clickKey) return;
    var links = d.querySelectorAll('a[href*="{ofclk}"], a[href*="%7Bofclk%7D"], a[href*="%7bofclk%7d"]');
    for (var i = 0; i < links.length; i++) {
      links[i].setAttribute("href", links[i].getAttribute("href").replace(/\{ofclk\}|%7Bofclk%7D/gi, clickKey));
    }
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
    // Google's auto-tagging adds the campaign id itself, so a click is
    // tied to its campaign even before the tracking template is set.
    if (!ad.campaign && q.get("gad_campaignid")) { ad.campaign = q.get("gad_campaignid"); anyAd = true; }
    if (anyAd) p.ad = ad;
    if (q.get("ofc")) p.ofc = q.get("ofc");
    // A redirect link's click key, so the visit can claim the click.
    if (/^[a-z0-9]{16}$/.test(q.get("ofclk") || "")) p.ofclk = q.get("ofclk");
    // Direct mode: an ad straight to this page with a tracker campaign's
    // ofc and no click key gets one named here, kept for this tab's visit,
    // so the visit records the click and the page's links can carry it.
    else if (p.ofc) {
      var dk = directKey(p.ofc);
      if (dk) { p.ofclk = dk; p.direct = true; }
    }
    if (p.ofclk) clickKey = p.ofclk;
    var ref = q.get("ref") || q.get("via");
    if (ref) p.ref = ref;
    return p;
  }

  // Page speed: once per page load, when the page is left, how long the
  // largest element took to show (LCP), the slowest response to a click or
  // key (INP), how much the layout jumped (CLS), first paint and the
  // server's response. It carries no visitor id and no query string, so it
  // needs no consent. data-vitals="off" stops it.
  function vitals() {
    if (script.getAttribute("data-vitals") === "off" || !w.PerformanceObserver || !w.performance) return;
    var v = {}, sent = false, url = location.origin + location.pathname, path = location.pathname;
    var hidden = d.visibilityState === "hidden", jumps = 0, burst = 0, burstStart = 0, burstLast = 0, slowest = {};
    function watchEntries(opts, fn) {
      try { new PerformanceObserver(function (l) { l.getEntries().forEach(fn); }).observe(opts); } catch (e) {}
    }
    // A page opened in the background has no real loading time.
    if (!hidden) {
      watchEntries({ type: "largest-contentful-paint", buffered: true }, function (e) { if (!hidden) v.lcp = e.startTime; });
      watchEntries({ type: "paint", buffered: true }, function (e) { if (e.name === "first-contentful-paint") v.fcp = e.startTime; });
    }
    // CLS: the worst burst of jumps (each under a second apart, five
    // seconds at most), leaving out those right after the visitor's input.
    watchEntries({ type: "layout-shift", buffered: true }, function (e) {
      if (e.hadRecentInput) return;
      if (burst && e.startTime - burstLast < 1000 && e.startTime - burstStart < 5000) burst += e.value;
      else { burst = e.value; burstStart = e.startTime; }
      burstLast = e.startTime;
      if (burst > jumps) jumps = burst;
      v.cls = jumps;
    });
    // INP: each click or key's longest event; the slowest of them, or one
    // in fifty on a page with many.
    watchEntries({ type: "event", buffered: true, durationThreshold: 40 }, function (e) {
      if (e.interactionId && !(slowest[e.interactionId] >= e.duration)) slowest[e.interactionId] = e.duration;
    });
    try {
      var nav = performance.getEntriesByType("navigation")[0];
      if (nav && nav.responseStart > 0) v.ttfb = nav.responseStart;
    } catch (e) {}
    function flush() {
      if (sent) return;
      var all = [];
      for (var k in slowest) all.push(slowest[k]);
      if (all.length) {
        all.sort(function (a, b) { return b - a; });
        v.inp = all[Math.min(all.length - 1, Math.floor(all.length / 50))];
      }
      var props = {}, any = false;
      ["lcp", "inp", "cls", "fcp", "ttfb"].forEach(function (k) {
        if (typeof v[k] === "number" && isFinite(v[k]) && v[k] >= 0) {
          props[k] = k === "cls" ? Math.round(v[k] * 10000) / 10000 : Math.round(v[k]);
          any = true;
        }
      });
      if (!any) return;
      sent = true;
      send([{ type: "vitals", messageId: uuid(), properties: props, context: { page: { url: url, path: path } } }]);
    }
    d.addEventListener("visibilitychange", function () {
      if (d.visibilityState === "hidden") { hidden = true; flush(); }
    }, true);
    w.addEventListener("pagehide", flush, true);
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
        .then(function (res) {
          if (res.status >= 500) return retry();
          // The answer says whether this brand records visits or draws heatmaps.
          if (res.ok && res.json) res.json().then(function (o) { if (o) { gotReplay(o.replay || null); gotCapture(o.capture || null); gotPixels(o.pixels || null); } }, function () {});
        }, retry);
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
    if (isSignedIn()) c.signed_in = true;
    return c;
  }
  function page(name, properties) {
    var q = new URLSearchParams(location.search), props = landing(q);
    if (clickKey) whenLoaded(fillClickKey);
    for (var k in properties || {}) props[k] = properties[k];
    if (visitToken) { props.ofvisit = visitToken; visitToken = ""; }
    if (flagsWanted && lastURL && lastURL !== location.href) loadFlags();
    if (lastURL && lastURL !== location.href) heatPage();
    var e = { type: "page", anonymousId: anon, messageId: uuid(), properties: props, context: context(q) };
    if (name) e.name = String(name);
    send([e]);
    lastURL = location.href;
    if (pixelOn) applyPixels();
  }
  // track returns the event's messageId: pass it to an ad pixel as its
  // event id (Meta's eventID), so a conversion Outflare also sends back
  // server to server is counted once. options.messageId sets that id
  // yourself, the same one your server sends for the same event (such as
  // "signup-" + userId): Outflare then sends the conversion to ad
  // platforms once, not once per copy.
  function track(event, properties, options) {
    if (!event) return;
    var own = options && options.messageId != null ? String(options.messageId).trim() : "";
    var q = new URLSearchParams(location.search), id = own && own.length <= 200 ? own : uuid(), props = {}, ids = {};
    for (var k in properties || {}) props[k] = properties[k];
    // A conversion carries the ad pixels' cookies, for sending it back.
    if (!props.click_ids && cookieIDs(ids)) props.click_ids = ids;
    send([{ type: "track", event: String(event), anonymousId: anon, messageId: id, properties: props, context: context(q) }]);
    pixelEvent(event, props, id);
    return id;
  }

  // Flags: asked for once per page (and again on in-app navigation, since
  // rules can depend on the page), and kept for the visit so the next page
  // starts with the last answer.
  var FLAGS = "of_flags", SEEN = "of_flags_seen", flagsAttr = script.getAttribute("data-flags"), classFlags = flagsAttr !== null && flagsAttr !== "off", flagsWanted = classFlags, flagState = null, flagWaiters = [], flagClasses = [], seen = {}, codeAsked = false;
  // No-code tests: Outflare says whether the site runs tests made in the
  // visual editor (TESTS, kept a day when it does, ten minutes when it
  // doesn't), so the script asks for the visitor's versions on its own,
  // with no data-flags on the tag. data-flags="off" stops that. HIDE is
  // what the visitor's versions change: on their next visit, the guard in
  // <head> (or this script, if it runs first) hides just those parts until
  // their version shows, for a second at most.
  var TESTS = "of_tests", HIDE = "of_hide", hint = null;
  try { hint = JSON.parse(localStorage.getItem(TESTS) || "null"); } catch (e) {}
  if (!hint || hint.key !== key || hint.until < Date.now()) hint = null;
  // A preview link or the editor always asks: a draft test's versions
  // are shown to the person who made them before the site runs tests.
  var previewing = false;
  for (var pk in preview) if (Object.prototype.hasOwnProperty.call(preview, pk)) previewing = true;
  if (flagsAttr !== "off" && (!hint || hint.on || previewing || editToken)) flagsWanted = true;
  function safeSelectors(sel) { return typeof sel === "string" && sel && sel.length < 16000 && !/[{}<@;]|\/\*/.test(sel); }
  var hideEl = d.getElementById("of-hide");
  if (!hideEl && flagsWanted) try {
    var h = JSON.parse(localStorage.getItem(HIDE) || "null");
    if (h && h.until > Date.now() && safeSelectors(h.sel) && d.head) {
      hideEl = d.createElement("style");
      hideEl.id = "of-hide";
      hideEl.textContent = h.sel + "{opacity:0!important}";
      d.head.appendChild(hideEl);
      w.setTimeout && w.setTimeout(unhide, 1000);
    }
  } catch (e) {}
  function unhide() { if (hideEl && hideEl.parentNode) hideEl.parentNode.removeChild(hideEl); hideEl = null; }
  // Remember Outflare's answer for the next page and visit.
  function remember(out) {
    if (typeof out.tests !== "boolean") return;
    try {
      localStorage.setItem(TESTS, JSON.stringify({ key: key, on: out.tests, until: Date.now() + (out.tests ? 86400000 : 600000) }));
      var sel = (out.hide || []).join(",");
      if (out.tests && safeSelectors(sel)) localStorage.setItem(HIDE, JSON.stringify({ sel: sel, until: Date.now() + 30 * 86400000 }));
      else localStorage.removeItem(HIDE);
    } catch (e) {}
    // A site with no visual tests: stop asking, unless the page wants flags.
    if (!out.tests && !classFlags && !codeAsked && !previewing && !editToken) flagsWanted = false;
  }
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
  function applyFlags(state, source, expose) {
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
    // No-code tests this page is in, from a fresh answer for this page:
    // the visitor has seen their version, whichever it is (the control
    // changes nothing, so applying changes alone would never count it).
    if (expose && expose.length) for (var e = 0; e < expose.length && e < 100; e++) if (typeof expose[e] === "string") exposed(expose[e]);
    applyChanges();
    // Hidden parts show once their version is in: now, or once the page
    // has loaded when it is still loading.
    if (d.readyState !== "loading") unhide();
    root.classList.remove("of-wait");
    var waiting = flagWaiters;
    flagWaiters = [];
    waiting.forEach(function (fn) { try { fn(flagMap()); } catch (e) {} });
  }
  // Visual changes: a version's payload can list edits to make to the page
  // ({"changes": [{"selector", and one of "text", "rich", "hide", "href",
  // "src" and "alt", "style", "move" with "target", or "insert"}]}, from the
  // visual editor), its own CSS ("css") and a page to send its visitors to
  // ("redirect"). They apply when the flags arrive, again once the page has
  // loaded, a moment later and whenever the page changes (single-page apps
  // and content that comes late), and making them counts as showing the
  // version. Text goes in as text and rich text is built as elements, never
  // parsed as HTML; links and images only go to http(s) or a path; styles
  // and CSS never load anything; and a redirect only goes to the site's own
  // domains.
  function safeURL(u) {
    return typeof u === "string" && u.length <= 2000 && !/\s/.test(u) && (/^\/(?!\/)/.test(u) || /^https?:\/\/[^/]/i.test(u));
  }
  var STYLE = /^(color|background-color|font-size|font-weight|font-style|font-family|text-align|text-decoration|text-transform|line-height|letter-spacing|padding(-top|-right|-bottom|-left)?|margin(-top|-right|-bottom|-left)?|border(-color|-width|-style|-radius)?|(max-|min-)?width|(max-|min-)?height|display|opacity|gap|order|justify-content|align-items|flex-direction|box-shadow|visibility)$/;
  function safeStyle(v) { return typeof v === "string" && /^[#(),.%\-+\w\s/"']{1,100}$/.test(v) && !/url|expression|image/i.test(v); }
  function safeCSS(css) { return typeof css === "string" && css.length <= 8000 && !/<|\\|url\(|@import|@font-face|@charset|@namespace|expression\(|javascript:|behavior|-moz-binding|image-set\(|image\(/i.test(css); }
  // Rich text: **bold**, *italic*, [a link](/path) and line breaks, built
  // as elements.
  function rich(el, src) {
    while (el.firstChild) el.removeChild(el.firstChild);
    var re = /\*\*([^*]+)\*\*|\*([^*]+)\*|\[([^\]]+)\]\(([^)\s]+)\)|\n/g, last = 0, m;
    while ((m = re.exec(src))) {
      if (m.index > last) el.appendChild(d.createTextNode(src.slice(last, m.index)));
      var n;
      if (m[1] !== undefined) { n = d.createElement("strong"); n.textContent = m[1]; }
      else if (m[2] !== undefined) { n = d.createElement("em"); n.textContent = m[2]; }
      else if (m[3] !== undefined) {
        if (safeURL(m[4])) { n = d.createElement("a"); n.setAttribute("href", m[4]); n.textContent = m[3]; }
        else n = d.createTextNode(m[3]);
      } else n = d.createElement("br");
      el.appendChild(n);
      last = re.lastIndex;
    }
    if (last < src.length) el.appendChild(d.createTextNode(src.slice(last)));
  }
  function phone() { try { return !!(w.matchMedia && w.matchMedia("(max-width: 767px)").matches); } catch (e) { return false; } }
  // The site's own registrable domains: the page's, and data-domains.
  function ownSite(u) {
    try {
      var to = new URL(u, location.href);
      if (to.host === location.host) return true;
      var a = to.hostname.toLowerCase().replace(/^www\./, ""), here = location.hostname.toLowerCase().replace(/^www\./, "");
      var list = domains.concat([here]);
      for (var i = 0; i < list.length; i++) if (a === list[i] || a.slice(-list[i].length - 1) === "." + list[i]) return true;
    } catch (e) {}
    return false;
  }
  // A new button looks like the page's own: it takes the classes of the
  // first button-like link on the page, over a plain default that any of
  // the site's own styles beat.
  var btnClass = null;
  function buttonClass() {
    if (btnClass !== null) return btnClass;
    btnClass = "of-button";
    try {
      var own = d.querySelector("a[class*='btn']:not([data-of-insert]),a[class*='button']:not([data-of-insert]),button[class*='btn'],button[class*='button']");
      if (own && own.className && typeof own.className === "string") btnClass = own.className + " of-button";
      if (d.head && !d.getElementById("of-button-css")) {
        var bs = d.createElement("style");
        bs.id = "of-button-css";
        bs.textContent = ":where(.of-button){display:inline-block;padding:10px 18px;border-radius:6px;background:#111827;color:#fff;font-weight:600;text-decoration:none;margin:8px 0}";
        d.head.appendChild(bs);
      }
    } catch (e) {}
    return btnClass;
  }
  var redirected = false, missing = {};
  function applyChanges() {
    if (!flagState || !flagState.payloads) return;
    var mobile = phone();
    for (var k in flagState.payloads) {
      var p = flagState.payloads[k], cs = (p && p.changes) || [];
      // A payload kept from before is for the version the answer named: a
      // preview of another version waits for its own.
      if (!variant(k) || flagState.flags[k] !== variant(k) || !p || typeof p !== "object") continue;
      var applied = false, miss = missing[k] = { found: 0, lost: [] };
      // A split URL test: show this version's visitors another page (not
      // while someone edits the version, so the editor stays put).
      if (typeof p.redirect === "string" && p.redirect && !redirected && !editToken && safeURL(p.redirect) && ownSite(p.redirect)) {
        var to = new URL(p.redirect, location.href);
        if (to.pathname !== location.pathname || to.host !== location.host) {
          new URLSearchParams(location.search).forEach(function (v, q) { if (!to.searchParams.has(q)) to.searchParams.set(q, v); });
          redirected = true;
          exposed(k);
          location.replace(to.toString());
          return;
        }
      }
      if (typeof p.css === "string" && p.css && safeCSS(p.css) && d.head) {
        var sid = "of-css-" + k, st = d.getElementById(sid);
        if (!st) { st = d.createElement("style"); st.id = sid; d.head.appendChild(st); }
        if (st.textContent !== p.css) st.textContent = p.css;
        applied = true;
      }
      for (var i = 0; i < cs.length && i < 50; i++) {
        var c = cs[i], els = [];
        if (!c || typeof c.selector !== "string") continue;
        if ((c.device === "mobile" && !mobile) || (c.device === "desktop" && mobile)) continue;
        try { els = d.querySelectorAll(c.selector); } catch (e) { continue; }
        var target = null;
        if (c.move) { try { target = d.querySelector(c.target); } catch (e) {} }
        if (!els.length || (c.move && !target)) { miss.lost.push(els.length ? c.target : c.selector); continue; }
        miss.found++;
        if (c.insert) {
          var mark = k + ":" + i, inn = c.insert;
          if (d.querySelector("[data-of-insert='" + mark + "']")) { applied = true; continue; }
          var n = null;
          if (inn.kind === "image" && safeURL(inn.src)) { n = d.createElement("img"); n.setAttribute("src", inn.src); n.setAttribute("alt", String(inn.alt || "")); }
          else if ((inn.kind === "button" || inn.kind === "link") && safeURL(inn.href)) { n = d.createElement("a"); n.setAttribute("href", inn.href); n.textContent = String(inn.text || ""); if (inn.kind === "button") n.setAttribute("role", "button"); }
          else if (inn.kind === "text") { n = d.createElement("p"); n.textContent = String(inn.text || ""); }
          if (n) {
            n.setAttribute("data-of-insert", mark);
            if (inn.kind === "button") n.className = buttonClass();
            var at = els[0];
            if (at.parentNode) at.parentNode.insertBefore(n, inn.where === "before" ? at : at.nextSibling);
            applied = true;
          }
          continue;
        }
        for (var j = 0; j < els.length; j++) {
          var el = els[j];
          if (typeof c.text === "string") { if (el.textContent !== c.text) el.textContent = c.text; }
          else if (typeof c.rich === "string") { if (el.getAttribute("data-of-rich") !== c.rich) { rich(el, c.rich); el.setAttribute("data-of-rich", c.rich); } }
          else if (c.hide) el.style.setProperty("display", "none", "important");
          else if (safeURL(c.href)) el.setAttribute("href", c.href);
          else if (c.style && typeof c.style === "object") {
            for (var sp in c.style) if (STYLE.test(sp) && safeStyle(c.style[sp])) el.style.setProperty(sp, c.style[sp], "important");
          } else if (target) {
            if (el === target || el.contains(target)) continue;
            if (c.move === "before" && el.nextElementSibling !== target) target.parentNode && target.parentNode.insertBefore(el, target);
            else if (c.move === "after" && target.nextElementSibling !== el) target.parentNode && target.parentNode.insertBefore(el, target.nextSibling);
            else if (c.move === "start" && target.firstElementChild !== el) target.insertBefore(el, target.firstChild);
            else if (c.move === "end" && target.lastElementChild !== el) target.appendChild(el);
          } else if (c.src !== undefined || c.alt !== undefined) {
            if (safeURL(c.src)) el.setAttribute("src", c.src);
            if (typeof c.alt === "string") el.setAttribute("alt", c.alt);
          }
          applied = true;
        }
      }
      if (applied) exposed(k);
    }
  }
  if (d.readyState === "loading") d.addEventListener("DOMContentLoaded", function () { applyChanges(); if (flagState) unhide(); watchPage(); });
  // Pages that build themselves (single-page apps, content that loads
  // late): apply again shortly after the page changes. Applying is
  // idempotent, so it settles; a cap keeps a busy page cheap. While the
  // visual editor is open it owns the page, so its edits aren't undone.
  var watching = false, again = 0, runs = 0;
  function watchPage() {
    if (watching || editToken || !w.MutationObserver || !d.body) return;
    watching = true;
    new w.MutationObserver(function () {
      if (again || runs > 500 || !flagState || !flagState.payloads) return;
      again = w.setTimeout(function () { again = 0; runs++; applyChanges(); }, 60);
    }).observe(d.body, { childList: true, subtree: true });
  }
  if (d.readyState !== "loading") watchPage();
  w.setTimeout && w.setTimeout(function () { if (editToken) return; applyChanges(); reportMissing(); }, 1500);
  // Changes that found nothing on this page once it settled (the page was
  // redesigned since they were made) are reported once per visit and page,
  // so the test's checklist can say which to pick again.
  function reportMissing() {
    if (editToken) return;
    var done = {};
    try { done = JSON.parse(sessionStorage.getItem("of_missed") || "{}") || {}; } catch (e) {}
    for (var k in missing) {
      var m = missing[k], v = variant(k), id = k + "|" + location.pathname;
      if (!m.lost.length || !v || Object.prototype.hasOwnProperty.call(preview, k) || done[id]) continue;
      done[id] = 1;
      track("Experiment Change Missed", { flag: k, variant: v, path: location.pathname, missed: m.lost.length, found: m.found, selectors: m.lost.slice(0, 5) });
    }
    try { sessionStorage.setItem("of_missed", JSON.stringify(done)); } catch (e) {}
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
    // The server already decided this page.
    if (bootURL && bootURL === location.href) return applyFlags(flagState, "bootstrap");
    if (!w.fetch) return applyFlags({ anon: anon, flags: {}, payloads: {} });
    var body = JSON.stringify({ key: key, anonymousId: anon, url: location.href, referrer: lastURL || d.referrer, language: navigator.language || "", signedIn: isSignedIn(), preview: previewed() });
    fetch(api + "/v1/site/flags", { method: "POST", body: body, headers: { "Content-Type": "text/plain" } })
      .then(function (res) { return res.ok ? res.json() : null; })
      .then(function (out) {
        var state = { anon: anon, flags: (out && out.flags) || (flagState && flagState.flags) || {}, payloads: (out && out.payloads) || (flagState && flagState.payloads) || {} };
        if (out) try { sessionStorage.setItem(FLAGS, JSON.stringify(state)); } catch (e) {}
        if (out) remember(out);
        applyFlags(state, out ? "fetched" : "", out && out.expose);
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
    if (pixelCfg) whenLoaded(applyPixels);
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
    if (!cfg.cfg.reopen_button || reopenOff) return hideBanner();
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
  whenLoaded(function () { showPolicy(); showSignedIn(); setTimeout(reportCookies, 3000); });

  // ---- Session recordings and heatmaps ----
  // What to do comes with the answer to this page's events and is kept for
  // the visit, so the next page starts at once. A recording is one per
  // visit in this tab: REC keeps its id, the next part's number and
  // whether this visit was picked (the brand may record only a share).
  var REPLAY = "of_replay", REC = "of_rec", replayOff = script.getAttribute("data-replay") === "off", replayCfg = null, rec = null;
  var recStop = null, recLoading = false, recBuf = [], recSize = 0, recTimer = 0, heatOn = false, heatClicks = [], heatDepth = 0, heatAt = "", heatScreen = "", heatHooked = false, heatSent = false, snapAt = "", snapOn = false, snapKeep = null, recWait = [];
  try { replayCfg = JSON.parse(sessionStorage.getItem(REPLAY) || "null"); } catch (e) {}
  if (!replayCfg || replayCfg.key !== key) replayCfg = null;
  setShapes(replayCfg && replayCfg.hide);
  function pageHeight() {
    var e = d.documentElement, b = d.body || e;
    return Math.max(e.scrollHeight || 0, b.scrollHeight || 0, e.clientHeight || 0, 1);
  }
  function noteDepth() {
    var t = Math.max(1, Math.min(10, Math.ceil(((w.scrollY || w.pageYOffset || 0) + (w.innerHeight || 0)) / pageHeight() * 10)));
    if (t > heatDepth) heatDepth = t;
  }
  // What the page shows now, for a page whose address stays the same
  // while its content changes (the steps of a sign-up, a wizard): the
  // step marked as current, or the first heading in view. Each screen
  // gets its own heatmap. Hidden text is named by a number instead.
  function screenName() {
    try {
      var el = d.querySelector('[aria-current="step"]'), hs, i, r, t, n = 0;
      if (!el) {
        hs = d.querySelectorAll("h1, h2, [role=heading]");
        for (i = 0; i < hs.length && i < 30 && !el; i++) {
          r = hs[i].getBoundingClientRect();
          if (r.width > 0 && r.height > 0 && (hs[i].textContent || "").replace(/\s+/g, "")) el = hs[i];
        }
      }
      if (!el) return "";
      t = (el.textContent || "").replace(/[#\s]+/g, " ").replace(/^ | $/g, "").slice(0, 60);
      if (!t) return "";
      if ((replayCfg && replayCfg.mask_text) || (el.closest && el.closest("[data-of-mask], [data-of-block]"))) {
        for (i = 0; i < t.length; i++) n = (n * 31 + t.charCodeAt(i)) % 100000;
        return "Screen " + n;
      }
      return hideSecrets(t);
    } catch (e) { return ""; }
  }
  function noteClick(e) {
    if (!heatOn || typeof e.pageX !== "number") return;
    // A click on another screen of the same page: the last screen's
    // clicks are sent first, so each lands on its own heatmap.
    var sc = screenName();
    if (sc !== heatScreen && heatClicks.length) replaySend(false);
    heatScreen = sc;
    if (heatClicks.length >= 50) return;
    var wide = Math.max(d.documentElement.scrollWidth || 0, w.innerWidth || 0, 1);
    var x = Math.floor(e.pageX / wide * 100), y = Math.floor(e.pageY / 10);
    if (x >= 0 && x <= 99 && y >= 0 && y <= 5000) heatClicks.push([x, y]);
  }
  // The brand may record only some pages, or leave some out: a path, with
  // * for any run of characters.
  function pathIn(list, p) {
    for (var i = 0; list && i < list.length; i++) {
      var q = list[i];
      if (q === p) return true;
      if (q.indexOf("*") < 0) continue;
      try {
        if (new RegExp("^" + q.split("*").map(function (t) { return t.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"); }).join(".*") + "$").test(p)) return true;
      } catch (e) {}
    }
    return false;
  }
  function pageOn() {
    if (!replayCfg) return false;
    var p = location.pathname.replace(/(.)\/+$/, "$1");
    if (pathIn(replayCfg.exclude, p)) return false;
    return !(replayCfg.include && replayCfg.include.length) || pathIn(replayCfg.include, p);
  }
  function keepRec() { try { sessionStorage.setItem(REC, JSON.stringify(rec)); } catch (e) {} }
  function stopRec() {
    if (recStop) { try { recStop(); } catch (e) {} }
    recStop = null; recBuf = []; recSize = 0;
  }
  // Sends what has gathered: the recording's next part, and the page's
  // clicks. leaving adds how far down the page the visitor got, once.
  // The depth is sent once per page view, the first time the visitor
  // leaves or hides the page: on phones that is often the last chance.
  function replaySend(leaving) {
    var events = recStop && consented ? recBuf : [], clicks = heatOn ? heatClicks : [], depth = leaving && heatOn && !heatSent ? heatDepth : 0;
    if (recStop && !consented) stopRec();
    if (!events.length && !clicks.length && !depth) return;
    var body = { key: key, path: heatAt || location.pathname, width: w.innerWidth || 0, height: w.innerHeight || 0 };
    if (clicks.length || depth) body.screen = clicks.length ? heatScreen : screenName();
    if (clicks.length) body.clicks = clicks;
    if (depth) { body.depth = depth; body.page_height = pageHeight(); heatSent = true; }
    heatClicks = [];
    if (events.length) {
      // A leaving page's counts go on their own, so they arrive even
      // when the recording's part is too big to send while leaving.
      if (leaving && (clicks.length || depth)) {
        replayPost(body, true);
        body = { key: key, path: body.path, width: body.width, height: body.height };
      }
      body.session = rec.id; body.anonymousId = anon; body.part = rec.part++; body.events = events;
      keepRec();
    }
    recBuf = []; recSize = 0;
    replayPost(body, leaving);
  }
  function replayPost(body, leaving) {
    var text = JSON.stringify(body), url = api + "/v1/site/replay", small;
    // A last pass over what is sent, for a key in a place the others missed.
    if (body.events || body.snapshot) text = text.replace(SECRET_KNOWN, stars);
    small = text.length < 60000;
    try {
      if (leaving && small && navigator.sendBeacon && navigator.sendBeacon(url, text)) return;
      fetch(url, { method: "POST", body: text, keepalive: small, headers: { "Content-Type": "text/plain" } })
        .then(function (res) { return res.ok ? res.json() : null; })
        .then(function (o) {
          if (!o) return;
          if (o.recording === false && recStop) { stopRec(); rec.on = false; keepRec(); }
          if (o.heatmaps === false) heatOn = false;
          if (o.snapshot === true) takeSnap();
        }, function () {});
    } catch (e) {}
  }
  // The page changed inside an app: the last page's heatmap is closed.
  function heatPage() {
    if (heatOn) { noteDepth(); replaySend(true); }
    else if (recStop) replaySend(false);
    heatAt = location.pathname; heatSent = false; heatDepth = 0;
    heatOn = !!(replayCfg && replayCfg.heatmaps) && !replayOff && pageOn();
    // A page the brand leaves out is not recorded; the next one is.
    if (recStop && !pageOn()) stopRec();
  }
  // secrets:start
  // Keys, tokens and passwords are hidden before a recording or a
  // heatmap's picture leaves the browser, whatever the brand's settings:
  // known key formats, sign-in tokens, user:password@host, anything after
  // a label such as "API key" or "Password", and any long run of mixed
  // letters and digits (which covers ids too: better hidden than leaked).
  var SECRET_KNOWN = /sk-ant-[A-Za-z0-9_-]{16,}|sk-(?:proj-|live-|test-)?[A-Za-z0-9_-]{20,}|[sprk]k_(?:live|test)_[A-Za-z0-9]{16,}|gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,}|glpat-[A-Za-z0-9_-]{20,}|xox[abprs]-[A-Za-z0-9-]{10,}|A[KS]IA[0-9A-Z]{16}|AIza[0-9A-Za-z_-]{35}|of[tsi]_[A-Za-z0-9_-]{20,}|eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}|-----BEGIN [A-Z ]*PRIVATE KEY-----[A-Za-z0-9+\/=\\nr\s]*(?:-----END [A-Z ]*PRIVATE KEY-----)?/g;
  var SECRET_LOGIN = /(^|[\s"'\/])([^\s:@\/"']{1,80}):([^\s:@\/"']{3,120})@(?=[A-Za-z0-9])/g;
  var SECRET_LABEL = /\b((?:api|access|secret|private|auth)[ _-]?key|secret|token|password|passwd|pwd|credentials?)(["']?\s*[:=]\s*["']?)([^\s"'<>,;]{4,})/gi;
  var SECRET_BEARER = /\b(bearer|basic)(\s+)([A-Za-z0-9_\-+\/=.~]{12,})/gi;
  var SECRET_LONG = /[A-Za-z0-9_\-+\/=]{16,}/g;
  var SECRET_PARAM = /([?&](?:[a-z0-9_.-]*(?:key|token|secret|password|passwd|pwd|auth|signature|sig|session|code)[a-z0-9_.-]*)=)([^&#\s"']+)/gi;
  // The brand's own key shapes (Recordings, Settings): x is any letter or
  // digit, anything else is itself. Hidden wherever one stands on its own.
  var secretShapes;
  function setShapes(list) {
    secretShapes = [];
    for (var i = 0; list && i < list.length && i < 20; i++) {
      try {
        if (!/^[x\-_.:+\/=~]{8,100}$/.test(list[i])) continue;
        secretShapes.push(new RegExp("(^|[^A-Za-z0-9])(" + list[i].replace(/x+|[^x]/g, function (m) {
          return m.charAt(0) === "x" ? "[A-Za-z0-9]{" + m.length + "}" : "\\" + m;
        }) + ")(?![A-Za-z0-9])", "g"));
      } catch (e) {}
    }
  }
  function hideShapes(t) {
    for (var i = 0; secretShapes && i < secretShapes.length; i++) t = t.replace(secretShapes[i], function (m, a, k) { return a + stars(k); });
    return t;
  }
  function stars(t) { return t.replace(/[^\s]/g, "*"); }
  function looksSecret(t) {
    var digits = (t.match(/[0-9]/g) || []).length, letters = (t.match(/[A-Za-z]/g) || []).length;
    if (digits < 2 || letters < 2) return false;
    // A path of plain words is left alone; a mixed-case run is not.
    if (t.indexOf("/") >= 0 && !(/[a-z]/.test(t) && /[A-Z]/.test(t))) return false;
    return true;
  }
  // Formats that are a key wherever they are found.
  function hideKnown(t) {
    return hideShapes(t).replace(SECRET_KNOWN, stars)
      .replace(SECRET_LOGIN, function (m, a, u, p) { return a + u + ":" + stars(p) + "@"; });
  }
  // Everything, for text a visitor sees and attributes that carry text.
  function hideSecrets(t) {
    if (!t || t.length < 6) return t;
    return hideKnown(t)
      .replace(SECRET_LABEL, function (m, l, sep, v) { return l + sep + stars(v); })
      .replace(SECRET_BEARER, function (m, l, sp, v) { return l + sp + stars(v); })
      .replace(SECRET_LONG, function (m) { return looksSecret(m) ? stars(m) : m; });
  }
  function hideInAddress(t) {
    return hideKnown(t).replace(SECRET_PARAM, function (m, k, v) { return k + stars(v); });
  }
  var SECRET_TEXT_ATTR = /^(value|title|alt|placeholder|label|content|aria-label|aria-description|aria-valuetext)$|^data-/;
  var SECRET_URL_ATTR = /^(href|src|action|formaction|poster|xlink:href)$/;
  function hideInAttributes(a) {
    for (var k in a) {
      if (typeof a[k] !== "string" || k.indexOf("rr_") === 0) continue;
      if (SECRET_TEXT_ATTR.test(k)) a[k] = hideSecrets(a[k]);
      else if (SECRET_URL_ATTR.test(k) && a[k].indexOf("data:") !== 0) a[k] = hideInAddress(a[k]);
    }
  }
  function hideInNode(n, depth) {
    if (!n || depth > 400) return;
    if (n.attributes) hideInAttributes(n.attributes);
    if (typeof n.textContent === "string" && !n.isStyle) n.textContent = hideSecrets(n.textContent);
    for (var i = 0; n.childNodes && i < n.childNodes.length; i++) hideInNode(n.childNodes[i], depth + 1);
  }
  // One recorded event: the whole page, or what changed on it.
  function hideInEvent(ev) {
    try {
      var x = ev && ev.data, i;
      if (!x) return;
      if (ev.type === 2) hideInNode(x.node, 0);
      else if (ev.type === 4 && typeof x.href === "string") x.href = hideInAddress(x.href);
      else if (ev.type === 3 && x.source === 0) {
        for (i = 0; x.adds && i < x.adds.length; i++) hideInNode(x.adds[i].node, 0);
        for (i = 0; x.attributes && i < x.attributes.length; i++) if (x.attributes[i].attributes) hideInAttributes(x.attributes[i].attributes);
        for (i = 0; x.texts && i < x.texts.length; i++) if (typeof x.texts[i].value === "string") x.texts[i].value = hideSecrets(x.texts[i].value);
      }
    } catch (e) {}
  }
  // secrets:end
  // The recorder: its script is loaded once, when first needed.
  function withRecorder(done) {
    var lib = w.rrwebRecord, record = lib && (lib.record || lib);
    if (typeof record === "function") return done(record);
    recWait.push(done);
    if (recLoading) return;
    recLoading = true;
    var el = d.createElement("script");
    el.async = true;
    el.src = api + "/v1/site/replay.js";
    el.onload = el.onerror = function () {
      var wait = recWait;
      recLoading = false; recWait = [];
      lib = w.rrwebRecord; record = lib && (lib.record || lib);
      for (var i = 0; i < wait.length; i++) {
        if (typeof record === "function") wait[i](record); else wait[i](null);
      }
    };
    (d.head || d.documentElement).appendChild(el);
  }
  function recOptions(emit) {
    var all = !!replayCfg.mask_text;
    return {
      emit: function (ev) { hideInEvent(ev); emit(ev); },
      maskAllInputs: true,
      // Every piece of text passes through: hidden whole where the brand
      // or the page asks, and otherwise only what looks like a secret.
      maskTextSelector: "*",
      maskTextFn: function (t, el) {
        try {
          if (all || (el && el.closest && el.closest("[data-of-mask]"))) return stars(t);
          return hideSecrets(t);
        } catch (e) { return stars(t); }
      },
      blockSelector: "[data-of-block]",
      sampling: { mousemove: 100, scroll: 200, input: "last" },
      slimDOMOptions: "all",
      recordCanvas: false,
      collectFonts: false
    };
  }
  // A heatmap's picture of this page, when Outflare asks for one (a page
  // with no picture, or an old one): the page as the recorder first sees
  // it, with the same masking as a recording and no visitor id. Once per
  // page, and only from a visitor who allows analytics.
  function takeSnap() {
    var path = heatAt || location.pathname, screen = screenName(), at = path + "#" + screen;
    if (!heatOn || !consented || snapOn || snapAt === at || !replayCfg || !w.fetch || !w.MutationObserver) return;
    snapOn = true;
    withRecorder(function (record) {
      if (!record) { snapOn = false; return; }
      var got = [], stop = null, done = false;
      function finish() {
        if (stop) { try { stop(); } catch (e) {} stop = null; }
        snapOn = false;
      }
      function keep(ev) {
        if (done) return;
        if (ev.type === 4) got = [ev];
        else if (ev.type === 2 && got.length === 1) {
          got.push(ev); done = true; snapAt = at;
          if ((heatAt || location.pathname) === path && screenName() === screen) replayPost({ key: key, path: path, screen: screen, width: w.innerWidth || 0, height: w.innerHeight || 0, snapshot: got }, false);
          w.setTimeout(finish, 0);
        }
      }
      try {
        if (recStop) {
          // A recording is running: its next whole picture is copied.
          snapKeep = keep;
          if (typeof record.takeFullSnapshot === "function") record.takeFullSnapshot(true);
          snapKeep = null; snapOn = false;
        } else {
          stop = record(recOptions(keep)) || null;
          w.setTimeout(function () { if (!done) finish(); }, 10000);
        }
      } catch (e) { snapKeep = null; finish(); }
    });
  }
  function startRec() {
    if (recStop || recLoading || !replayCfg || !replayCfg.record || !consented || !w.fetch || !w.MutationObserver || !pageOn()) return;
    if (!rec) {
      try { rec = JSON.parse(sessionStorage.getItem(REC) || "null"); } catch (e) {}
      if (!rec || rec.key !== key || !/^[A-Za-z0-9-]{8,64}$/.test(rec.id) || Date.now() - rec.at > 14400000) {
        rec = { key: key, id: uuid(), part: 0, at: Date.now(), on: Math.random() * 100 < (replayCfg.sample || 100) };
        keepRec();
      }
    }
    if (!rec.on) return;
    withRecorder(function (record) {
      if (!record || recStop || !consented || !replayCfg || !replayCfg.record) return;
      try {
        recStop = record(recOptions(function (ev) {
          if (snapKeep) snapKeep(ev);
          recBuf.push(ev);
          // A rough size, so a busy page is sent before it grows too big.
          recSize += ev.type === 2 ? 200000 : 300;
          // The whole page goes at once: a visitor who leaves within
          // seconds is still recorded, and what is left to send when
          // they do is small.
          if (ev.type === 2) w.setTimeout(function () { replaySend(false); }, 0);
          else if (recSize > 800000) replaySend(false);
        })) || null;
      } catch (e) { recStop = null; }
    });
  }
  function applyReplay() {
    heatOn = !!(replayCfg && replayCfg.heatmaps) && !replayOff && pageOn();
    if (replayOff || !replayCfg) return stopRec();
    if (!heatAt) heatAt = location.pathname;
    if (!heatHooked) {
      heatHooked = true;
      d.addEventListener("click", noteClick, true);
      w.addEventListener("scroll", noteDepth, { passive: true });
      w.addEventListener("pagehide", function () { noteDepth(); replaySend(true); }, true);
      d.addEventListener("visibilitychange", function () { if (d.visibilityState === "hidden") { noteDepth(); replaySend(true); } });
      recTimer = w.setInterval(function () { replaySend(false); if (!recStop) startRec(); }, 5000);
    }
    noteDepth();
    if (replayCfg.record && pageOn()) startRec(); else stopRec();
  }
  function gotReplay(r) {
    var next = r ? { key: key, record: !!r.record, heatmaps: !!r.heatmaps, sample: r.sample, mask_text: !!r.mask_text, include: r.include || [], exclude: r.exclude || [], hide: r.hide || [] } : null;
    if (JSON.stringify(next) === JSON.stringify(replayCfg)) return;
    replayCfg = next;
    setShapes(next && next.hide);
    try { if (next) sessionStorage.setItem(REPLAY, JSON.stringify(next)); else sessionStorage.removeItem(REPLAY); } catch (e) {}
    applyReplay();
  }
  if (replayCfg) applyReplay();

  // ---- Autocapture: clicks and form submits ----
  // What to capture comes with the answer to this page's events and is
  // kept for the visit. At most 100 captured events a page, so a page
  // that fires clicks in a loop can't flood the site's events.
  var CAPTURE = "of_capture", captureOff = script.getAttribute("data-autocapture") === "off", captureCfg = null, captured = 0, capturedAt = "";
  try { captureCfg = JSON.parse(sessionStorage.getItem(CAPTURE) || "null"); } catch (e) {}
  if (!captureCfg || captureCfg.key !== key) captureCfg = null;
  function gotCapture(c) {
    captureCfg = c && (c.clicks || c.forms || c.errors) ? { key: key, clicks: !!c.clicks, forms: !!c.forms, errors: !!c.errors } : null;
    // Errors that happened before the first answer are sent now, or dropped.
    var held = heldErrors; heldErrors = null;
    for (var i = 0; held && captureCfg && captureCfg.errors && i < held.length; i++) sendError(held[i]);
    try { if (captureCfg) sessionStorage.setItem(CAPTURE, JSON.stringify(captureCfg)); else sessionStorage.removeItem(CAPTURE); } catch (e) {}
  }
  function attr(el, name) { return el && el.getAttribute ? el.getAttribute(name) : null; }
  // A label is short, and never something a person typed or a secret: an
  // email address, a long number or a run of letters and digits that
  // looks like a key is left out.
  function label(t) {
    t = String(t || "").replace(/\s+/g, " ").trim().slice(0, 80);
    return /@|\d{6,}|[A-Za-z0-9_\-]{24,}/.test(t) ? "" : t;
  }
  // The link's address without its query string or fragment.
  function bare(u) {
    try { var x = new URL(u, location.href); return /^https?:$/.test(x.protocol) ? x.origin + x.pathname : x.protocol; } catch (e) { return ""; }
  }
  function clickable(el) {
    var tag = (el.tagName || "").toLowerCase(), type = (attr(el, "type") || "").toLowerCase();
    return tag === "a" || tag === "button" || tag === "summary" || attr(el, "role") === "button" || (tag === "input" && (type === "submit" || type === "button"));
  }
  function capRoom() {
    if (capturedAt !== location.pathname) { capturedAt = location.pathname; captured = 0; }
    return captured++ < 100;
  }
  function captureClick(e) {
    if (captureOff) return;
    var el = e.target, hit = null, named = "", hidden = false, n = 0;
    for (var p = el; p && n < 30; p = p.parentNode, n++) {
      if (!p.getAttribute) continue;
      if (attr(p, "data-outflare-ignore") !== null) return;
      if (attr(p, "data-of-mask") !== null || attr(p, "data-of-block") !== null) hidden = true;
      if (!hit && (attr(p, "data-outflare-event") || clickable(p))) { hit = p; named = attr(p, "data-outflare-event") || ""; }
    }
    if (!hit || (!named && !(captureCfg && captureCfg.clicks)) || !capRoom()) return;
    var tag = (hit.tagName || "").toLowerCase(), props = { tag: tag, path: location.pathname };
    var text = hidden ? "" : label(tag === "input" ? hit.value : (attr(hit, "aria-label") || hit.innerText || hit.textContent || attr(hit, "title")));
    if (text) props.text = text;
    if (hit.id) props.id = String(hit.id).slice(0, 80);
    var cls = String(attr(hit, "class") || "").split(/\s+/).filter(Boolean).slice(0, 5).join(" ").slice(0, 200);
    if (cls) props.classes = cls;
    if (tag === "a" && attr(hit, "href")) { var to = bare(attr(hit, "href")); if (to) props.href = to; }
    track(named.slice(0, 100) || "Element Clicked", props);
  }
  function captureSubmit(e) {
    var f = e.target, n = 0;
    if (captureOff || !captureCfg || !captureCfg.forms || !f || (f.tagName || "").toLowerCase() !== "form") return;
    for (var p = f; p && n < 30; p = p.parentNode, n++) if (attr(p, "data-outflare-ignore") !== null) return;
    if (!capRoom()) return;
    var props = { path: location.pathname, method: (attr(f, "method") || "get").toLowerCase().slice(0, 10) };
    if (f.id) props.id = String(f.id).slice(0, 80);
    if (attr(f, "name")) props.name = String(attr(f, "name")).slice(0, 80);
    if (attr(f, "action")) { var to = bare(attr(f, "action")); if (to) props.action = to; }
    if (f.elements && typeof f.elements.length === "number") props.fields = f.elements.length;
    track("Form Submitted", props);
  }
  if (!captureOff) {
    d.addEventListener("click", captureClick, true);
    d.addEventListener("submit", captureSubmit, true);
  }

  // ---- Ad pixels ----
  // Which pixel to load comes with the answer to this page's events and is
  // kept for the visit, with the signed-in visitor's hashed email, phone
  // and id when Outflare's server knows them. The pixel starts once the
  // page has loaded, so a snippet the page carries itself is seen first:
  // then the script loads nothing and only passes the user data on.
  var PIXELS = "of_pixels", pixelOff = script.getAttribute("data-pixels") === "off", pixelCfg = null, pixelOn = false, pixelOurs = false, pixelUser = "", pixelSeen = "", pixelConsent = null;
  // Meta's and TikTok's pixels: whether each was started here, the choice
  // it was last told and the page it last counted.
  var metaOn = false, metaConsent = null, metaSeen = "", tiktokOn = false, tiktokConsent = null, tiktokSeen = "";
  try { pixelCfg = JSON.parse(sessionStorage.getItem(PIXELS) || "null"); } catch (e) {}
  if (!pixelCfg || pixelCfg.key !== key) pixelCfg = null;
  function keepPixels() {
    try { if (pixelCfg) sessionStorage.setItem(PIXELS, JSON.stringify(pixelCfg)); else sessionStorage.removeItem(PIXELS); } catch (e) {}
  }
  function onePixel(c) {
    return c && /^[A-Za-z0-9_-]{1,80}$/.test(c.id || "") ? { id: c.id, user: c.user || null, events: c.events || null } : null;
  }
  function gotPixels(p) {
    var c = onePixel(p && p.chatgpt), me = onePixel(p && p.meta), tt = onePixel(p && p.tiktok);
    pixelCfg = c || me || tt ? { key: key, id: c ? c.id : "", user: c ? c.user : null } : null;
    if (me) pixelCfg.meta = me;
    if (tt) pixelCfg.tiktok = tt;
    keepPixels();
    whenLoaded(applyPixels);
  }
  function applyPixels() {
    if (pixelOff || !pixelCfg) return;
    var m = cats.marketing;
    try { applyMeta(m); } catch (e) {}
    try { applyTikTok(m); } catch (e) {}
    if (!pixelCfg.id) return;
    if (!pixelOn) {
      if (!m) return;
      pixelOn = true;
      pixelOurs = typeof w.oaiq !== "function";
      if (pixelOurs) {
        // OpenAI's own loader: calls wait in the queue until the SDK is in.
        var q = function () { q.q.push(arguments); };
        q.q = [];
        w.oaiq = q;
        var js = d.createElement("script");
        js.async = true;
        js.src = "https://bzrcdn.openai.com/sdk/oaiq.min.js";
        (d.head || d.documentElement).appendChild(js);
      }
    }
    try {
      // The visitor's choice goes to a pixel this script loaded, and to
      // the page's own when Outflare's banner asks for consent.
      if ((pixelOurs || on()) && pixelConsent !== m) { w.oaiq("consent", m); pixelConsent = m; }
      var user = m && pixelCfg.user ? pixelCfg.user : null, sig = JSON.stringify(user);
      if (pixelUser === "" || (user && sig !== pixelUser)) {
        if (user) w.oaiq("init", { pixelId: pixelCfg.id, user: user });
        else if (pixelOurs) w.oaiq("init", { pixelId: pixelCfg.id });
        pixelUser = sig;
      }
      if (pixelOurs && m && pixelSeen !== location.href) {
        pixelSeen = location.href;
        w.oaiq("measure", "page_viewed", { type: "contents" });
      }
    } catch (e) {}
  }
  // Meta's pixel, with Meta's own loader. A page that already carries
  // Meta's snippet keeps it and this does nothing: its events are the
  // page's to send, and sending them twice would count them twice. The
  // signed-in visitor's hashes go in when the pixel starts, so someone
  // who signs in is known to it from the next page.
  function applyMeta(m) {
    var c = pixelCfg.meta;
    if (!c) return;
    if (!metaOn) {
      if (!m || typeof w.fbq === "function") return;
      metaOn = true;
      var n = w.fbq = function () { if (n.callMethod) n.callMethod.apply(n, arguments); else n.queue.push(arguments); };
      if (!w._fbq) w._fbq = n;
      n.push = n;
      n.loaded = true;
      n.version = "2.0";
      n.queue = [];
      var js = d.createElement("script");
      js.async = true;
      js.src = "https://connect.facebook.net/en_US/fbevents.js";
      (d.head || d.documentElement).appendChild(js);
      w.fbq("init", c.id, c.user || {});
    }
    if (metaConsent !== m) { w.fbq("consent", m ? "grant" : "revoke"); metaConsent = m; }
    if (m && metaSeen !== location.href) {
      metaSeen = location.href;
      w.fbq("trackSingle", c.id, "PageView");
    }
  }
  // TikTok's pixel, with TikTok's own loader; a page that carries
  // TikTok's snippet keeps it, as with Meta's.
  function applyTikTok(m) {
    var c = pixelCfg.tiktok;
    if (!c) return;
    if (!tiktokOn) {
      if (!m || w.ttq) return;
      tiktokOn = true;
      var t = w.ttq = [], names = ["page", "track", "identify", "instances", "debug", "on", "off", "once", "ready", "alias", "group", "enableCookie", "disableCookie", "holdConsent", "revokeConsent", "grantConsent"];
      w.TiktokAnalyticsObject = "ttq";
      t.methods = names;
      t.setAndDefer = function (o, name) { o[name] = function () { o.push([name].concat(Array.prototype.slice.call(arguments, 0))); }; };
      for (var i = 0; i < names.length; i++) t.setAndDefer(t, names[i]);
      t.instance = function (id) {
        var o = t._i[id] || [];
        for (var k = 0; k < names.length; k++) t.setAndDefer(o, names[k]);
        return o;
      };
      t._i = {};
      t._i[c.id] = [];
      t._i[c.id]._u = "https://analytics.tiktok.com/i18n/pixel/events.js";
      t._t = {};
      t._t[c.id] = +new Date();
      t._o = {};
      t._o[c.id] = {};
      var js = d.createElement("script");
      js.async = true;
      js.src = "https://analytics.tiktok.com/i18n/pixel/events.js?sdkid=" + encodeURIComponent(c.id) + "&lib=ttq";
      (d.head || d.documentElement).appendChild(js);
      if (c.user) w.ttq.identify(c.user);
    }
    if (tiktokConsent !== m) {
      if (m) w.ttq.grantConsent(); else w.ttq.revokeConsent();
      tiktokConsent = m;
    }
    if (m && tiktokSeen !== location.href) {
      tiktokSeen = location.href;
      w.ttq.page();
    }
  }
  // A track call's event as Outflare's catalog names it (Order Completed
  // is order_completed), the way the server does.
  function catalogName(raw) {
    var s = String(raw).trim().replace(/([a-z])([A-Z])/g, "$1_$2").toLowerCase().replace(/[^a-z0-9.]+/g, "_").replace(/_*\._*/g, ".").replace(/\.{2,}/g, ".").replace(/^[_.]+|[_.]+$/g, "");
    return s && (s.charAt(0) < "a" || s.charAt(0) > "z") ? "event_" + s : s;
  }
  // The value a conversion carries, read as the server reads it for the
  // same rule: a property of the event or a fixed amount, in the rule's
  // currency, then the event's, then the ad account's.
  function pixelValue(e, props) {
    var v = null;
    if (e.value_property) {
      var raw = props[e.value_property];
      if (typeof raw === "number" && isFinite(raw) && raw >= 0) v = e.value_unit === "minor" ? raw / 100 : raw;
    } else if (typeof e.value === "number") v = e.value;
    if (v === null) return {};
    var own = typeof props.currency === "string" && props.currency.trim().length === 3 ? props.currency.trim().toUpperCase() : "";
    var cur = e.currency || own || e.account_currency || "";
    return cur ? { value: v, currency: cur } : {};
  }
  var META_EVENTS = " AddPaymentInfo AddToCart AddToWishlist CompleteRegistration Contact CustomizeProduct Donate FindLocation InitiateCheckout Lead Purchase Schedule Search StartTrial SubmitApplication Subscribe ViewContent ";
  // A conversion the page caused goes to the pixels this script started,
  // with the id the server sends the same conversion with, so the
  // platform counts the two as one.
  function pixelEvent(name, props, id) {
    if (pixelOff || !pixelCfg || !cats.marketing || !(metaOn || tiktokOn)) return;
    var n = catalogName(name), e;
    try {
      e = metaOn && pixelCfg.meta && pixelCfg.meta.events && pixelCfg.meta.events[n];
      if (e && e.type) w.fbq(META_EVENTS.indexOf(" " + e.type + " ") >= 0 ? "trackSingle" : "trackSingleCustom", pixelCfg.meta.id, e.type, pixelValue(e, props), { eventID: id });
    } catch (x) {}
    try {
      e = tiktokOn && pixelCfg.tiktok && pixelCfg.tiktok.events && pixelCfg.tiktok.events[n];
      if (e && e.type) w.ttq.track(e.type, pixelValue(e, props), { event_id: id });
    } catch (x) {}
  }
  if (pixelCfg) whenLoaded(applyPixels);

  // ---- Errors ----
  // An error is sent as the track event Error Caught, which the Errors
  // page groups into issues by fingerprint: the error's type, its message
  // with numbers and ids taken out, and the first place in its stack.
  // Uncaught errors and rejected promises are sent when the site key's
  // "Capture errors" switch is on; outflare.captureException(err) sends
  // one the page caught itself, with or without the switch. At most 20 a
  // page, and each issue at most 5 times, so a loop can't flood events.
  // Email addresses and long keys in a message or stack are taken out.
  var heldErrors = captureCfg ? null : [], errorsSent = 0, errorTimes = {}, release = (script.getAttribute("data-release") || "").slice(0, 100);
  // Errors that aren't the site's: thrown in a browser extension, or on a
  // copy of the page opened from a file. Outflare filters more on its side.
  var FOREIGN = /(chrome|moz|safari|safari-web)-extension:\/\/|webkit-masked-url:\/\//;
  function foreign(p) {
    if (location.protocol === "file:" || FOREIGN.test(p.source || "")) return true;
    var files = (p.stack || "").match(/[a-z][a-z0-9+.-]*:\/\/[^\s()]+/g);
    if (!files) return false;
    for (var i = 0; i < files.length; i++) if (!FOREIGN.test(files[i])) return false;
    return true;
  }
  function media(q) { try { return w.matchMedia && w.matchMedia(q).matches; } catch (e) { return false; } }
  function scrub(t, max) {
    return String(t == null ? "" : t).replace(/[^\s@"'<>()]+@[^\s@"'<>()]+\.[A-Za-z]{2,}/g, "[email]").replace(/[A-Za-z0-9_\-]{32,}/g, "[key]").slice(0, max);
  }
  function hash(t) {
    var h1 = 5381, h2 = 52711;
    for (var i = 0; i < t.length; i++) { var c = t.charCodeAt(i); h1 = (h1 * 33) ^ c; h2 = (h2 * 33) ^ c; }
    return ("0000000" + (h1 >>> 0).toString(16)).slice(-8) + ("0000000" + (h2 >>> 0).toString(16)).slice(-8);
  }
  // describe turns whatever was thrown into what is sent.
  function describe(err, handled, extra) {
    var isErr = err && typeof err === "object" && ("message" in err || "stack" in err);
    var type = scrub(isErr ? (err.name || "Error") : "Error", 100), message = scrub(isErr ? err.message : (typeof err === "string" ? err : (function () { try { return JSON.stringify(err); } catch (e) { return String(err); } })()), 500);
    var stack = isErr && err.stack ? scrub(String(err.stack).replace(/\?[^\s):]*/g, ""), 4000) : "";
    // The first line of the stack that names a file, without line and
    // column, so the same bug in a new build is still the same issue.
    var top = "", lines = stack.split("\n");
    for (var i = 0; i < lines.length && !top; i++) if (/(https?:|\/)[^\s]*\.\w+/.test(lines[i]) && lines[i].indexOf(message) < 0) top = lines[i].replace(/:\d+(:\d+)?\)?\s*$/, "").trim();
    var p = { type: type, message: message, handled: !!handled, path: location.pathname };
    if (stack) p.stack = stack;
    if (release) p.release = release;
    // The window's size and the visitor's settings, which some bugs
    // depend on. Nothing they typed or chose on the page.
    p.viewport = (w.innerWidth || 0) + "x" + (w.innerHeight || 0);
    p.color_scheme = media("(prefers-color-scheme: dark)") ? "dark" : "light";
    p.reduced_motion = media("(prefers-reduced-motion: reduce)") ? "reduce" : "no-preference";
    if (extra && extra.handled === false) p.handled = false;
    p.fingerprint = hash(type + "|" + message.replace(/\d+/g, "0").replace(/[0-9a-f]{8}-[0-9a-f-]{27}/gi, "id") + "|" + top);
    for (var k in extra || {}) if (!(k in p)) p[k] = extra[k];
    return p;
  }
  function sendError(p) {
    if (foreign(p)) return;
    var n = errorTimes[p.fingerprint] || 0;
    if (errorsSent >= 20 || n >= 5) return;
    errorsSent++; errorTimes[p.fingerprint] = n + 1;
    return track("Error Caught", p);
  }
  function uncaught(err, extra) {
    if (captureOff) return;
    var p = describe(err, false, extra);
    if (heldErrors) { if (heldErrors.length < 10) heldErrors.push(p); return; }
    if (captureCfg && captureCfg.errors) sendError(p);
  }
  function captureException(err, extra) { return sendError(describe(err, true, extra)); }
  if (!captureOff) {
    w.addEventListener("error", function (e) {
      // A script from another site that hides its error says only "Script error.".
      if (!e || (!e.error && (!e.message || e.message === "Script error."))) return;
      var extra = {};
      if (e.filename) extra.source = bare(e.filename);
      if (e.lineno) extra.line = e.lineno;
      if (e.colno) extra.column = e.colno;
      uncaught(e.error || e.message, extra);
    });
    w.addEventListener("unhandledrejection", function (e) { if (e) uncaught(e.reason, { rejection: true }); });
  }

  var queued = Array.isArray(w.outflare) ? w.outflare : [];
  w.outflare = {
    loaded: true,
    page: page,
    track: track,
    // Send an error the page caught itself to the Errors page:
    // try { ... } catch (err) { outflare.captureException(err, { step: "checkout" }); }
    captureException: captureException,
    // The release (your version or commit) errors are sent with, when the
    // tag's data-release can't say it: outflare.setRelease("2026.11.18").
    setRelease: function (r) { release = String(r == null ? "" : r).slice(0, 100); },
    // Say whether this browser is signed in to your app: true after
    // sign-in, false on sign-out. Pages on every subdomain can then ask
    // outflare.isSignedIn(), flags can have a "Signed in" rule, and page
    // views carry signed_in. Only a hint: never rely on it for access.
    signedIn: function (yes) {
      var was = isSignedIn();
      setCookie(SIGNED, "1", !!yes);
      showSignedIn();
      if (was !== isSignedIn() && flagsWanted) loadFlags();
      return isSignedIn();
    },
    isSignedIn: isSignedIn,
    // Join this browser's visits to a person your server vouched for.
    // Your server signs the user id with the site's identify secret,
    // hex(HMAC-SHA256(secret, userId)), and gives the page both:
    //   outflare.identify(user.id, user.outflareSignature)
    // Resolves to true when Outflare took it. A browser can't name a
    // person on its own word, so without the signature this does nothing.
    identify: function (userId, signature) {
      if (!userId || !signature || !w.fetch) return Promise.resolve(false);
      return fetch(api + "/v1/site/identify", { method: "POST", keepalive: true, headers: { "Content-Type": "text/plain" },
        body: JSON.stringify({ key: key, anonymousId: anon, userId: String(userId), signature: String(signature) }) })
        .then(function (res) { return res.ok; }, function () { return false; });
    },
    // The visitor's id, to send with identify from your server when they
    // sign up or sign in, so their earlier visits join their record.
    anonymousId: function () { return anon; },
    // The tracker click id of this visit (a redirect link's ofclk, or the
    // one named for a direct-mode ad), "" without one: pass it to an
    // offer as its sub id. Links with {ofclk} in their address get it.
    clickId: function () { return clickKey; },
    // The visitor signed up with this email: the partner whose link they
    // came from gets the lead at once. For sites whose backend reports
    // nothing; sales still come from your server or billing. Resolves to
    // true once sent.
    signup: function (email) {
      if (!email || !w.fetch) return Promise.resolve(false);
      return fetch(api + "/v1/site/signup", { method: "POST", keepalive: true, headers: { "Content-Type": "text/plain" },
        body: JSON.stringify({ key: key, anonymousId: anon, email: String(email) }) })
        .then(function (res) { return res.ok; }, function () { return false; });
    },
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
      setCookie(SIGNED, "1", false);
      showSignedIn();
      anon = uuid();
      if (consented) writeCookie(anon);
      if (pixelCfg) { pixelCfg.user = null; if (pixelCfg.meta) pixelCfg.meta.user = null; if (pixelCfg.tiktok) pixelCfg.tiktok.user = null; keepPixels(); }
      seen = {};
      try { sessionStorage.removeItem(FLAGS); sessionStorage.removeItem(SEEN); } catch (e) {}
      if (flagsWanted) loadFlags();
    },
    // The visitor's variant of a flag, or false: false too until the
    // flags arrive, so wait with onFlags when it matters.
    flag: function (k) {
      codeAsked = true;
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
      codeAsked = true;
      if (!flagsWanted) loadFlags();
      if (flagState) { try { fn(flagMap()); } catch (e) {} } else flagWaiters.push(fn);
    }
  };
  vitals();
  if (flagsWanted) loadFlags();
  if (on()) settle(true);
  else unblock();
  loadBanner();
  page();
  queued.forEach(function (c) {
    if (c && w.outflare[c[0]]) w.outflare[c[0]].apply(null, c.slice(1));
  });
})();
