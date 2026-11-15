// Commissions (docs/sales-team.md, "Commission"): what each person earns
// in a month under the brand's commission plans, line by line with the
// rule that paid it, and the plans themselves: ordered lists of rules in
// plain words. Outflare works commission out; it never pays anyone.

import { useCallback, useEffect, useState } from "react";
import type { CommissionPlan, CommissionRule, CommissionStatement, RuleCondition } from "@outflare/sdk";
import { track } from "@/lib/analytics";
import { api, errorMessage } from "../api";
import { plural } from "../format";
import { Badge, Button, Card, confirmAction, Empty, ErrorText, field, Loading, PageHeader, Stat, Stats, toast } from "../ui";
import { ops, readValue } from "./Scores";
import { money, roleLabel } from "./SalesTeam";
import type { Tab } from "../App";
import { Download } from "lucide-react";
import { saveFile } from "./PartnerProof";

type Person = { id: string; name: string; title: string; status: string };
type Statements = { month: string; currency: string; total_minor: number; statements: CommissionStatement[]; can_manage: boolean };
type Draft = { id?: string; name: string; status: CommissionPlan["status"]; positions: string[]; staff_ids: string[]; lapse_days: number; rules: CommissionRule[] };

// This month and the eleven before it, newest first.
function months(): string[] {
  const now = new Date();
  const out: string[] = [];
  for (let i = 0; i < 12; i++) {
    const d = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth() - i, 1));
    out.push(`${d.getUTCFullYear()}-${String(d.getUTCMonth() + 1).padStart(2, "0")}`);
  }
  return out;
}
const monthLabel = (m: string) => new Date(`${m}-01T00:00:00Z`).toLocaleDateString(undefined, { month: "long", year: "numeric", timeZone: "UTC" });
const day = (iso: string) => new Date(`${iso}T00:00:00Z`).toLocaleDateString(undefined, { month: "short", day: "numeric", timeZone: "UTC" });

const paysOn: [CommissionRule["pays_on"], string][] = [
  ["deal_won", "any deal won"],
  ["new_business", "a deal won with a new customer"],
  ["expansion", "a deal won with someone who already pays"],
  ["renewal", "a renewal won"],
  ["payment", "money collected"],
];
const whoGets: [CommissionRule["who"], string][] = [
  ["owner", "the owner"],
  ["company_owner", "the company's owner"],
  ["sourced_by", "whoever sourced the deal"],
  ["manager", "the owner's manager"],
];
const whoShort: Record<string, string> = { owner: "as the owner", company_owner: "as the company's owner", sourced_by: "for sourcing it", manager: "as the owner's manager" };
const statusTone = { draft: "grey", active: "green", archived: "grey" } as const;
const statementTone = { open: "grey", approved: "blue", paid: "green" } as const;
const pct = (n: number) => `${Number.isInteger(n) ? n : n.toFixed(2).replace(/0+$/, "")}%`;

// A rule in one sentence: "10% of money collected in the customer's first 30 days, to the owner".
function ruleWords(r: CommissionRule, currency: string): string {
  const what = paysOn.find(([k]) => k === r.pays_on)?.[1] ?? r.pays_on;
  const amount = r.flat_minor ? `${money(r.flat_minor, currency)} for` : `${pct(r.percent ?? 0)} of`;
  const bits: string[] = [];
  if (r.source) bits.push(r.source);
  if (r.customer) bits.push(r.customer === "new" ? "new customers" : "customers who came back");
  if (r.days_from != null || r.days_to != null) {
    bits.push(r.days_from == null || r.days_from === 0 ? `in the customer's first ${r.days_to} days` : r.days_to == null ? `from day ${r.days_from}` : `on days ${r.days_from} to ${r.days_to}`);
  }
  if (r.conditions?.length) bits.push(plural(r.conditions.length, "more condition"));
  let s = `${amount} ${what}${bits.length ? ` (${bits.join(", ")})` : ""}, to ${whoGets.find(([k]) => k === r.who)?.[1] ?? r.who}`;
  if (r.tiers?.length) s += `; ${r.tiers.map((t) => `${pct(t.percent)} from ${t.from_attainment}% of quota`).join(", ")}`;
  if (r.cap_minor != null) s += `; at most ${money(r.cap_minor, currency)} a month`;
  if (r.clawback) s += "; taken back on a refund";
  return s;
}

function coversWords(p: { positions: string[]; staff_ids: string[] }, people: Person[]): string {
  const names = [
    ...p.positions.map((x) => (x.startsWith("role:") ? `every ${(roleLabel[x.slice(5)] ?? x.slice(5)).toLowerCase()}` : `everyone titled ${x}`)),
    ...p.staff_ids.map((id) => people.find((x) => x.id === id)?.name ?? "someone who left"),
  ];
  return names.length ? names.join(", ") : "Nobody yet";
}

const slug = (name: string, taken: Set<string>) => {
  const base = name.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 30) || "rule";
  let id = base;
  for (let n = 2; taken.has(id); n++) id = `${base}-${n}`;
  return id;
};

