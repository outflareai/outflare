import { useCallback, useEffect, useRef, useState } from "react";
import type { InboxLoopIn } from "@outflare/sdk";
import { api } from "../api";
import { pickedBrandId } from "@/lib/brand";
import { alertNow } from "@/lib/notify";
import { toastAction } from "../ui";

// What a teammate looped this person in on and they haven't opened yet.
// The Inbox asks every half minute; a new one shows a message with an Open
// button (and the chime, when alerts are on), and counts on the Watching
// views until it is opened.
export function useLoopIns(myId: string, open: (item: string) => void) {
  const [list, setList] = useState<InboxLoopIn[]>([]);
  const known = useRef<Set<string> | null>(null);
  const openRef = useRef(open);
  openRef.current = open;
  const brand = pickedBrandId();

  const load = useCallback(() => {
    if (!myId || !brand) return;
    api.GET("/v1/brands/{brand}/inbox-loop-ins", { params: { path: { brand } } }).then(({ data }) => {
      if (!data) return;
      const first = known.current === null;
      const seen = known.current ?? new Set<string>();
      for (const l of data.loop_ins) {
        const key = `${l.kind}:${l.id}`;
        if (seen.has(key)) continue;
        seen.add(key);
        if (first) continue;
        const item = l.kind === "support" ? `support-${l.id}` : l.id;
        const text = `${l.by?.name ?? "A teammate"} looped you in${l.name ? ` on ${l.name}` : ""}.`;
        toastAction(text, "Open", () => openRef.current(item));
        alertNow("You were looped in", text, () => openRef.current(item));
      }
      known.current = seen;
      setList(data.loop_ins);
    });
  }, [myId, brand]);

  useEffect(() => {
    known.current = null;
    load();
    const t = setInterval(load, 30_000);
    return () => clearInterval(t);
  }, [load]);

  return { support: list.filter((l) => l.kind === "support").length, replies: list.filter((l) => l.kind === "reply").length, reload: load };
}

// The count of new loop-ins beside a Watching view.
export function NewCount({ n }: { n: number }) {
  if (!n) return null;
  return (
    <span title={`${n} new: a teammate looped you in`} className="rounded-full bg-blue-500/15 px-1.5 text-[11px] font-medium tabular-nums text-blue-700 dark:text-blue-300">
      {n} new
    </span>
  );
}
