import { useCallback, useEffect, useState, type FormEvent } from "react";
import type { PlaybookAutomation, PlaybookAutomationInput, WorkspacePlaybook } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, confirmAction, ErrorText, field, toast, type tones } from "../ui";
import { ago, plural, when } from "../format";
import type { Tab } from "../App";

// A playbook that runs by itself, on a schedule or when an event happens.
// A person approves it once, with the mode and spend limits; each run is
// then a mission started inside them. Anything a person must act on (a
// limit, a missing connection, its approver gone) holds it with the reason.

const status: Record<PlaybookAutomation["status"], { label: string; tone: keyof typeof tones }> = {
  draft: { label: "Waits for approval", tone: "amber" },
  active: { label: "On", tone: "green" },
  paused: { label: "Paused", tone: "grey" },
  held: { label: "Held", tone: "red" },
};
const weekdays = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];
const modes: Record<PlaybookAutomation["mode"], string> = {
  dry_run: "Dry run: nothing changes",
  approve_writes: "Each change waits for approval",
  autonomous: "Changes run within the limits",
};
const gathers: Record<PlaybookAutomation["gather"], string> = {
  daily: "Gather them into one run a day",
  hourly: "Gather them into one run an hour",
  each: "A run per event",
};

const hour = (h: number) => `${String(h).padStart(2, "0")}:00 UTC`;
const dollars = (c: number) => `$${(c / 100).toLocaleString(undefined, { maximumFractionDigits: 2 })}`;

/** When it runs, in words: "Monthly on the 1st at 09:00 UTC", "When backlink.lost happens, gathered daily". */
export function automationWhen(a: PlaybookAutomation): string {
  if (a.kind === "event") return `When ${a.event_type} happens, ${a.gather === "each" ? `a run per event, at most ${a.daily_cap} a day` : `gathered ${a.gather}`}`;
  if (a.cadence === "weekly") return `Weekly on ${weekdays[a.run_weekday]} at ${hour(a.run_hour)}`;
  if (a.cadence === "monthly") return `Monthly on day ${a.run_monthday} at ${hour(a.run_hour)}`;
  return `Daily at ${hour(a.run_hour)}`;
}

type Props = { ws: string; playbook: WorkspacePlaybook; inputs: Record<string, string>; canManage: boolean; go: (tab: Tab, item?: string) => void };