// Ready-made plans a new plan can start from.
const starters: { label: string; about: string; rules: CommissionRule[] }[] = [
  {
    label: "A percent of every deal",
    about: "10% of every deal won, to its owner.",
    rules: [{ id: "every-deal", name: "Every deal", pays_on: "deal_won", who: "owner", percent: 10 }],
  },
  {
    label: "Money collected, more in the first month",
    about: "10% of what a customer pays in their first 30 days, then 2% up to day 100. Refunds take it back.",
    rules: [
      { id: "first-30-days", name: "First 30 days", pays_on: "payment", who: "owner", percent: 10, days_to: 30, clawback: true },
      { id: "days-31-to-100", name: "Days 31 to 100", pays_on: "payment", who: "owner", percent: 2, days_from: 31, days_to: 100, clawback: true },
    ],
  },
  {
    label: "Outbound pays more than inbound",
    about: "12% of outbound deals, 6% of inbound ones, and 8% when a customer who had left comes back.",
    rules: [
      { id: "won-back", name: "Won back", pays_on: "deal_won", who: "owner", percent: 8, customer: "reactivated" },
      { id: "outbound", name: "Outbound deals", pays_on: "deal_won", who: "owner", percent: 12, source: "outbound" },
      { id: "inbound", name: "Inbound deals", pays_on: "deal_won", who: "owner", percent: 6, source: "inbound" },
    ],
  },
  {
    label: "Faster once quota is reached",
    about: "8% of every deal, 12% from 100% of quota, plus 2% to the owner's manager.",
    rules: [
      { id: "every-deal", name: "Every deal", pays_on: "deal_won", who: "owner", percent: 8, tiers: [{ from_attainment: 100, percent: 12 }] },
      { id: "manager-override", name: "Manager's share", pays_on: "deal_won", who: "manager", percent: 2, stack: true },
    ],
  },
];

