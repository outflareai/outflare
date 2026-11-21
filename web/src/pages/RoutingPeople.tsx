// Hours and capacity (docs/routing.md): when each teammate works, how many
// people and companies they take, and whether they are away. Routing gives
// new records to people at work first, and never to someone who is away
// or full.

import { useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Avatar } from "@/components/teammate-picker";
import { track } from "@/lib/analytics";
import { Badge, Button, Card, ErrorText, field, toast } from "../ui";

export type RoutingPerson = Schemas["RoutingPerson"];
type Draft = Schemas["RoutingPersonInput"];
type Member = { user_id: string; name: string; email: string; avatar_url?: string | null };

const dayNames = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];
const here = (() => {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
})();
const zones: string[] = (() => {
  try {
    return ["UTC", ...(Intl as unknown as { supportedValuesOf: (k: string) => string[] }).supportedValuesOf("timeZone")];
  } catch {
    return ["UTC", here];
  }
})();

// A run of days in words: Mon to Fri, or Mon, Wed, Fri.
function daysText(days: number[]): string {
  if (days.length === 0 || days.length === 7) return "Every day";
  const run = days.every((d, i) => i === 0 || d === days[i - 1] + 1);
  return run && days.length > 2 ? `${dayNames[days[0] - 1]} to ${dayNames[days[days.length - 1] - 1]}` : days.map((d) => dayNames[d - 1]).join(", ");
}

function summary(p: Draft | undefined): string {
  if (!p) return "Always at work · no limit";
  const hours = p.from && p.to ? `${daysText(p.days ?? [])}, ${p.from} to ${p.to} (${(p.timezone || "UTC").replace(/_/g, " ")})` : (p.days ?? []).length > 0 && (p.days ?? []).length < 7 ? daysText(p.days ?? []) : "Always at work";
  return `${hours} · ${p.capacity == null ? "no limit" : `up to ${p.capacity}`}`;
}

// Nothing set is the same as not being listed.
const plain = (p: Draft) => !p.paused && p.capacity == null && !p.from && (p.days ?? []).length === 0;

