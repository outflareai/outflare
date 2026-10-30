import { useEffect, useMemo, useState } from "react";
import type { Report, ReportMetrics, Sequence } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { ErrorText, field, Loading, PageHeader, Stat, Stats } from "../ui";
import { DataTable } from "@/components/data-table";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useBrand } from "@/lib/brand";

type Group = "day" | "week" | "sequence" | "step" | "mailbox" | "domain" | "brand";
const groups: { id: Group; label: string }[] = [
  { id: "day", label: "Day" },
  { id: "week", label: "Week" },
  { id: "sequence", label: "Sequence" },
  { id: "step", label: "Step" },
  { id: "mailbox", label: "Mailbox" },
  { id: "domain", label: "Domain" },
  { id: "brand", label: "Brand" },
];
const periods = [7, 30, 90, 365];

const pct = (v: number | null | undefined) => (v === null || v === undefined ? "–" : `${v}%`);
const zone = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";

// Everything outreach did, by any grain. The API panel shows the same call
// for scripts and agents.
export function Reports({ ws }: { ws: string }) {
  const [days, setDays] = useState(30);
  const [group, setGroup] = useState<Group>("day");
  const [sequence, setSequence] = useState("");
  const [sequences, setSequences] = useState<Sequence[]>([]);
  const [report, setReport] = useState<Report | null>(null);
  const [error, setError] = useState("");
  // The sidebar's brand narrows the report; with brands, it can compare them.
  const { query: brandQuery, active: brands } = useBrand(ws);
  const brandKey = brandQuery.brand ?? "";

  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/sequences", { params: { path: { ws } } }).then(({ data }) => setSequences(data?.data ?? []));
  }, [ws]);

  useEffect(() => {
    const from = new Date(Date.now() - days * 86400000).toISOString();
    setReport(null);
    api.GET("/v1/workspaces/{ws}/reports", { params: { path: { ws }, query: { from, group_by: group, timezone: zone, ...brandQuery, ...(sequence ? { sequence_id: sequence } : {}) } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setError("");
      setReport(data);
    });
  }, [ws, days, group, sequence, brandKey]); // eslint-disable-line react-hooks/exhaustive-deps

  const t = report?.totals;
  return (
    <div className="space-y-6">
      <PageHeader title="Sending reports" description="Sends, bounces, replies and opt-outs from your own records, by day, sequence, step, mailbox or domain." />
      <div className="flex flex-wrap items-center gap-2">
        <select aria-label="Period" className={field} value={days} onChange={(e) => setDays(Number(e.target.value))}>
          {periods.map((d) => <option key={d} value={d}>Last {d} days</option>)}
        </select>
        <select aria-label="Sequence" className={`${field} max-w-64`} value={sequence} onChange={(e) => setSequence(e.target.value)}>
          <option value="">All sequences</option>
          {sequences.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
        </select>
        <span className="flex-1" />
        <Tabs value={group} onValueChange={(v) => setGroup(v as Group)}>
          <TabsList className="h-8">
            {groups.filter((g) => g.id !== "brand" || brands.length > 1).map((g) => <TabsTrigger key={g.id} value={g.id} className="px-2.5 text-[12px]">{g.label}</TabsTrigger>)}
          </TabsList>
        </Tabs>
      </div>
      <ErrorText>{error}</ErrorText>
      {!report && !error && <Loading />}
      {t && report && (
        <>
          <Stats cols={5}>
            <Stat label="Sent" value={t.sent.toLocaleString()} sub={`${t.new_leads.toLocaleString()} new · ${t.follow_ups.toLocaleString()} follow-ups`} />
            <Stat label="Leads contacted" value={t.contacted.toLocaleString()} sub={t.clicks > 0 ? `${t.clicked_leads.toLocaleString()} clicked a link (${pct(t.click_rate)})` : undefined} />
            <Stat label="Reply rate" value={pct(t.reply_rate)} sub={`${t.replied_leads.toLocaleString()} leads replied`} tone={t.reply_rate !== null && t.reply_rate >= 3 ? "green" : undefined} />
            <Stat label="Bounce rate" value={pct(t.bounce_rate)} sub={`${t.bounced.toLocaleString()} bounced`} tone={t.bounce_rate === null ? undefined : t.bounce_rate >= 5 ? "red" : t.bounce_rate >= 3 ? "amber" : "green"} />
            <Stat label="Opt-outs" value={pct(t.opt_out_rate)} sub={`${t.opt_outs + t.unsubscribes} opted out`} />
          </Stats>
          {(group === "day" || group === "week") && report.rows.length > 1 && <SendsChart rows={report.rows} />}
          {report.rows.length === 0 ? (
            <p className="text-[13px] text-muted-foreground">Nothing sent in this period yet.</p>
          ) : (
            <Breakdown rows={report.rows} group={group} clicks={t.clicks > 0} />
          )}
          {(t.blocked > 0 || t.failed > 0) && (
            <p className="text-[12px] text-muted-foreground">{t.blocked.toLocaleString()} emails were held by the send policy (suppressed, outside the window, or over a limit) and {t.failed.toLocaleString()} failed to send. Messages under a sequence show each one's reason.</p>
          )}
        </>
      )}
    </div>
  );
}

type Row = Report["rows"][number];

// Sends per day or week: one series, so no legend; hover shows the numbers.
function SendsChart({ rows }: { rows: Row[] }) {
  const max = Math.max(1, ...rows.map((r) => r.sent));
  return (
    <figure className="rounded-lg border bg-card p-4 shadow-xs">
      <figcaption className="mb-3 text-[13px] font-medium">Emails sent</figcaption>
      <div className="flex h-40 items-end gap-0.5" role="img" aria-label={`Emails sent per period, highest ${max}`}>
        {rows.map((r) => (
          <div key={r.key} className="group relative flex h-full min-w-0 flex-1 items-end">
            <div className="w-full rounded-t-[4px] bg-brand/80 transition-colors group-hover:bg-brand" style={{ height: `${Math.max(r.sent ? 2 : 0, (r.sent / max) * 100)}%` }} />
            <div className="pointer-events-none absolute bottom-full left-1/2 z-10 mb-1 hidden -translate-x-1/2 whitespace-nowrap rounded-md border bg-popover px-2 py-1 text-[12px] text-popover-foreground shadow-md group-hover:block">
              <div className="font-medium">{r.name}</div>
              <div>{r.sent} sent · {r.replied_leads} replied · {r.bounced} bounced</div>
            </div>
          </div>
        ))}
      </div>
      <div className="mt-1 flex justify-between font-mono text-[11px] text-muted-foreground">
        <span>{rows[0].name}</span>
        <span>{rows[rows.length - 1].name}</span>
      </div>
    </figure>
  );
}

function Breakdown({ rows, group, clicks }: { rows: Row[]; group: Group; clicks: boolean }) {
  const header = useMemo(() => groups.find((g) => g.id === group)!.label, [group]);
  const num = (k: keyof ReportMetrics, h: string) => ({ id: k, header: h, accessorFn: (r: Row) => (r[k] as number | null) ?? -1, meta: { align: "right" as const, mono: true }, cell: ({ row: { original: r } }: { row: { original: Row } }) => (k.endsWith("rate") ? pct(r[k] as number | null) : (r[k] as number).toLocaleString()) });
  return (
    <DataTable
      data={rows}
      rowId={(r) => r.key || "none"}
      columnsMenu={false}
      columns={[
        { id: "label", header, accessorFn: (r) => (group === "day" || group === "week" || group === "step" ? r.key : r.name), cell: ({ row: { original: r } }) => <span className="font-medium">{r.name}</span> },
        num("sent", "Sent"),
        num("contacted", "Leads"),
        num("replied_leads", "Replied"),
        num("reply_rate", "Reply rate"),
        num("bounced", "Bounced"),
        num("bounce_rate", "Bounce rate"),
        num("opt_outs", "Opt-outs"),
        num("unsubscribes", "Unsubscribes"),
        ...(clicks ? [num("clicked_leads", "Clicked"), num("click_rate", "Click rate")] : []),
      ]}
    />
  );
}
