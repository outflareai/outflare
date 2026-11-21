import { useEffect, useState } from "react";
import { api, errorMessage } from "@/api";
import { Button, ErrorText, Loading, field } from "@/ui";

type Totals = {
  calls: number;
  errors: number;
  recorded_cents: number | null;
  unknown_cost_calls: number;
};
type Run = {
  id: string;
  mission_id: string;
  status: string;
  started_at: string;
  finished_at: string | null;
  last_event_at: string | null;
};
type Receipt = {
  id: string;
  run_id: string;
  lane_id: string | null;
  lane_title: string | null;
  company: string | null;
  grade: string | null;
  qualification: string | null;
  phase: string;
  agent: string;
  kind: string;
  provider: string | null;
  tool: string;
  status: string;
  started_at: string;
  duration_ms: number;
  attempt: number;
  cost_cents: number | null;
  input: Record<string, unknown>;
  output: Record<string, unknown>;
};
type Trace = {
  scope: string;
  total: number;
  offset: number;
  summary: Totals;
  runs: Run[];
  tools: (Totals & {
    provider: string;
    tool: string;
    empty: number;
    duration_ms: number;
    units: number;
    unit_kind: string;
  })[];
  lanes: (Totals & {
    lane_id: string | null;
    title: string | null;
    companies: number;
    screened: number;
    passed: number;
    graded_ab: number;
    sent: number;
  })[];
  rows: Receipt[];
};
const money = (cents: number | null) =>
  cents === null ? "Unknown" : `$${(cents / 100).toFixed(4)}`;
const time = (value: string) =>
  new Date(value).toLocaleString(undefined, {
    timeZone: "UTC",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  }) + " UTC";

