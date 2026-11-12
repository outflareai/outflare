// Tracker → Report (plans/ad-tracker-ui.md B1): the drill-down report over
// the brand's visits, credited conversions, revenue and ad spend, grouped by
// up to five levels, with saved views and custom columns. Every list of the
// tracker is this report with its first level fixed (Voluum's "every list is
// a report"); the whole view lives in the URL, so it can be shared or opened
// in a new tab.

import { Fragment, useCallback, useEffect, useMemo, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { PickBrand } from "@/components/pick-brand";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { pickedBrandId, useBrand } from "@/lib/brand";
import { csvName, downloadCsv } from "@/lib/csv";
import { track } from "@/lib/analytics";
import { api, errorMessage } from "../api";
import { ask, Button, Card, CopyButton, Empty, ErrorText, field, Loading, Menu, PageHeader, Stat, Stats, toast } from "../ui";
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
const dimLabel: Record<string, string> = Object.fromEntries(dimensionGroups.flatMap((g) => g.dims));

// The metric columns, bold ones on by default (B1's table).
const metricCols: { key: keyof Omit<Metrics, "custom">; label: string; kind: "count" | "money" | "ratio" | "percent" }[] = [
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
];

// State, as it lives in the URL.
type State = { view: string; preset: Preset | ""; from: string; to: string; tz: string; conv: "visit" | "conversion"; groups: string[]; filters: Filter[]; sort: string; columns: Column[] };
const defaults: State = { view: "", preset: "last_7_days", from: "", to: "", tz: "", conv: "visit", groups: ["channel"], filters: [], sort: "-visits", columns: [] };

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
  if (q.get("conversion_time") === "conversion") s.conv = "conversion";
  const g = q.get("group_by");
  if (g !== null) s.groups = g.split(",").filter((x) => x in dimLabel).slice(0, 5);
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
  if (s.conv !== "visit") q.set("conversion_time", s.conv);
  if (s.groups.join(",") !== defaults.groups.join(",")) q.set("group_by", s.groups.join(","));
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

export function format(v: number | null | undefined, kind: "count" | "money" | "ratio" | "percent", currency: string): string {
  if (v === null || v === undefined) return "—";
  switch (kind) {
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

// Profit and ROI are green above zero and red below; the sign stays, so
// colour is never the only signal.
const tone = (key: string, v: number | null | undefined) =>
  (key === "profit" || key === "roi") && v !== null && v !== undefined && v !== 0 ? (v > 0 ? "text-emerald-600 dark:text-emerald-400" : "text-red-600 dark:text-red-400") : "";

export function TrackerReport(props: Props) {
  const { brand, active } = useBrand(props.ws);
  if (!brand) return <PickBrand what="the tracker report" brands={active.length} />;
  return <ReportPage {...props} brandTz={brand.timezone ?? "UTC"} />;
}

function ReportPage({ canManage, brandTz }: Props & { brandTz: string }) {
  const [s, setS] = useState<State>(() => readState(window.location.search));
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
  useEffect(() => {
    const q = writeState(s);
    window.history.replaceState(null, "", window.location.pathname + (q ? "?" + q : ""));
  }, [s]);

  const query = useMemo(() => {
    const q: Record<string, unknown> = { from, to, group_by: s.groups.join(","), sort: s.sort, conversion_time: s.conv };
    if (s.tz) q.timezone = s.tz;
    const filters = s.filters.filter((f) => !f.not).map((f) => `${f.dim}:${f.value}`);
    const excludes = s.filters.filter((f) => f.not).map((f) => `${f.dim}:${f.value}`);
    if (filters.length) q.filter = filters;
    if (excludes.length) q.exclude = excludes;
    if (s.columns.length) q.column = s.columns.map((c) => `${c.name}:${c.formula}`);
    return q;
  }, [from, to, s.groups, s.sort, s.conv, s.tz, s.filters, s.columns]);

  const load = useCallback(() => {
    setLoading(true);
    api
      .GET("/v1/brands/{brand}/drilldown", { params: { path: { brand: pickedBrandId() }, query: query as never } })
      .then(({ data, error }) => {
        if (error) setError(errorMessage(error));
        else {
          setError("");
          setReport(data as Report);
        }
      })
      .finally(() => setLoading(false));
  }, [query]);
  useEffect(load, [load]);

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
      preset,
      from: sp.date_range?.from ?? "",
      to: sp.date_range?.to ?? "",
      tz: sp.timezone ?? "",
      conv: sp.conversion_time === "conversion" ? "conversion" : "visit",
      groups: sp.group_by?.length ? sp.group_by : defaults.groups,
      filters,
      sort: sp.sort ?? defaults.sort,
      columns: (sp.columns ?? []).map((c) => ({ name: c.name, formula: c.formula })),
    });
    track("ui.report_view_opened", { shared: v.shared, mine: v.mine });
  };
  const specOf = (): ViewSpec => ({
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
    const { error } = await api.DELETE("/v1/brands/{brand}/report-views/{id}", { params: { path: { brand: pickedBrandId(), id: current.id } } });
    if (error) return toast(errorMessage(error), "error");
    set({ view: "" });
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
          <option value="visit">Conversions at visit time</option>
          <option value="conversion">Conversions at conversion time</option>
        </select>
      </div>

      <GroupBy groups={s.groups} onChange={(groups) => { set({ groups }); setOpen({}); track("ui.report_grouped", { levels: groups.length }); }} />
      <Filters filters={s.filters} onChange={(filters) => set({ filters })} />

      {error && <ErrorText>{error}</ErrorText>}
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
          {report.cost_note && <p className="text-xs text-muted-foreground">{report.cost_note}</p>}
          {report.rows.length === 0 ? (
            <Empty title="No visits in these dates">Add the site script and tag your links in Setup, and visits, conversions and cost show here.</Empty>
          ) : (
            <Card
              title={`${report.group_by.map((g) => dimLabel[g] ?? g).join(" › ")}`}
              action={
                <div className="flex gap-2">
                  <Button variant="ghost" onClick={() => setColumnsOpen(true)}>Columns</Button>
                  <Button variant="ghost" onClick={() => void download()}>Download CSV</Button>
                </div>
              }
            >
              <ReportTable
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
          )}
        </>
      ) : null}

      <Sheet open={columnsOpen} onOpenChange={setColumnsOpen}>
        <SheetContent className="w-full sm:max-w-md">
          <SheetHeader>
            <SheetTitle>Custom columns</SheetTitle>
            <SheetDescription>A formula over the metrics (visits, visitors, conversions, revenue, cost, profit, roi, roas, cr, epv, cpa) with + − × ÷ and brackets, such as revenue / visitors.</SheetDescription>
          </SheetHeader>
          <Columns columns={s.columns} onChange={(columns) => { set({ columns }); track("ui.report_columns_changed", { custom: columns.length }); }} />
        </SheetContent>
      </Sheet>
    </div>
  );
}

