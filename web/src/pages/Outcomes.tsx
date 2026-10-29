import { useCallback, useEffect, useMemo, useState, type FormEvent, type ReactNode } from "react";
import type { Deal, Meeting, MeetingStatus, OutcomeRow, OutcomeSync, OutcomesReport, RecurringSpend, Sequence, SpendCategory, SpendEntry, SpendRate } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, confirmAction, Empty, ErrorText, field, Loading, Menu, PageHeader, Stat, Stats, Status, toast } from "../ui";
import { ago, plural } from "../format";
import { Icon } from "../icons";
import type { Tab } from "../App";
import { DataTable } from "@/components/data-table";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useBrand } from "@/lib/brand";
import { connection } from "@/lib/status";

// Outcomes: what outreach produced (meetings, deals) and what it cost, so
// every sequence and brand has a cost per meeting.

type Period = "7d" | "30d" | "90d" | "month" | "last_month" | "quarter";
const periods: { id: Period; label: string }[] = [
  { id: "7d", label: "Last 7 days" },
  { id: "30d", label: "Last 30 days" },
  { id: "90d", label: "Last 90 days" },
  { id: "month", label: "This month" },
  { id: "last_month", label: "Last month" },
  { id: "quarter", label: "This quarter" },
];

const day = (d: Date) => d.toISOString().slice(0, 10);
const shortDate = (iso: string) => new Date(iso).toLocaleDateString(undefined, { dateStyle: "medium" });

// Units are stored as typed ("credit" or "credits"); show them in the right number.
function unitWord(n: number, unit: string) {
  const one = unit.endsWith("s") && !unit.endsWith("ss") ? unit.slice(0, -1) : unit;
  return n === 1 ? one : one ? `${one}s` : "units";
}

// The period as from and to dates; to is inclusive, as the API reads a date.
function range(p: Period): { from: string; to: string } {
  const now = new Date();
  const today = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate()));
  const back = (n: number) => new Date(today.getTime() - (n - 1) * 86400000);
  switch (p) {
    case "7d":
      return { from: day(back(7)), to: day(today) };
    case "30d":
      return { from: day(back(30)), to: day(today) };
    case "90d":
      return { from: day(back(90)), to: day(today) };
    case "month":
      return { from: day(new Date(Date.UTC(today.getUTCFullYear(), today.getUTCMonth(), 1))), to: day(today) };
    case "last_month": {
      const first = new Date(Date.UTC(today.getUTCFullYear(), today.getUTCMonth() - 1, 1));
      const last = new Date(Date.UTC(today.getUTCFullYear(), today.getUTCMonth(), 0));
      return { from: day(first), to: day(last) };
    }
    case "quarter":
      return { from: day(new Date(Date.UTC(today.getUTCFullYear(), Math.floor(today.getUTCMonth() / 3) * 3, 1))), to: day(today) };
  }
}

const currencies = ["USD", "AUD", "EUR", "GBP", "CAD", "NZD"];

function money(cents: number | null | undefined, currency: string, exact = false) {
  if (cents === null || cents === undefined) return "—";
  return new Intl.NumberFormat(undefined, { style: "currency", currency, maximumFractionDigits: exact || Math.abs(cents) < 10000 ? 2 : 0 }).format(cents / 100);
}

const categoryLabel: Record<SpendCategory, string> = {
  data: "Lead data",
  verification: "Verification",
  mailboxes: "Mailboxes",
  domains: "Domains",
  sending: "Sending tools",
  software: "Software",
  people: "People",
  ads: "Ads",
  other: "Other",
};
const categoryList = Object.keys(categoryLabel) as SpendCategory[];

const statusLabel: Record<MeetingStatus, string> = { booked: "Booked", held: "Held", no_show: "No-show", canceled: "Canceled" };
const statusTone: Record<MeetingStatus, "grey" | "green" | "amber" | "red"> = { booked: "grey", held: "green", no_show: "amber", canceled: "red" };
const sourceLabel: Record<string, string> = {
  google_calendar: "Google Calendar",
  microsoft_calendar: "Outlook",
  calendly: "Calendly",
  cal_com: "Cal.com",
  hubspot: "HubSpot",
  manual: "Logged by hand",
  api: "API or agent",
};
const attributionHelp: Record<string, string> = {
  lead: "An attendee is a lead you emailed.",
  company: "An attendee works at a company you emailed.",
  manual: "Someone chose the sequence.",
  none: "Not tied to outreach, so not counted.",
};

type Group = "sequence" | "brand" | "none";

export function Outcomes({ ws, canManage, go }: { ws: string; canManage: boolean; go: (t: Tab, item?: string) => void }) {
  const [period, setPeriod] = useState<Period>("30d");
  const [group, setGroup] = useState<Group>("sequence");
  const [currency, setCurrency] = useState("USD");
  const [report, setReport] = useState<OutcomesReport | null>(null);
  const [error, setError] = useState("");
  const [sequences, setSequences] = useState<Sequence[]>([]);
  const [syncs, setSyncs] = useState<OutcomeSync[] | null>(null);
  const [view, setView] = useState("breakdown");
  const [version, setVersion] = useState(0);
  const { from, to } = range(period);
  const refresh = () => setVersion((v) => v + 1);
  // The sidebar's brand narrows the report; with brands, it can compare them.
  const { query: brandQuery, active: brands } = useBrand(ws);
  const brandKey = brandQuery.brand ?? "";

  useEffect(() => {
    let live = true;
    setError("");
    api.GET("/v1/workspaces/{ws}/outcomes/report", { params: { path: { ws }, query: { from, to, group_by: group, currency, ...brandQuery } } }).then(({ data, error }) => {
      if (!live) return;
      if (error) return setError(errorMessage(error));
      setReport(data);
    });
    return () => {
      live = false;
    };
  }, [ws, from, to, group, currency, version, brandKey]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/sequences", { params: { path: { ws } } }).then(({ data }) => data && setSequences(data.data));
    api.GET("/v1/workspaces/{ws}/outcomes/syncs", { params: { path: { ws } } }).then(({ data }) => setSyncs(data?.data ?? []));
  }, [ws, version]);

  const t = report?.totals;
  const connected = (syncs ?? []).length > 0;
  const nothingYet = report && t && t.meetings === 0 && t.deals === 0 && t.spend.total_cents === 0 && report.unattributed_meetings === 0;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Outcomes"
        description="Meetings and deals your outreach produced, what it cost, and the cost of each. Costs are recorded here, never billed: you pay your providers directly."
        action={<ExportMenu ws={ws} from={from} to={to} group={group} currency={currency} />}
      />

      <div className="flex flex-wrap items-center gap-2" role="group" aria-label="Report settings">
        <select aria-label="Period" className={field} value={period} onChange={(e) => setPeriod(e.target.value as Period)}>
          {periods.map((p) => <option key={p.id} value={p.id}>{p.label}</option>)}
        </select>
        <select aria-label="Group by" className={field} value={group} onChange={(e) => setGroup(e.target.value as Group)}>
          <option value="sequence">By sequence</option>
          {brands.length > 1 && <option value="brand">By brand</option>}
          <option value="none">Whole workspace</option>
        </select>
        <select aria-label="Currency" className={field} value={currency} onChange={(e) => setCurrency(e.target.value)}>
          {currencies.map((c) => <option key={c}>{c}</option>)}
        </select>
        <span className="font-mono text-[12px] text-muted-foreground">{from} → {to}</span>
      </div>

      <ErrorText>{error}</ErrorText>
      {!report || !t ? (
        !error && <Loading />
      ) : (
        <>
          {nothingYet && <GetStarted connected={connected} canManage={canManage} go={go} onRecord={() => setView("spend")} />}
          <Stats cols={5}>
            <Stat label="Cost per meeting" value={money(t.cost_per_meeting_cents, currency)} sub={t.cost_per_held_meeting_cents !== null ? `${money(t.cost_per_held_meeting_cents, currency)} per held meeting` : "Needs meetings and spend"} />
            <Stat label="Meetings" value={t.meetings.toLocaleString()} sub={t.meetings ? `${t.held} held · ${t.no_shows} no-show${t.qualified ? ` · ${t.qualified} qualified` : ""}` : "Booked in this period"} />
            <Stat label="Spend" value={money(t.spend.total_cents, currency)} sub={t.spend.estimated_cents ? `${money(t.spend.estimated_cents, currency)} estimated from credits` : t.spend.recurring_cents ? `${money(t.spend.recurring_cents, currency)} recurring` : "Recorded in this period"} />
            <Stat label="Cost per reply" value={money(t.cost_per_reply_cents, currency)} sub={`${plural(t.replies, "reply", "replies")} from ${plural(t.sent, "email")}`} />
            <Stat label="Deals won" value={t.won ? money(t.won_cents, currency) : "—"} sub={t.deals ? `${t.won} of ${plural(t.deals, "deal")}${t.roi ? ` · ${t.roi.toFixed(1)}× what you spent` : ""}` : "From your CRM"} tone={t.roi && t.roi >= 1 ? "green" : undefined} />
          </Stats>
          <Notes report={report} onRates={() => setView("spend")} onMeetings={() => setView("meetings")} />
        </>
      )}

      <Tabs value={view} onValueChange={setView}>
        <TabsList>
          <TabsTrigger value="breakdown">{group === "sequence" ? "By sequence" : group === "brand" ? "By brand" : "Summary"}</TabsTrigger>
          <TabsTrigger value="meetings">Meetings</TabsTrigger>
          <TabsTrigger value="deals">Deals</TabsTrigger>
          <TabsTrigger value="spend">Spend</TabsTrigger>
          <TabsTrigger value="sources">Sources{syncs?.some((s) => s.last_error) ? " ⚠" : ""}</TabsTrigger>
        </TabsList>
        <TabsContent value="breakdown" className="mt-4">
          {report && <Breakdown report={report} group={group} go={go} />}
        </TabsContent>
        <TabsContent value="meetings" className="mt-4">
          <Meetings ws={ws} from={from} to={to} canManage={canManage} sequences={sequences} onChange={refresh} />
        </TabsContent>
        <TabsContent value="deals" className="mt-4">
          <Deals ws={ws} from={from} to={to} canManage={canManage} currency={currency} connected={(syncs ?? []).some((s) => s.connector.startsWith("hubspot"))} onChange={refresh} go={go} />
        </TabsContent>
        <TabsContent value="spend" className="mt-4">
          <Spend ws={ws} from={from} to={to} canManage={canManage} currency={currency} sequences={sequences} onChange={refresh} />
        </TabsContent>
        <TabsContent value="sources" className="mt-4">
          <Sources ws={ws} syncs={syncs} canManage={canManage} go={go} onChange={refresh} />
        </TabsContent>
      </Tabs>
    </div>
  );
}

