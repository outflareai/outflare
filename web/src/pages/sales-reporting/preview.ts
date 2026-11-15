// Deliberately local preview data. No provider calls, workspace credentials or
// production records are used. Replace this adapter when reporting is connected.
export const stages = [
  {
    id: "discovered",
    label: "Discovered",
    description: "Accepted from the leads API",
    color: "var(--series-6)",
    queue: "248 awaiting research",
  },
  {
    id: "researched",
    label: "Researched",
    description: "Research completed",
    color: "var(--series-4)",
    queue: "6 need evidence review",
  },
  {
    id: "verified",
    label: "Verified",
    description: "Ready for outreach",
    color: "var(--series-3)",
    queue: "32 awaiting Bison sync",
  },
  {
    id: "emailed",
    label: "First email",
    description: "Confirmed first sends",
    color: "var(--series-1)",
    queue: "419 awaiting first email",
  },
  {
    id: "replied",
    label: "Replied",
    description: "Leads with a first reply",
    color: "var(--series-2)",
    queue: "8 interested, awaiting SDR",
  },
  {
    id: "sdr",
    label: "SDR picked up",
    description: "Accepted by an SDR",
    color: "var(--series-6)",
    queue: "22 active with the team",
  },
  {
    id: "closed",
    label: "Confirmed customers",
    description: "Customer confirmations",
    color: "var(--series-3)",
    queue: "Explore customers & amounts",
  },
] as const;
export type Stage = (typeof stages)[number]["id"];
export type Counts = Record<Stage, number>;
export const periods = [
  { id: "24h", label: "24 hours", count: 24, hourly: true },
  { id: "48h", label: "48 hours", count: 48, hourly: true },
  { id: "7d", label: "Week", count: 7, hourly: false },
  { id: "30d", label: "Month", count: 30, hourly: false },
  { id: "365d", label: "Year", count: 365, hourly: false },
] as const;
export type Period = (typeof periods)[number]["id"];
export type Bucket = { at: number; counts: Counts };
export type Lead = {
  id: string;
  company: string;
  contact: string;
  email: string;
  useCase: string;
  owner: string;
  status: string;
  next: string;
  firstEmailAt: number;
  repliedAt: number;
  pickedUpAt: number;
  closedAt?: number;
  outcome?: "customer";
  valueCents: number | null;
};
export type Report = {
  period: (typeof periods)[number];
  from: number;
  to: number;
  buckets: Bucket[];
  totals: Counts;
  deals: Lead[];
};
export const previewAsOf = Date.UTC(2026, 10, 11, 10);
const HOUR = 3_600_000;
const DAY = 24 * HOUR;
const emptyCounts = (): Counts => ({
  discovered: 0,
  researched: 0,
  verified: 0,
  emailed: 0,
  replied: 0,
  sdr: 0,
  closed: 0,
});
const noise = (n: number) => {
  const x = Math.sin(n * 12.9898 + 78.233) * 43758.5453;
  return x - Math.floor(x);
};
const companyNames = [
  "Juniper Search",
  "Lumen Software",
  "Fable Commerce",
  "Bridgepoint Data",
  "Atlas Retail",
  "Harbor Analytics",
  "Cedar Insights",
  "Aster Markets",
];
const contacts = [
  "Maya Singh",
  "Daniel Kim",
  "Ava Johnson",
  "Ethan Brooks",
  "Sofia Rivera",
  "Noah Patel",
  "Olivia Reed",
  "Liam Walker",
];
const useCases = [
  "Search result monitoring",
  "Company data enrichment",
  "Retail stock tracking",
  "Job market intelligence",
  "Marketplace pricing",
  "Property listing data",
  "Travel price monitoring",
  "Financial news research",
];
const owners = ["Alex K.", "Jamie L.", "Sam R."];

function closedLead(at: number): Lead {
  const n = Math.floor(at / HOUR);
  const i = n % companyNames.length;
  const valueCents = [null, 120000, 195000, 240000][n % 4];
  return {
    id: `preview-deal-${n}`,
    company: companyNames[i],
    contact: contacts[i],
    email: `contact@${["juniper", "lumen", "fable", "bridgepoint", "atlas", "harbor", "cedar", "aster"][i]}.example`,
    useCase: useCases[i],
    owner: owners[n % owners.length],
    status: "Customer",
    next: "Customer onboarding",
    firstEmailAt: at - 19 * DAY,
    repliedAt: at - 10 * DAY,
    pickedUpAt: at - 9 * DAY,
    closedAt: at,
    outcome: "customer",
    valueCents,
  };
}

// Independent event counts, not a cohort funnel: a first send this month can
// belong to a lead discovered last month. Closed totals come from the records
// behind them, so filters and financial totals always reconcile in the preview.
const hours: Bucket[] = [];
const allDeals: Lead[] = [];
for (
  let at = Math.floor(previewAsOf / DAY) * DAY - 730 * DAY;
  at < previewAsOf;
  at += HOUR
) {
  const hour = new Date(at).getUTCHours();
  const day = Math.floor(at / DAY);
  const n = Math.floor(at / HOUR);
  const weekday = new Date(at).getUTCDay();
  const activity =
    (weekday === 0 || weekday === 6 ? 0.65 : 1) * (0.7 + noise(day) * 0.6);
  const discovered = Math.round((38 + 50 * noise(n)) * activity);
  const closed = [9, 14, 16, 18].includes(hour) && noise(n + 23) > 0.18 ? 1 : 0;
  hours.push({
    at,
    counts: {
      discovered,
      researched: Math.round(discovered * (0.58 + noise(n + 3) * 0.3)),
      verified: Math.round(discovered * (0.48 + noise(n + 7) * 0.25)),
      emailed: Math.round((30 + 57 * noise(n + 11)) * activity),
      replied: Math.round(noise(n + 29) * 5 * activity),
      sdr: noise(n + 41) > 0.45 ? Math.ceil(noise(n + 43) * 2 * activity) : 0,
      closed,
    },
  });
  if (closed) allDeals.push(closedLead(at));
}

