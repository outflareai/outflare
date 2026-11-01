import { useEffect, useState } from "react";
import type { SupportSLAPolicy } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Button, ErrorText, Loading, toast } from "../ui";
import { cn } from "@/lib/utils";
import { track } from "@/lib/analytics";

// Response times: the workspace's SLA. How fast the team promises to reply
// and resolve, around the clock or in its working hours. A brand can have
// its own through the API; this edits the workspace's.

type Unit = "minutes" | "hours" | "days";
type Target = { on: boolean; value: string; unit: Unit };

const unitMinutes: Record<Unit, number> = { minutes: 1, hours: 60, days: 1440 };
const days = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

function toTarget(m: number | null | undefined, fallback: Target): Target {
  if (m == null) return { ...fallback, on: false };
  for (const unit of ["days", "hours"] as const) {
    if (m % unitMinutes[unit] === 0) return { on: true, value: String(m / unitMinutes[unit]), unit };
  }
  return { on: true, value: String(m), unit: "minutes" };
}

function minutes(t: Target): number | null {
  if (!t.on) return null;
  const n = Math.round(Number(t.value) * unitMinutes[t.unit]);
  return Number.isFinite(n) && n >= 1 ? n : NaN;
}

const clock = (m: number) => `${String(Math.floor(m / 60)).padStart(2, "0")}:${String(m % 60).padStart(2, "0")}`;
const fromClock = (s: string) => {
  const [h, m] = s.split(":").map(Number);
  return h * 60 + m;
};

function zones(): string[] {
  try {
    return (Intl as unknown as { supportedValuesOf?: (k: string) => string[] }).supportedValuesOf?.("timeZone") ?? ["UTC"];
  } catch {
    return ["UTC"];
  }
}

