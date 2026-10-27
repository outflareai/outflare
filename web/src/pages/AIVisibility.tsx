import { useEffect, useState } from "react";
import type { Integration, Schemas } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, Empty, ErrorText, Loading, PageHeader, Stat, Stats, field, toast } from "../ui";
import type { Tab } from "../App";

// AI answers: how often AI assistants name each brand when asked the
// questions in its cohorts, against its competitors; whether what they say
// matches the brand's facts; and whether AI crawlers can read and do visit
// its sites. Every answer runs on the workspace's own AI connections.

type Props = { ws: string; go: (tab: Tab, item?: string) => void };
type Visibility = Schemas["AIVisibility"];
type Cohort = Schemas["AICohort"];
type Fact = Schemas["AIFact"];
type Settings = Schemas["AIVisibilitySettings"];
type BotHits = Schemas["AIBotHits"];

const aiConnectors = ["claude", "openai", "gemini", "mistral", "openrouter"];
const pct = (v?: number | null) => (v === undefined || v === null ? "–" : `${Math.round(v * 100)}%`);
const usd = (cents: number) => `$${(cents / 100).toFixed(2)}`;
const held = (data: object) => "queued" in data && toast("Held for approval by the mission's owner.");

export function AIVisibility({ ws, go }: Props) {
  const { query, brand } = useBrand(ws);
  const brandKey = query.brand ?? "";
  const [days, setDays] = useState(28);
  const [vis, setVis] = useState<Visibility[] | null>(null);
  const [cohorts, setCohorts] = useState<Cohort[]>([]);
  const [settings, setSettings] = useState<Settings | null>(null);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);
  const [n, setN] = useState(0);
  const reload = () => setN((x) => x + 1);

  useEffect(() => {
    const q = { ...(brandKey ? { brand: brandKey } : {}) };
    Promise.all([
      api.GET("/v1/workspaces/{ws}/ai-visibility", { params: { path: { ws }, query: { ...q, days } } }),
      api.GET("/v1/workspaces/{ws}/ai-cohorts", { params: { path: { ws }, query: q } }),
      api.GET("/v1/workspaces/{ws}/ai-visibility/settings", { params: { path: { ws } } }),
    ]).then(([v, c, s]) => {
      const err = v.error ?? c.error ?? s.error;
      if (err || !v.data || !c.data || !s.data) return setError(errorMessage(err));
      setError("");
      setVis(v.data.data);
      setCohorts(c.data.data);
      setSettings(s.data);
    });
  }, [ws, brandKey, days, n]);

  // Follow a run until it finishes.
  const running = cohorts.some((c) => c.last_run && (c.last_run.status === "queued" || c.last_run.status === "running"));
  useEffect(() => {
    if (!running) return;
    const t = setTimeout(reload, 5000);
    return () => clearTimeout(t);
  }, [running, n]);

  const header = (
    <PageHeader
      title="AI visibility"
      description="How often ChatGPT, Claude, Gemini and others name your brand when people ask about what you sell, whether they get your facts right, and whether AI crawlers can read your site."
      action={
        <select className={field} aria-label="Period" value={days} onChange={(e) => setDays(Number(e.target.value))}>
          {[7, 28, 90].map((d) => <option key={d} value={d}>Last {d} days</option>)}
        </select>
      }
    />
  );
  if (error) return <div className="space-y-6">{header}<ErrorText>{error}</ErrorText></div>;
  if (!vis || !settings) return <div className="space-y-6">{header}<Loading /></div>;

  const noModels = settings.models.length === 0;
  return (
    <div className="space-y-6">
      {header}
      <ModelsCard ws={ws} settings={settings} onSaved={setSettings} go={go} />
      {!noModels && cohorts.length === 0 && !adding && (
        <Empty title="Add the questions your buyers ask AI" action={<Button onClick={() => setAdding(true)}>Add questions</Button>}>
          A cohort is a set of questions such as "What is the best proxy provider for scraping?", with the competitors to count against. Each run asks every model each question and counts who the answers name and cite.
        </Empty>
      )}
      {adding && <NewCohort ws={ws} brandId={brand?.id ?? null} onDone={() => { setAdding(false); reload(); }} />}
      {cohorts.map((c) => (
        <CohortCard key={c.id} ws={ws} cohort={c} vis={vis.find((v) => v.cohort.id === c.id)} canRun={!noModels} onChange={reload} go={go} />
      ))}
      {cohorts.length > 0 && !adding && <Button variant="ghost" onClick={() => setAdding(true)}>Add a cohort</Button>}
      <FactsCard ws={ws} brandKey={brandKey} brandId={brand?.id ?? null} days={days} />
      <CrawlersCard ws={ws} days={days} go={go} />
    </div>
  );
}

