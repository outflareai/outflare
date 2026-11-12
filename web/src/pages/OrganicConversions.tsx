import { PickBrand } from "@/components/pick-brand";
import { useEffect, useState } from "react";
import { useBrand, pickedBrandId } from "@/lib/brand";
import { DataTable } from "@/components/data-table";
import type { SEOSite } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Button, Empty, ErrorText, field, Loading, PageHeader, Stat, Stats } from "../ui";
import type { Tab } from "../lib/paths";
import { SitePicker, pathOf } from "./Keywords";

type Props = { ws: string; go: (tab: Tab, item?: string) => void };

type Gap = { code: string; message: string };
type GroupRef = { id: string; name: string };
type Totals = {
  organic_sessions: number;
  signups: number;
  signup_people: number;
  payments: number;
  payment_people: number;
  revenue_usd: number;
  signup_conversion_rate: number;
  payment_conversion_rate: number;
};
type Source = {
  integration_id: string;
  name: string;
  connector: string;
  site: string;
  newest_day: string | null;
  last_run_at: string | null;
  last_error: string | null;
  payment_succeeded_from: string | null;
  sessions_source: string | null;
  attribution: string;
  currency: string;
};
type PageRow = {
  url: string;
  path: string;
  canonical_path: string;
  groups: GroupRef[];
  organic_sessions: number;
  signups: number;
  signup_people: number;
  payments: number;
  payment_people: number;
  revenue_usd: number;
  signup_conversion_rate: number;
  payment_conversion_rate: number;
  clicks: number | null;
  impressions: number | null;
  position: number | null;
};
type ClusterRow = {
  id: string | null;
  name: string;
  pages: number;
  organic_sessions: number;
  signups: number;
  signup_people: number;
  payments: number;
  payment_people: number;
  revenue_usd: number;
  signup_conversion_rate: number;
  payment_conversion_rate: number;
};

const num = (v: number) => v.toLocaleString();
const pct = (v: number) => `${(v * 100).toLocaleString(undefined, { maximumFractionDigits: 1 })}%`;
const money = (v: number) =>
  new Intl.NumberFormat(undefined, { style: "currency", currency: "USD", maximumFractionDigits: v >= 10000 ? 0 : 2 }).format(v);
const day = (d: string) => new Date(d + "T00:00:00Z").toLocaleDateString(undefined, { month: "short", day: "numeric", timeZone: "UTC" });

export function OrganicConversions(props: Props) {
  const { brand, active } = useBrand(props.ws);
  if (!brand) return <PickBrand what="Organic conversions" brands={active.length} />;
  return <OrganicConversionsPage key={brand.id} {...props} />;
}

