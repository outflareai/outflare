import type { SearchInsightRow } from "@outflare/sdk";
import { type Report, number, reports } from "./model";
export function InsightChart({
  report,
  rows,
}: {
  report: Report;
  rows: SearchInsightRow[];
}) {
  const points =
    report === "decay"
      ? (rows[0]?.values ?? [])
      : rows
          .filter(
            (r) =>
              r.date &&
              (report !== "changes" || r.status === "sustained_change"),
          )
          .map((r) => ({
            date: r.date!,
            value: report === "changes" ? (r.after ?? 0) : (r.value ?? 0),
          }));
  // A vitals chart must show one metric/URL/device, never mix unlike units.
  const first = rows[0];
  const data =
    report === "vitals"
      ? rows
          .filter(
            (r) =>
              r.page === first?.page &&
              r.device === first?.device &&
              r.metric === first?.metric,
          )
          .sort((a, b) => (a.date ?? "").localeCompare(b.date ?? ""))
          .map((r) => ({ date: r.date!, value: r.value ?? 0 }))
      : points;
  if (data.length < 2) return null;
  const values = data.map((p) => p.value);
  const max = Math.max(...values, 1);
  const line = data
    .map(
      (p, i) =>
        `${20 + (i / (data.length - 1)) * 760},${170 - (p.value / max) * 140}`,
    )
    .join(" ");
  return (
    <figure className="rounded border p-3">
      <figcaption className="mb-2 text-xs text-muted-foreground">
        {report === "decay"
          ? rows[0]?.page
          : report === "vitals"
            ? `${first?.metric} · ${first?.device} · ${first?.page}`
            : reports[report]}
      </figcaption>
      <svg
        role="img"
        aria-label={`${reports[report]} from ${data[0].date} to ${data[data.length - 1].date}`}
        viewBox="0 0 800 200"
        className="w-full max-h-56"
      >
        <line
          x1="20"
          y1="170"
          x2="780"
          y2="170"
          stroke="currentColor"
          opacity=".2"
        />
        <polyline
          points={line}
          fill="none"
          stroke="currentColor"
          strokeWidth="2"
        />
        <text x="20" y="195" fontSize="11" fill="currentColor">
          {data[0].date}
        </text>
        <text x="700" y="195" fontSize="11" fill="currentColor">
          {data[data.length - 1].date}
        </text>
        <text x="20" y="15" fontSize="11" fill="currentColor">
          {number(max)}
        </text>
      </svg>
    </figure>
  );
}
