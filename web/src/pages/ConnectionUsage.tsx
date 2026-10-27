import { useEffect, useState } from "react";
import type { components } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Button, Card, ErrorText, field, Stat, Stats, toast } from "../ui";
import { ago } from "../format";

// What a paid connection (SEO data, lead data, email verification, page
// reads) used today and this month, and the daily and monthly limits that
// stop Outflare making more of its calls.

export type ConnectionUsage = components["schemas"]["ConnectionUsage"];

// amount writes a usage in its unit: $1.23, 1,500 API units, 1 credit.
export function amount(unit: string, v: number) {
  if (unit === "USD") return `$${v.toFixed(2)}`;
  const n = Math.abs(v) < 100 && !Number.isInteger(v) ? +v.toFixed(2) : Math.round(v);
  return `${n.toLocaleString()} ${n === 1 || unit.endsWith("s") ? unit : `${unit}s`}`;
}

const unitLabel = (unit: string) => (unit === "USD" ? "US dollars" : `${unit}s`);

// usageLine is the one line the Connectors table shows for a paid
// connection.
export function usageLine(u: ConnectionUsage) {
  const l = u.limits;
  if (l.monthly_limit != null) return `${amount(u.used.unit, u.used.month)} of ${amount(l.unit, l.monthly_limit)} this month`;
  if (l.daily_limit != null) return `${amount(u.used.unit, u.used.today)} of ${amount(l.unit, l.daily_limit)} today`;
  return `${amount(u.used.unit, u.used.month)} this month, no limit`;
}

const tone = (used: number, limit?: number | null) => (limit == null ? undefined : used >= limit ? ("red" as const) : used >= limit * 0.8 ? ("amber" as const) : undefined);

