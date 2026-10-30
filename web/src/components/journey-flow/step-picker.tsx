// The step picker: a searchable catalog of step kinds, grouped as in the
// actions plan (Messaging, Data, Cross-campaign, Notify, Integrations, AI,
// Flow control). A kind that needs a connection the workspace hasn't made
// is greyed out with a link to make it.

import { useEffect, useState } from "react";
import { api } from "../../api";
import { CommandDialog, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command";
import { builtinKinds, groupOrder, groupText, type StepKind } from "./model";
import { iconFor } from "./flow";

export function StepPicker({ kinds, onPick, onClose, onConnect }: { kinds: StepKind[]; onPick: (k: StepKind) => void; onClose: () => void; onConnect?: (connection: string) => void }) {
  return (
    <CommandDialog open onOpenChange={(o) => !o && onClose()} title="Add a step" description="Search the steps a journey can take.">
      <CommandInput placeholder="Search steps: email, wait, check…" />
      <CommandList className="max-h-[60vh]">
        <CommandEmpty>No step matches.</CommandEmpty>
        {groupOrder.map((g) => {
          const list = kinds.filter((k) => k.group === g);
          if (!list.length) return null;
          return (
            <CommandGroup key={g} heading={groupText[g]}>
              {list.map((k) => {
                const Icon = iconFor(k.kind);
                const missing = k.requires.filter((r) => r.connected === false);
                return (
                  <CommandItem key={k.kind} value={`${k.label} ${k.kind}`} keywords={[groupText[k.group]]} disabled={missing.length > 0} onSelect={() => onPick(k)} className="items-start">
                    <Icon className="mt-0.5 shrink-0" />
                    <span className="min-w-0">
                      <span className="block text-[13px] font-medium">{k.label}</span>
                      <span className="block text-[12px] text-muted-foreground">{k.description}</span>
                      {missing.map((r) => (
                        <button key={r.connection} type="button" className="pointer-events-auto mt-0.5 block text-[12px] underline" onClick={() => onConnect?.(r.connection)}>Connect {r.label}</button>
                      ))}
                    </span>
                  </CommandItem>
                );
              })}
            </CommandGroup>
          );
        })}
      </CommandList>
    </CommandDialog>
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
