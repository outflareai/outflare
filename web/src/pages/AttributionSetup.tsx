// Attribution setup, under Channels: the workspace's own attribution models
// (a built-in model with its own windows, channels left out, weights or
// half-life, versioned so a change never rewrites what a module pinned),
// and its conversion definitions (which customer events count, what each is
// worth and each ad network's name for it). Saving either credits every
// conversion again in the background.

import { useCallback, useEffect, useState, type FormEvent } from "react";
import type { AdTrackingTemplate, ConversionDefinition, ConversionPreset, Schemas, TrackingChannel } from "@outflare/sdk";
import { DataTable } from "@/components/data-table";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, Input, Menu, confirmAction, field, toast } from "../ui";
import { ago } from "../format";

type CustomModel = Schemas["AttributionCustomModel"];
type Spec = Schemas["AttributionSpec"];
type Base = Spec["base"];
type Filter = Schemas["ConversionFilter"];
type DefinitionInput = Schemas["ConversionDefinitionInput"];
type ValueMode = NonNullable<DefinitionInput["value_mode"]>;

export const builtInModels: { id: Base; label: string; help: string }[] = [
  { id: "last_non_direct", label: "Last non-direct touch", help: "The last visit that wasn't direct gets all the credit." },
  { id: "first_touch", label: "First touch", help: "The first visit gets all the credit." },
  { id: "linear", label: "Linear", help: "Every visit gets an equal share." },
  { id: "position", label: "40/20/40", help: "40% to the first visit, 40% to the last, 20% shared by the rest." },
  { id: "time_decay", label: "Time decay", help: "Shared, with visits closer to the conversion getting more." },
];
const baseLabel = (b: Base) => builtInModels.find((m) => m.id === b)?.label ?? b;
const recredit = "Every conversion is being credited again in the background; reports catch up within a few minutes.";

// The channels a model can set a window for or leave out: the campaign
// channels, and direct.
function useChannels(ws: string) {
  const [channels, setChannels] = useState<{ id: string; label: string }[]>([]);
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/tracking-taxonomy", { params: { path: { ws } } }).then(({ data }) => {
      setChannels([...(data?.channels ?? []).map((c: TrackingChannel) => ({ id: c.id, label: c.label })), { id: "direct", label: "Direct" }]);
    });
  }, [ws]);
  return channels;
}

// specSummary says in a line what a model changes from its base.
function specSummary(s: Spec, channels: { id: string; label: string }[]) {
  const name = (id: string) => channels.find((c) => c.id === id)?.label ?? id;
  const parts = [`From ${baseLabel(s.base)}`];
  if (s.lookback_days) parts.push(`${s.lookback_days}-day window`);
  for (const [c, d] of Object.entries(s.channel_lookback_days ?? {})) parts.push(`${name(c)} ${d} days`);
  if (s.exclude_channels?.length) parts.push(`leaves out ${s.exclude_channels.map(name).join(", ")}`);
  if (s.base === "position" && (s.first_weight !== undefined || s.last_weight !== undefined)) {
    parts.push(`first ${Math.round((s.first_weight ?? 0.4) * 100)}%, last ${Math.round((s.last_weight ?? 0.4) * 100)}%`);
  }
  if (s.base === "time_decay" && s.half_life_days) parts.push(`halves every ${s.half_life_days} days`);
  return parts.join(" · ");
}

