// Discovery lanes fed by Outflare itself. First the Lane leaderboard of the
// selected profile (LaneLeaderboard.tsx: each lane's yield, status, lesson
// and a person's controls), then the live flow: the companies discovery
// agents (and the lead finder's own searches) send into the brand's lead
// finder runs, by lane, and where each one is now, from being checked to a
// person in the campaign. The lead finder is an Outflare Cloud feature: where the
// server runs it (the lead_finder capability), its /cloud/lead-finder/lanes
// routes answer, called without the typed client as LeadFinder.tsx does;
// elsewhere this shows nothing and the connected portal's view stays.
//
// The counting rules are the server's: a company counts once per run, in
// the lane that sent it first; one the workspace had is only "already
// known"; every company is at exactly one stage, and a lane's company list
// filters by the same stage its count used.

import { useCallback, useEffect, useState, type ReactNode } from "react";
import { ArrowLeft, RefreshCw } from "lucide-react";
import { errorMessage } from "@/api";
import type { Tab } from "@/lib/paths";
import { useCapabilities } from "@/lib/cloud";
import { Badge, Button, Card, ErrorText, Loading } from "@/ui";
import { number } from "./preview";
import { LaneLeaderboard } from "./LaneLeaderboard";

export type LaneStage =
  | "checking"
  | "not_a_fit"
  | "to_review"
  | "adding"
  | "finding_people"
  | "ready"
  | "in_crm"
  | "in_campaign"
  | "stopped";

// The stages in the order of the flow, as people read them.
export const laneStages: { id: LaneStage; label: string; note: string }[] = [
  { id: "checking", label: "Being checked", note: "read, researched and judged" },
  { id: "not_a_fit", label: "Not a fit", note: "rejected, unreadable or failed verification" },
  { id: "to_review", label: "Waiting for review", note: "fits; a person adds or dismisses it" },
  { id: "adding", label: "Being added", note: "on its way into People" },
  { id: "finding_people", label: "Finding people", note: "people and addresses being found and checked" },
  { id: "ready", label: "Ready for the campaign", note: "a person waits for a place or an approval" },
  { id: "in_crm", label: "In People", note: "no campaign on this profile" },
  { id: "in_campaign", label: "In the campaign", note: "a person is in the sequencer's campaign" },
  { id: "stopped", label: "Stopped", note: "fit, then stopped on the way" },
];

export type Lane = {
  run_id: string;
  profile_id: string | null;
  profile_name: string;
  run_status: string;
  standing: boolean;
  run_error_code: string | null;
  source: string;
  source_name: string;
  lane: string;
  agents: string[];
  proposed: number;
  already_known: number;
  companies: number;
  stages: Record<LaneStage, number>;
  fit: number;
  people: number;
  emails: number;
  in_campaign: number;
  stops: Record<string, number>;
  waits: Record<string, number>;
  first_at: string | null;
  last_at: string | null;
};
type Totals = Pick<Lane, "proposed" | "already_known" | "companies" | "stages" | "fit" | "people" | "emails" | "in_campaign" | "stops">;
type Board = { days: number; generated_at: string; totals: Totals; lanes: Lane[] };
type Person = {
  id: string;
  first_name: string;
  last_name: string;
  title: string;
  has_email: boolean;
  status: string;
  stop_code: string | null;
  record_id: string | null;
};
type Company = {
  domain: string;
  name: string;
  source_url: string;
  evidence: string;
  agent: string;
  stage: LaneStage;
  code: string | null;
  candidate_id: string | null;
  verdict: string | null;
  score: number | null;
  record_id: string | null;
  arrived_at: string;
  people: Person[];
};
type Page = { data: Company[]; total: number; next_cursor: string | null };
type Failure = { error: { code: string; message: string } };

// get reads one of the lead finder's routes with the page's session.
async function get<T>(path: string, signal?: AbortSignal): Promise<{ data?: T; error?: Failure; status?: number }> {
  try {
    const res = await fetch(path, { credentials: "same-origin", signal });
    const json = await res.json().catch(() => ({}));
    return res.ok ? { data: json as T, status: res.status } : { error: json as Failure, status: res.status };
  } catch (e) {
    if ((e as Error)?.name === "AbortError") throw e;
    return { error: { error: { code: "network_error", message: "Could not reach Outflare. Check your connection and try again." } } };
  }
}

