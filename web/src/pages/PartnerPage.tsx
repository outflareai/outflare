import { useCallback, useEffect, useState, type MouseEvent, type ReactNode } from "react";
import type { Partner, PartnerGroup, PartnerPayout, PartnerProgram, PartnerReferral, Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { pickedBrandId } from "@/lib/brand";
import { appPath, type Tab } from "@/lib/paths";
import { track } from "@/lib/analytics";
import { Badge, Button, CopyButton, ErrorText, Loading, Menu, PageHeader, Stat, Stats, ask, toast } from "../ui";
import { ago, stamp } from "../format";
import { DataTable, type ColumnDef } from "@/components/data-table";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { PartnerCouponDialog } from "./PartnerCoupons";
import { LoadMore, money, ownRate, pageSize, partnerTone, payoutActions, payoutMethods, rulesSummary, toRuleInput, usePages, type Page } from "./Partners";
import { ConversionList, RewardList } from "./PartnerLedger";
import { ProofExport, downloadProof, payoutProofName, payoutTone, platformName } from "./PartnerProof";

type Go = (t: Tab, item?: string) => void;
type Activity = Schemas["PartnerActivity"];
export type Section = "referrals" | "conversions" | "rewards" | "payouts" | "activity";

const sections: [Section, string][] = [["referrals", "Referrals"], ["conversions", "Conversions"], ["rewards", "Rewards"], ["payouts", "Payouts"], ["activity", "Activity"]];

export const partnerName = (p: Pick<Partner, "name" | "email" | "code">) => p.name || p.email || p.code;

const day = (iso: string) => new Date(iso).toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" });

const plainClick = (e: MouseEvent) => e.button === 0 && !e.metaKey && !e.ctrlKey && !e.shiftKey && !e.altKey;

// Opens in place on a plain click; a modified click keeps the browser's own behavior.
export function AppLink({ ws, tab, item, onOpen, className, children }: { ws: string; tab: Tab; item?: string; onOpen?: () => void; className?: string; children: ReactNode }) {
  return (
    <a href={appPath(ws, tab, item)} className={className} onClick={(e) => {
      if (!onOpen || !plainClick(e)) return;
      e.preventDefault();
      onOpen();
    }}>
      {children}
    </a>
  );
}

async function setStatus(p: Partner, status: Partner["status"], done: () => void) {
  const { error } = await api.PATCH("/v1/brands/{brand}/partners/{id}", { params: { path: { brand: pickedBrandId(), id: p.id } }, body: { status } });
  if (error) return toast(errorMessage(error), "error");
  toast(status === "active" ? `${partnerName(p)} is active.` : `${partnerName(p)} is ${status}. Their links still land, uncounted.`);
  done();
}

export function partnerActions(p: Partner, done: () => void, addCode: () => void) {
  const label = partnerName(p);
  async function ledger(kind: "fees" | "adjustments") {
    const fee = kind === "fees";
    const r = await ask({
      title: fee ? "Log a fee" : "Correct the balance",
      body: fee ? "A fixed fee, such as for a post or a month. It counts toward their next payout and as a cost." : "Positive adds to what they're owed, negative takes away. Say why in the note.",
      input: { label: `Amount (${p.currency})`, type: "number", placeholder: fee ? "500" : "-20" },
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
    const { error } = await api.POST(`/v1/brands/{brand}/partners/{id}/${kind}`, { params: { path: { brand: pickedBrandId(), id: p.id } }, body: { amount_cents: cents, note } });
    if (error) return toast(errorMessage(error), "error");
    toast(fee ? "Fee logged." : "Balance corrected.");
    done();
  }
  async function postback() {
    const { data } = await api.GET("/v1/brands/{brand}/partners/{id}/postbacks", { params: { path: { brand: pickedBrandId(), id: p.id } } });
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
    const { error } = await api.PATCH("/v1/brands/{brand}/partners/{id}", { params: { path: { brand: pickedBrandId(), id: p.id } }, body: { postback_url: r.value.trim() } });
    if (error) return toast(errorMessage(error), "error");
    toast(r.value.trim() ? "Postback URL saved." : "Postbacks turned off.");
    done();
  }
  // rate gives a partner their own share of each sale, which replaces the
  // program's cash rate for them (their group's and tiers too).
  async function rate() {
    const cur = await api.GET("/v1/brands/{brand}/partner-programs/{id}", { params: { path: { brand: pickedBrandId(), id: p.program_id } } });
    if (cur.error || !cur.data) return toast(errorMessage(cur.error), "error");
    const mine = ownRate(cur.data.rules, p.id);
    const r = await ask({
      title: `Commission for ${label}`,
      body: `Their own share of each sale, instead of the program's (${rulesSummary({ ...cur.data, rules: cur.data.rules.filter((x) => !x.partner_id) })}). Leave it empty to go back to the program's rate.`,
      input: { label: "Percent of each sale", type: "number", value: mine?.kind === "percent" ? String((mine.percent_bp ?? 0) / 100) : "", placeholder: "30", optional: true },
      choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Save", value: "ok" }],
    });
    if (r?.choice !== "ok") return;
    const pct = r.value.trim() === "" ? null : Number(r.value);
    if (pct !== null && !(pct > 0 && pct <= 100)) return toast("Enter a percent from 0.01 to 100.", "error");
    const next = cur.data.rules.filter((x) => x !== mine).map(toRuleInput);
    if (pct !== null) next.push({ trigger: "sale", side: "partner", kind: "percent", percent_bp: Math.round(pct * 100), recurrence: "every", partner_id: p.id });
    const { error } = await api.PUT("/v1/brands/{brand}/partner-programs/{id}/rules", { params: { path: { brand: pickedBrandId(), id: p.program_id } }, body: { rules: next } });
    if (error) return toast(errorMessage(error), "error");
    toast(pct === null ? `${label} is back on the program's rate.` : `${label} earns ${pct}% of each sale from now on. Rewards already earned keep their amounts.`);
    done();
  }
  async function viewAs() {
    const { data, error } = await api.POST("/v1/brands/{brand}/partners/{id}/impersonate", { params: { path: { brand: pickedBrandId(), id: p.id } } });
    if (error || !data) return toast(errorMessage(error), "error");
    window.open(data.url, "_blank", "noopener");
  }
  return [
    ...(p.status !== "active" ? [{ label: p.status === "pending" ? "Approve" : "Reactivate", onClick: () => setStatus(p, "active", done) }] : []),
    ...(p.status === "pending" ? [{ label: "Reject", onClick: () => setStatus(p, "rejected", done) }] : []),
    ...(p.status === "active" ? [{ label: "Suspend", onClick: () => setStatus(p, "suspended", done) }] : []),
    { label: "Add promo code", onClick: addCode },
    { label: "Log a fee", onClick: () => ledger("fees") },
    { label: "Correct balance", onClick: () => ledger("adjustments") },
    { label: "Commission rate", onClick: rate },
    { label: "Postback URL", onClick: postback },
    { label: "View as partner", onClick: viewAs },
  ];
}

const sourceText: Record<Partner["source"], string> = { manual: "Added by hand", api: "Added through the API", signup: "Signed up on the portal", import: "Imported" };

export function PartnerPage({ ws, id, canManage, back, onLoaded, go, initialSection = "referrals" }: { ws: string; id: string; canManage: boolean; back: () => void; onLoaded: (p: Partner) => void; go: Go; initialSection?: Section }) {
  const [p, setP] = useState<Partner | null>(null);
  const [program, setProgram] = useState<(PartnerProgram & { groups: PartnerGroup[] }) | null>(null);
  const [error, setError] = useState("");
  const [section, setSection] = useState<Section>(initialSection);
  const [coupon, setCoupon] = useState(false);
  const [exporting, setExporting] = useState(false);
  const [tick, setTick] = useState(0);

  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/partners/{id}", { params: { path: { brand: pickedBrandId(), id } } });
    if (!data) return setError(errorMessage(error));
    setError("");
    setP(data);
    window.dispatchEvent(new CustomEvent("outflare:item-name", { detail: { id, name: partnerName(data) } }));
    onLoaded(data);
    const prog = await api.GET("/v1/brands/{brand}/partner-programs/{id}", { params: { path: { brand: pickedBrandId(), id: data.program_id } } });
    if (prog.data) setProgram(prog.data);
  }, [ws, id]);
  useEffect(() => {
    load();
  }, [load]);
  const changed = () => {
    load();
    setTick((t) => t + 1);
  };

  if (!p) {
    return error ? (
      <div className="space-y-6">
        <PageHeader title="Partner not found" back={{ label: "Partners", onClick: back }} />
        <ErrorText>{error}</ErrorText>
      </div>
    ) : <Loading />;
  }
  const c = p.currency;
  const s = p.stats;
  const earned = s.pending_cents + s.approved_cents + s.paid_cents;
  const held = s.held_at_source;
  const inPayouts = s.approved_cents - held.approved_cents - s.balance_cents;
  const platform = platformName(program?.imported_from ?? "");
  const group = program?.groups.find((g) => g.id === p.group_id);
  const method = payoutMethods.find(([m]) => m === p.payout_type)?.[1];
  const imported = p.source === "import" && program?.imported_from ? `Imported from ${platformName(program.imported_from)}` : sourceText[p.source];
  const openTab = (v: string) => {
    setSection(v as Section);
    track("ui.partner_tab_opened", { tab: v });
  };

  return (
    <div className="space-y-6">
      <div>
        <PageHeader
          back={{ label: "Partners", onClick: back }}
          title={<><span className="min-w-0 break-all">{partnerName(p)}</span><Badge tone={partnerTone[p.status]}>{p.status}</Badge></>}
          description={[p.name && p.email ? p.email : "", program ? `Partner in ${program.name}` : ""].filter(Boolean).join(" · ") || undefined}
          action={
            <>
              {canManage && p.status === "pending" && <Button onClick={() => setStatus(p, "active", changed)}>Approve</Button>}
              <Button variant="ghost" onClick={() => { setExporting(true); track("ui.partner_proof_export_opened"); }}>Download proof</Button>
              {canManage && <Menu label="Actions" aria="Partner actions" items={partnerActions(p, changed, () => { setCoupon(true); track("ui.partner_coupon_composer_opened"); })} />}
            </>
          }
        />
        <ErrorText>{error}</ErrorText>
      </div>

      <dl className="grid grid-cols-1 gap-px overflow-hidden rounded-lg border bg-border shadow-xs sm:grid-cols-2 lg:grid-cols-3">
        <Fact label="Code and link">
          <span className="block truncate font-mono">{p.code}</span>
          <span className="flex min-w-0 items-center gap-1">
            <span className="min-w-0 truncate font-mono text-[12px] text-muted-foreground">{p.link}</span>
            <CopyButton text={p.link} label="Copy link" />
          </span>
        </Fact>
        <Fact label="Promo codes">
          {p.promo_codes.length === 0 ? <span className="text-muted-foreground">None</span> : (
            <span className="block break-all font-mono">{p.promo_codes.map((x) => x.code + (x.status === "failed" ? " (not made in billing)" : x.status === "pending" ? " (making…)" : "")).join(", ")}</span>
          )}
        </Fact>
        <Fact label="Group">{group ? <span className="block truncate">{group.name}</span> : <span className="text-muted-foreground">No group</span>}</Fact>
        <Fact label="Payout">
          <span className="flex flex-wrap items-center gap-2">
            {method && <span>{`${method}${p.payout_type === "crypto" && p.payout_token ? ` (${p.payout_token})` : ""}`}</span>}
            {p.payout_method ? <Badge tone="green">details added</Badge> : <Badge tone="amber">no payout details</Badge>}
          </span>
        </Fact>
        <Fact label="Joined"><span title={stamp(p.created_at)}>{day(p.created_at)}</span></Fact>
        <Fact label="Came from">{imported}</Fact>
      </dl>

      <div className="[&>div>div:last-child]:col-span-2 sm:[&>div>div:last-child]:col-span-1">
      <Stats cols={3}>
        <Stat label="Clicks" value={s.clicks.toLocaleString()} sub="on their short link" />
        <Stat label="Referrals" value={s.referrals.toLocaleString()} sub="customers credited to them" />
        <Stat label="Customers" value={s.customers.toLocaleString()} sub={s.referrals ? `${Math.round((s.customers / s.referrals) * 100)}% of referrals bought` : undefined} />
        <Stat label="Sales" value={money(s.sales_cents, c)} sub="less refunds" />
        <Stat label="Commission earned" value={money(earned, c)} sub="on hold, approved and paid" />
        <Stat label="On hold" value={money(s.pending_cents - held.pending_cents, c)} sub={held.pending_cents ? `What Outflare pays; ${platform} pays ${money(held.pending_cents, c)} more` : "pending, not approved yet"} />
        <Stat label="Approved" value={money(s.approved_cents - held.approved_cents, c)} sub={held.approved_cents ? `What Outflare pays; ${platform} pays ${money(held.approved_cents, c)} more` : "not paid yet"} />
        <Stat label="Paid" value={money(s.paid_cents, c)} />
        <Stat label="Balance due" value={money(s.balance_cents, c)} sub={inPayouts > 0 ? `${money(inPayouts, c)} more in due payouts` : "the next payout gathers this"} />
      </Stats>
      </div>

      <Tabs value={section} onValueChange={openTab}>
        <TabsList className="max-w-full flex-wrap group-data-[orientation=horizontal]/tabs:h-auto">
          {sections.map(([v, label]) => <TabsTrigger key={v} value={v} className="h-9 px-3 text-[12px]">{label}</TabsTrigger>)}
        </TabsList>
      </Tabs>
      {section === "referrals" && <Referrals key={tick} ws={ws} p={p} go={go} />}
      {section === "conversions" && <ConversionList key={tick} ws={ws} go={go} partnerId={p.id} />}
      {section === "rewards" && (
        <RewardList key={tick} ws={ws} go={go} canManage={canManage} slug={p.program} importedFrom={program?.imported_from ?? ""} partnerId={p.id} onChanged={load}
          openPayouts={() => openTab("payouts")} />
      )}
      {section === "payouts" && <Payouts key={tick} p={p} canManage={canManage} onChanged={changed} />}
      {section === "activity" && <ActivityList key={tick} p={p} />}
      {coupon && <PartnerCouponDialog ws={ws} partner={p} currency={c} onClose={() => setCoupon(false)} onSaved={() => { setCoupon(false); changed(); }} />}
      {exporting && <ProofExport partner={p.id} slug={p.program} onClose={() => setExporting(false)} />}
    </div>
  );
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="min-w-0 bg-card px-4 py-3">
      <dt className="truncate font-mono text-[11px] tracking-wide text-muted-foreground uppercase">{label}</dt>
      <dd className="mt-1 min-w-0 space-y-0.5 text-[13px]">{children}</dd>
    </div>
  );
}

