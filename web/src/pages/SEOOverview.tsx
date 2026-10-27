import { useEffect, useState } from "react";
import type { SEOOverviewBrand, SEOTrend } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { api, errorMessage } from "../api";
import { Badge, Button, Empty, ErrorText, Loading, PageHeader, type tones } from "../ui";
import type { Tab } from "../App";
import { scoreTone } from "./SiteAudits";

// The SEO overview: is each brand growing in search? Branded search,
// search clicks and visitors by week, and domain rating, links and organic
// traffic from the SEO snapshots, each with which way it went over the last
// four weeks. It reads only what is stored, so opening it costs nothing.
// When a brand has no snapshots yet, its SEO status says why and what to do,
// and while a first read is under way the page checks back on its own.

type Props = { ws: string; go: (tab: Tab, item?: string) => void };
type Tone = keyof typeof tones;
type Week = SEOOverviewBrand["weeks"][number];
type Snap = SEOOverviewBrand["seo"][number];
type SEOStatus = SEOOverviewBrand["seo_status"];

const fmt = (v?: number | null, digits = 0) => (v === undefined || v === null ? "–" : v.toLocaleString(undefined, { maximumFractionDigits: digits }));
const day = (d: string) => new Date(d + "T00:00:00Z").toLocaleDateString(undefined, { month: "short", day: "numeric", timeZone: "UTC" });

const verdict: Record<SEOTrend["direction"], { label: string; tone: Tone; arrow: string }> = {
  up: { label: "Growing", tone: "green", arrow: "↑" },
  down: { label: "Falling", tone: "red", arrow: "↓" },
  flat: { label: "Steady", tone: "grey", arrow: "→" },
  unknown: { label: "Not enough data", tone: "grey", arrow: "" },
};

export function SEOOverview({ ws, go }: Props) {
  const [data, setData] = useState<SEOOverviewBrand[] | null>(null);
  const [range, setRange] = useState<{ from: string; to: string } | null>(null);
  const [notes, setNotes] = useState<string[]>([]);
  const [error, setError] = useState("");
  const [weeks, setWeeks] = useState(12);
  const [poll, setPoll] = useState(0);
  const brandKey = useBrand(ws).query.brand ?? "";
  const reading = data?.some((b) => b.seo_status.state === "reading") ?? false;

  useEffect(() => {
    setData(null);
  }, [ws, brandKey, weeks]);

  // A first SEO read takes a minute or two; check back until it lands.
  useEffect(() => {
    if (!reading) return;
    const t = setTimeout(() => setPoll((n) => n + 1), 20000);
    return () => clearTimeout(t);
  }, [reading, poll]);

  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/seo/overview", { params: { path: { ws }, query: { weeks, ...(brandKey ? { brand: brandKey } : {}) } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setError("");
      setData(data.data);
      setRange({ from: data.from, to: data.to });
      setNotes(data.notes);
    });
  }, [ws, brandKey, weeks, poll]);

  const header = (
    <PageHeader
      title="SEO overview"
      description="Is each brand growing in search? The last four weeks against the four before, from your search consoles, analytics tools and SEO snapshots."
      action={
        <select className="h-8 rounded-md border border-input bg-transparent px-2.5 text-[13px] shadow-xs dark:bg-input/30" aria-label="Weeks" value={weeks} onChange={(e) => setWeeks(Number(e.target.value))}>
          {[8, 12, 26, 52].map((n) => <option key={n} value={n}>Last {n} weeks</option>)}
        </select>
      }
    />
  );
  if (error) return <div className="space-y-6">{header}<ErrorText>{error}</ErrorText></div>;
  if (!data) return <div className="space-y-6">{header}<Loading /></div>;

  const nothing = data.every((b) => b.weeks.every((w) => w.visitors === null && w.search_clicks === null) && b.seo.length === 0 && b.seo_status.state === "not_connected");
  return (
    <div className="space-y-8">
      {header}
      {nothing && (
        <Empty title="Nothing to compare yet" action={<Button onClick={() => go("connectors")}>Connect a tool</Button>}>
          Connect Google Search Console for branded search, an analytics tool such as GA4 or Plausible for visitors, and Ahrefs or DataForSEO for domain rating and backlinks. Each runs on your own account. Numbers show up here after their first sync.
        </Empty>
      )}
      {!nothing && data.map((b) => <BrandCard key={b.brand?.id ?? "shared"} b={b} go={go} />)}
      {!nothing && range && (
        <p className="text-[12px] text-muted-foreground">
          Weeks run Monday to Sunday, {day(range.from)} to {day(range.to)}. {notes.join(" ")}
        </p>
      )}
    </div>
  );
}

