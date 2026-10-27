// Journeys: if-this-then-that flows for people on customer records. An
// event on someone's timeline (or a manual add) starts a run that sends
// lifecycle emails, waits, branches on what they did and exits. The page
// lists journeys, builds their steps and shows who is in them.

import { useCallback, useEffect, useState, type FormEvent, type ReactNode } from "react";
import type { EmailTemplate, Journey, JourneyCondition, JourneyRun, JourneyStep } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { useBrand } from "@/lib/brand";
import { Badge, Button, Card, confirmAction, Empty, ErrorText, field, Input, Loading, PageHeader, Stat, Stats, toast } from "../ui";
import { ago, plural, stamp } from "../format";
import { DataTable, type ColumnDef } from "@/components/data-table";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Facts } from "./Emails";
import type { Go } from "./Lifecycle";

type Status = Journey["status"];
const statusTone: Record<Status, "green" | "grey" | "amber"> = { draft: "grey", active: "green", paused: "amber", archived: "grey" };
const statusText: Record<Status, string> = { draft: "Draft", active: "Active", paused: "Paused", archived: "Archived" };
const runText: Record<JourneyRun["status"], string> = { active: "In it", completed: "Completed", exited: "Exited", failed: "Failed" };
const exitText: Record<string, string> = {
  goal: "Reached the goal", exit_step: "Exit step", manual: "Taken out", archived: "Journey archived", step_removed: "Their step was removed",
  email_failed: "An email could not be sent", "too many steps": "Too many steps",
};

function StatusBadge({ j }: { j: Journey }) {
  return <Badge tone={statusTone[j.status]}>{statusText[j.status]}</Badge>;
}

function triggerText(j: Pick<Journey, "trigger">) {
  if (j.trigger.kind === "manual") return "When someone is added";
  return `When someone does ${j.trigger.event}`;
}

function waitText(s: number) {
  if (s % 86400 === 0) return plural(s / 86400, "day");
  if (s % 3600 === 0) return plural(s / 3600, "hour");
  return plural(Math.round(s / 60), "minute");
}

const opText: Record<JourneyCondition["op"], string> = { eq: "is", neq: "is not", gt: "is more than", lt: "is less than", contains: "contains", exists: "is set", not_exists: "is not set" };

function conditionText(c: JourneyCondition, steps: JourneyStep[]) {
  const did = c.op === "exists";
  if (c.event) return `${did ? "did" : "didn't do"} ${c.event}`;
  if (c.email) return `${did ? "" : "not "}${c.email} ${label(steps.find((s) => s.id === c.step)) || c.step}`;
  const what = c.attribute ?? `event ${c.property}`;
  return c.op === "exists" || c.op === "not_exists" ? `${what} ${opText[c.op]}` : `${what} ${opText[c.op]} ${JSON.stringify(c.value)}`;
}

function label(s?: JourneyStep) {
  if (!s) return "";
  if (s.name) return s.name;
  switch (s.kind) {
    case "email": return `Send ${s.template}`;
    case "wait": return `Wait ${waitText(s.wait_seconds ?? 0)}`;
    case "branch": return "Check";
    case "exit": return "Exit";
  }
}

const columns: ColumnDef<Journey>[] = [
  {
    id: "name", header: "Journey", accessorFn: (j) => j.name,
    cell: ({ row: { original: j } }) => (
      <span className="block min-w-0">
        <span className="block truncate font-medium">{j.name}</span>
        <span className="block truncate text-[12px] text-muted-foreground">{triggerText(j)}</span>
      </span>
    ),
  },
  { id: "status", header: "Status", accessorFn: (j) => j.status, cell: ({ row: { original: j } }) => <StatusBadge j={j} /> },
  { id: "active", header: "In it now", accessorFn: (j) => j.stats.active },
  { id: "entered", header: "Entered", accessorFn: (j) => j.stats.entered },
  { id: "completed", header: "Completed", accessorFn: (j) => j.stats.completed },
  { id: "updated", header: "Changed", accessorFn: (j) => j.updated_at, cell: ({ row: { original: j } }) => <span className="whitespace-nowrap text-muted-foreground" title={stamp(j.updated_at)}>{ago(j.updated_at)}</span> },
];

