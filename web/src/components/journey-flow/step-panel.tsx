// The side panels of the flow builder: one step's settings, and the
// trigger's. Each shows the template fields it can use (person.* and
// event.*) from the catalog, and suggests field, property and event names
// as you type. Read-only panels show the same form, disabled.

import { useEffect, useState, type ReactNode } from "react";
import type { EmailTemplate, JourneyCondition, Schemas } from "@outflare/sdk";
import { api } from "@/api";
import { Button, CopyButton, field, Input } from "@/ui";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import type { Go } from "@/pages/Lifecycle";
import { track } from "@/lib/analytics";
import { kindOf, leadsTo, opText, outputLabel, outputs, stepLabel, type Problem, type SettingsSchema, type Step, type StepKind } from "./model";

type EventDefinition = Schemas["EventDefinition"];

// ---- The catalog: events, their properties and people's fields ----

export type FlowCatalog = { events: EventDefinition[]; personFields: { key: string; label: string }[]; types: { key: string; name: string }[] };

// useFlowCatalog reads the approved events, record types and person
// fields, for suggestions. Someone who can't read the catalog gets none,
// and can still type names.
export function useFlowCatalog(ws: string): FlowCatalog {
  const [catalog, setCatalog] = useState<FlowCatalog>({ events: [], personFields: [], types: [] });
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/catalog", { params: { path: { ws } } }).then(({ data }) => {
      if (!data) return;
      const person = data.types.find((t) => t.key === "person");
      setCatalog({
        events: data.events.filter((e) => e.status === "approved"),
        personFields: (person?.fields ?? []).filter((f) => !f.archived).map((f) => ({ key: f.key, label: f.label })),
        types: data.types.filter((t) => !t.archived).map((t) => ({ key: t.key, name: t.name })),
      });
    });
  }, [ws]);
  return catalog;
}

function propertiesOf(c: FlowCatalog, event?: string | null) {
  return c.events.find((e) => e.name === event)?.properties ?? [];
}

export function EventInput({ id, label, value, onChange, events, hint, required, placeholder = "signed_up" }: {
  id: string; label: string; value: string; onChange: (v: string) => void; events: string[]; hint?: string; required?: boolean; placeholder?: string;
}) {
  return (
    <>
      <Input label={label} hint={hint} value={value} onChange={(e) => onChange(e.target.value.trim())} placeholder={placeholder} list={id} required={required} pattern="[a-z][a-z0-9_]*(\.[a-z0-9_]+)*" />
      <datalist id={id}>{events.map((e) => <option key={e} value={e} />)}</datalist>
    </>
  );
}

// TemplateFields lists what an email can fill in, each with a copy button.
export function TemplateFields({ catalog, event }: { catalog: FlowCatalog; event?: string | null }) {
  const person = ["first_name", "name", "email", ...catalog.personFields.map((f) => f.key).filter((k) => !["first_name", "name", "email"].includes(k))];
  const props = propertiesOf(catalog, event).map((p) => p.key);
  const chip = (v: string) => (
    <span key={v} className="inline-flex items-center gap-0.5 rounded border bg-muted/40 py-px pr-0.5 pl-1.5 font-mono text-[11px]">
      {`{{${v}}}`}<CopyButton text={`{{${v}}}`} label={`Copy ${v}`} />
    </span>
  );
  return (
    <div className="space-y-2 text-[12px]">
      <div>
        <span className="mb-1 block font-medium">Their fields</span>
        <div className="flex flex-wrap gap-1">{person.slice(0, 24).map((k) => chip(`person.${k}`))}</div>
      </div>
      <div>
        <span className="mb-1 block font-medium">The event's properties</span>
        {props.length ? <div className="flex flex-wrap gap-1">{props.map((k) => chip(`event.${k}`))}</div>
          : <p className="text-muted-foreground">{event ? `The catalog lists no properties for ${event}. Any property it sends works as {{event.name}}.` : "People added by hand bring the data they were added with, as {{event.name}}."}</p>}
      </div>
    </div>
  );
}

// ---- Conditions ----

type Test = "attribute" | "property" | "event" | "email";

