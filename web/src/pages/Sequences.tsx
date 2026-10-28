import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import type { Enrollment, Lead, LeadList, MailboxPool, Sender, Sequence, StepInput, Team } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { BrandBadge } from "@/components/brand-badge";
import { api, errorMessage } from "../api";
import type { Tab } from "../App";
import { ask, Badge, Button, Card, confirmAction, Empty, ErrorText, field, Loading, Menu, PageHeader, Stat, Stats, toast } from "../ui";
import { ago, plural, replyRate, when } from "../format";
import { DataTable, type ColumnDef } from "@/components/data-table";
import { SequenceMailboxes } from "./SequenceMailboxes";

const days = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];
const zones: string[] = (Intl as unknown as { supportedValuesOf?: (k: string) => string[] }).supportedValuesOf?.("timeZone") ?? [];
const variables = ["first_name", "last_name", "company", "title", "website", "sender_first_name", "sender_name", "unsubscribe_url"];

const statusTone = { draft: "grey", active: "green", paused: "grey", archived: "grey" } as const;
const statusLabel = { draft: "Draft", active: "Sending", paused: "Paused", archived: "Archived" } as const;

const sequenceColumns: ColumnDef<Sequence, unknown>[] = [
  {
    id: "name",
    header: "Name",
    accessorFn: (q) => q.name,
    cell: ({ row: { original: q } }) => (
      <>
        <div className="flex items-center gap-1.5"><span className="font-medium group-hover:underline">{q.name}</span> <BrandBadge id={q.brand_id} /></div>
        {(q.links?.length ?? 0) > 0 && <div className="text-xs text-muted-foreground">Runs in {q.links!.map((l) => l.platform).join(", ")}</div>}
      </>
    ),
  },
  { id: "status", header: "Status", accessorFn: (q) => q.status, cell: ({ row: { original: q } }) => <Badge tone={statusTone[q.status]}>{statusLabel[q.status]}</Badge> },
  { id: "steps", header: "Steps", accessorFn: (q) => q.steps.length, meta: { align: "right", mono: true } },
  {
    id: "mailboxes",
    header: "Mailboxes",
    accessorFn: (q) => q.allocation.mailboxes,
    meta: { align: "right", mono: true },
    cell: ({ row: { original: q } }) => {
      const short = q.status === "active" && q.allocation.needed_daily != null && q.allocation.active_daily < q.allocation.needed_daily;
      return <span className={short ? "text-amber-700 dark:text-amber-400" : undefined} title={q.allocation.note || undefined}>{q.allocation.mailboxes}{short && " · short"}</span>;
    },
  },
  { id: "active", header: "Being emailed", accessorFn: (q) => q.stats.active ?? 0, meta: { align: "right", mono: true } },
  { id: "sent", header: "Sent", accessorFn: (q) => q.stats.sent ?? 0, meta: { align: "right", mono: true } },
  {
    id: "replied",
    header: "Replied",
    accessorFn: (q) => q.stats.replied ?? 0,
    meta: { align: "right", mono: true },
    cell: ({ row: { original: q } }) => {
      const rate = replyRate(q.stats.replied ?? 0, q.stats.contacted ?? 0);
      return <>{q.stats.replied ?? 0}{rate && <span className="text-muted-foreground"> · {rate}</span>}</>;
    },
  },
  { id: "updated", header: "Edited", accessorFn: (q) => q.updated_at, cell: ({ row: { original: q } }) => <span className="text-muted-foreground">{ago(q.updated_at)}</span> },
];

type Props = { ws: string; canManage: boolean; open?: string; setOpen: (id: string | undefined) => void; go: (t: Tab) => void };

