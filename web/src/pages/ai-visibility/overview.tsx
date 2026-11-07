import { useEffect, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { QueryChart } from "@/components/query-chart";
import { api, errorMessage } from "../../api";
import { Badge, Button, Loading, Stat, Stats, field, toast } from "../../ui";
import { Frame, RunBar, Section, addCompetitor, answerCents, awarenessLevels, awarenessName, held, marketName, pct, questionTypes, score, th, typeName, usd, useCompetitorSite, type AIData, type Entity, type Go, type Series } from "./shared";
import { Assistants, patchCohort } from "./settings";
import { Suggestions, WhatChanged, usePrompts } from "./questions";
import { pickedBrandId } from "@/lib/brand";

type Fix = Schemas["Fix"];
type Opportunity = Schemas["PlacementOpportunity"];

export function AIVisibility({ ws, go, item }: { ws: string; go: Go; item?: string }) {
  return (
    <Frame ws={ws} title="AI visibility" description="How often ChatGPT, Gemini, Claude and others name and recommend you when buyers ask, against your competitors, and what to do about it.">
      {(d) => (item === "setup" || needsSetup(d) ? <Setup ws={ws} d={d} go={go} /> : <Overview ws={ws} d={d} go={go} />)}
    </Frame>
  );
}

// A brand needs the setup until its question set has questions and
// assistants and has been run once.
const needsSetup = (d: AIData) => !d.cohort || d.cohort.prompts === 0 || d.settings.models.length === 0 || (!d.cohort.last_run && !d.running);

const steps = ["Your brand", "Competitors", "Questions", "Assistants", "First run"] as const;

function Setup({ ws, d, go }: { ws: string; d: AIData; go: Go }) {
  const first = !d.cohort ? 0 : d.cohort.prompts === 0 ? 1 : d.settings.models.length === 0 ? 3 : 4;
  const [step, setStep] = useState(first);
  const [settings, setSettings] = useState(d.settings);
  const { list, reload } = usePrompts(ws, d.cohort);
  const [rival, setRival] = useState("");
  const [busy, setBusy] = useState(false);
  const site = useCompetitorSite(ws, d.brand.id);
  const c = d.cohort;
  const active = (list ?? []).filter((p) => p.status === "active").length;

  async function confirmBrand() {
    if (c) return setStep(1);
    setBusy(true);
    const { data, error } = await api.POST("/v1/brands/{brand}/ai-cohorts", { params: { path: { brand: pickedBrandId() } }, body: { name: `${d.brand.name} questions`, brand_id: d.brand.id, competitors: [], schedule: "weekly" } });
    setBusy(false);
    if (error || !data) return toast(errorMessage(error), "error");
    if (held(data)) return;
    d.reload();
    setStep(1);
  }
  // A competitor added here goes on the brand's one competitor list (the
  // Competitors page), which every run counts. Only a brand without an
  // SEO site, so without that list, keeps it on its question set alone.
  async function addRival(e: React.FormEvent) {
    e.preventDefault();
    if (!c) return;
    const [name, ...domains] = rival.split(",").map((s) => s.trim()).filter(Boolean);
    if (!name) return;
    if (site) {
      const website = domains[0] ?? (name.includes(".") ? name : "");
      if (!website) return toast("Add its website after a comma, like Northpeak, northpeak.example.", "error");
      if (await addCompetitor(ws, site, website === name ? "" : name, website)) { setRival(""); d.reload(); }
      return;
    }
    const competitors = [...c.competitors.map(({ name, domains, aliases }) => ({ name, domains, aliases })), { name, domains }];
    if (await patchCohort(ws, c, { competitors })) { setRival(""); d.reload(); }
  }
  async function dropRival(name: string) {
    if (!c) return;
    const competitors = c.competitors.filter((x) => x.name !== name).map(({ name, domains, aliases }) => ({ name, domains, aliases }));
    if (await patchCohort(ws, c, { competitors })) d.reload();
  }
  async function run() {
    if (!c) return;
    setBusy(true);
    const { data, error } = await api.POST("/v1/brands/{brand}/ai-cohorts/{cohort}/runs", { params: { path: { brand: pickedBrandId(), cohort: c.id } } });
    setBusy(false);
    if (error || !data) return toast(errorMessage(error), "error");
    if (held(data)) return;
    toast("Asking now. Results fill in over the next few minutes.");
    d.reload();
    go("ai-visibility");
  }

  const perRun = active * settings.models.reduce((n, m) => n + answerCents(m), 0) * Math.max(1, settings.samples_per_prompt);
  const rivals = c ? [...c.competitors.map((x) => ({ ...x, own: true })), ...c.tracked_competitors.map((x) => ({ ...x, own: false }))] : [];

  return (
    <div className="grid gap-6 lg:grid-cols-[200px_1fr]">
      <ol className="space-y-1 text-[13px]">
        {steps.map((s, i) => (
          <li key={s}>
            <button disabled={i > 0 && !c} onClick={() => setStep(i)}
              className={`flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left ${i === step ? "bg-muted font-medium" : "text-muted-foreground hover:text-foreground"} disabled:opacity-50`}>
              <span className={`flex size-5 items-center justify-center rounded-full border font-mono text-[11px] ${i < first || (i === 0 && c) ? "border-primary bg-primary text-primary-foreground" : ""}`}>{i + 1}</span>
              {s}
            </button>
          </li>
        ))}
      </ol>
      <div className="min-w-0">
        {step === 0 && (
          <Section title="Is this your brand?" note="AI answers are checked for this name and website. The questions are drafted from what the website says it offers.">
            <dl className="grid grid-cols-[120px_1fr] gap-y-1.5">
              <dt className="text-muted-foreground">Name</dt><dd className="font-medium">{d.brand.name}</dd>
              <dt className="text-muted-foreground">Website</dt><dd className="font-mono">{d.brand.website || d.brand.domains[0] || "not set"}</dd>
              {d.brand.domains.length > 1 && <><dt className="text-muted-foreground">Also</dt><dd className="font-mono">{d.brand.domains.slice(1).join(", ")}</dd></>}
              <dt className="text-muted-foreground">Offers</dt><dd><KnowledgeLine ws={ws} d={d} go={go} /></dd>
            </dl>
            <div className="mt-4 flex gap-2">
              <Button onClick={confirmBrand} disabled={busy}>{busy ? "Saving…" : "Yes, continue"}</Button>
              <Button variant="ghost" onClick={() => go("setup")}>Edit the brand</Button>
            </div>
          </Section>
        )}
        {step === 1 && c && (
          <Section title="Who do you compete with?" note={<>Answers are counted for you and for every competitor on your <button className="underline" onClick={() => go("competitors")}>Competitors page</button>. Adding one here adds it there.</>}>
            {rivals.length > 0 ? (
              <ul className="mb-3 divide-y rounded-md border">
                {rivals.map((r) => (
                  <li key={r.name} className="flex items-center justify-between px-3 py-2">
                    <span>{r.name} <span className="font-mono text-[12px] text-muted-foreground">{r.domains.filter((x) => x !== r.name).join(", ")}</span></span>
                    {r.own ? <Button variant="ghost" onClick={() => dropRival(r.name)}>Remove</Button> : <span className="text-[12px] text-muted-foreground">on Competitors</span>}
                  </li>
                ))}
              </ul>
            ) : <p className="mb-3 text-muted-foreground">No competitors yet. Add three or four. After each run, brands the answers name next to you are suggested on the Competitors page too.</p>}
            <form className="flex flex-wrap gap-2" onSubmit={addRival}>
              <input className={`${field} w-72`} aria-label="Competitor" placeholder="Northpeak, northpeak.example" value={rival} onChange={(e) => setRival(e.target.value)} />
              <Button variant="ghost" type="submit" disabled={!rival.trim()}>Add</Button>
            </form>
            <div className="mt-4"><Button onClick={() => setStep(2)}>Continue</Button></div>
          </Section>
        )}
        {step === 2 && c && (
          <Section title="Which questions do your buyers ask AI?" note="Aim for 20 to 30, from people who only know their problem through to people comparing you with a competitor.">
            {list && <Suggestions ws={ws} cohort={c} list={list} reload={reload} onChange={d.reload} />}
            <div className="mt-4 flex items-center gap-3">
              <Button onClick={() => setStep(3)} disabled={active === 0}>Continue with {active} {active === 1 ? "question" : "questions"}</Button>
            </div>
          </Section>
        )}
        {step === 3 && c && (
          <Section title="Which assistants should answer?" note="Each one runs on your own account. Add at least one.">
            <Assistants ws={ws} brandId={d.brand.id} settings={settings} onSaved={setSettings} go={go} />
            <div className="mt-4"><Button onClick={() => setStep(4)} disabled={settings.models.length === 0}>Continue</Button></div>
          </Section>
        )}
        {step === 4 && c && (
          <Section title="Ready to ask" note="The first run takes a few minutes. Results fill the overview as they arrive.">
            <p>
              {active} {active === 1 ? "question" : "questions"} × {settings.models.length} {settings.models.length === 1 ? "assistant" : "assistants"}
              {settings.samples_per_prompt > 1 && ` × ${settings.samples_per_prompt} answers each`}: about <span className="font-semibold">{usd(Math.ceil(perRun))}</span> a run on your own accounts, within your {usd(settings.monthly_limit_cents)} monthly limit.
            </p>
            <label className="mt-3 flex items-center gap-2">
              Then ask again
              <select className={field} value={c.schedule} onChange={async (e) => { if (await patchCohort(ws, c, { schedule: e.target.value as typeof c.schedule })) d.reload(); }}>
                <option value="weekly">every week</option><option value="daily">every day</option><option value="off">only when I press Run</option>
              </select>
            </label>
            <div className="mt-4"><Button onClick={run} disabled={busy || active === 0 || settings.models.length === 0}>{busy ? "Starting…" : "Start the first run"}</Button></div>
          </Section>
        )}
      </div>
    </div>
  );
}

// change is the score's move from the week before, when there is one.
function change(weeks: NonNullable<AIData["vis"]>["weeks"]) {
  const w = weeks.filter((x) => x.score !== null);
  if (w.length < 2) return null;
  return Math.round((w.at(-1)!.score ?? 0) - (w.at(-2)!.score ?? 0));
}

// KnowledgeLine says what the brand's website was read to offer, or
// offers to read it, so the questions drafted next fit the brand.
function KnowledgeLine({ ws, d, go }: { ws: string; d: AIData; go: Go }) {
  const [c, setC] = useState<Schemas["BrandContext"] | null>(null);
  const [n, setN] = useState(0);
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/brands/{brand}/context", { params: { path: { ws, brand: d.brand.id } } }).then(({ data }) => data && setC(data));
  }, [ws, d.brand.id, n]);
  if (!c) return <span className="text-muted-foreground">…</span>;
  const k = c.knowledge;
  const items = Object.values(k.lists).reduce((s, l) => s + l.length, 0);
  if (items === 0 && !k.summary) {
    return (
      <span className="text-muted-foreground">
        {c.knowledge_error ? "The website couldn't be read. " : "Not read yet. "}
        {d.brand.website && (
          <button className="underline" onClick={async () => {
            const { error } = await api.POST("/v1/workspaces/{ws}/brands/{brand}/knowledge/read", { params: { path: { ws, brand: d.brand.id } } });
            if (error) return toast(errorMessage(error), "error");
            toast("Reading the website. Come back to this step in a minute or two.");
            setTimeout(() => setN((x) => x + 1), 20000);
          }}>Read the website</button>
        )}
      </span>
    );
  }
  return <span>{k.summary || `${items} things it offers`} <button className="text-[12px] text-muted-foreground underline" onClick={() => go("setup")}>Edit on the brand page</button></span>;
}

