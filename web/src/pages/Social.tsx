import { useCallback, useEffect, useState } from "react";
import type { Brand, BrandLinkSuggestion, Integration, SocialAccount, SocialGrowth, SocialPost } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { BrandBadge } from "@/components/brand-badge";
import { DataTable } from "@/components/data-table";
import { ConnectFor, geonodeWhy } from "@/components/connect-for";
import { PlatformIcon } from "@/components/platform-icon";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, confirmAction, ErrorText, field, Loading, PageHeader, Stat, Stats, toast } from "../ui";
import { ago } from "../format";
import type { Tab } from "../App";

// Social growth: the brand's own X accounts, LinkedIn company pages and
// LinkedIn profiles, read every day through the workspace's own X app (or,
// for followers only, the public profile page through its own Geonode
// account) and LinkedIn sign-in. Followers are kept day by day and each post's numbers
// over time, so growth and engagement show per channel and per brand.

type Props = { ws: string; canManage: boolean; go: (tab: Tab, item?: string) => void };
type Channel = SocialAccount["channel"];

const channels: { id: Channel; name: string }[] = [
  { id: "x", name: "X" }, { id: "linkedin", name: "LinkedIn" }, { id: "youtube", name: "YouTube" }, { id: "instagram", name: "Instagram" },
  { id: "tiktok", name: "TikTok" }, { id: "facebook", name: "Facebook" }, { id: "reddit", name: "Reddit" }, { id: "github", name: "GitHub" }, { id: "discord", name: "Discord" },
  { id: "bluesky", name: "Bluesky" }, { id: "threads", name: "Threads" }, { id: "mastodon", name: "Mastodon" }, { id: "dev_to", name: "DEV" }, { id: "docker_hub", name: "Docker Hub" },
];
const n = (v: number | null | undefined) => (v === null || v === undefined ? "—" : v.toLocaleString());
const change = (v: number | null | undefined) => (v === null || v === undefined ? undefined : `${v > 0 ? "+" : ""}${v.toLocaleString()}`);
const rate = (v: number | null | undefined) => (v === null || v === undefined ? "—" : `${(v * 100).toFixed(1)}%`);

