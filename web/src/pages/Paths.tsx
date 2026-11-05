import { useCallback, useEffect, useRef, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, confirmAction, Empty, ErrorText, field, Loading, PageHeader, toast } from "../ui";
import { PickBrand } from "@/components/pick-brand";
import { useBrand } from "@/lib/brand";
import { nameItem } from "@/lib/records";
import { ago } from "../format";
import { day, presetLabels, today, whole } from "./Funnels";
import { takeDraft } from "@/components/insight-panels";

// Paths (docs/insights.md): where people went step by step, as a flow
// chart: the pages they viewed and the events they caused, from a start,
// towards an end, or from each person's first page or event in the dates.

export type PathsSpec = Schemas["PathsSpec"];
export type PathsResult = Schemas["PathsResult"];
type PathPoint = Schemas["PathPoint"];
type Catalog = Schemas["InsightsCatalog"];
type Insight = Schemas["Insight"];
type Preset = NonNullable<Schemas["InsightDateRange"]["preset"]>;

const starter: PathsSpec = { date_range: { preset: "last_30_days" }, steps: 4, kinds: ["page"] };

export function pathsReady(s: PathsSpec): boolean {
  const ok = (p?: PathPoint | null) => !p || (p.kind === "page" ? p.name.startsWith("/") : !!p.name);
  return ok(s.start) && ok(s.end) && (s.exclude ?? []).every((x) => ok(x));
}

export function cleanPaths(s: PathsSpec): PathsSpec {
  const out: PathsSpec = { ...s };
  if (!out.start?.name) delete out.start;
  if (!out.end?.name) delete out.end;
  if (!out.exclude?.length) delete out.exclude;
  if (!out.kinds?.length || out.kinds.length === 2) delete out.kinds;
  return out;
}

export function Paths({ ws, canManage }: { ws: string; canManage: boolean }) {
  const { brand, active } = useBrand(ws);
  useEffect(() => nameItem("paths", "Paths"), []);
  return (
    <div className="space-y-6">
      <PageHeader title="Paths" description="Where people go, step by step: the pages they view and the things they do after landing, before signing up, or on the way to paying." />
      {brand ? <PathsFor key={brand.id} brand={brand.id} canManage={canManage} /> : <PickBrand what="Paths" brands={active.length} />}
    </div>
  );
}

