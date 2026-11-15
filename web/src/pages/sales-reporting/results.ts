// Equal completed periods; activity counts are not cohort conversion rates.
export type Outcome = "replied" | "sdr" | "closed";
export type DealAmount = {
  currency: string;
  amount_minor: number | null;
  valued: number;
  unvalued: number;
};
export type ResultData = {
  from: string;
  to: string;
  source: { status: string };
  totals: Partial<Record<Outcome, number>>;
  results?: { amounts: DealAmount[] };
};

export function previousRange(from: string, to: string) {
  const start = Date.parse(from),
    end = Date.parse(to);
  if (!Number.isFinite(start) || !Number.isFinite(end) || end <= start)
    throw new Error("Invalid comparison period");
  return {
    from: new Date(start - (end - start)).toISOString(),
    to: new Date(start).toISOString(),
  };
}

export function outcomeCount(
  data: ResultData | undefined,
  stage: Outcome,
): number | null {
  if (!data || (stage === "replied" && data.source.status !== "connected"))
    return null;
  return data.totals[stage] ?? 0;
}

export function change(current: number | null, previous: number | null) {
  if (current === null || previous === null)
    return { label: "Comparison unavailable", direction: "flat" };
  const delta = current - previous;
  if (!delta) return { label: "No change", direction: "flat" };
  const direction = delta > 0 ? "up" : "down";
  if (!previous)
    return { label: "Up from 0 · no percentage baseline", direction };
  return {
    label: `${delta > 0 ? "+" : "−"}${new Intl.NumberFormat("en-US", { maximumFractionDigits: 1 }).format(Math.abs(delta / previous) * 100)}% vs previous period`,
    direction,
  };
}

export function dealComparisons(current: ResultData, previous?: ResultData) {
  const now = current.results?.amounts,
    before = previous?.results?.amounts;
  const currencies = [
    ...new Set([...(now ?? []), ...(before ?? [])].map((r) => r.currency)),
  ].sort();
  const value = (rows: DealAmount[] | undefined, currency: string) =>
    rows
      ? (rows.find((r) => r.currency === currency) ?? {
          currency,
          amount_minor: 0,
          valued: 0,
          unvalued: 0,
        })
      : undefined;
  return currencies.map((currency) => ({
    currency,
    current: value(now, currency),
    previous: value(before, currency),
  }));
}

// Known provider pages, independent of URLs returned by a connection.
const billingPages: Record<
  string,
  { name: string; url: string; action: string }
> = {
  parallel: {
    name: "Parallel",
    url: "https://platform.parallel.ai",
    action: "Add funds",
  },
  treg: { name: "Treg", url: "https://treg.to/app", action: "Add funds" },
  blitz: {
    name: "Blitz",
    url: "https://app.blitz-api.ai",
    action: "Manage plan",
  },
  openrouter: {
    name: "OpenRouter",
    url: "https://openrouter.ai/settings/credits",
    action: "Add funds",
  },
  bouncer: {
    name: "Bouncer",
    url: "https://app.usebouncer.com/",
    action: "Add funds",
  },
  apify: {
    name: "Apify",
    url: "https://console.apify.com/billing",
    action: "Add funds",
  },
};
export function billingPage(wallet: string | null) {
  return wallet && Object.hasOwn(billingPages, wallet)
    ? billingPages[wallet]
    : null;
}
