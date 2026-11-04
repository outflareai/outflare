import { useCallback, useEffect, useState } from "react";
import type { BrandContext, Mission, MissionAction, MissionCall, MissionUpdate } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { BrandBadge } from "@/components/brand-badge";
import { api } from "../api";
import { Badge, Button, Card, Empty, field, Loading, PageHeader, Stat, Stats } from "../ui";
import { ago, plural, stamp } from "../format";
import { DataTable, type ColumnDef } from "@/components/data-table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import type { Tab } from "../App";

// Missions: goals agents worked toward before agent runs on the action
// layer. Missions are retired: nothing changes one any more. This page is
// the record of what each one did: its goal and progress, every call an
// agent made and why, the changes it held, and the notes on it.

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

const metricLabel = { meetings: "meetings", replies: "replies", sends: "emails sent", leads: "leads contacted", revenue_attributed: "USD revenue from its sequences", revenue_deals: "USD in won deals", revenue_referred: "USD referred by partners" } as const;
const money = (cents: number) => (cents / 100).toLocaleString(undefined, { style: "currency", currency: "USD", maximumFractionDigits: cents % 100 ? 2 : 0 });
// The end date in the viewer's time zone, with the zone named.
const endsAt = (iso: string) => new Date(iso).toLocaleString(undefined, { weekday: "short", day: "numeric", month: "short", year: "numeric", hour: "numeric", minute: "2-digit", timeZoneName: "short" });
const isLive = (m: Mission) => !["completed", "stopped", "failed"].includes(m.status);

type Detail = { mission: Mission; brand?: BrandContext | null; updates: MissionUpdate[]; open_connect_requests: MissionUpdate[]; how_to_act: { header: string; writes: string } };

export function Missions({ ws, open, setOpen, go }: { ws: string; open?: string; setOpen: (id?: string) => void; go: (tab: Tab) => void }) {
  if (open) return <MissionDetail key={open} ws={ws} id={open} onBack={() => setOpen(undefined)} go={go} />;
  return <MissionList ws={ws} onOpen={setOpen} go={go} />;
}

// ---- List ----

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
  { id: "goal", header: "Goal", accessorFn: (m) => m.goal.current, enableSorting: false, cell: ({ row: { original: m } }) => <GoalBar m={m} compact /> },
  { id: "agent", header: "Agent", accessorFn: (m) => m.token_name ?? m.created_by_name ?? "", cell: ({ row: { original: m } }) => <span className="whitespace-nowrap">{m.token_name ?? <span className="text-muted-foreground">Any agent</span>}</span> },
  { id: "last", header: "Last call", accessorFn: (m) => m.last_call_at ?? "", meta: { mono: true }, cell: ({ row: { original: m } }) => <span className="whitespace-nowrap text-muted-foreground">{m.last_call_at ? ago(m.last_call_at) : "never"}</span> },
];

function MissionList({ ws, onOpen, go }: { ws: string; onOpen: (id: string) => void; go: (tab: Tab) => void }) {
  const [list, setList] = useState<Mission[] | null>(null);
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
  const shown = list.filter((m) => filter === "all" || (filter === "live") === isLive(m));

  return (
    <div className="space-y-6">
      <PageHeader
        title="Missions"
        description="Goals your agents worked toward before agent runs: what each one did, every call an agent made and why, and the changes it held."
      />
      <Retired go={go} />
      {list.length === 0 ? (
        <Empty title="No missions">Missions are retired, and this workspace had none. Work runs on the Agents page.</Empty>
      ) : (
        <>
          <Stats cols={3}>
            <Stat label="Sent by missions" value={list.reduce((n, m) => n + m.progress.sends, 0).toLocaleString()} />
            <Stat label="Meetings from missions" value={list.reduce((n, m) => n + m.progress.meetings, 0).toLocaleString()} />
          </Stats>
          <DataTable
            data={shown}
            rowId={(m) => m.id}
            onRowClick={(m) => onOpen(m.id)}
            columns={columns}
            searchPlaceholder="Filter missions"
            empty={filter === "live" ? "No missions were left running or in draft. Ended ones are under All." : "No missions here."}
            toolbar={
              <select className={field} aria-label="Show" value={filter} onChange={(e) => setFilter(e.target.value as typeof filter)}>
                <option value="live">Left running and drafts</option>
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
              </div>
            )}
          />
        </>
      )}
    </div>
  );
}

