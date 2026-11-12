import { useEffect, useRef, useState } from "react";
import type { SEOOverviewBrand, SEOTrend } from "@outflare/sdk";
import { useBrand, pickedBrandId } from "@/lib/brand";
import { QueryChart } from "@/components/query-chart";
import { api, errorMessage } from "../api";
import { Badge, Button, Empty, ErrorText, Loading, PageHeader, field, type tones } from "../ui";
import type { Tab } from "../App";
import { scoreTone } from "./SiteAudits";

// The SEO overview: is each brand doing better in search? One full-width
// chart per number, for the weeks picked, with a y-axis, each week's value
// on hover, and the whole period set against as many weeks before it:
// non-branded clicks (with CTR and position), search conversions, branded
// search, search clicks and visitors by week (the period before drawn
// dashed), tracked keywords in the top 10 and AI mention rates by week,
// share of voice against tracked competitors, and keywords in the top 3,
// organic traffic, keywords, domain rating and links from the SEO
// snapshots. Visitors are every channel, not search alone. It reads only what is stored, so opening it costs
// nothing. When a brand has no snapshots yet, its SEO status says why and
// what to do, and while a first read is under way the page checks back on
// its own. The dates picked stay in the address, so a link shows the same.

type Props = { ws: string; go: (tab: Tab, item?: string) => void };
type Tone = keyof typeof tones;
type Week = SEOOverviewBrand["weeks"][number];
type Snap = SEOOverviewBrand["seo"][number];
type SEOStatus = SEOOverviewBrand["seo_status"];
type Trends = SEOOverviewBrand["period"];
// A period: weeks back from the last week that is over, or two dates.
type Range = { weeks: number } | { from: string; to: string };
type Dates = { from: string; to: string; previous_from: string; previous_to: string };

const fmt = (v?: number | null, digits = 0) => (v === undefined || v === null ? "–" : v.toLocaleString(undefined, { maximumFractionDigits: digits }));
const day = (d: string) => new Date(d + "T00:00:00Z").toLocaleDateString(undefined, { month: "short", day: "numeric", timeZone: "UTC" });
const fullDay = (d: string) => new Date(d + "T00:00:00Z").toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric", timeZone: "UTC" });
const isoDay = (offset: number) => new Date(Date.now() + offset * 86400000).toISOString().slice(0, 10);
const pct = (c: number) => `${c > 0 ? "+" : ""}${(c * 100).toFixed(Math.abs(c) < 0.1 ? 1 : 0)}%`;
// A rate is a share (0 to 1, shown as a percentage) or a search position
// (lower is better); anything else is a count.
type Rate = "percent" | "position";
const show = (v: number | null | undefined, rate?: Rate, digits = 0) =>
  v === null || v === undefined ? "–" : rate === "percent" ? `${(v * 100).toLocaleString(undefined, { maximumFractionDigits: 1 })}%` : fmt(v, rate === "position" ? 1 : digits);
const connectorName = (c: string) => ({ ahrefs: "Ahrefs", dataforseo: "DataForSEO" })[c] ?? c;

const verdict: Record<SEOTrend["direction"], { label: string; tone: Tone; arrow: string }> = {
  up: { label: "Growing", tone: "green", arrow: "↑" },
  down: { label: "Falling", tone: "red", arrow: "↓" },
  flat: { label: "Steady", tone: "grey", arrow: "→" },
  unknown: { label: "Not enough data", tone: "grey", arrow: "" },
};
const changeTone = (t: Tone) => (t === "green" ? "text-green-700 dark:text-green-400" : t === "red" ? "text-red-700 dark:text-red-400" : "text-muted-foreground");

const presets: { weeks: number; label: string }[] = [
  { weeks: 4, label: "Last 4 weeks" },
  { weeks: 12, label: "Last 12 weeks" },
  { weeks: 26, label: "Last 6 months" },
  { weeks: 52, label: "Last 12 months" },
  { weeks: 104, label: "Last 2 years" },
];

