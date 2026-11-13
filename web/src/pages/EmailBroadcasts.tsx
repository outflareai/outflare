import { PickBrand } from "@/components/pick-brand";
// Broadcasts: one lifecycle email to many people on customer records at
// once, such as a newsletter or a launch. A draft picks a published
// template and a topic; sending makes one email per person and skips the
// people who chose not to get it, with why.

import { useCallback, useEffect, useState, type FormEvent, type ReactNode } from "react";
import type { EmailBroadcast, EmailBroadcastRecipient, EmailTemplate, EmailTopic } from "@outflare/sdk";
import { UploadPeople } from "@/components/upload-people";
import { api, errorMessage } from "../api";
import { useBrand } from "@/lib/brand";
import { ExclusionPicker, noExclusion, type Exclusion } from "@/components/exclusion-picker";
import { Badge, Button, Card, confirmAction, Empty, ErrorText, field, Input, Loading, PageHeader, Stat, Stats, toast } from "../ui";
import { ago, plural, stamp } from "../format";
import { csvName, downloadCsv, toCsv } from "@/lib/csv";
import { track } from "@/lib/analytics";
import { DataTable, type ColumnDef } from "@/components/data-table";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Facts } from "./Emails";
import type { Go } from "./Lifecycle";
import { SendLift } from "@/components/send-lift";

type Status = EmailBroadcast["status"];
const statusTone: Record<Status, "green" | "grey" | "amber" | "red" | "blue"> = { draft: "grey", scheduled: "blue", sending: "amber", sent: "green", cancelled: "grey", failed: "red" };
const statusText: Record<Status, string> = { draft: "Draft", scheduled: "Scheduled", sending: "Sending", sent: "Sent", cancelled: "Cancelled", failed: "Failed" };

function StatusBadge({ b }: { b: EmailBroadcast }) {
  return <Badge tone={statusTone[b.status]}>{statusText[b.status]}</Badge>;
}

const pct = (n: number, of: number) => (of ? `${Math.round((n / of) * 100)}%` : "—");

function audienceText(b: EmailBroadcast, listName?: string) {
  if (b.audience.kind === "records") return plural(b.audience.record_ids.length, "chosen person", "chosen people");
  if (b.audience.kind === "list") return listName ? `Everyone in ${listName}` : "Everyone in a saved list";
  return b.topic ? `Everyone in ${b.topic.name}` : "Everyone who gets marketing email";
}

function when(b: EmailBroadcast) {
  const at = b.started_at ?? b.send_at ?? b.created_at;
  return <span className="whitespace-nowrap text-muted-foreground" title={stamp(at)}>{b.status === "scheduled" && b.send_at ? `for ${stamp(b.send_at)}` : ago(at)}</span>;
}

const columns: ColumnDef<EmailBroadcast>[] = [
  {
    id: "name", header: "Broadcast", accessorFn: (b) => b.name,
    cell: ({ row: { original: b } }) => (
      <span className="block min-w-0">
        <span className="block truncate font-medium">{b.name}</span>
        <span className="block truncate text-[12px] text-muted-foreground">{audienceText(b)}</span>
      </span>
    ),
  },
  { id: "status", header: "Status", accessorFn: (b) => b.status, cell: ({ row: { original: b } }) => <StatusBadge b={b} /> },
  { id: "sent", header: "Sent", accessorFn: (b) => b.stats.sent },
  { id: "opened", header: "Opened", accessorFn: (b) => (b.stats.sent ? b.stats.opened / b.stats.sent : 0), cell: ({ row: { original: b } }) => pct(b.stats.opened, b.stats.sent) },
  { id: "clicked", header: "Clicked", accessorFn: (b) => (b.stats.sent ? b.stats.clicked / b.stats.sent : 0), cell: ({ row: { original: b } }) => pct(b.stats.clicked, b.stats.sent) },
  { id: "when", header: "When", accessorFn: (b) => b.started_at ?? b.send_at ?? b.created_at, cell: ({ row: { original: b } }) => when(b) },
];

// Broadcasts belong to one brand (plans/brands/README.md): with all brands
// picked the page asks for one, and switching brands opens it afresh.
export function Broadcasts(props: { ws: string; canManage: boolean; open: string; onOpen: (id: string) => void; go: Go }) {
  const { brand, active } = useBrand(props.ws);
  if (!brand) return <PickBrand what="Broadcasts" brands={active.length} />;
  return <BroadcastsPage key={brand.id} {...props} />;
}

function BroadcastsPage({ ws, canManage, open, onOpen, go }: { ws: string; canManage: boolean; open: string; onOpen: (id: string) => void; go: Go }) {
  const brandId = useBrand(ws).brand?.id ?? "";
  const [list, setList] = useState<EmailBroadcast[] | null>(null);
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/email-broadcasts", { params: { path: { brand: brandId } } });
    if (error) setError(errorMessage(error));
    setList(data?.data ?? []);
  }, [ws, brandId]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (!open) load();
  }, [load, open]);

  if (creating) return <BroadcastEditor ws={ws} go={go} onClose={() => setCreating(false)} onSaved={(b) => { setCreating(false); onOpen(b.id); }} />;
  if (open) return <BroadcastDetail ws={ws} id={open} canManage={canManage} back={() => onOpen("")} go={go} />;
  if (!list) return <Loading />;
  const add = canManage && <Button onClick={() => setCreating(true)}>New broadcast</Button>;
  return (
    <div className="space-y-6">
      <PageHeader
        title="Broadcasts"
        description="Send one lifecycle email to many people at once, such as a newsletter or a launch. Each person gets their own copy, and anyone who left its topic or all marketing email is skipped."
        action={add}
      />
      <ErrorText>{error}</ErrorText>
      {list.length === 0 ? (
        <Empty title="No broadcasts yet" action={add}>
          A broadcast sends one of your email templates to many people: everyone subscribed to a topic, such as your newsletter, or the people in a list. Write the email under Templates first.
        </Empty>
      ) : (
        <DataTable
          columns={columns}
          data={list}
          rowId={(b) => b.id}
          onRowClick={(b) => onOpen(b.id)}
          searchPlaceholder="Search broadcasts"
          mobile={(b) => (
            <div className="flex min-w-0 items-center gap-3">
              <span className="min-w-0 flex-1">
                <span className="block truncate text-[13px] font-medium">{b.name}</span>
                <span className="block truncate text-[12px] text-muted-foreground">{b.stats.sent ? `${b.stats.sent} sent · ${pct(b.stats.opened, b.stats.sent)} opened` : audienceText(b)}</span>
              </span>
              <StatusBadge b={b} />
            </div>
          )}
        />
      )}
    </div>
  );
}