export function SupportSLA({ ws, canManage, onChanged }: { ws: string; canManage: boolean; onChanged?: () => void }) {
  const [policy, setPolicy] = useState<SupportSLAPolicy | null | undefined>(undefined);
  const [first, setFirst] = useState<Target>({ on: true, value: "4", unit: "hours" });
  const [next, setNext] = useState<Target>({ on: false, value: "8", unit: "hours" });
  const [resolve, setResolve] = useState<Target>({ on: false, value: "2", unit: "days" });
  const [business, setBusiness] = useState(true);
  const [open, setOpen] = useState<boolean[]>([false, true, true, true, true, true, false]);
  const [start, setStart] = useState("09:00");
  const [end, setEnd] = useState("17:00");
  const [tz, setTz] = useState(() => Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/support/sla-policies", { params: { path: { ws } } }).then(({ data, error }) => {
      if (error) return setError(errorMessage(error));
      const p = data.data.find((x) => x.brand_id === null) ?? null;
      setPolicy(p);
      if (!p) return;
      setFirst((d) => toTarget(p.first_reply_minutes, d));
      setNext((d) => toTarget(p.next_reply_minutes, d));
      setResolve((d) => toTarget(p.resolution_minutes, d));
      setTz(p.timezone);
      setBusiness(p.hours !== null);
      if (p.hours) {
        setOpen(p.hours.map((spans) => spans.length > 0));
        const span = p.hours.find((spans) => spans.length > 0)?.[0];
        if (span) {
          setStart(clock(span[0]));
          setEnd(clock(span[1]));
        }
      }
    });
  }, [ws]);

  async function save(enabled = true) {
    const targets = [minutes(first), minutes(next), minutes(resolve)];
    if (targets.some((t) => Number.isNaN(t))) return setError("Each target needs a number above zero.");
    if (enabled && targets.every((t) => t === null)) return setError("Turn on at least one target.");
    const s = fromClock(start), e = end === "00:00" ? 1440 : fromClock(end);
    if (business && (s >= e || !open.some(Boolean))) return setError("Working hours need at least one day, and a start before the end.");
    setBusy(true);
    setError("");
    const { data, error } = await api.PUT("/v1/workspaces/{ws}/support/sla-policies/{scope}", {
      params: { path: { ws, scope: "default" } },
      body: {
        enabled,
        first_reply_minutes: targets[0],
        next_reply_minutes: targets[1],
        resolution_minutes: targets[2],
        timezone: tz,
        hours: business ? open.map((o) => (o ? [[s, e]] : [])) : null,
      },
    });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    setPolicy(data);
    track("ui.support_sla_saved", { enabled, business_hours: business });
    toast(enabled ? "Response times saved: open conversations show when they're due" : "Response times turned off");
    onChanged?.();
  }

  if (policy === undefined && !error) return <Loading />;
  const on = policy?.enabled ?? false;
  const row = (label: string, hint: string, t: Target, set: (t: Target) => void) => (
    <div className="flex flex-wrap items-center gap-2 py-1.5">
      <label className="flex min-w-48 flex-1 items-start gap-2">
        <input type="checkbox" className="mt-0.5" checked={t.on} disabled={!canManage} onChange={(e) => set({ ...t, on: e.target.checked })} />
        <span>
          <span className="block text-[13px] font-medium">{label}</span>
          <span className="block text-xs text-muted-foreground">{hint}</span>
        </span>
      </label>
      <span className={cn("flex items-center gap-1.5", !t.on && "opacity-40")}>
        <input
          type="number"
          min={1}
          aria-label={`${label} within`}
          value={t.value}
          disabled={!t.on || !canManage}
          onChange={(e) => set({ ...t, value: e.target.value })}
          className="h-8 w-16 rounded-md border bg-background px-2 text-[13px] tabular-nums"
        />
        <select aria-label={`${label} unit`} value={t.unit} disabled={!t.on || !canManage} onChange={(e) => set({ ...t, unit: e.target.value as Unit })} className="h-8 rounded-md border bg-background px-2 text-[13px]">
          <option value="minutes">minutes</option>
          <option value="hours">hours</option>
          <option value="days">days</option>
        </select>
      </span>
    </div>
  );

  return (
    <div className="space-y-3 rounded-lg border p-4">
      <div className="flex items-start gap-3">
        <div className="min-w-0 flex-1">
          <p className="text-[13px] font-medium">Response times</p>
          <p className="mt-0.5 text-xs text-muted-foreground">
            Promise how fast your team answers. Each conversation shows when its reply is due, and Overdue collects the ones that ran late.
          </p>
        </div>
        {policy && <span className={cn("rounded-full px-2 py-0.5 text-[11px] font-medium", on ? "bg-emerald-500/10 text-emerald-700 dark:text-emerald-400" : "bg-muted text-muted-foreground")}>{on ? "On" : "Off"}</span>}
      </div>
      <div className="divide-y">
        {row("First reply", "From the customer's first email", first, setFirst)}
        {row("Next reply", "From each later email you haven't answered", next, setNext)}
        {row("Resolution", "From opening to closing the conversation", resolve, setResolve)}
      </div>
      <fieldset className="space-y-2" disabled={!canManage}>
        <legend className="mb-1 text-[13px] font-medium">Count time</legend>
        <div className="flex flex-wrap gap-4 text-[13px]">
          <label className="flex items-center gap-1.5"><input type="radio" name="sla-count" checked={business} onChange={() => setBusiness(true)} />In working hours</label>
          <label className="flex items-center gap-1.5"><input type="radio" name="sla-count" checked={!business} onChange={() => setBusiness(false)} />Around the clock</label>
        </div>
        {business && (
          <div className="space-y-2 rounded-md bg-muted/40 p-3">
            <div className="flex flex-wrap gap-1" role="group" aria-label="Working days">
              {days.map((d, i) => (
                <button
                  key={d}
                  type="button"
                  aria-pressed={open[i]}
                  onClick={() => setOpen(open.map((o, j) => (j === i ? !o : o)))}
                  className={cn("h-7 w-11 rounded-md border text-xs font-medium", open[i] ? "border-primary bg-primary text-primary-foreground" : "bg-background text-muted-foreground")}
                >
                  {d}
                </button>
              ))}
            </div>
            <div className="flex flex-wrap items-center gap-2 text-[13px]">
              <input type="time" aria-label="Opens at" value={start} onChange={(e) => setStart(e.target.value)} className="h-8 rounded-md border bg-background px-2" />
              <span className="text-muted-foreground">to</span>
              <input type="time" aria-label="Closes at" value={end} onChange={(e) => setEnd(e.target.value)} className="h-8 rounded-md border bg-background px-2" />
              <select aria-label="Time zone" value={tz} onChange={(e) => setTz(e.target.value)} className="h-8 max-w-full min-w-0 flex-1 basis-full rounded-md border bg-background px-2 sm:basis-auto">
                {[...new Set([tz, ...zones()])].map((z) => <option key={z} value={z}>{z.replace(/_/g, " ")}</option>)}
              </select>
            </div>
            <p className="text-xs text-muted-foreground">An email on Friday evening is due on Monday, not over the weekend.</p>
          </div>
        )}
      </fieldset>
      {error && <ErrorText>{error}</ErrorText>}
      {canManage ? (
        <div className="flex flex-wrap gap-2">
          <Button disabled={busy} onClick={() => save(true)}>{busy ? "Saving…" : policy && on ? "Save" : "Turn on"}</Button>
          {policy && on && <Button variant="ghost" disabled={busy} onClick={() => save(false)}>Turn off</Button>}
        </div>
      ) : (
        <p className="text-xs text-muted-foreground">Ask someone who manages support to change response times.</p>
      )}
    </div>
  );
}
