import { useCallback, useEffect, useRef, useState } from "react";
import type { InboxPresence } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { pickedBrandId } from "@/lib/brand";
import { toast } from "../ui";
import { cn } from "@/lib/utils";

// Online and Away, at the top of the Inbox's views column. While someone
// is Online, new website chats come to them; the page checks in every
// minute, so closing it takes them out on its own. Away sends new chats to
// whoever else is online, or tells the visitor the team will answer by
// email.
export function PresenceSwitch({ myId, canManage }: { myId: string; canManage: boolean }) {
  const [p, setP] = useState<InboxPresence | null>(null);
  const status = useRef<string | null>(null);
  const brand = pickedBrandId();

  const load = useCallback(() => {
    api.GET("/v1/brands/{brand}/inbox-presence", { params: { path: { brand } } }).then(({ data }) => {
      if (data) {
        status.current = data.status ?? null;
        setP(data);
      }
    });
  }, [brand]);
  useEffect(load, [load]);

  // The check-in: say "online" again every minute while the page is open,
  // and look at who else is there.
  useEffect(() => {
    const t = setInterval(() => {
      if (status.current === "online") {
        api.PUT("/v1/brands/{brand}/inbox-presence", { params: { path: { brand } }, body: { status: "online" } }).then(({ data }) => data && setP(data));
      } else load();
    }, 60_000);
    return () => clearInterval(t);
  }, [brand, load]);

  async function set(next: "online" | "away") {
    if (p?.status === next) return;
    const { data, error } = await api.PUT("/v1/brands/{brand}/inbox-presence", { params: { path: { brand } }, body: { status: next } });
    if (error || !data) return toast(errorMessage(error));
    status.current = data.status ?? null;
    setP(data);
    toast(next === "online" ? "You're online. New chats come to you." : "You're away. New chats go to whoever is online.");
  }

  if (!p || !myId || !canManage) return null;
  const on = p.status === "online";
  const others = p.online.filter((o) => o.id !== myId);
  return (
    <div className="mb-3 px-2">
      <div role="group" aria-label="Your status for new chats" className="grid grid-cols-2 gap-0.5 rounded-md border bg-background p-0.5 text-xs font-medium">
        <button type="button" aria-pressed={on} onClick={() => set("online")} className={cn("flex items-center justify-center gap-1.5 rounded px-2 py-1", on ? "bg-emerald-500/15 text-emerald-800 dark:text-emerald-300" : "text-muted-foreground hover:bg-accent")}>
          <span aria-hidden className={cn("size-2 rounded-full", on ? "bg-emerald-500" : "border border-muted-foreground/50")} />
          Online
        </button>
        <button type="button" aria-pressed={!on} onClick={() => set("away")} className={cn("flex items-center justify-center gap-1.5 rounded px-2 py-1", !on ? "bg-accent text-foreground" : "text-muted-foreground hover:bg-accent")}>
          <span aria-hidden className={cn("size-2 rounded-full", !on ? "bg-amber-500" : "border border-muted-foreground/50")} />
          Away
        </button>
      </div>
      <p className="mt-1.5 text-[11px] leading-snug text-muted-foreground">
        {others.length
          ? `${others.map((o) => o.name).join(", ")} ${others.length === 1 ? "is" : "are"} ${on ? "also " : ""}online.`
          : on
            ? "New chats come to you."
            : "Nobody is online. Visitors are told you'll reply by email."}
      </p>
    </div>
  );
}
