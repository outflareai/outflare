import { useCallback, useEffect, useState, type FormEvent, type ReactNode } from "react";
import type { APIToken, Mission, MissionAction, MissionBrief, MissionCall, MissionMode, MissionUpdate } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { BrandBadge } from "@/components/brand-badge";
import { api, errorMessage } from "../api";
import { ask, Badge, Button, Card, confirmAction, CopyButton, Empty, ErrorText, field, Loading, PageHeader, Stat, Stats, Status, toast } from "../ui";
import { ago, plural, stamp, when } from "../format";
import { DataTable, type ColumnDef } from "@/components/data-table";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Snippet } from "./Tokens";
import { ConnectorLogo } from "@/components/connector-logo";
import { appPath } from "../lib/paths";

// Missions: goals agents work toward. This page is where a person sees what
// an agent is doing and why, approves its plan and its writes, answers its
// questions, and stops it.

const statusLabel: Record<Mission["status"], string> = {
  draft: "Draft",
  pending_approval: "Waiting for approval",
  running: "Running",
  paused: "Paused",
  completed: "Completed",
  stopped: "Stopped",
  failed: "Failed",
};
const statusTone = { draft: "grey", pending_approval: "amber", running: "green", paused: "grey", completed: "blue", stopped: "grey", failed: "red" } as const;

const modes: { value: MissionMode; label: string; help: string }[] = [
  { value: "dry_run", label: "Dry run", help: "The agent can read everything. Its writes are recorded so you can review them, and nothing changes." },
  { value: "approve_writes", label: "Approve each change", help: "Every change the agent asks for waits here until someone approves it." },
  { value: "autonomous", label: "Autonomous", help: "Changes run straight away, within the mission's limits and the agent's token limits." },
];
const modeLabel = (m: MissionMode) => modes.find((x) => x.value === m)?.label ?? m;

const writesText: Record<Mission["writes"], string> = {
  run: "Changes run straight away, within the limits below.",
  wait_for_approval: "Every change waits for your approval.",
  dry_run: "Changes are dry runs: recorded for review, never run.",
  refused: "Changes are refused. Reads still work while paused.",
};

const metricLabel = { meetings: "meetings", replies: "replies", sends: "emails sent", leads: "leads contacted" } as const;
const money = (cents: number) => (cents / 100).toLocaleString(undefined, { style: "currency", currency: "USD", maximumFractionDigits: cents % 100 ? 2 : 0 });
// The end date in the viewer's time zone, with the zone named.
const endsAt = (iso: string) => new Date(iso).toLocaleString(undefined, { weekday: "short", day: "numeric", month: "short", year: "numeric", hour: "numeric", minute: "2-digit", timeZoneName: "short" });
const isLive = (m: Mission) => !["completed", "stopped", "failed"].includes(m.status);

type Detail = { mission: Mission; brief: MissionBrief | null; updates: MissionUpdate[]; open_connect_requests: MissionUpdate[]; how_to_act: { header: string; writes: string } };

export function Missions({ ws, canManage, open, setOpen }: { ws: string; canManage: boolean; open?: string; setOpen: (id?: string) => void }) {
  if (open) return <MissionDetail key={open} ws={ws} id={open} canManage={canManage} onBack={() => setOpen(undefined)} />;
  return <MissionList ws={ws} canManage={canManage} onOpen={setOpen} />;
}

// ---- List ----

function needsYou(m: Mission): string[] {
  const out: string[] = [];
  if (m.status === "pending_approval") out.push("Plan to approve");
  if (m.pending_actions > 0) out.push(plural(m.pending_actions, "change") + " to approve");
  return out;
}

function GoalBar({ m, compact = false }: { m: Mission; compact?: boolean }) {
  if (!m.goal.metric || !m.goal.target) return <span className="text-muted-foreground">{compact ? "—" : "No number set"}</span>;
  const pct = Math.min(100, Math.round((m.goal.current / m.goal.target) * 100));
  return (
    <div className="min-w-28">
      <div className="font-mono text-[12px] tabular-nums">
        {m.goal.current.toLocaleString()} / {m.goal.target.toLocaleString()} <span className="text-muted-foreground">{metricLabel[m.goal.metric]}</span>
      </div>
      <div className="mt-1 h-1.5 overflow-hidden rounded-full bg-muted" role="progressbar" aria-valuenow={pct} aria-valuemin={0} aria-valuemax={100} aria-label="Progress toward the goal">
        <div className={`h-full rounded-full ${m.goal.reached ? "bg-green-500" : "bg-brand-strong dark:bg-brand"}`} style={{ width: `${pct}%` }} />
      </div>
    </div>
  );
}

const columns: ColumnDef<Mission, unknown>[] = [
  {
    id: "name",
    header: "Mission",
    accessorFn: (m) => `${m.name} ${m.goal.text}`,
    cell: ({ row: { original: m } }) => (
      <div className="min-w-0 max-w-96">
        <div className="flex items-center gap-1.5"><span className="font-medium group-hover:underline">{m.name}</span> <BrandBadge id={m.brand_id} /></div>
        {m.goal.text && <div className="truncate text-[12px] text-muted-foreground">{m.goal.text}</div>}
      </div>
    ),
  },
  { id: "status", header: "Status", accessorFn: (m) => m.status, cell: ({ row: { original: m } }) => <Badge tone={statusTone[m.status]}>{statusLabel[m.status]}</Badge> },
  { id: "mode", header: "Changes", accessorFn: (m) => m.mode, cell: ({ row: { original: m } }) => <span className="whitespace-nowrap text-[12px]">{modeLabel(m.mode)}</span> },
  { id: "goal", header: "Goal", accessorFn: (m) => m.goal.current, enableSorting: false, cell: ({ row: { original: m } }) => <GoalBar m={m} compact /> },
  { id: "agent", header: "Agent", accessorFn: (m) => m.token_name ?? m.created_by_name ?? "", cell: ({ row: { original: m } }) => <span className="whitespace-nowrap">{m.token_name ?? <span className="text-muted-foreground">Any agent</span>}</span> },
  {
    id: "needs",
    header: "Needs you",
    accessorFn: (m) => needsYou(m).length,
    cell: ({ row: { original: m } }) => {
      const n = needsYou(m);
      return n.length ? <div className="flex flex-wrap gap-1">{n.map((x) => <Badge key={x} tone="amber">{x}</Badge>)}</div> : <span className="text-muted-foreground">—</span>;
    },
  },
  { id: "last", header: "Last call", accessorFn: (m) => m.last_call_at ?? "", meta: { mono: true }, cell: ({ row: { original: m } }) => <span className="whitespace-nowrap text-muted-foreground">{m.last_call_at ? ago(m.last_call_at) : "never"}</span> },
];

