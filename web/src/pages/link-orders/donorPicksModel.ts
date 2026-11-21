import { lowestPrice, pricePerThousandVisits } from "./catalogFilters";
import { pbnSignals } from "./pbnSignals";
import type { LinkOrder, LinkOrderCandidate, LinkOrderDonor } from "./shared";

// Recommended donors for one link target: the catalog ranked by what a
// person would weigh, with the reasons shown. It only reads what the page
// already loaded (the catalog, the brand's orders and the saved competitor
// gap), never calls a paid provider, and never orders anything: picks go
// into the request like a ticked row.

export type DonorPick = {
  donor: LinkOrderDonor;
  /** The page it is picked for, when picks cover several pages. */
  forPage?: string;
  /** 0 to 100. */
  score: number;
  reasons: string[];
  warnings: string[];
};

export type PickTarget = {
  url: string;
  /** Competitors that rank for the target's keyword or have links to it (from the target suggestion). */
  competitors: string[];
};

const languageByCode: Record<string, string> = {
  en: "English", es: "Spanish", de: "German", fr: "French", it: "Italian", pt: "Portuguese", br: "Portuguese", ru: "Russian",
  uk: "Ukrainian", ua: "Ukrainian", pl: "Polish", nl: "Dutch", ja: "Japanese", jp: "Japanese", zh: "Chinese", cn: "Chinese", ko: "Korean",
  kr: "Korean", tr: "Turkish", id: "Indonesian", ms: "Malay", vi: "Vietnamese", th: "Thai", ar: "Arabic", he: "Hebrew", hi: "Hindi",
  sv: "Swedish", da: "Danish", no: "Norwegian", fi: "Finnish", cs: "Czech", sk: "Slovak", ro: "Romanian", hu: "Hungarian", el: "Greek",
  bg: "Bulgarian", hr: "Croatian", sr: "Serbian", lt: "Lithuanian", lv: "Latvian", et: "Estonian",
};

/** The language a target page is in, from a locale path segment (/fr/, /pt-br/); English when there is none. */
export function targetLanguage(url: string): string {
  let path = "";
  try {
    path = new URL(url).pathname;
  } catch {
    return "English";
  }
  const first = path.split("/").filter(Boolean)[0]?.toLowerCase() ?? "";
  const code = /^[a-z]{2}(?:[-_][a-z]{2})?$/.test(first) ? first.slice(0, 2) : "";
  return (code && languageByCode[code]) || "English";
}

