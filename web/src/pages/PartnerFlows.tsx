// Partner reward flows, as in Tolt: a trigger, an action (a share of the
// sale, a fixed amount, an action for your own system, or moving the
// partner to a group), AND/OR conditions on the partner, the customer and
// the transaction, its own approval, whether it adds to other cash flows
// or competes with them, and other rates for some partners or products.

import { useCallback, useEffect, useState, type FormEvent } from "react";
import type { Partner, PartnerProgram, PartnerRuleInput } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Empty, ErrorText, field, Input, Loading, Menu, ask, toast } from "../ui";
import { DataTable, type ColumnDef } from "@/components/data-table";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { ruleText, toRuleInput } from "./Partners";

type Flow = PartnerProgram["rules"][number];
type Conditions = NonNullable<PartnerRuleInput["conditions"]>;
type Group = NonNullable<Conditions["groups"]>[number];
type Cond = Group["conditions"][number];

// The facts a condition can test, with the kind of value each takes.
const fields: { id: string; label: string; kind: "number" | "money" | "text" | "bool" | "date" }[] = [
  { id: "partner.transactions", label: "Partner: sales", kind: "number" },
  { id: "partner.customers", label: "Partner: customers who bought", kind: "number" },
  { id: "partner.leads", label: "Partner: referred customers", kind: "number" },
  { id: "partner.revenue_cents", label: "Partner: revenue", kind: "money" },
  { id: "partner.earnings_cents", label: "Partner: earnings", kind: "money" },
  { id: "partner.country", label: "Partner: country (ISO code)", kind: "text" },
  { id: "partner.id", label: "Partner: id", kind: "text" },
  { id: "customer.transactions", label: "Customer: earlier sales", kind: "number" },
  { id: "customer.total_paid_cents", label: "Customer: paid so far", kind: "money" },
  { id: "customer.first_sale_at", label: "Customer: first sale", kind: "date" },
  { id: "customer.signed_up_at", label: "Customer: referred", kind: "date" },
  { id: "transaction.amount_cents", label: "Transaction: amount", kind: "money" },
  { id: "transaction.product_id", label: "Transaction: product or plan id", kind: "text" },
  { id: "transaction.recurring", label: "Transaction: a renewal", kind: "bool" },
  { id: "transaction.interval", label: "Transaction: billing interval (day, week, month, year, one_time)", kind: "text" },
  { id: "transaction.payment_source", label: "Transaction: payment provider (stripe, paddle…)", kind: "text" },
];
const opsFor: Record<string, { id: Cond["op"]; label: string }[]> = {
  number: [{ id: "gte", label: "at least" }, { id: "gt", label: "more than" }, { id: "lte", label: "at most" }, { id: "lt", label: "less than" }, { id: "eq", label: "is" }, { id: "neq", label: "is not" }],
  text: [{ id: "eq", label: "is" }, { id: "neq", label: "is not" }, { id: "in", label: "is one of" }, { id: "not_in", label: "is none of" }],
  bool: [{ id: "eq", label: "is" }],
  date: [{ id: "within", label: "within the last" }, { id: "not_within", label: "not within the last" }],
};
opsFor.money = opsFor.number;
const kindOf = (f: string) => fields.find((x) => x.id === f)?.kind ?? "number";

const approvalText: Record<string, string> = { program: "the program's hold", auto: "approved at once", after_days: "approved after", manual: "approved by a person" };

function scopeText(f: Flow, groups: PartnerProgram["groups"], partners: Partner[]): string {
  if (f.partner_id) return `${partners.find((p) => p.id === f.partner_id)?.code ?? "a partner"} only`;
  if (f.group_id) return `${groups.find((g) => g.id === f.group_id)?.name ?? "a group"} only`;
  return "Everyone";
}

function actionText(f: Flow, p: PartnerProgram): string {
  if (f.kind === "group") return `Move to ${p.groups.find((g) => g.id === f.target_group_id)?.name ?? "a group"}`;
  return ruleText(f, p.currency);
}