function testOf(c: JourneyCondition): Test {
  return c.event !== undefined ? "event" : c.email !== undefined ? "email" : c.property !== undefined ? "property" : "attribute";
}

function parseValue(v: string): unknown {
  if (v === "true" || v === "false") return v === "true";
  if (v.trim() !== "" && !Number.isNaN(Number(v))) return Number(v);
  return v;
}

// Conditions edits a list of tests. A trigger's filter only tests the
// person and the event; a check can also ask what they did since.
export function Conditions({ list, onChange, emailSteps, filter, catalog, event, disabled }: {
  list: JourneyCondition[]; onChange: (c: JourneyCondition[]) => void; emailSteps: Step[]; filter?: boolean; catalog: FlowCatalog; event?: string | null; disabled?: boolean;
}) {
  const set = (i: number, c: JourneyCondition) => onChange(list.map((x, k) => (k === i ? c : x)));
  const tests: [Test, string][] = [["attribute", "Their field"], ["property", "The event's property"], ...(filter ? [] : [["event", "They did an event"], ["email", "An email of this journey"]] as [Test, string][])];
  const listID = filter ? "flow-filter" : "flow-check";
  return (
    <div className="space-y-2">
      <datalist id={`${listID}-fields`}>{catalog.personFields.map((f) => <option key={f.key} value={f.key}>{f.label}</option>)}</datalist>
      <datalist id={`${listID}-props`}>{propertiesOf(catalog, event).map((p) => <option key={p.key} value={p.key} />)}</datalist>
      <datalist id={`${listID}-events`}>{catalog.events.map((e) => <option key={e.name} value={e.name} />)}</datalist>
      {list.map((c, i) => {
        const t = testOf(c);
        const did = t === "event" || t === "email";
        return (
          <div key={i} className="flex flex-wrap items-center gap-2 rounded-md border p-2">
            <select aria-label="Test" className={field} value={t} disabled={disabled} onChange={(e) => {
              const v = e.target.value as Test;
              set(i, v === "event" ? { event: "", op: "exists" } : v === "email" ? { email: "opened", step: emailSteps[0]?.id ?? "", op: "exists" } : { [v]: "", op: "eq", value: "" });
            }}>
              {tests.map(([v, text]) => <option key={v} value={v}>{text}</option>)}
            </select>
            {(t === "attribute" || t === "property") && (
              <input aria-label="Field" className={`${field} min-w-0 flex-1`} placeholder={t === "attribute" ? "plan" : "amount"} value={(t === "attribute" ? c.attribute : c.property) ?? ""}
                list={`${listID}-${t === "attribute" ? "fields" : "props"}`} disabled={disabled} onChange={(e) => set(i, { ...c, [t]: e.target.value.trim() })} required />
            )}
            {t === "event" && <input aria-label="Event" className={`${field} min-w-0 flex-1`} placeholder="upgraded" value={c.event ?? ""} list={`${listID}-events`} disabled={disabled} onChange={(e) => set(i, { ...c, event: e.target.value.trim() })} required />}
            {t === "email" && (
              <>
                <select aria-label="Email step" className={field} value={c.step ?? ""} disabled={disabled} onChange={(e) => set(i, { ...c, step: e.target.value })} required>
                  {emailSteps.map((s) => <option key={s.id} value={s.id}>{stepLabel(s)}</option>)}
                </select>
                <select aria-label="Was" className={field} value={c.email} disabled={disabled} onChange={(e) => set(i, { ...c, email: e.target.value as JourneyCondition["email"] })}>
                  <option value="delivered">was delivered</option>
                  <option value="opened">was opened</option>
                  <option value="clicked">was clicked</option>
                </select>
              </>
            )}
            <select aria-label="Op" className={field} value={c.op} disabled={disabled} onChange={(e) => set(i, { ...c, op: e.target.value as JourneyCondition["op"] })}>
              {did ? (
                <>
                  <option value="exists">{t === "event" ? "did" : "yes"}</option>
                  <option value="not_exists">{t === "event" ? "didn't" : "no"}</option>
                </>
              ) : (Object.keys(opText) as JourneyCondition["op"][]).map((o) => <option key={o} value={o}>{opText[o]}</option>)}
            </select>
            {!did && c.op !== "exists" && c.op !== "not_exists" && (
              <input aria-label="Value" className={`${field} w-28`} placeholder="pro" value={c.value === undefined ? "" : String(c.value)} disabled={disabled} onChange={(e) => set(i, { ...c, value: parseValue(e.target.value) })} required />
            )}
            {!disabled && <Button type="button" variant="ghost" onClick={() => onChange(list.filter((_, k) => k !== i))}>Remove</Button>}
          </div>
        );
      })}
      {!disabled && <Button type="button" variant="ghost" onClick={() => onChange([...list, filter ? { attribute: "", op: "eq", value: "" } : { event: "", op: "exists" }])}>Add a condition</Button>}
    </div>
  );
}