export function Commissions({ ws, brandId, go, onBack }: { ws: string; brandId: string; go: (tab: Tab, item?: string) => void; onBack: () => void }) {
  const list = months();
  const [month, setMonth] = useState(list[0]);
  const [view, setView] = useState<"statements" | "plans">("statements");
  const [s, setS] = useState<Statements | null>(null);
  const [plans, setPlans] = useState<CommissionPlan[] | null>(null);
  const [people, setPeople] = useState<Person[]>([]);
  const [error, setError] = useState("");
  const [open, setOpen] = useState<string | null>(null); // the statement whose lines show
  const [draft, setDraft] = useState<Draft | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    const [st, pl] = await Promise.all([
      api.GET("/v1/brands/{brand}/commissions", { params: { path: { brand: brandId }, query: { month } } }),
      api.GET("/v1/brands/{brand}/commission-plans", { params: { path: { brand: brandId } } }),
    ]);
    if (st.error || !st.data) return setError(errorMessage(st.error));
    setError("");
    setS(st.data as Statements);
    setPlans(pl.data?.data ?? []);
  }, [brandId, month]);
  useEffect(() => {
    setS(null);
    void load();
  }, [load]);
  useEffect(() => {
    void api.GET("/v1/workspaces/{ws}/staff", { params: { path: { ws } } }).then(({ data }) => setPeople((data?.data ?? []) as Person[]));
  }, [ws]);

  async function setStatus(st: CommissionStatement, status: "open" | "approved" | "paid") {
    if (busy) return;
    if (status === "open" && !(await confirmAction(`Reopen ${st.name}'s ${monthLabel(month)}?`, "The amounts that were approved are dropped, and the month is worked out again from the deals, payments and rules as they are now.", "Reopen", false))) return;
    setBusy(true);
    try {
      const { data, error } = await api.PUT("/v1/brands/{brand}/commissions/{staff}", { params: { path: { brand: brandId, staff: st.staff_id } }, body: { month, status } });
      if (error || !data) return toast(errorMessage(error), "error");
      if ("staff_id" in data) {
        toast(status === "approved" ? `${st.name}'s month is approved.` : status === "paid" ? "Marked as paid." : "Reopened.");
        track("ui.commission_statement_changed", { status });
        await load();
      } else toast("Sent for approval.");
    } finally {
      setBusy(false);
    }
  }

  async function savePlan(d: Draft) {
    if (busy) return;
    setBusy(true);
    try {
      const body = { name: d.name.trim(), status: d.status, positions: d.positions, staff_ids: d.staff_ids, lapse_days: d.lapse_days, rules: d.rules };
      const res = d.id
        ? await api.PATCH("/v1/brands/{brand}/commission-plans/{id}", { params: { path: { brand: brandId, id: d.id } }, body })
        : await api.POST("/v1/brands/{brand}/commission-plans", { params: { path: { brand: brandId } }, body });
      if (res.error || !res.data) return toast(errorMessage(res.error), "error");
      if ("id" in res.data) {
        toast(d.status === "active" ? "Plan saved. It is paying from now." : "Plan saved as a draft. Turn it on when it is ready.");
        track("ui.commission_plan_saved", { rules: d.rules.length, status: d.status, is_new: !d.id });
        setDraft(null);
        await load();
      } else toast("Sent for approval.");
    } finally {
      setBusy(false);
    }
  }

  async function removePlan(p: CommissionPlan) {
    if (!(await confirmAction(`Delete ${p.name}?`, "Months already approved keep their amounts. Open months stop counting this plan.", "Delete"))) return;
    const { error } = await api.DELETE("/v1/brands/{brand}/commission-plans/{id}", { params: { path: { brand: brandId, id: p.id } } });
    if (error) return toast(errorMessage(error), "error");
    toast("Plan deleted.");
    await load();
  }

  // The month's lines as a file, one row a line, for payroll.
  async function download() {
    const { data, error, response } = await api.GET("/v1/brands/{brand}/commissions", { params: { path: { brand: brandId }, query: { month, format: "csv" } }, parseAs: "text" });
    if (error || typeof data !== "string") return toast(errorMessage(error), "error");
    saveFile(data, response.headers.get("Content-Type") ?? "text/csv", `commission-${month}.csv`);
    track("ui.commission_statement_downloaded", { lines: s?.statements.reduce((n, x) => n + x.lines.length, 0) ?? 0 });
  }

  if (draft) return <PlanEditor draft={draft} people={people} currency={s?.currency ?? "USD"} busy={busy} onChange={setDraft} onSave={() => void savePlan(draft)} onCancel={() => setDraft(null)} />;

  const header = (
    <PageHeader
      title="Commissions"
      back={{ label: "Revenue", onClick: onBack }}
      description="What each person earns, worked out from your deals and payments by rules you write. Every line shows which rule paid it. Outflare never pays anyone: you approve the month, pay it your way, and mark it paid."
      action={
        <div role="tablist" aria-label="View" className="flex gap-1">
          {(["statements", "plans"] as const).map((v) => (
            <Button key={v} role="tab" aria-selected={view === v} variant="ghost" className={view === v ? "bg-muted" : "border-transparent text-muted-foreground"} onClick={() => setView(v)}>
              {v === "statements" ? "Earnings" : `Plans${plans ? ` (${plans.length})` : ""}`}
            </Button>
          ))}
        </div>
      }
    />
  );
  if (error) return <div className="space-y-6">{header}<ErrorText>{error}</ErrorText></div>;
  if (!s || !plans) return <div className="space-y-6">{header}<Loading /></div>;
  const ccy = s.currency;
  const blank = (rules: CommissionRule[] = []): Draft => ({ name: "", status: "draft", positions: [], staff_ids: [], lapse_days: 90, rules });
  const activePlans = plans.filter((p) => p.status === "active").length;

  if (view === "plans") {
    return (
      <div className="space-y-6">
        {header}
        {plans.length === 0 ? (
          <Empty title="No commission plans yet" action={s.can_manage && <Button onClick={() => setDraft(blank())}>New plan</Button>}>
            A plan says who it covers and holds a list of rules, such as 10% of what a customer pays in their first 30 days and 2% after that. {s.can_manage ? "Start from an example and change it to fit." : "Someone who manages the workspace sets them up."}
          </Empty>
        ) : (
          <>
            {s.can_manage && <div className="flex justify-end"><Button onClick={() => setDraft(blank())}>New plan</Button></div>}
            {plans.map((p) => (
              <Card
                key={p.id}
                title={<span className="flex flex-wrap items-center gap-2">{p.name} <Badge tone={statusTone[p.status]}>{p.status === "active" ? "Paying" : p.status === "draft" ? "Draft" : "Archived"}</Badge></span>}
                description={`For ${coversWords(p, people)}`}
                action={
                  s.can_manage && (
                    <div className="flex gap-2">
                      <Button variant="ghost" onClick={() => void removePlan(p)}>Delete</Button>
                      <Button variant="ghost" onClick={() => setDraft({ id: p.id, name: p.name, status: p.status, positions: p.positions, staff_ids: p.staff_ids, lapse_days: p.lapse_days, rules: p.rules })}>Edit</Button>
                    </div>
                  )
                }
              >
                {p.rules.length === 0 ? (
                  <p className="text-[13px] text-muted-foreground">No rules yet, so it pays nothing.</p>
                ) : (
                  <ol className="space-y-1.5 text-[13px]">
                    {p.rules.map((r, i) => (
                      <li key={r.id} className="flex gap-2">
                        <span className="w-5 shrink-0 text-right tabular-nums text-muted-foreground">{i + 1}.</span>
                        <span><span className="font-medium">{r.name}:</span> {ruleWords(r, ccy)}{r.stack ? " (on top of the rules above)" : ""}</span>
                      </li>
                    ))}
                  </ol>
                )}
              </Card>
            ))}
            <p className="text-[12px] text-muted-foreground">For each deal or payment, the first rule from the top that applies pays, plus any rule marked "on top".</p>
          </>
        )}
      </div>
    );
  }

  return (
    <div className="space-y-6">
      {header}
      <div className="flex flex-wrap items-center gap-3">
        <label className="flex items-center gap-2 text-[13px] text-muted-foreground">
          Month
          <select aria-label="Month" className={field} value={month} onChange={(e) => { setMonth(e.target.value); setOpen(null); track("ui.commission_month_changed", { back: list.indexOf(e.target.value) }); }}>
            {list.map((m) => <option key={m} value={m}>{monthLabel(m)}</option>)}
          </select>
        </label>
        {s.statements.some((x) => x.lines.length > 0) && (
          <Button variant="ghost" className="ml-auto" disabled={busy} onClick={() => void download()}>
            <Download className="size-3.5" aria-hidden />
            Download {monthLabel(month)} (CSV)
          </Button>
        )}
      </div>
      <Stats cols={3}>
        <Stat label={s.can_manage ? "Commission earned" : "You and your team earned"} value={money(s.total_minor, ccy)} sub={monthLabel(month)} />
        <Stat label="People" value={s.statements.length} sub={`${s.statements.filter((x) => x.status !== "open").length} approved`} />
        <Stat label="Still to approve" value={money(s.statements.filter((x) => x.status === "open").reduce((n, x) => n + x.total_minor, 0), ccy)} sub="Open months follow your deals and rules as they change" />
      </Stats>

      {s.statements.length === 0 ? (
        <Empty
          title={activePlans === 0 ? "No plan is paying yet" : "Nobody is covered by a plan"}
          action={s.can_manage && <Button onClick={() => setView("plans")}>{plans.length ? "Open the plans" : "Set up a plan"}</Button>}
        >
          {activePlans === 0
            ? "Commission is worked out from commission plans. Add one, say who it covers, and turn it on."
            : "Your plans are on, but they name no job title or person that is in the team directory. Open a plan and choose who it is for."}
        </Empty>
      ) : (
        <div className="space-y-3">
          {s.statements.map((st) => {
            const shown = open === st.staff_id;
            return (
              <section key={st.staff_id} className="rounded-lg border">
                <div className="flex flex-wrap items-center gap-x-4 gap-y-2 px-4 py-3">
                  <button className="min-w-[10rem] flex-1 text-left" aria-expanded={shown} onClick={() => { setOpen(shown ? null : st.staff_id); if (!shown) track("ui.commission_statement_opened", { lines: st.lines.length }); }}>
                    <div className="flex flex-wrap items-center gap-2 text-sm font-medium">
                      {st.name} <Badge tone={statementTone[st.status]}>{st.status === "open" ? "Not approved yet" : st.status === "approved" ? "Approved" : "Paid"}</Badge>
                    </div>
                    <div className="text-[12px] text-muted-foreground">
                      {st.title || "No job title"} · {plural(st.lines.length, "line")}{st.attainment != null ? ` · ${Math.round(st.attainment)}% of quota` : ""} · <span className="text-foreground underline underline-offset-2">{shown ? "Hide the lines" : "Show how it adds up"}</span>
                    </div>
                  </button>
                  <div className="text-lg font-semibold tabular-nums">{money(st.total_minor, ccy)}</div>
                  {s.can_manage && (
                    <div className="flex gap-2">
                      {st.status === "open" && <Button disabled={busy} onClick={() => void setStatus(st, "approved")}>Approve</Button>}
                      {st.status === "approved" && <Button disabled={busy} onClick={() => void setStatus(st, "paid")}>Mark as paid</Button>}
                      {st.status !== "open" && <Button variant="ghost" disabled={busy} onClick={() => void setStatus(st, "open")}>Reopen</Button>}
                    </div>
                  )}
                </div>
                {shown && (
                  <div className="border-t">
                    {st.lines.length === 0 ? (
                      <p className="px-4 py-3 text-[13px] text-muted-foreground">Nothing this month: no deal or payment matched a rule of their plan.</p>
                    ) : (
                      <ul className="divide-y">
                        {st.lines.map((l, i) => (
                          <li key={i} className="flex flex-wrap items-baseline gap-x-4 gap-y-1 px-4 py-2.5 text-[13px]">
                            <span className="w-14 shrink-0 text-muted-foreground">{day(l.event.on)}</span>
                            <span className="min-w-[12rem] flex-1">
                              <span className="font-medium">{l.event.name}</span>
                              {l.event.record_name && l.event.record_name !== l.event.name ? <span className="text-muted-foreground"> · {l.event.record_name}</span> : null}
                              <span className="block text-[12px] text-muted-foreground">
                                {l.event.kind === "deal" ? "Deal won" : l.event.kind === "refund" ? "Refund" : "Payment"} of {money(l.event.amount_minor, ccy)}
                                {l.event.source ? `, ${l.event.source}` : ""}
                                {l.event.customer === "reactivated" ? ", came back" : l.event.customer === "new" ? ", new customer" : ""}
                                {l.event.customer_days != null ? `, day ${l.event.customer_days}` : ""}
                              </span>
                            </span>
                            <span className="min-w-[12rem] flex-1 text-muted-foreground">
                              <span className="text-foreground">{l.rule_name}</span> {whoShort[l.who]}
                              <span className="block text-[12px]">
                                {l.percent != null ? pct(l.percent) : "A flat amount"}
                                {l.tier_from != null ? `, the rate from ${l.tier_from}% of quota` : ""}
                                {l.split_percent != null ? `, on their ${pct(l.split_percent)} of a shared deal` : ""}
                                {l.capped ? ", cut at the monthly cap" : ""} · {l.plan_name}
                              </span>
                            </span>
                            <span className={`w-24 shrink-0 text-right font-medium tabular-nums ${l.amount_minor < 0 ? "text-red-600 dark:text-red-400" : ""}`}>{money(l.amount_minor, ccy)}</span>
                          </li>
                        ))}
                      </ul>
                    )}
                  </div>
                )}
              </section>
            );
          })}
        </div>
      )}
      <p className="text-[12px] text-muted-foreground">
        Amounts are in {ccy}. An approved month keeps its lines even when a deal, a payment or a rule changes later; reopen it to work it out again.
        {" "}<button className="underline underline-offset-2" onClick={() => go("revenue", "team")}>See the sales team's quotas</button>.
      </p>
    </div>
  );
}

