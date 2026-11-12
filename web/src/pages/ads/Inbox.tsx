// Needs you: the decision inbox. Each finding is a card with the question
// first, one line of evidence, the expected change in cost per new paying
// customer, and what to do about it. Every change waits here for a
// person; savings applied on their own (only where Outflare Cloud allows
// it) are "Done" cards with Undo; alerts are acknowledged; while shadow
// mode is on, what the rules would have done sits in a collapsed section
// with the switch that turns them on (a person's decision).

import { useCallback, useEffect, useState, type ReactNode } from "react";
import { api, errorMessage } from "../../api";
import { track } from "@/lib/analytics";
import { Badge, Button, Card, confirmAction, ErrorText, Loading, toast } from "../../ui";
import { ago } from "../../format";
import { money, n, platformName, scopeQuery, type AdScope } from "../Ads";
import { testKinds } from "../AdTests";
import { AdDrafts } from "../AdDrafts";
import { Recommendations } from "../AdSuggestions";
import { acknowledgeFinding, isAlert, isDone, isMeasuring, isOpen, isShadow, leverWord, revertFinding, spendsMore, statusOf, unitWord, type AdEconomics, type Finding } from "./api";

const num = (v: unknown): number | null => (typeof v === "number" && Number.isFinite(v) ? v : null);

// What the change sets, in the finding's own words: a budget before and
// after, a keyword, a target.
export function changeLine(f: Finding): ReactNode {
  if (String(f.kind).startsWith("alert.")) return null;
  const before = num(f.evidence.daily_budget_micros);
  const after = num(f.proposed.daily_budget_micros);
  const keyword = typeof f.proposed.keyword === "string" ? f.proposed.keyword : null;
  const target = num(f.proposed.target_cpa_micros) ?? num(f.proposed.target_cost_per_payer_micros);
  return (
    <>
      {leverWord(f.kind)}
      {keyword && <> <span className="font-mono">[{keyword}]</span></>}
      {after !== null && <> {before !== null && <>{money(before, f.currency)} → </>}{money(after, f.currency)} a day</>}
      {target !== null && <> to {money(target, f.currency)}</>}
    </>
  );
}

// One line of evidence from the numbers behind the finding, in the unit
// its rule counted in.
export function evidenceLine(f: Finding): string {
  const e = f.evidence;
  const parts: string[] = [];
  const days = num(e.days);
  const spend = num(e.spend_micros);
  const payers = num(e.new_payers);
  const signups = num(e.signups);
  const conversions = num(e.conversions);
  const count = f.unit === "payers" ? payers : f.unit === "signups" ? signups : (conversions ?? payers ?? signups);
  const unit = f.unit ?? (payers !== null ? "payers" : signups !== null ? "signups" : "platform_conversions");
  const cost = num(e.cost_per_new_payer_micros) ?? num(e.cost_per_signup_micros) ?? num(e.cost_per_conversion_micros);
  const target = num(e.target_cost_per_payer_micros) ?? num(e.target_cost_per_signup_micros) ?? num(e.target_cost_per_conversion_micros);
  const budgetDays = num(e.days_budget_spent);
  if (days !== null) parts.push(`Last ${days} days`);
  if (spend !== null) parts.push(`${money(spend, f.currency)} spent`);
  if (count !== null) parts.push(`${n(count)} ${unitWord(unit, count)}${cost !== null ? ` at ${money(cost, f.currency)} each` : ""}`);
  if (target !== null) parts.push(`target ${money(target, f.currency)}`);
  if (budgetDays !== null && budgetDays > 0) parts.push(`budget ran out on ${n(budgetDays)} ${budgetDays === 1 ? "day" : "days"}`);
  return parts.join(", ");
}

// The expected change in cost per new paying customer (negative is better).
export function impactLine(f: Finding): string | null {
  const v = f.impact_micros;
  if (v === null || v === undefined) return null;
  if (v === 0) return "Expected: cost per new customer unchanged";
  return `Expected: about ${money(Math.abs(v), f.currency)} ${v < 0 ? "less" : "more"} per new customer`;
}

function Where({ f }: { f: Finding }) {
  return <span className="text-[12px] text-muted-foreground">{f.campaign_name} · {platformName(f.connector)} · {f.account_name}</span>;
}

function FindingCard({ f, badge, actions, muted = false }: { f: Finding; badge?: ReactNode; actions?: ReactNode; muted?: boolean }) {
  const impact = impactLine(f);
  const evidence = evidenceLine(f);
  return (
    <li className={`space-y-2 rounded-lg border bg-card p-4 shadow-xs ${muted ? "opacity-80" : ""}`}>
      <div className="flex flex-wrap items-start justify-between gap-2">
        <h3 className="text-[14px] font-medium">{f.reason || leverWord(f.kind)}</h3>
        {badge}
      </div>
      {changeLine(f) && <div className="text-[13px]">{changeLine(f)}</div>}
      <Where f={f} />
      {evidence && <p className="text-[13px] text-muted-foreground">{evidence}</p>}
      {impact && <p className="text-[13px]">{impact}</p>}
      {actions && <div className="flex flex-wrap gap-2 pt-1">{actions}</div>}
    </li>
  );
}

