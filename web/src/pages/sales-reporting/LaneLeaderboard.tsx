// The Lane leaderboard: a profile's discovery lanes (one idea x one way of
// finding companies x one segment) with what each found, how much of it was
// new, what passed, what it cost and what it taught, the shared record every
// agent reads before it works, and a person's controls on a lane. It is the
// earlier Sales Discovery leaderboard on Outflare's own lane registry (the
// lead finder, an Outflare Cloud feature): the routes are the hosted
// extension's, called without the typed client as LeadFinder.tsx does.
//
// Statuses are set on the server;
// this page only shows them and offers a person's controls. Lane text
// (titles, recipes, lessons, notes) is written by agents and shown as text.

import { useEffect, useMemo, useState, type ReactNode } from "react";
import { ArrowLeft, Plus, RefreshCw } from "lucide-react";
import type { CustomerProfile } from "@outflare/sdk";
import { api, errorMessage } from "@/api";
import type { Tab } from "@/lib/paths";
import { Badge, Button, Card, ErrorText, field, Loading, ask, toast } from "@/ui";
import { number } from "./preview";
import { codeLabel, laneStages, type LaneStage } from "./FinderLanes";

// ---------------------------------------------------------------------------
// The wire shapes of the lane registry routes.

export type LaneStatus = "proposed" | "probing" | "productive" | "low_yield" | "cooling_down" | "paused" | "rejected";
type Recipe = { queries?: string[]; urls?: string[]; seeds?: string[]; describe?: string[]; jobs?: string[]; refresh_days?: number };
type Cost = { agent_cents: number; agent_requests: number; agent_unpriced: number; outflare_cents: number; total_cents: number; per_pass_cents: number | null };
export type RegistryLane = {
  id: string;
  profile_id: string;
  name: string;
  title: string;
  hypothesis: string;
  kind: string | null;
  signal: string | null;
  segment: string;
  recipe: Recipe | null;
  origin: string;
  parent_id: string | null;
  status: LaneStatus;
  priority: number;
  why: string | null;
  learned: string | null;
  est_size: number | null;
  claimed_by: string | null;
  lease_expires_at: string | null;
  next_revisit_at: string | null;
  score_pending: boolean;
  waiting_for: number;
  created_by: string;
  created_at: string;
  last_activity_at: string | null;
  // What uploads said, counted at upload time.
  found: number;
  new: number;
  known: number;
  repeat: number;
  known_by?: Record<string, number>;
  repeat_by?: Record<string, number>;
  // What Outflare's own checks made of the lane's new companies, live.
  companies: number;
  checking: number;
  screened: number;
  passed: number;
  errors: number;
  known_later: number;
  stopped_at_intake?: Record<string, number>;
  grades?: Record<string, number>;
  new_rate: number | null;
  pass_rate: number | null;
  cost: Cost;
};
type Note = { id: string; lane_id: string | null; lane: string | null; author: string; kind: string; text: string; created_at: string };
type Settings = {
  probe_size: number;
  batch_size: number;
  precheck_size: number;
  min_pass_rate: number;
  min_new_rate: number;
  exploration_share: number;
  review_mode: string;
  lease_seconds: number;
};
type Totals = Record<
  | "lanes" | "proposed" | "probing" | "scaling" | "dead" | "exhausted" | "paused" | "rejected"
  | "found" | "new" | "known" | "repeat" | "companies" | "screened" | "passed" | "errors" | "known_later"
  | "agent_cents" | "agent_requests" | "outflare_cents" | "total_cents",
  number
>;
export type Board = {
  profile_id: string;
  generated_at: string;
  settings: Settings;
  totals: Totals;
  lanes: RegistryLane[];
  notes: Note[];
  notes_text: string;
  covered_text?: string;
};
type Company = {
  domain: string;
  name: string;
  evidence: string;
  agent: string;
  stage: LaneStage;
  code: string | null;
  grade: string | null;
  reasons: string[] | null;
  record_id: string | null;
  arrived_at: string;
};
type Page = { data: Company[]; total: number; next_cursor: string | null };
type Failure = { error: { code: string; message: string } };

// send reaches the lead finder's routes with the page's session.
async function send<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<{ data?: T; error?: Failure; status?: number }> {
  try {
    const res = await fetch(path, {
      method,
      credentials: "same-origin",
      signal,
      headers: body ? { "Content-Type": "application/json" } : {},
      body: body ? JSON.stringify(body) : undefined,
    });
    const json = await res.json().catch(() => ({}));
    return res.ok ? { data: json as T, status: res.status } : { error: json as Failure, status: res.status };
  } catch (e) {
    if ((e as Error)?.name === "AbortError") throw e;
    return { error: { error: { code: "network_error", message: "Could not reach Outflare. Check your connection and try again." } } };
  }
}

// ---------------------------------------------------------------------------
// Words: the lane statuses in the loop's words, as the earlier board had them.

