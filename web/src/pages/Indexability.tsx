import { useCallback, useEffect, useState } from "react";
import type { IndexabilityIssue, IndexabilityPage, IndexabilitySummary } from "@outflare/sdk";
import { DataTable, type ColumnDef } from "@/components/data-table";
import { ConnectFor } from "@/components/connect-for";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { track } from "@/lib/analytics";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, ErrorText, field, Loading, Stat, Stats, type tones } from "../ui";
import { ago, plural } from "../format";
import type { Tab } from "../App";
import { severityLabel, severityTone } from "./SiteAudits";

// The indexability report of a site: every page the crawl, its sitemaps,
// Search Console and an SEO tool know, whether each can be indexed, whether
// Google indexed it, the issues where they disagree, and how that changed
// day by day. GET .../seo-sites/{site}/indexability.

type Tone = keyof typeof tones;
type Report = {
  summary: IndexabilitySummary;
  issues: (IndexabilityIssue & { pages: number })[];
  history: (IndexabilitySummary & { day: string })[];
  gaps: { source: string; connector?: string; message: string }[];
  sources: {
    crawl: { run_id: string | null; finished_at: string | null; pages: number; in_sitemap: number };
    search_console: { connected: boolean; newest_day: string | null; inspected: number };
    seo_tool: { connector: string | null; day: string | null; pages: number };
  };
};
type Verdict = IndexabilityPage["verdict"];

const verdicts: { id: Verdict; label: string; tone: Tone; color: string; help: string }[] = [
  { id: "indexed", label: "Indexed", tone: "green", color: "var(--series-3)", help: "Google showed it in results in the last 28 days, or URL Inspection says it's on Google." },
  { id: "likely_indexed", label: "Likely indexed", tone: "blue", color: "var(--series-1)", help: "Your SEO tool sees it ranking, and Search Console says nothing against it." },
  { id: "not_indexed", label: "Not indexed", tone: "red", color: "var(--series-2)", help: "URL Inspection says Google hasn't indexed it, and nothing shows it in results." },
  { id: "unknown", label: "Unknown", tone: "grey", color: "var(--muted-foreground)", help: "No source has looked at it yet." },
];
const verdictOf = Object.fromEntries(verdicts.map((v) => [v.id, v]));

const reasons: Record<string, string> = {
  noindex: "noindex tag",
  canonical: "Canonical points elsewhere",
  redirect: "Redirects",
  error: "Answers with an error",
  blocked: "Blocked by robots.txt",
  failed: "Couldn't be read",
};
const connectorNames: Record<string, string> = { google_search_console: "Google Search Console", ahrefs: "Ahrefs", dataforseo: "DataForSEO" };

const pct = (n: number, of: number) => (of > 0 ? `${Math.round((n / of) * 100)}%` : "–");
const path = (u: string) => {
  try {
    const x = new URL(u);
    return x.pathname + x.search;
  } catch {
    return u;
  }
};

