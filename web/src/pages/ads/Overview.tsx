// Overview's tables under the number: the brand's own name judged apart
// from everything else, the actions with the biggest effect, and the three
// steps from ad click to first payment. They read the same campaign rows
// as the number above them, so the page can't disagree with itself.

import { DataTable } from "@/components/data-table";
import { TimeChart } from "@/components/time-chart";
import { Badge, Button, Card } from "../../ui";
import { money, n } from "../Ads";
import { targetState } from "./Hero";
import { evidenceLine, impactLine } from "./Inbox";
import { isAlert, isOpen, isShadow, leverWord, statusOf, type AdPerformanceDay, type AdPerformanceRow, type Finding } from "./api";

// Under five customers a cost per customer is noise, so it gets no colour.
const fewCustomers = 5;

export function Against({ cost, payers, target }: { cost: number | null; payers: number; target: number | null }) {
  if (cost === null || target === null) return <span className="text-muted-foreground">—</span>;
  if (payers < fewCustomers) return <Badge tone="grey" title={`Fewer than ${fewCustomers} customers, so the cost per customer can swing a lot.`}>Too few to judge</Badge>;
  const s = targetState(cost, target);
  return <Badge tone={s.tone === "red" ? "red" : s.tone === "green" ? "green" : "blue"}>{s.tone === "red" ? "Over" : s.tone === "green" ? "On target" : "Under"}</Badge>;
}

// The rows of the currency that spent the most: money is never added
// across currencies.
function mainCurrency(rows: AdPerformanceRow[]) {
  const by = new Map<string, number>();
  for (const r of rows) by.set(r.currency, (by.get(r.currency) ?? 0) + r.spend_micros);
  const top = [...by.entries()].sort((a, b) => b[1] - a[1])[0]?.[0];
  return { currency: top ?? "USD", rows: rows.filter((r) => r.currency === top) };
}

type Group = { id: string; name: string; campaigns: number; spend: number; signups: number; payers: number; cost: number | null; means: string };

// A campaign is for the brand's own name when its name says so: the name
// itself, or "brand" or "branded".
export const isBrandCampaign = (name: string, brandName: string) => {
  const low = name.toLowerCase();
  const brand = brandName.trim().toLowerCase();
  return (brand.length >= 3 && low.includes(brand)) || /\bbrand(ed)?\b/.test(low);
};

export function BrandSplit({ rows, brandName, target, open }: { rows: AdPerformanceRow[]; brandName: string; target: number | null; open: () => void }) {
  const main = mainCurrency(rows);
  const group = (id: string, name: string, own: boolean): Group => {
    const its = main.rows.filter((r) => isBrandCampaign(r.name, brandName) === own && (r.spend_micros > 0 || r.signups > 0 || r.new_payers > 0));
    const spend = its.reduce((a, r) => a + r.spend_micros, 0);
    const payers = its.reduce((a, r) => a + r.new_payers, 0);
    const cost = payers > 0 ? Math.round(spend / payers) : null;
    const state = payers >= fewCustomers ? targetState(cost, target).tone : undefined;
    const means = own
      ? "People who search for your name would often have found you anyway. Keep it funded so competitors can't take the top spot."
      : state === "red" ? "This is where growth comes from, and it costs too much today. Cut the waste first, then scale what is on target."
      : state ? "This is where growth comes from. It is within your target, so there is room to scale."
      : "This is where growth comes from. Too few customers yet to judge it.";
    return { id, name, campaigns: its.length, spend, signups: its.reduce((a, r) => a + r.signups, 0), payers, cost, means };
  };
  const groups = [group("brand", `Searches for ${brandName}`, true), group("rest", "Everything else", false)];
  // With no campaign for the brand's own name there is nothing to judge apart.
  if (groups.some((g) => g.campaigns === 0)) return null;
  return (
    <Card title={`Searches for ${brandName} against everything else`} description="Campaigns named for your brand are judged apart from the rest, so a cheap average doesn't hide an expensive campaign. Source: ad network spend, Outflare sign-ups and payments."
      action={<Button variant="ghost" onClick={open}>All campaigns</Button>}>
      <DataTable
        data={groups}
        rowId={(g) => g.id}
        columnsMenu={false}
        empty=""
        columns={[
          { id: "group", header: "Group", enableSorting: false, accessorFn: (g) => g.name, cell: ({ row: { original: g } }) => <div><div className="font-medium">{g.name}</div><div className="text-[12px] text-muted-foreground">{g.campaigns} {g.campaigns === 1 ? "campaign" : "campaigns"}</div></div> },
          { id: "spend", header: "Spend", enableSorting: false, meta: { align: "right" }, accessorFn: (g) => g.spend, cell: ({ row: { original: g } }) => <span className="font-mono tabular-nums">{money(g.spend, main.currency)}</span> },
          { id: "signups", header: "Sign-ups", enableSorting: false, meta: { align: "right" }, accessorFn: (g) => g.signups, cell: ({ row: { original: g } }) => <span className="font-mono tabular-nums">{n(g.signups)}</span> },
          { id: "payers", header: "New customers", enableSorting: false, meta: { align: "right" }, accessorFn: (g) => g.payers, cell: ({ row: { original: g } }) => <span className="font-mono tabular-nums">{n(g.payers)}</span> },
          { id: "cost", header: "Cost per customer", enableSorting: false, meta: { align: "right" }, accessorFn: (g) => g.cost ?? -1, cell: ({ row: { original: g } }) => <span className="font-mono tabular-nums">{money(g.cost, main.currency)}</span> },
          { id: "against", header: "Against target", enableSorting: false, cell: ({ row: { original: g } }) => <Against cost={g.cost} payers={g.payers} target={target} /> },
          { id: "means", header: "What it means", enableSorting: false, cell: ({ row: { original: g } }) => <div className="max-w-md whitespace-normal text-muted-foreground">{g.means}</div> },
        ]}
      />
    </Card>
  );
}

