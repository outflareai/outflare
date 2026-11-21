// Tracker → Campaigns (plans/ad-tracker-ui.md B2): the brand's tracker
// campaigns with this week's numbers from the report, and the campaign
// editor as a full page with Settings · Landers and offers · Tracking parameters tabs and the
// campaign URL above them. A tracker campaign is a campaign link with
// routing (plan decision 4): its first redirect link carries the paths,
// landers, offers and weights.

import { useCallback, useEffect, useMemo, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { PickBrand } from "@/components/pick-brand";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { pickedBrandId, useBrand } from "@/lib/brand";
import { api, errorMessage } from "../api";
import { CurrencySelect } from "@/components/currency-select";
import { BackButton, Button, Card, confirmAction, CopyTextButton, Empty, ErrorText, field, Loading, Mono, PageHeader, toast } from "../ui";
import { presetLabels } from "./Funnels";
import { format, openReport, rangeOf, todayIn, tone, Unconverted } from "./TrackerReport";
import { Routing, type RoutingBackend } from "./TrackerRouting";
import type { Tab } from "../App";

type Campaign = Schemas["TrackingCampaign"];
type Link = Schemas["TrackingLink"];
type Source = Schemas["TrafficSource"];
type Flow = Schemas["TrackerFlow"];
type RedirectMode = Schemas["RedirectMode"];
type Metrics = Schemas["DrilldownMetrics"];

type Props = { ws: string; canManage: boolean; item?: string; go: (tab: Tab, item?: string) => void };

const redirectModes: { id: RedirectMode; label: string; help: string }[] = [
  { id: "302", label: "302", help: "Fastest; the landing page sees the referrer." },
  { id: "307", label: "307", help: "For networks that ask for a 307; otherwise the same as 302." },
  { id: "meta", label: "Meta refresh", help: "Hides the referrer; a moment slower." },
  { id: "double_meta", label: "Double meta refresh", help: "Hides the referrer from every browser; slowest." },
];

export function TrackerCampaigns(props: Props) {
  const { brand, active } = useBrand(props.ws);
  if (!brand) return <PickBrand what="tracker campaigns" brands={active.length} />;
  return props.item ? <Editor {...props} id={props.item} /> : <List {...props} tz={brand.timezone ?? "UTC"} />;
}

type Preset = keyof typeof presetLabels;
type SortKey = "name" | "source" | "visits" | "conversions" | "revenue" | "cost" | "profit" | "roi";
const columns: { key: SortKey; label: string; kind?: "count" | "money" | "percent"; title?: string }[] = [
  { key: "name", label: "Campaign" },
  { key: "source", label: "Traffic source" },
  { key: "visits", label: "Clicks", kind: "count" },
  { key: "conversions", label: "Conversions", kind: "count" },
  { key: "revenue", label: "Revenue", kind: "money" },
  { key: "cost", label: "Cost", kind: "money", title: "From each campaign's cost model." },
  { key: "profit", label: "Profit", kind: "money" },
  { key: "roi", label: "ROI", kind: "percent" },
];

// List: every campaign with a traffic source or redirect links, with its
// tracker clicks for the dates picked beside it (Voluum: every list is a
// report), sorted by any column.
function List({ canManage, go, tz }: Props & { tz: string }) {
  const [campaigns, setCampaigns] = useState<Campaign[] | null>(null);
  const [sources, setSources] = useState<Source[]>([]);
  const [numbers, setNumbers] = useState<Record<string, Metrics>>({});
  const [currency, setCurrency] = useState("USD");
  const [unconverted, setUnconverted] = useState<string[]>([]);
  const [error, setError] = useState("");
  const [creating, setCreating] = useState(false);
  const [status, setStatus] = useState<"active" | "archived">("active");
  const [preset, setPreset] = useState<Preset>("last_7_days");
  const [sort, setSort] = useState<{ key: SortKey; desc: boolean }>({ key: "visits", desc: true });
  useEffect(() => {
    const brand = pickedBrandId();
    const [from, to] = rangeOf(preset, todayIn(tz));
    let stale = false;
    void Promise.all([
      api.GET("/v1/brands/{brand}/tracking-campaigns", { params: { path: { brand }, query: { status } } }),
      api.GET("/v1/brands/{brand}/traffic-sources", { params: { path: { brand } } }),
      // The tracker's own clicks: the site report would count only visits
      // the site script saw.
      api.GET("/v1/brands/{brand}/drilldown", { params: { path: { brand }, query: { source: "tracker", from, to, group_by: "tracking_campaign", limit: 1000 } as never } }),
    ]).then(([c, s, r]) => {
      if (stale) return;
      if (c.error) return setError(errorMessage(c.error));
      setCampaigns(((c.data?.data ?? []) as Campaign[]).filter((x) => x.traffic_source_id || x.redirect_links));
      setSources((s.data?.data ?? []) as Source[]);
      const report = r.data as Schemas["DrilldownReport"] | undefined;
      const rows = (report?.rows ?? []) as { keys: string[]; metrics: Metrics }[];
      setNumbers(Object.fromEntries(rows.map((row) => [row.keys[0], row.metrics])));
      if (report?.currency) setCurrency(report.currency);
      setUnconverted(report?.unconverted_currencies ?? []);
    });
    return () => {
      stale = true;
    };
  }, [tz, status, preset]);
  const sourceName = useCallback((id: string | null) => sources.find((s) => s.id === id)?.name ?? "—", [sources]);
  const rows = useMemo(() => {
    const value = (c: Campaign): string | number | null => {
      if (sort.key === "name") return c.name.toLowerCase();
      if (sort.key === "source") return sourceName(c.traffic_source_id).toLowerCase();
      return numbers[c.id]?.[sort.key] ?? null;
    };
    return [...(campaigns ?? [])].sort((a, b) => {
      const x = value(a), y = value(b);
      // A campaign with no number goes last whichever way the column sorts.
      if (x === null || y === null) return x === y ? 0 : x === null ? 1 : -1;
      const d = typeof x === "string" ? x.localeCompare(y as string) : x - (y as number);
      return sort.desc ? -d : d;
    });
  }, [campaigns, numbers, sort, sourceName]);
  if (error) return <ErrorText>{error}</ErrorText>;
  if (!campaigns) return <Loading />;
  const live = sources.filter((s) => s.status === "active");
  const report = (c: Campaign) => {
    const q = new URLSearchParams({ source: "tracker", group_by: "offer" });
    if (preset !== "last_7_days") q.set("preset", preset);
    q.append("filter", `tracking_campaign:${c.id}`);
    openReport(q.toString());
    go("tracker");
  };
  const newButton = canManage && status === "active" && (
    <Button variant="primary" onClick={() => setCreating(true)}>New campaign</Button>
  );
  return (
    <div className="space-y-4">
      <PageHeader title="Campaigns" description="Your tracker campaigns: where each one's clicks go, and what they cost and made." action={newButton} />
      {creating && <NewCampaign sources={live} onCancel={() => setCreating(false)} onCreated={(c) => go("tracker-campaigns", c.id)} />}
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <select aria-label="Dates" className={field} value={preset} onChange={(e) => setPreset(e.target.value as Preset)}>
          {(Object.keys(presetLabels) as Preset[]).map((p) => (
            <option key={p} value={p}>{presetLabels[p]}</option>
          ))}
        </select>
        <label className="flex items-center gap-1.5 text-muted-foreground">
          <input type="checkbox" checked={status === "archived"} onChange={(e) => { setCampaigns(null); setStatus(e.target.checked ? "archived" : "active"); }} /> Show archived
        </label>
      </div>
      {unconverted.length > 0 && <Unconverted currencies={unconverted} to={currency} />}
      {campaigns.length === 0 && status === "archived" ? (
        <Empty title="No archived campaigns">A campaign you archive stops taking clicks and waits here until you restore it.</Empty>
      ) : campaigns.length === 0 ? (
        <Empty title="No tracker campaigns yet" action={!creating && newButton}>
          A tracker campaign gives one ad network a link that sends each click on by your rules: landers, offers and weights. It needs a traffic source and an offer first; Setup walks you through them.
          <div className="mt-2">
            <button className="underline" onClick={() => go("tracker-setup")}>Open Setup</button>
          </div>
        </Empty>
      ) : (
        <Card title={`${campaigns.length} ${status === "archived" ? "archived " : ""}campaign${campaigns.length === 1 ? "" : "s"}`}>
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="text-left text-xs text-muted-foreground">
                  {columns.map((col) => (
                    <th key={col.key} title={col.title} aria-sort={sort.key === col.key ? (sort.desc ? "descending" : "ascending") : "none"} className={`py-2 font-medium ${col.kind ? "px-3 text-right" : col.key === "name" ? "pr-3" : "px-3"}`}>
                      <button className="hover:text-foreground" onClick={() => setSort((x) => ({ key: col.key, desc: x.key === col.key ? !x.desc : !!col.kind }))}>
                        {col.label}
                        {sort.key === col.key ? (sort.desc ? " ↓" : " ↑") : ""}
                      </button>
                    </th>
                  ))}
                  <th className="py-2 pl-3" />
                </tr>
              </thead>
              <tbody>
                {rows.map((c) => {
                  const m = numbers[c.id];
                  return (
                    <tr key={c.id} className="cursor-pointer border-t hover:bg-muted/40" onClick={() => go("tracker-campaigns", c.id)}>
                      <td className="min-w-48 py-2 pr-3 font-medium">
                        <button className="text-left hover:underline" onClick={(e) => { e.stopPropagation(); go("tracker-campaigns", c.id); }}>{c.name}</button>
                      </td>
                      <td className="whitespace-nowrap px-3 py-2">{sourceName(c.traffic_source_id)}</td>
                      {columns.filter((col) => col.kind).map((col) => {
                        const key = col.key as "visits" | "conversions" | "revenue" | "cost" | "profit" | "roi";
                        // No clicks in the dates is a plain 0, never a blank.
                        const v = m ? m[key] : key === "visits" || key === "conversions" || key === "revenue" ? 0 : null;
                        return (
                          <td key={key} className={`px-3 py-2 text-right font-mono tabular-nums ${tone(key, v)}`}>{format(v, col.kind!, currency)}</td>
                        );
                      })}
                      <td className="py-2 pl-3 text-right">
                        <Button variant="ghost" aria-label={`Report for ${c.name}`} onClick={(e) => { e.stopPropagation(); report(c); }}>Report</Button>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </Card>
      )}
    </div>
  );
}

// NewCampaign: the traffic source, a name and the fallback page in Voluum's
// pattern, then the editor takes over.
function NewCampaign({ sources, onCancel, onCreated }: { sources: Source[]; onCancel: () => void; onCreated: (c: Campaign) => void }) {
  const [source, setSource] = useState(sources[0]?.id ?? "");
  const [name, setName] = useState("");
  const [url, setUrl] = useState("");
  const [busy, setBusy] = useState(false);
  const [tried, setTried] = useState(false);
  const picked = sources.find((s) => s.id === source);
  const placeholder = `${picked?.name ?? "Source"} - Global - `;
  const urlError = fallbackError(url);
  const create = async () => {
    setTried(true);
    if (urlError) return;
    setBusy(true);
    const body = { name: name.trim() || placeholder + new Date().toISOString().slice(0, 10), channel: "paid_ads" as const, source: (picked?.name ?? "tracker").toLowerCase().slice(0, 60), landing_url: url.trim(), redirect_links: true, traffic_source: source };
    const { data, error } = await api.POST("/v1/brands/{brand}/tracking-campaigns", { params: { path: { brand: pickedBrandId() } }, body });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    onCreated(data as Campaign);
  };
  return (
    <Card title="New campaign">
      <form className="grid gap-3 sm:grid-cols-3" onSubmit={(e) => { e.preventDefault(); void create(); }}>
        <label className="text-sm">
          Traffic source
          <select aria-label="Traffic source" className={`${field} mt-1 w-full`} value={source} onChange={(e) => setSource(e.target.value)}>
            <option value="">None</option>
            {sources.map((s) => (
              <option key={s.id} value={s.id}>{s.name}</option>
            ))}
          </select>
        </label>
        <label className="text-sm">
          Name
          <input aria-label="Name" className={`${field} mt-1 w-full`} maxLength={120} placeholder={placeholder} value={name} onChange={(e) => setName(e.target.value)} />
        </label>
        <label className="text-sm">
          Fallback page
          <input aria-label="Fallback page" aria-invalid={tried && !!urlError} className={`${field} mt-1 w-full`} maxLength={2000} placeholder="https://" value={url} onChange={(e) => setUrl(e.target.value)} />
          <span className={`mt-1 block text-xs ${tried && urlError ? "text-red-600 dark:text-red-400" : "text-muted-foreground"}`}>{tried && urlError ? urlError : fallbackHelp}</span>
        </label>
        <div className="flex gap-2 sm:col-span-3">
          <Button type="submit" variant="primary" disabled={busy}>Create and set up</Button>
          <Button type="button" variant="ghost" onClick={onCancel}>Cancel</Button>
        </div>
      </form>
    </Card>
  );
}

const fallbackHelp = "Where a click goes when no path or offer takes it. Every campaign needs one.";
// fallbackError says, in the field's own words, what is wrong with a
// fallback page; "" when it will do.
function fallbackError(url: string): string {
  const u = url.trim();
  if (!u) return "Add the page clicks fall back to, such as your offer or home page.";
  if (!/^https?:\/\/[^\s/]+\.[^\s/]+/i.test(u)) return "Use a full address that starts with http:// or https://";
  return "";
}

// Editor: one campaign, as a full page.
function Editor({ id, canManage, go }: Props & { id: string }) {
  const brand = pickedBrandId();
  const [campaign, setCampaign] = useState<Campaign | null>(null);
  const [link, setLink] = useState<Link | null>(null);
  const [sources, setSources] = useState<Source[]>([]);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    const [c, s] = await Promise.all([
      api.GET("/v1/brands/{brand}/tracking-campaigns/{id}", { params: { path: { brand, id } } }),
      api.GET("/v1/brands/{brand}/traffic-sources", { params: { path: { brand } } }),
    ]);
    if (c.error) return setError(errorMessage(c.error));
    setCampaign(c.data as Campaign);
    setSources((s.data?.data ?? []) as Source[]);
    // The campaign's routing lives on its first redirect link; make one
    // when it has none.
    const links = await api.GET("/v1/brands/{brand}/tracking-campaigns/{id}/links", { params: { path: { brand, id } } });
    let l = ((links.data?.data ?? []) as Link[]).find((x) => x.redirect_url);
    // A link needs somewhere to fall back to, so a campaign without a
    // fallback page gets its link once General has one.
    const c0 = c.data as Campaign;
    if (!l && canManage && c0.landing_url && c0.status === "active") {
      const made = await api.POST("/v1/brands/{brand}/tracking-campaigns/{id}/links", { params: { path: { brand, id } }, body: { redirect: true } });
      if (made.error) return setError(errorMessage(made.error));
      l = made.data as Link;
    }
    setLink(l ?? null);
  }, [brand, id, canManage]);
  useEffect(() => void load(), [load]);
  if (error) return <ErrorText>{error}</ErrorText>;
  if (!campaign) return <Loading />;
  const source = sources.find((s) => s.id === campaign.traffic_source_id);
  const url = link?.campaign_url ?? link?.redirect_url ?? "";
  const archived = campaign.status === "archived";
  const setStatus = async (status: "active" | "archived") => {
    if (status === "archived" && !(await confirmAction(`Archive ${campaign.name}?`, "Its link stops sending clicks on: they get the tracking domain's fallback page. Its numbers stay in the report, and you can restore it from the Campaigns list.", "Archive"))) return;
    const { data, error } = await api.PATCH("/v1/brands/{brand}/tracking-campaigns/{id}", { params: { path: { brand, id } }, body: { status } as never });
    if (error) return toast(errorMessage(error), "error");
    toast(status === "archived" ? "Campaign archived" : "Campaign restored", "ok");
    if (status === "archived") return go("tracker-campaigns");
    setCampaign(data as Campaign);
    void load();
  };
  const report = () => {
    openReport(new URLSearchParams([["source", "tracker"], ["group_by", "offer"], ["filter", `tracking_campaign:${campaign.id}`]]).toString());
    go("tracker");
  };
  const needsPage = !link && !campaign.landing_url;
  return (
    <div className="space-y-4">
      <BackButton to="campaigns" onClick={() => go("tracker-campaigns")} />
      <PageHeader
        title={<>{campaign.name}{archived && <span className="rounded-md border px-1.5 py-0.5 text-xs font-normal text-muted-foreground">Archived</span>}</>}
        description={source ? `Traffic from ${source.name}` : "No traffic source"}
        action={
          <>
            <Button variant="ghost" onClick={report}>View report</Button>
            {canManage && (archived ? <Button variant="ghost" onClick={() => void setStatus("active")}>Restore</Button> : <Button variant="danger" onClick={() => void setStatus("archived")}>Archive</Button>)}
          </>
        }
      />
      {needsPage && !archived && (
        <p className="rounded-md border border-amber-300 bg-amber-50 p-3 text-sm text-amber-900 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-200">
          This campaign has no fallback page yet, so it has no link. Add one under Settings and save: the campaign then gets its URL, and Landers and offers and Tracking parameters open.
        </p>
      )}
      <section aria-label="Campaign URL" className="rounded-lg border border-primary/40 bg-card p-4">
        <div className="flex flex-wrap items-baseline justify-between gap-x-4">
          <h2 className="text-sm font-semibold">Campaign URL</h2>
          <p className="text-xs text-muted-foreground">Paste this into the ad network as the ad's link. Every click on it is tracked and sent on to your landers and offers.</p>
        </div>
        {url ? (
          <div className="mt-2 flex flex-wrap items-center gap-2">
            <div className="min-w-0 flex-1 break-all rounded-md border bg-muted/40 px-3 py-2 font-mono text-sm" data-testid="campaign-url">{url}</div>
            <CopyTextButton text={url} label="Copy URL" variant="primary" />
          </div>
        ) : (
          <p className="mt-2 text-sm text-muted-foreground">{archived ? "An archived campaign has no link. Restore it to get one." : needsPage ? "Add a fallback page under Settings to get the link." : "The link is made when someone with edit rights opens the campaign."}</p>
        )}
      </section>
      <div>
        <Tabs defaultValue="general">
          {/* On a phone the three names are wider than the page: the row scrolls, the page doesn't. */}
          <div className="max-w-full overflow-x-auto">
            <TabsList>
              <TabsTrigger value="general">Settings</TabsTrigger>
              <TabsTrigger value="destination">Landers and offers</TabsTrigger>
              <TabsTrigger value="tracking">Tracking parameters</TabsTrigger>
            </TabsList>
          </div>
          <TabsContent value="general">
            <General campaign={campaign} sources={sources} canManage={canManage && !archived} onSaved={(c) => { setCampaign(c); if (!link) void load(); }} />
          </TabsContent>
          <TabsContent value="destination">{link ? <Destination campaign={campaign} link={link} canManage={canManage && !archived} /> : <NoLink archived={archived} />}</TabsContent>
          <TabsContent value="tracking">{link ? <Tracking source={source} /> : <NoLink archived={archived} />}</TabsContent>
        </Tabs>
      </div>
    </div>
  );
}

function General({ campaign, sources, canManage, onSaved }: { campaign: Campaign; sources: Source[]; canManage: boolean; onSaved: (c: Campaign) => void }) {
  const [name, setName] = useState(campaign.name);
  const [source, setSource] = useState(campaign.traffic_source_id ?? "");
  const [mode, setMode] = useState<RedirectMode>(campaign.redirect_mode);
  const [costModel, setCostModel] = useState(campaign.cost.model);
  const [amount, setAmount] = useState(campaign.cost.amount === "0" ? "" : campaign.cost.amount);
  const [share, setShare] = useState(campaign.cost.share ? String(campaign.cost.share) : "");
  const [costCurrency, setCostCurrency] = useState(campaign.cost.currency);
  const [page, setPage] = useState(campaign.landing_url);
  const [tried, setTried] = useState(false);
  const pageError = fallbackError(page);
  const [domains, setDomains] = useState<number | null>(null);
  useEffect(() => {
    api.GET("/v1/brands/{brand}/tracking-domains", { params: { path: { brand: pickedBrandId() } } }).then(({ data }) =>
      setDomains(((data?.data ?? []) as Schemas["TrackingDomain"][]).filter((d) => d.status === "verified").length),
    );
  }, []);
  const costDirty =
    costModel !== campaign.cost.model || amount !== (campaign.cost.amount === "0" ? "" : campaign.cost.amount) || share !== (campaign.cost.share ? String(campaign.cost.share) : "") || costCurrency !== campaign.cost.currency;
  const dirty = name !== campaign.name || source !== (campaign.traffic_source_id ?? "") || mode !== campaign.redirect_mode || page !== campaign.landing_url || costDirty;
  // The campaign's own source stays in the list even once archived, so
  // opening the form never drops it.
  const reset = () => {
    setName(campaign.name);
    setSource(campaign.traffic_source_id ?? "");
    setMode(campaign.redirect_mode);
    setCostModel(campaign.cost.model);
    setAmount(campaign.cost.amount === "0" ? "" : campaign.cost.amount);
    setShare(campaign.cost.share ? String(campaign.cost.share) : "");
    setCostCurrency(campaign.cost.currency);
    setPage(campaign.landing_url);
    setTried(false);
  };
  const pickable = sources.filter((s) => s.status === "active" || s.id === campaign.traffic_source_id);
  const save = async () => {
    setTried(true);
    if (pageError) return;
    const cost: Record<string, unknown> = { model: costModel, currency: costCurrency };
    if (costModel === "cpc" || costModel === "cpa") cost.amount = amount || "0";
    if (costModel === "revshare") cost.share = Number(share) || 0;
    const { data, error } = await api.PATCH("/v1/brands/{brand}/tracking-campaigns/{id}", {
      params: { path: { brand: pickedBrandId(), id: campaign.id } },
      body: { name, traffic_source: source, redirect_mode: mode, ...(page !== campaign.landing_url ? { landing_url: page.trim() } : {}), ...(costDirty ? { cost } : {}) } as never,
    });
    if (error) return toast(errorMessage(error), "error");
    toast("Campaign saved", "ok");
    onSaved(data as Campaign);
  };
  return (
    <Card title="Settings">
      <div className="grid items-start gap-x-8 gap-y-5 lg:grid-cols-2">
        <label className="block text-sm">
          Traffic source
          <select aria-label="Traffic source" className={`${field} mt-1 w-full`} value={source} disabled={!canManage} onChange={(e) => setSource(e.target.value)}>
            <option value="">None</option>
            {pickable.map((s) => (
              <option key={s.id} value={s.id}>{s.name}{s.status === "archived" ? " (archived)" : ""}</option>
            ))}
          </select>
        </label>
        <label className="block text-sm">
          Name
          <input aria-label="Name" className={`${field} mt-1 w-full`} maxLength={120} value={name} disabled={!canManage} onChange={(e) => setName(e.target.value)} />
        </label>
        <label className="block text-sm">
          Fallback page
          <input aria-label="Fallback page" aria-invalid={tried && !!pageError} className={`${field} mt-1 w-full`} maxLength={2000} placeholder="https://" value={page} disabled={!canManage} onChange={(e) => setPage(e.target.value)} />
          <span className={`mt-1 block text-xs ${tried && pageError ? "text-red-600 dark:text-red-400" : "text-muted-foreground"}`}>{tried && pageError ? pageError : fallbackHelp}</span>
        </label>
        <div className="text-sm">
          Tracking domain
          {domains === 0 ? (
            <p className="mt-1 text-amber-700 dark:text-amber-400">Tracker links run only on your own domain: verify one in Domains, and its links take over.</p>
          ) : (
            <p className="mt-1 text-muted-foreground">Your verified tracking domain{domains === 1 ? "" : "s"} serve{domains === 1 ? "s" : ""} this campaign's links.</p>
          )}
        </div>
        <fieldset className="text-sm">
          <legend>Cost model</legend>
          <span className="block text-xs text-muted-foreground">How the tracker report works out what this campaign's clicks cost.</span>
          <div className="mt-1 flex flex-wrap items-center gap-2">
            <select aria-label="Cost model" className={field} value={costModel} disabled={!canManage} onChange={(e) => setCostModel(e.target.value as Campaign["cost"]["model"])}>
              <option value="token">Reported by the traffic source ({"{cost}"})</option>
              <option value="cpc">CPC: per click</option>
              <option value="cpa">CPA: per conversion</option>
              <option value="revshare">Revshare: share of the payout</option>
            </select>
            {(costModel === "cpc" || costModel === "cpa") && (
              <>
                <input aria-label="Cost amount" inputMode="decimal" className={`${field} w-28`} placeholder="0.05" value={amount} disabled={!canManage} onChange={(e) => setAmount(e.target.value)} />
                <CurrencySelect label="Cost currency" empty="The source's currency" value={costCurrency} disabled={!canManage} onChange={setCostCurrency} />
              </>
            )}
            {costModel === "revshare" && (
              <span className="flex items-center gap-1">
                <input aria-label="Revenue share" inputMode="decimal" className={`${field} w-20`} placeholder="50" value={share} disabled={!canManage} onChange={(e) => setShare(e.target.value)} /> % of each payout
              </span>
            )}
          </div>
          {costModel === "token" && <span className="mt-1 block text-xs text-muted-foreground">Needs the traffic source's cost parameter in the campaign URL (Tracking parameters tab).</span>}
        </fieldset>
        <fieldset className="text-sm lg:col-span-2">
          <legend>Redirect mode</legend>
          <div className="mt-1 grid gap-2 sm:grid-cols-2 xl:grid-cols-4">
            {redirectModes.map((m) => (
              <label key={m.id} className={`flex cursor-pointer gap-2 rounded-md border p-2 ${mode === m.id ? "border-primary" : ""}`}>
                <input type="radio" name="redirect-mode" value={m.id} checked={mode === m.id} disabled={!canManage} onChange={() => setMode(m.id)} />
                <span>
                  <b>{m.label}</b>
                  <span className="block text-xs text-muted-foreground">{m.help}</span>
                </span>
              </label>
            ))}
          </div>
        </fieldset>
        {canManage && (
          <div className="flex gap-2 lg:col-span-2">
            <Button variant="primary" disabled={!dirty} onClick={() => void save()}>
              {dirty ? "Save" : "Saved"}
            </Button>
            {dirty && <Button variant="ghost" onClick={reset}>Cancel</Button>}
          </div>
        )}
      </div>
    </Card>
  );
}

// Destination: a flow, or the campaign's own path cards.
function Destination({ campaign, link, canManage }: { campaign: Campaign; link: Link; canManage: boolean }) {
  const brand = pickedBrandId();
  const [flows, setFlows] = useState<Flow[]>([]);
  const [flowId, setFlowId] = useState<string | null | undefined>(undefined);
  const backend = useMemo(() => linkRouting(brand, campaign.id, link.id), [brand, campaign.id, link.id]);
  useEffect(() => {
    const path = { brand, id: campaign.id, link: link.id };
    void Promise.all([
      api.GET("/v1/brands/{brand}/flows", { params: { path: { brand } } }),
      api.GET("/v1/brands/{brand}/tracking-campaigns/{id}/links/{link}/flow", { params: { path } }),
    ]).then(([f, lf]) => {
      setFlows((f.data?.data ?? []) as Flow[]);
      setFlowId((lf.data as Schemas["TrackingLinkFlow"] | undefined)?.flow_id ?? null);
    });
  }, [brand, campaign.id, link.id]);
  const setFlow = async (v: string) => {
    const { error } = await api.PUT("/v1/brands/{brand}/tracking-campaigns/{id}/links/{link}/flow", { params: { path: { brand, id: campaign.id, link: link.id } }, body: { flow_id: v || null } });
    if (error) return toast(errorMessage(error), "error");
    setFlowId(v || null);
  };
  if (flowId === undefined) return <Loading />;
  const flow = flows.find((f) => f.id === flowId);
  return (
    <div className="space-y-4">
      <Card title="Where clicks go">
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <label className="flex items-center gap-2">
            <input type="radio" name="dest-kind" checked={!flowId} disabled={!canManage} onChange={() => void setFlow("")} /> Paths for this campaign
          </label>
          <label className="flex items-center gap-2">
            <input type="radio" name="dest-kind" checked={!!flowId} disabled={!canManage || flows.length === 0} onChange={() => flows[0] && void setFlow(flows[0].id)} /> Use a flow
          </label>
          {flowId && (
            <select aria-label="Flow" className={field} value={flowId} disabled={!canManage} onChange={(e) => void setFlow(e.target.value)}>
              {flows.map((f) => (
                <option key={f.id} value={f.id}>{f.name}</option>
              ))}
            </select>
          )}
        </div>
        {flow && <p className="mt-2 text-sm text-muted-foreground">Clicks follow the flow "{flow.name}", shared with {flow.links - 1 > 0 ? `${flow.links - 1} other link${flow.links - 1 === 1 ? "" : "s"}` : "no other link"}. Edit it on the Flows tab.</p>}
      </Card>
      {!flowId && <Routing backend={backend} canManage={canManage} />}
    </div>
  );
}

// linkRouting loads and saves one campaign link's own routing.
function linkRouting(brand: string, id: string, link: string): RoutingBackend {
  const path = { brand, id, link };
  return {
    load: async () => {
      const [d, p, sg] = await Promise.all([
        api.GET("/v1/brands/{brand}/tracking-campaigns/{id}/links/{link}/destinations", { params: { path } }),
        api.GET("/v1/brands/{brand}/tracking-campaigns/{id}/links/{link}/paths", { params: { path } }),
        api.GET("/v1/brands/{brand}/tracking-campaigns/{id}/links/{link}/weight-suggestions", { params: { path } }),
      ]);
      const dd = d.data as Schemas["TrackingLinkDestinations"] | undefined;
      return { error: d.error ?? p.error, destinations: dd?.destinations ?? [], paths: (p.data as Schemas["TrackingLinkPaths"] | undefined)?.paths ?? [], needsDomain: !!dd?.needs_domain, suggestions: (sg.data as Schemas["TrackerWeightSuggestions"] | undefined) ?? null };
    },
    save: async (destinations, paths) => {
      const r1 = await api.PUT("/v1/brands/{brand}/tracking-campaigns/{id}/links/{link}/destinations", { params: { path }, body: { destinations } as never });
      if (r1.error) return r1.error;
      return (await api.PUT("/v1/brands/{brand}/tracking-campaigns/{id}/links/{link}/paths", { params: { path }, body: { paths } as never })).error;
    },
    apply: async (weights) => (await api.PATCH("/v1/brands/{brand}/tracking-campaigns/{id}/links/{link}/weights", { params: { path }, body: { source: "suggestion", weights } })).error,
  };
}

// NoLink stands in for the tabs that need the campaign's link.
function NoLink({ archived }: { archived: boolean }) {
  return <Empty title="No link yet">{archived ? "Restore the campaign to route its clicks." : "Add a fallback page under Settings and save. The campaign's link is made then."}</Empty>;
}

function Tracking({ source }: { source?: Source }) {
  const params = useMemo(() => source?.parameters ?? [], [source]);
  return (
    <div className="space-y-4">
      <Card title="Parameters" description={source ? `What ${source.name} fills in on each click, and where it lands.` : "Pick a traffic source on General to carry its click id, cost and parameters."}>
        {source && (
          <table className="w-full text-sm">
            <thead>
              <tr className="text-left text-xs text-muted-foreground">
                <th className="py-1 font-medium">Parameter</th>
                <th className="py-1 font-medium">Token</th>
                <th className="py-1 font-medium">Lands in</th>
              </tr>
            </thead>
            <tbody>
              {source.click_id.param && (
                <tr className="border-t"><td className="py-1"><Mono value={source.click_id.param} /></td><td><Mono value={source.click_id.token} /></td><td>Click id</td></tr>
              )}
              {source.cost.param && (
                <tr className="border-t"><td className="py-1"><Mono value={source.cost.param} /></td><td><Mono value={source.cost.token} /></td><td>Cost</td></tr>
              )}
              {params.map((p) => (
                <tr key={p.param} className="border-t"><td className="py-1"><Mono value={p.param} /></td><td><Mono value={p.token} /></td><td>{p.name} (field {p.field})</td></tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>
      {source?.postback?.url && (
        <Card title={`Postback to ${source.name}`} description="Outflare calls this when a conversion comes in, with each {token} replaced by that click's value.">
          <div className="break-all rounded-md border bg-muted/40 p-2 font-mono text-xs">{source.postback.url}</div>
        </Card>
      )}
    </div>
  );
}
