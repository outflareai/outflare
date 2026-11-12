// Tracker → Campaigns (plans/ad-tracker-ui.md B2): the brand's tracker
// campaigns with this week's numbers from the report, and the campaign
// editor as a full page with General · Destination · Tracking tabs and the
// campaign URL beside them. A tracker campaign is a campaign link with
// routing (plan decision 4): its first redirect link carries the paths,
// landers, offers and weights.

import { useCallback, useEffect, useMemo, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { PickBrand } from "@/components/pick-brand";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { pickedBrandId, useBrand } from "@/lib/brand";
import { api, errorMessage } from "../api";
import { Button, Card, CopyButton, Empty, ErrorText, field, Loading, Mono, PageHeader, toast } from "../ui";
import { format, rangeOf, todayIn } from "./TrackerReport";
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
  { id: "307", label: "307", help: "Like 302, and keeps a POST a POST." },
  { id: "meta", label: "Meta refresh", help: "Hides the referrer; a moment slower." },
  { id: "double_meta", label: "Double meta refresh", help: "Hides the referrer from every browser; slowest." },
];

export function TrackerCampaigns(props: Props) {
  const { brand, active } = useBrand(props.ws);
  if (!brand) return <PickBrand what="tracker campaigns" brands={active.length} />;
  return props.item ? <Editor {...props} id={props.item} /> : <List {...props} tz={brand.timezone ?? "UTC"} />;
}

