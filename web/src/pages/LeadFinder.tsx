// The lead finder (Outflare Cloud): AI runs that search the web for
// companies that fit an ideal customer profile, read their own websites,
// qualify them, find the right people there and add them to a campaign.
// The server lists the lead_finder capability where it runs; its routes
// are the hosted extension's, so they are called without the typed client.
// Everything runs on the workspace's own accounts.
//
// Runs get big (thousands of companies, tens of thousands of people), so
// no screen holds them all: the front page shows what needs you and what
// each search produced, and a run's companies and people are lists that
// load a page at a time from the server, with bulk actions that take
// everything matching a filter.

import { useCallback, useEffect, useState, type ReactNode } from "react";
import type { CustomerProfile, Sequence } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, Empty, ErrorText, field, Loading, PageHeader, Stat, Stats, confirmAction, toast } from "../ui";
import { ago, plural } from "../format";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Switch } from "@/components/ui/switch";
import { TagInput } from "@/components/icp";

type Props = { ws: string; canManage: boolean; onConnectors: () => void; onProfiles: () => void };

type Settings = { respect_robots: boolean; review: boolean; share_index: boolean; generic_emails?: boolean; eu_notice_accepted_at: string | null };
// Someone a run looked up at one of its added companies.
type Contact = {
  id: string; domain: string; source: "website" | "provider" | "inbox"; first_name: string; last_name: string; title: string; email: string;
  email_code: string; confidence: number; status: "finding" | "added" | "ready" | "enrolled" | "stopped"; stop_code: string | null; warnings: string[];
  record_id: string | null; sequence_id?: string | null;
};
// The campaign a profile's people go into: held for you unless auto_add.
type Campaign = { profile_id: string; sequence_id: string | null; auto_add: boolean; daily_limit: number; ready_limit: number };
type RunCampaign = { sequence_id: string; name: string; status: string; auto_add: boolean; daily_limit: number };
type Qualifier = { id: string | null; profile_id: string | null; version: number; accept: string[]; reject: string[] };
type Estimate = { searches: number; pages: number; max_ai_cents: number; provider_usd: number; per_company_usd: number };
type Counts = { finding: number; added: number; ready?: number; enrolled?: number; stopped: number };
type Run = {
  id: string; profile_id: string | null; status: "queued" | "running" | "review" | "done" | "failed" | "cancelled"; target: number;
  max_searches: number; max_pages: number; max_ai_cents: number; review: boolean; queries: string[]; searches: number; pages: number;
  found: number; accepted: number; maybe: number; rejected: number; added: number; search_tokens: number; page_tokens: number;
  provider_cents: number; ai_cents: number; error_code: string | null; error: string | null; created_at: string; finished_at: string | null;
  // Only on a single run (and the front page's latest runs).
  pipeline?: Pipeline; contacts?: Counts; campaign?: RunCampaign;
  spend?: { ai_cents: number; web_cents: number; web_tokens: number; people_credits: number };
  results?: { in_campaign: number; sent: number; replied: number; meetings?: number };
  to_review?: number;
};
type Pipeline = { state: "running" | "paused" | "cancelled" | "finished"; pause_code?: string; pause_reason?: string; stages: StageCount[] };
type StageCount = {
  stage: string; waiting: number; working: number; done: number; skipped: number; held: number; failed: number;
  entered: number; retried: number; waited: number; busy_ms: number; errors: Record<string, number>; oldest_waiting_seconds: number;
};
type Candidate = {
  id: string; domain: string; name: string; facts: Record<string, unknown>; emails: string[]; verdict: "accept" | "maybe" | "reject" | "unread";
  score: number | null; reasons: string[]; query: string; status: "pending" | "added" | "dismissed"; record_id: string | null; from_index: boolean;
  picked?: boolean; blocked?: string | null; warnings?: string[];
};
type Totals = { runs: number; found: number; fit: number; added: number; people: number; emails: number; in_campaign: number; replied: number; meetings?: number; ai_cents: number; web_cents: number; web_tokens: number };
type Need = { kind: "paused" | "ready" | "review" | "waiting"; run_id: string; profile_id: string | null; profile_name: string; count: number; code?: string; reason?: string; campaign?: string };
type Search = Totals & { profile_id: string; profile_name: string; campaign: RunCampaign | null; last_run: Run | null };
type Summary = { days: number; needs: Need[]; totals: Totals; searches: Search[] };
type Failure = { error: { code: string; message: string } };

// call reaches the lead finder's routes, with the same session as the
// typed client and errors in the engine's shape.
async function call<T>(method: string, path: string, body?: unknown): Promise<{ data?: T; error?: Failure; status?: number }> {
  try {
    const res = await fetch(path, { method, credentials: "same-origin", headers: body ? { "Content-Type": "application/json" } : {}, body: body ? JSON.stringify(body) : undefined });
    const json = await res.json().catch(() => ({}));
    return res.ok ? { data: json as T, status: res.status } : { error: json as Failure, status: res.status };
  } catch {
    return { error: { error: { code: "network_error", message: "Could not reach Outflare. Check your connection and try again." } } };
  }
}

function useDebounced<T>(v: T, ms = 300) {
  const [d, setD] = useState(v);
  useEffect(() => {
    const t = setTimeout(() => setD(v), ms);
    return () => clearTimeout(t);
  }, [v, ms]);
  return d;
}

const n = (v: number) => v.toLocaleString();
const usd = (cents: number) => `$${(cents / 100).toFixed(cents < 100 ? 3 : 2).replace(/\.?0+$/, "") || "0"}`;
const money = (cents: number) => `$${(cents / 100).toFixed(2)}`;
const verdictTone = { accept: "green", maybe: "amber", reject: "red", unread: "grey" } as const;
// What an opt-out list entry that let a company through means.
const warningLabel = (w: string) => {
  const why = w.split(":")[1] ?? w;
  return why === "customer" ? "A customer" : why === "partner" ? "A partner" : why === "competitor" ? "A competitor" : `On your opt-out list (${why})`;
};
const codeLabel: Record<string, string> = {
  target_reached: "enough found", rejected: "not a fit", unreachable: "site couldn't be read", no_ideas: "no new searches",
  no_answer: "no clear answer", cancelled: "stopped", reviewed: "reviewed", review: "waiting for you", maybe: "maybe, left for you",
  added: "added", dismissed: "dismissed", not_found: "no email found", rate_limited: "slowed by rate limit", provider_error: "provider error, retried",
  connector_quota: "Geonode limit", page_budget_reached: "page limit", ai_budget_reached: "AI limit", search_budget: "search limit",
  interrupted: "interrupted, retried", internal: "error, retried", stage_error: "error, retried", no_result: "retried",
  no_people: "nobody to contact", undeliverable: "email doesn't work", verify_pending: "checking email",
  already_known: "already in your CRM", opted_out: "opted out", bounced: "bounced", not_a_company: "not a company domain",
  connector_needed: "needs a connection", connector_auth: "key refused", connector_unavailable: "provider down, retried",
  ai_model_needed: "needs an AI model", usage_limit_reached: "usage limit", skipped: "skipped by you",
  in_another_campaign: "in another campaign", campaign_ready: "ready for campaign", campaign_full: "campaign full, waiting",
  campaign_unavailable: "campaign unavailable", paused_by_you: "paused by you", risky_email: "unconfirmed email",
};
const label = (code: string | null | undefined) => (code ? codeLabel[code] ?? code.replace(/_/g, " ") : "");

// The steps a company goes through, in order. Unknown steps show as they are.
const stages: { id: string; label: string; what: string }[] = [
  { id: "search", label: "Search", what: "Plans searches and runs them on Geonode" },
  { id: "read", label: "Read website", what: "Reads each new company's homepage and contact or about pages" },
  { id: "qualify", label: "Judge fit", what: "Your AI says what the company is and whether it fits" },
  { id: "review", label: "Review", what: "Waits for you to add or dismiss, or sends a fit on straight away with review off" },
  { id: "known", label: "Already known", what: "Skips companies already in your CRM" },
  { id: "contact_rules", label: "Contact rules", what: "Stops companies that opted out or bounced; customers, partners and competitors only warn" },
  { id: "add_to_crm", label: "Add to CRM", what: "Adds the company as a prospect" },
  { id: "people", label: "Pick people", what: "Picks who to contact: people the site names, people from your data provider, and shared inboxes if you allow them" },
  { id: "find_email", label: "Find email", what: "Finds and checks each person's work email" },
  { id: "contact_check", label: "Contact rules", what: "Stops people who opted out or bounced" },
  { id: "add_person", label: "Add person", what: "Adds the person to your CRM, linked to the company" },
  { id: "enroll", label: "Add to campaign", what: "Adds the person to the profile's campaign, after you approve them unless auto-add is on" },
];
function waited(seconds: number) {
  if (seconds < 60) return `${Math.round(seconds)}s`;
  if (seconds < 3600) return `${Math.round(seconds / 60)}m`;
  return `${Math.round(seconds / 3600)}h`;
}
// Limits a pause asks to raise, and the run field each one is.
const raises: Record<string, { field: "max_pages" | "max_ai_cents" | "max_searches"; label: string }> = {
  page_budget_reached: { field: "max_pages", label: "Pages it may read" },
  ai_budget_reached: { field: "max_ai_cents", label: "AI it may spend (cents)" },
};

