import { useCallback, useEffect, useState, type FormEvent, type ReactNode } from "react";
import type { Action, ActionGrant, APIToken } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { ask, Badge, Button, confirmAction, Empty, ErrorText, field, Input, Loading, PageHeader, toast } from "../ui";
import { ago, plural, stamp } from "../format";
import { track } from "@/lib/analytics";
import { useBrand } from "@/lib/brand";
import { actionsAPI } from "@/lib/scoped";
import { BrandBadge } from "@/components/brand-badge";
import { DataTable } from "@/components/data-table";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { AgentPageTabsContext, type AgentPageTab } from "@/lib/brandAgent";
import { ChangePreview } from "@/components/change-preview";
import { AgentRunDetail, AgentRuns } from "./AgentRuns";
import type { Tab } from "../App";

// Agents: what agent tokens on the action layer changed, what waits for a
// person, what each agent may do on its own (docs/actions.md), and the runs
// of Outflare's own agent (AgentRuns.tsx). A person approves held changes
// one at a time, in a batch, or as a pattern to let changes like it through
// from then on.

type Status = Action["status"];
const statusLabel: Record<Status, string> = { held: "Waiting", running: "Running", executed: "Done", failed: "Failed", refused: "Refused", rejected: "Rejected", expired: "Expired", cancelled: "Cancelled" };
const statusTone: Record<Status, "amber" | "blue" | "green" | "red" | "grey"> = { held: "amber", running: "blue", executed: "green", failed: "red", refused: "red", rejected: "grey", expired: "grey", cancelled: "grey" };
const ruleLabel: Record<string, string> = { no_grant: "No rule covers it", grant: "By a rule", daily_limit: "Daily limit reached", always_person: "Always needs a person" };
// Why a change waits, in words, when it is more than "nobody has allowed
// this kind of change yet" (the usual case, which needs no badge).
const waitLabel: Record<string, string> = { daily_limit: "Over today's limit", always_person: "Always needs a person" };
// The fields that say what a change is about, first match wins.
const targetKeys = ["url", "page_url", "target", "website", "domain", "email", "name", "title"];
const isID = (k: string) => k === "id" || k.endsWith("_id") || k.endsWith("_ids");
const modeLabel = { run: "Runs", hold: "Waits for a person", refuse: "Refused" } as const;
const modeTone = { run: "green", hold: "amber", refuse: "red" } as const;