// The three open actions with the biggest expected effect. Approving is
// done on Actions, where each one shows what it changes.
export function NextActions({ findings, shadowMode, open }: { findings: Finding[]; shadowMode: boolean; open: () => void }) {
  const waiting = findings.filter((f) => !isShadow(f) && (isOpen(f) || (isAlert(f) && statusOf(f) === "open")));
  const unseen = findings.filter(isShadow).length;
  if (waiting.length === 0) {
    if (!shadowMode || unseen === 0) return null;
    return (
      <Card title="Do this next" description="Recommendations are switched off, so nothing is listed for you yet.">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <p className="text-[13px]">Outflare has {n(unseen)} {unseen === 1 ? "recommendation" : "recommendations"} it is keeping to itself. Switch recommendations on to see them. Nothing changes in your ad accounts until you approve one.</p>
          <Button onClick={open}>See them on Actions</Button>
        </div>
      </Card>
    );
  }
  const top = [...waiting].sort((a, b) => Math.abs(b.impact_micros ?? 0) - Math.abs(a.impact_micros ?? 0)).slice(0, 3);
  return (
    <Card title="Do this next" description={`The ${top.length === 1 ? "action" : `${top.length} actions`} with the biggest effect. Each waits for your OK.`}
      action={<Button variant="ghost" onClick={open}>All {waiting.length} actions</Button>}>
      <DataTable
        data={top}
        rowId={(f) => f.id}
        columnsMenu={false}
        onRowClick={open}
        empty=""
        columns={[
          { id: "action", header: "Action", enableSorting: false, accessorFn: (f) => f.reason, cell: ({ row: { original: f } }) => <div className="max-w-sm whitespace-normal"><div className="font-medium">{f.reason || leverWord(f.kind)}</div><div className="text-[12px] text-muted-foreground">{f.campaign_name}</div></div> },
          { id: "why", header: "Why", enableSorting: false, cell: ({ row: { original: f } }) => <div className="max-w-md whitespace-normal text-muted-foreground">{evidenceLine(f) || "—"}</div> },
          { id: "effect", header: "Expected effect", enableSorting: false, cell: ({ row: { original: f } }) => <span className="whitespace-normal">{impactLine(f)?.replace(/^Expected: /, "") ?? "—"}</span> },
          { id: "go", header: "", enableSorting: false, meta: { align: "right" }, cell: () => <Button variant="ghost" onClick={open}>Review</Button> },
        ]}
      />
    </Card>
  );
}

