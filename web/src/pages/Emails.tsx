// Sent emails: every lifecycle and transactional email, newest first, with
// filters; one email's facts, trigger, timeline and content; and one
// recipient's whole history.

import { useCallback, useEffect, useState, type FormEvent, type ReactNode } from "react";
import type { Email, EmailRecipient, EmailStatus, EmailTopic } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { useBrand } from "@/lib/brand";
import { Badge, Button, Card, confirmAction, Empty, field, Loading, Mono, PageHeader, toast } from "../ui";
import { ago, plural, stamp } from "../format";
import { appPath } from "@/lib/paths";
import { cn } from "@/lib/utils";
import { ConnectorLogo } from "@/components/connector-logo";
import { DataTable, type ColumnDef } from "@/components/data-table";
import { EmailStatusBadge, type Go } from "./Lifecycle";

const connectorName: Record<string, string> = { amazon_ses: "Amazon SES", postmark: "Postmark", resend: "Resend" };

const statuses: EmailStatus[] = ["queued", "scheduled", "sending", "sent", "delivered", "delayed", "bounced", "complained", "suppressed", "failed", "cancelled"];

type Trigger = Email["trigger"]["kind"];
type Filters = { status: EmailStatus | ""; purpose: "" | "transactional" | "lifecycle"; to: string; test: "" | "true" | "false"; trigger: Trigger | ""; topic: string; broadcast: string };
const noFilters: Filters = { status: "", purpose: "", to: "", test: "", trigger: "", topic: "", broadcast: "" };

// What sent an email, in a few words: "API", "Web app", "MCP agent", "Test".
const surfaceName: Record<string, string> = { web: "Web app", api: "API", mcp: "MCP agent", mission: "Mission", cli: "CLI", sdk: "SDK" };
const triggerName: Record<Trigger, string> = { api: "API call", test: "Test send", event: "Event", journey: "Journey", broadcast: "Broadcast", system: "Outflare" };
export function sentBy(t: Email["trigger"]) {
  if (t.kind === "api") return t.surface ? surfaceName[t.surface] ?? t.surface : "API";
  return triggerName[t.kind];
}

// The address's part of the path that opens its history.
export const recipientItem = (address: string) => `to:${address.toLowerCase()}`;

function Engagement({ e }: { e: Email }) {
  if (!e.track_opens && !e.track_clicks && !e.opens && !e.clicks) return <span className="text-muted-foreground" title="Tracking is off for this email">—</span>;
  const parts = [
    (e.track_opens || e.opens > 0) && (e.opens ? "Opened" : "Not opened"),
    (e.track_clicks || e.clicks > 0) && (e.clicks ? "Clicked" : "No clicks"),
  ].filter(Boolean);
  return <span className={cn("whitespace-nowrap", e.opens || e.clicks ? "" : "text-muted-foreground")}>{parts.join(" · ")}</span>;
}

const columns: ColumnDef<Email, unknown>[] = [
  {
    id: "subject", header: "Email", enableHiding: false, accessorFn: (e) => e.subject,
    cell: ({ row: { original: e } }) => (
      <span className="block min-w-0 max-w-[28rem]">
        <span className="block truncate font-medium">{e.subject}</span>
        <span className="block truncate text-[12px] text-muted-foreground">{e.to.join(", ")}</span>
      </span>
    ),
  },
  { id: "status", header: "Status", accessorFn: (e) => e.status, cell: ({ row: { original: e } }) => <span className="inline-flex items-center gap-1.5"><EmailStatusBadge status={e.status} />{e.test && <Badge tone="grey">test</Badge>}</span> },
  { id: "engagement", header: "Activity", accessorFn: (e) => e.opens + e.clicks, cell: ({ row: { original: e } }) => <Engagement e={e} /> },
  { id: "trigger", header: "Sent by", accessorFn: (e) => sentBy(e.trigger), cell: ({ row: { original: e } }) => <span className="whitespace-nowrap">{sentBy(e.trigger)}</span> },
  { id: "template", header: "Template", accessorFn: (e) => e.template_handle ?? "", cell: ({ row: { original: e } }) => e.template_handle ? <span className="font-mono text-[12px]">{e.template_handle}{e.template_version ? ` v${e.template_version}` : ""}</span> : <span className="text-muted-foreground">—</span> },
  { id: "purpose", header: "Kind", accessorFn: (e) => e.purpose, cell: ({ row: { original: e } }) => <span className="capitalize">{e.purpose}</span> },
  { id: "provider", header: "Provider", accessorFn: (e) => e.connector ?? "", cell: ({ row: { original: e } }) => e.connector ? <ConnectorLogo id={e.connector} /> : <span className="text-muted-foreground">—</span> },
  { id: "created_at", header: "When", accessorFn: (e) => e.created_at, cell: ({ row: { original: e } }) => <span className="whitespace-nowrap text-muted-foreground" title={stamp(e.created_at)}>{ago(e.created_at)}</span> },
];

