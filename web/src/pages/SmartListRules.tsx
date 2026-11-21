import { Fragment, useEffect, useMemo, useState, type ReactNode } from "react";
import type { LeadList, NewsSearch, Schemas, SmartList, SmartListRule } from "@outflare/sdk";
import { Check, ChevronsUpDown, X } from "lucide-react";
import { Command, CommandEmpty, CommandInput, CommandItem, CommandList } from "@/components/ui/command";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { api } from "../api";
import { field } from "../ui";
import { plural } from "../format";

// The smart list rule builder. A list is said in two parts, who to include
// and who to leave out. "Add a condition" asks what kind it is (something
// they did, a page they visited, a detail, a list, their company, a
// lifecycle stage, whether they can be emailed), and its row then offers
// the brand's own events, properties, sites, pages, fields and lists in
// searchable pickers. Sets mix all, either and none.

export type Field = { key: string; label: string; type: string; options: { key: string; label: string }[] };
type ChoiceEvent = Schemas["SmartListChoices"]["events"][number];
type ChoicePage = Schemas["SmartListChoicePage"];
type Stage = Schemas["ProductStage"];

export type RuleChoices = {
  fields: Field[];
  companyFields: Field[];
  events: ChoiceEvent[];
  hosts: ChoicePage[];
  pages: ChoicePage[];
  lists: { id: string; name: string; record_type: string }[];
  leadLists: LeadList[];
  newsSearches: NewsSearch[];
  products: { key: string; name: string }[];
  stages: Stage[];
};

// useRuleChoices loads what rules can pick from: the brand's catalog
// fields (people and companies) with their options, the events, sites and
// pages it receives, its lists, lead lists, news searches (for media lists;
// empty without seo.read), products and lifecycle stages.
export function useRuleChoices(_ws: string, brand: string): RuleChoices {
  const [c, setC] = useState<RuleChoices>({ fields: [], companyFields: [], events: [], hosts: [], pages: [], lists: [], leadLists: [], newsSearches: [], products: [], stages: [] });
  useEffect(() => {
    if (!brand) return;
    const path = { params: { path: { brand } } };
    const put = (p: Partial<RuleChoices>) => setC((x) => ({ ...x, ...p }));
    api.GET("/v1/brands/{brand}/catalog", path).then(({ data }) => {
      const options = new Map((data?.option_sets ?? []).map((o) => [o.key, o.options.filter((x) => !x.archived).map((x) => ({ key: x.key, label: x.label }))]));
      const of = (type: string): Field[] =>
        (data?.types.find((t) => t.key === type)?.fields ?? [])
          .filter((f) => !f.archived)
          .map((f) => ({ key: f.key, label: f.label, type: f.value_type, options: f.option_set ? (options.get(f.option_set) ?? []) : [] }));
      put({ fields: of("person"), companyFields: of("account") });
    });
    api.GET("/v1/brands/{brand}/smart-lists/choices", path).then(({ data }) => data && put({ events: data.events, hosts: data.hosts, pages: data.pages }));
    api.GET("/v1/brands/{brand}/smart-lists", path).then(({ data }) => put({ lists: (data?.data ?? []).map((l) => ({ id: l.id, name: l.name, record_type: l.record_type })) }));
    api.GET("/v1/brands/{brand}/lead-lists", path).then(({ data }) => put({ leadLists: data?.data ?? [] }));
    api.GET("/v1/brands/{brand}/news-searches", path).then(({ data }) => put({ newsSearches: data?.data ?? [] }));
    api.GET("/v1/brands/{brand}/products", path).then(({ data }) => put({ products: (data?.data ?? []).filter((p) => !p.archived_at).map((p) => ({ key: p.key, name: p.name })) }));
    api.GET("/v1/brands/{brand}/lifecycle", path).then(({ data }) => put({ stages: data?.stages ?? [] }));
  }, [brand]);
  return c;
}

export type Op = NonNullable<SmartListRule["op"]>;
export type Kind = "page" | "event" | "attribute" | "in_smart_list" | "company_attribute" | "product_stage" | "marketing_email" | "in_list" | "news_search";

export const kindText: Record<Kind, string> = {
  page: "Visited a page",
  event: "Did something",
  attribute: "Has a detail",
  in_smart_list: "Is in a list",
  company_attribute: "Their company",
  product_stage: "Lifecycle stage",
  marketing_email: "Can get marketing email",
  in_list: "Is in a lead list",
  news_search: "Wrote about a news topic",
};

export const isGroup = (r: SmartListRule) => r.rules !== undefined || r.match !== undefined;
type GroupMatch = NonNullable<SmartListRule["match"]>;
// How deep groups go: the list's own rules are level 1 (the engine's
// smartlists.MaxDepth).
const maxDepth = 3;
export const newRule = (): SmartListRule => ({ event: "", op: "exists" });

export const kindOf = (r: SmartListRule): Kind =>
  r.page !== undefined ? "page"
  : r.event !== undefined ? "event"
  : r.in_smart_list !== undefined ? "in_smart_list"
  : r.company_attribute !== undefined ? "company_attribute"
  : r.product_stage !== undefined ? "product_stage"
  : r.marketing_email !== undefined ? "marketing_email"
  : r.in_list !== undefined ? "in_list"
  : r.news_search !== undefined ? "news_search"
  : "attribute";