export function DiscoveryTrace({
  brand,
  connection,
  lane,
  observe,
}: {
  brand: string;
  connection: string;
  lane?: string;
  observe: (action: string, value?: string) => void;
}) {
  const [run, setRun] = useState("");
  const [offset, setOffset] = useState(0);
  const [version, setVersion] = useState(0);
  const [data, setData] = useState<Trace | null>();
  const [runs, setRuns] = useState<Run[]>([]);
  const [busy, setBusy] = useState(true);
  const [error, setError] = useState("");
  const [open, setOpen] = useState("");
  useEffect(() => {
    const controller = new AbortController();
    setBusy(true);
    setError("");
    setData(undefined);
    api
      .GET("/v1/brands/{brand}/sales/reporting/lanes", {
        params: {
          path: { brand },
          query: {
            trace: true,
            offset,
            ...(run ? { run } : {}),
            ...(lane ? { lane } : {}),
            ...(connection ? { integration: connection } : {}),
          },
        },
        signal: controller.signal,
      })
      .then((response) => {
        if (controller.signal.aborted) return;
        if (response.error) setError(errorMessage(response.error));
        else {
          const trace = response.data?.trace as unknown as Trace | null;
          setData(trace);
          if (!run) setRuns(trace?.runs ?? []);
        }
        setBusy(false);
      })
      .catch((e) => {
        if (!controller.signal.aborted) {
          setError(errorMessage(e));
          setBusy(false);
        }
      });
    return () => controller.abort();
  }, [brand, connection, lane, run, offset, version]);
  return (
    <section aria-label="Discovery execution history" className="sr-execution grid gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h3 className="font-semibold">Execution history</h3>
          <p className="text-sm text-muted-foreground">
            Actual calls and decisions from the latest 20 recorded runs. Earlier
            activity has no call history.
          </p>
        </div>
        <Button
          variant="ghost"
          disabled={busy}
          onClick={() => {
            setVersion((v) => v + 1);
            observe("trace_refresh");
          }}
        >
          Refresh history
        </Button>
      </div>
      <label className="grid gap-1 text-sm">
        Run
        <select
          className={field}
          value={run}
          onChange={(e) => {
            setRun(e.target.value);
            setOffset(0);
            setOpen("");
            observe("trace_run", e.target.value ? "selected" : "all");
          }}
        >
          <option value="">Latest 20 recorded runs</option>
          {runs.map((r) => (
            <option key={r.id} value={r.id}>
              {time(r.started_at)} · {r.status} · {r.id.slice(0, 8)}
            </option>
          ))}
        </select>
      </label>
      {error && <ErrorText>{error}</ErrorText>}
      {busy && <Loading />}
      {!busy && !error && !data && (
        <p>Execution history is unavailable for this connection.</p>
      )}
      {data && (
        <>
          {!data.runs.length ? (
            <p>
              No traced runs yet. New history appears after the updated
              discovery runner starts.
            </p>
          ) : (
            <>
              <p className="text-sm">
                <strong>{data.summary.calls.toLocaleString()} calls</strong> ·{" "}
                {data.summary.errors.toLocaleString()} failed attempts ·{" "}
                {money(data.summary.recorded_cents)} recorded ·{" "}
                {data.summary.unknown_cost_calls.toLocaleString()} calls without
                a price
              </p>
              <p className="text-xs text-muted-foreground">
                Recorded costs are partial when prices are missing. These
                receipts explain spend already booked by discovery; they are not
                an additional charge. Refresh to see new activity.
              </p>
              <div className="flex flex-wrap gap-2 text-xs">
                {data.runs.map((r) => (
                  <span key={r.id}>
                    {r.id.slice(0, 8)} ·{" "}
                    {r.status === "running" &&
                    Date.now() - Date.parse(r.last_event_at ?? r.started_at) >
                      10 * 60_000
                      ? "unfinished; no recent receipts"
                      : r.status}{" "}
                    · last receipt {time(r.last_event_at ?? r.started_at)}
                  </span>
                ))}
              </div>
              <div className="sr-table-wrap">
                <table className="sr-table">
                  <caption className="text-left text-sm font-semibold">
                    Tool performance
                  </caption>
                  <thead>
                    <tr>
                      <th>Provider / tool</th>
                      <th>Calls</th>
                      <th>Errors</th>
                      <th>Empty</th>
                      <th>Time</th>
                      <th>Recorded cost</th>
                      <th>Unpriced</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.tools.map((t) => (
                      <tr key={`${t.provider}:${t.tool}:${t.unit_kind}`}>
                        <td>
                          {t.provider} / {t.tool}
                          <small className="block">
                            {t.units} {t.unit_kind}s
                          </small>
                        </td>
                        <td>{t.calls}</td>
                        <td>{t.errors}</td>
                        <td>{t.empty}</td>
                        <td>{(t.duration_ms / 1000).toFixed(1)}s</td>
                        <td>{money(t.recorded_cents)}</td>
                        <td>{t.unknown_cost_calls}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              <div className="sr-table-wrap">
                <table className="sr-table">
                  <caption className="text-left text-sm font-semibold">
                    Lane yield for these runs
                  </caption>
                  <thead>
                    <tr>
                      <th>Lane</th>
                      <th>Screened</th>
                      <th>Passed</th>
                      <th>A/B</th>
                      <th>To Research</th>
                      <th>Recorded cost</th>
                      <th>Cost / A/B</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.lanes.map((l) => (
                      <tr key={l.lane_id ?? "shared"}>
                        <td>{l.title ?? "Mission exploration"}</td>
                        <td>{l.screened}</td>
                        <td>{l.passed}</td>
                        <td>{l.graded_ab}</td>
                        <td>{l.sent}</td>
                        <td>
                          {money(l.recorded_cents)}
                          {l.unknown_cost_calls > 0 && " (partial)"}
                        </td>
                        <td>
                          {l.graded_ab && l.recorded_cents !== null
                            ? `${money(l.recorded_cents / l.graded_ab)}${l.unknown_cost_calls ? " (partial)" : ""}`
                            : "—"}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              <p className="text-xs text-muted-foreground">
                Quality shows the current outcomes of companies examined in
                these runs, including carried-over companies. Mission
                exploration is shared and is not assigned to a lane. A/B grades
                are screening evidence, not confirmed sales.
              </p>
              <div className="sr-table-wrap">
                <table className="sr-table">
                  <caption className="text-left text-sm font-semibold">
                    Calls and decisions
                  </caption>
                  <thead>
                    <tr>
                      <th>Time / lane</th>
                      <th>Work</th>
                      <th>Result</th>
                      <th>Duration</th>
                      <th>Cost</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.rows.map((r) => (
                      <tr key={r.id}>
                        <td>
                          {time(r.started_at)}
                          <small className="block">
                            {r.lane_title ?? "Mission"}
                          </small>
                        </td>
                        <td>
                          <button
                            className="sr-lead-link text-left"
                            aria-expanded={open === r.id}
                            onClick={() => {
                              setOpen(open === r.id ? "" : r.id);
                              observe("trace_receipt", r.kind);
                            }}
                          >
                            {r.phase} · {r.provider ?? r.agent} / {r.tool}
                          </button>
                          <small className="block">
                            {r.company ?? ""}
                            {r.attempt > 1 ? ` · attempt ${r.attempt}` : ""}
                          </small>
                          {open === r.id && (
                            <div className="grid gap-2 py-2">
                              <strong>Request</strong>
                              <pre className="whitespace-pre-wrap break-all text-xs">
                                {JSON.stringify(r.input, null, 2)}
                              </pre>
                              <strong>Recorded result</strong>
                              <pre className="whitespace-pre-wrap break-all text-xs">
                                {JSON.stringify(r.output, null, 2)}
                              </pre>
                              {r.company && (
                                <p>
                                  Current company outcome:{" "}
                                  {r.qualification ?? "pending"} · grade{" "}
                                  {r.grade ?? "pending"}
                                </p>
                              )}
                            </div>
                          )}
                        </td>
                        <td>{r.status}</td>
                        <td>{(r.duration_ms / 1000).toFixed(1)}s</td>
                        <td>{r.kind === "tool" ? money(r.cost_cents) : "—"}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              <div className="flex flex-wrap items-center justify-between gap-2">
                <span className="text-xs">
                  {data.total ? offset + 1 : 0}–
                  {Math.min(offset + 50, data.total)} of {data.total} receipts
                </span>
                <div className="flex gap-2">
                  <Button
                    variant="ghost"
                    disabled={busy || !offset}
                    onClick={() => {
                      setOffset(Math.max(0, offset - 50));
                      observe("trace_page", "previous");
                    }}
                  >
                    Previous receipts
                  </Button>
                  <Button
                    variant="ghost"
                    disabled={busy || offset + 50 >= data.total}
                    onClick={() => {
                      setOffset(offset + 50);
                      observe("trace_page", "next");
                    }}
                  >
                    Next receipts
                  </Button>
                </div>
              </div>
            </>
          )}
        </>
      )}
    </section>
  );
}
