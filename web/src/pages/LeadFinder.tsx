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
import { TagInput } from "./Projects";

type Props = { ws: string; canManage: boolean; onConnectors: () => void; onProfiles: () => void };

type Settings = { respect_robots: boolean; review: boolean; share_index: boolean; eu_notice_accepted_at: string | null };
type Qualifier = { id: string | null; profile_id: string | null; version: number; accept: string[]; reject: string[] };
type Estimate = { searches: number; pages: number; max_ai_cents: number; provider_usd: number; per_company_usd: number };
type Run = {
  id: string; profile_id: string | null; status: "queued" | "running" | "review" | "done" | "failed" | "cancelled"; target: number;
  max_searches: number; max_pages: number; max_ai_cents: number; review: boolean; queries: string[]; searches: number; pages: number;
  found: number; accepted: number; maybe: number; rejected: number; added: number; search_tokens: number; page_tokens: number;
  provider_cents: number; ai_cents: number; error_code: string | null; error: string | null; created_at: string; finished_at: string | null;
};
type Candidate = {
  id: string; domain: string; name: string; facts: Record<string, unknown>; emails: string[]; verdict: "accept" | "maybe" | "reject" | "unread";
  score: number | null; reasons: string[]; query: string; status: "pending" | "added" | "dismissed"; record_id: string | null; from_index: boolean;
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
const usd = (n: number) => `$${n < 1 ? n.toFixed(3) : n.toFixed(2)}`;

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
    { id: "status", header: "Status", accessorFn: (r) => r.status, cell: ({ row: { original: r } }) => <Badge tone={statusTone[r.status]}>{statusLabel[r.status]}</Badge> },
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
              <span className="block truncate text-[12px] text-muted-foreground">{statusLabel[r.status]} · {r.accepted + r.maybe} of {r.target} fit · {ago(r.created_at)}</span>
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
              <span className="block text-[12px] text-muted-foreground">Off adds the companies that fit as soon as the run ends.</span>
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
  useEffect(() => {
    if (run?.status !== "queued" && run?.status !== "running") return;
    const t = setInterval(load, 3000);
    return () => clearInterval(t);
  }, [run?.status, load]);
  const shown = useMemo(() => cands.filter((c) => show === "all" || (show === "fit" ? c.verdict === "accept" || c.verdict === "maybe" : c.verdict === "reject" || c.verdict === "unread")), [cands, show]);

  if (!run) return error ? <ErrorText>{error}</ErrorText> : <Loading />;
  const profile = profiles.find((p) => p.id === run.profile_id)?.name ?? "Deleted profile";
  const pending = cands.filter((c) => c.status === "pending" && (c.verdict === "accept" || c.verdict === "maybe"));

  async function act(kind: "add" | "dismiss", body: { ids?: string[]; verdicts?: string[] }) {
    const { data, error } = await call<{ added?: number; dismissed?: number }>("POST", `${base}/runs/${id}/candidates/${kind}`, body);
    if (error) return toast(errorMessage(error), "error");
    toast(kind === "add" ? `Added ${plural(data?.added ?? 0, "company", "companies")} to your companies.` : `Dismissed ${plural(data?.dismissed ?? 0, "company", "companies")}.`);
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
      cell: ({ row: { original: c } }: { row: { original: Candidate } }) => c.status === "pending" && c.verdict !== "unread" ? (
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
    { id: "status", header: "", accessorFn: (c) => c.status, cell: ({ row: { original: c } }) => c.status === "added" ? <Badge tone="green">Added</Badge> : c.status === "dismissed" ? <Badge>Dismissed</Badge> : null },
  ];

  const finding = run.status === "queued" || run.status === "running";
  return (
    <div className="space-y-6">
      <PageHeader
        back={{ label: "Find companies", onClick: onBack }}
        title={profile}
        description={finding ? "Finding companies. This page updates as it goes." : run.error ?? undefined}
        action={canManage && (finding || run.status === "review") && <Button variant="ghost" onClick={cancel}>{finding ? "Stop" : "Close review"}</Button>}
      />
      <ErrorText>{error}</ErrorText>
      {run.status === "failed" && run.error_code === "connector_needed" && <Button onClick={onConnectors}>Open Connectors</Button>}
      <Stats cols={4}>
        <Stat label="Fit" value={`${run.accepted + run.maybe} of ${run.target}`} sub={`${run.accepted} fit, ${run.maybe} maybe`} />
        <Stat label="Read" value={run.found} sub={`${plural(run.rejected, "rejected", "rejected")}`} />
        <Stat label="Geonode" value={run.provider_cents ? usd(run.provider_cents / 100) : `${run.search_tokens + run.page_tokens} tokens`} sub={`${plural(run.searches, "search", "searches")}, ${plural(run.pages, "page")}`} />
        <Stat label="AI" value={usd(run.ai_cents / 100)} sub={run.accepted + run.maybe > 0 ? `${usd(run.ai_cents / 100 / (run.accepted + run.maybe))} a company that fits` : undefined} />
      </Stats>
      {cands.length === 0 ? (
        <Empty title={finding ? "Searching" : "No companies found"}>{finding ? "Companies show up here as they are read." : "Try a broader profile or looser criteria."}</Empty>
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
    </div>
  );
}
