import { useEffect, useRef, useState, type MouseEvent, type ReactNode } from "react";
import { cn } from "@/lib/utils";

// What a row in the Inbox offers: the same list opens on a right-click
// and from the row's "More" button, so nothing is reachable only by
// right-clicking. A null draws a line between groups.
// An action with items opens them in the same menu (Assign to, Move to
// team, Loop in), with a way back.
export type RowAction = { label: string; onClick?: () => void; items?: RowAction[]; danger?: boolean; hint?: string } | null | false;

type At = { x: number; y: number; items: RowAction[]; back?: RowAction[]; title?: string };

export function useRowMenu() {
  const [at, setAt] = useState<At | null>(null);
  const box = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!at) return;
    const close = (e: Event) => {
      if (e instanceof KeyboardEvent ? e.key === "Escape" : !box.current?.contains(e.target as Node)) setAt(null);
    };
    window.addEventListener("mousedown", close);
    window.addEventListener("keydown", close);
    window.addEventListener("scroll", close, true);
    return () => {
      window.removeEventListener("mousedown", close);
      window.removeEventListener("keydown", close);
      window.removeEventListener("scroll", close, true);
    };
  }, [at]);
  // Keep the menu on the screen when the row is near an edge.
  useEffect(() => {
    const el = box.current;
    if (!el || !at) return;
    const r = el.getBoundingClientRect();
    const x = Math.min(at.x, window.innerWidth - r.width - 8);
    const y = Math.min(at.y, window.innerHeight - r.height - 8);
    if (x !== at.x || y !== at.y) setAt({ ...at, x: Math.max(8, x), y: Math.max(8, y) });
    el.querySelector<HTMLElement>("[role=menuitem]")?.focus();
  }, [at]);

  function open(e: Pick<MouseEvent, "preventDefault" | "stopPropagation" | "clientX" | "clientY">, items: RowAction[]) {
    e.preventDefault();
    e.stopPropagation();
    setAt({ x: e.clientX, y: e.clientY, items: items.filter((i) => i !== false) });
  }
  const shown = at?.items ?? [];
  const menu: ReactNode = at && (
    <div ref={box} role="menu" aria-label="Conversation actions" style={{ left: at.x, top: at.y }} className="fixed z-50 min-w-52 rounded-lg border bg-popover py-1 text-[13px] text-popover-foreground shadow-xl">
      {at.back && (
        <button type="button" role="menuitem" className="flex h-8 w-full items-center gap-2 border-b px-3 text-left text-muted-foreground hover:bg-accent focus:bg-accent focus:outline-none" onClick={() => setAt({ x: at.x, y: at.y, items: at.back! })}>
          <span aria-hidden>←</span>
          <span>{at.title}</span>
        </button>
      )}
      {at.back && shown.length === 0 && <p className="px-3 py-2 text-muted-foreground">Nothing to choose from yet.</p>}
      {shown.map((i, n) =>
        !i ? (
          n > 0 && n < shown.length - 1 && <div key={n} className="my-1 border-t" />
        ) : (
          <button
            key={i.label}
            type="button"
            role="menuitem"
            className={cn("flex h-8 w-full items-center justify-between gap-6 px-3 text-left hover:bg-accent focus:bg-accent focus:outline-none", i.danger && "text-red-600 dark:text-red-400")}
            aria-haspopup={i.items ? "menu" : undefined}
            onClick={() => {
              if (i.items) return setAt({ x: at.x, y: at.y, items: i.items.filter((x) => x !== false), back: at.items, title: i.label });
              setAt(null);
              i.onClick?.();
            }}
            onKeyDown={(e) => {
              const all = [...(box.current?.querySelectorAll<HTMLElement>("[role=menuitem]") ?? [])];
              const me = all.indexOf(e.currentTarget);
              if (e.key === "ArrowDown") all[(me + 1) % all.length]?.focus();
              if (e.key === "ArrowUp") all[(me - 1 + all.length) % all.length]?.focus();
            }}
          >
            <span>{i.label}</span>
            {i.hint && <span className="text-[11px] text-muted-foreground">{i.hint}</span>}
            {i.items && <span aria-hidden className="text-muted-foreground">›</span>}
          </button>
        ),
      )}
    </div>
  );
  return { open, menu };
}

// The button on a row that opens its actions without a right-click.
export function RowMore({ onOpen, name }: { onOpen: (e: MouseEvent) => void; name: string }) {
  return (
    <span
      role="button"
      tabIndex={0}
      aria-label={`More actions for ${name}`}
      title="More actions (or right-click)"
      onClick={onOpen}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          const r = e.currentTarget.getBoundingClientRect();
          onOpen({ preventDefault: () => e.preventDefault(), stopPropagation: () => e.stopPropagation(), clientX: r.left, clientY: r.bottom } as unknown as MouseEvent);
        }
      }}
      className="rounded px-1 text-muted-foreground opacity-0 group-hover:opacity-100 hover:bg-background hover:text-foreground focus:opacity-100"
    >
      ⋯
    </span>
  );
}

// The bar that replaces a list's heading while rows are ticked.
export function BulkBar({ count, onClear, actions }: { count: number; onClear: () => void; actions: { label: string; onClick: (e: MouseEvent) => void }[] }) {
  return (
    <div role="toolbar" aria-label="Actions for the selected conversations" className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b bg-accent px-3 py-2 text-[12px]">
      <span className="font-medium">{count} selected</span>
      {actions.map((a) => <button key={a.label} type="button" className="font-medium hover:underline" onClick={a.onClick}>{a.label}</button>)}
      <button type="button" className="ml-auto text-muted-foreground hover:text-foreground" onClick={onClear}>Clear</button>
    </div>
  );
}
