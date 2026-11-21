import { Fragment, useEffect, useMemo, useState } from "react";
import type { components } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { PickBrand } from "@/components/pick-brand";
import { api, errorMessage } from "../api";
import { Badge, Button, Empty, ErrorText, Loading } from "../ui";
import type { Tab } from "../App";
import { track } from "../lib/analytics";
import { ImportTargets, intents, pageTypes, Passport, statusText, statusTone } from "./SEOTargetPassport";

// The target report: every page's target keyword and how it ranks, on
// one screen. Search Console position on the page for the exact keyword
// over 28 days with its change and a 12-week line, the tracked rank by
// country, other pages Google shows for the same keyword, and whether the
// keyword is in the title, H1 and description. The summary tiles filter
// the table; a row opens its secondary keywords, ranks and competing
// pages. Targets are set on Keywords, Targets. Page and keyword are
// columns of their own: each column sorts, and the row under the headers
// filters by page, keyword, status, intent, page type and band.

type Report = components["schemas"]["SEOTargetReport"];
type Row = Report["targets"][number];
type Kw = components["schemas"]["SEOTargetKeyword"];
type Props = { ws: string; go: (tab: Tab, item?: string) => void };

type Filter =
  | "all"
  | "top3"
  | "top10"
  | "striking"
  | "beyond"
  | "none"
  | "improved"
  | "declined"
  | "competing"
  | "missing_on_page"
  | "untracked"
  | "on_target"
  | "cannibalized"
  | "wrong_intent"
  | "missing_content"
  | "no_summary";
const statusTiles: { id: Filter; label: string; hint: string }[] = [
  { id: "on_target", label: "On target", hint: "Nothing to fix" },
  { id: "cannibalized", label: "Cannibalized", hint: "Another page of ours ranks for the keyword instead" },
  { id: "wrong_intent", label: "Wrong intent", hint: "The page type rarely answers the keyword's intent" },
  { id: "missing_content", label: "Missing content", hint: "Keyword not in the title or H1, or a secondary keyword not in the summary" },
  { id: "no_summary", label: "No summary", hint: "No summary of what the page holds" },
];
const tiles: { id: Filter; label: string; hint: string }[] = [
  { id: "all", label: "Targets", hint: "Pages with a target keyword" },
  { id: "top3", label: "Top 3", hint: "Ranks 1 to 3" },
  { id: "top10", label: "Top 10", hint: "Ranks 4 to 10" },
  { id: "striking", label: "11–20", hint: "Striking distance: page 2" },
  { id: "beyond", label: "21+", hint: "Below page 2" },
  { id: "none", label: "No data", hint: "No impressions and no tracked rank" },
  { id: "improved", label: "Improved", hint: "Position up by 1 or more vs the 28 days before" },
  { id: "declined", label: "Declined", hint: "Position down by 1 or more vs the 28 days before" },
  { id: "competing", label: "Competing", hint: "Another page of ours shows for the keyword" },
  { id: "missing_on_page", label: "Not in title/H1", hint: "Keyword missing from the title or H1" },
  { id: "untracked", label: "Not tracked", hint: "The rank tracker doesn't check the keyword" },
];

const bandTone: Record<Row["band"], "green" | "blue" | "amber" | "grey" | "red"> = { top3: "green", top10: "blue", striking: "amber", beyond: "red", none: "grey" };
const bandText: Record<Row["band"], string> = { top3: "Top 3", top10: "Top 10", striking: "11–20", beyond: "21+", none: "No data" };

const pos = (v: number | null | undefined) => (v === null || v === undefined ? "–" : v.toLocaleString("en-US", { maximumFractionDigits: 1 }));
const num = (v: number) => Math.round(v).toLocaleString("en-US");
const pathOf = (u: string) => {
  try {
    const x = new URL(u);
    return x.pathname + x.search;
  } catch {
    return u;
  }
};

// Delta shows a position change: positive means it moved up the results.
function Delta({ cur, prev }: { cur: number | null | undefined; prev: number | null | undefined }) {
  if (cur === null || cur === undefined || prev === null || prev === undefined) return null;
  const d = prev - cur;
  if (Math.abs(d) < 0.1) return <span className="text-[11px] text-muted-foreground">±0</span>;
  return <span className={"text-[11px] tabular-nums " + (d > 0 ? "text-green-700 dark:text-green-400" : "text-red-700 dark:text-red-400")}>{`${d > 0 ? "▲" : "▼"}${Math.abs(d).toFixed(1)}`}</span>;
}

