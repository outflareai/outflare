import { Button, Card, Empty } from "../../ui";
import {
  dash,
  invoiceOf,
  priceOf,
  rateLine,
  day,
  donorBy,
  money,
  shortId,
  spentStatuses,
  statusLabel,
  statusTone,
  type PageData,
} from "./shared";
import { Badge } from "../../ui";

function sum(data: PageData, currencies: ("USD" | "EUR")[], pick: "spent" | "committed" | "lost") {
  const fromSummary = data.summary?.[pick] ?? [];
  return currencies.map((c) => fromSummary.find((r) => r.currency === c)?.amount_minor ?? 0);
}

export function Payments({ data }: { data: PageData }) {
  const currencies = (() => {
    const set = new Set<"USD" | "EUR">();
    for (const b of data.budget?.budgets ?? []) set.add(b.currency);
    for (const row of [...(data.summary?.spent ?? []), ...(data.summary?.committed ?? []), ...(data.summary?.lost ?? [])]) set.add(row.currency);
    if (!set.size) {
      set.add("USD");
      set.add("EUR");
    }
    return [...set];
  })();
  const spent = sum(data, currencies, "spent");
  const committed = sum(data, currencies, "committed");
  const paid = data.orders.filter((o) => o.paid_at || spentStatuses.includes(o.status));
  const lostOpen = data.orders.filter((o) => o.status === "lost" && !o.credit_payment_id && !data.orders.some((x) => x.replaces_order_id === o.id && x.status !== "lost"));

  return (
    <div className="flex flex-col gap-3">
      <div className="grid gap-3 lg:grid-cols-2">
        <Card title="Money in this period" description="Donor price only — no commission and no client total. USD and EUR are never added together; an invoice in any other currency counts here at its US-dollar equivalent on the invoice date. Recording a payment does not send money.">
          {data.canFinanceRead ? (
            <table className="w-full text-left text-[13px]">
              <thead className="text-muted-foreground">
                <tr className="border-b">
                  <th className="py-2 pr-3 font-medium"></th>
                  {currencies.map((c) => (
                    <th key={c} className="py-2 pr-3 text-right font-medium">
                      {c}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                <tr className="border-b">
                  <td className="py-2 pr-3">Paid to donors</td>
                  {spent.map((n, i) => (
                    <td key={currencies[i]} className="py-2 pr-3 text-right tabular-nums">
                      {money(n, currencies[i])}
                    </td>
                  ))}
                </tr>
                <tr className="border-b">
                  <td className="py-2 pr-3">Committed, unpaid</td>
                  {committed.map((n, i) => (
                    <td key={currencies[i]} className="py-2 pr-3 text-right tabular-nums">
                      {money(n, currencies[i])}
                    </td>
                  ))}
                </tr>
                <tr>
                  <td className="py-2 pr-3">Lost (still in term, no credit)</td>
                  {currencies.map((c) => {
                    const n = lostOpen.filter((o) => (o.currency ?? o.quoted_currency) === c).reduce((s, o) => s + (o.donor_amount_minor ?? o.quoted_amount_minor ?? 0), 0);
                    return (
                      <td key={c} className="py-2 pr-3 text-right tabular-nums">
                        {money(n, c)}
                      </td>
                    );
                  })}
                </tr>
              </tbody>
            </table>
          ) : (
            <p className="text-[13px] text-muted-foreground">Coverage finance read is needed to see amounts.</p>
          )}
        </Card>
        <Card title="Guarantee on a lost link" description="Open a replacement first. A credit is only offered if the donor cannot replace the link, and only the brand approver can record it.">
          {lostOpen.length ? (
            <ul className="flex flex-col gap-3">
              {lostOpen.map((o) => {
                const d = donorBy(data.donors, o.donor_profile_id);
                return (
                  <li key={o.id} className="rounded-md border p-3 text-[13px]">
                    <div className="font-medium">
                      {shortId(o.id)} · {d?.domain ?? o.domain} · {data.canFinanceRead ? priceOf(o) : dash}
                    </div>
                    <p className="mt-1 text-muted-foreground">
                      {o.guarantee_until ? `The donor's stated term runs until ${day(o.guarantee_until)}.` : "No guarantee end date is stored."}{" "}
                      {o.paid_at ? `Paid ${day(o.paid_at)} after publication.` : "This order was not marked Paid."}
                    </p>
                    <div className="mt-2">
                      <Button variant="ghost" onClick={() => data.goSection("orders", o.id)}>
                        Open replacement or credit
                      </Button>
                    </div>
                  </li>
                );
              })}
            </ul>
          ) : (
            <p className="text-[13px] text-muted-foreground">No lost link is waiting for a replacement or a credit.</p>
          )}
        </Card>
      </div>
      <Card title="Payments ledger" description="Immutable Coverage payment rows, as on the invoice and in US dollars at the mid-market rate on the invoice date. A credit is a new reversal row, not an edit. Nothing here sends money.">
        {paid.length ? (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[860px] text-left text-[13px]">
              <thead className="text-muted-foreground">
                <tr className="border-b">
                  <th className="py-2 pr-3 font-medium">Date</th>
                  <th className="py-2 pr-3 font-medium">Order</th>
                  <th className="py-2 pr-3 font-medium">Donor</th>
                  <th className="py-2 pr-3 font-medium">Kind</th>
                  <th className="py-2 pr-3 text-right font-medium">As invoiced</th>
                  <th className="py-2 pr-3 text-right font-medium">In USD</th>
                  <th className="py-2 pr-3 font-medium">Rate</th>
                  <th className="py-2 pr-3 font-medium">Status</th>
                </tr>
              </thead>
              <tbody>
                {paid.flatMap((o) => {
                  const d = donorBy(data.donors, o.donor_profile_id);
                  const inv = invoiceOf(o);
                  const cells = (
                    <>
                      <td className="py-2 pr-3 text-right tabular-nums">{data.canFinanceRead && inv ? money(inv.amount_minor, inv.currency) : dash}</td>
                      <td className="py-2 pr-3 text-right tabular-nums">{data.canFinanceRead && inv ? money(inv.usd_amount_minor, "USD") : dash}</td>
                      <td className="py-2 pr-3 text-[12px] text-muted-foreground">{data.canFinanceRead && inv && inv.currency !== "USD" ? rateLine(inv) : dash}</td>
                    </>
                  );
                  const rows = [
                    <tr key={o.id} className="cursor-pointer border-b last:border-0 hover:bg-accent/40" onClick={() => data.goSection("orders", o.id)}>
                      <td className="py-2 pr-3">{o.paid_at ? day(o.paid_at) : dash}</td>
                      <td className="py-2 pr-3 font-mono text-[12px]">{shortId(o.id)}</td>
                      <td className="py-2 pr-3">{d?.domain ?? o.domain}</td>
                      <td className="py-2 pr-3">Payment</td>
                      {cells}
                      <td className="py-2 pr-3">
                        <Badge tone={statusTone[o.status]}>{statusLabel[o.status]}</Badge>
                      </td>
                    </tr>,
                  ];
                  if (o.credit_payment_id) {
                    rows.push(
                      <tr key={`${o.id}-credit`} className="cursor-pointer border-b last:border-0 hover:bg-accent/40" onClick={() => data.goSection("orders", o.id)}>
                        <td className="py-2 pr-3">{dash}</td>
                        <td className="py-2 pr-3 font-mono text-[12px]">{shortId(o.id)}</td>
                        <td className="py-2 pr-3">{d?.domain ?? o.domain}</td>
                        <td className="py-2 pr-3">Credit</td>
                        {cells}
                        <td className="py-2 pr-3">
                          <Badge tone="grey">Credit</Badge>
                        </td>
                      </tr>,
                    );
                  }
                  return rows;
                })}
              </tbody>
            </table>
          </div>
        ) : (
          <Empty title="No payments recorded">Paid is recorded after publication and a passing live-link check, and only by the brand approver.</Empty>
        )}
      </Card>
    </div>
  );
}