function conditionsText(c: Flow["conditions"]): string {
  const groups = (c.groups ?? []).filter((g) => g.conditions.length > 0);
  if (groups.length === 0) return "";
  const n = groups.reduce((s, g) => s + g.conditions.length, 0);
  return `${n} condition${n === 1 ? "" : "s"}${groups.length > 1 ? `, ${c.match === "any" ? "any" : "all"} of ${groups.length} groups` : ""}`;
}

// Flows lists a program's flows; editing one replaces the program's list.
export function Flows({ ws, program, canManage, onChanged }: { ws: string; program: PartnerProgram; canManage: boolean; onChanged: () => void }) {
  const [p, setP] = useState<PartnerProgram | null>(null);
  const [partners, setPartners] = useState<Partner[]>([]);
  const [editing, setEditing] = useState<{ index: number; flow: PartnerRuleInput } | null>(null);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    const [prog, list] = await Promise.all([
      api.GET("/v1/workspaces/{ws}/partner-programs/{id}", { params: { path: { ws, id: program.id } } }),
      api.GET("/v1/workspaces/{ws}/partners", { params: { path: { ws }, query: { program: program.id, limit: 500 } } }),
    ]);
    if (prog.error || !prog.data) return setError(errorMessage(prog.error));
    setP(prog.data);
    setPartners(list.data?.data ?? []);
  }, [ws, program.id]);
  useEffect(() => {
    load();
  }, [load]);
  if (!p) return error ? <ErrorText>{error}</ErrorText> : <Loading />;

  async function save(rules: PartnerRuleInput[], msg: string) {
    const { data, error } = await api.PUT("/v1/workspaces/{ws}/partner-programs/{id}/rules", { params: { path: { ws, id: p!.id } }, body: { rules } });
    if (error || !data) return errorMessage(error);
    setP(data);
    toast(msg);
    onChanged();
    return "";
  }
  async function remove(i: number) {
    const f = p!.rules[i];
    const r = await ask({ title: `Remove ${f.name || "this flow"}?`, body: "Rewards it already made keep their amounts.", choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Remove", value: "ok", variant: "danger-solid" }] });
    if (r?.choice !== "ok") return;
    const err = await save(p!.rules.filter((_, j) => j !== i).map(toRuleInput), "Flow removed.");
    if (err) toast(err, "error");
  }
  const rows = p.rules.map((f, i) => ({ f, i }));
  const columns: ColumnDef<{ f: Flow; i: number }>[] = [
    {
      id: "name", header: "Flow", accessorFn: ({ f }) => f.name,
      cell: ({ row: { original: { f } } }) => (
        <span className="block min-w-0">
          <span className="block truncate font-medium">{f.name || actionText(f, p)}</span>
          <span className="block truncate text-[12px] text-muted-foreground">{[scopeText(f, p.groups, partners), conditionsText(f.conditions)].filter(Boolean).join(" · ")}</span>
        </span>
      ),
    },
    { id: "action", header: "Does", accessorFn: ({ f }) => actionText(f, p) },
    {
      id: "approval", header: "Approval", accessorFn: ({ f }) => f.approval,
      cell: ({ row: { original: { f } } }) => (f.kind === "group" ? "" : f.approval === "after_days" ? `after ${f.approval_days} days` : approvalText[f.approval]),
    },
    {
      id: "stacking", header: "With others", accessorFn: ({ f }) => f.stacking,
      cell: ({ row: { original: { f } } }) => (f.kind === "percent" || f.kind === "fixed") && f.side === "partner" ? <Badge tone={f.stacking === "adds" ? "blue" : "grey"}>{f.stacking === "adds" ? "adds" : "exclusive"}</Badge> : null,
    },
    ...(canManage ? [{
      id: "actions", header: "", enableSorting: false,
      cell: ({ row: { original: { f, i } } }: { row: { original: { f: Flow; i: number } } }) => (
        <Menu aria={`Actions for ${f.name || "flow"}`} items={[{ label: "Edit", onClick: () => setEditing({ index: i, flow: toRuleInput(f) }) }, { label: "Remove", onClick: () => remove(i) }]} />
      ),
    } as ColumnDef<{ f: Flow; i: number }>] : []),
  ];
  const blank: PartnerRuleInput = { name: "", trigger: "sale", side: "partner", kind: "percent", percent_bp: 2000, recurrence: "every", approval: "program", stacking: "exclusive", conditions: {}, overrides: [] };
  // Templates are flows to start from; every field stays editable. A
  // template of two flows is added at once.
  const templates: { label: string; flows: PartnerRuleInput[] }[] = [
    { label: "Blank", flows: [blank] },
    { label: "One-time: 30% of the first sale", flows: [{ ...blank, name: "First sale", percent_bp: 3000, recurrence: "first" }] },
    { label: "Recurring: 20% of every sale", flows: [{ ...blank, name: "Recurring", percent_bp: 2000, recurrence: "every" }] },
    { label: "First 12 months: 25% of each sale", flows: [{ ...blank, name: "First 12 months", percent_bp: 2500, recurrence: "months", months: 12 }] },
    {
      label: "10% of the first sale, then 2% for 12 months",
      flows: [
        { ...blank, name: "First sale", percent_bp: 1000, recurrence: "first" },
        { ...blank, name: "Next 12 months", percent_bp: 200, recurrence: "months", months: 12 },
      ],
    },
    { label: "Fixed amount per sign-up", flows: [{ ...blank, name: "Sign-up", trigger: "signup", kind: "fixed", percent_bp: undefined, amount_cents: 500, recurrence: undefined }] },
  ];
  async function start(t: (typeof templates)[number]) {
    if (t.flows.length === 1) return setEditing({ index: -1, flow: t.flows[0] });
    const err = await save([...p!.rules.map(toRuleInput), ...t.flows], `Added ${t.flows.length} flows. Edit them to change the rates.`);
    if (err) toast(err, "error");
  }
  const newFlow = canManage && <Menu aria="New flow" label="New flow" items={templates.map((t) => ({ label: t.label, onClick: () => start(t) }))} />;
  return (
    <div className="space-y-3">
      <p className="text-[13px] text-muted-foreground">
        A flow pays when a sale, sign-up or custom event matches its conditions. Exclusive cash flows compete and one pays: a partner's own, then their group's, then the highest tier, then the narrower sale, then the higher payout. A flow that adds pays on top. Actions and group moves always run.
      </p>
      {rows.length === 0 ? (
        <Empty title="No flows: the program tracks only" action={newFlow}>
          Add a flow to pay partners a share of each sale, a fixed amount, or something your own product grants.
        </Empty>
      ) : (
        <DataTable columns={columns} data={rows} rowId={({ i }) => String(i)} searchPlaceholder="Search flows" toolbar={newFlow} empty="No flows match." />
      )}
      {editing && (
        <FlowEditor
          program={p}
          partners={partners}
          flow={editing.flow}
          onClose={() => setEditing(null)}
          onSave={async (flow) => {
            const list = p.rules.map(toRuleInput);
            if (editing.index < 0) list.push(flow);
            else list[editing.index] = flow;
            const err = await save(list, editing.index < 0 ? "Flow added." : "Flow saved. Rewards already earned keep their amounts.");
            if (!err) setEditing(null);
            return err;
          }}
        />
      )}
    </div>
  );
}

function FlowEditor({ program: p, partners, flow, onClose, onSave }: {
  program: PartnerProgram; partners: Partner[]; flow: PartnerRuleInput; onClose: () => void; onSave: (f: PartnerRuleInput) => Promise<string>;
}) {
  const [f, setF] = useState<PartnerRuleInput>(flow);
  const [amount, setAmount] = useState(flow.kind === "percent" ? String((flow.percent_bp ?? 0) / 100) : flow.kind === "fixed" ? String((flow.amount_cents ?? 0) / 100) : "");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const set = (patch: Partial<PartnerRuleInput>) => setF((cur) => ({ ...cur, ...patch }));
  const groups = f.conditions?.groups ?? [];
  const setGroups = (g: Group[]) => set({ conditions: { ...f.conditions, groups: g } });
  const trigger = f.trigger === "sale" || f.trigger === "signup" ? f.trigger : "custom";
  const cash = f.kind === "percent" || f.kind === "fixed";

  async function submit(e: FormEvent) {
    e.preventDefault();
    const n = Number(amount);
    if (cash && !(n > 0)) return setError("Enter the amount.");
    setBusy(true);
    const out: PartnerRuleInput = {
      ...f,
      name: f.name?.trim(),
      percent_bp: f.kind === "percent" ? Math.round(n * 100) : undefined,
      amount_cents: f.kind === "fixed" ? Math.round(n * 100) : undefined,
      months: f.trigger === "sale" && f.recurrence === "months" ? f.months ?? 12 : undefined,
      approval_days: f.approval === "after_days" ? f.approval_days ?? 30 : undefined,
      overrides: cash ? f.overrides : [],
      target_group_id: f.kind === "group" ? f.target_group_id : undefined,
      side: f.kind === "action" ? f.side : "partner",
    };
    setError(await onSave(out));
    setBusy(false);
  }

  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-2xl">
        <SheetHeader>
          <SheetTitle>{flow.name ? `Edit ${flow.name}` : "New flow"}</SheetTitle>
          <SheetDescription>When something happens, if the conditions hold, do something. Rewards already earned keep their amounts.</SheetDescription>
        </SheetHeader>
        <form onSubmit={submit} className="space-y-5 px-4 pb-6 text-[13px]">
          <Input label="Name" value={f.name ?? ""} maxLength={120} placeholder="Launch bonus" onChange={(e) => set({ name: e.target.value })} />
          <div className="grid gap-3 sm:grid-cols-2">
            <label className="block space-y-1">
              <span className="font-medium">For</span>
              <select className={`${field} w-full`} value={f.partner_id ? `p:${f.partner_id}` : f.group_id ? `g:${f.group_id}` : ""}
                onChange={(e) => { const v = e.target.value; set({ partner_id: v.startsWith("p:") ? v.slice(2) : null, group_id: v.startsWith("g:") ? v.slice(2) : null }); }}>
                <option value="">Every partner</option>
                {p.groups.map((g) => <option key={g.id} value={`g:${g.id}`}>Group: {g.name}</option>)}
                {partners.map((x) => <option key={x.id} value={`p:${x.id}`}>Partner: {x.name || x.code}</option>)}
              </select>
            </label>
            <label className="block space-y-1">
              <span className="font-medium">When</span>
              <select className={`${field} w-full`} value={trigger} onChange={(e) => set({ trigger: e.target.value === "custom" ? "trial_started" : e.target.value })}>
                <option value="sale">A sale or payment</option>
                <option value="signup">A sign-up</option>
                <option value="custom">A custom event</option>
              </select>
            </label>
            {trigger === "custom" && <Input label="Event name" value={f.trigger} maxLength={80} onChange={(e) => set({ trigger: e.target.value.trim() })} />}
            {f.trigger === "sale" && (
              <label className="block space-y-1">
                <span className="font-medium">Which sales</span>
                <span className="flex gap-2">
                  <select className={`${field} w-full`} value={f.recurrence ?? "every"} onChange={(e) => set({ recurrence: e.target.value as PartnerRuleInput["recurrence"] })}>
                    <option value="every">Every sale</option>
                    <option value="first">The customer's first sale</option>
                    <option value="months">Sales in the first months</option>
                  </select>
                  {f.recurrence === "months" && <input aria-label="Months" className={`${field} w-20`} type="number" min={1} max={120} value={f.months ?? 12} onChange={(e) => set({ months: Number(e.target.value) || undefined })} />}
                </span>
              </label>
            )}
          </div>

          <div className="grid gap-3 sm:grid-cols-2">
            <label className="block space-y-1">
              <span className="font-medium">Do</span>
              <select className={`${field} w-full`} value={f.kind} onChange={(e) => set({ kind: e.target.value as PartnerRuleInput["kind"] })}>
                <option value="percent">Pay a share of the sale</option>
                <option value="fixed">Pay a fixed amount</option>
                <option value="action">Send an action to your system</option>
                {p.groups.length > 0 && <option value="group">Move the partner to a group</option>}
              </select>
            </label>
            {cash && <Input label={f.kind === "percent" ? "Percent" : `Amount (${p.currency})`} type="number" step="0.01" min={0} value={amount} onChange={(e) => setAmount(e.target.value)} />}
            {f.kind === "action" && (
              <>
                <Input label="Action" value={f.action ?? ""} maxLength={60} placeholder="grant_credit" onChange={(e) => set({ action: e.target.value })} />
                <label className="block space-y-1">
                  <span className="font-medium">Who gets it</span>
                  <select className={`${field} w-full`} value={f.side ?? "partner"} onChange={(e) => set({ side: e.target.value as PartnerRuleInput["side"] })}>
                    <option value="partner">The partner</option>
                    <option value="referred">The customer they brought</option>
                  </select>
                </label>
              </>
            )}
            {f.kind === "group" && (
              <label className="block space-y-1">
                <span className="font-medium">Group</span>
                <select className={`${field} w-full`} value={f.target_group_id ?? ""} onChange={(e) => set({ target_group_id: e.target.value || undefined })}>
                  <option value="">Choose…</option>
                  {p.groups.map((g) => <option key={g.id} value={g.id}>{g.name}</option>)}
                </select>
              </label>
            )}
          </div>

          {f.kind !== "group" && (
            <div className="grid gap-3 sm:grid-cols-2">
              <label className="block space-y-1">
                <span className="font-medium">Approval</span>
                <span className="flex gap-2">
                  <select className={`${field} w-full`} value={f.approval ?? "program"} onChange={(e) => set({ approval: e.target.value as PartnerRuleInput["approval"] })}>
                    <option value="program">The program's hold ({p.hold_days} days)</option>
                    <option value="auto">Automatic, at once</option>
                    <option value="after_days">Automatic, after days</option>
                    <option value="manual">Manual</option>
                  </select>
                  {f.approval === "after_days" && <input aria-label="Days" className={`${field} w-20`} type="number" min={0} max={365} value={f.approval_days ?? 30} onChange={(e) => set({ approval_days: Number(e.target.value) })} />}
                </span>
              </label>
              {cash && f.side !== "referred" && (
                <label className="block space-y-1">
                  <span className="font-medium">With other cash flows</span>
                  <select className={`${field} w-full`} value={f.stacking ?? "exclusive"} onChange={(e) => set({ stacking: e.target.value as PartnerRuleInput["stacking"] })}>
                    <option value="exclusive">Exclusive: one of these pays</option>
                    <option value="adds">Adds on top</option>
                  </select>
                </label>
              )}
            </div>
          )}

          <ConditionsEditor conditions={f.conditions ?? {}} groups={groups} currency={p.currency} onMatch={(m) => set({ conditions: { ...f.conditions, match: m } })} onGroups={setGroups} />

          {cash && <Overrides flow={f} partners={partners} currency={p.currency} onChange={(o) => set({ overrides: o })} />}

          <ErrorText>{error}</ErrorText>
          <div className="flex gap-2">
            <Button disabled={busy}>Save</Button>
            <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
          </div>
        </form>
      </SheetContent>
    </Sheet>
  );
}

