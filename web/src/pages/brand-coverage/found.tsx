// Found: every page discovery found that links to or names the brand,
// grouped by site. Each page was read once to see whether it's about the
// brand, so the list opens on the ones that are, and "About us" or "Not
// us" settles a page, or a whole site, in one click. "About us" makes it a
// placement under the site's publisher with no CRM setup needed. It is a
// table: one row per site with its numbers, sortable and filterable, and
// more rows load as you scroll.
import { useState } from "react";
import type { paths } from "@outflare/sdk";
import { api, errorMessage } from "../../api";
import { Badge, Button, Empty, ErrorText, field, toast, type tones } from "../../ui";
import { MoreOnScroll, Th, Toggle, num, useInfinite, usePref } from "./table";
import { label } from "./client";

type Found = paths["/v1/brands/{brand}/coverage/found"]["get"]["responses"]["200"]["content"]["application/json"];
type Site = Found["data"][number];
type Query = NonNullable<paths["/v1/brands/{brand}/coverage/found"]["get"]["parameters"]["query"]>;
type Sort = NonNullable<Query["sort"]>;
type View = "about" | "maybe" | "not" | "confirmed";
type Tone = keyof typeof tones;

const views: [View, string, string][] = [
  ["about", "About you", "Pages that link to you or name your website."],
  ["maybe", "Maybe", "Pages that only use your name, which another product may share, or couldn't be read yet."],
  ["not", "Not about you", "Pages you said aren't about you, or that don't name you at all."],
  ["confirmed", "Confirmed", "Pages you said are about you. They're placements now, checked over time."],
];
const verdicts: Record<string, [string, Tone]> = {
  links: ["Links to you", "green"],
  mentions: ["Names your website", "green"],
  name_only: ["Name only", "amber"],
  absent: ["Doesn't name you", "grey"],
  unreachable: ["Couldn't read", "grey"],
  unchecked: ["Checking", "blue"],
};
const sources: Record<string, string> = {
  backlinks: "Backlinks",
  web_search: "Web search",
  news: "News",
  social: "Social",
  ai_citations: "AI answers",
  link_prospects: "Link prospects",
  manual: "Added by hand",
  import: "Import",
};

