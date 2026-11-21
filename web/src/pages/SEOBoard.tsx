import { useEffect, useMemo, useState } from "react";
import type { components } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { PickBrand } from "@/components/pick-brand";
import { WorldMap } from "@/components/share-charts";
import { seriesColor } from "@/components/chart-kit";
import { api, errorMessage } from "../api";
import { Badge, Button, Empty, ErrorText, Loading } from "../ui";
import type { Tab } from "../App";
import { toAlpha2 } from "./SearchPerformance";
import { track } from "../lib/analytics";
import { SEOMoney } from "./SEOMoney";

// The SEO board: a brand's search money at a glance, in tiles like a
// seller's profit dashboard. Today, yesterday, the month so far, the
// month's forecast and last month, each with organic revenue, payments,
// sign-ups and Search Console clicks against the period it is usually
// compared with, and a coloured change. Picking a tile shows the pages
// and the countries behind it. It reads only what is stored, so opening
// it costs nothing. The picked tile stays in the address.

type Board = components["schemas"]["SEOBoard"];
type Metrics = components["schemas"]["SEOBoardMetrics"];
type Period = Board["periods"][number];
type PeriodID = Period["id"];
type Page = Board["pages"][number];
type Props = { ws: string; go: (tab: Tab, item?: string) => void };

const ids: PeriodID[] = ["today", "yesterday", "month_to_date", "month_forecast", "last_month"];
const titles: Record<PeriodID, string> = {
  today: "Today",
  yesterday: "Yesterday",
  month_to_date: "Month to date",
  month_forecast: "This month (forecast)",
  last_month: "Last month",
};

const money = (v: number | null | undefined) => (v === null || v === undefined ? "–" : v.toLocaleString("en-US", { style: "currency", currency: "USD", maximumFractionDigits: Math.abs(v) < 1000 ? 2 : 0 }));
const count = (v: number | null | undefined) => (v === null || v === undefined ? "–" : Math.round(v).toLocaleString("en-US"));
const percent = (v: number | null | undefined) => (v === null || v === undefined ? "–" : `${(v * 100).toLocaleString("en-US", { maximumFractionDigits: 1 })}%`);
const regionName = (code: string) => {
  try {
    return new Intl.DisplayNames(["en"], { type: "region" }).of(code) ?? code;
  } catch {
    return code;
  }
};
const position = (v: number | null | undefined) => (v === null || v === undefined ? "–" : v.toLocaleString("en-US", { maximumFractionDigits: 1 }));
const shortDay = (d: string) => new Date(d + "T00:00:00Z").toLocaleDateString(undefined, { day: "2-digit", month: "2-digit", year: "numeric", timeZone: "UTC" });
const span = (from: string, to: string) => (from === to ? shortDay(from) : `${shortDay(from)} – ${shortDay(to)}`);

// change is the relative change, or null when either side is missing.
function change(cur: number | null | undefined, prev: number | null | undefined) {
  if (cur === null || cur === undefined || prev === null || prev === undefined) return null;
  if (prev === 0) return cur === 0 ? 0 : null;
  return (cur - prev) / Math.abs(prev);
}

function Change({ value, lowerIsBetter }: { value: number | null; lowerIsBetter?: boolean }) {
  if (value === null) return null;
  const good = lowerIsBetter ? value < 0 : value > 0;
  const tone = Math.abs(value) < 0.005 ? "grey" : good ? "green" : "red";
  return <Badge tone={tone}>{`${value > 0 ? "+" : value < 0 ? "−" : "±"}${(Math.abs(value) * 100).toFixed(1)}%`}</Badge>;
}

type Row = { key: keyof Metrics; label: string; show: (v: number | null | undefined) => string; lowerIsBetter?: boolean };
const mainRows: Row[] = [
  { key: "revenue", label: "Organic revenue", show: money },
  { key: "payments", label: "Payments", show: count },
  { key: "signups", label: "Sign-ups", show: count },
  { key: "clicks", label: "Search clicks", show: count },
];
const moreRows: Row[] = [
  { key: "organic_sessions", label: "Organic sessions", show: count },
  { key: "impressions", label: "Impressions", show: count },
  { key: "ctr", label: "CTR", show: percent },
  // A rate is not scaled, so the forecast's is the month so far.
  { key: "position", label: "Avg. position", show: position, lowerIsBetter: true },
];

