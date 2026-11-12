import { useState } from "react";
import { api, errorMessage } from "../../api";
import { Badge, Button, Card, Empty, ErrorText, field, toast } from "../../ui";
import { dash, idemp, type PageData } from "./shared";

const filters = [
  ["all", "All"],
  ["not_in_catalog", "Not in catalog"],
  ["in_catalog", "In catalog"],
  ["ordered", "Ordered"],
] as const;

const statusLabel = {
  not_in_catalog: "Not in catalog",
  in_catalog: "In catalog",
  ordered: "Ordered",
} as const;

export function Candidates({ data }: { data: PageData }) {
  const [filter, setFilter] = useState<(typeof filters)[number][0]>("all");
  const [busy, setBusy] = useState("");
  const [err, setErr] = useState("");
  const [target, setTarget] = useState("/");

  const list = data.candidates.filter((c) => filter === "all" || c.status === filter);

  const add = async (domain: string) => {
    if (!data.canManage) return;
    setBusy(domain);
    setErr("");
    try {
      const { error } = await api.POST("/v1/brands/{brand}/link-orders/candidates/{domain}/add-to-catalog", {
        params: { path: { brand: data.brand, domain }, header: idemp() },
      });
      if (error) throw error;
      toast(`Added ${domain} to the catalog, with no price yet.`);
      data.reload();
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy("");
    }
  };

  const request = async (c: (typeof data.candidates)[number]) => {
    if (!data.canManage || !c.donor_profile_id) return;
    const url = target.trim();
    if (!url) {
      setErr("Set a target URL for the request.");
      return;
    }
    setBusy(c.domain);
    setErr("");
    try {
      const { error } = await api.POST("/v1/brands/{brand}/link-orders", {
        params: { path: { brand: data.brand }, header: idemp() },
        body: { donor_profile_id: c.donor_profile_id, target_url: url },
      });
      if (error) throw error;
      toast(`Opened a request for ${c.domain}.`);
      data.reload();
      data.goSection("orders");
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy("");
    }
  };

  return (
    <Card
      title="Donor candidates"
      description="Domains that link to tracked competitors and not to this brand, read from already-saved link gaps. This tab does not call Ahrefs or DataForSEO, and it does not POST /seo-competitors/gaps/links. SE Ranking has no referring-domain report, so it cannot fill this list."
      action={
        <div className="flex flex-wrap items-center gap-2">
          <label className="flex items-center gap-2 text-[12px] text-muted-foreground">
            Target URL
            <input className={field} value={target} onChange={(e) => setTarget(e.target.value)} />
          </label>
          <div className="flex rounded-md border p-0.5">
            {filters.map(([id, label]) => (
              <button
                key={id}
                type="button"
                className={`rounded px-2 py-1 text-[12px] ${filter === id ? "bg-accent font-medium" : "text-muted-foreground"}`}
                onClick={() => setFilter(id)}
              >
                {label}
              </button>
            ))}
          </div>
        </div>
      }
    >
      {err ? <ErrorText>{err}</ErrorText> : null}
      {list.length ? (
        <div className="overflow-x-auto">
          <table className="w-full min-w-[720px] text-left text-[13px]">
            <thead className="text-muted-foreground">
              <tr className="border-b">
                <th className="py-2 pr-3 font-medium">Domain</th>
                <th className="py-2 pr-3 font-medium">DR</th>
                <th className="py-2 pr-3 font-medium">Traffic</th>
                <th className="py-2 pr-3 font-medium">Competitors</th>
                <th className="py-2 pr-3 font-medium">Status</th>
                <th className="py-2 pr-3 font-medium"></th>
              </tr>
            </thead>
            <tbody>
              {list.map((c) => (
                <tr key={c.domain} className="border-b last:border-0">
                  <td className="py-2 pr-3">
                    <div className="font-medium">{c.domain}</div>
                  </td>
                  <td className="py-2 pr-3 tabular-nums">{c.rating ?? dash}</td>
                  <td className="py-2 pr-3 tabular-nums" data-testid="candidate-traffic">
                    {dash}
                  </td>
                  <td className="py-2 pr-3">{c.competitors.join(", ") || dash}</td>
                  <td className="py-2 pr-3">
                    <Badge tone={c.status === "ordered" ? "green" : c.status === "in_catalog" ? "blue" : "grey"}>{statusLabel[c.status]}</Badge>
                  </td>
                  <td className="py-2 pr-3">
                    {data.canManage && c.status === "not_in_catalog" ? (
                      <Button variant="ghost" disabled={busy === c.domain} onClick={() => void add(c.domain)}>
                        Add to catalog
                      </Button>
                    ) : null}
                    {data.canManage && c.status === "in_catalog" ? (
                      <Button disabled={busy === c.domain} onClick={() => void request(c)}>
                        Request price
                      </Button>
                    ) : null}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <Empty title="No candidates in this filter">Saved competitor link gaps that are not already linking to this brand will appear here.</Empty>
      )}
      <p className="mt-2 text-[12px] text-muted-foreground">
        Traffic is not stored on the gap, so this column is always {dash}. The weekly snapshot only reads already-saved referring domains; a full referring-domain pull is a separate paid read and is not run from this page.
      </p>
    </Card>
  );
}