function MissionList({ ws, canManage, onOpen }: { ws: string; canManage: boolean; onOpen: (id: string) => void }) {
  const [list, setList] = useState<Mission[] | null>(null);
  const [creating, setCreating] = useState(false);
  const [filter, setFilter] = useState<"live" | "ended" | "all">("live");

  const brand = useBrand(ws).brand?.handle;
  const load = useCallback(async () => {
    const { data } = await api.GET("/v1/workspaces/{ws}/missions", { params: { path: { ws }, query: { ...(brand ? { brand } : {}) } } });
    setList(data?.data ?? []);
  }, [ws, brand]);
  useEffect(() => {
    load();
    const t = setInterval(load, 15000);
    return () => clearInterval(t);
  }, [load]);

  if (!list) return <Loading />;
  const waiting = list.filter((m) => needsYou(m).length > 0);
  const shown = list.filter((m) => filter === "all" || (filter === "live") === isLive(m));
  const running = list.filter((m) => m.status === "running").length;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Missions"
        description="Goals your agents work toward, such as 10 meetings by a date. See every call an agent made and why, approve its plan and its changes, and stop it at any time."
        action={canManage && list.length > 0 && <Button onClick={() => setCreating(true)}>New mission</Button>}
      />
      {canManage && <GoalBox ws={ws} first={list.length === 0} onPlanned={(m) => onOpen(m.id)} />}
      {list.length === 0 ? (
        <FirstMission ws={ws} canManage={canManage} onCreate={() => setCreating(true)} />
      ) : (
        <>
          <Stats cols={4}>
            <Stat label="Running" value={running} />
            <Stat label="Needs you" value={waiting.length} tone={waiting.length ? "amber" : undefined} sub={waiting.length ? "plans or changes to approve" : "nothing waiting"} />
            <Stat label="Sent by missions" value={list.reduce((n, m) => n + m.progress.sends, 0).toLocaleString()} />
            <Stat label="Meetings from missions" value={list.reduce((n, m) => n + m.progress.meetings, 0).toLocaleString()} />
          </Stats>
          {waiting.length > 0 && (
            <div className="rounded-lg border border-amber-500/30 bg-amber-500/10 px-4 py-3 text-[13px]">
              <p className="font-medium text-amber-800 dark:text-amber-300">Waiting for you</p>
              <ul className="mt-1 space-y-0.5">
                {waiting.map((m) => (
                  <li key={m.id}>
                    <button className="underline-offset-2 hover:underline" onClick={() => onOpen(m.id)}>{m.name}</button>
                    <span className="text-muted-foreground">: {needsYou(m).join(", ")}</span>
                  </li>
                ))}
              </ul>
            </div>
          )}
          <DataTable
            data={shown}
            rowId={(m) => m.id}
            onRowClick={(m) => onOpen(m.id)}
            columns={columns}
            searchPlaceholder="Filter missions"
            empty={filter === "live" ? "No running or draft missions. Ended ones are under All." : "No missions here."}
            toolbar={
              <select className={field} aria-label="Show" value={filter} onChange={(e) => setFilter(e.target.value as typeof filter)}>
                <option value="live">Active and drafts</option>
                <option value="ended">Ended</option>
                <option value="all">All</option>
              </select>
            }
            mobile={(m) => (
              <div className="space-y-1.5">
                <div className="flex items-center justify-between gap-2">
                  <span className="flex flex-wrap items-center gap-1.5 font-medium">{m.name} <BrandBadge id={m.brand_id} /></span>
                  <Badge tone={statusTone[m.status]}>{statusLabel[m.status]}</Badge>
                </div>
                <GoalBar m={m} compact />
                {needsYou(m).length > 0 && <div className="text-[12px] text-amber-700 dark:text-amber-400">{needsYou(m).join(", ")}</div>}
              </div>
            )}
          />
        </>
      )}
      {creating && <MissionForm ws={ws} onClose={() => setCreating(false)} onSaved={(m) => { setCreating(false); onOpen(m.id); }} />}
    </div>
  );
}

const examples = [
  "Book 10 meetings with CFOs at Australian fintechs with 50 to 500 staff by 30 November",
  "Get 30 replies from SaaS founders in London this month",
  "Re-engage leads who replied last quarter but never booked",
];

// What stops the AI model being used, when the server says so.
const aiCodes = ["connector_needed", "ai_model_needed", "ai_budget_reached"];
type AIBlock = { code: string; message: string } | null;
function aiBlock(status: number, error: unknown): AIBlock {
  const e = (error as { error?: { code?: string; message?: string } })?.error;
  return status === 409 && e?.code && aiCodes.includes(e.code) ? { code: e.code, message: e.message ?? "" } : null;
}

function AINeeded({ ws, block }: { ws: string; block: AIBlock }) {
  if (!block) return null;
  return (
    <div className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-[13px]">
      <span>{block.code === "connector_needed" ? "Connect an AI model with your own API key first: Claude, OpenAI, Gemini or Mistral. Usage is billed to your account with them." : block.message}</span>
      <a href={appPath(ws, "workspace")}><Button type="button">{block.code === "connector_needed" ? "Choose an AI model" : "Open AI settings"}</Button></a>
    </div>
  );
}

// The model in use, under the goal box.
function useAIModel(ws: string) {
  const [label, setLabel] = useState("");
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/ai-settings", { params: { path: { ws } } }).then(({ data }) => {
      if (data?.active && !data.problem) setLabel(`${data.active.provider_name}, ${data.active.model}`);
    });
  }, [ws]);
  return label;
}

// Type a goal, and the workspace's AI model drafts the mission: steps,
// limits and the connectors it needs. The draft waits for a person to
// approve it.
function GoalBox({ ws, first, onPlanned }: { ws: string; first: boolean; onPlanned: (m: Mission) => void }) {
  const [goal, setGoal] = useState("");
  const [busy, setBusy] = useState(false);
  const [block, setBlock] = useState<AIBlock>(null);
  const [err, setErr] = useState("");
  const model = useAIModel(ws);
  async function plan(e: FormEvent) {
    e.preventDefault();
    if (!goal.trim()) return;
    setBusy(true);
    setErr("");
    setBlock(null);
    const { data, error, response } = await api.POST("/v1/workspaces/{ws}/missions/plan", { params: { path: { ws } }, body: { goal: goal.trim() } });
    setBusy(false);
    const b = aiBlock(response.status, error);
    if (b) return setBlock(b);
    if (error || !data) return setErr(errorMessage(error));
    toast("Plan drafted. Review it, then approve to start.");
    onPlanned(data.mission);
  }
  return (
    <Card title={first ? "Start with a goal" : "New mission from a goal"} description="Say what you want in plain words. Your AI model drafts the plan, the limits and the tools it needs. Nothing runs until you approve it.">
      <form onSubmit={plan} className="space-y-3">
        <textarea
          className={`${field} w-full text-[14px]`}
          rows={first ? 3 : 2}
          placeholder={`e.g. ${examples[0]}`}
          value={goal}
          onChange={(e) => setGoal(e.target.value)}
          onKeyDown={(e) => { if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) plan(e); }}
          aria-label="Goal"
          maxLength={2000}
          disabled={busy}
        />
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex flex-wrap gap-1.5">
            {!goal && examples.map((x) => (
              <button key={x} type="button" onClick={() => setGoal(x)} className="rounded-full border px-2.5 py-0.5 text-[12px] text-muted-foreground hover:bg-accent hover:text-foreground">
                {x.length > 48 ? x.slice(0, 46) + "…" : x}
              </button>
            ))}
            {busy && <span className="text-[12px] text-muted-foreground">{model ? model.split(",")[0] : "The model"} is drafting the plan. This takes up to a minute.</span>}
            {!busy && goal && model && (
              <span className="text-[12px] text-muted-foreground">
                Uses {model}. <a className="underline underline-offset-2" href={appPath(ws, "workspace")}>Change</a>
              </span>
            )}
          </div>
          <Button type="submit" disabled={busy || !goal.trim()}>{busy ? "Planning…" : "Plan it"}</Button>
        </div>
        <AINeeded ws={ws} block={block} />
        {err && <ErrorText>{err}</ErrorText>}
      </form>
    </Card>
  );
}

