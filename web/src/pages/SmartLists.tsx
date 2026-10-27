import { useCallback, useEffect, useState, type FormEvent } from "react";
import type { LeadList, SmartList, SmartListPerson, SmartListRule } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { BrandBadge } from "@/components/brand-badge";
import { DataTable } from "@/components/data-table";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, confirmAction, ErrorText, field, Loading, Menu, PageHeader, Stat, Stats, toast } from "../ui";
import { ago, plural } from "../format";

// Smart lists: people on customer records who match rules now. They sit
// beside lead lists on the Lists page; this file has their editor and
// detail page.

type Field = { key: string; label: string };

// useRuleChoices loads what rules can pick from: the person fields and
// approved events in the catalog, and the workspace's lead lists.
function useRuleChoices(ws: string) {
  const [fields, setFields] = useState<Field[]>([]);
  const [events, setEvents] = useState<string[]>([]);
  const [leadLists, setLeadLists] = useState<LeadList[]>([]);
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/catalog", { params: { path: { ws } } }).then(({ data }) => {
      const person = data?.types.find((t) => t.key === "person");
      setFields((person?.fields ?? []).filter((f) => !f.archived).map((f) => ({ key: f.key, label: f.label })));
      setEvents((data?.events ?? []).filter((e) => e.status === "approved").map((e) => e.name));
    });
    api.GET("/v1/workspaces/{ws}/lead-lists", { params: { path: { ws } } }).then(({ data }) => setLeadLists(data?.data ?? []));
  }, [ws]);
  return { fields, events, leadLists };
}

const opText: Record<SmartListRule["op"], string> = { eq: "is", neq: "is not", gt: "is more than", lt: "is less than", contains: "contains", exists: "is set", not_exists: "is not set" };

type Test = "attribute" | "event" | "in_list";
const testOf = (r: SmartListRule): Test => (r.event !== undefined ? "event" : r.in_list !== undefined ? "in_list" : "attribute");

function parseValue(v: string): unknown {
  if (v === "true" || v === "false") return v === "true";
  if (v.trim() !== "" && !Number.isNaN(Number(v))) return Number(v);
  return v;
}

// ruleText says a rule in words: "plan is pro", "did payment_succeeded in
// the last 90 days", "is in Webinar".
export function ruleText(r: SmartListRule, fieldLabel: (k: string) => string, listName: (id: string) => string) {
  switch (testOf(r)) {
    case "event": {
      const when = r.within_days ? ` in the last ${plural(r.within_days, "day")}` : "";
      return `${r.op === "exists" ? "did" : "didn't do"} ${r.event}${when}`;
    }
    case "in_list":
      return `${r.op === "exists" ? "is" : "isn't"} in ${listName(r.in_list ?? "")}`;
  }
  const value = r.op === "exists" || r.op === "not_exists" ? "" : ` ${String(r.value)}`;
  return `${fieldLabel(r.attribute ?? "")} ${opText[r.op]}${value}`;
}

export function rulesText(l: Pick<SmartList, "rules" | "match">, fieldLabel: (k: string) => string, listName: (id: string) => string) {
  if (l.rules.length === 0) return "Everyone";
  return l.rules.map((r) => ruleText(r, fieldLabel, listName)).join(l.match === "any" ? ", or " : ", and ");
}

