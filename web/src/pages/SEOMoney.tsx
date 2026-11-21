import { useEffect, useState } from "react";
import type { components } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, ErrorText, Loading } from "../ui";
import { track } from "../lib/analytics";

// Money left on the table: the SEO dashboard's estimate of what search
// gaps are worth, from stored data. Pages that lost clicks, queries in
// positions 4 to 20 worth moving to 3, and queries clicked less than their
// position earns, each in extra clicks and dollars at one rate (revenue
// per organic click), with the link orders already pointing at the page.
// The rate and the formula are on the page; the rate can be changed.

type Money = components["schemas"]["SEOMoney"];
type Q = Money["positions"][number];
type View = "lost" | "positions" | "low_ctr" | "formula";

const usd = (v: number | null | undefined) =>
  v === null || v === undefined ? "–" : v.toLocaleString("en-US", { style: "currency", currency: "USD", maximumFractionDigits: Math.abs(v) < 100 ? 2 : 0 });
const n = (v: number | null | undefined) => (v === null || v === undefined ? "–" : Math.round(v).toLocaleString("en-US"));
const pct = (v: number) => `${(v * 100).toLocaleString("en-US", { maximumFractionDigits: 1 })}%`;
const pos = (v: number | null | undefined) => (v === null || v === undefined ? "–" : v.toFixed(1));
const day = (d: string | null) => (d ? new Date(d + "T00:00:00Z").toLocaleDateString(undefined, { day: "2-digit", month: "2-digit", timeZone: "UTC" }) : "–");
const pathOf = (u: string) => {
  try {
    const x = new URL(u);
    return x.pathname + x.search;
  } catch {
    return u;
  }
};

export function SEOMoney({ brand }: { brand: string }) {
  const [data, setData] = useState<Money | null>(null);
  const [error, setError] = useState("");
  const [view, setView] = useState<View>("positions");
  const [rpc, setRpc] = useState("");
  const [branded, setBranded] = useState(false);

  useEffect(() => {
    let live = true;
    setError("");
    const r = Number(rpc);
    const query: { rpc?: number; branded?: boolean } = {};
    if (rpc !== "" && Number.isFinite(r) && r >= 0) query.rpc = r;
    if (branded) query.branded = true;
    api
      .GET("/v1/brands/{brand}/seo/money", { params: { path: { brand }, query } })
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
  }, [brand, rpc, branded]);

  if (error) return <ErrorText>{error}</ErrorText>;
  if (!data) return <Loading />;
  if (!data.search_through) return null;

  const pick = (v: View) => {
    setView(v);
    track("ui.seo_money_view_switched", { view: v });
  };
  const tabs: [View, string, number | null][] = [
    ["positions", "Positions 4–20", data.totals.positions_4_20],
    ["low_ctr", "Low CTR", data.totals.low_ctr],
    ["lost", "Losing pages", data.totals.lost],
    ["formula", "How it's counted", null],
  ];

  return (
    <section className="rounded-lg border bg-card">
      <div className="flex flex-wrap items-center gap-3 border-b px-4 py-2">
        <span className="text-[15px] font-semibold">Money left on the table</span>
        <span className="text-[12px] text-muted-foreground">
          {day(data.from)} – {day(data.to)} vs {day(data.previous_from)} – {day(data.previous_to)}
        </span>
        <div className="ml-auto flex flex-wrap items-center gap-3 text-[12px]">
          <label className="flex items-center gap-1">
            Revenue per click
            <input
              type="number"
              min={0}
              step="0.01"
              value={rpc}
              placeholder={data.rpc_source === "measured" ? (data.rpc ?? 0).toFixed(2) : "0.00"}
              onChange={(e) => setRpc(e.target.value)}
              aria-label="Revenue per organic click in USD"
              className="h-7 w-20 rounded border bg-background px-1 tabular-nums"
            />
            <span className="text-muted-foreground">{data.rpc_source === "set" ? "your rate" : data.rpc_source === "measured" ? "measured" : "no revenue data"}</span>
          </label>
          <label className="flex items-center gap-1">
            <input type="checkbox" checked={branded} onChange={(e) => setBranded(e.target.checked)} />
            Brand searches
          </label>
        </div>
      </div>
      <div className="grid gap-px border-b bg-border sm:grid-cols-3">
        {tabs.slice(0, 3).map(([id, label, v]) => (
          <button type="button" key={id} onClick={() => pick(id)} className={"bg-card px-4 py-3 text-left " + (view === id ? "ring-2 ring-inset ring-foreground/50" : "hover:bg-muted/40")}>
            <div className="text-[12px] text-muted-foreground">{label}</div>
            <div className="text-[20px] font-semibold tabular-nums">{usd(v)}</div>
            <div className="text-[11px] text-muted-foreground">{id === "lost" ? "estimated, vs the 28 days before" : "estimated extra per 28 days"}</div>
          </button>
        ))}
      </div>
      <div className="flex gap-1 border-b px-3">
        {tabs.map(([id, label]) => (
          <button
            type="button"
            key={id}
            onClick={() => pick(id)}
            className={"border-b-2 px-2 py-1.5 text-[13px] " + (view === id ? "border-foreground font-medium" : "border-transparent text-muted-foreground hover:text-foreground")}
          >
            {label}
          </button>
        ))}
      </div>
      {view === "positions" && <Queries rows={data.positions} target />}
      {view === "low_ctr" && <Queries rows={data.low_ctr} />}
      {view === "lost" && <Lost rows={data.losing_pages} />}
      {view === "formula" && <Formula data={data} />}
    </section>
  );
}

