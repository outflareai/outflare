import { Fragment, useEffect, useMemo, useState } from "react";
import type { components } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { PickBrand } from "@/components/pick-brand";
import { api, errorMessage } from "../api";
import { Badge, Empty, ErrorText, Loading } from "../ui";
import { track } from "../lib/analytics";
import { toAlpha2 } from "./SearchPerformance";

// Pages losing search clicks: the last 7 or 28 days against the same
// length before, as new, still declining or recovering, with where the
// loss is (country, device, query), what moved (impressions, position,
// CTR), what it costs, and flags that point at a cause.

type Data = components["schemas"]["SEODeclining"];
type Page = Data["pages"][number];
type Cohort = "all" | Page["cohort"];

const n = (v: number | null | undefined) => (v === null || v === undefined ? "–" : Math.round(v).toLocaleString("en-US"));
const usd = (v: number) => v.toLocaleString("en-US", { style: "currency", currency: "USD", maximumFractionDigits: 0 });
const pct = (v: number | null | undefined) => (v === null || v === undefined ? "–" : `${(v * 100).toFixed(1)}%`);
const pos = (v: number | null | undefined) => (v === null || v === undefined ? "–" : v.toFixed(1));
const day = (d: string | null) => (d ? new Date(d + "T00:00:00Z").toLocaleDateString(undefined, { day: "2-digit", month: "2-digit", timeZone: "UTC" }) : "–");
const region = new Intl.DisplayNames(["en"], { type: "region" });
const country = (code: string) => {
  const two = code.length === 2 ? code.toUpperCase() : toAlpha2.get(code.toLowerCase());
  return (two && region.of(two)) || code.toUpperCase();
};

const cohortText: Record<Page["cohort"], string> = { new: "New", still: "Still declining", recovering: "Recovering" };
const cohortTone: Record<Page["cohort"], "red" | "amber" | "green"> = { new: "red", still: "amber", recovering: "green" };
const driverText: Record<Page["driver"], string> = {
  clicks: "Clicks only",
  impressions: "Fewer impressions",
  position: "Lost positions",
  ctr: "Lower CTR",
};
const techText: Record<string, string> = { server_error: "5xx", not_found: "4xx", redirect: "Redirects", noindex: "noindex" };

