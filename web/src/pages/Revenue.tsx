// Revenue (docs/revenue.md): what the brand earns every month and how
// that moved, what it collected, which renewals are coming up and how
// much revenue sits with customers whose health score is low. Every card
// hides when the brand has no data for it, so a brand that only tracks
// renewals sees only renewals.

import { useEffect, useState } from "react";
import type { RevenueReport } from "@outflare/sdk";
import { PickBrand } from "@/components/pick-brand";
import { bandText, bandTone } from "@/components/record-scores";
import { useBrand } from "@/lib/brand";
import { track } from "@/lib/analytics";
import { api, errorMessage } from "../api";
import { plural } from "../format";
import { Button, Card, Empty, ErrorText, Loading, PageHeader, Stat, Stats, Status } from "../ui";
import type { Tab } from "../App";
import { Commissions } from "./Commissions";
import { RevenueForecast } from "./RevenueForecast";
import { SalesTeam } from "./SalesTeam";

type Props = { ws: string; canManage: boolean; open?: string; onOpen: (item: string) => void; go: (tab: Tab, item?: string) => void };
type Period = RevenueReport["periods"][number];
type Band = "high" | "medium" | "low";

const ranges = [
  { days: 30, label: "30 days" },
  { days: 90, label: "90 days" },
  { days: 180, label: "6 months" },
  { days: 365, label: "12 months" },
];

// The ways recurring revenue moves, in the order they stack.
const moves = [
  { key: "new_minor", label: "New", up: true, color: "bg-green-500" },
  { key: "expansion_minor", label: "Expansion", up: true, color: "bg-emerald-300 dark:bg-emerald-700" },
  { key: "reactivation_minor", label: "Came back", up: true, color: "bg-sky-400 dark:bg-sky-600" },
  { key: "contraction_minor", label: "Contraction", up: false, color: "bg-amber-400 dark:bg-amber-600" },
  { key: "churned_minor", label: "Churned", up: false, color: "bg-red-500" },
] as const;

const bandColor: Record<Band, string> = { high: "bg-green-500", medium: "bg-amber-400", low: "bg-red-500" };

function money(minor: number, currency: string, whole = true): string {
  const digits = ["JPY", "KRW", "ISK", "HUF"].includes(currency) ? 0 : 2;
  try {
    return new Intl.NumberFormat(undefined, { style: "currency", currency, maximumFractionDigits: whole ? 0 : digits }).format(minor / 10 ** digits);
  } catch {
    return `${Math.round(minor / 10 ** digits)} ${currency}`;
  }
}

const percent = (v: number) => `${Math.round(v * 100)}%`;

function periodLabel(iso: string, group: RevenueReport["group"]): string {
  const d = new Date(`${iso}T00:00:00Z`);
  return group === "month"
    ? d.toLocaleDateString(undefined, { month: "short", year: "2-digit", timeZone: "UTC" })
    : d.toLocaleDateString(undefined, { month: "short", day: "numeric", timeZone: "UTC" });
}

// When a renewal is due, in words.
function renewsText(iso: string): { text: string; late: boolean } {
  const today = new Date().toISOString().slice(0, 10);
  const days = Math.round((Date.parse(`${iso}T00:00:00Z`) - Date.parse(`${today}T00:00:00Z`)) / 86400000);
  if (days < 0) return { text: `${plural(-days, "day")} ago`, late: true };
  if (days === 0) return { text: "today", late: false };
  return { text: `in ${plural(days, "day")}`, late: false };
}

