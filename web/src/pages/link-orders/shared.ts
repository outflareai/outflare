import type { components } from "@outflare/sdk";

export type LinkOrder = components["schemas"]["LinkOrder"];
export type LinkOrderDonor = components["schemas"]["LinkOrderDonor"];
export type LinkOrderCandidate = components["schemas"]["LinkOrderCandidate"];
export type LinkOrderSettings = components["schemas"]["LinkOrderSettings"];
export type LinkOrderSummary = components["schemas"]["LinkOrderSummary"];
export type LinkOrderBudget = components["schemas"]["LinkOrderBudget"];
export type LinkOrderMoney = components["schemas"]["LinkOrderMoney"];
export type LinkOrderImpact = components["schemas"]["LinkOrderImpact"];
export type LinkOrderEquivalent = components["schemas"]["LinkOrderEquivalent"];
export type LinkOrderStatus = components["schemas"]["LinkOrderStatus"];
export type Member = components["schemas"]["Member"];

export const sections = ["overview", "catalog", "candidates", "orders", "checks", "impact", "payments"] as const;
export type Section = (typeof sections)[number];
export const sectionLabel: Record<Section, string> = {
  overview: "Overview",
  catalog: "Catalog",
  candidates: "Candidates",
  orders: "Orders",
  checks: "Checks",
  impact: "Impact",
  payments: "Payments",
};

export const statuses: LinkOrderStatus[] = [
  "requested",
  "quoted",
  "negotiating",
  "approved",
  "published",
  "paid",
  "verified",
  "lost",
];

export const statusLabel: Record<LinkOrderStatus, string> = {
  requested: "Requested",
  quoted: "Quoted",
  negotiating: "Negotiating",
  approved: "Approved",
  published: "Published",
  paid: "Paid",
  verified: "Verified",
  lost: "Lost",
};

export const statusTone: Record<LinkOrderStatus, "grey" | "blue" | "amber" | "green" | "red"> = {
  requested: "grey",
  quoted: "blue",
  negotiating: "amber",
  approved: "blue",
  published: "blue",
  paid: "green",
  verified: "green",
  lost: "red",
};

export const spentStatuses: LinkOrderStatus[] = ["paid", "verified", "lost"];
export const committedStatuses: LinkOrderStatus[] = ["approved", "published"];
export const liveStatuses: LinkOrderStatus[] = ["published", "paid", "verified"];

export const dash = "—";

export function money(amountMinor: number | null | undefined, currency: string | null | undefined): string {
  if (amountMinor == null || !currency) return dash;
  try {
    const digits =
      new Intl.NumberFormat(undefined, { style: "currency", currency }).resolvedOptions().maximumFractionDigits ?? 2;
    return new Intl.NumberFormat(undefined, { style: "currency", currency }).format(amountMinor / 10 ** digits);
  } catch {
    return `${amountMinor} ${currency}`;
  }
}

export function moneyList(rows: LinkOrderMoney[] | null | undefined): string {
  if (!rows?.length) return dash;
  return rows.map((r) => money(r.amount_minor, r.currency)).join(" · ");
}

export function parseMajor(value: string, currency: string): number | null {
  const n = Number(value.trim().replace(",", "."));
  if (!Number.isFinite(n) || n < 0) return null;
  try {
    const digits =
      new Intl.NumberFormat(undefined, { style: "currency", currency }).resolvedOptions().maximumFractionDigits ?? 2;
    return Math.round(n * 10 ** digits);
  } catch {
    return Math.round(n * 100);
  }
}

export function day(iso: string | null | undefined): string {
  if (!iso) return dash;
  return new Date(iso).toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" });
}

export function slotLabel(s: string): string {
  if (s === "guest_post") return "Guest post";
  if (s === "insert") return "Link insert";
  return s || dash;
}

export function durationLabel(s: string): string {
  if (s === "permanent") return "Lifetime";
  if (s === "limited") return "Time-limited";
  return s || dash;
}

export function shortId(id: string): string {
  return id.slice(0, 8);
}

export function donorBy(donors: LinkOrderDonor[], id: string | null | undefined): LinkOrderDonor | undefined {
  return id ? donors.find((d) => d.id === id) : undefined;
}

export function idemp(): { "Idempotency-Key": string } {
  return { "Idempotency-Key": crypto.randomUUID() };
}

export function passingLiveCheck(o: LinkOrder): boolean {
  return o.last_check_live === true && o.last_check_target_match === true;
}

export function amountOf(o: LinkOrder): { amount: number | null; currency: string | null } {
  return {
    amount: o.donor_amount_minor ?? o.quoted_amount_minor,
    currency: o.currency ?? o.quoted_currency,
  };
}

export const catalogExampleCSV = `domain,name,pbn,country,languages,niches,casino_allowed,rel_offered,slot_types,duration,tat_days,guarantee_days,domain_rating,amount_minor,currency
example.com,Example Review,false,US,EN,proxies,false,dofollow,guest_post,permanent,14,90,50,85000,USD
`;

export type PageData = {
  ws: string;
  brand: string;
  myId: string;
  can: (scope: string) => boolean;
  canManage: boolean;
  canFinanceRead: boolean;
  isApprover: boolean;
  canPay: boolean;
  settings: LinkOrderSettings | null;
  donors: LinkOrderDonor[];
  orders: LinkOrder[];
  candidates: LinkOrderCandidate[];
  summary: LinkOrderSummary | null;
  budget: LinkOrderBudget | null;
  members: Member[];
  reload: () => void;
  goSection: (section: Section, orderId?: string) => void;
};

export function approverGate(isApprover: boolean, settings: LinkOrderSettings | null): string | undefined {
  if (!settings?.approver_user_id) return "Approve and Paid stay blocked until an approver is set.";
  if (!isApprover) return "Only the brand's designated approver can approve or mark Paid.";
  return undefined;
}

export function payGate(o: LinkOrder, canPay: boolean, isApprover: boolean, settings: LinkOrderSettings | null): string | undefined {
  const gate = approverGate(isApprover, settings);
  if (gate) return gate;
  if (!canPay) return "Marking Paid also needs Coverage finance manage.";
  if (o.status !== "published" && o.status !== "verified") return "Paid is recorded after publication.";
  if (!passingLiveCheck(o)) return "Paid only after a passing live-link check (the link is on the page and the target matches).";
  return undefined;
}

export function memberName(members: Member[], id: string | null | undefined): string {
  if (!id) return dash;
  const m = members.find((x) => x.user_id === id);
  return m?.name || m?.email || shortId(id);
}
