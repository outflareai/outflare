import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import type { AuditEvent, Job, LogEntry } from "@outflare/sdk";
import { ChevronDown, ChevronUp, Link2, Pause, Play, RotateCw, Search, X } from "lucide-react";
import { api, errorMessage } from "../api";
import { Badge, Button, CopyButton, Empty, ErrorText, field, Loading, Menu, PageHeader, toast } from "../ui";
import { ago, stamp } from "../format";
import { cn } from "@/lib/utils";
import { track } from "@/lib/analytics";
import { useBrand } from "@/lib/brand";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { HookEvents } from "@/components/live-updates";

// The Logs page: every API call in the workspace, like a hosting
// platform's request logs. Filters live in the URL, so a link opens the
// same view; Live follows new calls every two seconds.

type Filters = { q: string; level: string[]; surface: string; status: string; since: string; until: string; operation: string; error_code: string; mcp_tool: string; mission_id: string; actor_id: string };
const empty: Filters = { q: "", level: [], surface: "", status: "", since: "24h", until: "", operation: "", error_code: "", mcp_tool: "", mission_id: "", actor_id: "" };
const keys = Object.keys(empty) as (keyof Filters)[];

const ranges: [string, string][] = [["15m", "Last 15 minutes"], ["1h", "Last hour"], ["24h", "Last 24 hours"], ["7d", "Last 7 days"], ["30d", "Last 30 days"]];
const surfaces: [string, string][] = [["", "Every source"], ["web", "Web app"], ["api", "API"], ["mcp", "MCP (agents)"], ["mission", "Missions"], ["cli", "CLI"], ["sdk", "SDK"]];
const statuses: [string, string][] = [["", "Any status"], ["2xx", "2xx success"], ["3xx", "3xx"], ["4xx", "4xx refused"], ["5xx", "5xx error"], ["401,403", "401/403 access"], ["404", "404 not found"], ["429", "429 rate limited"]];
const levels = [
  { id: "info", label: "Info", dot: "bg-muted-foreground/60" },
  { id: "warning", label: "Warning", dot: "bg-amber-500" },
  { id: "error", label: "Error", dot: "bg-red-500" },
] as const;

type View = "requests" | "jobs" | "incoming";

function readURL(): { f: Filters; open?: string; view: View } {
  const p = new URLSearchParams(location.search);
  const f = { ...empty };
  for (const k of keys) {
    const v = p.get(k);
    if (v === null) continue;
    if (k === "level") f.level = v.split(",").filter(Boolean);
    else f[k] = v;
  }
  return { f, open: p.get("request") ?? undefined, view: p.get("view") === "jobs" ? "jobs" : p.get("view") === "incoming" ? "incoming" : "requests" };
}

function writeURL(f: Filters, open: string | undefined, view: string, brand?: string) {
  const p = new URLSearchParams();
  if (brand) p.set("brand", brand);
  if (view !== "requests") p.set("view", view);
  for (const k of keys) {
    const v = k === "level" ? f.level.join(",") : f[k];
    if (v && v !== empty[k]) p.set(k, v);
  }
  if (open) p.set("request", open);
  const qs = p.toString();
  const url = location.pathname + (qs ? `?${qs}` : "");
  if (location.pathname + location.search !== url) history.replaceState(history.state, "", url);
}

// The API query for the filters.
function query(f: Filters, brand: { brand?: string }) {
  const q: Record<string, string> = { ...brand };
  for (const k of keys) {
    const v = k === "level" ? f.level.join(",") : f[k].trim();
    if (v) q[k] = v;
  }
  return q;
}

const statusTone = (s: number) => (s >= 500 ? "red" : s >= 400 ? "amber" : s >= 300 ? "blue" : "green");
const surfaceName = (s: string) => surfaces.find(([id]) => id === s)?.[1] ?? s;
const ms = (n: number) => (n >= 1000 ? `${(n / 1000).toFixed(n >= 10000 ? 0 : 1)}s` : `${n}ms`);
// The time of a call, with the day when it wasn't today.
const clock = (iso: string, seconds = true) => {
  const d = new Date(iso);
  const time = d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", ...(seconds && { second: "2-digit" }) });
  return d.toDateString() === new Date().toDateString() ? time : `${d.toLocaleDateString(undefined, { day: "numeric", month: "short" })} ${time}`;
};
const isPreset = (since: string) => ranges.some(([v]) => v === since);
// A range picked on the chart, such as "14:05 to 14:10".
const rangeLabel = (since: string, until: string) => {
  return until ? `${clock(since, false)} to ${clock(until, false)}` : `Since ${clock(since, false)}`;
};

