// The journey flow builder's model: the catalog of step kinds, and the
// edits and checks the builder makes on a journey's steps. Nothing here
// draws; flow.tsx lays the steps out top-down and step-panel.tsx edits one.
//
// Steps are a list whose first step is where a run starts. Each step names
// the steps after it in its link fields (next, or then and else for a
// branch); an empty link ends the run. The step kinds come from a catalog
// in the shape of GET .../journeys/step-kinds, so new kinds drop in with
// their own group, description and settings, and the builder draws their
// links from `outputs`.

import type { EmailTemplate, JourneyCondition, JourneyStep } from "@outflare/sdk";
import { plural } from "../../format";

export type StepGroup = "messaging" | "data" | "cross_campaign" | "notify" | "integrations" | "ai" | "flow";

// A JSON schema for a kind's settings, as far as the builder reads it to
// draw a form for a kind it has no form of its own for.
export type SettingsSchema = {
  type?: string | string[];
  title?: string;
  description?: string;
  enum?: unknown[];
  default?: unknown;
  minimum?: number;
  maximum?: number;
  maxLength?: number;
  items?: SettingsSchema;
  properties?: Record<string, SettingsSchema>;
  additionalProperties?: SettingsSchema | boolean;
  required?: string[];
  // What the field picks from, when the builder has a picker for it
  // (ad_account, ad_audience); others are typed.
  "x-outflare-picker"?: string;
};

export type StepKind = {
  kind: string;
  group: StepGroup;
  label: string;
  description: string;
  // Connections the workspace must have for the step to work; connected
  // says whether it has.
  requires: { connection: string; label: string; connected?: boolean }[];
  settings_schema: SettingsSchema;
  // The step's fields that name the step after it, in the order they are
  // drawn left to right.
  outputs: string[];
  // The field naming where a run goes when the step fails: stop, continue
  // or a step's id, drawn as a dashed error link.
  error_output?: string;
};

export const groupText: Record<StepGroup, string> = {
  messaging: "Messaging", data: "Data", cross_campaign: "Cross-campaign", notify: "Notify and hand off",
  integrations: "Integrations", ai: "AI", flow: "Flow control",
};
export const groupOrder: StepGroup[] = ["messaging", "data", "cross_campaign", "notify", "integrations", "ai", "flow"];

// Today's four kinds, in the catalog's shape until the API serves it.
export const builtinKinds: StepKind[] = [
  {
    kind: "email", group: "messaging", label: "Send an email", description: "Send one of your published lifecycle templates. People who left its topic or all marketing email are skipped.",
    requires: [], outputs: ["next"],
    settings_schema: { type: "object", required: ["template"], properties: { template: { type: "string", title: "Template" } } },
  },
  {
    kind: "wait", group: "flow", label: "Wait", description: "Wait a fixed time, from a minute to 90 days, before the next step.",
    requires: [], outputs: ["next"],
    settings_schema: { type: "object", required: ["wait_seconds"], properties: { wait_seconds: { type: "integer", title: "Seconds", minimum: 60, maximum: 7776000 } } },
  },
  {
    kind: "branch", group: "flow", label: "Check", description: "Go one way or the other on their fields, the event's properties, what they did since they started, or an email's opens and clicks.",
    requires: [], outputs: ["then", "else"],
    settings_schema: { type: "object", required: ["conditions"], properties: { conditions: { type: "array", title: "Conditions" }, match: { type: "string", enum: ["all", "any"] } } },
  },
  {
    kind: "exit", group: "flow", label: "Exit", description: "End their run here.",
    requires: [], outputs: [],
    settings_schema: { type: "object", properties: {} },
  },
];

const outputText: Record<string, string> = { then: "Yes", else: "No", timeout_next: "Timed out" };
// outputLabel names a link: a path by its name or id (paths.0.next).
export const outputLabel = (field: string, s?: Step) => {
  const m = /^paths\.(\d+)\.next$/.exec(field);
  if (m) {
    const p = (s?.paths as { id?: string; name?: string }[] | undefined)?.[Number(m[1])];
    return p?.name || p?.id || `Path ${Number(m[1]) + 1}`;
  }
  return outputText[field] ?? (field === "next" ? "" : field);
};

