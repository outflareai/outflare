import { useEffect, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { api } from "@/api";
import type { Tab } from "@/App";

type Lead = Schemas["SalesLead"];
type Signal = Schemas["Signal"];

// The conversation's person and company have pages of their own, with their
// whole history.
export function SalesRecordLinks({
  lead,
  go,
}: {
  lead: Lead;
  go: (tab: Tab, item?: string) => void;
}) {
  const link = "text-sm underline underline-offset-2 hover:text-foreground";
  return (
    <>
      <button
        type="button"
        className={link}
        onClick={() => go("people", lead.record_id)}
      >
        Open person
      </button>
      {lead.company_id && (
        <button
          type="button"
          className={link}
          onClick={() => go("people", lead.company_id ?? undefined)}
        >
          Open company
        </button>
      )}
    </>
  );
}

// The open signals on the conversation's person and their company, from
// Needs attention. Shown only when there are any.
export function SalesNeedsAttention({
  brand,
  lead,
  go,
}: {
  brand: string;
  lead: Lead;
  go: (tab: Tab, item?: string) => void;
}) {
  const [signals, setSignals] = useState<Signal[]>([]);
  const company = lead.company_id;
  useEffect(() => {
    let live = true;
    const records = [lead.record_id, ...(company ? [company] : [])];
    void Promise.all(
      records.map((record) =>
        api.GET("/v1/brands/{brand}/signals", {
          params: { path: { brand }, query: { record, status: "open" } },
        }),
      ),
    )
      .then((answers) => {
        if (!live) return;
        const all = answers.flatMap((r) => r.data?.data ?? []);
        all.sort((a, b) =>
          (a.due_at ?? "9999").localeCompare(b.due_at ?? "9999"),
        );
        setSignals(all);
      })
      // A side panel: the conversation works without it.
      .catch(() => live && setSignals([]));
    return () => {
      live = false;
    };
  }, [brand, lead.record_id, company]);
  if (signals.length === 0) return null;
  return (
    <section className="space-y-2 rounded-xl border bg-card p-4 text-sm">
      <div className="flex items-center justify-between gap-3">
        <h3 className="font-medium">Needs attention</h3>
        <button
          type="button"
          className="text-xs underline underline-offset-2"
          onClick={() => go("signals")}
        >
          Open Needs attention
        </button>
      </div>
      <ul className="space-y-1">
        {signals.map((s) => (
          <li key={s.id} className="flex flex-wrap items-baseline gap-x-2">
            <span className="font-medium">{s.title}</span>
            <span className="text-xs text-muted-foreground">
              {s.type_label}
              {s.record_id !== lead.record_id && ` · ${s.record_name}`}
              {s.due_at &&
                ` · due ${new Date(s.due_at).toLocaleDateString()}`}
            </span>
          </li>
        ))}
      </ul>
    </section>
  );
}
