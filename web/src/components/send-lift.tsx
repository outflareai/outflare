// SendLift shows what a broadcast or sequence added: the people it reached
// against the people it held out on purpose, 30 days after each was
// reached. People without analytics.read don't see it.

import { useEffect, useState } from "react";
import type { LiftPath } from "@outflare/sdk";
import { api } from "../api";
import { Card } from "../ui";
import { cn } from "@/lib/utils";
import { money } from "../pages/Channels";

const verdictText: Record<string, string> = { better: "better", worse: "worse", no_clear_change: "no clear change", too_early: "too early to tell" };
const label: Record<string, string> = { sent: "Got it", held_out: "Held out" };

export function SendLift({ brand, kind, id }: { brand: string; kind: "email-broadcasts" | "sequences"; id: string }) {
  const [paths, setPaths] = useState<LiftPath[] | null>(null);
  useEffect(() => {
    setPaths(null);
    const req = kind === "sequences"
      ? api.GET("/v1/brands/{brand}/sequences/{id}/lift", { params: { path: { brand, id } } })
      : api.GET("/v1/brands/{brand}/email-broadcasts/{id}/lift", { params: { path: { brand, id } } });
    req.then(({ data }) => setPaths(data?.paths ?? []));
  }, [brand, kind, id]);
  if (!paths || paths.every((p) => p.people === 0)) return null;
  const sent = paths.find((p) => p.id === "sent");
  return (
    <Card title="Against the people held out" description="Who converted in the 30 days after they were reached, and what they paid, next to the people held out on purpose.">
      <ul className="space-y-1 text-[13px]">
        {paths.map((p) => (
          <li key={p.id} className="flex flex-wrap justify-between gap-2">
            <span>{label[p.id] ?? p.id}: {(p.rate * 100).toFixed(1)}% of {p.people} converted · {money(p.revenue_per_person)} each</span>
          </li>
        ))}
      </ul>
      {sent?.verdict && (
        <p className={cn("mt-2 text-[13px]", sent.verdict === "better" ? "text-emerald-600" : sent.verdict === "worse" ? "text-destructive" : "text-muted-foreground")}>
          {sent.lift !== null && sent.verdict !== "too_early" ? `${sent.lift >= 0 ? "+" : ""}${(sent.lift * 100).toFixed(0)}% conversion, ` : ""}
          {verdictText[sent.verdict] ?? sent.verdict}
          {sent.incremental_revenue && sent.incremental_revenue.length > 0 && sent.verdict !== "too_early" ? ` · ${money(sent.incremental_revenue)} more than without it` : ""}
        </p>
      )}
    </Card>
  );
}
