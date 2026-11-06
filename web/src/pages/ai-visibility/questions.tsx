import { useEffect, useState } from "react";
import { api, errorMessage } from "../../api";
import { Badge, Button, Loading, field, toast } from "../../ui";
import { Frame, NotSetUp, RunBar, Section, Trend, awarenessLevels, awarenessName, delta, downloadCSV, pct, questionTypes, th, typeName, useQuestionResults, type AIData, type Cohort, type Go, type Prompt, type QuestionResult, type Sample } from "./shared";

// usePrompts loads a question set's questions.
export function usePrompts(ws: string, cohort: Cohort | null) {
  const [list, setList] = useState<Prompt[] | null>(null);
  const [n, setN] = useState(0);
  useEffect(() => {
    if (!cohort) return setList([]);
    api.GET("/v1/workspaces/{ws}/ai-cohorts/{cohort}/prompts", { params: { path: { ws, cohort: cohort.id } } }).then(({ data }) => setList(data?.data ?? []));
  }, [ws, cohort, n]);
  return { list, reload: () => setN((x) => x + 1) };
}

// Drafting asks the workspace's AI for questions; they arrive as suggestions.
export async function draftQuestions(ws: string, cohort: Cohort, personas: string[] = []) {
  const { data, error } = await api.POST("/v1/workspaces/{ws}/ai-cohorts/{cohort}/prompts/draft", { params: { path: { ws, cohort: cohort.id } }, body: { personas } });
  if (error || !data) {
    toast(errorMessage(error), "error");
    return false;
  }
  if (!("queued" in data)) {
    toast("Held for approval.");
    return false;
  }
  return true;
}

export async function addQuestions(ws: string, cohort: Cohort, texts: string[]) {
  const prompts = texts.map((t) => t.trim()).filter((t) => t.length >= 3).map((text) => ({ text }));
  if (prompts.length === 0) return true;
  const { error } = await api.POST("/v1/workspaces/{ws}/ai-cohorts/{cohort}/prompts", { params: { path: { ws, cohort: cohort.id } }, body: { prompts } });
  if (error) toast(errorMessage(error), "error");
  return !error;
}

export async function setPrompt(ws: string, cohort: Cohort, p: Prompt, status: "active" | "paused" | null) {
  const path = { ws, cohort: cohort.id, prompt: p.id };
  const { error } = status
    ? await api.PATCH("/v1/workspaces/{ws}/ai-cohorts/{cohort}/prompts/{prompt}", { params: { path }, body: { status } })
    : await api.DELETE("/v1/workspaces/{ws}/ai-cohorts/{cohort}/prompts/{prompt}", { params: { path } });
  if (error) toast(errorMessage(error), "error");
  return !error;
}

function Tags({ p }: { p: Prompt }) {
  return (
    <>
      {p.topic && <Badge>{p.topic}</Badge>} {p.awareness && <Badge tone="blue">{awarenessName(p.awareness)}</Badge>} {p.question_type && <Badge>{typeName(p.question_type)}</Badge>}
    </>
  );
}

