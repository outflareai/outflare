import { useEffect, useState, type FormEvent } from "react";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { api, errorMessage } from "../../api";
import { Badge, Button, ErrorText, field, confirmAction, toast } from "../../ui";
import { ago } from "../../format";
import { anchorSourceLabel, kindLabel, secondaryTypeLabel } from "./targets";
import {
  approverGate,
  bothAmounts,
  invoiceCurrencies,
  frozenOf,
  invoiceOf,
  quotedOf,
  minorDigits,
  parseMajor,
  rateLine,
  todayISO,
  type InvoiceCurrency,
  dash,
  day,
  donorBy,
  idemp,
  memberName,
  money,
  payGate,
  shortId,
  slotLabel,
  statusLabel,
  statusTone,
  type LinkOrder,
  type LinkOrderEquivalent,
  type PageData,
} from "./shared";

export function OrderSheet({ order, data, onClose }: { order?: LinkOrder; data: PageData; onClose: () => void }) {
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState("");
  const [quoteAmount, setQuoteAmount] = useState("");
  const [quoteCurrency, setQuoteCurrency] = useState<InvoiceCurrency>("USD");
  const [quoteDate, setQuoteDate] = useState(todayISO());
  const [payDate, setPayDate] = useState("");
  const [publishedUrl, setPublishedUrl] = useState("");
  const [proofUrl, setProofUrl] = useState("");
  const [notes, setNotes] = useState("");
  const [equivalents, setEquivalents] = useState<LinkOrderEquivalent[] | null>(null);
  const [replaceDonor, setReplaceDonor] = useState("");

  useEffect(() => {
    setErr("");
    setBusy("");
    setEquivalents(null);
    if (!order) return;
    const q = quotedOf(order);
    setQuoteAmount(q ? String(q.amount_minor / 10 ** minorDigits(q.currency)) : "");
    setQuoteCurrency((q?.currency ?? "USD") as InvoiceCurrency);
    setQuoteDate(q?.invoice_date ?? todayISO());
    const f = invoiceOf(order);
    setPayDate(f?.invoice_date ?? "");
    setPublishedUrl(order.published_url || "");
    setProofUrl(order.proof_url || "");
    setNotes("");
    setReplaceDonor("");
  }, [order?.id]);

  useEffect(() => {
    if (!order || order.status !== "lost") return;
    let gone = false;
    api.GET("/v1/brands/{brand}/link-orders/{id}/equivalents", { params: { path: { brand: data.brand, id: order.id } } }).then(({ data: rows, error }) => {
      if (gone) return;
      if (error) {
        setErr(errorMessage(error));
        return;
      }
      const list = rows?.data ?? [];
      setEquivalents(list);
      const first = list.find((r) => !r.same_donor) ?? list[0];
      if (first) setReplaceDonor(first.donor.id);
    });
    return () => {
      gone = true;
    };
  }, [order?.id, order?.status, data.brand]);

  if (!order) return null;
  const d = donorBy(data.donors, order.donor_profile_id);
  const frozen = frozenOf(order);
  const quoted = quotedOf(order);
  const approveBlocked = approverGate(data.isApprover, data.settings);
  const payBlocked = payGate(order, data.canPay, data.isApprover, data.settings);
  const canQuote = data.canManage && (order.status === "requested" || order.status === "quoted" || order.status === "negotiating");
  const canNegotiate = data.canManage && order.status === "quoted";
  const canApprove = order.status === "quoted" || order.status === "negotiating";
  const canPublish = data.canManage && order.status === "approved";
  const canCheck = data.canManage && (order.status === "published" || order.status === "paid" || order.status === "verified");
  const canVerify = data.canManage && (order.status === "published" || order.status === "paid") && order.last_check_live === true && order.last_check_target_match === true;
  const canPay = order.status === "published" || order.status === "verified";
  const canLost = data.canManage && (order.status === "published" || order.status === "paid" || order.status === "verified");
  const replacements = data.orders.filter((o) => o.replaces_order_id === order.id && o.status !== "lost");
  const canReplace = data.canManage && order.status === "lost" && replacements.length === 0;
  const canCredit = order.status === "lost" && !order.credit_payment_id && replacements.length === 0;

  const run = async (label: string, fn: () => Promise<unknown>) => {
    setBusy(label);
    setErr("");
    try {
      await fn();
      toast("Order updated.");
      data.reload();
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy("");
    }
  };

  const post = (path: "/quote" | "/negotiate" | "/approve" | "/publish" | "/check" | "/pay" | "/verify" | "/lost" | "/replace" | "/credit", body?: object) => {
    const header = idemp();
    const params = { path: { brand: data.brand, id: order.id }, header };
    const routes = {
      "/quote": () => api.POST("/v1/brands/{brand}/link-orders/{id}/quote", { params, body: body as { amount_minor: number; currency: InvoiceCurrency; invoice_date?: string } }),
      "/negotiate": () => api.POST("/v1/brands/{brand}/link-orders/{id}/negotiate", { params, body: body as { notes?: string | null } }),
      "/approve": () => api.POST("/v1/brands/{brand}/link-orders/{id}/approve", { params }),
      "/publish": () => api.POST("/v1/brands/{brand}/link-orders/{id}/publish", { params, body: body as { published_url: string; proof_url?: string } }),
      "/check": () => api.POST("/v1/brands/{brand}/link-orders/{id}/check", { params }),
      "/pay": () => api.POST("/v1/brands/{brand}/link-orders/{id}/pay", { params, body: body as { invoice_date?: string } | undefined }),
      "/verify": () => api.POST("/v1/brands/{brand}/link-orders/{id}/verify", { params }),
      "/lost": () => api.POST("/v1/brands/{brand}/link-orders/{id}/lost", { params, body: body as { notes?: string | null } }),
      "/replace": () => api.POST("/v1/brands/{brand}/link-orders/{id}/replace", { params, body: body as { donor_profile_id: string; notes?: string | null } }),
      "/credit": () => api.POST("/v1/brands/{brand}/link-orders/{id}/credit", { params, body: body as { reason?: string | null } }),
    };
    return routes[path]().then(({ data: row, error }) => {
      if (error) throw error;
      return row;
    });
  };

  const onQuote = (e: FormEvent) => {
    e.preventDefault();
    const minor = parseMajor(quoteAmount, quoteCurrency);
    if (minor == null) {
      setErr("Enter a donor price of zero or more.");
      return;
    }
    const body: { amount_minor: number; currency: InvoiceCurrency; invoice_date?: string } = { amount_minor: minor, currency: quoteCurrency };
    if (quoteDate) body.invoice_date = quoteDate;
    void run("quote", () => post("/quote", body));
  };

  return (
    <Sheet open={!!order} onOpenChange={(o) => { if (!o) onClose(); }}>
      <SheetContent className="flex w-full flex-col gap-4 overflow-y-auto sm:max-w-md">
        <SheetHeader>
          <SheetTitle className="flex flex-wrap items-center gap-2">
            <span className="font-mono text-[14px]">{shortId(order.id)}</span>
            <Badge tone={statusTone[order.status]}>{statusLabel[order.status]}</Badge>
            {d?.pbn ? <Badge tone="amber">PBN</Badge> : null}
          </SheetTitle>
          <SheetDescription>
            Donor price only. Approve and Paid are reserved for the brand's designated approver. Paid also needs a passing live-link check.
          </SheetDescription>
        </SheetHeader>

        <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-[13px]">
          <dt className="text-muted-foreground">Donor</dt>
          <dd>{d?.domain ?? order.domain}</dd>
          <dt className="text-muted-foreground">Target</dt>
          <dd className="break-all font-mono text-[12px]">{order.target_url}</dd>
          {order.target_kind ? (
            <>
              <dt className="text-muted-foreground">Page kind</dt>
              <dd>{kindLabel[order.target_kind]} · {order.target_choice === "suggested" ? "suggested" : "picked by hand"}</dd>
            </>
          ) : null}
          {order.anchor_primary ? (
            <>
              <dt className="text-muted-foreground">Anchor</dt>
              <dd>{order.anchor_primary} <span className="text-muted-foreground">({anchorSourceLabel[order.anchor_primary_source] || "typed"})</span></dd>
            </>
          ) : null}
          {order.anchor_secondary ? (
            <>
              <dt className="text-muted-foreground">Second anchor</dt>
              <dd>{order.anchor_secondary} <span className="text-muted-foreground">({secondaryTypeLabel[order.anchor_secondary_type] || order.anchor_secondary_type})</span></dd>
            </>
          ) : null}
          <dt className="text-muted-foreground">Type</dt>
          <dd>{slotLabel(order.slot_type)}</dd>
          <dt className="text-muted-foreground">Rel</dt>
          <dd>{order.rel_requested || dash}</dd>
          <dt className="text-muted-foreground">Quoted</dt>
          <dd>
            {bothAmounts(quoted)}
            {quoted?.invoice_date ? <span className="text-muted-foreground"> · invoice {day(quoted.invoice_date)}</span> : null}
            {rateLine(quoted) ? <div className="text-[12px] text-muted-foreground">{rateLine(quoted)}</div> : null}
          </dd>
          <dt className="text-muted-foreground">Donor price</dt>
          <dd className="font-medium">
            {frozen ? bothAmounts(frozen) : dash}
            {frozen?.invoice_date ? <span className="font-normal text-muted-foreground"> · invoice {day(frozen.invoice_date)}</span> : null}
            {rateLine(frozen) ? <div className="text-[12px] font-normal text-muted-foreground">{rateLine(frozen)}</div> : null}
          </dd>
          <dt className="text-muted-foreground">Guarantee</dt>
          <dd>{order.guarantee_days != null ? `${order.guarantee_days} days` : dash}{order.guarantee_until ? ` · until ${day(order.guarantee_until)}` : ""}</dd>
          <dt className="text-muted-foreground">Published URL</dt>
          <dd className="break-all font-mono text-[12px]">{order.published_url || dash}</dd>
          <dt className="text-muted-foreground">Last check</dt>
          <dd>
            {order.last_check_at ? ago(order.last_check_at) : dash}
            {order.last_check_at ? ` · live ${order.last_check_live == null ? dash : order.last_check_live ? "yes" : "no"} · target ${order.last_check_target_match == null ? dash : order.last_check_target_match ? "yes" : "no"}` : ""}
            {order.last_check_rel ? ` · ${order.last_check_rel}` : ""}
            {order.last_check_anchor ? ` · “${order.last_check_anchor}”` : ""}
          </dd>
          <dt className="text-muted-foreground">Owner</dt>
          <dd>{memberName(data.members, order.owner_user_id)}</dd>
          <dt className="text-muted-foreground">Approved</dt>
          <dd>{order.approved_at ? `${day(order.approved_at)} by ${memberName(data.members, order.approved_by)}` : dash}</dd>
          <dt className="text-muted-foreground">Paid</dt>
          <dd>{order.paid_at ? `${day(order.paid_at)} by ${memberName(data.members, order.paid_by)}` : dash}</dd>
          {order.replaces_order_id ? (
            <>
              <dt className="text-muted-foreground">Replaces</dt>
              <dd>
                <button type="button" className="font-mono text-[12px] underline" onClick={() => data.goSection("orders", order.replaces_order_id!)}>
                  {shortId(order.replaces_order_id)}
                </button>
              </dd>
            </>
          ) : null}
        </dl>
        {order.notes ? <p className="text-[13px] text-muted-foreground">{order.notes}</p> : null}
        {err ? <ErrorText>{err}</ErrorText> : null}

        {canQuote ? (
          <form className="flex flex-col gap-2 rounded-md border p-3" onSubmit={onQuote}>
            <div className="text-[13px] font-medium">Quote a donor price</div>
            <p className="text-[12px] text-muted-foreground">Enter the amount as it is on the invoice. Outflare keeps it in that currency and shows the US-dollar equivalent beside it.</p>
            <div className="flex gap-2">
              <input className={field} type="number" min={0} step={minorDigits(quoteCurrency) ? "0.01" : "1"} value={quoteAmount} onChange={(e) => setQuoteAmount(e.target.value)} aria-label="Quoted donor amount" required />
              <select className={field} value={quoteCurrency} onChange={(e) => setQuoteCurrency(e.target.value as InvoiceCurrency)} aria-label="Quoted currency">
                {invoiceCurrencies.map((c) => (
                  <option key={c}>{c}</option>
                ))}
              </select>
            </div>
            <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
              Invoice date
              <input className={field} type="date" value={quoteDate} max={todayISO()} onChange={(e) => setQuoteDate(e.target.value)} aria-label="Invoice date" />
            </label>
            {quoteCurrency !== "USD" ? (
              <p className="text-[12px] text-muted-foreground" data-testid="quote-rate-note">
                Outflare converts to US dollars at the ECB reference rate for the invoice date (or the last working day before it). If there is no ECB rate for that date, the quote is not saved.
              </p>
            ) : null}
            <Button type="submit" disabled={busy === "quote"}>Save quote</Button>
          </form>
        ) : null}

        <div className="flex flex-col gap-2">
          {canNegotiate ? (
            <Button variant="ghost" disabled={!!busy} onClick={() => void run("negotiate", () => post("/negotiate", { notes: notes || null }))}>
              Mark as negotiating
            </Button>
          ) : null}
          {canApprove ? (
            <Button
              disabled={!!busy || !!approveBlocked}
              title={approveBlocked}
              onClick={async () => {
                if (approveBlocked) return;
                if (!(await confirmAction("Approve this order?", `This freezes the quoted donor price of ${bothAmounts(quoted)} and reserves it against the ${order.quoted_currency} budget, when one is set. Only the brand approver can do this.`, "Approve", false))) return;
                void run("approve", () => post("/approve"));
              }}
            >
              Approve
            </Button>
          ) : null}
          {canPublish ? (
            <form
              className="flex flex-col gap-2 rounded-md border p-3"
              onSubmit={(e) => {
                e.preventDefault();
                if (!publishedUrl.trim()) {
                  setErr("A published URL is required.");
                  return;
                }
                void run("publish", () => post("/publish", { published_url: publishedUrl.trim(), proof_url: proofUrl.trim() }));
              }}
            >
              <div className="text-[13px] font-medium">Record publication</div>
              <input className={field} value={publishedUrl} onChange={(e) => setPublishedUrl(e.target.value)} placeholder="https://..." aria-label="Published URL" required />
              <input className={field} value={proofUrl} onChange={(e) => setProofUrl(e.target.value)} placeholder="Proof URL (optional)" aria-label="Proof URL" />
              <Button type="submit" disabled={busy === "publish"}>Mark published</Button>
            </form>
          ) : null}
          {canCheck ? (
            <Button variant="ghost" disabled={!!busy} onClick={() => void run("check", () => post("/check").then(() => toast("Live-link check queued.")))}>
              Recheck link
            </Button>
          ) : null}
          {canPay && frozen ? (
            <div className="flex flex-col gap-2 rounded-md border p-3">
              <div className="text-[13px] font-medium">Final invoice date (optional)</div>
              <p className="text-[12px] text-muted-foreground">
                Change this only if the donor's final invoice has another date. The amount stays {money(frozen.amount_minor, frozen.currency)}{frozen.currency !== "USD" ? "; the US-dollar equivalent is worked out again at the ECB reference rate for that date" : ""}.
              </p>
              <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
                Invoice date
                <input className={field} type="date" value={payDate} max={todayISO()} onChange={(e) => setPayDate(e.target.value)} aria-label="Final invoice date" />
              </label>
            </div>
          ) : null}
          {canPay ? (
            <Button
              disabled={!!busy || !!payBlocked}
              title={payBlocked}
              onClick={async () => {
                if (payBlocked) return;
                const changed = !!payDate && payDate !== (frozen?.invoice_date ?? "");
                if (!(await confirmAction("Mark this order Paid?", `This records a Coverage payment of ${bothAmounts(frozen)} — the donor price only, as on the invoice and in US dollars. It does not send money. Paid is allowed only after publication and a passing live-link check.`, "Mark Paid", false))) return;
                void run("pay", () => post("/pay", changed ? { invoice_date: payDate } : undefined));
              }}
            >
              Mark Paid
            </Button>
          ) : null}
          {canVerify ? (
            <Button variant="ghost" disabled={!!busy} onClick={() => void run("verify", () => post("/verify"))}>
              Mark verified
            </Button>
          ) : null}
          {canLost ? (
            <Button
              variant="danger"
              disabled={!!busy}
              onClick={async () => {
                if (!(await confirmAction("Mark this link lost?", "Two agreeing down checks usually do this automatically. Manual lost is for when you already know the link is gone.", "Mark lost"))) return;
                void run("lost", () => post("/lost", { notes: notes || null }));
              }}
            >
              Mark lost
            </Button>
          ) : null}
          {canReplace ? (
            <form
              className="flex flex-col gap-2 rounded-md border p-3"
              onSubmit={(e) => {
                e.preventDefault();
                if (!replaceDonor) {
                  setErr("Pick a replacement donor.");
                  return;
                }
                void run("replace", () => post("/replace", { donor_profile_id: replaceDonor, notes: notes || null }).then((row) => {
                  const id = (row as LinkOrder | undefined)?.id;
                  if (id) data.goSection("orders", id);
                }));
              }}
            >
              <div className="text-[13px] font-medium">Open a replacement order</div>
              <p className="text-[12px] text-muted-foreground">
                Prefer an equivalent donor in the same DR band and niche. A replacement in the same currency and within the original amount does not need a new budget approval.
              </p>
              <select className={field} value={replaceDonor} onChange={(e) => setReplaceDonor(e.target.value)} aria-label="Replacement donor">
                <option value="">Select a donor</option>
                {(equivalents ?? []).map((eq) => (
                  <option key={eq.donor.id} value={eq.donor.id}>
                    {eq.donor.domain}{eq.same_donor ? " (current)" : ""}{eq.donor.pbn ? " · PBN" : ""} · DR {eq.donor.domain_rating ?? dash} · band {eq.dr_band_start}-{eq.dr_band_end}
                  </option>
                ))}
                {!equivalents && data.donors.filter((x) => !x.archived_at).map((x) => (
                  <option key={x.id} value={x.id}>{x.domain}{x.pbn ? " · PBN" : ""}</option>
                ))}
              </select>
              <Button type="submit" disabled={busy === "replace"}>Open replacement</Button>
            </form>
          ) : null}
          {canCredit ? (
            <Button
              variant="ghost"
              disabled={!!busy || !!approverGate(data.isApprover, data.settings) || !data.canPay}
              title={approverGate(data.isApprover, data.settings) || (!data.canPay ? "Recording a credit also needs Coverage finance manage." : undefined)}
              onClick={async () => {
                if (approverGate(data.isApprover, data.settings) || !data.canPay) return;
                if (!(await confirmAction("Record a credit?", "Use a credit only if the donor cannot replace the lost link. This records a Coverage reversal; it does not send money.", "Record credit", false))) return;
                void run("credit", () => post("/credit", { reason: notes || null }));
              }}
            >
              Record credit
            </Button>
          ) : null}
          {(canNegotiate || canLost || canReplace || canCredit) ? (
            <textarea className={field} rows={2} value={notes} onChange={(e) => setNotes(e.target.value)} placeholder="Notes (optional)" aria-label="Order notes" />
          ) : null}
        </div>
      </SheetContent>
    </Sheet>
  );
}
