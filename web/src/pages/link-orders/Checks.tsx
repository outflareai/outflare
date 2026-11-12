import { useState } from "react";
import { api, errorMessage } from "../../api";
import { Button, Card, Empty, ErrorText, toast } from "../../ui";
import { dash, day, donorBy, idemp, shortId, type LinkOrder, type PageData } from "./shared";

function mark(ok: boolean | null): string {
  if (ok == null) return dash;
  return ok ? "Yes" : "No";
}

export function Checks({ data }: { data: PageData }) {
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState("");
  const list = data.orders.filter((o) => o.status === "published" || o.status === "paid" || o.status === "verified" || o.status === "lost");

  const recheck = async (o: LinkOrder) => {
    if (!data.canManage) return;
    setBusy(o.id);
    setErr("");
    try {
      const { error } = await api.POST("/v1/brands/{brand}/link-orders/{id}/check", {
        params: { path: { brand: data.brand, id: o.id }, header: idemp() },
      });
      if (error) throw error;
      toast("Live-link check queued.");
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy("");
    }
  };

  return (
    <Card
      title="Link checks"
      description="Live, rel, anchor and target come from the coverage.check job (and from the weekly backlinks sync). Indexed on a third-party URL is out of this version, so that column stays unknown and does not block Verified. Two agreeing down checks are required before Lost; a network error does not count."
    >
      {err ? <ErrorText>{err}</ErrorText> : null}
      {list.length ? (
        <div className="overflow-x-auto">
          <table className="w-full min-w-[860px] text-left text-[13px]">
            <thead className="text-muted-foreground">
              <tr className="border-b">
                <th className="py-2 pr-3 font-medium">Order</th>
                <th className="py-2 pr-3 font-medium">Published URL</th>
                <th className="py-2 pr-3 font-medium">Live</th>
                <th className="py-2 pr-3 font-medium">Indexed</th>
                <th className="py-2 pr-3 font-medium">Rel</th>
                <th className="py-2 pr-3 font-medium">Anchor</th>
                <th className="py-2 pr-3 font-medium">Target</th>
                <th className="py-2 pr-3 font-medium">Last check</th>
                <th className="py-2 pr-3 font-medium"></th>
              </tr>
            </thead>
            <tbody>
              {list.map((o) => {
                const d = donorBy(data.donors, o.donor_profile_id);
                return (
                  <tr key={o.id} className="cursor-pointer border-b last:border-0 hover:bg-accent/40" onClick={() => data.goSection("orders", o.id)}>
                    <td className="py-2 pr-3 font-mono text-[12px]">
                      {shortId(o.id)}
                      <div className="font-sans text-[12px] text-muted-foreground">{d?.domain ?? o.domain}</div>
                    </td>
                    <td className="max-w-[260px] truncate py-2 pr-3 font-mono text-[12px]">{o.published_url ? o.published_url.replace(/^https?:\/\//, "") : dash}</td>
                    <td className="py-2 pr-3">{mark(o.last_check_live)}</td>
                    <td className="py-2 pr-3">{dash}</td>
                    <td className="py-2 pr-3">{o.last_check_rel || dash}</td>
                    <td className="py-2 pr-3">{o.last_check_anchor || dash}</td>
                    <td className="py-2 pr-3">{mark(o.last_check_target_match)}</td>
                    <td className="py-2 pr-3 text-muted-foreground">{o.last_check_at ? day(o.last_check_at) : dash}</td>
                    <td className="py-2 pr-3" onClick={(e) => e.stopPropagation()}>
                      {data.canManage && o.status !== "lost" ? (
                        <Button variant="ghost" disabled={busy === o.id} onClick={() => void recheck(o)}>
                          Recheck
                        </Button>
                      ) : null}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      ) : (
        <Empty title="No published orders yet">Checks appear once an order has a published URL.</Empty>
      )}
    </Card>
  );
}
