import { useCallback, useEffect, useState } from "react";
import type { Brand, BrandLinkSuggestion, Review, ReviewGuide, ReviewMomentum, ReviewPage, ReviewPlatform, ReviewsSummary } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { ConnectFor, geonodeWhy } from "@/components/connect-for";
import { PlatformIcon } from "@/components/platform-icon";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, confirmAction, ErrorText, field, Loading, PageHeader, Stat, Stats, toast } from "../ui";
import { ago } from "../format";
import { readWhy, retryIn } from "@/lib/read-failures";
import type { Tab } from "../App";

// Reviews: each brand's Trustpilot, G2, Capterra and Google pages, from its
// links, read once a day through the workspace's own page reader. Every
// review is kept with its stars, sentiment and the business's answer;
// Outflare's review score and momentum say where they're heading, and
// Draft answer writes a public answer with the workspace's own AI for a
// person to post on the site.

type Props = { ws: string; canManage: boolean; go: (tab: Tab, item?: string) => void };

const platforms: { id: ReviewPlatform; name: string; example: string }[] = [
  { id: "trustpilot", name: "Trustpilot", example: "trustpilot.com/review/northpeak.example" },
  { id: "g2", name: "G2", example: "g2.com/products/northpeak/reviews" },
  { id: "capterra", name: "Capterra", example: "capterra.com/p/123456/Northpeak/" },
  { id: "google_reviews", name: "Google", example: "the place's Google Maps link" },
];
const platformName = (p: string) => platforms.find((x) => x.id === p)?.name ?? p;
// The longest answer each site takes (reviewReplyLimits in the engine).
const answerLimit: Record<ReviewPlatform, number> = { trustpilot: 2000, g2: 2000, capterra: 2000, google_reviews: 4000 };

const momentumText: Record<ReviewMomentum["state"], { label: string; tone: "green" | "amber" | "red" | "grey"; say: (m: ReviewMomentum) => string }> = {
  growing: { label: "Growing", tone: "green", say: (m) => `New reviews are coming ${pct(m.pace_change)} faster than the 28 days before, and the score held.` },
  improving: { label: "Improving", tone: "green", say: (m) => `The score is up ${m.score_change} points in 28 days.` },
  steady: { label: "Steady", tone: "grey", say: (m) => `About ${m.per_week.toLocaleString(undefined, { maximumFractionDigits: 1 })} new reviews a week, and the score is holding.` },
  stagnating: { label: "Stagnating", tone: "amber", say: (m) => m.new_reviews_before > 0 && m.pace_change !== null && m.pace_change <= -0.3 ? `New reviews slowed by ${pct(m.pace_change)}: ${m.new_reviews} in the last 28 days against ${m.new_reviews_before} before.` : `Only ${m.new_reviews + m.new_reviews_before} new reviews in 8 weeks. Asking happy customers would help.` },
  slipping: { label: "Slipping", tone: "red", say: (m) => `The score fell ${Math.abs(m.score_change)} points in 28 days: recent reviews are less happy.` },
  too_few: { label: "Too few to tell", tone: "grey", say: () => "Fewer than 5 reviews so far, so there's no trend yet." },
};
const pct = (v: number | null) => (v === null ? "" : `${Math.round(Math.abs(v) * 100)}%`);
const share = (v: number | null | undefined) => (v === null || v === undefined ? "—" : `${Math.round(v * 100)}%`);
const stars = (r: number) => "★★★★★".slice(0, Math.round(r)) + "☆☆☆☆☆".slice(0, 5 - Math.round(r));

