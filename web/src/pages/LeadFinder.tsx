// The lead finder (Outflare Cloud): AI runs that search the web for
// companies that fit an ideal customer profile, read their own websites
// and qualify them, for a person to review before they become company
// records. The server lists the lead_finder capability where it runs; its
// routes are the hosted extension's, so they are called without the typed
// client. Everything runs on the workspace's own Geonode and AI accounts.

import { useCallback, useEffect, useMemo, useState } from "react";
import type { CustomerProfile } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, Empty, ErrorText, field, Loading, PageHeader, Stat, Stats, toast } from "../ui";
import { ago, plural } from "../format";
import { DataTable, type ColumnDef } from "@/components/data-table";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Switch } from "@/components/ui/switch";
import { TagInput } from "@/components/icp";

type Props = { ws: string; canManage: boolean; onConnectors: () => void; onProfiles: () => void };

type Settings = { respect_robots: boolean; review: boolean; share_index: boolean; eu_notice_accepted_at: string | null };
type Qualifier = { id: string | null; profile_id: string | null; version: number; accept: string[]; reject: string[] };
type Estimate = { searches: number; pages: number; max_ai_cents: number; provider_usd: number; per_company_usd: number };
type Run = {
  id: string; profile_id: string | null; status: "queued" | "running" | "review" | "done" | "failed" | "cancelled"; target: number;
  max_searches: number; max_pages: number; max_ai_cents: number; review: boolean; queries: string[]; searches: number; pages: number;
  found: number; accepted: number; maybe: number; rejected: number; added: number; search_tokens: number; page_tokens: number;
  provider_cents: number; ai_cents: number; error_code: string | null; error: string | null; created_at: string; finished_at: string | null;
  // The run's progress per step; only on a single run, and only for runs
  // that work company by company.
  pipeline?: Pipeline;
};
type Pipeline = { state: "running" | "paused" | "cancelled" | "finished"; pause_code?: string; pause_reason?: string; stages: StageCount[] };
type StageCount = {
  stage: string; waiting: number; working: number; done: number; skipped: number; held: number; failed: number;
  entered: number; retried: number; waited: number; busy_ms: number; errors: Record<string, number>; oldest_waiting_seconds: number;
};
type Candidate = {
  id: string; domain: string; name: string; facts: Record<string, unknown>; emails: string[]; verdict: "accept" | "maybe" | "reject" | "unread";
  score: number | null; reasons: string[]; query: string; status: "pending" | "added" | "dismissed"; record_id: string | null; from_index: boolean;
  // Chosen to add and on its way through the contact rules to the CRM;
  // blocked when the contact rules stopped it. Warnings are opt-out list
  // entries (suppressed_domain:customer).
  picked?: boolean; blocked?: string | null; warnings?: string[];
};
type Failure = { error: { code: string; message: string } };

// call reaches the lead finder's routes, with the same session as the
// typed client and errors in the engine's shape.
async function call<T>(method: string, path: string, body?: unknown): Promise<{ data?: T; error?: Failure }> {
  try {
    const res = await fetch(path, { method, credentials: "same-origin", headers: body ? { "Content-Type": "application/json" } : {}, body: body ? JSON.stringify(body) : undefined });
    const json = await res.json().catch(() => ({}));
    return res.ok ? { data: json as T } : { error: json as Failure };
  } catch {
    return { error: { error: { code: "network_error", message: "Could not reach Outflare. Check your connection and try again." } } };
  }
}

const verdictTone = { accept: "green", maybe: "amber", reject: "red", unread: "grey" } as const;
const statusTone = { queued: "grey", running: "blue", review: "amber", done: "green", failed: "red", cancelled: "grey" } as const;
const statusLabel = { queued: "Queued", running: "Finding", review: "To review", done: "Done", failed: "Failed", cancelled: "Cancelled" } as const;
// What an opt-out list entry that let a company through means.
const warningLabel = (w: string) => {
  const why = w.split(":")[1] ?? w;
  return why === "customer" ? "A customer" : why === "partner" ? "A partner" : why === "competitor" ? "A competitor" : `On your opt-out list (${why})`;
};
const adding = (c: Candidate) => c.status === "pending" && !!c.picked && !c.blocked;
const usd = (n: number) => `$${n < 1 ? n.toFixed(3) : n.toFixed(2)}`;