function GetStarted({ connected, canManage, go, onRecord }: { connected: boolean; canManage: boolean; go: (t: Tab) => void; onRecord: () => void }) {
  const steps: { done: boolean; title: string; body: string; action?: ReactNode }[] = [
    {
      done: connected,
      title: "Connect where meetings are booked",
      body: "Google Calendar, Outlook, Calendly, Cal.com or HubSpot. Only meetings with someone you emailed are kept; the rest of the calendar is never copied.",
      action: <Button variant="ghost" onClick={() => go("connectors")}>Open Connectors</Button>,
    },
    {
      done: false,
      title: "Add what outreach costs",
      body: "Mailbox seats and subscriptions as recurring costs; data and verification credits as they're spent. Agents record their own spend through the API.",
      action: canManage ? <Button variant="ghost" onClick={onRecord}>Add costs</Button> : undefined,
    },
    { done: false, title: "Read cost per meeting", body: "Every sequence gets one, updated as meetings are booked and costs come in." },
  ];
  return (
    <Card title="Set up cost per meeting" description="Three steps. Nothing here sends email or changes your calendar.">
      <ol className="space-y-3 text-[13px]">
        {steps.map((s, i) => (
          <li key={s.title} className="flex items-start gap-3">
            <span className={`mt-0.5 flex size-5 shrink-0 items-center justify-center rounded-full border font-mono text-[11px] ${s.done ? "border-brand bg-brand/15 text-brand-strong dark:text-brand" : ""}`}>{s.done ? <Icon name="check" className="size-3" /> : i + 1}</span>
            <div className="min-w-0 flex-1">
              <div className="font-medium">{s.title}</div>
              <div className="text-muted-foreground">{s.body}</div>
            </div>
            {!s.done && s.action}
          </li>
        ))}
      </ol>
    </Card>
  );
}

function Notes({ report, onRates, onMeetings }: { report: OutcomesReport; onRates: () => void; onMeetings: () => void }) {
  const items: ReactNode[] = [];
  if (report.unpriced.length) {
    items.push(
      <>
        No price yet for {report.unpriced.map((u) => `${u.units.toLocaleString()} ${u.provider} ${unitWord(u.units, u.unit_name)}`).join(", ")}, so spend is understated.{" "}
        <button className="underline" onClick={onRates}>Set prices</button>
      </>,
    );
  }
  if (report.unattributed_meetings) {
    items.push(
      <>
        {plural(report.unattributed_meetings, "meeting")} could not be tied to anyone you emailed and {report.unattributed_meetings === 1 ? "is" : "are"} not counted.{" "}
        <button className="underline" onClick={onMeetings}>Review</button>
      </>,
    );
  }
  const others = Object.entries(report.other_currencies);
  if (others.length) items.push(<>Left out, in other currencies: {others.map(([c, v]) => money(v, c)).join(", ")}. Switch the currency to see them.</>);
  if (report.totals.spend.shared_cents && report.group_by !== "none") {
    items.push(<>{money(report.totals.spend.shared_cents, report.currency)} of costs belong to no single {report.group_by}, so each gets a share by how many emails it sent.</>);
  }
  if (!items.length) return null;
  return (
    <ul className="space-y-1 rounded-lg border border-amber-500/30 bg-amber-500/5 px-4 py-3 text-[13px]" aria-label="About these numbers">
      {items.map((n, i) => (
        <li key={i} className="flex gap-2">
          <span aria-hidden="true" className="text-amber-600 dark:text-amber-400">•</span>
          <span>{n}</span>
        </li>
      ))}
    </ul>
  );
}

