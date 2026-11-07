import { useEffect, useState, type ReactNode } from "react";
import type { Brand, Schemas, paths } from "@outflare/sdk";
import { useBrand, pickedBrandId } from "@/lib/brand";
import { PickBrand } from "@/components/pick-brand";
import { api, errorMessage } from "../../api";
import { Badge, Button, ErrorText, Loading, PageHeader, field, toast } from "../../ui";
import { track } from "@/lib/analytics";
import type { Tab } from "../../App";

// AI visibility: how AI assistants answer the questions a brand's buyers
// ask, against its competitors. Each brand has one set of tracked
// questions (the API calls it a cohort); the pages of the AI visibility
// menu read it through useAIVisibility.

export type Go = (tab: Tab, item?: string) => void;
export type Visibility = Schemas["AIVisibility"];
export type Entity = Schemas["AIVisibilityEntity"];
export type Cohort = Schemas["AICohort"];
export type Settings = Schemas["AIVisibilitySettings"];
export type Prompt = Schemas["AIPrompt"];
export type Sample = Schemas["AISample"];
export type QuestionResult = Schemas["AIVisibilityQuestion"];
export type Placement = Schemas["AIVisibilityPlacement"];
export type Series = paths["/v1/brands/{brand}/ai-visibility/series"]["get"]["responses"]["200"]["content"]["application/json"];

export const pct = (v?: number | null) => (v === undefined || v === null ? "–" : `${Math.round(v * 100)}%`);
export const usd = (cents: number) => `$${(cents / 100).toFixed(2)}`;
export const cash = (cents: number, currency: string) => new Intl.NumberFormat(undefined, { style: "currency", currency }).format(cents / 100);
export const score = (v?: number | null) => (v === undefined || v === null ? "–" : String(Math.round(v)));
export const held = (data: object) => "queued" in data && toast("Held for approval by the mission's owner.");
export const screenName = (model: string) => (model === "chatgpt" ? "ChatGPT" : model === "gemini" ? "Gemini" : model);

export const awarenessLevels = [
  { id: "problem", name: "Knows the problem" },
  { id: "solution", name: "Knows the solution" },
  { id: "competitor", name: "Knows a competitor" },
  { id: "brand", name: "Knows you" },
] as const;
export const questionTypes = [
  { id: "how_to", name: "How do I" },
  { id: "fix", name: "How do I fix" },
  { id: "best", name: "Who's best" },
  { id: "alternatives", name: "Alternatives to" },
  { id: "versus", name: "X vs Y" },
  { id: "supplier", name: "Best supplier for" },
  { id: "about", name: "About you" },
] as const;
export const awarenessName = (id: string) => awarenessLevels.find((a) => a.id === id)?.name ?? "";
export const typeName = (id: string) => questionTypes.find((q) => q.id === id)?.name ?? "";

// What one answer costs on the workspace's own accounts, roughly: a screen
// read through DataForSEO is about US$0.004; an API answer with web search
// on a mid-size model about US$0.01. Only for the estimate before a run.
export const answerCents = (m: { surface?: string }) => (m.surface === "screen" ? 0.4 : 1);

export type AIData = {
  brand: Brand;
  vis: Visibility | null;
  cohort: Cohort | null;
  cohorts: Cohort[];
  settings: Settings;
  days: number;
  running: boolean;
  reload: () => void;
};