function FirstMission({ ws, canManage, onCreate }: { ws: string; canManage: boolean; onCreate: () => void }) {
  return (
    <div className="space-y-4">
      <Empty
        title="No missions yet"
        action={canManage && <Button onClick={onCreate}>New mission</Button>}
      >
        Give an agent a goal, like "book 10 meetings with fintech CFOs in Australia by 30 November". Type it in the box above and your AI model drafts the plan, or let your own agent start one over MCP. Either way it reads the project brief, rehearses in a dry run, waits for your approval, and works within the limits you set.
      </Empty>
      <div className="grid gap-4 lg:grid-cols-2">
        <Snippet
          title="Claude Code"
          code={`claude mcp add outflare \\\n  -e OUTFLARE_URL=${location.origin} \\\n  -e OUTFLARE_TOKEN=oft_... \\\n  -e OUTFLARE_WORKSPACE=${ws} \\\n  -- node mcp/dist/index.js\n\n# Then ask: "Start a mission to book 10 meetings\n# with fintech CFOs in Australia by 30 November."`}
        />
        <Card title="How a mission runs">
          <ol className="list-decimal space-y-1.5 pl-5 text-[13px]">
            <li>The agent creates the mission and reads the brief. Its changes are dry runs.</li>
            <li>It asks you to connect any tool it needs, like an email verifier.</li>
            <li>It sends you its plan. You approve it and choose how changes run.</li>
            <li>It works, posting progress here. You approve changes, answer questions or pause it.</li>
            <li>It finishes with a report.</li>
          </ol>
          <p className="mt-3 text-[12px] text-muted-foreground">The agent's API token needs the missions.read and missions.run scopes. It can never approve its own plan or changes.</p>
        </Card>
      </div>
    </div>
  );
}

// ---- Create and edit ----

