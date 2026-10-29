// Planning and AI for A/B tests: how long a test will take, test ideas,
// versions written from a prompt, and a results summary. The AI runs on the
// workspace's own model; everything it returns is a proposal a person
// applies.

import { useEffect, useRef, useState } from "react";
import type { ABTestAIRun, ABTestGoal, ABTestPlan, ABTestResults, Flag, FlagVariant } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, ErrorText, field, toast } from "../ui";

// measured is true for a goal compared on its mean per visitor.
export const measured = (g: ABTestGoal) => g.metric === "count" || g.metric === "value";

// perVisitor shows an arm's mean per visitor: dollars for deals.
export function perVisitor(g: ABTestGoal, mean: number): string {
  if (g.kind === "deal" && g.metric === "value") return `$${mean.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`;
  return mean.toLocaleString(undefined, { maximumFractionDigits: mean < 10 ? 3 : 2 });
}
const signed = (n: number) => `${n >= 0 ? "+" : "−"}${Math.abs(Math.round(n * 100))}%`;
const weeksText = (w: number) => (w <= 0 ? "over a year" : w < 1 ? "under a week" : `${w.toFixed(w < 10 ? 1 : 0)} weeks`);

// useAIRun starts an AI run and follows it until it is ready or failed.
export function useAIRun(ws: string) {
  const [run, setRun] = useState<ABTestAIRun | null>(null);
  const [error, setError] = useState("");
  const timer = useRef<number | undefined>(undefined);
  useEffect(() => () => window.clearTimeout(timer.current), []);
  const follow = (id: string) => {
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(async () => {
      const { data, error } = await api.GET("/v1/workspaces/{ws}/ab-test-ai-runs/{run}", { params: { path: { ws, run: id } } });
      if (error) return setError(errorMessage(error));
      setRun(data);
      if (data.status === "queued" || data.status === "running") follow(id);
    }, 1500);
  };
  const start = async (call: () => Promise<{ data?: ABTestAIRun; error?: unknown }>) => {
    setError("");
    setRun(null);
    const { data, error } = await call();
    if (error) return setError(errorMessage(error));
    setRun(data!);
    follow(data!.id);
  };
  const busy = run?.status === "queued" || run?.status === "running";
  return { run, error: error || (run?.status === "failed" ? run.error : ""), busy, start, ready: run?.status === "ready" ? run : null };
}

