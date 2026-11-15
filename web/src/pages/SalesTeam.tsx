// The sales team (docs/sales-team.md): everyone who carries a quota or
// owns deals in a brand, with what each has won and is forecast to win
// against their quota this quarter, laid out by who reports to whom so a
// manager's row adds up their team.

import { useCallback, useEffect, useState } from "react";
import type { RevenueTeam, RevenueTeamMember, RevenueTeamSum } from "@outflare/sdk";
import { track } from "@/lib/analytics";
import { api, errorMessage } from "../api";
import { plural } from "../format";
import { Badge, Button, Empty, ErrorText, field, Loading, PageHeader, Stat, Stats, toast } from "../ui";
import type { Tab } from "../App";

export function money(minor: number, currency: string): string {
  const digits = ["JPY", "KRW", "ISK", "HUF"].includes(currency) ? 0 : 2;
  try {
    return new Intl.NumberFormat(undefined, { style: "currency", currency, maximumFractionDigits: 0 }).format(minor / 10 ** digits);
  } catch {
    return `${Math.round(minor / 10 ** digits)} ${currency}`;
  }
}

// This quarter and the next three, such as 2026-Q4.
function quarters(): string[] {
  const now = new Date();
  let year = now.getUTCFullYear();
  let q = Math.floor(now.getUTCMonth() / 3) + 1;
  const out: string[] = [];
  for (let i = 0; i < 4; i++) {
    out.push(`${year}-Q${q}`);
    if (++q > 4) {
      q = 1;
      year++;
    }
  }
  return out;
}
const quarterLabel = (q: string) => `${q.slice(5)} ${q.slice(0, 4)}`;

export const roleLabel: Record<string, string> = { sdr: "SDR", ae: "Account executive", manager: "Sales manager" };

type Person = { id: string; name: string; title: string; status: string };
type Row = { m: RevenueTeamMember; depth: number };

// The team as a tree: each person under their manager, top of the tree first.
function tree(members: RevenueTeamMember[]): Row[] {
  const ids = new Set(members.map((m) => m.staff_id));
  const under = new Map<string, RevenueTeamMember[]>();
  const top: RevenueTeamMember[] = [];
  for (const m of members) {
    if (m.manager_id && ids.has(m.manager_id) && m.manager_id !== m.staff_id) under.set(m.manager_id, [...(under.get(m.manager_id) ?? []), m]);
    else top.push(m);
  }
  const out: Row[] = [];
  const seen = new Set<string>();
  const walk = (m: RevenueTeamMember, depth: number) => {
    if (seen.has(m.staff_id)) return;
    seen.add(m.staff_id);
    out.push({ m, depth });
    for (const c of under.get(m.staff_id) ?? []) walk(c, depth + 1);
  };
  for (const m of top) walk(m, 0);
  for (const m of members) walk(m, 0); // anyone a loop left out
  return out;
}

// within says a number of hours the way people do.
function within(hours: number): string {
  if (hours < 1) return "an hour";
  if (hours < 48) return plural(Math.round(hours), "hour");
  return plural(Math.round(hours / 24), "day");
}

function Bar({ sum, currency }: { sum: RevenueTeamSum; currency: string }) {
  if (!sum.quota_minor) return <span className="text-[12px] text-muted-foreground">No quota</span>;
  const pct = sum.attainment ?? 0;
  const forecast = Math.min(100, (sum.forecast_minor / sum.quota_minor) * 100);
  const tone = pct >= 100 ? "bg-green-500" : pct >= 60 ? "bg-sky-500" : "bg-amber-500";
  return (
    <div className="min-w-[9rem]" title={`${money(sum.won_minor, currency)} won of ${money(sum.quota_minor, currency)}; forecast ${money(sum.forecast_minor, currency)}`}>
      <div className="relative h-2 overflow-hidden rounded-full bg-muted">
        <div className="absolute inset-y-0 left-0 rounded-full bg-sky-200 dark:bg-sky-900" style={{ width: `${forecast}%` }} />
        <div className={`absolute inset-y-0 left-0 rounded-full ${tone}`} style={{ width: `${Math.min(100, pct)}%` }} />
      </div>
      <div className="mt-1 text-[12px] tabular-nums text-muted-foreground">
        <span className="font-medium text-foreground">{Math.round(pct)}%</span> of {money(sum.quota_minor, currency)}
      </div>
    </div>
  );
}

