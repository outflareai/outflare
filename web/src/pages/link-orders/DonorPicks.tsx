import { useMemo, useState } from "react";
import { track } from "../../lib/analytics";
import { Badge, Button, Card } from "../../ui";
import { ago } from "../../format";
import { checkInAhrefs } from "./ahrefsCheck";
import { PicksOutreach } from "./PicksOutreach";
import { greenLight, needsAhrefsCheck, topPageLabel } from "./quality";
import { autoPicks, catalogTopics, guessTopics, recommendDonors, targetLanguage } from "./donorPicksModel";
import { dash, moneyList, type LinkOrderDonor, type LinkOrderTargetSuggestion, type PageData } from "./shared";

// Recommended donors for the link target picked above the catalog. The
// ranking follows the catalog filters, so a language, price or category
// filter narrows the picks too. Nothing is ordered: a ticked pick is in the
// request for Request quotes, like the checkbox in the table. A site someone
// at it is already enrolled in a sequence for (outreach) leaves the picks
// and is listed under "Sent to a sequence" instead.

const topicsKey = (brand: string) => `outflare.link-orders.pick-topics.${brand}`;
const howManyKey = (brand: string) => `outflare.link-orders.pick-count.${brand}`;

/** How many picks can be shown, ticked and sent to a sequence at once. */
export const PICK_COUNTS = [10, 25, 50, 100, 200] as const;

function loadHowMany(brand: string): number {
  try {
    const n = Number(window.localStorage.getItem(howManyKey(brand)));
    return (PICK_COUNTS as readonly number[]).includes(n) ? n : 10;
  } catch {
    return 10;
  }
}

function saveHowMany(brand: string, n: number) {
  try {
    window.localStorage.setItem(howManyKey(brand), String(n));
  } catch {
    // A per-browser convenience only.
  }
}

function loadTopics(brand: string): string[] | null {
  try {
    const raw = window.localStorage.getItem(topicsKey(brand));
    const v = raw ? JSON.parse(raw) : null;
    return Array.isArray(v) ? v.filter((x) => typeof x === "string") : null;
  } catch {
    return null;
  }
}

function saveTopics(brand: string, topics: string[]) {
  try {
    window.localStorage.setItem(topicsKey(brand), JSON.stringify(topics));
  } catch {
    // A per-browser convenience only.
  }
}