// A running run with an error code is paused: it waits for the person to
// raise a limit or fix a connection, then resume.
const paused = (r: Run) => r.status === "running" && (r.pipeline ? r.pipeline.state === "paused" : r.error_code != null);
const runBadge = (r: Run) => paused(r) ? <Badge tone="amber">Paused</Badge> : <Badge tone={statusTone[r.status]}>{statusLabel[r.status]}</Badge>;

// The steps a company goes through, in order, and what the codes a step
// records mean. Unknown codes show as they are.
const stages: { id: string; label: string; what: string }[] = [
  { id: "search", label: "Search", what: "Plans searches and runs them on Geonode" },
  { id: "read", label: "Read website", what: "Reads each new company's homepage and contact or about pages" },
  { id: "qualify", label: "Judge fit", what: "Your AI says what the company is and whether it fits" },
  { id: "review", label: "Review", what: "Waits for you to add or dismiss, or sends a fit on straight away with review off" },
  { id: "known", label: "Already known", what: "Skips companies already in your CRM" },
  { id: "contact_rules", label: "Contact rules", what: "Stops companies that opted out or bounced; customers, partners and competitors only warn" },
  { id: "add_to_crm", label: "Add to CRM", what: "Adds the company as a prospect" },
];
const codeLabel: Record<string, string> = {
  target_reached: "enough found", rejected: "not a fit", unreachable: "site couldn't be read", no_ideas: "no new searches",
  no_answer: "no clear answer", cancelled: "stopped", reviewed: "reviewed", review: "waiting for you", maybe: "maybe, left for you",
  added: "added", dismissed: "dismissed", not_found: "gone", rate_limited: "slowed by rate limit", provider_error: "provider error, retried",
  connector_quota: "Geonode limit", page_budget_reached: "page limit", ai_budget_reached: "AI limit", search_budget: "search limit",
  interrupted: "interrupted, retried", internal: "error, retried", stage_error: "error, retried", no_result: "retried",
  already_known: "already in your CRM", opted_out: "opted out", bounced: "bounced", not_a_company: "not a company domain",
  connector_needed: "needs a connection", connector_auth: "key refused", connector_unavailable: "provider down, retried",
  ai_model_needed: "needs an AI model", usage_limit_reached: "usage limit",
};
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

