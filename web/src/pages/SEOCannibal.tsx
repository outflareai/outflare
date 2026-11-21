import { Fragment, useEffect, useMemo, useState } from "react";
import type { components } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { PickBrand } from "@/components/pick-brand";
import { api, errorMessage } from "../api";
import { Badge, Button, Empty, ErrorText, Loading } from "../ui";
import { track } from "../lib/analytics";

// Searches the brand's pages compete for, across its sites: what is new,
// ongoing, gained a page or resolved against the 28 days before, the page
// to keep (a person can choose it), whether the pages share a language,
// the clicks split between them, and the fix on the fixes list.

type Data = components["schemas"]["SEOCannibalisation"];
type Cluster = Data["clusters"][number];
type StateFilter = "all" | Cluster["state"];

const n = (v: number) => Math.round(v).toLocaleString("en-US");
const pathOf = (u: string) => {
  try {
    const x = new URL(u);
    return x.pathname + x.search;
  } catch {
    return u;
  }
};
const stateText: Record<Cluster["state"], string> = { new: "New", ongoing: "Ongoing", gained_page: "Gained a page" };
const stateTone: Record<Cluster["state"], "red" | "grey" | "amber"> = { new: "red", ongoing: "grey", gained_page: "amber" };
const reasonText: Record<Cluster["keep_reason"], string> = { chosen: "chosen", target: "target", clicks: "most clicks" };

