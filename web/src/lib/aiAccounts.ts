// Shared sums and formats for AI Accounts' pages (pages/AIAccounts*.tsx).

import type { AIAccount } from "@outflare/sdk";

export const usd = (cents: number) =>
  (cents / 100).toLocaleString("en-US", { style: "currency", currency: "USD", maximumFractionDigits: Math.round(cents) % 100 ? 2 : 0 });

export const cycleWord: Record<string, string> = { monthly: "month", yearly: "year", prepaid: "top-up", usage: "usage" };

// Dates are calendar days (YYYY-MM-DD), read at noon so no time zone moves them.
export const dayDate = (d: string) => new Date(`${d}T12:00:00`);
export const shortDay = (d: string) => dayDate(d).toLocaleDateString(undefined, { month: "short", day: "numeric" });
export const daysUntil = (d: string) => Math.round((dayDate(d).getTime() - Date.now()) / 86_400_000);
export const isoDay = (t: Date) => `${t.getFullYear()}-${String(t.getMonth() + 1).padStart(2, "0")}-${String(t.getDate()).padStart(2, "0")}`;

// What an account costs per month, in cents: monthly plans as they are,
// yearly ones spread over twelve months. Prepaid and usage-billed
// accounts have no fixed monthly cost (null), nor do unknown prices.
export function monthlyCents(a: AIAccount): number | null {
  if (a.cancelled || a.price_cents == null) return null;
  if (a.billing_cycle === "monthly") return a.price_cents;
  if (a.billing_cycle === "yearly") return a.price_cents / 12;
  return null;
}

// Only company-paid accounts count in spend.
export const companyPaid = (a: AIAccount) => a.paid_by === "company" && !a.cancelled;

export const owners = (a: AIAccount) => a.members.filter((m) => m.role === "owner");

// The next renewals within days, soonest first.
export function renewingWithin(list: AIAccount[], days: number) {
  return list
    .filter((a) => !a.cancelled && a.renews_on && daysUntil(a.renews_on) >= 0 && daysUntil(a.renews_on) <= days)
    .sort((x, y) => x.renews_on!.localeCompare(y.renews_on!));
}

// The days an account renews between from and to (inclusive, YYYY-MM-DD),
// stepping its billing cycle back and forth from its next renewal day.
export function renewalsBetween(a: AIAccount, from: string, to: string): string[] {
  if (a.cancelled || !a.renews_on) return [];
  const months = a.billing_cycle === "monthly" ? 1 : a.billing_cycle === "yearly" ? 12 : 0;
  const start = dayDate(a.renews_on);
  if (months === 0) return a.renews_on >= from && a.renews_on <= to ? [a.renews_on] : [];
  const out: string[] = [];
  for (let k = -24; k <= 24; k++) {
    // The same day of the month, or the month's last day when it is shorter
    // (a plan bought on the 31st renews on 30 April).
    const m = start.getMonth() + k * months;
    const last = new Date(start.getFullYear(), m + 1, 0).getDate();
    const s = isoDay(new Date(start.getFullYear(), m, Math.min(start.getDate(), last), 12));
    if (s >= from && s <= to) out.push(s);
  }
  return out;
}
