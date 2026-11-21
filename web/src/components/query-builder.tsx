import { useState, type DragEvent, type ReactNode } from "react";
import type { Schemas } from "@outflare/sdk";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command";
import { Icon } from "@/icons";
import { cn } from "@/lib/utils";
import { field } from "../ui";

// The visual query builder (docs/query.md, "Build it without SQL"): a
// table to start from, the columns to show, filters, order and how many.
// The engine writes its SQL (POST .../query/build), so what is built here
// is always exactly what runs.

export type Builder = Schemas["QueryBuilder"];
export type BuilderColumn = Schemas["QueryBuilderColumn"];
export type BuilderFilter = Schemas["QueryBuilderFilter"];
export type BuilderSource = Schemas["QueryBuilderSource"];
type Related = NonNullable<BuilderSource["related"]>;
type Op = Schemas["QueryBuilderOp"];
type Table = Schemas["QueryTable"];

/** A catalog field people can pick, by record type (person, account). */
export type CatalogField = { key: string; label: string; value_type: string };

export type BuilderChoices = {
  tables: Table[];
  fields: Record<string, CatalogField[]>;
};

const recordTypes: Record<string, string> = { people: "person", companies: "account" };
const identities = ["email", "user_id", "phone", "domain", "anonymous_id"];

const opWords: Record<Op, string> = {
  eq: "is",
  neq: "is not",
  gt: "is more than",
  gte: "is at least",
  lt: "is less than",
  lte: "is at most",
  contains: "contains",
  not_contains: "doesn't contain",
  starts_with: "starts with",
  in: "is one of",
  not_in: "is none of",
  is_set: "has a value",
  not_set: "is empty",
  within_days: "is within the last … days",
  before_days: "is more than … days ago",
};
const noValue: Op[] = ["is_set", "not_set"];
const dayOps: Op[] = ["within_days", "before_days"];
const aggWords: Record<Related["agg"], string> = { count: "Number of", count_distinct: "Number of different", sum: "Total", avg: "Average", min: "Lowest", max: "Highest" };

// Names people know for the columns whose own names are database words.
const knownNames: Record<string, string> = {
  brand_amount_minor: "Amount",
  amount_minor: "Amount in its own currency",
  record_id: "Person or company",
  created_at: "Created",
  updated_at: "Last changed",
  occurred_at: "When it happened",
  anonymous_id: "Anonymous id",
  user_id: "User id",
};

/** humanize turns a column name into words: created_at → Created, plan_name → Plan name. */
export const humanize = (s: string) => {
  if (knownNames[s]) return knownNames[s];
  const words = s.replace(/_(minor|cents)$/, "").replace(/_at$/, "").replace(/_/g, " ");
  return words.charAt(0).toUpperCase() + words.slice(1);
};

/** money says a column holds an amount in cents (minor units). */
export const money = (col?: string) => !!col && /(_minor|_cents)$/.test(col);

/** sourceText says where a value comes from, in words. */
export function sourceText(s: BuilderSource, fields: CatalogField[] = []): string {
  if (s.column) return humanize(s.column);
  if (s.field) return fields.find((f) => f.key === s.field)?.label ?? humanize(s.field);
  if (s.identity) return humanize(s.identity);
  if (s.related) {
    const r = s.related;
    const what = r.agg === "count" && !r.column ? `${humanize(r.table).toLowerCase()}` : `${humanize(r.column ?? "").toLowerCase()} of ${humanize(r.table).toLowerCase()}`;
    const where = (r.filters ?? []).map((f) => `${humanize(f.column).toLowerCase()} ${opWords[f.op]}${noValue.includes(f.op) ? "" : ` ${String(f.value ?? "")}`}`).join(", ");
    return `${aggWords[r.agg]} ${what}${where ? ` (${where})` : ""}`;
  }
  return "Pick a value";
}

