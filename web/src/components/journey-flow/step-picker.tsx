// The step picker: a searchable catalog of step kinds, grouped as in the
// actions plan (Messaging, Data, Cross-campaign, Notify, Integrations, AI,
// Flow control). A kind that needs a connection the workspace hasn't made
// is greyed out with a link to make it.

import { useEffect, useMemo, useState } from "react";
import { Search } from "lucide-react";
import { api } from "../../api";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { cn } from "@/lib/utils";
import { builtinKinds, groupOrder, groupText, type StepGroup, type StepKind } from "./model";
import { iconFor } from "./flow";

// The chooser fills most of the screen: every step as a card, its group
// down the side, and a search box that is ready to type in.
export function StepPicker({ kinds, onPick, onClose, onConnect, title = "Add a step", description = "Pick what happens next. You fill in its details on the next screen." }: {
  kinds: StepKind[];
  onPick: (k: StepKind) => void;
  onClose: () => void;
  onConnect?: (connection: string) => void;
  title?: string;
  description?: string;
}) {
  const [q, setQ] = useState("");
  const [group, setGroup] = useState<StepGroup | "all">("all");
  const groups = groupOrder.filter((g) => kinds.some((k) => k.group === g));
  const words = q.trim().toLowerCase().split(/\s+/).filter(Boolean);
  const shown = useMemo(
    () =>
      kinds.filter((k) => {
        if (!words.length) return group === "all" || k.group === group;
        // A search looks through every group.
        const text = `${k.label} ${k.kind} ${k.description} ${groupText[k.group]}`.toLowerCase();
        return words.every((w) => text.includes(w));
      }),
    [kinds, group, words.join(" ")], // eslint-disable-line react-hooks/exhaustive-deps
  );
  const usable = (k: StepKind) => !k.requires.some((r) => r.connected === false);
  const first = shown.find(usable);
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="flex h-[min(46rem,calc(100dvh-2rem))] w-full max-w-[min(64rem,calc(100%-2rem))] flex-col gap-0 overflow-hidden p-0 sm:max-w-[min(64rem,calc(100%-2rem))]">
        <DialogHeader className="border-b px-5 pt-5 pb-4 text-left">
          <DialogTitle className="text-lg">{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
          <label className="relative mt-2 block">
            <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" aria-hidden />
            <input
              autoFocus
              type="search"
              aria-label="Search steps"
              placeholder="Search: email, wait, tag, Slack…"
              className="h-11 w-full rounded-md border bg-background pr-3 pl-10 text-[15px] outline-none focus-visible:ring-2 focus-visible:ring-ring"
              value={q}
              onChange={(e) => setQ(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && first) {
                  e.preventDefault();
                  onPick(first);
                }
              }}
            />
          </label>
        </DialogHeader>
        <div className="flex min-h-0 flex-1 flex-col sm:flex-row">
          <nav aria-label="Kinds of step" className="flex shrink-0 gap-1 overflow-x-auto border-b px-3 py-2 sm:w-52 sm:flex-col sm:overflow-y-auto sm:border-r sm:border-b-0 sm:py-3">
            {(["all", ...groups] as const).map((g) => {
              const on = !words.length && group === g;
              const count = g === "all" ? kinds.length : kinds.filter((k) => k.group === g).length;
              return (
                <button
                  key={g}
                  type="button"
                  aria-pressed={on}
                  onClick={() => { setGroup(g); setQ(""); }}
                  className={cn("flex shrink-0 items-center justify-between gap-3 rounded-md px-3 py-2 text-left text-[13px] whitespace-nowrap hover:bg-muted", on ? "bg-muted font-medium" : "text-muted-foreground")}
                >
                  {g === "all" ? "All steps" : groupText[g]}
                  <span className="text-[11px] tabular-nums text-muted-foreground">{count}</span>
                </button>
              );
            })}
          </nav>
          <div className="min-h-0 flex-1 overflow-y-auto p-4">
            {shown.length === 0 ? (
              <p className="py-10 text-center text-sm text-muted-foreground">
                No step matches “{q.trim()}”. <button type="button" className="underline" onClick={() => setQ("")}>Show every step</button>
              </p>
            ) : (
              (words.length || group !== "all" ? [null] : groups).map((g) => {
                const list = g === null ? shown : shown.filter((k) => k.group === g);
                if (!list.length) return null;
                return (
                  <section key={g ?? "found"} className="mb-5 last:mb-0">
                    <h3 className="mb-2 text-[12px] font-medium tracking-wide text-muted-foreground uppercase">
                      {g === null ? (words.length ? `${list.length} found` : groupText[group as StepGroup]) : groupText[g]}
                    </h3>
                    <ul className="grid gap-2 sm:grid-cols-2">
                      {list.map((k) => {
                        const Icon = iconFor(k.kind);
                        const missing = k.requires.filter((r) => r.connected === false);
                        return (
                          <li key={k.kind} className={cn("rounded-lg border", missing.length ? "bg-muted/40" : "hover:border-ring hover:bg-muted/40")}>
                            <button
                              type="button"
                              disabled={missing.length > 0}
                              onClick={() => onPick(k)}
                              className="flex w-full items-start gap-3 rounded-lg p-3 text-left focus-visible:outline-2 focus-visible:outline-ring disabled:cursor-not-allowed disabled:opacity-60"
                            >
                              <span className="flex size-9 shrink-0 items-center justify-center rounded-md border bg-background">
                                <Icon className="size-4" aria-hidden />
                              </span>
                              <span className="min-w-0">
                                <span className="block text-sm font-medium">{k.label}</span>
                                <span className="mt-0.5 block text-[13px] leading-snug text-muted-foreground">{k.description}</span>
                              </span>
                            </button>
                            {missing.map((r) => (
                              <p key={r.connection} className="px-3 pb-3 pl-[3.75rem] text-[12px] text-muted-foreground">
                                Needs {r.label}. <button type="button" className="underline hover:text-foreground" onClick={() => onConnect?.(r.connection)}>Connect it</button>
                              </p>
                            ))}
                          </li>
                        );
                      })}
                    </ul>
                  </section>
                );
              })
            )}
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}

