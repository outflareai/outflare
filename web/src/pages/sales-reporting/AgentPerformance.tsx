import { useState } from "react";
import { Card } from "@/ui";
import type { Schemas } from "@outflare/sdk";

export type Attribution = Schemas["SalesAttribution"];
export type AgentPerformance = Schemas["SalesAgentPerformance"];
const number = (n: number | undefined) =>
  n === undefined ? "—" : n.toLocaleString("en-US");
export function AttributionLabel({ value }: { value?: Attribution }) {
  return (
    <span title={value?.id !== "unknown" ? value?.id : undefined}>
      {value?.status === "unavailable"
        ? "Origin unavailable"
        : value?.name || "Unknown"}
      {value?.revoked && (
        <small className="ml-1 text-muted-foreground">(revoked)</small>
      )}
    </span>
  );
}

export function AgentPerformanceTable({
  rows,
  onViewChange,
  connected,
  coverage,
}: {
  rows?: AgentPerformance[];
  onViewChange: (view: "progress" | "quality") => void;
  connected: boolean;
  coverage?: {
    native_milestones?: number;
    known?: number;
    unavailable?: number;
  };
}) {
  const [view, setView] = useState<"progress" | "quality">("progress");
  const selectView = (value: "progress" | "quality") => {
    setView(value);
    onViewChange(value);
  };
  const sorted = [...(rows || [])].sort(
    (a, b) =>
      (b.counts.discovered || 0) - (a.counts.discovered || 0) ||
      a.attribution.name.localeCompare(b.attribution.name) ||
      a.attribution.id.localeCompare(b.attribution.id),
  );
  const columns =
    view === "progress"
      ? [
          ["discovered", "Discovered"],
          ["researched", "Researched"],
          ["verified", "Verified"],
          ["emailed", "First email"],
          ["replied", "Replied"],
          ["sdr", "SDR picked up"],
          ["closed", "Confirmed customers"],
        ]
      : [
          ["submitted", "Submitted"],
          ["accepted", "Accepted"],
          ["duplicates", "Duplicates"],
          ["blocked", "Blocked"],
          ["held", "Held"],
        ];
  return (
    <Card
      title="Agent performance"
      description="Original uploader across the selected period. Current researchers and Sales owners do not replace the lead’s origin."
    >
      <div
        className="sr-periods mb-4 w-fit"
        role="group"
        aria-label="Agent statistics"
      >
        <button
          aria-pressed={view === "progress"}
          onClick={() => selectView("progress")}
        >
          Lead progress
        </button>
        <button
          aria-pressed={view === "quality"}
          onClick={() => selectView("quality")}
        >
          Submission quality
        </button>
      </div>
      {sorted.length ? (
        <div className="sr-table-wrap">
          <table className="sr-table">
            <thead>
              <tr>
                <th>Source / agent</th>
                {columns.map(([key, label]) => (
                  <th key={key}>{label}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {sorted.map((row) => (
                <tr key={row.attribution.id}>
                  <td>
                    <AttributionLabel value={row.attribution} />
                    <small className="block text-muted-foreground">
                      {
                        {
                          worker: "Discovery agent",
                          api_key: "Portal API key",
                          sales_agent: "Sales API agent",
                          manual: "Manual submission",
                          unknown: "Original submitter not recorded",
                        }[row.attribution.kind]
                      }
                      {row.attribution.id !== "unknown" &&
                        ` · ${row.attribution.id.split(":").at(-1)?.slice(0, 8)}`}
                    </small>
                  </td>
                  {columns.map(([key]) => (
                    <td key={key}>
                      {view === "progress"
                        ? number(
                            !connected && !["sdr", "closed"].includes(key)
                              ? undefined
                              : row.counts[key] || 0,
                          )
                        : number(
                            row.intake
                              ? Number(row.intake[key] || 0)
                              : undefined,
                          )}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <p className="text-sm text-muted-foreground">
          {connected
            ? "No source activity recorded in this period."
            : "Connect Lead Portal to see agent attribution."}
        </p>
      )}
      <p className="mt-3 text-xs text-muted-foreground">
        {view === "progress"
          ? "Milestones use their own event dates. These counts show activity, not conversion rates for the same set of leads. Unknown origins stay visible in totals."
          : "Discovery API submissions only. Each agent/job/company is counted once using its latest recorded outcome in this period. Repeated requests do not inflate the counts. — means this intake history is unavailable."}
      </p>
      {!!coverage?.native_milestones && (
        <p className="mt-2 text-xs text-muted-foreground">
          {number(coverage.known)} of {number(coverage.native_milestones)}{" "}
          SDR/closed milestones have a known uploader.
          {!!coverage.unavailable &&
            ` Origin lookup was unavailable for ${number(coverage.unavailable)}; refresh to retry.`}
        </p>
      )}
    </Card>
  );
}
