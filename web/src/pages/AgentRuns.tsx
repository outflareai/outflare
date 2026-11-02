import { useCallback, useEffect, useState, type FormEvent } from "react";
import type { Action, AgentRun, AgentRunCheckIn, AgentRunUpdate } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { ask, Badge, Button, confirmAction, Empty, ErrorText, field, Input, Loading, Stat, Stats, toast } from "../ui";
import { ago, plural, stamp, when } from "../format";
import { track } from "@/lib/analytics";
import { useBrand } from "@/lib/brand";
import { BrandBadge } from "@/components/brand-badge";
import { DataTable } from "@/components/data-table";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { ChangePreview } from "./Missions";

// Agent runs: Outflare's own agent carrying a playbook out in turns, as an
// agent token on the action layer (docs/actions.md, "Outflare's agent on the
// action layer"). A person picks the mode when starting one, reads what each
// turn did, writes to it, and pauses or stops it. Its changes are the actions
// of its token, so they show under Waiting for you and Everything too.

type Status = AgentRun["status"];
export const runStatusLabel: Record<Status, string> = { running: "Running", paused: "Paused", completed: "Completed", stopped: "Stopped", failed: "Failed" };
export const runStatusTone: Record<Status, "amber" | "blue" | "green" | "red" | "grey"> = { running: "green", paused: "amber", completed: "blue", stopped: "grey", failed: "red" };
export const runModes: { value: AgentRun["mode"]; label: string; help: string }[] = [
  { value: "copilot", label: "Copilot", help: "Every change waits for you under Waiting for you. The agent carries on once you decide." },
  { value: "autonomous", label: "Autonomous", help: "Changes run on their own, within a budget for what they may spend on your accounts. Some always wait for a person." },
  { value: "dry_run", label: "Dry run", help: "Nothing changes. Every change is refused and kept, so you see what it would have done." },
];
export const runModeLabel = (m: AgentRun["mode"]) => runModes.find((x) => x.value === m)?.label ?? m;
const money = (cents: number) => (cents / 100).toLocaleString(undefined, { style: "currency", currency: "USD", maximumFractionDigits: cents % 100 ? 2 : 0 });
const isLive = (r: AgentRun) => r.status === "running" || r.status === "paused";

/** The run an API answer is, when it may be a mission instead (an automation's run). */
export const isAgentRun = (d: unknown): d is AgentRun => !!d && typeof d === "object" && "token_id" in d && "max_spend_cents" in d && !("limits" in d);

// ---- List ----