/** summary says the whole builder in one line. */
export function builderSummary(b: Builder): string {
  const parts = [humanize(b.from), `${b.columns.length} column${b.columns.length === 1 ? "" : "s"}`];
  if (b.filters?.length) parts.push(`${b.filters.length} filter${b.filters.length === 1 ? "" : "s"}`);
  if (b.sort?.length) parts.push(`sorted by ${b.sort.map((s) => s.column).join(", ")}`);
  if (b.limit) parts.push(`first ${b.limit.toLocaleString()} rows`);
  return parts.join(" · ");
}

export function emptyBuilder(from = "people"): Builder {
  const cols: BuilderColumn[] = from === "people"
    ? [{ label: "Name", source: { column: "name" } }, { label: "Email", source: { column: "email" } }]
    : [{ label: "Name", source: { column: "name" } }];
  return { from, columns: cols, filters: [], match: "all", sort: [], limit: null };
}

// ---- A searchable picker ----

export type PickItem = { value: string; label: string; group: string; hint?: string };

export function Picker({ items, value, onPick, placeholder, label, className }: { items: PickItem[]; value: string; onPick: (v: string) => void; placeholder: string; label: string; className?: string }) {
  const [open, setOpen] = useState(false);
  const current = items.find((i) => i.value === value);
  const groups = [...new Set(items.map((i) => i.group))];
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button type="button" aria-label={label} className={cn("flex h-8 min-w-0 items-center justify-between gap-2 rounded-md border bg-background px-2.5 text-left text-[13px] shadow-xs hover:bg-accent dark:bg-input/30", className)}>
          <span className={cn("truncate", !current && "text-muted-foreground")}>{current?.label ?? placeholder}</span>
          <Icon name="updown" className="size-3.5 shrink-0 text-muted-foreground" />
        </button>
      </PopoverTrigger>
      <PopoverContent className="w-80 p-0" align="start">
        <Command>
          <CommandInput placeholder="Search" />
          <CommandList className="max-h-80">
            <CommandEmpty>Nothing matches.</CommandEmpty>
            {groups.map((g) => (
              <CommandGroup key={g} heading={g}>
                {items.filter((i) => i.group === g).map((i) => (
                  <CommandItem key={i.value} value={`${i.label} ${i.value} ${g}`} onSelect={() => { setOpen(false); onPick(i.value); }}>
                    <span className="min-w-0 flex-1 truncate">{i.label}</span>
                    {i.hint && <span className="shrink-0 font-mono text-[11px] text-muted-foreground">{i.hint}</span>}
                    {i.value === value && <Icon name="check" className="size-3.5" />}
                  </CommandItem>
                ))}
              </CommandGroup>
            ))}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}

// ---- Sources ----

const encode = (s: BuilderSource) => (s.column ? `column:${s.column}` : s.field ? `field:${s.field}` : s.identity ? `identity:${s.identity}` : s.related ? "related" : "");
function decode(v: string): BuilderSource | null {
  const [kind, ...rest] = v.split(":");
  const name = rest.join(":");
  if (kind === "column") return { column: name };
  if (kind === "field") return { field: name };
  if (kind === "identity") return { identity: name };
  return null;
}

function sourceItems(from: Table | undefined, fields: CatalogField[], current?: BuilderSource): PickItem[] {
  const items: PickItem[] = [];
  for (const c of from?.columns ?? []) {
    if (c.name === "attributes") continue;
    items.push({ value: `column:${c.name}`, label: humanize(c.name), group: `Columns of ${from!.name}`, hint: c.type.replace("timestamp with time zone", "time") });
  }
  for (const f of fields) items.push({ value: `field:${f.key}`, label: f.label, group: "Fields", hint: f.value_type });
  if (from && recordTypes[from.name]) {
    const ids = new Set(identities);
    if (current?.identity) ids.add(current.identity);
    for (const i of ids) items.push({ value: `identity:${i}`, label: humanize(i), group: "Identities", hint: i });
  }
  items.push({ value: "related", label: "A total from another table…", group: "From another table", hint: "count, sum, last…" });
  return items;
}

