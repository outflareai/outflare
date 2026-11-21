import { useState } from "react";
import { track } from "../../lib/analytics";
import { Button, Card, ErrorText, field, FilePicker, toast } from "../../ui";
import {
  checkBoughtLink,
  emptyBoughtLink,
  invoiceCurrencies,
  parsePastOrders,
  pastOrdersExampleCSV,
  recordBoughtLink,
  type BoughtLink,
  type InvoiceCurrency,
  type RecordResult,
} from "./recordBoughtModel";
import type { PageData } from "./shared";

// "Record a bought link": one form for a link bought outside Outflare, or a
// CSV of past orders. It runs the normal order steps with the person's own
// permissions (see recordBoughtModel.ts), so Approve stays with the approver and
// Paid stays a separate step after the live-link check.

const reachedLabel: Record<RecordResult["reached"], string> = {
  none: "not recorded",
  requested: "Requested",
  quoted: "Quoted",
  approved: "Approved",
  published: "Published, live check queued",
};

export function RecordBought({ data, onClose }: { data: PageData; onClose: () => void }) {
  const canApprove = data.isApprover && !!data.settings?.approver_user_id;
  const [mode, setMode] = useState<"one" | "csv">("one");
  const [b, setB] = useState<BoughtLink>(emptyBoughtLink);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [results, setResults] = useState<{ row: number; label: string; r: RecordResult }[]>([]);
  const [progress, setProgress] = useState<{ done: number; total: number } | null>(null);

  const set = (patch: Partial<BoughtLink>) => setB({ ...b, ...patch });
  const input = (key: keyof BoughtLink, label: string, placeholder = "", type = "text") => (
    <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
      {label}
      <input className={field} type={type} value={b[key]} placeholder={placeholder} onChange={(e) => set({ [key]: e.target.value } as Partial<BoughtLink>)} />
    </label>
  );

  const recordOne = async () => {
    const problems = checkBoughtLink(b);
    if (problems.length) {
      setErr(problems.join(" "));
      return;
    }
    setBusy(true);
    setErr("");
    try {
      const r = await recordBoughtLink(data.brand, data.donors, b, canApprove);
      track("ui.link_order_bought_recorded", { count: 1, published: r.reached === "published" ? 1 : 0 });
      if (r.reached === "published") {
        toast("Recorded. A live-link check is queued; mark Paid once it passes.");
        setB(emptyBoughtLink);
      } else if (r.reached === "quoted" && !r.stopped?.startsWith("Approving")) {
        toast("Recorded as Quoted: the brand's approver approves it, then the publication is recorded on the order.");
        setB(emptyBoughtLink);
      } else {
        setErr(`Stopped at ${reachedLabel[r.reached]}. ${r.stopped ?? ""}`);
      }
      data.reload();
      if (r.orderId && r.reached !== "none") data.goSection("orders", r.orderId);
    } catch (e) {
      setErr(errorText(e));
    } finally {
      setBusy(false);
    }
  };

  const recordCSV = async () => {
    if (!file) return;
    setErr("");
    setResults([]);
    const { rows, missing } = parsePastOrders(await file.text());
    if (missing.length) {
      setErr(`The file needs these columns: ${missing.join(", ")}. Download the example for the full list.`);
      return;
    }
    if (!rows.length) {
      setErr("The file has no rows.");
      return;
    }
    setBusy(true);
    const out: { row: number; label: string; r: RecordResult }[] = [];
    // Each row adds its donor to the catalog when missing; later rows for
    // the same site reuse it.
    const donors = [...data.donors];
    try {
      for (const [i, row] of rows.entries()) {
        setProgress({ done: i, total: rows.length });
        const problems = checkBoughtLink(row);
        const label = row.domain || row.publishedURL || `row ${i + 2}`;
        if (problems.length) {
          out.push({ row: i + 2, label, r: { reached: "none", stopped: problems.join(" ") } });
          continue;
        }
        const r = await recordBoughtLink(data.brand, donors, row, canApprove);
        out.push({ row: i + 2, label, r });
        setResults([...out]);
      }
    } finally {
      setProgress(null);
      setBusy(false);
      setResults(out);
      track("ui.link_order_bought_recorded", { count: out.length, published: out.filter((x) => x.r.reached === "published").length });
      data.reload();
    }
  };

  const okN = results.filter((x) => x.r.reached === "published" || (x.r.reached === "quoted" && !canApprove)).length;

  return (
    <Card
      title="Record a bought link"
      description={`For a link already bought outside Outflare. It adds the site to the catalog if needed, opens the order with the price, ${canApprove ? "approves it" : "and leaves it at Quoted for the brand's approver"}, and records the publication, which queues a live-link check. Mark Paid on the order once the check passes; nothing here sends money.`}
    >
      <div className="mb-3 flex gap-2" role="group" aria-label="Record">
        <Button variant={mode === "one" ? "primary" : "ghost"} aria-pressed={mode === "one"} onClick={() => setMode("one")}>One link</Button>
        <Button variant={mode === "csv" ? "primary" : "ghost"} aria-pressed={mode === "csv"} onClick={() => setMode("csv")}>Past orders from CSV</Button>
      </div>
      {mode === "one" ? (
        <form className="grid gap-2 sm:grid-cols-2" onSubmit={(e) => { e.preventDefault(); void recordOne(); }}>
          {input("publishedURL", "Published article", "https://donor.example/blog/post")}
          {input("domain", "Donor site (from the article if empty)", "donor.example")}
          {input("targetURL", "Page it links to", "https://your-site.example/page")}
          {input("anchor", "Anchor", "the link text you ordered")}
          {input("anchor2", "Second anchor (optional)")}
          {input("intermediary", "Intermediary (optional)", "Broker you bought through")}
          {input("price", "Price paid", "150")}
          <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
            Currency
            <select className={field} value={b.currency} onChange={(e) => set({ currency: e.target.value as InvoiceCurrency })}>
              {invoiceCurrencies.map((c) => <option key={c}>{c}</option>)}
            </select>
          </label>
          {input("invoiceDate", "Invoice date (for the exchange rate)", "", "date")}
          {input("proofURL", "Proof (optional)", "Screenshot or invoice link")}
          <label className="flex flex-col gap-1 text-[12px] text-muted-foreground sm:col-span-2">
            Notes
            <input className={field} value={b.notes} onChange={(e) => set({ notes: e.target.value })} />
          </label>
          <div className="flex gap-2 sm:col-span-2">
            <Button type="submit" disabled={busy}>{busy ? "Recording…" : "Record the link"}</Button>
            <Button type="button" variant="ghost" disabled={busy} onClick={onClose}>Cancel</Button>
          </div>
        </form>
      ) : (
        <div className="flex flex-col gap-3">
          <p className="text-[12px] text-muted-foreground">One bought link per row. Needed: target_url, published_url and price; optional: domain, intermediary, anchor, anchor_secondary, currency (default USD), invoice_date, proof_url, notes. Rows are recorded one at a time; a bad row is listed and the rest go on.</p>
          <FilePicker file={file} onChange={(f) => { setFile(f); setResults([]); }} accept=".csv,text/csv" example={{ name: "past-link-orders-example.csv", content: pastOrdersExampleCSV }} />
          {progress ? <span className="text-[12px] text-muted-foreground" role="status">Recording row {progress.done + 1} of {progress.total}…</span> : null}
          <div className="flex gap-2">
            <Button disabled={!file || busy} onClick={() => void recordCSV()}>{busy ? "Recording…" : "Record past orders"}</Button>
            <Button variant="ghost" disabled={busy} onClick={onClose}>Close</Button>
          </div>
          {results.length ? (
            <div className="text-[12px]">
              <p className="font-medium">{okN} of {results.length} recorded{canApprove ? " and published" : " (waiting for approval)"}.</p>
              <ul className="mt-1 flex flex-col gap-0.5 text-muted-foreground">
                {results.filter((x) => x.r.stopped && !(x.r.reached === "quoted" && !canApprove)).map((x) => (
                  <li key={x.row}>Row {x.row} ({x.label}): {reachedLabel[x.r.reached]}. {x.r.stopped}</li>
                ))}
              </ul>
            </div>
          ) : null}
        </div>
      )}
      {err ? <ErrorText>{err}</ErrorText> : null}
    </Card>
  );
}

function errorText(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}
