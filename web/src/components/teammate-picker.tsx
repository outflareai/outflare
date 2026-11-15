// One way to pick a teammate everywhere: a searchable list of the team
// with their photos, you first. It lists the workspace's members and the
// people in the team directory who have no sign-in, so a record can belong
// to someone who never opens Outflare.

import { useEffect, useState, type ReactNode } from "react";
import { Check, ChevronsUpDown, UserX } from "lucide-react";
import type { Schemas } from "@outflare/sdk";
import { api } from "../api";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command";

/** Someone a record can be given to: a member (id is their user id) or a directory person with no sign-in (id is their directory id). */
export type Teammate = { id: string; name: string; email: string; avatar_url?: string | null; title?: string; signIn: boolean; brand_ids?: string[]; role?: string; /** Their directory id, which records given to them before they had a sign-in still carry. */ aka?: string };

let cache: { ws: string; me: string; people: Teammate[] } | null = null;

/** The team and who is signed in, fetched once per workspace. `brand` keeps members to those who work on it. */
export function useTeammates(ws: string, brand?: string): { me: string; people: Teammate[] } {
  const [team, setTeam] = useState(cache?.ws === ws ? cache : null);
  useEffect(() => {
    if (cache?.ws === ws) return setTeam(cache);
    let live = true;
    void Promise.all([
      api.GET("/v1/workspaces/{ws}/members", { params: { path: { ws } } }),
      api.GET("/v1/me"),
      // Not everyone may read the directory: without it the list is the members.
      api.GET("/v1/workspaces/{ws}/staff", { params: { path: { ws }, query: { status: "active" } } }).catch(() => ({ data: undefined })),
    ]).then(([m, me, st]) => {
      const members = ((m.data?.data ?? []) as Schemas["Member"][])
        .filter((x) => !!x.user_id)
        .map((x): Teammate => ({ id: x.user_id!, name: x.name || x.email || "Team member", email: x.email ?? "", avatar_url: x.avatar_url, signIn: true, brand_ids: x.brand_ids ?? [], role: x.role }));
      const staff = ((st.data as { data?: Schemas["StaffMember"][] } | undefined)?.data ?? []) as Schemas["StaffMember"][];
      const linked = new Map(staff.filter((p) => p.user_id).map((p) => [p.user_id!, p]));
      for (const p of members) {
        p.title = linked.get(p.id)?.title || undefined;
        p.aka = linked.get(p.id)?.id;
      }
      const offline = staff.filter((p) => !p.user_id).map((p): Teammate => ({ id: p.id, name: p.name, email: p.email ?? "", title: p.title || undefined, signIn: false }));
      cache = { ws, me: (me.data as { user?: { id?: string } } | undefined)?.user?.id ?? "", people: [...members, ...offline] };
      if (live) setTeam(cache);
    });
    return () => {
      live = false;
    };
  }, [ws]);
  const t = team ?? { me: "", people: [] };
  if (!brand) return t;
  // Owners and admins see every brand; others only the brands they work on.
  return { me: t.me, people: t.people.filter((p) => !p.signIn || p.role === "owner" || p.role === "admin" || !p.brand_ids?.length || p.brand_ids.includes(brand)) };
}

/** Everyone's name by the ids a record can carry for them. */
export function teammateNames(people: Teammate[]): Record<string, string> {
  const names: Record<string, string> = {};
  for (const p of people) {
    names[p.id] = p.name;
    if (p.aka) names[p.aka] = p.name;
  }
  return names;
}

/** Forget the cached team, after the directory or the members change. */
export function forgetTeammates() {
  cache = null;
}

const palette = ["bg-sky-600", "bg-emerald-600", "bg-amber-600", "bg-rose-600", "bg-violet-600", "bg-teal-600", "bg-orange-600", "bg-indigo-600"];

function initials(name: string): string {
  const words = name.trim().split(/[\s@._-]+/).filter(Boolean);
  return ((words[0]?.[0] ?? "?") + (words.length > 1 ? words[words.length - 1][0] : "")).toUpperCase();
}

/** A person's profile photo, or their initials on a colour of their own. */
export function Avatar({ name, url, id, size = "size-6" }: { name: string; url?: string | null; id?: string | null; size?: string }) {
  const [broken, setBroken] = useState(false);
  if (url && !broken)
    return <img src={url} alt="" referrerPolicy="no-referrer" onError={() => setBroken(true)} className={`${size} shrink-0 rounded-full object-cover`} />;
  const seed = [...(id || name)].reduce((n, c) => n + c.charCodeAt(0), 0);
  return (
    <span aria-hidden className={`${size} ${palette[seed % palette.length]} inline-flex shrink-0 items-center justify-center rounded-full text-[10px] font-medium text-white`}>
      {initials(name)}
    </span>
  );
}

