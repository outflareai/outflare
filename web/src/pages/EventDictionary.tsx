import { Fragment, useCallback, useEffect, useMemo, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Empty, ErrorText, field, Loading, toast } from "../ui";
import { ago } from "../format";
import { track } from "@/lib/analytics";
import { TimeChart } from "@/components/time-chart";

// The event list (once called the dictionary): every event name a brand
// has, with the name people see, what it means, how many arrived in the
// range picked (and from how many people) and what it carries.
// A row opens in place to its last 30 days, its properties and where the
// name is used, and to edit its label, description and property notes.
// The labels show wherever an event is picked, and the plain-English
// question box reads them to know what each event means.
//
// The properties an event lists are its tracking plan: the ones marked
// required must be on every event. Events that break the plan are stored
// all the same and counted, so each row says how many did in the last
// seven days and the open row says which properties.

type Definition = Schemas["EventDefinition"];
type Usage = Schemas["EventUsage"];
type Status = Definition["status"];
type Problem = Schemas["EventPlanProblem"];
type Count = Schemas["EventCount"];

// The ranges the counts can cover; 0 is all time (the definition's own count).
const ranges = [
  [7, "Last 7 days"],
  [30, "Last 30 days"],
  [90, "Last 90 days"],
  [0, "All time"],
] as const;
type Range = (typeof ranges)[number][0];

const statusNames: Record<Status, { label: string; tone: "green" | "amber" | "grey"; help: string }> = {
  approved: { label: "In use", tone: "green", help: "Stored and usable everywhere." },
  deprecated: { label: "Retired", tone: "grey", help: "Still stored, but marked as one not to build on." },
  quarantined: { label: "Paused", tone: "amber", help: "New ones are held until someone approves the name again." },
};

const whole = (n: number) => n.toLocaleString();

// sourceLabel names where an event came from in plain words.
export function sourceLabel(source: string): string {
  if (source === "browser") return "Website";
  if (source === "api") return "API";
  if (source === "outflare") return "Outflare";
  if (source === "transactions") return "Outflare payments";
  return source.charAt(0).toUpperCase() + source.slice(1);
}

// madeFrom says, for the events Outflare writes itself from others, what
// they are made from, so two names with the same count don't read as a
// double send.
const madeFrom: Record<string, string> = {
  "payment.succeeded":
    "Outflare writes one of these for each payment, once, whichever way it arrives: an order_completed event with revenue, a transaction sent to the API, or a billing connector such as Stripe. Revenue, attribution and ad conversions count these. Send this one to ad platforms, not order_completed as well, or each purchase counts twice.",
  "payment.failed": "Outflare writes one of these when a payment is sent to the API with the status failed, such as a declined card. It is not money and no report counts it as revenue.",
  "payment.disputed": "Outflare writes one of these for each chargeback sent to the API. The money comes off as a payment.refunded; this event says the customer disputed it.",
  "payment.refunded": "Outflare writes one of these for each refund, once, from an order_refunded event, a refund sent to the API or a billing connector.",
  order_completed:
    "Each one with revenue and a person also becomes one payment.succeeded event, so the two counts match. They are the same purchases, not duplicates. For ad conversions and revenue, use payment.succeeded.",
};
// describable is a key the catalog can hold a note for: lowercase
// letters, numbers and underscores.
const describable = (key: string) => /^[a-z][a-z0-9_]{1,62}$/.test(key);

