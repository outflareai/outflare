import { PickBrand } from "@/components/pick-brand";
// The link autopilot (Outflare Cloud): an agent run that keeps the link
// building playbooks working toward a number of new live links a month,
// within a monthly budget, on the workspace's own accounts. The server
// lists the link_autopilot capability where it runs; its routes are the
// hosted extension's, so they are called without the typed client. A
// person approves every automation it sets up and every
// pitch, in Missions, Playbooks and Link building.

import { useEffect, useState } from "react";
import type { SEOSite } from "@outflare/sdk";
import { useBrand, pickedBrandId } from "@/lib/brand";
import { addBrandObjective, brandAgentURL, listBrandAgents, type AgentObjective, type BrandAgent } from "@/lib/brandAgent";
import { useCapabilities } from "@/lib/cloud";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, Empty, ErrorText, field, Loading, PageHeader, Stat, Stats, toast, type tones } from "../ui";
import { ago } from "../format";
import type { Tab } from "../App";

type Props = { ws: string; canManage: boolean; go: (tab: Tab, item?: string) => void };

// An autopilot is an agent run (run_id); ones started before runs existed
// have a mission instead. status is the run's or the mission's.
type Autopilot = {
  id: string; run_id: string | null; mission_id: string | null; status: string; mission_status: string; brand_id: string | null; site: string;
  target_per_month: number; monthly_budget_cents: number; created_at: string;
};
const openAutopilot = (go: (tab: Tab, item?: string) => void, a: Autopilot) => (a.run_id ? go("agents", a.run_id) : go("missions", a.mission_id ?? undefined));
type Failure = { error: { code: string; message: string } };

// call reaches the autopilot's routes, with the same session as the typed
// client and errors in the engine's shape.
async function call<T>(method: string, path: string, body?: unknown): Promise<{ data?: T; error?: Failure }> {
  try {
    const res = await fetch(path, { method, credentials: "same-origin", headers: body ? { "Content-Type": "application/json" } : {}, body: body ? JSON.stringify(body) : undefined });
    const json = await res.json().catch(() => ({}));
    return res.ok ? { data: json as T } : { error: json as Failure };
  } catch {
    return { error: { error: { code: "network_error", message: "Could not reach Outflare. Check your connection and try again." } } };
  }
}

// What the autopilot's status means for the person. Drafts are missions
// from before autopilots ran as agent runs.
const statusLabel: Record<string, [string, keyof typeof tones]> = {
  draft: ["Waiting for your approval", "amber"],
  pending_approval: ["Waiting for your approval", "amber"],
  running: ["Running", "green"],
  paused: ["Paused", "grey"],
  completed: ["Finished", "grey"],
  stopped: ["Stopped", "grey"],
  failed: ["Failed", "red"],
};
const finished = (s: string) => s === "completed" || s === "stopped" || s === "failed";
const host = (h: string) => h.replace(/^www\./, "");
const dollars = (cents: number) => `$${(cents / 100).toLocaleString(undefined, { maximumFractionDigits: cents % 100 ? 2 : 0 })}`;

// Link autopilot settings belong to one brand.
export function LinkAutopilot(props: Props) {
  const { brand, active } = useBrand(props.ws);
  if (!brand) return <PickBrand what="Link autopilot settings" brands={active.length} />;
  return <LinkAutopilotPage key={brand.id} {...props} />;
}

