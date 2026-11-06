// Found: every page discovery found that links to or names the brand,
// grouped by site. Each page was read once to see whether it's about the
// brand, so the list opens on the ones that are, and "About us" or "Not
// us" settles a page, or a whole site, in one click. "About us" makes it a
// placement under the site's publisher with no CRM setup needed.
import { useEffect, useState } from "react";
import type { paths } from "@outflare/sdk";
import { api, errorMessage } from "../../api";
import { Badge, Button, Empty, ErrorText, field, toast, type tones } from "../../ui";
import { label } from "./client";

type Found = paths["/v1/brands/{brand}/coverage/found"]["get"]["responses"]["200"]["content"]["application/json"];
type Site = Found["data"][number];
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
    [data, setData] = useState<Found | null>(null),
    [offset, setOffset] = useState(0),
    [open, setOpen] = useState<Record<string, boolean>>({}),
    [busy, setBusy] = useState(""),
    [error, setError] = useState(""),
    [reload, setReload] = useState(0);
  useEffect(() => {
    let active = true;
    const t = setTimeout(() => {
      api
        .GET("/v1/brands/{brand}/coverage/found", { params: { path: { brand }, query: { view, q: q || undefined, cursor: offset ? String(offset) : undefined, limit: 50 } } })
        .then(({ data, error }) => {
          if (!active) return;
          if (error) setError(errorMessage(error));
          else {
            setError("");
            setData(data ?? null);
          }
        })
        .catch((e) => active && setError(String(e)));
    }, 200);
    return () => {
      active = false;
      clearTimeout(t);
    };
  }, [brand, view, q, offset, reload]);

  const review = async (key: string, decision: "about_us" | "not_us", body: { ids?: string[]; host?: string }) => {
    setBusy(key);
    try {
      const { data, error } = await api.POST("/v1/brands/{brand}/coverage/found/review", { params: { path: { brand } }, body: { decision, ...body } });
      if (error) throw new Error(errorMessage(error));
      const n = data && "reviewed" in data ? data.reviewed : 0;
      toast(decision === "about_us" ? `${n} added to your placements` : `${n} marked not about you`);
      setReload((r) => r + 1);
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy("");
    }
  };

  const counts = data?.counts;
  const blurb = views.find((v) => v[0] === view)?.[2];
  return (
    <section className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        {views.map(([v, name]) => (
          <Button
            key={v}
            variant={view === v ? "primary" : "ghost"}
            onClick={() => {
              setView(v);
              setOffset(0);
            }}
          >
            {name}
            {counts && <span className="ml-1 tabular-nums opacity-70">{(counts as Record<string, number>)[v] ?? 0}</span>}
          </Button>
        ))}
        <input className={`${field} ml-auto w-64`} placeholder="Search pages" value={q} onChange={(e) => (setQ(e.target.value), setOffset(0))} />
      </div>
      <p className="text-sm text-muted-foreground">
        {blurb}
        {counts && counts.checking ? ` ${counts.checking} new pages are still being read.` : ""}
      </p>
      <ErrorText>{error}</ErrorText>
      {data && data.data.length === 0 && (
        <Empty title={view === "about" ? "Nothing new to review" : "Nothing here"}>
          {view === "about" ? "New pages come in every day from your links, news, social, web searches and AI answers." : null}
        </Empty>
      )}
      <div className="divide-y rounded-xl border">
        {data?.data.map((s: Site) => {
          const [vl, tone] = verdicts[s.best] ?? [s.best, "grey" as Tone];
          const shown = open[s.host] || s.pages === 1;
          const reviewable = manage && view !== "confirmed";
          return (
            <div key={s.host} className="p-3">
              <div className="flex flex-wrap items-center gap-2">
                {reviewable && (
                  <div className="flex shrink-0 gap-1">
                    <Button disabled={!!busy} onClick={() => review(s.host, "about_us", s.pages === 1 ? { ids: [s.items[0].id] } : { host: s.host })} title={s.pages > 1 ? `All ${s.pages} pages on ${s.host}` : undefined}>
                      About us
                    </Button>
                    {view !== "not" && (
                      <Button variant="ghost" disabled={!!busy} onClick={() => review(s.host, "not_us", s.pages === 1 ? { ids: [s.items[0].id] } : { host: s.host })}>
                        Not us
                      </Button>
                    )}
                  </div>
                )}
                <button className="min-w-0 flex-1 text-left" onClick={() => setOpen((o) => ({ ...o, [s.host]: !o[s.host] }))}>
                  <span className="font-medium">{s.host}</span>
                  <span className="ml-2 text-sm text-muted-foreground">
                    {s.pages === 1 ? s.items[0].title : `${s.pages} pages${shown ? "" : " · show"}`}
                  </span>
                </button>
                <Badge tone={tone}>{vl}</Badge>
              </div>
              {shown && (
                <ul className="mt-2 space-y-1.5 pl-1 text-sm">
                  {s.items.map((p) => {
                    const [pl, pt] = verdicts[p.verdict] ?? [p.verdict, "grey" as Tone];
                    return (
                      <li key={p.id} className="flex flex-wrap items-center gap-2">
                        {reviewable && s.pages > 1 && (
                          <span className="flex shrink-0 gap-1">
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
                        <a className="min-w-0 flex-1 truncate underline-offset-2 hover:underline" href={p.url} target="_blank" rel="noreferrer noopener nofollow" title={p.url}>
                          {p.title || p.url}
                        </a>
                        <span className="text-xs text-muted-foreground">
                          {[sources[p.source] ?? label(p.source), p.kind && label(p.kind), new Date(p.found_at).toLocaleDateString()].filter(Boolean).join(" · ")}
                        </span>
                        {s.pages > 1 && <Badge tone={pt}>{pl}</Badge>}
                      </li>
                    );
                  })}
                  {s.pages > s.items.length && <li className="text-xs text-muted-foreground">and {s.pages - s.items.length} more on this site</li>}
                </ul>
              )}
            </div>
          );
        })}
      </div>
      {data && data.sites_total > 50 && (
        <div className="flex items-center gap-2 text-sm">
          <Button variant="ghost" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - 50))}>
            Previous
          </Button>
          <span className="text-muted-foreground">
            Sites {offset + 1}–{Math.min(offset + 50, data.sites_total)} of {data.sites_total}
          </span>
          <Button variant="ghost" disabled={offset + 50 >= data.sites_total} onClick={() => setOffset(offset + 50)}>
            Next
          </Button>
        </div>
      )}
    </section>
  );
}