// Retired says where the work went: missions are retired for agent runs
// on the action layer, and nothing changes one from here.
function Retired({ go }: { go: (tab: Tab) => void }) {
  return (
    <div className="rounded-lg border bg-muted/40 px-4 py-3 text-[13px]">
      <p className="font-medium">Missions are retired.</p>
      <p className="mt-1 text-muted-foreground">
        New work runs as an <button type="button" className="underline underline-offset-2" onClick={() => go("agents")}>agent run</button>: start a playbook from the{" "}
        <button type="button" className="underline underline-offset-2" onClick={() => go("playbooks")}>Playbooks</button> page and Outflare's agent carries it out on your AI model, with every change through the action layer.
        Your own agent works as an API token with the action layer on, with no mission. What missions did stays here as a record; none of them acts or changes any more, including ones left running.
      </p>
    </div>
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

function MissionDetail({ ws, id, onBack, go }: { ws: string; id: string; onBack: () => void; go: (tab: Tab) => void }) {
  const [d, setD] = useState<Detail | null>(null);
  const [missing, setMissing] = useState(false);
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

  return (
    <div className="space-y-6">
      <PageHeader
        back={{ label: "All missions", onClick: onBack }}
        title={<>{m.name} <Badge tone={statusTone[m.status]}>{statusLabel[m.status]}</Badge></>}
        description={m.goal.text || undefined}
      />
      <Retired go={go} />

      {(m.status_reason || m.started_by_agent) && (
        <div className="flex flex-wrap items-center gap-x-4 gap-y-1 rounded-lg border bg-card px-4 py-2.5 text-[13px] shadow-xs">
          {m.status_reason && <span className="text-muted-foreground">{statusLabel[m.status]}: {m.status_reason}</span>}
          {m.started_by_agent && <Badge tone="blue">started by agent</Badge>}
        </div>
      )}

      <Stats cols={5}>
        <Stat label="Goal" value={m.goal.target ? `${m.goal.current}/${m.goal.target}` : "—"} sub={m.goal.metric ? metricLabel[m.goal.metric] : "no number set"} tone={m.goal.reached ? "green" : undefined} />
        <Stat label="Emails sent" value={m.progress.sends.toLocaleString()} sub={m.limits.max_sends ? `limit ${m.limits.max_sends.toLocaleString()}` : "no mission limit"} tone={m.limits.max_sends && m.progress.sends >= m.limits.max_sends ? "amber" : undefined} />
        <Stat label="Replies" value={m.progress.replies.toLocaleString()} sub={m.progress.sends ? `from ${m.progress.sends.toLocaleString()} emails` : "no emails sent yet"} />
        <Stat label="Meetings" value={m.progress.meetings.toLocaleString()} sub={`${plural(m.progress.leads, "lead")} in its campaigns`} />
        <Stat label="Spend" value={money(m.progress.spend_cents)} sub={m.limits.max_spend_cents != null ? `limit ${money(m.limits.max_spend_cents)}` : "no mission limit"} />
      </Stats>

      <div className="grid gap-6 lg:grid-cols-[1fr_20rem]">
        <Tabs defaultValue="activity" className="min-w-0">
          <TabsList>
            <TabsTrigger value="activity">Activity</TabsTrigger>
            <TabsTrigger value="plan">Plan and context</TabsTrigger>
            <TabsTrigger value="calls">Calls ({m.calls.toLocaleString()})</TabsTrigger>
            <TabsTrigger value="changes">Changes</TabsTrigger>
          </TabsList>
          <TabsContent value="activity" className="mt-4">
            <Activity updates={d.updates} />
          </TabsContent>
          <TabsContent value="plan" className="mt-4 space-y-4">
            <Card title="Plan" description={m.plan_updated_at ? `Updated ${ago(m.plan_updated_at)}` : "The agent has not written one yet."}>
              {m.plan ? <Prose text={m.plan} /> : <p className="text-[13px] text-muted-foreground">No plan was written.</p>}
            </Card>
            {m.instructions && <Card title="Instructions"><Prose text={m.instructions} /></Card>}
            {d.brand && (
              <Card title={`${d.brand.name} context`} description="What the agent reads about this mission's brand, on top of the company's.">
                <div className="space-y-3 text-[13px]">
                  {([["About", d.brand.about], ["Audience", d.brand.audience], ["Goals", d.brand.goals]] as const).filter(([, v]) => v).map(([k, v]) => (
                    <div key={k}><div className="text-[12px] text-muted-foreground">{k}</div><div>{v}</div></div>
                  ))}
                  {d.brand.notes && <Prose text={d.brand.notes} />}
                </div>
              </Card>
            )}
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
          {m.runner === "builtin" && (
            <Card title="Outflare's agent">
              <div className="space-y-2 text-[13px]">
                {m.turns.error && <p className="rounded border border-amber-500/30 bg-amber-500/10 px-2 py-1 text-amber-800 dark:text-amber-300">The last turn failed: {m.turns.error}</p>}
                {m.turns.summary ? <p>{m.turns.summary}</p> : <p className="text-muted-foreground">It never took a turn.</p>}
                <dl className="space-y-1 text-[12px]">
                  <div className="flex justify-between gap-2"><dt className="text-muted-foreground">Turns</dt><dd>{m.turns.count.toLocaleString()}</dd></div>
                  {m.turns.last_at && <div className="flex justify-between gap-2"><dt className="text-muted-foreground">Last turn</dt><dd title={stamp(m.turns.last_at)}>{ago(m.turns.last_at)}</dd></div>}
                </dl>
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

function Activity({ updates }: { updates: MissionUpdate[] }) {
  const kindTone = { question: "amber", instruction: "blue", report: "green", connect_request: "amber", progress: "grey", note: "grey" } as const;
  const kindLabel = { question: "question", instruction: "instruction", report: "report", connect_request: "connect request", progress: "progress", note: "note" } as const;
  return (
    <div className="space-y-4">
      {updates.length === 0 ? (
        <p className="py-6 text-center text-[13px] text-muted-foreground">No updates.</p>
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
          {a.status === "failed" && <p className="mt-1 text-[12px] text-red-600 dark:text-red-400">{(a.result as { error?: { message?: string } } | null | undefined)?.error?.message ?? `Failed (status ${a.result_status ?? "unknown"})`}</p>}
        </li>
      ))}
    </ol>
  );
}
