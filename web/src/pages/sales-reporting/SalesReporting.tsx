import { Throughput } from "./Throughput";
import { ResultsGrowth } from "./ResultsGrowth";
import { ToolFundingLink } from "./ToolFundingLink";
import { useMemo, useState } from "react";
import {
  ArrowRight,
  BarChart3,
  Check,
  ChevronLeft,
  ChevronRight,
  Coins,
  Mail,
} from "lucide-react";
import type { Tab } from "@/lib/paths";
import { track } from "@/lib/analytics";
import { Badge, Button, Card, PageHeader } from "@/ui";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  costPerLead,
  dateLabel,
  exampleLeads,
  money,
  number,
  periodLabel,
  periods,
  reportFor,
  spend,
  stages,
  tools,
  unitCost,
  type Lead,
  type Period,
  type Report,
  type Stage,
} from "./preview";
import "./sales-reporting.css";

type View = "flow" | "costs" | "results";
type ReportAction =
  | "funding_opened"
  | "chart_style"
  | "interval_opened"
  | "view"
  | "period"
  | "metric"
  | "bucket"
  | "stage_opened"
  | "lead_opened"
  | "detail_closed"
  | "detail_back"
  | "outcome"
  | "page";
type Observe = (action: ReportAction, value?: string, count?: number) => void;
const views = [
  { id: "flow", label: "Live flow", icon: BarChart3 },
  { id: "costs", label: "Costs & capacity", icon: Coins },
  { id: "results", label: "Results", icon: Check },
] as const;

