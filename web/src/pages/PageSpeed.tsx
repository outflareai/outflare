import { PickBrand } from "@/components/pick-brand";
import { pickedBrandId, useBrand } from "@/lib/brand";
// Page speed: how fast the brand's pages were for real visitors, as its own
// tag measured each page load (Core Web Vitals), by page, with the pages
// that got slower than the period before. Google's own data for big pages
// is in Search insights; this covers every page the tag runs on.

import { useEffect, useState } from "react";
import type { PageSpeedReport } from "@outflare/sdk";
import { DataTable } from "@/components/data-table";
import { api, errorMessage } from "../api";
import { Badge, Button, Empty, ErrorText, field, Loading, PageHeader, Stat, Stats } from "../ui";
import type { Tab } from "../App";

type Props = { ws: string; canManage: boolean; go: (tab: Tab, item?: string) => void };
type Metrics = PageSpeedReport["metrics"];
type MetricKey = keyof Metrics;
type Page = PageSpeedReport["pages"][number];

const shown: { key: MetricKey; label: string; help: string }[] = [
  { key: "lcp", label: "Loading", help: "How long the biggest part of the page took to show (LCP). Good is 2.5s or less." },
  { key: "inp", label: "Response to clicks", help: "How long the page took to react to a click or key (INP). Good is 200ms or less." },
  { key: "cls", label: "Layout jumps", help: "How much the page moved while loading (CLS). Good is 0.1 or less." },
];
const periods = [
  { days: 7, label: "Last 7 days" },
  { days: 28, label: "Last 28 days" },
  { days: 90, label: "Last 90 days" },
];
const day = (d: Date) => d.toISOString().slice(0, 10);

export function value(key: string, v: number | null | undefined) {
  if (v === null || v === undefined) return "—";
  if (key === "cls") return v.toFixed(2);
  return v >= 1000 ? `${(v / 1000).toFixed(1)}s` : `${Math.round(v)}ms`;
}
const toneOf = (g: string) => (g === "good" ? "green" : g === "poor" ? "red" : g ? "amber" : "grey") as "green" | "red" | "amber" | "grey";
const gradeLabel = (g: string) => (g === "good" ? "Good" : g === "poor" ? "Poor" : g ? "Needs work" : "No data");

export function PageSpeed(props: Props) {
  const { brand, active } = useBrand(props.ws);
  if (!brand) return <PickBrand what="Page speed" brands={active.length} />;
  return <PageSpeedPage key={brand.id} {...props} />;
}