type Props = { ws: string; brand: string; scope: AdScope; canManage: boolean; economics: AdEconomics | null; reloadBrand: () => void };

export function Inbox({ ws, brand, scope, canManage, economics, reloadBrand }: Props) {
  const [rows, setRows] = useState<Finding[] | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const [shadowError, setShadowError] = useState("");

  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/ads/findings", { params: { path: { brand }, query: { ...scopeQuery(scope), limit: 300 } } });
    setError(error ? errorMessage(error) : "");
    if (data) setRows(data.data as Finding[]);
    else if (!rows) setRows([]);
  }, [brand, scope.network, scope.account]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    load();
  }, [load]);

  // A decided card leaves the list at once and comes back if the call
  // fails; the server's answer replaces everything at the next load.
  async function act(f: Finding, call: () => Promise<{ error?: unknown }>, done: string) {
    const before = rows;
    setBusy(f.id);
    setRows((r) => (r ?? []).filter((x) => x.id !== f.id));
    const { error } = await call();
    setBusy(null);
    if (error) {
      setRows(before);
      return toast(errorMessage(error), "error");
    }
    toast(done);
    load();
  }
  const p = (id: string) => ({ params: { path: { brand, id } } });
  async function approve(f: Finding, test = false) {
    if (!test && spendsMore(f.kind) && !(await confirmAction(`${leverWord(f.kind)} on ${f.campaign_name}?`, "This change can spend more. Outflare sends it to the network, then measures it and gives a verdict when the window ends.", "Approve", false))) return;
    await act(f, () => api.POST("/v1/brands/{brand}/ads/findings/{id}/approve", { ...p(f.id), body: test ? { test: true } : {} }),
      test ? "Test queued: the network tries the change on part of the audience first. Follow it under Tests." : "Change sent to the network. Outflare measures it and gives a verdict when the window ends.");
  }
  const dismiss = (f: Finding) => act(f, () => api.POST("/v1/brands/{brand}/ads/findings/{id}/dismiss", p(f.id)), "Dismissed.");
  const acknowledge = (f: Finding) => act(f, () => acknowledgeFinding(brand, f.id), "Acknowledged.");
  async function undo(f: Finding) {
    if (!(await confirmAction(`Undo: ${leverWord(f.kind).toLowerCase()} on ${f.campaign_name}?`, "Outflare makes the opposite change on the network: restores the budget, resumes what was paused, or removes what was added.", "Undo", false))) return;
    await act(f, () => revertFinding(brand, f.id), "Undone. The network shows the change back within a minute.");
  }
  // A budget or bidding finding can be tried inside the network first
  // where its tests try that.
  const testable = (f: Finding) => ((f.kind === "budget_up" || f.kind === "budget_down") && testKinds[f.connector] === "budget") || (f.kind === "bidding_target" && testKinds[f.connector] === "bidding");

  async function turnRulesOn() {
    if (!(await confirmAction("Turn the rules on?", "What the rules find waits here for you to approve, savings included. Where Outflare Cloud allows it, savings (a pause, a budget cut, a negative keyword, an exclusion) can apply on their own and are listed here with Undo; anything that can spend more always waits for you. You can switch back to shadow mode at any time.", "Turn rules on", false))) return;
    setShadowError("");
    const { error } = await api.PUT("/v1/brands/{brand}/ads/economics", { params: { path: { brand } }, body: { shadow_mode: false } });
    if (error) {
      // Only a person's session may turn rules on; say so in the server's words.
      setShadowError(errorMessage(error));
      return;
    }
    toast("Rules are on. From the next daily review, what they find waits here for you.");
    reloadBrand();
    load();
  }
  async function refresh() {
    setBusy("refresh");
    const { error } = await api.POST("/v1/brands/{brand}/ads/findings/refresh", { params: { path: { brand } } });
    setBusy(null);
    if (error) return toast(errorMessage(error), "error");
    toast("Reading your campaigns again. New findings show in a minute.");
    setTimeout(load, 8000);
  }

  if (error && !rows) return <ErrorText>{error}</ErrorText>;
  if (!rows) return <Loading />;
  const open = rows.filter(isOpen);
  const done = rows.filter(isDone);
  const alerts = rows.filter((f) => isAlert(f) && statusOf(f) === "open");
  const measuring = rows.filter(isMeasuring);
  const shadow = rows.filter(isShadow);
  const shadowOn = economics?.shadow_mode ?? false;

  return (
    <div className="space-y-6">
      {error && <ErrorText>{error}</ErrorText>}
      <section aria-labelledby="ads-decisions" className="space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 id="ads-decisions" className="text-[13px] font-medium text-muted-foreground">Decisions{open.length > 0 && <span className="ml-2 font-mono text-[12px]">{open.length}</span>}</h2>
          {canManage && <Button variant="ghost" disabled={busy === "refresh"} onClick={refresh}>Check again</Button>}
        </div>
        {open.length === 0 ? (
          <p className="rounded-lg border border-dashed px-4 py-6 text-center text-[13px] text-muted-foreground">
            Nothing waits for you. Outflare reviews the brand's ads once a day and asks here before it changes anything.
          </p>
        ) : (
          <ul className="grid gap-3 lg:grid-cols-2">
            {open.map((f) => (
              <FindingCard key={f.id} f={f}
                badge={spendsMore(f.kind) ? <Badge tone="amber" title="This change can spend more, so it waits for you">Spends more</Badge> : <Badge tone="green" title="This change can only spend less">Saves</Badge>}
                actions={canManage && (
                  <>
                    <Button disabled={busy === f.id} onClick={() => approve(f)}>Approve</Button>
                    {testable(f) && <Button variant="ghost" disabled={busy === f.id} onClick={() => approve(f, true)}>Test it first</Button>}
                    <Button variant="ghost" disabled={busy === f.id} onClick={() => dismiss(f)}>Dismiss</Button>
                  </>
                )}
              />
            ))}
          </ul>
        )}
      </section>

      {alerts.length > 0 && (
        <section aria-labelledby="ads-alerts" className="space-y-3">
          <h2 id="ads-alerts" className="text-[13px] font-medium text-muted-foreground">Alerts</h2>
          <ul className="grid gap-3 lg:grid-cols-2">
            {alerts.map((f) => (
              <FindingCard key={f.id} f={f} badge={<Badge tone="red">Alert</Badge>}
                actions={canManage && <Button variant="ghost" disabled={busy === f.id} onClick={() => acknowledge(f)}>Acknowledge</Button>} />
            ))}
          </ul>
        </section>
      )}

      {done.length > 0 && (
        <section aria-labelledby="ads-done" className="space-y-3">
          <h2 id="ads-done" className="text-[13px] font-medium text-muted-foreground">Done for you</h2>
          <ul className="grid gap-3 lg:grid-cols-2">
            {done.map((f) => (
              <FindingCard key={f.id} f={f} badge={<Badge tone="green" title={`Applied ${ago(f.decided_at ?? f.created_at)}`}>Done · {ago(f.decided_at ?? f.created_at)}</Badge>}
                actions={canManage && <Button variant="ghost" disabled={busy === f.id} onClick={() => undo(f)}>Undo</Button>} />
            ))}
          </ul>
        </section>
      )}

      {measuring.length > 0 && (
        <Card title="Measuring" description="Approved changes, measured long enough to judge: at least a week and enough new customers, plus a few days for late payments. Each gets a verdict under Verdicts.">
          <ul className="divide-y text-[13px]">
            {measuring.map((f) => (
              <li key={f.id} className="flex flex-wrap items-center justify-between gap-2 py-2">
                <div><div>{changeLine(f)}</div><Where f={f} /></div>
                <span className="text-muted-foreground" title={f.window_note}>{statusOf(f) === "testing" ? "Being tested (Tests)" : f.measure_until ? `Verdict by ${f.measure_until}` : "Measuring"}</span>
              </li>
            ))}
          </ul>
        </Card>
      )}

      {(shadowOn || shadow.length > 0) && (
        <details className="rounded-lg border bg-card shadow-xs" onToggle={(e) => (e.currentTarget as HTMLDetailsElement).open && track("ui.ads_shadow_viewed", { rows: shadow.length })}>
          <summary className="cursor-pointer px-4 py-2.5 text-[13px] font-medium">
            What the rules would have done{shadow.length > 0 && <span className="ml-2 font-mono text-[12px] text-muted-foreground">{shadow.length}</span>}
            {shadowOn && <Badge tone="grey">Shadow mode</Badge>}
          </summary>
          <div className="space-y-3 border-t p-4">
            <p className="text-[13px] text-muted-foreground">
              {shadowOn
                ? "The rules only record what they would do. Nothing here was applied or offered. Turn them on when what they would have done looks right."
                : "Findings recorded while shadow mode was on."}
            </p>
            {shadow.length === 0 ? (
              <p className="text-[13px] text-muted-foreground">Nothing recorded yet. The rules run once a day after the sync.</p>
            ) : (
              <ul className="divide-y text-[13px]">
                {shadow.map((f) => (
                  <li key={f.id} className="py-2">
                    <div className="flex flex-wrap items-center justify-between gap-2"><span className="font-medium">{f.reason || leverWord(f.kind)}</span><span className="text-[12px] text-muted-foreground">{ago(f.created_at)}</span></div>
                    <div>{changeLine(f)}</div>
                    <Where f={f} />
                    {evidenceLine(f) && <div className="text-[12px] text-muted-foreground">{evidenceLine(f)}</div>}
                  </li>
                ))}
              </ul>
            )}
            {shadowError && <ErrorText>{shadowError}</ErrorText>}
            {canManage && shadowOn && <Button onClick={turnRulesOn}>Turn rules on</Button>}
          </div>
        </details>
      )}

      {scope.account && scope.network === "google_ads" && <Recommendations ws={ws} account={scope.account} />}

      <AdDrafts key={brand} brand={brand} canManage={canManage} />
    </div>
  );
}
