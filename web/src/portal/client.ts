export type CouponRules = {
  disabled?: boolean;
  starts_at?: string | null;
  ends_at?: string | null;
  percent_bp?: number;
  amount_cents?: number;
  currency?: string;
  max_redemptions?: number;
  first_order_only?: boolean;
  min_order_cents?: number;
  duration?: string;
  months?: number;
};
export type Program = {
  coupon_templates: {
    id: string;
    label: string;
    coupon: string;
    rules: CouponRules;
  }[];
  home_url: string;
  support_url: string;
  support_email: string;
  header_links: { label: string; url: string }[];
  footer_links: { label: string; url: string }[];
  landing_pages: { label: string; url: string }[];
  help_articles: {
    id: string;
    title: string;
    category: string;
    body: string;
  }[];
  payout_terms: string;
  terms_version: string;
  title: string;
  name: string;
  kind: string;
  color: string;
  logo_url: string;
  favicon_url: string;
  custom_css: string;
  welcome: string;
  pages: string[];
  signup_open: boolean;
  signup_fields: {
    key: string;
    label: string;
    type: string;
    required: boolean;
    options: string[];
  }[];
  terms_url: string;
  currency: string;
  powered_by: boolean;
};
export type Me = {
  promo_code_details: {
    code: string;
    coupon: string;
    status: string;
    error: string;
    rules: CouponRules;
  }[];
  requires_terms: boolean;
  accepted_terms_version: string;
  name: string;
  email: string;
  status: string;
  read_only: boolean;
  link: string;
  campaign_link: string;
  promo_codes: string[];
  currency: string;
  payout_method: string;
  payout_type: string | null;
  payout_token: string | null;
  payout_methods: string[];
  payout_tokens: string[];
  postback_url: string;
  notify: { conversions?: boolean; payouts?: boolean };
  min_payout_cents: number;
  hold_days: number;
  program: Program;
  stats: {
    balance_cents: number;
    paid_cents: number;
    pending_cents: number;
    approved_cents: number;
    clicks: number;
    referrals: number;
    customers: number;
  };
};
export type Config = {
  title: string;
  description: string;
  category: string;
  reward_kind: string;
  pricing: string;
  amount_cents: number;
  max_amount_cents: number;
  reward_label: string;
  claim_mode: string;
  verification: string;
  hold_days: number;
  asset_kind: string;
  min_reach: number;
  target_host: string;
  slots: number;
};
export type Task = { id: string; status: string; config: Config };
export type Submission = {
  id: string;
  task_id: string;
  audience_id: string | null;
  status: string;
  amount_cents: number;
  evidence_url: string;
  note: string;
  check_status: string;
  hold_until: string | null;
};
export type Audience = {
  id: string;
  url: string;
  host: string;
  kind: string;
  role: string;
  topics: string;
  reach: number;
  reach_source: string;
  measured_at: string | null;
  verified_at: string | null;
  challenge: string;
  dns_name: string;
  last_error: string;
};
export type Tasks = { data: Task[]; submissions: Submission[] };
export class PortalError extends Error {
  constructor(
    message: string,
    public status: number,
    public code: string,
  ) {
    super(message);
  }
}
const declared = document.documentElement.dataset.portalBase;
export const base =
  declared && !declared.includes("{{")
    ? declared
    : (/^\/p\/[^/]+\//.exec(location.pathname)?.[0] ?? "/");
const embedded = new URLSearchParams(location.search).get("view") === "portal";
let bearer = embedded
  ? (/(?:^#|&)token=(ofps_[A-Za-z0-9_-]+)/.exec(location.hash)?.[1] ?? "")
  : "";
export const isEmbedded = embedded;
export const hasEmbeddedSession = () => !!bearer;
if (bearer)
  history.replaceState(
    null,
    "",
    location.pathname + location.search + "#tasks",
  );
export const invitationKey = "outflare-partner-website:" + base;
const invitation = new URLSearchParams(location.search).get("website");
if (invitation) {
  try {
    sessionStorage.setItem(invitationKey, invitation);
  } catch {
    /* Storage is optional in an iframe. */
  }
}
export function invitedWebsite() {
  try {
    return sessionStorage.getItem(invitationKey) || "";
  } catch {
    return "";
  }
}
export async function request<T>(
  path: string,
  method = "GET",
  body?: unknown,
): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (bearer) headers.Authorization = "Bearer " + bearer;
  const response = await fetch(base + "api/" + path, {
    method,
    credentials: bearer ? "omit" : "same-origin",
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const result = await response.json().catch(() => ({}));
  if (!response.ok)
    throw new PortalError(
      result.error?.message ||
        "We couldn't complete that request. Please try again.",
      response.status,
      result.error?.code || "unknown",
    );
  return result as T;
}
export async function signOut() {
  await request("sign-out", "POST");
  bearer = "";
}
export function money(cents: number, currency: string) {
  return new Intl.NumberFormat(undefined, {
    style: "currency",
    currency,
    maximumFractionDigits: cents % 100 === 0 ? 0 : 2,
  }).format(cents / 100);
}
export function reward(c: Config, currency: string) {
  return c.reward_kind === "free"
    ? "Voluntary"
    : c.reward_kind === "action"
      ? c.reward_label
      : c.pricing === "custom"
        ? "Custom offer"
        : c.pricing === "range"
          ? `${money(c.amount_cents, currency)}–${money(c.max_amount_cents, currency)}`
          : money(c.amount_cents, currency);
}
export function matches(c: Config, a: Audience) {
  return (
    (!c.asset_kind || c.asset_kind === a.kind) &&
    a.reach >= c.min_reach &&
    (!c.target_host || (a.host === c.target_host && !!a.verified_at))
  );
}
export function needsAsset(c: Config) {
  return !!(c.asset_kind || c.min_reach || c.target_host);
}
export const names: Record<string, string> = {
  website: "Website",
  youtube: "YouTube",
  x: "X",
  linkedin: "LinkedIn",
  instagram: "Instagram",
  tiktok: "TikTok",
  newsletter: "Newsletter",
  reddit: "Reddit",
  facebook: "Facebook",
  discord: "Discord",
  podcast: "Podcast",
  other: "Other",
  blog: "Article",
  video: "Video",
  social: "Social",
  banner: "Website placement",
  community: "Community",
  applied: "Application sent",
  offered: "Offer ready",
  accepted: "In progress",
  submitted: "In review",
  approved: "Approved",
  pending: "On hold",
  paid: "Paid",
  fulfilled: "Delivered",
  due: "Awaiting payout",
  canceled: "Canceled",
  rejected: "Not approved",
  human_required: "Needs review",
  held: "Review hold",
  passed: "Link verified",
};
