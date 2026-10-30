// PersonPicker searches the workspace's people by name or email and picks
// one: for testing a journey with someone, or seeing what reaches them on
// the automation map.

import { useEffect, useState } from "react";
import { Search } from "lucide-react";
import type { Schemas } from "@outflare/sdk";
import { api } from "../api";
import { cn } from "@/lib/utils";
import { Command, CommandEmpty, CommandInput, CommandItem, CommandList } from "@/components/ui/command";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";

export type Person = { id: string; name: string; email?: string };

function usePeople(ws: string, q: string) {
  const [list, setList] = useState<Person[] | null>(null);
  useEffect(() => {
    let live = true;
    const t = setTimeout(async () => {
      const { data } = await api.GET("/v1/workspaces/{ws}/records", { params: { path: { ws }, query: { type: "person", limit: 8, ...(q.trim() ? { q: q.trim() } : {}) } } });
      if (live) setList((data?.data ?? []).map((r: Schemas["Record"]) => ({ id: r.id, name: r.name, email: r.identities.find((i) => i.namespace === "email")?.value })));
    }, q ? 250 : 0);
    return () => { live = false; clearTimeout(t); };
  }, [ws, q]);
  return list;
}

export function PersonPicker({ ws, person, onPick, startOpen = !person }: { ws: string; person: Person | null; onPick: (p: Person) => void; startOpen?: boolean }) {
  const [open, setOpen] = useState(startOpen);
  const [q, setQ] = useState("");
  const people = usePeople(ws, open ? q : "");
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button type="button" className="flex h-8 min-w-0 max-w-full items-center gap-1.5 rounded-md border bg-background px-2.5 text-[13px] shadow-xs hover:border-ring/60">
          <Search className="size-3.5 shrink-0 text-muted-foreground" />
          <span className={cn("truncate", !person && "text-muted-foreground")}>{person ? person.name || person.email : "Pick a person"}</span>
        </button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-80 p-0">
        <Command shouldFilter={false}>
          <CommandInput placeholder="Search people by name or email" value={q} onValueChange={setQ} />
          <CommandList>
            <CommandEmpty>{people ? "No one matches." : "Searching…"}</CommandEmpty>
            {(people ?? []).map((p) => (
              <CommandItem key={p.id} value={p.id} onSelect={() => { onPick(p); setOpen(false); }}>
                <span className="min-w-0">
                  <span className="block truncate text-[13px]">{p.name || p.email || "Unnamed"}</span>
                  {p.email && p.name && <span className="block truncate text-[12px] text-muted-foreground">{p.email}</span>}
                </span>
              </CommandItem>
            ))}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}