function BroadcastDetail({ ws, id, canManage, back, go }: { ws: string; id: string; canManage: boolean; back: () => void; go: Go }) {
  const brandId = useBrand(ws).brand?.id ?? "";
  const [b, setB] = useState<EmailBroadcast | null>(null);
  const [editing, setEditing] = useState(false);
  const [scheduling, setScheduling] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [listName, setListName] = useState<string>();
  const ref = b?.audience.kind === "list" ? b.audience.ref : null;
  useEffect(() => {
    if (!ref) return;
    // A list audience is a saved list or segment, or a lead list.
    api.GET("/v1/brands/{brand}/smart-lists/{id}", { params: { path: { brand: brandId, id: ref } } }).then(async ({ data }) => {
      if (data) return setListName(data.name);
      const lead = await api.GET("/v1/brands/{brand}/lead-lists/{id}", { params: { path: { brand: brandId, id: ref } } });
      setListName(lead.data?.name ?? "a deleted list");
    });
  }, [ref]);
  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/email-broadcasts/{id}", { params: { path: { brand: brandId, id } } });
    if (error) return setError(errorMessage(error));
    setB(data!);
  }, [ws, id]);
  useEffect(() => {
    load();
  }, [load]);
  // Keep the numbers moving while it sends.
  useEffect(() => {
    if (b?.status !== "sending" && b?.status !== "scheduled") return;
    const t = setInterval(load, 4000);
    return () => clearInterval(t);
  }, [b?.status, load]);

  if (error && !b) return <Empty title="Could not load this broadcast" action={<Button onClick={back}>All broadcasts</Button>}>{error}</Empty>;
  if (!b) return <Loading />;

  async function send(at?: string) {
    const n = b!.audience_size ?? 0;
    const ok = await confirmAction(at ? "Schedule this broadcast?" : "Send this broadcast now?",
      <>It goes to up to {plural(n, "person", "people")}{at ? `, at ${stamp(at)}` : ""}. {b!.service ? "It is a service message: only suppressed addresses (bounces, complaints, do not contact) are skipped." : `People who left ${b!.topic ? b!.topic.name : "marketing email"} are skipped.`}</>,
      at ? "Schedule" : "Send now", false);
    if (!ok) return;
    setBusy(true);
    const { data, error } = await api.POST("/v1/brands/{brand}/email-broadcasts/{id}/send", { params: { path: { brand: brandId, id } }, body: at ? { send_at: at } : {} });
    setBusy(false);
    if (error || !data) return toast(errorMessage(error), "error");
    setScheduling(false);
    setB(data);
    toast(at ? `Scheduled for ${stamp(at)}.` : "Sending. The numbers update as it goes.");
  }
  async function cancel() {
    if (!(await confirmAction("Cancel this broadcast?", "Emails that have not gone yet are cancelled. This can't be undone.", "Cancel broadcast"))) return;
    const { data, error } = await api.POST("/v1/brands/{brand}/email-broadcasts/{id}/cancel", { params: { path: { brand: brandId, id } } });
    if (error || !data) return toast(errorMessage(error), "error");
    setB(data);
    toast("Cancelled.");
  }

  if (editing) return <BroadcastEditor ws={ws} go={go} broadcast={b} onClose={() => setEditing(false)} onSaved={(v) => { setEditing(false); setB(v); }} />;
  const s = b.stats;
  const draft = b.status === "draft";
  return (
    <div className="space-y-6">
      <PageHeader
        back={{ label: "Broadcasts", onClick: back }}
        title={<span className="flex items-center gap-2">{b.name} <StatusBadge b={b} /></span>}
        description={b.status_reason ?? (b.status === "scheduled" && b.send_at ? `Goes out ${stamp(b.send_at)}.` : undefined)}
        action={canManage && (
          <div className="flex flex-wrap gap-2">
            {draft && <Button variant="ghost" onClick={() => setEditing(true)}>Edit</Button>}
            {draft && <Button variant="ghost" onClick={() => setScheduling(true)} disabled={busy}>Schedule</Button>}
            {draft && <Button onClick={() => send()} disabled={busy}>Send now</Button>}
            {(b.status === "scheduled" || b.status === "sending") && <Button variant="ghost" onClick={cancel}>Cancel</Button>}
          </div>
        )}
      />
      {s.emails === 0 && b.audience_size !== null ? (
        <Stats cols={2}>
          <Stat label="Audience" value={b.audience_size} sub={b.audience_excluded ? `people, before their choices; ${b.audience_excluded} left out by the exclusion` : "people, before their choices"} />
          <Stat label="Topic" value={b.topic?.name ?? "None"} sub={b.topic ? "people who left it are skipped" : "only people who left all marketing email are skipped"} />
        </Stats>
      ) : (
      <Stats cols={6}>
        {b.audience_size !== null ? <Stat label="Audience" value={b.audience_size} sub="before their choices" /> : <Stat label="Emails" value={s.emails} sub={b.skipped ? `${b.skipped} skipped` : undefined} />}
        <Stat label="Delivered" value={s.delivered} sub={pct(s.delivered, s.sent)} />
        <Stat label="Opened" value={s.opened} sub={pct(s.opened, s.sent)} />
        <Stat label="Clicked" value={s.clicked} sub={pct(s.clicked, s.sent)} />
        <Stat label="Unsubscribed" value={s.unsubscribed} sub={pct(s.unsubscribed, s.sent)} />
        <Stat label="Bounced" value={s.bounced} sub={pct(s.bounced, s.sent)} tone={s.sent && s.bounced / s.sent > 0.05 ? "red" : undefined} />
      </Stats>
      )}
      <div className="grid gap-4 lg:grid-cols-2">
        <Card title="What it sends" action={s.emails > 0 && <Button variant="ghost" onClick={() => go("emails", `broadcast:${b.id}`)}>See its emails</Button>}>
          <Facts rows={[
            ["Template", <button className="underline" onClick={() => go("templates", b.template.handle)}>{b.template.name}</button>],
            ["Version", b.template_version ? `v${b.template_version}` : "The published one, when it starts"],
            ["Topic", b.topic ? <button className="underline" onClick={() => go("topics")}>{b.topic.name}</button> : "None"],
            ["Kind", b.service ? "Service message (no marketing consent or unsubscribe link)" : "Marketing"],
            ["Audience", audienceText(b, listName)],
            ["From", b.from || "The template's sender"],
            ...(b.holdout_percent > 0 ? [["Held out", `${b.holdout_percent}% of the audience${b.held_out ? ` (${b.held_out} people)` : ""}, to measure what it adds`] as [string, React.ReactNode]] : []),
            ["Started", b.started_at ? stamp(b.started_at) : "Not yet"],
            ...(b.finished_at ? [["Finished", stamp(b.finished_at)] as [string, React.ReactNode]] : []),
          ]} />
        </Card>
        <Card title="Who gets it">
          {b.service ? (
          <div className="space-y-2 text-[13px] text-muted-foreground">
            <p>Everyone in the audience with an email address, imported ones included. Each gets their own copy, filled in with their own fields (such as {"{{person.first_name}}"}).</p>
            <p>It is a service message, so marketing choices don't apply and it has no unsubscribe link. Skipped: suppressed addresses (hard bounces, complaints, do not contact). Use it only for news about the service people use.</p>
          </div>
          ) : (
          <div className="space-y-2 text-[13px] text-muted-foreground">
            <p>People on your customer records with a verified email address{b.topic ? `, in ${b.topic.name}` : ""}. Each gets their own copy, filled in with their own fields (such as {"{{person.first_name}}"}).</p>
            <p>Skipped: anyone who left {b.topic ? "this topic" : "marketing email"} or all marketing email, anyone who never joined an opt-in topic, and prospects from lead lists who never agreed to marketing email.</p>
          </div>
          )}
        </Card>
      </div>
      {b.held_out > 0 && <SendLift brand={brandId} kind="email-broadcasts" id={b.id} />}
      {(s.emails > 0 || b.skipped > 0) && <Recipients brand={brandId} b={b} openEmail={(email) => go("emails", email)} />}
      {scheduling && <ScheduleDialog onClose={() => setScheduling(false)} onPick={(at) => send(at)} />}
    </div>
  );
}

