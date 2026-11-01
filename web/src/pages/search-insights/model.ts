import type {
  SearchInsightReport,
  SearchInsightRow,
  Schemas,
} from "@outflare/sdk";
export type Report = SearchInsightReport["report"];
export const reports: Record<Report, string> = {
  opportunities: "Opportunities",
  ctr: "CTR benchmarks",
  decay: "Content decay",
  segments: "Countries & devices",
  groups: "Content & topics",
  forecast: "Forecast",
  completeness: "Data completeness",
  changes: "Trend breaks",
  attribution: "GA4 outcomes",
  "ai-referrals": "AI referrals",
  migrations: "Migrations",
  vitals: "Core Web Vitals",
};
export const descriptions: Record<Report, string> = {
  opportunities:
    "Prioritise pages and queries by estimated click gains from better snippets and top-three rankings.",
  ctr: "Your observed CTR by ranking position, with the benchmark used to estimate opportunities.",
  decay:
    "Compare complete calendar weeks or months to find gradual declines. Click a row to inspect its history.",
  segments:
    "Find the pages, countries and devices behind search performance. Historical segments build as Search Console syncs.",
  groups:
    "Measure saved URL groups and keyword topics. Groups may overlap, so don't add their totals together.",
  forecast:
    "Project search clicks using weekday seasonality and a bounded trend. Change assumptions to compare scenarios.",
  completeness:
    "Compare property totals with visible page/query rows. Google privacy omissions and collection limits both affect the gap.",
  changes:
    "Sustained traffic changes alongside official Google updates and production deploys. Timing alone does not prove cause.",
  attribution:
    "Measured GA4 landing-page outcomes and estimated query allocation using visible Search Console clicks.",
  "ai-referrals":
    "Measure visits and outcomes from known AI assistant referrers in GA4. Missing referrers cannot be attributed.",
  migrations:
    "Compare settled search traffic around a migration and inspect redirect evidence from stored crawls.",
  vitals:
    "Real-user page experience from Chrome UX Report, including weekly history and regression flags.",
};
export const percent = (n: number) =>
  `${(n * 100).toLocaleString(undefined, { maximumFractionDigits: 1 })}%`;
export const number = (n: number) =>
  n.toLocaleString(undefined, { maximumFractionDigits: 2 });
export const rateKeys = new Set([
  "ctr",
  "expected_ctr",
  "change",
  "share",
  "good_share",
]);
export const labels: Record<string, string> = {
  key: "Page / query",
  detail: "Details",
  clicks: "Clicks",
  impressions: "Impressions",
  ctr: "CTR",
  position: "Position",
  expected_ctr: "Expected CTR",
  missed_clicks: "Snippet upside",
  potential_clicks: "Top 3 upside",
  score: "Priority",
  before: "Before",
  after: "After",
  change: "Change",
  value: "Value",
  lower: "Lower estimate",
  upper: "Upper estimate",
  share: "Share",
  good_share: "Good experiences",
  revenue_micros: "Revenue",
  currency: "Currency",
  sessions: "Sessions",
  conversions: "Key events",
  source: "Source",
  target: "Target",
  metric: "Metric",
  status: "Status",
  date: "Date",
  country: "Country",
  device: "Device",
};
export const columns: Record<Report, (keyof SearchInsightRow)[]> = {
  opportunities: [
    "key",
    "position",
    "impressions",
    "ctr",
    "expected_ctr",
    "missed_clicks",
    "potential_clicks",
    "score",
  ],
  ctr: ["position", "impressions", "ctr", "expected_ctr", "status"],
  decay: ["key", "before", "after", "change"],
  segments: [
    "page",
    "country",
    "device",
    "clicks",
    "impressions",
    "ctr",
    "position",
    "before",
    "change",
  ],
  groups: ["detail", "status", "clicks", "impressions", "ctr", "position"],
  forecast: [
    "date",
    "value",
    "lower",
    "upper",
    "conversions",
    "revenue_micros",
    "currency",
  ],
  completeness: [
    "date",
    "clicks",
    "before",
    "after",
    "share",
    "value",
    "status",
  ],
  changes: ["date", "status", "before", "after", "change", "detail"],
  attribution: [
    "key",
    "status",
    "sessions",
    "conversions",
    "revenue_micros",
    "currency",
  ],
  "ai-referrals": [
    "page",
    "source",
    "sessions",
    "conversions",
    "revenue_micros",
    "share",
    "currency",
  ],
  migrations: ["source", "target", "status", "detail"],
  vitals: [
    "page",
    "device",
    "date",
    "metric",
    "value",
    "good_share",
    "change",
    "status",
  ],
};

