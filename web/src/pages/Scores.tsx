import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import type { RuleCondition, Score, ScoreFactor, ScoreInput, ScorePart, ScoreStarter, ScoreTrackRecord } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, Empty, ErrorText, field, Loading, PageHeader, confirmAction, toast } from "../ui";
import { ago } from "../format";
import { useBrand } from "@/lib/brand";
import { nameItem } from "@/lib/records";
import { track } from "@/lib/analytics";
import { natureHint, natureText, signed } from "@/components/record-scores";

// Scores (docs/scores.md): a number from 0 to 100 a brand keeps on every
// company or person, such as a health score or a lead score. A score
// starts everyone at a base and adds or takes points for each factor that
// holds, so the record can say why it scores what it does. Rules,
// journeys, smart lists and Query read it under scores.<key>. Adding and
// changing scores needs catalog.manage; every record is scored again
// quietly, so nothing fires for where records already stand.

type Draft = {
  key: string; label: string; description: string; record_type: "account" | "person" | "deal"; base: number; high_from: number; low_below: number; factors: ScoreFactor[];
  nature: Score["nature"]; parts: ScorePart[]; higher_is_better: boolean; needs: string;
};

const blank: Draft = { key: "", label: "", description: "", record_type: "account", base: 50, high_from: 70, low_below: 40, factors: [], nature: "measured", parts: [], higher_is_better: true, needs: "" };
const natures: Score["nature"][] = ["measured", "ai", "predicted"];
const one = (t: Draft["record_type"]) => (t === "account" ? "company" : t);
const typeBadge: Record<Draft["record_type"], string> = { account: "Companies", person: "People", deal: "Deals" };
const copy = (s: Score | ScoreStarter): Draft => ({ ...s, factors: s.factors.map((f) => ({ ...f, conditions: f.conditions.map((c) => ({ ...c })) })), parts: s.parts.map((p) => ({ ...p })) });
export const ops: [RuleCondition["op"], string][] = [["gt", "is more than"], ["lt", "is less than"], ["eq", "is"], ["neq", "is not"], ["contains", "contains"], ["exists", "is set"], ["not_exists", "is not set"]];
const about = (t: Draft["record_type"]) => (t === "account" ? "companies" : t === "person" ? "people" : "deals");

export function Scores({ ws, canManage, tabs }: { ws: string; canManage: boolean; tabs: ReactNode }) {
  const { brand, active } = useBrand(ws);
  useEffect(() => nameItem("scores", "Scores"), []);
  return (
    <div className="space-y-6">
      <PageHeader
        title="Scores"
        description="Numbers from 0 to 100 on every company, person or deal: health, lead score, churn risk, the chance a deal is won. You choose what adds points and what takes them, so each record can say why it scores what it does, and each score says whether it is measured, read by your AI or a prediction."
      />
      {tabs}
      {brand ? (
        <ScoresFor key={brand.id} brand={brand.id} canManage={canManage} />
      ) : (
        <Empty title="Pick a brand">
          Scores belong to one brand. Pick {active.length > 1 ? `one of your ${active.length} brands` : "your brand"} in the brand switcher.
        </Empty>
      )}
    </div>
  );
}

