// A/B tests: which version of a feature flag does best at a goal. A test
// names the flag, its control and its goals; Outflare counts visitors who
// were shown each version and how many of them converted, and says which
// version wins, in plain words.

import { useCallback, useEffect, useState, type FormEvent } from "react";
import type { ABTest, ABTestComparison, ABTestGoal, ABTestResults, Flag } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, confirmAction, Empty, ErrorText, field, Loading, PageHeader, toast, type tones } from "../ui";
import { ago } from "../format";
import { useBrand } from "@/lib/brand";
import { BrandBadge } from "@/components/brand-badge";
import { DataTable } from "@/components/data-table";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import type { Tab } from "@/lib/paths";

type Props = { ws: string; canManage: boolean; go: (t: Tab) => void };

// A flag to start a test of, set by the Feature flags page before it opens
// this one.
let pendingFlag: string | null = null;
export function testFlag(key: string) {
  pendingFlag = key;
}

const statusTone: Record<ABTest["status"], keyof typeof tones> = { draft: "grey", running: "blue", done: "green" };
const statusText: Record<ABTest["status"], string> = { draft: "Draft", running: "Running", done: "Done" };

export const goalText = (g: ABTestGoal) => (g.kind === "event" ? g.name ?? "" : `Viewed ${g.path}`);
const pct = (n: number, digits = 1) => `${(n * 100).toFixed(digits)}%`;
// A chance never reads as certain: 99.96% is "over 99%".
const chance = (n: number) => (n > 0.99 ? "over 99%" : n < 0.01 ? "under 1%" : pct(n, 0));
const signed = (n: number) => `${n >= 0 ? "+" : "−"}${Math.abs(Math.round(n * 100))}%`;

export function ABTests({ ws, canManage, go }: Props) {
  const { query } = useBrand(ws);
  const [tests, setTests] = useState<ABTest[] | null>(null);
  const [flags, setFlags] = useState<Flag[]>([]);
  const [open, setOpen] = useState<ABTest | null>(null);
  const [creating, setCreating] = useState<string | null>(null);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/ab-tests", { params: { path: { ws }, query } });
    if (error) setError(errorMessage(error));
    setTests(data?.data ?? []);
    setOpen((cur) => (cur ? data?.data.find((t) => t.id === cur.id) ?? null : null));
  }, [ws, query.brand]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    load();
    api.GET("/v1/workspaces/{ws}/flags", { params: { path: { ws }, query } }).then(({ data }) => setFlags(data?.data ?? []));
    if (pendingFlag !== null && canManage) setCreating(pendingFlag);
    pendingFlag = null;
  }, [load, ws, canManage]);

  const splits = flags.filter((f) => f.variants.length > 1);
  const newTest = () => setCreating("");

  return (
    <div className="space-y-6">
      <PageHeader
        title="A/B tests"
        description="Pick a feature flag with two or more versions, say what counts as success, and see which version wins. The numbers stay honest however often you check."
        action={canManage && <Button onClick={newTest}>New test</Button>}
      />
      <ErrorText>{error}</ErrorText>
      {tests === null ? (
        <Loading />
      ) : tests.length === 0 ? (
        <Empty title="No A/B tests yet" action={canManage && <Button onClick={newTest}>New test</Button>}>
          A test is built on a <button className="underline underline-offset-2" onClick={() => go("flags")}>feature flag</button> that splits visitors between versions, such as a new headline for half of them. Visitors join when a page shows them their version.
        </Empty>
      ) : (
        <DataTable
          data={tests}
          rowId={(t) => t.id}
          searchPlaceholder="Search tests"
          onRowClick={(t) => setOpen(t)}
          mobile={(t) => (
            <div className="space-y-0.5">
              <div className="font-medium">{t.name} <Badge tone={statusTone[t.status]}>{statusText[t.status]}</Badge></div>
              <div className="text-[12px] text-muted-foreground">{t.flag_key} · {t.goals[0] ? goalText(t.goals[0]) : ""}</div>
            </div>
          )}
          columns={[
            {
              id: "name", header: "Test", accessorFn: (t) => `${t.name} ${t.flag_key}`,
              cell: ({ row: { original: t } }) => (
                <div>
                  <div className="font-medium">{t.name} <BrandBadge id={t.brand_id} /></div>
                  <div className="font-mono text-[12px] text-muted-foreground">{t.flag_key}</div>
                </div>
              ),
            },
            { id: "status", header: "Status", accessorFn: (t) => t.status, cell: ({ row: { original: t } }) => <Badge tone={statusTone[t.status]}>{statusText[t.status]}</Badge> },
            { id: "goal", header: "Judged on", accessorFn: (t) => (t.goals[0] ? goalText(t.goals[0]) : ""), cell: ({ row: { original: t } }) => <span className="font-mono text-[12px]">{t.goals[0] ? goalText(t.goals[0]) : ""}</span> },
            {
              id: "when", header: "When", accessorFn: (t) => t.ended_at ?? t.started_at ?? t.created_at,
              cell: ({ row: { original: t } }) => (
                <span className="text-muted-foreground">
                  {t.status === "done" ? `Ended ${ago(t.ended_at!)}${t.decision ? `, kept ${t.decision}` : ""}` : t.status === "running" ? `Started ${ago(t.started_at!)}` : `Added ${ago(t.created_at)}`}
                </span>
              ),
            },
          ]}
        />
      )}
      {open && <TestSheet ws={ws} test={open} flag={flags.find((f) => f.id === open.flag_id)} canManage={canManage} onClose={() => setOpen(null)} onChanged={load} />}
      {creating !== null && (
        <NewTestSheet ws={ws} flags={splits} flag={creating} go={go} onClose={() => setCreating(null)} onSaved={(t) => { setCreating(null); load(); setOpen(t); }} />
      )}
    </div>
  );
}