function ConditionsEditor({ conditions, groups, currency, onMatch, onGroups }: {
  conditions: Conditions; groups: Group[]; currency: string; onMatch: (m: "all" | "any") => void; onGroups: (g: Group[]) => void;
}) {
  const setGroup = (i: number, g: Group) => onGroups(groups.map((x, j) => (j === i ? g : x)));
  const blankCond: Cond = { field: "partner.transactions", op: "gte", value: 1 };
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium">Conditions</span>
        {groups.length > 1 && (
          <select aria-label="Groups match" className={field} value={conditions.match ?? "all"} onChange={(e) => onMatch(e.target.value as "all" | "any")}>
            <option value="all">All groups must hold (AND)</option>
            <option value="any">Any group may hold (OR)</option>
          </select>
        )}
      </div>
      {groups.length === 0 && <p className="text-muted-foreground">None: it runs on every matching event.</p>}
      {groups.map((g, i) => (
        <div key={i} className="space-y-2 rounded-md border p-2">
          <div className="flex items-center gap-2">
            <select aria-label="Group match" className={field} value={g.match ?? "all"} onChange={(e) => setGroup(i, { ...g, match: e.target.value as "all" | "any" })}>
              <option value="all">All of these (AND)</option>
              <option value="any">Any of these (OR)</option>
            </select>
            <Button type="button" variant="ghost" className="ml-auto" onClick={() => onGroups(groups.filter((_, j) => j !== i))}>Remove group</Button>
          </div>
          {g.conditions.map((c, k) => (
            <ConditionRow key={k} c={c} currency={currency}
              onChange={(nc) => setGroup(i, { ...g, conditions: g.conditions.map((x, j) => (j === k ? nc : x)) })}
              onRemove={() => setGroup(i, { ...g, conditions: g.conditions.filter((_, j) => j !== k) })} />
          ))}
          <Button type="button" variant="ghost" onClick={() => setGroup(i, { ...g, conditions: [...g.conditions, blankCond] })}>Add a condition</Button>
        </div>
      ))}
      <Button type="button" variant="ghost" onClick={() => onGroups([...groups, { match: "all", conditions: [blankCond] }])}>Add a condition group</Button>
    </div>
  );
}