function usePartnerPages<T>(path: "/v1/brands/{brand}/partner-referrals" | "/v1/brands/{brand}/partner-payouts", partner: string) {
  const get = useCallback(async (cursor?: string): Promise<Page<T>> => {
    const { data, error } = await api.GET(path, { params: { path: { brand: pickedBrandId() }, query: { partner_id: partner, limit: pageSize, ...(cursor ? { cursor } : {}) } } });
    return { list: (data?.data ?? []) as T[], next: data?.next_cursor ?? null, error: error ? errorMessage(error) : "" };
  }, [path, partner]);
  const pages = usePages(get);
  const { reload } = pages;
  useEffect(() => {
    reload();
  }, [reload]);
  return pages;
}

export const viaText: Record<PartnerReferral["via"], string> = { ref: "Campaign link", click: "Short link", promo_code: "Promo code", visit: "Website visit", manual: "Added by hand", import: "Imported" };
const referralTone = { lead: "grey", customer: "green", refunded: "red", rejected: "grey" } as const;

function Referrals({ ws, p, go }: { ws: string; p: Partner; go: Go }) {
  const { list, next, error, more } = usePartnerPages<PartnerReferral>("/v1/brands/{brand}/partner-referrals", p.id);
  if (!list) return <Loading />;
  const customer = (r: PartnerReferral) => (
    <span className="block min-w-0">
      <AppLink ws={ws} tab="people" item={r.record_id} onOpen={() => go("people", r.record_id)} className="block truncate font-medium underline-offset-2 hover:underline">
        {r.customer_name || r.customer_email || "Unknown customer"}
      </AppLink>
      {r.customer_name && r.customer_email && <span className="block truncate text-[12px] text-muted-foreground">{r.customer_email}</span>}
    </span>
  );
  const columns: ColumnDef<PartnerReferral>[] = [
    { id: "customer", header: "Customer", accessorFn: (r) => r.customer_name || r.customer_email, cell: ({ row: { original: r } }) => customer(r) },
    {
      id: "via", header: "Via", accessorFn: (r) => viaText[r.via] ?? r.via,
      cell: ({ row: { original: r } }) => <span className="block">{viaText[r.via] ?? r.via}{r.sub_id && <span className="block font-mono text-[12px] text-muted-foreground">sub {r.sub_id}</span>}</span>,
    },
    { id: "promo", header: "Promo code", accessorFn: (r) => r.promo_code, cell: ({ getValue }) => <span className="font-mono text-[12px]">{getValue() as string}</span> },
    { id: "when", header: "When", accessorFn: (r) => r.created_at, cell: ({ row: { original: r } }) => <span title={stamp(r.created_at)}>{ago(r.created_at)}</span> },
    { id: "status", header: "Status", accessorFn: (r) => r.status, cell: ({ row: { original: r } }) => <Badge tone={referralTone[r.status]}>{r.status}</Badge> },
    { id: "first", header: "First sale", accessorFn: (r) => r.first_sale_at ?? "", cell: ({ row: { original: r } }) => (r.first_sale_at ? day(r.first_sale_at) : <span className="text-muted-foreground">None yet</span>) },
    { id: "sales", header: "Lifetime sales", accessorFn: (r) => r.sales_cents, cell: ({ row: { original: r } }) => money(r.sales_cents, p.currency) },
  ];
  return (
    <div className="space-y-3">
      <ErrorText>{error}</ErrorText>
      <DataTable
        columns={columns}
        data={list}
        rowId={(r) => r.id}
        empty="No referrals yet. A customer shows here once this partner's link, promo code or a visit credits them."
        mobile={(r) => (
          <span className="block min-w-0">
            {customer(r)}
            <span className="block truncate text-[12px] text-muted-foreground">{r.status} · {viaText[r.via] ?? r.via}{r.promo_code && ` ${r.promo_code}`} · {money(r.sales_cents, p.currency)} · {ago(r.created_at)}</span>
          </span>
        )}
      />
      <LoadMore next={next} onMore={more} shown={list.length} />
    </div>
  );
}

