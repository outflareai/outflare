import { PartnerProposals } from "./PartnerProposals";
import { pickedBrandId } from "@/lib/brand";
import { PartnerCouponDialog } from "./PartnerCoupons";
// Partners: customer referral, affiliate and influencer programs on one
// platform. A program holds the rules; partners are customer records with a
// ref code, links and promo codes; the workspace's backend reports
// conversions; rewards are a ledger, and payouts are marked paid once the
// workspace has paid them from its own account.

import { Settings2 } from "lucide-react";
import { useCallback, useEffect, useRef, useState, type Dispatch, type FormEvent, type SetStateAction } from "react";
import { isID, type Tab } from "@/lib/paths";
import { track } from "@/lib/analytics";
import type { Partner, PartnerGroup, PartnerStats, PartnerConversionLogEntry, PartnerLead, PartnerPayout, PartnerProgram, PartnerReferral, PartnerReward, PartnerRuleInput, paths } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { useBrand } from "@/lib/brand";
import { Badge, Button, CopyButton, Empty, ErrorText, field, Input, Loading, Menu, PageHeader, Stat, Stats, ask, toast } from "../ui";
import { ago, plural } from "../format";
import { DataTable, type ColumnDef } from "@/components/data-table";
import { Flows } from "./PartnerFlows";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { PartnerTasks } from "./PartnerTasks";
import { PartnerReview } from "./PartnerReview";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { ProgramSettings, settingsTabs } from "./PartnerSettings";
import { AppLink, PartnerPage, partnerActions, partnerName, type Section as PartnerSection } from "./PartnerPage";
import { ConversionList, RewardList } from "./PartnerLedger";
import { downloadProof, payoutProofName, platformName, saveFile } from "./PartnerProof";

type Go = (t: Tab, item?: string) => void;
type Props = { ws: string; canManage: boolean; open: string; onOpen: (id: string) => void; go: Go };
type PartnerSort = NonNullable<NonNullable<paths["/v1/brands/{brand}/partners"]["get"]["parameters"]["query"]>["sort"]>;
type PartnerFind = { q: string; sort: PartnerSort };
type Section = "proposals" | "tasks" | "partners" | "groups" | "flows" | "leads" | "conversions" | "log" | "review" | "rewards" | "payouts";
type Kind = PartnerProgram["kind"];

const kinds: { id: Kind; label: string; blurb: string }[] = [
  { id: "affiliate", label: "Sales commissions", blurb: "Marketers and review sites earn a share of each sale (20% to start), held 30 days for refunds and paid above a minimum." },
  { id: "referral", label: "Customer rewards", blurb: "Customers share a link; on the friend's first sale both get a reward your own product grants, such as credit." },
  { id: "influencer", label: "Creator partnerships", blurb: "Track what creators' links and codes bring. Log their fixed fees so cost per sale works." },
];

export const money = (cents: number, currency: string) => {
  try {
    return new Intl.NumberFormat(undefined, { style: "currency", currency, maximumFractionDigits: Math.abs(cents) < 100000 ? 2 : 0 }).format(cents / 100);
  } catch {
    return `${(cents / 100).toFixed(2)} ${currency}`;
  }
};

export const partnerTone = { active: "green", pending: "amber", suspended: "red", rejected: "grey" } as const;
export const rewardTone = { pending: "amber", approved: "blue", paid: "green", fulfilled: "green", rejected: "grey" } as const;

