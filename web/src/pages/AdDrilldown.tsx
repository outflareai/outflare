// Inside a campaign: its numbers by day, its ad sets (Google: ad groups)
// and its ads, each with what it spent and what the platform says it
// brought. Meta Ads and Google Ads read below campaigns; other platforms
// show the campaign's trend only.

import { useEffect, useRef, useState } from "react";
import type { AdCampaign, AdItem, AdSummaryRow } from "@outflare/sdk";
import { DataTable } from "@/components/data-table";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, ErrorText } from "../ui";
import { metricColumns, money, n, platformName } from "./Ads";

const metrics = [
  { id: "spend", label: "Spend", value: (r: AdSummaryRow) => r.totals.spend_micros, format: (v: number, c: string) => money(v, c) },
  { id: "clicks", label: "Clicks", value: (r: AdSummaryRow) => r.totals.clicks, format: (v: number) => n(v) },
  { id: "impressions", label: "Impressions", value: (r: AdSummaryRow) => r.totals.impressions, format: (v: number) => n(v) },
  { id: "conversions", label: "Platform conversions", value: (r: AdSummaryRow) => r.totals.conversions ?? 0, format: (v: number) => n(v) },
] as const;

// AdTrend draws one number per day from a summary grouped by day, in one
// currency, with the days that have no row at zero.
export function AdTrend({ rows, from, to }: { rows: AdSummaryRow[]; from: string; to: string }) {
  const [metric, setMetric] = useState<(typeof metrics)[number]["id"]>("spend");
  const [hover, setHover] = useState<number | null>(null);
  const currencies = [...new Set(rows.map((r) => r.currency))];
  const [currency, setCurrency] = useState("");
  const cur = currencies.includes(currency) ? currency : currencies[0] ?? "USD";
  const m = metrics.find((x) => x.id === metric)!;
  const byDay = new Map(rows.filter((r) => r.currency === cur).map((r) => [r.key, m.value(r)]));
  const days: string[] = [];
  for (let d = new Date(`${from}T00:00:00Z`); d <= new Date(`${to}T00:00:00Z`); d = new Date(d.getTime() + 86_400_000)) days.push(d.toISOString().slice(0, 10));
  const values = days.map((d) => byDay.get(d) ?? 0);
  const max = Math.max(1, ...values);
  const total = values.reduce((s, v) => s + v, 0);
  const W = 600, H = 120, bw = W / Math.max(days.length, 1);

  return (
    <div>
      <div className="mb-2 flex flex-wrap items-center gap-2">
        <div className="flex rounded-md border" role="group" aria-label="Number">
          {metrics.map((x) => (
            <button key={x.id} type="button" className={`px-2.5 py-1 text-[12px] ${x.id === metric ? "bg-muted font-medium" : "text-muted-foreground"}`} onClick={() => setMetric(x.id)}>{x.label}</button>
          ))}
        </div>
        {currencies.length > 1 && (
          <select className="h-7 rounded-md border bg-background px-2 text-[12px]" value={cur} onChange={(e) => setCurrency(e.target.value)} aria-label="Currency">
            {currencies.map((c) => <option key={c}>{c}</option>)}
          </select>
        )}
        <span className="ml-auto font-mono text-[12px] text-muted-foreground tabular-nums">{m.format(total, cur)} in all</span>
      </div>
      {rows.length === 0 ? (
        <p className="text-[13px] text-muted-foreground">Nothing in this range yet.</p>
      ) : (
        <div className="relative">
          <svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" className="h-28 w-full" role="img" aria-label={`${m.label} per day, ${m.format(total, cur)} in all`}
            onMouseLeave={() => setHover(null)}
            onMouseMove={(e) => {
              const r = e.currentTarget.getBoundingClientRect();
              setHover(Math.min(days.length - 1, Math.max(0, Math.floor(((e.clientX - r.left) / r.width) * days.length))));
            }}>
            <line x1={0} x2={W} y1={H} y2={H} className="stroke-border" strokeWidth={1} vectorEffect="non-scaling-stroke" />
            {values.map((v, i) => (
              <rect key={days[i]} x={i * bw + bw * 0.15} width={bw * 0.7} y={H - (v / max) * (H - 4)} height={(v / max) * (H - 4)} rx={1}
                style={{ fill: "var(--series-1)", opacity: hover === null || hover === i ? 1 : 0.45 }} />
            ))}
          </svg>
          {hover !== null && (
            <div className="pointer-events-none absolute top-0 z-10 rounded-md border bg-popover px-2 py-1 text-[12px] text-popover-foreground shadow-md"
              style={{ left: `${((hover + 0.5) / days.length) * 100}%`, transform: `translateX(${hover > days.length / 2 ? "-105%" : "5%"})` }}>
              <div className="font-medium">{days[hover]}</div>
              <div className="font-mono tabular-nums">{m.format(values[hover], cur)}</div>
            </div>
          )}
          <div className="mt-1 flex justify-between font-mono text-[11px] text-muted-foreground">
            <span>{days[0]}</span>
            <span>{days[days.length - 1]}</span>
          </div>
        </div>
      )}
    </div>
  );
}

function status(s: AdItem["status"], word: string) {
  if (s === "active") return null;
  return <Badge tone={s === "removed" ? "red" : "grey"} title={word}>{s === "paused" ? "Paused" : s === "removed" ? "Removed" : word || "Other"}</Badge>;
}

const host = (u: string) => {
  try {
    return new URL(u).host + new URL(u).pathname.replace(/\/$/, "");
  } catch {
    return u;
  }
};