// The comparisons that make sense for each kind of field.
const numberOps: Op[] = ["eq", "neq", "gt", "lt", "in", "not_in", "exists", "not_exists"];
const dateOps: Op[] = ["in_last_days", "more_than_days_ago", "gt", "lt", "exists", "not_exists"];
const choiceOps: Op[] = ["in", "not_in", "eq", "neq", "exists", "not_exists"];
const textOps: Op[] = ["eq", "neq", "contains", "starts_with", "in", "not_in", "exists", "not_exists"];
const boolOps: Op[] = ["eq", "exists", "not_exists"];
export function opsFor(type: string | undefined): Op[] {
  switch (type) {
    case "number": case "currency": case "rating": return numberOps;
    case "date": case "timestamp": return dateOps;
    case "select": case "status": return choiceOps;
    case "checkbox": case "boolean": return boolOps;
  }
  return textOps;
}
const isDate = (type?: string) => type === "date" || type === "timestamp";

export function opText(op: Op, type?: string): string {
  switch (op) {
    case "eq": return "is";
    case "neq": return "is not";
    case "gt": return isDate(type) ? "is after" : "is more than";
    case "lt": return isDate(type) ? "is before" : "is less than";
    case "contains": return "contains";
    case "starts_with": return "starts with";
    case "in": return "is one of";
    case "not_in": return "is none of";
    case "in_last_days": return "is in the last";
    case "more_than_days_ago": return "is more than";
    case "exists": return "is set";
    case "not_exists": return "is not set";
  }
}

const noValue = (op?: Op) => op === "exists" || op === "not_exists";
const days = (op?: Op) => op === "in_last_days" || op === "more_than_days_ago";
const list = (op?: Op) => op === "in" || op === "not_in";

function parseValue(v: string): unknown {
  if (v === "true" || v === "false") return v === "true";
  if (v.trim() !== "" && !Number.isNaN(Number(v))) return Number(v);
  return v;
}

function valueText(op: Op | undefined, v: unknown): string {
  if (noValue(op)) return "";
  if (days(op)) return ` ${plural(Number(v) || 0, "day")}${op === "more_than_days_ago" ? " ago" : ""}`;
  if (Array.isArray(v)) return ` ${v.map(String).join(", ")}`;
  return ` ${String(v ?? "")}`;
}

function whenText(r: SmartListRule): string {
  const times = r.min_count && r.min_count > 1 ? ` at least ${r.min_count} times` : "";
  const when = r.within_days ? ` in the last ${plural(r.within_days, "day")}` : r.before_days ? ` more than ${plural(r.before_days, "day")} ago` : "";
  return times + when;
}

export type Names = { field: (k: string) => string; list: (id: string) => string };

// ruleText says a rule in words: "plan is pro", "did payment_succeeded at
// least 2 times in the last 90 days", "never visited app.example.com".
export function ruleText(r: SmartListRule, fieldLabel: (k: string) => string, listName: (id: string) => string): string {
  if (isGroup(r)) {
    const parts = (r.rules ?? []).map((x) => ruleText(x, fieldLabel, listName));
    if (r.match === "none") return `none of (${parts.join(", ")})`;
    return `(${parts.join(r.match === "any" ? ", or " : ", and ")})`;
  }
  const yes = r.op === "exists";
  switch (kindOf(r)) {
    case "event": {
      const where = (r.where ?? []).map((w) => `${w.property} ${opText(w.op)}${minor(w.property) && single(w.op) && typeof w.value === "number" ? ` ${(w.value / 100).toFixed(2)}` : valueText(w.op, w.value)}`).join(" and ");
      const fewer = !yes && r.min_count && r.min_count > 1;
      return `${yes ? "did" : fewer ? "did" : "didn't do"} ${r.event || "…"}${where ? ` where ${where}` : ""}${fewer ? ` fewer than ${r.min_count} times` : whenText(r)}${fewer ? whenText({ ...r, min_count: 0 }) : ""}`;
    }
    case "page": {
      const p = r.page ?? {};
      const where = p.path ? `${p.match === "is" ? "the page" : p.match === "starts_with" ? "pages starting with" : "pages containing"} ${p.path}${p.host ? ` on ${p.host}` : ""}` : (p.host ?? "your site");
      return `${yes ? "visited" : "never visited"} ${where}${whenText(r)}`;
    }
    case "in_smart_list":
      return `${yes ? "is" : "isn't"} in ${listName(r.in_smart_list ?? "")}`;
    case "marketing_email":
      return yes ? "can get marketing email" : "can't get marketing email";
    case "product_stage":
      return `${r.op === "eq" ? "is" : "isn't"} ${r.product_stage} for ${r.product}`;
    case "in_list":
      return `${yes ? "is" : "isn't"} in ${listName(r.in_list ?? "")}`;
    case "news_search": {
      const when = r.within_days ? ` in the last ${plural(r.within_days, "day")}` : "";
      return `${yes ? "wrote" : "didn't write"} about ${listName(r.news_search ?? "")}${when}`;
    }
    case "company_attribute":
      return `their company's ${fieldLabel(r.company_attribute ?? "")} ${opText(r.op ?? "eq")}${valueText(r.op, r.value)}`;
  }
  return `${fieldLabel(r.attribute ?? "")} ${opText(r.op ?? "eq")}${valueText(r.op, r.value)}`;
}

export function rulesText(l: Pick<SmartList, "rules" | "match">, fieldLabel: (k: string) => string, listName: (id: string) => string) {
  if (l.rules.length === 0) return "Everyone";
  return l.rules.map((r) => ruleText(r, fieldLabel, listName)).join(l.match === "any" ? ", or " : ", and ");
}

