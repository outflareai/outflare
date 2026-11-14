import { useState } from "react";
import { api, errorMessage } from "../../api";
import { Badge, Button, Card, Empty, ErrorText, field, Loading, toast } from "../../ui";
import { idemp, type LinkOrderTargetSuggestion, type PageData } from "./shared";
import { anchorSourceLabel, kindLabel, reasonText, useTargets } from "./targets";

/**
 * The brand's article and product pages a paid link can point at. Ticked
 * pages are offered when ordering; suggestions come from stored competitor
 * data only and never create an order.
 */
export function Targets({ data }: { data: PageData }) {
  const [tick, setTick] = useState(0);
  const { suggestions, sources, saved, error, ready } = useTargets(data.brand, tick);
  const [kind, setKind] = useState<"" | "article" | "product">("");
  const [add, setAdd] = useState({ url: "", kind: "article" as "article" | "product" });
  const [busy, setBusy] = useState("");
  const [err, setErr] = useState("");
  const reload = () => setTick((n) => n + 1);

  const run = async (key: string, fn: () => Promise<{ error?: unknown }>, done: string) => {
    setBusy(key);
    setErr("");
    try {
      const { error } = await fn();
      if (error) throw error;
      toast(done);
      reload();
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy("");
    }
  };

  const toggle = (s: LinkOrderTargetSuggestion) =>
    s.target_id
      ? run(s.url, () => api.PATCH("/v1/brands/{brand}/link-orders/targets/{id}", { params: { path: { brand: data.brand, id: s.target_id! }, header: idemp() }, body: { selected: false } }), "Unticked: no longer offered for new orders.")
      : run(s.url, () => api.POST("/v1/brands/{brand}/link-orders/targets", { params: { path: { brand: data.brand }, header: idemp() }, body: { url: s.url, kind: s.kind, selected: true, source: "suggested" } }), "Ticked: offered for new orders.");

  const retype = (id: string, url: string, next: "article" | "product") =>
    run(url, () => api.PATCH("/v1/brands/{brand}/link-orders/targets/{id}", { params: { path: { brand: data.brand, id }, header: idemp() }, body: { kind: next } }), `Marked as ${kindLabel[next].toLowerCase()}.`);

  const addPage = () =>
    run("add", () => api.POST("/v1/brands/{brand}/link-orders/targets", { params: { path: { brand: data.brand }, header: idemp() }, body: { url: add.url.trim(), kind: add.kind, selected: true } }), "Added and ticked.").then(() => setAdd({ ...add, url: "" }));

  if (!ready) return <Loading />;
  const list = suggestions.filter((s) => !kind || s.kind === kind);
  const unticked = saved.filter((t) => !t.selected && (!kind || t.kind === kind));
  const se = sources?.se_ranking_keywords ?? 0;

  return (
    <div className="flex flex-col gap-3">
      <Card
        title="Link targets"
        description="Your own article and product pages, ranked by the comparison with tracked competitors: keywords they rank for where you are missing or behind, and their pages other sites link to. Read from stored data only; this tab never calls Ahrefs, DataForSEO or SE Ranking, and never creates an order. Tick a page to offer it when ordering a link."
        action={
          <div className="flex rounded-md border p-0.5">
            {([["", "All"], ["article", "Articles"], ["product", "Products"]] as const).map(([id, label]) => (
              <button key={id || "all"} type="button" className={`rounded px-2 py-1 text-[12px] ${kind === id ? "bg-accent font-medium" : "text-muted-foreground"}`} onClick={() => setKind(id)}>
                {label}
              </button>
            ))}
          </div>
        }
      >
        {error ? <ErrorText>{error}</ErrorText> : null}
        {err ? <ErrorText>{err}</ErrorText> : null}
        {sources ? (
          <p className="mb-2 text-[12px] text-muted-foreground" data-testid="target-sources">
            Based on {se} stored SE Ranking keyword{se === 1 ? "" : "s"}, {sources.other_keywords} from other SEO connections, {sources.page_targets} keyword target{sources.page_targets === 1 ? "" : "s"},
            {" "}{sources.competitor_keywords} competitor keywords and {sources.competitor_link_pages} competitor pages with stored links.
            {se === 0 ? " No SE Ranking data is stored for this brand's pages yet, so primary anchors come from keyword targets or other stored keywords." : ""}
          </p>
        ) : null}
        {list.length ? (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[760px] text-left text-[13px]">
              <thead className="text-muted-foreground">
                <tr className="border-b">
                  <th className="py-2 pr-3 font-normal">Use</th>
                  <th className="py-2 pr-3 font-normal">Page</th>
                  <th className="py-2 pr-3 font-normal">Kind</th>
                  <th className="py-2 pr-3 font-normal">Why</th>
                  <th className="py-2 pr-3 font-normal">Primary anchor</th>
                  <th className="py-2 pr-3 text-right font-normal">Score</th>
                </tr>
              </thead>
              <tbody>
                {list.map((s) => (
                  <tr key={s.url} className="border-b align-top last:border-0">
                    <td className="py-2 pr-3">
                      <input type="checkbox" aria-label={`Use ${s.url}`} checked={s.selected} disabled={!data.canManage || busy === s.url} onChange={() => void toggle(s)} />
                    </td>
                    <td className="max-w-[260px] truncate py-2 pr-3 font-mono text-[12px]">{s.url}</td>
                    <td className="py-2 pr-3">
                      {s.target_id && data.canManage ? (
                        <select className={field} aria-label="Kind" value={s.kind} onChange={(e) => void retype(s.target_id!, s.url, e.target.value as "article" | "product")}>
                          <option value="article">Article</option>
                          <option value="product">Product</option>
                        </select>
                      ) : (
                        <Badge tone={s.kind === "article" ? "blue" : "green"} title={s.kind_guessed ? "Guessed from the address; tick to save and change it" : undefined}>
                          {kindLabel[s.kind]}{s.kind_guessed ? "?" : ""}
                        </Badge>
                      )}
                    </td>
                    <td className="py-2 pr-3 text-[12px] text-muted-foreground">
                      {s.reasons.length ? s.reasons.slice(0, 2).map((r, i) => <div key={i}>{reasonText(r)}</div>) : "Ticked by you"}
                    </td>
                    <td className="py-2 pr-3 text-[12px]">
                      {s.anchors.primary[0] ? (
                        <span className="flex flex-wrap items-center gap-1">
                          {s.anchors.primary[0].text}
                          <Badge tone={s.anchors.primary[0].source === "se_ranking" ? "green" : "grey"}>{anchorSourceLabel[s.anchors.primary[0].source]}</Badge>
                        </span>
                      ) : "—"}
                    </td>
                    <td className="py-2 pr-3 text-right font-mono text-[12px]">{s.score || "—"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <Empty title="No pages to suggest yet">
            Suggestions need stored keyword snapshots for this brand's site and tracked competitors. Add a page below to tick it by hand.
          </Empty>
        )}
        {data.canManage ? (
          <div className="mt-3 flex flex-wrap items-end gap-2">
            <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
              Add a page
              <input className={`${field} w-[320px]`} value={add.url} placeholder="https://… (your own site)" onChange={(e) => setAdd({ ...add, url: e.target.value })} />
            </label>
            <select className={field} aria-label="New page kind" value={add.kind} onChange={(e) => setAdd({ ...add, kind: e.target.value as "article" | "product" })}>
              <option value="article">Article</option>
              <option value="product">Product</option>
            </select>
            <Button disabled={!add.url.trim() || busy === "add"} onClick={() => void addPage()}>Add and tick</Button>
          </div>
        ) : null}
      </Card>
      {unticked.length ? (
        <Card title="Unticked pages" description="Saved pages that are not offered for new orders. Tick one to offer it again.">
          <ul className="flex flex-col gap-1 text-[13px]">
            {unticked.map((t) => (
              <li key={t.id} className="flex items-center gap-2">
                <input
                  type="checkbox"
                  aria-label={`Use ${t.url}`}
                  checked={false}
                  disabled={!data.canManage || busy === t.url}
                  onChange={() => void run(t.url, () => api.PATCH("/v1/brands/{brand}/link-orders/targets/{id}", { params: { path: { brand: data.brand, id: t.id }, header: idemp() }, body: { selected: true } }), "Ticked: offered for new orders.")}
                />
                <span className="font-mono text-[12px]">{t.url}</span>
                <Badge tone={t.kind === "article" ? "blue" : "green"}>{kindLabel[t.kind]}</Badge>
              </li>
            ))}
          </ul>
        </Card>
      ) : null}
    </div>
  );
}
