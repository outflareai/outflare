// Broadcasts: one lifecycle email to many people on customer records at
// once, such as a newsletter or a launch. A draft picks a published
// template and a topic; sending makes one email per person and skips the
// people who chose not to get it, with why.

import { useCallback, useEffect, useState, type FormEvent } from "react";
import type { EmailBroadcast, EmailBroadcastRecipient, EmailTemplate, EmailTopic } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { useBrand } from "@/lib/brand";
import { ExclusionPicker, noExclusion, type Exclusion } from "@/components/exclusion-picker";
import { Badge, Button, Card, confirmAction, Empty, ErrorText, field, Input, Loading, PageHeader, Stat, Stats, toast } from "../ui";
import { ago, plural, stamp } from "../format";
import { DataTable, type ColumnDef } from "@/components/data-table";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Facts } from "./Emails";
import type { Go } from "./Lifecycle";

type Status = EmailBroadcast["status"];
const statusTone: Record<Status, "green" | "grey" | "amber" | "red" | "blue"> = { draft: "grey", scheduled: "blue", sending: "amber", sent: "green", cancelled: "grey", failed: "red" };
const statusText: Record<Status, string> = { draft: "Draft", scheduled: "Scheduled", sending: "Sending", sent: "Sent", cancelled: "Cancelled", failed: "Failed" };

function StatusBadge({ b }: { b: EmailBroadcast }) {
  return <Badge tone={statusTone[b.status]}>{statusText[b.status]}</Badge>;
}

const pct = (n: number, of: number) => (of ? `${Math.round((n / of) * 100)}%` : "—");

function audienceText(b: EmailBroadcast) {
  if (b.audience.kind === "records") return plural(b.audience.record_ids.length, "chosen person", "chosen people");
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

export function Broadcasts({ ws, canManage, open, onOpen, go }: { ws: string; canManage: boolean; open: string; onOpen: (id: string) => void; go: Go }) {
  const [list, setList] = useState<EmailBroadcast[] | null>(null);
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState("");
  const { query: brandQuery } = useBrand(ws);
  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/email-broadcasts", { params: { path: { ws }, query: brandQuery } });
    if (error) setError(errorMessage(error));
    setList(data?.data ?? []);
  }, [ws, brandQuery.brand]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (!open) load();
  }, [load, open]);

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
          A broadcast sends a published lifecycle template to the people on your customer records: everyone in a topic, such as your newsletter. Write the email as a template first.
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
      {creating && <BroadcastDialog ws={ws} onClose={() => setCreating(false)} onSaved={(b) => { setCreating(false); onOpen(b.id); }} />}
    </div>
  );
}

function BroadcastDetail({ ws, id, canManage, back, go }: { ws: string; id: string; canManage: boolean; back: () => void; go: Go }) {
  const [b, setB] = useState<EmailBroadcast | null>(null);
  const [skipped, setSkipped] = useState<EmailBroadcastRecipient[]>([]);
  const [editing, setEditing] = useState(false);
  const [scheduling, setScheduling] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/email-broadcasts/{id}", { params: { path: { ws, id } } });
    if (error) return setError(errorMessage(error));
    setB(data!);
    if (data!.skipped > 0) {
      const r = await api.GET("/v1/workspaces/{ws}/email-broadcasts/{id}/recipients", { params: { path: { ws, id }, query: { skipped: "true", limit: 200 } } });
      setSkipped(r.data?.data ?? []);
    }
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
      <>It goes to up to {plural(n, "person", "people")}{at ? `, at ${stamp(at)}` : ""}. People who left {b!.topic ? b!.topic.name : "marketing email"} are skipped.</>,
      at ? "Schedule" : "Send now", false);
    if (!ok) return;
    setBusy(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/email-broadcasts/{id}/send", { params: { path: { ws, id } }, body: at ? { send_at: at } : {} });
    setBusy(false);
    if (error || !data) return toast(errorMessage(error), "error");
    setScheduling(false);
    setB(data);
    toast(at ? `Scheduled for ${stamp(at)}.` : "Sending. The numbers update as it goes.");
  }
  async function cancel() {
    if (!(await confirmAction("Cancel this broadcast?", "Emails that have not gone yet are cancelled. This can't be undone.", "Cancel broadcast"))) return;
    const { data, error } = await api.POST("/v1/workspaces/{ws}/email-broadcasts/{id}/cancel", { params: { path: { ws, id } } });
    if (error || !data) return toast(errorMessage(error), "error");
    setB(data);
    toast("Cancelled.");
  }

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
            ["Audience", audienceText(b)],
            ["From", b.from || "The template's sender"],
            ["Started", b.started_at ? stamp(b.started_at) : "Not yet"],
            ...(b.finished_at ? [["Finished", stamp(b.finished_at)] as [string, React.ReactNode]] : []),
          ]} />
        </Card>
        <Card title="Who gets it">
          <div className="space-y-2 text-[13px] text-muted-foreground">
            <p>People on your customer records with a verified email address{b.topic ? `, in ${b.topic.name}` : ""}. Each gets their own copy, filled in with their own fields (such as {"{{person.first_name}}"}).</p>
            <p>Skipped: anyone who left {b.topic ? "this topic" : "marketing email"} or all marketing email, anyone who never joined an opt-in topic, and prospects from lead lists who never agreed to marketing email.</p>
          </div>
        </Card>
      </div>
      {skipped.length > 0 && (
        <Card title={`Skipped (${b.skipped})`}>
          <ul className="divide-y text-[13px]">
            {skipped.map((r) => (
              <li key={r.record_id} className="flex flex-wrap items-baseline justify-between gap-2 py-2">
                <span className="min-w-0 truncate">{r.name || r.address}</span>
                <span className="text-muted-foreground">{r.skipped}</span>
              </li>
            ))}
          </ul>
        </Card>
      )}
      {editing && <BroadcastDialog ws={ws} broadcast={b} onClose={() => setEditing(false)} onSaved={(v) => { setEditing(false); setB(v); }} />}
      {scheduling && <ScheduleDialog onClose={() => setScheduling(false)} onPick={(at) => send(at)} />}
    </div>
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

