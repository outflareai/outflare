// A search ad as it reads in Google's results: up to three headlines in
// blue, the site and its path in green, then up to two descriptions. Google
// picks which headlines and descriptions to show each time, so the preview
// can step through combinations, and lists every line with its length
// against Google's limits (30 for a headline, 90 for a description, 15 for
// a path part).

import { useState, type ReactNode } from "react";
import { track } from "@/lib/analytics";

export const adLimits = { headline: 30, description: 90, path: 15 } as const;

export type SearchAdText = {
  final_url?: string | null;
  path1?: string | null;
  path2?: string | null;
  headlines?: string[] | null;
  descriptions?: string[] | null;
  // One entry per headline: 0 unpinned, 1 to 3 the position it always takes.
  headline_pins?: number[] | null;
};

// Whether an ad's text is enough to draw as a search ad.
export function isSearchAd(t: SearchAdText): boolean {
  return (t.headlines?.length ?? 0) > 0 && (t.descriptions?.length ?? 0) > 0;
}

// How many of an ad's lines are longer than Google takes.
export function overLimit(t: SearchAdText): number {
  return (t.headlines ?? []).filter((h) => [...h].length > adLimits.headline).length +
    (t.descriptions ?? []).filter((d) => [...d].length > adLimits.description).length +
    [t.path1, t.path2].filter((p) => p && [...p].length > adLimits.path).length;
}

function site(url?: string | null): string {
  if (!url) return "";
  try {
    return new URL(url.includes("://") ? url : `https://${url}`).host;
  } catch {
    return url;
  }
}

// The headlines of combination n: a pinned headline keeps its position, the
// rest fill the free positions in turn.
function pick(t: SearchAdText, n: number): string[] {
  const all = (t.headlines ?? []).filter(Boolean);
  const pins = t.headline_pins ?? [];
  const free = all.filter((_, i) => !(pins[i] >= 1 && pins[i] <= 3));
  const out: string[] = [];
  let next = n * 3;
  for (let pos = 1; pos <= 3; pos++) {
    const pinned = all.filter((_, i) => pins[i] === pos);
    let h = pinned.length ? pinned[n % pinned.length] : undefined;
    for (let tries = 0; !h && free.length && tries < free.length; tries++) {
      const c = free[next++ % free.length];
      if (!out.includes(c)) h = c;
    }
    if (h) out.push(h);
  }
  return out;
}

function Count({ text, max }: { text: string; max: number }) {
  const n = [...text].length;
  return <span className={`shrink-0 tabular-nums text-[11px] ${n > max ? "font-medium text-red-600 dark:text-red-400" : "text-muted-foreground"}`}>{n}/{max}</span>;
}

// open: whether every headline and description is listed under the
// preview from the start (a dense table passes false).
export function SearchAdPreview({ ad, actions, where = "ads", open = true }: { ad: SearchAdText; actions?: ReactNode; where?: string; open?: boolean }) {
  const [n, setN] = useState(0);
  const [phone, setPhone] = useState(false);
  const [all, setAll] = useState(open);
  const headlines = (ad.headlines ?? []).filter(Boolean);
  const descriptions = (ad.descriptions ?? []).filter(Boolean);
  const shownH = pick(ad, n).slice(0, phone ? 2 : 3);
  const d0 = descriptions.length ? n % descriptions.length : 0;
  const shownD = descriptions.length > 1 ? [descriptions[d0], descriptions[(d0 + 1) % descriptions.length]] : descriptions;
  const moreH = headlines.length - shownH.length;
  const moreD = descriptions.length - shownD.length;
  const combos = Math.max(headlines.length > 3 ? Math.ceil(headlines.length / 3) : 1, descriptions.length > 2 ? descriptions.length : 1);
  const path = [ad.path1, ad.path2].filter(Boolean).join("/");
  const over = overLimit(ad);
  const link = "text-[12px] text-blue-700 hover:underline dark:text-blue-400";
  return (
    <div className="min-w-0 max-w-[640px]" data-ad-preview>
      <div className={`rounded-md border bg-card px-3 py-2.5 ${phone ? "max-w-[340px]" : ""}`}>
        <div className="text-[11px] font-semibold text-foreground">Sponsored</div>
        <div className="break-words text-[15px] leading-snug text-blue-700 dark:text-blue-400">
          {shownH.join(" | ")}
          {moreH > 0 && <span className="ml-2 whitespace-nowrap text-[12px] text-muted-foreground">+{moreH} more</span>}
        </div>
        <div className="truncate text-[13px] text-green-700 dark:text-green-400">{site(ad.final_url)}{path ? `/${path}` : ""}</div>
        <div className="break-words text-[13px] leading-snug text-muted-foreground">
          {shownD.join(" ")}
          {moreD > 0 && <span className="ml-2 whitespace-nowrap text-[12px]">+{moreD} more</span>}
        </div>
      </div>
      <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1">
        <button type="button" className={link} aria-expanded={all} onClick={() => { setAll(!all); track("ui.ad_preview_text_opened", { where }); }}>
          {all ? "Hide the full text" : `All ${headlines.length} headlines and ${descriptions.length} descriptions`}
        </button>
        {combos > 1 && <button type="button" className={link} onClick={() => setN((n + 1) % combos)}>Another combination</button>}
        <button type="button" className={link} onClick={() => setPhone(!phone)}>{phone ? "Show on a computer" : "Show on a phone"}</button>
        {over > 0 && <span className="text-[12px] font-medium text-red-600 dark:text-red-400">{over} {over === 1 ? "line is" : "lines are"} too long for Google</span>}
        {actions}
      </div>
      {all && (
        <div className="mt-2 space-y-2 rounded-md border px-3 py-2 text-[13px]">
          <div className="text-[12px] text-muted-foreground">The whole ad. Google shows up to three of these headlines and two of these descriptions at a time, and picks which.</div>
          <div>
            <div className="mb-1 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">Headlines</div>
            {headlines.map((h, i) => (
              <div key={`${i}${h}`} className="flex items-baseline justify-between gap-3">
                <span className="min-w-0 break-words">{h}{(ad.headline_pins?.[i] ?? 0) > 0 && <span className="ml-2 text-[11px] text-muted-foreground">always in position {ad.headline_pins![i]}</span>}</span>
                <Count text={h} max={adLimits.headline} />
              </div>
            ))}
          </div>
          <div>
            <div className="mb-1 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">Descriptions</div>
            {descriptions.map((d, i) => (
              <div key={`${i}${d}`} className="flex items-baseline justify-between gap-3">
                <span className="min-w-0 break-words">{d}</span>
                <Count text={d} max={adLimits.description} />
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