export function reportFor(id: Period, offset = 0): Report {
  const period = periods.find((p) => p.id === id)!;
  const unit = period.hourly ? HOUR : DAY;
  const to =
    (period.hourly ? previewAsOf : Math.floor(previewAsOf / DAY) * DAY) +
    offset * period.count * unit;
  const from = to - period.count * unit;
  const buckets = Array.from({ length: period.count }, (_, i) => ({
    at: from + i * unit,
    counts: emptyCounts(),
  }));
  const totals = emptyCounts();
  for (const hour of hours) {
    if (hour.at < from || hour.at >= to) continue;
    const bucket = buckets[Math.floor((hour.at - from) / unit)];
    for (const { id: stage } of stages) {
      bucket.counts[stage] += hour.counts[stage];
      totals[stage] += hour.counts[stage];
    }
  }
  return {
    period,
    from,
    to,
    buckets,
    totals,
    deals: allDeals
      .filter((d) => d.closedAt! >= from && d.closedAt! < to)
      .reverse(),
  };
}

export function exampleLeads(stage: Stage, report: Report): Lead[] {
  if (stage === "closed") return report.deals;
  return companyNames.slice(4).map((company, i) => {
    const at = report.to - (i + 1) * (report.period.hourly ? HOUR : DAY);
    return {
      id: `preview-${stage}-${i}`,
      company,
      contact: contacts[i + 4],
      email: `contact@${["atlas", "harbor", "cedar", "aster"][i]}.example`,
      useCase: useCases[i + 4],
      owner: stage === "sdr" ? owners[i % owners.length] : "Unassigned",
      status:
        stage === "sdr"
          ? [
              "Introduction sent",
              "With AE",
              "Trial in progress",
              "In conversation",
            ][i]
          : stages.find((s) => s.id === stage)!.label,
      next:
        stage === "sdr"
          ? [
              "Answer technical questions",
              "AE to accept handoff",
              "Review trial usage",
              "Confirm data requirements",
            ][i]
          : {
              discovered: "Research company",
              researched: "Verify contact",
              verified: "Sync to EmailBison",
              emailed: "Await reply",
              replied: "SDR to pick up",
              closed: "",
            }[stage],
      firstEmailAt: stage === "emailed" ? at : at - 7 * DAY,
      repliedAt: stage === "replied" ? at : at - DAY,
      pickedUpAt: at,
      valueCents: null,
    };
  });
}

export const tools = [
  {
    name: "Parallel",
    wallet: "parallel",
    purpose: "Company research",
    hourCents: 412,
    dayCents: 9620,
    balanceCents: 10345,
  },
  {
    name: "Treg",
    wallet: "treg",
    purpose: "Contact discovery",
    hourCents: 186,
    dayCents: 4880,
    balanceCents: 17747,
  },
  {
    name: "Bouncer",
    wallet: "bouncer",
    purpose: "Email verification",
    hourCents: 83,
    dayCents: 2240,
    balanceCents: 8450,
  },
  {
    name: "Apify",
    wallet: "apify",
    purpose: "Source collection",
    hourCents: 35,
    dayCents: 810,
    balanceCents: 3159,
  },
  {
    name: "AI models",
    wallet: "openrouter",
    purpose: "Classification & enrichment",
    hourCents: 29,
    dayCents: 690,
    balanceCents: null,
  },
] as const;
export const spend = {
  hourCents: tools.reduce((n, t) => n + t.hourCents, 0),
  dayCents: tools.reduce((n, t) => n + t.dayCents, 0),
  verifiedLeads: reportFor("24h").totals.verified,
};
export function costPerLead(cents: number, leads: number): number | null {
  return leads > 0 ? cents / 100 / leads : null;
}
export const number = (n: number) => n.toLocaleString("en-US");
export const money = (cents: number) =>
  new Intl.NumberFormat("en-US", {
    style: "currency",
    currency: "USD",
    maximumFractionDigits: 2,
  }).format(cents / 100);
export const unitCost = (cents: number) => {
  const cost = costPerLead(cents, spend.verifiedLeads);
  return cost === null ? "—" : `$${cost.toFixed(4)}`;
};
export const dateLabel = (at: number, hourly = false) =>
  new Intl.DateTimeFormat("en-GB", {
    timeZone: "UTC",
    day: "numeric",
    month: "short",
    ...(hourly ? { hour: "2-digit", minute: "2-digit" } : {}),
  }).format(at);
export const periodLabel = (r: Report) =>
  `${dateLabel(r.from)} ${new Date(r.from).getUTCFullYear()} – ${dateLabel(r.to - 1)} ${new Date(r.to - 1).getUTCFullYear()}`;