export function PresenceFound({ brand, manage }: { brand: string; manage: boolean }) {
  const [view, setView] = useState<View>("about"),
    [q, setQ] = useState(""),
    [source, setSource] = useState(""),
    [sort, setSort] = usePref<Sort>("found.sort", "best"),
    [minDR, setMinDR] = usePref<string>("found.min_dr", ""),
    [noRivals, setNoRivals] = usePref<boolean>("found.no_rivals", false),
    [open, setOpen] = useState<Record<string, boolean>>({}),
    [busy, setBusy] = useState(""),
    [actError, setActError] = useState("");
  const list = useInfinite<Site, Found>(
    (cursor) =>
      api.GET("/v1/brands/{brand}/coverage/found", {
        params: {
          path: { brand },
          query: {
            view,
            sort,
            limit: 50,
            ...(q.trim() ? { q: q.trim() } : {}),
            ...(source ? { source } : {}),
            ...(minDR ? { min_domain_rating: Number(minDR) } : {}),
            ...(noRivals ? { exclude_competitors: true } : {}),
            ...(cursor ? { cursor } : {}),
          },
        },
      }),
    [brand, view, q, source, sort, minDR, noRivals],
    errorMessage,
  );

  const review = async (key: string, decision: "about_us" | "not_us", body: { ids?: string[]; host?: string }) => {
    setBusy(key);
    setActError("");
    try {
      const { data, error } = await api.POST("/v1/brands/{brand}/coverage/found/review", { params: { path: { brand } }, body: { decision, ...body } });
      if (error) throw new Error(errorMessage(error));
      const n = data && "reviewed" in data ? data.reviewed : 0;
      toast(decision === "about_us" ? `${n} added to your placements` : `${n} marked not about you`);
      list.reload();
    } catch (e) {
      setActError(String(e));
    } finally {
      setBusy("");
    }
  };

  const counts = list.page?.counts;
  const blurb = views.find((v) => v[0] === view)?.[2];
  const reviewable = manage && view !== "confirmed";
  const cols = reviewable ? 9 : 8;
  return (
    <section className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        {views.map(([v, name]) => (
          <Button key={v} variant={view === v ? "primary" : "ghost"} onClick={() => setView(v)}>
            {name}
            {counts && <span className="ml-1 tabular-nums opacity-70">{(counts as Record<string, number>)[v] ?? 0}</span>}
          </Button>
        ))}
      </div>
      <p className="text-sm text-muted-foreground">
        {blurb}
        {counts && counts.checking ? ` ${counts.checking} new pages are still being read.` : ""}
        {list.page ? ` ${list.page.sites_total} sites, ${list.page.pages_total ?? 0} pages.` : ""}
      </p>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <input className={`${field} w-56`} placeholder="Search pages" value={q} onChange={(e) => setQ(e.target.value)} />
        <select className={`${field} !w-auto`} aria-label="Found by" value={source} onChange={(e) => setSource(e.target.value)}>
          <option value="">Every source</option>
          {Object.entries(sources).map(([k, v]) => (
            <option key={k} value={k}>
              {v}
            </option>
          ))}
        </select>
        <select className={`${field} !w-auto`} aria-label="Domain rating" value={minDR} onChange={(e) => setMinDR(e.target.value)}>
          <option value="">Any domain rating</option>
          {["20", "40", "60", "80"].map((n) => (
            <option key={n} value={n}>
              DR {n}+
            </option>
          ))}
        </select>
        <Toggle on={noRivals} onChange={setNoRivals} hint="Hide pages on your tracked competitors' own sites.">
          Hide competitors' sites
        </Toggle>
      </div>
      <ErrorText>{list.error || actError}</ErrorText>
      {list.page && list.rows.length === 0 && !list.loading ? (
        <Empty title={view === "about" ? "Nothing new to review" : "Nothing here"}>
          {view === "about" ? "New pages come in every day from your links, news, social, web searches and AI answers." : null}
        </Empty>
      ) : (
        <div className="overflow-x-auto rounded-xl border">
          <table className="w-full text-sm">
            <thead className="bg-muted/30">
              <tr>
                {reviewable && <Th by={sort} onSort={setSort}>{""}</Th>}
                <Th sort="best" by={sort} onSort={setSort} title="Sites that link to you first">
                  Site
                </Th>
                <Th sort="pages" by={sort} onSort={setSort} className="text-right">
                  Pages
                </Th>
                <Th by={sort} onSort={setSort}>
                  Found by
                </Th>
                <Th sort="domain_rating" by={sort} onSort={setSort} className="text-right" title="The site's strength, 0 to 100, from competitor scans">
                  DR
                </Th>
                <Th sort="traffic" by={sort} onSort={setSort} className="text-right" title="The site's monthly search traffic">
                  Traffic
                </Th>
                <Th sort="ai_citations" by={sort} onSort={setSort} className="text-right" title="How often AI answers cited the site in 90 days">
                  AI cites
                </Th>
                <Th sort="competitors" by={sort} onSort={setSort} className="text-right" title="How many of your competitors are on the site">
                  Competitors
                </Th>
                <Th sort="latest" by={sort} onSort={setSort} className="text-right">
                  Latest
                </Th>
              </tr>
            </thead>
            <tbody>
              {list.rows.flatMap((s: Site) => {
                const [vl, tone] = verdicts[s.best] ?? [s.best, "grey" as Tone];
                const shown = !!open[s.host];
                const target = s.pages === 1 ? { ids: [s.items[0].id] } : { host: s.host };
                const head = (
                  <tr key={s.host} className="border-t align-top">
                    {reviewable && (
                      <td className="px-3 py-2">
                        <div className="flex gap-1">
                          <Button disabled={!!busy} onClick={() => review(s.host, "about_us", target)} title={s.pages > 1 ? `All ${s.pages} pages on ${s.host}` : undefined}>
                            About us
                          </Button>
                          {view !== "not" && (
                            <Button variant="ghost" disabled={!!busy} onClick={() => review(s.host, "not_us", target)} title={s.pages > 1 ? `All ${s.pages} pages on ${s.host}` : undefined}>
                              Not us
                            </Button>
                          )}
                        </div>
                      </td>
                    )}
                    <td className="max-w-sm px-3 py-2">
                      <button className="block max-w-full text-left" onClick={() => setOpen((o) => ({ ...o, [s.host]: !o[s.host] }))} aria-expanded={shown}>
                        <span className="font-medium">{s.host}</span>
                        <span className="block truncate text-xs text-muted-foreground">{s.pages === 1 ? s.items[0].title || s.items[0].url : shown ? "Hide pages" : `Show ${s.pages} pages`}</span>
                      </button>
                      <div className="mt-1 flex flex-wrap gap-1">
                        <Badge tone={tone}>{vl}</Badge>
                        {s.competitor_site && <Badge tone="amber">Competitor's site</Badge>}
                      </div>
                    </td>
                    <td className="px-3 py-2 text-right tabular-nums">{s.pages}</td>
                    <td className="px-3 py-2 text-xs text-muted-foreground">{(s.sources ?? []).map((x) => sources[x] ?? label(x)).join(", ")}</td>
                    <td className="px-3 py-2 text-right tabular-nums">{num(s.domain_rating)}</td>
                    <td className="px-3 py-2 text-right tabular-nums">{num(s.domain_traffic)}</td>
                    <td className="px-3 py-2 text-right tabular-nums">{num(s.ai_citations ?? 0)}</td>
                    <td className="px-3 py-2 text-right tabular-nums">{num(s.competitors ?? 0)}</td>
                    <td className="whitespace-nowrap px-3 py-2 text-right text-xs text-muted-foreground">{s.latest ? new Date(s.latest).toLocaleDateString() : "–"}</td>
                  </tr>
                );
                if (!shown) return [head];
                const pages = s.items.map((p) => {
                  const [pl, pt] = verdicts[p.verdict] ?? [p.verdict, "grey" as Tone];
                  return (
                    <tr key={p.id} className="bg-muted/20 text-xs">
                      {reviewable && (
                        <td className="px-3 py-1.5">
                          {s.pages > 1 && (
                            <span className="flex gap-1">
                              <Button variant="ghost" disabled={!!busy} onClick={() => review(p.id, "about_us", { ids: [p.id] })}>
                                Yes
                              </Button>
                              {view !== "not" && (
                                <Button variant="ghost" disabled={!!busy} onClick={() => review(p.id, "not_us", { ids: [p.id] })}>
                                  No
                                </Button>
                              )}
                            </span>
                          )}
                        </td>
                      )}
                      <td className="max-w-sm px-3 py-1.5 pl-6" colSpan={3}>
                        <a className="block truncate underline-offset-2 hover:underline" href={p.url} target="_blank" rel="noreferrer noopener nofollow" title={p.url}>
                          {p.title || p.url}
                        </a>
                        <span className="text-muted-foreground">{[sources[p.source] ?? label(p.source), p.kind && label(p.kind)].filter(Boolean).join(" · ")}</span>
                      </td>
                      <td className="px-3 py-1.5" colSpan={4}>
                        <Badge tone={pt}>{pl}</Badge>
                      </td>
                      <td className="whitespace-nowrap px-3 py-1.5 text-right text-muted-foreground">{new Date(p.found_at).toLocaleDateString()}</td>
                    </tr>
                  );
                });
                const rest =
                  s.pages > s.items.length
                    ? [
                        <tr key={s.host + ":more"} className="bg-muted/20 text-xs text-muted-foreground">
                          <td className="px-3 py-1.5 pl-6" colSpan={cols}>
                            and {s.pages - s.items.length} more on this site
                          </td>
                        </tr>,
                      ]
                    : [];
                return [head, ...pages, ...rest];
              })}
            </tbody>
          </table>
          <MoreOnScroll more={list.more} hasMore={list.hasMore} loading={list.loading} />
        </div>
      )}
    </section>
  );
}