function Breakdown({ report, group, go }: { report: OutcomesReport; group: Group; go: (t: Tab, item?: string) => void }) {
  const c = report.currency;
  const rows = report.rows;
  if (rows.every((r) => !r.sent && !r.meetings && !r.spend.total_cents && !r.deals)) {
    return <Empty title="Nothing in this period">Sends, meetings and spend show here once there are some. Try a longer period.</Empty>;
  }
  const open = (r: OutcomeRow) => {
    if (!r.key || r.key === "all") return;
    go(group === "brand" ? "brands" : "sequences", r.key);
  };
  return (
    <DataTable
      data={rows}
      rowId={(r) => r.key || "none"}
      onRowClick={group === "none" ? undefined : open}
      searchPlaceholder={group === "none" ? undefined : `Search ${group === "brand" ? "brands" : "sequences"}`}
      hidden={["replies", "deals", "cost_reply"]}
      mobile={(r) => (
        <div className="space-y-1">
          <div className="flex items-center justify-between gap-2">
            <span className={r.key ? "font-medium" : "text-muted-foreground"}>{r.name}</span>
            <span className="font-mono">{money(r.cost_per_meeting_cents, c)}<span className="text-muted-foreground"> / meeting</span></span>
          </div>
          <div className="font-mono text-[12px] text-muted-foreground">
            {plural(r.meetings, "meeting")} · {money(r.spend.total_cents, c)} spent · {plural(r.sent, "email")}
          </div>
        </div>
      )}
      initialSort={[{ id: "meetings", desc: true }]}
      columns={[
        { id: "name", header: group === "sequence" ? "Sequence" : group === "brand" ? "Brand" : "", accessorFn: (r) => r.name, cell: ({ row: { original: r } }) => <span className={r.key ? "font-medium" : "text-muted-foreground"} title={r.key ? undefined : "Outcomes and costs that belong to no " + group}>{r.name}</span> },
        { id: "sent", header: "Sent", accessorFn: (r) => r.sent, meta: { align: "right" }, cell: ({ getValue }) => <span className="font-mono tabular-nums">{(getValue() as number).toLocaleString()}</span> },
        {
          id: "replies",
          header: "Replies",
          accessorFn: (r) => r.replies,
          meta: { align: "right" },
          cell: ({ row: { original: r } }) => <span className="font-mono tabular-nums">{r.replies.toLocaleString()}{r.reply_rate !== null && <span className="text-muted-foreground"> · {(r.reply_rate * 100).toFixed(1)}%</span>}</span>,
        },
        {
          id: "meetings",
          header: "Meetings",
          accessorFn: (r) => r.meetings,
          meta: { align: "right" },
          cell: ({ row: { original: r } }) => <span className="font-mono tabular-nums" title={`${r.held} held, ${r.no_shows} no-show, ${r.qualified} qualified`}>{r.meetings}{r.meetings > 0 && <span className="text-muted-foreground"> · {r.held} held</span>}</span>,
        },
        { id: "deals", header: "Deals", accessorFn: (r) => r.deals, meta: { align: "right" }, cell: ({ row: { original: r } }) => <span className="font-mono tabular-nums">{r.deals}{r.won > 0 && <span className="text-muted-foreground"> · {r.won} won</span>}</span> },
        {
          id: "spend",
          header: "Spend",
          accessorFn: (r) => r.spend.total_cents,
          meta: { align: "right" },
          cell: ({ row: { original: r } }) => (
            <span className="font-mono tabular-nums" title={`${money(r.spend.direct_cents, c, true)} direct, ${money(r.spend.shared_cents, c, true)} shared${r.spend.estimated_cents ? `, ${money(r.spend.estimated_cents, c, true)} estimated from credits` : ""}`}>
              {money(r.spend.total_cents, c)}
              {r.spend.estimated_cents > 0 && <span className="text-muted-foreground">*</span>}
            </span>
          ),
        },
        { id: "cost_reply", header: "Per reply", accessorFn: (r) => r.cost_per_reply_cents ?? Infinity, meta: { align: "right" }, cell: ({ row: { original: r } }) => <span className="font-mono tabular-nums">{money(r.cost_per_reply_cents, c)}</span> },
        {
          id: "cost_meeting",
          header: "Per meeting",
          accessorFn: (r) => r.cost_per_meeting_cents ?? Infinity,
          meta: { align: "right" },
          cell: ({ row: { original: r } }) => <span className="font-mono font-medium tabular-nums">{money(r.cost_per_meeting_cents, c)}</span>,
        },
        { id: "won", header: "Won", accessorFn: (r) => r.won_cents, meta: { align: "right" }, cell: ({ row: { original: r } }) => <span className="font-mono tabular-nums">{r.won_cents ? money(r.won_cents, c) : "—"}</span> },
      ]}
    />
  );
}

// ---- Meetings ----