// negative reports whether the rules pick nearly everyone, as the engine's
// smartlists.Negative does: what decides who is in only leaves people out
// (didn't, isn't, is not). That is every rule when all must pass, or any
// one of them when any may.
export function negative(rules: SmartListRule[], match: string = "all", flipped = false): boolean {
  if (rules.length === 0) return false;
  const inner = flipped !== (match === "none");
  const every = (match === "all") !== inner;
  const broad = (r: SmartListRule) =>
    isGroup(r) ? negative(r.rules ?? [], r.match ?? "all", inner) : (r.op === "not_exists" || r.op === "neq" || r.op === "not_in") !== inner;
  return every ? rules.every(broad) : rules.some(broad);
}

// counted reports whether a typed number of days or times is filled in.
const counted = (r: SmartListRule) => r.within_days !== 0 && r.before_days !== 0 && r.min_count !== 0;

// Money in an event's properties is in minor units (cents) when the
// property's name ends in _minor; the builder shows and takes whole units.
const minor = (property: string) => property.endsWith("_minor");
const single = (op?: Op) => op === "eq" || op === "neq" || op === "gt" || op === "lt";

// ready reports whether every rule is filled in enough to count.
export const ready = (rules: SmartListRule[]): boolean =>
  rules.every((r) => {
    if (isGroup(r)) return (r.rules ?? []).length > 0 && ready(r.rules ?? []);
    const filled = (op: Op | undefined, v: unknown) => noValue(op) || (Array.isArray(v) ? v.length > 0 : v !== undefined && v !== "");
    switch (kindOf(r)) {
      case "event":
        return !!r.event && counted(r) && (r.where ?? []).every((w) => !!w.property && filled(w.op, w.value));
      case "page":
        return counted(r) && (r.page?.path === undefined || r.page.path.trim() !== "");
      case "in_smart_list":
        return !!r.in_smart_list;
      case "marketing_email":
        return true;
      case "product_stage":
        return !!r.product_stage && !!r.product;
      case "in_list":
        return !!r.in_list;
      case "news_search":
        return !!r.news_search;
      case "company_attribute":
        return !!r.company_attribute && filled(r.op, r.value);
    }
    return !!r.attribute && filled(r.op, r.value);
  });

// Pick is a searchable dropdown: type to filter, pick one, or (with free)
// use what you typed.
export function Pick({ label, value, options, onChange, placeholder, free = false, className = "" }: {
  label: string; value: string; options: { value: string; label: string; hint?: string }[]; onChange: (v: string) => void; placeholder: string; free?: boolean; className?: string;
}) {
  const [open, setOpen] = useState(false);
  const [typed, setTyped] = useState("");
  const picked = options.find((o) => o.value === value);
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button type="button" role="combobox" aria-label={label} aria-expanded={open} className={`${field} flex min-w-[10rem] items-center justify-between gap-2 text-left ${className}`}>
          <span className={`truncate ${value ? "" : "text-muted-foreground"}`}>{picked?.label ?? (value || placeholder)}</span>
          <ChevronsUpDown className="size-3.5 shrink-0 text-muted-foreground" />
        </button>
      </PopoverTrigger>
      <PopoverContent className="w-[min(26rem,90vw)] p-0" align="start">
        <Command>
          <CommandInput placeholder="Search…" value={typed} onValueChange={setTyped} />
          <CommandList>
            <CommandEmpty>{free && typed.trim() ? null : "Nothing matches."}</CommandEmpty>
            {options.map((o) => (
              <CommandItem key={o.value} value={o.value} keywords={[o.label]} onSelect={() => { onChange(o.value); setOpen(false); setTyped(""); }}>
                <span className="min-w-0 flex-1 truncate">{o.label}</span>
                {o.hint && <span className="text-xs text-muted-foreground tabular-nums">{o.hint}</span>}
                {o.value === value && <Check className="size-3.5" />}
              </CommandItem>
            ))}
            {free && typed.trim() && !options.some((o) => o.value === typed.trim()) && (
              <CommandItem value={`__use ${typed}`} keywords={[typed]} onSelect={() => { onChange(typed.trim()); setOpen(false); setTyped(""); }}>
                Use “{typed.trim()}”
              </CommandItem>
            )}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}

const sel = (label: string, value: string | number, onChange: (v: string) => void, opts: [string | number, string][], className = field) => (
  <select aria-label={label} className={className} value={value} onChange={(e) => onChange(e.target.value)}>
    {opts.map(([v, t]) => <option key={String(v)} value={v}>{t}</option>)}
  </select>
);

// What a row starts with: the words that say what kind of condition it is.
// A select when there is a yes and a no ("Did", "Didn't do"), plain words
// otherwise.
const lead = "h-8 w-[5.75rem] shrink-0 text-[13px] text-muted-foreground";
const verb = (label: string, value: string, onChange: (v: string) => void, opts: [string, string][]) =>
  sel(label, value, onChange, opts, `${lead} cursor-pointer rounded-md bg-transparent outline-none hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring/40`);
const noun = (text: string) => <span className={`${lead} flex items-center`}>{text}</span>;

// An optional part of a condition, shown as a dashed "+ when" until asked for.
const more = (text: string, onClick: () => void) => (
  <button type="button" className="order-1 h-8 rounded-md border border-dashed px-2.5 text-[13px] text-muted-foreground hover:bg-muted hover:text-foreground" onClick={onClick}>+ {text}</button>
);

