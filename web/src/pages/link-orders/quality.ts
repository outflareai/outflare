import type { LinkOrderDonor } from "./shared";

// The server's verdict on a catalog site (Assess in the engine): where its
// search traffic sits and how it links out. These helpers only word it.

export type Verdict = LinkOrderDonor["quality"]["verdict"];

const pct = (v: number | null | undefined) => (v == null ? "—" : `${Math.round(v * 100)}%`);

/** The numbers behind the verdict, in one line. */
export function qualityLine(d: Pick<LinkOrderDonor, "quality">): string {
  const q = d.quality;
  const parts: string[] = [];
  if (q.top_page_share != null) parts.push(`Top page ${pct(q.top_page_share)}`);
  if (q.home_share != null && q.home_share > 0) parts.push(`home ${pct(q.home_share)}`);
  if (q.top3_share != null) parts.push(`top 3 ${pct(q.top3_share)}`);
  if (q.rd_ld != null) parts.push(`RD/LD ${q.rd_ld}${q.grade ? ` (${gradeLabel[q.grade]})` : ""}`);
  return parts.join(" · ");
}

export const gradeLabel: Record<LinkOrderDonor["quality"]["grade"], string> = {
  "": "",
  link_farm: "link farm",
  weak: "weak",
  normal: "normal",
  premium: "premium",
};

export const verdictLabel: Record<Verdict, string> = { skip: "Skip", check: "Check", ok: "OK", unknown: "Not checked" };
export const verdictTone: Record<Verdict, "red" | "amber" | "green" | "grey"> = { skip: "red", check: "amber", ok: "green", unknown: "grey" };

/** How old an Ahrefs check may be and still give a green light. */
export const GREEN_LIGHT_DAYS = 30;

export type Light = {
  tone: "green" | "amber" | "red" | "grey";
  /** One or two words for the badge. */
  label: string;
  /** Why it is not green, in plain words; empty when green. */
  why: string[];
};

/**
 * greenLight is the go / no-go for buying a link on a site: green only when
 * its verdict is OK and Ahrefs checked it in the last GREEN_LIGHT_DAYS days,
 * so where its traffic sits is known and current. Red for a site to skip;
 * amber for one close to the limit or with an old check; grey when it was
 * never checked.
 */
export function greenLight(d: Pick<LinkOrderDonor, "quality" | "ahrefs_check">, now = Date.now()): Light {
  const v = d.quality?.verdict ?? "unknown";
  const check = d.ahrefs_check;
  const checked = check?.status === "checked";
  const ageDays = checked ? (now - Date.parse(check.at)) / 86_400_000 : Infinity;
  if (v === "skip") return { tone: "red", label: "Skip", why: d.quality.reasons.length ? d.quality.reasons : ["Below the base minimum"] };
  if (!checked && v === "unknown") return { tone: "grey", label: "Not checked", why: ["Not checked in Ahrefs yet: where its traffic sits is unknown"] };
  const why: string[] = [];
  if (v === "check") why.push(...(d.quality.reasons.length ? d.quality.reasons : ["Traffic leans on one page"]));
  if (v === "unknown") why.push("No verdict yet");
  if (!checked) why.push("Not checked in Ahrefs yet");
  else if (ageDays > GREEN_LIGHT_DAYS) why.push(`Ahrefs check is ${Math.floor(ageDays)} days old: check again`);
  if (!why.length) return { tone: "green", label: "Green light", why: [] };
  return { tone: "amber", label: v === "check" ? "Check" : "Re-check", why };
}

/**
 * needsAhrefsCheck is true when the site has no successful Ahrefs check in
 * the last GREEN_LIGHT_DAYS days. A fresher check is used as it is: the
 * server doesn't check such a site again either.
 */
export function needsAhrefsCheck(d: Pick<LinkOrderDonor, "ahrefs_check">, now = Date.now()): boolean {
  const c = d.ahrefs_check;
  return !(c?.status === "checked" && (now - Date.parse(c.at)) / 86_400_000 <= GREEN_LIGHT_DAYS);
}

/** The share of search traffic on the site's top page, as a short label. */
export function topPageLabel(d: Pick<LinkOrderDonor, "quality">): string {
  return d.quality?.top_page_share == null ? "Top page —" : `Top page ${pct(d.quality.top_page_share)}`;
}
