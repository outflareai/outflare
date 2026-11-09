import { useCallback, useEffect, useRef, useState } from "react";
import type { PartnerConversion, PartnerPayout } from "@outflare/sdk";
import { X } from "lucide-react";
import { api, errorMessage } from "../api";
import { pickedBrandId } from "@/lib/brand";
import { track } from "@/lib/analytics";
import type { Tab } from "@/lib/paths";
import { Badge, Button, Empty, ErrorText, field, Loading, Menu, ask, toast } from "../ui";
import { ago, plural, stamp } from "../format";
import { DataTable, type ColumnDef } from "@/components/data-table";
import { LoadMore, money, pageSize, rewardActions, rewardTone, usePages, type Page } from "./Partners";
import { AppLink, type Section } from "./PartnerPage";
import { ProofDrawer, ProofLink, SourceBadges, decisionText, netText, payoutTone, platformName, rewardWhat, ruleSummary, type RewardRow } from "./PartnerProof";

type Go = (t: Tab, item?: string) => void;
type RewardStatus = RewardRow["status"];
type RewardSource = "native" | "imported" | "held_at_source";
type ConversionType = PartnerConversion["type"];
type OpenPartner = (id: string, section?: Section) => void;

function useDebounced(text: string, ms = 300) {
  const [value, setValue] = useState(text.trim());
  useEffect(() => {
    const t = setTimeout(() => setValue(text.trim()), ms);
    return () => clearTimeout(t);
  }, [text, ms]);
  return value;
}

// useSearchStarted records a search starting, never its text.
function useSearchStarted(q: string, event: "ui.partner_rewards_searched" | "ui.partner_conversions_searched", where: "program" | "partner") {
  const was = useRef(q);
  useEffect(() => {
    if (q && !was.current) track(event, { where });
    was.current = q;
  }, [q, event, where]);
}

const actionsColumn = <T,>(cell: ColumnDef<T>["cell"]): ColumnDef<T> => ({ id: "actions", header: "", enableSorting: false, enableHiding: false, meta: { pin: "right" }, cell });

function PartnerCell({ ws, id, code, name, open }: { ws: string; id: string; code: string; name: string; open?: OpenPartner }) {
  return (
    <span className="block w-36">
      <AppLink ws={ws} tab="partners" item={id} onOpen={open && (() => open(id))} className="block truncate font-medium underline-offset-2 hover:underline">{name || code}</AppLink>
      {name && <span className="block truncate font-mono text-[12px] text-muted-foreground">{code}</span>}
    </span>
  );
}

function CustomerCell({ ws, go, id, name, email }: { ws: string; go: Go; id: string | null; name: string; email: string }) {
  if (!id) return <span className="text-muted-foreground">No customer</span>;
  return (
    <span className="block w-36">
      <AppLink ws={ws} tab="people" item={id} onOpen={() => go("people", id)} className="block truncate underline-offset-2 hover:underline">{name || email || "Unnamed customer"}</AppLink>
      {name && email && <span className="block truncate text-[12px] text-muted-foreground">{email}</span>}
    </span>
  );
}

type RewardListProps = {
  ws: string;
  go: Go;
  canManage: boolean;
  slug: string;
  importedFrom: string;
  programId?: string;
  partnerId?: string;
  openPartner?: OpenPartner;
  openPayouts: (partner: string) => void;
  onChanged?: () => void;
};