export function LeadFinder({ ws, canManage, onConnectors, onProfiles }: Props) {
  const base = `/v1/workspaces/${ws}/cloud/lead-finder`;
  const [settings, setSettings] = useState<Settings | null>(null);
  const [profiles, setProfiles] = useState<CustomerProfile[]>([]);
  const [runs, setRuns] = useState<Run[] | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  const [starting, setStarting] = useState(false);
  const [editingSettings, setEditingSettings] = useState(false);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    const [s, r] = await Promise.all([call<Settings>("GET", `${base}/settings`), call<{ data: Run[] }>("GET", `${base}/runs`)]);
    if (s.error || r.error) setError(errorMessage(s.error ?? r.error));
    if (s.data) setSettings(s.data);
    setRuns(r.data?.data ?? []);
  }, [base]);
  useEffect(() => {
    load();
    api.GET("/v1/workspaces/{ws}/ideal-customer-profiles", { params: { path: { ws } } }).then(({ data }) => setProfiles(data?.data ?? []));
  }, [load, ws]);
  // Refresh while a run is finding.
  useEffect(() => {
    if (!runs?.some((r) => r.status === "queued" || r.status === "running")) return;
    const t = setInterval(load, 3000);
    return () => clearInterval(t);
  }, [runs, load]);

  if (!settings || !runs) return error ? <ErrorText>{error}</ErrorText> : <Loading />;
  if (open) return <RunView base={base} id={open} canManage={canManage} profiles={profiles} onBack={() => { setOpen(null); load(); }} onConnectors={onConnectors} />;

  const profileName = Object.fromEntries(profiles.map((p) => [p.id, p.name])) as Record<string, string>;
  const columns: ColumnDef<Run>[] = [
    { id: "profile", header: "Looking for", accessorFn: (r) => (r.profile_id ? profileName[r.profile_id] ?? "Deleted profile" : "Deleted profile") },
    { id: "status", header: "Status", accessorFn: (r) => (paused(r) ? "paused" : r.status), cell: ({ row: { original: r } }) => runBadge(r) },
    { id: "found", header: "Fit", accessorFn: (r) => r.accepted + r.maybe, cell: ({ row: { original: r } }) => `${r.accepted + r.maybe} of ${r.target}` },
    { id: "added", header: "Added", accessorFn: (r) => r.added },
    { id: "spend", header: "AI", accessorFn: (r) => r.ai_cents, cell: ({ row: { original: r } }) => usd(r.ai_cents / 100) },
    { id: "created", header: "Started", accessorFn: (r) => r.created_at, cell: ({ row: { original: r } }) => ago(r.created_at) },
  ];
  return (
    <div className="space-y-6">
      <PageHeader
        title="Find companies"
        description="Outflare searches the web for companies that fit an ideal customer profile, reads their websites and says which fit and why. You review them before they are added. Searches and page reads use your Geonode account, and judging uses your AI key."
        action={canManage && settings.eu_notice_accepted_at && (
          <div className="flex gap-2">
            <Button variant="ghost" onClick={() => setEditingSettings(true)}>Settings</Button>
            <Button onClick={() => setStarting(true)} disabled={profiles.length === 0}>Find companies</Button>
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
          The lead finder looks for companies that fit an ideal customer profile: who they are and why they buy, in words, plus the industries, sizes and places that fit.
        </Empty>
      ) : runs.length === 0 ? (
        <Empty title="No runs yet" action={canManage && <Button onClick={() => setStarting(true)}>Find companies</Button>}>
          Pick a profile and how many companies you want. You see the most it can cost before it starts.
        </Empty>
      ) : (
        <DataTable columns={columns} data={runs} rowId={(r) => r.id} onRowClick={(r) => setOpen(r.id)}
          mobile={(r) => (
            <span className="block min-w-0">
              <span className="block truncate text-[13px] font-medium">{r.profile_id ? profileName[r.profile_id] : "Deleted profile"}</span>
              <span className="block truncate text-[12px] text-muted-foreground">{paused(r) ? "Paused" : statusLabel[r.status]} · {r.accepted + r.maybe} of {r.target} fit · {ago(r.created_at)}</span>
            </span>
          )} />
      )}
      {starting && <StartDialog base={base} profiles={profiles.filter((p) => p.status === "active")} settings={settings} onClose={() => setStarting(false)}
        onStarted={(id) => { setStarting(false); setOpen(id); }} />}
      {editingSettings && <SettingsDialog base={base} settings={settings} onClose={() => setEditingSettings(false)} onSaved={(s) => { setSettings(s); setEditingSettings(false); }} />}
    </div>
  );
}

function Notice({ canManage, onAccept }: { canManage: boolean; onAccept: () => void }) {
  return (
    <Card title="Before your first run" description="Shown once for the workspace.">
      <div className="space-y-3 text-[13px] leading-relaxed">
        <p>The lead finder reads public company websites and keeps the email addresses those sites publish. Some of those belong to people, including people in the EU and UK.</p>
        <p>Under the GDPR and UK GDPR you can contact business people at their work address on the basis of legitimate interest when what you offer is relevant to their job. You need to tell them where you got their details, let them opt out in every message, and stop when they do. Outflare adds the opt-out and keeps your suppression list, but whether your outreach is relevant is yours to judge.</p>
        {canManage ? <Button onClick={onAccept}>I understand</Button> : <p className="text-muted-foreground">An admin or operator accepts this once for the workspace.</p>}
      </div>
    </Card>
  );
}

