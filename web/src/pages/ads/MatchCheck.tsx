// The match check: a dry run that reads stored visits and says how many
// more people an ad would be credited with if visits from the same
// network, place and kind of device were treated as one person, and how
// often that guess is wrong where the truth is known. It collects and
// joins nothing.

import { useState } from "react";
import { api, errorMessage } from "../../api";
import type { components } from "@outflare/sdk";
import { track } from "../../lib/analytics";
import { Badge, Button, Card, ErrorText } from "../../ui";

type Check = components["schemas"]["AdMatchCheck"];

const verdicts: Record<Check["verdict"], { tone: "green" | "amber" | "grey"; word: string; text: string }> = {
  worth_testing: { tone: "green", word: "Worth testing", text: "The guess was right often enough, and it would credit ads with noticeably more people." },
  not_worth_it: { tone: "amber", word: "Not worth it", text: "Either the guess is wrong too often, or it would add too few people to matter." },
  not_enough_data: { tone: "grey", word: "Not enough data yet", text: "There are too few known people or known pairs of browsers to judge. Run it again when more people have signed in." },
};

export function MatchCheck({ brand }: { brand: string }) {
  const [check, setCheck] = useState<Check | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const run = async () => {
    setBusy(true);
    setError("");
    const { data, error } = await api.GET("/v1/brands/{brand}/ads/match-check", { params: { path: { brand }, query: { days: 30 } } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    setCheck(data);
    track("ui.ads_match_check_run", { verdict: data.verdict });
  };
  const open = check ? check.people - check.people_credited : 0;
  const pct = (a: number, of: number) => (of > 0 ? `${Math.round((a * 100) / of)}%` : "—");
  const v = check ? verdicts[check.verdict] : null;
  return (
    <Card
      title="Would matching by network and device find more ad customers?"
      description="Someone who clicks an ad on their phone and signs up on their laptop is not credited to the ad today. This checks, from visits you already have, how many more people would be if two browsers on the same network, in the same city, with the same device and browser counted as one person. It is a dry run: nothing is collected, joined or changed."
      action={<Button data-testid="match-check-run" onClick={run} disabled={busy}>{busy ? "Checking…" : check ? "Run again" : "Run the check"}</Button>}
    >
      {error && <ErrorText>{error}</ErrorText>}
      {!check && !error && <p className="text-[13px] text-muted-foreground">Reads the last 30 days. It takes a few seconds.</p>}
      {check && v && (
        <div className="space-y-3 text-[13px]" data-testid="match-check">
          <div className="flex flex-wrap items-center gap-2">
            <Badge tone={v.tone}>{v.word}</Badge>
            <span>{v.text}</span>
          </div>
          <div className="grid gap-3 sm:grid-cols-3">
            <Fact id="would-credit" value={String(check.would_credit)} label="more people an ad would be credited with" note={`${pct(check.would_credit, open)} of the ${open} known people no ad is credited with today`} />
            <Fact id="accuracy" value={check.accuracy == null ? "—" : `${Math.round(check.accuracy * 100)}%`} label="of the time the guess was right" note={check.known_pairs > 0 ? `${check.known_pairs_right} of ${check.known_pairs} pairs of browsers we already know the owner of` : "No two browsers with the same network and device have known owners yet"} />
            <Fact id="ambiguous" value={String(check.ambiguous)} label="people it could not decide for" note="More than one browser fits, or the network and device are too common" />
          </div>
          <p className="text-[12px] text-muted-foreground">
            Read {check.visits} visits from the last {check.days} days; {pct(check.visits_usable, check.visits)} had a known network and browser. Of {check.browsers} browsers, {pct(check.browsers_unique, check.browsers)} had a network and device no other browser shared. A click counts for {check.window_days} days, your acquisition window.
          </p>
        </div>
      )}
    </Card>
  );
}

function Fact({ id, value, label, note }: { id: string; value: string; label: string; note: string }) {
  return (
    <div className="rounded-md border p-3" data-testid={`match-check-${id}`}>
      <div className="font-mono text-[22px] font-medium tabular-nums">{value}</div>
      <div>{label}</div>
      <div className="mt-1 text-[12px] text-muted-foreground">{note}</div>
    </div>
  );
}