function ModelsCard({ ws, settings, onSaved, go }: { ws: string; settings: Settings; onSaved: (s: Settings) => void; go: Props["go"] }) {
  const [conns, setConns] = useState<Integration[]>([]);
  const [conn, setConn] = useState("");
  const [model, setModel] = useState("");
  const [search, setSearch] = useState(true);
  const [limit, setLimit] = useState(String(settings.monthly_limit_cents / 100));
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/integrations", { params: { path: { ws } } }).then(({ data }) => {
      const ai = (data?.data ?? []).filter((i) => aiConnectors.includes(i.connector));
      setConns(ai);
      setConn((c) => c || ai[0]?.id || "");
    });
  }, [ws]);

  async function save(models: Schemas["AISampleModelInput"][], monthly = settings.monthly_limit_cents) {
    const { data, error } = await api.PUT("/v1/workspaces/{ws}/ai-visibility/settings", { params: { path: { ws } }, body: { models, monthly_limit_cents: monthly } });
    if (error || !data) return toast(errorMessage(error), "error");
    if (held(data)) return;
    onSaved(data as Settings);
    setModel("");
    toast("Saved.");
  }
  const current = settings.models.map(({ integration_id, model, web_search }) => ({ integration_id, model, web_search }));

  return (
    <Card
      title="Models"
      description={`Each one is asked on your own AI connection and billed to it. ${usd(settings.spent_this_month_cents)} of ${usd(settings.monthly_limit_cents)} spent this month.`}
    >
      {conns.length === 0 ? (
        <p className="text-[13px] text-muted-foreground">
          Connect an AI provider first: OpenRouter reaches ChatGPT, Gemini, Perplexity and more with one key, or connect Claude, OpenAI, Gemini or Mistral directly.{" "}
          <button className="underline" onClick={() => go("connectors")}>Connect one</button>
        </p>
      ) : (
        <div className="space-y-3">
          {settings.models.length > 0 && (
            <ul className="divide-y rounded-md border text-[13px]">
              {settings.models.map((m, i) => (
                <li key={i} className="flex items-center justify-between gap-2 px-3 py-2">
                  <span>
                    <span className="font-mono">{m.model}</span> <span className="text-muted-foreground">on {m.provider_name}</span>
                    {m.web_search && <Badge tone="blue">web search</Badge>} {!m.connected && <Badge tone="red">connection removed</Badge>}
                  </span>
                  <Button variant="ghost" onClick={() => save(current.filter((_, j) => j !== i))}>Remove</Button>
                </li>
              ))}
            </ul>
          )}
          {settings.models.length < settings.max_models && (
            <form className="flex flex-wrap items-center gap-2" onSubmit={(e) => { e.preventDefault(); if (model.trim()) save([...current, { integration_id: conn, model: model.trim(), web_search: search }]); }}>
              <select className={field} aria-label="Connection" value={conn} onChange={(e) => setConn(e.target.value)}>
                {conns.map((c) => <option key={c.id} value={c.id}>{c.name || c.connector_name}</option>)}
              </select>
              <input className={`${field} w-56`} aria-label="Model" placeholder="Model, such as openai/gpt-5-mini" value={model} onChange={(e) => setModel(e.target.value)} />
              <label className="flex items-center gap-1.5 text-[13px]"><input type="checkbox" checked={search} onChange={(e) => setSearch(e.target.checked)} /> Web search</label>
              <Button type="submit" disabled={!model.trim()}>Add model</Button>
            </form>
          )}
          <form className="flex items-center gap-2 text-[13px]" onSubmit={(e) => { e.preventDefault(); save(current, Math.round(Number(limit) * 100)); }}>
            <label htmlFor="ai-limit" className="text-muted-foreground">Monthly limit (US$)</label>
            <input id="ai-limit" className={`${field} w-24`} type="number" min={0} step="1" value={limit} onChange={(e) => setLimit(e.target.value)} />
            <Button variant="ghost" type="submit">Save</Button>
          </form>
        </div>
      )}
    </Card>
  );
}

