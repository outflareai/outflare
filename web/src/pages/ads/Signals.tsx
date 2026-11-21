// A campaign's room to grow and where its money goes: the share of
// searches it missed for want of budget or rank, and its numbers by
// device, country, network and hour. Read live from the ad network when
// the campaign is opened.

import { useEffect, useState } from "react";
import type { AdCampaign, AdCampaignSignals } from "@outflare/sdk";
import { DataTable } from "@/components/data-table";
import { pickedBrandId } from "@/lib/brand";
import { api, errorMessage } from "../../api";
import { Card, ErrorText, Loading, field } from "../../ui";
import { money, n, platformName } from "../Ads";

const cuts = [
  { id: "device", label: "Device" },
  { id: "location", label: "Country" },
  { id: "network", label: "Network" },
  { id: "hour", label: "Hour of the day" },
  { id: "placement", label: "Placement" },
] as const;
type Cut = (typeof cuts)[number]["id"];

const countryName = (code: string) => {
  try {
    return new Intl.DisplayNames(["en"], { type: "region" }).of(code) ?? code;
  } catch {
    return code;
  }
};
const words = (v: string) => (v ? v[0].toUpperCase() + v.slice(1).replace(/_/g, " ") : "Unknown");
const pct = (v: number) => `${Math.round(v * 100)}%`;

// What the two shares mean for this campaign, in a sentence each.
export function roomToGrow(s: AdCampaignSignals, overTarget: boolean): string[] {
  const out: string[] = [];
  const budget = s.budget_lost_impression_share, rank = s.rank_lost_impression_share;
  if (budget >= 0.1) out.push(overTarget
    ? `It missed ${pct(budget)} of the searches it could have shown for because the budget ran out. It costs more than your target, so fix the cost before raising the budget.`
    : `It missed ${pct(budget)} of the searches it could have shown for because the budget ran out. A higher budget would buy more of the same clicks.`);
  else out.push("The budget is not holding it back: it showed for nearly every search the budget could pay for.");
  if (rank >= 0.1) out.push(`It missed ${pct(rank)} because other advertisers ranked higher. Better ads and landing pages, or higher bids, win these back.`);
  return out;
}

export function CampaignSignals({ campaign, from, to }: { campaign: AdCampaign; from: string; to: string }) {
  const [data, setData] = useState<AdCampaignSignals | null>(null);
  const [error, setError] = useState("");
  const [cut, setCut] = useState<Cut>("device");

  useEffect(() => {
    let live = true;
    setData(null);
    setError("");
    api.GET("/v1/brands/{brand}/ads/campaigns/{id}/signals", { params: { path: { brand: pickedBrandId(), id: campaign.id }, query: { from, to } } }).then(({ data, error }) => {
      if (!live) return;
      if (error || !data) setError(errorMessage(error));
      else setData(data);
    });
    return () => {
      live = false;
    };
  }, [campaign.id, from, to]);

  if (data && !data.supported) return null;
  const network = platformName(campaign.connector);
  const has = (id: Cut) => !!data?.segments.some((s) => s.kind === id);
  const shown = has(cut) ? cut : cuts.find((c) => has(c.id))?.id ?? cut;
  const rows = (data?.segments ?? []).filter((s) => s.kind === shown);
  const name = (s: AdCampaignSignals["segments"][number]) => shown === "location" ? (s.label ? countryName(s.label) : "Unknown place") : shown === "hour" ? s.label || `${s.value}:00` : words(s.label || s.value);
  return (
    <Card title="Room to grow, and where the money goes" description={`How much more this campaign could show, and its numbers by device, country, network and hour. Source: ${network}, read just now.`}>
      {error && <ErrorText>{error}</ErrorText>}
      {!data && !error && <Loading />}
      {data && (
        <>
          <ul className="mb-4 space-y-1 text-[13px]" data-testid="room-to-grow">
            {roomToGrow(data, campaign.target_status === "over").map((line) => <li key={line}>{line}</li>)}
          </ul>
          <DataTable
            data={rows}
            rowId={(s) => `${s.kind}:${s.value}`}
            columnsMenu={false}
            empty={`${network} reported nothing by ${cuts.find((c) => c.id === shown)!.label.toLowerCase()} for these days.`}
            toolbar={
              <label className="flex items-center gap-2 text-[13px]">View by
                <select className={field} aria-label="View by" value={shown} onChange={(e) => setCut(e.target.value as Cut)}>
                  {cuts.filter((c) => has(c.id) || c.id === shown).map((c) => <option key={c.id} value={c.id}>{c.label}</option>)}
                </select>
              </label>
            }
            columns={[
              { id: "name", header: cuts.find((c) => c.id === shown)!.label, accessorFn: (s) => name(s), cell: ({ row: { original: s } }) => <span className="font-medium">{name(s)}</span> },
              { id: "spend", header: "Spend", meta: { align: "right" }, accessorFn: (s) => s.spend_micros, cell: ({ row: { original: s } }) => <span className="font-mono tabular-nums">{money(s.spend_micros, data.currency)}</span> },
              { id: "clicks", header: "Clicks", meta: { align: "right" }, accessorFn: (s) => s.clicks, cell: ({ row: { original: s } }) => <span className="font-mono tabular-nums">{n(s.clicks)}</span> },
              { id: "impressions", header: "Impressions", meta: { align: "right" }, accessorFn: (s) => s.impressions, cell: ({ row: { original: s } }) => <span className="font-mono tabular-nums">{n(s.impressions)}</span> },
              { id: "conversions", header: `${network} conversions`, meta: { align: "right" }, accessorFn: (s) => s.conversions, cell: ({ row: { original: s } }) => <span className="font-mono tabular-nums">{n(Math.round(s.conversions * 10) / 10)}</span> },
              { id: "cost", header: "Cost per conversion", meta: { align: "right" }, accessorFn: (s) => (s.conversions > 0 ? s.spend_micros / s.conversions : -1), cell: ({ row: { original: s } }) => s.conversions > 0
                ? <span className="font-mono tabular-nums">{money(Math.round(s.spend_micros / s.conversions), data.currency)}</span>
                : <span className="text-muted-foreground">{s.spend_micros > 0 ? "None yet" : "—"}</span> },
            ]}
          />
        </>
      )}
    </Card>
  );
}
