import { useEffect, useState } from "react";
import type { Schemas, paths } from "@outflare/sdk";
import { api, errorMessage } from "../../api";
import { Badge, Button, ErrorText, Loading, Stat, Stats, field, toast } from "../../ui";
import { Frame, NotSetUp, Section, cash, downloadCSV, held, pct, th, type AIData, type Go, type Placement } from "./shared";
import { Bar, Leaderboard } from "./overview";
import { patchCohort } from "./settings";

type Fact = Schemas["AIFact"];
type Claim = Schemas["AIVisibilityClaim"];
type Claims = paths["/v1/brands/{brand}/ai-visibility/claims"]["get"]["responses"]["200"]["content"]["application/json"];
type BotHits = Schemas["AIBotHits"];

const noAnswers = (d: AIData) => !d.vis || d.vis.answers === 0;

export function AICompetitors({ ws, go }: { ws: string; go: Go }) {
  return (
    <Frame ws={ws} title="Competitors in AI answers" description="Who AI assistants name and recommend for your questions, and the brands they name that you don't count yet.">
      {(d) => (noAnswers(d) ? <NotSetUp d={d} go={go} /> : <CompetitorsBody ws={ws} d={d} go={go} />)}
    </Frame>
  );
}

function CompetitorsBody({ ws, d, go }: { ws: string; d: AIData; go: Go }) {
  const v = d.vis!;
  const c = d.cohort!;
  const [hidden, setHidden] = useState<string[]>([]);
  const board = [{ ...v.brand, you: true }, ...v.competitors.map((x) => ({ ...x, you: false }))].sort((a, b) => (b.score ?? 0) - (a.score ?? 0));
  async function count(name: string) {
    const competitors = [...c.competitors.map(({ name, domains, aliases }) => ({ name, domains, aliases })), { name }];
    if (await patchCohort(ws, c, { competitors })) {
      toast(`Counting ${name} from the next run.`);
      d.reload();
    }
  }
  const suggested = v.suggested_competitors.filter((b) => !hidden.includes(b.name));
  return (
    <>
      <Section title="Leaderboard" note={`${v.answers} answers in this period. Score: recommended counts most, named or listed less, warned against counts nothing.`}>
        <div className="overflow-x-auto"><Leaderboard rows={board} full /></div>
      </Section>
      <Section title="Also named in your answers" note="Brands the assistants name for your questions that you don't count yet. Yes counts them from the next run.">
        {suggested.length === 0 ? (
          <p className="text-muted-foreground">None right now.</p>
        ) : (
          <ul className="divide-y">
            {suggested.map((b) => (
              <li key={b.name} className="flex items-center justify-between gap-2 py-1.5">
                <span>{b.name} <span className="text-[12px] text-muted-foreground">in {b.answers} {b.answers === 1 ? "answer" : "answers"}</span></span>
                <span className="flex gap-1">
                  <Button variant="ghost" disabled={c.competitors.length >= 25} onClick={() => count(b.name)}>Yes, a competitor</Button>
                  <Button variant="ghost" onClick={() => setHidden((h) => [...h, b.name])}>No</Button>
                </span>
              </li>
            ))}
          </ul>
        )}
        <p className="mt-3 text-[12px] text-muted-foreground">
          Competitors on the Competitors page count everywhere in Outflare. <button className="underline" onClick={() => go("competitors")}>Manage them there</button>.
        </p>
      </Section>
    </>
  );
}

export function AISources({ ws, go }: { ws: string; go: Go }) {
  return (
    <Frame ws={ws} title="Sources" description="The pages AI assistants cite or read when they answer your questions, most valuable first. Getting named on them is the fastest way into the answers.">
      {(d) => (noAnswers(d) ? <NotSetUp d={d} go={go} /> : <SourcesBody d={d} go={go} />)}
    </Frame>
  );
}