export function UsageLimits({ ws, integrationId, name, canManage }: { ws: string; integrationId: string; name: string; canManage: boolean }) {
  const [u, setU] = useState<ConnectionUsage | null>(null);
  const [error, setError] = useState("");
  const [unit, setUnit] = useState("");
  const [daily, setDaily] = useState("");
  const [monthly, setMonthly] = useState("");
  const [stop, setStop] = useState(true);
  const [busy, setBusy] = useState(false);

  function show(data: ConnectionUsage) {
    setU(data);
    setUnit(data.limits.unit);
    setDaily(data.limits.daily_limit == null ? "" : String(data.limits.daily_limit));
    setMonthly(data.limits.monthly_limit == null ? "" : String(data.limits.monthly_limit));
    setStop(data.limits.hard_stop);
  }

  useEffect(() => {
    void (async () => {
      const { data, error } = await api.GET("/v1/workspaces/{ws}/integrations/{id}/usage", { params: { path: { ws, id: integrationId } } });
      if (error) return setError(errorMessage(error));
      show(data);
    })();
  }, [ws, integrationId]);

  if (!u) return <ErrorText>{error}</ErrorText>;

  const l = u.limits;
  const num = (v: string) => (v.trim() === "" ? null : Number(v));
  const bad = [daily, monthly].some((v) => v.trim() !== "" && !(Number(v) >= 0));
  const changed = unit !== l.unit || num(daily) !== (l.daily_limit ?? null) || num(monthly) !== (l.monthly_limit ?? null) || stop !== l.hard_stop;
  const peak = Math.max(1e-9, ...(u.days ?? []).map((d) => d.used));

  async function save() {
    setBusy(true);
    const { data, error } = await api.PUT("/v1/workspaces/{ws}/integrations/{id}/usage/limits", {
      params: { path: { ws, id: integrationId } },
      body: { unit, daily_limit: num(daily), monthly_limit: num(monthly), hard_stop: stop },
    });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    setError("");
    toast("Limits saved.");
    show(data);
  }

  async function reset() {
    setBusy(true);
    const { data, error } = await api.DELETE("/v1/workspaces/{ws}/integrations/{id}/usage/limits", { params: { path: { ws, id: integrationId } } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    setError("");
    toast("Limits reset to the default.");
    show(data);
  }

  return (
    <Card title="Usage and limits" description={`What Outflare's calls through ${name} used, in ${unitLabel(u.used.unit)}${u.estimated ? ", estimated from the provider's price list" : ""}. Days and months are UTC.`}>
      <div className="space-y-4">
        <p className="rounded-md border border-amber-300/60 bg-amber-50 px-3 py-2 text-[12px] text-amber-900 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200">
          These calls run on your own {name} account, and {name} bills you for them. Outflare doesn't pay for them. Set a spend limit with {name} too: the limits here are a second line, not a guarantee.
        </p>
        <Stats cols={2}>
          <Stat label="Today" value={amount(u.used.unit, u.used.today)} sub={l.daily_limit != null ? `of ${amount(l.unit, l.daily_limit)} daily limit` : "no daily limit"} tone={tone(u.used.today, l.daily_limit)} />
          <Stat label="This month" value={amount(u.used.unit, u.used.month)} sub={l.monthly_limit != null ? `of ${amount(l.unit, l.monthly_limit)} monthly limit${l.default ? " (starting limit)" : ""}` : "no monthly limit"} tone={tone(u.used.month, l.monthly_limit)} />
        </Stats>

        {(u.days ?? []).length > 0 && (
          <div>
            <div className="mb-1 text-[12px] text-muted-foreground">Last 30 days</div>
            <div className="flex h-16 items-end gap-0.5" role="img" aria-label={`${name} usage by day`}>
              {(u.days ?? []).map((d) => (
                <div key={d.date} className="min-w-1 flex-1 rounded-t-sm bg-brand/70" style={{ height: `${Math.max(2, (d.used / peak) * 100)}%` }} title={`${d.date}: ${amount(u.used.unit, d.used)}, ${d.calls} ${d.calls === 1 ? "charge" : "charges"}`} />
              ))}
            </div>
          </div>
        )}

        {canManage ? (
          <div className="grid gap-3 text-[13px] sm:grid-cols-3">
            {u.units.length > 1 && (
              <label className="space-y-1 sm:col-span-3">
                <span className="font-medium">Count in</span>
                <select className={`${field} w-full sm:w-56`} value={unit} onChange={(e) => setUnit(e.target.value)}>
                  {u.units.map((x) => <option key={x} value={x}>{unitLabel(x)}</option>)}
                </select>
              </label>
            )}
            <label className="space-y-1">
              <span className="font-medium">Daily limit</span>
              <input className={`${field} w-full`} inputMode="decimal" placeholder="No limit" value={daily} onChange={(e) => setDaily(e.target.value.replace(/[^0-9.]/g, ""))} aria-label={`Daily limit in ${unitLabel(unit)}`} />
            </label>
            <label className="space-y-1">
              <span className="font-medium">Monthly limit</span>
              <input className={`${field} w-full`} inputMode="decimal" placeholder="No limit" value={monthly} onChange={(e) => setMonthly(e.target.value.replace(/[^0-9.]/g, ""))} aria-label={`Monthly limit in ${unitLabel(unit)}`} />
            </label>
            <label className="flex items-center gap-2 self-end pb-1.5">
              <input type="checkbox" checked={stop} onChange={(e) => setStop(e.target.checked)} />
              <span>Stop calls at the limit</span>
            </label>
            <p className="text-[12px] text-muted-foreground sm:col-span-3">
              In {unitLabel(unit)}. Leave a limit empty for none; 0 stops every call. Owners and admins are emailed at 80% and when a limit is reached.
              {!stop && " With stopping off, the limits only warn."}
            </p>
            <div className="flex flex-wrap gap-2 sm:col-span-3">
              <Button disabled={busy || !changed || bad} onClick={save}>{busy ? "Saving…" : "Save limits"}</Button>
              {!l.default && <Button variant="ghost" disabled={busy} onClick={reset}>Reset to default</Button>}
            </div>
          </div>
        ) : (
          <p className="text-[13px] text-muted-foreground">Only owners and admins can change limits.</p>
        )}
        <ErrorText>{error}</ErrorText>

        {(u.alerts ?? []).length > 0 && (
          <div>
            <div className="mb-1 text-[12px] font-medium">Warnings sent</div>
            <ul className="space-y-0.5 text-[12px] text-muted-foreground">
              {(u.alerts ?? []).map((a) => (
                <li key={`${a.period}${a.period_start}${a.level}`}>
                  {a.level >= 100 ? "Reached" : "80% of"} the {a.period === "day" ? "daily" : "monthly"} limit of {amount(a.unit, a.limit)} · {ago(a.sent_at)}
                </li>
              ))}
            </ul>
          </div>
        )}
      </div>
    </Card>
  );
}
