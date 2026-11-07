// Presence opportunities: the places worth the team's effort, highest
// leverage first. Leverage is SEO (the site's domain rating and traffic)
// and AI answers (how often they cite the site) together, plus how many
// competitors are there. Picking one contacts nobody. The table sorts by
// any number, groups pages by site, can hide competitors' own sites, and
// loads more as you scroll.
import { useState } from "react";
import type { components, paths } from "@outflare/sdk";
import { api, errorMessage } from "../../api";
import { Badge, Button, ErrorText, field, toast } from "../../ui";
import { label } from "./client";
import { AccessBadge, AccessSelect } from "./linkvalue";
import { MoreOnScroll, Th, Toggle, num, useInfinite, usePref } from "./table";

type Opportunity = components["schemas"]["PlacementOpportunity"];
type List = paths["/v1/brands/{brand}/coverage/opportunities"]["get"]["responses"]["200"]["content"]["application/json"];
type Query = NonNullable<paths["/v1/brands/{brand}/coverage/opportunities"]["get"]["parameters"]["query"]>;
type State = "open" | "picked" | "dismissed" | "won" | "all";
type Reason = "competitor_gap" | "ai_cited" | "unlinked_mention" | "link_removed";
type Sort = NonNullable<Query["sort"]>;

const reasons: Record<Reason, string> = {
  competitor_gap: "Competitors are here, you aren't",
  ai_cited: "AI answers cite it",
  unlinked_mention: "Names you without a link",
  link_removed: "Your link was removed",
};
const actions: Record<string, string> = { pitch: "Pitch to be included", request_link: "Ask for a link", reclaim_link: "Win the link back" };
const parts: [string, string][] = [
  ["seo", "SEO"],
  ["ai", "AI answers"],
  ["competitors", "Competitors"],
  ["fit", "Page type"],
  ["relationship", "Relationship"],
];