export function AgentRuns({ ws, onOpen }: { ws: string; onOpen: (id: string) => void }) {
  const [list, setList] = useState<AgentRun[] | null>(null);
  const [cursor, setCursor] = useState<string | null>(null);
  const [filter, setFilter] = useState<"live" | "ended" | "all">("live");
  const [error, setError] = useState("");
  const brand = useBrand(ws).brand?.handle;

  const load = useCallback(async (after?: string) => {
    const status = filter === "live" ? "running,paused" : filter === "ended" ? "completed,stopped,failed" : "";
    const { data, error } = await api.GET("/v1/workspaces/{ws}/agent-runs", { params: { path: { ws }, query: { limit: 50, ...(status ? { status } : {}), ...(after ? { cursor: after } : {}), ...(brand ? { brand } : {}) } } });
    if (error) return setError(errorMessage(error));
    setError("");
    setList((l) => (after && l ? [...l, ...(data?.data ?? [])] : data?.data ?? []));
    setCursor(data?.next_cursor ?? null);
  }, [ws, filter, brand]);
  useEffect(() => {
    setList(null);
    load();
    const t = setInterval(() => load(), 15000);
    return () => clearInterval(t);
  }, [load]);

  if (error) return <ErrorText>{error}</ErrorText>;
  if (list === null) return <Loading />;
  const held = list.reduce((n, r) => n + r.held_actions, 0);
  return (
    <div className="space-y-3">
      <p className="text-[13px] text-muted-foreground">A run is Outflare's agent carrying a playbook out in turns on your own AI model, as an agent of its own: the mode you chose says what it may change by itself. Start one from a playbook.</p>
      {list.length > 0 && filter === "live" && (
        <Stats cols={3}>
          <Stat label="Running" value={list.filter((r) => r.status === "running").length} />
          <Stat label="Paused" value={list.filter((r) => r.status === "paused").length} tone={list.some((r) => r.status === "paused" && r.status_reason) ? "amber" : undefined} sub={list.some((r) => r.status === "paused" && r.status_reason) ? "one needs you" : undefined} />
          <Stat label="Changes waiting for you" value={held} tone={held ? "amber" : undefined} />
        </Stats>
      )}
      <DataTable
        data={list}
        rowId={(r) => r.id}
        columnsMenu={false}
        onRowClick={(r) => onOpen(r.id)}
        searchPlaceholder="Filter runs"
        empty={filter === "live" ? "No run is going. Open a playbook and start one; ended runs are under All." : "No runs here."}
        toolbar={
          <select className={field} aria-label="Show" value={filter} onChange={(e) => setFilter(e.target.value as typeof filter)}>
            <option value="live">Running and paused</option>
            <option value="ended">Ended</option>
            <option value="all">All</option>
          </select>
        }
        mobile={(r) => (
          <span className="block space-y-1">
            <span className="flex items-center justify-between gap-2"><span className="flex flex-wrap items-center gap-1.5 font-medium">{r.name} <BrandBadge id={r.brand_id} /></span> <Badge tone={runStatusTone[r.status]}>{runStatusLabel[r.status]}</Badge></span>
            <span className="block text-[12px] text-muted-foreground">{runModeLabel(r.mode)} · {plural(r.turns, "turn")}{r.held_actions > 0 && ` · ${plural(r.held_actions, "change")} waiting for you`}</span>
          </span>
        )}
        columns={[
          { id: "run", header: "Run", accessorFn: (r) => r.name, cell: ({ row: { original: r } }) => <span className="block min-w-0"><span className="flex flex-wrap items-center gap-1.5 font-medium">{r.name} <BrandBadge id={r.brand_id} /></span><span className="block text-[12px] text-muted-foreground">{r.turn_summary || r.goal}</span></span> },
          { id: "status", header: "Status", accessorFn: (r) => r.status, cell: ({ row: { original: r } }) => <span className="flex flex-wrap items-center gap-1"><Badge tone={runStatusTone[r.status]}>{runStatusLabel[r.status]}</Badge>{r.status === "paused" && r.status_reason && <span className="text-[12px] text-amber-700 dark:text-amber-400">{r.status_reason}</span>}</span> },
          { id: "mode", header: "Mode", accessorFn: (r) => r.mode, cell: ({ row: { original: r } }) => <span className="whitespace-nowrap">{runModeLabel(r.mode)}{r.mode === "autonomous" && r.max_spend_cents != null && <span className="text-muted-foreground"> · {money(r.max_spend_cents)}</span>}</span> },
          { id: "changes", header: "Changes", accessorFn: (r) => r.actions, cell: ({ row: { original: r } }) => <span className="whitespace-nowrap">{r.actions}{r.held_actions > 0 && <span className="text-amber-700 dark:text-amber-400"> · {r.held_actions} waiting</span>}</span> },
          { id: "turns", header: "Turns", accessorFn: (r) => r.turns, cell: ({ row: { original: r } }) => <span className="whitespace-nowrap text-muted-foreground">{r.turns}{r.in_turn ? " · in a turn" : r.status === "running" && r.next_turn_at ? ` · next ${when(r.next_turn_at)}` : ""}</span> },
          { id: "when", header: "Started", accessorFn: (r) => r.created_at, cell: ({ row: { original: r } }) => <span className="whitespace-nowrap text-muted-foreground" title={stamp(r.created_at)}>{ago(r.created_at)}</span> },
        ]}
      />
      {cursor && <div className="flex justify-center"><Button variant="ghost" onClick={() => load(cursor)}>Show more</Button></div>}
    </div>
  );
}

// ---- One run ----