// Step is a journey step with whatever fields a newer kind adds.
export type Step = JourneyStep & Record<string, unknown>;

export function kindOf(kinds: StepKind[], kind: string): StepKind | undefined {
  return kinds.find((k) => k.kind === kind);
}

// outputs is the link fields of a step; a kind the catalog doesn't know
// links through next.
// A list of paths (paths[].next) is one link per path: paths.0.next,
// paths.1.next and so on.
export function outputs(kinds: StepKind[], s: Step): string[] {
  const out: string[] = [];
  for (const f of kindOf(kinds, s.kind)?.outputs ?? ["next"]) {
    const m = /^(\w+)\[\]\.(\w+)$/.exec(f);
    if (!m) {
      out.push(f);
      continue;
    }
    const list = s[m[1]];
    if (Array.isArray(list)) list.forEach((_, i) => out.push(`${m[1]}.${i}.${m[2]}`));
  }
  return out;
}

export function linkOf(s: Step, field: string): string | undefined {
  let v: unknown = s;
  for (const part of field.split(".")) v = v && typeof v === "object" ? (v as Record<string, unknown>)[part] : undefined;
  return typeof v === "string" && v ? v : undefined;
}

// setLink is s with one of its links (next, else, paths.0.next) set.
export function setLink(s: Step, field: string, to: string | undefined): Step {
  const parts = field.split(".");
  if (parts.length === 1) return { ...s, [field]: to };
  const set = (o: unknown, i: number): unknown => {
    const copy: Record<string, unknown> | unknown[] = Array.isArray(o) ? [...o] : { ...(o as Record<string, unknown>) };
    (copy as Record<string, unknown>)[parts[i]] = i === parts.length - 1 ? to : set((o as Record<string, unknown>)[parts[i]], i + 1);
    return copy;
  };
  return set(s, 0) as Step;
}

// errorLink is the step a failing step goes to, if its error mode names
// one rather than stop or continue.
export function errorLink(kinds: StepKind[], s: Step): string | undefined {
  const f = kindOf(kinds, s.kind)?.error_output;
  const to = f ? linkOf(s, f) : undefined;
  return to && to !== "stop" && to !== "continue" ? to : undefined;
}

function links(kinds: StepKind[], s: Step): string[] {
  return [...outputs(kinds, s).map((f) => linkOf(s, f)), errorLink(kinds, s)].filter((v): v is string => !!v);
}

export function waitText(s: number) {
  if (s % 86400 === 0) return plural(s / 86400, "day");
  if (s % 3600 === 0) return plural(s / 3600, "hour");
  return plural(Math.round(s / 60), "minute");
}

export function stepLabel(s: Step | undefined, kinds: StepKind[] = builtinKinds, templates?: EmailTemplate[]): string {
  if (!s) return "";
  if (s.name) return s.name;
  switch (s.kind) {
    case "email": {
      const t = templates?.find((x) => x.handle === s.template || x.id === s.template);
      return `Send ${t?.name ?? s.template ?? "an email"}`;
    }
    case "wait": return `Wait ${waitText(s.wait_seconds ?? 0)}`;
    case "branch": return "Check";
    case "exit": return "Exit";
  }
  return kindOf(kinds, s.kind)?.label ?? s.kind;
}

const opText: Record<JourneyCondition["op"], string> = { eq: "is", neq: "is not", gt: "is more than", lt: "is less than", contains: "contains", exists: "is set", not_exists: "is not set" };
export { opText };

export function conditionText(c: JourneyCondition, steps: Step[]) {
  const did = c.op === "exists";
  if (c.event) return `${did ? "did" : "didn't do"} ${c.event}`;
  if (c.email) return `${did ? "" : "not "}${c.email} ${stepLabel(steps.find((s) => s.id === c.step)) || c.step}`;
  const what = c.attribute ?? `event ${c.property}`;
  return c.op === "exists" || c.op === "not_exists" ? `${what} ${opText[c.op]}` : `${what} ${opText[c.op]} ${JSON.stringify(c.value)}`;
}

// ---- Reading the graph ----

