// Paid ads, Whole business. Every payment the brand took against
// everything it spent on ads, whichever ad or channel got the credit: the
// one number no network's own counting can move. Under it, the spend of
// each network per week with the revenue drawn over it, what each network
// says it won beside what Outflare counted, and whether the customers of
// each month have paid back what the month's ads cost.

import { useCallback, useEffect, useState } from "react";
import type { components } from "@outflare/sdk";
import { ArrowDown, ArrowUp } from "lucide-react";
import { niceTicks, seriesColor, short, Swatch, useWidth } from "@/components/chart-kit";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { downloadCsv, toCsv, type CsvValue } from "@/lib/csv";
import { api, errorMessage } from "../../api";
import { track } from "../../lib/analytics";
import { Button, Card, ErrorText, Loading } from "../../ui";
import { money, n, platformName } from "../Ads";
import { ChooseNumbers } from "./Numbers";
import type { AdEconomics } from "./api";

type Report = components["schemas"]["AdBusiness"];
type Totals = components["schemas"]["AdBusinessTotals"];

const per = (v: number | null | undefined, currency: string) => (v === null || v === undefined ? "—" : money(v * 1e6, currency));
// One unit of the currency, written short: $1.
const one = (currency: string) => {
  try {
    return new Intl.NumberFormat(undefined, { style: "currency", currency, maximumFractionDigits: 0 }).format(1);
  } catch {
    return `1 ${currency}`;
  }
};
const monthName = (m: string) => new Date(`${m}-01T00:00:00Z`).toLocaleDateString(undefined, { month: "long", timeZone: "UTC" });
const weekName = (d: string) => new Date(`${d}T00:00:00Z`).toLocaleDateString(undefined, { month: "short", day: "numeric", timeZone: "UTC" });
const green = "text-green-700 dark:text-green-400";
const red = "text-red-700 dark:text-red-400";

function Tile({ id, label, wide = false, value, now, was, wasText, better, note, beforeWords }: { id: string; label: string; value: string; now: number | null; was: number | null; wasText: string; better: "lower" | "higher" | null; note: React.ReactNode; beforeWords: string; wide?: boolean }) {
  const moved = now !== null && was !== null && was !== 0 ? (now - was) / was : null;
  const change = moved !== null && Math.abs(moved) < 0.0005 ? null : moved;
  const good = change === null || better === null ? null : (change < 0) === (better === "lower");
  const Arrow = change !== null && change < 0 ? ArrowDown : ArrowUp;
  return (
    <div className={`min-w-0 bg-card px-4 py-3 ${wide ? "max-lg:col-span-2" : ""}`} data-testid={`business-${id}`}>
      <div className="font-mono text-[11px] tracking-wide text-muted-foreground uppercase">{label}</div>
      <div className="mt-1 font-mono text-2xl font-medium tracking-tight tabular-nums" data-testid={`business-${id}-value`}>{value}</div>
      <div className="mt-0.5 flex flex-wrap items-center gap-x-1.5 text-[12px]">
        {change !== null ? (
          <>
            <span className={`inline-flex items-center gap-0.5 font-semibold ${good === null ? "" : good ? green : red}`}>
              <Arrow aria-hidden="true" className="size-3" />
              <span className="sr-only">{change < 0 ? "down" : "up"}</span>
              {Math.abs(change * 100).toFixed(1)}%
            </span>
            <span className="text-muted-foreground">was {wasText}</span>
          </>
        ) : (
          <span className="text-muted-foreground">{was === null || was === 0 ? `Nothing in ${beforeWords}` : "No change"}</span>
        )}
      </div>
      <div className="mt-1.5 text-[11.5px] text-muted-foreground">{note}</div>
    </div>
  );
}

