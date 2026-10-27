import { useEffect, useState } from "react";
import type { components } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Button, toast } from "../ui";
import { Icon } from "../icons";
import { cn } from "@/lib/utils";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command";

type Catalog = components["schemas"]["BusinessModels"];

let cached: Promise<Catalog | undefined> | undefined;
function useBusinessModels() {
  const [list, setList] = useState<Catalog | null>(null);
  useEffect(() => {
    cached ??= api.GET("/v1/business-models").then(({ data }) => data);
    void cached.then((d) => setList(d ?? null));
  }, []);
  return list;
}

// What a brand does, from a searchable grouped list, and how its customers
// pay. Saved on the brand at once; either can change any time.
export function BusinessModelPicker({ ws, brand, model, revenue, canManage, onSaved }: { ws: string; brand: string; model: string | null; revenue: string[]; canManage: boolean; onSaved: () => void }) {
  const list = useBusinessModels();
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  if (!list) return null;
  const current = list.models.find((m) => m.id === model);

  async function save(body: { business_model?: string; revenue_models?: string[] }) {
    setBusy(true);
    const { error } = await api.PATCH("/v1/workspaces/{ws}/brands/{brand}", { params: { path: { ws, brand } }, body });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    onSaved();
  }

  return (
    <div className="space-y-4">
      <div className="space-y-1.5">
        <p className="text-[13px] font-medium">What does this brand do?</p>
        <Popover open={open} onOpenChange={setOpen}>
          <PopoverTrigger asChild>
            <button
              type="button"
              disabled={!canManage || busy}
              aria-label="What does this brand do?"
              className="flex h-9 w-full max-w-md items-center justify-between rounded-md border bg-background px-3 text-left text-[13px] shadow-xs hover:bg-accent disabled:opacity-60"
            >
              <span className={cn(!current && "text-muted-foreground")}>{current ? current.label : "Pick the closest one"}</span>
              <Icon name="updown" className="size-3.5 text-muted-foreground" />
            </button>
          </PopoverTrigger>
          <PopoverContent className="w-(--radix-popover-trigger-width) min-w-72 p-0" align="start">
            <Command>
              <CommandInput placeholder="Search, such as SaaS or store" />
              <CommandList className="max-h-80">
                <CommandEmpty>Nothing matches. Pick Other.</CommandEmpty>
                {list.groups.map((g) => (
                  <CommandGroup key={g.id} heading={g.label}>
                    {list.models.filter((m) => m.group === g.id).map((m) => (
                      <CommandItem
                        key={m.id}
                        value={`${m.label} ${g.label}`}
                        onSelect={() => {
                          setOpen(false);
                          if (m.id !== model) void save({ business_model: m.id });
                        }}
                      >
                        <span className="flex-1">{m.label}</span>
                        {m.id === model && <Icon name="check" className="size-3.5" />}
                      </CommandItem>
                    ))}
                  </CommandGroup>
                ))}
              </CommandList>
            </Command>
          </PopoverContent>
        </Popover>
      </div>
      <fieldset className="space-y-1.5">
        <legend className="text-[13px] font-medium">How do customers pay? <span className="font-normal text-muted-foreground">Optional, pick any</span></legend>
        <div className="flex flex-wrap gap-1.5 pt-1.5">
          {list.revenue_models.map((r) => {
            const on = revenue.includes(r.id);
            return (
              <Button
                key={r.id}
                variant="ghost"
                aria-pressed={on}
                disabled={!canManage || busy}
                className={cn(on && "border-brand/50 bg-brand/10")}
                onClick={() => save({ revenue_models: on ? revenue.filter((x) => x !== r.id) : [...revenue, r.id] })}
              >
                {on && <Icon name="check" className="size-3.5" />}
                {r.label}
              </Button>
            );
          })}
        </div>
      </fieldset>
    </div>
  );
}