function Overview({ ws, d, go }: { ws: string; d: AIData; go: Go }) {
  const v = d.vis;
  if (!v || v.answers === 0) {
    return (
      <>
        <RunBar ws={ws} d={d} />
        <p className="text-[13px] text-muted-foreground">{d.running ? "The first answers are on their way." : "No answers in this period. Run the questions, or pick a longer period."}</p>
      </>
    );
  }
  const move = change(v.weeks);
  const board = [{ ...v.brand, you: true }, ...v.competitors.map((c) => ({ ...c, you: false }))].sort((a, b) => (b.score ?? 0) - (a.score ?? 0));
  const rank = board.findIndex((e) => e.you) + 1;
  return (
    <>
      <RunBar ws={ws} d={d} />
      <Stats cols={5}>
        <Stat label="AI visibility score" value={score(v.brand.score)} sub={move === null ? `${v.answers} answers` : `${move >= 0 ? "+" : ""}${move} on the week before`} tone={move === null || move === 0 ? undefined : move > 0 ? "green" : "red"} />
        <Stat label="Rank" value={`#${rank}`} sub={`of ${board.length} brands`} />
        <Stat label="Share of voice" value={pct(v.brand.share_of_voice)} sub="of every brand named" />
        <Stat label="Recommended" value={pct(v.brand.recommend_rate)} sub={`named in ${pct(v.brand.mention_rate)}`} />
        <Stat label="Average position" value={v.brand.avg_position?.toFixed(1) ?? "–"} sub="1 is named first" />
      </Stats>
      <div className="grid gap-6 xl:grid-cols-[3fr_2fr]">
        <ScoreOverTime d={d} />
        <Section title="Who wins AI answers" action={<Button variant="ghost" onClick={() => go("ai-competitors")}>All competitors</Button>}>
          <Leaderboard rows={board.slice(0, 7)} />
        </Section>
      </div>
      <div className="grid gap-6 xl:grid-cols-2">
        <NextSteps ws={ws} d={d} go={go} />
        <WhatChanged d={d} go={go} />
      </div>
      <div className="grid gap-6 xl:grid-cols-2">
        <Grid v={v} go={go} />
        <Section title="By assistant">
          <table className="w-full">
            <thead><tr><th className={th}>Assistant</th><th className={th}>Answers</th><th className={th}>Names you</th></tr></thead>
            <tbody className="divide-y">
              {v.models.map((m) => (
                <tr key={m.provider + m.model + m.web_search}>
                  <td className="py-1.5 pr-3">{m.model === "chatgpt" ? "ChatGPT" : m.model === "gemini" ? "Gemini" : <span className="font-mono">{m.model}</span>}</td>
                  <td className="pr-3">{m.answers}</td>
                  <td><Bar value={m.mention_rate} /></td>
                </tr>
              ))}
            </tbody>
          </table>
        </Section>
      </div>
    </>
  );
}