// The weeks as bars of spend, one colour per network, with all revenue as
// a line over them on the same scale.
function WeeksChart({ report, colour }: { report: Report; colour: (connector: string) => string }) {
  const [ref, width] = useWidth();
  const height = 240;
  const pad = { l: 44, r: 12, t: 22, b: 26 };
  const weeks = report.weeks;
  const top = Math.max(1, ...weeks.map((w) => Math.max(w.spend_micros, w.revenue_micros) / 1e6));
  const ticks = niceTicks(0, top);
  const max = ticks[ticks.length - 1];
  const y = (v: number) => pad.t + (1 - v / max) * (height - pad.t - pad.b);
  const slot = (width - pad.l - pad.r) / Math.max(1, weeks.length);
  const bar = Math.min(72, slot * 0.5);
  const x = (i: number) => pad.l + slot * (i + 0.5);
  return (
    <div ref={ref} className="w-full" data-testid="business-weeks">
      <svg width={width} height={height} role="img" aria-label="Ad spend by network and total revenue, by week">
        {ticks.map((t) => (
          <g key={t}>
            <line x1={pad.l} x2={width - pad.r} y1={y(t)} y2={y(t)} stroke="var(--border)" strokeWidth={1} />
            <text x={pad.l - 6} y={y(t) + 3.5} textAnchor="end" fontSize={10.5} fill="var(--muted-foreground)">{short(t)}</text>
          </g>
        ))}
        {weeks.map((w, i) => {
          let base = 0;
          return (
            <g key={w.week}>
              <title>{`Week of ${weekName(w.week)}: ${money(w.spend_micros, report.currency)} spent, ${money(w.revenue_micros, report.currency)} earned`}</title>
              {[...w.spend].reverse().map((s) => {
                const from = base;
                base += s.spend_micros / 1e6;
                return <rect key={s.connector} x={x(i) - bar / 2} width={bar} y={y(base)} height={Math.max(0, y(from) - y(base) - 1)} rx={2} fill={colour(s.connector)} />;
              })}
              <text x={x(i)} y={height - 8} textAnchor="middle" fontSize={10.5} fill="var(--muted-foreground)">{weekName(w.week)}</text>
            </g>
          );
        })}
        {weeks.length > 1 && <polyline fill="none" stroke="var(--foreground)" strokeWidth={1.75} points={weeks.map((w, i) => `${x(i)},${y(Math.max(0, w.revenue_micros / 1e6))}`).join(" ")} />}
        {weeks.map((w, i) => (
          <g key={w.week}>
            <circle cx={x(i)} cy={y(Math.max(0, w.revenue_micros / 1e6))} r={3.5} fill="var(--card)" stroke="var(--foreground)" strokeWidth={1.75} />
            <text x={x(i)} y={y(Math.max(0, w.revenue_micros / 1e6)) - 8} textAnchor="middle" fontSize={10.5} fontWeight={600} fill="var(--foreground)">{short(w.revenue_micros / 1e6)}</text>
          </g>
        ))}
      </svg>
    </div>
  );
}