function Payouts({ p, canManage, onChanged }: { p: Partner; canManage: boolean; onChanged: () => void }) {
  const { list, next, error, more } = usePartnerPages<PartnerPayout>("/v1/brands/{brand}/partner-payouts", p.id);
  if (!list) return <Loading />;
  const proof = (o: PartnerPayout) => <Button variant="ghost" onClick={() => downloadProof({ payout_id: o.id }, payoutProofName(p.program, o.id))}>Download proof</Button>;
  const columns: ColumnDef<PartnerPayout>[] = [
    { id: "amount", header: "Amount", accessorFn: (o) => o.amount_cents, cell: ({ row: { original: o } }) => money(o.amount_cents, o.currency) },
    { id: "status", header: "Status", accessorFn: (o) => o.status, cell: ({ row: { original: o } }) => <Badge tone={payoutTone[o.status]}>{o.status}</Badge> },
    { id: "reference", header: "Reference", accessorFn: (o) => o.reference, cell: ({ getValue }) => <span className="font-mono text-[12px]">{getValue() as string}</span> },
    { id: "paid", header: "Paid", accessorFn: (o) => o.paid_at ?? "", cell: ({ row: { original: o } }) => (o.paid_at ? <span title={stamp(o.paid_at)}>{day(o.paid_at)}</span> : <span className="text-muted-foreground">Not yet</span>) },
    { id: "rewards", header: "Rewards", accessorFn: (o) => o.rewards },
    { id: "made", header: "Made", accessorFn: (o) => o.created_at, cell: ({ row: { original: o } }) => <span title={stamp(o.created_at)}>{ago(o.created_at)}</span> },
    {
      id: "actions", header: "", enableSorting: false, enableHiding: false,
      cell: ({ row: { original: o } }) => <span className="flex items-center justify-end gap-1">{proof(o)}{canManage && o.status === "due" && <Menu aria="Payout actions" items={payoutActions(o, onChanged)} />}</span>,
    },
  ];
  return (
    <div className="space-y-3">
      <ErrorText>{error}</ErrorText>
      <DataTable
        columns={columns}
        data={list}
        rowId={(o) => o.id}
        empty="No payouts yet. Make due payouts on the program's Payouts tab once this partner's balance reaches the minimum."
        mobile={(o) => (
          <span className="flex items-start justify-between gap-3">
            <span className="block min-w-0">
              <span className="block truncate font-medium">{money(o.amount_cents, o.currency)} · {o.status}</span>
              <span className="block truncate text-[12px] text-muted-foreground">{o.paid_at ? `paid ${day(o.paid_at)}` : `made ${ago(o.created_at)}`} · {o.rewards} rewards{o.reference && <span className="font-mono"> · {o.reference}</span>}</span>
            </span>
            <span className="flex shrink-0 items-center gap-1">{proof(o)}{canManage && o.status === "due" && <Menu aria="Payout actions" items={payoutActions(o, onChanged)} />}</span>
          </span>
        )}
      />
      <LoadMore next={next} onMore={more} shown={list.length} />
    </div>
  );
}

