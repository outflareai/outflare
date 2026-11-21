// Tracker → Report (plans/ad-tracker-ui.md B1): the drill-down report over
// the brand's visits, credited conversions, revenue and ad spend, grouped by
// up to five levels, with saved views and custom columns (edited in a card above the table,
// so the table shows each one as it is added). Every list of the
// tracker is this report with its first level fixed (Voluum's "every list is
// a report"); the whole view lives in the URL, so it can be shared or opened
// in a new tab.

import { createContext, Fragment, useCallback, useContext, useEffect, useMemo, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { PickBrand } from "@/components/pick-brand";
import { pickedBrandId, useBrand } from "@/lib/brand";
import { csvName, downloadCsv } from "@/lib/csv";
import { track } from "@/lib/analytics";
import { pageParams } from "@/lib/urlParam";
import { api, errorMessage } from "../api";
import { ask, Button, Card, confirmAction, CopyButton, Empty, ErrorText, field, Loading, Menu, PageHeader, Stat, Stats, toast } from "../ui";
import { presetLabels } from "./Funnels";
import type { Tab } from "../App";

type Report = Schemas["DrilldownReport"];
type Metrics = Schemas["DrilldownMetrics"];
type View = Schemas["ReportView"];
type ViewSpec = Schemas["ReportViewSpec"];
type Row = { keys: string[]; metrics: Metrics; rows: Row[] };
type Preset = keyof typeof presetLabels;
type Filter = { dim: string; value: string; not: boolean };
type Column = { name: string; formula: string };

type Props = { ws: string; canManage: boolean; go: (tab: Tab, item?: string) => void };

// The dimensions, as the report names them, in the groups the picker shows.
export const dimensionGroups: { label: string; dims: [string, string][] }[] = [
  { label: "Traffic", dims: [["channel", "Channel"], ["source", "Source"], ["campaign", "Campaign"], ["tracking_campaign", "Tracker campaign"]] },
  { label: "Ad ids", dims: [["ad_campaign", "Ad campaign"], ["ad_adset", "Ad set"], ["ad_ad", "Ad"], ["ad_placement", "Placement"]] },
  { label: "UTM tags", dims: [["utm_source", "utm_source"], ["utm_medium", "utm_medium"], ["utm_campaign", "utm_campaign"], ["utm_term", "utm_term"], ["utm_content", "utm_content"]] },
  { label: "Geo", dims: [["country", "Country"], ["region", "Region"], ["city", "City"], ["language", "Language"]] },
  { label: "Connection", dims: [["isp", "ISP"], ["asn", "ASN"]] },
  { label: "Device", dims: [["device", "Device"], ["device_brand", "Device brand"], ["os", "OS"], ["browser", "Browser"]] },
  { label: "Time", dims: [["day", "Day"], ["hour", "Hour"], ["weekday", "Weekday"]] },
  { label: "Visit", dims: [["landing_path", "Landing page"], ["referrer_host", "Referrer"]] },
];
// The tracker source's dimensions (B5d): its clicks, where they went, and
// the custom fields the traffic source filled.
export const trackerDimensionGroups: { label: string; dims: [string, string][] }[] = [
  { label: "Tracker", dims: [["tracking_campaign", "Tracker campaign"], ["traffic_source", "Traffic source"], ["offer", "Offer"], ["lander", "Lander"], ["path", "Path"], ["suspect", "Suspect"]] },
  { label: "Geo", dims: [["country", "Country"], ["region", "Region"], ["city", "City"], ["language", "Language"]] },
  { label: "Connection", dims: [["isp", "ISP"], ["asn", "ASN"]] },
  { label: "Device", dims: [["device", "Device"], ["device_brand", "Device brand"], ["os", "OS"], ["browser", "Browser"]] },
  { label: "Time", dims: [["day", "Day"], ["hour", "Hour"], ["weekday", "Weekday"]] },
  { label: "Custom fields", dims: Array.from({ length: 20 }, (_, i) => [`field${i + 1}`, `Field ${i + 1}`] as [string, string]) },
];
type Source = "site" | "tracker";
const groupsOf = (source: Source) => (source === "tracker" ? trackerDimensionGroups : dimensionGroups);
const defaultGroup = (source: Source) => (source === "tracker" ? "tracking_campaign" : "channel");
// The tracker dimensions whose values are ids, shown by name.
const idDims = ["tracking_campaign", "traffic_source", "offer", "lander", "path"];
export const dimLabel: Record<string, string> = Object.fromEntries([...dimensionGroups, ...trackerDimensionGroups].flatMap((g) => g.dims));

// The metric columns, bold ones on by default (B1's table).
// The lander columns come with the tracker source only.
const metricCols: { key: keyof Omit<Metrics, "custom">; label: string; kind: "count" | "money" | "ratio" | "percent"; tracker?: boolean; title?: string }[] = [
  { key: "visits", label: "Visits", kind: "count" },
  { key: "visitors", label: "Visitors", kind: "count" },
  { key: "conversions", label: "Conversions", kind: "count" },
  { key: "cr", label: "CR", kind: "percent" },
  { key: "revenue", label: "Revenue", kind: "money" },
  { key: "cost", label: "Cost", kind: "money" },
  { key: "profit", label: "Profit", kind: "money" },
  { key: "roi", label: "ROI", kind: "percent" },
  { key: "roas", label: "ROAS", kind: "ratio" },
  { key: "epv", label: "EPV", kind: "money" },
  { key: "cpa", label: "CPA", kind: "money" },
  { key: "lander_views", label: "Lander views", kind: "count", tracker: true, title: "Clicks sent to a lander." },
  { key: "lander_clicks", label: "Lander clicks", kind: "count", tracker: true, title: "Of those, the clicks that went on from the lander to an offer." },
  { key: "lander_ctr", label: "Lander CTR", kind: "percent", tracker: true, title: "Lander clicks out of lander views." },
];

// State, as it lives in the URL.
type State = { view: string; source: Source; preset: Preset | ""; from: string; to: string; tz: string; conv: "visit" | "conversion"; groups: string[]; filters: Filter[]; sort: string; columns: Column[] };
const defaults: State = { view: "", source: "site", preset: "last_7_days", from: "", to: "", tz: "", conv: "visit", groups: ["channel"], filters: [], sort: "-visits", columns: [] };

export function readState(search: string): State {
  const q = new URLSearchParams(search);
  const s: State = { ...defaults, groups: [...defaults.groups] };
  s.view = q.get("view") ?? "";
  const p = q.get("preset");
  if (p && p in presetLabels) s.preset = p as Preset;
  if (q.get("from") && q.get("to")) {
    s.from = q.get("from")!;
    s.to = q.get("to")!;
    s.preset = "";
  }
  s.tz = q.get("tz") ?? "";
  // A fresh visit opens on the tracker's own clicks. A link names its
  // source; one made before links did (some state, no source) meant site
  // visits.
  const fresh = [...q.keys()].length === 0;
  if (q.get("source") === "tracker" || (fresh && !q.get("source"))) {
    s.source = "tracker";
    s.groups = [defaultGroup("tracker")];
  }
  if (q.get("conversion_time") === "conversion") s.conv = "conversion";
  const known = Object.fromEntries(groupsOf(s.source).flatMap((x) => x.dims));
  const g = q.get("group_by");
  if (g !== null) s.groups = g.split(",").filter((x) => x in known).slice(0, 5);
  for (const [kind, not] of [["filter", false], ["exclude", true]] as const)
    for (const f of q.getAll(kind)) {
      const i = f.indexOf(":");
      if (i > 0) s.filters.push({ dim: f.slice(0, i), value: f.slice(i + 1), not });
    }
  s.sort = q.get("sort") ?? defaults.sort;
  for (const c of q.getAll("column")) {
    const i = c.indexOf(":");
    if (i > 0) s.columns.push({ name: c.slice(0, i), formula: c.slice(i + 1) });
  }
  return s;
}

export function writeState(s: State): string {
  const q = new URLSearchParams();
  if (s.view) q.set("view", s.view);
  if (s.preset && s.preset !== defaults.preset) q.set("preset", s.preset);
  if (!s.preset && s.from && s.to) {
    q.set("from", s.from);
    q.set("to", s.to);
  }
  if (s.tz) q.set("tz", s.tz);
  q.set("source", s.source);
  if (s.conv !== "visit") q.set("conversion_time", s.conv);
  if (s.groups.join(",") !== defaultGroup(s.source)) q.set("group_by", s.groups.join(","));
  for (const f of s.filters) q.append(f.not ? "exclude" : "filter", `${f.dim}:${f.value}`);
  if (s.sort !== defaults.sort) q.set("sort", s.sort);
  for (const c of s.columns) q.append("column", `${c.name}:${c.formula}`);
  return q.toString();
}

// todayIn is today's date in a time zone, as YYYY-MM-DD.
export function todayIn(tz: string, now = new Date()): string {
  try {
    return new Intl.DateTimeFormat("en-CA", { timeZone: tz || "UTC", year: "numeric", month: "2-digit", day: "2-digit" }).format(now);
  } catch {
    return now.toISOString().slice(0, 10);
  }
}

// rangeOf is a preset's first and last day, counted from today in the zone.
export function rangeOf(preset: Preset, today: string): [string, string] {
  const d = (s: string) => new Date(s + "T00:00:00Z");
  const iso = (x: Date) => x.toISOString().slice(0, 10);
  const add = (s: string, days: number) => iso(new Date(d(s).getTime() + days * 86400000));
  const t = d(today);
  const m = preset.match(/^last_(\d+)_days$/);
  if (m) return [add(today, -(Number(m[1]) - 1)), today];
  switch (preset) {
    case "yesterday":
      return [add(today, -1), add(today, -1)];
    case "this_month":
      return [iso(new Date(Date.UTC(t.getUTCFullYear(), t.getUTCMonth(), 1))), today];
    case "last_month": {
      const first = new Date(Date.UTC(t.getUTCFullYear(), t.getUTCMonth() - 1, 1));
      const last = new Date(Date.UTC(t.getUTCFullYear(), t.getUTCMonth(), 0));
      return [iso(first), iso(last)];
    }
    case "this_year":
      return [`${t.getUTCFullYear()}-01-01`, today];
  }
  return [today, today];
}

const zones = (() => {
  try {
    return Intl.supportedValuesOf("timeZone");
  } catch {
    return ["UTC"];
  }
})();

export function format(v: number | null | undefined, kind: "count" | "money" | "ratio" | "percent" | "decimal", currency: string): string {
  if (v === null || v === undefined) return "—";
  switch (kind) {
    case "decimal":
      // A custom column can be a ratio (0.989) or a count: two places show both.
      return v.toLocaleString(undefined, { maximumFractionDigits: 2 });
    case "money":
      try {
        return new Intl.NumberFormat(undefined, { style: "currency", currency, maximumFractionDigits: Math.abs(v) < 100 ? 2 : 0 }).format(v);
      } catch {
        return v.toFixed(2);
      }
    case "percent":
      return `${(v * 100).toLocaleString(undefined, { maximumFractionDigits: 1 })}%`;
    case "ratio":
      return `${v.toLocaleString(undefined, { maximumFractionDigits: 2 })}×`;
  }
  return Number.isInteger(v) ? v.toLocaleString() : v.toLocaleString(undefined, { maximumFractionDigits: 1 });
}

// Unconverted warns that money is missing from every number on screen: a
// currency with no exchange rate can't be added to the others.
export function Unconverted({ currencies, to }: { currencies: string[]; to: string }) {
  return (
    <p role="status" data-testid="unconverted" className="rounded-md border border-amber-300 bg-amber-50 p-2 text-xs text-amber-900 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-200">
      Revenue and cost in {currencies.join(", ")} are not counted here: there is no exchange rate from {currencies.length === 1 ? currencies[0] : "them"} to {to} yet. Rates arrive daily; until then these numbers are too low.
    </p>
  );
}

// Profit and ROI are green above zero and red below; the sign stays, so
// colour is never the only signal.
export const tone = (key: string, v: number | null | undefined) =>
  (key === "profit" || key === "roi") && v !== null && v !== undefined && v !== 0 ? (v > 0 ? "text-emerald-600 dark:text-emerald-400" : "text-red-600 dark:text-red-400") : "";

export function TrackerReport(props: Props) {
  const { brand, active } = useBrand(props.ws);
  if (!brand) return <PickBrand what="the tracker report" brands={active.length} />;
  return <ReportPage {...props} brandTz={brand.timezone ?? "UTC"} />;
}

// The report's own parameters belong to its page: the app clears them when
// the page changes.
for (const name of ["view", "preset", "from", "to", "tz", "source", "conversion_time", "group_by", "filter", "exclude", "sort", "column"]) pageParams.add(name);

// openReport hands the next Report page its state, as the query string a
// link to it would carry: the page reads its state as it opens, before the
// app has moved the address there. Call it, then go to the Report.
let handoff: string | null = null;
export function openReport(query: string) {
  handoff = query;
}
// pendingReport is the state waiting for the next Report page, for tests.
export const pendingReport = () => handoff;

function ReportPage({ canManage, brandTz }: Props & { brandTz: string }) {
  const [s, setS] = useState<State>(() => readState(handoff ?? window.location.search));
  const [report, setReport] = useState<Report | null>(null);
  const [views, setViews] = useState<View[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [open, setOpen] = useState<Record<string, boolean>>({});
  const [columnsOpen, setColumnsOpen] = useState(false);
  const tz = s.tz || brandTz;
  const [from, to] = s.preset ? rangeOf(s.preset, todayIn(tz)) : [s.from, s.to];
  const set = (patch: Partial<State>) => setS((x) => ({ ...x, ...patch }));

  // The URL follows the view, so a link or a new tab opens the same report.
  // It is written a tick later: arriving from another page, the app moves
  // the address here first (and drops the report's parameters on the way
  // out, so they never trail onto other pages).
  useEffect(() => {
    handoff = null;
    const t = setTimeout(() => {
      const q = writeState(s);
      window.history.replaceState(window.history.state, "", window.location.pathname + (q ? "?" + q : ""));
    }, 0);
    return () => clearTimeout(t);
  }, [s]);

  const query = useMemo(() => {
    const q: Record<string, unknown> = { from, to, source: s.source, group_by: s.groups.join(","), sort: s.sort, conversion_time: s.conv };
    if (s.tz) q.timezone = s.tz;
    const filters = s.filters.filter((f) => !f.not).map((f) => `${f.dim}:${f.value}`);
    const excludes = s.filters.filter((f) => f.not).map((f) => `${f.dim}:${f.value}`);
    if (filters.length) q.filter = filters;
    if (excludes.length) q.exclude = excludes;
    if (s.columns.length) q.column = s.columns.map((c) => `${c.name}:${c.formula}`);
    return q;
  }, [from, to, s.source, s.groups, s.sort, s.conv, s.tz, s.filters, s.columns]);

  const load = useCallback(() => {
    setLoading(true);
    api
      .GET("/v1/brands/{brand}/drilldown", { params: { path: { brand: pickedBrandId() }, query: query as never } })
      .then(({ data, error }) => {
        if (error) {
          setError(errorMessage(error));
          setReport(null);
        } else {
          setError("");
          setReport(data as Report);
        }
      })
      .finally(() => setLoading(false));
  }, [query]);
  useEffect(load, [load]);

  // Names for the tracker's ids: campaigns, traffic sources, offers, landers.
  const [names, setNames] = useState<Record<string, string>>({});
  useEffect(() => {
    if (s.source !== "tracker") return;
    const path = { path: { brand: pickedBrandId() } };
    void Promise.all([
      api.GET("/v1/brands/{brand}/tracking-campaigns", { params: path }),
      api.GET("/v1/brands/{brand}/traffic-sources", { params: path }),
      api.GET("/v1/brands/{brand}/offers", { params: path }),
      api.GET("/v1/brands/{brand}/landers", { params: path }),
    ]).then((rs) => {
      const lists = rs.map((r) => ((r.data as { data?: { id: string; name: string }[] } | undefined)?.data ?? []));
      setNames(Object.fromEntries(lists.flat().map((x) => [x.id, x.name])));
      setPick({ tracking_campaign: lists[0], traffic_source: lists[1], offer: lists[2], lander: lists[3] });
      const by: Record<string, string[]> = {};
      for (const src of lists[1] as unknown as { parameters?: { name: string; field: number }[] }[])
        for (const p of src.parameters ?? []) if (p.name && !(by[`field${p.field}`] ??= []).includes(p.name)) by[`field${p.field}`].push(p.name);
      setFieldNames(Object.fromEntries(Object.entries(by).map(([k, v]) => [k, v.slice(0, 3).join(" / ") + (v.length > 3 ? " …" : "")])));
    });
  }, [s.source]);
  // What a filter on an id level can be picked from.
  const [pick, setPick] = useState<Record<string, { id: string; name: string }[]>>({});
  // The report names the ids it shows (paths too); the lists fill in while
  // it loads.
  const nameOf = (dim: string, key: string) => (idDims.includes(dim) && (report?.names?.[key] || names[key])) || key;
  // What each custom field holds, from the traffic sources' parameters:
  // "Field 4 · Placement". Sources may use one field for different things.
  const [fieldNames, setFieldNames] = useState<Record<string, string>>({});
  const label = useCallback((dim: string) => (fieldNames[dim] ? `${dimLabel[dim]} · ${fieldNames[dim]}` : dimLabel[dim] ?? dim), [fieldNames]);

  const loadViews = useCallback(() => {
    api.GET("/v1/brands/{brand}/report-views", { params: { path: { brand: pickedBrandId() } } }).then(({ data }) => setViews((data?.data ?? []) as View[]));
  }, []);
  useEffect(loadViews, [loadViews]);

  // A view's settings replace the page's.
  const applyView = (v: View | undefined) => {
    if (!v) {
      set({ view: "" });
      return;
    }
    const sp = v.spec;
    const filters: Filter[] = [...(sp.filters ?? []).map((f) => split(f, false)), ...(sp.excludes ?? []).map((f) => split(f, true))].filter((f): f is Filter => !!f);
    const preset = (sp.date_range?.preset as Preset | undefined) ?? (sp.date_range?.from ? "" : defaults.preset);
    setS({
      view: v.id,
      source: sp.source === "tracker" ? "tracker" : "site",
      preset,
      from: sp.date_range?.from ?? "",
      to: sp.date_range?.to ?? "",
      tz: sp.timezone ?? "",
      conv: sp.conversion_time === "conversion" ? "conversion" : "visit",
      groups: sp.group_by?.length ? sp.group_by : [defaultGroup(sp.source === "tracker" ? "tracker" : "site")],
      filters,
      sort: sp.sort ?? defaults.sort,
      columns: (sp.columns ?? []).map((c) => ({ name: c.name, formula: c.formula })),
    });
    track("ui.report_view_opened", { shared: v.shared, mine: v.mine });
  };
  const specOf = (): ViewSpec => ({
    source: s.source,
    group_by: s.groups,
    filters: s.filters.filter((f) => !f.not).map((f) => `${f.dim}:${f.value}`),
    excludes: s.filters.filter((f) => f.not).map((f) => `${f.dim}:${f.value}`),
    sort: s.sort,
    conversion_time: s.conv,
    timezone: s.tz || undefined,
    date_range: s.preset ? { preset: s.preset } : { from: s.from, to: s.to },
    columns: s.columns,
  });
  const current = views.find((v) => v.id === s.view);
  const saveAs = async () => {
    const r = await ask({ title: "Save view", input: { label: "Name", maxLength: 120 }, choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Save", value: "ok", variant: "primary" }] });
    if (r?.choice !== "ok" || !r.value.trim()) return;
    const { data, error } = await api.POST("/v1/brands/{brand}/report-views", { params: { path: { brand: pickedBrandId() } }, body: { name: r.value, spec: specOf() } });
    if (error) return toast(errorMessage(error), "error");
    toast("View saved", "ok");
    loadViews();
    set({ view: (data as View).id });
  };
  const save = async () => {
    if (!current) return saveAs();
    const { error } = await api.PATCH("/v1/brands/{brand}/report-views/{id}", { params: { path: { brand: pickedBrandId(), id: current.id } }, body: { spec: specOf() } });
    if (error) return toast(errorMessage(error), "error");
    toast("View saved", "ok");
    loadViews();
  };
  const share = async () => {
    if (!current) return;
    const { error } = await api.PATCH("/v1/brands/{brand}/report-views/{id}", { params: { path: { brand: pickedBrandId(), id: current.id } }, body: { shared: !current.shared } });
    if (error) return toast(errorMessage(error), "error");
    loadViews();
  };
  const remove = async () => {
    if (!current) return;
    if (!(await confirmAction(`Delete "${current.name}"?`, "The saved view goes for everyone it is shared with, and so does any dashboard tile that shows it. The data stays.", "Delete"))) return;
    const { error } = await api.DELETE("/v1/brands/{brand}/report-views/{id}", { params: { path: { brand: pickedBrandId(), id: current.id } } });
    if (error) return toast(errorMessage(error), "error");
    set({ view: "" });
    toast("View deleted", "ok");
    loadViews();
  };

  const download = async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/drilldown", { params: { path: { brand: pickedBrandId() }, query: { ...query, format: "csv" } as never }, parseAs: "text" });
    if (error) return toast(errorMessage(error), "error");
    downloadCsv(csvName("report", ...s.groups, from, to), data as unknown as string);
    track("ui.report_downloaded", { levels: s.groups.length });
  };

  const currency = report?.currency ?? "USD";
  const sortBy = (key: string) => {
    // The first click sorts largest first (ClickFlare).
    set({ sort: s.sort === "-" + key ? key : "-" + key });
    track("ui.report_sorted", { metric: metricCols.some((m) => m.key === key) ? key : "custom" });
  };

  return (
    <div className="space-y-4">
      <PageHeader
        title="Report"
        description="Visits, conversions, revenue and cost, grouped up to five levels deep. Double-click a row to drill in."
        action={
          <div className="flex flex-wrap items-center gap-2">
            <select aria-label="View" className={field} value={s.view} onChange={(e) => applyView(views.find((v) => v.id === e.target.value))}>
              <option value="">No saved view</option>
              {views.some((v) => v.mine && !v.shared) && (
                <optgroup label="Mine">
                  {views.filter((v) => v.mine && !v.shared).map((v) => (
                    <option key={v.id} value={v.id}>{v.name}</option>
                  ))}
                </optgroup>
              )}
              {views.some((v) => v.shared) && (
                <optgroup label="Shared">
                  {views.filter((v) => v.shared).map((v) => (
                    <option key={v.id} value={v.id}>{v.name}</option>
                  ))}
                </optgroup>
              )}
            </select>
            {canManage && <Button onClick={() => void save()}>{current ? "Save view" : "Save as…"}</Button>}
            {canManage && (
              <Menu
                items={[
                  !!current && { label: "Save as…", onClick: () => void saveAs() },
                  !!current && (current.mine || !current.shared) && { label: current.shared ? "Stop sharing" : "Share with the team", onClick: () => void share() },
                  !!current && { label: "Delete view", onClick: () => void remove(), danger: true },
                ]}
              />
            )}
            <CopyButton text={window.location.href} label="Copy link" />
          </div>
        }
      />

      <div className="flex flex-wrap items-center gap-2">
        <select
          aria-label="Report on"
          className={field}
          value={s.source}
          onChange={(e) => {
            const source = e.target.value as Source;
            set({ source, groups: [defaultGroup(source)], filters: [], view: "" });
            setOpen({});
          }}
        >
          <option value="site">Site visits</option>
          <option value="tracker">Tracker clicks</option>
        </select>
        <select aria-label="Dates" className={field} value={s.preset || "custom"} onChange={(e) => (e.target.value === "custom" ? set({ preset: "", from, to }) : set({ preset: e.target.value as Preset }))}>
          {Object.entries(presetLabels).map(([k, label]) => (
            <option key={k} value={k}>{label}</option>
          ))}
          <option value="custom">Custom</option>
        </select>
        {!s.preset && (
          <>
            <input aria-label="From" type="date" className={field} value={s.from} onChange={(e) => set({ from: e.target.value })} />
            <input aria-label="To" type="date" className={field} value={s.to} onChange={(e) => set({ to: e.target.value })} />
          </>
        )}
        <select aria-label="Time zone" className={field} value={s.tz} onChange={(e) => set({ tz: e.target.value })}>
          <option value="">{brandTz} (the brand's)</option>
          {zones.filter((z) => z !== brandTz).map((z) => (
            <option key={z} value={z}>{z}</option>
          ))}
        </select>
        <select aria-label="Count conversions at" className={field} value={s.conv} onChange={(e) => set({ conv: e.target.value as State["conv"] })}>
          <option value="visit">Conversions at {s.source === "tracker" ? "click" : "visit"} time</option>
          <option value="conversion">Conversions at conversion time</option>
        </select>
      </div>

      <Labels.Provider value={label}>
        <GroupBy source={s.source} groups={s.groups} onChange={(groups) => { set({ groups }); setOpen({}); track("ui.report_grouped", { levels: groups.length }); }} />
        <Filters source={s.source} filters={s.filters} pick={pick} nameOf={nameOf} onChange={(filters) => set({ filters })} />
      </Labels.Provider>

      {error && <ErrorText>{error}</ErrorText>}
      {/* A column a link or an old view carries can fail the whole report: never without a way out. */}
      {error && s.columns.length > 0 && (
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <span className="text-muted-foreground">This report has {s.columns.length === 1 ? "a custom column" : `${s.columns.length} custom columns`} ({s.columns.map((c) => c.name).join(", ")}).</span>
          <Button variant="ghost" onClick={() => set({ columns: [] })}>Remove {s.columns.length === 1 ? "it" : "them"}</Button>
        </div>
      )}
      {loading && !report ? (
        <Loading />
      ) : report ? (
        <>
          <Stats cols={6}>
            <Stat label="Visits" value={format(report.totals.visits, "count", currency)} />
            <Stat label="Conversions" value={format(report.totals.conversions, "count", currency)} />
            <Stat label="Revenue" value={format(report.totals.revenue, "money", currency)} />
            <Stat label="Cost" value={format(report.totals.cost, "money", currency)} sub={report.cost_available ? undefined : "Not split this way"} />
            <Stat label="Profit" value={format(report.totals.profit, "money", currency)} tone={(report.totals.profit ?? 0) < 0 ? "red" : (report.totals.profit ?? 0) > 0 ? "green" : undefined} />
            <Stat label="ROI" value={format(report.totals.roi, "percent", currency)} />
          </Stats>
          {report.cost_note && report.rows.length > 0 && <p className="text-xs text-muted-foreground">{report.cost_note}</p>}
          {(report.unconverted_currencies?.length ?? 0) > 0 && <Unconverted currencies={report.unconverted_currencies} to={report.currency} />}
          {report.rows.length === 0 ? (
            s.filters.length > 0 ? (
              <Empty title="Nothing matches these filters" action={<Button variant="ghost" onClick={() => set({ filters: [] })}>Clear filters</Button>}>No {s.source === "tracker" ? "clicks" : "visits"} in these dates match every filter above.</Empty>
            ) : s.source === "tracker" ? (
              <Empty title="No clicks in these dates">Clicks on your tracker campaigns' links show here with their conversions and cost. Widen the dates, or finish Tracker → Setup if no campaign is running yet.</Empty>
            ) : (
              <Empty title="No visits in these dates">Add the site script and tag your links, and visits, conversions and cost show here. For clicks on tracker campaigns, pick Tracker clicks above.</Empty>
            )
          ) : (
            <>
            {columnsOpen && (
              <Card title="Custom columns" action={<Button variant="ghost" onClick={() => setColumnsOpen(false)}>Done</Button>}>
                <p className="text-sm text-muted-foreground">Add a column of your own to the table below: a formula over the metrics (visits, visitors, conversions, revenue, cost, profit, roi, roas, cr, epv, cpa, and for tracker clicks lander_views, lander_clicks, lander_ctr) with + − × ÷ and brackets, such as revenue / visitors.</p>
                <Columns tracker={s.source === "tracker"} columns={s.columns} onChange={(columns) => { set({ columns }); track("ui.report_columns_changed", { custom: columns.length }); }} />
              </Card>
            )}
            <Card
              title={`${report.group_by.map(label).join(" › ")}`}
              action={
                <div className="flex gap-2">
                  <Button variant="ghost" aria-expanded={columnsOpen} onClick={() => setColumnsOpen(!columnsOpen)}>Columns</Button>
                  <Button variant="ghost" onClick={() => void download()}>Download CSV</Button>
                </div>
              }
            >
              <ReportTable
                label={label}
                nameOf={nameOf}
                report={report}
                columns={s.columns}
                sort={s.sort}
                onSort={sortBy}
                open={open}
                onToggle={(k) => setOpen((o) => ({ ...o, [k]: !o[k] }))}
                onDrill={(row) => {
                  // The row becomes a filter and the grouping moves down a level.
                  const filters = [...s.filters, ...row.keys.map((v, i) => ({ dim: s.groups[i], value: v, not: false }))];
                  const groups = s.groups.slice(row.keys.length);
                  set({ filters, groups: groups.length ? groups : [s.groups[s.groups.length - 1]] });
                  setOpen({});
                  track("ui.report_drilled", { level: row.keys.length });
                }}
              />
              {report.truncated && <p className="mt-2 text-xs text-muted-foreground">More rows than shown: filter or drill in to see the rest. The totals count them all.</p>}
            </Card>
            </>
          )}
        </>
      ) : null}
    </div>
  );
}

function split(f: string, not: boolean): Filter | null {
  const i = f.indexOf(":");
  return i > 0 ? { dim: f.slice(0, i), value: f.slice(i + 1), not } : null;
}

// Labels names a level for the screen: the fields carry what the traffic
// sources called them.
const Labels = createContext<(dim: string) => string>((dim) => dimLabel[dim] ?? dim);

function DimSelect({ source, value, onChange, label }: { source: Source; value: string; onChange: (v: string) => void; label: string }) {
  const name = useContext(Labels);
  return (
    <select aria-label={label} className={field} value={value} onChange={(e) => onChange(e.target.value)}>
      {!value && <option value="">Pick…</option>}
      {groupsOf(source).map((g) => (
        <optgroup key={g.label} label={g.label}>
          {g.dims.map(([k]) => (
            <option key={k} value={k}>{name(k)}</option>
          ))}
        </optgroup>
      ))}
    </select>
  );
}

// GroupBy: up to five level chips, each a searchable dimension list, moved
// left or right and removed with ×.
function GroupBy({ source, groups, onChange }: { source: Source; groups: string[]; onChange: (g: string[]) => void }) {
  const label = useContext(Labels);
  const move = (i: number, d: number) => {
    const g = [...groups];
    [g[i], g[i + d]] = [g[i + d], g[i]];
    onChange(g);
  };
  return (
    <div className="flex flex-wrap items-center gap-2" aria-label="Group by">
      <span className="text-sm text-muted-foreground">Group by</span>
      {groups.map((g, i) => (
        <span key={i} className="inline-flex items-center gap-1 rounded-md border px-1 py-0.5">
          {i > 0 && <button type="button" aria-label="Move left" className="px-1 text-muted-foreground" onClick={() => move(i, -1)}>‹</button>}
          <DimSelect source={source} label={`Level ${i + 1}`} value={g} onChange={(v) => onChange(groups.map((x, j) => (j === i ? v : x)))} />
          {i < groups.length - 1 && <button type="button" aria-label="Move right" className="px-1 text-muted-foreground" onClick={() => move(i, 1)}>›</button>}
          {groups.length > 1 && <button type="button" aria-label={`Remove ${label(g)}`} className="px-1 text-muted-foreground" onClick={() => onChange(groups.filter((_, j) => j !== i))}>×</button>}
        </span>
      ))}
      {groups.length < 5 && (
        <Button variant="ghost" onClick={() => onChange([...groups, groupsOf(source).flatMap((x) => x.dims).map(([k]) => k).find((k) => !groups.includes(k)) ?? "country"])}>
          + Level
        </Button>
      )}
    </div>
  );
}

function Filters({ source, filters, onChange, pick, nameOf }: { source: Source; filters: Filter[]; onChange: (f: Filter[]) => void; pick: Record<string, { id: string; name: string }[]>; nameOf: (dim: string, key: string) => string }) {
  const label = useContext(Labels);
  const [dim, setDim] = useState("country");
  const [not, setNot] = useState(false);
  const [value, setValue] = useState("");
  const options = source === "tracker" ? pick[dim] : undefined;
  // Codes are stored in capitals, so "us" means US.
  const tidy = (v: string) => (dim === "country" ? v.trim().toUpperCase() : v.trim());
  return (
    <div className="flex flex-wrap items-center gap-2">
      {filters.map((f, i) => (
        <span key={i} className="inline-flex items-center gap-1 rounded-md border bg-muted/40 px-2 py-0.5 text-sm">
          {label(f.dim)} {f.not ? "is not" : "is"} <b>{f.value ? nameOf(f.dim, f.value) : "(none)"}</b>
          <button type="button" aria-label="Remove filter" className="px-1 text-muted-foreground" onClick={() => onChange(filters.filter((_, j) => j !== i))}>×</button>
        </span>
      ))}
      <form
        className="inline-flex flex-wrap items-center gap-1"
        onSubmit={(e) => {
          e.preventDefault();
          if (options && !value) return;
          onChange([...filters, { dim, value: tidy(value), not }]);
          setValue("");
          track("ui.report_filtered", { not });
        }}
      >
        <DimSelect source={source} label="Filter dimension" value={dim} onChange={(d) => { setDim(d); setValue(""); }} />
        <select aria-label="Filter operator" className={field} value={not ? "not" : "is"} onChange={(e) => setNot(e.target.value === "not")}>
          <option value="is">is</option>
          <option value="not">is not</option>
        </select>
        {options ? (
          <select aria-label="Filter value" className={field} value={value} onChange={(e) => setValue(e.target.value)}>
            <option value="">Pick…</option>
            {options.map((o) => (
              <option key={o.id} value={o.id}>{o.name}</option>
            ))}
          </select>
        ) : (
          <input aria-label="Filter value" className={field} placeholder={dim === "country" ? "Code, such as US" : "Value"} value={value} onChange={(e) => setValue(e.target.value)} />
        )}
        <Button type="submit" variant="ghost">+ Filter</Button>
      </form>
    </div>
  );
}

// formulaError says what is wrong with a custom column's formula before it
// is sent: one bad column makes every later report fail. "" when it reads.
export function formulaError(formula: string, tracker: boolean): string {
  // The server's own grammar (drilldown_formula.go): metrics and numbers
  // joined by + - * /, with brackets. Read here the same way, so nothing
  // the server would refuse is ever added.
  const known = metricCols.filter((m) => tracker || !m.tracker).map((m) => m.key as string);
  const s = formula;
  if (!s.trim() || s.length > 200) return "A formula is 1 to 200 characters.";
  let pos = 0;
  const skip = () => {
    while (pos < s.length && /\s/.test(s[pos])) pos++;
  };
  const atom = (): string => {
    skip();
    if (pos >= s.length) return "The formula ends early: put a metric or a number after the last sign.";
    const ch = s[pos];
    if (ch === "(") {
      pos++;
      const inner = expr();
      if (inner) return inner;
      skip();
      if (s[pos] !== ")") return "A bracket isn't closed.";
      pos++;
      return "";
    }
    if (/[a-z]/.test(ch)) {
      const start = pos;
      while (pos < s.length && /[a-z_]/.test(s[pos])) pos++;
      const name = s.slice(start, pos);
      return known.includes(name) ? "" : `"${name}" isn't a metric. Use ${known.join(", ")}.`;
    }
    if (/[0-9.]/.test(ch)) {
      const start = pos;
      while (pos < s.length && /[0-9.]/.test(s[pos])) pos++;
      const n = s.slice(start, pos);
      return /^(\d+\.?\d*|\.\d+)$/.test(n) ? "" : `"${n}" isn't a number.`;
    }
    return `"${ch}" can't be used there: a formula takes metric names, numbers, + - * / and brackets.`;
  };
  const term = (): string => {
    let problem = atom();
    while (!problem) {
      skip();
      if (s[pos] !== "*" && s[pos] !== "/") break;
      pos++;
      problem = atom();
    }
    return problem;
  };
  function expr(): string {
    let problem = term();
    while (!problem) {
      skip();
      if (s[pos] !== "+" && s[pos] !== "-") break;
      pos++;
      problem = term();
    }
    return problem;
  }
  const problem = expr();
  if (problem) return problem;
  skip();
  return pos < s.length ? `"${s.slice(pos).trim()}" isn't expected there.` : "";
}

function Columns({ columns, onChange, tracker }: { columns: Column[]; onChange: (c: Column[]) => void; tracker: boolean }) {
  const [name, setName] = useState("");
  const [formula, setFormula] = useState("");
  const [problem, setProblem] = useState("");
  return (
    <div className="space-y-3 p-4">
      {columns.map((c, i) => (
        <div key={c.name} className="flex items-center justify-between gap-2 rounded-md border px-3 py-2 text-sm">
          <span>
            <b>{c.name}</b> <span className="font-mono text-muted-foreground">= {c.formula}</span>
          </span>
          <button type="button" aria-label={`Remove ${c.name}`} className="text-muted-foreground" onClick={() => onChange(columns.filter((_, j) => j !== i))}>×</button>
        </div>
      ))}
      {columns.length < 10 && (
        <form
          className="space-y-2"
          onSubmit={(e) => {
            e.preventDefault();
            const n = name.trim().toLowerCase().replace(/[^a-z0-9_]/g, "_");
            if (!/^[a-z][a-z0-9_]{0,39}$/.test(n) || !formula.trim()) return setProblem("A column needs a name that starts with a letter (letters, numbers and _), and a formula.");
            if (columns.some((c) => c.name === n)) return setProblem(`There is already a column named ${n}.`);
            // Metric names are lower case; "Revenue / Visits" means the same.
            const f = formula.trim().toLowerCase();
            const bad = formulaError(f, tracker);
            if (bad) return setProblem(bad);
            onChange([...columns, { name: n, formula: f }]);
            setName("");
            setFormula("");
            setProblem("");
          }}
        >
          <input aria-label="Column name" className={`${field} w-full`} placeholder="Name, such as rpv" value={name} onChange={(e) => setName(e.target.value)} />
          <input aria-label="Formula" aria-invalid={!!problem} className={`${field} w-full font-mono`} placeholder="revenue / visits" value={formula} onChange={(e) => { setFormula(e.target.value); setProblem(""); }} />
          {problem && <p role="alert" className="text-xs text-red-600 dark:text-red-400">{problem}</p>}
          <Button type="submit">+ Custom column</Button>
        </form>
      )}
    </div>
  );
}

// ReportTable: a tree. Rows at deeper levels nest under a chevron; the name
// column and header stay put; a double-click or Enter drills in.
function ReportTable({ label, nameOf, report, columns, sort, onSort, open, onToggle, onDrill }: {
  label: (dim: string) => string;
  nameOf: (dim: string, key: string) => string;
  report: Report;
  columns: Column[];
  sort: string;
  onSort: (key: string) => void;
  open: Record<string, boolean>;
  onToggle: (key: string) => void;
  onDrill: (row: Row) => void;
}) {
  const currency = report.currency;
  const heads = [
    ...metricCols.filter((m) => !m.tracker || report.source === "tracker").map((m) => ({ key: m.key as string, label: m.label, kind: m.kind, title: m.title })),
    ...columns.map((c) => ({ key: c.name, label: c.name, kind: "decimal" as const, title: c.formula as string | undefined })),
  ];
  const value = (m: Metrics, key: string) => (key in m ? (m as unknown as Record<string, number | null>)[key] : m.custom?.[key]);
  const arrow = (key: string) => (sort === "-" + key ? " ↓" : sort === key ? " ↑" : "");
  const render = (rows: Row[], depth: number): React.ReactNode =>
    rows.map((row) => {
      const k = row.keys.join("\u0000");
      const kids = row.rows.length > 0;
      const isOpen = !!open[k];
      return (
        <Fragment key={k}>
          <tr
            tabIndex={0}
            className="border-t hover:bg-muted/40 focus:bg-muted/40 focus:outline-none"
            onDoubleClick={() => onDrill(row)}
            onKeyDown={(e) => {
              if (e.key === "Enter") onDrill(row);
            }}
          >
            <td className="sticky left-0 min-w-56 max-w-96 truncate whitespace-nowrap bg-background py-1.5 pr-3" title={row.keys[row.keys.length - 1] ? nameOf(report.group_by[row.keys.length - 1], row.keys[row.keys.length - 1]) : undefined} style={{ paddingLeft: 8 + depth * 18 }}>
              {kids ? (
                <button type="button" aria-label={isOpen ? "Collapse" : "Expand"} aria-expanded={isOpen} className="mr-1 w-4 text-muted-foreground" onClick={() => onToggle(k)}>
                  {isOpen ? "▾" : "▸"}
                </button>
              ) : (
                <span className="mr-1 inline-block w-4" />
              )}
              {(row.keys[row.keys.length - 1] && nameOf(report.group_by[row.keys.length - 1], row.keys[row.keys.length - 1])) || <span className="text-muted-foreground">(none)</span>}
            </td>
            {heads.map((h) => {
              const v = value(row.metrics, h.key);
              return (
                <td key={h.key} className={`px-3 py-1.5 text-right font-mono tabular-nums ${tone(h.key, v)}`}>
                  {format(v, h.kind, currency)}
                </td>
              );
            })}
          </tr>
          {kids && isOpen && render(row.rows, depth + 1)}
        </Fragment>
      );
    });
  return (
    <>
      <div className="hidden max-h-[70vh] overflow-auto md:block">
        <table className="w-full text-sm">
          <thead className="sticky top-0 z-10 bg-background">
            <tr className="text-left text-xs text-muted-foreground">
              <th className="sticky left-0 min-w-56 bg-background py-2 pl-2 pr-3 font-medium">{report.group_by.map(label).join(" › ")}</th>
              {heads.map((h) => (
                <th key={h.key} title={h.title} aria-sort={sort === "-" + h.key ? "descending" : sort === h.key ? "ascending" : "none"} className="whitespace-nowrap px-3 py-2 text-right font-medium">
                  <button type="button" className="hover:text-foreground" onClick={() => onSort(h.key)}>
                    {h.label}
                    {arrow(h.key)}
                  </button>
                </th>
              ))}
            </tr>
            <tr className="border-t font-medium">
              <td className="sticky left-0 bg-background py-1.5 pl-2 pr-3">Total</td>
              {heads.map((h) => (
                <td key={h.key} className={`px-3 py-1.5 text-right font-mono tabular-nums ${tone(h.key, value(report.totals, h.key))}`}>
                  {format(value(report.totals, h.key), h.kind, currency)}
                </td>
              ))}
            </tr>
          </thead>
          <tbody>{render(report.rows as Row[], 0)}</tbody>
        </table>
      </div>
      <div className="space-y-2 md:hidden">
        {(report.rows as Row[]).map((row) => (
          <button type="button" key={row.keys.join("\u0000")} className="block w-full rounded-md border p-3 text-left" onClick={() => onDrill(row)}>
            <div className="font-medium">{(row.keys[row.keys.length - 1] && nameOf(report.group_by[row.keys.length - 1], row.keys[row.keys.length - 1])) || "(none)"}</div>
            <div className="mt-1 grid grid-cols-2 gap-1 text-xs text-muted-foreground">
              <span>Profit <b className={tone("profit", row.metrics.profit)}>{format(row.metrics.profit, "money", currency)}</b></span>
              <span>Visits <b>{format(row.metrics.visits, "count", currency)}</b></span>
              <span>Conversions <b>{format(row.metrics.conversions, "count", currency)}</b></span>
              <span>ROI <b className={tone("roi", row.metrics.roi)}>{format(row.metrics.roi, "percent", currency)}</b></span>
            </div>
          </button>
        ))}
      </div>
    </>
  );
}
