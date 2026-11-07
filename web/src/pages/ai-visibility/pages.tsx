import { Fragment, useEffect, useState } from "react";
import type { paths } from "@outflare/sdk";
import { api, errorMessage } from "../../api";
import { Badge, Button, ErrorText, Loading, Stat, Stats, toast } from "../../ui";
import { Frame, Section, th, type AIData, type Go } from "./shared";

type Pages = paths["/v1/brands/{brand}/ai-visibility/pages"]["get"]["responses"]["200"]["content"]["application/json"];
type Review = Pages["reviews"][number];

// Your pages: whether AI answers can read, quote and trust the brand's own
// pages, from the latest site audit, with the pages the answers already
// cite first.
export function AIPages({ ws, go }: { ws: string; go: Go }) {
  return (
    <Frame ws={ws} title="Your pages" description="Whether AI assistants can read, quote and trust your own pages, with the pages they already cite first. From your latest site audit, so nothing extra is fetched.">
      {(d) => <PagesBody d={d} go={go} />}
    </Frame>
  );
}

function PagesBody({ d, go }: { d: AIData; go: Go }) {
  const [data, setData] = useState<Pages | null>(null);
  const [error, setError] = useState("");
  const [open, setOpen] = useState("");
  const [shown, setShown] = useState("");
  const [n, setN] = useState(0);
  const busy = !!data?.reviews.some((r) => r.status === "queued" || r.status === "reading");
  useEffect(() => {
    api.GET("/v1/brands/{brand}/ai-visibility/pages", { params: { path: { brand: d.brand.id }, query: { days: d.days } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setData(data);
    });
  }, [d.brand.id, d.days, n]);
  // Follow checks until they finish.
  useEffect(() => {
    if (!busy) return;
    const t = setTimeout(() => setN((x) => x + 1), 3000);
    return () => clearTimeout(t);
  }, [busy, data]);
  async function check(urls: string[]) {
    const { data: res, error } = await api.POST("/v1/brands/{brand}/ai-visibility/pages/review", { params: { path: { brand: d.brand.id } }, body: { urls } });
    if (error || !res) return toast(errorMessage(error), "error");
    if (!("pages" in res)) return toast("Held for approval by the mission's owner.");
    toast(urls.length === 1 ? "Checking the page with your AI model" : `Checking ${urls.length} pages with your AI model`);
    setN((x) => x + 1);
  }
  if (error) return <ErrorText>{error}</ErrorText>;
  if (!data) return <Loading />;
  if (data.sites.length === 0) {
    return (
      <div className="rounded-lg border border-dashed px-4 py-8 text-center text-[13px]">
        <p className="font-medium">No site audit yet for {d.brand.name}</p>
        <p className="mt-1 text-muted-foreground">Add your website and run a site audit. The checks for AI answers run with it.</p>
        <div className="mt-4"><Button onClick={() => go("audits")}>Open site audits</Button></div>
      </div>
    );
  }
  const title = Object.fromEntries(data.rules.map((r) => [r.id, r.title]));
  const review: Record<string, Review> = Object.fromEntries(data.reviews.map((r) => [r.url, r]));
  const top = data.pages.slice(0, 10).map((p) => p.url);
  // Average AI check score of the brand's pages against competitors' pages.
  const theirURLs = new Set(data.competitor_pages.map((p) => p.url));
  const avg = (l: Review[]) => (l.length === 0 ? null : Math.round(l.reduce((n, r) => n + r.result!.score, 0) / l.length));
  const ready = data.reviews.filter((r) => r.status === "ready" && r.result);
  const ours = avg(ready.filter((r) => !theirURLs.has(r.url)));
  const theirsAvg = avg(ready.filter((r) => theirURLs.has(r.url)));
  const compare = ours !== null && theirsAvg !== null ? { ours, theirs: theirsAvg } : null;
  const cited = data.pages.filter((p) => p.answers > 0).length + data.cited_not_crawled.length;
  const blocked = data.sites.filter((s) => s.blocked_bots.length > 0);
  return (
    <>
      <Stats cols={4}>
        <Stat label="Pages checked" value={String(data.pages_checked)} sub={data.sites.map((s) => s.host).join(", ")} />
        <Stat label="With a problem" value={String(data.pages_with_issues)} tone={data.pages_with_issues > 0 ? "amber" : "green"} />
        <Stat label="Cited by AI answers" value={String(cited)} sub={`last ${data.days} days`} />
        <Stat label="AI search blocked" value={blocked.length === 0 ? "No" : "Yes"} tone={blocked.length === 0 ? "green" : "red"} sub={blocked.length === 0 ? "robots.txt lets them in" : blocked.flatMap((s) => s.blocked_bots).slice(0, 3).join(", ")} />
      </Stats>

      <Section title="Your sites" note="What each site tells AI crawlers, from its latest audit." action={<Button variant="ghost" onClick={() => go("audits")}>Open site audits</Button>}>
        <table className="w-full">
          <thead><tr><th className={th}>Site</th><th className={th}>Audited</th><th className={th}>llms.txt</th><th className={th}>AI crawlers kept out</th><th className={th}>Says about AI use</th></tr></thead>
          <tbody className="divide-y">
            {data.sites.map((s) => (
              <tr key={s.site_id}>
                <td className="py-1.5 pr-3 font-mono text-[12px]">{s.host}</td>
                <td className="pr-3 text-[12px] text-muted-foreground">{new Date(s.crawled_at).toLocaleDateString()}</td>
                <td className="pr-3">{s.llms_txt ? <Badge tone="green">yes</Badge> : <Badge>none</Badge>}</td>
                <td className="pr-3 text-[12px]">{s.blocked_bots.length === 0 ? <span className="text-muted-foreground">none</span> : <span className="text-red-600 dark:text-red-400">{s.blocked_bots.join(", ")}</span>}</td>
                <td className="font-mono text-[12px] text-muted-foreground">{s.usage_signals.length === 0 ? "–" : s.usage_signals.join(" · ")}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </Section>

      <Section title="What to fix" note="Each problem, with how many pages have it. They are on your fixes list too." action={<Button variant="ghost" onClick={() => go("fixes")}>Open fixes</Button>}>
        {data.rules.length === 0 ? <p className="text-muted-foreground">Nothing to fix: AI answers can read and quote every page checked.</p> : (
          <ul className="divide-y">
            {data.rules.map((r) => (
              <li key={r.id} className="py-2">
                <button className="flex w-full items-center justify-between gap-3 text-left" onClick={() => setOpen(open === r.id ? "" : r.id)} aria-expanded={open === r.id}>
                  <span className="flex items-center gap-2"><Badge tone={r.severity === "error" ? "red" : r.severity === "warning" ? "amber" : "grey"}>{r.severity}</Badge>{r.title}</span>
                  <span className="font-mono text-[12px] text-muted-foreground">{r.pages} {r.pages === 1 ? "page" : "pages"}</span>
                </button>
                {open === r.id && (
                  <div className="mt-2 space-y-1 text-[12px]">
                    <p><span className="font-medium">Why it matters: </span>{r.why}</p>
                    <p><span className="font-medium">How to fix it: </span>{r.fix}</p>
                  </div>
                )}
              </li>
            ))}
          </ul>
        )}
      </Section>

      <Section title="Pages" note="Pages AI answers cite come first: fix those before the rest. Check with AI has your own AI model read a page the way an AI answer does." action={top.length > 0 && <Button variant="ghost" disabled={busy} onClick={() => check(top)}>Check the first {top.length} with AI</Button>}>
        {data.pages.length === 0 ? <p className="text-muted-foreground">No page has a problem, and no answer cites one yet.</p> : (
          <div className="overflow-x-auto">
            <table className="w-full">
              <thead><tr><th className={th}>Page</th><th className={th}>Cited in</th><th className={th}>Problems</th><th className={th}>Details</th><th className={th}>AI check</th></tr></thead>
              <tbody className="divide-y">
                {data.pages.map((p) => {
                  const s = p.signals;
                  const notes = [
                    s.unanswered?.length ? `No direct answer: ${s.unanswered.join(", ")}` : "",
                    s.schema_mismatch?.length ? `Not on the page: ${s.schema_mismatch.join("; ")}` : "",
                    s.article ? [s.author ? `By ${s.author}` : "", s.modified || s.published ? `dated ${(s.modified || s.published)!.slice(0, 10)}` : ""].filter(Boolean).join(", ") : "",
                    s.markdown ? "Has a Markdown copy" : "",
                  ].filter(Boolean);
                  const rv = review[p.url];
                  return (
                    <Fragment key={p.url}>
                    <tr className="align-top">
                      <td className="max-w-sm truncate py-1.5 pr-3"><a className="hover:underline" href={p.url} target="_blank" rel="noreferrer noopener">{p.title || p.url}</a><div className="truncate font-mono text-[11px] text-muted-foreground">{p.url}</div></td>
                      <td className="pr-3 font-mono text-[12px]">{p.answers === 0 ? <span className="text-muted-foreground">–</span> : `${p.answers} ${p.answers === 1 ? "answer" : "answers"}`}</td>
                      <td className="pr-3"><div className="flex flex-wrap gap-1">{p.issues.length === 0 ? <Badge tone="green">none</Badge> : p.issues.map((i) => <Badge key={i} tone="amber">{title[i] ?? i}</Badge>)}</div></td>
                      <td className="max-w-xs pr-3 text-[12px] text-muted-foreground">{notes.length === 0 ? "–" : notes.map((n) => <div key={n} className="truncate" title={n}>{n}</div>)}</td>
                      <td className="whitespace-nowrap text-[12px]">
                        {!rv ? <Button variant="ghost" onClick={() => check([p.url])}>Check with AI</Button>
                          : rv.status === "queued" || rv.status === "reading" ? <span className="text-muted-foreground">Checking…</span>
                          : rv.status === "failed" ? <span title={rv.error}><Badge tone="red">failed</Badge> <button className="underline" onClick={() => check([p.url])}>Try again</button></span>
                          : <button className="hover:underline" onClick={() => setShown(shown === p.url ? "" : p.url)} aria-expanded={shown === p.url}><Badge tone={rv.result!.score >= 80 ? "green" : rv.result!.score >= 50 ? "amber" : "red"}>{rv.result!.score}/100</Badge> {shown === p.url ? "Hide" : "See why"}</button>}
                      </td>
                    </tr>
                    {rv?.result && shown === p.url && (
                      <tr>
                        <td colSpan={5} className="bg-muted/40 px-3 py-2 text-[12px]">
                          <ReviewDetail r={rv} onAgain={() => check([p.url])} />
                        </td>
                      </tr>
                    )}
                    </Fragment>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </Section>

      <Section title="Competitors' pages the answers cite" note={<>Their pages AI answers quote for your questions. Check a few with AI to see what they do that yours don't.{compare && <> Checked so far: yours average <b>{compare.ours}/100</b>, theirs <b>{compare.theirs}/100</b>.</>}</>}>
        {data.competitor_pages.length === 0 ? <p className="text-muted-foreground">No competitor's page cited in this period.</p> : (
          <div className="overflow-x-auto">
            <table className="w-full">
              <thead><tr><th className={th}>Page</th><th className={th}>Competitor</th><th className={th}>Cited in</th><th className={th}>Named you too</th><th className={th}>AI check</th></tr></thead>
              <tbody className="divide-y">
                {data.competitor_pages.map((p) => {
                  const rv = review[p.url];
                  return (
                    <Fragment key={p.url}>
                      <tr>
                        <td className="max-w-sm truncate py-1.5 pr-3"><a className="hover:underline" href={p.url} target="_blank" rel="noreferrer noopener">{p.title || p.url}</a></td>
                        <td className="pr-3 text-[12px]">{p.competitor || "–"}</td>
                        <td className="pr-3 font-mono text-[12px]">{p.answers} {p.answers === 1 ? "answer" : "answers"}</td>
                        <td className="pr-3 font-mono text-[12px]">{p.named_you === 0 ? <Badge tone="red">no</Badge> : `${p.named_you} of ${p.answers}`}</td>
                        <td className="whitespace-nowrap text-[12px]">
                          {!rv ? <Button variant="ghost" onClick={() => check([p.url])}>Check with AI</Button>
                            : rv.status === "queued" || rv.status === "reading" ? <span className="text-muted-foreground">Checking…</span>
                            : rv.status === "failed" ? <span title={rv.error}><Badge tone="red">failed</Badge> <button className="underline" onClick={() => check([p.url])}>Try again</button></span>
                            : <button className="hover:underline" onClick={() => setShown(shown === p.url ? "" : p.url)} aria-expanded={shown === p.url}><Badge tone={rv.result!.score >= 80 ? "green" : rv.result!.score >= 50 ? "amber" : "red"}>{rv.result!.score}/100</Badge> {shown === p.url ? "Hide" : "See why"}</button>}
                        </td>
                      </tr>
                      {rv?.result && shown === p.url && (
                        <tr><td colSpan={5} className="bg-muted/40 px-3 py-2 text-[12px]"><ReviewDetail r={rv} onAgain={() => check([p.url])} /></td></tr>
                      )}
                    </Fragment>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </Section>

      {data.cited_not_crawled.length > 0 && (
        <Section title="Cited pages the audit didn't read" note="AI answers cite these, but the latest audit didn't reach them. Raise the audit's page limit or link to them, so they get checked too.">
          <ul className="divide-y">
            {data.cited_not_crawled.map((p) => (
              <li key={p.url} className="flex items-center justify-between gap-3 py-1.5">
                <a className="truncate hover:underline" href={p.url} target="_blank" rel="noreferrer noopener">{p.title || p.url}</a>
                <span className="font-mono text-[12px] text-muted-foreground">{p.answers} {p.answers === 1 ? "answer" : "answers"}</span>
              </li>
            ))}
          </ul>
        </Section>
      )}
    </>
  );
}

function ReviewDetail({ r, onAgain }: { r: Review; onAgain: () => void }) {
  const res = r.result!;
  return (
    <div className="space-y-2">
      <p>{res.summary} {res.answers_up_front ? <Badge tone="green">answers up front</Badge> : <Badge tone="amber">no answer up front</Badge>}</p>
      {res.changes.length > 0 && (
        <div>
          <p className="font-medium">What to change</p>
          <ol className="ml-4 list-decimal">{res.changes.map((c, i) => <li key={i}>{c.problem && <span className="text-muted-foreground">{c.problem} </span>}{c.fix}</li>)}</ol>
        </div>
      )}
      {res.unclear.length > 0 && <p><span className="font-medium">Unclear or unsupported: </span>{res.unclear.join("; ")}</p>}
      {res.questions.length > 0 && <p><span className="font-medium">Answers well: </span>{res.questions.join(" · ")}</p>}
      <p className="text-muted-foreground">Checked {r.reviewed_at ? new Date(r.reviewed_at).toLocaleString() : ""}{r.model ? ` with ${r.model}` : ""}. <button className="underline" onClick={onAgain}>Check again</button></p>
    </div>
  );
}
