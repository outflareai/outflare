import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import type { Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { pickedBrandId } from "@/lib/brand";
import { track } from "@/lib/analytics";
import type { Tab } from "@/lib/paths";
import { cn } from "@/lib/utils";
import { Badge, Button, ErrorText, Input, Loading, toast } from "../ui";
import { stamp } from "../format";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { money, payoutMethods, rewardTone } from "./Partners";
import { AppLink, viaText } from "./PartnerPage";

export type RewardRow = Schemas["PartnerRewardRow"];
export type Proof = Schemas["PartnerRewardProof"];
type Gap = Proof["gaps"][number];
type Step = Gap["step"];
type Go = (t: Tab, item?: string) => void;

export const platformName = (s: string) => (s === "tolt" ? "Tolt" : s || "the old platform");

export const payoutTone = { paid: "green", due: "amber", canceled: "grey" } as const;

export function saveFile(text: string, type: string, name: string) {
  const url = URL.createObjectURL(new Blob([text], { type }));
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

type ProofQuery = { payout_id: string } | { partner_id: string; from?: string; to?: string };

export async function downloadProof(query: ProofQuery, name: string) {
  const { data, error, response } = await api.GET("/v1/brands/{brand}/partner-proofs/export", {
    params: { path: { brand: pickedBrandId() }, query: { ...query, format: "csv" } }, parseAs: "text",
  });
  if (error || typeof data !== "string") return toast(errorMessage(error), "error");
  saveFile(data, response.headers.get("Content-Type") ?? "text/csv", name);
}

export const payoutProofName = (slug: string, payout: string) => `${slug}-payout-${payout}-proof.csv`;

export function rewardWhat(w: Pick<RewardRow, "kind" | "action" | "side">) {
  return w.action ? `${w.action}${w.side === "referred" ? " (friend)" : ""}` : w.kind;
}

// netText says what became of a reward that later entries took back.
export function netText(w: RewardRow) {
  if (w.reversed_by.length === 0) return "";
  if (w.net_cents === w.amount_cents) return `A take-back isn't approved yet, net ${money(w.net_cents, w.currency)}`;
  return `${w.status[0].toUpperCase()}${w.status.slice(1)}, then taken back, net ${money(w.net_cents, w.currency)}`;
}

export function SourceBadges({ w, className = "inline-flex flex-wrap gap-1" }: { w: Pick<RewardRow, "source" | "imported_from" | "held_at_source">; className?: string }) {
  if (w.source !== "imported") return <Badge tone="grey" title="Made by Outflare">native</Badge>;
  const from = platformName(w.imported_from);
  return (
    <span className={className}>
      <Badge tone="blue">Imported from {from}</Badge>
      {w.held_at_source && <Badge tone="amber" title={`${from} still pays it; Outflare never puts it in a payout`}>paid by {from}</Badge>}
    </span>
  );
}

const ruleRate = (r: NonNullable<Proof["rule"]>, currency: string) =>
  r.kind === "percent" ? `${(r.percent_bp ?? 0) / 100}%` : r.kind === "fixed" ? money(r.amount_cents ?? 0, currency) : "An action";
const ruleWhen = (r: NonNullable<Proof["rule"]>) =>
  r.recurrence === "first" ? "First sale only" : r.recurrence === "months" ? `Recurring, for ${r.months ?? "some"} months` : "Recurring, every sale";

export function ruleSummary(r: NonNullable<Proof["rule"]>, currency: string) {
  const what = `${ruleRate(r, currency)}, ${ruleWhen(r).toLowerCase()}`;
  return r.name ? `${r.name}: ${what}` : what;
}

const actorText: Record<string, string> = { human: "a member", api_token: "an API token", service_account: "a service account", agent: "an agent", system: "Outflare" };
const byText: Record<string, (platform: string) => string> = {
  auto: () => "automatically, after the hold", manual: () => "by hand", refund: () => "by a refund", import: (p) => `on ${p}, before the import`, flow: () => "by a flow",
};

export function decisionText(d: NonNullable<RewardRow["decision"]>, platform: string) {
  const who = d.actor_name || (d.by === "import" ? platform : d.by === "auto" ? "Outflare" : actorText[d.actor_type] ?? d.actor_type);
  const what = `${d.status === "approved" ? "Approved" : "Rejected"}${who ? ` by ${who}` : ""}`;
  return { what, who, how: byText[d.by]?.(platform) ?? "" };
}

function Line({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex gap-3 text-[13px]">
      <dt className="w-24 shrink-0 text-muted-foreground">{label}</dt>
      <dd className="min-w-0 flex-1 break-words">{children}</dd>
    </div>
  );
}

const when = (iso: string | null | undefined) => (iso ? <time dateTime={iso}>{stamp(iso)}</time> : <span className="text-muted-foreground">Not yet</span>);
const mono = (s: string) => <span className="font-mono text-[12px] break-all">{s}</span>;
const country = (c: string) => (c ? c.toUpperCase() : "");

type Ctx = { ws: string; go: Go; platform: string; slug: string; openPayouts: (partner: string) => void };

type StepView = { step: Step; title: (p: Proof) => string; body: (p: Proof, x: Ctx) => ReactNode | null };

const stepViews: StepView[] = [
  {
    step: "credit",
    title: (p) => (p.credit?.click ? "Click" : p.credit?.visit ? "Website visit" : "Click or visit"),
    body: (p) => {
      const c = p.credit;
      if (!c) return null;
      return (
        <>
          <Line label="Credited by">{viaText[c.via] ?? c.via}</Line>
          <Line label="When">{when(c.at)}</Line>
          {c.ref && <Line label="Ref">{mono(c.ref)}</Line>}
          {c.promo_code && <Line label="Promo code">{mono(c.promo_code)}</Line>}
          {c.sub_id && <Line label="Sub id">{mono(c.sub_id)}</Line>}
          {c.click && (
            <>
              <Line label="Clicked">{when(c.click.at)}</Line>
              {c.click.referrer_host && <Line label="From">{c.click.referrer_host}</Line>}
              {c.click.country && <Line label="Country">{country(c.click.country)}</Line>}
              {c.click.external_id && <Line label="Source id">{mono(c.click.external_id)}</Line>}
            </>
          )}
          {c.visit && (
            <>
              <Line label="Visited">
                <span className="inline-flex flex-wrap items-center gap-2">
                  {when(c.visit.at)}
                  {!c.visit.recorded && <Badge tone="amber" title="Outflare didn't keep this visit when it credited the partner; it is the customer's latest visit with this ref before then">matched later</Badge>}
                </span>
              </Line>
              {c.visit.landing_url && <Line label="Landing page"><span className="break-all">{c.visit.landing_url}</span></Line>}
              {c.visit.ref && <Line label="Visit ref">{mono(c.visit.ref)}</Line>}
              {c.visit.referrer_host && <Line label="From">{c.visit.referrer_host}</Line>}
              {c.visit.country && <Line label="Country">{country(c.visit.country)}</Line>}
              {(c.visit.utm_source || c.visit.utm_medium || c.visit.utm_campaign) && (
                <Line label="UTM">{[c.visit.utm_source, c.visit.utm_medium, c.visit.utm_campaign].filter(Boolean).join(" / ")}</Line>
              )}
            </>
          )}
        </>
      );
    },
  },
  {
    step: "referral",
    title: () => "Referral",
    body: (p) => p.referral && (
      <>
        <Line label="Via">{viaText[p.referral.via] ?? p.referral.via}</Line>
        <Line label="When">{when(p.referral.created_at)}</Line>
        <Line label="Status"><Badge tone={p.referral.status === "customer" ? "green" : p.referral.status === "refunded" ? "red" : "grey"}>{p.referral.status}</Badge></Line>
        <Line label="First sale">{when(p.referral.first_sale_at)}</Line>
      </>
    ),
  },
  {
    step: "customer",
    title: () => "Customer",
    body: (p, x) => p.customer && (
      <>
        <Line label="Person">
          <AppLink ws={x.ws} tab="people" item={p.customer.record_id} onOpen={() => x.go("people", p.customer!.record_id)} className="font-medium break-all underline-offset-2 hover:underline">
            {p.customer.name || p.customer.email || "Unnamed customer"}
          </AppLink>
          {p.customer.name && p.customer.email && <span className="block break-all text-muted-foreground">{p.customer.email}</span>}
        </Line>
        {p.customer.customer_id && <Line label="Your user id">{mono(p.customer.customer_id)}</Line>}
      </>
    ),
  },
  {
    step: "conversion",
    title: (p) => (p.conversion?.type === "refund" ? "Refund" : p.conversion?.type === "signup" ? "Sign-up" : p.conversion?.type === "custom" ? "Conversion" : "Sale"),
    body: (p) => {
      const v = p.conversion;
      if (!v) return null;
      return (
        <>
          <Line label="What">{v.event_name || v.type}{v.recurring ? " (renewal)" : ""}</Line>
          {v.type !== "signup" && <Line label="Amount">{money(v.type === "refund" ? -v.amount_cents : v.amount_cents, v.currency)} <span className="text-muted-foreground">{v.currency}</span></Line>}
          {v.external_id && <Line label="Your id">{mono(v.external_id)}</Line>}
          {v.transaction_id && <Line label="Transaction">{mono(v.transaction_id)}</Line>}
          {v.payment_source && <Line label="Paid with">{v.payment_source}</Line>}
          {v.source && <Line label="Reported by">{v.source}</Line>}
          {v.product_id && <Line label="Product">{mono(v.product_id)}</Line>}
          {v.refund_of && <Line label="Refund of">{mono(v.refund_of)}</Line>}
          <Line label="When">{when(v.occurred_at)}</Line>
        </>
      );
    },
  },
  {
    step: "rule",
    title: () => "Rule",
    body: (p) => p.rule && (
      <>
        {p.rule.name && <Line label="Name">{p.rule.name}</Line>}
        <Line label="Rate">{ruleRate(p.rule, p.reward.currency)}</Line>
        <Line label="Pays on">{ruleWhen(p.rule)}</Line>
        <Line label="Version">
          {p.rule.kept ? <Badge tone="grey" title="Outflare kept the rule as it was when it priced this reward">as it was then</Badge>
            : <Badge tone="amber" title="The rule as it is now; it may have changed since it priced this reward">as it is now</Badge>}
        </Line>
      </>
    ),
  },
  {
    step: "decision",
    title: (p) => (p.decision?.status === "approved" ? "Approved" : p.decision?.status === "rejected" ? "Rejected" : "Approval"),
    body: (p, x) => {
      const d = p.decision;
      if (!d) return null;
      const t = decisionText(d, x.platform);
      return (
        <>
          <Line label="By">{t.who || "Unknown"}</Line>
          {t.how && <Line label="How">{t.how}</Line>}
          <Line label="When">{when(d.at)}</Line>
          {d.note && <Line label="Note"><span className="whitespace-pre-wrap">{d.note}</span></Line>}
        </>
      );
    },
  },
  {
    step: "payout",
    title: () => "Payout",
    body: (p, x) => {
      const o = p.payout;
      if (!o) return null;
      const method = payoutMethods.find(([m]) => m === o.method)?.[1] ?? o.method;
      return (
        <>
          <Line label="Status"><Badge tone={payoutTone[o.status]}>{o.status}</Badge></Line>
          <Line label="Amount">{money(o.amount_cents, o.currency)}</Line>
          <Line label="Made">{when(o.created_at)}</Line>
          <Line label="Paid">{when(o.paid_at)}</Line>
          {method && <Line label="Method">{method}</Line>}
          {o.reference && <Line label="Reference">{mono(o.reference)}</Line>}
          <div className="flex flex-wrap gap-2 pt-1">
            <AppLink ws={x.ws} tab="partners" item={p.partner.id} onOpen={() => x.openPayouts(p.partner.id)} className="inline-flex h-8 items-center rounded-md border px-3 text-[13px] shadow-xs hover:bg-accent">
              Open payouts
            </AppLink>
            <Button variant="ghost" onClick={() => downloadProof({ payout_id: o.id }, payoutProofName(x.slug, o.id))}>Download CSV</Button>
          </div>
        </>
      );
    },
  },
];

function StepItem({ title, body, gaps, last }: { title: string; body: ReactNode | null; gaps: Gap[]; last: boolean }) {
  const has = !!body;
  return (
    <li className="relative pb-5 pl-6 last:pb-0" data-step={title}>
      {!last && <span aria-hidden className="absolute top-4 bottom-0 left-[4.5px] w-px bg-border" />}
      <span aria-hidden className={cn("absolute top-1 left-0 size-2.5 rounded-full border-2", has ? "border-foreground bg-foreground" : "border-muted-foreground/50 bg-background")} />
      <h3 className={cn("font-mono text-[11px] tracking-wide uppercase", has ? "text-foreground" : "text-muted-foreground")}>{title}</h3>
      {has && <dl className="mt-1.5 space-y-1">{body}</dl>}
      {gaps.map((g) => <p key={g.reason} className="mt-1 text-[13px] text-muted-foreground">{g.message}</p>)}
      {!has && gaps.length === 0 && <p className="mt-1 text-[13px] text-muted-foreground">Nothing on record.</p>}
    </li>
  );
}

function RewardLinks({ w, follow }: { w: RewardRow; follow: (id: string, how: "reversed_by" | "reverses") => void }) {
  if (!w.reverses_id && w.reversed_by.length === 0) return null;
  return (
    <span className="flex flex-wrap gap-x-3 gap-y-1">
      {w.reverses_id && <ProofLink onClick={() => follow(w.reverses_id!, "reverses")}>Reverses a reward</ProofLink>}
      {w.reversed_by.map((id, n) => (
        <ProofLink key={id} onClick={() => follow(id, "reversed_by")}>Reversed by{w.reversed_by.length > 1 ? ` (${n + 1})` : ""}</ProofLink>
      ))}
    </span>
  );
}

export function ProofLink({ onClick, children }: { onClick: () => void; children: ReactNode }) {
  return <button type="button" className="text-[12px] text-blue-700 underline-offset-2 hover:underline dark:text-blue-400" onClick={onClick}>{children}</button>;
}

export function ProofDrawer({ ws, id, slug, go, onClose, openPayouts }: { ws: string; id: string; slug: string; go: Go; onClose: () => void; openPayouts: (partner: string) => void }) {
  const [trail, setTrail] = useState<string[]>([id]);
  const cur = trail[trail.length - 1];
  const [proof, setProof] = useState<Proof | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    let live = true;
    setProof(null);
    setError("");
    api.GET("/v1/brands/{brand}/partner-rewards/{id}/proof", { params: { path: { brand: pickedBrandId(), id: cur } } }).then(({ data, error }) => {
      if (!live) return;
      if (data) setProof(data);
      else setError(errorMessage(error));
    });
    return () => {
      live = false;
    };
  }, [cur]);
  const follow = (to: string, how: "reversed_by" | "reverses") => {
    track("ui.partner_proof_link_followed", { to: how });
    setTrail((t) => [...t, to]);
  };
  const back = () => {
    track("ui.partner_proof_link_followed", { to: "back" });
    setTrail((t) => t.slice(0, -1));
  };
  const w = proof?.reward;
  const platform = platformName(w?.imported_from ?? "");
  const x: Ctx = { ws, go, platform, slug, openPayouts };
  const known = new Set(stepViews.map((s) => s.step));
  const stray = (proof?.gaps ?? []).filter((g) => !known.has(g.step));
  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full gap-0 overflow-x-hidden overflow-y-auto sm:max-w-xl">
        <SheetHeader className="border-b pr-10">
          {trail.length > 1 && <button type="button" className="self-start text-[12px] text-muted-foreground hover:text-foreground" onClick={back}>← Back to the earlier reward</button>}
          <SheetTitle className="flex flex-wrap items-center gap-2">
            {w ? <>{w.kind === "action" ? rewardWhat(w) : `${money(w.amount_cents, w.currency)} ${rewardWhat(w)}`}<Badge tone={rewardTone[w.status]}>{w.status}</Badge></> : "Reward proof"}
          </SheetTitle>
          <SheetDescription className="min-w-0 text-[13px] break-words">
            {proof ? <>For {proof.partner.name || proof.partner.email || proof.partner.code} <span className="font-mono">({proof.partner.code})</span>, made {stamp(proof.reward.created_at)}</> : "Why the partner earned this, link by link."}
          </SheetDescription>
        </SheetHeader>
        <div className="space-y-5 p-4">
          <ErrorText>{error}</ErrorText>
          {!proof && !error && <Loading />}
          {proof && w && (
            <>
              <div className="space-y-2">
                <SourceBadges w={w} />
                {netText(w) && <p className="text-[13px]">{netText(w)}</p>}
                <RewardLinks w={w} follow={follow} />
                {w.note && <p className="text-[13px] break-words text-muted-foreground">Note: {w.note}</p>}
              </div>
              {proof.imported && (
                <div className="rounded-md border bg-muted/40 p-3">
                  <p className="font-mono text-[11px] tracking-wide text-muted-foreground uppercase">Imported from {platformName(proof.imported.connector)}</p>
                  <dl className="mt-1.5 space-y-1">
                    {([["Partner", proof.imported.partner_id], ["Customer", proof.imported.customer_id], ["Commission", proof.imported.commission_id], ["Transaction", proof.imported.transaction_id], ["Payout", proof.imported.payout_id]] as const)
                      .filter(([, v]) => v).map(([k, v]) => <Line key={k} label={`${k} id`}>{mono(v)}</Line>)}
                  </dl>
                </div>
              )}
              <ol aria-label="Proof trail">
                {stepViews.map((s, n) => (
                  <StepItem key={s.step} title={s.title(proof)} body={s.body(proof, x)} gaps={proof.gaps.filter((g) => g.step === s.step)} last={n === stepViews.length - 1 && stray.length === 0} />
                ))}
                {stray.length > 0 && <StepItem title="Other" body={null} gaps={stray} last />}
              </ol>
            </>
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}

const dayInput = (d: Date) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;

// ProofExport downloads one partner's proof for the days picked, both included.
export function ProofExport({ partner, slug, onClose }: { partner: string; slug: string; onClose: () => void }) {
  const now = new Date();
  const [from, setFrom] = useState(dayInput(new Date(now.getFullYear(), now.getMonth() - 1, 1)));
  const [to, setTo] = useState(dayInput(now));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function save(e: FormEvent) {
    e.preventDefault();
    if (from && to && from > to) return setError("The first day is after the last.");
    const end = to ? new Date(`${to}T00:00:00`) : null;
    end?.setDate(end.getDate() + 1);
    setBusy(true);
    await downloadProof(
      { partner_id: partner, ...(from ? { from: new Date(`${from}T00:00:00`).toISOString() } : {}), ...(end ? { to: end.toISOString() } : {}) },
      `${slug}-partner-${partner}-proof${from ? `-from-${from}` : ""}${to ? `-to-${to}` : ""}.csv`,
    );
    setBusy(false);
    onClose();
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Download proof</DialogTitle>
          <DialogDescription>A spreadsheet with one row per reward made in these days: the click or visit, the customer, the sale, the rule, who approved it and the payout. It holds customers' names and emails, never payout details.</DialogDescription>
        </DialogHeader>
        <form onSubmit={save} className="space-y-3">
          <div className="grid grid-cols-2 gap-3">
            <Input label="From" type="date" value={from} max={to || undefined} onChange={(e) => setFrom(e.target.value)} />
            <Input label="To" type="date" value={to} min={from || undefined} onChange={(e) => setTo(e.target.value)} />
          </div>
          <ErrorText>{error}</ErrorText>
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
            <Button disabled={busy}>{busy ? "Downloading…" : "Download CSV"}</Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