function BrandCard({ b, go }: { b: SEOOverviewBrand; go: Props["go"] }) {
  const name = b.brand?.name ?? "Shared websites";
  const series = (k: keyof Week) => b.weeks.map((w) => w[k] as number | null);
  const snaps = (k: keyof Snap) => b.seo.map((s) => s[k] as number | null);
  const hasSearch = b.weeks.some((w) => w.search_clicks !== null);
  const hasTraffic = b.weeks.some((w) => w.visitors !== null);
  const status = b.seo_status;
  const missing = [!hasSearch && "a search console", !hasTraffic && "an analytics tool", status.state === "not_connected" && "Ahrefs or DataForSEO"].filter(Boolean) as string[];

  return (
    <section aria-label={name} className="space-y-3">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <div className="flex flex-wrap items-baseline gap-2">
          <h2 className="text-[15px] font-semibold">{name}</h2>
          <span className="font-mono text-[12px] text-muted-foreground">{b.hosts.join(" · ") || "No website yet"}</span>
        </div>
        {b.terms.length > 0 && hasSearch && <span className="text-[12px] text-muted-foreground">Branded means a search with {b.terms.slice(0, 3).map((t) => `"${t}"`).join(", ")}{b.terms.length > 3 ? "…" : ""}</span>}
      </div>
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 xl:grid-cols-5">
        <Tile label="Branded search" sub="clicks, last 4 weeks" trend={b.trends.branded_search} points={series("branded_clicks")} />
        <Tile label="Visitors" sub="last 4 weeks" trend={b.trends.visitors} points={series("visitors")} alt={{ label: "Search clicks", trend: b.trends.search_clicks }} />
        <Tile label="Domain rating" sub={b.seo.at(-1)?.connector === "ahrefs" ? "Ahrefs DR" : "latest snapshot"} trend={b.trends.domain_rating} points={snaps("domain_rating")} digits={1} />
        <Tile label="Referring domains" sub="latest snapshot" trend={b.trends.referring_domains} points={snaps("referring_domains")} alt={{ label: "Backlinks", trend: b.trends.backlinks }} />
        <Tile label="Organic traffic" sub={b.seo.at(-1)?.connector === "ahrefs" ? "Ahrefs estimate, monthly" : "estimate, monthly"} trend={b.trends.organic_traffic} points={snaps("organic_traffic")} alt={{ label: "Keywords", trend: b.trends.organic_keywords }} />
      </div>
      {status.state !== "ready" && status.state !== "not_connected" && <StatusLine status={status} brand={b.brand?.id} go={go} />}
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-[13px]">
        {b.sites.map((s) => (
          <button key={s.id} className="flex items-center gap-1.5 hover:underline" onClick={() => go("audits", s.id)}>
            <span className="font-mono">{s.host}</span>
            <Badge tone={scoreTone(s.health_score)}>{s.health_score === null ? "Not audited" : `Health ${s.health_score}`}</Badge>
          </button>
        ))}
        {b.open_fixes !== null && b.open_fixes > 0 && <button className="text-muted-foreground hover:text-foreground hover:underline" onClick={() => go("fixes")}>{b.open_fixes.toLocaleString()} open {b.open_fixes === 1 ? "fix" : "fixes"}</button>}
        {b.open_fixes !== null && b.sites.length === 0 && <button className="text-muted-foreground hover:text-foreground hover:underline" onClick={() => go("audits")}>Audit {b.brand ? `${b.brand.name}'s` : "a"} website</button>}
        {missing.length > 0 && (
          <button className="text-muted-foreground hover:text-foreground hover:underline" onClick={() => go("connectors")}>
            Connect {missing.join(", ").replace(/, ([^,]*)$/, " and $1")} to see the rest
          </button>
        )}
      </div>
    </section>
  );
}