function BroadcastDialog({ ws, broadcast, onClose, onSaved }: { ws: string; broadcast?: EmailBroadcast; onClose: () => void; onSaved: (b: EmailBroadcast) => void }) {
  const [templates, setTemplates] = useState<EmailTemplate[] | null>(null);
  const [topics, setTopics] = useState<EmailTopic[]>([]);
  const [name, setName] = useState(broadcast?.name ?? "");
  const [template, setTemplate] = useState(broadcast?.template.id ?? "");
  const [topic, setTopic] = useState<string | undefined>(broadcast ? broadcast.topic?.key ?? "" : undefined);
  const [exclude, setExclude] = useState<Exclusion>(broadcast?.exclude ?? noExclusion);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/email-templates", { params: { path: { ws } } }).then(({ data }) =>
      setTemplates((data?.data ?? []).filter((t) => t.purpose === "lifecycle" && t.status === "active")),
    );
    api.GET("/v1/workspaces/{ws}/email-topics", { params: { path: { ws }, query: { status: "active" } } }).then(({ data }) => setTopics(data?.data ?? []));
  }, [ws]);
  const chosen = templates?.find((t) => t.id === template);
  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const body = { name, template, exclude, ...(topic !== undefined ? { topic: topic || null } : {}) };
    const res = broadcast
      ? await api.PATCH("/v1/workspaces/{ws}/email-broadcasts/{id}", { params: { path: { ws, id: broadcast.id } }, body })
      : await api.POST("/v1/workspaces/{ws}/email-broadcasts", { params: { path: { ws } }, body });
    setBusy(false);
    if (res.error || !res.data) return setError(errorMessage(res.error));
    toast(broadcast ? "Saved." : "Draft saved. Check the audience, then send it.");
    onSaved(res.data);
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{broadcast ? "Edit the broadcast" : "New broadcast"}</DialogTitle>
          <DialogDescription>Nothing is sent until you press Send.</DialogDescription>
        </DialogHeader>
        {!templates ? <Loading /> : templates.length === 0 ? (
          <p className="text-[13px] text-muted-foreground">You need a lifecycle template first: write the email under Templates, set its kind to lifecycle, and publish it.</p>
        ) : (
          <form onSubmit={save} className="space-y-3">
            <Input label="Name" value={name} onChange={(e) => setName(e.target.value)} placeholder="October newsletter" maxLength={200} required autoFocus />
            <label className="block space-y-1 text-[13px]">
              <span className="font-medium">Template</span>
              <select aria-label="Template" className={`${field} w-full`} value={template} onChange={(e) => { setTemplate(e.target.value); setTopic(undefined); }} required>
                <option value="">Choose a template</option>
                {templates.map((t) => <option key={t.id} value={t.id}>{t.name}{t.published_version ? "" : " (not published yet)"}</option>)}
              </select>
            </label>
            {chosen && !chosen.published_version && <p className="text-[12px] text-amber-700 dark:text-amber-400">Publish this template before sending.</p>}
            <label className="block space-y-1 text-[13px]">
              <span className="font-medium">Topic</span>
              <select aria-label="Topic" className={`${field} w-full`} value={topic ?? chosen?.topic?.key ?? ""} onChange={(e) => setTopic(e.target.value)}>
                <option value="">No topic: everyone who gets marketing email</option>
                {topics.map((t) => <option key={t.id} value={t.key}>{t.name}</option>)}
              </select>
              <span className="block text-[12px] text-muted-foreground">Who it goes to, and what the unsubscribe link leaves. The template's topic unless you pick another.</span>
            </label>
            <ExclusionPicker ws={ws} value={exclude} onChange={setExclude} what="Skips" />
            <ErrorText>{error}</ErrorText>
            <div className="flex justify-end gap-2">
              <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
              <Button disabled={busy || !name.trim() || !template}>{broadcast ? "Save" : "Save draft"}</Button>
            </div>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}