export function Journeys({ ws, canManage, open, onOpen, go }: { ws: string; canManage: boolean; open: string; onOpen: (id: string) => void; go: Go }) {
  const [list, setList] = useState<Journey[] | null>(null);
  const [archived, setArchived] = useState(false);
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState("");
  const { query: brandQuery } = useBrand(ws);
  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/journeys", { params: { path: { ws }, query: { ...(archived ? { status: "archived" as const } : {}), ...brandQuery } } });
    if (error) setError(errorMessage(error));
    setList(data?.data ?? []);
  }, [ws, archived, brandQuery.brand]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (!open) load();
  }, [load, open]);

  if (open) return <JourneyDetail ws={ws} id={open} canManage={canManage} back={() => onOpen("")} go={go} />;
  if (!list) return <Loading />;
  const add = canManage && <Button onClick={() => setCreating(true)}>New journey</Button>;
  return (
    <div className="space-y-6">
      <PageHeader
        title="Journeys"
        description="Flows that an event starts: when someone signs up, send the welcome email, wait two days, then send tips unless they upgraded. Each person goes through at their own pace."
        action={add}
      />
      <ErrorText>{error}</ErrorText>
      {list.length === 0 && !archived ? (
        <Empty title="No journeys yet" action={add}>
          A journey sends your published lifecycle templates to people on your customer records when they do something, such as signing up. People who left the email's topic or all marketing email are skipped.
        </Empty>
      ) : (
        <DataTable
          columns={columns}
          data={list}
          rowId={(j) => j.id}
          onRowClick={(j) => onOpen(j.id)}
          searchPlaceholder="Search journeys"
          toolbar={
            <select aria-label="Show" className={field} value={archived ? "archived" : ""} onChange={(e) => setArchived(e.target.value === "archived")}>
              <option value="">Current journeys</option>
              <option value="archived">Archived</option>
            </select>
          }
          mobile={(j) => (
            <div className="flex min-w-0 items-center gap-3">
              <span className="min-w-0 flex-1">
                <span className="block truncate text-[13px] font-medium">{j.name}</span>
                <span className="block truncate text-[12px] text-muted-foreground">{j.stats.active ? `${plural(j.stats.active, "person", "people")} in it` : triggerText(j)}</span>
              </span>
              <StatusBadge j={j} />
            </div>
          )}
        />
      )}
      {creating && <NewJourneyDialog ws={ws} onClose={() => setCreating(false)} onSaved={(j) => { setCreating(false); onOpen(j.id); }} />}
    </div>
  );
}

// useEvents is the approved events in the catalog, for picking a trigger
// or goal.
function useEvents(ws: string) {
  const [events, setEvents] = useState<string[]>([]);
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/catalog/events", { params: { path: { ws }, query: { status: "approved" } } }).then(({ data }) =>
      setEvents((data?.data ?? []).map((e) => e.name)),
    );
  }, [ws]);
  return events;
}

function EventInput({ id, label, value, onChange, events, hint, required, placeholder = "signed_up" }: { id: string; label: string; value: string; onChange: (v: string) => void; events: string[]; hint?: string; required?: boolean; placeholder?: string }) {
  return (
    <>
      <Input label={label} hint={hint} value={value} onChange={(e) => onChange(e.target.value.trim())} placeholder={placeholder} list={id} required={required} pattern="[a-z][a-z0-9_]*(\.[a-z0-9_]+)*" />
      <datalist id={id}>{events.map((e) => <option key={e} value={e} />)}</datalist>
    </>
  );
}