export function EventDictionary({ brand, canManage }: { brand: string; canManage: boolean }) {
  const [list, setList] = useState<Definition[] | null>(null);
  const [error, setError] = useState("");
  const [search, setSearch] = useState("");
  const [show, setShow] = useState<"all" | "undescribed" | "problems" | Status>("all");
  const [problems, setProblems] = useState<Problem[]>([]);
  const [open, setOpen] = useState<string | null>(null);
  const [range, setRange] = useState<Range>(30);
  // Counts for the range by name; null while they load.
  const [counts, setCounts] = useState<Map<string, Count> | null>(null);

  useEffect(() => {
    if (range === 0) return setCounts(new Map());
    let live = true;
    setCounts(null);
    api.GET("/v1/brands/{brand}/catalog/event-counts", { params: { path: { brand }, query: { days: range } } }).then(({ data, error }) => {
      if (!live) return;
      if (!data) {
        toast(errorMessage(error), "error");
        return setCounts(new Map());
      }
      setCounts(new Map(data.data.map((c) => [c.name, c])));
    });
    return () => {
      live = false;
    };
  }, [brand, range]);
  // events and people are the counts for the range; all time has no people.
  const events = useCallback((e: Definition) => (range === 0 ? e.seen_count : (counts?.get(e.name)?.events ?? 0)), [range, counts]);

  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/catalog/events", { params: { path: { brand } } });
    if (!data) return setError(errorMessage(error));
    setError("");
    setList(data.data);
    // The plan's problems are an extra: the dictionary still shows without them.
    const found = await api.GET("/v1/brands/{brand}/catalog/event-problems", { params: { path: { brand }, query: { days: 7 } } });
    if (found.data) setProblems(found.data.data);
  }, [brand]);
  useEffect(() => {
    void load();
  }, [load]);

  // What broke the plan, by event name.
  const broken = useMemo(() => {
    const by = new Map<string, Problem[]>();
    for (const p of problems) by.set(p.event_name, [...(by.get(p.event_name) ?? []), p]);
    return by;
  }, [problems]);

  const shown = useMemo(() => {
    const q = search.trim().toLowerCase();
    return (list ?? [])
      .filter((e) => !e.same_as)
      .filter((e) => (show === "all" ? true : show === "undescribed" ? !e.description : show === "problems" ? broken.has(e.name) : e.status === show))
      .filter((e) => !q || e.name.includes(q) || e.label.toLowerCase().includes(q) || e.description.toLowerCase().includes(q))
      .sort((a, b) => events(b) - events(a) || b.seen_count - a.seen_count || a.name.localeCompare(b.name));
  }, [list, search, show, broken, events]);

  if (error) return <ErrorText>{error}</ErrorText>;
  if (!list) return <Loading />;
  if (list.length === 0) return <Empty title="No events yet">Events appear here as your website and product send them.</Empty>;
  const undescribed = list.filter((e) => !e.same_as && !e.description).length;

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-3">
        <input className={`${field} w-72`} placeholder="Search names and descriptions" aria-label="Search events" value={search} onChange={(e) => setSearch(e.target.value)} />
        <select className={field} aria-label="Show" value={show} onChange={(e) => setShow(e.target.value as typeof show)}>
          <option value="all">All events</option>
          <option value="undescribed">Without a description ({undescribed})</option>
          <option value="problems">Breaks the plan ({broken.size})</option>
          <option value="approved">In use</option>
          <option value="deprecated">Retired</option>
          <option value="quarantined">Paused</option>
        </select>
        <select
          className={field}
          aria-label="Count events from"
          value={range}
          onChange={(e) => {
            const days = Number(e.target.value) as Range;
            setRange(days);
            track("ui.event_list_range_changed", { days });
          }}
        >
          {ranges.map(([days, label]) => (
            <option key={days} value={days}>{label}</option>
          ))}
        </select>
        <span className="text-[12px] text-muted-foreground">
          {whole(shown.length)} of {whole(list.filter((e) => !e.same_as).length)} events
        </span>
      </div>
      <div className="overflow-x-auto rounded-md border bg-card">
        <table className="w-full text-[13px]">
          <thead>
            <tr className="border-b text-left font-mono text-[11px] uppercase text-muted-foreground">
              <th className="px-3 py-2 font-medium">Event</th>
              <th className="px-3 py-2 font-medium">What it means</th>
              <th className="px-3 py-2 text-right font-medium">Events</th>
              <th className="px-3 py-2 text-right font-medium" title="Different people, known or anonymous, who sent it in the range">People</th>
              <th className="px-3 py-2 font-medium">Source</th>
              <th className="px-3 py-2 font-medium">Last seen</th>
              <th className="px-3 py-2 font-medium">Plan</th>
              <th className="px-3 py-2 font-medium">Status</th>
            </tr>
          </thead>
          <tbody>
            {shown.map((e) => (
              <Fragment key={e.name}>
                <tr className={`cursor-pointer border-b last:border-0 hover:bg-muted/40 ${open === e.name ? "bg-muted/40" : ""}`} onClick={() => setOpen(open === e.name ? null : e.name)} aria-expanded={open === e.name}>
                  <td className="max-w-[280px] px-3 py-2 align-top">
                    <div className="truncate font-medium">{e.label || e.name}</div>
                    {e.label && <div className="truncate font-mono text-[11px] text-muted-foreground">{e.name}</div>}
                  </td>
                  <td className="max-w-[420px] px-3 py-2 align-top text-muted-foreground">
                    {e.description ? (
                      <span className="line-clamp-2">{e.description}</span>
                    ) : madeFrom[e.name] ? (
                      <span className="line-clamp-2" title={madeFrom[e.name]}>{madeFrom[e.name]}</span>
                    ) : (
                      <span className="italic">No description yet</span>
                    )}
                  </td>
                  <td className="px-3 py-2 text-right align-top tabular-nums">{range !== 0 && !counts ? "…" : whole(events(e))}</td>
                  <td className="px-3 py-2 text-right align-top tabular-nums text-muted-foreground">
                    {range === 0 ? "–" : !counts ? "…" : whole(counts.get(e.name)?.people ?? 0)}
                  </td>
                  <td className="max-w-[200px] px-3 py-2 align-top text-muted-foreground">
                    {range === 0 ? "–" : !counts ? "…" : (counts.get(e.name)?.sources ?? []).map((x) => sourceLabel(x.source)).join(", ") || "–"}
                  </td>
                  <td className="whitespace-nowrap px-3 py-2 align-top text-muted-foreground">{e.last_seen_at ? ago(e.last_seen_at) : "Never"}</td>
                  <td className="whitespace-nowrap px-3 py-2 align-top">
                    <PlanBadge event={e} problems={broken.get(e.name)} />
                  </td>
                  <td className="px-3 py-2 align-top">
                    <Badge tone={statusNames[e.status].tone}>{statusNames[e.status].label}</Badge>
                  </td>
                </tr>
                {open === e.name && (
                  <tr className="border-b last:border-0">
                    <td colSpan={8} className="bg-muted/20 px-4 py-4">
                      <Detail brand={brand} event={e} problems={broken.get(e.name) ?? []} canManage={canManage} onSaved={load} />
                    </td>
                  </tr>
                )}
              </Fragment>
            ))}
            {shown.length === 0 && (
              <tr>
                <td colSpan={8} className="px-3 py-6 text-center text-muted-foreground">Nothing matches.</td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// PlanBadge says how an event stands against its plan over the last
// seven days: no plan, followed, or how many events broke it.
function PlanBadge({ event, problems }: { event: Definition; problems?: Problem[] }) {
  if (event.properties.length === 0) return <span className="text-muted-foreground">No plan</span>;
  if (!problems?.length) return <Badge tone="green">Followed</Badge>;
  // The most events any one property was wrong on: an event with two
  // problems is one event.
  const worst = Math.max(...problems.map((p) => p.events));
  return <Badge tone="amber">{whole(worst)} broke it</Badge>;
}

function Detail({ brand, event, problems, canManage, onSaved }: { brand: string; event: Definition; problems: Problem[]; canManage: boolean; onSaved: () => Promise<void> }) {
  const [usage, setUsage] = useState<Usage | null>(null);
  const [error, setError] = useState("");
  const [label, setLabel] = useState(event.label);
  const [description, setDescription] = useState(event.description);
  const [status, setStatus] = useState<Status>(event.status);
  const [notes, setNotes] = useState<Record<string, string>>(() => Object.fromEntries(event.properties.map((p) => [p.key, p.description])));
  const [required, setRequired] = useState<Record<string, boolean>>(() => Object.fromEntries(event.properties.map((p) => [p.key, p.required])));
  // Properties a person added to the plan here, before saving.
  const [added, setAdded] = useState<Record<string, boolean>>({});
  const [saving, setSaving] = useState(false);
  const problem = (key: string) => problems.find((p) => p.property === key);

  useEffect(() => {
    let live = true;
    api.GET("/v1/brands/{brand}/catalog/events/{event}/usage", { params: { path: { brand, event: event.name } } }).then(({ data, error }) => {
      if (!live) return;
      if (!data) setError(errorMessage(error));
      else setUsage(data);
    });
    return () => {
      live = false;
    };
  }, [brand, event.name]);

  // Every property someone defined or that was seen, by key.
  const rows = useMemo(() => {
    const seen = new Map((usage?.properties ?? []).map((p) => [p.key, p]));
    const keys = [...new Set([...event.properties.map((p) => p.key), ...seen.keys(), ...problems.map((p) => p.property)])].sort();
    return keys.map((key) => ({ key, defined: event.properties.find((p) => p.key === key), seen: seen.get(key) }));
  }, [event.properties, usage, problems]);

  const changed =
    label !== event.label || description !== event.description || status !== event.status || rows.some((r) => (notes[r.key] ?? "") !== (r.defined?.description ?? "") || !!required[r.key] !== !!r.defined?.required || (added[r.key] && !r.defined));

  async function save() {
    setSaving(true);
    // A property is in the plan once it has a note, is required or was
    // added; its type is the one already defined, or the one seen.
    const jsonType = (t?: string): Schemas["FieldValueType"] => (t === "number" ? "number" : t === "boolean" ? "checkbox" : "text");
    const properties = rows
      .filter((r) => describable(r.key) && (r.defined || (notes[r.key] ?? "").trim() || required[r.key] || added[r.key]))
      .map((r) => ({ key: r.key, value_type: r.defined?.value_type ?? jsonType(r.seen?.types[0]), description: (notes[r.key] ?? "").trim(), required: !!required[r.key] }));
    const { error } = await api.PATCH("/v1/brands/{brand}/catalog/events/{event}", {
      params: { path: { brand, event: event.name } },
      body: { label: label.trim(), description: description.trim(), status, properties },
    });
    setSaving(false);
    if (error) return toast(errorMessage(error), "error");
    toast(`Saved ${label.trim() || event.name}.`);
    await onSaved();
  }

  return (
    <div className="grid gap-6 lg:grid-cols-[minmax(0,360px)_minmax(0,1fr)]">
      <div className="space-y-3 text-[13px]">
        {madeFrom[event.name] && <p className="rounded-md border bg-card p-2.5 text-[12px] text-muted-foreground">{madeFrom[event.name]}</p>}
        <label className="block">
          <span className="text-muted-foreground">Name people see</span>
          <input className={`${field} mt-1 block w-full`} value={label} maxLength={80} placeholder={event.name} disabled={!canManage} onChange={(e) => setLabel(e.target.value)} />
        </label>
        <label className="block">
          <span className="text-muted-foreground">What it means</span>
          <textarea className={`${field} mt-1 block h-24 w-full`} value={description} maxLength={1000} placeholder="When it happens, and what it tells you. One or two sentences." disabled={!canManage} onChange={(e) => setDescription(e.target.value)} />
        </label>
        <label className="block">
          <span className="text-muted-foreground">Status</span>
          <select className={`${field} mt-1 block w-full`} value={status} disabled={!canManage} onChange={(e) => setStatus(e.target.value as Status)}>
            {(Object.keys(statusNames) as Status[]).map((s) => (
              <option key={s} value={s}>{statusNames[s].label}</option>
            ))}
          </select>
          <span className="mt-1 block text-[12px] text-muted-foreground">{statusNames[status].help}</span>
        </label>
        <div className="text-[12px] text-muted-foreground">
          Sent as <span className="font-mono">{event.name}</span>
          {event.first_seen_at ? `, first seen ${ago(event.first_seen_at)}` : ""}.
        </div>
        {canManage && (
          <Button onClick={save} disabled={!changed || saving}>{saving ? "Saving…" : "Save"}</Button>
        )}
      </div>
      <div className="min-w-0 space-y-5">
        {error && <ErrorText>{error}</ErrorText>}
        {!usage && !error && <Loading />}
        {usage && (
          <>
            <div>
              <div className="mb-2 flex flex-wrap items-baseline gap-x-6 gap-y-1 text-[13px]">
                <span className="font-medium">Last 30 days</span>
                <span className="text-muted-foreground"><span className="font-semibold text-foreground tabular-nums">{whole(usage.count)}</span> events</span>
                <span className="text-muted-foreground"><span className="font-semibold text-foreground tabular-nums">{whole(usage.people)}</span> people</span>
              </div>
              {usage.count > 0 ? (
                <TimeChart starts={usage.days.map((d) => `${d.day}T00:00:00Z`)} series={[{ label: "Events", values: usage.days.map((d) => d.count) }]} bars height={150} format={(v) => whole(v ?? 0)} label={`${event.name} per day`} />
              ) : (
                <p className="text-[13px] text-muted-foreground">None arrived in the last 30 days.</p>
              )}
            </div>
            <div>
              <div className="mb-2 text-[13px] font-medium">Properties</div>
              {rows.length === 0 ? (
                <p className="text-[13px] text-muted-foreground">It carries no properties.</p>
              ) : (
                <div className="overflow-x-auto rounded-md border bg-card">
                  <table className="w-full text-[12px]">
                    <thead>
                      <tr className="border-b text-left font-mono text-[11px] uppercase text-muted-foreground">
                        <th className="px-3 py-1.5 font-medium">Property</th>
                        <th className="px-3 py-1.5 font-medium">Type</th>
                        <th className="px-3 py-1.5 text-right font-medium">On</th>
                        <th className="px-3 py-1.5 font-medium">Common values</th>
                        <th className="px-3 py-1.5 font-medium">Required</th>
                        <th className="px-3 py-1.5 font-medium">Last 7 days</th>
                        <th className="px-3 py-1.5 font-medium">What it means</th>
                      </tr>
                    </thead>
                    <tbody>
                      {rows.map((r) => (
                        <tr key={r.key} className="border-b last:border-0">
                          <td className="px-3 py-1.5 font-mono">{r.key}</td>
                          <td className="px-3 py-1.5 text-muted-foreground">{r.seen ? r.seen.types.join(", ") : r.defined?.value_type}</td>
                          <td className="px-3 py-1.5 text-right tabular-nums text-muted-foreground">{r.seen ? `${Math.round(r.seen.share * 100)}%` : "Not seen"}</td>
                          <td className="max-w-[240px] px-3 py-1.5">
                            <div className="flex flex-wrap gap-1">
                              {(r.seen?.values ?? []).map((v) => (
                                <span key={v.value} className="max-w-[140px] truncate rounded bg-muted px-1.5 py-0.5 font-mono text-[11px]" title={`${v.value}: ${whole(v.count)}`}>{v.value}</span>
                              ))}
                            </div>
                          </td>
                          <td className="px-3 py-1.5">
                            <input type="checkbox" aria-label={`${r.key} is required`} checked={!!required[r.key]} disabled={!canManage || !describable(r.key)} onChange={(e) => setRequired({ ...required, [r.key]: e.target.checked })} />
                          </td>
                          <td className="whitespace-nowrap px-3 py-1.5">
                            <PropertyProblem found={problem(r.key)} inPlan={!!r.defined || !!added[r.key]} canAdd={canManage && describable(r.key)} onAdd={() => setAdded({ ...added, [r.key]: true })} />
                          </td>
                          <td className="min-w-[200px] px-3 py-1">
                            <input className={`${field} w-full text-[12px]`} aria-label={`What ${r.key} means`} value={notes[r.key] ?? ""} maxLength={300} placeholder={!describable(r.key) ? "Notes need a snake_case key" : canManage ? "Add a note" : ""} disabled={!canManage || !describable(r.key)} onChange={(e) => setNotes({ ...notes, [r.key]: e.target.value })} />
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
              <p className="mt-1 text-[11px] text-muted-foreground">
                The properties with a note or a tick are this event's plan. Events that arrive without a required one, or with one that isn't in the plan, are still stored and are counted here.
              </p>
              {usage.sampled > 0 && <p className="mt-1 text-[11px] text-muted-foreground">Read from the newest {whole(usage.sampled)} events. Values that could name a person are never shown.</p>}
            </div>
            <div>
              <div className="mb-2 text-[13px] font-medium">Used in</div>
              {usage.used_in.length === 0 ? (
                <p className="text-[13px] text-muted-foreground">No saved chart, conversion or journey uses it yet.</p>
              ) : (
                <ul className="flex flex-wrap gap-2 text-[12px]">
                  {usage.used_in.map((u) => (
                    <li key={u.kind + u.id} className="rounded-md border bg-card px-2 py-1">
                      <span className="text-muted-foreground">{u.kind === "insight" ? "Chart" : u.kind === "conversion" ? "Conversion" : "Journey"}</span> {u.name}
                    </li>
                  ))}
                </ul>
              )}
            </div>
          </>
        )}
      </div>
    </div>
  );
}

// PropertyProblem is what the last seven days' events got wrong about one
// property, with a way to put an unlisted one in the plan.
function PropertyProblem({ found, inPlan, canAdd, onAdd }: { found?: Problem; inPlan: boolean; canAdd: boolean; onAdd: () => void }) {
  if (!found) return <span className="text-muted-foreground">{inPlan ? "Fine" : ""}</span>;
  if (found.kind === "missing_property") return <span className="text-amber-700 dark:text-amber-400">Missing on {whole(found.events)}</span>;
  if (inPlan) return <span className="text-muted-foreground">Added, save to keep</span>;
  return (
    <span className="inline-flex items-center gap-2">
      <span className="text-amber-700 dark:text-amber-400">Not in the plan, on {whole(found.events)}</span>
      {canAdd && (
        <button type="button" className="underline underline-offset-2 hover:text-foreground" onClick={onAdd}>
          Add to plan
        </button>
      )}
    </span>
  );
}
