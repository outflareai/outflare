// Partners: customer referral, affiliate and influencer programs on one
// platform. A program holds the rules; partners are customer records with a
// ref code, links and promo codes; the workspace's backend reports
// conversions; rewards are a ledger, and payouts are marked paid once the
// workspace has paid them from its own account.

import { useCallback, useEffect, useState, type FormEvent, type ReactNode } from "react";
import { appPath } from "@/lib/paths";
import { track } from "@/lib/analytics";
import type { Partner, PartnerAsset, PartnerGroup, PartnerStats, PartnerConversion, PartnerImport, PartnerPayout, PartnerProgram, PartnerReferral, PartnerReward, PartnerRuleInput } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { useBrand } from "@/lib/brand";
import { Badge, Button, CopyButton, Empty, ErrorText, field, Input, Loading, Menu, PageHeader, Stat, Stats, ask, toast } from "../ui";
import { ago, plural } from "../format";
import { DataTable, type ColumnDef } from "@/components/data-table";
import { Flows } from "./PartnerFlows";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { PartnerInstall } from "./PartnerInstall";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";

type Props = { ws: string; canManage: boolean };
type Section = "partners" | "groups" | "flows" | "conversions" | "rewards" | "payouts";
type Kind = PartnerProgram["kind"];

const kinds: { id: Kind; label: string; blurb: string }[] = [
  { id: "affiliate", label: "Affiliate", blurb: "Marketers and review sites earn a share of each sale (20% to start), held 30 days for refunds and paid above a minimum." },
  { id: "referral", label: "Customer referral", blurb: "Customers share a link; on the friend's first sale both get a reward your own product grants, such as credit." },
  { id: "influencer", label: "Influencer", blurb: "Track what creators' links and codes bring. Log their fixed fees so cost per sale works." },
];

export const money = (cents: number, currency: string) => {
  try {
    return new Intl.NumberFormat(undefined, { style: "currency", currency, maximumFractionDigits: Math.abs(cents) < 100000 ? 2 : 0 }).format(cents / 100);
  } catch {
    return `${(cents / 100).toFixed(2)} ${currency}`;
  }
};

const partnerTone = { active: "green", pending: "amber", suspended: "red", rejected: "grey" } as const;
const rewardTone = { pending: "amber", approved: "blue", paid: "green", fulfilled: "green", rejected: "grey" } as const;

export function Partners({ ws, canManage }: Props) {
  const { query: brandQuery } = useBrand(ws);
  const [programs, setPrograms] = useState<PartnerProgram[] | null>(null);
  const [program, setProgram] = useState("");
  const [section, setSection] = useState<Section>("partners");
  const [creating, setCreating] = useState(false);
  const [settings, setSettings] = useState(false);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/partner-programs", { params: { path: { ws } } });
    if (error) setError(errorMessage(error));
    const list = (data?.data ?? []).filter((p) => p.status !== "archived");
    setPrograms(list);
    setProgram((cur) => (list.some((p) => p.id === cur) ? cur : list[0]?.id ?? ""));
  }, [ws, brandQuery.brand]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    load();
  }, [load]);

  if (!programs) return <Loading />;
  const current = programs.find((p) => p.id === program);
  return (
    <div className="space-y-6">
      <PageHeader
        title="Partners"
        description="Customer referrals, affiliates and influencers on one platform. Partners are customer records with their own links and codes; your backend reports sign-ups, sales and refunds; rewards wait out a hold, then you pay from your own account and mark them paid."
        action={canManage && <Button onClick={() => setCreating(true)}>New program</Button>}
      />
      <ErrorText>{error}</ErrorText>
      {programs.length === 0 || !current ? (
        <Empty title="No partner programs yet" action={canManage && <Button onClick={() => setCreating(true)}>New program</Button>}>
          Start from a preset: affiliates earn a share of each sale, customer referrals reward both sides with something your product grants, and influencer programs track what each creator brings. Every setting can be changed.
        </Empty>
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-2">
            <select aria-label="Program" className={field} value={program} onChange={(e) => setProgram(e.target.value)}>
              {programs.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
            </select>
            <Badge tone="grey">{kinds.find((k) => k.id === current.kind)?.label}</Badge>
            {current.status === "paused" && <Badge tone="amber">Paused</Badge>}
            <span className="text-[12px] text-muted-foreground">{rulesSummary(current)}</span>
            <Button variant="ghost" className="ml-auto" onClick={() => setSettings(true)}>Settings</Button>
          </div>
          <Tabs value={section} onValueChange={(v) => setSection(v as Section)}>
            <TabsList>
              <TabsTrigger value="partners" className="px-3 text-[12px]">Partners</TabsTrigger>
              <TabsTrigger value="groups" className="px-3 text-[12px]">Groups</TabsTrigger>
              <TabsTrigger value="flows" className="px-3 text-[12px]">Flows</TabsTrigger>
              <TabsTrigger value="conversions" className="px-3 text-[12px]">Conversions</TabsTrigger>
              <TabsTrigger value="rewards" className="px-3 text-[12px]">Rewards</TabsTrigger>
              <TabsTrigger value="payouts" className="px-3 text-[12px]">Payouts</TabsTrigger>
            </TabsList>
          </Tabs>
          {section === "partners" && <PartnerList key={current.id} ws={ws} program={current} canManage={canManage} />}
          {section === "groups" && <Groups key={current.id} ws={ws} program={current} canManage={canManage} onChanged={load} />}
          {section === "flows" && <Flows key={current.id} ws={ws} program={current} canManage={canManage} onChanged={load} />}
          {section === "conversions" && <Conversions key={current.id} ws={ws} program={current} />}
          {section === "rewards" && <Rewards key={current.id} ws={ws} program={current} canManage={canManage} />}
          {section === "payouts" && <Payouts key={current.id} ws={ws} program={current} canManage={canManage} />}
        </>
      )}
      {creating && <NewProgram ws={ws} onClose={() => setCreating(false)} onSaved={(p) => { setCreating(false); setProgram(p.id); load(); }} />}
      {settings && current && <ProgramSettings ws={ws} program={current} canManage={canManage} onClose={() => { setSettings(false); load(); }} />}
    </div>
  );
}

export function ruleText(r: PartnerProgram["rules"][number], currency: string): string {
  if (r.kind === "group") return r.name || "Moves the partner to a group";
  const what = r.kind === "percent" ? `${(r.percent_bp ?? 0) / 100}%` : r.kind === "fixed" ? money(r.amount_cents ?? 0, currency) : r.action;
  const when = r.trigger === "signup" ? "per sign-up" : r.trigger !== "sale" ? `per ${r.trigger}` : r.recurrence === "first" ? "on the first sale" : r.recurrence === "months" ? `on sales for ${r.months} months` : "on every sale";
  const who = r.side === "referred" ? " for the friend" : "";
  const tier = r.min_sales > 0 ? ` after ${r.min_sales} sales` : "";
  return `${what} ${when}${who}${tier}`;
}

function rulesSummary(p: PartnerProgram): string {
  const all = p.rules.filter((r) => !r.partner_id && !r.group_id);
  const own = new Set(p.rules.filter((r) => r.partner_id).map((r) => r.partner_id)).size;
  const groups = new Set(p.rules.filter((r) => r.group_id && !r.partner_id).map((r) => r.group_id)).size;
  const extra = [groups > 0 && `${plural(groups, "group has its own rate", "groups have their own rates")}`, own > 0 && `${plural(own, "partner has their own rate", "partners have their own rates")}`].filter(Boolean);
  const text = all.length === 0 ? "Track only" : all.map((r) => ruleText(r, p.currency)).join(" + ");
  return [text, ...extra].join("; ");
}

// toRuleInput is a saved rule as PUT .../rules takes it back.
export function toRuleInput(r: PartnerProgram["rules"][number]): PartnerRuleInput {
  return {
    trigger: r.trigger, side: r.side, kind: r.kind, recurrence: r.recurrence, months: r.months ?? undefined, min_sales: r.min_sales,
    action: r.action, action_data: r.action_data, group_id: r.group_id, partner_id: r.partner_id, product_ids: r.product_ids,
    percent_bp: r.percent_bp ?? undefined, amount_cents: r.amount_cents ?? undefined,
    name: r.name, conditions: r.conditions, approval: r.approval, approval_days: r.approval_days ?? undefined, stacking: r.stacking,
    overrides: r.overrides, target_group_id: r.target_group_id ?? undefined,
  };
}

// ownRate is a partner's own cash rule for sales, if they have one.
function ownRate(rules: PartnerProgram["rules"], partner: string) {
  return rules.find((r) => r.partner_id === partner && r.side === "partner" && r.trigger === "sale" && (r.kind === "percent" || r.kind === "fixed"));
}

// A program's totals across all its partners.
type PartnerProgramStats = PartnerStats & { partners: { total: number; active: number; pending: number; suspended: number; rejected: number; missing_payout_details: number } };

// A page of a list, newest first, and the cursor to the next.
type Page<T> = { list: T[]; next: string | null; error?: string };

