import {
  AgentPerformanceTable,
  AttributionLabel,
  type Attribution,
  type AgentPerformance,
} from "./AgentPerformance";
import { useEffect, useMemo, useState } from "react";
import { ArrowRight, RefreshCw } from "lucide-react";
import { api, errorMessage } from "@/api";
import { pickedBrandId } from "@/lib/brand";
import type { Tab } from "@/lib/paths";
import { track } from "@/lib/analytics";
import {
  Badge,
  Button,
  Card,
  ErrorText,
  Loading,
  PageHeader,
  field,
} from "@/ui";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  dateLabel,
  number,
  periods,
  stages,
  type Stage,
  type Period,
  type Counts,
  type Report,
} from "./preview";
import { Throughput } from "./Throughput";
import { ResultsGrowth } from "./ResultsGrowth";
import { previousRange, type Outcome } from "./results";
import { DiscoveryTrace } from "./DiscoveryTrace";
import { FinderLanes } from "./FinderLanes";
import { ToolFundingLink } from "./ToolFundingLink";
import { SalesReportingPreview } from "./SalesReporting";
import "./sales-reporting.css";
type Row = {
  id: string;
  company: string;
  domain?: string;
  email?: string;
  status: string;
  at: string;
  origin?: string;
  owner_id?: string;
  owner_name?: string;
  attribution?: Attribution;
  amount_minor?: number | null;
  currency?: string;
};
type Group = { stage: Stage; total: number; offset: number; leads: Row[] };
type Cost = {
  provider: string;
  label: string;
  shared: boolean;
  hour_cents: number | null;
  day_cents: number | null;
  balance_cents: number | null;
  wallet: string | null;
  balance_at: string | null;
  balance_error: boolean;
};
type Data = {
  agents?: AgentPerformance[];
  attribution_coverage?: {
    native_milestones?: number;
    known?: number;
    unavailable?: number;
  };
  source: {
    status: "connected" | "missing" | "choose";
    integration_id?: string;
    connections: { id: string; name: string }[];
  };
  generated_at: string;
  from: string;
  to: string;
  bucket: "hour" | "day";
  buckets: { at: string; counts: Partial<Counts> }[];
  totals: Partial<Counts>;
  cohort: Record<string, number> | null;
  discovery?: Discovery | null;
  tools: {
    from: string;
    to: string;
    verified_leads: number | null;
    rows: Cost[];
    coverage: string;
  } | null;
  capacity: {
    as_of: string;
    totals: {
      mailboxes: number;
      ready: number;
      warming: number;
      blocked: number;
      stale: number;
      sharedCampaigns: number;
      unknownLimits: number;
      readyConfiguredSlots: number;
    };
    sends: {
      total: { first: number; followup: number; total: number } | null;
      observedCampaigns: number;
      totalCampaigns: number;
      periods: { day: string; timezone: string }[];
    };
    sources: { label: string; observedAt: string | null; stale: boolean }[];
    warnings: string[];
  } | null;
  results: {
    amounts: {
      currency: string;
      amount_minor: number | null;
      valued: number;
      unvalued: number;
    }[];
  };
  groups?: Group[];
};
type DiscoveryCounts = Partial<
  Record<"found" | "new" | "passed" | "graded_ab" | "sent", number>
>;
// The connected portal's company discovery system. Counts and cents only.
type Discovery = {
  missions: {
    id: string;
    name: string;
    state: string;
    budget_cents: number | null;
    spent_cents: number | null;
  }[];
  totals: DiscoveryCounts;
  buckets: { at: string; counts: DiscoveryCounts }[];
  lanes: {
    total: number;
    by_status: Record<string, number>;
    rows: {
      title: string;
      method: string | null;
      status: string;
      found: number;
      new: number;
      passed: number;
    }[];
  };
  spend: {
    window_cents: number | null;
    rows: { tool: string; units: number | null; cents: number | null }[];
  };
};
const stamp = (iso: string) =>
  new Intl.DateTimeFormat("en-GB", {
    timeZone: "UTC",
    year: "numeric",
    month: "short",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  }).format(new Date(iso)) + " UTC";
const zero = () => Object.fromEntries(stages.map((s) => [s.id, 0])) as Counts;
const fine = (n: number | null) =>
  n === null
    ? "—"
    : new Intl.NumberFormat("en-US", {
        style: "currency",
        currency: "USD",
        minimumFractionDigits: 2,
        maximumFractionDigits: 4,
      }).format(n / 100);