function host(s: string): string {
  return s.toLowerCase().replace(/^https?:\/\//, "").replace(/^www\./, "").split("/")[0];
}

function topicsOf(d: LinkOrderDonor): string[] {
  const m = d.metrics;
  return [m?.main_category, m?.subcategory, d.theme, ...d.niches].filter((x): x is string => !!x);
}

const gambling = /gambl|casino|betting|poker|lotter/i;
const adult = /adult|dating|escort/i;

/**
 * The topics the brand's links tend to come from: the main categories of
 * donors it already ordered from and of catalog sites that link to its
 * competitors, most common first. A starting guess the person edits.
 */
export function guessTopics(donors: LinkOrderDonor[], orders: LinkOrder[], candidates: LinkOrderCandidate[], max = 6): string[] {
  const byId = new Map(donors.map((d) => [d.id, d]));
  const counts = new Map<string, number>();
  const add = (d: LinkOrderDonor | undefined, w: number) => {
    const c = d?.metrics?.main_category;
    if (c && !gambling.test(c) && !adult.test(c)) counts.set(c, (counts.get(c) ?? 0) + w);
  };
  for (const o of orders) if (o.status !== "lost") add(byId.get(o.donor_profile_id), 2);
  for (const c of candidates) add(c.donor_profile_id ? byId.get(c.donor_profile_id) : undefined, 1);
  return [...counts.entries()].sort((a, b) => b[1] - a[1]).slice(0, max).map(([t]) => t);
}

/** Every main category in the catalog, most common first, for the topic picker. */
export function catalogTopics(donors: LinkOrderDonor[]): { topic: string; n: number }[] {
  const counts = new Map<string, number>();
  for (const d of donors) {
    const c = d.metrics?.main_category;
    if (c) counts.set(c, (counts.get(c) ?? 0) + 1);
  }
  return [...counts.entries()].map(([topic, n]) => ({ topic, n })).sort((a, b) => b.n - a.n || a.topic.localeCompare(b.topic));
}

/**
 * autoPicks recommends donors without a page chosen: for each of the top
 * suggested link targets (pages SE Ranking has near the top first), its
 * best few donors, a donor used for one page only once.
 */
export function autoPicks(opts: {
  donors: LinkOrderDonor[];
  orders: LinkOrder[];
  candidates: LinkOrderCandidate[];
  pages: PickTarget[];
  topics: string[];
  perPage?: number;
  limit?: number;
}): DonorPick[] {
  const per = opts.perPage ?? 3;
  const taken = new Set<string>();
  const out: DonorPick[] = [];
  for (const page of opts.pages) {
    const picks = recommendDonors({ donors: opts.donors, orders: opts.orders, candidates: opts.candidates, target: page, topics: opts.topics, limit: per + taken.size });
    let n = 0;
    for (const p of picks) {
      if (n >= per || taken.has(p.donor.id)) continue;
      taken.add(p.donor.id);
      out.push({ ...p, forPage: page.url });
      n++;
    }
    if (out.length >= (opts.limit ?? 15)) break;
  }
  return out.slice(0, opts.limit ?? 15);
}

export function recommendDonors(opts: {
  donors: LinkOrderDonor[];
  orders: LinkOrder[];
  candidates: LinkOrderCandidate[];
  target: PickTarget;
  topics: string[];
  limit?: number;
}): DonorPick[] {
  const { donors, orders, candidates, target, topics } = opts;
  const lang = targetLanguage(target.url);
  const targetURL = target.url.trim().replace(/\/+$/, "").toLowerCase();
  const wantTopics = topics.map((t) => t.toLowerCase());
  const topicWords = new Set(wantTopics.flatMap((t) => t.split(/[^\p{L}\p{N}]+/u)).filter((w) => w.length > 3));
  const riskyTopicWanted = topics.some((t) => gambling.test(t) || adult.test(t));
  const targetCompetitors = new Set(target.competitors.map(host));

  // What the brand already did with each donor.
  const orderedForTarget = new Set<string>();
  const usedBefore = new Map<string, number>();
  for (const o of orders) {
    if (o.status === "lost") continue;
    usedBefore.set(o.donor_profile_id, (usedBefore.get(o.donor_profile_id) ?? 0) + 1);
    if (targetURL && o.target_url.trim().replace(/\/+$/, "").toLowerCase() === targetURL) orderedForTarget.add(o.donor_profile_id);
  }
  // Catalog sites that link to competitors (the saved competitor gap).
  const gapByDonor = new Map<string, string[]>();
  for (const c of candidates) if (c.donor_profile_id) gapByDonor.set(c.donor_profile_id, c.competitors.map(host));

  const picks: DonorPick[] = [];
  for (const d of donors) {
    // Flagged PBNs, and sites whose metrics look like one, are never picked.
    // Nor are sites the team's rules say to skip: traffic on the home page
    // or one to three pages, or more links out than a publication gives.
    if (d.pbn || orderedForTarget.has(d.id) || pbnSignals(d).likely || d.quality?.verdict === "skip") continue;
    const price = lowestPrice(d);
    if (price == null) continue;
    // The base minimum, also when the site was never assessed: DR 30 and
    // 5,000 organic visits a month.
    if ((d.domain_rating ?? 0) < 30 || (d.traffic ?? 0) < 5000) continue;
    const dTopics = topicsOf(d);
    const risky = dTopics.some((t) => gambling.test(t) || adult.test(t));
    if (risky && !riskyTopicWanted) continue;

    const reasons: string[] = [];
    const warnings: string[] = [];
    let score = 0;

    // Topic: 0-25. An exact category match counts most; a shared word in
    // the subcategory, theme or niches counts some.
    const lower = dTopics.map((t) => t.toLowerCase());
    const exact = wantTopics.length ? lower.find((t) => wantTopics.includes(t)) : undefined;
    const wordHit = !exact && topicWords.size ? lower.find((t) => t.split(/[^\p{L}\p{N}]+/u).some((w) => topicWords.has(w))) : undefined;
    if (exact) {
      score += 25;
      reasons.push(`Topic: ${dTopics[lower.indexOf(exact)]}`);
    } else if (wordHit) {
      score += 12;
      reasons.push(`Related topic: ${dTopics[lower.indexOf(wordHit)]}`);
    } else if (wantTopics.length) {
      warnings.push("Off-topic for the chosen topics");
    }

    // Language and audience: 0-10.
    if (d.languages.includes(lang)) {
      score += 10;
      reasons.push(`${lang} site`);
    } else if (d.languages.length) {
      warnings.push(`${d.languages.join(", ")}, page is ${lang}`);
    }

    // Authority: 0-25 from DR (70+ is full marks).
    const dr = d.domain_rating ?? 0;
    score += (Math.min(dr, 70) / 70) * 25;
    if (dr >= 50) reasons.push(`DR ${dr}`);

    // Real organic traffic: 0-15 on a log scale (100k+ is full marks).
    const traffic = d.traffic ?? 0;
    if (traffic > 0) score += Math.min(Math.log10(traffic) / 5, 1) * 15;
    if (traffic >= 10000) reasons.push(`${traffic.toLocaleString()} visits/mo`);
    if (traffic < 500) warnings.push("Little organic traffic");

    // Value for money: 0-15, full marks at $5 or less per 1,000 visits.
    const value = pricePerThousandVisits(d);
    if (value != null) {
      score += value <= 5 ? 15 : value >= 100 ? 0 : 15 * (1 - Math.log(value / 5) / Math.log(20));
      if (value <= 10) reasons.push(`Good value: ${value < 10 ? value.toFixed(1) : Math.round(value)} per 1k visits`);
      if (value >= 100) warnings.push("Expensive for its traffic");
    }

    // Links to competitors that rank for this page: +10; to other competitors: +5.
    const gap = gapByDonor.get(d.id);
    if (gap?.length) {
      const forTarget = gap.filter((c) => targetCompetitors.has(c));
      if (forTarget.length) {
        score += 10;
        reasons.push(`Links to ${forTarget.slice(0, 2).join(", ")}`);
      } else {
        score += 5;
        reasons.push(`Links to competitors (${gap.slice(0, 2).join(", ")})`);
      }
    }

    // A link farm sells to everyone: penalise many followed outgoing domains.
    const outgoing = d.metrics?.outgoing_domains_followed;
    if (typeof outgoing === "number" && outgoing > 1000) {
      score -= outgoing > 5000 ? 20 : 10;
      warnings.push(`${outgoing.toLocaleString()} outgoing domains`);
    }

    // Already a donor for this brand: a second link from it is worth less.
    const used = usedBefore.get(d.id);
    if (used) {
      score -= 10;
      warnings.push(`Already ordered ${used === 1 ? "once" : `${used} times`} for this brand`);
    }
    if (d.quality?.verdict === "check") {
      score -= 10;
      warnings.push(d.quality.reasons[0] ?? "Traffic leans on one page");
    }
    if (d.quality?.grade === "premium") {
      score += 5;
      reasons.push(d.quality.rd_ld != null ? `RD/LD ${d.quality.rd_ld} (premium)` : "Premium link profile");
    }
    if (d.direct_contact.recommended) reasons.push("Direct contact");
    // A site the team labelled Best link comes first among equals.
    if (d.best_link) {
      score += 10;
      reasons.unshift("Best link");
    }

    picks.push({ donor: d, score: Math.max(0, Math.min(100, Math.round(score))), reasons, warnings });
  }
  return picks.sort((a, b) => b.score - a.score || (lowestPrice(a.donor) ?? 0) - (lowestPrice(b.donor) ?? 0)).slice(0, opts.limit ?? 10);
}
