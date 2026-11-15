// Needs attention: the brand's open signals (docs/signals.md), soonest due
// first. Each row is one thing to act on about a person or company, with
// the three things someone does with it in one click: take it, finish it,
// or say it wasn't useful. The types and the events that raise them are
// edited behind "Signal types".

import { useCallback, useEffect, useState } from "react";
import { Flag } from "lucide-react";
import type { Schemas } from "@outflare/sdk";
import { PickBrand } from "@/components/pick-brand";
import { useBrand } from "@/lib/brand";
import { track } from "@/lib/analytics";
import { cn } from "@/lib/utils";
import { api, errorMessage } from "../api";
import { ago, plural } from "../format";
import { Badge, Button, Card, Empty, ErrorText, field, Loading, Menu, PageHeader, toast } from "../ui";
import type { Tab } from "../App";
import { channelText, ConversationSettings, Credit } from "./SignalCredit";

type Signal = Schemas["Signal"];
type SignalType = Schemas["SignalTypeList"]["data"][number];
type Update = { status?: Signal["status"]; outcome?: NonNullable<Signal["outcome"]>; owner_id?: string | null; snoozed_until?: string | null };
type Filter = "mine" | "all" | "late" | "none" | "snoozed" | "closed";

const filters: { id: Filter; label: string }[] = [
  { id: "mine", label: "Mine" },
  { id: "all", label: "All open" },
  { id: "late", label: "Late" },
  { id: "none", label: "No owner" },
  { id: "snoozed", label: "Snoozed" },
  { id: "closed", label: "Closed" },
];

const outcomeText: Record<string, string> = { won: "Won", no_change: "Done, no change", not_useful: "Not useful", duplicate: "Duplicate" };
const sourceText: Record<string, string> = { event: "Raised by a rule or event", journey: "Raised by a journey", manual: "Raised by a person", agent: "Raised by an agent", api: "Raised through the API" };

// Where a signal came from, in words: "Found by AI in a support
// conversation", "Flagged from a Telegram chat", "Raised by a journey".
function originText(s: Signal): string {
  const where = typeof s.data.channel === "string" ? channelText[s.data.channel] : undefined;
  if (where) return s.source === "agent" ? `Found by AI in ${where}` : `Flagged from ${where}`;
  return sourceText[s.source] ?? s.source;
}

// When it is due, in words: "due tomorrow", "2 days late".
export function dueText(iso: string | null | undefined): { text: string; late: boolean } {
  if (!iso) return { text: "No due date", late: false };
  const days = Math.round((new Date(iso).setHours(0, 0, 0, 0) - new Date().setHours(0, 0, 0, 0)) / 86400000);
  if (days < 0) return { text: `${plural(-days, "day")} late`, late: true };
  if (days === 0) return { text: "Due today", late: new Date(iso).getTime() < Date.now() };
  if (days === 1) return { text: "Due tomorrow", late: false };
  return { text: `Due in ${days} days`, late: false };
}

const inDays = (n: number) => new Date(Date.now() + n * 86400000).toISOString();

export function Signals(props: { ws: string; canManage: boolean; go: (tab: Tab, item?: string) => void }) {
  const { brand, active } = useBrand(props.ws);
  if (!brand) return <PickBrand what="Needs attention" brands={active.length} />;
  return <SignalsPage key={brand.id} {...props} brandId={brand.id} />;
}