function Meetings({ ws, from, to, canManage, sequences, onChange }: { ws: string; from: string; to: string; canManage: boolean; sequences: Sequence[]; onChange: () => void }) {
  const [list, setList] = useState<Meeting[] | null>(null);
  const [status, setStatus] = useState<MeetingStatus | "">("");
  const [adding, setAdding] = useState(false);
  const [moving, setMoving] = useState<Meeting | null>(null);

  const { query: brandQuery } = useBrand(ws);
  const load = useCallback(async () => {
    const { data } = await api.GET("/v1/workspaces/{ws}/meetings", { params: { path: { ws }, query: { from, to, limit: 500, ...(status ? { status } : {}), ...brandQuery } } });
    setList(data?.data ?? []);
  }, [ws, from, to, status, brandQuery.brand]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    load();
  }, [load]);

  async function patch(m: Meeting, body: Record<string, unknown>, done: string) {
    const { error } = await api.PATCH("/v1/workspaces/{ws}/meetings/{id}", { params: { path: { ws, id: m.id } }, body });
    if (error) return toast(errorMessage(error), "error");
    toast(done);
    load();
    onChange();
  }

  async function remove(m: Meeting) {
    const synced = m.source !== "manual" && m.source !== "api";
    if (!(await confirmAction("Delete this meeting?", synced ? <p>It came from {sourceLabel[m.source] ?? m.source} and will come back on the next sync. To stop counting it, choose <b>Not from outreach</b> instead.</p> : <p>It stops counting toward cost per meeting.</p>, "Delete"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/meetings/{id}", { params: { path: { ws, id: m.id } } });
    if (error) return toast(errorMessage(error), "error");
    toast("Meeting deleted.");
    load();
    onChange();
  }

  if (!list) return <Loading />;
  return (
    <div className="space-y-3">
      <DataTable
        data={list}
        rowId={(m) => m.id}
        searchPlaceholder="Search by person, title or sequence"
        empty={<span>No meetings in this period. Connect a calendar under <b>Sources</b>, or log one by hand.</span>}
        toolbar={
          <>
            <select aria-label="Status" className={field} value={status} onChange={(e) => setStatus(e.target.value as MeetingStatus | "")}>
              <option value="">Every status</option>
              {(Object.keys(statusLabel) as MeetingStatus[]).map((s) => <option key={s} value={s}>{statusLabel[s]}</option>)}
            </select>
            {canManage && <Button onClick={() => setAdding(true)}><Icon name="plus" className="size-3.5" />Log meeting</Button>}
          </>
        }
        mobile={(m) => (
          <div className="space-y-1">
            <div className="flex items-center justify-between gap-2">
              <span className="font-medium break-all">{m.attendee_name || m.attendee_email || "Unknown attendee"}</span>
              <Status tone={statusTone[m.status]}>{statusLabel[m.status]}</Status>
            </div>
            <div className="text-[12px] text-muted-foreground">{new Date(m.starts_at).toLocaleString()} · {m.sequence_name ?? (m.attribution === "none" ? "Not from outreach" : "No sequence")}</div>
            {canManage && <MeetingActions m={m} patch={patch} remove={remove} onMove={() => setMoving(m)} />}
          </div>
        )}
        columns={[
          {
            id: "who",
            header: "With",
            accessorFn: (m) => `${m.attendee_name} ${m.attendee_email} ${m.title}`,
            cell: ({ row: { original: m } }) => (
              <div className="min-w-0">
                <div className="truncate font-medium">{m.attendee_name || m.attendee_email || "Unknown attendee"}</div>
                <div className="truncate text-xs text-muted-foreground">{m.attendee_name ? m.attendee_email : ""}{m.title && `${m.attendee_name ? " · " : ""}${m.title}`}</div>
              </div>
            ),
          },
          { id: "when", header: "When", accessorFn: (m) => m.starts_at, cell: ({ row: { original: m } }) => <span title={`Booked ${new Date(m.booked_at).toLocaleString()}`}>{new Date(m.starts_at).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" })}</span> },
          { id: "status", header: "Status", accessorFn: (m) => m.status, cell: ({ row: { original: m } }) => <span className="flex items-center gap-1.5"><Status tone={statusTone[m.status]}>{statusLabel[m.status]}</Status>{m.qualified && <Badge tone="green">Qualified</Badge>}</span> },
          {
            id: "credit",
            header: "Counts for",
            accessorFn: (m) => m.sequence_name ?? "",
            cell: ({ row: { original: m } }) =>
              m.attribution === "none" ? (
                <span className="text-muted-foreground" title={attributionHelp.none}>Not from outreach</span>
              ) : (
                <span title={attributionHelp[m.attribution]}>
                  {m.sequence_name ?? <span className="text-muted-foreground">No sequence</span>}
                  {m.attribution === "company" && <Badge title={attributionHelp.company}>Colleague</Badge>}
                </span>
              ),
          },
          { id: "source", header: "From", accessorFn: (m) => sourceLabel[m.source] ?? m.source, cell: ({ getValue }) => <span className="text-muted-foreground">{getValue() as string}</span> },
          {
            id: "actions",
            header: () => <span className="sr-only">Actions</span>,
            enableSorting: false,
            enableHiding: false,
            meta: { align: "right" },
            cell: ({ row: { original: m } }) => canManage && <MeetingActions m={m} patch={patch} remove={remove} onMove={() => setMoving(m)} />,
          },
        ]}
      />
      {adding && <LogMeeting ws={ws} sequences={sequences} onClose={() => setAdding(false)} onDone={() => { load(); onChange(); }} />}
      {moving && <MoveMeeting m={moving} sequences={sequences} onClose={() => setMoving(null)} onSave={(body) => patch(moving, body, "Attribution updated. Syncs won't change it.")} />}
    </div>
  );
}

function MeetingActions({ m, patch, remove, onMove }: { m: Meeting; patch: (m: Meeting, body: Record<string, unknown>, done: string) => void; remove: (m: Meeting) => void; onMove: () => void }) {
  return (
    <Menu
      label="Actions"
      aria={`Actions for the meeting with ${m.attendee_email || "this attendee"}`}
      items={[
        m.status !== "held" && { label: "Mark held", onClick: () => patch(m, { status: "held" }, "Marked held.") },
        m.status !== "no_show" && { label: "Mark no-show", onClick: () => patch(m, { status: "no_show" }, "Marked no-show.") },
        { label: m.qualified ? "Not qualified" : "Mark qualified", onClick: () => patch(m, { qualified: !m.qualified }, m.qualified ? "No longer qualified." : "Marked qualified.") },
        { label: "Change what it counts for…", onClick: onMove },
        m.attribution !== "none" && { label: "Not from outreach", onClick: () => patch(m, { unattributed: true }, "No longer counted.") },
        { label: "Delete", onClick: () => remove(m), danger: true },
      ]}
    />
  );
}

function MoveMeeting({ m, sequences, onClose, onSave }: { m: Meeting; sequences: Sequence[]; onClose: () => void; onSave: (body: Record<string, unknown>) => void }) {
  const [seq, setSeq] = useState(m.sequence_id ?? "");
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <form
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            onSave(seq ? { sequence_id: seq } : { unattributed: true });
            onClose();
          }}
        >
          <DialogHeader className="text-left">
            <DialogTitle className="text-[15px]">What does this meeting count for?</DialogTitle>
            <DialogDescription className="text-[13px]">Syncs never change a choice made here.</DialogDescription>
          </DialogHeader>
          <label className="block text-sm">
            <span className="mb-1 block font-medium">Sequence</span>
            <select className={`${field} w-full`} value={seq} onChange={(e) => setSeq(e.target.value)}>
              <option value="">Not from outreach</option>
              {sequences.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
            </select>
          </label>
          <DialogFooter className="flex-row justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
            <Button>Save</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function LogMeeting({ ws, sequences, onClose, onDone }: { ws: string; sequences: Sequence[]; onClose: () => void; onDone: () => void }) {
  const [email, setEmail] = useState("");
  const [name, setName] = useState("");
  const [when, setWhen] = useState("");
  const [title, setTitle] = useState("");
  const [seq, setSeq] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/meetings", {
      params: { path: { ws } },
      body: {
        attendee_email: email.trim(),
        starts_at: new Date(when).toISOString(),
        ...(name ? { attendee_name: name } : {}),
        ...(title ? { title } : {}),
        ...(seq ? { sequence_id: seq } : {}),
      },
    });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    toast(data.attribution === "none" ? "Logged. Nobody you emailed matches that address, so it isn't counted yet." : `Logged${data.sequence_name ? ` for ${data.sequence_name}` : ""}.`);
    onDone();
    onClose();
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <form className="space-y-3 text-sm" onSubmit={submit}>
          <DialogHeader className="text-left">
            <DialogTitle className="text-[15px]">Log a meeting</DialogTitle>
            <DialogDescription className="text-[13px]">For meetings no connected calendar sees. It's credited to the sequence that last emailed this person, unless you pick one.</DialogDescription>
          </DialogHeader>
          <label className="block">
            <span className="mb-1 block font-medium">Their email</span>
            <input type="email" required className={`${field} w-full`} placeholder="sam@acme-com.example" value={email} onChange={(e) => setEmail(e.target.value)} autoFocus />
          </label>
          <div className="grid gap-3 sm:grid-cols-2">
            <label className="block">
              <span className="mb-1 block font-medium">Name <span className="font-normal text-muted-foreground">(optional)</span></span>
              <input className={`${field} w-full`} value={name} onChange={(e) => setName(e.target.value)} />
            </label>
            <label className="block">
              <span className="mb-1 block font-medium">When</span>
              <input type="datetime-local" required className={`${field} w-full`} value={when} onChange={(e) => setWhen(e.target.value)} />
            </label>
          </div>
          <label className="block">
            <span className="mb-1 block font-medium">Title <span className="font-normal text-muted-foreground">(optional)</span></span>
            <input className={`${field} w-full`} placeholder="Intro call" value={title} onChange={(e) => setTitle(e.target.value)} />
          </label>
          <label className="block">
            <span className="mb-1 block font-medium">Counts for</span>
            <select className={`${field} w-full`} value={seq} onChange={(e) => setSeq(e.target.value)}>
              <option value="">The sequence that last emailed them</option>
              {sequences.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
            </select>
          </label>
          <ErrorText>{error}</ErrorText>
          <DialogFooter className="flex-row justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
            <Button disabled={busy || !email || !when}>{busy ? "Logging…" : "Log meeting"}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// ---- Deals ----

function Deals({ ws, from, to, canManage, currency, connected, onChange, go }: { ws: string; from: string; to: string; canManage: boolean; currency: string; connected: boolean; onChange: () => void; go: (t: Tab) => void }) {
  const [list, setList] = useState<Deal[] | null>(null);
  const [adding, setAdding] = useState(false);

  const { query: brandQuery } = useBrand(ws);
  const load = useCallback(async () => {
    const { data } = await api.GET("/v1/workspaces/{ws}/deals", { params: { path: { ws }, query: { from, to, limit: 500, ...brandQuery } } });
    setList(data?.data ?? []);
  }, [ws, from, to, brandQuery.brand]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    load();
  }, [load]);

  if (!list) return <Loading />;
  const stateTone = { open: "grey", won: "green", lost: "red" } as const;
  return (
    <div className="space-y-3">
      {!connected && list.length === 0 ? (
        <Empty
          title="No deals yet"
          action={
            <div className="flex flex-wrap justify-center gap-2">
              <Button onClick={() => go("connectors")}>Connect HubSpot</Button>
              {canManage && <Button variant="ghost" onClick={() => setAdding(true)}>Log a deal by hand</Button>}
            </div>
          }
        >
          Connect your CRM and deals opened with people you emailed show here with their stage and amount. HubSpot stays the source of truth; Outflare never edits deals there.
        </Empty>
      ) : (
        <DataTable
          data={list}
          rowId={(d) => d.id}
          searchPlaceholder="Search deals"
          empty="No deals opened in this period."
          toolbar={canManage && <Button variant="ghost" onClick={() => setAdding(true)}><Icon name="plus" className="size-3.5" />Log deal</Button>}
          mobile={(d) => (
            <div className="flex items-start justify-between gap-2">
              <div className="min-w-0">
                <div className="truncate font-medium">{d.name}</div>
                <div className="text-[12px] text-muted-foreground">{d.stage || d.state} · {d.sequence_name ?? "No sequence"}</div>
              </div>
              <span className="font-mono">{money(d.amount_cents, d.currency)}</span>
            </div>
          )}
          columns={[
            { id: "name", header: "Deal", accessorFn: (d) => `${d.name} ${d.lead_email ?? ""}`, cell: ({ row: { original: d } }) => <div className="min-w-0"><div className="truncate font-medium">{d.name}</div>{d.lead_email && <div className="truncate text-xs text-muted-foreground">{d.lead_email}</div>}</div> },
            { id: "stage", header: "Stage", accessorFn: (d) => d.stage, cell: ({ row: { original: d } }) => <span className="flex items-center gap-1.5"><Status tone={stateTone[d.state]}>{d.state === "open" ? "Open" : d.state === "won" ? "Won" : "Lost"}</Status><span className="text-muted-foreground">{d.stage}</span></span> },
            { id: "amount", header: "Amount", accessorFn: (d) => d.amount_cents ?? 0, meta: { align: "right" }, cell: ({ row: { original: d } }) => <span className={`font-mono tabular-nums ${d.currency !== currency ? "text-muted-foreground" : ""}`} title={d.currency !== currency ? `In ${d.currency}, left out of the ${currency} report` : undefined}>{money(d.amount_cents, d.currency)}</span> },
            { id: "credit", header: "Counts for", accessorFn: (d) => d.sequence_name ?? "", cell: ({ row: { original: d } }) => (d.attribution === "none" ? <span className="text-muted-foreground">Not from outreach</span> : <span>{d.sequence_name ?? <span className="text-muted-foreground">No sequence</span>}</span>) },
            { id: "opened", header: "Opened", accessorFn: (d) => d.opened_at, cell: ({ row: { original: d } }) => <span className="text-muted-foreground" title={new Date(d.opened_at).toLocaleString()}>{ago(d.opened_at)}</span> },
            { id: "source", header: "From", accessorFn: (d) => sourceLabel[d.source] ?? d.source, cell: ({ getValue }) => <span className="text-muted-foreground">{getValue() as string}</span> },
          ]}
        />
      )}
      {adding && <LogDeal ws={ws} currency={currency} onClose={() => setAdding(false)} onDone={() => { load(); onChange(); }} />}
    </div>
  );
}

function LogDeal({ ws, currency, onClose, onDone }: { ws: string; currency: string; onClose: () => void; onDone: () => void }) {
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [state, setState] = useState<"open" | "won" | "lost">("open");
  const [amount, setAmount] = useState("");
  const [cur, setCur] = useState(currency);
  const [error, setError] = useState("");

  async function submit(e: FormEvent) {
    e.preventDefault();
    const cents = amount ? Math.round(parseFloat(amount) * 100) : undefined;
    if (amount && (cents === undefined || Number.isNaN(cents) || cents < 0)) return setError("Enter the amount as a number, e.g. 12000.");
    const { data, error } = await api.POST("/v1/workspaces/{ws}/deals", { params: { path: { ws } }, body: { name, contact_email: email, state, currency: cur, ...(cents !== undefined ? { amount_cents: cents } : {}) } });
    if (error) return setError(errorMessage(error));
    toast(data.attribution === "none" ? "Logged. Nobody you emailed matches that contact, so it isn't counted yet." : `Logged${data.sequence_name ? ` for ${data.sequence_name}` : ""}.`);
    onDone();
    onClose();
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <form className="space-y-3 text-sm" onSubmit={submit}>
          <DialogHeader className="text-left">
            <DialogTitle className="text-[15px]">Log a deal</DialogTitle>
            <DialogDescription className="text-[13px]">For teams without a connected CRM. It's tied to the sequence that emailed the contact.</DialogDescription>
          </DialogHeader>
          <label className="block">
            <span className="mb-1 block font-medium">Deal name</span>
            <input required maxLength={300} className={`${field} w-full`} placeholder="Acme, annual plan" value={name} onChange={(e) => setName(e.target.value)} autoFocus />
          </label>
          <label className="block">
            <span className="mb-1 block font-medium">Contact's email</span>
            <input type="email" required className={`${field} w-full`} placeholder="sam@acme-com.example" value={email} onChange={(e) => setEmail(e.target.value)} />
          </label>
          <div className="grid grid-cols-3 gap-3">
            <label className="block">
              <span className="mb-1 block font-medium">State</span>
              <select className={`${field} w-full`} value={state} onChange={(e) => setState(e.target.value as typeof state)}>
                <option value="open">Open</option>
                <option value="won">Won</option>
                <option value="lost">Lost</option>
              </select>
            </label>
            <label className="col-span-2 block">
              <span className="mb-1 block font-medium">Amount <span className="font-normal text-muted-foreground">(optional)</span></span>
              <div className="flex gap-2">
                <input inputMode="decimal" className={`${field} min-w-0 flex-1 font-mono`} placeholder="12000" value={amount} onChange={(e) => setAmount(e.target.value)} />
                <select aria-label="Currency" className={field} value={cur} onChange={(e) => setCur(e.target.value)}>
                  {currencies.map((c) => <option key={c}>{c}</option>)}
                </select>
              </div>
            </label>
          </div>
          <ErrorText>{error}</ErrorText>
          <DialogFooter className="flex-row justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
            <Button disabled={!name || !email}>Log deal</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// ---- Spend ----

function Spend({ ws, from, to, canManage, currency, sequences, onChange }: { ws: string; from: string; to: string; canManage: boolean; currency: string; sequences: Sequence[]; onChange: () => void }) {
  const [entries, setEntries] = useState<SpendEntry[] | null>(null);
  const [recurring, setRecurring] = useState<RecurringSpend[] | null>(null);
  const [rates, setRates] = useState<SpendRate[] | null>(null);
  const [adding, setAdding] = useState<"" | "once" | "recurring">("");
  const [editing, setEditing] = useState<RecurringSpend | null>(null);
  const seqName = useMemo(() => Object.fromEntries(sequences.map((s) => [s.id, s.name])), [sequences]);

  const load = useCallback(async () => {
    const [e, r, x] = await Promise.all([
      api.GET("/v1/workspaces/{ws}/spend", { params: { path: { ws }, query: { from, to, limit: 1000 } } }),
      api.GET("/v1/workspaces/{ws}/spend-recurring", { params: { path: { ws } } }),
      api.GET("/v1/workspaces/{ws}/spend-rates", { params: { path: { ws } } }),
    ]);
    setEntries(e.data?.data ?? []);
    setRecurring(r.data?.data ?? []);
    setRates(x.data?.data ?? []);
  }, [ws, from, to]);

  useEffect(() => {
    load();
  }, [load]);

  const changed = () => {
    load();
    onChange();
  };

  async function removeEntry(e: SpendEntry) {
    if (!(await confirmAction("Delete this charge?", <p>{e.amount_cents !== null ? money(e.amount_cents, e.currency, true) : `${e.units?.toLocaleString()} ${unitWord(e.units ?? 0, e.unit_name)}`} from {e.provider || categoryLabel[e.category]} stops counting in every report. The deletion is kept in the audit log.</p>, "Delete"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/spend/{id}", { params: { path: { ws, id: e.id } } });
    if (error) return toast(errorMessage(error), "error");
    toast("Charge deleted.");
    changed();
  }

  async function stopRecurring(r: RecurringSpend) {
    const { error } = await api.PUT("/v1/workspaces/{ws}/spend-recurring/{id}", { params: { path: { ws, id: r.id } }, body: { ...recurringBody(r), ends_on: day(new Date()) } });
    if (error) return toast(errorMessage(error), "error");
    toast(`${r.name} stops today. Past reports keep it.`);
    changed();
  }

  async function removeRecurring(r: RecurringSpend) {
    if (!(await confirmAction(`Delete ${r.name}?`, <p>It disappears from every report, past ones included. To stop paying for it from today, choose <b>Stop</b> instead.</p>, "Delete"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/spend-recurring/{id}", { params: { path: { ws, id: r.id } } });
    if (error) return toast(errorMessage(error), "error");
    toast(`${r.name} deleted.`);
    changed();
  }

  if (!entries || !recurring || !rates) return <Loading />;
  const today = day(new Date());
  const unpriced = rates.filter((r) => r.unpriced_units > 0 && r.unit_price_micros === 0);
  return (
    <div className="space-y-6">
      {unpriced.length > 0 && <PriceUnits ws={ws} rates={unpriced} canManage={canManage} currency={currency} onDone={changed} />}

      <Card
        title="Recurring costs"
        description="Mailbox seats, subscriptions, people. Each is spread evenly over the days it was active."
        action={canManage && <Button variant="ghost" onClick={() => setAdding("recurring")}><Icon name="plus" className="size-3.5" />Add</Button>}
      >
        {recurring.length === 0 ? (
          <p className="text-[13px] text-muted-foreground">None yet. Most teams add their mailbox seats (e.g. 50 Google Workspace seats at $3) and their data subscription.</p>
        ) : (
          <ul className="divide-y text-[13px]">
            {recurring.map((r) => {
              const ended = r.ends_on !== null && r.ends_on !== undefined && r.ends_on < today;
              return (
                <li key={r.id} className="flex items-center justify-between gap-3 py-2">
                  <div className="min-w-0">
                    <div className={`truncate font-medium ${ended ? "text-muted-foreground line-through" : ""}`}>{r.name}</div>
                    <div className="truncate text-xs text-muted-foreground">
                      {categoryLabel[r.category]}
                      {r.sequence_id ? ` · ${seqName[r.sequence_id] ?? "a sequence"}` : " · whole workspace"}
                      {` · since ${r.starts_on}`}
                      {r.ends_on && ` · until ${r.ends_on}`}
                    </div>
                  </div>
                  <div className="flex shrink-0 items-center gap-2">
                    <span className="font-mono tabular-nums">{money(r.amount_cents, r.currency ?? "USD", true)}<span className="text-muted-foreground"> / {r.every}</span></span>
                    {canManage && (
                      <Menu
                        label="Actions"
                        aria={`Actions for ${r.name}`}
                        items={[{ label: "Edit", onClick: () => setEditing(r) }, !ended && { label: "Stop from today", onClick: () => stopRecurring(r) }, { label: "Delete", onClick: () => removeRecurring(r), danger: true }]}
                      />
                    )}
                  </div>
                </li>
              );
            })}
          </ul>
        )}
      </Card>

      <div className="space-y-3">
        <div className="flex items-center justify-between gap-2">
          <h3 className="text-[13px] font-medium">Charges in this period</h3>
          {canManage && <Button onClick={() => setAdding("once")}><Icon name="plus" className="size-3.5" />Record spend</Button>}
        </div>
        <DataTable
          data={entries}
          rowId={(e) => e.id}
          searchPlaceholder="Search by provider or note"
          empty="No charges in this period. Credits spent by connectors and agents show up here on their own."
          mobile={(e) => (
            <div className="flex items-start justify-between gap-2">
              <div className="min-w-0">
                <div className="truncate font-medium">{e.provider || categoryLabel[e.category]}</div>
                <div className="text-[12px] text-muted-foreground">{categoryLabel[e.category]} · {shortDate(e.occurred_at)}</div>
              </div>
              <span className="font-mono">{e.amount_cents !== null ? money(e.amount_cents, e.currency, true) : `${e.units?.toLocaleString()} ${unitWord(e.units ?? 0, e.unit_name)}`}</span>
            </div>
          )}
          columns={[
            { id: "provider", header: "Provider", accessorFn: (e) => `${e.provider} ${e.note}`, cell: ({ row: { original: e } }) => <div className="min-w-0"><div className="truncate font-medium">{e.provider || "—"}</div>{e.note && <div className="truncate text-xs text-muted-foreground">{e.note}</div>}</div> },
            { id: "category", header: "For", accessorFn: (e) => categoryLabel[e.category] },
            {
              id: "amount",
              header: "Amount",
              accessorFn: (e) => e.amount_cents ?? -1,
              meta: { align: "right" },
              cell: ({ row: { original: e } }) =>
                e.amount_cents !== null ? (
                  <span className="font-mono tabular-nums">{money(e.amount_cents, e.currency, true)}</span>
                ) : (
                  <span className="font-mono tabular-nums text-muted-foreground" title="Priced with the unit rate in reports">{e.units?.toLocaleString()} {unitWord(e.units ?? 0, e.unit_name)}</span>
                ),
            },
            { id: "credit", header: "Counts for", accessorFn: (e) => (e.sequence_id ? seqName[e.sequence_id] : "") ?? "", cell: ({ row: { original: e } }) => <span className={e.sequence_id ? "" : "text-muted-foreground"}>{e.sequence_id ? seqName[e.sequence_id] ?? "A sequence" : e.domain ? `Sequences sending from ${e.domain}` : e.mailbox_id ? "Sequences sending from this mailbox" : "Shared"}</span> },
            { id: "when", header: "When", accessorFn: (e) => e.occurred_at, cell: ({ row: { original: e } }) => <span className="text-muted-foreground">{shortDate(e.occurred_at)}</span> },
            { id: "source", header: "Source", accessorFn: (e) => e.source, cell: ({ row: { original: e } }) => <span className="text-muted-foreground">{e.source === "auto" ? "Connector" : e.source === "import" ? "Import" : "Manual or API"}</span> },
            {
              id: "actions",
              header: () => <span className="sr-only">Actions</span>,
              enableSorting: false,
              enableHiding: false,
              meta: { align: "right" },
              cell: ({ row: { original: e } }) => canManage && <Button variant="danger" className="opacity-0 group-hover:opacity-100 focus-visible:opacity-100" onClick={() => removeEntry(e)}>Delete</Button>,
            },
          ]}
        />
      </div>

      {adding === "once" && <RecordSpend ws={ws} currency={currency} sequences={sequences} onClose={() => setAdding("")} onDone={changed} />}
      {(adding === "recurring" || editing) && <RecurringForm ws={ws} currency={currency} sequences={sequences} editing={editing} onClose={() => { setAdding(""); setEditing(null); }} onDone={changed} />}
    </div>
  );
}

const recurringBody = (r: RecurringSpend) => ({
  name: r.name,
  category: r.category,
  amount_cents: r.amount_cents,
  currency: r.currency,
  every: r.every,
  provider: r.provider,
  starts_on: r.starts_on,
  ends_on: r.ends_on ?? null,
  project_id: r.project_id ?? null,
  sequence_id: r.sequence_id ?? null,
  note: r.note,
});

// Prices for usage recorded only as units, typed as money per unit.
function PriceUnits({ ws, rates, canManage, currency, onDone }: { ws: string; rates: SpendRate[]; canManage: boolean; currency: string; onDone: () => void }) {
  const [prices, setPrices] = useState<Record<string, string>>({});
  const key = (r: SpendRate) => `${r.provider}/${r.unit_name}`;

  async function save(r: SpendRate) {
    const v = parseFloat(prices[key(r)] ?? "");
    if (Number.isNaN(v) || v < 0) return toast("Enter the price of one unit, e.g. 0.004.", "error");
    const { error } = await api.PUT("/v1/workspaces/{ws}/spend-rates", { params: { path: { ws } }, body: { provider: r.provider, unit_name: r.unit_name, unit_price_micros: Math.round(v * 1_000_000), currency } });
    if (error) return toast(errorMessage(error), "error");
    toast(`Priced ${r.provider} ${unitWord(2, r.unit_name)} at ${v} ${currency} each.`);
    onDone();
  }

  return (
    <Card title="Usage without a price" description="These were recorded as units, not money. Give each a price and reports include them (marked estimated).">
      <ul className="divide-y text-[13px]">
        {rates.map((r) => (
          <li key={key(r)} className="flex flex-wrap items-center justify-between gap-3 py-2">
            <span><span className="font-medium">{r.provider}</span> <span className="text-muted-foreground">· {r.unpriced_units.toLocaleString()} {unitWord(r.unpriced_units, r.unit_name)}</span></span>
            {canManage && (
              <form className="flex items-center gap-2" onSubmit={(e) => { e.preventDefault(); save(r); }}>
                <label className="flex items-center gap-1.5">
                  <span className="text-muted-foreground">{currency} per {unitWord(1, r.unit_name)}</span>
                  <input aria-label={`Price of one ${r.provider} ${unitWord(1, r.unit_name)}`} inputMode="decimal" className={`${field} w-24 font-mono`} placeholder="0.004" value={prices[key(r)] ?? ""} onChange={(e) => setPrices({ ...prices, [key(r)]: e.target.value })} />
                </label>
                <Button variant="ghost" disabled={!prices[key(r)]}>Save</Button>
              </form>
            )}
          </li>
        ))}
      </ul>
    </Card>
  );
}

function CountsFor({ sequences, seq, setSeq }: { sequences: Sequence[]; seq: string; setSeq: (v: string) => void }) {
  return (
    <label className="block">
        <span className="mb-1 block font-medium">Sequence <span className="font-normal text-muted-foreground">(optional)</span></span>
        <select className={`${field} w-full`} value={seq} onChange={(e) => setSeq(e.target.value)}>
          <option value="">Shared by the workspace</option>
          {sequences.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
        </select>
    </label>
  );
}

function RecordSpend({ ws, currency, sequences, onClose, onDone }: { ws: string; currency: string; sequences: Sequence[]; onClose: () => void; onDone: () => void }) {
  const [category, setCategory] = useState<SpendCategory>("data");
  const [provider, setProvider] = useState("");
  const [mode, setMode] = useState<"money" | "units">("money");
  const [amount, setAmount] = useState("");
  const [units, setUnits] = useState("");
  const [unit, setUnit] = useState("credit");
  const [cur, setCur] = useState(currency);
  const [date, setDate] = useState(day(new Date()));
  const [seq, setSeq] = useState("");
  const [note, setNote] = useState("");
  const [error, setError] = useState("");

  async function submit(e: FormEvent) {
    e.preventDefault();
    const value = parseFloat(mode === "money" ? amount : units);
    if (Number.isNaN(value) || value < 0) return setError(mode === "money" ? "Enter the amount, e.g. 49.99." : "Enter how many units, e.g. 1000.");
    const { error } = await api.POST("/v1/workspaces/{ws}/spend", {
      params: { path: { ws } },
      body: {
        category,
        provider,
        currency: cur,
        occurred_at: new Date(`${date}T12:00:00Z`).toISOString(),
        ...(mode === "money" ? { amount_cents: Math.round(value * 100) } : { units: value, unit_name: unit }),
        ...(seq ? { sequence_id: seq } : {}),
        ...(note ? { note } : {}),
      },
    });
    if (error) return setError(errorMessage(error));
    toast("Recorded.");
    onDone();
    onClose();
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <form className="space-y-3 text-sm" onSubmit={submit}>
          <DialogHeader className="text-left">
            <DialogTitle className="text-[15px]">Record spend</DialogTitle>
            <DialogDescription className="text-[13px]">A one-off charge: a batch of credits, a domain renewal. Monthly costs belong under recurring costs.</DialogDescription>
          </DialogHeader>
          <div className="grid gap-3 sm:grid-cols-2">
            <label className="block">
              <span className="mb-1 block font-medium">For</span>
              <select className={`${field} w-full`} value={category} onChange={(e) => setCategory(e.target.value as SpendCategory)}>
                {categoryList.map((c) => <option key={c} value={c}>{categoryLabel[c]}</option>)}
              </select>
            </label>
            <label className="block">
              <span className="mb-1 block font-medium">Provider</span>
              <input className={`${field} w-full`} placeholder="e.g. Apollo" maxLength={200} value={provider} onChange={(e) => setProvider(e.target.value)} />
            </label>
          </div>
          <fieldset>
            <legend className="mb-1 font-medium">What it cost</legend>
            <div className="mb-2 flex gap-4 text-[13px]">
              <label className="flex items-center gap-1.5"><input type="radio" checked={mode === "money"} onChange={() => setMode("money")} />An amount</label>
              <label className="flex items-center gap-1.5"><input type="radio" checked={mode === "units"} onChange={() => setMode("units")} />Credits or units</label>
            </div>
            {mode === "money" ? (
              <div className="flex gap-2">
                <input aria-label="Amount" inputMode="decimal" className={`${field} min-w-0 flex-1 font-mono`} placeholder="49.99" value={amount} onChange={(e) => setAmount(e.target.value)} autoFocus />
                <select aria-label="Currency" className={field} value={cur} onChange={(e) => setCur(e.target.value)}>
                  {currencies.map((c) => <option key={c}>{c}</option>)}
                </select>
              </div>
            ) : (
              <div className="flex gap-2">
                <input aria-label="How many" inputMode="decimal" className={`${field} min-w-0 flex-1 font-mono`} placeholder="1000" value={units} onChange={(e) => setUnits(e.target.value)} />
                <input aria-label="Unit" className={`${field} w-32`} placeholder="credit" value={unit} onChange={(e) => setUnit(e.target.value)} />
              </div>
            )}
            {mode === "units" && <p className="mt-1 text-xs text-muted-foreground">Reports price these with the unit price you set for this provider.</p>}
          </fieldset>
          <label className="block">
            <span className="mb-1 block font-medium">Date</span>
            <input type="date" required className={`${field} w-full`} max={day(new Date())} value={date} onChange={(e) => setDate(e.target.value)} />
          </label>
          <CountsFor sequences={sequences} seq={seq} setSeq={setSeq} />
          <label className="block">
            <span className="mb-1 block font-medium">Note <span className="font-normal text-muted-foreground">(optional)</span></span>
            <input className={`${field} w-full`} maxLength={2000} value={note} onChange={(e) => setNote(e.target.value)} />
          </label>
          <ErrorText>{error}</ErrorText>
          <DialogFooter className="flex-row justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
            <Button disabled={mode === "money" ? !amount : !units || !unit}>Record</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function RecurringForm({ ws, currency, sequences, editing, onClose, onDone }: { ws: string; currency: string; sequences: Sequence[]; editing: RecurringSpend | null; onClose: () => void; onDone: () => void }) {
  const [name, setName] = useState(editing?.name ?? "");
  const [category, setCategory] = useState<SpendCategory>(editing?.category ?? "mailboxes");
  const [amount, setAmount] = useState(editing ? String(editing.amount_cents / 100) : "");
  const [cur, setCur] = useState(editing?.currency ?? currency);
  const [every, setEvery] = useState<"month" | "year">(editing?.every ?? "month");
  const [starts, setStarts] = useState(editing?.starts_on ?? day(new Date()));
  const [ends, setEnds] = useState(editing?.ends_on ?? "");
  const [seq, setSeq] = useState(editing?.sequence_id ?? "");
  // Kept as it was until projects are removed; no longer picked here.
  const project = editing?.project_id ?? "";
  const [error, setError] = useState("");

  async function submit(e: FormEvent) {
    e.preventDefault();
    const v = parseFloat(amount);
    if (Number.isNaN(v) || v < 0) return setError("Enter the amount, e.g. 150.");
    const body = {
      name,
      category,
      amount_cents: Math.round(v * 100),
      currency: cur,
      every,
      starts_on: starts,
      ends_on: ends || null,
      sequence_id: seq || null,
      project_id: seq ? null : project || null,
      provider: editing?.provider ?? "",
      note: editing?.note ?? "",
    };
    const { error } = editing
      ? await api.PUT("/v1/workspaces/{ws}/spend-recurring/{id}", { params: { path: { ws, id: editing.id } }, body })
      : await api.POST("/v1/workspaces/{ws}/spend-recurring", { params: { path: { ws } }, body });
    if (error) return setError(errorMessage(error));
    toast(editing ? "Saved." : `Added ${name}.`);
    onDone();
    onClose();
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <form className="space-y-3 text-sm" onSubmit={submit}>
          <DialogHeader className="text-left">
            <DialogTitle className="text-[15px]">{editing ? `Edit ${editing.name}` : "Add a recurring cost"}</DialogTitle>
            <DialogDescription className="text-[13px]">Reports spread it evenly over each day it's active, so a month's seats count fully in a monthly report and partly in a weekly one.</DialogDescription>
          </DialogHeader>
          <label className="block">
            <span className="mb-1 block font-medium">Name</span>
            <input required maxLength={200} className={`${field} w-full`} placeholder="50 Google Workspace seats" value={name} onChange={(e) => setName(e.target.value)} autoFocus />
          </label>
          <div className="grid gap-3 sm:grid-cols-3">
            <label className="block">
              <span className="mb-1 block font-medium">For</span>
              <select className={`${field} w-full`} value={category} onChange={(e) => setCategory(e.target.value as SpendCategory)}>
                {categoryList.map((c) => <option key={c} value={c}>{categoryLabel[c]}</option>)}
              </select>
            </label>
            <label className="block sm:col-span-2">
              <span className="mb-1 block font-medium">Amount</span>
              <div className="flex gap-2">
                <input inputMode="decimal" required className={`${field} min-w-0 flex-1 font-mono`} placeholder="150" value={amount} onChange={(e) => setAmount(e.target.value)} />
                <select aria-label="Currency" className={field} value={cur} onChange={(e) => setCur(e.target.value)}>
                  {currencies.map((c) => <option key={c}>{c}</option>)}
                </select>
                <select aria-label="How often" className={field} value={every} onChange={(e) => setEvery(e.target.value as "month" | "year")}>
                  <option value="month">a month</option>
                  <option value="year">a year</option>
                </select>
              </div>
            </label>
          </div>
          <div className="grid gap-3 sm:grid-cols-2">
            <label className="block">
              <span className="mb-1 block font-medium">Since</span>
              <input type="date" required className={`${field} w-full`} value={starts} onChange={(e) => setStarts(e.target.value)} />
            </label>
            <label className="block">
              <span className="mb-1 block font-medium">Until <span className="font-normal text-muted-foreground">(optional)</span></span>
              <input type="date" className={`${field} w-full`} min={starts} value={ends} onChange={(e) => setEnds(e.target.value)} />
            </label>
          </div>
          <CountsFor sequences={sequences} seq={seq} setSeq={setSeq} />
          <ErrorText>{error}</ErrorText>
          <DialogFooter className="flex-row justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
            <Button disabled={!name || !amount}>{editing ? "Save" : "Add"}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// ---- Sources ----

const streamLabel: Record<string, string> = { meetings: "Meetings", deals: "Deals", push: "Meetings written to the CRM", "": "Waiting for the first sync" };

function Sources({ ws, syncs, canManage, go, onChange }: { ws: string; syncs: OutcomeSync[] | null; canManage: boolean; go: (t: Tab) => void; onChange: () => void }) {
  if (!syncs) return <Loading />;
  if (syncs.length === 0) {
    return (
      <Empty title="Nothing connected yet" action={<Button onClick={() => go("connectors")}>Open Connectors</Button>}>
        Connect Google Calendar, Outlook, Calendly, Cal.com or HubSpot. Meetings with people you emailed are counted every 15 minutes; nothing else is copied and nothing is changed there.
      </Empty>
    );
  }
  const byIntegration = new Map<string, OutcomeSync[]>();
  for (const s of syncs) byIntegration.set(s.integration_id, [...(byIntegration.get(s.integration_id) ?? []), s]);

  async function run(id: string, name: string) {
    const { error } = await api.POST("/v1/workspaces/{ws}/outcomes/syncs/{id}/run", { params: { path: { ws, id } } });
    if (error) return toast(errorMessage(error), "error");
    toast(`Syncing ${name}. New meetings show here in a minute.`);
    setTimeout(onChange, 15000);
  }

  return (
    <div className="space-y-3">
      {[...byIntegration.values()].map((streams) => {
        const s0 = streams[0];
        const failing = streams.find((s) => s.last_error);
        return (
          <Card
            key={s0.integration_id}
            title={<span className="flex items-center gap-2">{s0.name}{s0.paused ? <Badge>Paused</Badge> : failing ? <Status tone={connection.failing.tone}>{connection.failing.label}</Status> : <Status tone={connection.ok.tone}>Syncing</Status>}</span>}
            action={canManage && !s0.paused && <Button variant="ghost" onClick={() => run(s0.integration_id, s0.name)}><Icon name="refresh" className="size-3.5" />Sync now</Button>}
          >
            <ul className="divide-y text-[13px]">
              {streams.map((s) => (
                <li key={s.stream || "none"} className="flex flex-wrap items-center justify-between gap-2 py-2">
                  <span>{streamLabel[s.stream] ?? s.stream}</span>
                  <span className="text-muted-foreground">
                    {s.last_error ? <span className="text-red-600 dark:text-red-400">{s.last_error}</span> : s.last_run_at ? `${ago(s.last_run_at)} · ${plural(s.last_changes, "change")}` : "Starts within 15 minutes"}
                  </span>
                </li>
              ))}
            </ul>
          </Card>
        );
      })}
      <p className="text-[13px] text-muted-foreground">
        Add or change sources, pause them, or choose to write meetings into HubSpot under{" "}
        <button className="underline" onClick={() => go("connectors")}>Connectors</button>.
      </p>
    </div>
  );
}

// ---- Export ----

function ExportMenu({ ws, from, to, group, currency }: { ws: string; from: string; to: string; group: Group; currency: string }) {
  const link = (dataset: string, format = "csv") => {
    const q = new URLSearchParams({ dataset, format, from, to, currency, ...(dataset === "report" ? { group_by: group === "none" ? "sequence" : group } : {}) });
    return `/v1/workspaces/${ws}/outcomes/export?${q}`;
  };
  const download = (href: string) => {
    const a = document.createElement("a");
    a.href = href;
    a.click();
  };
  return (
    <Menu
      label="Export"
      items={[
        { label: "Report as CSV", onClick: () => download(link("report")) },
        { label: "Meetings as CSV", onClick: () => download(link("meetings")) },
        { label: "Deals as CSV", onClick: () => download(link("deals")) },
        { label: "Spend as CSV", onClick: () => download(link("spend")) },
        { label: "Everything as NDJSON (for a warehouse)", onClick: () => ["report", "meetings", "deals", "spend"].forEach((d, i) => setTimeout(() => download(link(d, "ndjson")), i * 400)) },
      ]}
    />
  );
}
