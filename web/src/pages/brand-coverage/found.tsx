// Found: every page discovery found that links to or names the brand,
// grouped by site. Each page was read once to see whether it's about the
// brand, so the list opens on the ones that are. Every page is its own row,
// always shown under its site (no hidden rows to expand), with its address
// as a link that opens in a new tab and what the read found in its own
// column. "Yes, track it" makes a page (or every page of a site) a placement
// under the site's publisher with no CRM setup needed; "No, hide it" moves it
// to Not about you. "Spam" marks the whole site as spam, and pages found on
// it later stay hidden too; "Check again in a week" hides a page (one that
// is offline, say) until it is read again. Follow-ups (the work Presence
// creates: a link gone, a deadline passed) are a view of the same page, so
// everything waiting for a person is in one place. More sites load as you
// scroll.
import { useEffect, useState, type ReactNode } from "react";
import { ExternalLink } from "lucide-react";
import type { paths } from "@outflare/sdk";
import { api, errorMessage } from "../../api";
import { Badge, Button, Empty, ErrorText, field, toast, type tones } from "../../ui";
import { MoreOnScroll, Th, Toggle, num, useInfinite, usePref } from "./table";
import { label, list as listResource } from "./client";

type Found = paths["/v1/brands/{brand}/coverage/found"]["get"]["responses"]["200"]["content"]["application/json"];
type Site = Found["data"][number];
type Query = NonNullable<paths["/v1/brands/{brand}/coverage/found"]["get"]["parameters"]["query"]>;
type Sort = NonNullable<Query["sort"]>;
export type FoundView = "about" | "maybe" | "later" | "not" | "confirmed" | "followups";
type View = FoundView;
type Decision = "about_us" | "not_us" | "spam" | "recheck";
type Tone = keyof typeof tones;