export function Sequences({ ws, canManage, open, setOpen, go }: Props) {
  const [list, setList] = useState<Sequence[] | null>(null);

  const brand = useBrand(ws).brand?.handle;
  const load = useCallback(async () => {
    const { data } = await api.GET("/v1/workspaces/{ws}/sequences", { params: { path: { ws }, query: { ...(brand ? { brand } : {}) } } });
    if (data) setList(data.data);
  }, [ws, brand]);

  useEffect(() => {
    load();
  }, [load]);

  async function create() {
    const r = await ask({
      title: "New sequence",
      body: <p>It starts as a draft with one step to edit. Nothing is sent until you activate it.</p>,
      input: { label: "Name", placeholder: "e.g. Founders, Q4" },
      choices: [{ label: "Cancel", value: "cancel" }, { label: "Create sequence", value: "create", variant: "primary" }],
    });
    if (!r?.value.trim()) return;
    const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone;
    const { data, error } = await api.POST("/v1/workspaces/{ws}/sequences", {
      params: { path: { ws } },
      body: { name: r.value.trim(), timezone, steps: [{ subject: "Quick question, {{first_name|there}}", body: "Hi {{first_name|there}},\n\n\n\n{{sender_first_name}}" }] },
    });
    if (error) return toast(errorMessage(error), "error");
    await load();
    setOpen(data.id);
  }

  if (list === null) return <Loading />;
  const total = (k: string) => list.reduce((n, q) => n + (q.stats[k] ?? 0), 0);
  const current = open ? list.find((s) => s.id === open) : undefined;
  if (open && current) return <Detail ws={ws} seq={current} canManage={canManage} onBack={() => setOpen(undefined)} onChange={load} go={go} />;
  if (open && !current) {
    return <Empty title="This sequence doesn't exist" action={<Button variant="ghost" onClick={() => setOpen(undefined)}>Back to sequences</Button>}>It may have been deleted or archived.</Empty>;
  }

  return (
    <div className="space-y-6">
      <PageHeader
        title="Sequences"
        description="Multi-step emails that go out from your mailboxes on a schedule, and stop when someone replies."
        action={canManage && list.length > 0 && <Button onClick={create}>New sequence</Button>}
      />
      {list.length === 0 ? (
        <Empty title="No sequences yet" action={canManage && <Button onClick={create}>Create your first sequence</Button>}>
          Write a few short steps, check them against a real lead, add a lead list, then activate.
        </Empty>
      ) : (
        <>
          <Stats>
            <Stat label="Sending" value={list.filter((q) => q.status === "active").length} sub={`of ${plural(list.length, "sequence")}`} tone={list.some((q) => q.status === "active") ? "green" : undefined} />
            <Stat label="Being emailed" value={total("active").toLocaleString()} sub="leads still in a sequence" />
            <Stat label="Sent" value={total("sent").toLocaleString()} />
            <Stat label="Replied" value={total("replied").toLocaleString()} sub={replyRate(total("replied"), total("contacted")) && `${replyRate(total("replied"), total("contacted"))} reply rate`} />
          </Stats>
          <DataTable
            data={list}
            rowId={(q) => q.id}
            onRowClick={(q) => setOpen(q.id)}
            searchPlaceholder="Search sequences"
            initialSort={[{ id: "updated", desc: true }]}
            mobile={(q) => (
              <div className="space-y-1">
                <div className="flex items-start justify-between gap-2">
                  <span className="flex flex-wrap items-center gap-1.5 font-medium">{q.name} <BrandBadge id={q.brand_id} /></span>
                  <Badge tone={statusTone[q.status]}>{statusLabel[q.status]}</Badge>
                </div>
                <div className="font-mono text-xs text-muted-foreground">
                  {plural(q.steps.length, "step")} · {q.stats.active ?? 0} emailing · {q.stats.sent ?? 0} sent · {q.stats.replied ?? 0} replied
                </div>
              </div>
            )}
            columns={sequenceColumns}
          />
        </>
      )}
    </div>
  );
}