// A run's state in plain words: what it is doing, why, and what happens
// next. Pipeline state wins over the run's own status, because a finished
// run can still be adding its people to the campaign.
type State = { key: string; label: string; tone: "grey" | "blue" | "amber" | "green" | "red"; why?: string };
function runState(r: Run): State {
  const p = r.pipeline;
  const ready = r.contacts?.ready ?? 0;
  if (r.status === "cancelled") return { key: "stopped", label: "Stopped", tone: "grey", why: "You stopped it. What it found is kept." };
  if (r.status === "failed") return { key: "failed", label: "Failed", tone: "red", why: r.error ?? "It couldn't carry on." };
  if (p?.state === "paused" || (!p && r.status === "running" && r.error_code)) {
    return { key: "paused", label: "Paused", tone: "amber", why: p?.pause_code === "paused_by_you" ? "You paused it. Resume to carry on where it stopped." : r.error ?? p?.pause_reason ?? "It paused." };
  }
  const search = p?.stages.find((s) => s.stage === "search");
  if (r.status === "running" && search && search.waiting > 0 && (search.errors.campaign_full ?? 0) > 0) {
    return { key: "waiting", label: "Waiting", tone: "amber", why: "The campaign already has 3 days of people waiting. It searches again as they're sent." };
  }
  if (r.status === "running" || r.status === "queued") return { key: "finding", label: "Finding", tone: "blue", why: "Finding companies and the people there. This page updates as it goes." };
  if (r.status === "review") return { key: "review", label: "To review", tone: "amber", why: "Companies wait for you to add or dismiss them." };
  if (ready > 0) return { key: "ready", label: "Needs you", tone: "amber", why: `${plural(ready, "person", "people")} wait for you to add them to the campaign.` };
  if (p?.state === "running") return { key: "adding", label: "Adding people", tone: "blue", why: "Finding the people at the companies it added." };
  return { key: "done", label: "Done", tone: "green", why: r.error ?? undefined };
}
const stateBadge = (r: Run) => {
  const s = runState(r);
  return <Badge tone={s.tone} title={s.why}>{s.label}</Badge>;
};

export function LeadFinder({ ws, canManage, onConnectors, onProfiles }: Props) {
  const base = `/v1/workspaces/${ws}/cloud/lead-finder`;
  const [settings, setSettings] = useState<Settings | null>(null);
  const [profiles, setProfiles] = useState<CustomerProfile[]>([]);
  const [runs, setRuns] = useState<Run[] | null>(null);
  const [summary, setSummary] = useState<Summary | null>(null);
  const [days, setDays] = useState(30);
  const [view, setView] = useState<"searches" | "runs">("searches");
  const [open, setOpen] = useState<{ id: string; tab?: Tab; filter?: string } | null>(null);
  const [setup, setSetup] = useState<{ profile?: string; target?: number } | null>(null);
  const [editingSettings, setEditingSettings] = useState(false);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    const [s, r, sm] = await Promise.all([call<Settings>("GET", `${base}/settings`), call<{ data: Run[] }>("GET", `${base}/runs`), call<Summary>("GET", `${base}/summary?days=${days}`)]);
    if (s.error || r.error) setError(errorMessage(s.error ?? r.error));
    if (s.data) setSettings(s.data);
    setRuns(r.data?.data ?? []);
    // A server without the summary shows the list of runs only.
    setSummary(sm.data ?? null);
    if (!sm.data) setView("runs");
  }, [base, days]);
  useEffect(() => {
    load();
    api.GET("/v1/workspaces/{ws}/ideal-customer-profiles", { params: { path: { ws } } }).then(({ data }) => setProfiles(data?.data ?? []));
  }, [load, ws]);
  // Refresh while something is moving.
  const live = runs?.some((r) => r.status === "queued" || r.status === "running") || summary?.searches.some((s) => s.last_run?.pipeline?.state === "running");
  useEffect(() => {
    if (!live) return;
    const t = setInterval(load, 5000);
    return () => clearInterval(t);
  }, [live, load]);

  if (!settings || !runs) return error ? <ErrorText>{error}</ErrorText> : <Loading />;
  if (open) {
    return <RunView key={open.id} ws={ws} base={base} id={open.id} tab={open.tab} filter={open.filter} canManage={canManage} profiles={profiles}
      onBack={() => { setOpen(null); load(); }} onConnectors={onConnectors} onRunAgain={(profile, target) => { setOpen(null); setSetup({ profile, target }); }} />;
  }
  const profileName = Object.fromEntries(profiles.map((p) => [p.id, p.name])) as Record<string, string>;
  const ready = settings.eu_notice_accepted_at && profiles.length > 0;
  return (
    <div className="space-y-6">
      <PageHeader
        title="Lead finder"
        description="Finds companies that fit an ideal customer profile, the right people there and their work emails, and adds them to your campaigns. Everything runs on your own Geonode, AI and data provider accounts."
        action={canManage && settings.eu_notice_accepted_at && (
          <div className="flex gap-2">
            <Button variant="ghost" onClick={() => setEditingSettings(true)}>Settings</Button>
            <Button onClick={() => setSetup({})} disabled={profiles.length === 0}>New search</Button>
          </div>
        )}
      />
      <ErrorText>{error}</ErrorText>
      {!settings.eu_notice_accepted_at ? (
        <Notice canManage={canManage} onAccept={async () => {
          const { data, error } = await call<Settings>("PATCH", `${base}/settings`, { accept_eu_notice: true });
          if (error) return toast(errorMessage(error), "error");
          setSettings(data!);
        }} />
      ) : profiles.length === 0 ? (
        <Empty title="Describe who you sell to first" action={<Button onClick={onProfiles}>Add an ideal customer profile</Button>}>
          The lead finder looks for companies that fit an ideal customer profile: who they are and why they buy, in words, plus the industries, sizes, places and job titles that fit.
        </Empty>
      ) : runs.length === 0 ? (
        <Empty title="No searches yet" action={canManage && <Button onClick={() => setSetup({})}>New search</Button>}>
          Pick a profile, where its people go, and how many companies you want. You see the most it can cost before it starts.
        </Empty>
      ) : (
        <>
          {summary && summary.needs.length > 0 && <NeedsYou needs={summary.needs} canManage={canManage} onOpen={(id, tab, filter) => setOpen({ id, tab, filter })} onConnectors={onConnectors} />}
          {summary && <Results totals={summary.totals} days={days} onDays={setDays} />}
          <div className="space-y-3">
            {summary && (
              <div className="flex gap-1 border-b text-[13px]" role="tablist">
                {(["searches", "runs"] as const).map((v) => (
                  <button key={v} type="button" role="tab" aria-selected={view === v} onClick={() => setView(v)}
                    className={`-mb-px border-b-2 px-3 py-2 ${view === v ? "border-foreground font-medium" : "border-transparent text-muted-foreground hover:text-foreground"}`}>
                    {v === "searches" ? `Searches ${summary.searches.length}` : `All runs ${runs.length}`}
                  </button>
                ))}
              </div>
            )}
            {view === "searches" && summary ? (
              <SearchesTable searches={summary.searches} canManage={canManage} onOpen={(id) => setOpen({ id })} onRunAgain={(profile, target) => setSetup({ profile, target })} />
            ) : (
              <RunsTable runs={runs} profileName={profileName} onOpen={(id) => setOpen({ id })} />
            )}
          </div>
        </>
      )}
      {setup && ready && <SetupDialog ws={ws} base={base} profiles={profiles.filter((p) => p.status === "active")} settings={settings} initial={setup}
        onClose={() => setSetup(null)} onStarted={(id) => { setSetup(null); setOpen({ id }); }} />}
      {editingSettings && <SettingsDialog base={base} settings={settings} onClose={() => setEditingSettings(false)} onSaved={(s) => { setSettings(s); setEditingSettings(false); }} />}
    </div>
  );
}

// NeedsYou is everything waiting for a person, across runs, each with the
// one button that deals with it.
function NeedsYou({ needs, canManage, onOpen, onConnectors }: { needs: Need[]; canManage: boolean; onOpen: (id: string, tab?: Tab, filter?: string) => void; onConnectors: () => void }) {
  const order = { paused: 0, ready: 1, review: 2, waiting: 3 } as const;
  const sorted = [...needs].sort((a, b) => order[a.kind] - order[b.kind]);
  return (
    <section aria-label="Needs you" className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
      {sorted.map((nd) => {
        const tone = nd.kind === "paused" ? "border-red-300 bg-red-50 dark:border-red-900 dark:bg-red-950/40" : nd.kind === "waiting" ? "border-border bg-card" : "border-amber-300 bg-amber-50 dark:border-amber-900 dark:bg-amber-950/40";
        const [title, sub, action] =
          nd.kind === "paused" ? [`Paused: ${nd.code === "paused_by_you" ? "by you" : label(nd.code) || "needs attention"}`, nd.profile_name,
            nd.code?.startsWith("connector_") || nd.code === "ai_model_needed" ? <Button variant="ghost" onClick={onConnectors}>Fix</Button> : <Button variant="ghost" onClick={() => onOpen(nd.run_id)}>Open</Button>]
          : nd.kind === "ready" ? [`${plural(nd.count, "person", "people")} ready for ${nd.campaign ? `"${nd.campaign}"` : "a campaign"}`, nd.profile_name,
            <Button variant="ghost" onClick={() => onOpen(nd.run_id, "people", "ready")}>{canManage ? "Review" : "View"}</Button>]
          : nd.kind === "review" ? [`${plural(nd.count, "company", "companies")} to review`, nd.profile_name,
            <Button variant="ghost" onClick={() => onOpen(nd.run_id, "companies", "to_review")}>{canManage ? "Review" : "View"}</Button>]
          : ["Waiting: campaign full", `${nd.profile_name} searches again as the campaign sends`, <Button variant="ghost" onClick={() => onOpen(nd.run_id)}>Open</Button>];
        return (
          <div key={`${nd.kind}/${nd.run_id}`} className={`flex min-w-0 items-center justify-between gap-3 rounded-lg border px-4 py-3 ${tone}`}>
            <div className="min-w-0">
              <p className="truncate text-[13px] font-medium">{title}</p>
              <p className="truncate text-[12px] text-muted-foreground">{sub}</p>
            </div>
            {action}
          </div>
        );
      })}
    </section>
  );
}