export function Reviews({ ws, canManage, go }: Props) {
  const brand = useBrand(ws);
  const brandKey = brand.query.brand ?? "";
  const [summary, setSummary] = useState<ReviewsSummary | null>(null);
  const [pages, setPages] = useState<ReviewPage[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    const path = { ws };
    const query = brandKey ? { brand: brandKey } : {};
    const [s, p] = await Promise.all([
      api.GET("/v1/workspaces/{ws}/reviews/summary", { params: { path, query: { ...query, weeks: 26 } } }),
      api.GET("/v1/workspaces/{ws}/reviews/pages", { params: { path, query } }),
    ]);
    if (s.error || !s.data) return setError(errorMessage(s.error));
    setError("");
    setSummary(s.data);
    setPages(p.data?.data ?? []);
  }, [ws, brandKey]);
  useEffect(() => {
    load();
  }, [load]);

  async function readNow() {
    setBusy(true);
    const { error } = await api.POST("/v1/workspaces/{ws}/reviews/pages/sync", { params: { path: { ws } } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast("Reading your review pages now. New reviews show in a minute.");
    setTimeout(load, 8000);
  }

  const header = (
    <PageHeader
      title="Reviews"
      description="What customers say about your brands on Trustpilot, G2, Capterra and Google, where it's heading, and answers drafted for you to post."
      action={canManage && pages.length > 0 && summary?.can_read && <Button variant="ghost" disabled={busy} onClick={readNow}>Read now</Button>}
    />
  );
  if (error && !summary) return <div className="space-y-6">{header}<ErrorText>{error}</ErrorText></div>;
  if (!summary) return <div className="space-y-6">{header}<Loading /></div>;

  const brands = brand.brand ? [brand.brand] : brand.active;
  const read = pages.some((p) => p.last_read_at);
  const m = summary.momentum;
  const mt = momentumText[m.state];
  return (
    <div className="space-y-6">
      {header}
      {error && <ErrorText>{error}</ErrorText>}

      <YourPages ws={ws} brands={brands} pages={pages} canRead={summary.can_read} canManage={canManage} go={go} onChange={load} />

      {read && summary.outflare_score !== null && <>
        <Stats cols={5}>
          <Stat label="Review score" value={`${summary.outflare_score} / 100`} sub={m.state !== "too_few" && m.score_change !== 0 ? `${m.score_change > 0 ? "+" : ""}${m.score_change} in 28 days` : undefined} tone={m.score_change <= -2 ? "red" : m.score_change >= 2 ? "green" : undefined} />
          <Stat label="Direction" value={<Badge tone={mt.tone}>{mt.label}</Badge>} sub={`${m.new_reviews} new in 28 days`} />
          <Stat label="Average stars" value={summary.average?.toFixed(2) ?? "—"} sub="weighted to recent reviews" />
          <Stat label="Answered, 28 days" value={share(summary.last_28_days.reply_rate)} sub={summary.last_28_days.median_reply_hours !== null ? `in ${hours(summary.last_28_days.median_reply_hours)} typically` : undefined} />
          <Stat label="Unhappy, 28 days" value={share(summary.last_28_days.negative)} sub={`of ${summary.last_28_days.reviews} reviews`} tone={(summary.last_28_days.negative ?? 0) > 0.25 ? "amber" : undefined} />
        </Stats>

        <Card title="Where reviews are heading" description={`The review score weights each review's stars by how recent it is (half as much after ${summary.half_life_days} days) and by how many reviews there are, so a few new 5-star reviews don't outweigh hundreds.`}>
          <p className="mb-3 text-[13px]"><Badge tone={mt.tone}>{mt.label}</Badge> {mt.say(m)}</p>
          {summary.weeks.filter((w) => w.outflare_score !== null).length > 1 && <Weeks weeks={summary.weeks} />}
        </Card>

        {summary.platforms.length > 0 && (
          <Card title="By site" description="Each site's own score and count as it shows them, beside Outflare's score from the reviews kept.">
            <table className="w-full text-[13px]">
              <thead className="text-left text-[12px] text-muted-foreground">
                <tr><th className="py-1.5 font-normal">Site</th><th className="font-normal">Site's score</th><th className="font-normal">Reviews</th><th className="font-normal">Review score</th><th className="font-normal">Direction</th></tr>
              </thead>
              <tbody className="divide-y">
                {summary.platforms.map((p) => (
                  <tr key={p.platform}>
                    <td className="py-2"><span className="flex items-center gap-2"><PlatformIcon kind={p.platform} />{platformName(p.platform)}</span></td>
                    <td className="font-mono tabular-nums">{p.score?.toFixed(1) ?? "—"}</td>
                    <td className="font-mono tabular-nums">{p.review_count?.toLocaleString() ?? "—"}</td>
                    <td className="font-mono tabular-nums">{p.outflare_score ?? "—"}</td>
                    <td><Badge tone={momentumText[p.momentum.state].tone}>{momentumText[p.momentum.state].label}</Badge></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </Card>
        )}
      </>}

      {pages.length > 0 && <Feed ws={ws} pages={pages} brandKey={brandKey} canDraft={summary.can_draft} canManage={canManage} go={go} onChange={load} />}

      {canManage && brands.length > 0 && pages.length > 0 && <Guides ws={ws} brands={brands} />}
    </div>
  );
}

const hours = (h: number) => (h < 1 ? "under an hour" : h < 48 ? `${Math.round(h)} hours` : `${Math.round(h / 24)} days`);

// The review score week by week, with the first and last week under it.
function Weeks({ weeks }: { weeks: ReviewsSummary["weeks"] }) {
  const points = weeks.filter((w) => w.outflare_score !== null) as { week: string; outflare_score: number; new_reviews: number }[];
  const W = 600, H = 120;
  const lo = Math.min(...points.map((p) => p.outflare_score)) - 2, hi = Math.max(...points.map((p) => p.outflare_score)) + 2;
  const x = (i: number) => (i / (points.length - 1)) * W;
  const y = (v: number) => H - 4 - ((v - lo) / (hi - lo)) * (H - 8);
  const first = points[0], last = points[points.length - 1];
  return (
    <div className="space-y-1">
      <svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" className="h-32 w-full overflow-visible" role="img" aria-label={`Review score from ${first.outflare_score} to ${last.outflare_score}`}>
        <polyline fill="none" stroke="var(--series-1)" strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" vectorEffect="non-scaling-stroke" points={points.map((p, i) => `${x(i)},${y(p.outflare_score)}`).join(" ")} />
      </svg>
      <div className="flex justify-between font-mono text-[11px] text-muted-foreground tabular-nums">
        <span>{first.week} · {first.outflare_score}</span>
        <span>{last.week} · {last.outflare_score}</span>
      </div>
    </div>
  );
}

// The first step, and the cold start: each brand's review pages, from its
// links or found by searching the web, to confirm, add or remove here.
const reviewKinds = platforms.map((p) => p.id) as readonly string[];
const short = (url: string) => url.replace(/^https?:\/\/(www\.)?/, "").replace(/\/$/, "");
const withScheme = (v: string) => (/^https?:\/\//i.test(v.trim()) ? v.trim() : `https://${v.trim()}`);
const kindOfURL = (url: string): ReviewPlatform | undefined => {
  try {
    const u = new URL(url);
    const h = u.hostname.toLowerCase();
    if (/(^|\.)trustpilot\.com$/.test(h) && u.pathname.startsWith("/review/")) return "trustpilot";
    if (/(^|\.)g2\.com$/.test(h) && u.pathname.startsWith("/products/")) return "g2";
    if (/(^|\.)capterra\.[a-z.]+$/.test(h) && u.pathname.startsWith("/p/")) return "capterra";
    if (h === "maps.app.goo.gl" || h === "g.page" || (/(^|\.)google\.[a-z.]+$/.test(h) && (u.pathname.startsWith("/maps") || u.searchParams.has("placeid") || u.searchParams.has("cid")))) return "google_reviews";
  } catch {
    return undefined;
  }
  return undefined;
};

function YourPages({ ws, brands, pages, canRead, canManage, go, onChange }: {
  ws: string; brands: Brand[]; pages: ReviewPage[]; canRead: boolean; canManage: boolean; go: (tab: Tab, item?: string) => void; onChange: () => void;
}) {
  const [found, setFound] = useState<Record<string, BrandLinkSuggestion[]>>({});
  const [busy, setBusy] = useState("");
  const [adding, setAdding] = useState({ brand: "", url: "" });
  const { reload } = useBrand(ws);
  const ids = brands.map((b) => b.id).join(",");

  const loadFound = useCallback(async () => {
    const all = await Promise.all(brands.map(async (b) => {
      const { data } = await api.GET("/v1/workspaces/{ws}/brands/{brand}/link-suggestions", { params: { path: { ws, brand: b.id } } });
      return [b.id, (data?.data ?? []).filter((x) => x.status === "suggested" && reviewKinds.includes(x.kind))] as const;
    }));
    setFound(Object.fromEntries(all));
  }, [ws, ids]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    loadFound();
  }, [loadFound]);

  // A new page is read straight away when a page reader is connected.
  async function refresh(read: boolean) {
    await Promise.all([reload(), loadFound()]);
    onChange();
    if (read && canRead) setTimeout(onChange, 8000);
  }

  async function saveLinks(b: Brand, links: Brand["links"], done: string, read: boolean) {
    setBusy(b.id);
    const { error } = await api.PATCH("/v1/workspaces/{ws}/brands/{brand}", { params: { path: { ws, brand: b.id } }, body: { links } });
    setBusy("");
    if (error) {
      toast(errorMessage(error), "error");
      return false;
    }
    toast(done);
    await refresh(read);
    return true;
  }

  async function add() {
    const b = brands.find((x) => x.id === adding.brand) ?? brands[0];
    const url = withScheme(adding.url);
    const kind = kindOfURL(url);
    if (!b || !adding.url.trim()) return;
    if (!kind) return toast("Paste the address of a Trustpilot, G2 or Capterra review page, or a Google Maps place.", "error");
    if (b.links.some((l) => short(l.url).toLowerCase() === short(url).toLowerCase())) return toast(`${b.name} already has that page.`, "error");
    if (await saveLinks(b, [...b.links, { url, kind }], canRead ? "Added. Reading its reviews now." : "Added.", true)) setAdding({ brand: adding.brand, url: "" });
  }

  async function remove(b: Brand, url: string) {
    if (!(await confirmAction(`Remove ${short(url)}?`, <p>It comes off {b.name}'s links and stops being read. Its reviews stay.</p>, "Remove"))) return;
    await saveLinks(b, b.links.filter((l) => l.url !== url), "Removed.", false);
  }

  async function decide(b: Brand, x: BrandLinkSuggestion, how: "add" | "dismiss") {
    setBusy(x.id);
    const res = how === "add"
      ? await api.POST("/v1/workspaces/{ws}/brands/{brand}/link-suggestions/{suggestion}/add", { params: { path: { ws, brand: b.id, suggestion: x.id } }, body: {} })
      : await api.POST("/v1/workspaces/{ws}/brands/{brand}/link-suggestions/{suggestion}/dismiss", { params: { path: { ws, brand: b.id, suggestion: x.id } } });
    setBusy("");
    if (res.error) return toast(errorMessage(res.error), "error");
    toast(how === "dismiss" ? "Dismissed." : canRead ? "Confirmed. Reading its reviews now." : "Confirmed.");
    await refresh(how === "add");
  }

  async function discover(b: Brand) {
    setBusy(b.id);
    const { error } = await api.POST("/v1/workspaces/{ws}/brands/{brand}/reviews/discover", { params: { path: { ws, brand: b.id } } });
    if (error) {
      setBusy("");
      return toast(errorMessage(error), "error");
    }
    // Three web searches take a few seconds.
    for (let i = 0; i < 8; i++) {
      await new Promise((r) => setTimeout(r, 2500));
      const { data } = await api.GET("/v1/workspaces/{ws}/brands/{brand}/link-suggestions", { params: { path: { ws, brand: b.id } } });
      if (data?.data.some((x) => x.status === "suggested" && reviewKinds.includes(x.kind))) break;
    }
    setBusy("");
    loadFound();
  }

  async function pause(p: ReviewPage) {
    const { error } = await api.PATCH("/v1/workspaces/{ws}/reviews/pages/{id}", { params: { path: { ws, id: p.id } }, body: { status: p.status === "paused" ? "active" : "paused" } });
    if (error) return toast(errorMessage(error), "error");
    onChange();
  }

  async function retry(p: ReviewPage) {
    setBusy(p.id);
    const { error } = await api.POST("/v1/workspaces/{ws}/reviews/pages/{id}/read", { params: { path: { ws, id: p.id } } });
    setBusy("");
    if (error) return toast(errorMessage(error), "error");
    toast("Reading it again now. This takes a minute.");
    setTimeout(onChange, 15000);
  }

  // How a confirmed page is going: its score, or what reading it needs.
  function status(b: Brand, url: string) {
    const p = pages.find((x) => x.brand_id === b.id && short(x.url).toLowerCase() === short(url).toLowerCase()) ?? pages.find((x) => short(x.url).toLowerCase() === short(url).toLowerCase());
    if (p?.status === "paused") return <span className="flex items-center gap-2"><Badge>Paused</Badge>{canManage && <Button variant="ghost" onClick={() => pause(p)}>Resume</Button>}</span>;
    // A failed read shows, also when an earlier one worked.
    if (p?.last_error) {
      const why = readWhy(p.last_error_code, p.last_error);
      return (
        <span className="flex flex-wrap items-center gap-2">
          <span className="text-amber-700 dark:text-amber-300" title={p.last_error}>
            {p.last_read_at ? `Last read ${ago(p.last_read_at)}; the latest read failed.` : "Couldn't read it yet."} {why}
            {p.failures > 1 && ` ${p.failures} tries so far.`} Trying again {retryIn(p.next_read_at)}.
          </span>
          {canManage && canRead && <Button variant="ghost" disabled={busy === p.id} onClick={() => retry(p)}>{busy === p.id ? "Reading…" : "Try again"}</Button>}
        </span>
      );
    }
    if (p?.last_read_at && (p.score !== null || p.review_count !== null)) {
      return (
        <span className="flex items-center gap-2 text-muted-foreground">
          {p.score !== null && <span><span className="font-mono tabular-nums text-foreground">{p.score.toFixed(1)}</span> stars</span>}
          {p.review_count !== null && <span>· <span className="font-mono tabular-nums text-foreground">{p.review_count.toLocaleString()}</span> reviews</span>}
          {p.unanswered_negative > 0 && <Badge tone="amber">{p.unanswered_negative} to answer</Badge>}
          <span>· read {ago(p.last_read_at)}</span>
          {canManage && <Button variant="ghost" onClick={() => pause(p)}>Pause</Button>}
        </span>
      );
    }
    if (canRead) return <span className="text-muted-foreground">First read within a few minutes</span>;
    return <span className="text-muted-foreground">Waiting for Geonode</span>;
  }

  const row = "flex flex-wrap items-center justify-between gap-2";
  const confirmed = brands.some((b) => b.links.some((l) => reviewKinds.includes(l.kind ?? "")));
  return (
    <Card title="Your review pages" description="Each brand's pages on review sites. Add, confirm or remove them here; every page is read once a day, so the trend shows from today.">
      {!canRead && (
        <div className="mb-3 flex flex-wrap items-center justify-between gap-3 rounded-lg border bg-muted/40 px-3 py-2.5 text-[13px]">
          <span>{confirmed ? "Review pages are read through your own Geonode account. Connect it once and every page below is read today, then every day." : "Connect your own Geonode account and Outflare finds your brands' Trustpilot, G2 and Capterra pages for you to confirm, then reads them every day."}</span>
          {canManage && <ConnectFor connector="geonode" name="Geonode" back="reviews" why={geonodeWhy.reviews} go={go} />}
        </div>
      )}
      {brands.length === 0 ? (
        <p className="text-[13px] text-muted-foreground">Add a brand with its website first. <button className="underline" onClick={() => go("brands")}>Add a brand</button></p>
      ) : (
        <ul className="divide-y text-[13px]">
          {brands.flatMap((b) => platforms.map(({ id: kind, name, example }) => {
            const links = b.links.filter((l) => l.kind === kind);
            const waiting = (found[b.id] ?? []).filter((x) => x.kind === kind && !links.some((l) => short(l.url).toLowerCase() === short(x.url).toLowerCase()));
            const label = <span className="flex min-w-0 items-center gap-2.5" title={name}><PlatformIcon kind={kind} />{brands.length > 1 && <span className="font-medium">{b.name}</span>}</span>;
            return (
              <li key={`${b.id}:${kind}`} className="space-y-2 py-3 first:pt-0">
                {links.map((l) => (
                  <div key={l.url} className={row}>
                    <span className="flex min-w-0 items-center gap-2.5">{label}<a className="truncate hover:underline" href={l.url} target="_blank" rel="noreferrer noopener">{short(l.url)}</a></span>
                    <span className="flex flex-wrap items-center gap-2">
                      {status(b, l.url)}
                      {canManage && <Button variant="ghost" disabled={busy === b.id} onClick={() => remove(b, l.url)}>Remove</Button>}
                    </span>
                  </div>
                ))}
                {waiting.map((x) => (
                  <div key={x.id} className={row}>
                    <span className="flex min-w-0 items-center gap-2.5">{label}<a className="truncate hover:underline" href={x.url} target="_blank" rel="noreferrer noopener">{short(x.url)}</a><span className="text-muted-foreground">found by searching the web</span></span>
                    {canManage && (
                      <span className="flex gap-2">
                        <Button disabled={busy === x.id} onClick={() => decide(b, x, "add")}>Confirm</Button>
                        <Button variant="ghost" disabled={busy === x.id} onClick={() => decide(b, x, "dismiss")}>Not ours</Button>
                      </span>
                    )}
                  </div>
                ))}
                {links.length === 0 && waiting.length === 0 && (
                  <div className={row}>
                    <span className="flex min-w-0 items-center gap-2.5">{label}<span className="text-muted-foreground">No {name} page yet{kind === "google_reviews" ? `: paste ${example} below` : ""}</span></span>
                    {canManage && canRead && kind !== "google_reviews" && <Button variant="ghost" disabled={busy === b.id} onClick={() => discover(b)}>{busy === b.id ? "Searching…" : "Find it"}</Button>}
                  </div>
                )}
              </li>
            );
          }))}
        </ul>
      )}
      {canManage && brands.length > 0 && (
        <form className="mt-3 flex flex-wrap items-center gap-2 border-t pt-3" onSubmit={(e) => { e.preventDefault(); add(); }}>
          {brands.length > 1 && (
            <select aria-label="Brand" className={`${field} h-8`} value={adding.brand || brands[0].id} onChange={(e) => setAdding({ ...adding, brand: e.target.value })}>
              {brands.map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}
            </select>
          )}
          <input aria-label="Review page address" placeholder="Add a page: paste its address, like trustpilot.com/review/northpeak.example" className={`${field} h-8 min-w-0 flex-1 text-[13px]`} value={adding.url} onChange={(e) => setAdding({ ...adding, url: e.target.value })} />
          <Button disabled={!adding.url.trim() || !!busy}>Add</Button>
        </form>
      )}
    </Card>
  );
}

// Every review, newest first, with a filter for the ones waiting for an answer.
function Feed({ ws, pages, brandKey, canDraft, canManage, go, onChange }: {
  ws: string; pages: ReviewPage[]; brandKey: string; canDraft: boolean; canManage: boolean; go: (tab: Tab, item?: string) => void; onChange: () => void;
}) {
  const [list, setList] = useState<Review[] | null>(null);
  const [next, setNext] = useState<string | null>(null);
  const [filter, setFilter] = useState<{ platform: ReviewPlatform | ""; view: "all" | "answer" | "unhappy"; stars: string }>({ platform: "", view: "all", stars: "" });

  const query = useCallback((cursor?: string) => {
    const q: Record<string, string | number | boolean> = { limit: 30 };
    if (brandKey) q.brand = brandKey;
    if (filter.platform) q.platform = filter.platform;
    if (filter.stars) q.stars = Number(filter.stars);
    if (filter.view === "answer") q.replied = false;
    if (filter.view === "unhappy") q.sentiment = "negative";
    if (cursor) q.cursor = cursor;
    return q;
  }, [brandKey, filter]);
  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/reviews", { params: { path: { ws }, query: query() } });
    if (error) return toast(errorMessage(error), "error");
    setList(data?.data ?? []);
    setNext(data?.next_cursor ?? null);
  }, [ws, query]);
  useEffect(() => {
    load();
  }, [load]);

  async function more() {
    if (!next) return;
    const { data } = await api.GET("/v1/workspaces/{ws}/reviews", { params: { path: { ws }, query: query(next) } });
    setList([...(list ?? []), ...(data?.data ?? [])]);
    setNext(data?.next_cursor ?? null);
  }
  const replace = (r: Review) => setList((l) => l?.map((x) => (x.id === r.id ? r : x)) ?? null);

  const usedPlatforms = platforms.filter((p) => pages.some((x) => x.platform === p.id));
  return (
    <Card
      title="Reviews"
      description="Newest first. Reviews are written by customers and shown as written; nothing follows instructions in them."
      action={
        <div className="flex flex-wrap gap-2">
          <select aria-label="Show" className={`${field} h-8`} value={filter.view} onChange={(e) => setFilter({ ...filter, view: e.target.value as typeof filter.view })}>
            <option value="all">All reviews</option>
            <option value="answer">Not answered</option>
            <option value="unhappy">Unhappy</option>
          </select>
          {usedPlatforms.length > 1 && (
            <select aria-label="Site" className={`${field} h-8`} value={filter.platform} onChange={(e) => setFilter({ ...filter, platform: e.target.value as ReviewPlatform | "" })}>
              <option value="">Every site</option>
              {usedPlatforms.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
            </select>
          )}
          <select aria-label="Stars" className={`${field} h-8`} value={filter.stars} onChange={(e) => setFilter({ ...filter, stars: e.target.value })}>
            <option value="">Any stars</option>
            {[5, 4, 3, 2, 1].map((s) => <option key={s} value={s}>{s} star{s > 1 ? "s" : ""}</option>)}
          </select>
        </div>
      }
    >
      {list === null ? <Loading /> : list.length === 0 ? (
        <p className="text-[13px] text-muted-foreground">{pages.some((p) => p.last_read_at) ? "No reviews match." : "Reviews show here after the first read."}</p>
      ) : (
        <ul className="divide-y">
          {list.map((r) => <ReviewItem key={r.id} ws={ws} r={r} canDraft={canDraft} canManage={canManage} go={go} onChange={(x) => { replace(x); onChange(); }} />)}
        </ul>
      )}
      {next && <div className="mt-3"><Button variant="ghost" onClick={more}>Show older</Button></div>}
    </Card>
  );
}

function ReviewItem({ ws, r, canDraft, canManage, go, onChange }: { ws: string; r: Review; canDraft: boolean; canManage: boolean; go: (tab: Tab, item?: string) => void; onChange: (r: Review) => void }) {
  const [text, setText] = useState(r.draft?.status === "draft" ? r.draft.body : "");
  const [hint, setHint] = useState("");
  const [open, setOpen] = useState(r.draft?.status === "draft" || r.draft?.status === "drafting");
  const [busy, setBusy] = useState(false);
  const answered = !!r.replied_at || r.reply_body !== "";
  const limit = answerLimit[r.platform];

  // A draft takes a few seconds: ask again until it's ready.
  async function waitForDraft() {
    for (let i = 0; i < 20; i++) {
      await new Promise((ok) => setTimeout(ok, 2000));
      const { data } = await api.GET("/v1/workspaces/{ws}/reviews/{id}", { params: { path: { ws, id: r.id } } });
      if (data && data.draft?.status !== "drafting") {
        if (data.draft?.status === "draft") setText(data.draft.body);
        onChange(data);
        return;
      }
    }
  }

  async function draft() {
    setBusy(true);
    setOpen(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/reviews/{id}/draft", { params: { path: { ws, id: r.id } }, body: hint.trim() ? { hint: hint.trim() } : {} });
    if (error || !data) {
      setBusy(false);
      return toast(errorMessage(error), "error");
    }
    onChange(data);
    setHint("");
    await waitForDraft();
    setBusy(false);
  }

  async function copyAndOpen() {
    try {
      await navigator.clipboard.writeText(text);
      toast(`Copied. Paste it as your answer on ${platformName(r.platform)}, then mark it answered here.`);
    } catch {
      toast("Couldn't copy: select the answer and copy it yourself.", "error");
    }
    if (r.url) window.open(r.url, "_blank", "noopener,noreferrer");
  }

  async function markAnswered() {
    setBusy(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/reviews/{id}/replied", { params: { path: { ws, id: r.id } }, body: text.trim() ? { body: text.trim() } : {} });
    setBusy(false);
    if (error || !data) return toast(errorMessage(error), "error");
    toast("Marked as answered.");
    setOpen(false);
    onChange(data);
  }

  const d = r.draft;
  return (
    <li className="space-y-2 py-4 first:pt-0">
      <div className="flex flex-wrap items-center gap-2 text-[13px]">
        <PlatformIcon kind={r.platform} />
        <span className={r.sentiment === "negative" ? "text-red-600 dark:text-red-400" : r.sentiment === "neutral" ? "text-amber-600 dark:text-amber-300" : "text-green-700 dark:text-green-400"} aria-label={`${r.rating} stars`}>{stars(r.rating)}</span>
        <span className="font-medium">{r.author_name || "Someone"}</span>
        {r.author_country && <span className="text-muted-foreground">{r.author_country}</span>}
        {r.verified && <Badge>Verified</Badge>}
        <span className="text-muted-foreground">· {ago(r.posted_at)}{r.edited_at ? " · edited" : ""}</span>
        {r.state === "removed" && <Badge tone="grey">Removed from the site</Badge>}
        {r.url && <a className="ml-auto text-[12px] text-muted-foreground hover:underline" href={r.url} target="_blank" rel="noreferrer noopener">Open on {platformName(r.platform)}</a>}
      </div>
      {r.title && <p className="text-[13px] font-medium">{r.title}</p>}
      {r.body && <p className="whitespace-pre-line text-[13px] text-muted-foreground">{r.body}</p>}
      {answered && (
        <div className="rounded-md border-l-2 bg-muted/40 px-3 py-2 text-[13px]">
          <div className="text-[12px] text-muted-foreground">Your answer{r.replied_at ? ` · ${ago(r.replied_at)}` : ""}</div>
          {r.reply_body ? <p className="whitespace-pre-line">{r.reply_body}</p> : <p className="text-muted-foreground">Marked as answered.</p>}
        </div>
      )}
      {!answered && canManage && !open && (
        <div className="flex flex-wrap gap-2">
          {canDraft ? <Button variant="ghost" onClick={draft} disabled={busy}>Draft answer</Button> : <Button variant="ghost" onClick={() => setOpen(true)}>Write an answer</Button>}
        </div>
      )}
      {!answered && canManage && open && (
        <div className="space-y-2 rounded-md border p-3">
          {d?.status === "drafting" || (busy && !text) ? <p className="text-[13px] text-muted-foreground">Drafting an answer…</p> : <>
            {d?.status === "skipped" && <p className="text-[13px] text-amber-700 dark:text-amber-300">{d.status_reason}</p>}
            {d?.status === "failed" && <p className="text-[13px] text-red-600 dark:text-red-400">The draft didn't work{d.status_reason ? `: ${d.status_reason}` : ""}. Try again, or write it yourself.</p>}
            <textarea aria-label="Answer" className={`${field} min-h-28 w-full`} value={text} onChange={(e) => setText(e.target.value)} placeholder="Write your answer" />
            <div className="flex flex-wrap items-center gap-2">
              <span className={`text-[12px] tabular-nums ${text.length > limit ? "text-red-600 dark:text-red-400" : "text-muted-foreground"}`}>{text.length.toLocaleString()} / {limit.toLocaleString()}</span>
              <span className="flex-1" />
              {canDraft && (
                <form className="flex gap-2" onSubmit={(e) => { e.preventDefault(); draft(); }}>
                  <input aria-label="What to change" placeholder={text ? "Rewrite: shorter, mention the fix…" : "Anything to mention?"} className={`${field} h-8 w-56 text-[13px]`} value={hint} onChange={(e) => setHint(e.target.value)} maxLength={500} />
                  <Button variant="ghost" disabled={busy}>{text ? "Rewrite" : "Draft"}</Button>
                </form>
              )}
              <Button variant="ghost" disabled={!text.trim() || text.length > limit} onClick={copyAndOpen}>Copy and open the review</Button>
              <Button disabled={busy} onClick={markAnswered}>Mark as answered</Button>
              <Button variant="ghost" onClick={() => setOpen(false)}>Close</Button>
            </div>
            {!canDraft && <p className="text-[12px] text-muted-foreground">To have answers drafted for you, <button className="underline" onClick={() => go("workspace")}>connect your own AI model</button>.</p>}
          </>}
        </div>
      )}
    </li>
  );
}

// How each brand answers reviews: what drafts follow.
function Guides({ ws, brands }: { ws: string; brands: Brand[] }) {
  const [id, setId] = useState(brands[0]?.id ?? "");
  const [g, setG] = useState<ReviewGuide | null>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (!brands.some((b) => b.id === id)) setId(brands[0]?.id ?? "");
  }, [brands, id]);
  useEffect(() => {
    if (!id) return;
    setG(null);
    api.GET("/v1/workspaces/{ws}/brands/{brand}/review-guide", { params: { path: { ws, brand: id } } }).then(({ data }) => setG(data ?? null));
  }, [ws, id]);

  async function save() {
    if (!g) return;
    setBusy(true);
    const { data, error } = await api.PUT("/v1/workspaces/{ws}/brands/{brand}/review-guide", {
      params: { path: { ws, brand: id } },
      body: { tone: g.tone, signed_by: g.signed_by, support_contact: g.support_contact, never_say: g.never_say, notes: g.notes },
    });
    setBusy(false);
    if (error || !data) return toast(errorMessage(error), "error");
    setG(data);
    toast("Saved. New drafts follow it.");
  }

  const text = (key: "tone" | "never_say" | "notes", label: string, placeholder: string) => (
    <label className="block space-y-1 text-[13px]">
      <span className="font-medium">{label}</span>
      <textarea className={`${field} min-h-16 w-full`} value={g?.[key] ?? ""} placeholder={placeholder} onChange={(e) => g && setG({ ...g, [key]: e.target.value })} />
    </label>
  );
  return (
    <Card
      title="How you answer reviews"
      description="Drafts follow this, with your past answers as examples. Keep the notes current: a known issue and its fix make the best answers."
      action={brands.length > 1 && (
        <select aria-label="Brand" className={`${field} h-8`} value={id} onChange={(e) => setId(e.target.value)}>
          {brands.map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}
        </select>
      )}
    >
      {!g ? <Loading /> : (
        <form className="space-y-3" onSubmit={(e) => { e.preventDefault(); save(); }}>
          {g.prefilled && <p className="text-[12px] text-muted-foreground">Filled in from the brand's voice and rules. Check it and save.</p>}
          {text("tone", "Tone", "Warm and plain. First names. No exclamation marks.")}
          <div className="grid gap-3 sm:grid-cols-2">
            <label className="block space-y-1 text-[13px]"><span className="font-medium">Signed by</span><input className={`${field} w-full`} value={g.signed_by} onChange={(e) => setG({ ...g, signed_by: e.target.value })} placeholder="The Northpeak team" /></label>
            <label className="block space-y-1 text-[13px]"><span className="font-medium">Where to send people for help</span><input className={`${field} w-full`} value={g.support_contact} onChange={(e) => setG({ ...g, support_contact: e.target.value })} placeholder="support@northpeak.example" /></label>
          </div>
          {text("never_say", "Never say", "Prices, discounts, anything about other customers.")}
          {text("notes", "Notes: known issues and fixes", "Calendar sync broke on 20 October and was fixed in version 2.4.")}
          <Button disabled={busy}>Save</Button>
        </form>
      )}
    </Card>
  );
}
