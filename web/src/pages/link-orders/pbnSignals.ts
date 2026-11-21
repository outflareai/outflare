import type { LinkOrderDonor } from "./shared";

// Signs that a catalog site may be a PBN (a site kept for selling links),
// read from the metrics already stored on it. A suggestion only: the PBN
// flag stays a person's decision, and nothing is flagged on its own.
//
// The core sign is authority without an audience: DR 30 or more with
// almost no organic traffic, or with very few ranking keywords (traffic
// that few keywords cannot explain is a second warning). Signs of a link
// network add to it: few referring subnets for its referring domains,
// thousands of followed outgoing domains, or backlinks that are mostly
// sitewide. A big news site that links out a lot is not "likely" without
// a core sign. Calibrated on an 11,000-site broker export (about 5% likely).

export type PBNSignals = {
  /** 0 when nothing points at a PBN. */
  score: number;
  likely: boolean;
  reasons: string[];
};

const LIKELY_AT = 3;

function num(v: unknown): number | null {
  return typeof v === "number" && Number.isFinite(v) ? v : null;
}

export function pbnSignals(d: Pick<LinkOrderDonor, "domain_rating" | "traffic" | "metrics">): PBNSignals {
  const m = d.metrics ?? ({} as LinkOrderDonor["metrics"]);
  const dr = d.domain_rating ?? 0;
  const traffic = d.traffic ?? 0;
  const keywords = num(m.total_keywords) ?? num(m.organic_keywords);
  const rd = num(m.referring_domains) ?? 0;
  const subnets = num(m.referring_subnets);
  const backlinks = num(m.backlinks);
  const outgoing = num(m.outgoing_domains_followed) ?? 0;
  const reasons: string[] = [];
  let score = 0;
  let core = false;

  if (dr >= 30 && traffic < 1000) {
    score += 2;
    core = true;
    reasons.push(`DR ${dr} with ${traffic.toLocaleString()} organic visits`);
  }
  if (dr >= 30 && keywords != null && keywords < 100) {
    score += 2;
    core = true;
    reasons.push(`DR ${dr} but ranks for ${keywords} keywords`);
    if (traffic >= 5000) {
      score += 1;
      reasons.push(`${traffic.toLocaleString()} visits from ${keywords} keywords`);
    }
  }
  if (rd >= 50 && subnets != null && subnets / rd < 0.25) {
    score += 1;
    reasons.push(`${rd.toLocaleString()} referring domains from ${subnets.toLocaleString()} subnets`);
  }
  if (outgoing > 10000) {
    score += 2;
    reasons.push(`${outgoing.toLocaleString()} followed outgoing domains`);
  } else if (outgoing > 2000) {
    score += 1;
    reasons.push(`${outgoing.toLocaleString()} followed outgoing domains`);
  }
  if (rd > 0 && backlinks != null && backlinks / rd > 200) {
    score += 1;
    reasons.push(`${Math.round(backlinks / rd).toLocaleString()} backlinks per referring domain`);
  }
  return { score, likely: core && score >= LIKELY_AT, reasons };
}
