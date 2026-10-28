import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent, type ReactNode } from "react";
import type { Brand, Connector, ConnectorCategory, Integration, VerificationSettings, VerificationSummary } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { AppMark } from "@/components/app-mark";
import { hostedMcpUrl } from "@/lib/agentApps";
import { UrlBox } from "./ConnectAI";
import { AICostNotice } from "./AIUsage";
import { track } from "@/lib/analytics";
import { Badge, Button, confirmAction, Empty, ErrorText, field, Input, Loading, Menu, PageHeader, Stat, Stats, Status, ask, toast } from "../ui";
import { ago, plural, stamp } from "../format";
import { Icon } from "../icons";
import { DataTable } from "@/components/data-table";
import { useSiteSetupAfterConnect } from "@/components/analytics-sites";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Switch } from "@/components/ui/switch";
import { ConnectorLogo } from "@/components/connector-logo";
import { type ConnectionUsage, UsageLimits, usageLine } from "./ConnectionUsage";
import { SupplierMailboxes } from "./SupplierMailboxes";
import { LiveUpdates } from "@/components/live-updates";
import { ImportDialog } from "./ImportDialog";
import { CRMImportDialog } from "./CRMImportDialog";
import { DeploysDialog } from "./DeploysDialog";
import { connection } from "@/lib/status";
import { useBrand } from "@/lib/brand";
import { ConnectionBrandsDialog, UsedByBadge } from "@/components/connection-brands";
import { clearConnectIntent, connectIntent } from "@/components/connect-for";
import type { Tab } from "@/lib/paths";

// The order categories appear in, with what each one is for in plain words.
const categories: { id: ConnectorCategory; label: string; help: string }[] = [
  { id: "model", label: "AI models for Outflare's own agent", help: "The model Outflare itself uses to plan and run missions, with your own API key. This is not how you connect Claude or ChatGPT to Outflare: for that, use Connect your AI agent at the top of this page." },
  { id: "email_verification", label: "Email verification", help: "Check addresses before they are emailed, so bounces never hurt your mailboxes." },
  { id: "email_sending", label: "Email sending", help: "Send receipts, password resets and onboarding email from your own provider account. Set up under Lifecycle." },
  { id: "sequencer", label: "Sequencers", help: "Warm up your mailboxes in their own warm-up, or switch over: import campaigns, leads and mailboxes from the tool you use now." },
  { id: "mailbox_provider", label: "Mailbox suppliers", help: "Buy and set up sending mailboxes without leaving Outflare." },
  { id: "data", label: "Lead data", help: "Find and enrich leads." },
  { id: "analytics", label: "Analytics", help: "Read visitors, top pages, sources, search queries and conversions from the tools already on your website." },
  { id: "code", label: "Code and deploys", help: "Know when your website ships, from the repository on GitHub or the project on Vercel, so fixes are re-checked and experiments start on the real deploy. Read only." },
  { id: "seo", label: "SEO", help: "Domain rating, backlinks, keyword rankings, competitors and site audits from DataForSEO or Ahrefs, paid per call within a monthly budget." },
  { id: "payments", label: "Payments", help: "See who pays you: customers, subscriptions, payments and refunds on each person's record, with their plan and revenue. Read-only." },
  { id: "partners", label: "Partner programs", help: "Move a referral or affiliate program over from Tolt: partners, codes, referred customers and commissions. Read only; run it from Partners." },
  { id: "ads", label: "Ad platforms", help: "Read spend, clicks and platform-reported conversions from your own ad accounts. Read only; see them under Paid ads." },
  { id: "social", label: "Social", help: "Find posts your brand can help with and reply from the brand's own account, through your own app. Set up under Social." },
  { id: "crm", label: "CRM", help: "Send interested replies and meetings to your CRM." },
  { id: "calendar", label: "Calendars", help: "Count meetings booked from outreach." },
  { id: "dns", label: "DNS", help: "Add the records Outflare needs in one click: site verification, SPF, DKIM and DMARC." },
  { id: "tags", label: "Tag managers", help: "Put Outflare's website script on your site in one click, without a developer. Set up under each brand's setup." },
  { id: "registrar", label: "Domain registrars", help: "Buy and renew sending domains." },
  { id: "warehouse", label: "Warehouses", help: "Export sends, replies and results." },
  { id: "notification", label: "Notifications", help: "Get alerts where your team works." },
  { id: "custom", label: "Custom", help: "Any HTTP API or MCP server." },
];
const categoryLabel = (c: ConnectorCategory) => categories.find((x) => x.id === c)?.label ?? c;

const credits = (n?: number) => (n === undefined ? "" : `${n.toLocaleString()} credits`);

// A connector's ways to connect: OAuth (sign in) and/or an API key.
export const authOption = (def: Connector, method: "oauth2" | "api_key") => def.auth_options.find((o) => o.method === method);
// Who the person signs in with: one Google or Microsoft account covers
// several connectors.
export const provider = (def: Connector) => (def.id.startsWith("google_") ? "Google" : def.id.startsWith("outlook_") ? "Microsoft" : def.name);
export const signInWith = (def: Connector) => `Sign in with ${provider(def)}`;
function howToConnect(def: Connector) {
  const oauth = def.auth_methods.includes("oauth2"), key = def.auth_methods.includes("api_key");
  if (oauth && key) return def.id.startsWith("google_") ? "Google sign-in or service account key" : "Sign in or API key";
  return oauth ? signInWith(def) : "API key";
}

