import { useEffect, useState } from "react";
import type { components } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { PickBrand } from "@/components/pick-brand";
import { HBars } from "@/components/share-charts";
import { seriesColor } from "@/components/chart-kit";
import { api, errorMessage } from "../api";
import { appPath } from "../lib/paths";
import { Badge, ErrorText, Loading } from "../ui";
import { track } from "../lib/analytics";

// Links at a glance: referring domains week by week against competitors,
// links and domains gained and lost a week, links lost lately (bought
// ones first), bought links failing their live check, and the anchors.
// Link orders and Backlinks keep every record; this links to them.

type Data = components["schemas"]["SEOLinks"];
type Series = Data["series"][number];
type Problem = Data["orders"][number]["problems"][number];

const n = (v: number | null | undefined) => (v === null || v === undefined ? "–" : Math.round(v).toLocaleString("en-US"));
const signed = (v: number | null | undefined) => (v === null || v === undefined ? "–" : `${v > 0 ? "+" : ""}${Math.round(v).toLocaleString("en-US")}`);
const tone = (v: number | null | undefined) => (!v ? "" : v > 0 ? "text-green-700 dark:text-green-400" : "text-red-700 dark:text-red-400");
const date = (d: string | null | undefined) => (d ? new Date(d).toLocaleDateString("en-US", { day: "numeric", month: "short" }) : "–");
const problemText: Record<Problem, string> = {
  lost: "Lost",
  not_live: "Not found",
  wrong_target: "Other target",
  anchor_mismatch: "Other anchor",
  unchecked: "Not checked 3 days",
};
const anchorText: Record<keyof Data["anchors"], string> = {
  branded: "Branded",
  url: "URL",
  generic: "Generic",
  exact: "Exact match",
  partial: "Partial match",
  other: "Other",
  empty: "No text",
};

function Spark({ s, i }: { s: Series; i: number }) {
  const pts = s.points;
  if (pts.length < 2) return <span className="text-muted-foreground">–</span>;
  const vals = pts.map((p) => p.referring_domains);
  const lo = Math.min(...vals);
  const hi = Math.max(...vals);
  const w = 120;
  const h = 24;
  const d = pts.map((p, k) => `${k ? "L" : "M"}${((k / (pts.length - 1)) * w).toFixed(1)},${(hi === lo ? h / 2 : h - ((p.referring_domains - lo) / (hi - lo)) * h).toFixed(1)}`).join(" ");
  return (
    <svg width={w} height={h} viewBox={`0 0 ${w} ${h}`} aria-hidden="true">
      <path d={d} fill="none" stroke={seriesColor(s.competitor ? 1 + (i % 5) : 0)} strokeWidth={1.5} />
    </svg>
  );
}

