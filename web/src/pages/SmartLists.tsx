import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import type { Schemas, SmartList, SmartListPerson, SmartListRule } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { BrandBadge } from "@/components/brand-badge";
import { DataTable } from "@/components/data-table";
import { Textarea } from "@/components/ui/textarea";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, confirmAction, ErrorText, field, Input, Loading, Menu, PageHeader, Stat, Stats, toast } from "../ui";
import { ago } from "../format";
import { Sparkles } from "lucide-react";
import type { Tab } from "@/lib/paths";
import { AppLink } from "./PartnerPage";
import { BulkEditSheet } from "./BulkEdit";
import { Builder, conditions, negative, newRule, plainText, ready, useNames, useRuleChoices } from "./SmartListRules";

// Smart lists: people on customer records who match rules now. They sit
// beside lead lists on the Lists page; this file has their editor and
// detail page.

export { Rules, ruleText, rulesText, useRuleChoices } from "./SmartListRules";

export function SmartListSheet({ ws, list, onClose, onSaved }: { ws: string; list: SmartList | null; onClose: () => void; onSaved: (l: SmartList) => void }) {
  const { brand, byId } = useBrand(ws);
  // A list belongs to one brand: its own, or the one picked for a new one.
  const brandID = (list ? list.brand_id : brand?.id) ?? "";
  const choices = useRuleChoices(ws, brandID);
  const names = useNames(choices);
  const recordType = list?.record_type ?? "person";
  const [name, setName] = useState(list?.name ?? "");
  const [description, setDescription] = useState(list?.description ?? "");
  const [describing, setDescribing] = useState(!!list?.description);
  const [match, setMatch] = useState<SmartList["match"]>(list?.match ?? "all");
  const [rules, setRules] = useState<SmartListRule[]>(list?.rules ?? [newRule()]);
  // A list fills from its rules, or from a saved query's record_id column.
  const [from, setFrom] = useState<"rules" | "query">(list?.kind === "query" ? "query" : "rules");
  const [queryID, setQueryID] = useState(list?.saved_query_id ?? "");
  const [queries, setQueries] = useState<{ id: string; name: string }[] | null>(null);
  useEffect(() => {
    if (from !== "query" || !brandID || queries) return;
    api.GET("/v1/brands/{brand}/queries", { params: { path: { brand: brandID } } }).then(({ data }) => setQueries(data?.data ?? []));
  }, [from, brandID, queries]);
  const [count, setCount] = useState<{ members: number; sample: SmartListPerson[] } | null>(null);
  const [counting, setCounting] = useState(false);
  const [countError, setCountError] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  // Count as the rules change, a moment after the last edit.
  useEffect(() => {
    if (from !== "rules" || !ready(rules) || !brandID) return;
    setCounting(true);
    const t = setTimeout(async () => {
      const { data, error } = await api.POST("/v1/brands/{brand}/smart-lists/preview", { params: { path: { brand: brandID } }, body: { rules, match } });
      setCounting(false);
      setCountError(error ? errorMessage(error) : "");
      if (data) setCount(data);
    }, 400);
    return () => clearTimeout(t);
  }, [rules, match, brandID, from]);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const body = from === "query" ? { name, description, saved_query_id: queryID, ...(list ? {} : { kind: "query" as const }) } : { name, description, rules, match };
    const res = list
      ? await api.PATCH("/v1/brands/{brand}/smart-lists/{id}", { params: { path: { brand: brandID, id: list.id } }, body })
      : await api.POST("/v1/brands/{brand}/smart-lists", { params: { path: { brand: brandID } }, body });
    setBusy(false);
    if (res.error) return setError(errorMessage(res.error));
    if (res.data) {
      toast(list ? "Saved. Updating who's in it." : `Created ${res.data.name}. Filling it now.`);
      onSaved(res.data);
    }
  }

  const who = recordType === "account" ? "Companies that" : "People who";
  const tab = (on: boolean) => `rounded-md px-3 py-1 text-[13px] ${on ? "bg-background font-semibold shadow-xs" : "text-muted-foreground hover:text-foreground"}`;
  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-5xl">
        <SheetHeader>
          <SheetTitle>{list ? `Edit ${list.name}` : "New smart list"}</SheetTitle>
          <SheetDescription>
            A smart list keeps itself up to date: {recordType === "account" ? "companies" : "people"} join when they start matching and leave when they stop. {byId(brandID)?.name ?? "This brand"}'s {recordType === "account" ? "companies" : "people"} only.
          </SheetDescription>
        </SheetHeader>
        <form onSubmit={submit} className="space-y-5 px-4 pb-6 text-[13px]">
          <div className="flex flex-wrap items-end gap-3">
            <label className="block min-w-[14rem] flex-1">
              <span className="mb-1 block font-medium">Name</span>
              <input className={`${field} w-full`} required maxLength={200} placeholder="e.g. Paying customers" value={name} onChange={(e) => setName(e.target.value)} />
            </label>
            {describing ? (
              <label className="block min-w-[14rem] flex-[2]">
                <span className="mb-1 block font-medium">Description <span className="font-normal text-muted-foreground">(optional)</span></span>
                <input className={`${field} w-full`} maxLength={2000} autoFocus={!description} value={description} onChange={(e) => setDescription(e.target.value)} />
              </label>
            ) : (
              <button type="button" className="h-8 text-muted-foreground hover:text-foreground" onClick={() => setDescribing(true)}>+ Add a description</button>
            )}
          </div>

          {!list && (
            <div className="inline-flex gap-0.5 rounded-lg border bg-muted/60 p-0.5" role="tablist" aria-label="Fill it from">
              <button type="button" role="tab" aria-selected={from === "rules"} className={tab(from === "rules")} onClick={() => setFrom("rules")}>Choose who's in it</button>
              <button type="button" role="tab" aria-selected={from === "query"} className={tab(from === "query")} onClick={() => setFrom("query")}>Use a saved query</button>
            </div>
          )}

          {from === "query" ? (
            <div className="max-w-2xl space-y-2">
              <p className="text-muted-foreground">For lists that conditions can't say. A saved query is a question you wrote and saved under Data, Queries. The list holds the people it returns and runs it again every 15 minutes.</p>
              <label className="block">
                <span className="mb-1 block font-medium">Saved query</span>
                <select className={`${field} w-full`} required value={queryID} onChange={(e) => setQueryID(e.target.value)}>
                  <option value="">{queries === null ? "Loading…" : queries.length ? "Pick a saved query" : "No saved queries yet"}</option>
                  {queries?.map((q) => <option key={q.id} value={q.id}>{q.name}</option>)}
                  {queryID && queries && !queries.some((q) => q.id === queryID) && <option value={queryID}>The current query</option>}
                </select>
              </label>
              <p className="text-[12px] text-muted-foreground">
                Return a <span className="font-mono">record_id</span> column, for example <span className="font-mono">SELECT id AS record_id FROM people WHERE …</span>. Up to 50,000 people. If the query fails, the list keeps who it had and says why.
              </p>
            </div>
          ) : (
          <div className="grid items-start gap-6 lg:grid-cols-[minmax(0,1fr)_17rem]">
            <div className="min-w-0 space-y-5">
              <DescribeBox ws={ws} brand={brandID} recordType={recordType} onRules={(d) => {
                setRules(d.rules ?? []);
                setMatch(d.match === "any" ? "any" : "all");
                if (!name.trim()) setName(d.name);
              }} />
              <Builder rules={rules} match={match} onChange={(r, m) => { setRules(r); setMatch(m); }} self={list?.id} recordType={recordType} {...choices} />
            </div>

            <aside aria-live="polite" className="space-y-3 rounded-lg border p-4 lg:sticky lg:top-4">
              <div>
                <p className="text-[12px] text-muted-foreground">{recordType === "account" ? "Companies" : "People"} who match right now</p>
                {!ready(rules) || rules.length === 0 ? (
                  <p className="mt-1 text-muted-foreground">{rules.length === 0 ? "Add a condition to see who matches." : "Finish the conditions to see who matches."}</p>
                ) : countError ? (
                  <ErrorText>{countError}</ErrorText>
                ) : count ? (
                  <p className={`text-3xl font-semibold tracking-tight tabular-nums ${counting ? "opacity-50" : ""}`} data-count>{count.members.toLocaleString()}</p>
                ) : (
                  <p className="mt-1 text-muted-foreground">Counting…</p>
                )}
                {ready(rules) && rules.length > 0 && !countError && count && count.sample.length > 0 && (
                  <p className="mt-1 truncate text-[12px] text-muted-foreground">{count.sample.map((p) => p.name || p.email).join(", ")}{count.members > count.sample.length ? ", …" : ""}</p>
                )}
              </div>
              {ready(rules) && rules.length > 0 && (
                <div className="border-t pt-3">
                  <p className="font-semibold">In plain words</p>
                  <p><span className="text-muted-foreground">{who} </span>{plainText(rules, match, names)}.</p>
                </div>
              )}
              {ready(rules) && negative(rules, match) && (
                <p className="border-t pt-3 text-[12px] text-amber-700 dark:text-amber-400">{match === "any" && rules.length > 1
                  ? "One of these conditions only leaves people out, and any one is enough, so this list will hold nearly everyone. Switch \"or\" to \"and\", or move it under \"But leave out\"."
                  : "Nothing here says who to include, so this list will hold nearly everyone. Add something they did or are under \"Include\"."}</p>
              )}
            </aside>
          </div>
          )}

          <ErrorText>{error}</ErrorText>
          <div className="flex flex-wrap gap-2 border-t pt-4">
            <Button disabled={busy || !name.trim() || (from === "rules" ? !ready(rules) || rules.length === 0 : !queryID)}>{busy ? "Saving…" : list ? "Save" : "Create smart list"}</Button>
            <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
          </div>
        </form>
      </SheetContent>
    </Sheet>
  );
}