export function Revenue({ ws, canManage, open, onOpen, go }: Props) {
  const { brand, active } = useBrand(ws);
  const brandId = brand?.id;
  const [days, setDays] = useState(90);
  const [report, setReport] = useState<RevenueReport | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    if (!brandId) return;
    let live = true;
    void api.GET("/v1/brands/{brand}/revenue", { params: { path: { brand: brandId }, query: { days } } }).then(({ data, error }) => {
      if (!live) return;
      if (error || !data) return setError(errorMessage(error));
      setError("");
      setReport(data);
    });
    return () => {
      live = false;
    };
  }, [brandId, days]);

  if (!brand) return <PickBrand what="Revenue" brands={active.length} />;
  if (open === "forecast") return <RevenueForecast key={brand.id} ws={ws} brandId={brand.id} canManage={canManage} go={go} onBack={() => onOpen("")} />;
  if (open === "team") return <SalesTeam key={brand.id} ws={ws} brandId={brand.id} go={go} onBack={() => onOpen("")} />;
  if (open === "commissions") return <Commissions key={brand.id} ws={ws} brandId={brand.id} go={go} onBack={() => onOpen("")} />;

  const r = report;
  const health = r?.bands.health as Partial<Record<Band, number>> | undefined;
  const nothing = r && !r.recurring && !r.collected && r.renewals.length === 0 && !health && r.spending.up.length + r.spending.down.length === 0;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Revenue"
        description="What you earn every month and how it moved, what you collected, the renewals coming up, and how much sits with customers who aren't healthy."
        action={
          <div role="tablist" aria-label="Period" className="flex flex-wrap gap-1">
            <Button
              variant="primary"
              onClick={() => {
                onOpen("forecast");
                track("ui.revenue_forecast_opened");
              }}
            >
              Forecast
            </Button>
            <Button
              variant="ghost"
              onClick={() => {
                onOpen("team");
                track("ui.sales_team_opened");
              }}
            >
              Sales team
            </Button>
            <Button
              variant="ghost"
              className="mr-2"
              onClick={() => {
                onOpen("commissions");
                track("ui.commissions_opened");
              }}
            >
              Commissions
            </Button>
            {ranges.map((x) => (
              <Button
                key={x.days}
                role="tab"
                aria-selected={days === x.days}
                variant="ghost"
                className={days === x.days ? "bg-muted" : "border-transparent text-muted-foreground"}
                onClick={() => {
                  setDays(x.days);
                  track("ui.revenue_period_changed", { days: x.days });
                }}
              >
                {x.label}
              </Button>
            ))}
          </div>
        }
      />
      {error && <ErrorText>{error}</ErrorText>}
      {!r && !error && <Loading />}

      {nothing && (
        <Empty title="No revenue to show yet">
          <p>This page fills in by itself from three things. Add the ones that fit your business:</p>
          <ul className="mx-auto mt-3 max-w-md list-disc space-y-1.5 pl-5 text-left">
            <li>
              <button className="underline underline-offset-4" onClick={() => go("connectors")}>Connect your billing</button> (your own Stripe, Paddle or Chargebee account) for recurring revenue and payments.
            </li>
            <li>
              <button className="underline underline-offset-4" onClick={() => go("people", "scores")}>Add a health score</button> to see how much revenue is at risk.
            </li>
            <li>
              <button className="underline underline-offset-4" onClick={() => go("playbooks", "flag-renewals-and-quiet-accounts")}>Add a "Renews on" date</button> to companies or agreements for the renewal list.
            </li>
          </ul>
        </Empty>
      )}

      {r?.recurring && <Recurring r={r} rec={r.recurring} go={go} />}

      {r && r.renewals.length > 0 && (
        <Card title="Renewals" description="From a month late to 90 days out, soonest first.">
          <ul className="divide-y">
            {r.renewals.map((x) => {
              const due = renewsText(x.renews_on);
              const value = (x.value ?? null) as { amount_minor?: number; currency?: string } | null;
              return (
                <li key={x.record_id} className="flex flex-wrap items-center gap-x-4 gap-y-1 py-2.5 text-sm">
                  <span className={`w-44 shrink-0 tabular-nums ${due.late ? "font-medium text-red-700 dark:text-red-400" : "text-muted-foreground"}`} title={x.renews_on}>
                    {due.late ? `Was due ${due.text}` : `Renews ${due.text}`}
                  </span>
                  <span className="min-w-0 flex-1">
                    <button className="truncate font-medium underline-offset-2 hover:underline" onClick={() => go("people", x.record_id)}>{x.name}</button>
                    {x.company_id && x.company_name && (
                      <>
                        {" "}
                        <span className="text-muted-foreground">for</span>{" "}
                        <button className="underline-offset-2 hover:underline" onClick={() => go("people", x.company_id!)}>{x.company_name}</button>
                      </>
                    )}
                  </span>
                  {x.health_band && <Status tone={bandTone[x.health_band]}>{bandText[x.health_band]} health</Status>}
                  <span className="w-32 shrink-0 text-right tabular-nums">
                    {typeof value?.amount_minor === "number" ? money(value.amount_minor, value.currency ?? r.currency) : x.mrr_minor != null ? `${money(x.mrr_minor, r.currency)} a month` : ""}
                  </span>
                </li>
              );
            })}
          </ul>
        </Card>
      )}

      {r && r.at_risk.length > 0 && (
        <Card title="Revenue at risk" description="Paying customers whose health score is low, the biggest first.">
          <ul className="divide-y">
            {r.at_risk.map((x) => (
              <li key={x.record_id} className="flex items-center gap-4 py-2.5 text-sm">
                {x.health != null && <span className="flex size-8 shrink-0 items-center justify-center rounded-full border-2 border-red-500/50 text-[12px] font-semibold tabular-nums text-red-700 dark:text-red-400">{x.health}</span>}
                <button className="min-w-0 flex-1 truncate text-left font-medium underline-offset-2 hover:underline" onClick={() => go("people", x.record_id)}>{x.name}</button>
                <span className="shrink-0 tabular-nums">{money(x.mrr_minor, r.currency)} a month</span>
              </li>
            ))}
          </ul>
        </Card>
      )}

      {r && (r.spending.up.length > 0 || r.spending.down.length > 0) && (
        <div className="grid gap-6 lg:grid-cols-2">
          <Movers title="Spending less" description={`Paid less than in the ${r.days} days before, the biggest drop first.`} list={r.spending.down} currency={r.currency} go={go} />
          <Movers title="Spending more" description={`Paid more than in the ${r.days} days before, the biggest rise first.`} list={r.spending.up} currency={r.currency} go={go} />
        </div>
      )}

      {r && health && <HealthSpread health={health} go={go} />}

      {r?.collected && <Collected r={r} c={r.collected} />}

      {r && r.products.length > 0 && <Products r={r} />}
    </div>
  );
}

