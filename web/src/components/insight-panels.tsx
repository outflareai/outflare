import { useEffect, useMemo, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { api } from "../api";
import { InsightTile } from "../pages/Dashboards";
import { TrendView } from "../pages/Trends";
import { track } from "@/lib/analytics";

// Insight panels: the funnels and trends Outflare shows on its own pages
// (Paid ads, Visitors and Home), built from what the brand has, so nobody
// has to save one first. Each panel opens in Funnels or Trends with the
// same spec, ready to change and save.

type Catalog = Schemas["InsightsCatalog"];
type FunnelSpec = Schemas["FunnelSpec"];
type FunnelStep = Schemas["FunnelStep"];
type TrendSpec = Schemas["TrendSpec"];
type DateRange = Schemas["InsightDateRange"];
type RetentionSpec = Schemas["RetentionSpec"];
type PathsSpec = Schemas["PathsSpec"];
type LifecycleSpec = Schemas["LifecycleSpec"];

export type PanelKind = "funnel" | "trend" | "retention" | "paths" | "lifecycle";
export type PanelGo = (tab: "funnels" | "trends" | "retention" | "paths" | "dashboards") => void;

const draftKey = "outflare:insight-draft";

// openDraft hands a spec to the Funnels or Trends page, which takes it as
// its starting point once, then navigates there.
export function openDraft(kind: PanelKind, spec: FunnelSpec | TrendSpec | RetentionSpec | PathsSpec | LifecycleSpec, go: PanelGo, from: string) {
  try {
    sessionStorage.setItem(draftKey, JSON.stringify({ kind, spec }));
  } catch {
    // Private windows: the page opens with its usual starter instead.
  }
  track("ui.insight_panel_opened", { kind, from });
  go(kind === "funnel" ? "funnels" : kind === "trend" ? "trends" : kind === "paths" ? "paths" : "retention");
}

// peekDraft says which kind of spec a panel handed over, if any, without
// taking it: the Retention page uses it to open on the right view.
export function peekDraft(): PanelKind | null {
  try {
    const raw = sessionStorage.getItem(draftKey);
    return raw ? (JSON.parse(raw) as { kind: PanelKind }).kind : null;
  } catch {
    return null;
  }
}

// takeDraft reads the spec a panel handed over, once.
export function takeDraft<T extends FunnelSpec | TrendSpec | RetentionSpec | PathsSpec | LifecycleSpec>(kind: PanelKind): T | null {
  try {
    const raw = sessionStorage.getItem(draftKey);
    if (!raw) return null;
    sessionStorage.removeItem(draftKey);
    const d = JSON.parse(raw) as { kind: PanelKind; spec: T };
    return d.kind === kind ? d.spec : null;
  } catch {
    return null;
  }
}

// useInsightsCatalog loads what the brand has to build panels from: its
// conversions, the events seen lately and the ad networks connected.
export function useInsightsCatalog(brand: string): Catalog | null {
  const [catalog, setCatalog] = useState<Catalog | null>(null);
  useEffect(() => {
    setCatalog(null);
    if (!brand) return;
    api.GET("/v1/brands/{brand}/insights/catalog", { params: { path: { brand } } }).then(({ data }) => setCatalog(data ?? null));
  }, [brand]);
  return catalog;
}

// signUpStep is the brand's own idea of a sign-up: its first conversion
// definition, else a signed_up event if the site sends one, else nothing.
export function signUpStep(catalog: Catalog): FunnelStep | null {
  const c = catalog.conversions[0];
  if (c) return { kind: "conversion", conversion: c.key, label: c.name };
  if (catalog.events.some((e) => e.name === "signed_up")) return { kind: "event", event: "signed_up", label: "Sign-up" };
  return null;
}

export function daysRange(days: number): DateRange {
  return days === 7 ? { preset: "last_7_days" } : days === 14 ? { preset: "last_14_days" } : days === 90 ? { preset: "last_90_days" } : days === 30 ? { preset: "last_30_days" } : { from: dayAgo(days - 1), to: dayAgo(0) };
}

function dayAgo(n: number): string {
  return new Date(Date.now() - n * 86_400_000).toISOString().slice(0, 10);
}

// Panel draws one built-in insight as a tile with a link to open it.
export function Panel({ brand, kind, name, spec, go, from, height = 320 }: { brand: string; kind: PanelKind; name: string; spec: FunnelSpec | TrendSpec | RetentionSpec; go: PanelGo; from: string; height?: number }) {
  const insight = useMemo(() => ({ kind, name, spec }), [kind, name, spec]);
  return (
    <div className="relative" style={{ height }}>
      <InsightTile insight={insight} brand={brand} range={undefined} compare={kind === "trend" && !!(spec as TrendSpec).compare} />
      <button
        type="button"
        className="absolute top-1.5 right-20 font-mono text-[11px] text-muted-foreground hover:text-foreground"
        onClick={() => openDraft(kind, spec, go, from)}
      >
        {kind === "funnel" ? "Open in Funnels →" : kind === "trend" ? "Open in Trends →" : kind === "paths" ? "Open in Paths →" : "Open in Retention →"}
      </button>
    </div>
  );
}

// AdsPanels is Paid ads' own view of what the spend brought: the people
// funnel under the networks' clicks, spend beside sign-ups over time, and
// the cost of each sign-up.
export function AdsPanels({ brand, days, go }: { brand: string; days: number; go: PanelGo }) {
  const catalog = useInsightsCatalog(brand);
  if (!catalog || !catalog.networks.some((n) => n.connected)) return null;
  const range = daysRange(days);
  const signUp = signUpStep(catalog);
  const funnel: FunnelSpec = {
    date_range: range,
    ad_clicks: {},
    steps: [{ kind: "visit", filters: [{ property: "network", op: "exists" }], label: "Visited from an ad" }, ...(signUp ? [{ ...signUp, within: { value: 7, unit: "day" as const } }] : []), { kind: "payment", first: true, label: "First payment", within: { value: 14, unit: "day" } }],
    breakdown: "network",
  };
  const outcome: Schemas["TrendSeries"] = signUp
    ? signUp.kind === "conversion"
      ? { kind: "conversion", conversion: signUp.conversion, label: signUp.label }
      : { kind: "event", event: signUp.event, label: signUp.label }
    : { kind: "payment", first: true, label: "First payments" };
  const over: TrendSpec = {
    date_range: range,
    interval: days > 60 ? "week" : "day",
    display: "line",
    series: [{ kind: "ad", measure: "spend", label: "Ad spend" }, { kind: "visit", filters: [{ property: "network", op: "exists" }], label: "Visits from ads" }, outcome],
  };
  const cost: TrendSpec = { date_range: range, interval: "day", display: "number", compare: true, series: [{ kind: "ad", measure: "spend", label: "Ad spend" }, outcome], formula: "A / B" };
  return (
    <section aria-labelledby="ads-insights" className="space-y-3">
      <div className="flex items-center">
        <h2 id="ads-insights" className="flex-1 text-[13px] font-medium text-muted-foreground">What the ads brought · {days} days</h2>
        <button onClick={() => go("dashboards")} className="font-mono text-[12px] text-muted-foreground hover:text-foreground">Dashboards →</button>
      </div>
      <Panel brand={brand} kind="funnel" name="From ad click to first payment" spec={funnel} go={go} from="ads" height={500} />
      <div className="grid gap-3 lg:grid-cols-3">
        <div className="lg:col-span-2">
          <Panel brand={brand} kind="trend" name="Spend, visits and sign-ups" spec={over} go={go} from="ads" />
        </div>
        <Panel brand={brand} kind="trend" name={signUp ? `Cost per ${signUp.label?.toLowerCase() ?? "sign-up"}` : "Cost per first payment"} spec={cost} go={go} from="ads" />
      </div>
    </section>
  );
}

// VisitorsPanels is the Visitors page's view of the traffic as a whole:
// visits and people over time, and how many went on to sign up and pay.
export function VisitorsPanels({ brand, days, go }: { brand: string; days: number; go: PanelGo }) {
  const catalog = useInsightsCatalog(brand);
  if (!catalog) return null;
  const range = daysRange(days);
  const signUp = signUpStep(catalog);
  const over: TrendSpec = {
    date_range: range,
    interval: days > 60 ? "week" : "day",
    display: "line",
    compare: true,
    series: [{ kind: "visit", label: "Visits" }, { kind: "visit", measure: "people", label: "People" }],
  };
  const funnel: FunnelSpec = {
    date_range: range,
    steps: [{ kind: "visit", label: "Visited" }, ...(signUp ? [{ ...signUp, within: { value: 7, unit: "day" as const } }] : []), { kind: "payment", first: true, label: "First payment", within: { value: 14, unit: "day" } }],
    breakdown: "channel",
  };
  return (
    <section aria-labelledby="visitors-insights" className="space-y-3">
      <div className="flex items-center">
        <h2 id="visitors-insights" className="flex-1 text-[13px] font-medium text-muted-foreground">Traffic · {days} days</h2>
        <button onClick={() => go("dashboards")} className="font-mono text-[12px] text-muted-foreground hover:text-foreground">Dashboards →</button>
      </div>
      <div className="grid gap-3 lg:grid-cols-2">
        <Panel brand={brand} kind="trend" name="Visits and people" spec={over} go={go} from="visitors" height={460} />
        <Panel brand={brand} kind="funnel" name="From a visit to a first payment" spec={funnel} go={go} from="visitors" height={460} />
      </div>
    </section>
  );
}

// HomeTiles is Home's line of numbers for the brand over the last 28 days,
// against the 28 before: visits, sign-ups, first payments and ad spend. It
// stays hidden until there is a visit or a payment to count.
export function HomeTiles({ brand, go }: { brand: string; go: PanelGo }) {
  const catalog = useInsightsCatalog(brand);
  const [result, setResult] = useState<Schemas["TrendResult"] | null>(null);
  const spec = useMemo<TrendSpec | null>(() => {
    if (!catalog) return null;
    const signUp = signUpStep(catalog);
    const series: Schemas["TrendSeries"][] = [{ kind: "visit", label: "Visits" }];
    if (signUp) series.push(signUp.kind === "conversion" ? { kind: "conversion", conversion: signUp.conversion, label: signUp.label } : { kind: "event", event: signUp.event, label: signUp.label });
    series.push({ kind: "payment", first: true, label: "First payments" });
    if (catalog.networks.some((n) => n.connected)) series.push({ kind: "ad", measure: "spend", label: "Ad spend" });
    return { date_range: { from: dayAgo(27), to: dayAgo(0) }, interval: "day", display: "number", compare: true, series };
  }, [catalog]);
  useEffect(() => {
    setResult(null);
    if (!spec) return;
    api.POST("/v1/brands/{brand}/insights/trend", { params: { path: { brand } }, body: spec }).then(({ data }) => setResult(data ?? null));
  }, [brand, spec]);
  if (!result || !spec || result.series.every((s) => !s.total)) return null;
  return (
    <section aria-labelledby="home-insights" className="space-y-2">
      <div className="flex items-center">
        <h2 id="home-insights" className="flex-1 text-[13px] font-medium text-muted-foreground">Visits to payments · 28 days</h2>
        <button onClick={() => openDraft("trend", { ...spec, display: "line" }, go, "home")} className="mr-3 font-mono text-[12px] text-muted-foreground hover:text-foreground">Over time →</button>
        <button onClick={() => go("dashboards")} className="font-mono text-[12px] text-muted-foreground hover:text-foreground">Dashboards →</button>
      </div>
      <div className="rounded-lg border bg-card p-4 shadow-xs">
        <TrendView result={result} display="number" compact />
      </div>
    </section>
  );
}