// ---- One step ----

type Unit = 60 | 3600 | 86400;
const units: [Unit, string][] = [[60, "minutes"], [3600, "hours"], [86400, "days"]];

function Row({ label, children, hint }: { label: string; children: ReactNode; hint?: ReactNode }) {
  return (
    <label className="block space-y-1 text-[13px]">
      <span className="font-medium">{label}</span>
      {children}
      {hint && <span className="block text-[12px] text-muted-foreground">{hint}</span>}
    </label>
  );
}

function ProblemList({ list, go }: { list: Problem[]; go: Go }) {
  if (!list.length) return null;
  return (
    <ul className="space-y-1 text-[12px]">
      {list.map((p, i) => (
        <li key={i} className={p.level === "error" ? "text-red-700 dark:text-red-400" : "text-amber-700 dark:text-amber-400"}>
          {p.text}{p.template && <> <button type="button" className="underline" onClick={() => go("templates", p.template)}>Open the template</button></>}
        </li>
      ))}
    </ul>
  );
}

// SchemaFields draws a simple form for a kind the builder has no form of
// its own for, from its settings schema.
function SchemaFields({ schema, step, onChange, disabled }: { schema: SettingsSchema; step: Step; onChange: (s: Step) => void; disabled: boolean }) {
  return (
    <>
      {Object.entries(schema.properties ?? {}).map(([key, p]) => {
        const title = p.title ?? key;
        const v = step[key];
        const type = Array.isArray(p.type) ? p.type.find((t) => t !== "null") : p.type;
        if (p.enum) {
          return (
            <Row key={key} label={title} hint={p.description}>
              <select className={`${field} w-full`} value={v === undefined ? "" : String(v)} disabled={disabled} onChange={(e) => onChange({ ...step, [key]: e.target.value || undefined })}>
                <option value="">Choose…</option>
                {p.enum.map((o) => <option key={String(o)} value={String(o)}>{String(o)}</option>)}
              </select>
            </Row>
          );
        }
        if (type === "boolean") {
          return (
            <label key={key} className="flex items-center gap-2 text-[13px]">
              <input type="checkbox" checked={v === true} disabled={disabled} onChange={(e) => onChange({ ...step, [key]: e.target.checked })} />
              <span>{title}</span>
            </label>
          );
        }
        if (type === "string" || type === "number" || type === "integer") {
          return (
            <Input key={key} label={title} hint={p.description} type={type === "string" ? "text" : "number"} min={p.minimum} max={p.maximum} disabled={disabled}
              value={v === undefined ? "" : String(v)} onChange={(e) => onChange({ ...step, [key]: type === "string" ? e.target.value : e.target.value === "" ? undefined : Number(e.target.value) })} />
          );
        }
        return <p key={key} className="text-[12px] text-muted-foreground">{title}: set through the API for now.</p>;
      })}
    </>
  );
}

