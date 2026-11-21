import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import type { components, Connector } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, ErrorText, Loading, PageHeader, Status } from "../ui";
import { Icon } from "../icons";
import { cn } from "@/lib/utils";
import { track } from "@/lib/analytics";
import { setBrand, useBrand } from "@/lib/brand";
import type { Tab } from "@/lib/paths";
import { takeConnectOutcome } from "./Connectors";
import { InstallStep } from "./BrandSetup";
import { scriptHealth, SiteKeysCard } from "./SiteKeys";
import { ago, plural } from "../format";

type Setup = components["schemas"]["BrandSetup"];
type Step = components["schemas"]["BrandSetupStep"];
type SiteKey = components["schemas"]["SiteKey"];

// How a brand's script is doing, from its setup step.
type Stage = "none" | "waiting" | "receiving";
const stageOf = (step?: Step): Stage => (step?.done ? "receiving" : step?.install?.site_key_id ? "waiting" : "none");
const scriptStep = (s: Setup | null) => s?.steps.find((x) => x.id === "site_script");

/**
 * Settings → Website tracking: put Outflare's script on a brand's site in
 * three steps (get it, add it, see the first visit), the same steps as the
 * brand's setup, so finishing here ticks it off there too. Once visits
 * arrive the steps give way to one line that says whether the script is
 * working now. Below, the script itself (shown once: while the steps
 * show it, the list leaves it out): copy it, change which sites it
 * records, and clear out any spare one.
 */
export function WebsiteTracking({ ws, canManage, go }: { ws: string; canManage: boolean; go: (t: Tab) => void }) {
  const { brand, active, loaded } = useBrand(ws);
  // The guided steps and the script settings below each change what the
  // other shows (a script made, sites edited), so each change reloads both.
  const [version, setVersion] = useState(0);
  const changed = () => setVersion((v) => v + 1);
  // While the steps above are showing the script to add, the list below
  // leaves it out, so it is on the page once.
  const [inSteps, setInSteps] = useState(false);
  const header = (
    <PageHeader
      title="Website tracking"
      description="Outflare's script records every visit to your site with where it came from, so sign-ups and revenue are credited to the channel that brought them. Add it once per brand; this page says whether it's working."
    />
  );
  if (!loaded) return <div className="space-y-6">{header}<Loading /></div>;
  return (
    <div className="space-y-6">
      {header}
      {active.length === 0 ? (
        <SiteKeysCard ws={ws} canManage={canManage} />
      ) : brand ? (
        <>
          <TrackingFlow key={brand.handle} ws={ws} brand={brand.handle} brandID={brand.id} canManage={canManage} go={go} version={version} onChange={changed} onSteps={setInSteps} />
          <SiteKeysCard ws={ws} canManage={canManage} version={version} onChange={changed} hideEmpty hideSnippet={inSteps} />
        </>
      ) : (
        <>
          <BrandList ws={ws} />
          <SiteKeysCard ws={ws} canManage={canManage} hideEmpty />
        </>
      )}
    </div>
  );
}

// With every brand showing: each brand and whether its script works, to
// pick the one to set up.
function BrandList({ ws }: { ws: string }) {
  const { active } = useBrand(ws);
  const [stages, setStages] = useState<Record<string, Stage>>({});
  useEffect(() => {
    for (const b of active) {
      api.GET("/v1/workspaces/{ws}/brands/{brand}/setup", { params: { path: { ws, brand: b.handle } } }).then(({ data }) => {
        if (data) setStages((cur) => ({ ...cur, [b.handle]: stageOf(scriptStep(data)) }));
      });
    }
  }, [ws, active.length]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (Object.keys(stages).length === active.length) track("ui.tracking_opened", { brands: active.length, receiving: Object.values(stages).filter((s) => s === "receiving").length });
  }, [stages]); // eslint-disable-line react-hooks/exhaustive-deps
  return (
    <Card title="Pick a brand" description="Each brand's site gets its own script.">
      <div className="grid gap-2 sm:grid-cols-2">
        {active.map((b) => (
          <button key={b.id} onClick={() => setBrand(b.handle)} className="flex items-center justify-between gap-3 rounded-lg border bg-card px-4 py-3 text-left hover:bg-accent">
            <span className="font-medium">{b.name}</span>
            <span className="flex items-center gap-2 text-[12px] text-muted-foreground">
              <StageLabel stage={stages[b.handle]} />
              <Icon name="arrow" className="size-4" />
            </span>
          </button>
        ))}
      </div>
    </Card>
  );
}

