// Rules: the rules engine of the customer data platform. A rule turns what
// people do, or don't do in time, into an event of its own on their
// timeline, which journeys start on. This page lists rules and has the
// rule card: a sentence builder ("When … and doesn't … within …, if …,
// emit …"), its dry run, the journeys its event starts, and "When this
// fires, do…", which makes a journey started by its event and opens it in
// the flow builder.

import { useCallback, useEffect, useState, type ReactNode } from "react";
import type { EmailTemplate, Journey, Rule, RuleCondition, RuleSimulation, RuleTrigger, SmartList } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { track } from "@/lib/analytics";
import { Badge, Button, Card, confirmAction, Empty, ErrorText, field, Input, Loading, Menu, PageHeader, Stat, Stats, toast } from "../ui";
import { ago, plural, stamp } from "../format";
import { DataTable, type ColumnDef } from "@/components/data-table";
import { clean, newStep } from "@/components/journey-flow/model";
import { StepPicker, useStepKinds } from "@/components/journey-flow/step-picker";
import { Conditions, useFlowCatalog, type FlowCatalog } from "@/components/journey-flow/step-panel";
import { KitNotice } from "@/components/kit-notice";
import type { Go } from "./Lifecycle";

type Status = Rule["status"];
const statusTone: Record<Status, "green" | "grey" | "amber"> = { draft: "grey", active: "green", paused: "amber", archived: "grey" };
const statusText: Record<Status, string> = { draft: "Draft", active: "Active", paused: "Paused", archived: "Archived" };

const opText: Record<RuleCondition["op"], string> = { eq: "is", neq: "is not", gt: "is more than", lt: "is less than", contains: "contains", exists: "is set", not_exists: "is not set" };

function duration(s: number) {
  if (s % 86400 === 0) return plural(s / 86400, "day");
  if (s % 3600 === 0) return plural(s / 3600, "hour");
  return plural(Math.round(s / 60), "minute");
}

// sentence reads a rule out loud, as the card builds it.
export function sentence(r: Pick<Rule, "trigger" | "conditions" | "match" | "emit_event" | "record_type">, lists: SmartList[] = []): string {
  const t = r.trigger;
  const when = {
    event: `When someone does ${t.event || "…"}`,
    absence: t.start_event
      ? `When someone does ${t.start_event} and doesn't do ${t.event || "…"} within ${duration(t.within_seconds ?? 0)}`
      : `When someone hasn't done ${t.event || "…"} for ${duration(t.within_seconds ?? 0)}`,
    change: `When a ${r.record_type || "person"} comes to match`,
    audience: `When someone ${t.on === "leave" ? "leaves" : "joins"} ${lists.find((l) => l.id === t.list_id)?.name ?? "a smart list"}`,
  }[t.kind];
  const ifs = r.conditions.map((c) => {
    const what = c.attribute ?? `event ${c.property}`;
    return c.op === "exists" || c.op === "not_exists" ? `${what} ${opText[c.op]}` : `${what} ${opText[c.op]} ${JSON.stringify(c.value)}`;
  });
  return `${when}${ifs.length ? `, if ${ifs.join(r.match === "any" ? " or " : " and ")}` : ""}, emit ${r.emit_event || "…"}.`;
}

function StatusBadge({ r }: { r: Rule }) {
  return <Badge tone={statusTone[r.status]}>{statusText[r.status]}</Badge>;
}

function useLists(ws: string) {
  const [lists, setLists] = useState<SmartList[]>([]);
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/smart-lists", { params: { path: { ws } } }).then(({ data }) => setLists(data?.data ?? []));
  }, [ws]);
  return lists;
}