// Line draws positions by week, oldest first; up is better.
function Line({ values }: { values: (number | null)[] }) {
  const pts = values.map((v, i) => [i, v] as const).filter((p): p is readonly [number, number] => p[1] !== null);
  if (pts.length < 2) return <span className="text-[11px] text-muted-foreground">–</span>;
  const w = 72, h = 20;
  const ys = pts.map((p) => p[1]);
  const lo = Math.min(...ys), hi = Math.max(...ys);
  const x = (i: number) => (i / (values.length - 1)) * w;
  const y = (v: number) => (hi === lo ? h / 2 : ((v - lo) / (hi - lo)) * (h - 2) + 1);
  const d = pts.map((p, k) => `${k ? "L" : "M"}${x(p[0]).toFixed(1)},${y(p[1]).toFixed(1)}`).join(" ");
  const better = pts[pts.length - 1][1] <= pts[0][1];
  return (
    <svg width={w} height={h} viewBox={`0 0 ${w} ${h}`} aria-label={`Position by week, from ${pos(pts[0][1])} to ${pos(pts[pts.length - 1][1])}`}>
      <path d={d} fill="none" strokeWidth={1.5} className={better ? "stroke-green-600" : "stroke-red-600"} />
    </svg>
  );
}

function Checks({ c }: { c: Row["checks"] }) {
  if (c.in_title === null) return <span className="text-[11px] text-muted-foreground">Not crawled</span>;
  const item = (ok: boolean | null, label: string) => (
    <span title={`${label}: ${ok ? "has the keyword" : "missing the keyword"}`} className={"rounded px-1 text-[11px] " + (ok ? "bg-green-100 text-green-800 dark:bg-green-900/40 dark:text-green-300" : "bg-red-100 text-red-800 dark:bg-red-900/40 dark:text-red-300")}>
      {label}
    </span>
  );
  return (
    <span className="flex gap-1">
      {item(c.in_title, "T")}
      {item(c.in_h1, "H1")}
      {item(c.in_description, "D")}
    </span>
  );
}

// bestTracked is the newest tracked rank with the best position.
function bestTracked(r: Row) {
  const newest = r.tracked.reduce((a, t) => (t.day && t.day > a ? t.day : a), "");
  return r.tracked.filter((t) => t.day === newest).sort((a, b) => (a.position ?? 999) - (b.position ?? 999))[0];
}

function matches(r: Row, f: Filter) {
  switch (f) {
    case "all":
      return true;
    case "improved":
    case "declined":
      return r.trend === f;
    case "competing":
      return r.competing.length > 0;
    case "missing_on_page":
      return r.checks.in_title !== null && (!r.checks.in_title || !r.checks.in_h1);
    case "untracked":
      return r.tracked.length === 0;
    case "no_summary":
      return r.summary_length === 0;
    case "on_target":
    case "cannibalized":
    case "wrong_intent":
    case "missing_content":
      return r.status === f;
    default:
      return r.band === f;
  }
}

type SortKey = "page" | "keyword" | "status" | "intent" | "type" | "band" | "position" | "change" | "clicks" | "impressions" | "tracked";

// Column filters: text for page and keyword, a pick for the rest ("" is any).
type Cols = { page: string; keyword: string; status: "" | Row["status"]; intent: string; type: string; band: "" | Row["band"] };
const noCols: Cols = { page: "", keyword: "", status: "", intent: "", type: "", band: "" };
const bandOrder: Record<Row["band"], number> = { top3: 0, top10: 1, striking: 2, beyond: 3, none: 4 };
// Sorting a text column starts A to Z, a position column best first, a number column biggest first.
const startsAscending = (k: SortKey) => ["page", "keyword", "status", "intent", "type", "band", "position", "tracked"].includes(k);
const NOT_SET = "(not set)";

function matchesCols(r: Row, c: Cols) {
  const page = c.page.trim().toLowerCase();
  const kw = c.keyword.trim().toLowerCase();
  if (page && !r.url.toLowerCase().includes(page)) return false;
  if (kw && !r.primary.keyword.toLowerCase().includes(kw) && !r.secondary.some((s) => s.keyword.toLowerCase().includes(kw))) return false;
  if (c.status && r.status !== c.status) return false;
  if (c.intent && (c.intent === NOT_SET ? r.intent !== "" : r.intent !== c.intent)) return false;
  if (c.type && (c.type === NOT_SET ? r.page_type !== "" : r.page_type !== c.type)) return false;
  if (c.band && r.band !== c.band) return false;
  return true;
}

