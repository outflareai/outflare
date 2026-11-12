import { PickBrand } from "@/components/pick-brand";
import { pickedBrandId } from "@/lib/brand";
// A/B tests: which version of a feature flag does best at a goal. A test
// names the flag, its control and its goals; Outflare counts visitors who
// were shown each version and how many of them converted, and says which
// version wins, in plain words.
//
// Each test is a page of its own (/{ws}/{brand}/ab-tests/{slug}): a draft
// walks through its versions, goal and a last check before it starts; a
// started test has tabs for its results, versions, setup, activity and
// what it taught. Every tab has its own link (#versions and so on).

import { useCallback, useEffect, useState, type FormEvent, type ReactNode } from "react";
import type { ABTest, ABTestAIRun, ABTestBreakdown, ABTestComparison, ABTestGoal, ABTestPreviews, ABTestResults, Flag } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, confirmAction, Empty, ErrorText, field, Loading, PageHeader, toast, type tones } from "../ui";
import { ago, plural } from "../format";
import { useBrand } from "@/lib/brand";
import { BrandBadge } from "@/components/brand-badge";
import { DataTable } from "@/components/data-table";
import { isID, type Tab } from "@/lib/paths";
import { AIRunCard, DayChart, LearningsPanel, LiftChart, measured, perVisitor, PlanPanel, SuggestPanel, SummaryPanel, useAIRun } from "./ABTestsAI";
import { VersionsTab } from "./ABTestsPageAI";

type Go = (t: Tab, item?: string) => void;
type Props = { ws: string; canManage: boolean; go: Go; open?: string; setOpen: (item?: string) => void };

// A flag to start a test of, set by the Feature flags page before it opens
// this one.
let pendingFlag: string | null = null;
export function testFlag(key: string) {
  pendingFlag = key;
}
// An idea from Suggest tests that New test fills in.
let pendingSeed: Seed | null = null;

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

// useHashTab keeps which tab of a page is open in the URL's #, so each tab
// has a link of its own.
function useHashTab<T extends string>(tabs: readonly T[], first: T): [T, (t: T) => void] {
  const read = () => {
    const h = location.hash.slice(1) as T;
    return tabs.includes(h) ? h : first;
  };
  const [tab, setTab] = useState<T>(read);
  useEffect(() => {
    const on = () => setTab(read());
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, [tabs.join()]); // eslint-disable-line react-hooks/exhaustive-deps
  const set = (t: T) => {
    setTab(t);
    history.replaceState(history.state, "", `${location.pathname}${location.search}#${t}`);
    window.scrollTo({ top: 0 });
  };
  return [tab, set];
}

function TabBar<T extends string>({ tabs, tab, set, label }: { tabs: readonly (readonly [T, string])[]; tab: T; set: (t: T) => void; label: string }) {
  return (
    <div role="tablist" aria-label={label} className="mb-5 flex gap-1 overflow-x-auto border-b text-[13px]">
      {tabs.map(([k, name]) => (
        <button key={k} type="button" role="tab" aria-selected={tab === k} onClick={() => set(k)}
          className={`-mb-px whitespace-nowrap border-b-2 px-3 py-2 ${tab === k ? "border-foreground font-medium" : "border-transparent text-muted-foreground hover:text-foreground"}`}>
          {name}
        </button>
      ))}
    </div>
  );
}

// Steps shows where a test is in being set up: the page (done once the
// test exists), its versions, its goal, and the last check before it starts.
const steps = [["page", "Page"], ["versions", "Versions"], ["goal", "Goal"], ["review", "Review and start"]] as const;
type Step = (typeof steps)[number][0];
function Steps({ at, set }: { at: Step; set?: (s: Exclude<Step, "page">) => void }) {
  const n = steps.findIndex(([k]) => k === at);
  return (
    <ol className="mb-5 flex flex-wrap items-center gap-2 text-[13px]">
      {steps.map(([k, name], i) => {
        const can = !!set && k !== "page";
        return (
          <li key={k} className="flex items-center gap-2">
            {i > 0 && <span className="text-muted-foreground" aria-hidden>→</span>}
            <button type="button" disabled={!can} aria-current={i === n ? "step" : undefined} onClick={() => can && set!(k as Exclude<Step, "page">)}
              className={`flex items-center gap-1.5 rounded-full border px-2.5 py-1 ${i === n ? "border-foreground font-medium" : "text-muted-foreground"} ${can ? "hover:text-foreground" : ""}`}>
              <span className={`flex size-5 items-center justify-center rounded-full text-[11px] ${i < n ? "bg-green-600 text-white" : i === n ? "bg-foreground text-background" : "bg-muted"}`}>{i < n ? "✓" : i + 1}</span>
              {name}
            </button>
          </li>
        );
      })}
    </ol>
  );
}

// A/B tests belong to one brand (plans/brands/README.md): with all brands
// picked the page asks for one, and switching brands opens it afresh.
export function ABTests(props: Props) {
  const { brand, active } = useBrand(props.ws);
  if (!brand) return <PickBrand what="A/B tests" brands={active.length} />;
  return <ABTestsPage key={brand.id} {...props} />;
}

function ABTestsPage({ ws, canManage, go, open, setOpen }: Props) {
  const { query } = useBrand(ws);
  const [tests, setTests] = useState<ABTest[] | null>(null);
  const [flags, setFlags] = useState<Flag[]>([]);
  const [error, setError] = useState("");

  const loadFlags = useCallback(() => {
    api.GET("/v1/brands/{brand}/flags", { params: { path: { brand: pickedBrandId() } } }).then(({ data }) => setFlags(data?.data ?? []));
  }, [ws, query.brand]); // eslint-disable-line react-hooks/exhaustive-deps
  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/ab-tests", { params: { path: { brand: pickedBrandId() } } });
    if (error) setError(errorMessage(error));
    setTests(data?.data ?? []);
  }, [ws, query.brand]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    load();
    loadFlags();
    if (pendingFlag !== null && canManage) setOpen("new");
  }, [load, loadFlags, canManage]); // eslint-disable-line react-hooks/exhaustive-deps

  const splits = flags.filter((f) => f.variants.length > 1);
  if (open === "new" && canManage)
    return <NewTestPage ws={ws} flags={splits} go={go} onBack={() => setOpen(undefined)} onCreated={(t) => { load(); loadFlags(); history.replaceState(history.state, "", location.pathname.replace(/\/new\/?$/, "")); setOpen(t.id); }} />;
  if (open && isID(open))
    return <TestPage key={open} ws={ws} testID={open} flags={flags} canManage={canManage} go={go} onBack={() => { setOpen(undefined); load(); }} onOpen={setOpen} onFlagChanged={loadFlags} />;
  return <TestsList ws={ws} tests={tests} error={error} canManage={canManage} go={go} onOpen={setOpen} />;
}

