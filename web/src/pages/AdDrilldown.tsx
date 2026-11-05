// Inside a campaign: its numbers by day and its conversions by type, its
// ad sets (Google: ad groups) and its ads, each with what it spent, what the
// platform says it brought and what Outflare's own attribution credits to
// it, and for Google its keywords and the searches they matched. Meta Ads
// and Google Ads read below campaigns; other platforms show the campaign's
// trend only.

import { useEffect, useState } from "react";
import type { AdCampaign, AdConversionAction, AdItem, AdKeyword, AdSearchTerm, AdSummaryRow, AttributionAdRow } from "@outflare/sdk";
import { DataTable } from "@/components/data-table";
import { api, errorMessage } from "../api";
import { Badge, Card, ErrorText, Stat, Stats } from "../ui";
import { deliveryColumns, metricColumns, money, n, platformName } from "./Ads";
import { CampaignTargeting } from "./AdTargeting";

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

// cents formats Outflare's own money, which is in cents, one entry per
// currency.
const cents = (list: { amount_cents: number; currency: string }[] | undefined) =>
  list && list.length > 0 ? list.map((m) => money(m.amount_cents * 10_000, m.currency)).join(" · ") : "—";

// outflareColumns are what Outflare's own attribution credits to each row,
// found by the platform's id in the visits its tracking template marked.
export function outflareColumns<T extends { external_id: string }>(by: Record<string, AttributionAdRow>) {
  const mono = (s: string) => <span className="font-mono tabular-nums">{s}</span>;
  return [
    { id: "of_visits", header: "Visits", accessorFn: (r: T) => by[r.external_id]?.visits ?? 0, cell: ({ row: { original: r } }: { row: { original: T } }) => mono(n(by[r.external_id]?.visits ?? 0)) },
    { id: "of_conversions", header: "Outflare conv.", accessorFn: (r: T) => by[r.external_id]?.conversions ?? 0, cell: ({ row: { original: r } }: { row: { original: T } }) => mono(n(by[r.external_id]?.conversions ?? 0)) },
    { id: "of_revenue", header: "Outflare revenue", accessorFn: (r: T) => by[r.external_id]?.revenue[0]?.amount_cents ?? 0, cell: ({ row: { original: r } }: { row: { original: T } }) => mono(cents(by[r.external_id]?.revenue)) },
  ];
}

