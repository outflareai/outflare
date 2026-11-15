// The forecast (docs/revenue.md, "Forecast"): what the brand can expect
// to book in a quarter against its target. Won deals, renewals weighted
// by health and open deals weighted by stage, each line showing what it
// adds, so the total is arithmetic anyone can check.

import { useEffect, useState, useMemo } from "react";
import { teammateNames, useTeammates } from "@/components/teammate-picker";
import type { Schemas } from "@outflare/sdk";
import { bandText, bandTone } from "@/components/record-scores";
import { track } from "@/lib/analytics";
import { api, errorMessage } from "../api";
import { plural } from "../format";
import { Badge, Button, Card, Empty, ErrorText, Loading, PageHeader, Stat, Stats, Status, toast } from "../ui";
import type { Tab } from "../App";

type Forecast = Schemas["RevenueForecast"];
type Band = "high" | "medium" | "low";

function money(minor: number, currency: string): string {
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
const day = (iso: string) => new Date(`${iso}T00:00:00Z`).toLocaleDateString(undefined, { month: "short", day: "numeric", timeZone: "UTC" });

const parts = [
  { key: "won", label: "Won", color: "bg-green-500" },
  { key: "renewals", label: "Renewals", color: "bg-sky-400 dark:bg-sky-600" },
  { key: "expansion", label: "Expansion", color: "bg-emerald-300 dark:bg-emerald-700" },
  { key: "new_business", label: "New business", color: "bg-violet-400 dark:bg-violet-600" },
] as const;

const renewalBands = [
  { key: "high", label: "High health" },
  { key: "medium", label: "Medium health" },
  { key: "low", label: "Low health" },
  { key: "none", label: "No health score" },
];

export function RevenueForecast({ ws, brandId, canManage, go, onBack }: { ws: string; brandId: string; canManage: boolean; go: (tab: Tab, item?: string) => void; onBack: () => void }) {
  const list = quarters();
  const [quarter, setQuarter] = useState(list[0]);
  const [f, setF] = useState<Forecast | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [target, setTarget] = useState<string | null>(null); // the target being typed, in whole units
  const [weights, setWeights] = useState<{ renewal: Record<string, string>; stages: Record<string, string> } | null>(null);

  const team = useTeammates(ws);
  const names = useMemo(() => teammateNames(team.people), [team.people]);

  useEffect(() => {
    let live = true;
    setF(null);
    void api.GET("/v1/brands/{brand}/revenue/forecast", { params: { path: { brand: brandId }, query: { quarter } } }).then(({ data, error }) => {
      if (!live) return;
      if (error || !data) return setError(errorMessage(error));
      setError("");
      setF(data);
    });
    return () => {
      live = false;
    };
  }, [brandId, quarter]);

  async function save(body: { target_minor?: number | null; renewal_weights?: Record<string, number | null>; stage_weights?: Record<string, number | null> }, done: string) {
    if (busy) return;
    setBusy(true);
    try {
      const { data, error } = await api.PUT("/v1/brands/{brand}/revenue/forecast", { params: { path: { brand: brandId } }, body: { quarter, ...body } });
      if (error || !data) return toast(errorMessage(error), "error");
      if ("quarter" in data) {
        setF(data);
        setTarget(null);
        setWeights(null);
        toast(done);
      } else toast("Sent for approval.");
    } catch (e) {
      toast(errorMessage(e), "error");
    } finally {
      setBusy(false);
    }
  }

  function saveTarget() {
    const n = Number((target ?? "").replace(/[\s,]/g, ""));
    if (target?.trim() === "") return void save({ target_minor: null }, "Target removed.").then(() => track("ui.revenue_forecast_target_saved", { has_target: false }));
    if (!Number.isFinite(n) || n < 0) return toast("Type the target as a number, such as 250000.", "error");
    void save({ target_minor: Math.round(n * 100) }, "Target saved.").then(() => track("ui.revenue_forecast_target_saved", { has_target: true }));
  }

  function saveWeights() {
    if (!weights || !f) return;
    const read = (v: string) => (v.trim() === "" ? null : Math.round(Number(v)));
    const bad = (v: number | null) => v !== null && (!Number.isFinite(v) || v < 0 || v > 100);
    const renewal: Record<string, number | null> = {};
    const stages: Record<string, number | null> = {};
    for (const [k, v] of Object.entries(weights.renewal)) renewal[k] = read(v);
    for (const [k, v] of Object.entries(weights.stages)) stages[k] = read(v);
    if ([...Object.values(renewal), ...Object.values(stages)].some(bad)) return toast("A weight is a percent from 0 to 100. Leave it empty for the default.", "error");
    void save({ renewal_weights: renewal, stage_weights: stages }, "Weights saved.").then(() => track("ui.revenue_forecast_weights_saved", { stages: Object.keys(stages).length }));
  }

  const header = (
    <PageHeader
      title="Forecast"
      back={{ label: "Revenue", onClick: onBack }}
      description="What you can expect to book this quarter: what is already won, renewals weighted by each company's health, and open deals weighted by their stage."
      action={
        <div role="tablist" aria-label="Quarter" className="flex gap-1">
          {list.map((q) => (
            <Button
              key={q}
              role="tab"
              aria-selected={quarter === q}
              variant="ghost"
              className={quarter === q ? "bg-muted" : "border-transparent text-muted-foreground"}
              onClick={() => {
                setQuarter(q);
                track("ui.revenue_forecast_quarter_changed", { ahead: list.indexOf(q) });
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
  if (!f) return <div className="space-y-6">{header}<Loading /></div>;

  const ccy = f.currency;
  const empty = f.best_case_minor === 0 && f.skipped.deals + f.skipped.renewals === 0;
  const scale = Math.max(f.best_case_minor, f.target_minor ?? 0, 1);
  const stageLabel = (key: string) => f.weights.stages.find((s) => s.key === key)?.label ?? key;
  const editing = weights !== null;

  return (
    <div className="space-y-6">
      {header}

      <Stats cols={4}>
        <Stat
          label="Forecast"
          value={money(f.forecast_minor, ccy)}
          sub={f.attainment != null ? `${Math.round(f.attainment * 100)}% of target` : `${plural(f.days_left, "day")} left`}
          tone={f.attainment == null ? undefined : f.attainment >= 1 ? "green" : f.attainment >= 0.8 ? "amber" : "red"}
        />
        <Stat
          label="Target"
          value={f.target_minor != null ? money(f.target_minor, ccy) : <span className="text-muted-foreground">Not set</span>}
          sub={
            canManage && target === null ? (
              <button className="underline underline-offset-2" onClick={() => setTarget(f.target_minor != null ? String(f.target_minor / 100) : "")}>
                {f.target_minor != null ? "Change" : "Set a target"}
              </button>
            ) : undefined
          }
        />
        <Stat
          label={f.gap_minor != null && f.gap_minor <= 0 ? "Ahead by" : "Gap to target"}
          value={f.gap_minor != null ? money(Math.abs(f.gap_minor), ccy) : <span className="text-muted-foreground">—</span>}
          sub={f.gap_minor != null && f.gap_minor > 0 ? `${plural(f.days_left, "day")} left` : undefined}
        />
        <Stat label="Best case" value={money(f.best_case_minor, ccy)} sub="Every renewal and open deal in full" />
      </Stats>

      {target !== null && (
        <form
          className="flex flex-wrap items-end gap-2 rounded-lg border p-3"
          onSubmit={(e) => {
            e.preventDefault();
            saveTarget();
          }}
        >
          <label className="text-sm">
            <span className="mb-1 block text-xs text-muted-foreground">Target for {quarterLabel(quarter)}, in {ccy}</span>
            <input className="field w-48" inputMode="decimal" autoFocus value={target} placeholder="250000" onChange={(e) => setTarget(e.target.value)} />
          </label>
          <Button variant="primary" type="submit" disabled={busy}>Save target</Button>
          <Button variant="ghost" type="button" onClick={() => setTarget(null)}>Cancel</Button>
          <span className="text-xs text-muted-foreground">Leave it empty to remove the target.</span>
        </form>
      )}

      {empty ? (
        <Empty title="Nothing to forecast yet">
          <p>The forecast fills in by itself from two things:</p>
          <ul className="mx-auto mt-3 max-w-md list-disc space-y-1.5 pl-5 text-left">
            <li>
              <button className="underline underline-offset-4" onClick={() => go("crm")}>Open deals</button> with an amount, and a close date when you know it.
            </li>
            <li>
              A <button className="underline underline-offset-4" onClick={() => go("playbooks", "flag-renewals-and-quiet-accounts")}>"Renews on" date</button> on companies or agreements.
            </li>
          </ul>
        </Empty>
      ) : (
        <Card title="How it adds up" description={`Won so far, plus what is expected by ${day(f.ends_on)}. The line marks the target.`}>
          <div className="relative h-4 overflow-hidden rounded-full bg-muted" role="img" aria-label={`Forecast ${money(f.forecast_minor, ccy)}${f.target_minor != null ? ` against a target of ${money(f.target_minor, ccy)}` : ""}`}>
            <div className="flex h-full">
              {parts.map((p) => {
                const v = f[p.key].expected_minor;
                return v > 0 ? <div key={p.key} className={p.color} style={{ width: `${(v / scale) * 100}%` }} title={`${p.label}: ${money(v, ccy)}`} /> : null;
              })}
            </div>
            {f.target_minor != null && f.target_minor > 0 && (
              <div className="absolute inset-y-0 w-0.5 bg-foreground" style={{ left: `min(calc(${(f.target_minor / scale) * 100}% - 1px), calc(100% - 2px))` }} aria-hidden="true" />
            )}
          </div>
          <dl className="mt-4 grid grid-cols-2 gap-x-6 gap-y-3 sm:grid-cols-4">
            {parts.map((p) => {
              const s = f[p.key];
              return (
                <div key={p.key} className="min-w-0">
                  <dt className="flex items-center gap-1.5 text-xs text-muted-foreground">
                    <span className={`size-2 shrink-0 rounded-full ${p.color}`} aria-hidden="true" />
                    {p.label}
                  </dt>
                  <dd className="mt-0.5 text-sm font-medium tabular-nums">{money(s.expected_minor, ccy)}</dd>
                  <dd className="text-xs text-muted-foreground">
                    {p.key === "won" ? plural(s.count, "deal") : s.count === 0 ? "None" : `of ${money(s.total_minor, ccy)} in ${plural(s.count, p.key === "renewals" ? "renewal" : "deal")}`}
                  </dd>
                </div>
              );
            })}
          </dl>
          {f.collected_minor > 0 && (
            <p className="mt-3 text-xs text-muted-foreground">For comparison, you collected {money(f.collected_minor, ccy)} in payments so far this quarter.</p>
          )}
        </Card>
      )}

      {f.owners.some((o) => o.owner_id) && (
        <Card title="By owner" description="Who the forecast sits with: what each teammate has won, plus what is expected from the renewals and open deals they own.">
          <ul className="divide-y">
            {f.owners.map((o) => {
              const max = Math.max(1, ...f.owners.map((x) => x.best_case_minor));
              return (
                <li key={o.owner_id ?? "none"} className="flex flex-wrap items-center gap-x-4 gap-y-1 py-2.5 text-sm">
                  <span className="min-w-full flex-1 sm:min-w-0">
                    <span className={`block truncate ${o.owner_id ? "font-medium" : "text-muted-foreground"}`}>{o.owner_id ? (names[o.owner_id] ?? "Someone who left") : "No owner"}</span>
                    <span className="mt-1 flex h-1.5 overflow-hidden rounded-full bg-muted" role="img" aria-label={`Won ${money(o.won_minor, ccy)}, expected ${money(o.renewals_expected_minor + o.deals_expected_minor, ccy)}`}>
                      <span className="h-full bg-green-600" style={{ width: `${(o.won_minor / max) * 100}%` }} />
                      <span className="h-full bg-foreground/60" style={{ width: `${((o.renewals_expected_minor + o.deals_expected_minor) / max) * 100}%` }} />
                    </span>
                  </span>
                  <span className="shrink-0 text-xs sm:w-56 sm:text-right text-xs tabular-nums text-muted-foreground">
                    {money(o.won_minor, ccy)} won · {money(o.renewals_expected_minor + o.deals_expected_minor, ccy)} expected from {o.open} open
                  </span>
                  <span className="shrink-0 sm:w-32 sm:text-right text-xs tabular-nums text-muted-foreground" title="Every renewal and open deal in full">best {money(o.best_case_minor, ccy)}</span>
                  <span className="ml-auto w-24 shrink-0 text-right font-medium tabular-nums sm:ml-0">{money(o.forecast_minor, ccy)}</span>
                </li>
              );
            })}
          </ul>
          <p className="mt-3 text-xs text-muted-foreground">A deal or renewal with no owner of its own counts for the owner of the company it is with.</p>
        </Card>
      )}

      {f.renewal_lines.length > 0 && (
        <Card title="Renewals" description="Each renewal counts for a share of its value that depends on the company's health.">
          <ul className="divide-y">
            {f.renewal_lines.map((x) => (
              <li key={x.record_id} className="flex flex-wrap items-center gap-x-4 gap-y-1 py-2.5 text-sm">
                <span className={`w-24 shrink-0 tabular-nums ${x.late ? "font-medium text-red-700 dark:text-red-400" : "text-muted-foreground"}`} title={x.renews_on}>
                  {x.late ? `Late, ${day(x.renews_on)}` : day(x.renews_on)}
                </span>
                <span className="min-w-[55%] flex-1 sm:min-w-0">
                  <button className="text-left font-medium underline-offset-2 hover:underline" onClick={() => go("people", x.record_id)}>{x.name}</button>
                  {x.company_id && x.company_name && (
                    <>
                      {" "}
                      <span className="text-muted-foreground">for</span>{" "}
                      <button className="underline-offset-2 hover:underline" onClick={() => go("people", x.company_id!)}>{x.company_name}</button>
                    </>
                  )}
                </span>
                {x.health_band ? <Status tone={bandTone[x.health_band as Band]}>{bandText[x.health_band as Band]} health</Status> : <span className="text-xs text-muted-foreground">No score</span>}
                <span className="shrink-0 sm:w-44 sm:text-right text-xs tabular-nums text-muted-foreground">{money(x.value_minor, ccy)} × {x.weight}%</span>
                <span className="ml-auto w-24 shrink-0 text-right font-medium tabular-nums sm:ml-0">{money(x.expected_minor, ccy)}</span>
              </li>
            ))}
          </ul>
        </Card>
      )}

      {f.deal_lines.length > 0 && (
        <Card title="Open deals" description="Each deal counts for a share of its amount that depends on its stage. A deal with someone who already pays is expansion; a deal opened for a renewal counts as that renewal, by the company's health.">
          <ul className="divide-y">
            {f.deal_lines.map((x) => (
              <li key={x.deal_id} className="flex flex-wrap items-center gap-x-4 gap-y-1 py-2.5 text-sm">
                <span className="w-24 shrink-0 tabular-nums text-muted-foreground">{x.close_date ? day(x.close_date) : "No date"}</span>
                <span className="min-w-[55%] flex-1 sm:min-w-0">
                  <button className="text-left font-medium underline-offset-2 hover:underline" onClick={() => go("people", x.deal_id)}>{x.name || "Unnamed deal"}</button>
                  {x.record_id && x.record_name && (
                    <>
                      {" "}
                      <span className="text-muted-foreground">with</span>{" "}
                      <button className="underline-offset-2 hover:underline" onClick={() => go("people", x.record_id!)}>{x.record_name}</button>
                    </>
                  )}
                </span>
                <Badge tone={x.kind === "expansion" ? "green" : x.kind === "renewal" ? "amber" : "blue"}>{x.kind === "expansion" ? "Expansion" : x.kind === "renewal" ? "Renewal" : "New"}</Badge>
                <span className="shrink-0 truncate sm:w-28 text-xs text-muted-foreground">{stageLabel(x.stage)}</span>
                <span className="shrink-0 sm:w-44 sm:text-right text-xs tabular-nums text-muted-foreground" title={x.weight_from === "score" ? "Its win likelihood score. Open the deal to see why." : x.weight_from === "health" ? "A renewal counts by the company's health, whatever stage its deal is at." : "Its stage's weight."}>{money(x.amount_minor, ccy)} × {x.weight}%{x.weight_from === "score" && " likely"}</span>
                <span className="ml-auto w-24 shrink-0 text-right font-medium tabular-nums sm:ml-0">{money(x.expected_minor, ccy)}</span>
              </li>
            ))}
          </ul>
        </Card>
      )}

      {f.skipped.deals + f.skipped.renewals > 0 && (
        <p className="text-xs text-muted-foreground">
          Left out because they have no amount:{" "}
          {[f.skipped.deals > 0 && plural(f.skipped.deals, "open deal"), f.skipped.renewals > 0 && plural(f.skipped.renewals, "renewal")].filter(Boolean).join(" and ")}. Add an amount to count them.
        </p>
      )}

      <Card
        title="How it's weighted"
        description="The share of each renewal and deal that counts. Change them to match how your renewals and deals really turn out."
        action={
          canManage &&
          (editing ? (
            <div className="flex gap-2">
              <Button variant="ghost" onClick={() => setWeights(null)}>Cancel</Button>
              <Button variant="primary" disabled={busy} onClick={saveWeights}>Save weights</Button>
            </div>
          ) : (
            <Button
              variant="ghost"
              onClick={() =>
                setWeights({
                  renewal: Object.fromEntries(renewalBands.map((b) => [b.key, String(f.weights.renewal[b.key] ?? "")])),
                  stages: Object.fromEntries(f.weights.stages.map((s) => [s.key, s.custom ? String(s.weight) : ""])),
                })
              }
            >
              Change
            </Button>
          ))
        }
      >
        <div className="grid gap-6 md:grid-cols-2">
          <div>
            <h4 className="mb-2 text-xs font-semibold text-muted-foreground">RENEWALS, BY HEALTH</h4>
            <ul className="divide-y text-sm">
              {renewalBands.map((b) => (
                <li key={b.key} className="flex items-center justify-between gap-3 py-1.5">
                  <label htmlFor={`rw-${b.key}`}>{b.label}</label>
                  {editing ? (
                    <span className="flex items-center gap-1">
                      <input id={`rw-${b.key}`} className="field w-20 text-right" inputMode="numeric" value={weights.renewal[b.key] ?? ""} onChange={(e) => setWeights({ ...weights, renewal: { ...weights.renewal, [b.key]: e.target.value } })} />%
                    </span>
                  ) : (
                    <span className="tabular-nums">{f.weights.renewal[b.key]}%</span>
                  )}
                </li>
              ))}
            </ul>
          </div>
          <div>
            <h4 className="mb-2 text-xs font-semibold text-muted-foreground">OPEN DEALS, BY STAGE</h4>
            {f.deal_lines.some((d) => d.weight_from === "score") && (
              <p className="mb-2 text-xs text-muted-foreground">Deals with a win likelihood score count at that score instead: the stage is one of its factors. Change it under Scores.</p>
            )}
            {f.weights.stages.length === 0 ? (
              <p className="text-sm text-muted-foreground">This brand has no deal stages yet.</p>
            ) : (
              <ul className="divide-y text-sm">
                {f.weights.stages.map((s) => (
                  <li key={s.key} className="flex items-center justify-between gap-3 py-1.5">
                    <label htmlFor={`sw-${s.key}`}>{s.label}</label>
                    {editing ? (
                      <span className="flex items-center gap-1">
                        <input id={`sw-${s.key}`} className="field w-20 text-right" inputMode="numeric" placeholder={String(s.custom ? "" : s.weight)} value={weights.stages[s.key] ?? ""} onChange={(e) => setWeights({ ...weights, stages: { ...weights.stages, [s.key]: e.target.value } })} />%
                      </span>
                    ) : (
                      <span className="tabular-nums">
                        {s.weight}%{!s.custom && <span className="ml-1.5 text-xs text-muted-foreground">default</span>}
                      </span>
                    )}
                  </li>
                ))}
              </ul>
            )}
          </div>
        </div>
        {editing && <p className="mt-3 text-xs text-muted-foreground">Leave a box empty to go back to its default.</p>}
      </Card>
    </div>
  );
}