// reachable is the ids a run can get to from the first step.
export function reachable(kinds: StepKind[], steps: Step[]): Set<string> {
  const byID = new Map(steps.map((s) => [s.id, s]));
  const seen = new Set<string>();
  const todo = steps.length ? [steps[0].id] : [];
  while (todo.length) {
    const id = todo.pop()!;
    if (seen.has(id) || !byID.has(id)) continue;
    seen.add(id);
    todo.push(...links(kinds, byID.get(id)!));
  }
  return seen;
}

// leadsTo reports whether a run at from can get to to.
export function leadsTo(kinds: StepKind[], steps: Step[], from: string, to: string): boolean {
  const byID = new Map(steps.map((s) => [s.id, s]));
  const seen = new Set<string>();
  const todo = [from];
  while (todo.length) {
    const id = todo.pop()!;
    if (id === to) return true;
    if (seen.has(id) || !byID.has(id)) continue;
    seen.add(id);
    todo.push(...links(kinds, byID.get(id)!));
  }
  return false;
}

// ---- Editing ----

export function newID(kind: string, steps: Step[]) {
  const base = kind.replace(/[^a-z0-9_-]/g, "").slice(0, 30) || "step";
  for (let n = 1; ; n++) {
    const id = `${base}-${n}`;
    if (!steps.some((s) => s.id === id)) return id;
  }
}

// newStep is a step of a kind with its usual settings filled in.
export function newStep(kinds: StepKind[], kind: string, steps: Step[], templates: EmailTemplate[], goal?: string | null): Step {
  const s: Step = { id: newID(kind, steps), kind: kind as JourneyStep["kind"] };
  if (kind === "email") s.template = templates.find((t) => t.published_version)?.handle ?? templates[0]?.handle ?? "";
  if (kind === "wait") s.wait_seconds = 86400;
  if (kind === "branch") s.conditions = [{ event: goal || "upgraded", op: "not_exists" }];
  const k = kindOf(kinds, kind);
  for (const [field, p] of Object.entries(k?.settings_schema.properties ?? {})) {
    if (s[field] === undefined && p.default !== undefined) s[field] = p.default;
  }
  return s;
}

// A place to put a step: after a step on one of its links, or at the
// start (from the trigger).
export type Slot = { from: string | null; field: string };

// insert puts s into a slot: what the slot went to now comes after s, on
// s's first link.
export function insert(kinds: StepKind[], steps: Step[], slot: Slot, s: Step): Step[] {
  const parent = slot.from === null ? undefined : steps.find((x) => x.id === slot.from);
  const after = slot.from === null ? steps[0]?.id : parent && linkOf(parent, slot.field);
  const first = outputs(kinds, s)[0];
  const added: Step = first && after ? setLink(s, first, after) : s;
  if (slot.from === null) return [added, ...steps];
  const i = steps.findIndex((x) => x.id === slot.from);
  const out = steps.map((x) => (x.id === slot.from ? setLink(x, slot.field, added.id) : x));
  return [...out.slice(0, i + 1), added, ...out.slice(i + 1)];
}

// remove takes a step out. Whatever went to it goes on to where its first
// link went. Steps that only it led to (a check's No path) go too, and are
// returned so the builder can say so first.
export function remove(kinds: StepKind[], steps: Step[], id: string): { steps: Step[]; dropped: Step[] } {
  const gone = steps.find((s) => s.id === id);
  if (!gone) return { steps, dropped: [] };
  const first = outputs(kinds, gone)[0];
  const after = first ? linkOf(gone, first) : undefined;
  const before = reachable(kinds, steps);
  let out = steps.filter((s) => s.id !== id).map((s) => {
    let c: Step = { ...s };
    for (const f of outputs(kinds, s)) if (linkOf(c, f) === id) c = setLink(c, f, after);
    const ef = kindOf(kinds, s.kind)?.error_output;
    if (ef && c[ef] === id) c[ef] = "stop";
    if (c.conditions) c.conditions = c.conditions.filter((x) => x.step !== id);
    return c;
  });
  // Taking out the first step: what came after it starts.
  if (steps[0]?.id === id && after) {
    const k = out.findIndex((s) => s.id === after);
    if (k > 0) out = [out[k], ...out.slice(0, k), ...out.slice(k + 1)];
  }
  const now = reachable(kinds, out);
  const dropped = out.filter((s) => before.has(s.id) && !now.has(s.id));
  return { steps: out.filter((s) => !dropped.includes(s)).map((s) => ({ ...s, conditions: s.conditions?.filter((c) => !dropped.some((d) => d.id === c.step)) })), dropped };
}