// ConversionActions lists the conversions the platforms counted, by their
// own action: all campaigns, or the one campaign, ad set or ad entity.
// With card, it is a card of its own, shown only when there is something.
export function ConversionActions({ ws, from, to, brand, entity, card }: { ws: string; from: string; to: string; brand?: string; entity?: string; card?: boolean }) {
  const [rows, setRows] = useState<AdConversionAction[] | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/ads/conversion-actions", { params: { path: { ws }, query: { from, to, ...(brand ? { brand } : {}), ...(entity ? { entity_id: entity } : {}) } } })
      .then(({ data, error }) => {
        setError(error ? errorMessage(error) : "");
        setRows(data?.data ?? []);
      });
  }, [ws, from, to, brand, entity]);
  if (error) return <ErrorText>{error}</ErrorText>;
  if (!rows || rows.length === 0) return null;
  const label = (a: string) => (a.startsWith("custom.") ? `Custom conversion ${a.slice(7)}` : a.replace(/_/g, " ").replace(/^./, (c) => c.toUpperCase()));
  const list = (
    <div className="divide-y rounded-md border text-[13px]">
      {rows.map((r) => (
        <div key={`${r.connector}|${r.action}|${r.currency}`} className="flex flex-wrap items-baseline justify-between gap-x-3 px-3 py-1.5">
          <span className="min-w-0 truncate">{label(r.action)} <span className="text-muted-foreground">· {platformName(r.connector)}</span></span>
          <span className="font-mono tabular-nums">{n(r.conversions)}{r.value_micros !== null && r.value_micros > 0 && <span className="text-muted-foreground"> · {money(r.value_micros, r.currency)}</span>}</span>
        </div>
      ))}
    </div>
  );
  if (!card) return list;
  return (
    <Card title="Conversions by type" description="What each platform counted, by its own action: purchases, leads, sign-ups and the rest. Meta reports a purchase several ways; each kind counts once here.">
      {list}
    </Card>
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

// CampaignDetail is a campaign's own page, or one of its ad groups' when
// groupId is set: the level's numbers, its trend by day, and the level
// below it, each row opening the next level the way the ad platforms do.
export function CampaignDetail({ ws, campaign, groupId, from, to, open, back }: { ws: string; campaign: AdCampaign; groupId?: string; from: string; to: string; open: (groupId?: string) => void; back: () => void }) {
  const [trend, setTrend] = useState<AdSummaryRow[] | null>(null);
  const [groups, setGroups] = useState<AdItem[]>([]);
  const [ads, setAds] = useState<AdItem[]>([]);
  const [setsBy, setSetsBy] = useState<Record<string, AttributionAdRow>>({});
  const [adsBy, setAdsBy] = useState<Record<string, AttributionAdRow>>({});
  const [keywords, setKeywords] = useState<AdKeyword[]>([]);
  const [terms, setTerms] = useState<AdSearchTerm[]>([]);
  const [search, setSearch] = useState<"keywords" | "terms">("keywords");
  const [error, setError] = useState("");
  const google = campaign.connector === "google_ads";
  // Meta's delivery numbers show where Meta has them.
  const hidden = campaign.connector === "meta_ads" ? [] : deliveryColumns;
  const below = campaign.connector === "meta_ads" || campaign.connector === "google_ads";
  const groupWord = campaign.connector === "meta_ads" ? "Ad sets" : "Ad groups";
  const group = groupId ? groups.find((g) => g.id === groupId) ?? null : null;
  useEffect(() => {
    window.scrollTo({ top: 0 });
  }, [campaign.id, groupId]);

  // The trend of the level open: the ad group's, else the campaign's.
  useEffect(() => {
    setTrend(null);
    api.GET("/v1/workspaces/{ws}/ads/summary", { params: { path: { ws }, query: { from, to, group: "day", entity_id: groupId ?? campaign.id } } }).then(({ data }) => setTrend(data?.data ?? []));
  }, [ws, campaign.id, groupId, from, to]);

  useEffect(() => {
    const path = { ws };
    const range = { from, to };
    const byId = (rows: AttributionAdRow[] | undefined) => Object.fromEntries((rows ?? []).map((r) => [r.id, r]));
    Promise.all([
      below ? api.GET("/v1/workspaces/{ws}/ads/items", { params: { path, query: { ...range, level: "ad_group", campaign_id: campaign.id, limit: 500 } } }) : null,
      below ? api.GET("/v1/workspaces/{ws}/ads/items", { params: { path, query: { ...range, level: "ad", campaign_id: campaign.id, limit: 1000 } } }) : null,
      below ? api.GET("/v1/workspaces/{ws}/attribution/ads", { params: { path, query: { ...range, level: "adset" } } }) : null,
      below ? api.GET("/v1/workspaces/{ws}/attribution/ads", { params: { path, query: { ...range, level: "ad" } } }) : null,
      google ? api.GET("/v1/workspaces/{ws}/ads/keywords", { params: { path, query: { ...range, campaign_id: campaign.id, limit: 500 } } }) : null,
      google ? api.GET("/v1/workspaces/{ws}/ads/search-terms", { params: { path, query: { ...range, campaign_id: campaign.id, limit: 1000 } } }) : null,
    ]).then(([g, a, os, oa, k, t]) => {
      const err = g?.error ?? a?.error ?? os?.error ?? oa?.error ?? k?.error ?? t?.error;
      setError(err ? errorMessage(err) : "");
      setGroups(g?.data?.data ?? []);
      setAds(a?.data?.data ?? []);
      setSetsBy(byId(os?.data?.data));
      setAdsBy(byId(oa?.data?.data));
      setKeywords(k?.data?.data ?? []);
      setTerms(t?.data?.data ?? []);
    });
  }, [ws, campaign.id, from, to, below, google]);

  const shown = groupId ? ads.filter((a) => a.parent_id === groupId) : ads;
  const level = group ?? campaign;
  const t = level.totals;
  const of = group ? setsBy[group.external_id] : undefined;
  const crumb = "text-blue-700 underline-offset-2 hover:underline dark:text-blue-400";
  return (
    <div className="space-y-6">
      <div className="space-y-1">
        <nav aria-label="Where you are" className="flex flex-wrap items-center gap-1.5 text-[13px] text-muted-foreground">
          <button type="button" className={crumb} onClick={back}>All campaigns</button>
          <span aria-hidden>›</span>
          {groupId ? <button type="button" className={crumb} onClick={() => open()}>{campaign.name}</button> : <span className="text-foreground">{campaign.name}</span>}
          {groupId && (
            <>
              <span aria-hidden>›</span>
              <span className="text-foreground">{group ? group.name || group.external_id : "…"}</span>
            </>
          )}
        </nav>
        <h2 className="text-lg font-semibold">{group ? group.name || group.external_id : campaign.name}</h2>
        <p className="text-[13px] text-muted-foreground">
          {group ? `${groupWord.slice(0, -1)} in ${campaign.name}` : `${platformName(campaign.connector)} · ${campaign.account_name}`}
          {!group && campaign.objective && ` · ${campaign.objective.replace(/_/g, " ")}`}
          {!group && campaign.bidding_strategy && campaign.bidding_strategy !== "unknown" && ` · bids ${campaign.bidding_strategy.replace(/_/g, " ")}`}
          {group && " "}{group && status(group.status, group.provider_status)}
        </p>
      </div>
      <ErrorText>{error}</ErrorText>
      <Stats cols={5}>
        <Stat label="Spend" value={money(t.spend_micros, level.currency)} sub={!group && campaign.spend_change != null ? `${campaign.spend_change > 0 ? "+" : ""}${Math.round(campaign.spend_change * 100)}% on the period before` : undefined} />
        <Stat label="Clicks" value={n(t.clicks)} sub={`${n(t.impressions)} impressions`} />
        <Stat label="Conversions" value={n(t.conversions)} sub={of ? `${n(of.conversions)} by Outflare's count` : "as the platform counts them"} />
        <Stat
          label="Cost per conversion"
          value={money(t.cost_per_conversion_micros, level.currency)}
          sub={!group && campaign.target_cost_per_conversion_micros != null ? `target ${money(campaign.target_cost_per_conversion_micros, campaign.currency)}` : undefined}
          tone={!group ? (campaign.target_status === "over" ? "red" : campaign.target_status === "under" ? "green" : undefined) : undefined}
        />
        <Stat label="Cost per click" value={money(t.cpc_micros, level.currency)} sub={`CTR ${t.ctr == null ? "—" : `${(t.ctr * 100).toFixed(2)}%`}`} />
      </Stats>

      <Card title="By day" description={`${groupId ? `This ${groupWord.toLowerCase().slice(0, -1)}'s` : "This campaign's"} numbers each day, as ${platformName(campaign.connector)} reports them.`}>
        {trend && <AdTrend rows={trend} from={from} to={to} />}
      </Card>

      <ConversionActions ws={ws} from={from} to={to} entity={groupId ?? campaign.id} card />

      {!groupId && (campaign.connector === "google_ads" || campaign.connector === "meta_ads") && <CampaignTargeting ws={ws} campaign={campaign} />}

      {below && !groupId && (
        <Card title={groupWord} description={`Open one for its ads${google ? ", keywords and searches" : ""}.`}>
          <DataTable
            data={groups}
            rowId={(g) => g.id}
            onRowClick={(g) => open(g.id)}
            hidden={hidden}
            searchPlaceholder={`Search ${groupWord.toLowerCase()}`}
            empty={`No ${groupWord.toLowerCase()} spent or ran in this range.`}
            initialSort={[{ id: "spend", desc: true }]}
            columns={[
              {
                id: "name",
                header: groupWord.slice(0, -1),
                accessorFn: (g) => g.name,
                cell: ({ row: { original: g } }) => (
                  <div className="space-y-0.5">
                    <div className="font-medium text-blue-700 underline-offset-2 hover:underline dark:text-blue-400">{g.name || g.external_id}</div>
                    <div className="text-[12px] text-muted-foreground">
                      {((k) => (k === 1 ? "1 ad" : `${k} ads`))(ads.filter((a) => a.parent_id === g.id).length)}{g.daily_budget_micros !== null && <> · {money(g.daily_budget_micros, g.currency)} a day</>} {status(g.status, g.provider_status)}
                    </div>
                  </div>
                ),
              },
              ...metricColumns<AdItem>((g) => g.totals, (g) => g.currency),
              ...outflareColumns<AdItem>(setsBy),
            ]}
          />
        </Card>
      )}

      {below && (
        <Card title={groupId ? "Ads" : "Every ad in this campaign"}>
              <DataTable
                data={shown}
                rowId={(a) => a.id}
                searchPlaceholder="Search ads"
                hidden={hidden}
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
                          {a.name && a.details.headline && a.details.headline !== a.name && <div className="truncate text-[12px]">{a.details.headline}</div>}
                          <div className="truncate text-[12px] text-muted-foreground">
                            {!groupId && <>{a.parent_name} · </>}
                            {a.details.url ? <a href={a.details.url} target="_blank" rel="noreferrer" className="underline">{host(a.details.url)}</a> : `id ${a.external_id}`} {status(a.status, a.provider_status)}
                          </div>
                          {a.details.templated === "false" && <Badge tone="amber" title="Its links don't carry Outflare's tracking template, so its clicks can't be tied to it. Add the template under Tracking templates.">No tracking template</Badge>}
                        </div>
                      </div>
                    ),
                  },
                  ...metricColumns<AdItem>((a) => a.totals, (a) => a.currency),
                  ...outflareColumns<AdItem>(adsBy),
                ]}
                mobile={(a) => (
                  <span className="block min-w-0">
                    <span className="block truncate text-[13px] font-medium">{a.name || a.details.headline || a.external_id}</span>
                    <span className="block truncate text-[12px] text-muted-foreground">{money(a.totals.spend_micros, a.currency)} · {n(a.totals.clicks)} clicks · {n(a.totals.conversions)} conversions</span>
                  </span>
                )}
              />
        </Card>
      )}

      {google && (
        <Card title="Search">
              <div>
                <div className="mb-2 flex rounded-md border w-fit" role="group" aria-label="Search">
                  {(["keywords", "terms"] as const).map((k) => (
                    <button key={k} type="button" className={`px-2.5 py-1 text-[12px] ${k === search ? "bg-muted font-medium" : "text-muted-foreground"}`} onClick={() => setSearch(k)}>
                      {k === "keywords" ? "Keywords" : "Search terms"}
                    </button>
                  ))}
                </div>
                {search === "keywords" ? (
                  <DataTable
                    data={groupId ? keywords.filter((k) => k.ad_group_id === groupId) : keywords}
                    rowId={(k) => k.id}
                    searchPlaceholder="Search keywords"
                    hidden={deliveryColumns}
                    empty="No keyword showed an ad in this range."
                    initialSort={[{ id: "spend", desc: true }]}
                    columns={[
                      {
                        id: "keyword",
                        header: "Keyword",
                        accessorFn: (k) => k.text,
                        cell: ({ row: { original: k } }) => (
                          <div className="space-y-0.5">
                            <div className="font-medium">{k.match_type === "exact" ? `[${k.text}]` : k.match_type === "phrase" ? `"${k.text}"` : k.text}</div>
                            <div className="text-[12px] text-muted-foreground">{k.match_type} · {k.ad_group_name} {status(k.status, "")}</div>
                          </div>
                        ),
                      },
                      ...metricColumns<AdKeyword>((k) => k.totals, (k) => k.currency),
                    ]}
                  />
                ) : (
                  <DataTable
                    data={groupId ? terms.filter((t) => t.ad_group_id === groupId) : terms}
                    rowId={(t) => `${t.ad_group_id}|${t.term}|${t.keyword_text}|${t.keyword_match_type}`}
                    searchPlaceholder="Search what people searched"
                    hidden={deliveryColumns}
                    empty="No searches in this range. Google leaves out searches too few people made."
                    initialSort={[{ id: "spend", desc: true }]}
                    columns={[
                      {
                        id: "term",
                        header: "Search",
                        accessorFn: (t) => `${t.term} ${t.keyword_text}`,
                        cell: ({ row: { original: t } }) => (
                          <div className="space-y-0.5">
                            <div className="font-medium">{t.term}</div>
                            <div className="text-[12px] text-muted-foreground">{t.keyword_text ? `matched ${t.keyword_text} (${t.keyword_match_type})` : "keyword not reported"} · {t.ad_group_name}</div>
                          </div>
                        ),
                      },
                      ...metricColumns<AdSearchTerm>((t) => t.totals, (t) => t.currency),
                    ]}
                  />
                )}
              </div>
        </Card>
      )}
    </div>
  );
}