export function Emails({ ws, canSend, admin, open, onOpen, go }: { ws: string; canSend: boolean; admin: boolean; open: string; onOpen: (id: string) => void; go: Go }) {
  // topic:<key> and broadcast:<id> open the list filtered to one topic or
  // broadcast, from those pages.
  const listed = open.startsWith("topic:") || open.startsWith("broadcast:");
  const [filters, setFilters] = useState<Filters>(() =>
    open.startsWith("topic:") ? { ...noFilters, topic: open.slice(6) } : open.startsWith("broadcast:") ? { ...noFilters, broadcast: open.slice(10) } : noFilters,
  );
  const [topics, setTopics] = useState<EmailTopic[]>([]);
  const [to, setTo] = useState("");
  useEffect(() => {
    if (listed) onOpen("");
  }, [listed, onOpen]);
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/email-topics", { params: { path: { ws } } }).then(({ data }) => setTopics(data?.data ?? []));
  }, [ws]);
  const [emails, setEmails] = useState<Email[] | null>(null);
  const [next, setNext] = useState<string | null>(null);
  const [more, setMore] = useState(false);

  const { query: brandQuery } = useBrand(ws);
  const fetchPage = useCallback(async (before?: string) => {
    const query = {
      limit: 50,
      ...brandQuery,
      ...(before ? { before } : {}),
      ...(filters.status ? { status: filters.status } : {}),
      ...(filters.purpose ? { purpose: filters.purpose } : {}),
      ...(filters.to ? { to: filters.to } : {}),
      ...(filters.test ? { test: filters.test === "true" } : {}),
      ...(filters.trigger ? { trigger: filters.trigger } : {}),
      ...(filters.topic ? { topic: filters.topic } : {}),
      ...(filters.broadcast ? { broadcast: filters.broadcast } : {}),
    };
    const { data } = await api.GET("/v1/workspaces/{ws}/emails", { params: { path: { ws }, query } });
    return data ?? { data: [], next_before: null };
  }, [ws, filters, brandQuery.brand]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    let live = true;
    setEmails(null);
    fetchPage().then((p) => {
      if (!live) return;
      setEmails(p.data);
      setNext(p.next_before ?? null);
    });
    return () => {
      live = false;
    };
  }, [fetchPage]);

  async function loadMore() {
    if (!next) return;
    setMore(true);
    const p = await fetchPage(next);
    setMore(false);
    setEmails((e) => [...(e ?? []), ...p.data]);
    setNext(p.next_before ?? null);
  }

  if (listed) return <Loading />;
  if (open.startsWith("to:")) return <Recipient ws={ws} address={open.slice(3)} back={() => onOpen("")} openEmail={onOpen} go={go} />;
  if (open) return <EmailDetail ws={ws} id={open} canSend={canSend} admin={admin} back={() => onOpen("")} openRecipient={(a) => onOpen(recipientItem(a))} go={go} />;

  const filtered = !!(filters.status || filters.purpose || filters.to || filters.test || filters.trigger || filters.topic || filters.broadcast);
  const searchTo = (e: FormEvent) => {
    e.preventDefault();
    setFilters((f) => ({ ...f, to: to.trim() }));
  };
  const toolbar = (
    <div className="flex w-full flex-wrap items-center gap-2">
      <form onSubmit={searchTo} className="min-w-0 flex-1 basis-56">
        <input type="search" aria-label="Recipient" placeholder="Recipient's email address" className={`${field} w-full`} value={to} onChange={(e) => { setTo(e.target.value); if (!e.target.value) setFilters((f) => ({ ...f, to: "" })); }} />
      </form>
      <select aria-label="Status" className={field} value={filters.status} onChange={(e) => setFilters((f) => ({ ...f, status: e.target.value as Filters["status"] }))}>
        <option value="">Any status</option>
        {statuses.map((s) => <option key={s} value={s}>{s[0].toUpperCase() + s.slice(1)}</option>)}
      </select>
      <select aria-label="Kind" className={field} value={filters.purpose} onChange={(e) => setFilters((f) => ({ ...f, purpose: e.target.value as Filters["purpose"] }))}>
        <option value="">Any kind</option>
        <option value="transactional">Transactional</option>
        <option value="lifecycle">Lifecycle</option>
      </select>
      <select aria-label="Test emails" className={field} value={filters.test} onChange={(e) => setFilters((f) => ({ ...f, test: e.target.value as Filters["test"] }))}>
        <option value="">Real and test</option>
        <option value="false">Real only</option>
        <option value="true">Tests only</option>
      </select>
      <select aria-label="Sent by" className={field} value={filters.trigger} onChange={(e) => setFilters((f) => ({ ...f, trigger: e.target.value as Filters["trigger"] }))}>
        <option value="">Sent by anything</option>
        <option value="api">API calls</option>
        <option value="test">Test sends</option>
        <option value="broadcast">Broadcasts</option>
        <option value="journey">Journeys</option>
      </select>
      {topics.length > 0 && (
        <select aria-label="Topic" className={field} value={filters.topic} onChange={(e) => setFilters((f) => ({ ...f, topic: e.target.value }))}>
          <option value="">Any topic</option>
          {topics.map((t) => <option key={t.id} value={t.key}>{t.name}</option>)}
        </select>
      )}
      {filtered && <Button variant="ghost" onClick={() => { setTo(""); setFilters(noFilters); }}>Clear</Button>}
      {filters.to && <Button variant="ghost" onClick={() => onOpen(recipientItem(filters.to))}>See {filters.to}'s history</Button>}
    </div>
  );

  return (
    <div className="space-y-6">
      <PageHeader
        title="Sent emails"
        description="Every receipt, reset and onboarding email Outflare has sent through your provider, with what happened to it."
        action={<Button variant="ghost" onClick={() => go("templates")}>Templates</Button>}
      />
      {emails === null ? <Loading /> : emails.length === 0 && !filtered ? (
        <Empty title="No emails yet" action={<Button onClick={() => go("lifecycle")}>Get set up</Button>}>
          Emails your app or your agent sends show here, with deliveries, bounces and complaints as they come back from the provider.
        </Empty>
      ) : (
        <>
          <DataTable
            columns={columns}
            data={emails}
            rowId={(e) => e.id}
            onRowClick={(e) => onOpen(e.id)}
            toolbar={toolbar}
            hidden={["purpose", "template"]}
            empty="No emails match these filters."
            mobile={(e) => (
              <div className="flex min-w-0 items-center gap-3">
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-[13px] font-medium">{e.subject}</span>
                  <span className="block truncate text-[12px] text-muted-foreground">{e.to.join(", ")} · {ago(e.created_at)}</span>
                </span>
                <EmailStatusBadge status={e.status} />
              </div>
            )}
          />
          {next && <div className="flex justify-center"><Button variant="ghost" onClick={loadMore} disabled={more}>{more ? "Loading…" : "Load older emails"}</Button></div>}
        </>
      )}
    </div>
  );
}