function Detail({ ws, seq, canManage, onBack, onChange, go }: { ws: string; seq: Sequence; canManage: boolean; onBack: () => void; onChange: () => void; go: (t: Tab) => void }) {
  const path = { ws, id: seq.id };
  const st = (k: string) => seq.stats[k] ?? 0;
  const [error, setError] = useState("");
  const dirty = useRef(new Set<string>());
  const markDirty = useCallback((part: string, d: boolean) => {
    if (d) dirty.current.add(part);
    else dirty.current.delete(part);
  }, []);

  // Warn before leaving with unsaved edits, in the app and when closing the tab.
  useEffect(() => {
    const warn = (e: BeforeUnloadEvent) => {
      if (dirty.current.size > 0) e.preventDefault();
    };
    addEventListener("beforeunload", warn);
    return () => removeEventListener("beforeunload", warn);
  }, []);
  async function back() {
    if (dirty.current.size > 0 && !(await confirmAction("Leave without saving?", <p>Your changes to the {[...dirty.current].join(" and ")} haven't been saved.</p>, "Leave without saving"))) return;
    onBack();
  }

  async function setStatus(status: "active" | "paused" | "archived") {
    if (dirty.current.size > 0) {
      setError(`Save your changes to the ${[...dirty.current].join(" and ")} first.`);
      return;
    }
    if (status === "active" && seq.daily_new_limit == null && seq.target_daily == null && seq.allocation.mailboxes === 0) {
      setError("Set new leads per day or a daily send target in Settings first, so Outflare knows how many mailboxes this sequence needs. Or add mailboxes by hand under Mailboxes.");
      return;
    }
    if (status === "active") {
      const leads = seq.stats.active ?? 0;
      const ok = await confirmAction(
        `Start sending "${seq.name}"?`,
        <>
          <p>{leads > 0 ? `${plural(leads, "lead")} will start getting step 1` : "No leads are in it yet. Leads you add later start getting step 1"} during the sending window ({seq.send_days.map((d) => days[d - 1]).join(", ")}, {seq.send_start} to {seq.send_end}, {seq.timezone}).</p>
          <p>{seq.allocation.mailboxes > 0 ? `It sends from its ${plural(seq.allocation.mailboxes, "mailbox", "mailboxes")}` : "Outflare gives it ready mailboxes straight away"}{seq.daily_new_limit != null || seq.target_daily != null ? ", topped up to cover its daily target" : ""}. Every email is checked against the suppression list first. You can pause at any time.</p>
        </>,
        "Start sending",
        false,
      );
      if (!ok) return;
    }
    if (status === "archived" && !(await confirmAction(`Archive "${seq.name}"?`, <p>It's hidden from the list and sends nothing. Sent emails and replies are kept.</p>, "Archive"))) return;
    const { error } = await api.PATCH("/v1/workspaces/{ws}/sequences/{id}", { params: { path }, body: { status } });
    if (error) return setError(errorMessage(error));
    setError("");
    toast(status === "active" ? `"${seq.name}" is sending.` : status === "paused" ? `Paused "${seq.name}". Leads stay where they are.` : `Archived "${seq.name}".`);
    if (status === "archived") onBack();
    onChange();
  }

  const linked = (seq.links ?? []).map((l) => l.platform).join(" and ");
  return (
    <div className="space-y-6">
      <PageHeader
        back={{ label: "Sequences", onClick: back }}
        title={<>{seq.name} <Badge tone={statusTone[seq.status]}>{statusLabel[seq.status]}</Badge></>}
        description={
          linked ? `Runs as a ${linked} campaign. Change its steps, leads and status there; Outflare decides which mailboxes it uses.`
          : seq.status === "draft" ? "A draft sends nothing. Write the steps, preview them for a real lead, add leads, then start sending."
            : seq.status === "paused" ? "Paused: nothing is sent. Leads carry on from where they were when you resume."
              : seq.status === "active" ? `Sending during the window below. ${plural(seq.stats.sent ?? 0, "email")} sent so far.` : undefined
        }
        action={
          canManage && !linked && (
            <>
              {seq.status !== "active" && <Button onClick={() => setStatus("active")}>{seq.status === "paused" ? "Resume sending" : "Start sending"}</Button>}
              {seq.status === "active" && <Button variant="ghost" onClick={() => setStatus("paused")}>Pause</Button>}
              {seq.status !== "active" && <Menu items={[{ label: "Archive", onClick: () => setStatus("archived"), danger: true }]} />}
            </>
          )
        }
      />
      <ErrorText>{error}</ErrorText>
      {!linked && <Stats>
        <Stat label="Being emailed" value={st("active")} sub={st("completed") > 0 ? `${st("completed")} finished every step` : "leads still in the sequence"} />
        <Stat label="Sent" value={st("sent")} sub={seq.track_clicks ? `${plural(st("clicked"), "lead")} clicked a link` : undefined} />
        <Stat label="Replied" value={st("replied")} sub={replyRate(st("replied"), st("contacted")) && `${replyRate(st("replied"), st("contacted"))} reply rate`} tone={st("replied") > 0 ? "green" : undefined} />
        <Stat label="Opted out · bounced" value={`${st("unsubscribed")} · ${st("bounced")}`} sub="added to suppression" tone={st("bounced") > 0 && st("bounced") / Math.max(1, st("sent")) > 0.02 ? "red" : undefined} />
      </Stats>}
      {!linked && <Steps ws={ws} seq={seq} canManage={canManage} onChange={onChange} markDirty={markDirty} />}
      {!linked && <Preview ws={ws} seq={seq} />}
      <Settings ws={ws} seq={seq} canManage={canManage} onChange={onChange} markDirty={markDirty} go={go} />
      <SequenceMailboxes ws={ws} seq={seq} canManage={canManage} go={go} onChange={onChange} />
      {!linked && <Leads ws={ws} seq={seq} canManage={canManage} onChange={onChange} go={go} />}
    </div>
  );
}

function SaveBar({ dirty, onSave, msg, label = "Save" }: { dirty: boolean; onSave?: () => void; msg: string; label?: string }) {
  return (
    <div className="flex flex-wrap items-center gap-3">
      <Button type={onSave ? "button" : "submit"} onClick={onSave} disabled={!dirty}>{label}</Button>
      {dirty ? <span className="text-sm text-amber-700 dark:text-amber-400">Unsaved changes</span> : msg && <span className="text-sm text-muted-foreground">{msg}</span>}
    </div>
  );
}

function Settings({ ws, seq, canManage, onChange, markDirty, go }: { ws: string; seq: Sequence; canManage: boolean; onChange: () => void; markDirty: (p: string, d: boolean) => void; go: (t: Tab) => void }) {
  const initial = {
    name: seq.name, pool_id: seq.pool_id, timezone: seq.timezone, send_days: seq.send_days, send_start: seq.send_start, send_end: seq.send_end, daily_new_limit: seq.daily_new_limit, stop_on_reply: seq.stop_on_reply,
    team_id: seq.team_id, priority: seq.priority, target_daily: seq.target_daily, min_daily: seq.min_daily, allocation_mode: seq.allocation_mode, preferred_sender_ids: seq.preferred_sender_ids,
    track_clicks: seq.track_clicks, unsubscribe_header: seq.unsubscribe_header,
  };
  const [s, setS] = useState(initial);
  const [saved, setSaved] = useState(JSON.stringify(initial));
  const linked = (seq.links?.length ?? 0) > 0;
  const [pools, setPools] = useState<MailboxPool[]>([]);
  const [linkDomain, setLinkDomain] = useState<string | null | undefined>();
  const [teams, setTeams] = useState<Team[]>([]);
  const [senders, setSenders] = useState<Sender[]>([]);
  const [msg, setMsg] = useState("");
  const [error, setError] = useState("");
  const dirty = JSON.stringify(s) !== saved;

  useEffect(() => {
    markDirty("settings", dirty);
  }, [dirty, markDirty]);
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/mailbox-pools", { params: { path: { ws } } }).then(({ data }) => data && setPools(data.data));
    api.GET("/v1/workspaces/{ws}/tracking-domains", { params: { path: { ws } } }).then(({ data }) => data && setLinkDomain(data.data.find((d) => d.status === "verified")?.domain ?? null));
    api.GET("/v1/workspaces/{ws}/teams", { params: { path: { ws } } }).then(({ data }) => data && setTeams(data.data ?? []));
    api.GET("/v1/workspaces/{ws}/senders", { params: { path: { ws } } }).then(({ data }) => data && setSenders((data.data ?? []).filter((x) => x.status === "active")));
  }, [ws]);

  async function save(e: FormEvent) {
    e.preventDefault();
    if (s.send_days.length === 0) return setError("Pick at least one day to send on.");
    if (s.send_start >= s.send_end) return setError("The window has to end after it starts.");
    if (s.min_daily != null && s.target_daily != null && s.min_daily > s.target_daily) return setError("The minimum can't be more than the daily send target.");
    const { error } = await api.PATCH("/v1/workspaces/{ws}/sequences/{id}", { params: { path: { ws, id: seq.id } }, body: s });
    if (error) return setError(errorMessage(error));
    setError("");
    setSaved(JSON.stringify(s));
    setMsg("Saved.");
    onChange();
  }

  const toggleDay = (d: number) => setS({ ...s, send_days: s.send_days.includes(d) ? s.send_days.filter((x) => x !== d) : [...s.send_days, d].sort() });

  return (
    <Card title="Settings" description={linked ? "How many mailboxes it needs. Its schedule and steps are set in the platform." : "When this sequence sends, and how many mailboxes it needs."}>
      <form onSubmit={save} className="space-y-5 text-sm">
        <fieldset disabled={!canManage} className="space-y-5">
          <label className="block max-w-md">
            <span className="mb-1 block font-medium">Name</span>
            <input className={`${field} w-full`} value={s.name} onChange={(e) => setS({ ...s, name: e.target.value })} required />
          </label>
          {!linked && <div>
            <span className="mb-1 block font-medium">Sending window</span>
            <div className="flex flex-wrap gap-1.5" role="group" aria-label="Days">
              {days.map((label, i) => {
                const on = s.send_days.includes(i + 1);
                return (
                  <button type="button" key={label} aria-pressed={on} onClick={() => toggleDay(i + 1)} className={`w-12 rounded-md border py-1.5 text-sm ${on ? "border-primary bg-primary text-primary-foreground" : "border-border text-muted-foreground"}`}>
                    {label}
                  </button>
                );
              })}
            </div>
            <div className="mt-2 flex flex-wrap items-center gap-2">
              <span>from</span>
              <input aria-label="Start time" type="time" className={field} value={s.send_start} onChange={(e) => setS({ ...s, send_start: e.target.value })} />
              <span>to</span>
              <input aria-label="End time" type="time" className={field} value={s.send_end} onChange={(e) => setS({ ...s, send_end: e.target.value })} />
              <span>in</span>
              <select aria-label="Time zone" className={`${field} max-w-60`} value={s.timezone} onChange={(e) => setS({ ...s, timezone: e.target.value })}>
                {!zones.includes(s.timezone) && <option value={s.timezone}>{s.timezone}</option>}
                {zones.map((z) => <option key={z} value={z}>{z.replaceAll("_", " ")}</option>)}
              </select>
            </div>
            <p className="mt-1 text-xs text-muted-foreground">Use the time zone of the people you're emailing, so emails land in their working hours.</p>
          </div>}
          <div className="grid gap-5 sm:grid-cols-2">
            {!linked && <label className="block">
              <span className="mb-1 block font-medium">New leads per day</span>
              <input type="number" min={1} className={`${field} w-full`} placeholder="No limit" value={s.daily_new_limit ?? ""} onChange={(e) => setS({ ...s, daily_new_limit: e.target.value ? Number(e.target.value) : null })} />
              <span className="mt-1 block text-xs text-muted-foreground">How many leads start step 1 each day. Mailboxes are allocated for this times the number of steps, unless you set a daily send target.</span>
            </label>}
            <label className="block">
              <span className="mb-1 block font-medium">Daily send target</span>
              <input type="number" min={1} className={`${field} w-full`} placeholder={s.daily_new_limit ? `${(s.daily_new_limit * Math.max(1, seq.steps.length)).toLocaleString()} (from new leads × steps)` : "Not set"} value={s.target_daily ?? ""} onChange={(e) => setS({ ...s, target_daily: e.target.value ? Number(e.target.value) : null })} />
              <span className="mt-1 block text-xs text-muted-foreground">{linked ? "Emails a day the campaign sends, follow-ups included. Nothing is allocated until you set it." : "Emails a day, follow-ups included, to allocate mailboxes for."}</span>
            </label>
          </div>
          <details className="rounded-lg border" open={!!(s.team_id || s.priority !== "normal" || s.min_daily || s.allocation_mode || s.pool_id || s.preferred_sender_ids.length)}>
            <summary className="cursor-pointer px-3 py-2 font-medium">Mailbox allocation <span className="font-normal text-muted-foreground">· team, priority, preferred senders</span></summary>
            <div className="grid gap-5 border-t p-3 sm:grid-cols-2">
              {teams.length > 0 && (
                <label className="block">
                  <span className="mb-1 block font-medium">Team</span>
                  <select className={`${field} w-full`} value={s.team_id ?? ""} onChange={(e) => setS({ ...s, team_id: e.target.value || null })}>
                    <option value="">No team</option>
                    {teams.map((t) => <option key={t.id} value={t.id}>{t.name}</option>)}
                  </select>
                  <span className="mt-1 block text-xs text-muted-foreground">Uses this team's senders first, then shared ones. Other teams' mailboxes need their approval.</span>
                </label>
              )}
              <label className="block">
                <span className="mb-1 block font-medium">Priority</span>
                <select className={`${field} w-full`} value={s.priority} onChange={(e) => setS({ ...s, priority: e.target.value as Sequence["priority"] })}>
                  <option value="high">High</option>
                  <option value="normal">Normal</option>
                  <option value="low">Low</option>
                </select>
                <span className="mt-1 block text-xs text-muted-foreground">When mailboxes are short, higher priority may take them from lower, once their follow-ups are done.</span>
              </label>
              <label className="block">
                <span className="mb-1 block font-medium">Never go below</span>
                <input type="number" min={1} className={`${field} w-full`} placeholder="No minimum" value={s.min_daily ?? ""} onChange={(e) => setS({ ...s, min_daily: e.target.value ? Number(e.target.value) : null })} />
                <span className="mt-1 block text-xs text-muted-foreground">Sends a day a higher-priority sequence can't take from this one.</span>
              </label>
              <label className="block">
                <span className="mb-1 block font-medium">Changes</span>
                <select className={`${field} w-full`} value={s.allocation_mode ?? ""} onChange={(e) => setS({ ...s, allocation_mode: (e.target.value || null) as Sequence["allocation_mode"] })}>
                  <option value="">{teams.find((t) => t.id === s.team_id) ? "Team default" : "Workspace default"}</option>
                  <option value="automatic">Make them automatically</option>
                  <option value="suggest">Suggest them for review</option>
                </select>
              </label>
              <label className="block">
                <span className="mb-1 block font-medium">Only use mailboxes from</span>
                <select className={`${field} w-full`} value={s.pool_id ?? ""} onChange={(e) => setS({ ...s, pool_id: e.target.value || null })}>
                  <option value="">Any pool</option>
                  {pools.map((p) => <option key={p.id} value={p.id}>Pool: {p.name}</option>)}
                </select>
              </label>
              {senders.length > 0 && (
                <fieldset className="sm:col-span-2">
                  <legend className="mb-1 font-medium">Preferred senders</legend>
                  <p className="mb-2 text-xs text-muted-foreground">Their mailboxes are used first, e.g. the account owner for an ABM sequence.</p>
                  <div className="flex flex-wrap gap-x-4 gap-y-1">
                    {senders.map((x) => (
                      <label key={x.id} className="flex items-center gap-2">
                        <input type="checkbox" checked={s.preferred_sender_ids.includes(x.id!)} onChange={() => setS({ ...s, preferred_sender_ids: s.preferred_sender_ids.includes(x.id!) ? s.preferred_sender_ids.filter((y) => y !== x.id) : [...s.preferred_sender_ids, x.id!].sort() })} />
                        {x.name}
                      </label>
                    ))}
                  </div>
                </fieldset>
              )}
            </div>
          </details>
          {!linked && <label className="flex items-start gap-2">
            <input type="checkbox" className="mt-0.5" checked={s.stop_on_reply} onChange={(e) => setS({ ...s, stop_on_reply: e.target.checked })} />
            <span><span className="font-medium">Stop when they reply</span><span className="block text-xs text-muted-foreground">Recommended. Auto-replies such as out-of-office don't count.</span></span>
          </label>}
          {!linked && <>
          <label className="flex items-start gap-2">
            <input type="checkbox" className="mt-0.5" checked={s.unsubscribe_header} onChange={(e) => setS({ ...s, unsubscribe_header: e.target.checked })} />
            <span>
              <span className="font-medium">Add an unsubscribe header</span>
              <span className="block text-xs text-muted-foreground">Recommended. Gmail and Yahoo show an unsubscribe button and expect one-click unsubscribe from bulk senders. A {"{{unsubscribe_url}}"} link in the body works either way.</span>
            </span>
          </label>
          <label className="flex items-start gap-2">
            <input type="checkbox" className="mt-0.5" checked={s.track_clicks} onChange={(e) => setS({ ...s, track_clicks: e.target.checked })} />
            <span>
              <span className="font-medium">Track link clicks</span>
              <span className="block text-xs text-muted-foreground">
                Off by default. Links are swapped for a redirect so clicks can be counted, which spam filters can count against you. Clicks by link scanners are left out.{" "}
                {linkDomain ? <>Links will use {linkDomain}.</>
                  : linkDomain === null && <>Links would use this server's address. <button type="button" className="underline" onClick={() => go("domains")}>Set up a tracking domain</button> first.</>}
              </span>
            </span>
          </label>
          </>}
        </fieldset>
        <ErrorText>{error}</ErrorText>
        {canManage && <SaveBar dirty={dirty} msg={msg} label="Save settings" />}
      </form>
    </Card>
  );
}

function Steps({ ws, seq, canManage, onChange, markDirty }: { ws: string; seq: Sequence; canManage: boolean; onChange: () => void; markDirty: (p: string, d: boolean) => void }) {
  const initial = seq.steps.map(({ delay_days, subject, body }) => ({ delay_days, subject, body }));
  const [steps, setSteps] = useState<StepInput[]>(initial);
  const [saved, setSaved] = useState(JSON.stringify(initial));
  const [msg, setMsg] = useState("");
  const [error, setError] = useState("");
  const [focus, setFocus] = useState<{ i: number; part: "subject" | "body" } | null>(null);
  const refs = useRef<Record<string, HTMLInputElement | HTMLTextAreaElement | null>>({});
  const dirty = JSON.stringify(steps) !== saved;
  useEffect(() => {
    markDirty("steps", dirty);
  }, [dirty, markDirty]);

  const update = (i: number, patch: Partial<StepInput>) => setSteps(steps.map((s, j) => (j === i ? { ...s, ...patch } : s)));

  // Inserts {{variable}} at the cursor of the last focused subject or body.
  function insert(v: string) {
    const f = focus ?? { i: 0, part: "body" as const };
    const el = refs.current[`${f.i}-${f.part}`];
    const text = `{{${v}}}`;
    const cur = (steps[f.i][f.part] ?? "") as string;
    const at = el?.selectionStart ?? cur.length;
    update(f.i, { [f.part]: cur.slice(0, at) + text + cur.slice(el?.selectionEnd ?? at) });
    requestAnimationFrame(() => {
      el?.focus();
      el?.setSelectionRange(at + text.length, at + text.length);
    });
  }

  async function save() {
    if (!steps[0]?.subject?.trim()) return setError("Step 1 needs a subject.");
    const empty = steps.findIndex((s) => !s.body.trim());
    if (empty >= 0) return setError(`Step ${empty + 1} has no text.`);
    const { error } = await api.PUT("/v1/workspaces/{ws}/sequences/{id}/steps", { params: { path: { ws, id: seq.id } }, body: { steps } });
    if (error) return setError(errorMessage(error));
    setError("");
    setSaved(JSON.stringify(steps));
    setMsg("Saved.");
    onChange();
  }

  let day = 0;
  return (
    <Card title="Steps" description="Plain-text emails, sent in order. Short and personal works best." action={canManage && steps.length < 20 && <Button variant="ghost" onClick={() => setSteps([...steps, { delay_days: 3, subject: "", body: "" }])}>Add a follow-up</Button>}>
      {canManage && (
        <div className="mb-4 text-sm">
          <span className="text-muted-foreground">Insert a variable: </span>
          <span className="inline-flex flex-wrap gap-1.5 align-middle">
            {variables.map((v) => (
              <button key={v} type="button" onClick={() => insert(v)} className="rounded border border-border px-1.5 py-0.5 font-mono text-xs hover:bg-accent">{v}</button>
            ))}
          </span>
          <p className="mt-2 text-xs text-muted-foreground">
            Any column from your lead CSV works too, like <code>{"{{pain_point}}"}</code>. Add a fallback for leads without it: <code>{"{{first_name|there}}"}</code>. A lead missing a variable with no fallback isn't emailed, so check the preview.
          </p>
        </div>
      )}
      <ol className="space-y-4">
        {steps.map((s, i) => {
          day += i === 0 ? 0 : (s.delay_days ?? 1);
          return (
            <li key={i}>
              <fieldset disabled={!canManage} className="space-y-2 rounded-md border border-border p-3 text-sm">
                <legend className="sr-only">Step {i + 1}</legend>
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-medium">Step {i + 1}</span>
                  {i === 0 ? (
                    <span className="text-muted-foreground">sent first</span>
                  ) : (
                    <label className="flex items-center gap-1 text-muted-foreground">
                      sent <input type="number" min={1} max={365} aria-label={`Days before step ${i + 1}`} className={`${field} w-16 px-2`} value={s.delay_days ?? 1} onChange={(e) => update(i, { delay_days: Math.max(1, Number(e.target.value)) })} /> days later
                      <span className="text-xs">(day {day + 1})</span>
                    </label>
                  )}
                  {canManage && steps.length > 1 && <Button variant="danger" className="ml-auto" onClick={() => setSteps(steps.filter((_, j) => j !== i))}>Remove step</Button>}
                </div>
                <input
                  ref={(el) => { refs.current[`${i}-subject`] = el; }}
                  onFocus={() => setFocus({ i, part: "subject" })}
                  aria-label={`Step ${i + 1} subject`}
                  className={`${field} w-full font-mono`}
                  placeholder={i === 0 ? "Subject" : "Leave empty to reply in the same thread (Re: …)"}
                  value={s.subject ?? ""}
                  onChange={(e) => update(i, { subject: e.target.value })}
                />
                <textarea
                  ref={(el) => { refs.current[`${i}-body`] = el; }}
                  onFocus={() => setFocus({ i, part: "body" })}
                  aria-label={`Step ${i + 1} text`}
                  className={`${field} min-h-40 w-full font-mono [field-sizing:content]`}
                  value={s.body}
                  onChange={(e) => update(i, { body: e.target.value })}
                />
              </fieldset>
            </li>
          );
        })}
      </ol>
      <div className="mt-3 space-y-2">
        <ErrorText>{error}</ErrorText>
        {canManage && <SaveBar dirty={dirty} onSave={save} msg={msg} label="Save steps" />}
      </div>
    </Card>
  );
}

type Rendered = { position: number; subject: string; body: string; missing: string[] };

// Shows exactly what one lead would get, with any variable they're missing.
function Preview({ ws, seq }: { ws: string; seq: Sequence }) {
  const [q, setQ] = useState("");
  const [options, setOptions] = useState<Lead[]>([]);
  const [lead, setLead] = useState<Lead | null>(null);
  const [out, setOut] = useState<{ data: Rendered[]; suppressed: boolean } | null>(null);
  const steps = JSON.stringify(seq.steps);

  useEffect(() => {
    const t = setTimeout(async () => {
      const { data } = await api.GET("/v1/workspaces/{ws}/leads", { params: { path: { ws }, query: { ...(q ? { q } : {}), limit: 8 } } });
      if (data) {
        setOptions(data.data);
        setLead((cur) => cur ?? data.data[0] ?? null);
      }
    }, 200);
    return () => clearTimeout(t);
  }, [ws, q]);

  useEffect(() => {
    if (!lead) return setOut(null);
    api.POST("/v1/workspaces/{ws}/sequences/{id}/preview", { params: { path: { ws, id: seq.id } }, body: { lead_id: lead.id } }).then(({ data }) => setOut(data ?? null));
  }, [ws, seq.id, lead, steps]);

  return (
    <Card title="Preview" description="What a lead actually receives, from the saved steps.">
      {options.length === 0 && !q ? (
        <p className="text-sm text-muted-foreground">Import leads to preview your steps with real names and companies.</p>
      ) : (
        <div className="space-y-4 text-sm">
          <div className="flex flex-wrap items-center gap-2">
            <label htmlFor="preview-lead" className="font-medium">Preview for</label>
            <select id="preview-lead" className={`${field} max-w-full`} value={lead?.id ?? ""} onChange={(e) => setLead(options.find((o) => o.id === e.target.value) ?? null)}>
              {lead && !options.some((o) => o.id === lead.id) && <option value={lead.id}>{lead.email}</option>}
              {options.map((o) => <option key={o.id} value={o.id}>{[o.first_name, o.last_name].filter(Boolean).join(" ") || o.email}{o.company ? `, ${o.company}` : ""}</option>)}
            </select>
            <input type="search" aria-label="Find a lead to preview" className={`${field} w-48`} placeholder="Find a lead…" value={q} onChange={(e) => setQ(e.target.value)} />
          </div>
          {out?.suppressed && <p className="rounded-md bg-red-50 p-2 text-red-800 dark:bg-red-950 dark:text-red-200">This lead is on the suppression list and won't be emailed.</p>}
          {out?.data.map((r) => (
            <div key={r.position} className="rounded-md border border-border">
              <div className="border-b border-border px-3 py-2">
                <span className="text-xs text-muted-foreground">Step {r.position + 1} · </span>
                <span className="font-medium">{r.subject || "(no subject)"}</span>
              </div>
              <pre className="whitespace-pre-wrap px-3 py-2 font-sans">{r.body}</pre>
              {r.missing.length > 0 && (
                <p className="border-t border-red-200 bg-red-50 px-3 py-2 text-red-800 dark:border-red-900 dark:bg-red-950 dark:text-red-200">
                  This step won't be sent to this lead: they have no {r.missing.map((m) => <code key={m} className="mx-0.5">{m}</code>)}. Add a fallback, like <code>{`{{${r.missing[0]}|…}}`}</code>.
                </p>
              )}
            </div>
          ))}
        </div>
      )}
    </Card>
  );
}

const enrollmentLabel: Record<string, [string, "green" | "grey" | "amber" | "red" | "blue"]> = {
  active: ["In progress", "green"],
  completed: ["Finished", "grey"],
  replied: ["Replied", "blue"],
  stopped: ["Stopped", "grey"],
  unsubscribed: ["Unsubscribed", "amber"],
  bounced: ["Bounced", "red"],
  blocked: ["Blocked", "red"],
  failed: ["Failed", "red"],
};

const reasonText: Record<string, string> = {
  outside_sending_window: "waiting for the sending window",
  no_active_mailbox: "waiting: no mailbox is sending",
  mailbox_daily_limit: "waiting: mailboxes hit their daily limit",
  mailbox_min_gap: "waiting: spacing out sends",
  sequence_daily_new_limit: "waiting: new-leads-per-day limit reached",
  no_mailbox_allocated: "waiting: no mailbox allocated to this sequence",
  sender_mailbox_gone: "waiting: its mailbox was removed, decide under Allocation",
  token_daily_budget: "waiting: the agent's daily send budget is used up",
  mailbox_auth_failed: "waiting: mailbox sign-in failed",
  mailbox_not_active: "waiting: mailbox paused",
  suppressed_email: "on the suppression list",
  suppressed_domain: "domain on the suppression list",
  invalid_email: "invalid email address",
};

function Leads({ ws, seq, canManage, onChange, go }: { ws: string; seq: Sequence; canManage: boolean; onChange: () => void; go: (t: Tab) => void }) {
  const [rows, setRows] = useState<Enrollment[] | null>(null);
  const [next, setNext] = useState<string | null>(null);
  const [lists, setLists] = useState<LeadList[]>([]);
  const [listId, setListId] = useState("");
  const [msg, setMsg] = useState("");
  const path = { ws, id: seq.id };

  const load = useCallback(async (after?: string) => {
    const { data } = await api.GET("/v1/workspaces/{ws}/sequences/{id}/enrollments", { params: { path: { ws, id: seq.id }, query: after ? { after } : {} } });
    if (!data) return;
    setRows((prev) => (after ? [...(prev ?? []), ...data.data] : data.data));
    setNext(data.next_after ?? null);
  }, [ws, seq.id]);

  useEffect(() => {
    load();
    // A brand's sequence enrolls from that brand's lists and unassigned
    // ones, never another brand's.
    api.GET("/v1/workspaces/{ws}/lead-lists", { params: { path: { ws } } }).then(({ data }) => data && setLists(data.data.filter((l) => !seq.brand_id || !l.brand_id || l.brand_id === seq.brand_id)));
  }, [load, ws, seq.brand_id]);

  async function enroll(e: FormEvent) {
    e.preventDefault();
    const { data, error } = await api.POST("/v1/workspaces/{ws}/sequences/{id}/enrollments", { params: { path }, body: { list_id: listId } });
    if (error) return setMsg(errorMessage(error));
    const skipped = [data.already_enrolled && `${data.already_enrolled} already in`, data.suppressed && `${data.suppressed} on the suppression list`, data.active_elsewhere && `${data.active_elsewhere} in another active sequence`].filter(Boolean);
    setMsg(`Added ${plural(data.enrolled, "lead")}.${skipped.length ? ` Skipped ${skipped.join(", ")}.` : ""}${seq.status !== "active" && data.enrolled > 0 ? " They start once you start sending." : ""}`);
    setListId("");
    load();
    onChange();
  }

  async function stop(en: Enrollment) {
    if (!(await confirmAction(`Stop emailing ${en.email}?`, <p>They get no more steps from this sequence. This can't be undone, but you can add them to another sequence.</p>, "Stop emailing"))) return;
    await api.POST("/v1/workspaces/{ws}/sequences/{id}/enrollments/{eid}/stop", { params: { path: { ...path, eid: en.id } } });
    toast(`Stopped emailing ${en.email}.`);
    load();
    onChange();
  }

  const counts = Object.entries(seq.stats).filter(([k, v]) => k !== "sent" && v > 0);

  return (
    <Card title="Leads" description={counts.length ? counts.map(([k, v]) => `${v} ${(enrollmentLabel[k]?.[0] ?? k).toLowerCase()}`).join(" · ") : "No leads in this sequence yet."}>
      {canManage && (
        lists.length === 0 ? (
          <p className="mb-4 text-sm text-muted-foreground">Leads are added by list. <button className="underline" onClick={() => go("leads")}>Import leads into a list</button> first.</p>
        ) : (
          <form onSubmit={enroll} className="mb-4 flex flex-wrap items-center gap-2 text-sm">
            <select aria-label="Lead list to add" className={field} value={listId} onChange={(e) => setListId(e.target.value)} required>
              <option value="">Choose a lead list…</option>
              {lists.map((l) => <option key={l.id} value={l.id}>{l.name} ({plural(l.leads, "lead")})</option>)}
            </select>
            <Button disabled={!listId}>Add to sequence</Button>
            {msg && <span className="basis-full text-muted-foreground">{msg}</span>}
          </form>
        )
      )}
      {rows === null ? (
        <Loading />
      ) : rows.length > 0 && (
        <DataTable
          className="-mx-4 [&>div]:rounded-none [&>div]:border-x-0 [&>div]:border-b-0 [&>div]:shadow-none [&>ul]:rounded-none [&>ul]:border-x-0 [&>ul]:border-b-0"
          data={rows}
          rowId={(en) => en.id}
          columnsMenu={false}
          mobile={(en) => {
            const [label, tone] = enrollmentLabel[en.status] ?? [en.status, "grey"];
            return (
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0">
                  <div className="font-mono text-[12px] break-all">{en.email}</div>
                  <div className="mt-1 flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground"><Badge tone={tone}>{label}</Badge>{en.status === "active" && <>Step {en.next_step + 1}</>}</div>
                </div>
                {canManage && en.status === "active" && <Button variant="danger" onClick={() => stop(en)}>Stop</Button>}
              </div>
            );
          }}
          columns={[
            { id: "lead", header: "Lead", accessorFn: (en) => en.email, cell: ({ row: { original: en } }) => <><span className="font-mono text-[12px]">{en.email}</span><div className="text-xs text-muted-foreground">{[en.name, en.company].filter(Boolean).join(" · ")}</div></> },
            {
              id: "status",
              header: "Status",
              accessorFn: (en) => enrollmentLabel[en.status]?.[0] ?? en.status,
              cell: ({ row: { original: en } }) => {
                const [label, tone] = enrollmentLabel[en.status] ?? [en.status, "grey"];
                return <><Badge tone={tone}>{label}</Badge>{en.status_reason && <div className="mt-0.5 text-xs text-muted-foreground">{reasonText[en.status_reason] ?? en.status_reason.replaceAll("_", " ")}</div>}</>;
              },
            },
            {
              id: "next",
              header: "Next email",
              accessorFn: (en) => (en.status === "active" ? en.next_send_at ?? "" : "~"),
              cell: ({ row: { original: en } }) => en.status === "active" ? <>Step {en.next_step + 1}{en.next_send_at && <div className="text-xs text-muted-foreground">{seq.status === "active" ? when(en.next_send_at, seq.timezone) : "once you start sending"}</div>}</> : <span className="text-muted-foreground/60">–</span>,
            },
            { id: "from", header: "From", accessorFn: (en) => en.mailbox_email ?? "", cell: ({ row: { original: en } }) => <span className="text-muted-foreground">{en.mailbox_email ? <span className="font-mono text-[12px]">{en.mailbox_email}</span> : <span className="text-muted-foreground/70">Picked when sending</span>}</span> },
            { id: "actions", header: () => <span className="sr-only">Actions</span>, enableSorting: false, enableHiding: false, meta: { align: "right" }, cell: ({ row: { original: en } }) => canManage && en.status === "active" && <Button variant="danger" onClick={() => stop(en)}>Stop</Button> },
          ]}
        />
      )}
      {next && <Button variant="ghost" className="mt-3" onClick={() => load(next)}>Show more</Button>}
    </Card>
  );
}