// usePages loads a list a page at a time: reload starts again, more adds
// the next page. get must be stable (useCallback).
function usePages<T>(get: (before?: string) => Promise<Page<T>>) {
  const [list, setList] = useState<T[] | null>(null);
  const [next, setNext] = useState<string | null>(null);
  const [error, setError] = useState("");
  const reload = useCallback(async () => {
    const r = await get();
    setList(r.list);
    setNext(r.next);
    setError(r.error ?? "");
  }, [get]);
  const more = useCallback(async () => {
    if (!next) return;
    const r = await get(next);
    setList((l) => [...(l ?? []), ...r.list]);
    setNext(r.next);
    setError(r.error ?? "");
  }, [get, next]);
  return { list, next, error, reload, more };
}

function LoadMore({ next, onMore, shown }: { next: string | null; onMore: () => Promise<void>; shown: number }) {
  const [busy, setBusy] = useState(false);
  if (!next) return null;
  return (
    <div className="flex items-center justify-center gap-3 text-[12px] text-muted-foreground">
      <span>Showing the newest {shown}. Search and sorting cover these.</span>
      <Button variant="ghost" disabled={busy} onClick={async () => { setBusy(true); await onMore(); setBusy(false); }}>{busy ? "Loading…" : "Load more"}</Button>
    </div>
  );
}

const pageSize = 100;

