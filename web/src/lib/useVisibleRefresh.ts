import { useEffect } from "react";

/** Keep externally updated lists current without polling hidden pages or
 * letting an obsolete workspace/filter request overwrite the current one.
 * It loads at once, every `every` ms while the page is visible, and again
 * when the page is shown or the window focused. */
export function useVisibleRefresh(load: (signal?: AbortSignal) => Promise<void>, every = 2000) {
  useEffect(() => {
    const controller = new AbortController();
    let running = false;
    const refresh = async () => {
      if (document.visibilityState === "hidden" || running) return;
      running = true;
      try { await load(controller.signal); } finally { running = false; }
    };
    void refresh();
    const timer = window.setInterval(refresh, every);
    window.addEventListener("focus", refresh);
    document.addEventListener("visibilitychange", refresh);
    return () => {
      controller.abort();
      window.clearInterval(timer);
      window.removeEventListener("focus", refresh);
      document.removeEventListener("visibilitychange", refresh);
    };
  }, [load, every]);
}
