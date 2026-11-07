import { PickBrand } from "@/components/pick-brand";
import { pickedBrandId, useBrand } from "@/lib/brand";
// Right now: who is on the brand's website this minute, as its own tag sees
// it, asked again every 5 seconds while the page is open and visible. It
// names no one: counts, pages, sources and a feed of what is happening.

import { useEffect, useState } from "react";
import type { SiteLiveReport } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Button, Card, Empty, ErrorText, Loading, PageHeader, Stat, Stats } from "../ui";
import type { Tab } from "../App";

type Props = { ws: string; canManage: boolean; go: (tab: Tab, item?: string) => void };
type Item = SiteLiveReport["feed"][number];

const EVERY = 5000;

let regionNames: Intl.DisplayNames | null = null;
function country(code: string) {
  if (!code || code === "(none)") return "";
  try {
    regionNames ??= new Intl.DisplayNames(undefined, { type: "region" });
    return regionNames.of(code) ?? code;
  } catch {
    return code;
  }
}
function since(iso: string, now: number) {
  const s = Math.max(0, Math.round((now - new Date(iso).getTime()) / 1000));
  if (s < 60) return `${s}s ago`;
  if (s < 3600) return `${Math.round(s / 60)}m ago`;
  return `${Math.round(s / 3600)}h ago`;
}
const sourceName = (s: string) => (s === "(direct)" ? "Direct" : s === "(none)" || !s ? "Unknown" : s);
const eventName = (label: string, name: string) => label || name.replaceAll("_", " ").replace(/^\w/, (c) => c.toUpperCase());

function describe(it: Item) {
  const from = [country(it.country) && `from ${country(it.country)}`, it.source && it.source !== "(direct)" && `via ${sourceName(it.source)}`].filter(Boolean).join(" ");
  const who = it.known ? "A known visitor" : "A visitor";
  if (it.kind === "page") return <>{who}{from ? ` ${from}` : ""} viewed <span className="font-medium">{it.path || "/"}</span></>;
  return <>{who}{from ? ` ${from}` : ""}: <span className="font-medium">{eventName(it.label, it.name)}</span>{it.path ? <span className="text-muted-foreground"> on {it.path}</span> : null}</>;
}

export function Live(props: Props) {
  const { brand, active } = useBrand(props.ws);
  if (!brand) return <PickBrand what="Right now" brands={active.length} />;
  return <LivePage key={brand.id} {...props} />;
}

