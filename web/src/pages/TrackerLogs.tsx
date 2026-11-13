// Tracker → Logs (plans/ad-tracker-ui.md B6): the brand's clicks, lander
// clicks, conversions, postbacks in and out, and errors, newest first, in
// RedTrack's tabs. A time window and filters narrow each tab, Live refreshes
// it every few seconds until paused, a row opens its detail with the click
// id to copy, and Load more follows the cursor.

import { useCallback, useEffect, useRef, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { PickBrand } from "@/components/pick-brand";
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { pickedBrandId, useBrand } from "@/lib/brand";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, CopyButton, ErrorText, field, Loading, PageHeader } from "../ui";
import type { Tab } from "../App";

type Click = Schemas["TrackerClick"];
type LanderClick = Schemas["TrackerLanderClick"];
type Conversion = Schemas["TrackerConversion"];
type Call = Schemas["TrackerConversionCall"];
type Send = Schemas["TrafficSourcePostbackSend"] & { source: string };
type Kind = "clicks" | "lander" | "conversions" | "in" | "out" | "errors";

type Props = { ws: string; go: (tab: Tab, item?: string) => void };

const kinds: { id: Kind; label: string }[] = [
  { id: "clicks", label: "Clicks" },
  { id: "lander", label: "Lander clicks" },
  { id: "conversions", label: "Conversions" },
  { id: "in", label: "Postbacks in" },
  { id: "out", label: "Postbacks out" },
  { id: "errors", label: "Errors" },
];

const windows: { id: string; label: string; minutes: number }[] = [
  { id: "15m", label: "15 minutes", minutes: 15 },
  { id: "1h", label: "1 hour", minutes: 60 },
  { id: "24h", label: "24 hours", minutes: 1440 },
  { id: "7d", label: "7 days", minutes: 10080 },
  { id: "30d", label: "30 days", minutes: 43200 },
];

// The fix for each kind of postback that wasn't recorded (Voluum's error
// list): what went wrong and what to do.
export const fixes: Record<string, { title: string; fix: string; tab?: Tab }> = {
  unknown_click: { title: "Unknown click id", fix: "The postback named a click Outflare never saw: check the offer URL carries {click_id} where the network takes your sub id.", tab: "tracker-offers" },
  expired_click: { title: "Click expired", fix: "The conversion came after the network's click expiry: raise it on the affiliate network if that is normal for the offer.", tab: "tracker-networks" },
  invalid: { title: "Bad parameter", fix: "A value couldn't be read, such as a payout that isn't a number: check the network's macros in the postback URL.", tab: "tracker-setup" },
  wrong_domain: { title: "Wrong domain", fix: "The postback came to a domain that isn't the brand's verified tracking domain: give the network the URL from Setup.", tab: "tracker-setup" },
  ip_refused: { title: "Address not allowed", fix: "The network called from an address off the allowlist: add its address on the affiliate network, or clear the list.", tab: "tracker-networks" },
  rate_limited: { title: "Too many postbacks", fix: "The network sent more than 6,000 a minute: ask it to slow down or batch them.", tab: undefined },
  error: { title: "Outflare couldn't record it", fix: "Something failed on Outflare's side: the network's retries usually get through. If it keeps happening, contact support with the request id.", tab: undefined },
};

const when = (t: string) => new Date(t).toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", second: "2-digit" });

