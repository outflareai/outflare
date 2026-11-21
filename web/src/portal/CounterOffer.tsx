import { useState, type FormEvent } from "react";
import { LockKeyhole } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import {
  maxCounterRounds,
  offerTotal,
  proposalMoney,
  removalText,
  type Counter,
  type CounterChange,
  type LinkRule,
  type Proposal,
} from "./proposals";

const fields: { key: keyof CounterChange; label: string }[] = [
  { key: "placement_url", label: "Your page" },
  { key: "link_text", label: "Link text" },
  { key: "deadline", label: "Publish date" },
];

// The terms a recipient can never change in a counter-offer.
export function LockedTerms({ rule }: { rule?: LinkRule | null }) {
  return (
    <div className="rounded-lg bg-muted/40 p-4 text-xs leading-6 text-muted-foreground">
      <p className="flex items-center gap-2 font-medium text-foreground">
        <LockKeyhole className="size-3.5" />
        Fixed terms
      </p>
      <ul className="mt-1 list-disc pl-5">
        <li>Where the link points</li>
        <li>The link is a normal link (not nofollow or sponsored)</li>
        <li>The link stays up for good, with no renewal fees</li>
        <li>Nothing is paid before the link is live</li>
        {rule && <li>{removalText(rule)}</li>}
      </ul>
    </div>
  );
}

// One line per round: the price asked and what changed, against the offer.
export function CounterHistory({
  proposal,
  currency,
}: {
  proposal: Proposal;
  currency: string;
}) {
  const counters = proposal.progress.counters || [];
  if (!counters.length) return null;
  const titles = Object.fromEntries(
    proposal.config.deliverables.map((d) => [d.id, d]),
  );
  return (
    <ol className="space-y-3">
      {counters.map((c: Counter) => (
        <li key={c.round} className="rounded-lg border p-4 text-sm">
          <p className="flex flex-wrap items-center justify-between gap-2">
            <span className="font-medium">
              Round {c.round} of {maxCounterRounds}
            </span>
            <span className="text-xs text-muted-foreground">
              {new Date(c.at).toLocaleString()}
              {c.accepted_at ? " · accepted" : ""}
            </span>
          </p>
          <ul className="mt-2 space-y-1">
            <li>
              Price:{" "}
              {c.amount_cents > 0 ? (
                <>
                  <span className="line-through text-muted-foreground">
                    {proposalMoney(offerTotal(proposal.config), currency)}
                  </span>{" "}
                  <strong>{proposalMoney(c.amount_cents, currency)}</strong>
                </>
              ) : (
                "as offered"
              )}
            </li>
            {Object.entries(c.changes || {}).map(([id, ch]) =>
              fields
                .filter((f) => ch[f.key])
                .map((f) => (
                  <li key={id + f.key} className="break-words">
                    {titles[id]?.title || id}, {f.label.toLowerCase()}:{" "}
                    {f.key === "placement_url" ? (
                      <a
                        className="underline"
                        href={ch[f.key]}
                        target="_blank"
                        rel="noreferrer"
                      >
                        {ch[f.key]}
                      </a>
                    ) : (
                      <strong>{ch[f.key]}</strong>
                    )}
                  </li>
                )),
            )}
          </ul>
          {c.note && (
            <p className="mt-2 whitespace-pre-wrap text-muted-foreground">
              {c.note}
            </p>
          )}
        </li>
      ))}
    </ol>
  );
}

// The recipient's form for suggesting changes to an offer.
export function CounterForm({
  proposal,
  currency,
  disabled,
  onCancel,
  onSend,
}: {
  proposal: Proposal;
  currency: string;
  disabled: boolean;
  onCancel: () => void;
  onSend: (body: {
    amount_cents: number;
    changes: Record<string, CounterChange>;
    note: string;
  }) => void;
}) {
  const total = offerTotal(proposal.config);
  const used = (proposal.progress.counters || []).length;
  const [price, setPrice] = useState(total ? String(total / 100) : "");
  const [note, setNote] = useState("");
  const [changes, setChanges] = useState<Record<string, CounterChange>>(() =>
    Object.fromEntries(
      proposal.config.deliverables.map((d) => [
        d.id,
        {
          placement_url: d.placement_url,
          link_text: d.link_text,
          deadline: d.deadline,
        },
      ]),
    ),
  );
  const submit = (e: FormEvent) => {
    e.preventDefault();
    onSend({
      amount_cents: total ? Math.round(Number(price) * 100) : 0,
      changes,
      note,
    });
  };
  return (
    <form className="space-y-5 border-t pt-6" onSubmit={submit}>
      <div>
        <h3 className="text-base font-semibold">Suggest changes</h3>
        <p className="mt-1 text-xs text-muted-foreground">
          You can change the price, which of your pages the link goes on, the
          link text and the publish date. Round {used + 1} of {maxCounterRounds}
          .
        </p>
      </div>
      {total > 0 && (
        <label className="block space-y-1.5 text-sm">
          <span className="font-medium">Your price ({currency})</span>
          <Input
            type="number"
            min="0.01"
            step="0.01"
            required
            value={price}
            onChange={(e) => setPrice(e.target.value)}
          />
          <span className="text-xs text-muted-foreground">
            Offered: {proposalMoney(total, currency)}
          </span>
        </label>
      )}
      {proposal.config.deliverables.map((d) => (
        <fieldset key={d.id} className="space-y-3 rounded-lg border p-4">
          <legend className="px-1 text-sm font-medium">{d.title}</legend>
          {fields.map((f) => (
            <label key={f.key} className="block space-y-1.5 text-sm">
              <span>{f.label}</span>
              <Input
                type={f.key === "placement_url" ? "url" : "text"}
                placeholder={
                  f.key === "placement_url"
                    ? "https://"
                    : f.key === "deadline"
                      ? "For example, by 15 December"
                      : ""
                }
                value={changes[d.id]?.[f.key] || ""}
                onChange={(e) =>
                  setChanges((c) => ({
                    ...c,
                    [d.id]: { ...c[d.id], [f.key]: e.target.value },
                  }))
                }
              />
            </label>
          ))}
        </fieldset>
      ))}
      <label className="block space-y-1.5 text-sm">
        <span className="font-medium">Message to the brand</span>
        <Textarea
          maxLength={4000}
          placeholder="Optional: why these changes"
          value={note}
          onChange={(e) => setNote(e.target.value)}
        />
      </label>
      <LockedTerms rule={proposal.config.removal} />
      <div className="flex gap-3">
        <Button disabled={disabled}>Send changes</Button>
        <Button type="button" variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </form>
  );
}