export function AttributionModels({ ws, canManage, defaultModel, onChanged }: { ws: string; canManage: boolean; defaultModel?: string; onChanged: () => void }) {
  const [list, setList] = useState<CustomModel[] | null>(null);
  const [editing, setEditing] = useState<CustomModel | "new" | null>(null);
  const channels = useChannels(ws);

  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/attribution/models", { params: { path: { ws } } });
    if (error) toast(errorMessage(error), "error");
    setList(data?.data ?? []);
  }, [ws]);
  useEffect(() => {
    load();
  }, [load]);

  async function archive(m: CustomModel) {
    if (!(await confirmAction(`Archive ${m.name}?`, "Reports can no longer use it. A version a module pins keeps its credit.", "Archive model"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/attribution/models/{key}", { params: { path: { ws, key: m.key } } });
    if (error) return toast(errorMessage(error), "error");
    toast(`${m.name} is archived.`);
    load();
    onChanged();
  }

  return (
    <Card
      title="Attribution models"
      description="Start from a built-in model and change what it credits: how far back a visit counts, per channel if you like, channels that never get credit, and the weights. Each change is a new version, so a module that pinned one keeps its numbers."
      action={canManage ? <Button variant="ghost" onClick={() => setEditing("new")}>New model</Button> : undefined}
    >
      {list === null ? null : list.length === 0 ? (
        <p className="text-[13px] text-muted-foreground">No models of your own yet. The five built-in ones are in the menu at the top of this page.</p>
      ) : (
        <DataTable
          data={list}
          rowId={(m) => m.key}
          searchPlaceholder="Search models"
          columns={[
            {
              id: "name",
              header: "Model",
              accessorFn: (m) => m.name,
              cell: ({ row: { original: m } }) => (
                <div className="space-y-0.5">
                  <div className="font-medium">
                    {m.name} {m.key === defaultModel && <Badge tone="green">Default</Badge>}
                  </div>
                  <div className="text-[12px] text-muted-foreground">{specSummary(m.spec, channels)}</div>
                </div>
              ),
            },
            {
              id: "version",
              header: "Version",
              accessorFn: (m) => m.version,
              cell: ({ row: { original: m } }) => (
                <div className="text-[13px]">
                  <span className="font-mono">{m.key}</span> · v{m.version}
                  <div className="text-[12px] text-muted-foreground">{ago(m.created_at)}{m.pinned_by.length > 0 && ` · pinned by ${m.pinned_by.join(", ")}`}</div>
                </div>
              ),
            },
            {
              id: "actions",
              header: "",
              enableSorting: false,
              cell: ({ row: { original: m } }) =>
                canManage && (
                  <div className="flex justify-end">
                    <Menu items={[{ label: "Edit", onClick: () => setEditing(m) }, m.key !== defaultModel && { label: "Archive", onClick: () => archive(m), danger: true }]} />
                  </div>
                ),
            },
          ]}
        />
      )}
      {editing && (
        <ModelDialog
          ws={ws}
          model={editing === "new" ? null : editing}
          channels={channels}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            load();
            onChanged();
          }}
        />
      )}
    </Card>
  );
}

// days reads an optional whole number of days; "" is none.
const days = (v: string) => (v.trim() === "" ? undefined : Number(v));

