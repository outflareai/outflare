import { useEffect, useMemo, useRef, useState } from "react";
import type { components, SEOSite } from "@outflare/sdk";
import { useBrand, everyBrand } from "@/lib/brand";
import { track } from "../lib/analytics";
import { api, errorMessage } from "../api";
import { Button, Empty, ErrorText, Loading, field } from "../ui";
import type { Tab } from "../App";

// Search Console's Performance report, as Search Console shows it: a
// search type, dates and a period to compare with, filters by query, page,
// country, device and search appearance, the four totals that switch the
// chart's lines on and off, and a table for each dimension. Every number
// is asked live from the workspace's own Search Console (GET
// .../search/performance), so it matches Search Console for the same
// choices. On top of Search Console's own filters: branded or non-branded
// searches, questions, how many words a search has, positions, at least so
// many impressions, what was won or lost against the earlier period, days
// grouped into weeks or months, and the table to a CSV file. The choices
// stay in the address, so a link shows the same report.

type Report = components["schemas"]["SearchPerformance"];
type Metrics = components["schemas"]["SearchPerformanceMetrics"];
type Row = components["schemas"]["SearchPerformanceRow"];
type Point = components["schemas"]["SearchPerformancePoint"];
type Dimension = "query" | "page" | "country" | "device" | "searchAppearance" | "date";
type SearchType = "web" | "image" | "video" | "news" | "discover" | "googleNews";
type Operator = "contains" | "notContains" | "equals" | "notEquals" | "includingRegex" | "excludingRegex";
type Filter = { dimension: Exclude<Dimension, "date">; operator: Operator; expression: string };
type MetricKey = keyof Metrics;

const metricInfo: Record<MetricKey, { label: string; total: string; color: string }> = {
  clicks: { label: "Clicks", total: "Total clicks", color: "#1a73e8" },
  impressions: { label: "Impressions", total: "Total impressions", color: "#5e35b1" },
  ctr: { label: "CTR", total: "Average CTR", color: "#00897b" },
  position: { label: "Position", total: "Average position", color: "#e8710a" },
};
const metricKeys: MetricKey[] = ["clicks", "impressions", "ctr", "position"];
const types: { id: SearchType; label: string }[] = [
  { id: "web", label: "Web" },
  { id: "image", label: "Image" },
  { id: "video", label: "Video" },
  { id: "news", label: "News" },
  { id: "discover", label: "Discover" },
  { id: "googleNews", label: "Google News" },
];
const dims: { id: Dimension; label: string; one: string }[] = [
  { id: "query", label: "Queries", one: "Query" },
  { id: "page", label: "Pages", one: "Page" },
  { id: "country", label: "Countries", one: "Country" },
  { id: "device", label: "Devices", one: "Device" },
  { id: "searchAppearance", label: "Search appearance", one: "Search appearance" },
  { id: "date", label: "Dates", one: "Date" },
];
const operators: { id: Operator; label: string }[] = [
  { id: "contains", label: "Contains" },
  { id: "notContains", label: "Doesn't contain" },
  { id: "equals", label: "Exact" },
  { id: "notEquals", label: "Not exact" },
  { id: "includingRegex", label: "Matches regex" },
  { id: "excludingRegex", label: "Doesn't match regex" },
];
const ranges: { id: string; label: string; days: number }[] = [
  { id: "24h", label: "Last 24 hours", days: 1 },
  { id: "7d", label: "Last 7 days", days: 7 },
  { id: "28d", label: "Last 28 days", days: 28 },
  { id: "3m", label: "Last 3 months", days: 91 },
  { id: "6m", label: "Last 6 months", days: 182 },
  { id: "12m", label: "Last 12 months", days: 365 },
  { id: "16m", label: "Last 16 months", days: 486 },
];
type Compare = "none" | "previous" | "year" | "custom";
const compares: { id: Compare; label: string }[] = [
  { id: "none", label: "No comparison" },
  { id: "previous", label: "Previous period" },
  { id: "year", label: "Same period last year" },
  { id: "custom", label: "Custom dates" },
];
// Query shapes Search Console can't filter by itself, as RE2 patterns.
const questionRegex = "^(who|what|when|where|why|how|which|whose|can|could|does|do|is|are|should|will|would)\\b|\\?";
const wordRegex: Record<string, { pattern: string; label: string }> = {
  "1": { pattern: "^\\S+$", label: "One word" },
  "2-3": { pattern: "^\\S+(\\s+\\S+){1,2}$", label: "2–3 words" },
  "4+": { pattern: "^\\S+(\\s+\\S+){3,}$", label: "4 words or more" },
};
const positionBands: Record<string, [number, number]> = {
  "1-3": [0, 3.5],
  "4-10": [3.5, 10.5],
  "11-20": [10.5, 20.5],
  "21-50": [20.5, 50.5],
  "51+": [50.5, Infinity],
};
type Change = "all" | "new" | "lost" | "up" | "down";

const iso = (d: Date) => d.toISOString().slice(0, 10);
const addDays = (s: string, n: number) => iso(new Date(Date.parse(s + "T00:00:00Z") + n * 86400000));
const daysBetween = (a: string, b: string) => Math.round((Date.parse(b) - Date.parse(a)) / 86400000);
const yesterday = () => addDays(iso(new Date()), -1);
// The report reads like Search Console's in English: numbers, dates and
// country names in English whatever the browser's language.
const LOCALE = "en-US";
const num = (v: number, digits = 0) => v.toLocaleString(LOCALE, { maximumFractionDigits: digits, minimumFractionDigits: digits });
const showMetric = (k: MetricKey, v: number | undefined) =>
  v === undefined ? "–" : k === "ctr" ? `${num(v * 100, 1)}%` : k === "position" ? num(v, 1) : v >= 10000 ? compact(v) : num(v);