export function Partners({ ws, canManage, open, onOpen, go }: Props) {
  const { query: brandQuery } = useBrand(ws);
  const [programs, setPrograms] = useState<PartnerProgram[] | null>(null);
  const [program, setProgram] = useState("");
  const [section, setSection] = useState<Section>("partners");
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState("");
  const [find, setFind] = useState<PartnerFind>({ q: "", sort: "newest" });
  const [landing, setLanding] = useState<PartnerSection | undefined>();
  useEffect(() => {
    if (!open) setLanding(undefined);
  }, [open]);
  const openPartner = useCallback((id: string, section?: PartnerSection) => {
    setLanding(section);
    onOpen(id);
  }, [onOpen]);
  const fromLedger = (from: "rewards" | "conversions" | "proof") => (id: string, section?: PartnerSection) => {
    track("ui.partner_opened", { from });
    openPartner(id, section);
  };

  // The program picked last on this device, so a reload or a link to the
  // settings page opens the same one.
  const pick = (id: string) => {
    setProgram(id);
    try { localStorage.setItem("outflare.partners.program", id); } catch { /* storage blocked */ }
  };
  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/partner-programs", { params: { path: { brand: pickedBrandId() } } });
    if (error) setError(errorMessage(error));
    const list = (data?.data ?? []).filter((p) => p.status !== "archived");
    setPrograms(list);
    let last = "";
    try { last = localStorage.getItem("outflare.partners.program") ?? ""; } catch { /* storage blocked */ }
    setProgram((cur) => (list.some((p) => p.id === cur) ? cur : list.some((p) => p.id === last) ? last : list[0]?.id ?? ""));
  }, [ws, brandQuery.brand]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    load();
  }, [load]);

  if (!programs) return <Loading />;
  if (isID(open)) {
    return <PartnerPage key={open} ws={ws} id={open} canManage={canManage} back={() => onOpen("")} go={go} initialSection={landing}
      onLoaded={(p) => setProgram((cur) => (programs.some((x) => x.id === p.program_id) ? p.program_id : cur))} />;
  }
  const current = programs.find((p) => p.id === program);
  if ((open === "settings" || open.startsWith("settings/")) && current) {
    const tab = settingsTabs.find((t) => `settings/${t.id}` === open)?.id ?? "general";
    return <ProgramSettings key={current.id} ws={ws} program={current} programs={programs} canManage={canManage} tab={tab} onTab={(t) => onOpen(`settings/${t}`)} onPick={pick} onChanged={load}
      back={() => onOpen("")} openFlows={() => { setSection("flows"); onOpen(""); }} />;
  }
  return (
    <div className="space-y-6">
      <PageHeader
        title="Partners"
        description="Invite your users to grow with you through referrals, paid or voluntary tasks, and custom website proposals. Partners manage their profiles and opportunities in your hosted or embedded portal; approved rewards join your payout ledger."
        action={canManage && <Button onClick={() => setCreating(true)}>New program</Button>}
      />
      <ErrorText>{error}</ErrorText>
      {programs.length === 0 || !current ? (
        <Empty title="No partner programs yet" action={canManage && <Button onClick={() => setCreating(true)}>New program</Button>}>
          Create one partner program for tasks, introductions and purchases. Choose a starting reward setup; every rule can be customized.
        </Empty>
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-2">
            <select aria-label="Program" className={field} value={program} onChange={(e) => pick(e.target.value)}>
              {programs.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
            </select>
            <Badge tone="grey">Partner program</Badge>
            {current.status === "paused" && <Badge tone="amber">Paused</Badge>}
            <span className="text-[12px] text-muted-foreground">{rulesSummary(current)}</span>
            {current.portal_enabled
              ? <a className="text-[12px] text-muted-foreground underline underline-offset-2 hover:text-foreground" href={current.portal_url} target="_blank" rel="noreferrer">Portal open</a>
              : <span className="text-[12px] text-muted-foreground">Portal closed</span>}
            <Button variant="ghost" className="ml-auto" onClick={() => onOpen("settings")}><Settings2 className="size-3.5" /> Program settings</Button>
          </div>
          <Tabs value={section} onValueChange={(v) => setSection(v as Section)}>
            <TabsList className="max-w-full flex-wrap group-data-[orientation=horizontal]/tabs:h-auto">
              <TabsTrigger value="proposals" className="h-9 px-3 text-[12px]">Proposals</TabsTrigger>
              <TabsTrigger value="tasks" className="h-9 px-3 text-[12px]">Tasks</TabsTrigger>
              <TabsTrigger value="partners" className="h-9 px-3 text-[12px]">Partners</TabsTrigger>
              <TabsTrigger value="groups" className="h-9 px-3 text-[12px]">Groups</TabsTrigger>
              <TabsTrigger value="flows" className="h-9 px-3 text-[12px]">Flows</TabsTrigger>
              {current.portal.lead_submissions && <TabsTrigger value="leads" className="h-9 px-3 text-[12px]">Leads</TabsTrigger>}
              <TabsTrigger value="conversions" className="h-9 px-3 text-[12px]">Conversions</TabsTrigger>
              <TabsTrigger value="log" className="h-9 px-3 text-[12px]">Log</TabsTrigger>
              <TabsTrigger value="review" className="h-9 px-3 text-[12px]">Review</TabsTrigger>
              <TabsTrigger value="rewards" className="h-9 px-3 text-[12px]">Rewards</TabsTrigger>
              <TabsTrigger value="payouts" className="h-9 px-3 text-[12px]">Payouts</TabsTrigger>
            </TabsList>
          </Tabs>
          {section === "proposals" && <PartnerProposals key={current.id} ws={ws} program={current} canManage={canManage}/>}
          {section === "tasks" && <PartnerTasks key={current.id} ws={ws} program={current} canManage={canManage} />}
          {section === "partners" && <PartnerList key={current.id} ws={ws} program={current} canManage={canManage} find={find} onFind={setFind} onOpen={openPartner} />}
          {section === "groups" && <Groups key={current.id} ws={ws} program={current} canManage={canManage} onChanged={load} />}
          {section === "flows" && <Flows key={current.id} ws={ws} program={current} canManage={canManage} onChanged={load} />}
          {section === "leads" && <Leads key={current.id} ws={ws} program={current} canManage={canManage} />}
          {section === "conversions" && <ConversionList key={current.id} ws={ws} go={go} programId={current.id} openPartner={fromLedger("conversions")} />}
          {section === "log" && <ConversionLog key={current.id} ws={ws} program={current} />}
          {section === "review" && <PartnerReview key={current.id} program={current} canManage={canManage} onOpenPartner={(id) => { track("ui.partner_opened", { from: "review" }); openPartner(id); }} />}
          {section === "rewards" && (
            <RewardList key={current.id} ws={ws} go={go} canManage={canManage} slug={current.slug} importedFrom={current.imported_from} programId={current.id}
              openPartner={fromLedger("rewards")} openPayouts={(id) => fromLedger("proof")(id, "payouts")} />
          )}
          {section === "payouts" && <Payouts key={current.id} ws={ws} program={current} canManage={canManage} />}
        </>
      )}
      {creating && <NewProgram ws={ws} onClose={() => setCreating(false)} onSaved={(p) => { setCreating(false); pick(p.id); load(); }} />}
    </div>
  );
}

export function ruleText(r: PartnerProgram["rules"][number], currency: string): string {
  if (r.kind === "group") return r.name || "Moves the partner to a group";
  const what = r.kind === "percent" ? `${(r.percent_bp ?? 0) / 100}%` : r.kind === "fixed" ? money(r.amount_cents ?? 0, currency) : r.action;
  const when = r.trigger === "signup" ? "per sign-up" : r.trigger !== "sale" ? `per ${r.trigger}` : r.recurrence === "first" ? "on the first sale" : r.recurrence === "months" ? `on sales for ${r.months} months` : salesText(r.conditions);
  const who = r.side === "referred" ? " for the friend" : "";
  const tier = r.min_sales > 0 ? ` after ${r.min_sales} sales` : "";
  return `${what} ${when}${who}${tier}`;
}

// salesText says which sales a rule pays on, so "on every sale" never shows
// for a rule its conditions narrow: "on the first sale, within 1 year of
// sign-up", or "on sales that match" when the conditions say more.
function salesText(c: PartnerProgram["rules"][number]["conditions"] | undefined): string {
  const groups = (c?.groups ?? []).filter((g) => g.conditions.length > 0);
  if (groups.length === 0) return "on every sale";
  if (groups.length > 1 || groups[0].match === "any") return "on sales that match";
  let which = "", since = "", other = false;
  for (const x of groups[0].conditions) {
    const v = x.value as unknown;
    if (x.field === "customer.transactions" && typeof v === "number") {
      if (x.op === "eq" && v === 0) which = "on the first sale";
      else if ((x.op === "gt" && v === 0) || (x.op === "gte" && v === 1)) which = "on sales after the first";
      else other = true;
    } else if (x.field === "customer.signed_up_at" && x.op === "within" && v && typeof v === "object") {
      const w = v as { amount?: number; unit?: string };
      const unit = w.amount === 1 ? (w.unit ?? "days").replace(/s$/, "") : w.unit;
      since = `within ${w.amount} ${unit} of sign-up`;
    } else other = true;
  }
  if (other || (!which && !since)) return "on sales that match";
  return [which || "on sales", since].filter(Boolean).join(", ");
}