type Draft = Schemas["SmartListDraft"];

// DescribeBox writes a list's rules from a sentence, on the workspace's own
// AI model, and hands them to the builder to check and save.
function DescribeBox({ ws, brand, recordType, onRules }: { ws: string; brand: string; recordType: string; onRules: (d: Draft) => void }) {
  const [text, setText] = useState("");
  const [d, setD] = useState<Draft | null>(null);
  const [error, setError] = useState<{ message: string; noModel?: boolean } | null>(null);
  const waiting = d?.status === "queued" || d?.status === "running";
  const rulesTo = useRef(onRules);
  rulesTo.current = onRules;

  // While the model works, look again every second or so, for two minutes.
  useEffect(() => {
    if (!d || !waiting) return;
    const started = Date.parse(d.created_at);
    const t = setTimeout(async () => {
      const { data, error } = await api.GET("/v1/brands/{brand}/smart-list-drafts/{draft}", { params: { path: { brand, draft: d.id } } });
      if (!data) return setError({ message: errorMessage(error) });
      const late = Date.now() - started > 120000 && (data.status === "queued" || data.status === "running");
      setD(late ? { ...data, status: "failed", error: "This is taking too long. Try again in a moment." } : data);
      if (data.status === "ready" && data.rules) rulesTo.current(data);
    }, 1200);
    return () => clearTimeout(t);
  }, [d, waiting, brand]);

  async function write() {
    if (!text.trim() || waiting) return;
    setError(null);
    setD(null);
    const { data, error } = await api.POST("/v1/brands/{brand}/smart-list-drafts", { params: { path: { brand } }, body: { description: text.trim(), record_type: recordType as "person" } });
    if (!data || !("id" in data)) {
      const code = (error as { error?: { code?: string } } | undefined)?.error?.code;
      const noModel = code === "connector_needed" || code === "ai_model_needed";
      return setError({ message: data ? "Held for approval in the mission." : noModel ? "Connect an AI model with your own key to fill in a list from a description. Outflare never supplies one." : errorMessage(error), noModel });
    }
    setD(data);
  }

  return (
    <div className="space-y-2 rounded-lg border bg-muted/40 p-3">
      <label htmlFor="describe-list" className="flex items-center gap-1.5 font-semibold"><Sparkles className="size-4 text-emerald-600" />Describe who should be in it</label>
      <p className="text-[12px] text-muted-foreground">Type it in your own words and your AI model fills in the conditions below. You check them before anything is saved.</p>
      <Textarea id="describe-list" rows={2} maxLength={500} className="resize-none bg-background"
        placeholder="For example: paid in the last 90 days but never opened the app" value={text} onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => { if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); void write(); } }} />
      <div className="flex flex-wrap items-center gap-3">
        <Button type="button" disabled={!text.trim() || waiting} onClick={() => void write()} title="Your own AI model fills in the conditions from your field, event and page names. It never sees people.">{waiting ? "Filling in…" : "Fill in for me"}</Button>
        <span className="text-[12px] text-muted-foreground">Or skip this and add the conditions yourself below.</span>
      </div>
      {error && (
        <div className="flex flex-wrap items-center gap-3">
          <ErrorText>{error.message}</ErrorText>
          {error.noModel && <AppLink ws={ws} tab="connectors" className="text-[13px] text-blue-700 underline dark:text-blue-400">Connect an AI model</AppLink>}
        </div>
      )}
      {d?.status === "failed" && <ErrorText>{d.error || "That didn't work. Try again."}</ErrorText>}
      {d?.status === "ready" && <p className="text-[13px]">{d.rules ? `Filled in below. Check it before you save. ${d.explanation}` : d.explanation}</p>}
    </div>
  );
}