export function RoutingPeople({ brand, members, saved, canManage, onSaved }: { brand: string; members: Member[]; saved: RoutingPerson[]; canManage: boolean; onSaved: (people: RoutingPerson[]) => void }) {
  const [drafts, setDrafts] = useState<Record<string, Draft> | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const current: Record<string, Draft> = drafts ?? Object.fromEntries(saved.map((p) => [p.user_id, { user_id: p.user_id, capacity: p.capacity, days: p.days, from: p.from, to: p.to, timezone: p.timezone, paused: p.paused }]));
  const live = Object.fromEntries(saved.map((p) => [p.user_id, p]));
  const set = (id: string, patch: Partial<Draft>) => setDrafts({ ...current, [id]: { timezone: here, ...current[id], ...patch, user_id: id } });
  const bad = Object.values(current).find((p) => (p.from || p.to) && (!p.from || !p.to || p.from === p.to));

  async function save() {
    setBusy(true);
    setError("");
    const people = Object.values(current).filter((p) => !plain(p));
    const { data, error } = await api.PUT("/v1/brands/{brand}/routing/people", { params: { path: { brand } }, body: { people } });
    setBusy(false);
    if (error || !data || !("people" in data)) return setError(errorMessage(error));
    track("ui.routing_people_saved", { people: people.length });
    toast("Saved. Routing follows these from now on.");
    setDrafts(null);
    setOpen(null);
    onSaved(data.people);
  }

  return (
    <Card title="Hours and capacity" description="When each teammate works and how many people and companies they can look after. New ones go to someone at work first, and never to someone who is away or full. If nobody is at work, the next person still gets it, so nothing waits without an owner.">
      {error && <ErrorText>{error}</ErrorText>}
      {members.length === 0 ? (
        <p className="py-1 text-sm text-muted-foreground">Nobody else is in this workspace yet.</p>
      ) : (
        <ul className="divide-y">
          {members.map((m) => {
            const p = current[m.user_id];
            const now = live[m.user_id];
            const full = now && now.capacity != null && now.owned >= now.capacity;
            return (
              <li key={m.user_id} className="py-2.5 text-sm">
                <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
                  <Avatar name={m.name || m.email} url={m.avatar_url} id={m.user_id} />
                  <span className="min-w-[10rem] flex-1">
                    <span className="block font-medium">
                      {m.name || m.email} {p?.paused ? <Badge>Away</Badge> : full ? <Badge>Full</Badge> : now && !drafts && !now.at_work ? <Badge>Off now</Badge> : null}
                    </span>
                    <span className="block text-[13px] text-muted-foreground">
                      {summary(p && !plain(p) ? p : undefined)}
                      {now && !drafts ? ` · owns ${now.owned}` : ""}
                    </span>
                  </span>
                  {canManage && (
                    <Button variant="ghost" onClick={() => setOpen(open === m.user_id ? null : m.user_id)}>
                      {open === m.user_id ? "Done" : "Change"}
                    </Button>
                  )}
                </div>
                {open === m.user_id && (
                  <div className="mt-3 grid gap-4 rounded-md border bg-muted/30 p-3 sm:grid-cols-2">
                    <label className="flex items-center gap-2 text-[13px] sm:col-span-2">
                      <input type="checkbox" className="size-4" checked={!!p?.paused} onChange={(e) => set(m.user_id, { paused: e.target.checked })} />
                      Away (on leave): give them nothing until this is switched off
                    </label>
                    <label className="text-[13px] text-muted-foreground">
                      Most people and companies they own at once
                      <input
                        type="number"
                        min={0}
                        className={`${field} mt-1 block w-40 text-foreground`}
                        placeholder="No limit"
                        value={p?.capacity ?? ""}
                        onChange={(e) => set(m.user_id, { capacity: e.target.value === "" ? null : Math.max(0, Math.round(Number(e.target.value))) })}
                      />
                    </label>
                    <div className="text-[13px] text-muted-foreground">
                      Working days
                      <div className="mt-1 flex flex-wrap gap-1">
                        {dayNames.map((d, i) => {
                          const on = (p?.days ?? []).includes(i + 1);
                          return (
                            <button
                              key={d}
                              type="button"
                              aria-pressed={on}
                              className={`h-8 rounded-md border px-2.5 text-[13px] ${on ? "border-primary bg-primary text-primary-foreground" : "bg-background text-foreground hover:bg-muted"}`}
                              onClick={() => set(m.user_id, { days: on ? (p?.days ?? []).filter((x) => x !== i + 1) : [...(p?.days ?? []), i + 1].sort() })}
                            >
                              {d}
                            </button>
                          );
                        })}
                      </div>
                      <span className="mt-1 block text-[12px]">None picked means every day.</span>
                    </div>
                    <div className="text-[13px] text-muted-foreground">
                      Working hours
                      <div className="mt-1 flex flex-wrap items-center gap-2 text-foreground">
                        <input aria-label="From" type="time" className={field} value={p?.from ?? ""} onChange={(e) => set(m.user_id, { from: e.target.value || null })} />
                        to
                        <input aria-label="To" type="time" className={field} value={p?.to ?? ""} onChange={(e) => set(m.user_id, { to: e.target.value || null })} />
                        {(p?.from || p?.to) && (
                          <Button variant="ghost" onClick={() => set(m.user_id, { from: null, to: null })}>
                            All day
                          </Button>
                        )}
                      </div>
                      <span className="mt-1 block text-[12px]">{(p?.from || p?.to) && (!p?.from || !p?.to || p.from === p.to) ? "Set both times, and make them differ." : "Leave empty for all day."}</span>
                    </div>
                    <label className="text-[13px] text-muted-foreground">
                      Their time zone
                      <select className={`${field} mt-1 block w-full max-w-64 text-foreground`} value={p?.timezone || here} onChange={(e) => set(m.user_id, { timezone: e.target.value })}>
                        {[...new Set([p?.timezone || here, here, ...zones])].map((z) => (
                          <option key={z} value={z}>
                            {z.replace(/_/g, " ")}
                          </option>
                        ))}
                      </select>
                    </label>
                  </div>
                )}
              </li>
            );
          })}
        </ul>
      )}
      {canManage && drafts && (
        <div className="mt-3 flex flex-wrap items-center gap-3">
          <Button disabled={busy || !!bad} onClick={save}>
            {busy ? "Saving…" : "Save hours and capacity"}
          </Button>
          <Button variant="ghost" disabled={busy} onClick={() => (setDrafts(null), setOpen(null))}>
            Discard changes
          </Button>
        </div>
      )}
    </Card>
  );
}