function periodFromURL(): PeriodID {
  const p = new URLSearchParams(location.search).get("period");
  return ids.includes(p as PeriodID) ? (p as PeriodID) : "month_to_date";
}

export function SEOBoard({ ws, go }: Props) {
  const { brand, active } = useBrand(ws);
  const [period, setPeriod] = useState<PeriodID>(periodFromURL);
  const [board, setBoard] = useState<Board | null>(null);
  const [error, setError] = useState("");
  const [more, setMore] = useState(false);

  const pick = (p: PeriodID) => {
    const url = new URL(location.href);
    if (p === "month_to_date") url.searchParams.delete("period");
    else url.searchParams.set("period", p);
    history.replaceState(history.state, "", url);
    setPeriod(p);
    track("ui.seo_board_period_picked", { period: p });
  };

  useEffect(() => {
    if (!brand) return;
    let live = true;
    setError("");
    api
      .GET("/v1/brands/{brand}/seo/board", { params: { path: { brand: brand.id }, query: { period } } })
      .then(({ data, error }) => {
        if (!live) return;
        if (error || !data) return setError(errorMessage(error));
        setBoard(data);
      })
      .catch((err: unknown) => {
        if (live && !(err instanceof Error && err.name === "AbortError")) setError(errorMessage(undefined));
      });
    return () => {
      live = false;
    };
  }, [brand?.id, period]);

  if (!brand) return <PickBrand what="Search money and pages" brands={active.length} />;
  if (error) return <ErrorText>{error}</ErrorText>;
  if (!board) return <Loading />;

  const nothing = board.search_through === null && board.conversions_through === null;
  if (nothing)
    return (
      <Empty title="Nothing stored yet" action={<Button onClick={() => go("connectors")}>Connect a tool</Button>}>
        Connect Google Search Console for clicks and PostHog for organic sign-ups, payments and revenue. Each runs on your own account; numbers show up here after their first sync.
      </Empty>
    );

  return (
    <div className="space-y-6">
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-5">
        {board.periods.map((p, i) => (
          <Tile key={p.id} p={p} color={seriesColor(i)} picked={p.id === period} more={more} onPick={() => pick(p.id)} />
        ))}
      </div>
      <div className="flex flex-wrap items-center justify-between gap-2 text-[12px] text-muted-foreground">
        <span>
          Search Console through <span className="font-medium text-foreground">{board.search_through ? shortDay(board.search_through) : "–"}</span>, conversions through{" "}
          <span className="font-medium text-foreground">{board.conversions_through ? shortDay(board.conversions_through) : "–"}</span>. Dates in {board.timezone}.
        </span>
        <button type="button" className="text-[13px] text-foreground underline-offset-2 hover:underline" onClick={() => setMore((m) => !m)}>
          {more ? "Fewer numbers" : "More numbers"}
        </button>
      </div>
      <Details board={board} period={period} />
      <SEOMoney brand={brand.id} />
      <ul className="list-disc space-y-1 pl-5 text-[12px] text-muted-foreground">
        {board.notes.map((n) => (
          <li key={n}>{n}</li>
        ))}
      </ul>
    </div>
  );
}