// ---- The plan editor: a full page ----

function PlanEditor({ draft: d, people, currency, busy, onChange, onSave, onCancel }: { draft: Draft; people: Person[]; currency: string; busy: boolean; onChange: (d: Draft) => void; onSave: () => void; onCancel: () => void }) {
  const titles = [...new Set(people.filter((p) => p.status !== "left" && p.title.trim()).map((p) => p.title.trim()))].sort();
  const setRule = (i: number, r: CommissionRule) => onChange({ ...d, rules: d.rules.map((x, j) => (j === i ? r : x)) });
  const move = (i: number, by: number) => {
    const rules = [...d.rules];
    const [r] = rules.splice(i, 1);
    rules.splice(i + by, 0, r);
    onChange({ ...d, rules });
  };
  const addRule = () => {
    const taken = new Set(d.rules.map((r) => r.id));
    onChange({ ...d, rules: [...d.rules, { id: slug(`rule ${d.rules.length + 1}`, taken), name: "", pays_on: "deal_won", who: "owner", percent: 10 }] });
  };
  const covers = d.positions.length + d.staff_ids.length;
  const problem = !d.name.trim()
    ? "Give the plan a name."
    : d.rules.some((r) => !r.name.trim())
      ? "Give every rule a name: it shows on each line it pays."
      : d.rules.some((r) => !r.flat_minor && !(r.percent ?? 0) && !(r.tiers?.length))
        ? "Every rule needs a percent or an amount."
        : d.status === "active" && covers === 0
          ? "Choose who the plan is for before turning it on."
          : "";

  return (
    <div className="space-y-6">
      <PageHeader
        title={d.id ? `Edit ${d.name || "plan"}` : "New commission plan"}
        back={{ label: "Commissions", onClick: onCancel }}
        description="Say who the plan is for, then list its rules. For each deal or payment, the first rule from the top that applies pays."
        action={
          <div className="flex gap-2">
            <Button variant="ghost" onClick={onCancel}>Cancel</Button>
            <Button disabled={busy || !!problem} title={problem} onClick={onSave}>Save plan</Button>
          </div>
        }
      />

      <Card title="The plan">
        <div className="grid gap-4 sm:grid-cols-2">
          <label className="block text-[13px]">
            <span className="mb-1 block font-medium">Name</span>
            <input className={`${field} w-full`} placeholder="Account executives 2026" value={d.name} maxLength={80} onChange={(e) => onChange({ ...d, name: e.target.value })} />
          </label>
          <label className="block text-[13px]">
            <span className="mb-1 block font-medium">Status</span>
            <select className={`${field} w-full`} value={d.status} onChange={(e) => onChange({ ...d, status: e.target.value as Draft["status"] })}>
              <option value="draft">Draft: pays nothing yet</option>
              <option value="active">On: pays from now</option>
              <option value="archived">Archived: kept, pays nothing</option>
            </select>
          </label>
        </div>
      </Card>

      <Card title="Who it is for" description="Pick job titles, roles on the sales desk, or people by name. Someone new with that title is covered by themselves.">
        <div className="space-y-3">
          <div className="flex flex-wrap gap-2">
            {d.positions.map((x) => (
              <button key={x} className="rounded-full border px-2.5 py-1 text-[12px] hover:bg-muted" title="Remove" onClick={() => onChange({ ...d, positions: d.positions.filter((y) => y !== x) })}>
                {x.startsWith("role:") ? `Every ${(roleLabel[x.slice(5)] ?? x.slice(5)).toLowerCase()}` : `Everyone titled ${x}`} ×
              </button>
            ))}
            {d.staff_ids.map((id) => (
              <button key={id} className="rounded-full border px-2.5 py-1 text-[12px] hover:bg-muted" title="Remove" onClick={() => onChange({ ...d, staff_ids: d.staff_ids.filter((y) => y !== id) })}>
                {people.find((p) => p.id === id)?.name ?? "Someone who left"} ×
              </button>
            ))}
            {covers === 0 && <span className="text-[13px] text-muted-foreground">Nobody yet.</span>}
          </div>
          <div className="flex flex-wrap gap-2">
            <select aria-label="Add a job title or role" className={field} value="" onChange={(e) => e.target.value && onChange({ ...d, positions: [...d.positions, e.target.value] })}>
              <option value="">Add a job title or role…</option>
              <optgroup label="Roles on the sales desk">
                {Object.entries(roleLabel).filter(([k]) => !d.positions.includes(`role:${k}`)).map(([k, v]) => <option key={k} value={`role:${k}`}>Every {v.toLowerCase()}</option>)}
              </optgroup>
              {titles.length > 0 && (
                <optgroup label="Job titles in your team">
                  {titles.filter((x) => !d.positions.some((y) => y.toLowerCase() === x.toLowerCase())).map((x) => <option key={x} value={x}>{x}</option>)}
                </optgroup>
              )}
            </select>
            <select aria-label="Add a person" className={field} value="" onChange={(e) => e.target.value && onChange({ ...d, staff_ids: [...d.staff_ids, e.target.value] })}>
              <option value="">Add a person…</option>
              {people.filter((p) => p.status !== "left" && !d.staff_ids.includes(p.id)).map((p) => <option key={p.id} value={p.id}>{p.name}{p.title ? `, ${p.title}` : ""}</option>)}
            </select>
          </div>
        </div>
      </Card>

      <Card
        title="Rules"
        description="Tried from the top for each deal won and each payment. Put the special cases first and the general rule last."
        action={<Button variant="ghost" onClick={addRule} disabled={d.rules.length >= 30}>Add a rule</Button>}
      >
        {d.rules.length === 0 ? (
          <div className="space-y-3">
            <p className="text-[13px] text-muted-foreground">Start from an example and change it, or add a rule of your own.</p>
            <div className="grid gap-2 sm:grid-cols-2">
              {starters.map((x) => (
                <button key={x.label} className="rounded-lg border p-3 text-left hover:bg-muted" onClick={() => { onChange({ ...d, rules: x.rules.map((r) => ({ ...r })) }); track("ui.commission_starter_picked", { rules: x.rules.length }); }}>
                  <span className="block text-sm font-medium">{x.label}</span>
                  <span className="block text-[12px] text-muted-foreground">{x.about}</span>
                </button>
              ))}
            </div>
          </div>
        ) : (
          <ol className="space-y-3">
            {d.rules.map((r, i) => (
              <li key={i}>
                <RuleEditor
                  rule={r}
                  n={i + 1}
                  currency={currency}
                  onChange={(n) => setRule(i, n)}
                  onRemove={() => onChange({ ...d, rules: d.rules.filter((_, j) => j !== i) })}
                  onUp={i > 0 ? () => move(i, -1) : undefined}
                  onDown={i < d.rules.length - 1 ? () => move(i, 1) : undefined}
                />
              </li>
            ))}
          </ol>
        )}
      </Card>

      <Card title="New or coming back" description="For rules about new and returning customers, and for counting days since a customer started paying.">
        <label className="flex flex-wrap items-center gap-2 text-[13px]">
          A customer who pays again after
          <input aria-label="Days without a payment" className={`${field} w-20 text-right`} inputMode="numeric" value={d.lapse_days} onChange={(e) => onChange({ ...d, lapse_days: Math.max(1, Math.min(1000, Number(e.target.value.replace(/\D/g, "")) || 1)) })} />
          days without a payment counts as coming back, and their days start again from 0.
        </label>
        <p className="mt-2 text-[12px] text-muted-foreground">90 suits monthly billing. If customers pay once a year, use 400 so a yearly renewal isn't mistaken for someone coming back.</p>
      </Card>

      {problem && <p className="text-[13px] text-amber-700 dark:text-amber-400">{problem}</p>}
      <div className="flex justify-end gap-2">
        <Button variant="ghost" onClick={onCancel}>Cancel</Button>
        <Button disabled={busy || !!problem} onClick={onSave}>Save plan</Button>
      </div>
    </div>
  );
}