export function SalesTeam({ ws, brandId, go, onBack }: { ws: string; brandId: string; go: (tab: Tab, item?: string) => void; onBack: () => void }) {
  const list = quarters();
  const [quarter, setQuarter] = useState(list[0]);
  const [t, setT] = useState<RevenueTeam | null>(null);
  const [error, setError] = useState("");
  const [people, setPeople] = useState<Person[]>([]);
  // Quotas being typed, in whole units, by staff id. null when not editing.
  const [draft, setDraft] = useState<Record<string, string> | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(() => {
    return api.GET("/v1/brands/{brand}/revenue/team", { params: { path: { brand: brandId }, query: { quarter } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setError("");
      setT(data);
    });
  }, [brandId, quarter]);
  useEffect(() => {
    setT(null);
    setDraft(null);
    void load();
  }, [load]);
  useEffect(() => {
    void api.GET("/v1/workspaces/{ws}/staff", { params: { path: { ws } } }).then(({ data }) => setPeople(((data?.data ?? []) as Person[]).filter((p) => p.status !== "left")));
  }, [ws]);

  const header = (
    <PageHeader
      title="Sales team"
      back={{ label: "Revenue", onClick: onBack }}
      description="Who is asked to win what this quarter, and how far along they are. A manager's row adds up everyone who reports to them."
      action={
        <div role="tablist" aria-label="Quarter" className="flex flex-wrap gap-1">
          {list.map((q) => (
            <Button
              key={q}
              role="tab"
              aria-selected={quarter === q}
              variant="ghost"
              className={quarter === q ? "bg-muted" : "border-transparent text-muted-foreground"}
              onClick={() => {
                setQuarter(q);
                track("ui.sales_team_quarter_changed", { ahead: list.indexOf(q) });
              }}
            >
              {quarterLabel(q)}
            </Button>
          ))}
        </div>
      }
    />
  );
  if (error) return <div className="space-y-6">{header}<ErrorText>{error}</ErrorText></div>;
  if (!t) return <div className="space-y-6">{header}<Loading /></div>;

  const ccy = t.currency;
  const rows = tree(t.members);
  const onTeam = new Set(t.members.map((m) => m.staff_id));
  const editing = draft !== null;
  const extra = editing ? Object.keys(draft).filter((id) => !onTeam.has(id)) : [];
  const addable = people.filter((p) => !onTeam.has(p.id) && !extra.includes(p.id));

  function startEditing(add?: string) {
    const d: Record<string, string> = {};
    for (const m of t!.members) d[m.staff_id] = m.has_quota ? String(m.quota_minor / 100) : "";
    if (add) d[add] = "";
    setDraft(d);
  }

  async function save() {
    if (!draft || busy) return;
    const quotas: { staff_id: string; target_minor: number | null }[] = [];
    for (const [id, v] of Object.entries(draft)) {
      const before = t!.members.find((m) => m.staff_id === id);
      const was = before?.has_quota ? String(before.quota_minor / 100) : "";
      if (v.trim() === was) continue;
      if (v.trim() === "") {
        quotas.push({ staff_id: id, target_minor: null });
        continue;
      }
      const n = Number(v.replace(/[\s,]/g, ""));
      if (!Number.isFinite(n) || n < 0) return toast("Type each quota as a number, such as 150000. Leave it empty for no quota.", "error");
      quotas.push({ staff_id: id, target_minor: Math.round(n * 100) });
    }
    if (quotas.length === 0) return setDraft(null);
    setBusy(true);
    try {
      const { data, error } = await api.PUT("/v1/brands/{brand}/revenue/team/quotas", { params: { path: { brand: brandId } }, body: { quarter, quotas } });
      if (error || !data) return toast(errorMessage(error), "error");
      if ("updated" in data) {
        toast(`${plural(quotas.length, "quota")} saved.`);
        track("ui.sales_team_quotas_saved", { people: quotas.length });
        setDraft(null);
        await load();
      } else toast("Sent for approval.");
    } catch (e) {
      toast(errorMessage(e), "error");
    } finally {
      setBusy(false);
    }
  }

  const quotaInput = (id: string, name: string) => (
    <input
      aria-label={`Quota for ${name}`}
      className={`${field} w-32 text-right tabular-nums`}
      inputMode="decimal"
      placeholder="No quota"
      value={draft?.[id] ?? ""}
      onChange={(e) => setDraft({ ...draft!, [id]: e.target.value })}
    />
  );

  return (
    <div className="space-y-6">
      {header}

      {t.total && (
        <Stats cols={4}>
          <Stat
            label="Won"
            value={money(t.total.won_minor, ccy)}
            sub={t.total.attainment != null ? `${Math.round(t.total.attainment)}% of the team's quota` : `${plural(t.days_left, "day")} left`}
            tone={t.total.attainment == null ? undefined : t.total.attainment >= 100 ? "green" : undefined}
          />
          <Stat label="Team quota" value={t.total.quota_minor ? money(t.total.quota_minor, ccy) : <span className="text-muted-foreground">Not set</span>} sub={`${plural(t.days_left, "day")} left in ${quarterLabel(t.quarter)}`} />
          <Stat label="Forecast" value={money(t.total.forecast_minor, ccy)} sub="Won, plus renewals and open deals by their odds" />
          <Stat
            label="With no owner on the team"
            value={money(t.unassigned?.forecast_minor ?? 0, ccy)}
            sub={
              (t.unassigned?.forecast_minor ?? 0) > 0 ? (
                <button className="underline underline-offset-2" onClick={() => go("people", "routing")}>Give them owners with routing</button>
              ) : (
                "Everything in the forecast has an owner"
              )
            }
            tone={(t.unassigned?.forecast_minor ?? 0) > 0 ? "amber" : undefined}
          />
        </Stats>
      )}

      {rows.length === 0 && !editing ? (
        <Empty
          title={t.can_manage ? "Nobody is on the sales team yet" : "You are not on the sales team yet"}
          action={
            t.can_manage &&
            (people.length > 0 ? <Button onClick={() => startEditing()}>Set quotas</Button> : <Button onClick={() => go("team")}>Add your people</Button>)
          }
        >
          {t.can_manage
            ? people.length > 0
              ? "Give someone a quota for the quarter and they show up here, with what they have won and are forecast to win. People who own deals appear by themselves."
              : "Start in Settings, Team: add each person, their job title, who they report to and their sign-in. Then come back and give them a quota."
            : "Once you have a quota or own a deal, your numbers show here."}
        </Empty>
      ) : (
        <section className="rounded-lg border">
          <div className="flex flex-wrap items-center justify-between gap-2 border-b px-4 py-3">
            <div>
              <h2 className="text-sm font-medium">{quarterLabel(t.quarter)}, person by person</h2>
              <p className="text-[12px] text-muted-foreground">The bar is what is won; the lighter part behind it is the forecast.</p>
            </div>
            {t.can_manage &&
              (editing ? (
                <div className="flex gap-2">
                  <Button variant="ghost" onClick={() => setDraft(null)}>Cancel</Button>
                  <Button disabled={busy} onClick={() => void save()}>Save quotas</Button>
                </div>
              ) : (
                <div className="flex gap-2">
                  <Button variant="ghost" onClick={() => go("team")}>Edit the team</Button>
                  <Button onClick={() => startEditing()}>Set quotas</Button>
                </div>
              ))}
          </div>
          <ul className="divide-y">
            {rows.map(({ m, depth }) => (
              <li key={m.staff_id} className="px-4 py-3">
                <div className="flex flex-wrap items-center gap-x-6 gap-y-2" style={{ paddingLeft: `${Math.min(depth, 4) * 1.25}rem` }}>
                  <div className="min-w-[10rem] flex-1">
                    <div className="flex flex-wrap items-center gap-2 text-sm font-medium">
                      {m.name}
                      {m.role && <Badge>{roleLabel[m.role] ?? m.role}</Badge>}
                      {!m.user_id && <Badge title="They can own records and earn commission. Without a sign-in they don't get alerts or see their own numbers.">No sign-in</Badge>}
                    </div>
                    <div className="text-[12px] text-muted-foreground">{m.title || "No job title"}</div>
                  </div>
                  {editing ? (
                    <label className="flex items-center gap-2 text-[12px] text-muted-foreground">
                      Quota ({ccy}) {quotaInput(m.staff_id, m.name)}
                    </label>
                  ) : (
                    <>
                      <Bar sum={m} currency={ccy} />
                      <dl className="grid min-w-[15rem] flex-1 grid-cols-3 gap-x-4 text-[12px] sm:flex-none">
                        <div><dt className="text-muted-foreground">Won</dt><dd className="text-sm tabular-nums">{money(m.won_minor, ccy)}</dd></div>
                        <div><dt className="text-muted-foreground">Forecast</dt><dd className="text-sm tabular-nums">{money(m.forecast_minor, ccy)}</dd></div>
                        <div><dt className="text-muted-foreground">Open</dt><dd className="text-sm tabular-nums">{m.open}</dd></div>
                      </dl>
                    </>
                  )}
                </div>
                {!editing && (m.signals.open > 0 || m.signals.handled > 0) && (
                  <p className="mt-1.5 text-[12px] text-muted-foreground" style={{ paddingLeft: `${Math.min(depth, 4) * 1.25}rem` }}>
                    Signals: {m.signals.handled} handled this quarter
                    {m.signals.won > 0 ? ` (${m.signals.won} won)` : ""}
                    {m.signals.median_hours != null ? `, usually within ${within(m.signals.median_hours)}` : ""}
                    {" · "}
                    {m.signals.open} waiting
                    {m.signals.late > 0 && <span className="text-amber-700 dark:text-amber-400">, {m.signals.late} late</span>}
                  </p>
                )}
                {!editing && m.team && (
                  <div className="mt-2 flex flex-wrap items-center gap-x-6 gap-y-2 rounded-md bg-muted/50 py-2 pl-3" style={{ marginLeft: `${Math.min(depth, 4) * 1.25}rem` }}>
                    <div className="min-w-[10rem] flex-1 text-[12px] text-muted-foreground">
                      {m.name.split(" ")[0]} and the {plural(m.reports, "person", "people")} below, together
                    </div>
                    <Bar sum={m.team} currency={ccy} />
                    <dl className="grid min-w-[15rem] flex-1 grid-cols-3 gap-x-4 text-[12px] sm:flex-none">
                      <div><dt className="text-muted-foreground">Won</dt><dd className="text-sm tabular-nums">{money(m.team.won_minor, ccy)}</dd></div>
                      <div><dt className="text-muted-foreground">Forecast</dt><dd className="text-sm tabular-nums">{money(m.team.forecast_minor, ccy)}</dd></div>
                      <div><dt className="text-muted-foreground">Open</dt><dd className="text-sm tabular-nums">{m.team.open}</dd></div>
                    </dl>
                  </div>
                )}
              </li>
            ))}
            {extra.map((id) => {
              const p = people.find((x) => x.id === id);
              if (!p) return null;
              return (
                <li key={id} className="flex flex-wrap items-center gap-x-6 gap-y-2 px-4 py-3">
                  <div className="min-w-[10rem] flex-1">
                    <div className="text-sm font-medium">{p.name}</div>
                    <div className="text-[12px] text-muted-foreground">{p.title || "No job title"}</div>
                  </div>
                  <label className="flex items-center gap-2 text-[12px] text-muted-foreground">
                    Quota ({ccy}) {quotaInput(id, p.name)}
                  </label>
                </li>
              );
            })}
          </ul>
          {editing && (
            <div className="flex flex-wrap items-center gap-3 border-t px-4 py-3 text-[13px]">
              {addable.length > 0 ? (
                <select aria-label="Add a person" className={field} value="" onChange={(e) => e.target.value && setDraft({ ...draft!, [e.target.value]: "" })}>
                  <option value="">Add a person…</option>
                  {addable.map((p) => <option key={p.id} value={p.id}>{p.name}{p.title ? `, ${p.title}` : ""}</option>)}
                </select>
              ) : (
                <span className="text-muted-foreground">Everyone in your team directory is listed.</span>
              )}
              <span className="text-[12px] text-muted-foreground">
                Someone missing? <button className="underline underline-offset-2" onClick={() => go("team")}>Add them in Settings, Team</button>. Empty a quota to take it away.
              </span>
            </div>
          )}
        </section>
      )}

      <p className="text-[12px] text-muted-foreground">
        A deal counts for its Owner field; when that is empty, for the owner of its person, then of its company. Renewals count for the owner of the company.
        {!t.can_manage && " You see your own numbers and those of the people who report to you."}
      </p>
    </div>
  );
}
