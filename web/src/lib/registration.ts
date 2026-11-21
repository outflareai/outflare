// When a domain's registration runs out, from its public registration
// record: quiet while it is far off, amber within 30 days, red within 7
// days and once it has run out, when the links on it stop working.
export function registrationNote(expiresAt: string | null | undefined, now: number = Date.now()): { label: string; tone: "red" | "amber" | "grey" } | null {
  if (!expiresAt) return null;
  const at = Date.parse(expiresAt);
  if (Number.isNaN(at)) return null;
  const day = new Date(at).toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric", timeZone: "UTC" });
  const days = Math.ceil((at - now) / 86_400_000);
  if (at <= now) return { label: `Registration expired ${day}`, tone: "red" };
  if (days <= 7) return { label: `Registration expires ${days === 1 ? "tomorrow" : `in ${days} days`}`, tone: "red" };
  if (days <= 30) return { label: `Registration expires in ${days} days`, tone: "amber" };
  return { label: `Registered until ${day}`, tone: "grey" };
}