export function SEOCannibal({ ws }: { ws: string }) {
  const { brand, active } = useBrand(ws);
  const [data, setData] = useState<Data | null>(null);
  const [error, setError] = useState("");
  const [reload, setReload] = useState(0);
  const [state, setState] = useState<StateFilter>("all");
  const [locale, setLocale] = useState<Cluster["locale"]>("same_locale");
  const [onlyUnfixed, setOnlyUnfixed] = useState(false);
  const [open, setOpen] = useState<string | null>(null);

  useEffect(() => {
    if (!brand) return;
    let live = true;
    setError("");
    api
      .GET("/v1/brands/{brand}/seo/cannibalisation", { params: { path: { brand: brand.id } } })
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
  }, [brand?.id, reload]);

  const rows = useMemo(
    () =>
      (data?.clusters ?? []).filter((c) => (state === "all" || c.state === state) && c.locale === locale && (!onlyUnfixed || !c.fix)),
    [data, state, locale, onlyUnfixed],
  );

  if (!brand) return <PickBrand what="Cannibalization" brands={active.length} />;
  if (error) return <ErrorText>{error}</ErrorText>;
  if (!data) return <Loading />;
  if (!data.search_through) return <Empty title="No Search Console data yet">Connect Google Search Console to an SEO site of this brand; competing searches show up after its first sync.</Empty>;

  const keep = async (c: Cluster, url: string) => {
    const { error } = await api.PUT("/v1/brands/{brand}/seo-sites/{site}/cannibalisation/primary", {
      params: { path: { brand: brand.id, site: c.site_id } },
      body: { query: c.query, url },
    });
    if (error) return setError(errorMessage(error));
    track("ui.seo_cannibal_primary_set", { cleared: url === "" });
    setReload((x) => x + 1);
  };

  const s = data.summary;
  const tiles: [StateFilter, string, number][] = [
    ["all", "Open", s.open],
    ["new", "New", s.new],
    ["gained_page", "Gained a page", s.gained_page],
  ];

  return (
    <div className="space-y-5">
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-4 lg:grid-cols-7">
        {tiles.map(([id, label, v]) => (
          <button
            type="button"
            key={id}
            aria-pressed={state === id}
            onClick={() => setState(id)}
            className={"rounded-lg border bg-card px-3 py-2 text-left " + (state === id ? "ring-2 ring-foreground/60" : "hover:shadow-sm")}
          >
            <div className="text-[11px] text-muted-foreground">{label}</div>
            <div className="text-[18px] font-semibold tabular-nums">{n(v)}</div>
          </button>
        ))}
        {(
          [
            ["Resolved", s.resolved],
            ["Not on fixes", s.not_on_fixes],
            ["Split clicks", s.split_clicks],
            ["Estimated gain", s.estimated_gain],
          ] as const
        ).map(([label, v]) => (
          <div key={label} className="rounded-lg border bg-card px-3 py-2">
            <div className="text-[11px] text-muted-foreground">{label}</div>
            <div className="text-[18px] font-semibold tabular-nums">{n(v)}</div>
          </div>
        ))}
      </div>

      <div className="flex flex-wrap items-center gap-3 text-[13px]">
        <div className="flex gap-1">
          {(
            [
              ["same_locale", "Same language"],
              ["cross_locale", `Across languages (${s.cross_locale})`],
            ] as const
          ).map(([id, label]) => (
            <button type="button" key={id} onClick={() => setLocale(id)} className={"rounded border px-2 py-1 " + (locale === id ? "border-foreground font-medium" : "text-muted-foreground")}>
              {label}
            </button>
          ))}
        </div>
        <label className="flex items-center gap-1">
          <input type="checkbox" checked={onlyUnfixed} onChange={(e) => setOnlyUnfixed(e.target.checked)} />
          Not on the fixes list
        </label>
        <span className="text-muted-foreground">28 days to {data.search_through}</span>
      </div>

      <section className="overflow-x-auto rounded-lg border bg-card">
        {rows.length ? (
          <table className="w-full text-[13px]">
            <thead>
              <tr className="border-b text-left text-[12px] text-muted-foreground">
                <th className="px-4 py-2 font-medium">Search</th>
                <th className="px-3 py-2 font-medium">State</th>
                <th className="px-3 py-2 text-right font-medium">Pages</th>
                <th className="px-3 py-2 text-right font-medium">Impressions</th>
                <th className="px-3 py-2 text-right font-medium">Clicks</th>
                <th className="px-3 py-2 font-medium">Page to keep</th>
                <th className="px-3 py-2 text-right font-medium">Split clicks</th>
                <th className="px-3 py-2 text-right font-medium">Est. gain</th>
                <th className="px-3 py-2 font-medium">Fix</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((c) => {
                const id = c.site_id + c.query;
                const isOpen = open === id;
                return (
                  <Fragment key={id}>
                    <tr className="cursor-pointer border-b hover:bg-muted/40" onClick={() => setOpen(isOpen ? null : id)}>
                      <td className="max-w-[280px] truncate px-4 py-2 font-medium">{c.query}</td>
                      <td className="px-3 py-2">
                        <Badge tone={stateTone[c.state]}>{c.state === "gained_page" ? `${c.previous_pages} → ${c.pages.length} pages` : stateText[c.state]}</Badge>
                      </td>
                      <td className="px-3 py-2 text-right tabular-nums">{c.pages.length}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{n(c.impressions)}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{n(c.clicks)}</td>
                      <td className="max-w-[260px] px-3 py-2">
                        <span className="truncate font-mono text-[12px]">{pathOf(c.keep)}</span> <span className="text-[11px] text-muted-foreground">({reasonText[c.keep_reason]})</span>
                      </td>
                      <td className="px-3 py-2 text-right tabular-nums">{n(c.split_clicks)}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{c.estimated_gain ? `+${n(c.estimated_gain)}` : "–"}</td>
                      <td className="px-3 py-2">{c.fix ? <Badge tone={c.fix.status === "done" ? "green" : "blue"}>{c.fix.status.replace("_", " ")}</Badge> : <span className="text-muted-foreground">–</span>}</td>
                    </tr>
                    {isOpen && (
                      <tr className="border-b bg-muted/20">
                        <td colSpan={9} className="px-4 py-3">
                          <table className="w-full text-[12px]">
                            <thead>
                              <tr className="text-left text-muted-foreground">
                                <th className="py-1 font-medium">Page</th>
                                <th className="py-1 text-right font-medium">Impressions</th>
                                <th className="py-1 text-right font-medium">Share</th>
                                <th className="py-1 text-right font-medium">Clicks</th>
                                <th className="py-1 text-right font-medium">Position</th>
                                <th />
                              </tr>
                            </thead>
                            <tbody>
                              {c.pages.map((p) => (
                                <tr key={p.url}>
                                  <td className="py-1 font-mono text-[11px]">
                                    {pathOf(p.url)} {p.target && <Badge tone="blue">target</Badge>} {p.url === c.keep && <Badge tone="green">keep</Badge>}
                                  </td>
                                  <td className="py-1 text-right tabular-nums">{n(p.impressions)}</td>
                                  <td className="py-1 text-right tabular-nums">{(p.share * 100).toFixed(0)}%</td>
                                  <td className="py-1 text-right tabular-nums">{n(p.clicks)}</td>
                                  <td className="py-1 text-right tabular-nums">{p.position.toFixed(1)}</td>
                                  <td className="py-1 text-right">
                                    {p.url !== c.keep && (
                                      <Button variant="ghost" onClick={() => keep(c, p.url)}>
                                        Keep this one
                                      </Button>
                                    )}
                                  </td>
                                </tr>
                              ))}
                            </tbody>
                          </table>
                          <p className="mt-2 text-[12px] text-muted-foreground">
                            Point the other pages at the one to keep: a link with "{c.query}" as the anchor, the search out of their titles and H1s, or a canonical or redirect when they
                            are duplicates.{" "}
                            {c.keep_reason === "chosen" && (
                              <button type="button" className="underline underline-offset-2" onClick={() => keep(c, "")}>
                                Go back to Outflare's pick
                              </button>
                            )}
                          </p>
                        </td>
                      </tr>
                    )}
                  </Fragment>
                );
              })}
            </tbody>
          </table>
        ) : (
          <p className="p-4 text-[13px] text-muted-foreground">No competing search in this group.</p>
        )}
      </section>

      {data.resolved.length > 0 && (
        <section className="rounded-lg border bg-card p-4">
          <div className="mb-2 text-[13px] font-semibold">Resolved since the 28 days before</div>
          <div className="flex flex-wrap gap-2 text-[12px]">
            {data.resolved.map((r) => (
              <Badge key={r.site_id + r.query} tone="green">{`${r.query} (${r.pages} pages)`}</Badge>
            ))}
          </div>
        </section>
      )}
      <ul className="list-disc space-y-1 pl-5 text-[12px] text-muted-foreground">
        {data.notes.map((x) => (
          <li key={x}>{x}</li>
        ))}
      </ul>
    </div>
  );
}