export function WholeBusiness({ brand, from, to, economics, canManage, reload, beforeWords }: { brand: string; from: string; to: string; economics: AdEconomics | null; canManage: boolean; reload: () => void; beforeWords: string }) {
  const [report, setReport] = useState<Report | null>(null);
  const [error, setError] = useState("");
  const [choosing, setChoosing] = useState(false);
  const load = useCallback(() => {
    setReport(null);
    api.GET("/v1/brands/{brand}/ads/business", { params: { path: { brand }, query: { from, to } } }).then(({ data, error }) => (error ? setError(errorMessage(error)) : (setError(""), setReport(data))));
  }, [brand, from, to]);
  useEffect(load, [load]);
  if (error) return <ErrorText>{error}</ErrorText>;
  if (!report) return <Loading />;

  const cur = report.currency;
  const t: Totals = report.totals;
  const was: Totals = report.previous;
  const order = report.networks.map((x) => x.connector);
  const colour = (connector: string) => seriesColor(Math.max(0, order.indexOf(connector)));
  const ratio = t.revenue_per_spend;
  const target = report.target_roas;
  const headline =
    t.spend_micros === 0
      ? "No ad spend in these days."
      : `Every ${one(cur)} of ads brought back ${per(ratio, cur)} in revenue${was.revenue_per_spend !== null && was.revenue_per_spend !== undefined && ratio !== null && ratio !== undefined && Math.abs(ratio - was.revenue_per_spend) >= 0.005 ? `, ${ratio > was.revenue_per_spend ? "up" : "down"} from ${per(was.revenue_per_spend, cur)}` : ""}.`;
  const targetNote =
    target === null || target === undefined || target <= 0 ? (
      "No target set"
    ) : ratio === null || ratio === undefined ? (
      `Target · ${per(target, cur)}`
    ) : (
      <span className="inline-flex items-center gap-1.5">
        <span aria-hidden="true" className={`size-2 rounded-full ${ratio >= target * 0.9 ? "bg-green-500" : "bg-red-500"}`} />
        <span><span className={ratio >= target * 0.9 ? green : red}>{ratio >= target * 1.1 ? "Over target" : ratio >= target * 0.9 ? "On target" : "Under target"}</span> · {per(target, cur)}</span>
      </span>
    );
  const claimed = report.networks.reduce((a, x) => a + x.platform_conversions, 0);
  const counted = report.networks.reduce((a, x) => a + x.new_payers, 0);
  const csv = () => {
    const rows: CsvValue[][] = [
      ["Totals", "Total revenue", t.revenue_micros / 1e6],
      ["Totals", "Ad spend, all networks", t.spend_micros / 1e6],
      ["Totals", "Revenue per 1 of ads", ratio ?? ""],
      ["Totals", "New customers, all sources", t.new_customers],
      ["Totals", "New customers from ads", t.new_customers_from_ads],
      ["Totals", "Cost per new customer, blended", t.cost_per_new_customer_micros === null ? "" : t.cost_per_new_customer_micros / 1e6],
      ...report.weeks.flatMap((w): CsvValue[][] => [["Week of " + w.week, "Total revenue", w.revenue_micros / 1e6], ...w.spend.map((s): CsvValue[] => ["Week of " + w.week, `Spend, ${platformName(s.connector)}`, s.spend_micros / 1e6])]),
      ...report.networks.flatMap((x): CsvValue[][] => [
        [platformName(x.connector), "Spend", x.spend_micros / 1e6],
        [platformName(x.connector), "Network says", x.platform_conversions],
        [platformName(x.connector), "Outflare counted", x.new_payers],
      ]),
      ...report.cohorts.flatMap((c): CsvValue[][] => [
        ["First paid in " + c.month, "New customers", c.new_customers],
        ["First paid in " + c.month, "Cost each", c.cost_each_micros === null ? "" : c.cost_each_micros / 1e6],
        ...report.cohort_days.map((d, i): CsvValue[] => ["First paid in " + c.month, `Revenue each after ${d} days`, c.revenue_each_micros[i] === null ? "" : (c.revenue_each_micros[i] as number) / 1e6]),
        ["First paid in " + c.month, "Paid back in (days)", c.payback_days ?? ""],
      ]),
    ];
    downloadCsv(`whole-business-${from}-to-${to}.csv`, toCsv(["Part", "Number", `Value (${cur})`], rows));
    track("ui.ads_business_downloaded", {});
  };
  const th = "px-3 py-2 text-right font-mono text-[10.5px] font-normal tracking-wide text-muted-foreground uppercase whitespace-nowrap";
  const td = "px-3 py-2 text-right font-mono text-[12.5px] tabular-nums whitespace-nowrap";
  const thisMonth = to.slice(0, 7);

  return (
    <div className="space-y-4" data-testid="whole-business">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 max-w-3xl">
          <h2 className="text-[17px] font-semibold tracking-tight" data-testid="business-headline">{headline}</h2>
          <p className="text-[13px] text-muted-foreground">All your ad spend against all your revenue, whichever ad or channel got the credit. This is the number no ad network can inflate.</p>
        </div>
        <div className="flex flex-wrap gap-2">
          <Popover>
            <PopoverTrigger asChild><Button variant="ghost">How this is counted</Button></PopoverTrigger>
            <PopoverContent align="end" className="w-[24rem] space-y-2 text-[12.5px]">
              <p><b>Total revenue.</b> Every payment Outflare received for this brand in these days, less refunds, from ads or not.</p>
              <p><b>Ad spend.</b> What every connected network charged in the same days.</p>
              <p><b>New customers.</b> People whose first ever payment fell in these days. “From ads” are the ones Outflare traced to an ad click.</p>
              <p><b>Network says.</b> The conversions each network reports with its own counting. Two networks can claim the same customer, so their total can be higher than the real one.</p>
              <p><b>Payback.</b> For each month: that month's ad spend divided by its new customers, against what a customer had paid 30, 60, 90 and 120 days after their first payment. A column fills in once at least half of the month's customers have had that long.</p>
              <p className="text-muted-foreground">Everything is in {cur}.{report.left_out_currencies.length > 0 && ` Money in ${report.left_out_currencies.join(", ")} has no exchange rate and is left out.`}</p>
            </PopoverContent>
          </Popover>
          {canManage && <ChooseNumbers brand={brand} economics={economics} currency={cur} open={choosing} setOpen={setChoosing} saved={() => { reload(); load(); }} label="Set targets" />}
          <Button variant="ghost" onClick={csv}>Download CSV</Button>
        </div>
      </div>

      <div className="overflow-hidden rounded-lg border">
        <div className="grid grid-cols-2 gap-px bg-border lg:grid-cols-5">
          <Tile id="revenue" label="Total revenue" value={money(t.revenue_micros, cur)} now={t.revenue_micros} was={was.revenue_micros} wasText={money(was.revenue_micros, cur)} better="higher" beforeWords={beforeWords} note="Every payment, from ads or not" />
          <Tile id="spend" label="Ad spend, all networks" value={money(t.spend_micros, cur)} now={t.spend_micros} was={was.spend_micros} wasText={money(was.spend_micros, cur)} better={null} beforeWords={beforeWords} note={order.length > 0 ? order.map(platformName).join(", ") : "No network spent"} />
          <Tile id="ratio" label={`Revenue per ${one(cur)} of ads`} value={per(ratio, cur)} now={ratio ?? null} was={was.revenue_per_spend ?? null} wasText={per(was.revenue_per_spend, cur)} better="higher" beforeWords={beforeWords} note={targetNote} />
          <Tile id="new" label="New customers, all sources" value={n(t.new_customers)} now={t.new_customers} was={was.new_customers} wasText={n(was.new_customers)} better="higher" beforeWords={beforeWords} note={`${n(t.new_customers_from_ads)} from ads, ${n(t.new_customers - t.new_customers_from_ads)} from elsewhere`} />
          <Tile id="cost" wide label="Cost per new customer, blended" value={money(t.cost_per_new_customer_micros, cur)} now={t.cost_per_new_customer_micros ?? null} was={was.cost_per_new_customer_micros ?? null} wasText={money(was.cost_per_new_customer_micros, cur)} better="lower" beforeWords={beforeWords} note="Ad spend ÷ every new customer" />
        </div>
      </div>

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-2 [&>*]:min-w-0">
        <Card title="Ad spend against total revenue, by week" description="Bars are what you spent on each network. The line is everything you earned.">
          {report.weeks.length === 0 ? (
            <p className="text-[13px] text-muted-foreground">Nothing was spent or earned in these days.</p>
          ) : (
            <>
              <div className="mb-2 flex flex-wrap items-center gap-x-4 gap-y-1 text-[12px] text-muted-foreground">
                {order.map((c) => <span key={c} className="inline-flex items-center gap-1.5"><Swatch color={colour(c)} />{platformName(c)}</span>)}
                <span className="inline-flex items-center gap-1.5"><span className="inline-block h-0 w-4 border-t-2 border-foreground" />Total revenue</span>
              </div>
              <WeeksChart report={report} colour={colour} />
            </>
          )}
        </Card>

        <Card title="Where the money went" description="What each network says it won, next to the new customers Outflare could trace to it.">
          {report.networks.length === 0 ? (
            <p className="text-[13px] text-muted-foreground">No network spent anything in these days.</p>
          ) : (
            <>
              <div className="-mx-1 overflow-x-auto">
                <table className="w-full" data-testid="business-networks">
                  <thead>
                    <tr className="border-b">
                      <th className={`${th} text-left!`}>Network</th>
                      <th className={th}>Spend</th>
                      <th className={th}>Share</th>
                      <th className={th} title="Conversions the network reports, with its own counting">Network says</th>
                      <th className={th} title="New paying customers Outflare traced to this network's clicks">Outflare counted</th>
                      <th className={th} title="Spend divided by the customers Outflare counted">Cost each</th>
                    </tr>
                  </thead>
                  <tbody>
                    {report.networks.map((x) => (
                      <tr key={x.connector} className="border-b">
                        <td className="px-3 py-2 text-[13px] whitespace-nowrap"><span className="inline-flex items-center gap-2"><Swatch color={colour(x.connector)} />{platformName(x.connector)}</span></td>
                        <td className={td}>{money(x.spend_micros, cur)}</td>
                        <td className={td}>{Math.round(x.share * 100)}%</td>
                        <td className={td}>{n(x.platform_conversions)}</td>
                        <td className={td}>{n(x.new_payers)}</td>
                        <td className={td}>{money(x.cost_per_new_payer_micros, cur)}</td>
                      </tr>
                    ))}
                    <tr className="font-semibold">
                      <td className="px-3 py-2 text-[13px] whitespace-nowrap">All networks</td>
                      <td className={td}>{money(t.spend_micros, cur)}</td>
                      <td className={td}>100%</td>
                      <td className={td}>{n(claimed)}</td>
                      <td className={td}>{n(counted)}</td>
                      <td className={td}>{counted > 0 ? money(t.spend_micros / counted, cur) : "—"}</td>
                    </tr>
                  </tbody>
                </table>
              </div>
              <p className="mt-3 text-[12.5px] text-muted-foreground" data-testid="business-claim">
                {Math.round(claimed) > counted
                  ? `The networks claim ${n(claimed)} between them. Outflare traced ${n(counted)} new ${counted === 1 ? "customer" : "customers"} to ads.`
                  : `The networks claim ${n(claimed)} and Outflare traced ${n(counted)} new ${counted === 1 ? "customer" : "customers"} to ads.`}
              </p>
            </>
          )}
        </Card>
      </div>

      <Card title="Do new customers pay back what they cost?" description="Each row is the customers who first paid in that month. Green means they have already earned back more than it cost to get them.">
        <div className="-mx-1 overflow-x-auto">
          <table className="w-full" data-testid="business-cohorts">
            <thead>
              <tr className="border-b">
                <th className={`${th} text-left!`}>First paid in</th>
                <th className={th}>New customers</th>
                <th className={th} title="The month's ad spend divided by its new customers">Cost each</th>
                {report.cohort_days.map((d, i) => <th key={d} className={th}>{i === 0 ? `Revenue each after ${d} days` : `After ${d} days`}</th>)}
                <th className={th}>Paid back in</th>
              </tr>
            </thead>
            <tbody>
              {report.cohorts.map((c) => (
                <tr key={c.month} className="border-b last:border-0" data-testid={`business-cohort-${c.month}`}>
                  <td className="px-3 py-2 text-[13px] whitespace-nowrap">{monthName(c.month)}{c.month === thisMonth && to >= new Date().toISOString().slice(0, 10) ? " so far" : ""}</td>
                  <td className={td}>{n(c.new_customers)}</td>
                  <td className={td}>{money(c.cost_each_micros, cur)}</td>
                  {c.revenue_each_micros.map((v, i) => {
                    const paid = v !== null && c.cost_each_micros !== null ? v >= c.cost_each_micros : null;
                    return (
                      <td key={i} className={`${td} ${v === null ? "text-muted-foreground" : paid === null ? "" : paid ? "bg-green-500/15" : "bg-red-500/10"}`}>
                        {v === null ? "not yet" : money(v, cur)}
                      </td>
                    );
                  })}
                  <td className={`${td} font-semibold ${c.payback_days === null ? "font-normal text-muted-foreground" : c.payback_estimated ? "text-amber-700 dark:text-amber-400" : green}`}>
                    {c.new_customers === 0 ? "—" : c.cost_each_micros === null ? "no ad spend" : c.payback_days === null ? (c.payback_estimated ? "over a year" : "too early") : `${c.payback_estimated ? "about " : ""}${c.payback_days} days`}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Card>
      <p className="text-[12px] text-muted-foreground">Source: ad network spend, and the payments Outflare received for this brand.</p>
    </div>
  );
}