export function Indexability({ ws, site, go }: { ws: string; site: string; go: (tab: Tab, item?: string) => void }) {
  const [report, setReport] = useState<Report | null>(null);
  const [pages, setPages] = useState<IndexabilityPage[] | null>(null);
  const [total, setTotal] = useState(0);
  const [error, setError] = useState("");
  const [verdict, setVerdict] = useState<Verdict | "">("");
  const [issue, setIssue] = useState("");
  const [q, setQ] = useState("");
  const [open, setOpen] = useState<IndexabilityPage | null>(null);
  const p = { path: { ws, site } };

  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/seo-sites/{site}/indexability", { params: p }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setReport(data as Report);
    });
  }, [ws, site]); // eslint-disable-line react-hooks/exhaustive-deps

  const loadPages = useCallback(async () => {
    const query = { verdict: verdict || undefined, issue: (issue || undefined) as never, q: q.trim() || undefined, limit: 1000 };
    const { data, error } = await api.GET("/v1/workspaces/{ws}/seo-sites/{site}/indexability/pages", { params: { ...p, query } });
    if (error || !data) return setError(errorMessage(error));
    setPages(data.data);
    setTotal(data.total);
  }, [ws, site, verdict, issue, q]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    const t = setTimeout(loadPages, q ? 300 : 0);
    return () => clearTimeout(t);
  }, [loadPages, q]);

  function filter(kind: "verdict" | "issue", value: string) {
    track("ui.indexability_filtered", { filter: kind, value: value || "all" });
    if (kind === "verdict") setVerdict(value as Verdict | "");
    else setIssue(value);
  }

  if (error && !report) return <Card title="Indexability"><ErrorText>{error}</ErrorText></Card>;
  if (!report) return <Card title="Indexability"><Loading /></Card>;
  const s = report.summary;
  const src = report.sources;
  const found = report.issues.filter((i) => i.pages > 0);

  const columns: ColumnDef<IndexabilityPage, unknown>[] = [
    {
      id: "url",
      header: "Page",
      accessorFn: (r) => r.url,
      cell: ({ row: { original: r } }) => <span className="font-mono text-[12px] break-all">{path(r.url)}</span>,
    },
    {
      id: "verdict",
      header: "Google",
      accessorFn: (r) => verdicts.findIndex((v) => v.id === r.verdict),
      cell: ({ row: { original: r } }) => <Badge tone={verdictOf[r.verdict].tone}>{verdictOf[r.verdict].label}</Badge>,
    },
    {
      id: "indexable",
      header: "Can be indexed",
      accessorFn: (r) => (r.indexable === null ? 2 : r.indexable ? 0 : 1),
      cell: ({ row: { original: r } }) =>
        r.indexable === null ? <span className="text-muted-foreground">Not crawled</span> : r.indexable ? "Yes" : <span className="text-red-600 dark:text-red-400">{reasons[r.not_indexable_reason] ?? "No"}</span>,
    },
    { id: "sitemap", header: "Sitemap", accessorFn: (r) => r.in_sitemap, cell: ({ row: { original: r } }) => (r.in_sitemap ? "Yes" : <span className="text-muted-foreground">No</span>) },
    { id: "impressions", header: "Impressions", accessorFn: (r) => r.impressions, cell: ({ row: { original: r } }) => <span className="font-mono tabular-nums">{r.impressions.toLocaleString()}</span> },
    { id: "clicks", header: "Clicks", accessorFn: (r) => r.clicks, cell: ({ row: { original: r } }) => <span className="font-mono tabular-nums">{r.clicks.toLocaleString()}</span> },
    { id: "keywords", header: "Keywords", accessorFn: (r) => r.seo_keywords, cell: ({ row: { original: r } }) => <span className="font-mono tabular-nums">{r.seo_keywords.toLocaleString()}</span> },
    {
      id: "issues",
      header: "Issues",
      accessorFn: (r) => r.issues.length,
      cell: ({ row: { original: r } }) => (
        <span className="flex flex-wrap gap-1">
          {r.issues.map((id) => {
            const i = report.issues.find((x) => x.id === id);
            return i ? <Badge key={id} tone={severityTone[i.severity]}>{i.title}</Badge> : null;
          })}
        </span>
      ),
    },
  ];

  return (
    <Card
      title="Indexability"
      description="Which pages Google has indexed, from the crawl and its sitemaps, Search Console and your SEO tool. Where they disagree, results beat an old inspection."
    >
      <div className="space-y-5">
        {report.gaps.length > 0 && (
          <ul className="space-y-2 text-[13px]">
            {report.gaps.map((g) => (
              <li key={g.source} className="flex flex-wrap items-center justify-between gap-2 rounded-md border bg-muted/40 px-3 py-2">
                <span className="text-muted-foreground">{g.message}</span>
                {g.connector && <ConnectFor connector={g.connector} name={connectorNames[g.connector] ?? g.connector} back="audits" item={site} why={g.message} go={go} variant="ghost" />}
              </li>
            ))}
          </ul>
        )}

        <Stats cols={5}>
          <Stat label="Pages" value={s.pages.toLocaleString()} sub={`${s.indexable.toLocaleString()} can be indexed`} />
          {verdicts.map((v) => (
            <Stat key={v.id} label={v.label} value={s[v.id].toLocaleString()} sub={pct(s[v.id], s.pages)}
              tone={v.id === "indexed" ? "green" : v.id === "not_indexed" && s.not_indexed > 0 ? "red" : undefined} />
          ))}
        </Stats>

        <History days={report.history} />

        {found.length > 0 && (
          <div>
            <h3 className="mb-2 text-[13px] font-semibold">What to look at</h3>
            <ul className="divide-y rounded-md border text-[13px]">
              {found.map((i) => (
                <li key={i.id}>
                  <button className={`flex w-full flex-wrap items-start gap-x-3 gap-y-1 px-3 py-2 text-left hover:bg-muted/50 ${issue === i.id ? "bg-muted" : ""}`}
                    onClick={() => filter("issue", issue === i.id ? "" : i.id)}>
                    <Badge tone={severityTone[i.severity]}>{severityLabel[i.severity]}</Badge>
                    <span className="min-w-0 flex-1">
                      <span className="font-medium">{i.title}</span>
                      <span className="block text-muted-foreground">{i.why}</span>
                    </span>
                    <span className="font-mono tabular-nums">{plural(i.pages, "page")}</span>
                  </button>
                </li>
              ))}
            </ul>
          </div>
        )}
        {found.length === 0 && s.pages > 0 && <p className="text-[13px] text-muted-foreground">No indexability issues: every source agrees with what the site asks for.</p>}

        <div>
          <div className="mb-2 flex flex-wrap items-center gap-2">
            {([["", "All"], ...verdicts.map((v) => [v.id, v.label])] as [string, string][]).map(([id, label]) => (
              <Button key={id || "all"} variant={verdict === id ? "primary" : "ghost"} onClick={() => filter("verdict", id)}>{label}</Button>
            ))}
            {issue && <Button variant="ghost" onClick={() => filter("issue", "")}>✕ {report.issues.find((i) => i.id === issue)?.title}</Button>}
            <input className={`${field} ml-auto w-full sm:w-56`} placeholder="Search URLs" value={q} onChange={(e) => setQ(e.target.value)} aria-label="Search URLs" />
          </div>
          {pages === null ? <Loading /> : (
            <>
              <DataTable columns={columns} data={pages} rowId={(r) => r.url} onRowClick={setOpen} hidden={["clicks", "keywords"]}
                empty={<p className="py-6 text-center text-[13px] text-muted-foreground">No pages match.</p>}
                mobile={(r) => (
                  <div className="space-y-1">
                    <div className="font-mono text-[12px] break-all">{path(r.url)}</div>
                    <div className="flex flex-wrap gap-1"><Badge tone={verdictOf[r.verdict].tone}>{verdictOf[r.verdict].label}</Badge>{r.issues.length > 0 && <Badge tone="amber">{plural(r.issues.length, "issue")}</Badge>}</div>
                  </div>
                )} />
              {total > pages.length && <p className="mt-2 text-[12px] text-muted-foreground">Showing the first {pages.length.toLocaleString()} of {total.toLocaleString()}. Filter or search to find the rest.</p>}
            </>
          )}
        </div>

        <p className="text-[12px] text-muted-foreground">
          {src.crawl.finished_at ? `Crawl ${ago(src.crawl.finished_at)}, ${plural(src.crawl.pages, "page")}, ${src.crawl.in_sitemap.toLocaleString()} in sitemaps` : "Not crawled yet"}
          {" · "}
          {src.search_console.connected ? `Search Console up to ${src.search_console.newest_day ?? "–"}, ${plural(src.search_console.inspected, "page")} inspected` : "No Search Console"}
          {" · "}
          {src.seo_tool.connector ? `${connectorNames[src.seo_tool.connector] ?? src.seo_tool.connector} rankings from ${src.seo_tool.day}` : "No SEO tool"}
        </p>
      </div>
      {open && <PageSheet page={open} issues={report.issues} onClose={() => setOpen(null)} />}
    </Card>
  );
}