export function Rules({ ws, canManage, open, onOpen, go }: { ws: string; canManage: boolean; open: string; onOpen: (id: string) => void; go: Go }) {
  const [list, setList] = useState<Rule[] | null>(null);
  const [archived, setArchived] = useState(false);
  const [error, setError] = useState("");
  const lists = useLists(ws);
  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/rules", { params: { path: { ws }, query: archived ? { status: "archived" } : {} } });
    if (error) setError(errorMessage(error));
    setList((data?.data ?? []).filter((r) => archived || r.status !== "archived"));
  }, [ws, archived]);
  useEffect(() => {
    if (!open) load();
  }, [load, open]);

  if (open) return <RuleDetail ws={ws} id={open === "new" ? "" : open} canManage={canManage} lists={lists} back={() => onOpen("")} onCreated={onOpen} go={go} />;
  if (!list) return <Loading />;
  const add = canManage && <Button onClick={() => onOpen("new")}>New rule</Button>;
  const columns: ColumnDef<Rule>[] = [
    {
      id: "name", header: "Rule", accessorFn: (r) => r.name,
      cell: ({ row: { original: r } }) => (
        <span className="block min-w-0">
          <span className="block truncate font-medium">{r.name}</span>
          <span className="block truncate text-[12px] text-muted-foreground">{sentence(r, lists)}</span>
        </span>
      ),
    },
    { id: "status", header: "Status", accessorFn: (r) => r.status, cell: ({ row: { original: r } }) => <StatusBadge r={r} /> },
    { id: "emits", header: "Emits", accessorFn: (r) => r.emit_event, cell: ({ row: { original: r } }) => <code className="text-[12px]">{r.emit_event}</code> },
    { id: "updated", header: "Changed", accessorFn: (r) => r.updated_at, cell: ({ row: { original: r } }) => <span className="whitespace-nowrap text-muted-foreground" title={stamp(r.updated_at)}>{ago(r.updated_at)}</span> },
  ];
  return (
    <div className="space-y-6">
      <PageHeader
        title="Rules"
        description="A rule watches what people do, or don't do in time, and emits an event of its own on their timeline: signed up and didn't activate within 7 days emits customer.stalled. Journeys start on it."
        action={add}
      />
      <ErrorText>{error}</ErrorText>
      {list.length === 0 && !archived ? (
        <Empty title="No rules yet" action={add}>
          A rule never sends anything itself. It emits an event, and the journeys that start on that event do the sending, so one event can drive several of them.
        </Empty>
      ) : (
        <DataTable
          columns={columns} data={list} rowId={(r) => r.id} onRowClick={(r) => onOpen(r.id)} searchPlaceholder="Search rules"
          toolbar={
            <select aria-label="Show" className={field} value={archived ? "archived" : ""} onChange={(e) => setArchived(e.target.value === "archived")}>
              <option value="">Current rules</option>
              <option value="archived">Archived</option>
            </select>
          }
          mobile={(r) => (
            <div className="flex min-w-0 items-center gap-3">
              <span className="min-w-0 flex-1">
                <span className="block truncate text-[13px] font-medium">{r.name}</span>
                <span className="block truncate text-[12px] text-muted-foreground">{sentence(r, lists)}</span>
              </span>
              <StatusBadge r={r} />
            </div>
          )}
        />
      )}
    </div>
  );
}

// ---- The rule card ----

type Form = {
  name: string; description: string; record_type: string; trigger: RuleTrigger; conditions: RuleCondition[]; match: "all" | "any";
  emit_event: string; fire: "every_time" | "once"; cooldown_seconds: number; manual_edits: boolean;
};

const blank: Form = {
  name: "", description: "", record_type: "person", trigger: { kind: "absence", start_event: "", event: "", within_seconds: 7 * 86400 },
  conditions: [], match: "all", emit_event: "", fire: "every_time", cooldown_seconds: 0, manual_edits: false,
};

function formOf(r: Rule): Form {
  return { name: r.name, description: r.description, record_type: r.record_type, trigger: r.trigger, conditions: r.conditions, match: r.match, emit_event: r.emit_event, fire: r.fire, cooldown_seconds: r.cooldown_seconds, manual_edits: r.manual_edits };
}

function bodyOf(f: Form) {
  const t = f.trigger;
  const trigger: RuleTrigger = { kind: t.kind };
  if (t.kind === "event") trigger.event = t.event;
  if (t.kind === "absence") Object.assign(trigger, { event: t.event, within_seconds: t.within_seconds, ...(t.start_event ? { start_event: t.start_event } : {}) });
  if (t.kind === "audience") Object.assign(trigger, { list_id: t.list_id, on: t.on ?? "enter" });
  return { ...f, trigger, record_type: t.kind === "change" ? f.record_type : "person" };
}

type Unit = 3600 | 86400;

// Word is one blank in the sentence.
function Word({ children }: { children: ReactNode }) {
  return <span className="inline-flex items-center align-middle">{children}</span>;
}

