// Tracker → Setup (plans/ad-tracker-ui.md B5): the brand's conversion
// tracking checklist, ticked from its own data (Voluum's and ClickFlare's
// checklists); the postback URL builder for an affiliate network, in its
// own tokens, with the image pixel, script and server call it implies; and a
// test conversion whose outcome shows inline with its reason.

import { useCallback, useEffect, useMemo, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { PickBrand } from "@/components/pick-brand";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { pickedBrandId, useBrand } from "@/lib/brand";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, confirmAction, CopyButton, ErrorText, field, Loading, PageHeader, toast } from "../ui";
import type { Tab } from "../App";

type Network = Schemas["PostbackSource"];
type NetworkTemplate = Schemas["AffiliateNetworkTemplate"];
type Upload = Schemas["TrackerConversionUpload"];
type Conversion = Schemas["TrackerConversion"];

type Props = { ws: string; canManage: boolean; go: (tab: Tab, item?: string) => void };

type Step = { id: string; title: string; done: boolean; help: string; tab?: Tab; action: string };

// The parameters a postback takes (docs/campaign-links.md, "Conversions by
// click id"), with the template macro each is prefilled from.
export const postbackParams: { name: string; label: string; macro: string }[] = [
  { name: "cid", label: "Click id", macro: "cid" },
  { name: "payout", label: "Payout", macro: "payout" },
  { name: "currency", label: "Currency", macro: "currency" },
  { name: "txid", label: "Transaction id", macro: "txid" },
  { name: "status", label: "Status", macro: "status" },
  { name: "type", label: "Conversion type", macro: "type" },
  ...Array.from({ length: 20 }, (_, i) => ({ name: `p${i + 1}`, label: `Parameter ${i + 1}`, macro: "" })),
];

type Row = { on: boolean; token: string };

// postbackURL builds the URL to give the network: its secret path on the
// tracking domain, then each parameter that is on, in the network's tokens.
export function postbackURL(domain: string, secret: string, rows: Record<string, Row>): string {
  const q = postbackParams.filter((p) => rows[p.name]?.on && rows[p.name].token).map((p) => `${p.name}=${rows[p.name].token}`);
  return `https://${domain || "<your tracking domain>"}/t/${secret || "<secret>"}${q.length ? `?${q.join("&")}` : ""}`;
}