function ConditionRow({ c, currency, onChange, onRemove }: { c: Cond; currency: string; onChange: (c: Cond) => void; onRemove: () => void }) {
  const kind = kindOf(c.field);
  const reset = (fieldID: string): Cond => {
    const k = kindOf(fieldID);
    const op = opsFor[k][0].id;
    const value = k === "bool" ? true : k === "date" ? { amount: 30, unit: "days" } : k === "text" ? "" : 0;
    return { field: fieldID, op, value };
  };
  const within = (c.value ?? {}) as { amount?: number; unit?: string };
  return (
    <div className="flex flex-wrap items-center gap-2">
      <select aria-label="Field" className={field} value={c.field} onChange={(e) => onChange(reset(e.target.value))}>
        {fields.map((x) => <option key={x.id} value={x.id}>{x.label}</option>)}
      </select>
      <select aria-label="Comparison" className={field} value={c.op} onChange={(e) => onChange({ ...c, op: e.target.value as Cond["op"], value: e.target.value === "in" || e.target.value === "not_in" ? [] : kind === "text" ? "" : c.value })}>
        {opsFor[kind].map((o) => <option key={o.id} value={o.id}>{o.label}</option>)}
      </select>
      {kind === "bool" && (
        <select aria-label="Value" className={field} value={String(c.value)} onChange={(e) => onChange({ ...c, value: e.target.value === "true" })}>
          <option value="true">yes</option>
          <option value="false">no</option>
        </select>
      )}
      {(kind === "number" || kind === "money") && (
        <input aria-label="Value" className={`${field} w-28`} type="number" step={kind === "money" ? "0.01" : "1"}
          value={kind === "money" ? Number(c.value ?? 0) / 100 : Number(c.value ?? 0)}
          onChange={(e) => onChange({ ...c, value: kind === "money" ? Math.round(Number(e.target.value) * 100) : Number(e.target.value) })} />
      )}
      {kind === "money" && <span className="text-muted-foreground">{currency}</span>}
      {kind === "text" && (
        <input aria-label="Value" className={field} placeholder={c.op === "in" || c.op === "not_in" ? "DE, AT, CH" : ""}
          value={Array.isArray(c.value) ? (c.value as string[]).join(", ") : String(c.value ?? "")}
          onChange={(e) => onChange({ ...c, value: c.op === "in" || c.op === "not_in" ? e.target.value.split(",").map((s) => s.trim()).filter(Boolean) : e.target.value })} />
      )}
      {kind === "date" && (
        <>
          <input aria-label="Amount" className={`${field} w-20`} type="number" min={1} max={3650} value={within.amount ?? 30} onChange={(e) => onChange({ ...c, value: { amount: Number(e.target.value), unit: within.unit ?? "days" } })} />
          <select aria-label="Unit" className={field} value={within.unit ?? "days"} onChange={(e) => onChange({ ...c, value: { amount: within.amount ?? 30, unit: e.target.value } })}>
            <option value="days">days</option>
            <option value="months">months</option>
            <option value="years">years</option>
          </select>
        </>
      )}
      <Button type="button" variant="ghost" onClick={onRemove}>Remove</Button>
    </div>
  );
}

