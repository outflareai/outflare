// A playbook's home in a brand: what is on, what each part has done and
// how its signals ended. Community playbooks and a brand's own share it.
import { useEffect, useState } from "react";
import type { PlaybookHome, PlaybookLift, Schemas } from "@outflare/sdk";
import { api } from "@/api";
import { Badge, field } from "@/ui";
import { plural } from "@/format";
import type { Tab } from "@/App";

type Go = (tab: Tab, item?: string) => void;
type Part = PlaybookHome["parts"][number] | Schemas["OwnPlaybook"]["parts"][number];
type Home = { parts: Part[]; signals: PlaybookHome["signals"] };

export const partKind: Record<Part["kind"], string> = { rule: "Rule", journey: "Journey", smart_list: "List", signal_type: "Signal type", metric: "Metric", score: "Score", routing_rule: "Routing rule", email: "Email" };
const partTab: Record<Part["kind"], Tab> = { rule: "rules", journey: "journeys", smart_list: "lists", signal_type: "signals", metric: "people", score: "people", routing_rule: "people", email: "templates" };
const partItem = (part: Part) => (part.kind === "metric" ? "metrics" : part.kind === "score" ? "scores" : part.kind === "routing_rule" ? "routing" : (part.id ?? undefined));

// PlaybookHomeSection is the playbook's home in the brand: what is on,
// what each part has done, and how its signals ended.
export function PlaybookHomeSection({ home, go, title = "In this brand" }: { home: Home; go: Go; title?: string }) {
  const did = (part: Part) =>
    part.kind === "rule" ? `fired ${plural(part.count, "time")} in 30 days`
      : part.kind === "journey" ? `${part.count} in it now`
        : part.kind === "smart_list" ? `${part.count} on it`
          : part.kind === "metric" ? `on ${plural(part.count, "record")}`
            : part.kind === "score" ? `${plural(part.count, "record")} scored`
              : part.kind === "routing_rule" ? `${part.count} routed`
                : part.kind === "email" ? `sent ${plural(part.count, "time")} in 30 days`
                  : `${part.count} open`;
  const off = home.parts.filter((x) => (x.kind === "rule" || x.kind === "journey") && x.status !== "active").length;
  const g = home.signals;
  const closed = g.won + g.no_change + g.not_useful;
  return (
    <section className="space-y-3 rounded-md border p-3">
      <div className="flex items-center justify-between gap-2">
        <h3 className="font-medium">{title}</h3>
        {off > 0 ? <Badge tone="amber">{off} to turn on</Badge> : <Badge tone="green">Running</Badge>}
      </div>
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        {([["Needs attention", g.open], ["Late", g.late], ["Won", g.won], ["Not useful", g.not_useful]] as const).map(([label, n]) => (
          <button key={label} type="button" className="rounded-md border px-2.5 py-2 text-left hover:bg-muted/50" onClick={() => go("signals")}>
            <span className="block text-[18px] font-medium tabular-nums">{n}</span>
            <span className="block text-[12px] text-muted-foreground">{label}</span>
          </button>
        ))}
      </div>
      {closed > 0 && <p className="text-[12px] text-muted-foreground">{Math.round((g.won / closed) * 100)}% of the signals closed in the last 90 days were won.</p>}
      <ul className="divide-y">
        {home.parts.map((part) => (
          <li key={part.kind + part.key} className="flex items-center gap-2 py-1.5">
            <span className="w-20 shrink-0 text-[12px] text-muted-foreground">{partKind[part.kind]}</span>
            <button type="button" className="min-w-0 flex-1 truncate text-left underline-offset-4 hover:underline" onClick={() => go(partTab[part.kind], partItem(part))}>{part.name}</button>
            <span className="shrink-0 text-[12px] text-muted-foreground">{did(part)}</span>
            {(part.kind === "rule" || part.kind === "journey") && <Badge tone={part.status === "active" ? "green" : part.status === "paused" ? "amber" : "grey"}>{part.status === "active" ? "On" : part.status === "paused" ? "Paused" : "Draft"}</Badge>}
          </li>
        ))}
      </ul>
    </section>
  );
}

type Lift = PlaybookLift;
const money = (list: { amount_cents: number; currency: string }[] | null | undefined) =>
  (list ?? []).map((m) => new Intl.NumberFormat(undefined, { style: "currency", currency: m.currency, maximumFractionDigits: 0 }).format(m.amount_cents / 100)).join(" + ");
