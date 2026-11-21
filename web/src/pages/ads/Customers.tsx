// Customers: the people the brand's ads acquired, newest click first. Each
// row is one person: the click that brought them (campaign › ad group › ad
// › keyword), when they clicked, signed up and first paid, and what they
// have paid to date. A row opens the person's record.

import { useCallback, useEffect, useState } from "react";
import type { AdCampaign } from "@outflare/sdk";
import type { Tab } from "../../App";
import { api, errorMessage } from "../../api";
import { DataTable } from "@/components/data-table";
import { Badge, Button, Card, ErrorText, Loading, field } from "../../ui";
import { ago } from "../../format";
import { money, n, platformName } from "../Ads";
import { minorList, rangeOf, type AdAcquisition, type Customers as Bucket } from "./api";

const when = (iso: string | null) => (iso ? <span title={new Date(iso).toLocaleString()}>{ago(iso)}</span> : <span className="text-muted-foreground">—</span>);

// A click is tied to its campaign from the network's own click report,
// which has a click a few hours after it happens; Outflare reads it on
// every sync. After a day and a half without one, it isn't coming.
const reportLag = 36 * 3600 * 1000;

// Where the person came from, in words: never a bare id or "unknown"
// without saying why.
export function pathOf(a: AdAcquisition, now = Date.now()): { main: string; note?: string; quiet?: boolean } {
  const network = platformName(a.connector);
  if (a.campaign.name) {
    const parts = [a.campaign.name, a.ad_group.name, a.ad.name, a.keyword ? `[${a.keyword}]` : ""].filter(Boolean);
    return { main: parts.join(" › "), note: !a.ad_group.name && !a.keyword && a.ad_group.external_id === "" ? `${network} · no ad group or keyword came with this click` : undefined };
  }
  if (a.campaign.external_id) return { main: "A campaign not in the connected ad account", note: `${network} campaign id ${a.campaign.external_id}. It may belong to another ${network} account: connect that account under Setup.`, quiet: true };
  if (now - Date.parse(a.clicked_at) < reportLag) return { main: "Arrives in a few hours", note: `${network} reports a click a few hours after it happens`, quiet: true };
  return { main: "Not matched to a campaign", note: `${network}'s click report has no click with this id. It may belong to another ${network} account.`, quiet: true };
}

function Path({ a }: { a: AdAcquisition }) {
  const p = pathOf(a);
  return (
    <div className="min-w-48 max-w-md space-y-0.5 whitespace-normal">
      <div className={p.quiet ? "text-[13px] text-muted-foreground" : "text-[13px]"}>{p.main}</div>
      <div className="text-[12px] text-muted-foreground">{p.note ?? platformName(a.connector)}</div>
    </div>
  );
}

type Props = { brand: string; days: number; end?: string; customers: Bucket; campaigns: AdCampaign[]; go: (tab: Tab, item?: string) => void; version?: number };