export function RewardList({ ws, go, canManage, slug, importedFrom, programId, partnerId, openPartner, openPayouts, onChanged }: RewardListProps) {
  const where = partnerId ? "partner" : "program";
  const platform = platformName(importedFrom);
  const bulk = canManage && !partnerId && !!programId;
  const [status, setStatus] = useState<RewardStatus | "">("");
  const [source, setSource] = useState<RewardSource | "">("");
  const [text, setText] = useState("");
  const q = useDebounced(text);
  useSearchStarted(q, "ui.partner_rewards_searched", where);
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState(false);
  const [proof, setProof] = useState("");
  const [payouts, setPayouts] = useState<Map<string, PartnerPayout>>(new Map());

  const get = useCallback(async (cursor?: string): Promise<Page<RewardRow>> => {
    const { data, error } = await api.GET("/v1/brands/{brand}/partner-rewards", {
      params: { path: { brand: pickedBrandId() }, query: {
        ...(partnerId ? { partner_id: partnerId } : { program: programId }), limit: pageSize,
        ...(status ? { status } : {}), ...(source ? { source } : {}), ...(q ? { q } : {}), ...(cursor ? { cursor } : {}),
      } },
    });
    return { list: data?.data ?? [], next: data?.next_cursor ?? null, error: error ? errorMessage(error) : "" };
  }, [programId, partnerId, status, source, q]);
  const { list, next, error, reload, more } = usePages(get);
  const load = useCallback(async () => {
    await reload();
    setPicked(new Set());
  }, [reload]);
  useEffect(() => {
    load();
  }, [load]);
  useEffect(() => {
    if (!partnerId) return;
    api.GET("/v1/brands/{brand}/partner-payouts", { params: { path: { brand: pickedBrandId() }, query: { partner_id: partnerId, limit: 500 } } })
      .then(({ data }) => setPayouts(new Map((data?.data ?? []).map((o) => [o.id, o]))));
  }, [partnerId]);
  const changed = () => {
    load();
    onChanged?.();
  };
  const openProof = (id: string) => {
    track("ui.partner_proof_opened", { from: where });
    setProof(id);
  };
  const filter = (what: "status" | "source", value: string) => track("ui.partner_rewards_filtered", { filter: what, value: value || "all", where });

  // many approves or rejects the picked rewards, or every pending one in the
  // program, after a dry run says what that touches by source.
  async function many(what: "approve" | "reject") {
    const ids = [...picked];
    const target = ids.length > 0 ? { ids } : { program: programId };
    const dry = await api.POST("/v1/brands/{brand}/partner-rewards/bulk", {
      params: { path: { brand: pickedBrandId() } },
      body: { action: what, ...target, dry_run: true, include_held_at_source: true },
    });
    if (dry.error || !dry.data) return toast(errorMessage(dry.error), "error");
    const { native, imported, held_at_source: held } = dry.data.counts;
    const n = native + imported;
    if (n + held === 0) return toast(ids.length > 0 ? "None of the picked rewards are pending." : "No pending rewards in this program.");
    track("ui.partner_rewards_bulk_confirm_opened", { action: what, scope: ids.length > 0 ? "picked" : "all", native, imported, held_at_source: held });
    const verb = what === "approve" ? "Approve" : "Reject";
    const parts = [native > 0 && `${native.toLocaleString()} made by Outflare`, imported > 0 && `${imported.toLocaleString()} imported that Outflare pays`].filter(Boolean).join(", ");
    const r = await ask({
      title: ids.length > 0 ? `${verb} the picked rewards?` : `${verb} every pending reward in this program?`,
      body: (
        <>
          <span className="block">
            {n > 0 ? `${verb} ${plural(n, "reward")}: ${parts}.` : `All of them are imported, and ${platform} still pays them.`}
            {held > 0 && n > 0 && ` ${plural(held, "imported reward")} ${platform} still pays ${held === 1 ? "is" : "are"} left out.`}
          </span>
          {held > 0 && <span className="block text-muted-foreground">Including those changes their status here only. {platform} pays them until you switch over, and Outflare never puts them in a payout.</span>}
          <span className="block text-muted-foreground">{what === "approve" ? "Cash counts toward the next payouts and is logged as a cost; actions are sent to your system." : "Say why; the partners can see it."}</span>
        </>
      ),
      ...(what === "reject" ? { input: { label: "Reason", maxLength: 2000, optional: true } } : {}),
      choices: [
        { label: "Cancel", value: "cancel", variant: "ghost" },
        ...(held > 0 && n > 0 ? [{ label: `${verb} all ${(n + held).toLocaleString()}, with ${platform}'s`, value: "all", variant: "ghost" as const }] : []),
        n > 0
          ? { label: `${verb} ${n.toLocaleString()}`, value: "ok", variant: what === "approve" ? undefined : ("danger-solid" as const) }
          : { label: `${verb} ${held.toLocaleString()} ${platform} pays`, value: "all", variant: what === "approve" ? undefined : ("danger-solid" as const) },
      ],
    });
    if (r?.choice !== "ok" && r?.choice !== "all") return;
    const include = r.choice === "all";
    if (include) track("ui.partner_rewards_held_included", { action: what });
    setBusy(true);
    let done = 0;
    const notPending = new Set<string>(), leftOut = new Set<string>();
    for (;;) {
      const { data, error } = await api.POST("/v1/brands/{brand}/partner-rewards/bulk", {
        params: { path: { brand: pickedBrandId() } },
        body: { action: what, ...target, ...(include ? { include_held_at_source: true } : {}), ...(what === "reject" && r.value.trim() ? { note: r.value.trim() } : {}) },
      });
      if (error || !data) {
        setBusy(false);
        return toast(errorMessage(error), "error");
      }
      done += data.done;
      const seen = notPending.size + leftOut.size;
      for (const s of data.skipped) (s.reason === "held_at_source" ? leftOut : notPending).add(s.id);
      if (!data.more || (data.done === 0 && notPending.size + leftOut.size === seen)) break;
    }
    setBusy(false);
    const notes = [leftOut.size > 0 && `left out ${leftOut.size} ${platform} still pays`, notPending.size > 0 && `${notPending.size} weren't pending`].filter(Boolean);
    toast(`${what === "approve" ? "Approved" : "Rejected"} ${plural(done, "reward")}${notes.length ? `; ${notes.join("; ")}` : ""}.`);
    changed();
  }
  const toggle = (id: string) => setPicked((cur) => {
    const next = new Set(cur);
    if (!next.delete(id)) next.add(id);
    return next;
  });

  if (!list) return <Loading />;
  const pending = list.filter((w) => w.status === "pending");
  const amount = (w: RewardRow) => (w.kind === "action" ? rewardWhat(w) : `${money(w.amount_cents, w.currency)} ${rewardWhat(w)}`);
  const links = (w: RewardRow) => (
    <>
      {netText(w) && <span className="block text-[12px] text-muted-foreground">{netText(w)}</span>}
      {(w.reverses_id || w.reversed_by.length > 0) && (
        <span className="flex flex-wrap gap-x-3">
          {w.reverses_id && <ProofLink onClick={() => openProof(w.reverses_id!)}>Reverses a reward</ProofLink>}
          {w.reversed_by.map((id, n) => <ProofLink key={id} onClick={() => openProof(id)}>Reversed by{w.reversed_by.length > 1 ? ` (${n + 1})` : ""}</ProofLink>)}
        </span>
      )}
    </>
  );
  const paidOn = (w: RewardRow) => {
    const o = w.payout_id ? payouts.get(w.payout_id) : undefined;
    return o?.paid_at ? `Paid ${new Date(o.paid_at).toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" })}` : "";
  };
  const payoutCell = (w: RewardRow) => {
    if (!w.payout_status) return <span className="block w-24 text-muted-foreground">{w.held_at_source ? `${platform} pays it` : "Not in a payout"}</span>;
    const o = w.payout_id ? payouts.get(w.payout_id) : undefined;
    return (
      <span className="block">
        <Badge tone={payoutTone[w.payout_status]}>{w.payout_status}</Badge>
        {paidOn(w) && <span className="block text-[12px] whitespace-nowrap text-muted-foreground">{paidOn(w)}</span>}
        {o?.reference && <span className="block font-mono text-[12px] text-muted-foreground">{o.reference}</span>}
      </span>
    );
  };
  const decided = (w: RewardRow) => {
    if (!w.decision) return <span className="text-muted-foreground">{w.status === "pending" ? "Not yet" : "Not on record"}</span>;
    const t = decisionText(w.decision, platformName(w.imported_from || importedFrom));
    return (
      <span className="block w-40 whitespace-normal">
        <span className="block">{t.what}</span>
        <span className="block text-[12px] text-muted-foreground">{[t.how, w.decision.at && ago(w.decision.at)].filter(Boolean).join(" · ")}</span>
      </span>
    );
  };
  const proofButton = (w: RewardRow) => <Button variant="ghost" onClick={() => openProof(w.id)} aria-label="Proof">Proof</Button>;
  const menu = (w: RewardRow) => canManage && w.status === "pending" && <Menu aria="Reward actions" items={rewardActions(w, changed)} />;

  const columns: ColumnDef<RewardRow>[] = [
    ...(bulk ? [{
      id: "pick", enableSorting: false, enableHiding: false,
      header: () => pending.length > 0 && (
        <input type="checkbox" aria-label="Pick every pending reward" checked={picked.size > 0 && picked.size === pending.length}
          onChange={(e) => setPicked(e.target.checked ? new Set(pending.map((w) => w.id)) : new Set())} />
      ),
      cell: ({ row: { original: w } }: { row: { original: RewardRow } }) => w.status === "pending" && (
        <input type="checkbox" aria-label="Pick this reward" checked={picked.has(w.id)} onChange={() => toggle(w.id)} />
      ),
    } as ColumnDef<RewardRow>] : []),
    ...(partnerId ? [] : [{
      id: "partner", header: "Partner", accessorFn: (w: RewardRow) => w.partner_code,
      cell: ({ row: { original: w } }: { row: { original: RewardRow } }) => <PartnerCell ws={ws} id={w.partner_id} code={w.partner_code} name={w.partner_name} open={openPartner} />,
    } as ColumnDef<RewardRow>]),
    { id: "customer", header: "Customer", accessorFn: (w) => w.customer_email, cell: ({ row: { original: w } }) => <CustomerCell ws={ws} go={go} id={w.customer_record_id} name={w.customer_name} email={w.customer_email} /> },
    {
      id: "sale", header: "Sale", accessorFn: (w) => w.sale?.external_id ?? "",
      cell: ({ row: { original: w } }) => (w.sale ? (
        <span className="block min-w-0 max-w-48">
          {w.sale.external_id && <span className="block truncate font-mono text-[12px]" title={w.sale.external_id}>{w.sale.external_id}</span>}
          <span className={w.sale.external_id ? "block text-[12px] text-muted-foreground" : "block"}>{w.sale.type === "signup" ? "Sign-up" : money(w.sale.type === "refund" ? -w.sale.amount_cents : w.sale.amount_cents, w.sale.currency)}{w.sale.type === "refund" && " refund"}</span>
        </span>
      ) : <span className="text-muted-foreground">No sale</span>),
    },
    { id: "reward", header: "Reward", accessorFn: (w) => w.amount_cents, cell: ({ row: { original: w } }) => <span className="block w-32"><span className="block whitespace-nowrap">{amount(w)}</span>{links(w)}</span> },
    {
      id: "status", header: "Status", accessorFn: (w) => w.status,
      cell: ({ row: { original: w } }) => (
        <span className="block">
          <Badge tone={rewardTone[w.status]}>{w.status}</Badge>
          {w.status === "pending" && <span className="block text-[12px] whitespace-nowrap text-muted-foreground" title={stamp(w.hold_until)}>until {new Date(w.hold_until).toLocaleDateString()}</span>}
        </span>
      ),
    },
    { id: "rule", header: "Rule", accessorFn: (w) => w.rule?.name ?? "", cell: ({ row: { original: w } }) => (w.rule ? <span className="block w-40 text-[12px]">{ruleSummary(w.rule, w.currency)}</span> : <span className="text-muted-foreground">No rule</span>) },
    { id: "source", header: "Source", accessorFn: (w) => w.source, cell: ({ row: { original: w } }) => <SourceBadges w={w} className="flex flex-col items-start gap-1" /> },
    { id: "decided", header: "Decided", accessorFn: (w) => w.decision?.at ?? "", cell: ({ row: { original: w } }) => decided(w) },
    { id: "payout", header: "Payout", accessorFn: (w) => w.payout_status ?? "", cell: ({ row: { original: w } }) => payoutCell(w) },
    { id: "note", header: "Note", accessorFn: (w) => w.note, cell: ({ getValue }) => <span className="block max-w-56 text-[12px] break-words text-muted-foreground">{getValue() as string}</span> },
    { id: "created", header: "Made", accessorFn: (w) => w.created_at, cell: ({ row: { original: w } }) => <span className="whitespace-nowrap" title={stamp(w.created_at)}>{ago(w.created_at)}</span> },
    actionsColumn<RewardRow>(({ row: { original: w } }) => <span className="flex items-center justify-end gap-1">{proofButton(w)}{menu(w)}</span>),
  ];

  const filtered = !!(q || source);
  return (
    <div className="space-y-3">
      <ErrorText>{error}</ErrorText>
      <DataTable
        columns={columns}
        data={list}
        rowId={(w) => w.id}
        hidden={["rule", "note", "created"]}
        searchPlaceholder="Search by partner code or email, customer email or your id"
        search={{ value: text, onChange: setText }}
        toolbar={
          <span className="flex flex-wrap items-center gap-2">
            <select aria-label="Status" className={field} value={status} onChange={(e) => { setStatus(e.target.value as RewardStatus | ""); filter("status", e.target.value); }}>
              <option value="">Every status</option>
              {(["pending", "approved", "paid", "fulfilled", "rejected"] as const).map((s) => <option key={s} value={s}>{s}</option>)}
            </select>
            <select aria-label="Source" className={field} value={source} onChange={(e) => { setSource(e.target.value as RewardSource | ""); filter("source", e.target.value); }}>
              <option value="">Every source</option>
              <option value="native">Made by Outflare</option>
              <option value="imported">Imported</option>
              <option value="held_at_source">Imported, {platform} pays</option>
            </select>
            {bulk && (picked.size > 0 || (pending.length > 0 && !filtered)) && (
              <>
                <Button variant="ghost" disabled={busy} onClick={() => many("approve")}>{picked.size > 0 ? `Approve ${picked.size}` : "Approve all pending"}</Button>
                <Button variant="ghost" disabled={busy} onClick={() => many("reject")}>{picked.size > 0 ? `Reject ${picked.size}` : "Reject all pending"}</Button>
              </>
            )}
          </span>
        }
        empty={partnerId ? "No rewards yet. They appear when a customer this partner referred signs up or buys." : "No rewards yet. They appear when a referred customer signs up or buys."}
        mobile={(w) => (
          <span className="flex items-start justify-between gap-3">
            <span className="block min-w-0">
              <span className="block truncate font-medium">{amount(w)} · {w.status}</span>
              <span className="block truncate text-[12px] text-muted-foreground">
                {[!partnerId && w.partner_code, w.customer_name || w.customer_email, w.sale?.external_id].filter(Boolean).join(" · ") || ago(w.created_at)}
              </span>
              {w.payout_status && <span className="block truncate text-[12px] text-muted-foreground">{paidOn(w) || `In a ${w.payout_status} payout`}</span>}
              <span className="mt-1 block"><SourceBadges w={w} /></span>
              {links(w)}
            </span>
            <span className="flex shrink-0 items-center gap-1">{proofButton(w)}{menu(w)}</span>
          </span>
        )}
      />
      <LoadMore next={next} onMore={more} />
      {proof && <ProofDrawer key={proof} ws={ws} id={proof} slug={slug} go={go} onClose={() => setProof("")} openPayouts={(p) => { setProof(""); openPayouts(p); }} />}
    </div>
  );
}