// One bar a day, split by verdict.
function History({ days }: { days: Report["history"] }) {
  if (days.length < 2) return <p className="text-[12px] text-muted-foreground">History builds up from here: a snapshot is taken after every crawl and Search Console sync.</p>;
  const W = 600, H = 120;
  const top = Math.max(...days.map((d) => d.pages), 1);
  const bw = W / days.length;
  const last = days[days.length - 1], first = days[0];
  return (
    <div>
      <svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" className="h-28 w-full" role="img"
        aria-label={`Indexed pages from ${first.day} to ${last.day}: ${first.indexed} then ${last.indexed}`}>
        {days.map((d, i) => {
          let y = H;
          return (
            <g key={d.day}>
              <title>{`${d.day}: ${verdicts.map((v) => `${d[v.id]} ${v.label.toLowerCase()}`).join(", ")}`}</title>
              {verdicts.map((v) => {
                const h = (d[v.id] / top) * (H - 4);
                y -= h;
                return <rect key={v.id} x={i * bw + bw * 0.15} y={y} width={bw * 0.7} height={h} fill={v.color} opacity={v.id === "unknown" ? 0.35 : 1} />;
              })}
            </g>
          );
        })}
      </svg>
      <div className="mt-1 flex flex-wrap justify-between gap-2 text-[12px] text-muted-foreground">
        <span>{first.day}</span>
        <span className="flex flex-wrap gap-3">
          {verdicts.map((v) => (
            <span key={v.id} className="inline-flex items-center gap-1"><span className="inline-block size-2.5 rounded-sm" style={{ background: v.color, opacity: v.id === "unknown" ? 0.35 : 1 }} />{v.label}</span>
          ))}
        </span>
        <span>{last.day}</span>
      </div>
    </div>
  );
}

