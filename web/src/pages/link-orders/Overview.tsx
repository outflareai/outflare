import { Card } from "../../ui";
import {
  amountOf,
  committedStatuses,
  donorBy,
  liveStatuses,
  money,
  shortId,
  spentStatuses,
  statusLabel,
  statuses,
  type LinkOrder,
  type PageData,
} from "./shared";
import { OrdersTable } from "./OrdersTable";

export function BudgetBars({ data }: { data: PageData }) {
  const rows = data.budget?.budgets ?? [];
  if (!rows.length) {
    return (
      <Card title="Budget vs spend" description="Donor price only, per brand. USD and EUR are never added together.">
        <p className="text-[13px] text-muted-foreground">
          No budget tagged <span className="font-mono">link_orders</span> is set for this brand. Approve still works; there is no cap until one is added in Presence.
        </p>
      </Card>
    );
  }
  return (
    <Card title="Budget vs spend" description="Donor price only, per brand. USD and EUR are never added together. Spent is booked when the approver marks Paid — after publication, not before.">
      <div className="flex flex-col gap-3">
        {rows.map((b) => {
          const cap = Math.max(b.amount_minor, 1);
          const spentPct = (b.spent / cap) * 100;
          const commitPct = (b.committed / cap) * 100;
          const used = b.spent + b.committed;
          const hot = used >= b.amount_minor * 0.8;
          return (
            <div key={b.budget_id || b.currency} className="flex flex-col gap-1">
              <div className="flex flex-wrap gap-x-3 gap-y-0.5 text-[12px] text-muted-foreground">
                <span className="font-medium text-foreground">{b.currency}</span>
                {b.title ? <span>{b.title}</span> : null}
                <span>Spent {money(b.spent, b.currency)}</span>
                <span>Committed {money(b.committed, b.currency)}</span>
                <span>Remaining {money(b.remaining, b.currency)}</span>
                <span>80% alert at {money(Math.round(b.amount_minor * 0.8), b.currency)}</span>
              </div>
              <div className="flex h-2 overflow-hidden rounded-full bg-muted" role="img" aria-label={`${b.currency} budget: spent ${money(b.spent, b.currency)}, committed ${money(b.committed, b.currency)}, remaining ${money(b.remaining, b.currency)}`}>
                <div className="h-full bg-foreground" style={{ width: `${Math.min(spentPct, 100)}%` }} />
                <div className="h-full bg-foreground/40" style={{ width: `${Math.min(commitPct, Math.max(0, 100 - spentPct))}%` }} />
              </div>
              {hot ? <p className="text-[12px] text-amber-600 dark:text-amber-400">This {b.currency} budget is at or over 80% used (spent plus committed).</p> : null}
            </div>
          );
        })}
      </div>
    </Card>
  );
}

function alerts(data: PageData): { tone: "red" | "amber" | "grey"; text: string; order?: LinkOrder }[] {
  const out: { tone: "red" | "amber" | "grey"; text: string; order?: LinkOrder }[] = [];
  const replacements = new Set(data.orders.map((o) => o.replaces_order_id).filter(Boolean) as string[]);
  for (const o of data.orders) {
    const d = donorBy(data.donors, o.donor_profile_id);
    const { amount, currency } = amountOf(o);
    const price = money(amount, currency);
    if (o.status === "lost" && !replacements.has(o.id) && !o.credit_payment_id) {
      const term = o.guarantee_until ? ` The donor's term runs until ${new Date(o.guarantee_until).toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" })}.` : "";
      out.push({
        tone: "red",
        text: `The link on ${o.domain} to ${o.target_url} is lost (${shortId(o.id)}, ${price}).${term} Open a replacement first; record a credit only if the donor cannot replace it.`,
        order: o,
      });
    }
    if (o.status === "published") {
      if (o.last_check_live === true && o.last_check_target_match === true) {
        out.push({
          tone: "amber",
          text: `${d?.domain ?? o.domain} has been published (${shortId(o.id)}, ${price}). Waiting for the brand approver to mark Paid — payment is recorded after publication and a passing live-link check.`,
          order: o,
        });
      } else if (o.last_check_live === false || o.last_check_target_match === false) {
        out.push({
          tone: "amber",
          text: `The live-link check for ${shortId(o.id)} on ${o.domain} did not pass. Paid stays blocked until a later check finds the link on the page, pointing at the target.`,
          order: o,
        });
      }
    }
  }
  for (const b of data.budget?.budgets ?? []) {
    if (b.spent + b.committed >= b.amount_minor * 0.8) {
      out.push({
        tone: "amber",
        text: `The ${b.currency} link-orders budget is at or over 80% used: ${money(b.spent + b.committed, b.currency)} of ${money(b.amount_minor, b.currency)}. Currencies are kept apart.`,
      });
    }
  }
  const pbnLive = data.orders.filter((o) => liveStatuses.includes(o.status) && donorBy(data.donors, o.donor_profile_id)?.pbn).length;
  if (pbnLive) {
    out.push({
      tone: "grey",
      text: `${pbnLive} published (or later) order${pbnLive === 1 ? "" : "s"} use a donor flagged as PBN. The flag is set by hand; Outflare does not detect a PBN automatically.`,
    });
  }
  return out;
}

