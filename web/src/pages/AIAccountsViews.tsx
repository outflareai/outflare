// The manager's views of AI Accounts (plans/ai-accounts, step 5): spend,
// people, a timeline of renewals, and usage. Spend, people and the
// timeline work from the accounts as people entered them. Usage (which
// account to use, what runs out before it resets, which plans fit) needs
// readings from Outflare for desktop; until it reports, that view shows
// what it will look like with example data, marked as such.

import { useMemo, useState } from "react";
import type { AIAccount, AIProvider } from "@outflare/sdk";
import { Badge, Button, Card, Stat, Stats } from "../ui";
import { AILogo, providerLabel } from "@/components/ai-logo";
import { Switch } from "@/components/ui/switch";
import { cn } from "@/lib/utils";
import { track } from "@/lib/analytics";
import { companyPaid, dayDate, daysUntil, isoDay, monthlyCents, owners, renewalsBetween, renewingWithin, shortDay, usd } from "@/lib/aiAccounts";

const providerOrder: AIProvider[] = ["claude", "chatgpt", "cursor", "copilot", "anthropic_api", "openai_api"];

// ---- Spend ----

export function SpendView({ accounts, open }: { accounts: AIAccount[]; open: (a: AIAccount) => void }) {
  const company = accounts.filter(companyPaid);
  const month = company.reduce((n, a) => n + (monthlyCents(a) ?? 0), 0);
  const upcoming = renewingWithin(accounts, 30);
  const upcomingCost = upcoming.filter(companyPaid).reduce((n, a) => n + (a.price_cents ?? 0), 0);
  const personal = accounts.filter((a) => a.paid_by === "person" && !a.cancelled);
  const unpriced = company.filter((a) => monthlyCents(a) == null);

  const byTool = providerOrder
    .map((p) => {
      const list = company.filter((a) => a.provider === p);
      return { p, count: list.length, cents: list.reduce((n, a) => n + (monthlyCents(a) ?? 0), 0) };
    })
    .filter((t) => t.cents > 0)
    .sort((x, y) => y.cents - x.cents);
  const top = Math.max(1, ...byTool.map((t) => t.cents));

  const attention = [
    ...accounts.filter((a) => a.owner_left && !a.cancelled).map((a) => ({ a, why: "Its owner left the workspace: cancel it or hand it to someone." })),
    ...accounts.filter((a) => !a.cancelled && a.renews_on && daysUntil(a.renews_on) < 0).map((a) => ({ a, why: `Its renewal date (${shortDay(a.renews_on!)}) has passed: set the next one.` })),
    ...company.filter((a) => a.price_cents == null).map((a) => ({ a, why: "No price yet, so it isn't in the totals." })),
  ];

  return (
    <div className="space-y-6">
      <Stats cols={4}>
        <Stat label="A month" value={usd(Math.round(month))} sub={`${company.length - unpriced.length} company-paid plans, before usage`} />
        <Stat label="A year at this rate" value={usd(Math.round(month * 12))} sub="Monthly plans × 12, yearly as they are" />
        <Stat label="Renewing in 30 days" value={usd(upcomingCost)} sub={`${upcoming.length} ${upcoming.length === 1 ? "renewal" : "renewals"}`} tone={upcoming.some((a) => daysUntil(a.renews_on!) <= 3) ? "amber" : undefined} />
        <Stat label="Paid by people" value={String(personal.length)} sub="Tracked, not in the totals" />
      </Stats>

      <Card title="Spend by AI tool" description="What company-paid plans cost a month, yearly plans spread over twelve months.">
        {byTool.length === 0 ? (
          <p className="text-[13px] text-muted-foreground">No company-paid accounts with a price yet.</p>
        ) : (
          <div className="space-y-2.5 text-[13px]">
            {byTool.map((t) => (
              <div key={t.p} className="grid grid-cols-[minmax(0,10rem)_1fr_auto_3rem] items-center gap-3">
                <span className="flex min-w-0 items-center gap-2"><AILogo provider={t.p} className="size-6" /> <span className="truncate">{providerLabel(t.p)}</span></span>
                <span className="h-2 overflow-hidden rounded-full bg-muted"><span className="block h-full rounded-full bg-foreground/70" style={{ width: `${(t.cents / top) * 100}%` }} /></span>
                <span className="text-right tabular-nums">{usd(Math.round(t.cents))}</span>
                <span className="text-right text-muted-foreground tabular-nums">{month ? Math.round((t.cents / month) * 100) : 0}%</span>
              </div>
            ))}
            {unpriced.length > 0 && <p className="pt-1 text-[12px] text-muted-foreground">Not included: {unpriced.length} prepaid, usage-billed or unpriced {unpriced.length === 1 ? "account" : "accounts"}.</p>}
          </div>
        )}
      </Card>

      <div className="grid gap-6 lg:grid-cols-2">
        <Card title="Renewing in the next 30 days" description={upcoming.length ? `${usd(upcomingCost)} charged to the company.` : undefined}>
          {upcoming.length === 0 ? (
            <p className="text-[13px] text-muted-foreground">Nothing renews in the next 30 days.</p>
          ) : (
            <ul className="divide-y text-[13px]">
              {upcoming.map((a) => (
                <li key={a.id}>
                  <button className="flex w-full items-center gap-3 py-2 text-left hover:bg-accent/50" onClick={() => open(a)}>
                    <span className={cn("w-14 shrink-0 tabular-nums", daysUntil(a.renews_on!) <= 3 && "text-amber-600 dark:text-amber-400")}>{shortDay(a.renews_on!)}</span>
                    <AILogo provider={a.provider} className="size-6" />
                    <span className="min-w-0 flex-1 truncate">{a.name}</span>
                    {a.paid_by === "person" && <Badge tone="blue">Person</Badge>}
                    <span className="tabular-nums">{a.price_hidden ? "Hidden" : a.price_cents != null ? usd(a.price_cents) : "—"}</span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </Card>
        <Card title="Needs a look" description="Keep the numbers right, and stop paying for what nobody uses.">
          {attention.length === 0 ? (
            <p className="text-[13px] text-muted-foreground">Everything has an owner, a price and a renewal date.</p>
          ) : (
            <ul className="divide-y text-[13px]">
              {attention.map(({ a, why }, i) => (
                <li key={`${a.id}-${i}`}>
                  <button className="flex w-full items-start gap-3 py-2 text-left hover:bg-accent/50" onClick={() => open(a)}>
                    <AILogo provider={a.provider} className="size-6" />
                    <span className="min-w-0 flex-1"><span className="block font-medium">{a.name}</span><span className="text-muted-foreground">{why}</span></span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </Card>
      </div>
    </div>
  );
}

// ---- People ----

type PersonRow = { id: string; name: string; email: string; left: boolean; accounts: AIAccount[] };

export function PeopleView({ accounts, open }: { accounts: AIAccount[]; open: (a: AIAccount) => void }) {
  const people = useMemo(() => {
    const byID = new Map<string, PersonRow>();
    for (const a of accounts) {
      const os = owners(a).length ? owners(a) : a.members;
      if (os.length === 0) {
        const row = byID.get("") ?? { id: "", name: "Nobody", email: "", left: false, accounts: [] };
        row.accounts.push(a);
        byID.set("", row);
      }
      for (const m of os) {
        const row = byID.get(m.user_id) ?? { id: m.user_id, name: m.name, email: m.email, left: m.left, accounts: [] };
        row.accounts.push(a);
        byID.set(m.user_id, row);
      }
    }
    const cost = (p: PersonRow) => p.accounts.filter(companyPaid).reduce((n, a) => n + (monthlyCents(a) ?? 0), 0);
    return [...byID.values()].map((p) => ({ ...p, cents: cost(p) })).sort((x, y) => Number(x.left) - Number(y.left) || y.cents - x.cents || x.name.localeCompare(y.name));
  }, [accounts]);
  const [openRow, setOpenRow] = useState<string | null>(null);

  return (
    <Card title="Who has which AI accounts" description="Each person and the accounts they own, with what the company-paid ones cost a month. An account several people own counts for each of them.">
      <ul className="divide-y text-[13px]">
        {people.map((p) => {
          const tools = [...new Set(p.accounts.map((a) => a.provider))];
          const personal = p.accounts.filter((a) => a.paid_by === "person").length;
          const expanded = openRow === p.id;
          return (
            <li key={p.id || "nobody"}>
              <button className="flex w-full flex-wrap items-center gap-x-4 gap-y-1 py-2.5 text-left hover:bg-accent/50" onClick={() => setOpenRow(expanded ? null : p.id)} aria-expanded={expanded}>
                <span className="min-w-0 flex-1 basis-48">
                  <span className="font-medium">{p.name || p.email}</span> {p.left && <Badge tone="amber">Left</Badge>}
                  {p.email && p.name && <span className="block text-[12px] text-muted-foreground">{p.email}</span>}
                </span>
                <span className="flex items-center gap-1">{tools.map((t) => <AILogo key={t} provider={t} className="size-6" />)}</span>
                <span className="w-40 text-muted-foreground">{p.accounts.length} {p.accounts.length === 1 ? "account" : "accounts"}{personal ? ` · ${personal} self-paid` : ""}</span>
                <span className="w-24 text-right tabular-nums">{p.cents ? `${usd(Math.round(p.cents))}/mo` : "—"}</span>
              </button>
              {expanded && (
                <ul className="mb-2 ml-2 space-y-1 border-l pl-3">
                  {p.accounts.map((a) => (
                    <li key={a.id}>
                      <button className="flex w-full items-center gap-2 py-1 text-left hover:underline" onClick={() => open(a)}>
                        <AILogo provider={a.provider} className="size-5" /> {a.name}
                        <span className="text-muted-foreground">{a.plan ? `· ${a.plan}` : ""}{a.renews_on && !a.cancelled ? ` · renews ${shortDay(a.renews_on)}` : ""}{a.cancelled ? " · cancelled" : ""}</span>
                      </button>
                    </li>
                  ))}
                </ul>
              )}
            </li>
          );
        })}
      </ul>
    </Card>
  );
}

// ---- Timeline ----

const SPAN = 35; // days shown

export function TimelineView({ accounts, open }: { accounts: AIAccount[]; open: (a: AIAccount) => void }) {
  const [offset, setOffset] = useState(0); // in spans from this week's Monday
  const start = useMemo(() => {
    const d = new Date();
    d.setHours(12, 0, 0, 0);
    d.setDate(d.getDate() - ((d.getDay() + 6) % 7) + offset * SPAN);
    return d;
  }, [offset]);
  const days = Array.from({ length: SPAN }, (_, i) => {
    const d = new Date(start);
    d.setDate(start.getDate() + i);
    return isoDay(d);
  });
  const today = isoDay(new Date());
  const from = days[0], to = days[SPAN - 1];
  const live = accounts.filter((a) => !a.cancelled);
  const groups = providerOrder.map((p) => ({ p, list: live.filter((a) => a.provider === p) })).filter((g) => g.list.length);
  const cols = { gridTemplateColumns: `minmax(10rem,14rem) repeat(${SPAN}, minmax(1.25rem,1fr))` };

  return (
    <Card
      title="Timeline"
      description="When each account renews. Usage limit resets (the 5-hour and weekly windows) appear here once Outflare for desktop reports them."
      action={
        <div className="flex items-center gap-1">
          <Button variant="ghost" onClick={() => setOffset((o) => o - 1)} aria-label="Earlier">‹</Button>
          <Button variant="ghost" onClick={() => setOffset(0)}>Today</Button>
          <Button variant="ghost" onClick={() => setOffset((o) => o + 1)} aria-label="Later">›</Button>
        </div>
      }
    >
      <div className="mb-2 text-[13px] font-medium">{shortDay(from)} – {shortDay(to)}</div>
      <div className="overflow-x-auto">
        <div className="min-w-[56rem] text-[12px]">
          <div className="grid border-b pb-1" style={cols}>
            <span />
            {days.map((d) => {
              const dt = dayDate(d);
              return (
                <span key={d} className={cn("text-center leading-tight", d === today ? "font-semibold text-foreground" : "text-muted-foreground", (dt.getDay() === 0 || dt.getDay() === 6) && "opacity-60")}>
                  {dt.toLocaleDateString(undefined, { weekday: "narrow" })}<br />{dt.getDate()}
                </span>
              );
            })}
          </div>
          {groups.map((g) => (
            <div key={g.p}>
              <div className="flex items-center gap-2 bg-muted/40 px-1 py-1.5 font-medium"><AILogo provider={g.p} className="size-5 border-0 bg-transparent" /> {providerLabel(g.p)} <span className="font-normal text-muted-foreground">{g.list.length}</span></div>
              {g.list.map((a) => {
                const marks = renewalsBetween(a, from, to);
                return (
                  <div key={a.id} className="grid items-center border-b py-1.5" style={cols}>
                    <button className="truncate pr-2 text-left hover:underline" onClick={() => open(a)} title={a.name}>{a.name}</button>
                    {days.map((d) => (
                      <span key={d} className={cn("relative flex h-6 items-center justify-center", d === today && "bg-foreground/5")}>
                        {d === today && <span className="absolute inset-y-0 left-1/2 w-px bg-foreground/40" />}
                        {marks.includes(d) && (
                          <span className="relative z-10 rounded bg-foreground px-1 py-0.5 text-[10px] whitespace-nowrap text-background" title={`${a.name} renews ${shortDay(d)}`}>
                            {a.price_cents != null && !a.price_hidden ? usd(a.price_cents) : "Renews"}
                          </span>
                        )}
                      </span>
                    ))}
                  </div>
                );
              })}
            </div>
          ))}
          {groups.length === 0 && <p className="py-6 text-center text-muted-foreground">No accounts to show.</p>}
        </div>
      </div>
      <p className="mt-3 text-[12px] text-muted-foreground">Monthly and yearly plans repeat from their next renewal day; prepaid and usage-billed accounts show only a date you set. {live.filter((a) => !a.renews_on).length > 0 && `${live.filter((a) => !a.renews_on).length} without a renewal date aren't marked.`}</p>
    </Card>
  );
}

// ---- Usage ----

// Example numbers for the usage view until Outflare for desktop reports
// real ones. Northpeak is the made-up company the demo workspace uses.
const example = {
  pick: [
    { provider: "claude" as AIProvider, name: "Claude Team — Northpeak", room: 66, limit: "Weekly", resets: "Mon 09:00", others: "Claude Max — Design: 18% left in the 5-hour window" },
    { provider: "chatgpt" as AIProvider, name: "ChatGPT Plus — Theo", room: 52, limit: "Weekly", resets: "Sat 01:44", others: "ChatGPT Team — Northpeak: 9% left in the 5-hour window" },
  ],
  forecast: [
    { provider: "claude" as AIProvider, name: "Claude Max — Design", used: 74, at: "Runs out Fri 08:11", then: "locked 3 days 19 h until Tue 03:21", out: true },
    { provider: "cursor" as AIProvider, name: "Cursor Teams — Engineering", used: 61, at: "Runs out Nov 11", then: "locked 10 days until the cycle ends", out: true },
    { provider: "chatgpt" as AIProvider, name: "ChatGPT Team — Northpeak", used: 40, at: "~85% at reset", then: "resets Wed 01:44", out: false },
    { provider: "copilot" as AIProvider, name: "Copilot Business", used: 22, at: "~47% at reset", then: "resets Sun 00:00", out: false },
  ],
  fit: [
    { provider: "claude" as AIProvider, name: "Claude Max — Design", weeks: [62, 81, 100, 100, 74, 100, 100, 100, 88, 100, 100, 69, 100], verdict: "too_small", line: "Hit the limit in 9 of 13 weeks: locked about 120 hours. A bigger plan costs $100 more a month." },
    { provider: "chatgpt" as AIProvider, name: "ChatGPT Team — Northpeak", weeks: [44, 52, 61, 48, 70, 66, 58, 49, 63, 71, 55, 60, 47], verdict: "fits", line: "Peaks average 57%: it fits." },
    { provider: "claude" as AIProvider, name: "Claude Team — Northpeak", weeks: [12, 9, 15, 11, 8, 14, 10, 7, 13, 9, 11, 12, 10], verdict: "room", line: "Never above 15%: 3 of 8 seats are unused, $90 a month." },
  ],
};

const verdicts: Record<string, { label: string; tone: "red" | "green" | "blue" }> = {
  too_small: { label: "↑ Too small", tone: "red" },
  fits: { label: "✓ Fits", tone: "green" },
  room: { label: "↓ Room to spare", tone: "blue" },
};

export function UsageView({ accounts }: { accounts: AIAccount[] }) {
  const [preview, setPreview] = useState(false);
  const peakTone = (v: number) => (v >= 100 ? "bg-red-600" : v >= 80 ? "bg-foreground/80" : v >= 60 ? "bg-foreground/55" : v >= 40 ? "bg-foreground/35" : "bg-foreground/15");
  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center gap-3 rounded-md border px-3 py-2.5 text-[13px]">
        <div className="min-w-0 flex-1">
          <div className="font-medium">Waiting for usage data</div>
          <div className="text-muted-foreground">
            Which account to use, what runs out before it resets and which plans fit need readings from Outflare for desktop, which people install once. None of the {accounts.length} accounts has reported yet.
          </div>
        </div>
        <label className="flex items-center gap-2 text-muted-foreground"><Switch checked={preview} onCheckedChange={(on) => { setPreview(on); if (on) track("ui.ai_accounts_example_shown"); }} aria-label="Show an example" /> Show an example</label>
      </div>

      {preview && (
        <>
          <Card title={<span className="flex items-center gap-2">Which account to use <Badge>Example</Badge></span>} description="For each AI tool, the account with the most room before its tightest limit resets.">
            <div className="grid gap-3 sm:grid-cols-2 text-[13px]">
              {example.pick.map((p) => (
                <div key={p.name} className="rounded-md border p-3">
                  <div className="flex items-center gap-2"><AILogo provider={p.provider} className="size-6" /> <span className="font-medium">{p.name}</span></div>
                  <div className="mt-2 flex items-baseline gap-2"><span className="text-xl font-semibold tabular-nums">{p.room}%</span><span className="text-muted-foreground">left · limited by {p.limit} · resets {p.resets}</span></div>
                  <div className="mt-1.5 h-1.5 overflow-hidden rounded-full bg-muted"><span className="block h-full rounded-full bg-green-600" style={{ width: `${p.room}%` }} /></div>
                  <div className="mt-2 text-[12px] text-muted-foreground">Next best: {p.others}</div>
                </div>
              ))}
            </div>
          </Card>

          <Card title={<span className="flex items-center gap-2">2 accounts run out before they reset <Badge>Example</Badge></span>} description="If everyone keeps their current pace. Time locked out is time someone can't work.">
            <ul className="divide-y text-[13px]">
              {example.forecast.map((f) => (
                <li key={f.name} className="grid grid-cols-[minmax(0,14rem)_1fr_minmax(0,13rem)] items-center gap-3 py-2">
                  <span className="flex min-w-0 items-center gap-2"><AILogo provider={f.provider} className="size-6" /> <span className="truncate">{f.name}</span></span>
                  <span className="h-2 overflow-hidden rounded-full bg-muted"><span className={cn("block h-full rounded-full", f.out ? "bg-red-600" : "bg-foreground/60")} style={{ width: `${f.used}%` }} /></span>
                  <span><span className={cn("font-medium", f.out && "text-red-600 dark:text-red-400")}>{f.at}</span><span className="block text-[12px] text-muted-foreground">{f.then}</span></span>
                </li>
              ))}
            </ul>
          </Card>

          <Card title={<span className="flex items-center gap-2">1 plan is too small, 1 has room to spare <Badge>Example</Badge></span>} description="How full each plan's weekly limit got over the last 13 weeks. Red: it hit the limit.">
            <ul className="divide-y text-[13px]">
              {example.fit.map((f) => (
                <li key={f.name} className="grid gap-2 py-2.5 md:grid-cols-[minmax(0,14rem)_auto_minmax(0,1fr)] md:items-center md:gap-4">
                  <span className="flex min-w-0 items-center gap-2"><AILogo provider={f.provider} className="size-6" /> <span className="truncate">{f.name}</span></span>
                  <span className="flex gap-0.5">{f.weeks.map((w, i) => <span key={i} title={`${w}%`} className={cn("h-5 w-3 rounded-sm", peakTone(w))} />)}</span>
                  <span><Badge tone={verdicts[f.verdict].tone}>{verdicts[f.verdict].label}</Badge> <span className="text-muted-foreground">{f.line}</span></span>
                </li>
              ))}
            </ul>
          </Card>
        </>
      )}
    </div>
  );
}