// secretOf takes the secret out of a postback URL Outflare returned.
export function secretOf(url: string): string {
  return /\/t\/([^/?#]+)/.exec(url)?.[1] ?? "";
}

const skipKey = (brand: string) => `outflare.tracker-setup.skipped.${brand}`;
function readSkipped(brand: string): string[] {
  try {
    return JSON.parse(localStorage.getItem(skipKey(brand)) ?? "[]") as string[];
  } catch {
    return [];
  }
}
function writeSkipped(brand: string, ids: string[]) {
  try {
    localStorage.setItem(skipKey(brand), JSON.stringify(ids));
  } catch {
    // The checklist works without storage; a skip just won't last.
  }
}

export function TrackerSetup({ ws, canManage, go }: Props) {
  const { brand, active } = useBrand(ws);
  const [steps, setSteps] = useState<Step[] | null>(null);
  const [domains, setDomains] = useState<string[]>([]);
  const [networks, setNetworks] = useState<Network[]>([]);
  const [templates, setTemplates] = useState<NetworkTemplate[]>([]);
  const [converted, setConverted] = useState(false);
  const [campaignNames, setCampaignNames] = useState<Record<string, string>>({});
  const [skipped, setSkipped] = useState<string[]>([]);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    const b = pickedBrandId();
    const path = { path: { brand: b } };
    const [d, s, o, c, n, t, conv] = await Promise.all([
      api.GET("/v1/brands/{brand}/tracking-domains", { params: path }),
      api.GET("/v1/brands/{brand}/traffic-sources", { params: path }),
      api.GET("/v1/brands/{brand}/offers", { params: path }),
      api.GET("/v1/brands/{brand}/tracking-campaigns", { params: { ...path, query: { status: "active" } } }),
      api.GET("/v1/brands/{brand}/postback-sources", { params: path }),
      api.GET("/v1/brands/{brand}/affiliate-network-templates", { params: path }),
      api.GET("/v1/brands/{brand}/tracker-conversions", { params: path }),
    ]);
    if (d.error) return setError(errorMessage(d.error));
    const verified = ((d.data?.data ?? []) as Schemas["TrackingDomain"][]).filter((x) => x.status === "verified").map((x) => x.domain);
    const offers = (o.data?.data ?? []) as Schemas["TrackerOffer"][];
    const campaigns = ((c.data?.data ?? []) as Schemas["TrackingCampaign"][]).filter((x) => x.traffic_source_id || x.redirect_links);
    const conversions = (conv.data?.data ?? []) as Conversion[];
    setDomains(verified);
    setCampaignNames(Object.fromEntries(campaigns.map((x) => [x.id, x.name])));
    setNetworks((n.data?.data ?? []) as Network[]);
    setTemplates((t.data?.data ?? []) as NetworkTemplate[]);
    setConverted(conversions.some((x) => x.type !== "test"));
    setSkipped(readSkipped(b));
    setSteps([
      { id: "domain", title: "Verify a tracking domain", done: verified.length > 0, help: "Tracker links, postbacks and click URLs answer only on your own domain.", tab: "domains", action: "Open Domains" },
      { id: "source", title: "Add a traffic source", done: ((s.data?.data ?? []) as unknown[]).length > 0, help: "The ad network you buy clicks from, with its click id and cost.", tab: "tracker-sources", action: "Add one" },
      { id: "offer", title: "Add an offer that carries the click id", done: offers.some((x) => !x.missing_click_id), help: "So the network's postback can name the click.", tab: "tracker-offers", action: "Add one" },
      { id: "campaign", title: "Create a campaign and copy its URL", done: campaigns.length > 0, help: "Paste its campaign URL into the ad network as the ad's link.", tab: "tracker-campaigns", action: "Create one" },
      { id: "click", title: "Click your campaign URL", done: campaigns.some((x) => x.clicks > 0), help: "Open it yourself once: the click id lands in the offer's URL, ready for the test below.", tab: "tracker-campaigns", action: "Open Campaigns" },
      { id: "conversion", title: "Send a test conversion", done: conversions.length > 0, help: "Fire one below with the click id from your test click." , action: "Test below" },
    ]);
  }, []);
  useEffect(() => {
    if (brand) void load();
  }, [brand, load]);
  if (!brand) return <PickBrand what="tracker setup" brands={active.length} />;
  if (error) return <ErrorText>{error}</ErrorText>;
  if (!steps) return <Loading />;
  const skip = (id: string) => {
    const next = [...skipped, id];
    setSkipped(next);
    writeSkipped(pickedBrandId(), next);
  };
  const doneCount = steps.filter((s) => s.done || skipped.includes(s.id)).length;
  return (
    <div className="space-y-4">
      <PageHeader title="Setup" description="Get conversions from your affiliate networks onto the right clicks, step by step." action={<Badge tone={doneCount === steps.length ? "green" : "blue"}>Setup {doneCount}/{steps.length}</Badge>} />
      {converted ? (
        <Card title="You're tracking conversions">
          <p className="text-sm text-muted-foreground">Your first real conversion is in, so the checklist is done. The builder and test below stay here for your next network.</p>
        </Card>
      ) : (
        <Card title="Checklist" description="Each step ticks itself when your data shows it's done.">
          <ol className="space-y-2" data-testid="checklist">
            {steps.map((s, i) => {
              const isSkipped = !s.done && skipped.includes(s.id);
              return (
                <li key={s.id} className="flex flex-wrap items-start gap-3 rounded-md border p-3">
                  <span aria-hidden className={`mt-0.5 inline-flex size-6 shrink-0 items-center justify-center rounded-full border text-xs ${s.done ? "border-green-600 bg-green-600 text-white" : ""}`}>{s.done ? "✓" : i + 1}</span>
                  <span className="min-w-0 flex-1">
                    <span className={`block text-sm font-medium ${s.done || isSkipped ? "text-muted-foreground" : ""}`}>
                      {s.title} {s.done && <span className="sr-only">(done)</span>} {isSkipped && <Badge tone="grey">Skipped</Badge>}
                    </span>
                    <span className="block text-xs text-muted-foreground">{s.help}</span>
                  </span>
                  {!s.done && !isSkipped && (
                    <span className="flex gap-1">
                      {s.tab && <Button variant="ghost" onClick={() => go(s.tab!)}>{s.action}</Button>}
                      <Button variant="ghost" aria-label={`Skip ${s.title}`} onClick={() => skip(s.id)}>Skip</Button>
                    </span>
                  )}
                </li>
              );
            })}
          </ol>
        </Card>
      )}
      {networks.length === 0 ? (
        <Card title="Postback URL">
          <p className="text-sm text-muted-foreground">
            Add the affiliate network that pays you first: its postback URL is built here in its own tokens.{" "}
            <button type="button" className="underline" onClick={() => go("tracker-networks")}>Add a network</button>
          </p>
        </Card>
      ) : (
        <Builder networks={networks} templates={templates} domains={domains} canManage={canManage} />
      )}
      <ClickFilters canManage={canManage} />
      <IPLookups ws={ws} canManage={canManage} go={go} />
      {networks.length > 0 && <TestConversion networks={networks} campaignNames={campaignNames} canManage={canManage} onDone={() => void load()} />}
    </div>
  );
}