const str = (v: unknown) => (typeof v === "string" ? v : "");
const fieldText: Record<string, string> = { payout_method: "payout details", payout_type: "payout method", payout_token: "payout token", group_id: "group", postback_url: "postback URL" };
const changedText = (d: Activity["data"]) => (Array.isArray(d.changed) ? d.changed : []).filter((f): f is string => typeof f === "string").map((f) => fieldText[f] ?? f.replaceAll("_", " ")).join(", ");
const amountOf = (d: Activity["data"], currency: string) => (typeof d.amount_cents === "number" ? money(d.amount_cents, str(d.currency) || currency) : "");
const ofAmount = (thing: string, d: Activity["data"], currency: string) => (amountOf(d, currency) ? `${thing} of ${amountOf(d, currency)}` : thing);

const activityText: Record<string, (d: Activity["data"], currency: string) => string> = {
  "partner.created": (d) => (str(d.by) === "import" ? "Imported into the program" : "Joined the program"),
  "partner.joined": () => "Signed up on the portal",
  "partner.updated": (d) => {
    const now = str(d.status), was = str(d.previous_status);
    if (now && was && now !== was) return `Status changed from ${was} to ${now}`;
    const what = changedText(d);
    return what ? `Changed the ${what}` : "Details changed";
  },
  "partner.group_switched": () => "Moved to another group",
  "partner.portal_updated": (d) => (changedText(d) ? `Changed their ${changedText(d)} on the portal` : "Changed their details on the portal"),
  "partner.promo_code_added": (d) => (str(d.code) ? `Promo code ${str(d.code)} added` : "Promo code added"),
  "partner.promo_code_created": (d) => (str(d.code) ? `Promo code ${str(d.code)} made in billing` : "Promo code made in billing"),
  "partner.promo_code_updated": (d) => (str(d.code) ? `Promo code ${str(d.code)} changed` : "Promo code changed"),
  "partner.promo_code_removed": (d) => (str(d.code) ? `Promo code ${str(d.code)} removed` : "Promo code removed"),
  "partner.promo_code_failed": () => "A promo code couldn't be made in billing",
  "partner.terms_accepted": () => "Accepted the program terms",
  "partner.signed_in": () => "Signed in to the portal",
  "partner.impersonated": () => "Portal viewed as this partner",
  "partner.portal_handoff_created": () => "Portal sign-in link made",
  "partner_referral.created": (d) => {
    const via = str(d.via) as PartnerReferral["via"];
    return via ? `Customer referred by ${(viaText[via] ?? via).toLowerCase()}${str(d.code) ? ` ${str(d.code)}` : ""}` : "Customer referred";
  },
  "partner_reward.created": (d, c) => {
    const kind = str(d.kind);
    if (kind === "fee") return `${ofAmount("Fee", d, c)} logged`;
    if (kind === "adjustment") return amountOf(d, c) ? `Balance corrected by ${amountOf(d, c)}` : "Balance corrected";
    if (kind === "action") return str(d.side) === "referred" ? "Reward earned for the friend" : "Reward earned";
    return amountOf(d, c) ? `Earned ${amountOf(d, c)}` : "Reward earned";
  },
  "partner_reward.approved": (d, c) => `${ofAmount("Reward", d, c)} approved${str(d.by) === "auto" ? " after the hold" : ""}`,
  "partner_reward.rejected": (d, c) => `${ofAmount("Reward", d, c)} rejected${str(d.reason) ? `: ${str(d.reason)}` : ""}`,
  "partner_reward.reversed": (d, c) => `${ofAmount("Reward", d, c)} taken back${str(d.by) === "refund" ? " after a refund" : ""}`,
  "partner_reward.fulfilled": () => "Reward fulfilled",
  "partner_payout.created": (d, c) => `${ofAmount("Payout", d, c)} made due`,
  "partner_payout.paid": (d, c) => `${ofAmount("Payout", d, c)} marked paid${str(d.reference) ? `, reference ${str(d.reference)}` : ""}`,
  "partner_payout.canceled": (d, c) => `${ofAmount("Payout", d, c)} canceled`,
};