type ErrorInfo = { code: string; description: string; retry: boolean };
// The error catalog, fetched once: what each code means and whether a retry can work.
let catalog: Promise<Map<string, ErrorInfo>> | undefined;
const errorCatalog = () =>
  (catalog ??= api.GET("/v1/error-codes").then(({ data }) => new Map((data?.data ?? []).map((e) => [e.code, e]))));
// The path with the workspace left out: /v1/workspaces/acme/leads is …/leads.
const shortPath = (p: string) => p.replace(/^\/v1\/workspaces\/[^/]+/, "…");

export function Logs({ ws, canManage = false }: { ws: string; canManage?: boolean }) {
  const start = useMemo(readURL, []);
  const { query: brandQuery, brand, byId } = useBrand(ws);
  const [view, setView] = useState(start.view);
  const [f, setF] = useState<Filters>(start.f);
  const [q, setQ] = useState(start.f.q);
  const [open, setOpen] = useState<string | undefined>(start.open);
  const [logs, setLogs] = useState<LogEntry[] | null>(null);
  const [next, setNext] = useState<string | null>(null);
  const [error, setError] = useState("");
  const [live, setLive] = useState(false);
  const [fresh, setFresh] = useState<Set<string>>(new Set());
  const [hist, setHist] = useState<{ bucket: number; since: string; until: string; data: Bucket[] } | null>(null);
  const after = useRef<string | null>(null);
  const search = useRef<HTMLInputElement>(null);

  // "/" jumps to the search box, as in most developer tools.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement;
      if (e.key !== "/" || e.metaKey || e.ctrlKey || el.closest("input, textarea, select, [contenteditable], [role=dialog]")) return;
      e.preventDefault();
      search.current?.focus();
    };
    addEventListener("keydown", onKey);
    return () => removeEventListener("keydown", onKey);
  }, []);

  const params = useMemo(() => query(f, brandQuery), [f, brandQuery]);

  useEffect(() => writeURL(f, open, view, brand?.handle), [f, open, view, brand]);

  // Typing searches after a pause.
  useEffect(() => {
    const t = setTimeout(() => setF((cur) => (cur.q === q ? cur : { ...cur, q })), 350);
    return () => clearTimeout(t);
  }, [q]);

  const load = useCallback(async () => {
    setError("");
    const [list, chart, tail] = await Promise.all([
      api.GET("/v1/workspaces/{ws}/logs", { params: { path: { ws }, query: { ...params, limit: 100 } } }),
      api.GET("/v1/workspaces/{ws}/logs/histogram", { params: { path: { ws }, query: params } }),
      api.GET("/v1/workspaces/{ws}/logs", { params: { path: { ws }, query: { ...params, after: "latest" } } }),
    ]);
    if (list.error) {
      setError(errorMessage(list.error));
      setLogs([]);
      return;
    }
    setLogs(list.data.data);
    setNext(list.data.next_before ?? null);
    after.current = tail.data?.next_after ?? null;
    if (chart.data) setHist({ bucket: chart.data.bucket_seconds, since: chart.data.since, until: chart.data.until, data: chart.data.data });
  }, [ws, params]);

  useEffect(() => {
    if (view === "requests") void load();
  }, [load, view]);

  const older = async () => {
    if (!next) return;
    const { data, error } = await api.GET("/v1/workspaces/{ws}/logs", { params: { path: { ws }, query: { ...params, limit: 100, before: next } } });
    if (error) return toast(errorMessage(error), "error");
    setLogs((cur) => [...(cur ?? []), ...data.data]);
    setNext(data.next_before ?? null);
  };

  // Live: ask for calls after the newest one every two seconds, while the
  // tab is visible.
  useEffect(() => {
    if (!live || view !== "requests") return;
    let stop = false;
    let n = 0;
    const tick = async () => {
      if (stop || document.hidden || !after.current) return;
      if (++n % 5 === 0) {
        api.GET("/v1/workspaces/{ws}/logs/histogram", { params: { path: { ws }, query: params } }).then(({ data: c }) => {
          if (!stop && c) setHist({ bucket: c.bucket_seconds, since: c.since, until: c.until, data: c.data });
        });
      }
      const { data } = await api.GET("/v1/workspaces/{ws}/logs", { params: { path: { ws }, query: { ...params, after: after.current, limit: 500 } } });
      if (stop || !data) return;
      after.current = data.next_after ?? after.current;
      if (data.data.length === 0) return;
      const added = [...data.data].reverse();
      setLogs((cur) => [...added, ...(cur ?? [])].slice(0, 2000));
      setFresh(new Set(added.map((l) => l.id)));
    };
    const t = setInterval(tick, 2000);
    return () => {
      stop = true;
      clearInterval(t);
    };
  }, [live, view, ws, params]);

  const set = (patch: Partial<Filters>, what: string) => {
    setF((cur) => ({ ...cur, ...patch }));
    if ("q" in patch) setQ(patch.q ?? "");
    track("ui.logs_filtered", { filter: what });
  };
  const active = keys.filter((k) => k !== "since" && k !== "until" && (k === "level" ? f.level.length > 0 : f[k] !== ""));
  const index = logs?.findIndex((l) => l.id === open) ?? -1;

  const exportURL = (format: "csv" | "ndjson") => `/v1/workspaces/${encodeURIComponent(ws)}/logs/export?${new URLSearchParams({ ...params, format })}`;
  const download = (format: "csv" | "ndjson") => {
    track("ui.logs_exported", { format });
    const a = document.createElement("a");
    a.href = exportURL(format);
    a.download = "";
    a.click();
  };
  const copyLink = async () => {
    await navigator.clipboard?.writeText(location.href);
    track("ui.logs_link_copied", { filters: active.length });
    toast("Link copied. It opens this view for anyone in the workspace who can see logs.");
  };

  return (
    <div className="space-y-4">
      <PageHeader
        title="Logs"
        description="Every API call in this workspace, from the web app, the API, MCP agents, the CLI and missions. Kept 30 days. Bodies, keys and query values are never stored."
        action={
          view === "requests" && (
            <>
              <Button variant={live ? "primary" : "ghost"} onClick={() => { setLive((v) => !v); track("ui.logs_live_toggled", { on: !live }); }} aria-pressed={live}>
                {live ? <Pause className="size-3.5" /> : <Play className="size-3.5" />}
                {live ? "Live" : "Go live"}
                {live && <span className="size-1.5 animate-pulse rounded-full bg-current" aria-hidden />}
              </Button>
              <Button variant="ghost" onClick={copyLink} aria-label="Copy a link to this view"><Link2 className="size-3.5" />Share</Button>
              <Menu
                label="Export"
                aria="Export logs"
                items={[
                  { label: "Download CSV", onClick: () => download("csv") },
                  { label: "Download JSON (one call per line)", onClick: () => download("ndjson") },
                ]}
              />
            </>
          )
        }
      />

      <Tabs value={view} onValueChange={(v) => { setView(v as View); setOpen(undefined); }}>
        <TabsList>
          <TabsTrigger value="requests">API calls</TabsTrigger>
          <TabsTrigger value="jobs">Background jobs</TabsTrigger>
          <TabsTrigger value="incoming">Incoming webhooks</TabsTrigger>
        </TabsList>
      </Tabs>

      {view === "incoming" ? (
        <Incoming ws={ws} canManage={canManage} />
      ) : view === "jobs" ? (
        <Jobs ws={ws} />
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-2">
            <label className="relative min-w-0 flex-1 basis-64">
              <Search className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground" />
              <input
                className={`${field} w-full pl-8`}
                type="search"
                ref={search}
                placeholder="Search path, operation, error code, request ID or tool   /"
                aria-label="Search logs"
                value={q}
                onChange={(e) => setQ(e.target.value)}
              />
            </label>
            <select className={field} aria-label="Time range" value={f.since} onChange={(e) => set({ since: e.target.value, until: "" }, "since")}>
              {!isPreset(f.since) && <option value={f.since}>{rangeLabel(f.since, f.until)}</option>}
              {ranges.map(([v, l]) => <option key={v} value={v}>{l}</option>)}
            </select>
            <select className={field} aria-label="Source" value={f.surface} onChange={(e) => set({ surface: e.target.value }, "surface")}>
              {surfaces.map(([v, l]) => <option key={v} value={v}>{l}</option>)}
            </select>
            <select className={field} aria-label="Status" value={f.status} onChange={(e) => set({ status: e.target.value }, "status")}>
              {statuses.some(([v]) => v === f.status) ? null : <option value={f.status}>{f.status}</option>}
              {statuses.map(([v, l]) => <option key={v} value={v}>{l}</option>)}
            </select>
            <div className="flex h-8 items-center rounded-md border border-input p-0.5" role="group" aria-label="Level">
              {levels.map((l) => {
                const on = f.level.includes(l.id);
                return (
                  <button
                    key={l.id}
                    type="button"
                    aria-pressed={on}
                    onClick={() => set({ level: on ? f.level.filter((x) => x !== l.id) : [...f.level, l.id] }, "level")}
                    className={cn("flex h-full items-center gap-1.5 rounded px-2 text-[12px] text-muted-foreground hover:text-foreground", on && "bg-accent text-foreground")}
                  >
                    <span className={cn("size-1.5 rounded-full", l.dot)} />
                    {l.label}
                  </button>
                );
              })}
            </div>
            <Button variant="ghost" onClick={() => void load()} aria-label="Refresh"><RotateCw className="size-3.5" /></Button>
          </div>

          {active.length > 0 && (
            <div className="flex flex-wrap items-center gap-1.5">
              {active.map((k) => (
                <button
                  key={k}
                  type="button"
                  onClick={() => set({ [k]: k === "level" ? [] : "" } as Partial<Filters>, `clear_${k}`)}
                  className="inline-flex h-6 items-center gap-1 rounded border bg-muted px-1.5 font-mono text-[11px] text-muted-foreground hover:text-foreground"
                  aria-label={`Remove filter ${k}`}
                >
                  {k === "error_code" ? "error" : k.replace("_id", "")}: {k === "level" ? f.level.join(", ") : k === "surface" ? surfaceName(f.surface) : f[k]}
                  <X className="size-3" />
                </button>
              ))}
              <button type="button" className="text-[12px] text-muted-foreground underline-offset-2 hover:text-foreground hover:underline" onClick={() => { setF({ ...empty, since: f.since, until: f.until }); setQ(""); track("ui.logs_filtered", { filter: "clear_all" }); }}>
                Clear all
              </button>
            </div>
          )}

          {hist && <Histogram {...hist} onZoom={(since, until) => set({ since, until }, "chart_range")} />}
          {error && <ErrorText>{error}</ErrorText>}
          {logs === null && <Loading />}
          {logs?.length === 0 && !error && (
            <Empty title={active.length ? "No calls match these filters" : "No calls yet"}>
              {active.length ? "Try a longer time range or remove a filter." : "Calls from the web app, the API, MCP agents and the CLI show up here as they happen. Turn on Live to watch."}
            </Empty>
          )}
          {logs && logs.length > 0 && (
            <div className="overflow-hidden rounded-md border">
              <div className="hidden grid-cols-[88px_56px_60px_minmax(0,1fr)_minmax(0,200px)_150px_64px] gap-3 border-b bg-muted/40 px-3 py-1.5 font-mono text-[11px] text-muted-foreground md:grid">
                <span>Time</span><span>Status</span><span>Method</span><span>Path</span><span>Operation</span><span>Who</span><span className="text-right">Duration</span>
              </div>
              <ul className="divide-y font-mono text-[12px]">
                {logs.map((l) => (
                  <li key={l.id}>
                    <button
                      type="button"
                      onClick={() => setOpen(l.id)}
                      className={cn(
                        "grid w-full grid-cols-[88px_44px_minmax(0,1fr)] items-center gap-x-3 gap-y-0.5 px-3 py-1.5 text-left hover:bg-accent/60 md:grid-cols-[88px_56px_60px_minmax(0,1fr)_minmax(0,200px)_150px_64px]",
                        l.level === "error" && "bg-red-500/5",
                        fresh.has(l.id) && "animate-in fade-in bg-brand/5",
                        open === l.id && "bg-accent",
                      )}
                    >
                      <span className="text-muted-foreground" title={stamp(l.created_at)}>{clock(l.created_at)}</span>
                      <span><Badge tone={statusTone(l.status)}>{l.status}</Badge></span>
                      <span className="hidden text-muted-foreground md:block">{l.method}</span>
                      <span className="truncate" title={l.path}>
                        <span className="text-muted-foreground md:hidden">{l.method} </span>
                        {shortPath(l.path)}
                        {l.error_code && <span className={cn("ml-2", l.level === "error" ? "text-red-600 dark:text-red-400" : "text-amber-700 dark:text-amber-400")}>{l.error_code}{l.error_param && ` · ${l.error_param}`}</span>}
                      </span>
                      <span className="col-start-3 truncate text-muted-foreground md:col-start-auto" title={l.operation}>{l.mcp_tool ? `${l.mcp_tool} (MCP)` : l.operation}</span>
                      <span className="col-start-3 flex min-w-0 items-center gap-1.5 truncate text-muted-foreground md:col-start-auto">
                        <span className="shrink-0 rounded border px-1 text-[10px] uppercase">{l.surface}</span>
                        <span className="truncate">{l.actor_name ?? (l.actor_type === "system" ? "Outflare" : "")}</span>
                      </span>
                      <span className={cn("hidden text-right tabular-nums md:block", l.duration_ms >= 1000 ? "text-amber-600 dark:text-amber-400" : "text-muted-foreground")}>{ms(l.duration_ms)}</span>
                    </button>
                  </li>
                ))}
              </ul>
            </div>
          )}
          {next && !live && <Button variant="ghost" onClick={older}>Load older</Button>}
          {live && <p className="font-mono text-[11px] text-muted-foreground">Live: new calls appear at the top every 2 seconds.</p>}
        </>
      )}

      {open && (
        <LogSheet
          ws={ws}
          id={open}
          brandName={(id) => byId(id)?.name}
          onClose={() => setOpen(undefined)}
          onFilter={(patch, what) => { set(patch, what); setOpen(undefined); }}
          newer={index > 0 ? () => setOpen(logs![index - 1].id) : undefined}
          older={index >= 0 && index < (logs?.length ?? 0) - 1 ? () => setOpen(logs![index + 1].id) : undefined}
        />
      )}
    </div>
  );
}