// Suggestions lists drafted questions to keep or drop, with a way to draft
// more and to type your own. The setup and the Questions page share it.
export function Suggestions({ ws, cohort, list, reload, onChange }: { ws: string; cohort: Cohort; list: Prompt[]; reload: () => void; onChange?: () => void }) {
  const [drafting, setDrafting] = useState(false);
  const [typed, setTyped] = useState("");
  const suggested = list.filter((p) => p.status === "suggested");
  async function draft() {
    if (!(await draftQuestions(ws, cohort))) return;
    setDrafting(true);
    // Drafting takes under a minute; look twice.
    setTimeout(reload, 20_000);
    setTimeout(() => { reload(); setDrafting(false); }, 45_000);
  }
  async function keep(ps: Prompt[], status: "active" | null) {
    for (const p of ps) await setPrompt(ws, cohort, p, status);
    reload();
    onChange?.();
  }
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <Button onClick={draft} disabled={drafting}>{drafting ? "Drafting questions…" : "Suggest questions with AI"}</Button>
        <span className="text-[12px] text-muted-foreground">Uses your AI model, your competitors, your Search Console searches and, with DataForSEO, what people really ask AI.</span>
      </div>
      {suggested.length > 0 && (
        <div>
          <div className="mb-1 flex items-center justify-between">
            <span className="text-[11px] text-muted-foreground uppercase">Suggested ({suggested.length})</span>
            <span className="flex gap-1">
              <Button variant="ghost" onClick={() => keep(suggested, "active")}>Keep all</Button>
              <Button variant="ghost" onClick={() => keep(suggested, null)}>Drop all</Button>
            </span>
          </div>
          <ul className="max-h-96 divide-y overflow-y-auto rounded-md border">
            {suggested.map((p) => (
              <li key={p.id} className="flex items-start justify-between gap-2 px-3 py-2">
                <span>
                  {p.text} <Tags p={p} />
                  {p.demand !== null && <span className="text-muted-foreground"> · about {p.demand.toLocaleString()} {p.demand_source === "ai_search" ? "AI questions" : "searches"} a month</span>}
                  {p.why && <span className="block text-[12px] text-muted-foreground">{p.why}</span>}
                </span>
                <span className="flex shrink-0 gap-1">
                  <Button variant="ghost" onClick={() => keep([p], "active")}>Keep</Button>
                  <Button variant="danger" onClick={() => keep([p], null)}>Drop</Button>
                </span>
              </li>
            ))}
          </ul>
        </div>
      )}
      <form className="grid gap-2" onSubmit={async (e) => { e.preventDefault(); if (await addQuestions(ws, cohort, typed.split("\n"))) { setTyped(""); reload(); onChange?.(); } }}>
        <textarea className={`${field} h-20 py-1.5`} aria-label="Your own questions" value={typed} onChange={(e) => setTyped(e.target.value)} placeholder={"Or type your own, one per line:\nWhat is the best residential proxy provider?\nAcme vs Northpeak: which is cheaper?"} />
        <div><Button variant="ghost" type="submit" disabled={typed.trim().length < 3}>Add these questions</Button></div>
      </form>
    </div>
  );
}

export function AIQuestions({ ws, go, item }: { ws: string; go: Go; item?: string }) {
  return (
    <Frame ws={ws} title="Questions" description="The questions your buyers ask AI, grouped by topic, how much the asker knows and what they ask for. Open one to read what each assistant answered.">
      {(d) => <QuestionsBody ws={ws} d={d} go={go} open={item} />}
    </Frame>
  );
}

