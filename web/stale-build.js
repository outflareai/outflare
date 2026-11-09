// Inlined into each HTML entry: after a deploy, a page of the previous build reloads once when its scripts are gone.
(() => {
  const key = "outflare:stale-build-reload";
  const reload = (reason) => {
    try {
      const last = JSON.parse(sessionStorage.getItem(key) || "null");
      if (last && Date.now() - last.at < 10 * 60 * 1000) return;
      sessionStorage.setItem(key, JSON.stringify({ at: Date.now(), reason }));
    } catch {
      return;
    }
    location.reload();
  };
  addEventListener(
    "error",
    (e) => {
      const src = e.target instanceof HTMLScriptElement && e.target.src ? new URL(e.target.src) : undefined;
      if (src && src.origin === location.origin && src.pathname.startsWith("/assets/")) reload("entry");
    },
    true,
  );
  addEventListener("vite:preloadError", () => reload("chunk"));
})();