// useStepKinds is the step kinds a journey can use, from the API's catalog
// (GET .../journeys/step-kinds), with whether the workspace has made each
// connection a kind needs. Until it answers, or if it can't, it is the
// built-in four.
export function useStepKinds(ws: string): StepKind[] {
  const [kinds, setKinds] = useState<StepKind[]>(builtinKinds);
  useEffect(() => {
    let live = true;
    Promise.all([
      api.GET("/v1/workspaces/{ws}/journeys/step-kinds", { params: { path: { ws } } }),
      api.GET("/v1/workspaces/{ws}/integrations", { params: { path: { ws } } }),
    ]).then(([k, i]) => {
      if (!live || !k.data) return;
      // A requirement named after a connector (slack) is met when it is
      // connected, ai_model when any AI model is, sequencer when a
      // connection can add people to its campaigns, crm when one can
      // create contacts, and ad_audiences when one can fill ad audiences;
      // others (an email provider) aren't checked here.
      const on = i.data?.data.filter((x) => x.status === "connected") ?? [];
      const connectors = i.data ? new Set(on.map((x) => x.connector)) : null;
      if (connectors && on.some((x) => x.category === "model")) connectors.add("ai_model");
      if (connectors && on.some((x) => x.capabilities.includes("push.leads"))) connectors.add("sequencer");
      if (connectors && on.some((x) => x.capabilities.includes("push.contacts"))) connectors.add("crm");
      if (connectors && on.some((x) => x.capabilities.includes("ads.audiences"))) connectors.add("ad_audiences");
      setKinds((k.data.data as StepKind[]).map((kind) => ({
        ...kind,
        requires: kind.requires.map((r) => (connectors && r.connection !== "email_provider" ? { ...r, connected: connectors.has(r.connection) } : r)),
      })));
    }).catch(() => {});
    return () => { live = false; };
  }, [ws]);
  return kinds;
}