function ModelDialog({ ws, model, channels, onClose, onSaved }: { ws: string; model: CustomModel | null; channels: { id: string; label: string }[]; onClose: () => void; onSaved: () => void }) {
  const s = model?.spec;
  const [key, setKey] = useState("");
  const [name, setName] = useState(model?.name ?? "");
  const [base, setBase] = useState<Base>(s?.base ?? "last_non_direct");
  const [lookback, setLookback] = useState(s?.lookback_days ? String(s.lookback_days) : "");
  const [windows, setWindows] = useState<Record<string, string>>(Object.fromEntries(Object.entries(s?.channel_lookback_days ?? {}).map(([c, d]) => [c, String(d)])));
  const [excluded, setExcluded] = useState<string[]>(s?.exclude_channels ?? []);
  const [first, setFirst] = useState(s?.first_weight !== undefined ? String(Math.round(s.first_weight * 100)) : "");
  const [last, setLast] = useState(s?.last_weight !== undefined ? String(Math.round(s.last_weight * 100)) : "");
  const [halfLife, setHalfLife] = useState(s?.half_life_days ? String(s.half_life_days) : "");
  const [busy, setBusy] = useState(false);

  function spec(): Spec {
    const out: Spec = { base };
    if (days(lookback)) out.lookback_days = days(lookback);
    const w = Object.fromEntries(Object.entries(windows).filter(([, d]) => d.trim() !== "").map(([c, d]) => [c, Number(d)]));
    if (Object.keys(w).length > 0) out.channel_lookback_days = w;
    if (excluded.length > 0) out.exclude_channels = excluded;
    if (base === "position") {
      if (first.trim() !== "") out.first_weight = Number(first) / 100;
      if (last.trim() !== "") out.last_weight = Number(last) / 100;
    }
    if (base === "time_decay" && halfLife.trim() !== "") out.half_life_days = Number(halfLife);
    return out;
  }

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const body = { name: name.trim(), spec: spec() };
    const { error } = model
      ? await api.PATCH("/v1/workspaces/{ws}/attribution/models/{key}", { params: { path: { ws, key: model.key } }, body })
      : await api.POST("/v1/workspaces/{ws}/attribution/models", { params: { path: { ws } }, body: { ...body, key: key.trim() } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast(model ? `${body.name} is saved. ${recredit}` : `${body.name} is ready. ${recredit}`);
    onSaved();
  }

  const toggleExcluded = (id: string) => setExcluded((x) => (x.includes(id) ? x.filter((c) => c !== id) : [...x, id]));

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{model ? `Edit ${model.name}` : "New attribution model"}</DialogTitle>
          <DialogDescription>{model ? `Changing how it credits makes version ${model.version + 1}; a new name alone keeps version ${model.version}.` : "Pick the built-in model it starts from, then change only what you need."}</DialogDescription>
        </DialogHeader>
        <form className="space-y-3" onSubmit={save}>
          {!model && <Input label="Key" hint="What reports and agents call it: lowercase letters, digits and hyphens, such as paid-7-day. It never changes." required pattern="[a-z][a-z0-9\-]{0,39}" value={key} onChange={(e) => setKey(e.target.value)} />}
          <Input label="Name" required maxLength={80} placeholder="Paid ads 7 days" value={name} onChange={(e) => setName(e.target.value)} />
          <label className="flex flex-col gap-1 text-[13px]">
            <span className="font-medium">Starts from</span>
            <select className={field} value={base} onChange={(e) => setBase(e.target.value as Base)}>
              {builtInModels.map((m) => <option key={m.id} value={m.id}>{m.label}</option>)}
            </select>
            <span className="text-[12px] text-muted-foreground">{builtInModels.find((m) => m.id === base)?.help}</span>
          </label>
          <Input label="Look-back window, days (optional)" hint="How far before a conversion a visit can get credit. Empty uses the workspace's window." type="number" min={1} max={365} value={lookback} onChange={(e) => setLookback(e.target.value)} />
          {base === "position" && (
            <div className="grid gap-3 sm:grid-cols-2">
              <Input label="First visit's share, %" hint="40 when empty." type="number" min={0} max={100} value={first} onChange={(e) => setFirst(e.target.value)} />
              <Input label="Last visit's share, %" hint="40 when empty. The visits between share the rest." type="number" min={0} max={100} value={last} onChange={(e) => setLast(e.target.value)} />
            </div>
          )}
          {base === "time_decay" && <Input label="Half-life, days" hint="A visit's share halves for every this many days before the conversion. 7 when empty." type="number" min={0.5} max={365} step={0.5} value={halfLife} onChange={(e) => setHalfLife(e.target.value)} />}
          {channels.length > 0 && (
            <div className="space-y-1.5 text-[13px]">
              <div className="font-medium">Per channel</div>
              <p className="text-[12px] text-muted-foreground">A shorter or longer window for one channel, such as 7 days for paid ads, or leave a channel out so it never gets credit. A conversion with no visit left goes to direct.</p>
              <div className="divide-y rounded-md border">
                {channels.map((c) => (
                  <div key={c.id} className="flex flex-wrap items-center justify-between gap-2 px-3 py-1.5">
                    <span>{c.label}</span>
                    <div className="flex items-center gap-3">
                      <input
                        aria-label={`${c.label} window in days`}
                        className={`${field} w-24`}
                        type="number"
                        min={1}
                        max={365}
                        placeholder="Days"
                        disabled={excluded.includes(c.id)}
                        value={windows[c.id] ?? ""}
                        onChange={(e) => setWindows((w) => ({ ...w, [c.id]: e.target.value }))}
                      />
                      <label className="flex items-center gap-1.5 text-[12px]">
                        <input type="checkbox" checked={excluded.includes(c.id)} onChange={() => toggleExcluded(c.id)} /> Leave out
                      </label>
                    </div>
                  </div>
                ))}
              </div>
            </div>
          )}
          <div className="flex gap-2 pt-1">
            <Button type="submit" disabled={busy || !name.trim() || (!model && !key.trim())}>{model ? "Save" : "Make model"}</Button>
            <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

const ops: { id: Filter["op"]; label: string }[] = [
  { id: "eq", label: "is" },
  { id: "neq", label: "is not" },
  { id: "gt", label: "is more than" },
  { id: "gte", label: "is at least" },
  { id: "lt", label: "is less than" },
  { id: "lte", label: "is at most" },
  { id: "in", label: "is one of" },
  { id: "contains", label: "contains" },
  { id: "exists", label: "is set" },
  { id: "not_exists", label: "is not set" },
];
const noValue = (op: Filter["op"]) => op === "exists" || op === "not_exists";

function filterText(f: Filter) {
  const label = ops.find((o) => o.id === f.op)?.label ?? f.op;
  if (noValue(f.op)) return `${f.property} ${label}`;
  return `${f.property} ${label} ${Array.isArray(f.value) ? f.value.join(", ") : String(f.value)}`;
}

function worth(d: ConversionDefinition) {
  const fixed = d.fixed_value_minor;
  if (d.value_mode === "fixed" && fixed !== null && fixed !== undefined) {
    try {
      return new Intl.NumberFormat(undefined, { style: "currency", currency: d.currency }).format(fixed / 100);
    } catch {
      return `${(fixed / 100).toFixed(2)} ${d.currency}`;
    }
  }
  if (d.value_mode === "property") return `Value from ${d.value_property}${d.value_unit === "minor" ? " (cents)" : ""}`;
  return "No value";
}

export function ConversionDefinitions({ ws, canManage, onChanged }: { ws: string; canManage: boolean; onChanged: () => void }) {
  const [list, setList] = useState<ConversionDefinition[] | null>(null);
  const [presets, setPresets] = useState<ConversionPreset[]>([]);
  const [networks, setNetworks] = useState<{ id: string; label: string }[]>([]);
  const [editing, setEditing] = useState<ConversionDefinition | "new" | null>(null);

  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/conversion-definitions", { params: { path: { ws } } });
    if (error) toast(errorMessage(error), "error");
    setList(data?.data ?? []);
  }, [ws]);
  useEffect(() => {
    load();
    api.GET("/v1/workspaces/{ws}/conversion-definitions/presets", { params: { path: { ws } } }).then(({ data }) => setPresets(data?.data ?? []));
    api.GET("/v1/workspaces/{ws}/ads/tracking-templates", { params: { path: { ws } } }).then(({ data }) =>
      setNetworks((data?.data ?? []).map((n: AdTrackingTemplate) => ({ id: n.network, label: n.label }))),
    );
  }, [ws, load]);

  async function addPreset(p: ConversionPreset) {
    const { data, error } = await api.POST("/v1/workspaces/{ws}/conversion-definitions/presets/{preset}", { params: { path: { ws, preset: p.id } } });
    if (error || !data) return toast(errorMessage(error), "error");
    const added = "added" in data ? data.added.length : 0;
    toast(added > 0 ? `Added ${added} from ${p.name}. ${recredit}` : `You already have every conversion in ${p.name}.`);
    load();
    onChanged();
  }

  async function archive(d: ConversionDefinition) {
    if (!(await confirmAction(`Archive ${d.name}?`, `${d.event} events stop counting as this conversion, and conversions are credited again without it.`, "Archive conversion"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/conversion-definitions/{key}", { params: { path: { ws, key: d.key } } });
    if (error) return toast(errorMessage(error), "error");
    toast(`${d.name} is archived.`);
    load();
    onChanged();
  }

  const networkName = (id: string) => networks.find((n) => n.id === id)?.label ?? id;

  return (
    <Card
      title="Conversions"
      description="What counts as a conversion: a customer event from any source (a sign-up, a payment, a held meeting, a long call), only when its properties match, with what it's worth and each ad network's own name for it. Attribution credits channels with every conversion that has attribution on."
      action={
        canManage ? (
          <div className="flex gap-1">
            {presets.length > 0 && <Menu label="Add a preset" aria="Add a preset's conversions" items={presets.map((p) => ({ label: p.name, onClick: () => addPreset(p) }))} />}
            <Button variant="ghost" onClick={() => setEditing("new")}>New conversion</Button>
          </div>
        ) : undefined
      }
    >
      {list === null ? null : list.length === 0 ? (
        <div className="space-y-2 text-[13px] text-muted-foreground">
          <p>No conversions defined yet: attribution counts the events in your attribution settings. Start from a preset for your kind of business, or define one yourself.</p>
          {presets.length > 0 && (
            <ul className="list-disc space-y-0.5 pl-5">
              {presets.map((p) => <li key={p.id}><span className="font-medium text-foreground">{p.name}</span>: {p.description}</li>)}
            </ul>
          )}
        </div>
      ) : (
        <DataTable
          data={list}
          rowId={(d) => d.key}
          searchPlaceholder="Search conversions"
          columns={[
            {
              id: "name",
              header: "Conversion",
              accessorFn: (d) => d.name,
              cell: ({ row: { original: d } }) => (
                <div className="space-y-0.5">
                  <div className="font-medium">
                    {d.name} {!d.attribution && <Badge>Not credited</Badge>}
                  </div>
                  <div className="text-[12px] text-muted-foreground">
                    <span className="font-mono">{d.event}</span>
                    {(d.filters ?? []).length > 0 && ` where ${(d.filters ?? []).map(filterText).join(" and ")}`}
                  </div>
                </div>
              ),
            },
            { id: "value", header: "Worth", enableSorting: false, cell: ({ row: { original: d } }) => <span className="text-[13px]">{worth(d)}</span> },
            {
              id: "networks",
              header: "Ad networks call it",
              enableSorting: false,
              cell: ({ row: { original: d } }) => (
                <span className="text-[12px] text-muted-foreground">
                  {Object.entries(d.network_events ?? {}).map(([n, e]) => `${networkName(n)}: ${e}`).join(" · ") || "—"}
                </span>
              ),
            },
            {
              id: "actions",
              header: "",
              enableSorting: false,
              cell: ({ row: { original: d } }) =>
                canManage && (
                  <div className="flex justify-end">
                    <Menu items={[{ label: "Edit", onClick: () => setEditing(d) }, { label: "Archive", onClick: () => archive(d), danger: true }]} />
                  </div>
                ),
            },
          ]}
        />
      )}
      {editing && (
        <DefinitionDialog
          ws={ws}
          definition={editing === "new" ? null : editing}
          networks={networks}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            load();
            onChanged();
          }}
        />
      )}
    </Card>
  );
}

type FilterRow = { property: string; op: Filter["op"]; value: string };

// filterValue turns what was typed into the value the op takes: a number
// for comparisons, a list for "is one of", nothing for set and not set, and
// a number for is and is not when it reads as one.
function filterValue(f: FilterRow): Filter["value"] {
  const v = f.value.trim();
  if (noValue(f.op)) return undefined;
  if (f.op === "in") return v.split(",").map((x) => x.trim()).filter(Boolean);
  if (["gt", "gte", "lt", "lte"].includes(f.op)) return Number(v);
  if ((f.op === "eq" || f.op === "neq") && v !== "" && !Number.isNaN(Number(v))) return Number(v);
  return v;
}

function DefinitionDialog({ ws, definition, networks, onClose, onSaved }: { ws: string; definition: ConversionDefinition | null; networks: { id: string; label: string }[]; onClose: () => void; onSaved: () => void }) {
  const d = definition;
  const [key, setKey] = useState("");
  const [name, setName] = useState(d?.name ?? "");
  const [event, setEvent] = useState(d?.event ?? "");
  const [filters, setFilters] = useState<FilterRow[]>(
    (d?.filters ?? []).map((f) => ({ property: f.property, op: f.op, value: f.value === undefined || f.value === null ? "" : Array.isArray(f.value) ? f.value.join(", ") : String(f.value) })),
  );
  const [mode, setMode] = useState<ValueMode>(d?.value_mode ?? "none");
  const [amount, setAmount] = useState(d?.fixed_value_minor !== null && d?.fixed_value_minor !== undefined ? String(d.fixed_value_minor / 100) : "");
  const [valueProperty, setValueProperty] = useState(d?.value_property ?? "");
  const [unit, setUnit] = useState<"major" | "minor">(d?.value_unit ?? "major");
  const [currency, setCurrency] = useState(d?.currency ?? "");
  const [attribution, setAttribution] = useState(d?.attribution ?? true);
  const [netEvents, setNetEvents] = useState<{ network: string; event: string }[]>(Object.entries(d?.network_events ?? {}).map(([network, event]) => ({ network, event })));
  const [busy, setBusy] = useState(false);

  async function save(e: FormEvent) {
    e.preventDefault();
    const body: DefinitionInput = {
      name: name.trim(),
      event: event.trim(),
      filters: filters.filter((f) => f.property.trim()).map((f) => {
        const out: Filter = { property: f.property.trim(), op: f.op };
        const v = filterValue(f);
        if (v !== undefined) out.value = v;
        return out;
      }),
      value_mode: mode,
      currency: currency.trim().toUpperCase(),
      attribution,
      network_events: Object.fromEntries(netEvents.filter((n) => n.network && n.event.trim()).map((n) => [n.network, n.event.trim()])),
    };
    if (mode === "fixed") body.fixed_value_minor = Math.round(Number(amount) * 100);
    if (mode === "property") {
      body.value_property = valueProperty.trim();
      body.value_unit = unit;
    }
    setBusy(true);
    const { error } = d
      ? await api.PATCH("/v1/workspaces/{ws}/conversion-definitions/{key}", { params: { path: { ws, key: d.key } }, body })
      : await api.POST("/v1/workspaces/{ws}/conversion-definitions", { params: { path: { ws } }, body: { ...body, key: key.trim() } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast(`${body.name} is saved. ${recredit}`);
    onSaved();
  }

  const setFilter = (i: number, patch: Partial<FilterRow>) => setFilters((fs) => fs.map((f, j) => (j === i ? { ...f, ...patch } : f)));
  const setNet = (i: number, patch: Partial<{ network: string; event: string }>) => setNetEvents((ns) => ns.map((n, j) => (j === i ? { ...n, ...patch } : n)));
  const unused = networks.filter((n) => !netEvents.some((x) => x.network === n.id));

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{d ? `Edit ${d.name}` : "New conversion"}</DialogTitle>
          <DialogDescription>A customer event that counts as a conversion, from any source: your site's track events, payments, meetings, calls or your own app.</DialogDescription>
        </DialogHeader>
        <form className="space-y-3" onSubmit={save}>
          <div className="grid gap-3 sm:grid-cols-2">
            {!d && <Input label="Key" hint="Lowercase letters, digits, _ and -, such as held_call. It never changes." required pattern="[a-z][a-z0-9_\-]{0,39}" value={key} onChange={(e) => setKey(e.target.value)} />}
            <Input label="Name" required maxLength={80} placeholder="Held call" value={name} onChange={(e) => setName(e.target.value)} />
          </div>
          <Input label="Customer event" hint="Its name in the customer events stream, such as signed_up, payment.succeeded, meeting.held or call.completed." required placeholder="meeting.held" value={event} onChange={(e) => setEvent(e.target.value)} />

          <div className="space-y-1.5 text-[13px]">
            <div className="font-medium">Only when</div>
            {filters.length === 0 && <p className="text-[12px] text-muted-foreground">Every such event counts. Add a condition to count only some, such as a call over a minute.</p>}
            {filters.map((f, i) => (
              <div key={i} className="flex flex-wrap items-center gap-2">
                <input aria-label="Property" className={`${field} w-36`} placeholder="duration_s" value={f.property} onChange={(e) => setFilter(i, { property: e.target.value })} />
                <select aria-label="Condition" className={field} value={f.op} onChange={(e) => setFilter(i, { op: e.target.value as Filter["op"] })}>
                  {ops.map((o) => <option key={o.id} value={o.id}>{o.label}</option>)}
                </select>
                {!noValue(f.op) && <input aria-label="Value" className={`${field} w-36`} placeholder={f.op === "in" ? "pro, team" : "60"} value={f.value} onChange={(e) => setFilter(i, { value: e.target.value })} />}
                <Button type="button" variant="ghost" onClick={() => setFilters((fs) => fs.filter((_, j) => j !== i))}>Remove</Button>
              </div>
            ))}
            {filters.length < 10 && <Button type="button" variant="ghost" onClick={() => setFilters((fs) => [...fs, { property: "", op: "eq", value: "" }])}>Add a condition</Button>}
          </div>

          <div className="grid gap-3 sm:grid-cols-2">
            <label className="flex flex-col gap-1 text-[13px]">
              <span className="font-medium">Worth</span>
              <select className={field} value={mode} onChange={(e) => setMode(e.target.value as ValueMode)}>
                <option value="none">Nothing: count it only</option>
                <option value="fixed">The same amount each time</option>
                <option value="property">An amount on the event</option>
              </select>
            </label>
            {mode === "fixed" && <Input label="Amount" type="number" min={0} step="0.01" required placeholder="50.00" value={amount} onChange={(e) => setAmount(e.target.value)} />}
            {mode === "property" && <Input label="Amount property" placeholder="revenue" required value={valueProperty} onChange={(e) => setValueProperty(e.target.value)} />}
          </div>
          {mode !== "none" && (
            <div className="grid gap-3 sm:grid-cols-2">
              {mode === "property" && (
                <label className="flex flex-col gap-1 text-[13px]">
                  <span className="font-medium">The amount is in</span>
                  <select className={field} value={unit} onChange={(e) => setUnit(e.target.value as "major" | "minor")}>
                    <option value="major">Dollars, euros… (12.50)</option>
                    <option value="minor">Cents (1250)</option>
                  </select>
                </label>
              )}
              <Input
                label="Currency"
                hint={mode === "fixed" ? "Three letters, such as USD." : "Empty uses the event's own currency property."}
                required={mode === "fixed"}
                maxLength={3}
                placeholder="USD"
                value={currency}
                onChange={(e) => setCurrency(e.target.value)}
              />
            </div>
          )}

          <div className="space-y-1.5 text-[13px]">
            <div className="font-medium">Each ad network's name for it</div>
            <p className="text-[12px] text-muted-foreground">Used when the conversion is sent back to a network, such as Schedule for Meta or CompletePayment for TikTok.</p>
            {netEvents.map((n, i) => (
              <div key={i} className="flex flex-wrap items-center gap-2">
                <select aria-label="Ad network" className={field} value={n.network} onChange={(e) => setNet(i, { network: e.target.value })}>
                  {networks.filter((x) => x.id === n.network || !netEvents.some((y) => y.network === x.id)).map((x) => <option key={x.id} value={x.id}>{x.label}</option>)}
                </select>
                <input aria-label="Event name" className={`${field} w-44`} placeholder="Purchase" value={n.event} onChange={(e) => setNet(i, { event: e.target.value })} />
                <Button type="button" variant="ghost" onClick={() => setNetEvents((ns) => ns.filter((_, j) => j !== i))}>Remove</Button>
              </div>
            ))}
            {unused.length > 0 && <Button type="button" variant="ghost" onClick={() => setNetEvents((ns) => [...ns, { network: unused[0].id, event: "" }])}>Add a network</Button>}
          </div>

          <label className="flex items-start gap-2 text-[13px]">
            <input type="checkbox" className="mt-0.5" checked={attribution} onChange={(e) => setAttribution(e.target.checked)} />
            <span>
              <span className="font-medium">Credit channels with it</span>
              <span className="block text-[12px] text-muted-foreground">Off keeps it out of attribution reports, for a conversion you only send to ad networks.</span>
            </span>
          </label>

          <div className="flex gap-2 pt-1">
            <Button type="submit" disabled={busy || !name.trim() || !event.trim() || (!d && !key.trim())}>{d ? "Save" : "Add conversion"}</Button>
            <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