function Cross({ label, onClick, className = "" }: { label: string; onClick: () => void; className?: string }) {
  return (
    <button type="button" aria-label={label} title={label} onClick={onClick}
      className={`grid size-7 shrink-0 place-items-center rounded-md text-muted-foreground hover:bg-muted hover:text-foreground ${className}`}>
      <X className="size-3.5" />
    </button>
  );
}

// A small number box beside a menu, for "another number".
function Count({ label, value, max, onChange }: { label: string; value: number; max: number; onChange: (n: number) => void }) {
  return (
    <input aria-label={label} type="number" min={1} max={max} className={`${field} w-20`} value={value || ""} required
      onChange={(e) => onChange(Math.max(0, Math.min(max, Math.floor(Number(e.target.value) || 0))))} />
  );
}

const whenPresets = { w: [7, 30, 90, 365], b: [30, 90, 365] };

// When an event or page view counts: any time (not shown until asked for),
// recently, or long ago, with the common windows as choices and any other
// number of days typed in.
function When({ r, set, long = true }: { r: SmartListRule; set: (r: SmartListRule) => void; long?: boolean }) {
  const kind = r.within_days !== undefined ? "w" : r.before_days !== undefined ? "b" : "";
  const n = r.within_days ?? r.before_days ?? 0;
  const [other, setOther] = useState(kind !== "" && !whenPresets[kind].includes(n));
  const put = (k: string, days: number | undefined) => set({ ...r, within_days: k === "w" ? days : undefined, before_days: k === "b" ? days : undefined });
  if (kind === "") return more("when", () => { setOther(false); put("w", 30); });
  const value = other ? `${kind}x` : `${kind}${n}`;
  const opts: [string, string][] = [["", "at any time"], ["w7", "in the last 7 days"], ["w30", "in the last 30 days"], ["w90", "in the last 90 days"], ["w365", "in the last year"], ["wx", "in the last … days"]];
  if (long) opts.push(["b30", "more than 30 days ago"], ["b90", "more than 90 days ago"], ["b365", "more than a year ago"], ["bx", "more than … days ago"]);
  return (
    <>
      {sel("When", value, (v) => {
        const k = v.slice(0, 1);
        const custom = v.endsWith("x");
        setOther(custom);
        put(k, v === "" ? undefined : custom ? (n || 14) : Number(v.slice(1)));
      }, opts)}
      {other && (
        <span className="flex items-center gap-1"><Count label="Days" value={n} max={3650} onChange={(d) => put(kind, d)} /> days{kind === "b" ? " ago" : ""}</span>
      )}
    </>
  );
}

// How often: once (not shown until asked for), or a number typed in.
// Clearing the number, or leaving it at 1, puts it away again.
function Times({ r, set }: { r: SmartListRule; set: (r: SmartListRule) => void }) {
  const n = r.min_count;
  const not = r.op === "not_exists";
  if (n === undefined) return more("how often", () => set({ ...r, min_count: 2 }));
  return (
    <span className="flex items-center gap-1.5" onBlur={() => n <= 1 && set({ ...r, min_count: undefined })}>
      {not ? "fewer than" : "at least"}
      <Count label="Times" value={n} max={100000} onChange={(c) => set({ ...r, min_count: c })} />
      times
    </span>
  );
}

// Value is what a field is compared with, shaped by the comparison: a
// number of days, a list, a choice from the field's options, or text.
function Value({ op, value, field: f, onChange }: { op?: Op; value: unknown; field?: Field; onChange: (v: unknown) => void }) {
  if (noValue(op)) return null;
  if (days(op)) {
    return (
      <span className="flex items-center gap-1">
        <input aria-label="Days" type="number" min={1} max={36500} className={`${field} w-20`} value={value === undefined ? "" : String(value)} onChange={(e) => onChange(e.target.value === "" ? "" : Number(e.target.value))} required />
        {op === "more_than_days_ago" ? "days ago" : "days"}
      </span>
    );
  }
  if (f?.type === "checkbox") return sel("Value", String(value ?? true), (v) => onChange(v === "true"), [["true", "yes"], ["false", "no"]]);
  if (list(op)) {
    const vals = Array.isArray(value) ? value.map(String) : [];
    if (f?.options.length) {
      return (
        <span className="flex flex-wrap items-center gap-1">
          {vals.map((v) => (
            <button key={v} type="button" className="rounded-full border px-2 py-0.5 text-[12px] hover:bg-muted" onClick={() => onChange(vals.filter((x) => x !== v))} aria-label={`Remove ${v}`}>
              {f.options.find((o) => o.key === v)?.label ?? v} ×
            </button>
          ))}
          <Pick label="Add a value" value="" placeholder="Add a value" options={f.options.filter((o) => !vals.includes(o.key)).map((o) => ({ value: o.key, label: o.label }))} onChange={(v) => onChange([...vals, v])} />
        </span>
      );
    }
    return (
      <input aria-label="Values" className={field} placeholder="pro, team" value={vals.join(", ")} required
        onChange={(e) => onChange(e.target.value.split(",").map((x) => x.trim()).filter(Boolean).map(parseValue))} />
    );
  }
  if (f?.options.length && (op === "eq" || op === "neq")) {
    return <Pick label="Value" value={String(value ?? "")} placeholder="Pick a value" options={f.options.map((o) => ({ value: o.key, label: o.label }))} onChange={onChange} free />;
  }
  const date = isDate(f?.type);
  return (
    <input aria-label="Value" type={date ? "date" : "text"} className={field} placeholder={date ? "" : "pro"} value={value === undefined ? "" : String(value)} required
      onChange={(e) => onChange(date ? e.target.value : parseValue(e.target.value))} />
  );
}