export function CampaignDetail({ ws, campaign, from, to, onClose }: { ws: string; campaign: AdCampaign; from: string; to: string; onClose: () => void }) {
  const [trend, setTrend] = useState<AdSummaryRow[] | null>(null);
  const [groups, setGroups] = useState<AdItem[]>([]);
  const [ads, setAds] = useState<AdItem[]>([]);
  const [group, setGroup] = useState<AdItem | null>(null);
  const [error, setError] = useState("");
  const below = campaign.connector === "meta_ads" || campaign.connector === "google_ads";
  const groupWord = campaign.connector === "meta_ads" ? "Ad sets" : "Ad groups";
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => ref.current?.scrollIntoView({ behavior: "smooth", block: "start" }), [campaign.id]);

  useEffect(() => {
    const path = { ws };
    const range = { from, to };
    setGroup(null);
    Promise.all([
      api.GET("/v1/workspaces/{ws}/ads/summary", { params: { path, query: { ...range, group: "day", entity_id: campaign.id } } }),
      below ? api.GET("/v1/workspaces/{ws}/ads/items", { params: { path, query: { ...range, level: "ad_group", campaign_id: campaign.id, limit: 500 } } }) : null,
      below ? api.GET("/v1/workspaces/{ws}/ads/items", { params: { path, query: { ...range, level: "ad", campaign_id: campaign.id, limit: 1000 } } }) : null,
    ]).then(([s, g, a]) => {
      const err = s.error ?? g?.error ?? a?.error;
      setError(err ? errorMessage(err) : "");
      setTrend(s.data?.data ?? []);
      setGroups(g?.data?.data ?? []);
      setAds(a?.data?.data ?? []);
    });
  }, [ws, campaign.id, from, to, below]);

  const shown = group ? ads.filter((a) => a.parent_id === group.id) : ads;
  return (
    <div ref={ref} className="scroll-mt-16">
      <Card
        title={campaign.name}
        description={`${platformName(campaign.connector)} · ${campaign.account_name}. Its numbers by day, ${below ? `its ${groupWord.toLowerCase()} and its ads, ` : ""}as the platform reports them.`}
        action={<Button variant="ghost" onClick={onClose}>Close</Button>}
      >
        <ErrorText>{error}</ErrorText>
        {trend && <AdTrend rows={trend} from={from} to={to} />}
        {below && (
          <div className="mt-6 space-y-6">
            <div>
              <h3 className="mb-2 text-[13px] font-medium">{groupWord}</h3>
              <DataTable
                data={groups}
                rowId={(g) => g.id}
                onRowClick={(g) => setGroup(group?.id === g.id ? null : g)}
                columnsMenu={false}
                searchPlaceholder={`Search ${groupWord.toLowerCase()}`}
                empty={`No ${groupWord.toLowerCase()} spent or ran in this range.`}
                initialSort={[{ id: "spend", desc: true }]}
                columns={[
                  {
                    id: "name",
                    header: groupWord.slice(0, -1),
                    accessorFn: (g) => g.name,
                    cell: ({ row: { original: g } }) => (
                      <div className={`space-y-0.5 ${group?.id === g.id ? "font-semibold" : ""}`}>
                        <div className="font-medium">{g.name || g.external_id}</div>
                        <div className="text-[12px] text-muted-foreground">
                          {((k) => (k === 1 ? "1 ad" : `${k} ads`))(ads.filter((a) => a.parent_id === g.id).length)}{g.daily_budget_micros !== null && <> · {money(g.daily_budget_micros, g.currency)} a day</>} {status(g.status, g.provider_status)}
                        </div>
                      </div>
                    ),
                  },
                  ...metricColumns<AdItem>((g) => g.totals, (g) => g.currency),
                ]}
              />
            </div>
            <div>
              <h3 className="mb-2 flex items-center gap-2 text-[13px] font-medium">
                {group ? <>Ads in {group.name || group.external_id}</> : "Ads"}
                {group && <Button variant="ghost" onClick={() => setGroup(null)}>Show every ad</Button>}
              </h3>
              <DataTable
                data={shown}
                rowId={(a) => a.id}
                searchPlaceholder="Search ads"
                empty="No ads spent or ran in this range."
                initialSort={[{ id: "spend", desc: true }]}
                columns={[
                  {
                    id: "ad",
                    header: "Ad",
                    accessorFn: (a) => `${a.name} ${a.details.headline ?? ""} ${a.external_id}`,
                    cell: ({ row: { original: a } }) => (
                      <div className="flex max-w-md gap-3">
                        {a.details.thumbnail_url && <img src={a.details.thumbnail_url} alt="" className="size-10 shrink-0 rounded object-cover" loading="lazy" referrerPolicy="no-referrer" />}
                        <div className="min-w-0 space-y-0.5">
                          <div className="truncate font-medium">{a.name || a.details.headline || a.external_id}</div>
                          {a.details.headline && a.details.headline !== a.name && <div className="truncate text-[12px]">{a.details.headline}</div>}
                          <div className="truncate text-[12px] text-muted-foreground">
                            {!group && <>{a.parent_name} · </>}
                            {a.details.url ? <a href={a.details.url} target="_blank" rel="noreferrer" className="underline">{host(a.details.url)}</a> : `id ${a.external_id}`} {status(a.status, a.provider_status)}
                          </div>
                        </div>
                      </div>
                    ),
                  },
                  ...metricColumns<AdItem>((a) => a.totals, (a) => a.currency),
                ]}
                mobile={(a) => (
                  <span className="block min-w-0">
                    <span className="block truncate text-[13px] font-medium">{a.name || a.details.headline || a.external_id}</span>
                    <span className="block truncate text-[12px] text-muted-foreground">{money(a.totals.spend_micros, a.currency)} · {n(a.totals.clicks)} clicks · {n(a.totals.conversions)} conversions</span>
                  </span>
                )}
              />
            </div>
          </div>
        )}
      </Card>
    </div>
  );
}
