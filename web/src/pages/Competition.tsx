import { useEffect, useMemo, useState } from "react";
import type { components } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { PickBrand } from "@/components/pick-brand";
import { api, errorMessage } from "../api";
import { ConnectFor, geonodeWhy } from "@/components/connect-for";
import { Badge, Button, Card, Empty, ErrorText, Input, Loading, PageHeader, type tones } from "../ui";
import type { Tab } from "../App";

// Competitors → Overview: who is dominating. A leaderboard of the brand and
// every competitor it tracks with an overall score, where the brand leads
// and trails and whether its share is gaining, each area's share over the
// year, and one page per competitor (/competition/{id}). It reads only
// stored data (GET /v1/brands/{brand}/competition), so it costs nothing.

type Props = { ws: string; go: (tab: Tab, item?: string) => void; item?: string };
type Data = components["schemas"]["Competition"];
type Entry = Data["entries"][number];
type Area = Data["areas"][number];
type Tone = keyof typeof tones;
type Profiles = components["schemas"]["CompetitorProfiles"];
type Profile = Profiles["profiles"][number];

const weightsKey = "competition-weights";
function savedWeights(): Record<string, number> {
  try {
    const v = JSON.parse(localStorage.getItem(weightsKey) ?? "{}");
    return v && typeof v === "object" ? v : {};
  } catch {
    return {};
  }
}

const standingLabel: Record<string, { label: string; tone: Tone }> = {
  leading: { label: "Leading", tone: "green" },
  ahead: { label: "Ahead", tone: "green" },
  level: { label: "Level", tone: "blue" },
  behind: { label: "Behind", tone: "amber" },
  far_behind: { label: "Far behind", tone: "red" },
};
const trendLabel: Record<string, { label: string; tone: Tone }> = {
  gaining: { label: "Gaining", tone: "green" },
  steady: { label: "Steady", tone: "grey" },
  losing: { label: "Losing", tone: "red" },
  unknown: { label: "Too early", tone: "grey" },
};

const num = (v?: number | null) => (v === undefined || v === null ? "–" : v.toLocaleString(undefined, { maximumFractionDigits: 0 }));
const pct = (v?: number | null) => (v === undefined || v === null ? "–" : `${v.toLocaleString(undefined, { maximumFractionDigits: 1 })}%`);
const weeks = (w: number) => (w < 9 ? `about ${w} week${w === 1 ? "" : "s"}` : `about ${Math.round(w / 4.35)} months`);

export function Competition(props: Props) {
  const { brand, brands, loaded } = useBrand(props.ws);
  if (!loaded) return <Loading />;
  if (!brand) return <PickBrand what="Competitors" brands={brands.length} />;
  return <CompetitionBrand key={brand.id} {...props} brand={brand.id} />;
}