function FieldTest({ r, set, fields, prop }: { r: SmartListRule; set: (r: SmartListRule) => void; fields: Field[]; prop: "attribute" | "company_attribute" }) {
  const key = r[prop] ?? "";
  const f = fields.find((x) => x.key === key);
  const ops = opsFor(f?.type);
  return (
    <>
      <Pick label="Field" value={key} placeholder="Pick a field" free
        options={fields.map((x) => ({ value: x.key, label: x.label }))}
        onChange={(k) => {
          const nf = fields.find((x) => x.key === k);
          const nops = opsFor(nf?.type);
          set({ ...r, [prop]: k, op: nops.includes(r.op as Op) ? r.op : nops[0], value: nf?.type === "checkbox" ? true : undefined });
        }} />
      {sel("Compare", r.op ?? ops[0], (v) => set({ ...r, op: v as Op, value: list(v as Op) ? [] : days(v as Op) ? 30 : f?.type === "checkbox" ? true : undefined }),
        ops.map((o) => [o, opText(o, f?.type)]))}
      <Value op={r.op} value={r.value} field={f} onChange={(value) => set({ ...r, value })} />
    </>
  );
}

// The empty rule of each kind.
function startRule(k: Kind, c: RuleChoices, self?: string): SmartListRule {
  switch (k) {
    case "page": return { page: {}, op: "exists" };
    case "event": return { event: "", op: "exists" };
    case "in_smart_list": return { in_smart_list: c.lists.find((l) => l.id !== self)?.id ?? "", op: "exists" };
    case "company_attribute": return { company_attribute: "", op: "eq", value: "" };
    case "product_stage": return { product_stage: c.stages.find((s) => s.level === "person")?.key ?? "", product: c.products[0]?.key ?? "", op: "eq" };
    case "marketing_email": return { marketing_email: true, op: "exists" };
    case "in_list": return { in_list: c.leadLists[0]?.id ?? "", op: "exists" };
    case "news_search": return { news_search: c.newsSearches[0]?.id ?? "", op: "exists" };
  }
  return { attribute: "", op: "eq", value: "" };
}