function SourceEditor({ value, onChange, choices, from, label }: { value: BuilderSource; onChange: (s: BuilderSource) => void; choices: BuilderChoices; from: string; label: string }) {
  const table = choices.tables.find((t) => t.name === from);
  const fields = choices.fields[recordTypes[from] ?? ""] ?? [];
  return (
    <div className="min-w-0 space-y-2">
      <Picker
        label={label}
        items={sourceItems(table, fields, value)}
        value={encode(value)}
        placeholder="Pick a value"
        className="w-full"
        onPick={(v) => {
          if (v === "related") {
            const other = choices.tables.find((t) => t.available && t.name !== from && t.columns.some((c) => c.name === "record_id"));
            onChange({ related: { table: other?.name ?? "transactions", link: "record_id", agg: "count" } });
          } else {
            const s = decode(v);
            if (s) onChange(s);
          }
        }}
      />
      {value.related && <RelatedEditor value={value.related} onChange={(related) => onChange({ related })} choices={choices} from={from} />}
    </div>
  );
}

const colItems = (cols: Table["columns"], group: string): PickItem[] => cols.map((c) => ({ value: c.name, label: humanize(c.name), group, hint: c.type.replace("timestamp with time zone", "time") }));

function RelatedEditor({ value, onChange, choices, from }: { value: Related; onChange: (r: Related) => void; choices: BuilderChoices; from: string }) {
  const [showLink, setShowLink] = useState(false);
  const tables = choices.tables.filter((t) => t.available && t.name !== from);
  const other = choices.tables.find((t) => t.name === value.table);
  const cols = other?.columns ?? [];
  const set = (p: Partial<Related>) => onChange({ ...value, ...p });
  const linked = value.link === "record_id" && !value.link_from;
  return (
    <div className="space-y-2 rounded-md border border-dashed bg-muted/20 p-2.5 text-[12px]">
      <div className="grid grid-cols-[3.5rem_minmax(0,1fr)] items-center gap-x-2 gap-y-1.5">
        <span className="text-muted-foreground">Show the</span>
        <div className="min-w-0">
          <select aria-label="How to total" className={cn(field, "h-8 w-full")} value={value.agg} onChange={(e) => set({ agg: e.target.value as Related["agg"], column: e.target.value === "count" ? undefined : value.column ?? cols.find((c) => money(c.name))?.name ?? cols.find((c) => c.name !== value.link)?.name })}>
            {(Object.keys(aggWords) as Related["agg"][]).map((a) => <option key={a} value={a}>{aggWords[a].toLowerCase()}</option>)}
          </select>
        </div>
        {value.agg !== "count" && (
          <>
            <span className="text-muted-foreground">of</span>
            <Picker label="Which column" className="w-full" placeholder="Column" value={value.column ?? ""} onPick={(c) => set({ column: c })} items={colItems(cols, humanize(value.table))} />
          </>
        )}
        <span className="text-muted-foreground">{value.agg === "count" ? "rows in" : "from"}</span>
        <Picker
          label="Which table"
          className="w-full"
          placeholder="Table"
          value={value.table}
          onPick={(t) => {
            const next = choices.tables.find((x) => x.name === t);
            const link = next?.columns.some((c) => c.name === "record_id") ? "record_id" : next?.columns[0]?.name ?? "";
            onChange({ table: t, link, agg: "count" });
          }}
          items={tables.map((t) => ({ value: t.name, label: humanize(t.name), group: t.columns.some((c) => c.name === "record_id") ? "About this person or company" : "Other tables", hint: t.name }))}
        />
      </div>
      {(value.filters ?? []).map((f, i) => (
        <div key={i} className="grid grid-cols-[3.5rem_minmax(0,1fr)_auto] items-start gap-2">
          <span className="pt-2 text-muted-foreground">{i === 0 ? "only if" : "and"}</span>
          <div className="min-w-0 space-y-1.5">
            <Picker label="Column" className="w-full" placeholder="Column" value={f.column} onPick={(c) => set({ filters: (value.filters ?? []).map((x, k) => (k === i ? { ...x, column: c } : x)) })} items={colItems(cols, humanize(value.table))} />
            <OpValue op={f.op} value={f.value} onChange={(op, v) => set({ filters: (value.filters ?? []).map((x, k) => (k === i ? { ...x, op, value: v } : x)) })} />
          </div>
          <button type="button" aria-label="Remove this condition" className="rounded p-1 text-muted-foreground hover:bg-accent hover:text-destructive" onClick={() => set({ filters: (value.filters ?? []).filter((_, k) => k !== i) })}><Icon name="x" className="size-3.5" /></button>
        </div>
      ))}
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <button type="button" className="text-[12px] text-primary hover:underline" onClick={() => set({ filters: [...(value.filters ?? []), { column: cols.find((c) => c.name === "kind" || c.name === "status")?.name ?? cols[0]?.name ?? "", op: "eq", value: "" }] })}>
          + Only count some rows
        </button>
        {linked && !showLink ? (
          <button type="button" className="text-[12px] text-muted-foreground hover:text-foreground" onClick={() => setShowLink(true)}>Linked to this row by its person or company</button>
        ) : (
          <label className="flex items-center gap-1.5 text-muted-foreground">
            Linked where its
            <select aria-label="Linked by" className={cn(field, "h-7 w-auto")} value={value.link} onChange={(e) => set({ link: e.target.value })}>
              {cols.map((c) => <option key={c.name} value={c.name}>{humanize(c.name)}</option>)}
            </select>
            is this row
          </label>
        )}
      </div>
    </div>
  );
}