function StartDialog({ base, profiles, settings, onClose, onStarted }: { base: string; profiles: CustomerProfile[]; settings: Settings; onClose: () => void; onStarted: (id: string) => void }) {
  const [profile, setProfile] = useState(profiles[0]?.id ?? "");
  const [target, setTarget] = useState(25);
  const [review, setReview] = useState(settings.review);
  const [estimate, setEstimate] = useState<Estimate | null>(null);
  const [qualifier, setQualifier] = useState<Qualifier | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    if (target < 1 || target > 1000) return setEstimate(null);
    call<Estimate>("POST", `${base}/estimate`, { target }).then(({ data }) => setEstimate(data ?? null));
  }, [base, target]);
  useEffect(() => {
    if (profile) call<Qualifier>("GET", `${base}/qualifiers?profile_id=${profile}`).then(({ data }) => setQualifier(data ?? null));
  }, [base, profile]);

  async function start() {
    setBusy(true);
    setError("");
    if (qualifier) {
      const q = await call<Qualifier>("PUT", `${base}/qualifiers`, { profile_id: profile, accept: qualifier.accept, reject: qualifier.reject });
      if (q.error) {
        setBusy(false);
        return setError(errorMessage(q.error));
      }
    }
    const { data, error } = await call<Run>("POST", `${base}/runs`, { profile_id: profile, target, review });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    toast("Finding companies. This page updates as it goes.");
    onStarted(data!.id);
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Find companies</DialogTitle>
          <DialogDescription>It stops once it has found this many that fit, or at its limits.</DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          <label className="block space-y-1 text-[13px]">
            <span className="font-medium">Ideal customer</span>
            <select className={`${field} w-full`} value={profile} onChange={(e) => setProfile(e.target.value)}>
              {profiles.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
            </select>
          </label>
          <label className="block space-y-1 text-[13px]">
            <span className="font-medium">Companies to find</span>
            <input type="number" min={1} max={1000} className={`${field} w-32`} value={target} onChange={(e) => setTarget(Number(e.target.value))} />
          </label>
          {qualifier && (
            <>
              <TagInput label="Accept companies that" placeholder="Sell software to finance teams" values={qualifier.accept} onChange={(v) => setQualifier({ ...qualifier, accept: v })} />
              <TagInput label="Reject companies that" placeholder="Are agencies or resellers" values={qualifier.reject} onChange={(v) => setQualifier({ ...qualifier, reject: v })} />
              {qualifier.version > 0 && <p className="text-[12px] text-muted-foreground">Criteria version {qualifier.version}. Changing them saves a new version.</p>}
            </>
          )}
          <label className="flex cursor-pointer items-start justify-between gap-3 text-[13px]">
            <span>
              <span className="block">Review before adding</span>
              <span className="block text-[12px] text-muted-foreground">Off adds each company that fits as soon as it is judged.</span>
            </span>
            <Switch checked={review} onCheckedChange={setReview} />
          </label>
          {estimate && (
            <p className="rounded-md bg-muted px-3 py-2 text-[12px] text-muted-foreground">
              At most {plural(estimate.searches, "search", "searches")} and {plural(estimate.pages, "page")} on Geonode (about {usd(estimate.provider_usd)}), and up to {usd(estimate.max_ai_cents / 100)} of AI: about {usd(estimate.per_company_usd)} a company at most. It usually spends less.
            </p>
          )}
          <ErrorText>{error}</ErrorText>
          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={onClose}>Cancel</Button>
            <Button onClick={start} disabled={busy || !profile || target < 1}>Start</Button>
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
    const { data, error } = await call<Settings>("PATCH", `${base}/settings`, { respect_robots: s.respect_robots, review: s.review, share_index: s.share_index });
    if (error) return setError(errorMessage(error));
    onSaved(data!);
  }
  const row = (key: "respect_robots" | "review" | "share_index", title: string, body: string) => (
    <label className="flex cursor-pointer items-start justify-between gap-3 text-[13px]">
      <span>
        <span className="block">{title}</span>
        <span className="block text-[12px] text-muted-foreground">{body}</span>
      </span>
      <Switch checked={s[key]} onCheckedChange={(v) => setS({ ...s, [key]: v })} />
    </label>
  );
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Lead finder settings</DialogTitle>
        </DialogHeader>
        <div className="space-y-4">
          {row("review", "Review before adding", "Runs stop for you to pick which companies to add.")}
          {row("respect_robots", "Follow robots.txt", "Skip pages a site's robots.txt asks crawlers not to read. Off reads any public page; it never logs in.")}
          {row("share_index", "Share public company facts", "Facts read from company websites (never people or email addresses) go into a shared index, so a site is read once for everyone. Off neither adds to it nor uses it.")}
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

function RunView({ base, id, canManage, profiles, onBack, onConnectors }: { base: string; id: string; canManage: boolean; profiles: CustomerProfile[]; onBack: () => void; onConnectors: () => void }) {
  const [run, setRun] = useState<Run | null>(null);
  const [cands, setCands] = useState<Candidate[]>([]);
  const [show, setShow] = useState<"fit" | "reject" | "all">("fit");
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [resuming, setResuming] = useState(false);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    const [r, c] = await Promise.all([call<Run>("GET", `${base}/runs/${id}`), call<{ data: Candidate[] }>("GET", `${base}/runs/${id}/candidates`)]);
    if (r.error) setError(errorMessage(r.error));
    if (r.data) setRun(r.data);
    setCands(c.data?.data ?? []);
  }, [base, id]);
  useEffect(() => {
    load();
  }, [load]);
  // Refresh while it finds, and while picked companies are being added.
  const busy = cands.some(adding);
  useEffect(() => {
    if (run?.status !== "queued" && run?.status !== "running" && !busy) return;
    const t = setInterval(load, 3000);
    return () => clearInterval(t);
  }, [run?.status, busy, load]);
  const shown = useMemo(() => cands.filter((c) => show === "all" || (show === "fit" ? c.verdict === "accept" || c.verdict === "maybe" : c.verdict === "reject" || c.verdict === "unread")), [cands, show]);

  if (!run) return error ? <ErrorText>{error}</ErrorText> : <Loading />;
  const profile = profiles.find((p) => p.id === run.profile_id)?.name ?? "Deleted profile";
  const pending = cands.filter((c) => c.status === "pending" && !c.picked && (c.verdict === "accept" || c.verdict === "maybe"));

  async function act(kind: "add" | "dismiss", body: { ids?: string[]; verdicts?: string[] }) {
    const { data, error } = await call<{ adding?: number; added?: number; dismissed?: number }>("POST", `${base}/runs/${id}/candidates/${kind}`, body);
    if (error) return toast(errorMessage(error), "error");
    toast(kind === "add"
      ? data?.adding != null ? `Adding ${plural(data.adding, "company", "companies")}. Each one is checked against your opt-out list first.` : `Added ${plural(data?.added ?? 0, "company", "companies")} to your companies.`
      : `Dismissed ${plural(data?.dismissed ?? 0, "company", "companies")}.`);
    setPicked(new Set());
    load();
  }
  async function cancel() {
    const { error } = await call("POST", `${base}/runs/${id}/cancel`);
    if (error) return toast(errorMessage(error), "error");
    load();
  }

  const columns: ColumnDef<Candidate>[] = [
    ...(canManage ? [{
      id: "pick", header: "", enableSorting: false,
      cell: ({ row: { original: c } }: { row: { original: Candidate } }) => c.status === "pending" && !c.picked && c.verdict !== "unread" ? (
        <span onClick={(e) => e.stopPropagation()}>
          <Checkbox aria-label={`Pick ${c.name || c.domain}`} checked={picked.has(c.id)} onCheckedChange={() => setPicked((s) => { const n = new Set(s); if (n.has(c.id)) n.delete(c.id); else n.add(c.id); return n; })} />
        </span>
      ) : null,
    } as ColumnDef<Candidate>] : []),
    {
      id: "company", header: "Company", accessorFn: (c) => `${c.name} ${c.domain}`,
      cell: ({ row: { original: c } }) => (
        <span className="block min-w-0 max-w-md">
          <span className="block truncate font-medium">{c.name || c.domain} <a className="font-normal text-muted-foreground hover:underline" href={`https://${c.domain}`} target="_blank" rel="noreferrer noopener">{c.domain}</a></span>
          {typeof c.facts.summary === "string" && c.facts.summary && <span className="block truncate text-[12px] text-muted-foreground">{c.facts.summary}</span>}
        </span>
      ),
    },
    { id: "verdict", header: "Fit", accessorFn: (c) => c.score ?? -1, cell: ({ row: { original: c } }) => <Badge tone={verdictTone[c.verdict]} title={c.reasons.join("\n")}>{c.verdict === "unread" ? "Not read" : `${c.verdict === "accept" ? "Fits" : c.verdict === "maybe" ? "Maybe" : "No"}${c.score != null ? ` · ${c.score}` : ""}`}</Badge> },
    { id: "why", header: "Why", accessorFn: (c) => c.reasons.join(" "), cell: ({ row: { original: c } }) => <span className="block max-w-sm truncate text-[12px] text-muted-foreground" title={c.reasons.join("\n")}>{c.reasons[0] ?? ""}</span> },
    { id: "emails", header: "Emails", accessorFn: (c) => c.emails.length, cell: ({ row: { original: c } }) => <span title={c.emails.join("\n")}>{c.emails.length || ""}</span> },
    {
      id: "status", header: "", accessorFn: (c) => (c.blocked ? "blocked" : adding(c) ? "adding" : c.status),
      cell: ({ row: { original: c } }) => {
        const warnings = (c.warnings ?? []).map(warningLabel).join("\n");
        if (c.status === "added") return <span className="inline-flex gap-1"><Badge tone="green">Added</Badge>{warnings && <Badge tone="amber" title={warnings}>{(c.warnings ?? []).length === 1 ? warningLabel(c.warnings![0]) : "Check"}</Badge>}</span>;
        if (c.blocked) return <Badge tone="red" title={warnings}>Not added: {codeLabel[c.blocked] ?? c.blocked}</Badge>;
        if (adding(c)) return <Badge tone="blue">Adding</Badge>;
        return c.status === "dismissed" ? <Badge>Dismissed</Badge> : null;
      },
    },
  ];

  const finding = run.status === "queued" || run.status === "running";
  const isPaused = paused(run);
  const connector = run.error_code?.startsWith("connector_") ?? false;
  return (
    <div className="space-y-6">
      <PageHeader
        back={{ label: "Find companies", onClick: onBack }}
        title={<span className="inline-flex items-center gap-2">{profile} {runBadge(run)}</span>}
        description={isPaused ? undefined : finding ? "Finding companies. This page updates as it goes." : run.error ?? undefined}
        action={canManage && (finding || run.status === "review") && <Button variant="ghost" onClick={cancel}>{finding ? "Stop" : "Close review"}</Button>}
      />
      <ErrorText>{error}</ErrorText>
      {isPaused && (
        <Card title="Paused" description="The companies found so far can still be added or dismissed.">
          <div className="space-y-3 text-[13px]">
            <p>{run.error ?? run.pipeline?.pause_reason ?? "The run paused."}</p>
            {canManage && (
              <div className="flex flex-wrap gap-2">
                {connector && <Button variant="ghost" onClick={onConnectors}>Open Connectors</Button>}
                <Button onClick={() => setResuming(true)}>Resume</Button>
              </div>
            )}
          </div>
        </Card>
      )}
      {run.status === "failed" && run.error_code === "connector_needed" && <Button onClick={onConnectors}>Open Connectors</Button>}
      <Stats cols={4}>
        <Stat label="Fit" value={`${run.accepted + run.maybe} of ${run.target}`} sub={`${run.accepted} fit, ${run.maybe} maybe`} />
        <Stat label="Read" value={run.found} sub={`${plural(run.rejected, "rejected", "rejected")}`} />
        <Stat label="Geonode" value={run.provider_cents ? usd(run.provider_cents / 100) : `${run.search_tokens + run.page_tokens} tokens`} sub={`${plural(run.searches, "search", "searches")}, ${plural(run.pages, "page")}`} />
        <Stat label="AI" value={usd(run.ai_cents / 100)} sub={run.accepted + run.maybe > 0 ? `${usd(run.ai_cents / 100 / (run.accepted + run.maybe))} a company that fits` : undefined} />
      </Stats>
      {run.pipeline && run.pipeline.stages.length > 0 && <Steps pipeline={run.pipeline} />}
      {cands.length === 0 ? (
        <Empty title={isPaused ? "Nothing found yet" : finding ? "Searching" : "No companies found"}>{isPaused ? "Companies show up here once the run carries on." : finding ? "Companies show up here as they are read." : "Try a broader profile or looser criteria."}</Empty>
      ) : (
        <DataTable columns={columns} data={shown} rowId={(c) => c.id} searchPlaceholder="Search companies"
          toolbar={
            <div className="flex flex-wrap items-center gap-2">
              <select aria-label="Show" className={field} value={show} onChange={(e) => setShow(e.target.value as typeof show)}>
                <option value="fit">Fits and maybes</option>
                <option value="reject">Not a fit</option>
                <option value="all">All</option>
              </select>
              {canManage && picked.size > 0 && (
                <>
                  <Button onClick={() => act("add", { ids: [...picked] })}>Add {picked.size}</Button>
                  <Button variant="ghost" onClick={() => act("dismiss", { ids: [...picked] })}>Dismiss {picked.size}</Button>
                </>
              )}
              {canManage && picked.size === 0 && pending.some((c) => c.verdict === "accept") && <Button onClick={() => act("add", { verdicts: ["accept"] })}>Add all that fit</Button>}
            </div>
          }
          mobile={(c) => (
            <span className="block min-w-0">
              <span className="block truncate text-[13px] font-medium">{c.name || c.domain}</span>
              <span className="block truncate text-[12px] text-muted-foreground">{c.verdict === "unread" ? "Not read" : c.verdict} · {c.reasons[0] ?? c.domain}</span>
            </span>
          )}
        />
      )}
      {run.queries.length > 0 && (
        <details className="text-[12px] text-muted-foreground">
          <summary className="cursor-pointer">{plural(run.queries.length, "search", "searches")} planned</summary>
          <ul className="mt-2 list-disc space-y-0.5 pl-5">{run.queries.map((q) => <li key={q}>{q}</li>)}</ul>
        </details>
      )}
      {resuming && <ResumeDialog base={base} run={run} onClose={() => setResuming(false)} onResumed={() => { setResuming(false); load(); }} />}
    </div>
  );
}