// One condition: the words it starts with, only the fields its kind needs,
// the optional ones behind "+ how often", "+ when" and "+ filter", and a
// cross to remove it. An event's filters sit on their own lines below.
function OneRule({ r, set, remove, c, self, recordType }: { r: SmartListRule; set: (r: SmartListRule) => void; remove: () => void; c: RuleChoices; self?: string; recordType: string }) {
  const k = kindOf(r);
  const people = recordType !== "account";
  const ev = c.events.find((e) => e.name === r.event);
  const yesNo = (label: string, yes: string, no: string) => verb(label, r.op === "not_exists" ? "not_exists" : "exists", (v) => set({ ...r, op: v as Op }), [["exists", yes], ["not_exists", no]]);
  let main: ReactNode = null;
  let below: ReactNode = null;
  switch (k) {
    case "page": {
      const p = r.page ?? {};
      const setPage = (np: typeof p) => set({ ...r, page: np });
      const paths = c.pages.filter((x) => !p.host || x.host === p.host || x.host === `www.${p.host}`);
      main = (
        <>
          {yesNo("Visited", "Visited", "Never visited")}
          {sel("Page", p.path === undefined ? "" : (p.match ?? "contains"), (v) => setPage(v ? { ...p, match: v as "is", path: p.path ?? "" } : { host: p.host }),
            [["", "any page"], ["is", "the page"], ["starts_with", "pages starting with"], ["contains", "pages containing"]])}
          {p.path !== undefined && (
            <Pick label="Path" value={p.path} placeholder="/pricing" free
              options={paths.map((x) => ({ value: x.path, label: x.path, hint: `${x.views.toLocaleString()} views` }))}
              onChange={(path) => setPage({ ...p, path })} />
          )}
          {p.host !== undefined ? (
            <span className="flex items-center gap-1.5 text-muted-foreground">on
              <Pick label="Site" value={p.host} placeholder="any site" free
                options={[{ value: "", label: "any site" }, ...c.hosts.map((h) => ({ value: h.host, label: h.host, hint: `${h.views.toLocaleString()} views` }))]}
                onChange={(host) => setPage({ ...p, host: host || undefined })} />
            </span>
          ) : c.hosts.length > 1 && more("site", () => setPage({ ...p, host: c.hosts[0].host }))}
          <Times r={r} set={set} />
          <When r={r} set={set} />
        </>
      );
      below = <p className="mt-1.5 text-[12px] text-muted-foreground">Counts visits from browsers they signed in on, or reached from a link in your email.</p>;
      break;
    }
    case "event":
      main = (
        <>
          {yesNo("Did", "Did", "Didn't do")}
          <Pick label="Event" value={r.event ?? ""} placeholder="Pick what they did" free
            options={c.events.map((e) => ({ value: e.name, label: e.label ? `${e.label} (${e.name})` : e.name, hint: e.count.toLocaleString() }))}
            onChange={(event) => set({ ...r, event, where: undefined })} />
          <Times r={r} set={set} />
          <When r={r} set={set} />
          {r.event && (r.where ?? []).length < 5 && more("filter", () => set({ ...r, where: [...(r.where ?? []), { property: ev?.properties[0]?.key ?? "", op: "eq", value: "" }] }))}
        </>
      );
      below = (r.where ?? []).map((w, j) => {
        const setW = (nw: typeof w | null) => {
          const ws = (r.where ?? []).flatMap((x, n) => (n === j ? (nw ? [nw] : []) : [x]));
          set({ ...r, where: ws.length ? ws : undefined });
        };
        const prop = ev?.properties.find((x) => x.key === w.property);
        const pf: Field | undefined = prop ? { key: prop.key, label: prop.key, type: prop.value_type, options: [] } : undefined;
        const ops = opsFor(pf?.type);
        return (
          <div key={j} className="mt-1.5 ml-[6.1rem] flex flex-wrap items-center gap-1.5 border-l-2 pl-3 text-muted-foreground">
            <span>{j === 0 ? "where" : "and"}</span>
            <Pick label="Property" value={w.property} placeholder="Pick a property" free
              options={(ev?.properties ?? []).map((x) => ({ value: x.key, label: x.key, hint: x.value_type }))}
              onChange={(property) => setW({ ...w, property })} />
            {sel("Compare", w.op, (v) => setW({ ...w, op: v as Op, value: list(v as Op) ? [] : undefined }), ops.map((o) => [o, opText(o, pf?.type)]))}
            {minor(w.property) && single(w.op) ? (
              <input aria-label="Amount" type="number" min={0} step="0.01" className={`${field} w-28`} placeholder="100.00" required
                title="A whole amount, such as 100 for $100. It is stored in cents."
                value={typeof w.value === "number" ? w.value / 100 : ""}
                onChange={(e) => setW({ ...w, value: e.target.value === "" ? undefined : Math.round(Number(e.target.value) * 100) })} />
            ) : (
              <Value op={w.op} value={w.value} field={pf} onChange={(value) => setW({ ...w, value })} />
            )}
            {minor(w.property) && !single(w.op) && !noValue(w.op) && <span className="text-[12px]">in cents: 10000 is $100</span>}
            <Cross label="Remove filter" onClick={() => setW(null)} />
          </div>
        );
      });
      break;
    case "attribute":
      main = <>{noun(people ? "Their" : "Its")}<FieldTest r={r} set={set} fields={people ? c.fields : c.companyFields} prop="attribute" /></>;
      break;
    case "company_attribute":
      main = <>{noun("Their company's")}<FieldTest r={r} set={set} fields={c.companyFields} prop="company_attribute" /></>;
      break;
    case "in_smart_list": {
      const lists = c.lists.filter((l) => l.id !== self && l.record_type === recordType);
      main = (
        <>
          {yesNo("Is", "Is in", "Isn't in")}
          <Pick label="List" value={r.in_smart_list ?? ""} placeholder={lists.length ? "Pick a list" : "No other lists yet"} options={lists.map((l) => ({ value: l.id, label: l.name }))} onChange={(id) => set({ ...r, in_smart_list: id })} />
        </>
      );
      break;
    }
    case "product_stage": {
      const level = r.level ?? (people ? "person" : "company");
      main = (
        <>
          {noun("Stage for")}
          {sel("Product", r.product ?? "", (v) => set({ ...r, product: v }), [["", c.products.length ? "Pick a product" : "No products yet"], ...c.products.map((p) => [p.key, p.name] as [string, string])])}
          {sel("Is", r.op ?? "eq", (v) => set({ ...r, op: v as Op }), [["eq", "is"], ["neq", "isn't"]])}
          {sel("Stage", r.product_stage ?? "", (v) => set({ ...r, product_stage: v }), [["", "Pick a stage"], ...c.stages.filter((s) => s.level === level).map((s) => [s.key, s.name] as [string, string])])}
        </>
      );
      break;
    }
    case "marketing_email":
      main = <>{yesNo("Can be emailed", "Can get", "Can't get")}<span>marketing email</span></>;
      break;
    case "in_list":
      main = (
        <>
          {yesNo("Is", "Is in", "Isn't in")}
          {sel("Lead list", r.in_list ?? "", (v) => set({ ...r, in_list: v }), c.leadLists.map((l) => [l.id, l.name] as [string, string]))}
        </>
      );
      break;
    case "news_search":
      main = (
        <>
          {yesNo("Wrote", "Wrote about", "Didn't write")}
          {sel("News search", r.news_search ?? "", (v) => set({ ...r, news_search: v }), c.newsSearches.map((n) => [n.id, n.name] as [string, string]))}
          <When r={r} set={set} long={false} />
        </>
      );
      break;
  }
  return (
    <div className="px-3 py-2" data-rule={k}>
      <div className="flex flex-wrap items-center gap-1.5">
        {main}
        <Cross label="Remove" onClick={remove} className="order-2 ml-auto" />
      </div>
      {below}
    </div>
  );
}