// Everything every source says about one page.
function PageSheet({ page, issues, onClose }: { page: IndexabilityPage; issues: Report["issues"]; onClose: () => void }) {
  const v = verdictOf[page.verdict];
  const ins = page.inspection;
  const row = (label: string, value: React.ReactNode) => (
    <div className="flex justify-between gap-4 py-1.5"><dt className="text-muted-foreground">{label}</dt><dd className="text-right">{value}</dd></div>
  );
  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-lg">
        <SheetHeader>
          <SheetTitle className="font-mono text-[13px] break-all">{page.url}</SheetTitle>
          <SheetDescription><Badge tone={v.tone}>{v.label}</Badge> {v.help}</SheetDescription>
        </SheetHeader>
        <div className="space-y-5 px-4 pb-6 text-[13px]">
          {page.issues.map((id) => {
            const i = issues.find((x) => x.id === id);
            if (!i) return null;
            return (
              <div key={id} className="rounded-md border p-3">
                <div className="flex items-center gap-2"><Badge tone={severityTone[i.severity]}>{severityLabel[i.severity]}</Badge><span className="font-medium">{i.title}</span></div>
                <p className="mt-1 text-muted-foreground">{i.why}</p>
                <p className="mt-1"><span className="font-medium">Fix: </span>{i.fix}</p>
              </div>
            );
          })}
          <section>
            <h3 className="font-semibold">The crawl</h3>
            <dl className="divide-y">
              {row("Found", page.crawled ? `Yes, ${plural(page.depth ?? 0, "click")} from the start` : "No")}
              {page.crawled && row("Answered", page.http_status ?? page.crawl_status)}
              {row("Can be indexed", page.indexable === null ? "Not read" : page.indexable ? "Yes" : reasons[page.not_indexable_reason] ?? "No")}
              {row("In a sitemap", page.in_sitemap ? "Yes" : "No")}
              {page.crawled && row("Links to it", page.links_in.toLocaleString())}
            </dl>
          </section>
          <section>
            <h3 className="font-semibold">Search Console</h3>
            <dl className="divide-y">
              {row("Impressions, 28 days", page.impressions.toLocaleString())}
              {row("Clicks, 28 days", page.clicks.toLocaleString())}
              {row("URL Inspection", ins ? `${ins.verdict === "PASS" ? "On Google" : "Not on Google"}${ins.coverage ? `: ${ins.coverage}` : ""}` : "Not inspected yet")}
              {ins && ins.google_canonical && row("Google's canonical", <span className="font-mono text-[12px] break-all">{ins.google_canonical}</span>)}
              {ins && row("Inspected", ago(ins.inspected_at))}
            </dl>
          </section>
          <section>
            <h3 className="font-semibold">SEO tool</h3>
            <dl className="divide-y">
              {row("Keywords it ranks for", page.seo_keywords.toLocaleString())}
              {row("Estimated visits a month", Math.round(page.seo_traffic).toLocaleString())}
            </dl>
          </section>
        </div>
      </SheetContent>
    </Sheet>
  );
}