const eventText: Record<string, string> = {
  queued: "Queued", scheduled: "Scheduled", sent: "Handed to the provider", delivered: "Delivered", delayed: "Delayed by the receiving server",
  bounced: "Bounced", complained: "Marked as spam", opened: "Opened", clicked: "Link clicked", suppressed: "Not sent to this person",
  retrying: "Provider busy, trying again", failed: "Failed", cancelled: "Cancelled", unsubscribed: "Unsubscribed", preferences_changed: "Changed their email preferences",
  opened_automatic: "Opened by a scanner or privacy proxy", clicked_automatic: "Link checked by a scanner",
};
const eventDot: Record<string, string> = { opened: "bg-sky-500", clicked: "bg-sky-500", delivered: "bg-green-500", bounced: "bg-red-500", complained: "bg-red-500", failed: "bg-red-500", suppressed: "bg-amber-500", delayed: "bg-amber-500", retrying: "bg-amber-500", unsubscribed: "bg-amber-500" };

function EmailDetail({ ws, id, canSend, admin, back, openRecipient, go }: { ws: string; id: string; canSend: boolean; admin: boolean; back: () => void; openRecipient: (address: string) => void; go: Go }) {
  const [email, setEmail] = useState<Email | null>(null);
  const [missing, setMissing] = useState(false);
  const [view, setView] = useState<"html" | "text">("html");

  const load = useCallback(async () => {
    const { data } = await api.GET("/v1/workspaces/{ws}/emails/{id}", { params: { path: { ws, id } } });
    if (data) setEmail(data);
    else setMissing(true);
  }, [ws, id]);
  useEffect(() => {
    load();
  }, [load]);
  // Until the provider has answered, look again: deliveries come back
  // within seconds.
  useEffect(() => {
    if (!email || !["queued", "sending", "sent", "delayed"].includes(email.status)) return;
    const t = setInterval(load, email.status === "sent" ? 8000 : 3000);
    return () => clearInterval(t);
  }, [email, load]);

  if (missing) return <Empty title="Email not found" action={<Button onClick={back}>All sent emails</Button>}>It may have been sent from another workspace.</Empty>;
  if (!email) return <Loading />;
  const e = email;

  async function cancel() {
    if (!(await confirmAction("Cancel this email?", "It has not been sent yet, and will not be.", "Cancel email"))) return;
    const { data, error } = await api.POST("/v1/workspaces/{ws}/emails/{id}/cancel", { params: { path: { ws, id } } });
    if (error || !data) return toast(errorMessage(error), "error");
    toast("Cancelled.");
    load();
  }

  const people = (list: string[]) => <span className="flex flex-col items-start">{list.map((a) => <button key={a} className="break-all text-left underline decoration-muted-foreground/50 underline-offset-2 hover:decoration-foreground" title={`Everything sent to ${a}`} onClick={() => openRecipient(a)}>{a}</button>)}</span>;
  const facts: [string, React.ReactNode][] = [
    ["From", e.from],
    ["To", people(e.to)],
    ...(e.cc.length ? [["Cc", people(e.cc)] as [string, React.ReactNode]] : []),
    ...(e.bcc.length ? [["Bcc", people(e.bcc)] as [string, React.ReactNode]] : []),
    ...(e.reply_to.length ? [["Reply to", e.reply_to.join(", ")] as [string, string]] : []),
    ["Kind", <span className="capitalize">{e.purpose}{e.test ? ", test" : ""}</span>],
    ["Template", e.template_handle ? <button className="font-mono text-[12px] underline" onClick={() => go("templates", e.template_handle!)}>{e.template_handle}{e.template_version ? ` v${e.template_version}` : " (draft)"}</button> : "None"],
    ...(e.purpose === "lifecycle" ? [["Topic", e.topic ? <button className="underline" onClick={() => go("emails", `topic:${e.topic!.key}`)}>{e.topic.name}</button> : "None"] as [string, ReactNode]] : []),
    ...(e.send_at ? [["Send at", stamp(e.send_at)] as [string, string]] : []),
    ["Provider", e.connector ? <span className="inline-flex items-center gap-1.5"><ConnectorLogo id={e.connector} />{connectorName[e.connector] ?? e.connector}{e.provider_message_id && <Mono value={e.provider_message_id} short />}</span> : "None"],
    ...(e.tags.length ? [["Tags", <span className="flex flex-wrap gap-1">{e.tags.map((t) => <Badge key={t}>{t}</Badge>)}</span>] as [string, React.ReactNode]] : []),
    ...(Object.keys(e.metadata).length ? [["Metadata", <code className="break-all font-mono text-[12px]">{JSON.stringify(e.metadata)}</code>] as [string, React.ReactNode]] : []),
    ["Email id", <Mono value={e.id} short />],
  ];

  return (
    <div className="space-y-6">
      <PageHeader
        back={{ label: "Sent emails", onClick: back }}
        title={e.subject}
        description={<span className="inline-flex flex-wrap items-center gap-2"><EmailStatusBadge status={e.status} />{e.status_reason && <span>{e.status_reason}</span>}{e.status === "scheduled" && e.send_at ? <span>Sends {stamp(e.send_at)}</span> : <span title={stamp(e.created_at)}>{ago(e.created_at)}</span>}</span>}
        action={canSend && (e.status === "queued" || e.status === "scheduled") && <Button variant="danger" onClick={cancel}>Cancel email</Button>}
      />
      {e.status === "suppressed" && (
        <p className="rounded-lg border border-amber-500/40 bg-amber-500/10 px-4 py-3 text-[13px]">
          This address is on your suppression list, usually after a hard bounce, a spam complaint or an unsubscribe, so nothing was sent. <button className="underline" onClick={() => go("suppression")}>See the suppression list</button>
        </p>
      )}
      {(e.status === "complained" || (e.status === "bounced" && e.bounce_type === "hard")) && (
        <p className="rounded-lg border border-red-500/40 bg-red-500/10 px-4 py-3 text-[13px]">
          {e.status === "complained" ? "The recipient marked this as spam" : "The address does not exist"}, so Outflare added it to your suppression list: nothing more goes to it, from here or from Sequences. <button className="underline" onClick={() => go("suppression")}>See the suppression list</button>
        </p>
      )}
      <div className="grid gap-6 lg:grid-cols-[1fr_20rem]">
        <section aria-label="Content" className="min-w-0 rounded-lg border bg-card shadow-xs">
          <header className="flex items-center gap-1 border-b px-2 py-1.5" role="tablist">
            {(["html", "text"] as const).map((v) => (
              <button key={v} role="tab" aria-selected={view === v} onClick={() => setView(v)} className={cn("h-8 rounded-md px-3 text-[13px]", view === v ? "bg-accent font-medium" : "text-muted-foreground hover:text-foreground")}>{v === "html" ? "Email" : "Plain text"}</button>
            ))}
          </header>
          {view === "html"
            ? <iframe title="Email content" sandbox="" srcDoc={e.html ?? ""} className="h-[32rem] w-full rounded-b-lg bg-white" />
            : <pre className="h-[32rem] overflow-auto whitespace-pre-wrap p-4 font-mono text-[12px]">{e.text}</pre>}
        </section>
        <div className="space-y-6">
          <Card title="What happened">
            <ol className="relative space-y-3 border-l pl-4">
              {(e.events ?? []).map((ev, i) => (
                <li key={i} className="relative text-[13px]">
                  <span aria-hidden="true" className={cn("absolute top-1.5 -left-[21px] size-2 rounded-full", eventDot[ev.type] ?? "bg-muted-foreground/50")} />
                  <span className="block font-medium">{eventText[ev.type] ?? ev.type}{ev.recipient && e.to.length > 1 ? `: ${ev.recipient}` : ""}</span>
                  {ev.detail && <span className="block break-words text-[12px] text-muted-foreground">{ev.detail}</span>}
                  <span className="block text-[12px] text-muted-foreground" title={stamp(ev.occurred_at)}>{stamp(ev.occurred_at)}</span>
                </li>
              ))}
            </ol>
          </Card>
          <TriggerCard e={e} ws={ws} admin={admin} go={go} />
          <ActivityCard e={e} go={go} />
          <Card title="Details">
            <dl className="space-y-2 text-[13px]">
              {facts.map(([k, v]) => (
                <div key={k} className="grid grid-cols-[5.5rem_1fr] gap-2">
                  <dt className="text-muted-foreground">{k}</dt>
                  <dd className="min-w-0 break-words">{v}</dd>
                </div>
              ))}
            </dl>
          </Card>
        </div>
      </div>
    </div>
  );
}