export function SEOLinks({ ws }: { ws: string }) {
  const { brand, active } = useBrand(ws);
  const [data, setData] = useState<Data | null>(null);
  const [error, setError] = useState("");
  const [paidOnly, setPaidOnly] = useState(false);

  useEffect(() => {
    if (!brand) return;
    let live = true;
    setData(null);
    setError("");
    api
      .GET("/v1/brands/{brand}/seo/links", { params: { path: { brand: brand.id } } })
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

  if (!brand) return <PickBrand what="Links" brands={active.length} />;
  if (error) return <ErrorText>{error}</ErrorText>;
  if (!data) return <Loading />;

  const s = data.summary;
  const lost = paidOnly ? data.lost.filter((l) => l.order_id) : data.lost;
  const maxWeek = Math.max(1, ...data.velocity.map((v) => Math.max(v.new_domains, v.lost_domains)));
  const tiles: [string, string, string?, string?][] = [
    ["Referring domains", n(s.referring_domains)],
    ["Week on week", signed(s.rd_wow), tone(s.rd_wow)],
    ["Four weeks", signed(s.rd_mom), tone(s.rd_mom)],
    ["Domains this week", `+${n(s.new_domains_week)} / −${n(s.lost_domains_week)}`],
    ["Lost links, 30 days", n(s.lost_links), "", s.lost_paid ? `${n(s.lost_paid)} bought` : undefined],
    ["Bought links live", `${n(s.bought_live)} / ${n(s.bought_total)}`, s.bought_problems ? "text-red-700 dark:text-red-400" : "", s.bought_problems ? `${n(s.bought_problems)} need a look` : undefined],
  ];

  return (
    <div className="space-y-5">
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-6">
        {tiles.map(([label, v, cls, sub]) => (
          <div key={label} className="rounded-lg border bg-card px-3 py-2">
            <div className="text-[11px] text-muted-foreground">{label}</div>
            <div className={"text-[18px] font-semibold tabular-nums " + (cls ?? "")}>{v}</div>
            {sub && <div className="text-[11px] text-red-700 dark:text-red-400">{sub}</div>}
          </div>
        ))}
      </div>

      <div className="grid gap-5 lg:grid-cols-2">
        <section className="overflow-x-auto rounded-lg border bg-card">
          <div className="border-b px-4 py-2 text-[13px] font-medium">Referring domains, 13 weeks</div>
          {data.series.length ? (
            <table className="w-full text-[13px]">
              <thead>
                <tr className="border-b text-left text-[12px] text-muted-foreground">
                  <th className="px-4 py-2 font-medium">Domain</th>
                  <th className="px-3 py-2 font-medium">Trend</th>
                  <th className="px-3 py-2 text-right font-medium">Now</th>
                  <th className="px-3 py-2 text-right font-medium">WoW</th>
                  <th className="px-3 py-2 text-right font-medium">4 weeks</th>
                </tr>
              </thead>
              <tbody>
                {data.series.map((x, i) => (
                  <tr key={x.host} className={"border-b last:border-0 " + (x.competitor ? "" : "font-medium")}>
                    <td className="px-4 py-2 font-mono text-[12px]">
                      {x.host}
                      {!x.competitor && <span className="ml-2"><Badge tone="blue">us</Badge></span>}
                    </td>
                    <td className="px-3 py-1">
                      <Spark s={x} i={i} />
                    </td>
                    <td className="px-3 py-2 text-right tabular-nums">{n(x.latest)}</td>
                    <td className={"px-3 py-2 text-right tabular-nums " + tone(x.wow)}>{signed(x.wow)}</td>
                    <td className={"px-3 py-2 text-right tabular-nums " + tone(x.mom)}>{signed(x.mom)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          ) : (
            <div className="px-4 py-6 text-[13px] text-muted-foreground">No SEO snapshots yet: connect Ahrefs or DataForSEO in Rankings & backlinks.</div>
          )}
        </section>

        <section className="rounded-lg border bg-card">
          <div className="border-b px-4 py-2 text-[13px] font-medium">Linking domains won and lost a week</div>
          <ul className="space-y-1 px-4 py-3 text-[12px]">
            {data.velocity.map((v) => (
              <li key={v.week} className="grid grid-cols-[56px_1fr_1fr_80px] items-center gap-2">
                <span className="text-muted-foreground">{date(v.week)}</span>
                <span className="flex justify-end">
                  <span className="h-3 rounded-l bg-red-500/70" style={{ width: `${(v.lost_domains / maxWeek) * 100}%` }} />
                </span>
                <span>
                  <span className="block h-3 rounded-r bg-green-600/70" style={{ width: `${(v.new_domains / maxWeek) * 100}%` }} />
                </span>
                <span className="text-right tabular-nums">
                  <span className="text-green-700 dark:text-green-400">+{v.new_domains}</span> <span className="text-red-700 dark:text-red-400">−{v.lost_domains}</span>
                </span>
              </li>
            ))}
          </ul>
        </section>
      </div>

      {data.orders.length > 0 && (
        <section className="overflow-x-auto rounded-lg border bg-card">
          <div className="flex items-center justify-between border-b px-4 py-2 text-[13px]">
            <span className="font-medium">Bought links that need a look</span>
            <a className="text-[12px] underline" href={appPath(ws, "link-orders")}>
              Link orders
            </a>
          </div>
          <table className="w-full text-[13px]">
            <thead>
              <tr className="border-b text-left text-[12px] text-muted-foreground">
                <th className="px-4 py-2 font-medium">Published on</th>
                <th className="px-3 py-2 font-medium">Target</th>
                <th className="px-3 py-2 font-medium">Ordered anchor</th>
                <th className="px-3 py-2 font-medium">Found</th>
                <th className="px-3 py-2 font-medium">Checked</th>
                <th className="px-3 py-2 font-medium">Problem</th>
              </tr>
            </thead>
            <tbody>
              {data.orders.map((o) => (
                <tr key={o.id} className="border-b last:border-0">
                  <td className="max-w-[260px] truncate px-4 py-2 font-mono text-[12px]" title={o.published_url}>
                    <a className="hover:underline" href={appPath(ws, "link-orders", o.id)} onClick={() => track("ui.seo_links_order_opened", { problems: o.problems.length })}>
                      {o.published_url || "–"}
                    </a>
                  </td>
                  <td className="max-w-[220px] truncate px-3 py-2 font-mono text-[12px]" title={o.target_url}>{o.target_url}</td>
                  <td className="px-3 py-2">{o.anchor || "–"}</td>
                  <td className="px-3 py-2">{o.live_anchor || "–"}</td>
                  <td className="px-3 py-2">{date(o.checked_at)}</td>
                  <td className="space-x-1 px-3 py-2">
                    {o.problems.map((p) => (
                      <Badge key={p} tone={p === "unchecked" || p === "anchor_mismatch" ? "amber" : "red"}>
                        {problemText[p]}
                      </Badge>
                    ))}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </section>
      )}

      <div className="grid gap-5 lg:grid-cols-[2fr_1fr]">
        <section className="overflow-x-auto rounded-lg border bg-card">
          <div className="flex items-center justify-between border-b px-4 py-2 text-[13px]">
            <span className="font-medium">Links lost in the last 30 days</span>
            <label className="flex items-center gap-2 text-[12px] text-muted-foreground">
              <input
                type="checkbox"
                checked={paidOnly}
                onChange={(e) => {
                  setPaidOnly(e.target.checked);
                  track("ui.seo_links_filtered", { bought_only: e.target.checked });
                }}
              />
              Bought only
            </label>
          </div>
          {lost.length ? (
            <table className="w-full text-[13px]">
              <thead>
                <tr className="border-b text-left text-[12px] text-muted-foreground">
                  <th className="px-4 py-2 font-medium">Linking page</th>
                  <th className="px-3 py-2 text-right font-medium">DR</th>
                  <th className="px-3 py-2 font-medium">Target</th>
                  <th className="px-3 py-2 font-medium">Anchor</th>
                  <th className="px-3 py-2 font-medium">Lost</th>
                </tr>
              </thead>
              <tbody>
                {lost.map((l) => (
                  <tr key={l.source_url + l.target_url} className="border-b last:border-0">
                    <td className="max-w-[280px] truncate px-4 py-2 font-mono text-[12px]" title={l.source_url}>
                      {l.order_id && <span className="mr-2"><Badge tone="red">bought</Badge></span>}
                      {l.source_url}
                    </td>
                    <td className="px-3 py-2 text-right tabular-nums">{n(l.source_rating)}</td>
                    <td className="max-w-[200px] truncate px-3 py-2 font-mono text-[12px]" title={l.target_url}>{l.target_url}</td>
                    <td className="max-w-[160px] truncate px-3 py-2">{l.anchor || "–"}</td>
                    <td className="px-3 py-2">{date(l.lost_at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          ) : (
            <div className="px-4 py-6 text-[13px] text-muted-foreground">No links lost in the last 30 days.</div>
          )}
        </section>

        <section className="rounded-lg border bg-card">
          <div className="border-b px-4 py-2 text-[13px] font-medium">Anchors of live links</div>
          <div className="px-4 py-3">
            <HBars
              compact
              format={(v) => n(v)}
              parts={(Object.keys(anchorText) as (keyof Data["anchors"])[]).map((k) => ({ label: anchorText[k], value: data.anchors[k] })).sort((a, b) => (b.value ?? 0) - (a.value ?? 0))}
            />
          </div>
        </section>
      </div>

      <ul className="list-disc space-y-1 pl-5 text-[12px] text-muted-foreground">
        {data.notes.map((t) => (
          <li key={t}>{t}</li>
        ))}
      </ul>
    </div>
  );
}