function Tile({ p, color, picked, more, onPick }: { p: Period; color: string; picked: boolean; more: boolean; onPick: () => void }) {
  const rows = more ? [...mainRows, ...moreRows] : mainRows;
  return (
    <button
      type="button"
      onClick={onPick}
      aria-pressed={picked}
      className={"overflow-hidden rounded-lg border bg-card text-left transition-shadow hover:shadow-sm " + (picked ? "ring-2 ring-offset-1" : "")}
      style={picked ? ({ "--tw-ring-color": color } as React.CSSProperties) : undefined}
    >
      <div className="px-3 py-2.5" style={{ background: `color-mix(in srgb, ${color} 16%, transparent)`, borderBottom: `2px solid ${color}` }}>
        <div className="text-[14px] font-semibold">{titles[p.id]}</div>
        <div className="mt-0.5 flex justify-between gap-2 text-[11px] tabular-nums text-muted-foreground">
          <span>{span(p.from, p.to)}</span>
          <span>{span(p.compare_from, p.compare_to)}</span>
        </div>
      </div>
      <div className="divide-y px-3">
        {rows.map((r) => {
          const cur = p.metrics[r.key];
          const prev = p.previous[r.key];
          return (
            <div key={r.key} className="py-2">
              <div className="flex items-center gap-1.5 text-[12px] text-muted-foreground">
                {r.label}
                <Change value={change(cur, prev)} lowerIsBetter={r.lowerIsBetter} />
              </div>
              <div className="mt-0.5 flex items-baseline justify-between gap-2 tabular-nums">
                <span className="text-[15px] font-semibold">{r.show(cur)}</span>
                <span className="text-[12px] text-muted-foreground">{r.show(prev)}</span>
              </div>
            </div>
          );
        })}
      </div>
    </button>
  );
}

type Col = { key: keyof Page | "clicks_change" | "revenue_change"; label: string; show: (p: Page) => string; num: (p: Page) => number | null };
const cols: Col[] = [
  { key: "clicks", label: "Clicks", show: (p) => count(p.clicks), num: (p) => p.clicks },
  { key: "clicks_change", label: "Clicks vs before", show: (p) => fmtChange(change(p.clicks, p.previous_clicks)), num: (p) => change(p.clicks, p.previous_clicks) },
  { key: "impressions", label: "Impressions", show: (p) => count(p.impressions), num: (p) => p.impressions },
  { key: "ctr", label: "CTR", show: (p) => percent(p.ctr), num: (p) => p.ctr },
  { key: "position", label: "Position", show: (p) => position(p.position), num: (p) => p.position },
  { key: "organic_sessions", label: "Organic sessions", show: (p) => count(p.organic_sessions), num: (p) => p.organic_sessions },
  { key: "signups", label: "Sign-ups", show: (p) => count(p.signups), num: (p) => p.signups },
  { key: "payments", label: "Payments", show: (p) => count(p.payments), num: (p) => p.payments },
  { key: "revenue", label: "Revenue", show: (p) => money(p.revenue), num: (p) => p.revenue },
  { key: "revenue_change", label: "Revenue vs before", show: (p) => fmtChange(change(p.revenue, p.previous_revenue)), num: (p) => change(p.revenue, p.previous_revenue) },
];
function fmtChange(v: number | null) {
  return v === null ? "–" : `${v > 0 ? "+" : v < 0 ? "−" : "±"}${(Math.abs(v) * 100).toFixed(0)}%`;
}

type Country = Board["countries"][number];
const duration = (v: number | null | undefined) => {
  if (v === null || v === undefined) return "–";
  const m = Math.floor(v / 60);
  const sec = Math.round(v % 60);
  return m ? `${m}m ${sec}s` : `${sec}s`;
};
// What the map can shade countries by. "Clicks, no payments" shows only
// countries whose search clicks brought no payment in the dates.
const measures: { id: string; label: string; value: (c: Country) => number | null; show: (v: number) => string }[] = [
  { id: "revenue", label: "Revenue", value: (c) => c.revenue_usd || null, show: money },
  { id: "payments", label: "Payments", value: (c) => c.payments || null, show: count },
  { id: "clicks", label: "Clicks", value: (c) => c.clicks || null, show: count },
  { id: "unpaid", label: "Clicks, no payments", value: (c) => (c.payments === 0 && c.clicks > 0 ? c.clicks : null), show: count },
  { id: "impressions", label: "Impressions", value: (c) => c.impressions || null, show: count },
  { id: "rpc", label: "Revenue per click", value: (c) => (c.clicks > 0 && c.revenue_usd > 0 ? c.revenue_usd / c.clicks : null), show: money },
  { id: "sessions", label: "Organic sessions", value: (c) => c.organic_sessions || null, show: count },
  { id: "time", label: "Time on site", value: (c) => c.avg_session_seconds ?? null, show: duration },
  { id: "signups", label: "Sign-ups", value: (c) => c.signups || null, show: count },
];