function PartnerList({ ws, program, canManage }: { ws: string; program: PartnerProgram; canManage: boolean }) {
  const [rules, setRules] = useState(program.rules);
  const [groups, setGroups] = useState<PartnerGroup[]>([]);
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [adding, setAdding] = useState(false);
  const [totals, setTotals] = useState<PartnerProgramStats | null>(null);
  // Only partners who haven't given payout details (imported ones re-enter them on the portal).
  const [missingOnly, setMissingOnly] = useState(false);
  useEffect(() => setRules(program.rules), [program.rules]);
  const get = useCallback(async (before?: string): Promise<Page<Partner>> => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/partners", {
      params: { path: { ws }, query: { program: program.id, limit: pageSize, ...(before ? { before } : {}), ...(missingOnly ? { payout_details: "missing" as const } : {}) } },
    });
    return { list: data?.data ?? [], next: data?.next_before ?? null, error: error ? errorMessage(error) : "" };
  }, [ws, program.id, missingOnly]);
  const { list, next, error, reload, more } = usePages(get);
  // The tiles count every partner, not just the loaded page.
  const load = useCallback(async () => {
    const [, stats, prog] = await Promise.all([
      reload(),
      api.GET("/v1/workspaces/{ws}/partner-programs/{id}/stats", { params: { path: { ws, id: program.id } } }),
      api.GET("/v1/workspaces/{ws}/partner-programs/{id}", { params: { path: { ws, id: program.id } } }),
    ]);
    setTotals(stats.data ?? null);
    if (prog.data) {
      setGroups(prog.data.groups);
      setRules(prog.data.rules);
    }
    setPicked(new Set());
  }, [reload, ws, program.id]);

  // move puts the picked partners in a group, or in none.
  async function move(group: string) {
    const ids = [...picked];
    const { data, error } = await api.POST("/v1/workspaces/{ws}/partner-programs/{id}/groups/{group}/partners", {
      params: { path: { ws, id: program.id, group } },
      body: { partner_ids: ids },
    });
    if (error || !data) return toast(errorMessage(error), "error");
    const name = groups.find((g) => g.id === group)?.name;
    toast(`Moved ${plural(data.moved, "partner")} ${name ? `to ${name}` : "out of their group"}.`);
    load();
  }
  const toggle = (id: string) => setPicked((cur) => {
    const next = new Set(cur);
    if (!next.delete(id)) next.add(id);
    return next;
  });
  useEffect(() => {
    load();
  }, [load]);

  async function setStatus(p: Partner, status: Partner["status"]) {
    const { error } = await api.PATCH("/v1/workspaces/{ws}/partners/{id}", { params: { path: { ws, id: p.id } }, body: { status } });
    if (error) return toast(errorMessage(error), "error");
    toast(status === "active" ? `${p.name || p.code} is active.` : `${p.name || p.code} is ${status}. Their links still land, uncounted.`);
    load();
  }
  async function addCode(p: Partner) {
    const r = await ask({ title: "Add a promo code", body: "The code as customers type it at checkout.", input: { label: "Code", placeholder: "ANA20", maxLength: 64 }, choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Add", value: "ok" }] });
    if (r?.choice !== "ok" || !r.value.trim()) return;
    const c = await ask({ title: "Make it in Stripe too?", body: "Give the id of a coupon in your connected Stripe account and Outflare makes the code there with your own key. Leave it empty if you make the code yourself.", input: { label: "Coupon id", placeholder: "co_...", maxLength: 200, optional: true }, choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Add", value: "ok" }] });
    if (c?.choice !== "ok") return;
    const { error } = await api.POST("/v1/workspaces/{ws}/partners/{id}/promo-codes", { params: { path: { ws, id: p.id } }, body: { code: r.value.trim(), ...(c.value.trim() ? { coupon: c.value.trim() } : {}) } });
    if (error) return toast(errorMessage(error), "error");
    toast(`Added ${r.value.trim()}.`);
    load();
  }
  async function ledger(p: Partner, kind: "fees" | "adjustments") {
    const fee = kind === "fees";
    const r = await ask({
      title: fee ? "Log a fee" : "Correct the balance",
      body: fee ? "A fixed fee, such as for a post or a month. It counts toward their next payout and as a cost." : "Positive adds to what they're owed, negative takes away. Say why in the note.",
      input: { label: `Amount (${program.currency})`, type: "number", placeholder: fee ? "500" : "-20" },
      choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: fee ? "Log fee" : "Next", value: "ok" }],
    });
    if (r?.choice !== "ok") return;
    const cents = Math.round(Number(r.value) * 100);
    if (!Number.isFinite(cents) || cents === 0) return toast("Enter an amount.", "error");
    let note = "";
    if (!fee) {
      const n = await ask({ title: "Why?", input: { label: "Note", maxLength: 2000 }, choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Save", value: "ok" }] });
      if (n?.choice !== "ok" || !n.value.trim()) return;
      note = n.value.trim();
    }
    const { error } = await api.POST(`/v1/workspaces/{ws}/partners/{id}/${kind}`, { params: { path: { ws, id: p.id } }, body: { amount_cents: cents, note } });
    if (error) return toast(errorMessage(error), "error");
    toast(fee ? "Fee logged." : "Balance corrected.");
    load();
  }

  async function postback(p: Partner) {
    const { data } = await api.GET("/v1/workspaces/{ws}/partners/{id}/postbacks", { params: { path: { ws, id: p.id } } });
    const sends = data?.data ?? [];
    const last = sends.slice(0, 5).map((s) => `${ago(s.created_at)}: ${s.status}${s.http_status ? ` (${s.http_status})` : ""}${s.error ? `, ${s.error}` : ""}`);
    const r = await ask({
      title: "Postback URL",
      body: (
        <span className="space-y-2">
          <span className="block">The partner's own tracker, called once for each conversion credited to them and again if it's refunded. Placeholders: {"{sub_id}"} (the sub on their link, usually their click id), {"{click_id}"}, {"{conversion_id}"}, {"{type}"}, {"{amount}"}, {"{currency}"}, {"{commission}"}, {"{status}"} and more (see the docs). Partners can set it on the portal too. Empty turns it off.</span>
          {last.length > 0 && <span className="block text-muted-foreground">Recent: {last.join(" · ")}</span>}
        </span>
      ),
      input: { label: "https address", value: p.postback_url, placeholder: "https://tracker.example/postback?clickid={sub_id}&payout={commission}", maxLength: 2000, optional: true },
      choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Save", value: "ok" }],
    });
    if (r?.choice !== "ok") return;
    const { error } = await api.PATCH("/v1/workspaces/{ws}/partners/{id}", { params: { path: { ws, id: p.id } }, body: { postback_url: r.value.trim() } });
    if (error) return toast(errorMessage(error), "error");
    toast(r.value.trim() ? "Postback URL saved." : "Postbacks turned off.");
    load();
  }

  // rate gives a partner their own share of each sale, which replaces the
  // program's cash rate for them (their group's and tiers too).
  async function rate(p: Partner) {
    const cur = await api.GET("/v1/workspaces/{ws}/partner-programs/{id}", { params: { path: { ws, id: program.id } } });
    if (cur.error || !cur.data) return toast(errorMessage(cur.error), "error");
    const mine = ownRate(cur.data.rules, p.id);
    const r = await ask({
      title: `Commission for ${p.name || p.code}`,
      body: `Their own share of each sale, instead of the program's (${rulesSummary({ ...cur.data, rules: cur.data.rules.filter((x) => !x.partner_id) })}). Leave it empty to go back to the program's rate.`,
      input: { label: "Percent of each sale", type: "number", value: mine?.kind === "percent" ? String((mine.percent_bp ?? 0) / 100) : "", placeholder: "30", optional: true },
      choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Save", value: "ok" }],
    });
    if (r?.choice !== "ok") return;
    const pct = r.value.trim() === "" ? null : Number(r.value);
    if (pct !== null && !(pct > 0 && pct <= 100)) return toast("Enter a percent from 0.01 to 100.", "error");
    const next = cur.data.rules.filter((x) => x !== mine).map(toRuleInput);
    if (pct !== null) next.push({ trigger: "sale", side: "partner", kind: "percent", percent_bp: Math.round(pct * 100), recurrence: "every", partner_id: p.id });
    const { data, error } = await api.PUT("/v1/workspaces/{ws}/partner-programs/{id}/rules", { params: { path: { ws, id: program.id } }, body: { rules: next } });
    if (error || !data) return toast(errorMessage(error), "error");
    setRules(data.rules);
    toast(pct === null ? `${p.name || p.code} is back on the program's rate.` : `${p.name || p.code} earns ${pct}% of each sale from now on. Rewards already earned keep their amounts.`);
  }

  async function viewAs(p: Partner) {
    const { data, error } = await api.POST("/v1/workspaces/{ws}/partners/{id}/impersonate", { params: { path: { ws, id: p.id } } });
    if (error || !data) return toast(errorMessage(error), "error");
    window.open(data.url, "_blank", "noopener");
  }

  const c = program.currency;
  const groupName = (id: string | null) => groups.find((g) => g.id === id)?.name ?? "";
  const columns: ColumnDef<Partner>[] = [
    ...(canManage && groups.length > 0 ? [{
      id: "pick", enableSorting: false,
      header: () => list && list.length > 0 && (
        <input type="checkbox" aria-label="Pick every partner" checked={picked.size > 0 && picked.size === list.length}
          onChange={(e) => setPicked(e.target.checked ? new Set(list.map((p) => p.id)) : new Set())} />
      ),
      cell: ({ row: { original: p } }: { row: { original: Partner } }) => (
        <input type="checkbox" aria-label={`Pick ${p.code}`} checked={picked.has(p.id)} onChange={() => toggle(p.id)} />
      ),
    } as ColumnDef<Partner>] : []),
    {
      id: "name", header: "Partner", accessorFn: (p) => `${p.name} ${p.email} ${p.code}`,
      cell: ({ row: { original: p } }) => (
        <span className="block min-w-0">
          <span className="block truncate font-medium">{p.name || p.email || p.code}</span>
          <span className="block truncate font-mono text-[12px] text-muted-foreground">{p.code}{ownRate(rules, p.id) && ` · own rate ${ruleText(ownRate(rules, p.id)!, c)}`}{p.promo_codes.length > 0 && ` · ${p.promo_codes.map((c) => c.code + (c.status === "failed" ? " (not made in billing)" : c.status === "pending" ? " (making…)" : "")).join(", ")}`}{!p.payout_method && " · no payout details"}</span>
        </span>
      ),
    },
    { id: "status", header: "Status", accessorFn: (p) => p.status, cell: ({ row: { original: p } }) => <Badge tone={partnerTone[p.status]}>{p.status}</Badge> },
    ...(groups.length > 0 ? [{ id: "group", header: "Group", accessorFn: (p: Partner) => groupName(p.group_id) } as ColumnDef<Partner>] : []),
    { id: "clicks", header: "Clicks", accessorFn: (p) => p.stats.clicks },
    { id: "customers", header: "Customers", accessorFn: (p) => p.stats.customers, cell: ({ row: { original: p } }) => `${p.stats.customers} of ${p.stats.referrals}` },
    { id: "sales", header: "Sales", accessorFn: (p) => p.stats.sales_cents, cell: ({ row: { original: p } }) => money(p.stats.sales_cents, c) },
    { id: "pending", header: "On hold", accessorFn: (p) => p.stats.pending_cents, cell: ({ row: { original: p } }) => money(p.stats.pending_cents, c) },
    { id: "balance", header: "Due", accessorFn: (p) => p.stats.balance_cents, cell: ({ row: { original: p } }) => money(p.stats.balance_cents, c) },
    { id: "paid", header: "Paid", accessorFn: (p) => p.stats.paid_cents, cell: ({ row: { original: p } }) => money(p.stats.paid_cents, c) },
    {
      id: "actions", header: "", enableSorting: false,
      cell: ({ row: { original: p } }) => (
        <span className="flex items-center justify-end gap-1">
          <CopyButton text={p.link} label="Link" />
          {canManage && (
            <Menu aria={`Actions for ${p.code}`} items={[
              ...(p.status !== "active" ? [{ label: p.status === "pending" ? "Approve" : "Reactivate", onClick: () => setStatus(p, "active") }] : []),
              ...(p.status === "pending" ? [{ label: "Reject", onClick: () => setStatus(p, "rejected") }] : []),
              ...(p.status === "active" ? [{ label: "Suspend", onClick: () => setStatus(p, "suspended") }] : []),
              { label: "Add promo code", onClick: () => addCode(p) },
              { label: "Log a fee", onClick: () => ledger(p, "fees") },
              { label: "Correct balance", onClick: () => ledger(p, "adjustments") },
              { label: "Commission rate", onClick: () => rate(p) },
              { label: "Postback URL", onClick: () => postback(p) },
              { label: "View as partner", onClick: () => viewAs(p) },
            ]} />
          )}
        </span>
      ),
    },
  ];

  if (!list) return <Loading />;
  return (
    <div className="space-y-4">
      <ErrorText>{error}</ErrorText>
      {totals && (
        <Stats>
          <Stat label="Partners" value={totals.partners.active} sub={totals.partners.pending ? `${totals.partners.pending} waiting for approval` : undefined} />
          <Stat label="Referred customers" value={totals.customers} sub={plural(totals.clicks, "click")} />
          <Stat label="Referred sales" value={money(totals.sales_cents, c)} />
          <Stat label="Due to partners" value={money(totals.balance_cents, c)} sub={`${money(totals.pending_cents, c)} on hold`} />
        </Stats>
      )}
      {list.length === 0 && !missingOnly ? (
        <Empty title="No partners yet" action={canManage && <Button onClick={() => setAdding(true)}>Add partner</Button>}>
          Add someone by email, or a customer record you already have. Each partner gets a ref code, a short link and a tagged campaign link.
        </Empty>
      ) : (
        <DataTable
          columns={columns}
          data={list}
          rowId={(p) => p.id}
          searchPlaceholder="Search partners"
          toolbar={
            <span className="flex flex-wrap items-center gap-2">
              {canManage && picked.size > 0 && (
                <select aria-label="Move to group" className={field} value="" onChange={(e) => e.target.value && move(e.target.value)}>
                  <option value="">Move {picked.size} to…</option>
                  {groups.map((g) => <option key={g.id} value={g.id}>{g.name}</option>)}
                  <option value="none">No group</option>
                </select>
              )}
              {(missingOnly || (totals?.partners.missing_payout_details ?? 0) > 0) && (
                <Button variant={missingOnly ? "primary" : "ghost"} onClick={() => { track("ui.partners_filtered", { filter: "payout_details", value: missingOnly ? "all" : "missing" }); setMissingOnly(!missingOnly); }}>
                  {missingOnly ? "All partners" : `No payout details (${totals?.partners.missing_payout_details})`}
                </Button>
              )}
              {canManage && <Button onClick={() => setAdding(true)}>Add partner</Button>}
            </span>
          }
          empty="No partners match."
          mobile={(p) => (
            <span className="block min-w-0">
              <span className="block truncate text-[13px] font-medium">{p.name || p.email || p.code}</span>
              <span className="block truncate text-[12px] text-muted-foreground">{p.status} · {plural(p.stats.customers, "customer")} · {money(p.stats.balance_cents, c)} due</span>
            </span>
          )}
        />
      )}
      <LoadMore next={next} onMore={more} shown={list.length} />
      {adding && <AddPartner ws={ws} program={program} onClose={() => setAdding(false)} onSaved={() => { setAdding(false); load(); }} />}
    </div>
  );
}

function AddPartner({ ws, program, onClose, onSaved }: { ws: string; program: PartnerProgram; onClose: () => void; onSaved: () => void }) {
  const [email, setEmail] = useState("");
  const [name, setName] = useState("");
  const [code, setCode] = useState("");
  const [made, setMade] = useState<Partner | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/partners", {
      params: { path: { ws } },
      body: { program: program.id, email: email.trim(), name: name.trim() || undefined, code: code.trim() || undefined },
    });
    setBusy(false);
    if (error || !data) return setError(errorMessage(error));
    setMade(data);
  }
  return (
    <Dialog open onOpenChange={(o) => !o && (made ? onSaved() : onClose())}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{made ? "Partner added" : "Add a partner"}</DialogTitle>
          <DialogDescription>{made ? "Send them their links. The short link counts clicks; the campaign link goes straight to your page." : `They join ${program.name} as a customer record, active at once.`}</DialogDescription>
        </DialogHeader>
        {made ? (
          <div className="space-y-3">
            {[["Short link", made.link], ["Campaign link", made.campaign_link]].filter(([, v]) => v).map(([k, v]) => (
              <div key={k} className="space-y-1">
                <p className="text-[13px] font-medium">{k}</p>
                <p className="rounded-md bg-muted px-2.5 py-2 font-mono text-[12px] break-all">{v}</p>
                <CopyButton text={v} />
              </div>
            ))}
            {!program.landing_url && <p className="text-[13px] text-muted-foreground">The program has no landing page yet, so links don't land anywhere. Add one in Settings.</p>}
            <div className="flex justify-end"><Button onClick={onSaved}>Done</Button></div>
          </div>
        ) : (
          <form onSubmit={save} className="space-y-3">
            <Input label="Email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} placeholder="ana@example.com" required autoFocus />
            <Input label="Name" value={name} onChange={(e) => setName(e.target.value)} placeholder="Ana Lima" maxLength={200} />
            <Input label="Ref code" value={code} onChange={(e) => setCode(e.target.value)} placeholder="ana" maxLength={64} hint="What their links carry. Made from their name when left empty. It can't change later." />
            <ErrorText>{error}</ErrorText>
            <div className="flex justify-end gap-2">
              <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
              <Button disabled={busy || !email.trim()}>Add partner</Button>
            </div>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}

function Conversions({ ws, program }: { ws: string; program: PartnerProgram }) {
  const get = useCallback(async (before?: string): Promise<Page<PartnerConversion>> => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/partner-conversions", { params: { path: { ws }, query: { program: program.id, limit: pageSize, ...(before ? { before } : {}) } } });
    return { list: data?.data ?? [], next: data?.next_before ?? null, error: error ? errorMessage(error) : "" };
  }, [ws, program.id]);
  const { list, next, error, reload, more } = usePages(get);
  useEffect(() => {
    reload();
  }, [reload]);
  if (!list) return <Loading />;
  if (list.length === 0) {
    return (
      <Empty title="No conversions yet">
        Have your backend report every sign-up, sale and refund with POST /v1/workspaces/{"{ws}"}/partner-conversions, using a token with the partners.conversions scope, or use the postback URL in Settings. Outflare credits the referred ones.
      </Empty>
    );
  }
  const columns: ColumnDef<PartnerConversion>[] = [
    { id: "type", header: "Type", accessorFn: (v) => v.event_name || v.type, cell: ({ row: { original: v } }) => <Badge tone={v.type === "refund" ? "red" : v.type === "sale" ? "green" : "grey"}>{v.event_name || v.type}{v.recurring ? " (renewal)" : ""}</Badge> },
    { id: "amount", header: "Amount", accessorFn: (v) => v.amount_cents, cell: ({ row: { original: v } }) => (v.type === "signup" ? "" : money(v.type === "refund" ? -v.amount_cents : v.amount_cents, v.currency)) },
    { id: "external", header: "Your id", accessorFn: (v) => v.external_id, cell: ({ getValue }) => <span className="font-mono text-[12px]">{getValue() as string}</span> },
    { id: "product", header: "Product", accessorFn: (v) => v.product_id },
    { id: "source", header: "From", accessorFn: (v) => v.source },
    { id: "when", header: "When", accessorFn: (v) => v.occurred_at, cell: ({ row: { original: v } }) => ago(v.occurred_at) },
  ];
  return (
    <div className="space-y-3">
      <ErrorText>{error}</ErrorText>
      <DataTable columns={columns} data={list} rowId={(v) => v.id} searchPlaceholder="Search conversions" empty="No conversions match." />
      <LoadMore next={next} onMore={more} shown={list.length} />
    </div>
  );
}

function Rewards({ ws, program, canManage }: { ws: string; program: PartnerProgram; canManage: boolean }) {
  const [status, setStatus] = useState<PartnerReward["status"] | "">("");
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState(false);
  const get = useCallback(async (before?: string): Promise<Page<PartnerReward>> => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/partner-rewards", { params: { path: { ws }, query: { program: program.id, limit: pageSize, ...(status ? { status } : {}), ...(before ? { before } : {}) } } });
    return { list: data?.data ?? [], next: data?.next_before ?? null, error: error ? errorMessage(error) : "" };
  }, [ws, program.id, status]);
  const { list, next, error: listError, reload, more } = usePages(get);
  const load = useCallback(async () => {
    await reload();
    setPicked(new Set());
  }, [reload]);
  useEffect(() => {
    load();
  }, [load]);
  async function act(w: PartnerReward, what: "approve" | "reject") {
    let note = "";
    if (what === "reject") {
      const r = await ask({ title: "Reject this reward?", body: "Say why; the partner can see it.", input: { label: "Reason", maxLength: 2000, optional: true }, choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Reject", value: "ok", variant: "danger-solid" }] });
      if (r?.choice !== "ok") return;
      note = r.value.trim();
    }
    const { error } = what === "approve"
      ? await api.POST("/v1/workspaces/{ws}/partner-rewards/{id}/approve", { params: { path: { ws, id: w.id } }, body: {} })
      : await api.POST("/v1/workspaces/{ws}/partner-rewards/{id}/reject", { params: { path: { ws, id: w.id } }, body: { note } });
    if (error) return toast(errorMessage(error), "error");
    toast(what === "approve" ? "Approved." : "Rejected.");
    load();
  }
  // many approves or rejects the picked rewards, or every pending one in
  // the program when none are picked, a thousand at a time.
  async function many(what: "approve" | "reject") {
    const ids = [...picked];
    const which = ids.length > 0 ? plural(ids.length, "reward") : "every pending reward in this program";
    const r = await ask({
      title: what === "approve" ? `Approve ${which}?` : `Reject ${which}?`,
      body: what === "approve" ? "Cash counts toward the next payouts and is logged as a cost; actions are sent to your system." : "Say why; the partners can see it.",
      ...(what === "reject" ? { input: { label: "Reason", maxLength: 2000, optional: true } } : {}),
      choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: what === "approve" ? "Approve" : "Reject", value: "ok", variant: what === "approve" ? undefined : "danger-solid" }],
    });
    if (r?.choice !== "ok") return;
    setBusy(true);
    let done = 0, skipped = 0;
    for (;;) {
      const { data, error } = await api.POST("/v1/workspaces/{ws}/partner-rewards/bulk", {
        params: { path: { ws } },
        body: { action: what, ...(ids.length > 0 ? { ids } : { program: program.id }), ...(what === "reject" && r.value.trim() ? { note: r.value.trim() } : {}) },
      });
      if (error || !data) {
        setBusy(false);
        return toast(errorMessage(error), "error");
      }
      done += data.done;
      skipped += data.skipped.length;
      if (!data.more || data.done === 0) break;
    }
    setBusy(false);
    toast(`${what === "approve" ? "Approved" : "Rejected"} ${plural(done, "reward")}${skipped ? `; ${skipped} weren't pending` : ""}.`);
    load();
  }
  const toggle = (id: string) => setPicked((cur) => {
    const next = new Set(cur);
    if (!next.delete(id)) next.add(id);
    return next;
  });

  if (!list) return <Loading />;
  const pending = list.filter((w) => w.status === "pending");
  const columns: ColumnDef<PartnerReward>[] = [
    ...(canManage ? [{
      id: "pick", enableSorting: false,
      header: () => pending.length > 0 && (
        <input type="checkbox" aria-label="Pick every pending reward" checked={picked.size > 0 && picked.size === pending.length}
          onChange={(e) => setPicked(e.target.checked ? new Set(pending.map((w) => w.id)) : new Set())} />
      ),
      cell: ({ row: { original: w } }: { row: { original: PartnerReward } }) => w.status === "pending" && (
        <input type="checkbox" aria-label="Pick this reward" checked={picked.has(w.id)} onChange={() => toggle(w.id)} />
      ),
    } as ColumnDef<PartnerReward>] : []),
    { id: "kind", header: "Reward", accessorFn: (w) => w.kind, cell: ({ row: { original: w } }) => (w.action ? `${w.action}${w.side === "referred" ? " (friend)" : ""}` : w.kind) },
    { id: "amount", header: "Amount", accessorFn: (w) => w.amount_cents, cell: ({ row: { original: w } }) => (w.kind === "action" ? "" : money(w.amount_cents, w.currency)) },
    { id: "status", header: "Status", accessorFn: (w) => w.status, cell: ({ row: { original: w } }) => <Badge tone={rewardTone[w.status]}>{w.status}</Badge> },
    { id: "hold", header: "Held until", accessorFn: (w) => w.hold_until, cell: ({ row: { original: w } }) => (w.status === "pending" ? new Date(w.hold_until).toLocaleDateString() : "") },
    { id: "note", header: "Note", accessorFn: (w) => w.note },
    { id: "created", header: "Made", accessorFn: (w) => w.created_at, cell: ({ row: { original: w } }) => ago(w.created_at) },
    ...(canManage ? [{
      id: "actions", header: "", enableSorting: false,
      cell: ({ row: { original: w } }: { row: { original: PartnerReward } }) => w.status === "pending" && (
        <Menu aria="Reward actions" items={[{ label: "Approve now", onClick: () => act(w, "approve") }, { label: "Reject", onClick: () => act(w, "reject") }]} />
      ),
    } as ColumnDef<PartnerReward>] : []),
  ];
  return (
    <div className="space-y-3">
      <ErrorText>{listError}</ErrorText>
      <DataTable
        columns={columns}
        data={list}
        rowId={(w) => w.id}
        searchPlaceholder="Search rewards"
        toolbar={
          <span className="flex flex-wrap items-center gap-2">
            <select aria-label="Status" className={field} value={status} onChange={(e) => setStatus(e.target.value as PartnerReward["status"] | "")}>
              <option value="">Every status</option>
              {(["pending", "approved", "paid", "fulfilled", "rejected"] as const).map((s) => <option key={s} value={s}>{s}</option>)}
            </select>
            {canManage && (picked.size > 0 || pending.length > 0) && (
              <>
                <Button variant="ghost" disabled={busy} onClick={() => many("approve")}>{picked.size > 0 ? `Approve ${picked.size}` : "Approve all pending"}</Button>
                <Button variant="ghost" disabled={busy} onClick={() => many("reject")}>{picked.size > 0 ? `Reject ${picked.size}` : "Reject all pending"}</Button>
              </>
            )}
          </span>
        }
        empty="No rewards yet. They appear when a referred customer signs up or buys."
      />
      <LoadMore next={next} onMore={more} shown={list.length} />
    </div>
  );
}

function Payouts({ ws, program, canManage }: { ws: string; program: PartnerProgram; canManage: boolean }) {
  const get = useCallback(async (before?: string): Promise<Page<PartnerPayout>> => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/partner-payouts", { params: { path: { ws }, query: { program: program.id, limit: pageSize, ...(before ? { before } : {}) } } });
    return { list: data?.data ?? [], next: data?.next_before ?? null, error: error ? errorMessage(error) : "" };
  }, [ws, program.id]);
  const { list, next, error: listError, reload: load, more } = usePages(get);
  useEffect(() => {
    load();
  }, [load]);
  async function makeDue() {
    const { data, error } = await api.POST("/v1/workspaces/{ws}/partner-payouts", { params: { path: { ws } }, body: { program: program.id } });
    if (error) return toast(errorMessage(error), "error");
    const n = data?.data.length ?? 0;
    const waiting = data?.waiting_for_details ? ` ${plural(data.waiting_for_details, "partner")} still need to add payout details on the portal; theirs wait.` : "";
    toast((n ? `${plural(n, "payout")} due. Pay them from your own account, then mark each paid.` : `Nobody's balance has reached the ${money(program.min_payout_cents, program.currency)} minimum yet.`) + waiting);
    load();
  }
  // download saves the due payouts, with each partner's payout details, as
  // CSV for paying them from your own account or a payout tool.
  async function download() {
    const { data, error, response } = await api.GET("/v1/workspaces/{ws}/partner-payouts/export", {
      params: { path: { ws }, query: { program: program.id, status: "due" } }, parseAs: "text",
    });
    if (error || typeof data !== "string") return toast(errorMessage(error), "error");
    const url = URL.createObjectURL(new Blob([data], { type: response.headers.get("Content-Type") ?? "text/csv" }));
    const a = document.createElement("a");
    a.href = url;
    a.download = `${program.slug}-due-payouts.csv`;
    a.click();
    URL.revokeObjectURL(url);
  }
  async function paid(o: PartnerPayout) {
    const r = await ask({ title: `Mark ${money(o.amount_cents, o.currency)} paid`, body: "Only once you've paid it. The reference is the payment's id, such as a PayPal or Wise transfer id.", input: { label: "Reference", maxLength: 200, optional: true }, choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Mark paid", value: "ok" }] });
    if (r?.choice !== "ok") return;
    const { error } = await api.POST("/v1/workspaces/{ws}/partner-payouts/{id}/paid", { params: { path: { ws, id: o.id } }, body: { reference: r.value.trim() } });
    if (error) return toast(errorMessage(error), "error");
    toast("Marked paid.");
    load();
  }
  async function cancel(o: PartnerPayout) {
    const { error } = await api.POST("/v1/workspaces/{ws}/partner-payouts/{id}/cancel", { params: { path: { ws, id: o.id } }, body: {} });
    if (error) return toast(errorMessage(error), "error");
    toast("Canceled; its rewards are back in the balance.");
    load();
  }
  if (!list) return <Loading />;
  const columns: ColumnDef<PartnerPayout>[] = [
    { id: "partner", header: "Partner", accessorFn: (o) => o.partner_name },
    { id: "amount", header: "Amount", accessorFn: (o) => o.amount_cents, cell: ({ row: { original: o } }) => money(o.amount_cents, o.currency) },
    { id: "status", header: "Status", accessorFn: (o) => o.status, cell: ({ row: { original: o } }) => <Badge tone={o.status === "paid" ? "green" : o.status === "due" ? "amber" : "grey"}>{o.status}</Badge> },
    { id: "reference", header: "Reference", accessorFn: (o) => o.reference, cell: ({ getValue }) => <span className="font-mono text-[12px]">{getValue() as string}</span> },
    { id: "when", header: "Made", accessorFn: (o) => o.created_at, cell: ({ row: { original: o } }) => (o.paid_at ? `paid ${ago(o.paid_at)}` : ago(o.created_at)) },
    ...(canManage ? [{
      id: "actions", header: "", enableSorting: false,
      cell: ({ row: { original: o } }: { row: { original: PartnerPayout } }) => o.status === "due" && (
        <Menu aria="Payout actions" items={[{ label: "Mark paid", onClick: () => paid(o) }, { label: "Cancel", onClick: () => cancel(o) }]} />
      ),
    } as ColumnDef<PartnerPayout>] : []),
  ];
  return (
    <div className="space-y-3">
      <p className="text-[13px] text-muted-foreground">Outflare never holds or sends money. Make the payouts that are due, pay them from your own account or payout tool (the partner_payout.created webhook can start it), then mark each paid with its reference.</p>
      <DataTable
        columns={columns}
        data={list}
        rowId={(o) => o.id}
        searchPlaceholder="Search payouts"
        toolbar={canManage && (
          <span className="flex gap-2">
            <Button onClick={makeDue}>Make due payouts</Button>
            <Button variant="ghost" onClick={download}>Download CSV</Button>
          </span>
        )}
        empty="No payouts yet."
      />
      <ErrorText>{listError}</ErrorText>
      <LoadMore next={next} onMore={more} shown={list.length} />
      <ErasePayoutNotes ws={ws} canManage={canManage} />
    </div>
  );
}

// ErasePayoutNotes is the workspace's one partner setting: whether deleting
// a partner's record for good also clears their payouts' notes and
// references. It holds for every program, so it says so.
function ErasePayoutNotes({ ws, canManage }: { ws: string; canManage: boolean }) {
  const [on, setOn] = useState<boolean | null>(null);
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/partner-settings", { params: { path: { ws } } }).then(({ data }) => setOn(data?.erase_payout_notes ?? null));
  }, [ws]);
  async function change(v: boolean) {
    setOn(v);
    const { error } = await api.PATCH("/v1/workspaces/{ws}/partner-settings", { params: { path: { ws } }, body: { erase_payout_notes: v } });
    if (error) {
      setOn(!v);
      return toast(errorMessage(error), "error");
    }
    toast(v ? "Deleting a partner now clears their payout notes too." : "Deleting a partner now keeps their payout notes.");
  }
  if (on === null) return null;
  return (
    <label className="flex items-start gap-2 border-t pt-3 text-[13px]">
      <input type="checkbox" className="mt-0.5" checked={on} disabled={!canManage} onChange={(e) => change(e.target.checked)} />
      <span>
        Clear payout notes when a partner is deleted <span className="text-muted-foreground">(every program)</span>
        <span className="block text-xs text-muted-foreground">Deleting a partner for good always keeps their payouts and their links. This also clears each payout's note and reference, which can hold their name or email.</span>
      </span>
    </label>
  );
}

function NewProgram({ ws, onClose, onSaved }: { ws: string; onClose: () => void; onSaved: (p: PartnerProgram) => void }) {
  const { active: brands } = useBrand(ws);
  const [kind, setKind] = useState<Kind>("affiliate");
  const [name, setName] = useState("");
  const [brand, setBrand] = useState("");
  const [landing, setLanding] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/partner-programs", {
      params: { path: { ws } },
      body: { name: name.trim(), kind, brand: brand || undefined, landing_url: landing.trim() || undefined },
    });
    setBusy(false);
    if (error || !data) return setError(errorMessage(error));
    toast(`Made ${data.name}. Add partners, then have your backend report conversions.`);
    onSaved(data);
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>New partner program</DialogTitle>
          <DialogDescription>Pick a preset; every setting it fills in can be changed afterwards.</DialogDescription>
        </DialogHeader>
        <form onSubmit={save} className="space-y-3">
          <div className="grid gap-2">
            {kinds.map((k) => (
              <label key={k.id} className={`flex cursor-pointer gap-3 rounded-md border p-3 text-[13px] ${kind === k.id ? "border-primary" : ""}`}>
                <input type="radio" name="kind" checked={kind === k.id} onChange={() => setKind(k.id)} className="mt-0.5" />
                <span><span className="block font-medium">{k.label}</span><span className="block text-muted-foreground">{k.blurb}</span></span>
              </label>
            ))}
          </div>
          <Input label="Name" value={name} onChange={(e) => setName(e.target.value)} placeholder="Northpeak affiliates" maxLength={120} required />
          {brands.length > 0 && (
            <label className="block space-y-1 text-[13px]">
              <span className="font-medium">Brand</span>
              <select className={`${field} w-full`} value={brand} onChange={(e) => setBrand(e.target.value)}>
                <option value="">No brand</option>
                {brands.map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}
              </select>
            </label>
          )}
          <Input label="Landing page" type="url" value={landing} onChange={(e) => setLanding(e.target.value)} placeholder="https://example.com/pricing" maxLength={2000} hint="Where partner links land." />
          <ErrorText>{error}</ErrorText>
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
            <Button disabled={busy || !name.trim()}>Make program</Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function ProgramSettings({ ws, program: p, canManage, onClose }: { ws: string; program: PartnerProgram; canManage: boolean; onClose: () => void }) {
  const [landing, setLanding] = useState(p.landing_url);
  const [windowDays, setWindowDays] = useState(String(p.credit_window_days));
  const [model, setModel] = useState(p.credit_model);
  const [hold, setHold] = useState(String(p.hold_days));
  const [auto, setAuto] = useState(p.auto_approve);
  const [payBrowser, setPayBrowser] = useState(p.pay_browser_signups);
  const [minPayout, setMinPayout] = useState(String(p.min_payout_cents / 100));
  const [salesSource, setSalesSource] = useState(p.sales_source);
  const [postback, setPostback] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const { error } = await api.PATCH("/v1/workspaces/{ws}/partner-programs/{id}", {
      params: { path: { ws, id: p.id } },
      body: { landing_url: landing.trim(), credit_window_days: Number(windowDays), credit_model: model, hold_days: Number(hold), auto_approve: auto, pay_browser_signups: payBrowser, min_payout_cents: Math.round(Number(minPayout) * 100), sales_source: salesSource },
    });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    toast("Saved.");
    onClose();
  }
  async function setStatus(status: PartnerProgram["status"]) {
    const { error } = await api.PATCH("/v1/workspaces/{ws}/partner-programs/{id}", { params: { path: { ws, id: p.id } }, body: { status } });
    if (error) return toast(errorMessage(error), "error");
    toast(status === "active" ? "Program active." : status === "paused" ? "Paused: nothing new is credited; links still land." : "Archived.");
    onClose();
  }
  async function makePostback() {
    const { data, error } = await api.POST("/v1/workspaces/{ws}/partner-programs/{id}/postback", { params: { path: { ws, id: p.id } } });
    if (error || !data) return toast(errorMessage(error), "error");
    setPostback(data.url);
  }

  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>{p.name}</SheetTitle>
          <SheetDescription>The program's own credit rule, rewards and payouts. They never follow the workspace's attribution settings.</SheetDescription>
        </SheetHeader>
        <form onSubmit={save} className="space-y-6 px-4 pb-6">
          <fieldset disabled={!canManage} className="space-y-3">
            <Input label="Landing page" type="url" value={landing} onChange={(e) => setLanding(e.target.value)} placeholder="https://example.com/pricing" maxLength={2000} />
            <div className="grid gap-3 sm:grid-cols-2">
              <Input label="Credit window (days)" type="number" min={1} max={365} value={windowDays} onChange={(e) => setWindowDays(e.target.value)} hint="How long after a visit a sale still counts." />
              <label className="block space-y-1 text-[13px]">
                <span className="font-medium">Which visit wins</span>
                <select className={`${field} w-full`} value={model} onChange={(e) => setModel(e.target.value as "first" | "last")}>
                  <option value="last">The last partner's</option>
                  <option value="first">The first partner's</option>
                </select>
              </label>
              <Input label="Hold (days)" type="number" min={0} max={365} value={hold} onChange={(e) => setHold(e.target.value)} hint="Refunds inside it cancel the reward." />
              <Input label={`Minimum payout (${p.currency})`} type="number" min={0} step="0.01" value={minPayout} onChange={(e) => setMinPayout(e.target.value)} />
            </div>
            <label className="flex items-center gap-2 text-[13px]">
              <input type="checkbox" checked={auto} onChange={(e) => setAuto(e.target.checked)} /> Approve rewards on their own when the hold ends
            </label>
            <label className="flex items-center gap-2 text-[13px]">
              <input type="checkbox" checked={payBrowser} onChange={(e) => setPayBrowser(e.target.checked)} /> Pay for sign-ups the browser reports (outflare.signup). Off: they make the lead only, since anyone can call it
            </label>
            <label className="flex items-center gap-2 text-[13px]">
              <input type="checkbox" checked={salesSource === "stripe"} onChange={(e) => setSalesSource(e.target.checked ? "stripe" : "")} /> Also take sales and refunds from connected Stripe
            </label>
          </fieldset>
          <p className="text-[12px] text-muted-foreground">Rewards are flows: set them on the Flows tab.</p>
          <ErrorText>{error}</ErrorText>
          {canManage && <Button disabled={busy}>Save</Button>}
          <div className="space-y-2 border-t pt-4">
            <h3 className="text-[13px] font-medium">Postback URL</h3>
            <p className="text-[12px] text-muted-foreground">For trackers and backends that can only call a URL: ?type=sale&ref=…&user_id=…&amount=49.90&order_id=…. Anyone with it can report conversions, so keep it private. {p.postback_hint && `The current one starts ${p.postback_hint}; making a new one retires it.`}</p>
            {postback ? (
              <div className="space-y-2 rounded-md border p-3">
                <p className="font-mono text-[12px] break-all">{postback}</p>
                <CopyButton text={postback} label="Copy URL" />
                <p className="text-[12px] text-muted-foreground">Shown once.</p>
              </div>
            ) : canManage && <Button type="button" variant="ghost" onClick={makePostback}>{p.postback_hint ? "Make a new postback URL" : "Make a postback URL"}</Button>}
          </div>
          {canManage && (
            <div className="flex gap-2 border-t pt-4">
              {p.status === "active" ? <Button type="button" variant="ghost" onClick={() => setStatus("paused")}>Pause</Button> : <Button type="button" variant="ghost" onClick={() => setStatus("active")}>Resume</Button>}
              <Button type="button" variant="ghost" onClick={() => setStatus("archived")}>Archive</Button>
            </div>
          )}
        </form>
        <PartnerInstall ws={ws} program={p} />
        <PortalSection ws={ws} program={p} canManage={canManage} />
        <ImportSection ws={ws} program={p} canManage={canManage} />
      </SheetContent>
    </Sheet>
  );
}

