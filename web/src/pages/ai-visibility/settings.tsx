import { useEffect, useState } from "react";
import type { Integration, Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../../api";
import { Badge, Button, CopyButton, confirmAction, field, toast } from "../../ui";
import { ago } from "../../format";
import { Frame, Section, held, marketName, markets, screenName, usd, type AIData, type Cohort, type Go, type Settings } from "./shared";

const aiConnectors = ["claude", "openai", "gemini", "mistral", "xai", "deepseek", "openrouter", "vercel_ai_gateway", "requesty"];

// Assistants: which AI assistants answer the brand's questions, each on the
// workspace's own account, and the monthly limit.
export function Assistants({ ws, brandId, settings, onSaved, go }: { ws: string; brandId: string; settings: Settings; onSaved: (s: Settings) => void; go: Go }) {
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
    const { data, error } = await api.PUT("/v1/brands/{brand}/ai-visibility/settings", { params: { path: { brand: brandId } }, body: { models, monthly_limit_cents: monthly } });
    if (error || !data) return toast(errorMessage(error), "error");
    if (held(data)) return;
    onSaved(data as Settings);
    setModel("");
    toast("Saved.");
  }
  const current = settings.models.map(({ integration_id, surface, model, web_search }) => ({ integration_id, surface, model, web_search }));
  const screensLeft = settings.screens.filter((sc) => !settings.models.some((m) => m.surface === "screen" && m.integration_id === sc.integration_id && m.model === sc.model));
  const full = settings.models.length >= settings.max_models;

  return (
    <div className="space-y-4 text-[13px]">
      {settings.models.length > 0 && (
        <ul className="divide-y rounded-md border">
          {settings.models.map((m, i) => (
            <li key={i} className="flex items-center justify-between gap-2 px-3 py-2">
              <span>
                {m.surface === "screen" ? (
                  <><span className="font-medium">{screenName(m.model)}</span> <span className="text-muted-foreground">as people see it, through {m.provider_name}</span></>
                ) : (
                  <><span className="font-mono">{m.model}</span> <span className="text-muted-foreground">on {m.provider_name}</span></>
                )}{" "}
                {m.web_search && <Badge tone="blue">web search</Badge>} {!m.connected && <Badge tone="red">connection removed</Badge>}
              </span>
              <Button variant="ghost" onClick={() => save(current.filter((_, j) => j !== i))}>Remove</Button>
            </li>
          ))}
        </ul>
      )}
      <div className="grid gap-3 md:grid-cols-2">
        <div className="rounded-md border p-3">
          <p className="font-medium">What a real person sees</p>
          <p className="mt-0.5 text-[12px] text-muted-foreground">ChatGPT and Gemini as they answer a signed-out person, read through your own DataForSEO account. About US$0.004 an answer.</p>
          <div className="mt-2 flex flex-wrap gap-2">
            {settings.screens.length === 0 ? (
              <Button variant="ghost" onClick={() => go("connectors")}>Connect DataForSEO</Button>
            ) : screensLeft.length === 0 ? (
              <span className="text-[12px] text-muted-foreground">Both added.</span>
            ) : (
              screensLeft.map((sc) => (
                <Button key={sc.integration_id + sc.model} variant="ghost" disabled={full}
                  onClick={() => save([...current, { integration_id: sc.integration_id, surface: "screen", model: sc.model, web_search: true }])}>
                  Add {sc.name}
                </Button>
              ))
            )}
          </div>
        </div>
        <div className="rounded-md border p-3">
          <p className="font-medium">Through an AI provider's API</p>
          <p className="mt-0.5 text-[12px] text-muted-foreground">Claude, Perplexity, Grok and more, on your own key. OpenRouter reaches most of them with one key.</p>
          {conns.length === 0 ? (
            <div className="mt-2"><Button variant="ghost" onClick={() => go("connectors")}>Connect an AI provider</Button></div>
          ) : (
            <form className="mt-2 flex flex-wrap items-center gap-2" onSubmit={(e) => { e.preventDefault(); if (model.trim()) save([...current, { integration_id: conn, model: model.trim(), web_search: search }]); }}>
              <select className={field} aria-label="Connection" value={conn} onChange={(e) => setConn(e.target.value)}>
                {conns.map((c) => <option key={c.id} value={c.id}>{c.name || c.connector_name}</option>)}
              </select>
              <input className={`${field} w-52`} aria-label="Model" placeholder="such as perplexity/sonar" value={model} onChange={(e) => setModel(e.target.value)} />
              <label className="flex items-center gap-1.5"><input type="checkbox" checked={search} onChange={(e) => setSearch(e.target.checked)} /> Web search</label>
              <Button type="submit" disabled={!model.trim() || full}>Add</Button>
            </form>
          )}
        </div>
      </div>
      <form className="flex flex-wrap items-center gap-2" onSubmit={(e) => { e.preventDefault(); save(current, Math.round(Number(limit) * 100)); }}>
        <label htmlFor="ai-limit" className="text-muted-foreground">Stop asking once this month's answers cost (US$)</label>
        <input id="ai-limit" className={`${field} w-24`} type="number" min={0} step="1" value={limit} onChange={(e) => setLimit(e.target.value)} />
        <Button variant="ghost" type="submit">Save</Button>
        <span className="text-[12px] text-muted-foreground">{usd(settings.spent_this_month_cents)} so far this month.</span>
      </form>
    </div>
  );
}