const STATUS: Record<LaneStatus, { label: string; tone: "green" | "blue" | "grey" | "amber" | "red" }> = {
  proposed: { label: "Proposed", tone: "grey" },
  probing: { label: "Probing", tone: "blue" },
  productive: { label: "Scaling", tone: "green" },
  low_yield: { label: "Dead", tone: "red" },
  cooling_down: { label: "Exhausted", tone: "amber" },
  paused: { label: "Paused", tone: "grey" },
  rejected: { label: "Rejected", tone: "red" },
};
const FILTERS: [LaneStatus | "all", string][] = [
  ["all", "All"],
  ["productive", "Scaling"],
  ["probing", "Probing"],
  ["proposed", "Proposed"],
  ["low_yield", "Dead"],
  ["cooling_down", "Exhausted"],
];
const KIND: Record<string, string> = { list: "List", signal: "Signal", lookalike: "Lookalike" };
const ORIGIN: Record<string, string> = { learned: "learned from another lane", human: "suggested by a person", initial: "" };
const SIGNAL: Record<string, string> = { hiring: "Hiring", stack: "Stack", size: "Size", content: "Content", footprint: "Footprint", other: "Other signal" };
const NOTE_KIND: Record<string, string> = { finding: "Finding", dead_end: "Dead end", trait: "Trait", tip: "Tip" };
const SMALL_SAMPLE = 10;
const LIVE_MS = 30_000;
const pct = (num: number, den: number) => (den > 0 ? `${Math.round((num / den) * 100)}%` : "—");
const usd = (cents: number) => new Intl.NumberFormat("en-US", { style: "currency", currency: "USD", maximumFractionDigits: cents < 100 ? 3 : 2 }).format(cents / 100);
const day = (iso: string | null) => (iso ? new Intl.DateTimeFormat("en-GB", { day: "2-digit", month: "short", year: "numeric", timeZone: "UTC" }).format(new Date(iso)) : "—");
const stamp = (iso: string | null) =>
  iso ? new Intl.DateTimeFormat("en-GB", { month: "short", day: "2-digit", hour: "2-digit", minute: "2-digit", timeZone: "UTC" }).format(new Date(iso)) + " UTC" : "—";
const statusOf = (s: string) => STATUS[s as LaneStatus] ?? { label: s, tone: "grey" as const };
const leased = (l: RegistryLane) => !!l.claimed_by && !!l.lease_expires_at && Date.parse(l.lease_expires_at) > Date.now();
const recipeLists: [keyof Recipe, string][] = [
  ["queries", "Searches"],
  ["urls", "Lists"],
  ["seeds", "Seeds"],
  ["describe", "Described as"],
  ["jobs", "Job ads"],
];

type Observe = (action: string, value?: string, count?: number) => void;