function OpValue({ op, value, onChange }: { op: Op; value: unknown; onChange: (op: Op, v: unknown) => void }) {
  const text = Array.isArray(value) ? value.join(", ") : value === undefined || value === null ? "" : String(value);
  const parse = (o: Op, raw: string): unknown => {
    if (noValue.includes(o)) return undefined;
    if (dayOps.includes(o)) return Number(raw) || 0;
    if (o === "in" || o === "not_in") return raw.split(",").map((x) => x.trim()).filter(Boolean).map((x) => (x !== "" && !Number.isNaN(Number(x)) ? Number(x) : x));
    if (["gt", "gte", "lt", "lte"].includes(o) && raw.trim() !== "" && !Number.isNaN(Number(raw))) return Number(raw);
    if (raw === "true" || raw === "false") return raw === "true";
    if ((o === "eq" || o === "neq") && raw.trim() !== "" && !Number.isNaN(Number(raw))) return Number(raw);
    return raw;
  };
  return (
    <div className="flex min-w-0 flex-1 items-center gap-1.5">
      <select aria-label="Test" className={cn(field, "h-8 w-36 shrink-0")} value={op} onChange={(e) => onChange(e.target.value as Op, parse(e.target.value as Op, text))}>
        {(Object.keys(opWords) as Op[]).map((o) => <option key={o} value={o}>{opWords[o]}</option>)}
      </select>
      {!noValue.includes(op) && (
        <input
          aria-label="Value"
          className={cn(field, "h-8 min-w-0", dayOps.includes(op) ? "w-20" : "flex-1")}
          type={dayOps.includes(op) ? "number" : "text"}
          min={0}
          placeholder={op === "in" || op === "not_in" ? "a, b, c" : dayOps.includes(op) ? "30" : "Value"}
          value={text}
          onChange={(e) => onChange(op, parse(op, e.target.value))}
        />
      )}
      {dayOps.includes(op) && <span className="shrink-0 text-[12px] text-muted-foreground">days</span>}
    </div>
  );
}

// ---- The builder ----

