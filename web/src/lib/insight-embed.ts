import type { Schemas } from "@outflare/sdk";

type Insight = Schemas["Insight"];
type DateRange = Schemas["InsightDateRange"];

// InsightEmbed opens a Funnels, Trends, Retention, Paths or Lifecycle
// builder inside another page (a dashboard tile), on one saved insight or
// a new one. The builder hides its own list and hands the saved insight
// back.
export type InsightEmbed = {
  insight?: Insight;
  // The dates the dashboard shows every tile over, when it sets them.
  range?: DateRange;
  onSaved: (i: Insight) => void;
};

// embedSpec is the spec the builder starts from: the insight's own, or
// the page's starter, over the dashboard's dates when it has them.
export function embedSpec<T extends { date_range?: DateRange }>(e: InsightEmbed, starter: T): T {
  const s = { ...((e.insight?.spec as T | undefined) ?? starter) };
  if (e.range) s.date_range = e.range;
  return s;
}

type Filter = Schemas["InsightFilter"];
type Part = { kind?: string; filters?: Filter[] };

// withFilters adds a dashboard's filters to every event and page view part
// of a tile's spec, as the server does for share links and the digest
// (engine/internal/api/dashboards_filters.go).
export function withFilters<T>(kind: Insight["kind"], spec: T, filters: Filter[] | undefined): T {
  if (!filters?.length) return spec;
  const add = <P extends Part>(p: P | undefined): P | undefined => (p && (p.kind === "event" || p.kind === "page") ? { ...p, filters: [...(p.filters ?? []), ...filters] } : p);
  const s = { ...spec } as Record<string, unknown>;
  if (kind === "trend") s.series = (s.series as Part[] | undefined)?.map(add);
  else if (kind === "funnel") s.steps = (s.steps as Part[] | undefined)?.map(add);
  else if (kind === "retention") {
    s.start = add(s.start as Part | undefined);
    if (s.return) s.return = add(s.return as Part);
  } else if (kind === "lifecycle" || kind === "stickiness") s.step = add(s.step as Part | undefined);
  return s as T;
}