const pct = (v: number) => `${Math.round(v * 1000) / 10}%`;
const verdictText: Record<string, { text: string; tone: "green" | "red" | "grey" | "amber" }> = {
  better: { text: "It worked", tone: "green" },
  worse: { text: "It did worse", tone: "red" },
  no_clear_change: { text: "No clear change", tone: "grey" },
  too_early: { text: "Too early to say", tone: "amber" },
};

// PlaybookLiftSection answers "did this playbook work?": each of its
// journeys that holds some back at random, against the ones held back.
export function PlaybookLiftSection({ brand, playbook, go, own }: { brand: string; playbook: string; go: Go; own?: boolean }) {
  const [lift, setLift] = useState<Lift | null>(null);
  const [days, setDays] = useState(30);
  useEffect(() => {
    let live = true;
    const params = { path: { brand, id: playbook }, query: { days } };
    void (own ? api.GET("/v1/brands/{brand}/own-playbooks/{id}/lift", { params }) : api.GET("/v1/brands/{brand}/playbooks/{id}/lift", { params })).then(({ data }) => {
      if (live && data) setLift(data);
    });
    return () => {
      live = false;
    };
  }, [brand, playbook, days, own]);
  if (!lift || lift.journeys.length === 0) return null;
  return (
    <section className="space-y-3 rounded-md border p-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="font-medium">Did it work?</h3>
        {lift.measured && (
          <label className="flex items-center gap-1.5 text-[12px] text-muted-foreground">
            Counting what happened in the
            <select aria-label="Days after entering" className={field} value={days} onChange={(e) => setDays(Number(e.target.value))}>
              {[14, 30, 60, 90].map((d) => <option key={d} value={d}>{d} days</option>)}
            </select>
            after
          </label>
        )}
      </div>
      {!lift.measured && (
        <p className="text-muted-foreground">
          Nothing here can say yet. To know what a playbook changes, some have to be left out on purpose, so there is someone to compare with. Open one of its journeys and put a "Split at random" step first, with one path marked as held back (10% is usual).
        </p>
      )}
      <ul className="divide-y">
        {lift.journeys.map((j) => {
          const ran = j.ran, held = j.held_back;
          const v = verdictText[ran?.verdict ?? "too_early"] ?? verdictText.too_early;
          return (
            <li key={j.journey_id} className="space-y-1.5 py-2">
              <div className="flex items-center gap-2">
                <button type="button" className="min-w-0 flex-1 truncate text-left font-medium underline-offset-4 hover:underline" onClick={() => go("journeys", j.journey_id)}>{j.name}</button>
                {j.measured ? <Badge tone={v.tone}>{v.text}</Badge> : <Badge>Not measured</Badge>}
              </div>
              {j.measured && ran && held ? (
                <>
                  <div className="grid grid-cols-2 gap-2">
                    {([["Went through", ran], [`Held back (${j.holdout_percent}%)`, held]] as const).map(([label, x]) => (
                      <div key={label} className="rounded-md border px-2.5 py-2">
                        <span className="block text-[12px] text-muted-foreground">{label}</span>
                        <span className="block text-[15px] font-medium tabular-nums">{x.people === 0 ? "Nobody yet" : `${x.converted} of ${x.people} · ${pct(x.rate)}`}</span>
                        {x.revenue_per_person.length > 0 && <span className="block text-[12px] text-muted-foreground">{money(x.revenue_per_person)} each</span>}
                      </div>
                    ))}
                  </div>
                  <p className="text-[12px] text-muted-foreground">
                    {ran.verdict === "too_early"
                      ? `A verdict needs at least ${lift.min_people} in each group and someone converting. So far ${ran.people} went through and ${held.people} ${held.people === 1 ? "was" : "were"} held back.`
                      : ran.verdict === "no_clear_change"
                        ? "The difference is within what chance alone would give."
                        : `${ran.lift != null ? `${ran.lift >= 0 ? "+" : ""}${pct(ran.lift)} against the ones held back` : "Against the ones held back"}${(ran.incremental_revenue ?? []).length > 0 ? `, ${money(ran.incremental_revenue ?? [])} more than they would have paid anyway` : ""}.`}
                  </p>
                </>
              ) : (
                <p className="text-[12px] text-muted-foreground">It holds nobody back, so there is nothing to compare with.</p>
              )}
            </li>
          );
        })}
      </ul>
    </section>
  );
}
