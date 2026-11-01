import { ArrowUpRight, Check, Clock, FileText, Mail } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { track } from "@/lib/analytics";

import {
  proposalMoney as money,
  proposalStage,
  milestoneDue,
  type Proposal,
  type ProposalConfig,
} from "./proposals";
export function ProposalDocument({
  config: c,
  currency,
  proposal: p,
}: {
  config: ProposalConfig;
  currency: string;
  proposal?: Proposal;
}) {
  const total = c.milestones.reduce((n, m) => n + m.amount_cents, 0);
  return (
    <div className="space-y-7">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <div className="mb-3 flex gap-2">
            <Badge variant="outline">
              {p ? proposalStage(p) : "Personal invitation"}
            </Badge>
            {p && (
              <span className="text-xs text-muted-foreground">
                Version {p.version}
              </span>
            )}
          </div>
          <h2 className="text-2xl font-semibold tracking-tight">{c.title}</h2>
        </div>
        <div className="text-right">
          <p className="text-xs text-muted-foreground">
            {c.request_quote
              ? "Your quote requested"
              : p?.accepted_at
                ? "Agreed cash compensation"
                : "Proposed cash compensation"}
          </p>
          <p className="mt-2 text-2xl font-semibold">
            {c.request_quote
              ? "Let's hear your price"
              : total
                ? money(total, currency)
                : "No cash payment"}
          </p>
          {c.milestones.some((m) => m.benefit) && (
            <p className="mt-2 text-xs text-muted-foreground">
              Plus product or credit benefits below
            </p>
          )}
        </div>
      </div>
      <p className="whitespace-pre-wrap text-sm leading-7 text-muted-foreground">
        {c.brief}
      </p>
      <div className="flex flex-wrap gap-3">
        {[
          [c.brand_kit_url, "Brand kit"],
          [c.product_url, "Product information"],
        ]
          .filter(([url]) => url)
          .map(([url, label]) => (
            <Button key={label} asChild variant="outline" size="sm">
              <a href={url} target="_blank" rel="noopener noreferrer">
                {label}
                <ArrowUpRight className="size-3" />
              </a>
            </Button>
          ))}
        <Button variant="ghost" size="sm" asChild>
          <a
            href={`mailto:${c.reply_to}?subject=${encodeURIComponent(c.title)}`}
          >
            <Mail className="size-3" />
            Discuss changes by email
          </a>
        </Button>
      </div>
      <section className="space-y-4">
        <h3 className="text-sm font-semibold">What we're making together</h3>
        {c.deliverables.map((d, i) => (
          <Card className="gap-4 p-5 shadow-none" key={d.id}>
            <div className="flex items-start gap-3">
              <span className="grid size-7 shrink-0 place-items-center rounded-full border text-xs">
                {i + 1}
              </span>
              <div>
                <h4 className="text-sm font-semibold">{d.title}</h4>
                <p className="mt-1 text-xs text-muted-foreground">
                  {[
                    d.channel,
                    d.format,
                    d.deadline && `Due: ${d.deadline}`,
                    d.live_days
                      ? `Must remain live for ${d.live_days} days`
                      : "No minimum live period specified",
                  ]
                    .filter(Boolean)
                    .join(" · ")}
                </p>
              </div>
            </div>
            {d.requirements && (
              <p className="whitespace-pre-wrap text-sm leading-6 text-muted-foreground">
                {d.requirements}
              </p>
            )}
            {d.placement_url && (
              <a
                className="break-all text-xs underline"
                target="_blank"
                rel="noopener noreferrer"
                href={d.placement_url}
              >
                {d.placement_url}
              </a>
            )}
            {d.link_url && (
              <div className="rounded-lg bg-muted/40 p-3 text-xs leading-6">
                <p className="break-all">Required link: {d.link_url}</p>
                {d.link_text && <p>Link text: {d.link_text}</p>}
                {d.link_attribute && <p>Link attribute: {d.link_attribute}</p>}
              </div>
            )}
          </Card>
        ))}
      </section>
      <section className="space-y-3">
        <h3 className="text-sm font-semibold">Rewards & approval milestones</h3>
        <p className="text-xs leading-6 text-muted-foreground">
          Every milestone needs the brand's manual approval. Waiting periods
          start when the brand verifies the linked deliverable. Approval adds
          eligible rewards to your program ledger; payout terms still apply.
        </p>
        {c.milestones.map((m) => {
          const paid = p?.progress.milestones?.[m.id]?.approved_at,
            due = p ? milestoneDue(p, m) : null;
          return (
            <div
              key={m.id}
              className="flex flex-wrap items-center justify-between gap-3 rounded-lg border p-4"
            >
              <div>
                <p className="text-sm font-medium">{m.title}</p>
                <p className="mt-1 text-xs text-muted-foreground">
                  {m.wait_days
                    ? `${m.wait_days} days after verification`
                    : "After publication is verified"}
                  {due ? ` · Eligible ${due.toLocaleDateString()}` : ""}
                </p>
                {m.benefit && (
                  <p className="mt-2 text-xs">Benefit: {m.benefit}</p>
                )}
              </div>
              <div className="text-right">
                <p className="text-sm font-semibold">
                  {m.amount_cents
                    ? money(m.amount_cents, currency)
                    : m.benefit
                      ? "Product benefit"
                      : "No cash payment"}
                </p>
                <span className="mt-2 inline-flex items-center gap-1 text-[11px] text-muted-foreground">
                  {paid ? (
                    <Check className="size-3" />
                  ) : (
                    <Clock className="size-3" />
                  )}
                  {paid
                    ? "Approved"
                    : due && due <= new Date()
                      ? "Ready for brand review"
                      : "Awaiting approval"}
                </span>
              </div>
            </div>
          );
        })}
      </section>
      {c.commission_terms && (
        <section>
          <h3 className="text-sm font-semibold">Commission arrangement</h3>
          <p className="mt-3 whitespace-pre-wrap text-sm leading-6 text-muted-foreground">
            {c.commission_terms}
          </p>
          <p className="mt-2 text-xs text-muted-foreground">
            Commission tracking and calculation follow this program's reward
            rules.
          </p>
        </section>
      )}
      {c.terms && (
        <section>
          <h3 className="text-sm font-semibold">Additional agreement terms</h3>
          <p className="mt-3 whitespace-pre-wrap text-sm leading-7 text-muted-foreground">
            {c.terms}
          </p>
        </section>
      )}
      {p?.accepted_at && (
        <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg bg-muted/40 p-4">
          <p className="text-xs text-muted-foreground">
            Accepted {new Date(p.accepted_at).toLocaleString()} · Version{" "}
            {p.version}
          </p>
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              const text = `${c.title}\nAgreement ${p.id}, version ${p.version}\nAccepted: ${p.accepted_at}\nRecipient: ${p.recipient}\nCurrency: ${currency}\n\n${JSON.stringify(c, null, 2)}`;
              const url = URL.createObjectURL(
                new Blob([text], { type: "text/plain;charset=utf-8" }),
              );
              const a = document.createElement("a");
              a.href = url;
              a.download = `agreement-${p.id}.txt`;
              a.click();
              URL.revokeObjectURL(url);
              track("ui.partner_agreement_downloaded");
            }}
          >
            <FileText className="size-3" />
            Download agreement
          </Button>
        </div>
      )}
    </div>
  );
}
