// Runs in Gmail. It reads who the open email is from, only when the side
// panel asks, and tells the panel when you open another email. Nothing is
// read from the email's text.
(() => {
  function me() {
    const m = document.title.match(/[\w.+-]+@[\w-]+(\.[\w-]+)+/);
    return m ? m[0].toLowerCase() : "";
  }

  // The newest sender in the open conversation who isn't you.
  function sender() {
    const mine = me();
    const seen = [...document.querySelectorAll('div[role="main"] span[email]')]
      .map((el) => ({ email: (el.getAttribute("email") || "").toLowerCase(), name: el.getAttribute("name") || el.textContent.trim() }))
      .filter((p) => /^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(p.email) && p.email !== mine);
    // span.gD marks a message's sender; fall back to anyone named.
    const from = [...document.querySelectorAll('div[role="main"] span.gD[email]')]
      .map((el) => (el.getAttribute("email") || "").toLowerCase())
      .filter((e) => e && e !== mine);
    const pick = from.length ? seen.find((p) => p.email === from[from.length - 1]) : null;
    return pick || null;
  }

  chrome.runtime.onMessage.addListener((msg, _from, reply) => {
    if (msg && msg.type === "outflare.sender") reply(sender());
  });

  // Tell the panel when the open email changes.
  let last = "";
  let timer;
  const tell = () => {
    const s = sender();
    const key = s ? s.email : "";
    if (key === last) return;
    last = key;
    chrome.runtime.sendMessage({ type: "outflare.sender_changed" }).catch(() => {});
  };
  new MutationObserver(() => {
    clearTimeout(timer);
    timer = setTimeout(tell, 400);
  }).observe(document.body, { childList: true, subtree: true });
  window.addEventListener("hashchange", () => setTimeout(tell, 400));
})();
