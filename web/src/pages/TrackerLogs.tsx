// Tracker → Logs (plans/ad-tracker-ui.md B6): the brand's clicks, lander
// clicks, conversions, postbacks in and out, cost updates and errors,
// newest first, in RedTrack's tabs. Cost updates also takes a campaign's
// cost by hand or from a CSV. A time window and filters narrow each tab, Live refreshes
// it every few seconds until paused, a row opens its detail in the page (Back returns
// to the log as it was) with the click id to copy, and Load more follows the cursor.

import { useCallback, useEffect, useRef, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { PickBrand } from "@/components/pick-brand";
import { pickedBrandId, useBrand } from "@/lib/brand";
import { api, errorMessage } from "../api";
import { BackButton, Badge, Button, Card, CopyButton, ErrorText, field, Loading, PageHeader } from "../ui";
import type { Tab } from "../App";

type Click = Schemas["TrackerClick"];
type LanderClick = Schemas["TrackerLanderClick"];
type Conversion = Schemas["TrackerConversion"];
type Call = Schemas["TrackerConversionCall"];
type Send = Schemas["TrafficSourcePostbackSend"] & { source: string; source_id: string };
type CostUpdate = Schemas["TrackerCostUpdate"];
type Kind = "clicks" | "lander" | "conversions" | "in" | "out" | "costs" | "errors";

type Props = { ws: string; go: (tab: Tab, item?: string) => void };

const kinds: { id: Kind; label: string }[] = [
  { id: "clicks", label: "Clicks" },
  { id: "lander", label: "Lander clicks" },
  { id: "conversions", label: "Conversions" },
  { id: "in", label: "Postbacks in" },
  { id: "out", label: "Postbacks out" },
  { id: "costs", label: "Cost updates" },
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
          const all = await Promise.all(sources.map((src) => api.GET("/v1/brands/{brand}/traffic-sources/{id}/postbacks", { params: { path: { brand: brandId, id: src.id } } }).then((r) => ((r.data?.data ?? []) as Schemas["TrafficSourcePostbackSend"][]).map((x) => ({ ...x, source: src.name, source_id: src.id })))));
          const list = all.flat().filter((x) => x.created_at >= since).sort((a, b) => b.created_at.localeCompare(a.created_at));
          return { data: list, next: null, error: s.error };
        }
        case "costs": {
          const r = await api.GET("/v1/brands/{brand}/tracker-cost-updates", { params: { path, query: { campaign: campaign || undefined, limit: 200, ...page } } });
          return { data: ((r.data?.data ?? []) as CostUpdate[]).filter((u) => u.created_at >= since), next: r.data?.next_cursor ?? null, error: r.error };
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
  // Sends one postback to its traffic source again, as it was queued.
  const resend = async (send: Send) => {
    const { error } = await api.POST("/v1/brands/{brand}/traffic-sources/{id}/postbacks/{send}/resend", { params: { path: { brand: brandId, id: send.source_id, send: send.id } } });
    if (error) return setError(errorMessage(error));
    setError("");
    void load();
  };

  if (!brand) return <PickBrand what="tracker logs" brands={active.length} />;
  const name = (id: string | null | undefined) => (id ? names[id] ?? id.slice(0, 8) : "—");
  // One row opens in the page's own space; Back returns to the same log,
  // with its filters as they were.
  if (open !== null) {
    const label = kinds.find((k) => k.id === kind)!.label.toLowerCase();
    return (
      <div data-testid="details">
        <BackButton to={label} onClick={() => setOpen(null)} />
        <Detail row={open as Record<string, unknown>} name={name} />
      </div>
    );
  }
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
        {(kind === "clicks" || kind === "conversions" || kind === "costs") && (
          <select aria-label="Campaign" className={field} value={campaign} onChange={(e) => setCampaign(e.target.value)}>
            <option value="">Every campaign</option>
            {campaigns.map((c) => (
              <option key={c.id} value={c.id}>{c.name}</option>
            ))}
          </select>
        )}
        {kind !== "out" && kind !== "costs" && <input aria-label="Click id filter" className={`${field} w-48 font-mono text-xs`} placeholder="Click id" value={click} onChange={(e) => setClick(e.target.value)} />}
        {(kind === "clicks" || kind === "lander") && (
          <label className="flex items-center gap-1">
            <input type="checkbox" checked={bots} onChange={(e) => setBots(e.target.checked)} /> Show likely bots
          </label>
        )}
        <Button variant={live ? "primary" : "ghost"} aria-pressed={live} onClick={() => setLive(!live)}>{live ? "Live · Pause" : "Live"}</Button>
      </div>
      {error && <ErrorText>{error}</ErrorText>}
      {kind === "costs" && (
        <CostForm brandId={brandId} campaigns={campaigns} onSaved={() => {
            void load();
            // The job usually finishes within a second or two.
            setTimeout(() => void load(), 2000);
          }} />
      )}
      {!rows ? (
        <Loading />
      ) : kind === "errors" ? (
        <Errors calls={rows as Call[]} go={go} />
      ) : rows.length === 0 ? (
        <Card title="Nothing in this window">
          <p className="text-sm text-muted-foreground">Widen the time window or clear the filters.</p>
          {kind === "out" && <p className="mt-2 text-sm text-muted-foreground">{heldNote}</p>}
        </Card>
      ) : (
        <Card title={`${rows.length}${cursor ? "+" : ""} ${kinds.find((k) => k.id === kind)!.label.toLowerCase()}`}>
          <div className="overflow-x-auto">
            <table className="w-full text-sm" data-testid="log-table">
              <thead>
                <tr className="text-left text-xs text-muted-foreground">
                  {logHeaders[kind].map((h, i) => (
                    <th key={i} scope="col" className={`py-1.5 pr-3 font-medium whitespace-nowrap ${h.right ? "text-right" : ""}`}>{h.label}</th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {rows.map((row, i) => (
                  <tr key={i} tabIndex={0} className="cursor-pointer border-t hover:bg-muted/40 focus-visible:outline-2 focus-visible:outline-primary" onClick={() => setOpen(row)}
                    onKeyDown={(e) => { if (e.key === "Enter" && e.target === e.currentTarget) setOpen(row); }}>
                    <LogRow kind={kind} row={row} name={name} resend={resend} />
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {cursor && <Button variant="ghost" onClick={() => void more()}>Load more</Button>}
          {kind === "out" && <p className="mt-2 text-xs text-muted-foreground">{heldNote}</p>}
        </Card>
      )}
    </div>
  );
}

// The column headers of each log, in the order LogRow fills them.
const logHeaders: Record<string, { label: string; right?: boolean }[]> = {
  clicks: [{ label: "Time" }, { label: "Click id" }, { label: "Campaign" }, { label: "Went to" }, { label: "Visitor" }, { label: "Flags" }],
  lander: [{ label: "Time" }, { label: "Click id" }, { label: "Lander" }, { label: "Went to" }, { label: "Flags" }],
  conversions: [{ label: "Time" }, { label: "Click id" }, { label: "Campaign" }, { label: "Type" }, { label: "Payout", right: true }, { label: "Status" }, { label: "Traffic source told" }, { label: "Flags" }],
  in: [{ label: "Time" }, { label: "Network" }, { label: "Click id" }, { label: "Payout" }, { label: "Outcome" }, { label: "Why" }],
  out: [{ label: "Time" }, { label: "Traffic source" }, { label: "Payout" }, { label: "Status" }, { label: "Answer" }, { label: "Detail" }, { label: "" }],
  costs: [{ label: "Entered" }, { label: "Campaign" }, { label: "Days" }, { label: "Amount", right: true }, { label: "Status" }, { label: "Result" }],
  errors: [],
};

// Why a click or conversion was flagged, in words.
const suspectReasons: Record<string, string> = {
  fast_conversion: "converted within 10 seconds of the click",
  datacenter_network: "came from a hosting network",
  repeat_click: "one visitor clicked over and over",
  filter_list: "on your click filter lists",
  honeypot: "followed the lander's hidden link",
  ip_reputation: "your IP lookup scored the address as risky",
};
// What became of telling the traffic source about a conversion, in words.
const NETWORK_TOLD: Record<string, { label: string; tone: "green" | "amber" | "red" | "grey"; why: string }> = {
  sent: { label: "Sent", tone: "green", why: "The traffic source was told. Postbacks out has the call." },
  queued: { label: "Queued", tone: "grey", why: "The call to the traffic source is waiting to go out." },
  failed: { label: "Failed", tone: "red", why: "The traffic source refused the call or couldn't be reached. Postbacks out has the answer and a Send again button." },
  no_traffic_source: { label: "Not sent", tone: "grey", why: "Its campaign has no active traffic source, so there is nobody to tell." },
  no_postback_url: { label: "Not sent", tone: "amber", why: "The traffic source has no postback URL, or its URL still holds REPLACE. Add it under Traffic sources." },
  suspect: { label: "Held back", tone: "amber", why: "A suspect conversion counts in your report but isn't sent to the traffic source." },
  awaiting_lookup: { label: "Waiting", tone: "grey", why: "Its click's address is still being looked up. It is sent once the lookup clears it." },
  filtered: { label: "Not sent", tone: "grey", why: "The traffic source's postback rules leave out this type or status." },
  below_minimum: { label: "Not sent", tone: "grey", why: "Its payout is under the minimum in the traffic source's postback rules." },
};

export function suspectWhy(reasons: string[] | undefined): string {
  const words = (reasons ?? []).map((r) => suspectReasons[r] ?? r.replaceAll("_", " "));
  return words.length ? words.join("; ").replace(/^./, (c) => c.toUpperCase()) : "";
}
const heldNote = "A conversion flagged as suspect is not sent back to the traffic source, so it has no row here. Find them under Conversions, marked Suspect.";

function LogRow({ kind, row, name, resend }: { kind: Kind; row: unknown; name: (id: string | null | undefined) => string; resend: (send: Send) => Promise<void> }) {
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
          <td className={cell}>
            {NETWORK_TOLD[c.network_postback] && (
              <span title={NETWORK_TOLD[c.network_postback].why}>
                <Badge tone={NETWORK_TOLD[c.network_postback].tone}>{NETWORK_TOLD[c.network_postback].label}</Badge>
                {!["sent", "queued", "failed"].includes(c.network_postback) && <span className="ml-1 text-xs text-muted-foreground">{NETWORK_TOLD[c.network_postback].why}</span>}
              </span>
            )}
          </td>
          <td className={cell}>
            {(c.fraud_reasons?.length ?? 0) > 0 && (
              <span title={`${suspectWhy(c.fraud_reasons)}. A suspect conversion counts in your report but isn't sent back to the traffic source.`}>
                <Badge tone="amber">Suspect</Badge> <span className="text-xs text-muted-foreground">{suspectWhy(c.fraud_reasons)}</span>
              </span>
            )}
          </td>
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
          <td className={`${cell} max-w-xs truncate text-muted-foreground`}>
            {c.warning === "no_source_click_id" && (
              <span title="The postback URL asks for the network's click id, and this conversion's click arrived without one, so the network can't tell which click to credit. Check that the ad's URL passes the network's click id.">
                <Badge tone="amber">No network click id</Badge>{" "}
              </span>
            )}
            {c.error || c.url}
          </td>
          <td className={`${cell} text-right`}>
            {c.status !== "queued" && (
              <Button variant="ghost" title="Send this conversion to the traffic source again, with the status and payout it was queued with." onClick={(e) => {
                  e.stopPropagation();
                  void resend(c);
                }}>
                Send again
              </Button>
            )}
          </td>
        </>
      );
    }
    case "costs": {
      const c = row as CostUpdate;
      return (
        <>
          <td className={cell}>{when(c.created_at)}</td>
          <td className={cell}>{name(c.campaign_id)}</td>
          <td className={cell}>{c.from === c.to ? c.from : `${c.from} to ${c.to}`}</td>
          <td className={`${cell} text-right font-mono tabular-nums`}>{c.mode === "clear" ? "Cleared" : c.amount}</td>
          <td className={cell}><Badge tone={c.status === "applied" ? "green" : c.status === "failed" ? "red" : "grey"}>{c.status}</Badge></td>
          <td className={`${cell} max-w-xs truncate text-muted-foreground`}>{c.status === "applied" ? `${c.clicks.toLocaleString()} clicks` : c.error}</td>
        </>
      );
    }
    default:
      return null;
  }
}

// A CSV of costs: campaign, from, to, amount on each line, with or without
// a header. The campaign is its name, its code or its id.
export function parseCostCSV(text: string, campaigns: { id: string; name: string; code: string }[]): { updates: { campaign_id: string; from: string; to: string; amount: number }[]; error: string } {
  const updates: { campaign_id: string; from: string; to: string; amount: number }[] = [];
  const day = /^\d{4}-\d{2}-\d{2}$/;
  const lines = text.split(/\r?\n/).map((l) => l.trim()).filter(Boolean);
  for (const [i, line] of lines.entries()) {
    const cells = line.split(/[,;\t]/).map((c) => c.trim().replace(/^"(.*)"$/, "$1"));
    if (i === 0 && cells[0]?.toLowerCase() === "campaign") continue;
    const [who, from, to, amount] = cells;
    const found = campaigns.find((c) => c.id === who || c.code === who || c.name.toLowerCase() === (who ?? "").toLowerCase());
    const value = Number(amount);
    if (cells.length < 4) return { updates: [], error: `Line ${i + 1} needs four values: campaign, from, to, amount.` };
    if (!found) return { updates: [], error: `Line ${i + 1}: no campaign is called "${who}".` };
    if (!day.test(from ?? "") || !day.test(to ?? "")) return { updates: [], error: `Line ${i + 1}: write the days as 2026-10-01.` };
    if (amount === "" || !Number.isFinite(value) || value < 0) return { updates: [], error: `Line ${i + 1}: "${amount}" isn't an amount.` };
    updates.push({ campaign_id: found.id, from: from!, to: to!, amount: value });
  }
  if (updates.length === 0) return { updates: [], error: "The file has no costs in it." };
  if (updates.length > 500) return { updates: [], error: "A file takes 500 lines at most; split it." };
  return { updates, error: "" };
}

// CostForm enters what a campaign cost over some days, for networks whose
// cost Outflare can't read: one campaign by hand, or many from a CSV.
function CostForm({ brandId, campaigns, onSaved }: { brandId: string; campaigns: Schemas["TrackingCampaign"][]; onSaved: () => void }) {
  const today = new Date().toISOString().slice(0, 10);
  const [campaign, setCampaign] = useState("");
  const [from, setFrom] = useState(today);
  const [to, setTo] = useState(today);
  const [amount, setAmount] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [note, setNote] = useState("");
  const send = async (updates: { campaign_id: string; from: string; to: string; amount: number }[]) => {
    setBusy(true);
    setError("");
    setNote("");
    const { error } = await api.POST("/v1/brands/{brand}/tracker-cost-updates", { params: { path: { brand: brandId } }, body: { updates } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    setNote(updates.length === 1 ? "Queued. It shows below as applied once it is shared out over the clicks." : `${updates.length} costs queued.`);
    setAmount("");
    onSaved();
  };
  const upload = async (file: File | undefined) => {
    if (!file) return;
    const parsed = parseCostCSV(await file.text(), campaigns);
    if (parsed.error) return setError(parsed.error);
    await send(parsed.updates);
  };
  const value = Number(amount);
  const ready = campaign !== "" && from !== "" && to !== "" && amount.trim() !== "" && Number.isFinite(value) && value >= 0;
  return (
    <Card title="Enter a campaign's cost" description="For a network whose cost Outflare can't read. The amount is shared out evenly over the campaign's clicks in those days, in the traffic source's currency, so every report has cost. Entering the same days again replaces it.">
      <div className="flex flex-wrap items-end gap-2 text-sm">
        <label className="space-y-1">
          <span className="block text-xs text-muted-foreground">Campaign</span>
          <select aria-label="Cost campaign" className={field} value={campaign} onChange={(e) => setCampaign(e.target.value)}>
            <option value="">Choose a campaign</option>
            {campaigns.map((c) => (
              <option key={c.id} value={c.id}>{c.name}</option>
            ))}
          </select>
        </label>
        <label className="space-y-1">
          <span className="block text-xs text-muted-foreground">From</span>
          <input aria-label="Cost from" type="date" className={field} value={from} max={today} onChange={(e) => setFrom(e.target.value)} />
        </label>
        <label className="space-y-1">
          <span className="block text-xs text-muted-foreground">To</span>
          <input aria-label="Cost to" type="date" className={field} value={to} max={today} onChange={(e) => setTo(e.target.value)} />
        </label>
        <label className="space-y-1">
          <span className="block text-xs text-muted-foreground">Cost</span>
          <input aria-label="Cost amount" inputMode="decimal" className={`${field} w-28 font-mono`} placeholder="125.40" value={amount} onChange={(e) => setAmount(e.target.value)} />
        </label>
        <Button variant="primary" disabled={!ready || busy} onClick={() => void send([{ campaign_id: campaign, from, to, amount: value }])}>Save cost</Button>
        <label className="cursor-pointer rounded-md border px-3 py-1.5 hover:bg-muted" title="A file with campaign, from, to, amount on each line. The campaign is its name, code or id.">
          Upload CSV
          <input aria-label="Cost CSV" type="file" accept=".csv,text/csv,text/plain" className="sr-only" disabled={busy} onChange={(e) => {
              void upload(e.target.files?.[0]);
              e.target.value = "";
            }} />
        </label>
      </div>
      {error && <ErrorText>{error}</ErrorText>}
      {note && <p className="mt-2 text-sm text-muted-foreground">{note}</p>}
    </Card>
  );
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
    if (k.endsWith("_at") && typeof v === "string") return when(v);
    if (Array.isArray(v)) return v.length ? v.join(", ") : "—";
    if (typeof v === "boolean") return v ? "Yes" : "No";
    if (typeof v === "object") return JSON.stringify(v);
    return String(v);
  };
  return (
    <div className="space-y-4">
      <PageHeader title="Details" description="Every field recorded for this row." />
      {key && (
        <section className="rounded-lg border bg-card p-4">
          <div className="text-xs text-muted-foreground">Click id</div>
          <div className="mt-1 flex flex-wrap items-center gap-3">
            <span className="break-all font-mono text-sm" data-testid="detail-click">{key}</span>
            <CopyButton text={key} label="Copy" />
          </div>
        </section>
      )}
      <section className="rounded-lg border bg-card p-4">
        <dl className="grid gap-x-8 gap-y-3 text-sm sm:grid-cols-2 xl:grid-cols-3">
          {entries.map(([k, v]) => (
            <div key={k}>
              <dt className="text-xs capitalize text-muted-foreground">{(k.endsWith("_click_id") ? k : k.replace(/_id$/, "")).replace(/_/g, " ")}</dt>
              <dd className="break-all font-mono text-xs">{show(k, v)}</dd>
            </div>
          ))}
        </dl>
      </section>
    </div>
  );
}