export type Filters = Schemas["SearchViewFilters"];
export type WorkItem = Schemas["SearchWorkItem"];
export type SavedReport = Schemas["SearchSavedReport"];
export type ReportRun = Schemas["SearchReportRun"];
export type Schedule = Schemas["SearchCollectionSchedule"];
export type CollectionRun = Schemas["SearchCollectionRun"];
export type Workspace = Schemas["SearchWorkspace"];
export type Area =
  | "overview"
  | "explore"
  | "work"
  | "reports"
  | "automations"
  | "activity";
export const areas: Record<Area, string> = {
  overview: "Overview",
  explore: "Explore",
  work: "Work",
  reports: "Reports",
  automations: "Automations",
  activity: "Activity",
};
export const reportFamilies = [
  {
    name: "Grow search traffic",
    reports: ["opportunities", "ctr", "forecast"] as Report[],
  },
  {
    name: "Investigate a change",
    reports: ["decay", "changes", "segments", "migrations"] as Report[],
  },
  {
    name: "Measure & organise",
    reports: [
      "attribution",
      "ai-referrals",
      "vitals",
      "completeness",
      "groups",
    ] as Report[],
  },
];
export function displayCell(
  row: SearchInsightRow,
  key: keyof SearchInsightRow,
): string {
  const value = row[key];
  if (
    ["before", "change"].includes(key) &&
    ["partial_history", "no_baseline", "google_update", "deploy"].includes(
      row.status ?? "",
    )
  )
    return "—";
  if (key === "revenue_micros")
    return `${row.currency ?? ""} ${number((typeof value === "number" ? value : 0) / 1e6)}`;
  if (typeof value === "number")
    return rateKeys.has(key) ? percent(value) : number(value);
  if (typeof value === "string")
    return key === "status" ? value.replaceAll("_", " ") : value;
  if (
    [
      "clicks",
      "impressions",
      "ctr",
      "position",
      "missed_clicks",
      "potential_clicks",
      "before",
      "after",
      "change",
      "score",
      "value",
      "sessions",
      "conversions",
      "share",
      "good_share",
    ].includes(key)
  )
    return "0";
  return "—";
}
export function findingAdvice(
  report: Report,
  row: SearchInsightRow,
): { title: string; why: string; next: string } {
  if (report === "opportunities")
    return row.missed_clicks
      ? {
          title: "Improve the search snippet",
          why: `CTR is below the position benchmark. About ${number(row.missed_clicks)} additional clicks could be available at unchanged impressions.`,
          next: "Review the title and description against the query intent, then measure the change.",
        }
      : {
          title: "Strengthen a page within reach",
          why: `This query ranks around position ${number(row.position ?? 0)}. Moving into the top three could add clicks.`,
          next: "Improve the answer, add relevant internal links and track the target keyword.",
        };
  if (report === "decay")
    return {
      title: "Investigate declining content",
      why: `Clicks moved from ${number(row.before ?? 0)} to ${number(row.after ?? 0)} across complete periods.`,
      next: "Check lost queries, indexing and recent deploys before deciding what to refresh.",
    };
  if (report === "vitals")
    return {
      title:
        row.status === "regressed"
          ? "Review a page-experience regression"
          : "Check real-user page experience",
      why: `${row.metric?.replaceAll("_", " ") ?? "Field performance"} on ${row.device ?? "this device"}: ${number(row.value ?? 0)}.`,
      next: "Compare the field window with deploys. Fix loading, responsiveness or layout stability, then collect a later window.",
    };
  if (report === "migrations")
    return {
      title: "Review the redirect evidence",
      why: `This mapping is ${row.status?.replaceAll("_", " ") ?? "unchecked"}. ${row.detail ?? ""}`,
      next: "Review the redirect, target response, indexability and canonical. Re-crawl both sites to collect fresh evidence.",
    };
  return {
    title: reports[report],
    why:
      row.detail ||
      "Review this row alongside its source, date range and any data limitations.",
    next: "Save the finding with a note, compare the evidence, and choose a small measurable next step.",
  };
}
export function isActionable(report: Report, row: SearchInsightRow): boolean {
  return (
    ["opportunities", "decay", "attribution"].includes(report) ||
    (report === "vitals" &&
      ["regressed", "needs_improvement"].includes(row.status ?? "")) ||
    (report === "migrations" && row.status !== "healthy")
  );
}
export function cleanFilters(filters: Filters): Filters {
  return Object.fromEntries(
    Object.entries(filters).filter(([, v]) => v !== undefined && v !== ""),
  ) as Filters;
}
