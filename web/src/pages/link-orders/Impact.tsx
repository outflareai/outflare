import { useEffect, useMemo, useState } from "react";
import { api, errorMessage } from "../../api";
import { Card, Empty, ErrorText, field, Loading } from "../../ui";
import { dash, donorBy, shortId, type LinkOrder, type LinkOrderImpact, type PageData } from "./shared";

const defaultNote = "These figures do not show that the link caused a change. They are already-saved Search Console and ranking points around publication, not a fresh paid read.";

function spark(before: number, after: number) {
  const max = Math.max(before, after, 1);
  const y = (v: number) => 100 - (v / max) * 80 - 5;
  return (
    <svg className="h-28 w-full text-muted-foreground" viewBox="0 0 220 120" aria-hidden="true">
      <line x1="20" y1="10" x2="20" y2="100" stroke="currentColor" opacity=".12" />
      <line x1="20" y1="100" x2="210" y2="100" stroke="currentColor" opacity=".12" />
      <polyline fill="none" stroke="currentColor" strokeWidth="2.5" className="text-foreground" points={`30,${y(before)} 190,${y(after)}`} />
      <circle cx="30" cy={y(before)} r="3" className="fill-foreground" />
      <circle cx="190" cy={y(after)} r="3" className="fill-foreground" />
      <text x="30" y="115" fontSize="10" fill="currentColor" opacity=".6">
        Before
      </text>
      <text x="190" y="115" fontSize="10" textAnchor="end" fill="currentColor" opacity=".6">
        After
      </text>
    </svg>
  );
}

function keywordRows(impact: LinkOrderImpact) {
  const by = new Map<string, { first: number | null; last: number | null }>();
  for (const r of impact.rankings.series) {
    const cur = by.get(r.keyword) ?? { first: null, last: null };
    if (cur.first == null) cur.first = r.position;
    cur.last = r.position;
    by.set(r.keyword, cur);
  }
  return [...by.entries()].map(([q, v]) => ({ q, before: v.first, after: v.last }));
}

export function Impact({ data }: { data: PageData }) {
  const published = data.orders.filter((o) => o.published_at);
  const [id, setId] = useState(published[0]?.id ?? "");
  const [impact, setImpact] = useState<LinkOrderImpact | null>(null);
  const [err, setErr] = useState("");
  const [loading, setLoading] = useState(false);
  const current: LinkOrder | undefined = published.find((o) => o.id === id) ?? published[0];

  useEffect(() => {
    if (current && current.id !== id) setId(current.id);
  }, [current?.id]);

  useEffect(() => {
    if (!current) {
      setImpact(null);
      return;
    }
    let gone = false;
    setLoading(true);
    setErr("");
    api.GET("/v1/brands/{brand}/link-orders/{id}/impact", { params: { path: { brand: data.brand, id: current.id } } }).then(({ data: row, error }) => {
      if (gone) return;
      setLoading(false);
      if (error) {
        setImpact(null);
        setErr(errorMessage(error));
        return;
      }
      setImpact(row ?? null);
    });
    return () => {
      gone = true;
    };
  }, [current?.id, data.brand]);

  const kws = useMemo(() => (impact ? keywordRows(impact) : []), [impact]);
  const d = current ? donorBy(data.donors, current.donor_profile_id) : undefined;
  const sc = impact?.search_console;
  const before = sc?.baseline?.clicks;
  const after = sc?.after?.clicks;
  const posBefore = sc?.baseline?.position;
  const posAfter = sc?.after?.position;
  const note = impact?.note?.trim() || defaultNote;

  if (!published.length) {
    return <Empty title="No published order yet">Impact is available after an order has a publication date. Figures are stored snapshots only and are not a causal claim.</Empty>;
  }

  return (
    <div className="flex flex-col gap-3">
      <Card
        title="Effect on the target page"
        description={note}
        action={
          <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
            Order / page
            <select className={field} value={current?.id ?? ""} onChange={(e) => setId(e.target.value)} aria-label="Order or page">
              {published.map((o) => (
                <option key={o.id} value={o.id}>
                  {o.target_url} · {shortId(o.id)}
                </option>
              ))}
            </select>
          </label>
        }
      >
        <p className="text-[13px] text-muted-foreground">
          Comparison uses already-saved Search Console and ranking points around publication (typically 28 days before and after). This is not a claim that the link caused a change, and this page does not call a connector.
        </p>
      </Card>
      {err ? <ErrorText>{err}</ErrorText> : null}
      {loading ? <Loading /> : null}
      {impact && !loading ? (
        <>
          <div className="grid gap-3 lg:grid-cols-2">
            <Card title={<span>Search Console clicks · <span className="font-mono text-[13px] font-normal">{current?.target_url}</span></span>}>
              {sc?.available && before != null && after != null ? (
                <>
                  {spark(before, after)}
                  <div className="flex flex-wrap gap-3 text-[13px]">
                    <span>Before {before.toLocaleString()}</span>
                    <span>After {after.toLocaleString()}</span>
                    <span className={after >= before ? "text-emerald-600 dark:text-emerald-400" : "text-red-600 dark:text-red-400"}>
                      {after - before >= 0 ? "+" : ""}
                      {after - before}
                    </span>
                  </div>
                  <p className="mt-2 text-[12px] text-muted-foreground">
                    Avg. position {posBefore == null ? dash : posBefore.toFixed(1)} → {posAfter == null ? dash : posAfter.toFixed(1)}.
                    Link from {d?.domain ?? current?.domain}, {current?.status}. Movement on the page is shown next to the order; it is not attributed to it.
                  </p>
                </>
              ) : (
                <p className="text-[13px] text-muted-foreground">No stored Search Console window is available for this order yet.</p>
              )}
            </Card>
            <Card title="Tracked keywords on this page">
              {kws.length ? (
                <table className="w-full text-left text-[13px]">
                  <thead className="text-muted-foreground">
                    <tr className="border-b">
                      <th className="py-2 pr-3 font-medium">Keyword</th>
                      <th className="py-2 pr-3 text-right font-medium">Before</th>
                      <th className="py-2 pr-3 text-right font-medium">After</th>
                    </tr>
                  </thead>
                  <tbody>
                    {kws.map((k) => (
                      <tr key={k.q} className="border-b last:border-0">
                        <td className="py-2 pr-3">{k.q}</td>
                        <td className="py-2 pr-3 text-right tabular-nums">{k.before ?? dash}</td>
                        <td className="py-2 pr-3 text-right tabular-nums">{k.after ?? dash}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              ) : (
                <p className="text-[13px] text-muted-foreground">No stored keyword ranks sit on this page in the window around publication.</p>
              )}
              <p className="mt-2 text-[12px] text-muted-foreground">Lower is better. These are already-tracked keywords, not a fresh ranking pull.</p>
            </Card>
          </div>
          <Card title="How to read this">
            <p className="text-[13px]">{note}</p>
          </Card>
        </>
      ) : null}
    </div>
  );
}