function Orders({ count }: { count: number }) {
  return count ? <Badge tone="blue">{`${count} link order${count > 1 ? "s" : ""}`}</Badge> : <span className="text-muted-foreground">–</span>;
}

function Queries({ rows, target }: { rows: Q[]; target?: boolean }) {
  if (!rows.length) return <p className="p-4 text-[13px] text-muted-foreground">Nothing here for these 28 days.</p>;
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-[13px]">
        <thead>
          <tr className="border-b text-left text-[12px] text-muted-foreground">
            <th className="px-4 py-2 font-medium">Query and page</th>
            <th className="px-3 py-2 text-right font-medium">Impressions</th>
            <th className="px-3 py-2 text-right font-medium">Clicks</th>
            <th className="px-3 py-2 text-right font-medium">Position</th>
            <th className="px-3 py-2 text-right font-medium">CTR</th>
            <th className="px-3 py-2 text-right font-medium">Expected CTR</th>
            <th className="px-3 py-2 text-right font-medium">{target ? "Extra clicks at 3" : "Extra clicks"}</th>
            <th className="px-3 py-2 text-right font-medium">Extra $</th>
            <th className="px-3 py-2 text-left font-medium">Links</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((q) => (
            <tr key={q.query + q.url} className="border-b last:border-0 hover:bg-muted/40">
              <td className="max-w-[380px] px-4 py-2">
                <div className="truncate font-medium">{q.query}</div>
                <div className="truncate font-mono text-[11px] text-muted-foreground" title={q.url}>
                  {pathOf(q.url)}
                </div>
              </td>
              <td className="px-3 py-2 text-right tabular-nums">{n(q.impressions)}</td>
              <td className="px-3 py-2 text-right tabular-nums">{n(q.clicks)}</td>
              <td className="px-3 py-2 text-right tabular-nums">{pos(q.position)}</td>
              <td className="px-3 py-2 text-right tabular-nums">{pct(q.ctr)}</td>
              <td className="px-3 py-2 text-right tabular-nums text-muted-foreground">{pct(target && q.target_ctr ? q.target_ctr : q.expected_ctr)}</td>
              <td className="px-3 py-2 text-right tabular-nums">{n(q.extra_clicks)}</td>
              <td className="px-3 py-2 text-right font-medium tabular-nums">{usd(q.extra_revenue)}</td>
              <td className="px-3 py-2">
                <Orders count={q.link_orders} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function Lost({ rows }: { rows: Money["losing_pages"] }) {
  if (!rows.length) return <p className="p-4 text-[13px] text-muted-foreground">No page lost 10 or more clicks against the 28 days before.</p>;
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-[13px]">
        <thead>
          <tr className="border-b text-left text-[12px] text-muted-foreground">
            <th className="px-4 py-2 font-medium">Page</th>
            <th className="px-3 py-2 text-right font-medium">Clicks</th>
            <th className="px-3 py-2 text-right font-medium">Before</th>
            <th className="px-3 py-2 text-right font-medium">Lost</th>
            <th className="px-3 py-2 text-right font-medium">Position</th>
            <th className="px-3 py-2 text-right font-medium">Modeled loss</th>
            <th className="px-3 py-2 text-right font-medium">Own revenue change</th>
            <th className="px-3 py-2 text-left font-medium">Links</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((p) => (
            <tr key={p.host + p.path} className="border-b last:border-0 hover:bg-muted/40">
              <td className="max-w-[380px] truncate px-4 py-2 font-mono text-[12px]" title={p.host + p.path}>
                {p.path}
              </td>
              <td className="px-3 py-2 text-right tabular-nums">{n(p.clicks)}</td>
              <td className="px-3 py-2 text-right tabular-nums text-muted-foreground">{n(p.previous_clicks)}</td>
              <td className="px-3 py-2 text-right tabular-nums text-red-700 dark:text-red-400">−{n(p.lost_clicks)}</td>
              <td className="whitespace-nowrap px-3 py-2 text-right tabular-nums">
                {pos(p.position)} <span className="text-[11px] text-muted-foreground">from {pos(p.previous_position)}</span>
              </td>
              <td className="px-3 py-2 text-right font-medium tabular-nums">{usd(p.modeled_loss)}</td>
              <td
                className={
                  "px-3 py-2 text-right tabular-nums " +
                  (p.revenue_change === null ? "" : p.revenue_change < 0 ? "text-red-700 dark:text-red-400" : "text-green-700 dark:text-green-400")
                }
              >
                {p.revenue_change === null ? "–" : (p.revenue_change > 0 ? "+" : "") + usd(p.revenue_change)}
              </td>
              <td className="px-3 py-2">
                <Orders count={p.link_orders} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function Formula({ data }: { data: Money }) {
  return (
    <div className="grid gap-6 p-4 text-[13px] lg:grid-cols-[1.4fr_1fr]">
      <div className="space-y-2">
        <p>
          <span className="font-medium">Revenue per organic click</span> = first-touch organic revenue {usd(data.organic_revenue)} ÷ Search Console clicks {n(data.organic_clicks)} ={" "}
          <span className="font-medium">{usd(data.rpc)}</span>
          {data.rpc_source === "set" && " (your rate is used instead)"}.
        </p>
        <p>
          <span className="font-medium">Positions 4–20:</span> extra clicks = impressions × expected CTR at position 3 ({pct(data.expected_ctr[2])}) − clicks now. Extra $ = extra clicks ×
          revenue per click.
        </p>
        <p>
          <span className="font-medium">Low CTR:</span> CTR under 70% of the expected CTR for its position; extra clicks = impressions × expected CTR − clicks now.
        </p>
        <p>
          <span className="font-medium">Losing pages:</span> modeled loss = clicks lost × revenue per click. The page's own revenue change is shown beside it and never added in.
        </p>
        <ul className="list-disc space-y-1 pl-5 text-[12px] text-muted-foreground">
          {data.notes.map((x) => (
            <li key={x}>{x}</li>
          ))}
        </ul>
      </div>
      <div>
        <div className="mb-1 text-[12px] font-semibold">Expected CTR by position</div>
        <table className="w-full text-[12px] tabular-nums">
          <tbody>
            {[0, 1, 2, 3, 4].map((row) => (
              <tr key={row}>
                {[0, 1, 2, 3].map((col) => {
                  const i = col * 5 + row;
                  return (
                    <td key={col} className="py-0.5 pr-3">
                      <span className="text-muted-foreground">{i + 1}.</span> {pct(data.expected_ctr[i])}
                    </td>
                  );
                })}
              </tr>
            ))}
          </tbody>
        </table>
        <p className="mt-2 text-[11px] text-muted-foreground">A planning curve for organic results, not a measurement of this site.</p>
      </div>
    </div>
  );
}