function Details({ board, period }: { board: Board; period: PeriodID }) {
  const [view, setView] = useState<"pages" | "map">("pages");
  const [measure, setMeasure] = useState("revenue");
  const [csort, setCsort] = useState<{ id: string; desc: boolean }>({ id: "revenue", desc: true });
  const [sort, setSort] = useState<{ key: Col["key"]; desc: boolean }>({ key: "revenue", desc: true });
  const [q, setQ] = useState("");
  const hosts = new Set(board.pages.map((p) => p.host)).size;

  const rows = useMemo(() => {
    const col = cols.find((c) => c.key === sort.key) ?? cols[0];
    const shown = board.pages.filter((p) => !q || (p.host + p.path).toLowerCase().includes(q.toLowerCase()));
    return [...shown].sort((a, b) => {
      const x = col.num(a), y = col.num(b);
      if (x === y) return 0;
      if (x === null) return 1;
      if (y === null) return -1;
      return sort.desc ? y - x : x - y;
    });
  }, [board.pages, sort, q]);

  const download = () => {
    const head = ["Page", ...cols.map((c) => c.label)];
    const lines = rows.map((p) => [(hosts > 1 ? p.host : "") + p.path, ...cols.map((c) => c.num(p) ?? "")]);
    const csv = [head, ...lines].map((l) => l.map((v) => `"${String(v).replaceAll('"', '""')}"`).join(",")).join("\n");
    const a = document.createElement("a");
    a.href = URL.createObjectURL(new Blob([csv], { type: "text/csv" }));
    a.download = `seo-pages-${period}.csv`;
    a.click();
    URL.revokeObjectURL(a.href);
    track("ui.seo_board_pages_downloaded", { period, rows: rows.length });
  };

  const m = measures.find((x) => x.id === measure) ?? measures[0];
  const parts = board.countries
    .map((c) => ({ label: toAlpha2.get(c.country) ?? "", value: m.value(c) }))
    .filter((p): p is { label: string; value: number } => !!p.label && p.value !== null)
    .sort((a, b) => b.value - a.value);
  const byCode = new Map(board.countries.map((c) => [toAlpha2.get(c.country) ?? "", c]));
  const sortBy = measures.find((x) => x.id === csort.id) ?? measures[0];
  const countryRows = [...board.countries]
    .filter((c) => toAlpha2.has(c.country))
    .sort((a, b) => ((sortBy.value(a) ?? -1) - (sortBy.value(b) ?? -1)) * (csort.desc ? -1 : 1));

  return (
    <section className="rounded-lg border bg-card">
      <div className="flex flex-wrap items-center gap-3 border-b px-4 py-2">
        <span className="text-[15px] font-semibold">{titles[period]}</span>
        <div className="flex gap-1">
          {(
            [
              ["pages", "Pages"],
              ["map", "Map"],
            ] as const
          ).map(([id, text]) => (
            <button
              type="button"
              key={id}
              onClick={() => setView(id)}
              className={"border-b-2 px-2 py-1.5 text-[13px] " + (view === id ? "border-foreground font-medium" : "border-transparent text-muted-foreground hover:text-foreground")}
            >
              {text}
            </button>
          ))}
        </div>
        <div className="ml-auto flex items-center gap-2">
          {view === "pages" ? (
            <>
              <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search pages" aria-label="Search pages" className="h-8 w-56 rounded-md border bg-background px-2 text-[13px]" />
              <Button variant="ghost" onClick={download} disabled={!rows.length}>
                Download CSV
              </Button>
            </>
          ) : (
            <label className="flex items-center gap-2 text-[13px]">
              <span className="text-muted-foreground">Shade by</span>
              <select
                value={measure}
                onChange={(e) => {
                  setMeasure(e.target.value);
                  track("ui.seo_board_map_measured", { measure: e.target.value });
                }}
                className="h-8 rounded-md border bg-background px-2"
              >
                {measures.map((x) => (
                  <option key={x.id} value={x.id}>
                    {x.label}
                  </option>
                ))}
              </select>
            </label>
          )}
        </div>
      </div>
      {view === "map" ? (
        <div className="p-4">
          {parts.length ? (
            <WorldMap
              parts={parts}
              format={(v) => (typeof v === "number" ? m.show(v) : "–")}
              detail={(code) => {
                const c = byCode.get(code);
                if (!c) return null;
                return (
                  <div className="mt-1 grid grid-cols-[auto_auto] gap-x-3 text-[12px] tabular-nums">
                    <span className="text-muted-foreground">Clicks</span>
                    <span>{count(c.clicks)}</span>
                    <span className="text-muted-foreground">Impressions</span>
                    <span>{count(c.impressions)}</span>
                    <span className="text-muted-foreground">Sessions</span>
                    <span>{count(c.organic_sessions)}</span>
                    <span className="text-muted-foreground">Time on site</span>
                    <span>{duration(c.avg_session_seconds)}</span>
                    <span className="text-muted-foreground">Sign-ups</span>
                    <span>{count(c.signups)}</span>
                    <span className="text-muted-foreground">Payments</span>
                    <span>{count(c.payments)}</span>
                    <span className="text-muted-foreground">Revenue</span>
                    <span>{money(c.revenue_usd)}</span>
                  </div>
                );
              }}
            />
          ) : (
            <p className="text-[13px] text-muted-foreground">Nothing to shade by {m.label.toLowerCase()} for these dates.</p>
          )}
          {countryRows.length > 0 && (
            <div className="mt-4 max-h-[420px] overflow-auto">
              <table className="w-full text-[13px]">
                <thead className="sticky top-0 bg-card">
                  <tr className="border-b text-left text-[12px] text-muted-foreground">
                    <th className="px-3 py-2 font-medium">Country</th>
                    {measures.map((x) => (
                      <th key={x.id} className="whitespace-nowrap px-3 py-2 text-right font-medium">
                        <button type="button" className="hover:text-foreground" onClick={() => setCsort((s) => ({ id: x.id, desc: s.id === x.id ? !s.desc : true }))}>
                          {x.label}
                          {csort.id === x.id ? (csort.desc ? " ↓" : " ↑") : ""}
                        </button>
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {countryRows.map((c) => (
                    <tr key={c.country} className="border-b last:border-0">
                      <td className="px-3 py-1.5">{regionName(toAlpha2.get(c.country) ?? c.country)}</td>
                      {measures.map((x) => {
                        const v = x.value(c);
                        return (
                          <td key={x.id} className={"px-3 py-1.5 text-right tabular-nums " + (x.id === "unpaid" && v ? "text-red-700 dark:text-red-400" : "")}>
                            {v === null ? "–" : x.show(v)}
                          </td>
                        );
                      })}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      ) : rows.length ? (
        <div className="overflow-x-auto">
          <table className="w-full text-[13px]">
            <thead>
              <tr className="border-b text-left text-[12px] text-muted-foreground">
                <th className="px-4 py-2 font-medium">Page</th>
                {cols.map((c) => (
                  <th key={c.key} className="whitespace-nowrap px-3 py-2 text-right font-medium">
                    <button type="button" className="hover:text-foreground" onClick={() => setSort((s) => ({ key: c.key, desc: s.key === c.key ? !s.desc : true }))}>
                      {c.label}
                      {sort.key === c.key ? (sort.desc ? " ↓" : " ↑") : ""}
                    </button>
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map((p) => (
                <tr key={p.host + p.path} className="border-b last:border-0 hover:bg-muted/40">
                  <td className="max-w-[360px] truncate px-4 py-2" title={p.host + p.path}>
                    {hosts > 1 && <span className="text-muted-foreground">{p.host}</span>}
                    {p.path}
                  </td>
                  {cols.map((c) => {
                    const v = c.num(p);
                    const tone = c.key.endsWith("_change") && v !== null && Math.abs(v) >= 0.005 ? (v > 0 ? "text-green-700 dark:text-green-400" : "text-red-700 dark:text-red-400") : "";
                    return (
                      <td key={c.key} className={"whitespace-nowrap px-3 py-2 text-right tabular-nums " + tone}>
                        {c.show(p)}
                      </td>
                    );
                  })}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <p className="p-4 text-[13px] text-muted-foreground">No pages stored for these dates yet.</p>
      )}
    </section>
  );
}