function Builder({ networks, templates, domains, canManage }: { networks: Network[]; templates: NetworkTemplate[]; domains: string[]; canManage: boolean }) {
  const [networkId, setNetworkId] = useState(networks[0].id);
  const network = networks.find((n) => n.id === networkId) ?? networks[0];
  const template = templates.find((t) => t.id === network.network);
  const [domain, setDomain] = useState(domains[0] ?? "");
  const [secret, setSecret] = useState("");
  const [show, setShow] = useState(false);
  const [more, setMore] = useState(false);
  const initial = useCallback((): Record<string, Row> => {
    const macros = template?.macros ?? {};
    return Object.fromEntries(postbackParams.map((p) => {
      const custom = /^p\d+$/.test(p.name) ? "" : `{${p.name === "cid" ? "click_id" : p.name}}`;
      const token = (p.macro && macros[p.macro]) || custom;
      return [p.name, { on: ["cid", "payout", "txid", "status"].includes(p.name) || !!(p.macro && macros[p.macro]), token }];
    }));
  }, [template]);
  const [rows, setRows] = useState<Record<string, Row>>(initial);
  useEffect(() => {
    setRows(initial());
    setSecret("");
  }, [initial, networkId]);
  const url = postbackURL(domain, secret, rows);
  const json = useMemo(() => {
    const body = Object.fromEntries(postbackParams.filter((p) => rows[p.name]?.on && rows[p.name].token).map((p) => [p.name, rows[p.name].token]));
    return JSON.stringify({ url: url.split("?")[0], method: "POST", body }, null, 2);
  }, [rows, url]);
  const rotate = async () => {
    if (!(await confirmAction(`Make a new postback URL for ${network.name}?`, "The URL the network has now stops working at once: paste the new one into the network straight away.", "Make a new URL"))) return;
    const { data, error } = await api.POST("/v1/brands/{brand}/postback-sources/{id}/rotate", { params: { path: { brand: pickedBrandId(), id: network.id } } });
    if (error) return toast(errorMessage(error), "error");
    setSecret(secretOf((data as Network).postback_url ?? ""));
    toast("New postback URL made", "ok");
  };
  const base = url.split("?")[0];
  const query = url.includes("?") ? url.slice(url.indexOf("?")) : "";
  const pixel = `<img src="${base}/pixel${query}" width="1" height="1" alt="" />`;
  const script = `<script src="${base}/pixel.js${query}" async></script>`;
  const curl = `curl -X POST '${base}' \\\n  -H 'Content-Type: application/json' \\\n  -d '${JSON.stringify(JSON.parse(json).body)}'`;
  const shown = postbackParams.filter((p) => more || !p.name.match(/^p\d+$/) || rows[p.name]?.on);
  return (
    <Card title="Postback URL" description="Give this to the network: it calls it for each conversion, filling its own tokens in.">
      <div className="space-y-4">
        <div className="grid gap-3 sm:grid-cols-2">
          <label className="text-sm">
            Affiliate network
            <select aria-label="Network" className={`${field} mt-1 w-full`} value={networkId} onChange={(e) => setNetworkId(e.target.value)}>
              {networks.map((n) => (
                <option key={n.id} value={n.id}>{n.name}</option>
              ))}
            </select>
          </label>
          <label className="text-sm">
            Domain
            <select aria-label="Domain" className={`${field} mt-1 w-full`} value={domain} onChange={(e) => setDomain(e.target.value)}>
              {domains.length === 0 && <option value="">Verify a tracking domain first</option>}
              {domains.map((d) => (
                <option key={d} value={d}>{d}</option>
              ))}
            </select>
          </label>
        </div>
        <div className="text-sm">
          Secret
          <span className="block text-xs text-muted-foreground">Part of the URL, shown only when it is made: paste it from the URL you saved (it starts {network.token_hint}), or make a new one.</span>
          <div className="mt-1 flex flex-wrap gap-2">
            <input aria-label="Secret" type={show ? "text" : "password"} autoComplete="off" className={`${field} min-w-48 flex-1 font-mono text-xs`} placeholder={`${network.token_hint}…`} value={secret} onChange={(e) => setSecret(e.target.value.trim())} />
            <Button type="button" variant="ghost" onClick={() => setShow(!show)}>{show ? "Hide" : "Show"}</Button>
            {secret && <CopyButton text={secret} label="Copy" />}
            {canManage && <Button type="button" variant="ghost" onClick={() => void rotate()}>Make a new one</Button>}
          </div>
        </div>
        <table className="w-full text-sm" aria-label="Postback parameters">
          <thead>
            <tr className="text-left text-xs text-muted-foreground">
              <th className="w-8 py-1 font-medium" />
              <th className="py-1 font-medium">Outflare</th>
              <th className="py-1 font-medium">{template ? `${template.name}'s token` : "The network's token"}</th>
            </tr>
          </thead>
          <tbody>
            {shown.map((p) => (
              <tr key={p.name} className="border-t">
                <td className="py-1"><input type="checkbox" aria-label={`Send ${p.label}`} checked={rows[p.name]?.on ?? false} onChange={(e) => setRows({ ...rows, [p.name]: { ...rows[p.name], on: e.target.checked } })} /></td>
                <td className="py-1"><span className="font-mono text-xs">{p.name}</span> <span className="text-xs text-muted-foreground">{p.label}</span></td>
                <td className="py-1"><input aria-label={`${p.label} token`} className={`${field} w-full font-mono text-xs`} value={rows[p.name]?.token ?? ""} onChange={(e) => setRows({ ...rows, [p.name]: { on: true, token: e.target.value } })} /></td>
              </tr>
            ))}
          </tbody>
        </table>
        {!more && <Button type="button" variant="ghost" onClick={() => setMore(true)}>+ Parameters p1 to p20</Button>}
        <div className="space-y-1">
          <div className="break-all rounded-md border bg-muted/40 p-3 font-mono text-sm" data-testid="postback-preview">{url}</div>
          <div className="flex flex-wrap gap-2">
            <CopyButton text={url} label="Copy" />
            <CopyButton text={json} label="Copy as POST JSON" />
          </div>
          {!secret && <p className="text-xs text-amber-700 dark:text-amber-400">Put the secret in before you give the URL to the network.</p>}
        </div>
        <Tabs defaultValue="pixel">
          <TabsList>
            <TabsTrigger value="pixel">Image pixel</TabsTrigger>
            <TabsTrigger value="script">JavaScript</TabsTrigger>
            <TabsTrigger value="server">Server call</TabsTrigger>
          </TabsList>
          {[
            ["pixel", "When the network can only put a tag on its thank-you page. Anyone can read a pixel's URL, so prefer the postback for payouts that matter.", pixel],
            ["script", "Like the pixel, where the page allows scripts but not images.", script],
            ["server", "From your own backend, such as a shop recording a sale.", curl],
          ].map(([id, when, code]) => (
            <TabsContent key={id} value={id} className="space-y-2">
              <p className="text-xs text-muted-foreground">{when}</p>
              <pre className="overflow-x-auto rounded-md border bg-muted/40 p-2 font-mono text-xs" data-testid={`snippet-${id}`}>{code}</pre>
              <CopyButton text={code} label="Copy" />
            </TabsContent>
          ))}
        </Tabs>
      </div>
    </Card>
  );
}

