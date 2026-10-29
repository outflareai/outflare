import { useCallback, useEffect, useRef, useState } from "react";
import { cn } from "@/lib/utils";
import type { Workspace } from "@outflare/sdk";
import { api } from "./api";
import { identify, page, resetAnalytics } from "./lib/analytics";
import { appPath, isID, isItemSlug, itemKinds, itemOrPage, parsePath, rememberWorkspaces, type Tab } from "./lib/paths";
import { DialogHost, Loading, toast, ToastHost } from "./ui";
import { Icon, type IconName } from "./icons";
import { SidebarInset, SidebarProvider } from "@/components/ui/sidebar";
import { ApiPanel, canOpen, CommandPalette, everyday, isSettings, modules, resetNav, SettingsFrame, settingsGroups, shortcuts, Sidebar, startNav, tabNames, TopBar, useTheme, type Command, type MenuLink } from "./shell";
import { Setup } from "./pages/Setup";
import { Login } from "./pages/Login";
import { AcceptInvite } from "./pages/AcceptInvite";
import { ConfirmEmailBanner, ConfirmEmailFirst, ResetPassword, VerifyEmail } from "./pages/AccountEmail";
import { SupportViewBanner } from "./components/support-view-banner";
import { Members } from "./pages/Members";
import { Tokens } from "./pages/Tokens";
import { Webhooks } from "./pages/Webhooks";
import { Alerts } from "./pages/Alerts";
import { Digests } from "./pages/Digests";
import { AIUsage } from "./pages/AIUsage";
import { Experiments } from "./pages/Experiments";
import { Audit } from "./pages/Audit";
import { Logs } from "./pages/Logs";
import { Mailboxes } from "./pages/Mailboxes";
import { Leads } from "./pages/Leads";
import { Suppression } from "./pages/Suppression";
import { Lists } from "./pages/Lists";
import { Sequences } from "./pages/Sequences";
import { Inbox } from "./pages/Inbox";
import { Lifecycle, Providers } from "./pages/Lifecycle";
import { Emails } from "./pages/Emails";
import { Templates } from "./pages/EmailTemplates";
import { Topics } from "./pages/EmailTopics";
import { Broadcasts } from "./pages/EmailBroadcasts";
import { Journeys } from "./pages/Journeys";
import { Rules } from "./pages/Rules";
import { Overview } from "./pages/Overview";
import { GetStarted } from "./pages/GetStarted";
import { Domains } from "./pages/Domains";
import { Outcomes } from "./pages/Outcomes";
import { Projects } from "./pages/Projects";
import { Allocation } from "./pages/Allocation";
import { Senders } from "./pages/Senders";
import { Servers } from "./pages/Servers";
import { Connectors } from "./pages/Connectors";
import { Connect, pendingConnectToken } from "./pages/Connect";
import { Reports } from "./pages/Reports";
import { Analytics } from "./pages/Analytics";
import { WebsiteTracking } from "./pages/WebsiteTracking";
import { Flags } from "./pages/Flags";
import { ABTests } from "./pages/ABTests";
import { CampaignLinks } from "./pages/CampaignLinks";
import { Channels } from "./pages/Channels";
import { IdealCustomers } from "./pages/IdealCustomers";
import { LeadFinder } from "./pages/LeadFinder";
import { EmailFinder } from "./pages/EmailFinder";
import { CloudPage } from "./pages/CloudLocked";
import { cloudFeature } from "./lib/cloud";
import { Missions } from "./pages/Missions";
import { Playbooks } from "./pages/Playbooks";
import { Infrastructure } from "./pages/Infrastructure";
import { Instance } from "./pages/Instance";
import { Account } from "./pages/Account";
import { Company } from "./pages/Company";
import { Brands } from "./pages/Brands";
import { BrandSetup } from "./pages/BrandSetup";
import { WorkspaceSettings } from "./pages/WorkspaceSettings";
import { Tools } from "./pages/Tools";
import { SEO } from "./pages/SEO";
import { SiteAudits } from "./pages/SiteAudits";
import { Fixes } from "./pages/Fixes";
import { Keywords } from "./pages/Keywords";
import { Reddit } from "./pages/Reddit";
import { Social } from "./pages/Social";
import { Reviews } from "./pages/Reviews";
import { Ads } from "./pages/Ads";
import { Partners } from "./pages/Partners";
import { Visitors } from "./pages/Visitors";
import { SEOOverview } from "./pages/SEOOverview";
import { AIVisibility } from "./pages/AIVisibility";
import { Backlinks } from "./pages/Backlinks";
import { Competitors } from "./pages/Competitors";
import { LinkBuilding } from "./pages/LinkBuilding";
import { News } from "./pages/News";
import { Warmup } from "./pages/Warmup";
import { ConnectAI } from "./pages/ConnectAI";
import { Authorize } from "./pages/Authorize";
import { setBrand, useBrand } from "@/lib/brand";