function NewJourneyDialog({ ws, onClose, onSaved }: { ws: string; onClose: () => void; onSaved: (j: Journey) => void }) {
  const events = useEvents(ws);
  const [name, setName] = useState("");
  const [kind, setKind] = useState<"event" | "manual">("event");
  const [event, setEvent] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/journeys", {
      params: { path: { ws } },
      body: { name, trigger: kind === "event" ? { kind, event } : { kind } },
    });
    setBusy(false);
    if (error || !data) return setError(errorMessage(error));
    toast("Draft saved. Add its steps, then activate it.");
    onSaved(data);
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>New journey</DialogTitle>
          <DialogDescription>Nothing runs until you activate it.</DialogDescription>
        </DialogHeader>
        <form onSubmit={save} className="space-y-3">
          <Input label="Name" value={name} onChange={(e) => setName(e.target.value)} placeholder="Onboarding" maxLength={200} required autoFocus />
          <label className="block space-y-1 text-[13px]">
            <span className="font-medium">Starts</span>
            <select aria-label="Starts" className={`${field} w-full`} value={kind} onChange={(e) => setKind(e.target.value as "event" | "manual")}>
              <option value="event">When someone does an event</option>
              <option value="manual">When someone is added, by hand or through the API</option>
            </select>
          </label>
          {kind === "event" && <EventInput id="new-journey-events" label="Event" value={event} onChange={setEvent} events={events} hint="An event your product sends to Outflare, approved in the event catalog." required />}
          <ErrorText>{error}</ErrorText>
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
            <Button disabled={busy || !name.trim() || (kind === "event" && !event)}>Save draft</Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function JourneyDetail({ ws, id, canManage, back, go }: { ws: string; id: string; canManage: boolean; back: () => void; go: Go }) {
  const [j, setJ] = useState<Journey | null>(null);
  const [editing, setEditing] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/journeys/{id}", { params: { path: { ws, id } } });
    if (error) return setError(errorMessage(error));
    setJ(data!);
  }, [ws, id]);
  useEffect(() => {
    load();
  }, [load]);

  if (error && !j) return <Empty title="Could not load this journey" action={<Button onClick={back}>All journeys</Button>}>{error}</Empty>;
  if (!j) return <Loading />;

  async function act(to: "activate" | "pause" | "archive") {
    const ask = {
      activate: ["Activate this journey?", j!.trigger.kind === "event" ? `From now on, everyone who does ${j!.trigger.event} starts it and gets its emails.` : "People you add start it and get its emails.", "Activate", false],
      pause: ["Pause this journey?", "No one new starts, and everyone in it stays where they are until you activate it again.", "Pause", false],
      archive: ["Archive this journey?", "It stops for good and everyone in it is taken out. This can't be undone.", "Archive", true],
    }[to] as [string, string, string, boolean];
    if (!(await confirmAction(...ask))) return;
    setBusy(true);
    const path = `/v1/workspaces/{ws}/journeys/{id}/${to}` as "/v1/workspaces/{ws}/journeys/{id}/activate";
    const { data, error } = await api.POST(path, { params: { path: { ws, id } } });
    setBusy(false);
    if (error || !data) return toast(errorMessage(error), "error");
    setJ({ ...data, step_stats: j!.step_stats });
    toast({ activate: "Active.", pause: "Paused.", archive: "Archived." }[to]);
  }

  const s = j.stats;
  const changeable = j.status === "draft" || j.status === "paused";
  return (
    <div className="space-y-6">
      <PageHeader
        back={{ label: "Journeys", onClick: back }}
        title={<span className="flex items-center gap-2">{j.name} <StatusBadge j={j} /></span>}
        description={j.description || undefined}
        action={canManage && j.status !== "archived" && !editing && (
          <div className="flex flex-wrap gap-2">
            {changeable && <Button variant="ghost" onClick={() => setEditing(true)}>Edit</Button>}
            {j.status !== "draft" || j.stats.entered > 0 ? <Button variant="ghost" onClick={() => act("archive")} disabled={busy}>Archive</Button> : null}
            {j.status === "active" && <Button variant="ghost" onClick={() => act("pause")} disabled={busy}>Pause</Button>}
            {changeable && <Button onClick={() => act("activate")} disabled={busy || j.steps.length === 0}>Activate</Button>}
          </div>
        )}
      />
      {j.status === "active" && canManage && <p className="text-[13px] text-muted-foreground">To change its steps, pause it first. People in it wait where they are while it is paused.</p>}
      <Stats cols={5}>
        <Stat label="Entered" value={s.entered} />
        <Stat label="In it now" value={s.active} />
        <Stat label="Completed" value={s.completed} />
        <Stat label="Exited" value={s.exited} sub="goal, exit step or taken out" />
        <Stat label="Failed" value={s.failed} tone={s.failed ? "red" : undefined} />
      </Stats>
      {editing ? (
        <JourneyEditor ws={ws} journey={j} onCancel={() => setEditing(false)} onSaved={(v) => { setEditing(false); setJ({ ...v, step_stats: j.step_stats }); }} go={go} />
      ) : (
        <>
          <div className="grid gap-4 lg:grid-cols-2">
            <Card title="Starts">
              <Facts rows={[
                ["Trigger", triggerText(j)],
                ...(j.trigger.filter.length ? [["Only if", j.trigger.filter.map((c) => conditionText(c, j.steps)).join(" and ")] as [string, ReactNode]] : []),
                ["Entry", j.entry === "once" ? "Once per person, ever" : "Again after they finish"],
                ["Goal", j.goal_event ? `Doing ${j.goal_event} ends their run` : "None"],
                ["Activated", j.activated_at ? stamp(j.activated_at) : "Not yet"],
              ]} />
            </Card>
            <Card title="Who gets its emails">
              <p className="text-[13px] text-muted-foreground">
                People on your customer records with a verified email address. Each email is filled in with their own fields ({"{{person.first_name}}"}) and the event's ({"{{event.plan}}"}). Anyone who left the email's topic or all marketing email is skipped and goes on to the next step.
              </p>
            </Card>
          </div>
          <Card title="Steps" action={changeable && canManage && <Button variant="ghost" onClick={() => setEditing(true)}>Edit steps</Button>}>
            {j.steps.length === 0 ? <p className="text-[13px] text-muted-foreground">No steps yet. Edit the journey to add an email, a wait or a check.</p> : <StepList j={j} go={go} />}
          </Card>
        </>
      )}
      {j.stats.entered > 0 && <RunsCard ws={ws} j={j} canManage={canManage} go={go} />}
    </div>
  );
}

