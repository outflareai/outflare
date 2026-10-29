// Journeys: if-this-then-that flows for people on customer records. An
// event on someone's timeline (or a manual add) starts a run that sends
// lifecycle emails, waits, branches on what they did and exits. The page
// lists journeys, builds their steps in the top-down flow builder
// (components/journey-flow) and shows who is in them. A live journey is
// read-only: Edit makes a draft version and Publish swaps it in.

import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent, type ReactNode } from "react";
import { Check, CircleAlert, Redo2, Undo2 } from "lucide-react";
import type { EmailTemplate, Journey, JourneyRun, JourneyVersion, Rule, RuleSimulation } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { useBrand } from "@/lib/brand";
import { track } from "@/lib/analytics";
import { cn } from "@/lib/utils";
import { useIsMobile } from "@/hooks/use-mobile";
import { Badge, Button, Card, confirmAction, Empty, ErrorText, field, Input, Loading, Menu, PageHeader, Stat, Stats, toast } from "../ui";
import { ago, plural, stamp } from "../format";
import { DataTable, type ColumnDef } from "@/components/data-table";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Flow, type StepCard } from "@/components/journey-flow/flow";
import {
  changes, clean, conditionText, errorCount, insert, newStep, problems, remove, replace, stepLabel,
  type Change, type Problem, type Slot, type Step, type StepKind,
} from "@/components/journey-flow/model";
import { StepPicker, useStepKinds } from "@/components/journey-flow/step-picker";
import { EventInput, StepPanel, TriggerPanel, useFlowCatalog, type TriggerFields } from "@/components/journey-flow/step-panel";
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

// ---- One journey: the flow builder ----

// Work is the part of a journey the builder edits: what a version holds.
type Work = { trigger: TriggerFields; steps: Step[] };

type Source = Pick<Journey, "trigger" | "entry" | "goal_event" | "steps">;

function workFrom(src: Source): Work {
  return {
    trigger: { kind: src.trigger.kind, event: src.trigger.event ?? "", filter: src.trigger.filter, entry: src.entry, goal: src.goal_event ?? "" },
    steps: src.steps as Step[],
  };
}

function bodyOf(w: Work) {
  const t = w.trigger;
  return {
    trigger: t.kind === "event" ? { kind: t.kind, event: t.event, filter: t.filter } : { kind: t.kind },
    entry: t.entry, goal_event: t.goal || null, steps: clean(w.steps),
  };
}

const outcomeText: Record<string, string> = { sent: "sent", skipped: "skipped", waiting: "waiting", waited: "waited", yes: "yes", no: "no", failed: "failed", exited: "exited" };

// stepCard is what a step's card says: its label, a detail line, and its
// live numbers.
const pct = (n: number, of: number) => `${Math.round((100 * n) / of)}%`;

function stepCard(s: Step, j: Journey, kinds: StepKind[], templates: EmailTemplate[], steps: Step[], probs: Problem[]): StepCard {
  const st = j.step_stats?.[s.id] ?? {};
  const em = j.email_stats?.[s.id];
  const n = (o: string) => st[o] ?? 0;
  const numbers: StepCard["numbers"] = [];
  let detail: string | undefined;
  switch (s.kind) {
    case "email": {
      const t = templates.find((x) => x.handle === s.template || x.id === s.template);
      // The label already names the template, unless the step has a name.
      if (s.name || (t && !t.published_version)) detail = t ? `${t.name}${t.published_version ? "" : " (not published)"}` : s.template;
      if (n("sent")) numbers.push({ text: `${n("sent")} sent` });
      // Opens and clicks as rates of delivered email, as email tools show them.
      if (em?.delivered) numbers.push({ text: `${pct(em.opened, em.delivered)} opened` }, { text: `${pct(em.clicked, em.delivered)} clicked` });
      if (n("skipped")) numbers.push({ text: `${n("skipped")} skipped`, tone: "amber" });
      break;
    }
    case "wait":
      if (n("waited")) numbers.push({ text: `${n("waited")} waited` });
      break;
    case "branch":
      detail = (s.conditions ?? []).map((c) => conditionText(c, steps)).join(s.match === "any" ? " or " : " and ");
      if (n("yes") || n("no")) numbers.push({ text: `Yes ${n("yes")} (${pct(n("yes"), n("yes") + n("no"))})` }, { text: `No ${n("no")}` });
      break;
    case "exit":
      if (n("exited")) numbers.push({ text: `${n("exited")} exited` });
      break;
    default:
      for (const [o, c] of Object.entries(st)) if (o !== "waiting" && o !== "failed") numbers.push({ text: `${c} ${outcomeText[o] ?? o}` });
  }
  if (n("failed")) numbers.push({ text: `${n("failed")} failed`, tone: "red" });
  return { label: stepLabel(s, kinds, templates), detail, numbers, here: j.at_step?.[s.id] ?? 0, problems: probs };
}

