import { PickBrand } from "@/components/pick-brand";
import { DataTable } from "@/components/data-table";
import { useEffect, useState } from "react";
import { useBrand, pickedBrandId } from "@/lib/brand";
import type { components } from "@outflare/sdk";

type LinkROI = components["schemas"]["LinkROI"];
type LinkROIOrder = components["schemas"]["LinkROIOrder"];
type LinkROIPage = components["schemas"]["LinkROIPage"];
type LinkROIWindow = components["schemas"]["LinkROIWindow"];
type LinkOrderMoney = components["schemas"]["LinkOrderMoney"];
import { api, errorMessage } from "../api";
import { Button, Empty, ErrorText, Loading, PageHeader, Stat, Stats } from "../ui";
import type { Tab } from "../lib/paths";
import { money, moneyList } from "./link-orders/shared";

type Props = { ws: string; go: (tab: Tab, item?: string) => void };

const num = (v: number) => v.toLocaleString();
const signed = (v: number) => `${v > 0 ? "+" : ""}${v.toLocaleString(undefined, { maximumFractionDigits: 2 })}`;
const dash = "–";

function windowOf(windows: LinkROIWindow[], days: number): LinkROIWindow | undefined {
  return windows.find((w) => w.days === days);
}

function costPer(v: number | null | undefined, currency: string | null | undefined) {
  if (v == null || !currency) return dash;
  return money(Math.round(v), currency);
}

export function LinkROIPage(props: Props) {
  const { brand, active } = useBrand(props.ws);
  if (!brand) return <PickBrand what="Link ROI" brands={active.length} />;
  return <LinkROIReport key={brand.id} {...props} />;
}

export { LinkROIPage as LinkROI };