const conversionTypes: [ConversionType, string][] = [["signup", "Sign-ups"], ["sale", "Sales"], ["refund", "Refunds"], ["custom", "Custom"]];

type ConversionListProps = { ws: string; go: Go; programId?: string; partnerId?: string; openPartner?: OpenPartner };

export function ConversionList({ ws, go, programId, partnerId, openPartner }: ConversionListProps) {
  const where = partnerId ? "partner" : "program";
  const [text, setText] = useState("");
  const [q, setQ] = useState("");
  const [type, setType] = useState<ConversionType | "">("");
  const [partner, setPartner] = useState<{ id: string; code: string } | null>(null);
  useSearchStarted(q, "ui.partner_conversions_searched", where);
  const pickPartner = useCallback((p: { id: string; code: string }, from: "code" | "link") => {
    track("ui.partner_conversions_filtered", { filter: "partner", from });
    setPartner({ id: p.id, code: p.code });
    setText("");
    setQ("");
  }, []);
  useEffect(() => {
    const s = text.trim();
    if (s === q) return;
    let live = true;
    const t = setTimeout(async () => {
      if (programId && !partner && s) {
        const { data } = await api.GET("/v1/brands/{brand}/partners", { params: { path: { brand: pickedBrandId() }, query: { program: programId, q: s, limit: 5 } } });
        const same = (data?.data ?? []).filter((p) => p.code.toLowerCase() === s.toLowerCase());
        if (!live) return;
        if (same.length === 1) return pickPartner(same[0], "code");
      }
      if (live) setQ(s);
    }, 300);
    return () => {
      live = false;
      clearTimeout(t);
    };
  }, [text, q, programId, partner, pickPartner]);

  const get = useCallback(async (cursor?: string): Promise<Page<PartnerConversion>> => {
    const { data, error } = await api.GET("/v1/brands/{brand}/partner-conversions", {
      params: { path: { brand: pickedBrandId() }, query: {
        ...(partnerId ? { partner_id: partnerId } : { program: programId, ...(partner ? { partner_id: partner.id } : {}) }), limit: pageSize,
        ...(type ? { type } : {}), ...(q ? { q } : {}), ...(cursor ? { cursor } : {}),
      } },
    });
    return { list: data?.data ?? [], next: data?.next_cursor ?? null, error: error ? errorMessage(error) : "" };
  }, [programId, partnerId, partner, type, q]);
  const { list, next, error, reload, more } = usePages(get);
  useEffect(() => {
    reload();
  }, [reload]);

  if (!list) return <Loading />;
  if (list.length === 0 && !partnerId && !q && !type && !partner && !text) {
    return (
      <Empty title="No conversions yet">
        Have your backend report every sign-up, sale and refund with POST /v1/workspaces/{"{ws}"}/partner-conversions, using a token with the partners.conversions scope, or use the postback URL in Settings. Outflare credits the referred ones.
      </Empty>
    );
  }
  const amount = (v: PartnerConversion) => (v.type === "signup" ? "" : money(v.type === "refund" ? -v.amount_cents : v.amount_cents, v.currency));
  const columns: ColumnDef<PartnerConversion>[] = [
    ...(partnerId ? [] : [{
      id: "partner", header: "Partner", accessorFn: (v: PartnerConversion) => v.partner_code,
      cell: ({ row: { original: v } }: { row: { original: PartnerConversion } }) => (
        <span className="block min-w-0 max-w-56">
          <AppLink ws={ws} tab="partners" item={v.partner_id} onOpen={openPartner && (() => openPartner(v.partner_id))} className="block truncate font-medium underline-offset-2 hover:underline">{v.partner_name || v.partner_code}</AppLink>
          <button type="button" className="block max-w-full truncate font-mono text-[12px] text-muted-foreground hover:text-foreground hover:underline" title="Show only this partner's conversions" onClick={() => pickPartner({ id: v.partner_id, code: v.partner_code }, "link")}>{v.partner_code}</button>
        </span>
      ),
    } as ColumnDef<PartnerConversion>]),
    { id: "customer", header: "Customer", accessorFn: (v) => v.customer_email, cell: ({ row: { original: v } }) => <CustomerCell ws={ws} go={go} id={v.record_id} name={v.customer_name} email={v.customer_email} /> },
    { id: "type", header: "Type", accessorFn: (v) => v.event_name || v.type, cell: ({ row: { original: v } }) => <Badge tone={v.type === "refund" ? "red" : v.type === "sale" ? "green" : "grey"}>{v.event_name || v.type}{v.recurring ? " (renewal)" : ""}</Badge> },
    { id: "amount", header: "Amount", accessorFn: (v) => v.amount_cents, cell: ({ row: { original: v } }) => <span className="whitespace-nowrap">{amount(v)}</span> },
    { id: "external", header: "Your id", accessorFn: (v) => v.external_id, cell: ({ getValue }) => <span className="block max-w-48 truncate font-mono text-[12px]" title={getValue() as string}>{getValue() as string}</span> },
    { id: "product", header: "Product", accessorFn: (v) => v.product_id },
    { id: "source", header: "From", accessorFn: (v) => v.source },
    { id: "when", header: "When", accessorFn: (v) => v.occurred_at, cell: ({ row: { original: v } }) => <span className="whitespace-nowrap" title={stamp(v.occurred_at)}>{ago(v.occurred_at)}</span> },
  ];
  return (
    <div className="space-y-3">
      <ErrorText>{error}</ErrorText>
      <DataTable
        columns={columns}
        data={list}
        rowId={(v) => v.id}
        searchPlaceholder={partnerId ? "Search by customer email or your id" : "Search by partner code or email, customer email or your id"}
        search={{ value: text, onChange: setText }}
        toolbar={
          <span className="flex flex-wrap items-center gap-2">
            <select aria-label="Type" className={field} value={type} onChange={(e) => {
              setType(e.target.value as ConversionType | "");
              track("ui.partner_conversions_filtered", { filter: "type", value: e.target.value || "all" });
            }}>
              <option value="">Every type</option>
              {conversionTypes.map(([v, label]) => <option key={v} value={v}>{label}</option>)}
            </select>
            {partner && (
              <span className="inline-flex h-8 max-w-56 items-center gap-1 rounded-md border bg-muted/50 pr-1 pl-2.5 text-[13px]">
                <span className="truncate">Partner: <span className="font-mono">{partner.code}</span></span>
                <button type="button" aria-label="Show every partner" className="rounded p-0.5 text-muted-foreground hover:bg-accent hover:text-foreground" onClick={() => setPartner(null)}>
                  <X className="size-3.5" />
                </button>
              </span>
            )}
          </span>
        }
        empty={partnerId ? "No conversions credited to this partner yet." : "No conversions match."}
        mobile={(v) => (
          <span className="block min-w-0">
            <span className="block truncate font-medium">{v.event_name || v.type}{v.recurring ? " (renewal)" : ""}{v.type !== "signup" && ` · ${amount(v)}`}</span>
            <span className="block truncate text-[12px] text-muted-foreground">{[v.customer_name || v.customer_email, !partnerId && v.partner_code].filter(Boolean).join(" · ")}</span>
            <span className="block truncate text-[12px] text-muted-foreground">{v.external_id && <><span className="font-mono">{v.external_id}</span> · </>}{v.source} · {ago(v.occurred_at)}</span>
          </span>
        )}
      />
      <LoadMore next={next} onMore={more} />
    </div>
  );
}