function OrganicConversionsPage({ ws, go }: Props) {
  const [sites, setSites] = useState<SEOSite[] | null>(null);
  const [site, setSite] = useState("");
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [error, setError] = useState("");
  const brandKey = useBrand(ws).query.brand ?? "";

  useEffect(() => {
    api.GET("/v1/brands/{brand}/seo-sites", { params: { path: { brand: pickedBrandId() } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setSites(data.data);
      setSite((s) => (data.data.some((x) => x.id === s) ? s : (data.data[0]?.id ?? "")));
    });
  }, [ws, brandKey]);

  const header = (
    <PageHeader
      title="Organic conversions"
      description="First-touch Organic Search signups and revenue by landing page and search group, from your PostHog events. A payment is counted once: payment_succeeded when it exists, otherwise a paid service_purchased that is not a trial or a wallet top-up. Agents use get_organic_conversions."
      action={<div className="flex gap-2">{sites && <SitePicker sites={sites} site={site} onChange={setSite} />}</div>}
    />
  );
  if (error) return <div className="space-y-6">{header}<ErrorText>{error}</ErrorText></div>;
  if (!sites) return <div className="space-y-6">{header}<Loading /></div>;
  if (sites.length === 0)
    return (
      <div className="space-y-6">
        {header}
        <Empty title="Add your website first" action={<Button onClick={() => go("audits")}>Add a site</Button>}>
          Organic conversions are shown for the sites OutflareBot audits. Add your website, then connect PostHog.
        </Empty>
      </div>
    );
  return (
    <div className="space-y-6">
      {header}
      {site && <OrganicReport ws={ws} site={site} go={go} from={from} to={to} setFrom={setFrom} setTo={setTo} />}
    </div>
  );
}

function OrganicReport({
  ws, site, go, from, to, setFrom, setTo,
}: {
  ws: string; site: string; go: Props["go"]; from: string; to: string; setFrom: (v: string) => void; setTo: (v: string) => void;
}) {
  const [pages, setPages] = useState<PageRow[] | null>(null);
  const [clusters, setClusters] = useState<ClusterRow[] | null>(null);
  const [src, setSrc] = useState<Source | null | undefined>(undefined);
  const [totals, setTotals] = useState<Totals | null>(null);
  const [gaps, setGaps] = useState<Gap[]>([]);
  const [range, setRange] = useState<{ from: string; to: string } | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    setPages(null);
    setClusters(null);
    setError("");
    const query: { from?: string; to?: string; sort: "revenue"; limit: number } = { sort: "revenue", limit: 1000 };
    if (from) query.from = from;
    if (to) query.to = to;
    const path = { brand: pickedBrandId(), site };
    Promise.all([
      api.GET("/v1/brands/{brand}/seo-sites/{site}/organic-conversions", { params: { path, query: { ...query, group: "page" } } }),
      api.GET("/v1/brands/{brand}/seo-sites/{site}/organic-conversions", { params: { path, query: { ...query, group: "cluster" } } }),
    ]).then(([pageRes, clusterRes]) => {
      if (pageRes.error || !pageRes.data) return setError(errorMessage(pageRes.error));
      if (clusterRes.error || !clusterRes.data) return setError(errorMessage(clusterRes.error));
      setSrc(pageRes.data.source);
      setTotals(pageRes.data.totals);
      setGaps(pageRes.data.gaps ?? []);
      setRange(pageRes.data.from && pageRes.data.to ? { from: pageRes.data.from, to: pageRes.data.to } : null);
      setPages(pageRes.data.data as PageRow[]);
      setClusters(clusterRes.data.data as ClusterRow[]);
      if (!from && pageRes.data.from) setFrom(pageRes.data.from);
      if (!to && pageRes.data.to) setTo(pageRes.data.to);
    });
  }, [ws, site, from, to, setFrom, setTo]);

  if (error) return <ErrorText>{error}</ErrorText>;
  if (!pages || !clusters || !totals || src === undefined) return <Loading />;
  if (!src)
    return (
      <Empty title="Count organic conversions with PostHog" action={<Button onClick={() => go("connectors", "posthog")}>Connect PostHog</Button>}>
        Outflare attributes Organic Search signups and payments to each person's first organic landing page, then maps that page to your search groups. Once PostHog is connected and has events for this site, they show here after a sync. This page does not change the events on the Keywords conversions report.
      </Empty>
    );

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-end gap-3">
        <label className="text-xs text-muted-foreground">From
          <input aria-label="From date" className={`${field} mt-1 block`} type="date" value={from} max={to || undefined} onChange={(e) => setFrom(e.target.value)} />
        </label>
        <label className="text-xs text-muted-foreground">To
          <input aria-label="To date" className={`${field} mt-1 block`} type="date" value={to} min={from || undefined} onChange={(e) => setTo(e.target.value)} />
        </label>
        {range && <p className="pb-2 text-xs text-muted-foreground">{day(range.from)} – {day(range.to)} from {src.name}{src.newest_day ? `, stored to ${day(src.newest_day)}` : ""}.</p>}
      </div>
      <Stats cols={4}>
        <Stat label="Organic sessions" value={num(totals.organic_sessions)} />
        <Stat label="Signups" value={num(totals.signups)} sub={pct(totals.signup_conversion_rate)} />
        <Stat label="Payments" value={num(totals.payments)} sub={pct(totals.payment_conversion_rate)} />
        <Stat label="Revenue" value={money(totals.revenue_usd)} sub="USD, counted once" />
      </Stats>
      {gaps.length > 0 && (
        <ul className="space-y-1 rounded-lg border bg-muted/30 px-3 py-2 text-[13px] text-muted-foreground">
          {gaps.map((g) => <li key={g.code}>{g.message}</li>)}
        </ul>
      )}
      <div className="space-y-2">
        <h2 className="text-sm font-medium">By search group</h2>
        <p className="text-xs text-muted-foreground">A page can match more than one group, so do not add these totals together. Site totals above are unique. Unmatched pages are Ungrouped.</p>
        <DataTable
          data={clusters}
          rowId={(r) => r.id ?? "ungrouped"}
          searchPlaceholder="Search groups"
          empty="No search groups or landing pages in this window yet. Add content groups in Search insights, or wait for the PostHog sync."
          mobile={(r) => (
            <div className="space-y-1">
              <div className="text-[13px] font-medium">{r.name}</div>
              <div className="text-xs text-muted-foreground">{num(r.signups)} signups · {num(r.payments)} payments · {money(r.revenue_usd)}</div>
            </div>
          )}
          columns={[
            { id: "name", header: "Group", accessorFn: (r) => r.name, cell: ({ row }) => <span className={row.original.id ? "" : "text-muted-foreground"}>{row.original.name}</span> },
            { id: "pages", header: "Pages", accessorFn: (r) => r.pages, meta: { align: "right", mono: true }, cell: ({ row }) => num(row.original.pages) },
            { id: "sessions", header: "Organic sessions", accessorFn: (r) => r.organic_sessions, meta: { align: "right", mono: true }, cell: ({ row }) => num(row.original.organic_sessions) },
            { id: "signups", header: "Signups", accessorFn: (r) => r.signups, meta: { align: "right", mono: true }, cell: ({ row }) => `${num(row.original.signups)} (${pct(row.original.signup_conversion_rate)})` },
            { id: "payments", header: "Payments", accessorFn: (r) => r.payments, meta: { align: "right", mono: true }, cell: ({ row }) => `${num(row.original.payments)} (${pct(row.original.payment_conversion_rate)})` },
            { id: "revenue", header: "Revenue", accessorFn: (r) => r.revenue_usd, meta: { align: "right", mono: true }, cell: ({ row }) => money(row.original.revenue_usd) },
          ]}
        />
      </div>
      <div className="space-y-2">
        <h2 className="text-sm font-medium">By landing page</h2>
        <DataTable
          data={pages}
          rowId={(r) => r.url}
          searchPlaceholder="Search pages"
          empty="No organic signups or payments counted for this site's pages yet."
          mobile={(r) => (
            <div className="space-y-1">
              <div className="font-mono text-[12px] break-all">{pathOf(r.url)}</div>
              <div className="text-xs text-muted-foreground">{num(r.signups)} signups · {num(r.payments)} payments · {money(r.revenue_usd)}</div>
            </div>
          )}
          columns={[
            {
              id: "path",
              header: "Landing page",
              accessorFn: (r) => r.path,
              cell: ({ row }) => (
                <div>
                  <span className="font-mono text-[12px] break-all">{row.original.path}</span>
                  {row.original.canonical_path !== row.original.path && <div className="text-xs text-muted-foreground">Grouped as {row.original.canonical_path}</div>}
                  {row.original.groups.length > 0 && <div className="text-xs text-muted-foreground">{row.original.groups.map((g) => g.name).join(", ")}</div>}
                </div>
              ),
            },
            { id: "sessions", header: "Organic sessions", accessorFn: (r) => r.organic_sessions, meta: { align: "right", mono: true }, cell: ({ row }) => num(row.original.organic_sessions) },
            { id: "signups", header: "Signups", accessorFn: (r) => r.signups, meta: { align: "right", mono: true }, cell: ({ row }) => `${num(row.original.signups)} (${pct(row.original.signup_conversion_rate)})` },
            { id: "payments", header: "Payments", accessorFn: (r) => r.payments, meta: { align: "right", mono: true }, cell: ({ row }) => `${num(row.original.payments)} (${pct(row.original.payment_conversion_rate)})` },
            { id: "revenue", header: "Revenue", accessorFn: (r) => r.revenue_usd, meta: { align: "right", mono: true }, cell: ({ row }) => money(row.original.revenue_usd) },
            { id: "clicks", header: "Search clicks", accessorFn: (r) => r.clicks ?? -1, meta: { align: "right", mono: true }, cell: ({ row }) => (row.original.clicks === null ? "–" : num(row.original.clicks)) },
          ]}
        />
      </div>
    </div>
  );
}