// Results are what the period's runs produced, for whoever asks "is this
// working, and what does it cost?".
function Results({ totals: t, days, onDays }: { totals: Totals; days: number; onDays: (d: number) => void }) {
  const spent = t.ai_cents + t.web_cents;
  return (
    <section className="space-y-2">
      <div className="flex items-center justify-between gap-2">
        <h2 className="text-[13px] font-semibold">Results</h2>
        <select aria-label="Period" className={field} value={days} onChange={(e) => onDays(Number(e.target.value))}>
          <option value={7}>Last 7 days</option>
          <option value={30}>Last 30 days</option>
          <option value={90}>Last 90 days</option>
          <option value={365}>Last year</option>
        </select>
      </div>
      <Stats cols={t.meetings !== undefined ? 5 : 4}>
        <Stat label="Companies" value={n(t.found)} sub={`${n(t.fit)} fit, ${n(t.added)} added`} />
        <Stat label="People" value={n(t.people)} sub={`${n(t.emails)} with an email`} />
        <Stat label="In campaigns" value={n(t.in_campaign)} sub={`${n(t.replied)} replied${t.in_campaign ? `, ${((100 * t.replied) / t.in_campaign).toFixed(1)}%` : ""}`} />
        {t.meetings !== undefined && <Stat label="Meetings" value={n(t.meetings)} sub={t.in_campaign ? `${((100 * t.meetings) / t.in_campaign).toFixed(1)}% of people in campaigns` : "booked by people it found"} />}
        <Stat label="Spent" value={money(spent)} sub={t.meetings ? `${money(spent / t.meetings)} a meeting` : t.in_campaign ? `${usd(spent / t.in_campaign)} a person in a campaign` : `${n(t.web_tokens)} Geonode tokens`} />
      </Stats>
    </section>
  );
}

