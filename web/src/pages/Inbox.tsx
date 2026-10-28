import { useCallback, useEffect, useState, type FormEvent, type ReactNode } from "react";
import type { InboundMessage, InboxTag, InboxView, Member, ThreadEntry } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { useBrand } from "@/lib/brand";
import { ask, Badge, Button, Card, confirmAction, Empty, ErrorText, field, Loading, Menu, PageHeader, toast } from "../ui";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { X } from "lucide-react";
import { listTime, stamp } from "../format";

type Category = NonNullable<InboundMessage["category"]>;
type Status = "open" | "snoozed" | "done" | "all";

// What a reply means, in the order people work through them, and the
// group each label belongs to.
type Group = "hot" | "later" | "closed" | "automatic";
export const categories: { id: Category; label: string; group: Group; tone: "green" | "blue" | "amber" | "grey" | "red" }[] = [
  { id: "interested", label: "Interested", group: "hot", tone: "green" },
  { id: "meeting_booked", label: "Meeting booked", group: "hot", tone: "green" },
  { id: "question", label: "Question", group: "hot", tone: "blue" },
  { id: "referral", label: "Referral", group: "later", tone: "blue" },
  { id: "not_now", label: "Not now", group: "later", tone: "amber" },
  { id: "objection", label: "Objection", group: "later", tone: "amber" },
  { id: "not_interested", label: "Not interested", group: "closed", tone: "grey" },
  { id: "wrong_person", label: "Wrong person", group: "closed", tone: "grey" },
  { id: "unsubscribe", label: "Unsubscribed", group: "closed", tone: "red" },
  { id: "complaint", label: "Complaint", group: "closed", tone: "red" },
  { id: "out_of_office", label: "Out of office", group: "automatic", tone: "grey" },
  { id: "auto_reply", label: "Auto-reply", group: "automatic", tone: "grey" },
];
const groups: { id: Group; label: string; help: string }[] = [
  { id: "hot", label: "Hot", help: "Interested, meeting booked or a question: answer these first." },
  { id: "later", label: "Later", help: "Referrals, not now and objections: worth coming back to." },
  { id: "closed", label: "Closed", help: "Not interested, wrong person, unsubscribed or a complaint." },
  { id: "automatic", label: "Automatic", help: "Out-of-office and other automatic replies." },
];
const categoryOf = (c: InboundMessage["category"]) => categories.find((x) => x.id === c);
const sourceLabel: Record<string, string> = {
  rules: "Labelled by the built-in rules",
  ai: "Labelled by the AI",
  human: "Labelled by a person",
  api_token: "Labelled by an integration",
  agent: "Labelled by an agent",
  system: "Labelled by Outflare",
};
// Where replies come from: Outflare's own mailboxes or a connected sequencer.
const sources: Record<string, string> = { mailbox: "Outflare mailboxes", emailbison: "EmailBison", instantly: "Instantly", smartlead: "Smartlead", lemlist: "lemlist", apollo: "Apollo" };
const sourceName = (s: string) => sources[s] ?? s;
const detailNames: Record<string, string> = { return_date: "Back on", referral_name: "Referred to", referral_email: "Their email", meeting_time: "Meeting", objection: "Objection" };

const tagTones: Record<string, string> = {
  gray: "border-border bg-muted text-muted-foreground",
  red: "border-red-600/25 bg-red-500/10 text-red-700 dark:text-red-400",
  orange: "border-orange-600/25 bg-orange-500/10 text-orange-700 dark:text-orange-400",
  amber: "border-amber-600/30 bg-amber-500/10 text-amber-700 dark:text-amber-400",
  green: "border-green-600/25 bg-green-500/10 text-green-700 dark:text-green-400",
  teal: "border-teal-600/25 bg-teal-500/10 text-teal-700 dark:text-teal-400",
  blue: "border-blue-600/25 bg-blue-500/10 text-blue-700 dark:text-blue-400",
  violet: "border-violet-600/25 bg-violet-500/10 text-violet-700 dark:text-violet-400",
  pink: "border-pink-600/25 bg-pink-500/10 text-pink-700 dark:text-pink-400",
};
const tagColors = Object.keys(tagTones) as InboxTag["color"][];

function Tag({ name, tags, children }: { name: string; tags: InboxTag[]; children?: ReactNode }) {
  const color = tags.find((t) => t.name === name)?.color ?? "gray";
  return <span className={`inline-flex h-5 items-center gap-1 rounded border px-1.5 font-mono text-[11px] ${tagTones[color]}`}>{name}{children}</span>;
}

function CategoryBadge({ m }: { m: InboundMessage }) {
  const c = categoryOf(m.category);
  if (!c) return null;
  const sure = m.category_confidence != null && m.category_confidence < 1 ? `, ${Math.round(m.category_confidence * 100)}% sure` : "";
  return <Badge tone={c.tone} title={m.category_source ? sourceLabel[m.category_source] + sure : undefined}>{c.label}</Badge>;
}