// Words for the codes the lead finder gives, the rest read as written.
const codeWords: Record<string, string> = {
  rejected: "Not a fit for the profile",
  unreachable: "Site could not be read",
  verification_rejected: "Failed verification",
  dismissed: "Dismissed by a person",
  already_known: "Already in People",
  no_people: "Nobody named there",
  no_email: "No verified address",
  opted_out: "Opted out",
  bounced: "Bounced before",
  customer: "Already a customer",
  blocklisted: "On the blocklist",
  free_mail: "Free mail domain",
  graded_out: "Graded C or D",
  open_deal: "Has an open deal",
  in_another_campaign: "In another campaign",
  sequencer_refused: "The sequencer left them out",
  skipped: "Skipped by a person",
  campaign_ready: "Waiting for a place in the campaign",
  connector_needed: "A connection is missing",
  daily_verification_budget_reached: "Verification budget used for today",
  daily_ai_budget_reached: "AI budget used for today",
};
export const codeLabel = (code: string) => codeWords[code] ?? code.charAt(0).toUpperCase() + code.slice(1).replace(/_/g, " ");
// What a person's status says, in words.
const personWords: Record<string, string> = {
  finding: "address being found",
  added: "in People",
  ready: "ready for the campaign",
  enrolled: "in the campaign",
  stopped: "stopped",
};
const laneName = (l: Pick<Lane, "lane" | "source_name">) => l.lane || `${l.source_name} (no lane named)`;
const stamp = (iso: string | null) =>
  iso
    ? new Intl.DateTimeFormat("en-GB", { month: "short", day: "2-digit", hour: "2-digit", minute: "2-digit", timeZone: "UTC" }).format(new Date(iso)) + " UTC"
    : "—";
const share = (part: number, whole: number) => (whole > 0 ? `${Math.round((100 * part) / whole)}%` : "—");
const keyOf = (l: Lane) => `${l.run_id}\u0000${l.source}\u0000${l.lane}`;
const LIVE_MS = 30_000;

export function FinderLanes({
  brand,
  days,
  observe,
  go,
  children,
}: {
  brand: string;
  days: number;
  observe: (action: string, value?: string, count?: number) => void;
  go: (tab: Tab, item?: string) => void;
  // The rest of the Discovery view, shown under the board and hidden while
  // a lane's own page is open.
  children?: ReactNode;
}) {
  const caps = useCapabilities();
  const runs = !!caps?.includes("lead_finder");
  const [board, setBoard] = useState<Board>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [tick, setTick] = useState(0);
  const [open, setOpen] = useState<string>("");
  // A leaderboard lane's own page (or Add a lane) replaces the rest of the view.
  const [boardPage, setBoardPage] = useState(false);
  const onBoardPage = useCallback((v: boolean) => setBoardPage(v), []);
  // The server lists the capability but has no lanes routes yet.
  const [absent, setAbsent] = useState(false);
  const base = `/v1/brands/${brand}/cloud/lead-finder`;
  useEffect(() => {
    if (!runs) return;
    const controller = new AbortController();
    setBusy(true);
    get<Board>(`${base}/lanes?days=${days}`, controller.signal)
      .then((r) => {
        if (controller.signal.aborted) return;
        // A server without the lead finder's routes has nothing to show.
        if (r.status === 404) {
          setBoard(undefined);
          setAbsent(true);
        } else if (r.error) setError(errorMessage(r.error));
        else {
          setError("");
          setBoard(r.data);
        }
        setBusy(false);
      })
      .catch(() => undefined);
    return () => controller.abort();
  }, [runs, base, days, tick]);
  // Live: read again every 30 seconds while the page is visible.
  useEffect(() => {
    if (!runs) return;
    const id = window.setInterval(() => {
      if (document.visibilityState === "visible") setTick((t) => t + 1);
    }, LIVE_MS);
    return () => window.clearInterval(id);
  }, [runs]);
  const lane = board?.lanes.find((l) => keyOf(l) === open);
  if (lane)
    return (
      <LanePage
        base={base}
        lane={lane}
        observe={observe}
        go={go}
        onBack={() => {
          setOpen("");
          observe("finder_lane_closed");
        }}
      />
    );
  if (!runs) return <>{children}</>;
  return (
    <>
      <LaneLeaderboard brand={brand} observe={observe} go={go} onOpenChange={onBoardPage} />
      {!boardPage && !absent && (
      <Card
        title="Lane flow, live"
        description="Companies your discovery agents and Outflare's own company searches send into the flow, by lane, and where each one is now. Updates every 30 seconds."
        action={
          <Button
            variant="ghost"
            disabled={busy}
            onClick={() => {
              setTick((t) => t + 1);
              observe("finder_lanes_refreshed");
            }}
          >
            <RefreshCw className="size-3.5" />
            Refresh
          </Button>
        }
      >
        {error && <ErrorText>{error}</ErrorText>}
        {!board && busy && <Loading />}
        {board && <LaneBoard board={board} onOpen={(l) => {
          setOpen(keyOf(l));
          observe("finder_lane_opened", l.source.startsWith("proposal") ? "proposal" : l.source);
        }} />}
      </Card>
      )}
      {!boardPage && children}
    </>
  );
}