function LivePage({ go }: Props) {
  const [data, setData] = useState<SiteLiveReport | null>(null);
  const [error, setError] = useState("");
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    let stop = false, timer = 0;
    const load = async () => {
      if (document.visibilityState === "visible") {
        const { data, error } = await api.GET("/v1/brands/{brand}/analytics/live", { params: { path: { brand: pickedBrandId() } } });
        if (stop) return;
        if (error || !data) setError(errorMessage(error));
        else {
          setError("");
          setData(data);
          setNow(Date.now());
        }
      }
      if (!stop) timer = window.setTimeout(load, EVERY);
    };
    load();
    return () => {
      stop = true;
      window.clearTimeout(timer);
    };
  }, []);

  const header = (
    <PageHeader
      title="Right now"
      description="Who is on your website this minute, where they came from and what they are doing, from your Outflare tag. Nobody is named."
      action={<span className="flex items-center gap-2 text-[12px] text-muted-foreground"><span aria-hidden="true" className="size-2 rounded-full bg-green-500" />Updates every 5 seconds</span>}
    />
  );
  if (!data) return <div>{header}{error ? <ErrorText>{error}</ErrorText> : <Loading />}</div>;
  const max = Math.max(1, ...data.per_minute.map((m) => m.visitors));
  const quiet = data.last_30_minutes === 0 && data.feed.length === 0;
  return (
    <div className="space-y-6">
      {header}
      <ErrorText>{error}</ErrorText>
      {quiet && (
        <Empty title="Nobody on your website in the last 30 minutes" action={<Button variant="ghost" onClick={() => go("tracking")}>Check your tracking</Button>}>
          Visitors show here a second after they open a page with your Outflare tag.
        </Empty>
      )}
      <Stats cols={4}>
        <Stat label="On the site now" value={data.now.toLocaleString()} sub="Active in the last 5 minutes" />
        <Stat label="Last 30 minutes" value={data.last_30_minutes.toLocaleString()} sub="Visitors" />
        <Stat label="Events in the last hour" value={data.events.reduce((s, e) => s + e.count, 0).toLocaleString()} sub={data.events[0] ? `Most: ${eventName(data.events[0].label, data.events[0].name)}` : "Sign-ups, orders and other events"} />
        <Stat label="Top source now" value={<span className="block truncate text-lg">{data.sources[0] ? sourceName(data.sources[0].key) : "—"}</span>} sub={data.sources[0] ? `${data.sources[0].visitors} on the site` : undefined} />
      </Stats>

      <Card title="Visitors per minute" description="The last 30 minutes">
        <div className="flex h-28 items-end gap-1" role="img" aria-label={`Visitors per minute over the last 30 minutes, ${data.per_minute.at(-1)?.visitors ?? 0} in the last minute`}>
          {data.per_minute.map((m) => (
            <div key={m.at} title={`${new Date(m.at).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" })}: ${m.visitors}`} className="min-w-0 flex-1 rounded-t-sm bg-blue-500/80" style={{ height: `${Math.max(2, (m.visitors / max) * 100)}%` }} />
          ))}
        </div>
        <div className="mt-1 flex justify-between font-mono text-[11px] text-muted-foreground"><span>30 min ago</span><span>Now</span></div>
      </Card>

      <div className="grid gap-4 lg:grid-cols-3">
        <Card title="Pages open now">
          <List rows={data.pages.map((p) => ({ key: `${p.website}${p.path}`, label: p.path, sub: p.website, n: p.visitors }))} empty="No page open now." />
        </Card>
        <Card title="Where they came from">
          <List rows={data.sources.map((s) => ({ key: s.key, label: sourceName(s.key), n: s.visitors }))} empty="Nobody on the site now." />
        </Card>
        <Card title="Countries">
          <List rows={data.countries.map((c) => ({ key: c.key, label: country(c.key) || "Unknown", n: c.visitors }))} empty="Nobody on the site now." />
        </Card>
      </div>

      <Card title="As it happens" description="The last 30 page views and events">
        {data.feed.length === 0 ? (
          <p className="py-4 text-center text-[13px] text-muted-foreground">Nothing in the last 30 minutes.</p>
        ) : (
          <ul className="divide-y" aria-live="polite">
            {data.feed.map((it, i) => (
              <li key={`${it.at}${i}`} className="flex items-start gap-3 py-2 text-[13px]">
                <span aria-hidden="true" className={`mt-1.5 size-2 shrink-0 rounded-full ${it.kind === "track" ? "bg-green-500" : "bg-muted-foreground/40"}`} />
                <span className="min-w-0 flex-1">{describe(it)}</span>
                <span className="shrink-0 font-mono text-[12px] text-muted-foreground">{since(it.at, now)}</span>
              </li>
            ))}
          </ul>
        )}
      </Card>
    </div>
  );
}

function List({ rows, empty }: { rows: { key: string; label: string; sub?: string; n: number }[]; empty: string }) {
  if (rows.length === 0) return <p className="py-2 text-[13px] text-muted-foreground">{empty}</p>;
  return (
    <ul className="divide-y">
      {rows.map((r) => (
        <li key={r.key} className="flex items-center justify-between gap-3 py-2 text-[13px]">
          <span className="min-w-0 truncate" title={r.sub ? `${r.sub}${r.label}` : r.label}>{r.label}</span>
          <span className="font-mono font-medium tabular-nums">{r.n}</span>
        </li>
      ))}
    </ul>
  );
}