const views: [View, string, string][] = [
  ["about", "About you", "Pages that link to you or name your website."],
  ["maybe", "Maybe", "Pages that only use your name, which another product may share, or couldn't be read yet."],
  ["later", "Check again", "Pages you asked to check again. Each is read again on the date shown and comes back to your list with what we found."],
  ["not", "Not about you", "Pages you said aren't about you or are spam, and pages that don't name you at all."],
  ["confirmed", "Confirmed", "Pages you said are about you. They're placements now, checked over time."],
  ["followups", "Follow-ups", "Work Presence created for you: a link that went missing, a page that's gone, a deadline that passed. Open one to see the evidence and close it."],
];
// Why a site's links look like spam, in plain words.
const spamReasons: Record<string, string> = {
  provider_spam: "your SEO tool (Ahrefs or DataForSEO) flags its links as spam",
  weak_domain: "the site is very weak",
  spam_tld: "it's on a domain ending spam networks favour",
  spam_anchor: "the link text uses spam words",
  link_farm: "the page links out to hundreds of sites",
  many_outlinks: "the page has a lot of outgoing links",
  sitewide: "it links to you from 100 pages or more",
};
// A hint only: the person decides, with Spam site.
const spam = (s: Site) => {
  const score = s.spam_score ?? 0;
  if (score < 30) return null;
  const why = (s.spam_reasons ?? []).map((r) => spamReasons[r] ?? r).join("; ");
  return (
    <Badge tone={score >= 60 ? "red" : "amber"} title={`Spam score ${score} of 100${why ? `: ${why}` : ""}. Nothing is hidden; use Spam site if you agree.`}>
      {score >= 60 ? "Likely spam" : "Possible spam"}
    </Badge>
  );
};
const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? "" : "s"}`;
const done: Record<Decision, string> = {
  about_us: "added to your placements",
  not_us: "moved to Not about you",
  spam: "marked as spam",
  recheck: "will be checked again in a week",
};
const verdicts: Record<string, [string, Tone, string]> = {
  links: ["Links to you", "green", "The page has a link to one of your websites."],
  mentions: ["Names your website", "green", "The page names your website's address, without a link."],
  name_only: ["Name only", "amber", "The page uses your brand's name but not your website, so it may be about another product with the same name."],
  absent: ["Doesn't name you", "grey", "We read the page and it doesn't name you."],
  unreachable: ["Couldn't read", "grey", "The page couldn't be read, so we don't know yet."],
  unchecked: ["Checking", "blue", "The page is waiting to be read."],
};
const sorts: [Sort, string][] = [
  ["best", "Links first"],
  ["latest", "Newest"],
  ["domain_rating", "Domain rating"],
  ["traffic", "Traffic"],
  ["ai_citations", "AI cites"],
  ["competitors", "Competitors there"],
  ["pages", "Most pages"],
];
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

export function PresenceFound({
  brand,
  manage,
  view,
  setView,
  followUps,
}: {
  brand: string;
  manage: boolean;
  view: View;
  setView: (v: View) => void;
  followUps: ReactNode;
}) {
  const [open, setOpen] = useState<number>();
  useEffect(() => {
    let live = true;
    listResource(brand, "tasks", { status: "open", limit: 1 })
      .then((r) => live && setOpen(r.total))
      .catch(() => undefined);
    return () => {
      live = false;
    };
  }, [brand]);
  const [q, setQ] = useState(""),
    [source, setSource] = useState(""),
    [sort, setSort] = usePref<Sort>("found.sort", "best"),
    [minDR, setMinDR] = usePref<string>("found.min_dr", ""),
    [noRivals, setNoRivals] = usePref<boolean>("found.no_rivals", false),
    [busy, setBusy] = useState(""),
    [actError, setActError] = useState("");
  const list = useInfinite<Site, Found>(
    (cursor) =>
      api.GET("/v1/brands/{brand}/coverage/found", {
        params: {
          path: { brand },
          query: {
            // Follow-ups aren't finds; the about view still brings the counts.
            view: view === "followups" ? "about" : view,
            sort,
            limit: 50,
            ...(q.trim() ? { q: q.trim() } : {}),
            ...(source ? { source } : {}),
            ...(minDR ? { min_domain_rating: Number(minDR) } : {}),
            ...(noRivals ? { exclude_competitors: true } : {}),
            ...(cursor ? { cursor } : {}),
            ...(view === "followups" ? { limit: 1 } : {}),
          },
        },
      }),
    [brand, view, q, source, sort, minDR, noRivals],
    errorMessage,
  );

  const review = async (key: string, decision: Decision, body: { ids?: string[]; host?: string }) => {
    setBusy(key);
    setActError("");
    try {
      const { data, error } = await api.POST("/v1/brands/{brand}/coverage/found/review", { params: { path: { brand } }, body: { decision, ...body } });
      if (error) throw new Error(errorMessage(error));
      const n = data && "reviewed" in data ? data.reviewed : 0;
      toast(`${plural(n, "page")} ${done[decision]}`);
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
  const cols = reviewable ? 6 : 5;
  // The two answers, worded for the view: in Not about you, only bringing a
  // page back makes sense.
  const yes = (n: number) => (n > 1 ? `Yes, track all ${n}` : "Yes, track it");
  const no = (n: number) => (n > 1 ? `No, hide all ${n}` : "No, hide it");
  const small = "text-xs text-muted-foreground underline-offset-2 hover:text-foreground hover:underline disabled:opacity-50";
  const actions = (key: string, n: number, target: { ids?: string[]; host?: string }, where: string, spam = false) => (
    <div className="flex flex-col items-end gap-1">
      <div className="flex flex-wrap justify-end gap-1">
        <Button disabled={!!busy} onClick={() => review(key, "about_us", target)} title={`Add ${where} to your placements. We keep checking that it still links to or names you.`}>
          {yes(n)}
        </Button>
        {view !== "not" && (
          <Button variant="ghost" disabled={!!busy} onClick={() => review(key, "not_us", target)} title={`Move ${where} to Not about you. You can bring it back from there.`}>
            {no(n)}
          </Button>
        )}
      </div>
      <div className="flex flex-wrap justify-end gap-x-3">
        {!spam && (
          <button type="button" className={small} disabled={!!busy} onClick={() => review(key, "spam", target)} title="Mark the whole site as spam. Its pages leave this list, and pages found on it later stay hidden too. You can bring them back from Not about you.">
            Spam site
          </button>
        )}
        {view !== "later" && view !== "not" && (
          <button type="button" className={small} disabled={!!busy} onClick={() => review(key, "recheck", target)} title={`Hide ${where} for a week, then read it again: useful when a site is offline. It comes back with what we find then.`}>
            Check again in a week
          </button>
        )}
      </div>
    </div>
  );
  const pageLink = (p: Site["items"][number], strong: boolean) => (
    <a className="group block max-w-full" href={p.url} target="_blank" rel="noreferrer noopener nofollow" title={`Open ${p.url} in a new tab`}>
      <span className={`flex items-center gap-1 ${strong ? "font-medium" : ""} text-brand underline-offset-2 group-hover:underline`}>
        <span className="truncate">{p.title || p.url}</span>
        <ExternalLink className="size-3.5 shrink-0" aria-hidden />
      </span>
      {p.title && <span className="block truncate text-xs text-muted-foreground">{p.url}</span>}
    </a>
  );
  const verdict = (v: string) => {
    const [text, tone, why] = verdicts[v] ?? [v, "grey" as Tone, ""];
    return (
      <Badge tone={tone} title={why}>
        {text}
      </Badge>
    );
  };
  // What a page shows, plus why it's where it is: spam, or when it is
  // checked again.
  const state = (p: Site["items"][number]) => (
    <div className="flex flex-wrap gap-1">
      {p.status === "spam" ? (
        <Badge tone="red" title="You marked this site as spam.">
          Spam
        </Badge>
      ) : p.recheck_at && new Date(p.recheck_at) > new Date() ? (
        <Badge tone="blue" title="It's read again on this date and comes back to your list.">
          Checking again {new Date(p.recheck_at).toLocaleDateString()}
        </Badge>
      ) : (
        verdict(p.verdict)
      )}
    </div>
  );
  const site = (s: Site) => (
    <span className="flex flex-wrap items-center gap-1">
      <a className="font-medium underline-offset-2 hover:underline" href={`https://${s.host}`} target="_blank" rel="noreferrer noopener nofollow" title={`Open ${s.host} in a new tab`}>
        {s.host}
      </a>
      {s.competitor_site && <Badge tone="amber">Competitor's site</Badge>}
      {spam(s)}
    </span>
  );
  // The site's numbers in one column, so the answer buttons always fit.
  const numbers = (s: Site) => {
    const parts = [
      s.domain_rating != null && `DR ${num(s.domain_rating)}`,
      s.domain_traffic != null && `${num(s.domain_traffic)} visits/mo`,
      !!s.ai_citations && `${num(s.ai_citations)} AI cites`,
      !!s.competitors && `${num(s.competitors)} competitors`,
    ].filter(Boolean);
    return (
      <td className="px-3 py-2 text-xs text-muted-foreground" title="Domain rating (0 to 100), monthly search traffic, how often AI answers cited the site in 90 days, and how many of your competitors are on it">
        {parts.length ? parts.map((x) => <span key={String(x)} className="block whitespace-nowrap">{x}</span>) : "–"}
      </td>
    );
  };
  const foundBy = (x: string[]) => x.map((v) => sources[v] ?? label(v)).join(", ");
  const count = (v: View) => (v === "followups" ? open : (counts as Record<string, number> | undefined)?.[v]);
  const tabs = (
    <div className="flex flex-wrap items-center gap-2">
      {views.map(([v, name]) => (
        <Button key={v} variant={view === v ? "primary" : "ghost"} onClick={() => setView(v)}>
          {name}
          {count(v) != null && <span className="ml-1 tabular-nums opacity-70">{count(v)}</span>}
        </Button>
      ))}
    </div>
  );
  if (view === "followups")
    return (
      <section className="space-y-3">
        {tabs}
        <p className="text-sm text-muted-foreground">{blurb}</p>
        {followUps}
      </section>
    );
  return (
    <section className="space-y-3">
      {tabs}
      <p className="text-sm text-muted-foreground">
        {blurb}
        {counts && counts.checking ? ` ${counts.checking} new pages are still being read.` : ""}
        {list.page ? ` ${plural(list.page.sites_total, "site")}, ${plural(list.page.pages_total ?? 0, "page")}.` : ""}
      </p>
      {reviewable && (
        <p className="rounded-lg border bg-muted/20 p-3 text-sm">
          <span className="font-medium">Is each page really about you?</span> Open it to check.{" "}
          <span className="font-medium">Yes, track it</span> adds it to your placements, and we keep checking it still links to or names you.
          {view !== "not" && (
            <>
              {" "}
              <span className="font-medium">No, hide it</span> moves it to Not about you, for a product with the same name, say. You can bring it back from there.{" "}
              <span className="font-medium">Spam site</span> hides every page on that site, now and later.
              {view !== "later" && (
                <>
                  {" "}
                  <span className="font-medium">Check again in a week</span> is for a page that's offline: it comes back once we've read it again.
                </>
              )}
            </>
          )}
        </p>
      )}
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
        <select className={`${field} !w-auto`} aria-label="Sort by" value={sort} onChange={(e) => setSort(e.target.value as Sort)}>
          {sorts.map(([k, v]) => (
            <option key={k} value={k}>
              Sort: {v}
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
                <Th sort="best" by={sort} onSort={setSort} title="Sites that link to you first">
                  Page
                </Th>
                <Th by={sort} onSort={setSort} title="What reading the page showed">
                  What we found
                </Th>
                <Th by={sort} onSort={setSort}>
                  Found by
                </Th>
                <Th by={sort} onSort={setSort} title="Domain rating, traffic, AI citations and competitors on the site">
                  Site
                </Th>
                <Th sort="latest" by={sort} onSort={setSort} className="text-right">
                  Found
                </Th>
                {reviewable && (
                  <Th by={sort} onSort={setSort} className="text-right">
                    About you?
                  </Th>
                )}
              </tr>
            </thead>
            <tbody>
              {list.rows.flatMap((s: Site) => {
                // A site with one page is one row: the page, with its site under it.
                if (s.pages === 1 && s.items.length === 1) {
                  const p = s.items[0];
                  return [
                    <tr key={s.host} className="border-t align-top">
                      <td className="max-w-md px-3 py-2">
                        {pageLink(p, true)}
                        <div className="mt-1 text-xs text-muted-foreground">{site(s)}</div>
                      </td>
                      <td className="px-3 py-2">{state(p)}</td>
                      <td className="px-3 py-2 text-xs text-muted-foreground">{foundBy([p.source])}</td>
                      {numbers(s)}
                      <td className="whitespace-nowrap px-3 py-2 text-right text-xs text-muted-foreground">{new Date(p.found_at).toLocaleDateString()}</td>
                      {reviewable && <td className="px-3 py-2">{actions(p.id, 1, { ids: [p.id] }, "this page", p.status === "spam")}</td>}
                    </tr>,
                  ];
                }
                // A site with several pages: a heading row with the site's
                // numbers and one answer for all of them, then every page.
                const head = (
                  <tr key={s.host} className="border-t bg-muted/30 align-top">
                    <td className="max-w-md px-3 py-2">
                      {site(s)}
                      <span className="block text-xs text-muted-foreground">{s.pages} pages on this site</span>
                    </td>
                    <td className="px-3 py-2">{s.items.every((p) => p.status === "spam") ? state(s.items[0]) : verdict(s.best)}</td>
                    <td className="px-3 py-2 text-xs text-muted-foreground">{foundBy(s.sources ?? [])}</td>
                    {numbers(s)}
                    <td className="whitespace-nowrap px-3 py-2 text-right text-xs text-muted-foreground">{s.latest ? new Date(s.latest).toLocaleDateString() : "–"}</td>
                    {reviewable && <td className="px-3 py-2">{actions(s.host, s.pages, { host: s.host }, `all ${s.pages} pages on ${s.host}`, s.items.every((p) => p.status === "spam"))}</td>}
                  </tr>
                );
                const pages = s.items.map((p) => (
                  <tr key={p.id} className="border-t border-dashed align-top">
                    <td className="max-w-md px-3 py-2 pl-7">{pageLink(p, false)}</td>
                    <td className="px-3 py-2">{state(p)}</td>
                    <td className="px-3 py-2 text-xs text-muted-foreground">{[foundBy([p.source]), p.kind && label(p.kind)].filter(Boolean).join(" · ")}</td>
                    <td className="px-3 py-2" />
                    <td className="whitespace-nowrap px-3 py-2 text-right text-xs text-muted-foreground">{new Date(p.found_at).toLocaleDateString()}</td>
                    {reviewable && <td className="px-3 py-2">{actions(p.id, 1, { ids: [p.id] }, "this page", p.status === "spam")}</td>}
                  </tr>
                ));
                const rest =
                  s.pages > s.items.length
                    ? [
                        <tr key={s.host + ":more"} className="text-xs text-muted-foreground">
                          <td className="px-3 py-1.5 pl-7" colSpan={cols}>
                            and {s.pages - s.items.length} older pages on this site. The answer on the {s.host} row covers them too.
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