function split(f: string, not: boolean): Filter | null {
  const i = f.indexOf(":");
  return i > 0 ? { dim: f.slice(0, i), value: f.slice(i + 1), not } : null;
}

function DimSelect({ value, onChange, label }: { value: string; onChange: (v: string) => void; label: string }) {
  return (
    <select aria-label={label} className={field} value={value} onChange={(e) => onChange(e.target.value)}>
      {!value && <option value="">Pick…</option>}
      {dimensionGroups.map((g) => (
        <optgroup key={g.label} label={g.label}>
          {g.dims.map(([k, l]) => (
            <option key={k} value={k}>{l}</option>
          ))}
        </optgroup>
      ))}
    </select>
  );
}

// GroupBy: up to five level chips, each a searchable dimension list, moved
// left or right and removed with ×.
function GroupBy({ groups, onChange }: { groups: string[]; onChange: (g: string[]) => void }) {
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
          <DimSelect label={`Level ${i + 1}`} value={g} onChange={(v) => onChange(groups.map((x, j) => (j === i ? v : x)))} />
          {i < groups.length - 1 && <button type="button" aria-label="Move right" className="px-1 text-muted-foreground" onClick={() => move(i, 1)}>›</button>}
          {groups.length > 1 && <button type="button" aria-label={`Remove ${dimLabel[g] ?? g}`} className="px-1 text-muted-foreground" onClick={() => onChange(groups.filter((_, j) => j !== i))}>×</button>}
        </span>
      ))}
      {groups.length < 5 && (
        <Button variant="ghost" onClick={() => onChange([...groups, dimensionGroups.flatMap((x) => x.dims).map(([k]) => k).find((k) => !groups.includes(k)) ?? "country"])}>
          + Level
        </Button>
      )}
    </div>
  );
}

