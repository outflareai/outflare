import { useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { api, errorMessage } from "../api";
import { Button, ErrorText, field, toast } from "../ui";

// Editing a brand's lifecycle stages and the moves between them
// (docs/product-lifecycle.md), the owner's decision of 2026-11-01: the
// default stages are a starting point each brand can rename, add to and
// remove from, and change what moves people between them. Both save with
// PUT, all levels at once; the server refuses removing a stage someone is
// in or a move uses (409).

type Lifecycle = Schemas["ProductLifecycle"];
type Stage = Schemas["ProductStage"];
type Move = Schemas["ProductMove"];
type Level = Stage["level"];

const levels: { key: Level; label: string }[] = [
  { key: "company", label: "Company (billing)" },
  { key: "person", label: "Person (usage)" },
];
const categories: Stage["category"][] = ["lead", "trial", "active", "at_risk", "churned"];
const keyOf = (name: string) => name.toLowerCase().replace(/[^a-z0-9]+/g, "_").replace(/^_+|_+$/g, "").replace(/^([0-9])/, "s_$1").slice(0, 40);

export function StagesSheet({ brand, life, onClose, onSaved }: { brand: string; life: Lifecycle; onClose: () => void; onSaved: (l: Lifecycle) => void }) {
  const [stages, setStages] = useState<Stage[]>(life.stages);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const fresh = new Set(life.stages.map((s) => `${s.level}:${s.key}`));
  const set = (i: number, p: Partial<Stage>) => setStages((ss) => ss.map((s, k) => (k === i ? { ...s, ...p } : s)));

  async function save() {
    setBusy(true);
    setError("");
    const { data, error } = await api.PUT("/v1/brands/{brand}/lifecycle/stages", { params: { path: { brand } }, body: { stages } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    if (data && "stages" in data) {
      toast("Stages saved.");
      onSaved(data);
    }
  }

  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-2xl">
        <SheetHeader>
          <SheetTitle>Lifecycle stages</SheetTitle>
          <SheetDescription>
            Rename, add or remove stages for each level. The category says what a stage means (a trial, a paying customer, churned); a company's overall status is its highest-ranked stage across products. A stage someone is in, or a move uses, can't be removed.
          </SheetDescription>
        </SheetHeader>
        <div className="space-y-6 px-4 pb-6 text-[13px]">
          {levels.map((lv) => (
            <section key={lv.key} className="space-y-2">
              <h3 className="font-medium">{lv.label}</h3>
              <div className="hidden grid-cols-[1fr_8rem_8rem_5rem_auto] gap-2 text-[12px] text-muted-foreground sm:grid">
                <span>Name</span><span>Key</span><span>Category</span><span>Rank</span><span />
              </div>
              {stages.map((s, i) =>
                s.level !== lv.key ? null : (
                  <div key={i} className="grid grid-cols-2 items-center gap-2 sm:grid-cols-[1fr_8rem_8rem_5rem_auto]">
                    <input aria-label="Stage name" className={field} value={s.name} onChange={(e) => set(i, fresh.has(`${s.level}:${s.key}`) ? { name: e.target.value } : { name: e.target.value, key: keyOf(e.target.value) })} />
                    <span className="truncate font-mono text-[12px] text-muted-foreground" title="The key never changes once saved: events and rules use it.">{s.key || "…"}</span>
                    <select aria-label="Category" className={field} value={s.category} onChange={(e) => set(i, { category: e.target.value as Stage["category"] })}>
                      {categories.map((c) => <option key={c} value={c}>{c.replace("_", " ")}</option>)}
                    </select>
                    <input aria-label="Rank" type="number" min={0} max={1000} className={field} value={s.rank} onChange={(e) => set(i, { rank: Number(e.target.value) })} />
                    <Button variant="danger" onClick={() => setStages((ss) => ss.filter((_, k) => k !== i))}>Remove</Button>
                  </div>
                ),
              )}
              <Button variant="ghost" onClick={() => setStages((ss) => [...ss, { level: lv.key, key: "", name: "", category: "active", rank: 50 }])}>Add a stage</Button>
            </section>
          ))}
          <ErrorText>{error}</ErrorText>
          <div className="flex flex-wrap gap-2 border-t pt-4">
            <Button disabled={busy || stages.some((s) => !s.name.trim() || !s.key)} onClick={save}>{busy ? "Saving…" : "Save stages"}</Button>
            <Button variant="ghost" onClick={onClose}>Cancel</Button>
          </div>
        </div>
      </SheetContent>
    </Sheet>
  );
}

export function MovesSheet({ brand, life, onClose, onSaved }: { brand: string; life: Lifecycle; onClose: () => void; onSaved: (l: Lifecycle) => void }) {
  const [moves, setMoves] = useState<Move[]>(life.moves);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const fresh = new Set(life.moves.map((m) => `${m.level}:${m.key}`));
  const set = (i: number, p: Partial<Move>) => setMoves((ms) => ms.map((m, k) => (k === i ? { ...m, ...p } : m)));
  const stagesOf = (level: Level) => life.stages.filter((s) => s.level === level);
  // Moves are tried in order within a level: swap with the neighbour of
  // the same level.
  function shift(i: number, by: -1 | 1) {
    setMoves((ms) => {
      const level = ms[i].level;
      let j = i + by;
      while (j >= 0 && j < ms.length && ms[j].level !== level) j += by;
      if (j < 0 || j >= ms.length) return ms;
      const out = [...ms];
      [out[i], out[j]] = [out[j], out[i]];
      return out;
    });
  }

  async function save() {
    setBusy(true);
    setError("");
    const body = { moves: moves.map(({ position: _, ...m }) => m) };
    const { data, error } = await api.PUT("/v1/brands/{brand}/lifecycle/moves", { params: { path: { brand } }, body });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    if (data && "moves" in data) {
      toast("Moves saved.");
      onSaved(data);
    }
  }

  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-3xl">
        <SheetHeader>
          <SheetTitle>What moves people between stages</SheetTitle>
          <SheetDescription>
            Each move takes a company or person from some stages to another when an event happens (such as subscription.canceled), when the product is used, or after a time without usage. They are tried in order: the first that fits wins. Each move records its event on the timeline, which rules and journeys can start on.
          </SheetDescription>
        </SheetHeader>
        <div className="space-y-6 px-4 pb-6 text-[13px]">
          {levels.map((lv) => (
            <section key={lv.key} className="space-y-3">
              <h3 className="font-medium">{lv.label}</h3>
              {moves.map((m, i) =>
                m.level !== lv.key ? null : (
                  <div key={i} className="space-y-2 rounded-md border p-3">
                    <div className="flex flex-wrap items-center gap-2">
                      <input aria-label="Move name" className={`${field} flex-1`} value={m.name} onChange={(e) => set(i, fresh.has(`${m.level}:${m.key}`) ? { name: e.target.value } : { name: e.target.value, key: keyOf(e.target.value) })} />
                      <span className="font-mono text-[12px] text-muted-foreground">{m.key || "…"}</span>
                      <Button variant="ghost" aria-label="Move up" onClick={() => shift(i, -1)}>↑</Button>
                      <Button variant="ghost" aria-label="Move down" onClick={() => shift(i, 1)}>↓</Button>
                      <Button variant="danger" onClick={() => setMoves((ms) => ms.filter((_, k) => k !== i))}>Remove</Button>
                    </div>
                    <div className="flex flex-wrap items-center gap-2">
                      <span>When</span>
                      <select aria-label="Kind" className={field} value={m.kind} onChange={(e) => set(i, { kind: e.target.value as Move["kind"] })}>
                        <option value="event">an event happens</option>
                        <option value="usage">the product is used</option>
                        <option value="inactivity">no usage for</option>
                      </select>
                      {m.kind === "event" && <input aria-label="Event" className={`${field} w-56 font-mono`} placeholder="subscription.canceled" value={m.event ?? ""} onChange={(e) => set(i, { event: e.target.value })} />}
                      {m.kind === "inactivity" && (
                        <select aria-label="After" className={field} value={m.after ?? "at_risk_days"} onChange={(e) => set(i, { after: e.target.value as Move["after"] })}>
                          <option value="at_risk_days">the product's at-risk days</option>
                          <option value="churn_days">the product's churn days</option>
                        </select>
                      )}
                      {(m.conditions?.length ?? 0) > 0 && <span className="text-[12px] text-muted-foreground">and {m.conditions!.length} property test{m.conditions!.length === 1 ? "" : "s"} (kept as they are)</span>}
                    </div>
                    <div className="flex flex-wrap items-center gap-2">
                      <span>from</span>
                      <label className="flex items-center gap-1"><input type="checkbox" checked={!!m.any_stage} onChange={(e) => set(i, { any_stage: e.target.checked })} /> any stage</label>
                      {!m.any_stage &&
                        [{ key: "none", name: "no stage yet" }, ...stagesOf(lv.key)].map((s) => (
                          <label key={s.key} className="flex items-center gap-1">
                            <input
                              type="checkbox"
                              checked={(m.from_stages ?? []).includes(s.key)}
                              onChange={(e) => set(i, { from_stages: e.target.checked ? [...(m.from_stages ?? []), s.key] : (m.from_stages ?? []).filter((x) => x !== s.key) })}
                            />
                            {s.name}
                          </label>
                        ))}
                    </div>
                    <div className="flex flex-wrap items-center gap-2">
                      <span>to</span>
                      <select aria-label="To stage" className={field} value={m.to_stage} onChange={(e) => set(i, { to_stage: e.target.value })}>
                        <option value="">Pick a stage</option>
                        {stagesOf(lv.key).map((s) => <option key={s.key} value={s.key}>{s.name}</option>)}
                      </select>
                      <span>and record</span>
                      <input aria-label="Event it records" className={`${field} w-56 font-mono`} placeholder="product.churned" value={m.emit_event} onChange={(e) => set(i, { emit_event: e.target.value })} />
                      <select aria-label="Products" className={field} value={m.churn_mode ?? ""} onChange={(e) => set(i, { churn_mode: (e.target.value || undefined) as Move["churn_mode"] })}>
                        <option value="">for every product</option>
                        <option value="cancellation">only products churned by cancelling</option>
                        <option value="inactivity">only products churned by no usage</option>
                      </select>
                    </div>
                  </div>
                ),
              )}
              <Button variant="ghost" onClick={() => setMoves((ms) => [...ms, { level: lv.key, key: "", name: "", kind: "event", event: "", to_stage: "", emit_event: "", from_stages: [] }])}>Add a move</Button>
            </section>
          ))}
          <p className="text-[12px] text-muted-foreground">Each move needs where it starts: some stages, "no stage yet", or any stage.</p>
          <ErrorText>{error}</ErrorText>
          <div className="flex flex-wrap gap-2 border-t pt-4">
            <Button disabled={busy || moves.some((m) => !m.name.trim() || !m.key || !m.to_stage || !m.emit_event.trim() || (m.kind === "event" && !m.event?.trim()) || (!m.any_stage && !(m.from_stages ?? []).length))} onClick={save}>
              {busy ? "Saving…" : "Save moves"}
            </Button>
            <Button variant="ghost" onClick={onClose}>Cancel</Button>
          </div>
        </div>
      </SheetContent>
    </Sheet>
  );
}