function Recurring({ r, rec, go }: { r: RevenueReport; rec: NonNullable<RevenueReport["recurring"]>; go: Props["go"] }) {
  const net = rec.new_minor + rec.expansion_minor + rec.reactivation_minor - rec.contraction_minor - rec.churned_minor;
  const moved = moves.some((m) => rec[m.key] > 0);
  return (
    <>
      <Stats cols={5}>
        <Stat label="Monthly recurring" value={money(rec.mrr_minor, r.currency)} sub={net === 0 ? "No change in this period" : `${net > 0 ? "Up" : "Down"} ${money(Math.abs(net), r.currency)} in this period`} />
        <Stat label="Yearly run rate" value={money(rec.arr_minor, r.currency)} sub="Monthly recurring times 12" />
        <Stat label="Paying customers" value={rec.customers} sub={`${rec.new_customers} new, ${rec.churned_customers} left`} />
        <Stat
          label="Net revenue retention"
          value={rec.net_retention == null ? "–" : percent(rec.net_retention)}
          tone={rec.net_retention == null ? undefined : rec.net_retention >= 1 ? "green" : rec.net_retention < 0.9 ? "red" : "amber"}
          sub={rec.gross_retention == null ? "Needs revenue at the start of the period" : `${percent(rec.gross_retention)} without expansion`}
        />
        <Stat
          label="At risk"
          value={money(rec.at_risk_minor, r.currency)}
          tone={rec.at_risk_minor > 0 ? "red" : undefined}
          sub={r.bands.health ? `${plural(rec.at_risk_customers, "customer")} with low health` : <button className="underline underline-offset-4" onClick={() => go("people", "scores")}>Add a health score</button>}
        />
      </Stats>

      <Card
        title="How recurring revenue moved"
        description={moved ? `Each bar is a ${r.group}: what came in above the line, what left below it.` : `Nothing has moved since counting began on ${periodLabel(rec.counted_since, "day")}. New, expanded and lost revenue show here as it happens.`}
      >
        {moved && <Movement r={r} />}
        <dl className="mt-4 grid grid-cols-2 gap-x-6 gap-y-2 text-sm sm:grid-cols-5">
          {moves.map((m) => (
            <div key={m.key} className="min-w-0">
              <dt className="flex items-center gap-1.5 text-[12px] text-muted-foreground">
                <span className={`size-2 shrink-0 rounded-sm ${m.color}`} />
                {m.label}
              </dt>
              <dd className="tabular-nums">{rec[m.key] === 0 ? "–" : `${m.up ? "+" : "−"}${money(rec[m.key], r.currency)}`}</dd>
            </div>
          ))}
        </dl>
      </Card>
    </>
  );
}