function Rules({ rules, onChange, fields, events, leadLists }: { rules: SmartListRule[]; onChange: (r: SmartListRule[]) => void; fields: Field[]; events: string[]; leadLists: LeadList[] }) {
  const set = (i: number, r: SmartListRule) => onChange(rules.map((x, k) => (k === i ? r : x)));
  return (
    <div className="space-y-2">
      {rules.map((r, i) => {
        const t = testOf(r);
        return (
          <div key={i} className="flex flex-wrap items-center gap-2 rounded-md border p-2">
            <select aria-label="Test" className={field} value={t} onChange={(e) => {
              const v = e.target.value as Test;
              set(i, v === "event" ? { event: "", op: "exists" } : v === "in_list" ? { in_list: leadLists[0]?.id ?? "", op: "exists" } : { attribute: "", op: "eq", value: "" });
            }}>
              <option value="attribute">Their field</option>
              <option value="event">They did an event</option>
              {leadLists.length > 0 && <option value="in_list">They're in a lead list</option>}
            </select>
            {t === "attribute" && (
              <>
                {fields.length > 0 ? (
                  <select aria-label="Field" className={field} value={r.attribute ?? ""} onChange={(e) => set(i, { ...r, attribute: e.target.value })} required>
                    <option value="" disabled>Pick a field</option>
                    {r.attribute && !fields.some((f) => f.key === r.attribute) && <option value={r.attribute}>{r.attribute}</option>}
                    {fields.map((f) => <option key={f.key} value={f.key}>{f.label}</option>)}
                  </select>
                ) : (
                  <input aria-label="Field" className={field} placeholder="billing_plan" value={r.attribute ?? ""} onChange={(e) => set(i, { ...r, attribute: e.target.value.trim() })} required />
                )}
                <select aria-label="Op" className={field} value={r.op} onChange={(e) => set(i, { ...r, op: e.target.value as SmartListRule["op"] })}>
                  {(Object.keys(opText) as SmartListRule["op"][]).map((o) => <option key={o} value={o}>{opText[o]}</option>)}
                </select>
                {r.op !== "exists" && r.op !== "not_exists" && (
                  <input aria-label="Value" className={field} placeholder="pro" value={r.value === undefined ? "" : String(r.value)} onChange={(e) => set(i, { ...r, value: parseValue(e.target.value) })} required />
                )}
              </>
            )}
            {t === "event" && (
              <>
                <select aria-label="Did" className={field} value={r.op} onChange={(e) => set(i, { ...r, op: e.target.value as SmartListRule["op"] })}>
                  <option value="exists">did</option>
                  <option value="not_exists">didn't do</option>
                </select>
                <input aria-label="Event" className={field} placeholder="payment_succeeded" list="smart-events" value={r.event ?? ""} onChange={(e) => set(i, { ...r, event: e.target.value.trim() })} required pattern="[a-z][a-z0-9_]*(\.[a-z0-9_]+)*" />
                <select aria-label="When" className={field} value={r.within_days ?? 0} onChange={(e) => set(i, { ...r, within_days: Number(e.target.value) || undefined })}>
                  <option value={0}>ever</option>
                  <option value={7}>in the last 7 days</option>
                  <option value={30}>in the last 30 days</option>
                  <option value={90}>in the last 90 days</option>
                  <option value={365}>in the last year</option>
                </select>
              </>
            )}
            {t === "in_list" && (
              <>
                <select aria-label="Is" className={field} value={r.op} onChange={(e) => set(i, { ...r, op: e.target.value as SmartListRule["op"] })}>
                  <option value="exists">is in</option>
                  <option value="not_exists">isn't in</option>
                </select>
                <select aria-label="Lead list" className={field} value={r.in_list ?? ""} onChange={(e) => set(i, { ...r, in_list: e.target.value })}>
                  {leadLists.map((l) => <option key={l.id} value={l.id}>{l.name}</option>)}
                </select>
              </>
            )}
            <Button type="button" variant="ghost" className="ml-auto" onClick={() => onChange(rules.filter((_, k) => k !== i))}>Remove</Button>
          </div>
        );
      })}
      <datalist id="smart-events">{events.map((e) => <option key={e} value={e} />)}</datalist>
      {rules.length < 20 && <Button type="button" variant="ghost" onClick={() => onChange([...rules, { attribute: "", op: "eq", value: "" }])}>Add a rule</Button>}
    </div>
  );
}

// ready reports whether every rule is filled in enough to count.
const ready = (rules: SmartListRule[]) =>
  rules.every((r) => (testOf(r) === "event" ? !!r.event : testOf(r) === "in_list" ? !!r.in_list : !!r.attribute && (r.op === "exists" || r.op === "not_exists" || (r.value !== undefined && r.value !== ""))));

export function SmartListSheet({ ws, list, onClose, onSaved }: { ws: string; list: SmartList | null; onClose: () => void; onSaved: (l: SmartList) => void }) {
  const { brand, active } = useBrand(ws);
  const { fields, events, leadLists } = useRuleChoices(ws);
  const [name, setName] = useState(list?.name ?? "");
  const [description, setDescription] = useState(list?.description ?? "");
  const [brandID, setBrandID] = useState<string>(list ? list.brand_id ?? "" : brand?.id ?? "");
  const [match, setMatch] = useState<SmartList["match"]>(list?.match ?? "all");
  const [rules, setRules] = useState<SmartListRule[]>(list?.rules ?? [{ attribute: "", op: "eq", value: "" }]);
  const [count, setCount] = useState<{ members: number; sample: SmartListPerson[] } | null>(null);
  const [counting, setCounting] = useState(false);
  const [countError, setCountError] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  // Count as the rules change, a moment after the last edit.
  useEffect(() => {
    if (!ready(rules)) return;
    setCounting(true);
    const t = setTimeout(async () => {
      const { data, error } = await api.POST("/v1/workspaces/{ws}/smart-lists/preview", { params: { path: { ws } }, body: { rules, match, brand_id: brandID || null } });
      setCounting(false);
      setCountError(error ? errorMessage(error) : "");
      if (data) setCount(data);
    }, 400);
    return () => clearTimeout(t);
  }, [ws, rules, match, brandID]);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const body = { name, description, brand_id: brandID || null, rules, match };
    const res = list
      ? await api.PATCH("/v1/workspaces/{ws}/smart-lists/{id}", { params: { path: { ws, id: list.id } }, body })
      : await api.POST("/v1/workspaces/{ws}/smart-lists", { params: { path: { ws } }, body });
    setBusy(false);
    if (res.error) return setError(errorMessage(res.error));
    if (res.data) {
      toast(list ? "Saved. Updating who's in it." : `Created ${res.data.name}. Filling it now.`);
      onSaved(res.data);
    }
  }

  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>{list ? `Edit ${list.name}` : "New smart list"}</SheetTitle>
          <SheetDescription>People on your customer records who match these rules. It keeps itself up to date: people join when they start matching and leave when they stop.</SheetDescription>
        </SheetHeader>
        <form onSubmit={submit} className="space-y-6 px-4 pb-6 text-[13px]">
          <div className="space-y-3">
            <label className="block">
              <span className="mb-1 block font-medium">Name</span>
              <input className={`${field} w-full`} required maxLength={200} placeholder="e.g. Paying customers" value={name} onChange={(e) => setName(e.target.value)} />
            </label>
            <label className="block">
              <span className="mb-1 block font-medium">Description <span className="font-normal text-muted-foreground">(optional)</span></span>
              <input className={`${field} w-full`} maxLength={2000} value={description} onChange={(e) => setDescription(e.target.value)} />
            </label>
            {active.length > 0 && (
              <label className="block">
                <span className="mb-1 block font-medium">Brand</span>
                <select className={`${field} w-full`} value={brandID} onChange={(e) => setBrandID(e.target.value)}>
                  <option value="">Every brand</option>
                  {active.map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}
                </select>
                <span className="mt-1 block text-[12px] text-muted-foreground">{brandID ? "This brand's people, and people shared by every brand." : "People of every brand."}</span>
              </label>
            )}
          </div>

          <div className="space-y-2">
            <div className="flex flex-wrap items-center gap-2 font-medium">
              People who match
              <select aria-label="Match" className={field} value={match} onChange={(e) => setMatch(e.target.value as SmartList["match"])}>
                <option value="all">all</option>
                <option value="any">any</option>
              </select>
              of these rules
            </div>
            <Rules rules={rules} onChange={setRules} fields={fields} events={events} leadLists={leadLists} />
          </div>

          <div aria-live="polite" className="rounded-md border bg-muted/40 p-3">
            {!ready(rules) ? (
              <p className="text-muted-foreground">Finish the rules to see who matches.</p>
            ) : countError ? (
              <ErrorText>{countError}</ErrorText>
            ) : count ? (
              <div className={counting ? "opacity-60" : ""}>
                <p><span className="font-mono text-base font-medium tabular-nums">{count.members.toLocaleString()}</span> {count.members === 1 ? "person matches" : "people match"} now</p>
                {count.sample.length > 0 && <p className="mt-1 truncate text-[12px] text-muted-foreground">{count.sample.map((p) => p.name || p.email).join(", ")}{count.members > count.sample.length ? ", …" : ""}</p>}
              </div>
            ) : (
              <p className="text-muted-foreground">Counting…</p>
            )}
          </div>

          <ErrorText>{error}</ErrorText>
          <div className="flex flex-wrap gap-2 border-t pt-4">
            <Button disabled={busy || !name.trim() || !ready(rules)}>{busy ? "Saving…" : list ? "Save" : "Create smart list"}</Button>
            <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
          </div>
        </form>
      </SheetContent>
    </Sheet>
  );
}