const compact = (v: number) => v.toLocaleString(LOCALE, { notation: "compact", maximumFractionDigits: 1 });
// An hour, as Search Console names it, in the viewer's time.
const isHour = (d: string) => d.includes("T");
const hourText = (d: string) => new Date(d).toLocaleString(LOCALE, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" });
const longDay = (d: string) => isHour(d) ? hourText(d) : new Date(d + "T00:00:00Z").toLocaleDateString(LOCALE, { month: "short", day: "numeric", year: "numeric", timeZone: "UTC" });

const ago = (at: string) => {
  const m = Math.max(0, Math.round((Date.now() - Date.parse(at)) / 60000));
  return m < 1 ? "just now" : m < 60 ? `${m} min ago` : `${Math.round(m / 6) / 10} hours ago`;
};

// Search Console names countries by ISO 3166-1 alpha-3; the browser names
// regions by alpha-2.
const alpha3 =
  "abwAWafgAFagoAOaiaAIalaAXalbALandADareAEargARarmAMasmASataAQatfTFatgAGausAUautATazeAZbdiBIbelBEbenBJbesBQbfaBFbgdBDbgrBGbhrBHbhsBSbihBAblmBLblrBYblzBZbmuBMbolBObraBRbrbBBbrnBNbtnBTbvtBVbwaBWcafCFcanCAcckCCcheCHchlCLchnCNcivCIcmrCMcodCDcogCGcokCKcolCOcomKMcpvCVcriCRcubCUcuwCWcxrCXcymKYcypCYczeCZdeuDEdjiDJdmaDMdnkDKdomDOdzaDZecuECegyEGeriEReshEHespESestEEethETfinFIfjiFJflkFKfraFRfroFOfsmFMgabGAgbrGBgeoGEggyGGghaGHgibGIginGNglpGPgmbGMgnbGWgnqGQgrcGRgrdGDgrlGLgtmGTgufGFgumGUguyGYhkgHKhmdHMhndHNhrvHRhtiHThunHUidnIDimnIMindINiotIOirlIEirnIRirqIQislISisrILitaITjamJMjeyJEjorJOjpnJPkazKZkenKEkgzKGkhmKHkirKIknaKNkorKRkwtKWlaoLAlbnLBlbrLRlbyLYlcaLClieLIlkaLKlsoLSltuLTluxLUlvaLVmacMOmafMFmarMAmcoMCmdaMDmdgMGmdvMVmexMXmhlMHmkdMKmliMLmltMTmmrMMmneMEmngMNmnpMPmozMZmrtMRmsrMSmtqMQmusMUmwiMWmysMYmytYTnamNAnclNCnerNEnfkNFngaNGnicNIniuNUnldNLnorNOnplNPnruNRnzlNZomnOMpakPKpanPApcnPNperPEphlPHplwPWpngPGpolPLpriPRprkKPprtPTpryPYpsePSpyfPFqatQAreuRErouROrusRUrwaRWsauSAsdnSDsenSNsgpSGsgsGSshnSHsjmSJslbSBsleSLslvSVsmrSMsomSOspmPMsrbRSssdSSstpSTsurSRsvkSKsvnSIsweSEswzSZsxmSXsycSCsyrSYtcaTCtcdTDtgoTGthaTHtjkTJtklTKtkmTMtlsTLtonTOttoTTtunTNturTRtuvTVtwnTWtzaTZugaUGukrUAumiUMuryUYusaUSuzbUZvatVAvctVCvenVEvgbVGvirVIvnmVNvutVUwlfWFwsmWSyemYEzafZAzmbZMzweZW";
export const toAlpha2 = new Map<string, string>();
for (let i = 0; i + 5 <= alpha3.length; i += 5) toAlpha2.set(alpha3.slice(i, i + 3), alpha3.slice(i + 3, i + 5));
let regions: Intl.DisplayNames | null = null;
function countryName(code: string) {
  const a2 = toAlpha2.get(code.toLowerCase());
  if (!a2) return code.toUpperCase();
  try {
    regions ??= new Intl.DisplayNames(LOCALE, { type: "region" });
    return regions.of(a2) ?? code.toUpperCase();
  } catch {
    return code.toUpperCase();
  }
}
const deviceName: Record<string, string> = { DESKTOP: "Desktop", MOBILE: "Mobile", TABLET: "Tablet" };
const label = (dim: Dimension, key: string) =>
  dim === "country" ? countryName(key) : dim === "device" ? (deviceName[key] ?? key) : dim === "date" ? longDay(key) : key.replace(/_/g, " ");

// The report's choices, kept in the address under p_ names.
type State = {
  site: string;
  type: SearchType;
  range: string;
  from: string;
  to: string;
  compare: Compare;
  cfrom: string;
  cto: string;
  dim: Dimension;
  filters: Filter[];
  metrics: MetricKey[];
  brandMode: "all" | "branded" | "nonbranded";
  brandRegex: string;
  questions: boolean;
  words: string;
  position: string;
  minImpressions: number;
  change: Change;
  group: "day" | "week" | "month";
};

function readState(): State {
  const q = new URLSearchParams(location.search);
  const get = (k: string) => q.get("p_" + k) ?? "";
  const filters: Filter[] = q.getAll("p_filter").flatMap((f) => {
    const [d, o, ...rest] = f.split(":");
    const e = rest.join(":");
    return dims.some((x) => x.id === d && d !== "date") && operators.some((x) => x.id === o) && e ? [{ dimension: d as Filter["dimension"], operator: o as Operator, expression: e }] : [];
  });
  const range = ranges.some((r) => r.id === get("range")) || get("range") === "custom" ? get("range") : "3m";
  const metrics = get("metrics").split(",").filter((m): m is MetricKey => metricKeys.includes(m as MetricKey));
  return {
    site: get("site"),
    type: types.some((t) => t.id === get("type")) ? (get("type") as SearchType) : "web",
    range,
    from: get("from"),
    to: get("to"),
    compare: compares.some((c) => c.id === get("compare")) ? (get("compare") as Compare) : "none",
    cfrom: get("cfrom"),
    cto: get("cto"),
    dim: dims.some((d) => d.id === get("dim")) ? (get("dim") as Dimension) : "query",
    filters,
    metrics: metrics.length ? metrics : ["clicks", "impressions"],
    brandMode: get("brand") === "branded" || get("brand") === "nonbranded" ? (get("brand") as State["brandMode"]) : "all",
    brandRegex: get("brand_regex"),
    questions: get("questions") === "1",
    words: get("words") in wordRegex ? get("words") : "",
    position: get("position") in positionBands ? get("position") : "",
    minImpressions: Math.max(0, Number(get("min_impressions")) || 0),
    change: (["new", "lost", "up", "down"] as Change[]).includes(get("change") as Change) ? (get("change") as Change) : "all",
    group: get("group") === "week" || get("group") === "month" ? (get("group") as State["group"]) : "day",
  };
}

function writeState(s: State) {
  const url = new URL(location.href);
  for (const k of [...url.searchParams.keys()]) if (k.startsWith("p_")) url.searchParams.delete(k);
  const set = (k: string, v: string, skip = "") => v && v !== skip && url.searchParams.set("p_" + k, v);
  set("site", s.site);
  set("type", s.type, "web");
  set("range", s.range, "3m");
  if (s.range === "custom") {
    set("from", s.from);
    set("to", s.to);
  }
  set("compare", s.compare, "none");
  if (s.compare === "custom") {
    set("cfrom", s.cfrom);
    set("cto", s.cto);
  }
  set("dim", s.dim, "query");
  for (const f of s.filters) url.searchParams.append("p_filter", `${f.dimension}:${f.operator}:${f.expression}`);
  set("metrics", s.metrics.join(","), "clicks,impressions");
  set("brand", s.brandMode, "all");
  set("brand_regex", s.brandRegex);
  set("questions", s.questions ? "1" : "");
  set("words", s.words);
  set("position", s.position);
  set("min_impressions", s.minImpressions ? String(s.minImpressions) : "");
  set("change", s.change, "all");
  set("group", s.group, "day");
  history.replaceState(history.state, "", url);
}

// The dates asked: the range, and the period compared with.
function periods(s: State) {
  let to = yesterday();
  let from = addDays(to, -((ranges.find((r) => r.id === s.range)?.days ?? 91) - 1));
  if (s.range === "custom" && s.from && s.to && s.from <= s.to) [from, to] = [s.from, s.to];
  const len = daysBetween(from, to) + 1;
  let cmp: [string, string] | null = null;
  if (s.compare === "previous") cmp = [addDays(from, -len), addDays(from, -1)];
  if (s.compare === "year") cmp = [addDays(from, -364), addDays(to, -364)];
  if (s.compare === "custom" && s.cfrom && s.cto && s.cfrom <= s.cto) cmp = [s.cfrom, s.cto];
  return { from, to, cmp };
}

// brandGuess is a first pattern for the brand's own searches: its name and
// the site's name, with or without spaces.
function brandGuess(names: string[]) {
  const parts = new Set<string>();
  for (const n of names) {
    const base = n.toLowerCase().replace(/^www\./, "").replace(/\.[a-z.]+$/, "").trim();
    if (base.length < 3) continue;
    parts.add(base.replace(/[^a-z0-9]+/g, "\\s?"));
  }
  return [...parts].join("|");
}

// The filters sent to Search Console: the ones picked, and the extra ones
// as query patterns.
function serverFilters(s: State): Filter[] {
  const out = [...s.filters];
  if (s.brandMode !== "all" && s.brandRegex) out.push({ dimension: "query", operator: s.brandMode === "branded" ? "includingRegex" : "excludingRegex", expression: s.brandRegex });
  if (s.questions) out.push({ dimension: "query", operator: "includingRegex", expression: questionRegex });
  if (s.words) out.push({ dimension: "query", operator: "includingRegex", expression: wordRegex[s.words].pattern });
  return out;
}

// group adds days up into weeks (Monday first) or months; CTR and
// position are weighted by impressions, as Search Console averages them.
function group(points: Point[], by: State["group"]): Point[] {
  if (by === "day" || points.some((p) => isHour(p.date))) return points;
  const buckets = new Map<string, { c: number; i: number; p: number }>();
  for (const x of points) {
    const d = new Date(x.date + "T00:00:00Z");
    const key = by === "month" ? x.date.slice(0, 7) + "-01" : iso(new Date(d.getTime() - ((d.getUTCDay() + 6) % 7) * 86400000));
    const b = buckets.get(key) ?? { c: 0, i: 0, p: 0 };
    b.c += x.clicks;
    b.i += x.impressions;
    b.p += x.position * x.impressions;
    buckets.set(key, b);
  }
  return [...buckets.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([date, b]) => ({ date, clicks: b.c, impressions: b.i, ctr: b.i ? b.c / b.i : 0, position: b.i ? b.p / b.i : 0 }));
}

export function SearchPerformance({ ws, go }: { ws: string; go: (tab: Tab, item?: string) => void }) {
  const { active, brand: picked, brands } = useBrand(ws);
  const [sites, setSites] = useState<SEOSite[] | null>(null);
  const [s, setS] = useState<State>(readState);
  const [report, setReport] = useState<Report | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [fresh, setFresh] = useState(0);
  const exportRef = useRef<(() => void) | null>(null);
  const [showExtra, setShowExtra] = useState(false);
  const update = (patch: Partial<State>) => setS((x) => ({ ...x, ...patch }));

  useEffect(() => {
    let live = true;
    setSites(null);
    const list = (brand: string) => api.GET("/v1/brands/{brand}/seo-sites", { params: { path: { brand } } });
    (picked ? list(picked.id).then((r) => r.data?.data ?? []) : everyBrand<SEOSite>(list)).then((all) => {
      if (!live) return;
      const unique = [...new Map(all.map((x) => [x.id, x])).values()];
      setSites(unique);
      setS((x) => (unique.some((u) => u.id === x.site) ? x : { ...x, site: unique[0]?.id ?? "" }));
    }, () => live && setError(errorMessage(undefined)));
    return () => {
      live = false;
    };
  }, [ws, picked?.id, active.length]);

  const site = sites?.find((x) => x.id === s.site);
  const brandId = site?.brand_id ?? picked?.id ?? active[0]?.id ?? "";
  // The brand pattern starts as a guess from the brand's and the site's
  // names, and can be edited.
  useEffect(() => {
    if (!site || s.brandRegex) return;
    const b = brands.find((x) => x.id === site.brand_id);
    const guess = brandGuess([b?.name ?? "", b?.handle ?? "", site.host]);
    if (guess) update({ brandRegex: guess });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [site?.id]);

  useEffect(() => writeState(s), [s]);
  const { from, to, cmp } = periods(s);
  const filters = serverFilters(s);
  const requestKey = JSON.stringify([s.site, brandId, s.type, s.dim, from, to, cmp, filters, fresh]);

  useEffect(() => {
    if (!site || !brandId) return;
    let live = true;
    setLoading(true);
    setError("");
    api
      .GET("/v1/brands/{brand}/seo-sites/{site}/search/performance", {
        params: {
          path: { brand: brandId, site: site.id },
          query: {
            from,
            to,
            compare_from: cmp?.[0],
            compare_to: cmp?.[1],
            type: s.type,
            dimension: s.dim,
            filter: filters.map((f) => `${f.dimension}:${f.operator}:${f.expression}`),
            limit: 5000,
            fresh: fresh > 0 ? true : undefined,
            ...(s.range === "24h" ? { hours: 24, compare_previous: s.compare !== "none" ? true : undefined, compare_from: undefined, compare_to: undefined } : {}),
          },
        },
      })
      .then(({ data, error }) => {
        if (!live) return;
        setLoading(false);
        if (error || !data) return setError(errorMessage(error));
        setReport(data);
      });
    return () => {
      live = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [requestKey]);

  if (sites === null) return error ? <ErrorText>{error}</ErrorText> : <Loading />;
  if (sites.length === 0)
    return (
      <Empty title="Add your website first" action={<Button onClick={() => go("audits")}>Add a site</Button>}>
        The Performance report reads the Search Console property that covers one of your sites.
      </Empty>
    );
  const noQueries = s.type === "discover" || s.type === "googleNews";
  const extraCount = [s.brandMode !== "all", s.questions, !!s.words, !!s.position, s.minImpressions > 0, s.change !== "all"].filter(Boolean).length;

  const quick = ["24h", "7d", "28d", "3m"];
  const seg = (on: boolean) =>
    "flex items-center gap-1.5 border-r px-3 py-1.5 text-[13px] last:border-r-0 " + (on ? "bg-[#e8f0fe] font-medium text-[#1967d2] dark:bg-[#1a73e8]/20" : "hover:bg-muted/50");
  const toggleMetric = (m: MetricKey) => update({ metrics: s.metrics.includes(m) ? (s.metrics.length > 1 ? s.metrics.filter((x) => x !== m) : s.metrics) : [...s.metrics, m] });

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-3">
          <h2 className="text-[20px] font-normal">Performance</h2>
          {sites.length > 1 && (
            <select className={field} aria-label="Site" value={s.site} onChange={(e) => update({ site: e.target.value, brandRegex: "" })}>
              {sites.map((x) => (
                <option key={x.id} value={x.id}>{x.host}</option>
              ))}
            </select>
          )}
        </div>
        <button type="button" className="flex items-center gap-2 px-2 py-1 text-[13px] font-medium tracking-wide hover:bg-muted/50 disabled:opacity-50" disabled={!report?.rows.length} onClick={() => exportRef.current?.()}>
          <span aria-hidden>⤓</span> EXPORT
        </button>
      </div>

      <div className="flex flex-wrap items-center gap-2 border-t pt-4">
        <div className="flex overflow-hidden rounded-md border">
          {quick.map((id) => (
            <button type="button" key={id} className={seg(s.range === id)} onClick={() => update({ range: id })}>
              {s.range === id && <span aria-hidden>✓</span>}
              {ranges.find((r) => r.id === id)!.label.replace("Last ", "")}
            </button>
          ))}
          <label className={seg(!quick.includes(s.range) || s.compare !== "none") + " relative cursor-pointer"}>
            {(!quick.includes(s.range) || s.compare !== "none") && <span aria-hidden>✓</span>}
            {!quick.includes(s.range) ? (ranges.find((r) => r.id === s.range)?.label.replace("Last ", "") ?? "Custom") : "More"}
            {s.compare !== "none" && " · compared"} ▾
            <select
              className="absolute inset-0 cursor-pointer opacity-0"
              aria-label="More dates and comparison"
              value=""
              onChange={(e) => {
                const v = e.target.value;
                if (v.startsWith("cmp:")) update({ compare: v.slice(4) as Compare, change: "all", ...(v === "cmp:custom" && !s.cfrom ? { cfrom: cmp?.[0] ?? from, cto: cmp?.[1] ?? to } : {}) });
                else if (v === "custom") update({ range: "custom", from, to });
                else if (v) update({ range: v });
              }}
            >
              <option value="">More</option>
              <optgroup label="Filter">
                {ranges.map((r) => (
                  <option key={r.id} value={r.id}>{r.label}</option>
                ))}
                <option value="custom">Custom range</option>
              </optgroup>
              <optgroup label="Compare">
                {compares.map((c) => (
                  <option key={c.id} value={"cmp:" + c.id}>{c.id === "none" ? "No comparison" : "Compare " + c.label.toLowerCase()}</option>
                ))}
              </optgroup>
            </select>
          </label>
        </div>
        {s.range === "custom" && (
          <span className="flex items-center gap-1">
            <input type="date" className={field} aria-label="From" value={s.from} max={s.to || yesterday()} onChange={(e) => update({ from: e.target.value })} />
            –
            <input type="date" className={field} aria-label="To" value={s.to} min={s.from} max={yesterday()} onChange={(e) => update({ to: e.target.value })} />
          </span>
        )}
        {s.compare === "custom" && (
          <span className="flex items-center gap-1 text-[13px]">
            vs
            <input type="date" className={field} aria-label="Compare from" value={s.cfrom} max={s.cto || yesterday()} onChange={(e) => update({ cfrom: e.target.value })} />
            –
            <input type="date" className={field} aria-label="Compare to" value={s.cto} min={s.cfrom} max={yesterday()} onChange={(e) => update({ cto: e.target.value })} />
          </span>
        )}
        <span className="mx-1 h-6 w-px bg-border" aria-hidden />
        <label className="relative flex cursor-pointer items-center gap-2 rounded-md border px-3 py-1.5 text-[13px] hover:bg-muted/50">
          Search type: {types.find((x) => x.id === s.type)?.label}
          {s.type === "web" && " (text)"} ▾
          <select
            className="absolute inset-0 cursor-pointer opacity-0"
            aria-label="Search type"
            value={s.type}
            onChange={(e) => {
              const type = e.target.value as SearchType;
              const nq = type === "discover" || type === "googleNews";
              update({ type, ...(nq ? { dim: s.dim === "query" ? "page" : s.dim, filters: s.filters.filter((f) => f.dimension !== "query"), brandMode: "all", questions: false, words: "" } : {}) });
            }}
          >
            {types.map((x) => (
              <option key={x.id} value={x.id}>{x.label}</option>
            ))}
          </select>
        </label>
        {s.filters.map((f, i) => (
          <span key={i} className="inline-flex items-center gap-1 rounded-md border border-[#1a73e8] bg-[#e8f0fe] px-3 py-1.5 text-[13px] text-[#1967d2] dark:bg-[#1a73e8]/20">
            <span>{dims.find((d) => d.id === f.dimension)?.one}:</span>
            <span className="opacity-80">{operators.find((o) => o.id === f.operator)?.label.toLowerCase()}</span>
            <span className="max-w-[220px] truncate font-medium">{f.dimension === "country" ? countryName(f.expression) : f.dimension === "device" ? (deviceName[f.expression] ?? f.expression) : f.expression}</span>
            <button type="button" aria-label="Remove filter" className="ml-1" onClick={() => update({ filters: s.filters.filter((_, j) => j !== i) })}>
              ×
            </button>
          </span>
        ))}
        <NewFilter noQueries={noQueries} onAdd={(f) => update({ filters: [...s.filters, f] })} />
        <button
          type="button"
          title="More filters: branded, questions, length, position, impressions, won or lost"
          aria-label="More filters"
          aria-pressed={showExtra}
          onClick={() => setShowExtra((x) => !x)}
          className={"relative rounded-md px-2.5 py-1.5 text-[15px] " + (showExtra ? "bg-[#1a73e8] text-white" : "bg-[#1a73e8]/90 text-white hover:bg-[#1a73e8]")}
        >
          ☰{extraCount > 0 && <span className="absolute -right-1.5 -top-1.5 rounded-full bg-[#e8710a] px-1.5 text-[10px] leading-4">{extraCount}</span>}
        </button>
        <span className="ml-auto text-[13px] text-muted-foreground">
          {loading ? "Loading…" : report?.fetched_at ? <>Last update: {ago(report.fetched_at)} · <button type="button" className="underline" onClick={() => setFresh((n) => n + 1)}>refresh</button></> : null}
        </span>
      </div>

      {showExtra && (
        <div className="rounded-lg border bg-muted/30 p-3">
          <ExtraFilters s={s} update={update} noQueries={noQueries} />
        </div>
      )}

      {error && <ErrorText>{error}</ErrorText>}
      {!report && !error && <Loading />}
      {report && (
        <div className={loading ? "space-y-4 opacity-60 transition-opacity" : "space-y-4"}>
          {!report.source ? (
            <Empty title="Connect Search Console" action={<Button onClick={() => go("connectors")}>Connect</Button>}>
              {report.notes.join(" ")}
            </Empty>
          ) : (
            <>
              <div className="overflow-hidden rounded-xl border bg-background">
                <div className="flex flex-wrap items-start justify-between">
                  <Totals report={report} metrics={s.metrics} onToggle={toggleMetric} />
                  <div className={"p-4" + (s.range === "24h" ? " invisible" : "")}>
                    <select className={field} aria-label="Group days" value={s.group} onChange={(e) => update({ group: e.target.value as State["group"] })}>
                      <option value="day">Daily</option>
                      <option value="week">Weekly</option>
                      <option value="month">Monthly</option>
                    </select>
                  </div>
                </div>
                <div className="px-4 pb-4 pt-2">
                  <Chart series={group(report.series, s.group)} previous={group(report.previous_series, s.group)} metrics={s.metrics} />
                </div>
                <p className="px-4 pb-3 text-[12px] text-muted-foreground">
                  {report.source.property} · {longDay(report.from)} – {longDay(report.to)}
                  {report.compare_from && report.compare_to && <> vs {longDay(report.compare_from)} – {longDay(report.compare_to)}</>}
                </p>
              </div>
              <Table
                report={report}
                s={s}
                update={update}
                noQueries={noQueries}
                exportRef={exportRef}
                onPick={(key) => {
                  if (s.dim === "date") return update({ range: "custom", from: key, to: key });
                  update({ filters: [...s.filters.filter((f) => f.dimension !== s.dim), { dimension: s.dim as Filter["dimension"], operator: "equals", expression: key }], dim: s.dim === "query" ? "page" : "query" });
                }}
              />
              {report.notes.length > 0 && <p className="text-[12px] text-muted-foreground">{report.notes.join(" ")}</p>}
            </>
          )}
        </div>
      )}
    </div>
  );
}

// NewFilter adds one of Search Console's filters.
function NewFilter({ noQueries, onAdd }: { noQueries: boolean; onAdd: (f: Filter) => void }) {
  const [open, setOpen] = useState(false);
  const [f, setF] = useState<Filter>({ dimension: noQueries ? "page" : "query", operator: "contains", expression: "" });
  if (!open)
    return (
      <button type="button" className="flex items-center gap-2 rounded-md border px-3 py-1.5 text-[13px] hover:bg-muted/50" onClick={() => setOpen(true)}>
        <span className="text-[16px] leading-none">+</span> Add filter
      </button>
    );
  const add = () => {
    const expression = f.expression.trim();
    if (!expression) return;
    onAdd({ ...f, expression: f.dimension === "country" ? expression.toLowerCase() : f.dimension === "device" ? expression.toUpperCase() : expression });
    setF({ ...f, expression: "" });
    setOpen(false);
  };
  return (
    <span className="inline-flex flex-wrap items-center gap-2 rounded-md border p-1">
      <select className={field} aria-label="Filter by" value={f.dimension} onChange={(e) => setF({ ...f, dimension: e.target.value as Filter["dimension"] })}>
        {dims
          .filter((d) => d.id !== "date" && !(noQueries && d.id === "query"))
          .map((d) => (
            <option key={d.id} value={d.id}>{d.one}</option>
          ))}
      </select>
      <select className={field} aria-label="Match" value={f.operator} onChange={(e) => setF({ ...f, operator: e.target.value as Operator })}>
        {operators.map((o) => (
          <option key={o.id} value={o.id}>{o.label}</option>
        ))}
      </select>
      {f.dimension === "device" ? (
        <select className={field} aria-label="Device" value={f.expression} onChange={(e) => setF({ ...f, expression: e.target.value })}>
          <option value="">Pick a device</option>
          {Object.entries(deviceName).map(([k, v]) => (
            <option key={k} value={k}>{v}</option>
          ))}
        </select>
      ) : (
        <input
          className={field}
          autoFocus
          placeholder={f.dimension === "country" ? "Country code, e.g. deu" : f.operator.endsWith("Regex") ? "RE2 pattern" : "Text"}
          value={f.expression}
          onChange={(e) => setF({ ...f, expression: e.target.value })}
          onKeyDown={(e) => e.key === "Enter" && add()}
        />
      )}
      <Button onClick={add}>Apply</Button>
      <Button variant="ghost" onClick={() => setOpen(false)}>Cancel</Button>
    </span>
  );
}

// ExtraFilters are the filters Search Console lacks.
function ExtraFilters({ s, update, noQueries }: { s: State; update: (p: Partial<State>) => void; noQueries: boolean }) {
  return (
    <div className="flex flex-wrap items-center gap-2 text-[13px]">
      {!noQueries && (
        <>
          <select className={field} aria-label="Branded searches" value={s.brandMode} onChange={(e) => update({ brandMode: e.target.value as State["brandMode"] })}>
            <option value="all">Branded and non-branded</option>
            <option value="nonbranded">Non-branded only</option>
            <option value="branded">Branded only</option>
          </select>
          {s.brandMode !== "all" && (
            <input className={field} aria-label="Brand pattern" title="An RE2 pattern for the brand's own searches" value={s.brandRegex} onChange={(e) => update({ brandRegex: e.target.value })} placeholder="brand|brand name" />
          )}
          <select className={field} aria-label="Words" value={s.words} onChange={(e) => update({ words: e.target.value })}>
            <option value="">Any length</option>
            {Object.entries(wordRegex).map(([k, v]) => (
              <option key={k} value={k}>{v.label}</option>
            ))}
          </select>
          <label className="inline-flex items-center gap-1.5">
            <input type="checkbox" checked={s.questions} onChange={(e) => update({ questions: e.target.checked })} /> Questions only
          </label>
        </>
      )}
      <select className={field} aria-label="Position" value={s.position} onChange={(e) => update({ position: e.target.value })}>
        <option value="">Any position</option>
        {Object.keys(positionBands).map((k) => (
          <option key={k} value={k}>Position {k}</option>
        ))}
      </select>
      <label className="inline-flex items-center gap-1.5">
        At least
        <input type="number" min={0} className={field + " w-20"} aria-label="Minimum impressions" value={s.minImpressions || ""} onChange={(e) => update({ minImpressions: Math.max(0, Number(e.target.value) || 0) })} />
        impressions
      </label>
      {s.compare !== "none" && (
        <select className={field} aria-label="Change" value={s.change} onChange={(e) => update({ change: e.target.value as Change })}>
          <option value="all">All rows</option>
          <option value="new">New (none before)</option>
          <option value="lost">Lost (none now)</option>
          <option value="up">More clicks</option>
          <option value="down">Fewer clicks</option>
        </select>
      )}
    </div>
  );
}

// Totals are Search Console's four boxes: each switches its line on the
// chart on or off.
function Totals({ report, metrics, onToggle }: { report: Report; metrics: MetricKey[]; onToggle: (m: MetricKey) => void }) {
  return (
    <div className="grid grid-cols-2 md:flex">
      {metricKeys.map((k) => {
        const on = metrics.includes(k);
        const v = report.totals[k];
        const p = report.previous_totals?.[k];
        const diff = p !== undefined && p !== null ? v - p : null;
        const better = diff === null || diff === 0 ? null : k === "position" ? diff < 0 : diff > 0;
        return (
          <button
            type="button"
            key={k}
            onClick={() => onToggle(k)}
            className="min-w-[180px] border-r px-5 pb-4 pt-5 text-left transition-colors md:w-[220px]"
            style={on ? { background: metricInfo[k].color, borderColor: metricInfo[k].color, color: "white" } : undefined}
            aria-pressed={on}
          >
            <div className="flex items-center gap-2 text-[14px]">
              <span className={"inline-flex h-4 w-4 items-center justify-center rounded-[3px] border-2 text-[11px] leading-none " + (on ? "border-white" : "border-muted-foreground")}>{on ? "✓" : ""}</span>
              <span className={on ? "" : "text-muted-foreground"}>{metricInfo[k].total}</span>
            </div>
            <div className={"mt-2 text-[34px] font-normal leading-tight " + (on ? "" : "text-muted-foreground")}>{showMetric(k, v)}</div>
            {diff !== null && p !== undefined && (
              <div className={"text-[12px] " + (on ? "opacity-90" : better === null ? "text-muted-foreground" : better ? "text-green-700 dark:text-green-400" : "text-red-700 dark:text-red-400")}>
                {diff > 0 ? "+" : ""}
                {k === "ctr" ? `${num(diff * 100, 1)} pp` : k === "position" ? num(diff, 1) : num(diff)} · was {showMetric(k, p)}
              </div>
            )}
          </button>
        );
      })}
    </div>
  );
}

// Chart draws each metric switched on against its own scale, as Search
// Console does: with one or two metrics on, the first one's axis on the
// left and the second's on the right; position upside down (up is
// better); the period compared with dashed; every value of a day on hover.
const niceMax = (v: number) => {
  if (v <= 0) return 1;
  const e = Math.pow(10, Math.floor(Math.log10(v)));
  return ([1, 1.2, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10].find((m) => m * e >= v) ?? 10) * e;
};
const axisLabel = (k: MetricKey, v: number) => (k === "ctr" ? `${num(v * 100, v < 0.1 ? 1 : 0)}%` : k === "position" ? num(v, v < 10 ? 1 : 0) : v >= 1000 ? compact(v).toUpperCase() : num(v));
const slashDay = (d: string) => isHour(d) ? new Date(d).toLocaleTimeString(LOCALE, { hour: "numeric" }) : new Date(d + "T00:00:00Z").toLocaleDateString(LOCALE, { month: "numeric", day: "numeric", year: "2-digit", timeZone: "UTC" });

function Chart({ series, previous, metrics }: { series: Point[]; previous: Point[]; metrics: MetricKey[] }) {
  const ref = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(800);
  const [hover, setHover] = useState<number | null>(null);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const ro = new ResizeObserver(([e]) => setWidth(Math.max(280, e.contentRect.width)));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  const axes = metrics.length <= 2 ? metrics : [];
  const h = 320;
  const pad = { l: axes[0] ? 48 : 8, r: axes[1] ? 56 : 8, t: 28, b: 32 };
  const n = Math.max(series.length, previous.length);
  const x = (i: number) => pad.l + (n <= 1 ? (width - pad.l - pad.r) / 2 : (i / (n - 1)) * (width - pad.l - pad.r));
  const scales = useMemo(() => {
    const out = {} as Record<MetricKey, { min: number; max: number }>;
    for (const k of metricKeys) {
      const vals = [...series, ...previous].filter((p) => !(k === "position" && p.impressions === 0)).map((p) => p[k]);
      if (k === "position") out[k] = { min: 0, max: niceMax(Math.max(...vals, 1)) };
      else out[k] = { min: 0, max: niceMax(Math.max(...vals, k === "ctr" ? 0.01 : 1)) };
    }
    return out;
  }, [series, previous]);
  const y = (k: MetricKey, v: number) => {
    const { min, max } = scales[k];
    const span = max - min || 1;
    const f = k === "position" ? (v - min) / span : 1 - (v - min) / span;
    return pad.t + f * (h - pad.t - pad.b);
  };
  // A line with a gap where a day has no position.
  const path = (pts: Point[], k: MetricKey) => {
    let d = "";
    let pen = false;
    pts.forEach((p, i) => {
      if (k === "position" && p.impressions === 0) {
        pen = false;
        return;
      }
      d += `${pen ? "L" : "M"}${x(i).toFixed(1)},${y(k, p[k]).toFixed(1)}`;
      pen = true;
    });
    return d;
  };
  if (n === 0) return <Empty title="No data for these choices">Search Console has nothing for these dates and filters.</Empty>;
  const count = Math.max(2, Math.min(11, Math.floor((width - pad.l - pad.r) / 90)));
  const ticks = [...new Set(Array.from({ length: count }, (_, j) => Math.round((j * (n - 1)) / (count - 1))))];
  const grid = [0, 0.25, 0.5, 0.75, 1];
  return (
    <div ref={ref} className="relative">
      <svg width={width} height={h} role="img" aria-label="Performance over time" onMouseLeave={() => setHover(null)}
        onMouseMove={(e) => {
          const r = (e.currentTarget as SVGSVGElement).getBoundingClientRect();
          const i = Math.round(((e.clientX - r.left - pad.l) / (width - pad.l - pad.r)) * (n - 1));
          setHover(Math.min(n - 1, Math.max(0, i)));
        }}
      >
        {grid.map((f) => (
          <line key={f} x1={pad.l} x2={width - pad.r} y1={pad.t + f * (h - pad.t - pad.b)} y2={pad.t + f * (h - pad.t - pad.b)} stroke="currentColor" strokeOpacity={f === 1 ? 0.3 : 0.1} />
        ))}
        {axes.map((k, side) => (
          <g key={k} fontSize={12} fill="currentColor" opacity={0.65}>
            <text x={side ? width - 4 : 4} y={12} textAnchor={side ? "end" : "start"}>{metricInfo[k].label}</text>
            {grid.map((f) => {
              const v = k === "position" ? scales[k].min + f * (scales[k].max - scales[k].min) : scales[k].max * (1 - f);
              return (
                <text key={f} x={side ? width - pad.r + 8 : pad.l - 8} y={pad.t + f * (h - pad.t - pad.b) + 4} textAnchor={side ? "start" : "end"}>
                  {axisLabel(k, v)}
                </text>
              );
            })}
          </g>
        ))}
        {metrics.map((k) => (
          <g key={k} fill="none" stroke={metricInfo[k].color} strokeWidth={2} strokeLinejoin="round">
            {previous.length > 0 && <path d={path(previous, k)} strokeDasharray="4 4" strokeOpacity={0.6} />}
            <path d={path(series, k)} />
          </g>
        ))}
        {ticks.map((i) => series[i] && (
          <text key={i} x={x(i)} y={h - 8} fontSize={12} textAnchor="middle" fill="currentColor" opacity={0.65}>
            {slashDay(series[i].date)}
          </text>
        ))}
        {hover !== null && <line x1={x(hover)} x2={x(hover)} y1={pad.t} y2={h - pad.b} stroke="currentColor" strokeOpacity={0.3} />}
        {hover !== null && series[hover] && metrics.map((k) => !(k === "position" && series[hover].impressions === 0) && (
          <circle key={k} cx={x(hover)} cy={y(k, series[hover][k])} r={4} fill={metricInfo[k].color} />
        ))}
      </svg>
      {hover !== null && (series[hover] || previous[hover]) && (
        <div className="pointer-events-none absolute top-6 min-w-[180px] rounded-md border bg-background p-2 text-[12px] shadow" style={{ left: Math.min(x(hover) + 12, width - 220) }}>
          {series[hover] && <div className="mb-1 font-medium">{longDay(series[hover].date)}</div>}
          {metrics.map((k) => (
            <div key={k} className="flex gap-3">
              <span className="flex items-center gap-1.5"><span className="h-2 w-2 rounded-full" style={{ background: metricInfo[k].color }} />{metricInfo[k].label}</span>
              <span className="ml-auto font-medium">{series[hover] ? showMetric(k, series[hover][k]) : "–"}</span>
              {previous[hover] && <span className="text-muted-foreground">was {showMetric(k, previous[hover][k])}</span>}
            </div>
          ))}
          {previous[hover] && <div className="mt-1 text-muted-foreground">against {longDay(previous[hover].date)}</div>}
        </div>
      )}
    </div>
  );
}

type SortKey = MetricKey | "key" | `diff_${MetricKey}`;

// Table is one dimension's rows, as Search Console lists them: the
// metrics switched on, with the period compared with and the difference;
// search, sort, pages and a CSV file. A row's key opens the report filtered
// to it.
function Table({ report, s, update, noQueries, onPick, exportRef }: { report: Report; s: State; update: (p: Partial<State>) => void; noQueries: boolean; onPick: (key: string) => void; exportRef: React.MutableRefObject<(() => void) | null> }) {
  const [search, setSearch] = useState("");
  const [sort, setSort] = useState<{ key: SortKey; desc: boolean }>({ key: "clicks", desc: true });
  const [page, setPage] = useState(0);
  const [size, setSize] = useState(25);
  const comparing = report.compare_from !== null;
  useEffect(() => setPage(0), [report, search, s.position, s.minImpressions, s.change, size]);
  useEffect(() => setSort(s.dim === "date" ? { key: "key", desc: true } : { key: "clicks", desc: true }), [s.dim]);

  const rows = useMemo(() => {
    const band = s.position ? positionBands[s.position] : null;
    const needle = search.trim().toLowerCase();
    const out = report.rows.filter((r) => {
      if (needle && !label(s.dim, r.key).toLowerCase().includes(needle) && !r.key.toLowerCase().includes(needle)) return false;
      if (band && (r.impressions === 0 || r.position < band[0] || r.position >= band[1])) return false;
      if (s.minImpressions && Math.max(r.impressions, r.previous?.impressions ?? 0) < s.minImpressions) return false;
      if (comparing && s.change !== "all") {
        const before = r.previous?.clicks ?? 0;
        if (s.change === "new" && (r.previous !== null || r.impressions === 0)) return false;
        if (s.change === "lost" && (r.impressions > 0 || r.previous === null)) return false;
        if (s.change === "up" && !(r.clicks > before)) return false;
        if (s.change === "down" && !(r.clicks < before)) return false;
      }
      return true;
    });
    const val = (r: Row): number | string => {
      if (sort.key === "key") return r.key;
      if (sort.key.startsWith("diff_")) {
        const k = sort.key.slice(5) as MetricKey;
        return r[k] - (r.previous?.[k] ?? 0);
      }
      return r[sort.key as MetricKey];
    };
    return out.sort((a, b) => {
      const va = val(a), vb = val(b);
      const c = typeof va === "string" ? va.localeCompare(vb as string) : (va as number) - (vb as number);
      return sort.desc ? -c : c;
    });
  }, [report, s.dim, s.position, s.minImpressions, s.change, search, sort, comparing]);

  const shown = rows.slice(page * size, page * size + size);
  const cols = metricKeys.filter((k) => s.metrics.includes(k));
  const head = (key: SortKey, text: string, right = true) => (
    <th key={key} className={"cursor-pointer select-none whitespace-nowrap px-2 py-2 font-medium " + (right ? "text-right" : "text-left")} onClick={() => setSort((x) => ({ key, desc: x.key === key ? !x.desc : key !== "key" }))}>
      {text}
      {sort.key === key ? (sort.desc ? " ↓" : " ↑") : ""}
    </th>
  );
  const exportCSV = () => {
    const esc = (v: string | number) => (typeof v === "number" ? String(v) : `"${v.replace(/"/g, '""')}"`);
    const header = [dims.find((d) => d.id === s.dim)?.one ?? "Key", ...cols.flatMap((k) => (comparing ? [metricInfo[k].label, metricInfo[k].label + " before", metricInfo[k].label + " change"] : [metricInfo[k].label]))];
    const lines = rows.map((r) =>
      [label(s.dim, r.key), ...cols.flatMap((k) => (comparing ? [r[k], r.previous?.[k] ?? 0, r[k] - (r.previous?.[k] ?? 0)] : [r[k]]))].map(esc).join(","),
    );
    const blob = new Blob([[header.map(esc).join(","), ...lines].join("\n")], { type: "text/csv" });
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = `search-performance-${s.dim}-${report.from}-${report.to}.csv`;
    a.click();
    URL.revokeObjectURL(a.href);
    track("ui.search_performance_exported", { dimension: s.dim, rows: rows.length, comparing });
  };
  exportRef.current = exportCSV;

  return (
    <div className="overflow-hidden rounded-xl border bg-background">
      <div className="flex overflow-x-auto border-b">
        {dims
          .filter((d) => !(noQueries && d.id === "query"))
          .map((d) => (
            <button
              type="button"
              key={d.id}
              onClick={() => update({ dim: d.id })}
              className={"flex-1 whitespace-nowrap border-b-[3px] px-4 py-4 text-[14px] font-medium tracking-wide " + (s.dim === d.id ? "border-[#1a73e8] text-foreground" : "border-transparent text-muted-foreground hover:text-foreground")}
            >
              {(d.id === "date" ? (s.range === "24h" ? "Hours" : "Days") : d.label).toUpperCase()}
            </button>
          ))}
      </div>
      <div className="flex justify-end px-3 py-2">
        <input className={field} placeholder="Filter rows" aria-label="Filter rows" value={search} onChange={(e) => setSearch(e.target.value)} />
      </div>
      <div className="overflow-x-auto">
        <table className="w-full text-[13px]">
          <thead className="border-b text-muted-foreground">
            <tr>
              {head("key", dims.find((d) => d.id === s.dim)?.one ?? "", false)}
              {cols.flatMap((k) =>
                comparing
                  ? [head(k, metricInfo[k].label), <th key={k + "_before"} className="px-2 py-2 text-right font-medium">Before</th>, head(`diff_${k}`, "Change")]
                  : [head(k, metricInfo[k].label)],
              )}
            </tr>
          </thead>
          <tbody>
            {shown.map((r) => (
              <tr key={r.key} className="border-b last:border-0 hover:bg-muted/40">
                <td className="max-w-[420px] px-2 py-1.5">
                  <span className="flex items-center gap-2">
                    <button type="button" className="truncate text-left hover:underline" title={r.key} onClick={() => onPick(r.key)}>
                      {label(s.dim, r.key)}
                    </button>
                    {s.dim === "page" && (
                      <a href={r.key} target="_blank" rel="noreferrer" className="shrink-0 text-muted-foreground" aria-label="Open page">
                        ↗
                      </a>
                    )}
                  </span>
                </td>
                {cols.flatMap((k) => {
                  const before = r.previous?.[k];
                  const diff = before === undefined ? null : r[k] - before;
                  const better = diff === null || diff === 0 ? null : k === "position" ? diff < 0 : diff > 0;
                  const cells = [
                    <td key={k} className="px-2 py-1.5 text-right tabular-nums">{k === "position" && r.impressions === 0 ? "–" : showMetric(k, r[k])}</td>,
                  ];
                  if (comparing)
                    cells.push(
                      <td key={k + "_before"} className="px-2 py-1.5 text-right tabular-nums text-muted-foreground">{before === undefined ? "–" : showMetric(k, before)}</td>,
                      <td key={k + "_diff"} className={"px-2 py-1.5 text-right tabular-nums " + (better === null ? "text-muted-foreground" : better ? "text-green-700 dark:text-green-400" : "text-red-700 dark:text-red-400")}>
                        {diff === null ? "new" : r.impressions === 0 ? "lost" : (diff > 0 ? "+" : "") + (k === "ctr" ? `${num(diff * 100, 1)} pp` : k === "position" ? num(diff, 1) : num(diff))}
                      </td>,
                    );
                  return cells;
                })}
              </tr>
            ))}
            {shown.length === 0 && (
              <tr>
                <td colSpan={1 + cols.length * (comparing ? 3 : 1)} className="px-2 py-6 text-center text-muted-foreground">No rows for these choices.</td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
      <div className="flex flex-wrap items-center justify-end gap-3 border-t px-2 py-2 text-[12px] text-muted-foreground">
        <label className="inline-flex items-center gap-1">
          Rows per page
          <select className={field} value={size} onChange={(e) => setSize(Number(e.target.value))}>
            {[10, 25, 50, 100, 500].map((n) => (
              <option key={n} value={n}>{n}</option>
            ))}
          </select>
        </label>
        <span>
          {rows.length === 0 ? 0 : page * size + 1}–{Math.min(rows.length, page * size + size)} of {num(rows.length)}
        </span>
        <Button variant="ghost" disabled={page === 0} onClick={() => setPage((p) => p - 1)}>‹</Button>
        <Button variant="ghost" disabled={(page + 1) * size >= rows.length} onClick={() => setPage((p) => p + 1)}>›</Button>
      </div>
    </div>
  );
}