export function SalesReporting({
  ws,
  go,
}: {
  ws: string;
  go: (tab: Tab, item?: string) => void;
}) {
  const [demo, setDemo] = useState(false);
  return demo ? (
    <>
      <Button
        variant="ghost"
        onClick={() => {
          track("ui.sales_reporting_interacted", {
            workspace_id: ws,
            outcome: "success",
            action: "return_live",
            preview: true,
          });
          setDemo(false);
        }}
      >
        ← Return to live reporting
      </Button>
      <SalesReportingPreview ws={ws} go={go} />
    </>
  ) : (
    <LiveSalesReporting ws={ws} go={go} preview={() => setDemo(true)} />
  );
}
function LiveSalesReporting({
  ws,
  go,
  preview,
}: {
  ws: string;
  go: (tab: Tab, item?: string) => void;
  preview: () => void;
}) {
  const brand = pickedBrandId();
  const [period, setPeriod] = useState<Period>("24h");
  const [view, setView] = useState<"flow" | "costs" | "results" | "discovery">(
    "flow",
  );
  const [connection, setConnection] = useState("");
  const [revision, setRevision] = useState(0);
  const [data, setData] = useState<Data>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [inspect, setInspect] = useState<{
    from: string;
    to: string;
    stages: Stage[];
  } | null>(null);
  const observe = (action: string, value?: string, count?: number) =>
    track("ui.sales_reporting_interacted", {
      workspace_id: ws,
      outcome: "success",
      action,
      value,
      count,
      preview: false,
    });
  useEffect(() => {
    if (!brand) {
      setData(undefined);
      return;
    }
    const controller = new AbortController();
    setBusy(true);
    setError("");
    setData(undefined);
    setInspect(null);
    api
      .GET("/v1/brands/{brand}/sales/reporting", {
        params: {
          path: { brand },
          query: { period, ...(connection ? { integration: connection } : {}) },
        },
        signal: controller.signal,
      })
      .then((r) => {
        if (controller.signal.aborted) return;
        if (r.error) setError(errorMessage(r.error));
        else setData(r.data as unknown as Data);
      })
      .catch((e) => {
        if (!controller.signal.aborted) setError(errorMessage(e));
      })
      .finally(() => {
        if (!controller.signal.aborted) setBusy(false);
      });
    return () => controller.abort();
  }, [brand, period, connection, revision]);
  const report = useMemo(() => {
    if (!data) return undefined;
    const p = periods.find((p) => p.id === period)!;
    const from = Date.parse(data.from),
      to = Date.parse(data.to),
      step = p.hourly ? 3_600_000 : 86_400_000;
    const indexed = new Map(
      data.buckets.map((b) => [Date.parse(b.at), b.counts]),
    );
    return {
      period: p,
      from,
      to,
      buckets: Array.from(
        { length: Math.ceil((to - from) / step) },
        (_, i) => ({
          at: from + i * step,
          counts: { ...zero(), ...indexed.get(from + i * step) },
        }),
      ),
      totals: { ...zero(), ...data.totals },
      deals: [],
    } as Report;
  }, [data, period]);
  const available = stages
    .filter(
      (s) =>
        data?.source.status === "connected" || ["sdr", "closed"].includes(s.id),
    )
    .map((s) => s.id);
  const open = (at: number, selected: Stage[]) => {
    setInspect({
      from: new Date(at).toISOString(),
      to: new Date(
        at + (report!.period.hourly ? 3_600_000 : 86_400_000),
      ).toISOString(),
      stages: selected,
    });
  };
  return (
    <div className="sales-reporting space-y-5">
      <PageHeader
        title="Sales reporting"
        description="Compare pipeline volume, follow the same leads, and see the cost of moving them forward."
        action={
          <div className="flex flex-wrap gap-2">
            <Button
              variant="ghost"
              onClick={() => {
                observe("team_activity");
                go("crm", "sales:activity");
              }}
            >
              Team activity
            </Button>
            <Button
              variant="ghost"
              onClick={() => {
                observe("preview");
                preview();
              }}
            >
              View UI demo
            </Button>
            <Button
              variant="ghost"
              disabled={busy}
              onClick={() => {
                setRevision((v) => v + 1);
                observe("refresh");
              }}
            >
              <RefreshCw className="size-3.5" />
              Refresh
            </Button>
            <Button variant="ghost" onClick={() => go("crm")}>
              Open Sales
              <ArrowRight className="size-3.5" />
            </Button>
          </div>
        }
      />
      {!brand && (
        <Card
          title="Choose a brand"
          description="Select a brand in the sidebar to see its Sales reporting."
        >
          {null}
        </Card>
      )}
      {error && <ErrorText>{error}</ErrorText>}
      {busy && <Loading />}
      {data && report && (
        <>
          <div className="flex flex-wrap items-center justify-between gap-3 text-xs text-muted-foreground">
            <span>
              <Badge
                tone={data.source.status === "connected" ? "green" : "amber"}
              >
                {data.source.status === "connected"
                  ? "Connected"
                  : "Outflare data only"}
              </Badge>{" "}
              <span className="ml-2">
                Updated {stamp(data.generated_at)} · UTC buckets
              </span>
            </span>
            <div className="flex gap-3">
              <button onClick={() => go("connectors", "leadportal")}>
                Manage data connection →
              </button>
            </div>
          </div>
          {data.source.status !== "connected" && (
            <Card
              title={
                data.source.status === "choose"
                  ? "Choose a Lead Portal connection"
                  : "Connect your Lead Portal"
              }
              description="Connect the portal to this brand for discovery, research, verification, first sends, replies, tool costs and mailbox capacity. SDR pickup and confirmed customers come from Outflare Sales."
            >
              {data.source.status === "choose" ? (
                <select
                  className={field}
                  value={connection}
                  onChange={(e) => {
                    setConnection(e.target.value);
                    observe("connection_selected");
                  }}
                >
                  <option value="">Select connection</option>
                  {data.source.connections.map((c) => (
                    <option key={c.id} value={c.id}>
                      {c.name}
                    </option>
                  ))}
                </select>
              ) : (
                <Button onClick={() => go("connectors", "leadportal")}>
                  Connect Lead Portal
                </Button>
              )}
            </Card>
          )}
          <div className="flex flex-wrap justify-between gap-3">
            <div
              className="sr-periods"
              role="group"
              aria-label="Sales reporting sections"
            >
              {(["flow", "discovery", "costs", "results"] as const).map((v) => (
                <button
                  key={v}
                  aria-pressed={view === v}
                  onClick={() => {
                    setView(v);
                    observe("view", v);
                  }}
                >
                  {
                    {
                      flow: "Live flow",
                      discovery: "Discovery",
                      costs: "Costs & capacity",
                      results: "Results",
                    }[v]
                  }
                </button>
              ))}
            </div>
            {view !== "costs" && (
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
                      setData(undefined);
                      setPeriod(p.id);
                      observe("period", p.id);
                    }}
                  >
                    {p.label}
                  </button>
                ))}
              </div>
            )}
          </div>
          {view === "flow" && (
            <>
              <div className="sr-stages">
                {stages.map((s, i) => (
                  <button
                    key={s.id}
                    className="sr-stage"
                    disabled={!available.includes(s.id)}
                    onClick={() => {
                      setInspect({
                        from: data.from,
                        to: data.to,
                        stages: [s.id],
                      });
                      observe("stage_opened", s.id);
                    }}
                  >
                    <span className="sr-stage-label">
                      <span className="sr-stage-index">{i + 1}</span>
                      {s.label}
                    </span>
                    <strong className="sr-stage-count">
                      {available.includes(s.id)
                        ? number(report.totals[s.id])
                        : "—"}
                    </strong>
                    <span className="text-xs text-muted-foreground">
                      {s.id === "closed"
                        ? "confirmed customers"
                        : s.id === "replied"
                          ? "companies with a first recorded reply"
                          : s.description}
                    </span>
                    <span className="sr-stage-note">
                      {available.includes(s.id)
                        ? "View companies →"
                        : "Connect Lead Portal"}
                    </span>
                  </button>
                ))}
              </div>
              <Throughput
                key={`${period}:${connection}`}
                report={report}
                observe={observe}
                onInspect={open}
                available={available}
              />
              {data.cohort && (
                <Card
                  title="Same companies"
                  description="Companies discovered in the selected period, followed through their latest recorded progress. Recent cohorts still need time to move forward."
                >
                  <div className="sr-cohort">
                    {(["discovered", "researched", "verified"] as const).map(
                      (s, i) => (
                        <div key={s}>
                          <span className="text-xs text-muted-foreground">
                            {stages.find((x) => x.id === s)!.label}
                          </span>
                          <strong>{number(data.cohort![s])}</strong>
                          <div className="sr-cohort-track">
                            <span
                              style={{
                                width: `${data.cohort!.discovered ? (100 * data.cohort![s]) / data.cohort!.discovered : 0}%`,
                                background: stages[i].color,
                              }}
                            />
                          </div>
                          <small>
                            {s === "discovered"
                              ? "100% of this cohort"
                              : `${data.cohort!.discovered ? Math.round((100 * data.cohort![s]) / data.cohort!.discovered) : 0}% of this cohort`}
                          </small>
                        </div>
                      ),
                    )}
                  </div>
                  <p className="mt-4 text-xs text-muted-foreground">
                    {number(data.cohort.research_waiting)} have no completed
                    research yet · {number(data.cohort.research_rejected)}{" "}
                    rejected during research ·{" "}
                    {number(data.cohort.not_verified)} researched, awaiting a
                    verified milestone. Missing later milestones are not
                    automatically lost leads.
                  </p>
                </Card>
              )}
              <div className="sr-resources">
                <LiveCosts
                  data={data}
                  onFund={(wallet) => observe("funding_opened", wallet)}
                />
                <LiveCapacity data={data} />
              </div>
            </>
          )}
          {view === "discovery" && brand && (
            <FinderLanes
              brand={brand}
              days={{ "24h": 1, "48h": 2, "7d": 7, "30d": 30, "365d": 365 }[period]}
              observe={observe}
              go={go}
            >
              <LiveDiscovery
                data={data}
                brand={brand}
                connection={connection}
                observe={observe}
              />
            </FinderLanes>
          )}
          {view === "costs" && (
            <>
              <LiveCosts
                data={data}
                onFund={(wallet) => observe("funding_opened", wallet)}
              />
              <LiveCapacity data={data} />
            </>
          )}
          {view === "results" && (
            <LiveResults
              data={data}
              brand={brand}
              period={period}
              connection={connection}
              go={go}
              observe={observe}
              onOpen={(stage) => {
                setInspect({ from: data.from, to: data.to, stages: [stage] });
                observe("stage_opened", stage);
              }}
            />
          )}
          <p className="text-xs text-muted-foreground">
            Verified counts the approval once; Bison enrollment is not a second
            funnel stage. Discovery and research use company identities. Replies
            use the first recorded email reply. SDR pickup and closure include
            Outflare records; historical portal outcomes are not imported.
          </p>
        </>
      )}
      <Dialog
        open={!!inspect}
        onOpenChange={(v) => {
          if (!v) {
            setInspect(null);
            observe("detail_closed");
          }
        }}
      >
        <DialogContent className="sr-dialog">
          <DialogHeader>
            <DialogTitle>Milestone details</DialogTitle>
            <DialogDescription>
              {inspect
                ? `${dateLabel(Date.parse(inspect.from), true)} – ${dateLabel(Date.parse(inspect.to), true)} UTC · end exclusive`
                : ""}
            </DialogDescription>
          </DialogHeader>
          {inspect && (
            <IntervalLeads
              key={`${inspect.from}:${inspect.stages.join(",")}`}
              brand={brand}
              query={inspect}
              period={period}
              connection={connection}
              go={go}
            />
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
      <span className="text-xs text-muted-foreground">{label}</span>
      <strong>{value}</strong>
      <span className="text-xs text-muted-foreground">{note}</span>
    </div>
  );
}
function LiveResults({
  data,
  brand,
  period,
  connection,
  go,
  onOpen,
  observe,
}: {
  data: Data;
  brand: string;
  period: Period;
  connection: string;
  go: (tab: Tab, item?: string) => void;
  onOpen: (stage: Outcome) => void;
  observe: (action: string, value?: string) => void;
}) {
  const [previous, setPrevious] = useState<Data>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(true);
  const [revision, setRevision] = useState(0);
  const integration = connection || data.source.integration_id;
  useEffect(() => {
    const controller = new AbortController();
    setBusy(true);
    setError("");
    setPrevious(undefined);
    api
      .GET("/v1/brands/{brand}/sales/reporting", {
        params: {
          path: { brand },
          query: {
            period,
            ...previousRange(data.from, data.to),
            ...(integration ? { integration } : {}),
          },
        },
        signal: controller.signal,
      })
      .then((r) => {
        if (controller.signal.aborted) return;
        if (r.error) setError(errorMessage(r.error));
        else setPrevious(r.data as unknown as Data);
      })
      .catch((e) => {
        if (!controller.signal.aborted) setError(errorMessage(e));
      })
      .finally(() => {
        if (!controller.signal.aborted) setBusy(false);
      });
    return () => controller.abort();
  }, [brand, data.from, data.to, period, integration, revision]);
  return (
    <>
      {error && (
        <div className="space-y-2">
          <ErrorText>Previous period: {error}</ErrorText>
          <Button
            variant="ghost"
            onClick={() => {
              setRevision((v) => v + 1);
              observe("comparison_retry");
            }}
          >
            Retry comparison
          </Button>
        </div>
      )}
      <ResultsGrowth
        current={data}
        previous={previous}
        loading={busy}
        onOpen={onOpen}
      />
      <AgentPerformanceTable
        onViewChange={(view) => observe("agent_view", view)}
        rows={data.agents}
        connected={data.source.status === "connected"}
        coverage={data.attribution_coverage}
      />
      <Card
        title="Confirmed customers"
        description="Confirmed customers recorded in Outflare. Open a company to see the owner, conversation and latest deal details."
      >
        <IntervalLeads
          key={`closed:${period}`}
          brand={brand}
          query={{ from: data.from, to: data.to, stages: ["closed"] }}
          period={period}
          connection={connection}
          go={go}
        />
      </Card>
    </>
  );
}