function LinkAutopilotPage({ ws, canManage, go }: Props) {
  const base = `/v1/workspaces/${ws}/cloud/link-autopilot`;
  const { brand } = useBrand(ws);
  const [list, setList] = useState<Autopilot[] | null>(null);
  const [sites, setSites] = useState<SEOSite[]>([]);
  const [agents, setAgents] = useState<BrandAgent[]>([]);
  const [error, setError] = useState("");
  const caps = useCapabilities();
  // With brand agents on the server, link building is an objective of the
  // brand's agent; the autopilots already running are shown until they end.
  const viaAgent = caps?.includes("brand_agent") ?? false;

  const load = () =>
    call<{ data: Autopilot[] }>("GET", base).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setError("");
      setList(data.data);
    });
  useEffect(() => {
    setList(null);
    void load();
    api.GET("/v1/brands/{brand}/seo-sites", { params: { path: { brand: pickedBrandId() } } }).then(({ data }) => setSites(data?.data ?? []));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, brand?.id]);
  useEffect(() => {
    if (!viaAgent) return;
    void listBrandAgents(ws).then(({ data }) => setAgents(data ?? []));
  }, [ws, viaAgent]);
  const objectives = agents
    .filter((a) => !brand || a.brand_id === brand.id)
    .flatMap((a) => a.objectives.filter((o) => o.kind === "build_links" && o.status === "active").map((o) => ({ agent: a, o })));

  // Narrowed to a brand, show its autopilots and the ones every brand shares.
  const shown = list?.filter((a) => !brand || !a.brand_id || a.brand_id === brand.id);
  const active = shown?.filter((a) => !finished(a.status)) ?? [];
  const past = shown?.filter((a) => finished(a.status)) ?? [];
  const siteOf = (a: Autopilot) => sites.find((s) => host(s.host) === a.site);

  return (
    <div className="space-y-6">
      <PageHeader
        title="Link autopilot"
        description="Set how many new live links you want a month and what the link building playbooks may spend. Outflare's agent picks which playbooks run, when and with what budget, and changes course from the results. You approve every change and every pitch."
      />
      {error && <ErrorText>{error}</ErrorText>}
      {!shown ? (
        !error && <Loading />
      ) : (
        <>
          {objectives.length > 0 && (
            <Card title="Your brand agent's link building" description="Objectives the brand's agent works toward: it sets up the link playbooks cheapest first within the budget and rebalances them weekly. You approve each automation and pitch.">
              <ul className="divide-y text-[13px]">
                {objectives.map(({ agent, o }) => <ObjectiveRow key={o.id} ws={ws} agent={agent} o={o} />)}
              </ul>
            </Card>
          )}
          {active.map((a) => <AutopilotCard key={a.id} ws={ws} a={a} site={siteOf(a)} go={go} />)}
          {canManage ? (
            <Start ws={ws} base={base} viaAgent={viaAgent} brandID={brand?.id} sites={sites.filter((s) => !brand || !s.brand_id || s.brand_id === brand.id)} busy={new Set([...active.map((a) => a.site), ...objectives.map(({ o }) => String(o.target.site ?? ""))])} onStarted={(a) => { void load(); openAutopilot(go, a); }} go={go} />
          ) : (
            active.length === 0 && objectives.length === 0 && <Empty title="No autopilot yet">Someone who can run agents in this workspace can set one up.</Empty>
          )}
          {past.length > 0 && (
            <Card title="Earlier autopilots">
              <ul className="divide-y text-[13px]">
                {past.map((a) => (
                  <li key={a.id} className="flex items-center justify-between gap-3 py-2">
                    <span><span className="font-medium">{a.site}</span> <span className="text-muted-foreground">· {a.target_per_month} links a month · started {ago(a.created_at)}</span></span>
                    <span className="flex items-center gap-2">
                      <Badge tone={statusLabel[a.status]?.[1] ?? "grey"}>{statusLabel[a.status]?.[0] ?? a.status}</Badge>
                      <Button variant="ghost" onClick={() => openAutopilot(go, a)}>{a.run_id ? "Run" : "Mission"}</Button>
                    </span>
                  </li>
                ))}
              </ul>
            </Card>
          )}
        </>
      )}
    </div>
  );
}

// One link building objective of a brand agent: where it stands.
function ObjectiveRow({ ws, agent, o }: { ws: string; agent: BrandAgent; o: AgentObjective }) {
  const site = typeof o.target.site === "string" ? o.target.site : "";
  const budget = typeof o.target.monthly_budget_cents === "number" ? o.target.monthly_budget_cents : null;
  const pct = o.progress === null ? null : Math.round(o.progress * 100);
  return (
    <li className="flex flex-wrap items-center justify-between gap-3 py-2">
      <span className="min-w-0">
        <span className="font-medium">{o.title}</span>
        <span className="block text-[12px] text-muted-foreground">
          {site ? `${site} · ` : ""}
          {o.current !== null ? `${o.current.toLocaleString()} live in the last 30 days` : o.measure_error || "not measured yet"}
          {o.target_value !== null ? ` of ${o.target_value.toLocaleString()}` : ""}
          {pct !== null ? ` (${pct}%)` : ""}
          {budget !== null ? ` · budget ${dollars(budget)} a month` : ""}
        </span>
      </span>
      <a className="text-[13px] underline underline-offset-4" href={brandAgentURL(ws, agent.brand_id)}>Open the brand agent</a>
    </li>
  );
}

// One running autopilot: where it stands against its aim this month.
function AutopilotCard({ ws, a, site, go }: { ws: string; a: Autopilot; site?: SEOSite; go: (tab: Tab, item?: string) => void }) {
  const [live, setLive] = useState<number | null>(null);
  useEffect(() => {
    if (!site) return;
    const day = new Date().getUTCDate();
    api.GET("/v1/brands/{brand}/link-prospects/results", { params: { path: { brand: pickedBrandId() }, query: { site_id: site.id, days: day } } }).then(({ data }) => setLive(data?.totals.live ?? null));
  }, [ws, site]);
  const [label, tone] = statusLabel[a.status] ?? [a.status, "grey"];
  const waiting = a.status === "draft" || a.status === "pending_approval";
  return (
    <Card
      title={a.site}
      description={`Started ${ago(a.created_at)}`}
      action={
        <span className="flex items-center gap-2">
          <Badge tone={tone}>{label}</Badge>
          <Button variant={waiting ? "primary" : "ghost"} onClick={() => openAutopilot(go, a)}>{waiting ? "Review and approve" : a.run_id ? "Open run" : "Open mission"}</Button>
        </span>
      }
    >
      <Stats cols={3}>
        <Stat label="Live links this month" value={live ?? "–"} sub={`aim: ${a.target_per_month}`} tone={live === null ? undefined : live >= a.target_per_month ? "green" : undefined} />
        <Stat label="Monthly budget" value={dollars(a.monthly_budget_cents)} sub="for the playbooks' runs, on your own accounts" />
        <Stat label="What it changes" value="Automations" sub="each one waits for your approval in Playbooks" />
      </Stats>
      {waiting ? (
        <p className="mt-3 text-[12px] text-muted-foreground">It starts once you approve its mission. Its first weekly review sets up the cheapest playbooks first, and each automation it sets up waits for your approval.</p>
      ) : (
        a.run_id && <p className="mt-3 text-[12px] text-muted-foreground">Its first weekly review sets up the cheapest playbooks first; each automation it sets up waits for your approval. Write to it, pause or stop it from its run.</p>
      )}
      <div className="mt-3 flex gap-2">
        <Button variant="ghost" onClick={() => go("playbooks")}>Automations</Button>
        <Button variant="ghost" onClick={() => go("link-building")}>Pitches and results</Button>
      </div>
    </Card>
  );
}