export function AgentRunDetail({ ws, id, canRun, canApprove, onBack }: { ws: string; id: string; canRun: boolean; canApprove: boolean; onBack: () => void }) {
  const [d, setD] = useState<AgentRunCheckIn | null>(null);
  const [missing, setMissing] = useState(false);
  const [refresh, setRefresh] = useState(0);
  const [tab, setTab] = useState("");

  const load = useCallback(async () => {
    const { data, response } = await api.GET("/v1/workspaces/{ws}/agent-runs/{id}", { params: { path: { ws, id } } });
    if (response.status === 404) return setMissing(true);
    if (data) setD(data);
    setRefresh((n) => n + 1);
  }, [ws, id]);
  useEffect(() => {
    load();
    const t = setInterval(load, 10000);
    return () => clearInterval(t);
  }, [load]);

  // The run's name, not its id, in the top bar.
  const name = d?.run.name;
  useEffect(() => {
    if (name) window.dispatchEvent(new CustomEvent("outflare:item-name", { detail: { id, name } }));
  }, [id, name]);

  if (missing) return <Empty title="Run not found" action={<Button variant="ghost" onClick={onBack}>All runs</Button>}>It may be in another workspace, or you may not see its brand.</Empty>;
  if (!d) return <Loading />;
  const r = d.run;

  async function act(what: "pause" | "resume" | "stop") {
    if (what === "stop" && !(await confirmAction("Stop this run?", <p>It takes no more turns and nothing more can act as it. Its changes waiting for you stay until you decide them or they expire.</p>, "Stop"))) return;
    const p = { params: { path: { ws, id } } };
    const { error } = what === "pause" ? await api.POST("/v1/workspaces/{ws}/agent-runs/{id}/pause", p) : what === "resume" ? await api.POST("/v1/workspaces/{ws}/agent-runs/{id}/resume", p) : await api.POST("/v1/workspaces/{ws}/agent-runs/{id}/stop", p);
    if (error) return toast(errorMessage(error), "error");
    track("ui.agent_run_acted", { action: what, mode: r.mode });
    toast(what === "pause" ? "Paused. It takes no more turns until you resume it." : what === "resume" ? "Resumed. Its next turn comes soon." : "Stopped.");
    load();
  }

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 space-y-1">
          <button type="button" className="text-[13px] text-muted-foreground underline-offset-4 hover:underline" onClick={onBack}>← All runs</button>
          <h2 className="flex flex-wrap items-center gap-2 text-[18px] font-semibold leading-tight">{r.name} <Badge tone={runStatusTone[r.status]}>{runStatusLabel[r.status]}</Badge> <BrandBadge id={r.brand_id} /></h2>
          <p className="text-[13px] text-muted-foreground">
            {runModeLabel(r.mode)}{r.mode === "autonomous" && (r.max_spend_cents != null ? ` · budget ${money(r.max_spend_cents)}` : " · no budget")}
            {r.playbook_id && ` · playbook ${r.playbook_id}`} · started {ago(r.created_at)}{r.created_by_name && ` by ${r.created_by_name}`}{r.automation_id && " · by an automation"}
          </p>
        </div>
        {canRun && isLive(r) && (
          <div className="flex flex-wrap gap-2">
            {r.status === "running" && <Button variant="ghost" onClick={() => act("pause")}>Pause</Button>}
            {r.status === "paused" && <Button onClick={() => act("resume")}>Resume</Button>}
            <Button variant="ghost" onClick={() => act("stop")}>Stop</Button>
          </div>
        )}
      </div>

      {r.status === "paused" && r.status_reason && (
        <div className="rounded-lg border border-amber-500/30 bg-amber-500/10 px-4 py-3 text-[13px]">
          <p className="font-medium text-amber-800 dark:text-amber-300">Paused: {r.status_reason}</p>
          {r.turn_error && <p className="mt-1 text-muted-foreground">{r.turn_error}</p>}
          <p className="mt-1 text-muted-foreground">Fix what it needs, then resume it.</p>
        </div>
      )}
      {r.status === "failed" && <div className="rounded-lg border border-red-500/30 bg-red-500/10 px-4 py-3 text-[13px]"><p className="font-medium text-red-800 dark:text-red-300">Failed: {r.status_reason || r.turn_error}</p></div>}

      <Stats cols={4}>
        <Stat label="Turns" value={r.turns} sub={r.in_turn ? "in a turn now" : r.status === "running" && r.next_turn_at ? `next ${when(r.next_turn_at)}` : r.last_turn_at ? `last ${ago(r.last_turn_at)}` : "none yet"} />
        <Stat label="Changes" value={r.actions} sub={r.mode === "dry_run" ? "refused and kept" : r.mode === "copilot" ? "each waits for you" : "run on their own"} />
        <Stat label="Waiting for you" value={r.held_actions} tone={r.held_actions ? "amber" : undefined} />
        <Stat label="Spent" value={money(r.spend_cents)} sub={`on your accounts · AI ${money(r.ai_cents)}`} />
      </Stats>

      <Tabs value={tab || (r.held_actions > 0 && canApprove ? "waiting" : "activity")} onValueChange={setTab}>
        <TabsList>
          <TabsTrigger value="activity">Activity</TabsTrigger>
          <TabsTrigger value="waiting">Waiting for you{r.held_actions ? ` (${r.held_actions})` : ""}</TabsTrigger>
          <TabsTrigger value="changes">Changes</TabsTrigger>
          <TabsTrigger value="brief">Brief</TabsTrigger>
        </TabsList>
        <TabsContent value="activity"><Activity ws={ws} r={r} updates={d.updates} canWrite={canRun} onPosted={load} /></TabsContent>
        <TabsContent value="waiting"><RunActions ws={ws} r={r} held refresh={refresh} canApprove={canApprove} onDone={load} /></TabsContent>
        <TabsContent value="changes"><RunActions ws={ws} r={r} refresh={refresh} canApprove={false} onDone={load} /></TabsContent>
        <TabsContent value="brief">
          <div className="space-y-4 text-[13px]">
            <section><h3 className="mb-1 font-medium">Goal</h3><Prose text={r.goal} /></section>
            {r.plan && <section><h3 className="mb-1 font-medium">Plan</h3><Prose text={r.plan} /></section>}
            {r.instructions && <section><h3 className="mb-1 font-medium">Standing instructions</h3><Prose text={r.instructions} /></section>}
            {Object.keys(r.inputs).length > 0 && (
              <section>
                <h3 className="mb-1 font-medium">Inputs</h3>
                <dl className="grid gap-1 sm:grid-cols-2">{Object.entries(r.inputs).map(([k, v]) => <div key={k} className="flex gap-2"><dt className="font-mono text-[12px] text-muted-foreground">{k}</dt><dd className="min-w-0 break-words">{v}</dd></div>)}</dl>
              </section>
            )}
            {r.report && <section><h3 className="mb-1 font-medium">Report</h3><Prose text={r.report} /></section>}
            <p className="font-mono text-[11px] text-muted-foreground">run {r.id}{r.token_id && ` · token ${r.token_id}`}</p>
          </div>
        </TabsContent>
      </Tabs>
    </div>
  );
}