function PathsFor({ brand, canManage }: { brand: string; canManage: boolean }) {
  const [catalog, setCatalog] = useState<Catalog | null>(null);
  const [saved, setSaved] = useState<Insight[]>([]);
  const [current, setCurrent] = useState<Insight | null>(null);
  const [spec, setSpec] = useState<PathsSpec>(() => takeDraft<PathsSpec>("paths") ?? starter);
  const [result, setResult] = useState<PathsResult | null>(null);
  const [running, setRunning] = useState(false);
  const [error, setError] = useState("");
  const [name, setName] = useState("");
  const run = useRef(0);

  const loadSaved = useCallback(async () => {
    const { data } = await api.GET("/v1/brands/{brand}/insights", { params: { path: { brand }, query: { kind: "paths" } } });
    if (data) setSaved(data.data);
  }, [brand]);

  useEffect(() => {
    api.GET("/v1/brands/{brand}/insights/catalog", { params: { path: { brand } } }).then(({ data, error }) => (data ? setCatalog(data) : setError(errorMessage(error))));
    loadSaved();
  }, [brand, loadSaved]);

  useEffect(() => {
    if (!pathsReady(spec)) return;
    const n = ++run.current;
    const t = setTimeout(async () => {
      setRunning(true);
      const { data, error } = await api.POST("/v1/brands/{brand}/insights/paths", { params: { path: { brand } }, body: cleanPaths(spec) });
      if (n !== run.current) return;
      setRunning(false);
      if (!data) return setError(errorMessage(error));
      setError("");
      setResult(data);
    }, 400);
    return () => clearTimeout(t);
  }, [brand, spec]);

  function open(i: Insight) {
    setCurrent(i);
    setName(i.name);
    setSpec(i.spec as PathsSpec);
  }

  async function save() {
    const body = cleanPaths(spec);
    if (current) {
      const { data, error } = await api.PATCH("/v1/brands/{brand}/insights/{insight}", { params: { path: { brand, insight: current.id } }, body: { name, spec: body } });
      if (!data) return toast(errorMessage(error), "error");
      setCurrent(data);
    } else {
      const { data, error } = await api.POST("/v1/brands/{brand}/insights", { params: { path: { brand } }, body: { kind: "paths", name, spec: body } });
      if (!data || !("id" in data)) return toast(errorMessage(error), "error");
      setCurrent(data);
    }
    toast("Saved");
    loadSaved();
  }

  async function remove() {
    if (!current || !(await confirmAction(`Delete ${current.name}?`, "The paths question goes for everyone, and off any dashboard it is on. The data stays.", "Delete"))) return;
    const { error } = await api.DELETE("/v1/brands/{brand}/insights/{insight}", { params: { path: { brand, insight: current.id } } });
    if (error) return toast(errorMessage(error), "error");
    toast("Deleted");
    setCurrent(null);
    setName("");
    loadSaved();
  }

  if (!catalog) return error ? <ErrorText>{error}</ErrorText> : <Loading />;
  const anchor: "start" | "end" | "none" = spec.start ? "start" : spec.end ? "end" : "none";
  const point = spec.start ?? spec.end;
  const setPoint = (p: PathPoint | undefined) => setSpec((s) => (anchor === "end" ? { ...s, end: p, start: undefined } : { ...s, start: p, end: undefined }));
  const kinds = spec.kinds?.length ? spec.kinds : ["page", "event"];

  return (
    <div className="grid gap-6 xl:grid-cols-[380px_minmax(0,1fr)]">
      <div className="min-w-0 space-y-4">
        <Card title="Saved" action={current ? <Button variant="ghost" onClick={() => { setCurrent(null); setName(""); setSpec(starter); }}>New question</Button> : undefined}>
          {saved.length === 0 ? (
            <p className="text-[13px] text-muted-foreground">None yet. Build one below and save it to put it on a dashboard.</p>
          ) : (
            <ul className="-mx-1 space-y-0.5">
              {saved.map((i) => (
                <li key={i.id}>
                  <button onClick={() => open(i)} className={`flex w-full items-center justify-between gap-2 rounded px-2 py-1.5 text-left text-[13px] hover:bg-accent ${current?.id === i.id ? "bg-accent font-medium" : ""}`}>
                    <span className="truncate">{i.name}</span>
                    <span className="shrink-0 font-mono text-[11px] text-muted-foreground">{ago(i.updated_at)}</span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </Card>

        <Card title="Where paths begin or end" description="From a page or event, towards one, or from whatever each person did first.">
          <div className="space-y-2 text-[13px]">
            <select id="paths-anchor" aria-label="Paths run" className={`${field} w-full`} value={anchor} onChange={(e) => {
              const v = e.target.value;
              setSpec((s) => ({ ...s, start: v === "start" ? (s.start ?? s.end ?? { kind: "page", name: "/" }) : undefined, end: v === "end" ? (s.end ?? s.start ?? { kind: "event", name: "" }) : undefined }));
            }}>
              <option value="none">From each person's first page or event</option>
              <option value="start">From a page or event</option>
              <option value="end">Towards a page or event</option>
            </select>
            {point && <PointEditor point={point} catalog={catalog} onChange={setPoint} label={anchor === "end" ? "Ends at" : "Starts at"} />}
          </div>
        </Card>

        <Card title="Options">
          <div className="space-y-3 text-[13px]">
            <label className="block space-y-1">
              <span className="font-medium">{anchor === "end" ? "Dates the paths end in" : "Dates the paths start in"}</span>
              <select id="paths-dates" className={`${field} w-full`} value={spec.date_range?.preset ?? (spec.date_range?.from ? "custom" : "last_30_days")} onChange={(e) => setSpec((s) => ({ ...s, date_range: e.target.value === "custom" ? { from: today(-29), to: today(0) } : { preset: e.target.value as Preset } }))}>
                {Object.entries(presetLabels).map(([k, l]) => (
                  <option key={k} value={k}>{l}</option>
                ))}
                <option value="custom">Custom dates</option>
              </select>
            </label>
            {spec.date_range?.from !== undefined && !spec.date_range.preset && (
              <div className="flex gap-2">
                <input type="date" className={`${field} flex-1`} aria-label="From" value={spec.date_range.from ?? ""} onChange={(e) => setSpec((s) => ({ ...s, date_range: { ...s.date_range, from: e.target.value } }))} />
                <input type="date" className={`${field} flex-1`} aria-label="To" value={spec.date_range.to ?? ""} onChange={(e) => setSpec((s) => ({ ...s, date_range: { ...s.date_range, to: e.target.value } }))} />
              </div>
            )}
            <div>
              <span className="font-medium">Steps are</span>
              <div className="mt-1 flex gap-4">
                {(["page", "event"] as const).map((k) => (
                  <label key={k} className="flex items-center gap-2">
                    <input type="checkbox" checked={kinds.includes(k)} onChange={(e) => {
                      const next = e.target.checked ? [...new Set([...kinds, k])] : kinds.filter((x) => x !== k);
                      if (next.length) setSpec((s) => ({ ...s, kinds: next as PathsSpec["kinds"] }));
                    }} />
                    {k === "page" ? "Page views" : "Events"}
                  </label>
                ))}
              </div>
            </div>
            <div className="grid grid-cols-2 gap-2">
              <label className="block space-y-1">
                <span className="font-medium">Steps shown</span>
                <input id="paths-steps" type="number" min={2} max={6} className={`${field} w-full`} value={spec.steps ?? 4} onChange={(e) => setSpec((s) => ({ ...s, steps: Math.max(2, Math.min(6, Number(e.target.value) || 2)) }))} />
              </label>
              <label className="block space-y-1">
                <span className="font-medium">Per step, before "other"</span>
                <input type="number" min={1} max={20} className={`${field} w-full`} value={spec.top ?? 8} onChange={(e) => setSpec((s) => ({ ...s, top: Math.max(1, Math.min(20, Number(e.target.value) || 1)) }))} />
              </label>
            </div>
            <div>
              <span className="font-medium">A whole path takes at most</span>
              <div className="mt-1 flex gap-2">
                <input aria-label="Window" type="number" min={1} className={`${field} w-20`} value={spec.window?.value ?? 1} onChange={(e) => setSpec((s) => ({ ...s, window: { value: Math.max(1, Number(e.target.value) || 1), unit: s.window?.unit ?? "day" } }))} />
                <select aria-label="Window unit" className={field} value={spec.window?.unit ?? "day"} onChange={(e) => setSpec((s) => ({ ...s, window: { value: s.window?.value ?? 1, unit: e.target.value as NonNullable<PathsSpec["window"]>["unit"] } }))}>
                  <option value="minute">minutes</option>
                  <option value="hour">hours</option>
                  <option value="day">days</option>
                  <option value="week">weeks</option>
                </select>
              </div>
            </div>
            <div>
              <span className="font-medium">Leave out</span>
              {(spec.exclude ?? []).map((x, i) => (
                <div key={i} className="mt-1 flex items-start gap-1.5">
                  <div className="min-w-0 flex-1">
                    <PointEditor point={x} catalog={catalog} onChange={(p) => setSpec((s) => ({ ...s, exclude: (s.exclude ?? []).map((y, j) => (j === i ? (p ?? y) : y)) }))} />
                  </div>
                  <button aria-label="Remove" className="px-1 pt-1.5 text-muted-foreground hover:text-foreground" onClick={() => setSpec((s) => ({ ...s, exclude: (s.exclude ?? []).filter((_, j) => j !== i) }))}>×</button>
                </div>
              ))}
              {(spec.exclude ?? []).length < 20 && (
                <button className="mt-1 text-[12px] text-muted-foreground hover:text-foreground" onClick={() => setSpec((s) => ({ ...s, exclude: [...(s.exclude ?? []), { kind: "page", name: "/" }] }))}>+ A page or event to ignore</button>
              )}
            </div>
          </div>
        </Card>

        {canManage && (
          <Card title={current ? "Save changes" : "Save this question"}>
            <div className="flex gap-2">
              <input id="paths-name" aria-label="Name" className={`${field} min-w-0 flex-1`} placeholder="After landing on pricing" value={name} onChange={(e) => setName(e.target.value)} maxLength={120} />
              <Button disabled={!name.trim() || !pathsReady(spec)} onClick={save}>Save</Button>
            </div>
            {current && <button className="mt-2 text-[12px] text-muted-foreground hover:text-destructive" onClick={remove}>Delete this question</button>}
          </Card>
        )}
      </div>

      <div className="min-w-0 space-y-4">
        {error && <ErrorText>{error}</ErrorText>}
        {!pathsReady(spec) && <Empty title="Finish the question">A page is a path such as /pricing; an event needs its name.</Empty>}
        {pathsReady(spec) && !result && <Loading />}
        {pathsReady(spec) && result && (
          <Card
            title={<span className="flex items-center gap-2">{whole(result.people)} {result.people === 1 ? "person" : "people"}{running && <Badge>updating</Badge>}</span>}
            description={`${day(result.from)} to ${day(new Date(new Date(result.to).getTime() - 1).toISOString())}. Each column is a step; the bars are how many people were there, and the faded part how many went nowhere within the time limit.`}
          >
            <PathsView result={result} />
          </Card>
        )}
      </div>
    </div>
  );
}

function PointEditor({ point, catalog, onChange, label }: { point: PathPoint; catalog: Catalog; onChange: (p: PathPoint | undefined) => void; label?: string }) {
  return (
    <div className="grid grid-cols-[auto_minmax(0,1fr)] gap-1.5">
      <select aria-label={label ?? "Kind"} className={field} value={point.kind} onChange={(e) => onChange({ kind: e.target.value as PathPoint["kind"], name: e.target.value === "page" ? "/" : "" })}>
        <option value="page">Page</option>
        <option value="event">Event</option>
      </select>
      {point.kind === "page" ? (
        <input aria-label="Page path" className={`${field} min-w-0 font-mono`} placeholder="/pricing" value={point.name} onChange={(e) => onChange({ ...point, name: e.target.value.trim() })} />
      ) : (
        <>
          <input aria-label="Event" list="paths-events" className={`${field} min-w-0 font-mono`} placeholder="signed_up" value={point.name} onChange={(e) => onChange({ ...point, name: e.target.value.trim() })} />
          <datalist id="paths-events">
            {catalog.events.map((e) => (
              <option key={e.name} value={e.name}>{`${whole(e.count)} in 90 days`}</option>
            ))}
          </datalist>
        </>
      )}
    </div>
  );
}

// PathsView draws the graph as columns of nodes, one per step, with each
// node's share of the people and, on hover, where they went next.
export function PathsView({ result, compact }: { result: PathsResult; compact?: boolean }) {
  const [hover, setHover] = useState<string | null>(null);
  if (result.people === 0) return <Empty title="Nobody in these dates">No page views or events matched. Widen the dates, or pick another start.</Empty>;
  const steps = Array.from({ length: result.steps }, (_, i) => i + 1);
  const key = (step: number, kind: string, name: string) => `${step}:${kind}:${name}`;
  const nodes = (step: number) => result.nodes.filter((n) => n.step === step);
  const max = Math.max(1, ...result.nodes.map((n) => n.count));
  const out = hover ? result.links.filter((l) => key(l.step, l.from_kind, l.from) === hover) : [];
  const into = hover ? result.links.filter((l) => key(l.step + 1, l.to_kind, l.to) === hover) : [];
  const lit = new Set([...out.map((l) => key(l.step + 1, l.to_kind, l.to)), ...into.map((l) => key(l.step, l.from_kind, l.from))]);
  const name = (kind: string, n: string) => (kind === "other" ? "Other pages and events" : n);
  return (
    <div className="overflow-x-auto" onMouseLeave={() => setHover(null)}>
      <div className="grid gap-3" style={{ gridTemplateColumns: `repeat(${steps.length}, minmax(${compact ? 120 : 160}px, 1fr))` }}>
        {steps.map((step) => (
          <div key={step} className="min-w-0 space-y-1.5">
            <div className="font-mono text-[11px] uppercase text-muted-foreground">Step {step}</div>
            {nodes(step).length === 0 && <div className="text-[12px] text-muted-foreground">Nobody got this far.</div>}
            {nodes(step).map((n) => {
              const k = key(n.step, n.kind, n.name);
              const dim = hover !== null && hover !== k && !lit.has(k);
              const link = hover ? [...out, ...into].find((l) => key(l.step + 1, l.to_kind, l.to) === k || key(l.step, l.from_kind, l.from) === k) : undefined;
              return (
                <div key={k} className={`rounded border px-2 py-1.5 text-[12px] transition-opacity ${dim ? "opacity-30" : ""} ${hover === k ? "border-primary" : ""}`} onMouseEnter={() => setHover(k)} title={`${name(n.kind, n.name)}: ${whole(n.count)} people${n.drop_off ? `, ${whole(n.drop_off)} went nowhere` : ""}`}>
                  <div className="flex items-center justify-between gap-2">
                    <span className={`truncate ${n.kind === "event" ? "font-mono" : ""} ${n.kind === "other" ? "text-muted-foreground" : ""}`}>{n.kind === "event" ? "⚡ " : ""}{name(n.kind, n.name)}</span>
                    <span className="shrink-0 tabular-nums">{link && hover !== k ? <span className="text-muted-foreground">{whole(link.count)} of </span> : null}{whole(n.count)}</span>
                  </div>
                  <div className="mt-1 h-1.5 w-full rounded bg-muted">
                    <div className="relative h-1.5 rounded bg-primary" style={{ width: `${(n.count / max) * 100}%` }}>
                      {n.drop_off > 0 && <div className="absolute right-0 top-0 h-1.5 rounded-r bg-primary/30" style={{ width: `${(n.drop_off / n.count) * 100}%` }} />}
                    </div>
                  </div>
                </div>
              );
            })}
          </div>
        ))}
      </div>
      {!compact && (
        <p className="mt-3 text-[12px] text-muted-foreground">Hover a step to see how many came from, and went to, each neighbour. Faded bars are people who went nowhere within the time limit.</p>
      )}
    </div>
  );
}