const reasons: Record<string, string> = {
  accepted: "Recorded on the click.",
  updated: "It changed a conversion already recorded.",
  duplicate: "A conversion with this transaction id is already recorded, and the network's duplicate policy ignores repeats.",
};

// TestConversion records one conversion on a click: type test, payout 0 and
// declined, so it isn't sent back to traffic sources by default, doesn't
// count toward caps and adds no revenue.
function TestConversion({ networks, campaignNames, canManage, onDone }: { networks: Network[]; campaignNames: Record<string, string>; canManage: boolean; onDone: () => void }) {
  const [source, setSource] = useState(networks[0].id);
  const [click, setClick] = useState("");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<{ outcome: string; reason: string; conversion?: Conversion } | null>(null);
  // The latest click on the brand's tracker links, likely bots left out.
  const latest = async () => {
    const { data } = await api.GET("/v1/brands/{brand}/tracker-clicks", { params: { path: { brand: pickedBrandId() }, query: { limit: 1 } } });
    const c = data?.data?.[0];
    if (!c) return toast("No clicks yet: open your campaign URL first", "error");
    setClick(c.click_key);
  };
  const fire = async () => {
    setBusy(true);
    setResult(null);
    const brand = pickedBrandId();
    const { data, error } = await api.POST("/v1/brands/{brand}/tracker-conversions", {
      params: { path: { brand } },
      body: { source, conversions: [{ click_id: click.trim(), transaction_id: `test-${Date.now()}`, payout: 0, type: "test", status: "declined" }] },
    });
    if (error) {
      setBusy(false);
      return toast(errorMessage(error), "error");
    }
    const row = (data as Upload).rows[0];
    let conversion: Conversion | undefined;
    if (row?.conversion_id) {
      const list = await api.GET("/v1/brands/{brand}/tracker-conversions", { params: { path: { brand }, query: { source } } });
      conversion = ((list.data?.data ?? []) as Conversion[]).find((c) => c.id === row.conversion_id);
    }
    setBusy(false);
    setResult({ outcome: row?.outcome ?? "failed", reason: row?.error || reasons[row?.outcome ?? ""] || "", conversion });
    if (row?.outcome === "accepted") onDone();
  };
  return (
    <Card title="Test conversion" description="Fire one on a click to see what a real conversion would do. It is recorded as type test, with no payout and declined, so it isn't sent to traffic sources and adds no revenue.">
      <form className="space-y-3" onSubmit={(e) => { e.preventDefault(); void fire(); }}>
        <div className="grid gap-3 sm:grid-cols-[1fr_2fr]">
          <label className="text-sm">
            Affiliate network
            <select aria-label="Test network" className={`${field} mt-1 w-full`} value={source} onChange={(e) => setSource(e.target.value)}>
              {networks.map((n) => (
                <option key={n.id} value={n.id}>{n.name}</option>
              ))}
            </select>
          </label>
          <label className="text-sm">
            Click id
            <span className="mt-1 flex gap-2">
              <input aria-label="Click id" required className={`${field} min-w-0 flex-1 font-mono text-xs`} placeholder="Click your campaign URL, then take its click id" value={click} onChange={(e) => setClick(e.target.value)} />
              <Button type="button" variant="ghost" onClick={() => void latest()}>Use my latest click</Button>
            </span>
          </label>
        </div>
        {canManage && <Button type="submit" variant="primary" disabled={busy || !click.trim()}>Fire test conversion</Button>}
      </form>
      {result && (
        <div className="mt-3 rounded-md border p-3 text-sm" data-testid="test-result" role="status">
          <div className="flex items-center gap-2">
            <Badge tone={result.outcome === "accepted" || result.outcome === "updated" ? "green" : result.outcome === "duplicate" ? "amber" : "red"}>{result.outcome}</Badge>
            <span>{result.reason}</span>
          </div>
          {result.conversion && (
            <dl className="mt-2 grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 text-xs">
              <dt className="text-muted-foreground">Campaign</dt>
              <dd>{result.conversion.campaign_id ? campaignNames[result.conversion.campaign_id] ?? result.conversion.campaign_id : "None: the click has no campaign"}</dd>
              <dt className="text-muted-foreground">Clicked</dt>
              <dd>{new Date(result.conversion.clicked_at).toLocaleString()}</dd>
              <dt className="text-muted-foreground">Payout and status</dt>
              <dd>{result.conversion.payout} {result.conversion.currency}, {result.conversion.status}</dd>
            </dl>
          )}
        </div>
      )}
    </Card>
  );
}

