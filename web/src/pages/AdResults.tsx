// What each ad campaign brought, by Outflare's own count: the visits that
// arrived through it (joined by the ids its tracking template put on the
// landing page), and the conversions and revenue credited to them under the
// workspace's attribution model, beside what the platform says it spent and
// converted. A campaign that spent with no tracked visits usually has no
// tracking template yet.

import { useEffect, useState } from "react";
import type { AttributionAdRow } from "@outflare/sdk";
import { DataTable } from "@/components/data-table";
import { api, errorMessage } from "../api";
import { Card, ErrorText } from "../ui";
import { money, num } from "./Channels";

const platforms: Record<string, string> = {
  google_ads: "Google Ads", meta_ads: "Meta Ads", microsoft_ads: "Microsoft Advertising", reddit_ads: "Reddit Ads", x_ads: "X Ads",
  linkedin_ads: "LinkedIn Ads", tiktok_ads: "TikTok Ads", snapchat_ads: "Snapchat Ads", taboola_ads: "Taboola Ads", outbrain_ads: "Outbrain Ads",
  apple_ads: "Apple Ads", amazon_ads: "Amazon Ads", pinterest_ads: "Pinterest Ads",
};

// ret is revenue over spend when both are in one currency.
function ret(r: AttributionAdRow) {
  const c = r.cost.length === 1 ? r.cost[0] : null, v = r.revenue.length === 1 ? r.revenue[0] : null;
  if (!c || !v || c.currency !== v.currency || c.amount_cents <= 0) return "—";
  return `${(v.amount_cents / c.amount_cents).toFixed(1)}×`;
}

export function AdResults({ ws, from, to, brand, go }: { ws: string; from: string; to: string; brand: string; go: (tab: "tracking") => void }) {
  const [rows, setRows] = useState<AttributionAdRow[] | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/attribution/ads", { params: { path: { ws }, query: { from, to, ...(brand ? { brand } : {}) } } }).then(({ data, error }) => {
      setError(error ? errorMessage(error) : "");
      setRows(data?.data ?? []);
    });
  }, [ws, from, to, brand]);
  if (rows === null) return null;
  const untracked = rows.filter((r) => r.visits === 0 && r.cost.length > 0).length;

  return (
    <Card
      title="What each campaign brought"
      description={
        <>
          Outflare's own count: visits that came through each campaign, and the conversions and revenue credited to them under your attribution model, beside what the platform says.{" "}
          {untracked > 0 && (
            <>
              {untracked === 1 ? "One campaign" : `${untracked} campaigns`} spent with no tracked visits: add the tracking template below so its clicks carry its ids, or{" "}
              <button type="button" className="underline" onClick={() => go("tracking")}>check website tracking</button>.
            </>
          )}
        </>
      }
    >
      <ErrorText>{error}</ErrorText>
      <DataTable
        data={rows}
        rowId={(r) => r.id}
        searchPlaceholder="Search campaigns"
        empty="Nothing yet. Paid visits show up here once your ads carry Outflare's tracking template and the website snippet is installed."
        columns={[
          {
            id: "campaign",
            header: "Campaign",
            accessorFn: (r) => `${r.name} ${r.id}`,
            cell: ({ row: { original: r } }) => (
              <div className="space-y-0.5">
                <div className="font-medium">{r.name || <span className="font-mono text-[12px]">{r.id}</span>}</div>
                <div className="text-[12px] text-muted-foreground">{r.connector ? platforms[r.connector] ?? r.connector : "Not in a connected ad account"}</div>
              </div>
            ),
          },
          { id: "spend", header: "Spend", accessorFn: (r) => r.cost[0]?.amount_cents ?? 0, cell: ({ row: { original: r } }) => money(r.cost) },
          { id: "visits", header: "Visits", accessorFn: (r) => r.visits, cell: ({ row: { original: r } }) => num(r.visits) },
          { id: "conversions", header: "Conversions", accessorFn: (r) => r.conversions, cell: ({ row: { original: r } }) => num(r.conversions) },
          { id: "revenue", header: "Revenue", accessorFn: (r) => r.revenue[0]?.amount_cents ?? 0, cell: ({ row: { original: r } }) => money(r.revenue) },
          { id: "return", header: "Return", enableSorting: false, cell: ({ row: { original: r } }) => ret(r) },
          {
            id: "platform",
            header: "Platform says",
            accessorFn: (r) => r.platform_conversions ?? -1,
            cell: ({ row: { original: r } }) => <span className="text-muted-foreground">{r.platform_conversions === null ? "—" : `${num(r.platform_conversions)} ${r.platform_conversions === 1 ? "conversion" : "conversions"}`}</span>,
          },
        ]}
        mobile={(r) => (
          <span className="block min-w-0">
            <span className="block truncate text-[13px] font-medium">{r.name || r.id}</span>
            <span className="block truncate text-[12px] text-muted-foreground">{money(r.cost)} spent · {num(r.conversions)} conversions · {money(r.revenue)}</span>
          </span>
        )}
      />
    </Card>
  );
}