export function DonorPicks({ data, donors, targetURL, suggestion, suggestions, cart, setCart, onOpenDonor }: {
  data: PageData;
  /** Every suggested link target, best first: used when no page is chosen. */
  suggestions: LinkOrderTargetSuggestion[];
  /** The catalog after its filters. */
  donors: LinkOrderDonor[];
  targetURL: string;
  suggestion: LinkOrderTargetSuggestion | undefined;
  cart: string[];
  setCart: (ids: string[]) => void;
  onOpenDonor: (d: LinkOrderDonor) => void;
}) {
  const guessed = useMemo(() => guessTopics(data.donors, data.orders, data.candidates), [data.donors, data.orders, data.candidates]);
  const [topics, setTopicsState] = useState<string[]>(() => loadTopics(data.brand) ?? guessed);
  const setTopics = (t: string[]) => {
    setTopicsState(t);
    saveTopics(data.brand, t);
    track("ui.link_order_pick_topics_changed", { count: t.length });
  };
  const [open, setOpen] = useState(true);
  const [more, setMore] = useState(false);
  const [outreach, setOutreach] = useState<LinkOrderDonor[] | null>(null);
  const [checking, setChecking] = useState(false);
  const all = useMemo(() => catalogTopics(data.donors), [data.donors]);

  // Sites already in outreach leave the picks; they are listed below.
  const fresh = useMemo(() => donors.filter((d) => !d.outreach), [donors]);
  const sent = useMemo(
    () => donors.filter((d) => d.outreach).sort((a, b) => Date.parse(b.outreach!.enrolled_at) - Date.parse(a.outreach!.enrolled_at)),
    [donors],
  );
  const [showSent, setShowSent] = useState(false);
  // How many picks to show (and so to tick and send at once).
  const [howMany, setHowManyState] = useState<number>(() => loadHowMany(data.brand));
  const setHowMany = (n: number) => {
    setHowManyState(n);
    saveHowMany(data.brand, n);
  };

  const chosen = targetURL.trim();
  // With no page chosen, picks cover the top five suggested pages.
  const autoPages = useMemo(() => (chosen ? [] : suggestions.slice(0, 5).map((s) => ({ url: s.url, competitors: s.competitors ?? [] }))), [chosen, suggestions]);
  const url = chosen || (autoPages.length ? "auto" : "");
  const picks = useMemo(
    () => (chosen
      ? recommendDonors({ donors: fresh, orders: data.orders, candidates: data.candidates, target: { url: chosen, competitors: suggestion?.competitors ?? [] }, topics, limit: howMany })
      : autoPages.length ? autoPicks({ donors: fresh, orders: data.orders, candidates: data.candidates, pages: autoPages, topics, perPage: Math.ceil(howMany / autoPages.length), limit: howMany }) : []),
    [chosen, autoPages, fresh, data.orders, data.candidates, suggestion, topics, howMany],
  );
  if (!data.canManage) return null;

  const toggleTopic = (t: string) => setTopics(topics.includes(t) ? topics.filter((x) => x !== t) : [...topics, t]);
  const shownTopics = more ? all : all.filter((t) => topics.includes(t.topic) || all.indexOf(t) < 12);
  const ticked = picks.filter((p) => cart.includes(p.donor.id));
  const allTicked = picks.length > 0 && ticked.length === picks.length;
  const toggleAll = () => {
    const ids = picks.map((p) => p.donor.id);
    setCart(allTicked ? cart.filter((x) => !ids.includes(x)) : [...cart, ...ids.filter((x) => !cart.includes(x))]);
    track("ui.link_order_picks_selected_all", { count: ids.length, selected: !allTicked });
  };
  const toggle = (id: string, score: number) => {
    if (cart.includes(id)) return setCart(cart.filter((x) => x !== id));
    setCart([...cart, id]);
    track("ui.link_order_picks_added", { count: 1, top_score: score });
  };

  return (
    <Card
      title="Recommended donors"
      description="The catalog ranked for this page: topic, language, DR, organic traffic, price per 1,000 visits, links to the competitors that rank for it and the Best link label, minus link farms and donors already used. Flagged and likely PBNs are left out. It follows the filters above and only reads stored data. Nothing is ordered until you request quotes."
    >
      <div className="flex flex-wrap items-center gap-2">
        <Button variant="ghost" onClick={() => setOpen(!open)} aria-expanded={open}>{open ? "Hide" : "Show"}</Button>
        {chosen ? <span className="text-[12px] text-muted-foreground">For {chosen} · {targetLanguage(chosen)} page</span> : autoPages.length ? <span className="text-[12px] text-muted-foreground">For the top {autoPages.length} suggested pages, shared between them. Tick a link target above for one page.</span> : <span className="text-[12px] text-muted-foreground">Tick a link target or type a page above to see picks.</span>}
        <label className="ml-auto flex items-center gap-2 text-[12px] text-muted-foreground">
          Show
          <select className="h-8 rounded-md border bg-background px-2 text-[13px]" value={howMany} onChange={(e) => { setHowMany(Number(e.target.value)); track("ui.link_order_pick_count_changed", { count: Number(e.target.value) }); }} aria-label="How many recommended donors to show">
            {PICK_COUNTS.map((n) => <option key={n} value={n}>{n}</option>)}
          </select>
          picks
        </label>
      </div>
      {open ? (
        <>
          <div className="mt-3 flex flex-wrap items-center gap-1.5" role="group" aria-label="Topics to prefer">
            <span className="mr-1 text-[12px] text-muted-foreground">Topics:</span>
            {shownTopics.map((t) => (
              <button
                key={t.topic}
                type="button"
                aria-pressed={topics.includes(t.topic)}
                onClick={() => toggleTopic(t.topic)}
                className={`rounded-full border px-2 py-0.5 text-[12px] ${topics.includes(t.topic) ? "border-primary bg-primary text-primary-foreground" : "text-muted-foreground hover:text-foreground"}`}
              >
                {t.topic} <span className="opacity-70">{t.n}</span>
              </button>
            ))}
            {all.length > 12 ? <button type="button" className="text-[12px] text-muted-foreground underline" onClick={() => setMore(!more)}>{more ? "Fewer" : `All ${all.length}`}</button> : null}
            {guessed.length && guessed.join("|") !== topics.join("|") ? (
              <button type="button" className="text-[12px] text-muted-foreground underline" onClick={() => setTopics(guessed)} title="The categories of donors already ordered and of catalog sites that link to competitors">Suggested topics</button>
            ) : null}
          </div>
          {url && !topics.length ? (
            <p className="mt-2 text-[12px] text-amber-700 dark:text-amber-400">Pick the topics this page is about: without them the picks are ranked on authority and price only.</p>
          ) : null}
          {url && picks.length ? (
            <>
              <label className="mt-3 flex items-center gap-2 border-b pb-2 text-[12px] text-muted-foreground">
                <input type="checkbox" className="size-4" checked={allTicked} ref={(el) => { if (el) el.indeterminate = ticked.length > 0 && !allTicked; }} onChange={toggleAll} aria-label="Select all recommended donors" />
                {ticked.length ? `${ticked.length} of ${picks.length} selected` : `Select all ${picks.length}`}
              </label>
              <ol className="flex flex-col divide-y" aria-label="Recommended donors">
                {picks.map((p) => {
                  const inCart = cart.includes(p.donor.id);
                  const light = greenLight(p.donor);
                  return (
                    <li key={p.donor.id} className={`flex flex-wrap items-start gap-x-4 gap-y-1 py-2 ${inCart ? "bg-muted/40" : ""}`} data-pick={p.donor.domain}>
                      <input type="checkbox" className="mt-1 size-4 shrink-0" checked={inCart} onChange={() => toggle(p.donor.id, p.score)} aria-label={`Select ${p.donor.domain}`} />
                      <span className="w-8 shrink-0 text-right text-[15px] font-semibold tabular-nums" title="Score out of 100">{p.score}</span>
                      <div className="min-w-48 flex-1">
                        <div className="flex flex-wrap items-center gap-2">
                          <button type="button" className="font-medium hover:underline" onClick={() => onOpenDonor(p.donor)}>{p.donor.domain}</button>
                          <Badge tone={light.tone} title={light.why.join(" · ") || "Verdict OK and checked in Ahrefs recently"}>● {light.label}</Badge>
                          <Badge tone={p.donor.quality?.top_page_share == null ? "grey" : light.tone === "red" ? "red" : p.donor.quality.verdict === "check" ? "amber" : "green"} title="Share of the site's search traffic on its top page: over 45% means skip, 35% to 45% check.">{topPageLabel(p.donor)}</Badge>
                        </div>
                        {p.forPage ? <div className="truncate text-[12px]">For <span className="font-medium">{p.forPage.replace(/^https?:\/\/[^/]+/, "") || "/"}</span></div> : null}
                        <div className="text-[12px] text-muted-foreground">
                          {moneyList(p.donor.prices)} · DR {p.donor.domain_rating ?? dash} · {p.donor.traffic == null ? dash : p.donor.traffic.toLocaleString()} visits{p.donor.intermediary ? ` · ${p.donor.intermediary}` : ""}
                        </div>
                        <div className="mt-1 flex flex-wrap gap-1">
                          {p.reasons.map((r) => <Badge key={r} tone="green">{r}</Badge>)}
                          {p.warnings.map((w) => <Badge key={w} tone="amber">{w}</Badge>)}
                        </div>
                      </div>
                    </li>
                  );
                })}
              </ol>
              <div className="mt-2 flex flex-wrap gap-2">
                <Button disabled={!ticked.length} title={ticked.length ? undefined : "Tick the donors to write to first"} onClick={() => setOutreach(ticked.map((p) => p.donor))}>
                  Find emails and send to a sequence ({ticked.length})
                </Button>
                {(() => {
                  // Picks with no Ahrefs check in the last 30 days: the ticked
                  // ones, else every pick shown. A fresher check is used as
                  // it is, never pulled again.
                  const pool = ticked.length ? ticked : picks;
                  const stale = pool.filter((p) => needsAhrefsCheck(p.donor));
                  if (!stale.length) return null;
                  return (
                    <Button variant="ghost" disabled={checking} title="Reads only DR, traffic, referring and linked domains and the three top pages: about 250 Ahrefs API units a site, on the brand's own Ahrefs." onClick={async () => {
                      setChecking(true);
                      await checkInAhrefs(data, stale.map((p) => p.donor.id), "picks");
                      setChecking(false);
                    }}>
                      Check {stale.length} in Ahrefs
                    </Button>
                  );
                })()}
              </div>
            </>
          ) : url ? (
            <p className="mt-3 text-[12px] text-muted-foreground">No priced, non-PBN donors match the filters above for this page.</p>
          ) : null}
        </>
      ) : null}
      {open && sent.length ? (
        <div className="mt-4 border-t pt-3">
          <button type="button" className="text-[13px] font-medium" aria-expanded={showSent} onClick={() => setShowSent(!showSent)}>
            {showSent ? "▾" : "▸"} Sent to a sequence ({sent.length})
          </button>
          <p className="text-[12px] text-muted-foreground">Someone at these sites is in one of the brand's sequences, so they are left out of the picks. Stop them in the sequence, or archive it, to bring a site back.</p>
          {showSent ? (
            <ul className="mt-2 flex flex-col divide-y text-[13px]" aria-label="Sent to a sequence">
              {sent.slice(0, 100).map((d) => {
                const o = d.outreach!;
                const waiting = o.sequence_status !== "active" && o.status === "active";
                return (
                  <li key={d.id} className="flex flex-wrap items-center gap-x-3 gap-y-1 py-1.5" data-sent={d.domain}>
                    <button type="button" className="font-medium hover:underline" onClick={() => onOpenDonor(d)}>{d.domain}</button>
                    <span className="text-muted-foreground">{o.email}</span>
                    <span className="text-muted-foreground">→ {o.sequence_name}</span>
                    <Badge tone={o.status === "replied" ? "green" : o.status === "bounced" || o.status === "failed" || o.status === "blocked" ? "red" : waiting ? "amber" : "grey"} title={waiting ? `The sequence is ${o.sequence_status}: nothing is sent until it is started.` : undefined}>
                      {waiting ? `${o.sequence_status}, not sending` : o.status}
                    </Badge>
                    <span className="text-[12px] text-muted-foreground">{ago(o.enrolled_at)}</span>
                  </li>
                );
              })}
            </ul>
          ) : null}
        </div>
      ) : null}
      {outreach ? <PicksOutreach brand={data.brand} donors={outreach} onClose={(added) => {
        setOutreach(null);
        if (added) {
          setCart(cart.filter((x) => !outreach.some((d) => d.id === x)));
          data.reload();
        }
      }} /> : null}
    </Card>
  );
}
