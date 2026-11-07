// How a competitor's link is got and what it's worth to the brand, the
// same on a competitor's page and on Presence (coverage.Value).
import { Badge } from "@/ui";

export type Access = "cant_buy" | "earned" | "paid" | "free" | "unknown";

export const accessNames: Record<Access, string> = {
  cant_buy: "Can't buy",
  earned: "Earned",
  paid: "Paid",
  free: "Free to get",
  unknown: "Unknown",
};

const accessTips: Record<Access, string> = {
  cant_buy: "Editorial on a site that doesn't sell links: news, a university or government site, Wikipedia, or a strong site with real readers",
  earned: "A third party chose to write it: a review, comparison, roundup or article",
  paid: "There is evidence of a deal: sponsored or affiliate",
  free: "Anyone can make it: a profile, a listing, a forum post or an upload to a platform",
  unknown: "The address and numbers say nothing about how it was got",
};

const accessTones: Record<Access, "green" | "blue" | "amber" | "grey"> = { cant_buy: "green", earned: "blue", paid: "amber", free: "grey", unknown: "grey" };

export function AccessBadge({ access }: { access?: string }) {
  if (!access || !(access in accessNames)) return <span className="text-muted-foreground">–</span>;
  const a = access as Access;
  return (
    <Badge tone={accessTones[a]} title={accessTips[a]}>
      {accessNames[a]}
    </Badge>
  );
}

// ValueCell is the 0 to 100 value, with its reasons on hover.
export function ValueCell({ value, reasons }: { value?: number | null; reasons?: string[] }) {
  if (value == null) return <span className="text-muted-foreground">–</span>;
  return (
    <span title={reasons?.length ? reasons.join(" · ") : undefined} className="font-medium tabular-nums">
      {Math.round(value)}
    </span>
  );
}

// Reasons is the short line under a page: why it's worth what it is.
export function Reasons({ reasons }: { reasons?: string[] }) {
  if (!reasons?.length) return null;
  return <div className="text-[11px] text-muted-foreground">{reasons.join(" · ")}</div>;
}

// AccessSelect filters a list by how its links are got.
export function AccessSelect({ value, onChange, className }: { value: string; onChange: (v: Access | "") => void; className?: string }) {
  return (
    <select className={className} aria-label="How it's got" value={value} onChange={(e) => onChange(e.target.value as Access | "")}>
      <option value="">Any link</option>
      {(Object.keys(accessNames) as Access[]).map((a) => (
        <option key={a} value={a}>
          {accessNames[a]}
        </option>
      ))}
    </select>
  );
}
