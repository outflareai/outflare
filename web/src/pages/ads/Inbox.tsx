// Actions: every recommendation, alert and applied change as one table,
// under To do, In progress, Done and Dismissed. Each row says what to do,
// why, and what to expect, with one main button and the rest under More.
// Every change waits here for a person; anything that can spend more asks
// first, with the budget before and after and the most it can add a month.
// Recommendations have an On/Off switch: while they are off (shadow mode),
// what Outflare would have recommended is listed as a preview only.

import { useCallback, useEffect, useState, type ReactNode } from "react";
import type { Tab } from "../../App";
import { DataTable } from "@/components/data-table";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api, errorMessage } from "../../api";
import { track } from "@/lib/analytics";
import { Badge, Button, Card, confirmAction, ErrorText, Loading, Menu, Status, toast } from "../../ui";
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

// What kind of action a row is, in one word.
export function typeOf(f: Finding): { word: string; tone: "green" | "amber" | "red" | "blue" } {
  if (isAlert(f) || f.kind === "overspend_pause") return { word: "Fix", tone: "red" };
  if (f.kind === "experiment_start" || f.test_first) return { word: "Experiment", tone: "blue" };
  return spendsMore(f.kind) ? { word: "Grow", tone: "amber" } : { word: "Cut waste", tone: "green" };
}

// An alert's main button opens the page where it is fixed.
function alertFix(f: Finding): { label: string; item: string } {
  if (f.kind === "alert.tracking_broken" || f.kind === "alert.zero_conversions") return { label: "Open tracking", item: "tracking" };
  if (f.kind === "alert.sends_failing" || f.kind === "alert.sends_not_counted") return { label: "Open the send log", item: "sends" };
  if (f.kind === "alert.auto_apply_on") return { label: "Open ad accounts", item: "accounts" };
  if (f.kind === "alert.target_stale" || !f.campaign_id) return { label: "Open Settings", item: "setup" };
  return { label: "Open the campaign", item: f.campaign_id };
}

// The most a budget raise can add in a month (30.4 days).
function monthlyMax(f: Finding): number | null {
  const before = num(f.evidence.daily_budget_micros);
  const after = num(f.proposed.daily_budget_micros);
  return before !== null && after !== null && after > before ? Math.round((after - before) * 30.4) : null;
}

const views = ["todo", "progress", "done", "dismissed", "preview"] as const;
type View = (typeof views)[number];

type Props = { ws: string; brand: string; scope: AdScope; canManage: boolean; economics: AdEconomics | null; reloadBrand: () => void; go: (tab: Tab, item?: string) => void; changed?: () => void };

