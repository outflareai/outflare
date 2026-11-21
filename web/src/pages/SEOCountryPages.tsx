import { useEffect, useState } from "react";
import type { components } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { PickBrand } from "@/components/pick-brand";
import { api, errorMessage } from "../api";
import { Badge, Empty, ErrorText, Loading } from "../ui";
import { track } from "../lib/analytics";
import { toAlpha2 } from "./SearchPerformance";

// Country pages: pages made for a country, city or network, and how they
// rank in that country. Pick a template (a path such as /proxies/); each
// page shows its target country, Search Console's numbers there against
// the 28 days before, the tracked rank there, and whether its keyword is
// tracked. Templates show median positions, so a template change that
// moves a whole group stands out.

type Data = components["schemas"]["SEOCountryPages"];
type Row = Data["pages"][number];

const n = (v: number) => Math.round(v).toLocaleString("en-US");
const pos = (v: number | null | undefined) => (v === null || v === undefined ? "–" : v.toFixed(1));
const pct = (v: number | null | undefined) => (v === null || v === undefined ? "–" : `${(v * 100).toFixed(1)}%`);
const region = new Intl.DisplayNames(["en"], { type: "region" });
const country = (code: string) => {
  const two = toAlpha2.get(code.toLowerCase());
  return (two && region.of(two)) || code.toUpperCase();
};
const bandTone: Record<Row["band"], "green" | "blue" | "amber" | "red" | "grey"> = { top3: "green", top10: "blue", striking: "amber", beyond: "red", none: "grey" };
const bandText: Record<Row["band"], string> = { top3: "Top 3", top10: "Top 10", striking: "11–20", beyond: "21+", none: "No data" };

function Move({ now, before }: { now: number | null | undefined; before: number | null | undefined }) {
  if (now === null || now === undefined || before === null || before === undefined) return null;
  const d = before - now;
  if (Math.abs(d) < 0.1) return null;
  return <span className={"ml-1 text-[11px] " + (d > 0 ? "text-green-700 dark:text-green-400" : "text-red-700 dark:text-red-400")}>{`${d > 0 ? "▲" : "▼"}${Math.abs(d).toFixed(1)}`}</span>;
}

function prefixFromURL() {
  return new URLSearchParams(location.search).get("prefix") ?? "";
}