const outcomeText: Record<string, string> = { sent: "sent", skipped: "skipped", waiting: "waiting", waited: "waited", yes: "yes", no: "no", failed: "failed", exited: "exited" };

function StepList({ j, go }: { j: Journey; go: Go }) {
  const byID = new Map(j.steps.map((s) => [s.id, s]));
  const to = (id?: string) => (id ? label(byID.get(id)) : "End");
  return (
    <ol className="space-y-2">
      {j.steps.map((s, i) => {
        const stats = j.step_stats?.[s.id] ?? {};
        return (
          <li key={s.id} className="rounded-md border p-3 text-[13px]">
            <div className="flex flex-wrap items-baseline justify-between gap-2">
              <span className="font-medium"><span className="mr-2 text-muted-foreground">{i + 1}</span>{label(s)}</span>
              <span className="text-[12px] text-muted-foreground">
                {Object.entries(stats).filter(([o]) => o !== "waiting").map(([o, n]) => `${n} ${outcomeText[o] ?? o}`).join(" · ")}
              </span>
            </div>
            <div className="mt-1 text-[12px] text-muted-foreground">
              {s.kind === "email" && <>Template <button className="underline" onClick={() => go("templates", s.template)}>{s.template}</button>, then {to(s.next)}</>}
              {s.kind === "wait" && <>Then {to(s.next)}</>}
              {s.kind === "branch" && <>If {(s.conditions ?? []).map((c) => conditionText(c, j.steps)).join(s.match === "any" ? " or " : " and ")}: {to(s.then)}. Otherwise: {to(s.else)}.</>}
              {s.kind === "exit" && <>Ends their run.</>}
            </div>
          </li>
        );
      })}
    </ol>
  );
}

// ---- Building a journey ----

type Unit = 60 | 3600 | 86400;
const units: [Unit, string][] = [[60, "minutes"], [3600, "hours"], [86400, "days"]];

function newID(kind: JourneyStep["kind"], steps: JourneyStep[]) {
  for (let n = 1; ; n++) {
    const id = `${kind}-${n}`;
    if (!steps.some((s) => s.id === id)) return id;
  }
}

function parseValue(v: string): unknown {
  if (v === "true" || v === "false") return v === "true";
  if (v.trim() !== "" && !Number.isNaN(Number(v))) return Number(v);
  return v;
}

