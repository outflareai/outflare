// SubSourcePicker picks where within their Source someone came from: for
// Events and conferences, the event or conference they were met at. It is a
// searchable list of the brand's sub-sources of that Source, each with how
// many people have it, and typing a name that isn't there adds it.

import { useCallback, useEffect, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { Check, ChevronsUpDown, Plus, X } from "lucide-react";
import { api, errorMessage } from "@/api";
import { field, toast } from "@/ui";
import { Command, CommandEmpty, CommandInput, CommandItem, CommandList } from "@/components/ui/command";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { catalogChanged } from "@/lib/records";

export type SubSource = Schemas["SubSource"];

// What a sub-source is called for each Source, in the picker's words.
export const subSourceNoun = (source?: string | null) =>
  source === "event" ? "event or conference" : source === "inbound" ? "inbound channel" : "sub-source";

// useSubSources is the brand's sub-sources of a Source (all of them without
// one), with a reload.
export function useSubSources(brand: string | undefined, source?: string | null) {
  const [list, setList] = useState<SubSource[] | null>(null);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    if (!brand) return;
    const { data, error } = await api.GET("/v1/brands/{brand}/sub-sources", {
      params: { path: { brand }, query: source ? { source: source as SubSource["source"] } : {} },
    });
    if (data) setList(data.data);
    else setError(errorMessage(error));
  }, [brand, source]);
  useEffect(() => {
    setList(null);
    void load();
  }, [load]);
  return { list, error, reload: load };
}

export function SubSourcePicker({
  id,
  brand,
  source,
  value,
  onChange,
  disabled,
}: {
  id?: string;
  brand: string;
  source?: string | null;
  value: string | null | undefined;
  onChange: (key: string | null, picked?: SubSource) => void;
  disabled?: boolean;
}) {
  const { list, reload } = useSubSources(brand, source);
  const [open, setOpen] = useState(false);
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  const noun = subSourceNoun(source);
  const picked = list?.find((s) => s.key === value);
  const exact = list?.some((s) => s.label.toLowerCase() === typed.trim().replace(/\s+/g, " ").toLowerCase());

  async function add() {
    const label = typed.trim();
    if (!label || !source) return;
    setBusy(true);
    const { data, error } = await api.POST("/v1/brands/{brand}/sub-sources", { params: { path: { brand } }, body: { source: source as SubSource["source"], label } });
    setBusy(false);
    if (!data) return toast(errorMessage(error), "error");
    await reload();
    window.dispatchEvent(new Event(catalogChanged));
    onChange(data.key, data);
    setTyped("");
    setOpen(false);
  }

  if (!source)
    return (
      <button type="button" id={id} disabled className={`${field} w-full text-left text-muted-foreground`}>
        Pick a Source first
      </button>
    );
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button
          type="button"
          id={id}
          role="combobox"
          aria-expanded={open}
          disabled={disabled}
          className={`${field} flex w-full items-center justify-between gap-2 text-left`}
        >
          <span className={picked ? "" : "text-muted-foreground"}>{picked?.label ?? (list === null && value ? "Loading…" : `Pick or type the ${noun}…`)}</span>
          <ChevronsUpDown className="size-3.5 shrink-0 text-muted-foreground" />
        </button>
      </PopoverTrigger>
      <PopoverContent className="w-[min(24rem,90vw)] p-0" align="start">
        <Command>
          <CommandInput
            placeholder={`Search or type a new ${noun}…`}
            value={typed}
            onValueChange={setTyped}
            onKeyDown={(e) => {
              if (e.key === "Enter" && typed.trim() && !exact && !list?.some((s) => s.label.toLowerCase().includes(typed.trim().toLowerCase()))) {
                e.preventDefault();
                void add();
              }
            }}
          />
          <CommandList>
            <CommandEmpty>{list === null ? "Loading…" : typed.trim() ? null : `No ${noun}s yet. Type a name to add one.`}</CommandEmpty>
            {(list ?? []).map((s) => (
              <CommandItem
                key={s.key}
                value={s.key}
                keywords={[s.label]}
                onSelect={() => {
                  onChange(s.key, s);
                  setOpen(false);
                }}
              >
                <span className="min-w-0 flex-1 truncate">{s.label}</span>
                <span className="text-xs text-muted-foreground tabular-nums">{s.people.toLocaleString()} {s.people === 1 ? "person" : "people"}</span>
                {s.key === value && <Check className="size-3.5" />}
              </CommandItem>
            ))}
            {typed.trim() && !exact && (
              <CommandItem value={`__add ${typed}`} keywords={[typed]} disabled={busy} onSelect={() => void add()}>
                <Plus className="size-3.5" />
                Add “{typed.trim()}” as a new {noun}
              </CommandItem>
            )}
            {value && (
              <CommandItem
                value="__clear"
                onSelect={() => {
                  onChange(null);
                  setOpen(false);
                }}
              >
                <X className="size-3.5" />
                Clear
              </CommandItem>
            )}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}