export function SmartListDetail({ ws, list: initial, canManage, onBack, onEdit, onChanged }: { ws: string; list: SmartList; canManage: boolean; onBack: () => void; onEdit: (l: SmartList) => void; onChanged: () => void }) {
  const { fields, leadLists } = useRuleChoices(ws);
  const [list, setList] = useState(initial);
  const [people, setPeople] = useState<SmartListPerson[] | null>(null);
  const [next, setNext] = useState<string | null>(null);
  const [error, setError] = useState("");
  const [waiting, setWaiting] = useState(!initial.refreshed_at);

  const load = useCallback(async () => {
    const [l, m] = await Promise.all([
      api.GET("/v1/workspaces/{ws}/smart-lists/{id}", { params: { path: { ws, id: initial.id } } }),
      api.GET("/v1/workspaces/{ws}/smart-lists/{id}/members", { params: { path: { ws, id: initial.id }, query: { limit: 100 } } }),
    ]);
    if (l.data) setList(l.data);
    if (m.data) {
      setPeople(m.data.data);
      setNext(m.data.next_cursor ?? null);
    } else setError(errorMessage(m.error ?? l.error));
    return l.data;
  }, [ws, initial.id]);

  useEffect(() => {
    load();
  }, [load]);

  // A new or just-changed list fills in a few seconds: check until it has.
  useEffect(() => {
    if (!waiting) return;
    const since = list.refreshed_at;
    const t = setInterval(async () => {
      const l = await load();
      if (l && l.refreshed_at !== since) setWaiting(false);
    }, 2000);
    const stop = setTimeout(() => setWaiting(false), 60000);
    return () => { clearInterval(t); clearTimeout(stop); };
  }, [waiting, load]); // eslint-disable-line react-hooks/exhaustive-deps

  async function more() {
    if (!next) return;
    const { data } = await api.GET("/v1/workspaces/{ws}/smart-lists/{id}/members", { params: { path: { ws, id: list.id }, query: { limit: 100, cursor: next } } });
    if (data) {
      setPeople((p) => [...(p ?? []), ...data.data]);
      setNext(data.next_cursor ?? null);
    }
  }

  async function refresh() {
    const { error } = await api.POST("/v1/workspaces/{ws}/smart-lists/{id}/refresh", { params: { path: { ws, id: list.id } } });
    if (error) return toast(errorMessage(error));
    toast("Updating who's in it.");
    setWaiting(true);
  }

  async function remove() {
    if (!(await confirmAction(`Delete ${list.name}?`, "The list goes; the people in it stay on your customer records.", "Delete list"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/smart-lists/{id}", { params: { path: { ws, id: list.id } } });
    if (error) return toast(errorMessage(error));
    toast(`Deleted ${list.name}.`);
    onChanged();
    onBack();
  }

  const fieldLabel = (k: string) => fields.find((f) => f.key === k)?.label ?? k;
  const listName = (id: string) => leadLists.find((l) => l.id === id)?.name ?? "a lead list";

  return (
    <div className="space-y-6">
      <PageHeader
        back={{ label: "Lists", onClick: onBack }}
        title={<>{list.name} <Badge tone="blue">Smart list</Badge> <BrandBadge id={list.brand_id} /></>}
        description={list.description || "Keeps itself up to date from its rules."}
        action={canManage && (
          <>
            <Button variant="ghost" onClick={refresh} disabled={waiting}>{waiting ? "Updating…" : "Refresh now"}</Button>
            <Button onClick={() => onEdit(list)}>Edit rules</Button>
            <Menu items={[{ label: "Delete list", danger: true, onClick: remove }]} />
          </>
        )}
      />
      <Stats cols={3}>
        <Stat label="People" value={list.refreshed_at ? list.members.toLocaleString() : "…"} />
        <Stat label="Updated" value={list.refreshed_at ? ago(list.refreshed_at) : "filling"} sub="every 15 minutes" />
        <Stat label="Rules" value={list.rules.length} sub={list.match === "any" ? "any must match" : "all must match"} />
      </Stats>
      {list.error_code && (
        <p role="alert" className="rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-[13px] text-amber-800 dark:text-amber-200">
          {list.error_code === "query_timeout" ? "The last update took too long and stopped. Narrow the rules, for example with a time window on an event." : "The last update failed. It tries again in 15 minutes."}
        </p>
      )}
      <Card title="Who's in it" description={rulesText(list, fieldLabel, listName)}>
        <ErrorText>{error}</ErrorText>
        {people === null && !error && <Loading />}
        {people !== null && (
          <DataTable
            data={people}
            rowId={(p) => p.id}
            searchPlaceholder="Search people"
            empty={waiting ? "Filling the list…" : "Nobody matches these rules right now."}
            mobile={(p) => (
              <div className="space-y-0.5">
                <div className="font-medium">{p.name || p.email || "Unnamed person"}</div>
                <div className="text-[12px] text-muted-foreground">{p.email} · joined {ago(p.entered_at)}</div>
              </div>
            )}
            columns={[
              { id: "name", header: "Name", accessorFn: (p) => p.name, cell: ({ row: { original: p } }) => <span className="font-medium">{p.name || <span className="text-muted-foreground">Unnamed</span>}</span> },
              { id: "email", header: "Email", accessorFn: (p) => p.email },
              { id: "joined", header: "Joined", accessorFn: (p) => p.entered_at ?? "", cell: ({ row: { original: p } }) => <span className="text-muted-foreground">{ago(p.entered_at)}</span> },
            ]}
          />
        )}
        {next && <div className="mt-3"><Button variant="ghost" onClick={more}>Show more</Button></div>}
      </Card>
    </div>
  );
}