// Sends the browser to a connector's sign-in page. back lands on that page
// afterwards instead of Connectors: Get started, brand setup, or a page
// that asked for a DNS record (Email providers, Site audits, Domains).
// brand makes it that brand's own connection.
export type ReturnPage = "get-started" | "setup" | "tracking" | "providers" | "audits" | "domains";
export async function startConnectorOAuth(ws: string, def: Connector, back?: ReturnPage, brand?: string) {
  const { data, error } = await api.GET("/v1/workspaces/{ws}/integrations/oauth/{connector}/authorize", { params: { path: { ws, connector: def.id }, query: { format: "json", ...(back ? { return: back } : {}), ...(brand ? { brand } : {}) } } });
  if (error || !data) return toast(errorMessage(error), "error");
  location.assign((data as { url: string }).url);
}

// What an OAuth sign-in that came back says, from the query: shown as a
// toast and taken out of the URL. Gives the new connection's id.
export function takeConnectOutcome(): string | null {
  const q = new URLSearchParams(location.search);
  const ok = q.get("integration"), failed = q.get("connect_error");
  if (!ok && !failed) return null;
  history.replaceState(null, "", location.pathname);
  if (failed) toast(`Could not connect: ${failed}`, "error");
  else if (q.getAll("also").length) toast(`Connected, along with ${q.getAll("also").join(" and ")} from the same sign-in.`);
  else toast("Connected. It's ready to use.");
  return ok;
}