export function LaneLeaderboard({ brand, observe, go, onOpenChange }: { brand: string; observe: Observe; go: (tab: Tab, item?: string) => void; onOpenChange: (open: boolean) => void }) {
  const [profiles, setProfiles] = useState<CustomerProfile[]>();
  const [profile, setProfile] = useState("");
  const [board, setBoard] = useState<Board>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [tick, setTick] = useState(0);
  const [filter, setFilter] = useState<LaneStatus | "all">("all");
  const [open, setOpen] = useState("");
  const [adding, setAdding] = useState(false);
  // The server runs the lead finder but not the lane registry yet.
  const [absent, setAbsent] = useState(false);
  const base = `/v1/brands/${brand}/cloud/lead-finder/profiles/${profile}/lanes`;

  useEffect(() => {
    api
      .GET("/v1/brands/{brand}/ideal-customer-profiles", { params: { path: { brand } } })
      .then(({ data, error }) => {
        if (error) setError(errorMessage(error));
        const list = (data?.data ?? []) as CustomerProfile[];
        setProfiles(list);
        setProfile((p) => (list.some((x) => x.id === p) ? p : (list.find((x) => x.status === "active") ?? list[0])?.id ?? ""));
      })
      .catch((e) => setError(errorMessage(e)));
  }, [brand]);

  useEffect(() => {
    if (!profile) return;
    const controller = new AbortController();
    setBusy(true);
    send<Board>("GET", `${base}/board`, undefined, controller.signal)
      .then((r) => {
        if (controller.signal.aborted) return;
        // A server without the lane registry answers 404: the rest of the view stays.
        if (r.status === 404) {
          setAbsent(true);
          setBoard(undefined);
        } else if (r.error) setError(errorMessage(r.error));
        else {
          setError("");
          setAbsent(false);
          setBoard(r.data);
        }
        setBusy(false);
      })
      .catch(() => undefined);
    return () => controller.abort();
  }, [base, profile, tick]);

  // Live: read again every 30 seconds while the page is visible.
  useEffect(() => {
    if (!profile) return;
    const id = window.setInterval(() => {
      if (document.visibilityState === "visible") setTick((t) => t + 1);
    }, LIVE_MS);
    return () => window.clearInterval(id);
  }, [profile]);

  useEffect(() => onOpenChange(!!open || adding), [open, adding, onOpenChange]);

  const lane = board?.lanes.find((l) => l.id === open);
  if (lane && board)
    return (
      <LanePage
        base={base}
        brand={brand}
        lane={lane}
        board={board}
        observe={observe}
        go={go}
        onChanged={() => setTick((t) => t + 1)}
        onOpenLane={(id) => {
          setOpen(id);
          observe("finder_board_lane_parent_opened");
        }}
        onBack={() => {
          setOpen("");
          observe("finder_board_lane_closed");
        }}
      />
    );
  if (adding && board)
    return (
      <AddLane
        base={base}
        observe={observe}
        onBack={() => setAdding(false)}
        onAdded={(id) => {
          setAdding(false);
          setTick((t) => t + 1);
          if (id) setOpen(id);
        }}
      />
    );
  if (absent || (profiles && profiles.length === 0)) return null;

  const lanes = board?.lanes ?? [];
  const shown = lanes.filter((l) => filter === "all" || l.status === filter);
  const count = (key: LaneStatus | "all") => (key === "all" ? lanes.length : lanes.filter((l) => l.status === key).length);
  const s = board?.settings;
  return (
    <>
      <Card
        title="Lane leaderboard"
        description="Where the companies are, and what each way of finding them is worth: every lane of the profile with what it found, how much was new, what passed, what it cost and what it taught. Updates every 30 seconds."
        action={
          <span className="flex flex-wrap items-center gap-2">
            {profiles && profiles.length > 1 && (
              <select
                aria-label="Profile"
                className={field}
                value={profile}
                onChange={(e) => {
                  setProfile(e.target.value);
                  setBoard(undefined);
                  setFilter("all");
                  observe("finder_board_profile");
                }}
              >
                {profiles.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}
                  </option>
                ))}
              </select>
            )}
            <Button
              variant="ghost"
              disabled={busy}
              onClick={() => {
                setTick((t) => t + 1);
                observe("finder_board_refreshed");
              }}
            >
              <RefreshCw className="size-3.5" />
              Refresh
            </Button>
            {board && (
              <Button
                onClick={() => {
                  setAdding(true);
                  observe("finder_board_lane_add_opened");
                }}
              >
                <Plus className="size-3.5" />
                Add a lane
              </Button>
            )}
          </span>
        }
      >
        {error && <ErrorText>{error}</ErrorText>}
        {!board && busy && <Loading />}
        {board && lanes.length === 0 && (
          <p className="text-sm text-muted-foreground">
            No lanes yet. Agents open lanes themselves: an agent reads the board and the shared record, proposes ways to find companies, claims one, probes a
            sample and lets the score decide whether to scale it. You can add one too.
          </p>
        )}
        {board && lanes.length > 0 && <Yield board={board} />}
      </Card>
      {board && lanes.length > 0 && (
        <Card
          title="Lanes"
          description={
            s
              ? `Probe size ${number(s.probe_size)}. A probe scales at ${Math.round(s.min_new_rate * 100)}% new and ${Math.round(s.min_pass_rate * 100)}% passed; otherwise it is dead, with the reason kept. ${s.review_mode === "automatic" ? "Agents' lanes start without approval." : "Agents' lanes wait for a person's approval."}`
              : undefined
          }
        >
          <div className="sr-periods" role="group" aria-label="Leaderboard lane state" style={{ flexWrap: "wrap" }}>
            {FILTERS.map(([key, name]) => (
              <button
                key={key}
                aria-pressed={filter === key}
                onClick={() => {
                  setFilter(key);
                  observe("finder_board_filter", key);
                }}
              >
                {name} · {number(count(key))}
              </button>
            ))}
          </div>
          {shown.length === 0 ? (
            <p className="mt-4 text-sm text-muted-foreground">No lanes in this state.</p>
          ) : (
            <div className="sr-table-wrap mt-4">
              <table className="sr-table">
                <thead>
                  <tr>
                    <th>Lane</th>
                    <th>State</th>
                    <th>Found</th>
                    <th>New</th>
                    <th>Passed / screened</th>
                    <th>Could hold</th>
                    <th>Cost per pass</th>
                    <th>What it taught</th>
                  </tr>
                </thead>
                <tbody>
                  {shown.map((l) => (
                    <LaneLine
                      key={l.id}
                      l={l}
                      observe={observe}
                      onOpen={() => {
                        setOpen(l.id);
                        observe("finder_board_lane_opened", l.status);
                      }}
                    />
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Card>
      )}
      {board && lanes.length > 0 && (
        <div className="grid grid-cols-[minmax(0,1fr)] gap-4 lg:grid-cols-2">
          <Groups lanes={lanes} />
          <SharedRecord
            board={board}
            observe={observe}
            onLane={(id) => {
              setOpen(id);
              observe("finder_note_lane_opened");
            }}
          />
        </div>
      )}
    </>
  );
}

function Tile({ term, value, sub }: { term: string; value: string; sub: string }) {
  return (
    <div className="sr-summary">
      <span className="text-xs text-muted-foreground">{term}</span>
      <strong>{value}</strong>
      <span className="text-xs text-muted-foreground">{sub}</span>
    </div>
  );
}

// perPass is what a passed company cost (the agent's reported spend plus
// Outflare's own), or a dash when nothing passed or nothing was priced.
const perPass = (cents: number | null | undefined, passed: number) => (passed > 0 && cents ? usd(cents) : "—");
const passRate = (l: Pick<RegistryLane, "passed" | "screened">) => pct(l.passed, l.screened);
const newRate = (l: Pick<RegistryLane, "new_rate">) => (l.new_rate == null ? "—" : `${Math.round(l.new_rate * 100)}%`);

// Yield: the board's totals.
function Yield({ board }: { board: Board }) {
  const t = board.totals;
  const unpriced = board.lanes.reduce((n, l) => n + (l.cost?.agent_unpriced ?? 0), 0);
  return (
    <div className="sr-summary-grid">
      <Tile term="Found" value={number(t.found)} sub={`${number(t.found - t.repeat)} distinct companies · ${number(t.repeat)} repeats`} />
      <Tile term="New" value={number(t.new)} sub={`${pct(t.new, t.found)} of finds · ${number(t.known)} Outflare already had`} />
      <Tile term="Screened" value={number(t.screened)} sub={t.errors ? `${number(t.errors)} ${t.errors === 1 ? "site" : "sites"} could not be read` : "new companies the profile's checks decided"} />
      <Tile term="Passed" value={number(t.passed)} sub={`${pct(t.passed, t.screened)} of screened (${number(t.passed)} / ${number(t.screened)})`} />
      <Tile
        term="Cost per passed company"
        value={perPass(t.passed ? t.total_cents / t.passed : null, t.passed)}
        sub={`${usd(t.agent_cents)} agents reported · ${usd(t.outflare_cents)} Outflare${unpriced ? ` · ${number(unpriced)} costs not priced` : ""}`}
      />
      <Tile
        term="Search and page requests"
        value={number(t.agent_requests)}
        sub={t.passed ? `${(t.agent_requests / t.passed).toFixed(1)} per passed company, as agents reported them` : "as agents reported them"}
      />
      <Tile
        term="Lanes"
        value={number(t.lanes)}
        sub={`${number(t.scaling)} scaling · ${number(t.probing)} probing · ${number(t.proposed)} proposed · ${number(t.dead)} dead · ${number(t.exhausted)} exhausted`}
      />
    </div>
  );
}

function RecipeText({ l }: { l: Pick<RegistryLane, "hypothesis" | "recipe"> }) {
  const r = l.recipe ?? {};
  const lists = recipeLists.filter(([k]) => ((r[k] as string[] | undefined) ?? []).length > 0);
  return (
    <span className="mt-1 block text-xs text-muted-foreground" style={{ overflowWrap: "anywhere" }}>
      {l.hypothesis && <span className="block">{l.hypothesis}</span>}
      {lists.map(([k, label]) => (
        <span key={k} className="block">
          {label}: {(r[k] as string[]).join(" · ")}
        </span>
      ))}
      {r.refresh_days ? <span className="block">Comes back every {number(r.refresh_days)} days</span> : null}
      {!l.hypothesis && lists.length === 0 && <span className="block">No recipe written down.</span>}
    </span>
  );
}

const kindLabel = (l: Pick<RegistryLane, "kind" | "signal">) =>
  l.kind ? `${KIND[l.kind] ?? l.kind}${l.kind === "signal" && l.signal ? `: ${SIGNAL[l.signal] ?? l.signal}` : ""}` : "Named on upload";
function laneMeta(l: RegistryLane) {
  return [kindLabel(l), l.segment, ORIGIN[l.origin] ?? ""].filter(Boolean).join(" · ");
}

function LaneLine({ l, observe, onOpen }: { l: RegistryLane; observe: Observe; onOpen: () => void }) {
  const [recipe, setRecipe] = useState(false);
  const st = statusOf(l.status);
  return (
    <tr>
      <td style={{ maxWidth: "22rem" }}>
        <button type="button" className="sr-lead-link text-left" onClick={onOpen}>
          {l.title}
        </button>
        <small className="block text-muted-foreground">{laneMeta(l)}</small>
        <button
          type="button"
          className="text-xs text-muted-foreground underline"
          onClick={() => {
            setRecipe(!recipe);
            if (!recipe) observe("finder_board_lane_recipe_shown", l.kind ?? "none");
          }}
        >
          {recipe ? "Hide recipe" : "Show recipe"}
        </button>
        {recipe && <RecipeText l={l} />}
      </td>
      <td>
        <Badge tone={st.tone}>{st.label}</Badge>
        {leased(l) && <small className="block text-muted-foreground">{l.claimed_by} working</small>}
        {l.score_pending && <small className="block text-muted-foreground">verdict waits for {number(l.waiting_for)} being checked</small>}
      </td>
      <td>{number(l.found)}</td>
      <td>
        {number(l.new)} <small className="text-muted-foreground">({newRate(l)})</small>
        {l.known_later > 0 && <small className="block text-muted-foreground">{number(l.known_later)} known at intake</small>}
      </td>
      <td>
        {number(l.passed)} / {number(l.screened)} <small className="text-muted-foreground">({passRate(l)})</small>
        {l.screened > 0 && l.screened < SMALL_SAMPLE && <small className="block text-amber-700 dark:text-amber-400">Small sample</small>}
        {l.errors > 0 && <small className="block text-muted-foreground">{number(l.errors)} unreadable</small>}
      </td>
      <td>
        {l.est_size ? (
          <>
            ~{number(l.est_size)}
            <small className="block text-muted-foreground">agent's estimate</small>
          </>
        ) : (
          "—"
        )}
      </td>
      <td>
        {perPass(l.cost?.per_pass_cents, l.passed)}
        <small className="block text-muted-foreground">{usd(l.cost?.total_cents ?? 0)} spent</small>
      </td>
      <td className="text-xs" style={{ maxWidth: "26rem" }}>
        {l.why && <span className="block font-medium">{l.why}</span>}
        {l.learned ?? (l.why ? "" : <span className="text-muted-foreground">Not probed yet</span>)}
      </td>
    </tr>
  );
}

type Group = { key: string; by: "kind" | "signal"; lanes: number; new: number; screened: number; passed: number; cents: number };
// Groups: which kind of lane, and which signal, pays off ("By way of finding").
function Groups({ lanes }: { lanes: RegistryLane[] }) {
  const groups = useMemo(() => {
    const by = new Map<string, Group>();
    const add = (key: string, kind: "kind" | "signal", l: RegistryLane) => {
      const g = by.get(`${kind}:${key}`) ?? { key, by: kind, lanes: 0, new: 0, screened: 0, passed: 0, cents: 0 };
      g.lanes++;
      g.new += l.new;
      g.screened += l.screened;
      g.passed += l.passed;
      g.cents += l.cost?.total_cents ?? 0;
      by.set(`${kind}:${key}`, g);
    };
    for (const l of lanes) {
      add(l.kind ?? "", "kind", l);
      if (l.kind === "signal" && l.signal) add(l.signal, "signal", l);
    }
    return [...by.values()].sort((a, b) => b.passed - a.passed || b.new - a.new);
  }, [lanes]);
  return (
    <Card title="By way of finding" description="Which kind of lane and which signal pay off.">
      <div className="sr-table-wrap">
        <table className="sr-table">
          <thead>
            <tr>
              <th>Way of finding</th>
              <th>Lanes</th>
              <th>New</th>
              <th>Passed / screened</th>
              <th>Cost per pass</th>
            </tr>
          </thead>
          <tbody>
            {groups.map((g) => (
              <tr key={`${g.by}:${g.key}`}>
                <td>
                  {g.by === "kind" ? (g.key ? (KIND[g.key] ?? g.key) : "Named on upload") : (SIGNAL[g.key] ?? g.key)}
                  <small className="block text-muted-foreground">{g.by === "kind" ? "kind of lane" : "signal searched for"}</small>
                </td>
                <td>{number(g.lanes)}</td>
                <td>{number(g.new)}</td>
                <td>
                  {number(g.passed)} / {number(g.screened)} <small className="text-muted-foreground">({pct(g.passed, g.screened)})</small>
                  {g.screened < SMALL_SAMPLE && <small className="block text-amber-700 dark:text-amber-400">Small sample</small>}
                </td>
                <td>{perPass(g.passed ? g.cents / g.passed : null, g.passed)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

function NoteList({ notes, onLane }: { notes: Note[]; onLane?: (id: string) => void }) {
  if (notes.length === 0) return <p className="text-sm text-muted-foreground">Nothing written down yet.</p>;
  return (
    <ul className="grid max-h-[28rem] gap-2 overflow-y-auto text-sm">
      {notes.map((n) => (
        <li key={n.id} className="border-l-2 border-border pl-3">
          <span className="flex flex-wrap items-center gap-2">
            <Badge tone={n.kind === "dead_end" ? "red" : n.kind === "finding" ? "green" : "grey"}>{NOTE_KIND[n.kind] ?? n.kind}</Badge>
            {onLane && n.lane_id ? (
              <button type="button" className="sr-lead-link text-left" onClick={() => onLane(n.lane_id!)}>
                {n.lane ?? "A lane"}
              </button>
            ) : (
              n.lane && <span className="font-medium">{n.lane}</span>
            )}
            <small className="text-muted-foreground">
              {n.author} · {stamp(n.created_at)}
            </small>
          </span>
          <span className="block" style={{ overflowWrap: "anywhere" }}>
            {n.text}
          </span>
        </li>
      ))}
    </ul>
  );
}

// SharedRecord: the notes every agent reads, and the exact text it is given.
function SharedRecord({ board, observe, onLane }: { board: Board; observe: Observe; onLane: (id: string) => void }) {
  return (
    <Card title="Shared record" description="What every agent reads before it starts: each verdict, lesson and note, newest first.">
      <NoteList notes={board.notes} onLane={onLane} />
      <details
        className="mt-3 text-sm"
        onToggle={(e) => {
          if ((e.target as HTMLDetailsElement).open) observe("finder_board_record_text_shown");
        }}
      >
        <summary className="cursor-pointer text-muted-foreground">The text an agent reads</summary>
        <pre className="mt-2 whitespace-pre-wrap text-xs" style={{ overflowWrap: "anywhere" }}>
          {[board.notes_text, board.covered_text].filter(Boolean).join("\n\n")}
        </pre>
      </details>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// A lane's own page: its recipe, why it has its status, what it taught, its
// counters and notes, its companies with grade and stop reason, and the
// person's controls.

type Act = "approve" | "reject" | "pause" | "resume" | "priority" | "revisit" | "recipe";

function LanePage({
  base,
  brand,
  lane: l,
  board,
  observe,
  go,
  onChanged,
  onOpenLane,
  onBack,
}: {
  base: string;
  brand: string;
  lane: RegistryLane;
  board: Board;
  observe: Observe;
  go: (tab: Tab, item?: string) => void;
  onChanged: () => void;
  onOpenLane: (id: string) => void;
  onBack: () => void;
}) {
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [revisit, setRevisit] = useState(l.next_revisit_at?.slice(0, 10) ?? "");
  const [editing, setEditing] = useState(false);
  const st = statusOf(l.status);
  const parent = l.parent_id ? board.lanes.find((x) => x.id === l.parent_id) : undefined;
  const notes = board.notes.filter((n) => n.lane_id === l.id);

  async function act(action: Act, extra: Record<string, unknown> = {}) {
    let reason = "";
    if (action === "reject" || action === "pause") {
      const r = await ask({
        title: action === "reject" ? "Reject this lane?" : "Pause this lane?",
        body:
          action === "reject"
            ? "No agent will work it. The reason is kept on the lane and in the audit log."
            : "No agent will claim it until it is resumed. The reason is kept in the audit log.",
        input: { label: "Why", maxLength: 1000 },
        choices: [
          { label: "Cancel", value: "cancel", variant: "ghost" },
          { label: action === "reject" ? "Reject" : "Pause", value: "ok", variant: "danger-solid" },
        ],
      });
      if (r?.choice !== "ok") return;
      reason = r.value.trim();
    }
    // approve, reject, pause and resume are actions; the rest change a field.
    const body = ["approve", "reject", "pause", "resume"].includes(action) ? { action, ...(reason ? { reason } : {}) } : extra;
    setBusy(true);
    const r = await send<RegistryLane>("PATCH", `${base}/${l.id}`, body);
    setBusy(false);
    if (r.error) {
      setError(errorMessage(r.error));
      observe("finder_board_lane_control_failed", action);
      return;
    }
    setError("");
    observe("finder_board_lane_control", action);
    toast(
      {
        approve: "Approved: the next agent can probe it.",
        reject: "Rejected.",
        pause: "Paused.",
        resume: "Resumed: it probes again.",
        priority: "Priority changed.",
        revisit: "Revisit date saved.",
        recipe: "Recipe saved.",
      }[action],
    );
    if (action === "recipe") setEditing(false);
    onChanged();
  }

  return (
    <div className="grid grid-cols-[minmax(0,1fr)] gap-4">
      <div>
        <Button variant="ghost" onClick={onBack}>
          <ArrowLeft className="size-3.5" />
          All lanes
        </Button>
      </div>
      <Card
        title={l.title}
        description={[laneMeta(l), `Added by ${l.created_by || "an agent"} ${day(l.created_at)}`, l.last_activity_at ? `last worked ${stamp(l.last_activity_at)}` : ""]
          .filter(Boolean)
          .join(" · ")}
        action={<Badge tone={st.tone}>{st.label}</Badge>}
      >
        <div className="grid grid-cols-[minmax(0,1fr)] gap-4">
          {error && <ErrorText>{error}</ErrorText>}
          <div className="flex flex-wrap items-center gap-2" role="group" aria-label="Lane controls">
            {l.status === "proposed" && (
              <>
                <Button disabled={busy} onClick={() => act("approve")}>
                  Approve
                </Button>
                <Button variant="ghost" disabled={busy} onClick={() => act("reject")}>
                  Reject
                </Button>
              </>
            )}
            {l.status === "paused" ? (
              <Button disabled={busy} onClick={() => act("resume")}>
                Resume
              </Button>
            ) : (
              l.status !== "rejected" && (
                <Button variant="ghost" disabled={busy} onClick={() => act("pause")}>
                  Pause
                </Button>
              )
            )}
            <span className="flex items-center gap-1 text-sm">
              <span className="text-muted-foreground">Priority {number(l.priority)}</span>
              <Button variant="ghost" aria-label="Raise priority" disabled={busy} onClick={() => act("priority", { priority: l.priority + 1 })}>
                +
              </Button>
              <Button variant="ghost" aria-label="Lower priority" disabled={busy} onClick={() => act("priority", { priority: l.priority - 1 })}>
                −
              </Button>
            </span>
            <span className="flex flex-wrap items-center gap-1 text-sm">
              <label className="text-muted-foreground" htmlFor={`revisit-${l.id}`}>
                Revisit on
              </label>
              <input id={`revisit-${l.id}`} type="date" className={field} value={revisit} onChange={(e) => setRevisit(e.target.value)} />
              <Button variant="ghost" disabled={busy || revisit === (l.next_revisit_at?.slice(0, 10) ?? "")} onClick={() => act("revisit", { next_revisit_at: revisit ? `${revisit}T00:00:00Z` : null })}>
                Save date
              </Button>
            </span>
          </div>
          <p className="text-xs text-muted-foreground">
            Priority only orders which lane is claimed first; it never waives the profile's checks. A status the rules set (dead, exhausted) changes only with new
            evidence.
          </p>
          <div className="sr-summary-grid">
            <Tile term="Found" value={number(l.found)} sub={`${number(l.known)} known · ${number(l.repeat)} repeats`} />
            <Tile term="New" value={number(l.new)} sub={`${newRate(l)} of finds${l.known_later ? ` · ${number(l.known_later)} known at intake` : ""}`} />
            <Tile
              term="Passed / screened"
              value={`${number(l.passed)} / ${number(l.screened)}`}
              sub={`${passRate(l)}${l.screened > 0 && l.screened < SMALL_SAMPLE ? " · small sample" : ""}${l.checking ? ` · ${number(l.checking)} being checked` : ""}`}
            />
            <Tile term="Could hold" value={l.est_size ? `~${number(l.est_size)}` : "—"} sub="agent's estimate" />
            <Tile
              term="Cost per pass"
              value={perPass(l.cost?.per_pass_cents, l.passed)}
              sub={`${usd(l.cost?.agent_cents ?? 0)} agent · ${usd(l.cost?.outflare_cents ?? 0)} Outflare · ${number(l.cost?.agent_requests ?? 0)} requests${l.cost?.agent_unpriced ? ` · ${number(l.cost.agent_unpriced)} not priced` : ""}`}
            />
          </div>
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
            <Counts title="Already known, because" counts={l.known_by} />
            <Counts title="Repeats" counts={l.repeat_by} />
            <Counts title="Stopped at intake" counts={l.stopped_at_intake} />
            <Counts title="Grades" counts={l.grades} order={["A", "B", "C", "D"]} />
          </div>
          <div className="grid gap-4 sm:grid-cols-2">
            <Section title="Why it has this status">
              {l.why || (l.status === "proposed" ? "Waiting for approval or a first probe." : "—")}
              {l.score_pending && (
                <span className="block text-muted-foreground">The last batch is scored once its {number(l.waiting_for)} companies still being checked are done.</span>
              )}
              {leased(l) && <span className="block text-muted-foreground">{l.claimed_by} is working it until {stamp(l.lease_expires_at)}.</span>}
              {l.next_revisit_at && <span className="block text-muted-foreground">Rests until {day(l.next_revisit_at)}.</span>}
            </Section>
            <Section title="What it taught">{l.learned || <span className="text-muted-foreground">Not probed yet.</span>}</Section>
          </div>
          {parent && (
            <p className="text-sm">
              Learned from{" "}
              <button type="button" className="sr-lead-link" onClick={() => onOpenLane(parent.id)}>
                {parent.title}
              </button>
            </p>
          )}
        </div>
      </Card>
      <Card
        title="Recipe"
        description="How an agent finds this lane's companies."
        action={
          !editing && (
            <Button
              variant="ghost"
              onClick={() => {
                setEditing(true);
                observe("finder_board_lane_recipe_edit");
              }}
            >
              Edit recipe
            </Button>
          )
        }
      >
        {editing ? <RecipeForm initial={l.recipe ?? {}} busy={busy} onCancel={() => setEditing(false)} onSave={(recipe) => act("recipe", { recipe })} /> : <RecipeText l={l} />}
      </Card>
      <Card title="Notes on this lane" description="What agents wrote down about it, in the shared record.">
        <NoteList notes={notes} />
      </Card>
      <LaneCompanies brand={brand} lane={l} observe={observe} go={go} />
    </div>
  );
}

// Words for the reasons uploads and intake give.
const reasonWords: Record<string, string> = {
  in_people_list: "Already in People",
  seen_by_lead_finder: "Checked by the finder before",
  customer: "Already a customer",
  blocklisted: "On the blocklist",
  opted_out: "Opted out",
  free_mail: "Free mail domain",
  same_batch: "Twice in one upload",
  same_lane: "This lane sent it before",
  other_lane: "Another lane found it first",
};

function Counts({ title, counts, order }: { title: string; counts?: Record<string, number>; order?: string[] }) {
  const rows = Object.entries(counts ?? {})
    .filter(([, n]) => n > 0)
    .sort((a, b) => (order ? order.indexOf(a[0]) - order.indexOf(b[0]) : b[1] - a[1]));
  return (
    <div className="text-sm">
      <strong className="block">{title}</strong>
      {rows.length === 0 ? (
        <span className="text-muted-foreground">None</span>
      ) : (
        <ul className="mt-1 grid gap-0.5 text-muted-foreground">
          {rows.map(([k, n]) => (
            <li key={k}>
              {reasonWords[k] ?? k} · {number(n)}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="text-sm" style={{ overflowWrap: "anywhere" }}>
      <strong className="block">{title}</strong>
      <span className="mt-1 block">{children}</span>
    </div>
  );
}

const lines = (s: string) =>
  s
    .split("\n")
    .map((x) => x.trim())
    .filter(Boolean);

// useRecipe holds a recipe being typed: one line per search, list, seed...
function useRecipe(initial: Recipe) {
  const [text, setText] = useState<Record<string, string>>(() => Object.fromEntries(recipeLists.map(([k]) => [k, ((initial?.[k] as string[] | undefined) ?? []).join("\n")])));
  const [refresh, setRefresh] = useState(initial?.refresh_days ? String(initial.refresh_days) : "");
  const recipe = (): Recipe => {
    const r: Recipe = {};
    for (const [k] of recipeLists) {
      const v = lines(text[k] ?? "");
      if (v.length) (r[k] as string[]) = v;
    }
    const n = Number(refresh);
    if (refresh && Number.isInteger(n) && n > 0) r.refresh_days = n;
    return r;
  };
  const fields = (
    <>
      {recipeLists.map(([k, label]) => (
        <label key={k} className="grid gap-1 text-sm">
          <span>{label}, one a line</span>
          <textarea className={field} rows={3} value={text[k]} onChange={(e) => setText({ ...text, [k]: e.target.value })} />
        </label>
      ))}
      <label className="grid gap-1 text-sm">
        <span>Comes back every (days, for a source that refreshes)</span>
        <input className={field} inputMode="numeric" value={refresh} onChange={(e) => setRefresh(e.target.value.replace(/\D/g, ""))} />
      </label>
    </>
  );
  return { fields, recipe };
}

function RecipeForm({ initial, busy, onSave, onCancel }: { initial: Recipe; busy: boolean; onSave: (r: Recipe) => void; onCancel: () => void }) {
  const { fields, recipe } = useRecipe(initial);
  return (
    <div className="grid gap-3">
      {fields}
      <div className="flex gap-2">
        <Button disabled={busy} onClick={() => onSave(recipe())}>
          Save recipe
        </Button>
        <Button variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </div>
  );
}

// A person adds a lane: it starts as proposed like any agent's, unless the person approves it on its page.
function AddLane({ base, observe, onBack, onAdded }: { base: string; observe: Observe; onBack: () => void; onAdded: (id?: string) => void }) {
  const [title, setTitle] = useState("");
  const [hypothesis, setHypothesis] = useState("");
  const [kind, setKind] = useState("signal");
  const [segment, setSegment] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const { fields, recipe } = useRecipe({});
  async function save() {
    const r0 = recipe();
    if (title.trim().length < 8) {
      setError("Give the lane a plain-language title of at least 8 characters.");
      observe("finder_board_lane_add_failed", "title");
      return;
    }
    if (!recipeLists.some(([k]) => ((r0[k] as string[] | undefined) ?? []).length > 0)) {
      setError("Add at least one search, list, seed, description or job ad, so an agent knows how to find the companies.");
      observe("finder_board_lane_add_failed", "recipe");
      return;
    }
    setBusy(true);
    const lane = { title: title.trim(), hypothesis: hypothesis.trim(), kind, segment: segment.trim(), recipe: r0 };
    const r = await send<{ lanes?: { index: number; created: boolean; lane?: RegistryLane; skipped?: string }[] }>("POST", base, { lanes: [lane] });
    setBusy(false);
    if (r.error) {
      setError(errorMessage(r.error));
      observe("finder_board_lane_add_failed", r.error.error?.code);
      return;
    }
    const made = r.data?.lanes?.[0];
    if (made?.skipped) {
      // The server's cleaning refused it; its words say why.
      setError(`Not added: ${made.skipped}.`);
      observe("finder_board_lane_add_failed", "skipped");
      return;
    }
    observe("finder_board_lane_added", made?.created === false ? "existing" : "created");
    if (made?.created === false) toast(`That lane is already on record (${statusOf(made.lane?.status ?? "").label}); opened it.`);
    else toast("Lane added.");
    onAdded(made?.lane?.id);
  }
  return (
    <div className="grid grid-cols-[minmax(0,1fr)] gap-4">
      <div>
        <Button variant="ghost" onClick={onBack}>
          <ArrowLeft className="size-3.5" />
          All lanes
        </Button>
      </div>
      <Card title="Add a lane" description="One idea x one way of finding companies x one segment. It starts as proposed; approve it on its page to have the next agent probe it.">
        <div className="grid gap-3">
          {error && <ErrorText>{error}</ErrorText>}
          <label className="grid gap-1 text-sm">
            <span>Title</span>
            <input className={field} maxLength={200} value={title} onChange={(e) => setTitle(e.target.value)} placeholder="Payroll tools listed on review sites, EU" />
          </label>
          <label className="grid gap-1 text-sm">
            <span>Why these companies should fit</span>
            <textarea className={field} rows={3} maxLength={800} value={hypothesis} onChange={(e) => setHypothesis(e.target.value)} />
          </label>
          <div className="grid gap-3 sm:grid-cols-2">
            <label className="grid gap-1 text-sm">
              <span>Way of finding</span>
              <select className={field} value={kind} onChange={(e) => setKind(e.target.value)}>
                <option value="list">List: harvest a directory, list or job board</option>
                <option value="signal">Signal: search for a trace of the need</option>
                <option value="lookalike">Lookalike: peers of companies that fit</option>
              </select>
            </label>
            <label className="grid gap-1 text-sm">
              <span>Segment</span>
              <input className={field} maxLength={120} value={segment} onChange={(e) => setSegment(e.target.value)} placeholder="EU, 50 to 500 people" />
            </label>
          </div>
          {fields}
          <div>
            <Button disabled={busy} onClick={save}>
              Add lane
            </Button>
          </div>
        </div>
      </Card>
    </div>
  );
}

// A lane's companies, with the stage each reached, why it stopped and its grade.
function LaneCompanies({ brand, lane, observe, go }: { brand: string; lane: RegistryLane; observe: Observe; go: (tab: Tab, item?: string) => void }) {
  const [stage, setStage] = useState<LaneStage | "">("");
  const [cursors, setCursors] = useState<string[]>([]);
  const [page, setPage] = useState<Page>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(true);
  const cursor = cursors[cursors.length - 1] ?? "";
  useEffect(() => {
    const controller = new AbortController();
    setBusy(true);
    const q = new URLSearchParams({ lane_id: lane.id, limit: "50" });
    if (stage) q.set("stage", stage);
    if (cursor) q.set("cursor", cursor);
    send<Page>("GET", `/v1/brands/${brand}/cloud/lead-finder/lanes/companies?${q}`, undefined, controller.signal)
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
  }, [brand, lane.id, stage, cursor]);
  return (
    <Card
      title={stage ? `Companies: ${laneStages.find((s) => s.id === stage)!.label.toLowerCase()}` : "Companies this lane sent"}
      description={page ? `${number(page.total)} ${page.total === 1 ? "company" : "companies"}, newest first. Companies Outflare already had are counted, not listed.` : undefined}
      action={
        <select
          aria-label="Stage"
          className={field}
          value={stage}
          onChange={(e) => {
            setStage(e.target.value as LaneStage | "");
            setCursors([]);
            observe("finder_board_lane_stage", e.target.value || "all");
          }}
        >
          <option value="">Every stage</option>
          {laneStages.map((s) => (
            <option key={s.id} value={s.id}>
              {s.label}
            </option>
          ))}
        </select>
      }
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
                  <th>Grade</th>
                  <th>Why it stopped</th>
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
                        <button
                          type="button"
                          className="sr-lead-link text-left"
                          onClick={() => {
                            observe("finder_record_opened", "company");
                            go("people", c.record_id!);
                          }}
                        >
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
                    <td>
                      {c.grade ? <Badge tone={c.grade === "A" || c.grade === "B" ? "green" : c.grade === "C" ? "amber" : "red"}>{c.grade}</Badge> : "—"}
                      {!!c.reasons?.length && (
                        <small className="block text-muted-foreground" style={{ overflowWrap: "anywhere" }}>
                          {c.reasons.join(" · ")}
                        </small>
                      )}
                    </td>
                    <td>{c.code ? codeLabel(c.code) : "—"}</td>
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
                  observe("finder_board_lane_page", "previous");
                }}
              >
                Previous
              </Button>
              <Button
                variant="ghost"
                disabled={busy || !page.next_cursor}
                onClick={() => {
                  setCursors([...cursors, page.next_cursor!]);
                  observe("finder_board_lane_page", "next");
                }}
              >
                Next
              </Button>
            </div>
          )}
        </>
      )}
    </Card>
  );
}