export function Opportunities({ brand, manage, myId }: { brand: string; manage: boolean; myId: string }) {
  const [state, setState] = useState<State>("open"),
    [reason, setReason] = useState<Reason | "">(""),
    [access, setAccess] = useState<NonNullable<Query["access"]> | "">(""),
    [q, setQ] = useState(""),
    [sort, setSort] = usePref<Sort>("opportunities.sort", "score"),
    [minDR, setMinDR] = usePref<string>("opportunities.min_dr", ""),
    [bySite, setBySite] = usePref<boolean>("opportunities.by_site", false),
    [noRivals, setNoRivals] = usePref<boolean>("opportunities.no_rivals", true),
    [open, setOpen] = useState<Record<string, Opportunity[] | "loading">>({}),
    [busy, setBusy] = useState(false),
    [actError, setActError] = useState("");
  const query = (extra: Partial<Query> = {}): Query => ({
    state,
    sort,
    limit: 50,
    ...(reason ? { reason } : {}),
    ...(access ? { access } : {}),
    ...(q.trim() ? { q: q.trim() } : {}),
    ...(minDR ? { min_domain_rating: Number(minDR) } : {}),
    ...(noRivals ? { exclude_competitors: true } : {}),
    ...extra,
  });
  const list = useInfinite<Opportunity, List>(
    (cursor) =>
      api.GET("/v1/brands/{brand}/coverage/opportunities", {
        params: { path: { brand }, query: query({ ...(bySite ? { group: "domain" } : {}), ...(cursor ? { cursor } : {}) }) },
      }),
    [brand, state, reason, access, q, sort, minDR, bySite, noRivals],
    errorMessage,
  );
  const expand = async (host: string) => {
    if (open[host]) return setOpen(({ [host]: _, ...rest }) => rest);
    setOpen((o) => ({ ...o, [host]: "loading" }));
    const { data, error } = await api.GET("/v1/brands/{brand}/coverage/opportunities", { params: { path: { brand }, query: query({ host, limit: 200 }) } });
    if (error || !data) {
      setActError(errorMessage(error));
      return setOpen(({ [host]: _, ...rest }) => rest);
    }
    setOpen((o) => ({ ...o, [host]: data.data }));
  };
  const act = async (fn: () => Promise<{ error?: unknown }>, done: string) => {
    setBusy(true);
    setActError("");
    const { error } = await fn();
    setBusy(false);
    if (error) return setActError(errorMessage(error));
    setOpen({});
    list.reload();
    toast(done);
  };
  const update = (id: string, body: { state?: "open" | "picked" | "dismissed" | "won"; owner_id?: string | null }, done: string) =>
    act(() => api.PATCH("/v1/brands/{brand}/coverage/opportunities/{id}", { params: { path: { brand, id } }, body }), done);

  const row = (o: Opportunity, nested: boolean) => {
    const points = (o.factors.points ?? {}) as Record<string, number>;
    const f = o.factors as { domain_rating?: number; domain_traffic?: number; ai_citations?: number; competitors?: number; access?: string };
    const more = !nested && bySite ? (o.site_opportunities ?? 1) - 1 : 0;
    return (
      <tr key={o.id} className={`border-t align-top ${nested ? "bg-muted/20" : ""}`}>
        <td className="px-3 py-2">
          <div className="w-10 text-center">
            <div className="font-semibold tabular-nums">{Math.round(o.score)}</div>
            <div className="h-1 rounded bg-muted">
              <div className="h-1 rounded bg-brand" style={{ width: `${o.score}%` }} />
            </div>
          </div>
        </td>
        <td className={`max-w-md px-3 py-2 ${nested ? "pl-8" : ""}`}>
          <a className="block truncate text-brand underline-offset-2 hover:underline" href={o.url} target="_blank" rel="noreferrer noopener nofollow" title={o.url}>
            {o.title || o.url}
          </a>
          <div className="flex flex-wrap items-center gap-1 text-xs text-muted-foreground">
            <span>{o.host}</span>
            {o.kind && <span>· {label(o.kind)}</span>}
            {o.competitor_site && <Badge tone="amber">Competitor's site</Badge>}
            {more > 0 && (
              <button className="text-brand hover:underline" onClick={() => expand(o.host)}>
                {open[o.host] ? "Hide" : `+${more} more on this site`}
              </button>
            )}
          </div>
        </td>
        <td className="px-3 py-2 text-xs">
          <div>{reasons[o.reason as Reason]}</div>
          <div className="mt-1 flex flex-wrap gap-1">
            <Badge tone="blue">{actions[o.action]}</Badge>
            {o.evidence.likely_paid ? <Badge tone="grey">Competitors likely paid</Badge> : null}
            {o.state !== "open" && <Badge tone={o.state === "won" ? "green" : "grey"}>{label(o.state)}</Badge>}
          </div>
        </td>
        <td className="px-3 py-2">
          <AccessBadge access={f.access} />
        </td>
        <td className="px-3 py-2 text-right tabular-nums">{num(f.domain_rating)}</td>
        <td className="px-3 py-2 text-right tabular-nums">{num(f.domain_traffic)}</td>
        <td className="px-3 py-2 text-right tabular-nums">{num(f.ai_citations ?? 0)}</td>
        <td className="px-3 py-2 text-right tabular-nums">{num(f.competitors ?? 0)}</td>
        <td className="hidden px-3 py-2 text-xs text-muted-foreground xl:table-cell">
          {parts
            .filter(([k]) => points[k])
            .map(([k, name]) => `${name} +${points[k]}`)
            .join(" · ")}
        </td>
        {manage && (
          <td className="px-3 py-2">
            <div className="flex justify-end gap-1">
              {o.state !== "picked" && o.state !== "won" && (
                <Button variant="ghost" disabled={busy} onClick={() => update(o.id, { state: "picked", owner_id: myId }, "Picked: it's yours")}>
                  Pick
                </Button>
              )}
              {o.state !== "won" && (
                <Button variant="ghost" disabled={busy} onClick={() => update(o.id, { state: "won" }, "Marked won")}>
                  Won
                </Button>
              )}
              {o.state === "open" ? (
                <Button variant="ghost" disabled={busy} onClick={() => update(o.id, { state: "dismissed" }, "Dismissed")}>
                  Dismiss
                </Button>
              ) : (
                <Button variant="ghost" disabled={busy} onClick={() => update(o.id, { state: "open" }, "Reopened")}>
                  Reopen
                </Button>
              )}
            </div>
          </td>
        )}
      </tr>
    );
  };

  return (
    <div className="space-y-3">
      <div>
        <h2 className="font-medium">
          Opportunities · {list.page?.total ?? 0}
          {bySite ? " sites" : ""}
        </h2>
        <p className="text-sm text-muted-foreground">
          Ranked by leverage: SEO and AI answers together, plus how many competitors are there. Updated every day and after each competitor scan. Click a column to sort by it.
        </p>
      </div>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <select className={`${field} !w-auto`} aria-label="State" value={state} onChange={(e) => setState(e.target.value as State)}>
          <option value="open">Open</option>
          <option value="picked">Picked</option>
          <option value="won">Won</option>
          <option value="dismissed">Dismissed</option>
          <option value="all">All</option>
        </select>
        <select className={`${field} !w-auto`} aria-label="Reason" value={reason} onChange={(e) => setReason(e.target.value as Reason | "")}>
          <option value="">Every reason</option>
          {Object.entries(reasons).map(([k, v]) => (
            <option key={k} value={k}>
              {v}
            </option>
          ))}
        </select>
        <AccessSelect className={`${field} !w-auto`} value={access} onChange={setAccess} />
        <select className={`${field} !w-auto`} aria-label="Domain rating" value={minDR} onChange={(e) => setMinDR(e.target.value)}>
          <option value="">Any domain rating</option>
          {["20", "40", "60", "80"].map((n) => (
            <option key={n} value={n}>
              DR {n}+
            </option>
          ))}
        </select>
        <input className={`${field} w-56`} placeholder="Search site or title" value={q} onChange={(e) => setQ(e.target.value)} />
        <Toggle on={bySite} onChange={setBySite} hint="One row per site, its best opportunity. Open a site to see the rest.">
          One row per site
        </Toggle>
        <Toggle on={noRivals} onChange={setNoRivals} hint="Competitors rarely link to a rival, so their own sites are hidden.">
          Hide competitors' sites
        </Toggle>
        {manage && (
          <Button
            className="ml-auto"
            variant="ghost"
            disabled={busy}
            onClick={() => act(() => api.POST("/v1/brands/{brand}/coverage/opportunities/refresh", { params: { path: { brand } } }), "Opportunities updated")}
          >
            Refresh
          </Button>
        )}
      </div>
      <ErrorText>{list.error || actError}</ErrorText>
      <div className="overflow-x-auto rounded-xl border">
        <table className="w-full text-sm">
          <thead className="bg-muted/30">
            <tr>
              <Th sort="score" by={sort} onSort={setSort}>
                Score
              </Th>
              <Th by={sort} onSort={setSort}>{bySite ? "Site · best page" : "Page"}</Th>
              <Th by={sort} onSort={setSort}>
                Why · next step
              </Th>
              <Th by={sort} onSort={setSort} title="How a link here is got: can't buy, earned, paid or free to get">
                How it's got
              </Th>
              <Th sort="domain_rating" by={sort} onSort={setSort} className="text-right" title="The site's strength, 0 to 100">
                DR
              </Th>
              <Th sort="traffic" by={sort} onSort={setSort} className="text-right" title="The site's monthly search traffic">
                Traffic
              </Th>
              <Th sort="ai_citations" by={sort} onSort={setSort} className="text-right" title="How often AI answers cited the site in 90 days">
                AI cites
              </Th>
              <Th sort="competitors" by={sort} onSort={setSort} className="text-right" title="How many of your competitors are there">
                Competitors
              </Th>
              <Th by={sort} onSort={setSort} className="hidden xl:table-cell">
                Score parts
              </Th>
              {manage && <Th by={sort} onSort={setSort}>{""}</Th>}
            </tr>
          </thead>
          <tbody>
            {list.rows.flatMap((o) => {
              const sub = open[o.host];
              const extra = bySite && Array.isArray(sub) ? sub.filter((x) => x.id !== o.id).map((x) => row(x, true)) : [];
              return [row(o, false), ...extra];
            })}
          </tbody>
        </table>
        {list.rows.length === 0 && !list.loading && (
          <p className="p-4 text-center text-sm text-muted-foreground">
            Nothing here yet. Opportunities come from where competitors appear and from your placements.
          </p>
        )}
        <MoreOnScroll more={list.more} hasMore={list.hasMore} loading={list.loading} />
      </div>
    </div>
  );
}