// StepResults is what a step has done and who is on it now, in its side
// panel: each number leads to the people behind it, as in email tools.
function StepResults({ ws, j, step, card }: { ws: string; j: Journey; step: Step; card: StepCard }) {
  const [runs, setRuns] = useState<JourneyRun[] | null>(null);
  const here = j.at_step?.[step.id] ?? 0;
  useEffect(() => {
    setRuns(null);
    if (!here) return;
    api.GET("/v1/workspaces/{ws}/journeys/{id}/runs", { params: { path: { ws, id: j.id }, query: { status: "active", step: step.id, limit: 20 } } }).then(({ data }) => setRuns(data?.data ?? []));
  }, [ws, j.id, step.id, here]);
  if (!card.numbers.length && !here) return null;
  return (
    <div className="space-y-2 rounded-md border p-3 text-[13px]">
      {card.numbers.length > 0 && <p><span className="font-medium">So far: </span>{card.numbers.map((n) => n.text).join(" · ")}</p>}
      {here > 0 && (
        <div>
          <span className="font-medium">On this step now: {here}</span>
          {!runs ? <Loading /> : (
            <ul className="mt-1 space-y-0.5">
              {runs.map((r) => (
                <li key={r.id} className="flex justify-between gap-2">
                  <span className="truncate">{r.name || "Unnamed person"}</span>
                  <span className="shrink-0 text-muted-foreground">{r.next_at ? `until ${stamp(r.next_at)}` : ago(r.entered_at)}</span>
                </li>
              ))}
              {here > runs.length && <li className="text-muted-foreground">and {here - runs.length} more</li>}
            </ul>
          )}
        </div>
      )}
    </div>
  );
}

// useRules is the workspace's rules, to link a trigger to the rule that
// emits its event. Someone who can't read rules sees no link.
function useRules(ws: string) {
  const [rules, setRules] = useState<Rule[]>([]);
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/rules", { params: { path: { ws } } }).then(({ data }) => setRules(data?.data ?? []));
  }, [ws]);
  return rules;
}

function useTemplates(ws: string) {
  const [templates, setTemplates] = useState<EmailTemplate[] | null>(null);
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/email-templates", { params: { path: { ws } } }).then(({ data }) =>
      setTemplates((data?.data ?? []).filter((t) => t.purpose === "lifecycle" && t.status === "active")),
    );
  }, [ws]);
  return templates;
}

// RuleDryRun is what the rule behind a trigger would have done lately.
function useDryRun(ws: string, rule?: Rule) {
  const [sim, setSim] = useState<RuleSimulation | null>(null);
  useEffect(() => {
    setSim(null);
    if (!rule) return;
    api.POST("/v1/workspaces/{ws}/rules/{id}/simulate", { params: { path: { ws, id: rule.id } }, body: { days: 30 } }).then(({ data }) => setSim(data ?? null));
  }, [ws, rule?.id]); // eslint-disable-line react-hooks/exhaustive-deps
  return sim;
}

function TriggerCard({ t, rule, sim, go }: { t: TriggerFields; rule?: Rule; sim: RuleSimulation | null; go: Go }) {
  return (
    <>
      <span className="truncate text-[13px] font-medium">{t.kind === "manual" ? "When someone is added" : <>When someone does <code>{t.event || "…"}</code></>}</span>
      {rule && (
        <span className="truncate text-muted-foreground">
          Emitted by the rule{" "}
          <button type="button" className="underline" onClick={(e) => { e.stopPropagation(); go("rules", rule.id); }}>{rule.name}</button>
        </span>
      )}
      <span className="truncate text-muted-foreground">
        {t.filter.length ? `Only if ${t.filter.map((c) => conditionText(c, [])).join(" and ")} · ` : ""}{t.entry === "once" ? "Once per person" : "Again after they finish"}{t.goal ? ` · Goal: ${t.goal}` : ""}
      </span>
      {sim && <span className="truncate text-muted-foreground" title={sim.note}>Dry run: {plural(sim.records, "person", "people")} in {sim.window_days} days{sim.truncated ? " (at least)" : ""}</span>}
    </>
  );
}