// Why a list from a saved query kept its members (smart_lists.error_code).
const queryListError: Record<string, string> = {
  query_missing: "Its saved query was archived, so the list keeps who it had. Pick another query.",
  no_record_id: "Its query has no record_id (or id) column, so the list keeps who it had. Add one, for example SELECT id AS record_id FROM people.",
  too_many: "Its query returns more than 50,000 people, so the list keeps who it had. Narrow the query.",
};

// What a member has paid, in its own currency ("€60.00"), or a dash.
function lifetimeValue(p: SmartListPerson): string {
  const v = p.lifetime_revenue;
  if (!v) return "—";
  try {
    const f = new Intl.NumberFormat(undefined, { style: "currency", currency: v.currency });
    return f.format(v.amount_minor / 10 ** (f.resolvedOptions().maximumFractionDigits ?? 2));
  } catch {
    return `${(v.amount_minor / 100).toFixed(2)} ${v.currency}`;
  }
}

const day = (iso?: string) => (iso ? new Date(iso).toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" }) : "—");

export function SmartListDetail({ ws, list: initial, canManage, onBack, onEdit, onChanged, go }: { ws: string; list: SmartList; canManage: boolean; onBack: () => void; onEdit: (l: SmartList) => void; onChanged: () => void; go: (t: Tab, item?: string) => void }) {
  const brand = initial.brand_id ?? "";
  const names = useNames(useRuleChoices(ws, brand));
  const [list, setList] = useState(initial);
  const [people, setPeople] = useState<SmartListPerson[] | null>(null);
  const [next, setNext] = useState<string | null>(null);
  const [error, setError] = useState("");
  // A static list holds who was put in it (by hand, a journey, an import
  // or a bulk edit): no rules, nothing to refresh.
  const isStatic = initial.kind === "static";
  // A query list's members are its saved query's records.
  const isQuery = initial.kind === "query";
  const [waiting, setWaiting] = useState(!isStatic && !initial.refreshed_at);
  const [bulk, setBulk] = useState(false);
  // Renaming it and changing its description, in place, with Save and Cancel.
  const [editing, setEditing] = useState(false);
  const [name, setName] = useState(initial.name);
  const [description, setDescription] = useState(initial.description);
  const [saving, setSaving] = useState(false);

  function edit() {
    setName(list.name);
    setDescription(list.description);
    setEditing(true);
  }
  async function saveDetails(e: FormEvent) {
    e.preventDefault();
    setSaving(true);
    const { data, error } = await api.PATCH("/v1/brands/{brand}/smart-lists/{id}", { params: { path: { brand, id: list.id } }, body: { name: name.trim(), description: description.trim() } });
    setSaving(false);
    if (error || !data || !("id" in data)) return toast(errorMessage(error), "error");
    setList(data as SmartList);
    setEditing(false);
    toast("Saved.");
    onChanged();
  }

  const load = useCallback(async () => {
    const [l, m] = await Promise.all([
      api.GET("/v1/brands/{brand}/smart-lists/{id}", { params: { path: { brand, id: initial.id } } }),
      api.GET("/v1/brands/{brand}/smart-lists/{id}/members", { params: { path: { brand, id: initial.id }, query: { limit: 100 } } }),
    ]);
    if (l.data) setList(l.data);
    if (m.data) {
      setPeople(m.data.data);
      setNext(m.data.next_cursor ?? null);
    } else setError(errorMessage(m.error ?? l.error));
    return l.data;
  }, [brand, initial.id]);

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
    const { data } = await api.GET("/v1/brands/{brand}/smart-lists/{id}/members", { params: { path: { brand, id: list.id }, query: { limit: 100, cursor: next } } });
    if (data) {
      setPeople((p) => [...(p ?? []), ...data.data]);
      setNext(data.next_cursor ?? null);
    }
  }

  async function refresh() {
    const { error } = await api.POST("/v1/brands/{brand}/smart-lists/{id}/refresh", { params: { path: { brand, id: list.id } } });
    if (error) return toast(errorMessage(error));
    toast("Updating who's in it.");
    setWaiting(true);
  }

  async function takeOut(p: SmartListPerson) {
    const { error } = await api.POST("/v1/brands/{brand}/smart-lists/{id}/members/remove", { params: { path: { brand, id: list.id } }, body: { record_ids: [p.id] } });
    if (error) return toast(errorMessage(error));
    toast(`Took ${p.name || p.email || "them"} out of ${list.name}.`);
    setPeople((ps) => (ps ?? []).filter((x) => x.id !== p.id));
    load();
    onChanged();
  }

  async function copy() {
    const { data, error } = await api.POST("/v1/brands/{brand}/smart-lists/{id}/copy", { params: { path: { brand, id: list.id } }, body: {} });
    if (error) return toast(errorMessage(error));
    if (data && "id" in data) toast(`Saved ${data.members.toLocaleString()} ${data.members === 1 ? "person" : "people"} as ${data.name}, a static list.`);
    onChanged();
  }

  async function remove() {
    if (!(await confirmAction(`Delete ${list.name}?`, "The list goes; the people in it stay on your customer records.", "Delete list"))) return;
    const { error } = await api.DELETE("/v1/brands/{brand}/smart-lists/{id}", { params: { path: { brand, id: list.id } } });
    if (error) return toast(errorMessage(error));
    toast(`Deleted ${list.name}.`);
    onChanged();
    onBack();
  }

  // A member's name, linked to their record.
  const open = (p: SmartListPerson, label: string) => (
    <AppLink ws={ws} tab="people" item={p.id} onOpen={() => go("people", p.id)} className="font-medium text-blue-700 underline-offset-2 hover:underline dark:text-blue-400">{label}</AppLink>
  );
  const fieldLabel = names.field;
  const listName = names.list;

  return (
    <div className="space-y-6">
      {editing ? (
        <form className="mb-6 max-w-xl space-y-4 border-b pb-5" onSubmit={saveDetails}>
          <Input label="Name" required maxLength={200} value={name} onChange={(e) => setName(e.target.value)} autoFocus />
          <label className="block text-sm">
            <span className="mb-1.5 block text-[13px] font-medium">Description <span className="font-normal text-muted-foreground">(optional)</span></span>
            <textarea className={`${field} w-full`} rows={3} maxLength={2000} value={description} onChange={(e) => setDescription(e.target.value)} />
            <span className="mt-1 block text-xs text-muted-foreground">Shown under the name, so everyone knows what the list is for.</span>
          </label>
          <div className="flex gap-2">
            <Button type="submit" disabled={saving || !name.trim()}>{saving ? "Saving…" : "Save"}</Button>
            <Button type="button" variant="ghost" onClick={() => setEditing(false)}>Cancel</Button>
          </div>
        </form>
      ) : (
        <PageHeader
          back={{ label: "Lists", onClick: onBack }}
          title={<>{list.name} <Badge tone="blue">{isStatic ? "Static list" : isQuery ? "From a query" : "Smart list"}</Badge> <BrandBadge id={list.brand_id} /></>}
          description={list.description || (isStatic ? "The people put in it by hand, a journey, an import or a bulk edit." : isQuery ? "Keeps itself up to date from a saved query." : "Keeps itself up to date: people join when they start matching and leave when they stop.")}
          action={canManage && (
            <>
              <Button variant="ghost" onClick={edit}>Edit name and description</Button>
              {!isStatic && <Button variant="ghost" onClick={refresh} disabled={waiting}>{waiting ? "Updating…" : "Refresh now"}</Button>}
              {!isStatic && <Button onClick={() => onEdit(list)}>{isQuery ? "Change query" : "Edit who's in it"}</Button>}
              <Menu
                items={[
                    { label: "Bulk edit everyone in it", onClick: () => setBulk(true) },
                  ...(list.record_type === "person" ? [{ label: "Save a copy", onClick: copy }] : []),
                  { label: "Delete list", danger: true, onClick: remove },
                ]}
              />
            </>
          )}
        />
      )}
      {isStatic ? (
        <Stats cols={2}>
          <Stat label={list.record_type === "account" ? "Companies" : "People"} value={list.members.toLocaleString()} />
          <Stat label="Changed" value={ago(list.updated_at)} sub="when people are added or taken out" />
        </Stats>
      ) : (
        <Stats cols={3}>
          <Stat label="People" value={list.refreshed_at ? list.members.toLocaleString() : "…"} />
          <Stat label="Updated" value={list.refreshed_at ? ago(list.refreshed_at) : "filling"} sub="every 15 minutes" />
          {isQuery ? <Stat label="Source" value="Saved query" sub="up to 50,000 people" /> : <Stat label="Conditions" value={conditions(list.rules)} sub="that decide who is in" />}
        </Stats>
      )}
      {list.error_code && (
        <p role="alert" className="rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-[13px] text-amber-800 dark:text-amber-200">
          {queryListError[list.error_code] ??
            (list.error_code === "query_timeout"
              ? isQuery
                ? "The query took too long and stopped. The list keeps who it had; make the query faster."
                : "The last update took too long and stopped. Narrow the rules, for example with a time window on an event."
              : isQuery
                ? "The query failed, so the list keeps who it had. Fix the query in Query; it tries again in 15 minutes."
                : "The last update failed. It tries again in 15 minutes.")}
        </p>
      )}
      <Card title="Who's in it" description={isStatic ? "People join through a bulk edit (from another list, a saved query or ids), a journey or an import." : isQuery ? "The records in its saved query's record_id (or id) column." : `${list.record_type === "account" ? "Companies that" : "People who"} ${plainText(list.rules, list.match, { field: fieldLabel, list: listName })}.`}>
        <ErrorText>{error}</ErrorText>
        {people === null && !error && <Loading />}
        {people !== null && (
          <DataTable
            data={people}
            rowId={(p) => p.id}
            searchPlaceholder="Search people"
            empty={isStatic ? "Nobody is in this list yet." : waiting ? "Filling the list…" : isQuery ? "The query returns nobody right now." : "Nobody matches these rules right now."}
            view={list.record_type === "account" ? "list-companies" : "list-people"}
            hidden={["first_seen"]}
            mobile={(p) => (
              <div className="space-y-0.5">
                {open(p, p.name || p.email || "Unnamed person")}
                <div className="text-[12px] text-muted-foreground">{[p.email, p.lifetime_revenue ? lifetimeValue(p) : "", `joined ${ago(p.entered_at)}`].filter(Boolean).join(" · ")}</div>
              </div>
            )}
            columns={[
              { id: "name", header: "Name", enableHiding: false, accessorFn: (p) => p.name || p.email, cell: ({ row: { original: p } }) => open(p, p.name || p.email || "Unnamed") },
              { id: "email", header: list.record_type === "account" ? "Domain" : "Email", accessorFn: (p) => p.email },
              { id: "lifetime_value", header: "Lifetime value", accessorFn: (p) => p.lifetime_revenue?.amount_minor ?? -1, meta: { align: "right", csv: (p: SmartListPerson) => lifetimeValue(p) }, cell: ({ row: { original: p } }) => <span className="font-mono tabular-nums" title="Everything they have paid, less refunds.">{lifetimeValue(p)}</span> },
              { id: "signed_up", header: "Signed up", accessorFn: (p) => p.signed_up_at ?? "", cell: ({ row: { original: p } }) => <span className="text-muted-foreground" title={p.signed_up_at ? new Date(p.signed_up_at).toLocaleString() : "No sign-up event from them yet."}>{day(p.signed_up_at)}</span> },
              { id: "first_seen", header: "First seen", accessorFn: (p) => p.created_at ?? "", cell: ({ row: { original: p } }) => <span className="text-muted-foreground" title={p.created_at ? new Date(p.created_at).toLocaleString() : undefined}>{day(p.created_at)}</span> },
              { id: "joined", header: "Joined list", accessorFn: (p) => p.entered_at ?? "", cell: ({ row: { original: p } }) => <span className="text-muted-foreground">{ago(p.entered_at)}</span> },
              ...(isStatic && canManage
                ? [{ id: "remove", header: "", enableSorting: false, enableHiding: false, cell: ({ row: { original: p } }: { row: { original: SmartListPerson } }) => <Button variant="ghost" onClick={() => takeOut(p)}>Remove</Button> }]
                : []),
            ]}
          />
        )}
        {next && <div className="mt-3"><Button variant="ghost" onClick={more}>Show more</Button></div>}
      </Card>
      {bulk && <BulkEditSheet ws={ws} brand={brand} source={{ kind: "list", list }} onClose={() => setBulk(false)} onDone={load} />}
    </div>
  );
}