// replace swaps one step for its edited self.
export function replace(steps: Step[], s: Step): Step[] {
  return steps.map((x) => (x.id === s.id ? s : x));
}

// clean drops empty fields before saving.
export function clean(steps: Step[]): JourneyStep[] {
  return steps.map((s) => Object.fromEntries(Object.entries(s).filter(([, v]) => v !== undefined && v !== "")) as JourneyStep);
}

// ---- Problems ----

export type Problem = { level: "error" | "warning"; text: string; template?: string };

// problems is what is wrong with each step, by id, and with the journey as
// a whole (under ""). Errors stop it going live; warnings don't.
export function problems(kinds: StepKind[], steps: Step[], templates: EmailTemplate[] | null): Map<string, Problem[]> {
  const out = new Map<string, Problem[]>();
  const add = (id: string, p: Problem) => out.set(id, [...(out.get(id) ?? []), p]);
  if (steps.length === 0) add("", { level: "error", text: "Add a first step." });
  const ids = new Set(steps.map((s) => s.id));
  const live = reachable(kinds, steps);
  for (const s of steps) {
    if (!live.has(s.id)) add(s.id, { level: "error", text: "Nothing leads to this step. Link it from another step, or remove it." });
    for (const to of links(kinds, s)) {
      if (!ids.has(to)) add(s.id, { level: "error", text: "It goes to a step that no longer exists." });
    }
    switch (s.kind) {
      case "email": {
        if (!s.template) {
          add(s.id, { level: "error", text: "Choose a template." });
          break;
        }
        if (!templates) break;
        const t = templates.find((x) => x.handle === s.template || x.id === s.template);
        if (!t) add(s.id, { level: "error", text: `The template ${s.template} isn't an active lifecycle template.` });
        else if (!t.published_version) add(s.id, { level: "error", text: `Publish ${t.name} before this goes live.`, template: t.id });
        break;
      }
      case "wait":
        if (!s.next) add(s.id, { level: "warning", text: "Nothing comes after this wait, so it only delays the end." });
        break;
      case "branch":
        if (!s.conditions?.length) add(s.id, { level: "error", text: "Add a condition to check." });
        if (s.conditions?.some((c) => c.step && !ids.has(c.step))) add(s.id, { level: "error", text: "A condition is about an email step that was removed." });
        if (!s.then && !s.else) add(s.id, { level: "warning", text: "Both ways end the run here." });
        break;
    }
    const k = kindOf(kinds, s.kind);
    for (const r of k?.requires ?? []) if (r.connected === false) add(s.id, { level: "error", text: `Connect ${r.label} first.` });
    const schema = k && !["email", "wait", "branch", "exit"].includes(s.kind) ? k.settings_schema : undefined;
    for (const field of schema?.required ?? []) {
      if (s[field] === undefined || s[field] === "") add(s.id, { level: "error", text: `Fill in ${schema?.properties?.[field]?.title ?? field}.` });
    }
  }
  return out;
}

export function errorCount(p: Map<string, Problem[]>) {
  let n = 0;
  for (const list of p.values()) n += list.filter((x) => x.level === "error").length;
  return n;
}

// ---- Versions ----

export type Change = { kind: "added" | "removed" | "changed"; label: string };

// changes is how one version's steps differ from another's, by step id.
export function changes(from: Step[], to: Step[], kinds: StepKind[], templates?: EmailTemplate[]): Change[] {
  const was = new Map(from.map((s) => [s.id, s]));
  const now = new Map(to.map((s) => [s.id, s]));
  const out: Change[] = [];
  for (const s of to) {
    const old = was.get(s.id);
    if (!old) out.push({ kind: "added", label: stepLabel(s, kinds, templates) });
    else if (JSON.stringify(clean([old])) !== JSON.stringify(clean([s]))) out.push({ kind: "changed", label: stepLabel(s, kinds, templates) });
  }
  for (const s of from) if (!now.has(s.id)) out.push({ kind: "removed", label: stepLabel(s, kinds, templates) });
  return out;
}