function StageLabel({ stage }: { stage?: Stage }) {
  if (stage === "receiving") return <Status tone="green">Receiving visits</Status>;
  if (stage === "waiting") return <Status tone="blue">Waiting for a visit</Status>;
  if (stage === "none") return <Status>Not added</Status>;
  return null;
}

// One brand: get the script, add it to the site (one click through Tag
// Manager, a developer, an AI coding tool or by hand), then wait for the
// first visit and say so.
function TrackingFlow({ ws, brand, brandID, canManage, go, version, onChange, onSteps }: { ws: string; brand: string; brandID: string; canManage: boolean; go: (t: Tab) => void; version: number; onChange: () => void; onSteps: (showing: boolean) => void }) {
  const [setup, setSetup] = useState<Setup | null>(null);
  const [catalog, setCatalog] = useState<Connector[]>([]);
  // The brand's script itself, for when it last sent a visit.
  const [siteKey, setSiteKey] = useState<SiteKey | null>(null);
  const [error, setError] = useState("");
  const seen = useRef<Stage | null>(null);

  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/brands/{brand}/setup", { params: { path: { ws, brand } } });
    if (error || !data) return setError(errorMessage(error));
    setError("");
    // The script before the setup, so the first thing shown already knows
    // when it last sent a visit.
    const id = scriptStep(data)?.install?.site_key_id;
    const keys = id ? await api.GET("/v1/brands/{brand}/site-keys", { params: { path: { brand: brandID } } }) : null;
    setSiteKey(keys?.data?.data.find((k) => k.id === id) ?? null);
    setSetup(data);
  }, [ws, brand, brandID]);

  useEffect(() => {
    takeConnectOutcome();
    void load();
    api.GET("/v1/workspaces/{ws}/connectors", { params: { path: { ws } } }).then(({ data }) => setCatalog(data?.data ?? []));
  }, [load]);
  useEffect(() => {
    if (version > 0) void load();
  }, [version]); // eslint-disable-line react-hooks/exhaustive-deps

  const step = scriptStep(setup);
  const stage = stageOf(step);
  // Opened once per brand; and the moment the first visit shows up while
  // someone is watching, which is the point of this page.
  useEffect(() => {
    if (!setup) return;
    if (seen.current === null) track("ui.tracking_opened", { brands: 1, receiving: stage === "receiving" ? 1 : 0, state: stage === "receiving" ? (siteKey ? scriptHealth(siteKey) : "live") : stage });
    else if (seen.current !== "receiving" && stage === "receiving") track("ui.tracking_verified", { brand_id: brandID });
    seen.current = stage;
  }, [setup, stage, brandID]);

  useEffect(() => {
    onSteps(!!setup && stage === "waiting");
    return () => onSteps(false);
  }, [setup, stage]); // eslint-disable-line react-hooks/exhaustive-deps

  // While the script is out and no visit has come, look every few seconds.
  useEffect(() => {
    if (stage !== "waiting") return;
    const t = setInterval(load, 4000);
    return () => clearInterval(t);
  }, [stage, load]);

  if (error) return <ErrorText>{error}</ErrorText>;
  if (!setup || !step || !step.install) return <Loading />;
  const site = setup.website?.replace(/\/$/, "");
  const install = (
    <InstallStep ws={ws} brand={brand} step={step} install={step.install} canManage={canManage} catalog={catalog} onChange={(d) => (d ? setSetup(d) : void load())} back="tracking" />
  );

  // Set up and sending: say so in one line, with the proof beside it.
  if (stage === "receiving") return <ScriptStatus k={siteKey} site={site} go={go} />;

  return (
    <ol className="divide-y rounded-lg border bg-card">
      <Stage n={1} title="Get your script" done={stage !== "none"} current={stage === "none"}>
        <InstallStep ws={ws} brand={brand} step={step} install={step.install} canManage={canManage} catalog={catalog} onChange={(d) => { if (d) setSetup(d); else void load(); onChange(); }} back="tracking" />
      </Stage>
      <Stage n={2} title={`Add it to ${site ? site.replace(/^https?:\/\//, "") : "your site"}`} done={false} current={stage === "waiting"}>
        {stage !== "none" && install}
      </Stage>
      <Stage n={3} title="See your first visit" done={false} current={stage === "waiting"}>
        {stage === "waiting" && (
          <div className="space-y-2">
            <p className="flex items-center gap-2 text-[13px]">
              <span className="relative flex size-2"><span className="absolute inline-flex size-full animate-ping rounded-full bg-blue-500 opacity-60" /><span className="relative inline-flex size-2 rounded-full bg-blue-500" /></span>
              Checking every few seconds. Once the script is on your site, open it in a new tab to send the first visit.
            </p>
            {site && <a href={site} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-[13px] underline underline-offset-2">Open {site.replace(/^https?:\/\//, "")} <Icon name="arrow" className="size-3.5" /></a>}
          </div>
        )}
      </Stage>
    </ol>
  );
}

