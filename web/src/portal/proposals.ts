export type Deliverable = {
  id: string;
  title: string;
  channel: string;
  format: string;
  placement_url: string;
  requirements: string;
  deadline: string;
  live_days: number;
  link_url: string;
  link_text: string;
  link_attribute: string;
};
export type Milestone = {
  id: string;
  title: string;
  deliverable_id: string;
  amount_cents: number;
  benefit: string;
  wait_days: number;
};
export type ProposalConfig = {
  partner_site_name?: string;
  partner_site_url?: string;
  partner_site_icon_url?: string;
  title: string;
  brief: string;
  request_quote: boolean;
  reply_to: string;
  brand_kit_url: string;
  product_url: string;
  terms: string;
  commission_terms: string;
  deliverables: Deliverable[];
  milestones: Milestone[];
  // Set by the server when the offer is sent: the brand's rule for a paid
  // link removed after payment.
  removal?: LinkRule;
};
export type LinkRule = {
  removal:
    | "restore_or_refund"
    | "restore_or_refund_forever"
    | "shrinking_refund"
    | "warn_only";
  restore_days: number;
  guarantee_months: number;
};
// The rule in one plain sentence, as the site owner reads it.
export function removalText(r: LinkRule) {
  const months =
    r.guarantee_months === 1 ? "month" : `${r.guarantee_months} months`;
  switch (r.removal) {
    case "restore_or_refund":
      return `If the link is removed in the first ${months}, it is restored within ${r.restore_days} days or the payment is refunded.`;
    case "restore_or_refund_forever":
      return `If the link is ever removed, it is restored within ${r.restore_days} days or the payment is refunded.`;
    case "shrinking_refund":
      return `If the link is removed and not restored within ${r.restore_days} days, part of the payment is refunded: all of it at first, falling evenly to nothing after ${months}.`;
    default:
      return "If the link is removed, the brand is told; no refund is asked for.";
  }
}
export type Proposal = {
  id: string;
  membership_id: string;
  record_id: string;
  recipient: string;
  status: string;
  version: number;
  config: ProposalConfig;
  progress: {
    deliverables: Record<
      string,
      {
        url: string;
        note: string;
        submitted_at: string | null;
        verified_at: string | null;
      }
    >;
    milestones: Record<
      string,
      { approved_at: string | null; note: string; reward_ids: string[] }
    >;
    invoice_note: string;
    quoted_cents: number;
    quote_note: string;
    counters?: Counter[];
  };
  accepted_at: string | null;
  created_at: string;
  updated_at: string;
  invitation_url?: string;
};
// What a recipient may change on one deliverable in a counter-offer.
export type CounterChange = {
  placement_url?: string;
  link_text?: string;
  deadline?: string;
};
export type Counter = {
  round: number;
  amount_cents: number;
  changes: Record<string, CounterChange>;
  note: string;
  at: string;
  accepted_at?: string | null;
};
// A recipient may send at most this many counter-offers on one proposal.
export const maxCounterRounds = 3;
export function offerTotal(c: ProposalConfig) {
  return c.milestones.reduce((t, m) => t + (m.amount_cents || 0), 0);
}
// The counter-offer waiting for the brand, if there is one.
export function pendingCounter(p: Proposal) {
  const all = p.progress.counters || [];
  const last = all[all.length - 1];
  return p.status === "quote_received" && last && !last.accepted_at
    ? last
    : null;
}

export function blankProposal(): ProposalConfig {
  return {
    title: "",
    brief: "",
    request_quote: false,
    reply_to: "",
    brand_kit_url: "",
    product_url: "",
    terms: "",
    commission_terms: "",
    deliverables: [
      {
        id: "deliverable_1",
        title: "",
        channel: "",
        format: "",
        placement_url: "",
        requirements: "",
        deadline: "",
        live_days: 0,
        link_url: "",
        link_text: "",
        link_attribute: "follow",
      },
    ],
    milestones: [
      {
        id: "milestone_1",
        title: "Publication approved",
        deliverable_id: "deliverable_1",
        amount_cents: 0,
        benefit: "",
        wait_days: 0,
      },
    ],
  };
}
export function milestoneDue(p: Proposal, m: Milestone) {
  const verified = p.progress.deliverables?.[m.deliverable_id]?.verified_at;
  return verified
    ? new Date(new Date(verified).getTime() + m.wait_days * 86400000)
    : null;
}

export function proposalMoney(cents: number, currency: string) {
  return new Intl.NumberFormat(undefined, {
    style: "currency",
    currency,
    maximumFractionDigits: cents % 100 === 0 ? 0 : 2,
  }).format(cents / 100);
}

export function proposalStage(p: Proposal) {
  if (p.status !== "accepted") return p.status.replaceAll("_", " ");
  const work = Object.values(p.progress.deliverables || {});
  return work.some((d) => d.verified_at)
    ? "Live"
    : work.some((d) => d.submitted_at)
      ? "In review"
      : "Awaiting publication";
}