function useEvents(ws: string) {
  const [events, setEvents] = useState<string[]>([]);
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/catalog/events", { params: { path: { ws }, query: { status: "approved" } } }).then(({ data }) =>
      // Page views are a page goal, and seeing a version is the test itself.
      setEvents((data?.data ?? []).map((e) => e.name).filter((n) => n !== "experiment_viewed" && n !== "page.viewed")),
    );
  }, [ws]);
  return events;
}

function GoalsEditor({ goals, setGoals, events }: { goals: ABTestGoal[]; setGoals: (g: ABTestGoal[]) => void; events: string[] }) {
  const set = (i: number, g: ABTestGoal) => setGoals(goals.map((x, k) => (k === i ? g : x)));
  return (
    <div className="space-y-2">
      <datalist id="ab-events">{events.map((e) => <option key={e} value={e} />)}</datalist>
      {goals.map((g, i) => (
        <div key={i} className="flex flex-wrap items-center gap-2 rounded-md border p-2">
          <span className="w-16 text-[12px] text-muted-foreground">{i === 0 ? "Judged on" : "Also"}</span>
          <select aria-label="Kind" className={field} value={g.kind} onChange={(e) => set(i, e.target.value === "page" ? { kind: "page", path: "/" } : { kind: "event", name: "" })}>
            <option value="event">Event</option>
            <option value="page">Page viewed</option>
          </select>
          {g.kind === "event" ? (
            <input aria-label="Event" list="ab-events" className={`${field} min-w-40 flex-1 font-mono`} required pattern="[a-z][a-z0-9_]*(\.[a-z0-9_]+)*" placeholder="signup_started" value={g.name ?? ""} onChange={(e) => set(i, { kind: "event", name: e.target.value.trim() })} />
          ) : (
            <input aria-label="Path" className={`${field} min-w-40 flex-1 font-mono`} required pattern="/.*" placeholder="/thanks or /docs/*" value={g.path ?? ""} onChange={(e) => set(i, { kind: "page", path: e.target.value.trim() })} />
          )}
          {goals.length > 1 && <Button type="button" variant="ghost" onClick={() => setGoals(goals.filter((_, k) => k !== i))}>Remove</Button>}
        </div>
      ))}
      {goals.length < 5 && <Button type="button" variant="ghost" onClick={() => setGoals([...goals, { kind: "event", name: "" }])}>Watch another goal</Button>}
    </div>
  );
}