// Snooze choices, worked out in the reader's own time zone.
function snoozeTimes(): { label: string; at: Date }[] {
  const at9 = (days: number) => {
    const d = new Date();
    d.setDate(d.getDate() + days);
    d.setHours(9, 0, 0, 0);
    return d;
  };
  const monday = (8 - new Date().getDay()) % 7 || 7;
  return [
    { label: "For 3 hours", at: new Date(Date.now() + 3 * 3600_000) },
    { label: "Until tomorrow, 9am", at: at9(1) },
    { label: "Until Monday, 9am", at: at9(monday) },
    { label: "For a week", at: at9(7) },
    { label: "For a month", at: at9(30) },
  ];
}

type Kind = "" | "reply" | "auto_reply" | "bounce" | "other" | "all";
const kinds: [Kind, string][] = [["", "Replies"], ["auto_reply", "Auto-replies"], ["bounce", "Bounces"], ["other", "Other mail"], ["all", "Everything"]];
const kindLabel: Record<string, string> = { auto_reply: "Auto-reply", bounce: "Bounce", other: "Other" };

type Props = { ws: string; canManage: boolean; open?: string; setOpen: (id: string | undefined) => void; onRead: () => void };

// The inbox's filters, as the API takes them; a saved view stores the same.
type Filters = { status: Status; kind: Kind; category: string; group: string; source: string; tag: string; assigned_to: string; unread: string; needs_review: string; q: string };
const noFilters: Filters = { status: "open", kind: "", category: "", group: "", source: "", tag: "", assigned_to: "", unread: "", needs_review: "", q: "" };