function Sentence({ f, set, catalog, lists, disabled }: { f: Form; set: (f: Form) => void; catalog: FlowCatalog; lists: SmartList[]; disabled: boolean }) {
  const t = f.trigger;
  const setT = (x: Partial<RuleTrigger>) => set({ ...f, trigger: { ...t, ...x } });
  const events = catalog.events.map((e) => e.name);
  const unit: Unit = (t.within_seconds ?? 0) % 86400 === 0 ? 86400 : 3600;
  const ev = (value: string | undefined, onChange: (v: string) => void, label: string, placeholder: string) => (
    <input aria-label={label} className={`${field} w-44`} list="rule-events" value={value ?? ""} placeholder={placeholder} disabled={disabled}
      onChange={(e) => onChange(e.target.value.trim())} pattern="[a-z][a-z0-9_]*(\.[a-z0-9_]+)*" />
  );
  return (
    <div className="space-y-3 text-[14px] leading-9">
      <datalist id="rule-events">{events.map((e) => <option key={e} value={e} />)}</datalist>
      <p className="flex flex-wrap items-center gap-x-2">
        <span className="font-medium">When</span>
        <Word>
          <select aria-label="Trigger" className={field} value={t.kind} disabled={disabled} onChange={(e) => {
            const kind = e.target.value as RuleTrigger["kind"];
            track("ui.rule_trigger_picked", { kind });
            set({ ...f, trigger: { kind, ...(kind === "absence" ? { within_seconds: 7 * 86400 } : {}), ...(kind === "audience" ? { on: "enter" } : {}) } });
          }}>
            <option value="event">someone does something</option>
            <option value="absence">someone doesn't do something in time</option>
            <option value="change">a record comes to match</option>
            <option value="audience">someone joins or leaves a smart list</option>
          </select>
        </Word>
      </p>
      {t.kind === "event" && <p className="flex flex-wrap items-center gap-x-2"><span>they do</span><Word>{ev(t.event, (v) => setT({ event: v }), "Event", "product_activated")}</Word></p>}
      {t.kind === "absence" && (
        <p className="flex flex-wrap items-center gap-x-2">
          <span>they do</span><Word>{ev(t.start_event, (v) => setT({ start_event: v }), "Start event", "signed_up (or leave empty)")}</Word>
          <span className="font-medium">and doesn't</span><span>do</span><Word>{ev(t.event, (v) => setT({ event: v }), "Event", "product_activated")}</Word>
          <span className="font-medium">within</span>
          <Word>
            <input aria-label="How long" type="number" min={1} className={`${field} w-20`} value={String((t.within_seconds ?? unit) / unit)} disabled={disabled}
              onChange={(e) => setT({ within_seconds: Math.max(1, Number(e.target.value)) * unit })} />
            <select aria-label="Unit" className={`${field} ml-1`} value={unit} disabled={disabled} onChange={(e) => setT({ within_seconds: ((t.within_seconds ?? unit) / unit) * Number(e.target.value) })}>
              <option value={3600}>hours</option>
              <option value={86400}>days</option>
            </select>
          </Word>
          <span className="block w-full text-[12px] leading-5 text-muted-foreground">Leave the first event empty for inactivity: each time they do the second, the clock starts again.</span>
        </p>
      )}
      {t.kind === "change" && (
        <p className="flex flex-wrap items-center gap-x-2">
          <span>a</span>
          <Word>
            <select aria-label="Record type" className={field} value={f.record_type} disabled={disabled} onChange={(e) => set({ ...f, record_type: e.target.value })}>
              {(catalog.types.length ? catalog.types : [{ key: "person", name: "Person" }]).map((x) => <option key={x.key} value={x.key}>{x.name.toLowerCase()}</option>)}
            </select>
          </Word>
          <span>comes to match the conditions below</span>
          <label className="flex items-center gap-1.5 text-[12px] leading-5 text-muted-foreground">
            <input type="checkbox" checked={f.manual_edits} disabled={disabled} onChange={(e) => set({ ...f, manual_edits: e.target.checked })} />
            also when someone changes it by hand
          </label>
        </p>
      )}
      {t.kind === "audience" && (
        <p className="flex flex-wrap items-center gap-x-2">
          <span>they</span>
          <Word>
            <select aria-label="Joins or leaves" className={field} value={t.on ?? "enter"} disabled={disabled} onChange={(e) => setT({ on: e.target.value as "enter" | "leave" })}>
              <option value="enter">join</option>
              <option value="leave">leave</option>
            </select>
          </Word>
          <Word>
            <select aria-label="Smart list" className={field} value={t.list_id ?? ""} disabled={disabled} onChange={(e) => setT({ list_id: e.target.value || undefined })}>
              <option value="">Choose a smart list</option>
              {lists.map((l) => <option key={l.id} value={l.id}>{l.name}</option>)}
            </select>
          </Word>
        </p>
      )}
      <div className="space-y-2 leading-normal">
        <p className="flex flex-wrap items-center gap-2">
          <span className="font-medium">if</span>
          <select aria-label="Match" className={field} value={f.match} disabled={disabled} onChange={(e) => set({ ...f, match: e.target.value as "all" | "any" })}>
            <option value="all">all of these hold</option>
            <option value="any">any of these holds</option>
          </select>
          {f.conditions.length === 0 && <span className="text-[12px] text-muted-foreground">(no conditions: always)</span>}
        </p>
        <Conditions list={f.conditions} onChange={(c) => set({ ...f, conditions: c })} emailSteps={[]} filter catalog={catalog}
          event={t.kind === "absence" ? t.start_event : t.event} disabled={disabled} />
        {(t.kind === "change" || t.kind === "audience") && <p className="text-[12px] text-muted-foreground">This kind of rule tests their fields only.</p>}
      </div>
      <p className="flex flex-wrap items-center gap-x-2">
        <span className="font-medium">emit</span>
        <Word>
          <input aria-label="Emit event" className={`${field} w-52`} value={f.emit_event} placeholder="customer.stalled" disabled={disabled}
            onChange={(e) => set({ ...f, emit_event: e.target.value.trim() })} pattern="[a-z][a-z0-9_]*(\.[a-z0-9_]+)*" required />
        </Word>
        <Word>
          <select aria-label="How often" className={field} value={f.fire} disabled={disabled} onChange={(e) => set({ ...f, fire: e.target.value as Form["fire"] })}>
            <option value="every_time">every time</option>
            <option value="once">once per {f.record_type === "person" || t.kind !== "change" ? "person" : "record"}, ever</option>
          </select>
        </Word>
        {f.fire === "every_time" && (
          <>
            <span>at most once every</span>
            <Word>
              <input aria-label="Cooldown in days" type="number" min={0} className={`${field} w-16`} value={String(Math.round(f.cooldown_seconds / 86400))} disabled={disabled}
                onChange={(e) => set({ ...f, cooldown_seconds: Math.max(0, Number(e.target.value)) * 86400 })} />
            </Word>
            <span>days</span>
          </>
        )}
      </p>
    </div>
  );
}

