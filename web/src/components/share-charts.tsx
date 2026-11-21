import { useEffect, useMemo, useState, type ReactNode } from "react";
import { HoverCard, seriesColor, Swatch, useWidth } from "./chart-kit";

// Charts of totals rather than time: horizontal bars, a ring of shares
// and a world map. Each takes named amounts, biggest first.

export type Part = { label: string; value: number | null; currency?: string };
type Format = (v: number | null | undefined, currency?: string) => string;

const pct = (v: number, total: number) => (total > 0 ? `${((v / total) * 100).toFixed(v / total < 0.1 ? 1 : 0)}%` : "");

// HBars lists each part as a bar across, with its number and its share
// of the whole at the end.
// With oneColor every bar takes the first series colour: for parts that
// are steps of one scale (spans of time), not groups to tell apart.
export function HBars({ parts, format, compact, oneColor }: { parts: Part[]; format: Format; compact?: boolean; oneColor?: boolean }) {
  const shown = parts.filter((p) => typeof p.value === "number");
  const max = Math.max(0, ...shown.map((p) => p.value as number));
  const total = shown.reduce((a, p) => a + Math.max(0, p.value as number), 0);
  return (
    <ul className={`space-y-2 ${compact ? "text-[12px]" : "text-[13px]"}`} role="list" aria-label="Totals by group">
      {shown.map((p, i) => (
        <li key={p.label + i} className="grid grid-cols-[minmax(80px,30%)_minmax(0,1fr)_auto] items-center gap-3">
          <span className="truncate text-muted-foreground" title={p.label}>{p.label}</span>
          <span className="h-5 rounded bg-muted/60">
            <span className="block h-full rounded" style={{ width: `${max > 0 && (p.value as number) > 0 ? Math.max(1, ((p.value as number) / max) * 100) : 0}%`, background: seriesColor(oneColor ? 0 : i % 6), opacity: oneColor || i < 6 ? 1 : 0.6 }} />
          </span>
          <span className="whitespace-nowrap text-right tabular-nums">
            <span className="font-semibold">{format(p.value, p.currency)}</span>
            {shown.length > 1 && <span className="ml-2 inline-block w-11 text-muted-foreground">{pct(p.value as number, total)}</span>}
          </span>
        </li>
      ))}
    </ul>
  );
}

