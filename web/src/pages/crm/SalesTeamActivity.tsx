import { useEffect, useMemo, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { api, errorMessage } from "@/api";
import { Button, Empty, ErrorText, Loading, field } from "@/ui";
import { track } from "@/lib/analytics";

type Report = Schemas["SalesTeamReporting"];
type Period = "today" | "yesterday" | "7d" | "30d";
const actions: Record<string, string> = {
  lead_seen: "Seen",
  lead_assigned: "Assigned",
  assignment_released: "Assignment released",
  lead_taken: "Conversation started",
  lead_archived: "Declined / archived",
  lead_reopened: "Reopened",
  handoff_queued: "AE transfer queued",
  handoff_completed: "Transferred to AE",
  reply_queued: "Reply queued",
  send_accepted: "Send accepted",
  send_failed: "Send failed",
  send_uncertain: "Send needs checking",
  send_retry_queued: "Send retry queued",
  customer_confirmed: "Customer confirmed",
  feedback_added: "Feedback added",
  quality_set: "Quality marked",
  details_updated: "Details updated",
  draft_saved: "Draft saved",
  template_applied: "Template applied",
  draft_assist_requested: "AI draft requested",
  draft_assist_applied: "AI draft applied",
  draft_assist_ready: "AI draft ready",
  draft_assist_failed: "AI draft failed",
  draft_assist_expired: "AI draft expired",
  message_received: "Message received",
  lead_arrived: "Lead arrived",
};
const roleName = (role: string) =>
  ({
    sdr: "SDR",
    ae: "Account executive",
    manager: "Sales manager",
    worker: "Background task",
    bridge: "Import",
    sync: "Email sync",
    none: "Workspace member",
    unrecorded: "Role not recorded",
    "": "Role not recorded",
  })[role] ?? role;
function range(period: Period) {
  const now = new Date();
  const start = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  const end = new Date(now);
  if (period === "yesterday") {
    end.setTime(start.getTime());
    start.setDate(start.getDate() - 1);
  } else if (period !== "today")
    start.setDate(start.getDate() - (period === "7d" ? 6 : 29));
  return { from: start.toISOString(), to: end.toISOString() };
}
const columns = [
  ["lead_seen", "Seen"],
  ["lead_assigned", "Assigned"],
  ["lead_taken", "Started"],
  ["lead_archived", "Declined / archived"],
  ["handoff_completed", "Transferred to AE"],
  ["send_accepted", "Send accepted"],
  ["customer_confirmed", "Customers confirmed"],
] as const;

export function SalesTeamActivity({
  brand,
  openLead,
}: {
  brand: string;
  openLead: (id: string) => void;
}) {
  const [period, setPeriod] = useState<Period>("today");
  const [revision, setRevision] = useState(0);
  const interval = useMemo(() => range(period), [period, revision]);
  const [person, setPerson] = useState("");
  const [action, setAction] = useState("");
  const [searchText, setSearchText] = useState("");
  const [search, setSearch] = useState("");
  const [cursor, setCursor] = useState<string>();
  const [report, setReport] = useState<Report>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const observe = (action: string) =>
    track("ui.sales_reporting_interacted", { action, preview: false });
  useEffect(() => {
    const timer = setTimeout(() => {
      setSearch(searchText.trim());
      setCursor(undefined);
    }, 250);
    return () => clearTimeout(timer);
  }, [searchText]);
  useEffect(() => {
    const controller = new AbortController();
    setBusy(true);
    setError("");
    api
      .GET("/v1/brands/{brand}/sales/reporting/team", {
        params: {
          path: { brand },
          query: {
            ...interval,
            ...(person ? { user_id: person } : {}),
            ...(action ? { action } : {}),
            ...(search ? { search } : {}),
            ...(cursor ? { cursor } : {}),
          },
        },
        signal: controller.signal,
      })
      .then((r) => {
        if (controller.signal.aborted) return;
        if (r.error) {
          setReport(undefined);
          setError(errorMessage(r.error));
        } else setReport(r.data);
      })
      .catch((e) => {
        if (!controller.signal.aborted) {
          setReport(undefined);
          setError(errorMessage(e));
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setBusy(false);
      });
    return () => controller.abort();
  }, [brand, interval, person, action, search, cursor]);
  return (
    <section aria-label="Team activity" className="space-y-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 className="text-xl font-semibold">Team activity</h2>
          <p className="mt-1 text-sm text-muted-foreground">
            What each SDR and account executive did.
          </p>
        </div>
        <Button
          variant="ghost"
          disabled={busy}
          onClick={() => {
            setCursor(undefined);
            setRevision((v) => v + 1);
            observe("team_refresh");
          }}
        >
          Refresh
        </Button>
      </div>
      <div className="flex flex-wrap items-end gap-3">
        <label className="text-sm">
          Period
          <select
            aria-label="Team activity period"
            className={`${field} mt-1 block`}
            value={period}
            onChange={(e) => {
              setPeriod(e.target.value as Period);
              setCursor(undefined);
              setReport(undefined);
              observe("team_period");
            }}
          >
            <option value="today">Today</option>
            <option value="yesterday">Yesterday</option>
            <option value="7d">Last 7 days</option>
            <option value="30d">Last 30 days</option>
          </select>
        </label>
        <p className="pb-2 text-xs text-muted-foreground">
          {Intl.DateTimeFormat().resolvedOptions().timeZone} · through{" "}
          {new Date(interval.to).toLocaleString()}
        </p>
      </div>
      <ErrorText>{error}</ErrorText>
      {busy && <Loading />}
      {report && (
        <div
          aria-busy={busy}
          className={
            busy ? "pointer-events-none opacity-50 space-y-5" : "space-y-5"
          }
        >
          {(["sdr", "ae", "other"] as const).map((role) => {
            const people = report.members.filter((m) =>
              role === "other"
                ? !["sdr", "ae"].includes(m.role)
                : m.role === role,
            );
            if (role === "other" && !people.length) return null;
            const visibleColumns = columns
              .filter(([key]) => role !== "sdr" || key !== "customer_confirmed")
              .filter(([key]) => role !== "ae" || key !== "handoff_completed");
            return (
              <section
                key={role}
                aria-label={
                  role === "sdr"
                    ? "SDR activity"
                    : role === "ae"
                      ? "Account executive activity"
                      : "Other team activity"
                }
                className="space-y-2"
              >
                <h3 className="text-sm font-medium">
                  {role === "sdr"
                    ? "SDRs"
                    : role === "ae"
                      ? "Account executives"
                      : "Managers and other members"}{" "}
                  <span className="text-muted-foreground">
                    · {people.filter((m) => m.active).length} active
                  </span>
                </h3>
                {!people.length ? (
                  <p className="text-sm text-muted-foreground">
                    No {role === "sdr" ? "SDRs" : "account executives"} in this
                    team yet.
                  </p>
                ) : (
                  <div className="overflow-x-auto rounded-lg border">
                    <table className="w-full text-sm">
                      <thead className="bg-muted/30 text-xs text-muted-foreground">
                        <tr>
                          <th className="p-3 text-left">Teammate</th>
                          {visibleColumns.map(([key, label]) => (
                            <th
                              key={key}
                              className="p-3 text-right font-normal"
                            >
                              {label}
                            </th>
                          ))}
                        </tr>
                      </thead>
                      <tbody>
                        {people.map((m) => (
                          <tr key={m.user_id} className="border-t">
                            <td className="p-3">
                              <button
                                className="text-left font-medium hover:underline"
                                onClick={() => {
                                  setPerson(m.user_id);
                                  setCursor(undefined);
                                  observe("team_person");
                                  document
                                    .getElementById("sales-activity-log")
                                    ?.scrollIntoView({ behavior: "smooth" });
                                }}
                              >
                                {m.name || "Former member"}
                              </button>
                              {!m.active && (
                                <span className="ml-2 text-xs text-muted-foreground">
                                  Not active in Sales
                                </span>
                              )}
                            </td>
                            {visibleColumns.map(([key]) => (
                              <td
                                key={key}
                                className="p-3 text-right tabular-nums"
                              >
                                {m.counts[key] ?? 0}
                              </td>
                            ))}
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                )}
              </section>
            );
          })}
          <p className="text-xs text-muted-foreground">
            Unique conversations per person. Click a teammate to inspect their
            actions.
          </p>
          <details className="text-xs text-muted-foreground">
            <summary className="cursor-pointer">
              How counts and history work
            </summary>
            <p className="mt-2">
              Assigned counts assignments made by that teammate; Started means
              they took the conversation. Declined / archived is not a lost
              deal. Transfers count only after a successful handoff; send
              acceptance does not confirm delivery.
            </p>
            <p className="mt-2">
              Older recorded actions, including Seen audit history, are
              included. Completed-transfer history starts with this update;
              earlier successful transfers cannot be reconstructed. Team
              sections use current roles, or the last recorded role for former
              members.
            </p>
          </details>
          <section
            id="sales-activity-log"
            aria-label="Activity log"
            className="space-y-3 border-t pt-5"
          >
            <h3 className="font-medium">Activity log</h3>
            <div className="flex flex-wrap gap-3">
              <label className="min-w-40 text-sm">
                Teammate
                <select
                  aria-label="Activity teammate"
                  className={`${field} mt-1 block w-full`}
                  value={person}
                  onChange={(e) => {
                    setPerson(e.target.value);
                    setCursor(undefined);
                    observe("team_person");
                  }}
                >
                  <option value="">All teammates</option>
                  {report.members.map((m) => (
                    <option key={m.user_id} value={m.user_id}>
                      {m.name || "Former member"}
                    </option>
                  ))}
                </select>
              </label>
              <label className="min-w-40 text-sm">
                Action
                <select
                  aria-label="Activity action"
                  className={`${field} mt-1 block w-full`}
                  value={action}
                  onChange={(e) => {
                    setAction(e.target.value);
                    setCursor(undefined);
                    observe("team_action");
                  }}
                >
                  <option value="">All actions</option>
                  {Object.entries(actions).map(([key, label]) => (
                    <option key={key} value={key}>
                      {label}
                    </option>
                  ))}
                </select>
              </label>
              <label className="min-w-44 flex-1 text-sm">
                Person or company
                <input
                  aria-label="Activity person or company"
                  className={`${field} mt-1 block w-full`}
                  type="search"
                  maxLength={200}
                  placeholder="Search accounts…"
                  value={searchText}
                  onChange={(e) => setSearchText(e.target.value)}
                />
              </label>
            </div>
            <p className="text-xs text-muted-foreground">
              Filters apply to the log. Team totals above always cover the
              selected period.
            </p>
            {report.data.length === 0 ? (
              <Empty title="No matching activity">
                Try a different period, teammate or action.
              </Empty>
            ) : (
              <ol className="divide-y rounded-lg border">
                {report.data.map((e) => (
                  <li
                    key={e.id}
                    className="flex flex-wrap items-start justify-between gap-2 p-3 text-sm"
                  >
                    <div className="min-w-0">
                      <p>
                        <span className="font-medium">
                          {e.actor_name ||
                            ([
                              "sdr",
                              "ae",
                              "manager",
                              "unrecorded",
                              "none",
                            ].includes(e.actor_role)
                              ? "Former teammate"
                              : "System")}
                        </span>
                        <span className="text-xs text-muted-foreground">
                          {" "}
                          · {roleName(e.actor_role)}
                        </span>{" "}
                        · {actions[e.action] ?? e.action.replaceAll("_", " ")}
                      </p>
                      <button
                        className="mt-1 break-words text-left text-muted-foreground underline-offset-4 hover:underline"
                        onClick={() => openLead(e.lead_id)}
                      >
                        {e.company ? `${e.company} · ` : ""}
                        {e.name || "Conversation"}
                      </button>
                    </div>
                    <time
                      className="shrink-0 text-xs text-muted-foreground"
                      dateTime={e.occurred_at}
                      title={new Date(e.occurred_at).toLocaleString(undefined, {
                        dateStyle: "full",
                        timeStyle: "long",
                      })}
                    >
                      {new Date(e.occurred_at).toLocaleString()}
                    </time>
                  </li>
                ))}
              </ol>
            )}
            <div className="flex gap-2">
              {cursor && (
                <Button
                  variant="ghost"
                  disabled={busy}
                  onClick={() => {
                    setCursor(undefined);
                    observe("team_first_page");
                  }}
                >
                  Newest actions
                </Button>
              )}
              {report.next_cursor && (
                <Button
                  variant="ghost"
                  disabled={busy}
                  onClick={() => {
                    setCursor(report.next_cursor!);
                    observe("team_next_page");
                  }}
                >
                  Older actions
                </Button>
              )}
            </div>
          </section>
        </div>
      )}
    </section>
  );
}