export function StepPanel({ step: s, steps, kinds, templates, catalog, event, problems, editable, go, onChange, onRemove, onClose, extra }: {
  step: Step; steps: Step[]; kinds: StepKind[]; templates: EmailTemplate[]; catalog: FlowCatalog; event?: string | null; problems: Problem[];
  editable: boolean; go: Go; onChange: (s: Step) => void; onRemove: () => void; onClose: () => void;
  // What the step has done and who is on it, from the journey page.
  extra?: ReactNode;
}) {
  const k = kindOf(kinds, s.kind);
  const disabled = !editable;
  const unit: Unit = s.wait_seconds && s.wait_seconds % 86400 === 0 ? 86400 : s.wait_seconds && s.wait_seconds % 3600 === 0 ? 3600 : 60;
  const tpl = templates.find((t) => t.handle === s.template || t.id === s.template);
  // A link can go to any step that doesn't lead back here.
  const targets = steps.filter((x) => x.id !== s.id && !leadsTo(kinds, steps, x.id, s.id));
  return (
    <Sheet open modal={false} onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto shadow-xl sm:max-w-md" onInteractOutside={(e) => e.preventDefault()}>
        <SheetHeader>
          <SheetTitle>{stepLabel(s, kinds, templates)}</SheetTitle>
          <SheetDescription>{k?.description ?? s.kind}</SheetDescription>
        </SheetHeader>
        <div className="space-y-4 px-4 pb-6">
          <ProblemList list={problems} go={go} />
          {extra}
          <Input label="Label (optional)" value={s.name ?? ""} maxLength={100} disabled={disabled} placeholder={stepLabel({ ...s, name: undefined }, kinds, templates)}
            onChange={(e) => onChange({ ...s, name: e.target.value || undefined })} />
          {s.kind === "email" && (
            <>
              <Row label="Template" hint={templates.length === 0 && <>Write a lifecycle template first, under <button type="button" className="underline" onClick={() => go("templates")}>Templates</button>.</>}>
                <select aria-label="Template" className={`${field} w-full`} value={tpl?.handle ?? s.template ?? ""} disabled={disabled} onChange={(e) => onChange({ ...s, template: e.target.value })}>
                  <option value="">Choose a template</option>
                  {templates.map((t) => <option key={t.id} value={t.handle}>{t.name}{t.published_version ? "" : " (not published yet)"}</option>)}
                </select>
              </Row>
              {tpl && <button type="button" className="text-[12px] underline" onClick={() => go("templates", tpl.id)}>Open {tpl.name}</button>}
              <div className="rounded-md border p-3">
                <p className="mb-2 text-[12px] text-muted-foreground">The template can fill these in. Anyone who left its topic or all marketing email is skipped and goes on to the next step.</p>
                <TemplateFields catalog={catalog} event={event} />
              </div>
            </>
          )}
          {s.kind === "wait" && (
            <div className="flex items-end gap-2">
              <Input label="Wait" type="number" min={1} value={String((s.wait_seconds ?? 60) / unit)} disabled={disabled}
                onChange={(e) => onChange({ ...s, wait_seconds: Math.max(1, Number(e.target.value)) * unit })} required />
              <select aria-label="Unit" className={field} value={unit} disabled={disabled} onChange={(e) => onChange({ ...s, wait_seconds: ((s.wait_seconds ?? 60) / unit) * Number(e.target.value) })}>
                {units.map(([u, t]) => <option key={u} value={u}>{t}</option>)}
              </select>
            </div>
          )}
          {s.kind === "branch" && (
            <div className="space-y-2">
              <label className="flex items-center gap-2 text-[13px]">
                <span className="font-medium">Yes if</span>
                <select aria-label="Match" className={field} value={s.match ?? "all"} disabled={disabled} onChange={(e) => onChange({ ...s, match: e.target.value as "all" | "any" })}>
                  <option value="all">all of these hold</option>
                  <option value="any">any of these holds</option>
                </select>
              </label>
              <Conditions list={s.conditions ?? []} onChange={(c) => onChange({ ...s, conditions: c })} emailSteps={steps.filter((x) => x.kind === "email")}
                catalog={catalog} event={event} disabled={disabled} />
            </div>
          )}
          {s.kind === "exit" && <p className="text-[13px] text-muted-foreground">Ends their run here.</p>}
          {k && !["email", "wait", "branch", "exit"].includes(s.kind) && <SchemaFields schema={k.settings_schema} step={s} onChange={onChange} disabled={disabled} />}
          {outputs(kinds, s).map((f) => (
            <Row key={f} label={outputLabel(f) ? `${outputLabel(f)}: go to` : "Then"}>
              <select aria-label={outputLabel(f) || "Then"} className={`${field} w-full`} value={(s[f] as string | undefined) ?? ""} disabled={disabled}
                onChange={(e) => {
                  track("ui.journey_step_linked", { kind: s.kind, output: f, to: e.target.value ? "step" : "end" });
                  onChange({ ...s, [f]: e.target.value || undefined });
                }}>
                <option value="">End (completed)</option>
                {targets.map((x) => <option key={x.id} value={x.id}>{steps.indexOf(x) + 1}. {stepLabel(x, kinds, templates)}</option>)}
              </select>
            </Row>
          ))}
          {k?.error_output && (
            <Row label="If it fails" hint="A failed step can end the run, let it go on, or send it down a path of its own.">
              <select aria-label="If it fails" className={`${field} w-full`} value={(s[k.error_output] as string | undefined) || "stop"} disabled={disabled}
                onChange={(e) => onChange({ ...s, [k.error_output!]: e.target.value })}>
                <option value="stop">Stop the run (failed)</option>
                <option value="continue">Go on to the next step</option>
                {targets.map((x) => <option key={x.id} value={x.id}>Go to {steps.indexOf(x) + 1}. {stepLabel(x, kinds, templates)}</option>)}
              </select>
            </Row>
          )}
          {editable && (
            <div className="flex justify-between gap-2 border-t pt-4">
              <Button type="button" variant="ghost" onClick={onRemove}>Remove step</Button>
              <Button type="button" onClick={onClose}>Done</Button>
            </div>
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}

// ---- The trigger ----

export type TriggerFields = {
  kind: "event" | "manual";
  event: string;
  filter: JourneyCondition[];
  entry: "once" | "again";
  goal: string;
};

export function TriggerPanel({ value, onChange, editable, catalog, rule, onClose }: {
  value: TriggerFields; onChange: (v: TriggerFields) => void; editable: boolean; catalog: FlowCatalog; rule?: ReactNode; onClose: () => void;
}) {
  const disabled = !editable;
  const events = catalog.events.map((e) => e.name);
  return (
    <Sheet open modal={false} onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto shadow-xl sm:max-w-md" onInteractOutside={(e) => e.preventDefault()}>
        <SheetHeader>
          <SheetTitle>Trigger</SheetTitle>
          <SheetDescription>What starts a run, who can enter, and when they leave early.</SheetDescription>
        </SheetHeader>
        <div className="space-y-4 px-4 pb-6">
          <Row label="Starts">
            <select aria-label="Starts" className={`${field} w-full`} value={value.kind} disabled={disabled} onChange={(e) => onChange({ ...value, kind: e.target.value as TriggerFields["kind"] })}>
              <option value="event">When someone does an event</option>
              <option value="manual">When someone is added, by hand or through the API</option>
            </select>
          </Row>
          {value.kind === "event" && (
            <>
              <fieldset disabled={disabled}>
                <EventInput id="flow-trigger-events" label="Event" value={value.event} onChange={(v) => onChange({ ...value, event: v })} events={events}
                  hint="An event approved in the event catalog, or one a rule emits." required />
              </fieldset>
              {rule}
              <div className="space-y-2">
                <span className="block text-[13px] font-medium">Only if</span>
                <Conditions list={value.filter} onChange={(f) => onChange({ ...value, filter: f })} emailSteps={[]} filter catalog={catalog} event={value.event} disabled={disabled} />
              </div>
            </>
          )}
          <Row label="Entry">
            <select aria-label="Entry" className={`${field} w-full`} value={value.entry} disabled={disabled} onChange={(e) => onChange({ ...value, entry: e.target.value as TriggerFields["entry"] })}>
              <option value="once">Once per person, ever</option>
              <option value="again">Again after they finish</option>
            </select>
          </Row>
          <fieldset disabled={disabled}>
            <EventInput id="flow-goal-events" label="Goal (optional)" value={value.goal} onChange={(v) => onChange({ ...value, goal: v })} events={events}
              hint="Doing this event ends their run: they reached the goal." placeholder="upgraded" />
          </fieldset>
          <div className="flex justify-end border-t pt-4"><Button type="button" onClick={onClose}>Done</Button></div>
        </div>
      </SheetContent>
    </Sheet>
  );
}
