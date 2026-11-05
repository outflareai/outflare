// Are placements growing, and how fast? The last 28 days against the 28
// before (weighted by links and traction), twelve weeks of gained against
// lost, and the brand's share of voice against tracked competitors.
import { Badge } from "../../ui";

type Week = { week: string; gained: number; lost: number };
type Momentum = {
  gained: number;
  lost: number;
  net: number;
  weighted_net: number;
  previous_net: number;
  previous_weighted_net: number;
  taking_off: number;
  quiet: number;
  trend: string;
  weeks: Week[];
};
type Voice = { who: "brand" | "competitor"; name: string | null; placements: number; share: number };

const trends: Record<string, [string, "green" | "blue" | "amber" | "red" | "grey"]> = {
  accelerating: ["Accelerating", "green"],
  growing: ["Growing", "blue"],
  slowing: ["Growing, but slowing", "amber"],
  flat: ["Flat", "grey"],
  shrinking: ["Shrinking", "red"],
};
const signed = (n: number) => (n > 0 ? `+${n}` : String(n));

export function PresenceMomentum({ momentum, voice }: { momentum?: Momentum; voice?: Voice[] }) {
  if (!momentum) return null;
  const [trend, tone] = trends[momentum.trend] ?? [momentum.trend, "grey"];
  const most = Math.max(1, ...momentum.weeks.flatMap((w) => [w.gained, w.lost]));
  return (
    <div className="grid gap-5 lg:grid-cols-3">
      <section className="rounded-xl border p-5 lg:col-span-2">
        <div className="flex flex-wrap items-center gap-2">
          <h2 className="mr-auto font-semibold">Momentum</h2>
          <Badge tone={tone}>{trend}</Badge>
        </div>
        <p className="mt-1 text-sm text-muted-foreground">
          Last 28 days: {momentum.gained} gained, {momentum.lost} lost, net {signed(momentum.net)} (the 28 days before: net{" "}
          {signed(momentum.previous_net)}). Weighted for links and traction: {signed(momentum.weighted_net)} against{" "}
          {signed(momentum.previous_weighted_net)}. {momentum.taking_off} live {momentum.taking_off === 1 ? "page has" : "pages have"} taken off,{" "}
          {momentum.quiet} {momentum.quiet === 1 ? "is" : "are"} quiet.
        </p>
        <div className="mt-5 flex h-28 items-end gap-1.5" role="img" aria-label="Placements gained and lost each week, last twelve weeks">
          {momentum.weeks.map((w) => (
            <div key={w.week} className="flex h-full flex-1 items-end justify-center gap-0.5" title={`Week of ${w.week}: ${w.gained} gained, ${w.lost} lost`}>
              <div className="w-1/2 rounded-t bg-emerald-500" style={{ height: `${(w.gained / most) * 100}%` }} />
              <div className="w-1/2 rounded-t bg-red-400" style={{ height: `${(w.lost / most) * 100}%` }} />
            </div>
          ))}
        </div>
        <p className="mt-2 text-xs text-muted-foreground">Each week, gained (green) and lost (red) placements.</p>
      </section>
      <section className="rounded-xl border p-5">
        <h2 className="font-semibold">Share of voice</h2>
        <p className="mt-1 text-xs text-muted-foreground">Live placements against each tracked competitor's</p>
        <div className="mt-4 space-y-2 text-sm">
          {(voice ?? []).map((v, i) => (
            <div key={i}>
              <div className="flex justify-between">
                <span className={v.who === "brand" ? "font-medium" : ""}>{v.who === "brand" ? "Us" : v.name}</span>
                <span className="tabular-nums text-muted-foreground">
                  {v.placements} · {v.share}%
                </span>
              </div>
              <div className="mt-1 h-1.5 rounded bg-muted">
                <div className={`h-1.5 rounded ${v.who === "brand" ? "bg-brand" : "bg-slate-400"}`} style={{ width: `${v.share}%` }} />
              </div>
            </div>
          ))}
          {(voice ?? []).length <= 1 && <p className="text-muted-foreground">Track competitors to compare.</p>}
        </div>
      </section>
    </div>
  );
}