function JourneyDetail({ ws, id, canManage, back, go }: { ws: string; id: string; canManage: boolean; back: () => void; go: Go }) {
  const kinds = useStepKinds(ws);
  const templates = useTemplates(ws);
  const catalog = useFlowCatalog(ws);
  const rules = useRules(ws);
  const mobile = useIsMobile();
  const [j, setJ] = useState<Journey | null>(null);
  const [draft, setDraft] = useState<JourneyVersion | null>(null);
  const [versions, setVersions] = useState<JourneyVersion[] | null>(null);
  const [viewing, setViewing] = useState<JourneyVersion | null>(null);
  const [work, setWork] = useState<Work | null>(null);
  const [dirty, setDirty] = useState(false);
  const [panel, setPanel] = useState<{ step: string } | "trigger" | null>(null);
  const [picker, setPicker] = useState<Slot | null>(null);
  const [publishing, setPublishing] = useState(false);
  const [renaming, setRenaming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  // Changes save themselves a moment after the last edit, as in other flow
  // builders; undo and redo walk back through this session's edits.
  const [saving, setSaving] = useState<"saved" | "saving" | "failed">("saved");
  const [saveError, setSaveError] = useState("");
  const past = useRef<Work[]>([]);
  const future = useRef<Work[]>([]);
  const workRef = useRef<Work | null>(null);
  workRef.current = work;
  const saveRef = useRef<() => Promise<boolean>>(async () => false);
  const undoRef = useRef<(redo: boolean) => void>(() => {});

  const loadVersions = useCallback(async () => {
    const { data } = await api.GET("/v1/workspaces/{ws}/journeys/{id}/versions", { params: { path: { ws, id } } });
    setVersions(data?.data ?? []);
  }, [ws, id]);
  // load reads the journey; with fresh, it also starts the builder again
  // from what is saved (its own steps for a journey not live yet, else its
  // draft version).
  const load = useCallback(async (fresh = true) => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/journeys/{id}", { params: { path: { ws, id } } });
    if (error || !data) return setError(errorMessage(error));
    setJ(data);
    if (!fresh) return;
    let d: JourneyVersion | null = null;
    if (data.draft_updated_at) {
      const res = await api.GET("/v1/workspaces/{ws}/journeys/{id}/draft", { params: { path: { ws, id } } });
      d = res.data ?? null;
    }
    setDraft(d);
    setWork(data.status === "draft" ? workFrom(data) : d ? workFrom(d) : null);
    setDirty(false);
    past.current = [];
    future.current = [];
  }, [ws, id]);
  useEffect(() => {
    load();
    loadVersions();
  }, [load, loadVersions]);
  // Live numbers: look again every 30 seconds while people move through it.
  useEffect(() => {
    if (j?.status !== "active") return;
    const t = setInterval(() => document.visibilityState === "visible" && load(false), 30000);
    return () => clearInterval(t);
  }, [j?.status, load]);

  useEffect(() => {
    if (!dirty || !work) return;
    const t = setTimeout(() => saveRef.current(), 1200);
    return () => clearTimeout(t);
  }, [dirty, work]);
  useEffect(() => {
    if (!dirty && saving !== "saving") return;
    const warn = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty, saving]);
  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement | null;
      if (!(e.metaKey || e.ctrlKey) || t?.closest("input, textarea, select, [contenteditable]")) return;
      const k = e.key.toLowerCase();
      if (k !== "z" && k !== "y") return;
      e.preventDefault();
      undoRef.current(k === "y" || e.shiftKey);
    };
    window.addEventListener("keydown", key);
    return () => window.removeEventListener("keydown", key);
  }, []);

  const shown: Work | null = viewing ? workFrom(viewing) : work ?? (j ? workFrom(j) : null);
  const editable = !!(canManage && !mobile && !viewing && work && j && j.status !== "archived");
  const probs = useMemo(() => (shown ? problems(kinds, shown.steps, templates) : new Map<string, Problem[]>()), [shown, kinds, templates]);
  const rule = useMemo(() => shown?.trigger.kind === "event" ? rules.find((r) => r.emit_event === shown.trigger.event && r.status !== "archived") : undefined, [rules, shown?.trigger.kind, shown?.trigger.event]);
  const sim = useDryRun(ws, rule);

  if (error && !j) return <Empty title="Could not load this journey" action={<Button onClick={back}>All journeys</Button>}>{error}</Empty>;
  if (!j || !shown || !templates) return <Loading />;

  const errors = errorCount(probs);
  const live = j.status === "active" || j.status === "paused";
  const change = (w: Work) => {
    if (work) past.current = [...past.current.slice(-49), work];
    future.current = [];
    setWork(w);
    setDirty(true);
  };
  undoRef.current = (redo) => {
    if (!editable || !work) return;
    const from = redo ? future : past;
    const to = redo ? past : future;
    const w = from.current.pop();
    if (!w) return;
    to.current.push(work);
    track("ui.journey_change_undone", { redo });
    setWork(w);
    setDirty(true);
  };
  const setSteps = (steps: Step[]) => work && change({ ...work, steps });

  async function save(): Promise<boolean> {
    const sent = workRef.current;
    if (!sent || !j) return false;
    setSaving("saving");
    const res = j.status === "draft"
      ? await api.PATCH("/v1/workspaces/{ws}/journeys/{id}", { params: { path: { ws, id } }, body: bodyOf(sent) })
      : await api.PATCH("/v1/workspaces/{ws}/journeys/{id}/draft", { params: { path: { ws, id } }, body: bodyOf(sent) });
    if (res.error) {
      setSaving("failed");
      setSaveError(errorMessage(res.error));
      return false;
    }
    setSaving("saved");
    setSaveError("");
    // Edits made while it saved still need saving.
    if (workRef.current === sent) setDirty(false);
    if (j.status !== "draft") setDraft(res.data as JourneyVersion);
    else setJ({ ...(res.data as Journey), step_stats: j.step_stats, at_step: j.at_step, email_stats: j.email_stats });
    return true;
  }
  saveRef.current = save;

  async function act(to: "activate" | "pause" | "archive", reviewed = false) {
    const ask = {
      activate: ["Activate this journey?", shown!.trigger.kind === "event" ? `From now on, everyone who does ${shown!.trigger.event} starts it and gets its emails.` : "People you add start it and get its emails.", "Activate", false],
      pause: ["Pause this journey?", "No one new starts, and everyone in it stays where they are until you activate it again.", "Pause", false],
      archive: ["Archive this journey?", "It stops for good and everyone in it is taken out. This can't be undone.", "Archive", true],
    }[to] as [string, string, string, boolean];
    if (!reviewed && !(await confirmAction(...ask))) return;
    if (to === "activate" && dirty && !(await save())) return;
    setBusy(true);
    const path = `/v1/workspaces/{ws}/journeys/{id}/${to}` as "/v1/workspaces/{ws}/journeys/{id}/activate";
    const { data, error } = await api.POST(path, { params: { path: { ws, id } } });
    setBusy(false);
    if (error || !data) return toast(errorMessage(error), "error");
    toast({ activate: "Active.", pause: "Paused.", archive: "Archived." }[to]);
    setPublishing(false);
    await load();
    loadVersions();
  }

  async function edit() {
    setBusy(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/journeys/{id}/draft", { params: { path: { ws, id } } });
    setBusy(false);
    if (error || !data) return toast(errorMessage(error), "error");
    setViewing(null);
    setDraft(data);
    setWork(workFrom(data));
    setDirty(false);
    past.current = [];
    future.current = [];
    loadVersions();
  }

  async function discard() {
    if (!(await confirmAction("Throw away this draft?", "Your changes are lost. The live version keeps running as it is.", "Discard"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/journeys/{id}/draft", { params: { path: { ws, id } } });
    if (error) return toast(errorMessage(error), "error");
    toast("Draft discarded.");
    await load();
    loadVersions();
  }

  async function publish(runs: "keep" | "move") {
    if (dirty && !(await save())) return;
    setBusy(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/journeys/{id}/publish", { params: { path: { ws, id } }, body: { runs } });
    setBusy(false);
    if (error || !data) return toast(errorMessage(error), "error");
    setPublishing(false);
    toast(`Version ${data.version} is live.`);
    await load();
    loadVersions();
  }

  function addStep(slot: Slot, k: StepKind) {
    if (!work) return;
    const s = newStep(kinds, k.kind, work.steps, templates ?? [], work.trigger.goal);
    track("ui.journey_step_added", { kind: k.kind, group: k.group, at: slot.from === null ? "start" : slot.field });
    setSteps(insert(kinds, work.steps, slot, s));
    setPicker(null);
    setPanel({ step: s.id });
  }

  async function removeStep(id: string) {
    if (!work) return;
    const gone = work.steps.find((s) => s.id === id);
    const { steps, dropped } = remove(kinds, work.steps, id);
    if (dropped.length && !(await confirmAction("Remove this step?", `These steps only it leads to go too: ${dropped.map((d) => stepLabel(d, kinds, templates ?? [])).join(", ")}.`, "Remove"))) return;
    track("ui.journey_step_removed", { kind: gone?.kind, also_removed: dropped.length });
    setSteps(steps);
    setPanel(null);
  }

  const open = panel && panel !== "trigger" ? shown.steps.find((s) => s.id === panel.step) : undefined;
  const liveVersion = versions?.find((v) => v.status === "published" && v.version === j.version);
  const s = j.stats;
  const banner = viewing ? (
    <Notice tone="grey" action={<Button variant="ghost" onClick={() => setViewing(null)}>Back to {work && j.status !== "draft" ? "the draft" : "the live version"}</Button>}>
      Version {viewing.version}{viewing.published_at ? `, published ${stamp(viewing.published_at)}` : ""}. Read-only.{viewing.runs_active ? ` ${plural(viewing.runs_active, "person is", "people are")} still going through it.` : ""}
    </Notice>
  ) : draft && live ? (
    <Notice tone="blue">Editing a draft of version {j.version}. Everyone keeps going through version {j.version} until you publish.</Notice>
  ) : live && canManage && !mobile ? (
    <Notice tone="grey">Live and read-only. Press Edit to make a draft version; publish it to swap it in.</Notice>
  ) : null;

  return (
    <div className="space-y-6">
      <PageHeader
        back={{ label: "Journeys", onClick: back }}
        title={<span className="flex flex-wrap items-center gap-2">{j.name} <StatusBadge j={j} />{live && <Badge tone="grey">v{j.version}</Badge>}</span>}
        description={j.description || undefined}
        action={canManage && j.status !== "archived" && (
          <div className="flex flex-wrap gap-2">
            {editable && <SaveState state={dirty && saving === "saved" ? "pending" : saving} error={saveError} retry={() => saveRef.current()}
              undo={past.current.length > 0 ? () => undoRef.current(false) : undefined} redo={future.current.length > 0 ? () => undoRef.current(true) : undefined} />}
            {live && !draft && !mobile && <Button variant="ghost" onClick={edit} disabled={busy}>Edit</Button>}
            {live && draft && <Button variant="ghost" onClick={discard} disabled={busy}>Discard draft</Button>}
            {live && draft && <Button onClick={() => setPublishing(true)} disabled={busy}>Review and publish</Button>}
            {j.status === "active" && !draft && <Button variant="ghost" onClick={() => act("pause")} disabled={busy}>Pause</Button>}
            {j.status === "draft" && <Button onClick={() => setPublishing(true)} disabled={busy}>Review and activate</Button>}
            {j.status === "paused" && !draft && <Button onClick={() => act("activate")} disabled={busy || errors > 0} title={errors ? "Fix the problems first" : undefined}>Activate</Button>}
            <Menu items={[
              { label: "Name and description", onClick: () => setRenaming(true) },
              (j.status !== "draft" || j.stats.entered > 0) && { label: "Archive", onClick: () => act("archive"), danger: true },
            ]} />
          </div>
        )}
      />
      <Stats cols={5}>
        <Stat label="Entered" value={s.entered} />
        <Stat label="In it now" value={s.active} />
        <Stat label="Completed" value={s.completed} />
        <Stat label="Exited" value={s.exited} sub="goal, exit step or taken out" />
        <Stat label="Failed" value={s.failed} tone={s.failed ? "red" : undefined} />
      </Stats>
      {banner}
      {mobile && canManage && (work || live) && <p className="text-[13px] text-muted-foreground">Open this journey on a wider screen to change it.</p>}
      <Flow
        steps={shown.steps} kinds={kinds}
        trigger={<TriggerCard t={shown.trigger} rule={rule} sim={sim} go={go} />}
        card={(x) => stepCard(x, j, kinds, templates, shown.steps, probs.get(x.id) ?? [])}
        selected={open?.id}
        onOpen={(sid) => {
          track("ui.journey_step_opened", { kind: shown.steps.find((x) => x.id === sid)?.kind, editable });
          setPanel({ step: sid });
        }}
        onOpenTrigger={() => {
          track("ui.journey_trigger_opened", { editable });
          setPanel("trigger");
        }}
        onAdd={editable ? (slot) => {
          track("ui.journey_step_picker_opened", { from: slot.from === null ? "start" : "link" });
          setPicker(slot);
        } : undefined}
      />
      <ProblemSummary probs={probs} steps={shown.steps} kinds={kinds} templates={templates} live={live} onOpen={(sid) => setPanel(sid ? { step: sid } : "trigger")} />
      {versions && versions.some((v) => v.status === "published") && (
        <VersionsCard versions={versions} current={j.version} viewing={viewing} kinds={kinds} templates={templates} live={liveVersion}
          onView={(v) => {
            track("ui.journey_version_viewed", { version: v.version ?? undefined, current: v.version === j.version });
            setPanel(null);
            setViewing(v.version === j.version && !work ? null : v);
          }} />
      )}
      {j.stats.entered > 0 && <RunsCard ws={ws} j={j} canManage={canManage} go={go} />}
      {picker && <StepPicker kinds={kinds} onPick={(k) => addStep(picker, k)} onClose={() => setPicker(null)} onConnect={() => go("connectors")} />}
      {open && (
        <StepPanel step={open} steps={shown.steps} kinds={kinds} templates={templates} catalog={catalog} event={shown.trigger.kind === "event" ? shown.trigger.event : null}
          problems={probs.get(open.id) ?? []} editable={editable} go={go}
          extra={!viewing && <StepResults ws={ws} j={j} step={open} card={stepCard(open, j, kinds, templates, shown.steps, [])} />}
          onChange={(x) => work && setSteps(replace(work.steps, x))} onRemove={() => removeStep(open.id)} onClose={() => setPanel(null)} />
      )}
      {panel === "trigger" && (
        <TriggerPanel value={shown.trigger} editable={editable} catalog={catalog} onClose={() => setPanel(null)}
          onChange={(t) => work && change({ ...work, trigger: t })}
          rule={rule && (
            <p className="text-[12px] text-muted-foreground">
              The rule <button type="button" className="underline" onClick={() => go("rules", rule.id)}>{rule.name}</button> emits this event.
              {sim && ` It would have fired for ${plural(sim.records, "person", "people")} in the last ${sim.window_days} days.`}
            </p>
          )} />
      )}
      {publishing && work && (
        <ReviewDialog j={j} work={work} kinds={kinds} templates={templates} probs={probs} rule={rule} sim={sim} busy={busy}
          onPublish={j.status === "draft" ? () => act("activate", true) : publish}
          onOpen={(sid) => { setPublishing(false); setPanel(sid ? { step: sid } : "trigger"); }} onClose={() => setPublishing(false)} />
      )}
      {renaming && <RenameDialog ws={ws} j={j} onClose={() => setRenaming(false)} onSaved={(v) => { setRenaming(false); setJ({ ...v, step_stats: j.step_stats, at_step: j.at_step, email_stats: j.email_stats }); }} />}
    </div>
  );
}

function Notice({ tone, children, action }: { tone: "grey" | "blue"; children: ReactNode; action?: ReactNode }) {
  return (
    <div className={cn("flex flex-wrap items-center justify-between gap-2 rounded-md border px-3 py-2 text-[13px]", tone === "blue" ? "border-blue-500/40 bg-blue-500/5" : "bg-muted/40")}>
      <span>{children}</span>
      {action}
    </div>
  );
}

function ProblemSummary({ probs, steps, kinds, templates, live, onOpen }: { probs: Map<string, Problem[]>; steps: Step[]; kinds: StepKind[]; templates: EmailTemplate[]; live: boolean; onOpen: (id: string) => void }) {
  const rows = [...probs.entries()].flatMap(([sid, list]) => list.map((p) => ({ sid, p })));
  if (!rows.length) return null;
  const errors = rows.filter((r) => r.p.level === "error").length;
  return (
    <Card title={errors ? `${plural(errors, "problem")} to fix before it can ${live ? "be published" : "go live"}` : "Worth a look"}>
      <ul className="space-y-1 text-[13px]">
        {rows.map(({ sid, p }, i) => (
          <li key={i} className="flex gap-2">
            <span className={p.level === "error" ? "text-red-700 dark:text-red-400" : "text-amber-700 dark:text-amber-400"}>{p.level === "error" ? "Problem" : "Warning"}</span>
            {sid ? <button type="button" className="font-medium underline" onClick={() => onOpen(sid)}>{stepLabel(steps.find((s) => s.id === sid), kinds, templates)}</button> : <span className="font-medium">Journey</span>}
            <span className="text-muted-foreground">{p.text}</span>
          </li>
        ))}
      </ul>
    </Card>
  );
}

function ChangeList({ list }: { list: Change[] }) {
  if (!list.length) return <p className="text-[13px] text-muted-foreground">The steps are the same; the trigger, entry or goal may differ.</p>;
  const tone = { added: "green", removed: "red", changed: "amber" } as const;
  return (
    <ul className="space-y-1 text-[13px]">
      {list.map((c, i) => <li key={i} className="flex items-center gap-2"><Badge tone={tone[c.kind]}>{c.kind}</Badge>{c.label}</li>)}
    </ul>
  );
}

function VersionsCard({ versions, current, viewing, kinds, templates, live, onView }: {
  versions: JourneyVersion[]; current: number; viewing: JourneyVersion | null; kinds: StepKind[]; templates: EmailTemplate[]; live?: JourneyVersion; onView: (v: JourneyVersion) => void;
}) {
  const published = versions.filter((v) => v.status === "published");
  return (
    <Card title="Versions" description="Every version that went live is kept. People finish on the version they entered on unless a publish moved them.">
      <ul className="divide-y text-[13px]">
        {published.map((v) => (
          <li key={v.id} className="flex flex-wrap items-center justify-between gap-2 py-2">
            <span className="flex items-center gap-2">
              <span className="font-medium">Version {v.version}</span>
              {v.version === current && <Badge tone="green">Live</Badge>}
              {viewing?.id === v.id && <Badge tone="blue">Showing</Badge>}
              <span className="text-muted-foreground">{v.published_at ? `published ${ago(v.published_at)}` : ""}{v.runs_active ? ` · ${plural(v.runs_active, "person", "people")} in it` : ""}</span>
            </span>
            <Button variant="ghost" onClick={() => onView(v)}>View</Button>
          </li>
        ))}
      </ul>
      {viewing && live && viewing.id !== live.id && (
        <div className="mt-3 space-y-2 border-t pt-3">
          <span className="block text-[13px] font-medium">From version {viewing.version} to the live version {live.version}</span>
          <ChangeList list={changes(viewing.steps as Step[], live.steps as Step[], kinds, templates)} />
        </div>
      )}
    </Card>
  );
}

// SaveState says whether the builder's edits are saved, with undo and redo.
function SaveState({ state, error, retry, undo, redo }: { state: "pending" | "saving" | "saved" | "failed"; error: string; retry: () => void; undo?: () => void; redo?: () => void }) {
  const btn = "flex size-8 items-center justify-center rounded-md text-muted-foreground hover:bg-muted hover:text-foreground disabled:opacity-40";
  return (
    <span className="flex items-center gap-1 text-[12px] text-muted-foreground">
      <button type="button" className={btn} onClick={undo} disabled={!undo} aria-label="Undo" title="Undo (⌘Z)"><Undo2 className="size-4" /></button>
      <button type="button" className={btn} onClick={redo} disabled={!redo} aria-label="Redo" title="Redo (⇧⌘Z)"><Redo2 className="size-4" /></button>
      {state === "failed" ? (
        <span className="flex items-center gap-1 text-red-700 dark:text-red-400" title={error}>
          <CircleAlert className="size-3.5" />Not saved: {error.slice(0, 80)} <button type="button" className="underline" onClick={retry}>Try again</button>
        </span>
      ) : <span className="w-16">{state === "saved" ? "Saved" : "Saving…"}</span>}
    </span>
  );
}

// ReviewDialog is the last look before a journey goes live or a new
// version is published: what starts it, whether every step is ready, what
// changed, and what happens to the people already in it.
function ReviewDialog({ j, work, kinds, templates, probs, rule, sim, busy, onPublish, onOpen, onClose }: {
  j: Journey; work: Work; kinds: StepKind[]; templates: EmailTemplate[]; probs: Map<string, Problem[]>; rule?: Rule; sim: RuleSimulation | null; busy: boolean;
  onPublish: (runs: "keep" | "move") => void; onOpen: (step: string) => void; onClose: () => void;
}) {
  const [runs, setRuns] = useState<"keep" | "move">("keep");
  const first = j.status === "draft";
  const list = changes(j.steps as Step[], work.steps, kinds, templates);
  const ids = new Set(work.steps.map((s) => s.id));
  const stranded = Object.entries(j.at_step ?? {}).filter(([sid]) => !ids.has(sid)).reduce((n, [, c]) => n + c, 0);
  const rows = [...probs.entries()].flatMap(([sid, ps]) => ps.map((p) => ({ sid, p })));
  const errors = rows.filter((r) => r.p.level === "error");
  const warnings = rows.filter((r) => r.p.level === "warning");
  const emails = work.steps.filter((s) => s.kind === "email").length;
  const t = work.trigger;
  const item = (ok: boolean, text: ReactNode) => (
    <li className="flex items-start gap-2">
      {ok ? <Check className="mt-0.5 size-4 shrink-0 text-green-600 dark:text-green-400" /> : <CircleAlert className="mt-0.5 size-4 shrink-0 text-red-600 dark:text-red-400" />}
      <span>{text}</span>
    </li>
  );
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{first ? "Review and activate" : `Review and publish version ${j.version + 1}`}</DialogTitle>
          <DialogDescription>{first ? "Once it's on, it emails people as they qualify." : `It replaces version ${j.version} for everyone who starts from now on. Version ${j.version} is kept.`}</DialogDescription>
        </DialogHeader>
        <div className="space-y-4 text-[13px]">
          <div>
            <span className="font-medium">Who starts it</span>
            <p className="text-muted-foreground">
              {t.kind === "manual" ? "People you add, by hand or through the API." : <>Everyone who does <code>{t.event}</code>{t.filter.length ? `, if ${t.filter.map((c) => conditionText(c, [])).join(" and ")}` : ""}, {t.entry === "once" ? "once per person" : "again after they finish"}.</>}
              {rule && sim && ` Its rule ${rule.name} would have fired for ${plural(sim.records, "person", "people")} in the last ${sim.window_days} days.`}
            </p>
          </div>
          <ul className="space-y-1">
            {item(errors.length === 0, errors.length === 0 ? `All ${plural(work.steps.length, "step")} are set up` : `${plural(errors.length, "problem")} to fix first`)}
            {errors.map(({ sid, p }, i) => (
              <li key={i} className="ml-6 text-red-700 dark:text-red-400">
                {sid ? <button type="button" className="underline" onClick={() => onOpen(sid)}>{stepLabel(work.steps.find((s) => s.id === sid), kinds, templates)}</button> : "Journey"}: {p.text}
              </li>
            ))}
            {emails > 0 && item(!errors.some((r) => r.p.template), `${plural(emails, "email")}, each from a published template, skipping anyone who opted out`)}
            {warnings.length > 0 && <li className="ml-6 text-amber-700 dark:text-amber-400">{plural(warnings.length, "thing")} worth a look: {warnings.map((w) => w.p.text).join(" ")}</li>}
          </ul>
          {!first && (
            <div>
              <span className="font-medium">What changes</span>
              <ChangeList list={list} />
            </div>
          )}
          {!first && j.stats.active > 0 && (
            <fieldset className="space-y-2">
              <legend className="mb-1 font-medium">The {plural(j.stats.active, "person", "people")} in it now</legend>
              <label className="flex items-start gap-2">
                <input type="radio" name="runs" checked={runs === "keep"} onChange={() => setRuns("keep")} className="mt-1" />
                <span><span className="font-medium">Finish on their version</span> (recommended)<span className="block text-muted-foreground">They go on exactly as when they entered. Only new people get the new version.</span></span>
              </label>
              <label className="flex items-start gap-2">
                <input type="radio" name="runs" checked={runs === "move"} onChange={() => setRuns("move")} className="mt-1" />
                <span><span className="font-medium">Move onto the new version</span><span className="block text-muted-foreground">
                  They carry on from the same step in the new version.{stranded ? ` ${plural(stranded, "person is", "people are")} on a step it removes and will exit.` : ""}
                </span></span>
              </label>
            </fieldset>
          )}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
            <Button onClick={() => onPublish(runs)} disabled={busy || errors.length > 0}>{first ? "Activate" : "Publish"}</Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}

function RenameDialog({ ws, j, onClose, onSaved }: { ws: string; j: Journey; onClose: () => void; onSaved: (j: Journey) => void }) {
  const [name, setName] = useState(j.name);
  const [description, setDescription] = useState(j.description);
  const [error, setError] = useState("");
  async function save(e: FormEvent) {
    e.preventDefault();
    const { data, error } = await api.PATCH("/v1/workspaces/{ws}/journeys/{id}", { params: { path: { ws, id: j.id } }, body: { name, description } });
    if (error || !data) return setError(errorMessage(error));
    onSaved(data);
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Name and description</DialogTitle>
          <DialogDescription>These change straight away, even while it runs.</DialogDescription>
        </DialogHeader>
        <form onSubmit={save} className="space-y-3">
          <Input label="Name" value={name} onChange={(e) => setName(e.target.value)} maxLength={200} required autoFocus />
          <Input label="Description" value={description} onChange={(e) => setDescription(e.target.value)} maxLength={2000} placeholder="Optional" />
          <ErrorText>{error}</ErrorText>
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
            <Button disabled={!name.trim()}>Save</Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
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
                  {r.status === "active" ? `${stepLabel(j.steps.find((s) => s.id === r.step) as Step | undefined) || "Starting"}${r.next_at ? `, until ${stamp(r.next_at)}` : ""}` : `${runText[r.status]}${r.exit_reason ? `: ${exitText[r.exit_reason] ?? r.exit_reason}` : ""}`}
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
  const byID = new Map(j.steps.map((s) => [s.id, s as Step]));
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
                  <span>{stepLabel(byID.get(h.step)) || h.step}: <span className="text-muted-foreground">{outcomeText[h.outcome] ?? h.outcome}{h.detail ? ` (${h.detail})` : ""}</span></span>
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