// The smallest change a test looks for, in words.
const effects: { value: number; label: string }[] = [
  { value: 0.1, label: "+10%: small tweaks, for busy sites" },
  { value: 0.25, label: "+25%: most tests (default)" },
  { value: 0.5, label: "+50%: bold changes, for small sites" },
];

function EffectPicker({ value, onChange }: { value: number; onChange: (v: number) => void }) {
  const known = effects.some((e) => e.value === value);
  return (
    <label className="block">
      <span className="mb-1 block font-medium">Smallest change worth finding</span>
      <select className={`${field} w-full`} value={value} onChange={(e) => onChange(Number(e.target.value))}>
        {!known && <option value={value}>{signed(value)}</option>}
        {effects.map((e) => <option key={e.value} value={e.value}>{e.label}</option>)}
      </select>
      <span className="mt-1 block text-[12px] text-muted-foreground">The test is tuned to spot changes of about this size. Smaller finds small wins but needs more visitors; big wins show up a little sooner when it's larger. It can't change once the test starts.</span>
    </label>
  );
}

function NewTestSheet({ ws, flags, flag: initial, go, onClose, onSaved }: { ws: string; flags: Flag[]; flag: string; go: (t: Tab) => void; onClose: () => void; onSaved: (t: ABTest) => void }) {
  const events = useEvents(ws);
  const [flagKey, setFlagKey] = useState(initial || flags[0]?.key || "");
  const flag = flags.find((f) => f.key === flagKey);
  const [control, setControl] = useState("");
  const [name, setName] = useState("");
  const [hypothesis, setHypothesis] = useState("");
  const [goals, setGoals] = useState<ABTestGoal[]>([{ kind: "event", name: "" }]);
  const [minEffect, setMinEffect] = useState(0.25);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    if (!flagKey && flags[0]) setFlagKey(flags[0].key);
  }, [flags, flagKey]);
  const keys = flag?.variants.map((v) => v.key) ?? [];
  const ctl = keys.includes(control) ? control : keys.includes("control") ? "control" : keys[0] ?? "";

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const { data, error } = await api.POST("/v1/workspaces/{ws}/ab-tests", { params: { path: { ws } }, body: { flag: flagKey, control: ctl, name: name.trim() || undefined, hypothesis, goals, min_effect: minEffect } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    toast(`Added ${data!.name}. Start it when the pages are ready.`);
    onSaved(data!);
  }

  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>New A/B test</SheetTitle>
          <SheetDescription>Choose a flag that splits visitors between versions, the version to compare with, and what counts as success.</SheetDescription>
        </SheetHeader>
        {flags.length === 0 ? (
          <div className="px-4 pb-6 text-[13px]">
            <p className="text-muted-foreground">No flag splits visitors between versions yet. <button className="underline underline-offset-2" onClick={() => go("flags")}>Add a flag</button> with two or more versions first.</p>
          </div>
        ) : (
          <form onSubmit={submit} className="space-y-5 px-4 pb-6 text-[13px]">
            <label className="block">
              <span className="mb-1 block font-medium">Flag</span>
              <select className={`${field} w-full`} value={flagKey} onChange={(e) => setFlagKey(e.target.value)}>
                {flags.map((f) => <option key={f.id} value={f.key}>{f.name} ({f.key})</option>)}
              </select>
            </label>
            <label className="block">
              <span className="mb-1 block font-medium">Compare with</span>
              <select className={`${field} w-full`} value={ctl} onChange={(e) => setControl(e.target.value)}>
                {keys.map((k) => <option key={k} value={k}>{k}</option>)}
              </select>
              <span className="mt-1 block text-[12px] text-muted-foreground">The control: usually what visitors see today.</span>
            </label>
            <div className="space-y-2">
              <span className="block font-medium">Goals</span>
              <GoalsEditor goals={goals} setGoals={setGoals} events={events} />
              <p className="text-[12px] text-muted-foreground">A visitor converts when they reach a goal after seeing their version. The first goal decides the winner; the rest are watched alongside it. Goals can't change once the test starts.</p>
            </div>
            <EffectPicker value={minEffect} onChange={setMinEffect} />
            <label className="block">
              <span className="mb-1 block font-medium">Name <span className="font-normal text-muted-foreground">(optional)</span></span>
              <input className={`${field} w-full`} maxLength={200} placeholder={flag?.name} value={name} onChange={(e) => setName(e.target.value)} />
            </label>
            <label className="block">
              <span className="mb-1 block font-medium">Hypothesis <span className="font-normal text-muted-foreground">(optional)</span></span>
              <textarea className={`${field} w-full`} rows={2} maxLength={2000} placeholder="A shorter headline gets more signups, because…" value={hypothesis} onChange={(e) => setHypothesis(e.target.value)} />
            </label>
            <ErrorText>{error}</ErrorText>
            <div className="flex gap-2 border-t pt-4">
              <Button disabled={busy || !flagKey}>{busy ? "Adding…" : "Add test"}</Button>
              <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
            </div>
          </form>
        )}
      </SheetContent>
    </Sheet>
  );
}

