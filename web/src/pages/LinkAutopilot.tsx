// The link autopilot (Outflare Cloud): a mission that keeps the link
// building playbooks working toward a number of new live links a month,
// within a monthly budget, on the workspace's own accounts. The server
// lists the link_autopilot capability where it runs; its routes are the
// hosted extension's, so they are called without the typed client. A
// person approves its mission, every automation it sets up and every
// pitch, in Missions, Playbooks and Link building.

import { useEffect, useState } from "react";
import type { SEOSite } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, Empty, ErrorText, field, Loading, PageHeader, Stat, Stats, toast, type tones } from "../ui";
import { ago } from "../format";
import type { Tab } from "../App";

type Props = { ws: string; canManage: boolean; go: (tab: Tab, item?: string) => void };

type Autopilot = {
  id: string; mission_id: string; mission_status: string; brand_id: string | null; site: string;
  target_per_month: number; monthly_budget_cents: number; created_at: string;
};
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

// What the autopilot's mission status means for the person.
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

export function LinkAutopilot({ ws, canManage, go }: Props) {
  const base = `/v1/workspaces/${ws}/cloud/link-autopilot`;
  const { brand } = useBrand(ws);
  const [list, setList] = useState<Autopilot[] | null>(null);
  const [sites, setSites] = useState<SEOSite[]>([]);
  const [error, setError] = useState("");

  const load = () =>
    call<{ data: Autopilot[] }>("GET", base).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setError("");
      setList(data.data);
    });
  useEffect(() => {
    setList(null);
    void load();
    api.GET("/v1/workspaces/{ws}/seo-sites", { params: { path: { ws } } }).then(({ data }) => setSites(data?.data ?? []));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, brand?.id]);

  // Narrowed to a brand, show its autopilots and the ones every brand shares.
  const shown = list?.filter((a) => !brand || !a.brand_id || a.brand_id === brand.id);
  const active = shown?.filter((a) => !finished(a.mission_status)) ?? [];
  const past = shown?.filter((a) => finished(a.mission_status)) ?? [];
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
          {active.map((a) => <AutopilotCard key={a.id} ws={ws} a={a} site={siteOf(a)} go={go} />)}
          {canManage ? (
            <Start base={base} sites={sites.filter((s) => !brand || !s.brand_id || s.brand_id === brand.id)} busy={new Set(active.map((a) => a.site))} onStarted={(a) => { void load(); go("missions", a.mission_id); }} go={go} />
          ) : (
            active.length === 0 && <Empty title="No autopilot yet">Someone who can run missions in this workspace can set one up.</Empty>
          )}
          {past.length > 0 && (
            <Card title="Earlier autopilots">
              <ul className="divide-y text-[13px]">
                {past.map((a) => (
                  <li key={a.id} className="flex items-center justify-between gap-3 py-2">
                    <span><span className="font-medium">{a.site}</span> <span className="text-muted-foreground">· {a.target_per_month} links a month · started {ago(a.created_at)}</span></span>
                    <span className="flex items-center gap-2">
                      <Badge tone={statusLabel[a.mission_status]?.[1] ?? "grey"}>{statusLabel[a.mission_status]?.[0] ?? a.mission_status}</Badge>
                      <Button variant="ghost" onClick={() => go("missions", a.mission_id)}>Mission</Button>
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

// One running autopilot: where it stands against its aim this month.
function AutopilotCard({ ws, a, site, go }: { ws: string; a: Autopilot; site?: SEOSite; go: (tab: Tab, item?: string) => void }) {
  const [live, setLive] = useState<number | null>(null);
  useEffect(() => {
    if (!site) return;
    const day = new Date().getUTCDate();
    api.GET("/v1/workspaces/{ws}/link-prospects/results", { params: { path: { ws }, query: { site_id: site.id, days: day } } }).then(({ data }) => setLive(data?.totals.live ?? null));
  }, [ws, site]);
  const [label, tone] = statusLabel[a.mission_status] ?? [a.mission_status, "grey"];
  const waiting = a.mission_status === "draft" || a.mission_status === "pending_approval";
  return (
    <Card
      title={a.site}
      description={`Started ${ago(a.created_at)}`}
      action={
        <span className="flex items-center gap-2">
          <Badge tone={tone}>{label}</Badge>
          <Button variant={waiting ? "primary" : "ghost"} onClick={() => go("missions", a.mission_id)}>{waiting ? "Review and approve" : "Open mission"}</Button>
        </span>
      }
    >
      <Stats cols={3}>
        <Stat label="Live links this month" value={live ?? "–"} sub={`aim: ${a.target_per_month}`} tone={live === null ? undefined : live >= a.target_per_month ? "green" : undefined} />
        <Stat label="Monthly budget" value={dollars(a.monthly_budget_cents)} sub="for the playbooks' runs, on your own accounts" />
        <Stat label="What it changes" value="Automations" sub="each one waits for your approval in Playbooks" />
      </Stats>
      {waiting && <p className="mt-3 text-[12px] text-muted-foreground">It starts once you approve its mission. Its first weekly review sets up the cheapest playbooks first, and each automation it sets up waits for your approval.</p>}
      <div className="mt-3 flex gap-2">
        <Button variant="ghost" onClick={() => go("playbooks")}>Automations</Button>
        <Button variant="ghost" onClick={() => go("link-building")}>Pitches and results</Button>
      </div>
    </Card>
  );
}

// Start sets up an autopilot for one site; its mission opens as a draft.
function Start({ base, sites, busy, onStarted, go }: { base: string; sites: SEOSite[]; busy: Set<string>; onStarted: (a: Autopilot) => void; go: (tab: Tab, item?: string) => void }) {
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
    const { data, error } = await call<Autopilot>("POST", base, { site: site.host, target_per_month: t, monthly_budget_cents: cents, brand_id: site.brand_id });
    setSaving(false);
    if (error || !data) return setError(errorMessage(error));
    toast("Autopilot set up. Approve its mission to start it.");
    onStarted(data);
  };

  return (
    <Card title="Set up an autopilot" description="One per website. It plans in turns on your workspace's AI model, so its own turns count toward its mission, not the budget.">
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
        <Button disabled={saving} onClick={submit}>{saving ? "Setting up…" : "Set up"}</Button>
      </div>
      <p className="mt-2 text-[12px] text-muted-foreground">The budget is the most the playbooks it runs may spend a month on your own SEO, search and AI accounts. It never pays for a link.</p>
      {error && <div className="mt-2"><ErrorText>{error}</ErrorText></div>}
    </Card>
  );
}