function CompetitionBrand({ go, item, brand }: Props & { brand: string }) {
  const [data, setData] = useState<Data | null>(null);
  const [error, setError] = useState("");
  const [weights, setWeights] = useState<Record<string, number>>(savedWeights);
  const [editing, setEditing] = useState(false);
  const query = Object.entries(weights)
    .filter(([, v]) => v !== 1)
    .map(([k, v]) => `${k}:${v}`)
    .join(",");

  useEffect(() => {
    let live = true;
    api
      .GET("/v1/brands/{brand}/competition", { params: { path: { brand }, query: query ? { weights: query } : {} } })
      .then(({ data, error }) => {
        if (!live) return;
        if (error || !data) return setError(errorMessage(error));
        setError("");
        setData(data);
      })
      .catch(() => live && setError(errorMessage(undefined)));
    return () => {
      live = false;
    };
  }, [brand, query]);

  const setWeight = (k: string, v: number) => {
    const next = { ...weights, [k]: v };
    setWeights(next);
    try {
      localStorage.setItem(weightsKey, JSON.stringify(next));
    } catch {
      // Kept for this visit only.
    }
  };

  const picked = item && data?.entries.find((e) => e.id === item && e.kind === "competitor");
  if (error) return <div className="space-y-6"><PageHeader title="Who is dominating" /><ErrorText>{error}</ErrorText></div>;
  if (!data) return <div className="space-y-6"><PageHeader title="Who is dominating" /><Loading /></div>;
  if (picked) return <CompetitorPage data={data} them={picked} go={go} brand={brand} />;

  const header = (
    <PageHeader
      title="Who is dominating"
      description="Your rank against every competitor you track, where you lead and trail, and whether you're catching up. From stored data, so it costs nothing to open."
      action={<Button variant="ghost" onClick={() => setEditing((e) => !e)}>{editing ? "Done" : "What counts"}</Button>}
    />
  );
  if (data.tracked === 0)
    return (
      <div className="space-y-6">
        {header}
        <Empty title="Track your competitors first" action={<Button onClick={() => go("competitors")}>Add competitors</Button>}>
          Add the companies you compete with, or say yes to the ones Outflare suggests. Once they're on your list, this page ranks you against them.
        </Empty>
      </div>
    );
  const measured = data.areas.filter((a) => a.measured);
  const missing = data.areas.filter((a) => !a.measured);
  const ranked = [...data.entries].sort((a, b) => (a.rank ?? 1e9) - (b.rank ?? 1e9));
  const you = data.entries.find((e) => e.kind === "brand")!;

  return (
    <div className="space-y-8">
      {header}
      {editing && <Weights areas={data.areas} weights={weights} set={setWeight} />}
      <Card title="Overall ranking" description="A score out of 100 for each brand. 100 means it leads every area. The arrow shows how its rank moved over the last 28 days.">
        <div className="-mx-4 overflow-x-auto">
          <table className="w-full min-w-[640px] text-[13px] tabular-nums">
            <thead>
              <tr className="border-b text-left font-mono text-[11px] uppercase tracking-wide text-muted-foreground">
                <th className="px-4 py-2 font-medium">Rank</th>
                <th className="px-2 py-2 font-medium">Brand</th>
                <th className="px-2 py-2 font-medium">Score</th>
                {measured.map((a) => <th key={a.key} className="px-2 py-2 text-right font-medium">{a.label}</th>)}
              </tr>
            </thead>
            <tbody>
              {ranked.map((e) => (
                <tr
                  key={e.id}
                  className={`border-b last:border-0 ${e.kind === "brand" ? "bg-[var(--series-1)]/8 font-semibold" : "cursor-pointer hover:bg-muted/50"}`}
                  onClick={e.kind === "competitor" ? () => go("competition", e.id) : undefined}
                >
                  <td className="px-4 py-2.5"><Rank e={e} /></td>
                  <td className="px-2 py-2.5">
                    <div>{e.kind === "brand" ? `${e.name} (you)` : e.name}</div>
                    {e.host && <div className="font-mono text-[11px] font-normal text-muted-foreground">{e.host}</div>}
                  </td>
                  <td className="px-2 py-2.5"><ScoreBar score={e.score} /></td>
                  {measured.map((a) => {
                    const v = e.areas[a.key];
                    return (
                      <td key={a.key} className="px-2 py-2.5 text-right">
                        {v ? <span title={`${num(v.value)} ${a.unit}`}>{pct(v.share)}{a.leader_id === e.id && <span className="ml-1 text-[11px] text-green-700 dark:text-green-400">Top</span>}</span> : <span className="text-muted-foreground">–</span>}
                      </td>
                    );
                  })}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <p className="mt-3 text-[12px] text-muted-foreground">Each area shows a brand's share of everyone's total. Select a competitor to see its page.</p>
      </Card>

      <Card title="Where you stand" description="Against the typical competitor, and whether your share grew over the last 28 days.">
        {data.standings.length === 0 ? (
          <p className="text-[13px] text-muted-foreground">Nothing to compare yet: these numbers appear once your competitors have been read.</p>
        ) : (
          <ul className="divide-y">
            {data.standings.map((s) => {
              const area = data.areas.find((a) => a.key === s.area);
              const passes = data.catch_ups.filter((c) => c.area === s.area);
              return (
                <li key={s.area} className="flex flex-wrap items-center gap-x-3 gap-y-1 py-2.5 text-[13px]">
                  <span className="w-44 font-medium">{area?.label}</span>
                  <Badge tone={standingLabel[s.standing].tone}>{standingLabel[s.standing].label}</Badge>
                  <Badge tone={trendLabel[s.trend].tone}>{trendLabel[s.trend].label}</Badge>
                  <span className="font-mono text-[12px] text-muted-foreground">{pct(s.share_before)} → {pct(s.share)}</span>
                  {passes.map((c) => {
                    const who = data.entries.find((e) => e.id === c.competitor_id)?.name ?? "a competitor";
                    return (
                      <span key={c.direction} className={`basis-full text-[12px] sm:basis-auto ${c.direction === "they_pass" ? "text-amber-700 dark:text-amber-400" : "text-green-700 dark:text-green-400"}`}>
                        {c.direction === "you_pass" ? `At this pace you pass ${who} in ${weeks(c.weeks)}.` : `At this pace ${who} passes you in ${weeks(c.weeks)}.`}
                      </span>
                    );
                  })}
                </li>
              );
            })}
          </ul>
        )}
      </Card>

      {measured.length > 0 && (
        <section className="space-y-3">
          <h2 className="text-[15px] font-semibold">Share of voice over the year</h2>
          <div className="grid gap-3 md:grid-cols-2">
            {measured.map((a) => <ShareChart key={a.key} area={a} data={data} focus={[you.id]} />)}
          </div>
        </section>
      )}
      {(missing.length > 0 || data.no_website) && (
        <ul className="space-y-1 text-[13px] text-muted-foreground">
          {data.no_website && <li>Add your website to the brand to compare search numbers. <button className="underline" onClick={() => go("brands")}>Open Brands</button></li>}
          {missing.map((a) => <li key={a.key}>{a.label}: no numbers yet. It needs {a.needs}.</li>)}
        </ul>
      )}
      <p className="text-[12px] text-muted-foreground">{data.notes.join(" ")}</p>
    </div>
  );
}

function Rank({ e }: { e: Entry }) {
  if (e.rank === null) return <span className="text-muted-foreground">–</span>;
  const moved = e.rank_before === null ? 0 : e.rank_before - e.rank;
  return (
    <span className="inline-flex items-baseline gap-1.5">
      <span>{e.rank}</span>
      {moved > 0 && <span className="text-[11px] text-green-700 dark:text-green-400" title={`Up ${moved} since 28 days ago`}>↑{moved}</span>}
      {moved < 0 && <span className="text-[11px] text-red-700 dark:text-red-400" title={`Down ${-moved} since 28 days ago`}>↓{-moved}</span>}
    </span>
  );
}

function ScoreBar({ score }: { score: number | null }) {
  if (score === null) return <span className="text-muted-foreground">Not measured</span>;
  return (
    <span className="flex items-center gap-2">
      <span className="h-1.5 w-20 overflow-hidden rounded-full bg-muted" aria-hidden="true">
        <span className="block h-full rounded-full bg-[var(--series-1)]" style={{ width: `${score}%` }} />
      </span>
      <span>{score}</span>
    </span>
  );
}

function Weights({ areas, weights, set }: { areas: Area[]; weights: Record<string, number>; set: (k: string, v: number) => void }) {
  return (
    <div className="rounded-lg border bg-card p-4 shadow-xs">
      <p className="mb-3 text-[13px]">How much each area counts in the overall score. This is kept in this browser.</p>
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {areas.map((a) => (
          <label key={a.key} className="flex items-center justify-between gap-3 text-[13px]" htmlFor={`weight-${a.key}`}>
            <span>{a.label}</span>
            <select id={`weight-${a.key}`} className="h-8 rounded-md border border-input bg-transparent px-2 text-[13px] dark:bg-input/30" value={weights[a.key] ?? 1} onChange={(e) => set(a.key, Number(e.target.value))}>
              <option value={0}>Doesn't count</option>
              <option value={1}>Normal</option>
              <option value={2}>Twice as much</option>
              <option value={3}>Three times</option>
            </select>
          </label>
        ))}
      </div>
    </div>
  );
}

// ShareChart draws each brand's share of one area week by week: the ones
// in focus and the current top three, each in its own colour.
function ShareChart({ area, data, focus }: { area: Area; data: Data; focus: string[] }) {
  const pts = data.series[area.key] ?? [];
  const lines = useMemo(() => {
    // Only ones measured in the area get a line: one with no profiles or
    // no data isn't at zero, it's unknown.
    const has = (id: string) => pts.some((p) => p.shares[id] !== undefined);
    const top = [...data.entries]
      .filter((e) => e.areas[area.key] && has(e.id))
      .sort((a, b) => (b.areas[area.key]?.share ?? 0) - (a.areas[area.key]?.share ?? 0))
      .map((e) => e.id);
    return [...new Set([...focus.filter(has), ...top])].slice(0, 4);
  }, [data, area.key, focus]);
  const W = 320, H = 120, P = 4;
  const max = Math.max(10, ...pts.flatMap((p) => lines.map((id) => p.shares[id] ?? 0)));
  const x = (i: number) => (pts.length > 1 ? P + (i / (pts.length - 1)) * (W - 2 * P) : W / 2);
  const y = (v: number) => H - P - (v / max) * (H - 2 * P);
  const name = (id: string) => {
    const e = data.entries.find((e) => e.id === id);
    return e?.kind === "brand" ? "You" : e?.name ?? id;
  };
  return (
    <div className="flex min-w-0 flex-col gap-2 rounded-lg border bg-card p-4 shadow-xs">
      <div className="flex items-baseline justify-between gap-2">
        <span className="font-mono text-[11px] uppercase tracking-wide text-muted-foreground">{area.label}</span>
        <span className="text-[11px] text-muted-foreground">share, up to {Math.round(max)}%</span>
      </div>
      {pts.length < 2 ? (
        <div className="h-[120px] text-[12px] text-muted-foreground">Too few weeks stored for a line yet.</div>
      ) : (
        <svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" className="h-[120px] w-full" role="img" aria-label={`${area.label}: share over time`}>
          {[0.5, 1].map((f) => <line key={f} x1={0} x2={W} y1={y(max * f)} y2={y(max * f)} stroke="var(--border)" strokeWidth={1} vectorEffect="non-scaling-stroke" />)}
          {lines.map((id, n) => (
            <polyline
              key={id}
              fill="none"
              stroke={`var(--series-${n + 1})`}
              strokeWidth={id === focus[0] ? 2.5 : 1.5}
              strokeLinejoin="round"
              vectorEffect="non-scaling-stroke"
              points={pts.map((p, i) => `${x(i)},${y(p.shares[id] ?? 0)}`).join(" ")}
            />
          ))}
        </svg>
      )}
      <div className="flex flex-wrap gap-x-3 gap-y-1 text-[12px]">
        {lines.map((id, n) => (
          <span key={id} className="inline-flex items-center gap-1.5">
            <span aria-hidden="true" className="size-2 rounded-full" style={{ background: `var(--series-${n + 1})` }} />
            {name(id)}
          </span>
        ))}
      </div>
    </div>
  );
}

// One competitor next to the brand: each area's value, share and who is
// ahead, and their share lines against yours.
function CompetitorPage({ data, them, go, brand }: { data: Data; them: Entry; go: Props["go"]; brand: string }) {
  const you = data.entries.find((e) => e.kind === "brand")!;
  const measured = data.areas.filter((a) => a.measured);
  return (
    <div className="space-y-8">
      <PageHeader
        title={them.name}
        description={them.host}
        back={{ label: "Who is dominating", onClick: () => go("competition") }}
        action={<Button variant="ghost" onClick={() => go("competitors")}>Edit in Competitors</Button>}
      />
      <div className="grid gap-3 sm:grid-cols-3">
        <Box label="Their score" value={them.score === null ? "–" : String(them.score)} sub={them.rank === null ? "Not ranked" : `Rank ${them.rank} of ${data.entries.length}`} />
        <Box label="Your score" value={you.score === null ? "–" : String(you.score)} sub={you.rank === null ? "Not ranked" : `Rank ${you.rank} of ${data.entries.length}`} />
        <Box label="Areas measured" value={String(them.measured_areas)} sub={`of ${data.areas.length}`} />
      </div>
      <Card title="Head to head" description="Your numbers next to theirs, and each one's share of everyone's total.">
        <div className="-mx-4 overflow-x-auto">
          <table className="w-full min-w-[560px] text-[13px] tabular-nums">
            <thead>
              <tr className="border-b text-left font-mono text-[11px] uppercase tracking-wide text-muted-foreground">
                <th className="px-4 py-2 font-medium">Area</th>
                <th className="px-2 py-2 text-right font-medium">You</th>
                <th className="px-2 py-2 text-right font-medium">{them.name}</th>
                <th className="px-4 py-2 font-medium">Who's ahead</th>
              </tr>
            </thead>
            <tbody>
              {data.areas.map((a) => {
                const mine = you.areas[a.key], theirs = them.areas[a.key];
                const lead = mine?.value != null && theirs?.value != null ? (mine.value > theirs.value ? "you" : mine.value < theirs.value ? "them" : "level") : null;
                return (
                  <tr key={a.key} className="border-b last:border-0">
                    <td className="px-4 py-2.5">
                      <div>{a.label}</div>
                      <div className="text-[11px] text-muted-foreground">{a.unit}</div>
                    </td>
                    <td className="px-2 py-2.5 text-right">{num(mine?.value)} <span className="text-[11px] text-muted-foreground">{pct(mine?.share)}</span></td>
                    <td className="px-2 py-2.5 text-right">{num(theirs?.value)} <span className="text-[11px] text-muted-foreground">{pct(theirs?.share)}</span></td>
                    <td className="px-4 py-2.5">
                      {lead === "you" && <Badge tone="green">You</Badge>}
                      {lead === "them" && <Badge tone="amber">{them.name}</Badge>}
                      {lead === "level" && <Badge tone="blue">Level</Badge>}
                      {lead === null && <span className="text-[12px] text-muted-foreground">{a.measured ? "Not measured for both" : `Needs ${a.needs}`}</span>}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      </Card>
      <ProfilesCard brand={brand} them={them} go={go} />
      {measured.length > 0 && (
        <section className="space-y-3">
          <h2 className="text-[15px] font-semibold">Your share against theirs</h2>
          <div className="grid gap-3 md:grid-cols-2">
            {measured.map((a) => <ShareChart key={a.key} area={a} data={data} focus={[you.id, them.id]} />)}
          </div>
        </section>
      )}
    </div>
  );
}

const channelName: Record<string, string> = {
  x: "X", linkedin: "LinkedIn", youtube: "YouTube", instagram: "Instagram", tiktok: "TikTok", facebook: "Facebook", reddit: "Reddit",
  github: "GitHub", discord: "Discord", bluesky: "Bluesky", threads: "Threads", mastodon: "Mastodon", dev_to: "DEV", docker_hub: "Docker Hub",
  trustpilot: "Trustpilot", g2: "G2", capterra: "Capterra", google: "Google reviews",
};
const isReviews = (kind: string) => ["trustpilot", "g2", "capterra", "google"].includes(kind);

// ProfilesCard: the competitor's social profiles and review pages. Ones
// linked from its home page are suggested for a yes or no; confirmed ones
// are read daily and fill the Social and Reviews areas.
function ProfilesCard({ brand, them, go }: { brand: string; them: Entry; go: Props["go"] }) {
  const [data, setData] = useState<Profiles | null>(null);
  const [error, setError] = useState("");
  const [url, setUrl] = useState("");
  const [busy, setBusy] = useState(false);
  const path = { brand, competitor: them.id };

  useEffect(() => {
    let live = true;
    api
      .GET("/v1/brands/{brand}/competition/{competitor}/profiles", { params: { path: { brand, competitor: them.id } } })
      .then(({ data, error }) => {
        if (!live) return;
        if (error || !data) return setError(errorMessage(error));
        setData(data);
      })
      .catch(() => live && setError(errorMessage(undefined)));
    return () => {
      live = false;
    };
  }, [brand, them.id]);

  const answer = async (p: Profile, state: "confirmed" | "dismissed") => {
    setBusy(true);
    const { data, error } = await api.PATCH("/v1/brands/{brand}/competition/{competitor}/profiles", { params: { path }, body: { url: p.url, state } });
    setBusy(false);
    if (error || !data) return setError(errorMessage(error));
    setError("");
    setData(data as Profiles);
  };
  const add = async () => {
    if (!url.trim()) return;
    setBusy(true);
    const { data, error } = await api.POST("/v1/brands/{brand}/competition/{competitor}/profiles", { params: { path }, body: { url: url.trim() } });
    setBusy(false);
    if (error || !data) return setError(errorMessage(error));
    setError("");
    setUrl("");
    setData(data as Profiles);
  };

  const suggested = data?.profiles.filter((p) => p.state === "suggested") ?? [];
  const followed = data?.profiles.filter((p) => p.state === "confirmed") ?? [];
  return (
    <Card title="Their social profiles and reviews" description="Followed every day, on your own Geonode account. Their numbers fill Social followers and Reviews above, never your own pages.">
      <div className="space-y-4">
        <ErrorText>{error}</ErrorText>
        {!data && !error && <Loading />}
        {data?.needs_reader && <ConnectFor connector="geonode" name="Geonode" back="competition" item={them.id} why={geonodeWhy.competitors} go={go} />}
        {suggested.length > 0 && (
          <div className="space-y-2">
            <div className="font-mono text-[11px] uppercase tracking-wide text-muted-foreground">Found on their website. Follow these?</div>
            {suggested.map((p) => (
              <div key={p.url} className="flex flex-wrap items-center gap-2 rounded-md border px-3 py-2 text-[13px]">
                <span className="font-medium">{channelName[p.kind] ?? p.kind}</span>
                <a href={p.url} target="_blank" rel="noreferrer" className="min-w-0 flex-1 truncate text-muted-foreground hover:underline">{p.url}</a>
                <Button disabled={busy} onClick={() => answer(p, "confirmed")}>Follow</Button>
                <Button variant="ghost" disabled={busy} onClick={() => answer(p, "dismissed")}>Not theirs</Button>
              </div>
            ))}
          </div>
        )}
        {data && followed.length === 0 && suggested.length === 0 && (
          <p className="text-[13px] text-muted-foreground">Nothing followed yet. OutflareBot suggests the profiles their home page links to after its weekly look, or add one below.</p>
        )}
        {followed.length > 0 && (
          <div className="-mx-4 overflow-x-auto">
            <table className="w-full min-w-[480px] text-[13px] tabular-nums">
              <tbody>
                {followed.map((p) => (
                  <tr key={p.url} className="border-b last:border-0">
                    <td className="px-4 py-2">
                      <div className="font-medium">{channelName[p.kind] ?? p.kind}</div>
                      <a href={p.url} target="_blank" rel="noreferrer" className="block max-w-[260px] truncate text-[11px] text-muted-foreground hover:underline">{p.url}</a>
                    </td>
                    <td className="px-2 py-2 text-right">
                      {isReviews(p.kind) ? (
                        <span>{p.score == null ? "–" : `${p.score.toFixed(1)} ★`} <span className="text-[11px] text-muted-foreground">{num(p.review_count)} reviews</span></span>
                      ) : (
                        <span>
                          {num(p.followers)} <span className="text-[11px] text-muted-foreground">followers</span>
                          {p.followers_change_28d != null && p.followers_change_28d !== 0 && (
                            <span className="ml-1 text-[11px] text-muted-foreground">({p.followers_change_28d > 0 ? "+" : ""}{num(p.followers_change_28d)} in 28 days)</span>
                          )}
                        </span>
                      )}
                      {p.last_error ? <div className="text-[11px] text-destructive">{p.last_error}</div> : !p.last_read_at && <div className="text-[11px] text-muted-foreground">Not read yet</div>}
                    </td>
                    <td className="px-4 py-2 text-right">
                      <Button variant="ghost" disabled={busy} onClick={() => answer(p, "dismissed")}>Stop following</Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        <form
          className="flex flex-wrap items-end gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            add();
          }}
        >
          <div className="min-w-[240px] flex-1">
            <Input label="Add a profile or review page" placeholder="https://x.com/their-handle" value={url} onChange={(e) => setUrl(e.target.value)} />
          </div>
          <Button type="submit" disabled={busy || !url.trim()}>Follow</Button>
        </form>
      </div>
    </Card>
  );
}

function Box({ label, value, sub }: { label: string; value: string; sub: string }) {
  return (
    <div className="rounded-lg border bg-card p-4 shadow-xs">
      <div className="font-mono text-[11px] uppercase tracking-wide text-muted-foreground">{label}</div>
      <div className="text-2xl font-semibold tabular-nums">{value}</div>
      <div className="text-[12px] text-muted-foreground">{sub}</div>
    </div>
  );
}