function SignalsPage({ ws, canManage, go, brandId }: { ws: string; canManage: boolean; go: (tab: Tab, item?: string) => void; brandId: string }) {
  const [filter, setFilter] = useState<Filter>("mine");
  // Teammates a signal can be given to.
  const [members, setMembers] = useState<{ user_id: string; name: string }[]>([]);
  useEffect(() => {
    if (!canManage) return;
    void api.GET("/v1/workspaces/{ws}/members", { params: { path: { ws } } }).then(({ data }) =>
      setMembers(
        ((data?.data ?? []) as { user_id: string; name?: string; email?: string }[])
          .map((m) => ({ user_id: m.user_id, name: m.name || m.email || "A teammate" }))
          .sort((a, b) => a.name.localeCompare(b.name))
          .slice(0, 20),
      ),
    );
  }, [ws, canManage]);
  const [type, setType] = useState("");
  const [rows, setRows] = useState<Signal[] | null>(null);
  const [counts, setCounts] = useState({ open: 0, mine: 0, late: 0, no_owner: 0 });
  const [types, setTypes] = useState<SignalType[]>([]);
  const [error, setError] = useState("");
  const [view, setView] = useState<"queue" | "types" | "credit" | "conversations">("queue");
  const editing = view === "types";
  const [busy, setBusy] = useState("");

  const load = useCallback(async () => {
    const query: { status?: "open" | "snoozed" | "closed"; owner?: string; type?: string } = {};
    if (filter === "mine") query.owner = "me";
    if (filter === "none") query.owner = "none";
    if (filter === "snoozed" || filter === "closed") query.status = filter;
    if (type) query.type = type;
    const { data, error } = await api.GET("/v1/brands/{brand}/signals", { params: { path: { brand: brandId }, query } });
    if (error || !data) return setError(errorMessage(error));
    setError("");
    setRows(filter === "late" ? data.data.filter((s) => s.late) : data.data);
    setCounts(data.counts);
  }, [brandId, filter, type]);
  const loadTypes = useCallback(async () => {
    const { data } = await api.GET("/v1/brands/{brand}/signal-types", { params: { path: { brand: brandId } } });
    if (data) setTypes(data.data);
  }, [brandId]);
  useEffect(() => {
    load();
  }, [load]);
  useEffect(() => {
    loadTypes();
  }, [loadTypes]);

  // The first visit lands on what is yours; with nothing of yours, on everything.
  const [settled, setSettled] = useState(false);
  useEffect(() => {
    if (settled || rows === null) return;
    setSettled(true);
    if (filter === "mine" && rows.length === 0 && counts.open > 0) setFilter("all");
  }, [rows, counts, filter, settled]);

  async function change(s: Signal, body: Update, done: string) {
    setBusy(s.id);
    const { error } = await api.PATCH("/v1/brands/{brand}/signals/{id}", { params: { path: { brand: brandId, id: s.id } }, body });
    setBusy("");
    if (error) return toast(errorMessage(error), "error");
    toast(done);
    load();
    loadTypes();
  }

  const count: Record<Filter, number | undefined> = { mine: counts.mine, all: counts.open, late: counts.late, none: counts.no_owner, snoozed: undefined, closed: undefined };

  return (
    <div>
      <PageHeader
        title={view === "credit" ? "Credit" : view === "conversations" ? "Signals from conversations" : view === "types" ? "Signal types" : "Needs attention"}
        description={
          view === "credit"
            ? "Whose signals turned into revenue, and which sources are worth listening to."
            : view === "conversations"
              ? "Let your AI listen for what customers say in conversations, and put it in front of the account's owner."
              : view === "types"
                ? "The kinds of signal this brand raises, how long an owner has, and the events that raise each."
                : "People and companies someone should act on now: a customer at risk, an account ready for more, a renewal coming up. Rules, teammates and agents raise them; each has an owner and a due date."
        }
        back={view === "queue" ? undefined : { label: "Needs attention", onClick: () => setView("queue") }}
        action={
          view === "queue" && (
            <div className="flex flex-wrap gap-1">
              {(
                [
                  ["credit", "Credit"],
                  ["conversations", "Conversations"],
                  ["types", "Signal types"],
                ] as const
              ).map(([id, label]) => (
                <Button
                  key={id}
                  variant="ghost"
                  onClick={() => {
                    setView(id);
                    track(id === "types" ? "ui.signal_types_opened" : id === "credit" ? "ui.signal_credit_opened" : "ui.conversation_signals_opened");
                  }}
                >
                  {label}
                </Button>
              ))}
            </div>
          )
        }
      />
      {error && <ErrorText>{error}</ErrorText>}
      {view === "credit" ? (
        <Credit brandId={brandId} />
      ) : view === "conversations" ? (
        <ConversationSettings brandId={brandId} canManage={canManage} go={go} />
      ) : editing ? (
        <TypesEditor brandId={brandId} types={types} canManage={canManage} onChange={loadTypes} go={go} />
      ) : (
        <>
          <div className="mb-4 flex flex-wrap items-center gap-2">
            <div role="tablist" aria-label="Which signals" className="flex flex-wrap gap-1">
              {filters.map((f) => (
                <button
                  key={f.id}
                  role="tab"
                  aria-selected={filter === f.id}
                  onClick={() => {
                    setFilter(f.id);
                    setSettled(true);
                    track("ui.signals_filter_changed", { filter: f.id });
                  }}
                  className={cn(
                    "rounded-md px-2.5 py-1 text-[13px]",
                    filter === f.id ? "bg-secondary font-medium text-foreground" : "text-muted-foreground hover:text-foreground",
                  )}
                >
                  {f.label}
                  {count[f.id] !== undefined && (
                    <span className={cn("ml-1.5 tabular-nums", f.id === "late" && count[f.id]! > 0 ? "text-red-700 dark:text-red-400" : "text-muted-foreground")}>{count[f.id]}</span>
                  )}
                </button>
              ))}
            </div>
            <select aria-label="Signal type" className={cn(field, "ml-auto")} value={type} onChange={(e) => setType(e.target.value)}>
              <option value="">Every type</option>
              {types.map((t) => (
                <option key={t.key} value={t.key}>
                  {t.label}
                </option>
              ))}
            </select>
          </div>
          {rows === null ? (
            <Loading />
          ) : rows.length === 0 ? (
            <Empty title={filter === "mine" ? "Nothing is waiting on you" : filter === "closed" ? "No closed signals yet" : "Nothing here"}>
              {counts.open === 0 && filter !== "closed" && filter !== "snoozed"
                ? "Signals appear when a rule's event raises one, or when someone flags a person or company. Open Signal types to choose which events raise them."
                : "Try another filter above."}
            </Empty>
          ) : (
            <ul className="divide-y rounded-xl border">
              {rows.map((s) => {
                const due = dueText(s.due_at);
                const open = s.status === "open" || s.status === "accepted";
                return (
                  <li key={s.id} className="flex flex-wrap items-start gap-x-4 gap-y-2 px-4 py-3">
                    <div className="min-w-0 flex-1 basis-72">
                      <div className="flex flex-wrap items-center gap-2">
                        <Badge tone={s.type === "churn_risk" || s.type === "billing" ? "red" : s.type === "other" ? "grey" : "blue"}>{s.type_label}</Badge>
                        <button className="truncate text-sm font-medium underline-offset-2 hover:underline" onClick={() => go("people", s.record_id)}>
                          {s.record_name || "Unnamed"}
                        </button>
                        {s.status === "accepted" && <Badge tone="green">In progress</Badge>}
                        {s.escalated_at && open && <Badge tone="amber" title="Nobody accepted it in time, so it went to the owners and admins.">Escalated</Badge>}
                      </div>
                      {s.title && s.title !== s.type_label && <p className="mt-1 text-sm">{s.title}</p>}
                      {s.detail && <p className="mt-0.5 text-[13px] whitespace-pre-line text-muted-foreground">{s.detail}</p>}
                      {typeof s.data.quote === "string" && s.data.quote && (
                        <blockquote className="mt-1.5 border-l-2 pl-2.5 text-[13px] text-muted-foreground italic">“{s.data.quote}”</blockquote>
                      )}
                      <p className="mt-1 text-xs text-muted-foreground">
                        {open ? (
                          <span className={due.late ? "font-medium text-red-700 dark:text-red-400" : ""}>{due.text}</span>
                        ) : (
                          <span>
                            {outcomeText[s.outcome ?? ""] ?? "Closed"} {ago(s.closed_at)}
                          </span>
                        )}
                        {" · "}
                        {s.owner_name ?? "No owner"}
                        {" · "}
                        {originText(s)} {ago(s.created_at)}
                        {(s.data.channel === "support" || s.data.channel === "telegram") && typeof s.data.ref === "string" && (
                          <>
                            {" · "}
                            <button className="underline underline-offset-2 hover:text-foreground" onClick={() => go(s.data.channel as Tab, s.data.ref as string)}>
                              Open the conversation
                            </button>
                          </>
                        )}
                        {s.snoozed_until && new Date(s.snoozed_until).getTime() > Date.now() && ` · Snoozed until ${new Date(s.snoozed_until).toLocaleDateString(undefined, { month: "short", day: "numeric" })}`}
                      </p>
                    </div>
                    {canManage && (
                      <div className="flex shrink-0 items-center gap-1">
                        {open ? (
                          <>
                            {s.status === "open" && (
                              <Button variant="ghost" disabled={busy === s.id} onClick={() => change(s, { status: "accepted" }, "It's yours")}>
                                Accept
                              </Button>
                            )}
                            <Menu
                              label="Done"
                              aria={`Close the signal about ${s.record_name}`}
                              items={[
                                { label: "Won: it led to what we wanted", onClick: () => change(s, { status: "done", outcome: "won" }, "Closed as won") },
                                { label: "Done, nothing changed", onClick: () => change(s, { status: "done", outcome: "no_change" }, "Closed") },
                                { label: "Not useful", onClick: () => change(s, { status: "dismissed", outcome: "not_useful" }, "Closed as not useful") },
                                { label: "Duplicate", onClick: () => change(s, { status: "dismissed", outcome: "duplicate" }, "Closed as a duplicate") },
                              ]}
                            />
                            <Menu
                              aria={`More for the signal about ${s.record_name}`}
                              items={[
                                s.snoozed_until && new Date(s.snoozed_until).getTime() > Date.now()
                                  ? { label: "Wake it now", onClick: () => change(s, { snoozed_until: null }, "Back in the queue") }
                                  : false,
                                { label: "Snooze until tomorrow", onClick: () => change(s, { snoozed_until: inDays(1) }, "Snoozed until tomorrow") },
                                { label: "Snooze for a week", onClick: () => change(s, { snoozed_until: inDays(7) }, "Snoozed for a week") },
                                s.owner_id ? { label: "Give it up (no owner)", onClick: () => change(s, { owner_id: null, status: "open" }, "It has no owner now") } : false,
                                { label: "Open their page", onClick: () => go("people", s.record_id) },
                              ]}
                            />
                            {members.length > 1 && (
                              <Menu
                                label="Assign"
                                aria={`Give the signal about ${s.record_name} to a teammate`}
                                items={members
                                  .filter((m) => m.user_id !== s.owner_id)
                                  .map((m) => ({
                                    label: m.name,
                                    onClick: () => {
                                      track("ui.signal_assigned", {});
                                      void change(s, { owner_id: m.user_id }, `Given to ${m.name}`);
                                    },
                                  }))}
                              />
                            )}
                          </>
                        ) : (
                          <Button variant="ghost" disabled={busy === s.id} onClick={() => change(s, { status: "open" }, "Reopened")}>
                            Reopen
                          </Button>
                        )}
                      </div>
                    )}
                  </li>
                );
              })}
            </ul>
          )}
        </>
      )}
    </div>
  );
}

