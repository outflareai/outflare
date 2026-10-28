import { useEffect, useState } from "react";
import type { components } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Button, Card, Empty, ErrorText, field, PageHeader, Stat, Stats, toast } from "../ui";
import { appPath, type Tab } from "@/lib/paths";
import { semanticSearchDocs } from "./SemanticSearch";

type Usage = components["schemas"]["AIUsage"];

const dollars = (cents: number) => `$${(cents / 100).toFixed(2)}`;
const tokens = (n: number) => (n >= 1e6 ? `${+(n / 1e6).toFixed(1)}M` : n >= 1e3 ? `${+(n / 1e3).toFixed(1)}k` : String(n));
const monthName = (m: string) => new Date(`${m}-01T00:00:00Z`).toLocaleDateString(undefined, { month: "long", year: "numeric", timeZone: "UTC" });
// This month and the twelve before it, as YYYY-MM.
const months = Array.from({ length: 13 }, (_, i) => {
  const d = new Date();
  d.setUTCDate(1);
  d.setUTCMonth(d.getUTCMonth() - i);
  return d.toISOString().slice(0, 7);
});

// The notice every AI cost page and connection shows: the provider bills
// the workspace, and Outflare takes no part in it.
export function AICostNotice({ className = "" }: { className?: string }) {
  return (
    <p className={`rounded-md border border-amber-300/60 bg-amber-50 px-3 py-2 text-[12px] text-amber-900 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200 ${className}`}>
      AI calls run on your own provider account, with your own key, and your provider bills you for them. Outflare doesn't pay for them and takes no responsibility for what they cost.
      Set a hard spend limit in your provider's account: the limits in Outflare are a second line, not a guarantee.
    </p>
  );
}

