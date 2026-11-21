// Results of past changes: what each approved or auto-applied change did, judged against
// as many days before it, in plain words ("This cut worked: 14 new
// customers at $38 each, before 9 at $41").

import { useEffect, useState } from "react";
import { api, errorMessage } from "../../api";
import { DataTable } from "@/components/data-table";
import { Card, ErrorText, Loading, Status } from "../../ui";
import { ago } from "../../format";
import { money, n, scopeQuery, type AdScope } from "../Ads";
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
  const cost = (v: unknown, f: Finding) => {
    const x = side(v, f.unit);
    if (x.count === null) return <span className="text-muted-foreground">—</span>;
    return (
      <div className="whitespace-nowrap">
        <div className="font-mono tabular-nums">{x.count && x.spend !== null ? money(Math.round(x.spend / x.count), f.currency) : "—"}</div>
        <div className="text-[12px] text-muted-foreground">{n(x.count)} {unitWord(f.unit, x.count)}</div>
      </div>
    );
  };
  return (
    <Card title="Results of past changes" description="Every change you approved, or that applied on its own, judged against as many days before it. Source: ad network spend, Outflare sign-ups and payments.">
      {error && <ErrorText>{error}</ErrorText>}
      <DataTable
        data={sorted}
        rowId={(f) => f.id}
        columnsMenu={false}
        empty="No results yet. Each approved change is measured for one to six weeks, then judged here against the days before it."
        mobile={(f) => (
          <div className="space-y-1">
            <div className="flex items-start justify-between gap-2"><span className="font-medium">{verdictSentence(f)}</span><Status tone={verdictTone[f.verdict]}>{verdictWord[f.verdict]}</Status></div>
            <div className="text-[12px] text-muted-foreground">{leverWord(f.kind)} · {f.campaign_name}</div>
          </div>
        )}
        columns={[
          { id: "result", header: "Result", accessorFn: (f) => verdictWord[f.verdict], cell: ({ row: { original: f } }) => <Status tone={verdictTone[f.verdict]}>{verdictWord[f.verdict]}</Status> },
          { id: "change", header: "Change", enableSorting: false, accessorFn: (f) => verdictSentence(f), cell: ({ row: { original: f } }) => (
            <div className="max-w-md whitespace-normal">
              <div className="font-medium">{verdictSentence(f)}</div>
              <div className="text-[12px] text-muted-foreground">{leverWord(f.kind)} · {f.campaign_name}{f.applied_auto ? " · applied on its own" : ""}</div>
              {f.verdict_note && !!f.result.after && <div className="text-[12px] text-muted-foreground">{f.verdict_note}</div>}
            </div>
          ) },
          { id: "before", header: `Before, each`, enableSorting: false, meta: { align: "right" }, cell: ({ row: { original: f } }) => cost(f.result.before, f) },
          { id: "after", header: "After, each", enableSorting: false, meta: { align: "right" }, cell: ({ row: { original: f } }) => cost(f.result.after, f) },
          { id: "measured", header: "Measured", accessorFn: (f) => f.measure_until ?? "", cell: ({ row: { original: f } }) => f.measure_from && f.measure_until ? <span className="whitespace-nowrap">{f.measure_from} to {f.measure_until}</span> : <span className="text-muted-foreground" title={f.window_note}>—</span> },
          { id: "judged", header: "Judged", accessorFn: (f) => f.judged_at ?? "", cell: ({ row: { original: f } }) => f.judged_at ? <span className="whitespace-nowrap">{ago(f.judged_at)}</span> : <span className="text-muted-foreground">—</span> },
        ]}
      />
    </Card>
  );
}