function LinkROIReport({ ws, go }: Props) {
  const [data, setData] = useState<LinkROI | null>(null);
  const [error, setError] = useState("");
  const [days, setDays] = useState(30);
  const brandKey = useBrand(ws).query.brand ?? "";

  useEffect(() => {
    setData(null);
    setError("");
    api.GET("/v1/brands/{brand}/link-roi", { params: { path: { brand: pickedBrandId() } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setData(data);
    });
  }, [ws, brandKey]);

  const header = (
    <PageHeader
      title="Link ROI"
      description="Donor price of each published link against already-stored Search Console, ranking and referring-domain changes on the target page, 30, 60 and 90 days around publication. These figures do not show that the link caused a change. Agents use get_link_roi."
      action={<Button variant="ghost" onClick={() => go("link-orders")}>Open Link orders</Button>}
    />
  );
  if (error) return <div className="space-y-6">{header}<ErrorText>{error}</ErrorText></div>;
  if (!data) return <div className="space-y-6">{header}<Loading /></div>;

  const t = data.totals;
  const spendLabel = moneyList(t.spend as LinkOrderMoney[]);
  const avgLabel = moneyList(t.cost_per_link as LinkOrderMoney[]);
  const published = data.orders;
  const clickSum = published.reduce((n, o) => n + (windowOf(o.windows, days)?.traffic?.clicks_change ?? 0), 0);
  const rdSum = published.reduce((n, o) => n + (windowOf(o.windows, days)?.referring_domains?.change ?? 0), 0);

  return (
    <div className="space-y-6">
      {header}
      <p className="rounded-lg border bg-muted/30 px-3 py-2 text-[13px] text-muted-foreground">{data.note}</p>
      <div className="flex flex-wrap gap-2">
        {data.windows.map((d) => (
          <Button key={d} variant={days === d ? "primary" : "ghost"} onClick={() => setDays(d)}>{d} days</Button>
        ))}
      </div>
      <Stats cols={4}>
        <Stat label="Published links" value={num(t.published)} sub={`${num(t.open)} still open · ${num(t.lost)} lost`} />
        <Stat label="Spend" value={spendLabel || dash} sub={avgLabel ? `${avgLabel} per link` : "No donor prices yet"} />
        <Stat label={`Click change, ${days}d`} value={t.with_traffic ? signed(clickSum) : dash} sub={`${num(t.with_traffic)} of ${num(t.published)} have Search Console rows`} />
        <Stat label={`Referring domains, ${days}d`} value={t.with_referring_domains ? signed(rdSum) : dash} sub={`${num(t.with_referring_domains)} of ${num(t.published)} have stored backlinks`} />
      </Stats>
      {t.published === 0 ? (
        <Empty title="Publish a link order first" action={<Button onClick={() => go("link-orders")}>Go to Link orders</Button>}>
          Link ROI uses published, paid and verified orders and the Search Console, ranking and backlink rows Outflare already stores. Nothing is pulled from Ahrefs or DataForSEO here.
        </Empty>
      ) : (
        <>
          <div className="space-y-2">
            <h2 className="text-sm font-medium">By published link</h2>
            <DataTable
              data={published}
              rowId={(r) => r.id}
              searchPlaceholder="Search domains or pages"
              empty="No published link orders yet."
              mobile={(r) => <OrderCard row={r} days={days} />}
              columns={orderColumns(days)}
            />
          </div>
          <div className="space-y-2">
            <h2 className="text-sm font-medium">By target page</h2>
            <p className="text-xs text-muted-foreground">Several links to the same page share one Search Console series, so the change is measured around the first publication rather than added together.</p>
            <DataTable
              data={data.pages}
              rowId={(r) => r.target_url}
              searchPlaceholder="Search pages"
              empty="No published target pages yet."
              mobile={(r) => <PageCard row={r} days={days} />}
              columns={pageColumns(days)}
            />
          </div>
        </>
      )}
    </div>
  );
}

function OrderCard({ row, days }: { row: LinkROIOrder; days: number }) {
  const w = windowOf(row.windows, days);
  return (
    <div className="space-y-1">
      <div className="text-[13px] font-medium">{row.domain}</div>
      <div className="font-mono text-[12px] break-all text-muted-foreground">{row.target_url}</div>
      <div className="text-xs text-muted-foreground">{money(row.amount_minor, row.currency)} · clicks {chg(w?.traffic?.clicks_change, w?.traffic?.available)} · rank {chg(w?.rank?.position_change, w?.rank?.available)}</div>
    </div>
  );
}

function PageCard({ row, days }: { row: LinkROIPage; days: number }) {
  const w = windowOf(row.windows, days);
  return (
    <div className="space-y-1">
      <div className="font-mono text-[12px] break-all">{row.target_url}</div>
      <div className="text-xs text-muted-foreground">{row.links} links · {moneyList(row.spend)} · clicks {chg(w?.traffic?.clicks_change, w?.traffic?.available)}</div>
    </div>
  );
}

function chg(v: number | null | undefined, available?: boolean | null) {
  if (!available || v == null) return dash;
  return signed(v);
}

function orderColumns(days: number) {
  return [
    { id: "domain", header: "Referring domain", accessorFn: (r: LinkROIOrder) => r.domain, cell: ({ row }: { row: { original: LinkROIOrder } }) => (
      <div>
        <div>{row.original.domain}</div>
        <div className="text-xs text-muted-foreground">{row.original.status}{row.original.donor_domain_rating != null ? ` · DR ${row.original.donor_domain_rating}` : ""}</div>
      </div>
    ) },
    { id: "target", header: "Target page", accessorFn: (r: LinkROIOrder) => r.target_url, cell: ({ row }: { row: { original: LinkROIOrder } }) => <span className="font-mono text-[12px] break-all">{row.original.target_url}</span> },
    { id: "cost", header: "Cost", accessorFn: (r: LinkROIOrder) => r.amount_minor ?? -1, meta: { align: "right", mono: true }, cell: ({ row }: { row: { original: LinkROIOrder } }) => money(row.original.amount_minor, row.original.currency) },
    { id: "clicks", header: "Clicks", accessorFn: (r: LinkROIOrder) => windowOf(r.windows, days)?.traffic?.clicks_change ?? null, meta: { align: "right", mono: true }, cell: ({ row }: { row: { original: LinkROIOrder } }) => {
      const w = windowOf(row.original.windows, days);
      return <span>{chg(w?.traffic?.clicks_change, w?.traffic?.available)}{w && !w.elapsed ? " · open" : ""}</span>;
    } },
    { id: "rank", header: "Avg position", accessorFn: (r: LinkROIOrder) => windowOf(r.windows, days)?.rank?.position_change ?? null, meta: { align: "right", mono: true }, cell: ({ row }: { row: { original: LinkROIOrder } }) => chg(windowOf(row.original.windows, days)?.rank?.position_change, windowOf(row.original.windows, days)?.rank?.available) },
    { id: "rd", header: "Referring domains", accessorFn: (r: LinkROIOrder) => windowOf(r.windows, days)?.referring_domains?.change ?? null, meta: { align: "right", mono: true }, cell: ({ row }: { row: { original: LinkROIOrder } }) => chg(windowOf(row.original.windows, days)?.referring_domains?.change, windowOf(row.original.windows, days)?.referring_domains?.available) },
    { id: "cpc", header: "Cost / click gained", accessorFn: (r: LinkROIOrder) => windowOf(r.windows, days)?.cost_per_click_gained ?? null, meta: { align: "right", mono: true }, cell: ({ row }: { row: { original: LinkROIOrder } }) => costPer(windowOf(row.original.windows, days)?.cost_per_click_gained, row.original.currency) },
  ];
}

function pageColumns(days: number) {
  return [
    { id: "target", header: "Target page", accessorFn: (r: LinkROIPage) => r.target_url, cell: ({ row }: { row: { original: LinkROIPage } }) => <span className="font-mono text-[12px] break-all">{row.original.target_url}</span> },
    { id: "links", header: "Links", accessorFn: (r: LinkROIPage) => r.links, meta: { align: "right", mono: true }, cell: ({ row }: { row: { original: LinkROIPage } }) => num(row.original.links) },
    { id: "spend", header: "Spend", accessorFn: (r: LinkROIPage) => r.spend[0]?.amount_minor ?? 0, meta: { align: "right", mono: true }, cell: ({ row }: { row: { original: LinkROIPage } }) => moneyList(row.original.spend) || dash },
    { id: "clicks", header: "Clicks", accessorFn: (r: LinkROIPage) => windowOf(r.windows, days)?.traffic?.clicks_change ?? null, meta: { align: "right", mono: true }, cell: ({ row }: { row: { original: LinkROIPage } }) => chg(windowOf(row.original.windows, days)?.traffic?.clicks_change, windowOf(row.original.windows, days)?.traffic?.available) },
    { id: "rank", header: "Avg position", accessorFn: (r: LinkROIPage) => windowOf(r.windows, days)?.rank?.position_change ?? null, meta: { align: "right", mono: true }, cell: ({ row }: { row: { original: LinkROIPage } }) => chg(windowOf(row.original.windows, days)?.rank?.position_change, windowOf(row.original.windows, days)?.rank?.available) },
    { id: "rd", header: "Referring domains", accessorFn: (r: LinkROIPage) => windowOf(r.windows, days)?.referring_domains?.change ?? null, meta: { align: "right", mono: true }, cell: ({ row }: { row: { original: LinkROIPage } }) => chg(windowOf(row.original.windows, days)?.referring_domains?.change, windowOf(row.original.windows, days)?.referring_domains?.available) },
  ];
}