export function Facts({ rows }: { rows: [string, React.ReactNode][] }) {
  return (
    <dl className="space-y-2 text-[13px]">
      {rows.map(([k, v]) => (
        <div key={k} className="grid grid-cols-[5.5rem_1fr] gap-2">
          <dt className="text-muted-foreground">{k}</dt>
          <dd className="min-w-0 break-words">{v}</dd>
        </div>
      ))}
    </dl>
  );
}

const viaText: Record<string, string> = { web: "Sent from the web app", api: "A call to the API", mcp: "An AI agent, through MCP", mission: "A mission's agent", cli: "The command line", sdk: "Your app, through the SDK" };

// What sent the email, and a way to the API call on the Logs page.
function TriggerCard({ e, ws, admin, go }: { e: Email; ws: string; admin: boolean; go: Go }) {
  const t = e.trigger;
  const rows: [string, React.ReactNode][] = [
    ["Trigger", t.kind === "api" ? (viaText[t.surface ?? "api"] ?? "A call to the API") : triggerName[t.kind]],
    ...(t.actor_name ? [["By", t.actor_name] as [string, React.ReactNode]] : []),
    ...(t.token_id ? [["API token", admin ? <button className="underline" onClick={() => go("tokens", t.token_id!)}>{t.token_name ?? "Token"}</button> : t.token_name ?? "Token"] as [string, React.ReactNode]] : []),
    ...(t.mission_id ? [["Mission", <button className="underline" onClick={() => go("missions", t.mission_id!)}>Open the mission</button>] as [string, React.ReactNode]] : []),
    ...(t.kind === "broadcast" && t.ref ? [["Broadcast", <button className="underline" onClick={() => go("broadcasts", t.ref!)}>Open the broadcast</button>] as [string, React.ReactNode]] : []),
    ...(t.kind === "journey" && t.ref ? [["Journey", <button className="underline" onClick={() => go("journeys", t.ref!)}>Open the journey</button>] as [string, React.ReactNode]] : []),
    ...(t.ref && t.kind !== "broadcast" && t.kind !== "journey" ? [["From", <Mono value={t.ref} short />] as [string, React.ReactNode]] : []),
    ...(t.request_id ? [["Request", admin
      ? <a className="underline" href={`${appPath(ws, "logs")}?request=${encodeURIComponent(t.request_id)}`}>See the call in Logs</a>
      : <Mono value={t.request_id} short />] as [string, React.ReactNode]] : []),
  ];
  return <Card title="Triggered by"><Facts rows={rows} /></Card>;
}

