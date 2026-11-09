// The Outflare desktop app (docs/desktop-app.md): a Tauri shell whose
// window shows this web app. The shell puts a marker on the page and
// exposes a few commands; everything here does nothing in a browser, so
// the same build serves both.

type AppInfo = { version: string; platform: string };
type Invoke = (cmd: string, args?: Record<string, unknown>) => Promise<unknown>;
type Listen = (event: string, handler: (e: { payload: unknown }) => void) => Promise<() => void>;

/** Whether the shell has a newer version downloaded and waiting. */
export type UpdateState = { enabled: boolean; available: boolean; version?: string };

declare global {
  interface Window {
    __OUTFLARE_APP__?: AppInfo;
    __TAURI__?: { core?: { invoke?: Invoke }; event?: { listen?: Listen } };
  }
}

/** Whether this page runs inside the Outflare desktop app. */
export function isDesktopApp(): boolean {
  return typeof window !== "undefined" && !!window.__OUTFLARE_APP__;
}

/** The shell's version and platform, in the desktop app. */
export function desktopInfo(): AppInfo | undefined {
  return window.__OUTFLARE_APP__;
}

async function invoke<T>(cmd: string, args?: Record<string, unknown>): Promise<T> {
  const fn = window.__TAURI__?.core?.invoke;
  if (!isDesktopApp() || !fn) throw new Error("not in the Outflare desktop app");
  return (await fn(cmd, args)) as T;
}

export const desktop = {
  /** Starts signing in: the shell opens the system browser on the approve page. */
  startSignIn: () => invoke<void>("start_sign_in"),
  /** Opens an https link in the system browser. */
  openExternal: (url: string) => invoke<void>("open_external", { url }),
  /** A system notification; path is where a click should go, when the platform can. */
  notify: (title: string, body: string, path?: string) => invoke<void>("notify", { title, body, path }),
  /** The dock badge; null clears it. */
  setBadge: (count: number | null) => invoke<void>("set_badge", { count }),
  checkForUpdates: () => invoke<UpdateState>("check_for_updates"),
  /** Whether an update is downloaded and waiting for one click. */
  updateState: () => invoke<UpdateState>("update_state"),
  /** Installs the waiting update and reopens the app. Only returns on failure. */
  installUpdate: () => invoke<void>("install_update"),
};

// The shell downloads an update in the background and says so; the web
// app then shows the button. Returns a function that stops listening.
export function onUpdateReady(handler: (state: UpdateState) => void): () => void {
  const listen = window.__TAURI__?.event?.listen;
  if (!isDesktopApp() || !listen) return () => {};
  let stop: (() => void) | undefined;
  let stopped = false;
  listen("update-ready", (e) => {
    const p = (e.payload ?? {}) as { version?: string };
    handler({ enabled: true, available: true, version: p.version });
  }).then((unlisten) => {
    if (stopped) unlisten();
    else stop = unlisten;
  }).catch(() => {});
  return () => {
    stopped = true;
    stop?.();
  };
}

/** Whether a link leaves this site (another origin, or mail). */
export function isExternalHref(href: string): boolean {
  try {
    const u = new URL(href, location.href);
    if (u.protocol === "mailto:" || u.protocol === "tel:") return true;
    return (u.protocol === "http:" || u.protocol === "https:") && u.origin !== location.origin;
  } catch {
    return false;
  }
}

// In the desktop app a link that leaves Outflare opens in the person's
// browser, so the window never shows another site. Links on this origin
// behave as in a browser. The shell refuses such navigations anyway; this
// makes the click do the right thing the first time, and records it.
export function installExternalLinks(onOpened?: () => void) {
  if (!isDesktopApp()) return;
  document.addEventListener(
    "click",
    (e) => {
      if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey) return;
      const a = (e.target as Element | null)?.closest?.("a[href]") as HTMLAnchorElement | null;
      if (!a || !isExternalHref(a.href)) return;
      e.preventDefault();
      desktop.openExternal(a.href).catch(() => {});
      onOpened?.();
    },
    true,
  );
  const open = window.open.bind(window);
  window.open = (url?: string | URL, target?: string, features?: string) => {
    const href = url ? String(url) : "";
    if (href && isExternalHref(href)) {
      desktop.openExternal(href).catch(() => {});
      onOpened?.();
      return null;
    }
    return open(url, target, features);
  };
}