export function SalesReportingPreview({
  ws,
  go,
}: {
  ws: string;
  go: (tab: Tab, item?: string) => void;
}) {
  const [view, setView] = useState<View>("flow");
  const [period, setPeriod] = useState<Period>("24h");
  const [detail, setDetail] = useState<Stage | null>(null);
  const [lead, setLead] = useState<Lead | null>(null);
  const report = useMemo(() => reportFor(period), [period]);
  const observe: Observe = (action, value, count) =>
    track("ui.sales_reporting_interacted", {
      workspace_id: ws,
      outcome: "success",
      action,
      value,
      count,
      preview: true,
    });
  const selectView = (next: View) => {
    setView(next);
    observe("view", next);
  };
  const openStage = (stage: Stage) => {
    setLead(null);
    setDetail(stage);
    observe("stage_opened", stage);
  };
  const openLead = (row: Lead) => {
    setLead(row);
    observe("lead_opened", row.outcome ?? "active");
  };
  const closeDetails = () => {
    setDetail(null);
    setLead(null);
    observe("detail_closed");
  };
  const stage = stages.find((s) => s.id === detail);

  return (
    <div className="sales-reporting space-y-5">
      <PageHeader
        title="Sales reporting"
        description="From the first discovery to the final outcome. One view of volume, cost and results."
        action={
          <Button variant="ghost" onClick={() => go("crm")}>
            Open Sales <ArrowRight className="size-3.5" />
          </Button>
        }
      />
      <div className="sr-preview" role="note">
        <Badge tone="amber">Preview</Badge>
        <span>
          Fictional example data · 11 Nov 2026, 10:00 UTC. Explore the dashboard,
          then return to live reporting for your brand’s actual data.
        </span>
      </div>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <Tabs value={view} onValueChange={(v) => selectView(v as View)}>
          <TabsList className="h-10" aria-label="Sales reporting sections">
            {views.map(({ id, label, icon: Icon }) => (
              <TabsTrigger
                key={id}
                value={id}
                className="gap-2 px-3 text-[13px]"
              >
                <Icon className="size-3.5" />
                {label}
              </TabsTrigger>
            ))}
          </TabsList>
        </Tabs>
        {view !== "costs" ? (
          <div
            className="sr-periods"
            role="group"
            aria-label="Reporting period"
          >
            {periods.map((p) => (
              <button
                key={p.id}
                aria-pressed={period === p.id}
                onClick={() => {
                  setPeriod(p.id);
                  observe("period", p.id);
                }}
              >
                {p.label}
              </button>
            ))}
          </div>
        ) : (
          <span className="text-xs text-muted-foreground">
            Rolling spend windows · mailbox capacity today · UTC
          </span>
        )}
      </div>

      {view === "flow" && (
        <>
          <section aria-label="Sales milestones">
            <div className="mb-3 flex flex-wrap items-baseline justify-between gap-2">
              <h2 className="text-sm font-semibold">The full journey</h2>
              <span className="text-xs text-muted-foreground">
                {periodLabel(report)} · click a milestone to explore
              </span>
            </div>
            <div className="sr-stages">
              {stages.map((s, i) => (
                <button
                  className="sr-stage"
                  key={s.id}
                  onClick={() => openStage(s.id)}
                  aria-label={`${s.label}: ${number(report.totals[s.id])}. View details`}
                >
                  <span className="sr-stage-label">
                    <span className="sr-stage-index">{i + 1}</span>
                    {s.label}
                    <ChevronRight className="ml-auto size-3.5 text-muted-foreground" />
                  </span>
                  <strong className="sr-stage-count">
                    {number(report.totals[s.id])}
                  </strong>
                  <span className="text-xs text-muted-foreground">
                    {s.description}
                  </span>
                  <span className="sr-stage-note">{s.queue}</span>
                </button>
              ))}
            </div>
            <p className="mt-3 text-xs text-muted-foreground">
              Milestones count events during this period; companies can enter at
              different times. Queue notes show the current snapshot. Bison sync
              is included under Verified.
            </p>
          </section>
          <Throughput
            key={period}
            report={report}
            observe={(action, value, count) =>
              observe(action as ReportAction, value, count)
            }
          />
          <div className="sr-resources">
            <ToolSpend
              compact
              onExpand={() => selectView("costs")}
              onFund={(wallet) => observe("funding_opened", wallet)}
            />
            <MailboxCapacity />
          </div>
        </>
      )}

      {view === "costs" && (
        <>
          <div className="sr-summary-grid">
            <Summary
              label="Total cost per company"
              value={money(
                (costPerLead(spend.dayCents, spend.verifiedLeads) ?? 0) * 100,
              )}
              note="Tool spend / verified company · last 24h"
            />
            <Summary
              label="Spent in the last hour"
              value={money(spend.hourCents)}
              note="Across all five tools · USD"
            />
            <Summary
              label="Spent in the last 24 hours"
              value={money(spend.dayCents)}
              note={`${number(spend.verifiedLeads)} verified companies in the same window`}
            />
          </div>
          <ToolSpend onFund={(wallet) => observe("funding_opened", wallet)} />
          <MailboxCapacity expanded />
        </>
      )}

      {view === "results" && (
        <>
          <ResultsGrowth
            current={previewResults(report)}
            previous={previewResults(reportFor(period, -1))}
            onOpen={openStage}
          />
          <Card
            title="Confirmed customers"
            description={`Customers confirmed ${periodLabel(report)}. Open a company for its owner, use case and optional monthly amount.`}
          >
            <ClosedDeals
              key={period}
              report={report}
              onOpen={openLead}
              observe={(action, value, count) =>
                observe(action as ReportAction, value, count)
              }
            />
          </Card>
        </>
      )}

      <Dialog
        open={detail !== null || lead !== null}
        onOpenChange={(open) => {
          if (!open) closeDetails();
        }}
      >
        <DialogContent className="sr-detail-dialog sm:max-w-3xl">
          <DialogHeader>
            <DialogTitle>{lead ? lead.company : stage?.label}</DialogTitle>
            <DialogDescription>
              {lead
                ? `${lead.contact} · ${lead.useCase}`
                : `${number(report.totals[detail ?? "discovered"])} events · ${periodLabel(report)} · example data`}
            </DialogDescription>
          </DialogHeader>
          {lead ? (
            <>
              {detail && (
                <button
                  className="sr-text-link w-fit"
                  onClick={() => {
                    setLead(null);
                    observe("detail_back", detail);
                  }}
                >
                  <ChevronLeft className="size-3.5" />
                  Back to {stage?.label.toLowerCase()}
                </button>
              )}
              <LeadDetails lead={lead} stage={detail ?? "closed"} />
            </>
          ) : (
            stage && (
              <>
                {stage.id === "verified" && (
                  <div className="sr-detail-note">
                    <strong>Verification and Bison delivery, together</strong>
                    <p>
                      A company is counted once when verification passes. Its Bison
                      sync status stays inside this milestone: 32 waiting now,
                      including 6 retrying. A first email is counted only after
                      send confirmation.
                    </p>
                  </div>
                )}
                {stage.id === "sdr" && (
                  <div className="sr-detail-note">
                    <strong>Picked up means a rep accepted ownership</strong>
                    <p>
                      See who owns the conversation, where it is now and what
                      happens next. The example rows below illustrate that view.
                    </p>
                  </div>
                )}
                {stage.id === "closed" ? (
                  <ClosedDeals
                    report={report}
                    onOpen={openLead}
                    observe={(action, value, count) =>
                      observe(action as ReportAction, value, count)
                    }
                  />
                ) : (
                  <>
                    <div className="flex items-center justify-between gap-2 text-xs">
                      <span className="text-muted-foreground">
                        Illustrative records · 4 examples
                      </span>
                      <Badge>{stage.queue} now</Badge>
                    </div>
                    <LeadTable
                      leads={exampleLeads(stage.id, report)}
                      onOpen={openLead}
                    />
                  </>
                )}
              </>
            )
          )}
        </DialogContent>
      </Dialog>
    </div>
  );
}