function JourneyEditor({ ws, journey, onCancel, onSaved, go }: { ws: string; journey: Journey; onCancel: () => void; onSaved: (j: Journey) => void; go: Go }) {
  const events = useEvents(ws);
  const [templates, setTemplates] = useState<EmailTemplate[] | null>(null);
  const [name, setName] = useState(journey.name);
  const [description, setDescription] = useState(journey.description);
  const [kind, setKind] = useState(journey.trigger.kind);
  const [event, setEvent] = useState(journey.trigger.event ?? "");
  const [filter, setFilter] = useState<JourneyCondition[]>(journey.trigger.filter);
  const [entry, setEntry] = useState(journey.entry);
  const [goal, setGoal] = useState(journey.goal_event ?? "");
  const [steps, setSteps] = useState<JourneyStep[]>(journey.steps);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/email-templates", { params: { path: { ws } } }).then(({ data }) =>
      setTemplates((data?.data ?? []).filter((t) => t.purpose === "lifecycle" && t.status === "active")),
    );
  }, [ws]);

  const set = (i: number, s: JourneyStep) => setSteps((all) => all.map((x, k) => (k === i ? s : x)));
  function add(k: JourneyStep["kind"]) {
    setSteps((all) => {
      const s: JourneyStep = { id: newID(k, all), kind: k };
      if (k === "email") s.template = templates?.find((t) => t.published_version)?.handle ?? templates?.[0]?.handle ?? "";
      if (k === "wait") s.wait_seconds = 86400;
      if (k === "branch") s.conditions = [{ event: goal || "upgraded", op: "not_exists" }];
      // The step before goes on to the new one when it went nowhere.
      const last = all[all.length - 1];
      const linked = !last ? all
        : (last.kind === "email" || last.kind === "wait") && !last.next ? [...all.slice(0, -1), { ...last, next: s.id }]
        : last.kind === "branch" && !last.then ? [...all.slice(0, -1), { ...last, then: s.id }]
        : all;
      return [...linked, s];
    });
  }
  function remove(i: number) {
    setSteps((all) => {
      const gone = all[i].id;
      const after = all[i].kind === "email" || all[i].kind === "wait" ? all[i].next : undefined;
      // Whatever went to it goes where it went.
      return all.filter((_, k) => k !== i).map((s) => ({
        ...s,
        ...(s.next === gone ? { next: after } : {}),
        ...(s.then === gone ? { then: after } : {}),
        ...(s.else === gone ? { else: after } : {}),
        ...(s.conditions ? { conditions: s.conditions.filter((c) => c.step !== gone) } : {}),
      }));
    });
  }
  function move(i: number, by: -1 | 1) {
    setSteps((all) => {
      const k = i + by;
      if (k < 0 || k >= all.length) return all;
      const out = [...all];
      [out[i], out[k]] = [out[k], out[i]];
      return out;
    });
  }

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const clean = steps.map((s) => Object.fromEntries(Object.entries(s).filter(([, v]) => v !== undefined && v !== "")) as JourneyStep);
    const { data, error } = await api.PATCH("/v1/workspaces/{ws}/journeys/{id}", {
      params: { path: { ws, id: journey.id } },
      body: {
        name, description, entry, goal_event: goal || null, steps: clean,
        trigger: kind === "event" ? { kind, event, filter } : { kind },
      },
    });
    setBusy(false);
    if (error || !data) return setError(errorMessage(error));
    toast(journey.status === "draft" ? "Saved. Activate it when it's ready." : "Saved.");
    onSaved(data);
  }

  if (!templates) return <Loading />;
  return (
    <form onSubmit={save} className="space-y-4">
      <Card title="Starts">
        <div className="grid gap-3 sm:grid-cols-2">
          <Input label="Name" value={name} onChange={(e) => setName(e.target.value)} maxLength={200} required />
          <Input label="Description" value={description} onChange={(e) => setDescription(e.target.value)} maxLength={2000} placeholder="Optional" />
          <label className="block space-y-1 text-[13px]">
            <span className="font-medium">Trigger</span>
            <select aria-label="Trigger" className={`${field} w-full`} value={kind} onChange={(e) => setKind(e.target.value as typeof kind)}>
              <option value="event">When someone does an event</option>
              <option value="manual">When someone is added</option>
            </select>
          </label>
          {kind === "event" ? <EventInput id="journey-trigger-events" label="Event" value={event} onChange={setEvent} events={events} required /> : <span />}
          <label className="block space-y-1 text-[13px]">
            <span className="font-medium">Entry</span>
            <select aria-label="Entry" className={`${field} w-full`} value={entry} onChange={(e) => setEntry(e.target.value as typeof entry)}>
              <option value="once">Once per person, ever</option>
              <option value="again">Again after they finish</option>
            </select>
          </label>
          <EventInput id="journey-goal-events" label="Goal (optional)" value={goal} onChange={setGoal} events={events} hint="Doing this event ends their run." placeholder="upgraded" />
        </div>
        {kind === "event" && (
          <div className="mt-3 space-y-2">
            <span className="block text-[13px] font-medium">Only if</span>
            <Conditions list={filter} onChange={setFilter} steps={[]} filter />
          </div>
        )}
      </Card>
      <Card title="Steps" description="People start at the first step. Each step goes on to the one it names; a journey only moves forward.">
        <div className="space-y-3">
          {steps.length === 0 && <p className="text-[13px] text-muted-foreground">Add the first step: usually an email.</p>}
          {steps.map((s, i) => (
            <StepEdit key={s.id} n={i + 1} step={s} steps={steps} templates={templates} onChange={(v) => set(i, v)} onRemove={() => remove(i)}
              onMove={(by) => move(i, by)} go={go} />
          ))}
          <div className="flex flex-wrap gap-2">
            <Button type="button" variant="ghost" onClick={() => add("email")}>Add an email</Button>
            <Button type="button" variant="ghost" onClick={() => add("wait")}>Add a wait</Button>
            <Button type="button" variant="ghost" onClick={() => add("branch")}>Add a check</Button>
            <Button type="button" variant="ghost" onClick={() => add("exit")}>Add an exit</Button>
          </div>
        </div>
      </Card>
      <ErrorText>{error}</ErrorText>
      <div className="flex justify-end gap-2">
        <Button type="button" variant="ghost" onClick={onCancel}>Cancel</Button>
        <Button disabled={busy || !name.trim()}>Save</Button>
      </div>
    </form>
  );
}