export function SEODeclining({ ws }: { ws: string }) {
  const { brand, active } = useBrand(ws);
  const [days, setDays] = useState<7 | 28>(7);
  const [data, setData] = useState<Data | null>(null);
  const [error, setError] = useState("");
  const [cohort, setCohort] = useState<Cohort>("all");
  const [open, setOpen] = useState<string | null>(null);

  useEffect(() => {
    if (!brand) return;
    let live = true;
    setData(null);
    setError("");
    api
      .GET("/v1/brands/{brand}/seo/declining", { params: { path: { brand: brand.id }, query: { days } } })
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
  }, [brand?.id, days]);

  const rows = useMemo(() => (data ? data.pages.filter((p) => cohort === "all" || p.cohort === cohort) : []), [data, cohort]);

  if (!brand) return <PickBrand what="Pages losing search clicks" brands={active.length} />;
  if (error) return <ErrorText>{error}</ErrorText>;
  if (!data) return <Loading />;
  if (!data.to) return <Empty title="No Search Console data yet">Connect Google Search Console; pages show up here after its first sync.</Empty>;

  const tiles: [Cohort, string, string][] = [
    ["all", "Clicks lost", n(data.summary.clicks_lost)],
    ["new", "New", n(data.summary.new)],
    ["still", "Still declining", n(data.summary.still)],
    ["recovering", "Recovering", n(data.summary.recovering)],
  ];

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center gap-3 text-[13px]">
        <div className="flex gap-1">
          {([7, 28] as const).map((d) => (
            <button
              type="button"
              key={d}
              onClick={() => {
                setDays(d);
                track("ui.seo_declining_period_picked", { days: d });
              }}
              className={"rounded border px-2 py-1 " + (days === d ? "border-foreground font-medium" : "text-muted-foreground")}
            >
              {d} days
            </button>
          ))}
        </div>
        <span className="text-muted-foreground">
          {day(data.from)} – {day(data.to)} vs {day(data.previous_from)} – {day(data.previous_to)}
        </span>
      </div>
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-5">
        {tiles.map(([id, label, v]) => (
          <button
            type="button"
            key={id}
            aria-pressed={cohort === id}
            onClick={() => setCohort(id)}
            className={"rounded-lg border bg-card px-3 py-2 text-left " + (cohort === id ? "ring-2 ring-foreground/60" : "hover:shadow-sm")}
          >
            <div className="text-[11px] text-muted-foreground">{label}</div>
            <div className="text-[18px] font-semibold tabular-nums">{v}</div>
          </button>
        ))}
        <div className="rounded-lg border bg-card px-3 py-2">
          <div className="text-[11px] text-muted-foreground">Modeled loss</div>
          <div className="text-[18px] font-semibold tabular-nums">{usd(data.summary.modeled_loss)}</div>
          <div className="text-[11px] text-muted-foreground">at {usd(data.rpc ?? 0)} a click</div>
        </div>
      </div>

      <section className="overflow-x-auto rounded-lg border bg-card">
        {rows.length ? (
          <table className="w-full text-[13px]">
            <thead>
              <tr className="border-b text-left text-[12px] text-muted-foreground">
                <th className="px-4 py-2 font-medium">Page</th>
                <th className="px-3 py-2 font-medium">Cohort</th>
                <th className="px-3 py-2 text-right font-medium">Clicks</th>
                <th className="px-3 py-2 text-right font-medium">Before</th>
                <th className="px-3 py-2 text-right font-medium">Change</th>
                <th className="px-3 py-2 font-medium">What moved</th>
                <th className="px-3 py-2 font-medium">Most lost in</th>
                <th className="px-3 py-2 text-right font-medium">Sign-ups</th>
                <th className="px-3 py-2 text-right font-medium">Modeled loss</th>
                <th className="px-3 py-2 font-medium">Flags</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((p) => {
                const isOpen = open === p.url;
                return (
                  <Fragment key={p.url}>
                    <tr className="cursor-pointer border-b hover:bg-muted/40" onClick={() => setOpen(isOpen ? null : p.url)}>
                      <td className="max-w-[320px] truncate px-4 py-2 font-mono text-[12px]" title={p.url}>
                        {p.path}
                      </td>
                      <td className="px-3 py-2">
                        <Badge tone={cohortTone[p.cohort]}>{cohortText[p.cohort]}</Badge>
                      </td>
                      <td className="px-3 py-2 text-right tabular-nums">{n(p.clicks)}</td>
                      <td className="px-3 py-2 text-right tabular-nums text-muted-foreground">{n(p.previous_clicks)}</td>
                      <td className={"px-3 py-2 text-right tabular-nums " + (p.change < 0 ? "text-red-700 dark:text-red-400" : "text-green-700 dark:text-green-400")}>
                        {p.change > 0 ? "+" : ""}
                        {(p.change * 100).toFixed(0)}%
                      </td>
                      <td className="px-3 py-2 text-[12px]">{driverText[p.driver]}</td>
                      <td className="max-w-[220px] px-3 py-2 text-[12px]">
                        {p.top_country && <div>{country(p.top_country.key)}</div>}
                        {p.top_query && <div className="truncate text-muted-foreground">"{p.top_query.key}"</div>}
                      </td>
                      <td className="px-3 py-2 text-right tabular-nums">
                        {n(p.signups)} <span className="text-[11px] text-muted-foreground">from {n(p.previous_signups)}</span>
                      </td>
                      <td className="px-3 py-2 text-right tabular-nums">{p.modeled_loss ? usd(p.modeled_loss) : "–"}</td>
                      <td className="space-x-1 px-3 py-2">
                        {p.technical && <Badge tone="red">{techText[p.technical] ?? p.technical}</Badge>}
                        {p.competing_pages > 0 && <Badge tone="amber">{`${p.competing_pages} competing`}</Badge>}
                      </td>
                    </tr>
                    {isOpen && (
                      <tr className="border-b bg-muted/20">
                        <td colSpan={10} className="px-4 py-3">
                          <Details p={p} />
                        </td>
                      </tr>
                    )}
                  </Fragment>
                );
              })}
            </tbody>
          </table>
        ) : (
          <p className="p-4 text-[13px] text-muted-foreground">No page in this group.</p>
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

function Details({ p }: { p: Page }) {
  const line = (label: string, cur: string, prev: string) => (
    <tr>
      <td className="py-0.5 pr-3 text-muted-foreground">{label}</td>
      <td className="py-0.5 pr-3 text-right tabular-nums">{cur}</td>
      <td className="py-0.5 text-right tabular-nums text-muted-foreground">{prev}</td>
    </tr>
  );
  return (
    <div className="grid gap-5 text-[12px] lg:grid-cols-3">
      <table>
        <thead>
          <tr className="text-muted-foreground">
            <th />
            <th className="text-right font-medium">Now</th>
            <th className="text-right font-medium">Before</th>
          </tr>
        </thead>
        <tbody>
          {line("Clicks", n(p.clicks), n(p.previous_clicks))}
          {line("Impressions", n(p.impressions), n(p.previous_impressions))}
          {line("Position", pos(p.position), pos(p.previous_position))}
          {line("CTR", pct(p.ctr), pct(p.previous_ctr))}
          {line("Revenue", p.revenue === null ? "–" : usd(p.revenue), p.previous_revenue === null ? "–" : usd(p.previous_revenue))}
          {line("Clicks two periods ago", n(p.earlier_clicks), "")}
        </tbody>
      </table>
      <div>
        <div className="mb-1 font-semibold">Countries that lost most</div>
        {p.countries.length ? (
          <table className="w-full">
            <tbody>
              {p.countries.map((c) => line(country(c.key), n(c.clicks), n(c.previous_clicks)))}
            </tbody>
          </table>
        ) : (
          <p className="text-muted-foreground">No country data.</p>
        )}
        {p.top_device && (
          <p className="mt-2">
            Device that lost most: <span className="font-medium">{p.top_device.key.toLowerCase()}</span> ({n(p.top_device.clicks)} from {n(p.top_device.previous_clicks)})
          </p>
        )}
      </div>
      <div className="space-y-1">
        {p.top_query && (
          <p>
            Query that lost most: <span className="font-medium">"{p.top_query.key}"</span> ({n(p.top_query.clicks)} from {n(p.top_query.previous_clicks)})
          </p>
        )}
        {p.technical && <p>The latest crawl found this page {p.technical === "redirect" ? "redirecting" : p.technical === "noindex" ? "set to noindex" : `returning ${techText[p.technical]}`}: fix that first.</p>}
        {p.competing_pages > 0 && <p>{p.competing_pages} other page(s) of the site now show for that query: check for cannibalization.</p>}
        {p.driver === "ctr" && <p>Position held but fewer people clicked: rewrite the title and description.</p>}
        {p.driver === "position" && <p>The page lost positions: compare it with the results now above it and with what changed on it recently.</p>}
        {p.driver === "impressions" && <p>Fewer impressions: less demand, or the page stopped showing for some queries.</p>}
      </div>
    </div>
  );
}
