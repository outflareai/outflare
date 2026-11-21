import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "../api";
import { pickedBrandId } from "@/lib/brand";

type Viewer = { id: string; name: string; typing: boolean };

// Who else has this conversation open. The page asks every 15 seconds
// while it is open, which is also what tells the others this person is
// here; typing() says they are writing, at most once every few seconds.
export function useViewers(kind: "reply" | "support", id: string, on: boolean) {
  const [viewers, setViewers] = useState<Viewer[]>([]);
  const typedAt = useRef(0);
  const saidAt = useRef(0);
  const brand = pickedBrandId();

  const ask = useCallback(
    (typing: boolean) => {
      if (!on || !brand || !id) return;
      if (typing) saidAt.current = Date.now();
      api.GET("/v1/brands/{brand}/inbox-viewing", { params: { path: { brand }, query: { kind, id, typing } } }).then(({ data }) => data && setViewers(data.viewers));
    },
    [on, brand, kind, id],
  );

  useEffect(() => {
    setViewers([]);
    typedAt.current = 0;
    saidAt.current = 0;
    ask(false);
    const t = setInterval(() => {
      if (document.visibilityState === "visible") ask(Date.now() - typedAt.current < 8000);
    }, 15_000);
    return () => clearInterval(t);
  }, [ask]);

  const typing = useCallback(() => {
    typedAt.current = Date.now();
    if (Date.now() - saidAt.current > 5000) ask(true);
  }, [ask]);

  return { viewers, typing };
}

function names(list: Viewer[]) {
  const n = list.map((v) => v.name);
  return n.length <= 2 ? n.join(" and ") : `${n.slice(0, -1).join(", ")} and ${n[n.length - 1]}`;
}

// The warning above the reply box: someone else is here, or is writing.
export function ViewingNow({ viewers }: { viewers: Viewer[] }) {
  if (!viewers.length) return null;
  const writing = viewers.filter((v) => v.typing);
  const looking = viewers.filter((v) => !v.typing);
  return (
    <div role="status" className="flex items-center gap-2 border-t border-amber-500/30 bg-amber-50 px-4 py-2 text-[13px] text-amber-900 dark:bg-amber-500/10 dark:text-amber-200">
      <span aria-hidden className={writing.length ? "size-2 animate-pulse rounded-full bg-amber-500" : "size-2 rounded-full bg-amber-500"} />
      <span>
        {writing.length > 0 && `${names(writing)} ${writing.length === 1 ? "is" : "are"} writing here right now. Check with them before you answer.`}
        {writing.length > 0 && looking.length > 0 && " "}
        {looking.length > 0 && `${names(looking)} ${looking.length === 1 ? "has" : "have"} this open too.`}
      </span>
    </div>
  );
}