function Goto({ label: text, value, onChange, steps, from }: { label: string; value?: string; onChange: (v?: string) => void; steps: JourneyStep[]; from: string }) {
  return (
    <label className="block space-y-1 text-[13px]">
      <span className="font-medium">{text}</span>
      <select aria-label={text} className={`${field} w-full`} value={value ?? ""} onChange={(e) => onChange(e.target.value || undefined)}>
        <option value="">End (completed)</option>
        {steps.filter((s) => s.id !== from).map((s) => <option key={s.id} value={s.id}>{steps.indexOf(s) + 1}. {label(s)}</option>)}
      </select>
    </label>
  );
}

function StepEdit({ n, step: s, steps, templates, onChange, onRemove, onMove, go }: {
  n: number; step: JourneyStep; steps: JourneyStep[]; templates: EmailTemplate[]; onChange: (s: JourneyStep) => void; onRemove: () => void; onMove: (by: -1 | 1) => void; go: Go;
}) {
  const unit: Unit = s.wait_seconds && s.wait_seconds % 86400 === 0 ? 86400 : s.wait_seconds && s.wait_seconds % 3600 === 0 ? 3600 : 60;
  const tpl = templates.find((t) => t.handle === s.template || t.id === s.template);
  const kindText = { email: "Send an email", wait: "Wait", branch: "Check", exit: "Exit" }[s.kind];
  return (
    <div className="rounded-md border p-3">
      <div className="mb-2 flex items-center justify-between gap-2">
        <span className="text-[13px] font-medium"><span className="mr-2 text-muted-foreground">{n}</span>{kindText}</span>
        <span className="flex gap-1">
          <Button type="button" variant="ghost" onClick={() => onMove(-1)} aria-label="Move up" disabled={n === 1}>↑</Button>
          <Button type="button" variant="ghost" onClick={() => onMove(1)} aria-label="Move down" disabled={n === steps.length}>↓</Button>
          <Button type="button" variant="ghost" onClick={onRemove}>Remove</Button>
        </span>
      </div>
      <div className="grid gap-3 sm:grid-cols-2">
        {s.kind === "email" && (
          <>
            <label className="block space-y-1 text-[13px]">
              <span className="font-medium">Template</span>
              <select aria-label="Template" className={`${field} w-full`} value={tpl?.handle ?? s.template ?? ""} onChange={(e) => onChange({ ...s, template: e.target.value })} required>
                <option value="">Choose a template</option>
                {templates.map((t) => <option key={t.id} value={t.handle}>{t.name}{t.published_version ? "" : " (not published yet)"}</option>)}
              </select>
              {templates.length === 0 && <span className="block text-[12px] text-muted-foreground">Write a lifecycle template first, under <button type="button" className="underline" onClick={() => go("templates")}>Templates</button>.</span>}
              {tpl && !tpl.published_version && <span className="block text-[12px] text-amber-700 dark:text-amber-400">Publish it before activating the journey.</span>}
            </label>
            <Goto label="Then" value={s.next} onChange={(v) => onChange({ ...s, next: v })} steps={steps} from={s.id} />
          </>
        )}
        {s.kind === "wait" && (
          <>
            <div className="flex items-end gap-2">
              <Input label="Wait" type="number" min={1} value={String((s.wait_seconds ?? 60) / unit)} onChange={(e) => onChange({ ...s, wait_seconds: Math.max(1, Number(e.target.value)) * unit })} required />
              <select aria-label="Unit" className={field} value={unit} onChange={(e) => onChange({ ...s, wait_seconds: ((s.wait_seconds ?? 60) / unit) * Number(e.target.value) })}>
                {units.map(([u, t]) => <option key={u} value={u}>{t}</option>)}
              </select>
            </div>
            <Goto label="Then" value={s.next} onChange={(v) => onChange({ ...s, next: v })} steps={steps} from={s.id} />
          </>
        )}
        {s.kind === "branch" && (
          <>
            <div className="space-y-2 sm:col-span-2">
              <label className="flex items-center gap-2 text-[13px]">
                <span className="font-medium">If</span>
                <select aria-label="Match" className={field} value={s.match ?? "all"} onChange={(e) => onChange({ ...s, match: e.target.value as "all" | "any" })}>
                  <option value="all">all of these hold</option>
                  <option value="any">any of these holds</option>
                </select>
              </label>
              <Conditions list={s.conditions ?? []} onChange={(c) => onChange({ ...s, conditions: c })} steps={steps.filter((x) => x.kind === "email")} />
            </div>
            <Goto label="Yes: go to" value={s.then} onChange={(v) => onChange({ ...s, then: v })} steps={steps} from={s.id} />
            <Goto label="No: go to" value={s.else} onChange={(v) => onChange({ ...s, else: v })} steps={steps} from={s.id} />
          </>
        )}
        {s.kind === "exit" && <p className="text-[13px] text-muted-foreground sm:col-span-2">Ends their run here.</p>}
      </div>
    </div>
  );
}