function NewCohort({ ws, brandId, onDone }: { ws: string; brandId: string | null; onDone: () => void }) {
  const [name, setName] = useState("");
  const [questions, setQuestions] = useState("");
  const [rivals, setRivals] = useState("");
  const [schedule, setSchedule] = useState<"off" | "daily" | "weekly">("weekly");
  const [busy, setBusy] = useState(false);

  async function create(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    // One competitor per line: "Northpeak, northpeak.example".
    const competitors = rivals.split("\n").map((l) => l.split(",").map((p) => p.trim())).filter((p) => p[0]).map(([n, ...d]) => ({ name: n, domains: d.filter(Boolean) }));
    const c = await api.POST("/v1/workspaces/{ws}/ai-cohorts", { params: { path: { ws } }, body: { name: name.trim(), brand_id: brandId, competitors, schedule } });
    if (c.error || !c.data) { setBusy(false); return toast(errorMessage(c.error), "error"); }
    if (held(c.data)) { setBusy(false); return onDone(); }
    const prompts = questions.split("\n").map((t) => t.trim()).filter((t) => t.length >= 3).map((text) => ({ text }));
    if (prompts.length > 0) {
      const p = await api.POST("/v1/workspaces/{ws}/ai-cohorts/{cohort}/prompts", { params: { path: { ws, cohort: (c.data as Cohort).id } }, body: { prompts } });
      if (p.error) toast(errorMessage(p.error), "error");
    }
    setBusy(false);
    onDone();
  }

  return (
    <Card title="New cohort" action={<Button variant="ghost" onClick={onDone}>Cancel</Button>}>
      <form className="grid gap-3 text-[13px]" onSubmit={create}>
        <label className="grid gap-1">Name<input className={field} required value={name} onChange={(e) => setName(e.target.value)} placeholder="Buyers comparing providers" /></label>
        <label className="grid gap-1">Questions, one per line<textarea className={`${field} h-28 py-1.5`} value={questions} onChange={(e) => setQuestions(e.target.value)} placeholder={"What is the best residential proxy provider?\nAcme vs Northpeak: which is cheaper?"} /></label>
        <label className="grid gap-1">Other competitors, one per line, with their websites (the ones you track on the Competitors page are counted already)<textarea className={`${field} h-20 py-1.5`} value={rivals} onChange={(e) => setRivals(e.target.value)} placeholder="Northpeak, northpeak.example" /></label>
        <label className="flex items-center gap-2">Ask
          <select className={field} value={schedule} onChange={(e) => setSchedule(e.target.value as typeof schedule)}>
            <option value="weekly">every week</option><option value="daily">every day</option><option value="off">only when I run it</option>
          </select>
        </label>
        <div><Button type="submit" disabled={busy || !name.trim()}>{busy ? "Adding…" : "Add cohort"}</Button></div>
      </form>
    </Card>
  );
}