function Section({ title, hint, children, action }: { title: string; hint?: string; children: ReactNode; action?: ReactNode }) {
  return (
    <section className="space-y-2">
      <div className="flex items-center gap-2">
        <h3 className="text-[12px] font-semibold tracking-wide text-muted-foreground uppercase">{title}</h3>
        <div className="flex-1" />
        {action}
      </div>
      {hint && <p className="text-[12px] text-muted-foreground">{hint}</p>}
      {children}
    </section>
  );
}

const addButton = "rounded-md px-2 py-1 text-[12px] font-medium text-primary hover:bg-accent";

export function BuilderEditor({ value, onChange, choices, readOnly = false }: { value: Builder; onChange: (b: Builder) => void; choices: BuilderChoices; readOnly?: boolean }) {
  const [drag, setDrag] = useState<number | null>(null);
  const set = (p: Partial<Builder>) => onChange({ ...value, ...p });
  const cols = value.columns;
  const filters = value.filters ?? [];
  const sort = value.sort ?? [];
  const from = choices.tables.find((t) => t.name === value.from);
  const fields = choices.fields[recordTypes[value.from] ?? ""] ?? [];
  const labels = cols.map((c) => c.label);
  const setCol = (i: number, c: BuilderColumn) => {
    const old = cols[i];
    // An amount in cents shows as money by itself.
    const was = old.source.column ?? old.source.related?.column;
    const now = c.source.column ?? c.source.related?.column;
    if (now !== was) c = { ...c, divide_by: money(now) ? 100 : c.divide_by === 100 && money(was) ? undefined : c.divide_by };
    const next = cols.map((x, k) => (k === i ? c : x));
    // A renamed column keeps its filters and sort.
    const rename = (l?: string) => (l === old.label ? c.label : l);
    set({ columns: next, filters: filters.map((f) => (f.column ? { ...f, column: rename(f.column) } : f)), sort: sort.map((s) => ({ ...s, column: rename(s.column) ?? s.column })) });
  };
  const freshLabel = (base: string) => {
    let l = base;
    for (let n = 2; labels.some((x) => x.toLowerCase() === l.toLowerCase()); n++) l = `${base} ${n}`;
    return l;
  };
  const move = (i: number, to: number) => {
    const next = [...cols];
    const [moved] = next.splice(i, 1);
    next.splice(to, 0, moved);
    set({ columns: next });
  };
  const onDrop = (i: number) => (e: DragEvent) => {
    e.preventDefault();
    if (drag === null || drag === i) return;
    setDrag(null);
    move(drag, i);
  };
  const tableItems: PickItem[] = choices.tables.filter((t) => t.available).map((t) => ({
    value: t.name,
    label: humanize(t.name),
    group: recordTypes[t.name] ? "People and companies" : t.columns.some((c) => c.name === "record_id") ? "About people" : "Everything else",
    hint: t.name,
  }));
  const labelItems = labels.map((l) => ({ value: l, label: l, group: "Columns" }));

  return (
    <fieldset disabled={readOnly} className="min-w-0 space-y-5">
      <Section title="Start from">
        <Picker label="Start from" className="w-full" items={tableItems} value={value.from} placeholder="Pick a table" onPick={(t) => onChange(emptyBuilder(t))} />
        <p className="text-[12px] text-muted-foreground">One row for each {from && recordTypes[from.name] ? (from.name === "people" ? "person" : "company") : "row of this table"}{from && recordTypes[from.name] ? ". It can become a list." : "."}</p>
      </Section>

      <Section
        title={`Columns · ${cols.length}`}
        action={<button type="button" className={addButton} onClick={() => set({ columns: [...cols, { label: freshLabel("New column"), source: { column: from?.columns.find((c) => c.name !== "id")?.name ?? "id" } }] })}>+ Add</button>}
      >
        <ol className="space-y-1.5">
          {cols.map((c, i) => {
            const sourceCol = c.source.column ?? c.source.related?.column;
            return (
              <li
                key={i}
                draggable={!readOnly}
                onDragStart={() => setDrag(i)}
                onDragEnd={() => setDrag(null)}
                onDragOver={(e) => e.preventDefault()}
                onDrop={onDrop(i)}
                className={cn("group space-y-1.5 rounded-md border bg-background p-2 dark:bg-input/10", drag === i && "opacity-50")}
              >
                <div className="flex items-center gap-1">
                  <span className="cursor-grab px-0.5 text-muted-foreground select-none" title="Drag to reorder" aria-hidden>⋮⋮</span>
                  <input aria-label={`Name of column ${i + 1}`} className="h-7 min-w-0 flex-1 rounded border border-transparent bg-transparent px-1.5 text-[13px] font-medium outline-none hover:border-input focus:border-ring" maxLength={63} value={c.label} onChange={(e) => setCol(i, { ...c, label: e.target.value })} />
                  <span className="flex items-center opacity-60 group-hover:opacity-100 focus-within:opacity-100">
                    <button type="button" aria-label={`Move ${c.label} up`} disabled={i === 0} className="rounded p-1 text-muted-foreground hover:bg-accent disabled:opacity-30" onClick={() => move(i, i - 1)}>↑</button>
                    <button type="button" aria-label={`Move ${c.label} down`} disabled={i === cols.length - 1} className="rounded p-1 text-muted-foreground hover:bg-accent disabled:opacity-30" onClick={() => move(i, i + 1)}>↓</button>
                    <button type="button" aria-label={`Remove ${c.label}`} disabled={cols.length === 1} className="rounded p-1 text-muted-foreground hover:bg-accent hover:text-destructive disabled:opacity-30" onClick={() => set({ columns: cols.filter((_, k) => k !== i), filters: filters.filter((f) => f.column !== c.label), sort: sort.filter((s) => s.column !== c.label) })}><Icon name="x" className="size-3.5" /></button>
                  </span>
                </div>
                <SourceEditor label={`Value of ${c.label}`} value={c.source} from={value.from} choices={choices} onChange={(source) => setCol(i, { ...c, source, label: c.label.startsWith("New column") ? freshLabel(sourceText(source, fields)) : c.label })} />
                {(money(sourceCol) || c.divide_by) && (
                  <label className="flex items-center gap-1.5 text-[12px] text-muted-foreground">
                    <input type="checkbox" checked={c.divide_by === 100} onChange={(e) => setCol(i, { ...c, divide_by: e.target.checked ? 100 : undefined })} />
                    Show as money (stored in cents)
                  </label>
                )}
              </li>
            );
          })}
        </ol>
      </Section>

      <Section
        title={filters.length ? `Filters · ${filters.length}` : "Filters"}
        hint={filters.length ? undefined : "Every row shows. Add a filter to keep only some."}
        action={<button type="button" className={addButton} onClick={() => set({ filters: [...filters, { column: labels[0], op: "is_set" }] })}>+ Add</button>}
      >
        {filters.length > 1 && (
          <div className="inline-flex rounded-md border p-0.5 text-[12px]" role="radiogroup" aria-label="Keep rows that pass">
            {(["all", "any"] as const).map((m) => (
              <button key={m} type="button" role="radio" aria-checked={(value.match ?? "all") === m} className={cn("rounded px-2 py-0.5", (value.match ?? "all") === m ? "bg-accent text-foreground" : "text-muted-foreground")} onClick={() => set({ match: m })}>
                {m === "all" ? "Match every filter" : "Match any filter"}
              </button>
            ))}
          </div>
        )}
        <ul className="space-y-1.5">
          {filters.map((f, i) => {
            const setF = (p: Partial<BuilderFilter>) => set({ filters: filters.map((x, k) => (k === i ? { ...x, ...p } : x)) });
            const which = f.column !== undefined ? `label:${f.column}` : "source";
            return (
              <li key={i} className="space-y-1.5 rounded-md border bg-background p-2 text-[13px] dark:bg-input/10">
                <div className="flex items-center gap-1.5">
                  <span className="w-11 shrink-0 text-[12px] text-muted-foreground">{i === 0 ? "Where" : (value.match ?? "all") === "any" ? "or" : "and"}</span>
                  <Picker
                    label="What to test"
                    className="min-w-0 flex-1"
                    placeholder="Pick a column"
                    value={which}
                    items={[...labels.map((l) => ({ value: `label:${l}`, label: l, group: "Columns shown" })), { value: "source", label: "Something not shown…", group: "Other" }]}
                    onPick={(v) => (v === "source" ? setF({ column: undefined, source: { column: from?.columns[0]?.name ?? "id" } }) : setF({ column: v.slice(6), source: undefined, divide_by: undefined }))}
                  />
                  <button type="button" aria-label="Remove this filter" className="rounded p-1 text-muted-foreground hover:bg-accent hover:text-destructive" onClick={() => set({ filters: filters.filter((_, k) => k !== i) })}><Icon name="x" className="size-3.5" /></button>
                </div>
                {f.source && <div className="pl-12"><SourceEditor label="Value to test" value={f.source} from={value.from} choices={choices} onChange={(source) => setF({ source })} /></div>}
                <div className="flex pl-12"><OpValue op={f.op} value={f.value} onChange={(op, v) => setF({ op, value: v })} /></div>
              </li>
            );
          })}
        </ul>
      </Section>

      <Section
        title="Order"
        hint={sort.length ? undefined : "In no particular order."}
        action={sort.length < labels.length ? <button type="button" className={addButton} onClick={() => set({ sort: [...sort, { column: labels.find((l) => !sort.some((s) => s.column === l)) ?? labels[0], dir: "desc" }] })}>+ Add</button> : undefined}
      >
        {sort.map((s, i) => (
          <div key={i} className="flex items-center gap-1.5 text-[13px]">
            <span className="w-11 shrink-0 text-[12px] text-muted-foreground">{i === 0 ? "By" : "then"}</span>
            <Picker label="Sort column" className="min-w-0 flex-1" placeholder="Column" value={s.column} items={labelItems} onPick={(c) => set({ sort: sort.map((x, k) => (k === i ? { ...x, column: c } : x)) })} />
            <button
              type="button"
              className="h-8 shrink-0 rounded-md border px-2 text-[12px] hover:bg-accent"
              title="Change the direction"
              onClick={() => set({ sort: sort.map((x, k) => (k === i ? { ...x, dir: x.dir === "desc" ? "asc" : "desc" } : x)) })}
            >
              {s.dir === "desc" ? "↓ Biggest, newest first" : "↑ Smallest, oldest first"}
            </button>
            <button type="button" aria-label="Remove this sort" className="rounded p-1 text-muted-foreground hover:bg-accent hover:text-destructive" onClick={() => set({ sort: sort.filter((_, k) => k !== i) })}><Icon name="x" className="size-3.5" /></button>
          </div>
        ))}
      </Section>

      <Section title="Rows">
        <div className="inline-flex flex-wrap items-center gap-2 text-[13px]">
          <div className="inline-flex rounded-md border p-0.5 text-[12px]" role="radiogroup" aria-label="How many rows">
            {(["all", "some"] as const).map((m) => (
              <button key={m} type="button" role="radio" aria-checked={(value.limit ? "some" : "all") === m} className={cn("rounded px-2 py-0.5", (value.limit ? "some" : "all") === m ? "bg-accent text-foreground" : "text-muted-foreground")} onClick={() => set({ limit: m === "all" ? null : value.limit || 100 })}>
                {m === "all" ? "Every row" : "Only the first"}
              </button>
            ))}
          </div>
          {value.limit ? <input aria-label="Rows" type="number" min={1} max={50000} className={cn(field, "h-8 w-24")} value={value.limit} onChange={(e) => set({ limit: Math.max(1, Math.min(50000, Number(e.target.value) || 1)) })} /> : null}
        </div>
      </Section>
    </fieldset>
  );
}