const num = (v: string) => {
  const n = Number(v.replace(/[\s,]/g, ""));
  return v.trim() === "" || !Number.isFinite(n) || n < 0 ? undefined : n;
};

function RuleEditor({ rule: r, n, currency, onChange, onRemove, onUp, onDown }: { rule: CommissionRule; n: number; currency: string; onChange: (r: CommissionRule) => void; onRemove: () => void; onUp?: () => void; onDown?: () => void }) {
  const payment = r.pays_on === "payment";
  const flat = r.flat_minor != null && r.flat_minor > 0;
  const windowOn = r.days_from != null || r.days_to != null;
  const conditions = r.conditions ?? [];
  const tiers = r.tiers ?? [];
  const [more, setMore] = useState(conditions.length > 0 || tiers.length > 0 || r.cap_minor != null || !!r.stack);
  const set = (patch: Partial<CommissionRule>) => onChange({ ...r, ...patch });
  // Changing what a rule pays on drops the parts that no longer fit.
  const setPaysOn = (v: CommissionRule["pays_on"]) => {
    const next: CommissionRule = { ...r, pays_on: v };
    if (v !== "payment") {
      delete next.days_from;
      delete next.days_to;
      delete next.clawback;
    } else if (next.who === "sourced_by") next.who = "owner";
    onChange(next);
  };
  const setC = (k: number, c: RuleCondition) => set({ conditions: conditions.map((y, j) => (j === k ? c : y)) });
  const line = "flex flex-wrap items-center gap-2 text-[13px] [&_select]:max-w-full";

  return (
    <div className="space-y-3 rounded-lg border p-3">
      <div className="flex flex-wrap items-center gap-2">
        <span className="w-6 text-center text-[13px] tabular-nums text-muted-foreground">{n}</span>
        <input aria-label="Rule name" className={`${field} min-w-[10rem] flex-1 font-medium`} placeholder="Name this rule, such as First 30 days" maxLength={80} value={r.name} onChange={(e) => set({ name: e.target.value })} />
        <div className="flex gap-1">
          <Button variant="ghost" disabled={!onUp} aria-label="Move up" onClick={onUp}>↑</Button>
          <Button variant="ghost" disabled={!onDown} aria-label="Move down" onClick={onDown}>↓</Button>
          <Button variant="ghost" onClick={onRemove}>Remove</Button>
        </div>
      </div>

      <p className="pl-8 text-[12px] text-muted-foreground">In short: {ruleWords(r, currency)}.</p>

      <div className={line}>
        <span className="text-muted-foreground">Pay</span>
        <select aria-label="Percent or amount" className={field} value={flat ? "flat" : "percent"} onChange={(e) => (e.target.value === "flat" ? set({ flat_minor: 10000, percent: undefined, tiers: undefined }) : set({ flat_minor: undefined, percent: 10 }))}>
          <option value="percent">a percent</option>
          <option value="flat">a fixed amount</option>
        </select>
        {flat ? (
          <>
            <input aria-label="Amount" className={`${field} w-24 text-right`} inputMode="decimal" defaultValue={(r.flat_minor ?? 0) / 100} onChange={(e) => set({ flat_minor: Math.round((num(e.target.value) ?? 0) * 100) })} />
            <span className="text-muted-foreground">{currency} for</span>
          </>
        ) : (
          <>
            <input aria-label="Percent" className={`${field} w-16 text-right`} inputMode="decimal" defaultValue={r.percent ?? ""} onChange={(e) => set({ percent: Math.min(100, num(e.target.value) ?? 0) })} />
            <span className="text-muted-foreground">% of</span>
          </>
        )}
        <select aria-label="Pays on" className={field} value={r.pays_on} onChange={(e) => setPaysOn(e.target.value as CommissionRule["pays_on"])}>
          {paysOn.map(([k, v]) => <option key={k} value={k}>{v}</option>)}
        </select>
        <span className="text-muted-foreground">to</span>
        <select aria-label="Who gets it" className={field} value={r.who} onChange={(e) => set({ who: e.target.value as CommissionRule["who"] })}>
          {whoGets.filter(([k]) => !(payment && k === "sourced_by")).map(([k, v]) => <option key={k} value={k}>{k === "owner" ? (payment ? "the customer's owner" : "the deal's owner") : v}</option>)}
        </select>
      </div>

      <div className={line}>
        <span className="text-muted-foreground">Only for</span>
        <select aria-label="Inbound or outbound" className={field} value={r.source ?? ""} onChange={(e) => set({ source: (e.target.value || undefined) as CommissionRule["source"] })}>
          <option value="">inbound and outbound</option>
          <option value="inbound">inbound (they came to you)</option>
          <option value="outbound">outbound (you went to them)</option>
        </select>
        <select aria-label="New or returning" className={field} value={r.customer ?? ""} onChange={(e) => set({ customer: (e.target.value || undefined) as CommissionRule["customer"] })}>
          <option value="">any customer</option>
          <option value="new">new customers</option>
          <option value="reactivated">customers who came back</option>
        </select>
      </div>

      {payment && (
        <div className={line}>
          <label className="flex items-center gap-2">
            <input type="checkbox" checked={windowOn} onChange={(e) => (e.target.checked ? set({ days_from: 0, days_to: 30 }) : set({ days_from: undefined, days_to: undefined }))} />
            Only payments made from day
          </label>
          {windowOn && (
            <>
              <input aria-label="From day" className={`${field} w-16 text-right`} inputMode="numeric" value={r.days_from ?? ""} placeholder="0" onChange={(e) => set({ days_from: num(e.target.value) === undefined ? undefined : Math.round(num(e.target.value)!) })} />
              <span className="text-muted-foreground">to day</span>
              <input aria-label="To day" className={`${field} w-16 text-right`} inputMode="numeric" value={r.days_to ?? ""} placeholder="any" onChange={(e) => set({ days_to: num(e.target.value) === undefined ? undefined : Math.round(num(e.target.value)!) })} />
              <span className="text-muted-foreground">after the customer started paying</span>
            </>
          )}
          {!windowOn && <span className="text-muted-foreground">… (any day)</span>}
        </div>
      )}
      {payment && (
        <label className="flex items-start gap-2 text-[13px]">
          <input type="checkbox" className="mt-0.5" checked={!!r.clawback} onChange={(e) => set({ clawback: e.target.checked || undefined })} />
          Take the commission back when a payment is refunded
        </label>
      )}

      {!more ? (
        <button type="button" className="text-[12px] text-muted-foreground underline" onClick={() => setMore(true)}>More: rates by quota, a monthly cap, extra conditions</button>
      ) : (
        <div className="space-y-3 border-t pt-3">
          {!flat && (
            <div className="space-y-2">
              {tiers.map((t, k) => (
                <div key={k} className={line}>
                  <span className="text-muted-foreground">From</span>
                  <input aria-label="Percent of quota" className={`${field} w-16 text-right`} inputMode="numeric" value={t.from_attainment} onChange={(e) => set({ tiers: tiers.map((y, j) => (j === k ? { ...y, from_attainment: Math.round(num(e.target.value) ?? 0) } : y)) })} />
                  <span className="text-muted-foreground">% of quota, pay</span>
                  <input aria-label="Rate" className={`${field} w-16 text-right`} inputMode="decimal" value={t.percent} onChange={(e) => set({ tiers: tiers.map((y, j) => (j === k ? { ...y, percent: Math.min(100, num(e.target.value) ?? 0) } : y)) })} />
                  <span className="text-muted-foreground">%</span>
                  <Button variant="ghost" onClick={() => set({ tiers: tiers.length === 1 ? undefined : tiers.filter((_, j) => j !== k) })}>Remove</Button>
                </div>
              ))}
              {tiers.length < 10 && (
                <button type="button" className="text-[12px] text-muted-foreground underline" onClick={() => set({ tiers: [...tiers, { from_attainment: tiers.length ? tiers[tiers.length - 1].from_attainment + 50 : 100, percent: (r.percent ?? 10) + 2 }] })}>
                  {tiers.length ? "Add another rate" : "Pay a different rate once they reach a share of their quota"}
                </button>
              )}
            </div>
          )}
          <div className={line}>
            <label className="flex items-center gap-2">
              <input type="checkbox" checked={r.cap_minor != null} onChange={(e) => set({ cap_minor: e.target.checked ? 500000 : undefined })} />
              {r.cap_minor != null ? "Pay one person at most" : "Cap what one person can earn from this rule in a month"}
            </label>
            {r.cap_minor != null && (
              <>
                <input aria-label="Monthly cap" className={`${field} w-24 text-right`} inputMode="decimal" defaultValue={r.cap_minor / 100} onChange={(e) => set({ cap_minor: Math.round((num(e.target.value) ?? 0) * 100) })} />
                <span className="text-muted-foreground">{currency} a month from this rule</span>
              </>
            )}
          </div>
          <label className="flex items-start gap-2 text-[13px]">
            <input type="checkbox" className="mt-0.5" checked={!!r.stack} onChange={(e) => set({ stack: e.target.checked || undefined })} />
            Pay this on top, even when a rule above already paid the same person
          </label>
          <div className="space-y-2">
            {conditions.map((c, k) => {
              const bare = c.op === "exists" || c.op === "not_exists";
              return (
                <div key={k} className={line}>
                  <span className="w-10 text-muted-foreground">{k === 0 ? "When" : r.match === "any" ? "or" : "and"}</span>
                  <input aria-label="Field" className={`${field} w-48 font-mono`} placeholder="amount" list="commission-fields" value={c.attribute ?? ""} onChange={(e) => setC(k, { ...c, attribute: e.target.value.trim() })} />
                  <select aria-label="Compare" className={field} value={c.op} onChange={(e) => setC(k, { ...c, op: e.target.value as RuleCondition["op"] })}>
                    {ops.map(([v, t]) => <option key={v} value={v}>{t}</option>)}
                  </select>
                  {!bare && <input aria-label="Value" className={`${field} w-32`} placeholder="50000" value={c.value === undefined ? "" : String(c.value)} onChange={(e) => setC(k, { ...c, value: readValue(e.target.value) })} />}
                  <Button variant="ghost" onClick={() => set({ conditions: conditions.length === 1 ? undefined : conditions.filter((_, j) => j !== k) })}>Remove</Button>
                </div>
              );
            })}
            <datalist id="commission-fields">
              {["amount", "stage", "product", "company.country", "company.industry", "company.company_size"].map((x) => <option key={x} value={x} />)}
            </datalist>
            {conditions.length < 5 && (
              <button type="button" className="text-[12px] text-muted-foreground underline" onClick={() => set({ conditions: [...conditions, { attribute: "", op: "gt", value: "" }] })}>
                {conditions.length ? "Add another condition" : "Add a condition, such as amount is more than 50000 or company.country is DE"}
              </button>
            )}
          </div>
        </div>
      )}
    </div>
  );
}