function LiveCosts({
  data,
  onFund,
}: {
  data: Data;
  onFund: (wallet: string) => void;
}) {
  const tools = data.tools;
  const own = tools?.rows.filter((r) => !r.shared) ?? [];
  const sum = (key: "day_cents" | "hour_cents") =>
    !tools || own.some((r) => r[key] === null)
      ? null
      : own.reduce((n, r) => n + r[key]!, 0);
  const day = sum("day_cents");
  const unit = (n: number | null) =>
    n === null || !tools?.verified_leads ? null : n / tools.verified_leads;
  return (
    <Card
      title="Tools & spend"
      description={
        tools
          ? `${dateLabel(Date.parse(tools.from), true)} – ${dateLabel(Date.parse(tools.to), true)} UTC · completed hours`
          : data.source.status === "connected"
            ? "Recorded tool costs are unavailable. Refresh to retry."
            : "Connect Lead Portal to see recorded costs."
      }
    >
      {tools && (
        <>
          <div className="sr-summary-grid">
            <Summary
              label="Per verified company"
              value={fine(unit(day))}
              note="Recorded Sales costs / verified companies"
            />
            <Summary
              label="Last complete hour"
              value={fine(sum("hour_cents"))}
              note="Sales-attributed spend"
            />
            <Summary
              label="Last 24 complete hours"
              value={fine(day)}
              note={`${tools.verified_leads === null ? "—" : number(tools.verified_leads)} verified companies`}
            />
          </div>
          <div className="sr-table-wrap">
            <table className="sr-table">
              <thead>
                <tr>
                  <th>Tool</th>
                  <th>Last hour</th>
                  <th>Last 24h</th>
                  <th>Per company</th>
                  <th>Wallet balance</th>
                </tr>
              </thead>
              <tbody>
                {tools.rows.map((r) => (
                  <tr key={r.provider}>
                    <td>
                      {r.label}
                      {r.shared && (
                        <small className="block text-muted-foreground">
                          Shared · excluded from Sales total
                        </small>
                      )}
                    </td>
                    <td>{fine(r.hour_cents)}</td>
                    <td>{fine(r.day_cents)}</td>
                    <td>
                      {r.shared ? "Unallocated" : fine(unit(r.day_cents))}
                    </td>
                    <td>
                      {fine(r.balance_cents)}
                      <div>
                        <ToolFundingLink wallet={r.wallet} onOpen={onFund} />
                      </div>
                      {r.wallet && (
                        <small className="block text-muted-foreground">
                          {r.wallet} wallet
                          {r.balance_error ? " · refresh failed" : ""}
                        </small>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
              <tfoot>
                <tr>
                  <th>Sales total</th>
                  <td>{fine(sum("hour_cents"))}</td>
                  <td>{fine(day)}</td>
                  <td>{fine(unit(day))}</td>
                  <td>Shared wallets</td>
                </tr>
              </tfoot>
            </table>
          </div>
          <p className="mt-3 text-xs text-muted-foreground">
            Recorded usage, including allocated tool charges; excludes mailbox
            subscriptions and staff time. Shared usage has no reliable Sales
            allocation. Wallet balances are shared across tools and teams and
            are never added together. — means unavailable or no verified
            companies.
          </p>
        </>
      )}
    </Card>
  );
}
const words = (value: string) => {
  const text = value.replace(/_/g, " ");
  return text.charAt(0).toUpperCase() + text.slice(1);
};
const share = (part: number, whole: number) =>
  whole > 0 ? `${Math.round((100 * part) / whole)}%` : "—";
type Lane = {
  id: string;
  title: string;
  method: string | null;
  origin: string | null;
  status: string;
  segment: string | null;
  idea: string | null;
  why: string | null;
  verdict: string | null;
  learned: string | null;
  recipe: Partial<
    Record<"queries" | "urls" | "seeds" | "describe" | "jobs", string[]>
  >;
  found: number;
  new: number;
  known: number;
  screened: number;
  passed: number;
  ai_cents: number | null;
  expected_size: number | null;
  last_activity_at: string | null;
};
const recipeParts = [
  ["queries", "Searches it runs"],
  ["describe", "Descriptions it matches"],
  ["jobs", "Job-ad phrases it looks for"],
  ["urls", "Pages it reads"],
  ["seeds", "Companies it starts from"],
] as const;
// How the connected portal looks for companies: one row per lane, opened for its recipe and lessons.
function LaneBoard({
  brand,
  connection,
  total,
  statuses,
  observe,
}: {
  brand: string;
  connection: string;
  total: number;
  statuses: Record<string, number>;
  observe: (action: string, value?: string, count?: number) => void;
}) {
  const [status, setStatus] = useState("");
  const [offset, setOffset] = useState(0);
  const [open, setOpen] = useState("");
  const [page, setPage] = useState<{ total: number; rows: Lane[] }>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(true);
  useEffect(() => {
    const controller = new AbortController();
    setBusy(true);
    setError("");
    api
      .GET("/v1/brands/{brand}/sales/reporting/lanes", {
        params: {
          path: { brand },
          query: {
            offset,
            ...(status ? { status } : {}),
            ...(connection ? { integration: connection } : {}),
          },
        },
        signal: controller.signal,
      })
      .then((r) => {
        if (controller.signal.aborted) return;
        if (r.error) setError(errorMessage(r.error));
        else
          setPage(
            (r.data?.lanes as unknown as { total: number; rows: Lane[] }) ?? {
              total: 0,
              rows: [],
            },
          );
        setBusy(false);
      })
      .catch((e) => {
        // Leaving the view cancels the read; that is not an error to show.
        if (controller.signal.aborted) return;
        setError(errorMessage(e));
        setBusy(false);
      });
    return () => controller.abort();
  }, [brand, connection, status, offset]);
  const choices = Object.entries(statuses).sort((a, b) => b[1] - a[1]);
  return (
    <Card
      title="Search lanes"
      description={`${number(total)} ways of finding companies. Open a lane to see how it searches, why it exists and what it learned. Counts are all-time; the lanes that passed the most companies come first.`}
    >
      <div
        className="sr-periods"
        role="group"
        aria-label="Lane state"
        style={{ flexWrap: "wrap" }}
      >
        {[["", total] as [string, number], ...choices].map(([value, n]) => (
          <button
            key={value}
            aria-pressed={status === value}
            onClick={() => {
              setStatus(value);
              setOffset(0);
              setOpen("");
              observe("lane_status", value || "all");
            }}
          >
            {value ? words(value) : "All"} · {number(n)}
          </button>
        ))}
      </div>
      {error && <ErrorText>{error}</ErrorText>}
      {!page && busy && <Loading />}
      {page && (
        <>
          <div className="sr-table-wrap mt-4" aria-busy={busy}>
            <table className="sr-table">
              <thead>
                <tr>
                  <th>Lane</th>
                  <th>State</th>
                  <th>Found</th>
                  <th>New</th>
                  <th>Passed</th>
                  <th>Pass rate</th>
                </tr>
              </thead>
              <tbody>
                {page.rows.length === 0 && (
                  <tr>
                    <td colSpan={6}>No lanes in this state.</td>
                  </tr>
                )}
                {page.rows.map((r) => (
                  <LaneRow
                    key={r.id}
                    lane={r}
                    brand={brand}
                    connection={connection}
                    observe={observe}
                    open={open === r.id}
                    toggle={() => {
                      setOpen(open === r.id ? "" : r.id);
                      if (open !== r.id) observe("lane_opened", r.status);
                    }}
                  />
                ))}
              </tbody>
            </table>
          </div>
          {page.total > 50 && (
            <div className="mt-3 flex items-center justify-between gap-3">
              <span className="text-xs text-muted-foreground">
                {number(offset + 1)}–{number(Math.min(offset + 50, page.total))}{" "}
                of {number(page.total)}
              </span>
              <span className="flex gap-2">
                <Button
                  variant="ghost"
                  disabled={busy || offset === 0}
                  onClick={() => {
                    setOffset(Math.max(0, offset - 50));
                    setOpen("");
                    observe("lane_page", "previous");
                  }}
                >
                  Previous
                </Button>
                <Button
                  variant="ghost"
                  disabled={busy || offset + 50 >= page.total}
                  onClick={() => {
                    setOffset(offset + 50);
                    setOpen("");
                    observe("lane_page", "next");
                  }}
                >
                  Next
                </Button>
              </span>
            </div>
          )}
        </>
      )}
    </Card>
  );
}
function LaneRow({
  lane: r,
  open,
  toggle,
  brand,
  connection,
  observe,
}: {
  brand: string;
  connection: string;
  observe: (action: string, value?: string) => void;
  lane: Lane;
  open: boolean;
  toggle: () => void;
}) {
  const notes = [
    ["The idea", r.idea],
    ["Why it was proposed", r.why],
    ["Latest verdict", r.verdict],
    ["What it learned", r.learned],
  ].filter((n): n is [string, string] => !!n[1]);
  const lists = recipeParts.filter(([key]) => r.recipe?.[key]?.length);
  return (
    <>
      <tr>
        <td>
          <button
            type="button"
            className="sr-lead-link text-left"
            aria-expanded={open}
            onClick={toggle}
          >
            {r.title}
          </button>
          <small className="block text-muted-foreground">
            {[r.method && words(r.method), r.origin && words(r.origin)]
              .filter(Boolean)
              .join(" · ")}
          </small>
        </td>
        <td>{words(r.status)}</td>
        <td>{number(r.found)}</td>
        <td>{number(r.new)}</td>
        <td>{number(r.passed)}</td>
        <td>{share(r.passed, r.new)}</td>
      </tr>
      {open && (
        <tr>
          <td colSpan={6}>
            <div className="grid gap-3 py-2 text-sm">
              {r.segment && (
                <p>
                  <strong>Who it targets:</strong> {r.segment}
                </p>
              )}
              {notes.map(([label, text]) => (
                <p key={label}>
                  <strong>{label}:</strong> {text}
                </p>
              ))}
              {lists.map(([key, label]) => (
                <div key={key}>
                  <strong>{label}</strong>
                  <ul className="mt-1 list-disc pl-5 text-muted-foreground">
                    {r.recipe[key]!.map((item, i) => (
                      <li key={i} style={{ overflowWrap: "anywhere" }}>
                        {item}
                      </li>
                    ))}
                  </ul>
                </div>
              ))}
              {notes.length + lists.length === 0 && !r.segment && (
                <p className="text-muted-foreground">
                  The portal recorded no detail for this lane.
                </p>
              )}
              <DiscoveryTrace
                brand={brand}
                connection={connection}
                lane={r.id}
                observe={observe}
              />
              <p className="text-xs text-muted-foreground">
                {number(r.known)} already held · {number(r.screened)} screened ·
                AI spend {fine(r.ai_cents)}
                {r.expected_size === null
                  ? ""
                  : ` · expected about ${number(r.expected_size)} companies`}
                {r.last_activity_at
                  ? ` · last worked ${stamp(r.last_activity_at)}`
                  : " · not worked yet"}
              </p>
            </div>
          </td>
        </tr>
      )}
    </>
  );
}
function LiveDiscovery({
  data,
  brand,
  connection,
  observe,
}: {
  data: Data;
  brand: string;
  connection: string;
  observe: (action: string, value?: string, count?: number) => void;
}) {
  const d = data.discovery;
  const count = (key: keyof DiscoveryCounts) => d?.totals[key] ?? 0;
  const unit = (n: number) =>
    !d || d.spend.window_cents === null || n === 0
      ? null
      : d.spend.window_cents / n;
  return (
    <>
      <Card
        title="Company discovery"
        description={
          d
            ? `${dateLabel(Date.parse(data.from), true)} – ${dateLabel(Date.parse(data.to), true)} UTC · companies found by search lanes, screened, graded and sent to research`
            : data.source.status === "connected"
              ? "The connected portal does not report discovery. Refresh to retry."
              : "Connect Lead Portal to see company discovery."
        }
      >
        {d && (
          <>
            <div className="sr-summary-grid">
              <Summary
                label="Found"
                value={number(count("found"))}
                note={`${number(count("new"))} not already held (${share(count("new"), count("found"))})`}
              />
              <Summary
                label="Passed screening"
                value={number(count("passed"))}
                note={`${share(count("passed"), count("new"))} of new companies`}
              />
              <Summary
                label="Graded A or B"
                value={number(count("graded_ab"))}
                note="Second read by the larger model"
              />
              <Summary
                label="Sent to research"
                value={number(count("sent"))}
                note={`${fine(unit(count("sent")))} recorded spend each`}
              />
              <Summary
                label="Spend in this period"
                value={fine(d.spend.window_cents)}
                note="Recorded discovery costs"
              />
            </div>
            {d.missions.length > 0 && (
              <div className="sr-table-wrap mt-4">
                <table className="sr-table">
                  <thead>
                    <tr>
                      <th>Mission</th>
                      <th>State</th>
                      <th>Spent in total</th>
                      <th>Spending cap</th>
                      <th>Left</th>
                    </tr>
                  </thead>
                  <tbody>
                    {d.missions.map((m) => (
                      <tr key={m.id}>
                        <td>{m.name}</td>
                        <td>{words(m.state)}</td>
                        <td>{fine(m.spent_cents)}</td>
                        <td>
                          {m.budget_cents === null
                            ? "No cap"
                            : fine(m.budget_cents)}
                        </td>
                        <td>
                          {m.budget_cents === null || m.spent_cents === null
                            ? "—"
                            : fine(Math.max(0, m.budget_cents - m.spent_cents))}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </>
        )}
      </Card>
      {d && (
        <>
          <Card title="Agent activity">
            <DiscoveryTrace
              brand={brand}
              connection={connection}
              observe={observe}
            />
          </Card>
          <LaneBoard
            brand={brand}
            connection={connection}
            total={d.lanes.total}
            statuses={d.lanes.by_status}
            observe={observe}
          />
        </>
      )}
      {d && (
        <Card
          title="Discovery tools & spend"
          description="What discovery used in this period. — means the cost of that tool is not recorded."
        >
          <div className="sr-table-wrap">
            <table className="sr-table">
              <thead>
                <tr>
                  <th>Tool</th>
                  <th>Uses</th>
                  <th>Recorded spend</th>
                </tr>
              </thead>
              <tbody>
                {d.spend.rows.map((r) => (
                  <tr key={r.tool}>
                    <td>{r.tool}</td>
                    <td>
                      {r.units === null ? "—" : number(Math.round(r.units))}
                    </td>
                    <td>{fine(r.cents)}</td>
                  </tr>
                ))}
              </tbody>
              <tfoot>
                <tr>
                  <th>Total</th>
                  <td></td>
                  <td>{fine(d.spend.window_cents)}</td>
                </tr>
              </tfoot>
            </table>
          </div>
        </Card>
      )}
    </>
  );
}
function LiveCapacity({ data }: { data: Data }) {
  const c = data.capacity;
  return (
    <Card
      title="Sales mailbox capacity"
      description={
        c
          ? `Saved observations · ${stamp(c.as_of)}`
          : data.source.status === "connected"
            ? "No Sales mailbox snapshot is available yet."
            : "Connect Lead Portal to see capacity."
      }
    >
      {c ? (
        <>
          <div className="sr-summary-grid">
            <Summary
              label="Mailboxes used by Sales"
              value={number(c.totals.mailboxes)}
              note={`${c.totals.ready} ready · ${c.totals.warming} warming`}
            />
            <Summary
              label="Ready configured slots"
              value={number(c.totals.readyConfiguredSlots)}
              note={`${c.totals.unknownLimits} unknown limits · shared capacity, not reserved quota`}
            />
            <Summary
              label="Observed sends"
              value={c.sends.total ? number(c.sends.total.total) : "—"}
              note={`${c.sends.observedCampaigns}/${c.sends.totalCampaigns} campaigns observed`}
            />
          </div>
          <p className="mt-3 text-xs text-muted-foreground">
            {c.totals.sharedCampaigns} mailboxes shared with other campaigns ·{" "}
            {c.totals.stale} stale observations · {c.totals.blocked} blocked.{" "}
            {c.sends.periods.map((p) => `${p.day} (${p.timezone})`).join(", ")}
          </p>
          {c.sources.map((s) => (
            <p key={s.label} className="mt-1 text-xs text-muted-foreground">
              {s.label}: {s.observedAt ? stamp(s.observedAt) : "not observed"}
              {s.stale ? " · stale" : ""}
            </p>
          ))}
          {c.warnings.length > 0 && (
            <p className="mt-2 text-xs text-amber-600">
              {c.warnings.join(" ")}
            </p>
          )}
        </>
      ) : null}
    </Card>
  );
}
function IntervalLeads({
  brand,
  query,
  period,
  connection,
  go,
}: {
  brand: string;
  query: { from: string; to: string; stages: Stage[] };
  period: Period;
  connection: string;
  go: (tab: Tab, item?: string) => void;
}) {
  const [groups, setGroups] = useState<Group[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(true);
  const [page, setPage] = useState<{ stage?: Stage; offset: number }>({
    offset: 0,
  });
  useEffect(() => {
    const controller = new AbortController();
    setBusy(true);
    setError("");
    api
      .GET("/v1/brands/{brand}/sales/reporting/leads", {
        params: {
          path: { brand },
          query: {
            period,
            from: query.from,
            to: query.to,
            stages: (page.stage ? [page.stage] : query.stages).join(","),
            offset: page.offset,
            ...(connection ? { integration: connection } : {}),
          },
        },
        signal: controller.signal,
      })
      .then((r) => {
        if (controller.signal.aborted) return;
        if (r.error) setError(errorMessage(r.error));
        else {
          const incoming = (r.data?.groups ?? []) as unknown as Group[];
          setGroups((old) =>
            page.stage
              ? old.map((g) => incoming.find((i) => i.stage === g.stage) ?? g)
              : incoming,
          );
        }
      })
      .catch((e) => {
        if (!controller.signal.aborted) setError(errorMessage(e));
      })
      .finally(() => {
        if (!controller.signal.aborted) setBusy(false);
      });
    return () => controller.abort();
  }, [brand, query.from, query.to, query.stages, period, connection, page]);
  return (
    <div className="space-y-5">
      {error && <ErrorText>{error}</ErrorText>}
      {busy && <Loading />}
      {groups.length > 0 && (
        <div
          className="sticky top-0 z-10 flex flex-wrap gap-2 border-b bg-background py-2"
          aria-label="Selected stage counts"
        >
          {groups.map((g) => (
            <span key={g.stage} className="rounded-md border px-3 py-2 text-sm">
              {stages.find((s) => s.id === g.stage)!.label}{" "}
              <strong>{number(g.total)}</strong>
            </span>
          ))}
        </div>
      )}
      {groups.map((g) => (
        <section key={g.stage}>
          <div className="mb-2 flex items-center justify-between">
            <h3 className="text-sm font-semibold">
              {stages.find((s) => s.id === g.stage)!.label}{" "}
              <span className="text-muted-foreground">{number(g.total)}</span>
            </h3>
            <span className="text-xs text-muted-foreground">
              {g.total
                ? `${g.offset + 1}–${Math.min(g.total, g.offset + 50)} of ${number(g.total)}`
                : "No companies in this interval"}
            </span>
          </div>
          {g.leads.length > 0 && (
            <div className="sr-table-wrap">
              <table className="sr-table">
                <thead>
                  <tr>
                    <th>Company / person</th>
                    <th>Uploaded by</th>
                    <th>Current status</th>
                    <th>Milestone time</th>
                    {g.stage === "closed" && <th>Expected monthly revenue</th>}
                  </tr>
                </thead>
                <tbody>
                  {g.leads.map((l) => (
                    <tr key={l.id}>
                      <td>
                        {l.origin === "outflare" ? (
                          <button
                            className="font-medium text-primary"
                            onClick={() => go("crm", `sales:lead:${l.id}`)}
                          >
                            {l.company || l.id} →
                          </button>
                        ) : (
                          <span className="font-medium">{l.company}</span>
                        )}
                        <small className="block text-muted-foreground">
                          {l.email || l.domain || l.owner_name || "Unassigned"}
                        </small>
                      </td>
                      <td>
                        <AttributionLabel value={l.attribution} />
                      </td>
                      <td>{l.status}</td>
                      <td>{stamp(l.at)}</td>
                      {g.stage === "closed" && (
                        <td>
                          {l.amount_minor == null
                            ? "Not recorded"
                            : new Intl.NumberFormat("en-US", {
                                style: "currency",
                                currency: l.currency ?? "USD",
                              }).format(l.amount_minor / 100)}
                        </td>
                      )}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          {g.total > 50 && (
            <div className="mt-2 flex justify-end gap-3">
              <Button
                variant="ghost"
                disabled={busy || g.offset === 0}
                onClick={() =>
                  setPage({
                    stage: g.stage,
                    offset: Math.max(0, g.offset - 50),
                  })
                }
              >
                Previous
              </Button>
              <Button
                variant="ghost"
                disabled={busy || g.offset + 50 >= g.total}
                onClick={() =>
                  setPage({ stage: g.stage, offset: g.offset + 50 })
                }
              >
                Next
              </Button>
            </div>
          )}
        </section>
      ))}
    </div>
  );
}