export function SEOTargets({ ws, go }: Props) {
  const { brand, active } = useBrand(ws);
  const [report, setReport] = useState<Report | null>(null);
  const [error, setError] = useState("");
  const [filter, setFilter] = useState<Filter>("all");
  const [open, setOpen] = useState<string | null>(null);
  const [q, setQ] = useState("");
  const [cols, setCols] = useState<Cols>(noCols);
  const [sort, setSort] = useState<{ key: SortKey; desc: boolean }>({ key: "impressions", desc: true });
  const [importing, setImporting] = useState(false);
  const [reload, setReload] = useState(0);

  useEffect(() => {
    if (!brand) return;
    let live = true;
    if (!reload) setReport(null);
    setError("");
    api
      .GET("/v1/brands/{brand}/seo/targets", { params: { path: { brand: brand.id } } })
      .then(({ data, error }) => {
        if (!live) return;
        if (error || !data) return setError(errorMessage(error));
        setReport(data);
      })
      .catch((err: unknown) => {
        if (live && !(err instanceof Error && err.name === "AbortError")) setError(errorMessage(undefined));
      });
    return () => {
      live = false;
    };
  }, [brand?.id, reload]);

  const rows = useMemo(() => {
    if (!report) return [];
    const val = (r: Row): number | string | null => {
      switch (sort.key) {
        case "page":
          return pathOf(r.url).toLowerCase();
        case "keyword":
          return r.primary.keyword.toLowerCase();
        case "status":
          return statusText[r.status];
        case "intent":
          return r.intent || null;
        case "type":
          return r.page_type || null;
        case "band":
          return bandOrder[r.band];
        case "position":
          return r.primary.current.position;
        case "change":
          return r.primary.current.position !== null && r.primary.previous.position !== null ? r.primary.previous.position - r.primary.current.position : null;
        case "clicks":
          return r.primary.current.clicks;
        case "impressions":
          return r.primary.current.impressions;
        case "tracked":
          return bestTracked(r)?.position ?? null;
      }
    };
    const needle = q.trim().toLowerCase();
    return report.targets
      .filter((r) => matches(r, filter))
      .filter((r) => matchesCols(r, cols))
      .filter(
        (r) =>
          !needle ||
          r.url.toLowerCase().includes(needle) ||
          r.primary.keyword.includes(needle) ||
          r.secondary.some((s) => s.keyword.includes(needle)) ||
          r.intent === needle ||
          r.page_type === needle,
      )
      .sort((a, b) => {
        const x = val(a), y = val(b);
        if (x === y) return a.url.localeCompare(b.url);
        if (x === null) return 1;
        if (y === null) return -1;
        const c = typeof x === "string" ? x.localeCompare(String(y)) : x - (y as number);
        return sort.desc ? -c : c;
      });
  }, [report, filter, q, sort, cols]);

  if (!brand) return <PickBrand what="Target keywords and rankings" brands={active.length} />;
  if (error) return <ErrorText>{error}</ErrorText>;
  if (!report) return <Loading />;
  if (!report.sites.length)
    return (
      <Empty title="No SEO site yet" action={<Button onClick={() => go("keywords")}>Open Keywords</Button>}>
        Add the brand's website as an SEO site, then set the keyword each page should rank for on Keywords, Targets.
      </Empty>
    );
  const importer = (
    <ImportTargets brand={brand.id} sites={report.sites} open={importing} onClose={() => setImporting(false)} onDone={() => setReload((n) => n + 1)} />
  );
  if (!report.targets.length)
    return (
      <>
        <Empty
          title="No target keywords yet"
          action={
            <div className="flex gap-2">
              <Button onClick={() => setImporting(true)}>Import a keyword map</Button>
              <Button variant="ghost" onClick={() => go("keywords")}>
                Set targets one by one
              </Button>
            </div>
          }
        >
          Import a CSV of pages with their keywords, intent, page type and summary, or set the keyword each page should rank for on Keywords, Targets. With Search Console connected,
          Outflare suggests one for every page.
        </Empty>
        {importer}
      </>
    );

  const count = (f: Filter) => (f === "all" ? report.summary.targets : report.summary[f]);
  const tile = (t: { id: Filter; label: string; hint: string }) => (
    <button
      type="button"
      key={t.id}
      title={t.hint}
      aria-pressed={filter === t.id}
      onClick={() => {
        setFilter(t.id);
        track("ui.seo_targets_filtered", { filter: t.id });
      }}
      className={"rounded-lg border bg-card px-3 py-2 text-left transition-shadow hover:shadow-sm " + (filter === t.id ? "ring-2 ring-foreground/60" : "")}
    >
      <div className="text-[11px] text-muted-foreground">{t.label}</div>
      <div className="text-[18px] font-semibold tabular-nums">{count(t.id)}</div>
    </button>
  );
  const multi = report.sites.length > 1;
  const noSource = report.sites.filter((s) => !s.source);
  const head = (key: SortKey, label: string, right = true) => (
    <th className={"whitespace-nowrap px-3 py-2 font-medium " + (right ? "text-right" : "text-left")} aria-sort={sort.key === key ? (sort.desc ? "descending" : "ascending") : undefined}>
      <button
        type="button"
        title={`Sort by ${label.toLowerCase()}`}
        className="hover:text-foreground"
        onClick={() => setSort((s) => ({ key, desc: s.key === key ? !s.desc : !startsAscending(key) }))}
      >
        {label}
        {sort.key === key ? (sort.desc ? " ↓" : " ↑") : ""}
      </button>
    </th>
  );
  const colsOn = (Object.keys(cols) as (keyof Cols)[]).filter((k) => cols[k] !== "");
  const setCol = <K extends keyof Cols>(k: K, v: Cols[K]) => {
    setCols((c) => ({ ...c, [k]: v }));
    if (v !== "" && (k !== "page" && k !== "keyword" ? true : cols[k] === "")) track("ui.seo_targets_column_filtered", { column: k });
  };
  const field = "h-7 w-full min-w-0 rounded-md border bg-background px-1.5 text-[12px] font-normal text-foreground";
  const textFilter = (k: "page" | "keyword", label: string) => (
    <th className="px-3 pb-2 pt-0">
      <input value={cols[k]} onChange={(e) => setCol(k, e.target.value)} placeholder={`Filter ${label}`} aria-label={`Filter by ${label}`} className={field} />
    </th>
  );
  const pick = (k: "status" | "intent" | "type" | "band", label: string, options: { value: string; label: string }[]) => (
    <th className="px-3 pb-2 pt-0">
      <select value={cols[k]} onChange={(e) => setCol(k, e.target.value as Cols[typeof k])} aria-label={`Filter by ${label}`} className={field + (cols[k] ? "" : " text-muted-foreground")}>
        <option value="">Any</option>
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
    </th>
  );

  return (
    <div className="space-y-5">
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-5">{statusTiles.map(tile)}</div>
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-4 lg:grid-cols-11">{tiles.map(tile)}</div>
      {importer}

      {noSource.length > 0 && (
        <p className="text-[12px] text-muted-foreground">
          No Search Console data for {noSource.map((s) => s.host).join(", ")}: connect it on{" "}
          <button type="button" className="underline underline-offset-2" onClick={() => go("connectors", "google_search_console")}>
            Connectors
          </button>
          .
        </p>
      )}

      <section className="rounded-lg border bg-card">
        <div className="flex flex-wrap items-center gap-3 border-b px-4 py-2">
          <span className="text-[15px] font-semibold">{[...statusTiles, ...tiles].find((t) => t.id === filter)?.label}</span>
          <span className="text-[12px] text-muted-foreground">{rows.length} pages</span>
          {colsOn.length > 0 && (
            <button type="button" className="text-[12px] underline underline-offset-2" onClick={() => setCols(noCols)}>
              Clear {colsOn.length === 1 ? "column filter" : `${colsOn.length} column filters`}
            </button>
          )}
          <div className="ml-auto flex items-center gap-2">
            <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search pages or keywords" aria-label="Search pages or keywords" className="h-8 w-56 rounded-md border bg-background px-2 text-[13px]" />
            <Button variant="ghost" onClick={() => setImporting(true)}>
              Import CSV
            </Button>
            <Button variant="ghost" onClick={() => go("keywords")}>
              Edit targets
            </Button>
          </div>
        </div>
        <div className="overflow-x-auto">
          <table className="w-full text-[13px]">
            <thead>
              <tr className="border-b text-[12px] text-muted-foreground">
                {head("page", "Page", false)}
                {head("keyword", "Keyword", false)}
                {head("status", "Status", false)}
                {head("intent", "Intent", false)}
                {head("type", "Page type", false)}
                {head("band", "Band", false)}
                {head("position", "Position")}
                {head("change", "vs 28 days before")}
                <th className="px-3 py-2 text-right font-medium">7 days</th>
                <th className="px-3 py-2 text-left font-medium">12 weeks</th>
                {head("clicks", "Clicks")}
                {head("impressions", "Impressions")}
                {head("tracked", "Tracked rank")}
                <th className="px-3 py-2 text-left font-medium">On page</th>
                <th className="px-3 py-2 text-right font-medium">Competing</th>
              </tr>
              <tr className="border-b">
                {textFilter("page", "page")}
                {textFilter("keyword", "keyword")}
                {pick("status", "status", (Object.keys(statusText) as Row["status"][]).map((v) => ({ value: v, label: statusText[v] })))}
                {pick("intent", "intent", [...intents.map((v) => ({ value: v, label: v })), { value: NOT_SET, label: "Not set" }])}
                {pick("type", "page type", [...pageTypes.map((v) => ({ value: v, label: v })), { value: NOT_SET, label: "Not set" }])}
                {pick("band", "band", (Object.keys(bandText) as Row["band"][]).map((v) => ({ value: v, label: bandText[v] })))}
                <th colSpan={9} />
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => {
                const p = r.primary;
                const best = bestTracked(r);
                const isOpen = open === r.id;
                return (
                  <Fragment key={r.id}>
                    <tr
                      className="cursor-pointer border-b hover:bg-muted/40"
                      onClick={() => {
                        setOpen(isOpen ? null : r.id);
                        if (!isOpen) track("ui.seo_target_opened", { secondary: r.secondary.length, tracked: r.tracked.length });
                      }}
                    >
                      <td className="max-w-[280px] px-3 py-2">
                        <div className="truncate font-mono text-[12px]" title={r.url}>
                          {multi && <span className="text-muted-foreground">{r.host}</span>}
                          {pathOf(r.url)}
                        </div>
                      </td>
                      <td className="max-w-[260px] px-3 py-2">
                        <div className="flex items-center gap-1.5">
                          <span className="truncate font-medium" title={p.keyword}>
                            {p.keyword}
                          </span>
                          {r.secondary.length > 0 && (
                            <span className="shrink-0 text-[11px] text-muted-foreground" title={r.secondary.map((s) => s.keyword).join(", ")}>
                              +{r.secondary.length}
                            </span>
                          )}
                        </div>
                      </td>
                      <td className="px-3 py-2">
                        <Badge tone={statusTone[r.status]}>{statusText[r.status]}</Badge>
                      </td>
                      <td className="whitespace-nowrap px-3 py-2 text-[12px] text-muted-foreground">{r.intent || "–"}</td>
                      <td className="whitespace-nowrap px-3 py-2 text-[12px] text-muted-foreground">{r.page_type || "–"}</td>
                      <td className="px-3 py-2">
                        <Badge tone={bandTone[r.band]}>{bandText[r.band]}</Badge>
                      </td>
                      <td className="px-3 py-2 text-right tabular-nums">{pos(p.current.position)}</td>
                      <td className="px-3 py-2 text-right">
                        <Delta cur={p.current.position} prev={p.previous.position} />
                      </td>
                      <td className="px-3 py-2 text-right">
                        <Delta cur={p.week.position} prev={p.last_week.position} />
                      </td>
                      <td className="px-3 py-2">
                        <Line values={p.weekly} />
                      </td>
                      <td className="px-3 py-2 text-right tabular-nums">{num(p.current.clicks)}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{num(p.current.impressions)}</td>
                      <td className="whitespace-nowrap px-3 py-2 text-right tabular-nums">
                        {best ? (
                          <span title={best.url ? `Ranking page: ${best.url}` : "Not in the results read"}>
                            <span className="text-[11px] text-muted-foreground">{best.country} </span>
                            {best.position ?? "–"}{" "}
                            {best.position !== null && best.previous_position !== null && <Delta cur={best.position} prev={best.previous_position} />}
                            {best.position !== null && !best.on_target && <span className="ml-1 text-[11px] text-amber-700 dark:text-amber-400">other page</span>}
                          </span>
                        ) : (
                          <span className="text-[11px] text-muted-foreground">Not tracked</span>
                        )}
                      </td>
                      <td className="px-3 py-2">
                        <Checks c={r.checks} />
                      </td>
                      <td className="px-3 py-2 text-right tabular-nums">{r.competing.length ? <Badge tone="amber">{r.competing.length}</Badge> : "–"}</td>
                    </tr>
                    {isOpen && (
                      <tr className="border-b bg-muted/20">
                        <td colSpan={15} className="space-y-4 px-4 py-3">
                          <Passport r={r} brand={brand.id} onSaved={() => setReload((n) => n + 1)} />
                          <Details r={r} />
                        </td>
                      </tr>
                    )}
                  </Fragment>
                );
              })}
            </tbody>
          </table>
        </div>
      </section>

      <ul className="list-disc space-y-1 pl-5 text-[12px] text-muted-foreground">
        {report.notes.map((n) => (
          <li key={n}>{n}</li>
        ))}
      </ul>
    </div>
  );
}