export async function patchCohort(ws: string, c: Cohort, body: { name?: string; schedule?: "off" | "daily" | "weekly"; competitors?: Schemas["AIEntityInput"][] }) {
  const { data, error } = await api.PATCH("/v1/workspaces/{ws}/ai-cohorts/{cohort}", { params: { path: { ws, cohort: c.id } }, body });
  if (error || !data) {
    toast(errorMessage(error), "error");
    return false;
  }
  return !held(data);
}

export function AIVisibilitySettings({ ws, go }: { ws: string; go: Go }) {
  return (
    <Frame ws={ws} title="AI visibility settings" description="Which assistants answer your questions, how often they're asked, and how much it may cost. Everything runs on your own accounts." period={false}>
      {(d) => <SettingsBody ws={ws} d={d} go={go} />}
    </Frame>
  );
}

function SettingsBody({ ws, d, go }: { ws: string; d: AIData; go: Go }) {
  const [settings, setSettings] = useState(d.settings);
  const c = d.cohort;
  return (
    <>
      <Section title="Assistants" note="Each answer runs on your own account and is billed to it.">
        <Assistants ws={ws} brandId={d.brand.id} settings={settings} onSaved={setSettings} go={go} />
      </Section>
      {c && (
        <Section title="How often">
          <label className="flex items-center gap-2">
            Ask every question
            <select className={field} value={c.schedule} onChange={async (e) => { if (await patchCohort(ws, c, { schedule: e.target.value as Cohort["schedule"] })) d.reload(); }}>
              <option value="weekly">every week</option><option value="daily">every day</option><option value="off">only when I press Run</option>
            </select>
          </label>
        </Section>
      )}
      <Markets ws={ws} d={d} go={go} />
      <ShareByLink brandId={d.brand.id} />
      <Section title="Set up again" note="Walk through competitors, questions and assistants again.">
        <Button variant="ghost" onClick={() => go("ai-visibility", "setup")}>Open the setup</Button>
      </Section>
    </>
  );
}

// Markets lists the brand's question sets by market and adds one for a
// new market: the same competitors and aliases, questions drafted in that
// market's language on the setup.
function Markets({ ws, d, go }: { ws: string; d: AIData; go: Go }) {
  const [locale, setLocale] = useState("");
  const [busy, setBusy] = useState(false);
  const taken = new Set(d.cohorts.map((c) => c.locale));
  async function add(e: React.FormEvent) {
    e.preventDefault();
    if (!locale) return;
    setBusy(true);
    const from = d.cohort;
    const { data, error } = await api.POST("/v1/workspaces/{ws}/ai-cohorts", {
      params: { path: { ws } },
      body: { name: `${d.brand.name} · ${marketName(locale)}`, brand_id: d.brand.id, locale, aliases: from?.aliases ?? [], competitors: from?.competitors ?? [], schedule: from?.schedule ?? "off" },
    });
    setBusy(false);
    if (error || !data) return toast(errorMessage(error), "error");
    if (held(data)) return;
    setLocale("");
    d.reload();
    go("ai-visibility", "setup");
  }
  async function setMarket(c: Cohort, value: string) {
    const { data, error } = await api.PATCH("/v1/workspaces/{ws}/ai-cohorts/{cohort}", { params: { path: { ws, cohort: c.id } }, body: { locale: value } });
    if (error || !data) return toast(errorMessage(error), "error");
    if (!held(data)) d.reload();
  }
  return (
    <Section title="Markets" note="One question set per market: the questions are asked, and drafted, in that market's language and place. Switch markets at the top of every page.">
      <ul className="mb-3 divide-y rounded-md border">
        {d.cohorts.map((c) => (
          <li key={c.id} className="flex items-center justify-between gap-2 px-3 py-2">
            <span>{c.name}<span className="ml-2 text-[12px] text-muted-foreground">{c.prompts} questions</span></span>
            <select className={field} aria-label={`Market of ${c.name}`} value={c.locale ?? ""} onChange={(e) => setMarket(c, e.target.value)}>
              <option value="">Any market</option>
              {markets.map((m) => <option key={m} value={m}>{marketName(m)}</option>)}
            </select>
          </li>
        ))}
      </ul>
      <form className="flex flex-wrap items-center gap-2" onSubmit={add}>
        <select className={field} aria-label="New market" value={locale} onChange={(e) => setLocale(e.target.value)}>
          <option value="">Add a market…</option>
          {markets.filter((m) => !taken.has(m)).map((m) => <option key={m} value={m}>{marketName(m)}</option>)}
        </select>
        <Button type="submit" disabled={!locale || busy}>{busy ? "Adding…" : "Add the market"}</Button>
      </form>
    </Section>
  );
}