export function Social({ ws, canManage, go }: Props) {
  const brand = useBrand(ws);
  const brandKey = brand.query.brand ?? "";
  const [growth, setGrowth] = useState<SocialGrowth | null>(null);
  const [accounts, setAccounts] = useState<SocialAccount[]>([]);
  const [posts, setPosts] = useState<SocialPost[]>([]);
  const [conns, setConns] = useState<Integration[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    const path = { ws };
    const query = brandKey ? { brand: brandKey } : {};
    const [g, a, p, i] = await Promise.all([
      api.GET("/v1/workspaces/{ws}/social/growth", { params: { path, query: { ...query, days: 28 } } }),
      api.GET("/v1/workspaces/{ws}/social/accounts", { params: { path, query } }),
      api.GET("/v1/workspaces/{ws}/social/posts", { params: { path, query: { ...query, days: 28, sort: "engagements", limit: 50 } } }),
      api.GET("/v1/workspaces/{ws}/integrations", { params: { path } }),
    ]);
    if (g.error || !g.data) return setError(errorMessage(g.error));
    setError("");
    setGrowth(g.data);
    setAccounts(a.data?.data ?? []);
    setPosts(p.data?.data ?? []);
    setConns((i.data?.data ?? []).filter((c) => c.connector === "x" || c.connector === "linkedin" || c.connector === "geonode"));
  }, [ws, brandKey]);
  useEffect(() => {
    load();
  }, [load]);

  async function readNow() {
    setBusy(true);
    const { error } = await api.POST("/v1/workspaces/{ws}/social/accounts/sync", { params: { path: { ws } } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast("Reading your accounts now. Numbers update in a minute.");
    setTimeout(load, 5000);
  }

  async function signIn(c: Integration) {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/integrations/{id}/sign-in", { params: { path: { ws, id: c.id }, query: { format: "json", return: "social" } } });
    if (error || !data) return toast(errorMessage(error), "error");
    location.assign((data as { url: string }).url);
  }

  async function update(a: SocialAccount, body: { status?: "active" | "paused"; brand_id?: string | null }) {
    const { error } = await api.PATCH("/v1/workspaces/{ws}/social/accounts/{id}", { params: { path: { ws, id: a.id } }, body });
    if (error) return toast(errorMessage(error), "error");
    load();
  }

  const header = (
    <PageHeader
      title="Social"
      description="Your brands' own social profiles, their followers day by day, and how your X and LinkedIn posts do."
      action={canManage && accounts.length > 0 && <Button variant="ghost" disabled={busy} onClick={readNow}>Read now</Button>}
    />
  );
  if (error && !growth) return <div className="space-y-6">{header}<ErrorText>{error}</ErrorText></div>;
  if (!growth) return <div className="space-y-6">{header}<Loading /></div>;

  const byChannel = (id: Channel) => growth.channels.find((c) => c.channel === id);
  const total = growth.channels.reduce(
    (t, c) => ({ followers: t.followers + c.followers, change: c.followers_change === null ? t.change : (t.change ?? 0) + c.followers_change, posts: t.posts + c.posts, impressions: t.impressions + c.impressions, engagements: t.engagements + c.engagements }),
    { followers: 0, change: null as number | null, posts: 0, impressions: 0, engagements: 0 },
  );

  return (
    <div className="space-y-6">
      {header}
      {error && <ErrorText>{error}</ErrorText>}

      <YourAccounts ws={ws} brands={brand.brand ? [brand.brand] : brand.active} accounts={accounts} conns={conns} canManage={canManage} go={go} signIn={signIn} onChange={load} />

      {accounts.length > 0 && <>
      <Stats cols={5}>
        <Stat label="Followers" value={total.followers.toLocaleString()} sub={change(total.change) && `${change(total.change)} in 28 days`} tone={total.change !== null && total.change < 0 ? "red" : undefined} />
        <Stat label="Posts, 28 days" value={total.posts.toLocaleString()} />
        <Stat label="Impressions" value={total.impressions.toLocaleString()} />
        <Stat label="Engagements" value={total.engagements.toLocaleString()} />
        <Stat label="Engagement rate" value={rate(total.impressions ? total.engagements / total.impressions : null)} />
      </Stats>

      {growth.followers_by_day.length > 1 && (
        <Card title="Followers" description="Every account together, as read each day.">
          <Line points={growth.followers_by_day.map((d) => d.followers)} first={growth.followers_by_day[0].day} last={growth.followers_by_day[growth.followers_by_day.length - 1].day} />
        </Card>
      )}

      {growth.channels.length > 0 && (
        <Card title="By channel" description="The last 28 days.">
          <DataTable
            data={growth.channels}
            rowId={(c) => c.channel}
            columnsMenu={false}
            columns={[
              { id: "channel", header: "Channel", accessorFn: (c) => c.channel, cell: ({ row: { original: c } }) => <span className="font-medium">{channels.find((x) => x.id === c.channel)?.name ?? c.channel}</span> },
              { id: "followers", header: "Followers", accessorFn: (c) => c.followers, cell: ({ row: { original: c } }) => <span className="font-mono tabular-nums">{n(c.followers)} <span className="text-muted-foreground">{change(c.followers_change)}</span></span> },
              { id: "posts", header: "Posts", accessorFn: (c) => c.posts, cell: ({ row: { original: c } }) => <span className="font-mono tabular-nums">{n(c.posts)}</span> },
              { id: "impressions", header: "Impressions", accessorFn: (c) => c.impressions, cell: ({ row: { original: c } }) => <span className="font-mono tabular-nums">{n(c.impressions)}</span> },
              { id: "engagements", header: "Engagements", accessorFn: (c) => c.engagements, cell: ({ row: { original: c } }) => <span className="font-mono tabular-nums">{n(c.engagements)} <span className="text-muted-foreground">{rate(byChannel(c.channel)?.engagement_rate)}</span></span> },
            ]}
          />
        </Card>
      )}

      </>}

      {accounts.length > 0 && <Card title="Accounts" description="Each page and profile your connections reach. Pause one to stop reading it; give it a brand so its numbers show there.">
        <DataTable
          data={accounts}
          rowId={(a) => a.id}
          searchPlaceholder="Search accounts"
          empty="Connect X or LinkedIn above. Accounts appear after the first read."
          columns={[
            {
              id: "account",
              header: "Account",
              accessorFn: (a) => `${a.name} ${a.handle}`,
              cell: ({ row: { original: a } }) => (
                <div className="flex items-center gap-2">
                  {a.avatar_url && <img src={a.avatar_url} alt="" className="size-6 rounded-full" referrerPolicy="no-referrer" />}
                  <div className="space-y-0.5">
                    <a className="font-medium hover:underline" href={a.url} target="_blank" rel="noreferrer noopener">{a.name || a.handle}</a>
                    <div className="text-[12px] text-muted-foreground">
                      {channels.find((x) => x.id === a.channel)?.name} {a.kind === "page" ? "page" : "profile"}
                      {a.status !== "active" && <> · <Badge tone={a.status === "disconnected" ? "red" : "grey"}>{a.status === "paused" ? "Paused" : "Disconnected"}</Badge></>}
                    </div>
                  </div>
                </div>
              ),
            },
            { id: "followers", header: "Followers", accessorFn: (a) => a.followers ?? -1, cell: ({ row: { original: a } }) => <span className="font-mono tabular-nums">{n(a.followers)}</span> },
            { id: "change1", header: "Today", accessorFn: (a) => a.followers_change_1d ?? 0, cell: ({ row: { original: a } }) => <span className="font-mono tabular-nums">{change(a.followers_change_1d) ?? "—"}</span> },
            { id: "change", header: "7 days", accessorFn: (a) => a.followers_change_7d ?? 0, cell: ({ row: { original: a } }) => <span className="font-mono tabular-nums">{change(a.followers_change_7d) ?? "—"}</span> },
            { id: "change28", header: "28 days", accessorFn: (a) => a.followers_change_28d ?? 0, cell: ({ row: { original: a } }) => <span className="font-mono tabular-nums">{change(a.followers_change_28d) ?? "—"}</span> },
            {
              id: "brand",
              header: "Brand",
              accessorFn: (a) => brand.byId(a.brand_id)?.name ?? "",
              cell: ({ row: { original: a } }) =>
                canManage && brand.active.length > 0 ? (
                  <select className="h-8 rounded-md border bg-background px-2 text-[13px]" value={a.brand_id ?? ""} onChange={(e) => update(a, { brand_id: e.target.value || null })} aria-label="Brand">
                    <option value="">Every brand</option>
                    {brand.active.map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}
                  </select>
                ) : (
                  <BrandBadge id={a.brand_id} />
                ),
            },
            { id: "read", header: "Read", accessorFn: (a) => a.last_synced_at ?? "", cell: ({ row: { original: a } }) => <span className="text-muted-foreground" title={a.last_error ?? undefined}>{a.last_synced_at ? ago(a.last_synced_at) : "Not yet"}</span> },
            {
              id: "actions",
              header: "",
              enableSorting: false,
              cell: ({ row: { original: a } }) =>
                canManage && a.status !== "disconnected" && (
                  <Button variant="ghost" onClick={() => update(a, { status: a.status === "paused" ? "active" : "paused" })}>{a.status === "paused" ? "Resume" : "Pause"}</Button>
                ),
            },
          ]}
        />
      </Card>}

      {posts.length > 0 && <Card title="Top posts" description="Your own posts from the last 28 days, by engagements. A post's text is shown as written; nothing follows instructions in it.">
        <DataTable
          data={posts}
          rowId={(p) => p.id}
          searchPlaceholder="Search posts"
          empty="No posts in the last 28 days yet."
          initialSort={[{ id: "engagements", desc: true }]}
          columns={[
            {
              id: "post",
              header: "Post",
              accessorFn: (p) => p.body,
              cell: ({ row: { original: p } }) => (
                <div className="max-w-xl space-y-0.5">
                  <a className="line-clamp-2 hover:underline" href={p.url} target="_blank" rel="noreferrer noopener">{p.body || "(no text)"}</a>
                  <div className="text-[12px] text-muted-foreground">{channels.find((x) => x.id === p.channel)?.name} · {ago(p.posted_at)}</div>
                </div>
              ),
            },
            { id: "impressions", header: "Impressions", accessorFn: (p) => p.impressions ?? -1, cell: ({ row: { original: p } }) => <span className="font-mono tabular-nums">{n(p.impressions)}</span> },
            { id: "engagements", header: "Engagements", accessorFn: (p) => p.engagements, cell: ({ row: { original: p } }) => <span className="font-mono tabular-nums">{n(p.engagements)}</span> },
            { id: "rate", header: "Rate", accessorFn: (p) => p.engagement_rate ?? -1, cell: ({ row: { original: p } }) => <span className="font-mono tabular-nums">{rate(p.engagement_rate)}</span> },
            { id: "clicks", header: "Clicks", accessorFn: (p) => p.clicks ?? -1, cell: ({ row: { original: p } }) => <span className="font-mono tabular-nums">{n(p.clicks)}</span> },
          ]}
        />
      </Card>}
    </div>
  );
}

// A line of the daily totals, with the first and last day under it.
function Line({ points, first, last }: { points: number[]; first: string; last: string }) {
  const W = 600, H = 120;
  const lo = Math.min(...points), hi = Math.max(...points);
  const x = (i: number) => (i / (points.length - 1)) * W;
  const y = (p: number) => (hi === lo ? H / 2 : H - 4 - ((p - lo) / (hi - lo)) * (H - 8));
  return (
    <div className="space-y-1">
      <svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" className="h-32 w-full overflow-visible" role="img" aria-label={`Followers from ${lo.toLocaleString()} to ${hi.toLocaleString()}`}>
        <polyline fill="none" stroke="var(--series-1)" strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" vectorEffect="non-scaling-stroke" points={points.map((p, i) => `${x(i)},${y(p)}`).join(" ")} />
      </svg>
      <div className="flex justify-between font-mono text-[11px] text-muted-foreground tabular-nums">
        <span>{first} · {points[0].toLocaleString()}</span>
        <span>{last} · {points[points.length - 1].toLocaleString()}</span>
      </div>
    </div>
  );
}

// The first step on Social, and the cold start: each brand's own social
// profiles, from its links or found on its website, to confirm, add, edit
// or remove right here. Every public profile's followers are read once a
// day through the workspace's own Geonode account (X and LinkedIn also
// through their own app or sign-in, with posts), so growth shows day by day.
const socialKinds = ["x", "linkedin", "youtube", "instagram", "tiktok", "facebook", "threads", "bluesky", "mastodon", "reddit", "discord", "github", "docker_hub", "dev_to"] as const;
type SocialKind = (typeof socialKinds)[number];
const kindName: Record<SocialKind, string> = {
  x: "X", linkedin: "LinkedIn", youtube: "YouTube", instagram: "Instagram", tiktok: "TikTok", facebook: "Facebook", threads: "Threads", bluesky: "Bluesky",
  mastodon: "Mastodon", reddit: "Reddit", discord: "Discord", github: "GitHub", docker_hub: "Docker Hub", dev_to: "DEV",
};
// What each channel's headline count is (the engine keeps it as followers).
const unit: Partial<Record<SocialKind, string>> = { youtube: "subscribers", reddit: "members", discord: "members", github: "stars", docker_hub: "pulls", dev_to: "posts" };
// X and LinkedIn are the profiles most brands have: shown even when missing.
const usual: readonly SocialKind[] = ["x", "linkedin"];

// The account id the engine gives a profile address (sociallinks.PublicProfile).
const profileID = (kind: string, url: string) => {
  try {
    const u = new URL(url);
    const parts = u.pathname.split("/").filter(Boolean).map((p) => p.toLowerCase());
    if (kind === "bluesky" || kind === "docker_hub") return parts[1] ?? "";
    if (kind === "mastodon") return `${u.hostname.toLowerCase()}/${parts[0] ?? ""}`;
    if (kind === "linkedin" || kind === "reddit" || (kind === "youtube" && parts[0] && !parts[0].startsWith("@"))) return parts.slice(0, 2).join("/");
    if (kind === "discord" && parts[0] === "invite") return parts[1] ?? "";
    return (parts[0] ?? "").replace(/^@/, kind === "youtube" ? "@" : "");
  } catch {
    return "";
  }
};
// Profiles whose followers aren't public: a person on LinkedIn, a person on
// Reddit, a Discord server's channel link (its members need an invite link).
const notPublic = (kind: string, url: string) => (kind === "linkedin" && !/linkedin\.com\/(company|showcase)\//i.test(url)) || (kind === "reddit" && !/reddit\.com\/r\//i.test(url)) || (kind === "discord" && /discord\.com\/channels\//i.test(url));
const short = (url: string) => url.replace(/^https?:\/\/(www\.)?/, "").replace(/\/$/, "");
// The Mastodon servers Outflare recognises (sociallinks.KindHosts).
const mastodonHosts = ["mastodon.social", "mastodon.online", "mas.to", "fosstodon.org", "hachyderm.io", "infosec.exchange", "mstdn.social"];
const kindOfURL = (url: string): SocialKind | undefined => {
  try {
    const h = new URL(url).hostname.replace(/^(www|m)\./, "");
    return ({ "x.com": "x", "twitter.com": "x", "linkedin.com": "linkedin", "youtube.com": "youtube", "instagram.com": "instagram", "tiktok.com": "tiktok", "facebook.com": "facebook", "reddit.com": "reddit", "github.com": "github", "discord.gg": "discord", "discord.com": "discord",
      "bsky.app": "bluesky", "threads.com": "threads", "threads.net": "threads", "dev.to": "dev_to", "hub.docker.com": "docker_hub", ...Object.fromEntries(mastodonHosts.map((m) => [m, "mastodon"])) } as Record<string, SocialKind>)[h];
  } catch {
    return undefined;
  }
};
const withScheme = (v: string) => (/^https?:\/\//i.test(v.trim()) ? v.trim() : `https://${v.trim()}`);

function YourAccounts({ ws, brands, accounts, conns, canManage, go, signIn, onChange }: {
  ws: string; brands: Brand[]; accounts: SocialAccount[]; conns: Integration[]; canManage: boolean;
  go: (tab: Tab, item?: string) => void; signIn: (c: Integration) => void; onChange: () => void;
}) {
  const [found, setFound] = useState<Record<string, { data: BrandLinkSuggestion[]; can_discover: boolean }>>({});
  const [busy, setBusy] = useState("");
  const [editing, setEditing] = useState<{ brand: string; url: string; value: string } | null>(null);
  const [adding, setAdding] = useState({ brand: "", url: "" });
  const { reload } = useBrand(ws);
  const ids = brands.map((b) => b.id).join(",");

  const loadFound = useCallback(async () => {
    const all = await Promise.all(brands.map(async (b) => {
      const { data } = await api.GET("/v1/workspaces/{ws}/brands/{brand}/link-suggestions", { params: { path: { ws, brand: b.id } } });
      return [b.id, { data: (data?.data ?? []).filter((x) => x.status === "suggested"), can_discover: !!data?.can_discover }] as const;
    }));
    setFound(Object.fromEntries(all));
  }, [ws, ids]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    loadFound();
  }, [loadFound]);

  const geonode = conns.some((c) => c.connector === "geonode" && c.status !== "error");
  const xApp = conns.some((c) => c.connector === "x" && c.status !== "error");
  const linkedIn = conns.some((c) => c.connector === "linkedin" && c.status !== "error");
  const canRead = (kind: SocialKind) => geonode || (kind === "x" && xApp) || (kind === "linkedin" && linkedIn);
  const xWaiting = conns.filter((c) => c.connector === "x" && !accounts.some((a) => a.integration_id === c.id));

  // A changed or confirmed profile is read straight away when something
  // can read it (the engine queues the read); show it once it's in.
  async function readSoon(kind?: SocialKind) {
    if (kind && canRead(kind)) setTimeout(onChange, 6000);
  }
  async function refresh() {
    await Promise.all([reload(), loadFound()]);
    onChange();
  }

  async function saveLinks(b: Brand, links: Brand["links"], done: string, kind?: SocialKind) {
    setBusy(b.id);
    const { error } = await api.PATCH("/v1/workspaces/{ws}/brands/{brand}", { params: { path: { ws, brand: b.id } }, body: { links } });
    setBusy("");
    if (error) {
      toast(errorMessage(error), "error");
      return false;
    }
    toast(done);
    await refresh();
    await readSoon(kind);
    return true;
  }

  // Adds every profile pasted (one or several addresses), to one brand.
  async function add() {
    const b = brands.find((x) => x.id === adding.brand) ?? brands[0];
    if (!b || !adding.url.trim()) return;
    const urls = adding.url.split(/[\s,]+|(?=https?:\/\/)/).filter((v) => v.trim()).map(withScheme);
    const links = [...b.links];
    const skipped: string[] = [];
    const kinds: SocialKind[] = [];
    for (const url of urls) {
      const kind = kindOfURL(url);
      if (!kind || links.some((l) => l.kind === kind && profileID(kind, l.url) === profileID(kind, url))) {
        skipped.push(short(url));
        continue;
      }
      links.push({ url, kind });
      kinds.push(kind);
    }
    if (kinds.length === 0) {
      return toast(urls.length === 1 && !kindOfURL(urls[0])
        ? `Paste the address of a profile on ${socialKinds.map((k) => kindName[k]).join(", ")}.`
        : `${b.name} already has ${urls.length === 1 ? "that profile" : "those profiles"}.`, "error");
    }
    const what = kinds.length === 1 ? "Added." : `Added ${kinds.length} profiles.`;
    const done = `${what}${kinds.some(canRead) ? " Reading their followers now." : ""}${skipped.length ? ` Skipped ${skipped.join(", ")} (already there or not a profile).` : ""}`;
    if (await saveLinks(b, links, done, kinds.find(canRead))) setAdding({ brand: adding.brand, url: "" });
  }

  async function edit() {
    if (!editing) return;
    const b = brands.find((x) => x.id === editing.brand);
    const url = withScheme(editing.value);
    const kind = kindOfURL(url);
    if (!b) return;
    if (!kind) return toast("That isn't a social profile address.", "error");
    if (await saveLinks(b, b.links.map((l) => (l.url === editing.url ? { ...l, url, kind } : l)), "Saved.", kind)) setEditing(null);
  }

  async function remove(b: Brand, url: string, kind: SocialKind) {
    if (!(await confirmAction(`Remove ${short(url)}?`, <p>It comes off {b.name}'s links and stops being read. Its history stays.</p>, "Remove"))) return;
    await saveLinks(b, b.links.filter((l) => l.url !== url), "Removed.", kind);
  }

  async function decide(b: Brand, x: BrandLinkSuggestion, how: "add" | "dismiss") {
    setBusy(x.id);
    const res = how === "add"
      ? await api.POST("/v1/workspaces/{ws}/brands/{brand}/link-suggestions/{suggestion}/add", { params: { path: { ws, brand: b.id, suggestion: x.id } }, body: {} })
      : await api.POST("/v1/workspaces/{ws}/brands/{brand}/link-suggestions/{suggestion}/dismiss", { params: { path: { ws, brand: b.id, suggestion: x.id } } });
    setBusy("");
    if (res.error) return toast(errorMessage(res.error), "error");
    const kind = x.kind as SocialKind;
    toast(how === "dismiss" ? "Dismissed." : canRead(kind) ? "Confirmed. Reading its followers now." : "Confirmed.");
    await refresh();
    if (how === "add") await readSoon(kind);
  }

  async function discover(b: Brand) {
    setBusy(b.id);
    const { error } = await api.POST("/v1/workspaces/{ws}/brands/{brand}/links/discover", { params: { path: { ws, brand: b.id } } });
    if (error) {
      setBusy("");
      return toast(errorMessage(error), "error");
    }
    // Reading the website takes a few seconds.
    for (let i = 0; i < 8; i++) {
      await new Promise((r) => setTimeout(r, 2500));
      const { data } = await api.GET("/v1/workspaces/{ws}/brands/{brand}/link-suggestions", { params: { path: { ws, brand: b.id } } });
      if (data?.data.some((x) => x.status === "suggested" && (socialKinds as readonly string[]).includes(x.kind))) break;
    }
    setBusy("");
    loadFound();
  }

  // How a confirmed profile is going: its followers, or what reading it needs.
  function status(kind: SocialKind, url: string) {
    if (notPublic(kind, url)) {
      return <span className="text-muted-foreground">{kind === "linkedin" ? (linkedIn ? "Read through your LinkedIn sign-in" : "A person's profile: connect LinkedIn to read it") : kind === "discord" ? "Use an invite link (discord.gg/…) to read its members" : "A person's profile shows no followers"}</span>;
    }
    const id = profileID(kind, url);
    const a = accounts.find((x) => x.channel === kind && x.status !== "disconnected" && (x.external_id.toLowerCase() === id || x.handle.toLowerCase() === id.split("/").pop()?.replace(/^@/, "")));
    if (a?.last_synced_at && a.followers !== null) {
      return (
        <span className="text-muted-foreground">
          <span className="font-mono tabular-nums text-foreground">{n(a.followers)}</span> {unit[kind] ?? "followers"}
          {a.followers_change_1d !== null && a.followers_change_1d !== undefined && <span className={a.followers_change_1d < 0 ? "text-red-600 dark:text-red-400" : "text-green-700 dark:text-green-400"}> {change(a.followers_change_1d)} today</span>}
          {" "}· read {ago(a.last_synced_at)}
        </span>
      );
    }
    if (a?.last_error) return <span className="text-amber-700 dark:text-amber-300" title={a.last_error}>Couldn't read it yet. Tried again tomorrow.</span>;
    if (canRead(kind)) return <span className="text-muted-foreground">First read within a few minutes</span>;
    return <span className="text-muted-foreground">Waiting for Geonode</span>;
  }

  const row = "flex flex-wrap items-center justify-between gap-2";
  const confirmed = brands.some((b) => b.links.some((l) => (socialKinds as readonly string[]).includes(l.kind ?? "")));
  return (
    <Card title="Your accounts" description="Each brand's own social profiles. Add, change or remove them here; their followers are read every day, so growth shows day by day.">
      {!geonode && confirmed && (
        <div className="mb-3 flex flex-wrap items-center justify-between gap-3 rounded-lg border bg-muted/40 px-3 py-2.5 text-[13px]">
          <span>Followers are read through your own Geonode account. Connect it once and every profile below is read today, then every day.</span>
          {canManage && <ConnectFor connector="geonode" name="Geonode" back="social" why={geonodeWhy.social} go={go} />}
        </div>
      )}
      {brands.length === 0 ? (
        <p className="text-[13px] text-muted-foreground">Add a brand with its website first. <button className="underline" onClick={() => go("brands")}>Add a brand</button></p>
      ) : (
        <ul className="divide-y text-[13px]">
          {brands.flatMap((b) => socialKinds.map((kind) => {
            const links = b.links.filter((l) => l.kind === kind);
            const waiting = (found[b.id]?.data ?? []).filter((x) => x.kind === kind && !links.some((l) => profileID(kind, l.url) === profileID(kind, x.url)));
            if (!usual.includes(kind) && links.length === 0 && waiting.length === 0) return [];
            const label = <span className="flex min-w-0 items-center gap-2.5" title={kindName[kind]}><PlatformIcon kind={kind} />{brands.length > 1 && <span className="font-medium">{b.name}</span>}</span>;
            return (
              <li key={`${b.id}:${kind}`} className="space-y-2 py-3 first:pt-0">
                {links.map((l) => editing?.brand === b.id && editing.url === l.url ? (
                  <form key={l.url} className={row} onSubmit={(e) => { e.preventDefault(); edit(); }}>
                    <span className="flex min-w-0 flex-1 items-center gap-2.5">{label}<input autoFocus aria-label={`${kindName[kind]} address`} className={`${field} min-w-0 flex-1 font-mono text-[12px]`} value={editing.value} onChange={(e) => setEditing({ ...editing, value: e.target.value })} /></span>
                    <span className="flex gap-2"><Button disabled={busy === b.id}>Save</Button><Button type="button" variant="ghost" onClick={() => setEditing(null)}>Cancel</Button></span>
                  </form>
                ) : (
                  <div key={l.url} className={row}>
                    <span className="flex min-w-0 items-center gap-2.5">{label}<a className="truncate hover:underline" href={l.url} target="_blank" rel="noreferrer noopener">{short(l.url)}</a></span>
                    <span className="flex flex-wrap items-center gap-2">
                      {status(kind, l.url)}
                      {canManage && <>
                        <Button variant="ghost" onClick={() => setEditing({ brand: b.id, url: l.url, value: l.url })}>Edit</Button>
                        <Button variant="ghost" disabled={busy === b.id} onClick={() => remove(b, l.url, kind)}>Remove</Button>
                      </>}
                    </span>
                  </div>
                ))}
                {waiting.map((x) => (
                  <div key={x.id} className={row}>
                    <span className="flex min-w-0 items-center gap-2.5">{label}<a className="truncate hover:underline" href={x.url} target="_blank" rel="noreferrer noopener">{short(x.url)}</a><span className="text-muted-foreground">found on the website</span></span>
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
                    <span className="flex min-w-0 items-center gap-2.5">{label}<span className="text-muted-foreground">No {kindName[kind]} account yet</span></span>
                    {canManage && found[b.id]?.can_discover && <Button variant="ghost" disabled={busy === b.id} onClick={() => discover(b)}>{busy === b.id ? "Reading the website…" : "Find it on the website"}</Button>}
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
          <input aria-label="Profile address" placeholder="Add profiles: paste one or more addresses, like x.com/northpeak" className={`${field} h-8 min-w-0 flex-1 text-[13px]`} value={adding.url} onChange={(e) => setAdding({ ...adding, url: e.target.value })} />
          <Button disabled={!adding.url.trim() || !!busy}>Add</Button>
        </form>
      )}
      {canManage && (
        <div className="mt-3 flex flex-wrap items-center gap-2 text-[12px] text-muted-foreground">
          {xWaiting.length > 0
            ? xWaiting.map((c) => <Button key={c.id} onClick={() => signIn(c)}>Sign in to X{xWaiting.length > 1 ? ` (${c.name})` : ""}</Button>)
            : <span>Followers need only Geonode. For X and LinkedIn posts and their numbers, add <button className="underline" onClick={() => go("connectors", "social")}>your own X app or LinkedIn sign-in</button>.</span>}
        </div>
      )}
    </Card>
  );
}