export function PlaybookAutomations({ ws, playbook: p, inputs, canManage, go }: Props) {
  const [list, setList] = useState<PlaybookAutomation[] | null>(null);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);

  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/playbook-automations", { params: { path: { ws }, query: { playbook_id: p.id } } });
    if (error || !data) return setError(errorMessage(error));
    setList(data.data);
  }, [ws, p.id]);
  useEffect(() => {
    load();
  }, [load]);

  async function act(a: PlaybookAutomation, action: "approve" | "pause" | "run") {
    setError("");
    const path = { params: { path: { ws, id: a.id } } };
    const { data, error } =
      action === "approve" ? await api.POST("/v1/workspaces/{ws}/playbook-automations/{id}/approve", path)
      : action === "pause" ? await api.POST("/v1/workspaces/{ws}/playbook-automations/{id}/pause", path)
      : await api.POST("/v1/workspaces/{ws}/playbook-automations/{id}/run", path);
    if (error || !data) return setError(errorMessage(error));
    if (action === "run" && "id" in data) {
      toast("Started a run now.");
      go("missions", data.id);
      return;
    }
    toast(action === "approve" ? `${a.name} is on. ${automationWhen(a)}.` : `Paused ${a.name}.`);
    load();
  }

  async function remove(a: PlaybookAutomation) {
    if (!(await confirmAction(`Delete ${a.name}?`, "No more runs start. The runs it already started stay as missions.", "Delete"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/playbook-automations/{id}", { params: { path: { ws, id: a.id } } });
    if (error) return setError(errorMessage(error));
    load();
  }

  if (!list) return null;
  return (
    <section className="space-y-2">
      <h3 className="font-medium">Run it by itself</h3>
      <p className="text-muted-foreground">
        On a schedule, or when something happens. You approve it once, with its mode and spend limits, and each run starts as a mission inside them. It stops and tells you why if a limit is reached or a connection goes missing.
      </p>
      <ErrorText>{error}</ErrorText>
      {list.length > 0 && (
        <ul className="space-y-2">
          {list.map((a) => (
            <li key={a.id} className="space-y-1.5 rounded-md border p-3">
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-medium">{a.name}</span>
                <Badge tone={status[a.status].tone}>{status[a.status].label}</Badge>
              </div>
              <div className="text-muted-foreground">
                {automationWhen(a)} · {modes[a.mode]}
                {a.max_spend_cents_per_month != null && <> · {dollars(a.spent_this_month_cents)} of {dollars(a.max_spend_cents_per_month)} this month</>}
                {a.max_spend_cents_per_run != null && <> · up to {dollars(a.max_spend_cents_per_run)} a run</>}
              </div>
              {a.status === "held" && <p className="text-red-700 dark:text-red-400">{a.held_reason}</p>}
              <div className="text-[12px] text-muted-foreground">
                {a.status === "active" && a.next_run_at && <>Next {when(a.next_run_at)} · </>}
                {plural(a.runs, "run")}
                {a.last_run_at && <> · last {ago(a.last_run_at)}</>}
                {a.last_mission_id && <> · <button type="button" className="underline underline-offset-4" onClick={() => go("missions", a.last_mission_id!)}>Last run</button></>}
              </div>
              {canManage && (
                <div className="flex flex-wrap gap-2 pt-1">
                  {a.status !== "active" && <Button onClick={() => act(a, "approve")}>{a.status === "draft" ? "Approve and turn on" : "Turn back on"}</Button>}
                  {a.status === "active" && <Button variant="ghost" onClick={() => act(a, "run")}>Run now</Button>}
                  {(a.status === "active" || a.status === "held") && <Button variant="ghost" onClick={() => act(a, "pause")}>Pause</Button>}
                  <Button variant="ghost" onClick={() => remove(a)}>Delete</Button>
                </div>
              )}
            </li>
          ))}
        </ul>
      )}
      {canManage && (adding ? <AutomationForm ws={ws} playbook={p} inputs={inputs} onDone={(made) => { setAdding(false); if (made) load(); }} /> : (
        <Button variant="ghost" onClick={() => setAdding(true)} disabled={p.readiness.missing_connections.length > 0}>
          {p.readiness.missing_connections.length > 0 ? "Connect what it needs first" : "Set it to run by itself"}
        </Button>
      ))}
    </section>
  );
}

function AutomationForm({ ws, playbook: p, inputs, onDone }: { ws: string; playbook: WorkspacePlaybook; inputs: Record<string, string>; onDone: (made: boolean) => void }) {
  const sg = p.automation;
  const [kind, setKind] = useState<"schedule" | "event">(sg && !sg.schedule && sg.events.length > 0 ? "event" : "schedule");
  const [cadence, setCadence] = useState<"daily" | "weekly" | "monthly">(sg?.schedule ?? "weekly");
  const [runHour, setRunHour] = useState(9);
  const [event, setEvent] = useState(sg?.events[0] ?? "");
  const [gather, setGather] = useState<PlaybookAutomation["gather"]>(sg?.gather ?? "daily");
  const [mode, setMode] = useState<PlaybookAutomation["mode"]>("approve_writes");
  const [perRun, setPerRun] = useState("");
  const [perMonth, setPerMonth] = useState("");
  const [events, setEvents] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    api.GET("/v1/webhook-events").then(({ data }) => setEvents((data?.data ?? []).map((e) => e.type).filter((t) => t !== "webhook.test")));
  }, []);

  const cents = (s: string) => (s.trim() === "" ? null : Math.round(Number(s) * 100));

  async function save(e: FormEvent) {
    e.preventDefault();
    const run = cents(perRun), month = cents(perMonth);
    if ((run != null && !(run >= 0)) || (month != null && !(month >= 0))) return setError("Spend limits are amounts in dollars, or empty for none.");
    const body: PlaybookAutomationInput = {
      playbook_id: p.id, inputs, kind, mode, runner: "builtin", run_hour: runHour,
      max_spend_cents_per_run: run, max_spend_cents_per_month: month,
      ...(kind === "schedule" ? { cadence } : { event_type: event, gather }),
    };
    setBusy(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/playbook-automations", { params: { path: { ws } }, body });
    setBusy(false);
    if (error || !data || !("id" in data)) return setError(errorMessage(error));
    toast("Set up. Approve it to turn it on.");
    onDone(true);
  }

  const suggested = sg ? [sg.schedule && `${sg.schedule} runs`, sg.events.length > 0 && `runs when ${sg.events.join(" or ")} happens`].filter(Boolean).join(", or ") : "";
  return (
    <form className="space-y-3 rounded-md border p-3" onSubmit={save}>
      {suggested && <p className="text-muted-foreground">This playbook suggests {suggested}. It uses the inputs filled in above.</p>}
      <label className="flex flex-col gap-1">
        <span>Runs</span>
        <select className={field} value={kind} onChange={(e) => setKind(e.target.value as typeof kind)}>
          <option value="schedule">On a schedule</option>
          <option value="event">When something happens</option>
        </select>
      </label>
      {kind === "schedule" ? (
        <div className="flex flex-wrap gap-2">
          <select aria-label="How often" className={field} value={cadence} onChange={(e) => setCadence(e.target.value as typeof cadence)}>
            <option value="daily">Every day</option>
            <option value="weekly">Every week (Monday)</option>
            <option value="monthly">Every month (the 1st)</option>
          </select>
          <select aria-label="At" className={field} value={runHour} onChange={(e) => setRunHour(Number(e.target.value))}>
            {Array.from({ length: 24 }, (_, h) => <option key={h} value={h}>{hour(h)}</option>)}
          </select>
        </div>
      ) : (
        <div className="flex flex-col gap-2">
          <select aria-label="Event" className={field} value={event} onChange={(e) => setEvent(e.target.value)} required>
            <option value="">Choose an event…</option>
            {(sg?.events ?? []).map((t) => <option key={"s" + t} value={t}>{t} (suggested)</option>)}
            {events.filter((t) => !sg?.events.includes(t)).map((t) => <option key={t} value={t}>{t}</option>)}
          </select>
          <select aria-label="Gather" className={field} value={gather} onChange={(e) => setGather(e.target.value as typeof gather)}>
            {Object.entries(gathers).map(([k, v]) => <option key={k} value={k}>{v}</option>)}
          </select>
        </div>
      )}
      <label className="flex flex-col gap-1">
        <span>Each run</span>
        <select className={field} value={mode} onChange={(e) => setMode(e.target.value as typeof mode)}>
          {Object.entries(modes).map(([k, v]) => <option key={k} value={k}>{v}</option>)}
        </select>
      </label>
      <div className="flex flex-wrap gap-2">
        <label className="flex flex-col gap-1">
          <span>Spend limit a run ($)</span>
          <input className={field} inputMode="decimal" placeholder="No limit" value={perRun} onChange={(e) => setPerRun(e.target.value)} />
        </label>
        <label className="flex flex-col gap-1">
          <span>Spend limit a month ($)</span>
          <input className={field} inputMode="decimal" placeholder="No limit" value={perMonth} onChange={(e) => setPerMonth(e.target.value)} />
        </label>
      </div>
      <p className="text-[12px] text-muted-foreground">Outflare's agent runs each mission with your own AI model. Spend is on your own connected accounts. Link pitches still wait for your approval.</p>
      <ErrorText>{error}</ErrorText>
      <div className="flex gap-2">
        <Button type="submit" disabled={busy}>{busy ? "Saving…" : "Set it up"}</Button>
        <Button type="button" variant="ghost" onClick={() => onDone(false)}>Cancel</Button>
      </div>
    </form>
  );
}