// What "Add a condition" offers: the kind is picked once, here, so a row
// only shows the fields its kind needs.
type Start = { key: string; title: string; hint: string; rule: (c: RuleChoices, self?: string) => SmartListRule; when?: (c: RuleChoices) => boolean; people?: boolean };
const starts: Start[] = [
  { key: "event", title: kindText.event, hint: "An event, such as a payment or a sign-up", rule: (c, self) => startRule("event", c, self) },
  { key: "event_not", title: "Didn't do something", hint: "Never, or not in a time you choose", rule: () => ({ event: "", op: "not_exists" }) },
  { key: "page", title: kindText.page, hint: "On your website or app", rule: (c, self) => startRule("page", c, self), people: true },
  { key: "attribute", title: kindText.attribute, hint: "Plan, country, source or any other field", rule: (c, self) => startRule("attribute", c, self) },
  { key: "in_smart_list", title: kindText.in_smart_list, hint: "Another smart list", rule: (c, self) => startRule("in_smart_list", c, self) },
  { key: "company_attribute", title: kindText.company_attribute, hint: "Size, industry or any other company field", rule: (c, self) => startRule("company_attribute", c, self), people: true },
  { key: "product_stage", title: kindText.product_stage, hint: "Where they are with one of your products", rule: (c, self) => startRule("product_stage", c, self) },
  { key: "marketing_email", title: kindText.marketing_email, hint: "Or can't", rule: (c, self) => startRule("marketing_email", c, self), people: true },
  { key: "in_list", title: kindText.in_list, hint: "A list of people you found or imported", rule: (c, self) => startRule("in_list", c, self), when: (c) => c.leadLists.length > 0, people: true },
  { key: "news_search", title: kindText.news_search, hint: "Journalists from one of your news searches", rule: (c, self) => startRule("news_search", c, self), when: (c) => c.newsSearches.length > 0, people: true },
];

const dashed = "h-8 rounded-md border border-dashed px-3 text-[13px] font-medium text-muted-foreground hover:bg-muted hover:text-foreground";

function AddCondition({ onAdd, c, self, recordType }: { onAdd: (r: SmartListRule) => void; c: RuleChoices; self?: string; recordType: string }) {
  const [open, setOpen] = useState(false);
  const people = recordType !== "account";
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button type="button" className={dashed}>+ Add a condition</button>
      </PopoverTrigger>
      <PopoverContent className="w-[min(21rem,90vw)] p-1.5" align="start">
        <div role="menu" aria-label="Kind of condition">
          {starts.filter((s) => (people || !s.people) && (!s.when || s.when(c))).map((s) => (
            <button key={s.key} type="button" role="menuitem" className="block w-full rounded-md px-2.5 py-1.5 text-left text-[13px] hover:bg-muted focus-visible:bg-muted focus-visible:outline-none"
              onClick={() => { onAdd(s.rule(c, self)); setOpen(false); }}>
              <span className="block font-medium">{s.title}</span>
              <span className="block text-[12px] text-muted-foreground">{s.hint}</span>
            </button>
          ))}
        </div>
      </PopoverContent>
    </Popover>
  );
}

// The "and" or "or" between two conditions. With onSwitch it is a button
// that switches every one in its list.
function Joiner({ any, onSwitch }: { any: boolean; onSwitch?: () => void }) {
  const pill = "relative rounded-full border bg-background px-2 text-[11px] leading-[18px] font-semibold text-muted-foreground";
  return (
    <div className="relative flex h-5 items-center px-3">
      <div className="absolute inset-x-0 top-1/2 border-t" />
      {onSwitch ? (
        <button type="button" className={`${pill} hover:text-foreground`} onClick={onSwitch} aria-label={any ? "or: switch to and" : "and: switch to or"} title={any ? "Any one is enough. Click to need all of them." : "All are needed. Click so any one is enough."}>
          {any ? "or" : "and"} ▾
        </button>
      ) : (
        <span className={pill}>{any ? "or" : "and"}</span>
      )}
    </div>
  );
}

const setText: Record<GroupMatch, string> = { any: "Either of these", all: "All of these", none: "None of these" };

// Conditions edits a list of conditions: rows with "and" or "or" between
// them, sets of conditions (shaded, with their own all, either or none)
// and the buttons that add more. Sets go down to maxDepth.
function Conditions({ rules, onChange, any, onSwitch, sets = false, depth = 1, bare = false, self, recordType, c }: {
  rules: SmartListRule[]; onChange: (r: SmartListRule[]) => void; any: boolean; onSwitch?: () => void; sets?: boolean; depth?: number; bare?: boolean; self?: string; recordType: string; c: RuleChoices;
}) {
  const set = (i: number, r: SmartListRule) => onChange(rules.map((x, k) => (k === i ? r : x)));
  const remove = (i: number) => onChange(rules.filter((_, k) => k !== i));
  return (
    <div className={bare ? "" : "space-y-2"}>
      {rules.length > 0 && (
        <div className={bare ? "" : "overflow-hidden rounded-lg border"}>
          {rules.map((r, i) => (
            <Fragment key={i}>
              {i > 0 && <Joiner any={any} onSwitch={onSwitch} />}
              {isGroup(r) ? (
                <div className="bg-muted/50 pb-2" role="group" aria-label="Set of conditions">
                  <div className="flex items-center gap-2 px-3 pt-2">
                    {sel("Set match", r.match ?? "all", (v) => set(i, { ...r, match: v as GroupMatch }), (Object.keys(setText) as GroupMatch[]).map((m) => [m, setText[m]]),
                      "h-7 cursor-pointer rounded-md bg-transparent text-[12px] font-semibold text-muted-foreground outline-none hover:text-foreground")}
                    <Cross label="Remove set" onClick={() => remove(i)} className="ml-auto" />
                  </div>
                  <div className="pl-4">
                    <Conditions rules={r.rules ?? []} onChange={(rs) => (rs.length ? set(i, { ...r, rules: rs }) : remove(i))} any={r.match !== "all"} sets={sets} depth={depth + 1} bare self={self} recordType={recordType} c={c} />
                  </div>
                </div>
              ) : (
                <OneRule r={r} set={(x) => set(i, x)} remove={() => remove(i)} c={c} self={self} recordType={recordType} />
              )}
            </Fragment>
          ))}
        </div>
      )}
      {rules.length < 20 && (
        <div className={`flex flex-wrap gap-2 ${bare ? "px-3 pt-1" : ""}`}>
          <AddCondition onAdd={(r) => onChange([...rules, r])} c={c} self={self} recordType={recordType} />
          {sets && !bare && depth < maxDepth && (
            <button type="button" className={dashed} onClick={() => onChange([...rules, { match: any ? "all" : "any", rules: [newRule()] }])}>
              {any ? '+ Add an "all of these" set' : '+ Add an "either/or" set'}
            </button>
          )}
        </div>
      )}
    </div>
  );
}

