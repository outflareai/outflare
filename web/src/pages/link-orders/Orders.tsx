import { useState } from "react";
import { Badge } from "../../ui";
import { dash, donorBy, priceOf, shortId, statusLabel, statuses, type PageData } from "./shared";
import { OrdersTable } from "./OrdersTable";
import { OrderSheet } from "./OrderSheet";

export function Orders({ data, openId, onClose }: { data: PageData; openId?: string; onClose: () => void }) {
  const [view, setView] = useState<"kanban" | "table">(() => {
    const v = new URLSearchParams(location.search).get("orders");
    return v === "table" ? "table" : "kanban";
  });
  const set = (next: "kanban" | "table") => {
    setView(next);
    const p = new URLSearchParams(location.search);
    if (next === "kanban") p.delete("orders");
    else p.set("orders", "table");
    const qs = p.toString();
    history.replaceState(history.state, "", location.pathname + (qs ? `?${qs}` : ""));
  };
  const counts = Object.fromEntries(statuses.map((s) => [s, data.orders.filter((o) => o.status === s).length]));
  const open = data.orders.find((o) => o.id === openId);

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-[13px] text-muted-foreground">
          {data.orders.length} order{data.orders.length === 1 ? "" : "s"} · Paid comes after Published, and only the brand's designated approver can Approve or mark Paid
        </p>
        <div className="flex rounded-md border p-0.5" role="tablist" aria-label="Orders view">
          <button type="button" role="tab" aria-selected={view === "kanban"} className={`rounded px-2 py-1 text-[12px] ${view === "kanban" ? "bg-accent font-medium" : "text-muted-foreground"}`} onClick={() => set("kanban")}>
            Kanban
          </button>
          <button type="button" role="tab" aria-selected={view === "table"} className={`rounded px-2 py-1 text-[12px] ${view === "table" ? "bg-accent font-medium" : "text-muted-foreground"}`} onClick={() => set("table")}>
            Table
          </button>
        </div>
      </div>
      {view === "kanban" ? (
        <div className="overflow-x-auto pb-2">
          <div className="flex min-w-max gap-2">
            {statuses.map((s) => (
              <div key={s} className="w-52 shrink-0 rounded-lg border bg-card p-2">
                <h3 className="mb-2 flex items-center justify-between text-[12px] font-medium text-muted-foreground">
                  {statusLabel[s]} <em className="not-italic tabular-nums text-foreground">{counts[s]}</em>
                </h3>
                <div className="flex flex-col gap-2">
                  {data.orders
                    .filter((o) => o.status === s)
                    .map((o) => {
                      const d = donorBy(data.donors, o.donor_profile_id);
                      return (
                        <button
                          key={o.id}
                          type="button"
                          className="rounded-md border bg-background px-2 py-2 text-left hover:bg-accent/40"
                          onClick={() => data.goSection("orders", o.id)}
                        >
                          <div className="flex flex-wrap items-center gap-1 text-[13px] font-medium">
                            {d?.domain ?? o.domain}
                            {d?.pbn ? <Badge tone="amber">PBN</Badge> : null}
                          </div>
                          <div className="truncate font-mono text-[11px] text-muted-foreground">{o.target_url || dash}</div>
                          <div className="mt-1 flex items-center justify-between text-[12px]">
                            <span className="font-mono text-muted-foreground">{shortId(o.id)}</span>
                            <b className="tabular-nums">{priceOf(o)}</b>
                          </div>
                        </button>
                      );
                    })}
                  {counts[s] === 0 ? <div className="px-1 py-3 text-[12px] text-muted-foreground">None</div> : null}
                </div>
              </div>
            ))}
          </div>
        </div>
      ) : (
        <div className="rounded-lg border bg-card p-3">
          <OrdersTable orders={data.orders} data={data} />
        </div>
      )}
      <p className="text-[12px] text-muted-foreground">
        Approve freezes the quoted donor price. Paid records a Coverage payment after publication and a passing live-link check; it never sends money.
      </p>
      <OrderSheet order={open} data={data} onClose={onClose} />
    </div>
  );
}