function ScoresFor({ brand, canManage }: { brand: string; canManage: boolean }) {
  const [list, setList] = useState<Score[] | null>(null);
  const [starters, setStarters] = useState<ScoreStarter[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [editing, setEditing] = useState<{ score?: Score; draft: Draft } | null>(null);
  const names = useFieldNames(brand);

  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/scores", { params: { path: { brand } } });
    if (!data) return setError(errorMessage(error));
    setError("");
    setList(data.data);
    setStarters(data.starters);
  }, [brand]);
  useEffect(() => {
    void load();
  }, [load]);
  // Scoring every record runs in the background: look again until it ends.
  useEffect(() => {
    if (!list?.some((s) => s.scoring)) return;
    const t = setTimeout(() => void load(), 3000);
    return () => clearTimeout(t);
  }, [list, load]);

  async function archive(s: Score) {
    if (!(await confirmAction(`Archive ${s.label}?`, <p>It is taken off every record. Rules, lists and journeys that read <span className="font-mono">scores.{s.key}</span> will no longer find it.</p>, "Archive", true))) return;
    setBusy(true);
    const { error } = await api.POST("/v1/brands/{brand}/scores/{score}/archive", { params: { path: { brand, score: s.id } } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast(`${s.label} archived.`);
    void load();
  }

  if (error) return <ErrorText>{error}</ErrorText>;
  if (!list) return <Loading />;
  const taken = new Set(list.map((s) => s.key));
  // A factor can also read the record's other scores.
  const withScores = (t: Draft["record_type"]) => [...names[t], ...list.filter((s) => s.record_type === t).flatMap((s) => [`scores.${s.key}`, `scores.${s.key}_band`])];
  const fields = { account: withScores("account"), person: withScores("person"), deal: withScores("deal") };
  const unused = starters.filter((s) => !taken.has(s.key));
  const start = (s?: ScoreStarter) => {
    if (s) track("ui.score_starter_picked", { starter: s.key });
    setEditing({ draft: s ? copy(s) : blank });
  };
  const saved = () => {
    setEditing(null);
    void load();
  };

  return (
    <div className="space-y-6">
      {editing && !editing.score && <ScoreEditor brand={brand} draft={editing.draft} fields={fields} others={list} onClose={() => setEditing(null)} onSaved={saved} />}

      {list.length === 0 && !editing && (
        <Empty title="No scores yet">
          {canManage ? "Start from one of these and change it to fit, or build your own. Nothing is sent and no rule fires when you add a score." : "Someone who manages the catalog can add a score."}
        </Empty>
      )}

      {list.map((s) =>
        editing?.score?.id === s.id ? (
          <ScoreEditor key={s.id} brand={brand} score={s} draft={editing.draft} fields={fields} others={list} onClose={() => setEditing(null)} onSaved={saved} />
        ) : (
          <Card
            key={s.id}
            title={
              <span className="flex flex-wrap items-center gap-2">
                {s.label}
                <span className="font-mono text-[12px] font-normal text-muted-foreground">scores.{s.key}</span>
                <Badge>{typeBadge[s.record_type]}</Badge>
                <span title={natureHint[s.nature]}><Badge tone={s.nature === "measured" ? undefined : s.nature === "ai" ? "blue" : "amber"}>{natureText[s.nature]}</Badge></span>
                {s.scoring && <Badge tone="blue">Scoring every record</Badge>}
              </span>
            }
            description={s.description || undefined}
            action={
              canManage && (
                <span className="flex gap-1">
                  <Button variant="ghost" disabled={busy} onClick={() => setEditing({ score: s, draft: copy(s) })}>Edit</Button>
                  <Button variant="ghost" disabled={busy} onClick={() => archive(s)}>Archive</Button>
                </span>
              )
            }
          >
            <Spread s={s} />
            {s.parts.length > 0 ? (
              <ul className="mt-3 space-y-1 text-[13px]">
                <li className="text-muted-foreground">The average of these scores. One with nothing to go on for a {one(s.record_type)} is left out.</li>
                {s.parts.map((p) => (
                  <li key={p.key} className="flex gap-3">
                    <span className="w-9 shrink-0 text-right font-medium tabular-nums">{p.weight}×</span>
                    <span>{list.find((x) => x.key === p.key)?.label ?? p.key}</span>
                  </li>
                ))}
              </ul>
            ) : (
              <ul className="mt-3 space-y-1 text-[13px]">
                <li className="flex gap-3 text-muted-foreground">
                  <span className="w-9 shrink-0 text-right tabular-nums">{s.base}</span>
                  <span>Where every {one(s.record_type)} starts</span>
                </li>
                {s.factors.map((f, i) => (
                  <li key={i} className="flex gap-3">
                    <span className={`w-9 shrink-0 text-right font-medium tabular-nums ${f.points > 0 === s.higher_is_better ? "text-green-700 dark:text-green-400" : "text-red-700 dark:text-red-400"}`}>{signed(f.points)}</span>
                    <span>{f.label}</span>
                  </li>
                ))}
              </ul>
            )}
            {s.nature === "predicted" && <TrackRecord brand={brand} score={s} />}
            <p className="mt-3 text-[12px] text-muted-foreground">
              High from {s.high_from}, low under {s.low_below}{s.higher_is_better ? "" : ", and high is the one to worry about"}.{s.needs && ` Only for ${about(s.record_type)} where ${s.needs} is set.`} Use it anywhere a field goes: <span className="font-mono">scores.{s.key}</span> for the number, <span className="font-mono">scores.{s.key}_band</span> for high, medium or low.
              {s.scored_at && !s.scoring && ` Every record was last scored ${ago(s.scored_at)}.`}
            </p>
          </Card>
        ),
      )}

      {canManage && !editing && (
        <Card title={list.length === 0 ? "Start with" : "Add another"} description="A starter is only a first draft: you see every factor before anything is saved.">
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
            {unused.map((s) => (
              <button key={s.key} type="button" className="rounded-md border p-3 text-left hover:bg-muted/50" onClick={() => start(s)}>
                <span className="block text-[13px] font-medium">{s.label}</span>
                <span className="mt-0.5 block text-[12px] text-muted-foreground">{s.description}</span>
                <span className="mt-2 block text-[12px] text-muted-foreground">For {about(s.record_type)} · {natureText[s.nature]} · {s.factors.length} factors</span>
              </button>
            ))}
            <button type="button" className="rounded-md border border-dashed p-3 text-left hover:bg-muted/50" onClick={() => start()}>
              <span className="block text-[13px] font-medium">Build your own</span>
              <span className="mt-0.5 block text-[12px] text-muted-foreground">Start from nothing and add the factors that matter to you.</span>
            </button>
          </div>
        </Card>
      )}
    </div>
  );
}

// TrackRecord checks a prediction against what really happened, so it is
// never only a number: how many deals in each band were won, or how many
// customers in each band left.
function TrackRecord({ brand, score }: { brand: string; score: Score }) {
  const [t, setT] = useState<ScoreTrackRecord | null>(null);
  useEffect(() => {
    let live = true;
    void api.GET("/v1/brands/{brand}/scores/{score}/track-record", { params: { path: { brand, score: score.id } } }).then(({ data }) => {
      if (live && data) setT(data);
    });
    return () => {
      live = false;
    };
  }, [brand, score.id, score.version]);
  if (!t || !t.outcome) return null;
  const won = t.outcome === "won";
  const what = won ? "deals closed in the last year" : `${about(score.record_type)} scored 90 days ago`;
  return (
    <div className="mt-3 rounded-md border bg-muted/30 p-3 text-[13px]">
      <p className="font-medium">How it has held up</p>
      {t.records === 0 ? (
        <p className="mt-1 text-[12px] text-muted-foreground">
          Nothing to check it against yet. This fills in by itself {won ? "as scored deals are won or lost" : "once customers have had this score for 90 days"}, so you can see whether a high score really means what it says.
        </p>
      ) : (
        <>
          <ul className="mt-1 space-y-0.5">
            {t.bands.filter((b) => b.records > 0).map((b) => (
              <li key={b.band} className="flex gap-3">
                <span className="w-16 shrink-0 text-muted-foreground">{b.band === "high" ? "High" : b.band === "medium" ? "Medium" : "Low"}</span>
                <span>
                  <span className="font-medium tabular-nums">{b.rate}%</span> {won ? "won" : "left"}
                  <span className="text-muted-foreground"> · {b.happened} of {b.records}</span>
                </span>
              </li>
            ))}
          </ul>
          <p className="mt-1 text-[12px] text-muted-foreground">
            From {t.records.toLocaleString()} {what}.{!t.enough && " Too few to read much into yet."}
          </p>
        </>
      )}
    </div>
  );
}

// Spread shows how the records divide over the three bands.
function Spread({ s }: { s: Score }) {
  const total = s.counts.high + s.counts.medium + s.counts.low;
  if (total === 0) return <p className="text-[13px] text-muted-foreground">{s.scoring ? "Scoring every record now. This takes a moment." : `No ${about(s.record_type)} scored yet.`}</p>;
  const parts: [string, number, string][] = [["High", s.counts.high, "bg-green-500"], ["Medium", s.counts.medium, "bg-amber-500"], ["Low", s.counts.low, "bg-red-500"]];
  return (
    <div>
      <div className="flex h-2 overflow-hidden rounded-full bg-muted" role="img" aria-label={parts.map(([n, c]) => `${n} ${c}`).join(", ")}>
        {parts.map(([n, c, color]) => c > 0 && <span key={n} className={color} style={{ width: `${(c / total) * 100}%` }} />)}
      </div>
      <div className="mt-1.5 flex flex-wrap gap-x-4 text-[12px] text-muted-foreground">
        {parts.map(([n, c, color]) => (
          <span key={n} className="inline-flex items-center gap-1.5">
            <span aria-hidden="true" className={`size-1.5 rounded-full ${color}`} />
            {n} <span className="tabular-nums text-foreground">{c.toLocaleString()}</span>
          </span>
        ))}
      </div>
    </div>
  );
}

// useFieldNames suggests the fields a factor can read: the brand's
// metrics (with their trends) and each record type's fields.
export function useFieldNames(brand: string): Record<Draft["record_type"], string[]> {
  const [names, setNames] = useState<Record<Draft["record_type"], string[]>>({ account: [], person: [], deal: [] });
  useEffect(() => {
    void Promise.all([
      api.GET("/v1/brands/{brand}/metrics", { params: { path: { brand } } }),
      api.GET("/v1/brands/{brand}/catalog", { params: { path: { brand } } }),
    ]).then(([m, c]) => {
      const metrics = (m.data?.data ?? []).filter((x) => x.status === "active");
      const keys = new Set(metrics.map((x) => x.key));
      const forLevel = (level: "company" | "person") =>
        metrics
          .filter((x) => x.levels.includes(level))
          .flatMap((x) => [`metrics.${x.key}`, ...(keys.has(x.key.replace(/_(\d+d)$/, "_prev_$1")) && !x.key.includes("_prev_") ? [`metrics.${x.key}_change`] : [])]);
      const fieldsOf = (type: string) => (c.data?.types.find((t) => t.key === type)?.fields ?? []).filter((f) => !f.archived).map((f) => f.key);
      // What your AI read from their conversations, when that is on.
      const sentiment = ["sentiment.label", "sentiment.trend", "sentiment.value"];
      const company = [...forLevel("company"), ...sentiment, ...fieldsOf("account")];
      setNames({
        account: company,
        person: [...forLevel("person"), ...fieldsOf("person")],
        // A deal reads its own facts and its company's fields.
        deal: ["stage", "deal.days_in_stage", "deal.days_open", "deal.days_quiet", "deal.days_to_close", "deal.contacts", ...fieldsOf("deal").filter((k) => k !== "stage"), ...company.map((k) => `account.${k}`), "account.scores.health", "account.scores.health_band"],
      });
    });
  }, [brand]);
  return names;
}

export const readValue = (v: string): unknown => (v.trim() !== "" && !Number.isNaN(Number(v)) ? Number(v) : v);

// ScoreEditor adds a score or changes one. A change to how it scores makes
// a new version, and every record is scored again in the background.
function ScoreEditor({ brand, score, draft, fields, others, onClose, onSaved }: { brand: string; score?: Score; draft: Draft; fields: Record<Draft["record_type"], string[]>; others: Score[]; onClose: () => void; onSaved: () => void }) {
  const [f, setF] = useState<Draft>(draft);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const setFactor = (i: number, x: ScoreFactor) => setF({ ...f, factors: f.factors.map((y, k) => (k === i ? x : y)) });
  const range = useMemo(() => {
    const clamp = (n: number) => Math.min(100, Math.max(0, n));
    return [clamp(f.base + f.factors.filter((x) => x.points < 0).reduce((a, x) => a + x.points, 0)), clamp(f.base + f.factors.filter((x) => x.points > 0).reduce((a, x) => a + x.points, 0))];
  }, [f.base, f.factors]);
  // A headline score is made of the type's other plain scores.
  const [headline, setHeadline] = useState(draft.parts.length > 0);
  const candidates = others.filter((o) => o.record_type === f.record_type && o.id !== score?.id && o.parts.length === 0 && !o.factors.some((x) => x.conditions.some((c) => c.attribute?.startsWith("scores."))));
  const partsOK = f.parts.length >= 2 && f.parts.every((p) => p.key && p.weight >= 1) && new Set(f.parts.map((p) => p.key)).size === f.parts.length;
  const factorsOK = headline ? partsOK : f.factors.length > 0 && f.factors.every((x) => x.label.trim() && x.points !== 0 && x.conditions.length > 0 && x.conditions.every((c) => c.attribute && (c.op === "exists" || c.op === "not_exists" || (c.value !== undefined && c.value !== ""))));
  const valid = f.label.trim() && (score || /^[a-z][a-z0-9_]{0,39}$/.test(f.key)) && factorsOK && f.low_below < f.high_from;
  const listID = `score-fields-${score?.id ?? "new"}`;

  async function save() {
    setBusy(true);
    setError("");
    const body: ScoreInput = {
      label: f.label.trim(), description: f.description.trim(), base: f.base, high_from: f.high_from, low_below: f.low_below,
      nature: f.nature, higher_is_better: f.higher_is_better, needs: headline ? "" : f.needs.trim(),
      factors: headline ? [] : f.factors, parts: headline ? f.parts : [],
    };
    const { error } = score
      ? await api.PATCH("/v1/brands/{brand}/scores/{score}", { params: { path: { brand, score: score.id } }, body })
      : await api.POST("/v1/brands/{brand}/scores", { params: { path: { brand } }, body: { ...body, key: f.key, record_type: f.record_type } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    toast(score ? `${body.label} saved. If how it scores changed, every record is scored again now.` : `${body.label} added. Every ${one(f.record_type)} is being scored now.`);
    onSaved();
  }

  const label = "text-[12px] text-muted-foreground";
  const num = (v: string, lo: number, hi: number) => Math.min(hi, Math.max(lo, Math.round(Number(v) || 0)));
  return (
    <Card title={score ? `Change ${score.label}` : "New score"} description="Everyone starts at the same number. Each factor adds or takes points when it is true for them. The score stays between 0 and 100.">
      <datalist id={listID}>{fields[f.record_type].map((k) => <option key={k} value={k} />)}</datalist>
      <div className="space-y-4">
        <div className="flex flex-wrap items-end gap-3">
          <label className={label}>Name<input className={`${field} mt-1 block w-56`} placeholder="Health score" value={f.label} onChange={(e) => setF({ ...f, label: e.target.value })} /></label>
          {!score && <label className={label}>Key (never changes)<input className={`${field} mt-1 block w-40 font-mono`} placeholder="health" value={f.key} onChange={(e) => setF({ ...f, key: e.target.value.trim() })} /></label>}
          {!score && (
            <label className={label}>Scores
              <select className={`${field} mt-1 block`} value={f.record_type} onChange={(e) => setF({ ...f, record_type: e.target.value as Draft["record_type"] })}>
                <option value="account">Companies</option>
                <option value="person">People</option>
                <option value="deal">Deals</option>
              </select>
            </label>
          )}
          {!headline && <label className={label}>Everyone starts at<input type="number" min={0} max={100} className={`${field} mt-1 block w-24`} value={f.base} onChange={(e) => setF({ ...f, base: num(e.target.value, 0, 100) })} /></label>}
        </div>
        <label className={`${label} block`}>What it tells you (optional)<input className={`${field} mt-1 block w-full max-w-2xl`} placeholder="How well a customer is doing." value={f.description} onChange={(e) => setF({ ...f, description: e.target.value })} /></label>

        <div className="flex flex-wrap items-end gap-x-6 gap-y-3">
          <label className={label}>What kind of number it is
            <select className={`${field} mt-1 block`} value={f.nature} onChange={(e) => setF({ ...f, nature: e.target.value as Draft["nature"] })}>
              {natures.map((n) => <option key={n} value={n}>{natureText[n]}</option>)}
            </select>
          </label>
          <p className="max-w-md pb-1.5 text-[12px] text-muted-foreground">{natureHint[f.nature]} It is shown beside the score everywhere.</p>
          <label className="flex items-center gap-2 pb-1.5 text-[13px]">
            <input type="checkbox" checked={!f.higher_is_better} onChange={(e) => setF({ ...f, higher_is_better: !e.target.checked })} />
            A high score is bad news, like churn risk
          </label>
        </div>

        {(candidates.length >= 2 || headline) && (
          <div className="flex flex-wrap gap-4 text-[13px]">
            <label className="flex items-center gap-2"><input type="radio" name={`${listID}-mode`} checked={!headline} onChange={() => setHeadline(false)} />Built from factors</label>
            <label className="flex items-center gap-2"><input type="radio" name={`${listID}-mode`} checked={headline} onChange={() => { setHeadline(true); if (f.parts.length === 0) setF({ ...f, parts: candidates.slice(0, 2).map((c) => ({ key: c.key, weight: 1 })) }); }} />A headline score made of other scores</label>
          </div>
        )}

        {headline && (
          <div className="space-y-2">
            <p className="text-[13px] font-medium">Made of</p>
            {f.parts.map((p, i) => (
              <div key={i} className="flex flex-wrap items-center gap-2 text-[13px]">
                <select aria-label="Score" className={`${field} w-56`} value={p.key} onChange={(e) => setF({ ...f, parts: f.parts.map((y, k) => (k === i ? { ...y, key: e.target.value } : y)) })}>
                  {candidates.map((c) => <option key={c.key} value={c.key}>{c.label}</option>)}
                </select>
                <span className="text-muted-foreground">counts</span>
                <input aria-label="Weight" type="number" min={1} max={100} className={`${field} w-20`} value={p.weight} onChange={(e) => setF({ ...f, parts: f.parts.map((y, k) => (k === i ? { ...y, weight: num(e.target.value, 1, 100) } : y)) })} />
                <span className="text-muted-foreground">×</span>
                {f.parts.length > 2 && <Button variant="ghost" onClick={() => setF({ ...f, parts: f.parts.filter((_, k) => k !== i) })}>Remove</Button>}
              </div>
            ))}
            {f.parts.length < Math.min(6, candidates.length) && <Button variant="ghost" onClick={() => setF({ ...f, parts: [...f.parts, { key: candidates.find((c) => !f.parts.some((p) => p.key === c.key))?.key ?? "", weight: 1 }] })}>Add a score</Button>}
            <p className="text-[12px] text-muted-foreground">The headline is the average of these, so a score that counts 2× moves it twice as much. Each stays visible under the headline on the record, so one going down is never hidden by another going up.</p>
          </div>
        )}

        {!headline && <div className="space-y-2">
          <p className="text-[13px] font-medium">Factors</p>
          {f.factors.length === 0 && <p className="text-[12px] text-muted-foreground">Add what should move the score, such as usage falling or a renewal coming up.</p>}
          {f.factors.map((x, i) => (
            <div key={i} className="space-y-2 rounded-md border p-3">
              <div className="flex flex-wrap items-end gap-2">
                <label className={`${label} min-w-0 flex-1`}>In words (shown on the record)<input className={`${field} mt-1 block w-full`} placeholder="Usage fell by a third" value={x.label} onChange={(e) => setFactor(i, { ...x, label: e.target.value })} /></label>
                <label className={label}>Then
                  <select aria-label="Add or take" className={`${field} mt-1 block`} value={x.points < 0 ? "take" : "add"} onChange={(e) => setFactor(i, { ...x, points: Math.abs(x.points || 10) * (e.target.value === "take" ? -1 : 1) })}>
                    <option value="add">add</option>
                    <option value="take">take</option>
                  </select>
                </label>
                <label className={label}>Points<input type="number" min={1} max={100} className={`${field} mt-1 block w-20`} value={Math.abs(x.points)} onChange={(e) => setFactor(i, { ...x, points: num(e.target.value, 0, 100) * (x.points < 0 ? -1 : 1) })} /></label>
                <Button variant="ghost" onClick={() => setF({ ...f, factors: f.factors.filter((_, k) => k !== i) })}>Remove</Button>
              </div>
              {x.conditions.map((c, k) => {
                const setC = (n: RuleCondition) => setFactor(i, { ...x, conditions: x.conditions.map((y, j) => (j === k ? n : y)) });
                const bare = c.op === "exists" || c.op === "not_exists";
                return (
                  <div key={k} className="flex flex-wrap items-center gap-2 text-[13px]">
                    <span className="w-10 text-muted-foreground">{k === 0 ? "When" : x.match === "any" ? "or" : "and"}</span>
                    <input aria-label="Field" className={`${field} w-64 font-mono`} placeholder="metrics.usage_7d_change" list={listID} value={c.attribute ?? ""} onChange={(e) => setC({ ...c, attribute: e.target.value.trim() })} />
                    <select aria-label="Compare" className={field} value={c.op} onChange={(e) => setC({ ...c, op: e.target.value as RuleCondition["op"] })}>
                      {ops.map(([v, t]) => <option key={v} value={v}>{t}</option>)}
                    </select>
                    {!bare && <input aria-label="Value" className={`${field} w-32`} placeholder="-30" value={c.value === undefined ? "" : String(c.value)} onChange={(e) => setC({ ...c, value: readValue(e.target.value) })} />}
                    {x.conditions.length > 1 && <Button variant="ghost" onClick={() => setFactor(i, { ...x, conditions: x.conditions.filter((_, j) => j !== k) })}>Remove</Button>}
                  </div>
                );
              })}
              <div className="flex flex-wrap items-center gap-3 pl-12 text-[12px] text-muted-foreground">
                {x.conditions.length < 5 && <button type="button" className="underline" onClick={() => setFactor(i, { ...x, conditions: [...x.conditions, { attribute: "", op: "gt", value: "" }] })}>Add another test</button>}
                {x.conditions.length > 1 && (
                  <label className="flex items-center gap-1.5">
                    True when
                    <select aria-label="Match" className={field} value={x.match ?? "all"} onChange={(e) => setFactor(i, { ...x, match: e.target.value as "all" | "any" })}>
                      <option value="all">all tests hold</option>
                      <option value="any">any test holds</option>
                    </select>
                  </label>
                )}
              </div>
            </div>
          ))}
          {f.factors.length < 30 && <Button variant="ghost" onClick={() => setF({ ...f, factors: [...f.factors, { label: "", points: 10, match: "all", conditions: [{ attribute: "", op: "gt", value: "" }] }] })}>Add a factor</Button>}
          <p className="text-[12px] text-muted-foreground">
            Pick a field or a metric. For a trend, use a metric's change in percent, such as <span className="font-mono">metrics.usage_7d_change</span> is less than -30. For a date, compare with <span className="font-mono">today</span>, <span className="font-mono">+60d</span> (60 days from now) or <span className="font-mono">-14d</span> (14 days ago).
          </p>
          <label className={`${label} block`}>Only score {about(f.record_type)} where this field is set (optional)
            <input className={`${field} mt-1 block w-64 font-mono`} placeholder="sentiment.value" list={listID} value={f.needs} onChange={(e) => setF({ ...f, needs: e.target.value.trim() })} />
            <span className="mt-1 block">Without it, a {one(f.record_type)} gets no score at all, which is not a low one.</span>
          </label>
        </div>}

        <div className="flex flex-wrap items-end gap-3">
          <label className={label}>High from<input type="number" min={1} max={100} className={`${field} mt-1 block w-24`} value={f.high_from} onChange={(e) => setF({ ...f, high_from: num(e.target.value, 1, 100) })} /></label>
          <label className={label}>Low under<input type="number" min={0} max={99} className={`${field} mt-1 block w-24`} value={f.low_below} onChange={(e) => setF({ ...f, low_below: num(e.target.value, 0, 99) })} /></label>
          <p className="pb-1.5 text-[12px] text-muted-foreground">
            {!headline && `With these factors a score runs from ${range[0]} to ${range[1]}.`}
            {f.low_below >= f.high_from && <span className="text-red-600"> Low must be under high.</span>}
            {!headline && f.factors.length > 0 && range[1] < f.high_from && <span className="text-amber-600"> Nobody can reach high.</span>}
          </p>
        </div>

        {error && <ErrorText>{error}</ErrorText>}
        <div className="flex items-center gap-2">
          <Button disabled={busy || !valid} onClick={save}>{score ? "Save" : "Add the score"}</Button>
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <span className="text-[12px] text-muted-foreground">Scoring what is already there is quiet: no rule, journey, email or webhook fires for it.</span>
        </div>
      </div>
    </Card>
  );
}