function nextSteps(data: PageData): string[] {
  const steps: string[] = [];
  const replacements = new Set(data.orders.map((o) => o.replaces_order_id).filter(Boolean) as string[]);
  for (const o of data.orders) {
    if (o.status === "published" && o.last_check_live === true && o.last_check_target_match === true) {
      steps.push(`Mark Paid on ${shortId(o.id)} (${o.domain}) — published, and the last live-link check passed.`);
    }
  }
  for (const o of data.orders) {
    if ((o.status === "quoted" || o.status === "negotiating") && o.quoted_amount_minor != null) {
      steps.push(`Approve or renegotiate ${shortId(o.id)} (${o.domain}, ${money(o.quoted_amount_minor, o.quoted_currency)}) against the ${o.quoted_currency} budget.`);
    }
  }
  for (const o of data.orders) {
    if (o.status === "lost" && !replacements.has(o.id) && !o.credit_payment_id) {
      steps.push(`Open a replacement for the lost link on ${o.domain} (${shortId(o.id)}), using the donor's term when it is still open.`);
    }
  }
  const openCand = data.candidates.filter((c) => c.status === "not_in_catalog").length;
  if (openCand) {
    steps.push(`Review Donor candidates — ${openCand} domain${openCand === 1 ? "" : "s"} from saved competitor link gaps ${openCand === 1 ? "is" : "are"} not in the catalog yet. This list is read from stored data only; it does not start a paid pull.`);
  }
  return steps;
}

export function Overview({ data }: { data: PageData }) {
  const counts = Object.fromEntries(statuses.map((s) => [s, data.summary?.counts?.[s] ?? data.orders.filter((o) => o.status === s).length])) as Record<string, number>;
  const pbnCatalog = data.donors.filter((d) => d.pbn).length;
  const pbnLive = data.orders.filter((o) => liveStatuses.includes(o.status) && donorBy(data.donors, o.donor_profile_id)?.pbn).length;
  const recent = [...data.orders].sort((a, b) => b.created_at.localeCompare(a.created_at)).slice(0, 8);
  const items = alerts(data);
  const steps = nextSteps(data);

  return (
    <div className="flex flex-col gap-3">
      <BudgetBars data={data} />
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-4 lg:grid-cols-8">
        {statuses.map((s) => (
          <button
            key={s}
            type="button"
            className="rounded-lg border bg-card px-3 py-2 text-left hover:bg-accent/40"
            onClick={() => data.goSection("orders")}
          >
            <div className="text-[18px] font-semibold tabular-nums">{counts[s] ?? 0}</div>
            <div className="text-[12px] text-muted-foreground">{statusLabel[s]}</div>
          </button>
        ))}
      </div>
      <div className="grid gap-3 lg:grid-cols-2">
        <Card title="Alerts">
          {items.length ? (
            <ul className="flex flex-col gap-2">
              {items.map((a, i) => (
                <li key={i} className={`rounded-md border px-3 py-2 text-[13px] ${a.tone === "red" ? "border-red-500/30 bg-red-500/5" : a.tone === "amber" ? "border-amber-500/30 bg-amber-500/5" : "bg-muted/40"}`}>
                  {a.order ? (
                    <button type="button" className="text-left" onClick={() => data.goSection("orders", a.order!.id)}>
                      {a.text}
                    </button>
                  ) : (
                    a.text
                  )}
                </li>
              ))}
            </ul>
          ) : (
            <p className="text-[13px] text-muted-foreground">Nothing needs a look right now.</p>
          )}
        </Card>
        <Card title="What to do next">
          {steps.length ? (
            <ol className="flex list-decimal flex-col gap-2 pl-4 text-[13px]">
              {steps.map((s, i) => (
                <li key={i}>{s}</li>
              ))}
            </ol>
          ) : (
            <p className="text-[13px] text-muted-foreground">No next step is waiting.</p>
          )}
        </Card>
      </div>
      <Card title="PBN in this catalog">
        <p className="text-[13px]">
          {pbnCatalog === 0
            ? "No donors are flagged as PBN."
            : `${pbnCatalog} donor${pbnCatalog === 1 ? "" : "s"} ${pbnCatalog === 1 ? "is" : "are"} flagged as PBN. ${pbnLive === 0 ? "None of those orders are published yet." : `${pbnLive} of them ${pbnLive === 1 ? "is" : "are"} already published or later.`} The flag is set by hand — Outflare has no PBN detector today.`}
        </p>
        <p className="mt-2 rounded-md border border-amber-500/30 bg-amber-500/5 px-3 py-2 text-[13px]">
          PBN links are cheaper and drop more often, and they can harm the site if used carelessly. They stay out of the default “non-PBN” catalog filter and are not auto-tagged on competitor-gap candidates.
        </p>
      </Card>
      <Card title="Recent orders">
        <OrdersTable orders={recent} data={data} />
        <p className="mt-2 text-[12px] text-muted-foreground">
          Spent counts {spentStatuses.map((s) => statusLabel[s].toLowerCase()).join(", ")} orders; committed counts {committedStatuses.map((s) => statusLabel[s].toLowerCase()).join(", ")}. Amounts are the donor price only.
        </p>
      </Card>
    </div>
  );
}