const describe = (a: Activity, currency: string) => activityText[a.action]?.(a.data ?? {}, currency) ?? a.action;
const actorText: Record<Activity["actor_type"], string> = { human: "A member", api_token: "An API token", service_account: "A service account", agent: "An agent", system: "Outflare" };
const who = (a: Activity) => a.actor_name || (a.actor_type === "system" ? "Outflare" : actorText[a.actor_type] ?? a.actor_type);

function ActivityList({ p }: { p: Partner }) {
  const get = useCallback(async (cursor?: string): Promise<Page<Activity>> => {
    const { data, error } = await api.GET("/v1/brands/{brand}/partners/{id}/activity", { params: { path: { brand: pickedBrandId(), id: p.id }, query: { limit: pageSize, ...(cursor ? { cursor } : {}) } } });
    return { list: data?.data ?? [], next: data?.next_cursor ?? null, error: error ? errorMessage(error) : "" };
  }, [p.id]);
  const { list, next, error, reload, more } = usePages(get);
  useEffect(() => {
    reload();
  }, [reload]);
  if (!list) return <Loading />;
  const columns: ColumnDef<Activity>[] = [
    { id: "what", header: "What happened", accessorFn: (a) => describe(a, p.currency) },
    { id: "who", header: "Who", accessorFn: who },
    { id: "when", header: "When", accessorFn: (a) => a.created_at, cell: ({ row: { original: a } }) => <span title={stamp(a.created_at)}>{ago(a.created_at)}</span> },
  ];
  return (
    <div className="space-y-3">
      <ErrorText>{error}</ErrorText>
      <DataTable
        columns={columns}
        data={list}
        rowId={(a) => a.id}
        empty="Nothing recorded for this partner yet."
        mobile={(a) => (
          <span className="block min-w-0">
            <span className="block">{describe(a, p.currency)}</span>
            <span className="block truncate text-[12px] text-muted-foreground">{who(a)} · {ago(a.created_at)}</span>
          </span>
        )}
      />
      <LoadMore next={next} onMore={more} shown={list.length} />
    </div>
  );
}
