// WontBeEmailed is the badge on a lead that is on the do not contact list,
// and it says why: the reason, whether it is the address or its whole
// domain, where the entry came from and when.

import { Badge } from "@/ui";
import { ago } from "@/format";

type Why = { kind: "email" | "domain"; value: string; reason: string; source: string; created_at: string } | null | undefined;

const reasons: Record<string, string> = {
  unsubscribed: "Unsubscribed",
  bounced: "Bounced",
  complained: "Marked as spam",
  manual: "Do not contact",
  do_not_contact: "Do not contact",
  competitor: "Competitor",
  customer: "Customer",
  partner: "Partner",
  legal: "Legal block",
  blocklist: "On a blocklist",
};

const details: Record<string, string> = {
  unsubscribed: "They unsubscribed, so cold and marketing email skip them.",
  bounced: "An email to this address hard bounced, so it is never emailed again.",
  complained: "They marked an email as spam, so they are never emailed again.",
  manual: "Someone on your team added them to the do not contact list.",
  do_not_contact: "They asked not to be contacted.",
  competitor: "Marked as a competitor, so cold and marketing email skip them.",
  customer: "Marked as a customer, so cold email skips them.",
  partner: "Marked as a partner, so cold email skips them.",
  legal: "Blocked for a legal reason.",
  blocklist: "On the blocklist of a connected sequencer, so cold email skips them.",
};

// Where an entry came from, in words: "emailbison_sync" is EmailBison.
function sourceText(source: string) {
  const known: Record<string, string> = { manual: "added by hand", api: "added through the API", mcp: "added by an agent", reply: "from a reply", smtp: "from a bounce", unsubscribe_link: "from the unsubscribe link", email_provider: "reported by the email provider", bounce_report: "from a bounce report" };
  if (known[source]) return known[source];
  const m = /^(.+)_(sync|import)$/.exec(source);
  if (m) return `${m[2] === "sync" ? "synced" : "imported"} from ${m[1].replace(/_/g, " ").replace(/^\w/, (c) => c.toUpperCase())}`;
  return source ? `from ${source.replace(/_/g, " ")}` : "";
}

export function WontBeEmailed({ why }: { why: Why }) {
  if (!why) return <Badge tone="red" title="On the do not contact list: never emailed">Won't be emailed</Badge>;
  const label = reasons[why.reason] ?? "Do not contact";
  const whole = why.kind === "domain" ? ` Everyone at ${why.value} is listed.` : "";
  const from = [sourceText(why.source), ago(why.created_at)].filter(Boolean).join(", ");
  return (
    <Badge tone="red" title={`${details[why.reason] ?? "On the do not contact list."}${whole} ${from ? `Listed ${from}.` : ""} Change it under CRM, Do not contact.`}>
      Won't be emailed: {label.toLowerCase()}{why.kind === "domain" ? " (whole domain)" : ""}
    </Badge>
  );
}