export function Customers({ brand, days, end, customers, campaigns, go, version = 0 }: Props) {
  const [rows, setRows] = useState<AdAcquisition[] | null>(null);
  const [cursor, setCursor] = useState<string | null>(null);
  const [campaign, setCampaign] = useState("");
  const [error, setError] = useState("");
  const [more, setMore] = useState(false);

  const query = useCallback((after?: string) => {
    const range = rangeOf(days, end ? new Date(`${end}T12:00:00Z`) : undefined);
    return { from: range.from, to: range.to, customers, limit: 100, ...(campaign ? { campaign_id: campaign } : {}), ...(after ? { cursor: after } : {}) };
  }, [days, end, customers, campaign]);

  useEffect(() => {
    let live = true;
    setRows(null);
    setCursor(null);
    api.GET("/v1/brands/{brand}/ads/acquisitions", { params: { path: { brand }, query: query() } }).then(({ data, error }) => {
      if (!live) return;
      setError(error ? errorMessage(error) : "");
      setRows(data?.data ?? []);
      setCursor(data?.next_cursor ?? null);
    });
    return () => {
      live = false;
    };
  }, [brand, query, version]);

  async function loadMore() {
    if (!cursor) return;
    setMore(true);
    const { data, error } = await api.GET("/v1/brands/{brand}/ads/acquisitions", { params: { path: { brand }, query: query(cursor) } });
    setMore(false);
    if (error) return setError(errorMessage(error));
    setRows((r) => [...(r ?? []), ...(data?.data ?? [])]);
    setCursor(data?.next_cursor ?? null);
  }

  const filter = (
    <select className={field} aria-label="Campaign" value={campaign} onChange={(e) => setCampaign(e.target.value)}>
      <option value="">Every campaign</option>
      {campaigns.map((c) => <option key={c.id} value={c.id}>{c.name || c.external_id}</option>)}
    </select>
  );

  if (error && !rows) return <ErrorText>{error}</ErrorText>;
  if (!rows) return <Loading />;
  return (
    <Card
      title="Customers from ads"
      description={`Each person a paid click brought in the last ${days} days, and the ad that brought them. ${customers === "new" ? "Only people who have paid." : customers === "returning" ? "Only people who have paid more than once." : "Sign-ups and paying customers."} Open a row for the person. Source: Outflare tracking joined to the ad network's clicks.`}
      action={filter}
    >
      {error && <ErrorText>{error}</ErrorText>}
      <DataTable
        data={rows}
        rowId={(a) => a.id}
        onRowClick={(a) => go("people", a.record_id)}
        columnsMenu={false}
        initialSort={[{ id: "clicked", desc: true }]}
        empty={
          <span>
            Nobody here yet. A person appears once they visit from an ad (the website script sees the click id on the landing page) and then sign up or pay within the acquisition window, as a sign-up or payment event with their record.
          </span>
        }
        mobile={(a) => (
          <div className="space-y-1">
            <div className="flex items-center justify-between gap-2"><span className="font-medium">{a.record_name || "Someone"}</span><Badge tone={a.bucket === "payer" ? "green" : "blue"}>{a.bucket === "payer" ? "Paying" : "Signed up"}</Badge></div>
            <Path a={a} />
            <div className="text-[12px] text-muted-foreground">Clicked {ago(a.clicked_at)} · {n(a.payments)} {a.payments === 1 ? "payment" : "payments"} · {minorList(a.revenue_to_date, money)}</div>
          </div>
        )}
        columns={[
          { id: "person", header: "Person", accessorFn: (a) => a.record_name, cell: ({ row: { original: a } }) => (
            <div className="space-y-0.5">
              <div className="font-medium text-blue-700 underline-offset-2 hover:underline dark:text-blue-400">{a.record_name || "Someone"}</div>
              <Badge tone={a.bucket === "payer" ? "green" : "blue"}>{a.bucket === "payer" ? "Paying" : "Signed up"}</Badge>
            </div>
          ) },
          { id: "path", header: "Campaign › ad group › keyword", accessorFn: (a) => pathOf(a).main, cell: ({ row: { original: a } }) => <Path a={a} /> },
          { id: "clicked", header: "Clicked", accessorFn: (a) => a.clicked_at, cell: ({ row: { original: a } }) => when(a.clicked_at) },
          { id: "signed_up", header: "Signed up", accessorFn: (a) => a.signed_up_at ?? "", cell: ({ row: { original: a } }) => when(a.signed_up_at) },
          { id: "first_paid", header: "First paid", accessorFn: (a) => a.first_paid_at ?? "", cell: ({ row: { original: a } }) => when(a.first_paid_at) },
          { id: "payments", header: "Payments", meta: { align: "right" }, accessorFn: (a) => a.payments, cell: ({ row: { original: a } }) => <span className="font-mono tabular-nums">{n(a.payments)}</span> },
          { id: "revenue", header: "Revenue to date", meta: { align: "right" }, accessorFn: (a) => a.revenue_to_date.reduce((s, m) => s + m.amount_minor, 0), cell: ({ row: { original: a } }) => <span className="font-mono tabular-nums">{minorList(a.revenue_to_date, money)}</span> },
        ]}
      />
      {cursor && <div className="mt-3 flex justify-center"><Button variant="ghost" disabled={more} onClick={loadMore}>Show more</Button></div>}
    </Card>
  );
}
