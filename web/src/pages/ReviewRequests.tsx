import { useCallback, useEffect, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { PersonPicker, type Person } from "@/components/person-picker";
import { track } from "@/lib/analytics";
import { Rules, useRuleChoices } from "./SmartLists";
import { api, errorMessage } from "../api";
import { useBrand } from "@/lib/brand";
import { Badge, Button, Card, confirmAction, ErrorText, field, Input, Loading, Stat, Stats, toast } from "../ui";
import type { Tab } from "../App";

type Config = Schemas["ReviewCampaignConfig"];
type Campaign = Schemas["ReviewRequestCampaign"];
type Settings = Schemas["ReviewRequestSettings"];
type Catalog = Schemas["ReviewRequestCatalog"];
type Request = Schemas["ReviewRequest"];
type Result = Schemas["ReviewRequestList"];
type View = "overview" | "campaigns" | "requests" | "settings";
const templates = [
  { name: "General feedback", subject: "How was your experience with {{brand.name}}?", body: "Hi {{person.first_name|there}},\n\nThank you for choosing {{brand.name}}. Would you share an honest review of your experience? Your feedback helps us improve and helps others make a decision.\n\nThank you for your time." },
  { name: "Product review", subject: "Share your thoughts on {{brand.name}}", body: "Hi {{person.first_name|there}},\n\nThank you for using {{brand.name}}. We'd love an honest review of what worked well and what we could improve.\n\nYou can leave your review using the links below. Thank you!" },
  { name: "Service review", subject: "Tell us about your experience with {{brand.name}}", body: "Hi {{person.first_name|there}},\n\nThank you for trusting {{brand.name}}. Please share an honest review of the service you received. Your feedback helps our team improve and helps future customers.\n\nThank you for your time." },
];
const emptyConfig = (): Config => ({ name: "", mode: "manual", from: "", reply_to: "", subject: templates[0].subject, body: templates[0].body, platforms: [], audience: { kind: "list", match: "all", rules: [] } });
const savedKey = (brand: string) => `outflare.review-request-draft.${brand}`;
const date = (v?: string | null) => v ? new Date(v).toLocaleString() : "—";
const why = (s: string) => s.replaceAll("_", " ");

export function ReviewRequests({ ws, canManage, go, onReviews }: { ws: string; canManage: boolean; go: (tab: Tab, item?: string) => void; onReviews: () => void }) {
  const { brand } = useBrand(ws);
  const [view, updateView] = useState<View>("overview");
  const setView = (view: View) => { updateView(view); track("ui.review_view_opened", { view }); };
  if (!brand) return <Card title="Choose a brand"><p className="text-sm text-muted-foreground">Select a brand in the brand switcher to manage its review invitations.</p><Button variant="ghost" onClick={onReviews}>View reviews across brands</Button></Card>;
  return <div className="space-y-5">
    <nav aria-label="Review views" className="flex flex-wrap gap-2 border-b pb-3">
      {(["overview", "campaigns", "requests", "settings"] as View[]).map(v => <Button key={v} variant={view === v ? "primary" : "ghost"} onClick={() => setView(v)}>{v[0].toUpperCase() + v.slice(1)}</Button>)}
      <Button variant="ghost" onClick={onReviews}>Reviews & ratings</Button>
    </nav>
    <BrandRequests key={brand.id} brand={brand.id} brandName={brand.name} ws={ws} view={view} setView={setView} canManage={canManage} go={go} />
  </div>;
}

function BrandRequests({ brand, brandName, ws, view, setView, canManage, go }: { brand: string; brandName: string; ws: string; view: View; setView: (v: View) => void; canManage: boolean; go: (tab: Tab, item?: string) => void }) {
  const [catalog, setCatalog] = useState<Catalog>();
  const [campaigns, setCampaigns] = useState<Campaign[]>([]);
  const [result, setResult] = useState<Result>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [editor, setEditor] = useState<Campaign | "new" | null>(null);
  const [filter, setFilter] = useState({ campaign: "", status: "", from: "", to: "" });
  const [detail, setDetail] = useState<Request>();
  const [emailDetail, setEmailDetail] = useState<Schemas["Email"]>();
  const path = { brand };
  const query = () => ({ campaign: filter.campaign, status: filter.status, ...(filter.from ? { from: new Date(filter.from).toISOString() } : {}), ...(filter.to ? { to: new Date(`${filter.to}T23:59:59.999`).toISOString() } : {}) });
  const load = useCallback(async () => {
    setError("");
    const [a, b] = await Promise.all([
      api.GET("/v1/brands/{brand}/review-requests/settings", { params: { path: { brand } } }),
      api.GET("/v1/brands/{brand}/review-requests/campaigns", { params: { path: { brand } } }),
    ]);
    if (a.error || b.error) { setError(errorMessage(a.error || b.error)); return; }
    setCatalog(a.data); setCampaigns(b.data?.data ?? []);
  }, [brand]);
  useEffect(() => { void load(); }, [load]);
  const loadRequests = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/review-requests", { params: { path: { brand }, query: { campaign: filter.campaign, status: filter.status, ...(filter.from ? { from: new Date(filter.from).toISOString() } : {}), ...(filter.to ? { to: new Date(`${filter.to}T23:59:59.999`).toISOString() } : {}) } } });
    if (error) { setError(errorMessage(error)); return; } setResult(data);
  }, [brand, filter]);
  useEffect(() => { void loadRequests(); }, [loadRequests]);
  // Refresh request outcomes while a campaign has mail in flight.
  useEffect(() => { if (!result?.report.pending) return; const timer = setInterval(() => void loadRequests(), 10000); return () => clearInterval(timer); }, [result?.report.pending, loadRequests]);
  async function action(c: Campaign, action: "pause" | "resume" | "archive" | "duplicate") {
    if (action === "archive" && !await confirmAction(`Archive ${c.config.name}?`, <p>Pending invitations stop. Emails already handed to the provider cannot be recalled. Request history stays.</p>, "Archive")) return;
    setBusy(true);
    const { error } = await api.POST("/v1/brands/{brand}/review-requests/campaigns/{campaign}/action", { params: { path: { brand, campaign: c.id } }, body: { action } });
    setBusy(false); if (error) return toast(errorMessage(error), "error"); await load(); await loadRequests();
  }
  async function more() {
    if (!result?.next) return;
    const { data, error } = await api.GET("/v1/brands/{brand}/review-requests", { params: { path, query: { ...query(), after: result.next } } });
    if (error) return toast(errorMessage(error), "error"); if (data) setResult({ ...data, data: [...result.data, ...data.data] });
  }
  if (!catalog) return error ? <ErrorText>{error}</ErrorText> : <Loading />;
  const report = result?.report;
  const platformName = (id: string) => catalog.platforms.find(p => p.id === id)?.name ?? id;
  return <div className="space-y-5">
    {error && <ErrorText>{error}</ErrorText>}
    {editor && <CampaignEditor key={editor === "new" ? "new" : editor.id} brand={brand} brandName={brandName} ws={ws} catalog={catalog} campaign={editor === "new" ? undefined : editor} onClose={() => setEditor(null)} onSaved={async () => { setEditor(null); await load(); await loadRequests(); }} />}
    {!editor && <>
      {(view === "overview" || view === "requests") && <>
        <div className="flex flex-wrap items-end gap-3">
          <label className="text-xs">Campaign<select aria-label="Campaign filter" className={`${field} mt-1 block`} value={filter.campaign} onChange={e => setFilter({ ...filter, campaign: e.target.value })}><option value="">All campaigns</option>{campaigns.map(c => <option key={c.id} value={c.id}>{c.config.name}</option>)}</select></label>
          <label className="text-xs">From<input aria-label="From date" className={`${field} mt-1 block`} type="date" value={filter.from} onChange={e => setFilter({ ...filter, from: e.target.value })} /></label>
          <label className="text-xs">To<input aria-label="To date" className={`${field} mt-1 block`} type="date" min={filter.from} value={filter.to} onChange={e => setFilter({ ...filter, to: e.target.value })} /></label>
          <Button variant="ghost" onClick={() => void loadRequests()}>Refresh</Button>
        </div>
        {report && <Stats cols={5}><Stat label="Sent" value={report.sent} /><Stat label="Delivered" value={report.delivered} sub="When the provider confirms delivery" /><Stat label="Review-link clicks" value={report.clicked} sub={report.sent ? `${Math.round(report.clicked / report.sent * 100)}% of sent requests` : "Requests with a review-link click"} /><Stat label="Pending" value={report.pending} /><Stat label="Skipped / failed" value={`${report.skipped} / ${report.failed}`} /></Stats>}
      </>}
      {view === "overview" && <>
        <Card title={`Grow reviews for ${brandName}`} description="Ask customers for an honest review using one email. Configure a one-time audience or use a campaign in your existing workflows.">
          <div className="flex flex-wrap gap-2">{canManage && <Button onClick={() => { setEditor("new"); setView("campaigns"); }}>Create campaign</Button>}<Button variant="ghost" onClick={() => setView("settings")}>Configure destinations</Button><Button variant="ghost" onClick={() => go("journeys")}>Open workflows</Button></div>
          <p className="mt-3 text-xs text-muted-foreground">{catalog.settings.destinations.length} destinations configured · {catalog.settings.frequency === "once" ? "Ask once ever" : catalog.settings.frequency === "unlimited" ? "No additional review frequency limit" : `Wait ${catalog.settings.interval_days} days between requests`} · {catalog.settings.frequency_scope === "brand" ? "across this brand" : "per platform"}</p>
        </Card>
        <Card title="Clicks by review destination" description="Unique customers who clicked each review destination. A customer may click several destinations.">
          {report?.platforms.length ? <div className="space-y-2">{report.platforms.map(p => <div key={p.platform} className="flex justify-between text-sm"><span>{platformName(p.platform)}</span><span>{p.clickers}</span></div>)}</div> : <p className="text-sm text-muted-foreground">Review-link clicks will appear after your first campaign.</p>}
        </Card>
        <Card title="Review attribution" description="Clicks show interest; they do not confirm a review was submitted."><p className="text-sm text-muted-foreground">See newly observed reviews in Reviews & ratings. The current monitoring integrations do not reliably match a public review to an invitation, so completed-review conversion is unavailable.</p></Card>
      </>}
      {view === "campaigns" && <Card title="Review campaigns" description="One-time sends capture their audience at launch. Workflow campaigns evaluate each customer when the action runs.">
        {canManage && <Button onClick={() => setEditor("new")}>Create campaign</Button>}
        {!campaigns.length ? <p className="mt-4 text-sm text-muted-foreground">Start with a customer list, choose your destinations, and customize a ready-to-use email.</p> : <div className="mt-4 space-y-3">{campaigns.map(c => <div key={c.id} className="rounded-lg border p-4 space-y-2">
          <div className="flex flex-wrap justify-between gap-2"><div><p className="font-medium">{c.config.name}</p><p className="text-xs text-muted-foreground">{c.config.mode === "manual" ? "One-time campaign" : "Workflow campaign"} · {c.config.platforms.map(platformName).join(", ")}</p></div><Badge>{c.status === "active" && c.send_at && new Date(c.send_at) > new Date() ? "scheduled" : c.status === "active" && c.config.mode === "manual" ? "launched" : c.status}</Badge></div>
          {c.stats && <p className="text-xs text-muted-foreground">{c.stats.sent} sent · {c.stats.clicked} review-link clicks · {c.stats.pending} pending · {c.stats.skipped} skipped · {c.stats.failed} failed</p>}
          {c.send_at && <p className="text-xs text-muted-foreground">Scheduled: {date(c.send_at)}</p>}
          <div className="flex flex-wrap gap-2"><Button variant="ghost" onClick={() => { setFilter({ ...filter, campaign: c.id }); setView("requests"); }}>View requests</Button>{canManage && <>
            {(c.status === "draft" || c.status === "paused" && c.config.mode === "workflow") && <Button variant="ghost" onClick={() => setEditor(c)}>{c.status === "draft" ? "Edit & launch" : "Edit future requests"}</Button>}
            {c.status === "active" && <Button variant="ghost" disabled={busy} onClick={() => action(c, "pause")}>Pause</Button>}
            {c.status === "paused" && <Button variant="ghost" disabled={busy} onClick={() => action(c, "resume")}>Resume</Button>}
            <Button variant="ghost" disabled={busy} onClick={() => action(c, "duplicate")}>Duplicate</Button>
            {c.status !== "archived" && <Button variant="ghost" disabled={busy} onClick={() => action(c, "archive")}>Archive</Button>}
          </>}</div>
        </div>)}</div>}
      </Card>}
      {view === "requests" && <Card title="Request history" description="Frequency limits are shared across manual campaigns and workflows. Each skipped or failed request keeps its reason.">
        <select aria-label="Request status" className={field} value={filter.status} onChange={e => setFilter({ ...filter, status: e.target.value })}><option value="">All statuses</option>{["pending", "queued", "scheduled", "sent", "delivered", "clicked", "skipped", "failed", "bounced", "complained"].map(s => <option key={s} value={s}>{why(s)}</option>)}</select>
        <div className="overflow-x-auto mt-3"><table className="w-full text-sm text-left"><thead><tr><th className="py-2">Customer</th><th>Campaign</th><th>Status</th><th>Requested</th><th>Review link clicked</th></tr></thead><tbody className="divide-y">{result?.data.map(r => <tr key={r.id}><td className="py-3"><button className="underline text-left" onClick={() => { setDetail(r); setEmailDetail(undefined); }}>{r.customer || r.record_id}</button></td><td>{r.campaign}</td><td><Badge>{why(r.status)}</Badge>{r.reason && <p className="text-xs text-muted-foreground max-w-xs">{why(r.reason)}</p>}</td><td>{date(r.created_at)}</td><td>{date(r.clicked_at)}</td></tr>)}</tbody></table></div>
        {!result?.data.length && <p className="py-6 text-sm text-muted-foreground">No requests match these filters.</p>}{result?.next && <Button variant="ghost" onClick={more}>Load more</Button>}
        {detail && <div className="mt-4 border rounded-lg p-4 space-y-2"><div className="flex justify-between"><p className="font-medium">{detail.customer || "Customer"}</p><Button variant="ghost" onClick={() => setDetail(undefined)}>Close details</Button></div><p className="text-sm">{detail.campaign} · {detail.platforms.map(platformName).join(", ")}</p><p className="text-sm">Requested {date(detail.created_at)} → Sent {date(detail.sent_at)} → Review link clicked {date(detail.clicked_at)}</p>{detail.reason && <p className="text-sm">{why(detail.reason)}</p>}{detail.email_id && <Button variant="ghost" onClick={async () => { const { data, error } = await api.GET("/v1/workspaces/{ws}/emails/{id}", { params: { path: { ws, id: detail.email_id! } } }); if (error) toast(errorMessage(error), "error"); else setEmailDetail(data); }}>View email content</Button>}{emailDetail && <div className="border rounded-md p-3 space-y-2"><p className="font-medium text-sm">{emailDetail.subject}</p><p className="text-xs text-muted-foreground">From: {emailDetail.from}</p><p className="whitespace-pre-wrap break-words text-sm">{emailDetail.text}</p></div>}<p className="text-xs text-muted-foreground">Request ID: {detail.id}</p></div>}
      </Card>}
      {view === "settings" && <RequestSettings catalog={catalog} canManage={canManage} onSave={async settings => {
        const { error } = await api.PUT("/v1/brands/{brand}/review-requests/settings", { params: { path }, body: settings });
        if (error) { toast(errorMessage(error), "error"); return false; } await load(); toast("Brand review defaults saved."); return true;
      }} />}
    </>}
  </div>;
}