// Where the workspace's AI spend goes inside Outflare: by tool, by model and
// by day, with a monthly limit per tool.
export function AIUsage({ ws, canManage, go }: { ws: string; canManage: boolean; go: (t: Tab) => void }) {
  const [month, setMonth] = useState(months[0]);
  const [u, setU] = useState<Usage | null>(null);
  const [limits, setLimits] = useState<Record<string, string>>({});
  // Whether reaching each limit stops calls (true) or only warns.
  const [stops, setStops] = useState<Record<string, boolean>>({});
  const [wsStop, setWsStop] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  function show(data: Usage) {
    setU(data);
    setLimits(Object.fromEntries(data.tools.map((t) => [t.task, t.monthly_limit_cents == null ? "" : String(t.monthly_limit_cents / 100)])));
    setStops(Object.fromEntries(data.tools.map((t) => [t.task, t.hard_stop])));
    setWsStop(data.hard_stop);
  }

  async function load() {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/ai-usage", { params: { path: { ws }, query: { month } } });
    if (error) return setError(errorMessage(error));
    setError("");
    show(data);
  }
  useEffect(() => {
    setU(null);
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, month]);

  async function saveLimits() {
    setBusy(true);
    const body = {
      limits: Object.fromEntries(Object.entries(limits).map(([k, v]) => [k, v.trim() === "" ? null : Math.round(parseFloat(v) * 100)])),
      hard_stop: wsStop,
      hard_stops: Object.fromEntries(Object.entries(stops).filter(([, stop]) => !stop)),
    };
    const { error } = await api.PUT("/v1/workspaces/{ws}/ai-usage/limits", { params: { path: { ws } }, body });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    toast("Limits saved.");
    load();
  }

  const header = (
    <PageHeader
      title="AI usage"
      description="Where your AI spend goes inside Outflare: each tool, model and day. Costs are what your provider charges your account."
      action={
        <select aria-label="Month" className={field} value={month} onChange={(e) => setMonth(e.target.value)}>
          {months.map((m) => <option key={m} value={m}>{monthName(m)}</option>)}
        </select>
      }
    />
  );
  if (!u) return <div className="space-y-4">{header}<ErrorText>{error}</ErrorText></div>;

  const cur = month === months[0];
  const peak = Math.max(1, ...u.days.map((d) => d.cost_cents));
  const used = u.tools.filter((t) => t.calls > 0);
  const limitChanged = wsStop !== u.hard_stop || u.tools.some((t) => (limits[t.task] ?? "") !== (t.monthly_limit_cents == null ? "" : String(t.monthly_limit_cents / 100)) || (stops[t.task] ?? true) !== t.hard_stop);
  const badLimit = Object.values(limits).some((v) => v.trim() !== "" && !(parseFloat(v) >= 0));

  return (
    <div className="space-y-4">
      {header}
      <AICostNotice />
      <Stats>
        <Stat label={cur ? "Spent this month" : "Spent"} value={dollars(u.spent_cents)}
          sub={u.monthly_limit_cents != null ? `of ${dollars(u.monthly_limit_cents)} workspace limit` : "no workspace limit"}
          tone={u.monthly_limit_cents != null && u.spent_cents >= u.monthly_limit_cents ? "red" : u.monthly_limit_cents != null && u.spent_cents * 10 >= u.monthly_limit_cents * 8 ? "amber" : undefined} />
        <Stat label="AI calls" value={u.total.calls.toLocaleString()} sub={`${used.length} ${used.length === 1 ? "tool" : "tools"}`} />
        <Stat label="Input tokens" value={tokens(u.total.input_tokens + u.total.cache_read_tokens + u.total.cache_write_tokens)} sub={u.total.cache_read_tokens ? `${tokens(u.total.cache_read_tokens)} from cache` : undefined} />
        <Stat label="Output tokens" value={tokens(u.total.output_tokens)} />
      </Stats>
      <ErrorText>{error}</ErrorText>

      {u.total.calls === 0 ? (
        <Empty title={cur ? "No AI calls yet this month" : "No AI calls this month"}>
          Planning missions, Outflare's agent, reply labelling, domain research, AI visibility, social replies and email verification all show up here once they run.
          {canManage && <> Choose your AI model and the workspace's monthly limit under <button type="button" className="underline underline-offset-2" onClick={() => go("workspace")}>General</button>.</>}
        </Empty>
      ) : (
        <Card title="Each day" description="UTC days, in US dollars.">
          <div className="flex h-24 items-end gap-0.5" role="img" aria-label="AI spend by day">
            {u.days.map((d) => (
              <div key={d.date} className="min-w-1 flex-1 rounded-t-sm bg-brand/70" style={{ height: `${Math.max(2, (d.cost_cents / peak) * 100)}%` }} title={`${d.date}: ${dollars(d.cost_cents)}, ${d.calls} calls`} />
            ))}
          </div>
        </Card>
      )}

      <Card title="By tool" description="A limit per tool stops that tool's AI calls for the rest of the month, on top of the workspace's limit. Owners and admins are emailed at 80% and when a limit is reached. Untick Stop to make a limit warn only: the emails still go out, but the calls carry on and your provider keeps billing you.">
        <div className="overflow-x-auto">
          <table className="w-full text-[13px]">
            <thead className="text-left text-[11px] text-muted-foreground uppercase">
              <tr><th className="py-1 font-medium">Tool</th><th className="font-medium">Calls</th><th className="font-medium">Tokens in / out</th><th className="font-medium">Cost</th><th className="font-medium">Monthly limit</th><th className="font-medium">Stop</th></tr>
            </thead>
            <tbody className="divide-y">
              {u.tools.map((t) => (
                <tr key={t.task}>
                  <td className="py-1.5 pr-3">
                    <div className="font-medium">{t.name}</div>
                    {t.description && <div className="text-xs text-muted-foreground">{t.description}</div>}
                    {t.task === "semantic_search" && (
                      <div className="text-xs text-muted-foreground">
                        <a className="underline underline-offset-2" href={semanticSearchDocs} target="_blank" rel="noreferrer">Why this costs anything</a>
                        {" · "}
                        <a className="underline underline-offset-2" href={appPath(ws, "workspace") + "#meaning-search"}>Turn it off</a>
                      </div>
                    )}
                  </td>
                  <td className="tabular-nums">{t.calls.toLocaleString()}</td>
                  <td className="tabular-nums">{tokens(t.input_tokens + t.cache_read_tokens + t.cache_write_tokens)} / {tokens(t.output_tokens)}</td>
                  <td className="tabular-nums">{dollars(t.cost_cents)}{u.total.cost_cents > 0 && t.cost_cents > 0 && <span className="text-muted-foreground"> · {Math.round((t.cost_cents / u.total.cost_cents) * 100)}%</span>}</td>
                  <td>
                    <span className="flex h-8 w-28 items-center rounded-md border border-input pl-2 text-[13px]">
                      <span className="text-muted-foreground">US$</span>
                      <input aria-label={`Monthly limit for ${t.name} in US dollars`} className="h-full w-full bg-transparent px-1.5 outline-none disabled:opacity-50" inputMode="decimal" placeholder="none"
                        value={limits[t.task] ?? ""} disabled={!canManage} onChange={(e) => setLimits({ ...limits, [t.task]: e.target.value.replace(/[^0-9.]/g, "") })} />
                    </span>
                  </td>
                  <td>
                    <input type="checkbox" aria-label={`Stop ${t.name} at its limit`} checked={stops[t.task] ?? true} disabled={!canManage}
                      onChange={(e) => setStops({ ...stops, [t.task]: e.target.checked })} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <label className="mt-3 flex items-center gap-2 text-[13px]">
          <input type="checkbox" checked={wsStop} disabled={!canManage} onChange={(e) => setWsStop(e.target.checked)} />
          <span>Stop all AI calls at the workspace's monthly limit{u.monthly_limit_cents != null && ` (${dollars(u.monthly_limit_cents)})`}</span>
        </label>
        {!wsStop && <p className="mt-1 text-[13px] text-muted-foreground">The workspace limit only warns: AI calls carry on past it.</p>}
        {canManage ? (
          <Button className="mt-3" disabled={busy || !limitChanged || badLimit} onClick={saveLimits}>{busy ? "Saving…" : "Save limits"}</Button>
        ) : (
          <p className="mt-3 text-[13px] text-muted-foreground">Only owners and admins can change limits.</p>
        )}
      </Card>

      {u.models.length > 0 && (
        <Card title="By model">
          <div className="overflow-x-auto">
            <table className="w-full text-[13px]">
              <thead className="text-left text-[11px] text-muted-foreground uppercase"><tr><th className="py-1 font-medium">Model</th><th className="font-medium">Calls</th><th className="font-medium">Tokens in / out</th><th className="font-medium">Cost</th></tr></thead>
              <tbody className="divide-y">
                {u.models.map((m) => (
                  <tr key={m.provider + m.model}>
                    <td className="py-1.5 pr-3 font-mono">{m.model || m.provider}<span className="text-muted-foreground"> · {m.provider}</span></td>
                    <td className="tabular-nums">{m.calls.toLocaleString()}</td>
                    <td className="tabular-nums">{tokens(m.input_tokens + m.cache_read_tokens + m.cache_write_tokens)} / {tokens(m.output_tokens)}</td>
                    <td className="tabular-nums">{dollars(m.cost_cents)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Card>
      )}

      {u.recent.length > 0 && (
        <Card title="Latest calls" description={`The ${u.recent.length} most recent this month. Only counts are kept, never what was asked or answered. Calls are kept for ${Math.round(u.retention_days / 30)} months.`}>
          <div className="overflow-x-auto">
            <table className="w-full text-[13px]">
              <thead className="text-left text-[11px] text-muted-foreground uppercase"><tr><th className="py-1 font-medium">When</th><th className="font-medium">Tool</th><th className="font-medium">Model</th><th className="font-medium">Tokens in / out</th><th className="font-medium">Cost</th></tr></thead>
              <tbody className="divide-y">
                {u.recent.map((c) => (
                  <tr key={c.id}>
                    <td className="py-1.5 pr-3 whitespace-nowrap text-muted-foreground">{new Date(c.occurred_at).toLocaleString()}</td>
                    <td className="pr-3">{c.tool_name}</td>
                    <td className="pr-3 font-mono">{c.model || c.provider}</td>
                    <td className="tabular-nums">{tokens(c.input_tokens + c.cache_read_tokens + c.cache_write_tokens)} / {tokens(c.output_tokens)}</td>
                    <td className="tabular-nums" title={c.cost_reported ? "As your provider reported it" : "From the model's price"}>{dollars(c.cost_cents)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Card>
      )}
      {u.spent_cents > u.total.cost_cents && (
        <p className="text-xs text-muted-foreground">{dollars(u.spent_cents - u.total.cost_cents)} of this month's spend was booked before Outflare logged AI calls by tool, so it counts toward the workspace's limit but isn't shown by tool.</p>
      )}
    </div>
  );
}