// Steps shows where the run's companies are: how many each step has
// waiting and in progress, passed on, set aside (with why) and failed, and
// how long the oldest one has waited, so a slow step stands out.
function Steps({ pipeline }: { pipeline: Pipeline }) {
  const byId = Object.fromEntries(pipeline.stages.map((s) => [s.stage, s]));
  const known = stages.filter((s) => byId[s.id]);
  const extra = pipeline.stages.filter((s) => !stages.some((k) => k.id === s.stage)).map((s) => ({ id: s.stage, label: s.stage, what: "" }));
  const slowest = Math.max(0, ...pipeline.stages.map((s) => s.oldest_waiting_seconds));
  return (
    <Card title="Progress by step" description="Each search and each company moves through these steps one at a time.">
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
              const notes = Object.entries(s.errors).sort((a, b) => b[1] - a[1]).slice(0, 3).map(([c, n]) => `${codeLabel[c] ?? c} ${n}`);
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
  const input = (key: keyof typeof limits, label: string, min: number, max: number) => (
    <label className="block space-y-1 text-[13px]">
      <span className={`block ${key === raise?.field ? "font-medium" : ""}`}>{label}</span>
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
          <p className="text-[12px] text-muted-foreground">Limits can only go up. Used so far: {plural(run.pages, "page")}, {usd(run.ai_cents / 100)} of AI, {plural(run.searches, "search", "searches")}.</p>
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
