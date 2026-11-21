import { useEffect, useMemo, useState } from "react";
import { ChevronDown } from "lucide-react";
import type { Schemas } from "@outflare/sdk";
import { api } from "../../api";
import { HoverCard, niceTicks, runs, short, smooth, Swatch, seriesColor, useWidth } from "@/components/chart-kit";
import { DropdownMenu, DropdownMenuContent, DropdownMenuRadioGroup, DropdownMenuRadioItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { openDraft, type PanelGo } from "@/components/insight-panels";
import { track } from "@/lib/analytics";

// AdsChart is Paid ads' chart of the ads over time: four number tiles,
// each showing a metric picked from its menu, and a click on a tile draws
// or hides its line. The tiles and which are drawn are remembered in this
// browser until they are changed.

type TrendSpec = Schemas["TrendSpec"];
type TrendResult = Schemas["TrendResult"];

type Kind = "money" | "count" | "percent";
type Metric = { key: string; label: string; kind: Kind; value: (v: Row) => number | null };
type Row = { spend: number; clicks: number; impressions: number; conversions: number; visits: number };

const over = (a: number, b: number) => (b > 0 ? a / b : null);

export const adsChartMetrics: Metric[] = [
  { key: "clicks", label: "Clicks", kind: "count", value: (v) => v.clicks },
  { key: "cost", label: "Cost", kind: "money", value: (v) => v.spend },
  { key: "avg_cpc", label: "Avg. CPC", kind: "money", value: (v) => over(v.spend, v.clicks) },
  { key: "impressions", label: "Impressions", kind: "count", value: (v) => v.impressions },
  { key: "ctr", label: "Click rate", kind: "percent", value: (v) => over(v.clicks, v.impressions) },
  { key: "conversions", label: "Conversions", kind: "count", value: (v) => v.conversions },
  { key: "cost_per_conversion", label: "Cost per conversion", kind: "money", value: (v) => over(v.spend, v.conversions) },
  { key: "conversion_rate", label: "Conversion rate", kind: "percent", value: (v) => over(v.conversions, v.clicks) },
  { key: "visits", label: "Visits from ads", kind: "count", value: (v) => v.visits },
  { key: "cost_per_visit", label: "Cost per visit", kind: "money", value: (v) => over(v.spend, v.visits) },
];

type Prefs = { tiles: string[]; on: string[] };
const defaults: Prefs = { tiles: ["clicks", "cost", "avg_cpc", "impressions"], on: ["clicks", "cost"] };
const prefsKey = "outflare:ads-chart";

// readAdsChartPrefs is what was last chosen, or the starting choice: any
// part that no longer names a metric falls back to it.
export function readAdsChartPrefs(raw: string | null): Prefs {
  try {
    const p = JSON.parse(raw ?? "") as Partial<Prefs>;
    const known = (k: unknown) => typeof k === "string" && adsChartMetrics.some((m) => m.key === k);
    const tiles = Array.isArray(p.tiles) ? p.tiles.filter(known) : [];
    if (tiles.length !== 4 || new Set(tiles).size !== 4) return defaults;
    return { tiles, on: Array.isArray(p.on) ? p.on.filter((k) => tiles.includes(k)) : defaults.on };
  } catch {
    return defaults;
  }
}

function usePrefs(): [Prefs, (p: Prefs) => void] {
  const [prefs, setPrefs] = useState<Prefs>(() => {
    try {
      return readAdsChartPrefs(localStorage.getItem(prefsKey));
    } catch {
      return defaults;
    }
  });
  return [
    prefs,
    (p) => {
      setPrefs(p);
      try {
        localStorage.setItem(prefsKey, JSON.stringify(p));
      } catch {
        // Private windows: the choice lasts until the page is left.
      }
    },
  ];
}

function show(v: number | null, kind: Kind, currency: string): string {
  if (v === null || !Number.isFinite(v)) return "–";
  if (kind === "percent") return `${(v * 100).toFixed(v < 0.1 ? 2 : 1)}%`;
  if (kind === "count") return Math.round(v).toLocaleString();
  if (currency) {
    try {
      return v.toLocaleString(undefined, { style: "currency", currency });
    } catch {
      // Not a currency code the browser knows: the number with the code after it.
    }
  }
  const n = v.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
  return currency ? `${n} ${currency}` : n;
}

const axis = (v: number, kind: Kind) => (kind === "percent" ? `${short(v * 100)}%` : short(v));

export function AdsChart({ brand, days, go }: { brand: string; days: number; go: PanelGo }) {
  const [prefs, setPrefs] = usePrefs();
  const [result, setResult] = useState<TrendResult | null>(null);
  const [failed, setFailed] = useState(false);
  // Finished days only: today's part-day would draw as a drop.
  const spec = useMemo<TrendSpec>(() => {
    const day = (n: number) => new Date(Date.now() - n * 86_400_000).toISOString().slice(0, 10);
    return {
      date_range: days > 1 ? { from: day(days), to: day(1) } : { from: day(0), to: day(0) },
      interval: days > 60 ? "week" : "day",
      display: "line",
      forecast: false,
      series: [
        { kind: "ad", measure: "spend", label: "Ad spend" },
        { kind: "ad", measure: "clicks", label: "Clicks" },
        { kind: "ad", measure: "impressions", label: "Impressions" },
        { kind: "ad", measure: "conversions", label: "Conversions" },
        { kind: "visit", filters: [{ property: "network", op: "exists" }], label: "Visits from ads" },
      ],
    };
  }, [days]);
  useEffect(() => {
    setResult(null);
    setFailed(false);
    let live = true;
    api.POST("/v1/brands/{brand}/insights/trend", { params: { path: { brand } }, body: spec }).then(({ data }) => {
      if (!live) return;
      if (data) setResult(data);
      else setFailed(true);
    });
    return () => {
      live = false;
    };
  }, [brand, spec]);

  const data = useMemo(() => {
    if (!result) return null;
    const s = result.series;
    const at = (i: number, j: number) => s[i]?.points[j]?.value ?? 0;
    const rows: Row[] = (s[0]?.points ?? []).map((_, j) => ({ spend: at(0, j), clicks: at(1, j), impressions: at(2, j), conversions: at(3, j), visits: at(4, j) }));
    const total: Row = { spend: s[0]?.total ?? 0, clicks: s[1]?.total ?? 0, impressions: s[2]?.total ?? 0, conversions: s[3]?.total ?? 0, visits: s[4]?.total ?? 0 };
    const c = s[0]?.currency ?? "";
    return { rows, total, starts: (s[0]?.points ?? []).map((p) => p.start), currency: c === "brand" || c === "mixed" ? "" : c };
  }, [result]);

  const metric = (key: string) => adsChartMetrics.find((m) => m.key === key)!;
  const color = (key: string) => seriesColor(prefs.tiles.indexOf(key));
  const toggle = (key: string) => {
    const on = prefs.on.includes(key) ? prefs.on.filter((k) => k !== key) : [...prefs.on, key];
    setPrefs({ ...prefs, on });
    track("ui.ads_chart_metric_toggled", { metric: key, on: on.includes(key) });
  };
  const swap = (i: number, key: string) => {
    const was = prefs.tiles[i];
    if (key === was) return;
    const tiles = [...prefs.tiles];
    // A metric already on another tile trades places with this one.
    const other = tiles.indexOf(key);
    if (other >= 0) tiles[other] = was;
    tiles[i] = key;
    const on = prefs.on.map((k) => (k === was && other < 0 ? key : k));
    setPrefs({ tiles, on });
    track("ui.ads_chart_metric_picked", { metric: key });
  };

  return (
    <div className="overflow-hidden rounded-xl border bg-card">
      <div className="flex flex-wrap items-stretch border-b">
        {prefs.tiles.map((key, i) => {
          const m = metric(key);
          const drawn = prefs.on.includes(key);
          return (
            <div key={i} className="relative min-w-[150px] flex-1 border-r" style={drawn ? { background: color(key), color: "#fff" } : undefined}>
              <button type="button" aria-pressed={drawn} title={drawn ? "Hide from the chart" : "Show on the chart"} onClick={() => toggle(key)} className="block w-full px-4 pt-8 pb-3 text-left">
                <span className="block text-2xl font-semibold tabular-nums">{data ? show(m.value(data.total), m.kind, data.currency) : "…"}</span>
              </button>
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <button type="button" aria-label={`${m.label}: choose another metric`} className={`absolute top-2 left-3 flex items-center gap-1 rounded px-1 text-[13px] ${drawn ? "hover:bg-white/20" : "text-muted-foreground hover:bg-muted hover:text-foreground"}`}>
                    <ChevronDown className="size-3.5" />
                    {m.label}
                  </button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="start">
                  <DropdownMenuRadioGroup value={key} onValueChange={(k) => swap(i, k)}>
                    {adsChartMetrics.map((x) => (
                      <DropdownMenuRadioItem key={x.key} value={x.key}>{x.label}</DropdownMenuRadioItem>
                    ))}
                  </DropdownMenuRadioGroup>
                </DropdownMenuContent>
              </DropdownMenu>
            </div>
          );
        })}
        <div className="flex items-center px-4 py-2">
          <button type="button" className="font-mono text-[11px] text-muted-foreground hover:text-foreground" onClick={() => openDraft("trend", { ...spec, series: [spec.series[0], spec.series[4]] }, go, "ads")}>
            Open in Trends →
          </button>
        </div>
      </div>
      <div className="p-3">
        {failed ? (
          <p className="py-16 text-center text-[13px] text-muted-foreground">The chart could not be loaded. Reload the page to try again.</p>
        ) : !data ? (
          <p className="py-16 text-center text-[13px] text-muted-foreground">Loading…</p>
        ) : prefs.on.length === 0 ? (
          <p className="py-16 text-center text-[13px] text-muted-foreground">Click a number above to draw it.</p>
        ) : (
          <Lines
            starts={data.starts}
            weekly={days > 60}
            series={prefs.tiles.filter((k) => prefs.on.includes(k)).map((k) => ({ key: k, label: metric(k).label, kind: metric(k).kind, color: color(k), values: data.rows.map((r) => metric(k).value(r)) }))}
            currency={data.currency}
          />
        )}
      </div>
    </div>
  );
}

type Line = { key: string; label: string; kind: Kind; color: string; values: (number | null)[] };

// Lines draws each metric against its own scale, since clicks and cost
// have nothing in common: the first has its numbers on the left axis and
// the second on the right, and the hover card has every value for a day.
function Lines({ starts, weekly, series, currency }: { starts: string[]; weekly: boolean; series: Line[]; currency: string }) {
  const [ref, width] = useWidth();
  const [hover, setHover] = useState<number | null>(null);
  const height = 260;
  const pad = { l: 52, r: series.length > 1 ? 52 : 16, t: 12, b: 26 };
  const n = starts.length;
  const x = (i: number) => pad.l + (n <= 1 ? (width - pad.l - pad.r) / 2 : (i * (width - pad.l - pad.r)) / (n - 1));
  const scales = series.map((s) => {
    const ticks = niceTicks(0, Math.max(0, ...s.values.map((v) => v ?? 0)));
    return { ticks, top: ticks[ticks.length - 1] || 1 };
  });
  const y = (si: number, v: number) => pad.t + (1 - v / scales[si].top) * (height - pad.t - pad.b);
  const date = (iso: string) => new Date(iso).toLocaleDateString(undefined, { month: "short", day: "numeric", timeZone: "UTC" });
  const every = Math.max(1, Math.ceil(n / Math.max(2, Math.floor((width - pad.l - pad.r) / 80))));
  // The grid follows the first metric's ticks; the second's numbers sit at
  // the same heights on the right.
  const rowsOf = scales[0].ticks;
  return (
    <div ref={ref} className="relative" style={{ height }}>
      <svg
        width={width}
        height={height}
        role="img"
        aria-label={`${series.map((s) => s.label).join(", ")} over time`}
        onMouseMove={(e) => {
          const r = e.currentTarget.getBoundingClientRect();
          const i = n <= 1 ? 0 : Math.round(((e.clientX - r.left - pad.l) / (width - pad.l - pad.r)) * (n - 1));
          setHover(Math.min(n - 1, Math.max(0, i)));
        }}
        onMouseLeave={() => setHover(null)}
      >
        {rowsOf.map((t, i) => {
          const yy = y(0, t);
          const frac = t / scales[0].top;
          return (
            <g key={i}>
              <line x1={pad.l} x2={width - pad.r} y1={yy} y2={yy} stroke="var(--border)" strokeDasharray={i === 0 ? undefined : "3 4"} />
              <text x={pad.l - 8} y={yy + 4} textAnchor="end" fontSize={11} fill={series[0].color}>{axis(t, series[0].kind)}</text>
              {series[1] && <text x={width - pad.r + 8} y={yy + 4} fontSize={11} fill={series[1].color}>{axis(frac * scales[1].top, series[1].kind)}</text>}
            </g>
          );
        })}
        {starts.map((s, i) => (i % every === 0 ? <text key={i} x={x(i)} y={height - 6} textAnchor="middle" fontSize={11} fill="var(--muted-foreground)">{date(s)}</text> : null))}
        {series.map((s, si) =>
          runs(s.values).map((run, ri) => (
            <path key={`${s.key}-${ri}`} d={smooth(run.map(([i, v]) => [x(i), y(si, v)]))} fill="none" stroke={s.color} strokeWidth={2} strokeLinecap="round" />
          )),
        )}
        {hover !== null && (
          <g>
            <line x1={x(hover)} x2={x(hover)} y1={pad.t} y2={height - pad.b} stroke="var(--muted-foreground)" strokeOpacity={0.4} />
            {series.map((s, si) => (s.values[hover] === null ? null : <circle key={s.key} cx={x(hover)} cy={y(si, s.values[hover]!)} r={3.5} fill={s.color} stroke="var(--card)" strokeWidth={1.5} />))}
          </g>
        )}
      </svg>
      {hover !== null && (
        <HoverCard x={x(hover)} width={width}>
          <div className="mb-1 font-medium">{weekly ? `Week of ${date(starts[hover])}` : date(starts[hover])}</div>
          {series.map((s) => (
            <div key={s.key} className="flex items-center gap-2">
              <Swatch color={s.color} />
              <span className="flex-1 text-muted-foreground">{s.label}</span>
              <span className="tabular-nums">{show(s.values[hover], s.kind, currency)}</span>
            </div>
          ))}
        </HoverCard>
      )}
    </div>
  );
}