// From ad click to first payment, as three bars on one scale.
export function Funnel({ rows }: { rows: AdPerformanceRow[] }) {
  const main = mainCurrency(rows);
  const spend = main.rows.reduce((a, r) => a + r.spend_micros, 0);
  const clicks = main.rows.reduce((a, r) => a + r.clicks, 0);
  const signups = main.rows.reduce((a, r) => a + r.signups, 0);
  const payers = main.rows.reduce((a, r) => a + r.new_payers, 0);
  const claimed = main.rows.reduce((a, r) => a + (r.platform_conversions ?? 0), 0);
  if (clicks === 0 && signups === 0 && payers === 0) return null;
  const share = (part: number, of: number) => (of > 0 ? `${((part / of) * 100).toFixed(1)}%` : "—");
  const each = (count: number) => (count > 0 ? `${money(Math.round(spend / count), main.currency)} each` : "");
  const steps = [
    { label: "Ad clicks", value: clicks, sub: `${money(spend, main.currency)} spent` },
    { label: "Sign-ups", value: signups, sub: [`${share(signups, clicks)} of clicks`, each(signups)].filter(Boolean).join(" · ") },
    { label: "First payment", value: payers, sub: [`${share(payers, signups)} of sign-ups`, each(payers)].filter(Boolean).join(" · ") },
  ];
  const max = Math.max(clicks, signups, payers, 1);
  return (
    <Card title="From ad click to first payment" description="Source: ad network clicks, Outflare sign-ups and payments.">
      <ol className="space-y-3" data-testid="ads-funnel">
        {steps.map((s) => (
          <li key={s.label} className="grid items-center gap-x-3 gap-y-1 sm:grid-cols-[11rem_1fr_5rem]">
            <div>
              <div className="text-[13px] font-medium">{s.label}</div>
              <div className="text-[12px] text-muted-foreground">{s.sub}</div>
            </div>
            <div className="h-6 rounded bg-muted" aria-hidden="true">
              <div className="h-6 rounded bg-blue-600/80 dark:bg-blue-500/80" style={{ width: `${Math.max((s.value / max) * 100, s.value > 0 ? 1 : 0)}%` }} />
            </div>
            <div className="font-mono text-lg tabular-nums sm:text-right">{n(s.value)}</div>
          </li>
        ))}
      </ol>
      {claimed > 0 && <p className="mt-3 text-[12px] text-muted-foreground">The ad network's own count for the same clicks is {n(claimed)} conversions. It counts a different action, so the two never match exactly.</p>}
    </Card>
  );
}

// Cost per new customer against the target, day by day. A single day has
// too few customers to mean anything, so each point is the spend over the
// customers of the seven days up to it.
const smooth = 7;

export function CostChart({ daily, previous = [], from, to, target }: { daily: AdPerformanceDay[]; previous?: AdPerformanceDay[]; from: string; to: string; target: number | null }) {
  const by = new Map<string, number>();
  for (const d of daily) by.set(d.currency, (by.get(d.currency) ?? 0) + d.spend_micros);
  const currency = [...by.entries()].sort((a, b) => b[1] - a[1])[0]?.[0];
  const starts: string[] = [];
  for (let t = Date.parse(`${from}T00:00:00Z`); t <= Date.parse(`${to}T00:00:00Z`); t += 86400000) starts.push(new Date(t).toISOString().slice(0, 10));
  if (!currency || starts.length <= smooth) return null;
  const line = (list: AdPerformanceDay[]) => {
    const days = new Map(list.filter((d) => d.currency === currency).map((d) => [d.day, d]));
    return starts.map((_, i) => {
      let spend = 0, payers = 0;
      for (const day of starts.slice(Math.max(0, i - smooth + 1), i + 1)) {
        spend += days.get(day)?.spend_micros ?? 0;
        payers += days.get(day)?.new_payers ?? 0;
      }
      // In whole currency, so the axis reads 20, 40, 60.
      return payers > 0 ? spend / payers / 1e6 : null;
    });
  };
  const cost = line(daily);
  // The period it is compared with, drawn on the same days.
  const before = line(previous);
  if (cost.every((v) => v === null)) return null;
  const series = [{ label: "Cost per new customer", values: cost, currency }];
  if (before.some((v) => v !== null)) series.push({ label: "The period before", values: before, currency });
  if (target !== null) series.push({ label: "Your target", values: starts.map(() => target / 1e6), currency });
  return (
    <Card title="Cost per new customer against your target" description={`Each point is the spend over the new customers of the ${smooth} days up to it, so one quiet day doesn't swing the line. Under the target line is good. Source: ad network spend, Outflare payments.`}>
      <div data-testid="ads-cost-chart">
        <TimeChart starts={starts} series={series} format={(v, c) => money(v == null ? null : Math.round(v * 1e6), c ?? currency)} label="Cost per new customer against the target, by day" />
      </div>
    </Card>
  );
}