// The path as grants see it: after /v1/workspaces/{ws}/.
const shortPath = (p: string) => p.replace(/^\/v1\/workspaces\/[^/]+\//, "").replace(/^\/v1\//, "");
const daysLeft = (iso: string) => Math.max(0, Math.ceil((new Date(iso).getTime() - Date.now()) / 86400000));

// A view is one of these, or a tab the brand agent's page adds.
type View = string;

// brandAgent is the brand's own agent, when the server has one (an
// extension page, such as Outflare Cloud's): it opens first, and the
// rest of the page is what every agent changed and may do.
export function Agents({ ws, canApprove, canManage, canTokens, canRun, open, setOpen, go, brandAgent }: { ws: string; canApprove: boolean; canManage: boolean; canTokens: boolean; canRun: boolean; open?: string; setOpen: (id?: string) => void; go: (t: Tab) => void; brandAgent?: ReactNode }) {
  const [view, setView] = useState<View>(open ? "runs" : brandAgent ? "agent" : "waiting");
  const [waiting, setWaiting] = useState<number | null>(null);
  const [tokens, setTokens] = useState<APIToken[] | null>(null);

  useEffect(() => {
    if (!canTokens) return;
    api.GET("/v1/workspaces/{ws}/api-tokens", { params: { path: { ws } } }).then(({ data }) => setTokens(data?.data ?? []));
  }, [ws, canTokens]);

  const agents = tokens?.filter((t) => t.action_gate) ?? null;
  // The tab says how many changes wait before Waiting for you opens.
  const brandId = useBrand(ws).brand?.id;
  const hasAgent = !!brandAgent;
  useEffect(() => {
    if (!hasAgent || view === "waiting") return;
    actionsAPI(ws, brandId).list({ status: "held", limit: 200 }).then(({ data }) => setWaiting(data?.data.length ?? 0));
  }, [ws, hasAgent, view, brandId]);
  const [extra, setExtra] = useState<AgentPageTab[]>([]);
  const pick = useCallback((v: View) => { setView(v); track("ui.agents_view_opened", { view: v }); }, []);
  if (open) return <AgentRunDetail key={open} ws={ws} id={open} canRun={canRun} canApprove={canApprove} onBack={() => { setOpen(undefined); setView("runs"); }} />;
  // With a brand agent, the page is its chat, and every other part of it
  // (the agent's own views, approvals, history, what agents may do) is a
  // tab in one row at the top.
  if (brandAgent) {
    const agentView = view === "agent" || extra.some((t) => t.value === view);
    // History and Settings each gather several views; a row of pills under
    // the tabs picks one.
    const groups: Record<"history" | "settings", { value: string; label: string }[]> = {
      history: [...extra.filter((t) => t.group === "history"), { value: "all", label: "All changes" }, { value: "runs", label: "Playbook runs" }],
      settings: [...extra.filter((t) => t.group === "settings"), { value: "grants", label: "Rules" }],
    };
    const groupOf = (v: string) => (groups.history.some((t) => t.value === v) ? "history" : groups.settings.some((t) => t.value === v) ? "settings" : v);
    const top = groupOf(view);
    const pickTop = (v: string) => pick(v === "history" || v === "settings" ? groups[v][0].value : v);
    return (
      <div className="space-y-4">
        <PageHeader
          title="Agent"
          description="Ask for anything. It works on your tasks and asks before changes you haven't allowed."
          action={canTokens ? <Button variant="ghost" onClick={() => go("tokens")}>API tokens</Button> : undefined}
        />
        <Tabs value={top} onValueChange={pickTop}>
          <TabsList variant="line" className="max-w-full justify-start overflow-x-auto">
            <TabsTrigger value="agent">Chat</TabsTrigger>
            {extra.filter((t) => !t.group).map((t) => <TabsTrigger key={t.value} value={t.value}>{t.label}{t.count ? ` (${t.count})` : ""}</TabsTrigger>)}
            <TabsTrigger value="waiting">Waiting for you{waiting ? ` (${waiting})` : ""}</TabsTrigger>
            <TabsTrigger value="history">History</TabsTrigger>
            <TabsTrigger value="settings">Settings</TabsTrigger>
          </TabsList>
        </Tabs>
        {(top === "history" || top === "settings") && (
          <div className="flex flex-wrap gap-1.5" role="group" aria-label={top === "history" ? "History" : "Settings"}>
            {groups[top].map((t) => (
              <button key={t.value} type="button" aria-pressed={view === t.value} onClick={() => pick(t.value)}
                className={`rounded-full border px-3 py-1 text-[13px] ${view === t.value ? "border-foreground bg-foreground text-background" : "text-muted-foreground hover:bg-accent hover:text-foreground"}`}>
                {t.label}
              </button>
            ))}
          </div>
        )}
        <div hidden={!agentView}>
          <AgentPageTabsContext.Provider value={{ view: agentView ? view : "agent", go: pick, setTabs: setExtra }}>{brandAgent}</AgentPageTabsContext.Provider>
        </div>
        {view === "waiting" && <Waiting ws={ws} canApprove={canApprove} canManage={canManage} onCount={setWaiting} />}
        {view === "runs" && <AgentRuns ws={ws} onOpen={setOpen} />}
        {view === "all" && <Everything ws={ws} tokens={agents} />}
        {view === "grants" && <Grants ws={ws} canManage={canManage} tokens={agents} />}
      </div>
    );
  }
  return (
    <div className="space-y-6">
      <PageHeader
        title="Agents"
        description="What your agents changed, what waits for you, the playbooks Outflare's own agent ran, and the rules for what each agent may do on its own. An agent is an API token whose changes wait for your approval."
        action={canTokens ? <Button variant="ghost" onClick={() => go("tokens")}>API tokens</Button> : undefined}
      />
      {agents && agents.length === 0 && (
        <div className="flex flex-wrap items-center gap-3 rounded-lg border bg-muted/30 px-4 py-3 text-[13px]">
          <span className="min-w-0 flex-1">No token is an agent yet. Turn on <span className="font-medium">It's an agent</span> on a token, and its changes show up here, waiting for you until a rule lets them through.</span>
          <Button variant="ghost" onClick={() => go("tokens")}>Open API tokens</Button>
        </div>
      )}
      <Tabs value={view} onValueChange={pick}>
        <TabsList>
          <TabsTrigger value="waiting">Waiting for you{waiting ? ` (${waiting})` : ""}</TabsTrigger>
          <TabsTrigger value="runs">Playbook runs</TabsTrigger>
          <TabsTrigger value="all">All changes</TabsTrigger>
          <TabsTrigger value="grants">Rules</TabsTrigger>
        </TabsList>
        <TabsContent value="waiting"><Waiting ws={ws} canApprove={canApprove} canManage={canManage} onCount={setWaiting} /></TabsContent>
        <TabsContent value="runs"><AgentRuns ws={ws} onOpen={setOpen} /></TabsContent>
        <TabsContent value="all"><Everything ws={ws} tokens={agents} /></TabsContent>
        <TabsContent value="grants"><Grants ws={ws} canManage={canManage} tokens={agents} /></TabsContent>
      </Tabs>
    </div>
  );
}

// ---- Waiting for you ----

// team shows the team's changes, about no brand (workspace Settings);
// otherwise the picked brand's.
export function Waiting({ ws, canApprove, canManage, onCount, team }: { ws: string; canApprove: boolean; canManage: boolean; onCount: (n: number) => void; team?: boolean }) {
  const [list, setList] = useState<Action[] | null>(null);
  const [error, setError] = useState("");
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState(false);
  const pickedBrand = useBrand(ws).brand?.id;
  const brand = team ? undefined : pickedBrand;

  const load = useCallback(async () => {
    const { data, error } = await actionsAPI(ws, brand).list({ status: "held", limit: 200 });
    if (error) return setError(errorMessage(error));
    setError("");
    setList(data?.data ?? []);
    onCount(data?.data.length ?? 0);
    setPicked(new Set());
  }, [ws, brand, onCount]);
  useEffect(() => {
    load();
  }, [load]);

  async function decideBatch(approve: boolean) {
    const ids = [...picked];
    if (ids.length === 0) return;
    let note = "";
    if (approve) {
      if (!(await confirmAction(`Approve ${plural(ids.length, "change")}?`, <p>Each runs now as the agent that asked for it, through every check again.</p>, "Approve", false))) return;
    } else {
      const r = await ask({ title: `Reject ${plural(ids.length, "change")}?`, body: "The agents see your reason.", input: { label: "Reason", placeholder: "e.g. Not this week", optional: true }, choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Reject", value: "ok", variant: "danger-solid" }] });
      if (r?.choice !== "ok") return;
      note = r.value;
    }
    setBusy(true);
    const { data, error } = await actionsAPI(ws, brand).decide(approve ? { approve: ids, note } : { reject: ids, note });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    const failed = data?.data.filter((d) => d.error).length ?? 0;
    track("ui.action_decided", { decision: approve ? "approve" : "reject", how: "batch", count: ids.length, outcome: failed ? "failure" : "success" });
    toast(failed ? `${plural(ids.length - failed, "change")} ${approve ? "approved" : "rejected"}, ${failed} couldn't be: see each one.` : `${plural(ids.length, "change")} ${approve ? "approved" : "rejected"}.`, failed ? "error" : "ok");
    load();
  }

  if (error) return <ErrorText>{error}</ErrorText>;
  if (list === null) return <Loading />;
  if (list.length === 0) return <Empty title="Nothing waiting">Changes your agents can't make on their own show up here for you to approve.</Empty>;
  const all = picked.size === list.length;
  return (
    <div className="space-y-3">
      {canApprove && (
        <div className="flex flex-wrap items-center gap-2 text-[13px]">
          <label className="flex items-center gap-2">
            <input type="checkbox" checked={all} onChange={() => setPicked(all ? new Set() : new Set(list.map((a) => a.id)))} aria-label="Select every waiting change" />
            <span>{picked.size ? `${picked.size} selected` : "Select all"}</span>
          </label>
          {picked.size > 0 && (
            <>
              <Button disabled={busy} onClick={() => decideBatch(true)}>Approve {picked.size}</Button>
              <Button disabled={busy} variant="ghost" onClick={() => decideBatch(false)}>Reject {picked.size}</Button>
            </>
          )}
        </div>
      )}
      {list.map((a) => (
        <HeldAction key={a.id} ws={ws} a={a} canApprove={canApprove} canManage={canManage} picked={picked.has(a.id)} onPick={(on) => setPicked((p) => { const n = new Set(p); if (on) n.add(a.id); else n.delete(a.id); return n; })} onDone={load} />
      ))}
    </div>
  );
}

function resultMessage(a?: Action): string {
  const r = a?.result as { error?: { message?: string } } | undefined;
  return r?.error?.message ?? `status ${a?.result_status ?? "unknown"}`;
}

function HeldAction({ ws, a, canApprove, canManage, picked, onPick, onDone }: { ws: string; a: Action; canApprove: boolean; canManage: boolean; picked: boolean; onPick: (on: boolean) => void; onDone: () => void }) {
  const [busy, setBusy] = useState(false);
  const [remembering, setRemembering] = useState(false);
  const [fields, setFields] = useState<string[]>([]);
  const [limit, setLimit] = useState("");
  const body = a.body && typeof a.body === "object" && !Array.isArray(a.body) ? (a.body as Record<string, unknown>) : null;
  const scalar = body ? Object.entries(body).filter(([, v]) => v === null || ["string", "number", "boolean"].includes(typeof v)) : [];
  const targetKey = body ? targetKeys.find((k) => typeof body[k] === "string" && body[k]) : undefined;
  const [looked, setLooked] = useState("");
  const target = targetKey ? String(body![targetKey]) : looked;
  // A change to one thing (PATCH .../{id}) names it from the thing itself.
  useEffect(() => {
    if (targetKey || a.method === "POST" || !/\/[0-9a-f-]{36}$/.test(a.path)) return;
    let live = true;
    fetch(a.path, { credentials: "same-origin" })
      .then((r) => (r.ok ? r.json() : null))
      .then((v: Record<string, unknown> | null) => {
        const k = v && targetKeys.find((k) => typeof v[k] === "string" && v[k]);
        if (live && v && k) setLooked(String(v[k]));
      })
      .catch(() => {});
    return () => { live = false; };
  }, [a.method, a.path, targetKey]);
  const who = a.actor_name ?? "An agent";
  // An agent's reason leads with the yes/no question this card asks
  // ("Take X out of outreach? X is a competitor."); older ones don't.
  const qi = a.reason ? a.reason.indexOf("? ") : -1;
  const question = qi >= 0 ? a.reason!.slice(0, qi + 1) : a.reason?.endsWith("?") ? a.reason : "";
  const why = qi >= 0 ? a.reason!.slice(qi + 2).trim() : question ? "" : a.reason;

  async function decide(approve: boolean, remember?: { fields: string[]; daily_limit?: number }) {
    let note = "";
    if (!approve) {
      const r = await ask({ title: "Reject this change?", body: "The agent sees your reason.", input: { label: "Reason", placeholder: "e.g. Use a softer subject line", optional: true }, choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Reject", value: "ok", variant: "danger-solid" }] });
      if (r?.choice !== "ok") return;
      note = r.value;
    }
    setBusy(true);
    const acts = actionsAPI(ws, a.brand_id ?? undefined);
    const { data, error } = approve ? await acts.approve(a.id, { note, ...(remember ? { remember } : {}) }) : await acts.reject(a.id, { note });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    track("ui.action_decided", { decision: approve ? "approve" : "reject", how: remember ? "remember" : "single", count: 1 });
    if (approve) toast(data?.status === "executed" ? (remember ? "Approved and done. Changes like it run from now on." : "Approved and done.") : `Approved, but it failed: ${resultMessage(data)}`, data?.status === "executed" ? "ok" : "error");
    else toast("Rejected. The agent will see your reason.");
    onDone();
  }

  function rememberSubmit(e: FormEvent) {
    e.preventDefault();
    decide(true, { fields, ...(limit ? { daily_limit: Number(limit) } : {}) });
  }

  return (
    <div className={`rounded-lg border p-3 ${picked ? "border-brand/60 bg-brand/[0.04]" : ""}`}>
      <div className="flex items-start gap-3">
        {canApprove && <input type="checkbox" className="mt-1" checked={picked} onChange={(e) => onPick(e.target.checked)} aria-label={`Select ${a.summary}`} />}
        <div className="min-w-0 flex-1 text-[13px]">
          <div className="text-[12px] text-muted-foreground">{question ? `${who} asks` : `${who} asks to`}</div>
          <div className="flex flex-wrap items-center gap-1.5">
            <span className="text-[15px] font-medium">{question || a.summary}</span>
            {a.decided_by_rule && waitLabel[a.decided_by_rule] && <Badge tone="amber">{waitLabel[a.decided_by_rule]}</Badge>}
            <BrandBadge id={a.brand_id} />
          </div>
          {target && (
            <p className="mt-1 min-w-0 break-words">
              <span className="text-muted-foreground">On </span>
              {/^https?:\/\//.test(target) ? <a href={target} target="_blank" rel="noreferrer" className="font-medium underline underline-offset-2">{target}</a> : <span className="font-medium">{target}</span>}
            </p>
          )}
          {why && <p className="mt-1"><span className="font-medium">Why: </span>{why}</p>}
          {a.hold_reason && a.decided_by_rule && waitLabel[a.decided_by_rule] && <p className="mt-1 text-[12px] text-muted-foreground">{a.hold_reason}</p>}
          <p className="mt-1 text-[12px] text-muted-foreground">
            {question && `${a.summary} · `}Asked {ago(a.created_at)}
            {a.expires_at && ` · cancelled in ${plural(daysLeft(a.expires_at), "day")} if nobody answers`}
          </p>
        </div>
        {canApprove && (
          <div className="flex flex-wrap justify-end gap-2">
            <Button disabled={busy} onClick={() => decide(true)}>Approve</Button>
            <Button disabled={busy} variant="ghost" onClick={() => decide(false)}>Reject</Button>
          </div>
        )}
      </div>
      {a.body !== undefined && a.body !== null && (
        <>
          <ChangePreview body={a.body} hide={(k) => isID(k) || k === targetKey} />
          <details className="mt-2">
            <summary className="cursor-pointer text-[12px] text-muted-foreground">Show the exact request</summary>
            <p className="mt-1 font-mono text-[11px] text-muted-foreground">{a.method} {shortPath(a.path)}</p>
            <pre className="mt-1 max-h-72 overflow-auto rounded border bg-muted/40 p-2 text-[11px]"><code>{JSON.stringify(a.body, null, 2)}</code></pre>
          </details>
        </>
      )}
      {canManage && a.decided_by_rule !== "always_person" && (
        <details className="mt-2" open={remembering} onToggle={(e) => setRemembering((e.target as HTMLDetailsElement).open)}>
          <summary className="cursor-pointer text-[12px] text-muted-foreground">Always allow this kind of change</summary>
          <form onSubmit={rememberSubmit} className="mt-2 space-y-3 rounded-md border bg-muted/30 p-3 text-[13px]">
            <p className="text-muted-foreground">From now on, {who} may “{a.summary.toLowerCase()}” without asking{scalar.length ? ", when the ticked fields match:" : "."}</p>
            {scalar.length > 0 && (
              <div className="grid gap-1 sm:grid-cols-2">
                {scalar.map(([k, v]) => (
                  <label key={k} className="flex items-start gap-2">
                    <input type="checkbox" className="mt-0.5" checked={fields.includes(k)} onChange={(e) => setFields(e.target.checked ? [...fields, k] : fields.filter((f) => f !== k))} />
                    <span className="min-w-0 break-words"><span className="font-mono text-[12px]">{k}</span> is <span className="font-medium">{String(v)}</span></span>
                  </label>
                ))}
              </div>
            )}
            <div className="flex flex-wrap items-end gap-3">
              <Input label="Most a day" type="number" min={1} placeholder="No limit" hint="The rest wait for a person." value={limit} onChange={(e) => setLimit(e.target.value)} />
              <Button disabled={busy}>Approve and remember</Button>
            </div>
          </form>
        </details>
      )}
    </div>
  );
}

// ---- Everything ----

export function Everything({ ws, tokens, team }: { ws: string; tokens: APIToken[] | null; team?: boolean }) {
  const [list, setList] = useState<Action[] | null>(null);
  const [cursor, setCursor] = useState<string | null>(null);
  const [status, setStatus] = useState("");
  const [token, setToken] = useState("");
  const [open, setOpen] = useState<Action | null>(null);
  const [error, setError] = useState("");
  const picked = useBrand(ws).brand?.id;
  const brand = team ? undefined : picked;

  const load = useCallback(async (after?: string) => {
    const { data, error } = await actionsAPI(ws, brand).list({ limit: 50, ...(status ? { status } : {}), ...(token ? { token_id: token } : {}), ...(after ? { cursor: after } : {}) });
    if (error) return setError(errorMessage(error));
    setError("");
    setList((l) => (after && l ? [...l, ...(data?.data ?? [])] : data?.data ?? []));
    setCursor(data?.next_cursor ?? null);
  }, [ws, status, token, brand]);
  useEffect(() => {
    setList(null);
    load();
  }, [load]);

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap gap-2">
        <select className={field} value={status} onChange={(e) => setStatus(e.target.value)} aria-label="Status">
          <option value="">Any status</option>
          {(Object.keys(statusLabel) as Status[]).map((s) => <option key={s} value={s}>{statusLabel[s]}</option>)}
        </select>
        {tokens && tokens.length > 0 && (
          <select className={field} value={token} onChange={(e) => setToken(e.target.value)} aria-label="Agent">
            <option value="">Any agent</option>
            {tokens.map((t) => <option key={t.id} value={t.id}>{t.name}</option>)}
          </select>
        )}
      </div>
      {error && <ErrorText>{error}</ErrorText>}
      {list === null && !error && <Loading />}
      {list && list.length === 0 && <Empty title="No changes yet">Every change an agent makes shows up here: run, held, refused, and what people decided.</Empty>}
      {list && list.length > 0 && (
        <DataTable
          data={list}
          rowId={(a) => a.id}
          columnsMenu={false}
          onRowClick={setOpen}
          mobile={(a) => (
            <span className="block">
              <span className="flex items-center gap-2"><span className="font-medium">{a.summary}</span> <Badge tone={statusTone[a.status]}>{statusLabel[a.status]}</Badge></span>
              <span className="block text-[12px] text-muted-foreground">{a.actor_name ?? "agent"} · {ago(a.created_at)}</span>
            </span>
          )}
          columns={[
            { id: "change", header: "Change", accessorFn: (a) => a.summary, cell: ({ row: { original: a } }) => <span className="block min-w-0"><span className="font-medium">{a.summary}</span><span className="block font-mono text-[11px] text-muted-foreground">{a.method} {shortPath(a.path)}</span></span> },
            { id: "agent", header: "Agent", accessorFn: (a) => a.actor_name ?? "", cell: ({ row: { original: a } }) => <span className="whitespace-nowrap">{a.actor_name ?? <span className="text-muted-foreground">—</span>}</span> },
            { id: "status", header: "Status", accessorFn: (a) => a.status, cell: ({ row: { original: a } }) => <span className="flex flex-wrap items-center gap-1"><Badge tone={statusTone[a.status]}>{statusLabel[a.status]}</Badge>{a.decided_by_rule && a.status !== "executed" && <span className="text-[12px] text-muted-foreground">{ruleLabel[a.decided_by_rule]}</span>}</span> },
            { id: "decided", header: "Decided by", accessorFn: (a) => a.decided_by_name ?? "", cell: ({ row: { original: a } }) => <span className="whitespace-nowrap text-muted-foreground">{a.decided_by_name ? `${a.decided_by_name}${a.decided_at ? `, ${ago(a.decided_at)}` : ""}` : a.decided_by_rule === "grant" && (a.status === "executed" || a.status === "failed" || a.status === "refused") ? "A rule" : "—"}</span> },
            { id: "when", header: "When", accessorFn: (a) => a.created_at, cell: ({ row: { original: a } }) => <span className="whitespace-nowrap text-muted-foreground" title={stamp(a.created_at)}>{ago(a.created_at)}</span> },
          ]}
        />
      )}
      {cursor && <div className="flex justify-center"><Button variant="ghost" onClick={() => load(cursor)}>Show more</Button></div>}
      {open && <ActionDialog a={open} onClose={() => setOpen(null)} />}
    </div>
  );
}

function ActionDialog({ a, onClose }: { a: Action; onClose: () => void }) {
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle className="flex flex-wrap items-center gap-2">{a.summary} <Badge tone={statusTone[a.status]}>{statusLabel[a.status]}</Badge></DialogTitle>
          <DialogDescription>
            {a.method} {shortPath(a.path)} · {a.actor_name ?? "agent"} · {stamp(a.created_at)}
            {a.decided_by_rule && ` · ${ruleLabel[a.decided_by_rule]}`}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-3 text-[13px]">
          {a.reason && <p className="text-muted-foreground">“{a.reason}”</p>}
          {a.hold_reason && <p className="text-muted-foreground">{a.hold_reason}</p>}
          {a.decided_by_name && <p>{a.status === "rejected" ? "Rejected" : "Approved"} by {a.decided_by_name}{a.decided_at && `, ${ago(a.decided_at)}`}{a.decision_note && <>: “{a.decision_note}”</>}</p>}
          {a.result_status != null && <p className="text-muted-foreground">Answered {a.result_status}{a.status === "failed" && `: ${resultMessage(a)}`}</p>}
          {a.body !== undefined && a.body !== null && <ChangePreview body={a.body} />}
          {a.body !== undefined && a.body !== null && (
            <details>
              <summary className="cursor-pointer text-[12px] text-muted-foreground">Exact request</summary>
              <pre className="mt-1 max-h-72 overflow-auto rounded border bg-muted/40 p-2 text-[11px]"><code>{JSON.stringify(a.body, null, 2)}</code></pre>
            </details>
          )}
          {a.result !== undefined && a.result !== null && (
            <details>
              <summary className="cursor-pointer text-[12px] text-muted-foreground">Result</summary>
              <pre className="mt-1 max-h-72 overflow-auto rounded border bg-muted/40 p-2 text-[11px]"><code>{JSON.stringify(a.result, null, 2)}</code></pre>
            </details>
          )}
          <p className="font-mono text-[11px] text-muted-foreground">{a.body_hash ? `sha256 ${a.body_hash.slice(0, 16)}…` : "Body over 1 MiB: not kept"}{a.request_id && ` · request ${a.request_id}`}</p>
        </div>
      </DialogContent>
    </Dialog>
  );
}

// ---- Rules (grants in the API): what agents may do ----

function Grants({ ws, canManage, tokens }: { ws: string; canManage: boolean; tokens: APIToken[] | null }) {
  const [list, setList] = useState<ActionGrant[] | null>(null);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);
  const { byId } = useBrand(ws);

  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/action-grants", { params: { path: { ws } } });
    if (error) return setError(errorMessage(error));
    setError("");
    setList(data?.data ?? []);
  }, [ws]);
  useEffect(() => {
    load();
  }, [load]);

  async function revoke(g: ActionGrant) {
    if (!(await confirmAction("Remove this rule?", <p>From its next change, {g.token_name ?? "the agent"} waits for a person again where this rule let it through.</p>, "Remove"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/action-grants/{id}", { params: { path: { ws, id: g.id } } });
    if (error) return toast(errorMessage(error), "error");
    track("ui.action_grant_revoked", { mode: g.mode });
    toast("Rule removed.");
    load();
  }

  if (error) return <ErrorText>{error}</ErrorText>;
  if (list === null) return <Loading />;
  const byToken = new Map<string, ActionGrant[]>();
  for (const g of list) byToken.set(g.token_id, [...(byToken.get(g.token_id) ?? []), g]);
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3 text-[13px] text-muted-foreground">
        <p className="min-w-0 flex-1">A rule says what happens to an agent's changes that match it: run on their own, wait for a person, or be refused. The most specific rule decides; with none, the change waits. Some changes always wait for a person, whatever the rules say.</p>
        {canManage && <Button onClick={() => setAdding(true)}>New rule</Button>}
      </div>
      {list.length === 0 && <Empty title="No rules yet">Every change your agents make waits for a person. Approve one with “approve changes like this from now on”, or add a rule here.</Empty>}
      {[...byToken.entries()].map(([tokenId, grants]) => (
        <section key={tokenId} className="space-y-2">
          <h3 className="font-medium">{grants[0].token_name ?? "An agent"} <span className="text-[12px] font-normal text-muted-foreground">· {plural(grants.length, "rule")}</span></h3>
          <div className="divide-y rounded-lg border">
            {grants.map((g) => (
              <div key={g.id} className="flex flex-wrap items-center gap-3 px-3 py-2 text-[13px]">
                <Badge tone={modeTone[g.mode]}>{modeLabel[g.mode]}</Badge>
                <span className="min-w-0 flex-1">
                  <span className="font-mono text-[12px]">{g.method ?? "any"} {g.path}</span>
                  {g.match && <span className="block text-[12px] text-muted-foreground">when {Object.entries(g.match).map(([k, v]) => `${k} is ${String(v)}`).join(", ")}</span>}
                  {(g.daily_limit || g.note || byId(g.brand_id)) && (
                    <span className="block text-[12px] text-muted-foreground">
                      {g.daily_limit ? `at most ${plural(g.daily_limit, "change")} a day` : ""}{g.daily_limit && g.note ? " · " : ""}{g.note}{byId(g.brand_id) ? ` · only ${byId(g.brand_id)!.name}` : ""}
                    </span>
                  )}
                </span>
                <span className="whitespace-nowrap text-[12px] text-muted-foreground" title={stamp(g.created_at)}>{ago(g.created_at)}</span>
                {canManage && <Button variant="ghost" onClick={() => revoke(g)}>Remove</Button>}
              </div>
            ))}
          </div>
        </section>
      ))}
      {adding && <GrantDialog ws={ws} tokens={tokens} onClose={() => setAdding(false)} onDone={() => { setAdding(false); load(); }} />}
    </div>
  );
}

function GrantDialog({ ws, tokens, onClose, onDone }: { ws: string; tokens: APIToken[] | null; onClose: () => void; onDone: () => void }) {
  const [token, setToken] = useState(tokens?.[0]?.id ?? "");
  const [method, setMethod] = useState("");
  const [path, setPath] = useState("");
  const [mode, setMode] = useState<"run" | "hold" | "refuse">("run");
  const [limit, setLimit] = useState("");
  const [note, setNote] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const { error } = await api.POST("/v1/workspaces/{ws}/action-grants", {
      params: { path: { ws } },
      body: { token_id: token, method: (method || null) as "POST" | "PUT" | "PATCH" | "DELETE" | null, path: path.trim(), mode, daily_limit: mode === "run" && limit ? Number(limit) : null, note: note.trim() },
    });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    track("ui.action_grant_created", { mode, has_daily_limit: !!limit, template: false });
    toast("Rule added. It applies to the agent's next change.");
    onDone();
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>New rule</DialogTitle>
          <DialogDescription>What happens to this agent's changes that match the path. The path is the part after <span className="font-mono">/v1/workspaces/…/</span>, with <span className="font-mono">*</span> for one segment and a trailing <span className="font-mono">**</span> for the rest.</DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="space-y-4">
          {tokens && tokens.length > 0 ? (
            <label className="block">
              <span className="mb-1.5 block font-medium">Agent</span>
              <select className={`${field} w-full`} value={token} onChange={(e) => setToken(e.target.value)}>
                {tokens.map((t) => <option key={t.id} value={t.id}>{t.name}</option>)}
              </select>
            </label>
          ) : (
            <Input label="Agent token id" value={token} onChange={(e) => setToken(e.target.value)} hint="The id of a token that is marked as an agent." required />
          )}
          <div className="grid gap-4 sm:grid-cols-[8rem_1fr]">
            <label className="block">
              <span className="mb-1.5 block font-medium">Method</span>
              <select className={`${field} w-full`} value={method} onChange={(e) => setMethod(e.target.value)}>
                <option value="">Any</option>
                {["POST", "PUT", "PATCH", "DELETE"].map((m) => <option key={m} value={m}>{m}</option>)}
              </select>
            </label>
            <Input label="Path" placeholder="sequences/*/enrollments, leads/**, or ** for everything" value={path} onChange={(e) => setPath(e.target.value)} required />
          </div>
          <fieldset>
            <legend className="mb-2 block font-medium">Matching changes</legend>
            <div className="grid gap-2 sm:grid-cols-3">
              {(["run", "hold", "refuse"] as const).map((m) => (
                <button key={m} type="button" onClick={() => setMode(m)} aria-pressed={mode === m} className={`rounded-lg border px-3 py-2 text-left transition-colors ${mode === m ? "border-brand/60 bg-brand/[0.07] ring-1 ring-brand/40" : "border-border hover:border-ring/60"}`}>
                  <span className="block font-medium">{modeLabel[m]}</span>
                  <span className="mt-0.5 block text-[12px] text-muted-foreground">{m === "run" ? "On their own, within the limit." : m === "hold" ? "Until a person approves each." : "Never."}</span>
                </button>
              ))}
            </div>
          </fieldset>
          {mode === "run" && <Input label="Most a day" type="number" min={1} placeholder="No limit" hint="The rest wait for a person." value={limit} onChange={(e) => setLimit(e.target.value)} />}
          <Input label="Note" placeholder="Why, for whoever reads this later" value={note} onChange={(e) => setNote(e.target.value)} maxLength={500} />
          <ErrorText>{error}</ErrorText>
          <div className="flex gap-2">
            <Button disabled={busy || !token || !path.trim()}>{busy ? "Saving…" : "Add rule"}</Button>
            <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