function SearchesTable({ searches, canManage, onOpen, onRunAgain }: { searches: Search[]; canManage: boolean; onOpen: (id: string) => void; onRunAgain: (profile: string, target: number) => void }) {
  if (searches.length === 0) return <Empty title="No searches in this period">Pick a longer period, or start a new search.</Empty>;
  return (
    <div className="overflow-x-auto rounded-lg border bg-card shadow-xs">
      <table className="w-full text-[13px]">
        <thead className="text-left font-mono text-[11px] tracking-wide text-muted-foreground uppercase">
          <tr className="border-b">
            <th className="px-3 py-2 font-medium">Search</th><th className="px-3 py-2 font-medium">Campaign</th><th className="px-3 py-2 font-medium">Latest run</th>
            <th className="px-3 py-2 text-right font-medium">Companies</th><th className="px-3 py-2 text-right font-medium">People</th>
            <th className="px-3 py-2 text-right font-medium">In campaign</th><th className="px-3 py-2 text-right font-medium">Replied</th><th className="px-3 py-2 text-right font-medium">Meetings</th>
            <th className="px-3 py-2 text-right font-medium">Spent</th><th className="px-3 py-2" />
          </tr>
        </thead>
        <tbody>
          {searches.map((s) => {
            const r = s.last_run;
            const st = r ? runState(r) : null;
            const fit = r ? r.accepted + r.maybe : 0;
            const action = !r ? null
              : st?.key === "paused" || st?.key === "review" || st?.key === "ready" ? <Button variant="ghost" onClick={(e) => { e.stopPropagation(); onOpen(r.id); }}>{st.key === "paused" ? "Fix" : "Review"}</Button>
              : canManage && (st?.key === "done" || st?.key === "stopped" || st?.key === "failed") ? <Button variant="ghost" onClick={(e) => { e.stopPropagation(); onRunAgain(s.profile_id, r.target); }}>Run again</Button>
              : null;
            return (
              <tr key={s.profile_id} className="cursor-pointer border-b last:border-0 hover:bg-accent/40" onClick={() => r && onOpen(r.id)}>
                <td className="px-3 py-2.5"><span className="font-medium">{s.profile_name}</span><span className="block text-[12px] text-muted-foreground">{plural(s.runs, "run")}{r ? ` · last ${ago(r.created_at)}` : ""}</span></td>
                <td className="px-3 py-2.5">{s.campaign ? <span>{s.campaign.name}<span className="block text-[12px] text-muted-foreground">{s.campaign.auto_add ? "Adds people itself" : "You approve people"}</span></span> : <span className="text-muted-foreground">None</span>}</td>
                <td className="px-3 py-2.5">
                  {r && st && (
                    <span className="block min-w-36">
                      <Badge tone={st.tone} title={st.why}>{st.label}</Badge>
                      <span className="mt-1 flex items-center gap-2 text-[12px] text-muted-foreground">
                        <span className="h-1.5 w-20 rounded-full bg-muted"><span className="block h-1.5 rounded-full bg-primary/70" style={{ width: `${Math.min(100, (100 * fit) / Math.max(1, r.target))}%` }} /></span>
                        {n(fit)} of {n(r.target)}
                      </span>
                    </span>
                  )}
                </td>
                <td className="px-3 py-2.5 text-right tabular-nums">{n(s.found)}<span className="block text-[12px] text-muted-foreground">{n(s.added)} added</span></td>
                <td className="px-3 py-2.5 text-right tabular-nums">{n(s.people)}</td>
                <td className="px-3 py-2.5 text-right tabular-nums">{n(s.in_campaign)}</td>
                <td className="px-3 py-2.5 text-right tabular-nums">{n(s.replied)}</td>
                <td className="px-3 py-2.5 text-right tabular-nums">{s.meetings !== undefined ? n(s.meetings) : "–"}</td>
                <td className="px-3 py-2.5 text-right tabular-nums">{money(s.ai_cents + s.web_cents)}</td>
                <td className="px-3 py-2.5 text-right">{action}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function RunsTable({ runs, profileName, onOpen }: { runs: Run[]; profileName: Record<string, string>; onOpen: (id: string) => void }) {
  return (
    <div className="overflow-x-auto rounded-lg border bg-card shadow-xs">
      <table className="w-full text-[13px]">
        <thead className="text-left font-mono text-[11px] tracking-wide text-muted-foreground uppercase">
          <tr className="border-b">
            <th className="px-3 py-2 font-medium">Looking for</th><th className="px-3 py-2 font-medium">Status</th><th className="px-3 py-2 text-right font-medium">Fit</th>
            <th className="px-3 py-2 text-right font-medium">Added</th><th className="px-3 py-2 text-right font-medium">Spent</th><th className="px-3 py-2 font-medium">Started</th>
          </tr>
        </thead>
        <tbody>
          {runs.map((r) => (
            <tr key={r.id} className="cursor-pointer border-b last:border-0 hover:bg-accent/40" onClick={() => onOpen(r.id)}>
              <td className="px-3 py-2.5 font-medium">{r.profile_id ? profileName[r.profile_id] ?? "Deleted profile" : "Deleted profile"}</td>
              <td className="px-3 py-2.5">{stateBadge(r)}</td>
              <td className="px-3 py-2.5 text-right tabular-nums">{n(r.accepted + r.maybe)} of {n(r.target)}</td>
              <td className="px-3 py-2.5 text-right tabular-nums">{n(r.added)}</td>
              <td className="px-3 py-2.5 text-right tabular-nums">{money(r.ai_cents + r.provider_cents)}</td>
              <td className="px-3 py-2.5 text-muted-foreground">{ago(r.created_at)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function Notice({ canManage, onAccept }: { canManage: boolean; onAccept: () => void }) {
  return (
    <Card title="Before your first search" description="Shown once for the workspace.">
      <div className="space-y-3 text-[13px] leading-relaxed">
        <p>The lead finder reads public company websites and keeps the email addresses those sites publish. Some of those belong to people, including people in the EU and UK.</p>
        <p>Under the GDPR and UK GDPR you can contact business people at their work address on the basis of legitimate interest when what you offer is relevant to their job. You need to tell them where you got their details, let them opt out in every message, and stop when they do. Outflare adds the opt-out and keeps your suppression list, but whether your outreach is relevant is yours to judge.</p>
        {canManage ? <Button onClick={onAccept}>I understand</Button> : <p className="text-muted-foreground">An admin or operator accepts this once for the workspace.</p>}
      </div>
    </Card>
  );
}

// CampaignFields picks where a profile's people go: one of the
// workspace's sequences, and whether they go in without asking.
function CampaignFields({ ws, campaign, onChange }: { ws: string; campaign: Campaign; onChange: (c: Campaign) => void }) {
  const [sequences, setSequences] = useState<Sequence[]>([]);
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/sequences", { params: { path: { ws } } }).then(({ data }) => setSequences((data?.data ?? []).filter((q) => q.status !== "archived")));
  }, [ws]);
  return (
    <div className="space-y-3">
      <label className="block space-y-1 text-[13px]">
        <span className="font-medium">Campaign</span>
        <select className={`${field} w-full`} value={campaign.sequence_id ?? ""} onChange={(e) => onChange({ ...campaign, sequence_id: e.target.value || null })}>
          <option value="">None: keep people in the CRM</option>
          {sequences.map((q) => <option key={q.id} value={q.id}>{q.name}</option>)}
        </select>
        <span className="block text-[12px] text-muted-foreground">
          {campaign.sequence_id
            ? "The campaign sends at its own daily limit. The lead finder stops searching while 3 days of people are already waiting, so you don't pay for leads that go stale."
            : "People found are added to your CRM, linked to their company."}
        </span>
      </label>
      {campaign.sequence_id && (
        <label className="flex cursor-pointer items-start justify-between gap-3 text-[13px]">
          <span>
            <span className="block">Add people without asking</span>
            <span className="block text-[12px] text-muted-foreground">Off: they wait under "Ready" until you add them. Unconfirmed emails always wait.</span>
          </span>
          <Switch checked={campaign.auto_add} onCheckedChange={(v) => onChange({ ...campaign, auto_add: v })} />
        </label>
      )}
    </div>
  );
}

const saved = <span className="ml-1 rounded bg-muted px-1.5 py-0.5 font-mono text-[10px] font-normal tracking-wide text-muted-foreground uppercase">Saved for this profile</span>;

// SetupDialog starts a run in three short steps: who to look for, where
// their people go, and how many. The criteria and the campaign are saved
// for the profile, so the next run starts from them.
function SetupDialog({ ws, base, profiles, settings, initial, onClose, onStarted }: {
  ws: string; base: string; profiles: CustomerProfile[]; settings: Settings; initial: { profile?: string; target?: number }; onClose: () => void; onStarted: (id: string) => void;
}) {
  const [step, setStep] = useState(0);
  const [profile, setProfile] = useState(initial.profile && profiles.some((p) => p.id === initial.profile) ? initial.profile : profiles[0]?.id ?? "");
  const [target, setTarget] = useState(initial.target ?? 25);
  const [review, setReview] = useState(settings.review);
  const [estimate, setEstimate] = useState<Estimate | null>(null);
  const [qualifier, setQualifier] = useState<Qualifier | null>(null);
  // Null on a server without campaigns: that step only shows review.
  const [campaign, setCampaign] = useState<Campaign | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const p = profiles.find((x) => x.id === profile);

  useEffect(() => {
    if (target < 1 || target > 1000) return setEstimate(null);
    call<Estimate>("POST", `${base}/estimate`, { target }).then(({ data }) => setEstimate(data ?? null));
  }, [base, target]);
  useEffect(() => {
    if (!profile) return;
    call<Qualifier>("GET", `${base}/qualifiers?profile_id=${profile}`).then(({ data }) => setQualifier(data ?? null));
    call<Campaign>("GET", `${base}/profiles/${profile}/campaign`).then(({ data }) => setCampaign(data ?? null));
  }, [base, profile]);

  async function start() {
    setBusy(true);
    setError("");
    const fail = (e?: Failure) => {
      setBusy(false);
      setError(errorMessage(e));
    };
    if (qualifier) {
      const q = await call<Qualifier>("PUT", `${base}/qualifiers`, { profile_id: profile, accept: qualifier.accept, reject: qualifier.reject });
      if (q.error) return fail(q.error);
    }
    if (campaign) {
      const c = await call<Campaign>("PUT", `${base}/profiles/${profile}/campaign`, { sequence_id: campaign.sequence_id ?? "", auto_add: campaign.auto_add });
      if (c.error) return fail(c.error);
    }
    const { data, error } = await call<Run>("POST", `${base}/runs`, { profile_id: profile, target, review });
    if (error) return fail(error);
    setBusy(false);
    toast("Started. The page updates as it goes.");
    onStarted(data!.id);
  }

  const steps = ["Who", "Where they go", "How many"];
  const f = p?.filters;
  const chips = (title: string, vals?: string[]) => vals && vals.length > 0 && (
    <div className="text-[12px]"><span className="text-muted-foreground">{title}: </span>{vals.join(", ")}</div>
  );
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>New search</DialogTitle>
          <DialogDescription>
            <span className="flex gap-3 pt-1">
              {steps.map((s, i) => <span key={s} className={i === step ? "font-medium text-foreground" : ""}>{i + 1}. {s}</span>)}
            </span>
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          {step === 0 && (
            <>
              <label className="block space-y-1 text-[13px]">
                <span className="font-medium">Ideal customer profile</span>
                <select className={`${field} w-full`} value={profile} onChange={(e) => setProfile(e.target.value)}>
                  {profiles.map((x) => <option key={x.id} value={x.id}>{x.name}</option>)}
                </select>
              </label>
              {f && (
                <div className="space-y-1 rounded-md bg-muted px-3 py-2">
                  {chips("People it contacts", f.titles)}
                  {chips("Seniority", f.seniorities)}
                  {chips("Industries", f.industries)}
                  {chips("Size", f.employee_ranges)}
                  {!f.titles?.length && <div className="text-[12px] text-muted-foreground">No job titles on this profile: it contacts the people companies name on their own pages. Add titles to the profile to aim at the right people.</div>}
                </div>
              )}
              {qualifier && (
                <>
                  <TagInput label="Accept companies that" placeholder="Sell software to finance teams" values={qualifier.accept} onChange={(v) => setQualifier({ ...qualifier, accept: v })} />
                  <TagInput label="Reject companies that" placeholder="Are agencies or resellers" values={qualifier.reject} onChange={(v) => setQualifier({ ...qualifier, reject: v })} />
                  <p className="text-[12px] text-muted-foreground">The criteria are saved for this profile{qualifier.version > 0 ? ` (version ${qualifier.version})` : ""}.</p>
                </>
              )}
            </>
          )}
          {step === 1 && (
            <>
              {campaign && (
                <div className="space-y-1">
                  <p className="text-[13px] font-medium">Send people to a campaign {saved}</p>
                  <CampaignFields ws={ws} campaign={campaign} onChange={setCampaign} />
                </div>
              )}
              <label className="flex cursor-pointer items-start justify-between gap-3 text-[13px]">
                <span>
                  <span className="block">Review companies before adding</span>
                  <span className="block text-[12px] text-muted-foreground">For this run. Off adds each company that fits as soon as it is judged.</span>
                </span>
                <Switch checked={review} onCheckedChange={setReview} />
              </label>
            </>
          )}
          {step === 2 && (
            <>
              <label className="block space-y-1 text-[13px]">
                <span className="font-medium">Companies to find that fit</span>
                <input type="number" min={1} max={1000} className={`${field} w-32`} value={target} onChange={(e) => setTarget(Number(e.target.value))} />
                <span className="block text-[12px] text-muted-foreground">Up to 3 people at each, so about {n(target * 3)} people at most.</span>
              </label>
              {estimate && (
                <p className="rounded-md bg-muted px-3 py-2 text-[12px] text-muted-foreground">
                  At most {plural(estimate.searches, "search", "searches")} and {plural(estimate.pages, "page")} on Geonode (about ${estimate.provider_usd.toFixed(2)}), and up to {money(estimate.max_ai_cents)} of AI: about ${estimate.per_company_usd.toFixed(3)} a company at most. People searches and email checks use your data provider and verifier credits. It usually spends less.
                </p>
              )}
            </>
          )}
          <ErrorText>{error}</ErrorText>
          <div className="flex justify-between gap-2">
            <Button variant="ghost" onClick={step === 0 ? onClose : () => setStep(step - 1)}>{step === 0 ? "Cancel" : "Back"}</Button>
            {step < 2 ? <Button onClick={() => setStep(step + 1)} disabled={!profile}>Next</Button> : <Button onClick={start} disabled={busy || !profile || target < 1}>Start</Button>}
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}

function SettingsDialog({ base, settings, onClose, onSaved }: { base: string; settings: Settings; onClose: () => void; onSaved: (s: Settings) => void }) {
  const [s, setS] = useState(settings);
  const [error, setError] = useState("");
  async function save() {
    const { data, error } = await call<Settings>("PATCH", `${base}/settings`, { respect_robots: s.respect_robots, review: s.review, share_index: s.share_index,
      ...(s.generic_emails !== undefined && { generic_emails: s.generic_emails }) });
    if (error) return setError(errorMessage(error));
    onSaved(data!);
  }
  const row = (key: "respect_robots" | "review" | "share_index" | "generic_emails", title: string, body: string) => (
    <label className="flex cursor-pointer items-start justify-between gap-3 text-[13px]">
      <span>
        <span className="block">{title}</span>
        <span className="block text-[12px] text-muted-foreground">{body}</span>
      </span>
      <Switch checked={!!s[key]} onCheckedChange={(v) => setS({ ...s, [key]: v })} />
    </label>
  );
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Lead finder settings</DialogTitle>
          <DialogDescription>For every search in this workspace. Each search's criteria and campaign are set when you start it.</DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          {row("review", "Review companies by default", "New searches start with review on.")}
          {row("respect_robots", "Follow robots.txt", "Skip pages a site's robots.txt asks crawlers not to read. Off reads any public page; it never logs in.")}
          {row("share_index", "Share public company facts", "Facts read from company websites (never people or email addresses) go into a shared index, so a site is read once for everyone. Off neither adds to it nor uses it.")}
          {settings.generic_emails !== undefined && row("generic_emails", "Email shared inboxes too", "Also add a company's published shared addresses, such as info@, contact@ or sales@, as contacts. Off contacts named people only.")}
          <ErrorText>{error}</ErrorText>
          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={onClose}>Cancel</Button>
            <Button onClick={save}>Save</Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}

type Tab = "companies" | "people" | "spend" | "details";

function RunView({ ws, base, id, tab: initialTab, filter, canManage, profiles, onBack, onConnectors, onRunAgain }: {
  ws: string; base: string; id: string; tab?: Tab; filter?: string; canManage: boolean; profiles: CustomerProfile[];
  onBack: () => void; onConnectors: () => void; onRunAgain: (profile: string, target: number) => void;
}) {
  const [run, setRun] = useState<Run | null>(null);
  const [tab, setTab] = useState<Tab | null>(initialTab ?? null);
  const [companyFilter, setCompanyFilter] = useState<string | undefined>(initialTab === "companies" ? filter : undefined);
  const [peopleFilter, setPeopleFilter] = useState<string | undefined>(initialTab === "people" ? filter : undefined);
  const [resuming, setResuming] = useState(false);
  const [editingCampaign, setEditingCampaign] = useState(false);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    const r = await call<Run>("GET", `${base}/runs/${id}`);
    if (r.error) setError(errorMessage(r.error));
    if (r.data) setRun(r.data);
  }, [base, id]);
  useEffect(() => {
    load();
  }, [load]);
  const moving = run && (run.status === "queued" || run.status === "running" || run.pipeline?.state === "running");
  useEffect(() => {
    if (!moving) return;
    const t = setInterval(load, 4000);
    return () => clearInterval(t);
  }, [moving, load]);

  if (!run) return error ? <ErrorText>{error}</ErrorText> : <Loading />;
  const st = runState(run);
  const profile = profiles.find((p) => p.id === run.profile_id);
  const k = run.contacts;
  const people = k ? k.finding + k.added + (k.ready ?? 0) + (k.enrolled ?? 0) + k.stopped : 0;
  const toReview = run.to_review ?? (run.status === "review" ? (run.pipeline?.stages.find((s) => s.stage === "review")?.held ?? 0) : 0);
  // Open the tab that matters: people ready, companies to review, or the
  // companies found.
  const shown: Tab = tab ?? ((k?.ready ?? 0) > 0 ? "people" : "companies");
  const shownPeopleFilter = peopleFilter ?? (tab ? undefined : (k?.ready ?? 0) > 0 ? "ready" : undefined);

  async function act(path: string, body: unknown, done: (d: Record<string, number>) => string) {
    const { data, error } = await call<Record<string, number>>("POST", `${base}/runs/${id}/${path}`, body);
    if (error) return toast(errorMessage(error), "error");
    toast(done(data ?? {}));
    load();
  }
  async function stop() {
    if (!(await confirmAction("Stop this run?", "It stops searching and adding for good. What it found stays, and you can start a new run from the same profile.", "Stop run"))) return;
    act("cancel", {}, () => "Stopped.");
  }
  const connector = !!run.error_code?.startsWith("connector_") || run.pipeline?.pause_code === "ai_model_needed";
  const canPause = canManage && run.pipeline?.state === "running" && (run.status === "running" || run.status === "review" || run.status === "done");
  const canResume = canManage && st.key === "paused";
  const canStop = canManage && (run.status === "running" || run.status === "review" || run.status === "queued");

  return (
    <div className="space-y-5">
      <PageHeader
        back={{ label: "Lead finder", onClick: onBack }}
        title={<span className="inline-flex flex-wrap items-center gap-2">{profile?.name ?? "Deleted profile"} <Badge tone={st.tone}>{st.label}</Badge></span>}
        description={st.why}
        action={canManage && (
          <div className="flex flex-wrap gap-2">
            {canPause && <Button variant="ghost" onClick={() => act("pause", {}, () => "Paused. Resume to carry on.")}>Pause</Button>}
            {canResume && (raises[run.error_code ?? ""] ? <Button onClick={() => setResuming(true)}>Resume</Button> : <Button onClick={() => act("resume", {}, () => "Resumed.")}>Resume</Button>)}
            {connector && st.key === "paused" && <Button variant="ghost" onClick={onConnectors}>Open Connectors</Button>}
            {profile && <Button variant="ghost" onClick={() => setEditingCampaign(true)}>Campaign</Button>}
            {profile && !canStop && <Button variant="ghost" onClick={() => onRunAgain(profile.id, run.target)}>Run again</Button>}
            {canStop && <Button variant="ghost" onClick={stop}>Stop</Button>}
          </div>
        )}
      />
      <ErrorText>{error}</ErrorText>
      {st.key === "paused" && st.why && run.pipeline?.pause_code !== "paused_by_you" && (
        <p className="rounded-lg border border-amber-300 bg-amber-50 px-4 py-3 text-[13px] dark:border-amber-900 dark:bg-amber-950/40">{st.why}</p>
      )}
      {canManage && ((k?.ready ?? 0) > 0 || toReview > 0) && (
        <div className="grid gap-2 sm:grid-cols-2">
          {(k?.ready ?? 0) > 0 && (
            <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-amber-300 bg-amber-50 px-4 py-3 dark:border-amber-900 dark:bg-amber-950/40">
              <div className="min-w-0 text-[13px]">
                <p className="font-medium">{plural(k!.ready!, "person", "people")} ready for {run.campaign ? `"${run.campaign.name}"` : "the campaign"}</p>
                {run.campaign && <p className="text-[12px] text-muted-foreground">It sends to {n(run.campaign.daily_limit)} new people a day.</p>}
              </div>
              <div className="flex gap-2">
                <Button variant="ghost" onClick={() => { setTab("people"); setPeopleFilter("ready"); }}>Review</Button>
                <Button onClick={() => act("contacts/enroll", { all: true }, (d) => `Adding ${plural(d.adding ?? 0, "person", "people")} to the campaign.`)}>Add all</Button>
              </div>
            </div>
          )}
          {toReview > 0 && (
            <div className="flex items-center justify-between gap-2 rounded-lg border border-amber-300 bg-amber-50 px-4 py-3 dark:border-amber-900 dark:bg-amber-950/40">
              <p className="text-[13px] font-medium">{plural(toReview, "company", "companies")} to review</p>
              <Button variant="ghost" onClick={() => { setTab("companies"); setCompanyFilter("to_review"); }}>Review</Button>
            </div>
          )}
        </div>
      )}
      <Funnel run={run} people={people} onPick={(t, f) => { setTab(t); if (t === "companies") setCompanyFilter(f); else setPeopleFilter(f); }} />
      <div className="space-y-3">
        <div className="flex gap-1 overflow-x-auto border-b text-[13px]" role="tablist">
          {([["companies", `Companies ${n(run.found)}`], ["people", `People ${n(people)}`], ["spend", `Spend ${money(run.ai_cents + run.provider_cents)}`], ["details", "Details"]] as [Tab, string][]).map(([t, l]) => (
            <button key={t} type="button" role="tab" aria-selected={shown === t} onClick={() => setTab(t)}
              className={`-mb-px shrink-0 border-b-2 px-3 py-2 ${shown === t ? "border-foreground font-medium" : "border-transparent text-muted-foreground hover:text-foreground"}`}>{l}</button>
          ))}
        </div>
        {shown === "companies" && <Companies base={base} run={run} canManage={canManage} filter={companyFilter} onFilter={setCompanyFilter} onChanged={load} />}
        {shown === "people" && <People base={base} run={run} canManage={canManage} filter={shownPeopleFilter} onFilter={(f) => { setTab("people"); setPeopleFilter(f ?? ""); }} onChanged={load} onCampaign={() => setEditingCampaign(true)} />}
        {shown === "spend" && <Spend run={run} />}
        {shown === "details" && <Details run={run} />}
      </div>
      {resuming && <ResumeDialog base={base} run={run} onClose={() => setResuming(false)} onResumed={() => { setResuming(false); load(); }} />}
      {editingCampaign && profile && <CampaignDialog ws={ws} base={base} profile={profile} onClose={() => setEditingCampaign(false)} onSaved={() => { setEditingCampaign(false); load(); }} />}
    </div>
  );
}

// Funnel is the run from searches to replies, one tile per stage, with the
// stage that holds things up outlined. A tile opens its list, filtered.
function Funnel({ run, people, onPick }: { run: Run; people: number; onPick: (t: Tab, filter?: string) => void }) {
  const k = run.contacts;
  const stage = (id: string) => run.pipeline?.stages.find((s) => s.stage === id);
  const emails = stage("find_email");
  const tiles: { label: string; value: number; sub: string; tab?: Tab; filter?: string; hot?: boolean }[] = [
    { label: "Searched", value: run.searches, sub: `of ${n(run.max_searches)}` },
    { label: "Read", value: run.found, sub: `${n(run.pages)} pages`, tab: "companies" },
    { label: "Fit", value: run.accepted + run.maybe, sub: `${n(run.rejected)} not a fit`, tab: "companies", filter: run.status === "review" ? "to_review" : undefined, hot: run.status === "review" },
    { label: "Added", value: run.added, sub: "companies", tab: "companies", filter: "added" },
    { label: "People", value: people, sub: `${n(emails?.skipped ?? 0)} no email`, tab: "people" },
    { label: "Ready", value: k?.ready ?? 0, sub: "waiting for you", tab: "people", filter: "ready", hot: (k?.ready ?? 0) > 0 },
    { label: "In campaign", value: k?.enrolled ?? 0, sub: `${n(run.results?.replied ?? 0)} replied`, tab: "people", filter: "enrolled" },
    ...(run.results?.meetings !== undefined ? [{ label: "Meetings", value: run.results.meetings, sub: "booked" }] : []),
  ];
  return (
    <div className={`grid grid-cols-2 gap-1.5 sm:grid-cols-4 ${tiles.length > 7 ? "lg:grid-cols-8" : "lg:grid-cols-7"}`}>
      {tiles.map((t) => (
        <button key={t.label} type="button" disabled={!t.tab} onClick={() => t.tab && onPick(t.tab, t.filter)}
          className={`min-w-0 rounded-lg border bg-card px-3 py-2 text-left shadow-xs enabled:hover:bg-accent/40 ${t.hot ? "border-amber-400 ring-1 ring-amber-400 dark:border-amber-700 dark:ring-amber-700" : ""}`}>
          <span className="block truncate font-mono text-[10.5px] tracking-wide text-muted-foreground uppercase">{t.label}</span>
          <span className="block font-mono text-xl font-medium tabular-nums">{n(t.value)}</span>
          <span className={`block truncate text-[11.5px] ${t.hot ? "text-amber-700 dark:text-amber-400" : "text-muted-foreground"}`}>{t.sub}</span>
        </button>
      ))}
    </div>
  );
}

// usePage loads one page of a list from the server, again when its query
// changes or the run moves on.
function usePage<T>(path: string, version: string) {
  const [data, setData] = useState<(T & { total: number; offset: number }) | null>(null);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    const r = await call<T & { total: number; offset: number }>("GET", path);
    if (r.error) return setError(errorMessage(r.error));
    setError("");
    setData(r.data!);
  }, [path]);
  useEffect(() => {
    load();
  }, [load, version]);
  return { data, error, load };
}

const pageSize = 50;

function Pager({ offset, total, onOffset }: { offset: number; total: number; onOffset: (o: number) => void }) {
  if (total <= pageSize) return total > 0 ? <p className="text-[12px] text-muted-foreground">{plural(total, "row")}</p> : null;
  return (
    <div className="flex items-center justify-between gap-2 text-[12px] text-muted-foreground">
      <span className="tabular-nums">{n(offset + 1)}–{n(Math.min(total, offset + pageSize))} of {n(total)}</span>
      <span className="flex gap-2">
        <Button variant="ghost" disabled={offset === 0} onClick={() => onOffset(Math.max(0, offset - pageSize))}>Previous</Button>
        <Button variant="ghost" disabled={offset + pageSize >= total} onClick={() => onOffset(offset + pageSize)}>Next</Button>
      </span>
    </div>
  );
}

function Chips({ value, options, onChange }: { value: string; options: { id: string; label: string; count?: number }[]; onChange: (v: string) => void }) {
  return (
    <div className="flex flex-wrap gap-1.5" role="group">
      {options.map((o) => (
        <button key={o.id} type="button" onClick={() => onChange(o.id)} aria-pressed={value === o.id}
          className={`rounded-full border px-2.5 py-1 text-[12px] ${value === o.id ? "border-foreground bg-foreground text-background" : "bg-card text-muted-foreground hover:text-foreground"}`}>
          {o.label}{o.count !== undefined && <span className="ml-1 tabular-nums opacity-70">{n(o.count)}</span>}
        </button>
      ))}
    </div>
  );
}

function BulkBar({ children }: { children: ReactNode }) {
  return <div className="flex flex-wrap items-center gap-2 rounded-lg border bg-muted/50 px-3 py-2 text-[13px]">{children}</div>;
}

const candidateStates = [
  { id: "to_review", label: "To review" }, { id: "adding", label: "Adding" }, { id: "added", label: "Added" },
  { id: "blocked", label: "Stopped" }, { id: "dismissed", label: "Dismissed" }, { id: "not_a_fit", label: "Not a fit" },
];

function Companies({ base, run, canManage, filter, onFilter, onChanged }: { base: string; run: Run; canManage: boolean; filter?: string; onFilter: (f?: string) => void; onChanged: () => void }) {
  const [q, setQ] = useState("");
  const dq = useDebounced(q);
  const [offset, setOffset] = useState(0);
  const [minScore, setMinScore] = useState(0);
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const state = filter ?? "";
  const params = new URLSearchParams({ limit: String(pageSize), offset: String(offset), ...(state ? { state } : {}), ...(dq ? { q: dq } : {}) });
  const version = `${run.found}/${run.added}/${run.accepted}`;
  const { data, error, load } = usePage<{ data: Candidate[]; states: Record<string, number> }>(`${base}/runs/${run.id}/candidates?${params}`, version);
  useEffect(() => { setOffset(0); setPicked(new Set()); }, [state, dq]);
  const states = data?.states ?? {};
  const all = Object.values(states).reduce((a, b) => a + b, 0);

  async function act(kind: "add" | "dismiss", body: Record<string, unknown>) {
    const r = await call<{ adding?: number; dismissed?: number }>("POST", `${base}/runs/${run.id}/candidates/${kind}`, body);
    if (r.error) return toast(errorMessage(r.error), "error");
    toast(kind === "add" ? `Adding ${plural(r.data?.adding ?? 0, "company", "companies")}. Each is checked against your opt-out list first.` : `Dismissed ${plural(r.data?.dismissed ?? 0, "company", "companies")}.`);
    setPicked(new Set());
    load();
    onChanged();
  }
  const reviewing = canManage && state === "to_review" && (states.to_review ?? 0) > 0;
  const rows = data?.data ?? [];
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <Chips value={state} onChange={(v) => onFilter(v || undefined)} options={[{ id: "", label: "All", count: all }, ...candidateStates.filter((s) => (states[s.id] ?? 0) > 0 || s.id === state).map((s) => ({ ...s, count: states[s.id] ?? 0 }))]} />
        <input type="search" className={`${field} w-full sm:w-64`} value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search name or domain" aria-label="Search companies" />
      </div>
      {reviewing && (
        <BulkBar>
          {picked.size > 0 ? (
            <>
              <span>{n(picked.size)} selected</span>
              <Button onClick={() => act("add", { ids: [...picked] })}>Add</Button>
              <Button variant="ghost" onClick={() => act("dismiss", { ids: [...picked] })}>Dismiss</Button>
            </>
          ) : (
            <>
              <span>All {n(states.to_review ?? 0)} to review</span>
              <label className="flex items-center gap-1.5 text-muted-foreground">scoring
                <select className={field} value={minScore} onChange={(e) => setMinScore(Number(e.target.value))} aria-label="Minimum score">
                  {[0, 50, 60, 70, 80, 90].map((v) => <option key={v} value={v}>{v === 0 ? "any" : `${v}+`}</option>)}
                </select>
              </label>
              <Button onClick={() => act("add", { verdicts: ["accept", "maybe"], ...(minScore ? { min_score: minScore } : {}) })}>{minScore ? `Add those scoring ${minScore}+` : "Add all"}</Button>
              {minScore > 0 && <Button variant="ghost" onClick={() => act("dismiss", { verdicts: ["accept", "maybe"], min_score: minScore })}>Dismiss the rest</Button>}
            </>
          )}
        </BulkBar>
      )}
      <ErrorText>{error}</ErrorText>
      {!data ? <Loading /> : rows.length === 0 ? (
        <Empty title={dq || state ? "No companies match" : run.status === "running" ? "Searching" : "No companies found"}>{dq || state ? "Try another filter." : run.status === "running" ? "Companies show up here as they are read." : "Try a broader profile or looser criteria."}</Empty>
      ) : (
        <div className="overflow-x-auto rounded-lg border bg-card shadow-xs">
          <table className="w-full text-[13px]">
            <thead className="text-left font-mono text-[11px] tracking-wide text-muted-foreground uppercase">
              <tr className="border-b">
                {reviewing && <th className="w-8 px-3 py-2"><Checkbox aria-label="Select this page" checked={rows.every((c) => picked.has(c.id))} onCheckedChange={(v) => setPicked(v ? new Set(rows.map((c) => c.id)) : new Set())} /></th>}
                <th className="px-3 py-2 font-medium">Company</th><th className="px-3 py-2 font-medium">Fit</th><th className="px-3 py-2 font-medium">Why</th><th className="px-3 py-2 font-medium" />
              </tr>
            </thead>
            <tbody>
              {rows.map((c) => {
                const warnings = (c.warnings ?? []).map(warningLabel);
                return (
                  <tr key={c.id} className="border-b align-top last:border-0">
                    {reviewing && <td className="px-3 py-2"><Checkbox aria-label={`Select ${c.name || c.domain}`} checked={picked.has(c.id)} onCheckedChange={() => setPicked((s) => { const x = new Set(s); if (x.has(c.id)) x.delete(c.id); else x.add(c.id); return x; })} /></td>}
                    <td className="max-w-md px-3 py-2">
                      <span className="block truncate font-medium">{c.name || c.domain} <a className="font-normal text-muted-foreground hover:underline" href={`https://${c.domain}`} target="_blank" rel="noreferrer noopener">{c.domain}</a></span>
                      {typeof c.facts.summary === "string" && c.facts.summary && <span className="block truncate text-[12px] text-muted-foreground">{c.facts.summary}</span>}
                    </td>
                    <td className="px-3 py-2"><Badge tone={verdictTone[c.verdict]}>{c.verdict === "unread" ? "Not read" : `${c.verdict === "accept" ? "Fits" : c.verdict === "maybe" ? "Maybe" : "No"}${c.score != null ? ` · ${c.score}` : ""}`}</Badge></td>
                    <td className="max-w-sm px-3 py-2"><span className="block truncate text-[12px] text-muted-foreground" title={c.reasons.join("\n")}>{c.reasons[0] ?? ""}</span></td>
                    <td className="px-3 py-2">
                      <span className="inline-flex flex-wrap gap-1">
                        {c.status === "added" ? <Badge tone="green">Added</Badge> : c.blocked ? <Badge tone="red">Stopped: {label(c.blocked)}</Badge> : c.status === "dismissed" ? <Badge>Dismissed</Badge> : c.picked ? <Badge tone="blue">Adding</Badge> : null}
                        {warnings.length > 0 && <Badge tone="amber" title={warnings.join("\n")}>{warnings.length === 1 ? warnings[0] : "Check"}</Badge>}
                      </span>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      {data && <Pager offset={offset} total={data.total} onOffset={setOffset} />}
    </div>
  );
}

const peopleStatuses = [
  { id: "ready", label: "Ready" }, { id: "finding", label: "Finding email" }, { id: "added", label: "In CRM" },
  { id: "enrolled", label: "In campaign" }, { id: "stopped", label: "Not added" },
];
const sourceLabel = { website: "Their website", provider: "Data provider", inbox: "Shared inbox" } as const;

function People({ base, run, canManage, filter, onFilter, onChanged, onCampaign }: {
  base: string; run: Run; canManage: boolean; filter?: string; onFilter: (f?: string) => void; onChanged: () => void; onCampaign: () => void;
}) {
  const [q, setQ] = useState("");
  const dq = useDebounced(q);
  const [offset, setOffset] = useState(0);
  const [confirmed, setConfirmed] = useState("");
  const [stopCode, setStopCode] = useState("");
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const status = filter ?? "";
  const params = new URLSearchParams({ limit: String(pageSize), offset: String(offset), ...(status ? { status } : {}), ...(dq ? { q: dq } : {}),
    ...(confirmed ? { confirmed } : {}), ...(status === "stopped" && stopCode ? { stop_code: stopCode } : {}) });
  const k = run.contacts;
  const version = k ? `${k.finding}/${k.added}/${k.ready}/${k.enrolled}/${k.stopped}` : "";
  const { data, error, load } = usePage<{ data: Contact[]; statuses: Record<string, number>; stop_codes: Record<string, number> }>(`${base}/runs/${run.id}/contacts?${params}`, version);
  useEffect(() => { setOffset(0); setPicked(new Set()); }, [status, dq, confirmed, stopCode]);
  const statuses = data?.statuses ?? {};
  const all = Object.values(statuses).reduce((a, b) => a + b, 0);
  const rows = data?.data ?? [];
  const selecting = canManage && status === "ready" && (statuses.ready ?? 0) > 0;

  async function act(kind: "enroll" | "skip", body: Record<string, unknown>) {
    const r = await call<{ adding?: number; skipped?: number }>("POST", `${base}/runs/${run.id}/contacts/${kind}`, body);
    if (r.error) return toast(errorMessage(r.error), "error");
    toast(kind === "enroll" ? `Adding ${plural(r.data?.adding ?? 0, "person", "people")} to the campaign.` : `Kept ${plural(r.data?.skipped ?? 0, "person", "people")} out of the campaign. They stay in your CRM.`);
    setPicked(new Set());
    load();
    onChanged();
  }
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border bg-card px-4 py-2.5 text-[13px]">
        {run.campaign ? (
          <span>Going into <b>{run.campaign.name}</b> · {n(run.campaign.daily_limit)} new a day · {run.campaign.auto_add ? "added without asking (unconfirmed emails wait)" : "you approve each batch"}</span>
        ) : <span className="text-muted-foreground">No campaign: people stay in your CRM.</span>}
        {canManage && <Button variant="ghost" onClick={onCampaign}>{run.campaign ? "Change" : "Pick a campaign"}</Button>}
      </div>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <Chips value={status} onChange={(v) => onFilter(v || undefined)} options={[{ id: "", label: "All", count: all }, ...peopleStatuses.filter((s) => (statuses[s.id] ?? 0) > 0 || s.id === status).map((s) => ({ ...s, count: statuses[s.id] ?? 0 }))]} />
        <div className="flex flex-wrap gap-2">
          {status === "stopped" && (
            <select className={field} value={stopCode} onChange={(e) => setStopCode(e.target.value)} aria-label="Why">
              <option value="">Any reason</option>
              {Object.entries(data?.stop_codes ?? {}).map(([c, v]) => <option key={c} value={c}>{label(c)} ({n(v)})</option>)}
            </select>
          )}
          <select className={field} value={confirmed} onChange={(e) => setConfirmed(e.target.value)} aria-label="Email">
            <option value="">Any email</option>
            <option value="true">Confirmed emails</option>
            <option value="false">Unconfirmed</option>
          </select>
          <input type="search" className={`${field} w-full sm:w-56`} value={q} onChange={(e) => setQ(e.target.value)} placeholder="Name, title, email or domain" aria-label="Search people" />
        </div>
      </div>
      {selecting && (
        <BulkBar>
          {picked.size > 0 ? (
            <>
              <span>{n(picked.size)} selected</span>
              <Button onClick={() => act("enroll", { contact_ids: [...picked] })}>Add to campaign</Button>
              <Button variant="ghost" onClick={() => act("skip", { contact_ids: [...picked] })}>Keep out</Button>
            </>
          ) : (
            <>
              <span>All {n(statuses.ready ?? 0)} ready</span>
              <Button onClick={() => act("enroll", { all: true, confirmed_only: confirmed === "true" })}>{confirmed === "true" ? "Add confirmed ones" : "Add all to campaign"}</Button>
              {confirmed !== "true" && <Button variant="ghost" onClick={() => act("enroll", { all: true, confirmed_only: true })}>Add confirmed only</Button>}
              <Button variant="ghost" onClick={async () => { if (await confirmAction("Keep everyone ready out of the campaign?", "They stay in your CRM, marked as skipped.", "Keep them out")) act("skip", { all: true, confirmed_only: confirmed === "true" }); }}>Keep all out</Button>
            </>
          )}
        </BulkBar>
      )}
      <ErrorText>{error}</ErrorText>
      {!data ? <Loading /> : rows.length === 0 ? (
        <Empty title={dq || status || confirmed ? "Nobody matches" : "No people yet"}>{dq || status || confirmed ? "Try another filter." : "People show up here once companies are added."}</Empty>
      ) : (
        <div className="overflow-x-auto rounded-lg border bg-card shadow-xs">
          <table className="w-full text-[13px]">
            <thead className="text-left font-mono text-[11px] tracking-wide text-muted-foreground uppercase">
              <tr className="border-b">
                {selecting && <th className="w-8 px-3 py-2"><Checkbox aria-label="Select this page" checked={rows.every((x) => picked.has(x.id))} onCheckedChange={(v) => setPicked(v ? new Set(rows.map((x) => x.id)) : new Set())} /></th>}
                <th className="px-3 py-2 font-medium">Person</th><th className="px-3 py-2 font-medium">Company</th><th className="px-3 py-2 font-medium">Email</th>
                <th className="px-3 py-2 font-medium">From</th><th className="px-3 py-2 font-medium" />
              </tr>
            </thead>
            <tbody>
              {rows.map((x) => {
                const name = `${x.first_name} ${x.last_name}`.trim();
                const warnings = x.warnings.filter((w) => w !== "risky_email").map(warningLabel);
                return (
                  <tr key={x.id} className="border-b align-top last:border-0">
                    {selecting && <td className="px-3 py-2">{x.status === "ready" && <Checkbox aria-label={`Select ${name || x.email}`} checked={picked.has(x.id)} onCheckedChange={() => setPicked((s) => { const y = new Set(s); if (y.has(x.id)) y.delete(x.id); else y.add(x.id); return y; })} />}</td>}
                    <td className="px-3 py-2">{name || <span className="text-muted-foreground">Shared inbox</span>}{x.title && <span className="block text-[12px] text-muted-foreground">{x.title}</span>}</td>
                    <td className="px-3 py-2 text-muted-foreground">{x.domain}</td>
                    <td className="px-3 py-2 font-mono text-[12px]">{x.email || "…"}{x.email_code === "risky" && <span className="ml-1 font-sans text-amber-600 dark:text-amber-400" title="Nothing could confirm this address">unconfirmed</span>}</td>
                    <td className="px-3 py-2 text-[12px] text-muted-foreground">{sourceLabel[x.source]}</td>
                    <td className="px-3 py-2">
                      <span className="inline-flex flex-wrap gap-1">
                        {x.status === "added" ? <Badge tone="green">In CRM</Badge>
                          : x.status === "ready" ? <Badge tone="amber">Ready</Badge>
                          : x.status === "enrolled" ? <Badge tone="green">In campaign</Badge>
                          : x.status === "stopped" ? <Badge tone={x.stop_code === "skipped" ? "grey" : "red"} title={x.warnings.join("\n")}>{x.record_id ? "In CRM, not in campaign" : "Not added"}: {label(x.stop_code)}</Badge>
                          : <Badge tone="blue">Finding email</Badge>}
                        {x.status !== "stopped" && x.status !== "finding" && warnings.length > 0 && <Badge tone="amber" title={warnings.join("\n")}>{warnings.length === 1 ? warnings[0] : "Check"}</Badge>}
                      </span>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      {data && <Pager offset={offset} total={data.total} onOffset={setOffset} />}
    </div>
  );
}

// Spend is what the run cost on the workspace's own accounts.
function Spend({ run }: { run: Run }) {
  const sp = run.spend ?? { ai_cents: run.ai_cents, web_cents: run.provider_cents, web_tokens: run.search_tokens + run.page_tokens, people_credits: 0 };
  const money$ = sp.ai_cents + sp.web_cents;
  const fit = run.accepted + run.maybe;
  const inCampaign = run.contacts?.enrolled ?? 0;
  return (
    <div className="space-y-3">
      <Stats cols={4}>
        <Stat label="AI" value={money(sp.ai_cents)} sub={`your AI key, up to ${money(run.max_ai_cents)}`} />
        <Stat label="Web (Geonode)" value={sp.web_cents ? money(sp.web_cents) : `${n(sp.web_tokens)} tokens`} sub={`${plural(run.searches, "search", "searches")}, ${plural(run.pages, "page")}`} />
        <Stat label="Data provider" value={`${n(sp.people_credits)}`} sub="credits for people searches" />
        <Stat label="Per result" value={run.results?.meetings ? money(money$ / run.results.meetings) : inCampaign ? usd(money$ / inCampaign) : fit ? usd(money$ / fit) : "–"}
          sub={run.results?.meetings ? "a meeting" : inCampaign ? "a person in the campaign" : "a company that fits"} />
      </Stats>
      <p className="text-[12px] text-muted-foreground">Everything runs on your own accounts. Email checks use your verifier's credits, which show on the Connectors page.</p>
    </div>
  );
}

// Details is for troubleshooting: every step with what it did, and the
// searches the run planned.
function Details({ run }: { run: Run }) {
  return (
    <div className="space-y-3">
      {run.pipeline && run.pipeline.stages.length > 0 ? <Steps pipeline={run.pipeline} /> : <p className="text-[13px] text-muted-foreground">This run has no step-by-step record.</p>}
      {run.queries.length > 0 && (
        <details className="text-[12px] text-muted-foreground">
          <summary className="cursor-pointer">{plural(run.queries.length, "search", "searches")} planned</summary>
          <ul className="mt-2 list-disc space-y-0.5 pl-5">{run.queries.map((q) => <li key={q}>{q}</li>)}</ul>
        </details>
      )}
    </div>
  );
}

// Steps shows where the run's items are: how many each step has waiting
// and in progress, passed on, set aside (with why) and failed, and how long
// the oldest one has waited, so a slow step stands out.
function Steps({ pipeline }: { pipeline: Pipeline }) {
  const byId = Object.fromEntries(pipeline.stages.map((s) => [s.stage, s]));
  const known = stages.filter((s) => byId[s.id]);
  const extra = pipeline.stages.filter((s) => !stages.some((k) => k.id === s.stage)).map((s) => ({ id: s.stage, label: s.stage, what: "" }));
  const slowest = Math.max(0, ...pipeline.stages.map((s) => s.oldest_waiting_seconds));
  return (
    <Card title="Progress by step" description="Each search, company and person moves through these steps.">
      <div className="overflow-x-auto">
        <table className="w-full text-[13px]">
          <thead className="text-left font-mono text-[11px] tracking-wide text-muted-foreground uppercase">
            <tr>
              <th className="py-1.5 pr-3 font-medium">Step</th>
              <th className="py-1.5 pr-3 text-right font-medium">To do</th>
              <th className="py-1.5 pr-3 text-right font-medium">Working</th>
              <th className="py-1.5 pr-3 text-right font-medium">Passed</th>
              <th className="py-1.5 pr-3 text-right font-medium">Set aside</th>
              <th className="py-1.5 pr-3 text-right font-medium">Failed</th>
              <th className="py-1.5 font-medium">Notes</th>
            </tr>
          </thead>
          <tbody>
            {[...known, ...extra].map((k) => {
              const s = byId[k.id];
              const passed = s.entered - s.waiting - s.working - s.skipped - s.held - s.failed;
              const notes = Object.entries(s.errors).sort((a, b) => b[1] - a[1]).slice(0, 3).map(([c, v]) => `${label(c)} ${v}`);
              if (s.held > 0) notes.unshift(`${s.held} waiting for you`);
              const slow = s.oldest_waiting_seconds >= 60 && s.oldest_waiting_seconds === slowest;
              return (
                <tr key={k.id} className="border-t align-top">
                  <td className="py-1.5 pr-3" title={k.what}>{k.label}</td>
                  <td className="py-1.5 pr-3 text-right tabular-nums">{s.waiting || ""}{s.oldest_waiting_seconds >= 60 && <span className={`ml-1 text-[11px] ${slow ? "text-amber-600 dark:text-amber-400" : "text-muted-foreground"}`}>oldest {waited(s.oldest_waiting_seconds)}</span>}</td>
                  <td className="py-1.5 pr-3 text-right tabular-nums">{s.working || ""}</td>
                  <td className="py-1.5 pr-3 text-right tabular-nums">{passed > 0 ? passed : ""}</td>
                  <td className="py-1.5 pr-3 text-right tabular-nums">{s.skipped || ""}</td>
                  <td className={`py-1.5 pr-3 text-right tabular-nums ${s.failed ? "text-red-600 dark:text-red-400" : ""}`}>{s.failed || ""}</td>
                  <td className="py-1.5 text-[12px] text-muted-foreground">{notes.join(", ")}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

// CampaignDialog changes where a profile's people go, from a run. It
// applies to people not yet in a campaign, on every run of the profile.
function CampaignDialog({ ws, base, profile, onClose, onSaved }: { ws: string; base: string; profile: CustomerProfile; onClose: () => void; onSaved: () => void }) {
  const [campaign, setCampaign] = useState<Campaign | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    call<Campaign>("GET", `${base}/profiles/${profile.id}/campaign`).then(({ data, error }) => (data ? setCampaign(data) : setError(errorMessage(error))));
  }, [base, profile.id]);
  async function save() {
    if (!campaign) return;
    const { error } = await call<Campaign>("PUT", `${base}/profiles/${profile.id}/campaign`, { sequence_id: campaign.sequence_id ?? "", auto_add: campaign.auto_add });
    if (error) return setError(errorMessage(error));
    toast("Saved. It applies to people not yet in a campaign.");
    onSaved();
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Campaign for {profile.name}</DialogTitle>
          <DialogDescription>Where this profile's people go, on every run. People already in a campaign stay where they are.</DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          {campaign ? <CampaignFields ws={ws} campaign={campaign} onChange={setCampaign} /> : !error && <Loading />}
          <ErrorText>{error}</ErrorText>
          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={onClose}>Cancel</Button>
            <Button onClick={save} disabled={!campaign}>Save</Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}

// ResumeDialog lets a paused run carry on, raising the limit it reached
// first. Limits can only go up.
function ResumeDialog({ base, run, onClose, onResumed }: { base: string; run: Run; onClose: () => void; onResumed: () => void }) {
  const raise = run.error_code ? raises[run.error_code] : undefined;
  const [limits, setLimits] = useState({
    max_pages: raise?.field === "max_pages" ? run.max_pages * 2 : run.max_pages,
    max_ai_cents: raise?.field === "max_ai_cents" ? Math.max(run.max_ai_cents * 2, 50) : run.max_ai_cents,
    max_searches: run.max_searches,
  });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function resume() {
    setBusy(true);
    const { error } = await call<Run>("POST", `${base}/runs/${run.id}/resume`, limits);
    setBusy(false);
    if (error) return setError(errorMessage(error));
    toast("Resumed. This page updates as it goes.");
    onResumed();
  }
  const input = (key: keyof typeof limits, title: string, min: number, max: number) => (
    <label className="block space-y-1 text-[13px]">
      <span className={`block ${key === raise?.field ? "font-medium" : ""}`}>{title}</span>
      <input type="number" min={min} max={max} className={`${field} w-32`} value={limits[key]} onChange={(e) => setLimits({ ...limits, [key]: Number(e.target.value) })} />
    </label>
  );
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Resume the run</DialogTitle>
          <DialogDescription>{raise ? "Raise the limit it reached, then it carries on where it stopped." : "Fix what stopped it first, then it carries on where it stopped."}</DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          {input("max_pages", raises.page_budget_reached.label, run.max_pages, 5000)}
          {input("max_ai_cents", raises.ai_budget_reached.label, run.max_ai_cents, 100000)}
          {input("max_searches", "Searches it may run", run.max_searches, 500)}
          <p className="text-[12px] text-muted-foreground">Limits can only go up. Used so far: {plural(run.pages, "page")}, {money(run.ai_cents)} of AI, {plural(run.searches, "search", "searches")}.</p>
          <ErrorText>{error}</ErrorText>
          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={onClose}>Cancel</Button>
            <Button onClick={resume} disabled={busy}>Resume</Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
