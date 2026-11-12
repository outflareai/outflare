// Verdicts: what each approved or auto-applied change did, judged against
// as many days before it, in plain words ("This cut worked: 14 new
// customers at $38 each, before 9 at $41").

import { useEffect, useState } from "react";
import { api, errorMessage } from "../../api";
import { Badge, ErrorText, Loading } from "../../ui";
import { ago } from "../../format";
import { money, n, platformName, scopeQuery, type AdScope } from "../Ads";
import { isJudged, leverWord, unitWord, type Finding } from "./api";

const verdictWord = { worked: "Worked", did_not_work: "Didn't work", not_sure: "Not sure", "": "Not judged" } as const;
const verdictTone = { worked: "green", did_not_work: "red", not_sure: "grey", "": "grey" } as const;
const nouns: Record<string, string> = {
  budget_up: "raise", budget_down: "cut", pause: "pause", resume: "resume", negative_add: "negative keyword", keyword_add: "keyword", keyword_pause: "keyword pause",
  keyword_split: "split", ad_pause: "ad pause", target_tighten: "tighter target", target_loosen: "looser target", bidding_target: "bidding target", exclusion_add: "exclusion",
  consolidate: "merge", experiment_start: "test", overspend_pause: "protective pause",
};
const num = (v: unknown): number | null => (typeof v === "number" && Number.isFinite(v) ? v : null);

type Side = { count: number | null; spend: number | null; days: number | null };
function side(v: unknown, unit: Finding["unit"]): Side {
  const o = (v && typeof v === "object" ? v : {}) as Record<string, unknown>;
  const count = unit === "payers" ? num(o.new_payers) ?? num(o.payers) : unit === "signups" ? num(o.signups) : num(o.conversions) ?? num(o.new_payers);
  return { count, spend: num(o.spend_micros), days: num(o.days) };
}
const at = (s: Side, currency: string) => (s.count && s.spend !== null ? ` at ${money(Math.round(s.spend / s.count), currency)}` : "");

// The verdict as a sentence a person would say.
export function verdictSentence(f: Finding): string {
  const noun = nouns[f.kind] ?? leverWord(f.kind).toLowerCase();
  const head = f.verdict === "worked" ? `This ${noun} worked` : f.verdict === "did_not_work" ? `This ${noun} didn't work` : `Not sure about this ${noun}`;
  const after = side(f.result.after, f.unit);
  const before = side(f.result.before, f.unit);
  if (after.count === null && before.count === null) return `${head}.${f.verdict_note ? ` ${f.verdict_note}` : ""}`;
  const a = after.count !== null ? `${n(after.count)} ${unitWord(f.unit, after.count)}${at(after, f.currency)}${at(after, f.currency) ? " each" : ""}` : "no numbers yet";
  const b = before.count !== null ? `, before ${n(before.count)}${at(before, f.currency)}` : "";
  return `${head}: ${a}${b}.`;
}

type Props = { brand: string; scope: AdScope };

export function Verdicts({ brand, scope }: Props) {
  const [rows, setRows] = useState<Finding[] | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    let live = true;
    api.GET("/v1/brands/{brand}/ads/findings", { params: { path: { brand }, query: { status: "judged", ...scopeQuery(scope), limit: 300 } } }).then(({ data, error }) => {
      if (!live) return;
      setError(error ? errorMessage(error) : "");
      setRows(((data?.data ?? []) as Finding[]).filter(isJudged));
    });
    return () => {
      live = false;
    };
  }, [brand, scope.network, scope.account]);

  if (error && !rows) return <ErrorText>{error}</ErrorText>;
  if (!rows) return <Loading />;
  const sorted = [...rows].sort((a, b) => (b.judged_at ?? "").localeCompare(a.judged_at ?? ""));
  return (
    <section aria-labelledby="ads-verdicts" className="space-y-3">
      <h2 id="ads-verdicts" className="text-[13px] font-medium text-muted-foreground">Verdicts{rows.length > 0 && <span className="ml-2 font-mono text-[12px]">{rows.length}</span>}</h2>
      {error && <ErrorText>{error}</ErrorText>}
      {sorted.length === 0 ? (
        <p className="rounded-lg border border-dashed px-4 py-6 text-center text-[13px] text-muted-foreground">
          No verdicts yet. Each approved or auto-applied change is measured for one to six weeks, then judged here against the days before it.
        </p>
      ) : (
        <ul className="grid gap-3 lg:grid-cols-2">
          {sorted.map((f) => (
            <li key={f.id} className="space-y-2 rounded-lg border bg-card p-4 shadow-xs">
              <div className="flex flex-wrap items-start justify-between gap-2">
                <p className="text-[14px] font-medium">{verdictSentence(f)}</p>
                <Badge tone={verdictTone[f.verdict]}>{verdictWord[f.verdict]}</Badge>
              </div>
              <div className="text-[12px] text-muted-foreground">{leverWord(f.kind)} · {f.campaign_name} · {platformName(f.connector)} · {f.account_name}</div>
              {f.verdict_note && !!f.result.after && <p className="text-[13px] text-muted-foreground">{f.verdict_note}</p>}
              <div className="text-[12px] text-muted-foreground">
                {f.measure_from && f.measure_until ? <>Measured {f.measure_from} to {f.measure_until}</> : <span title={f.window_note}>Measured</span>}
                {f.judged_at && <> · judged {ago(f.judged_at)}</>}
                {f.applied_auto && <> · applied on its own</>}
              </div>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
