// Guardrails on marketing email: how many one person may get a day and a
// week, and quiet hours when it waits until morning. On by default; each
// workspace changes them here. Transactional email is never capped.

import { useEffect, useState, type FormEvent } from "react";
import type { Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Button, Card, ErrorText, field, Input, toast } from "../ui";

export type Guardrails = Schemas["EmailGuardrails"];

export function useGuardrails(ws: string): [Guardrails | null, (g: Guardrails) => void] {
  const [g, setG] = useState<Guardrails | null>(null);
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/email-guardrails", { params: { path: { ws } } }).then(({ data }) => data && setG(data));
  }, [ws]);
  return [g, setG];
}

const hour = (h: number) => `${h % 12 === 0 ? 12 : h % 12}${h < 12 ? "am" : "pm"}`;

// guardrailsText says the guardrails in a sentence, or that they're off.
export function guardrailsText(g: Guardrails): string {
  const parts: string[] = [];
  if (g.cap_enabled && (g.per_day || g.per_week)) {
    const limits = [g.per_day ? `${g.per_day} a day` : "", g.per_week ? `${g.per_week} a week` : ""].filter(Boolean).join(" and ");
    parts.push(`at most ${limits} per person`);
  }
  if (g.quiet_hours_enabled) parts.push(`none from ${hour(g.quiet_start)} to ${hour(g.quiet_end)} in their time zone`);
  return parts.length ? `Marketing email: ${parts.join(", ")}.` : "Marketing email guardrails are off.";
}

export function GuardrailsCard({ ws, canManage }: { ws: string; canManage: boolean }) {
  const [saved, setSaved] = useGuardrails(ws);
  const [g, setG] = useState<Guardrails | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => setG(saved), [saved]);
  if (!g || !saved) return null;
  const changed = JSON.stringify(g) !== JSON.stringify(saved);
  const disabled = !canManage;
  async function save(e: FormEvent) {
    e.preventDefault();
    if (!g) return;
    setBusy(true);
    const { data, error } = await api.PATCH("/v1/workspaces/{ws}/email-guardrails", { params: { path: { ws } }, body: g });
    setBusy(false);
    if (error || !data) return setError(errorMessage(error));
    setError("");
    setSaved(data);
    toast("Saved the guardrails.");
  }
  const hours = Array.from({ length: 24 }, (_, h) => h);
  return (
    <Card title="Guardrails" description="Keep journeys and broadcasts from piling up on one person. Receipts and other transactional email are never held back.">
      <form id="guardrails" onSubmit={save} className="space-y-4 text-[13px]">
        <fieldset className="space-y-2">
          <label className="flex items-center gap-2 font-medium">
            <input type="checkbox" checked={g.cap_enabled} disabled={disabled} onChange={(e) => setG({ ...g, cap_enabled: e.target.checked })} />
            Frequency cap
          </label>
          <div className="flex flex-wrap items-end gap-3 pl-6">
            <Input label="Per day" type="number" min={0} max={100} className={`${field} w-24`} value={String(g.per_day)} disabled={disabled || !g.cap_enabled}
              onChange={(e) => setG({ ...g, per_day: Math.max(0, Number(e.target.value)) })} />
            <Input label="Per week" type="number" min={0} max={500} className={`${field} w-24`} value={String(g.per_week)} disabled={disabled || !g.cap_enabled}
              onChange={(e) => setG({ ...g, per_week: Math.max(0, Number(e.target.value)) })} />
          </div>
          <p className="pl-6 text-[12px] text-muted-foreground">0 means no limit. An email over the cap isn't sent: the journey moves on, and the person's timeline says it was capped.</p>
        </fieldset>
        <fieldset className="space-y-2">
          <label className="flex items-center gap-2 font-medium">
            <input type="checkbox" checked={g.quiet_hours_enabled} disabled={disabled} onChange={(e) => setG({ ...g, quiet_hours_enabled: e.target.checked })} />
            Quiet hours
          </label>
          <div className="flex flex-wrap items-center gap-2 pl-6">
            <span>From</span>
            <select aria-label="Quiet hours start" className={field} value={g.quiet_start} disabled={disabled || !g.quiet_hours_enabled} onChange={(e) => setG({ ...g, quiet_start: Number(e.target.value) })}>
              {hours.map((h) => <option key={h} value={h}>{hour(h)}</option>)}
            </select>
            <span>to</span>
            <select aria-label="Quiet hours end" className={field} value={g.quiet_end} disabled={disabled || !g.quiet_hours_enabled} onChange={(e) => setG({ ...g, quiet_end: Number(e.target.value) })}>
              {hours.map((h) => <option key={h} value={h}>{hour(h)}</option>)}
            </select>
          </div>
          <div className="pl-6">
            <Input label="Time zone when a person's own is unknown" className={`${field} w-64`} value={g.time_zone} disabled={disabled || !g.quiet_hours_enabled} maxLength={100}
              placeholder="Europe/Paris" onChange={(e) => setG({ ...g, time_zone: e.target.value })} />
          </div>
          <p className="pl-6 text-[12px] text-muted-foreground">Email due in quiet hours waits until they end, by the person's timezone field.</p>
        </fieldset>
        <ErrorText>{error}</ErrorText>
        {canManage && (
          <div className="flex justify-end gap-2">
            {changed && <Button type="button" variant="ghost" onClick={() => setG(saved)}>Undo</Button>}
            <Button disabled={busy || !changed}>Save</Button>
          </div>
        )}
      </form>
    </Card>
  );
}