function RequestSettings({ catalog, canManage, onSave }: { catalog: Catalog; canManage: boolean; onSave: (s: Settings) => Promise<boolean> }) {
  const [settings, setSettings] = useState(catalog.settings);
  const [search, setSearch] = useState("");
  const [busy, setBusy] = useState(false);
  const update = (platform: string, url: string) => setSettings(s => ({ ...s, destinations: [...s.destinations.filter(d => d.platform !== platform), ...(url.trim() ? [{ platform, url: url.trim() }] : [])] }));
  return <form className="space-y-5" onSubmit={async e => { e.preventDefault(); setBusy(true); await onSave(settings); setBusy(false); }}>
    <Card title="Brand sending defaults" description="Campaigns can override the sender and reply-to. All email still uses your existing Lifecycle provider, consent rules, and guardrails."><fieldset disabled={!canManage || busy} className="grid gap-4 md:grid-cols-2"><Input label="Default sender" placeholder="Leave blank to use a brand lifecycle template sender" value={settings.from} onChange={e => setSettings({ ...settings, from: e.target.value })} /><Input label="Reply-to" value={settings.reply_to} onChange={e => setSettings({ ...settings, reply_to: e.target.value })} /></fieldset></Card>
    <Card title="How often to ask" description="These limits cover this brand's campaigns and workflow requests together. Failed, suppressed, and cancelled emails do not consume the limit."><fieldset disabled={!canManage || busy} className="flex flex-wrap gap-4">
      <label className="text-sm">Frequency<select aria-label="Review request frequency" className={`${field} mt-1 block`} value={settings.frequency} onChange={e => setSettings({ ...settings, frequency: e.target.value as Settings["frequency"] })}><option value="interval">Wait between requests</option><option value="once">Only ask once ever</option><option value="unlimited">No additional review limit</option></select></label>
      {settings.frequency === "interval" && <Input label="Days between requests" type="number" min={1} max={36500} value={settings.interval_days} onChange={e => setSettings({ ...settings, interval_days: Number(e.target.value) })} />}
      <label className="text-sm">Apply limit<select aria-label="Frequency scope" className={`${field} mt-1 block`} value={settings.frequency_scope} onChange={e => setSettings({ ...settings, frequency_scope: e.target.value as Settings["frequency_scope"] })}><option value="brand">Across all platforms for this brand</option><option value="platform">Separately per platform</option></select></label>
    </fieldset></Card>
    <Card title="Review destinations" description="One review link per platform for this brand. Invitation and monitoring capabilities are shown separately; no platform currently provides reliable invitation attribution.">
      <Input label="Find a platform" type="search" value={search} onChange={e => setSearch(e.target.value)} />
      <div className="mt-4 divide-y">{catalog.platforms.filter(p => p.name.toLowerCase().includes(search.toLowerCase())).map(p => <div key={p.id} className="py-4 space-y-2"><div className="flex flex-wrap items-center gap-2"><strong className="text-sm">{p.name}</strong><Badge>{p.invitation ? "Email invitations" : "Platform review flow"}</Badge>{p.monitoring && <Badge>Review monitoring</Badge>}</div><p className="text-xs text-muted-foreground">{p.note}</p><Input label={`${p.name} review link`} type="url" placeholder="https://…" disabled={!canManage || busy} value={settings.destinations.find(d => d.platform === p.id)?.url ?? ""} onChange={e => update(p.id, e.target.value)} /></div>)}</div>
    </Card>
    {canManage && <Button type="submit" disabled={busy}>{busy ? "Saving…" : "Save brand defaults"}</Button>}
  </form>;
}