function rangeFromURL(): Range {
  const q = new URLSearchParams(location.search);
  const from = q.get("from"), to = q.get("to");
  if (from && to && /^\d{4}-\d{2}-\d{2}$/.test(from) && /^\d{4}-\d{2}-\d{2}$/.test(to)) return { from, to };
  const weeks = Number(q.get("weeks"));
  return { weeks: presets.some((p) => p.weeks === weeks) ? weeks : 12 };
}

function rangeToURL(r: Range) {
  const url = new URL(location.href);
  for (const k of ["weeks", "from", "to"]) url.searchParams.delete(k);
  if ("weeks" in r) {
    if (r.weeks !== 12) url.searchParams.set("weeks", String(r.weeks));
  } else {
    url.searchParams.set("from", r.from);
    url.searchParams.set("to", r.to);
  }
  history.replaceState(history.state, "", url);
}

export function SEOOverview({ ws, go }: Props) {
  const [data, setData] = useState<SEOOverviewBrand[] | null>(null);
  const [dates, setDates] = useState<Dates | null>(null);
  const [notes, setNotes] = useState<string[]>([]);
  const [error, setError] = useState("");
  const [range, setRange] = useState<Range>(rangeFromURL);
  const [poll, setPoll] = useState(0);
  const { query: brandQuery, active } = useBrand(ws);
  const brandKey = brandQuery.brand ?? "";
  const reading = data?.some((b) => b.seo_status.state === "reading") ?? false;
  const rangeKey = JSON.stringify(range);

  useEffect(() => {
    setData(null);
    rangeToURL(range);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, brandKey, rangeKey]);

  // A first SEO read takes a minute or two; check back until it lands. The
  // checks keep going while a read runs, whether or not the last one got an
  // answer.
  useEffect(() => {
    if (!reading) return;
    const t = setInterval(() => setPoll((n) => n + 1), 20000);
    return () => clearInterval(t);
  }, [reading]);
  const shown = useRef(data);
  shown.current = data;

  // A failed check-back keeps the page as it was, and the next check tries
  // again. A failed first load shows the error. Answers to a call the page no
  // longer needs (another brand or range picked since) are dropped.
  useEffect(() => {
    let live = true;
    // Each brand's overview is its own: with all brands picked, every
    // brand's in turn.
    const query = "weeks" in range ? { weeks: range.weeks } : { from: range.from, to: range.to };
    const one = (brand: string) => api.GET("/v1/brands/{brand}/seo/overview", { params: { path: { brand }, query } });
    const ids = pickedBrandId() ? [pickedBrandId()] : active.map((b) => b.id);
    if (!ids.length) return;
    Promise.all(ids.map(one))
      .then((all) => {
        const failed = all.find((x) => x.error || !x.data);
        const first = all.find((x) => x.data)?.data;
        const error = failed?.error ?? (first ? undefined : failed?.error);
        const data = first && !failed ? { ...first, data: all.flatMap((x) => x.data?.data ?? []), notes: [...new Set(all.flatMap((x) => x.data?.notes ?? []))] } : undefined;
        if (!live) return;
        if (error || !data) {
          if (shown.current) return;
          return setError(errorMessage(error));
        }
        setError("");
        setData(data.data);
        setDates({ from: data.from, to: data.to, previous_from: data.previous_from, previous_to: data.previous_to });
        setNotes(data.notes);
      })
      .catch((err: unknown) => {
        if (!live || shown.current || (err instanceof Error && err.name === "AbortError")) return;
        setError(errorMessage(undefined));
      });
    return () => {
      live = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, brandKey, rangeKey, poll, active.length]);

  const header = (
    <PageHeader
      title="SEO overview"
      description="Is each brand doing better in search? Every number for the weeks you pick, against as many weeks before, from what your search consoles, analytics, rank checks, AI answers and SEO snapshots already stored."
      action={<RangeControl value={range} onChange={setRange} />}
    />
  );
  if (error) return <div className="space-y-6">{header}<ErrorText>{error}</ErrorText></div>;
  if (!data || !dates) return <div className="space-y-6">{header}<Loading /></div>;

  const nothing = data.every((b) => b.weeks.every((w) => w.visitors === null && w.search_clicks === null) && b.seo.length === 0 && b.seo_status.state === "not_connected");
  return (
    <div className="space-y-8">
      {header}
      {nothing && (
        <Empty title="Nothing to compare yet" action={<Button onClick={() => go("connectors")}>Connect a tool</Button>}>
          Connect Google Search Console for branded search, an analytics tool such as GA4 or Plausible for visitors, and Ahrefs or DataForSEO for domain rating and backlinks. Each runs on your own account. Numbers show up here after their first sync.
        </Empty>
      )}
      {!nothing && (
        <p className="text-[13px] text-muted-foreground">
          <span className="font-medium text-foreground">{fullDay(dates.from)} to {fullDay(dates.to)}</span>, against {fullDay(dates.previous_from)} to {fullDay(dates.previous_to)}. Weeks run Monday to Sunday.
        </p>
      )}
      {!nothing && data.map((b) => <BrandSection key={b.brand?.id ?? "shared"} b={b} dates={dates} go={go} />)}
      {!nothing && notes.length > 0 && <p className="text-[12px] text-muted-foreground">{notes.join(" ")}</p>}
    </div>
  );
}

// RangeControl picks the weeks: a preset, or two dates.
function RangeControl({ value, onChange }: { value: Range; onChange: (r: Range) => void }) {
  const [custom, setCustom] = useState(!("weeks" in value));
  const [from, setFrom] = useState("from" in value ? value.from : isoDay(-91));
  const [to, setTo] = useState("to" in value ? value.to : isoDay(-1));
  const apply = (f: string, t: string) => {
    if (f && t && f <= t) onChange({ from: f, to: t });
  };
  return (
    <div className="flex flex-wrap items-center gap-2">
      <select
        className={field}
        aria-label="Dates"
        value={custom ? "custom" : String((value as { weeks: number }).weeks)}
        onChange={(e) => {
          if (e.target.value === "custom") {
            setCustom(true);
            apply(from, to);
          } else {
            setCustom(false);
            onChange({ weeks: Number(e.target.value) });
          }
        }}
      >
        {presets.map((p) => (
          <option key={p.weeks} value={p.weeks}>
            {p.label}
          </option>
        ))}
        <option value="custom">Custom dates</option>
      </select>
      {custom && (
        <>
          <input type="date" className={field} aria-label="From" value={from} max={to || undefined} onChange={(e) => { setFrom(e.target.value); apply(e.target.value, to); }} />
          <span className="text-[13px] text-muted-foreground">to</span>
          <input type="date" className={field} aria-label="To" value={to} min={from || undefined} max={isoDay(0)} onChange={(e) => { setTo(e.target.value); apply(from, e.target.value); }} />
        </>
      )}
    </div>
  );
}

function BrandSection({ b, dates, go }: { b: SEOOverviewBrand; dates: Dates; go: Props["go"] }) {
  const name = b.brand?.name ?? "Shared websites";
  const hasSearch = [...b.weeks, ...b.previous_weeks].some((w) => w.search_clicks !== null);
  const hasTraffic = [...b.weeks, ...b.previous_weeks].some((w) => w.visitors !== null);
  const status = b.seo_status;
  const src = b.sources;
  const missing = [
    !hasSearch && src.search.state === "not_connected" && "a search console",
    !hasTraffic && src.traffic.state === "not_connected" && "an analytics tool",
    status.state === "not_connected" && "Ahrefs or DataForSEO",
  ].filter(Boolean) as string[];
  // Tools that are connected, but read other websites of the workspace.
  const elsewhere = [
    !hasSearch && src.search.state === "other_sites" && { what: "Search console numbers", hosts: src.search.hosts },
    !hasTraffic && src.traffic.state === "other_sites" && { what: "Visitor numbers", hosts: src.traffic.hosts },
  ].filter(Boolean) as { what: string; hosts: string[] }[];
  const ahrefs = b.seo.at(-1)?.connector === "ahrefs";
  const weeks = b.weeks.length;
  const before = `the ${weeks} ${weeks === 1 ? "week" : "weeks"} before`;
  const weekly = (key: keyof Trends, k: keyof Week, label: string, sub: string, empty: string, o: { rate?: Rate } = {}) => (
    <WeeklyCard label={label} sub={sub} period={b.period[key]} weeks={b.weeks} previous={b.previous_weeks} pick={(w) => w[k] as number | null} before={before} empty={empty} rate={o.rate} />
  );
  const snapshot = (key: keyof Trends, k: keyof Snap, label: string, sub: string, digits = 0, fit = true) => (
    <SnapshotCard label={label} sub={sub} period={b.period[key]} snaps={b.seo} pick={(s) => s[k] as number | null} since={dates.from} digits={digits} fit={fit} empty={snapshotEmpty(status)} />
  );

  return (
    <section aria-label={name} className="space-y-3">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <div className="flex flex-wrap items-baseline gap-2">
          <h2 className="text-[15px] font-semibold">{name}</h2>
          <span className="font-mono text-[12px] text-muted-foreground">{b.hosts.join(" · ") || "No website yet"}</span>
        </div>
        {b.terms.length > 0 && hasSearch && <span className="text-[12px] text-muted-foreground">Branded means a search with {b.terms.slice(0, 3).map((t) => `"${t}"`).join(", ")}{b.terms.length > 3 ? "…" : ""}</span>}
      </div>
      {status.state !== "ready" && status.state !== "not_connected" && <StatusLine status={status} brand={b.brand?.id} go={go} />}
      {elsewhere.map((x) => (
        <div key={x.what} className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-dashed px-3 py-2 text-[13px]">
          <span className="flex items-center gap-2">
            <Badge tone="amber">Other websites</Badge>
            <span>
              {x.what} come in for {hostList(x.hosts)}, but not for {b.hosts.length ? <span className="font-mono">{b.hosts[0]}</span> : "this brand"}. Add the website to that tool, or set which brand its site belongs to.
            </span>
          </span>
          <Button variant="ghost" onClick={() => go("analytics")}>Check analytics sites</Button>
        </div>
      ))}
      <div className="space-y-3">
        {weekly("non_branded_search", "non_branded_clicks", "Non-brand search", "Clicks from searches that don't name the brand, per week", "Connect a search console to see non-brand search.")}
        {weekly("search_ctr", "search_ctr", "Non-brand CTR", "Share of non-brand impressions that were clicked, per week", "Connect a search console to see click-through rate.", { rate: "percent" })}
        {weekly("search_position", "search_position", "Average position", "Non-brand queries, weighted by impressions; lower is better", "Connect a search console to see average position.", { rate: "position" })}
        {weekly("search_conversions", "search_conversions", "Search conversions", "Conversions on visits that landed from a search engine, per week", "Track conversions from search landings in product analytics to see this.")}
        {weekly("branded_search", "branded_clicks", "Branded search", "Clicks from searches that name the brand, per week", "Connect a search console to see branded search.")}
        {weekly("search_clicks", "search_clicks", "Search clicks", "All clicks from search, per week", "Connect a search console to see search clicks.")}
        {weekly("visitors", "visitors", "Visitors", "Visitors from every channel, not search alone, per week", "Connect an analytics tool such as GA4 or Plausible to see visitors.")}
        {snapshot("keywords_top3", "top3", "Keywords in the top 3", `Keywords ranking in the top 3${b.seo.at(-1)?.connector ? `, from ${connectorName(b.seo.at(-1)!.connector)}` : ""}, at each snapshot`)}
        {weekly("keywords_top10", "keywords_top10", "Tracked keywords in the top 10", "Share of the keywords you track in the top 10, per week", "Track keywords in Rankings to see this.", { rate: "percent" })}
        {weekly("ai_mention_rate", "ai_mention_rate", "AI mentions", "Share of stored AI answers that name the brand, per week", "Set up AI visibility questions to see how often AI answers name the brand.", { rate: "percent" })}
        {weekly("ai_recommend_rate", "ai_recommend_rate", "AI recommendations", "Share of stored AI answers that recommend the brand, per week", "Set up AI visibility questions to see how often AI answers recommend the brand.", { rate: "percent" })}
        <ShareCard period={b.period} before={dates.from} />
        {snapshot("organic_traffic", "organic_traffic", "Organic traffic", `${ahrefs ? "Ahrefs" : "The SEO tool's"} estimate of monthly visits from search, at each snapshot`)}
        {snapshot("organic_keywords", "organic_keywords", "Organic keywords", "Keywords ranking in the top 100, at each snapshot")}
        {snapshot("domain_rating", "domain_rating", "Domain rating", `${ahrefs ? "Ahrefs DR" : "Rating"} of the main website, at each snapshot`, 1)}
        {snapshot("referring_domains", "referring_domains", "Referring domains", "Websites linking to the brand, at each snapshot")}
        {snapshot("backlinks", "backlinks", "Backlinks", "Links to the brand's websites, at each snapshot")}
      </div>
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

function snapshotEmpty(status: SEOStatus): string {
  if (status.state === "not_connected") return "Connect Ahrefs or DataForSEO to see this. It runs on your own account.";
  if (status.state === "reading") return "The first read is under way. This fills in on its own.";
  return "No snapshot in these weeks yet.";
}

// Card is one number on its own row: what it is, its value for the period
// and how it moved against the period before, then its chart.
function Card({ label, sub, value, period, against, digits = 0, rate, extra, children }: { label: string; sub: string; value: string; period: SEOTrend; against: string; digits?: number; rate?: Rate; extra?: React.ReactNode; children: React.ReactNode }) {
  const v = verdict[period.direction];
  const delta = period.current !== null && period.previous !== null ? period.current - period.previous : null;
  const step = (d: number) => (rate === "percent" ? `${d > 0 ? "+" : ""}${(d * 100).toFixed(1)} pts` : `${d > 0 ? "+" : ""}${fmt(d, rate === "position" ? 1 : digits)}`);
  return (
    <div className="flex flex-col gap-4 rounded-lg border bg-card p-4 shadow-xs lg:flex-row">
      <div className="flex shrink-0 flex-col gap-2 lg:w-60">
        <div className="flex items-start justify-between gap-2">
          <div className="min-w-0">
            <h3 className="text-[14px] font-semibold">{label}</h3>
            <p className="text-[12px] text-muted-foreground">{sub}</p>
          </div>
          <Badge tone={v.tone}>{v.label}</Badge>
        </div>
        <div className="text-2xl font-semibold tabular-nums">{value}</div>
        {period.previous !== null && (
          <div className="text-[13px]">
            {delta !== null && (
              <span className={`font-mono tabular-nums ${changeTone(v.tone)}`}>
                {v.arrow} {step(delta)}{period.change !== null && rate !== "percent" && ` (${pct(rate === "position" ? -period.change : period.change)})`}
              </span>
            )}
            <div className="text-[12px] text-muted-foreground">
              vs {show(period.previous, rate, digits)} {against}
            </div>
          </div>
        )}
        {extra}
      </div>
      <div className="min-w-0 flex-1">{children}</div>
    </div>
  );
}

// WeeklyCard: a number counted per week. The period's total, each week as
// a line with the weeks before drawn dashed beside it, and the week it
// rose and fell most. The axis fits the values, so a rise or a drop shows
// instead of a flat line far above zero.
function WeeklyCard({ label, sub, period, weeks, previous, pick, before, empty, rate }: { label: string; sub: string; period: SEOTrend; weeks: Week[]; previous: Week[]; pick: (w: Week) => number | null; before: string; empty: string; rate?: Rate }) {
  const values = weeks.map(pick);
  const prev = previous.map(pick);
  if (values.every((v) => v === null) && prev.every((v) => v === null)) return <EmptyCard label={label} sub={sub} message={empty} />;
  // Week on week moves, the first week against the last week before.
  const moves = values
    .map((v, i) => {
      const p = i === 0 ? prev.at(-1) ?? null : values[i - 1];
      return v === null || p === null ? null : { i, d: v - p, c: p === 0 ? null : (v - p) / p };
    })
    .filter((m): m is { i: number; d: number; c: number | null } => m !== null);
  // For a position, a fall in the number is the gain.
  const good = (d: number) => (rate === "position" ? -d : d);
  const rise = moves.reduce<(typeof moves)[number] | null>((a, m) => (good(m.d) > 0 && (!a || good(m.d) > good(a.d)) ? m : a), null);
  const drop = moves.reduce<(typeof moves)[number] | null>((a, m) => (good(m.d) < 0 && (!a || good(m.d) < good(a.d)) ? m : a), null);
  const move = (m: (typeof moves)[number], word: string, tone: Tone) => (
    <div>
      {word} <span className={`font-mono tabular-nums ${changeTone(tone)}`}>{rate === "percent" ? `${m.d > 0 ? "+" : ""}${(m.d * 100).toFixed(1)} pts` : `${m.d > 0 ? "+" : ""}${fmt(m.d, rate === "position" ? 1 : 0)}`}{!rate && m.c !== null && ` (${pct(m.c)})`}</span>, week of {day(weeks[m.i].week)}
    </div>
  );
  // A count is a total for the period; a rate is the period's average.
  const average = rate || period.current === null ? null : period.current / Math.max(1, values.filter((v) => v !== null).length);
  const scale = (v: number | null) => (v === null ? null : rate === "percent" ? v * 100 : v);
  return (
    <Card
      label={label}
      sub={sub}
      value={show(period.current, rate)}
      period={period}
      against={before}
      rate={rate}
      extra={
        <div className="space-y-0.5 text-[12px] text-muted-foreground">
          {average !== null && <div>Average <span className="font-mono tabular-nums text-foreground">{fmt(average)}</span> a week</div>}
          {rate && <div>Average over the weeks</div>}
          {rise && move(rise, rate === "position" ? "Biggest gain" : "Biggest rise", "green")}
          {drop && move(drop, rate === "position" ? "Biggest fall" : "Biggest drop", "red")}
        </div>
      }
    >
      <QueryChart
        columns={[{ name: "week", type: "text" }, { name: rate === "percent" ? "This period (%)" : "This period", type: "float8" }, { name: rate === "percent" ? "Weeks before (%)" : "Weeks before", type: "float8" }]}
        rows={weeks.map((w, i) => [day(w.week), scale(values[i]), scale(prev[i] ?? null)])}
        spec={{ type: "line", x: "week", y: rate === "percent" ? ["This period (%)", "Weeks before (%)"] : ["This period", "Weeks before"] }}
        styles={{ "Weeks before": { color: "var(--muted-foreground)", dashed: true }, "Weeks before (%)": { color: "var(--muted-foreground)", dashed: true } }}
        title={(i) => `Week of ${day(weeks[i].week)}${previous[i] ? ` (before: ${day(previous[i].week)})` : ""}`}
        height={220}
        fit
        gaps
      />
    </Card>
  );
}

// SnapshotCard: a number the SEO tool reads now and then. The latest value,
// a line through every snapshot in the period, against the last snapshot
// before it.
function SnapshotCard({ label, sub, period, snaps, pick, since, digits, fit, empty }: { label: string; sub: string; period: SEOTrend; snaps: Snap[]; pick: (s: Snap) => number | null; since: string; digits: number; fit: boolean; empty: string }) {
  const points = snaps.filter((s) => pick(s) !== null);
  if (points.length === 0) return <EmptyCard label={label} sub={sub} message={empty} />;
  const inside = points.filter((s) => s.day >= since);
  const shown = inside.length >= 2 ? inside : points.slice(-2);
  const lo = Math.min(...shown.map((s) => pick(s) as number)), hi = Math.max(...shown.map((s) => pick(s) as number));
  return (
    <Card
      label={label}
      sub={sub}
      value={fmt(period.current, digits)}
      period={period}
      digits={digits}
      against={`before ${day(since)}`}
      extra={
        <div className="space-y-0.5 text-[12px] text-muted-foreground">
          <div>Latest snapshot {day(points.at(-1)!.day)}</div>
          {shown.length > 1 && <div>Range <span className="font-mono tabular-nums text-foreground">{fmt(lo, digits)} – {fmt(hi, digits)}</span></div>}
        </div>
      }
    >
      {shown.length < 2 ? (
        <p className="flex h-full min-h-20 items-center text-[13px] text-muted-foreground">One snapshot so far. The line starts with the next weekly read.</p>
      ) : (
        <QueryChart
          columns={[{ name: "day", type: "text" }, { name: label, type: "float8" }]}
          rows={shown.map((s) => [day(s.day), pick(s)])}
          spec={{ type: "line", x: "day", y: [label] }}
          title={(i) => `Snapshot of ${fullDay(shown[i].day)}`}
          height={220}
          fit={fit}
          gaps
        />
      )}
    </Card>
  );
}

// ShareCard: the brand's share of search visits and of top-3 keywords
// among itself and its tracked competitors, latest against before the
// period. It has no weekly line: competitors are read now and then.
function ShareCard({ period, before }: { period: Trends; before: string }) {
  const sov = period.share_of_voice, top = period.share_of_voice_top3;
  const label = "Share of voice", sub = "Your share of estimated search visits among you and your tracked competitors";
  if (sov.current === null && top.current === null) return <EmptyCard label={label} sub={sub} message="Track competitors and connect Ahrefs or DataForSEO to see your share of voice." />;
  const bar = (name: string, t: SEOTrend) => (
    <div className="space-y-1">
      <div className="flex justify-between text-[12px]">
        <span>{name}</span>
        <span className="font-mono tabular-nums">{show(t.current, "percent")}{t.previous !== null && <span className="text-muted-foreground"> (was {show(t.previous, "percent")})</span>}</span>
      </div>
      <div className="relative h-2 rounded bg-muted">
        {t.previous !== null && <div className="absolute inset-y-0 left-0 rounded border-r-2 border-muted-foreground" style={{ width: `${Math.min(100, t.previous * 100)}%` }} />}
        <div className="h-2 rounded" style={{ width: `${Math.min(100, (t.current ?? 0) * 100)}%`, background: "var(--series-1)" }} />
      </div>
    </div>
  );
  return (
    <Card label={label} sub={sub} value={show(sov.current, "percent")} period={sov} against={`before ${day(before)}`} rate="percent">
      <div className="flex h-full flex-col justify-center gap-4">
        {bar("Search visits", sov)}
        {bar("Keywords in the top 3", top)}
        <p className="text-[12px] text-muted-foreground">From the latest snapshots of your websites and your tracked competitors, from the same SEO tool. The mark shows where it stood before {day(before)}.</p>
      </div>
    </Card>
  );
}

function EmptyCard({ label, sub, message }: { label: string; sub: string; message: string }) {
  return (
    <div className="flex flex-col gap-1 rounded-lg border border-dashed px-4 py-3 sm:flex-row sm:items-center sm:gap-4">
      <div className="shrink-0 sm:w-60">
        <h3 className="text-[14px] font-semibold">{label}</h3>
        <p className="text-[12px] text-muted-foreground">{sub}</p>
      </div>
      <p className="text-[13px] text-muted-foreground">{message}</p>
    </div>
  );
}

function hostList(hosts: string[]) {
  const shown = hosts.slice(0, 2).join(" and ");
  return hosts.length > 2 ? `${shown} and ${hosts.length - 2} more` : shown;
}

// StatusLine says why the SEO numbers are missing or old, with the next step.
function StatusLine({ status, brand, go }: { status: SEOStatus; brand?: string; go: Props["go"] }) {
  const tone: Tone = status.state === "failed" ? "red" : status.state === "reading" ? "blue" : "amber";
  const label = { failed: "Read failed", reading: "Reading", off: "Turned off", not_followed: "Not followed", no_website: "No website", not_shared: "Not shared", not_connected: "", ready: "" }[status.state];
  const action =
    status.state === "no_website" ? <Button variant="ghost" onClick={() => go("brands", brand)}>Add the website</Button>
    : status.state === "not_shared" ? <Button variant="ghost" onClick={() => go("connectors", status.integration_id ?? undefined)}>Share it</Button>
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