// Rules edits a flat list of rules, for places that keep their own "all"
// or "any" (pass it as match so the words between rows agree).
export function Rules({ rules, onChange, match = "all", self, recordType = "person", ...c }: RuleChoices & {
  rules: SmartListRule[]; onChange: (r: SmartListRule[]) => void; match?: "all" | "any"; self?: string; recordType?: string;
}) {
  return <Conditions rules={rules} onChange={onChange} any={match === "any"} self={self} recordType={recordType} c={c} />;
}

// A smart list's rules are stored as one list with all or any, where a
// group set to "none" leaves people out. The builder shows that as two
// plain parts: who to include, and who to leave out.
export type Parts = { include: SmartListRule[]; any: boolean; exclude: SmartListRule[] };

// parts splits stored rules into the two parts. Leaving people out only
// works beside rules that must all pass, so with exclusions an "any" list
// is stored as all of: (any of the included rules), none of (the excluded).
export function parts(rules: SmartListRule[], match: string): Parts {
  if (match === "any") return { include: rules, any: true, exclude: [] };
  const out = (r: SmartListRule) => isGroup(r) && r.match === "none";
  const exclude = rules.filter(out).flatMap((r) => r.rules ?? []);
  const include = rules.filter((r) => !out(r));
  if (exclude.length > 0 && include.length === 1 && isGroup(include[0]) && include[0].match === "any") {
    return { include: include[0].rules ?? [], any: true, exclude };
  }
  return { include, any: false, exclude };
}

// stored puts the two parts back into rules and a match.
export function stored(p: Parts): { rules: SmartListRule[]; match: "all" | "any" } {
  if (p.exclude.length === 0) return { rules: p.include, match: p.any ? "any" : "all" };
  const none: SmartListRule = { match: "none", rules: p.exclude };
  if (!p.any || p.include.length < 2) return { rules: [...p.include, none], match: "all" };
  return { rules: [{ match: "any", rules: p.include }, none], match: "all" };
}

// conditions counts the conditions in rules, through every set.
export const conditions = (rules: SmartListRule[]): number => rules.reduce((n, r) => n + (isGroup(r) ? conditions(r.rules ?? []) : 1), 0);

// plainText says the whole list in words, in the two parts.
export function plainText(rules: SmartListRule[], match: string, names: Names): string {
  const p = parts(rules, match);
  const say = (r: SmartListRule) => ruleText(r, names.field, names.list);
  const include = p.include.map(say).join(p.any ? ", or " : ", and ");
  const exclude = p.exclude.map(say).join(", or ");
  if (!exclude) return include;
  return `${include || "anyone"}. Not anyone who ${exclude}`;
}

// Builder is the smart list editor's rules: "Include people who", with
// "and" or "or" between conditions and either/or sets, then "But leave out
// people who".
export function Builder({ rules, match, onChange, self, recordType = "person", ...c }: RuleChoices & {
  rules: SmartListRule[]; match: string; onChange: (rules: SmartListRule[], match: "all" | "any") => void; self?: string; recordType?: string;
}) {
  const p = parts(rules, match);
  const put = (np: Parts) => {
    const s = stored(np);
    onChange(s.rules, s.match);
  };
  const who = recordType === "account" ? "companies that" : "people who";
  const head = "mb-2 flex items-center gap-2 text-[13px] font-semibold";
  // With people left out, an "any" list is stored one level deeper, so its
  // sets can't hold sets of their own.
  const room = p.any && p.exclude.length > 0 ? 1 : 0;
  return (
    <div className="space-y-6">
      <section aria-label="Include">
        <h3 className={head}><span className="size-2 rounded-full bg-emerald-600" />Include {who}</h3>
        <Conditions rules={p.include} onChange={(include) => put({ ...p, include })} any={p.any} onSwitch={() => put({ ...p, any: !p.any })} sets depth={1 + room} self={self} recordType={recordType} c={c} />
      </section>
      <section aria-label="Leave out">
        <h3 className={head}><span className="size-2 rounded-full bg-red-600" />But leave out {who}</h3>
        <Conditions rules={p.exclude} onChange={(exclude) => put({ ...p, exclude })} any self={self} recordType={recordType} c={c} />
      </section>
    </div>
  );
}

// useNames turns field keys and list ids into what people call them.
export function useNames(c: RuleChoices) {
  return useMemo(() => {
    const fields = new Map([...c.companyFields, ...c.fields].map((f) => [f.key, f.label]));
    const lists = new Map<string, string>([...c.lists.map((l) => [l.id, l.name] as const), ...c.leadLists.map((l) => [l.id, l.name] as const), ...c.newsSearches.map((n) => [n.id, n.name] as const)]);
    return { field: (k: string) => fields.get(k) ?? k, list: (id: string) => lists.get(id) ?? "a list" };
  }, [c.fields, c.companyFields, c.lists, c.leadLists, c.newsSearches]);
}
