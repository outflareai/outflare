// The step picker: a searchable catalog of step kinds, grouped as in the
// actions plan (Messaging, Data, Cross-campaign, Notify, Integrations, AI,
// Flow control). A kind that needs a connection the workspace hasn't made
// is greyed out with a link to make it.

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

// useStepKinds is the step kinds a journey can use. Until the API serves
// its catalog (GET .../journeys/step-kinds, with which connections each
// kind needs), it is today's four.
export function useStepKinds(_ws: string): StepKind[] {
  return builtinKinds;
}