// Movement draws one stacked bar per period around a zero line: gains
// above, losses below, to one scale.
function Movement({ r }: { r: RevenueReport }) {
  const up = (p: Period) => p.new_minor + p.expansion_minor + p.reactivation_minor;
  const down = (p: Period) => p.contraction_minor + p.churned_minor;
  const maxUp = Math.max(1, ...r.periods.map(up));
  const maxDown = Math.max(0, ...r.periods.map(down));
  const total = maxUp + maxDown;
  const height = 180;
  const upH = (maxUp / total) * height;
  const every = Math.max(1, Math.ceil(r.periods.length / 8));
  return (
    <div role="img" aria-label="Recurring revenue gained and lost in each period">
      <div className="flex items-stretch gap-[3px]" style={{ height }}>
        {r.periods.map((p) => {
          const tip = `${periodLabel(p.period, r.group)}: ${moves.filter((m) => p[m.key] > 0).map((m) => `${m.label} ${m.up ? "+" : "−"}${money(p[m.key], r.currency)}`).join(", ") || "no change"}. Ended at ${money(p.mrr_minor, r.currency)} a month.`;
          return (
            <div key={p.period} className="flex min-w-0 flex-1 flex-col" title={tip}>
              <div className="flex flex-col-reverse" style={{ height: upH }}>
                {moves.filter((m) => m.up && p[m.key] > 0).map((m) => (
                  <div key={m.key} className={`${m.color} first:rounded-b-none last:rounded-t-sm`} style={{ height: `${(p[m.key] / maxUp) * 100}%` }} />
                ))}
              </div>
              <div className="h-px shrink-0 bg-border" />
              <div className="flex flex-1 flex-col">
                {moves.filter((m) => !m.up && p[m.key] > 0).map((m) => (
                  <div key={m.key} className={`${m.color} last:rounded-b-sm`} style={{ height: `${(p[m.key] / Math.max(1, maxDown)) * 100}%` }} />
                ))}
              </div>
            </div>
          );
        })}
      </div>
      <div className="mt-1.5 flex gap-[3px] text-[11px] text-muted-foreground">
        {r.periods.map((p, i) => (
          <span key={p.period} className="min-w-0 flex-1 overflow-visible whitespace-nowrap">
            {i % every === 0 ? periodLabel(p.period, r.group) : ""}
          </span>
        ))}
      </div>
    </div>
  );
}

function Movers({ title, description, list, currency, go }: { title: string; description: string; list: RevenueReport["spending"]["up"]; currency: string; go: Props["go"] }) {
  return (
    <Card title={title} description={description}>
      {list.length === 0 ? (
        <p className="py-2 text-sm text-muted-foreground">Nobody in this period.</p>
      ) : (
        <ul className="divide-y">
          {list.map((x) => {
            const diff = x.paid_minor - x.previous_paid_minor;
            return (
              <li key={x.record_id} className="flex items-center gap-4 py-2.5 text-sm">
                <button className="min-w-0 flex-1 truncate text-left font-medium underline-offset-2 hover:underline" onClick={() => go("people", x.record_id)}>{x.name}</button>
                <span className="shrink-0 tabular-nums text-muted-foreground" title="Before, then now">{money(x.previous_paid_minor, currency)} → {money(x.paid_minor, currency)}</span>
                <span className={`w-24 shrink-0 text-right font-medium tabular-nums ${diff > 0 ? "text-green-700 dark:text-green-400" : "text-red-700 dark:text-red-400"}`}>{diff > 0 ? "+" : "−"}{money(Math.abs(diff), currency)}</span>
              </li>
            );
          })}
        </ul>
      )}
    </Card>
  );
}

function HealthSpread({ health, go }: { health: Partial<Record<Band, number>>; go: Props["go"] }) {
  const order: Band[] = ["high", "medium", "low"];
  const total = order.reduce((n, b) => n + (health[b] ?? 0), 0);
  if (total === 0) return null;
  return (
    <Card title="Account health" description="Every company by its health score, as last counted." action={<Button variant="ghost" onClick={() => go("people", "scores")}>Health score</Button>}>
      <div className="flex h-3 gap-0.5 overflow-hidden rounded-full" role="img" aria-label={order.map((b) => `${health[b] ?? 0} ${bandText[b].toLowerCase()}`).join(", ")}>
        {order.filter((b) => (health[b] ?? 0) > 0).map((b) => (
          <div key={b} className={bandColor[b]} style={{ width: `${((health[b] ?? 0) / total) * 100}%` }} />
        ))}
      </div>
      <dl className="mt-3 flex flex-wrap gap-x-8 gap-y-2 text-sm">
        {order.map((b) => (
          <div key={b}>
            <dt className="flex items-center gap-1.5 text-[12px] text-muted-foreground">
              <span className={`size-2 rounded-sm ${bandColor[b]}`} />
              {bandText[b]}
            </dt>
            <dd className="tabular-nums">
              {health[b] ?? 0} <span className="text-muted-foreground">({percent((health[b] ?? 0) / total)})</span>
            </dd>
          </div>
        ))}
      </dl>
    </Card>
  );
}