type Bucket = { at: string; info: number; warning: number; error: number };

// Calls over the range, stacked by level, one bar per bucket including
// the empty ones, so bars sit where they happened. Hover a bar for counts.
function Histogram({ data, bucket, since, until, onZoom }: { data: Bucket[]; bucket: number; since: string; until: string; onZoom: (since: string, until: string) => void }) {
  const step = bucket * 1000;
  const from = Math.floor(new Date(since).getTime() / step) * step;
  const to = new Date(until).getTime();
  const byTime = new Map(data.map((d) => [new Date(d.at).getTime(), d]));
  const bars: Bucket[] = [];
  for (let t = from; t <= to && bars.length < 400; t += step) bars.push(byTime.get(t) ?? { at: new Date(t).toISOString(), info: 0, warning: 0, error: 0 });
  const max = Math.max(1, ...bars.map((d) => d.info + d.warning + d.error));
  const total = data.reduce((a, d) => ({ info: a.info + d.info, warning: a.warning + d.warning, error: a.error + d.error }), { info: 0, warning: 0, error: 0 });
  const calls = total.info + total.warning + total.error;
  const failed = calls ? ((total.warning + total.error) / calls) * 100 : 0;
  const label = (iso: string) => (bucket >= 86400 ? new Date(iso).toLocaleDateString(undefined, { day: "numeric", month: "short" }) : clock(iso, bucket < 60));
  if (bars.length === 0) return null;
  return (
    <div className="rounded-md border p-3">
      <div className="mb-2 flex flex-wrap gap-4 font-mono text-[11px] text-muted-foreground">
        <span className="text-foreground">{calls.toLocaleString()} calls</span>
        <span className="flex items-center gap-1"><span className="size-1.5 rounded-full bg-muted-foreground/60" />{total.info.toLocaleString()} info</span>
        <span className="flex items-center gap-1"><span className="size-1.5 rounded-full bg-amber-500" />{total.warning.toLocaleString()} warning</span>
        <span className="flex items-center gap-1"><span className="size-1.5 rounded-full bg-red-500" />{total.error.toLocaleString()} error</span>
        {calls > 0 && <span>{failed < 0.1 && failed > 0 ? "<0.1" : failed.toFixed(failed >= 10 ? 0 : 1)}% failed</span>}
        <span className="ml-auto hidden sm:inline">Click a bar to zoom in</span>
      </div>
      <div className="flex h-14 items-end gap-px" role="img" aria-label="Calls over time by level">
        {bars.map((d) => {
          const n = d.info + d.warning + d.error;
          return (
            <button
              type="button"
              key={d.at}
              onClick={() => onZoom(d.at, new Date(new Date(d.at).getTime() + step).toISOString())}
              aria-label={`${label(d.at)}: ${n} calls. Zoom in`}
              className="flex h-full min-w-px flex-1 cursor-zoom-in flex-col justify-end rounded-t-[1px] hover:bg-accent"
              title={`${label(d.at)}: ${n} calls, ${d.warning} warning, ${d.error} error. Click to zoom in.`}
            >
              <div className="bg-red-500" style={{ height: `${(d.error / max) * 100}%` }} />
              <div className="bg-amber-500" style={{ height: `${(d.warning / max) * 100}%` }} />
              <div className="bg-muted-foreground/40" style={{ height: `${(d.info / max) * 100}%` }} />
              {n === 0 && <div className="h-px bg-border" />}
            </button>
          );
        })}
      </div>
      <div className="mt-1 flex justify-between font-mono text-[10px] text-muted-foreground">
        <span>{label(bars[0].at)}</span>
        <span>{label(bars[bars.length - 1].at)}</span>
      </div>
    </div>
  );
}