function QuestionsBody({ ws, d, go, open }: { ws: string; d: AIData; go: Go; open?: string }) {
  const { list, reload } = usePrompts(ws, d.cohort);
  const { results } = useQuestionResults(d);
  const [topic, setTopic] = useState("");
  const [aw, setAw] = useState("");
  const [qt, setQt] = useState("");
  const [q, setQ] = useState("");
  const [sort, setSort] = useState<"asked" | "lost" | "gained" | "missing" | "won">(open === "missing" ? "missing" : "asked");
  if (!d.cohort) return <NotSetUp d={d} go={go} />;
  if (!list) return <Loading />;
  const picked = open ? list.find((p) => p.id === open) : undefined;
  if (picked) return <Question ws={ws} d={d} p={picked} r={results?.find((r) => r.id === picked.id)} back={() => go("ai-questions")} />;

  const live = list.filter((p) => p.status !== "suggested");
  const topics = [...new Set(live.map((p) => p.topic).filter(Boolean))].sort();
  const byID = new Map((results ?? []).map((r) => [r.id, r]));
  const shown = live
    .filter((p) => (!topic || p.topic === topic) && (!aw || p.awareness === aw) && (!qt || p.question_type === qt) && (!q || p.text.toLowerCase().includes(q.toLowerCase())))
    .map((p) => ({ p, r: byID.get(p.id) }))
    .sort((a, b) => {
      const ra = a.r, rb = b.r;
      switch (sort) {
        case "lost": return (ra?.change ?? 0) - (rb?.change ?? 0);
        case "gained": return (rb?.change ?? 0) - (ra?.change ?? 0);
        case "missing": return (ra?.mention_rate ?? 2) - (rb?.mention_rate ?? 2) || (rb?.answers ?? 0) - (ra?.answers ?? 0);
        case "won": return (rb?.score ?? -1) - (ra?.score ?? -1);
        default: return (rb?.demand ?? a.p.demand ?? -1) - (ra?.demand ?? b.p.demand ?? -1);
      }
    });
  const cohort = d.cohort;
  const missing = (results ?? []).filter((r) => r.answers > 0 && r.named === 0).length;
  return (
    <>
      <RunBar ws={ws} d={d} />
      <Section title={`Tracked questions (${live.filter((p) => p.status === "active").length})`} note={results && results.some((r) => r.answers > 0) ? `${missing} of them never named you in the last ${d.days} days. Change is the score now against the ${d.days} days before; the bars are the last weeks.` : "Each run asks every active question to every assistant."}
        action={results && results.some((r) => r.answers > 0) ? <Button variant="ghost" onClick={() => downloadCSV(d, "questions")}>Download CSV</Button> : undefined}>
        <div className="mb-3 flex flex-wrap gap-2">
          <input className={`${field} w-56`} placeholder="Search questions" aria-label="Search questions" value={q} onChange={(e) => setQ(e.target.value)} />
          <select className={field} aria-label="Sort" value={sort} onChange={(e) => setSort(e.target.value as typeof sort)}>
            <option value="asked">Most asked first</option><option value="missing">Not naming you first</option><option value="lost">Biggest drops first</option><option value="gained">Biggest gains first</option><option value="won">Best score first</option>
          </select>
          {topics.length > 0 && (
            <select className={field} aria-label="Topic" value={topic} onChange={(e) => setTopic(e.target.value)}>
              <option value="">All topics</option>{topics.map((t) => <option key={t}>{t}</option>)}
            </select>
          )}
          <select className={field} aria-label="Awareness" value={aw} onChange={(e) => setAw(e.target.value)}>
            <option value="">Anyone asking</option>{awarenessLevels.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
          </select>
          <select className={field} aria-label="Question type" value={qt} onChange={(e) => setQt(e.target.value)}>
            <option value="">Any question type</option>{questionTypes.map((t) => <option key={t.id} value={t.id}>{t.name}</option>)}
          </select>
        </div>
        {shown.length === 0 ? (
          <p className="text-muted-foreground">No questions match.</p>
        ) : (
          <table className="w-full">
            <thead><tr><th className={th}>Question</th><th className={th}>Names you</th><th className={th}>Recommends you</th><th className={th}>Change</th><th className={th}>Last weeks</th><th className={th}>Asked by</th><th className={th}>Demand</th><th className={th} /></tr></thead>
            <tbody className="divide-y">
              {shown.map(({ p, r }) => (
                <tr key={p.id} className={p.status === "paused" ? "text-muted-foreground" : ""}>
                  <td className="py-2 pr-3"><button className="text-left hover:underline" onClick={() => go("ai-questions", p.id)}>{p.text}</button> {p.topic && <Badge>{p.topic}</Badge>}</td>
                  <td className="pr-3 whitespace-nowrap">{!r || r.answers === 0 ? <span className="text-[12px] text-muted-foreground">not asked yet</span> : r.named === 0 ? <Badge tone="red">no</Badge> : <span className="font-mono text-[12px]">{pct(r.mention_rate)}</span>}</td>
                  <td className="pr-3 font-mono text-[12px]">{!r || r.answers === 0 ? "–" : pct(r.recommend_rate)}</td>
                  <td className={`pr-3 font-mono text-[12px] ${r?.change ? (r.change > 0 ? "text-green-700" : "text-red-700") : ""}`}>{delta(r?.change)}</td>
                  <td className="pr-3"><Trend weeks={r?.weeks ?? []} /></td>
                  <td className="pr-3 text-[12px] whitespace-nowrap">{awarenessName(p.awareness) || "–"}{p.question_type && <span className="text-muted-foreground"> · {typeName(p.question_type)}</span>}</td>
                  <td className="pr-3 font-mono text-[12px]">{p.demand === null ? "–" : p.demand.toLocaleString()}</td>
                  <td className="text-right whitespace-nowrap">
                    <Button variant="ghost" onClick={async () => { if (await setPrompt(ws, cohort, p, p.status === "active" ? "paused" : "active")) { reload(); d.reload(); } }}>{p.status === "active" ? "Pause" : "Resume"}</Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Section>
      <Section title="Add questions">
        <Suggestions ws={ws} cohort={cohort} list={list} reload={reload} onChange={d.reload} />
      </Section>
    </>
  );
}

const stanceTone = { recommended: "green", listed: "grey", warned: "red" } as const;

// Question shows one question's latest answers, side by side by assistant.
// History shows, week by week, whether the brand was shown for one
// question, by every assistant and by each one, so a person who worked on
// a placement sees whether it moved.
function History({ d, r }: { d: AIData; r: QuestionResult }) {
  return (
    <div className="grid gap-4 lg:grid-cols-2">
      <Section title="Shown over time" note={`${r.weeks.length} weeks of answers, oldest first. Change since the ${d.days} days before: ${delta(r.change)}.`}>
        <table className="w-full">
          <thead><tr><th className={th}>Week</th><th className={th}>Answers</th><th className={th}>Named you</th><th className={th}>Recommended you</th><th className={th}>Score</th></tr></thead>
          <tbody className="divide-y">
            {r.weeks.map((w) => (
              <tr key={w.week}>
                <td className="py-1 pr-3 font-mono text-[12px]">{w.week}</td>
                <td className="pr-3">{w.answers}</td>
                <td className="pr-3">{w.named === 0 ? <Badge tone="red">no</Badge> : <span className="font-mono text-[12px]">{w.named} of {w.answers}</span>}</td>
                <td className="pr-3 font-mono text-[12px]">{w.recommended}</td>
                <td className="font-mono text-[12px]">{w.score === null ? "–" : Math.round(w.score)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </Section>
      <Section title="By assistant and who else is named" note={`Over the last ${d.days} days.`}>
        <table className="w-full">
          <thead><tr><th className={th}>Assistant</th><th className={th}>Answers</th><th className={th}>Named you</th><th className={th}>Recommended you</th></tr></thead>
          <tbody className="divide-y">
            {r.by_assistant.map((m) => (
              <tr key={m.surface + m.provider + m.model + m.web_search}>
                <td className="py-1 pr-3">{m.surface === "screen" ? (m.model === "chatgpt" ? "ChatGPT" : "Gemini") : <span className="font-mono text-[12px]">{m.model}</span>}</td>
                <td className="pr-3">{m.answers}</td>
                <td className="pr-3 font-mono text-[12px]">{pct(m.mention_rate)}</td>
                <td className="font-mono text-[12px]">{pct(m.recommend_rate)}</td>
              </tr>
            ))}
          </tbody>
        </table>
        {(r.competitors.length > 0 || r.other_brands.length > 0) && (
          <p className="mt-3 flex flex-wrap items-center gap-1 text-[12px]">
            <span className="text-muted-foreground">Named instead or beside you:</span>
            {r.competitors.map((c) => <Badge key={c.name} tone={c.recommended > 0 ? "green" : "grey"}>{c.name} · {c.recommended > 0 ? `recommended ${c.recommended}×` : `${c.answers}×`}</Badge>)}
            {r.other_brands.map((b) => <Badge key={b.name} tone="amber">{b.name} · {b.answers}× (not tracked)</Badge>)}
          </p>
        )}
      </Section>
    </div>
  );
}

// WhatChanged lists the questions the brand gained and lost since the
// period before, and how many never name it, for the overview.
export function WhatChanged({ d, go }: { d: AIData; go: Go }) {
  const { results } = useQuestionResults(d);
  if (!results) return null;
  const moved = results.filter((r) => r.change !== null && r.previous);
  const gained = moved.filter((r) => (r.change ?? 0) >= 20).sort((a, b) => (b.change ?? 0) - (a.change ?? 0)).slice(0, 5);
  const lost = moved.filter((r) => (r.change ?? 0) <= -20).sort((a, b) => (a.change ?? 0) - (b.change ?? 0)).slice(0, 5);
  const missing = results.filter((r) => r.answers > 0 && r.named === 0);
  const row = (r: QuestionResult) => (
    <li key={r.id} className="flex items-center justify-between gap-2 py-1">
      <button className="truncate text-left hover:underline" onClick={() => go("ai-questions", r.id)}>{r.text}</button>
      <span className={`shrink-0 font-mono text-[12px] ${(r.change ?? 0) > 0 ? "text-green-700" : "text-red-700"}`}>{delta(r.change)}</span>
    </li>
  );
  return (
    <Section title="What changed" note={moved.length === 0 ? `Gains and losses show once there are answers from the ${d.days} days before this period.` : `Against the ${d.days} days before. A change of 20 points or more.`}
      action={missing.length > 0 ? <Button variant="ghost" onClick={() => go("ai-questions", "missing")}>{missing.length} never name you</Button> : undefined}>
      {moved.length === 0 ? (
        <p className="text-muted-foreground">{missing.length > 0 ? `${missing.length} of ${results.length} questions never named you. Start there.` : "Nothing to compare yet."}</p>
      ) : gained.length === 0 && lost.length === 0 ? (
        <p className="text-muted-foreground">No question moved by 20 points or more.</p>
      ) : (
        <div className="grid gap-4 md:grid-cols-2">
          <div><div className="text-[11px] uppercase text-muted-foreground">Gained</div>{gained.length === 0 ? <p className="text-muted-foreground">None.</p> : <ul className="divide-y">{gained.map(row)}</ul>}</div>
          <div><div className="text-[11px] uppercase text-muted-foreground">Lost</div>{lost.length === 0 ? <p className="text-muted-foreground">None.</p> : <ul className="divide-y">{lost.map(row)}</ul>}</div>
        </div>
      )}
    </Section>
  );
}

function Question({ ws, d, p, r, back }: { ws: string; d: AIData; p: Prompt; r?: QuestionResult; back: () => void }) {
  const [samples, setSamples] = useState<Sample[] | null>(null);
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/ai-samples", { params: { path: { ws }, query: { prompt_id: p.id, status: "ok", limit: 40 } } }).then(({ data }) => setSamples(data?.data ?? []));
  }, [ws, p.id]);
  // The newest answer from each assistant.
  const latest = (samples ?? []).filter((s, i, all) => all.findIndex((x) => x.provider === s.provider && x.model === s.model && x.surface === s.surface) === i);
  const named = (samples ?? []).filter((s) => s.brand_named).length;
  return (
    <>
      <div>
        <button onClick={back} className="mb-1.5 font-mono text-[12px] text-muted-foreground hover:text-foreground">← All questions</button>
        <h2 className="text-[15px] font-semibold">{p.text}</h2>
        <p className="mt-1 text-[12px] text-muted-foreground"><Tags p={p} />{p.why && ` ${p.why}`}</p>
      </div>
      {!samples ? <Loading /> : samples.length === 0 ? (
        <p className="text-[13px] text-muted-foreground">Not asked yet. It's asked on the next run.</p>
      ) : (
        <>
          <p className="text-[13px]">{d.brand.name} was named in {named} of the last {samples.length} answers ({pct(named / samples.length)}).</p>
          {r && r.answers > 0 && <History d={d} r={r} />}
          <div className="grid gap-4 lg:grid-cols-2">
            {latest.map((s) => {
              const you = s.mentions.find((m) => m.entity === "brand");
              return (
                <section key={s.id} className="rounded-lg border bg-card text-[13px] shadow-xs">
                  <header className="flex flex-wrap items-center justify-between gap-2 border-b px-4 py-2">
                    <span className="font-semibold">{s.surface === "screen" ? (s.model === "chatgpt" ? "ChatGPT" : "Gemini") : s.model}</span>
                    <span className="flex items-center gap-1">
                      {you ? <Badge tone={you.stance ? stanceTone[you.stance] : "green"}>{you.stance ? `${you.stance} · #${you.position}` : `named #${you.position}`}</Badge> : <Badge tone="amber">not named</Badge>}
                      <span className="text-[11px] text-muted-foreground">{new Date(s.sampled_at).toLocaleDateString()}</span>
                    </span>
                  </header>
                  <div className="space-y-3 p-4">
                    {s.mentions.length > 0 && (
                      <div className="flex flex-wrap gap-1">
                        {s.mentions.slice().sort((a, b) => a.position - b.position).map((m) => (
                          <Badge key={m.name} tone={m.entity === "brand" ? "blue" : m.stance === "recommended" ? "green" : m.stance === "warned" ? "red" : "grey"}>{`${m.position}. ${m.name}`}</Badge>
                        ))}
                      </div>
                    )}
                    {you?.stance_quote && <p className="border-l-2 pl-2 text-[12px] italic text-muted-foreground">"{you.stance_quote}"</p>}
                    <details>
                      <summary className="cursor-pointer text-[12px] text-muted-foreground">Read the answer</summary>
                      <p className="mt-2 max-h-80 overflow-y-auto whitespace-pre-wrap text-[12px]">{s.answer}</p>
                    </details>
                    {s.citations.length > 0 && (
                      <div>
                        <div className="text-[11px] text-muted-foreground uppercase">Sources it cited</div>
                        <ul className="mt-1 space-y-0.5 text-[12px]">
                          {s.citations.slice(0, 8).map((c) => (
                            <li key={c.url} className="truncate">
                              <a className="hover:underline" href={c.url} target="_blank" rel="noreferrer noopener">{c.title || c.url}</a>{" "}
                              {c.owner === "brand" && <Badge tone="green">yours</Badge>}{c.owner === "competitor" && <Badge tone="amber">{c.competitor}</Badge>}
                            </li>
                          ))}
                        </ul>
                      </div>
                    )}
                    {s.check_url && <a className="text-[12px] underline" href={s.check_url} target="_blank" rel="noreferrer noopener">Ask it yourself</a>}
                  </div>
                </section>
              );
            })}
          </div>
        </>
      )}
    </>
  );
}
