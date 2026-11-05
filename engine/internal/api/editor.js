/* Outflare visual editor. The site script loads it for an edit link from
   Outflare (?of_edit=<token>). Pick an element on the page and change its
   text (plain or with simple formatting), its look, its place, a link or
   image, hide it, or add a new heading, button, link or image next to it.
   A version can also have its own CSS and send its visitors to another
   page (a split URL test). Save, and the changes become the flag version's
   payload, which visitors in that version see. Everything it draws lives in
   a shadow root, so the page's styles don't reach it. It never adds
   scripts: everything it saves is data the site script applies safely. */
(function () {
  "use strict";
  var me = document.currentScript;
  if (!me || window.__outflareEditor) return;
  window.__outflareEditor = true;
  var token = me.getAttribute("data-token") || "";
  var api = (me.getAttribute("data-api") || "").replace(/\/$/, "");
  if (!token || !api) return;

  var MAX = 50;
  var state = {
    info: null, changes: [], css: "", redirect: "", device: "", picking: true, selected: null,
    busy: false, msg: "", msgOK: false, tab: "content", dirty: false,
  };

  // The same checks the server and site script make, so mistakes show here.
  function safeURL(u) {
    return typeof u === "string" && u.length <= 2000 && !/\s/.test(u) &&
      (/^https?:\/\/[^\/\s]+/i.test(u) || (u.charAt(0) === "/" && u.charAt(1) !== "/"));
  }
  var STYLE = /^(color|background-color|font-(size|weight|style|family)|text-(align|decoration|transform)|line-height|letter-spacing|(padding|margin)(-(top|right|bottom|left))?|border(-(color|width|style|radius))?|(max-|min-)?(width|height)|display|opacity|gap|order|justify-content|align-items|flex-direction|box-shadow|visibility)$/;
  function safeStyle(v) {
    return typeof v === "string" && /^[#(),.%\-+\w\s\/"']{1,100}$/.test(v) && !/url\s*\(|expression|image/i.test(v);
  }
  function safeCSS(c) {
    return typeof c === "string" && c.length <= 8000 &&
      !/[<\\]|url\s*\(|@import|@font-face|@charset|@namespace|expression\s*\(|javascript:|behavior|-moz-binding|image\s*\(/i.test(c);
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
        return /^[A-Za-z][\w-]*$/.test(c) && !/\d{3,}/.test(c) && c.length < 40 && c !== "of-button";
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
  function phone() {
    try { return window.matchMedia("(max-width: 767px)").matches; } catch (e) { return false; }
  }

  // Simple formatting, built as elements: **bold**, *italic*, [text](url)
  // and new lines. Never HTML.
  function richNodes(src) {
    var frag = document.createDocumentFragment();
    String(src).split("\n").forEach(function (line, n) {
      if (n) frag.appendChild(document.createElement("br"));
      var re = /\*\*(.+?)\*\*|\*(.+?)\*|\[([^\]]+)\]\(([^)\s]+)\)/g, last = 0, m;
      while ((m = re.exec(line))) {
        if (m.index > last) frag.appendChild(document.createTextNode(line.slice(last, m.index)));
        var el;
        if (m[1]) { el = document.createElement("strong"); el.textContent = m[1]; }
        else if (m[2]) { el = document.createElement("em"); el.textContent = m[2]; }
        else if (safeURL(m[4])) { el = document.createElement("a"); el.setAttribute("href", m[4]); el.textContent = m[3]; }
        else el = document.createTextNode(m[3]);
        frag.appendChild(el);
        last = re.lastIndex;
      }
      if (last < line.length) frag.appendChild(document.createTextNode(line.slice(last)));
    });
    return frag;
  }

  // Show a change on the page now, as the site script will for visitors.
  function apply(c, n) {
    if (c.device && (c.device === "mobile") !== phone()) return;
    var els = Array.prototype.filter.call(find(c.selector), function (el) { return !inEditor(el); });
    if (c.insert && els.length) {
      var inn = c.insert, mark = "editor:" + n, node = null;
      if (document.querySelector("[data-of-insert='" + mark + "']")) return;
      if (inn.kind === "image" && safeURL(inn.src)) { node = document.createElement("img"); node.setAttribute("src", inn.src); node.setAttribute("alt", inn.alt || ""); }
      else if ((inn.kind === "button" || inn.kind === "link") && safeURL(inn.href)) {
        node = document.createElement("a"); node.setAttribute("href", inn.href); node.textContent = inn.text || "";
        if (inn.kind === "button") { node.className = "of-button"; node.setAttribute("role", "button"); }
      } else if (inn.kind === "text") { node = document.createElement("p"); node.textContent = inn.text || ""; }
      if (!node) return;
      node.setAttribute("data-of-insert", mark);
      var at = els[0];
      at.parentNode.insertBefore(node, inn.where === "before" ? at : at.nextSibling);
      return;
    }
    var target = null;
    if (c.move) { target = find(c.target)[0]; if (!target) return; }
    els.forEach(function (el) {
      if (typeof c.text === "string") el.textContent = c.text;
      else if (typeof c.rich === "string") { el.textContent = ""; el.appendChild(richNodes(c.rich)); }
      else if (c.hide) el.style.setProperty("display", "none", "important");
      else if (typeof c.href === "string" && safeURL(c.href)) el.setAttribute("href", c.href);
      else if (c.style) {
        for (var k in c.style) if (STYLE.test(k) && safeStyle(c.style[k])) el.style.setProperty(k, c.style[k], "important");
      } else if (target && el !== target && !el.contains(target)) {
        if (c.move === "before") target.parentNode.insertBefore(el, target);
        else if (c.move === "after") target.parentNode.insertBefore(el, target.nextSibling);
        else if (c.move === "start") target.insertBefore(el, target.firstChild);
        else if (c.move === "end") target.appendChild(el);
      }
      if (typeof c.src === "string" && safeURL(c.src)) el.setAttribute("src", c.src);
      if (typeof c.alt === "string") el.setAttribute("alt", c.alt);
    });
  }
  function applyCSS() {
    var st = document.getElementById("of-css-editor");
    if (!st) { st = document.createElement("style"); st.id = "of-css-editor"; (document.head || document.documentElement).appendChild(st); }
    st.textContent = safeCSS(state.css) ? state.css : "";
  }
  function short(s) { s = String(s); return s.length > 40 ? s.slice(0, 40) + "…" : s; }
  function describe(c) {
    var d = c.device === "mobile" ? " (phones)" : c.device === "desktop" ? " (wider screens)" : "";
    if (c.hide) return "Hide" + d;
    if (typeof c.text === "string") return "Text: " + short(c.text) + d;
    if (typeof c.rich === "string") return "Formatted text: " + short(c.rich) + d;
    if (typeof c.href === "string") return "Link: " + c.href + d;
    if (typeof c.src === "string") return "Image: " + c.src + d;
    if (typeof c.alt === "string") return "Alt text: " + c.alt + d;
    if (c.style) return "Style: " + Object.keys(c.style).map(function (k) { return k + " " + c.style[k]; }).join(", ") + d;
    if (c.move) return "Move " + c.move + " another element" + d;
    if (c.insert) return "Add " + c.insert.kind + " " + c.insert.where + (c.insert.text ? ": " + short(c.insert.text) : "") + d;
    return "Change" + d;
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
    ".panel{position:fixed;right:16px;bottom:16px;width:340px;max-width:calc(100vw - 32px);max-height:78vh;overflow:auto;pointer-events:auto;" +
    "background:#fff;color:#111827;border:1px solid #e5e7eb;border-radius:10px;box-shadow:0 10px 30px rgba(0,0,0,.18);" +
    "font:13px/1.45 system-ui,-apple-system,Segoe UI,sans-serif;padding:12px}" +
    "h1{font-size:14px;margin:0 0 2px}p{margin:4px 0;color:#4b5563}code{font:11px ui-monospace,monospace;word-break:break-all;color:#374151}" +
    "label{display:block;margin:8px 0 2px;font-weight:600}label.inline{display:inline-flex;gap:4px;align-items:center;font-weight:400;margin:4px 8px 0 0}" +
    "input,textarea,select{box-sizing:border-box;width:100%;font:inherit;padding:5px 7px;border:1px solid #d1d5db;border-radius:6px;color:#111827;background:#fff}" +
    "input[type=checkbox],input[type=radio]{width:auto;padding:0}input[type=color]{width:34px;height:30px;padding:2px;flex:none}" +
    ".row{display:flex;gap:6px;align-items:center}.grid{display:grid;grid-template-columns:1fr 1fr;gap:4px 8px}.grid label{margin-top:4px}" +
    "textarea{min-height:56px;resize:vertical}textarea.code{font:12px ui-monospace,monospace;min-height:90px}" +
    "button{font:inherit;padding:5px 10px;border-radius:6px;border:1px solid #d1d5db;background:#fff;color:#111827;cursor:pointer;margin:6px 6px 0 0}" +
    "button.primary{background:#4f46e5;border-color:#4f46e5;color:#fff}button:disabled{opacity:.5;cursor:default}" +
    ".tabs{display:flex;gap:2px;margin:8px 0 4px;border-bottom:1px solid #e5e7eb}.tabs button{margin:0;border:0;border-radius:6px 6px 0 0;padding:4px 8px;color:#4b5563}" +
    ".tabs button.on{background:#eef2ff;color:#3730a3;font-weight:600}" +
    "ul{list-style:none;padding:0;margin:6px 0}li{display:flex;gap:6px;align-items:flex-start;padding:4px 0;border-top:1px solid #f3f4f6}" +
    "li span{flex:1;min-width:0}li button{margin:0;padding:1px 6px}" +
    "details{margin-top:8px;border-top:1px solid #f3f4f6;padding-top:6px}summary{cursor:pointer;font-weight:600}" +
    ".hint{font-size:12px;color:#6b7280}.msg{color:#b91c1c}.ok{color:#047857}" +
    "</style>" +
    "<div class='box hover'></div><div class='box sel'></div><div class='panel' role='dialog' aria-label='Outflare editor'></div>";
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
  var INLINE = /^(B|I|STRONG|EM|A|SPAN|BR|SMALL|U|MARK)$/;
  function textOnly(el) {
    return Array.prototype.every.call(el.children, function (c) { return INLINE.test(c.tagName) && !c.children.length; });
  }
  // The element's text as simple formatting, so editing starts from what
  // is there.
  function asRich(el) {
    var out = "";
    Array.prototype.forEach.call(el.childNodes, function (n) {
      if (n.nodeType === 3) out += n.nodeValue;
      else if (n.tagName === "BR") out += "\n";
      else if (n.tagName === "B" || n.tagName === "STRONG") out += "**" + n.textContent + "**";
      else if (n.tagName === "I" || n.tagName === "EM") out += "*" + n.textContent + "*";
      else if (n.tagName === "A" && safeURL(n.getAttribute("href") || "")) out += "[" + n.textContent + "](" + n.getAttribute("href") + ")";
      else out += n.textContent;
    });
    return out.replace(/\s+\n/g, "\n").trim();
  }
  function computed(el, k) {
    try { return getComputedStyle(el).getPropertyValue(k); } catch (e) { return ""; }
  }
  function hex(rgb) {
    var m = /rgba?\((\d+),\s*(\d+),\s*(\d+)/.exec(rgb || "");
    if (!m) return "#000000";
    return "#" + [m[1], m[2], m[3]].map(function (x) { return ("0" + (+x).toString(16)).slice(-2); }).join("");
  }

  var STYLE_FIELDS = [
    ["color", "Text colour", "color"], ["background-color", "Background", "color"],
    ["font-size", "Text size", "text", "18px"], ["font-weight", "Weight", "select", ["", "normal", "500", "600", "bold"]],
    ["text-align", "Align", "select", ["", "left", "center", "right"]], ["text-transform", "Case", "select", ["", "none", "uppercase", "capitalize"]],
    ["padding", "Padding", "text", "12px 20px"], ["margin", "Margin", "text", "0 0 16px"],
    ["border-radius", "Corners", "text", "8px"], ["border", "Border", "text", "1px solid #e5e7eb"],
    ["width", "Width", "text", "100%"], ["display", "Display", "select", ["", "block", "inline-block", "flex", "grid"]],
  ];

  function contentTab(el) {
    var h = "", tag = el.tagName;
    if (textOnly(el)) {
      var rich = el.children.length > 0;
      h += "<label for='t'>Text</label><textarea id='t'>" + esc(rich ? asRich(el) : el.textContent || "") + "</textarea>" +
        "<label class='inline'><input type='checkbox' id='rich'" + (rich ? " checked" : "") + "> Formatting</label>" +
        "<span class='hint'>**bold**, *italic*, [link](/page), new lines</span><br><button data-do='text'>Set text</button>";
    } else {
      h += "<p class='hint'>This holds other elements. Select one of them to change its text.</p>";
    }
    if (tag === "A") h += "<label for='h'>Link</label><input id='h' value='" + esc(el.getAttribute("href") || "") + "'><button data-do='href'>Set link</button>";
    if (tag === "IMG") {
      h += "<label for='s'>Image address</label><input id='s' value='" + esc(el.getAttribute("src") || "") + "'>" +
        "<label for='a'>Alt text</label><input id='a' value='" + esc(el.getAttribute("alt") || "") + "'><button data-do='src'>Set image</button>";
    }
    return h;
  }
  function styleTab(el) {
    var h = "<div class='grid'>";
    STYLE_FIELDS.forEach(function (f) {
      var id = "st-" + f[0], now = computed(el, f[0]);
      h += "<div><label for='" + id + "'>" + esc(f[1]) + "</label>";
      if (f[2] === "color") {
        h += "<div class='row'><input type='color' data-for='" + id + "' value='" + esc(hex(now)) + "'><input id='" + id + "' placeholder='" + esc(now) + "'></div>";
      } else if (f[2] === "select") {
        h += "<select id='" + id + "'>" + f[3].map(function (o) { return "<option value='" + esc(o) + "'>" + esc(o || "Keep (" + now + ")") + "</option>"; }).join("") + "</select>";
      } else {
        h += "<input id='" + id + "' placeholder='" + esc(f[3] + "  (now " + now + ")") + "'>";
      }
      h += "</div>";
    });
    return h + "</div><button data-do='style'>Set style</button><p class='hint'>Leave a box empty to keep it as it is.</p>";
  }
  function moveTab(el) {
    var prev = el.previousElementSibling, next = el.nextElementSibling;
    return "<p>Move this element among the ones around it.</p>" +
      "<button data-do='up'" + (prev ? "" : " disabled") + ">Move up</button>" +
      "<button data-do='down'" + (next ? "" : " disabled") + ">Move down</button>" +
      "<p class='hint'>Visitors in this version see it in its new place.</p>";
  }
  function addTab() {
    return "<label for='ik'>Add</label><select id='ik'><option value='text'>Text</option><option value='button'>Button</option>" +
      "<option value='link'>Link</option><option value='image'>Image</option></select>" +
      "<label class='inline'><input type='radio' name='iw' value='after' checked> After this</label>" +
      "<label class='inline'><input type='radio' name='iw' value='before'> Before this</label>" +
      "<label for='it'>Text</label><input id='it' placeholder='Start free trial'>" +
      "<label for='ih'>Link or image address</label><input id='ih' placeholder='/signup or https://…'>" +
      "<button data-do='insert'>Add it</button><p class='hint'>Then select what you added to style it.</p>";
  }

  function render() {
    var i = state.info, el = state.selected, h = "";
    if (!i) {
      panel.innerHTML = "<h1>Outflare editor</h1><p class='" + (state.msg ? "msg" : "") + "'>" + esc(state.msg || "Loading…") + "</p>";
      return;
    }
    h += "<h1>Editing " + esc(i.flag_name || i.flag_key) + " · " + esc(i.variant) + "</h1>";
    h += "<div class='row'><label for='dev' style='margin:4px 0'>New changes apply to</label></div>" +
      "<select id='dev'><option value=''>All screens</option><option value='mobile'" + (state.device === "mobile" ? " selected" : "") +
      ">Phones only</option><option value='desktop'" + (state.device === "desktop" ? " selected" : "") + ">Wider screens only</option></select>";
    if (!el) {
      h += "<p>" + (state.picking ? "Click anything on the page to change it." : "Picking is paused.") + "</p>";
    } else {
      var sel = selectorFor(el);
      h += "<p>Selected <code>" + esc(sel) + "</code> (" + find(sel).length + " on this page)</p>";
      h += "<div class='tabs' role='tablist'>" + [["content", "Content"], ["style", "Style"], ["move", "Move"], ["add", "Add"]].map(function (t) {
        return "<button role='tab' data-tab='" + t[0] + "' class='" + (state.tab === t[0] ? "on" : "") + "' aria-selected='" + (state.tab === t[0]) + "'>" + t[1] + "</button>";
      }).join("") + "</div>";
      h += state.tab === "style" ? styleTab(el) : state.tab === "move" ? moveTab(el) : state.tab === "add" ? addTab() : contentTab(el);
      h += "<div><button data-do='hide'>Hide it</button><button data-do='parent'>Select parent</button><button data-do='deselect'>Pick another</button></div>";
    }
    h += "<label>Changes (" + state.changes.length + "/" + MAX + ")</label><ul>";
    state.changes.forEach(function (c, n) {
      h += "<li><span>" + esc(describe(c)) + "<br><code>" + esc(c.selector) + "</code></span><button data-del='" + n + "' aria-label='Remove'>×</button></li>";
    });
    h += "</ul>";
    if (state.changes.length) h += "<button data-do='undo'>Undo last</button>";
    h += "<details" + (state.css || state.redirect ? " open" : "") + "><summary>Version settings</summary>" +
      "<label for='rd'>Send this version's visitors to another page</label><input id='rd' value='" + esc(state.redirect) + "' placeholder='/pricing-b'>" +
      "<p class='hint'>A split URL test: a path on your site, or an address on your brand's domains. Leave empty to show this page.</p>" +
      "<label for='css'>Extra CSS for this version</label><textarea id='css' class='code' placeholder='.hero h1 { letter-spacing: -0.02em; }'>" + esc(state.css) + "</textarea>" +
      "<p class='hint'>Looks only: no url(), @import or fonts.</p><button data-do='settings'>Use these settings</button></details>";
    if (state.msg) h += "<p class='" + (state.msgOK ? "ok" : "msg") + "' role='status'>" + esc(state.msg) + "</p>";
    h += "<button class='primary' data-do='save'" + (state.busy ? " disabled" : "") + ">Save" + (state.dirty ? " changes" : "") + "</button>" +
      "<button data-do='phone'>Preview on a phone</button><button data-do='close'>Close</button>";
    panel.innerHTML = h;
  }

  function add(c) {
    if (state.changes.length >= MAX) { say("A version can have up to " + MAX + " changes.", false); return; }
    if (state.device) c.device = state.device;
    // One change per element, kind and screen: a new text replaces the old one.
    var kind = Object.keys(c).filter(function (k) { return k !== "selector" && k !== "device" && k !== "target" && k !== "alt"; })[0];
    state.changes = state.changes.filter(function (o) {
      return !(o.selector === c.selector && kind !== "insert" && kind in o && (o.device || "") === (c.device || ""));
    });
    state.changes.push(c);
    apply(c, state.changes.length - 1);
    state.dirty = true;
    state.msg = "";
    render();
    place(selBox, state.selected);
  }
  function say(m, ok) { state.msg = m; state.msgOK = ok; render(); }

  panel.addEventListener("input", function (e) {
    var t = e.target, f = t.getAttribute && t.getAttribute("data-for");
    if (f) root.querySelector("#" + f).value = t.value;
  });
  panel.addEventListener("change", function (e) {
    if (e.target.id === "dev") state.device = e.target.value;
  });
  panel.addEventListener("click", function (e) {
    var t = e.target, act = t.getAttribute && t.getAttribute("data-do"), del = t.getAttribute && t.getAttribute("data-del"), tab = t.getAttribute && t.getAttribute("data-tab");
    if (tab) { state.tab = tab; render(); return; }
    if (del !== null && del !== undefined) {
      state.changes.splice(+del, 1);
      state.dirty = true;
      say("Removed. Save, then reload the page to see it without that change.", true);
      return;
    }
    var el = state.selected, sel = el && selectorFor(el), v = function (id) { var n = root.querySelector("#" + id); return n ? n.value : ""; };
    switch (act) {
      case "text":
        if (root.querySelector("#rich").checked) add({ selector: sel, rich: v("t").slice(0, 2000) });
        else add({ selector: sel, text: v("t").slice(0, 2000) });
        break;
      case "href":
        if (!safeURL(v("h"))) { say("A link starts with https:// or /.", false); return; }
        add({ selector: sel, href: v("h") }); break;
      case "src":
        if (!safeURL(v("s"))) { say("An image address starts with https:// or /.", false); return; }
        add({ selector: sel, src: v("s"), alt: v("a").slice(0, 500) }); break;
      case "style": {
        var st = {}, bad = "";
        STYLE_FIELDS.forEach(function (f) {
          var val = v("st-" + f[0]).trim();
          if (!val) return;
          if (!safeStyle(val)) bad = f[1];
          st[f[0]] = val;
        });
        if (bad) { say(bad + ": use colours, sizes and words, such as #0f766e or 18px.", false); return; }
        if (!Object.keys(st).length) { say("Fill in at least one box.", false); return; }
        add({ selector: sel, style: st });
        break;
      }
      case "up": case "down": {
        var other = act === "up" ? el.previousElementSibling : el.nextElementSibling;
        if (!other) return;
        add({ selector: sel, move: act === "up" ? "before" : "after", target: selectorFor(other) });
        break;
      }
      case "insert": {
        var kind = v("ik"), where = (root.querySelector("input[name=iw]:checked") || {}).value || "after", inn = { where: where, kind: kind };
        if (kind === "image") {
          if (!safeURL(v("ih"))) { say("An image address starts with https:// or /.", false); return; }
          inn.src = v("ih"); inn.alt = v("it").slice(0, 500);
        } else {
          if (!v("it")) { say("Write the text first.", false); return; }
          inn.text = v("it").slice(0, 2000);
          if (kind !== "text") {
            if (!safeURL(v("ih"))) { say("A link starts with https:// or /.", false); return; }
            inn.href = v("ih");
          }
        }
        add({ selector: sel, insert: inn });
        break;
      }
      case "hide": add({ selector: sel, hide: true }); state.selected = null; place(selBox, null); render(); break;
      case "parent":
        if (el && el.parentElement && el.parentElement !== document.documentElement) { state.selected = el.parentElement; place(selBox, state.selected); render(); }
        break;
      case "deselect": state.selected = null; state.picking = true; place(selBox, null); render(); break;
      case "undo":
        state.changes.pop();
        state.dirty = true;
        say("Undone. Save, then reload the page to see it without that change.", true);
        break;
      case "settings": {
        var css = v("css"), rd = v("rd").trim();
        if (!safeCSS(css)) { say("The CSS can change looks only: no url(), @import, fonts or <.", false); return; }
        if (rd && !safeURL(rd)) { say("The page to send visitors to starts with / or https://.", false); return; }
        state.css = css; state.redirect = rd; state.dirty = true;
        applyCSS();
        say(rd ? "Saved here. Visitors in this version will go to " + rd + " once you save." : "Settings ready: save to use them.", true);
        break;
      }
      case "save": save(); break;
      case "phone": {
        var u = new URL(location.href);
        u.searchParams.set("of_flag", state.info.flag_key + ":" + state.info.variant);
        var wnd = window.open(u.toString(), "of_phone", "width=390,height=844");
        say(wnd ? "Opened a phone-sized window. It shows what you have saved." : "Your browser blocked the window: allow pop-ups for this site.", !!wnd);
        break;
      }
      case "close":
        if (state.dirty && !window.confirm("Close without saving your changes?")) return;
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
    state.tab = "content";
    place(hoverBox, null);
    place(selBox, e.target);
    state.msg = "";
    render();
  }
  window.addEventListener("scroll", function () { place(selBox, state.selected); }, true);
  window.addEventListener("beforeunload", function (e) {
    if (state.dirty) { e.preventDefault(); e.returnValue = ""; }
  });

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
  function load(j) {
    state.info = j;
    state.changes = j.changes || [];
    state.css = j.css || "";
    state.redirect = j.redirect || "";
    state.dirty = false;
  }
  function save() {
    state.busy = true;
    say("Saving…", true);
    request("POST", { token: token, changes: state.changes, css: state.css, redirect: state.redirect }).then(function (j) {
      state.busy = false;
      load(j);
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
      load(j);
      applyCSS();
      render();
    }, function (err) {
      say(err.message, false);
    });
  }
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", start);
  else start();
})();