// /{workspace}/connectors/{integration id} opens one integration's own page,
// such as a mailbox supplier's mailboxes.
export function Connectors({ ws, canManage, canVerify, canImport, open, setOpen, go }: { ws: string; canManage: boolean; canVerify: boolean; canImport: boolean; open?: string; setOpen: (id?: string) => void; go: (tab: Tab, item?: string) => void }) {
  const [catalog, setCatalog] = useState<Connector[] | null>(null);
  const [integrations, setIntegrations] = useState<Integration[] | null>(null);
  // Paid connections' usage and limits, by connection id.
  const [usage, setUsage] = useState<Map<string, ConnectionUsage>>(new Map());
  const [connecting, setConnecting] = useState<{ def: Connector; replace?: Integration } | null>(null);
  const [importing, setImporting] = useState<Integration | null>(null);
  const [importingCRM, setImportingCRM] = useState<Integration | null>(null);
  const [deploys, setDeploys] = useState<Integration | null>(null);
  const [sharing, setSharing] = useState<Integration | null>(null);
  const [query, setQuery] = useState("");
  // Narrowed to one brand, the page lists the connections that brand uses:
  // its own and the workspace's shared with it.
  const { brand, query: brandQuery, brands } = useBrand(ws);
  // /{workspace}/connectors/{category}, e.g. analytics, opens the catalog on
  // that category.
  const openCategory = categories.find((c) => c.id === open)?.id;
  const [category, setCategory] = useState<ConnectorCategory | "">(openCategory ?? "");
  useEffect(() => {
    if (openCategory) setTimeout(() => document.getElementById("catalog")?.scrollIntoView({ behavior: "smooth" }), 300);
  }, [openCategory]);

  const load = useCallback(async () => {
    const [c, i, u] = await Promise.all([
      api.GET("/v1/workspaces/{ws}/connectors", { params: { path: { ws } } }),
      api.GET("/v1/workspaces/{ws}/integrations", { params: { path: { ws }, query: brandQuery } }),
      api.GET("/v1/workspaces/{ws}/connection-usage", { params: { path: { ws } } }),
    ]);
    setCatalog(c.data?.data ?? []);
    setIntegrations(i.data?.data ?? []);
    setUsage(new Map((u.data?.data ?? []).map((x) => [x.integration_id, x])));
  }, [ws, brandQuery.brand]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    load();
  }, [load]);

  // An OAuth sign-in comes back here with the outcome in the query; an
  // analytics tool then asks which sites to import.
  const siteSetup = useSiteSetupAfterConnect(ws);
  useEffect(() => {
    const id = takeConnectOutcome();
    // A sign-in started from another page's Connect button goes back there.
    if (id && connectIntent()) return goBack(true);
    void siteSetup.after(id);
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  // Opened from another page's Connect button (ConnectFor): the dialog says
  // what that page uses the service for, and connecting it or closing the
  // dialog goes back there.
  const [intent] = useState(() => connectIntent());
  const returned = useRef(false);
  function goBack(connected: boolean) {
    const it = connectIntent();
    if (!it || returned.current) return;
    returned.current = true;
    clearConnectIntent();
    track("ui.connect_for_returned", { connector: it.connector, from: it.back, connected });
    go(it.back, it.item);
  }

  const byId = useMemo(() => new Map((catalog ?? []).map((c) => [c.id, c])), [catalog]);

  // /{workspace}/connectors/posthog names a connector rather than one
  // connection when no connection has that slug yet: it opens the PostHog
  // connection if there is one, or the dialog to connect PostHog.
  const named = open && !openCategory && integrations && !integrations.some((i) => i.id === open) ? catalog?.find((c) => c.id === open || c.id.replace(/_/g, "-") === open) : undefined;
  useEffect(() => {
    if (!named || !integrations) return;
    const have = integrations.find((i) => i.connector === named.id);
    if (have && intent?.connector === named.id) {
      toast(`${named.name} is already connected.`);
      goBack(true);
    } else if (have) setOpen(have.id);
    else if (canManage) setConnecting({ def: named });
    else {
      toast(`${named.name} isn't connected yet. Ask a workspace admin to connect it.`);
      setOpen();
    }
  }, [named, integrations, canManage, setOpen]); // eslint-disable-line react-hooks/exhaustive-deps

  const startOAuth = (def: Connector, own?: boolean) => startConnectorOAuth(ws, def, undefined, own ? brand?.handle : undefined);

  // Straight to the sign-in page when that is the only way (and no brand
  // choice to make); otherwise the dialog, where the person picks sign-in
  // or a key, and who uses it.
  function connect(def: Connector) {
    if (authOption(def, "oauth2")?.available && !authOption(def, "api_key") && !brand) startOAuth(def);
    else setConnecting({ def });
  }

  async function test(i: Integration) {
    const { data, error } = await api.POST("/v1/workspaces/{ws}/integrations/{id}/test", { params: { path: { ws, id: i.id } } });
    if (error) toast(`${i.name}: ${errorMessage(error)}`, "error");
    else toast(`${i.name} signed in${data?.account?.credits !== undefined ? `, ${credits(data.account.credits)} left` : ""}.`);
    load();
  }

  async function rename(i: Integration) {
    const r = await ask({ title: `Rename "${i.name}"`, input: { label: "Name", placeholder: i.name }, choices: [{ label: "Cancel", value: "cancel" }, { label: "Rename", value: "ok", variant: "primary" }] });
    if (!r?.value.trim()) return;
    const { error } = await api.PATCH("/v1/workspaces/{ws}/integrations/{id}", { params: { path: { ws, id: i.id } }, body: { name: r.value.trim() } });
    if (error) return toast(errorMessage(error), "error");
    load();
  }

  async function disconnect(i: Integration) {
    const verifies = i.capabilities.includes("verify");
    if (!(await confirmAction(`Disconnect "${i.name}"?`, <p>Its credentials are deleted. {verifies ? "If it verifies email addresses, the free built-in checks take over." : "Anything using it stops until you connect it again."}</p>, "Disconnect"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/integrations/{id}", { params: { path: { ws, id: i.id } } });
    if (error) return toast(errorMessage(error), "error");
    toast(`Disconnected "${i.name}".`);
    load();
  }

  if (open && !openCategory && !named) {
    // Pages for one integration plug in here, by capability.
    const i = integrations?.find((x) => x.id === open);
    if (i?.capabilities.includes("vendor.mailboxes")) return <SupplierMailboxes ws={ws} integrationId={i.id} supplier={i.connector_name} canManage={canVerify} onBack={() => setOpen()} />;
    const paid = !!i && usage.has(i.id);
    return (
      <div className="space-y-6">
        <PageHeader back={{ label: "Connectors", onClick: () => setOpen() }} title={i ? <span className="flex items-center gap-2.5"><ConnectorLogo id={i.connector} />{i.name}</span> : "Connection"} description={i ? `${i.connector_name} · ${categoryLabel(i.category)}` : undefined} />
        {i && paid && <UsageLimits ws={ws} integrationId={i.id} name={i.connector_name} canManage={canManage} />}
        {i?.capabilities.includes("hooks.receive") ? (
          <LiveUpdates ws={ws} integration={i} canManage={canManage} />
        ) : i?.capabilities.includes("analytics.read") || i?.capabilities.includes("seo.read") ? (
          <Empty title="Read it on the Analytics page" action={<Button onClick={() => go("analytics")}>Open Analytics</Button>}>{i.capabilities.includes("seo.read") ? `Rankings, links and budget for ${i.connector_name}, next to each website.` : `Traffic, pages, sources, search queries and events from ${i.connector_name}.`}</Empty>
        ) : integrations === null ? <Loading /> : paid ? null : <Empty title={i ? "Nothing more to show for this connection yet" : "This connection doesn't exist"}>{i ? "Go back to Connectors to test, rename or disconnect it." : "It may have been disconnected. Go back to Connectors to see what is connected."}</Empty>}
      </div>
    );
  }

  const q = query.trim().toLowerCase();
  const shown = (catalog ?? []).filter((c) => c.auth !== "none" && (!category || c.category === category) && (!q || `${c.name} ${c.summary} ${categoryLabel(c.category)}`.toLowerCase().includes(q)));

  return (
    <div className="space-y-8">
      <PageHeader
        title="Connectors"
        description={brand
          ? <>The tools {brand.name} uses: its own connections, and the ones connected once for the workspace and shared with it. Everything on this page works over the API and MCP too.</>
          : <>Plug Outflare into the tools you already use. Nothing here is required: add mailboxes and leads yourself and you can send today. Connect a tool once and every brand can use it. Everything on this page works over the API and MCP too.</>}
      />

      <AgentConnect go={go} />

      {integrations === null || catalog === null ? (
        <Loading />
      ) : (
        <>
          <section className="space-y-3">
            <h2 className="flex items-baseline gap-2 text-[13px] font-semibold">Connected {integrations.length > 0 && <span className="font-mono text-[12px] font-normal text-muted-foreground">{integrations.length}</span>}</h2>
            {integrations.length === 0 ? (
              <Empty title="Nothing connected, and that's fine">Outflare runs on its own. Connect a service below when you want it to do more, such as checking addresses with a verification service or importing from the sequencer you use now.</Empty>
            ) : (
              <DataTable
                data={integrations}
                rowId={(i) => i.id}
                columnsMenu={false}
                initialSort={[{ id: "name", desc: false }]}
                columns={[
                  {
                    id: "name",
                    header: "Name",
                    accessorFn: (i) => i.name,
                    cell: ({ row: { original: i } }) => (
                      <div className="flex min-w-0 items-center gap-3">
                        <ConnectorLogo id={i.connector} />
                        <div className="min-w-0">
                          <div className="font-medium">{i.name}</div>
                          <div className="text-[12px] text-muted-foreground">{i.connector_name} · {categoryLabel(i.category)}</div>
                        </div>
                      </div>
                    ),
                  },
                  {
                    id: "status",
                    header: "Status",
                    accessorFn: (i) => i.status,
                    cell: ({ row: { original: i } }) =>
                      i.status === "error" ? (
                        <div className="max-w-64">
                          <Status tone={connection.failing.tone}>{connection.failing.label}</Status>
                          {i.last_error && <div className="mt-0.5 text-[12px] text-muted-foreground">{i.last_error}</div>}
                        </div>
                      ) : (
                        <Status tone={connection.ok.tone}>{connection.ok.label}</Status>
                      ),
                  },
                  {
                    id: "account",
                    header: "Account",
                    accessorFn: (i) => i.account?.email ?? i.account?.name ?? "",
                    cell: ({ row: { original: i } }) => (
                      <div className="text-[12px]">
                        <div>{i.account?.email || i.account?.name || <span className="text-muted-foreground">—</span>}</div>
                        {i.account?.credits !== undefined && <div className="font-mono text-muted-foreground">{credits(i.account.credits)}</div>}
                        {usage.has(i.id) && <button type="button" className="text-left text-muted-foreground underline-offset-2 hover:underline" onClick={() => setOpen(i.id)}>{usageLine(usage.get(i.id)!)}</button>}
                      </div>
                    ),
                  },
                  ...(brands.length > 0 ? [{ id: "brands", header: brand ? "From" : "Used by", accessorFn: (i: Integration) => (i.brand_id ? "1" : "0") + i.brand_ids.length, cell: ({ row: { original: i } }: { row: { original: Integration } }) => <UsedByBadge i={i} brands={brands} brand={brand} /> }] : []),
                  { id: "checked", header: "Last checked", accessorFn: (i) => i.last_checked_at ?? "", cell: ({ row: { original: i } }) => <span className="whitespace-nowrap text-muted-foreground" title={i.last_checked_at ? stamp(i.last_checked_at) : undefined}>{i.last_checked_at ? ago(i.last_checked_at) : "Never"}</span> },
                  {
                    id: "actions",
                    header: () => <span className="sr-only">Actions</span>,
                    enableSorting: false,
                    meta: { align: "right" },
                    cell: ({ row: { original: i } }) => (
                      <div className="flex items-center justify-end gap-1">
                        {i.capabilities.includes("vendor.mailboxes") && <Button variant="ghost" onClick={() => setOpen(i.id)}>Mailboxes</Button>}
                        {i.capabilities.includes("hooks.receive") && <Button variant="ghost" onClick={() => setOpen(i.id)}>Live updates</Button>}
                        {usage.has(i.id) && !i.capabilities.includes("hooks.receive") && <Button variant="ghost" onClick={() => setOpen(i.id)}>Usage</Button>}
                        {canImport && i.capabilities.includes("import.campaigns") && <Button onClick={() => setImporting(i)}>Import</Button>}
                        {canManage && i.capabilities.includes("import.crm") && <Button onClick={() => setImportingCRM(i)}>Import</Button>}
                        {i.capabilities.includes("deploys.read") && <Button variant="ghost" onClick={() => setDeploys(i)}>Deploys</Button>}
                        {canManage && <Button variant="ghost" onClick={() => test(i)}>Test</Button>}
                        {canManage && (
                          <Menu
                            items={[
                              { label: "Rename", onClick: () => rename(i) },
                              brands.length > 0 && { label: "Choose who uses it", onClick: () => setSharing(i) },
                              i.auth_method === "oauth2" && !!byId.get(i.connector) && !!authOption(byId.get(i.connector)!, "oauth2")?.available && { label: "Sign in again", onClick: () => startOAuth(byId.get(i.connector)!) },
                              !!byId.get(i.connector) && !!authOption(byId.get(i.connector)!, "api_key") && { label: i.auth_method === "oauth2" ? "Use an API key instead" : "Replace API key", onClick: () => setConnecting({ def: byId.get(i.connector)!, replace: i }) },
                              { label: "Disconnect", onClick: () => disconnect(i), danger: true },
                            ]}
                          />
                        )}
                      </div>
                    ),
                  },
                ]}
              />
            )}
          </section>

          <Verification ws={ws} integrations={integrations} canEdit={canVerify} onConnect={() => { setCategory("email_verification"); document.getElementById("catalog")?.scrollIntoView({ behavior: "smooth" }); }} />

          <section id="catalog" className="scroll-mt-6 space-y-4">
            <div className="flex flex-wrap items-end justify-between gap-3">
              <div>
                <h2 className="text-[13px] font-semibold">Add a connector</h2>
                <p className="text-[12px] text-muted-foreground">{plural(catalog.filter((c) => c.status === "available" && c.auth !== "none").length, "service")} ready to connect, more on the way.</p>
              </div>
              <div className="flex flex-wrap gap-2">
                <input type="search" aria-label="Search connectors" placeholder="Search connectors…" className={`${field} w-56`} value={query} onChange={(e) => setQuery(e.target.value)} />
                <select aria-label="Category" className={field} value={category} onChange={(e) => setCategory(e.target.value as ConnectorCategory | "")}>
                  <option value="">All categories</option>
                  {categories.filter((c) => catalog.some((d) => d.category === c.id && d.auth !== "none")).map((c) => <option key={c.id} value={c.id}>{c.label}</option>)}
                </select>
              </div>
            </div>
            {shown.length === 0 && <Empty title="No connectors match">Try another word, or <button className="underline" onClick={() => { setQuery(""); setCategory(""); }}>show them all</button>. Missing one you need? Any HTTP API or MCP server can be added as a custom connector.</Empty>}
            {categories.filter((c) => shown.some((d) => d.category === c.id)).map((c) => (
              <div key={c.id} className="space-y-2">
                <div>
                  <h3 className="font-mono text-[11px] tracking-wider text-muted-foreground uppercase">{c.label}</h3>
                  <p className="text-[12px] text-muted-foreground">{c.help}</p>
                </div>
                <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
                  {shown.filter((d) => d.category === c.id).map((d) => <ConnectorCard key={d.id} def={d} also={(d.also_connects ?? []).map((id) => byId.get(id)?.name).filter(Boolean).join(" and ")} canManage={canManage} onConnect={() => connect(d)} />)}
                </div>
              </div>
            ))}
          </section>
        </>
      )}

      {importing && <ImportDialog ws={ws} integration={importing} onClose={() => setImporting(null)} go={go} />}
      {importingCRM && <CRMImportDialog ws={ws} integration={importingCRM} onClose={() => setImportingCRM(null)} />}
      {sharing && <ConnectionBrandsDialog ws={ws} integration={sharing} brands={brands} onClose={() => setSharing(null)} onDone={load} />}
      {deploys && <DeploysDialog ws={ws} integration={deploys} canManage={canManage} onClose={() => setDeploys(null)} />}
      {connecting && <ConnectDialog ws={ws} def={connecting.def} replace={connecting.replace} brand={brand} why={intent?.connector === connecting.def.id ? intent.why : undefined} onSignIn={(own) => startOAuth(connecting.def, own)} onClose={() => { setConnecting(null); if (intent?.connector === connecting.def.id) goBack(false); else if (named) setOpen(); }} onDone={(i) => { if (i && intent?.connector === i.connector) return goBack(true); load(); if (i && canImport && i.capabilities.includes("import.campaigns")) setImporting(i); if (i && canManage && i.capabilities.includes("import.crm")) setImportingCRM(i); if (i && i.capabilities.includes("deploys.read")) setDeploys(i); void siteSetup.after(i?.id); }} />}
      {siteSetup.dialog}
    </div>
  );
}

// Connectors Outflare looks after carry no badge; the rest say who built
// them and whether that publisher is verified.
function TrustBadge({ def }: { def: Connector }) {
  if (def.trust === "verified") return <Badge tone="green" title={trustHelp(def)}>Verified</Badge>;
  if (def.trust === "community") return <Badge title={trustHelp(def)}>Community</Badge>;
  return null;
}

function trustHelp(def: Connector) {
  const by = def.publisher?.name ?? "another company";
  return def.trust === "verified"
    ? `Built by ${by}, a verified publisher. Outflare reviewed it before listing it.`
    : `Built by ${by}. Outflare reviewed it before listing it, but hasn't verified the publisher yet.`;
}

// Routers are a plain fact, data residency is a warning, the rest is news.
const tagTone = (label: string) => (label === "Router" ? "grey" : label === "Hosted in China" ? "amber" : "blue");

export function ConnectorCard({ def, also, canManage, onConnect }: { def: Connector; also?: string; canManage: boolean; onConnect: () => void }) {
  const planned = def.status === "planned";
  const blocked = !def.configured && !planned;
  // Straight to the provider when signing in is the only way.
  const leaves = !!authOption(def, "oauth2")?.available && !authOption(def, "api_key");
  let action: ReactNode;
  if (planned) action = <Badge>Planned</Badge>;
  else if (!canManage) action = <span className="text-[12px] text-muted-foreground">An admin can connect this</span>;
  else if (blocked) action = <span className="text-[12px] text-muted-foreground" title={authOption(def, "oauth2")?.reason}>Needs server setup</span>;
  else
    action = (
      <Button variant={def.connected > 0 ? "ghost" : "primary"} onClick={onConnect}>
        {leaves ? <>Connect <Icon name="external" className="size-3.5" /></> : def.connected > 0 ? "Add another" : "Connect"}
      </Button>
    );
  return (
    <div className={`flex flex-col rounded-lg border bg-card p-4 shadow-xs ${planned ? "opacity-70" : ""}`}>
      <div className="flex items-start gap-3">
        <ConnectorLogo id={def.id} size="lg" />
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1 font-medium">
            {def.name}
            {def.connected > 0 && <Badge tone="green">{def.connected > 1 ? `${def.connected} connected` : "Connected"}</Badge>}
            {(def.tags ?? []).map((t) => <Badge key={t.label} tone={tagTone(t.label)} title={t.help}>{t.label}</Badge>)}
            <TrustBadge def={def} />
          </div>
          <div className="mt-0.5 text-[12px] text-muted-foreground">{howToConnect(def)}{def.pricing ? ` · ${def.pricing}` : ""}</div>
        </div>
      </div>
      <p className="mt-2 flex-1 text-[13px] text-muted-foreground">
        {def.summary}
        {also && !planned && <span className="mt-1 block text-foreground/80">The same sign-in also connects {also}.</span>}
      </p>
      <div className="mt-3 flex items-center justify-between gap-2">
        {def.docs_url || def.website ? (
          <a className="text-[12px] text-muted-foreground underline decoration-border underline-offset-2 hover:text-foreground" href={def.docs_url || def.website} target="_blank" rel="noreferrer">
            {def.docs_url ? "Docs" : "Website"}
          </a>
        ) : <span />}
        {action}
      </div>
    </div>
  );
}

// brand: the page is narrowed to one brand, so the person picks whether
// the new connection is shared from the workspace or the brand's own.
export function ConnectDialog({ ws, def, replace, brand, why, onSignIn, onClose, onDone }: { ws: string; def: Connector; replace?: Integration; brand?: Brand; why?: string; onSignIn: (own: boolean) => void; onClose: () => void; onDone: (i?: Integration) => void }) {
  const [own, setOwn] = useState(false);
  const oauth = authOption(def, "oauth2"), key = authOption(def, "api_key");
  const canSignIn = !!oauth?.available && !replace;
  // Signing in is the recommended way; the key form opens on request.
  const [useKey, setUseKey] = useState(!canSignIn);
  const [name, setName] = useState("");
  const [values, setValues] = useState<Record<string, string>>({});
  const [useForVerify, setUseForVerify] = useState(def.capabilities.includes("verify"));
  // An AI model runs on the workspace's own provider bill: the person
  // acknowledges that before connecting one.
  const isAI = def.category === "model";
  const [costOK, setCostOK] = useState(!isAI || !!replace);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!useKey || !costOK) return;
    if (isAI && !replace) track("ui.ai_cost_notice_accepted", { connector: def.id, via: "api_key" });
    setBusy(true);
    setError("");
    const creds = Object.fromEntries(Object.entries(values).map(([k, v]) => [k, v.trim()]).filter(([, v]) => v));
    const res = replace
      ? await api.PATCH("/v1/workspaces/{ws}/integrations/{id}", { params: { path: { ws, id: replace.id } }, body: { credentials: creds } })
      : await api.POST("/v1/workspaces/{ws}/integrations", { params: { path: { ws } }, body: { connector: def.id, credentials: creds, ...(name.trim() ? { name: name.trim() } : {}), ...(own && brand ? { brand_id: brand.id } : {}) } });
    if (res.error || !res.data) {
      setBusy(false);
      return setError(errorMessage(res.error));
    }
    const i = res.data;
    if (!replace && useForVerify) {
      await api.PUT("/v1/workspaces/{ws}/verification-settings", { params: { path: { ws } }, body: { integration_id: i.id } });
    }
    toast(`${replace ? "Updated" : "Connected"} ${i.name}${i.account?.credits !== undefined ? `, ${credits(i.account.credits)} left` : ""}.${!replace && useForVerify ? " It now verifies every address." : ""}`);
    // Done first, so a page that asked for this connection is gone back to
    // as connected rather than as closed.
    onDone(replace ? undefined : i);
    onClose();
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <form onSubmit={submit} className="space-y-4">
          <DialogHeader className="text-left">
            <DialogTitle className="flex items-center gap-2.5 text-[15px]"><ConnectorLogo id={def.id} />{replace ? (replace.auth_method === "oauth2" ? `Use an API key for "${replace.name}"` : `Replace the key for "${replace.name}"`) : `Connect ${def.name}`}</DialogTitle>
            <DialogDescription className="text-[13px]">{why ?? (def.description || def.summary)}</DialogDescription>
            {(def.tags ?? []).filter((t) => t.label !== "Router").map((t) => <p key={t.label} className="text-[12px] text-muted-foreground"><Badge tone={tagTone(t.label)}>{t.label}</Badge> {t.help}</p>)}
            {def.trust !== "outflare" && <p className="text-[12px] text-muted-foreground"><TrustBadge def={def} /> {trustHelp(def)}</p>}
          </DialogHeader>
          {brand && !replace && (
            <fieldset className="space-y-1.5">
              <legend className="mb-1.5 text-[13px] font-medium">Who uses it</legend>
              <label className="flex items-start gap-2.5 text-[13px]">
                <input type="radio" name="connect-for" className="mt-1" checked={!own} onChange={() => setOwn(false)} />
                <span>Every brand <span className="block text-[12px] text-muted-foreground">Connect it once for the workspace. You can limit it to some brands later.</span></span>
              </label>
              <label className="flex items-start gap-2.5 text-[13px]">
                <input type="radio" name="connect-for" className="mt-1" checked={own} onChange={() => setOwn(true)} />
                <span>Only {brand.name} <span className="block text-[12px] text-muted-foreground">{brand.name} has its own {def.name} account. It replaces any shared {def.name} connection for {brand.name}.</span></span>
              </label>
            </fieldset>
          )}
          {canSignIn && (
            <div className="space-y-1.5">
              <Button type="button" className="w-full" disabled={!costOK} onClick={() => { if (isAI) track("ui.ai_cost_notice_accepted", { connector: def.id, via: "oauth" }); onSignIn(own); }}>{signInWith(def)} <Icon name="external" className="size-3.5" /></Button>
              <p className="text-[12px] text-muted-foreground">Recommended. You approve access on {provider(def)}'s own page, with nothing to copy, and can revoke it there any time.</p>
            </div>
          )}
          {canSignIn && key && !useKey && (
            <button type="button" className="block w-full rounded-md py-1.5 text-center text-[13px] text-muted-foreground underline decoration-border underline-offset-2 hover:text-foreground" onClick={() => setUseKey(true)}>
              {def.id.startsWith("google_") ? "Use a service account key instead" : "Use an API key instead"}
            </button>
          )}
          {canSignIn && useKey && <div className="flex items-center gap-3 text-[12px] text-muted-foreground"><span className="h-px flex-1 bg-border" />or paste a key<span className="h-px flex-1 bg-border" /></div>}
          {!replace && oauth && !oauth.available && key && <p className="text-[12px] text-muted-foreground" title={oauth.reason}>{signInWith(def)} isn't set up on this server yet, so connect it with a key.</p>}
          {useKey && def.fields.map((f) => (
            <Input
              key={f.name}
              label={f.label + (f.optional ? " (optional)" : "")}
              type={f.secret ? "password" : "text"}
              autoComplete="off"
              spellCheck={false}
              placeholder={f.placeholder}
              hint={f.help}
              required={!f.optional}
              value={values[f.name] ?? ""}
              onChange={(e) => setValues({ ...values, [f.name]: e.target.value })}
            />
          ))}
          {useKey && def.key_url && (
            <p className="text-[12px] text-muted-foreground">
              Find it in your {def.name} account:{" "}
              <a className="underline hover:text-foreground" href={def.key_url} target="_blank" rel="noreferrer">open the API key page</a>.
              {why && def.website && <>{" "}No account yet? <a className="underline hover:text-foreground" href={def.website} target="_blank" rel="noreferrer">Create one</a>.</>}
            </p>
          )}
          {useKey && !replace && !why && <Input label="Name (optional)" placeholder={def.name} hint="Tells accounts apart if you connect more than one." value={name} onChange={(e) => setName(e.target.value)} />}
          {useKey && !replace && def.capabilities.includes("verify") && (
            <label className="flex items-start gap-3 rounded-lg border p-3">
              <Switch checked={useForVerify} onCheckedChange={setUseForVerify} className="mt-0.5" />
              <span className="text-[13px]">
                <span className="block font-medium">Use it to verify email addresses</span>
                <span className="block text-[12px] text-muted-foreground">Replaces the built-in checks. If it runs out of credits, the built-in checks take over so sending isn't held up.</span>
              </span>
            </label>
          )}
          {useKey && <p className="text-[12px] text-muted-foreground">The key is tested before it's saved, stored encrypted, and never shown again.</p>}
          {isAI && !replace && (
            <div className="space-y-2">
              <AICostNotice />
              <label className="flex items-start gap-2.5 text-[13px]">
                <input type="checkbox" className="mt-0.5 size-4" checked={costOK} onChange={(e) => setCostOK(e.target.checked)} required />
                <span>I understand {def.name} bills my account for every AI call Outflare makes with this key, that managing that spend is my responsibility, and that I should set a hard limit with {def.name}.</span>
              </label>
            </div>
          )}
          <ErrorText>{error}</ErrorText>
          <DialogFooter className="flex-row justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
            {useKey && <Button disabled={busy || !costOK}>{busy ? "Testing the key…" : replace ? "Test and save" : "Test and connect"}</Button>}
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// Which service checks addresses, and what happens to each answer.
function Verification({ ws, integrations, canEdit, onConnect }: { ws: string; integrations: Integration[]; canEdit: boolean; onConnect: () => void }) {
  const [sum, setSum] = useState<VerificationSummary | null>(null);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    const { data } = await api.GET("/v1/workspaces/{ws}/verifications/summary", { params: { path: { ws } } });
    if (data) setSum(data);
  }, [ws]);

  useEffect(() => {
    load();
  }, [load, integrations]);

  // Re-read while addresses are waiting, so the numbers move.
  useEffect(() => {
    if (!sum?.pending) return;
    const t = setTimeout(load, 5000);
    return () => clearTimeout(t);
  }, [sum, load]);

  if (!sum) return null;
  const set = sum.settings;
  const verifiers = integrations.filter((i) => i.capabilities.includes("verify"));

  async function save(patch: Partial<VerificationSettings>) {
    setError("");
    const { error } = await api.PUT("/v1/workspaces/{ws}/verification-settings", { params: { path: { ws } }, body: patch });
    if (error) return setError(errorMessage(error));
    load();
  }

  async function verifyAll() {
    const { data, error } = await api.POST("/v1/workspaces/{ws}/verifications", { params: { path: { ws } }, body: { unverified: true } });
    if (error) return toast(errorMessage(error), "error");
    toast(`Checking ${plural(data?.queued ?? 0, "address", "addresses")}. Results appear here as they come in.`);
    load();
  }

  const todo = sum.unverified + sum.stale;
  const toggles: { key: keyof VerificationSettings; label: string; help: string }[] = [
    { key: "verify_on_import", label: "Check new leads when they're imported", help: "Addresses are checked in the background." },
    { key: "verify_before_send", label: "Hold each lead's first email until its address is checked", help: "Waits a few minutes at most. Off sends without waiting." },
    { key: "block_risky", label: "Skip risky addresses", help: "Role addresses like info@ and anything the service flags as risky." },
    { key: "block_catch_all", label: "Skip catch-all domains", help: "Domains that accept every address, so the mailbox can't be confirmed." },
    { key: "block_unknown", label: "Skip addresses that couldn't be checked", help: "Off is usual: the built-in checks can't confirm mailboxes, so many answers are unknown." },
  ];

  return (
    <section className="space-y-3">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h2 className="text-[13px] font-semibold">Email verification</h2>
          <p className="text-[12px] text-muted-foreground">Every address is checked before its first email. Undeliverable addresses are never emailed.</p>
        </div>
        {canEdit && todo > 0 && <Button variant="ghost" onClick={verifyAll}>Check {plural(todo, "lead")} now</Button>}
      </div>
      <Stats cols={5}>
        <Stat label="Deliverable" value={sum.deliverable.toLocaleString()} sub={`of ${plural(sum.leads, "lead")}`} tone={sum.deliverable ? "green" : undefined} />
        <Stat label="Undeliverable" value={sum.undeliverable.toLocaleString()} sub="never emailed" tone={sum.undeliverable ? "red" : undefined} />
        <Stat label="Risky" value={(sum.risky + sum.catch_all).toLocaleString()} sub={`${sum.catch_all.toLocaleString()} catch-all`} tone={sum.risky + sum.catch_all ? "amber" : undefined} />
        <Stat label="Not checked" value={todo.toLocaleString()} sub={sum.stale ? `${sum.stale.toLocaleString()} out of date` : `${sum.unknown.toLocaleString()} unknown`} />
        <Stat label="Checking now" value={sum.pending.toLocaleString()} sub={sum.pending ? "updates live" : "nothing waiting"} />
      </Stats>
      <div className="grid gap-4 rounded-lg border bg-card p-4 shadow-xs lg:grid-cols-[18rem_1fr]">
        <div className="space-y-2">
          <label className="block">
            <span className="mb-1.5 block text-[13px] font-medium">Checked by</span>
            <select className={`${field} w-full`} disabled={!canEdit} value={set.integration_id ?? ""} onChange={(e) => save({ integration_id: e.target.value || "builtin" })}>
              <option value="">Built-in checks (free)</option>
              {verifiers.map((i) => <option key={i.id} value={i.id}>{i.name}{i.status === "error" ? " (needs attention)" : ""}</option>)}
            </select>
          </label>
          <p className="text-[12px] text-muted-foreground">
            {set.integration_id
              ? "If it fails or runs out of credits, the built-in checks take over and it shows under Connected."
              : <>The built-in checks catch typos, disposable domains and domains without mail servers, but can't confirm a mailbox exists. {verifiers.length === 0 && <button className="underline hover:text-foreground" onClick={onConnect}>Connect a verification service</button>}</>}
          </p>
          <label className="block pt-2">
            <span className="mb-1.5 block text-[13px] font-medium">Check again after</span>
            <span className="flex items-center gap-2">
              <input type="number" min={1} max={3650} className={`${field} w-20`} disabled={!canEdit} defaultValue={set.max_age_days} key={set.max_age_days} onBlur={(e) => { const n = Number(e.target.value); if (n !== set.max_age_days && n >= 1) save({ max_age_days: n }); }} />
              <span className="text-[13px] text-muted-foreground">days</span>
            </span>
          </label>
        </div>
        <div className="divide-y">
          {toggles.map((t) => (
            <label key={t.key} className="flex items-start justify-between gap-4 py-2.5 first:pt-0 last:pb-0">
              <span className="text-[13px]">
                <span className="block font-medium">{t.label}</span>
                <span className="block text-[12px] text-muted-foreground">{t.help}</span>
              </span>
              <Switch disabled={!canEdit} checked={set[t.key] as boolean} onCheckedChange={(v) => save({ [t.key]: v })} />
            </label>
          ))}
        </div>
      </div>
      <ErrorText>{error}</ErrorText>
    </section>
  );
}

// The other direction from every connector below: Claude, ChatGPT or Cursor
// connecting to Outflare over MCP. People look for it here first.
function AgentConnect({ go }: { go: (tab: "ai") => void }) {
  const [url, setUrl] = useState<string | null>(null);
  useEffect(() => {
    hostedMcpUrl().then(setUrl);
  }, []);
  const open = () => { track("ui.agent_connect_prompted", { from: "connectors" }); go("ai"); };
  return (
    <section aria-labelledby="agent-connect" className="space-y-3 rounded-lg border border-brand-strong/40 bg-brand/5 p-4 shadow-xs dark:border-brand/40">
      <div className="flex items-start gap-3">
        <AppMark app="claude" className="size-9 shrink-0" />
        <div className="min-w-0 flex-1">
          <h2 id="agent-connect" className="text-[14px] font-semibold">Connect your AI agent (MCP)</h2>
          <p className="text-[13px] text-muted-foreground">Use Outflare from Claude, ChatGPT, Cursor or any MCP app. Add this URL as a connector in your app and approve it. No key to copy.</p>
        </div>
      </div>
      {url === "" ? (
        <p className="text-[13px]">This server has no hosted MCP address. <button type="button" onClick={open} className="font-medium underline underline-offset-2">See how to connect with an API token</button>.</p>
      ) : (
        <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
          <div className="min-w-0 flex-1">{url ? <UrlBox url={url} /> : <div className="h-9 animate-pulse rounded-md bg-muted" />}</div>
          <Button onClick={open}>Steps for your app →</Button>
        </div>
      )}
    </section>
  );
}