// verdict says in words what the first goal's results mean.
export function verdict(r: ABTestResults, names: (v: string) => string): { text: string; tone: keyof typeof tones } {
  const res = r.result;
  if (res.sample_ratio_mismatch) return { text: "The split between versions is off from the flag's weights, so these results can't be trusted. Something (a redirect, a cache, bots) is losing or double-counting visitors.", tone: "red" };
  const goal = res.goals[0];
  if (!goal) return { text: "", tone: "grey" };
  const total = res.arms.reduce((n, a) => n + a.visitors, 0);
  if (total === 0) return { text: "No visitors yet. They join when a page shows them their version.", tone: "grey" };
  const what = goalText(goal.goal);
  if (r.winner && r.winner !== r.control) {
    const c = goal.comparisons?.find((x) => x.variant === r.winner);
    if (c) return { text: `${names(r.winner)} is ${chance(c.chance_to_beat)} likely to beat ${names(r.control)}: ${signed(c.lift)} ${what} (range ${signed(c.lift_low)} to ${signed(c.lift_high)}). Safe to stop.`, tone: "green" };
  }
  if (r.winner === r.control) return { text: `${names(r.control)} is doing better than every other version on ${what}. Safe to stop and keep it.`, tone: "amber" };
  const best = [...(goal.comparisons ?? [])].sort((a, b) => b.chance_to_beat - a.chance_to_beat)[0];
  if (!best) return { text: "", tone: "grey" };
  return {
    text: `No clear winner yet. ${names(best.variant)} is ${chance(best.chance_to_beat)} likely to beat ${names(r.control)} on ${what}, but there aren't enough visitors yet to be sure it isn't luck: keep it running until a version is safe to stop.`,
    tone: "grey",
  };
}