// List: every campaign with a traffic source or redirect links, with the
// last 7 days from the report beside it (Voluum: every list is a report).
function List({ canManage, go, tz }: Props & { tz: string }) {
  const [campaigns, setCampaigns] = useState<Campaign[] | null>(null);
  const [sources, setSources] = useState<Source[]>([]);
  const [numbers, setNumbers] = useState<Record<string, Metrics>>({});
  const [error, setError] = useState("");
  const [creating, setCreating] = useState(false);
  useEffect(() => {
    const brand = pickedBrandId();
    const [from, to] = rangeOf("last_7_days", todayIn(tz));
    void Promise.all([
      api.GET("/v1/brands/{brand}/tracking-campaigns", { params: { path: { brand }, query: { status: "active" } } }),
      api.GET("/v1/brands/{brand}/traffic-sources", { params: { path: { brand } } }),
      api.GET("/v1/brands/{brand}/drilldown", { params: { path: { brand }, query: { from, to, group_by: "tracking_campaign", limit: 1000 } as never } }),
    ]).then(([c, s, r]) => {
      if (c.error) return setError(errorMessage(c.error));
      setCampaigns(((c.data?.data ?? []) as Campaign[]).filter((x) => x.traffic_source_id || x.redirect_links));
      setSources((s.data?.data ?? []) as Source[]);
      const rows = ((r.data as Schemas["DrilldownReport"] | undefined)?.rows ?? []) as { keys: string[]; metrics: Metrics }[];
      setNumbers(Object.fromEntries(rows.map((row) => [row.keys[0], row.metrics])));
    });
  }, [tz]);
  const sourceName = (id: string | null) => sources.find((s) => s.id === id)?.name ?? "—";
  if (error) return <ErrorText>{error}</ErrorText>;
  if (!campaigns) return <Loading />;
  return (
    <div className="space-y-4">
      <PageHeader
        title="Campaigns"
        description="Your tracker campaigns: where each one's clicks go, and what they made in the last 7 days."
        action={canManage && <Button variant="primary" onClick={() => setCreating(true)}>New campaign</Button>}
      />
      {creating && <NewCampaign sources={sources} onCancel={() => setCreating(false)} onCreated={(c) => go("tracker-campaigns", c.id)} />}
      {campaigns.length === 0 ? (
        <Empty title="No tracker campaigns yet">A tracker campaign gives one ad network a link that sends each click on by your rules: landers, offers and weights.</Empty>
      ) : (
        <Card title={`${campaigns.length} campaign${campaigns.length === 1 ? "" : "s"}`}>
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="text-left text-xs text-muted-foreground">
                  <th className="py-2 pr-3 font-medium">Campaign</th>
                  <th className="px-3 py-2 font-medium">Traffic source</th>
                  {["Visits", "Conversions", "Revenue", "Profit", "ROI"].map((h) => (
                    <th key={h} className="px-3 py-2 text-right font-medium">{h}</th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {campaigns.map((c) => {
                  const m = numbers[c.id];
                  return (
                    <tr key={c.id} className="cursor-pointer border-t hover:bg-muted/40" onClick={() => go("tracker-campaigns", c.id)}>
                      <td className="py-2 pr-3 font-medium">{c.name}</td>
                      <td className="px-3 py-2">{sourceName(c.traffic_source_id)}</td>
                      <td className="px-3 py-2 text-right font-mono tabular-nums">{format(m?.visits ?? 0, "count", "USD")}</td>
                      <td className="px-3 py-2 text-right font-mono tabular-nums">{format(m?.conversions ?? 0, "count", "USD")}</td>
                      <td className="px-3 py-2 text-right font-mono tabular-nums">{format(m?.revenue ?? 0, "money", "USD")}</td>
                      <td className="px-3 py-2 text-right font-mono tabular-nums">{format(m?.profit, "money", "USD")}</td>
                      <td className="px-3 py-2 text-right font-mono tabular-nums">{format(m?.roi, "percent", "USD")}</td>
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

// NewCampaign: the traffic source and a name in Voluum's pattern, then the
// editor takes over.
function NewCampaign({ sources, onCancel, onCreated }: { sources: Source[]; onCancel: () => void; onCreated: (c: Campaign) => void }) {
  const [source, setSource] = useState(sources[0]?.id ?? "");
  const [name, setName] = useState("");
  const [url, setUrl] = useState("");
  const [busy, setBusy] = useState(false);
  const picked = sources.find((s) => s.id === source);
  const placeholder = `${picked?.name ?? "Source"} - Global - `;
  const create = async () => {
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
          <input aria-label="Name" className={`${field} mt-1 w-full`} placeholder={placeholder} value={name} onChange={(e) => setName(e.target.value)} />
        </label>
        <label className="text-sm">
          Landing page
          <input aria-label="Landing page" className={`${field} mt-1 w-full`} placeholder="https://" value={url} onChange={(e) => setUrl(e.target.value)} />
        </label>
        <div className="flex gap-2 sm:col-span-3">
          <Button type="submit" variant="primary" disabled={busy}>Create and set up</Button>
          <Button type="button" variant="ghost" onClick={onCancel}>Cancel</Button>
        </div>
      </form>
    </Card>
  );
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
    if (!l && canManage) {
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
  return (
    <div className="space-y-4">
      <PageHeader title={campaign.name} description={source ? `Traffic from ${source.name}` : "No traffic source"} back={{ label: "Campaigns", onClick: () => go("tracker-campaigns") }} />
      <div className="grid gap-4 lg:grid-cols-[1fr_320px]">
        <Tabs defaultValue="general">
          <TabsList>
            <TabsTrigger value="general">General</TabsTrigger>
            <TabsTrigger value="destination">Destination</TabsTrigger>
            <TabsTrigger value="tracking">Tracking</TabsTrigger>
          </TabsList>
          <TabsContent value="general">
            <General campaign={campaign} sources={sources} canManage={canManage} onSaved={setCampaign} />
          </TabsContent>
          <TabsContent value="destination">{link ? <Destination campaign={campaign} link={link} canManage={canManage} /> : <Loading />}</TabsContent>
          <TabsContent value="tracking">{link ? <Tracking link={link} source={source} /> : <Loading />}</TabsContent>
        </Tabs>
        <aside className="lg:sticky lg:top-4 lg:self-start">
          <Card title="Campaign URL" description="Paste this into the ad network as the ad's link.">
            {url ? (
              <div className="space-y-2">
                <div className="break-all rounded-md border bg-muted/40 p-2 font-mono text-xs" data-testid="campaign-url">{url}</div>
                <CopyButton text={url} label="Copy" />
              </div>
            ) : (
              <p className="text-sm text-muted-foreground">The link is made when you open the campaign with edit rights.</p>
            )}
          </Card>
        </aside>
      </div>
    </div>
  );
}

function General({ campaign, sources, canManage, onSaved }: { campaign: Campaign; sources: Source[]; canManage: boolean; onSaved: (c: Campaign) => void }) {
  const [name, setName] = useState(campaign.name);
  const [source, setSource] = useState(campaign.traffic_source_id ?? "");
  const [mode, setMode] = useState<RedirectMode>(campaign.redirect_mode);
  const [domains, setDomains] = useState<number | null>(null);
  useEffect(() => {
    api.GET("/v1/brands/{brand}/tracking-domains", { params: { path: { brand: pickedBrandId() } } }).then(({ data }) =>
      setDomains(((data?.data ?? []) as Schemas["TrackingDomain"][]).filter((d) => d.status === "verified").length),
    );
  }, []);
  const dirty = name !== campaign.name || source !== (campaign.traffic_source_id ?? "") || mode !== campaign.redirect_mode;
  const save = async () => {
    const { data, error } = await api.PATCH("/v1/brands/{brand}/tracking-campaigns/{id}", {
      params: { path: { brand: pickedBrandId(), id: campaign.id } },
      body: { name, traffic_source: source, redirect_mode: mode },
    });
    if (error) return toast(errorMessage(error), "error");
    toast("Campaign saved", "ok");
    onSaved(data as Campaign);
  };
  return (
    <Card title="General">
      <div className="space-y-4">
        <label className="block text-sm">
          Traffic source
          <select aria-label="Traffic source" className={`${field} mt-1 w-full`} value={source} disabled={!canManage} onChange={(e) => setSource(e.target.value)}>
            <option value="">None</option>
            {sources.map((s) => (
              <option key={s.id} value={s.id}>{s.name}</option>
            ))}
          </select>
        </label>
        <label className="block text-sm">
          Name
          <input aria-label="Name" className={`${field} mt-1 w-full`} value={name} disabled={!canManage} onChange={(e) => setName(e.target.value)} />
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
          <legend>Redirect mode</legend>
          <div className="mt-1 grid gap-2 sm:grid-cols-2">
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
          <Button variant="primary" disabled={!dirty} onClick={() => void save()}>
            {dirty ? "Save" : "Saved"}
          </Button>
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

function Tracking({ link, source }: { link: Link; source?: Source }) {
  const params = useMemo(() => source?.parameters ?? [], [source]);
  return (
    <div className="space-y-4">
      <Card title="Campaign URL">
        <div className="space-y-2">
          <div className="break-all rounded-md border bg-muted/40 p-2 font-mono text-sm">{link.campaign_url ?? link.redirect_url ?? "—"}</div>
          {link.campaign_url && <CopyButton text={link.campaign_url} label="Copy" />}
        </div>
      </Card>
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
        <Card title={`Postback to ${source.name}`} description="Outflare calls this when a conversion comes in, with the network's own tokens filled.">
          <div className="break-all rounded-md border bg-muted/40 p-2 font-mono text-xs">{source.postback.url}</div>
        </Card>
      )}
    </div>
  );
}
