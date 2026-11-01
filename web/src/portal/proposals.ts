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
};
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
  };
  accepted_at: string | null;
  created_at: string;
  updated_at: string;
  invitation_url?: string;
};
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
        link_attribute: "sponsored",
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