type Me = { user: { id?: string; name?: string; email?: string; email_verified?: boolean; has_password?: boolean; email_resend_after?: number }; workspaces: Workspace[]; menu_links?: MenuLink[]; support_view?: { expires_at: string } | null };
type State =
  | { kind: "loading" }
  | { kind: "setup" }
  | { kind: "login" }
  | { kind: "invite"; token: string }
  | { kind: "connect"; token: string }
  | { kind: "reset"; token: string }
  | { kind: "verify"; token: string }
  | { kind: "confirm"; email: string; resendAfter: number }
  | { kind: "authorize"; me: Me }
  | { kind: "app"; me: Me };
export type { Tab };

const fromPath = () => parsePath(location.pathname);

export function App() {
  const [state, setState] = useState<State>({ kind: "loading" });

  const boot = useCallback(async () => {
    // Invite links carry the token in the fragment so it never reaches server logs.
    if (location.pathname === "/invite" && location.hash.length > 1) {
      return setState({ kind: "invite", token: location.hash.slice(1) });
    }
    // Connect request links open for signed-in members; the page asks to sign
    // in first. Google sign-in comes back without the fragment, so the token
    // waits in this tab meanwhile.
    if (location.pathname === "/connect") return setState({ kind: "connect", token: location.hash.slice(1) || pendingConnectToken() });
    // Links from account emails, which also work signed out.
    if (location.pathname === "/reset-password" && location.hash.length > 1) return setState({ kind: "reset", token: location.hash.slice(1) });
    if (location.pathname === "/verify-email" && location.hash.length > 1) return setState({ kind: "verify", token: location.hash.slice(1) });
    const setup = await api.GET("/v1/setup");
    if (setup.data?.needed) return setState({ kind: "setup" });
    const me = await api.GET("/v1/me");
    if (me.data) {
      const user = (me.data as Me).user;
      // Some servers (Outflare Cloud) open the app only once the address is confirmed.
      if (user.email_verified === false && user.email) {
        const meta = await api.GET("/v1/meta");
        if (meta.data?.capabilities?.includes("email_verification_required")) return setState({ kind: "confirm", email: user.email, resendAfter: user.email_resend_after ?? 0 });
      }
      // An AI app sent the person here to approve it (OAuth consent).
      if (location.pathname === "/oauth/authorize") return setState({ kind: "authorize", me: me.data as Me });
      return setState({ kind: "app", me: me.data as Me });
    }
    setState({ kind: "login" });
  }, []);

  useEffect(() => {
    boot();
  }, [boot]);

  // Signed-out screens count as pages too; the shell sends its own.
  useEffect(() => {
    if (state.kind !== "loading" && state.kind !== "app") page(state.kind);
  }, [state.kind]);

  switch (state.kind) {
    case "loading":
      return null;
    case "setup":
      return <Setup onDone={boot} />;
    case "login":
      return <Login onDone={boot} />;
    case "invite":
      return <AcceptInvite token={state.token} onDone={boot} />;
    case "reset":
      return (
        <>
          <ResetPassword token={state.token} onDone={boot} />
          <ToastHost />
        </>
      );
    case "verify":
      return <VerifyEmail token={state.token} onDone={boot} />;
    case "confirm":
      return (
        <>
          <ConfirmEmailFirst email={state.email} resendAfter={state.resendAfter} onDone={boot} />
          <ToastHost />
        </>
      );
    case "authorize":
      return (
        <>
          <Authorize email={state.me.user.email} onSignOut={async () => { await api.POST("/v1/auth/logout"); resetAnalytics(); boot(); }} />
          <DialogHost />
          <ToastHost />
        </>
      );
    case "connect":
      return (
        <>
          <Connect token={state.token} />
          <ToastHost />
        </>
      );
    case "app":
      return (
        <>
          <Shell me={state.me} onSignOut={boot} onReload={boot} />
          <DialogHost />
          <ToastHost />
        </>
      );
  }
}

// The OAuth consent screen returns here with the outcome in the query.
function takeOAuthOutcome() {
  const q = new URLSearchParams(location.search);
  const connected = q.get("connected"), failed = q.get("oauth_error"), ws = q.get("ws");
  if (connected || failed) history.replaceState(null, "", location.pathname);
  return { ws, notice: connected ? `Connected ${connected}. Checking its sign-in now.` : failed ? `Could not connect the mailbox: ${failed}` : "", failed: !!failed };
}

