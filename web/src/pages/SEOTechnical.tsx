import { Fragment, useEffect, useMemo, useState } from "react";
import type { components } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { PickBrand } from "@/components/pick-brand";
import { api, errorMessage } from "../api";
import { Badge, Empty, ErrorText, Loading } from "../ui";
import { track } from "../lib/analytics";

// Technical SEO at a glance: what the latest crawl of each of the brand's
// sites found, by rule, in unique pages and templates, new and fixed
// against the crawl before, weighted by the clicks of the pages it
// touches; then robots.txt, indexing, sitemaps and Core Web Vitals.

type Data = components["schemas"]["SEOTechnical"];
type Rule = Data["rules"][number];
type Severity = "all" | Rule["severity"];

const n = (v: number | null | undefined) => (v === null || v === undefined ? "–" : Math.round(v).toLocaleString("en-US"));
const when = (d: string | null | undefined) => (d ? new Date(d).toLocaleDateString("en-US", { day: "numeric", month: "short", year: "numeric" }) : "–");
const sevTone: Record<Rule["severity"], "red" | "amber" | "grey"> = { error: "red", warning: "amber", notice: "grey" };

export function SEOTechnical({ ws }: { ws: string }) {
  const { brand, active } = useBrand(ws);
  const [data, setData] = useState<Data | null>(null);
  const [error, setError] = useState("");
  const [sev, setSev] = useState<Severity>("all");
  const [notices, setNotices] = useState(false);
  const [open, setOpen] = useState<string | null>(null);

  useEffect(() => {
    if (!brand) return;
    let live = true;
    setData(null);
    setError("");
    api
      .GET("/v1/brands/{brand}/seo/technical", { params: { path: { brand: brand.id } } })
      .then(({ data, error }) => {
        if (!live) return;
        if (error || !data) return setError(errorMessage(error));
        setData(data);
      })
      .catch((err: unknown) => {
        if (live && !(err instanceof Error && err.name === "AbortError")) setError(errorMessage(undefined));
      });
    return () => {
      live = false;
    };
  }, [brand?.id]);

  const rules = useMemo(
    () =>
      data
        ? data.rules.filter((r) => (sev === "all" ? notices || r.severity !== "notice" : r.severity === sev) && (r.pages > 0 || r.fixed_pages > 0))
        : [],
    [data, sev, notices],
  );

  if (!brand) return <PickBrand what="Technical SEO" brands={active.length} />;
  if (error) return <ErrorText>{error}</ErrorText>;
  if (!data) return <Loading />;
  if (!data.sites.some((s) => s.crawl_id))
    return <Empty title="No finished crawl yet">Add the brand's site in Site audits and run a crawl; its findings show up here.</Empty>;

  const s = data.summary;
  const tiles: [Severity | null, string, string, string?][] = [
    ["error", "Pages with errors", n(s.errors)],
    ["warning", "Pages with warnings", n(s.warnings)],
    [null, "New since last crawl", n(s.new_pages), s.new_pages ? "text-red-700 dark:text-red-400" : ""],
    [null, "Fixed since last crawl", n(s.fixed_pages), s.fixed_pages ? "text-green-700 dark:text-green-400" : ""],
    [null, "Slow pages", n(s.poor_vitals)],
    [null, "Sitemap errors", n(s.sitemap_errors)],
  ];

  return (
    <div className="space-y-5">
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-6">
        {tiles.map(([id, label, v, tone]) =>
          id ? (
            <button
              type="button"
              key={label}
              aria-pressed={sev === id}
              onClick={() => {
                const next = sev === id ? "all" : id;
                setSev(next);
                track("ui.seo_technical_filtered", { severity: next });
              }}
              className={"rounded-lg border bg-card px-3 py-2 text-left " + (sev === id ? "ring-2 ring-foreground/60" : "hover:shadow-sm")}
            >
              <div className="text-[11px] text-muted-foreground">{label}</div>
              <div className="text-[18px] font-semibold tabular-nums">{v}</div>
            </button>
          ) : (
            <div key={label} className="rounded-lg border bg-card px-3 py-2">
              <div className="text-[11px] text-muted-foreground">{label}</div>
              <div className={"text-[18px] font-semibold tabular-nums " + (tone ?? "")}>{v}</div>
            </div>
          ),
        )}
      </div>

      <section className="rounded-lg border bg-card text-[13px]">
        <table className="w-full">
          <thead>
            <tr className="border-b text-left text-[12px] text-muted-foreground">
              <th className="px-4 py-2 font-medium">Site</th>
              <th className="px-3 py-2 font-medium">Latest crawl</th>
              <th className="px-3 py-2 text-right font-medium">Pages</th>
              <th className="px-3 py-2 font-medium">Compared with</th>
              <th className="px-3 py-2 text-right font-medium">Indexed</th>
              <th className="px-3 py-2 text-right font-medium">Not indexed</th>
              <th className="px-3 py-2 font-medium">robots.txt</th>
            </tr>
          </thead>
          <tbody>
            {data.sites.map((x) => (
              <tr key={x.site_id} className="border-b last:border-0">
                <td className="px-4 py-2 font-mono text-[12px]">{x.host}</td>
                <td className="px-3 py-2">{when(x.crawled_at)}</td>
                <td className="px-3 py-2 text-right tabular-nums">{n(x.pages)}</td>
                <td className="px-3 py-2 text-muted-foreground">{when(x.previous_crawled_at)}</td>
                <td className="px-3 py-2 text-right tabular-nums">
                  {n(x.indexed)}
                  {x.indexed !== null && x.previous_indexed !== null && x.indexed !== x.previous_indexed && (
                    <span className={"ml-1 text-[11px] " + (x.indexed < x.previous_indexed ? "text-red-700 dark:text-red-400" : "text-green-700 dark:text-green-400")}>
                      {x.indexed > x.previous_indexed ? "+" : ""}
                      {n(x.indexed - x.previous_indexed)}
                    </span>
                  )}
                </td>
                <td className="px-3 py-2 text-right tabular-nums">{n(x.not_indexed)}</td>
                <td className="px-3 py-2">{x.robots_changed.length ? <Badge tone="red">Changed</Badge> : <span className="text-muted-foreground">Same</span>}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </section>

      <section className="overflow-x-auto rounded-lg border bg-card">
        <div className="flex items-center justify-between border-b px-4 py-2 text-[13px]">
          <span className="font-medium">Issues by rule, most clicks at stake first</span>
          <label className="flex items-center gap-2 text-[12px] text-muted-foreground">
            <input type="checkbox" checked={notices} onChange={(e) => setNotices(e.target.checked)} />
            Show notices
          </label>
        </div>
        {rules.length ? (
          <table className="w-full text-[13px]">
            <thead>
              <tr className="border-b text-left text-[12px] text-muted-foreground">
                <th className="px-4 py-2 font-medium">Issue</th>
                <th className="px-3 py-2 font-medium">Severity</th>
                <th className="px-3 py-2 text-right font-medium">Pages</th>
                <th className="px-3 py-2 text-right font-medium">Templates</th>
                <th className="px-3 py-2 text-right font-medium">New</th>
                <th className="px-3 py-2 text-right font-medium">Fixed</th>
                <th className="px-3 py-2 text-right font-medium">Clicks at stake</th>
              </tr>
            </thead>
            <tbody>
              {rules.map((r) => {
                const isOpen = open === r.rule;
                return (
                  <Fragment key={r.rule}>
                    <tr className="cursor-pointer border-b hover:bg-muted/40" onClick={() => setOpen(isOpen ? null : r.rule)}>
                      <td className="px-4 py-2">
                        {r.title}
                        <span className="ml-2 text-[11px] text-muted-foreground">{r.category}</span>
                      </td>
                      <td className="px-3 py-2">
                        <Badge tone={sevTone[r.severity]}>{r.severity}</Badge>
                      </td>
                      <td className="px-3 py-2 text-right tabular-nums">
                        {n(r.pages)} <span className="text-[11px] text-muted-foreground">from {n(r.previous_pages)}</span>
                      </td>
                      <td className="px-3 py-2 text-right tabular-nums">{n(r.templates)}</td>
                      <td className={"px-3 py-2 text-right tabular-nums " + (r.new_pages ? "text-red-700 dark:text-red-400" : "text-muted-foreground")}>{n(r.new_pages)}</td>
                      <td className={"px-3 py-2 text-right tabular-nums " + (r.fixed_pages ? "text-green-700 dark:text-green-400" : "text-muted-foreground")}>{n(r.fixed_pages)}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{n(r.clicks)}</td>
                    </tr>
                    {isOpen && (
                      <tr className="border-b bg-muted/20">
                        <td colSpan={7} className="px-4 py-3 text-[12px]">
                          {r.top_pages.length ? (
                            <ul className="space-y-1">
                              {r.top_pages.map((p) => (
                                <li key={p.url} className="flex justify-between gap-4">
                                  <span className="truncate font-mono">{p.url}</span>
                                  <span className="tabular-nums">{n(p.clicks)} clicks</span>
                                </li>
                              ))}
                            </ul>
                          ) : (
                            <span className="text-muted-foreground">None of its pages had search clicks in the last 28 days.</span>
                          )}
                          <div className="mt-2 text-muted-foreground">Every page with it is in Site audits.</div>
                        </td>
                      </tr>
                    )}
                  </Fragment>
                );
              })}
            </tbody>
          </table>
        ) : (
          <div className="px-4 py-6 text-[13px] text-muted-foreground">Nothing found here in the latest crawl.</div>
        )}
      </section>

      {data.vitals.length > 0 && (
        <section className="overflow-x-auto rounded-lg border bg-card">
          <div className="border-b px-4 py-2 text-[13px] font-medium">Core Web Vitals from real visits, 28 days, 75th percentile</div>
          <table className="w-full text-[13px]">
            <thead>
              <tr className="border-b text-left text-[12px] text-muted-foreground">
                <th className="px-4 py-2 font-medium">Page</th>
                <th className="px-3 py-2 font-medium">Device</th>
                <th className="px-3 py-2 text-right font-medium">Visits</th>
                <th className="px-3 py-2 text-right font-medium">LCP</th>
                <th className="px-3 py-2 text-right font-medium">INP</th>
                <th className="px-3 py-2 text-right font-medium">CLS</th>
              </tr>
            </thead>
            <tbody>
              {data.vitals.map((v) => {
                const bad = (m: string) => (v.poor.includes(m as "lcp") ? "text-red-700 dark:text-red-400 font-medium" : "");
                return (
                  <tr key={v.path + v.device} className="border-b last:border-0">
                    <td className="max-w-[360px] truncate px-4 py-2 font-mono text-[12px]">{v.path}</td>
                    <td className="px-3 py-2">{v.device || "–"}</td>
                    <td className="px-3 py-2 text-right tabular-nums">{n(v.visits)}</td>
                    <td className={"px-3 py-2 text-right tabular-nums " + bad("lcp")}>{v.lcp_ms === null ? "–" : `${(v.lcp_ms / 1000).toFixed(1)} s`}</td>
                    <td className={"px-3 py-2 text-right tabular-nums " + bad("inp")}>{v.inp_ms === null ? "–" : `${Math.round(v.inp_ms)} ms`}</td>
                    <td className={"px-3 py-2 text-right tabular-nums " + bad("cls")}>{v.cls === null ? "–" : v.cls.toFixed(2)}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </section>
      )}

      {data.sitemaps.length > 0 && (
        <section className="overflow-x-auto rounded-lg border bg-card">
          <div className="border-b px-4 py-2 text-[13px] font-medium">Sitemaps in Search Console</div>
          <table className="w-full text-[13px]">
            <thead>
              <tr className="border-b text-left text-[12px] text-muted-foreground">
                <th className="px-4 py-2 font-medium">Sitemap</th>
                <th className="px-3 py-2 text-right font-medium">Submitted</th>
                <th className="px-3 py-2 text-right font-medium">Indexed</th>
                <th className="px-3 py-2 text-right font-medium">Errors</th>
                <th className="px-3 py-2 text-right font-medium">Warnings</th>
                <th className="px-3 py-2 font-medium">Last read</th>
              </tr>
            </thead>
            <tbody>
              {data.sitemaps.map((m) => (
                <tr key={m.host + m.path} className="border-b last:border-0">
                  <td className="max-w-[360px] truncate px-4 py-2 font-mono text-[12px]">{m.path}</td>
                  <td className="px-3 py-2 text-right tabular-nums">{n(m.submitted)}</td>
                  <td className="px-3 py-2 text-right tabular-nums">{n(m.indexed)}</td>
                  <td className={"px-3 py-2 text-right tabular-nums " + (m.errors ? "text-red-700 dark:text-red-400" : "")}>{n(m.errors)}</td>
                  <td className="px-3 py-2 text-right tabular-nums">{n(m.warnings)}</td>
                  <td className="px-3 py-2">{when(m.last_read)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </section>
      )}

      <ul className="list-disc space-y-1 pl-5 text-[12px] text-muted-foreground">
        {data.notes.map((t) => (
          <li key={t}>{t}</li>
        ))}
      </ul>
    </div>
  );
}