export function TrackerLogs({ ws, go }: Props) {
  const { brand, active } = useBrand(ws);
  const [kind, setKind] = useState<Kind>("clicks");
  const [win, setWin] = useState("24h");
  const [campaign, setCampaign] = useState("");
  const [click, setClick] = useState("");
  const [bots, setBots] = useState(false);
  const [live, setLive] = useState(false);
  const [campaigns, setCampaigns] = useState<Schemas["TrackingCampaign"][]>([]);
  const [names, setNames] = useState<Record<string, string>>({});
  const [rows, setRows] = useState<unknown[] | null>(null);
  const [cursor, setCursor] = useState<string | null>(null);
  const [error, setError] = useState("");
  const [open, setOpen] = useState<unknown>(null);
  const brandId = brand ? pickedBrandId() : "";

  // Names for ids: campaigns, offers, landers, networks and sources.
  useEffect(() => {
    if (!brandId) return;
    const path = { path: { brand: brandId } };
    void Promise.all([
      api.GET("/v1/brands/{brand}/tracking-campaigns", { params: path }),
      api.GET("/v1/brands/{brand}/offers", { params: path }),
      api.GET("/v1/brands/{brand}/landers", { params: path }),
      api.GET("/v1/brands/{brand}/postback-sources", { params: path }),
      api.GET("/v1/brands/{brand}/traffic-sources", { params: path }),
    ]).then((rs) => {
      const all = rs.flatMap((r) => ((r.data as { data?: { id: string; name: string }[] } | undefined)?.data ?? []));
      setNames(Object.fromEntries(all.map((x) => [x.id, x.name])));
      setCampaigns(((rs[0].data?.data ?? []) as Schemas["TrackingCampaign"][]).filter((c) => c.traffic_source_id || c.redirect_links));
    });
  }, [brandId]);

  const fetchPage = useCallback(
    async (after: string | null): Promise<{ data: unknown[]; next: string | null; error?: unknown }> => {
      const path = { brand: brandId };
      const since = new Date(Date.now() - (windows.find((w) => w.id === win)?.minutes ?? 1440) * 60_000).toISOString();
      const page = after ? { cursor: after } : {};
      const clickId = click.trim() || undefined;
      switch (kind) {
        case "clicks": {
          const r = await api.GET("/v1/brands/{brand}/tracker-clicks", { params: { path, query: { since, campaign: campaign || undefined, click_id: clickId, bots, ...page } } });
          return { data: r.data?.data ?? [], next: r.data?.next_cursor ?? null, error: r.error };
        }
        case "lander": {
          const r = await api.GET("/v1/brands/{brand}/tracker-lander-clicks", { params: { path, query: { since, click_id: clickId, bots, ...page } } });
          return { data: r.data?.data ?? [], next: r.data?.next_cursor ?? null, error: r.error };
        }
        case "conversions": {
          const r = await api.GET("/v1/brands/{brand}/tracker-conversions", { params: { path, query: { campaign: campaign || undefined } } });
          const list = ((r.data?.data ?? []) as Conversion[]).filter((c) => c.converted_at >= since && (!clickId || c.click_key === clickId.toLowerCase()));
          return { data: list, next: null, error: r.error };
        }
        case "in":
        case "errors": {
          const r = await api.GET("/v1/brands/{brand}/tracker-conversion-log", { params: { path, query: { click_id: clickId, limit: 200, ...page } } });
          let list = ((r.data?.data ?? []) as Call[]).filter((c) => c.created_at >= since);
          if (kind === "errors") list = list.filter((c) => !["accepted", "updated", "duplicate"].includes(c.outcome));
          return { data: list, next: kind === "in" ? r.data?.next_cursor ?? null : null, error: r.error };
        }
        case "out": {
          const s = await api.GET("/v1/brands/{brand}/traffic-sources", { params: { path } });
          const sources = ((s.data?.data ?? []) as Schemas["TrafficSource"][]).filter((x) => x.postback.url);
          const all = await Promise.all(sources.map((src) => api.GET("/v1/brands/{brand}/traffic-sources/{id}/postbacks", { params: { path: { brand: brandId, id: src.id } } }).then((r) => ((r.data?.data ?? []) as Schemas["TrafficSourcePostbackSend"][]).map((x) => ({ ...x, source: src.name })))));
          const list = all.flat().filter((x) => x.created_at >= since).sort((a, b) => b.created_at.localeCompare(a.created_at));
          return { data: list, next: null, error: s.error };
        }
      }
    },
    [brandId, kind, win, campaign, click, bots],
  );
  // seq ties a reply to the latest request, so a slow reply for another
  // tab or filter never lands on this one.
  const seq = useRef(0);
  const load = useCallback(async () => {
    const mine = ++seq.current;
    const r = await fetchPage(null);
    if (mine !== seq.current) return;
    if (r.error) return setError(errorMessage(r.error));
    setError("");
    setRows(r.data);
    setCursor(r.next);
  }, [fetchPage]);
  useEffect(() => {
    if (!brandId) return;
    setRows(null);
    void load();
  }, [brandId, load]);
  useEffect(() => {
    if (!live) return;
    const t = setInterval(() => void load(), 5000);
    return () => clearInterval(t);
  }, [live, load]);
  const more = async () => {
    if (!cursor) return;
    const mine = seq.current;
    const r = await fetchPage(cursor);
    if (mine !== seq.current) return;
    if (r.error) return setError(errorMessage(r.error));
    setRows([...(rows ?? []), ...r.data]);
    setCursor(r.next);
  };

  if (!brand) return <PickBrand what="tracker logs" brands={active.length} />;
  const name = (id: string | null | undefined) => (id ? names[id] ?? id.slice(0, 8) : "—");
  return (
    <div className="space-y-4">
      <PageHeader title="Logs" description="Every click, lander click, conversion and postback, newest first. Postback calls are kept for 30 days." />
      <div className="flex flex-wrap gap-1" role="tablist" aria-label="Log">
        {kinds.map((k) => (
          <button key={k.id} type="button" role="tab" aria-selected={kind === k.id} className={`rounded-md border px-3 py-1 text-sm ${kind === k.id ? "border-primary bg-primary text-primary-foreground" : "hover:bg-muted"}`} onClick={() => {
              setRows(null);
              setKind(k.id);
            }}>
            {k.label}
          </button>
        ))}
      </div>
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <select aria-label="Time window" className={field} value={win} onChange={(e) => setWin(e.target.value)}>
          {windows.map((w) => (
            <option key={w.id} value={w.id}>Last {w.label}</option>
          ))}
        </select>
        {(kind === "clicks" || kind === "conversions") && (
          <select aria-label="Campaign" className={field} value={campaign} onChange={(e) => setCampaign(e.target.value)}>
            <option value="">Every campaign</option>
            {campaigns.map((c) => (
              <option key={c.id} value={c.id}>{c.name}</option>
            ))}
          </select>
        )}
        {kind !== "out" && <input aria-label="Click id filter" className={`${field} w-48 font-mono text-xs`} placeholder="Click id" value={click} onChange={(e) => setClick(e.target.value)} />}
        {(kind === "clicks" || kind === "lander") && (
          <label className="flex items-center gap-1">
            <input type="checkbox" checked={bots} onChange={(e) => setBots(e.target.checked)} /> Show likely bots
          </label>
        )}
        <Button variant={live ? "primary" : "ghost"} aria-pressed={live} onClick={() => setLive(!live)}>{live ? "Live · Pause" : "Live"}</Button>
      </div>
      {error && <ErrorText>{error}</ErrorText>}
      {!rows ? (
        <Loading />
      ) : kind === "errors" ? (
        <Errors calls={rows as Call[]} go={go} />
      ) : rows.length === 0 ? (
        <Card title="Nothing in this window">
          <p className="text-sm text-muted-foreground">Widen the time window or clear the filters.</p>
        </Card>
      ) : (
        <Card title={`${rows.length}${cursor ? "+" : ""} ${kinds.find((k) => k.id === kind)!.label.toLowerCase()}`}>
          <div className="overflow-x-auto">
            <table className="w-full text-sm" data-testid="log-table">
              <tbody>
                {rows.map((row, i) => (
                  <tr key={i} className="cursor-pointer border-t hover:bg-muted/40" onClick={() => setOpen(row)}>
                    <LogRow kind={kind} row={row} name={name} />
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {cursor && <Button variant="ghost" onClick={() => void more()}>Load more</Button>}
        </Card>
      )}
      <Sheet open={open !== null} onOpenChange={(o) => !o && setOpen(null)}>
        <SheetContent className="w-full overflow-y-auto sm:max-w-lg">
          {open !== null && <Detail row={open as Record<string, unknown>} name={name} />}
        </SheetContent>
      </Sheet>
    </div>
  );
}

function LogRow({ kind, row, name }: { kind: Kind; row: unknown; name: (id: string | null | undefined) => string }) {
  const cell = "py-1.5 pr-3 whitespace-nowrap";
  switch (kind) {
    case "clicks": {
      const c = row as Click;
      return (
        <>
          <td className={cell}>{when(c.clicked_at)}</td>
          <td className={`${cell} font-mono text-xs`}>{c.click_key}</td>
          <td className={cell}>{name(c.campaign_id)}</td>
          <td className={cell}>{c.lander_id ? name(c.lander_id) : ""}{c.lander_id && c.offer_id ? " → " : ""}{c.offer_id ? name(c.offer_id) : ""}</td>
          <td className={cell}>{[c.country, c.device, c.os].filter(Boolean).join(" · ")}</td>
          <td className={cell}>
            {c.likely_bot && <Badge tone="grey">Bot</Badge>}
            {c.fraud_reasons.length > 0 && <Badge tone="amber">Suspect</Badge>}
          </td>
        </>
      );
    }
    case "lander": {
      const c = row as LanderClick;
      return (
        <>
          <td className={cell}>{when(c.clicked_at)}</td>
          <td className={`${cell} font-mono text-xs`}>{c.click_key}</td>
          <td className={cell}>{name(c.lander_id)}</td>
          <td className={cell}>{c.cta === 0 ? "Any offer" : `CTA ${c.cta}`} → {name(c.offer_id)}</td>
          <td className={cell}>{c.likely_bot && <Badge tone="grey">Bot</Badge>}</td>
        </>
      );
    }
    case "conversions": {
      const c = row as Conversion;
      return (
        <>
          <td className={cell}>{when(c.converted_at)}</td>
          <td className={`${cell} font-mono text-xs`}>{c.click_key}</td>
          <td className={cell}>{name(c.campaign_id)}</td>
          <td className={cell}>{c.type}</td>
          <td className={`${cell} text-right font-mono tabular-nums`}>{c.payout} {c.currency}</td>
          <td className={cell}><Badge tone={c.status === "approved" ? "green" : c.status === "pending" ? "amber" : "grey"}>{c.status}</Badge></td>
        </>
      );
    }
    case "in": {
      const c = row as Call;
      const ok = ["accepted", "updated"].includes(c.outcome);
      return (
        <>
          <td className={cell}>{when(c.created_at)}</td>
          <td className={cell}>{name(c.source_id)}</td>
          <td className={`${cell} font-mono text-xs`}>{c.click_key || "—"}</td>
          <td className={cell}>{c.request.payout ?? ""}</td>
          <td className={cell}><Badge tone={ok ? "green" : c.outcome === "duplicate" ? "amber" : "red"}>{c.outcome}</Badge></td>
          <td className={`${cell} text-muted-foreground`}>{c.reason}</td>
        </>
      );
    }
    case "out": {
      const c = row as Send;
      return (
        <>
          <td className={cell}>{when(c.created_at)}</td>
          <td className={cell}>{c.source}</td>
          <td className={`${cell} font-mono tabular-nums`}>{c.payout}</td>
          <td className={cell}><Badge tone={c.status === "sent" ? "green" : c.status === "failed" ? "red" : "grey"}>{c.status}</Badge></td>
          <td className={cell}>{c.http_status ?? "—"}{c.attempts > 1 ? ` · ${c.attempts} tries` : ""}</td>
          <td className={`${cell} max-w-xs truncate text-muted-foreground`}>{c.error || c.url}</td>
        </>
      );
    }
    default:
      return null;
  }
}

// Errors: the postbacks that weren't recorded, grouped by why, each with
// its fix (Voluum).
function Errors({ calls, go }: { calls: Call[]; go: (tab: Tab) => void }) {
  const groups = Object.entries(
    calls.reduce<Record<string, Call[]>>((acc, c) => {
      (acc[c.outcome] ??= []).push(c);
      return acc;
    }, {}),
  ).sort((a, b) => b[1].length - a[1].length);
  if (groups.length === 0) {
    return (
      <Card title="No errors in this window">
        <p className="text-sm text-muted-foreground">Every postback in this window was recorded.</p>
      </Card>
    );
  }
  return (
    <div className="space-y-2" data-testid="error-groups">
      {groups.map(([outcome, list]) => {
        const f = fixes[outcome] ?? { title: outcome, fix: list[0].reason };
        return (
          <Card key={outcome} title={`${f.title} · ${list.length}`}>
            <p className="text-sm">{f.fix}</p>
            <p className="mt-1 text-xs text-muted-foreground">Latest: {when(list[0].created_at)}, {list[0].reason}</p>
            {f.tab && <Button variant="ghost" onClick={() => go(f.tab!)}>Fix it</Button>}
          </Card>
        );
      })}
    </div>
  );
}

// Detail: every field of a row, ids by name, the click id to copy.
function Detail({ row, name }: { row: Record<string, unknown>; name: (id: string | null | undefined) => string }) {
  const key = (row.click_key as string) || "";
  const entries = Object.entries(row).filter(([k]) => k !== "click_key");
  const show = (k: string, v: unknown): string => {
    if (v === null || v === undefined || v === "") return "—";
    if (k.endsWith("_id") && typeof v === "string" && /^[0-9a-f-]{36}$/.test(v)) return name(v);
    if (typeof v === "object") return JSON.stringify(v);
    return String(v);
  };
  return (
    <div className="space-y-3 p-4">
      <SheetHeader className="p-0">
        <SheetTitle>Details</SheetTitle>
      </SheetHeader>
      {key && (
        <div className="flex items-center gap-2 text-sm">
          <span>Click id</span>
          <span className="font-mono text-xs" data-testid="detail-click">{key}</span>
          <CopyButton text={key} label="Copy" />
        </div>
      )}
      <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs">
        {entries.map(([k, v]) => (
          <div key={k} className="contents">
            <dt className="text-muted-foreground">{k.replace(/_/g, " ")}</dt>
            <dd className="break-all font-mono">{show(k, v)}</dd>
          </div>
        ))}
      </dl>
    </div>
  );
}
