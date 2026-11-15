import { useState } from "react";
import { ChevronLeft, ChevronRight } from "lucide-react";
import { dateLabel, number, stages, type Stage, type Report } from "./preview";
export type Observe = (action: string, value?: string, count?: number) => void;
export function Throughput({
  report,
  initialMetric,
  observe,
  onInspect,
  available = stages.map((s) => s.id),
}: {
  report: Report;
  initialMetric?: Stage;
  observe: Observe;
  onInspect?: (at: number, stages: Stage[]) => void;
  available?: Stage[];
}) {
  const [selectedStages, setStages] = useState<Stage[]>(
    initialMetric
      ? [initialMetric]
      : (["discovered", "researched", "verified"].filter((s) =>
          available.includes(s as Stage),
        ) as Stage[]),
  );
  const [selected, setSelected] = useState(report.buckets.length - 1);
  const [style, setStyle] = useState<"lines" | "bars">("lines");
  const active = selectedStages.length ? selectedStages : available.slice(0, 1);
  const buckets = report.buckets;
  const bucket = buckets[selected];
  const move = (i: number) => {
    setSelected(Math.max(0, Math.min(buckets.length - 1, i)));
    observe("bucket", report.period.id);
  };
  const inspect = (i = selected) => {
    setSelected(i);
    observe("interval_opened", report.period.id, active.length);
    onInspect?.(buckets[i].at, active);
  };
  const toggle = (id: Stage) => {
    if (active.includes(id) && active.length === 1) return;
    setStages(
      active.includes(id) ? active.filter((s) => s !== id) : [...active, id],
    );
    observe("metric", id);
  };
  const topStages = active.filter(
    (s) => !["replied", "sdr", "closed"].includes(s),
  );
  const resultStages = active.filter((s) =>
    ["replied", "sdr", "closed"].includes(s),
  );
  const panels = [topStages, resultStages].filter((p) => p.length);
  return (
    <section className="sr-chart-card" aria-label="Throughput">
      <div className="sr-chart-heading">
        <div>
          <h2 className="text-sm font-semibold">
            {report.period.hourly ? "Hourly" : "Daily"} throughput
          </h2>
          <p className="mt-1 text-xs text-muted-foreground">
            Compare milestones · complete{" "}
            {report.period.hourly ? "hours" : "days"} · UTC
          </p>
        </div>
        <div className="sr-periods" role="group" aria-label="Chart style">
          {(["lines", "bars"] as const).map((v) => (
            <button
              key={v}
              aria-pressed={style === v}
              onClick={() => {
                setStyle(v);
                observe("chart_style", v);
              }}
            >
              {v === "lines" ? "Lines" : "Bars"}
            </button>
          ))}
        </div>
      </div>
      <div className="sr-metrics" role="group" aria-label="Compare milestones">
        {stages.map((s) => (
          <button
            key={s.id}
            disabled={!available.includes(s.id)}
            aria-pressed={active.includes(s.id)}
            onClick={() => toggle(s.id)}
          >
            <span
              className="size-2 rounded-sm"
              style={{ background: s.color }}
            />
            {s.label}
            <span className="tabular-nums text-muted-foreground">
              {available.includes(s.id) ? number(report.totals[s.id]) : "—"}
            </span>
          </button>
        ))}
      </div>
      {panels.map((panel, p) => {
        const max = Math.max(
          1,
          ...buckets.flatMap((b) => panel.map((s) => b.counts[s])),
        );
        const unit = max > 100 ? 100 : max > 10 ? 10 : 1;
        const top = Math.ceil(max / unit) * unit;
        const width = 1100,
          height = panels.length > 1 ? 145 : 210,
          step = width / buckets.length;
        return (
          <div key={p}>
            <div className="mb-2 mt-3 text-xs text-muted-foreground">
              {panel === topStages
                ? "Lead processing"
                : "Replies & sales outcomes"}{" "}
              / {report.period.hourly ? "hour" : "day"}
              {panels.length > 1 ? " · separate scale" : ""}
            </div>
            <div
              className="sr-plot"
              style={{ height }}
              tabIndex={0}
              role="group"
              aria-label="Compare selected stages. Left and right arrows move; Enter opens the selected interval."
              onKeyDown={(e) => {
                if (
                  ["ArrowLeft", "ArrowRight", "Home", "End"].includes(e.key)
                ) {
                  e.preventDefault();
                  move(
                    e.key === "Home"
                      ? 0
                      : e.key === "End"
                        ? buckets.length - 1
                        : selected + (e.key === "ArrowLeft" ? -1 : 1),
                  );
                } else if (e.key === "Enter") {
                  e.preventDefault();
                  inspect();
                }
              }}
            >
              <div className="sr-y-axis" aria-hidden="true">
                <span>{number(top)}</span>
                <span>{number(top / 2)}</span>
                <span>0</span>
              </div>
              <svg
                viewBox={`0 0 ${width} ${height}`}
                preserveAspectRatio="none"
                role="img"
                aria-label={
                  panel
                    .map((id) => stages.find((s) => s.id === id)!.label)
                    .join(", ") + " comparison"
                }
              >
                {[0, height / 2, height].map((y) => (
                  <line
                    key={y}
                    x1="0"
                    x2={width}
                    y1={y}
                    y2={y}
                    className="sr-grid-line"
                  />
                ))}
                <rect
                  x={selected * step}
                  width={step}
                  height={height}
                  fill="currentColor"
                  opacity=".05"
                />
                {panel.map((id, j) => {
                  const stage = stages.find((s) => s.id === id)!;
                  return style === "lines" ? (
                    <polyline
                      key={id}
                      points={buckets
                        .map(
                          (b, i) =>
                            `${(i + 0.5) * step},${height - 2 - (b.counts[id] / top) * (height - 4)}`,
                        )
                        .join(" ")}
                      fill="none"
                      stroke={stage.color}
                      strokeWidth="2"
                      vectorEffect="non-scaling-stroke"
                    />
                  ) : (
                    buckets.map((b, i) => (
                      <rect
                        key={`${id}-${i}`}
                        x={
                          i * step +
                          step * 0.1 +
                          (j * step * 0.8) / panel.length
                        }
                        y={height - (b.counts[id] / top) * (height - 4)}
                        width={Math.max(0.5, (step * 0.8) / panel.length - 1)}
                        height={(b.counts[id] / top) * (height - 4)}
                        fill={stage.color}
                        opacity=".85"
                      />
                    ))
                  );
                })}
                {buckets.map((b, i) => (
                  <rect
                    key={b.at}
                    x={i * step}
                    width={step}
                    height={height}
                    fill="transparent"
                    onMouseEnter={() => setSelected(i)}
                    onClick={() => inspect(i)}
                    style={{ cursor: "pointer" }}
                  >
                    <title>
                      {dateLabel(b.at, report.period.hourly)} ·{" "}
                      {panel.map((s) => `${s}: ${b.counts[s]}`).join(" · ")}
                    </title>
                  </rect>
                ))}
              </svg>
            </div>
          </div>
        );
      })}
      <div className="sr-x-axis" aria-hidden="true">
        {[
          ...new Set([
            0,
            Math.floor((buckets.length - 1) / 2),
            buckets.length - 1,
          ]),
        ].map((i) => (
          <span key={i}>{dateLabel(buckets[i].at, report.period.hourly)}</span>
        ))}
      </div>
      <div className="sr-chart-readout">
        <div className="flex items-center gap-1">
          <button
            aria-label="Previous interval"
            disabled={selected === 0}
            onClick={() => move(selected - 1)}
          >
            <ChevronLeft className="size-4" />
          </button>
          <span className="text-xs font-medium">
            {dateLabel(bucket.at, report.period.hourly)}
          </span>
          <button
            aria-label="Next interval"
            disabled={selected === buckets.length - 1}
            onClick={() => move(selected + 1)}
          >
            <ChevronRight className="size-4" />
          </button>
        </div>
        <div className="flex flex-wrap gap-4 text-xs">
          {active.map((id) => (
            <span key={id}>
              <b className="tabular-nums">{number(bucket.counts[id])}</b>{" "}
              {stages.find((s) => s.id === id)!.label}
            </span>
          ))}
        </div>
        {onInspect && (
          <button
            className="text-xs font-medium text-primary"
            onClick={() => inspect()}
          >
            Inspect {report.period.hourly ? "hour" : "day"} →
          </button>
        )}
      </div>
      <p className="mt-3 text-xs text-muted-foreground">
        Counts use each milestone’s own time. Use “Same leads” below to measure
        progression from discovery.
      </p>
    </section>
  );
}