function Flow({ totals, onStage, selected }: { totals: Totals; onStage?: (s: LaneStage | "") => void; selected?: LaneStage | "" }) {
  return (
    <div className="sr-stages">
      {laneStages.map((s, i) => (
        <button
          key={s.id}
          className="sr-stage"
          aria-pressed={selected === s.id}
          disabled={!onStage}
          onClick={() => onStage?.(selected === s.id ? "" : s.id)}
        >
          <span className="sr-stage-label">
            <span className="sr-stage-index">{i + 1}</span>
            {s.label}
          </span>
          <strong className="sr-stage-count">{number(totals.stages[s.id] ?? 0)}</strong>
          <span className="text-xs text-muted-foreground">{s.note}</span>
        </button>
      ))}
    </div>
  );
}

function Arrivals({ t }: { t: Totals }) {
  return (
    <div className="sr-summary-grid">
      <div className="sr-summary">
        <span className="text-xs text-muted-foreground">Sent</span>
        <strong>{number(t.proposed)}</strong>
        <span className="text-xs text-muted-foreground">{number(t.already_known)} already known, not added again</span>
      </div>
      <div className="sr-summary">
        <span className="text-xs text-muted-foreground">New companies</span>
        <strong>{number(t.companies)}</strong>
        <span className="text-xs text-muted-foreground">each counted once, in the lane that sent it first</span>
      </div>
      <div className="sr-summary">
        <span className="text-xs text-muted-foreground">Fit</span>
        <strong>{number(t.fit)}</strong>
        <span className="text-xs text-muted-foreground">{share(t.fit, t.companies - (t.stages.checking ?? 0))} of those checked</span>
      </div>
      <div className="sr-summary">
        <span className="text-xs text-muted-foreground">People found</span>
        <strong>{number(t.people)}</strong>
        <span className="text-xs text-muted-foreground">{number(t.emails)} with an address</span>
      </div>
      <div className="sr-summary">
        <span className="text-xs text-muted-foreground">In the campaign</span>
        <strong>{number(t.in_campaign)}</strong>
        <span className="text-xs text-muted-foreground">people pushed to the sequencer</span>
      </div>
    </div>
  );
}

function Reasons({ title, counts }: { title: string; counts: Record<string, number> }) {
  const rows = Object.entries(counts).sort((a, b) => b[1] - a[1]);
  if (!rows.length) return null;
  return (
    <div className="text-sm">
      <strong>{title}</strong>
      <ul className="mt-1 grid gap-0.5 text-muted-foreground">
        {rows.map(([code, n]) => (
          <li key={code}>
            {codeLabel(code)} · {number(n)}
          </li>
        ))}
      </ul>
    </div>
  );
}

