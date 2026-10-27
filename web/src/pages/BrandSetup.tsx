import { useCallback, useEffect, useState } from "react";
import type { components, Connector } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Empty, Loading, PageHeader, Status, field, toast } from "../ui";
import { Icon } from "../icons";
import { cn } from "@/lib/utils";
import { track } from "@/lib/analytics";
import { setBrand, useBrand } from "@/lib/brand";
import { parsePath, type Tab } from "@/lib/paths";
import { ConnectorLogo } from "@/components/connector-logo";
import { BusinessModelPicker } from "@/components/business-model-picker";
import { ConnectDialog, ConnectorCard, authOption, startConnectorOAuth, takeConnectOutcome } from "./Connectors";
import { DNSOneClick, heldByMission, takeDNSFocus } from "@/components/dns-one-click";
import { AIPromptPanel, Code, CopyText, DeveloperPanel, Panel, Snippet } from "@/components/install-methods";

type Setup = components["schemas"]["BrandSetup"];
type Step = components["schemas"]["BrandSetupStep"];
type StepID = components["schemas"]["BrandSetupStepID"];
type Install = components["schemas"]["BrandSetupInstall"];
type Health = components["schemas"]["InstallHealth"];
type Check = components["schemas"]["InstallCheck"];
type Method = Install["methods"][number];
type Container = { id: string; name: string; public_id: string; account: string; domains: string[]; matches_website: boolean };