function ActivityCard({ e, go }: { e: Email; go: Go }) {
  const tracked = e.track_opens || e.track_clicks;
  const rows: [string, React.ReactNode][] = [
    ["Opens", e.track_opens || e.opens ? (e.opens ? <span>{plural(e.opens, "open")}, first {ago(e.opened_at!)}</span> : "Not opened yet") : "Not tracked"],
    ["Clicks", e.track_clicks || e.clicks ? (e.clicks ? <span>{plural(e.clicks, "click")}, first {ago(e.clicked_at!)}</span> : "No clicks yet") : "Not tracked"],
  ];
  return (
    <Card title="Opens and clicks">
      <Facts rows={rows} />
      {!tracked && e.template_handle && <p className="mt-3 text-[12px] text-muted-foreground">Turn tracking on in the <button className="underline" onClick={() => go("templates", e.template_handle!)}>template</button> to count them.</p>}
      {e.track_opens && <p className="mt-3 text-[12px] text-muted-foreground">Opens are a rough signal: Apple Mail loads images for privacy, and some apps block them.</p>}
    </Card>
  );
}

const recipientText: Record<string, string> = { ...eventText, queued: "Queued", sent: "Sent" };

// Everything one address was sent, as one timeline.
function Recipient({ ws, address, back, openEmail, go }: { ws: string; address: string; back: () => void; openEmail: (id: string) => void; go: Go }) {
  const [r, setR] = useState<EmailRecipient | null>(null);
  const [error, setError] = useState("");
  const [more, setMore] = useState(false);
  useEffect(() => {
    setR(null);
    api.GET("/v1/workspaces/{ws}/email-recipients/{address}", { params: { path: { ws, address } } }).then(({ data, error }) => (data ? setR(data) : setError(errorMessage(error))));
  }, [ws, address]);
  async function loadMore() {
    if (!r?.next_before) return;
    setMore(true);
    const { data } = await api.GET("/v1/workspaces/{ws}/email-recipients/{address}", { params: { path: { ws, address }, query: { before: r.next_before } } });
    setMore(false);
    if (data) setR({ ...data, events: [...r.events, ...data.events] });
  }
  if (error) return <Empty title="Could not load this address" action={<Button onClick={back}>All sent emails</Button>}>{error}</Empty>;
  if (!r) return <Loading />;
  const c = r.counts;
  return (
    <div className="space-y-6">
      <PageHeader
        back={{ label: "Sent emails", onClick: back }}
        title={r.address}
        description={c.emails ? `${plural(c.emails, "email")} since ${stamp(r.first_sent_at!)}, the last ${ago(r.last_sent_at!)}. Test sends are left out of the numbers.` : "No emails sent to this address yet."}
        action={r.lead_id && <Button variant="ghost" title="This address is also a lead in Outreach" onClick={() => go("leads")}>Also a lead</Button>}
      />
      {r.suppressed && (
        <p className="rounded-lg border border-amber-500/40 bg-amber-500/10 px-4 py-3 text-[13px]">
          On your suppression list ({r.suppressed.reason}) since {stamp(r.suppressed.created_at)}, so lifecycle email skips this address{r.suppressed.reason === "unsubscribed" ? "; transactional email still reaches it" : ""}. <button className="underline" onClick={() => go("suppression")}>See the suppression list</button>
        </p>
      )}
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-5">
        {([["Sent", c.emails], ["Delivered", c.delivered], ["Opened", c.opened], ["Clicked", c.clicked], ["Bounced or spam", c.bounced + c.complained]] as const).map(([k, v]) => (
          <div key={k} className="rounded-lg border bg-card px-3 py-2.5 shadow-xs">
            <div className="text-[12px] text-muted-foreground">{k}</div>
            <div className="text-lg font-semibold tabular-nums">{v.toLocaleString()}</div>
          </div>
        ))}
      </div>
      <Card title="Timeline" description="Every email to this address and what happened to it, newest first. Open an email for its content.">
        {r.events.length === 0 ? <p className="text-[13px] text-muted-foreground">Nothing yet.</p> : (
          <ol className="relative space-y-3 border-l pl-4">
            {r.events.map((ev, i) => (
              <li key={i} className="relative text-[13px]">
                <span aria-hidden="true" className={cn("absolute top-1.5 -left-[21px] size-2 rounded-full", eventDot[ev.type] ?? "bg-muted-foreground/50")} />
                <span className="flex flex-wrap items-baseline gap-x-2">
                  <span className="font-medium">{recipientText[ev.type] ?? ev.type}</span>
                  <button className="min-w-0 truncate text-left underline decoration-muted-foreground/50 underline-offset-2 hover:decoration-foreground" onClick={() => openEmail(ev.email_id)}>{ev.subject}</button>
                  {ev.template_handle && <span className="font-mono text-[11px] text-muted-foreground">{ev.template_handle}</span>}
                  {ev.trigger === "test" && <Badge tone="grey">test</Badge>}
                </span>
                {ev.detail && <span className="block break-words text-[12px] text-muted-foreground">{ev.detail}</span>}
                <span className="block text-[12px] text-muted-foreground">{stamp(ev.occurred_at)}</span>
              </li>
            ))}
          </ol>
        )}
        {r.next_before && <Button variant="ghost" className="mt-3" onClick={loadMore} disabled={more}>{more ? "Loading…" : "Show older"}</Button>}
      </Card>
    </div>
  );
}