type IPLookupSettings = Schemas["IPLookupSettings"];
type Integration = Schemas["Integration"];

// IPLookups: the brand's paid IP reputation lookups (B7c), on the
// workspace's own IPQualityScore connection. Off until the brand turns them
// on; then the clicks it chooses (flagged, converting, a sample) are looked
// up, up to a monthly cap, and a high score or a data-centre network flags
// the click.
function IPLookups({ ws, canManage, go }: { ws: string; canManage: boolean; go: (tab: Tab, item?: string) => void }) {
  const [v, setV] = useState<IPLookupSettings | null>(null);
  const [draft, setDraft] = useState<IPLookupSettings | null>(null);
  const [scorers, setScorers] = useState<Integration[]>([]);
  const [saving, setSaving] = useState(false);
  const load = useCallback(async () => {
    const [s, i] = await Promise.all([
      api.GET("/v1/brands/{brand}/ip-lookups", { params: { path: { brand: pickedBrandId() } } }),
      api.GET("/v1/workspaces/{ws}/integrations", { params: { path: { ws } } }),
    ]);
    if (s.data) {
      setV(s.data);
      setDraft(s.data);
    }
    setScorers(((i.data?.data ?? []) as Integration[]).filter((x) => x.capabilities.includes("ip.score")));
  }, [ws]);
  useEffect(() => void load(), [load]);
  const save = async (patch: Partial<IPLookupSettings>) => {
    if (!draft) return;
    const next = { ...draft, ...patch };
    const integration = next.integration_id ?? scorers[0]?.id ?? null;
    setSaving(true);
    const { data, error } = await api.PUT("/v1/brands/{brand}/ip-lookups", {
      params: { path: { brand: pickedBrandId() } },
      body: { integration_id: integration ?? "", enabled: next.enabled, flagged: next.flagged, converting: next.converting, sample_percent: next.sample_percent, monthly_cap: next.monthly_cap, threshold: next.threshold },
    });
    setSaving(false);
    if (error) return toast(errorMessage(error), "error");
    setV(data);
    setDraft(data);
    toast(data.enabled ? "IP lookups saved" : "IP lookups are off: kept addresses were deleted", "ok");
  };
  const set = (patch: Partial<IPLookupSettings>) => setDraft((d) => (d ? { ...d, ...patch } : d));
  const changed = !!v && !!draft && JSON.stringify(v) !== JSON.stringify(draft);
  return (
    <Card title="IP lookups" description="Look up the address of the clicks you choose on your own IPQualityScore account. A risk score at or over your threshold, or a data-centre network, flags the click as suspect, and its conversions aren't sent back to the traffic source. Exact addresses are kept for 30 days at most, only while lookups are on.">
      {v === null || draft === null ? (
        <Loading />
      ) : scorers.length === 0 ? (
        <p className="text-sm text-muted-foreground" data-testid="ip-lookups-connect">
          Connect IPQualityScore first, with your own API key: lookups spend your account's credits.{" "}
          <button type="button" className="underline" onClick={() => go("connectors", "ipqualityscore")}>Connect it under Fraud checks</button>
        </p>
      ) : (
        <div className="space-y-3 text-sm" data-testid="ip-lookups">
          <div className="flex flex-wrap items-center gap-3">
            <Badge tone={v.enabled ? "green" : "grey"}>{v.enabled ? "On" : "Off"}</Badge>
            <span className="text-muted-foreground" data-testid="ip-lookups-used">
              {v.used_this_month.toLocaleString()} of {v.monthly_cap.toLocaleString()} lookups used this month
            </span>
            {canManage && (
              <Button type="button" variant={v.enabled ? "ghost" : undefined} disabled={saving} onClick={() => void save({ enabled: !v.enabled })}>
                {v.enabled ? "Turn off" : "Turn on"}
              </Button>
            )}
          </div>
          {scorers.length > 1 && (
            <label className="flex items-center gap-2">
              <span className="w-40 text-muted-foreground">Account</span>
              <select aria-label="Lookup account" className={field} disabled={!canManage} value={draft.integration_id ?? scorers[0].id} onChange={(e) => set({ integration_id: e.target.value })}>
                {scorers.map((x) => <option key={x.id} value={x.id}>{x.name}</option>)}
              </select>
            </label>
          )}
          <fieldset className="space-y-1" disabled={!canManage}>
            <legend className="mb-1 text-muted-foreground">Which clicks</legend>
            <label className="flex items-center gap-2">
              <input type="checkbox" checked={draft.flagged} onChange={(e) => set({ flagged: e.target.checked })} />
              Clicks another signal already flagged (a hosting network, repeat clicks, your filters)
            </label>
            <label className="flex items-center gap-2">
              <input type="checkbox" checked={draft.converting} onChange={(e) => set({ converting: e.target.checked })} />
              Clicks that convert, before their conversion is sent back
            </label>
            <label className="flex items-center gap-2">
              <span>A sample of the rest:</span>
              <input aria-label="Sample percent" type="number" min={0} max={100} step={0.1} className={`${field} w-20`} value={draft.sample_percent} onChange={(e) => set({ sample_percent: Number(e.target.value) })} />%
            </label>
          </fieldset>
          <div className="flex flex-wrap gap-4">
            <label className="flex items-center gap-2">
              <span className="text-muted-foreground">Monthly cap</span>
              <input aria-label="Monthly cap" type="number" min={0} className={`${field} w-28`} disabled={!canManage} value={draft.monthly_cap} onChange={(e) => set({ monthly_cap: Number(e.target.value) })} />
            </label>
            <label className="flex items-center gap-2">
              <span className="text-muted-foreground">Flag at a score of</span>
              <input aria-label="Score threshold" type="number" min={1} max={100} className={`${field} w-20`} disabled={!canManage} value={draft.threshold} onChange={(e) => set({ threshold: Number(e.target.value) })} />
            </label>
          </div>
          {canManage && changed && (
            <div className="flex gap-2">
              <Button type="button" disabled={saving} onClick={() => void save({})}>Save</Button>
              <Button type="button" variant="ghost" onClick={() => setDraft(v)}>Cancel</Button>
            </div>
          )}
        </div>
      )}
    </Card>
  );
}

