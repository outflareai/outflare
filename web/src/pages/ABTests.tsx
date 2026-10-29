// A/B tests: which version of a feature flag does best at a goal. A test
// names the flag, its control and its goals; Outflare counts visitors who
// were shown each version and how many of them converted, and says which
// version wins, in plain words.

import { useCallback, useEffect, useState, type FormEvent } from "react";
import type { ABTest, ABTestBreakdown, ABTestComparison, ABTestGoal, ABTestResults, Flag } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, confirmAction, Empty, ErrorText, field, Loading, PageHeader, toast, type tones } from "../ui";
import { ago } from "../format";
import { useBrand } from "@/lib/brand";
import { BrandBadge } from "@/components/brand-badge";
import { DataTable } from "@/components/data-table";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import type { Tab } from "@/lib/paths";
import { DayChart, LiftChart, measured, perVisitor, PlanPanel, SuggestPanel, SummaryPanel, VersionsPanel } from "./ABTestsAI";

type Props = { ws: string; canManage: boolean; go: (t: Tab) => void };

// A flag to start a test of, set by the Feature flags page before it opens
// this one.
let pendingFlag: string | null = null;
export function testFlag(key: string) {
  pendingFlag = key;
}

const statusTone: Record<ABTest["status"], keyof typeof tones> = { draft: "grey", running: "blue", done: "green" };
const statusText: Record<ABTest["status"], string> = { draft: "Draft", running: "Running", done: "Done" };

// goalText names a goal and what it measures per visitor.
export function goalText(g: ABTestGoal): string {
  const what = g.kind === "event" ? g.name ?? "" : g.kind === "page" ? `Viewed ${g.path}` : "Won deals";
  switch (g.metric) {
    case "count":
      return `${what} (times per visitor)`;
    case "value":
      return g.kind === "deal" ? "Deal value per visitor" : `${g.property} per visitor (${what})`;
  }
  return what;
}

const pct = (n: number, digits = 1) => `${(n * 100).toFixed(digits)}%`;
// A chance never reads as certain: 99.96% is "over 99%".
const chance = (n: number) => (n > 0.99 ? "over 99%" : n < 0.01 ? "under 1%" : pct(n, 0));
const signed = (n: number) => `${n >= 0 ? "+" : "−"}${Math.abs(Math.round(n * 100))}%`;