// A brand whose script has sent visits: whether it still does (a visit in
// the last day), when the last one came and how many events today. When it
// has gone quiet it points at the script just below, to check against the site.
function ScriptStatus({ k, site, go }: { k: SiteKey | null; site?: string; go: (t: Tab) => void }) {
  const quiet = !!k && scriptHealth(k) === "quiet";
  const host = site ? site.replace(/^https?:\/\//, "") : (k?.name ?? "your site");
  return (
    <div className={cn("space-y-3 rounded-lg border bg-card px-4 py-4", quiet ? "border-amber-500/40" : "border-green-600/30")}>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-start gap-3">
          <span className="relative mt-1.5 flex size-2.5 shrink-0">
            {!quiet && <span className="absolute inline-flex size-full animate-ping rounded-full bg-green-500 opacity-60" />}
            <span className={cn("relative inline-flex size-2.5 rounded-full", quiet ? "bg-amber-500" : "bg-green-500")} />
          </span>
          <div>
            <div className="flex flex-wrap items-center gap-2 text-[15px] font-semibold">
              {quiet ? `No visits from ${host} in the last 24 hours` : `Tracking is live on ${host}`}
              <Badge tone={quiet ? "amber" : "green"}>{quiet ? "Quiet" : "Live"}</Badge>
            </div>
            <p className="text-[13px] text-muted-foreground">
              {k?.last_used_at ? `Last visit ${ago(k.last_used_at)} · ${plural(k.events_today, "event")} today.` : "The script is on the site and visits are arriving."}
              {quiet ? " It worked before. If the site still gets visitors, check the script below is still in its <head>." : " Nothing more to set up."}
            </p>
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button variant="ghost" onClick={() => go("visitors")}>See who's visiting</Button>
          <Button variant="ghost" onClick={() => go("analytics")}>Website analytics</Button>
        </div>
      </div>
    </div>
  );
}

function Stage({ n, title, done, current, children }: { n: number; title: string; done: boolean; current: boolean; children?: ReactNode }) {
  return (
    <li className="space-y-3 px-4 py-4">
      <div className="flex items-center gap-3">
        {done ? (
          <span className="flex size-6 shrink-0 items-center justify-center rounded-full bg-brand text-brand-foreground"><Icon name="check" className="size-3.5" /></span>
        ) : (
          <span className={cn("flex size-6 shrink-0 items-center justify-center rounded-full border font-mono text-[11px]", current ? "border-brand text-foreground" : "text-muted-foreground")}>{n}</span>
        )}
        <span className={cn("text-[13px] font-medium", done && !current && "text-muted-foreground")}>{title}</span>
      </div>
      {current && children && <div className="pl-9">{children}</div>}
    </li>
  );
}
