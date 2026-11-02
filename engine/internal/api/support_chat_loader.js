// Outflare live chat: the launcher button and the chat's frame. The chat
// itself runs in the frame, on Outflare's address, so this page can't read
// the conversation and the chat can't read this page. A site that knows
// who is signed in sets window.OutflareChat = { identity: { email, name,
// signature } } before this script, with signature the hex HMAC-SHA256 of
// the email under the widget's identity secret (made on your server).
(function () {
  var cfg = /*CONFIG*/{};
  if (window.__outflareChat) return;
  window.__outflareChat = true;
  var origin = new URL(cfg.frame).origin;
  var open = false, frame, button;

  function identity() {
    var o = window.OutflareChat;
    return o && o.identity && typeof o.identity.email === "string" ? o.identity : null;
  }

  function style(el, css) {
    for (var k in css) el.style[k] = css[k];
  }

  function place() {
    var small = window.innerWidth < 480;
    style(frame, small
      ? { right: "0", bottom: "0", width: "100%", height: "100%", borderRadius: "0" }
      : { right: "20px", bottom: "88px", width: "380px", height: "min(620px, calc(100vh - 110px))", borderRadius: "14px" });
  }

  function toggle(to) {
    open = to === undefined ? !open : to;
    if (open && !frame) {
      frame = document.createElement("iframe");
      frame.src = cfg.frame;
      frame.title = cfg.title || "Chat";
      frame.setAttribute("allow", "clipboard-write");
      style(frame, { position: "fixed", zIndex: "2147483647", border: "0", background: "#fff", boxShadow: "0 12px 48px rgba(0,0,0,.18)", colorScheme: "normal" });
      frame.addEventListener("load", function () {
        var id = identity();
        if (id) frame.contentWindow.postMessage({ type: "outflare-chat:identity", identity: id }, origin);
      });
      document.body.appendChild(frame);
      place();
    }
    if (frame) frame.style.display = open ? "block" : "none";
    button.setAttribute("aria-expanded", String(open));
    button.innerHTML = open ? icons.close : icons.chat;
  }

  var icons = {
    chat: '<svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="#fff" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/></svg>',
    close: '<svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="#fff" stroke-width="2.2" stroke-linecap="round" aria-hidden="true"><path d="M18 6 6 18M6 6l12 12"/></svg>',
  };

  function start() {
    button = document.createElement("button");
    button.type = "button";
    button.setAttribute("aria-label", cfg.title || "Chat with us");
    style(button, { position: "fixed", right: "20px", bottom: "20px", width: "56px", height: "56px", borderRadius: "28px", border: "0", cursor: "pointer",
      background: cfg.color || "#111827", boxShadow: "0 6px 24px rgba(0,0,0,.22)", zIndex: "2147483647", display: "flex", alignItems: "center", justifyContent: "center", padding: "0" });
    button.innerHTML = icons.chat;
    button.addEventListener("click", function () { toggle(); });
    document.body.appendChild(button);
    window.addEventListener("resize", function () { if (frame) place(); });
    window.addEventListener("message", function (e) {
      if (e.origin !== origin || !frame || e.source !== frame.contentWindow) return;
      if (e.data && e.data.type === "outflare-chat:close") toggle(false);
    });
  }

  if (document.body) start();
  else document.addEventListener("DOMContentLoaded", start);
})();