const compareBy = [
  { id: "", name: "Score and share of voice" },
  { id: "competitor", name: "Competitors" },
  { id: "assistant", name: "Assistants" },
  { id: "awareness", name: "Awareness level" },
  { id: "question_type", name: "Question type" },
  { id: "topic", name: "Topic" },
  { id: "persona", name: "Persona" },
  { id: "market", name: "Market" },
] as const;
type CompareBy = (typeof compareBy)[number]["id"];

// ScoreOverTime is the trend: the brand's score and share of voice, or
// the score split one way ("Compare by") so a person can see which
// competitor, assistant or kind of question is moving.
function ScoreOverTime({ d }: { d: AIData }) {
  const v = d.vis!;
  const [by, setBy] = useState<CompareBy>("");
  const [series, setSeries] = useState<Series | null>(null);
  useEffect(() => {
    if (!by) return setSeries(null);
    setSeries(null);
    api.GET("/v1/brands/{brand}/ai-visibility/series", { params: { path: { brand: d.brand.id }, query: { by, days: d.days, cohort: d.cohort?.id } } }).then(({ data, error }) => {
      if (error || !data) return toast(errorMessage(error));
      setSeries(data);
    });
  }, [by, d.brand.id, d.days, d.cohort?.id]);
  const names = by === "awareness" ? awarenessName : by === "question_type" ? typeName : by === "market" ? marketName : (x: string) => x;
  const label = (s: Series["series"][number]) => (s.you ? `${s.label} (you)` : by === "market" ? marketName(s.key) : (by === "awareness" || by === "question_type") && s.key ? names(s.key) || s.label : s.label);
  const picker = (
    <select className={field} aria-label="Compare by" value={by} onChange={(e) => setBy(e.target.value as CompareBy)}>
      {compareBy.map((c) => <option key={c.id} value={c.id}>{c.id ? `Compare ${c.name.toLowerCase()}` : c.name}</option>)}
    </select>
  );
  if (!by) {
    return (
      <Section title="Score over time" action={picker} note={v.weeks.some((w) => w.real_change) ? "A dot marks a week whose change is more than chance." : "Weekly, demand-weighted, 0 to 100."}>
        {v.weeks.length < 2 ? (
          <p className="text-muted-foreground">The line starts after a second week of answers.</p>
        ) : (
          <QueryChart
            columns={[{ name: "week", type: "text" }, { name: "Score", type: "float8" }, { name: "Share of voice %", type: "float8" }]}
            rows={v.weeks.map((w) => [w.week, w.score === null ? null : Math.round(w.score), w.share_of_voice === null ? null : Math.round(w.share_of_voice * 100)])}
            spec={{ type: "line", x: "week", y: ["Score", "Share of voice %"] }}
          />
        )}
      </Section>
    );
  }
  const lines = series?.series.filter((s) => s.answers > 0) ?? [];
  return (
    <Section title="Score over time" action={picker} note={`Score 0 to 100 by ${compareBy.find((c) => c.id === by)?.name.toLowerCase()}, week by week.`}>
      {!series ? (
        <Loading />
      ) : lines.length === 0 ? (
        <p className="text-muted-foreground">No answers in this period.</p>
      ) : (
        <>
          <QueryChart
            columns={[{ name: "week", type: "text" }, ...lines.map((s) => ({ name: label(s), type: "float8" as const }))]}
            rows={series.weeks.map((w, i) => [w, ...lines.map((s) => { const x = s.weeks[i]?.score; return x === null || x === undefined ? null : Math.round(x); })])}
            spec={{ type: "line", x: "week", y: lines.map(label) }}
          />
          <table className="mt-3 w-full">
            <thead><tr><th className={th}>{compareBy.find((c) => c.id === by)?.name}</th><th className={th}>Score</th><th className={th}>Named</th><th className={th}>Answers</th></tr></thead>
            <tbody className="divide-y">
              {lines.map((s) => (
                <tr key={s.key}>
                  <td className="py-1.5 pr-3">{label(s)}</td>
                  <td className="pr-3 tabular-nums">{score(s.score)}{s.score_low !== null && s.score_high !== null ? <span className="text-muted-foreground"> ({Math.round(s.score_low)}–{Math.round(s.score_high)})</span> : null}</td>
                  <td className="pr-3"><Bar value={s.mention_rate} /></td>
                  <td className="tabular-nums">{s.answers}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      )}
    </Section>
  );
}

export function Bar({ value, tone = "bg-primary" }: { value: number | null; tone?: string }) {
  return (
    <span className="flex items-center gap-2">
      <span className="h-1.5 w-24 overflow-hidden rounded bg-muted"><span className={`block h-full ${tone}`} style={{ width: `${Math.round((value ?? 0) * 100)}%` }} /></span>
      <span className="font-mono text-[12px]">{pct(value)}</span>
    </span>
  );
}

export function Leaderboard({ rows, full }: { rows: (Entity & { you: boolean })[]; full?: boolean }) {
  return (
    <table className="w-full">
      <thead>
        <tr>
          <th className={th}>Brand</th><th className={th}>Score</th><th className={th}>Share of voice</th>
          {full && <><th className={th}>Named</th><th className={th}>Recommended</th><th className={th}>Warned</th><th className={th}>Position</th><th className={th} title="Mentions in every AI answer DataForSEO keeps, counted once a day">All AI answers</th></>}
        </tr>
      </thead>
      <tbody className="divide-y">
        {rows.map((e, i) => (
          <tr key={e.name} className={e.you ? "bg-primary/5 font-semibold" : ""}>
            <td className="py-1.5 pr-3"><span className="mr-2 font-mono text-[11px] text-muted-foreground">{i + 1}</span>{e.name}{e.you && <span className="ml-1 text-[11px] font-normal text-muted-foreground">you</span>}</td>
            <td className="pr-3 font-mono">{score(e.score)}</td>
            <td className="pr-3"><Bar value={e.share_of_voice} tone={e.you ? "bg-primary" : "bg-muted-foreground/50"} /></td>
            {full && (
              <>
                <td className="pr-3">{pct(e.mention_rate)}</td>
                <td className="pr-3">{pct(e.recommend_rate)}</td>
                <td className="pr-3">{e.warned > 0 ? <Badge tone="amber">{String(e.warned)}</Badge> : "–"}</td>
                <td className="pr-3">{e.avg_position?.toFixed(1) ?? "–"}</td>
                <td>{e.benchmark_mentions === null ? "–" : e.benchmark_mentions.toLocaleString()}{e.benchmark_change ? <span className={e.benchmark_change > 0 ? "text-green-600" : "text-red-600"}>{` ${e.benchmark_change > 0 ? "+" : ""}${e.benchmark_change}`}</span> : null}</td>
              </>
            )}
          </tr>
        ))}
      </tbody>
    </table>
  );
}

// Grid colours the score by how much the asker knows and what they ask.
function Grid({ v, go }: { v: NonNullable<AIData["vis"]>; go: Go }) {
  const cell = (a: string, q: string) => v.grid.find((g) => g.awareness === a && g.question_type === q);
  const placed = v.grid.some((g) => g.awareness && g.question_type);
  const tone = (s: number | null) => (s === null ? "" : s >= 60 ? "bg-green-500/25" : s >= 30 ? "bg-amber-500/25" : "bg-red-500/20");
  return (
    <Section title="Where you're strong and weak" note="Score by how much the asker knows (rows) and what they ask (columns). Green 60+, red under 30.">
      {!placed ? (
        <p className="text-muted-foreground">Questions aren't placed yet. Suggested questions come placed; <button className="underline" onClick={() => go("ai-questions")}>add some</button>.</p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-center text-[12px]">
            <thead><tr><th />{questionTypes.map((q) => <th key={q.id} className="px-1 pb-1 font-medium text-muted-foreground">{q.name}</th>)}</tr></thead>
            <tbody>
              {awarenessLevels.map((a) => (
                <tr key={a.id}>
                  <td className="py-0.5 pr-2 text-left whitespace-nowrap text-muted-foreground">{a.name}</td>
                  {questionTypes.map((q) => {
                    const g = cell(a.id, q.id);
                    return <td key={q.id} className="p-0.5"><div className={`rounded px-1 py-1.5 font-mono ${g ? tone(g.score) : "text-muted-foreground/40"}`} title={g ? `${g.questions} questions, ${g.answers} answers` : "No questions here"}>{g ? score(g.score) : "·"}</div></td>;
                  })}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Section>
  );
}

// NextSteps ranks what would most raise the brand in AI answers: open AI
// visibility fixes, pages the answers cite that the brand isn't on, wrong
// facts and competitors the answers name that aren't counted yet.
function NextSteps({ ws, d, go }: { ws: string; d: AIData; go: Go }) {
  const [fixes, setFixes] = useState<Fix[]>([]);
  const [opps, setOpps] = useState<Opportunity[]>([]);
  useEffect(() => {
    api.GET("/v1/brands/{brand}/fixes", { params: { path: { brand: pickedBrandId() }, query: { source: "ai_visibility", status: "open" } } }).then(({ data }) => setFixes((data?.data ?? []).filter((f) => f.brand_id === d.brand.id || f.brand_id === null)));
    api.GET("/v1/brands/{brand}/coverage/opportunities", { params: { path: { brand: d.brand.id }, query: { reason: "ai_cited", state: "open", limit: 3 } } }).then(({ data }) => setOpps(data?.data ?? []));
  }, [ws, d.brand.id]);
  const v = d.vis!;
  const sev = { error: 0, warning: 1, notice: 2 } as const;
  const items: { key: string; what: string; why: string; act: string; go: () => void }[] = [
    ...fixes.slice().sort((a, b) => sev[a.rule.severity] - sev[b.rule.severity] || b.count - a.count).slice(0, 4).map((f) => ({ key: f.id, what: f.count > 1 ? `${f.rule.title} (${f.count} ${f.unit})` : f.rule.title, why: f.rule.why, act: "Fix it", go: () => go("fixes") })),
    ...opps.map((o) => ({ key: o.id, what: `Get onto ${o.host}`, why: "AI answers to your questions cite this page, and it doesn't name you.", act: "Open in Presence", go: () => go("coverage") })),
    ...(v.facts.facts_wrong > 0 ? [{ key: "facts", what: `AI gets ${v.facts.facts_wrong} of your facts wrong`, why: `${v.facts.wrong_answers} of ${v.facts.checked_answers} checked answers said something untrue about you.`, act: "See what", go: () => go("ai-perception") }] : []),
    ...(v.suggested_competitors.length > 0 ? [{ key: "rivals", what: `${v.suggested_competitors.length} more brands are named next to you`, why: `${v.suggested_competitors.slice(0, 3).map((b) => b.name).join(", ")} and others. Say Yes or No to them on Competitors to see how you compare.`, act: "Review", go: () => go(v.suggested_competitors.some((b) => b.competitor_id) ? "competitors" : "ai-competitors") }] : []),
  ];
  return (
    <Section title="What to do next" note="The changes most likely to get you named and recommended, from your latest answers.">
      {items.length === 0 ? (
        <p className="text-muted-foreground">Nothing waiting. New fixes appear after each run.</p>
      ) : (
        <ol className="divide-y">
          {items.slice(0, 6).map((x, i) => (
            <li key={x.key} className="flex items-start justify-between gap-3 py-2">
              <span className="flex gap-3">
                <span className="mt-0.5 flex size-5 shrink-0 items-center justify-center rounded-full bg-muted font-mono text-[11px]">{i + 1}</span>
                <span><span className="font-medium">{x.what}</span><span className="block text-[12px] text-muted-foreground">{x.why}</span></span>
              </span>
              <Button variant="ghost" onClick={x.go}>{x.act}</Button>
            </li>
          ))}
        </ol>
      )}
    </Section>
  );
}
