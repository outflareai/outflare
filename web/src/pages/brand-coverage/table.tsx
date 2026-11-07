// Shared pieces for Presence's tables (Found and Opportunities): sortable
// column headers, on/off filters that remember their setting, compact
// numbers, and loading more rows as you scroll instead of pages.
import { useEffect, useRef, useState, type ReactNode } from "react";

// usePref keeps a filter's setting for this browser. Storage can be
// missing or blocked; then it only lasts the visit.
export function usePref<T extends string | boolean>(key: string, initial: T): [T, (v: T) => void] {
  const [v, setV] = useState<T>(() => {
    try {
      const raw = localStorage.getItem("presence." + key);
      if (raw !== null) return JSON.parse(raw) as T;
    } catch {
      /* not stored */
    }
    return initial;
  });
  const set = (next: T) => {
    setV(next);
    try {
      localStorage.setItem("presence." + key, JSON.stringify(next));
    } catch {
      /* not stored */
    }
  };
  return [v, set];
}

// Toggle is an on/off filter.
export function Toggle({ on, onChange, children, hint }: { on: boolean; onChange: (v: boolean) => void; children: ReactNode; hint?: string }) {
  return (
    <label className="inline-flex cursor-pointer select-none items-center gap-2 text-sm" title={hint}>
      <span
        role="switch"
        aria-checked={on}
        tabIndex={0}
        onKeyDown={(e) => (e.key === " " || e.key === "Enter") && (e.preventDefault(), onChange(!on))}
        onClick={(e) => (e.preventDefault(), onChange(!on))}
        className={`relative h-5 w-9 shrink-0 rounded-full transition-colors ${on ? "bg-brand" : "bg-muted-foreground/30"}`}
      >
        <span className={`absolute top-0.5 size-4 rounded-full bg-background shadow transition-transform ${on ? "translate-x-4" : "translate-x-0.5"}`} />
      </span>
      <span onClick={() => onChange(!on)}>{children}</span>
    </label>
  );
}

// Th is a column header; one with a sort key orders the table by it.
export function Th<S extends string>({ children, sort, by, onSort, className = "", title }: { children: ReactNode; sort?: S; by: S; onSort: (s: S) => void; className?: string; title?: string }) {
  const active = sort !== undefined && sort === by;
  return (
    <th className={`whitespace-nowrap px-3 py-2 text-left text-xs font-medium text-muted-foreground ${className}`} title={title} aria-sort={active ? "descending" : undefined}>
      {sort === undefined ? (
        children
      ) : (
        <button className={`inline-flex items-center gap-1 hover:text-foreground ${active ? "text-foreground" : ""}`} onClick={() => onSort(sort)}>
          {children}
          <span aria-hidden className={active ? "" : "opacity-0"}>
            ↓
          </span>
        </button>
      )}
    </th>
  );
}

// num shows a count or a measurement compactly, or a dash when unknown.
export function num(v: number | null | undefined): string {
  if (v === null || v === undefined) return "–";
  return Intl.NumberFormat(undefined, { notation: "compact", maximumFractionDigits: 1 }).format(v);
}

// useInfinite loads a list a page at a time: the first page whenever the
// filters (deps) change, and the next one when more() is called.
export function useInfinite<R, P extends { data: R[]; next_cursor?: string | null }>(
  load: (cursor: string | undefined) => Promise<{ data?: P; error?: unknown }>,
  deps: unknown[],
  errorMessage: (e: unknown) => string,
) {
  const [rows, setRows] = useState<R[]>([]),
    [page, setPage] = useState<P | null>(null),
    [loading, setLoading] = useState(false),
    [error, setError] = useState(""),
    gen = useRef(0);
  const fetchPage = async (cursor: string | undefined, append: boolean) => {
    const mine = ++gen.current;
    setLoading(true);
    try {
      const { data, error } = await load(cursor);
      if (mine !== gen.current) return;
      if (error || !data) {
        setError(errorMessage(error));
        return;
      }
      setError("");
      setPage(data);
      setRows((old) => (append ? [...old, ...data.data] : data.data));
    } catch (e) {
      if (mine === gen.current) setError(String(e));
    } finally {
      if (mine === gen.current) setLoading(false);
    }
  };
  useEffect(() => {
    const t = setTimeout(() => void fetchPage(undefined, false), 150);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);
  const next = page?.next_cursor ?? null;
  return {
    rows,
    page,
    loading,
    error,
    hasMore: !!next,
    more: () => {
      if (next && !loading) void fetchPage(next, true);
    },
    reload: () => void fetchPage(undefined, false),
  };
}

// MoreOnScroll calls more() when it scrolls into view.
export function MoreOnScroll({ more, hasMore, loading }: { more: () => void; hasMore: boolean; loading: boolean }) {
  const ref = useRef<HTMLDivElement>(null);
  const cb = useRef(more);
  cb.current = more;
  useEffect(() => {
    const el = ref.current;
    if (!el || !hasMore || typeof IntersectionObserver === "undefined") return;
    const o = new IntersectionObserver((es) => es.some((e) => e.isIntersecting) && cb.current(), { rootMargin: "400px" });
    o.observe(el);
    return () => o.disconnect();
  }, [hasMore]);
  if (!hasMore && !loading) return null;
  return (
    <div ref={ref} className="py-3 text-center text-xs text-muted-foreground">
      {loading ? "Loading…" : <button onClick={more}>Load more</button>}
    </div>
  );
}