export function ABTests({ ws, canManage, go }: Props) {
  const { query } = useBrand(ws);
  const [tests, setTests] = useState<ABTest[] | null>(null);
  const [flags, setFlags] = useState<Flag[]>([]);
  const [open, setOpen] = useState<ABTest | null>(null);
  const [creating, setCreating] = useState<{ flag: string; seed?: Seed } | null>(null);
  const [suggesting, setSuggesting] = useState(false);
  const [error, setError] = useState("");

  const loadFlags = useCallback(() => {
    api.GET("/v1/workspaces/{ws}/flags", { params: { path: { ws }, query } }).then(({ data }) => setFlags(data?.data ?? []));
  }, [ws, query.brand]); // eslint-disable-line react-hooks/exhaustive-deps
  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/ab-tests", { params: { path: { ws }, query } });
    if (error) setError(errorMessage(error));
    setTests(data?.data ?? []);
    setOpen((cur) => (cur ? data?.data.find((t) => t.id === cur.id) ?? null : null));
  }, [ws, query.brand]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    load();
    loadFlags();
    if (pendingFlag !== null && canManage) setCreating({ flag: pendingFlag });
    pendingFlag = null;
  }, [load, loadFlags, canManage]);

  const splits = flags.filter((f) => f.variants.length > 1);
  const newTest = () => setCreating({ flag: "" });

  return (
    <div className="space-y-6">
      <PageHeader
        title="A/B tests"
        description="Pick a feature flag with two or more versions, say what counts as success, and see which version wins. The numbers stay honest however often you check."
        action={canManage && (
          <div className="flex gap-2">
            <Button variant="ghost" onClick={() => setSuggesting(true)}>Suggest tests</Button>
            <Button onClick={newTest}>New test</Button>
          </div>
        )}
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
      {open && <TestSheet ws={ws} test={open} flag={flags.find((f) => f.id === open.flag_id)} canManage={canManage} onClose={() => setOpen(null)} onChanged={load} onFlagChanged={loadFlags} />}
      {creating !== null && (
        <NewTestSheet ws={ws} flags={splits} flag={creating.flag} seed={creating.seed} go={go} onClose={() => setCreating(null)} onSaved={(t) => { setCreating(null); load(); setOpen(t); }} onFlagChanged={loadFlags} />
      )}
      {suggesting && (
        <Sheet open onOpenChange={(o) => !o && setSuggesting(false)}>
          <SheetContent className="w-full overflow-y-auto sm:max-w-xl">
            <SheetHeader>
              <SheetTitle>Suggest tests</SheetTitle>
              <SheetDescription>Ideas from your own AI model, with how long each would take on your traffic.</SheetDescription>
            </SheetHeader>
            <div className="px-4 pb-6">
              <SuggestPanel ws={ws} onUse={(seed) => { setSuggesting(false); setCreating({ flag: "", seed }); }} />
            </div>
          </SheetContent>
        </Sheet>
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

function GoalsEditor({ goals, setGoals, events, guardrail }: { goals: ABTestGoal[]; setGoals: (g: ABTestGoal[]) => void; events: string[]; guardrail?: boolean }) {
  const set = (i: number, g: ABTestGoal) => setGoals(goals.map((x, k) => (k === i ? g : x)));
  return (
    <div className="space-y-2">
      <datalist id="ab-events">{events.map((e) => <option key={e} value={e} />)}</datalist>
      {goals.map((g, i) => (
        <div key={i} className="flex flex-wrap items-center gap-2 rounded-md border p-2">
          <span className="w-16 text-[12px] text-muted-foreground">{guardrail ? "Guard" : i === 0 ? "Judged on" : "Also"}</span>
          <select aria-label="Kind" className={field} value={g.kind} onChange={(e) => set(i, e.target.value === "page" ? { kind: "page", path: "/" } : e.target.value === "deal" ? { kind: "deal", metric: "value" } : { kind: "event", name: "" })}>
            <option value="event">Event</option>
            <option value="page">Page viewed</option>
            <option value="deal">Won deal</option>
          </select>
          {g.kind === "event" ? (
            <input aria-label="Event" list="ab-events" className={`${field} min-w-40 flex-1 font-mono`} required pattern="[a-z][a-z0-9_]*(\.[a-z0-9_]+)*" placeholder="signup_started" value={g.name ?? ""} onChange={(e) => set(i, { ...g, name: e.target.value.trim() })} />
          ) : g.kind === "page" ? (
            <input aria-label="Path" className={`${field} min-w-40 flex-1 font-mono`} required pattern="/.*" placeholder="/thanks or /docs/*" value={g.path ?? ""} onChange={(e) => set(i, { ...g, path: e.target.value.trim() })} />
          ) : (
            <span className="flex-1 text-[12px] text-muted-foreground">of the person the visitor's browser belongs to</span>
          )}
          <select aria-label="Measure" className={field} value={g.metric ?? "conversion"}
            onChange={(e) => set(i, { ...g, metric: e.target.value as ABTestGoal["metric"], property: e.target.value === "value" && g.kind === "event" ? g.property ?? "revenue" : undefined, cap: e.target.value === "conversion" ? undefined : g.cap })}>
            <option value="conversion">{g.kind === "deal" ? "Won one" : "Reached it"}</option>
            {g.kind !== "deal" && <option value="count">Times per visitor</option>}
            {g.kind !== "page" && <option value="value">Value per visitor</option>}
          </select>
          {g.kind === "event" && g.metric === "value" && (
            <input aria-label="Property" className={`${field} w-28 font-mono`} required pattern="[A-Za-z_][A-Za-z0-9_]*" placeholder="revenue" value={g.property ?? ""} onChange={(e) => set(i, { ...g, property: e.target.value.trim() })} />
          )}
          {guardrail && (
            <select aria-label="Better" className={field} value={g.better ?? "higher"} onChange={(e) => set(i, { ...g, better: e.target.value === "lower" ? "lower" : undefined })}>
              <option value="higher">Higher is better</option>
              <option value="lower">Lower is better</option>
            </select>
          )}
          {(guardrail || goals.length > 1) && <Button type="button" variant="ghost" onClick={() => setGoals(goals.filter((_, k) => k !== i))}>Remove</Button>}
          <details className="basis-full text-[12px] text-muted-foreground">
            <summary className="cursor-pointer">Window{measured(g) ? " and cap" : ""}{g.window_hours ? ` · ${g.window_hours} h` : ""}{g.cap ? ` · cap ${g.cap}` : ""}</summary>
            <div className="mt-2 flex flex-wrap items-center gap-3">
              <label className="flex items-center gap-2">
                Count only within
                <input aria-label="Window in hours" type="number" min={0} max={8784} className={`${field} w-20`} value={g.window_hours ?? ""} placeholder="any"
                  onChange={(e) => set(i, { ...g, window_hours: e.target.value ? Number(e.target.value) : undefined })} />
                hours of seeing the version
              </label>
              {measured(g) && (
                <label className="flex items-center gap-2">
                  Cap each visitor at
                  <input aria-label="Cap per visitor" type="number" min={0} step="any" className={`${field} w-24`} value={g.cap ?? ""} placeholder="no cap"
                    onChange={(e) => set(i, { ...g, cap: e.target.value ? Number(e.target.value) : undefined })} />
                </label>
              )}
            </div>
          </details>
        </div>
      ))}
      {goals.length < 5 && (
        <Button type="button" variant="ghost" onClick={() => setGoals([...goals, guardrail ? { kind: "event", name: "", better: "lower" } : { kind: "event", name: "" }])}>
          {guardrail ? "Add a guardrail" : "Watch another goal"}
        </Button>
      )}
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

type Seed = { name: string; hypothesis: string; goal: ABTestGoal };

function NewTestSheet({ ws, flags, flag: initial, seed, go, onClose, onSaved, onFlagChanged }: { ws: string; flags: Flag[]; flag: string; seed?: Seed; go: (t: Tab) => void; onClose: () => void; onSaved: (t: ABTest) => void; onFlagChanged: () => void }) {
  const events = useEvents(ws);
  const [flagKey, setFlagKey] = useState(initial || flags[0]?.key || "");
  const flag = flags.find((f) => f.key === flagKey);
  const [control, setControl] = useState("");
  const [name, setName] = useState(seed?.name ?? "");
  const [hypothesis, setHypothesis] = useState(seed?.hypothesis ?? "");
  const [goals, setGoals] = useState<ABTestGoal[]>([seed?.goal ?? { kind: "event", name: "" }]);
  const [guardrails, setGuardrails] = useState<ABTestGoal[]>([]);
  const [minEffect, setMinEffect] = useState(0.25);
  const [writing, setWriting] = useState(false);
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
    const { data, error } = await api.POST("/v1/workspaces/{ws}/ab-tests", { params: { path: { ws } }, body: { flag: flagKey, control: ctl, name: name.trim() || undefined, hypothesis, goals, guardrails, min_effect: minEffect } });
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
            {flag && (writing ? (
              <div className="space-y-2 rounded-md border p-3">
                <span className="block font-medium">New versions with AI</span>
                <VersionsPanel ws={ws} flag={flag} onAdded={onFlagChanged} />
              </div>
            ) : (
              <Button type="button" variant="ghost" onClick={() => setWriting(true)}>Write more versions with AI</Button>
            ))}
            <div className="space-y-2">
              <span className="block font-medium">Goals</span>
              <GoalsEditor goals={goals} setGoals={setGoals} events={events} />
              <p className="text-[12px] text-muted-foreground">A visitor converts when they reach a goal after seeing their version; measure times or value per visitor to compare averages, such as revenue per visitor. The first goal decides the winner; the rest are exploratory. Goals can't change once the test starts.</p>
            </div>
            <div className="space-y-2">
              <span className="block font-medium">Guardrails <span className="font-normal text-muted-foreground">(optional)</span></span>
              <GoalsEditor goals={guardrails} setGoals={setGuardrails} events={events} guardrail />
              <p className="text-[12px] text-muted-foreground">Metrics a winner mustn't hurt, such as errors or refunds. You get an alert when a version clearly makes one worse; they never decide the winner.</p>
            </div>
            <EffectPicker value={minEffect} onChange={setMinEffect} />
            <PlanPanel ws={ws} flag={flagKey} goal={goals[0]} minEffect={minEffect} />
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

function TestSheet({ ws, test, flag, canManage, onClose, onChanged, onFlagChanged }: { ws: string; test: ABTest; flag?: Flag; canManage: boolean; onClose: () => void; onChanged: () => void; onFlagChanged: () => void }) {
  const events = useEvents(ws);
  const [results, setResults] = useState<ABTestResults | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [goals, setGoals] = useState<ABTestGoal[]>(test.goals);
  const [guardrails, setGuardrails] = useState<ABTestGoal[]>(test.guardrails);
  const [minEffect, setMinEffect] = useState(test.min_effect);
  const [decision, setDecision] = useState("");

  useEffect(() => {
    setResults(null);
    setError("");
    setGoals(test.goals);
    setGuardrails(test.guardrails);
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
    const changed = JSON.stringify(goals) !== JSON.stringify(test.goals) || JSON.stringify(guardrails) !== JSON.stringify(test.guardrails) || minEffect !== test.min_effect;
    if (changed) {
      const { error } = await api.PATCH("/v1/workspaces/{ws}/ab-tests/{test}", { params: { path: { ws, test: test.id } }, body: { goals, guardrails, min_effect: minEffect } });
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

  async function freeze(frozen: boolean) {
    if (frozen && !(await confirmAction(`Freeze ${test.name}?`, "New visitors stop joining; those already in keep counting, so the test can finish on the visitors it has. You can unfreeze it.", "Freeze"))) return;
    setBusy(true);
    const { error } = await api.POST("/v1/workspaces/{ws}/ab-tests/{test}/freeze", { params: { path: { ws, test: test.id } }, body: { frozen } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    toast(frozen ? `${test.name} is frozen: no new visitors join.` : `${test.name} lets new visitors join again.`);
    onChanged();
  }

  async function ship(variant: string) {
    const what = variant === test.control ? `${variant}, the control,` : variant;
    if (!(await confirmAction(`Ship ${variant}?`, `Everyone sees ${what} from now on: ${test.flag_key} gives it all the traffic.${test.status === "running" ? " The test ends, keeping its results." : ""}`, `Ship ${variant}`))) return;
    setBusy(true);
    const { error } = await api.POST("/v1/workspaces/{ws}/ab-tests/{test}/ship", { params: { path: { ws, test: test.id } }, body: { variant } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    toast(`Shipped ${variant}.`);
    onFlagChanged();
    onChanged();
  }

  async function duplicate() {
    setBusy(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/ab-tests/{test}/duplicate", { params: { path: { ws, test: test.id } } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    toast(`Added ${data.name} as a draft.`);
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
            {test.frozen_at && test.status === "running" && ` Frozen ${ago(test.frozen_at)}: no new visitors join.`}
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
              {(canManage || test.guardrails.length > 0) && (
                <div className="space-y-2">
                  <span className="block font-medium">Guardrails</span>
                  {canManage ? <GoalsEditor goals={guardrails} setGoals={setGuardrails} events={events} guardrail /> : <p className="font-mono">{test.guardrails.map(goalText).join(", ")}</p>}
                </div>
              )}
              {canManage ? <EffectPicker value={minEffect} onChange={setMinEffect} /> : <p>Tuned to find changes of {signed(test.min_effect)} or more.</p>}
              <PlanPanel ws={ws} flag={test.flag_id} goal={goals[0]} minEffect={minEffect} />
              {canManage && flag && (
                <div className="space-y-2 rounded-md border p-3">
                  <span className="block font-medium">New versions with AI</span>
                  <VersionsPanel ws={ws} flag={flag} onAdded={onFlagChanged} />
                </div>
              )}
              <ChecklistPanel ws={ws} testID={test.id} deps={JSON.stringify([goals, guardrails, minEffect])} />
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
              {canManage && <SummaryPanel ws={ws} testID={test.id} />}
              {results.result.goals[0]?.days && results.result.goals[0].days.length > 1 && (
                <Card title={`${goalText(results.result.goals[0].goal)}${measured(results.result.goals[0].goal) ? "" : " rate"}, day by day`}>
                  <DayChart days={results.result.goals[0].days} control={test.control} goal={results.result.goals[0].goal} />
                </Card>
              )}
              {results.result.goals[0]?.days && results.result.goals[0].days.length > 1 && (
                <Card title="Lift over the control, day by day">
                  <LiftChart days={results.result.goals[0].days} control={test.control} goal={results.result.goals[0].goal} />
                </Card>
              )}
              {results.result.goals.map((g, gi) => (
                <Card key={gi} title={gi === 0 ? `Judged on ${goalText(g.goal)}` : `${goalText(g.goal)} · exploratory`}>
                  <GoalTable goal={g.goal} arms={g.arms} comparisons={g.comparisons ?? []} control={test.control} names={names} />
                </Card>
              ))}
              {results.result.guardrails.map((g, gi) => (
                <Card key={`guard-${gi}`} title={`Guardrail: ${goalText(g.goal)} (${g.goal.better === "lower" ? "lower" : "higher"} is better)`}>
                  {g.harmed.length > 0 && (
                    <div className={`mb-2 rounded-md border px-3 py-2 ${toneClass.amber}`}>{g.harmed.map(names).join(", ")} clearly {g.harmed.length === 1 ? "makes" : "make"} this worse. Weigh that before shipping.</div>
                  )}
                  <GoalTable goal={g.goal} arms={g.arms} comparisons={g.comparisons ?? []} control={test.control} names={names} />
                </Card>
              ))}
              <BreakdownPanel ws={ws} testID={test.id} control={test.control} names={names} />
              <p className="text-[12px] text-muted-foreground">
                Visitors split {results.result.arms.map((a) => `${a.variant} ${a.visitors.toLocaleString()}`).join(" · ")}
                {results.result.sample_ratio_mismatch ? " — further from the flag's weights than luck explains." : " — in line with the flag's weights."}{" "}
                Tuned to find changes of {signed(results.result.min_effect)} or more.{" "}
                {results.result.corrected_for > 1 && `${results.result.corrected_for} versions compete with the control, so the results are corrected for that many chances of a false winner. `}
                {results.result.crossovers > 0 && `${results.result.crossovers.toLocaleString()} visitors saw more than one version (they count under the first)${results.result.crossovers * 100 > results.result.arms.reduce((n, a) => n + a.visitors, 0) ? ": more than 1%, so check nothing (a cache, a server) shows versions inconsistently" : ""}. `}
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
                  <Button variant="ghost" disabled={busy} onClick={() => freeze(!test.frozen_at)}>{test.frozen_at ? "Let new visitors join" : "Freeze"}</Button>
                </div>
              )}
              {canManage && (
                <div className="flex flex-wrap gap-2">
                  {results.winner && <Button disabled={busy} onClick={() => ship(results.winner)}>Ship {results.winner} to everyone</Button>}
                  {test.status === "done" && !results.winner && test.decision && <Button variant="ghost" disabled={busy} onClick={() => ship(test.decision)}>Ship {test.decision} to everyone</Button>}
                  <Button variant="ghost" disabled={busy} onClick={duplicate}>Duplicate as a new draft</Button>
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

function GoalTable({ goal, arms, comparisons, control, names }: { goal: ABTestGoal; arms: ABTestResults["result"]["arms"]; comparisons: ABTestComparison[]; control: string; names: (v: string) => string }) {
  const mean = measured(goal);
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-[13px] tabular-nums">
        <thead className="text-left text-[12px] text-muted-foreground">
          <tr>
            <th className="py-1 pr-3 font-normal">Version</th>
            <th className="py-1 pr-3 text-right font-normal">Visitors</th>
            <th className="py-1 pr-3 text-right font-normal">{mean ? "Reached it" : "Converted"}</th>
            <th className="py-1 pr-3 text-right font-normal">{mean ? "Per visitor" : "Rate"}</th>
            <th className="py-1 pr-3 text-right font-normal">Chance to beat</th>
            <th className="py-1 pr-3 text-right font-normal" title="The range stays honest however often you look, and leaves out 0 exactly when the result is safe to stop.">Lift (95% range)</th>
            <th className="py-1 font-normal"></th>
          </tr>
        </thead>
        <tbody>
          {arms.map((a, _i, all) => {
            // With no conversions anywhere, a chance is only the prior.
            const noneConverted = all.every((x) => x.conversions === 0);
            const c = comparisons.find((x) => x.variant === a.variant);
            const rate = a.visitors ? a.conversions / a.visitors : 0;
            return (
              <tr key={a.variant} className="border-t">
                <td className="py-1.5 pr-3 font-mono">{names(a.variant)}</td>
                <td className="py-1.5 pr-3 text-right">{a.visitors.toLocaleString()}</td>
                <td className="py-1.5 pr-3 text-right">{a.conversions.toLocaleString()}</td>
                <td className="py-1.5 pr-3 text-right">{mean ? perVisitor(goal, a.mean ?? 0) : pct(rate)}</td>
                <td className="py-1.5 pr-3 text-right">{a.variant === control || !c || noneConverted ? "—" : chance(c.chance_to_beat)}</td>
                <td className="py-1.5 pr-3 text-right">{a.variant === control || !c || a.visitors === 0 ? "—" : c.lift_low === 0 && c.lift_high === 0 ? `${signed(c.lift)} (range not yet)` : `${signed(c.lift)} (${signed(c.lift_low)} to ${signed(c.lift_high)})`}</td>
                <td className="py-1.5">{c && c.p_value < 0.05 && <Badge tone={c.lift >= 0 ? "green" : "amber"} title={`Always-valid p-value ${c.p_value.toFixed(3)}: safe to stop, however often you looked.`}>Safe to stop</Badge>}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

const dimensions = [
  { value: "device", label: "Device" },
  { value: "source", label: "Source" },
  { value: "country", label: "Country" },
  { value: "visitor", label: "New or returning" },
] as const;

// BreakdownPanel shows the first goal group by group, on demand: where a
// change works, corrected so a lucky group doesn't mislead.
function BreakdownPanel({ ws, testID, control, names }: { ws: string; testID: string; control: string; names: (v: string) => string }) {
  const [by, setBy] = useState<(typeof dimensions)[number]["value"] | "">("");
  const [data, setData] = useState<ABTestBreakdown | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    if (!by) return setData(null);
    let live = true;
    setData(null);
    api.GET("/v1/workspaces/{ws}/ab-tests/{test}/breakdown", { params: { path: { ws, test: testID }, query: { by } } }).then(({ data, error }) => {
      if (!live) return;
      setError(error ? errorMessage(error) : "");
      setData(data ?? null);
    });
    return () => {
      live = false;
    };
  }, [ws, testID, by]);
  const b = data?.breakdown;
  return (
    <Card title="Breakdown">
      <div className="space-y-3">
        <label className="flex items-center gap-2">
          <span>By</span>
          <select className={field} value={by} onChange={(e) => setBy(e.target.value as typeof by)}>
            <option value="">Choose…</option>
            {dimensions.map((d) => <option key={d.value} value={d.value}>{d.label}</option>)}
          </select>
        </label>
        {error && <ErrorText>{error}</ErrorText>}
        {by && !b && !error && <Loading />}
        {b && (
          <>
            <p className="text-[12px] text-muted-foreground">
              Each group is another chance of a lucky difference, so these are corrected for {b.corrected_for} comparisons and need more visitors to be safe to stop. Use them to learn where the change works; the winner is decided on everyone.
            </p>
            {b.segments.length === 0 && <p className="text-[12px] text-muted-foreground">No visitors yet.</p>}
            {b.segments.map((sg) => (
              <div key={sg.key} className="space-y-1">
                <div className="font-medium">{segmentLabel(by, sg.key)}</div>
                <GoalTable goal={b.goal} arms={sg.arms} comparisons={sg.comparisons ?? []} control={control} names={names} />
              </div>
            ))}
          </>
        )}
      </div>
    </Card>
  );
}

function segmentLabel(by: string, key: string): string {
  if (key === "unknown") return by === "visitor" ? "Unknown (no visit recorded)" : "Unknown";
  if (key === "other") return "Everything else";
  if (by === "visitor" || by === "device") return key.charAt(0).toUpperCase() + key.slice(1);
  if (by === "source" && key === "direct") return "Direct";
  return key;
}

const checkTone = { ok: "green", warn: "amber", fail: "red" } as const;

// ChecklistPanel says what would stop a draft measuring anything useful,
// read fresh when its goals change (the saved test's; deps only re-reads).
function ChecklistPanel({ ws, testID, deps }: { ws: string; testID: string; deps: string }) {
  const [checks, setChecks] = useState<{ id: string; status: "ok" | "warn" | "fail"; text: string }[] | null>(null);
  useEffect(() => {
    let live = true;
    api.GET("/v1/workspaces/{ws}/ab-tests/{test}/checklist", { params: { path: { ws, test: testID } } }).then(({ data }) => {
      if (live) setChecks(data?.checks ?? []);
    });
    return () => {
      live = false;
    };
  }, [ws, testID, deps]);
  if (!checks) return null;
  return (
    <div className="space-y-1 rounded-md border p-3">
      <span className="block font-medium">Before you start</span>
      {checks.map((c) => (
        <div key={c.id} className="flex items-start gap-2 text-[12px]">
          <Badge tone={checkTone[c.status]}>{c.status === "ok" ? "OK" : c.status === "warn" ? "Check" : "Fix"}</Badge>
          <span>{c.text}</span>
        </div>
      ))}
    </div>
  );
}

