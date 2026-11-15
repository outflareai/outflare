import { Card } from "@/ui";
import {
  change,
  dealComparisons,
  outcomeCount,
  previousRange,
  type Outcome,
  type ResultData,
} from "./results";

const num = (n: number | null) =>
  n === null
    ? "—"
    : new Intl.NumberFormat("en-US", { maximumFractionDigits: 1 }).format(n);
const money = (n: number | null | undefined, currency: string) =>
  n == null
    ? "—"
    : new Intl.NumberFormat("en-US", { style: "currency", currency }).format(
        n / 100,
      );
const rangeLabel = (from: string, to: string) => {
  const fmt = new Intl.DateTimeFormat("en-GB", {
    timeZone: "UTC",
    day: "numeric",
    month: "short",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
  return `${fmt.format(new Date(from))} – ${fmt.format(new Date(to))} UTC`;
};

function Delta({
  current,
  previous,
  loading,
}: {
  current: number | null;
  previous: number | null;
  loading?: boolean;
}) {
  const c = change(current, previous);
  return (
    <span
      className={`sr-growth-change sr-growth-${loading ? "flat" : c.direction}`}
    >
      {loading ? "Loading comparison…" : c.label}
    </span>
  );
}

export function ResultsGrowth({
  current,
  previous,
  loading = false,
  onOpen,
}: {
  current: ResultData;
  previous?: ResultData;
  loading?: boolean;
  onOpen: (stage: Outcome) => void;
}) {
  const range = previousRange(current.from, current.to);
  const days = (Date.parse(current.to) - Date.parse(current.from)) / 86_400_000;
  const amounts = dealComparisons(current, previous);
  return (
    <section aria-label="Growth and outcomes" className="space-y-4">
      <div>
        <h2 className="text-base font-semibold">Growth & outcomes</h2>
        <p className="mt-1 text-xs text-muted-foreground">
          Current: {rangeLabel(current.from, current.to)}
        </p>
        <p className="mt-1 text-xs text-muted-foreground">
          Compared with: {rangeLabel(range.from, range.to)} · same length
        </p>
      </div>
      <div className="sr-summary-grid">
        {(
          [
            [
              "replied",
              "Replied",
              "Companies with a first recorded email reply",
            ],
            ["sdr", "SDR picked up", "First pickup by an SDR in Outflare"],
            ["closed", "Confirmed customers", "Customer confirmations in Outflare"],
          ] as const
        ).map(([stage, label, note]) => {
          const now = outcomeCount(current, stage),
            before = outcomeCount(previous, stage);
          return (
            <article key={stage} className="sr-summary" aria-label={label}>
              <span className="text-xs font-medium text-muted-foreground">
                {label}
              </span>
              <strong>{num(now)}</strong>
              <Delta current={now} previous={before} loading={loading} />
              <span className="text-xs text-muted-foreground">
                Previous: {num(before)} ·{" "}
                {num(now === null ? null : now / days)} per day now
              </span>
              <span className="text-xs text-muted-foreground">{note}</span>
              <button
                className="sr-text-link mt-auto w-fit"
                disabled={now === null}
                onClick={() => onOpen(stage)}
              >
                View leads →
              </button>
            </article>
          );
        })}
      </div>
      <Card
        title="Recorded monthly revenue"
        description="Expected monthly revenue of customers confirmed in each period, using their latest saved amounts. Payments not connected."
      >
        {amounts.length ? (
          <div className="sr-value-grid">
            {amounts.map((row) => (
              <div key={row.currency} className="space-y-2">
                <div className="text-xs font-medium text-muted-foreground">
                  {row.currency}
                </div>
                <div className="text-2xl font-semibold tracking-tight">
                  {money(row.current?.amount_minor, row.currency)}
                </div>
                <Delta
                  current={row.current?.amount_minor ?? null}
                  previous={row.previous?.amount_minor ?? null}
                  loading={loading}
                />
                <p className="text-xs text-muted-foreground">
                  Previous: {money(row.previous?.amount_minor, row.currency)}
                </p>
                <p className="text-sm">
                  Average per valued customer:{" "}
                  {money(
                    row.current?.valued && row.current.amount_minor !== null
                      ? row.current.amount_minor / row.current.valued
                      : null,
                    row.currency,
                  )}
                </p>
                <p className="text-xs text-muted-foreground">
                  {num(row.current?.valued ?? null)} with a value ·{" "}
                  {num(row.current?.unvalued ?? null)} without a value
                  {row.current?.unvalued || row.previous?.unvalued
                    ? " · comparison includes only recorded amounts"
                    : ""}
                </p>
              </div>
            ))}
          </div>
        ) : (
          <p className="text-sm text-muted-foreground">
            No deal values recorded in these periods yet.
          </p>
        )}
      </Card>
      <p className="text-xs text-muted-foreground">
        Growth compares recorded activity, not conversion rates between the same
        leads. Older unimported records can affect the baseline. Currencies stay
        separate; archived leads are not counted as lost deals.
      </p>
    </section>
  );
}