// ShareByLink makes, shows and takes back the brand's AI visibility share
// link: anyone with it sees the overview, the trend and the questions for
// the last 28 days, read-only.
function ShareByLink({ brandId }: { brandId: string }) {
  const [share, setShare] = useState<Schemas["DashboardShare"] | null | undefined>(undefined);
  const [password, setPassword] = useState("");
  const [days, setDays] = useState("0");
  const [url, setUrl] = useState("");
  const [busy, setBusy] = useState(false);
  const load = () => api.GET("/v1/brands/{brand}/ai-visibility/share", { params: { path: { brand: brandId } } }).then(({ data }) => setShare(data?.share ?? null));
  useEffect(() => {
    load();
  }, [brandId]); // eslint-disable-line react-hooks/exhaustive-deps

  async function make() {
    setBusy(true);
    const { data, error, response } = await api.POST("/v1/brands/{brand}/ai-visibility/share", { params: { path: { brand: brandId } }, body: { password: password || undefined, expires_in_days: Number(days) || undefined } });
    setBusy(false);
    if (held(response)) return;
    if (!data || !("url" in data)) return toast(errorMessage(error), "error");
    setUrl(data.url);
    setPassword("");
    toast(share ? "New link made; the old one stopped working" : "Link made");
    load();
  }

  async function revoke() {
    if (!(await confirmAction("Take the link back?", "Anyone who has it sees nothing from now on.", "Take back"))) return;
    const { error, response } = await api.DELETE("/v1/brands/{brand}/ai-visibility/share", { params: { path: { brand: brandId } } });
    if (held(response)) return;
    if (error || !response.ok) return toast(errorMessage(error), "error");
    setUrl("");
    toast("Link taken back");
    load();
  }

  return (
    <Section title="Share by link" note="Anyone with the link sees your AI visibility score, the trend against competitors and each question's results for the last 28 days, read-only, and nothing else. Add a password so the link alone isn't enough.">
      <div className="space-y-3">
        {url && (
          <div className="flex items-center gap-2 rounded-md border bg-muted/40 p-2">
            <code className="min-w-0 flex-1 truncate font-mono text-[12px]" title={url}>{url}</code>
            <CopyButton text={url} label="Copy link" />
          </div>
        )}
        {share && !url && (
          <p className="text-muted-foreground">
            A link exists{share.has_password ? " with a password" : ""}
            {share.expires_at ? `, until ${share.expires_at.slice(0, 10)}` : ""}; opened {share.views} {share.views === 1 ? "time" : "times"}
            {share.last_viewed_at ? `, last ${ago(share.last_viewed_at)}` : ""}. Making a new one replaces it.
          </p>
        )}
        <div className="grid gap-2 sm:grid-cols-2">
          <label className="block space-y-1">
            <span className="text-muted-foreground">Password (optional)</span>
            <input className={`${field} w-full`} type="text" autoComplete="off" value={password} onChange={(e) => setPassword(e.target.value)} minLength={8} maxLength={200} placeholder="At least 8 characters" />
          </label>
          <label className="block space-y-1">
            <span className="text-muted-foreground">Expires</span>
            <select className={`${field} w-full`} value={days} onChange={(e) => setDays(e.target.value)}>
              <option value="0">Never</option>
              <option value="7">In 7 days</option>
              <option value="30">In 30 days</option>
              <option value="90">In 90 days</option>
              <option value="365">In a year</option>
            </select>
          </label>
        </div>
        <div className="flex items-center justify-between gap-2">
          {share ? (
            <button type="button" className="text-[12px] text-muted-foreground hover:text-destructive" onClick={revoke}>
              Take the link back
            </button>
          ) : (
            <span />
          )}
          <Button type="button" variant="ghost" disabled={busy || share === undefined || (password !== "" && password.length < 8)} onClick={make}>
            {share ? "Make a new link" : "Make a link"}
          </Button>
        </div>
      </div>
    </Section>
  );
}