function TestSheet({ ws, test, flag, canManage, onClose, onChanged }: { ws: string; test: ABTest; flag?: Flag; canManage: boolean; onClose: () => void; onChanged: () => void }) {
  const events = useEvents(ws);
  const [results, setResults] = useState<ABTestResults | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [goals, setGoals] = useState<ABTestGoal[]>(test.goals);
  const [minEffect, setMinEffect] = useState(test.min_effect);
  const [decision, setDecision] = useState("");

  useEffect(() => {
    setResults(null);
    setError("");
    setGoals(test.goals);
    setMinEffect(test.min_effect);
    if (test.status === "draft") return;
    api.GET("/v1/workspaces/{ws}/ab-tests/{test}/results", { params: { path: { ws, test: test.id } } }).then(({ data, error }) => {
      if (error) setError(errorMessage(error));
      else {
        setResults(data);
        setDecision(data.winner);
      }
    });
  }, [ws, test.id, test.status]); // eslint-disable-line react-hooks/exhaustive-deps

  async function start() {
    setBusy(true);
    const changed = JSON.stringify(goals) !== JSON.stringify(test.goals) || minEffect !== test.min_effect;
    if (changed) {
      const { error } = await api.PATCH("/v1/workspaces/{ws}/ab-tests/{test}", { params: { path: { ws, test: test.id } }, body: { goals, min_effect: minEffect } });
      if (error) {
        setBusy(false);
        return setError(errorMessage(error));
      }
    }
    const off = flag?.status === "off";
    if (!(await confirmAction(`Start ${test.name}?`, `Visitors who are shown ${test.flag_key} from now on join the test.${off ? " The flag is off, so starting turns it on." : ""} The control and goals can't change after this.`, "Start test"))) {
      setBusy(false);
      return onChanged();
    }
    const { error } = await api.POST("/v1/workspaces/{ws}/ab-tests/{test}/start", { params: { path: { ws, test: test.id } } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    toast(`${test.name} is running.`);
    onChanged();
  }

  async function end() {
    const kept = decision ? `You keep ${decision}; change the flag to show it to everyone.` : "You keep no version.";
    if (!(await confirmAction(`End ${test.name}?`, `The results are kept as they are now. ${kept} The flag stays as it is.`, "End test"))) return;
    setBusy(true);
    const { error } = await api.POST("/v1/workspaces/{ws}/ab-tests/{test}/end", { params: { path: { ws, test: test.id } }, body: { decision } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    toast(`Ended ${test.name}.`);
    onChanged();
  }

  const names = (v: string) => (v === test.control ? `${v} (control)` : v);
  const variants = flag?.variants.map((v) => v.key) ?? results?.result.arms.map((a) => a.variant) ?? [];
  const v = results ? verdict(results, (x) => x) : null;

  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-2xl">
        <SheetHeader>
          <SheetTitle>{test.name} <Badge tone={statusTone[test.status]}>{statusText[test.status]}</Badge></SheetTitle>
          <SheetDescription>
            Flag <span className="font-mono">{test.flag_key}</span>, compared with <span className="font-mono">{test.control}</span>.
            {test.started_at && ` Started ${ago(test.started_at)}.`}
            {test.ended_at && ` Ended ${ago(test.ended_at)}${test.decision ? `, kept ${test.decision}` : ", kept no version"}.`}
          </SheetDescription>
        </SheetHeader>
        <div className="space-y-5 px-4 pb-6 text-[13px]">
          {test.hypothesis && <p className="text-muted-foreground italic">{test.hypothesis}</p>}
          <ErrorText>{error}</ErrorText>

          {test.status === "draft" ? (
            <>
              <div className="space-y-2">
                <span className="block font-medium">Goals</span>
                {canManage ? <GoalsEditor goals={goals} setGoals={setGoals} events={events} /> : <p className="font-mono">{test.goals.map(goalText).join(", ")}</p>}
              </div>
              {canManage ? <EffectPicker value={minEffect} onChange={setMinEffect} /> : <p>Tuned to find changes of {signed(test.min_effect)} or more.</p>}
              <p className="text-muted-foreground">Make sure the pages read the flag (<span className="font-mono">outflare.flag("{test.flag_key}")</span>, or <span className="font-mono">data-flags</span> and its class), so visitors join when they see their version.</p>
              {canManage && (
                <div className="flex gap-2 border-t pt-4">
                  <Button disabled={busy} onClick={start}>{busy ? "Starting…" : "Start test"}</Button>
                </div>
              )}
            </>
          ) : results === null ? (
            !error && <Loading />
          ) : (
            <>
              {v && v.text && <div className={`rounded-md border px-3 py-2 ${toneClass[v.tone]}`}>{v.text}</div>}
              {results.result.goals.map((g, gi) => (
                <Card key={gi} title={gi === 0 ? `Judged on ${goalText(g.goal)}` : goalText(g.goal)}>
                  <GoalTable arms={g.arms} comparisons={g.comparisons ?? []} control={test.control} names={names} />
                </Card>
              ))}
              <p className="text-[12px] text-muted-foreground">
                Visitors split {results.result.arms.map((a) => `${a.variant} ${a.visitors.toLocaleString()}`).join(" · ")}
                {results.result.sample_ratio_mismatch ? " — further from the flag's weights than luck explains." : " — in line with the flag's weights."}{" "}
                Tuned to find changes of {signed(results.result.min_effect)} or more.{" "}
                {test.status === "running" ? `Counted ${ago(results.result.computed_at)}; reload for the latest.` : "As they stood when the test ended."}
              </p>
              {test.status === "running" && canManage && (
                <div className="flex flex-wrap items-center gap-2 border-t pt-4">
                  <label className="flex items-center gap-2">
                    <span>Keep</span>
                    <select className={field} value={decision} onChange={(e) => setDecision(e.target.value)}>
                      <option value="">No version</option>
                      {variants.map((k) => <option key={k} value={k}>{k}{k === results.winner ? " (winner)" : ""}</option>)}
                    </select>
                  </label>
                  <Button variant="ghost" disabled={busy} onClick={end}>{busy ? "Ending…" : "End test"}</Button>
                </div>
              )}
            </>
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}

const toneClass: Record<keyof typeof tones, string> = {
  green: "border-green-600/25 bg-green-500/10",
  grey: "border-border bg-muted/40",
  amber: "border-amber-600/30 bg-amber-500/10",
  red: "border-red-600/25 bg-red-500/10 text-red-700 dark:text-red-400",
  blue: "border-blue-600/25 bg-blue-500/10",
};

function GoalTable({ arms, comparisons, control, names }: { arms: ABTestResults["result"]["arms"]; comparisons: ABTestComparison[]; control: string; names: (v: string) => string }) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-[13px] tabular-nums">
        <thead className="text-left text-[12px] text-muted-foreground">
          <tr>
            <th className="py-1 pr-3 font-normal">Version</th>
            <th className="py-1 pr-3 text-right font-normal">Visitors</th>
            <th className="py-1 pr-3 text-right font-normal">Converted</th>
            <th className="py-1 pr-3 text-right font-normal">Rate</th>
            <th className="py-1 pr-3 text-right font-normal">Chance to beat</th>
            <th className="py-1 pr-3 text-right font-normal">Lift (95% range)</th>
            <th className="py-1 font-normal"></th>
          </tr>
        </thead>
        <tbody>
          {arms.map((a) => {
            const c = comparisons.find((x) => x.variant === a.variant);
            const rate = a.visitors ? a.conversions / a.visitors : 0;
            return (
              <tr key={a.variant} className="border-t">
                <td className="py-1.5 pr-3 font-mono">{names(a.variant)}</td>
                <td className="py-1.5 pr-3 text-right">{a.visitors.toLocaleString()}</td>
                <td className="py-1.5 pr-3 text-right">{a.conversions.toLocaleString()}</td>
                <td className="py-1.5 pr-3 text-right">{pct(rate)}</td>
                <td className="py-1.5 pr-3 text-right">{a.variant === control || !c ? "—" : chance(c.chance_to_beat)}</td>
                <td className="py-1.5 pr-3 text-right">{a.variant === control || !c || (c.lift === 0 && c.lift_low === 0) ? "—" : `${signed(c.lift)} (${signed(c.lift_low)} to ${signed(c.lift_high)})`}</td>
                <td className="py-1.5">{c && c.p_value < 0.05 && <Badge tone={c.lift >= 0 ? "green" : "amber"} title={`Always-valid p-value ${c.p_value.toFixed(3)}: safe to stop, however often you looked.`}>Safe to stop</Badge>}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
