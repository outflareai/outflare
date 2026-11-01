import { useEffect } from "react";

/** Keep externally updated lists current without polling hidden pages or
 * letting an obsolete workspace/filter request overwrite the current one. */
export function useVisibleRefresh(load: (signal?: AbortSignal) => Promise<void>) {
  useEffect(() => {
    const controller = new AbortController();
    let running = false;
    const refresh = async () => {
      if (document.visibilityState === "hidden" || running) return;
      running = true;
      try { await load(controller.signal); } finally { running = false; }
    };
    void refresh();
    const timer = window.setInterval(refresh, 2000);
    window.addEventListener("focus", refresh);
    document.addEventListener("visibilitychange", refresh);
    return () => {
      controller.abort();
      window.clearInterval(timer);
      window.removeEventListener("focus", refresh);
      document.removeEventListener("visibilitychange", refresh);
    };
  }, [load]);
}