// StatusLine says why the SEO numbers are missing or old, with the next step.
function StatusLine({ status, brand, go }: { status: SEOStatus; brand?: string; go: Props["go"] }) {
  const tone: Tone = status.state === "failed" ? "red" : status.state === "reading" ? "blue" : "amber";
  const label = { failed: "Read failed", reading: "Reading", off: "Turned off", not_followed: "Not followed", no_website: "No website", not_connected: "", ready: "" }[status.state];
  const action =
    status.state === "no_website" ? <Button variant="ghost" onClick={() => go("brands", brand)}>Add the website</Button>
    : status.state === "reading" ? null
    : <Button variant="ghost" onClick={() => go("seo")}>Open SEO settings</Button>;
  return (
    <div role="status" className="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-lg border bg-card px-3 py-2 text-[13px] shadow-xs">
      <Badge tone={tone}>{label}</Badge>
      <span className="min-w-0 flex-1">{status.message}</span>
      {action}
    </div>
  );
}

function Tile({ label, sub, trend, points, digits = 0, alt }: { label: string; sub: string; trend: SEOTrend; points: (number | null)[]; digits?: number; alt?: { label: string; trend: SEOTrend } }) {
  const v = verdict[trend.direction];
  const change = trend.change === null ? null : `${trend.change > 0 ? "+" : ""}${(trend.change * 100).toFixed(Math.abs(trend.change) < 0.1 ? 1 : 0)}%`;
  return (
    <div className="flex min-w-0 flex-col gap-2 rounded-lg border bg-card p-3 shadow-xs sm:p-4">
      <div className="flex flex-col items-start gap-1.5 lg:flex-row lg:justify-between">
        <div className="min-w-0">
          <div className="font-mono text-[11px] tracking-wide text-muted-foreground uppercase">{label}</div>
          <div className="text-[11px] text-muted-foreground">{sub}</div>
        </div>
        <Badge tone={v.tone}>{v.label}</Badge>
      </div>
      <div className="flex flex-wrap items-baseline gap-x-2">
        <span className="text-xl font-semibold tabular-nums sm:text-2xl">{fmt(trend.current, digits)}</span>
        {change && <span className={`font-mono text-[12px] tabular-nums ${v.tone === "green" ? "text-green-700 dark:text-green-400" : v.tone === "red" ? "text-red-700 dark:text-red-400" : "text-muted-foreground"}`}>{v.arrow} {change}</span>}
      </div>
      <Spark points={points} label={label} />
      {alt && alt.trend.current !== null && (
        <div className="text-[12px] text-muted-foreground">
          {alt.label}: <span className="font-mono tabular-nums text-foreground">{fmt(alt.trend.current)}</span> {alt.trend.direction !== "unknown" && <span>{verdict[alt.trend.direction].arrow}</span>}
        </div>
      )}
    </div>
  );
}

// A line of the values in order; gaps are skipped.
function Spark({ points, label }: { points: (number | null)[]; label: string }) {
  const known = points.map((p, i) => [i, p] as const).filter(([, p]) => p !== null) as [number, number][];
  if (known.length < 2) return <div className="h-10 text-[12px] text-muted-foreground">Too few weeks stored for a line.</div>;
  const W = 200, H = 40;
  const lo = Math.min(...known.map(([, p]) => p)), hi = Math.max(...known.map(([, p]) => p));
  const x = (i: number) => (points.length > 1 ? (i / (points.length - 1)) * W : W / 2);
  const y = (p: number) => (hi === lo ? H / 2 : H - 3 - ((p - lo) / (hi - lo)) * (H - 6));
  return (
    <svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" className="h-10 w-full overflow-visible" role="img" aria-label={`${label} over time`}>
      <polyline fill="none" stroke="var(--series-1)" strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" vectorEffect="non-scaling-stroke" points={known.map(([i, p]) => `${x(i)},${y(p)}`).join(" ")} />
    </svg>
  );
}
