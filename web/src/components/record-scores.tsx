import { useEffect, useState } from "react";
import type { RecordScore } from "@outflare/sdk";
import { api } from "@/api";
import { Status } from "@/ui";

// A record's scores (docs/scores.md), such as its health score, each with
// the factors behind it, so nobody has to guess why an account scores what
// it does. It renders nothing for a brand with no scores.

export const bandText: Record<RecordScore["band"], string> = { high: "High", medium: "Medium", low: "Low" };
export const bandTone: Record<RecordScore["band"], "green" | "amber" | "red"> = { high: "green", medium: "amber", low: "red" };
const bandRing: Record<RecordScore["band"], string> = {
  high: "border-green-500/50 text-green-700 dark:text-green-400",
  medium: "border-amber-500/50 text-amber-700 dark:text-amber-400",
  low: "border-red-500/50 text-red-700 dark:text-red-400",
};

export const signed = (n: number) => (n > 0 ? `+${n}` : String(n));

type Band = RecordScore["band"];
// For a score such as churn risk a high value is the bad news, so the
// colours swap; the words stay.
const flip: Record<Band, Band> = { high: "low", medium: "medium", low: "high" };
export const toneOf = (band: Band, higherIsBetter: boolean) => bandTone[higherIsBetter ? band : flip[band]];
const ringOf = (band: Band, higherIsBetter: boolean) => bandRing[higherIsBetter ? band : flip[band]];

// What kind of number a score is, so a guess is never read as a fact.
export const natureText: Record<RecordScore["nature"], string> = { measured: "Measured", ai: "AI-read", predicted: "Predicted" };
export const natureHint: Record<RecordScore["nature"], string> = {
  measured: "Worked out from what happened: usage, payments and the record's fields.",
  ai: "Read from conversations by your own AI. A judgement, not a count.",
  predicted: "An estimate of what will happen, built from the facts above it. Not a certainty.",
};

// Trend draws where a score has been as a small line, oldest first.
function Trend({ points, now }: { points: RecordScore["history"]; now: number }) {
  const values = [...points.map((p) => p.value), now];
  if (values.length < 3) return null;
  const w = 56;
  const h = 18;
  const path = values.map((v, i) => `${i === 0 ? "M" : "L"}${((i / (values.length - 1)) * w).toFixed(1)},${(h - 1 - (v / 100) * (h - 2)).toFixed(1)}`).join(" ");
  return (
    <svg width={w} height={h} viewBox={`0 0 ${w} ${h}`} className="shrink-0 text-muted-foreground" role="img" aria-label={`From ${values[0]} to ${now} over the last 90 days`}>
      <path d={path} fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}

function Change({ s }: { s: RecordScore }) {
  if (s.change === null || s.change === 0) return null;
  const good = s.change > 0 === s.higher_is_better;
  return (
    <span className={good ? "text-green-700 dark:text-green-400" : "text-red-700 dark:text-red-400"}>
      {s.change > 0 ? "Up" : "Down"} {Math.abs(s.change)} in 30 days
    </span>
  );
}

export function RecordScores({ brand, record, version }: { brand: string; record: string; version?: number }) {
  const [list, setList] = useState<RecordScore[]>([]);
  useEffect(() => {
    let live = true;
    void api.GET("/v1/brands/{brand}/records/{record}/scores", { params: { path: { brand, record } } }).then(({ data }) => {
      if (live) setList(data?.data ?? []);
    });
    return () => {
      live = false;
    };
  }, [brand, record, version]);
  if (list.length === 0) return null;
  // A headline score first, the scores it is made of right under it.
  const tops = list.filter((s) => !s.part_of);
  const ordered = tops.flatMap((s) => [s, ...list.filter((p) => p.part_of === s.key)]);
  return (
    <section aria-label="Scores" className="divide-y rounded-xl border">
      {ordered.map((s) => {
        const holding = s.factors.filter((f) => f.holds);
        const rest = s.factors.filter((f) => !f.holds);
        const part = Boolean(s.part_of);
        return (
          <details key={s.score_id} className={`group py-2 pr-3 ${part ? "pl-8" : "pl-3"}`}>
            <summary className="flex cursor-pointer list-none items-center gap-3 text-sm [&::-webkit-details-marker]:hidden">
              <span className={`flex shrink-0 items-center justify-center rounded-full border-2 font-semibold tabular-nums ${part ? "size-7 text-[11px]" : "size-9 text-[13px]"} ${ringOf(s.band, s.higher_is_better)}`}>{s.value}</span>
              <span className="min-w-0 flex-1">
                <span className={`block truncate ${part ? "" : "font-medium"}`}>{s.label}</span>
                <span className="flex flex-wrap items-center gap-x-2 text-xs text-muted-foreground">
                  <Status tone={toneOf(s.band, s.higher_is_better)}>{bandText[s.band]}</Status>
                  <span title={natureHint[s.nature]}>{natureText[s.nature]}</span>
                  <Change s={s} />
                </span>
              </span>
              <Trend points={s.history} now={s.value} />
              <span className="text-xs text-muted-foreground underline-offset-2 group-open:hidden group-hover:underline">Why?</span>
              <span className="hidden text-xs text-muted-foreground underline-offset-2 group-open:inline group-hover:underline">Hide</span>
            </summary>
            <div className={`mt-2 space-y-1 text-xs ${part ? "pl-10" : "pl-12"}`}>
              <p className="text-muted-foreground">{natureHint[s.nature]}</p>
              {s.evidence.length > 0 && (
                <ul className="space-y-1 border-l-2 pl-2">
                  {s.evidence.map((q, i) => (
                    <li key={i}>
                      “{q.quote}” <span className="text-muted-foreground">· sounded {q.label} · {q.channel === "call" ? "a call" : q.channel}</span>
                    </li>
                  ))}
                </ul>
              )}
              {s.parts.length > 0 ? (
                <ul className="space-y-1">
                  {s.parts.map((p) => (
                    <li key={p.key} className="flex gap-2">
                      <span className="w-8 shrink-0 text-right font-medium tabular-nums">{p.value ?? "–"}</span>
                      <span>
                        {p.label}
                        <span className="text-muted-foreground">
                          {p.value === null ? " · nothing to go on yet, so it is left out" : s.parts.some((x) => x.weight !== p.weight) ? ` · counts ${p.weight}×` : ""}
                        </span>
                      </span>
                    </li>
                  ))}
                  <li className="pt-1 text-muted-foreground">The average of the scores above, each shown below with what moves it.</li>
                </ul>
              ) : (
                <ul className="space-y-1">
                  <li className="flex gap-2 text-muted-foreground">
                    <span className="w-8 shrink-0 text-right tabular-nums">{s.base}</span>
                    <span>Where everyone starts</span>
                  </li>
                  {holding.map((f, i) => (
                    <li key={i} className="flex gap-2">
                      <span className={`w-8 shrink-0 text-right font-medium tabular-nums ${f.points > 0 === s.higher_is_better ? "text-green-700 dark:text-green-400" : "text-red-700 dark:text-red-400"}`}>{signed(f.points)}</span>
                      <span>{f.label}</span>
                    </li>
                  ))}
                  {holding.length === 0 && <li className="pl-10 text-muted-foreground">Nothing adds or takes points right now.</li>}
                  {rest.length > 0 && (
                    <li className="pt-1 text-muted-foreground">
                      Doesn't apply now: {rest.map((f) => `${f.label} (${signed(f.points)})`).join(", ")}
                    </li>
                  )}
                </ul>
              )}
            </div>
          </details>
        );
      })}
    </section>
  );
}