// PlanPanel says how long a test of the flag will take on the site's real
// traffic, before it starts.
export function PlanPanel({ ws, flag, goal, minEffect }: { ws: string; flag: string; goal?: ABTestGoal; minEffect: number }) {
  const [page, setPage] = useState("");
  const [plan, setPlan] = useState<ABTestPlan | null>(null);
  const [error, setError] = useState("");
  const usable = !!flag && !!goal && (goal.kind === "event" ? !!goal.name && (goal.metric !== "value" || !!goal.property) : goal.kind === "deal" || !!goal.path?.startsWith("/"));
  useEffect(() => {
    if (!usable) return setPlan(null);
    const t = window.setTimeout(async () => {
      const { data, error } = await api.POST("/v1/workspaces/{ws}/ab-tests/plan", {
        params: { path: { ws } },
        body: { flag, goal: goal!, min_effect: minEffect, ...(page.startsWith("/") ? { page } : {}) },
      });
      setError(error ? errorMessage(error) : "");
      setPlan(data ?? null);
    }, 400);
    return () => window.clearTimeout(t);
  }, [ws, flag, JSON.stringify(goal), minEffect, page, usable]); // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <div className="space-y-2 rounded-md border p-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="font-medium">How long it will take</span>
        <label className="flex items-center gap-2 text-[12px] text-muted-foreground">
          On page
          <input aria-label="Page the test changes" className={`${field} w-40 font-mono`} placeholder="/pricing (optional)" value={page} onChange={(e) => setPage(e.target.value.trim())} />
        </label>
      </div>
      {!usable ? (
        <p className="text-[12px] text-muted-foreground">Pick the goal it's judged on to see an estimate.</p>
      ) : error ? (
        <ErrorText>{error}</ErrorText>
      ) : !plan ? (
        <p className="text-[12px] text-muted-foreground">Estimating…</p>
      ) : (
        <>
          <p className="text-[12px] text-muted-foreground">
            Last 28 days: {plan.traffic.visitors.toLocaleString()} visitors,{" "}
            {goal && measured(goal) ? `${perVisitor(goal, plan.plan.baseline)} per visitor` : `${(plan.plan.baseline * 100).toFixed(1)}% reached the goal`}. About{" "}
            {Math.round(plan.plan.weekly_per_version).toLocaleString()} a week per version.
            {plan.plan.versions > 1 && ` With ${plan.plan.versions} versions against the control, each needs more visitors, since the results are corrected for the extra chances of a false winner.`}
          </p>
          {plan.plan.rows.length > 0 && (
            <div className="overflow-x-auto">
              <table className="w-full text-[12px] tabular-nums">
                <thead className="text-left text-muted-foreground">
                  <tr><th className="py-1 pr-3 font-normal">If the change truly is</th><th className="py-1 pr-3 text-right font-normal">Visitors per version</th><th className="py-1 text-right font-normal">Typically safe to stop in</th></tr>
                </thead>
                <tbody>
                  {plan.plan.rows.map((r) => (
                    <tr key={r.lift} className={`border-t ${Math.abs(r.lift - plan.plan.min_effect) < 1e-9 ? "font-medium" : ""}`}>
                      <td className="py-1 pr-3">{signed(r.lift)}</td>
                      <td className="py-1 pr-3 text-right">{r.visitors_per_version ? r.visitors_per_version.toLocaleString() : "over 50 million"}</td>
                      <td className="py-1 text-right">{weeksText(r.weeks)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          {plan.warning ? (
            <p className="rounded-md border border-amber-600/30 bg-amber-500/10 px-2 py-1.5 text-[12px]">{plan.warning}</p>
          ) : (
            <p className="text-[12px] text-muted-foreground">
              In 4 weeks it can find changes of about {signed(plan.plan.detectable_4_weeks)}; in 8 weeks, {signed(plan.plan.detectable_8_weeks)}. Half of tests finish sooner than these estimates, half later.
            </p>
          )}
        </>
      )}
    </div>
  );
}

type Suggestion = {
  title: string; hypothesis: string; page: string; change: string; goal: ABTestGoal;
  versions: { key: string; description: string }[]; weeks: number; detectable_8_weeks: number; warning: string;
};

// SuggestPanel asks the AI for test ideas, each with the engine's plan.
export function SuggestPanel({ ws, onUse }: { ws: string; onUse: (s: { name: string; hypothesis: string; goal: ABTestGoal }) => void }) {
  const ai = useAIRun(ws);
  const [notes, setNotes] = useState("");
  const [page, setPage] = useState("");
  const list = (ai.ready?.output as { suggestions?: Suggestion[] } | null)?.suggestions ?? [];
  return (
    <div className="space-y-3 text-[13px]">
      <p className="text-muted-foreground">Your AI model reads the site's busiest pages, the goals visitors reach, your brand and past tests, and proposes tests. Each comes with how long it would take on that page.</p>
      <textarea aria-label="What to focus on" className={`${field} w-full`} rows={2} maxLength={2000} placeholder="What to focus on (optional), e.g. more demo requests from agencies" value={notes} onChange={(e) => setNotes(e.target.value)} />
      <div className="flex flex-wrap items-center gap-2">
        <input aria-label="Only pages under" className={`${field} w-48 font-mono`} placeholder="/pricing (optional)" value={page} onChange={(e) => setPage(e.target.value.trim())} />
        <Button disabled={ai.busy} onClick={() => ai.start(() => api.POST("/v1/workspaces/{ws}/ab-tests/suggestions", { params: { path: { ws } }, body: { notes, ...(page.startsWith("/") ? { page } : {}) } }))}>
          {ai.busy ? "Thinking…" : list.length ? "Suggest again" : "Suggest tests"}
        </Button>
      </div>
      <ErrorText>{ai.error}</ErrorText>
      {list.map((s, i) => (
        <div key={i} className="space-y-1.5 rounded-md border p-3">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <span className="font-medium">{s.title}</span>
            <Badge tone={s.warning ? "amber" : "green"}>{s.weeks > 0 ? `~${weeksText(s.weeks)}` : "too little traffic"}</Badge>
          </div>
          <p>{s.hypothesis}</p>
          <p className="text-muted-foreground">{s.page && <span className="font-mono">{s.page}: </span>}{s.change}</p>
          <p className="text-[12px] text-muted-foreground">Judged on <span className="font-mono">{s.goal.kind === "event" ? s.goal.name : `viewing ${s.goal.path}`}</span>. Versions: {s.versions.map((v) => v.key).join(", ")}.</p>
          {s.warning && <p className="text-[12px] text-amber-700 dark:text-amber-400">{s.warning}</p>}
          <Button variant="ghost" onClick={() => onUse({ name: s.title, hypothesis: s.hypothesis, goal: s.goal })}>Use this</Button>
        </div>
      ))}
      {ai.ready && <p className="text-[12px] text-muted-foreground">Written by {ai.ready.model}. Check claims before you build anything.</p>}
    </div>
  );
}

type AIVersion = { key: string; content: Record<string, string>; rationale: string };

// VersionsPanel writes new versions of a flag from a prompt; a person adds
// the ones they like to the flag.
export function VersionsPanel({ ws, flag, onAdded }: { ws: string; flag: Flag; onAdded: () => void }) {
  const ai = useAIRun(ws);
  const [prompt, setPrompt] = useState("");
  const [added, setAdded] = useState<string[]>([]);
  const out = ai.ready?.output as { versions?: AIVersion[] } | null;
  async function add(v: AIVersion) {
    const weight = Math.min(...flag.variants.map((x) => x.weight).filter((w) => w > 0), 50);
    const variants: FlagVariant[] = [...flag.variants, { key: v.key, weight, payload: v.content }];
    const { error } = await api.PATCH("/v1/workspaces/{ws}/flags/{flag}", { params: { path: { ws, flag: flag.id } }, body: { variants } });
    if (error) return toast(errorMessage(error), "error");
    setAdded((a) => [...a, v.key]);
    toast(`Added ${v.key} to ${flag.name}.`);
    onAdded();
  }
  return (
    <div className="space-y-3 text-[13px]">
      <textarea aria-label="What to change" className={`${field} w-full`} rows={2} maxLength={2000} placeholder="What to change and for whom, e.g. a shorter, more urgent headline for agencies" value={prompt} onChange={(e) => setPrompt(e.target.value)} />
      <Button disabled={ai.busy || !prompt.trim()} onClick={() => ai.start(() => api.POST("/v1/workspaces/{ws}/ab-tests/versions", { params: { path: { ws } }, body: { flag: flag.id, prompt } }))}>
        {ai.busy ? "Writing…" : "Write versions"}
      </Button>
      <ErrorText>{ai.error}</ErrorText>
      {out?.versions?.map((v) => (
        <div key={v.key} className="space-y-1 rounded-md border p-3">
          <div className="flex items-center justify-between gap-2">
            <span className="font-mono">{v.key}</span>
            {added.includes(v.key) ? <Badge tone="green">Added</Badge> : <Button variant="ghost" onClick={() => add(v)}>Add to flag</Button>}
          </div>
          {Object.entries(v.content).map(([k, text]) => (
            <p key={k}><span className="text-muted-foreground">{k}: </span>{text}</p>
          ))}
          <p className="text-[12px] text-muted-foreground">{v.rationale}</p>
        </div>
      ))}
      {ai.ready && <p className="text-[12px] text-muted-foreground">Added versions go into the flag's split. Check the copy is true before visitors see it.</p>}
    </div>
  );
}

const recText: Record<string, { label: string; tone: "green" | "amber" | "red" | "grey" | "blue" }> = {
  ship: { label: "Ship the winner", tone: "green" },
  keep_control: { label: "Keep the original", tone: "amber" },
  fix_split: { label: "Fix the split first", tone: "red" },
  extend: { label: "Keep it running", tone: "blue" },
  stop: { label: "Stop and try something bolder", tone: "amber" },
  wait: { label: "Too early to tell", tone: "grey" },
};

// SummaryPanel has the AI put a test's results into words; the
// recommendation comes from the engine.
export function SummaryPanel({ ws, testID }: { ws: string; testID: string }) {
  const ai = useAIRun(ws);
  const out = ai.ready?.output as { recommendation: string; reason: string; headline: string; summary: string; next_steps: string[] } | null;
  return (
    <div className="space-y-2 text-[13px]">
      <Button variant="ghost" disabled={ai.busy} onClick={() => ai.start(() => api.POST("/v1/workspaces/{ws}/ab-tests/{test}/summary", { params: { path: { ws, test: testID } } }))}>
        {ai.busy ? "Summarising…" : out ? "Summarise again" : "Summarise with AI"}
      </Button>
      <ErrorText>{ai.error}</ErrorText>
      {out && (
        <div className="space-y-2 rounded-md border p-3">
          <div className="flex flex-wrap items-center gap-2">
            <Badge tone={recText[out.recommendation]?.tone ?? "grey"}>{recText[out.recommendation]?.label ?? out.recommendation}</Badge>
            <span className="font-medium">{out.headline}</span>
          </div>
          <p>{out.summary}</p>
          {out.next_steps.length > 0 && <ul className="list-disc space-y-1 pl-5">{out.next_steps.map((s, i) => <li key={i}>{s}</li>)}</ul>}
          <p className="text-[12px] text-muted-foreground">The recommendation is Outflare's own, from the numbers above: {out.reason} The words are by {ai.ready!.model}.</p>
        </div>
      )}
    </div>
  );
}

const lineColors = ["text-muted-foreground", "text-blue-600 dark:text-blue-400", "text-green-600 dark:text-green-400", "text-amber-600 dark:text-amber-400", "text-purple-600 dark:text-purple-400"];

// DayChart shows each version's conversion rate (or mean per visitor, for a
// count or value goal), cumulative, day by day.
export function DayChart({ days, control, goal }: { days: NonNullable<ABTestResults["result"]["goals"][number]["days"]>; control: string; goal: ABTestGoal }) {
  if (days.length < 2) return null;
  const W = 560, H = 160, pad = { l: 40, r: 8, t: 8, b: 20 };
  const mean = measured(goal);
  const rate = (a: { visitors: number; conversions: number; mean?: number }) => (mean ? a.mean ?? 0 : a.visitors ? a.conversions / a.visitors : 0);
  const tick = (v: number) => (mean ? perVisitor(goal, v) : `${(v * 100).toFixed(1)}%`);
  const max = Math.max(0.0001, ...days.flatMap((d) => d.arms.map(rate))) * 1.1;
  const x = (i: number) => pad.l + (i / (days.length - 1)) * (W - pad.l - pad.r);
  const y = (v: number) => pad.t + (1 - v / max) * (H - pad.t - pad.b);
  const variants = days[days.length - 1].arms.map((a) => a.variant);
  return (
    <div className="space-y-1">
      <svg viewBox={`0 0 ${W} ${H}`} className="w-full" role="img" aria-label={mean ? "Mean per visitor by day, cumulative" : "Conversion rate by day, cumulative"}>
        {[0, 0.5, 1].map((f) => (
          <g key={f} className="text-muted-foreground">
            <line x1={pad.l} x2={W - pad.r} y1={y(max * f)} y2={y(max * f)} stroke="currentColor" strokeOpacity={0.15} />
            <text x={pad.l - 4} y={y(max * f) + 3} textAnchor="end" fontSize="9" fill="currentColor">{tick(max * f)}</text>
          </g>
        ))}
        <text x={pad.l} y={H - 4} fontSize="9" className="text-muted-foreground" fill="currentColor">{days[0].date}</text>
        <text x={W - pad.r} y={H - 4} fontSize="9" textAnchor="end" className="text-muted-foreground" fill="currentColor">{days[days.length - 1].date}</text>
        {variants.map((v, vi) => (
          <polyline key={v} className={lineColors[vi % lineColors.length]} fill="none" stroke="currentColor" strokeWidth={v === control ? 1.5 : 2}
            strokeDasharray={v === control ? "4 3" : undefined} points={days.map((d, i) => `${x(i)},${y(rate(d.arms[vi] ?? { visitors: 0, conversions: 0 }))}`).join(" ")} />
        ))}
      </svg>
      <div className="flex flex-wrap gap-3 text-[12px]">
        {variants.map((v, vi) => <span key={v} className={lineColors[vi % lineColors.length]}>{v === control ? "- - " : "— "}<span className="text-foreground font-mono">{v}</span></span>)}
      </div>
    </div>
  );
}

// LiftChart shows each version's lift over the control, cumulative, day by
// day: whether a lead is steady or still settling.
export function LiftChart({ days, control, goal }: { days: NonNullable<ABTestResults["result"]["goals"][number]["days"]>; control: string; goal: ABTestGoal }) {
  if (days.length < 2) return null;
  const W = 560, H = 160, pad = { l: 40, r: 8, t: 8, b: 20 };
  const mean = measured(goal);
  const value = (a?: { visitors: number; conversions: number; mean?: number }) => (!a ? 0 : mean ? a.mean ?? 0 : a.visitors ? a.conversions / a.visitors : 0);
  const variants = days[days.length - 1].arms.map((a) => a.variant);
  const ci = variants.indexOf(control);
  const lift = (d: (typeof days)[number], vi: number) => {
    const c = value(d.arms[ci]);
    return c > 0 ? value(d.arms[vi]) / c - 1 : null;
  };
  const all = days.flatMap((d) => variants.map((_, vi) => (vi === ci ? 0 : lift(d, vi) ?? 0)));
  const top = Math.max(0.05, ...all) * 1.1, bottom = Math.min(-0.05, ...all) * 1.1;
  const x = (i: number) => pad.l + (i / (days.length - 1)) * (W - pad.l - pad.r);
  const y = (v: number) => pad.t + ((top - v) / (top - bottom)) * (H - pad.t - pad.b);
  const pctText = (v: number) => `${v >= 0 ? "+" : "−"}${Math.abs(Math.round(v * 100))}%`;
  return (
    <div className="space-y-1">
      <svg viewBox={`0 0 ${W} ${H}`} className="w-full" role="img" aria-label="Lift over the control by day, cumulative">
        {[top, 0, bottom].map((v) => (
          <g key={v} className="text-muted-foreground">
            <line x1={pad.l} x2={W - pad.r} y1={y(v)} y2={y(v)} stroke="currentColor" strokeOpacity={v === 0 ? 0.5 : 0.15} strokeDasharray={v === 0 ? "4 3" : undefined} />
            <text x={pad.l - 4} y={y(v) + 3} textAnchor="end" fontSize="9" fill="currentColor">{v === 0 ? control : pctText(v)}</text>
          </g>
        ))}
        <text x={pad.l} y={H - 4} fontSize="9" className="text-muted-foreground" fill="currentColor">{days[0].date}</text>
        <text x={W - pad.r} y={H - 4} fontSize="9" textAnchor="end" className="text-muted-foreground" fill="currentColor">{days[days.length - 1].date}</text>
        {variants.map((v, vi) =>
          vi === ci ? null : (
            <polyline key={v} className={lineColors[vi % lineColors.length]} fill="none" stroke="currentColor" strokeWidth={2}
              points={days.flatMap((d, i) => { const l = lift(d, vi); return l === null ? [] : [`${x(i)},${y(l)}`]; }).join(" ")} />
          ),
        )}
      </svg>
      <div className="flex flex-wrap gap-3 text-[12px]">
        {variants.map((v, vi) => (vi === ci ? null : <span key={v} className={lineColors[vi % lineColors.length]}>— <span className="text-foreground font-mono">{v}</span></span>))}
      </div>
    </div>
  );
}