// Ring draws the parts as shares of a whole, with the total in the
// middle. More than seven parts fold into "Everything else".
export function Ring({ parts, format, compact }: { parts: Part[]; format: Format; compact?: boolean }) {
  const [hover, setHover] = useState<number | null>(null);
  const shown = useMemo(() => {
    const pos = parts.filter((p) => typeof p.value === "number" && (p.value as number) > 0);
    if (pos.length <= 7) return pos;
    const rest = pos.slice(6).reduce((a, p) => a + (p.value as number), 0);
    return [...pos.slice(0, 6), { label: "Everything else", value: rest, currency: pos[0].currency }];
  }, [parts]);
  const total = shown.reduce((a, p) => a + (p.value as number), 0);
  const size = compact ? 150 : 200;
  const r = size / 2, inner = r * 0.62;
  let angle = -Math.PI / 2;
  const arcs = shown.map((p, i) => {
    const sweep = ((p.value as number) / (total || 1)) * Math.PI * 2;
    const a0 = angle, a1 = angle + Math.max(0, sweep - (shown.length > 1 ? 0.015 : 0));
    angle += sweep;
    const pt = (a: number, rad: number) => `${(r + Math.cos(a) * rad).toFixed(2)},${(r + Math.sin(a) * rad).toFixed(2)}`;
    const big = a1 - a0 > Math.PI ? 1 : 0;
    // A full ring can't be one arc: draw it as two halves.
    const d = shown.length === 1
      ? `M${pt(-Math.PI / 2, r)}A${r},${r} 0 1 1 ${pt(Math.PI / 2, r)}A${r},${r} 0 1 1 ${pt(-Math.PI / 2, r)}ZM${pt(-Math.PI / 2, inner)}A${inner},${inner} 0 1 0 ${pt(Math.PI / 2, inner)}A${inner},${inner} 0 1 0 ${pt(-Math.PI / 2, inner)}Z`
      : `M${pt(a0, r)}A${r},${r} 0 ${big} 1 ${pt(a1, r)}L${pt(a1, inner)}A${inner},${inner} 0 ${big} 0 ${pt(a0, inner)}Z`;
    return { d, i, p };
  });
  const mid = hover !== null ? shown[hover] : null;
  return (
    <div className={`flex items-center gap-6 ${compact ? "h-full justify-center" : "flex-wrap"}`}>
      <svg width={size} height={size} className="shrink-0" role="img" aria-label="Share of the total" onPointerLeave={() => setHover(null)}>
        {arcs.map(({ d, i }) => (
          <path key={i} d={d} fillRule="evenodd" fill={i < 6 ? seriesColor(i) : "var(--muted-foreground)"} opacity={hover === null || hover === i ? 1 : 0.35} onPointerEnter={() => setHover(i)} />
        ))}
        <text x={r} y={r - 4} textAnchor="middle" className="fill-foreground text-[18px] font-semibold tabular-nums">{format(mid ? mid.value : total, shown[0]?.currency)}</text>
        <text x={r} y={r + 14} textAnchor="middle" className="fill-muted-foreground text-[11px]">{mid ? `${mid.label.slice(0, 22)} · ${pct(mid.value as number, total)}` : "Total"}</text>
      </svg>
      <ul className="min-w-0 space-y-1 text-[12px]">
        {shown.map((p, i) => (
          <li key={p.label + i} className={`flex items-center gap-2 ${hover !== null && hover !== i ? "opacity-50" : ""}`} onPointerEnter={() => setHover(i)} onPointerLeave={() => setHover(null)}>
            <Swatch color={i < 6 ? seriesColor(i) : "var(--muted-foreground)"} />
            <span className="max-w-[180px] truncate text-muted-foreground" title={p.label}>{p.label}</span>
            <span className="font-semibold tabular-nums">{format(p.value, p.currency)}</span>
            <span className="text-muted-foreground tabular-nums">{pct(p.value as number, total)}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}

type World = { w: number; h: number; countries: [string, string][] };

const countryName = (code: string): string => {
  try {
    return new Intl.DisplayNames(["en"], { type: "region" }).of(code) ?? code;
  } catch {
    return code;
  }
};

// WorldMap shades each country by its amount: the darker, the more. The
// parts' labels are two-letter country codes. The shapes (Natural Earth,
// public domain, at 1:110m) load only when a map is shown.
// detail, when given, adds lines under a hovered country's amount (its
// other figures); it gets the two-letter code.
export function WorldMap({ parts, format, compact, detail }: { parts: Part[]; format: Format; compact?: boolean; detail?: (code: string) => ReactNode }) {
  const [world, setWorld] = useState<World | null>(null);
  const [box, width] = useWidth();
  const [hover, setHover] = useState<{ code: string; x: number; y: number } | null>(null);
  useEffect(() => {
    let live = true;
    import("@/assets/world.json").then((m) => live && setWorld(m.default as unknown as World));
    return () => {
      live = false;
    };
  }, []);
  const by = useMemo(() => new Map(parts.filter((p) => typeof p.value === "number").map((p) => [p.label.toUpperCase(), p])), [parts]);
  const max = Math.max(0, ...[...by.values()].map((p) => p.value as number));
  // A square-root scale, so one big country doesn't wash out the rest.
  const shade = (v: number) => 0.18 + 0.82 * Math.sqrt(v / (max || 1));
  const top = parts.filter((p) => typeof p.value === "number" && p.label && p.label !== "$other").slice(0, compact ? 0 : 8);
  const unplaced = world ? [...by.keys()].filter((c) => c && c !== "$OTHER" && !world.countries.some(([k]) => k === c)) : [];
  return (
    <div className={compact ? "h-full" : "grid gap-6 lg:grid-cols-[minmax(0,1fr)_220px]"}>
      <div ref={box} className="relative">
        {world ? (
          <svg viewBox={`0 0 ${world.w} ${world.h}`} className="block h-auto w-full" role="img" aria-label="Totals by country" onPointerLeave={() => setHover(null)}>
            {world.countries.map(([code, d], i) => {
              const p = by.get(code);
              return (
                <path
                  key={code + i}
                  d={d}
                  fill={p ? "var(--series-1)" : "var(--muted-foreground)"}
                  fillOpacity={p ? shade(p.value as number) : 0.18}
                  stroke="var(--card)"
                  strokeWidth={hover?.code === code ? 1.5 : 0.5}
                  onPointerMove={(e) => {
                    const r = e.currentTarget.ownerSVGElement!.getBoundingClientRect();
                    setHover({ code, x: e.clientX - r.left, y: e.clientY - r.top });
                  }}
                />
              );
            })}
          </svg>
        ) : (
          <div className="aspect-[2/1] animate-pulse rounded bg-muted/50" />
        )}
        {hover && (
          <HoverCard x={hover.x} width={width} top={Math.max(0, hover.y - 20)}>
            <div className="font-medium">{countryName(hover.code)}</div>
            <div className="tabular-nums text-muted-foreground">{by.has(hover.code) ? format(by.get(hover.code)!.value, by.get(hover.code)!.currency) : "Nothing in these dates"}</div>
            {detail?.(hover.code)}
          </HoverCard>
        )}
        {world && (
          <div className="mt-2 flex items-center gap-2 text-[11px] text-muted-foreground">
            <span>0</span>
            <span className="h-2 w-28 rounded" style={{ background: "linear-gradient(to right, color-mix(in srgb, var(--series-1) 18%, transparent), var(--series-1))" }} />
            <span className="tabular-nums">{format(max)}</span>
            {unplaced.length > 0 && <span className="ml-3">Not on the map: {unplaced.slice(0, 6).join(", ")}{unplaced.length > 6 ? "…" : ""}</span>}
          </div>
        )}
      </div>
      {top.length > 0 && (
        <ol className="space-y-1 text-[13px]">
          {top.map((p) => (
            <li key={p.label} className="flex items-center justify-between gap-3">
              <span className="truncate text-muted-foreground">{countryName(p.label.toUpperCase())}</span>
              <span className="font-semibold tabular-nums">{format(p.value, p.currency)}</span>
            </li>
          ))}
        </ol>
      )}
    </div>
  );
}