// The brand's signal types: what each is called, how long its owner has,
// which events raise it, and how its signals ended.
function TypesEditor({ brandId, types, canManage, onChange, go }: { brandId: string; types: SignalType[]; canManage: boolean; onChange: () => void; go: (tab: Tab) => void }) {
  const [draft, setDraft] = useState<Record<string, { label: string; events: string; due_days: string }>>({});
  const [fresh, setFresh] = useState({ label: "", events: "", due_days: "2" });
  const [error, setError] = useState("");

  const value = (t: SignalType) => draft[t.key] ?? { label: t.label, events: t.events.join(", "), due_days: String(t.due_days) };
  const events = (s: string) => s.split(/[\s,]+/).map((e) => e.trim()).filter(Boolean);

  async function save(key: string, v: { label: string; events: string; due_days: string }, isNew = false) {
    const { error } = await api.PUT("/v1/brands/{brand}/signal-types/{key}", {
      params: { path: { brand: brandId, key } },
      body: { label: v.label.trim(), events: events(v.events), due_days: Number(v.due_days) || 0 },
    });
    if (error) return setError(errorMessage(error));
    setError("");
    setDraft(({ [key]: _, ...rest }) => rest);
    if (isNew) setFresh({ label: "", events: "", due_days: "2" });
    toast("Saved");
    onChange();
  }
  async function remove(t: SignalType) {
    const { error } = await api.DELETE("/v1/brands/{brand}/signal-types/{key}", { params: { path: { brand: brandId, key: t.key } } });
    if (error) return setError(errorMessage(error));
    toast(`${t.label} removed`);
    onChange();
  }
  const keyOf = (label: string) => label.toLowerCase().replace(/[^a-z0-9]+/g, "_").replace(/^_+|_+$/g, "").replace(/^(\d)/, "s_$1").slice(0, 40);

  return (
    <div className="space-y-4">
      <p className="max-w-2xl text-sm text-muted-foreground">
        A signal type is one kind of thing to act on. List the events that should raise it, and every time one happens to a person or company a signal lands in the queue. The events usually come from{" "}
        <button className="underline" onClick={() => go("rules")}>
          rules
        </button>
        : a rule that emits <code className="font-mono text-xs">account.churn_risk</code> raises a Churn risk signal once that event is listed here.
      </p>
      {error && <ErrorText>{error}</ErrorText>}
      {types.map((t) => {
        const v = value(t);
        const dirty = !!draft[t.key];
        const set = (patch: Partial<typeof v>) => setDraft((d) => ({ ...d, [t.key]: { ...v, ...patch } }));
        return (
          <Card
            key={t.key}
            title={t.label}
            description={t.description || undefined}
            action={
              <span className="text-xs text-muted-foreground">
                {plural(t.open, "open signal")}
                {t.closed_90d > 0 && ` · last 90 days: ${t.won_90d} won, ${t.not_useful_90d} not useful of ${t.closed_90d} closed`}
              </span>
            }
          >
            <div className="grid gap-3 sm:grid-cols-[1fr_2fr_auto]">
              <label className="text-xs text-muted-foreground">
                Name
                <input className={cn(field, "mt-1 block w-full")} value={v.label} disabled={!canManage} maxLength={80} onChange={(e) => set({ label: e.target.value })} />
              </label>
              <label className="text-xs text-muted-foreground">
                Events that raise it
                <input
                  className={cn(field, "mt-1 block w-full font-mono")}
                  value={v.events}
                  disabled={!canManage}
                  placeholder="None yet. Add an event name"
                  onChange={(e) => set({ events: e.target.value })}
                />
              </label>
              <label className="text-xs text-muted-foreground">
                Days to act
                <input className={cn(field, "mt-1 block w-24")} type="number" min={0} max={365} value={v.due_days} disabled={!canManage} onChange={(e) => set({ due_days: e.target.value })} />
              </label>
            </div>
            {canManage && (
              <div className="mt-3 flex items-center gap-2">
                <Button disabled={!dirty || !v.label.trim()} onClick={() => save(t.key, v)}>
                  Save
                </Button>
                {t.key !== "other" && (
                  <Button variant="danger" onClick={() => remove(t)}>
                    Remove
                  </Button>
                )}
              </div>
            )}
          </Card>
        );
      })}
      {canManage && (
        <Card title="Add a signal type" description="For something of your own to act on, such as Usage dropped or Ready for a case study.">
          <div className="grid gap-3 sm:grid-cols-[1fr_2fr_auto]">
            <label className="text-xs text-muted-foreground">
              Name
              <input className={cn(field, "mt-1 block w-full")} value={fresh.label} maxLength={80} placeholder="Usage dropped" onChange={(e) => setFresh({ ...fresh, label: e.target.value })} />
            </label>
            <label className="text-xs text-muted-foreground">
              Events that raise it
              <input className={cn(field, "mt-1 block w-full font-mono")} value={fresh.events} placeholder="account.usage_dropped" onChange={(e) => setFresh({ ...fresh, events: e.target.value })} />
            </label>
            <label className="text-xs text-muted-foreground">
              Days to act
              <input className={cn(field, "mt-1 block w-24")} type="number" min={0} max={365} value={fresh.due_days} onChange={(e) => setFresh({ ...fresh, due_days: e.target.value })} />
            </label>
          </div>
          <Button className="mt-3" disabled={!keyOf(fresh.label)} onClick={() => save(keyOf(fresh.label), fresh, true)}>
            Add
          </Button>
        </Card>
      )}
    </div>
  );
}