// useAIVisibility loads the picked brand's question set, its results for
// the period and its settings, and follows a run until it finishes.
export function useAIVisibility(ws: string, days: number) {
  const { brand } = useBrand(ws);
  const [vis, setVis] = useState<Visibility[] | null>(null);
  const [cohorts, setCohorts] = useState<Cohort[]>([]);
  const [settings, setSettings] = useState<Settings | null>(null);
  const [picked, setPicked] = useState("");
  const [error, setError] = useState("");
  const [n, setN] = useState(0);
  const reload = () => setN((x) => x + 1);

  useEffect(() => {
    if (!brand) return;
    Promise.all([
      api.GET("/v1/brands/{brand}/ai-visibility", { params: { path: { brand: brand.id }, query: { days } } }),
      api.GET("/v1/brands/{brand}/ai-cohorts", { params: { path: { brand: brand.id } } }),
      api.GET("/v1/brands/{brand}/ai-visibility/settings", { params: { path: { brand: brand.id } } }),
    ]).then(([v, c, s]) => {
      const err = v.error ?? c.error ?? s.error;
      if (err || !v.data || !c.data || !s.data) return setError(errorMessage(err));
      setError("");
      setVis(v.data.data);
      setCohorts(c.data.data.filter((x) => x.brand_id === brand.id));
      setSettings(s.data);
    });
  }, [ws, brand, days, n]);

  const cohort = cohorts.find((c) => c.id === picked) ?? cohorts[0] ?? null;
  const running = !!cohort?.last_run && (cohort.last_run.status === "queued" || cohort.last_run.status === "running");
  useEffect(() => {
    if (!running) return;
    const t = setTimeout(reload, 5000);
    return () => clearTimeout(t);
  }, [running, n]);

  return {
    brand,
    error,
    loaded: !!vis && !!settings,
    data: brand && vis && settings ? ({ brand, vis: cohort ? vis.find((v) => v.cohort.id === cohort.id) ?? null : null, cohort, cohorts, settings, days, running, reload } satisfies AIData) : null,
    pick: setPicked,
    setSettings,
  };
}

// Frame is every AI visibility page: its header with the period and, when a
// brand has more than one question set, which one; then the page.
export function Frame({ ws, title, description, period = true, children }: { ws: string; title: string; description: string; period?: boolean; children: (d: AIData) => ReactNode }) {
  const { active } = useBrand(ws);
  const [days, setDays] = useState(28);
  const { brand, error, data, pick } = useAIVisibility(ws, days);
  const header = (
    <PageHeader
      title={title}
      description={description}
      action={
        <div className="flex items-center gap-2">
          {data && data.cohorts.length > 1 && (
            <select className={field} aria-label="Question set" value={data.cohort?.id ?? ""} onChange={(e) => pick(e.target.value)}>
              {data.cohorts.map((c) => <option key={c.id} value={c.id}>{setLabel(c, data.cohorts)}</option>)}
            </select>
          )}
          {period && (
            <select className={field} aria-label="Period" value={days} onChange={(e) => setDays(Number(e.target.value))}>
              {[7, 28, 90].map((d) => <option key={d} value={d}>Last {d} days</option>)}
            </select>
          )}
        </div>
      }
    />
  );
  if (!brand) return <div className="space-y-6">{header}<PickBrand what="AI visibility results" brands={active.length} /></div>;
  if (error) return <div className="space-y-6">{header}<ErrorText>{error}</ErrorText></div>;
  if (!data) return <div className="space-y-6">{header}<Loading /></div>;
  return <div className="space-y-6">{header}{children(data)}</div>;
}

// NotSetUp stands in for a results page before the brand's first run.
export function NotSetUp({ d, go }: { d: AIData; go: Go }) {
  const asking = d.running && d.cohort?.last_run;
  return (
    <div className="rounded-lg border border-dashed px-4 py-8 text-center text-[13px]">
      {asking ? (
        <p>Asking now: {asking.samples_done} of {asking.samples_planned} answers read. Results show here as they come in.</p>
      ) : (
        <>
          <p className="font-medium">No answers yet for {d.brand.name}</p>
          <p className="mt-1 text-muted-foreground">Set up takes about three minutes: confirm your competitors, pick the questions, choose the assistants, and run.</p>
          <div className="mt-4"><Button onClick={() => go("ai-visibility")}>Set up AI visibility</Button></div>
        </>
      )}
    </div>
  );
}