function LaneBoard({ board, onOpen }: { board: Board; onOpen: (l: Lane) => void }) {
  if (!board.lanes.length)
    return (
      <p className="text-sm text-muted-foreground">
        No company was sent into this brand's flows in the last {number(board.days)} days. A discovery agent sends companies with the
        profile's proposals route (or the propose_discovery_companies tool) and names its lane; they show here as they arrive.
      </p>
    );
  return (
    <div className="grid gap-4">
      <Arrivals t={board.totals} />
      <Flow totals={board.totals} />
      <Reasons title="Why companies stopped" counts={board.totals.stops} />
      <div className="sr-table-wrap">
        <table className="sr-table">
          <thead>
            <tr>
              <th>Lane</th>
              <th>Flow</th>
              <th>Sent</th>
              <th>New</th>
              <th>Being checked</th>
              <th>Fit</th>
              <th>Stopped</th>
              <th>In the campaign</th>
              <th>Last sent</th>
            </tr>
          </thead>
          <tbody>
            {board.lanes.map((l) => (
              <tr key={keyOf(l)}>
                <td>
                  <button type="button" className="sr-lead-link text-left" onClick={() => onOpen(l)}>
                    {laneName(l)}
                  </button>
                  <small className="block text-muted-foreground">
                    {[l.lane ? l.source_name : "", l.agents.join(", ")].filter(Boolean).join(" · ")}
                  </small>
                </td>
                <td>
                  {l.profile_name || "No profile"}{" "}
                  <Badge tone={l.run_status === "running" && !l.run_error_code ? "green" : l.run_error_code ? "amber" : "grey"}>
                    {l.run_error_code ? `paused: ${codeLabel(l.run_error_code)}` : l.standing && l.run_status === "running" ? "open" : l.run_status}
                  </Badge>
                </td>
                <td>{number(l.proposed)}</td>
                <td>{number(l.companies)}</td>
                <td>{number(l.stages.checking ?? 0)}</td>
                <td>
                  {number(l.fit)} <small className="text-muted-foreground">{share(l.fit, l.companies - (l.stages.checking ?? 0))}</small>
                </td>
                <td>{number((l.stages.stopped ?? 0) + (l.stages.not_a_fit ?? 0))}</td>
                <td>{number(l.in_campaign)}</td>
                <td>{stamp(l.last_at)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// LanePage is one lane's own page: its flow, why its companies stopped or
// wait, and its companies with their people, a page at a time.
function LanePage({
  base,
  lane,
  observe,
  go,
  onBack,
}: {
  base: string;
  lane: Lane;
  observe: (action: string, value?: string, count?: number) => void;
  go: (tab: Tab, item?: string) => void;
  onBack: () => void;
}) {
  const [stage, setStage] = useState<LaneStage | "">("");
  const [cursors, setCursors] = useState<string[]>([]);
  const [page, setPage] = useState<Page>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(true);
  const cursor = cursors[cursors.length - 1] ?? "";
  useEffect(() => {
    const controller = new AbortController();
    setBusy(true);
    const q = new URLSearchParams({ run_id: lane.run_id, source: lane.source, lane: lane.lane, limit: "50" });
    if (stage) q.set("stage", stage);
    if (cursor) q.set("cursor", cursor);
    get<Page>(`${base}/lanes/companies?${q}`, controller.signal)
      .then((r) => {
        if (controller.signal.aborted) return;
        if (r.error) setError(errorMessage(r.error));
        else {
          setError("");
          setPage(r.data);
        }
        setBusy(false);
      })
      .catch(() => undefined);
    return () => controller.abort();
  }, [base, lane.run_id, lane.source, lane.lane, stage, cursor]);
  const open = (id: string, what: string) => {
    observe("finder_record_opened", what);
    go("people", id);
  };
  return (
    <div className="grid gap-4">
      <div>
        <Button variant="ghost" onClick={onBack}>
          <ArrowLeft className="size-3.5" />
          All lanes
        </Button>
      </div>
      <Card
        title={laneName(lane)}
        description={[
          lane.lane ? lane.source_name : "",
          lane.agents.length ? `Agents: ${lane.agents.join(", ")}` : "",
          `Flow: ${lane.profile_name || "No profile"}`,
          `First sent ${stamp(lane.first_at)}`,
          `last sent ${stamp(lane.last_at)}`,
        ]
          .filter(Boolean)
          .join(" · ")}
      >
        <div className="grid gap-4">
          <Arrivals t={lane} />
          <Flow
            totals={lane}
            selected={stage}
            onStage={(s) => {
              setStage(s);
              setCursors([]);
              observe("finder_lane_stage", s || "all");
            }}
          />
          <div className="grid gap-4 sm:grid-cols-2">
            <Reasons title="Why companies stopped" counts={lane.stops} />
            <Reasons title="What companies wait for" counts={lane.waits} />
          </div>
        </div>
      </Card>
      <Card
        title={stage ? laneStages.find((s) => s.id === stage)!.label : "Every company in this lane"}
        description={page ? `${number(page.total)} ${page.total === 1 ? "company" : "companies"}, newest first. Select a stage above to see only those.` : undefined}
      >
        {error && <ErrorText>{error}</ErrorText>}
        {!page && busy && <Loading />}
        {page && (
          <>
            <div className="sr-table-wrap" aria-busy={busy}>
              <table className="sr-table">
                <thead>
                  <tr>
                    <th>Company</th>
                    <th>Stage</th>
                    <th>Why</th>
                    <th>People</th>
                    <th>Found by</th>
                    <th>Arrived</th>
                  </tr>
                </thead>
                <tbody>
                  {page.data.length === 0 && (
                    <tr>
                      <td colSpan={6}>No company of this lane is at this stage.</td>
                    </tr>
                  )}
                  {page.data.map((c) => (
                    <tr key={c.domain}>
                      <td>
                        {c.record_id ? (
                          <button type="button" className="sr-lead-link text-left" onClick={() => open(c.record_id!, "company")}>
                            {c.name || c.domain}
                          </button>
                        ) : (
                          <span>{c.name || c.domain}</span>
                        )}
                        <small className="block text-muted-foreground" style={{ overflowWrap: "anywhere" }}>
                          {c.domain}
                          {c.evidence ? ` · ${c.evidence}` : ""}
                        </small>
                      </td>
                      <td>{laneStages.find((s) => s.id === c.stage)?.label ?? c.stage}</td>
                      <td>{c.code ? codeLabel(c.code) : "—"}</td>
                      <td>
                        {c.people.length === 0
                          ? "—"
                          : c.people.map((p) => {
                              const name = [p.first_name, p.last_name].filter(Boolean).join(" ") || "Shared inbox";
                              const state = p.status === "stopped" && p.stop_code ? codeLabel(p.stop_code) : (personWords[p.status] ?? p.status);
                              return (
                                <span key={p.id} className="block">
                                  {p.record_id ? (
                                    <button type="button" className="sr-lead-link text-left" onClick={() => open(p.record_id!, "person")}>
                                      {name}
                                    </button>
                                  ) : (
                                    name
                                  )}
                                  <small className="text-muted-foreground">
                                    {" "}
                                    {[p.title, state, p.has_email ? "" : "no address"].filter(Boolean).join(" · ")}
                                  </small>
                                </span>
                              );
                            })}
                      </td>
                      <td>{c.agent || "—"}</td>
                      <td>{stamp(c.arrived_at)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            {(cursors.length > 0 || page.next_cursor) && (
              <div className="mt-3 flex justify-end gap-2">
                <Button
                  variant="ghost"
                  disabled={busy || cursors.length === 0}
                  onClick={() => {
                    setCursors(cursors.slice(0, -1));
                    observe("finder_lane_page", "previous");
                  }}
                >
                  Previous
                </Button>
                <Button
                  variant="ghost"
                  disabled={busy || !page.next_cursor}
                  onClick={() => {
                    setCursors([...cursors, page.next_cursor!]);
                    observe("finder_lane_page", "next");
                  }}
                >
                  Next
                </Button>
              </div>
            )}
          </>
        )}
      </Card>
    </div>
  );
}