function KeywordRows({ items }: { items: Kw[] }) {
  return (
    <table className="w-full text-[12px]">
      <thead>
        <tr className="text-muted-foreground">
          <th className="py-1 text-left font-medium">Keyword</th>
          <th className="py-1 text-right font-medium">Position</th>
          <th className="py-1 text-right font-medium">vs before</th>
          <th className="py-1 text-left font-medium">12 weeks</th>
          <th className="py-1 text-right font-medium">Clicks</th>
          <th className="py-1 text-right font-medium">Impressions</th>
        </tr>
      </thead>
      <tbody>
        {items.map((k) => (
          <tr key={k.keyword}>
            <td className="py-1">{k.keyword}</td>
            <td className="py-1 text-right tabular-nums">{pos(k.current.position)}</td>
            <td className="py-1 text-right">
              <Delta cur={k.current.position} prev={k.previous.position} />
            </td>
            <td className="py-1">
              <Line values={k.weekly} />
            </td>
            <td className="py-1 text-right tabular-nums">{num(k.current.clicks)}</td>
            <td className="py-1 text-right tabular-nums">{num(k.current.impressions)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function Details({ r }: { r: Row }) {
  return (
    <div className="grid gap-5 lg:grid-cols-3">
      <div>
        <div className="mb-1 text-[12px] font-semibold">Keywords on this page (Search Console, 28 days)</div>
        <KeywordRows items={[r.primary, ...r.secondary]} />
      </div>
      <div>
        <div className="mb-1 text-[12px] font-semibold">Tracked rank of "{r.primary.keyword}"</div>
        {r.tracked.length ? (
          <table className="w-full text-[12px]">
            <tbody>
              {r.tracked.map((t) => (
                <tr key={t.country + t.device}>
                  <td className="py-1">
                    {t.country} · {t.device}
                  </td>
                  <td className="py-1 text-right tabular-nums">
                    {t.position ?? "Not in results"} {t.position !== null && t.previous_position !== null && <Delta cur={t.position} prev={t.previous_position} />}
                  </td>
                  <td className="py-1 pl-2 text-muted-foreground">{t.day ?? "Not checked yet"}</td>
                  <td className="max-w-[160px] truncate py-1 pl-2 font-mono text-[11px]" title={t.url}>
                    {t.url ? (t.on_target ? "this page" : pathOf(t.url)) : ""}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : (
          <p className="text-[12px] text-muted-foreground">Not tracked. Keywords, Rankings, "Track every target keyword" adds it.</p>
        )}
      </div>
      <div>
        <div className="mb-1 text-[12px] font-semibold">Other pages shown for it</div>
        {r.competing.length ? (
          <table className="w-full text-[12px]">
            <tbody>
              {r.competing.map((c) => (
                <tr key={c.url}>
                  <td className="max-w-[220px] truncate py-1 font-mono text-[11px]" title={c.url}>
                    {pathOf(c.url)}
                  </td>
                  <td className="py-1 text-right tabular-nums">pos {pos(c.position)}</td>
                  <td className="py-1 text-right tabular-nums">{num(c.impressions)} impr.</td>
                  <td className="py-1 text-right tabular-nums">{num(c.clicks)} clicks</td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : (
          <p className="text-[12px] text-muted-foreground">None: only this page shows for the keyword.</p>
        )}
      </div>
    </div>
  );
}