// RunBar says where the brand's question set stands and runs it.
export function RunBar({ d }: { ws: string; d: AIData }) {
  const c = d.cohort;
  if (!c) return null;
  const run = c.last_run;
  async function start() {
    const { data, error } = await api.POST("/v1/brands/{brand}/ai-cohorts/{cohort}/runs", { params: { path: { brand: pickedBrandId(), cohort: c!.id } } });
    if (error || !data) return toast(errorMessage(error), "error");
    if (held(data)) return;
    toast("Asking now. Answers come in over the next few minutes.");
    d.reload();
  }
  const planned = run?.samples_planned ?? 0;
  return (
    <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border bg-card px-4 py-2.5 text-[13px]">
      {d.running && run ? (
        <div className="flex min-w-0 flex-1 items-center gap-3">
          <span className="whitespace-nowrap">Asking: {run.samples_done} of {planned} answers</span>
          <div className="h-1.5 max-w-64 flex-1 overflow-hidden rounded bg-muted"><div className="h-full bg-primary transition-all" style={{ width: `${planned ? (run.samples_done / planned) * 100 : 5}%` }} /></div>
        </div>
      ) : (
        <span className="text-muted-foreground">
          {c.prompts} {c.prompts === 1 ? "question" : "questions"} · {d.settings.models.length} {d.settings.models.length === 1 ? "assistant" : "assistants"} · {c.schedule === "off" ? "asked when you run it" : `asked every ${c.schedule === "daily" ? "day" : "week"}`}
          {run && ` · last asked ${new Date(run.finished_at ?? run.created_at).toLocaleDateString()} for ${usd(run.cost_cents + Math.ceil(run.cost_micros / 10_000))}`}
          {run?.reason && run.reason !== "stopped" && <> <Badge tone="amber">{`stopped early: ${run.reason.replaceAll("_", " ")}`}</Badge></>}
        </span>
      )}
      <Button variant="ghost" disabled={d.running || c.prompts === 0 || d.settings.models.length === 0} onClick={start}>Run now</Button>
    </div>
  );
}

// Section is a titled block inside a page, lighter than a card.
// downloadCSV saves a table of the brand's AI visibility (the questions
// or the placements) as the CSV the API writes for the same period.
export async function downloadCSV(d: AIData, table: "questions" | "placements") {
  const path = table === "questions" ? "/v1/brands/{brand}/ai-visibility/questions" : "/v1/brands/{brand}/ai-visibility/placements";
  const res = await api.GET(path, { params: { path: { brand: d.brand.id }, query: { days: d.days, cohort: d.cohort?.id, format: "csv" } }, parseAs: "blob" });
  if (res.error || !(res.data instanceof Blob)) return toast(errorMessage(res.error), "error");
  const name = /filename="([^"]+)"/.exec(res.response.headers.get("Content-Disposition") ?? "")?.[1] ?? `ai-visibility-${table}.csv`;
  const url = URL.createObjectURL(res.data);
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  URL.revokeObjectURL(url);
  track("ui.ai_visibility_csv_downloaded", { table });
  toast("Downloaded");
}

export function Section({ title, action, children, note }: { title: string; action?: ReactNode; note?: ReactNode; children: ReactNode }) {
  return (
    <section className="rounded-lg border bg-card shadow-xs">
      <header className="flex min-h-11 flex-wrap items-center justify-between gap-2 border-b px-4 py-2">
        <div>
          <h2 className="text-[13px] font-semibold">{title}</h2>
          {note && <p className="text-[12px] text-muted-foreground">{note}</p>}
        </div>
        {action}
      </header>
      <div className="p-4 text-[13px]">{children}</div>
    </section>
  );
}

export const th = "py-1.5 pr-3 text-left text-[11px] font-medium text-muted-foreground uppercase";