export function SEOCountryPages({ ws }: { ws: string }) {
  const { brand, active } = useBrand(ws);
  const [prefix, setPrefix] = useState(prefixFromURL);
  const [draft, setDraft] = useState(prefixFromURL);
  const [data, setData] = useState<Data | null>(null);
  const [error, setError] = useState("");
  const [onlyUntracked, setOnlyUntracked] = useState(false);

  useEffect(() => {
    if (!brand) return;
    let live = true;
    setData(null);
    setError("");
    api
      .GET("/v1/brands/{brand}/seo/country-pages", { params: { path: { brand: brand.id }, query: prefix ? { prefix } : {} } })
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
  }, [brand?.id, prefix]);

  const choose = (p: string) => {
    setDraft(p);
    setPrefix(p);
    const url = new URL(location.href);
    if (p) url.searchParams.set("prefix", p);
    else url.searchParams.delete("prefix");
    history.replaceState(history.state, "", url);
    track("ui.seo_country_pages_template_picked", { picked: p !== "" });
  };

  if (!brand) return <PickBrand what="Country pages" brands={active.length} />;
  if (error) return <ErrorText>{error}</ErrorText>;
  if (!data) return <Loading />;
  if (!data.to) return <Empty title="No Search Console data yet">Connect Google Search Console; country numbers come from its page and country data.</Empty>;

  const rows = data.pages.filter((p) => !onlyUntracked || p.untracked);
  const s = data.summary;

  return (
    <div className="space-y-5">
      <form
        className="flex flex-wrap items-center gap-2 text-[13px]"
        onSubmit={(e) => {
          e.preventDefault();
          choose(draft.trim());
        }}
      >
        <label className="flex items-center gap-1">
          Pages under
          <input value={draft} onChange={(e) => setDraft(e.target.value)} placeholder="/proxies/" aria-label="Path prefix" className="h-8 w-56 rounded-md border bg-background px-2 font-mono text-[12px]" />
        </label>
        {(data.template_choices ?? []).slice(0, 12).map((t) => (
          <button type="button" key={t.template} onClick={() => choose(t.template)} className={"rounded border px-2 py-0.5 font-mono text-[12px] " + (prefix === t.template ? "border-foreground" : "text-muted-foreground")}>
            {t.template} <span className="font-sans">({t.pages})</span>
          </button>
        ))}
      </form>

      <div className="grid grid-cols-2 gap-2 sm:grid-cols-4 lg:grid-cols-7">
        {(
          [
            ["Pages", s.pages],
            ["Top 3", s.top3],
            ["Top 10", s.top10],
            ["11–20", s.striking],
            ["21+", s.beyond],
            ["No data", s.none],
          ] as const
        ).map(([label, v]) => (
          <div key={label} className="rounded-lg border bg-card px-3 py-2">
            <div className="text-[11px] text-muted-foreground">{label}</div>
            <div className="text-[18px] font-semibold tabular-nums">{n(v)}</div>
          </div>
        ))}
        <button type="button" aria-pressed={onlyUntracked} onClick={() => setOnlyUntracked((x) => !x)} className={"rounded-lg border bg-card px-3 py-2 text-left " + (onlyUntracked ? "ring-2 ring-foreground/60" : "")}>
          <div className="text-[11px] text-muted-foreground">Keyword not tracked</div>
          <div className="text-[18px] font-semibold tabular-nums">{n(s.untracked)}</div>
        </button>
      </div>

      {data.templates.length > 0 && (
        <section className="overflow-x-auto rounded-lg border bg-card">
          <table className="w-full text-[13px]">
            <thead>
              <tr className="border-b text-left text-[12px] text-muted-foreground">
                <th className="px-4 py-2 font-medium">Template</th>
                <th className="px-3 py-2 text-right font-medium">Pages</th>
                <th className="px-3 py-2 text-right font-medium">Median position</th>
                <th className="px-3 py-2 text-right font-medium">Clicks</th>
                <th className="px-3 py-2 text-right font-medium">Top 10</th>
                <th className="px-3 py-2 text-right font-medium">11–20</th>
                <th className="px-3 py-2 text-right font-medium">Not tracked</th>
              </tr>
            </thead>
            <tbody>
              {data.templates.map((t) => (
                <tr key={t.template} className="border-b last:border-0">
                  <td className="px-4 py-2 font-mono text-[12px]">{t.template}</td>
                  <td className="px-3 py-2 text-right tabular-nums">{t.pages}</td>
                  <td className="whitespace-nowrap px-3 py-2 text-right tabular-nums">
                    {pos(t.median_position)}
                    <Move now={t.median_position} before={t.median_previous} />
                  </td>
                  <td className="px-3 py-2 text-right tabular-nums">
                    {n(t.clicks)} <span className="text-[11px] text-muted-foreground">from {n(t.previous_clicks)}</span>
                  </td>
                  <td className="px-3 py-2 text-right tabular-nums">{t.top10}</td>
                  <td className="px-3 py-2 text-right tabular-nums">{t.striking}</td>
                  <td className="px-3 py-2 text-right tabular-nums">{t.untracked}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </section>
      )}

      <section className="overflow-x-auto rounded-lg border bg-card">
        {rows.length ? (
          <table className="w-full text-[13px]">
            <thead>
              <tr className="border-b text-left text-[12px] text-muted-foreground">
                <th className="px-4 py-2 font-medium">Page</th>
                <th className="px-3 py-2 font-medium">Target country</th>
                <th className="px-3 py-2 font-medium">Band</th>
                <th className="px-3 py-2 text-right font-medium">Tracked rank</th>
                <th className="px-3 py-2 text-right font-medium">Position there</th>
                <th className="px-3 py-2 text-right font-medium">Clicks there</th>
                <th className="px-3 py-2 text-right font-medium">Impressions</th>
                <th className="px-3 py-2 text-right font-medium">CTR</th>
                <th className="px-3 py-2 text-right font-medium">All countries</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((p) => (
                <tr key={p.url} className="border-b last:border-0 hover:bg-muted/40">
                  <td className="max-w-[300px] px-4 py-2">
                    <div className="truncate font-mono text-[12px]" title={p.url}>
                      {p.path}
                    </div>
                    <div className="truncate text-[11px] text-muted-foreground">{p.keyword || "no target keyword"}</div>
                  </td>
                  <td className="whitespace-nowrap px-3 py-2">
                    {country(p.target_country)} <span className="text-[11px] text-muted-foreground">{p.country_from === "tracked" ? "tracked" : "by impressions"}</span>
                  </td>
                  <td className="px-3 py-2">
                    <Badge tone={bandTone[p.band]}>{bandText[p.band]}</Badge>
                  </td>
                  <td className="whitespace-nowrap px-3 py-2 text-right tabular-nums">
                    {p.untracked ? <Badge tone="grey">Not tracked</Badge> : p.tracked_position ?? "–"}
                    {!p.untracked && <Move now={p.tracked_position} before={p.tracked_previous} />}
                  </td>
                  <td className="whitespace-nowrap px-3 py-2 text-right tabular-nums">
                    {pos(p.current.position)}
                    <Move now={p.current.position} before={p.previous.position} />
                  </td>
                  <td className="whitespace-nowrap px-3 py-2 text-right tabular-nums">
                    {n(p.current.clicks)} <span className="text-[11px] text-muted-foreground">from {n(p.previous.clicks)}</span>
                  </td>
                  <td className="px-3 py-2 text-right tabular-nums">{n(p.current.impressions)}</td>
                  <td className="px-3 py-2 text-right tabular-nums">{pct(p.current.ctr)}</td>
                  <td className="px-3 py-2 text-right tabular-nums text-muted-foreground">{n(p.all_countries.clicks)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : (
          <p className="p-4 text-[13px] text-muted-foreground">No page here.</p>
        )}
      </section>
      <ul className="list-disc space-y-1 pl-5 text-[12px] text-muted-foreground">
        {data.notes.map((x) => (
          <li key={x}>{x}</li>
        ))}
      </ul>
    </div>
  );
}
