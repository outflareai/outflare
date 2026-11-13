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
export type LinkOrderTarget = components["schemas"]["LinkOrderTarget"];
export type LinkOrderTargetKind = components["schemas"]["LinkOrderTargetKind"];
export type LinkOrderTargetSuggestion = components["schemas"]["LinkOrderTargetSuggestion"];
export type LinkOrderTargetSources = components["schemas"]["LinkOrderTargetSources"];
export type LinkOrderTargetReason = components["schemas"]["LinkOrderTargetReason"];
export type LinkOrderAnchorOptions = components["schemas"]["LinkOrderAnchorOptions"];
export type LinkOrderAnchorOption = components["schemas"]["LinkOrderAnchorOption"];

export const sections = ["overview", "catalog", "targets", "candidates", "orders", "checks", "impact", "payments"] as const;
export type Section = (typeof sections)[number];
export const sectionLabel: Record<Section, string> = {
  overview: "Overview",
  catalog: "Catalog",
  targets: "Targets",
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

// The currencies an invoice may be in, as the API lists them.
export const invoiceCurrencies = [
  "USD", "EUR", "GBP", "PLN", "CZK", "UAH", "CHF", "CAD", "AUD", "NZD", "JPY", "SEK",
  "NOK", "DKK", "RON", "HUF", "BGN", "TRY", "INR", "SGD", "HKD", "MXN", "BRL", "ZAR",
] as const;
export type InvoiceCurrency = (typeof invoiceCurrencies)[number];
export type LinkOrderInvoice = components["schemas"]["LinkOrderInvoice"];

// Decimal places in a currency's amounts, the same as the engine's
// (fxrates.MinorDigits): yen-style currencies have none.
export function minorDigits(currency: string): number {
  return ["JPY", "KRW", "ISK", "HUF"].includes(currency) ? 0 : 2;
}

export function money(amountMinor: number | null | undefined, currency: string | null | undefined): string {
  if (amountMinor == null || !currency) return dash;
  const digits = minorDigits(currency);
  try {
    return new Intl.NumberFormat(undefined, { style: "currency", currency, minimumFractionDigits: digits, maximumFractionDigits: digits }).format(amountMinor / 10 ** digits);
  } catch {
    return `${amountMinor / 10 ** digits} ${currency}`;
  }
}

export const rateSourceLabel: Record<string, string> = {
  usd: "already in US dollars",
  ecb: "ECB reference rate",
};

// An invoice from the USD-or-EUR columns, for a response without one.
function legacyInvoice(amount: number | null | undefined, currency: string | null | undefined): LinkOrderInvoice | null {
  if (amount == null || !currency) return null;
  const usd = currency === "USD";
  return {
    currency: currency as InvoiceCurrency,
    amount_minor: amount,
    invoice_date: null,
    usd_amount_minor: usd ? amount : null,
    fx_rate: usd ? 1 : null,
    fx_rate_date: null,
    fx_rate_source: usd ? "usd" : null,
  };
}

// The quote as on the invoice.
export function quotedOf(o: LinkOrder): LinkOrderInvoice | null {
  return o.quoted_invoice ?? legacyInvoice(o.quoted_amount_minor, o.quoted_currency);
}

// The invoice frozen at approval (refreshed at Mark Paid).
export function frozenOf(o: LinkOrder): LinkOrderInvoice | null {
  return o.invoice ?? legacyInvoice(o.donor_amount_minor, o.currency);
}

// The invoice behind an order: frozen at approval, else the quote.
export function invoiceOf(o: LinkOrder): LinkOrderInvoice | null {
  return frozenOf(o) ?? quotedOf(o);
}

// "£500.00 · $635.40": as on the invoice, then in US dollars. One amount
// when the invoice is in US dollars or no rate is known yet.
export function bothAmounts(inv: LinkOrderInvoice | null | undefined): string {
  if (!inv) return dash;
  const original = money(inv.amount_minor, inv.currency);
  if (inv.currency === "USD") return original;
  if (inv.usd_amount_minor == null) return original;
  return `${original} · ${money(inv.usd_amount_minor, "USD")}`;
}

// "1 GBP = 1.2708 USD, ECB reference rate on 12 Nov 2026".
export function rateLine(inv: LinkOrderInvoice | null | undefined): string {
  if (!inv || inv.currency === "USD") return "";
  if (inv.fx_rate == null) return "No ECB reference rate for the invoice date yet.";
  const label = rateSourceLabel[inv.fx_rate_source ?? ""] ?? "rate";
  return `1 ${inv.currency} = ${Number(inv.fx_rate.toPrecision(6))} USD, ${label} on ${day(inv.fx_rate_date)}`;
}

// Today as YYYY-MM-DD in UTC, the engine's default invoice date.
export function todayISO(): string {
  return new Date().toISOString().slice(0, 10);
}

export function moneyList(rows: LinkOrderMoney[] | null | undefined): string {
  if (!rows?.length) return dash;
  return rows.map((r) => money(r.amount_minor, r.currency)).join(" · ");
}

export function parseMajor(value: string, currency: string): number | null {
  const n = Number(value.trim().replace(",", "."));
  if (!Number.isFinite(n) || n < 0) return null;
  return Math.round(n * 10 ** minorDigits(currency));
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

// The donor price as on the invoice (frozen at approval, else quoted).
export function amountOf(o: LinkOrder): { amount: number | null; currency: string | null } {
  const inv = invoiceOf(o);
  if (inv) return { amount: inv.amount_minor, currency: inv.currency };
  return {
    amount: o.donor_amount_minor ?? o.quoted_amount_minor,
    currency: o.currency ?? o.quoted_currency,
  };
}

// The donor price as on the invoice and in US dollars, for lists.
export function priceOf(o: LinkOrder): string {
  const inv = invoiceOf(o);
  if (inv) return bothAmounts(inv);
  const { amount, currency } = amountOf(o);
  return money(amount, currency);
}

export const catalogExampleCSV = `domain,name,intermediary,theme,site_age,pbn,country,languages,niches,casino_allowed,rel_offered,slot_types,duration,tat_days,guarantee_days,domain_rating,traffic,price,currency,Date Added,Geo AhrefsTop 2,Article type,Main category,Subcategory,Organic Keywords,Total Keywords,Referring domains,Referring domains / Followed,Referring domains / Not followed,Referring IPs,Referring subnets,Backlinks,Backlinks / Followed,Backlinks / Not followed,Backlinks / Redirects,Backlinks / Internal,Outgoing domains / Followed,Outgoing domains / All time,Outgoing links / Followed,Outgoing links / All time
example.com,Example Review,Acme Links,Proxies,2012,false,US,EN,proxies,false,dofollow,guest_post,permanent,14,90,50,12500,850,USD,2026-01-15,GB,Guest post,Technology,Networking,3400,5100,1200,900,300,800,600,45000,30000,15000,120,2000,250,1100,400,3500
`;

export type LinkOrderDonorMetrics = components["schemas"]["LinkOrderDonorMetrics"];

/** The intermediary's metrics in the order the donor card shows them, with their labels. */
export const metricFields: { key: keyof LinkOrderDonorMetrics; label: string; group: string }[] = [
  { key: "date_added", label: "Date added", group: "Listing" },
  { key: "article_type", label: "Article type", group: "Listing" },
  { key: "main_category", label: "Main category", group: "Listing" },
  { key: "subcategory", label: "Subcategory", group: "Listing" },
  { key: "geo_top1", label: "Geo top 1", group: "Audience" },
  { key: "geo_top2", label: "Geo top 2", group: "Audience" },
  { key: "organic_keywords", label: "Organic keywords", group: "Keywords" },
  { key: "total_keywords", label: "Total keywords", group: "Keywords" },
  { key: "referring_domains", label: "Referring domains", group: "Referring domains" },
  { key: "referring_domains_followed", label: "Followed", group: "Referring domains" },
  { key: "referring_domains_not_followed", label: "Not followed", group: "Referring domains" },
  { key: "referring_ips", label: "Referring IPs", group: "Referring domains" },
  { key: "referring_subnets", label: "Referring subnets", group: "Referring domains" },
  { key: "backlinks", label: "Backlinks", group: "Backlinks" },
  { key: "backlinks_followed", label: "Followed", group: "Backlinks" },
  { key: "backlinks_not_followed", label: "Not followed", group: "Backlinks" },
  { key: "backlinks_redirects", label: "Redirects", group: "Backlinks" },
  { key: "backlinks_internal", label: "Internal", group: "Backlinks" },
  { key: "outgoing_domains_followed", label: "Outgoing domains, followed", group: "Outgoing" },
  { key: "outgoing_domains_all_time", label: "Outgoing domains, all time", group: "Outgoing" },
  { key: "outgoing_links_followed", label: "Outgoing links, followed", group: "Outgoing" },
  { key: "outgoing_links_all_time", label: "Outgoing links, all time", group: "Outgoing" },
];

/** "Since 2012 · 14 years", or a dash. */
export function siteAgeLabel(d: Pick<LinkOrderDonor, "site_since" | "site_age_years">): string {
  if (!d.site_since) return dash;
  const since = d.site_since.endsWith("-01-01") ? d.site_since.slice(0, 4) : day(`${d.site_since}T00:00:00Z`);
  const years = d.site_age_years ?? 0;
  return `Since ${since} · ${years < 1 ? "under a year" : `${years} year${years === 1 ? "" : "s"}`}`;
}

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