export function Inbox({ ws, brand, scope, canManage, economics, reloadBrand, go, changed }: Props) {
  const [rows, setRows] = useState<Finding[] | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const [view, setView] = useState<View>("todo");

  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/ads/findings", { params: { path: { brand }, query: { ...scopeQuery(scope), limit: 300 } } });
    setError(error ? errorMessage(error) : "");
    if (data) setRows(data.data as Finding[]);
    else if (!rows) setRows([]);
  }, [brand, scope.network, scope.account]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    load();
  }, [load]);

  // A decided row leaves the list at once and comes back if the call
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
    changed?.();
  }
  const p = (id: string) => ({ params: { path: { brand, id } } });
  async function approve(f: Finding, test = false) {
    if (spendsMore(f.kind)) {
      const before = num(f.evidence.daily_budget_micros);
      const after = num(f.proposed.daily_budget_micros);
      const max = monthlyMax(f);
      const ok = await confirmAction(`${leverWord(f.kind)} on ${f.campaign_name}?`, <>
        <p>This change can spend more.</p>
        {after !== null && <p>Daily budget: {before !== null && <>{money(before, f.currency)} → </>}{money(after, f.currency)}.</p>}
        {max !== null && <p>The most it can add: {money(max, f.currency)} a month.</p>}
        <p>{test ? (f.test_first ? `The network tries it on half of the campaign for ${f.test_first.days} days, beside the campaign as it is.` : "The network tries it on part of the traffic first.") : "Outflare sends it to the network, measures it and tells you whether it worked."} You can undo it at any time.</p>
      </>, test ? "Start the test" : "Approve", false);
      if (!ok) return;
    }
    await act(f, () => api.POST("/v1/brands/{brand}/ads/findings/{id}/approve", { ...p(f.id), body: test ? { test: true } : {} }),
      test ? "Test queued: the network tries the change on part of the traffic first. Follow it under Experiments." : "Change sent to the network. It is now under In progress while Outflare measures it.");
  }
  const dismiss = (f: Finding) => act(f, () => api.POST("/v1/brands/{brand}/ads/findings/{id}/dismiss", p(f.id)), "Dismissed.");
  const acknowledge = (f: Finding) => act(f, () => acknowledgeFinding(brand, f.id), "Marked as handled.");
  async function undo(f: Finding) {
    if (!(await confirmAction(`Undo: ${leverWord(f.kind).toLowerCase()} on ${f.campaign_name}?`, "Outflare makes the opposite change on the network: restores the budget, resumes what was paused, or removes what was added.", "Undo", false))) return;
    await act(f, () => revertFinding(brand, f.id), "Undone. The network shows the change back within a minute.");
  }
  // A budget or bidding finding can be tried inside the network first
  // where its tests try that.
  const testable = (f: Finding) => ((f.kind === "budget_up" || f.kind === "budget_down") && testKinds[f.connector] === "budget") || (f.kind === "bidding_target" && testKinds[f.connector] === "bidding");

  // Recommendations on or off. Only a person's own session may switch them.
  async function setRecommendations(on: boolean) {
    const ok = on
      ? await confirmAction("Turn recommendations on?", "From the next daily review, what Outflare finds is listed here for you to approve. Nothing changes in your ad accounts until you approve it; anything that can spend more always asks first. Where Outflare Cloud allows it, savings (a pause, a budget cut, an exclusion) can apply on their own and are listed under Done with Undo.", "Turn recommendations on", false)
      : await confirmAction("Turn recommendations off?", "Outflare keeps reviewing the account but lists what it finds as a preview only. Nothing is offered or applied.", "Turn recommendations off", false);
    if (!ok) return;
    const { error } = await api.PUT("/v1/brands/{brand}/ads/economics", { params: { path: { brand } }, body: { shadow_mode: !on } });
    if (error) return toast(errorMessage(error), "error");
    toast(on ? "Recommendations are on. From the next daily review, what Outflare finds waits here for you." : "Recommendations are off.");
    reloadBrand();
    load();
    changed?.();
  }
  async function refresh() {
    setBusy("refresh");
    const { error } = await api.POST("/v1/brands/{brand}/ads/findings/refresh", { params: { path: { brand } } });
    setBusy(null);
    if (error) return toast(errorMessage(error), "error");
    toast("Reading your campaigns again. New actions show in a minute.");
    setTimeout(load, 8000);
  }

  if (error && !rows) return <ErrorText>{error}</ErrorText>;
  if (!rows) return <Loading />;
  const shadowOn = economics?.shadow_mode ?? false;
  const lists: Record<View, Finding[]> = {
    todo: rows.filter((f) => !isShadow(f) && (isOpen(f) || (isAlert(f) && statusOf(f) === "open"))).sort((a, b) => Math.abs(b.impact_micros ?? 0) - Math.abs(a.impact_micros ?? 0)),
    progress: rows.filter(isMeasuring),
    done: rows.filter((f) => !isShadow(f) && (isDone(f) || statusOf(f) === "judged")),
    dismissed: rows.filter((f) => ["dismissed", "acknowledged", "expired"].includes(statusOf(f))),
    preview: rows.filter(isShadow),
  };
  const shown = (shadowOn || lists.preview.length > 0 ? views : views.filter((v) => v !== "preview"));
  const names: Record<View, string> = { todo: "To do", progress: "In progress", done: "Done", dismissed: "Dismissed", preview: "Preview" };
  const list = lists[view];
  const raises = lists.todo.filter((f) => spendsMore(f.kind));
  const mostAdded = raises.reduce((a, f) => a + (monthlyMax(f) ?? 0), 0);
  const currency = lists.todo[0]?.currency ?? economics?.currency ?? "USD";
  const empty: Record<View, string> = {
    todo: shadowOn ? "Recommendations are off, so nothing is listed here. Turn them on above, or look at the Preview." : "Nothing waits for you. Outflare reviews the brand's ads once a day and asks here before it changes anything.",
    progress: "No approved change is being measured right now.",
    done: "No change has a result yet.",
    dismissed: "Nothing dismissed.",
    preview: "Nothing recorded yet. The review runs once a day after the sync.",
  };

  // A row opens the action's own page: every number behind it, the agent
  // to ask about it, and Good advice or Bad advice.
  const open = (f: Finding) => { track("ui.ads_action_opened", { kind: f.kind, view }); go("ads", `actions/${f.id}`); };
  const main = (f: Finding) => {
    if (!canManage) return null;
    const off = busy === f.id;
    if (view === "todo") {
      if (isAlert(f)) {
        const fix = alertFix(f);
        return (
          <div className="flex justify-end gap-1.5">
            <Button disabled={off} onClick={() => { track("ui.ads_alert_fix_opened", { kind: f.kind }); go("ads", fix.item); }}>{fix.label}</Button>
            <Menu aria={`More for ${f.reason || leverWord(f.kind)}`} items={[{ label: "Mark as handled", onClick: () => acknowledge(f) }]} />
          </div>
        );
      }
      return (
        <div className="flex justify-end gap-1.5">
          {f.test_first
            ? <Button disabled={off} title={f.test_first.note} onClick={() => approve(f, true)}>Test it first</Button>
            : <Button disabled={off} onClick={() => approve(f)}>Approve</Button>}
          <Menu aria={`More for ${f.reason || leverWord(f.kind)}`} items={[
            f.test_first ? { label: "Make the change now", onClick: () => approve(f) } : testable(f) && { label: "Test it first", onClick: () => approve(f, true) },
            { label: "See why", onClick: () => open(f) },
            { label: "Open the campaign", onClick: () => go("ads", f.campaign_id) },
            { label: "Dismiss", onClick: () => dismiss(f) },
          ]} />
        </div>
      );
    }
    if (view === "done" && isDone(f)) return <Button variant="ghost" disabled={off} onClick={() => undo(f)}>Undo</Button>;
    if (view === "progress" || view === "done") return <Button variant="ghost" onClick={() => go("ads", "experiments")}>See the result</Button>;
    return null;
  };
  const state = (f: Finding): ReactNode => {
    if (view === "progress") return <span title={f.window_note}>{statusOf(f) === "testing" ? "Being tested" : f.measure_until ? `Result by ${f.measure_until}` : "Being measured"}</span>;
    if (view === "done") return isDone(f) ? `Applied ${ago(f.decided_at ?? f.created_at)}` : f.verdict ? f.verdict.charAt(0).toUpperCase() + f.verdict.slice(1).replace(/_/g, " ") : "Judged";
    if (view === "dismissed") return statusOf(f) === "expired" ? "No longer applies" : statusOf(f) === "acknowledged" ? "Handled" : "Dismissed";
    if (view === "preview") return `Found ${ago(f.created_at)}`;
    const effect = impactLine(f)?.replace(/^Expected: /, "");
    if (f.test_first) return `A ${f.test_first.days}-day test proves it first`;
    return effect ? effect.charAt(0).toUpperCase() + effect.slice(1) : isAlert(f) ? "Keeps your numbers right" : "—";
  };

  return (
    <div className="space-y-6">
      {error && <ErrorText>{error}</ErrorText>}
      <section aria-label="Recommendations" className="flex flex-wrap items-center justify-between gap-3 rounded-lg border bg-card px-4 py-3 shadow-xs">
        <div className="min-w-0">
          <div className="flex items-center gap-2 text-[13px] font-medium">Recommendations <Status tone={shadowOn ? "grey" : "green"}>{shadowOn ? "Off" : "On"}</Status></div>
          <p className="text-[12px] text-muted-foreground">
            {shadowOn
              ? `Outflare reviews the account every day but keeps what it finds to itself${lists.preview.length > 0 ? `: ${lists.preview.length} so far, under Preview` : ""}. Turn them on to get actions you can approve.`
              : "Outflare reviews the account every day and lists actions here. Nothing changes without your OK."}
          </p>
        </div>
        {canManage && (
          <div className="flex gap-2">
            <Button variant="ghost" disabled={busy === "refresh"} onClick={refresh}>Check again</Button>
            <Button variant={shadowOn ? "primary" : "ghost"} onClick={() => setRecommendations(shadowOn)}>{shadowOn ? "Turn recommendations on" : "Turn off"}</Button>
          </div>
        )}
      </section>

      <Card title="Recommended actions"
        description={<>Biggest expected effect first. Actions look at their own time window, not the date range above.{raises.length > 0 && mostAdded > 0 && <> Approving every raise adds at most {money(mostAdded, currency)} a month.</>} Source: Outflare's daily review of your ad accounts.</>}>
        <Tabs value={view} onValueChange={(v) => { setView(v as View); track("ui.ads_actions_view_opened", { view: v }); }}>
          <TabsList className="mb-3 h-auto! flex-wrap justify-start" aria-label="Actions by state">
            {shown.map((v) => <TabsTrigger key={v} value={v} className="px-3 text-[12px]">{names[v]} · {lists[v].length}</TabsTrigger>)}
          </TabsList>
        </Tabs>
        <DataTable
          data={list}
          rowId={(f) => f.id}
          columnsMenu={false}
          onRowClick={open}
          empty={empty[view]}
          mobile={(f) => (
            <div className="space-y-1.5">
              <div className="flex items-start justify-between gap-2"><span className="font-medium">{f.reason || leverWord(f.kind)}</span><Badge tone={typeOf(f).tone}>{typeOf(f).word}</Badge></div>
              <div className="text-[12px] text-muted-foreground">{f.campaign_name}</div>
              {evidenceLine(f) && <div className="text-[12px] text-muted-foreground">{evidenceLine(f)}</div>}
              <div className="text-[13px]">{state(f)}</div>
              <div onClick={(e) => e.stopPropagation()}>{main(f)}</div>
            </div>
          )}
          columns={[
            { id: "type", header: "Type", enableSorting: false, accessorFn: (f) => typeOf(f).word, cell: ({ row: { original: f } }) => <Badge tone={typeOf(f).tone}>{typeOf(f).word}</Badge> },
            { id: "action", header: "Action", enableSorting: false, accessorFn: (f) => f.reason, cell: ({ row: { original: f } }) => (
              <div className="max-w-sm whitespace-normal">
                <button type="button" className="text-left font-medium text-blue-700 underline-offset-2 hover:underline dark:text-blue-400" onClick={(e) => { e.stopPropagation(); open(f); }}>{f.reason || leverWord(f.kind)}</button>
                {changeLine(f) && <div className="text-[13px]">{changeLine(f)}</div>}
                <div className="text-[12px] text-muted-foreground">{f.campaign_name} · {platformName(f.connector)}</div>
              </div>
            ) },
            { id: "why", header: "Why", enableSorting: false, cell: ({ row: { original: f } }) => <div className="max-w-md whitespace-normal text-muted-foreground">{evidenceLine(f) || "—"}</div> },
            { id: "effect", header: view === "todo" ? "Expected effect" : "Status", enableSorting: false, cell: ({ row: { original: f } }) => <div className="max-w-56 whitespace-normal">{state(f)}</div> },
            { id: "do", header: "", enableSorting: false, meta: { align: "right" }, cell: ({ row: { original: f } }) => <div onClick={(e) => e.stopPropagation()}>{main(f)}</div> },
          ]}
        />
      </Card>

      {scope.account && scope.network === "google_ads" && <Recommendations ws={ws} account={scope.account} />}

      <AdDrafts key={brand} brand={brand} canManage={canManage} />
    </div>
  );
}