function Summary({
  label,
  value,
  note,
}: {
  label: string;
  value: string;
  note: string;
}) {
  return (
    <div className="sr-summary">
      <span className="text-xs font-medium text-muted-foreground">{label}</span>
      <strong>{value}</strong>
      <span className="text-xs text-muted-foreground">{note}</span>
    </div>
  );
}

function ToolSpend({
  compact = false,
  onExpand,
  onFund,
}: {
  compact?: boolean;
  onExpand?: () => void;
  onFund: (wallet: string) => void;
}) {
  return (
    <Card
      title="Tools & spend"
      description={
        compact
          ? `Last 24h · ${unitCost(spend.dayCents)} per verified company`
          : "Each tool’s contribution to the cost of one verified company."
      }
      action={
        onExpand && (
          <button className="sr-text-link" onClick={onExpand}>
            Full breakdown <ArrowRight className="size-3.5" />
          </button>
        )
      }
    >
      <div className="sr-table-scroll">
        <table className="sr-table sr-cost-table">
          <thead>
            <tr>
              <th>Tool</th>
              <th>Last hour</th>
              <th>Last 24h</th>
              <th>
                Per company <span className="sr-table-hint">24h</span>
              </th>
              <th>Balance</th>
            </tr>
          </thead>
          <tbody>
            {tools.map((tool) => (
              <tr key={tool.name}>
                <td>
                  <span className="font-medium">{tool.name}</span>
                  {!compact && (
                    <span className="sr-table-sub">{tool.purpose}</span>
                  )}
                </td>
                <td>{money(tool.hourCents)}</td>
                <td>{money(tool.dayCents)}</td>
                <td className="font-medium">{unitCost(tool.dayCents)}</td>
                <td>
                  {tool.balanceCents === null ? (
                    <span className="text-muted-foreground">Postpaid</span>
                  ) : (
                    money(tool.balanceCents)
                  )}
                  <div>
                    <ToolFundingLink wallet={tool.wallet} onOpen={onFund} />
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
          <tfoot>
            <tr>
              <th>Total</th>
              <td>{money(spend.hourCents)}</td>
              <td>{money(spend.dayCents)}</td>
              <td>{unitCost(spend.dayCents)}</td>
              <td>—</td>
            </tr>
          </tfoot>
        </table>
      </div>
      <p className="mt-3 text-[11px] leading-relaxed text-muted-foreground">
        Per company = each tool’s Sales spend in the last 24h ÷{" "}
        {number(spend.verifiedLeads)} verified companies in that window. Total uses
        unrounded costs. Balances are shared provider wallets, not Sales
        allocations. USD; excludes mailbox subscriptions and rep time.
      </p>
    </Card>
  );
}

function MailboxCapacity({ expanded = false }: { expanded?: boolean }) {
  const today = reportFor("24h")
    .buckets.filter((b) => new Date(b.at).getUTCDate() === 11)
    .reduce((n, b) => n + b.counts.emailed, 0);
  const used = today + 410;
  const remaining = 3240 - used;
  return (
    <Card
      title="Sales mailbox capacity"
      description="Today · current Sales allocation · UTC"
      action={<Mail className="size-4 text-muted-foreground" />}
    >
      <div className="sr-capacity-stats">
        <div>
          <strong>
            54 <span>/ 60</span>
          </strong>
          <span>mailboxes ready / assigned</span>
        </div>
        <div>
          <strong>
            {number(used)} <span>/ 3,240</span>
          </strong>
          <span>daily sending slots used</span>
        </div>
      </div>
      <div
        className="sr-capacity-meter"
        role="meter"
        aria-label="Daily Sales sending capacity used"
        aria-valuemin={0}
        aria-valuemax={3240}
        aria-valuenow={used}
      >
        <span style={{ width: `${(used / 3240) * 100}%` }} />
        <span
          className="sr-capacity-reserved"
          style={{ width: `${(320 / 3240) * 100}%` }}
        />
      </div>
      <div className="mt-2 flex flex-wrap justify-between gap-2 text-xs text-muted-foreground">
        <span>{number(today)} first emails · 410 follow-ups</span>
        <span>320 slots reserved for follow-ups</span>
      </div>
      <div className="sr-capacity-bottom">
        <div>
          <span className="text-xs text-muted-foreground">
            Room for new companies today
          </span>
          <strong>
            {number(remaining - 320)} <span>emails</span>
          </strong>
        </div>
        <Badge tone="green">419 ready to send</Badge>
      </div>
      <p className="mt-3 text-xs text-muted-foreground">
        4 warming up · 2 disconnected. Capacity reflects the Sales allocation,
        not every mailbox in the workspace.
      </p>
      {expanded && (
        <div className="sr-table-scroll mt-5">
          <table className="sr-table">
            <thead>
              <tr>
                <th>Sales pool</th>
                <th>Assigned</th>
                <th>Ready</th>
                <th>Daily slots</th>
              </tr>
            </thead>
            <tbody>
              <tr>
                <td>Company outreach</td>
                <td>48</td>
                <td>44</td>
                <td>2,640</td>
              </tr>
              <tr>
                <td>Personal outreach</td>
                <td>12</td>
                <td>10</td>
                <td>600</td>
              </tr>
            </tbody>
          </table>
          <p className="mt-3 text-xs text-muted-foreground">
            Both pools are EmailBison accounts. A rep’s own Gmail is separate.
          </p>
        </div>
      )}
    </Card>
  );
}

function previewResults(report: Report) {
  const valued = report.deals.filter((d) => d.valueCents !== null);
  return {
    from: new Date(report.from).toISOString(),
    to: new Date(report.to).toISOString(),
    source: { status: "connected" },
    totals: report.totals,
    results: {
      amounts: [
        {
          currency: "USD",
          amount_minor: valued.length
            ? valued.reduce((n, d) => n + d.valueCents!, 0)
            : null,
          valued: valued.length,
          unvalued: report.deals.length - valued.length,
        },
      ],
    },
  };
}

function ClosedDeals({
  report,
  onOpen,
  observe,
}: {
  report: Report;
  onOpen: (lead: Lead) => void;
  observe: Observe;
}) {
  const [page, setPage] = useState(0);
  const rows = report.deals;
  const pageSize = 6;
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <span className="text-xs text-muted-foreground">
          {number(rows.length)} customers · monthly amount is optional
        </span>
      </div>
      <LeadTable
        leads={rows.slice(page * pageSize, (page + 1) * pageSize)}
        onOpen={onOpen}
        closed
      />
      <div className="flex items-center justify-between gap-2 text-xs text-muted-foreground">
        <span>
          {rows.length
            ? `${page * pageSize + 1}–${Math.min((page + 1) * pageSize, rows.length)} of ${number(rows.length)}`
            : "No customers confirmed in this period"}
        </span>
        <div className="flex gap-2">
          <Button
            variant="ghost"
            disabled={page === 0}
            onClick={() => {
              setPage(page - 1);
              observe("page", "previous");
            }}
          >
            Previous
          </Button>
          <Button
            variant="ghost"
            disabled={(page + 1) * pageSize >= rows.length}
            onClick={() => {
              setPage(page + 1);
              observe("page", "next");
            }}
          >
            Next
          </Button>
        </div>
      </div>
    </div>
  );
}

function LeadTable({
  leads,
  onOpen,
  closed = false,
}: {
  leads: Lead[];
  onOpen: (lead: Lead) => void;
  closed?: boolean;
}) {
  return (
    <div className="sr-table-scroll">
      <table className="sr-table">
        <thead>
          <tr>
            <th>Company / use case</th>
            <th>Owner</th>
            <th>{closed ? "Outcome" : "Status / next action"}</th>
            {closed && (
              <>
                <th>Expected monthly revenue</th>
                <th>Confirmed</th>
              </>
            )}
          </tr>
        </thead>
        <tbody>
          {leads.map((row) => (
            <tr key={row.id}>
              <td>
                <button className="sr-lead-link" onClick={() => onOpen(row)}>
                  {row.company}
                  <ArrowRight className="size-3" />
                </button>
                <span className="sr-table-sub">{row.useCase}</span>
              </td>
              <td className="whitespace-nowrap">{row.owner}</td>
              <td>
                <Badge tone={row.outcome === "customer" ? "green" : "grey"}>
                  {row.status}
                </Badge>
                {!closed && <span className="sr-table-sub">{row.next}</span>}
              </td>
              {closed && (
                <>
                  <td>
                    {row.valueCents === null ? "Not entered" : money(row.valueCents)}
                  </td>
                  <td className="whitespace-nowrap">
                    {dateLabel(row.closedAt!)}
                  </td>
                </>
              )}
            </tr>
          ))}
          {!leads.length && (
            <tr>
              <td
                colSpan={closed ? 5 : 3}
                className="py-8 text-center text-muted-foreground"
              >
                No companies match this view.
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}

function LeadDetails({ lead, stage }: { lead: Lead; stage: Stage }) {
  const through = stages.findIndex((s) => s.id === stage);
  const timeline = [
    { label: "First email", at: lead.firstEmailAt, index: 3 },
    { label: "Replied", at: lead.repliedAt, index: 4 },
    { label: "SDR picked up", at: lead.pickedUpAt, index: 5 },
    {
      label: "Customer confirmed",
      at: lead.closedAt,
      index: 6,
    },
  ].filter((e) => e.index <= through && e.at !== undefined);
  return (
    <div className="space-y-5">
      <div className="sr-detail-facts">
        <div>
          <span>Contact</span>
          <strong>{lead.email}</strong>
        </div>
        <div>
          <span>Owner</span>
          <strong>{lead.owner}</strong>
        </div>
        <div>
          <span>Status</span>
          <strong>{lead.status}</strong>
        </div>
        <div>
          <span>Next action</span>
          <strong>{lead.next}</strong>
        </div>
      </div>
      {lead.outcome && (
        <div className="sr-summary-grid">
          <Summary
            label="Expected monthly revenue"
            value={
              lead.valueCents === null ? "Not entered" : money(lead.valueCents)
            }
            note="Optional monthly amount, separate from payments"
          />
        </div>
      )}
      {timeline.length > 0 && (
        <section>
          <h3 className="mb-3 text-sm font-semibold">Timeline</h3>
          <ol className="sr-timeline">
            {timeline.map((event) => (
              <li key={event.label}>
                <span className="sr-timeline-dot">
                  <Check className="size-3" />
                </span>
                <div>
                  <strong>{event.label}</strong>
                  <span>
                    {dateLabel(event.at!, true)}{" "}
                    {new Date(event.at!).getUTCFullYear()} · UTC
                  </span>
                </div>
              </li>
            ))}
          </ol>
        </section>
      )}
      <p className="text-xs text-muted-foreground">
        Fictional customer example. Live reporting opens the actual Sales conversation.
        Customer confirmation does not require a payment or revenue amount.
      </p>
    </div>
  );
}