// One call: what it was, who made it, why it failed and what it changed.
// ↑ and ↓ (or k and j) step to the newer and older call.
function LogSheet({ ws, id, brandName, onClose, onFilter, newer, older }: { ws: string; id: string; brandName: (id?: string | null) => string | undefined; onClose: () => void; onFilter: (patch: Partial<Filters>, what: string) => void; newer?: () => void; older?: () => void }) {
  const [data, setData] = useState<{ log: LogEntry; audit_events: AuditEvent[] } | null>(null);
  const [error, setError] = useState("");
  const [info, setInfo] = useState<ErrorInfo>();
  useEffect(() => {
    setData(null);
    setError("");
    api.GET("/v1/workspaces/{ws}/logs/{id}", { params: { path: { ws, id } } }).then(({ data, error }) => (error ? setError(errorMessage(error)) : setData(data)));
  }, [ws, id]);
  const l = data?.log;
  useEffect(() => {
    setInfo(undefined);
    if (l?.error_code) void errorCatalog().then((c) => setInfo(c.get(l.error_code!)));
  }, [l?.error_code]);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.target as HTMLElement).closest("input, textarea, select")) return;
      if ((e.key === "ArrowUp" || e.key === "k") && newer) { e.preventDefault(); newer(); }
      if ((e.key === "ArrowDown" || e.key === "j") && older) { e.preventDefault(); older(); }
    };
    addEventListener("keydown", onKey);
    return () => removeEventListener("keydown", onKey);
  }, [newer, older]);

  // Everything someone needs to chase the call, for a bug report or a chat.
  const report = l
    ? [
        `${l.method} ${l.path} → ${l.status}${l.error_code ? ` ${l.error_code}` : ""}${l.error_param ? ` (${l.error_param})` : ""}`,
        `Request ID: ${l.request_id}`,
        `Operation: ${l.operation}`,
        `Time: ${new Date(l.created_at).toISOString()} (${l.duration_ms}ms)`,
        `Source: ${l.surface}${l.client ? ` (${l.client})` : ""}${l.mcp_tool ? `, MCP tool ${l.mcp_tool}` : ""}`,
      ].join("\n")
    : "";

  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-xl">
        <SheetHeader className="pr-12">
          <div className="flex items-center gap-1">
            <button type="button" disabled={!newer} onClick={newer} aria-label="Newer call (↑)" title="Newer call (↑)" className="flex size-7 items-center justify-center rounded border text-muted-foreground hover:text-foreground disabled:opacity-40"><ChevronUp className="size-4" /></button>
            <button type="button" disabled={!older} onClick={older} aria-label="Older call (↓)" title="Older call (↓)" className="flex size-7 items-center justify-center rounded border text-muted-foreground hover:text-foreground disabled:opacity-40"><ChevronDown className="size-4" /></button>
            {l && <CopyButton text={report} label="Copy details for a bug report" className="ml-auto" />}
          </div>
          <SheetTitle className="flex items-center gap-2 font-mono text-[14px] break-all">
            {l && <Badge tone={statusTone(l.status)}>{l.status}</Badge>}
            {l ? `${l.method} ${shortPath(l.path)}` : "Call"}
          </SheetTitle>
          <SheetDescription>{l ? `${stamp(l.created_at)} (${ago(l.created_at)}) · took ${ms(l.duration_ms)}` : error ? "This call couldn't be loaded." : "Loading the call…"}</SheetDescription>
        </SheetHeader>
        <div className="space-y-5 px-4 pb-6">
          {error && <ErrorText>{error}</ErrorText>}
          {!l && !error && <Loading />}
          {l && (
            <>
              {l.error_code && (
                <div className={cn("space-y-2 rounded-md border p-3 text-[13px]", l.level === "error" ? "border-red-600/25 bg-red-500/5" : "border-amber-600/25 bg-amber-500/5")}>
                  <div className="flex flex-wrap items-center gap-2 font-mono">
                    <span className={l.level === "error" ? "text-red-700 dark:text-red-400" : "text-amber-800 dark:text-amber-400"}>{l.error_code}</span>
                    {l.error_param && <span className="text-muted-foreground">{l.error_code === "forbidden" ? "missing scope" : "field"}: <span className="text-foreground">{l.error_param}</span></span>}
                  </div>
                  {info && <p>{info.description}</p>}
                  <p className="text-muted-foreground">
                    {l.level === "error"
                      ? "Outflare broke, not your request. Retry later; if it keeps failing, send the request ID (Copy above) and the server log has the full error."
                      : l.error_code === "forbidden" && l.error_param
                        ? l.actor_type === "api_token"
                          ? `Add ${l.error_param} to this token under API tokens, or use a token that has it.`
                          : l.actor_type === "human"
                            ? `This person's role doesn't include ${l.error_param}. An admin can change their role under Members.`
                            : `Whoever made this call wasn't allowed ${l.error_param}.`
                      : info?.retry
                        ? "Retrying the same request later can work."
                        : "Retrying the same request won't help: fix the request first. The response's message said exactly what was wrong."}
                  </p>
                  <FilterLink onClick={() => onFilter({ error_code: l.error_code! }, "error_code")}>Every call with this error</FilterLink>
                </div>
              )}
              <dl className="grid grid-cols-[110px_minmax(0,1fr)] gap-x-3 gap-y-1.5 text-[13px]">
                <Field k="Request ID"><Copyable v={l.request_id} /></Field>
                <Field k="Operation">
                  <FilterLink onClick={() => onFilter({ operation: l.operation }, "operation")}>{l.operation}</FilterLink>
                </Field>
                <Field k="Route"><span className="font-mono text-[12px] break-all">{l.route}</span></Field>
                <Field k="Path"><Copyable v={l.path} /></Field>
                {l.query_keys.length > 0 && <Field k="Query"><span className="font-mono text-[12px]">{l.query_keys.join(", ")} <span className="text-muted-foreground">(values not kept)</span></span></Field>}
                <Field k="Source"><FilterLink onClick={() => onFilter({ surface: l.surface }, "surface")}>{surfaceName(l.surface)}</FilterLink>{l.client && <span className="ml-2 font-mono text-[12px] text-muted-foreground">{l.client}</span>}</Field>
                {l.mcp_tool && <Field k="MCP tool"><FilterLink onClick={() => onFilter({ mcp_tool: l.mcp_tool! }, "mcp_tool")}>{l.mcp_tool}</FilterLink></Field>}
                {l.agent && <Field k="Agent app"><span className="font-mono text-[12px]">{l.agent}</span></Field>}
                <Field k="Who">
                  {l.actor_id ? <FilterLink onClick={() => onFilter({ actor_id: l.actor_id! }, "actor")}>{l.actor_name ?? l.actor_id}</FilterLink> : "Nobody signed in"}
                  {l.actor_type && l.actor_type !== "human" && <span className="ml-2"><Badge tone="blue">{l.actor_type === "api_token" ? "token" : l.actor_type}</Badge></span>}
                </Field>
                {l.mission_id && <Field k="Mission"><FilterLink onClick={() => onFilter({ mission_id: l.mission_id! }, "mission")}>Every call in this mission</FilterLink></Field>}
                {l.brand_id && <Field k="Brand">{brandName(l.brand_id) ?? <span className="font-mono text-[12px]">{l.brand_id}</span>}</Field>}
                {l.user_agent && <Field k="User agent"><span className="font-mono text-[12px] break-all text-muted-foreground">{l.user_agent}</span></Field>}
              </dl>
              <section>
                <h3 className="mb-2 text-[13px] font-medium">What it changed</h3>
                {data.audit_events.length === 0 ? (
                  <p className="text-[13px] text-muted-foreground">{l.method === "GET" ? "Nothing: it only read data." : l.status >= 400 ? "Nothing: it was refused before changing anything." : "Nothing was recorded in the audit log."}</p>
                ) : (
                  <ul className="space-y-2">
                    {data.audit_events.map((e) => (
                      <li key={e.id} className="rounded-md border p-2">
                        <div className="font-mono text-[12px]">{e.action}</div>
                        {e.target_type && <div className="font-mono text-[11px] text-muted-foreground">{e.target_type} {e.target_id}</div>}
                      </li>
                    ))}
                  </ul>
                )}
              </section>
              <details className="group">
                <summary className="cursor-pointer text-[13px] font-medium select-none">As JSON</summary>
                <div className="relative mt-2">
                  <CopyButton text={JSON.stringify(l, null, 2)} label="Copy JSON" className="absolute top-1 right-1" />
                  <pre className="overflow-x-auto rounded border bg-muted/40 p-2 text-[11px]"><code>{JSON.stringify(l, null, 2)}</code></pre>
                </div>
              </details>
              <p className="text-[12px] text-muted-foreground">Logs never keep request or response bodies, query values, headers or keys.</p>
            </>
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}

function Field({ k, children }: { k: string; children: ReactNode }) {
  return (
    <>
      <dt className="text-muted-foreground">{k}</dt>
      <dd className="flex min-w-0 flex-wrap items-center">{children}</dd>
    </>
  );
}

function Copyable({ v }: { v: string }) {
  return <span className="inline-flex min-w-0 items-center font-mono text-[12px] break-all">{v}<CopyButton text={v} label="Copy" /></span>;
}

function FilterLink({ children, onClick }: { children: ReactNode; onClick: () => void }) {
  return (
    <button type="button" onClick={onClick} className="font-mono text-[12px] underline decoration-dotted underline-offset-2 hover:text-brand-strong dark:hover:text-brand" title="Show only calls like this">
      {children}
    </button>
  );
}

// Background work (syncs, imports, DNS checks, sends) from the jobs API.
function Jobs({ ws }: { ws: string }) {
  const [status, setStatus] = useState("");
  const [jobs, setJobs] = useState<Job[] | null>(null);
  const [next, setNext] = useState<string | null>(null);
  const [shown, setShown] = useState<string>();
  const load = useCallback(
    async (before?: string) => {
      const { data, error } = await api.GET("/v1/workspaces/{ws}/jobs", { params: { path: { ws }, query: { status: (status || undefined) as Job["status"] | undefined, before } } });
      if (error) {
        toast(errorMessage(error), "error");
        return;
      }
      setJobs((cur) => (before ? [...(cur ?? []), ...data.data] : data.data));
      setNext(data.next_before ?? null);
    },
    [ws, status],
  );
  useEffect(() => {
    setJobs(null);
    void load();
  }, [load]);
  return (
    <div className="space-y-3">
      <div className="flex items-center gap-2">
        <select className={field} aria-label="Job status" value={status} onChange={(e) => setStatus(e.target.value)}>
          <option value="">Every job</option>
          <option value="failed">Failed</option>
          <option value="running">Running</option>
          <option value="queued">Queued or retrying</option>
          <option value="succeeded">Succeeded</option>
        </select>
        <span className="text-[12px] text-muted-foreground">Finished jobs are kept 14 days.</span>
      </div>
      {jobs === null && <Loading />}
      {jobs?.length === 0 && <Empty title="No jobs">Syncs, imports, DNS checks and other background work show up here.</Empty>}
      {jobs && jobs.length > 0 && (
        <ul className="divide-y rounded-md border font-mono text-[12px]">
          {jobs.map((j) => (
            <li key={j.id}>
              {/* A failed job opens to its whole error. */}
              <button
                type="button"
                disabled={!j.error}
                onClick={() => setShown(shown === j.id ? undefined : j.id)}
                aria-expanded={j.error ? shown === j.id : undefined}
                className="grid w-full grid-cols-[88px_90px_minmax(0,1fr)] items-start gap-3 px-3 py-1.5 text-left enabled:hover:bg-accent/60 md:grid-cols-[110px_90px_minmax(0,220px)_minmax(0,1fr)_110px]"
              >
                <span className="text-muted-foreground" title={stamp(j.updated_at)}>{ago(j.updated_at)}</span>
                <span><Badge tone={j.status === "failed" ? "red" : j.status === "succeeded" ? "green" : j.status === "running" ? "blue" : "grey"}>{j.status}</Badge></span>
                <span className="truncate">{j.kind}</span>
                <span className={cn("col-start-3 text-muted-foreground md:col-start-auto", shown === j.id ? "whitespace-pre-wrap break-words text-foreground" : "truncate")}>{j.error ?? ""}</span>
                <span className="hidden text-right text-muted-foreground md:block" title="Attempts so far, out of the most it gets">try {j.attempts} of {j.max_attempts}</span>
              </button>
            </li>
          ))}
        </ul>
      )}
      {next && <Button variant="ghost" onClick={() => load(next)}>Load older</Button>}
    </div>
  );
}

// Incoming: every event connected tools sent by webhook, with the
// connection's name.
function Incoming({ ws, canManage }: { ws: string; canManage: boolean }) {
  const [names, setNames] = useState<Map<string, string>>();
  useEffect(() => {
    void api.GET("/v1/workspaces/{ws}/integrations", { params: { path: { ws } } }).then(({ data }) => {
      if (data) setNames(new Map(data.data.map((i) => [i.id, i.name])));
    });
  }, [ws]);
  return <HookEvents ws={ws} canManage={canManage} names={names} />;
}