// A time in a table cell: "Nov 11, 4:19 PM". The exact time is on hover.
const shortTime = (iso: string) => new Date(iso).toLocaleString(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" });

type Show = "" | "delivered" | "opened" | "clicked" | "bounced" | "complained" | "unsubscribed" | "not_sent" | "skipped";

// What one person's row says happened, in plain words.
function recipientStatus(r: EmailBroadcastRecipient): { text: string; tone: "green" | "grey" | "amber" | "red" | "blue"; why?: string | null; tip?: string } {
  if (r.skipped) return { text: "Skipped", tone: "grey", why: r.skipped[0].toUpperCase() + r.skipped.slice(1) };
  switch (r.status) {
    case "delivered": return { text: "Delivered", tone: "green", tip: "The receiving mail server accepted it." };
    case "sent": return { text: "Sent", tone: "blue", tip: "Handed to the email provider; waiting to hear it arrived." };
    case "delayed": return { text: "Delayed", tone: "amber", why: r.status_reason };
    case "bounced": return { text: r.bounce_type === "soft" ? "Bounced (temporary)" : "Bounced", tone: "red", why: r.status_reason };
    case "complained": return { text: "Marked as spam", tone: "red", why: r.status_reason };
    case "suppressed": return { text: "Not sent", tone: "grey", why: r.status_reason };
    case "failed": return { text: "Failed", tone: "red", why: r.status_reason };
    case "cancelled": return { text: "Cancelled", tone: "grey", why: r.status_reason };
    default: return { text: "Sending", tone: "amber" };
  }
}

// How a recipient's row reads in a download: the same words as the table.
const recipientColumns: ColumnDef<EmailBroadcastRecipient>[] = [
  {
    id: "person", header: "Person", accessorFn: (r) => r.name || r.address || "", meta: { csvHeader: "Name", csv: (r: EmailBroadcastRecipient) => r.name },
    cell: ({ row: { original: r } }) => (
      <span className="block max-w-64 min-w-0">
        <span className="block truncate font-medium">{r.name || r.address || "No name"}</span>
        {r.name && r.address && <span className="block truncate text-[12px] text-muted-foreground">{r.address}</span>}
      </span>
    ),
  },
  {
    id: "status", header: "Status", accessorFn: (r) => recipientStatus(r).text,
    cell: ({ row: { original: r } }) => {
      const st = recipientStatus(r);
      return (
        <span className="block max-w-80 min-w-52">
          <span className="flex flex-wrap gap-1">
            <Badge tone={st.tone} title={st.tip}>{st.text}</Badge>
            {r.unsubscribed_at && <Badge tone="amber">Unsubscribed</Badge>}
          </span>
          {st.why && <span className="mt-1 line-clamp-3 text-[12px] text-muted-foreground" title={st.why}>{st.why}</span>}
          {r.bounced_at && <span className="mt-1 block text-[12px] text-muted-foreground" title={stamp(r.bounced_at)}>Bounced {shortTime(r.bounced_at)}</span>}
          {r.unsubscribed_at && <span className="mt-1 block text-[12px] text-muted-foreground" title={stamp(r.unsubscribed_at)}>Unsubscribed {shortTime(r.unsubscribed_at)}</span>}
        </span>
      );
    },
  },
  { id: "sent", header: "Sent", accessorFn: (r) => r.sent_at, cell: ({ row: { original: r } }) => <At at={r.sent_at} /> },
  { id: "delivered", header: "Delivered", accessorFn: (r) => r.delivered_at, cell: ({ row: { original: r } }) => <At at={r.delivered_at} /> },
  { id: "opened", header: "Opened", accessorFn: (r) => r.opened_at, cell: ({ row: { original: r } }) => <At at={r.opened_at} times={r.opens} /> },
  { id: "clicked", header: "Clicked", accessorFn: (r) => r.clicked_at, cell: ({ row: { original: r } }) => <At at={r.clicked_at} times={r.clicks} /> },
  { id: "open", header: "", accessorFn: () => "", meta: { csv: false, align: "right" }, cell: ({ row: { original: r } }) => r.email_id && <span className="whitespace-nowrap text-[12px] text-muted-foreground group-hover:text-foreground">See email →</span> },
];

// A time something happened, short, with the exact time on hover.
function At({ at, times }: { at: string | null; times?: number }) {
  if (!at) return <span className="text-muted-foreground">–</span>;
  return (
    <span className="whitespace-nowrap" title={stamp(at)}>
      {shortTime(at)}
      {!!times && times > 1 && <span className="block text-[12px] text-muted-foreground">{times} times</span>}
    </span>
  );
}

const recipientCsv = ["Name", "Email", "Status", "Reason", "Sent", "Delivered", "First opened", "Opens", "First clicked", "Clicks", "Bounced", "Unsubscribed"];

// Recipients is everyone a broadcast reached for, one row each: what
// happened to their email and when. The groups are the people behind the
// numbers at the top of the page.
function Recipients({ brand, b, openEmail }: { brand: string; b: EmailBroadcast; openEmail: (id: string) => void }) {
  const s = b.stats;
  const [show, setShow] = useState<Show>("");
  const [q, setQ] = useState("");
  const [rows, setRows] = useState<EmailBroadcastRecipient[] | null>(null);
  const [next, setNext] = useState<string | null>(null);
  const [more, setMore] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const groups: { id: Show; label: string; n: number; hint: string }[] = [
    { id: "", label: "Everyone", n: s.emails + b.skipped, hint: "Everyone in the audience when it was sent" },
    { id: "delivered", label: "Delivered", n: s.delivered, hint: "The receiving mail server accepted the email" },
    { id: "opened", label: "Opened", n: s.opened, hint: "Opened at least once; mail scanners are not counted" },
    { id: "clicked", label: "Clicked", n: s.clicked, hint: "Clicked a link at least once" },
    { id: "bounced", label: "Bounced", n: s.bounced, hint: "The receiving mail server refused the email" },
    { id: "complained", label: "Marked as spam", n: s.complained, hint: "The person reported the email as spam" },
    { id: "unsubscribed", label: "Unsubscribed", n: s.unsubscribed, hint: "Unsubscribed from this email" },
    { id: "not_sent", label: "Not sent", n: s.not_sent, hint: "An email was made but never left: suppressed, failed or cancelled" },
    { id: "skipped", label: "Skipped", n: b.skipped, hint: "Left out before sending, with why" },
  ];
  const group = groups.find((g) => g.id === show)!;
  const query = (extra: { limit: number; cursor?: string }) => ({ params: { path: { brand, id: b.id }, query: { ...(show ? { show } : {}), ...(q.trim() ? { q: q.trim() } : {}), ...extra } } });
  // The first page follows the group, the search and the numbers, so it
  // fills in while the broadcast sends.
  const moving = `${s.emails}.${s.delivered}.${s.opened}.${s.clicked}.${s.bounced}.${s.complained}.${s.unsubscribed}.${s.not_sent}.${b.skipped}`;
  useEffect(() => {
    let stale = false;
    const t = setTimeout(async () => {
      const { data, error } = await api.GET("/v1/brands/{brand}/email-broadcasts/{id}/recipients", query({ limit: 50 }));
      if (stale) return;
      if (error || !data) return setError(errorMessage(error));
      setError("");
      setRows(data.data);
      setNext(data.next_cursor);
    }, 200);
    return () => { stale = true; clearTimeout(t); };
  }, [brand, b.id, show, q, moving]);
  async function loadMore() {
    if (!next) return;
    setMore(true);
    const { data, error } = await api.GET("/v1/brands/{brand}/email-broadcasts/{id}/recipients", query({ limit: 50, cursor: next }));
    setMore(false);
    if (error || !data) return setError(errorMessage(error));
    setRows((cur) => [...(cur ?? []), ...data.data]);
    setNext(data.next_cursor);
  }
  // The download is the whole group, not only the rows loaded so far.
  async function save() {
    setSaving(true);
    const all: EmailBroadcastRecipient[] = [];
    for (let cursor: string | undefined; ; ) {
      const { data, error } = await api.GET("/v1/brands/{brand}/email-broadcasts/{id}/recipients", query({ limit: 200, cursor }));
      if (error || !data) {
        setSaving(false);
        return toast(errorMessage(error), "error");
      }
      all.push(...data.data);
      if (!data.next_cursor) break;
      cursor = data.next_cursor;
    }
    setSaving(false);
    downloadCsv(csvName(b.name, group.label), toCsv(recipientCsv, all.map((r) => {
      const st = recipientStatus(r);
      return [r.name, r.address, r.unsubscribed_at ? `${st.text}, unsubscribed` : st.text, r.skipped ?? r.status_reason, r.sent_at, r.delivered_at, r.opened_at, r.opens, r.clicked_at, r.clicks, r.bounced_at, r.unsubscribed_at];
    })));
    track("ui.broadcast_recipients_downloaded", { group: show || "everyone", rows: all.length });
  }
  const total = q.trim() ? null : group.n;
  return (
    <Card title="Recipients" description="One row per person: what happened to their email, and when. Click a row to see the email.">
      <div className="space-y-3">
        <div className="flex flex-wrap gap-2" role="group" aria-label="Show">
          {groups.filter((g) => g.id === "" || g.n > 0 || g.id === show).map((g) => (
            <button key={g.id} type="button" title={g.hint} aria-pressed={show === g.id} onClick={() => setShow(g.id)}
              className={`inline-flex h-7 items-center gap-1.5 rounded-md border px-2.5 text-[12px] ${show === g.id ? "border-primary bg-primary text-primary-foreground" : "hover:bg-muted/50"}`}>
              {g.label} <span className={`font-mono tabular-nums ${show === g.id ? "" : "text-muted-foreground"}`}>{g.n.toLocaleString()}</span>
            </button>
          ))}
        </div>
        <ErrorText>{error}</ErrorText>
        {rows === null ? <Loading /> : (
          <DataTable
            columns={recipientColumns}
            data={rows}
            rowId={(r) => r.record_id}
            onRowClick={(r) => r.email_id && openEmail(r.email_id)}
            searchPlaceholder="Find a name or address"
            search={{ value: q, onChange: setQ }}
            columnsMenu={false}
            empty={show ? `Nobody is in ${group.label} yet.` : "Nobody yet."}
            toolbar={
              <Button variant="ghost" className="ml-auto" onClick={save} disabled={saving || rows.length === 0} title={`Save ${show ? group.label.toLowerCase() : "everyone"}${q.trim() ? " matching the search" : ""} as a CSV file`}>
                {saving ? "Preparing…" : "Download CSV"}
              </Button>
            }
            mobile={(r) => {
              const st = recipientStatus(r);
              return (
                <span className="block space-y-1">
                  <span className="flex items-center justify-between gap-2">
                    <span className="min-w-0 truncate font-medium">{r.name || r.address || "No name"}</span>
                    <Badge tone={st.tone}>{st.text}</Badge>
                  </span>
                  {r.name && r.address && <span className="block truncate text-[12px] text-muted-foreground">{r.address}</span>}
                  {st.why && <span className="block text-[12px] text-muted-foreground">{st.why}</span>}
                  <span className="block text-[12px] text-muted-foreground">
                    {[r.sent_at && `Sent ${shortTime(r.sent_at)}`, r.opened_at && `opened ${shortTime(r.opened_at)}`, r.clicked_at && `clicked ${shortTime(r.clicked_at)}`, r.unsubscribed_at && `unsubscribed ${shortTime(r.unsubscribed_at)}`].filter(Boolean).join(" · ")}
                  </span>
                </span>
              );
            }}
          />
        )}
        {rows !== null && rows.length > 0 && (
          <div className="flex flex-wrap items-center justify-between gap-2 text-[12px] text-muted-foreground">
            <span>Showing {rows.length.toLocaleString()}{total !== null && total >= rows.length ? ` of ${total.toLocaleString()}` : ""}, by name. Times are in your time zone.</span>
            {next && <Button variant="ghost" onClick={loadMore} disabled={more}>{more ? "Loading…" : "Show more"}</Button>}
          </div>
        )}
      </div>
    </Card>
  );
}

function ScheduleDialog({ onClose, onPick }: { onClose: () => void; onPick: (at: string) => void }) {
  const [at, setAt] = useState("");
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-sm">
        <DialogHeader>
          <DialogTitle>Schedule the broadcast</DialogTitle>
          <DialogDescription>In your time zone, within 30 days.</DialogDescription>
        </DialogHeader>
        <form onSubmit={(e) => { e.preventDefault(); onPick(new Date(at).toISOString()); }} className="space-y-3">
          <input type="datetime-local" aria-label="When" required className={`${field} w-full`} value={at} onChange={(e) => setAt(e.target.value)} />
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
            <Button disabled={!at}>Schedule</Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// A numbered part of the editor: one question, in the order people answer it.
function Step({ n, title, hint, children }: { n: number; title: string; hint?: ReactNode; children: ReactNode }) {
  return (
    <section className="rounded-lg border bg-card p-5 shadow-xs">
      <header className="mb-4 flex items-start gap-3">
        <span className="flex size-6 shrink-0 items-center justify-center rounded-full bg-primary font-mono text-[12px] text-primary-foreground">{n}</span>
        <div>
          <h2 className="text-[15px] font-semibold leading-6">{title}</h2>
          {hint && <p className="text-[13px] text-muted-foreground">{hint}</p>}
        </div>
      </header>
      <div className="space-y-3 text-[13px] sm:pl-9">{children}</div>
    </section>
  );
}

function Pick({ checked, onChange, title, body, name }: { checked: boolean; onChange: () => void; title: string; body: ReactNode; name: string }) {
  return (
    <label className={`flex cursor-pointer items-start gap-2.5 rounded-md border p-3 ${checked ? "border-primary bg-muted/50" : "hover:bg-muted/30"}`}>
      <input type="radio" name={name} className="mt-0.5" checked={checked} onChange={onChange} />
      <span className="min-w-0">
        <span className="block font-medium">{title}</span>
        <span className="block text-[12px] text-muted-foreground">{body}</span>
      </span>
    </label>
  );
}

// BroadcastEditor is the whole page for making or changing a draft, in the
// order people think about it: what kind of email, which email, who gets
// it, then its name. The email shows beside it as people will see it.
// Saving sends nothing: it opens the draft, which shows the audience count
// and has Send and Schedule.
function BroadcastEditor({ ws, broadcast, onClose, onSaved, go }: { ws: string; broadcast?: EmailBroadcast; onClose: () => void; onSaved: (b: EmailBroadcast) => void; go: Go }) {
  const brandId = useBrand(ws).brand?.id ?? "";
  const [templates, setTemplates] = useState<EmailTemplate[] | null>(null);
  const [topics, setTopics] = useState<EmailTopic[]>([]);
  const [name, setName] = useState(broadcast?.name ?? "");
  const [template, setTemplate] = useState(broadcast?.template.id ?? "");
  const [topic, setTopic] = useState<string | undefined>(broadcast ? broadcast.topic?.key ?? "" : undefined);
  const [exclude, setExclude] = useState<Exclusion>(broadcast?.exclude ?? noExclusion);
  const [holdout, setHoldout] = useState(broadcast?.holdout_percent ?? 0);
  const [service, setService] = useState<boolean | null>(broadcast ? broadcast.service : null);
  const [audKind, setAudKind] = useState<"topic" | "list" | "records" | "">(broadcast?.audience.kind ?? "");
  const [listRef, setListRef] = useState(broadcast?.audience.ref ?? "");
  // Every list of people the brand has: saved lists and segments, then
  // lead lists.
  const [lists, setLists] = useState<{ id: string; name: string; members: number; lead: boolean }[] | null>(null);
  const [uploading, setUploading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const loadLists = useCallback(
    async () => {
      const [smart, lead] = await Promise.all([
        api.GET("/v1/brands/{brand}/smart-lists", { params: { path: { brand: brandId } } }),
        api.GET("/v1/brands/{brand}/lead-lists", { params: { path: { brand: brandId } } }),
      ]);
      setLists([
        ...(smart.data?.data ?? []).filter((l) => l.record_type === "person").map((l) => ({ id: l.id, name: l.name, members: l.members, lead: false })),
        ...(lead.data?.data ?? []).map((l) => ({ id: l.id, name: l.name, members: l.leads, lead: true })),
      ]);
    },
    [brandId],
  );
  useEffect(() => {
    api.GET("/v1/brands/{brand}/email-templates", { params: { path: { brand: brandId } } }).then(({ data }) =>
      setTemplates((data?.data ?? []).filter((t) => t.status === "active")),
    );
    api.GET("/v1/brands/{brand}/email-topics", { params: { path: { brand: brandId }, query: { status: "active" } } }).then(({ data }) => setTopics(data?.data ?? []));
    loadLists();
  }, [brandId, loadLists]);

  const usable = service === null ? [] : (templates ?? []).filter((t) => t.purpose === (service ? "transactional" : "lifecycle"));
  const chosen = usable.find((t) => t.id === template);
  const list = lists?.find((l) => l.id === listRef);
  const topicKey = topic ?? chosen?.topic?.key ?? "";
  const topicName = topics.find((t) => t.key === topicKey)?.name;

  function setKind(svc: boolean) {
    if (svc === service) return;
    setService(svc);
    setTemplate("");
    setTopic(undefined);
    if (svc && audKind === "topic") setAudKind("list");
  }
  function pickTemplate(t: EmailTemplate) {
    // The broadcast takes the email's name until someone types their own.
    if (!name.trim() || name === chosen?.name) setName(t.name);
    setTemplate(t.id);
    setTopic(undefined);
  }

  const missing = service === null ? "Choose what kind of email it is." : !template ? "Choose the email to send." : !audKind ? "Choose who gets it." : audKind === "list" && !listRef ? "Choose a list." : !name.trim() ? "Give it a name." : "";

  async function save(e: FormEvent) {
    e.preventDefault();
    if (missing || service === null) return;
    setBusy(true);
    setError("");
    const audience =
      audKind === "list" ? { kind: "list" as const, ref: listRef } : audKind === "records" ? { kind: "records" as const, record_ids: broadcast?.audience.record_ids ?? [] } : { kind: "topic" as const };
    const body = {
      name: name.trim(), template, exclude, service, audience,
      holdout_percent: service ? 0 : holdout,
      ...(service ? { topic: null } : topic !== undefined ? { topic: topic || null } : {}),
    };
    const res = broadcast
      ? await api.PATCH("/v1/brands/{brand}/email-broadcasts/{id}", { params: { path: { brand: brandId, id: broadcast.id } }, body })
      : await api.POST("/v1/brands/{brand}/email-broadcasts", { params: { path: { brand: brandId } }, body });
    setBusy(false);
    if (res.error || !res.data) return setError(errorMessage(res.error));
    toast(broadcast ? "Saved." : "Draft saved. Nothing is sent until you press Send.");
    onSaved(res.data);
  }

  if (!templates || !lists) return <Loading />;
  const who =
    audKind === "list" ? (list ? `${plural(list.members, "person", "people")} in ${list.name}` : "A list you choose")
    : audKind === "records" ? plural(broadcast?.audience.record_ids.length ?? 0, "chosen person", "chosen people")
    : audKind === "topic" ? (topicName ? `Everyone subscribed to ${topicName}` : "Everyone who gets marketing email")
    : "Not chosen yet";
  return (
    <form onSubmit={save} className="space-y-6">
      <PageHeader
        back={{ label: broadcast ? broadcast.name : "Broadcasts", onClick: onClose }}
        title={broadcast ? "Edit the broadcast" : "New broadcast"}
        description="Four short steps. Nothing is sent from this page: you save a draft, check who it goes to, then press Send."
      />
      <div className="grid items-start gap-6 xl:grid-cols-[minmax(0,1fr)_minmax(0,30rem)]">
        <div className="space-y-4">
          <Step n={1} title="What kind of email is it?" hint="This decides who may get it and whether it has an unsubscribe link.">
            <div className="grid gap-2 sm:grid-cols-2">
              <Pick name="kind" checked={service === false} onChange={() => setKind(false)} title="Marketing" body="A newsletter, a launch or an offer. It has an unsubscribe link, and people who opted out of marketing are skipped." />
              <Pick name="kind" checked={service === true} onChange={() => setKind(true)} title="Service message" body="News people need about the service they use, such as a change or an end date. It goes to everyone in the list, with no unsubscribe link." />
            </div>
            {service && <p className="text-[12px] text-muted-foreground">Use this only for real service news, never for promotion. Addresses that bounced, complained or asked not to be contacted are still skipped.</p>}
          </Step>

          <Step n={2} title="Which email do you want to send?" hint={service === null ? "Answer step 1 first." : `Emails are written under Templates. These are your published ${service ? "service (transactional)" : "marketing (lifecycle)"} templates.`}>
            {service !== null && usable.length === 0 && (
              <div className="rounded-md border border-dashed p-4 text-muted-foreground">
                <p>You have no {service ? "service" : "marketing"} email to send yet. Write it under Templates, set its kind to {service ? "transactional" : "lifecycle"}, and publish it. Then come back here.</p>
                <Button type="button" variant="ghost" className="mt-3" onClick={() => go("templates")}>Go to Templates</Button>
              </div>
            )}
            {usable.length > 0 && (
              <div className="grid max-h-80 gap-2 overflow-y-auto">
                {usable.map((t) => (
                  <Pick key={t.id} name="template" checked={template === t.id} onChange={() => pickTemplate(t)} title={t.name} body={<>Subject: {t.subject || "(no subject)"}{t.published_version ? "" : " · not published yet"}</>} />
                ))}
              </div>
            )}
            {chosen && !chosen.published_version && <p className="text-[12px] text-amber-700 dark:text-amber-400">This email isn't published yet. You can save the draft, but publish the template before sending.</p>}
            {usable.length > 0 && <button type="button" className="text-[12px] text-muted-foreground underline hover:text-foreground" onClick={() => go("templates")}>Write or change an email in Templates</button>}
          </Step>

          <Step n={3} title="Who gets it?" hint={service ? "A service message goes to a list of people." : "Everyone subscribed to a topic, or the people in one of your lists."}>
            <div className="grid gap-2 sm:grid-cols-2">
              {!service && <Pick name="aud" checked={audKind === "topic"} onChange={() => setAudKind("topic")} title="Everyone subscribed to a topic" body="Such as everyone who gets your newsletter." />}
              <Pick name="aud" checked={audKind === "list"} onChange={() => setAudKind("list")} title="The people in a list" body="Any list from Lists (a smart list, a lead list or a segment), or a CSV you upload." />
              {broadcast?.audience.kind === "records" && <Pick name="aud" checked={audKind === "records"} onChange={() => setAudKind("records")} title="The people chosen before" body={plural(broadcast.audience.record_ids.length, "person", "people")} />}
            </div>
            {audKind === "topic" && (
              <label className="block space-y-1">
                <span className="font-medium">Topic</span>
                <select aria-label="Topic" className={`${field} w-full`} value={topicKey} onChange={(e) => setTopic(e.target.value)}>
                  <option value="">No topic: everyone who gets marketing email</option>
                  {topics.map((t) => <option key={t.id} value={t.key}>{t.name}</option>)}
                </select>
                <span className="block text-[12px] text-muted-foreground">The unsubscribe link in the email takes people out of this topic.</span>
              </label>
            )}
            {audKind === "list" && (
              <div className="space-y-2">
                {lists.length === 0 ? (
                  <p className="rounded-md border border-dashed p-4 text-muted-foreground">You have no lists of people yet. Upload a CSV to make one now, or build one under Lists.</p>
                ) : (
                  <label className="block space-y-1">
                    <span className="font-medium">List</span>
                    <select aria-label="List" className={`${field} w-full`} value={listRef} onChange={(e) => setListRef(e.target.value)}>
                      <option value="">Choose a list</option>
                      {lists.some((l) => !l.lead) && (
                        <optgroup label="Lists and segments">
                          {lists.filter((l) => !l.lead).map((l) => <option key={l.id} value={l.id}>{l.name} ({plural(l.members, "person", "people")})</option>)}
                        </optgroup>
                      )}
                      {lists.some((l) => l.lead) && (
                        <optgroup label="Lead lists">
                          {lists.filter((l) => l.lead).map((l) => <option key={l.id} value={l.id}>{l.name} ({plural(l.members, "person", "people")})</option>)}
                        </optgroup>
                      )}
                    </select>
                  </label>
                )}
                <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
                  <Button type="button" variant="ghost" onClick={() => setUploading(true)}>Upload a CSV of people</Button>
                  <button type="button" className="text-[12px] text-muted-foreground underline hover:text-foreground" onClick={() => go("lists")}>See or build lists</button>
                </div>
              </div>
            )}
            {audKind && (
              <details className="rounded-md border px-3 py-2" open={exclude.lists.length + exclude.tags.length > 0}>
                <summary className="cursor-pointer font-medium">Leave some people out <span className="font-normal text-muted-foreground">(optional)</span></summary>
                <div className="pt-3"><ExclusionPicker ws={ws} value={exclude} onChange={setExclude} what="Skips" /></div>
              </details>
            )}
          </Step>

          <Step n={4} title="Name it" hint="Only your team sees the name. It's how you'll find this broadcast later.">
            <Input label="Name" value={name} onChange={(e) => setName(e.target.value)} placeholder={service ? "Change to your plan" : "October newsletter"} maxLength={200} />
            {service === false && (
              <details className="rounded-md border px-3 py-2" open={holdout > 0 || (audKind === "list" && topic !== undefined)}>
                <summary className="cursor-pointer font-medium">More options <span className="font-normal text-muted-foreground">(optional)</span></summary>
                <div className="space-y-3 pt-3">
                  {audKind !== "topic" && (
                    <label className="block space-y-1">
                      <span className="font-medium">Unsubscribe topic</span>
                      <select aria-label="Unsubscribe topic" className={`${field} w-full`} value={topicKey} onChange={(e) => setTopic(e.target.value)}>
                        <option value="">No topic: unsubscribing leaves all marketing email</option>
                        {topics.map((t) => <option key={t.id} value={t.key}>{t.name}</option>)}
                      </select>
                      <span className="block text-[12px] text-muted-foreground">What the unsubscribe link takes people out of. The email's own topic unless you pick another.</span>
                    </label>
                  )}
                  <label className="block space-y-1">
                    <span className="font-medium">Hold some people back to measure it</span>
                    <select aria-label="Hold out" className={`${field} w-full`} value={holdout} onChange={(e) => setHoldout(Number(e.target.value))}>
                      {[0, 5, 10, 20, 50].map((n) => <option key={n} value={n}>{n === 0 ? "Nobody: send to everyone" : `${n}% of the audience`}</option>)}
                    </select>
                    <span className="block text-[12px] text-muted-foreground">People picked at random who don't get it, so you can see what the email added.</span>
                  </label>
                </div>
              </details>
            )}
          </Step>

          <ErrorText>{error}</ErrorText>
          <div className="flex flex-wrap items-center justify-end gap-3">
            {missing && <span className="text-[12px] text-muted-foreground">{missing}</span>}
            <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
            <Button disabled={busy || !!missing}>{busy ? "Saving…" : broadcast ? "Save" : "Save draft and review"}</Button>
          </div>
        </div>

        <aside className="space-y-4 xl:sticky xl:top-4">
          <section aria-label="Summary" className="rounded-lg border bg-card p-4 shadow-xs">
            <h2 className="mb-2 text-[13px] font-semibold">Summary</h2>
            <Facts rows={[
              ["Kind", service === null ? "Not chosen yet" : service ? "Service message" : "Marketing"],
              ["Email", chosen?.name ?? "Not chosen yet"],
              ["Goes to", who],
              ...(service === false ? [["Unsubscribe", topicName ? `Leaves ${topicName}` : "Leaves all marketing email"] as [string, ReactNode]] : []),
              ...(service ? [["Unsubscribe", "No link: it is a service message"] as [string, ReactNode]] : []),
            ]} />
          </section>
          <TemplateLook brand={brandId} template={chosen} />
        </aside>
      </div>
      {uploading && <UploadPeople brand={brandId} onClose={() => setUploading(false)} onDone={(l) => { setUploading(false); loadLists(); setListRef(l.id); }} />}
    </form>
  );
}

// TemplateLook shows the chosen email as people will get it, filled with
// its example values.
function TemplateLook({ brand, template }: { brand: string; template?: EmailTemplate }) {
  const [out, setOut] = useState<{ id: string; subject: string; html: string } | null>(null);
  const id = template?.id;
  useEffect(() => {
    if (!template) return;
    let live = true;
    api.POST("/v1/brands/{brand}/email-templates/preview", {
      params: { path: { brand } },
      body: { subject: template.subject, html: template.html, text: template.text, variables: template.variables },
    }).then(({ data }) => {
      if (live && data) setOut({ id: template.id, subject: data.subject, html: data.html });
    });
    return () => { live = false; };
  }, [brand, id]); // eslint-disable-line react-hooks/exhaustive-deps
  const shown = out && out.id === id ? out : null;
  return (
    <section aria-label="Preview" className="rounded-lg border bg-card shadow-xs">
      <header className="border-b px-4 py-2.5">
        <h2 className="text-[13px] font-semibold">Preview</h2>
        <p className="truncate text-[12px] text-muted-foreground">{template ? `Subject: ${shown?.subject ?? template.subject}` : "Choose an email in step 2 to see it here."}</p>
      </header>
      {template && (
        <div className="p-3">
          <div className="overflow-hidden rounded-md border bg-white">
            <iframe title="Email preview" sandbox="" srcDoc={shown?.html ?? ""} className="h-[32rem] w-full" />
          </div>
        </div>
      )}
    </section>
  );
}