type ClickFilter = Schemas["ClickFilter"];

// ClickFilters: the brand's own lists of addresses, ranges and user agent
// pieces whose redirect clicks are flagged as suspect (B7b). They still go
// on; they just count against no cap and show as suspect in reports.
function ClickFilters({ canManage }: { canManage: boolean }) {
  const [list, setList] = useState<ClickFilter[] | null>(null);
  const [kind, setKind] = useState<ClickFilter["kind"]>("ip");
  const [value, setValue] = useState("");
  const [note, setNote] = useState("");
  const load = useCallback(async () => {
    const { data } = await api.GET("/v1/brands/{brand}/click-filters", { params: { path: { brand: pickedBrandId() } } });
    setList((data?.data ?? []) as ClickFilter[]);
  }, []);
  useEffect(() => void load(), [load]);
  const add = async () => {
    const { error } = await api.POST("/v1/brands/{brand}/click-filters", { params: { path: { brand: pickedBrandId() } }, body: { kind, value: value.trim(), note: note.trim() } });
    if (error) return toast(errorMessage(error), "error");
    setValue("");
    setNote("");
    toast("Filter added: it applies from the next click", "ok");
    void load();
  };
  const remove = async (f: ClickFilter) => {
    const { error } = await api.DELETE("/v1/brands/{brand}/click-filters/{id}", { params: { path: { brand: pickedBrandId(), id: f.id } } });
    if (error) return toast(errorMessage(error), "error");
    void load();
  };
  return (
    <Card title="Click filters" description="Addresses, ranges and user agents you know are bots or fraud. Their clicks still go on, but are flagged as suspect: they count against no cap and the report can leave them out.">
      {list === null ? (
        <Loading />
      ) : (
        <div className="space-y-3">
          {list.length > 0 && (
            <table className="w-full text-sm" data-testid="click-filters">
              <tbody>
                {list.map((f) => (
                  <tr key={f.id} className="border-t">
                    <td className="py-1 pr-2 text-xs text-muted-foreground">{f.kind === "ip" ? "Address" : "User agent"}</td>
                    <td className="py-1 pr-2 font-mono text-xs">{f.value}</td>
                    <td className="py-1 pr-2 text-xs text-muted-foreground">{f.note}</td>
                    <td className="py-1 text-right">{canManage && <Button variant="ghost" aria-label={`Remove ${f.value}`} onClick={() => void remove(f)}>×</Button>}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
          {canManage && (
            <form className="flex flex-wrap gap-2" onSubmit={(e) => { e.preventDefault(); void add(); }}>
              <select aria-label="Filter kind" className={field} value={kind} onChange={(e) => setKind(e.target.value as ClickFilter["kind"])}>
                <option value="ip">Address or range</option>
                <option value="user_agent">User agent contains</option>
              </select>
              <input aria-label="Filter value" required className={`${field} w-56 font-mono text-xs`} placeholder={kind === "ip" ? "203.0.113.0/24" : "headlesschrome"} value={value} onChange={(e) => setValue(e.target.value)} />
              <input aria-label="Filter note" className={`${field} w-48`} placeholder="Why (optional)" value={note} onChange={(e) => setNote(e.target.value)} />
              <Button type="submit" variant="ghost" disabled={!value.trim()}>Add filter</Button>
            </form>
          )}
        </div>
      )}
    </Card>
  );
}