function RuleDetail({ ws, id, canManage, lists, back, onCreated, go }: { ws: string; id: string; canManage: boolean; lists: SmartList[]; back: () => void; onCreated: (id: string) => void; go: Go }) {
  const catalog = useFlowCatalog(ws);
  const kinds = useStepKinds(ws);
  const [r, setR] = useState<Rule | null>(null);
  const [f, setF] = useState<Form | null>(id ? null : blank);
  const [dirty, setDirty] = useState(!id);
  const [journeys, setJourneys] = useState<Journey[]>([]);
  const [sim, setSim] = useState<RuleSimulation | null>(null);
  const [days, setDays] = useState(30);
  const [simming, setSimming] = useState(false);
  const [picking, setPicking] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    if (!id) return;
    const { data, error } = await api.GET("/v1/workspaces/{ws}/rules/{id}", { params: { path: { ws, id } } });
    if (error || !data) return setError(errorMessage(error));
    setR(data);
    setF(formOf(data));
    setDirty(false);
  }, [ws, id]);
  useEffect(() => {
    load();
    api.GET("/v1/workspaces/{ws}/journeys", { params: { path: { ws } } }).then(({ data }) => setJourneys(data?.data ?? []));
  }, [load, ws]);

  if (error && !f) return <Empty title="Could not load this rule" action={<Button onClick={back}>All rules</Button>}>{error}</Empty>;
  if (!f) return <Loading />;
  const editable = canManage && (!r || r.status === "draft" || r.status === "paused");
  const set = (x: Form) => {
    setF(x);
    setDirty(true);
    setSim(null);
  };

  async function save() {
    setBusy(true);
    setError("");
    const res = r
      ? await api.PATCH("/v1/workspaces/{ws}/rules/{id}", { params: { path: { ws, id: r.id } }, body: bodyOf(f!) })
      : await api.POST("/v1/workspaces/{ws}/rules", { params: { path: { ws } }, body: bodyOf(f!) });
    setBusy(false);
    if (res.error || !res.data) return setError(errorMessage(res.error));
    toast(r ? "Saved." : "Draft saved. Dry-run it, then activate it.");
    if (!r) return onCreated(res.data.id);
    setR(res.data);
    setF(formOf(res.data));
    setDirty(false);
  }

  async function act(to: "activate" | "pause" | "archive") {
    const ask = {
      activate: ["Activate this rule?", "It acts on what happens from now on: earlier events and late data never fire it.", "Activate", false],
      pause: ["Pause this rule?", "It fires for no one until you activate it again; clocks that run out meanwhile are dropped.", "Pause", false],
      archive: ["Archive this rule?", "It stops for good. Its firings stay.", "Archive", true],
    }[to] as [string, string, string, boolean];
    if (!(await confirmAction(...ask))) return;
    setBusy(true);
    const path = `/v1/workspaces/{ws}/rules/{id}/${to}` as "/v1/workspaces/{ws}/rules/{id}/activate";
    const { data, error } = await api.POST(path, { params: { path: { ws, id: r!.id } } });
    setBusy(false);
    if (error || !data) return toast(errorMessage(error), "error");
    toast({ activate: "Active.", pause: "Paused.", archive: "Archived." }[to]);
    load();
  }

  async function dryRun() {
    if (!r) return;
    setSimming(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/rules/{id}/simulate", { params: { path: { ws, id: r.id } }, body: { days } });
    setSimming(false);
    if (error || !data) return toast(errorMessage(error), "error");
    setSim(data);
  }

  // followUp makes a journey that starts on the rule's event, with the
  // step picked, and opens it in the flow builder.
  async function followUp(kind: string) {
    if (!r) return;
    setPicking(false);
    track("ui.rule_followup_picked", { kind });
    const { data: tpl } = await api.GET("/v1/workspaces/{ws}/email-templates", { params: { path: { ws } } });
    const templates = (tpl?.data ?? []).filter((t: EmailTemplate) => t.purpose === "lifecycle" && t.status === "active");
    const step = newStep(kinds, kind, [], templates);
    const steps = kind === "email" && !step.template ? [] : clean([step]);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/journeys", {
      params: { path: { ws } },
      body: { name: `${r.name}: follow-up`.slice(0, 200), trigger: { kind: "event", event: r.emit_event }, steps },
    });
    if (error || !data) return toast(errorMessage(error), "error");
    toast("Journey drafted. Finish its steps, then activate it.");
    go("journeys", data.id);
  }

  const starts = r ? journeys.filter((j) => j.trigger.kind === "event" && j.trigger.event === r.emit_event) : [];
  const ends = r ? journeys.filter((j) => j.goal_event === r.emit_event) : [];
  return (
    <div className="space-y-6">
      <PageHeader
        back={{ label: "Rules", onClick: back }}
        title={r ? <span className="flex flex-wrap items-center gap-2">{r.name} <StatusBadge r={r} />{r.status !== "draft" && <Badge tone="grey">v{r.version}</Badge>}</span> : "New rule"}
        description={r?.description || undefined}
        action={canManage && r && r.status !== "archived" && (
          <div className="flex flex-wrap gap-2">
            {r.status === "active" && <Button variant="ghost" onClick={() => act("pause")} disabled={busy}>Pause</Button>}
            {(r.status === "draft" || r.status === "paused") && <Button onClick={() => act("activate")} disabled={busy || dirty} title={dirty ? "Save first" : undefined}>Activate</Button>}
            <Menu items={[{ label: "Archive", onClick: () => act("archive"), danger: true }]} />
          </div>
        )}
      />
      {r?.kit && r.status === "draft" && <KitNotice kit={r.kit} then="Dry-run it below to see who it would have fired for, then press Activate." />}
      {r?.stats && (
        <Stats cols={3}>
          <Stat label="Fired" value={r.stats.firings} />
          <Stat label="For" value={plural(r.stats.records, "record")} />
          <Stat label="Last fired" value={r.stats.last_fired_at ? ago(r.stats.last_fired_at) : "Never"} />
        </Stats>
      )}
      <Card title="The rule" description={r?.status === "active" && canManage ? "Pause it to change it." : undefined}>
        <div className="space-y-4">
          <div className="grid gap-3 sm:grid-cols-2">
            <Input label="Name" value={f.name} onChange={(e) => set({ ...f, name: e.target.value })} maxLength={200} required disabled={!editable} placeholder="Stalled after sign-up" />
            <Input label="Description" value={f.description} onChange={(e) => set({ ...f, description: e.target.value })} maxLength={2000} disabled={!editable} placeholder="Optional" />
          </div>
          <Sentence f={f} set={set} catalog={catalog} lists={lists} disabled={!editable} />
          <p className="rounded-md bg-muted/50 px-3 py-2 text-[13px]">{sentence(f, lists)}</p>
          <ErrorText>{error}</ErrorText>
          {editable && (
            <div className="flex justify-end gap-2">
              {!r && <Button variant="ghost" onClick={back}>Cancel</Button>}
              <Button onClick={save} disabled={busy || !dirty || !f.name.trim() || !f.emit_event}>{r ? "Save" : "Save draft"}</Button>
            </div>
          )}
        </div>
      </Card>
      {r && (
        <div className="grid gap-4 lg:grid-cols-2">
          <Card title="Dry run" description="What it would have done lately, as if it had been active. Nothing is stored, emitted or sent.">
            <div className="space-y-3 text-[13px]">
              <div className="flex flex-wrap items-center gap-2">
                <select aria-label="Days" className={field} value={days} onChange={(e) => { setDays(Number(e.target.value)); setSim(null); }}>
                  <option value={7}>Last 7 days</option>
                  <option value={30}>Last 30 days</option>
                  <option value={90}>Last 90 days</option>
                </select>
                <Button variant="ghost" onClick={dryRun} disabled={simming || dirty} title={dirty ? "Save first" : undefined}>{simming ? "Running…" : "Dry run"}</Button>
              </div>
              {sim && (
                <>
                  <p className="text-[15px]">Would have fired for <b>{plural(sim.records, "record")}</b>, {plural(sim.firings, "time")}, in the last {sim.window_days} days{sim.truncated ? " (at least)" : ""}.</p>
                  <p className="text-[12px] text-muted-foreground">{sim.note}</p>
                </>
              )}
            </div>
          </Card>
          <Card title="Where its event goes" description={<>Journeys that start on <code>{r.emit_event}</code>, or end when it happens.</>}>
            <div className="space-y-3 text-[13px]">
              {starts.length === 0 && ends.length === 0 && <p className="text-muted-foreground">Nothing uses it yet.</p>}
              {starts.length > 0 && (
                <div>
                  <span className="font-medium">Starts {plural(starts.length, "journey")}</span>
                  <ul className="mt-1 space-y-1">{starts.map((j) => <li key={j.id}><button className="underline" onClick={() => go("journeys", j.id)}>{j.name}</button> <span className="text-muted-foreground">({statusText[j.status]})</span></li>)}</ul>
                </div>
              )}
              {ends.length > 0 && (
                <div>
                  <span className="font-medium">Is the goal of {plural(ends.length, "journey")}</span>
                  <ul className="mt-1 space-y-1">{ends.map((j) => <li key={j.id}><button className="underline" onClick={() => go("journeys", j.id)}>{j.name}</button></li>)}</ul>
                </div>
              )}
              {canManage && (
                <div className="border-t pt-3">
                  <Button onClick={() => { track("ui.journey_step_picker_opened", { from: "rule" }); setPicking(true); }} disabled={dirty}>When this fires, do…</Button>
                  <p className="mt-1 text-[12px] text-muted-foreground">Pick a first step: a draft journey that starts on {r.emit_event} opens in the flow builder.</p>
                </div>
              )}
            </div>
          </Card>
        </div>
      )}
      {picking && <StepPicker kinds={kinds.filter((k) => k.kind !== "exit")} onPick={(k) => followUp(k.kind)} onClose={() => setPicking(false)} onConnect={() => go("connectors")} />}
    </div>
  );
}