function CohortCard({ ws, cohort, vis, canRun, onChange, go }: { ws: string; cohort: Cohort; vis?: Visibility; canRun: boolean; onChange: () => void; go: Props["go"] }) {
  const run = cohort.last_run;
  const active = run && (run.status === "queued" || run.status === "running");
  async function start() {
    const { data, error } = await api.POST("/v1/workspaces/{ws}/ai-cohorts/{cohort}/runs", { params: { path: { ws, cohort: cohort.id } } });
    if (error || !data) return toast(errorMessage(error), "error");
    if (held(data)) return;
    toast("Asking now. Answers come in over the next few minutes.");
    onChange();
  }
  const status = !run ? "Not run yet"
    : active ? `Asking: ${run.samples_done} of ${run.samples_planned} answers`
    : `Last run ${new Date(run.finished_at ?? run.created_at).toLocaleDateString()}, ${usd(run.cost_cents)}${run.reason && run.reason !== "stopped" ? ` (stopped early: ${run.reason.replaceAll("_", " ")})` : ""}`;

  return (
    <Card
      title={cohort.name}
      description={`${cohort.prompts} questions · ${cohort.schedule === "off" ? "runs when asked" : `runs ${cohort.schedule}`} · ${status}`}
      action={<Button variant="ghost" disabled={!canRun || !!active || cohort.prompts === 0} onClick={start}>Run now</Button>}
    >
      {!vis || vis.answers === 0 ? (
        <p className="text-[13px] text-muted-foreground">No answers in this period yet.{cohort.prompts === 0 && " Add questions to this cohort first."}</p>
      ) : (
        <div className="space-y-4">
          <Stats>
            <Stat label="Mention rate" value={pct(vis.brand.mention_rate)} sub={`of ${vis.answers} answers`} />
            <Stat label="Share of voice" value={pct(vis.brand.share_of_voice)} sub="against competitors" />
            <Stat label="Cited" value={pct(vis.brand.citation_rate)} sub="answers citing your pages" />
            <Stat label="Facts right" value={pct(vis.facts.accuracy)} sub={vis.facts.checked_answers ? `${vis.facts.wrong_answers} of ${vis.facts.checked_answers} got one wrong` : "add facts below"} tone={vis.facts.wrong_answers > 0 ? "amber" : undefined} />
          </Stats>
          <div className="grid gap-4 lg:grid-cols-2">
            <table className="w-full text-[13px]">
              <thead className="text-left text-[11px] text-muted-foreground uppercase"><tr><th className="py-1 font-medium">Brand</th><th className="font-medium">Named</th><th className="font-medium">Share</th><th className="font-medium">Position</th></tr></thead>
              <tbody className="divide-y">
                {[{ ...vis.brand, you: true }, ...vis.competitors.map((c) => ({ ...c, you: false }))].sort((a, b) => (b.mention_rate ?? 0) - (a.mention_rate ?? 0)).map((e) => (
                  <tr key={e.name} className={e.you ? "font-semibold" : ""}>
                    <td className="py-1.5">{e.name}</td><td>{pct(e.mention_rate)}</td><td>{pct(e.share_of_voice)}</td><td>{e.avg_position?.toFixed(1) ?? "–"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            <table className="w-full text-[13px]">
              <thead className="text-left text-[11px] text-muted-foreground uppercase"><tr><th className="py-1 font-medium">Model</th><th className="font-medium">Answers</th><th className="font-medium">Names you</th></tr></thead>
              <tbody className="divide-y">
                {vis.models.map((m) => (
                  <tr key={m.provider + m.model + m.web_search}><td className="py-1.5 font-mono">{m.model}{m.web_search && <span className="text-muted-foreground"> + search</span>}</td><td>{m.answers}</td><td>{pct(m.mention_rate)}</td></tr>
                ))}
              </tbody>
            </table>
          </div>
          {vis.cited_hosts.length > 0 && (
            <div className="text-[13px]">
              <div className="mb-1 text-[11px] text-muted-foreground uppercase">Sites the answers cite</div>
              <div className="flex flex-wrap gap-x-4 gap-y-1">
                {vis.cited_hosts.map((h) => (
                  <span key={h.host} className="font-mono">
                    {h.host} <span className="text-muted-foreground">{h.answers}</span>
                    {h.owner === "brand" && <Badge tone="green">yours</Badge>}{h.owner === "competitor" && <Badge tone="amber">{h.competitor}</Badge>}
                  </span>
                ))}
              </div>
            </div>
          )}
          {(cohort.competitors.length > 0 || cohort.tracked_competitors.length > 0) && (
            <p className="text-[12px] text-muted-foreground">
              Counting {[...cohort.competitors, ...cohort.tracked_competitors].map((c) => c.name).join(", ")}.{" "}
              <button className="underline" onClick={() => go("competitors")}>Manage competitors</button>
            </p>
          )}
          {vis.open_fixes > 0 && <button className="text-[13px] text-muted-foreground hover:text-foreground hover:underline" onClick={() => go("fixes")}>{vis.open_fixes} open {vis.open_fixes === 1 ? "fix" : "fixes"} from these answers</button>}
        </div>
      )}
    </Card>
  );
}

function FactsCard({ ws, brandKey, brandId, days }: { ws: string; brandKey: string; brandId: string | null; days: number }) {
  const [facts, setFacts] = useState<Fact[] | null>(null);
  const [text, setText] = useState("");
  const [n, setN] = useState(0);
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/ai-facts", { params: { path: { ws }, query: { days, ...(brandKey ? { brand: brandKey } : {}) } } }).then(({ data }) => setFacts(data?.data ?? []));
  }, [ws, brandKey, days, n]);

  async function add(e: React.FormEvent) {
    e.preventDefault();
    const { data, error } = await api.POST("/v1/workspaces/{ws}/ai-facts", { params: { path: { ws } }, body: { statement: text.trim(), brand_id: brandId } });
    if (error || !data) return toast(errorMessage(error), "error");
    held(data);
    setText("");
    setN((x) => x + 1);
  }
  async function patch(f: Fact, status: "active" | "paused") {
    const { error } = await api.PATCH("/v1/workspaces/{ws}/ai-facts/{fact}", { params: { path: { ws, fact: f.id } }, body: { status } });
    if (error) return toast(errorMessage(error), "error");
    setN((x) => x + 1);
  }
  async function remove(f: Fact) {
    const { error } = await api.DELETE("/v1/workspaces/{ws}/ai-facts/{fact}", { params: { path: { ws, fact: f.id } } });
    if (error) return toast(errorMessage(error), "error");
    setN((x) => x + 1);
  }

  return (
    <Card title="Facts" description="What is true about the brand. After each run, answers that name it are checked against these, on your AI model.">
      <div className="space-y-3 text-[13px]">
        {facts?.length ? (
          <ul className="divide-y rounded-md border">
            {facts.map((f) => (
              <li key={f.id} className="space-y-1 px-3 py-2">
                <div className="flex items-start justify-between gap-2">
                  <span className={f.status === "paused" ? "text-muted-foreground" : ""}>{f.statement}</span>
                  <span className="flex shrink-0 items-center gap-1">
                    {f.accuracy !== null && <Badge tone={f.contradicted > 0 ? "red" : "green"}>{pct(f.accuracy)} right</Badge>}
                    <Button variant="ghost" onClick={() => patch(f, f.status === "active" ? "paused" : "active")}>{f.status === "active" ? "Pause" : "Resume"}</Button>
                    <Button variant="danger" onClick={() => remove(f)}>Delete</Button>
                  </span>
                </div>
                {f.latest_wrong && <p className="text-[12px] text-muted-foreground">{f.latest_wrong.model} said: "{f.latest_wrong.quote}"</p>}
              </li>
            ))}
          </ul>
        ) : facts && <p className="text-muted-foreground">No facts yet. Add the ones AI most often gets wrong: prices, plans, where you sell, what you don't do.</p>}
        <form className="flex gap-2" onSubmit={add}>
          <input className={`${field} flex-1`} aria-label="Fact" placeholder="Plans start at $49 a month" value={text} onChange={(e) => setText(e.target.value)} />
          <Button type="submit" disabled={text.trim().length < 5}>Add fact</Button>
        </form>
      </div>
    </Card>
  );
}

function CrawlersCard({ ws, days, go }: { ws: string; days: number; go: Props["go"] }) {
  const [hits, setHits] = useState<BotHits | null>(null);
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/ai-bot-hits", { params: { path: { ws }, query: { days } } }).then(({ data }) => setHits(data ?? null));
  }, [ws, days]);
  if (!hits) return null;
  const failing = hits.connections.filter((c) => c.status === "error");

  return (
    <Card title="AI crawlers" description="Whether AI crawlers can read your sites, from each site audit, and how often they visit, from your Cloudflare logs.">
      <div className="space-y-3 text-[13px]">
        <p className="text-muted-foreground">
          Site audits check what robots.txt lets each AI crawler read and whether you have an llms.txt; problems go on the{" "}
          <button className="underline" onClick={() => go("fixes")}>fixes list</button>. <button className="underline" onClick={() => go("audits")}>Open site audits</button>
        </p>
        {hits.connections.length === 0 ? (
          <p className="text-muted-foreground">
            To see visits, connect Cloudflare Web Analytics with an API token that can also read your zones and their analytics.{" "}
            <button className="underline" onClick={() => go("connectors")}>Connect Cloudflare</button>
          </p>
        ) : (
          <>
            {failing.map((c) => <ErrorText key={c.integration_id}>{c.message ?? "Reading Cloudflare's logs failed."}</ErrorText>)}
            {hits.data.length === 0 ? (
              <p className="text-muted-foreground">No AI crawler visits in this period{hits.connections.some((c) => c.status === "pending") ? " yet: the first read runs with the next analytics sync" : ""}.</p>
            ) : (
              <table className="w-full">
                <thead className="text-left text-[11px] text-muted-foreground uppercase"><tr><th className="py-1 font-medium">Site</th><th className="font-medium">AI search</th><th className="font-medium">For a person</th><th className="font-medium">Training</th><th className="font-medium">Top crawlers</th></tr></thead>
                <tbody className="divide-y">
                  {hits.data.map((h) => (
                    <tr key={h.host}>
                      <td className="py-1.5 font-mono">{h.host}</td><td>{h.search.toLocaleString()}</td><td>{h.user.toLocaleString()}</td><td>{h.training.toLocaleString()}</td>
                      <td className="text-muted-foreground">{h.bots.slice(0, 3).map((b) => `${b.token} ${b.requests.toLocaleString()}`).join(" · ")}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </>
        )}
      </div>
    </Card>
  );
}