function Overrides({ flow: f, partners, currency, onChange }: { flow: PartnerRuleInput; partners: Partner[]; currency: string; onChange: (o: NonNullable<PartnerRuleInput["overrides"]>) => void }) {
  const list = f.overrides ?? [];
  const pct = f.kind === "percent";
  const set = (i: number, o: (typeof list)[number]) => onChange(list.map((x, j) => (j === i ? o : x)));
  return (
    <div className="space-y-2">
      <span className="font-medium">Other rates in this flow</span>
      {list.length === 0 && <p className="text-muted-foreground">None. Give one partner, or one product or plan, a different {pct ? "share" : "amount"}.</p>}
      {list.map((o, i) => (
        <div key={i} className="flex flex-wrap items-center gap-2">
          <select aria-label="For" className={field} value={o.partner_id ? `p:${o.partner_id}` : "product"}
            onChange={(e) => set(i, e.target.value === "product" ? { ...o, partner_id: undefined, product_id: o.product_id ?? "" } : { ...o, partner_id: e.target.value.slice(2), product_id: undefined })}>
            <option value="product">Product or plan</option>
            {partners.map((x) => <option key={x.id} value={`p:${x.id}`}>Partner: {x.name || x.code}</option>)}
          </select>
          {!o.partner_id && <input aria-label="Product id" className={field} placeholder="price_pro" value={o.product_id ?? ""} onChange={(e) => set(i, { ...o, product_id: e.target.value })} />}
          <input aria-label={pct ? "Percent" : "Amount"} className={`${field} w-24`} type="number" step="0.01" min={0}
            value={pct ? (o.percent_bp ?? 0) / 100 : (o.amount_cents ?? 0) / 100}
            onChange={(e) => set(i, pct ? { ...o, percent_bp: Math.round(Number(e.target.value) * 100), amount_cents: undefined } : { ...o, amount_cents: Math.round(Number(e.target.value) * 100), percent_bp: undefined })} />
          <span className="text-muted-foreground">{pct ? "%" : currency}</span>
          <Button type="button" variant="ghost" onClick={() => onChange(list.filter((_, j) => j !== i))}>Remove</Button>
        </div>
      ))}
      <Button type="button" variant="ghost" onClick={() => onChange([...list, pct ? { product_id: "", percent_bp: f.percent_bp ?? 2000 } : { product_id: "", amount_cents: f.amount_cents ?? 1000 }])}>Add a rate</Button>
    </div>
  );
}
