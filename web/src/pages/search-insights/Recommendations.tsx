import { useEffect, useState } from "react";
import type { SearchInsightReport, SearchInsightRow } from "@outflare/sdk";
import { api, errorMessage } from "../../api";
import { Button, Loading } from "../../ui";
import { findingAdvice, reports, type Filters, type Report } from "./model";
export function Recommendations({
  path,
  onInspect,
  onExplore,
}: {
  path: { brand: string; site: string };
  onInspect: (row: SearchInsightRow, filters: Filters) => void;
  onExplore: (filters: Filters) => void;
}) {
  const [data, setData] = useState<SearchInsightReport[]>();
  const [errors, setErrors] = useState<string[]>([]);
  const [version, setVersion] = useState(0);
  useEffect(() => {
    let live = true;
    Promise.allSettled(
      (["opportunities", "decay", "vitals"] as Report[]).map(async (report) => {
        const { data, error } = await api.GET(
          "/v1/brands/{brand}/seo-sites/{site}/insights",
          { params: { path, query: { report } } },
        );
        if (error || !data)
          throw Error(`${reports[report]}: ${errorMessage(error)}`);
        return data;
      }),
    ).then((results) => {
      if (!live) return;
      setData(
        results.flatMap((r) => (r.status === "fulfilled" ? [r.value] : [])),
      );
      setErrors(
        results.flatMap((r) =>
          r.status === "rejected" ? [String(r.reason.message)] : [],
        ),
      );
    });
    return () => {
      live = false;
    };
  }, [path.brand, path.site, version]);
  const findings = (data ?? [])
    .flatMap((r) =>
      r.data
        .filter((x) => r.report !== "vitals" || x.status === "regressed")
        .slice(0, r.report === "opportunities" ? 3 : 2)
        .map((row) => ({ row, result: r })),
    )
    .slice(0, 6);
  return (
    <section className="space-y-3 rounded-xl border bg-card p-5">
      <div className="flex flex-wrap justify-between gap-2">
        <div>
          <h3 className="font-semibold">Recommended next actions</h3>
          <p className="text-sm text-muted-foreground">
            Fresh findings from stored search and page-experience data. Review
            the evidence before saving work.
          </p>
        </div>
        <Button variant="ghost" onClick={() => setVersion((v) => v + 1)}>
          Refresh findings
        </Button>
      </div>
      {!data ? (
        <Loading />
      ) : findings.length ? (
        <div className="grid gap-3 md:grid-cols-2">
          {findings.map(({ row, result }) => {
            const advice = findingAdvice(result.report, row);
            return (
              <button
                key={`${result.report}:${row.key}`}
                className="space-y-2 rounded-lg border p-4 text-left hover:bg-muted/30"
                onClick={() =>
                  onInspect(row, {
                    report: result.report,
                    from: result.from,
                    to: result.to,
                  })
                }
              >
                <p className="text-xs font-medium text-muted-foreground">
                  {reports[result.report]} · {result.from} – {result.to}
                </p>
                <h4 className="font-medium">{advice.title} →</h4>
                <p
                  className="truncate text-xs text-muted-foreground"
                  title={row.page || row.key}
                >
                  {row.page || row.key}
                </p>
                <p className="text-sm">{advice.why}</p>
              </button>
            );
          })}
        </div>
      ) : (
        <p className="text-sm text-muted-foreground">
          No actionable findings in the available data. Explore a longer period
          or check that your connections have finished syncing.
        </p>
      )}
      {errors.length > 0 && (
        <div role="alert" className="space-y-1 text-sm text-destructive">
          {errors.map((e) => (
            <p key={e}>{e}</p>
          ))}
          <Button variant="ghost" onClick={() => setVersion((v) => v + 1)}>
            Retry missing analyses
          </Button>
        </div>
      )}
      <Button
        variant="ghost"
        onClick={() => onExplore({ report: "opportunities" })}
      >
        Explore all opportunities →
      </Button>
    </section>
  );
}