// A brand's setup checklist: the website script, verifying the site, then
// the tools most brands connect, in that order. Each install step offers
// every way to do it, quickest first: one click through the workspace's own
// Tag Manager or Cloudflare, a message for a developer, a prompt for an AI
// coding tool, or the snippet itself. Whether a step is done comes from the
// workspace, so connecting a tool anywhere ticks it off here too. People
// can leave at any point; Home shows where they got to.
export function BrandSetup({ ws, canManage, go }: { ws: string; canManage: boolean; go: (t: Tab) => void }) {
  const { brand, active, loaded } = useBrand(ws);
  const [setup, setSetup] = useState<Setup | null>(null);
  const [health, setHealth] = useState<Check[]>([]);
  const [error, setError] = useState("");
  const [open, setOpen] = useState<StepID | null | undefined>(undefined);
  const [catalog, setCatalog] = useState<Connector[]>([]);
  const handle = brand?.handle;

  const load = useCallback(async () => {
    if (!handle) return;
    const path = { params: { path: { ws, brand: handle } } };
    const [{ data, error }, h] = await Promise.all([
      api.GET("/v1/workspaces/{ws}/brands/{brand}/setup", path),
      api.GET("/v1/workspaces/{ws}/install-health", { params: { path: { ws }, query: { brand: handle } } }),
    ]);
    if (error || !data) return setError(errorMessage(error));
    setError("");
    setSetup(data);
    setHealth((h.data as Health | undefined)?.checks ?? []);
    setOpen((cur) => (cur === undefined ? data.next_step : cur));
    return data;
  }, [ws, handle]);

  useEffect(() => {
    setSetup(null);
    setOpen(undefined);
    const focus = takeDNSFocus();
    if (takeConnectOutcome() && focus === "verify_site") setOpen(focus);
    void load().then((d) => d && track("ui.setup_opened", { done: d.done, total: d.total, brand_id: d.brand_id }));
    api.GET("/v1/workspaces/{ws}/connectors", { params: { path: { ws } } }).then(({ data }) => setCatalog(data?.data ?? []));
  }, [load]); // eslint-disable-line react-hooks/exhaustive-deps

  // While an install runs or Outflare waits for a visit or the DNS, look
  // again every few seconds so the step ticks off on its own.
  const busy = setup?.steps.some((s) => s.status === "waiting" || s.install?.last?.status === "queued");
  useEffect(() => {
    if (!busy) return;
    const t = setInterval(load, 5000);
    return () => clearInterval(t);
  }, [busy, load]);

  const goPage = (page: string) => {
    const tab = parsePath(page).tab;
    if (tab) go(tab);
  };

  if (!loaded) return <Loading />;
  if (!brand) {
    return (
      <div className="space-y-6">
        <PageHeader title="Set up a brand" description="Each brand has its own website and tools. Pick the one to set up." />
        {active.length === 0 ? (
          <Empty title="No brands yet" action={<Button onClick={() => go("brands")}>Add a brand</Button>}>Add your brand and its website first.</Empty>
        ) : (
          <div className="grid gap-2 sm:grid-cols-2">
            {active.map((b) => (
              <button key={b.id} onClick={() => setBrand(b.handle)} className="flex items-center justify-between rounded-lg border bg-card px-4 py-3 text-left hover:bg-accent">
                <span className="font-medium">{b.name}</span>
                <Icon name="arrow" className="size-4 text-muted-foreground" />
              </button>
            ))}
          </div>
        )}
      </div>
    );
  }
  if (error) return <p className="text-sm text-destructive">{error}</p>;
  if (!setup) return <Loading />;

  async function patch(body: { skip?: StepID[]; unskip?: StepID[]; dismissed?: boolean }) {
    const { data, error } = await api.PATCH("/v1/workspaces/{ws}/brands/{brand}/setup", { params: { path: { ws, brand: handle! } }, body });
    if (error || !data || !("steps" in data)) return toast(errorMessage(error), "error");
    setSetup(data);
    return data;
  }

  async function skip(step: Step) {
    const next = await patch(step.skipped ? { unskip: [step.id] } : { skip: [step.id] });
    if (next && !step.skipped) setOpen(next.next_step);
  }

  const pct = setup.total ? Math.round((setup.done / setup.total) * 100) : 0;

  return (
    <div className="space-y-6">
      <PageHeader
        title={`Set up ${setup.brand}`}
        description={<>Most useful first. Leave whenever you like: Home keeps your place{setup.website ? <> for <Code>{setup.website.replace(/^https?:\/\//, "")}</Code></> : null}.</>}
        action={
          <>
            <Button variant="ghost" onClick={() => go("overview")}>Continue later</Button>
            {canManage && setup.complete && !setup.dismissed && <Button onClick={async () => { if (await patch({ dismissed: true })) go("overview"); }}>Done</Button>}
          </>
        }
      />

      <div className="space-y-1.5">
        <div className="flex items-baseline justify-between text-[13px]">
          <span className="font-medium">{setup.complete ? "All set" : `${setup.done} of ${setup.total} done`}</span>
          <span className="font-mono text-[12px] text-muted-foreground">{pct}%</span>
        </div>
        <div className="h-1.5 overflow-hidden rounded-full bg-muted" role="progressbar" aria-valuenow={pct} aria-valuemin={0} aria-valuemax={100}>
          <div className="h-full rounded-full bg-brand transition-[width]" style={{ width: `${pct}%` }} />
        </div>
      </div>

      {health.length > 0 && <HealthList checks={health} onFix={(c) => (c.fix.page.endsWith("/setup") ? setOpen(c.kind === "site_unverified" ? "verify_site" : "site_script") : goPage(c.fix.page))} />}

      <ol className="divide-y rounded-lg border bg-card">
        {setup.steps.map((step, i) => (
          <li key={step.id}>
            <button
              onClick={() => {
                const next = open === step.id ? null : step.id;
                setOpen(next);
                if (next) track("ui.setup_step_opened", { step: step.id, status: step.status });
              }}
              aria-expanded={open === step.id}
              className="flex w-full items-center gap-3 px-4 py-3 text-left hover:bg-accent/50"
            >
              <StepMark step={step} n={i + 1} />
              <span className="min-w-0 flex-1">
                <span className={cn("block text-[13px] font-medium", step.done && "text-muted-foreground")}>{step.title}</span>
                {open !== step.id && step.waiting && <span className="block text-[12px] text-muted-foreground">{step.waiting}</span>}
              </span>
              {step.status === "skipped" && <Badge>Later</Badge>}
              {step.status === "waiting" && <Badge tone="blue">Checking</Badge>}
              <Icon name="chevron" className={cn("size-4 text-muted-foreground transition-transform", open === step.id && "rotate-90")} />
            </button>
            {open === step.id && (
              <div className="space-y-4 px-4 pb-4 pl-12">
                <p className="max-w-2xl text-[13px] text-muted-foreground">{step.detail}</p>
                {step.waiting && <p className="text-[13px]"><Status tone="blue">{step.waiting}</Status></p>}
                {step.id === "business_model" ? (
                  <BusinessModelPicker ws={ws} brand={handle!} model={setup.business_model} revenue={setup.revenue_models} canManage={canManage} onSaved={load} />
                ) : step.install ? (
                  <InstallStep ws={ws} brand={handle!} step={step} install={step.install} canManage={canManage} catalog={catalog} onChange={(d) => (d ? setSetup(d) : void load())} />
                ) : (
                  !step.done && <ConnectTools ws={ws} step={step} catalog={catalog} canManage={canManage} onChange={load} onMore={() => goPage(step.page)} />
                )}
                {canManage && !step.done && (
                  <div className="flex gap-2">
                    <Button variant="ghost" onClick={() => skip(step)}>{step.skipped ? "Bring it back" : "Do this later"}</Button>
                  </div>
                )}
              </div>
            )}
          </li>
        ))}
      </ol>
    </div>
  );
}

function StepMark({ step, n }: { step: Step; n: number }) {
  if (step.done) return <span className="flex size-6 shrink-0 items-center justify-center rounded-full bg-brand text-brand-foreground"><Icon name="check" className="size-3.5" /></span>;
  return <span className={cn("flex size-6 shrink-0 items-center justify-center rounded-full border font-mono text-[11px]", step.status === "waiting" ? "border-blue-500 text-blue-600 dark:text-blue-400" : "text-muted-foreground")}>{n}</span>;
}

const severityTone = { critical: "red", warning: "amber", info: "grey" } as const;

// What's out of date or broken for this brand: an old copy of the script,
// a site that stopped sending visits, a connection that needs signing in
// again.
function HealthList({ checks, onFix }: { checks: Check[]; onFix: (c: Check) => void }) {
  return (
    <section aria-labelledby="install-health" className="space-y-2 rounded-lg border border-amber-500/30 bg-amber-500/[0.04] p-4">
      <h2 id="install-health" className="flex items-center gap-2 text-[13px] font-semibold"><Icon name="fixes" className="size-4" />Needs attention</h2>
      <ul className="space-y-2">
        {checks.map((c) => (
          <li key={c.key} className="flex flex-wrap items-start justify-between gap-2">
            <div className="min-w-0 max-w-2xl">
              <p className="flex items-center gap-2 text-[13px] font-medium"><Badge tone={severityTone[c.severity]}>{c.severity}</Badge>{c.title}</p>
              <p className="text-[12px] text-muted-foreground">{c.detail}</p>
            </div>
            {c.fix.page && <Button variant="ghost" onClick={() => onFix(c)}>Fix</Button>}
          </li>
        ))}
      </ul>
    </section>
  );
}

const methodNames: Record<Method, string> = { gtm: "Google Tag Manager", dns: "Cloudflare", developer: "Send to a developer", ai: "Ask your AI coding tool", manual: "Do it myself" };
const methodIcons: Record<Method, Parameters<typeof Icon>[0]["name"]> = { gtm: "connectors", dns: "connectors", developer: "inbox", ai: "ai", manual: "code" };

// An install step: make what it installs first, then every way to put it
// on the site, quickest first.
export function InstallStep({ ws, brand, step, install, canManage, catalog, onChange, back = "setup" }: { ws: string; brand: string; step: Step; install: Install; canManage: boolean; catalog: Connector[]; onChange: (s?: Setup) => void; back?: "setup" | "tracking" }) {
  const [method, setMethod] = useState<Method>(install.methods[0] ?? "manual");
  const [busy, setBusy] = useState(false);
  const path = { params: { path: { ws, brand } } };
  const stepID = step.id as "site_script" | "verify_site";
  const made = stepID === "site_script" ? !!install.site_key_id : !!install.site_id;

  if (!made) {
    if (!canManage) return <p className="text-[13px] text-muted-foreground">An owner or admin starts this step.</p>;
    return (
      <Button
        disabled={busy}
        onClick={async () => {
          setBusy(true);
          const { data, error } = await api.POST("/v1/workspaces/{ws}/brands/{brand}/setup/prepare", { ...path, body: { step: stepID } });
          setBusy(false);
          if (error || !data || !("steps" in data)) return toast(errorMessage(error), "error");
          onChange(data);
        }}
      >
        {stepID === "site_script" ? "Get my script" : "Get my DNS record"}
      </Button>
    );
  }

  const used = (how?: string) => track("ui.setup_method_used", { step: step.id, method, how });
  const check = async () => {
    setBusy(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/brands/{brand}/setup/check", { ...path, body: { step: "verify_site" } });
    setBusy(false);
    if (error || !data || !("steps" in data)) return toast(errorMessage(error), "error");
    onChange(data);
    if (!data.steps.find((s) => s.id === "verify_site")?.done) toast("Not there yet. DNS can take a few minutes; Outflare keeps checking.");
  };

  return (
    <div className="space-y-3">
      <div role="tablist" className="flex flex-wrap gap-1.5">
        {install.methods.map((m, i) => (
          <button
            key={m}
            role="tab"
            aria-selected={method === m}
            onClick={() => setMethod(m)}
            className={cn("inline-flex h-8 items-center gap-1.5 rounded-md border px-3 text-[13px]", method === m ? "border-brand/50 bg-brand/10 font-medium" : "hover:bg-accent")}
          >
            {m === "gtm" || m === "dns" ? <ConnectorLogo id={install.connector} size="sm" /> : <Icon name={methodIcons[m]} className="size-3.5" />}
            {m === "dns" ? `${methodNames.dns} (one click)` : m === "gtm" ? `${methodNames.gtm} (one click)` : methodNames[m]}
            {i === 0 && (m === "gtm" || m === "dns") && <Badge tone="green">Quickest</Badge>}
          </button>
        ))}
      </div>

      {(method === "gtm" || method === "dns") && (
        <OneClick ws={ws} brand={brand} step={stepID} method={method} install={install} canManage={canManage} catalog={catalog} back={back} onQueued={() => { used(); onChange(); }} />
      )}
      {method === "developer" && <DeveloperPanel message={install.developer} used={used} />}
      {method === "ai" && (
        <AIPromptPanel prompt={install.ai_prompt} used={used} intro={stepID === "verify_site" ? "Paste this into Claude, ChatGPT or any agent that can reach your DNS host. It has the record exactly as it must be added." : "Paste this into Claude Code, Cursor, Lovable or whichever tool edits your site's code. It finds where the change goes and makes it."} />
      )}
      {method === "manual" && (
        <Panel>
          {stepID === "site_script" && install.snippet && (
            <>
              <p className="text-[13px] text-muted-foreground">Paste this into the <Code>&lt;head&gt;</Code> of every page, or a site-wide header setting in your site builder.</p>
              <Snippet text={install.snippet} onCopy={() => used("copied")} />
            </>
          )}
          {stepID === "verify_site" && install.record && (
            <>
              <p className="text-[13px] text-muted-foreground">Add this record where your domain's DNS is managed. Any one of the three ways below works.</p>
              <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-[13px]">
                <dt className="text-muted-foreground">Type</dt><dd className="font-mono">{install.record.type}</dd>
                <dt className="text-muted-foreground">Name</dt><dd className="font-mono">{install.record.name}</dd>
                <dt className="text-muted-foreground">Value</dt><dd className="flex items-center gap-1 font-mono break-all">{install.record.content}<CopyText text={install.record.content} icon onCopy={() => used("copied")} /></dd>
              </dl>
              {install.meta_tag && (
                <details>
                  <summary className="cursor-pointer text-[12px] text-muted-foreground hover:text-foreground">Or a meta tag, or a file</summary>
                  <div className="mt-2 space-y-2">
                    <Snippet text={install.meta_tag} onCopy={() => used("copied")} />
                    {install.file_url && <p className="text-[12px] text-muted-foreground">Or serve <Code>{install.file_url}</Code> with the text <Code>{install.file_body}</Code>.</p>}
                  </div>
                </details>
              )}
            </>
          )}
        </Panel>
      )}

      {stepID === "verify_site" && !step.done && canManage && <Button variant="ghost" disabled={busy} onClick={check}>Check now</Button>}
      {stepID === "site_script" && !step.done && back === "setup" && <p className="text-[12px] text-muted-foreground">This step ticks off by itself when the first visit arrives.</p>}
    </div>
  );
}

// The one-click path: connect the workspace's own Tag Manager or
// Cloudflare (once), then one button does the install.
function OneClick({ ws, brand, step, method, install, canManage, catalog, back, onQueued }: { ws: string; brand: string; step: "site_script" | "verify_site"; method: "gtm" | "dns"; install: Install; canManage: boolean; catalog: Connector[]; back: "setup" | "tracking"; onQueued: () => void }) {
  const def = catalog.find((c) => c.id === install.connector);
  const [connecting, setConnecting] = useState(false);
  const [containers, setContainers] = useState<Container[] | null>(null);
  const [container, setContainer] = useState("");
  const [publish, setPublish] = useState(true);
  const [busy, setBusy] = useState(false);
  const integration = install.connections[0];
  const last = install.last?.method === method ? install.last : null;

  useEffect(() => {
    if (method !== "gtm" || !integration) return;
    api.GET("/v1/workspaces/{ws}/brands/{brand}/setup/tag-containers", { params: { path: { ws, brand }, query: { integration_id: integration } } }).then(({ data, error }) => {
      if (error) return toast(errorMessage(error), "error");
      const list = ((data as { data?: Container[] })?.data ?? []) as Container[];
      setContainers(list);
      setContainer((cur) => cur || list[0]?.id || "");
    });
  }, [ws, brand, method, integration]);

  if (!canManage) return <Panel><p className="text-[13px] text-muted-foreground">An owner or admin can do this in one click.</p></Panel>;

  const name = def?.name ?? methodNames[method];
  if (method === "dns" && install.record) {
    const record = install.record;
    return (
      <DNSOneClick ws={ws} page="setup" item={step} approval={install.site_id ? { kind: "seo_site", id: install.site_id } : undefined} connections={install.connections} records={[{ type: record.type, name: record.name, value: record.content }]}
        run={last ? { status: last.status, error: last.error } : null} canManage={canManage} reload={onQueued}
        used={(how) => track("ui.dns_one_click_used", { where: "brand_setup", how })}
        intro="Connect the Cloudflare account your domain is on."
        after="Then Outflare looks for it every minute until it's found."
        onAdd={async (integration) => {
          const { data, error } = await api.POST("/v1/workspaces/{ws}/brands/{brand}/setup/install", { params: { path: { ws, brand } }, body: { step, method, integration_id: integration } });
          return { error, held: heldByMission(data) };
        }} />
    );
  }
  if (!integration) {
    const signIn = () => def && startConnectorOAuth(ws, def, back);
    return (
      <Panel>
        <p className="text-[13px] text-muted-foreground">
          {method === "gtm"
            ? "Sign in with the Google account that manages your Tag Manager container. Outflare adds one tag, in a Tag Manager workspace of its own, and never touches your other tags."
            : "Connect the Cloudflare account your domain is on. Outflare only adds records, each marked \"Added by Outflare\", and never changes or deletes one."}
        </p>
        {!def ? (
          <p className="text-[13px] text-muted-foreground">{name} can't be connected on this server yet. Pick another way above.</p>
        ) : (
          <Button onClick={() => { if (authOption(def, "oauth2")?.available && !authOption(def, "api_key")) signIn(); else setConnecting(true); }}>
            <ConnectorLogo id={def.id} size="sm" /> {method === "gtm" ? "Sign in with Google" : `Connect ${name}`}
          </Button>
        )}
        {connecting && def && <ConnectDialog ws={ws} def={def} onSignIn={() => signIn()} onClose={() => setConnecting(false)} onDone={() => { setConnecting(false); onQueued(); }} />}
      </Panel>
    );
  }

  async function run() {
    setBusy(true);
    const body = method === "gtm" ? { step, method, integration_id: integration, container, publish } : { step, method, integration_id: integration };
    const { data, error } = await api.POST("/v1/workspaces/{ws}/brands/{brand}/setup/install", { params: { path: { ws, brand } }, body });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    if (data && "mission_id" in data) toast("Waiting for approval in a mission.");
    onQueued();
  }

  const queued = last?.status === "queued";
  return (
    <Panel>
      {method === "gtm" && (
        containers === null ? <p className="text-[13px] text-muted-foreground">Finding your containers…</p>
        : containers.length === 0 ? <p className="text-[13px] text-muted-foreground">This Google account has no web container. Sign in with the account that manages your site's container, or pick another way above.</p>
        : (
          <div className="space-y-2">
            <label className="block text-[13px] font-medium" htmlFor="gtm-container">Container</label>
            <select id="gtm-container" className={cn(field, "w-full max-w-md")} value={container} onChange={(e) => setContainer(e.target.value)}>
              {containers.map((c) => <option key={c.id} value={c.id}>{c.name} ({c.public_id}){c.matches_website ? " · your website" : ""} · {c.account}</option>)}
            </select>
            <label className="flex items-center gap-2 text-[13px]">
              <input type="checkbox" checked={publish} onChange={(e) => setPublish(e.target.checked)} />
              Publish it now (untick to review it in Tag Manager first)
            </label>
          </div>
        )
      )}
      {method === "dns" && install.record && (
        <p className="text-[13px] text-muted-foreground">Adds a <Code>{install.record.type}</Code> record on <Code>{install.record.name}</Code>, then checks for it every minute until it's found.</p>
      )}
      <div className="flex flex-wrap items-center gap-3">
        <Button disabled={busy || queued || (method === "gtm" && !container)} onClick={run}>
          <Icon name="arrow" className="size-3.5" /> {queued ? "Working…" : method === "gtm" ? "Add to my site" : `Add the record in ${name}`}
        </Button>
        {last?.status === "done" && (
          <span className="text-[13px]">
            <Status tone="green">{method === "gtm" ? (last.published ? "Published" : "Added; publish it in Tag Manager") : "Record added"}</Status>
            {last.link && <a href={last.link} target="_blank" rel="noreferrer" className="ml-2 text-[12px] underline underline-offset-2">See it in Tag Manager</a>}
          </span>
        )}
        {last?.status === "failed" && <span className="text-[13px] text-destructive">{last.error}</span>}
      </div>
    </Panel>
  );
}

// A step done by connecting a tool: the suggested ones, connected right
// here, coming back to this page after a sign-in.
function ConnectTools({ ws, step, catalog, canManage, onChange, onMore }: { ws: string; step: Step; catalog: Connector[]; canManage: boolean; onChange: () => void; onMore: () => void }) {
  const [connecting, setConnecting] = useState<Connector | null>(null);
  const defs = step.connectors.map((id) => catalog.find((c) => c.id === id)).filter((c): c is Connector => !!c);
  function connect(def: Connector) {
    if (authOption(def, "oauth2")?.available && !authOption(def, "api_key")) void startConnectorOAuth(ws, def, "setup");
    else setConnecting(def);
  }
  return (
    <div className="space-y-2">
      {defs.length > 0 && <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">{defs.map((d) => <ConnectorCard key={d.id} def={d} canManage={canManage} onConnect={() => connect(d)} />)}</div>}
      <button onClick={onMore} className="text-[13px] text-muted-foreground underline underline-offset-2 hover:text-foreground">Use something else</button>
      {connecting && <ConnectDialog ws={ws} def={connecting} onSignIn={() => startConnectorOAuth(ws, connecting, "setup")} onClose={() => setConnecting(null)} onDone={() => { setConnecting(null); onChange(); }} />}
    </div>
  );
}
