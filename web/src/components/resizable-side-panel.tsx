import { useCallback, useEffect, useState, type CSSProperties, type KeyboardEvent as ReactKeyboardEvent, type PointerEvent as ReactPointerEvent, type ReactNode } from "react";

const defaultWidth = 416, minWidth = 360, maxWidth = 960;
// What the page beside the panel keeps: the sidebar and room to work in.
const pageKeeps = 760;

const clampWidth = (w: number) => Math.round(Math.max(minWidth, Math.min(w, maxWidth, window.innerWidth - pageKeeps)));

/**
 * usePanelWidth is how wide a panel on the right edge is: what the person
 * dragged it to, remembered in this browser under storageKey so it is the
 * same on every page, and never so wide that the page beside it is crushed
 * (a narrower window narrows the panel).
 */
export function usePanelWidth(storageKey: string): [number, (w: number, done?: boolean) => void] {
  // What the person chose. The panel is this wide whenever the window has room.
  const [chosen, setChosen] = useState(() => {
    try {
      const v = Number(localStorage.getItem(storageKey));
      return Number.isFinite(v) && v >= minWidth ? v : defaultWidth;
    } catch {
      return defaultWidth;
    }
  });
  const [, setWindowWidth] = useState(() => window.innerWidth);
  useEffect(() => {
    const fit = () => setWindowWidth(window.innerWidth);
    window.addEventListener("resize", fit);
    return () => window.removeEventListener("resize", fit);
  }, []);
  const setWidth = useCallback(
    (w: number, done = true) => {
      const next = clampWidth(w);
      setChosen(next);
      if (!done) return;
      try {
        localStorage.setItem(storageKey, String(next));
      } catch {
        // Remembering it is a convenience.
      }
    },
    [storageKey],
  );
  return [clampWidth(chosen), setWidth];
}

/**
 * ResizableSidePanel is a panel fixed to the right edge. On a wide screen
 * its left edge is a handle: drag it, or focus it and use the arrow keys;
 * double-click puts it back. On a narrow screen the panel lies over the
 * page at its usual width and the handle is gone.
 */
export function ResizableSidePanel({ width, onWidth, onResized, label, className = "", children }: { width: number; onWidth: (w: number, done?: boolean) => void; onResized?: (w: number) => void; label: string; className?: string; children: ReactNode }) {
  const [dragging, setDragging] = useState(false);
  const finish = (w: number) => {
    onWidth(w);
    onResized?.(clampWidth(w));
  };
  const down = (e: ReactPointerEvent<HTMLDivElement>) => {
    e.preventDefault();
    e.currentTarget.setPointerCapture(e.pointerId);
    setDragging(true);
  };
  const move = (e: ReactPointerEvent<HTMLDivElement>) => {
    if (dragging) onWidth(window.innerWidth - e.clientX, false);
  };
  const up = (e: ReactPointerEvent<HTMLDivElement>) => {
    if (!dragging) return;
    setDragging(false);
    finish(window.innerWidth - e.clientX);
  };
  const key = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    const step = e.key === "ArrowLeft" ? 32 : e.key === "ArrowRight" ? -32 : 0;
    if (!step && e.key !== "Home") return;
    e.preventDefault();
    finish(e.key === "Home" ? defaultWidth : width + step);
  };
  // While dragging, nothing on the page selects and the cursor stays a resize one.
  useEffect(() => {
    if (!dragging) return;
    const { userSelect, cursor } = document.body.style;
    document.body.style.userSelect = "none";
    document.body.style.cursor = "col-resize";
    return () => {
      document.body.style.userSelect = userSelect;
      document.body.style.cursor = cursor;
    };
  }, [dragging]);
  return (
    <div style={{ "--panel-w": `${width}px` } as CSSProperties} className={`fixed inset-y-0 right-0 z-30 w-full max-w-[26rem] xl:w-(--panel-w) xl:max-w-none ${className}`}>
      <div
        role="separator"
        aria-orientation="vertical"
        aria-label={label}
        aria-valuenow={width}
        aria-valuemin={minWidth}
        aria-valuemax={maxWidth}
        tabIndex={0}
        title="Drag to resize. Double-click to reset."
        onPointerDown={down}
        onPointerMove={move}
        onPointerUp={up}
        onPointerCancel={up}
        onDoubleClick={() => finish(defaultWidth)}
        onKeyDown={key}
        className="group absolute inset-y-0 -left-1.5 z-10 hidden w-3 cursor-col-resize touch-none items-center justify-center outline-none xl:flex"
      >
        <span className={`h-full w-0.5 transition-colors ${dragging ? "bg-primary" : "bg-transparent group-hover:bg-muted-foreground/50 group-focus-visible:bg-primary"}`} />
        <span className={`absolute h-10 w-1 rounded-full transition-colors ${dragging ? "bg-primary" : "bg-border group-hover:bg-muted-foreground/70 group-focus-visible:bg-primary"}`} />
      </div>
      {children}
    </div>
  );
}