function Nobody() {
  return (
    <span aria-hidden className="inline-flex size-6 shrink-0 items-center justify-center rounded-full border border-dashed border-muted-foreground/50 text-muted-foreground">
      <UserX className="size-3" />
    </span>
  );
}

/** You first, then everyone else by name. */
export function sortTeammates(people: Teammate[], me: string): Teammate[] {
  return [...people].sort((a, b) => (a.id === me ? -1 : b.id === me ? 1 : a.name.localeCompare(b.name)));
}

/**
 * A button that shows who has something and opens the team to change it.
 * With `trigger`, it is an action ("Assign to…") instead of a current value.
 */
export function TeammatePicker({
  value,
  people,
  me,
  onPick,
  disabled,
  canChange = true,
  noneLabel = "Unassigned",
  allowNone = true,
  fallbackName,
  fallbackAvatar,
  label = "Assigned to",
  trigger,
  className = "max-w-56",
}: {
  value: string | null;
  people: Teammate[];
  me: string;
  onPick: (id: string | null) => void;
  disabled?: boolean;
  canChange?: boolean;
  noneLabel?: string;
  allowNone?: boolean;
  /** The name to show when the current person isn't in `people` (someone who left). */
  fallbackName?: string | null;
  fallbackAvatar?: string | null;
  label?: string;
  trigger?: ReactNode;
  className?: string;
}) {
  const [open, setOpen] = useState(false);
  const current = value ? people.find((p) => p.id === value || p.aka === value) : undefined;
  const name = current?.name || fallbackName || (value ? "A former teammate" : "");
  const face = value ? (
    <>
      <Avatar name={name} url={current?.avatar_url ?? fallbackAvatar} id={value} />
      <span className="min-w-0 truncate">{(current?.id ?? value) === me ? `${name} (you)` : name}</span>
    </>
  ) : (
    <>
      <Nobody />
      <span className="text-muted-foreground">{noneLabel}</span>
    </>
  );
  if (!canChange) return <div className="flex min-w-0 items-center gap-2 text-[13px]">{face}</div>;
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        {trigger ? (
          <button type="button" disabled={disabled} className="inline-flex h-7 items-center gap-1.5 rounded-md border bg-background px-2.5 text-xs font-medium hover:bg-muted disabled:opacity-50">
            {trigger}
            <ChevronsUpDown className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
          </button>
        ) : (
          <button
            type="button"
            disabled={disabled}
            aria-label={`${label}: ${value ? name : "nobody"}. Change`}
            className={`-mx-1.5 inline-flex ${className} min-w-0 items-center gap-2 rounded-md px-1.5 py-1 text-left text-[13px] hover:bg-muted disabled:opacity-50`}
          >
            {face}
            <ChevronsUpDown className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
          </button>
        )}
      </PopoverTrigger>
      <PopoverContent align="start" className="w-72 p-0">
        <Command>
          <CommandInput placeholder="Search the team…" />
          <CommandList>
            <CommandEmpty>Nobody by that name.</CommandEmpty>
            <CommandGroup>
              {allowNone && (
                <CommandItem
                  value="unassigned nobody none"
                  onSelect={() => {
                    setOpen(false);
                    onPick(null);
                  }}
                >
                  <Nobody />
                  <span className="flex-1">{noneLabel}</span>
                  {!value && !trigger && <Check className="size-3.5" aria-hidden />}
                </CommandItem>
              )}
              {sortTeammates(people, me).map((p) => (
                <CommandItem
                  key={p.id}
                  value={`${p.name} ${p.email} ${p.title ?? ""} ${p.id}`}
                  onSelect={() => {
                    setOpen(false);
                    onPick(p.id);
                  }}
                >
                  <Avatar name={p.name} url={p.avatar_url} id={p.id} />
                  <span className="min-w-0 flex-1">
                    <span className="block truncate">
                      {p.name}
                      {p.id === me && <span className="text-muted-foreground"> (you)</span>}
                    </span>
                    {(p.title || !p.signIn) && (
                      <span className="block truncate text-[11px] text-muted-foreground">{[p.title, !p.signIn && "no sign-in"].filter(Boolean).join(" · ")}</span>
                    )}
                  </span>
                  {(p.id === value || (!!p.aka && p.aka === value)) && !trigger && <Check className="size-3.5" aria-hidden />}
                </CommandItem>
              ))}
            </CommandGroup>
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}