function SourcesBody({ d, go }: { d: AIData; go: Go }) {
  const v = d.vis!;
  const [places, setPlaces] = useState<{ data: Placement[]; own_pages: { url: string; host: string; title: string; answers: number; questions: number }[] } | null>(null);
  useEffect(() => {
    api.GET("/v1/brands/{brand}/ai-visibility/placements", { params: { path: { brand: d.brand.id }, query: { days: d.days, cohort: d.cohort?.id } } }).then(({ data, error }) => {
      if (error) toast(errorMessage(error), "error");
      setPlaces(typeof data === "object" && data ? data : { data: [], own_pages: [] });
    });
  }, [d.brand.id, d.days, d.cohort?.id, d.running]);
  const total = v.source_types.reduce((n, s) => n + s.answers, 0);
  return (
    <>
      <Section title="Placements to get" note="Pages the answers cite or read, ranked by how much being named on each is worth: how many answers cite it, how many people ask those questions, and whether it names you already. Each one is a Presence opportunity." action={<span className="flex gap-2">{places && places.data.length > 0 && <Button variant="ghost" onClick={() => downloadCSV(d, "placements")}>Download CSV</Button>}<Button variant="ghost" onClick={() => go("coverage")}>Open Presence</Button></span>}>
        {!places ? <Loading /> : places.data.length === 0 ? <p className="text-muted-foreground">No pages cited in this period. Assistants with web search cite their sources.</p> : (
          <table className="w-full">
            <thead><tr><th className={th}>Page</th><th className={th}>Worth</th><th className={th}>Answers citing it</th><th className={th}>Names you</th><th className={th}>Who's named there</th><th className={th}>Kind</th></tr></thead>
            <tbody className="divide-y">
              {places.data.slice(0, 40).map((p) => (
                <tr key={p.url}>
                  <td className="max-w-md truncate py-1.5 pr-3"><a className="hover:underline" href={p.url} target="_blank" rel="noreferrer noopener">{p.title || p.url}</a> <span className="font-mono text-[12px] text-muted-foreground">{p.host}</span></td>
                  <td className="pr-3"><Bar value={p.value / 100} /></td>
                  <td className="pr-3 font-mono text-[12px]">{p.answers}{p.reads > 0 && <span className="text-muted-foreground"> · read {p.reads}×</span>}</td>
                  <td className="pr-3">{p.answers === 0 ? "–" : p.named === 0 ? <Badge tone="red">no</Badge> : p.named === p.answers ? <Badge tone="green">yes</Badge> : <span className="font-mono text-[12px]">{p.named} of {p.answers}</span>}</td>
                  <td className="pr-3 text-[12px]">{p.competitors.length === 0 ? <span className="text-muted-foreground">nobody tracked</span> : p.competitors.slice(0, 4).map((c) => `${c.name} (${c.answers})`).join(", ")}</td>
                  <td className="text-[12px] capitalize text-muted-foreground">{p.source_type || "–"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Section>
      {places && places.own_pages.length > 0 && (
        <Section title="Your pages the answers cite" note="Keep these accurate and current: assistants read them to answer.">
          <table className="w-full">
            <thead><tr><th className={th}>Page</th><th className={th}>Answers</th><th className={th}>Questions</th></tr></thead>
            <tbody className="divide-y">
              {places.own_pages.map((p) => (
                <tr key={p.url}>
                  <td className="max-w-md truncate py-1.5 pr-3"><a className="hover:underline" href={p.url} target="_blank" rel="noreferrer noopener">{p.title || p.url}</a></td>
                  <td className="pr-3 font-mono text-[12px]">{p.answers}</td>
                  <td className="font-mono text-[12px]">{p.questions}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </Section>
      )}
      <div className="grid gap-6 xl:grid-cols-[2fr_1fr]">
        <Section title="Sites the answers cite">
          {v.cited_hosts.length === 0 ? <p className="text-muted-foreground">No citations in this period. Assistants with web search cite their sources.</p> : (
            <table className="w-full">
              <thead><tr><th className={th}>Site</th><th className={th}>Answers</th><th className={th}>Whose</th></tr></thead>
              <tbody className="divide-y">
                {v.cited_hosts.map((h) => (
                  <tr key={h.host}>
                    <td className="py-1.5 pr-3 font-mono">{h.host}</td>
                    <td className="pr-3">{h.answers}</td>
                    <td>{h.owner === "brand" ? <Badge tone="green">yours</Badge> : h.owner === "competitor" ? <Badge tone="amber">{h.competitor}</Badge> : <span className="text-[12px] text-muted-foreground">{h.source_type || "other"}</span>}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </Section>
        <Section title="Kinds of source" note="What the cited pages are, across every answer.">
          {v.source_types.length === 0 ? <p className="text-muted-foreground">Not read yet.</p> : (
            <ul className="space-y-1.5">
              {v.source_types.slice().sort((a, b) => b.answers - a.answers).map((s) => (
                <li key={s.type} className="flex items-center justify-between gap-2"><span className="capitalize">{s.type}</span><Bar value={total ? s.answers / total : 0} tone="bg-muted-foreground/60" /></li>
              ))}
            </ul>
          )}
        </Section>
      </div>
    </>
  );
}

export function AIAccuracy({ ws, go }: { ws: string; go: Go }) {
  return (
    <Frame ws={ws} title="How AI describes you" description="How AI answers treat your brand, what is true about it, and which answers get it wrong. Answers that name you are checked against these facts after each run, on your AI model.">
      {(d) => <AccuracyBody ws={ws} d={d} go={go} />}
    </Frame>
  );
}

function AccuracyBody({ ws, d, go }: { ws: string; d: AIData; go: Go }) {
  const [facts, setFacts] = useState<Fact[] | null>(null);
  const [text, setText] = useState("");
  const [n, setN] = useState(0);
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/ai-facts", { params: { path: { ws }, query: { days: d.days, brand: d.brand.handle } } }).then(({ data }) => setFacts(data?.data ?? []));
  }, [ws, d.brand.handle, d.days, n]);
  async function add(e: React.FormEvent) {
    e.preventDefault();
    const { data, error } = await api.POST("/v1/workspaces/{ws}/ai-facts", { params: { path: { ws } }, body: { statement: text.trim(), brand_id: d.brand.id } });
    if (error || !data) return toast(errorMessage(error), "error");
    held(data);
    setText("");
    setN((x) => x + 1);
  }
  async function patch(f: Fact, status: "active" | "paused" | null) {
    const path = { ws, fact: f.id };
    const { error } = status
      ? await api.PATCH("/v1/workspaces/{ws}/ai-facts/{fact}", { params: { path }, body: { status } })
      : await api.DELETE("/v1/workspaces/{ws}/ai-facts/{fact}", { params: { path } });
    if (error) return toast(errorMessage(error), "error");
    setN((x) => x + 1);
  }
  const f = d.vis?.facts;
  const v = d.vis;
  const treated = v && v.answers > 0 ? [{ ...v.brand, you: true }, ...v.competitors.map((x) => ({ ...x, you: false }))].filter((e) => e.answers > 0).sort((a, b) => b.answers - a.answers) : [];
  return (
    <>
      {v && v.answers > 0 && (
        <>
          <Stats cols={4}>
            <Stat label="Named" value={String(v.brand.answers)} sub={`of ${v.answers} answers`} />
            <Stat label="Recommended" value={String(v.brand.recommended)} sub={pct(v.brand.recommend_rate)} tone="green" />
            <Stat label="Listed as one option" value={String(v.brand.listed)} />
            <Stat label="Warned against" value={String(v.brand.warned)} tone={v.brand.warned > 0 ? "red" : undefined} />
          </Stats>
          <Section title="How answers treat each brand" note="Read from each answer's words by your own AI model.">
            <table className="w-full">
              <thead><tr><th className={th}>Brand</th><th className={th}>Named</th><th className={th}>Recommended</th><th className={th}>Listed</th><th className={th}>Warned against</th></tr></thead>
              <tbody className="divide-y">
                {treated.map((e) => (
                  <tr key={e.name} className={e.you ? "font-semibold" : ""}>
                    <td className="py-1.5 pr-3">{e.name}{e.you && <span className="ml-1 text-[11px] font-normal text-muted-foreground">you</span>}</td>
                    <td className="pr-3">{e.answers}</td><td className="pr-3 text-green-600">{e.recommended}</td><td className="pr-3">{e.listed}</td><td className={e.warned > 0 ? "text-red-600" : ""}>{e.warned}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </Section>
        </>
      )}
      {v && v.answers > 0 && <ClaimsBody d={d} go={go} />}
      {f && f.checked_answers > 0 && (
        <Stats cols={3}>
          <Stat label="Answers right" value={pct(f.accuracy)} sub={`of ${f.checked_answers} checked`} tone={f.wrong_answers > 0 ? "amber" : "green"} />
          <Stat label="Answers with a mistake" value={String(f.wrong_answers)} />
          <Stat label="Facts AI gets wrong" value={String(f.facts_wrong)} sub={f.facts_wrong > 0 ? <button className="underline" onClick={() => go("fixes")}>See the fixes</button> : undefined} />
        </Stats>
      )}
      <Section title="Your facts">
        {facts?.length ? (
          <ul className="mb-3 divide-y rounded-md border">
            {facts.map((x) => (
              <li key={x.id} className="space-y-1 px-3 py-2">
                <div className="flex items-start justify-between gap-2">
                  <span className={x.status === "paused" ? "text-muted-foreground" : ""}>{x.statement}</span>
                  <span className="flex shrink-0 items-center gap-1">
                    {x.accuracy !== null && <Badge tone={x.contradicted > 0 ? "red" : "green"}>{`${pct(x.accuracy)} right`}</Badge>}
                    <Button variant="ghost" onClick={() => patch(x, x.status === "active" ? "paused" : "active")}>{x.status === "active" ? "Pause" : "Resume"}</Button>
                    <Button variant="danger" onClick={() => patch(x, null)}>Delete</Button>
                  </span>
                </div>
                {x.latest_wrong && <p className="text-[12px] text-muted-foreground">{x.latest_wrong.model} said: "{x.latest_wrong.quote}"</p>}
              </li>
            ))}
          </ul>
        ) : facts && <p className="mb-3 text-muted-foreground">No facts yet. Add the ones AI most often gets wrong: prices, plans, where you sell, what you don't do.</p>}
        <form className="flex gap-2" onSubmit={add}>
          <input className={`${field} flex-1`} aria-label="Fact" placeholder="Plans start at $49 a month" value={text} onChange={(e) => setText(e.target.value)} />
          <Button type="submit" disabled={text.trim().length < 5}>Add fact</Button>
        </form>
      </Section>
    </>
  );
}

const sentimentTone = { positive: "green", negative: "red", neutral: "grey" } as const;

// ClaimsBody is what the answers say about the brand: what it is known
// for, what is held against it (with where that comes from and a way to
// dispute it), and how the competitors are described.
function ClaimsBody({ d, go }: { d: AIData; go: Go }) {
  const [claims, setClaims] = useState<Claims | null>(null);
  const [n, setN] = useState(0);
  useEffect(() => {
    api.GET("/v1/brands/{brand}/ai-visibility/claims", { params: { path: { brand: d.brand.id }, query: { days: d.days, cohort: d.cohort?.id } } }).then(({ data, error }) => {
      if (error || !data) return toast(errorMessage(error), "error");
      setClaims(data);
    });
  }, [d.brand.id, d.days, d.cohort?.id, n]);
  async function dispute(c: Claim) {
    const { data, error } = await api.POST("/v1/brands/{brand}/ai-visibility/claims/dispute", { params: { path: { brand: d.brand.id } }, body: { key: c.key, kind: c.kind } });
    if (error || !data) return toast(errorMessage(error), "error");
    if (held(data)) return;
    toast("On the fixes list, with the pages to correct.");
    setN((x) => x + 1);
  }
  if (!claims) return <Loading />;
  const move = (c: Claim) => (c.previous_answers === 0 ? null : c.answers - c.previous_answers);
  const row = (c: Claim, dispute?: (c: Claim) => void) => (
    <li key={c.kind + c.key} className="space-y-1 px-3 py-2">
      <div className="flex items-start justify-between gap-2">
        <span className="flex flex-wrap items-center gap-2">
          <span>{c.text}</span>
          <Badge tone={sentimentTone[c.sentiment]}>{c.sentiment}</Badge>
          {c.fix_id && <button className="text-[11px] underline" onClick={() => go("fixes", c.fix_id!)}>Disputed</button>}
        </span>
        <span className="flex shrink-0 items-center gap-2 text-[12px] text-muted-foreground">
          <span title={`${c.answers} of the answers naming you, on ${c.questions} question${c.questions === 1 ? "" : "s"}`}>{pct(c.share)} of answers</span>
          {move(c) !== null && <span className={move(c)! > 0 ? "text-amber-700" : "text-green-700"}>{move(c)! > 0 ? "+" : ""}{move(c)} on the period before</span>}
          {dispute && !c.fix_id && <Button variant="ghost" onClick={() => dispute(c)}>Not true</Button>}
        </span>
      </div>
      {c.quote && <p className="text-[12px] text-muted-foreground">{c.assistants[0] ?? "An assistant"} said: "{c.quote}"</p>}
      {c.sources.length > 0 && (
        <p className="text-[12px] text-muted-foreground">
          Cited: {c.sources.map((s, i) => <span key={s.url}>{i > 0 && ", "}<a className="underline" href={s.url} target="_blank" rel="noreferrer">{s.host}</a></span>)}
        </p>
      )}
    </li>
  );
  return (
    <>
      <div className="grid gap-6 xl:grid-cols-2">
        <Section title="What AI says you're known for" note="The attributes the answers give you, most said first, with how that moved since the period before.">
          {claims.attributes.length === 0 ? <p className="text-muted-foreground">Nothing yet: the answers that name you haven't said what you are known for.</p> : <ul className="divide-y rounded-md border">{claims.attributes.map((c) => row(c))}</ul>}
        </Section>
        <Section title="What AI holds against you" note="The reasons the answers give against you, or for picking another instead. Mark one that isn't true and it goes on the fixes list with the pages to correct.">
          {claims.objections.length === 0 ? <p className="text-muted-foreground">No objections in this period.</p> : <ul className="divide-y rounded-md border">{claims.objections.map((c) => row(c, dispute))}</ul>}
        </Section>
      </div>
      {claims.competitors.length > 0 && (
        <Section title="How AI describes your competitors" note="What the answers say each competitor is known for, and what they hold against it.">
          <table className="w-full">
            <thead><tr><th className={th}>Competitor</th><th className={th}>Known for</th><th className={th}>Held against it</th></tr></thead>
            <tbody className="divide-y align-top">
              {claims.competitors.map((c) => (
                <tr key={c.name}>
                  <td className="py-1.5 pr-3 font-medium">{c.name}<div className="text-[11px] font-normal text-muted-foreground">{c.answers} answers</div></td>
                  <td className="py-1.5 pr-3">{c.attributes.map((a) => <span key={a.key} className="mr-1 mb-1 inline-block"><Badge tone={sentimentTone[a.sentiment]}>{a.text}</Badge></span>)}</td>
                  <td className="py-1.5">{c.objections.map((o) => <div key={o.key}>{o.text}</div>)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </Section>
      )}
    </>
  );
}

export function AITraffic({ ws, go }: { ws: string; go: Go }) {
  return (
    <Frame ws={ws} title="Traffic from AI" description="The visits, conversions and revenue AI assistants send your site, and the AI crawlers that read it.">
      {(d) => <TrafficBody ws={ws} d={d} go={go} />}
    </Frame>
  );
}

function TrafficBody({ ws, d, go }: { ws: string; d: AIData; go: Go }) {
  const t = d.vis?.traffic;
  const [hits, setHits] = useState<BotHits | null>(null);
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/ai-bot-hits", { params: { path: { ws }, query: { days: d.days } } }).then(({ data }) => setHits(data ?? null));
  }, [ws, d.days]);
  const revenue = t?.revenue.map((m) => cash(m.amount_cents, m.currency)).join(" · ");
  return (
    <>
      {t ? (
        <>
          <Stats cols={4}>
            <Stat label="Visits from AI" value={t.visits.toLocaleString()} />
            <Stat label="Conversions" value={t.conversions.toLocaleString(undefined, { maximumFractionDigits: 1 })} sub={`credited ${t.model.replaceAll("_", " ")}`} />
            <Stat label="Revenue" value={revenue || "–"} />
            <Stat label="Spent on AI answers" value={t.spend_cents > 0 ? cash(t.spend_cents, "USD") : "–"} sub="reading answers, this period" />
          </Stats>
          <div className="grid gap-6 xl:grid-cols-2">
            <Section title="By assistant">
              {t.sources.length === 0 ? <p className="text-muted-foreground">No visits from AI assistants in this period. Visits are counted by website tracking.</p> : (
                <table className="w-full"><thead><tr><th className={th}>Assistant</th><th className={th}>Visits</th><th className={th}>Conversions</th></tr></thead>
                  <tbody className="divide-y">{t.sources.map((s) => <tr key={s.name}><td className="py-1.5 pr-3">{s.name}</td><td className="pr-3">{s.visits}</td><td>{s.conversions.toLocaleString(undefined, { maximumFractionDigits: 1 })}</td></tr>)}</tbody>
                </table>
              )}
            </Section>
            <Section title="Pages they land on">
              {t.landing_pages.length === 0 ? <p className="text-muted-foreground">None yet.</p> : (
                <table className="w-full"><thead><tr><th className={th}>Page</th><th className={th}>Visits</th><th className={th}>Conversions</th></tr></thead>
                  <tbody className="divide-y">{t.landing_pages.map((s) => <tr key={s.name}><td className="max-w-xs truncate py-1.5 pr-3 font-mono text-[12px]">{s.name}</td><td className="pr-3">{s.visits}</td><td>{s.conversions.toLocaleString(undefined, { maximumFractionDigits: 1 })}</td></tr>)}</tbody>
                </table>
              )}
            </Section>
          </div>
        </>
      ) : (
        <p className="text-[13px] text-muted-foreground">Traffic shows once website tracking is on for {d.brand.name}. <button className="underline" onClick={() => go("tracking")}>Set up tracking</button></p>
      )}
      <Section title="AI crawlers" note="Whether AI crawlers can read your sites, from each site audit, and how often they visit, from your Cloudflare logs.">
        <p className="mb-3 text-muted-foreground">
          Site audits check what robots.txt lets each AI crawler read and whether you have an llms.txt. Problems go on the <button className="underline" onClick={() => go("fixes")}>fixes list</button>.
        </p>
        {!hits ? null : hits.connections.length === 0 ? (
          <p className="text-muted-foreground">To see visits, connect Cloudflare with an API token that can read your zones and their analytics. <button className="underline" onClick={() => go("connectors")}>Connect Cloudflare</button></p>
        ) : (
          <>
            {hits.connections.filter((c) => c.status === "error").map((c) => <ErrorText key={c.integration_id}>{c.message ?? "Reading Cloudflare's logs failed."}</ErrorText>)}
            {hits.data.length === 0 ? <p className="text-muted-foreground">No AI crawler visits in this period.</p> : (
              <table className="w-full">
                <thead><tr><th className={th}>Site</th><th className={th}>AI search</th><th className={th}>For a person</th><th className={th}>Training</th><th className={th}>Top crawlers</th></tr></thead>
                <tbody className="divide-y">
                  {hits.data.map((h) => (
                    <tr key={h.host}>
                      <td className="py-1.5 pr-3 font-mono">{h.host}</td><td className="pr-3">{h.search.toLocaleString()}</td><td className="pr-3">{h.user.toLocaleString()}</td><td className="pr-3">{h.training.toLocaleString()}</td>
                      <td className="text-muted-foreground">{h.bots.slice(0, 3).map((b) => `${b.token} ${b.requests.toLocaleString()}`).join(" · ")}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </>
        )}
      </Section>
    </>
  );
}