function MissionForm({ ws, mission, canApprove = true, onClose, onSaved }: { ws: string; mission?: Mission; canApprove?: boolean; onClose: () => void; onSaved: (m: Mission) => void }) {
  const [name, setName] = useState(mission?.name ?? "");
  const [goal, setGoal] = useState(mission?.goal.text ?? "");
  const [metric, setMetric] = useState<string>(mission?.goal.metric ?? "meetings");
  const [target, setTarget] = useState(mission?.goal.target ? String(mission.goal.target) : "");
  const [instructions, setInstructions] = useState(mission?.instructions ?? "");
  const [tokenId, setTokenId] = useState(mission?.token_id ?? "");
  const [maxSends, setMaxSends] = useState(mission?.limits.max_sends ? String(mission.limits.max_sends) : "");
  const [maxSpend, setMaxSpend] = useState(mission?.limits.max_spend_cents != null ? String(mission.limits.max_spend_cents / 100) : "");
  const [endsAt, setEndsAt] = useState(mission?.limits.ends_at ? mission.limits.ends_at.slice(0, 10) : "");
  const [tokens, setTokens] = useState<APIToken[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const approved = mission && (mission.status === "running" || mission.status === "paused");
  const limitsLocked = approved && !canApprove;

  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/api-tokens", { params: { path: { ws } } }).then(({ data }) => setTokens(data?.data ?? []));
  }, [ws]);

  async function save(e: FormEvent) {
    e.preventDefault();
    setError("");
    const n = target.trim() ? Number(target) : null;
    if (n !== null && (!Number.isInteger(n) || n < 1)) return setError("The target must be a whole number, 1 or more.");
    const body = {
      name: name.trim(),
      goal: goal.trim(),
      goal_metric: n ? (metric as "meetings") : null,
      goal_target: n,
      instructions,
      ...(limitsLocked
        ? {}
        : {
            token_id: tokenId || null,
            max_sends: maxSends.trim() ? Number(maxSends) : null,
            max_spend_cents: maxSpend.trim() ? Math.round(Number(maxSpend) * 100) : null,
            ends_at: endsAt ? new Date(`${endsAt}T23:59:00`).toISOString() : null,
          }),
    };
    setBusy(true);
    const res = mission
      ? await api.PATCH("/v1/workspaces/{ws}/missions/{id}", { params: { path: { ws, id: mission.id } }, body })
      : await api.POST("/v1/workspaces/{ws}/missions", { params: { path: { ws } }, body });
    setBusy(false);
    if (res.error || !res.data) return setError(errorMessage(res.error));
    toast(mission ? "Mission saved." : "Mission created as a draft.");
    onSaved(res.data);
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{mission ? "Edit mission" : "New mission"}</DialogTitle>
          <DialogDescription>{mission ? "Changes apply to the agent's next check-in." : "It starts as a draft. An agent can rehearse it in a dry run, then send you its plan."}</DialogDescription>
        </DialogHeader>
        <form id="mission-form" onSubmit={save} className="space-y-4 text-[13px]">
          <Labeled label="Name">
            <input className={`${field} w-full`} required maxLength={200} value={name} onChange={(e) => setName(e.target.value)} placeholder="Fintech CFOs, Australia, Q4" autoFocus />
          </Labeled>
          <Labeled label="Goal" hint="What success looks like, in words the agent will read.">
            <textarea className={`${field} w-full`} rows={2} value={goal} onChange={(e) => setGoal(e.target.value)} placeholder="Book 10 meetings with CFOs at Australian fintechs with 50 to 500 staff." />
          </Labeled>
          <Labeled label="Track a number" hint="Optional. Progress shows on this page and to the agent.">
            <div className="flex gap-2">
              <input className={`${field} w-24`} inputMode="numeric" aria-label="Target" placeholder="10" value={target} onChange={(e) => setTarget(e.target.value.replace(/\D/g, ""))} />
              <select className={field} aria-label="Metric" value={metric} onChange={(e) => setMetric(e.target.value)}>
                {Object.entries(metricLabel).map(([k, v]) => <option key={k} value={k}>{v}</option>)}
              </select>
            </div>
          </Labeled>
          <Labeled label="Instructions" hint="On top of the project brief: what to avoid, who to exclude, tone. Markdown.">
            <textarea className={`${field} w-full font-mono text-[12px]`} rows={4} value={instructions} onChange={(e) => setInstructions(e.target.value)} placeholder="- Skip anyone we emailed in the last 90 days&#10;- No more than 3 steps" />
          </Labeled>
          <fieldset className="space-y-3 rounded-lg border p-3" disabled={limitsLocked}>
            <legend className="px-1 text-[12px] font-medium">Limits {limitsLocked && <span className="font-normal text-muted-foreground">(need approval rights to change)</span>}</legend>
            <Labeled label="Agent" hint="Only this API token may act in the mission. Leave empty to allow any token with missions.run.">
              <select className={`${field} w-full`} value={tokenId} onChange={(e) => setTokenId(e.target.value)}>
                <option value="">Any agent token</option>
                {tokens.map((t) => <option key={t.id} value={t.id}>{t.name}</option>)}
              </select>
            </Labeled>
            <div className="grid gap-3 sm:grid-cols-3">
              <Labeled label="Most emails">
                <input className={`${field} w-full`} inputMode="numeric" placeholder="No limit" value={maxSends} onChange={(e) => setMaxSends(e.target.value.replace(/\D/g, ""))} />
              </Labeled>
              <Labeled label="Most spend (USD)">
                <input className={`${field} w-full`} inputMode="decimal" placeholder="No limit" value={maxSpend} onChange={(e) => setMaxSpend(e.target.value.replace(/[^\d.]/g, ""))} />
              </Labeled>
              <Labeled label="Ends on">
                <input className={`${field} w-full`} type="date" value={endsAt} min={new Date().toISOString().slice(0, 10)} onChange={(e) => setEndsAt(e.target.value)} />
              </Labeled>
            </div>
            <p className="text-[12px] text-muted-foreground">These apply on top of the token's own daily send budget. Spend counts what paid connectors record in the spend ledger.</p>
          </fieldset>
          <ErrorText>{error}</ErrorText>
        </form>
        <DialogFooter>
          <Button variant="ghost" type="button" onClick={onClose}>Cancel</Button>
          <Button type="submit" form="mission-form" disabled={busy || !name.trim()}>{busy ? "Saving…" : mission ? "Save" : "Create draft"}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function Labeled({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <label className="block">
      <span className="mb-1 block text-[13px] font-medium">{label}</span>
      {children}
      {hint && <span className="mt-1 block text-[12px] text-muted-foreground">{hint}</span>}
    </label>
  );
}

type Runner = Mission["runner"];

const runners: { value: Runner; label: string; help: string }[] = [
  { value: "builtin", label: "Outflare's agent", help: "Your AI model carries out the plan with your key, in turns. It answers your notes within a minute and looks again when it says it will. Email replies it writes always wait for you." },
  { value: "external", label: "Your own agent", help: "An agent you run, such as Claude Code over MCP or a script, acts in the mission by sending its header." },
];

function ApproveDialog({ ws, m, onClose, onDone }: { ws: string; m: Mission; onClose: () => void; onDone: (m: Mission) => void }) {
  const [mode, setMode] = useState<MissionMode>(m.mode === "dry_run" ? "approve_writes" : m.mode);
  // A mission an outside agent started stays with it; one a person started goes to Outflare's agent.
  const [runner, setRunner] = useState<Runner>(m.runner === "builtin" || (!m.started_by_agent && !m.token_id) ? "builtin" : "external");
  const [note, setNote] = useState("");
  const [error, setError] = useState("");
  const [block, setBlock] = useState<AIBlock>(null);
  const [busy, setBusy] = useState(false);
  async function approve() {
    setBusy(true);
    setError("");
    setBlock(null);
    const { data, error, response } = await api.POST("/v1/workspaces/{ws}/missions/{id}/approve", { params: { path: { ws, id: m.id } }, body: { mode, runner, note: note.trim() || undefined } });
    setBusy(false);
    const b = aiBlock(response.status, error);
    if (b) return setBlock(b);
    if (error || !data) return setError(errorMessage(error));
    toast(runner === "builtin" ? "Mission approved. Outflare's agent is starting on it." : "Mission approved and running.");
    onDone(data);
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Approve “{m.name}”</DialogTitle>
          <DialogDescription>Choose who runs it and how its changes run. You can pause or stop it at any time.</DialogDescription>
        </DialogHeader>
        <p className="text-[12px] font-medium text-muted-foreground">Who runs it</p>
        <div className="grid gap-2 sm:grid-cols-2" role="radiogroup" aria-label="Who runs it">
          {runners.map((o) => (
            <label key={o.value} className={`flex cursor-pointer gap-3 rounded-lg border p-3 text-[13px] ${runner === o.value ? "border-ring bg-accent/50" : ""}`}>
              <input type="radio" name="runner" className="mt-0.5" checked={runner === o.value} onChange={() => setRunner(o.value)} />
              <span>
                <span className="font-medium">{o.label}</span>
                <span className="block text-muted-foreground">{o.help}</span>
              </span>
            </label>
          ))}
        </div>
        <AINeeded ws={ws} block={block} />
        <p className="text-[12px] font-medium text-muted-foreground">How its changes run</p>
        <div className="space-y-2" role="radiogroup" aria-label="How changes run">
          {modes.map((o) => (
            <label key={o.value} className={`flex cursor-pointer gap-3 rounded-lg border p-3 text-[13px] ${mode === o.value ? "border-ring bg-accent/50" : ""}`}>
              <input type="radio" name="mode" className="mt-0.5" checked={mode === o.value} onChange={() => setMode(o.value)} />
              <span>
                <span className="font-medium">{o.label}</span>
                {o.value === "approve_writes" && <span className="ml-1.5 text-[11px] text-muted-foreground">recommended to start</span>}
                <span className="block text-muted-foreground">{o.help}</span>
              </span>
            </label>
          ))}
        </div>
        <Limits m={m} />
        <textarea className={`${field} w-full`} rows={2} placeholder="Note for the agent (optional)" value={note} onChange={(e) => setNote(e.target.value)} />
        <ErrorText>{error}</ErrorText>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button onClick={approve} disabled={busy}>{busy ? "Approving…" : "Approve and start"}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function Limits({ m }: { m: Mission }) {
  const rows: [string, string][] = [
    ["Agent", m.runner === "builtin" ? "Outflare's agent" : m.token_name ?? (m.status === "draft" || m.status === "pending_approval" ? "Chosen when you approve" : "Any agent token")],
    ["Email limit", m.limits.max_sends ? `${m.progress.sends.toLocaleString()} of ${m.limits.max_sends.toLocaleString()} sent` : "None set (the agent's token limits apply)"],
    ["Spend limit", m.limits.max_spend_cents != null ? `${money(m.progress.spend_cents)} of ${money(m.limits.max_spend_cents)} spent` : "None set"],
    ["Ends", m.limits.ends_at ? endsAt(m.limits.ends_at) : "No end date"],
  ];
  return (
    <dl className="grid gap-x-4 gap-y-1.5 text-[13px] sm:grid-cols-[8rem_1fr]">
      {rows.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="text-muted-foreground">{k}</dt>
          <dd>{v}</dd>
        </div>
      ))}
    </dl>
  );
}

// ---- Detail ----

function MissionDetail({ ws, id, canManage, onBack }: { ws: string; id: string; canManage: boolean; onBack: () => void }) {
  const [d, setD] = useState<Detail | null>(null);
  const [missing, setMissing] = useState(false);
  const [editing, setEditing] = useState(false);
  const [approving, setApproving] = useState(false);
  const [refresh, setRefresh] = useState(0);
  const path = { ws, id };

  const load = useCallback(async () => {
    const { data, response } = await api.GET("/v1/workspaces/{ws}/missions/{id}", { params: { path } });
    if (response.status === 404) return setMissing(true);
    if (data) setD(data as Detail);
    setRefresh((n) => n + 1);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, id]);
  useEffect(() => {
    load();
    const t = setInterval(load, 10000);
    return () => clearInterval(t);
  }, [load]);

  // Show the mission's name, not its id, in the top bar.
  const name = d?.mission.name;
  useEffect(() => {
    if (name) window.dispatchEvent(new CustomEvent("outflare:item-name", { detail: { id, name } }));
  }, [id, name]);

  if (missing) return <Empty title="Mission not found" action={<Button variant="ghost" onClick={onBack}>All missions</Button>}>It may be in another workspace.</Empty>;
  if (!d) return <Loading />;
  const m = d.mission;

  async function act(path_: "/pause" | "/resume" | "/stop", label: string, body?: Record<string, string>) {
    const url = `/v1/workspaces/{ws}/missions/{id}${path_}` as "/v1/workspaces/{ws}/missions/{id}/pause";
    const { error } = await api.POST(url, { params: { path }, body: body as { reason?: string } });
    if (error) return toast(errorMessage(error), "error");
    toast(label);
    load();
  }
  async function stop() {
    if (!(await confirmAction(`Stop “${m.name}”?`, "The mission ends now. Changes waiting for approval are canceled and its sequences are paused. You can restart a sequence yourself, but not the mission.", "Stop mission"))) return;
    act("/stop", "Mission stopped.", { reason: "Stopped from the dashboard." });
  }

  const live = isLive(m);
  const actions = canManage && (
    <>
      {(m.status === "pending_approval" || m.status === "draft") && <Button onClick={() => setApproving(true)}>{m.status === "draft" ? "Approve without a plan" : "Review and approve"}</Button>}
      {m.status === "running" && <Button variant="ghost" onClick={() => act("/pause", "Mission paused. Its sequences stop sending.", { reason: "Paused from the dashboard." })}>Pause</Button>}
      {m.status === "paused" && <Button onClick={() => act("/resume", "Mission resumed.")}>Resume</Button>}
      {live && <Button variant="ghost" onClick={() => setEditing(true)}>Edit</Button>}
      {live && <Button variant="danger" onClick={stop}>Stop</Button>}
    </>
  );

  return (
    <div className="space-y-6">
      <PageHeader
        back={{ label: "All missions", onClick: onBack }}
        title={<>{m.name} <Badge tone={statusTone[m.status]}>{statusLabel[m.status]}</Badge></>}
        description={m.goal.text || undefined}
        action={actions}
      />

      <div className="flex flex-wrap items-center gap-x-4 gap-y-1 rounded-lg border bg-card px-4 py-2.5 text-[13px] shadow-xs">
        <Status tone={m.writes === "run" ? "green" : m.writes === "refused" ? "red" : "amber"}>{writesText[m.writes]}</Status>
        {m.status_reason && m.status !== "running" && <span className="text-muted-foreground">{statusLabel[m.status]}: {m.status_reason}</span>}
        {m.started_by_agent && <Badge tone="blue">started by agent</Badge>}
        {m.runner === "builtin" && m.status === "running" && (
          <span className="text-muted-foreground">
            Outflare's agent {m.turns.next_at ? (new Date(m.turns.next_at).getTime() <= Date.now() + 60000 ? "is working on it now" : `looks again ${when(m.turns.next_at)}`) : "is waiting for you"}
          </span>
        )}
      </div>

      <Stats cols={5}>
        <Stat label="Goal" value={m.goal.target ? `${m.goal.current}/${m.goal.target}` : "—"} sub={m.goal.metric ? metricLabel[m.goal.metric] : "no number set"} tone={m.goal.reached ? "green" : undefined} />
        <Stat label="Emails sent" value={m.progress.sends.toLocaleString()} sub={m.limits.max_sends ? `limit ${m.limits.max_sends.toLocaleString()}` : "no mission limit"} tone={m.limits.max_sends && m.progress.sends >= m.limits.max_sends ? "amber" : undefined} />
        <Stat label="Replies" value={m.progress.replies.toLocaleString()} sub={m.progress.sends ? `from ${m.progress.sends.toLocaleString()} emails` : "no emails sent yet"} />
        <Stat label="Meetings" value={m.progress.meetings.toLocaleString()} sub={`${plural(m.progress.leads, "lead")} in its campaigns`} />
        <Stat label="Spend" value={money(m.progress.spend_cents)} sub={m.limits.max_spend_cents != null ? `limit ${money(m.limits.max_spend_cents)}` : "no mission limit"} />
      </Stats>

      <NeedsYou ws={ws} d={d} canManage={canManage} refresh={refresh} onChange={load} />

      <div className="grid gap-6 lg:grid-cols-[1fr_20rem]">
        <Tabs defaultValue="activity" className="min-w-0">
          <TabsList>
            <TabsTrigger value="activity">Activity</TabsTrigger>
            <TabsTrigger value="plan">Plan and brief</TabsTrigger>
            <TabsTrigger value="calls">Calls ({m.calls.toLocaleString()})</TabsTrigger>
            <TabsTrigger value="changes">Changes</TabsTrigger>
          </TabsList>
          <TabsContent value="activity" className="mt-4">
            <Activity ws={ws} m={m} updates={d.updates} canManage={canManage && live} onPosted={load} />
          </TabsContent>
          <TabsContent value="plan" className="mt-4 space-y-4">
            <Card title="Plan" description={m.plan_updated_at ? `Updated ${ago(m.plan_updated_at)}` : "The agent has not written one yet."}>
              {m.plan ? <Prose text={m.plan} /> : <p className="text-[13px] text-muted-foreground">An agent submits its plan with submit_mission_plan before you approve.</p>}
            </Card>
            {m.instructions && <Card title="Instructions"><Prose text={m.instructions} /></Card>}
            <Card title="Project brief" description={d.brief ? `${d.brief.name}, version ${d.brief.version}` : undefined}>
              {d.brief ? (
                <div className="space-y-3 text-[13px]">
                  {d.brief.changed_since_approval && <p className="rounded border border-amber-500/30 bg-amber-500/10 px-2 py-1 text-amber-800 dark:text-amber-300">The brief changed after this mission was approved (approved against version {m.brief_version}).</p>}
                  {([["Offer", d.brief.offer], ["Audience", d.brief.audience], ["Market", d.brief.market]] as const).filter(([, v]) => v).map(([k, v]) => (
                    <div key={k}><div className="text-[12px] text-muted-foreground">{k}</div><div>{v}</div></div>
                  ))}
                  {d.brief.brief && <Prose text={d.brief.brief} />}
                </div>
              ) : (
                <p className="text-[13px] text-muted-foreground">No project is set, so the agent works from the goal and instructions only.</p>
              )}
            </Card>
          </TabsContent>
          <TabsContent value="calls" className="mt-4">
            <Calls ws={ws} id={id} refresh={refresh} />
          </TabsContent>
          <TabsContent value="changes" className="mt-4">
            <Changes ws={ws} id={id} refresh={refresh} />
          </TabsContent>
        </Tabs>
        <div className="space-y-4">
          <Card title="Limits">
            <Limits m={m} />
          </Card>
          {m.runner === "builtin" ? (
            <Card title="Outflare's agent">
              <div className="space-y-2 text-[13px]">
                {m.turns.error && <p className="rounded border border-amber-500/30 bg-amber-500/10 px-2 py-1 text-amber-800 dark:text-amber-300">The last turn failed: {m.turns.error}</p>}
                {m.turns.summary ? <p>{m.turns.summary}</p> : <p className="text-muted-foreground">It hasn't taken a turn yet.</p>}
                <dl className="space-y-1 text-[12px]">
                  <div className="flex justify-between gap-2"><dt className="text-muted-foreground">Turns</dt><dd>{m.turns.count.toLocaleString()}</dd></div>
                  {m.turns.last_at && <div className="flex justify-between gap-2"><dt className="text-muted-foreground">Last turn</dt><dd title={stamp(m.turns.last_at)}>{ago(m.turns.last_at)}</dd></div>}
                  {m.turns.next_at && m.status === "running" && <div className="flex justify-between gap-2"><dt className="text-muted-foreground">Next turn</dt><dd>{when(m.turns.next_at)}</dd></div>}
                </dl>
                <p className="text-[12px] text-muted-foreground">Write to it under Activity and it answers within a minute.</p>
              </div>
            </Card>
          ) : (
          <Card title="For the agent">
            <div className="space-y-2 text-[12px]">
              <p className="text-muted-foreground">Calls with this header act in the mission:</p>
              <div className="flex items-center gap-1 rounded border bg-muted/40 px-2 py-1 font-mono">
                <span className="truncate">{d.how_to_act.header}</span>
                <CopyButton text={d.how_to_act.header} label="Copy header" />
              </div>
              <p className="text-muted-foreground">Over MCP: <span className="font-mono">use_mission</span> with id <span className="font-mono">{m.id.slice(0, 8)}…</span></p>
            </div>
          </Card>
          )}
          <Card title="History">
            <dl className="space-y-1 text-[12px]">
              <HistoryRow k="Created" v={m.created_at} who={m.created_by_name} />
              {m.approved_at && <HistoryRow k="Approved" v={m.approved_at} who={m.approved_by_name} />}
              {m.ended_at && <HistoryRow k="Ended" v={m.ended_at} />}
            </dl>
          </Card>
        </div>
      </div>
      {m.report && (
        <Card title="Report">
          <Prose text={m.report} />
        </Card>
      )}
      {editing && <MissionForm ws={ws} mission={m} canApprove={canManage} onClose={() => setEditing(false)} onSaved={() => { setEditing(false); load(); }} />}
      {approving && <ApproveDialog ws={ws} m={m} onClose={() => setApproving(false)} onDone={() => { setApproving(false); load(); }} />}
    </div>
  );
}

function HistoryRow({ k, v, who }: { k: string; v: string; who?: string | null }) {
  return (
    <div className="flex justify-between gap-2">
      <dt className="text-muted-foreground">{k}</dt>
      <dd className="text-right" title={stamp(v)}>{ago(v)}{who && <span className="text-muted-foreground"> by {who}</span>}</dd>
    </div>
  );
}

// Plans and notes are Markdown; show **bold** and `code` and keep the rest as written.
function Prose({ text }: { text: string }) {
  const parts = text.split(/(\*\*[^*\n]+\*\*|`[^`\n]+`)/g);
  return (
    <div className="text-[13px] leading-relaxed whitespace-pre-wrap">
      {parts.map((p, i) =>
        p.startsWith("**") && p.endsWith("**") && p.length > 4 ? <strong key={i} className="font-medium">{p.slice(2, -2)}</strong>
        : p.startsWith("`") && p.endsWith("`") && p.length > 2 ? <code key={i} className="rounded bg-muted px-1 font-mono text-[12px]">{p.slice(1, -1)}</code>
        : p,
      )}
    </div>
  );
}

// What the person needs to do now: held changes and connect requests.
function NeedsYou({ ws, d, canManage, refresh, onChange }: { ws: string; d: Detail; canManage: boolean; refresh: number; onChange: () => void }) {
  const m = d.mission;
  const [pending, setPending] = useState<MissionAction[]>([]);
  useEffect(() => {
    if (m.pending_actions === 0) return setPending([]);
    api.GET("/v1/workspaces/{ws}/missions/{id}/actions", { params: { path: { ws, id: m.id }, query: { status: "pending" } } }).then(({ data }) => setPending(data?.data ?? []));
  }, [ws, m.id, m.pending_actions, refresh]);

  const requests = d.open_connect_requests;
  // The agent's latest question, unless a person wrote after it.
  const lastPerson = d.updates.find((u) => u.author === "person");
  const question = d.updates.find((u) => u.author === "agent" && u.kind === "question" && (!lastPerson || u.id > lastPerson.id));
  if (pending.length === 0 && requests.length === 0 && !question && m.status !== "pending_approval") return null;
  return (
    <Card title="Needs you" description="The agent is waiting on these.">
      <div className="space-y-3">
        {m.status === "pending_approval" && (
          <p className="text-[13px]">The plan is waiting for your approval. Read it under <span className="font-medium">Plan and brief</span>, then use <span className="font-medium">Review and approve</span> above.</p>
        )}
        {question && <Question ws={ws} missionId={m.id} q={question} canAnswer={canManage && isLive(m)} onAnswered={onChange} />}
        {requests.map((r) => (
          <ConnectRequest key={r.id} ws={ws} missionId={m.id} r={r} canManage={canManage} onDone={onChange} />
        ))}
        {pending.map((a) => (
          <HeldChange key={a.id} ws={ws} a={a} canManage={canManage} onDone={onChange} />
        ))}
      </div>
    </Card>
  );
}

function Question({ ws, missionId, q, canAnswer, onAnswered }: { ws: string; missionId: string; q: MissionUpdate; canAnswer: boolean; onAnswered: () => void }) {
  const [body, setBody] = useState("");
  const [busy, setBusy] = useState(false);
  async function answer(e: FormEvent) {
    e.preventDefault();
    if (!body.trim()) return;
    setBusy(true);
    const { error } = await api.POST("/v1/workspaces/{ws}/missions/{id}/updates", { params: { path: { ws, id: missionId } }, body: { kind: "instruction", body: body.trim() } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    setBody("");
    toast("Answer sent. The agent reads it at its next check-in.");
    onAnswered();
  }
  return (
    <div className="rounded-lg border p-3 text-[13px]">
      <div className="font-medium">The agent asked</div>
      <p>{q.body}</p>
      <p className="mt-0.5 text-[12px] text-muted-foreground">{ago(q.created_at)}</p>
      {canAnswer && (
        <form onSubmit={answer} className="mt-2 flex flex-col gap-2 sm:flex-row">
          <input className={`${field} min-w-0 flex-1`} placeholder="Your answer" value={body} onChange={(e) => setBody(e.target.value)} aria-label="Answer to the agent" />
          <Button type="submit" disabled={busy || !body.trim()}>Answer</Button>
        </form>
      )}
    </div>
  );
}

function ConnectRequest({ ws, missionId, r, canManage, onDone }: { ws: string; missionId: string; r: MissionUpdate; canManage: boolean; onDone: () => void }) {
  const data = (r.data ?? {}) as Record<string, unknown>;
  const connector = String(data.connector ?? "");
  const connectorName = String(data.connector_name ?? connector.replace(/^./, (c) => c.toUpperCase()));
  const [link, setLink] = useState("");
  async function resolve() {
    const { error } = await api.POST("/v1/workspaces/{ws}/missions/{id}/updates/{update}/resolve", { params: { path: { ws, id: missionId, update: r.id } }, body: { note: "Not needed." } });
    if (error) return toast(errorMessage(error), "error");
    toast("Closed. The agent will carry on without it.");
    onDone();
  }
  // A one-time link for whoever holds the key: no Outflare account needed.
  async function sendLink() {
    const { data, error } = await api.POST("/v1/workspaces/{ws}/connect-requests", { params: { path: { ws } }, body: { connector, reason: r.body.slice(0, 500) } });
    if (error || !data?.url) return toast(errorMessage(error), "error");
    setLink(data.url);
    try {
      await navigator.clipboard.writeText(data.url);
      toast("Link copied. Send it to whoever has the key.");
    } catch {
      toast("Link ready. Copy it below.");
    }
  }
  return (
    <div className="flex flex-wrap items-start justify-between gap-3 rounded-lg border p-3">
      <div className="flex min-w-0 gap-3 text-[13px]">
        {connector && <ConnectorLogo id={connector} />}
        <div className="min-w-0">
          <div className="font-medium">Connect {connectorName}</div>
          <p className="text-muted-foreground">{r.body}</p>
          <p className="mt-0.5 text-[12px] text-muted-foreground">Asked {ago(r.created_at)}. Closes by itself once {connectorName} is connected.</p>
          {link && (
            <div className="mt-2 flex items-center gap-1 rounded border bg-muted/40 px-2 py-1 font-mono text-[12px]">
              <span className="truncate">{link}</span>
              <CopyButton text={link} label="Copy link" />
            </div>
          )}
          {link && <p className="mt-1 text-[12px] text-muted-foreground">Works once, for 24 hours, without an Outflare account.</p>}
        </div>
      </div>
      {canManage && (
        <div className="flex flex-wrap gap-2">
          <a href={appPath(ws, "connectors", connector)} target="_blank" rel="noreferrer">
            <Button type="button">Connect</Button>
          </a>
          <Button variant="ghost" onClick={sendLink}>Send a link</Button>
          <Button variant="ghost" onClick={resolve}>Not needed</Button>
        </div>
      )}
    </div>
  );
}

function HeldChange({ ws, a, canManage, onDone }: { ws: string; a: MissionAction; canManage: boolean; onDone: () => void }) {
  const [busy, setBusy] = useState(false);
  async function decide(approve: boolean) {
    let note = "";
    if (!approve) {
      const r = await ask({ title: "Reject this change?", body: "The agent sees your reason as an instruction.", input: { label: "Reason", placeholder: "e.g. Use a softer subject line" }, choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Reject", value: "ok", variant: "danger-solid" }] });
      if (r?.choice !== "ok") return;
      note = r.value;
    }
    setBusy(true);
    const p = { params: { path: { ws, id: a.mission_id, action: a.id } }, body: { note } };
    const { data, error } = approve
      ? await api.POST("/v1/workspaces/{ws}/missions/{id}/actions/{action}/approve", p)
      : await api.POST("/v1/workspaces/{ws}/missions/{id}/actions/{action}/reject", p);
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    if (approve) toast(data?.status === "executed" ? "Approved and done." : `Approved, but it failed: ${resultMessage(data)}`, data?.status === "executed" ? "ok" : "error");
    else toast("Rejected. The agent will see your reason.");
    onDone();
  }
  return (
    <div className="rounded-lg border p-3">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 text-[13px]">
          <div className="font-medium">{a.summary}</div>
          {a.reason && <p className="text-muted-foreground">“{a.reason}”</p>}
          <p className="mt-0.5 font-mono text-[11px] text-muted-foreground">{a.method} {a.path.replace(/^\/v1\/workspaces\/[^/]+/, "")} · {ago(a.created_at)}{a.actor_name && ` · ${a.actor_name}`}</p>
        </div>
        {canManage && (
          <div className="flex gap-2">
            <Button disabled={busy} onClick={() => decide(true)}>Approve</Button>
            <Button disabled={busy} variant="ghost" onClick={() => decide(false)}>Reject</Button>
          </div>
        )}
      </div>
      {a.body !== undefined && a.body !== null && (
        <>
          <ChangePreview body={a.body} />
          <details className="mt-2">
            <summary className="cursor-pointer text-[12px] text-muted-foreground">Exact request</summary>
            <pre className="mt-1 max-h-72 overflow-auto rounded border bg-muted/40 p-2 text-[11px]"><code>{JSON.stringify(a.body, null, 2)}</code></pre>
          </details>
        </>
      )}
    </div>
  );
}

type StepBody = { subject?: string; body?: string; delay_days?: number };

// A readable view of what a held change will do: the emails of a sequence,
// how many leads it adds, and its other settings.
function ChangePreview({ body }: { body: unknown }) {
  if (typeof body !== "object" || body === null || Array.isArray(body)) return null;
  const b = body as Record<string, unknown>;
  const steps = Array.isArray(b.steps) ? (b.steps as StepBody[]) : [];
  const lists = Object.entries(b).filter(([, v]) => Array.isArray(v) && v !== b.steps) as [string, unknown[]][];
  const fields = Object.entries(b).filter(([, v]) => v !== null && ["string", "number", "boolean"].includes(typeof v));
  if (steps.length === 0 && lists.length === 0 && fields.length === 0) return null;
  const label = (k: string) => k.replace(/_/g, " ").replace(/^./, (c) => c.toUpperCase());
  return (
    <div className="mt-3 space-y-3 rounded-md border bg-muted/30 p-3 text-[13px]">
      {(fields.length > 0 || lists.length > 0) && (
        <dl className="grid gap-x-4 gap-y-1 sm:grid-cols-[9rem_1fr]">
          {fields.map(([k, v]) => (
            <div key={k} className="contents">
              <dt className="text-muted-foreground">{label(k)}</dt>
              <dd className="min-w-0 break-words">{String(v)}</dd>
            </div>
          ))}
          {lists.map(([k, v]) => (
            <div key={k} className="contents">
              <dt className="text-muted-foreground">{label(k)}</dt>
              <dd>{plural(v.length, "item")}</dd>
            </div>
          ))}
        </dl>
      )}
      {steps.length > 0 && (
        <ol className="space-y-2">
          {steps.map((st, i) => (
            <li key={i} className="rounded border bg-background p-2">
              <div className="text-[12px] text-muted-foreground">
                Email {i + 1}
                {i > 0 && ` · ${st.delay_days ? `${plural(st.delay_days, "day")} after the last` : "same day"}`}
                {i > 0 && !st.subject && " · replies in the same thread"}
              </div>
              {st.subject && <div className="font-medium">{st.subject}</div>}
              {st.body && <p className="mt-1 whitespace-pre-wrap text-muted-foreground">{st.body}</p>}
            </li>
          ))}
        </ol>
      )}
    </div>
  );
}

function resultMessage(a?: MissionAction): string {
  const r = a?.result as { error?: { message?: string } } | undefined;
  return r?.error?.message ?? `status ${a?.result_status ?? "unknown"}`;
}

function Activity({ ws, m, updates, canManage, onPosted }: { ws: string; m: Mission; updates: MissionUpdate[]; canManage: boolean; onPosted: () => void }) {
  const [body, setBody] = useState("");
  const [busy, setBusy] = useState(false);
  async function post(e: FormEvent) {
    e.preventDefault();
    if (!body.trim()) return;
    setBusy(true);
    const { error } = await api.POST("/v1/workspaces/{ws}/missions/{id}/updates", { params: { path: { ws, id: m.id } }, body: { kind: "instruction", body: body.trim() } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    setBody("");
    toast("Sent. The agent reads it at its next check-in.");
    onPosted();
  }
  const kindTone = { question: "amber", instruction: "blue", report: "green", connect_request: "amber", progress: "grey", note: "grey" } as const;
  const kindLabel = { question: "question", instruction: "instruction", report: "report", connect_request: "connect request", progress: "progress", note: "note" } as const;
  return (
    <div className="space-y-4">
      {canManage && (
        <form onSubmit={post} className="space-y-2">
          <textarea className={`${field} w-full`} rows={2} placeholder="Answer a question or tell the agent something, e.g. Focus on Sydney first" value={body} onChange={(e) => setBody(e.target.value)} aria-label="Message to the agent" />
          <div className="flex justify-end"><Button type="submit" disabled={busy || !body.trim()}>Send to agent</Button></div>
        </form>
      )}
      {updates.length === 0 ? (
        <p className="py-6 text-center text-[13px] text-muted-foreground">No updates yet. The agent posts progress and questions here.</p>
      ) : (
        <ol className="space-y-3">
          {updates.map((u) => (
            <li key={u.id} className={`rounded-lg border p-3 ${u.author === "system" ? "border-dashed bg-transparent" : "bg-card"}`}>
              <div className="flex flex-wrap items-center gap-2 text-[12px]">
                <span className="font-medium">{u.author === "system" ? "Outflare" : u.data?.planner ? "AI model, planning" : u.actor_name ?? u.author}</span>
                {u.author === "agent" && <Badge tone="blue">agent</Badge>}
                {u.kind !== "note" && <Badge tone={kindTone[u.kind]}>{kindLabel[u.kind]}</Badge>}
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

const outcomeTone = { executed: "green", simulated: "grey", queued: "amber", refused: "red" } as const;
const outcomeLabel = { executed: "ran", simulated: "dry run", queued: "held", refused: "refused" } as const;

function Calls({ ws, id, refresh }: { ws: string; id: string; refresh: number }) {
  const [calls, setCalls] = useState<MissionCall[] | null>(null);
  const [writes, setWrites] = useState(false);
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/missions/{id}/calls", { params: { path: { ws, id }, query: { limit: 200, writes } } }).then(({ data }) => setCalls(data?.data ?? []));
  }, [ws, id, writes, refresh]);
  if (!calls) return <Loading />;
  return (
    <DataTable
      data={calls}
      rowId={(c) => String(c.id)}
      columnsMenu={false}
      searchPlaceholder="Filter calls"
      empty="No calls yet. Every call the agent makes in this mission shows here."
      toolbar={
        <label className="inline-flex items-center gap-1.5 text-[12px]">
          <input type="checkbox" checked={writes} onChange={(e) => setWrites(e.target.checked)} /> Changes only
        </label>
      }
      columns={[
        { id: "time", header: "Time", accessorFn: (c) => c.created_at, meta: { mono: true }, cell: ({ row: { original: c } }) => <span className="whitespace-nowrap text-muted-foreground" title={stamp(c.created_at)}>{ago(c.created_at)}</span> },
        { id: "outcome", header: "Result", accessorFn: (c) => c.outcome, cell: ({ row: { original: c } }) => <Badge tone={outcomeTone[c.outcome]}>{outcomeLabel[c.outcome]}</Badge> },
        {
          id: "call",
          header: "Call",
          accessorFn: (c) => `${c.method} ${c.path}`,
          cell: ({ row: { original: c } }) => (
            <span className="font-mono text-[12px]">
              <span className={c.method === "GET" ? "text-muted-foreground" : "font-medium"}>{c.method}</span> {c.path.replace(/^\/v1\/workspaces\/[^/]+/, "")}{c.query && <span className="text-muted-foreground">?{c.query}</span>}
            </span>
          ),
        },
        { id: "status", header: "Status", accessorFn: (c) => c.status_code, meta: { align: "right", mono: true }, cell: ({ row: { original: c } }) => <span className={c.status_code >= 400 ? "text-red-600 dark:text-red-400" : ""}>{c.status_code}</span> },
        { id: "reason", header: "Why", accessorFn: (c) => c.reason, cell: ({ row: { original: c } }) => <span className="line-clamp-2 max-w-72 text-[12px] text-muted-foreground">{c.reason || "—"}</span> },
      ]}
      mobile={(c) => (
        <div className="space-y-1 text-[12px]">
          <div className="flex items-center justify-between gap-2"><Badge tone={outcomeTone[c.outcome]}>{outcomeLabel[c.outcome]}</Badge><span className="text-muted-foreground">{ago(c.created_at)}</span></div>
          <div className="font-mono break-all">{c.method} {c.path.replace(/^\/v1\/workspaces\/[^/]+/, "")}</div>
          {c.reason && <div className="text-muted-foreground">{c.reason}</div>}
        </div>
      )}
    />
  );
}

const actionTone = { simulated: "grey", pending: "amber", executed: "green", failed: "red", rejected: "red", canceled: "grey" } as const;
const actionLabel = { simulated: "dry run", pending: "waiting", executed: "done", failed: "failed", rejected: "rejected", canceled: "canceled" } as const;

function Changes({ ws, id, refresh }: { ws: string; id: string; refresh: number }) {
  const [list, setList] = useState<MissionAction[] | null>(null);
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/missions/{id}/actions", { params: { path: { ws, id } } }).then(({ data }) => setList(data?.data ?? []));
  }, [ws, id, refresh]);
  if (!list) return <Loading />;
  if (list.length === 0) return <p className="py-6 text-center text-[13px] text-muted-foreground">No held changes or dry runs. Changes that ran straight away are under Calls.</p>;
  return (
    <ol className="space-y-2">
      {list.map((a) => (
        <li key={a.id} className="rounded-lg border p-3 text-[13px]">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <span className="font-medium">{a.summary}</span>
            <Badge tone={actionTone[a.status]}>{actionLabel[a.status]}</Badge>
          </div>
          {a.reason && <p className="text-muted-foreground">“{a.reason}”</p>}
          <p className="mt-0.5 font-mono text-[11px] text-muted-foreground">
            {a.method} {a.path.replace(/^\/v1\/workspaces\/[^/]+/, "")} · {ago(a.created_at)}
            {a.decided_by_name && ` · decided by ${a.decided_by_name}`}
          </p>
          {a.decision_note && <p className="mt-1 text-[12px]">Note: {a.decision_note}</p>}
          {a.status === "failed" && <p className="mt-1 text-[12px] text-red-600 dark:text-red-400">{resultMessage(a)}</p>}
        </li>
      ))}
    </ol>
  );
}