function Filters({ filters, onChange }: { filters: Filter[]; onChange: (f: Filter[]) => void }) {
  const [dim, setDim] = useState("country");
  const [not, setNot] = useState(false);
  const [value, setValue] = useState("");
  return (
    <div className="flex flex-wrap items-center gap-2">
      {filters.map((f, i) => (
        <span key={i} className="inline-flex items-center gap-1 rounded-md border bg-muted/40 px-2 py-0.5 text-sm">
          {dimLabel[f.dim] ?? f.dim} {f.not ? "is not" : "is"} <b>{f.value || "(none)"}</b>
          <button type="button" aria-label="Remove filter" className="px-1 text-muted-foreground" onClick={() => onChange(filters.filter((_, j) => j !== i))}>×</button>
        </span>
      ))}
      <form
        className="inline-flex flex-wrap items-center gap-1"
        onSubmit={(e) => {
          e.preventDefault();
          onChange([...filters, { dim, value: value.trim(), not }]);
          setValue("");
          track("ui.report_filtered", { not });
        }}
      >
        <DimSelect label="Filter dimension" value={dim} onChange={setDim} />
        <select aria-label="Filter operator" className={field} value={not ? "not" : "is"} onChange={(e) => setNot(e.target.value === "not")}>
          <option value="is">is</option>
          <option value="not">is not</option>
        </select>
        <input aria-label="Filter value" className={field} placeholder="Value" value={value} onChange={(e) => setValue(e.target.value)} />
        <Button type="submit" variant="ghost">+ Filter</Button>
      </form>
    </div>
  );
}

function Columns({ columns, onChange }: { columns: Column[]; onChange: (c: Column[]) => void }) {
  const [name, setName] = useState("");
  const [formula, setFormula] = useState("");
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
            if (!/^[a-z][a-z0-9_]{0,39}$/.test(n) || !formula.trim()) return toast("A column needs a name (letters, numbers, _) and a formula", "error");
            onChange([...columns, { name: n, formula: formula.trim() }]);
            setName("");
            setFormula("");
          }}
        >
          <input aria-label="Column name" className={`${field} w-full`} placeholder="Name, such as rpv" value={name} onChange={(e) => setName(e.target.value)} />
          <input aria-label="Formula" className={`${field} w-full font-mono`} placeholder="revenue / visits" value={formula} onChange={(e) => setFormula(e.target.value)} />
          <Button type="submit">+ Custom column</Button>
        </form>
      )}
    </div>
  );
}

// ReportTable: a tree. Rows at deeper levels nest under a chevron; the name
// column and header stay put; a double-click or Enter drills in.
function ReportTable({ report, columns, sort, onSort, open, onToggle, onDrill }: {
  report: Report;
  columns: Column[];
  sort: string;
  onSort: (key: string) => void;
  open: Record<string, boolean>;
  onToggle: (key: string) => void;
  onDrill: (row: Row) => void;
}) {
  const currency = report.currency;
  const heads = [...metricCols.map((m) => ({ key: m.key as string, label: m.label, kind: m.kind })), ...columns.map((c) => ({ key: c.name, label: c.name, kind: "count" as const }))];
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
            <td className="sticky left-0 bg-background py-1.5 pr-3" style={{ paddingLeft: 8 + depth * 18 }}>
              {kids ? (
                <button type="button" aria-label={isOpen ? "Collapse" : "Expand"} aria-expanded={isOpen} className="mr-1 w-4 text-muted-foreground" onClick={() => onToggle(k)}>
                  {isOpen ? "▾" : "▸"}
                </button>
              ) : (
                <span className="mr-1 inline-block w-4" />
              )}
              {row.keys[row.keys.length - 1] || <span className="text-muted-foreground">(none)</span>}
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
              <th className="sticky left-0 bg-background py-2 pl-2 pr-3 font-medium">{report.group_by.map((g) => dimLabel[g] ?? g).join(" › ")}</th>
              {heads.map((h) => (
                <th key={h.key} className="cursor-pointer whitespace-nowrap px-3 py-2 text-right font-medium" onClick={() => onSort(h.key)}>
                  {h.label}
                  {arrow(h.key)}
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
            <div className="font-medium">{row.keys[row.keys.length - 1] || "(none)"}</div>
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
