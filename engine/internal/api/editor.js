/* Outflare visual editor. The site script loads it for an edit link from
   Outflare (?of_edit=<token>). Pick an element on the page, change its
   text, hide it, or change a link or image, then save: the changes become
   the "changes" of the flag version the link was made for, and visitors in
   that version see them. Everything it draws lives in a shadow root, so the
   page's styles don't reach it. */
(function () {
  "use strict";
  var me = document.currentScript;
  if (!me || window.__outflareEditor) return;
  window.__outflareEditor = true;
  var token = me.getAttribute("data-token") || "";
  var api = (me.getAttribute("data-api") || "").replace(/\/$/, "");
  if (!token || !api) return;

  var MAX = 50;
  var state = { info: null, changes: [], picking: true, selected: null, busy: false, msg: "" };

  function safeURL(u) {
    return typeof u === "string" && u.length <= 2000 && !/\s/.test(u) &&
      (/^https?:\/\/[^\/\s]+/i.test(u) || (u.charAt(0) === "/" && u.charAt(1) !== "/"));
  }

  // A selector that finds this element again on the next page load: its id
  // when it has a stable one, else a path of tag, classes and position.
  function cssEscape(s) {
    return window.CSS && CSS.escape ? CSS.escape(s) : s.replace(/[^a-zA-Z0-9_-]/g, "\\$&");
  }
  function selectorFor(el) {
    var parts = [];
    while (el && el.nodeType === 1 && el !== document.documentElement) {
      if (el.id && /^[A-Za-z][\w-]*$/.test(el.id) && !/\d{3,}/.test(el.id)) {
        parts.unshift("#" + cssEscape(el.id));
        break;
      }
      var part = el.tagName.toLowerCase();
      var classes = Array.prototype.filter.call(el.classList, function (c) {
        return /^[A-Za-z][\w-]*$/.test(c) && !/\d{3,}/.test(c) && c.length < 40;
      }).slice(0, 2);
      if (classes.length) part += "." + classes.map(cssEscape).join(".");
      var parent = el.parentElement;
      if (parent) {
        var same = Array.prototype.filter.call(parent.children, function (c) { return c.tagName === el.tagName; });
        if (same.length > 1) part += ":nth-of-type(" + (same.indexOf(el) + 1) + ")";
      }
      parts.unshift(part);
      if (el.tagName === "BODY") break;
      el = parent;
      if (parts.length >= 6) break;
    }
    return parts.join(" > ");
  }

  function find(sel) {
    try { return document.querySelectorAll(sel); } catch (e) { return []; }
  }
  // Show a change on the page now, as the site script will for visitors.
  function apply(c) {
    Array.prototype.forEach.call(find(c.selector), function (el) {
      if (el === host) return;
      if (typeof c.text === "string") el.textContent = c.text;
      if (c.hide) el.style.setProperty("display", "none", "important");
      if (typeof c.href === "string" && safeURL(c.href)) el.setAttribute("href", c.href);
      if (typeof c.src === "string" && safeURL(c.src)) el.setAttribute("src", c.src);
      if (typeof c.alt === "string") el.setAttribute("alt", c.alt);
    });
  }
  function describe(c) {
    if (c.hide) return "Hide";
    if (typeof c.text === "string") return "Text: " + (c.text.length > 40 ? c.text.slice(0, 40) + "…" : c.text);
    if (typeof c.href === "string") return "Link: " + c.href;
    if (typeof c.src === "string") return "Image: " + c.src;
    if (typeof c.alt === "string") return "Alt text: " + c.alt;
    return "Change";
  }

  // The overlay.
  var host = document.createElement("div");
  host.setAttribute("data-outflare-editor", "");
  host.style.cssText = "all:initial;position:fixed;inset:0;pointer-events:none;z-index:2147483647";
  var root = host.attachShadow ? host.attachShadow({ mode: "open" }) : host;
  root.innerHTML =
    "<style>" +
    ":host{all:initial}" +
    ".box{position:fixed;pointer-events:none;border:2px solid #4f46e5;background:rgba(79,70,229,.08);border-radius:2px;display:none}" +
    ".sel{border-color:#db2777;background:rgba(219,39,119,.08)}" +
    ".panel{position:fixed;right:16px;bottom:16px;width:320px;max-width:calc(100vw - 32px);max-height:70vh;overflow:auto;pointer-events:auto;" +
    "background:#fff;color:#111827;border:1px solid #e5e7eb;border-radius:10px;box-shadow:0 10px 30px rgba(0,0,0,.18);" +
    "font:13px/1.45 system-ui,-apple-system,Segoe UI,sans-serif;padding:12px}" +
    "h1{font-size:14px;margin:0 0 2px}p{margin:4px 0;color:#4b5563}code{font:11px ui-monospace,monospace;word-break:break-all;color:#374151}" +
    "label{display:block;margin:8px 0 2px;font-weight:600}" +
    "input,textarea{box-sizing:border-box;width:100%;font:inherit;padding:5px 7px;border:1px solid #d1d5db;border-radius:6px;color:#111827;background:#fff}" +
    "textarea{min-height:56px;resize:vertical}" +
    "button{font:inherit;padding:5px 10px;border-radius:6px;border:1px solid #d1d5db;background:#fff;color:#111827;cursor:pointer;margin:6px 6px 0 0}" +
    "button.primary{background:#4f46e5;border-color:#4f46e5;color:#fff}button:disabled{opacity:.5;cursor:default}" +
    "ul{list-style:none;padding:0;margin:6px 0}li{display:flex;gap:6px;align-items:flex-start;padding:4px 0;border-top:1px solid #f3f4f6}" +
    "li span{flex:1;min-width:0}li button{margin:0;padding:1px 6px}" +
    ".msg{color:#b91c1c}.ok{color:#047857}" +
    "</style>" +
    "<div class='box hover'></div><div class='box sel'></div><div class='panel'></div>";
  var hoverBox = root.querySelector(".hover"), selBox = root.querySelector(".sel"), panel = root.querySelector(".panel");

  function place(box, el) {
    if (!el) { box.style.display = "none"; return; }
    var r = el.getBoundingClientRect();
    box.style.display = "block";
    box.style.left = r.left + "px"; box.style.top = r.top + "px";
    box.style.width = r.width + "px"; box.style.height = r.height + "px";
  }
  function inEditor(el) { return el === host || (el && el.closest && el.closest("[data-outflare-editor]")); }

  function esc(s) {
    return String(s).replace(/[&<>"']/g, function (ch) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[ch];
    });
  }

  function render() {
    var i = state.info, el = state.selected, h = "";
    if (!i) {
      h = "<h1>Outflare editor</h1><p class='" + (state.msg ? "msg" : "") + "'>" + esc(state.msg || "Loading…") + "</p>";
      panel.innerHTML = h;
      return;
    }
    h += "<h1>Editing " + esc(i.flag_name || i.flag_key) + " · " + esc(i.variant) + "</h1>";
    if (!el) {
      h += "<p>" + (state.picking ? "Click anything on the page to change it." : "Picking is paused.") + "</p>";
    } else {
      var sel = selectorFor(el), tag = el.tagName;
      h += "<p>Selected <code>" + esc(sel) + "</code> (" + find(sel).length + " on this page)</p>";
      if (!el.children.length) {
        h += "<label for='t'>Text</label><textarea id='t'>" + esc(el.textContent || "") + "</textarea><button data-do='text'>Set text</button>";
      }
      if (tag === "A") h += "<label for='h'>Link</label><input id='h' value='" + esc(el.getAttribute("href") || "") + "'><button data-do='href'>Set link</button>";
      if (tag === "IMG") {
        h += "<label for='s'>Image address</label><input id='s' value='" + esc(el.getAttribute("src") || "") + "'><button data-do='src'>Set image</button>";
        h += "<label for='a'>Alt text</label><input id='a' value='" + esc(el.getAttribute("alt") || "") + "'><button data-do='alt'>Set alt text</button>";
      }
      h += "<div><button data-do='hide'>Hide it</button><button data-do='parent'>Select parent</button><button data-do='deselect'>Pick another</button></div>";
    }
    h += "<label>Changes (" + state.changes.length + "/" + MAX + ")</label><ul>";
    state.changes.forEach(function (c, n) {
      h += "<li><span>" + esc(describe(c)) + "<br><code>" + esc(c.selector) + "</code></span><button data-del='" + n + "' aria-label='Remove'>×</button></li>";
    });
    h += "</ul>";
    if (state.msg) h += "<p class='" + (state.msgOK ? "ok" : "msg") + "'>" + esc(state.msg) + "</p>";
    h += "<button class='primary' data-do='save'" + (state.busy ? " disabled" : "") + ">Save</button><button data-do='close'>Close</button>";
    panel.innerHTML = h;
  }

  function add(c) {
    if (state.changes.length >= MAX) { say("A version can have up to " + MAX + " changes.", false); return; }
    // One change per element and kind: a new text replaces the old one.
    var kind = Object.keys(c).filter(function (k) { return k !== "selector"; })[0];
    state.changes = state.changes.filter(function (o) { return !(o.selector === c.selector && kind in o); });
    state.changes.push(c);
    apply(c);
    state.msg = "";
    render();
  }
  function say(m, ok) { state.msg = m; state.msgOK = ok; render(); }

  panel.addEventListener("click", function (e) {
    var t = e.target, act = t.getAttribute && t.getAttribute("data-do"), del = t.getAttribute && t.getAttribute("data-del");
    if (del !== null && del !== undefined) {
      state.changes.splice(+del, 1);
      say("Removed. Save, then reload the page to see it without that change.", true);
      return;
    }
    var el = state.selected, sel = el && selectorFor(el), v = function (id) { return root.querySelector("#" + id).value; };
    switch (act) {
      case "text": add({ selector: sel, text: v("t").slice(0, 2000) }); break;
      case "href":
        if (!safeURL(v("h"))) { say("A link starts with https:// or /.", false); return; }
        add({ selector: sel, href: v("h") }); break;
      case "src":
        if (!safeURL(v("s"))) { say("An image address starts with https:// or /.", false); return; }
        add({ selector: sel, src: v("s") }); break;
      case "alt": add({ selector: sel, alt: v("a").slice(0, 500) }); break;
      case "hide": add({ selector: sel, hide: true }); state.selected = null; place(selBox, null); render(); break;
      case "parent":
        if (el && el.parentElement && el.parentElement !== document.documentElement) { state.selected = el.parentElement; place(selBox, state.selected); render(); }
        break;
      case "deselect": state.selected = null; state.picking = true; place(selBox, null); render(); break;
      case "save": save(); break;
      case "close":
        try { sessionStorage.removeItem("of_edit"); } catch (e2) {}
        host.remove();
        document.removeEventListener("mousemove", onMove, true);
        document.removeEventListener("click", onClick, true);
        break;
    }
  });

  function onMove(e) {
    if (!state.picking || inEditor(e.target)) { place(hoverBox, null); return; }
    place(hoverBox, e.target);
  }
  function onClick(e) {
    if (inEditor(e.target) || !state.picking || !state.info) return;
    e.preventDefault();
    e.stopPropagation();
    state.selected = e.target;
    state.picking = false;
    place(hoverBox, null);
    place(selBox, e.target);
    state.msg = "";
    render();
  }
  window.addEventListener("scroll", function () { place(selBox, state.selected); }, true);

  function request(method, body) {
    var url = api + "/v1/site/flags/edits" + (method === "GET" ? "?token=" + encodeURIComponent(token) : "");
    // text/plain keeps the request simple: no preflight.
    return fetch(url, { method: method, body: body ? JSON.stringify(body) : undefined, headers: body ? { "Content-Type": "text/plain" } : undefined, credentials: "omit" })
      .then(function (r) {
        return r.json().catch(function () { return {}; }).then(function (j) {
          if (!r.ok) throw new Error((j.error && j.error.message) || "Outflare answered " + r.status);
          return j;
        });
      });
  }
  function save() {
    state.busy = true;
    say("Saving…", true);
    request("POST", { token: token, changes: state.changes }).then(function (j) {
      state.busy = false;
      state.changes = j.changes || [];
      say("Saved. Visitors in " + j.variant + " see these changes within a minute.", true);
    }, function (err) {
      state.busy = false;
      say(err.message, false);
    });
  }

  function start() {
    (document.body || document.documentElement).appendChild(host);
    render();
    document.addEventListener("mousemove", onMove, true);
    document.addEventListener("click", onClick, true);
    request("GET").then(function (j) {
      state.info = j;
      state.changes = j.changes || [];
      render();
    }, function (err) {
      say(err.message, false);
    });
  }
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", start);
  else start();
})();