// "Flag for attention" on a person's or company's page: raise a signal
// about them by hand.
// From a conversation it is "Flag for sales": the signal remembers the
// conversation, and whoever flagged it gets the credit.
export function FlagButton({
  brandId,
  recordId,
  onRaised,
  conversation,
}: {
  brandId: string;
  recordId: string;
  onRaised?: () => void;
  conversation?: { channel: "support" | "telegram" | "email" | "call"; ref: string };
}) {
  const [open, setOpen] = useState(false);
  const [types, setTypes] = useState<SignalType[]>([]);
  const [type, setType] = useState("other");
  const [title, setTitle] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function show() {
    setOpen(true);
    track("ui.signal_flag_opened", { from: conversation?.channel ?? "account" });
    const { data } = await api.GET("/v1/brands/{brand}/signal-types", { params: { path: { brand: brandId } } });
    if (data) setTypes(data.data);
  }
  async function raise() {
    setBusy(true);
    const { error } = await api.POST("/v1/brands/{brand}/signals", { params: { path: { brand: brandId } }, body: { record_id: recordId, type, title: title.trim(), ...(conversation ? { conversation } : {}) } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    setOpen(false);
    setTitle("");
    setError("");
    toast("Flagged. It's in Needs attention");
    onRaised?.();
  }
  if (!open) {
    return (
      <Button variant="ghost" onClick={show}>
        <Flag className="size-3.5" />
        {conversation ? "Flag for sales" : "Flag for attention"}
      </Button>
    );
  }
  return (
    <div className="flex flex-wrap items-center gap-2">
      <select aria-label="What kind of signal" className={field} value={type} onChange={(e) => setType(e.target.value)}>
        {(types.length ? types : [{ key: "other", label: "Other" }]).map((t) => (
          <option key={t.key} value={t.key}>
            {t.label}
          </option>
        ))}
      </select>
      <input aria-label="What should happen" className={cn(field, "min-w-48 flex-1")} placeholder="What should happen? (optional)" value={title} maxLength={300} onChange={(e) => setTitle(e.target.value)} />
      <Button disabled={busy} onClick={raise}>
        Flag
      </Button>
      <Button variant="ghost" onClick={() => setOpen(false)}>
        Cancel
      </Button>
      {error && <ErrorText>{error}</ErrorText>}
    </div>
  );
}