export function rulesSummary(p: PartnerProgram): string {
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
export function ownRate(rules: PartnerProgram["rules"], partner: string) {
  return rules.find((r) => r.partner_id === partner && r.side === "partner" && r.trigger === "sale" && (r.kind === "percent" || r.kind === "fixed"));
}

// A program's totals across all its partners.
type PartnerProgramStats = PartnerStats & { partners: { total: number; active: number; pending: number; suspended: number; rejected: number; missing_payout_details: number } };

// A page of a list, newest first, and the cursor to the next.
export type Page<T> = { list: T[]; next: string | null; error?: string };

// usePages loads a list a page at a time: reload starts again, more adds
// the next page. get must be stable (useCallback). A page that arrives
// after a newer reload is dropped.
export function usePages<T>(get: (before?: string) => Promise<Page<T>>) {
  const [list, setList] = useState<T[] | null>(null);
  const [next, setNext] = useState<string | null>(null);
  const [error, setError] = useState("");
  const gen = useRef(0);
  const reload = useCallback(async () => {
    const g = ++gen.current;
    const r = await get();
    if (g !== gen.current) return;
    setList(r.list);
    setNext(r.next);
    setError(r.error ?? "");
  }, [get]);
  const more = useCallback(async () => {
    if (!next) return;
    const g = gen.current;
    const r = await get(next);
    if (g !== gen.current) return;
    setList((l) => [...(l ?? []), ...r.list]);
    setNext(r.next);
    setError(r.error ?? "");
  }, [get, next]);
  return { list, next, error, reload, more };
}

// Leave shown out when the server searches and sorts, so the note never claims the browser does.
export function LoadMore({ next, onMore, shown, search = false }: { next: string | null; onMore: () => Promise<void>; shown?: number; search?: boolean }) {
  const [busy, setBusy] = useState(false);
  if (!next) return null;
  return (
    <div className="flex flex-wrap items-center justify-center gap-x-3 gap-y-1 text-[12px] text-muted-foreground">
      {shown !== undefined && <span>Showing the newest {shown}. {search ? "Search and sorting cover" : "Sorting covers"} these.</span>}
      <Button variant="ghost" disabled={busy} onClick={async () => { setBusy(true); await onMore(); setBusy(false); }}>{busy ? "Loading…" : "Load more"}</Button>
    </div>
  );
}

export const pageSize = 100;

const partnerSorts: [PartnerSort, string][] = [
  ["newest", "Newest first"], ["oldest", "Oldest first"], ["name", "Name, A to Z"], ["code", "Code, A to Z"], ["clicks", "Most clicks"],
  ["customers", "Most customers"], ["sales", "Most sales"], ["balance", "Most due"], ["paid", "Most paid"],
];

function PartnerList({ ws, program, canManage, find, onFind, onOpen }: { ws: string; program: PartnerProgram; canManage: boolean; find: PartnerFind; onFind: Dispatch<SetStateAction<PartnerFind>>; onOpen: (id: string) => void }) {
  const [couponPartner,setCouponPartner]=useState<Partner|null>(null);
  const [rules, setRules] = useState(program.rules);
  const [groups, setGroups] = useState<PartnerGroup[]>([]);
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [adding, setAdding] = useState(false);
  const [totals, setTotals] = useState<PartnerProgramStats | null>(null);
  // Only partners who haven't given payout details (imported ones re-enter them on the portal).
  const [missingOnly, setMissingOnly] = useState(false);
  const [text, setText] = useState(find.q);
  useEffect(() => setRules(program.rules), [program.rules]);
  useEffect(() => {
    const q = text.trim();
    if (q === find.q) return;
    const t = setTimeout(async () => {
      if (isID(q)) {
        const { data } = await api.GET("/v1/brands/{brand}/partners/{id}", { params: { path: { brand: pickedBrandId(), id: q } } });
        if (data) {
          track("ui.partner_opened", { from: "search" });
          return onOpen(data.id);
        }
      }
      if (!find.q) track("ui.partners_searched", { sort: find.sort });
      onFind((f) => ({ ...f, q }));
    }, 300);
    return () => clearTimeout(t);
  }, [text, find.q, find.sort, onFind, onOpen]);
  const get = useCallback(async (cursor?: string): Promise<Page<Partner>> => {
    const { data, error } = await api.GET("/v1/brands/{brand}/partners", {
      params: { path: { brand: pickedBrandId() }, query: {
        program: program.id, limit: pageSize, ...(find.q ? { q: find.q } : {}), ...(find.sort !== "newest" ? { sort: find.sort } : {}),
        ...(cursor ? { cursor } : {}), ...(missingOnly ? { payout_details: "missing" as const } : {}),
      } },
    });
    return { list: data?.data ?? [], next: data?.next_cursor ?? null, error: error ? errorMessage(error) : "" };
  }, [ws, program.id, missingOnly, find.q, find.sort]);
  const { list, next, error, reload, more } = usePages(get);
  useEffect(() => {
    reload();
    setPicked(new Set());
  }, [reload]);
  // The tiles count every partner, not just the loaded page.
  const loadProgram = useCallback(async () => {
    const [stats, prog] = await Promise.all([
      api.GET("/v1/brands/{brand}/partner-programs/{id}/stats", { params: { path: { brand: pickedBrandId(), id: program.id } } }),
      api.GET("/v1/brands/{brand}/partner-programs/{id}", { params: { path: { brand: pickedBrandId(), id: program.id } } }),
    ]);
    setTotals(stats.data ?? null);
    if (prog.data) {
      setGroups(prog.data.groups);
      setRules(prog.data.rules);
    }
  }, [ws, program.id]);
  useEffect(() => {
    loadProgram();
  }, [loadProgram]);
  const load = useCallback(async () => {
    await Promise.all([reload(), loadProgram()]);
    setPicked(new Set());
  }, [reload, loadProgram]);

  // move puts the picked partners in a group, or in none.
  async function move(group: string) {
    const ids = [...picked];
    const { data, error } = await api.POST("/v1/brands/{brand}/partner-programs/{id}/groups/{group}/partners", {
      params: { path: { brand: pickedBrandId(), id: program.id, group } },
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
  function addCode(p: Partner) {setCouponPartner(p);track("ui.partner_coupon_composer_opened");}
  const open = (id: string) => {
    track("ui.partner_opened", { from: "list" });
    onOpen(id);
  };

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
          <AppLink ws={ws} tab="partners" item={p.id} onOpen={() => open(p.id)} className="block truncate font-medium hover:underline">{partnerName(p)}</AppLink>
          {p.name && p.email && <span className="block truncate text-[12px] text-muted-foreground">{p.email}</span>}
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
          {canManage && <Menu aria={`Actions for ${p.code}`} items={partnerActions(p, load, () => addCode(p))} />}
        </span>
      ),
    },
  ];

  if (!list) return <Loading />;
  const held = totals && (totals.held_at_source.pending_cents || totals.held_at_source.approved_cents) ? totals.held_at_source : null;
  return (
    <div className="space-y-4">
      <ErrorText>{error}</ErrorText>
      {couponPartner&&<PartnerCouponDialog ws={ws} partner={couponPartner} currency={program.currency} onClose={()=>setCouponPartner(null)} onSaved={()=>{setCouponPartner(null);load();}}/>}
      {totals && (
        <div className={held ? "[&>div>div:last-child]:col-span-2 sm:[&>div>div:last-child]:col-span-1" : undefined}>
        <Stats cols={held ? 5 : 4}>
          <Stat label="Partners" value={totals.partners.active} sub={totals.partners.pending ? `${totals.partners.pending} waiting for approval` : undefined} />
          <Stat label="Referred customers" value={totals.customers} sub={plural(totals.clicks, "click")} />
          <Stat label="Referred sales" value={money(totals.sales_cents, c)} />
          {held ? (
            <>
              <Stat label="Due to partners" value={money(totals.balance_cents, c)} sub={`What Outflare pays; ${platformName(program.imported_from)} pays ${money(held.approved_cents, c)} more`} />
              <Stat label="On hold" value={money(totals.pending_cents - held.pending_cents, c)} sub={`What Outflare pays; ${platformName(program.imported_from)} pays ${money(held.pending_cents, c)} more`} />
            </>
          ) : <Stat label="Due to partners" value={money(totals.balance_cents, c)} sub={`${money(totals.pending_cents, c)} on hold`} />}
        </Stats>
        </div>
      )}
      {list.length === 0 && !missingOnly && !find.q ? (
        <Empty title="No partners yet" action={canManage && <Button onClick={() => setAdding(true)}>Add partner</Button>}>
          Add someone by email, or a customer record you already have. Each partner gets a ref code, a short link and a tagged campaign link.
        </Empty>
      ) : (
        <DataTable
          columns={columns}
          data={list}
          rowId={(p) => p.id}
          searchPlaceholder="Search by name, email, code, promo code or id"
          search={{ value: text, onChange: setText }}
          toolbar={
            <span className="flex flex-wrap items-center gap-2">
              <select aria-label="Sort partners" className={field} value={find.sort} onChange={(e) => {
                const sort = e.target.value as PartnerSort;
                track("ui.partners_sorted", { sort });
                onFind((f) => ({ ...f, sort }));
              }}>
                {partnerSorts.map(([v, label]) => <option key={v} value={v}>{label}</option>)}
              </select>
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
              <AppLink ws={ws} tab="partners" item={p.id} onOpen={() => open(p.id)} className="block truncate text-[13px] font-medium underline-offset-2 hover:underline">{partnerName(p)}</AppLink>
              <span className="block truncate text-[12px] text-muted-foreground">{p.status} · {plural(p.stats.customers, "customer")} · {money(p.stats.balance_cents, c)} due</span>
            </span>
          )}
        />
      )}
      <LoadMore next={next} onMore={more} />
      {adding && <AddPartner ws={ws} program={program} onClose={() => setAdding(false)} onSaved={() => { setAdding(false); load(); }} />}
    </div>
  );
}