type Test = "attribute" | "property" | "event" | "email";

function testOf(c: JourneyCondition): Test {
  return c.event !== undefined ? "event" : c.email !== undefined ? "email" : c.property !== undefined ? "property" : "attribute";
}

// Conditions edits a list of tests. A trigger's filter only tests the
// person and the event; a check can also ask what they did since.
function Conditions({ list, onChange, steps, filter }: { list: JourneyCondition[]; onChange: (c: JourneyCondition[]) => void; steps: JourneyStep[]; filter?: boolean }) {
  const set = (i: number, c: JourneyCondition) => onChange(list.map((x, k) => (k === i ? c : x)));
  const tests: [Test, string][] = [["attribute", "Their field"], ["property", "The event's property"], ...(filter ? [] : [["event", "They did an event"], ["email", "An email of this journey"]] as [Test, string][])];
  return (
    <div className="space-y-2">
      {list.map((c, i) => {
        const t = testOf(c);
        const did = t === "event" || t === "email";
        return (
          <div key={i} className="flex flex-wrap items-center gap-2">
            <select aria-label="Test" className={field} value={t} onChange={(e) => {
              const v = e.target.value as Test;
              set(i, v === "event" ? { event: "", op: "exists" } : v === "email" ? { email: "opened", step: steps[0]?.id ?? "", op: "exists" } : { [v]: "", op: "eq", value: "" });
            }}>
              {tests.map(([v, text]) => <option key={v} value={v}>{text}</option>)}
            </select>
            {(t === "attribute" || t === "property") && (
              <input aria-label="Field" className={field} placeholder={t === "attribute" ? "plan" : "amount"} value={(t === "attribute" ? c.attribute : c.property) ?? ""}
                onChange={(e) => set(i, { ...c, [t]: e.target.value.trim() })} required />
            )}
            {t === "event" && <input aria-label="Event" className={field} placeholder="upgraded" value={c.event ?? ""} onChange={(e) => set(i, { ...c, event: e.target.value.trim() })} required />}
            {t === "email" && (
              <>
                <select aria-label="Email step" className={field} value={c.step ?? ""} onChange={(e) => set(i, { ...c, step: e.target.value })} required>
                  {steps.map((s) => <option key={s.id} value={s.id}>{label(s)}</option>)}
                </select>
                <select aria-label="Was" className={field} value={c.email} onChange={(e) => set(i, { ...c, email: e.target.value as JourneyCondition["email"] })}>
                  <option value="delivered">was delivered</option>
                  <option value="opened">was opened</option>
                  <option value="clicked">was clicked</option>
                </select>
              </>
            )}
            <select aria-label="Op" className={field} value={c.op} onChange={(e) => set(i, { ...c, op: e.target.value as JourneyCondition["op"] })}>
              {did ? (
                <>
                  <option value="exists">{t === "event" ? "did" : "yes"}</option>
                  <option value="not_exists">{t === "event" ? "didn't" : "no"}</option>
                </>
              ) : (Object.keys(opText) as JourneyCondition["op"][]).map((o) => <option key={o} value={o}>{opText[o]}</option>)}
            </select>
            {!did && c.op !== "exists" && c.op !== "not_exists" && (
              <input aria-label="Value" className={field} placeholder="pro" value={c.value === undefined ? "" : String(c.value)} onChange={(e) => set(i, { ...c, value: parseValue(e.target.value) })} required />
            )}
            <Button type="button" variant="ghost" onClick={() => onChange(list.filter((_, k) => k !== i))}>Remove</Button>
          </div>
        );
      })}
      <Button type="button" variant="ghost" onClick={() => onChange([...list, filter ? { attribute: "", op: "eq", value: "" } : { event: "", op: "exists" }])}>Add a condition</Button>
    </div>
  );
}