// Plans and notes are Markdown; show **bold** and `code` and keep the rest as written.
function Prose({ text }: { text: string }) {
  const parts = text.split(/(\*\*[^*\n]+\*\*|`[^`\n]+`)/g);
  return (
    <div className="whitespace-pre-wrap text-[13px] leading-relaxed">
      {parts.map((p, i) =>
        p.startsWith("**") && p.endsWith("**") && p.length > 4 ? <strong key={i} className="font-medium">{p.slice(2, -2)}</strong>
        : p.startsWith("`") && p.endsWith("`") && p.length > 2 ? <code key={i} className="rounded bg-muted px-1 font-mono text-[12px]">{p.slice(1, -1)}</code>
        : p,
      )}
    </div>
  );
}

function Activity({ ws, r, updates, canWrite, onPosted }: { ws: string; r: AgentRun; updates: AgentRunUpdate[]; canWrite: boolean; onPosted: () => void }) {
  const [body, setBody] = useState("");
  const [busy, setBusy] = useState(false);
  async function post(e: FormEvent) {
    e.preventDefault();
    if (!body.trim()) return;
    setBusy(true);
    const { error } = await api.POST("/v1/workspaces/{ws}/agent-runs/{id}/updates", { params: { path: { ws, id: r.id } }, body: { kind: "instruction", body: body.trim() } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    setBody("");
    track("ui.agent_run_acted", { action: "write", mode: r.mode });
    toast("Sent. The agent reads it at its next turn, which comes forward.");
    onPosted();
  }
  const kindTone = { question: "amber", instruction: "blue", report: "green", progress: "grey", note: "grey" } as const;
  return (
    <div className="space-y-4">
      {canWrite && isLive(r) && (
        <form onSubmit={post} className="space-y-2">
          <textarea className={`${field} w-full`} rows={2} placeholder="Answer a question or tell the agent something, e.g. Only fintech leads" value={body} onChange={(e) => setBody(e.target.value)} aria-label="Message to the agent" />
          <div className="flex justify-end"><Button type="submit" disabled={busy || !body.trim()}>Send to agent</Button></div>
        </form>
      )}
      {r.turn_summary && <p className="text-[13px]"><span className="font-medium">Last turn:</span> {r.turn_summary}</p>}
      {updates.length === 0 ? (
        <p className="py-6 text-center text-[13px] text-muted-foreground">No updates yet. The agent posts progress, questions and its report here.</p>
      ) : (
        <ol className="space-y-3">
          {updates.map((u) => (
            <li key={u.id} className={`rounded-lg border p-3 ${u.author === "system" ? "border-dashed bg-transparent" : "bg-card"}`}>
              <div className="flex flex-wrap items-center gap-2 text-[12px]">
                <span className="font-medium">{u.author === "system" ? "Outflare" : u.author === "agent" ? "Agent" : u.actor_name ?? "A person"}</span>
                {u.kind !== "note" && <Badge tone={kindTone[u.kind]}>{u.kind}</Badge>}
                <span className="text-muted-foreground" title={stamp(u.created_at)}>{ago(u.created_at)}</span>
              </div>
              <div className="mt-1"><Prose text={u.body} /></div>
            </li>
          ))}
        </ol>
      )}
    </div>
  );
}

// The run's changes: the actions of its token, held ones to decide here.
function RunActions({ ws, r, held, refresh, canApprove, onDone }: { ws: string; r: AgentRun; held?: boolean; refresh: number; canApprove: boolean; onDone: () => void }) {
  const [list, setList] = useState<Action[] | null>(null);
  const [open, setOpen] = useState<Action | null>(null);
  const [error, setError] = useState("");
  const token = r.token_id;
  const load = useCallback(async () => {
    if (!token) return setList([]);
    const { data, error } = await api.GET("/v1/workspaces/{ws}/actions", { params: { path: { ws }, query: { token_id: token, limit: held ? 200 : 100, ...(held ? { status: "held" } : {}) } } });
    if (error) return setError(errorMessage(error));
    setError("");
    setList(data?.data ?? []);
  }, [ws, token, held]);
  useEffect(() => {
    load();
  }, [load, refresh]);

  async function decide(a: Action, approve: boolean) {
    let note = "";
    if (!approve) {
      const ans = await ask({ title: "Reject this change?", body: "The agent sees your reason at its next turn.", input: { label: "Reason", placeholder: "e.g. Use a softer subject line", optional: true }, choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Reject", value: "ok", variant: "danger-solid" }] });
      if (ans?.choice !== "ok") return;
      note = ans.value;
    }
    const p = { params: { path: { ws, id: a.id } } };
    const { data, error } = approve ? await api.POST("/v1/workspaces/{ws}/actions/{id}/approve", { ...p, body: { note } }) : await api.POST("/v1/workspaces/{ws}/actions/{id}/reject", { ...p, body: { note } });
    if (error) return toast(errorMessage(error), "error");
    track("ui.action_decided", { decision: approve ? "approve" : "reject", how: "single", count: 1, from: "run" });
    toast(approve ? (data?.status === "executed" ? "Approved and done." : "Approved, but it failed. See the change.") : "Rejected.", approve && data?.status !== "executed" ? "error" : "ok");
    onDone();
  }

  if (error) return <ErrorText>{error}</ErrorText>;
  if (list === null) return <Loading />;
  if (list.length === 0) return <Empty title={held ? "Nothing waiting" : "No changes yet"}>{held ? "Changes the run can't make on its own show up here for you to decide." : r.mode === "dry_run" ? "What the run would have changed shows up here, refused and kept." : "Every change the run makes through the action layer shows up here."}</Empty>;
  return (
    <div className="space-y-2">
      {list.map((a) => (
        <div key={a.id} className="rounded-lg border p-3 text-[13px]">
          <div className="flex flex-wrap items-start gap-3">
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center gap-1.5">
                <button type="button" className="font-medium underline-offset-4 hover:underline" onClick={() => setOpen(a)}>{a.summary}</button>
                <Badge tone={actionTone[a.status]}>{actionLabel[a.status]}</Badge>
              </div>
              {a.reason && <p className="text-muted-foreground">“{a.reason}”</p>}
              <p className="mt-0.5 font-mono text-[11px] text-muted-foreground">{a.method} {a.path.replace(/^\/v1\/workspaces\/[^/]+\//, "")} · {ago(a.created_at)}{a.decided_by_name && ` · ${a.status === "rejected" ? "rejected" : "approved"} by ${a.decided_by_name}`}</p>
            </div>
            {held && canApprove && a.status === "held" && (
              <div className="flex flex-wrap justify-end gap-2">
                <Button onClick={() => decide(a, true)}>Approve</Button>
                <Button variant="ghost" onClick={() => decide(a, false)}>Reject</Button>
              </div>
            )}
          </div>
          {held && a.body !== undefined && a.body !== null && <ChangePreview body={a.body} />}
        </div>
      ))}
      {open && (
        <Dialog open onOpenChange={(o) => !o && setOpen(null)}>
          <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-2xl">
            <DialogHeader>
              <DialogTitle className="flex flex-wrap items-center gap-2">{open.summary} <Badge tone={actionTone[open.status]}>{actionLabel[open.status]}</Badge></DialogTitle>
              <DialogDescription>{open.method} {open.path.replace(/^\/v1\/workspaces\/[^/]+\//, "")} · {stamp(open.created_at)}</DialogDescription>
            </DialogHeader>
            <div className="space-y-3 text-[13px]">
              {open.reason && <p className="text-muted-foreground">“{open.reason}”</p>}
              {open.hold_reason && <p className="text-muted-foreground">{open.hold_reason}</p>}
              {open.decision_note && <p>“{open.decision_note}”</p>}
              {open.body !== undefined && open.body !== null && <ChangePreview body={open.body} />}
              {open.body !== undefined && open.body !== null && <details><summary className="cursor-pointer text-[12px] text-muted-foreground">Exact request</summary><pre className="mt-1 max-h-72 overflow-auto rounded border bg-muted/40 p-2 text-[11px]"><code>{JSON.stringify(open.body, null, 2)}</code></pre></details>}
              {open.result !== undefined && open.result !== null && <details><summary className="cursor-pointer text-[12px] text-muted-foreground">Result</summary><pre className="mt-1 max-h-72 overflow-auto rounded border bg-muted/40 p-2 text-[11px]"><code>{JSON.stringify(open.result, null, 2)}</code></pre></details>}
            </div>
          </DialogContent>
        </Dialog>
      )}
    </div>
  );
}

const actionLabel: Record<Action["status"], string> = { held: "Waiting", running: "Running", executed: "Done", failed: "Failed", refused: "Refused", rejected: "Rejected", expired: "Expired", cancelled: "Cancelled" };
const actionTone: Record<Action["status"], "amber" | "blue" | "green" | "red" | "grey"> = { held: "amber", running: "blue", executed: "green", failed: "red", refused: "red", rejected: "grey", expired: "grey", cancelled: "grey" };

// ---- Starting one from a playbook ----

/** The mode picker and budget for starting a playbook as a run. */
export function RunModePicker({ mode, setMode, budget, setBudget }: { mode: AgentRun["mode"]; setMode: (m: AgentRun["mode"]) => void; budget: string; setBudget: (s: string) => void }) {
  return (
    <fieldset className="space-y-2">
      <legend className="mb-2 block font-medium">What it may change on its own</legend>
      <div className="grid gap-2 sm:grid-cols-3">
        {runModes.map((m) => (
          <button key={m.value} type="button" onClick={() => setMode(m.value)} aria-pressed={mode === m.value} className={`rounded-lg border px-3 py-2 text-left transition-colors ${mode === m.value ? "border-brand/60 bg-brand/[0.07] ring-1 ring-brand/40" : "border-border hover:border-ring/60"}`}>
            <span className="block font-medium">{m.label}</span>
            <span className="mt-0.5 block text-[12px] text-muted-foreground">{m.help}</span>
          </button>
        ))}
      </div>
      {mode === "autonomous" && <Input label="Budget ($)" inputMode="decimal" placeholder="e.g. 20" hint="The most its changes may spend on your own accounts in all. Once the budget is near, changes wait for you." value={budget} onChange={(e) => setBudget(e.target.value)} required />}
    </fieldset>
  );
}

/** Dollars typed into the budget as cents, or null for none; NaN when it isn't a number. */
export const budgetCents = (s: string): number | null => (s.trim() === "" ? null : Math.round(Number(s) * 100));