// The hosted portal: where partners apply, sign in by email and see their
// links, referred customers (masked), rewards and payouts.
const importReasons: Record<string, string> = {
  incomplete: "missing its id or partner",
  no_email: "no email address",
  link_param: "a link parameter other than ref or via, which the website script doesn't read",
  extra_link: "a partner's second link (their code is the first)",
  code_changed: "the code was taken or not allowed, so the partner got a new one",
  partner_missing: "its partner didn't come over",
  partner_unavailable: "a partner other objects name, but Tolt couldn't give them either",
  customer_missing: "its customer didn't come over",
  promo_code_invalid: "a promo code with characters not allowed here",
  promo_code_taken: "a promo code another partner has here",
  already_referred: "another partner already has this customer here",
  status_conflict: "rejected in Tolt after it was approved here",
  unreadable: "Tolt's answer couldn't be read, so this kind didn't come over; the others did",
  partner_id_missing: "a promo code that names no partner where Outflare reads it: send us the fields below",
  partners_unlisted: "Tolt reported more partners than came over",
};

type ImportSource = { id: string; name: string; currency: string; status: string };

// ImportSection brings a Tolt program in, as often as needed until the
// switch-over day.
function ImportSection({ ws, program: p, canManage }: { ws: string; program: PartnerProgram; canManage: boolean }) {
  const [runs, setRuns] = useState<PartnerImport[]>([]);
  const [source, setSource] = useState("");
  // The programs in the Tolt account, or null when they couldn't be listed
  // (then the id is typed).
  const [sources, setSources] = useState<ImportSource[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ReactNode>("");
  const load = useCallback(async () => {
    const { data } = await api.GET("/v1/workspaces/{ws}/partner-programs/{id}/imports", { params: { path: { ws, id: p.id } } });
    setRuns(data?.data ?? []);
    return data?.data ?? [];
  }, [ws, p.id]);
  useEffect(() => {
    (async () => {
      const past = await load();
      if (!canManage) return;
      const { data } = await api.GET("/v1/workspaces/{ws}/partner-programs/{id}/import-sources", { params: { path: { ws, id: p.id } } });
      if (!data) return;
      setSources(data.data);
      // The program the last run read, else the one an import would take.
      const last = past.find((r) => r.source_program)?.source_program;
      setSource(data.data.some((s) => s.id === last) ? last! : data.suggested);
    })();
  }, [load, canManage, ws, p.id]);
  const running = runs.some((r) => r.status === "queued" || r.status === "running");
  useEffect(() => {
    if (!running) return;
    const t = setInterval(load, 3000);
    return () => clearInterval(t);
  }, [running, load]);

  async function start() {
    setBusy(true);
    setError("");
    const { error } = await api.POST("/v1/workspaces/{ws}/partner-programs/{id}/imports", {
      params: { path: { ws, id: p.id } }, body: source.trim() ? { source_program: source.trim() } : {},
    });
    setBusy(false);
    if (error) {
      if ((error as { error?: { code?: string } }).error?.code === "connector_needed") {
        return setError(<>Connect Tolt first, with your own API key: <a className="underline underline-offset-2" href={appPath(ws, "connectors", "tolt")}>Connectors, Tolt</a>.</>);
      }
      return setError(errorMessage(error));
    }
    toast("Import started.");
    load();
  }

  return (
    <div className="space-y-3 border-t px-4 pt-4 pb-6">
      <h3 className="text-[13px] font-medium">Import from Tolt</h3>
      <p className="text-[12px] text-muted-foreground">Brings partners with their link codes (so their links keep working), payout details, promo codes, referred customers, transactions and commissions, paid ones as paid payouts. Run it as often as you like until you switch: each run adds only what's new and brings over status changes. Keep auto-approve off until then, so Tolt's own decisions come over.</p>
      {canManage && (
        <div className="flex flex-wrap items-end gap-2">
          {sources && sources.length > 1 ? (
            <label className="grid gap-1 text-[12px]">
              Tolt program
              <select className={field} value={source} onChange={(e) => setSource(e.target.value)}>
                {!source && <option value="">Choose a program</option>}
                {sources.map((s) => (
                  <option key={s.id} value={s.id}>{s.name || s.id}{s.status === "inactive" ? " (inactive)" : ""}{s.currency ? `, ${s.currency}` : ""}</option>
                ))}
              </select>
            </label>
          ) : sources?.length === 1 ? (
            <p className="text-[12px] text-muted-foreground">From your Tolt program {sources[0].name || sources[0].id}.</p>
          ) : (
            <Input label="Tolt program id" value={source} onChange={(e) => setSource(e.target.value)} placeholder="Only if your account has more than one" maxLength={200} />
          )}
          <Button type="button" onClick={start} disabled={busy || running || (!!sources && sources.length > 1 && !source)}>{running ? "Importing…" : runs.length ? "Run again" : "Import"}</Button>
        </div>
      )}
      <ErrorText>{error}</ErrorText>
      {runs.slice(0, 5).map((r) => (
        <div key={r.id} className="space-y-1 rounded-md border p-3 text-[12px]">
          <div className="flex items-center gap-2">
            <Badge tone={r.status === "failed" ? "red" : r.status === "done" ? "green" : "blue"}>{r.status}</Badge>
            <span className="text-muted-foreground">{ago(r.created_at)}</span>
          </div>
          {r.error && <p className="text-destructive">{r.error}</p>}
          <p className="text-muted-foreground">
            {r.summary.kinds.filter((k) => k.kind !== "links" && (k.seen > 0 || k.reported)).map((k) => `${k.kind.replace("_", " ")}: ${k.seen} read${k.reported ? ` of ${k.reported} Tolt reports` : ""}, ${k.created} new, ${k.updated} updated${k.skipped ? `, ${k.skipped} skipped` : ""}`).join(" · ") || "Nothing read yet."}
          </p>
          {r.summary.issues.length > 0 && (
            <details>
              <summary className="cursor-pointer">{plural(r.summary.issues.length + r.summary.more_issues, "thing", "things")} to check</summary>
              <ul className="mt-1 list-disc pl-5">
                {r.summary.issues.map((i) => (
                  <li key={i.kind + i.external_id + i.reason}>
                    {i.kind.replace("_", " ")} {i.external_id && <code>{i.external_id}</code>}: {importReasons[i.reason] ?? i.reason}
                    {i.message && <span className="block text-muted-foreground">{i.message}</span>}
                  </li>
                ))}
              </ul>
            </details>
          )}
        </div>
      ))}
    </div>
  );
}

function PortalSection({ ws, program: p, canManage }: { ws: string; program: PartnerProgram; canManage: boolean }) {
  const [enabled, setEnabled] = useState(p.portal_enabled);
  const [sub, setSub] = useState(p.portal_subdomain ?? "");
  const [title, setTitle] = useState(p.portal.title ?? "");
  const [color, setColor] = useState(p.portal.color || "#111827");
  const [welcome, setWelcome] = useState(p.portal.welcome ?? "");
  const [from, setFrom] = useState(p.portal.email_from ?? "");
  const [signup, setSignup] = useState(!!p.portal.signup_open);
  const [url, setUrl] = useState(p.portal_url);
  const [assets, setAssets] = useState<PartnerAsset[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const loadAssets = useCallback(async () => {
    const { data } = await api.GET("/v1/workspaces/{ws}/partner-programs/{id}/assets", { params: { path: { ws, id: p.id } } });
    setAssets(data?.data ?? []);
  }, [ws, p.id]);
  useEffect(() => {
    loadAssets();
  }, [loadAssets]);

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const { data, error } = await api.PUT("/v1/workspaces/{ws}/partner-programs/{id}/portal", {
      params: { path: { ws, id: p.id } },
      body: { enabled, subdomain: sub.trim(), settings: { ...p.portal, title: title.trim(), color, welcome: welcome.trim(), email_from: from.trim(), signup_open: signup } },
    });
    setBusy(false);
    if (error || !data) return setError(errorMessage(error));
    setUrl(data.portal_url);
    toast(enabled ? "Portal saved." : "Portal closed.");
  }
  async function addAsset() {
    const r = await ask({ title: "Add an asset", body: "Copy or an email swipe partners can paste. For a banner or a link, put its https address on the first line.", input: { label: "Title", maxLength: 200 }, choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Next", value: "ok" }] });
    if (r?.choice !== "ok" || !r.value.trim()) return;
    const b = await ask({ title: r.value.trim(), input: { label: "Text, or an https address", maxLength: 10000 }, choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Add", value: "ok" }] });
    if (b?.choice !== "ok" || !b.value.trim()) return;
    const v = b.value.trim();
    const isURL = /^https:\/\/\S+$/.test(v);
    const kind = isURL ? (/\.(png|jpe?g|gif|webp|svg)(\?|$)/i.test(v) ? "image" : "link") : "text";
    const { error } = await api.POST("/v1/workspaces/{ws}/partner-programs/{id}/assets", {
      params: { path: { ws, id: p.id } }, body: { kind, title: r.value.trim(), ...(isURL ? { url: v } : { body: v }) },
    });
    if (error) return toast(errorMessage(error), "error");
    loadAssets();
  }
  async function removeAsset(a: PartnerAsset) {
    const { error } = await api.DELETE("/v1/workspaces/{ws}/partner-programs/{id}/assets/{asset}", { params: { path: { ws, id: p.id, asset: a.id } } });
    if (error) return toast(errorMessage(error), "error");
    loadAssets();
  }

  return (
    <div className="space-y-4 border-t px-4 pt-4 pb-6">
      <form onSubmit={save} className="space-y-3">
        <h3 className="text-[13px] font-medium">Partner portal</h3>
        <p className="text-[12px] text-muted-foreground">Where partners apply, sign in with a link emailed through your own email provider, and see their links, referred customers (never their names), rewards and payouts. Point your own domain at it from Domains, Tracking domains. To show customers their referral link inside your own app instead, your backend opens the referral widget (<code>POST …/widget-sessions</code>), which works while the portal is closed.</p>
        <fieldset disabled={!canManage} className="space-y-3">
          <label className="flex items-center gap-2 text-[13px]"><input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} /> Open the portal</label>
          <div className="grid gap-3 sm:grid-cols-2">
            <Input label="Subdomain" value={sub} onChange={(e) => setSub(e.target.value)} placeholder="northpeak" maxLength={63} hint="Used when the server has a partners domain." />
            <Input label="Title" value={title} onChange={(e) => setTitle(e.target.value)} placeholder={p.name} maxLength={120} />
            <Input label="Sender" value={from} onChange={(e) => setFrom(e.target.value)} placeholder="Partners <partners@example.com>" hint="On a verified sending domain. Needed to sign anyone in." />
            <Input label="Colour" type="color" value={color} onChange={(e) => setColor(e.target.value)} />
          </div>
          <Input label="Welcome" value={welcome} onChange={(e) => setWelcome(e.target.value)} maxLength={4000} placeholder="Earn 20% of every sale you send our way." />
          <label className="flex items-center gap-2 text-[13px]"><input type="checkbox" checked={signup} onChange={(e) => setSignup(e.target.checked)} /> Anyone can apply</label>
        </fieldset>
        <ErrorText>{error}</ErrorText>
        <div className="flex flex-wrap items-center gap-2">
          {canManage && <Button disabled={busy}>Save portal</Button>}
          {p.portal_enabled && <a className="text-[13px] underline" href={url} target="_blank" rel="noreferrer">{url}</a>}
        </div>
      </form>
      <div className="space-y-2">
        <h3 className="text-[13px] font-medium">Assets for partners</h3>
        {assets.length === 0 ? <p className="text-[12px] text-muted-foreground">No banners, copy or links yet.</p> : (
          <ul className="divide-y rounded-md border">
            {assets.map((a) => (
              <li key={a.id} className="flex items-center gap-2 px-3 py-2 text-[13px]">
                <Badge tone="grey">{a.kind}</Badge>
                <span className="min-w-0 flex-1 truncate">{a.title}</span>
                {canManage && <Button type="button" variant="ghost" onClick={() => removeAsset(a)}>Remove</Button>}
              </li>
            ))}
          </ul>
        )}
        {canManage && <Button type="button" variant="ghost" onClick={addAsset}>Add an asset</Button>}
      </div>
    </div>
  );
}

// Groups: each partner is in at most one group, new partners join the
// default one, and a group's own rate replaces the program's for its
// partners (a partner's own rate beats it).
function Groups({ ws, program, canManage, onChanged }: { ws: string; program: PartnerProgram; canManage: boolean; onChanged: () => void }) {
  const [p, setP] = useState<PartnerProgram | null>(null);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/partner-programs/{id}", { params: { path: { ws, id: program.id } } });
    if (error || !data) return setError(errorMessage(error));
    setP(data);
  }, [ws, program.id]);
  useEffect(() => {
    load();
  }, [load]);
  if (!p) return error ? <ErrorText>{error}</ErrorText> : <Loading />;

  const rateOf = (g: PartnerGroup) => p.rules.find((r) => r.group_id === g.id && !r.partner_id && r.side === "partner" && r.trigger === "sale" && (r.kind === "percent" || r.kind === "fixed"));
  const done = (msg: string) => {
    toast(msg);
    load();
    onChanged();
  };
  async function create() {
    const r = await ask({ title: "New group", body: "Such as Gold or Agencies. Give it its own rate after.", input: { label: "Name", maxLength: 80 }, choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Add", value: "ok" }] });
    if (r?.choice !== "ok" || !r.value.trim()) return;
    const { error } = await api.POST("/v1/workspaces/{ws}/partner-programs/{id}/groups", { params: { path: { ws, id: p!.id } }, body: { name: r.value.trim(), default: p!.groups.length === 0 } });
    if (error) return toast(errorMessage(error), "error");
    done(p!.groups.length === 0 ? `Added ${r.value.trim()}. New partners join it.` : `Added ${r.value.trim()}.`);
  }
  async function patch(g: PartnerGroup, body: { name?: string; default?: boolean }, msg: string) {
    const { error } = await api.PATCH("/v1/workspaces/{ws}/partner-programs/{id}/groups/{group}", { params: { path: { ws, id: p!.id, group: g.id } }, body });
    if (error) return toast(errorMessage(error), "error");
    done(msg);
  }
  async function rename(g: PartnerGroup) {
    const r = await ask({ title: "Rename group", input: { label: "Name", value: g.name, maxLength: 80 }, choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Save", value: "ok" }] });
    if (r?.choice !== "ok" || !r.value.trim() || r.value.trim() === g.name) return;
    patch(g, { name: r.value.trim() }, `Renamed to ${r.value.trim()}.`);
  }
  async function rate(g: PartnerGroup) {
    const mine = rateOf(g);
    const base = rulesSummary({ ...p!, rules: p!.rules.filter((x) => !x.group_id && !x.partner_id) });
    const r = await ask({
      title: `Commission for ${g.name}`,
      body: `A share of each sale for partners in ${g.name}, instead of the program's (${base}). A partner's own rate still beats it. Leave it empty to use the program's rate.`,
      input: { label: "Percent of each sale", type: "number", value: mine?.kind === "percent" ? String((mine.percent_bp ?? 0) / 100) : "", placeholder: "30", optional: true },
      choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Save", value: "ok" }],
    });
    if (r?.choice !== "ok") return;
    const pct = r.value.trim() === "" ? null : Number(r.value);
    if (pct !== null && !(pct > 0 && pct <= 100)) return toast("Enter a percent from 0.01 to 100.", "error");
    const next = p!.rules.filter((x) => x !== mine).map(toRuleInput);
    if (pct !== null) next.push({ trigger: "sale", side: "partner", kind: "percent", percent_bp: Math.round(pct * 100), recurrence: "every", group_id: g.id });
    const { error } = await api.PUT("/v1/workspaces/{ws}/partner-programs/{id}/rules", { params: { path: { ws, id: p!.id } }, body: { rules: next } });
    if (error) return toast(errorMessage(error), "error");
    done(pct === null ? `${g.name} is back on the program's rate.` : `${g.name} earns ${pct}% of each sale from now on.`);
  }
  async function remove(g: PartnerGroup) {
    const r = await ask({ title: `Remove ${g.name}?`, body: `Its own rules go with it, and its ${plural(g.partners, "partner")} move to no group. Rewards already earned keep their amounts.`, choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Remove", value: "ok", variant: "danger-solid" }] });
    if (r?.choice !== "ok") return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/partner-programs/{id}/groups/{group}", { params: { path: { ws, id: p!.id, group: g.id } } });
    if (error) return toast(errorMessage(error), "error");
    done(`Removed ${g.name}.`);
  }

  const columns: ColumnDef<PartnerGroup>[] = [
    { id: "name", header: "Group", accessorFn: (g) => g.name, cell: ({ row: { original: g } }) => <span className="flex items-center gap-2"><span className="font-medium">{g.name}</span>{g.default && <Badge tone="blue">default</Badge>}</span> },
    { id: "partners", header: "Partners", accessorFn: (g) => g.partners },
    { id: "rate", header: "Rate", accessorFn: (g) => rateOf(g)?.percent_bp ?? 0, cell: ({ row: { original: g } }) => { const r = rateOf(g); return r ? ruleText(r, p.currency) : <span className="text-muted-foreground">The program's</span>; } },
    ...(canManage ? [{
      id: "actions", header: "", enableSorting: false,
      cell: ({ row: { original: g } }: { row: { original: PartnerGroup } }) => (
        <Menu aria={`Actions for ${g.name}`} items={[
          { label: "Commission rate", onClick: () => rate(g) },
          { label: "Rename", onClick: () => rename(g) },
          g.default
            ? { label: "Stop adding new partners here", onClick: () => patch(g, { default: false }, "New partners now join no group.") }
            : { label: "Make default", onClick: () => patch(g, { default: true }, `New partners now join ${g.name}.`) },
          { label: "Remove", onClick: () => remove(g) },
        ]} />
      ),
    } as ColumnDef<PartnerGroup>] : []),
  ];
  return (
    <div className="space-y-3">
      <p className="text-[13px] text-muted-foreground">Each partner is in one group at most. New partners join the default group, and a group's rate replaces the program's for its partners. Move partners from the Partners tab: pick them, then Move to.</p>
      {p.groups.length === 0 ? (
        <Empty title="No groups yet" action={canManage && <Button onClick={create}>New group</Button>}>
          Groups give some partners a different rate, such as Gold for your best affiliates or Agencies.
        </Empty>
      ) : (
        <DataTable columns={columns} data={p.groups} rowId={(g) => g.id} searchPlaceholder="Search groups" toolbar={canManage && <Button onClick={create}>New group</Button>} empty="No groups match." />
      )}
    </div>
  );
}

// RecordPartners shows a customer record's place in partner programs: the
// programs they are a partner in and the partner who referred them. It
// shows nothing when there is neither, or the viewer can't read partners.
export function RecordPartners({ ws, record, go }: { ws: string; record: string; go?: () => void }) {
  const [own, setOwn] = useState<Partner[] | null>(null);
  const [by, setBy] = useState<{ ref: PartnerReferral; partner: Partner | null }[]>([]);

  useEffect(() => {
    let live = true;
    (async () => {
      const [mine, refs] = await Promise.all([
        api.GET("/v1/workspaces/{ws}/partners", { params: { path: { ws }, query: { record_id: record } } }),
        api.GET("/v1/workspaces/{ws}/partner-referrals", { params: { path: { ws }, query: { record_id: record } } }),
      ]);
      const list = refs.data?.data ?? [];
      const partners = await Promise.all(list.map((ref) =>
        api.GET("/v1/workspaces/{ws}/partners/{id}", { params: { path: { ws, id: ref.partner_id } } }).then(({ data }) => ({ ref, partner: data ?? null })),
      ));
      if (!live) return;
      setOwn(mine.data?.data ?? []);
      setBy(partners);
    })();
    return () => { live = false; };
  }, [ws, record]);

  if (own === null || (own.length === 0 && by.length === 0)) return null;
  return (
    <div className="space-y-2">
      <div className="font-medium">Partners</div>
      {own.map((p) => (
        <div key={p.id} className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5">
          <span className="min-w-0">Partner in <span className="font-medium">{p.program}</span> as <span className="font-mono">{p.code}</span> <Badge tone={partnerTone[p.status]}>{p.status}</Badge></span>
          <span className="shrink-0 text-muted-foreground">{money(p.stats.sales_cents, p.currency)} sales · {money(p.stats.balance_cents, p.currency)} due</span>
        </div>
      ))}
      {by.map(({ ref, partner }) => (
        <div key={ref.id} className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5">
          <span className="min-w-0">Referred by {partner ? <><span className="font-mono">{partner.code}</span> ({partner.name || partner.email || "partner"}) in <span className="font-medium">{partner.program}</span></> : "a partner"}</span>
          <span className="shrink-0 text-muted-foreground">{ref.status}, {ago(ref.created_at)}</span>
        </div>
      ))}
      {go && <Button variant="ghost" onClick={go}>Open partners</Button>}
    </div>
  );
}