// Products splits what was collected by the product each payment names.
function Products({ r }: { r: RevenueReport }) {
  const total = r.products.reduce((n, p) => n + p.paid_minor, 0);
  const max = Math.max(1, ...r.products.map((p) => p.paid_minor));
  return (
    <Card title="By product" description="What each product brought in this period, from the payments that name it.">
      <ul className="divide-y">
        {r.products.map((p) => {
          const change = p.previous_paid_minor > 0 ? (p.paid_minor - p.previous_paid_minor) / p.previous_paid_minor : null;
          return (
            <li key={p.product_id ?? "none"} className="flex flex-wrap items-center gap-x-4 gap-y-1 py-2.5 text-sm">
              <span className="min-w-0 flex-1">
                <span className={`block truncate ${p.product_id ? "font-medium" : "text-muted-foreground"}`}>{p.name ?? "No product named"}</span>
                <span className="mt-1 block h-1.5 overflow-hidden rounded-full bg-muted" aria-hidden>
                  <span className="block h-full rounded-full bg-foreground/70" style={{ width: `${(p.paid_minor / max) * 100}%` }} />
                </span>
              </span>
              <span className="w-28 shrink-0 text-right text-xs text-muted-foreground">{p.customers === 1 ? "1 customer" : `${p.customers} customers`}</span>
              <span className={`w-36 shrink-0 text-right text-xs ${change == null ? "text-muted-foreground" : change >= 0 ? "text-green-700 dark:text-green-400" : "text-red-700 dark:text-red-400"}`}>
                {change == null ? (p.paid_minor > 0 ? "New this period" : "Nothing this period") : `${change >= 0 ? "Up" : "Down"} ${percent(Math.abs(change))}`}
              </span>
              <span className="w-16 shrink-0 text-right text-xs tabular-nums text-muted-foreground">{total > 0 ? percent(p.paid_minor / total) : "–"}</span>
              <span className="w-24 shrink-0 text-right font-medium tabular-nums">{money(p.paid_minor, r.currency)}</span>
            </li>
          );
        })}
      </ul>
      <p className="mt-3 text-xs text-muted-foreground">Up or down is against the same number of days before. A payment names its product when your billing connection or the API sends one.</p>
    </Card>
  );
}

function Collected({ r, c }: { r: RevenueReport; c: NonNullable<RevenueReport["collected"]> }) {
  const max = Math.max(1, ...c.periods.map((p) => p.paid_minor));
  const change = c.previous_paid_minor > 0 ? (c.paid_minor - c.previous_paid_minor) / c.previous_paid_minor : null;
  const every = Math.max(1, Math.ceil(c.periods.length / 8));
  return (
    <Card title="Collected" description="Payments that went through, recurring or not, in this period.">
      <Stats cols={3}>
        <Stat label="Paid" value={money(c.paid_minor, r.currency)} sub={change == null ? "Nothing in the period before" : `${change >= 0 ? "Up" : "Down"} ${percent(Math.abs(change))} on the period before`} />
        <Stat label="Refunded" value={money(c.refunded_minor, r.currency)} tone={c.refunded_minor > 0 ? "amber" : undefined} sub="Refunds and chargebacks" />
        <Stat label="Kept" value={money(c.paid_minor - c.refunded_minor, r.currency)} sub="Paid less refunded" />
      </Stats>
      <div className="mt-5" role="img" aria-label="Payments collected in each period">
        <div className="flex h-28 items-end gap-[3px]">
          {c.periods.map((p) => (
            <div
              key={p.period}
              className="min-w-0 flex-1 rounded-t-sm bg-foreground/70"
              style={{ height: `${Math.max(p.paid_minor > 0 ? 2 : 0, (p.paid_minor / max) * 100)}%` }}
              title={`${periodLabel(p.period, r.group)}: ${money(p.paid_minor, r.currency)} paid${p.refunded_minor > 0 ? `, ${money(p.refunded_minor, r.currency)} refunded` : ""}`}
            />
          ))}
        </div>
        <div className="mt-1.5 flex gap-[3px] border-t pt-1.5 text-[11px] text-muted-foreground">
          {c.periods.map((p, i) => (
            <span key={p.period} className="min-w-0 flex-1 whitespace-nowrap">
              {i % every === 0 ? periodLabel(p.period, r.group) : ""}
            </span>
          ))}
        </div>
      </div>
    </Card>
  );
}