// Start gives the brand's agent a link building objective (setting the
// agent up when the brand has none), or, on a server without brand
// agents, starts an autopilot whose run starts at once.
function Start({ ws, base, viaAgent, brandID, sites, busy, onStarted, go }: { ws: string; base: string; viaAgent: boolean; brandID?: string; sites: SEOSite[]; busy: Set<string>; onStarted: (a: Autopilot) => void; go: (tab: Tab, item?: string) => void }) {
  const free = sites.filter((s) => !busy.has(host(s.host)));
  const [siteId, setSiteId] = useState("");
  const [target, setTarget] = useState("5");
  const [budget, setBudget] = useState("100");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const site = free.find((s) => s.id === siteId) ?? free[0];

  if (sites.length === 0)
    return (
      <Empty title="Add your website first" action={<Button onClick={() => go("seo")}>Add a website</Button>}>
        The autopilot works on a website you've added, with its link building playbooks.
      </Empty>
    );
  if (!site) return null;

  const submit = async () => {
    const t = Number(target);
    const cents = Math.round(Number(budget) * 100);
    if (!Number.isInteger(t) || t < 1 || t > 500) return setError("The aim is 1 to 500 new live links a month.");
    if (!Number.isFinite(cents) || cents < 0) return setError("The budget is a number of dollars, 0 or more.");
    setSaving(true);
    setError("");
    if (viaAgent) {
      const forBrand = site.brand_id ?? brandID;
      if (!forBrand) {
        setSaving(false);
        return setError("Pick a brand first: the objective goes to that brand's agent.");
      }
      const r = await addBrandObjective(ws, forBrand, {
        kind: "build_links",
        title: `${t} new live links a month for ${host(site.host)}`,
        target: { links_per_month: t, monthly_budget_cents: cents, site: host(site.host) },
        notes: `Site: ${host(site.host)}. The link playbooks may spend at most ${dollars(cents)} a month on the workspace's own accounts.`,
      });
      setSaving(false);
      if (r.error || !r.data) return setError(errorMessage(r.error));
      toast("The brand agent has the objective and starts on it now.");
      location.assign(brandAgentURL(ws, forBrand));
      return;
    }
    const { data, error } = await call<Autopilot>("POST", base, { site: site.host, target_per_month: t, monthly_budget_cents: cents, brand_id: site.brand_id });
    setSaving(false);
    if (error || !data) return setError(errorMessage(error));
    toast("Autopilot set up. It starts now; follow it on the Agents page.");
    onStarted(data);
  };

  return (
    <Card
      title={viaAgent ? "Give the brand agent a link building objective" : "Set up an autopilot"}
      description={viaAgent ? "One per website. The brand's agent sets up the link playbooks cheapest first within the budget, rebalances them weekly and reports where it stands; every automation it sets up waits for your approval." : "One per website. It plans in turns on your workspace's AI model; the budget caps what its playbooks spend, and every automation it sets up waits for your approval."}
    >
      <div className="flex flex-wrap items-end gap-3">
        <label className="grid gap-1 text-[12px]">
          <span className="text-muted-foreground">Website</span>
          <select className={field} value={site.id} onChange={(e) => setSiteId(e.target.value)}>
            {free.map((s) => <option key={s.id} value={s.id}>{s.host}</option>)}
          </select>
        </label>
        <label className="grid gap-1 text-[12px]">
          <span className="text-muted-foreground">New live links a month</span>
          <input className={`${field} w-28`} type="number" min={1} max={500} value={target} onChange={(e) => setTarget(e.target.value)} />
        </label>
        <label className="grid gap-1 text-[12px]">
          <span className="text-muted-foreground">Budget a month ($)</span>
          <input className={`${field} w-28`} type="number" min={0} step={10} value={budget} onChange={(e) => setBudget(e.target.value)} />
        </label>
        <Button disabled={saving} onClick={submit}>{saving ? "Setting up…" : viaAgent ? "Add the objective" : "Set up"}</Button>
      </div>
      <p className="mt-2 text-[12px] text-muted-foreground">The budget is the most the playbooks it runs may spend a month on your own SEO, search and AI accounts. It never pays for a link.</p>
      {error && <div className="mt-2"><ErrorText>{error}</ErrorText></div>}
    </Card>
  );
}