function Shell({ me, onSignOut, onReload }: { me: Me; onSignOut: () => void; onReload: () => void }) {
  const [outcome] = useState(takeOAuthOutcome);
  const [notice, setNotice] = useState(outcome.notice);
  // Something a link named that could not be opened, such as a workspace
  // this person is not in.
  const [lost, setLost] = useState("");
  rememberWorkspaces(me.workspaces);
  startNav(me.user.id ?? "");
  // A workspace from the URL: its slug, or its ID in older links.
  const known = (ref?: string | null) => (ref ? me.workspaces.find((w) => w.id === ref || w.slug === ref.toLowerCase())?.id : undefined);
  const [start] = useState(() => (outcome.ws ? { ws: outcome.ws, tab: "mailboxes" as Tab } : fromPath()));
  const [wsId, setWsId] = useState(known(start.ws) ?? me.workspaces[0]?.id ?? "");
  const [tab, setTabState] = useState<Tab>(start.tab ?? "overview");
  // item is what pages get: an item's ID, a domain name and so on.
  const [item, setItem] = useState<string | undefined>(isItemSlug(tab, start.item) ? undefined : start.item);
  // A workspace slug or item slug from the URL that is being looked up,
  // so the page waits instead of flashing something else first.
  const [findWs, setFindWs] = useState(start.ws && !known(start.ws) && me.workspaces.length ? start.ws : undefined);
  const [findItem, setFindItem] = useState(isItemSlug(tab, start.item) ? start.item : undefined);
  // Item links by workspace, kind and ID or slug: what each URL names.
  const [links, setLinks] = useState<Record<string, { id: string; slug: string; name?: string }>>({});
  const linkKey = (kind: string, ref: string) => `${wsId}:${kind}:${ref.toLowerCase()}`;
  const kind = itemKinds[tab];
  // Pages wait for the brands, so their first reads already carry the
  // brand picked.
  const brandState = useBrand(wsId || undefined);
  const pickedBrand = brandState.brand?.handle;
  const ready = !findWs && !findItem && (!wsId || brandState.loaded);
  const link = kind && item && isID(item) ? links[linkKey(kind, item)] : undefined;
  // Opens a tab, or one item in it (such as a domain).
  const setTab = useCallback((t: Tab, it?: string) => {
    setTabState(t);
    setItem(it);
    setFindItem(undefined);
  }, []);

  // A workspace slug that is not one of this person's current ones may be
  // an old slug of one of them.
  useEffect(() => {
    if (!findWs) return;
    api.GET("/v1/workspaces/{ws}", { params: { path: { ws: findWs } } }).then(({ data }) => {
      const id = known(data?.id);
      if (id) setWsId(id);
      else setLost(`No workspace you can open is at /${findWs}. Showing ${me.workspaces[0].name} instead.`);
      setFindWs(undefined);
    });
  }, [findWs]); // eslint-disable-line react-hooks/exhaustive-deps

  // An item named by slug in the URL: find its ID.
  useEffect(() => {
    if (findWs || !findItem || !kind || !wsId) return;
    let live = true;
    api.GET("/v1/workspaces/{ws}/links/{kind}/{ref}", { params: { path: { ws: wsId, kind, ref: findItem } } }).then(({ data }) => {
      if (!live) return;
      if (data) {
        setLinks((l) => ({ ...l, [linkKey(kind, findItem)]: data, [linkKey(kind, data.id)]: data }));
        setItem(data.id);
      } else if (itemOrPage[tab]) {
        setItem(findItem);
      } else {
        setLost(`Nothing in ${tabNames[tab]} is at that link. It may have been deleted.`);
        setItem(undefined);
      }
      setFindItem(undefined);
    });
    return () => {
      live = false;
    };
  }, [findWs, findItem, kind, wsId]); // eslint-disable-line react-hooks/exhaustive-deps

  // An item opened by ID: find its slug for the URL.
  useEffect(() => {
    if (!kind || !item || !isID(item) || link) return;
    let live = true;
    api.GET("/v1/workspaces/{ws}/links/{kind}/{ref}", { params: { path: { ws: wsId, kind, ref: item } } }).then(({ data }) => {
      if (live) setLinks((l) => ({ ...l, [linkKey(kind, item)]: data ?? { id: item, slug: item } }));
    });
    return () => {
      live = false;
    };
  }, [kind, item, link, wsId]); // eslint-disable-line react-hooks/exhaustive-deps
  const [unread, setUnread] = useState(0);
  const ws = me.workspaces.find((w) => w.id === wsId);
  // What the person may do here, from their role. The server checks every
  // request; this only hides what would be refused.
  const scopes: string[] = ws?.scopes ?? [];
  const can = (scope: string) => scopes.includes(scope);
  const isAdmin = can("workspace.manage");
  const writer = scopes.some((sc) => !sc.endsWith(".read"));

  useEffect(() => {
    identify(me.user, ws && { id: ws.id, name: ws.name });
  }, [me.user, ws?.id, ws?.name]);

  useEffect(() => {
    page(tab);
  }, [tab, wsId]);

  useEffect(() => {
    if (ws && ((tab === "tokens" && !can("tokens.manage")) || ((tab === "audit" || tab === "logs") && !can("audit.read")))) setTab("overview");
    // Someone kept to some brands only sees the pages that work for them.
    if (ws && !canOpen(ws, tab)) setTab("overview");
  }, [tab, ws, isAdmin, setTab]);

  // A new workspace opens on Get started for its owners and admins, once
  // per browser session, until something is done, skipped or it is closed.
  const [openedFrom, setOpenedFrom] = useState("link");
  useEffect(() => {
    if (!ws || !isAdmin || tab !== "overview") return;
    const key = `get-started-shown:${ws.id}`;
    try {
      if (sessionStorage.getItem(key)) return;
      sessionStorage.setItem(key, "1");
    } catch {
      // Not remembered; it may open again, which is fine.
    }
    let live = true;
    api.GET("/v1/workspaces/{ws}/onboarding", { params: { path: { ws: ws.id } } }).then(({ data }) => {
      if (live && data?.show) {
        setOpenedFrom("auto");
        setTab("get-started");
      }
    });
    return () => {
      live = false;
    };
  }, [ws?.id, isAdmin]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (!ws || findWs || findItem || !brandState.loaded) return;
    // Wait for an item's slug rather than show its ID first.
    if (kind && item && isID(item) && !link) return;
    const path = appPath(ws.id, tab, link?.slug ?? item);
    // An old link (/w/{id}, an item's ID or old slug) or a slug that just
    // changed is replaced rather than added to history, so Back skips it.
    const here = fromPath();
    const named = known(here.ws);
    const sameItem = here.tab === tab && !!here.item && !!item && !!kind && (here.item === item || links[linkKey(kind, here.item)]?.id === item);
    const stale = here.legacy || sameItem || (here.ws !== undefined && here.ws !== ws.slug && (named === undefined || named === ws.id));
    // The brand picked is part of the path (appPath), so switching it, or
    // an old link's ?brand=, replaces the URL rather than adding to history.
    const there = parsePath(path);
    const otherBrand = here.ws === there.ws && here.tab === there.tab && here.item === there.item;
    // An item open when the brand is switched closes (below), and its URL
    // keeps the old brand, so Back returns to it on that brand.
    if (otherBrand && here.item && here.brand && there.brand && here.brand !== there.brand) return;
    // An old link's ?brand= is in the path now.
    const query = new URLSearchParams(location.search);
    query.delete("brand");
    const search = query.toString() ? `?${query}` : "";
    if (location.pathname !== path || location.search !== search) history[stale || otherBrand ? "replaceState" : "pushState"](null, "", path + search);
    document.title = `${link?.name ?? (kind ? undefined : item) ?? tabNames[tab]} · ${ws.name} · Outflare`;
  }, [ws?.id, ws?.slug, ws?.name, tab, item, link, findWs, findItem, pickedBrand, brandState.loaded, brandState.active.length]); // eslint-disable-line react-hooks/exhaustive-deps

  // Switching brand shows the new brand's records everywhere: the page
  // reloads (keyed by brand below), and an item open from the old brand
  // closes back to its list.
  const lastBrand = useRef<string | undefined>(undefined);
  useEffect(() => {
    if (!brandState.loaded) return;
    const was = lastBrand.current;
    lastBrand.current = pickedBrand ?? "";
    if (was !== undefined && was !== lastBrand.current && item) setTab(tab);
  }, [pickedBrand, brandState.loaded]); // eslint-disable-line react-hooks/exhaustive-deps

  // Unread replies show on the Inbox tab, for the brand picked in the
  // sidebar, refreshed every 30 seconds.
  useEffect(() => {
    if (!wsId) return;
    const load = () => api.GET("/v1/workspaces/{ws}/stats", { params: { path: { ws: wsId }, query: pickedBrand ? { brand: pickedBrand } : {} } }).then(({ data }) => data && setUnread(data.unread_replies));
    load();
    const t = setInterval(load, 30000);
    return () => clearInterval(t);
  }, [wsId, tab, pickedBrand]);

  useEffect(() => {
    const back = () => {
      const p = fromPath();
      const id = known(p.ws);
      if (id) setWsId(id);
      if (p.brand && (!id || id === wsId)) setBrand(p.brand, "link");
      const t = p.tab ?? tab;
      if (p.tab) setTabState(p.tab);
      const k = itemKinds[t];
      if (isItemSlug(t, p.item) && k) {
        const found = links[`${id ?? wsId}:${k}:${p.item!.toLowerCase()}`];
        setItem(found?.id);
        setFindItem(found ? undefined : p.item);
      } else {
        setItem(p.item);
        setFindItem(undefined);
      }
    };
    addEventListener("popstate", back);
    return () => removeEventListener("popstate", back);
  });

  async function signOut() {
    await api.POST("/v1/auth/logout");
    resetAnalytics();
    resetNav();
    onSignOut();
  }

  const [theme, toggleTheme] = useTheme();
  const [palette, setPalette] = useState(false);
  const [apiOpen, setApiOpen] = useState(() => {
    try {
      return localStorage.getItem("api-panel") === "open";
    } catch {
      return false;
    }
  });
  useEffect(() => {
    try {
      localStorage.setItem("api-panel", apiOpen ? "open" : "closed");
    } catch {
      // Not remembered; fine.
    }
  }, [apiOpen]);

  const go = useCallback((t: Tab) => setTab(t), [setTab]);

  // ⌘K or Ctrl+K opens the palette; "g" then a letter opens a page.
  useEffect(() => {
    let pendingG = 0;
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setPalette((o) => !o);
        return;
      }
      const el = e.target as HTMLElement;
      if (e.metaKey || e.ctrlKey || e.altKey || el.closest("input, textarea, select, [contenteditable], [role=dialog], [role=alertdialog], [role=menu]")) return;
      if (e.key === "g") {
        pendingG = Date.now();
        return;
      }
      if (Date.now() - pendingG < 1200) {
        const t = (Object.entries(shortcuts) as [Tab, string][]).find(([, k]) => k === e.key)?.[0];
        pendingG = 0;
        if (t) go(t);
      }
    };
    addEventListener("keydown", onKey);
    return () => removeEventListener("keydown", onKey);
  }, [go]);

  const settings = settingsGroups(isAdmin, ws);
  const goTo = (t: Tab, label = tabNames[t], group = "Go to"): Command => ({ id: `go-${t}`, label, group, icon: t as IconName, hint: shortcuts[t] ? `G ${shortcuts[t]!.toUpperCase()}` : undefined, run: () => go(t) });
  const commands: Command[] = [
    ...everyday.filter((t) => canOpen(ws, t)).map((t) => goTo(t)),
    ...(canOpen(ws, "ai") ? [goTo("ai", "Connect Claude, ChatGPT or another AI app")] : []),
    ...(isAdmin ? [{ ...goTo("get-started", "Get started: set up the workspace", "Workspace"), run: () => { setOpenedFrom("palette"); go("get-started"); } }] : []),
    ...modules.flatMap((m) => m.tabs.filter((t) => canOpen(ws, t)).map((t) => goTo(t, tabNames[t], m.label))),
    ...(["tools", "connectors"] as const).filter((t) => canOpen(ws, t)).map((t) => goTo(t)),
    ...settings.flatMap((g) => g.tabs.map((t) => goTo(t, `${tabNames[t]}`, "Settings"))),
    { id: "api", label: apiOpen ? "Hide the API panel" : "Show the API call for this page", group: "Developer", icon: "code", run: () => setApiOpen((o) => !o) },
    ...(ws ? [{ id: "copy-url", label: "Copy link to this page", group: "Workspace", icon: "copy" as IconName, run: () => { navigator.clipboard?.writeText(location.href); toast("Link copied."); } }] : []),
    { id: "copy-ws", label: "Copy workspace ID", group: "Developer", icon: "copy", hint: wsId.slice(0, 8), run: () => { navigator.clipboard?.writeText(wsId); toast("Workspace ID copied."); } },
    { id: "spec", label: "Open the OpenAPI spec", group: "Developer", icon: "book", run: () => open("/v1/openapi.yaml", "_blank") },
    ...(can("integrations.manage") ? [{ id: "connect", label: "Connect a service", group: "Settings", icon: "connectors" as IconName, run: () => go("connectors") }] : []),
    ...(ws && writer ? [{ id: "new-email-template", label: "Create an email template", group: "Lifecycle", icon: "templates" as IconName, run: () => setTab("templates", "new") }] : []),
    ...(can("tokens.manage") ? [{ id: "token", label: "Create an API token", group: "Developer", icon: "tokens" as IconName, run: () => go("tokens") }] : []),
    ...me.workspaces.filter((w) => w.id !== wsId).map((w) => ({ id: `ws-${w.id}`, label: `Switch to ${w.name}`, group: "Workspaces", icon: "overview" as IconName, run: () => { setWsId(w.id); setItem(undefined); } })),
    { id: "theme", label: theme === "dark" ? "Switch to light theme" : "Switch to dark theme", group: "Preferences", icon: theme === "dark" ? "sun" : "moon", run: toggleTheme },
    { id: "signout", label: "Sign out", group: "Account", icon: "logout", run: signOut },
  ];

  // Get started is a page of its own: nothing else on screen while a new
  // workspace is walked through it.
  if (ready && ws && tab === "get-started" && isAdmin) {
    return <GetStarted key={ws.id} ws={ws.id} wsName={ws.name} from={openedFrom} go={setTab} />;
  }

  return (
    <SidebarProvider style={{ "--sidebar-width": "15rem" } as React.CSSProperties}>
      <a href="#main" className="sr-only focus:not-sr-only focus:fixed focus:left-3 focus:top-3 focus:z-50 focus:rounded-md focus:bg-background focus:px-3 focus:py-2 focus:text-sm focus:shadow-md focus:ring-2 focus:ring-ring">Skip to content</a>
      <Sidebar
        me={me.user}
        links={me.menu_links}
        workspaces={me.workspaces}
        ws={ws}
        onWorkspace={(id) => { setWsId(id); setItem(undefined); }}
        tab={tab}
        onTab={go}
        unread={unread}
        theme={theme}
        onTheme={toggleTheme}
        onSearch={() => setPalette(true)}
        onSignOut={signOut}
      />
      <SidebarInset id="main" tabIndex={-1} className={cn("min-w-0 outline-none", apiOpen && "xl:pr-[26rem]")}>
        <TopBar ws={ws} tab={tab} item={item} itemName={link?.name} onTab={go} onApi={() => setApiOpen((o) => !o)} apiOpen={apiOpen} />
        {/* Every page uses the whole width beside the sidebar: tables and
            charts stretch, and forms and text cap themselves at a reading width. */}
        <div key={pickedBrand ?? "all"} className="w-full px-4 py-6 sm:px-6 lg:px-8">
        {me.support_view && <SupportViewBanner name={me.user.name || me.user.email || "this person"} />}
        {!me.support_view && me.user.email_verified === false && me.user.email && <ConfirmEmailBanner email={me.user.email} resendAfter={me.user.email_resend_after ?? 0} />}
        {ws?.test && (
          <div role="status" className="mb-6 rounded-lg border border-sky-600/30 bg-sky-500/10 px-3 py-2 text-[13px] text-sky-900 dark:border-sky-400/20 dark:text-sky-200">
            <strong className="font-medium">Test workspace.</strong> Nothing here reaches anyone: mailboxes are simulated and nothing is delivered or bought.
          </div>
        )}
        {lost && (
          <div role="status" className="mb-6 flex items-center justify-between gap-3 rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-[13px] text-amber-800 dark:text-amber-200">
            <span>{lost}</span>
            <button aria-label="Dismiss" className="-m-2 p-2" onClick={() => setLost("")}><Icon name="x" className="size-3.5" /></button>
          </div>
        )}
        {notice && (
          <div className={`mb-6 flex items-center justify-between gap-3 rounded-lg border px-3 py-2 text-[13px] ${outcome.failed ? "border-red-500/30 bg-red-500/10 text-red-700 dark:text-red-300" : "border-green-500/30 bg-green-500/10 text-green-700 dark:text-green-300"}`}>
            <span>{notice}</span>
            <button aria-label="Dismiss" onClick={() => setNotice("")}><Icon name="x" className="size-3.5" /></button>
          </div>
        )}
        {!ready && <Loading />}
        {ready && ws && tab === "overview" && <Overview key={ws.id} ws={ws.id} name={ws.name} isAdmin={isAdmin} go={(t) => { if (t === "get-started") setOpenedFrom("overview"); setTab(t); }} limited={(ws.brand_ids?.length ?? 0) > 0} />}
        {ready && ws && tab === "setup" && <BrandSetup key={ws.id} ws={ws.id} canManage={isAdmin} go={setTab} />}
        {ready && ws && tab === "get-started" && !isAdmin && <p className="text-sm text-muted-foreground">An owner or admin of {ws.name} sets this up.</p>}
        {ready && ws && tab === "flags" && <Flags key={ws.id} ws={ws.id} canManage={can("analytics.manage")} go={setTab} />}
        {ready && ws && tab === "ab-tests" && <ABTests key={ws.id} ws={ws.id} canManage={can("analytics.manage")} go={setTab} />}
        {ready && ws && tab === "channels" && <Channels key={ws.id} ws={ws.id} canManage={can("analytics.manage")} go={setTab} />}
        {ready && ws && tab === "links" && <CampaignLinks key={ws.id} ws={ws.id} canManage={writer} />}
        {ready && ws && tab === "tracking" && <WebsiteTracking key={ws.id} ws={ws.id} canManage={isAdmin} go={setTab} />}
        {ready && ws && tab === "ideal-customers" && <IdealCustomers key={ws.id} ws={ws.id} canManage={writer} />}
        {ready && ws && tab === "experiments" && <Experiments key={ws.id} ws={ws.id} canManage={writer} go={setTab} />}
        {ready && ws && tab === "seo-overview" && <SEOOverview key={ws.id} ws={ws.id} go={setTab} />}
        {ready && ws && tab === "audits" && <SiteAudits key={ws.id} ws={ws.id} canManage={writer} open={item ?? ""} onOpen={(s) => setItem(s || undefined)} go={setTab} />}
        {ready && ws && tab === "keywords" && <Keywords key={ws.id} ws={ws.id} go={setTab} />}
        {ready && ws && tab === "ai-visibility" && <AIVisibility key={ws.id} ws={ws.id} go={setTab} />}
        {ready && ws && tab === "fixes" && <Fixes key={ws.id} ws={ws.id} canManage={writer} go={setTab} />}
        {ready && ws && tab === "backlinks" && <Backlinks key={ws.id} ws={ws.id} go={setTab} />}
        {ready && ws && tab === "competitors" && <Competitors key={ws.id} ws={ws.id} go={setTab} />}
        {ready && ws && tab === "social" && <Social key={ws.id} ws={ws.id} canManage={writer} go={setTab} />}
        {ready && ws && tab === "reviews" && <Reviews key={ws.id} ws={ws.id} canManage={writer} go={setTab} />}
        {ready && ws && tab === "ads" && <Ads key={ws.id} ws={ws.id} canManage={writer} go={setTab} />}
        {ready && ws && tab === "partners" && <Partners key={ws.id} ws={ws.id} canManage={writer} />}
        {ready && ws && tab === "visitors" && <Visitors key={ws.id} ws={ws.id} canManage={writer} go={setTab} />}
        {ready && ws && tab === "reddit" && <Reddit key={ws.id} ws={ws.id} canManage={writer} go={setTab} />}
        {ready && ws && tab === "seo" && <SEO key={ws.id} ws={ws.id} canManage={isAdmin} go={setTab} />}
        {ready && ws && tab === "warmup" && <Warmup key={ws.id} ws={ws.id} canManage={writer} go={setTab} />}
        {ready && ws && tab === "reports" && <Reports key={ws.id} ws={ws.id} />}
        {ready && ws && tab === "analytics" && <Analytics key={ws.id} ws={ws.id} canManage={isAdmin} go={setTab} />}
        {ready && ws && tab === "lifecycle" && <Lifecycle key={ws.id} ws={ws.id} canManage={isAdmin} canSend={writer} myEmail={me.user.email ?? ""} go={setTab} />}
        {ready && ws && tab === "emails" && <Emails key={ws.id} ws={ws.id} canSend={writer} admin={isAdmin} open={item ?? ""} onOpen={(id) => setItem(id || undefined)} go={setTab} />}
        {ready && ws && tab === "templates" && <Templates key={ws.id} ws={ws.id} canManage={writer} canSend={writer} myEmail={me.user.email ?? ""} open={item ?? ""} onOpen={(id) => setItem(id || undefined)} go={setTab} />}
        {ready && ws && tab === "broadcasts" && <Broadcasts key={ws.id} ws={ws.id} canManage={writer} open={item ?? ""} onOpen={(id) => setItem(id || undefined)} go={setTab} />}
        {ready && ws && tab === "journeys" && <Journeys key={ws.id} ws={ws.id} canManage={writer} open={item ?? ""} onOpen={(id) => setItem(id || undefined)} go={setTab} />}
        {ready && ws && tab === "rules" && <Rules key={ws.id} ws={ws.id} canManage={writer} open={item ?? ""} onOpen={(id) => setItem(id || undefined)} go={setTab} />}
        {ready && ws && tab === "topics" && <Topics key={ws.id} ws={ws.id} canManage={writer} go={setTab} />}
        {ready && ws && tab === "providers" && <Providers key={ws.id} ws={ws.id} canManage={isAdmin} open={item ?? ""} onOpen={(id) => setItem(id || undefined)} go={setTab} />}
        {ready && ws && tab === "inbox" && <Inbox key={ws.id} ws={ws.id} canManage={writer} open={item} setOpen={setItem} onRead={() => setUnread((n) => Math.max(0, n - 1))} />}
        {ready && ws && tab === "playbooks" && <Playbooks key={ws.id} ws={ws.id} canManage={writer} go={setTab} />}
        {ready && ws && tab === "missions" && <Missions key={ws.id} ws={ws.id} canManage={writer} open={item} setOpen={setItem} />}
        {ready && ws && tab === "outcomes" && <Outcomes key={ws.id} ws={ws.id} canManage={writer} go={setTab} />}
        {ready && ws && tab === "projects" && <Projects key={ws.id} ws={ws.id} canManage={writer} isAdmin={isAdmin} open={item} setOpen={setItem} go={setTab} />}
        {ready && ws && tab === "sequences" && <Sequences key={ws.id} ws={ws.id} canManage={writer} open={item} setOpen={setItem} go={setTab} />}
        {ready && ws && tab === "infrastructure" && <Infrastructure key={ws.id} ws={ws.id} canManage={writer} canApprove={isAdmin} open={item ?? ""} onOpen={(it) => setItem(it || undefined)} go={setTab} />}
        {ready && ws && tab === "mailboxes" && <Mailboxes key={ws.id} ws={ws.id} canManage={writer} openOAuth={!!outcome.ws} go={setTab} />}
        {ready && ws && tab === "domains" && <Domains key={ws.id} ws={ws.id} canManage={writer} open={item ?? ""} onOpen={(d) => setItem(d || undefined)} go={setTab} />}
        {ready && ws && tab === "link-building" && <LinkBuilding key={ws.id} ws={ws.id} canManage={can("seo.manage")} go={setTab} />}
        {ready && ws && tab === "news" && <News key={ws.id} ws={ws.id} canManage={can("seo.manage")} canLists={can("customers.manage")} go={setTab} />}
        {ready && ws && tab === "allocation" && <Allocation key={ws.id} ws={ws.id} canManage={writer} canSetMode={isAdmin} go={setTab} />}
        {ready && ws && tab === "senders" && <Senders key={ws.id} ws={ws.id} canManage={writer} canManageTeams={isAdmin} />}
        {ready && ws && tab === "servers" && <Servers key={ws.id} ws={ws.id} canManage={writer} open={item ?? ""} onOpen={(id) => setItem(id || undefined)} go={setTab} />}
        {ready && ws && tab === "leads" && <Leads key={ws.id} ws={ws.id} canManage={writer} go={setTab} />}
        {ready && ws && tab === "lists" && <Lists key={ws.id} ws={ws.id} canManage={writer} open={item} setOpen={setItem} go={setTab} />}
        {ready && ws && tab === "lead-finder" && (
          <CloudPage feature={cloudFeature("lead-finder")!}>
            <LeadFinder key={ws.id} ws={ws.id} canManage={writer} onConnectors={() => setTab("connectors")} onProfiles={() => setTab("ideal-customers")} />
          </CloudPage>
        )}
        {ready && ws && tab === "email-finder" && <EmailFinder key={ws.id} ws={ws.id} canManage={writer} />}
        {ready && ws && tab === "suppression" && <Suppression key={ws.id} ws={ws.id} canAdd={writer} canRemove={isAdmin} />}
        {ready && ws && tab === "connectors" && <Connectors key={ws.id} ws={ws.id} canManage={isAdmin} canVerify={writer} canImport={writer} open={item} setOpen={setItem} go={setTab} />}
        {ready && ws && tab === "company" && <Company key={ws.id} ws={ws.id} canManage={writer} />}
        {ready && ws && tab === "brands" && <Brands key={ws.id} ws={ws.id} canManage={writer} canEdit={writer} open={item} setOpen={setItem} go={setTab} />}
        {isSettings(tab) && (
          <SettingsFrame groups={settings} tab={tab} onTab={go}>
            {ready && ws && tab === "workspace" && <WorkspaceSettings key={ws.id} ws={ws} canManage={isAdmin} onSaved={onReload} />}
            {ready && ws && tab === "members" && <Members key={ws.id} ws={ws.id} myId={me.user.id ?? ""} myScopes={scopes} myRole={ws.role} />}
            {ready && ws && tab === "alerts" && <Alerts key={ws.id} ws={ws.id} canManage={isAdmin} />}
            {ready && ws && tab === "digests" && <Digests key={ws.id} ws={ws.id} canManage={isAdmin} go={setTab} />}
            {ready && ws && tab === "ai-usage" && <AIUsage key={ws.id} ws={ws.id} canManage={isAdmin} go={setTab} />}
            {ready && ws && tab === "webhooks" && <Webhooks key={ws.id} ws={ws.id} canManage={isAdmin} open={item ?? ""} onOpen={(id) => setItem(id || undefined)} />}
            {ready && ws && tab === "tokens" && can("tokens.manage") && <Tokens key={ws.id} ws={ws.id} open={item} setOpen={setItem} go={setTab} />}
            {ready && ws && tab === "audit" && can("audit.read") && <Audit key={ws.id} ws={ws.id} />}
            {ready && ws && tab === "ai" && <ConnectAI key={ws.id} ws={ws.id} canSeeAll={isAdmin} go={setTab} open={item} setOpen={setItem} />}
            {ready && ws && tab === "logs" && can("audit.read") && <Logs key={ws.id} ws={ws.id} canManage={isAdmin} />}
            {ready && ws && tab === "tools" && <Tools go={setTab} />}
            {tab === "instance" && <Instance />}
            {tab === "account" && <Account onChanged={onReload} />}
          </SettingsFrame>
        )}
        </div>
      </SidebarInset>
      {apiOpen && wsId && (
        <div className="fixed inset-y-0 right-0 z-30 w-full max-w-[26rem] shadow-2xl xl:shadow-none">
          <ApiPanel ws={wsId} tab={tab} item={item} onClose={() => setApiOpen(false)} />
        </div>
      )}
      <CommandPalette open={palette} onClose={() => setPalette(false)} commands={commands} />
    </SidebarProvider>
  );
}
