/* Outflare visual editor. The site script loads it for an edit link from
   Outflare (?of_edit=<token>). A bar across the top says which version is
   open (and switches to another), shows phone or computer, undoes and
   redoes, and saves. Pick an element on the page and change its text (or
   double-click to type on the page), its look, its place (or drag it), a
   link or image, hide it, or add a new heading, button, link or image next
   to it; or describe a change to the workspace's own AI, for the whole page
   or the part picked, and keep or undo what it writes. A version can also
   have its own CSS and send its visitors to another page (a split URL
   test). Save, and the changes become the flag version's payload, which
   visitors in that version see. Everything it draws lives in a shadow root,
   so the page's styles don't reach it. It never adds scripts: everything it
   saves is data the site script applies safely. */
(function () {
  "use strict";
  var me = document.currentScript;
  if (!me || window.__outflareEditor) return;
  window.__outflareEditor = true;
  var token = me.getAttribute("data-token") || "";
  var api = (me.getAttribute("data-api") || "").replace(/\/$/, "");
  if (!token || !api) return;

  var MAX = 50, BAR = 48;
  var state = {
    info: null, changes: [], css: "", redirect: "", device: "", picking: true, selected: null,
    busy: false, msg: "", msgOK: false, tab: "edit", part: "content", dirty: false, saved: [],
    undo: [], redo: [], ai: null, aiPrompt: "", aiKept: null, reload: false,
  };
  // The version to open: the link's, or the one switched to in this tab.
  var wanted = "";
  try {
    var sw = JSON.parse(sessionStorage.getItem("of_edit_variant") || "null");
    if (sw && sw.token === token) wanted = sw.variant;
  } catch (e) {}

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

  // The site's own button look, for buttons added here, as the site script
  // gives them to visitors.
  function buttonClass() {
    var b = document.querySelector("a[class*='btn'],a[class*='button'],button[class*='btn'],button[class*='button']");
    var c = b && !inEditor(b) ? String(b.className).split(/\s+/).filter(function (x) { return x && x !== "of-button"; }).slice(0, 4).join(" ") : "";
    return (c ? c + " " : "") + "of-button";
  }

  // Changes made here are shown on the page now, as the site script will
  // for visitors, and each keeps a way to take it back off. Changes saved
  // before were shown by the site script when the page opened; taking one
  // of those off shows once the page reloads.
  var undoers = new Map();
  // keep marks an undoer with the nodes a change touched, so alive() says
  // whether the change is still on the page (a page that builds itself
  // again, such as a React app finishing hydration, throws them away).
  function keep(back, nodes) {
    back.alive = function () { return nodes.every(function (n) { return n && n.isConnected; }); };
    return back;
  }
  function nothing() { return keep(function () {}, []); }
  function applyChange(c, n, adopt) {
    if (c.device && (c.device === "mobile") !== phone()) return nothing();
    var els = Array.prototype.filter.call(find(c.selector), function (el) { return !inEditor(el); });
    if (c.insert) {
      var inn = c.insert, node = null;
      // The site script made this one when the page opened: take it over.
      if (adopt && state.info) {
        var mine = document.querySelector("[data-of-insert='" + state.info.flag_key + ":" + n + "']");
        if (mine) return keep(function () { if (mine.parentNode) mine.parentNode.removeChild(mine); }, [mine]);
      }
      if (!els.length) return nothing();
      if (inn.kind === "image" && safeURL(inn.src)) { node = document.createElement("img"); node.setAttribute("src", inn.src); node.setAttribute("alt", inn.alt || ""); }
      else if ((inn.kind === "button" || inn.kind === "link") && safeURL(inn.href)) {
        node = document.createElement("a"); node.setAttribute("href", inn.href); node.textContent = inn.text || "";
        if (inn.kind === "button") { node.className = buttonClass(); node.setAttribute("role", "button"); }
      } else if (inn.kind === "text") { node = document.createElement("p"); node.textContent = inn.text || ""; }
      if (!node) return nothing();
      node.setAttribute("data-of-insert", "editor:" + n);
      var at = els[0];
      at.parentNode.insertBefore(node, inn.where === "before" ? at : at.nextSibling);
      return keep(function () { if (node.parentNode) node.parentNode.removeChild(node); }, [node]);
    }
    var target = null;
    if (c.move) { target = find(c.target)[0]; if (!target) return nothing(); }
    if (!els.length) return nothing();
    var backs = [];
    els.forEach(function (el) {
      var kids = Array.prototype.map.call(el.childNodes, function (k) { return k; });
      var style = el.getAttribute("style"), href = el.getAttribute("href"), src = el.getAttribute("src"), alt = el.getAttribute("alt");
      var parent = el.parentNode, next = el.nextSibling;
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
      backs.push(function () {
        if (typeof c.text === "string" || typeof c.rich === "string") {
          el.textContent = "";
          kids.forEach(function (k) { el.appendChild(k); });
        }
        if (c.hide || c.style) { if (style === null) el.removeAttribute("style"); else el.setAttribute("style", style); }
        [["href", href], ["src", src], ["alt", alt]].forEach(function (a) {
          if (a[0] in c) { if (a[1] === null) el.removeAttribute(a[0]); else el.setAttribute(a[0], a[1]); }
        });
        if (c.move && parent) parent.insertBefore(el, next && next.parentNode === parent ? next : null);
      });
    });
    var back = keep(function () { for (var i = backs.length - 1; i >= 0; i--) backs[i](); }, els.concat(target ? [target] : []));
    // The site script showed this one before the editor opened, so what
    // it keeps isn't the page as it was: taking it off needs a reload.
    if (adopt) back.adopted = true;
    return back;
  }
  // setChanges makes list the version's changes, taking off what left it
  // (newest first) and showing what joined it.
  function setChanges(list) {
    var gone = state.changes.filter(function (c) { return list.indexOf(c) < 0; });
    for (var i = gone.length - 1; i >= 0; i--) {
      var back = undoers.get(gone[i]);
      if (back && !back.adopted) back(); else state.reload = true;
      undoers.delete(gone[i]);
    }
    list.forEach(function (c, n) {
      if (state.changes.indexOf(c) < 0 && !undoers.has(c)) undoers.set(c, applyChange(c, n));
    });
    state.changes = list;
    state.dirty = JSON.stringify(list) !== JSON.stringify(state.saved) || state.dirty && state.cssDirty;
  }
  // reapply shows again any change the page threw away (a page that
  // builds itself in the browser replaces what the site script and the
  // editor changed), and shows the saved ones the first time, taking
  // over what the site script already made.
  var reapplying = false, reapplied = 0;
  function reapply(first) {
    if (reapplying || reapplied > 300) return;
    reapplying = true;
    try {
      state.changes.forEach(function (c, n) {
        var u = undoers.get(c);
        if (u && u.alive()) return;
        undoers.set(c, applyChange(c, n, first || (u && u.adopted)));
      });
    } finally { reapplying = false; }
    reapplied++;
  }
  var reapplyTimer = 0;
  function watchPage() {
    if (!window.MutationObserver || !document.body) return;
    new MutationObserver(function (list) {
      if (reapplying || list.every(function (m) { return inEditor(m.target); })) return;
      clearTimeout(reapplyTimer);
      reapplyTimer = setTimeout(function () { reapply(false); place(selBox, state.selected); }, 80);
    }).observe(document.body, { childList: true, subtree: true });
  }
  // edit records the change of the list as one step to undo.
  function edit(list) {
    state.undo.push(state.changes);
    if (state.undo.length > 100) state.undo.shift();
    state.redo = [];
    setChanges(list);
  }
  function applyCSS() {
    var st = document.getElementById("of-css-editor");
    if (!st) { st = document.createElement("style"); st.id = "of-css-editor"; (document.head || document.documentElement).appendChild(st); }
    st.textContent = safeCSS(state.css) ? state.css : "";
  }
  function short(s, n) { s = String(s); n = n || 40; return s.length > n ? s.slice(0, n) + "…" : s; }
  function describe(c) {
    var d = c.device === "mobile" ? " (phones)" : c.device === "desktop" ? " (computers)" : "";
    if (c.hide) return "Hidden" + d;
    if (typeof c.text === "string") return "Text: " + short(c.text) + d;
    if (typeof c.rich === "string") return "Text: " + short(c.rich.replace(/\*\*|\*|\[|\]\([^)]*\)/g, "")) + d;
    if (typeof c.href === "string") return "Link: " + c.href + d;
    if (typeof c.src === "string") return "Image: " + c.src + d;
    if (typeof c.alt === "string") return "Image text: " + c.alt + d;
    if (c.style) return "Style: " + Object.keys(c.style).map(function (k) { return k + " " + c.style[k]; }).join(", ") + d;
    if (c.move) return "Moved " + c.move + " another part" + d;
    if (c.insert) return "New " + c.insert.kind + " " + c.insert.where + " it" + (c.insert.text ? ": " + short(c.insert.text) : "") + d;
    return "Change" + d;
  }

  // The parts of the page, as the AI and the layers list see them.
  var OUTLINE = /^(H[1-6]|P|A|BUTTON|IMG|LI|BLOCKQUOTE|FIGCAPTION|LABEL|SECTION|HEADER|FOOTER|FORM|NAV)$/;
  function visible(el) {
    var r = el.getBoundingClientRect();
    return r.width > 0 && r.height > 0 && computed(el, "visibility") !== "hidden";
  }
  function partText(el) {
    if (el.tagName === "IMG") return el.getAttribute("alt") || "(image)";
    var t = (el.innerText || el.textContent || "").replace(/\s+/g, " ").trim();
    if (/^(SECTION|HEADER|FOOTER|FORM|NAV)$/.test(el.tagName)) {
      var h = el.querySelector("h1,h2,h3,h4");
      return "(" + el.tagName.toLowerCase() + (h ? ": " + short(h.textContent.replace(/\s+/g, " ").trim(), 80) : "") + ")";
    }
    return short(t, 200);
  }
  function outline(extra) {
    var out = [], seen = new Set();
    var all = document.body ? document.body.querySelectorAll("*") : [];
    for (var i = 0; i < all.length && out.length < 200; i++) {
      var el = all[i];
      if (!OUTLINE.test(el.tagName) || inEditor(el) || el.closest("script,style,noscript,template,svg,iframe") || !visible(el)) continue;
      var text = partText(el);
      if (!text || (el.tagName === "LI" && el.children.length === 1 && el.firstElementChild.tagName === "A")) continue;
      var sel = selectorFor(el);
      if (seen.has(sel)) continue;
      seen.add(sel);
      out.push({ el: el, selector: sel, tag: el.tagName.toLowerCase(), text: text, href: el.tagName === "A" ? el.getAttribute("href") || "" : "" });
    }
    if (extra && !seen.has(selectorFor(extra))) out.push({ el: extra, selector: selectorFor(extra), tag: extra.tagName.toLowerCase(), text: partText(extra) || "(" + extra.tagName.toLowerCase() + ")", href: "" });
    return out;
  }

  // The overlay.
  var host = document.createElement("div");
  host.setAttribute("data-outflare-editor", "");
  host.style.cssText = "all:initial;position:fixed;inset:0;pointer-events:none;z-index:2147483647";
  var root = host.attachShadow ? host.attachShadow({ mode: "open" }) : host;
  root.innerHTML =
    "<style>" +
    ":host{all:initial}*{box-sizing:border-box}" +
    ".box{position:fixed;pointer-events:none;border:2px solid #4f46e5;background:rgba(79,70,229,.08);border-radius:2px;display:none}" +
    ".sel{border-color:#db2777;background:rgba(219,39,119,.06)}.drop{position:fixed;height:3px;background:#db2777;display:none;pointer-events:none;border-radius:2px}" +
    ".handle{position:fixed;display:none;pointer-events:auto;cursor:grab;background:#db2777;color:#fff;font:11px/1 system-ui,sans-serif;padding:4px 6px;border-radius:4px 4px 0 0;user-select:none}" +
    ".bar,.panel{pointer-events:auto;font:13px/1.45 system-ui,-apple-system,Segoe UI,sans-serif;color:#111827}" +
    ".bar{position:fixed;left:0;right:0;top:0;height:" + BAR + "px;display:flex;align-items:center;gap:8px;padding:0 12px;background:#111827;color:#f9fafb;box-shadow:0 2px 10px rgba(0,0,0,.25);overflow-x:auto;white-space:nowrap}" +
    ".bar b{font-weight:700;letter-spacing:-.01em}.bar .name{color:#d1d5db;max-width:220px;overflow:hidden;text-overflow:ellipsis}" +
    ".bar select,.bar button{font:inherit;height:30px;border-radius:6px;border:1px solid #374151;background:#1f2937;color:#f9fafb;padding:0 8px;cursor:pointer;margin:0}" +
    ".bar button:disabled{opacity:.4;cursor:default}.bar .primary{background:#4f46e5;border-color:#4f46e5}.bar .seg{display:inline-flex}" +
    ".bar .seg button{border-radius:0}.bar .seg button:first-child{border-radius:6px 0 0 6px}.bar .seg button:last-child{border-radius:0 6px 6px 0}.bar .seg button.on{background:#374151}" +
    ".bar .status{color:#9ca3af;font-size:12px}.bar .status.dirty{color:#fbbf24}.bar .grow{flex:1}" +
    ".panel{position:fixed;right:12px;top:" + (BAR + 10) + "px;bottom:12px;width:350px;max-width:calc(100vw - 24px);overflow:auto;" +
    "background:#fff;border:1px solid #e5e7eb;border-radius:10px;box-shadow:0 10px 30px rgba(0,0,0,.18);padding:12px}" +
    ".panel.min{bottom:auto;max-height:44px;overflow:hidden}" +
    "@media (max-width:640px){.panel{top:auto;left:8px;right:8px;bottom:8px;width:auto;max-height:45vh}.panel.min{bottom:8px}}" +
    "h1{font-size:14px;margin:0 0 2px}h2{font-size:13px;margin:10px 0 4px}p{margin:4px 0;color:#4b5563}code{font:11px ui-monospace,monospace;word-break:break-all;color:#374151}" +
    "label{display:block;margin:8px 0 2px;font-weight:600}label.inline{display:inline-flex;gap:4px;align-items:center;font-weight:400;margin:4px 8px 0 0}" +
    ".panel input,.panel textarea,.panel select{width:100%;font:inherit;padding:5px 7px;border:1px solid #d1d5db;border-radius:6px;color:#111827;background:#fff}" +
    ".panel input[type=checkbox],.panel input[type=radio]{width:auto;padding:0}.panel input[type=color]{width:34px;height:30px;padding:2px;flex:none}" +
    ".row{display:flex;gap:6px;align-items:center}.grid{display:grid;grid-template-columns:1fr 1fr;gap:4px 8px}.grid label{margin-top:4px}" +
    "textarea{min-height:56px;resize:vertical}textarea.code{font:12px ui-monospace,monospace;min-height:90px}" +
    ".panel button{font:inherit;padding:5px 10px;border-radius:6px;border:1px solid #d1d5db;background:#fff;color:#111827;cursor:pointer;margin:6px 6px 0 0}" +
    ".panel button.primary{background:#4f46e5;border-color:#4f46e5;color:#fff}.panel button:disabled{opacity:.5;cursor:default}" +
    ".tabs{display:flex;gap:2px;margin:8px 0 4px;border-bottom:1px solid #e5e7eb}.tabs button{margin:0;border:0;border-radius:6px 6px 0 0;padding:4px 8px;color:#4b5563}" +
    ".tabs button.on{background:#eef2ff;color:#3730a3;font-weight:600}" +
    "ul{list-style:none;padding:0;margin:6px 0}li{display:flex;gap:6px;align-items:flex-start;padding:4px 0;border-top:1px solid #f3f4f6}" +
    "li span{flex:1;min-width:0}li button{margin:0!important;padding:1px 6px!important}li.part{cursor:pointer}li.part:hover{background:#f9fafb}li.part .tag{flex:none;width:58px;color:#6b7280;font:11px ui-monospace,monospace}" +
    "details{margin-top:8px;border-top:1px solid #f3f4f6;padding-top:6px}summary{cursor:pointer;font-weight:600}" +
    ".hint{font-size:12px;color:#6b7280}.msg{color:#b91c1c}.ok{color:#047857}" +
    ".ai{border:1px solid #c7d2fe;background:#eef2ff;border-radius:8px;padding:8px;margin:8px 0}.ai textarea{min-height:44px}" +
    ".chips{display:flex;flex-wrap:wrap;gap:4px;margin-top:4px}.chips button{margin:0;padding:2px 8px;border-radius:999px;font-size:12px;color:#4b5563}" +
    ".work{display:flex;align-items:center;gap:6px}.dot{width:8px;height:8px;border-radius:50%;background:#4f46e5;animation:p 1s infinite alternate}@keyframes p{to{opacity:.2}}" +
    "</style>" +
    "<div class='box hover'></div><div class='box sel'></div><div class='drop'></div><div class='handle' title='Drag to move it'>⠿ Drag</div>" +
    "<div class='bar' role='toolbar' aria-label='Outflare editor'></div><div class='panel' role='dialog' aria-label='Outflare editor'></div>";
  var hoverBox = root.querySelector(".hover"), selBox = root.querySelector(".sel"), dropLine = root.querySelector(".drop"), handle = root.querySelector(".handle");
  var bar = root.querySelector(".bar"), panel = root.querySelector(".panel");

  function place(box, el) {
    if (!el || !el.isConnected) { box.style.display = "none"; if (box === selBox) handle.style.display = "none"; return; }
    var r = el.getBoundingClientRect();
    box.style.display = "block";
    box.style.left = r.left + "px"; box.style.top = r.top + "px";
    box.style.width = r.width + "px"; box.style.height = r.height + "px";
    if (box === selBox) {
      handle.style.display = "block";
      handle.style.left = r.left + "px";
      handle.style.top = Math.max(BAR, r.top - 20) + "px";
    }
  }
  function inEditor(el) { return el === host || (el && el.closest && el.closest("[data-outflare-editor]")); }

  function esc(s) {
    return String(s).replace(/[&<>"']/g, function (ch) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[ch];
    });
  }
  var INLINE = /^(B|I|STRONG|EM|A|SPAN|BR|SMALL|U|MARK)$/;
  function textOnly(el) {
    return el.tagName !== "IMG" && Array.prototype.every.call(el.children, function (c) { return INLINE.test(c.tagName) && !c.children.length; });
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
  var EXAMPLES = {
    page: ["Make the headline shorter and more direct", "Add a line about the free trial under the headline", "Make the main button stand out more"],
    part: ["Make this shorter", "Make this stand out more", "Add a button after this to /sign-up"],
  };

  function contentPart(el) {
    var h = "", tag = el.tagName;
    if (textOnly(el)) {
      var rich = el.children.length > 0;
      h += "<label for='t'>Text</label><textarea id='t'>" + esc(rich ? asRich(el) : el.textContent || "") + "</textarea>" +
        "<label class='inline'><input type='checkbox' id='rich'" + (rich ? " checked" : "") + "> Formatting</label>" +
        "<span class='hint'>**bold**, *italic*, [link](/page), new lines. Or double-click it on the page to type there.</span><br><button data-do='text'>Set text</button>";
    } else if (tag !== "IMG") {
      h += "<p class='hint'>This holds other parts. Pick one of them to change its text.</p>";
    }
    if (tag === "A") h += "<label for='h'>Link</label><input id='h' value='" + esc(el.getAttribute("href") || "") + "'><button data-do='href'>Set link</button>";
    if (tag === "IMG") {
      h += "<label for='s'>Image address</label><input id='s' value='" + esc(el.getAttribute("src") || "") + "'>" +
        "<label for='a'>Alt text</label><input id='a' value='" + esc(el.getAttribute("alt") || "") + "'><button data-do='src'>Set image</button>";
    }
    return h;
  }
  function stylePart(el) {
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
  function movePart(el) {
    var prev = el.previousElementSibling, next = el.nextElementSibling;
    return "<p>Drag it by its handle on the page, or move it among the parts around it.</p>" +
      "<button data-do='up'" + (prev ? "" : " disabled") + ">Move up</button>" +
      "<button data-do='down'" + (next ? "" : " disabled") + ">Move down</button>";
  }
  function addPart() {
    return "<label for='ik'>Add</label><select id='ik'><option value='text'>Text</option><option value='button'>Button</option>" +
      "<option value='link'>Link</option><option value='image'>Image</option></select>" +
      "<label class='inline'><input type='radio' name='iw' value='after' checked> After this</label>" +
      "<label class='inline'><input type='radio' name='iw' value='before'> Before this</label>" +
      "<label for='it'>Text</label><input id='it' placeholder='Start free trial'>" +
      "<label for='ih'>Link or image address</label><input id='ih' placeholder='/signup or https://…'>" +
      "<button data-do='insert'>Add it</button>";
  }

  function aiBox() {
    var i = state.info, el = state.selected, a = state.ai;
    if (!i.ai) return "<p class='hint'>Describing a change to your AI works on A/B tests of a page. Make one under A/B tests in Outflare.</p>";
    if (i.variant === i.control) return "";
    var h = "<div class='ai'><label for='ai' style='margin-top:0'>" + (el ? "Ask AI to change this " + esc(el.tagName.toLowerCase()) : "Ask AI to change this page") + "</label>" +
      "<textarea id='ai' placeholder='" + esc(el ? "e.g. make it shorter and say Start free" : "e.g. add a line about premium plans under the headline") + "'>" + esc(state.aiPrompt) + "</textarea>";
    if (!state.aiPrompt) h += "<div class='chips'>" + EXAMPLES[el ? "part" : "page"].map(function (x) { return "<button data-ex='" + esc(x) + "'>" + esc(x) + "</button>"; }).join("") + "</div>";
    if (a && (a.status === "queued" || a.status === "running")) {
      var secs = Math.max(0, Math.round((Date.now() - new Date(a.created_at).getTime()) / 1000));
      h += "<div class='work' role='status'><span class='dot'></span><span>" + (a.status === "queued" ? "Waiting to start" : "Reading the page and writing the change") + " · " + secs + "s</span></div>" +
        "<p class='hint'>" + (secs > 60 ? "Taking longer than usual; it stops by itself after 2 minutes." : "Usually 15 to 60 seconds.") + "</p><button data-do='ai-stop'>Stop</button>";
    } else {
      h += "<button class='primary' data-do='ai'" + (state.aiPrompt.trim() ? "" : " disabled") + ">Write the change</button>";
      if (a && a.status === "failed") h += "<p class='" + (a.error === "Stopped." ? "hint" : "msg") + "'>" + esc(a.error === "Stopped." ? "Stopped. Nothing changed." : a.error) + "</p>";
    }
    if (state.aiKept) {
      var v = state.aiKept;
      h += "<p class='ok'>" + esc(v.summary || "Done") + ": shown on the page.</p>" +
        "<ul>" + v.changes.map(function (c) { return "<li><span>" + esc(describe(c)) + "</span></li>"; }).join("") + "</ul>" +
        "<button class='primary' data-do='ai-keep'>Keep</button><button data-do='ai-undo'>Undo</button>";
    }
    return h + "</div>";
  }

  function renderBar() {
    var i = state.info;
    if (!i) { bar.innerHTML = "<b>Outflare</b><span class='status'>Loading…</span>"; return; }
    var versions = (i.versions || [i.variant]).map(function (v) {
      return "<option value='" + esc(v) + "'" + (v === i.variant ? " selected" : "") + ">" + esc(v === i.control ? v + " (today's page)" : "Version " + v) + "</option>";
    }).join("");
    var dev = [["", "All screens"], ["mobile", "Phone"], ["desktop", "Computer"]].map(function (d) {
      return "<button data-dev='" + d[0] + "' class='" + (state.device === d[0] ? "on" : "") + "' aria-pressed='" + (state.device === d[0]) + "'>" + d[1] + "</button>";
    }).join("");
    var status = state.busy ? "Saving…" : state.dirty ? "Unsaved changes" : "Saved";
    bar.innerHTML = "<b>Outflare</b><span class='name' title='" + esc(i.test_name || i.flag_name || i.flag_key) + "'>" + esc(i.test_name || i.flag_name || i.flag_key) + "</span>" +
      "<select data-act='version' aria-label='Version'>" + versions + "</select>" +
      "<span class='seg' role='group' aria-label='New changes apply to'>" + dev + "</span>" +
      "<button data-do='phone' title='Open what you saved in a phone-sized window'>Preview on a phone</button>" +
      "<button data-do='undo' title='Undo (Ctrl+Z)'" + (state.undo.length ? "" : " disabled") + ">Undo</button>" +
      "<button data-do='redo' title='Redo (Ctrl+Shift+Z)'" + (state.redo.length ? "" : " disabled") + ">Redo</button>" +
      "<span class='grow'></span><span class='status" + (state.dirty ? " dirty" : "") + "' role='status'>" + status + "</span>" +
      "<button class='primary' data-do='save'" + (state.busy || !state.dirty ? " disabled" : "") + ">Save</button>" +
      "<button data-do='close'>Close</button>";
  }

  function render() {
    renderBar();
    var i = state.info, el = state.selected, h = "";
    if (!i) {
      panel.innerHTML = "<h1>Outflare editor</h1><p class='" + (state.msg ? "msg" : "") + "'>" + esc(state.msg || "Loading…") + "</p>";
      return;
    }
    var control = i.variant === i.control;
    h += "<div class='tabs' role='tablist'>" + [["edit", "Edit"], ["layers", "Layers"], ["changes", "Changes (" + state.changes.length + ")"], ["settings", "Settings"]].map(function (t) {
      return "<button role='tab' data-tab='" + t[0] + "' class='" + (state.tab === t[0] ? "on" : "") + "' aria-selected='" + (state.tab === t[0]) + "'>" + t[1] + "</button>";
    }).join("") + "</div>";
    if (control) h += "<p class='hint'>This is today's page, the control: visitors in it see the page as it is. Switch to a new version in the bar to change it.</p>";
    if (state.tab === "edit") {
      h += aiBox();
      if (control) {
        // Nothing to pick on the control.
      } else if (!el) {
        h += "<p>" + (state.picking ? "Click anything on the page to change it, or double-click text to type." : "Picking is paused.") + "</p>";
      } else {
        var sel = selectorFor(el);
        h += "<h2>" + esc(el.tagName.toLowerCase()) + " <span class='hint'>" + esc(short(partText(el), 50)) + "</span></h2><p class='hint'><code>" + esc(sel) + "</code> · " + find(sel).length + " on this page</p>";
        h += "<div class='tabs' role='tablist'>" + [["content", "Content"], ["style", "Style"], ["move", "Move"], ["add", "Add"]].map(function (t) {
          return "<button role='tab' data-part='" + t[0] + "' class='" + (state.part === t[0] ? "on" : "") + "' aria-selected='" + (state.part === t[0]) + "'>" + t[1] + "</button>";
        }).join("") + "</div>";
        h += state.part === "style" ? stylePart(el) : state.part === "move" ? movePart(el) : state.part === "add" ? addPart() : contentPart(el);
        h += "<div><button data-do='hide'>Hide it</button><button data-do='parent'>Pick the part around it</button><button data-do='deselect'>Pick another</button></div>";
      }
    } else if (state.tab === "layers") {
      var parts = outline();
      h += "<p class='hint'>The page's parts, top to bottom. Click one to pick it.</p><ul>";
      parts.forEach(function (p, n) {
        h += "<li class='part' data-layer='" + n + "'><span class='tag'>" + esc(p.tag) + "</span><span>" + esc(short(p.text, 70)) + "</span></li>";
      });
      h += "</ul>";
      layers = parts;
    } else if (state.tab === "changes") {
      if (!state.changes.length) h += "<p>No changes yet: this version looks like today's page.</p>";
      h += "<ul>";
      state.changes.forEach(function (c, n) {
        h += "<li data-change='" + n + "'><span>" + esc(describe(c)) + "<br><code>" + esc(short(c.selector, 70)) + "</code></span><button data-del='" + n + "' aria-label='Remove'>×</button></li>";
      });
      h += "</ul><p class='hint'>" + state.changes.length + " of " + MAX + ". Point at one to see it on the page.</p>";
    } else {
      h += "<label for='rd'>Send this version's visitors to another page</label><input id='rd' value='" + esc(state.redirect) + "' placeholder='/pricing-b'>" +
        "<p class='hint'>A split URL test: a path on your site, or an address on your brand's domains. Leave empty to show this page.</p>" +
        "<label for='css'>Extra CSS for this version</label><textarea id='css' class='code' placeholder='.hero h1 { letter-spacing: -0.02em; }'>" + esc(state.css) + "</textarea>" +
        "<p class='hint'>Looks only: no url(), @import or fonts.</p><button data-do='settings'>Use these settings</button>";
    }
    if (state.reload) h += "<p class='hint'>Something saved before was taken off: it shows once you save and reload the page.</p>";
    if (state.msg) h += "<p class='" + (state.msgOK ? "ok" : "msg") + "' role='status'>" + esc(state.msg) + "</p>";
    var focus = root.activeElement && root.activeElement.id, pos = focus && root.activeElement.selectionStart;
    panel.innerHTML = h;
    if (focus) {
      var back = root.getElementById ? root.getElementById(focus) : root.querySelector("#" + focus);
      if (back) { back.focus(); try { back.setSelectionRange(pos, pos); } catch (e) {} }
    }
    place(selBox, state.selected);
  }
  var layers = [];

  function add(c) {
    if (state.info && state.info.variant === state.info.control) { say("Switch to a new version to change the page: today's page stays as it is.", false); return; }
    if (state.changes.length >= MAX) { say("A version can have up to " + MAX + " changes.", false); return; }
    if (state.device) c.device = state.device;
    // One change per element, kind and screen: a new text replaces the old one.
    var kind = Object.keys(c).filter(function (k) { return k !== "selector" && k !== "device" && k !== "target" && k !== "alt"; })[0];
    var list = state.changes.filter(function (o) {
      return !(o.selector === c.selector && kind !== "insert" && kind in o && (o.device || "") === (c.device || ""));
    });
    list.push(c);
    edit(list);
    state.msg = "";
    render();
  }
  function say(m, ok) { state.msg = m; state.msgOK = ok; render(); }

  function select(el) {
    state.selected = el;
    state.picking = !el;
    state.part = "content";
    state.tab = "edit";
    state.msg = "";
    place(hoverBox, null);
    render();
  }

  // The AI: the page as the editor sees it, and the part picked, if any.
  var aiTimer = 0;
  function askAI() {
    var el = state.selected, parts = outline(el);
    state.aiKept = null;
    state.ai = { status: "queued", created_at: new Date().toISOString() };
    render();
    post("/v1/site/flags/edits/ai", {
      token: token, variant: state.info.variant, prompt: state.aiPrompt.trim(), focus: el ? selectorFor(el) : "",
      outline: parts.map(function (p) { return { selector: p.selector, tag: p.tag, text: p.text, href: p.href }; }),
    }).then(function (run) { state.ai = run; render(); follow(); }, function (err) {
      state.ai = { status: "failed", error: err.code === "connector_needed" ? "Connect your AI model in Outflare (Settings, AI model) to use this." : err.message };
      render();
    });
  }
  function follow() {
    clearTimeout(aiTimer);
    aiTimer = setTimeout(function () {
      var a = state.ai;
      if (!a || !a.id) return;
      get("/v1/site/flags/edits/ai?token=" + encodeURIComponent(token) + "&run=" + encodeURIComponent(a.id)).then(function (run) {
        state.ai = run;
        if (run.status === "ready") showAI(run);
        else if (run.status === "queued" || run.status === "running") follow();
        render();
      }, function (err) { state.ai = { status: "failed", error: err.message }; render(); });
    }, 1500);
    // Keep the seconds ticking.
    if (state.tab === "edit" && !(root.activeElement && root.activeElement.id === "ai")) render();
  }
  function showAI(run) {
    var v = run.output && run.output.versions && run.output.versions[0];
    if (!v || !v.changes || !v.changes.length) { state.ai = { status: "failed", error: "The AI didn't write a change it could make here. Try saying it another way." }; return; }
    // A change the version already has is left out, so a button isn't added twice.
    var key = function (c) { return JSON.stringify(c, Object.keys(c).concat(c.insert ? Object.keys(c.insert) : [], c.style ? Object.keys(c.style) : []).sort()); };
    var have = state.changes.map(key);
    var fresh = v.changes.filter(function (c) { return have.indexOf(key(c)) < 0; });
    if (!fresh.length) { state.ai = { status: "failed", error: "That's already on the page. Try asking for something else." }; return; }
    var room = MAX - state.changes.length;
    var cs = fresh.slice(0, Math.max(0, room)).map(function (c) { if (state.device && !c.device) c.device = state.device; return c; });
    state.aiKept = { summary: v.summary, changes: cs, before: state.changes };
    edit(state.changes.concat(cs));
    state.aiPrompt = "";
  }

  var editing = null; // the element being typed into on the page
  function startTyping(el) {
    if (!textOnly(el) || (state.info && state.info.variant === state.info.control)) return;
    select(el);
    var rich = el.children.length > 0;
    editing = { el: el, rich: rich, before: rich ? asRich(el) : el.textContent };
    el.setAttribute("contenteditable", "true");
    el.focus();
    say("Type, then press Enter or click away. Esc cancels.", true);
  }
  function stopTyping(keep) {
    if (!editing) return;
    var e = editing, el = e.el;
    editing = null;
    var now = (el.innerText || el.textContent || "").replace(/\r/g, "").trim();
    el.removeAttribute("contenteditable");
    // Put the page back as it was, then set the text as a change.
    if (e.rich) { el.textContent = ""; el.appendChild(richNodes(e.before)); } else el.textContent = e.before;
    if (!keep || now === (e.rich ? el.innerText.trim() : e.before.trim())) { state.msg = ""; render(); return; }
    add(e.rich ? { selector: selectorFor(el), rich: now.slice(0, 2000) } : { selector: selectorFor(el), text: now.slice(0, 2000) });
  }

  panel.addEventListener("input", function (e) {
    var t = e.target, f = t.getAttribute && t.getAttribute("data-for");
    if (f) root.querySelector("#" + f).value = t.value;
    if (t.id === "ai") {
      var was = !!state.aiPrompt.trim();
      state.aiPrompt = t.value;
      if (was !== !!t.value.trim()) render();
    }
  });
  bar.addEventListener("change", function (e) {
    if (e.target.getAttribute("data-act") === "version") switchTo(e.target.value);
  });
  panel.addEventListener("mouseover", function (e) {
    var li = e.target.closest && e.target.closest("[data-change],[data-layer]");
    if (!li) return;
    var el = li.hasAttribute("data-layer") ? layers[+li.getAttribute("data-layer")] && layers[+li.getAttribute("data-layer")].el : find(state.changes[+li.getAttribute("data-change")].selector)[0];
    place(hoverBox, el || null);
  });
  panel.addEventListener("mouseleave", function () { place(hoverBox, null); });
  panel.addEventListener("keydown", function (e) {
    if (e.target.id === "ai" && e.key === "Enter" && (e.metaKey || e.ctrlKey) && state.aiPrompt.trim()) askAI();
  });
  function onAction(e) {
    var t = e.target.closest ? e.target.closest("button,li") : e.target;
    if (!t) return;
    var act = t.getAttribute("data-do"), del = t.getAttribute("data-del"), tab = t.getAttribute("data-tab"), part = t.getAttribute("data-part"),
      dev = t.getAttribute("data-dev"), ex = t.getAttribute("data-ex"), layer = t.getAttribute("data-layer");
    if (tab) { state.tab = tab; render(); return; }
    if (part) { state.part = part; render(); return; }
    if (dev !== null) { state.device = dev; render(); return; }
    if (ex) { state.aiPrompt = ex; render(); var box = root.querySelector("#ai"); if (box) box.focus(); return; }
    if (layer !== null) {
      var p = layers[+layer];
      if (p && p.el.isConnected) { p.el.scrollIntoView({ block: "center" }); select(p.el); }
      return;
    }
    if (del !== null) {
      var n = +del;
      edit(state.changes.filter(function (_, k) { return k !== n; }));
      place(hoverBox, null);
      render();
      return;
    }
    var el = state.selected, sel = el && selectorFor(el), v = function (id) { var nd = root.querySelector("#" + id); return nd ? nd.value : ""; };
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
      case "hide": add({ selector: sel, hide: true }); select(null); break;
      case "parent":
        if (el && el.parentElement && el.parentElement !== document.body && el.parentElement !== document.documentElement) select(el.parentElement);
        break;
      case "deselect": select(null); break;
      case "undo": undo(); break;
      case "redo": redo(); break;
      case "ai": askAI(); break;
      case "ai-stop":
        if (state.ai && state.ai.id) post("/v1/site/flags/edits/ai/stop", { token: token, run: state.ai.id }).then(function (run) { state.ai = run; clearTimeout(aiTimer); render(); }, function (err) { say(err.message, false); });
        break;
      case "ai-keep": state.aiKept = null; say("Kept. Save to show it to this version's visitors.", true); break;
      case "ai-undo": {
        var before = state.aiKept && state.aiKept.before;
        state.aiKept = null;
        if (before) edit(before);
        say("Undone.", true);
        break;
      }
      case "settings": {
        var css = v("css"), rd = v("rd").trim();
        if (!safeCSS(css)) { say("The CSS can change looks only: no url(), @import, fonts or <.", false); return; }
        if (rd && !safeURL(rd)) { say("The page to send visitors to starts with / or https://.", false); return; }
        state.css = css; state.redirect = rd; state.dirty = true; state.cssDirty = true;
        applyCSS();
        say(rd ? "Visitors in this version will go to " + rd + " once you save." : "Settings ready: save to use them.", true);
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
        try { sessionStorage.removeItem("of_edit"); sessionStorage.removeItem("of_edit_variant"); } catch (e2) {}
        host.remove();
        document.removeEventListener("mousemove", onMove, true);
        document.removeEventListener("click", onClick, true);
        document.removeEventListener("dblclick", onDouble, true);
        document.removeEventListener("keydown", onKey, true);
        break;
    }
  }
  panel.addEventListener("click", onAction);
  bar.addEventListener("click", onAction);

  function undo() {
    if (!state.undo.length) return;
    state.redo.push(state.changes);
    setChanges(state.undo.pop());
    state.aiKept = null;
    state.msg = "";
    render();
  }
  function redo() {
    if (!state.redo.length) return;
    state.undo.push(state.changes);
    setChanges(state.redo.pop());
    state.msg = "";
    render();
  }
  // switchTo opens another version of the flag: the page reloads showing it.
  function switchTo(v) {
    if (v === state.info.variant) return;
    if (state.dirty && !window.confirm("Switch version without saving your changes?")) { render(); return; }
    state.dirty = false;
    try { sessionStorage.setItem("of_edit_variant", JSON.stringify({ token: token, variant: v })); } catch (e) {}
    var u = new URL(location.href);
    u.searchParams.set("of_flag", state.info.flag_key + ":" + v);
    location.href = u.toString();
  }

  // Dragging the picked part by its handle: a line shows where it lands.
  var drag = null;
  handle.addEventListener("mousedown", function (e) {
    if (!state.selected) return;
    e.preventDefault();
    drag = { el: state.selected, to: null, where: "" };
    handle.style.cursor = "grabbing";
  });
  function dropAt(x, y) {
    host.style.display = "none";
    var t = document.elementFromPoint(x, y);
    host.style.display = "";
    if (!t || t === document.body || t === document.documentElement || inEditor(t) || t === drag.el || drag.el.contains(t) || t.contains(drag.el)) return null;
    // Land next to a part of the same size or bigger, not inside a word.
    while (t.parentElement && INLINE.test(t.tagName)) t = t.parentElement;
    var r = t.getBoundingClientRect();
    return { el: t, where: y < r.top + r.height / 2 ? "before" : "after", r: r };
  }

  function onMove(e) {
    if (drag) {
      var d = dropAt(e.clientX, e.clientY);
      drag.to = d;
      if (!d) { dropLine.style.display = "none"; return; }
      dropLine.style.display = "block";
      dropLine.style.left = d.r.left + "px"; dropLine.style.width = d.r.width + "px";
      dropLine.style.top = (d.where === "before" ? d.r.top - 2 : d.r.bottom - 1) + "px";
      return;
    }
    if (!state.picking || editing || inEditor(e.target)) { place(hoverBox, null); return; }
    place(hoverBox, e.target);
  }
  document.addEventListener("mouseup", function () {
    if (!drag) return;
    var d = drag.to, el = drag.el;
    drag = null;
    handle.style.cursor = "grab";
    dropLine.style.display = "none";
    if (d) { add({ selector: selectorFor(el), move: d.where, target: selectorFor(d.el) }); dropped = true; setTimeout(function () { dropped = false; }, 0); select(el); }
  }, true);
  // The click that ends a drag doesn't pick what was under it.
  var dropped = false;
  function onClick(e) {
    if (dropped && !inEditor(e.target)) { dropped = false; e.preventDefault(); e.stopPropagation(); return; }
    dropped = false;
    if (inEditor(e.target) || !state.info) return;
    if (editing) { if (editing.el.contains(e.target)) return; stopTyping(true); }
    e.preventDefault();
    e.stopPropagation();
    select(e.target);
  }
  function onDouble(e) {
    if (inEditor(e.target) || !state.info) return;
    e.preventDefault();
    e.stopPropagation();
    var el = e.target;
    while (el && !textOnly(el) && el.parentElement && INLINE.test(el.tagName)) el = el.parentElement;
    startTyping(el);
  }
  function onKey(e) {
    if (editing) {
      if (e.key === "Escape") { e.preventDefault(); stopTyping(false); }
      else if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); stopTyping(true); }
      return;
    }
    var path = e.composedPath ? e.composedPath() : [];
    var typing = path.some(function (n) { return n && /^(INPUT|TEXTAREA|SELECT)$/.test(n.tagName); });
    var mod = e.metaKey || e.ctrlKey;
    if (mod && e.key.toLowerCase() === "s") { e.preventDefault(); if (state.dirty) save(); return; }
    if (typing) return;
    if (mod && e.key.toLowerCase() === "z") { e.preventDefault(); if (e.shiftKey) redo(); else undo(); }
    else if (mod && e.key.toLowerCase() === "y") { e.preventDefault(); redo(); }
    else if (e.key === "Escape" && state.selected) select(null);
  }
  window.addEventListener("scroll", function () { place(selBox, state.selected); }, true);
  window.addEventListener("resize", function () { place(selBox, state.selected); });
  window.addEventListener("beforeunload", function (e) {
    if (state.dirty) { e.preventDefault(); e.returnValue = ""; }
  });

  // text/plain keeps the requests simple: no preflight.
  function answer(r) {
    return r.json().catch(function () { return {}; }).then(function (j) {
      if (!r.ok) { var err = new Error((j.error && j.error.message) || "Outflare answered " + r.status); err.code = j.error && j.error.code; throw err; }
      return j;
    });
  }
  function get(path) { return fetch(api + path, { credentials: "omit" }).then(answer); }
  function post(path, body) {
    return fetch(api + path, { method: "POST", body: JSON.stringify(body), headers: { "Content-Type": "text/plain" }, credentials: "omit" }).then(answer);
  }
  function load(j) {
    state.info = j;
    state.changes = j.changes || [];
    state.saved = state.changes;
    state.css = j.css || "";
    state.redirect = j.redirect || "";
    state.dirty = false;
    state.cssDirty = false;
  }
  function save() {
    state.busy = true;
    say("Saving…", true);
    post("/v1/site/flags/edits", { token: token, variant: state.info.variant, changes: state.changes, css: state.css, redirect: state.redirect }).then(function (j) {
      state.busy = false;
      // What's on the page stays as it is: only the saved copy changes.
      state.saved = j.changes || [];
      state.dirty = false;
      state.cssDirty = false;
      state.info.expires_at = j.expires_at;
      say("Saved. Visitors in " + (j.variant === state.info.control ? "today's page" : "version " + j.variant) + " see it within a minute." + (state.reload ? " Reload the page to see it as they will." : ""), true);
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
    document.addEventListener("dblclick", onDouble, true);
    document.addEventListener("keydown", onKey, true);
    get("/v1/site/flags/edits?token=" + encodeURIComponent(token) + (wanted ? "&variant=" + encodeURIComponent(wanted) : "")).then(function (j) {
      load(j);
      reapply(true);
      watchPage();
      applyCSS();
      render();
    }, function (err) {
      if (wanted) { try { sessionStorage.removeItem("of_edit_variant"); } catch (e) {} }
      say(err.message, false);
    });
  }
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", start);
  else start();
})();