// useQuestionResults loads how each tracked question's answers treated the
// brand in the period, with the period before and week by week.
export function useQuestionResults(d: AIData) {
  const [list, setList] = useState<QuestionResult[] | null>(null);
  const [n, setN] = useState(0);
  const cohort = d.cohort?.id;
  useEffect(() => {
    if (!cohort) return;
    let live = true;
    api.GET("/v1/brands/{brand}/ai-visibility/questions", { params: { path: { brand: d.brand.id }, query: { days: d.days, cohort } } }).then(({ data, error }) => {
      if (!live) return;
      if (error) toast(errorMessage(error), "error");
      // The JSON body; the same route writes CSV when asked for it.
      setList(typeof data === "object" && data ? data.data : []);
    });
    return () => { live = false; };
  }, [d.brand.id, d.days, cohort, n, d.running]);
  return { results: list, reload: () => setN((x) => x + 1) };
}

// Trend draws a question's weeks as small bars: how often the brand was
// named each week (green when it was recommended in most answers, red
// when never named), so a person sees traction either way.
export function Trend({ weeks }: { weeks: QuestionResult["weeks"] }) {
  if (weeks.length === 0) return <span className="text-[12px] text-muted-foreground">–</span>;
  const shown = weeks.slice(-12);
  return (
    <span className="flex h-5 items-end gap-px" title={shown.map((w) => `${w.week}: named in ${w.named} of ${w.answers}`).join("\n")}>
      {shown.map((w) => {
        const r = w.answers ? w.named / w.answers : 0;
        const tone = w.named === 0 ? "bg-red-400/70" : w.recommended * 2 > w.answers ? "bg-green-500" : "bg-primary/70";
        return <span key={w.week} className={`w-1.5 rounded-sm ${tone}`} style={{ height: `${Math.max(12, Math.round(r * 100))}%` }} />;
      })}
    </span>
  );
}

// marketName says a question set's locale (en-US) as people read it:
// "English (United States)"; a set without one is "Any market".
export const marketName = (locale?: string | null) => {
  if (!locale) return "Any market";
  try {
    const [lang, region] = locale.split("-");
    const l = new Intl.DisplayNames(undefined, { type: "language" }).of(lang) ?? lang;
    return region ? `${l} (${new Intl.DisplayNames(undefined, { type: "region" }).of(region) ?? region})` : l;
  } catch {
    return locale;
  }
};

// markets are the locales offered when adding a question set for a market.
export const markets = ["en-US", "en-GB", "en-AU", "en-CA", "en-IN", "de-DE", "fr-FR", "es-ES", "es-MX", "pt-BR", "it-IT", "nl-NL", "sv-SE", "da-DK", "nb-NO", "fi-FI", "pl-PL", "tr-TR", "ja-JP", "ko-KR", "zh-CN", "zh-TW", "ar-AE", "hi-IN", "id-ID"];

// setLabel names a question set in the switch: by market when the
// brand's sets differ by market, else by name.
export const setLabel = (c: Cohort, all: Cohort[]) => (all.some((x) => x.locale !== c.locale) ? `${marketName(c.locale)}${all.filter((x) => x.locale === c.locale).length > 1 ? ` · ${c.name}` : ""}` : c.name);

export const delta = (v: number | null | undefined) => (v === null || v === undefined ? "–" : `${v > 0 ? "+" : ""}${Math.round(v)}`);

// The brand's SEO site, which holds its one competitor list (the
// Competitors page): "" when it has none, null while loading.
export function useCompetitorSite(ws: string, brand: string) {
  const [site, setSite] = useState<string | null>(null);
  useEffect(() => {
    api.GET("/v1/brands/{brand}/seo-sites", { params: { path: { brand: pickedBrandId() } } }).then(({ data }) => setSite(data?.data[0]?.id ?? ""));
  }, [ws, brand]);
  return site;
}

// addCompetitor puts a competitor on the brand's competitor list, which
// every AI visibility run counts. It says whether it worked.
export async function addCompetitor(_ws: string, site: string, name: string, website: string) {
  const { error } = await api.POST("/v1/brands/{brand}/seo-competitors", { params: { path: { brand: pickedBrandId() } }, body: { site_id: site, host: website, ...(name && name !== website ? { name } : {}) } });
  if (error) {
    toast(errorMessage(error), "error");
    return false;
  }
  toast(`${name || website} is on your competitor list. It counts from the next run.`);
  return true;
}