function AddPartner({ program, onClose, onSaved }: { ws: string; program: PartnerProgram; onClose: () => void; onSaved: () => void }) {
  const [email, setEmail] = useState("");
  const [name, setName] = useState("");
  const [code, setCode] = useState("");
  const [made, setMade] = useState<Partner | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const { data, error } = await api.POST("/v1/brands/{brand}/partners", {
      params: { path: { brand: pickedBrandId() } },
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

const logOutcomes: { id: PartnerConversionLogEntry["outcome"]; label: string; tone: "green" | "grey" | "amber" | "red" }[] = [
  { id: "credited", label: "Credited", tone: "green" },
  { id: "not_credited", label: "Not credited", tone: "amber" },
  { id: "duplicate", label: "Duplicate", tone: "grey" },
  { id: "rejected", label: "Rejected", tone: "red" },
  { id: "error", label: "Error", tone: "red" },
];

// ConversionLog is every conversion call from the last 30 days (API and
// postback URL), with what it sent and what Outflare answered.
function ConversionLog({ ws, program }: { ws: string; program: PartnerProgram }) {
  const [outcome, setOutcome] = useState<PartnerConversionLogEntry["outcome"] | "">("");
  const [all, setAll] = useState(false);
  const [q, setQ] = useState("");
  const [search, setSearch] = useState("");
  const [open, setOpen] = useState<PartnerConversionLogEntry | null>(null);
  const get = useCallback(async (cursor?: string): Promise<Page<PartnerConversionLogEntry>> => {
    const { data, error } = await api.GET("/v1/brands/{brand}/partner-conversion-log", {
      params: { path: { brand: pickedBrandId() }, query: { limit: pageSize, ...(all ? {} : { program: program.id }), ...(outcome ? { outcome } : {}), ...(search ? { q: search } : {}), ...(cursor ? { cursor } : {}) } },
    });
    return { list: data?.data ?? [], next: data?.next_cursor ?? null, error: error ? errorMessage(error) : "" };
  }, [ws, program.id, outcome, all, search]);
  const { list, next, error, reload, more } = usePages(get);
  useEffect(() => {
    reload();
  }, [reload]);
  const columns: ColumnDef<PartnerConversionLogEntry>[] = [
    { id: "when", header: "When", accessorFn: (v) => v.created_at, cell: ({ row: { original: v } }) => ago(v.created_at) },
    { id: "source", header: "From", accessorFn: (v) => (v.source === "api" ? "API" : "Postback URL") },
    { id: "type", header: "Type", accessorFn: (v) => v.type || "?" },
    { id: "external", header: "Your id", accessorFn: (v) => String(v.request.external_id ?? v.request.customer_id ?? ""), cell: ({ getValue }) => <span className="font-mono text-[12px]">{getValue() as string}</span> },
    {
      id: "outcome", header: "Result", accessorFn: (v) => v.outcome,
      cell: ({ row: { original: v } }) => {
        const o = logOutcomes.find((x) => x.id === v.outcome);
        return <span className="flex items-center gap-2"><Badge tone={o?.tone ?? "grey"}>{o?.label ?? v.outcome}</Badge>{v.reason && <span className="font-mono text-[12px] text-muted-foreground">{v.reason}</span>}</span>;
      },
    },
    { id: "view", header: "", cell: ({ row: { original: v } }) => <Button variant="ghost" onClick={() => setOpen(v)}>View</Button> },
  ];
  return (
    <div className="space-y-3">
      <form className="flex flex-wrap items-center gap-2" onSubmit={(e) => { e.preventDefault(); setSearch(q.trim()); }}>
        <input aria-label="Search the log" className={field + " w-72"} value={q} onChange={(e) => setQ(e.target.value)} placeholder="Order id, customer id, ref, promo code or email" maxLength={200} />
        <select aria-label="Result" className={field} value={outcome} onChange={(e) => setOutcome(e.target.value as PartnerConversionLogEntry["outcome"] | "")}>
          <option value="">Every result</option>
          {logOutcomes.map((o) => <option key={o.id} value={o.id}>{o.label}</option>)}
        </select>
        <select aria-label="Programs" className={field} value={all ? "all" : "this"} onChange={(e) => setAll(e.target.value === "all")}>
          <option value="this">This program</option>
          <option value="all">Every call (also those that named no program)</option>
        </select>
        <Button type="submit" variant="ghost">Search</Button>
      </form>
      <ErrorText>{error}</ErrorText>
      {!list ? <Loading /> : list.length === 0 ? (
        <Empty title={search || outcome ? "No calls match" : "No conversion calls in the last 30 days"}>
          Every conversion your backend or tracker reports, by the API or the postback URL, shows here for 30 days with what it sent, what Outflare answered and why it was or wasn't credited.
        </Empty>
      ) : (
        <>
          <DataTable columns={columns} data={list} rowId={(v) => v.id} empty="No calls match." />
          <LoadMore next={next} onMore={more} shown={list.length} />
        </>
      )}
      {open && (
        <Dialog open onOpenChange={(o) => !o && setOpen(null)}>
          <DialogContent className="max-w-2xl">
            <DialogHeader>
              <DialogTitle>{open.source === "api" ? "API call" : "Postback URL call"}, {ago(open.created_at)}</DialogTitle>
              <DialogDescription>
                HTTP {open.status} in {open.duration_ms} ms{open.request_id ? `, request ${open.request_id}` : ""}. An email the call sent is not kept; it shows as (sent).
              </DialogDescription>
            </DialogHeader>
            <p className="text-[12px] font-medium">Sent</p>
            <pre className="max-h-60 overflow-auto rounded bg-muted p-2 text-[12px]">{JSON.stringify(open.request, null, 2)}</pre>
            <p className="text-[12px] font-medium">Answer</p>
            <pre className="max-h-60 overflow-auto rounded bg-muted p-2 text-[12px]">{JSON.stringify(open.response, null, 2)}</pre>
          </DialogContent>
        </Dialog>
      )}
    </div>
  );
}

// Leads are people partners submitted on the portal as referred.
function Leads({ ws, program, canManage }: { ws: string; program: PartnerProgram; canManage: boolean }) {
  const [status, setStatus] = useState<PartnerLead["status"] | "">("pending");
  const get = useCallback(async (cursor?: string): Promise<Page<PartnerLead>> => {
    const { data, error } = await api.GET("/v1/brands/{brand}/partner-leads", { params: { path: { brand: pickedBrandId() }, query: { program: program.id, limit: pageSize, ...(status ? { status } : {}), ...(cursor ? { cursor } : {}) } } });
    return { list: data?.data ?? [], next: data?.next_cursor ?? null, error: error ? errorMessage(error) : "" };
  }, [ws, program.id, status]);
  const { list, next, error, reload, more } = usePages(get);
  useEffect(() => {
    reload();
  }, [reload]);
  async function decide(l: PartnerLead, what: "approve" | "reject") {
    let reason = "";
    if (what === "reject") {
      const r = await ask({ title: "Turn down this lead?", body: "Say why; the partner sees it.", input: { label: "Reason", maxLength: 500, optional: true }, choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Reject", value: "ok", variant: "danger-solid" }] });
      if (r?.choice !== "ok") return;
      reason = r.value.trim();
    }
    const { data, error } = what === "approve"
      ? await api.POST("/v1/brands/{brand}/partner-leads/{id}/approve", { params: { path: { brand: pickedBrandId(), id: l.id } }, body: {} })
      : await api.POST("/v1/brands/{brand}/partner-leads/{id}/reject", { params: { path: { brand: pickedBrandId(), id: l.id } }, body: { reason } });
    if (error) return toast(errorMessage(error), "error");
    toast(data?.status === "approved" ? "Added as a referral." : data?.reason && what === "approve" ? `Couldn't add it: ${data.reason}.` : "Rejected.", data?.status === "approved" ? "ok" : "error");
    reload();
  }
  const columns: ColumnDef<PartnerLead>[] = [
    { id: "person", header: "Lead", accessorFn: (l) => l.name || l.email, cell: ({ row: { original: l } }) => <span className="block"><span className="block">{l.name || l.email}</span>{l.name && <span className="text-[12px] text-muted-foreground">{l.email}</span>}</span> },
    { id: "partner", header: "Partner", accessorFn: (l) => l.partner_name },
    { id: "note", header: "Note", accessorFn: (l) => l.note, cell: ({ getValue }) => <span className="text-[12px] text-muted-foreground">{getValue() as string}</span> },
    { id: "status", header: "Status", accessorFn: (l) => l.status, cell: ({ row: { original: l } }) => <span className="flex items-center gap-2"><Badge tone={l.status === "approved" ? "green" : l.status === "rejected" ? "red" : "amber"}>{l.status}</Badge>{l.reason && <span className="text-[12px] text-muted-foreground">{l.reason}</span>}</span> },
    { id: "when", header: "Submitted", accessorFn: (l) => l.created_at, cell: ({ row: { original: l } }) => ago(l.created_at) },
    { id: "actions", header: "", enableSorting: false, cell: ({ row: { original: l } }) => canManage && l.status === "pending" && (
      <span className="inline-flex gap-1"><Button variant="ghost" onClick={() => decide(l, "approve")}>Approve</Button><Button variant="ghost" onClick={() => decide(l, "reject")}>Reject</Button></span>
    ) },
  ];
  return (
    <div className="space-y-3">
      <select aria-label="Status" className={field} value={status} onChange={(e) => setStatus(e.target.value as PartnerLead["status"] | "")}>
        <option value="pending">Waiting for you</option>
        <option value="approved">Approved</option>
        <option value="rejected">Rejected</option>
        <option value="">All</option>
      </select>
      <ErrorText>{error}</ErrorText>
      {!list ? <Loading /> : list.length === 0 ? (
        <Empty title={status === "pending" ? "No leads waiting" : "No leads"}>Partners submit people they referred on the portal's Referrals page. Approving one makes them the partner's referred customer.</Empty>
      ) : (
        <>
          <DataTable columns={columns} data={list} rowId={(l) => l.id} empty="No leads match." />
          <LoadMore next={next} onMore={more} shown={list.length} />
        </>
      )}
    </div>
  );
}

// SwitchOver warns, while the platform a program was imported from still
// pays it, that its imported commissions are paid there, and records the
// day that stops.
export function rewardActions(w: PartnerReward, done: () => void) {
  async function act(what: "approve" | "reject") {
    let note = "";
    if (what === "reject") {
      const r = await ask({ title: "Reject this reward?", body: "Say why; the partner can see it.", input: { label: "Reason", maxLength: 2000, optional: true }, choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Reject", value: "ok", variant: "danger-solid" }] });
      if (r?.choice !== "ok") return;
      note = r.value.trim();
    }
    const { error } = what === "approve"
      ? await api.POST("/v1/brands/{brand}/partner-rewards/{id}/approve", { params: { path: { brand: pickedBrandId(), id: w.id } }, body: {} })
      : await api.POST("/v1/brands/{brand}/partner-rewards/{id}/reject", { params: { path: { brand: pickedBrandId(), id: w.id } }, body: { note } });
    if (error) return toast(errorMessage(error), "error");
    toast(what === "approve" ? "Approved." : "Rejected.");
    done();
  }
  return [{ label: "Approve now", onClick: () => act("approve") }, { label: "Reject", onClick: () => act("reject") }];
}

export function payoutActions(o: PartnerPayout, done: () => void) {
  async function paid() {
    const r = await ask({ title: `Mark ${money(o.amount_cents, o.currency)} paid`, body: "Only once you've paid it. The reference is the payment's id, such as a PayPal or Wise transfer id.", input: { label: "Reference", maxLength: 200, optional: true }, choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Mark paid", value: "ok" }] });
    if (r?.choice !== "ok") return;
    const { error } = await api.POST("/v1/brands/{brand}/partner-payouts/{id}/paid", { params: { path: { brand: pickedBrandId(), id: o.id } }, body: { reference: r.value.trim() } });
    if (error) return toast(errorMessage(error), "error");
    toast("Marked paid.");
    done();
  }
  async function cancel() {
    const { error } = await api.POST("/v1/brands/{brand}/partner-payouts/{id}/cancel", { params: { path: { brand: pickedBrandId(), id: o.id } }, body: {} });
    if (error) return toast(errorMessage(error), "error");
    toast("Canceled; its rewards are back in the balance.");
    done();
  }
  return [{ label: "Mark paid", onClick: paid }, { label: "Cancel", onClick: cancel }];
}

function SwitchOver({ program, canManage, onDone }: { ws: string; program: PartnerProgram; canManage: boolean; onDone: () => void }) {
  const [at, setAt] = useState(program.switched_over_at);
  if (!program.imported_from || at) return null;
  const from = platformName(program.imported_from);
  async function switchOver() {
    const r = await ask({
      title: `Stop paying through ${from}?`,
      body: `Only once ${from} no longer pays this program. Its imported commissions that are still unpaid are then approved after the hold and paid here like any other.`,
      choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Switch over", value: "ok" }],
    });
    if (r?.choice !== "ok") return;
    const { data, error } = await api.POST("/v1/brands/{brand}/partner-programs/{id}/switch-over", { params: { path: { brand: pickedBrandId(), id: program.id } } });
    if (error || !data) return toast(errorMessage(error), "error");
    setAt(data.switched_over_at);
    toast(`Switched over from ${from}. Imported commissions are paid here from now on.`);
    onDone();
  }
  return (
    <div className="flex flex-wrap items-center gap-3 rounded-md border border-amber-500/40 bg-amber-500/10 p-3 text-[13px]">
      <span className="min-w-0 flex-1">{from} still pays this program. Commissions imported from {from} keep its status and are never put in a payout here, so nobody is paid twice. New ones Outflare credits are paid here.</span>
      {canManage && <Button variant="ghost" onClick={switchOver}>Switch over</Button>}
    </div>
  );
}

function Payouts({ ws, program, canManage }: { ws: string; program: PartnerProgram; canManage: boolean }) {
  const get = useCallback(async (cursor?: string): Promise<Page<PartnerPayout>> => {
    const { data, error } = await api.GET("/v1/brands/{brand}/partner-payouts", { params: { path: { brand: pickedBrandId() }, query: { program: program.id, limit: pageSize, ...(cursor ? { cursor } : {}) } } });
    return { list: data?.data ?? [], next: data?.next_cursor ?? null, error: error ? errorMessage(error) : "" };
  }, [ws, program.id]);
  const { list, next, error: listError, reload: load, more } = usePages(get);
  useEffect(() => {
    load();
  }, [load]);
  async function makeDue() {
    const { data, error } = await api.POST("/v1/brands/{brand}/partner-payouts", { params: { path: { brand: pickedBrandId() } }, body: { program: program.id } });
    if (error) return toast(errorMessage(error), "error");
    const n = data?.data.length ?? 0;
    const waiting = data?.waiting_for_details ? ` ${plural(data.waiting_for_details, "partner")} still need to add payout details on the portal; theirs wait.` : "";
    toast((n ? `${plural(n, "payout")} due. Pay them from your own account, then mark each paid.` : `Nobody's balance has reached the ${money(program.min_payout_cents, program.currency)} minimum yet.`) + waiting);
    load();
  }
  // download saves the due payouts, with each partner's payout details, as
  // CSV for paying them from your own account or a payout tool.
  async function download() {
    const { data, error, response } = await api.GET("/v1/brands/{brand}/partner-payouts/export", {
      params: { path: { brand: pickedBrandId() }, query: { program: program.id, status: "due" } }, parseAs: "text",
    });
    if (error || typeof data !== "string") return toast(errorMessage(error), "error");
    saveFile(data, response.headers.get("Content-Type") ?? "text/csv", `${program.slug}-due-payouts.csv`);
  }
  if (!list) return <Loading />;
  const columns: ColumnDef<PartnerPayout>[] = [
    { id: "partner", header: "Partner", accessorFn: (o) => o.partner_name },
    { id: "amount", header: "Amount", accessorFn: (o) => o.amount_cents, cell: ({ row: { original: o } }) => money(o.amount_cents, o.currency) },
    { id: "status", header: "Status", accessorFn: (o) => o.status, cell: ({ row: { original: o } }) => <Badge tone={o.status === "paid" ? "green" : o.status === "due" ? "amber" : "grey"}>{o.status}</Badge> },
    { id: "reference", header: "Reference", accessorFn: (o) => o.reference, cell: ({ getValue }) => <span className="font-mono text-[12px]">{getValue() as string}</span> },
    { id: "when", header: "Made", accessorFn: (o) => o.created_at, cell: ({ row: { original: o } }) => (o.paid_at ? `paid ${ago(o.paid_at)}` : ago(o.created_at)) },
    {
      id: "actions", header: "", enableSorting: false, enableHiding: false,
      cell: ({ row: { original: o } }) => (
        <span className="flex items-center justify-end gap-1">
          <Button variant="ghost" onClick={() => downloadProof({ payout_id: o.id }, payoutProofName(program.slug, o.id))}>Download proof</Button>
          {canManage && o.status === "due" && <Menu aria="Payout actions" items={payoutActions(o, load)} />}
        </span>
      ),
    },
  ];
  return (
    <div className="space-y-3">
      <p className="text-[13px] text-muted-foreground">Outflare never holds or sends money. Make the payouts that are due, pay them from your own account or payout tool (the partner_payout.created webhook can start it), then mark each paid with its reference.</p>
      <SwitchOver ws={ws} program={program} canManage={canManage} onDone={load} />
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
      <LoadMore next={next} onMore={more} shown={list.length} search />
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

function NewProgram({ onClose, onSaved }: { ws: string; onClose: () => void; onSaved: (p: PartnerProgram) => void }) {
  const [kind, setKind] = useState<Kind>("affiliate");
  const [name, setName] = useState("");
  const [landing, setLanding] = useState("");
  const [review, setReview] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const { data, error } = await api.POST("/v1/brands/{brand}/partner-programs", {
      params: { path: { brand: pickedBrandId() } },
      body: { name: name.trim(), kind, landing_url: landing.trim() || undefined, auto_accept: !review },
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
          <DialogDescription>One program for tasks, introductions and purchases. Start with sales commissions, then customize any reward.</DialogDescription>
        </DialogHeader>
        <form onSubmit={save} className="space-y-3">
          <details className="rounded-lg border p-3"><summary className="cursor-pointer text-xs font-medium">Customize starting rewards (optional)</summary>          <div className="grid gap-2">
            {kinds.map((k) => (
              <label key={k.id} className={`flex cursor-pointer gap-3 rounded-md border p-3 text-[13px] ${kind === k.id ? "border-primary" : ""}`}>
                <input type="radio" name="kind" checked={kind === k.id} onChange={() => setKind(k.id)} className="mt-0.5" />
                <span><span className="block font-medium">{k.label}</span><span className="block text-muted-foreground">{k.blurb}</span></span>
              </label>
            ))}
          </div>
</details>
          <Input label="Name" value={name} onChange={(e) => setName(e.target.value)} placeholder="Northpeak partners" maxLength={120} required />
          <label className="flex items-center gap-2 text-[13px]"><input type="checkbox" checked={review} onChange={e => setReview(e.target.checked)} /> Review each application before the partner can take part</label>
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

type PayoutMethod = PartnerProgram["payout_methods"][number];

export const payoutMethods: [PayoutMethod, string][] = [["paypal", "PayPal"], ["wise", "Wise"], ["bank_transfer", "Bank transfer"], ["crypto", "Crypto"], ["other", "Other"]];


// Groups: each partner is in at most one group, new partners join the
// default one, and a group's own rate replaces the program's for its
// partners (a partner's own rate beats it).
function Groups({ ws, program, canManage, onChanged }: { ws: string; program: PartnerProgram; canManage: boolean; onChanged: () => void }) {
  const [p, setP] = useState<PartnerProgram | null>(null);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/partner-programs/{id}", { params: { path: { brand: pickedBrandId(), id: program.id } } });
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
    const { error } = await api.POST("/v1/brands/{brand}/partner-programs/{id}/groups", { params: { path: { brand: pickedBrandId(), id: p!.id } }, body: { name: r.value.trim(), default: p!.groups.length === 0 } });
    if (error) return toast(errorMessage(error), "error");
    done(p!.groups.length === 0 ? `Added ${r.value.trim()}. New partners join it.` : `Added ${r.value.trim()}.`);
  }
  async function patch(g: PartnerGroup, body: { name?: string; default?: boolean }, msg: string) {
    const { error } = await api.PATCH("/v1/brands/{brand}/partner-programs/{id}/groups/{group}", { params: { path: { brand: pickedBrandId(), id: p!.id, group: g.id } }, body });
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
    const { error } = await api.PUT("/v1/brands/{brand}/partner-programs/{id}/rules", { params: { path: { brand: pickedBrandId(), id: p!.id } }, body: { rules: next } });
    if (error) return toast(errorMessage(error), "error");
    done(pct === null ? `${g.name} is back on the program's rate.` : `${g.name} earns ${pct}% of each sale from now on.`);
  }
  async function remove(g: PartnerGroup) {
    const r = await ask({ title: `Remove ${g.name}?`, body: `Its own rules go with it, and its ${plural(g.partners, "partner")} move to no group. Rewards already earned keep their amounts.`, choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Remove", value: "ok", variant: "danger-solid" }] });
    if (r?.choice !== "ok") return;
    const { error } = await api.DELETE("/v1/brands/{brand}/partner-programs/{id}/groups/{group}", { params: { path: { brand: pickedBrandId(), id: p!.id, group: g.id } } });
    if (error) return toast(errorMessage(error), "error");
    done(`Removed ${g.name}.`);
  }

  const columns: ColumnDef<PartnerGroup>[] = [
    { id: "name", header: "Group", accessorFn: (g) => g.name, cell: ({ row: { original: g } }) => <span className="flex items-center gap-2"><span className="font-medium">{g.name}</span>{g.default && <Badge tone="blue">default</Badge>}</span> },
    { id: "partners", header: "Partners", accessorFn: (g) => g.partners },
    { id: "rate", header: "Rate", accessorFn: (g) => rateOf(g)?.percent_bp ?? 0, cell: ({ row: { original: g } }) => { const r = rateOf(g); return r ? ruleText(r, p.currency) : <span className="text-muted-foreground">The program's</span>; } },
    { id: "signup", header: "Sign-up link", enableSorting: false, cell: ({ row: { original: g } }) => <CopyButton text={g.signup_url} label="Copy link" /> },
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
      <p className="text-[13px] text-muted-foreground">Each partner is in one group at most. New partners join the default group, and a group's rate replaces the program's for its partners. Move partners from the Partners tab: pick them, then Move to. A group's sign-up link opens the portal's application form, and partners who apply through it join that group, so share it only with those you'd put there.</p>
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
// programs they are a partner in and the partner who referred them, each
// linking to that partner's page. It shows nothing when there is neither,
// or the viewer can't read partners.
export function RecordPartners({ ws, record, go }: { ws: string; record: string; go?: Go }) {
  const [own, setOwn] = useState<Partner[] | null>(null);
  const [by, setBy] = useState<{ ref: PartnerReferral; partner: Partner | null }[]>([]);

  useEffect(() => {
    let live = true;
    (async () => {
      const [mine, refs] = await Promise.all([
        api.GET("/v1/brands/{brand}/partners", { params: { path: { brand: pickedBrandId() }, query: { record_id: record } } }),
        api.GET("/v1/brands/{brand}/partner-referrals", { params: { path: { brand: pickedBrandId() }, query: { record_id: record } } }),
      ]);
      const list = refs.data?.data ?? [];
      const partners = await Promise.all(list.map((ref) =>
        api.GET("/v1/brands/{brand}/partners/{id}", { params: { path: { brand: pickedBrandId(), id: ref.partner_id } } }).then(({ data }) => ({ ref, partner: data ?? null })),
      ));
      if (!live) return;
      setOwn(mine.data?.data ?? []);
      setBy(partners);
    })();
    return () => { live = false; };
  }, [ws, record]);

  if (own === null || (own.length === 0 && by.length === 0)) return null;
  const open = go && ((id: string) => {
    track("ui.partner_opened", { from: "record" });
    go("partners", id);
  });
  return (
    <div className="space-y-2">
      <div className="font-medium">Partners</div>
      {own.map((p) => (
        <div key={p.id} className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5">
          <span className="min-w-0">
            <AppLink ws={ws} tab="partners" item={p.id} onOpen={open && (() => open(p.id))} className="underline underline-offset-2 hover:text-foreground">
              Partner in <span className="font-medium">{p.program}</span> as <span className="font-mono">{p.code}</span>
            </AppLink>{" "}
            <Badge tone={partnerTone[p.status]}>{p.status}</Badge>
          </span>
          <span className="shrink-0 text-muted-foreground">{money(p.stats.sales_cents, p.currency)} sales · {money(p.stats.balance_cents, p.currency)} due</span>
        </div>
      ))}
      {by.map(({ ref, partner }) => (
        <div key={ref.id} className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5">
          <span className="min-w-0">
            {partner ? (
              <AppLink ws={ws} tab="partners" item={partner.id} onOpen={open && (() => open(partner.id))} className="underline underline-offset-2 hover:text-foreground">
                Referred by <span className="font-mono">{partner.code}</span> ({partner.name || partner.email || "partner"}) in <span className="font-medium">{partner.program}</span>
              </AppLink>
            ) : "Referred by a partner"}
          </span>
          <span className="shrink-0 text-muted-foreground">{ref.status}, {ago(ref.created_at)}</span>
        </div>
      ))}
    </div>
  );
}
