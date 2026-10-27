// Dates and times, written the way people say them: "2h ago", "Today 9:14 am",
// "Mon 26 Oct, 9:00 am". Formats follow the browser's locale.

const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });

/** "just now", "5 min ago", "3 h ago", then a date. For things that happened. */
export function ago(iso: string | null | undefined): string {
  if (!iso) return "never";
  const d = new Date(iso);
  const s = (Date.now() - d.getTime()) / 1000;
  if (s < 45) return "just now";
  if (s < 3600) return rtf.format(-Math.round(s / 60), "minute");
  if (s < 86400) return rtf.format(-Math.round(s / 3600), "hour");
  if (s < 7 * 86400) return rtf.format(-Math.round(s / 86400), "day");
  return d.toLocaleDateString(undefined, { day: "numeric", month: "short", year: d.getFullYear() === new Date().getFullYear() ? undefined : "numeric" });
}

/** A short date and time, optionally in another time zone, which is named. */
export function when(iso: string, timeZone?: string): string {
  const d = new Date(iso);
  const opts: Intl.DateTimeFormatOptions = { weekday: "short", day: "numeric", month: "short", hour: "numeric", minute: "2-digit", timeZone };
  const text = d.toLocaleString(undefined, opts);
  if (!timeZone || timeZone === Intl.DateTimeFormat().resolvedOptions().timeZone) return text;
  return `${text} (${timeZone.split("/").pop()!.replaceAll("_", " ")} time)`;
}

/** Full date and time, for logs. */
export function stamp(iso: string): string {
  return new Date(iso).toLocaleString(undefined, { day: "numeric", month: "short", year: "numeric", hour: "numeric", minute: "2-digit", second: "2-digit" });
}

/** For a message list: time today, weekday this week, else the date. */
export function listTime(iso: string): string {
  const d = new Date(iso), now = new Date();
  if (d.toDateString() === now.toDateString()) return d.toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" });
  if (now.getTime() - d.getTime() < 6 * 86400000) return d.toLocaleDateString(undefined, { weekday: "short" });
  return d.toLocaleDateString(undefined, { day: "numeric", month: "short" });
}

export const plural = (n: number, one: string, many = `${one}s`) => `${n.toLocaleString()} ${n === 1 ? one : many}`;

/** Tone for an inbox placement rate, matching the domain advice: amber under 90%, red under 75%. */
export function placementTone(rate: number | null | undefined): "red" | "amber" | "green" | undefined {
  if (rate === null || rate === undefined) return undefined;
  return rate < 0.75 ? "red" : rate < 0.9 ? "amber" : "green";
}

/** A connection's name without the account in brackets: "EmailBison (ops@acme.test)" is "EmailBison". */
export function shortPlatform(name: string | null | undefined): string {
  return (name ?? "").replace(/\s*\([^)]*\)$/, "");
}

// The reply rate, the same everywhere: leads who replied over leads
// emailed. Nothing when it would mislead (under 10 leads, or more replies
// than leads).
export function replyRate(repliedLeads: number, contactedLeads: number): string | undefined {
  if (contactedLeads < 10 || repliedLeads > contactedLeads) return undefined;
  return `${Math.round((repliedLeads / contactedLeads) * 1000) / 10}%`;
}