// ---- The people in it ----

function RunsCard({ ws, j, canManage, go }: { ws: string; j: Journey; canManage: boolean; go: Go }) {
  const [runs, setRuns] = useState<JourneyRun[] | null>(null);
  const [status, setStatus] = useState<JourneyRun["status"] | "">("");
  const [open, setOpen] = useState<string>("");
  const load = useCallback(async () => {
    const { data } = await api.GET("/v1/workspaces/{ws}/journeys/{id}/runs", { params: { path: { ws, id: j.id }, query: { limit: 100, ...(status ? { status } : {}) } } });
    setRuns(data?.data ?? []);
  }, [ws, j.id, status]);
  useEffect(() => {
    load();
  }, [load]);
  return (
    <Card title="People" action={
      <select aria-label="Status" className={field} value={status} onChange={(e) => setStatus(e.target.value as typeof status)}>
        <option value="">Everyone</option>
        {(Object.keys(runText) as JourneyRun["status"][]).map((s) => <option key={s} value={s}>{runText[s]}</option>)}
      </select>
    }>
      {!runs ? <Loading /> : runs.length === 0 ? <p className="text-[13px] text-muted-foreground">No one here.</p> : (
        <ul className="divide-y text-[13px]">
          {runs.map((r) => (
            <li key={r.id}>
              <button className="flex w-full flex-wrap items-baseline justify-between gap-2 py-2 text-left hover:bg-muted/40" onClick={() => setOpen(r.id)}>
                <span className="min-w-0 truncate font-medium">{r.name || "Unnamed person"}</span>
                <span className="text-muted-foreground">
                  {r.status === "active" ? `${label(j.steps.find((s) => s.id === r.step)) || "Starting"}${r.next_at ? `, until ${stamp(r.next_at)}` : ""}` : `${runText[r.status]}${r.exit_reason ? `: ${exitText[r.exit_reason] ?? r.exit_reason}` : ""}`}
                  {" · "}{ago(r.entered_at)}
                </span>
              </button>
            </li>
          ))}
        </ul>
      )}
      {open && <RunDialog ws={ws} j={j} id={open} canManage={canManage} go={go} onClose={() => setOpen("")} onChanged={load} />}
    </Card>
  );
}

function RunDialog({ ws, j, id, canManage, go, onClose, onChanged }: { ws: string; j: Journey; id: string; canManage: boolean; go: Go; onClose: () => void; onChanged: () => void }) {
  const [r, setR] = useState<JourneyRun | null>(null);
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/journeys/{id}/runs/{run}", { params: { path: { ws, id: j.id, run: id } } }).then(({ data, error }) => {
      if (error) toast(errorMessage(error), "error");
      setR(data ?? null);
    });
  }, [ws, j.id, id]);
  async function exit() {
    if (!(await confirmAction("Take them out of this journey?", "Nothing more is sent to them from it.", "Take out"))) return;
    const { data, error } = await api.POST("/v1/workspaces/{ws}/journeys/{id}/runs/{run}/exit", { params: { path: { ws, id: j.id, run: id } } });
    if (error || !data) return toast(errorMessage(error), "error");
    setR({ ...data, history: r?.history });
    onChanged();
  }
  const byID = new Map(j.steps.map((s) => [s.id, s]));
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{r?.name || "Their way through"}</DialogTitle>
          <DialogDescription>{r ? `Entered ${stamp(r.entered_at)}. ${runText[r.status]}${r.exit_reason ? `: ${exitText[r.exit_reason] ?? r.exit_reason}` : ""}.` : ""}</DialogDescription>
        </DialogHeader>
        {!r ? <Loading /> : (
          <div className="space-y-3">
            <ol className="space-y-1 text-[13px]">
              {(r.history ?? []).map((h, i) => (
                <li key={i} className="flex flex-wrap items-baseline justify-between gap-2">
                  <span>{label(byID.get(h.step)) || h.step}: <span className="text-muted-foreground">{outcomeText[h.outcome] ?? h.outcome}{h.detail ? ` (${h.detail})` : ""}</span></span>
                  <span className="text-[12px] text-muted-foreground">
                    {h.email_id && <button className="mr-2 underline" onClick={() => go("emails", h.email_id!)}>The email</button>}
                    {ago(h.at)}
                  </span>
                </li>
              ))}
            </ol>
            <div className="flex justify-end gap-2">
              {canManage && r.status === "active" && <Button variant="ghost" onClick={exit}>Take out</Button>}
            </div>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