function PageSpeedPage({ go }: Props) {
  const [days, setDays] = useState(7);
  const [device, setDevice] = useState<"" | "desktop" | "mobile">("");
  const [website, setWebsite] = useState("");
  const [data, setData] = useState<PageSpeedReport | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    setData(null);
    const to = new Date();
    const query = { from: day(new Date(to.getTime() - (days - 1) * 86_400_000)), to: day(to), ...(device ? { device } : {}), ...(website ? { website } : {}) };
    api.GET("/v1/brands/{brand}/analytics/page-speed", { params: { path: { brand: pickedBrandId() }, query } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setError("");
      setData(data);
    });
  }, [days, device, website]);

  const header = (
    <PageHeader
      title="Page speed"
      description="How fast your pages were for real visitors, measured by your Outflare tag on every page load, including pages Google has no data for."
      action={
        <div className="flex flex-wrap items-center gap-2">
          {data && data.websites.length > 1 && (
            <select aria-label="Website" className={field} value={website} onChange={(e) => setWebsite(e.target.value)}>
              <option value="">All websites</option>
              {data.websites.map((h) => <option key={h} value={h}>{h}</option>)}
            </select>
          )}
          <select aria-label="Device" className={field} value={device} onChange={(e) => setDevice(e.target.value as typeof device)}>
            <option value="">All devices</option>
            <option value="mobile">Phones</option>
            <option value="desktop">Computers</option>
          </select>
          <select aria-label="Period" className={field} value={days} onChange={(e) => setDays(Number(e.target.value))}>
            {periods.map((p) => <option key={p.days} value={p.days}>{p.label}</option>)}
          </select>
        </div>
      }
    />
  );
  if (error) return <div>{header}<ErrorText>{error}</ErrorText></div>;
  if (!data) return <div>{header}<Loading /></div>;
  if (data.loads === 0 && data.previous_loads === 0) {
    return (
      <div>
        {header}
        <Empty title="No page loads measured yet" action={<Button onClick={() => go("tracking")}>Check your tracking</Button>}>
          Your Outflare tag measures page speed by itself from version 16. Once visitors load your pages, their speed shows here within a minute. Sites that copied an old snippet get the new one with no change.
        </Empty>
      </div>
    );
  }
  const fastChange = data.fast_share !== null && data.previous_fast_share !== null ? data.fast_share - data.previous_fast_share : null;
  return (
    <div className="space-y-6">
      {header}
      <Stats cols={4}>
        {shown.map((m) => {
          const v = data.metrics[m.key], before = data.previous_metrics[m.key];
          return (
            <Stat
              key={m.key}
              label={m.label}
              value={value(m.key, v.p75)}
              tone={v.grade === "good" ? "green" : v.grade === "poor" ? "red" : v.grade ? "amber" : undefined}
              sub={<span title={m.help}>{gradeLabel(v.grade)}{before.p75 !== null && v.p75 !== null ? ` · was ${value(m.key, before.p75)}` : ""}</span>}
            />
          );
        })}
        <Stat
          label="Visits with a fast page"
          value={data.fast_share === null ? "—" : `${Math.round(data.fast_share * 100)}%`}
          tone={fastChange !== null && fastChange <= -0.05 ? "amber" : undefined}
          sub={fastChange === null ? `${data.loads.toLocaleString()} page loads` : `${fastChange >= 0 ? "Up" : "Down"} ${Math.abs(Math.round(fastChange * 100))} points · ${data.loads.toLocaleString()} loads`}
        />
      </Stats>
      <p className="text-[12px] text-muted-foreground">Each number is the 75th percentile, the value Google judges a page by: three in four visits were at least this fast.</p>

      {data.slower.length > 0 && (
        <section aria-label="Pages that got slower" className="space-y-2">
          {data.slower.map((s) => (
            <div key={`${s.website}${s.path}${s.metric}`} className="flex flex-wrap items-center gap-3 rounded-lg border border-amber-600/30 bg-amber-500/10 px-4 py-3 text-[13px]">
              <div className="min-w-0 flex-1">
                <p className="font-medium">
                  {s.path} got slower to {s.metric === "lcp" ? "load" : "respond to clicks"}: {value(s.metric, s.after)}, up from {value(s.metric, s.before)}
                </p>
                <p className="text-muted-foreground">{s.website ? `${s.website} · ` : ""}{s.loads.toLocaleString()} page loads in this period, compared with the {days} days before.</p>
              </div>
              <Button variant="ghost" onClick={() => go("audits")}>Check the page</Button>
            </div>
          ))}
        </section>
      )}

      <DataTable
        data={data.pages}
        rowId={(p: Page) => `${p.website} ${p.path}`}
        columnsMenu={false}
        empty="No page loads in this period."
        columns={[
          { id: "path", header: "Page", accessorFn: (p: Page) => p.path, cell: ({ row: { original: p } }: { row: { original: Page } }) => (
            <span className="block max-w-[28rem] truncate font-medium" title={`${p.website}${p.path}`}>{p.path}{data.websites.length > 1 && <span className="ml-2 font-normal text-muted-foreground">{p.website}</span>}</span>
          ) },
          { id: "loads", header: "Loads", accessorFn: (p: Page) => p.loads, meta: { align: "right" as const, mono: true }, cell: ({ row: { original: p } }: { row: { original: Page } }) => p.loads.toLocaleString() },
          ...shown.map((m) => ({
            id: m.key,
            header: m.label,
            accessorFn: (p: Page) => p.metrics[m.key].p75 ?? -1,
            meta: { align: "right" as const, mono: true },
            cell: ({ row: { original: p } }: { row: { original: Page } }) => {
              const v = p.metrics[m.key];
              return v.p75 === null ? <span className="text-muted-foreground">—</span> : <Badge tone={toneOf(v.grade)}>{value(m.key, v.p75)}</Badge>;
            },
          })),
          { id: "change", header: "Loading change", accessorFn: (p: Page) => p.previous_lcp ?? -1, meta: { align: "right" as const, mono: true }, cell: ({ row: { original: p } }: { row: { original: Page } }) => {
            const now = p.metrics.lcp.p75, then = p.previous_lcp;
            if (now === null || then === null) return <span className="text-muted-foreground">—</span>;
            const diff = now - then;
            if (Math.abs(diff) < 100) return <span className="text-muted-foreground">No change</span>;
            return <span className={diff > 0 ? "text-amber-700 dark:text-amber-400" : "text-green-700 dark:text-green-400"}>{diff > 0 ? "+" : "−"}{value("lcp", Math.abs(diff))}</span>;
          } },
        ]}
      />
    </div>
  );
}