export function Inbox({ ws, canManage, open, setOpen, onRead }: Props) {
  const [list, setList] = useState<InboundMessage[] | null>(null);
  const [f, setF] = useState<Filters>(noFilters);
  const set = (patch: Partial<Filters>) => setF((cur) => ({ ...cur, ...patch }));
  const [members, setMembers] = useState<Member[]>([]);
  const [tags, setTags] = useState<InboxTag[]>([]);
  const [views, setViews] = useState<InboxView[]>([]);
  const [view, setView] = useState<InboxView | null>(null);
  const [managingTags, setManagingTags] = useState(false);
  const [managingRules, setManagingRules] = useState(false);

  const { query: brandQuery } = useBrand(ws);
  const load = useCallback(async () => {
    const query = { ...Object.fromEntries(Object.entries(f).filter(([, v]) => v !== "")), ...brandQuery };
    const { data } = await api.GET("/v1/workspaces/{ws}/inbox", { params: { path: { ws }, query } });
    if (data) setList(data.data);
  }, [ws, f, brandQuery.brand]); // eslint-disable-line react-hooks/exhaustive-deps

  const loadTags = useCallback(() => {
    api.GET("/v1/workspaces/{ws}/inbox-tags", { params: { path: { ws } } }).then(({ data }) => setTags(data?.data ?? []));
  }, [ws]);
  const loadViews = useCallback(() => {
    api.GET("/v1/workspaces/{ws}/inbox-views", { params: { path: { ws } } }).then(({ data }) => setViews(data?.data ?? []));
  }, [ws]);

  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/members", { params: { path: { ws } } }).then(({ data }) => setMembers(data?.data ?? []));
    loadTags();
    loadViews();
  }, [ws, loadTags, loadViews]);

  useEffect(() => {
    const t = setTimeout(load, 200);
    const poll = setInterval(load, 30000);
    return () => {
      clearTimeout(t);
      clearInterval(poll);
    };
  }, [load]);

  const changed = (Object.keys(noFilters) as (keyof Filters)[]).filter((k) => f[k] !== noFilters[k]);
  const filtered = changed.length > 0;
  const reset = () => { setF(noFilters); setView(null); };
  const emptyText = { open: "Nothing waiting. New replies show up here within two minutes of arriving.", snoozed: "Nothing snoozed.", done: "Nothing marked done yet.", all: "No replies yet. They show up here within two minutes of arriving." }[f.status];
  const shownSources = [...new Set(["mailbox", "emailbison", ...(list ?? []).map((m) => m.source)])];

  function openView(v: InboxView) {
    setView(v);
    setF({ ...noFilters, ...(v.filters as Partial<Filters>) });
  }
  async function saveView() {
    const r = await ask({
      title: "Save these filters as a view",
      body: "The view shows in the Views menu. Only you see it unless you share it with the workspace.",
      input: { label: "Name", placeholder: "Hot replies from EmailBison", maxLength: 60 },
      choices: [{ label: "Cancel", value: "cancel" }, { label: "Save for everyone", value: "shared" }, { label: "Save for me", value: "mine", variant: "primary" }],
    });
    if (!r?.value.trim()) return;
    const filters = Object.fromEntries(changed.map((k) => [k, f[k]]));
    const { data, error } = await api.POST("/v1/workspaces/{ws}/inbox-views", { params: { path: { ws } }, body: { name: r.value.trim(), filters, shared: r.choice === "shared" } });
    if (error || !data) return toast(errorMessage(error), "error");
    setView(data);
    loadViews();
    toast(`Saved "${data.name}".`);
  }
  async function updateView() {
    if (!view) return;
    const filters = Object.fromEntries(changed.map((k) => [k, f[k]]));
    const { data, error } = await api.PATCH("/v1/workspaces/{ws}/inbox-views/{id}", { params: { path: { ws, id: view.id } }, body: { filters } });
    if (error || !data) return toast(errorMessage(error), "error");
    setView(data);
    loadViews();
    toast(`Updated "${data.name}".`);
  }
  async function deleteView() {
    if (!view || !(await confirmAction(`Delete the view "${view.name}"?`, view.shared ? "It's shared, so it goes for everyone in the workspace. The replies stay." : "The replies stay.", "Delete view"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/inbox-views/{id}", { params: { path: { ws, id: view.id } } });
    if (error) return toast(errorMessage(error), "error");
    setView(null);
    loadViews();
  }

  return (
    <div>
      <div className={open ? "hidden md:block" : ""}>
        <PageHeader title="Inbox" description="Replies from every mailbox and sequencer, labelled by what they mean. Answer them, mark them done or snooze them." />
      </div>
      <div className="grid gap-6 md:grid-cols-[24rem_1fr]">
        <div className={open ? "hidden md:block" : ""}>
          <Card
            title={view ? view.name : "Messages"}
            action={
              <div className="flex gap-1">
                <Menu
                  label="Views"
                  items={[
                    ...views.map((v) => ({ label: v.shared ? v.name : `${v.name} (only you)`, onClick: () => openView(v) })),
                    filtered && { label: "Save these filters…", onClick: saveView },
                    !!view && filtered && { label: `Update "${view.name}"`, onClick: updateView },
                    !!view && { label: "Show everything", onClick: reset },
                    !!view && { label: `Delete "${view.name}"`, onClick: deleteView, danger: true },
                  ]}
                />
                {canManage && <Button variant="ghost" onClick={() => setManagingTags(true)}>Tags</Button>}
                {canManage && <Button variant="ghost" onClick={() => setManagingRules(true)}>Rules</Button>}
              </div>
            }
          >
            <Tabs value={f.status} onValueChange={(v) => set({ status: v as Status })} className="mb-2">
              <TabsList className="h-8 w-full">
                {(["open", "snoozed", "done", "all"] as const).map((s) => <TabsTrigger key={s} value={s} className="px-2.5 text-[12px] capitalize">{s}</TabsTrigger>)}
              </TabsList>
            </Tabs>
            <div role="group" aria-label="Label group" className="mb-2 flex gap-1">
              {[{ id: "", label: "All", help: "Every label" }, ...groups].map((g) => (
                <button
                  key={g.id}
                  title={g.help}
                  aria-pressed={f.group === g.id}
                  onClick={() => set({ group: g.id })}
                  className={`h-7 flex-1 rounded-md border px-2 text-[12px] ${f.group === g.id ? "border-foreground/30 bg-accent font-medium" : "border-border text-muted-foreground hover:text-foreground"}`}
                >
                  {g.label}
                </button>
              ))}
            </div>
            <div className="mb-2 grid grid-cols-2 gap-2 text-sm">
              <select aria-label="Label" className={`${field} min-w-0`} value={f.category} onChange={(e) => set({ category: e.target.value })}>
                <option value="">Any label</option>
                <option value="none">Not labelled yet</option>
                {categories.map((c) => <option key={c.id} value={c.id}>{c.label}</option>)}
              </select>
              <select aria-label="Assigned to" className={`${field} min-w-0`} value={f.assigned_to} onChange={(e) => set({ assigned_to: e.target.value })}>
                <option value="">Anyone</option>
                <option value="me">Assigned to me</option>
                <option value="none">Unassigned</option>
                {members.map((m) => <option key={m.user_id} value={m.user_id ?? ""}>{m.name || m.email}</option>)}
              </select>
              <select aria-label="Source" className={`${field} min-w-0`} value={f.source} onChange={(e) => set({ source: e.target.value })}>
                <option value="">Every source</option>
                {shownSources.map((s) => <option key={s} value={s}>{sourceName(s)}</option>)}
              </select>
              <select aria-label="Tag" className={`${field} min-w-0`} value={f.tag} onChange={(e) => set({ tag: e.target.value })}>
                <option value="">Any tag</option>
                {tags.map((t) => <option key={t.id} value={t.name}>{t.name}</option>)}
              </select>
            </div>
            <div className="mb-3 flex flex-wrap items-center gap-2 text-sm">
              <select aria-label="Show" className={field} value={f.kind} onChange={(e) => set({ kind: e.target.value as Kind })}>
                {kinds.map(([k, label]) => <option key={k} value={k}>{label}</option>)}
              </select>
              <label className="flex items-center gap-1"><input type="checkbox" checked={f.unread === "true"} onChange={(e) => set({ unread: e.target.checked ? "true" : "" })} />Unread only</label>
              <label className="flex items-center gap-1" title="Labels the AI wasn't sure of, and opt-outs to check"><input type="checkbox" checked={f.needs_review === "true"} onChange={(e) => set({ needs_review: e.target.checked ? "true" : "" })} />Needs review</label>
              <input type="search" aria-label="Search messages" className={`${field} w-full`} placeholder="Search sender, subject or text" value={f.q} onChange={(e) => set({ q: e.target.value })} />
            </div>
            {list === null && <Loading />}
            {list?.length === 0 && (
              <p className="py-6 text-center text-sm text-muted-foreground">
                {changed.length === 1 && changed[0] === "status" ? emptyText : filtered ? <>Nothing matches. <button className="underline" onClick={reset}>Clear the filters</button></> : emptyText}
              </p>
            )}
            <ul className="-mx-4 -mb-4 text-sm">
              {list?.map((m) => (
                <li key={m.id}>
                  <button onClick={() => setOpen(m.id)} aria-current={open === m.id ? "true" : undefined} className={`w-full border-t border-border px-4 py-2.5 text-left hover:bg-accent/50 ${open === m.id ? "bg-accent" : ""}`}>
                    <div className="flex items-center justify-between gap-2">
                      <span className={`flex min-w-0 items-center gap-1.5 ${m.read ? "" : "font-semibold"}`}>
                        {!m.read && <span className="h-2 w-2 shrink-0 rounded-full bg-brand" aria-label="Unread" />}
                        <span className="truncate">{m.lead_name || m.from_name || m.from}</span>
                      </span>
                      <span className="shrink-0 text-xs text-muted-foreground" title={stamp(m.received_at)}>{listTime(m.received_at)}</span>
                    </div>
                    <div className={`truncate ${m.read ? "text-muted-foreground" : ""}`}>{m.subject || "(no subject)"}</div>
                    <div className="mt-0.5 flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
                      <CategoryBadge m={m} />
                      {m.needs_review && <Badge tone="amber" title="A person should check this label">Review</Badge>}
                      {kindLabel[m.kind] && !m.category && <Badge>{kindLabel[m.kind]}</Badge>}
                      {m.source !== "mailbox" && <Badge title="Where this reply came from">{sourceName(m.source)}</Badge>}
                      {m.snoozed_until && new Date(m.snoozed_until) > new Date() && <Badge title={stamp(m.snoozed_until)}>Snoozed</Badge>}
                      {m.assignee_name && <Badge title="Assigned to">{m.assignee_name}</Badge>}
                      {m.labels.map((l) => <Tag key={l} name={l} tags={tags} />)}
                      <span className="truncate">{[m.company, m.sequence_name].filter(Boolean).join(" · ")}</span>
                    </div>
                  </button>
                </li>
              ))}
            </ul>
          </Card>
        </div>
        {open ? (
          <Conversation key={open} ws={ws} id={open} canManage={canManage} members={members} tags={tags} onBack={() => setOpen(undefined)} onChange={() => { load(); loadTags(); }} onRead={onRead} />
        ) : (
          <div className="hidden md:block">{list && list.length > 0 && <Empty title="Pick a message to read it">The whole conversation shows here, and you can reply from the same mailbox.</Empty>}</div>
        )}
      </div>
      {managingTags && <TagsDialog ws={ws} tags={tags} onChange={() => { loadTags(); load(); }} onClose={() => setManagingTags(false)} />}
      {managingRules && <RulesDialog ws={ws} onClose={() => setManagingRules(false)} />}
    </div>
  );
}

function Conversation({ ws, id, canManage, members, tags, onBack, onChange, onRead }: { ws: string; id: string; canManage: boolean; members: Member[]; tags: InboxTag[]; onBack: () => void; onChange: () => void; onRead: () => void }) {
  const [msg, setMsg] = useState<InboundMessage | null>(null);
  const [missing, setMissing] = useState(false);
  const [thread, setThread] = useState<ThreadEntry[]>([]);
  const [body, setBody] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const path = { ws, id };

  const load = useCallback(async () => {
    const { data } = await api.GET("/v1/workspaces/{ws}/inbox/{id}", { params: { path: { ws, id } } });
    if (!data) return setMissing(true);
    setMsg(data.message);
    setThread(data.thread);
    if (!data.message.read && canManage) {
      await api.PATCH("/v1/workspaces/{ws}/inbox/{id}", { params: { path: { ws, id } }, body: { read: true } });
      onRead();
      onChange();
    }
  }, [ws, id, canManage, onChange, onRead]);

  useEffect(() => {
    load();
  }, [load]);

  async function reply(e?: FormEvent) {
    e?.preventDefault();
    if (!body.trim() || busy) return;
    setBusy(true);
    const { error } = await api.POST("/v1/workspaces/{ws}/inbox/{id}/reply", { params: { path }, body: { body } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    setError("");
    setBody("");
    toast("Reply sent.");
    load();
  }

  async function addReferral() {
    const { data, error } = await api.POST("/v1/workspaces/{ws}/inbox/{id}/referral", { params: { path }, body: {} });
    if (error) return toast(errorMessage(error), "error");
    toast(data.created ? `Added ${data.email} as a lead, referred by this reply.` : `${data.email} was already a lead; marked as referred by this reply.`);
  }
  async function markUnread() {
    await api.PATCH("/v1/workspaces/{ws}/inbox/{id}", { params: { path }, body: { read: false } });
    onChange();
    onBack();
  }

  type Patch = { category?: Category | null; assigned_to?: string | null; snoozed_until?: string | null; done?: boolean; labels?: string[]; needs_review?: boolean };
  async function triage(body: Patch, said?: string, close = false) {
    const { data, error } = await api.PATCH("/v1/workspaces/{ws}/inbox/{id}", { params: { path }, body });
    if (error || !data) return toast(errorMessage(error), "error");
    setMsg(data);
    onChange();
    if (said) toast(said);
    if (close) onBack();
  }

  // The breadcrumb names the sender, not the conversation's id.
  const crumb = msg ? msg.lead_name || msg.from_name || msg.from : "";
  useEffect(() => {
    if (crumb) window.dispatchEvent(new CustomEvent("outflare:item-name", { detail: { id, name: crumb } }));
  }, [id, crumb]);

  if (missing) return <Empty title="This message isn't here any more" action={<Button variant="ghost" onClick={onBack}>Back to inbox</Button>} />;
  if (!msg) return <Loading />;
  const name = msg.lead_name || msg.from_name || msg.from;
  const snoozed = msg.snoozed_until && new Date(msg.snoozed_until) > new Date();
  return (
    <div className="space-y-3">
      <button onClick={onBack} className="text-sm text-muted-foreground hover:text-foreground md:hidden">← Inbox</button>
      <Card
        title={msg.subject || "(no subject)"}
        description={<>{name} &lt;{msg.from}&gt;{msg.company && ` · ${msg.company}`}{msg.sequence_name && ` · ${msg.sequence_name}`}</>}
        action={
          canManage && (
            <div className="flex flex-wrap gap-2">
              {msg.done ? (
                <Button variant="ghost" onClick={() => triage({ done: false }, "Moved back to open.")}>Reopen</Button>
              ) : (
                <Button onClick={() => triage({ done: true, snoozed_until: null }, "Marked done.", true)}>Done</Button>
              )}
              <Menu
                label={snoozed ? "Snoozed" : "Snooze"}
                items={[
                  ...snoozeTimes().map((t) => ({ label: t.label, onClick: () => triage({ snoozed_until: t.at.toISOString() }, `Snoozed until ${t.at.toLocaleString(undefined, { weekday: "short", day: "numeric", month: "short", hour: "numeric", minute: "2-digit" })}.`, true) })),
                  !!snoozed && { label: "Wake it now", onClick: () => triage({ snoozed_until: null }, "Back in the open inbox.") },
                ]}
              />
              <Menu label="More" items={[{ label: "Mark unread", onClick: markUnread }]} />
            </div>
          )
        }
      >
        {canManage && msg.kind !== "bounce" && msg.kind !== "other" && (
          <div className="mb-4 flex flex-wrap items-center gap-2 border-b border-border pb-4 text-[13px]">
            <select aria-label="Label" className={field} value={msg.category ?? ""} onChange={(e) => triage({ category: (e.target.value || null) as Category | null })}>
              <option value="">Not labelled</option>
              {groups.map((g) => (
                <optgroup key={g.id} label={g.label}>
                  {categories.filter((c) => c.group === g.id).map((c) => <option key={c.id} value={c.id}>{c.label}</option>)}
                </optgroup>
              ))}
            </select>
            <select aria-label="Assigned to" className={field} value={msg.assigned_to ?? ""} onChange={(e) => triage({ assigned_to: e.target.value || null })}>
              <option value="">Unassigned</option>
              {members.map((m) => <option key={m.user_id} value={m.user_id ?? ""}>{m.name || m.email}</option>)}
            </select>
            <Labels labels={msg.labels} tags={tags} onChange={(labels) => triage({ labels })} />
            {msg.category_source && (
              <span className="text-[12px] text-muted-foreground">
                {sourceLabel[msg.category_source]}
                {msg.category_confidence != null && msg.category_confidence < 1 && `, ${Math.round(msg.category_confidence * 100)}% sure`}
                {msg.category_reason && `: ${msg.category_reason}`}
              </span>
            )}
          </div>
        )}
        {Object.keys(msg.details ?? {}).length > 0 && (
          <dl className="mb-4 flex flex-wrap gap-x-4 gap-y-1 text-[12px]">
            {Object.entries(msg.details).map(([k, v]) => (
              <div key={k} className="flex gap-1">
                <dt className="text-muted-foreground">{detailNames[k] ?? k}</dt>
                <dd>{String(v)}</dd>
              </div>
            ))}
          </dl>
        )}
        {canManage && !msg.needs_review && (msg.category === "referral" || msg.category === "wrong_person") && typeof msg.details?.referral_email === "string" && (
          <div className="mb-4">
            <Button variant="ghost" onClick={addReferral}>Add {String(msg.details.referral_name || msg.details.referral_email)} as a lead</Button>
          </div>
        )}
        {msg.needs_review && (
          <div className="mb-4 flex flex-wrap items-center justify-between gap-2 rounded-md bg-amber-50 p-2 text-sm text-amber-900 dark:bg-amber-950 dark:text-amber-200">
            <span>Check this label: {msg.category_source === "ai" ? "the AI wasn't sure of it" : "it was flagged for a person to look at"}. Pick another label above to change it.</span>
            {canManage && <Button variant="ghost" onClick={() => triage({ needs_review: false }, "Label confirmed.")}>The label is right</Button>}
          </div>
        )}
        {msg.source !== "mailbox" && <p className="mb-4 text-[12px] text-muted-foreground">This reply came from {sourceName(msg.source)}. Answering sends it through {sourceName(msg.source)}.</p>}
        {snoozed && <p className="mb-4 rounded-md bg-muted p-2 text-sm">Snoozed until {stamp(msg.snoozed_until!)}. It comes back to the open inbox then.</p>}
        {msg.opt_out && <p className="mb-4 rounded-md bg-amber-50 p-2 text-sm text-amber-900 dark:bg-amber-950 dark:text-amber-200">They asked to be removed, so they're on the suppression list and their sequences have stopped. If they didn't, pick another label above and the suppression is lifted.</p>}
        {msg.kind === "bounce" && <p className="mb-4 rounded-md bg-red-50 p-2 text-sm text-red-800 dark:bg-red-950 dark:text-red-200">A bounce report: the address it names was suppressed so it isn't emailed again.</p>}
        <ol className="space-y-3">
          {thread.map((t) => (
            <li key={t.id} className={`rounded-md border p-3 text-sm ${t.direction === "out" ? "ml-6 border-border bg-muted/40 sm:ml-12 " : "mr-6 border-border sm:mr-12"}`}>
              <div className="mb-1 flex flex-wrap justify-between gap-2 text-xs text-muted-foreground">
                <span>{t.direction === "out" ? `You, from ${t.from}` : t.from}</span>
                <span>{stamp(t.at)}</span>
              </div>
              <pre className="whitespace-pre-wrap break-words font-sans">{t.body}</pre>
            </li>
          ))}
        </ol>
        {canManage && msg.kind !== "bounce" && !msg.opt_out && (
          <form onSubmit={reply} className="mt-4 space-y-2">
            <label htmlFor="reply" className="block text-sm font-medium">Reply from {msg.mailbox_email}</label>
            <textarea
              id="reply"
              className={`${field} min-h-32 w-full [field-sizing:content]`}
              placeholder={`Write to ${name}…`}
              value={body}
              onChange={(e) => setBody(e.target.value)}
              onKeyDown={(e) => { if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) reply(); }}
            />
            <div className="flex flex-wrap items-center gap-3">
              <Button disabled={!body.trim() || busy}>{busy ? "Sending…" : "Send reply"}</Button>
              <span className="text-xs text-muted-foreground">Sends now, in the same thread. Ctrl or ⌘ + Enter to send.</span>
            </div>
            <ErrorText>{error}</ErrorText>
          </form>
        )}
      </Card>
    </div>
  );
}

function Labels({ labels, tags, onChange }: { labels: string[]; tags: InboxTag[]; onChange: (l: string[]) => void }) {
  const [text, setText] = useState("");
  function add(e: FormEvent) {
    e.preventDefault();
    const l = text.trim().toLowerCase();
    if (l && !labels.includes(l)) onChange([...labels, l]);
    setText("");
  }
  return (
    <form onSubmit={add} className="flex flex-wrap items-center gap-1.5">
      {labels.map((l) => (
        <Tag key={l} name={l} tags={tags}>
          <button type="button" aria-label={`Remove tag ${l}`} className="opacity-70 hover:opacity-100" onClick={() => onChange(labels.filter((x) => x !== l))}><X className="size-3" /></button>
        </Tag>
      ))}
      <input aria-label="Add a tag" list="inbox-tags" className={`${field} h-7 w-28`} maxLength={40} placeholder="Add tag" value={text} onChange={(e) => setText(e.target.value)} />
      <datalist id="inbox-tags">{tags.filter((t) => !labels.includes(t.name)).map((t) => <option key={t.id} value={t.name} />)}</datalist>
    </form>
  );
}

// The workspace's tags: add, rename, recolour or delete them. Renaming or
// deleting a tag changes every reply that carries it.
function TagsDialog({ ws, tags, onChange, onClose }: { ws: string; tags: InboxTag[]; onChange: () => void; onClose: () => void }) {
  const [name, setName] = useState("");
  const [error, setError] = useState("");

  async function create(e: FormEvent) {
    e.preventDefault();
    if (!name.trim()) return;
    const { error } = await api.POST("/v1/workspaces/{ws}/inbox-tags", { params: { path: { ws } }, body: { name: name.trim() } });
    if (error) return setError(errorMessage(error));
    setError("");
    setName("");
    onChange();
  }
  async function update(t: InboxTag, body: { name?: string; color?: InboxTag["color"] }) {
    if (body.name !== undefined && (!body.name.trim() || body.name.trim().toLowerCase() === t.name)) return;
    const { error } = await api.PATCH("/v1/workspaces/{ws}/inbox-tags/{id}", { params: { path: { ws, id: t.id } }, body });
    if (error) return setError(errorMessage(error));
    setError("");
    onChange();
  }
  async function remove(t: InboxTag) {
    const on = t.replies === 1 ? "1 reply" : `${t.replies} replies`;
    if (!(await confirmAction(`Delete the tag "${t.name}"?`, `It comes off ${on}. The replies stay.`, "Delete tag"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/inbox-tags/{id}", { params: { path: { ws, id: t.id } } });
    if (error) return setError(errorMessage(error));
    onChange();
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Tags</DialogTitle>
          <DialogDescription>Tags sort replies your own way. Renaming or deleting one changes every reply that carries it.</DialogDescription>
        </DialogHeader>
        <ul className="max-h-80 space-y-1.5 overflow-y-auto text-sm">
          {tags.length === 0 && <li className="py-2 text-muted-foreground">No tags yet. Add one below, or type one on a reply.</li>}
          {tags.map((t) => (
            <li key={t.id} className="flex items-center gap-2">
              <select aria-label={`Colour of ${t.name}`} className={`${field} w-24`} value={t.color} onChange={(e) => update(t, { color: e.target.value as InboxTag["color"] })}>
                {tagColors.map((c) => <option key={c} value={c}>{c}</option>)}
              </select>
              <input aria-label={`Name of ${t.name}`} className={`${field} min-w-0 flex-1 font-mono`} maxLength={40} defaultValue={t.name} onBlur={(e) => update(t, { name: e.target.value })} onKeyDown={(e) => e.key === "Enter" && e.currentTarget.blur()} />
              <span className="w-16 shrink-0 text-right text-xs text-muted-foreground">{t.replies} {t.replies === 1 ? "reply" : "replies"}</span>
              <button aria-label={`Delete ${t.name}`} className="flex size-8 shrink-0 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground" onClick={() => remove(t)}><X className="size-4" /></button>
            </li>
          ))}
        </ul>
        <form onSubmit={create} className="flex gap-2">
          <input aria-label="New tag" className={`${field} flex-1`} maxLength={40} placeholder="New tag" value={name} onChange={(e) => setName(e.target.value)} />
          <Button disabled={!name.trim()}>Add tag</Button>
        </form>
        <ErrorText>{error}</ErrorText>
      </DialogContent>
    </Dialog>
  );
}

// What each rule action does, in words.
const ruleChoices: Record<string, { id: string; label: string }[]> = {
  not_now: [
    { id: "snooze", label: "Snooze the reply until then" },
    { id: "restart", label: "Restart the sequence then" },
    { id: "nothing", label: "Do nothing" },
  ],
  out_of_office: [
    { id: "reschedule", label: "Wait until the day after they're back" },
    { id: "nothing", label: "Keep the sequence as it is" },
  ],
  referral: [
    { id: "suggest", label: "Offer to add them (one click)" },
    { id: "add", label: "Add them as a lead" },
    { id: "add_and_enroll", label: "Add them and enrol in a sequence" },
  ],
}
ruleChoices.wrong_person = ruleChoices.referral;

const ruleLabels: Record<string, string> = {
  not_now: "Not now",
  out_of_office: "Out of office",
  referral: "Referral",
  wrong_person: "Wrong person, names someone else",
};

type DealRule = { type: string; field: string; stage: string; closed?: string[] };
type ReplyRule = { action?: string; days?: number; sequence_id?: string; deal?: DealRule };
// Where deals live in the CRM: a record type with a stage (status) field
// and a reference to a person or company, so a reply can find its deal.
type DealSetup = { type: string; typeName: string; field: string; stages: { key: string; label: string }[] };

function RulesDialog({ ws, onClose }: { ws: string; onClose: () => void }) {
  const [rules, setRules] = useState<Record<string, ReplyRule> | null>(null);
  const [overrides, setOverrides] = useState(0);
  const [sequences, setSequences] = useState<{ id: string; name: string }[]>([]);
  const [deals, setDeals] = useState<DealSetup | null>(null);
  const [closed, setClosed] = useState<string[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    Promise.all([
      api.GET("/v1/workspaces/{ws}/reply-rules", { params: { path: { ws } } }),
      api.GET("/v1/workspaces/{ws}/sequences", { params: { path: { ws } } }),
      api.GET("/v1/workspaces/{ws}/catalog", { params: { path: { ws } } }),
    ]).then(([r, q, c]) => {
      if (r.error) return setError(errorMessage(r.error));
      const own = r.data.data.find((s) => !s.project_id && !s.sequence_id);
      const set = { ...(r.data.defaults as Record<string, ReplyRule>), ...((own?.rules ?? {}) as Record<string, ReplyRule>) };
      setRules(set);
      setOverrides(r.data.data.length - (own ? 1 : 0));
      if (q.data) setSequences(q.data.data.filter((s) => s.status !== "archived").map((s) => ({ id: s.id, name: s.name })));
      // The deal type the rules already use, else the first that fits.
      const used = Object.values(set).find((x) => x.deal)?.deal;
      const setups: DealSetup[] = [];
      for (const t of c.data?.types ?? []) {
        if (t.archived || t.key === "person" || t.key === "account") continue;
        const linked = t.fields.some((f) => !f.archived && f.value_type === "record_reference" && (f.target_type === "person" || f.target_type === "account"));
        for (const f of t.fields) {
          const options = c.data?.option_sets.find((o) => o.key === f.option_set)?.options.filter((o) => !o.archived);
          if (linked && !f.archived && f.value_type === "status" && options?.length) setups.push({ type: t.key, typeName: t.name, field: f.key, stages: options });
        }
      }
      const setup = setups.find((d) => used && d.type === used.type && d.field === used.field) ?? setups[0] ?? null;
      setDeals(setup);
      setClosed(used?.closed ?? setup?.stages.filter((o) => o.key === "won" || o.key === "lost").map((o) => o.key) ?? []);
    });
  }, [ws]);

  function moveTo(label: string, stage: string) {
    setRules((rs) => {
      const next = { ...rs!, [label]: { ...rs![label] } };
      if (stage && deals) next[label].deal = { type: deals.type, field: deals.field, stage };
      else delete next[label].deal;
      return next;
    });
  }

  function change(label: string, r: Partial<ReplyRule>) {
    setRules((rs) => {
      const next = { ...rs!, [label]: { ...rs![label], ...r } };
      if (next[label].action !== "add_and_enroll") delete next[label].sequence_id;
      return next;
    });
  }

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const body = Object.fromEntries(
      Object.entries(rules!)
        .filter(([l, r]) => ruleLabels[l] || r.deal)
        .map(([l, r]) => {
          const deal = r.deal && deals ? { deal: { ...r.deal, closed: closed.filter((c) => c !== r.deal!.stage) } } : {};
          if (!ruleLabels[l]) return [l, deal];
          const base = l === "not_now" ? { action: r.action, days: r.days } : { action: r.action, ...(r.sequence_id ? { sequence_id: r.sequence_id } : {}) };
          return [l, { ...base, ...deal }];
        }),
    );
    const { error } = await api.PUT("/v1/workspaces/{ws}/reply-rules", { params: { path: { ws } }, body: { rules: body } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    toast("Reply rules saved.");
    onClose();
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Reply rules</DialogTitle>
          <DialogDescription>
            What happens once a reply has a label Outflare is sure of. Labels the AI is unsure of wait for you first, and everything here can be undone.
          </DialogDescription>
        </DialogHeader>
        {!rules ? (
          error ? <ErrorText>{error}</ErrorText> : <Loading />
        ) : (
          <form onSubmit={save} className="space-y-4 text-sm">
            {Object.keys(ruleLabels).map((l) => (
              <div key={l} className="space-y-1.5">
                <label className="font-medium" htmlFor={`rule-${l}`}>{ruleLabels[l]}</label>
                <div className="flex flex-wrap gap-2">
                  <select id={`rule-${l}`} className={`${field} min-w-0 flex-1`} value={rules[l]?.action} onChange={(e) => change(l, { action: e.target.value })}>
                    {ruleChoices[l].map((c) => <option key={c.id} value={c.id}>{c.label}</option>)}
                  </select>
                  {l === "not_now" && rules[l]?.action !== "nothing" && (
                    <span className="flex items-center gap-1.5 text-muted-foreground">
                      <input aria-label="Days to wait" type="number" min={1} max={730} className={`${field} w-20`} value={rules[l]?.days ?? 90} onChange={(e) => change(l, { days: Number(e.target.value) })} />
                      days if no date
                    </span>
                  )}
                  {rules[l]?.action === "add_and_enroll" && (
                    <select aria-label={`Sequence for ${ruleLabels[l]}`} className={`${field} min-w-0 flex-1`} value={rules[l]?.sequence_id ?? ""} onChange={(e) => change(l, { sequence_id: e.target.value })}>
                      <option value="" disabled>Pick a sequence</option>
                      {sequences.map((q) => <option key={q.id} value={q.id}>{q.name}</option>)}
                    </select>
                  )}
                </div>
              </div>
            ))}
            <div className="space-y-2 border-t pt-4">
              <p className="font-medium">Deals</p>
              {!deals ? (
                <p className="text-muted-foreground">
                  To move deals from replies, your CRM needs a deal type with a stage field and a link to the person or company. Ask your agent, or set it up through the API (catalog).
                </p>
              ) : (
                <>
                  <p className="text-muted-foreground">
                    Open or move the reply's {deals.typeName.toLowerCase()}: the newest open one for the person or their company, or a new one. Deals only move forward, and never over a stage someone set.
                  </p>
                  {categories.filter((c) => c.group !== "automatic").map((c) => (
                    <div key={c.id} className="flex items-center gap-2">
                      <label className="w-36 shrink-0" htmlFor={`deal-${c.id}`}>{c.label}</label>
                      <select id={`deal-${c.id}`} className={`${field} min-w-0 flex-1`} value={rules[c.id]?.deal?.stage ?? ""} onChange={(e) => moveTo(c.id, e.target.value)}>
                        <option value="">Don't move</option>
                        {deals.stages.filter((o) => !closed.includes(o.key)).map((o) => <option key={o.key} value={o.key}>{o.label}</option>)}
                      </select>
                    </div>
                  ))}
                  <fieldset className="flex flex-wrap items-center gap-3">
                    <legend className="mb-1 text-muted-foreground">Closed stages, never moved out of:</legend>
                    {deals.stages.map((o) => (
                      <label key={o.key} className="flex items-center gap-1.5">
                        <input type="checkbox" checked={closed.includes(o.key)} onChange={(e) => setClosed(e.target.checked ? [...closed, o.key] : closed.filter((k) => k !== o.key))} />
                        {o.label}
                      </label>
                    ))}
                  </fieldset>
                </>
              )}
            </div>
            {overrides > 0 && <p className="text-[12px] text-muted-foreground">{overrides === 1 ? "A project or sequence has" : `${overrides} projects or sequences have`} its own rules too, set through the API or MCP, and those win for their replies.</p>}
            <ErrorText>{error}</ErrorText>
            <div className="flex justify-end gap-2">
              <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
              <Button disabled={busy}>{busy ? "Saving…" : "Save rules"}</Button>
            </div>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}