// TestsList is the A/B tests page: every test, ideas from the AI, and what
// finished tests taught.
function TestsList({ ws, tests, error, canManage, go, onOpen }: { ws: string; tests: ABTest[] | null; error: string; canManage: boolean; go: Go; onOpen: (item?: string) => void }) {
  const [tab, setTab] = useHashTab(["tests", "ideas", "learnings"] as const, "tests");
  const newTest = () => onOpen("new");
  return (
    <div>
      <PageHeader
        title="A/B tests"
        description="Pick a page, change it for each version (by hand or by describing it to your AI), say what counts as success, and see which version wins. No code needed. The numbers stay honest however often you check."
        action={canManage && <Button onClick={newTest}>New test</Button>}
      />
      <TabBar label="A/B tests" tab={tab} set={setTab} tabs={[["tests", "Tests"], ...(canManage ? [["ideas", "Ideas"] as const] : []), ["learnings", "Learnings"]]} />
      <ErrorText>{error}</ErrorText>
      {tab === "ideas" && canManage ? (
        <div className="max-w-2xl">
          <SuggestPanel ws={ws} onUse={(seed) => { pendingSeed = seed; newTest(); }} />
        </div>
      ) : tab === "learnings" ? (
        <div className="max-w-3xl">
          <p className="mb-3 text-[13px] text-muted-foreground">What your finished tests taught. Your AI model writes each learning when a test ends.</p>
          <LearningsPanel ws={ws} canAsk={canManage} />
        </div>
      ) : tests === null ? (
        <Loading />
      ) : tests.length === 0 ? (
        <Empty title="No A/B tests yet" action={canManage && <Button onClick={newTest}>New test</Button>}>
          Pick a page and change it for half your visitors, such as a new headline. Describe the change to your AI model or click on the page to edit it. Developers can also test a <button className="underline underline-offset-2" onClick={() => go("flags")}>feature flag</button> their code reads.
        </Empty>
      ) : (
        <DataTable
          data={tests}
          rowId={(t) => t.id}
          searchPlaceholder="Search tests"
          onRowClick={(t) => onOpen(t.id)}
          mobile={(t) => (
            <div className="space-y-0.5">
              <div className="font-medium">{t.name} <Badge tone={statusTone[t.status]}>{statusText[t.status]}</Badge></div>
              <div className="text-[12px] text-muted-foreground">{t.page || t.flag_key} · {t.goals[0] ? goalText(t.goals[0]) : ""}</div>
            </div>
          )}
          columns={[
            {
              id: "name", header: "Test", accessorFn: (t) => `${t.name} ${t.flag_key} ${t.page}`,
              cell: ({ row: { original: t } }) => (
                <div>
                  <div className="font-medium">{t.name} <BrandBadge id={t.brand_id} /></div>
                  <div className="truncate font-mono text-[12px] text-muted-foreground">{t.page || t.flag_key}</div>
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
    </div>
  );
}

function useEvents(ws: string) {
  const { brand: picked, brands } = useBrand(ws);
  const brand = picked?.id ?? brands[0]?.id ?? "";
  const [events, setEvents] = useState<string[]>([]);
  useEffect(() => {
    if (!brand) return;
    api.GET("/v1/brands/{brand}/catalog/events", { params: { path: { brand }, query: { status: "approved" } } }).then(({ data }) =>
      // Page views are a page goal, and seeing a version is the test itself.
      setEvents((data?.data ?? []).map((e) => e.name).filter((n) => n !== "experiment_viewed" && n !== "page.viewed")),
    );
  }, [brand]);
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

// NewTestPage is the first step of a test: the page (or a developer's
// flag) and what counts as success. It makes a draft, and the test's own
// page carries on with the versions.
function NewTestPage({ ws, flags, go, onBack, onCreated }: { ws: string; flags: Flag[]; go: Go; onBack: () => void; onCreated: (t: ABTest) => void }) {
  const events = useEvents(ws);
  const [seed] = useState(() => {
    const s = pendingSeed;
    pendingSeed = null;
    return s;
  });
  const [initial] = useState(() => {
    const f = pendingFlag ?? "";
    pendingFlag = null;
    return f;
  });
  const [flagKey, setFlagKey] = useState(initial || flags[0]?.key || "");
  const flag = flags.find((f) => f.key === flagKey);
  const [control, setControl] = useState("");
  const [name, setName] = useState(seed?.name ?? "");
  const [hypothesis, setHypothesis] = useState(seed?.hypothesis ?? "");
  const [goals, setGoals] = useState<ABTestGoal[]>([seed?.goal ?? { kind: "event", name: "" }]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  // No code: test a page, and Outflare makes the flag. A flag: test one a
  // developer already made.
  const [mode, setMode] = useState<"page" | "flag">(initial ? "flag" : "page");
  const [page, setPage] = useState("");
  const [versions, setVersions] = useState(1);
  // A split URL test: each new version is another page visitors are sent to.
  const [split, setSplit] = useState(false);
  const [targets, setTargets] = useState<string[]>(["", "", "", ""]);

  useEffect(() => {
    if (!flagKey && flags[0]) setFlagKey(flags[0].key);
  }, [flags, flagKey]);
  const keys = flag?.variants.map((v) => v.key) ?? [];
  const ctl = keys.includes(control) ? control : keys.includes("control") ? "control" : keys[0] ?? "";

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    if (mode === "page") {
      if (!/^https?:\/\/[^/\s]+/i.test(page.trim())) {
        setBusy(false);
        return setError("Enter the address of the page to test, starting with https://.");
      }
      const redirects = split ? targets.slice(0, versions).map((t) => t.trim()) : undefined;
      if (redirects && redirects.some((t) => !/^(\/(?!\/)|https?:\/\/)\S+$/i.test(t))) {
        setBusy(false);
        return setError("Enter the page for each version, such as /pricing-b.");
      }
      const { data, error } = await api.POST("/v1/brands/{brand}/ab-tests", { params: { path: { brand: pickedBrandId() } }, body: { page: { url: page.trim(), versions, redirects }, name: name.trim() || undefined, hypothesis, goals } });
      setBusy(false);
      if (error) return setError(errorMessage(error));
      toast(`Added ${data!.name} as a draft.`);
      return onCreated(data!);
    }
    const { data, error } = await api.POST("/v1/brands/{brand}/ab-tests", { params: { path: { brand: pickedBrandId() } }, body: { flag: flagKey, control: ctl, name: name.trim() || undefined, hypothesis, goals } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    toast(`Added ${data!.name} as a draft.`);
    onCreated(data!);
  }

  return (
    <div>
      <PageHeader title="New A/B test" back={{ label: "A/B tests", onClick: onBack }} description="Start with the page and what counts as success. You make the versions next, and nothing changes for visitors until you start the test." />
      <Steps at="page" />
      <form onSubmit={submit} className="max-w-2xl space-y-6 text-[13px]">
        <div role="tablist" className="inline-flex overflow-hidden rounded-md border text-[13px]">
          {([["page", "Test a page"], ["flag", "Use a developer's flag"]] as const).map(([m, label]) => (
            <button key={m} type="button" role="tab" aria-selected={mode === m} className={`px-3 py-1.5 ${mode === m ? "bg-muted font-medium" : "text-muted-foreground"}`} onClick={() => setMode(m)}>{label}</button>
          ))}
        </div>
        {mode === "page" ? (
          <section className="space-y-4">
            <label className="block">
              <span className="mb-1 block font-medium">Which page?</span>
              <input className={`${field} w-full`} type="url" required autoFocus placeholder="https://northpeak.example/pricing" value={page} onChange={(e) => setPage(e.target.value)} />
              <span className="mt-1 block text-[12px] text-muted-foreground">The page visitors see the versions on. End it with * for every page under it, such as /blog/*. Your site only needs the Outflare tracking script.</span>
            </label>
            <label className="block">
              <span className="mb-1 block font-medium">How many new versions?</span>
              <select className={`${field} w-full`} value={versions} onChange={(e) => setVersions(Number(e.target.value))}>
                {[1, 2, 3, 4].map((n) => <option key={n} value={n}>{n === 1 ? "1 (an A/B test)" : `${n} (A/B/${"CDE".slice(0, n - 1).split("").join("/")})`}</option>)}
              </select>
              <span className="mt-1 block text-[12px] text-muted-foreground">Visitors are split evenly between today's page and each new version.</span>
            </label>
            <label className="flex items-start gap-2">
              <input type="checkbox" className="mt-0.5" checked={split} onChange={(e) => setSplit(e.target.checked)} />
              <span>
                <span className="block font-medium">The versions are other pages</span>
                <span className="block text-[12px] text-muted-foreground">A split URL test: visitors to the page above are sent to another page for their version. Pages must be on your own site.</span>
              </span>
            </label>
            {split && Array.from({ length: versions }, (_, i) => (
              <label key={i} className="block">
                <span className="mb-1 block font-medium">Version {String.fromCharCode(66 + i)} page</span>
                <input className={`${field} w-full`} required placeholder={`/pricing-${String.fromCharCode(98 + i)}`} value={targets[i]} onChange={(e) => setTargets((t) => t.map((x, j) => (j === i ? e.target.value : x)))} />
              </label>
            ))}
          </section>
        ) : flags.length === 0 ? (
          <p className="text-muted-foreground">No flag splits visitors between versions yet. <button type="button" className="underline underline-offset-2" onClick={() => go("flags")}>Add a flag</button> with two or more versions, or test a page instead.</p>
        ) : (
          <section className="space-y-4">
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
          </section>
        )}
        <section className="space-y-2">
          <span className="block font-medium">What counts as success?</span>
          <GoalsEditor goals={goals} setGoals={setGoals} events={events} />
          <p className="text-[12px] text-muted-foreground">A visitor converts when they do this after seeing their version, such as signing up. You can add more goals and guardrails in the next steps.</p>
        </section>
        <section className="grid gap-4 sm:grid-cols-2">
          <label className="block">
            <span className="mb-1 block font-medium">Name <span className="font-normal text-muted-foreground">(optional)</span></span>
            <input className={`${field} w-full`} maxLength={200} placeholder={mode === "page" ? "Pricing headline" : flag?.name} value={name} onChange={(e) => setName(e.target.value)} />
            <span className="mt-1 block text-[12px] text-muted-foreground">It names the test's link too.</span>
          </label>
          <label className="block">
            <span className="mb-1 block font-medium">Hypothesis <span className="font-normal text-muted-foreground">(optional)</span></span>
            <textarea className={`${field} w-full`} rows={2} maxLength={2000} placeholder="A shorter headline gets more signups, because…" value={hypothesis} onChange={(e) => setHypothesis(e.target.value)} />
          </label>
        </section>
        <ErrorText>{error}</ErrorText>
        <div className="flex gap-2 border-t pt-4">
          <Button disabled={busy || (mode === "flag" && !flagKey)}>{busy ? "Adding…" : "Next: versions"}</Button>
          <Button type="button" variant="ghost" onClick={onBack}>Cancel</Button>
        </div>
      </form>
    </div>
  );
}

const draftSteps = ["versions", "goal", "review"] as const;
const liveTabs = ["overview", "versions", "setup", "activity", "learnings"] as const;

// TestPage is one test's own page.
function TestPage({ ws, testID, flags, canManage, go, onBack, onOpen, onFlagChanged }: { ws: string; testID: string; flags: Flag[]; canManage: boolean; go: Go; onBack: () => void; onOpen: (item?: string) => void; onFlagChanged: () => void }) {
  const [test, setTest] = useState<ABTest | null>(null);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/ab-tests/{test}", { params: { path: { brand: pickedBrandId(), test: testID } } });
    if (error) setError(errorMessage(error));
    else setTest(data);
  }, [testID]);
  useEffect(() => {
    load();
  }, [load]);
  const flag = flags.find((f) => f.id === test?.flag_id);
  if (!test) return error ? <div><PageHeader title="A/B test" back={{ label: "A/B tests", onClick: onBack }} /><ErrorText>{error}</ErrorText></div> : <Loading />;
  const changed = () => {
    load();
    onFlagChanged();
  };
  const header = (
    <PageHeader
      back={{ label: "A/B tests", onClick: onBack }}
      title={<>{test.name} <Badge tone={statusTone[test.status]}>{statusText[test.status]}</Badge></>}
      description={
        <>
          {test.page ? <>On <a className="font-mono underline-offset-2 hover:underline" href={test.page.replace(/\*$/, "")} target="_blank" rel="noreferrer">{test.page}</a></> : <>Flag <span className="font-mono">{test.flag_key}</span></>}, compared with <span className="font-mono">{test.control}</span>.
          {test.started_at && ` Started ${ago(test.started_at)}.`}
          {test.frozen_at && test.status === "running" && ` Frozen ${ago(test.frozen_at)}: no new visitors join.`}
          {test.ended_at && ` Ended ${ago(test.ended_at)}${test.decision ? `, kept ${test.decision}` : ", kept no version"}.`}
        </>
      }
    />
  );
  return (
    <div>
      {header}
      {test.status === "draft" ? (
        <DraftTest ws={ws} test={test} flag={flag} canManage={canManage} go={go} onChanged={changed} />
      ) : (
        <LiveTest ws={ws} test={test} flag={flag} canManage={canManage} go={go} onChanged={changed} onOpen={onOpen} />
      )}
    </div>
  );
}

// DraftTest walks a draft through its versions, its goals and a last
// check, saving each step as you go.
function DraftTest({ ws, test, flag, canManage, go, onChanged }: { ws: string; test: ABTest; flag?: Flag; canManage: boolean; go: Go; onChanged: () => void }) {
  const events = useEvents(ws);
  const [step, setStep] = useHashTab(draftSteps, "versions");
  const [goals, setGoals] = useState<ABTestGoal[]>(test.goals);
  const [guardrails, setGuardrails] = useState<ABTestGoal[]>(test.guardrails);
  const [minEffect, setMinEffect] = useState(test.min_effect);
  const [hypothesis, setHypothesis] = useState(test.hypothesis);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [checked, setChecked] = useState(0);

  const dirty = JSON.stringify(goals) !== JSON.stringify(test.goals) || JSON.stringify(guardrails) !== JSON.stringify(test.guardrails) || minEffect !== test.min_effect || hypothesis !== test.hypothesis;
  // save keeps the goal step's changes; false when they couldn't be kept.
  async function save(): Promise<boolean> {
    if (!dirty || !canManage) return true;
    const { error } = await api.PATCH("/v1/brands/{brand}/ab-tests/{test}", { params: { path: { brand: pickedBrandId(), test: test.id } }, body: { goals, guardrails, min_effect: minEffect, hypothesis } });
    if (error) {
      setError(errorMessage(error));
      return false;
    }
    setError("");
    onChanged();
    return true;
  }
  const move = async (s: (typeof draftSteps)[number]) => {
    if (step === "goal" && !(await save())) return;
    setStep(s);
  };

  async function start() {
    setBusy(true);
    if (!(await save())) return setBusy(false);
    const off = flag?.status === "off";
    if (!(await confirmAction(`Start ${test.name}?`, `Visitors who are shown ${test.page || test.flag_key} from now on join the test.${off ? " Its flag is off, so starting turns it on." : ""} The goals can't change after this.`, "Start test"))) return setBusy(false);
    const { error } = await api.POST("/v1/brands/{brand}/ab-tests/{test}/start", { params: { path: { brand: pickedBrandId(), test: test.id } } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    toast(`${test.name} is running. Visitors show up on the overview as they arrive.`);
    history.replaceState(history.state, "", `${location.pathname}#overview`);
    onChanged();
  }

  const empty = flag?.variants.filter((v) => v.key !== test.control && !hasContent(v.payload)).map((v) => v.key) ?? [];
  return (
    <div>
      <Steps at={step} set={move} />
      <ErrorText>{error}</ErrorText>
      {step === "versions" && (
        <div className="space-y-4">
          <p className="max-w-3xl text-[13px] text-muted-foreground">{test.page ? "Make each new version: describe what to change and your AI model writes it, or edit the page itself. Today's page stays as it is." : "The versions are your flag's. Write more with AI, or change them under Feature flags."}</p>
          <VersionsTab ws={ws} test={test} flag={flag} canManage={canManage} onFlagChanged={onChanged} go={() => go("ai")} />
          <div className="flex gap-2 border-t pt-4"><Button onClick={() => move("goal")}>Next: goal</Button></div>
        </div>
      )}
      {step === "goal" && (
        <div className="max-w-2xl space-y-5 text-[13px]">
          <div className="space-y-2">
            <span className="block font-medium">Goals</span>
            {canManage ? <GoalsEditor goals={goals} setGoals={setGoals} events={events} /> : <p className="font-mono">{test.goals.map(goalText).join(", ")}</p>}
            <p className="text-[12px] text-muted-foreground">The first goal decides the winner; the rest are for learning. Measure times or value per visitor to compare averages, such as revenue per visitor.</p>
          </div>
          <div className="space-y-2">
            <span className="block font-medium">Guardrails <span className="font-normal text-muted-foreground">(optional)</span></span>
            {canManage ? <GoalsEditor goals={guardrails} setGoals={setGuardrails} events={events} guardrail /> : <p className="font-mono">{test.guardrails.map(goalText).join(", ") || "None"}</p>}
            <p className="text-[12px] text-muted-foreground">Things a winner mustn't make worse, such as errors or refunds. You get an alert when a version clearly does.</p>
          </div>
          {canManage ? <EffectPicker value={minEffect} onChange={setMinEffect} /> : <p>Tuned to find changes of {signed(test.min_effect)} or more.</p>}
          <PlanPanel ws={ws} flag={test.flag_id} goal={goals[0]} minEffect={minEffect} />
          <label className="block">
            <span className="mb-1 block font-medium">Hypothesis <span className="font-normal text-muted-foreground">(optional)</span></span>
            <textarea className={`${field} w-full`} rows={2} maxLength={2000} disabled={!canManage} placeholder="A shorter headline gets more signups, because…" value={hypothesis} onChange={(e) => setHypothesis(e.target.value)} />
          </label>
          <div className="flex gap-2 border-t pt-4">
            <Button onClick={() => move("review")}>Next: review</Button>
            <Button variant="ghost" onClick={() => move("versions")}>Back</Button>
            {dirty && <span className="self-center text-[12px] text-muted-foreground">Saved when you move on.</span>}
          </div>
        </div>
      )}
      {step === "review" && (
        <div className="max-w-3xl space-y-4 text-[13px]">
          <Card title="What you're testing">
            <ul className="space-y-1">
              <li>{flag ? plural(flag.variants.length - 1, "new version") : "Versions"} against today's page ({test.control}){empty.length > 0 && <span className="text-amber-700 dark:text-amber-400">: {empty.join(", ")} {empty.length === 1 ? "has" : "have"} no changes yet</span>}.</li>
              <li>Judged on <span className="font-mono">{test.goals[0] ? goalText(test.goals[0]) : "no goal"}</span>{test.goals.length > 1 && `, also watching ${plural(test.goals.length - 1, "goal")}`}{test.guardrails.length > 0 && `, with ${plural(test.guardrails.length, "guardrail")}`}.</li>
              <li>Tuned to find changes of {signed(test.min_effect)} or more.</li>
            </ul>
          </Card>
          <PreviewPanel ws={ws} testID={test.id} canManage={canManage} onChecked={() => setChecked((n) => n + 1)} />
          <ChecklistPanel ws={ws} testID={test.id} deps={JSON.stringify([test.updated_at, checked])} />
          {!test.page && <p className="text-muted-foreground">For versions shown by your own code, make sure the pages read the flag (<span className="font-mono">outflare.flag("{test.flag_key}")</span>, or <span className="font-mono">data-flags</span> and its class).</p>}
          {canManage && (
            <div className="flex gap-2 border-t pt-4">
              <Button disabled={busy} onClick={start}>{busy ? "Starting…" : "Start test"}</Button>
              <Button variant="ghost" onClick={() => move("goal")}>Back</Button>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

const hasContent = (p: unknown) => {
  if (!p || typeof p !== "object" || Array.isArray(p)) return !!p;
  const o = p as { changes?: unknown[]; css?: string; redirect?: string };
  return (o.changes?.length ?? 0) > 0 || !!o.css || !!o.redirect || Object.keys(o).some((k) => !["changes", "css", "redirect"].includes(k));
};

// LiveTest is a started or finished test: its results first, then its
// versions, setup, what happened and what it taught.
function LiveTest({ ws, test, flag, canManage, go, onChanged, onOpen }: { ws: string; test: ABTest; flag?: Flag; canManage: boolean; go: Go; onChanged: () => void; onOpen: (item?: string) => void }) {
  const [tab, setTab] = useHashTab(liveTabs, "overview");
  const [results, setResults] = useState<ABTestResults | null>(null);
  const [error, setError] = useState("");
  const loadResults = useCallback(() => {
    api.GET("/v1/brands/{brand}/ab-tests/{test}/results", { params: { path: { brand: pickedBrandId(), test: test.id } } }).then(({ data, error }) => {
      if (error) setError(errorMessage(error));
      else setResults(data);
    });
  }, [test.id]);
  useEffect(() => loadResults(), [loadResults, test.status]);
  return (
    <div>
      <TabBar label="Test" tab={tab} set={setTab} tabs={[["overview", "Overview"], ["versions", "Versions"], ["setup", "Setup"], ["activity", "Activity"], ["learnings", "Learnings"]]} />
      <ErrorText>{error}</ErrorText>
      {tab === "overview" && (results ? <Overview ws={ws} test={test} flag={flag} results={results} canManage={canManage} onChanged={onChanged} onOpen={onOpen} reload={loadResults} /> : !error && <Loading />)}
      {tab === "versions" && (
        <div className="space-y-3">
          {test.status === "running" && <MissedChangesNotice testID={test.id} edit={null} />}
          {test.status === "running" && test.page && <p className="text-[13px] text-muted-foreground">The test is live: a change you save to a version shows to its visitors straight away, and the results then mix before and after.</p>}
          <VersionsTab ws={ws} test={test} flag={flag} canManage={canManage && test.status === "running"} onFlagChanged={onChanged} go={() => go("ai")} />
        </div>
      )}
      {tab === "setup" && <Setup test={test} />}
      {tab === "activity" && <Activity ws={ws} test={test} />}
      {tab === "learnings" && <TestLearning ws={ws} test={test} canManage={canManage} onChanged={onChanged} />}
    </div>
  );
}

// Overview leads with what the results mean in one sentence, then the
// numbers, then what to do.
function Overview({ ws, test, flag, results, canManage, onChanged, onOpen, reload }: { ws: string; test: ABTest; flag?: Flag; results: ABTestResults; canManage: boolean; onChanged: () => void; onOpen: (item?: string) => void; reload: () => void }) {
  const [decision, setDecision] = useState(results.winner);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const names = (v: string) => (v === test.control ? `${v} (control)` : v);
  const variants = flag?.variants.map((v) => v.key) ?? results.result.arms.map((a) => a.variant);
  const v = verdict(results, (x) => x);
  const visitors = results.result.arms.reduce((n, a) => n + a.visitors, 0);

  // Until the first visitor arrives, check every 15 seconds.
  useEffect(() => {
    if (visitors > 0 || test.status !== "running") return;
    const t = window.setInterval(reload, 15000);
    return () => window.clearInterval(t);
  }, [visitors, test.status, reload]);

  async function act(call: () => Promise<{ error?: unknown }>, done: string) {
    setBusy(true);
    const { error } = await call();
    setBusy(false);
    if (error) return setError(errorMessage(error));
    toast(done);
    onChanged();
  }
  async function end() {
    const kept = decision ? `You keep ${decision}; ship it to show it to everyone.` : "You keep no version.";
    if (!(await confirmAction(`End ${test.name}?`, `The results are kept as they are now. ${kept} The flag stays as it is.`, "End test"))) return;
    act(() => api.POST("/v1/brands/{brand}/ab-tests/{test}/end", { params: { path: { brand: pickedBrandId(), test: test.id } }, body: { decision } }), `Ended ${test.name}.`);
  }
  async function freeze(frozen: boolean) {
    if (frozen && !(await confirmAction(`Freeze ${test.name}?`, "New visitors stop joining; those already in keep counting, so the test can finish on the visitors it has. You can unfreeze it.", "Freeze"))) return;
    act(() => api.POST("/v1/brands/{brand}/ab-tests/{test}/freeze", { params: { path: { brand: pickedBrandId(), test: test.id } }, body: { frozen } }), frozen ? `${test.name} is frozen: no new visitors join.` : `${test.name} lets new visitors join again.`);
  }
  async function ship(variant: string) {
    const what = variant === test.control ? `${variant}, the control,` : variant;
    if (!(await confirmAction(`Ship ${variant}?`, `Everyone sees ${what} from now on.${test.status === "running" ? " The test ends, keeping its results." : ""}`, `Ship ${variant}`))) return;
    act(() => api.POST("/v1/brands/{brand}/ab-tests/{test}/ship", { params: { path: { brand: pickedBrandId(), test: test.id } }, body: { variant } }), `Shipped ${variant}.`);
  }
  async function duplicate() {
    setBusy(true);
    const { data, error } = await api.POST("/v1/brands/{brand}/ab-tests/{test}/duplicate", { params: { path: { brand: pickedBrandId(), test: test.id } } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    toast(`Added ${data.name} as a draft.`);
    onOpen(data.id);
  }

  const first = results.result.goals[0];
  return (
    <div className="space-y-5 text-[13px]">
      {visitors === 0 && test.status === "running" ? <CountingCard test={test} /> : v.text && <div className={`rounded-lg border px-4 py-3 text-[15px] ${toneClass[v.tone]}`}>{v.text}</div>}
      <ErrorText>{error}</ErrorText>
      {canManage && (
        <div className="flex flex-wrap items-center gap-2">
          {results.winner && <Button disabled={busy} onClick={() => ship(results.winner)}>Ship {results.winner} to everyone</Button>}
          {test.status === "done" && !results.winner && test.decision && <Button variant="ghost" disabled={busy} onClick={() => ship(test.decision)}>Ship {test.decision} to everyone</Button>}
          {test.status === "running" && (
            <>
              <label className="flex items-center gap-2">
                <span>Keep</span>
                <select className={field} value={decision} onChange={(e) => setDecision(e.target.value)}>
                  <option value="">No version</option>
                  {variants.map((k) => <option key={k} value={k}>{k}{k === results.winner ? " (winner)" : ""}</option>)}
                </select>
              </label>
              <Button variant="ghost" disabled={busy} onClick={end}>End test</Button>
              <Button variant="ghost" disabled={busy} onClick={() => freeze(!test.frozen_at)}>{test.frozen_at ? "Let new visitors join" : "Freeze"}</Button>
            </>
          )}
          <Button variant="ghost" disabled={busy} onClick={duplicate}>Duplicate as a new draft</Button>
        </div>
      )}
      {results.result.flag_changed && results.result.flag_changed.length > 0 && (
        <div className={`space-y-1 rounded-md border px-3 py-2 ${toneClass.amber}`}>
          <span className="block font-medium">The test changed after it started</span>
          {results.result.flag_changed.map((c) => <p key={c}>{c}</p>)}
          <p>Results now mix visitors from before and after. To measure the new setup cleanly, end this test and duplicate it.</p>
        </div>
      )}
      {canManage && visitors > 0 && <SummaryPanel ws={ws} testID={test.id} />}
      {first?.days && first.days.length > 1 && (
        <div className="grid gap-4 lg:grid-cols-2">
          <Card title={`${goalText(first.goal)}${measured(first.goal) ? "" : " rate"}, day by day`}>
            <DayChart days={first.days} control={test.control} goal={first.goal} />
          </Card>
          <Card title="Lift over the control, day by day">
            <LiftChart days={first.days} control={test.control} goal={first.goal} />
          </Card>
        </div>
      )}
      {results.result.goals.map((g, gi) => (
        <Card key={gi} title={gi === 0 ? `Judged on ${goalText(g.goal)}` : `${goalText(g.goal)} · for learning`}>
          <GoalTable goal={g.goal} arms={g.arms} comparisons={g.comparisons ?? []} control={test.control} names={names} />
        </Card>
      ))}
      {results.result.guardrails.map((g, gi) => (
        <Card key={`guard-${gi}`} title={`Guardrail: ${goalText(g.goal)} (${g.goal.better === "lower" ? "lower" : "higher"} is better)`}>
          {g.harmed.length > 0 && <div className={`mb-2 rounded-md border px-3 py-2 ${toneClass.amber}`}>{g.harmed.map(names).join(", ")} clearly {g.harmed.length === 1 ? "makes" : "make"} this worse. Weigh that before shipping.</div>}
          <GoalTable goal={g.goal} arms={g.arms} comparisons={g.comparisons ?? []} control={test.control} names={names} />
        </Card>
      ))}
      {results.result.variance_reduction && (first?.comparisons?.[0]?.variance_reduction ?? 0) > 0 && (
        <p className="text-[12px] text-muted-foreground">Adjusted for what visitors did in the 28 days before they joined, which took out {Math.round((first!.comparisons![0]!.variance_reduction ?? 0) * 100)}% of the noise, so the test needs fewer visitors.</p>
      )}
      <details className="rounded-md border px-3 py-2">
        <summary className="cursor-pointer font-medium">More detail: by device, country and source, and other tests at the same time</summary>
        <div className="mt-3 space-y-4">
          <BreakdownPanel ws={ws} testID={test.id} control={test.control} names={names} />
          <InteractionsPanel ws={ws} testID={test.id} names={names} />
          {(test.factors ?? []).length > 0 && <FactorialPanel ws={ws} testID={test.id} />}
        </div>
      </details>
      <p className="text-[12px] text-muted-foreground">
        Visitors split {results.result.arms.map((a) => `${a.variant} ${a.visitors.toLocaleString()}`).join(" · ")}
        {results.result.sample_ratio_mismatch ? ", further from the even split than luck explains." : ", in line with the split."}{" "}
        Tuned to find changes of {signed(results.result.min_effect)} or more.{" "}
        {results.result.corrected_for > 1 && `${results.result.corrected_for} versions compete with the control, so the results are corrected for that many chances of a false winner. `}
        {results.result.crossovers > 0 && `${results.result.crossovers.toLocaleString()} visitors saw more than one version (they count under the first)${results.result.crossovers * 100 > visitors ? ": more than 1%, so check nothing (a cache, a server) shows versions inconsistently" : ""}. `}
        {test.status === "running" ? `Counted ${ago(results.result.computed_at)}.` : "As they stood when the test ended."}{" "}
        {test.status === "running" && <button className="underline underline-offset-2" onClick={reload}>Refresh</button>}
      </p>
    </div>
  );
}

// CountingCard helps check a just-started test is counting: open the page
// yourself, and your visit shows up here within a minute.
function CountingCard({ test }: { test: ABTest }) {
  const page = test.page.replace(/\*$/, "");
  return (
    <div role="status" className="space-y-2 rounded-lg border border-blue-600/25 bg-blue-500/10 p-3">
      <span className="flex items-center gap-2 font-medium"><span className="inline-block size-2 animate-pulse rounded-full bg-blue-500" aria-hidden />Waiting for the first visitor</span>
      <p>Check it's counting: open {page ? <a className="font-mono underline underline-offset-2" href={page} target="_blank" rel="noreferrer">{page}</a> : "a page that reads the flag"} in a private window (so it's like a new visitor). Your visit shows up here within a minute; this page checks every 15 seconds.</p>
      <p className="text-[12px] text-muted-foreground">Nothing after a few minutes? The page may be missing the Outflare tracking script, or a cache may be serving an old copy.</p>
    </div>
  );
}

// Setup is what the test was registered with when it started.
function Setup({ test }: { test: ABTest }) {
  return (
    <div className="max-w-3xl space-y-4 text-[13px]">
      {test.hypothesis && <Card title="Hypothesis"><p className="italic">{test.hypothesis}</p></Card>}
      <Card title="Goals">
        <ul className="space-y-1">
          {test.goals.map((g, i) => <li key={i}><span className="text-muted-foreground">{i === 0 ? "Judged on: " : "Also: "}</span><span className="font-mono">{goalText(g)}</span>{g.window_hours ? ` within ${g.window_hours} h` : ""}</li>)}
          {test.guardrails.map((g, i) => <li key={`g${i}`}><span className="text-muted-foreground">Guardrail: </span><span className="font-mono">{goalText(g)}</span> ({g.better === "lower" ? "lower" : "higher"} is better)</li>)}
        </ul>
      </Card>
      <Card title="How it runs">
        <ul className="space-y-1">
          {test.page && <li>On <span className="font-mono">{test.page}</span>, through the Outflare tracking script.</li>}
          <li>Flag <span className="font-mono">{test.flag_key}</span>, compared with <span className="font-mono">{test.control}</span>.</li>
          <li>Tuned to find changes of {signed(test.min_effect)} or more.</li>
        </ul>
      </Card>
      {test.registration && <RegistrationCard reg={test.registration} />}
      <p className="text-[12px] text-muted-foreground">Goals and the hypothesis are fixed once a test starts, so its results can't be reshaped. To measure something else, duplicate it from the overview.</p>
    </div>
  );
}

// Activity is what happened to the test, newest first: when it was made,
// started, frozen and ended, versions checked, and AI help asked for.
function Activity({ ws, test }: { ws: string; test: ABTest }) {
  const [runs, setRuns] = useState<ABTestAIRun[]>([]);
  const [previews, setPreviews] = useState<ABTestPreviews | null>(null);
  useEffect(() => {
    api.GET("/v1/brands/{brand}/ab-test-ai-runs", { params: { path: { brand: pickedBrandId() }, query: { test: test.id } } }).then(({ data }) => setRuns(data?.data ?? []));
    api.GET("/v1/brands/{brand}/ab-tests/{test}/previews", { params: { path: { brand: pickedBrandId(), test: test.id } } }).then(({ data }) => setPreviews(data ?? null));
  }, [ws, test.id]);
  const kinds: Record<string, string> = { suggest: "suggested tests", versions: "wrote versions", summary: "summarised the results", learning: "wrote the learning", ask: "answered a question" };
  const rows: { at: string; text: ReactNode }[] = [
    { at: test.created_at, text: "Added as a draft" },
    ...(test.started_at ? [{ at: test.started_at, text: "Started: visitors began joining" }] : []),
    ...(test.frozen_at ? [{ at: test.frozen_at, text: "Frozen: new visitors stopped joining" }] : []),
    ...(test.ended_at ? [{ at: test.ended_at, text: `Ended${test.decision ? `, keeping ${test.decision}` : ", keeping no version"}` }] : []),
    ...(previews?.versions ?? []).filter((v) => v.check).map((v) => ({ at: v.check!.checked_at, text: <>Version <span className="font-mono">{v.key}</span> checked: {v.check!.status === "ok" ? "looks right" : "broken"}{v.check!.note ? ` (${v.check!.note})` : ""}</> })),
    ...runs.map((r) => ({ at: r.created_at, text: <>AI {kinds[r.kind] ?? r.kind}: {r.status === "ready" ? "done" : r.status === "failed" ? `failed (${r.error})` : "working"}{r.model ? ` · ${r.model}` : ""}</> })),
  ].sort((a, b) => b.at.localeCompare(a.at));
  return (
    <ol className="max-w-3xl divide-y rounded-md border text-[13px]">
      {rows.map((r, i) => (
        <li key={i} className="flex items-start justify-between gap-3 px-3 py-2">
          <span>{r.text}</span>
          <span className="shrink-0 text-[12px] text-muted-foreground" title={r.at}>{ago(r.at)}</span>
        </li>
      ))}
    </ol>
  );
}

// TestLearning is what the test taught, written by the workspace's AI
// when it ends, or again on request.
function TestLearning({ ws, test, canManage, onChanged }: { ws: string; test: ABTest; canManage: boolean; onChanged: () => void }) {
  const ai = useAIRun(ws);
  useEffect(() => {
    if (ai.ready) onChanged();
  }, [ai.ready?.id]); // eslint-disable-line react-hooks/exhaustive-deps
  const write = () => ai.start(() => api.POST("/v1/brands/{brand}/ab-tests/{test}/learning", { params: { path: { brand: pickedBrandId(), test: test.id } } }));
  return (
    <div className="max-w-3xl space-y-3 text-[13px]">
      {test.learning ? (
        <Card title="What it taught">
          <p className="font-medium">{test.learning.headline}</p>
          <p>{test.learning.learning}</p>
          {test.learning.tags.length > 0 && <p className="text-[12px] text-muted-foreground">{test.learning.tags.join(", ")}</p>}
        </Card>
      ) : (
        <p className="text-muted-foreground">{test.status === "done" ? "No learning written yet." : "Your AI model writes what the test taught when it ends."}</p>
      )}
      {canManage && test.status === "done" && <Button variant="ghost" disabled={ai.busy} onClick={write}>{test.learning ? "Write it again" : "Write the learning"}</Button>}
      <AIRunCard ai={ai} working="Writing what the test taught" onRetry={write} />
    </div>
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

// FactorialPanel shows a multivariate test cell by cell: every combination
// of versions against the all-controls one, and the best.
function FactorialPanel({ ws, testID }: { ws: string; testID: string }) {
  type Cell = { versions: Record<string, string>; arm: { visitors: number; conversions: number; mean?: number }; comparison: { chance_to_beat: number; lift: number; p_value: number } | null };
  const [data, setData] = useState<{ factors: string[] | null; cells: Cell[]; best: Cell | null; corrected_for: number; metric: string } | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    api.GET("/v1/brands/{brand}/ab-tests/{test}/factorial", { params: { path: { brand: pickedBrandId(), test: testID } } }).then(({ data, error }) => {
      setError(error ? errorMessage(error) : "");
      setData((data?.result as never) ?? null);
    });
  }, [ws, testID]);
  const label = (c: Cell) => (data?.factors ?? []).map((k) => `${k}: ${c.versions[k]}`).join(", ");
  const value = (c: Cell) => (data?.metric === "conversion" ? `${((c.arm.conversions / Math.max(1, c.arm.visitors)) * 100).toFixed(1)}%` : (c.arm.mean ?? 0).toFixed(2));
  return (
    <Card title="Every combination">
      <div className="space-y-2 text-[12px]">
        {error && <ErrorText>{error}</ErrorText>}
        {!data && !error && <Loading />}
        {data && (
          <>
            {data.best && <p className="font-medium">Best so far: {label(data.best)}{data.best.comparison && `, ${signed(data.best.comparison.lift)} over all controls (${Math.round(data.best.comparison.chance_to_beat * 100)}% likely better${data.best.comparison.p_value < 0.05 ? ", safe to stop" : ""})`}.</p>}
            <p className="text-muted-foreground">Each combination is compared with every flag on its control, corrected for {data.corrected_for} comparisons.</p>
            {data.cells.map((c) => (
              <div key={label(c)} className="flex justify-between gap-2 border-t pt-1">
                <span className="font-mono">{label(c)}</span>
                <span className="tabular-nums text-muted-foreground">
                  {c.arm.visitors.toLocaleString()} visitors · {value(c)}
                  {c.comparison && ` · ${signed(c.comparison.lift)}`}
                </span>
              </div>
            ))}
          </>
        )}
      </div>
    </Card>
  );
}

// InteractionsPanel checks the test against others that ran at the same
// time, on demand: whether one change altered the other's effect.
function InteractionsPanel({ testID, names }: { ws: string; testID: string; names: (v: string) => string }) {
  type Data = { interactions: { test_id: string; name: string; flag_key: string; overlap: number; versions: { variant: string; p_value: number; interacts: boolean; effects: { other_variant: string; visitors: number; difference: number }[] }[] }[] };
  const [data, setData] = useState<Data | null>(null);
  const [error, setError] = useState("");
  const [asked, setAsked] = useState(false);
  const check = () => {
    setAsked(true);
    api.GET("/v1/brands/{brand}/ab-tests/{test}/interactions", { params: { path: { brand: pickedBrandId(), test: testID } } }).then(({ data, error }) => {
      setError(error ? errorMessage(error) : "");
      setData((data as Data | undefined) ?? null);
    });
  };
  return (
    <Card title="Tests at the same time">
      <div className="space-y-3 text-[12px]">
        {!asked && (
          <div className="flex items-center gap-2">
            <span className="text-muted-foreground">Did another test running at the same time change this one's effect?</span>
            <Button variant="ghost" onClick={check}>Check</Button>
          </div>
        )}
        {error && <ErrorText>{error}</ErrorText>}
        {asked && !data && !error && <Loading />}
        {data && data.interactions.length === 0 && <p className="text-muted-foreground">No other test of another flag ran at the same time, outside this flag's exclusion group.</p>}
        {data?.interactions.map((x) => (
          <div key={x.test_id} className="space-y-1 rounded-md border p-2">
            <div className="font-medium">
              {x.name} <span className="font-mono text-muted-foreground">{x.flag_key}</span> · {x.overlap.toLocaleString()} visitors saw both
            </div>
            {x.versions.map((v) => (
              <div key={v.variant}>
                <span className="font-mono">{names(v.variant)}</span>:{" "}
                {v.interacts ? (
                  <Badge tone="amber">Interacts (p = {v.p_value.toFixed(3)})</Badge>
                ) : (
                  <span className="text-muted-foreground">no clear interaction (p = {v.p_value.toFixed(2)})</span>
                )}{" "}
                <span className="text-muted-foreground">{v.effects.map((e) => `with ${e.other_variant}: ${e.difference >= 0 ? "+" : ""}${e.difference.toFixed(3)}`).join(", ")}</span>
              </div>
            ))}
          </div>
        ))}
      </div>
    </Card>
  );
}

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
    api.GET("/v1/brands/{brand}/ab-tests/{test}/breakdown", { params: { path: { brand: pickedBrandId(), test: testID }, query: { by } } }).then(({ data, error }) => {
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

// PreviewPanel gives a link per version on the pages that show the flag,
// and lets a person mark each version as looking right or broken.
function PreviewPanel({ ws, testID, canManage, onChecked }: { ws: string; testID: string; canManage: boolean; onChecked: () => void }) {
  const [data, setData] = useState<ABTestPreviews | null>(null);
  const [page, setPage] = useState("");
  const [notes, setNotes] = useState<Record<string, string>>({});
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");
  const load = useCallback(
    (url?: string) => {
      setError("");
      api.GET("/v1/brands/{brand}/ab-tests/{test}/previews", { params: { path: { brand: pickedBrandId(), test: testID }, query: url ? { page: url } : {} } }).then(({ data, error }) => {
        if (error) setError(errorMessage(error));
        else setData(data);
      });
    },
    [ws, testID],
  );
  useEffect(() => load(), [load]);
  const check = async (variant: string, status: "ok" | "broken") => {
    setBusy(variant);
    setError("");
    const { data: out, error } = await api.POST("/v1/brands/{brand}/ab-tests/{test}/checks", {
      params: { path: { brand: pickedBrandId(), test: testID } },
      body: { variant, status, note: notes[variant] ?? "" },
    });
    setBusy("");
    if (error) return setError(errorMessage(error));
    // Keep the pages already shown (they may come from a typed address).
    setData((cur) => (cur && out ? { ...out, pages: cur.pages } : out ?? null));
    onChecked();
  };
  if (!data) return error ? <ErrorText>{error}</ErrorText> : null;
  return (
    <div className="space-y-3 rounded-md border p-3">
      <span className="block font-medium">Preview and check each version</span>
      <p className="text-muted-foreground text-[12px]">{data.hint}</p>
      <ErrorText>{error}</ErrorText>
      {data.pages.map((p) => (
        <div key={p.url} className="space-y-1">
          <span className="block truncate font-mono text-[12px]" title={p.url}>
            {p.url}
            {p.visitors > 0 && <span className="text-muted-foreground"> · {p.visitors} visitors</span>}
          </span>
          <div className="flex flex-wrap gap-2">
            {p.links.map((l) => (
              <a key={l.variant} href={l.url} target="_blank" rel="noreferrer" className="text-primary text-[12px] underline">
                {l.variant}
              </a>
            ))}
          </div>
        </div>
      ))}
      <form
        className="flex gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          load(page.trim());
        }}
      >
        <input className={field} placeholder="https://… another page that reads the flag" value={page} onChange={(e) => setPage(e.target.value)} />
        <Button variant="ghost" disabled={!page.trim()}>Get links</Button>
      </form>
      <div className="space-y-2 border-t pt-3">
        {data.versions.map((v) => (
          <div key={v.key} className="space-y-1">
            <div className="flex flex-wrap items-center gap-2 text-[12px]">
              <span className="font-mono">{v.key}</span>
              {v.control && <Badge>Control</Badge>}
              {v.check && !v.check.stale && <Badge tone={v.check.status === "ok" ? "green" : "red"}>{v.check.status === "ok" ? "Looks right" : "Broken"}</Badge>}
              {v.check?.stale && <Badge tone="amber">Changed since checked</Badge>}
              {!v.check && <span className="text-muted-foreground">Not checked</span>}
              {v.check && <span className="text-muted-foreground">{ago(v.check.checked_at)}</span>}
            </div>
            {v.check?.note && <p className="text-muted-foreground text-[12px]">{v.check.note}</p>}
            {canManage && (
              <div className="flex gap-2">
                <input
                  className={field}
                  placeholder="Note (optional)"
                  maxLength={1000}
                  value={notes[v.key] ?? ""}
                  onChange={(e) => setNotes((n) => ({ ...n, [v.key]: e.target.value }))}
                />
                <Button variant="ghost" disabled={busy !== ""} onClick={() => check(v.key, "ok")}>Looks right</Button>
                <Button variant="ghost" disabled={busy !== ""} onClick={() => check(v.key, "broken")}>Broken</Button>
              </div>
            )}
          </div>
        ))}
      </div>
    </div>
  );
}

// RegistrationCard shows what the test set out to show when it started.
function RegistrationCard({ reg }: { reg: NonNullable<ABTest["registration"]> }) {
  return (
    <details className="rounded-md border px-3 py-2 text-[12px]">
      <summary className="cursor-pointer font-medium">Registered when it started, {ago(reg.registered_at)}</summary>
      <div className="mt-2 space-y-1">
        {reg.hypothesis && <p className="italic">{reg.hypothesis}</p>}
        <p>
          Control <span className="font-mono">{reg.control}</span>; tuned to find changes of {signed(reg.min_effect)} or more; rollout {reg.rollout}%.
        </p>
        <p>
          Versions: {reg.versions.map((v) => `${v.key} (${v.weight})`).join(", ")}.
        </p>
        {reg.plan && reg.plan.weeks > 0 && (
          <p>
            Planned: about {Math.ceil(reg.plan.weeks)} week(s), {reg.plan.visitors_per_version.toLocaleString()} visitors per version.
          </p>
        )}
      </div>
    </details>
  );
}

// MissedChangesNotice warns, on a running test, when visual changes found
// nothing on the page for some visitors (the page changed since), with the
// editor links to pick those parts again.
function MissedChangesNotice({ testID, edit }: { testID: string; edit: ReactNode }) {
  const [text, setText] = useState("");
  useEffect(() => {
    let live = true;
    api.GET("/v1/brands/{brand}/ab-tests/{test}/checklist", { params: { path: { brand: pickedBrandId(), test: testID } } }).then(({ data }) => {
      if (live) setText(data?.checks.find((c) => c.id === "changes_found")?.text ?? "");
    });
    return () => {
      live = false;
    };
  }, [testID]);
  if (!text) return null;
  return (
    <div role="status" className="space-y-2 rounded-lg border border-amber-500/40 bg-amber-500/10 p-3 text-[12px]">
      <p>{text}</p>
      {edit}
    </div>
  );
}

// ChecklistPanel says what would stop a draft measuring anything useful,
// read fresh when its goals change (the saved test's; deps only re-reads).
function ChecklistPanel({ ws, testID, deps }: { ws: string; testID: string; deps: string }) {
  const [checks, setChecks] = useState<{ id: string; status: "ok" | "warn" | "fail"; text: string }[] | null>(null);
  useEffect(() => {
    let live = true;
    api.GET("/v1/brands/{brand}/ab-tests/{test}/checklist", { params: { path: { brand: pickedBrandId(), test: testID } } }).then(({ data }) => {
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