function CampaignEditor({ brand, brandName, ws, catalog, campaign, onClose, onSaved }: { brand: string; brandName: string; ws: string; catalog: Catalog; campaign?: Campaign; onClose: () => void; onSaved: () => Promise<void> }) {
  const [config, setConfig] = useState<Config>(() => {
    if (campaign) return campaign.config;
    try { const raw = sessionStorage.getItem(savedKey(brand)); if (raw) return JSON.parse(raw) as Config; } catch { /* Storage can be unavailable. */ }
    return { ...emptyConfig(), platforms: catalog.settings.destinations.filter(d => catalog.platforms.some(p => p.id === d.platform && p.invitation)).slice(0, 1).map(d => d.platform) };
  });
  const [id, setID] = useState(campaign?.id);
  const [step, updateStep] = useState(0);
  const setStep = (step: number) => { updateStep(step); track("ui.review_campaign_step_opened", { step }); };
  const [selectedPeople, setSelectedPeople] = useState<Person[]>([]);
  useEffect(() => { let live=true; const ids=campaign?.config.audience.record_ids ?? []; void Promise.all(ids.slice(0,50).map(async record => { const { data }=await api.GET("/v1/workspaces/{ws}/records/{record}",{params:{path:{ws,record}}});return data ? {id:data.id,name:data.name,email:data.identities.find(i=>i.namespace==="email")?.value} : null; })).then(people=>{if(live)setSelectedPeople(people.filter(p=>p!==null));});return()=>{live=false;}; },[ws,campaign?.id]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [preview, setPreview] = useState<Schemas["ReviewRequestPreview"]>();
  const [lists, setLists] = useState<Schemas["SmartList"][]>([]);
  const [sendAt, setSendAt] = useState("");
  const [testTo, setTestTo] = useState("");
  const choices = useRuleChoices(ws);
  useEffect(() => { if (!campaign) { try { sessionStorage.setItem(savedKey(brand), JSON.stringify(config)); } catch { /* Storage can be unavailable. */ } } }, [brand, campaign, config]);
  useEffect(() => { api.GET("/v1/workspaces/{ws}/smart-lists", { params: { path: { ws } } }).then(({ data }) => setLists(data?.data.filter(l => l.brand_id === brand) ?? [])); }, [ws, brand]);
  function change(next: Partial<Config>) { setConfig(c => ({ ...c, ...next })); setPreview(undefined); }
  async function save(): Promise<string | undefined> {
    setError(""); const body = config;

    const response = id ? await api.PUT("/v1/brands/{brand}/review-requests/campaigns/{campaign}", { params: { path: { brand, campaign: id } }, body }) : await api.POST("/v1/brands/{brand}/review-requests/campaigns", { params: { path: { brand } }, body });
    if (response.error || !response.data) { setError(errorMessage(response.error)); return; } setID(response.data.id); return response.data.id;
  }
  async function review() {
    setBusy(true); const campaign = await save();
    if (campaign) { const { data, error } = await api.POST("/v1/brands/{brand}/review-requests/campaigns/{campaign}/preview", { params: { path: { brand, campaign } } }); if (error) setError(errorMessage(error)); else { setPreview(data); setStep(3); } }
    setBusy(false);
  }
  async function launch() {
    if (!id || !preview) return;
    if (!await confirmAction(config.mode === "workflow" ? "Activate review campaign?" : "Launch review campaign?", <p>{config.mode === "workflow" ? "Connected workflows can now send this invitation. Set timing and approval in the workflow." : `${preview.eligible} customers currently qualify. Eligibility is checked again before sending. Each gets one email, with no reminders.`}</p>, config.mode === "workflow" ? "Activate" : "Launch")) return;
    setBusy(true);
    const { error } = await api.POST("/v1/brands/{brand}/review-requests/campaigns/{campaign}/action", { params: { path: { brand, campaign: id } }, body: { action: "launch", ...(sendAt ? { send_at: new Date(sendAt).toISOString() } : {}) } });
    setBusy(false); if (error) return setError(errorMessage(error)); sessionStorage.removeItem(savedKey(brand)); toast(config.mode === "workflow" ? "Campaign activated. Choose it in a Request a review workflow action." : "Review campaign launched."); await onSaved();
  }
  async function test() {
    setBusy(true); const campaign = await save();
    if (campaign) { const { error } = await api.POST("/v1/brands/{brand}/review-requests/campaigns/{campaign}/test", { params: { path: { brand, campaign } }, body: { to: testTo } }); if (error) setError(errorMessage(error)); else toast("Test email queued. Test sends do not affect review frequency limits."); }
    setBusy(false);
  }
  const destinations = catalog.settings.destinations.filter(d => config.platforms.includes(d.platform));
  const fill = (s: string) => s.replaceAll("{{brand.name}}", brandName).replaceAll("{{person.first_name|there}}", "there");
  return <Card title={campaign ? `Edit ${campaign.config.name}` : "Create review campaign"} description="One honest invitation, sent through your existing email infrastructure.">
    <div className="flex flex-wrap gap-2 my-4" aria-label="Campaign setup steps">{["Audience", "Email", "Timing", "Review & launch"].map((label, i) => <Button key={label} variant={step === i ? "primary" : "ghost"} disabled={busy || i === 3 && !preview} onClick={() => setStep(i)}>{i + 1}. {label}</Button>)}</div>
    {error && <ErrorText>{error}</ErrorText>}
    <fieldset disabled={busy} className="space-y-4">
      {step === 0 && <>
        <Input label="Campaign name" maxLength={200} value={config.name} onChange={e => change({ name: e.target.value })} />
        <label className="block text-sm">Campaign mode<select aria-label="Campaign mode" className={`${field} mt-1 block w-full`} disabled={!!campaign && campaign.status !== "draft"} value={config.mode} onChange={e => change({ mode: e.target.value as Config["mode"] })}><option value="manual">One-time campaign</option><option value="workflow">Used by workflows</option></select></label>
        {config.mode === "workflow" ? <p className="text-sm text-muted-foreground">The workflow selects customers and controls triggers, delays, and approvals. Add a Request a review action and choose this campaign.</p> : <>
          <label className="block text-sm">Audience<select aria-label="Audience source" className={`${field} mt-1 block w-full`} value={config.audience.kind} onChange={e => change({ audience: { ...config.audience, kind: e.target.value as Config["audience"]["kind"] } })}><option value="list">Existing customer list (static or smart)</option><option value="rules">Dynamic audience rules</option><option value="records">Selected customer IDs</option></select></label>
          {config.audience.kind === "list" && <label className="block text-sm">Customer list<select aria-label="Customer list" className={`${field} mt-1 block w-full`} value={config.audience.list_id ?? ""} onChange={e => change({ audience: { ...config.audience, list_id: e.target.value } })}><option value="">Choose a list…</option>{lists.map(l => <option key={l.id} value={l.id}>{l.name} ({l.kind})</option>)}</select></label>}
          {config.audience.kind === "records" && <div className="space-y-3"><p className="text-sm">Choose customers</p><PersonPicker ws={ws} person={null} startOpen={false} onPick={p => { if (!(config.audience.record_ids ?? []).includes(p.id)) { change({ audience: { ...config.audience, record_ids: [...(config.audience.record_ids ?? []), p.id] } }); setSelectedPeople(list => [...list, p]); } }} />{(config.audience.record_ids ?? []).map(id => <div key={id} className="flex items-center gap-2 text-sm"><span>{selectedPeople.find(p => p.id === id)?.name || selectedPeople.find(p => p.id === id)?.email || `Customer ${id.slice(0, 8)}`}</span><Button variant="ghost" onClick={() => change({ audience: { ...config.audience, record_ids: config.audience.record_ids?.filter(v => v !== id) } })}>Remove</Button></div>)}</div>}
          {config.audience.kind === "rules" && <><Rules rules={config.audience.rules ?? []} onChange={rules => change({ audience: { ...config.audience, rules } })} {...choices} /><select aria-label="Match audience rules" className={field} value={config.audience.match ?? "all"} onChange={e => change({ audience: { ...config.audience, match: e.target.value as "all" | "any" } })}><option value="all">Match all rules</option><option value="any">Match any rule</option></select></>}
          <p className="text-xs text-muted-foreground">The audience is captured at launch, including scheduled campaigns. Consent, customer availability, and frequency limits are checked again before sending.</p>
        </>}
      </>}
      {step === 1 && <>
        <label className="block text-sm">Start from a template<select aria-label="Starter template" className={`${field} block mt-1`} defaultValue="" onChange={e => { const t = templates[Number(e.target.value)]; if (t) change({ subject: t.subject, body: t.body }); }}><option value="" disabled>Choose a starter…</option>{templates.map((t, i) => <option key={t.name} value={i}>{t.name}</option>)}</select></label>
        <div className="grid gap-3 md:grid-cols-2"><Input label="Sender override" placeholder={catalog.settings.from || "Use brand sending default"} value={config.from} onChange={e => change({ from: e.target.value })} /><Input label="Reply-to override" placeholder={catalog.settings.reply_to || "Use brand reply-to"} value={config.reply_to} onChange={e => change({ reply_to: e.target.value })} /></div>
        <Input label="Subject" maxLength={500} value={config.subject} onChange={e => change({ subject: e.target.value })} />
        <label className="block text-sm">Message<textarea aria-label="Review email message" className={`${field} block w-full mt-1`} rows={9} maxLength={20000} value={config.body} onChange={e => change({ body: e.target.value })} /></label>
        <p className="text-xs text-muted-foreground">Personalize with {"{{brand.name}}"} or {"{{person.first_name|there}}"}. Review links and an unsubscribe link are added automatically.</p>
        <p className="text-sm font-medium">Review destinations</p>
        {!catalog.settings.destinations.some(d => catalog.platforms.some(p => p.id === d.platform && p.invitation)) && <p className="text-sm text-muted-foreground">Add review links in Reviews → Settings before launching.</p>}
        <div className="flex flex-wrap gap-4">{catalog.platforms.filter(p => p.invitation && catalog.settings.destinations.some(d => d.platform === p.id)).map(p => <label key={p.id} className="flex gap-2 items-center text-sm"><input type="checkbox" checked={config.platforms.includes(p.id)} onChange={e => change({ platforms: e.target.checked ? [...config.platforms, p.id] : config.platforms.filter(id => id !== p.id) })} />{p.name}</label>)}</div>
        <p className="text-xs text-muted-foreground">Use consistent customer eligibility rules and neutral wording. Destinations with restricted invitation flows are excluded.</p>
      </>}
      {step === 2 && <>
        {config.mode === "manual" ? <><Input label="Schedule (your local time; leave blank to send now)" type="datetime-local" value={sendAt} onChange={e => setSendAt(e.target.value)} /><p className="text-sm text-muted-foreground">Each eligible customer receives one email. No automatic reminders. Quiet hours may delay individual sends.</p></> : <p className="text-sm text-muted-foreground">Timing, triggers, branching and approvals are controlled by the existing workflow. Activating this campaign makes it available to the Request a review action.</p>}
        <p className="text-sm">Brand frequency rule: {catalog.settings.frequency === "once" ? "ask once ever" : catalog.settings.frequency === "interval" ? `wait ${catalog.settings.interval_days} days` : "no additional review limit"}, applied {catalog.settings.frequency_scope === "brand" ? "across all platforms" : "per platform"}.</p>
      </>}
      {step === 3 && preview && <>
        {config.mode === "manual" && <><Stats cols={3}><Stat label="Selected" value={preview.total} /><Stat label="Eligible now" value={preview.eligible} /><Stat label="Excluded" value={preview.total - preview.eligible} /></Stats>{Object.entries(preview.excluded).map(([reason, count]) => <p key={reason} className="text-sm">{count}: {why(reason)}</p>)}<p className="text-xs text-muted-foreground">Final eligibility also includes email frequency caps and sending-time checks.</p></>}
        <div className="rounded-lg border p-5 max-w-xl mx-auto space-y-4"><p className="text-xs text-muted-foreground">Email preview · sample personalization</p><p className="font-medium">{fill(config.subject)}</p><p className="whitespace-pre-wrap text-sm">{fill(config.body)}</p><div className="space-y-2">{destinations.map(d => <a key={d.platform} className="block underline text-sm" href={d.url} target="_blank" rel="noreferrer">Leave a review on {catalog.platforms.find(p => p.id === d.platform)?.name}</a>)}</div><p className="text-xs text-muted-foreground">Unsubscribe</p></div>
        <div className="flex flex-wrap items-end gap-2"><Input label="Send a test email" type="email" placeholder="you@example.com" value={testTo} onChange={e => setTestTo(e.target.value)} /><Button variant="ghost" disabled={!testTo || busy} onClick={test}>Send test</Button></div>
      </>}
    </fieldset>
    <div className="mt-5 flex flex-wrap justify-between gap-2"><Button variant="ghost" disabled={busy} onClick={onClose}>Close</Button><div className="flex flex-wrap gap-2"><Button variant="ghost" disabled={busy} onClick={async () => { setBusy(true); if (await save()) { toast("Draft saved."); await onSaved(); } setBusy(false); }}>{campaign?.status === "paused" ? "Save changes" : "Save draft"}</Button>{step < 2 && <Button disabled={busy} onClick={() => setStep(step + 1)}>Continue</Button>}{step === 2 && <Button disabled={busy} onClick={review}>Check audience & preview</Button>}{step === 3 && campaign?.status !== "paused" && <Button disabled={busy || !preview || config.mode === "manual" && !preview.eligible} onClick={launch}>{busy ? "Working…" : config.mode === "workflow" ? "Activate for workflows" : sendAt ? "Schedule campaign" : "Launch campaign"}</Button>}</div></div>
  </Card>;
}
