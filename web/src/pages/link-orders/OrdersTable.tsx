import { Badge } from "../../ui";
import { amountOf, dash, donorBy, money, shortId, slotLabel, statusLabel, statusTone, type LinkOrder, type PageData } from "./shared";

export function OrdersTable({ orders, data }: { orders: LinkOrder[]; data: PageData }) {
  if (!orders.length) {
    return <p className="text-[13px] text-muted-foreground">No orders yet.</p>;
  }
  return (
    <div className="overflow-x-auto">
      <table className="w-full min-w-[640px] text-left text-[13px]">
        <thead className="text-muted-foreground">
          <tr className="border-b">
            <th className="py-2 pr-3 font-medium">Order</th>
            <th className="py-2 pr-3 font-medium">Donor</th>
            <th className="py-2 pr-3 font-medium">Target</th>
            <th className="py-2 pr-3 font-medium">Type</th>
            <th className="py-2 pr-3 font-medium">Status</th>
            <th className="py-2 pr-3 text-right font-medium">Donor price</th>
          </tr>
        </thead>
        <tbody>
          {orders.map((o) => {
            const d = donorBy(data.donors, o.donor_profile_id);
            const { amount, currency } = amountOf(o);
            return (
              <tr key={o.id} className="cursor-pointer border-b last:border-0 hover:bg-accent/40" onClick={() => data.goSection("orders", o.id)}>
                <td className="py-2 pr-3 font-mono text-[12px]">{shortId(o.id)}</td>
                <td className="py-2 pr-3">
                  <div className="font-medium">{d?.domain ?? o.domain}</div>
                  {d?.pbn ? <Badge tone="amber">PBN</Badge> : null}
                </td>
                <td className="max-w-[220px] truncate py-2 pr-3 font-mono text-[12px]">{o.target_url || dash}</td>
                <td className="py-2 pr-3">{slotLabel(o.slot_type)}</td>
                <td className="py-2 pr-3">
                  <Badge tone={statusTone[o.status]}>{statusLabel[o.status]}</Badge>
                </td>
                <td className="py-2 pr-3 text-right tabular-nums">{money(amount, currency)}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
